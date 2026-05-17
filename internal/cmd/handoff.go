package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

var (
	handoffSubject string
	handoffMessage string
	handoffCollect bool
	handoffIdle    bool
	handoffDryRun  bool
)

var handoffCmd = &cobra.Command{
	Use:   "handoff",
	Short: "Recycle the current session, preserving context for the next",
	Long: `Handoff writes a checkpoint, sends a handoff mail to self, clears tmux
scrollback, and respawns the pane with a fresh Claude session. The new session
auto-primes with the checkpoint context and handoff mail.

Must be run from inside a tmux agent session.`,
	RunE: runHandoff,
}

func init() {
	handoffCmd.Flags().StringVarP(&handoffSubject, "subject", "s", "HANDOFF: Session cycling", "Mail subject")
	handoffCmd.Flags().StringVarP(&handoffMessage, "message", "m", "Context cycling. Run af prime for current step.", "Mail body")
	handoffCmd.Flags().BoolVarP(&handoffCollect, "collect", "c", false, "Collect and append current state to message")
	handoffCmd.Flags().BoolVar(&handoffIdle, "idle", false, "Signal that this cycle found no work (increments back-off counter)")
	handoffCmd.Flags().BoolVarP(&handoffDryRun, "dry-run", "n", false, "Show what would happen without executing")
	rootCmd.AddCommand(handoffCmd)
}

func runHandoff(cmd *cobra.Command, args []string) error {
	cwd, err := getWd()
	if err != nil {
		return err
	}
	return runHandoffCore(cmd.Context(), cwd, handoffSubject, handoffMessage, handoffCollect, handoffIdle, handoffDryRun)
}

// runHandoffCore contains the core logic for af handoff, separated from cobra for testability.
func runHandoffCore(ctx context.Context, cwd, subject, message string, collect, idle, dryRun bool) error {
	// 1. Validate tmux environment
	if !tmux.IsInsideTmux(os.Getenv("TMUX")) {
		return fmt.Errorf("af handoff must be run inside a tmux session")
	}
	pane := os.Getenv("TMUX_PANE")
	if pane == "" {
		return fmt.Errorf("TMUX_PANE not set — cannot identify current pane")
	}

	// 2. Find factory root
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return fmt.Errorf("finding factory root: %w", err)
	}

	// 3. Detect agent
	agentName, agentEntry, err := detectRole(cwd, factoryRoot)
	if err != nil {
		return fmt.Errorf("detecting agent: %w", err)
	}

	// 4. Collect state if requested
	if collect {
		collected := collectHandoffState(ctx, cwd, factoryRoot)
		if collected != "" {
			message = message + "\n\n" + collected
		}
	}

	// 5. Write checkpoint
	if dryRun {
		fmt.Printf("[dry-run] Would write checkpoint with notes: %s\n", subject)
	} else {
		if err := writeHandoffCheckpoint(ctx, cwd, factoryRoot, subject); err != nil {
			fmt.Fprintf(os.Stderr, "warning: checkpoint write failed: %v\n", err)
		} else {
			fmt.Println("Checkpoint written")
		}
	}

	// 6. Send mail to self
	if dryRun {
		fmt.Printf("[dry-run] Would send handoff mail to %s\n", agentName)
	} else {
		if err := sendHandoffMail(agentName, subject, message); err != nil {
			fmt.Fprintf(os.Stderr, "warning: mail send failed: %v\n", err)
		} else {
			fmt.Println("Handoff mail sent to self")
		}
	}

	// 7. Handle idle back-off counter
	sleepPrefix := ""
	if idle {
		cycles := readIdleCycles(cwd)
		cycles++
		writeIdleCycles(cwd, cycles)
		delay := idleBackoffSeconds(cycles)
		sleepPrefix = fmt.Sprintf("sleep %d && ", delay)
	} else {
		removeIdleCycles(cwd)
	}

	// 8. Build respawn command
	mgr := session.NewManager(factoryRoot, agentName, *agentEntry)
	mgr.SetInitialPrompt("af prime")
	respawnCmd := sleepPrefix + mgr.BuildStartupCommand()

	if dryRun {
		fmt.Printf("[dry-run] Would clear tmux history for pane %s\n", pane)
		fmt.Printf("[dry-run] Would respawn pane with: %s\n", respawnCmd)
		return nil
	}

	// 9. Clear tmux history and respawn pane
	fmt.Printf("Handing off %s...\n", agentName)
	tx := tmux.NewTmux()
	_ = tx.ClearHistory(pane) // best-effort

	// Process dies here — RespawnPane kills the current pane process
	return tx.RespawnPane(pane, respawnCmd)
}

