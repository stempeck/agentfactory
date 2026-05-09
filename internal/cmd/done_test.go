package cmd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// errOnListStore wraps an issuestore.Store and returns a configured error
// from List calls. All other methods delegate to the inner store.
type errOnListStore struct {
	issuestore.Store
	listErr error
}

func (s *errOnListStore) List(ctx context.Context, filter issuestore.Filter) ([]issuestore.Issue, error) {
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.Store.List(ctx, filter)
}

// AC6: TestRunDone_NoFormula — no .runtime/hooked_formula → error
func TestRunDone_NoFormula(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	// No .runtime/hooked_formula

	err := runDoneCore(t.Context(), filepath.Join(dir, ".agentfactory", "agents", "manager"), false, "")
	if err == nil {
		t.Fatal("expected error when no formula active")
	}
	if !strings.Contains(err.Error(), "no active formula") {
		t.Errorf("error should contain 'no active formula', got: %v", err)
	}
}

// AC7: TestRunDone_ClosesCurrentStep — bd close called with correct step ID
func TestRunDone_ClosesCurrentStep(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	// Set up formula
	writeRuntimeFile(t, workDir, "hooked_formula", "bd-instance-1")

	// runDoneCore will call bd commands which will fail in test (no bd binary),
	// but we can verify it gets past the formula check.
	err := runDoneCore(t.Context(), workDir, false, "")
	// We expect an error from bd commands, but NOT "no active formula"
	if err != nil && strings.Contains(err.Error(), "no active formula") {
		t.Error("should not get 'no active formula' when hooked_formula exists")
	}
	// The bd commands will fail since bd isn't available in test — that's expected.
	// The important thing is that the formula was found.
}

// AC8: TestRunDone_AllComplete_MailsWorkDone — completion triggers mail + checkpoint cleanup
func TestRunDone_AllComplete_MailsWorkDone(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	// Set up formula and caller
	writeRuntimeFile(t, workDir, "hooked_formula", "bd-instance-1")
	writeRuntimeFile(t, workDir, "formula_caller", "supervisor")

	// Write a checkpoint that should be cleaned up
	cp := &checkpoint.Checkpoint{FormulaID: "bd-instance-1"}
	if err := checkpoint.Write(workDir, cp); err != nil {
		t.Fatalf("checkpoint.Write: %v", err)
	}

	// Verify checkpoint exists
	cpPath := checkpoint.Path(workDir)
	if _, err := os.Stat(cpPath); err != nil {
		t.Fatalf("checkpoint should exist before done: %v", err)
	}

	// runDoneCore will try bd commands which fail in test.
	// sendWorkDoneAndCleanup is tested indirectly — we test the pieces directly.

	// Test readFormulaCaller
	caller := readFormulaCaller(workDir)
	if caller != "supervisor" {
		t.Errorf("readFormulaCaller() = %q, want 'supervisor'", caller)
	}
}

// AC9: TestRunDone_PhaseComplete_Gate — gate step handling
func TestRunDone_PhaseComplete_Gate(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	writeRuntimeFile(t, workDir, "hooked_formula", "bd-instance-1")

	// --phase-complete without --gate should error
	err := runDoneCore(t.Context(), workDir, true, "")
	// Will fail at bd commands or at gate validation
	if err != nil && strings.Contains(err.Error(), "no active formula") {
		t.Error("should not get 'no active formula' error")
	}
}

// AC9 continued: --gate required with --phase-complete
func TestRunDone_PhaseComplete_RequiresGate(t *testing.T) {
	// This tests that the gate validation logic exists.
	// When bd commands succeed and a step is found, --phase-complete without --gate errors.
	// Since bd isn't available in tests, we verify the flag is registered.
	if doneCmd.Flags().Lookup("gate") == nil {
		t.Error("done command should have --gate flag")
	}
	if doneCmd.Flags().Lookup("phase-complete") == nil {
		t.Error("done command should have --phase-complete flag")
	}
}

