package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tmux"
	"github.com/stempeck/agentfactory/internal/worktree"
)

var (
	donePhaseComplete bool
	doneGate          string
	doneSkip          string
)

var doneCmd = &cobra.Command{
	Use:   "done",
	Short: "Close current formula step and advance workflow",
	Long: `Done closes the current in-progress formula step and advances the workflow.

If more steps remain, it outputs the next step. If all steps are complete, it
mails WORK_DONE to the dispatcher and cleans up the checkpoint.

For gate steps, use --phase-complete --gate <id> to register as a gate waiter.
Run 'af prime' afterward to load your next step and continue.`,
	RunE: runDone,
}

func init() {
	doneCmd.Flags().BoolVar(&donePhaseComplete, "phase-complete", false, "Signal phase complete, await gate")
	doneCmd.Flags().StringVar(&doneGate, "gate", "", "Gate bead ID (required with --phase-complete)")
	doneCmd.Flags().StringVar(&doneSkip, "skip", "", "Close step with explicit skip reason (requires af prime)")
	rootCmd.AddCommand(doneCmd)
}

func runDone(cmd *cobra.Command, args []string) error {
	cwd, err := getWd()
	if err != nil {
		return err
	}

	return runDoneCore(cmd.Context(), cwd, donePhaseComplete, doneGate)
}

// runDoneCore contains the core logic for af done, separated from cobra for testability.
func runDoneCore(ctx context.Context, cwd string, phaseComplete bool, gate string) error {
	start := time.Now()

	// 1. Context discovery
	factoryRoot, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}

	// Recovery state: .runtime/hooked_formula is the source of truth for the active
	// formula instance. Checkpoint is NOT consulted here — it's informational only.
	instanceID := readHookedFormulaID(cwd)
	if instanceID == "" {
		return fmt.Errorf("no active formula (missing .runtime/hooked_formula)")
	}

	// 1a. Telemetry setup, behind one gate read. This verb never resolves an agent name on its
	// own account, but the record store refuses a record it cannot attribute, so the name is
	// resolved here and emission is skipped entirely when it cannot be. The export is deferred
	// rather than placed beside a recording site: a defer runs after every one of the return
	// paths below, so it sees everything this invocation recorded and still runs exactly once.
	// af done is the only verb that exports — af prime is a session-start hook and must not
	// put a network round trip in front of a session.
	// Identity is resolved ONCE, above the gate, because two consumers need it and they are gated
	// differently. The step records need it because the record store refuses a record it cannot
	// attribute. The #622 C5 step boundary needs it because the occupancy reader's roster check
	// admits no datum without a name — and that boundary is armed by statusline data alone, with
	// the telemetry gate off, which is the default. Resolving it inside the gate block would have
	// left the boundary permanently inert on most factories while every test still passed.
	agentName, agentErr := detectAgentName(cwd, factoryRoot)

	if telemetryFactoryEnabled(factoryRoot) {
		if agentErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not resolve the agent name to record step timing: %v\n", agentErr)
		} else {
			ctx = withVerbTelemetry(ctx, verbTelemetry{
				verb: "done", agent: agentName, start: start, enabled: true,
			})
			defer drainTelemetryBounded(factoryRoot, agentName)
		}
	}

	actor := os.Getenv("AF_ACTOR")
	store, err := newIssueStore(cwd, actor)
	if err != nil {
		return fmt.Errorf("initializing issue store: %w", err)
	}

	// 2. Find current step
	// Ready steps from the store are the work queue — the actual recovery mechanism, not checkpoint data.
	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: instanceID})
	if err != nil {
		return fmt.Errorf("querying formula steps: %w", err)
	}

	if len(result.Steps) == 0 {
		openChildren, err := store.List(ctx, issuestore.Filter{
			Parent:   instanceID,
			Statuses: []issuestore.Status{issuestore.StatusOpen},
		})
		if err != nil {
			return fmt.Errorf("listing open children: %w", err)
		}
		if len(openChildren) > 0 {
			return fmt.Errorf("no actionable steps (all remaining steps are blocked)")
		}
		// No open children at all — all steps complete, skip to WORK_DONE
		return sendWorkDoneAndCleanup(ctx, store, cwd, factoryRoot, instanceID, phaseComplete)
	}

	step := result.Steps[0]

	// 2a. Write last_closed_step identity BEFORE closing (for fidelity gate hook)
	closed, err := writeLastClosedStep(ctx, cwd, step, instanceID, store)
	if err != nil {
		return fmt.Errorf("writing last closed step: %w", err)
	}

	// 2b. Check close velocity for suspicious rapid-fire patterns (before prime
	// check so accumulated unprimed attempts eventually escalate)
	if err := checkDoneVelocity(cwd); err != nil {
		return err
	}

	// 2c. Verify step was primed (agent read instructions via af prime)
	if err := checkStepPrimed(ctx, cwd, step.ID, store); err != nil {
		_ = recordDoneTimestamp(cwd, step.ID, false, "")
		return err
	}

	// 3. Close current step
	if err := store.Close(ctx, step.ID, ""); err != nil {
		return fmt.Errorf("closing step %s: %w", step.ID, err)
	}
	fmt.Printf("✓ Step closed: %s\n", step.Title)

	// 3a. Record close timestamp for velocity tracking
	_ = recordDoneTimestamp(cwd, step.ID, true, "full_output")

	// 3b. The step is genuinely over: recorded here, beside the success marker, and not
	// alongside the last_closed_step write above. That write is a gate-hook input and is
	// deliberately made before closing; two guards run between it and the close, either of
	// which can reject this af done. A completion record for a step that was then rejected and
	// never closed is worse than no record at all, because nothing downstream could tell the
	// difference. Placed before the gate branch below so that a --phase-complete invocation
	// missing its --gate argument still records the close it really performed.
	// #622 C4/C5: one occupancy read serves both the closing record and the boundary decision
	// below. Taken before the record so the two cannot disagree about the same instant, and taken
	// outside the telemetry gate because the boundary is armed by statusline data alone.
	boundaryNow := time.Now()
	// LoadStartupConfig returns (nil, err) on a malformed file — absent is the case that yields
	// defaults, not unreadable — so stepCtx is derived once and every reader below goes through it
	// rather than through startupCfg. Its zero value carries HandoffPct 0, which
	// shouldBoundaryHandoff treats as unconfigured and never fires on: a factory whose bound cannot
	// be read must not be recycled against a bound nobody knows.
	startupCfg, startupErr := config.LoadStartupConfig(factoryRoot)
	closeReading := statusline.NoReading()
	var stepCtx config.StepContextConfig
	if startupErr == nil {
		closeReading = stepContextReading(factoryRoot, cwd, agentName, startupCfg.Recovery, boundaryNow)
		stepCtx = startupCfg.StepContext
	}

	if vt := verbTelemetryFrom(ctx); vt.enabled {
		ev := telemetryRecordFor(ctx, factoryRoot, cwd, vt.agent, instanceID, "")
		ev.Event = telemetry.EventStepEnd
		ev.Formula = telemetryFormulaName(closed.Formula)
		ev.StepID = step.ID
		ev.StepTitle = step.Title
		ev.Status = telemetry.StatusClosed
		if phaseComplete {
			ev.Status = telemetry.StatusGateWaiting
		}
		span := telemetryStepSpan(factoryRoot, vt.agent, instanceID, step.ID, ev.TS)
		ev.StepSeq, ev.DurationMS = span.seq, span.durationMS
		// A gate close records its occupancy like any other close (cross-review HIGH-2). The gate
		// contract excludes it from the HANDOFF, not from the measurement — a step that filled its
		// window and then hit a gate is exactly the step the improvement loop needs to see.
		attachStepOccupancy(&ev, closeReading, factoryRoot, boundaryNow)
		ev.CtxTokensStart = span.ctxTokensStart
		ev.CtxBoundTokens = int64(stepCtx.BoundTokens)
		ev.CumTokensDelta = stepCumTokensDelta(span, ev)
		appendTelemetryRecord(factoryRoot, ev)
	}

	// 4. Gate handling
	if phaseComplete {
		if gate == "" {
			return fmt.Errorf("--gate is required with --phase-complete")
		}
		// Try to close the gate bead as phase-complete signal
		_ = store.Close(ctx, gate, "")
		fmt.Printf("✓ Phase complete. Gate %s registered. Continue: run 'af prime' to load your next step.\n", gate)
	}

	openChildren, err := store.List(ctx, issuestore.Filter{
		Parent:   instanceID,
		Statuses: []issuestore.Status{issuestore.StatusOpen},
	})
	if err != nil {
		return fmt.Errorf("listing open children after close: %w", err)
	}
	if len(openChildren) > 0 {
		// More steps remain
		nextResult, nextErr := store.Ready(ctx, issuestore.Filter{MoleculeID: instanceID})
		if nextErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not query next step: %v\n", nextErr)
		} else if len(nextResult.Steps) > 0 {
			fmt.Printf("Next step: %s\n", nextResult.Steps[0].Title)
			fmt.Println("Run `af prime` NOW to load it. Do not end your turn — continue until all steps are done or a step is blocked.")
		} else {
			fmt.Println("Remaining steps are blocked. Waiting for dependencies.")
		}

		// #622 C5: the cooperative step boundary. LAST statement on this branch, because a
		// successful respawn replaces the pane this process is running in and nothing after it
		// would run. The step is already closed and its record already written, so there is no
		// in-flight work to lose — the boundary only ever recycles a session between steps.
		if shouldBoundaryHandoff(closeReading, stepCtx, phaseComplete, true) {
			runBoundaryHandoff(ctx, cwd, factoryRoot, "step "+step.ID, instanceID, closeReading, stepCtx, false)
		}
		return nil
	}

	// 6. All complete — mail WORK_DONE
	return sendWorkDoneAndCleanup(ctx, store, cwd, factoryRoot, instanceID, phaseComplete)
}

