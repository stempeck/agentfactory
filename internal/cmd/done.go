package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
	"github.com/stempeck/agentfactory/internal/worktree"
)

var (
	donePhaseComplete bool
	doneGate          string
)

var doneCmd = &cobra.Command{
	Use:   "done",
	Short: "Close current formula step and advance workflow",
	Long: `Done closes the current in-progress formula step and advances the workflow.

If more steps remain, it outputs the next step. If all steps are complete, it
mails WORK_DONE to the dispatcher and cleans up the checkpoint.

For gate steps, use --phase-complete --gate <id> to register as a gate waiter.
The session ends and a fresh agent is dispatched when the gate resolves.`,
	RunE: runDone,
}

func init() {
	doneCmd.Flags().BoolVar(&donePhaseComplete, "phase-complete", false, "Signal phase complete, await gate")
	doneCmd.Flags().StringVar(&doneGate, "gate", "", "Gate bead ID (required with --phase-complete)")
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
	// 1. Context discovery
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}

	beadsDir := filepath.Join(factoryRoot, ".beads")

	// Recovery state: .runtime/hooked_formula is the source of truth for the active
	// formula instance. Checkpoint is NOT consulted here — it's informational only.
	instanceID := readHookedFormulaID(cwd)
	if instanceID == "" {
		return fmt.Errorf("no active formula (missing .runtime/hooked_formula)")
	}

	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(cwd, beadsDir, actor)
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
		return sendWorkDoneAndCleanup(ctx, store, cwd, factoryRoot, instanceID)
	}

	step := result.Steps[0]

	// 3. Close current step
	if err := store.Close(ctx, step.ID, ""); err != nil {
		return fmt.Errorf("closing step %s: %w", step.ID, err)
	}
	fmt.Printf("✓ Step closed: %s\n", step.Title)

	// 4. Gate handling
	if phaseComplete {
		if gate == "" {
			return fmt.Errorf("--gate is required with --phase-complete")
		}
		// Try to close the gate bead as phase-complete signal
		_ = store.Close(ctx, gate, "")
		fmt.Printf("✓ Phase complete. Gate %s registered. Session ending.\n", gate)
		return nil
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
		} else {
			fmt.Println("Remaining steps are blocked. Waiting for dependencies.")
		}
		return nil
	}

	// 6. All complete — mail WORK_DONE
	return sendWorkDoneAndCleanup(ctx, store, cwd, factoryRoot, instanceID)
}

// sendWorkDoneAndCleanup sends the WORK_DONE mail and removes the checkpoint.
func sendWorkDoneAndCleanup(ctx context.Context, store issuestore.Store, cwd, factoryRoot, instanceID string) error {
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

	// Find the caller/dispatcher.
	// D1: no fallback. Per H-4/D15, a missing caller file means there is no
	// dispatcher waiting on WORK_DONE mail. Skip the send entirely. Pinned
	// by TestDone_NoCallerFile_NoMail.
	caller := readFormulaCaller(cwd)
	if caller == "" {
		fmt.Fprintln(os.Stderr, "no caller identity found; skipping WORK_DONE mail")
	}

	var mailErr error
	if caller != "" {
		mailErr = sendWorkDoneMail(caller, instanceID, formulaName, totalSteps)
		if mailErr != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to send WORK_DONE mail: %v\n", mailErr)
		}
	}

	// Read dispatch flag BEFORE cleanup removes the marker
	dispatched := isDispatchedSession(cwd)
	shouldTerminate := shouldAutoTerminate(dispatched, mailErr)

	if dispatched && !shouldTerminate {
		fmt.Fprintf(os.Stderr, "warning: skipping auto-terminate because WORK_DONE mail failed\n")
	}

	// Clean up checkpoint and runtime artifacts.
	// Checkpoint is removed on workflow completion. It was never read during this
	// done flow — recovery is fully handled by .runtime/hooked_formula + store.Ready above.
	_ = checkpoint.Remove(cwd)
	cleanupRuntimeArtifacts(cwd)

	// Release identity lock (lock PID belongs to the Claude process from af prime,
	// not af done; Release() simply deletes the file regardless of PID)
	_ = lock.New(cwd).Release()

	// Clean up worktree if this agent was running in one.
	// Must run AFTER cleanupRuntimeArtifacts (which preserves worktree_id/worktree_owner)
	// and BEFORE selfTerminate (which kills the process).
	if wtID := readWorktreeID(cwd); wtID != "" {
		agentName := os.Getenv("AF_ROLE")
		if agentName == "" {
			fmt.Fprintf(os.Stderr, "warning: AF_ROLE not set, skipping worktree cleanup\n")
		} else {
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

	if caller != "" && mailErr == nil {
		fmt.Println("✓ All formula steps complete. WORK_DONE mailed.")
	} else {
		fmt.Println("✓ All formula steps complete.")
	}

	// Auto-terminate dispatched sessions
	if shouldTerminate {
		selfTerminate(cwd, factoryRoot)
	}

	return nil
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

// sendWorkDoneMail shells out to `af mail send` to notify the dispatcher.
func sendWorkDoneMail(caller, instanceID, formulaName string, stepCount int) error {
	if isTestBinary() {
		return nil // no-op under go test to prevent fork bomb
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
	return cmd.Run()
}

// cleanupRuntimeArtifacts removes stale formula runtime files after completion.
// Best-effort removal (ignore errors) matches the existing checkpoint.Remove pattern.
// NOTE: Does NOT remove worktree_id or worktree_owner — those are needed by the
// worktree cleanup block that runs after this function.
func cleanupRuntimeArtifacts(cwd string) {
	os.Remove(filepath.Join(cwd, ".runtime", "hooked_formula"))
	os.Remove(filepath.Join(cwd, ".runtime", "formula_caller"))
	os.Remove(filepath.Join(cwd, ".runtime", "dispatched"))
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
		// Fallback: read .runtime/session_id which contains the tmux session ID
		sessionIDBytes, readErr := os.ReadFile(filepath.Join(cwd, ".runtime", "session_id"))
		if readErr != nil {
			fmt.Fprintf(os.Stderr, "warning: cannot detect agent for auto-terminate: %v (session_id fallback: %v)\n", err, readErr)
			return
		}
		sessionID := strings.TrimSpace(string(sessionIDBytes))
		terminateSession(sessionID, cwd)
		return
	}

	sessionID := session.SessionName(agentName)
	terminateSession(sessionID, cwd)
}

// terminateSession checks if a tmux session exists and kills it.
func terminateSession(sessionID, cwd string) {
	t := tmux.NewTmux()

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