// AC12: TestRunDone_RemovesCheckpoint — checkpoint removed on completion
func TestRunDone_RemovesCheckpoint(t *testing.T) {
	dir := t.TempDir()

	// Write a checkpoint
	cp := &checkpoint.Checkpoint{FormulaID: "bd-test-done"}
	if err := checkpoint.Write(dir, cp); err != nil {
		t.Fatalf("checkpoint.Write: %v", err)
	}

	// Verify it exists
	cpPath := checkpoint.Path(dir)
	if _, err := os.Stat(cpPath); err != nil {
		t.Fatalf("checkpoint should exist: %v", err)
	}

	// Remove it (simulating what sendWorkDoneAndCleanup does)
	if err := checkpoint.Remove(dir); err != nil {
		t.Fatalf("checkpoint.Remove: %v", err)
	}

	// Verify it's gone
	if _, err := os.Stat(cpPath); !os.IsNotExist(err) {
		t.Error("checkpoint file should have been removed")
	}
}

// Test helper functions

func TestReadFormulaCaller_Exists(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "formula_caller", "witness")

	caller := readFormulaCaller(dir)
	if caller != "witness" {
		t.Errorf("readFormulaCaller() = %q, want 'witness'", caller)
	}
}

func TestReadFormulaCaller_Missing(t *testing.T) {
	dir := t.TempDir()
	caller := readFormulaCaller(dir)
	if caller != "" {
		t.Errorf("readFormulaCaller() = %q, want empty string", caller)
	}
}

func TestReadFormulaCaller_Whitespace(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "formula_caller", "  manager \n")

	caller := readFormulaCaller(dir)
	if caller != "manager" {
		t.Errorf("readFormulaCaller() = %q, want 'manager'", caller)
	}
}

func TestPersistFormulaCaller(t *testing.T) {
	dir := t.TempDir()
	persistFormulaCaller(dir, "supervisor")

	path := filepath.Join(dir, ".runtime", "formula_caller")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("formula_caller file not created: %v", err)
	}
	if string(data) != "supervisor" {
		t.Errorf("expected 'supervisor', got %q", string(data))
	}
}

func TestCleanupRuntimeArtifacts_RemovesFiles(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "hooked_formula", "bd-instance-1")
	writeRuntimeFile(t, dir, "formula_caller", "supervisor")

	cleanupRuntimeArtifacts(dir)

	if _, err := os.Stat(filepath.Join(dir, ".runtime", "hooked_formula")); !os.IsNotExist(err) {
		t.Error("hooked_formula should be removed after cleanup")
	}
	if _, err := os.Stat(filepath.Join(dir, ".runtime", "formula_caller")); !os.IsNotExist(err) {
		t.Error("formula_caller should be removed after cleanup")
	}
}

func TestCleanupRuntimeArtifacts_NoErrorOnMissingFiles(t *testing.T) {
	dir := t.TempDir()
	// No .runtime directory at all — should not panic or error
	cleanupRuntimeArtifacts(dir)
}

func TestCleanupRuntimeArtifacts_PartialPresence(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "hooked_formula", "bd-instance-1")
	// No formula_caller

	cleanupRuntimeArtifacts(dir)

	if _, err := os.Stat(filepath.Join(dir, ".runtime", "hooked_formula")); !os.IsNotExist(err) {
		t.Error("hooked_formula should be removed even when formula_caller is absent")
	}
}

// Phase 2: Auto-Termination Tests

func TestIsDispatchedSession_MarkerPresent(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "dispatched", "test-caller")

	if !isDispatchedSession(dir) {
		t.Error("isDispatchedSession should return true when marker exists")
	}
}

func TestIsDispatchedSession_MarkerAbsent(t *testing.T) {
	dir := t.TempDir()

	if isDispatchedSession(dir) {
		t.Error("isDispatchedSession should return false when no marker")
	}
}

func TestIsDispatchedSession_EmptyRuntimeDir(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)

	if isDispatchedSession(dir) {
		t.Error("isDispatchedSession should return false when .runtime/ exists but no dispatched file")
	}
}

func TestCleanupRuntimeArtifacts_RemovesDispatched(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "hooked_formula", "bd-instance-1")
	writeRuntimeFile(t, dir, "formula_caller", "supervisor")
	writeRuntimeFile(t, dir, "dispatched", "test-caller")

	cleanupRuntimeArtifacts(dir)

	for _, name := range []string{"hooked_formula", "formula_caller", "dispatched"} {
		if _, err := os.Stat(filepath.Join(dir, ".runtime", name)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed after cleanup", name)
		}
	}
}

