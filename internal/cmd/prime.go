package cmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/templates"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

var primeHookMode bool

var primeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Inject agent identity context",
	Long:  "Output session metadata, role template, and startup directive for the current agent.",
	RunE:  runPrime,
}

func init() {
	primeCmd.Flags().BoolVar(&primeHookMode, "hook", false, "Read session ID from stdin JSON (Claude Code SessionStart hook)")
	primeCmd.Flags().Bool("formula", false, "Deprecated: formula context is now automatic")
	primeCmd.Flags().MarkHidden("formula")
	rootCmd.AddCommand(primeCmd)
}

func runPrime(cmd *cobra.Command, args []string) error {
	start := time.Now()

	cwd, err := getWd()
	if err != nil {
		return err
	}

	// 1. Find factory root
	factoryRoot, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}

	// 1a. One gate read for the whole invocation, carried to every site that may record.
	// Reading it a second time further down would be a second place for the exact-match rule
	// to drift, and would break the stated off-path budget of one gate-file read per verb.
	ctx := withVerbTelemetry(cmd.Context(), verbTelemetry{
		verb:    "prime",
		start:   start,
		enabled: telemetryFactoryEnabled(factoryRoot),
	})

	// 2. Check if cwd is factory root — fan out to all agents
	rel, err := filepath.Rel(factoryRoot, cwd)
	if err != nil {
		return fmt.Errorf("detecting role: %w", err)
	}
	if rel == "." {
		// --hook mode makes no sense from factory root (it's per-agent)
		if primeHookMode {
			return fmt.Errorf("cannot use --hook from factory root (hook mode is per-agent)")
		}
		return runPrimeAll(ctx, cmd.OutOrStdout(), factoryRoot)
	}

	// 3. Handle --hook mode (single-agent path). The payload is read whether or not the pane guard
	// admits this session, because the envelope below needs the event name the harness fired even
	// when the identity claim is declined; only the claim itself stays behind the guard.
	sessionChanged := false
	payload := hookPayload{}
	if primeHookMode {
		payload = readHookPayloadFromCmd(cmd)
		if hookRunsInAgentPane() {
			if payload.SessionID != "" {
				sessionChanged = persistSessionID(cwd, payload.SessionID)
			}
			persistTranscriptPath(cwd, payload.SessionID, payload.TranscriptPath)
		}
	}

	// 4. Detect role
	role, _, err := detectRole(cwd, factoryRoot)
	if err != nil {
		return err
	}

	// 4a. A session begins only when the persisted id actually changes. The SessionStart hook
	// fires on resume as well as on a fresh session, so recording per firing would multiply one
	// session into many and make every per-session view wrong. Recorded here rather than beside
	// the write above because the role is not resolved until now, and the store refuses a
	// record whose agent name it cannot validate.
	if sessionChanged && verbTelemetryFrom(ctx).enabled {
		// The formula's human name lives in the instance bead title, and resolving it here
		// would put a store round trip on the SessionStart hook path. The instance id — which
		// the identity derivation reads from .runtime/ — is the join key; only closing records
		// become spans, so nothing downstream loses anything.
		ev := telemetryRecordFor(ctx, factoryRoot, cwd, role, "", "")
		ev.Event = telemetry.EventSessionStart
		// #678 K1: what opened the session, and the arm it opened on. Attached at the call site
		// rather than inside telemetryRecordFor because these belong to the records that OPEN
		// something — a step_end carrying them would repeat one run-scoped fact per step.
		ev.AFVersion, ev.AFCommit = Version, Commit
		// #678 K5: the launch leg's own breadcrumb is preferred over the environment, and the
		// environment stays the fallback. The two agree on the LEVEL by construction — the breadcrumb
		// is written from the same value that was exported — but only the breadcrumb carries the
		// objective, and a launch that chose no level writes none, which is where the env read still
		// answers for a level a profile declared on its own.
		crumb := readEffortBreadcrumb(cwd)
		ev.EffortLevel = crumb.Level
		if ev.EffortLevel == "" {
			ev.EffortLevel = launchEffortLevel()
		}
		appendTelemetryRecord(factoryRoot, ev)

	}

	// In hook mode the block is buffered and shipped as ONE hookSpecificOutput.additionalContext
	// object (#675 K3). Plain mode writes straight through: its output is read by a human and by
	// four byte-exact economics assertions, and an envelope there would be noise.
	//
	// Known cost of the buffer: outputFormulaContext's errorTrackingWriter can no longer see a stdout
	// failure, because a bytes.Buffer never fails. A step is therefore marked primed even if the
	// encode below cannot reach stdout. Accepted — a SessionStart hook whose stdout is broken has
	// already lost the session, and the alternative is re-plumbing the error channel through a
	// writer whose whole job is to be infallible.
	//
	// recordPrimeCost is deliberately left measuring the RAW block: primeAgent wraps hookBuf, not
	// stdout, so cost.n keeps meaning what it has always meant. The envelope and its JSON escaping
	// are therefore uncounted.
	primeOut := cmd.OutOrStdout()
	var hookBuf bytes.Buffer
	if primeHookMode {
		primeOut = &hookBuf
	}
	primed, err := primeAgent(ctx, primeOut, factoryRoot, role, cwd)
	// The envelope is emitted BEFORE the error is returned: a prime that failed halfway still
	// produced the header and whatever came after it, and dropping that on the floor would make a
	// partial failure indistinguishable from a silent one.
	if primeHookMode {
		emitHookContext(cmd.OutOrStdout(), hookEventNameOr(payload, hookEventSessionStart), hookBuf.String())
	}
	if err != nil {
		return err
	}

	// #679 F2/T2: the effort treatment's record is written HERE, after primeAgent's step query has
	// resolved the step this session picked up (when there is one). The reduced/baseline split joins the
	// arm on the SESSION, not on a per-step key (rebuild.go objectivePerSession keys on SessionID and
	// never on StepID), so the record has everything it needs to join even with no step in flight — an
	// empty StepID is harmless under the session join, an ABSENT record is not. So the write is NO LONGER
	// gated on `primed != nil` (#679 T2): an opening prime that resolves no ready step still ran at a
	// reduced level, and dropping its record folded that treated session into the baseline arm it was
	// meant to be measured against. When there is no step, the step context is left EXPLICITLY empty and
	// the formula is resolved from disk — surfaced, not silently omitted. Still gated on
	// sessionChanged so the once-per-session firing is unchanged. StepID is retained (when present) for the
	// `af turn evidence` [step X] display that reads it, not for the arm.
	if sessionChanged && verbTelemetryFrom(ctx).enabled {
		crumb := readEffortBreadcrumb(cwd)
		if obj := recordObjective(crumb.Objective); crumb.Level != "" && obj != "" {
			instanceID, formula := "", hookedFormulaName(cwd)
			if primed != nil {
				instanceID, formula = primed.instanceID, telemetryFormulaName(primed.formula)
			}
			recordIntervention(ctx, factoryRoot, cwd, role, instanceID, func(ev *telemetry.StepEvent) {
				ev.Formula = formula
				if primed != nil {
					ev.StepID = primed.stepID
					ev.StepSeq = primed.stepSeq
					ev.StepTitle = primed.stepTitle
				}
				ev.Mechanism = string(tokenomics.MechanismEffort)
				ev.Action = telemetry.ActionReduceEffort
				ev.Objective = obj
				ev.EffortLevel = crumb.Level
			})
		}
	}
	return nil
}

