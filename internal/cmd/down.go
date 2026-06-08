package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

var downAll bool
var downReset bool

var downCmd = &cobra.Command{
	Use:   "down [agent...]",
	Short: "Stop agent sessions",
	Long:  "Stop agent tmux sessions. No args = stop all agents from agents.json.",
	RunE:  runDown,
}

func init() {
	downCmd.Flags().BoolVar(&downAll, "all", false, "Also kill orphaned Claude agent processes")
	downCmd.Flags().BoolVar(&downReset, "reset", false,
		"Force-remove worktrees and close formula beads (destructive, cannot be undone)")
	rootCmd.AddCommand(downCmd)
}

func runDown(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}

	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
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

	if downReset {
		preResetScan(cmd, root, agents)
	}

	ctx := context.Background()
	allOK := true
	for _, name := range agents {
		entry, ok := agentsCfg.Agents[name]
		if !ok {
			fmt.Fprintf(os.Stderr, "%s: unknown agent\n", name)
			allOK = false
			continue
		}

		mgr := session.NewManager(root, name, entry)
		if err := mgr.Stop(); err != nil {
			if errors.Is(err, session.ErrNotRunning) {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: not running\n", session.SessionName(name))
				if downReset {
					resetAgent(ctx, cmd, root, name)
				} else {
					cleanupAgentWorktree(cmd, root, name)
					os.Remove(filepath.Join(config.AgentDir(root, name), ".runtime", "dispatched"))
				}
				continue
			}
			fmt.Fprintf(os.Stderr, "%s: %v\n", name, err)
			allOK = false
			continue
		}
		fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", session.SessionName(name))
		if downReset {
			resetAgent(ctx, cmd, root, name)
		} else {
			cleanupAgentWorktree(cmd, root, name)
			os.Remove(filepath.Join(config.AgentDir(root, name), ".runtime", "dispatched"))
		}
	}

	// Kill watchdog session when stopping all agents
	if len(args) == 0 {
		watchdogSession := session.WatchdogSessionName()
		tx := newCmdTmux()
		if running, _ := tx.HasSession(watchdogSession); running {
			_ = tx.KillSession(watchdogSession)
			fmt.Fprintf(cmd.OutOrStdout(), "Stopped %s\n", watchdogSession)
		}
	}

	if downAll {
		killOrphanedClaudeProcesses()
	}

	if downReset {
		fmt.Fprintf(cmd.OutOrStdout(), "Reset complete.\n")
	}

	if !allOK {
		return fmt.Errorf("some agents failed to stop")
	}
	return nil
}

// cleanupAgentWorktree removes a worktree owned by the named agent.
// Deregisters the agent first via RemoveAgent, then removes the worktree only
// if no co-tenant agents remain (R-INT-3 safety).
// Non-fatal: logs warnings on errors, never returns an error.
func cleanupAgentWorktree(cmd *cobra.Command, factoryRoot, agentName string) {
	meta, err := worktree.FindByOwner(factoryRoot, agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: worktree lookup: %v\n", agentName, err)
		return
	}
	if meta == nil {
		return
	}
	updated, empty, err := worktree.RemoveAgent(factoryRoot, meta.ID, agentName)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: worktree RemoveAgent: %v\n", agentName, err)
		return
	}
	if empty {
		if rmErr := worktree.Remove(factoryRoot, updated); rmErr != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: worktree cleanup: %v\n", agentName, rmErr)
			fmt.Fprintf(os.Stderr, "  hint: use 'af down %s --reset' to force-remove worktrees with uncommitted changes\n", agentName)
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: cleaned up worktree %s\n", agentName, meta.ID)
		}
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "%s: deregistered from worktree %s (%d agents remain)\n", agentName, meta.ID, len(updated.Agents))
	}
}

func preResetScan(cmd *cobra.Command, factoryRoot string, agents []string) {
	w := cmd.ErrOrStderr()
	fmt.Fprintf(w, "WARNING: --reset will permanently destroy agent worktrees and close formula beads.\n")
	fmt.Fprintf(w, "  The following will be affected:\n")
	for _, name := range agents {
		wtLabel := "no worktree"
		meta, err := worktree.FindByAgent(factoryRoot, name)
		if err == nil && meta != nil {
			wtLabel = fmt.Sprintf("worktree %s", meta.ID)
		}

		beadLabel := "0 open beads"
		store, err := newIssueStore(factoryRoot, name)
		if err != nil {
			beadLabel = "beads: unavailable"
		} else {
			beads, lErr := store.List(context.Background(), issuestore.Filter{Assignee: name})
			if lErr != nil {
				beadLabel = "beads: unavailable"
			} else {
				beadLabel = fmt.Sprintf("%d open beads", len(beads))
			}
		}
		fmt.Fprintf(w, "    %-20s %s, %s\n", name+":", wtLabel, beadLabel)
	}
	fmt.Fprintf(w, "  This action cannot be undone.\n")
}

func resetAgent(ctx context.Context, cmd *cobra.Command, factoryRoot, agentName string) error {
	return resetAgentState(ctx, cmd.OutOrStdout(), factoryRoot, agentName, "reset by af down --reset")
}

func closeAgentBeads(ctx context.Context, store issuestore.Store, agentName, reason string) int {
	beads, err := store.List(ctx, issuestore.Filter{Assignee: agentName})
	if err != nil {
		fmt.Fprintf(os.Stderr, "%s: warning: listing beads: %v\n", agentName, err)
		return 0
	}
	closed := 0
	for _, bead := range beads {
		if err := store.Close(ctx, bead.ID, reason); err != nil {
			fmt.Fprintf(os.Stderr, "%s: warning: closing bead %s: %v\n", agentName, bead.ID, err)
			continue
		}
		closed++
	}
	return closed
}

func killOrphanedClaudeProcesses() {
	// All af-managed agents are now launched with --dangerously-skip-permissions,
	// so this pattern matches all agentfactory Claude processes regardless of type.
	pattern := "claude.*--dangerously-skip-permissions"

	// Check if any orphaned processes exist
	check := exec.Command("pgrep", "-f", pattern)
	if err := check.Run(); err != nil {
		return // No orphaned processes
	}

	// Kill them with SIGKILL
	kill := exec.Command("pkill", "-9", "-f", pattern)
	_ = kill.Run()
}
