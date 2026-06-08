package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
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
	Long:  "Start agent tmux sessions. No args = start all agents from agents.json.",
	RunE:  runUp,
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

	// Resolve agent list
	agents := args
	if len(agents) == 0 {
		for name := range agentsCfg.Agents {
			agents = append(agents, name)
		}
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
			return fmt.Errorf("worktree creation failed for %s: %w", name, wtErr)
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

	// Launch watchdog (best-effort)
	launchWatchdog(cmd, t, root)

	if !allOK {
		return fmt.Errorf("some agents failed to start")
	}
	return nil
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
func launchWatchdog(cmd *cobra.Command, t cmdTmux, root string) {
	watchdogSession := session.WatchdogSessionName()
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
	if err := t.SendKeysDelayed(watchdogSession, "af watchdog", 200); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to start watchdog: %v\n", err)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s\n", watchdogSession)
	}
}
