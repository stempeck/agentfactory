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
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tmux"
	"github.com/stempeck/agentfactory/internal/tokenomics"
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
	// #668 K7's policy operand travels beside the bound so both come from one load. Note which
	// conjunct protects an unreadable factory, because it is NOT this one: a zero TokenomicsConfig
	// leaves Enabled as "", which satisfies the umbrella's `!= "off"` test, and ResolvePolicy reads
	// an unwritten mechanism as its default — budget's default is on. What refuses here is the pair
	// above: stepCtx stays zero so HandoffPct is 0, and closeReading stays NoReading so the channel
	// is unhealthy, and shouldBoundaryHandoff short-circuits on either one long before the operand
	// is consulted. A factory whose config cannot be read is inert by the bound, not by the policy.
	var tokCfg config.TokenomicsConfig
	// The breaker threshold travels beside tokCfg through the same guarded load: on an unreadable
	// factory it stays 0, which EffectiveAdmissionCeilingPct reads as "not injected" and leaves the
	// ceiling unclamped — the pre-clamp behavior, not a nil dereference through the (nil, err) startupCfg.
	ctxThresholdPct := 0
	if startupErr == nil {
		closeReading = stepContextReading(factoryRoot, cwd, agentName, startupCfg.Recovery, boundaryNow)
		stepCtx = startupCfg.StepContext
		tokCfg = startupCfg.Tokenomics
		ctxThresholdPct = startupCfg.Recovery.ContextThresholdPct
	}

	// The closing record's own step sequence, kept for the intervention record a fired mechanism
	// writes further down. AC-4's join is between two records, so the second one has to carry the
	// same seq as the first — and step_seq has exactly one derivation (telemetryStepSpan reads it
	// back from what af prime wrote), so it is carried rather than derived a second time.
	closeStepSeq := 0
	if vt := verbTelemetryFrom(ctx); vt.enabled {
		ev := telemetryRecordFor(ctx, factoryRoot, cwd, vt.agent, instanceID, "")
		ev.Event = telemetry.EventStepEnd
		ev.Formula = telemetryFormulaName(closed.Formula)
		ev.StepID = step.ID
		ev.StepLabel = stepLabelOf(step)
		ev.StepTitle = step.Title
		ev.Status = telemetry.StatusClosed
		if phaseComplete {
			ev.Status = telemetry.StatusGateWaiting
		}
		span := telemetryStepSpan(factoryRoot, vt.agent, instanceID, step.ID, ev.TS)
		ev.StepSeq, ev.DurationMS = span.seq, span.durationMS
		closeStepSeq = ev.StepSeq
		// A gate close records its occupancy like any other close (cross-review HIGH-2). The gate
		// contract excludes it from the HANDOFF, not from the measurement — a step that filled its
		// window and then hit a gate is exactly the step the improvement loop needs to see.
		attachStepOccupancy(&ev, closeReading, factoryRoot, boundaryNow)
		ev.CtxTokensStart = span.ctxTokensStart
		ev.CtxBoundTokens = int64(stepCtx.BoundTokens)
		ev.CumTokensDelta = stepCumTokensDelta(span, ev)
		attachGenerationScalars(&ev, span, cwd)
		// #678 K1. effort_level is echoed from the launch env rather than re-derived: the level a
		// step ran AT is the level its session was started with, and asking the plan again here
		// would record what the NEXT step should get on the record for the one that just finished.
		ev.EffortLevel = launchEffortLevel()
		ev.GateFlags = gateFlagsInWindow(ctx, store, vt.agent, span.startTS, ev.TS)
		appendTelemetryRecord(factoryRoot, ev)
		// #668 K6: the record just written is a sample the learned-data cache is built from, so the
		// cache is refreshed here, after the append and inside the same gate. Nothing reads it yet —
		// this phase makes the flywheel turn, a later one puts a decision on the far end of it.
		updateLearnedDigest(factoryRoot, ev)
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

		// #668 K7 close-time. The question is about the step that is ABOUT TO START, so the key is
		// the next ready step's — the closing step's own appetite is history, and asking about it
		// here would predict the cost of work already paid for. A branch with no next step (all
		// remaining steps blocked) leaves the key empty, which learnedAppetite reports as unknown
		// and Admit turns into an observation: a boundary that cannot predict does not fire.
		// The key is the next step's STABLE label, resolved from its bead so it matches what
		// samplesFrom filed the learned side under; a bead id would find nothing a prior run wrote.
		nextStepLabel := ""
		if nextErr == nil && len(nextResult.Steps) > 0 {
			nextStepLabel = stepLabelOf(nextResult.Steps[0])
		}
		adm := stepAdmission(factoryRoot, cwd, agentName, telemetryFormulaName(closed.Formula),
			nextStepLabel, closeReading, tokCfg, ctxThresholdPct)

		// #678 K6. Asked from the plan the admission above already resolved, so the efficiency reason
		// and the capacity verdict describe one digest read.
		eff := boundaryEfficiencyRelaunch(cwd, instanceID, adm)
		if eff.atCap {
			// Written BEFORE the boundary, and independent of whether it fires: the fact recorded is
			// that the bound refused a relaunch history warranted, which is true whatever the boundary
			// then decides — and a record written after a handoff would be written in a pane that no
			// longer exists. Observe, not an action, so tokenomicsFirings does not count it as a firing
			// (tokenomics.go:597-600): nothing happened, and a mechanism that reported this as an act
			// would report its own bound as work.
			recordIntervention(ctx, factoryRoot, cwd, agentName, instanceID, func(ev *telemetry.StepEvent) {
				ev.Formula = telemetryFormulaName(closed.Formula)
				ev.StepID = step.ID
				ev.StepSeq = closeStepSeq
				ev.StepTitle = step.Title
				ev.Mechanism = string(eff.mechanism)
				ev.Action = telemetry.ActionObserve
				ev.Objective = telemetry.ObjectiveEfficiency
				attachStepOccupancy(ev, closeReading, factoryRoot, boundaryNow)
			})
		}

		// #622 C5: the cooperative step boundary. LAST statement on this branch, because a
		// successful respawn replaces the pane this process is running in and nothing after it
		// would run. The step is already closed and its record already written, so there is no
		// in-flight work to lose — the boundary only ever recycles a session between steps.
		if shouldBoundaryHandoff(closeReading, stepCtx, phaseComplete, true, adm.handoffHelps(), eff.warranted) {
			// Written BEFORE the handoff, because the handoff may replace this pane and never
			// return. A mechanism that fired and left no record is indistinguishable from one that
			// never fired, which is the whole thing AC-4 exists to make impossible.
			if adm.handoffHelps() {
				// #672 AC-3: the forced boundary handoff is an ENFORCEMENT act — it recycles the session
				// rather than merely advising — so its record must survive the telemetry toggle
				// (recordEnforcement, not recordIntervention).
				recordEnforcement(ctx, factoryRoot, cwd, agentName, instanceID, func(ev *telemetry.StepEvent) {
					ev.Formula = telemetryFormulaName(closed.Formula)
					ev.StepID = step.ID
					ev.StepSeq = closeStepSeq
					ev.StepTitle = step.Title
					ev.Mechanism = string(tokenomics.MechanismBudget)
					ev.Action = telemetry.ActionHandoff
					attachStepOccupancy(ev, closeReading, factoryRoot, boundaryNow)
				})
			}
			// #678 K6: the efficiency relaunch's own record. Enforcement, for the same reason the
			// capacity handoff above it is — a recycle is an act, not counsel, and #672 AC-3 makes an
			// act's record independent of the measurement toggle. Filed under the mechanism that
			// warranted it, NEVER budget: a budget record claims the window would not fit, and this
			// fires with the window nearly empty, which is the mislabelling that would make the
			// actuator read as capacity pressure in every read surface downstream.
			//
			// Gated on efficiency having CAUSED this handoff, not merely having warranted one. The
			// boundary is a disjunction, so a plain occupancy handoff can coincide with a warranted
			// relaunch — and charging that recycle to the efficiency arm would both credit the arm with
			// capacity's work and spend a BOUNDED budget on a respawn that was going to happen anyway,
			// disarming the actuator early for steps it never acted on. Nothing is lost by declining:
			// the relaunch still carries the level, and the session it starts records its own
			// effort/reduce_effort at prime time, which is where the treatment is actually applied.
			//
			// #679 F11/T4: the write is a CLOSURE handed to runBoundaryHandoff and invoked past its
			// decline gates, beside the cap charge — so a boundary that declines (no pane) files no
			// phantom record. AC-4's join still survives a respawn, because the write leads the respawn
			// at that commit point exactly as the cap charge does.
			efficiencyCaused := eff.warranted && efficiencyCausedBoundary(closeReading, stepCtx, phaseComplete, adm)
			recordStepRelaunch := func() {
				recordEnforcement(ctx, factoryRoot, cwd, agentName, instanceID, func(ev *telemetry.StepEvent) {
					ev.Formula = telemetryFormulaName(closed.Formula)
					ev.StepID = step.ID
					ev.StepSeq = closeStepSeq
					ev.StepTitle = step.Title
					ev.Mechanism = string(eff.mechanism)
					ev.Action = telemetry.ActionHandoff
					ev.Objective = telemetry.ObjectiveEfficiency
					ev.EffortLevel = eff.level
					attachStepOccupancy(ev, closeReading, factoryRoot, boundaryNow)
				})
			}
			runBoundaryHandoff(ctx, cwd, factoryRoot, "step "+step.ID, instanceID, closeReading, stepCtx, false, adm, eff, efficiencyCaused, recordStepRelaunch)
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
// when it should not. A sixth, admissionNoFit, is the only one that can fire it the other way —
// see below:
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
//
// admissionNoFit is #668 K7's close-time operand: the next step's learned appetite will not fit
// beside what this session is already carrying, and WOULD fit a fresh one. It is a predictive
// input, never a second owner of the recycle decision (D7) — which is why it enters at the
// terminal comparison and nowhere else. Every refusal above it still outranks it: a gate close, a
// close with nothing following, an unconfigured factory and an unhealthy channel are all reasons
// this must not fire whatever admission predicts, and the last of those matters most — a no-fit
// verdict computed from an absent occupancy reading is Observe by construction, so it cannot even
// reach here, but a caller passing true off a stale one still gets nothing.
//
// What it adds is the case occupancy alone cannot see: a session sitting comfortably at 60% about
// to start a step that has historically needed more than the 40% left. Before this, that step ran
// until the window filled and the watchdog killed it mid-step; the measured Phase-1 baseline did
// that four times in one run. The boundary is the cheap version of the same recycle, taken one
// instant earlier, while there is nothing in flight to lose.
//
// efficiencyRelaunch is #678 K6's operand, and it joins admissionNoFit at the same terminal
// comparison for the same reason: this function stays the boundary's SINGLE owner (D7), and a second
// owner is what an early return for an efficiency reason would create. Every refusal above it is
// unchanged and still outranks it — a gate close with a warranted relaunch must still refuse, because
// the gate contract already ends the session and a handoff there would resurrect an ended session into
// a blocked step.
//
// It differs from admissionNoFit in what it is derived FROM, and that difference is the whole issue.
// admissionNoFit divides the next step's appetite by a resolved window, so a 1M-token profile cannot
// trip it. efficiencyRelaunch comes from a predicate that never learns the window exists, so it fires
// on a nearly-empty window as readily as on a full one — which is why the reading is still required
// above: not as evidence of pressure, but because a boundary that cannot see the session it is
// recycling has no business recycling it.
func shouldBoundaryHandoff(reading statusline.ChannelReading, cfg config.StepContextConfig,
	gateClose, workFollows, admissionNoFit, efficiencyRelaunch bool) bool {

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
	return admissionNoFit || efficiencyRelaunch || pct >= float64(cfg.HandoffPct)
}

// efficiencyCausedBoundary answers whether the efficiency operand is what MADE this boundary fire, as
// opposed to having merely been true while capacity or occupancy fired it.
//
// Asked by re-running the single boundary owner with the efficiency operand off. That is the exact
// counterfactual, it costs one arithmetic evaluation, and it keeps the answer derived from the owner
// rather than from a second copy of its rules that would drift the first time one of them changed.
//
// The distinction only matters because the efficiency relaunch is BOUNDED. An unbounded mechanism can
// afford to claim a shared cause; a mechanism with six relaunches per formula cannot, because every
// claim it makes for a recycle it did not cause is a recycle it will not be able to make later.
func efficiencyCausedBoundary(reading statusline.ChannelReading, cfg config.StepContextConfig,
	gateClose bool, adm admission) bool {

	return !shouldBoundaryHandoff(reading, cfg, gateClose, true, adm.handoffHelps(), false)
}

// boundaryHandoffCause is the clause that says WHY this boundary fired, and it exists because the
// two reasons look nothing alike to a reader. The occupancy rule fires when the window is already
// past the configured bound; K7's admission fires on a PROJECTION, and can fire at an occupancy
// plainly below that bound. Reporting the latter as "Context at 60%" against a 75% threshold reads
// as a bug in the very surface the End State asks to record why it acted.
// #678 K6 adds a third reason, and it needs its own clause for the same argument one step further:
// an efficiency relaunch can fire at 5% of a 1M-token window, so BOTH occupancy clauses above would
// report it as pressure that is plainly not there. Capacity is named first when both hold, because a
// no-fit verdict is the more urgent of two true facts.
func boundaryHandoffCause(pct float64, after string, adm admission, eff efficiencyRelaunch) string {
	if adm.handoffHelps() {
		return fmt.Sprintf("Next step projected at %.0f%% of the window against a %.0f%% ceiling, from %.0f%% after %s",
			adm.decision.ProjectedPct, adm.decision.HeadroomPct, pct, after)
	}
	if eff.warranted {
		return boundaryEfficiencyCause(adm, eff)
	}
	return fmt.Sprintf("Context at %.0f%% after %s", pct, after)
}

// boundaryEfficiencyCause states the learned figures the relaunch was taken on, because an operator
// reading "handing off for a clean session" at 5% occupancy has no other way to tell this from a bug.
// The step's STABLE label is named rather than the bead id: the bead id is minted per instance and
// says nothing about the history that produced the decision.
func boundaryEfficiencyCause(adm admission, eff efficiencyRelaunch) string {
	in := adm.efficiency.Inputs
	if eff.level != "" {
		return fmt.Sprintf("Next step %s runs at effort %s (exact thinking share %d %% over %d runs)",
			adm.stepLabel, eff.level, in.ThinkingSharePct, in.PriorRuns)
	}
	return fmt.Sprintf("Next step %s has historically spanned %d sessions over %d runs",
		adm.stepLabel, in.SessionsPerStep, in.PriorRuns)
}

// boundaryHandoffMessage is the self-mail body a boundary handoff leaves for the session that
// inherits the fresh window. finalStep is the #622-C5 final-step case: the formula is already
// complete and an improvement session inherits (driven by the marker + urgent self-mail), so there
// is no next step to prime — the inheritor's action is to finish the improvement pass, not af prime.
func boundaryHandoffMessage(pct float64, after string, finalStep bool, adm admission, eff efficiencyRelaunch) string {
	if finalStep {
		return fmt.Sprintf("%s. Fresh session: the improvement session inherits — run af mail check, then af improvement complete.",
			boundaryHandoffCause(pct, after, adm, eff))
	}
	return fmt.Sprintf("%s. Fresh session: run af prime for the next step.", boundaryHandoffCause(pct, after, adm, eff))
}

// runBoundaryHandoff performs the cooperative boundary handoff, or declines it for a reason worth
// printing. Failure warns and returns: af done has already closed the step and must still exit 0,
// and a session that could not be recycled is merely one the forceful recovery ladder may catch
// later — which is the degradation this feature is layered above, not a new failure.
func runBoundaryHandoff(ctx context.Context, cwd, factoryRoot, after, instanceID string,
	reading statusline.ChannelReading, cfg config.StepContextConfig, finalStep bool,
	adm admission, eff efficiencyRelaunch, chargeEfficiencyRelaunch bool, recordEfficiencyRelaunch func()) {
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

	// #679 F12/BODY-6 (cap) and F11/T4 (record): both the efficiency-relaunch cap charge AND the
	// efficiency enforcement record are committed HERE, past the two declines above (no pane, an
	// unresolved role), not before the handoff. A cap slot spent on a boundary that DECLINED disarms the
	// actuator early for a step it never recycled; a record written on a decline reports a relaunch that
	// never happened, and Phase 7 measures a firing that was refused (AC-4's join holds on the SUCCESS
	// path and does not require writing on the decline path). Past these gates the respawn below replaces
	// this pane, so anything written after it would never be written — this is the commit point the
	// bound's own comment meant by "counted before the relaunch", and it is exactly where the record must
	// sit too, so it survives a respawn that never returns. The record's SHAPE differs per leg (the step
	// leg carries step keys; the formula sibling omits them), so each caller supplies its own builder.
	if chargeEfficiencyRelaunch {
		bumpEfficiencyRelaunches(cwd, instanceID)
		if recordEfficiencyRelaunch != nil {
			recordEfficiencyRelaunch()
		}
	}

	pct, _ := reading.UsedPct()
	fmt.Printf("%s — handing off for a clean session.\n", boundaryHandoffCause(pct, after, adm, eff))

	subject := "HANDOFF: step context boundary"
	message := boundaryHandoffMessage(pct, after, finalStep, adm, eff)

	// TriggerDetail is populated because this is the one cooperative class that KNOWS its
	// occupancy. crash, error_pattern, compact_handoff and self_handoff leave it zero because they
	// have no occupancy story; leaving it zero here would make the boundary indistinguishable from
	// them in the funnel log, and the read surface renders a zero observed_pct as UNKNOWN.
	detail := recycleDetail{ObservedPct: pct, ThresholdPct: cfg.HandoffPct, InstanceID: instanceID}
	if adm.handoffHelps() {
		// The same correction boundaryHandoffCause makes to the printed line. Occupancy and
		// threshold stay honest — they are what was measured and what was configured — and the
		// projection is what the recycle was actually taken against, so the log no longer reads as
		// a boundary that fired below its own bound.
		detail.ProjectedPct = adm.decision.ProjectedPct
	}
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

		// #678 K1: the pair that makes a run reproducible. instance_start recorded the formula as it
		// was when the run began; re-hashing it HERE is what turns "the formula was edited mid-run"
		// from an invisible event into two digests that differ. A single digest can only ever say
		// what the run started from.
		//
		// The error is dropped for the reason instance_start drops it: an unreadable formula file
		// yields "", and an omitempty empty string reads as "nobody recorded this" — the honest
		// answer on a lifecycle path where observability never blocks work.
		if formulaPath, err := formula.FindFormulaFile(telemetryFormulaName(formulaName), factoryRoot); err == nil {
			ev.FormulaDigest, _ = formulaSHA256(formulaPath)
		}
		// Resolved at close and not at instantiation because the branch is still moving until now:
		// the formula's own branch-setup step rebases, so the commit this run's work should be
		// diffed against is not knowable when the run starts.
		ev.BaseCommit = baseCommit(cwd)
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
		// The umbrella is resolved HERE, once, and handed down. tokenomicsState is the same
		// function the instance_start record uses, so the instruction the agent receives and the
		// posture every record of this run states come from one reading (#678 K10).
		tokenomicsOn := tokenomicsState(factoryRoot) == telemetry.TokenomicsStateOn
		fired, agent, instruction, reason := evaluateImprovementFire(cwd, factoryRoot, instanceID, caller, formulaName, shouldTerminate, tokenomicsOn)
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

			// #668 K7 close-time, the final-step call site. The operand is ASSEMBLED here rather
			// than passed as a constant, and the difference is not cosmetic even though today's
			// answer is the same either way. A literal false says "this cell is not admission's
			// business"; an assembled verdict says "admission was asked and had nothing to say" —
			// and only the second stays true when something changes underneath it. What inherits
			// this window is an improvement session, so the step key is deliberately empty: there
			// is no next formula step, and naming the step that just closed would predict the cost
			// of work already paid for. learnedAppetite reports an empty key as unknown and Admit
			// turns unknown into an observation, so occupancy alone decides this cell exactly as it
			// did before #668 — until a phase that keys appetite on improvement sessions arrives,
			// at which point this site is already asking rather than waiting to be found again.
			adm := stepAdmission(factoryRoot, cwd, improvementAgent, telemetryFormulaName(formulaName),
				"", reading, cfg.Tokenomics, cfg.Recovery.ContextThresholdPct)

			// #678 K6 on this leg too, and ASSEMBLED rather than passed as a constant for the same
			// reason the admission above it is. Today it answers false by construction — the step key
			// is empty, so the plan carries no learned data — and an assembled value is what keeps that
			// true by arithmetic rather than by a literal nobody will revisit.
			eff := boundaryEfficiencyRelaunch(cwd, instanceID, adm)

			if shouldBoundaryHandoff(reading, cfg.StepContext, gateClose, true, adm.handoffHelps(), eff.warranted) {
				// Same order as the more-steps site, for the same reason: the handoff may replace
				// this pane and never return, so the record AC-4 asks for is written first.
				if adm.handoffHelps() {
					// No StepID/StepSeq/StepTitle, because there is no step: the formula is closed and
					// what follows is an improvement session. AC-4's join still holds against the
					// instance_end record written above on formula, instance, session and model, which
					// is the finest granularity this cell has.
					recordIntervention(ctx, factoryRoot, cwd, improvementAgent, instanceID, func(ev *telemetry.StepEvent) {
						ev.Formula = telemetryFormulaName(formulaName)
						ev.Mechanism = string(tokenomics.MechanismBudget)
						ev.Action = telemetry.ActionHandoff
						attachStepOccupancy(ev, reading, factoryRoot, now)
					})
				}
				// #678 K6's record on this leg too. The improvement session is relaunched through the
				// same respawnSession the more-steps boundary uses, and an arm that recorded only the
				// mid-formula relaunches would report a fraction of its own firings as the whole.
				//
				// #679 F11/T4: the same closure move as the step leg. The record shape has NO step keys
				// here — the formula is closed and what inherits the window is an improvement session — so
				// this leg supplies its own builder past runBoundaryHandoff's decline gates.
				efficiencyCaused := eff.warranted && efficiencyCausedBoundary(reading, cfg.StepContext, gateClose, adm)
				recordFormulaRelaunch := func() {
					recordEnforcement(ctx, factoryRoot, cwd, improvementAgent, instanceID, func(ev *telemetry.StepEvent) {
						ev.Formula = telemetryFormulaName(formulaName)
						ev.Mechanism = string(eff.mechanism)
						ev.Action = telemetry.ActionHandoff
						ev.Objective = telemetry.ObjectiveEfficiency
						ev.EffortLevel = eff.level
						attachStepOccupancy(ev, reading, factoryRoot, now)
					})
				}
				// Same F12/BODY-6 move as the more-steps leg: the bump (and now the record) are inside
				// runBoundaryHandoff, past its declines, so a declined improvement boundary spends no cap
				// slot and files no phantom efficiency record.
				runBoundaryHandoff(ctx, cwd, factoryRoot, "formula "+formulaName, instanceID, reading, cfg.StepContext, true, adm, eff, efficiencyCaused, recordFormulaRelaunch)
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
	os.Remove(filepath.Join(cwd, ".runtime", "tokenomics_advisories.json"))
	// #678 K5/K6, swept for the advisory ledger's reason: both are scoped to the formula that earned
	// them. A relaunch count carried into the next formula would arrive at its first step already
	// spent, and an effort breadcrumb naming a step label of the formula that just finished would make
	// the next formula's first boundary compare its plan against a level nothing is running at.
	os.Remove(effortBreadcrumbPath(cwd))
	os.Remove(efficiencyRelaunchPath(cwd))
	// #678 K8(b)'s counter, swept beside them. It is session-keyed and self-resets on a session
	// change, so a stale one is never READ wrong — this sweeps it so a completed formula leaves no
	// .runtime/ file behind, which is the property the rest of this function exists to keep.
	os.Remove(primeCountPath(cwd))
	// Swept for the same reason as the advisory ledger above: it is counsel scoped to the formula that
	// earned it. Left behind, a refusal from the last minutes of formula A would be relayed into
	// formula B's first fan-out and recorded against B's step id — counsel that is not merely stale
	// but misfiled.
	os.Remove(filepath.Join(cwd, ".runtime", dispatchLastRefusalName))
	// A formula that completes must not carry a leaked sequential.slot into the next one; clear the
	// whole sub-agent reservation ledger, not just the eight named files above (#669 F5).
	clearDispatchReservations(cwd)
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

	if err := t.KillSession(sessionID); err != nil { //af:teardown:self
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