// stepCumTokensDelta answers how many tokens this step consumed, or refuses to answer.
//
// cum_tokens counts ONE session's lifetime, so the subtraction is only arithmetic when both ends
// were measured in the same session (cross-review HIGH-3). Across a mid-step recycle the new
// session's counter starts near zero and the difference comes out large and negative — a number
// that looks like a measurement, in the figure the improvement loop leans on hardest. Refusing is
// the only honest answer, and the report renders the refusal as its own state rather than as zero.
func stepCumTokensDelta(span stepSpan, ev telemetry.StepEvent) *int64 {
	if span.cumTokens == nil || ev.CumTokens == nil {
		return nil
	}
	if span.sessionID == "" || ev.SessionID == "" || span.sessionID != ev.SessionID {
		return nil
	}
	delta := *ev.CumTokens - *span.cumTokens
	if delta < 0 {
		// Same session id and a falling lifetime counter is not a consumption figure, it is a
		// contradiction. Suppress rather than record a negative the report would have to explain.
		return nil
	}
	return &delta
}

// shouldBoundaryHandoff is #622 C5's decision: may this step close by handing off to a clean
// session rather than leaving the next step to inherit a nearly-full window?
//
// Pure — no clock, no filesystem, no environment — so every cell of the decision matrix is
// exercisable directly. Five conditions must ALL hold, and each rules out a way this could fire
// when it should not:
//
//   - FRESH. A stale, dark, malformed or absent channel is not evidence of high occupancy; it is
//     evidence of nothing. Absence must never be able to trigger an action (Gap 3).
//   - SESSION-MATCHED. Not a parameter: it is delivered by construction, because the reading comes
//     from the session-keyed reader, which names the file from the caller's own raw session id. A
//     comparison here would put the raw id against a sanitized stem and could never match — the
//     #563 class, which would leave this function returning false forever with every test green.
//   - AT OR ABOVE handoff_pct. `>=`, so a threshold of exactly N fires at exactly N.
//   - NOT A GATE CLOSE (cross-review HIGH-2). A gate close is an input rather than a placement
//     accident: --phase-complete closes the gate bead and then STILL falls through to the
//     more-steps branch whenever other steps are open. The gate contract already ends the session
//     and dispatches a fresh agent when the gate resolves, so a handoff here would resurrect an
//     ended session into a blocked step.
//   - WORK FOLLOWS. Recycling a session with nothing left to do buys nothing and costs a respawn.
//     The spec states this conjunct as a disjunction — steps-remain OR improvement-fired-final —
//     and the two call sites supply one disjunct each: the more-steps branch knows steps are open,
//     and the completion branch knows an improvement session is about to inherit this window. The
//     parameter is named for what it decides rather than for either caller's evidence, so neither
//     site has to pass a value that reads as a lie.
//
// A zero handoff_pct means nobody configured this, not "hand off at 0%".
func shouldBoundaryHandoff(reading statusline.ChannelReading, cfg config.StepContextConfig, gateClose, workFollows bool) bool {
	if gateClose || !workFollows {
		return false
	}
	if cfg.HandoffPct < 1 {
		return false
	}
	if !reading.IsHealthy() {
		return false
	}
	pct, ok := reading.UsedPct()
	if !ok {
		// A false with a 0 is "no reading", never "empty context".
		return false
	}
	return pct >= float64(cfg.HandoffPct)
}

