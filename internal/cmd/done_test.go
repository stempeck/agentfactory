package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

type errOnGetStore struct {
	issuestore.Store
	getErr error
}

func (s *errOnGetStore) Get(ctx context.Context, id string) (issuestore.Issue, error) {
	return issuestore.Issue{}, s.getErr
}

func TestCheckStepPrimed_CompoundFormat(t *testing.T) {
	dir := t.TempDir()
	workDir := dir

	mem := memstore.New()
	ctx := t.Context()

	desc := "do the work for this step"
	step, err := mem.Create(ctx, issuestore.CreateParams{
		Title:       "Step: compound test",
		Description: desc,
		Type:        issuestore.TypeTask,
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}

	h := sha256.Sum256([]byte(desc))
	hash := fmt.Sprintf("%x", h[:4])
	writeRuntimeFile(t, workDir, "step_primed", step.ID+":"+hash)

	if err := checkStepPrimed(ctx, workDir, step.ID, mem); err != nil {
		t.Errorf("expected no error for correct compound format, got: %v", err)
	}
}

func TestCheckStepPrimed_HashMismatch(t *testing.T) {
	dir := t.TempDir()
	workDir := dir

	mem := memstore.New()
	ctx := t.Context()

	step, err := mem.Create(ctx, issuestore.CreateParams{
		Title:       "Step: hash mismatch",
		Description: "original instructions",
		Type:        issuestore.TypeTask,
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}

	writeRuntimeFile(t, workDir, "step_primed", step.ID+":deadbeef")

	err = checkStepPrimed(ctx, workDir, step.ID, mem)
	if err == nil {
		t.Fatal("expected error for hash mismatch, got nil")
	}
	if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("error should contain 'hash mismatch', got: %v", err)
	}
}

func TestCheckStepPrimed_LegacyFormat(t *testing.T) {
	dir := t.TempDir()
	workDir := dir

	mem := memstore.New()
	ctx := t.Context()

	stepID := "legacy-step-42"
	writeRuntimeFile(t, workDir, "step_primed", stepID)

	if err := checkStepPrimed(ctx, workDir, stepID, mem); err != nil {
		t.Errorf("expected no error for legacy format, got: %v", err)
	}
}

func TestCheckStepPrimed_StoreUnavailable(t *testing.T) {
	dir := t.TempDir()
	workDir := dir

	ctx := t.Context()

	stepID := "step-store-fail"
	writeRuntimeFile(t, workDir, "step_primed", stepID+":abcd1234")

	failStore := &errOnGetStore{
		Store:  memstore.New(),
		getErr: errors.New("store unavailable"),
	}

	if err := checkStepPrimed(ctx, workDir, stepID, failStore); err != nil {
		t.Errorf("expected graceful degradation when store unavailable, got: %v", err)
	}
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

	// Should not panic or write breadcrumb for non-existent session. Uses a
	// non-production af-test-<hex>- name so the literal is unambiguously outside
	// the production identity class the GUARD protects (cosmetic; terminateSession
	// early-returns on the read-only HasSession probe regardless).
	terminateSession("af-test-deadbeef-nonexistent", dir)

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
	writeRuntimeFile(t, workDir, "step_primed", blocker.ID)

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
	writeRuntimeFile(t, workDir, "step_primed", step1.ID)
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
	writeRuntimeFile(t, workDir, "step_primed", step2.ID)
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
	writeRuntimeFile(t, workDir, "step_primed", step3.ID)
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

// TestDone_WorktreePreserved_WhenNotDispatched verifies that when shouldTerminate
// is false (non-dispatched session), the worktree removal block is skipped.
func TestDone_WorktreePreserved_WhenNotDispatched(t *testing.T) {
	t.Setenv("AF_ROLE", "test-agent")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)

	writeRuntimeFile(t, dir, "worktree_id", "wt-test-preserve")
	writeRuntimeFile(t, dir, "worktree_owner", "true")
	// No dispatched marker → shouldTerminate = false

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: wt-preserve-nodispatch",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	s, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step 1",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := mem.Close(t.Context(), s.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	_ = sendWorkDoneAndCleanup(t.Context(), mem, dir, dir, instance.ID)

	w.Close()
	os.Stderr = origStderr
	out, _ := io.ReadAll(r)
	stderr := string(out)

	if strings.Contains(stderr, "worktree RemoveAgent") || strings.Contains(stderr, "worktree cleanup") || strings.Contains(stderr, "AF_ROLE not set") {
		t.Errorf("worktree block should be skipped when shouldTerminate=false (not dispatched), but stderr shows worktree activity: %q", stderr)
	}
}

// TestDone_WorktreePreserved_DispatchedMailFailed verifies that when dispatched
// but mail fails, shouldAutoTerminate returns false, which gates the worktree block.
func TestDone_WorktreePreserved_DispatchedMailFailed(t *testing.T) {
	// sendWorkDoneMail is a no-op under test (isTestBinary guard), so we can't
	// inject mail failure through sendWorkDoneAndCleanup. Instead, verify the
	// guard logic directly: dispatched + mail error → shouldTerminate=false.
	if shouldAutoTerminate(true, fmt.Errorf("send failed")) {
		t.Fatal("shouldAutoTerminate(dispatched=true, mailErr=error) should return false")
	}

	// TestDone_WorktreePreserved_WhenNotDispatched proves that
	// shouldTerminate=false → worktree block skipped.
	// TestDone_WorktreeCleanedUp_WhenTerminating proves that
	// shouldTerminate=true → worktree block runs.
	// Combined: dispatched + mail failure → shouldTerminate=false → worktree preserved.
}

// TestDone_WorktreeCleanedUp_WhenTerminating verifies that when shouldTerminate
// is true (dispatched + mail ok), the worktree removal block executes.
func TestDone_WorktreeCleanedUp_WhenTerminating(t *testing.T) {
	t.Setenv("AF_ROLE", "test-agent")
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)

	writeRuntimeFile(t, dir, "worktree_id", "wt-test-cleanup")
	writeRuntimeFile(t, dir, "worktree_owner", "true")
	writeRuntimeFile(t, dir, "dispatched", "true")
	// No formula_caller → caller="" → mail skipped → mailErr=nil
	// dispatched=true + mailErr=nil → shouldTerminate=true

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: wt-cleanup-terminate",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	s, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step 1",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := mem.Close(t.Context(), s.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	_ = sendWorkDoneAndCleanup(t.Context(), mem, dir, dir, instance.ID)

	w.Close()
	os.Stderr = origStderr
	out, _ := io.ReadAll(r)
	stderr := string(out)

	if !strings.Contains(stderr, "worktree RemoveAgent") {
		t.Errorf("worktree block should execute when shouldTerminate=true (dispatched), but stderr shows no worktree activity: %q", stderr)
	}
}

// TestSendWorkDoneAndCleanup_WarningIncludesContext verifies the warning message
// includes caller, formula name, and instance ID when sendWorkDoneMail fails.
// Derived from Gherkin: "Warning message includes recipient and formula context on mail failure"
func TestSendWorkDoneAndCleanup_WarningIncludesContext(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755)
	writeRuntimeFile(t, dir, "formula_caller", "supervisor")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: context-warning-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}

	s, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step 1",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := mem.Close(t.Context(), s.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	origSendWorkDoneMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error {
		return fmt.Errorf("mail send to %s failed: exit status 1\nsubprocess stderr: unknown recipient", caller)
	}
	defer func() { sendWorkDoneMail = origSendWorkDoneMail }()

	origStderr := os.Stderr
	r, w, _ := os.Pipe()
	os.Stderr = w

	_ = sendWorkDoneAndCleanup(t.Context(), mem, dir, dir, instance.ID)

	w.Close()
	os.Stderr = origStderr

	out, _ := io.ReadAll(r)
	stderr := string(out)

	if !strings.Contains(stderr, "supervisor") {
		t.Errorf("warning should include caller 'supervisor', got: %q", stderr)
	}
	if !strings.Contains(stderr, instance.ID) {
		t.Errorf("warning should include instance ID %q, got: %q", instance.ID, stderr)
	}
	if !strings.Contains(stderr, "context-warning-test") {
		t.Errorf("warning should include formula name, got: %q", stderr)
	}
}