// runPrimeAll primes all provisioned agents when run from the factory root.
func runPrimeAll(ctx context.Context, out io.Writer, factoryRoot string) error {
	agentsPath := config.AgentsConfigPath(factoryRoot)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	primed := 0
	for name := range agentsCfg.Agents {
		agentDir := config.AgentDir(factoryRoot, name)
		if _, err := os.Stat(agentDir); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%s: skipped (not provisioned, run af install %s)\n", name, name)
			continue
		}
		if _, err := primeAgent(ctx, out, factoryRoot, name, agentDir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: prime failed: %v\n", name, err)
			continue
		}
		primed++
	}

	if primed == 0 {
		return fmt.Errorf("no provisioned agents found (run af install <role>)")
	}
	return nil
}

// primeAgent outputs session metadata, role template, and startup directive for a single agent.
// workDir is the agent's working directory — may be a worktree agent dir or factory agent dir.
func primeAgent(ctx context.Context, out io.Writer, factoryRoot, role, workDir string) (*primedStep, error) {
	agentsPath := config.AgentsConfigPath(factoryRoot)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return nil, err
	}

	agentEntry, ok := agentsCfg.Agents[role]
	if !ok {
		return nil, fmt.Errorf("agent %q not found in agents.json", role)
	}

	// #668 K16: everything below writes through the counter, so the recorded cost is the bytes the
	// agent actually received rather than a sum of the pieces this function believes it emitted.
	// Wrapped here, above the first write, because a wrapper installed later measures a prime that
	// had already spent part of its budget.
	cost := &primeCostWriter{w: out}
	out = cost

	// Acquire identity lock
	sessionID := getSessionID(workDir, role)
	acquireIdentityLock(workDir, sessionID)

	// Loaded above the first block that may be withheld, because the posture decides whether it is.
	// One load serves the slimming decision here and the economics/advisory blocks below it; a second
	// would be a second place for the operator's thresholds to be read differently.
	startupCfg, startupErr := config.LoadStartupConfig(factoryRoot)
	policy := tokenomics.Policy{}
	if startupErr == nil {
		policy = resolvedPolicy(factoryRoot, startupCfg.Tokenomics)
	}

	// #678 K8(b). A session is primed once at SessionStart and again after every af done, and every
	// prime past the first re-sends an identity the session has had since its first turn. This is the
	// re-prime reduction: it fires on every profile, takes no window operand, and is switched off with
	// the interview mechanism.
	//
	// Counted before the decision, so the count includes THIS prime and "primes > 1" reads as "this
	// session has been primed before". The counter is session-keyed and reset on change, so the block
	// is re-emitted after any session change — a new session has received nothing.
	//
	// Counted only when the arm is on, and that ordering is the off-path budget rather than tidiness:
	// with the mechanism off the count can never change a decision, so an unconditional bump would make
	// every af prime in every factory pay a read and an atomic write for an answer nobody reads. Arming
	// it later starts the count at this session's next prime, which is correct — a session already in
	// flight when the switch flipped has received its identity block either way.
	// A --hook prime is never counted (#681 T1). #675 K1 withholds the identity block in hook mode
	// unconditionally, so a hook prime delivers no identity — counting it would spend the session's
	// first free identity render on a prime that carried none, and the first PLAIN prime (which OD-1
	// guarantees delivers identity whole) would then read as a re-prime and be slimmed. Counting only
	// non-hook primes makes "primes > 1" mean "a plain prime has already delivered identity".
	slimIdentity := false
	if policy.On(tokenomics.MechanismInterview) && !primeHookMode {
		slimIdentity = bumpPrimeCount(workDir, sessionID) > 1
	}

	// Output session metadata
	fmt.Fprintf(out, "[AGENT FACTORY] role:%s pid:%d session:%s factory:%s\n", role, os.Getpid(), sessionID, factoryRoot)

	// The identity block: who this agent is, where it is working, and what to do at startup. All three
	// are durable facts about the SESSION rather than about the step, which is what makes them the
	// re-prime reduction's subject — and why the header line above stays: it names the session id the
	// agent's own tooling reports, it costs one line, and a reader who cannot see it cannot tell which
	// session it is in.
	//
	// The role template is withheld in HOOK mode unconditionally (#675 K1). At SessionStart the
	// harness has already loaded the agent's own CLAUDE.md — the identical text — so re-sending it
	// buys nothing, and it is by far the largest block on a surface the harness truncates. Every
	// tool-result prime still carries it (OD-1): a plain `af prime` is the agent asking who it is.
	if !primeHookMode && !slimIdentity {
		// Render role template — try agent-specific template first, fall back to type default.
		// RootDir is the factory root, the one rule every provisioning site already obeys
		// (install.go, worktree.go); prime is the last site made to agree with it (#681 T9 / K2).
		// AGENTS.md resolves at the factory root, which always holds the real file, so a worktree
		// agent loses nothing by not seeing its worktree root here.
		output, err := templates.RenderIdentity(templates.New(), role, agentEntry, factoryRoot, workDir)
		if err != nil {
			return nil, fmt.Errorf("rendering role template: %w", err)
		}
		out.Write(output)
	}

	// The worktree block and the startup directive are NOT part of the role template, are not
	// delivered by any other carrier, and are what a session that just started most needs. They ride
	// the hook surface even though the template above does not — but a slimmed re-prime still
	// withholds them, because a session that has been primed before already has them.
	if primeHookMode || !slimIdentity {
		// Output worktree context if applicable
		outputWorktreeContext(out, workDir)

		// Output startup directive
		outputStartupDirective(out, agentEntry.Type)
	}

	// Inject formula workflow context if active (self-guarding -- no-op when no formula)
	primed := outputFormulaContext(ctx, out, workDir)

	// ONE occupancy derivation for this invocation, shared by the step_start record below and the
	// economics block after it. Two reads of the same snapshot a few microseconds apart can straddle
	// a write, and an advisory that disagrees with the record printed beside it is worse than either
	// alone — the reason step_context.go owns this derivation for the whole verb layer.
	now := time.Now()
	reading := statusline.NoReading()
	if startupErr == nil {
		reading = stepContextReading(factoryRoot, workDir, role, startupCfg.Recovery, now)
	}

	// A step begins the first time an agent is primed for it. Re-primes after a handoff or a
	// respawn are the same step continuing, and recording each one would restart its clock.
	// Emitted here rather than at the write site because this frame is the one that already
	// holds the factory root and the agent name; the write site has neither.
	if vt := verbTelemetryFrom(ctx); vt.enabled && primed != nil && primed.isNew {
		ev := telemetryRecordFor(ctx, factoryRoot, workDir, role, primed.instanceID, "")
		ev.Event = telemetry.EventStepStart
		ev.Formula = telemetryFormulaName(primed.formula)
		ev.StepID = primed.stepID
		ev.StepSeq = primed.stepSeq
		ev.StepTitle = primed.stepTitle
		// #622 C4: how full the window already was when this step began. Without it the closing
		// record's occupancy is a level with nothing to measure it against, and a step that
		// inherited a nearly-full window is indistinguishable from one that filled it itself.
		//
		// A file read, never a network one: TestPrimeNoNetworkIO pins that af prime — a
		// SessionStart hook — puts no round trip in front of a session. Frequently nil right after
		// a handoff, because the respawned session has not rendered its first snapshot yet; that is
		// honest, and honestly absent is the whole contract of these fields.
		if startupErr == nil {
			attachStepOccupancy(&ev, reading, factoryRoot, now)
		}
		appendTelemetryRecord(factoryRoot, ev)
	}

	// #668 K7 open time. Skipped outright when the startup config will not load: this block acts on
	// operator-configured thresholds, and a factory whose config cannot be read has no thresholds to
	// act on — inventing defaults here would make the mechanism fire hardest exactly where the
	// operator's intent is least known.
	//
	// STATED RESIDUAL — design-doc.md's K7 row asks for the predicate to run BEFORE the step body is
	// rendered, so a no-fit does not pay a ~4.5K-token render into a session that is about to die.
	// It runs after, because outputFormulaContext both renders the body and returns the primedStep
	// the predicate is keyed on, and there is no way to have the second without the first. Splitting
	// step resolution from step rendering is the fix and it is larger than this phase; the advisory
	// is correct either way, only the saving is not yet collected.
	//
	// #668 K9 rides the same assembled admission the economics block returns, so the counsel and the
	// block above it describe one arithmetic rather than two.
	if startupErr == nil {
		adm := outputEconomicsContext(ctx, out, factoryRoot, role, workDir, primed, reading, startupCfg, now)
		outputAdvisoryContext(ctx, out, factoryRoot, role, workDir, primed, adm, reading, now)
	}

	// #668 K16: the step this prime is RESUMING, or empty when it is starting one. Only a resume can
	// be slimmed, because only a resume has output the session already received.
	//
	// #678 K8(a) adds the second case that has: the step this prime is STARTING, when the brief on disk
	// was written FOR it. A boundary handoff recycles between steps, so its successor is starting a new
	// step and the resume leg above cannot see it — which is why that successor re-received every
	// section the brief it inherited already carried.
	resuming, priming := "", ""
	if primed != nil {
		priming = primed.stepID
		if !primed.isNew {
			resuming = primed.stepID
		}
	}
	successorSlimmed := outputCheckpointContext(out, workDir, resuming, priming,
		policy.On(tokenomics.MechanismInterview))

	// One record for both reductions, once per session, because they are one mechanism doing one thing:
	// withholding output the session already has. Written after the reductions rather than beside each,
	// so a prime that slimmed twice does not read as two firings — and markPrimeReduced is what makes
	// "once" true across the many primes of one session rather than once per invocation.
	if (slimIdentity || successorSlimmed) && markPrimeReduced(workDir, sessionID) {
		instanceID := ""
		if primed != nil {
			instanceID = primed.instanceID
		}
		recordIntervention(ctx, factoryRoot, workDir, role, instanceID, func(ev *telemetry.StepEvent) {
			// A reduction can happen with no formula in flight — af prime primes an agent either way —
			// so the step keys are attached only when there is a step to name. Absent is honest; a
			// fabricated key would join this firing to a step it was not about.
			if primed != nil {
				ev.Formula = telemetryFormulaName(primed.formula)
				ev.StepID = primed.stepID
				ev.StepSeq = primed.stepSeq
				ev.StepTitle = primed.stepTitle
			}
			ev.Mechanism = string(tokenomics.MechanismInterview)
			ev.Action = telemetry.ActionAdvise
			ev.Objective = telemetry.ObjectiveEfficiency
			attachStepOccupancy(ev, reading, factoryRoot, now)
		})
	}

	// Write checkpoint for crash recovery (skip during hook mode -- session just starting)
	if !primeHookMode {
		writeFormulaCheckpoint(ctx, workDir)
	}

	recordPrimeCost(ctx, factoryRoot, role, sessionID, cost.n, now)

	return primed, nil
}