// boundaryHandoffMessage is the self-mail body a boundary handoff leaves for the session that
// inherits the fresh window. finalStep is the #622-C5 final-step case: the formula is already
// complete and an improvement session inherits (driven by the marker + urgent self-mail), so there
// is no next step to prime — the inheritor's action is to finish the improvement pass, not af prime.
func boundaryHandoffMessage(pct float64, after string, finalStep bool) string {
	if finalStep {
		return fmt.Sprintf("Context at %.0f%% after %s. Fresh session: the improvement session inherits — run af mail check, then af improvement complete.", pct, after)
	}
	return fmt.Sprintf("Context at %.0f%% after %s. Fresh session: run af prime for the next step.", pct, after)
}

// runBoundaryHandoff performs the cooperative boundary handoff, or declines it for a reason worth
// printing. Failure warns and returns: af done has already closed the step and must still exit 0,
// and a session that could not be recycled is merely one the forceful recovery ladder may catch
// later — which is the degradation this feature is layered above, not a new failure.
func runBoundaryHandoff(ctx context.Context, cwd, factoryRoot, after, instanceID string,
	reading statusline.ChannelReading, cfg config.StepContextConfig, finalStep bool) {
	// af done legitimately runs outside tmux — an operator shell, a test. No pane, no handoff.
	// Said out loud rather than declined silently: by the time this runs the decision has already
	// come out true, so an operator whose factory never hands off has nothing else to grep for.
	pane := os.Getenv("TMUX_PANE")
	if !tmux.IsInsideTmux(os.Getenv("TMUX")) || pane == "" {
		fmt.Fprintf(os.Stderr, "warning: step-boundary handoff skipped: not running in a tmux pane\n")
		return
	}
	agentName, agentEntry, err := detectRole(cwd, factoryRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: step-boundary handoff skipped: %v\n", err)
		return
	}

	pct, _ := reading.UsedPct()
	fmt.Printf("Context at %.0f%% after %s — handing off for a clean session.\n", pct, after)

	subject := "HANDOFF: step context boundary"
	message := boundaryHandoffMessage(pct, after, finalStep)

	// TriggerDetail is populated because this is the one cooperative class that KNOWS its
	// occupancy. crash, error_pattern, compact_handoff and self_handoff leave it zero because they
	// have no occupancy story; leaving it zero here would make the boundary indistinguishable from
	// them in the funnel log, and the read surface renders a zero observed_pct as UNKNOWN.
	detail := recycleDetail{ObservedPct: pct, ThresholdPct: cfg.HandoffPct, InstanceID: instanceID}
	if obs, ok := reading.Observation(); ok {
		// The sanitized stem, which is the spelling the funnel's own fence compares against.
		detail.SessionID = obs.SessionID()
	}

	if err := boundaryHandoffExec(ctx, cwd, RespawnOptions{
		FactoryRoot:   factoryRoot,
		AgentName:     agentName,
		AgentEntry:    *agentEntry,
		PaneID:        pane,
		AgentWorkDir:  cwd,
		Trigger:       triggerStepBoundaryHandoff,
		TriggerDetail: detail,
	}, subject, message); err != nil {
		fmt.Fprintf(os.Stderr, "warning: step-boundary handoff failed: %v\n", err)
	}
}

// boundaryHandoffExec runs the three legs of a boundary handoff: checkpoint, mail to self, respawn.
//
// It calls respawnSession DIRECTLY rather than exec'ing `af handoff`, which hardcodes
// Trigger: triggerSelfHandoff and has no flag or env that can say otherwise. An exec'd handoff
// would write the wrong class into the funnel log, and the whole value of a cooperative boundary
// is being able to tell it apart from the forceful ladder afterwards.
//
// Declared as an ADR-009 package-var seam because the alternative leaves this untestable: the
// nearest sibling, triggerHandoffRespawn, is a plain func nooped by isTestBinary(), so a test of it
// can only ever assert "it did not error" — never that it ran, never that a failure warned and let
// the verb succeed. Deliberately separate from doRespawn (recovery.go), whose doc scopes it to the
// watchdog executor's own route into the funnel; two callers sharing one seam would let a test of
// either silently substitute for the other.
var boundaryHandoffExec = func(ctx context.Context, cwd string, opts RespawnOptions, subject, message string) error {
	// Both legs are best-effort, mirroring af handoff: a checkpoint or a mailbox that failed is
	// worth saying out loud, but neither is a reason to leave a session running on a full window.
	if err := captureCheckpointWithFormula(ctx, cwd, subject, nil); err != nil {
		fmt.Fprintf(os.Stderr, "warning: checkpoint write failed: %v\n", err)
	}
	if err := sendHandoffMail(opts.AgentName, subject, message); err != nil {
		fmt.Fprintf(os.Stderr, "warning: mail send failed: %v\n", err)
	}
	return respawnSession(opts)
}

