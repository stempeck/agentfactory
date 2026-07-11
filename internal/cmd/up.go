package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

var upCmd = &cobra.Command{
	Use:   "up [agent...]",
	Short: "Start agent sessions",
	Long: `Start agent tmux sessions. No args = start all agents from agents.json.

A bare 'af up' (no positional args) is driven by .agentfactory/startup.json:
  agents:           omit or null = start ALL agents; [] = start NONE; a subset
                    starts only those agents (the subset must be <= max_worktrees).
  quality/fidelity: "on"/"off" set the gate; default = leave the current gate
                    state unchanged (gates are set at 'af install --init' and
                    changed only via explicit 'af quality'/'af fidelity').
  start_dispatch:   true also starts the dispatcher ('af dispatch start').
  watchdog_agents:  REQUIRED to run the watchdog; names the explicit agents to
                    monitor. Omit/empty ⇒ the watchdog does not start (issue #408;
                    never watches all).

A start set larger than max_worktrees is warned about (not aborted) before launch.
Positional 'af up <names>' ignores startup.json and starts exactly those agents.`,
	RunE: runUp,
}

// upModel is the optional per-launch model profile (or raw model id) applied to
// EVERY agent started by an `af up` invocation (issue #480). It resolves through
// the same shared resolveLaunchModelEnv helper sling/respawn use, so fail-fast and
// precedence stay uniform across entrypoints.
var upModel string

// upSkipFitness applies the fitness-attestation-skip override (issue #508) to EVERY agent started by an
// `af up` invocation: launch a non-loopback model profile without a fitness attestation.
var upSkipFitness bool

func init() {
	upCmd.Flags().StringVar(&upModel, "model", "", "Per-agent model profile (or raw model id) from models.json — applied to every agent started by this `af up`")
	upCmd.Flags().BoolVar(&upSkipFitness, "skip-fitness", false, "Launch non-loopback model profiles without a fitness attestation (loud override; see `af config models attest`)")
	rootCmd.AddCommand(upCmd)
}