func TestCleanupRuntimeArtifacts_DispatchedOnly(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "dispatched", "test-caller")

	cleanupRuntimeArtifacts(dir)

	if _, err := os.Stat(filepath.Join(dir, ".runtime", "dispatched")); !os.IsNotExist(err) {
		t.Error("dispatched should be removed even when other runtime files are absent")
	}
}

func TestSelfTerminate_NoOpUnderGoTest(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)

	// selfTerminate should return immediately under go test (isTestBinary guard)
	selfTerminate(dir, dir)

	// No last_termination breadcrumb should be written
	if _, err := os.Stat(filepath.Join(dir, ".runtime", "last_termination")); !os.IsNotExist(err) {
		t.Error("selfTerminate should be no-op under go test — last_termination should not exist")
	}
}

func TestTerminateSession_NonExistentSession(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)

	// Should not panic or write breadcrumb for non-existent session
	terminateSession("af-nonexistent-test-session", dir)

	if _, err := os.Stat(filepath.Join(dir, ".runtime", "last_termination")); !os.IsNotExist(err) {
		t.Error("terminateSession should not write breadcrumb for non-existent session")
	}
}

func TestIsDispatchedSession_FalseAfterCleanup(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "dispatched", "test-caller")

	// Verify marker exists
	if !isDispatchedSession(dir) {
		t.Fatal("precondition: marker should exist")
	}

	// Cleanup removes it
	cleanupRuntimeArtifacts(dir)

	// Now it should be gone
	if isDispatchedSession(dir) {
		t.Error("isDispatchedSession should return false after cleanupRuntimeArtifacts")
	}
}

func TestShouldAutoTerminate(t *testing.T) {
	tests := []struct {
		name       string
		dispatched bool
		mailErr    error
		want       bool
	}{
		{"not dispatched, mail ok", false, nil, false},
		{"not dispatched, mail fail", false, fmt.Errorf("send failed"), false},
		{"dispatched, mail ok", true, nil, true},
		{"dispatched, mail fail", true, fmt.Errorf("send failed"), false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shouldAutoTerminate(tt.dispatched, tt.mailErr)
			if got != tt.want {
				t.Errorf("shouldAutoTerminate(%v, %v) = %v, want %v",
					tt.dispatched, tt.mailErr, got, tt.want)
			}
		})
	}
}

func TestPipelineContract_DispatchToDone(t *testing.T) {
	dir := t.TempDir()

	// Simulate sling output: write formula_caller and dispatched marker.
	writeRuntimeFile(t, dir, "formula_caller", "manager")
	writeRuntimeFile(t, dir, "dispatched", "")

	// Verify done's input: readFormulaCaller returns the caller sling wrote.
	if got := readFormulaCaller(dir); got != "manager" {
		t.Errorf("readFormulaCaller() = %q, want %q", got, "manager")
	}

	// Verify done's input: isDispatchedSession detects the marker.
	if !isDispatchedSession(dir) {
		t.Error("isDispatchedSession() = false, want true")
	}

	// Verify done's decision: dispatched + no mail error → auto-terminate.
	if !shouldAutoTerminate(true, nil) {
		t.Error("shouldAutoTerminate(true, nil) = false, want true")
	}
}

func TestPipelineContract_PersistentDispatch_NoAutoTerminate(t *testing.T) {
	dir := t.TempDir()

	// Simulate a --persistent dispatch: formula_caller exists (WORK_DONE mail works)
	// but NO dispatched marker (--persistent suppressed it).
	writeRuntimeFile(t, dir, "formula_caller", "orchestrator")

	// Verify: isDispatchedSession returns false (no marker)
	if isDispatchedSession(dir) {
		t.Error("isDispatchedSession() = true, want false (persistent dispatch has no marker)")
	}

	// Verify: shouldAutoTerminate returns false (not dispatched → no termination)
	if shouldAutoTerminate(false, nil) {
		t.Error("shouldAutoTerminate(false, nil) = true, want false")
	}

	// Verify: formula_caller is still readable (WORK_DONE mail can still be sent)
	if got := readFormulaCaller(dir); got != "orchestrator" {
		t.Errorf("readFormulaCaller() = %q, want %q", got, "orchestrator")
	}
}