// writeHandoffCheckpoint captures state and writes a checkpoint with handoff notes.
func writeHandoffCheckpoint(ctx context.Context, cwd, factoryRoot, subject string) error {
	cp, err := checkpoint.Capture(cwd)
	if err != nil {
		return err
	}

	formulaID := readHookedFormulaID(cwd)
	if formulaID != "" {
		actor := os.Getenv("AF_ACTOR")
		if store, err := newIssueStore(cwd, actor); err == nil {
			result, _ := store.Ready(ctx, issuestore.Filter{MoleculeID: formulaID})
			if len(result.Steps) > 0 {
				cp.WithFormula(formulaID, result.Steps[0].ID, result.Steps[0].Title)
			}
		}
	}

	cp.WithNotes(subject)
	if cp.SessionID == "" {
		cp.SessionID = os.Getenv("CLAUDE_SESSION_ID")
	}
	return checkpoint.Write(cwd, cp)
}

// sendHandoffMail shells out to `af mail send` to deliver the handoff message.
func sendHandoffMail(agentName, subject, body string) error {
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

	cmd := exec.Command(afPath, "mail", "send", agentName, "-s", subject, "-m", body)
	cmd.Env = os.Environ()

	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("mail send to %s failed: %w\nsubprocess stderr: %s", agentName, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("mail send to %s: %w", agentName, err)
	}
	return nil
}

// idleBackoffSeconds computes the back-off delay for a given number of idle cycles.
// Linear: 60s per cycle, capped at 1800s (30 minutes).
func idleBackoffSeconds(cycles int) int {
	delay := cycles * 60
	if delay > 1800 {
		return 1800
	}
	return delay
}

// readIdleCycles reads the idle counter from .runtime/idle_cycles, returning 0 if absent.
func readIdleCycles(cwd string) int {
	data, err := os.ReadFile(filepath.Join(cwd, ".runtime", "idle_cycles"))
	if err != nil {
		return 0
	}
	n := 0
	fmt.Sscanf(strings.TrimSpace(string(data)), "%d", &n)
	return n
}

// writeIdleCycles writes the idle counter to .runtime/idle_cycles.
func writeIdleCycles(cwd string, cycles int) {
	runtimeDir := filepath.Join(cwd, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "idle_cycles"), []byte(fmt.Sprintf("%d", cycles)), 0o644)
}

// removeIdleCycles deletes the idle counter file, resetting back-off state.
func removeIdleCycles(cwd string) {
	os.Remove(filepath.Join(cwd, ".runtime", "idle_cycles"))
}

// collectHandoffState gathers formula progress, inbox count, and modified files.
func collectHandoffState(ctx context.Context, cwd, factoryRoot string) string {
	var parts []string

	// Formula progress
	formulaID := readHookedFormulaID(cwd)
	if formulaID != "" {
		actor := os.Getenv("AF_ACTOR")
		if store, err := newIssueStore(cwd, actor); err == nil {
			result, _ := store.Ready(ctx, issuestore.Filter{MoleculeID: formulaID})
			if len(result.Steps) > 0 {
				parts = append(parts, fmt.Sprintf("Formula: %s, next step: %s (%s)", formulaID, result.Steps[0].ID, result.Steps[0].Title))
			} else {
				parts = append(parts, fmt.Sprintf("Formula: %s (no ready steps)", formulaID))
			}
		} else {
			parts = append(parts, fmt.Sprintf("Formula: %s (store init failed)", formulaID))
		}
	}

	// Modified files from checkpoint capture
	cp, err := checkpoint.Capture(cwd)
	if err == nil && len(cp.ModifiedFiles) > 0 {
		parts = append(parts, fmt.Sprintf("Modified files: %d", len(cp.ModifiedFiles)))
		for _, f := range cp.ModifiedFiles {
			parts = append(parts, fmt.Sprintf("  %s", f))
		}
	}

	if len(parts) == 0 {
		return ""
	}

	result := "--- Collected State ---\n"
	for _, p := range parts {
		result += p + "\n"
	}
	return result
}