// TestSendWorkDoneMail_SeamReturnsNilUnderTest verifies that the default
// sendWorkDoneMail seam returns nil under go test (isTestBinary guard).
// Derived from Gherkin: "sendWorkDoneMail returns nil on subprocess success"
func TestSendWorkDoneMail_SeamReturnsNilUnderTest(t *testing.T) {
	err := sendWorkDoneMail("test-caller", "inst-1", "formula-1", 3)
	if err != nil {
		t.Errorf("sendWorkDoneMail should return nil under go test, got: %v", err)
	}
}

func TestDone_WritesLastClosedStep(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:       "Formula: last-closed-test",
		Description: "test formula",
		Type:        issuestore.TypeEpic,
		Labels:      []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	step1, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:       "Step: verify write",
		Description: "step description for test",
		Parent:      instance.ID,
		Type:        issuestore.TypeTask,
		Labels:      []string{"formula-step"},
		Assignee:    "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step1: %v", err)
	}
	step2, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step: second",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step2: %v", err)
	}
	if err := mem.DepAdd(t.Context(), step2.ID, step1.ID); err != nil {
		t.Fatalf("dep add: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	writeRuntimeFile(t, workDir, "step_primed", step1.ID)

	if err := runDoneCore(t.Context(), workDir, false, ""); err != nil {
		t.Fatalf("runDoneCore: %v", err)
	}

	lcsPath := filepath.Join(workDir, ".runtime", "last_closed_step")
	data, err := os.ReadFile(lcsPath)
	if err != nil {
		t.Fatalf("last_closed_step not written: %v", err)
	}

	var record struct {
		ID          string `json:"id"`
		Title       string `json:"title"`
		Description string `json:"description"`
		ClosedAt    string `json:"closed_at"`
		Formula     string `json:"formula"`
	}
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("invalid JSON in last_closed_step: %v", err)
	}
	if record.ID != step1.ID {
		t.Errorf("last_closed_step.id = %q, want %q", record.ID, step1.ID)
	}
	if record.Title != "Step: verify write" {
		t.Errorf("last_closed_step.title = %q, want %q", record.Title, "Step: verify write")
	}
	if record.Description != "step description for test" {
		t.Errorf("last_closed_step.description = %q, want %q", record.Description, "step description for test")
	}
	if record.ClosedAt == "" {
		t.Error("last_closed_step.closed_at should not be empty")
	}
	if record.Formula == "" {
		t.Error("last_closed_step.formula should not be empty")
	}
}