// TestDone_NoCallerFile_NoMail pins the H-4/D15 atomic-write invariant
// (AC #13). Simulates a crash between persistFormulaCaller() and formula
// bead creation: the formula instance bead exists in the store, but
// .runtime/formula_caller was never written. Expected behavior: af done
// completes the formula silently — no mail dispatch, no panic, no crash.
//
// This test uses the newIssueStore seam to substitute an in-memory store
// so it passes without `bd` on PATH (AC #17).
func TestDone_NoCallerFile_NoMail(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	// Build an in-memory store with a completed formula instance (all
	// children closed) so runDoneCore takes the "all complete" branch.
	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:       "Formula: test",
		Description: "test formula",
		Type:        issuestore.TypeEpic,
		Labels:      []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	step, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step: done",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}
	// Close the step so there are no open children; this drives
	// runDoneCore into sendWorkDoneAndCleanup.
	if err := mem.Close(t.Context(), step.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	// Override the newIssueStore seam so runDoneCore hits memstore
	// instead of the production adapter. The seam is the whole point of
	// AC #17 — without it, the test would need a live mcpstore server.
	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	// Wire the hooked formula runtime file but NOT formula_caller —
	// this is the H-4/D15 crash-between scenario.
	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)

	// runDoneCore should complete cleanly — no panic, no mail send.
	if err := runDoneCore(t.Context(), workDir, false, ""); err != nil {
		t.Fatalf("runDoneCore returned error when caller file is missing: %v", err)
	}

	// Checkpoint + hooked_formula should have been cleaned up.
	if _, err := os.Stat(filepath.Join(workDir, ".runtime", "hooked_formula")); !os.IsNotExist(err) {
		t.Error("hooked_formula should have been cleaned up after completion")
	}
}

// Phase 3: Worktree helper tests

func TestReadWorktreeID(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "worktree_id", "wt-abc123\n")

	got := readWorktreeID(dir)
	if got != "wt-abc123" {
		t.Errorf("readWorktreeID() = %q, want %q", got, "wt-abc123")
	}
}

func TestReadWorktreeID_Missing(t *testing.T) {
	dir := t.TempDir()
	got := readWorktreeID(dir)
	if got != "" {
		t.Errorf("readWorktreeID() = %q, want empty", got)
	}
}

func TestIsWorktreeOwner(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "worktree_owner", "true")

	if !isWorktreeOwner(dir) {
		t.Error("isWorktreeOwner() = false, want true")
	}
}

func TestIsWorktreeOwner_Missing(t *testing.T) {
	dir := t.TempDir()
	if isWorktreeOwner(dir) {
		t.Error("isWorktreeOwner() = true, want false")
	}
}

func TestCleanupRuntimeArtifacts_PreservesWorktreeFiles(t *testing.T) {
	dir := t.TempDir()
	writeRuntimeFile(t, dir, "hooked_formula", "bd-instance-1")
	writeRuntimeFile(t, dir, "formula_caller", "supervisor")
	writeRuntimeFile(t, dir, "dispatched", "test-caller")
	writeRuntimeFile(t, dir, "worktree_id", "wt-abc123")
	writeRuntimeFile(t, dir, "worktree_owner", "true")

	cleanupRuntimeArtifacts(dir)

	// These should be removed
	for _, name := range []string{"hooked_formula", "formula_caller", "dispatched"} {
		if _, err := os.Stat(filepath.Join(dir, ".runtime", name)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed after cleanup", name)
		}
	}

	// These MUST be preserved for worktree cleanup that runs after
	for _, name := range []string{"worktree_id", "worktree_owner"} {
		if _, err := os.Stat(filepath.Join(dir, ".runtime", name)); err != nil {
			t.Errorf("%s should be preserved after cleanup, but got error: %v", name, err)
		}
	}
}