// sendWorkDoneAndCleanup sends the WORK_DONE mail and removes the checkpoint.
//
// gateClose carries runDoneCore's --phase-complete down to the #622 C5 final-step handoff. It is a
// parameter rather than a re-read because this function is reachable by two routes and neither can
// recover the flag afterwards: the all-complete branch closes the last step with the flag set, and
// nothing on disk afterwards distinguishes that from an ordinary close. AC-1 makes not-a-gate-close
// a conjunct of the WHOLE boundary rule, final step included, so dropping it here would leave one
// cell of the matrix firing where the spec says every gate-close cell is inert.
func sendWorkDoneAndCleanup(ctx context.Context, store issuestore.Store, cwd, factoryRoot, instanceID string, gateClose bool) error {
	totalSteps, totalErr := countAllChildren(ctx, store, instanceID)
	if totalErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not count total children: %v\n", totalErr)
	}

	closedChildren, closedErr := store.List(ctx, issuestore.Filter{
		Parent:        instanceID,
		Statuses:      []issuestore.Status{issuestore.StatusClosed, issuestore.StatusDone},
		IncludeClosed: true,
	})
	if closedErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not count closed children: %v\n", closedErr)
	}
	closedCount := len(closedChildren)
	if totalErr == nil && closedErr == nil && closedCount < totalSteps {
		fmt.Fprintf(os.Stderr, "warning: formula declared complete but only %d of %d steps were closed\n", closedCount, totalSteps)
	}

	// Get formula name from instance bead title.
	formulaName := instanceID
	if iss, err := store.Get(ctx, instanceID); err == nil && iss.Title != "" {
		formulaName = iss.Title
	}

	caller := readFormulaCaller(cwd)

	if guardTriggered, reason := checkFormulaCompletionVelocity(cwd); guardTriggered {
		fmt.Fprintf(os.Stderr, "GUARD: Formula completion blocked — %s\n", reason)
		escalationRecipient := caller
		if escalationRecipient == "" || escalationRecipient == "@cli" {
			if escalationRecipient == "@cli" {
				fmt.Fprintln(os.Stderr, "warning: formula caller is @cli (no agent mailbox); falling back to supervisor")
			} else {
				fmt.Fprintln(os.Stderr, "warning: no formula caller identity found; falling back to supervisor")
			}
			escalationRecipient = "supervisor"
		}
		if err := sendEscalationMail(escalationRecipient, instanceID, formulaName, reason); err != nil {
			fmt.Fprintf(os.Stderr, "GUARD: escalation mail to %s failed: %v — skipping respawn to preserve debuggable state\n", escalationRecipient, err)
			return fmt.Errorf("formula completion guard triggered: %s (escalation mail failed: %v)", reason, err)
		}
		triggerHandoffRespawn(cwd, factoryRoot)
		return fmt.Errorf("formula completion guard triggered: %s", reason)
	}

	// K5 (issue #392): genuine completion is now confirmed — the
	// completion-velocity guard passed above. Close the durable formula-instance
	// epic so that "an open formula-instance epic" reliably means "in flight" for
	// af up's K4 recovery query (the Gap-1 prerequisite, R1). Not gated on
	// shouldTerminate: an interactive, non-dispatched session that finishes its
	// formula must also close the epic. Best-effort (warn + continue) like every
	// other store/mail op here — the steps are already closed and the formula is
	// genuinely done, so a close failure must not abort completion. M1: this is
	// the formula-instance epic (af-<hex>), distinct from the GitHub work issue.
	if err := store.Close(ctx, instanceID, config.CloseReasonFormulaComplete); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not close formula-instance epic %s: %v\n", instanceID, err)
	}

	// K5a: the formula instance is over. Recorded here rather than on entry to this function,
	// because the completion-velocity guard above returns an error WITHOUT closing anything —
	// a run it rejects has not finished, and a record saying otherwise would make a blocked
	// formula indistinguishable from a completed one.
	if vt := verbTelemetryFrom(ctx); vt.enabled {
		ev := telemetryRecordFor(ctx, factoryRoot, cwd, vt.agent, instanceID, "")
		ev.Event = telemetry.EventInstanceEnd
		ev.Formula = telemetryFormulaName(formulaName)
		appendTelemetryRecord(factoryRoot, ev)
	}

	// K6 (issue #321): legacy in-flight rescue. A formula instance dispatched
	// before the Phase-1 fix persisted the unroutable "@cli" sentinel to
	// .runtime/formula_caller, and persistFormulaCaller's no-overwrite semantics
	// forbid re-synthesizing it. Heal it here at read time so the final WORK_DONE
	// delivers and the dispatched session auto-terminates. Placed strictly AFTER
	// the velocity guard so the C-2 escalation degrade (done.go:198-206) still
	// observes raw @cli -> supervisor; does NOT touch the empty-caller skip below
	// (empty != @cli — empty is the D1 "no dispatcher waiting" rule).
	if caller == "@cli" {
		fmt.Fprintln(os.Stderr, "warning: legacy @cli caller; routing WORK_DONE to manager")
		caller = fallbackCaller
	}

	// D1: no fallback. Per H-4/D15, a missing caller file means there is no
	// dispatcher waiting on WORK_DONE mail. Skip the send entirely. Pinned
	// by TestDone_NoCallerFile_NoMail.
	if caller == "" {
		fmt.Fprintln(os.Stderr, "no caller identity found; skipping WORK_DONE mail")
	}

	var mailErr error
	if caller != "" {
		mailErr = sendWorkDoneMail(caller, instanceID, formulaName, totalSteps)
		if mailErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to send WORK_DONE mail to %s for formula %s (%s): %v\n",
				caller, formulaName, instanceID, mailErr)
		}
	}

	// Read dispatch flag BEFORE cleanup removes the marker
	dispatched := isDispatchedSession(cwd)
	shouldTerminate := shouldAutoTerminate(dispatched, mailErr)

	if dispatched && !shouldTerminate {
		fmt.Fprintf(os.Stderr, "warning: skipping auto-terminate because WORK_DONE mail failed\n")
	}

	// Continuous-improvement hook (#483). On a qualifying final `af done`,
	// keep the session alive and deliver the /improve-agent instruction. The fire
	// condition is positional here — the velocity guard already returned above, and
	// `caller`, `dispatched`, and `shouldTerminate` are settled. Evaluate BEFORE
	// cleanupRuntimeArtifacts deletes the carrier files. The completion path stays
	// byte-identical whenever the toggles are off (the factory-toggle gate below), and
	// any error fails open (skip recorded, completion untouched).
	improvementFired := false
	var improvementInstr, improvementAgent string
	if caller != "" && improvementFactoryEnabled(factoryRoot) {
		fired, agent, instruction, reason := evaluateImprovementFire(cwd, factoryRoot, instanceID, caller, formulaName, shouldTerminate)
		switch {
		case fired:
			improvementFired = true
			improvementInstr = instruction
			improvementAgent = agent
			// Keep the session (and its worktree) alive; teardown and the lock
			// release defer to `af improvement complete`. terminate_on_complete in
			// the marker records the ORIGINAL shouldTerminate for that verb.
			shouldTerminate = false
		case reason != "":
			fmt.Fprintf(os.Stderr, "warning: improvement hook did not fire: %s\n", reason)
			_ = recordImprovementSkip(factoryRoot, agent, reason)
		}
	}

	// Clean up checkpoint and runtime artifacts.
	// Checkpoint is removed on workflow completion. It was never read during this
	// done flow — recovery is fully handled by .runtime/hooked_formula + store.Ready above.
	_ = checkpoint.Remove(cwd)
	cleanupRuntimeArtifacts(cwd)

	// Release identity lock (lock PID belongs to the Claude process from af prime,
	// not af done; Release() simply deletes the file regardless of PID). Deferred to
	// `af improvement complete` when the improvement hook fired and the session survives.
	if !improvementFired {
		_ = lock.New(cwd).Release()
	}

	if caller != "" && mailErr == nil {
		fmt.Println("✓ All formula steps complete. WORK_DONE mailed.")
	} else {
		fmt.Println("✓ All formula steps complete.")
	}

	// On fire, deliver the instruction over the redundant trio (#483).
	if improvementFired {
		deliverImprovement(improvementAgent, improvementInstr, formulaName)

		// #622 C5, the final-step extension. An improvement session inherits the dirtiest window
		// of the whole run — the very last state of the formula that just finished — and it is the
		// session whose whole job is reading a report and reasoning about it. So it starts clean.
		//
		// Strictly AFTER both durability legs, and only inside this branch. The marker is on disk
		// (written inside evaluateImprovementFire) and the instruction has been mailed, so the
		// respawn cannot lose the instruction: the mail is redelivered at SessionStart and the
		// marker survives until `af improvement complete` consumes it. The identity lock is
		// deliberately NOT released on this path, and the respawned af prime re-acquires it
		// because the lock of a dead PID is stale. Nothing outside `if improvementFired` reads
		// anything here: the non-fired completion path stays byte-identical.
		if cfg, err := config.LoadStartupConfig(factoryRoot); err == nil {
			now := time.Now()
			reading := stepContextReading(factoryRoot, cwd, improvementAgent, cfg.Recovery, now)
			if shouldBoundaryHandoff(reading, cfg.StepContext, gateClose, true) {
				runBoundaryHandoff(ctx, cwd, factoryRoot, "formula "+formulaName, instanceID, reading, cfg.StepContext, true)
			}
		}
	}

	// Tear down a dispatched session (worktree removal + self-terminate). Skipped
	// when the session survives — not dispatched, or WORK_DONE mail failed — so the
	// shell CWD and its worktree stay valid.
	if shouldTerminate {
		finishDispatchedSession(cwd, factoryRoot)
	}

	return nil
}