// detectRole determines the agent role from cwd relative to factory root.
// Delegates to resolveAgentName (helpers.go) for three-tier resolution, then
// loads the AgentEntry from agents.json.
func detectRole(cwd, factoryRoot string) (string, *config.AgentEntry, error) {
	agentName, err := resolveAgentName(cwd, factoryRoot)
	if err != nil {
		return "", nil, fmt.Errorf("detecting role: %w", err)
	}

	agentsPath := config.AgentsConfigPath(factoryRoot)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return "", nil, fmt.Errorf("detecting role: %w", err)
	}

	entry, ok := agentsCfg.Agents[agentName]
	if !ok {
		return "", nil, fmt.Errorf("agent %q not found in agents.json", agentName)
	}

	return agentName, &entry, nil
}

// outputWorktreeContext prints worktree information if the agent is running in a worktree.
// Reads .runtime/worktree_id from workDir; if absent, the agent is not in a worktree.
func outputWorktreeContext(out io.Writer, workDir string) {
	wtIDPath := filepath.Join(workDir, ".runtime", "worktree_id")
	wtIDData, err := os.ReadFile(wtIDPath)
	if err != nil {
		return // not in a worktree
	}
	wtID := strings.TrimSpace(string(wtIDData))
	if wtID == "" {
		return
	}

	// Get current branch
	branch := getCurrentGitBranch(workDir)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "## Worktree Isolation")
	fmt.Fprintln(out, "")
	if branch != "" {
		fmt.Fprintf(out, "[WORKTREE] branch: %s | id: %s | root: %s\n", branch, wtID, workDir)
	} else {
		fmt.Fprintf(out, "[WORKTREE] id: %s | root: %s\n", wtID, workDir)
	}
	fmt.Fprintln(out, "")
}