func runUp(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}

	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "factory: %s\n", root)

	t := newCmdTmux()
	if !t.IsAvailable() {
		return fmt.Errorf("tmux is not installed or not available")
	}

	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	factoryCfg, err := config.LoadFactoryConfig(config.FactoryConfigPath(root))
	if err != nil {
		return fmt.Errorf("loading factory config: %w", err)
	}

	buildHostCfg, err := config.LoadBuildHostConfig(config.BuildHostConfigPath(root))
	if err != nil {
		return fmt.Errorf("invalid build-host config: %w", err)
	}

	// Load startup config (C-4: an absent file yields defaults, never ErrNotFound;
	// surface only a real parse/validation error).
	startupCfg, err := config.LoadStartupConfig(root)
	if err != nil {
		return fmt.Errorf("loading startup config: %w", err)
	}

	// Resolve the agent list. Positional args ALWAYS win (R-2, the highest C-4 risk):
	// `af up <names>` stays byte-identical to today, so the startup.json-driven actions
	// below are gated on this `blanket` flag. A bare `af up` is driven by startup.json:
	// an explicit agents list (incl. the empty list) is honored, else all agents start.
	agents := args
	blanket := len(args) == 0
	if blanket {
		if startupCfg.Agents != nil {
			agents = startupCfg.Agents
			if len(agents) == 0 {
				// LOW-2: distinguish a deliberate empty set (Agents==[]) from the
				// nil "start all" default with a loud notice.
				fmt.Fprintf(cmd.OutOrStdout(), "startup.json present: 0 configured agents started\n")
			}
		} else {
			for name := range agentsCfg.Agents {
				agents = append(agents, name)
			}
		}
	}

	// SC11 subset×cap pre-flight (CRIT-1): warn — never abort — before the start loop
	// so the operator sees an actionable message instead of the opaque mid-loop cap
	// error. Blanket-only: byte-identical for `af up <names>`.
	if blanket && factoryCfg.MaxWorktrees > 0 && len(agents) > factoryCfg.MaxWorktrees {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: startup set of %d agents exceeds max_worktrees=%d; raise max_worktrees or reduce the agents list\n",
			len(agents), factoryCfg.MaxWorktrees)
	}

	// Omission warning (Gap-1/R-5 + PR2-HIGH-1): detection-only; a configured startup
	// subset that omits a known mail/notify/escalation sink leaves that sink's mail
	// unprocessed until it next runs. Only meaningful when startup.json explicitly
	// configures a subset (Agents != nil) on the blanket path.
	if blanket && startupCfg.Agents != nil {
		warnOmittedSinks(cmd, root, agentsCfg, agents)
	}

	type skippedAgent struct {
		name   string
		reason string
	}
	var skipped []skippedAgent

	// Loud, one-time setup check (issue #371 D-NOEXEC/ADR-014): if the af-managed
	// trailer hook can't actually execute (missing, non-exec bit, or a noexec
	// mount), say so visibly rather than letting a silent stream of trailer-less
	// commits go out. Never blocks startup.
	checkGitHooksExecutable(root, cmd.ErrOrStderr())

	allOK := true
	// K8 (issue #392 / AC-4): accumulate each agent's worktree-resolution Outcome
	// and formula-recovery result so the post-loop step can write a durable
	// factory-root breadcrumb and escalate any ambiguous recovery — neither of
	// which is visible from the per-agent stderr line under a bulk `af up`.
	var runRecords []agentRunRecord
	for _, name := range agents {
		entry, ok := agentsCfg.Agents[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: unknown agent\n", name)
			allOK = false
			continue
		}

		if entry.Formula != "" {
			formulaPath, findErr := formula.FindFormulaFile(entry.Formula, root)
			if findErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: cannot find formula %s: %v\n", name, entry.Formula, findErr)
				allOK = false
				continue
			}
			f, parseErr := formula.ParseFile(formulaPath)
			if parseErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: cannot parse formula %s: %v\n", name, entry.Formula, parseErr)
				allOK = false
				continue
			}
			skillsDir := filepath.Join(root, ".claude", "skills")
			if skillErr := f.ValidateSkills(skillsDir); skillErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", name, skillErr)
				skipped = append(skipped, skippedAgent{name: name, reason: skillErr.Error()})
				allOK = false
				continue
			}
		}

		envWT := os.Getenv("AF_WORKTREE")
		envWTID := os.Getenv("AF_WORKTREE_ID")
		creator, _ := resolveAgentName(wd, root)

		// Issue #188: non-specialist callers should not share worktrees.
		if creator != "" {
			if callerEntry, cErr := resolveCallerAgent(root, creator); cErr == nil && callerEntry.Formula == "" {
				envWT = ""
				envWTID = ""
				creator = ""
			}
		}

		wtPath, wtID, outcome, wtErr := worktree.ResolveOrCreate(root, name, creator, envWT, envWTID, worktree.CreateOpts{MaxWorktrees: factoryCfg.MaxWorktrees})
		if wtErr != nil {
			// PR2-HIGH-2: a worktree-creation failure (cap, disk, git) is best-effort
			// like every other af-up sub-action — warn, skip this agent, and continue
			// starting the rest. allOK=false keeps the aggregate exit non-zero.
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree creation failed for %s: %v (skipping)\n", name, wtErr)
			allOK = false
			continue
		}
		if wtPath != "" {
			if _, setupErr := worktree.SetupAgent(root, wtPath, name, outcome.IsCreated()); setupErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: SetupAgent for %s in %s: %v\n", name, wtPath, setupErr)
			}
			if outcome.IsCreated() {
				fmt.Fprintf(cmd.OutOrStdout(), "Created worktree %s for %s\n", wtID, name)
			}
		}

		mgr := session.NewManager(root, name, entry)
		if buildHostCfg != nil {
			mgr.SetBuildHost(buildHostCfg)
		}
		if wtPath != "" {
			if err := mgr.SetWorktree(wtPath, wtID); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: SetWorktree for %s: %v\n", name, err)
				allOK = false
				continue
			}
		}
		wireGitIdentity(mgr, root, wtPath)
		os.Remove(filepath.Join(config.AgentDir(root, name), ".runtime", "dispatched"))
		if wtPath != "" {
			os.Remove(filepath.Join(config.AgentDir(wtPath, name), ".runtime", "dispatched"))
		}
		// K4 (issue #392): if the in-worktree .runtime/hooked_formula pointer was
		// lost (worktree relocated/removed), rebind it from the durable
		// formula-instance epic BEFORE the agent's first af prime runs — otherwise
		// outputFormulaContext returns silently and the in-flight formula is not
		// resumed. The agent dir is the worktree agent dir when running in a
		// worktree, else the factory-root agent dir (mirrors the dispatched-marker
		// removal above). Best-effort: never blocks or crashes af up (R12).
		agentDir := config.AgentDir(root, name)
		if wtPath != "" {
			agentDir = config.AgentDir(wtPath, name)
		}
		rr := reconstructHookedFormula(cmd.Context(), agentDir, name, cmd.OutOrStdout(), cmd.ErrOrStderr())
		runRecords = append(runRecords, agentRunRecord{
			Agent:     name,
			Outcome:   outcome,
			Recovered: rr.Recovered,
			Ambiguous: rr.Ambiguous,
			OpenCount: rr.OpenCount,
		})
		// Per-agent model selection (issue #480): resolve the model-env export set
		// through the SHARED resolver (same one sling/respawn use) BEFORE Start, so
		// fail-fast and precedence stay uniform. A profile-selecting `--model` that
		// cannot resolve is surfaced per-agent (warn + allOK=false + continue), mirroring
		// every other best-effort sub-failure in this loop — one bad agent must not abort
		// the rest. agentDir (worktree-aware) was derived just above.
		modelName, modelEnv, modelErr := resolveLaunchModelEnv(root, name, agentDir, upModel, entry.Model, upSkipFitness, cmd.ErrOrStderr())
		if modelErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "%s: %v\n", name, modelErr)
			allOK = false
			continue
		}
		if len(modelEnv) > 0 {
			mgr.SetModelEnv(modelEnv)
		}
		if err := mgr.Start(); err != nil {
			if errors.Is(err, session.ErrAlreadyRunning) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: already running\n", session.SessionName(name))
				continue
			}
			if errors.Is(err, session.ErrNotProvisioned) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: skipped (not provisioned, run af install %s)\n", name, name)
				continue
			}
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			allOK = false
			continue
		}
		var parts []string
		// Echo the RESOLVED model (issue #480 discoverability, design-doc.md:109),
		// falling back to the legacy entry.Model — mirrors the sling.go launch echo
		// (PR #482). Without this, --model on an empty-entry.Model agent prints no
		// model, and an overridden agent prints the stale original.
		displayModel := entry.Model
		if modelName != "" {
			displayModel = modelName
		}
		if displayModel != "" {
			parts = append(parts, "model: "+displayModel)
		}
		// Endpoint echo from the resolved set (names only, never auth_token), falling
		// back to the legacy field; empty when neither applies.
		endpoint := entry.BaseURL
		if u := modelEnvValue(modelEnv, "ANTHROPIC_BASE_URL"); u != "" {
			endpoint = u
		}
		if endpoint != "" {
			parts = append(parts, "endpoint: "+endpoint)
		}
		if len(parts) > 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "Started %s (%s)\n", session.SessionName(name), strings.Join(parts, ", "))
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "Started %s\n", session.SessionName(name))
		}
	}

	if len(skipped) > 0 {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: %d agent(s) skipped due to missing skills:\n", len(skipped))
		for _, s := range skipped {
			fmt.Fprintf(cmd.ErrOrStderr(), "  - %s: %s\n", s.name, s.reason)
		}
	}

	// K8 (issue #392 / AC-4): surface the per-agent recovery outcomes durably so
	// ambiguous recovery is never silent under an unattended bulk `af up`. Both
	// actions are best-effort and non-fatal (C-5) — mirroring every other af-up
	// sub-action — so a write or mail failure never aborts the start.
	//
	//   (a) A factory-root breadcrumb (.runtime/af_up_last_run) records each
	//       agent's Outcome + any ambiguity. It lives at the FACTORY root (LOW-1),
	//       which survives agent-worktree teardown — NOT the per-agent in-worktree
	//       .runtime, which is the teardown-fragile state #392 is about.
	//   (b) The durable AC-4 channel is the escalation mail: any agent with >1 open
	//       formula instance (could not auto-resume) is escalated to the supervisor.
	if err := writeUpLastRun(root, formatUpRunSummary(runRecords, time.Now())); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to write .runtime/af_up_last_run breadcrumb: %v\n", err)
	}
	var ambiguousAgents []string
	for _, rec := range runRecords {
		if rec.Ambiguous {
			ambiguousAgents = append(ambiguousAgents, rec.Agent)
		}
	}
	if len(ambiguousAgents) > 0 {
		// Non-fatal, discarded return — matches recoverAgent (watchdog.go) and
		// no-ops under isTestBinary() in unit tests.
		_ = sendHandoffMail(escalationTarget,
			fmt.Sprintf("af up: %d agent(s) with ambiguous formula recovery", len(ambiguousAgents)),
			fmt.Sprintf("These agents had more than one open formula-instance epic and could not "+
				"auto-resume: %s. Resolve manually; see .runtime/af_up_last_run for the run summary.",
				strings.Join(ambiguousAgents, ", ")))
	}

	// Defined action order (Gap-9): agents → gates → dispatch → watchdog, each
	// best-effort (warn+continue, never abort). Gates/dispatch are startup.json-driven
	// and blanket-only so `af up <names>` stays byte-identical (C-4).
	if blanket {
		// AC-3/AC-4: apply gates with the af-up-resolved root (R-7) — for the gate
		// files AND as the fidelity active-formula formulaDir, so the guard checks
		// root/.runtime/hooked_formula no matter which directory af up runs from.
		// applyGate no-ops on ""/"default" (C-4), so the echo guards keep the
		// silent-when-default path.
		if gErr := applyGate(root, root, "quality", startupCfg.Quality); gErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: quality gate: %v\n", gErr)
			allOK = false
		} else if startupCfg.Quality != "" && startupCfg.Quality != "default" {
			fmt.Fprintf(cmd.OutOrStdout(), "quality gate: %s\n", startupCfg.Quality)
		}
		if gErr := applyGate(root, root, "fidelity", startupCfg.Fidelity); gErr != nil {
			// active-formula refusal ⇒ warn + continue (best-effort).
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: fidelity gate: %v\n", gErr)
			allOK = false
		} else if startupCfg.Fidelity != "" && startupCfg.Fidelity != "default" {
			fmt.Fprintf(cmd.OutOrStdout(), "fidelity gate: %s\n", startupCfg.Fidelity)
		}
		if gErr := applyGate(root, root, "improvement", startupCfg.Improvement); gErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: improvement hook: %v\n", gErr)
			allOK = false
		} else if startupCfg.Improvement != "" && startupCfg.Improvement != "default" {
			fmt.Fprintf(cmd.OutOrStdout(), "improvement hook: %s\n", startupCfg.Improvement)
		}

		// AC-5: start the dispatcher when configured (friendly-skips internally when
		// dispatch.json is absent/unconfigured, warns on real config errors; an
		// already-running dispatcher is a benign no-op). A real launch failure is
		// best-effort like every other af-up sub-action: warn + allOK=false.
		if startupCfg.StartDispatch {
			if dErr := startDispatch(cmd, root, t); dErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: dispatcher launch failed: %v\n", dErr)
				allOK = false
			}
		}
	}

	// N5 (issue #408 Phase 3): startup.json.watchdog_agents is the SOLE watchdog
	// scope source, applied on BOTH the blanket and positional `af up` paths (the
	// --agents/--agent flags are gone). Membership is checked here at the cmd layer
	// (startup.go defers it per ADR-004); warn-only — a monitoring-scope typo must
	// not fail the af up exit.
	watchdogScope := startupCfg.WatchdogAgents
	warnUnknownWatchdogAgents(cmd.ErrOrStderr(), watchdogScope, agentsCfg)

	// Launch watchdog (best-effort). N4: pre-checks the scope and skips session
	// creation (notice + breadcrumb) on an empty/all-unknown scope; otherwise
	// launches a bare `af watchdog` (the watchdog self-scopes from startup.json).
	launchWatchdog(cmd, t, root, watchdogScope, agentsCfg)

	if !allOK {
		return fmt.Errorf("some agents failed to start")
	}
	return nil
}