// finishDispatchedSession runs the deferred teardown for a dispatched session:
// worktree removal followed by tmux self-termination. Extracted verbatim from
// sendWorkDoneAndCleanup's tail so the improvement-completion verb can
// replay the exact same sequence instead of forking it.
func finishDispatchedSession(cwd, factoryRoot string) {
	// Session-end delete of the scoped-stop provenance datum (#548 P3). It survived formula
	// completion (cleanupRuntimeArtifacts leaves it untouched — Gap A's repair); its lifetime
	// is the dispatched SESSION, so it is destroyed here as the session tears itself down.
	os.Remove(filepath.Join(cwd, ".runtime", "dispatch_owner"))
	if wtID := readWorktreeID(cwd); wtID != "" {
		agentName := os.Getenv("AF_ROLE")
		if agentName == "" {
			fmt.Fprintf(os.Stderr, "warning: AF_ROLE not set, skipping worktree cleanup\n")
		} else {
			// Emitted before every removal branch below, so the operator reads what survived
			// while it is still standing. stderr is the only stream this path has.
			if line := memory.PreservedLine(factoryRoot, agentName); line != "" {
				fmt.Fprintf(os.Stderr, "%s\n", line)
			}
			if isWorktreeOwner(cwd) {
				meta, empty, err := worktree.RemoveAgent(factoryRoot, wtID, agentName)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: worktree RemoveAgent: %v\n", err)
				} else if empty {
					if rmErr := worktree.Remove(factoryRoot, meta); rmErr != nil {
						fmt.Fprintf(os.Stderr, "warning: worktree cleanup: %v\n", rmErr)
					}
				}
			} else {
				if _, _, err := worktree.RemoveAgent(factoryRoot, wtID, agentName); err != nil {
					fmt.Fprintf(os.Stderr, "warning: worktree RemoveAgent: %v\n", err)
				}
			}
		}
	}
	selfTerminate(cwd, factoryRoot)
}