// hookRunsInAgentPane reports whether this SessionStart hook belongs to the agent whose directory it
// is running in, and it is the fix for six-sigma Gap 1 (#678 K1).
//
// The problem it solves is not hypothetical. The quality-gate grader evaluates a turn by running
// `claude -p --model haiku` from the agent's own working directory (hooks/fidelity-gate.sh). That
// starts a real Claude Code session, which fires this very hook, which — before this guard — wrote
// the GRADER's session id into .runtime/session_id. Everything downstream then measured the wrong
// session: the step's generation figures are session-guarded and went nil, and the grader's own
// session_start counted as one more session the step had crossed. The steps that were graded hardest
// therefore scored worst, which is precisely backwards. A live factory was found in this state.
//
// The test is TMUX_PANE PRESENCE, and presence is enough because of how the grader is launched. It
// runs under `env -i HOME PATH` plus a short OTel allowlist, so it inherits no TMUX_PANE at all,
// while the agent's own Claude Code process is started inside a tmux pane and passes the variable
// down to its hooks. Matching the agent's OWN pane id would be stricter, but af does not record a
// pane id it could match against, and presence already separates the two cases that exist.
//
// The trade-off is deliberate and is the same one done.go accepts at its step-boundary handoff: a
// legitimate session with no tmux — an operator running claude directly in an agent workspace, a
// container without tmux — is also declined. The decline is written to stderr, which is where it can
// be acted on; Claude Code surfaces hook stderr to the user only on exit 2 and `af prime --hook`
// exits 0, so in practice it lands in the debug log. In the grader's case it is discarded with the
// rest of the grader's stderr, which is the point.
//
// What the decline costs such a session is wider than the missing session_start record, because the
// guard declines to CLAIM the identity and does not erase the one already there: .runtime/session_id
// keeps whatever the last tmux session wrote. `af statusline render` files its occupancy snapshot
// under the live session id, while stepContextReading (step_context.go:46) looks the snapshot up
// through readRuntimeSessionID and gets the stale one, finds nothing, and returns NoReading(). Every
// ctx_used_pct / ctx_tokens_used / ctx_tokens_total on that run is nil and the tokenomics admission
// gate loses its input — silently, unlike the generation figures, which at least report
// no_records_in_window. Clearing the stale marker is not the fix: it would let a grader session
// blank the agent's identity, which is the attack this guard exists to stop.
func hookRunsInAgentPane() bool {
	if os.Getenv("TMUX_PANE") != "" {
		return true
	}
	fmt.Fprintf(os.Stderr, "warning: session identity not claimed: --hook is not running in a tmux pane, "+
		"so this session cannot be told apart from a grader or subprocess session\n")
	return false
}