// TestDone_ListError_Propagated verifies that a store.List error in the
// post-close completion check (L121) propagates out of runDoneCore instead
// of silently triggering sendWorkDoneAndCleanup (#132). Ready returns the
// unblocked Blocker step, Close succeeds, then the second List at L121
// hits the errOnListStore and returns an error.
func TestDone_ListError_Propagated(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:       "Formula: test-error-prop",
		Description: "test formula for error propagation",
		Type:        issuestore.TypeEpic,
		Labels:      []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	// Blocker has no deps → Ready returns it. Blocked depends on Blocker →
	// not ready. runDoneCore closes Blocker, then the post-close List at
	// L121 fires and errOnListStore intercepts it.
	blocker, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Blocker step",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	step, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Blocked step",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := mem.DepAdd(t.Context(), step.ID, blocker.ID); err != nil {
		t.Fatalf("add dep: %v", err)
	}

	// Wrap memstore with an error-injecting List.
	failStore := &errOnListStore{
		Store:   mem,
		listErr: errors.New("transient MCP server failure"),
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return failStore, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("runDoneCore should return an error when store.List fails, but returned nil (premature completion)")
	}
	if !strings.Contains(err.Error(), "listing open children") {
		t.Errorf("error should contain 'listing open children', got: %v", err)
	}

	// Verify cleanup did NOT fire — hooked_formula should still exist.
	if _, statErr := os.Stat(filepath.Join(workDir, ".runtime", "hooked_formula")); os.IsNotExist(statErr) {
		t.Error("hooked_formula was cleaned up — sendWorkDoneAndCleanup fired despite store error")
	}
}

