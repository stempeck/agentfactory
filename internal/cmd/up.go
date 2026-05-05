package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
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

	// Resolve agent list
	agents := args
	if len(agents) == 0 {
		for name := range agentsCfg.Agents {
			agents = append(agents, name)
		}
	}

	allOK := true
	for _, name := range agents {
		entry, ok := agentsCfg.Agents[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: unknown agent\n", name)
			allOK = false
			continue
		}

		envWT := os.Getenv("AF_WORKTREE")
		envWTID := os.Getenv("AF_WORKTREE_ID")
		creator, _ := resolveAgentName(wd, root)
		wtPath, wtID, created, wtErr := worktree.ResolveOrCreate(root, name, creator, envWT, envWTID)
		if wtErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree resolution for %s: %v\n", name, wtErr)
			wtPath, wtID = "", ""
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
		fmt.Fprintf(cmd.OutOrStdout(), "Started %s\n", session.SessionName(name))
	}

	if !allOK {
		return fmt.Errorf("some agents failed to start")
	}
	return nil
}