// checkGitHooksExecutable verifies the af-managed prepare-commit-msg hook is
// present AND actually executable in this environment (issue #371 D-NOEXEC). On a
// noexec mount the exec bit is set but the kernel refuses to run the hook, which
// silently drops the Co-authored-by trailer (Gap 2); this converts that silent
// failure into a loud, visible setup error (ADR-014). It never blocks startup —
// agents still run, just without the centralized trailer.
func checkGitHooksExecutable(root string, errw io.Writer) {
	hook := filepath.Join(config.GitHooksDir(root), "prepare-commit-msg")
	info, err := os.Stat(hook)
	if err != nil {
		fmt.Fprintf(errw, "warning: git trailer hook missing (%s): the Co-authored-by trailer will not be applied — run `af install --init`\n", hook)
		return
	}
	if info.Mode().Perm()&0111 == 0 {
		fmt.Fprintf(errw, "warning: git trailer hook %s is not executable: the Co-authored-by trailer will not be applied\n", hook)
		return
	}
	// Real exec probe: the hook no-ops (exit 0) with no args / no AF_COAUTHOR_* env,
	// so a non-nil error here means the kernel refused to exec it (noexec mount).
	if err := exec.Command(hook).Run(); err != nil {
		fmt.Fprintf(errw, "warning: git trailer hook %s is present but not executable in this environment (noexec mount?): the Co-authored-by trailer will not be applied — %v\n", hook, err)
	}
}