func TestDone_RefusesWithoutPrime(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: refuse-no-prime",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	step, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step: should not close",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	// Do NOT write step_primed

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("expected error when step_primed is missing")
	}
	if !strings.Contains(err.Error(), "step not primed") {
		t.Errorf("error should contain 'step not primed', got: %v", err)
	}

	// Verify step was NOT closed
	s, err := mem.Get(t.Context(), step.ID)
	if err != nil {
		t.Fatalf("get step: %v", err)
	}
	if s.Status.IsTerminal() {
		t.Error("step should NOT be terminal when prime check fails")
	}
}

func TestDone_RefusesWithMismatchedPrime(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: mismatch-prime",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	_, err = mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step: should not close",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	writeRuntimeFile(t, workDir, "step_primed", "wrong-step-id")

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("expected error when step_primed has wrong ID")
	}
	if !strings.Contains(err.Error(), "step primed for") {
		t.Errorf("error should contain 'step primed for', got: %v", err)
	}
}

func TestDone_SucceedsWithPrime(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: success-prime",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	step, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step: should close",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	writeRuntimeFile(t, workDir, "step_primed", step.ID)

	if err := runDoneCore(t.Context(), workDir, false, ""); err != nil {
		t.Fatalf("runDoneCore should succeed with correct prime, got: %v", err)
	}

	s, err := mem.Get(t.Context(), step.ID)
	if err != nil {
		t.Fatalf("get step: %v", err)
	}
	if !s.Status.IsTerminal() {
		t.Errorf("step should be terminal after successful close, got %s", s.Status)
	}
}

func TestDone_SkipRequiresPrime(t *testing.T) {
	if doneCmd.Flags().Lookup("skip") == nil {
		t.Fatal("done command should have --skip flag")
	}

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: skip-prime",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	_, err = mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step: skip test",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	origDoneSkip := doneSkip
	doneSkip = "reason"
	defer func() { doneSkip = origDoneSkip }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	// Do NOT write step_primed

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("expected error: --skip should NOT bypass prime check")
	}
	if !strings.Contains(err.Error(), "step not primed") {
		t.Errorf("error should contain 'step not primed', got: %v", err)
	}
}