// readFormulaCaller reads the dispatcher address from .runtime/formula_caller.
func readFormulaCaller(workDir string) string {
	path := filepath.Join(workDir, ".runtime", "formula_caller")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// readDispatchOwner reads the dispatcher identity from .runtime/dispatch_owner — the
// session-scoped scoped-stop provenance datum (#548 P3) that, unlike formula_caller, survives
// formula completion. Best-effort: a missing/unreadable file yields "" (fail-closed).
func readDispatchOwner(workDir string) string {
	path := filepath.Join(workDir, ".runtime", "dispatch_owner")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// deliverImprovement runs the redundant delivery trio for a fired hook:
// (a) the stdout instruction block (always emitted — the inline anchor), then
// (b) best-effort self-mail of the full instruction (--priority urgent), then
// (c) a best-effort short nudge pointer. Mail is sent BEFORE the nudge so the
// nudge-triggered `af mail check --inject` surfaces the mailed instruction on the
// next turn. Channel failures warn on stderr; the stdout block always emits.
func deliverImprovement(agent, instruction, formulaName string) {
	fmt.Println()
	fmt.Println(instruction)

	subject := fmt.Sprintf("IMPROVEMENT HOOK: refine %s", strings.TrimPrefix(formulaName, "Formula: "))
	if err := sendImprovementMail(agent, subject, instruction); err != nil {
		fmt.Fprintf(os.Stderr, "warning: improvement self-mail failed: %v\n", err)
	}

	pointer := "IMPROVEMENT HOOK pending — check mail (af mail check) for the full instruction, then run: af improvement complete"
	if err := deliverImprovementNudge(session.SessionName(agent), pointer); err != nil {
		fmt.Fprintf(os.Stderr, "warning: improvement nudge failed: %v\n", err)
	}
}

// sendImprovementMail self-mails the full /improve-agent instruction to the
// finishing agent with --priority urgent (mail.go:38 — no other subprocess caller
// passes it). Declared as a var so tests observe delivery without shelling out
// (mirrors sendWorkDoneMail); the isTestBinary() no-op keeps unit tests hermetic.
var sendImprovementMail = func(agent, subject, instruction string) error {
	if isTestBinary() {
		return nil
	}

	afPath, err := os.Executable()
	if err != nil {
		afPath, _ = exec.LookPath("af")
	}
	if afPath == "" {
		return fmt.Errorf("cannot find af binary")
	}
	cmd := exec.Command(afPath, "mail", "send", agent, "-s", subject, "-m", instruction, "--priority", "urgent")
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("improvement mail to %s failed: %w\nsubprocess stderr: %s", agent, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("improvement mail to %s: %w", agent, err)
	}
	return nil
}

// deliverImprovementNudge sends the short directive pointer to the agent's live
// session via the sanctioned NudgeSession channel (tmux.go:390). A var seam with an
// isTestBinary() no-op: NudgeSession's guardOp PANICS on a production identity under
// the guarded test build, so routing it through a seam (not the cmdTmux interface,
// which lacks NudgeSession) is required.
var deliverImprovementNudge = func(sessionName, message string) error {
	if isTestBinary() {
		return nil
	}
	return tmux.NewTmux().NudgeSession(sessionName, message)
}

// sendWorkDoneMail shells out to `af mail send` to notify the dispatcher.
// Declared as a var so tests can override it to inject failures (seam pattern).
var sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error {
	if isTestBinary() {
		return nil
	}

	afPath, err := os.Executable()
	if err != nil {
		afPath, _ = exec.LookPath("af")
	}
	if afPath == "" {
		return fmt.Errorf("cannot find af binary")
	}
	subject := fmt.Sprintf("WORK_DONE: %s", instanceID)
	body := fmt.Sprintf("All %d steps complete for formula %s.", stepCount, formulaName)
	cmd := exec.Command(afPath, "mail", "send", caller, "-s", subject, "-m", body)
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("mail send to %s failed: %w\nsubprocess stderr: %s", caller, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("mail send to %s: %w", caller, err)
	}
	return nil
}

var sendEscalationMail = func(recipient, instanceID, formulaName, reason string) error {
	if isTestBinary() {
		return nil
	}

	afPath, err := os.Executable()
	if err != nil {
		afPath, _ = exec.LookPath("af")
	}
	if afPath == "" {
		return fmt.Errorf("cannot find af binary")
	}
	subject := fmt.Sprintf("GUARD: Formula completion blocked for %s", formulaName)
	body := fmt.Sprintf("Formula %s (%s) completion blocked: %s\n\n"+
		"Action required: Examine the velocity record at .runtime/done_velocity, "+
		"review the agent's output artifacts, and decide whether to trust the results. "+
		"The agent session has been respawned via af handoff --collect and will cycle "+
		"until this is resolved.", formulaName, instanceID, reason)
	cmd := exec.Command(afPath, "mail", "send", recipient, "-s", subject, "-m", body)
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("escalation mail to %s failed: %w\nsubprocess stderr: %s", recipient, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("escalation mail to %s: %w", recipient, err)
	}
	return nil
}

func checkFormulaCompletionVelocity(workDir string) (triggered bool, reason string) {
	path := filepath.Join(workDir, ".runtime", "done_velocity")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, ""
	}

	var record doneVelocityRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return false, ""
	}

	threshold := 3
	if v := os.Getenv("AF_DONE_VELOCITY_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			threshold = n
		}
	}

	unprimedCount := 0
	for _, entry := range record.Closes {
		if !entry.WasPrimed {
			unprimedCount++
		}
	}

	if unprimedCount >= threshold {
		return true, fmt.Sprintf("%d of %d closes were unprimed (threshold %d) — possible step-skipping detected", unprimedCount, len(record.Closes), threshold)
	}
	return false, ""
}