// persistTranscriptPath records where the host said this session's transcript lives, so that af done
// can read the host's answer instead of re-deriving it (#678 K1).
//
// The derivation it supersedes is not broken — it reproduces the real directory name on every
// project directory of a real host — so this is not a repair. It removes a dependency on an
// UNDOCUMENTED CONVENTION: the slug rule was learned by observation and the host is free to change it
// without telling anyone, at which point every generation figure would quietly stop being recorded.
//
// An absent or empty path writes nothing rather than truncating what is there. A payload that omits
// the field must leave the previous session's answer alone rather than replacing it with silence,
// because the reader treats an empty marker and a missing one identically and would then fall back —
// correctly, but for the wrong reason, and one release later that fallback might be gone.
//
// The session id is stored WITH the path, and that is what makes leaving a stale marker in place
// safe. Session N carries a transcript_path, session N+1's payload omits it: session_id advances and
// this marker does not, so a reader that checked only "does this file exist" would hand session N+1
// session N's transcript — a real file, belonging to the wrong session, preferred over a derivation
// that was right. Keying the marker turns that into a fallback instead of a wrong answer.
func persistTranscriptPath(dir, sessionID, transcriptPath string) {
	if sessionID == "" || transcriptPath == "" {
		return
	}
	runtimeDir := filepath.Join(dir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "transcript_path"), []byte(sessionID+"\t"+transcriptPath), 0o644)
}

// launchEffortLevel is the host effort level this process was launched under, or "" if the launcher
// set none or set one the host itself would not honour. Read from the environment HERE, in the cmd
// layer, for the reason claudeConfigDirEnv is (ADR-004): a library package must not read the process
// it happens to be running in.
func launchEffortLevel() string {
	level := os.Getenv(config.EnvEffortLevel)
	if !config.IsEffortLevel(level) {
		return ""
	}
	return level
}

// persistSessionID writes the session ID to <dir>/.runtime/session_id and reports whether that
// changed it. The write stays an unconditional overwrite; only the answer is new. Comparison is
// whitespace-insensitive because this writer stores the id bare while other writers in the
// repository terminate their runtime files with a newline.
func persistSessionID(dir, sessionID string) bool {
	runtimeDir := filepath.Join(dir, ".runtime")
	prev, _ := os.ReadFile(filepath.Join(runtimeDir, "session_id"))
	os.MkdirAll(runtimeDir, 0o755)
	// A failed write means there is no new session on disk, so reporting a change would put a
	// record into the log for a session nothing else can observe.
	if err := os.WriteFile(filepath.Join(runtimeDir, "session_id"), []byte(sessionID), 0o644); err != nil {
		return false
	}
	return strings.TrimSpace(string(prev)) != strings.TrimSpace(sessionID)
}

// writeStepPrimed records which step the agent has read instructions for, and reports whether
// that is a step it had not primed before.
//
// Only the step id decides that, never the description hash the marker also carries: re-priming
// after a step's description changed is still the same step, and treating it as a new one would
// restart a clock that is already running.
func writeStepPrimed(workDir, stepID, description string) bool {
	runtimeDir := filepath.Join(workDir, ".runtime")
	prev, _ := os.ReadFile(filepath.Join(runtimeDir, "step_primed"))
	primedID, _, _ := strings.Cut(strings.TrimSpace(string(prev)), ":")
	os.MkdirAll(runtimeDir, 0o755)
	h := sha256.Sum256([]byte(description))
	content := fmt.Sprintf("%s:%x", stepID, h[:4])
	// As above: an unwritten marker is not a primed step, and the close-side check would
	// reject it anyway.
	if err := os.WriteFile(filepath.Join(runtimeDir, "step_primed"), []byte(content), 0o644); err != nil {
		return false
	}
	return primedID != stepID
}

type errorTrackingWriter struct {
	w      io.Writer
	failed bool
}

func (e *errorTrackingWriter) Write(p []byte) (int, error) {
	n, err := e.w.Write(p)
	if err != nil {
		e.failed = true
	}
	return n, err
}

// getSessionID reads a persisted session ID or returns a fallback.
func getSessionID(dir, role string) string {
	path := filepath.Join(dir, ".runtime", "session_id")
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		return string(data)
	}
	return fmt.Sprintf("%s-%d", role, os.Getpid())
}

// acquireIdentityLock attempts to acquire the identity lock. Warns on failure, does not fail hard.
func acquireIdentityLock(workDir, sessionID string) {
	l := lock.New(workDir)
	if err := l.Acquire(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: identity lock: %v\n", err)
	}
}

// outputStartupDirective prints startup instructions based on agent type.
// Custom directives from agents.json are delivered via the startup nudge
// (session.Manager.Start), not here — tool output is not treated as a
// direct instruction by Claude.
func outputStartupDirective(w io.Writer, agentType string) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "## Startup Directive")
	fmt.Fprintln(w, "")
	// Step 1 no longer tells the agent to GO AND CHECK mail: the SessionStart hook array delivers it
	// before this text is read, so an inbox call is a second delivery of what the agent already has.
	switch agentType {
	case "autonomous":
		fmt.Fprintln(w, "1. Act on the mail delivered at session start (`af mail inbox` lists ids for `af mail delete`)")
		fmt.Fprintln(w, "2. Act on any hooked work or queued tasks")
		fmt.Fprintln(w, "3. Begin autonomous execution")
	default: // interactive
		fmt.Fprintln(w, "1. Act on the mail delivered at session start (`af mail inbox` lists ids for `af mail delete`)")
		fmt.Fprintln(w, "2. Act on any instructions or requests found in mail")
		fmt.Fprintln(w, "3. If no actionable mail, await user input")
	}
	// Custom directive is delivered via the startup nudge (session.Manager.Start),
	// not here — tool output is not treated as a direct instruction by Claude.
	fmt.Fprintln(w, "")
}