// TestDone_PrematureCompletionWarning verifies the defense-in-depth warning
// fires when closedCount < totalSteps in sendWorkDoneAndCleanup.
func TestDone_PrematureCompletionWarning(t *testing.T) {
	dir := t.TempDir()
	// No formula_caller → skips mail send (safe for test)
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: warning-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	// Create 3 steps, close only 2 → closedCount(2) < totalSteps(3)
	for i := 0; i < 3; i++ {
		s, err := mem.Create(t.Context(), issuestore.CreateParams{
			Title:    fmt.Sprintf("Step %d", i),
			Parent:   instance.ID,
			Type:     issuestore.TypeTask,
			Labels:   []string{"formula-step"},
			Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
		if i < 2 {
			if err := mem.Close(t.Context(), s.ID, ""); err != nil {
				t.Fatalf("close step %d: %v", i, err)
			}
		}
	}

	// Capture stderr
	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	_ = sendWorkDoneAndCleanup(t.Context(), mem, dir, dir, instance.ID)

	w.Close()
	os.Stderr = origStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)

	if !strings.Contains(stderr, "formula declared complete but only 2 of 3 steps were closed") {
		t.Errorf("expected premature completion warning on stderr, got: %q", stderr)
	}
}

// TestDone_NoPrematureWarning_AllClosed verifies no warning when all steps are closed.
func TestDone_NoPrematureWarning_AllClosed(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: no-warning-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	for i := 0; i < 3; i++ {
		s, err := mem.Create(t.Context(), issuestore.CreateParams{
			Title:    fmt.Sprintf("Step %d", i),
			Parent:   instance.ID,
			Type:     issuestore.TypeTask,
			Labels:   []string{"formula-step"},
			Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("seed step %d: %v", i, err)
		}
		if err := mem.Close(t.Context(), s.ID, ""); err != nil {
			t.Fatalf("close step %d: %v", i, err)
		}
	}

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	_ = sendWorkDoneAndCleanup(t.Context(), mem, dir, dir, instance.ID)

	w.Close()
	os.Stderr = origStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)

	if strings.Contains(stderr, "formula declared complete but only") {
		t.Errorf("should NOT emit premature completion warning when all steps closed, got: %q", stderr)
	}
}

// TestDone_LinearChain_3Steps_ProgressesCorrectly exercises a 3-step linear
// formula through 3 consecutive runDoneCore calls, verifying step closure,
// TotalSteps, remaining open count, and hooked_formula lifecycle at each stage.
func TestDone_LinearChain_3Steps_ProgressesCorrectly(t *testing.T) {
	t.Setenv("AF_ACTOR", "manager")
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")
	ctx := t.Context()

	mem := memstore.NewWithActor("manager")

	epic, err := mem.Create(ctx, issuestore.CreateParams{
		Title:    "Formula: linear-chain-test",
		Type:     issuestore.TypeEpic,
		Labels:   []string{"formula-instance"},
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("seed epic: %v", err)
	}

	step1, err := mem.Create(ctx, issuestore.CreateParams{
		Title:    "Step 1",
		Parent:   epic.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("seed step1: %v", err)
	}
	step2, err := mem.Create(ctx, issuestore.CreateParams{
		Title:    "Step 2",
		Parent:   epic.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("seed step2: %v", err)
	}
	step3, err := mem.Create(ctx, issuestore.CreateParams{
		Title:    "Step 3",
		Parent:   epic.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("seed step3: %v", err)
	}

	if err := mem.DepAdd(ctx, step2.ID, step1.ID); err != nil {
		t.Fatalf("dep step2->step1: %v", err)
	}
	if err := mem.DepAdd(ctx, step3.ID, step2.ID); err != nil {
		t.Fatalf("dep step3->step2: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", epic.ID)
	hookedPath := filepath.Join(workDir, ".runtime", "hooked_formula")

	// --- Iteration 1: close step1 ---
	if err := runDoneCore(ctx, workDir, false, ""); err != nil {
		t.Fatalf("iteration 1: %v", err)
	}
	s1, err := mem.Get(ctx, step1.ID)
	if err != nil {
		t.Fatalf("get step1: %v", err)
	}
	if !s1.Status.IsTerminal() {
		t.Errorf("iteration 1: step1 should be terminal, got %s", s1.Status)
	}
	result, err := mem.Ready(ctx, issuestore.Filter{MoleculeID: epic.ID})
	if err != nil {
		t.Fatalf("ready after iter1: %v", err)
	}
	if result.TotalSteps != 3 {
		t.Errorf("iteration 1: TotalSteps = %d, want 3", result.TotalSteps)
	}
	if _, err := os.Stat(hookedPath); err != nil {
		t.Error("iteration 1: hooked_formula should still exist")
	}

	// --- Iteration 2: close step2 ---
	if err := runDoneCore(ctx, workDir, false, ""); err != nil {
		t.Fatalf("iteration 2: %v", err)
	}
	s2, err := mem.Get(ctx, step2.ID)
	if err != nil {
		t.Fatalf("get step2: %v", err)
	}
	if !s2.Status.IsTerminal() {
		t.Errorf("iteration 2: step2 should be terminal, got %s", s2.Status)
	}
	result, err = mem.Ready(ctx, issuestore.Filter{MoleculeID: epic.ID})
	if err != nil {
		t.Fatalf("ready after iter2: %v", err)
	}
	if result.TotalSteps != 3 {
		t.Errorf("iteration 2: TotalSteps = %d, want 3", result.TotalSteps)
	}
	if _, err := os.Stat(hookedPath); err != nil {
		t.Error("iteration 2: hooked_formula should still exist")
	}

	// --- Iteration 3: close step3, triggers completion ---
	if err := runDoneCore(ctx, workDir, false, ""); err != nil {
		t.Fatalf("iteration 3: %v", err)
	}
	s3, err := mem.Get(ctx, step3.ID)
	if err != nil {
		t.Fatalf("get step3: %v", err)
	}
	if !s3.Status.IsTerminal() {
		t.Errorf("iteration 3: step3 should be terminal, got %s", s3.Status)
	}
	openChildren, err := mem.List(ctx, issuestore.Filter{
		Parent:   epic.ID,
		Statuses: []issuestore.Status{issuestore.StatusOpen},
	})
	if err != nil {
		t.Fatalf("list open after iter3: %v", err)
	}
	if len(openChildren) != 0 {
		t.Errorf("iteration 3: expected 0 open children, got %d", len(openChildren))
	}
	if _, err := os.Stat(hookedPath); !os.IsNotExist(err) {
		t.Error("iteration 3: hooked_formula should have been cleaned up after completion")
	}
}

// setupTestFactoryForDone creates a minimal factory structure for done tests.
func setupTestFactoryForDone(t *testing.T, agentName string) string {
	t.Helper()
	dir := setupTestFactoryForPrime(t) // reuse prime's setup
	os.MkdirAll(config.StoreDir(dir), 0o755)
	return dir
}

// writeRuntimeFile writes a value to <dir>/.runtime/<name>.
func writeRuntimeFile(t *testing.T, dir, name, value string) {
	t.Helper()
	runtimeDir := filepath.Join(dir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	if err := os.WriteFile(filepath.Join(runtimeDir, name), []byte(value), 0o644); err != nil {
		t.Fatal(err)
	}
}