// warnUnknownWatchdogAgents warns (never blocks — same philosophy as the rest
// of startup.json handling) when watchdog_agents names an agent that does not
// exist in agents.json: the watchdog poll loop silently skips unknown names,
// so a typo would otherwise shrink monitoring coverage with no signal anywhere.
func warnUnknownWatchdogAgents(w io.Writer, scope []string, agentsCfg *config.AgentConfig) {
	if agentsCfg == nil {
		return
	}
	for _, name := range scope {
		if _, ok := agentsCfg.Agents[name]; !ok {
			fmt.Fprintf(w, "warning: startup.json watchdog_agents names unknown agent %q — it will NOT be monitored (check agents.json for the correct name)\n", name)
		}
	}
}

// warnOmittedSinks emits a best-effort, detection-only warning for any mail/notify/
// escalation sink that is absent from the resolved startup set, so an operator who
// configures a startup subset is told which sink's mail will sit unprocessed until
// it next runs. The detection set is the union of three sources: members of any
// messaging.json group, the dispatch NotifyOnComplete target (default "manager"),
// and escalationTargets() (the centralized escalation recipients, PR2-HIGH-1, so a
// new escalation sink cannot be added without this warning extending to cover it).
// Loading messaging/dispatch is best-effort: any error skips that source — it never
// aborts `af up`.
func warnOmittedSinks(cmd *cobra.Command, root string, agentsCfg *config.AgentConfig, startSet []string) {
	inSet := make(map[string]struct{}, len(startSet))
	for _, n := range startSet {
		inSet[n] = struct{}{}
	}

	sinks := map[string]struct{}{}
	if msgCfg, err := config.LoadMessagingConfig(config.MessagingConfigPath(root), agentsCfg); err == nil {
		for _, members := range msgCfg.Groups {
			for _, m := range members {
				sinks[m] = struct{}{}
			}
		}
	}
	notify := "manager"
	if dispCfg, err := config.LoadDispatchConfig(root); err == nil && dispCfg.NotifyOnComplete != "" {
		notify = dispCfg.NotifyOnComplete
	}
	sinks[notify] = struct{}{}
	for _, e := range escalationTargets() {
		sinks[e] = struct{}{}
	}

	var omitted []string
	for s := range sinks {
		if _, ok := inSet[s]; !ok {
			omitted = append(omitted, s)
		}
	}
	sort.Strings(omitted)
	for _, name := range omitted {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: %q is a mail/notify target but is not in the startup set; its mail will sit unprocessed until it next runs\n", name)
	}
}