// isTestBinary reports whether the current process is a Go test binary.
// Go test binaries are named "<pkg>.test" (e.g., "cmd.test"). Spawning
// subprocess commands via os.Executable() from a test binary causes infinite
// recursion because Cobra routes the args back through the same command tree.
func isTestBinary() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.HasSuffix(filepath.Base(exe), ".test")
}

// readHookedFormulaID reads the formula instance bead ID from <workDir>/.runtime/hooked_formula.
// Returns empty string if file doesn't exist (no formula active).
func readHookedFormulaID(workDir string) string {
	path := filepath.Join(workDir, ".runtime", "hooked_formula")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// primedStep is what outputFormulaContext learned about the step it just primed: everything a
// step_start record needs, plus whether this prime was the step's first.
//
// It is returned rather than recorded in place because this function has neither the factory
// root nor the agent name, while its caller has both — and because returning a value leaves
// every existing call site, which uses this as a bare statement, compiling untouched.
type primedStep struct {
	instanceID string
	formula    string
	stepID     string
	// stepLabel is the formula's stable step id (the bead's step-id: label), which the learned
	// digest keys on; stepID stays the per-instance bead id every other rollup needs. Resolved here
	// because this is where the step bead is in hand, so the admission read downstream keys on the
	// same label the close write filed under (decisions.md D3).
	stepLabel string
	stepTitle string
	stepSeq   int
	isNew     bool
}

// outputFormulaContext injects formula workflow context into the prime output.
// Lazy-constructs the issue store via the newIssueStore seam only when a
// formula is hooked, so non-hooked prime paths don't need bd on PATH.
// Returns nil whenever no step was primed.
func outputFormulaContext(ctx context.Context, out io.Writer, workDir string) *primedStep {
	instanceID := readHookedFormulaID(workDir)
	if instanceID == "" {
		return nil
	}

	actor := os.Getenv("AF_ACTOR")
	store, err := newIssueStore(workDir, actor)
	if err != nil {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "## Formula Workflow")
		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "**Formula:** %s\n", instanceID)
		fmt.Fprintln(out, "**Status:** unknown (step query failed)")
		fmt.Fprintf(out, "Error: %v\n", err)
		return nil
	}

	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: instanceID})
	if err != nil {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "## Formula Workflow")
		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "**Formula:** %s\n", instanceID)
		fmt.Fprintln(out, "**Status:** unknown (step query failed)")
		fmt.Fprintf(out, "Error: %v\n", err)
		return nil
	}
	totalSteps := result.TotalSteps
	if totalSteps == 0 {
		ts, tsErr := countAllChildren(ctx, store, instanceID)
		if tsErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not count children: %v\n", tsErr)
		}
		totalSteps = ts
	}
	if totalSteps == 0 {
		totalSteps = 1
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "## Formula Workflow")
	fmt.Fprintln(out, "")

	// Get formula name from instance bead title.
	formulaName := instanceID
	if iss, err := store.Get(ctx, instanceID); err == nil && iss.Title != "" {
		formulaName = iss.Title
	}
	fmt.Fprintf(out, "**Formula:** %s\n", formulaName)

	if len(result.Steps) == 0 {
		openChildren, err := store.List(ctx, issuestore.Filter{
			Parent:   instanceID,
			Statuses: []issuestore.Status{issuestore.StatusOpen},
		})
		if err != nil {
			fmt.Fprintf(out, "**Status:** error querying formula state: %v\n", err)
			return nil
		}
		if len(openChildren) > 0 {
			fmt.Fprintln(out, "**Status:** blocked")
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "Some formula steps remain but are not actionable.")
			return nil
		}
		fmt.Fprintln(out, "**Status:** all_complete")
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "All formula steps are complete.")
		return nil
	}

	step := result.Steps[0]
	openChildren, err := store.List(ctx, issuestore.Filter{
		Parent:   instanceID,
		Statuses: []issuestore.Status{issuestore.StatusOpen},
	})
	if err != nil {
		fmt.Fprintf(out, "**Status:** error querying open steps: %v\n", err)
		return nil
	}
	openCount := len(openChildren)
	stepNum := totalSteps - openCount + 1
	if stepNum < 1 {
		stepNum = 1
	}

	fmt.Fprintf(out, "**Progress:** Step %d of %d: %s\n", stepNum, totalSteps, step.Title)
	fmt.Fprintln(out, "**Status:** ready")
	fmt.Fprintln(out, "")

	// Get step description from the store.
	var description string
	if iss, err := store.Get(ctx, step.ID); err == nil {
		description = iss.Description
	}

	tracker := &errorTrackingWriter{w: out}

	outputCommandPreflight(tracker, description)

	if description != "" {
		fmt.Fprintln(tracker, "### Current Step Instructions")
		fmt.Fprintln(tracker, "")
		fmt.Fprintln(tracker, description)
		fmt.Fprintln(tracker, "")
		outputStandingDirective(tracker)
	}

	// Check if this is a gate step
	if isGateStep(ctx, store, step.ID, description) {
		fmt.Fprintln(tracker, "**WARNING: This is a GATE step.** Complete all work, then run:")
		fmt.Fprintf(tracker, "  af done --phase-complete --gate %s\n", step.ID)
		fmt.Fprintln(tracker, "Do NOT skip this gate. Quality gates protect the CEO's time.")
		fmt.Fprintln(tracker, "")
	}

	fmt.Fprintln(tracker, "### Work Loop")
	fmt.Fprintln(tracker, "")
	fmt.Fprintln(tracker, "1. Complete the current step")
	fmt.Fprintln(tracker, "2. Run `af done` to close it and advance")
	fmt.Fprintln(tracker, "3. If gate step: run `af done --phase-complete --gate <gate-id>`")
	fmt.Fprintln(tracker, "4. Run `af prime` again immediately, in the same turn. Only end your turn when steps are blocked or the formula is complete.")

	if tracker.failed {
		// The instructions never reached the agent, so the step has not actually begun.
		return nil
	}
	return &primedStep{
		instanceID: instanceID,
		formula:    formulaName,
		stepID:     step.ID,
		stepLabel:  stepLabelOf(step),
		stepTitle:  step.Title,
		stepSeq:    stepNum,
		isNew:      writeStepPrimed(workDir, step.ID, description),
	}
}