func TestDone_VelocityEscalation(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: velocity-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed instance: %v", err)
	}
	step, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:    "Step: velocity check",
		Parent:   instance.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	writeRuntimeFile(t, workDir, "step_primed", step.ID)

	now := time.Now().UTC()
	velocityJSON := fmt.Sprintf(`{
  "closes": [
    {"step_id": "s1", "was_primed": false, "closed_at": %q},
    {"step_id": "s2", "was_primed": false, "closed_at": %q},
    {"step_id": "s3", "was_primed": false, "closed_at": %q}
  ]
}`, now.Add(-5*time.Second).Format(time.RFC3339),
		now.Add(-3*time.Second).Format(time.RFC3339),
		now.Add(-1*time.Second).Format(time.RFC3339))

	writeRuntimeFile(t, workDir, "done_velocity", velocityJSON)

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("expected velocity escalation error")
	}
	if !strings.Contains(err.Error(), "velocity escalation") {
		t.Errorf("error should contain 'velocity escalation', got: %v", err)
	}
}

func TestDone_CleanupRemovesNewFiles(t *testing.T) {
	dir := t.TempDir()

	allFiles := []string{
		"hooked_formula", "formula_caller", "dispatched",
		"last_closed_step", "step_primed", "done_velocity",
	}
	for _, name := range allFiles {
		writeRuntimeFile(t, dir, name, "test-value")
	}
	writeRuntimeFile(t, dir, "worktree_id", "wt-preserve")
	writeRuntimeFile(t, dir, "worktree_owner", "true")

	cleanupRuntimeArtifacts(dir)

	for _, name := range allFiles {
		if _, err := os.Stat(filepath.Join(dir, ".runtime", name)); !os.IsNotExist(err) {
			t.Errorf("%s should be removed after cleanup", name)
		}
	}

	for _, name := range []string{"worktree_id", "worktree_owner"} {
		if _, err := os.Stat(filepath.Join(dir, ".runtime", name)); err != nil {
			t.Errorf("%s should be preserved after cleanup, got error: %v", name, err)
		}
	}
}

func TestDone_FormulaCompletionGuard_Respawn(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: guard-respawn-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
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
	if err := mem.Close(t.Context(), step.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	var escalationCalled bool
	var escalationRecipient, escalationInstanceID, escalationFormulaName, escalationReason string
	origSendEscalation := sendEscalationMail
	sendEscalationMail = func(recipient, instanceID, formulaName, reason string) error {
		escalationCalled = true
		escalationRecipient = recipient
		escalationInstanceID = instanceID
		escalationFormulaName = formulaName
		escalationReason = reason
		return nil
	}
	defer func() { sendEscalationMail = origSendEscalation }()

	var workDoneMailCalled bool
	origSendWorkDone := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error {
		workDoneMailCalled = true
		return nil
	}
	defer func() { sendWorkDoneMail = origSendWorkDone }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	writeRuntimeFile(t, workDir, "formula_caller", "test-dispatcher")

	velocityJSON := `{
  "closes": [
    {"step_id": "s1", "was_primed": false, "closed_at": "2026-05-18T00:00:01Z"},
    {"step_id": "s2", "was_primed": false, "closed_at": "2026-05-18T00:00:02Z"},
    {"step_id": "s3", "was_primed": false, "closed_at": "2026-05-18T00:00:03Z"}
  ]
}`
	writeRuntimeFile(t, workDir, "done_velocity", velocityJSON)

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("expected error from formula completion guard, got nil")
	}
	if !strings.Contains(err.Error(), "formula completion guard triggered") {
		t.Errorf("error should contain 'formula completion guard triggered', got: %v", err)
	}

	if !escalationCalled {
		t.Error("sendEscalationMail should have been called")
	}
	if escalationRecipient != "test-dispatcher" {
		t.Errorf("escalation recipient = %q, want 'test-dispatcher'", escalationRecipient)
	}
	if escalationInstanceID != instance.ID {
		t.Errorf("escalation instanceID = %q, want %q", escalationInstanceID, instance.ID)
	}
	if escalationFormulaName != "Formula: guard-respawn-test" {
		t.Errorf("escalation formulaName = %q, want 'Formula: guard-respawn-test'", escalationFormulaName)
	}
	if escalationReason == "" {
		t.Error("escalation reason should not be empty")
	}

	if workDoneMailCalled {
		t.Error("sendWorkDoneMail should NOT have been called when guard triggers")
	}
}