// launchWatchdog health-gates the long-lived af-watchdog session (#309 AC-4). It
// mirrors session.Manager.Start's zombie gate, but the watchdog pane runs the
// `af` binary, so liveness is af-pane liveness (IsAgentRunning(ws, "af")), NOT
// IsClaudeRunning. A confirmed-dead watchdog (session present but `af` not
// running) is killed and recreated; a healthy one is left undisturbed; an absent
// one is created. Per design R-5 it is conservative: it kills only a session it
// has confirmed present-but-not-live, never on an ambiguous read. The (re)created
// session is given AF_ROOT (W2) so the watchdog can resolve its factory root even
// if its own cwd is later deleted.
func launchWatchdog(cmd *cobra.Command, t cmdTmux, root string, scope []string, agentsCfg *config.AgentConfig) {
	// N4 (issue #408 Phase 3): pre-check the scope and skip session creation when it
	// is empty or all-unknown — the early, observable echo of the watchdog's own
	// authoritative refusal (resolveWatchdogScope, Phase 2). Skipping prevents the
	// zombie-recreate loop (af up keeps recreating a session whose `af watchdog`
	// immediately self-refuses) and leaves the SAME namespaced breadcrumb the
	// watchdog writes, so operators have one place to look. Best-effort: the skip
	// never sets allOK=false and never changes af up's exit code (W1).
	if skip, reason := watchdogLaunchSkip(scope, agentsCfg); skip {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"watchdog: not started — %s (issue #408; the watchdog never monitors all agents)\n", reason)
		if err := writeWatchdogLastError(root, "af up: watchdog launch skipped — "+reason); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to write watchdog breadcrumb: %v\n", err)
		}
		return
	}

	watchdogSession := session.WatchdogSessionName()
	// Launch a BARE `af watchdog` (issue #408 Phase 3): the watchdog self-scopes from
	// startup.json.watchdog_agents (Phase 2) and the --agents flag no longer exists,
	// so the launch string carries no scope argument. Both the fresh-launch and
	// zombie-recreate paths fall through to the same SendKeysDelayed below.
	watchdogCmd := "af watchdog"
	if running, _ := t.HasSession(watchdogSession); running {
		if t.IsAgentRunning(watchdogSession, "af") {
			return // healthy — leave it undisturbed
		}
		// Zombie — tmux session alive but `af watchdog` dead. Kill and recreate.
		if err := t.KillSession(watchdogSession); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to kill stale watchdog: %v\n", err)
			return
		}
	}
	if err := t.NewSession(watchdogSession, root); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to create watchdog session: %v\n", err)
		return
	}
	_ = t.SetEnvironment(watchdogSession, "AF_ROOT", root)
	if err := t.SendKeysDelayed(watchdogSession, watchdogCmd, 200); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to start watchdog: %v\n", err)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s\n", watchdogSession)
	}
}