// outputCheckpointContext injects checkpoint/resume context from a previous session.
//
// resumingStepID is the #668 K16 slimming input: the step this prime is continuing, or empty when
// it is starting one. Slimming arms only when all three of the following hold — this is a resume,
// the checkpoint is about the SAME step, and the recycling session left a handoff interview behind.
// Any one of them missing and the full block is emitted, because the only thing that licenses
// dropping a section is a brief that already says what the section says. Absence never arms an
// action (step_context.go:28-29), and here the action is withholding context from a session that
// may need it.
//
// What is never slimmed: the step contract, which this function does not emit at all, and the
// branch clause below, which is the one line here that reports a DIVERGENCE rather than restating
// state — no brief can supersede it because no brief knows what branch the new session is on.
//
// primingStepID and interviewOn are #678 K8(a)'s inputs, and the returned bool says whether that new
// rule is what armed the reduction. It is returned rather than recorded here because this function has
// neither the factory root nor the agent name, which is the same argument outputFormulaContext's
// primedStep makes.
func outputCheckpointContext(out io.Writer, workDir, resumingStepID, primingStepID string, interviewOn bool) bool {
	cp, err := checkpoint.Read(workDir)
	if err != nil || cp == nil {
		return false
	}
	slim, successor := checkpointSlims(cp, resumingStepID, primingStepID, interviewOn)
	if cp.IsStale(24 * time.Hour) {
		_ = checkpoint.Remove(workDir)
		return false
	}

	if cp.CompactionHandoff {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "## Compaction Recovery — Previous Session Checkpoint")
		fmt.Fprintln(out, "This session was recycled because context compaction was imminent.")
		fmt.Fprintln(out, "The previous session's state was checkpointed before recycling.")
		if !cp.CompactionAt.IsZero() {
			fmt.Fprintf(out, "  **Compaction at:** %s\n", cp.CompactionAt.Format(time.RFC3339))
		}
		lastErrorPath := filepath.Join(workDir, ".runtime", "last_error")
		if errData, readErr := os.ReadFile(lastErrorPath); readErr == nil && len(errData) > 0 {
			fmt.Fprintf(out, "  **Last error:** %s\n", strings.TrimSpace(string(errData)))
		}
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "Resume your current task. Your formula step and branch are preserved.")
	} else {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "## Previous Session Checkpoint")
	}
	fmt.Fprintf(out, "A previous session left a checkpoint %s ago.\n\n", cp.Age().Round(time.Minute))
	// Step title, formula and step id are restated from the Formula Workflow block emitted a few
	// lines above by outputFormulaContext, which named the same step for the same reason. On a
	// same-step resume that is the same three facts twice in one prime.
	if !slim {
		if cp.StepTitle != "" {
			fmt.Fprintf(out, "  **Working on:** %s\n", cp.StepTitle)
		}
		if cp.FormulaID != "" {
			fmt.Fprintf(out, "  **Formula:** %s\n", cp.FormulaID)
		}
		if cp.CurrentStep != "" {
			fmt.Fprintf(out, "  **Step:** %s\n", cp.CurrentStep)
		}
	}
	if cp.Branch != "" {
		fmt.Fprintf(out, "  **Branch:** %s\n", cp.Branch)
		currentBranch := getCurrentGitBranch(workDir)
		if currentBranch != "" && cp.Branch != "HEAD" && currentBranch != cp.Branch {
			fmt.Fprintf(out, "  WARNING: Branch changed since checkpoint: was %s, now %s\n", cp.Branch, currentBranch)
		}
	}
	// The brief's artifact list is the same paths, bounded, so the two together are the long form
	// of one fact. Superseded rather than merged: a merge would have to decide which of two lists
	// to trust, and the brief is the one a session wrote deliberately.
	if !slim && len(cp.ModifiedFiles) > 0 {
		fmt.Fprintf(out, "  **Modified files:** %s\n", strings.Join(cp.ModifiedFiles, ", "))
		fmt.Fprintln(out, "  (These files were modified when the previous session checkpointed. Check if changes were committed or lost.)")
	}
	if cp.Notes != "" {
		fmt.Fprintf(out, "  **Notes:** %s\n", cp.Notes)
	}
	outputResumeBrief(out, cp)
	fmt.Fprintln(out, "")
	return successor
}