func triggerHandoffRespawn(cwd, factoryRoot string) {
	if isTestBinary() {
		return
	}

	afPath, err := os.Executable()
	if err != nil {
		afPath, _ = exec.LookPath("af")
	}
	if afPath == "" {
		fmt.Fprintf(os.Stderr, "warning: cannot find af binary for handoff respawn\n")
		return
	}
	cmd := exec.Command(afPath, "handoff", "--collect")
	cmd.Dir = cwd
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: handoff respawn failed: %v\n", err)
		if stderr.Len() > 0 {
			fmt.Fprintf(os.Stderr, "  subprocess stderr: %s\n", strings.TrimSpace(stderr.String()))
		}
	}
}

// cleanupRuntimeArtifacts removes stale formula runtime files after completion.
// Best-effort removal (ignore errors) matches the existing checkpoint.Remove pattern.
// NOTE: Does NOT remove worktree_id or worktree_owner — those are needed by the
// worktree cleanup block that runs after this function.
func cleanupRuntimeArtifacts(cwd string) {
	os.Remove(filepath.Join(cwd, ".runtime", "hooked_formula"))
	os.Remove(filepath.Join(cwd, ".runtime", "formula_caller"))
	os.Remove(filepath.Join(cwd, ".runtime", "dispatched"))
	os.Remove(filepath.Join(cwd, ".runtime", "last_closed_step"))
	os.Remove(filepath.Join(cwd, ".runtime", "step_primed"))
	os.Remove(filepath.Join(cwd, ".runtime", "done_velocity"))
}

// readWorktreeID reads the worktree ID from .runtime/worktree_id.
// Returns "" if the file does not exist or cannot be read.
func readWorktreeID(workDir string) string {
	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "worktree_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// isWorktreeOwner checks whether this agent is the owner of its worktree.
// Returns false if the file does not exist or cannot be read.
func isWorktreeOwner(workDir string) bool {
	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "worktree_owner"))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(data)) == "true"
}

// isDispatchedSession checks whether the current session was launched via af sling --agent.
func isDispatchedSession(cwd string) bool {
	_, err := os.Stat(filepath.Join(cwd, ".runtime", "dispatched"))
	return err == nil
}

// shouldAutoTerminate decides whether a dispatched session should self-terminate.
// Returns false when the session is not dispatched or when mail delivery failed
// (keeping the session alive for debugging).
func shouldAutoTerminate(dispatched bool, mailErr error) bool {
	if !dispatched {
		return false
	}
	return mailErr == nil
}

// selfTerminate kills the current tmux session. Called only for dispatched sessions
// after formula completion and cleanup are done.
func selfTerminate(cwd, factoryRoot string) {
	if isTestBinary() {
		return
	}

	// Ignore SIGHUP before killing the session. When tmux kill-session runs,
	// the tmux server asynchronously sends SIGHUP to all processes in the
	// session's process group — including this af done process.
	signal.Ignore(syscall.SIGHUP)

	agentName, err := detectAgentName(cwd, factoryRoot)
	if err != nil {
		// Fallback: ask tmux which session this process is actually in.
		//
		// This used to read .runtime/session_id and hand it to terminateSession. That file holds
		// the CLAUDE CODE session id — a UUID, written by persistSessionID from the SessionStart
		// hook payload — not a tmux session name, and HasSession matches names exactly. Tmux
		// sessions here are af-<role>, so the predicate was always false and auto-terminate
		// silently did nothing every time the name could not be resolved (#622 LOW-3).
		//
		// AF_ROLE is not the repair: resolveAgentName already exhausts it before failing, so by
		// the time this branch runs there is no role in the environment to fall back to. tmux
		// itself is the only remaining source of the one thing needed here — a real session name.
		//
		// That same AF_ROLE-lessness is why the K8 kill guard had to learn isSelfTmuxSession
		// (authority.go): without it the guard refuses this kill, and terminateSession has by then
		// already written .runtime/last_termination — trading a silent no-op for a durable lie.
		// isSelfSessionID, the Ledger D9 accommodation for the UUID this branch used to pass, loses
		// its last production caller here; retiring it is an ADR-021 decision, not this phase's.
		if os.Getenv("TMUX") == "" {
			fmt.Fprintf(os.Stderr, "warning: cannot detect agent for auto-terminate: %v (not inside tmux)\n", err)
			return
		}
		name, tmuxErr := newCmdTmux().CurrentSessionName()
		if tmuxErr != nil || name == "" {
			fmt.Fprintf(os.Stderr, "warning: cannot detect agent for auto-terminate: %v (tmux session lookup: %v)\n", err, tmuxErr)
			return
		}
		// The shape check the old code got for free by never matching anything. tmux answers with
		// whatever session this process is in, and af done is runnable from an operator's own
		// shell; killing that would be a strictly worse failure than the silent no-op this branch
		// replaces. Only a factory identity is ours to end.
		if !isAfProductionSession(name) {
			fmt.Fprintf(os.Stderr, "warning: cannot detect agent for auto-terminate: %v (tmux session %q is not a factory session)\n", err, name)
			return
		}
		terminateSession(name, cwd)
		return
	}

	sessionID := session.SessionName(agentName)
	terminateSession(sessionID, cwd)
}