// watchdogLaunchSkip decides whether `af up` should skip launching the watchdog,
// mirroring the watchdog's own refuse decision (resolveWatchdogScope, issue #408).
// It reuses the Phase-1 buildWatchdogScope contract (blank-trimmed, non-nil empty
// map) and the agents.json membership idiom (agentsCfg.Agents[name]). It returns
// skip=true when the scope is empty, or when every configured name is unknown vs
// agents.json. Transient-read guard (R1-L1): a nil agentsCfg (unreadable/absent
// agents.json) is NOT treated as all-unknown — prefer launching the configured
// non-empty scope over skipping on a flaky read. A successfully-parsed but EMPTY map
// is NOT a transient read: it routes to the all-unknown skip below. (A
// configured-but-not-running agent is still "known", so membership keys on
// agents.json, never on a live session.)
func watchdogLaunchSkip(scope []string, agentsCfg *config.AgentConfig) (skip bool, reason string) {
	set := buildWatchdogScope(scope, "")
	if len(set) == 0 {
		return true, "no watchdog_agents configured in startup.json"
	}
	if agentsCfg == nil {
		return false, "" // membership unvalidated (transient read) ⇒ launch the configured scope
	}
	// A successfully-parsed but EMPTY map is NOT a transient read: every configured
	// name is unknown, so fall through to the all-unknown skip below (#408/PR#410).
	var unknown []string
	for name := range set {
		if _, ok := agentsCfg.Agents[name]; ok {
			return false, "" // ≥1 known name ⇒ launch
		}
		unknown = append(unknown, name)
	}
	sort.Strings(unknown)
	return true, fmt.Sprintf("none of watchdog_agents {%s} exist in agents.json", strings.Join(unknown, ", "))
}

