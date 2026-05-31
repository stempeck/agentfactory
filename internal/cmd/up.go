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
	"github.com/stempeck/agentfactory/internal/tmux"
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

	t := tmux.NewTmux()
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

	if !allOK {
		return fmt.Errorf("some agents failed to start")
	}
	return nil
}