// checkpointSlims is the slimming decision, and it has two rules because a brief can be about the step
// a session is CONTINUING or about the step a session is STARTING.
//
// The same-step rule (#668 K16) is unchanged and ungated: the session is resuming the very step the
// checkpoint describes, and the brief on that checkpoint already says what the sections restate. The
// successor rule (#678 K8a) is the boundary handoff's case — the recycling session wrote a brief naming
// the step it would NOT get to, and the session that inherits it is starting exactly that step.
//
// The successor rule keys on the brief's own next step id and never on CurrentStep, because CurrentStep
// is the step the RECYCLING session was on and would arm the reduction for the wrong step in the one
// case the rule exists for. An empty next step id matches nothing, which is deliberate: absence must
// never arm an action (step_context.go:28-29), and here the action is withholding context.
//
// interviewOn gates ONLY the new rule. The same-step rule shipped before the switch had any readers
// and its behaviour is not this issue's to change: a factory with tokenomics off — the default — must
// see exactly what it saw yesterday.
func checkpointSlims(cp *checkpoint.Checkpoint, resumingStepID, primingStepID string, interviewOn bool) (slim, successor bool) {
	if !cp.HasResumeBrief() {
		return false, false
	}
	if resumingStepID != "" && cp.CurrentStep == resumingStepID {
		return true, false
	}
	if interviewOn && primingStepID != "" && cp.ResumeNextStepID == primingStepID {
		return true, true
	}
	return false, false
}

// outputResumeBrief renders the #668 K8 handoff interview.
//
// It is emitted on ANY resume that carries one, not only a slimmed one — the brief is what the
// previous session went to the trouble of writing down, and it is worth more than the sections it
// supersedes. Next action first: it is the only line the reader has to act on, and the two below it
// are context for that action.
func outputResumeBrief(out io.Writer, cp *checkpoint.Checkpoint) {
	if !cp.HasResumeBrief() {
		return
	}
	fmt.Fprintf(out, "  **Next action:** %s\n", cp.ResumeNextAction)
	if cp.ResumeVerified != "" {
		fmt.Fprintf(out, "  **Established:** %s\n", cp.ResumeVerified)
	}
	if len(cp.ResumeArtifacts) > 0 {
		fmt.Fprintf(out, "  **Artifacts:** %s\n", strings.Join(cp.ResumeArtifacts, ", "))
		if extra := len(cp.ModifiedFiles) - len(cp.ResumeArtifacts); extra > 0 {
			// Said out loud rather than truncated silently: a reader who cannot tell a complete
			// list from a clipped one will treat the clipped one as complete.
			fmt.Fprintf(out, "  (%d more modified files not listed.)\n", extra)
		}
	}
}

// getCurrentGitBranch returns the current git branch for the given directory.
// Returns empty string on error or if HEAD is detached (literal "HEAD").
func getCurrentGitBranch(workDir string) string {
	cmd := exec.Command("git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// writeFormulaCheckpoint captures current formula state for crash recovery.
// Called during PreCompact (--formula without --hook). Lazy-constructs the
// store via the seam only when a formula is hooked.
func writeFormulaCheckpoint(ctx context.Context, workDir string) {
	instanceID := readHookedFormulaID(workDir)
	if instanceID == "" {
		return
	}
	cp, err := checkpoint.Capture(workDir)
	if err != nil {
		return // best-effort
	}
	actor := os.Getenv("AF_ACTOR")
	store, err := newIssueStore(workDir, actor)
	if err != nil {
		cp.WithFormula(instanceID, "", "")
		cp.WithHookedBead(instanceID)
		if cp.SessionID == "" {
			cp.SessionID = os.Getenv("CLAUDE_SESSION_ID")
		}
		_ = checkpoint.Write(workDir, cp) // best-effort
		return
	}
	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: instanceID})
	if err != nil || len(result.Steps) == 0 {
		cp.WithFormula(instanceID, "", "")
	} else {
		cp.WithFormula(instanceID, result.Steps[0].ID, result.Steps[0].Title)
	}
	cp.WithHookedBead(instanceID)
	if cp.SessionID == "" {
		cp.SessionID = os.Getenv("CLAUDE_SESSION_ID")
	}
	_ = checkpoint.Write(workDir, cp) // best-effort
}

// isGateStep detects whether a step is a gate step using dual detection:
// structural (open blockers) + heuristic (description keywords).
func isGateStep(ctx context.Context, store issuestore.Store, stepID, description string) bool {
	// Primary: structural check via bead metadata
	if stepHasOpenBlockers(ctx, store, stepID) {
		return true
	}
	// Fallback: description text heuristic
	lower := strings.ToLower(description)
	markers := []string{
		"af done --phase-complete",
		"this step has a gate",
		"gate enforces",
		"gate blocks closure",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// stepHasOpenBlockers checks if a step bead has any non-terminal blockers.
//
// Defensive rewrite (Phase 1 investigation finding H-C-A-2): no adapter
// filters Issue.BlockedBy by terminal status. IMPLREADME Gotcha #D6 is
// WRONG — trusting
// len(iss.BlockedBy) > 0 would over-report gate steps whenever a blocker
// is already closed/done. We therefore re-read each blocker via store.Get
// and use Status.IsTerminal() to match the old "status != closed"
// semantics.
func stepHasOpenBlockers(ctx context.Context, store issuestore.Store, stepID string) bool {
	iss, err := store.Get(ctx, stepID)
	if err != nil {
		return false
	}
	for _, ref := range iss.BlockedBy {
		blocker, err := store.Get(ctx, ref.ID)
		if err != nil {
			// Can't verify the blocker — treat as open (defensive).
			return true
		}
		if !blocker.Status.IsTerminal() {
			return true
		}
	}
	return false
}