func TestDone_FormulaCompletionGuard_NoCaller_Fallback(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: guard-no-caller-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
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
	if err := mem.Close(t.Context(), step.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	var escalationCalled bool
	var escalationRecipient string
	origSendEscalation := sendEscalationMail
	sendEscalationMail = func(recipient, instanceID, formulaName, reason string) error {
		escalationCalled = true
		escalationRecipient = recipient
		return nil
	}
	defer func() { sendEscalationMail = origSendEscalation }()

	origSendWorkDone := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error {
		return nil
	}
	defer func() { sendWorkDoneMail = origSendWorkDone }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)

	velocityJSON := `{
  "closes": [
    {"step_id": "s1", "was_primed": false, "closed_at": "2026-05-18T00:00:01Z"},
    {"step_id": "s2", "was_primed": false, "closed_at": "2026-05-18T00:00:02Z"},
    {"step_id": "s3", "was_primed": false, "closed_at": "2026-05-18T00:00:03Z"}
  ]
}`
	writeRuntimeFile(t, workDir, "done_velocity", velocityJSON)

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("expected error from formula completion guard, got nil")
	}
	if !strings.Contains(err.Error(), "formula completion guard triggered") {
		t.Errorf("error should contain 'formula completion guard triggered', got: %v", err)
	}
	if !escalationCalled {
		t.Error("sendEscalationMail should have been called")
	}
	if escalationRecipient != "supervisor" {
		t.Errorf("escalation recipient = %q, want 'supervisor' (fallback when no caller)", escalationRecipient)
	}
}

func TestDone_FormulaCompletionGuard_CliCaller_Fallback(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: guard-cli-caller-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
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
	if err := mem.Close(t.Context(), step.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	var escalationCalled bool
	var escalationRecipient string
	origSendEscalation := sendEscalationMail
	sendEscalationMail = func(recipient, instanceID, formulaName, reason string) error {
		escalationCalled = true
		escalationRecipient = recipient
		return nil
	}
	defer func() { sendEscalationMail = origSendEscalation }()

	origSendWorkDone := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error {
		return nil
	}
	defer func() { sendWorkDoneMail = origSendWorkDone }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	writeRuntimeFile(t, workDir, "formula_caller", "@cli")

	velocityJSON := `{
  "closes": [
    {"step_id": "s1", "was_primed": false, "closed_at": "2026-05-18T00:00:01Z"},
    {"step_id": "s2", "was_primed": false, "closed_at": "2026-05-18T00:00:02Z"},
    {"step_id": "s3", "was_primed": false, "closed_at": "2026-05-18T00:00:03Z"}
  ]
}`
	writeRuntimeFile(t, workDir, "done_velocity", velocityJSON)

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("expected error from formula completion guard, got nil")
	}
	if !strings.Contains(err.Error(), "formula completion guard triggered") {
		t.Errorf("error should contain 'formula completion guard triggered', got: %v", err)
	}
	if !escalationCalled {
		t.Error("sendEscalationMail should have been called")
	}
	if escalationRecipient != "supervisor" {
		t.Errorf("escalation recipient = %q, want 'supervisor' (fallback when caller is @cli)", escalationRecipient)
	}
}

func TestDone_FormulaCompletionGuard_MailFailure_NoRespawn(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: guard-mail-failure-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
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
	if err := mem.Close(t.Context(), step.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	origSendEscalation := sendEscalationMail
	sendEscalationMail = func(recipient, instanceID, formulaName, reason string) error {
		return fmt.Errorf("simulated mail failure")
	}
	defer func() { sendEscalationMail = origSendEscalation }()

	origSendWorkDone := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error {
		return nil
	}
	defer func() { sendWorkDoneMail = origSendWorkDone }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)
	writeRuntimeFile(t, workDir, "formula_caller", "test-dispatcher")

	velocityJSON := `{
  "closes": [
    {"step_id": "s1", "was_primed": false, "closed_at": "2026-05-18T00:00:01Z"},
    {"step_id": "s2", "was_primed": false, "closed_at": "2026-05-18T00:00:02Z"},
    {"step_id": "s3", "was_primed": false, "closed_at": "2026-05-18T00:00:03Z"}
  ]
}`
	writeRuntimeFile(t, workDir, "done_velocity", velocityJSON)

	err = runDoneCore(t.Context(), workDir, false, "")
	if err == nil {
		t.Fatal("expected error from formula completion guard, got nil")
	}
	if !strings.Contains(err.Error(), "formula completion guard triggered") {
		t.Errorf("error should contain 'formula completion guard triggered', got: %v", err)
	}
	if !strings.Contains(err.Error(), "escalation mail failed") {
		t.Errorf("error should contain 'escalation mail failed' (skip-respawn path), got: %v", err)
	}
}