// recoveryResult is the per-agent outcome of reconstructHookedFormula, surfaced
// so runUp can aggregate it into the K8 run-summary breadcrumb and decide whether
// to escalate. The zero value means "nothing recovered, not ambiguous".
type recoveryResult struct {
	Recovered  bool   // exactly one in-flight instance was rebound
	InstanceID string // the rebound epic ID when Recovered, else ""
	Ambiguous  bool   // >1 open instance — could not auto-resume (the AC-4 signal)
	OpenCount  int    // number of open in-flight instances found
}

// agentRunRecord captures, per agent, how `af up` resolved its worktree and
// whether formula recovery was clean or ambiguous. It feeds the durable
// factory-root breadcrumb (.runtime/af_up_last_run) and the supervisor
// escalation mail (issue #392 K8 / AC-4).
type agentRunRecord struct {
	Agent     string
	Outcome   worktree.Outcome
	Recovered bool
	Ambiguous bool
	OpenCount int
}

// formatUpRunSummary renders the per-agent run records into the breadcrumb body.
// It names every agent with its resolution Outcome and flags any agent whose
// formula recovery was ambiguous (>1 open instance) so an operator scrolling
// past a bulk `af up` can still find what could not auto-resume.
func formatUpRunSummary(records []agentRunRecord, now time.Time) string {
	var b strings.Builder
	fmt.Fprintf(&b, "af up last run: %s\n", now.Format(time.RFC3339))
	if len(records) == 0 {
		b.WriteString("(no agents resolved a worktree this run)\n")
		return b.String()
	}
	for _, rec := range records {
		fmt.Fprintf(&b, "agent %s: outcome=%s recovered=%t", rec.Agent, rec.Outcome, rec.Recovered)
		if rec.Ambiguous {
			fmt.Fprintf(&b, " AMBIGUOUS: %d open formula instances — manual resolution required", rec.OpenCount)
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// writeUpLastRun writes the run-summary breadcrumb to the FACTORY-ROOT .runtime
// (LOW-1): that directory persists across agent-worktree teardown, unlike the
// per-agent in-worktree .runtime which is the very teardown-fragile state #392
// is about. Mirrors the writeLastError idiom (MkdirAll 0o755 → WriteFile 0o644);
// the caller warns-and-continues on error so a breadcrumb failure never fails
// `af up` (C-5).
func writeUpLastRun(root, content string) error {
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(runtimeDir, "af_up_last_run"), []byte(content), 0o644)
}

// reconstructHookedFormula rebinds a lost <agentDir>/.runtime/hooked_formula
// pointer from the durable formula-instance epic (K4, issue #392). The pointer
// is a git-ignored in-worktree cache; when a worktree is relocated or removed it
// is lost, and the next `af prime` then returns silently (outputFormulaContext,
// prime.go) — reporting "no active formula" even though the formula is still in
// flight. This reconstruction runs in `af up` before the agent's first prime so
// resume survives worktree relocation/removal.
//
// It is best-effort and self-scoped:
//   - An already-present pointer is left untouched (the cache is intact).
//   - It reads the agent's OWN formula-instance epics via the explicit
//     Filter.Assignee overlay bypass (ADR-002) — never the cross-actor opt-out
//     flag; a cross-actor read is forbidden (Sec-5a-2).
//   - It keeps only epics with >=1 non-terminal (open OR blocked) child step.
//     A default nil-Statuses child list returns exactly the non-terminal
//     children, so counting them excludes both genuinely-finished formulas and
//     the entire pre-K5 legacy population of OPEN epics whose children are all
//     closed (the MED-1 backstop — "open" alone does not imply "in flight").
//   - Exactly one qualifying instance -> rebind the pointer + stdout notice.
//   - More than one -> a stderr WARNING and rebind nothing (operator resolves).
//   - Zero -> silent (the genuinely-new-agent path, AC-5(ii); byte-identical to
//     today).
//
// Any store/daemon error is reported and swallowed so `af up` is never blocked
// or crashed (R12) — mirroring every other best-effort sub-action in the loop.
//
// It returns a recoveryResult so the caller can aggregate per-agent outcomes for
// the K8 run-summary breadcrumb and the ambiguous-recovery escalation mail. All
// stdout/stderr prints are unchanged (byte-identical to the pre-K8 behavior); the
// return value is purely additive. The zero recoveryResult means "nothing
// recovered, not ambiguous" — returned on every fast-path / error exit.
func reconstructHookedFormula(ctx context.Context, agentDir, agentName string, out, errw io.Writer) recoveryResult {
	if readHookedFormulaID(agentDir) != "" {
		return recoveryResult{} // intact in-worktree cache — leave the existing fast path untouched
	}

	store, err := newIssueStore(agentDir, os.Getenv("AF_ACTOR"))
	if err != nil {
		fmt.Fprintf(errw, "warning: %s: formula-resume store unavailable: %v (continuing)\n", agentName, err)
		return recoveryResult{}
	}

	// Self-scoped read: explicit Assignee suppresses the actor overlay; the
	// default (non-IncludeClosed) filter already excludes terminal epics.
	epics, err := store.List(ctx, issuestore.Filter{
		Assignee: agentName,
		Labels:   []string{"formula-instance"},
	})
	if err != nil {
		fmt.Fprintf(errw, "warning: %s: formula-resume query failed: %v (continuing)\n", agentName, err)
		return recoveryResult{}
	}

	var inFlight []string
	for _, epic := range epics {
		// nil Statuses (NOT IncludeClosed) => exactly the non-terminal children,
		// counting ready AND blocked steps so a sequential formula momentarily at
		// "0 ready" between closing step N and step N+1 becoming ready is not
		// misread as finished (the L3 open-step filter).
		//
		// Assignee scopes this query to the agent's own steps, which — like the
		// epic query above — suppresses the actor overlay (ADR-002). `af up` runs
		// in the launcher's process, whose AF_ACTOR is frequently a different agent
		// (e.g. a manager bringing up a specialist); without the explicit Assignee
		// the overlay would hide every agent-assigned child, collapse the count to
		// 0, and silently defeat resume in exactly that cross-actor case.
		children, childErr := store.List(ctx, issuestore.Filter{Parent: epic.ID, Assignee: agentName})
		if childErr != nil {
			fmt.Fprintf(errw, "warning: %s: formula-resume child query for %s failed: %v (continuing)\n", agentName, epic.ID, childErr)
			return recoveryResult{}
		}
		if len(children) >= 1 {
			inFlight = append(inFlight, epic.ID)
		}
	}

	switch len(inFlight) {
	case 0:
		// Silent: a genuinely-new agent, or a completed one (AC-5(ii)).
		return recoveryResult{}
	case 1:
		persistFormulaInstanceID(agentDir, inFlight[0])
		fmt.Fprintf(out, "Recovered in-flight formula %s for %s (rebound .runtime/hooked_formula)\n", inFlight[0], agentName)
		return recoveryResult{Recovered: true, InstanceID: inFlight[0], OpenCount: 1}
	default:
		fmt.Fprintf(errw, "WARNING: %s: %d open formula instances — cannot auto-resume; resolve manually\n", agentName, len(inFlight))
		return recoveryResult{Ambiguous: true, OpenCount: len(inFlight)}
	}
}
