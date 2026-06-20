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
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/session"
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
The session ends and a fresh agent is dispatched when the gate resolves.`,
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
	// 1. Context discovery
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}

	// Recovery state: .runtime/hooked_formula is the source of truth for the active
	// formula instance. Checkpoint is NOT consulted here — it's informational only.
	instanceID := readHookedFormulaID(cwd)
	if instanceID == "" {
		return fmt.Errorf("no active formula (missing .runtime/hooked_formula)")
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
		return sendWorkDoneAndCleanup(ctx, store, cwd, factoryRoot, instanceID)
	}

	step := result.Steps[0]

	// 2a. Write last_closed_step identity BEFORE closing (for fidelity gate hook)
	if err := writeLastClosedStep(ctx, cwd, step, instanceID, store); err != nil {
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
	if err := store.Close(ctx, instanceID, "formula complete"); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not close formula-instance epic %s: %v\n", instanceID, err)
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

	// Clean up checkpoint and runtime artifacts.
	// Checkpoint is removed on workflow completion. It was never read during this
	// done flow — recovery is fully handled by .runtime/hooked_formula + store.Ready above.
	_ = checkpoint.Remove(cwd)
	cleanupRuntimeArtifacts(cwd)

	// Release identity lock (lock PID belongs to the Claude process from af prime,
	// not af done; Release() simply deletes the file regardless of PID)
	_ = lock.New(cwd).Release()

	// Clean up worktree if this agent was running in one AND the session
	// will be terminated. If the session survives (not dispatched, or mail
	// failed), preserve the worktree so the shell CWD remains valid.
	if shouldTerminate {
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
	t := newCmdTmux()

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

type lastClosedStepRecord struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	Description string `json:"description"`
	ClosedAt    string `json:"closed_at"`
	Formula     string `json:"formula"`
}

func writeLastClosedStep(ctx context.Context, workDir string, step issuestore.Issue, instanceID string, store issuestore.Store) error {
	runtimeDir := filepath.Join(workDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
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
		return fmt.Errorf("marshaling last closed step: %w", err)
	}
	data = append(data, '\n')
	return os.WriteFile(filepath.Join(runtimeDir, "last_closed_step"), data, 0o644)
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