func TestDone_FormulaCompletionGuard_NormalCompletion(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	instance, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title:  "Formula: guard-normal-test",
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
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
	if err := mem.Close(t.Context(), step.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}

	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", instance.ID)

	velocityJSON := `{
  "closes": [
    {"step_id": "s1", "was_primed": true, "closed_at": "2026-05-18T00:00:01Z"},
    {"step_id": "s2", "was_primed": true, "closed_at": "2026-05-18T00:00:02Z"},
    {"step_id": "s3", "was_primed": true, "closed_at": "2026-05-18T00:00:03Z"}
  ]
}`
	writeRuntimeFile(t, workDir, "done_velocity", velocityJSON)

	if err := runDoneCore(t.Context(), workDir, false, ""); err != nil {
		t.Fatalf("runDoneCore should succeed with all-primed velocity, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(workDir, ".runtime", "hooked_formula")); !os.IsNotExist(err) {
		t.Error("hooked_formula should have been cleaned up after normal completion")
	}
}

// setupTestFactoryForDone creates a minimal factory structure for done tests.
func setupTestFactoryForDone(t *testing.T, agentName string) string {
	t.Helper()
	dir := setupTestFactoryForPrime(t) // reuse prime's setup
	os.MkdirAll(config.StoreDir(dir), 0o755)
	return dir
}

func TestDoneVelocity_EvidenceTypeRecorded(t *testing.T) {
	dir := t.TempDir()

	if err := recordDoneTimestamp(dir, "step-1", true, "full_output"); err != nil {
		t.Fatalf("recordDoneTimestamp (primed): %v", err)
	}
	if err := recordDoneTimestamp(dir, "step-2", false, ""); err != nil {
		t.Fatalf("recordDoneTimestamp (unprimed): %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".runtime", "done_velocity"))
	if err != nil {
		t.Fatalf("read done_velocity: %v", err)
	}

	var record doneVelocityRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(record.Closes) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(record.Closes))
	}

	if record.Closes[0].EvidenceType != "full_output" {
		t.Errorf("primed entry: expected EvidenceType=%q, got %q", "full_output", record.Closes[0].EvidenceType)
	}
	if record.Closes[1].EvidenceType != "" {
		t.Errorf("unprimed entry: expected EvidenceType=%q, got %q", "", record.Closes[1].EvidenceType)
	}

	rawJSON := string(data)
	if !strings.Contains(rawJSON, `"evidence_type"`) {
		t.Error("primed entry JSON should contain evidence_type key")
	}

	var wrapper struct {
		Closes []json.RawMessage `json:"closes"`
	}
	if err := json.Unmarshal(data, &wrapper); err != nil {
		t.Fatalf("unmarshal raw: %v", err)
	}
	unprimedRaw := string(wrapper.Closes[1])
	if strings.Contains(unprimedRaw, `"evidence_type"`) {
		t.Error("unprimed entry JSON should NOT contain evidence_type key (omitempty)")
	}
}

func TestDoneVelocity_BackwardCompat(t *testing.T) {
	dir := t.TempDir()

	now := time.Now().UTC()
	oldFormatJSON := fmt.Sprintf(`{
  "closes": [
    {"step_id": "old-1", "was_primed": false, "closed_at": %q},
    {"step_id": "old-2", "was_primed": true, "closed_at": %q}
  ]
}`, now.Add(-10*time.Second).Format(time.RFC3339),
		now.Add(-5*time.Second).Format(time.RFC3339))

	writeRuntimeFile(t, dir, "done_velocity", oldFormatJSON)

	if err := recordDoneTimestamp(dir, "new-1", true, "full_output"); err != nil {
		t.Fatalf("recordDoneTimestamp: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".runtime", "done_velocity"))
	if err != nil {
		t.Fatalf("read done_velocity: %v", err)
	}

	var record doneVelocityRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if len(record.Closes) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(record.Closes))
	}

	if record.Closes[0].EvidenceType != "" {
		t.Errorf("old entry 0: expected EvidenceType=%q, got %q", "", record.Closes[0].EvidenceType)
	}
	if record.Closes[1].EvidenceType != "" {
		t.Errorf("old entry 1: expected EvidenceType=%q, got %q", "", record.Closes[1].EvidenceType)
	}
	if record.Closes[2].EvidenceType != "full_output" {
		t.Errorf("new entry: expected EvidenceType=%q, got %q", "full_output", record.Closes[2].EvidenceType)
	}

	if err := checkDoneVelocity(dir); err != nil {
		t.Errorf("checkDoneVelocity should not error with mixed entries: %v", err)
	}
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