// terminateSession checks if a tmux session exists and kills it.
func terminateSession(sessionID, cwd string) {
	t := newCmdTmux()

	has, err := t.HasSession(sessionID)
	if err != nil || !has {
		return
	}

	termRecord := fmt.Sprintf("auto-terminated at %s\n", time.Now().UTC().Format(time.RFC3339))
	os.WriteFile(filepath.Join(cwd, ".runtime", "last_termination"), []byte(termRecord), 0o644)

	fmt.Printf("Auto-terminating dispatched session %s\n", sessionID)

	if err := t.KillSession(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "warning: auto-terminate failed: %v\n", err)
	}
}

func countAllChildren(ctx context.Context, store issuestore.Store, parentID string) (int, error) {
	items, err := store.List(ctx, issuestore.Filter{
		Parent:        parentID,
		IncludeClosed: true,
	})
	if err != nil {
		return 0, fmt.Errorf("counting children: %w", err)
	}
	return len(items), nil
}

type lastClosedStepRecord struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ClosedAt    string `json:"closed_at"`
	Formula     string `json:"formula"`
}

// writeLastClosedStep writes the fidelity gate hook's input and returns the record it built.
// It is the one place on this path that already resolves both the step description and the
// formula-instance title, so returning the record spares the caller a second store round trip
// on the lifecycle hot path.
func writeLastClosedStep(ctx context.Context, workDir string, step issuestore.Issue, instanceID string, store issuestore.Store) (lastClosedStepRecord, error) {
	runtimeDir := filepath.Join(workDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return lastClosedStepRecord{}, err
	}

	var description string
	if iss, err := store.Get(ctx, step.ID); err == nil {
		description = iss.Description
	}

	formulaName := instanceID
	if iss, err := store.Get(ctx, instanceID); err == nil && iss.Title != "" {
		formulaName = iss.Title
	}

	record := lastClosedStepRecord{
		ID:          step.ID,
		Title:       step.Title,
		Description: description,
		ClosedAt:    time.Now().UTC().Format(time.RFC3339),
		Formula:     formulaName,
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return lastClosedStepRecord{}, fmt.Errorf("marshaling last closed step: %w", err)
	}
	data = append(data, '\n')
	return record, os.WriteFile(filepath.Join(runtimeDir, "last_closed_step"), data, 0o644)
}

func checkStepPrimed(ctx context.Context, workDir string, stepID string, store issuestore.Store) error {
	path := filepath.Join(workDir, ".runtime", "step_primed")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("step not primed: run 'af prime' before 'af done' to read step instructions")
	}
	content := strings.TrimSpace(string(data))
	parts := strings.SplitN(content, ":", 2)
	primedID := parts[0]
	if primedID != stepID {
		return fmt.Errorf("step primed for %s but closing %s: run 'af prime' to refresh", primedID, stepID)
	}
	if len(parts) == 2 {
		storedHash := parts[1]
		if iss, err := store.Get(ctx, stepID); err == nil {
			h := sha256.Sum256([]byte(iss.Description))
			expectedHash := fmt.Sprintf("%x", h[:4])
			if storedHash != expectedHash {
				return fmt.Errorf("step instructions changed since prime (hash mismatch): run 'af prime' to refresh")
			}
		}
	}
	return nil
}

type doneVelocityRecord struct {
	Closes          []doneVelocityEntry `json:"closes"`
	LastEvalBetween string              `json:"last_eval_between,omitempty"`
}

type doneVelocityEntry struct {
	StepID       string `json:"step_id"`
	WasPrimed    bool   `json:"was_primed"`
	EvidenceType string `json:"evidence_type,omitempty"`
	ClosedAt     string `json:"closed_at"`
}

func checkDoneVelocity(workDir string) error {
	path := filepath.Join(workDir, ".runtime", "done_velocity")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	var record doneVelocityRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil
	}

	threshold := 3
	if v := os.Getenv("AF_DONE_VELOCITY_THRESHOLD"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			threshold = n
		}
	}

	window := 30
	if v := os.Getenv("AF_DONE_VELOCITY_WINDOW"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			window = n
		}
	}

	cutoff := time.Now().UTC().Add(-time.Duration(window) * time.Second)
	unprimedCount := 0
	for _, entry := range record.Closes {
		if entry.WasPrimed {
			continue
		}
		t, err := time.Parse(time.RFC3339, entry.ClosedAt)
		if err != nil {
			continue
		}
		if t.After(cutoff) {
			unprimedCount++
		}
	}

	if unprimedCount >= threshold {
		return fmt.Errorf("velocity escalation: %d unprimed closes within %ds exceeds threshold %d — possible step-skipping detected, review agent behavior", unprimedCount, window, threshold)
	}
	return nil
}

func recordDoneTimestamp(workDir string, stepID string, wasPrimed bool, evidenceType string) error {
	path := filepath.Join(workDir, ".runtime", "done_velocity")
	runtimeDir := filepath.Join(workDir, ".runtime")
	_ = os.MkdirAll(runtimeDir, 0o755)

	var record doneVelocityRecord
	if data, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(data, &record)
	}

	record.Closes = append(record.Closes, doneVelocityEntry{
		StepID:       stepID,
		WasPrimed:    wasPrimed,
		EvidenceType: evidenceType,
		ClosedAt:     time.Now().UTC().Format(time.RFC3339),
	})

	if len(record.Closes) > 10 {
		record.Closes = record.Closes[len(record.Closes)-10:]
	}

	data, err := json.MarshalIndent(record, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}
