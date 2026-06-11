package cmd

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
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
  watchdog_agents:  scope the watchdog to a subset (omit/null = all agents).

A start set larger than max_worktrees is warned about (not aborted) before launch.
Positional 'af up <names>' ignores startup.json and starts exactly those agents.`,
	RunE: runUp,
}

func init() {
	rootCmd.AddCommand(upCmd)
}

func runUp(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}

	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

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

	allOK := true
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

		wtPath, wtID, created, wtErr := worktree.ResolveOrCreate(root, name, creator, envWT, envWTID, worktree.CreateOpts{MaxWorktrees: factoryCfg.MaxWorktrees})
		if wtErr != nil {
			// PR2-HIGH-2: a worktree-creation failure (cap, disk, git) is best-effort
			// like every other af-up sub-action — warn, skip this agent, and continue
			// starting the rest. allOK=false keeps the aggregate exit non-zero.
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree creation failed for %s: %v (skipping)\n", name, wtErr)
			allOK = false
			continue
		}
		if wtPath != "" {
			if _, setupErr := worktree.SetupAgent(root, wtPath, name, created); setupErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: SetupAgent for %s in %s: %v\n", name, wtPath, setupErr)
			}
			if created {
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
		os.Remove(filepath.Join(config.AgentDir(root, name), ".runtime", "dispatched"))
		if wtPath != "" {
			os.Remove(filepath.Join(config.AgentDir(wtPath, name), ".runtime", "dispatched"))
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
		if entry.Model != "" {
			parts = append(parts, "model: "+entry.Model)
		}
		if entry.BaseURL != "" {
			parts = append(parts, "endpoint: "+entry.BaseURL)
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

	// Defined action order (Gap-9): agents → gates → dispatch → watchdog, each
	// best-effort (warn+continue, never abort). Gates/dispatch are startup.json-driven
	// and blanket-only so `af up <names>` stays byte-identical (C-4). The watchdog runs
	// on every path; its scope is only sourced from startup.json on the blanket path.
	var watchdogScope []string
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

		// AC-6: scope the watchdog to the configured agents. Membership is checked
		// here at the cmd layer (startup.go defers it per ADR-004); warn-only — a
		// monitoring-scope typo must not fail the af up exit.
		watchdogScope = startupCfg.WatchdogAgents
		warnUnknownWatchdogAgents(cmd.ErrOrStderr(), watchdogScope, agentsCfg)
	}

	// Launch watchdog (best-effort)
	launchWatchdog(cmd, t, root, watchdogScope)

	if !allOK {
		return fmt.Errorf("some agents failed to start")
	}
	return nil
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
func launchWatchdog(cmd *cobra.Command, t cmdTmux, root string, scope []string) {
	watchdogSession := session.WatchdogSessionName()
	// Build the launch string ONCE so both the fresh-launch and zombie-recreate
	// paths (which fall through to the same SendKeysDelayed below) carry the scope
	// (R-6/Gap-6). scope == nil keeps the bare "af watchdog" (C-4 default).
	watchdogCmd := "af watchdog"
	if len(scope) > 0 {
		watchdogCmd = "af watchdog --agents " + strings.Join(scope, ",")
	}
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
