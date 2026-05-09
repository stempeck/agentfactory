package cmd

import (
	"errors"
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

func TestReadHookedFormulaID_Exists(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("  bd-abc-123 \n"), 0o644)

	id := readHookedFormulaID(dir)
	if id != "bd-abc-123" {
		t.Errorf("expected bd-abc-123, got %q", id)
	}
}

func TestReadHookedFormulaID_Missing(t *testing.T) {
	dir := t.TempDir()
	id := readHookedFormulaID(dir)
	if id != "" {
		t.Errorf("expected empty string for missing file, got %q", id)
	}
}

func TestReadHookedFormulaID_Empty(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("  \n"), 0o644)

	id := readHookedFormulaID(dir)
	if id != "" {
		t.Errorf("expected empty string for empty file, got %q", id)
	}
}

func TestOutputFormulaContext_NoFormula(t *testing.T) {
	dir := t.TempDir()
	var buf strings.Builder
	// No .runtime/hooked_formula → no output
	outputFormulaContext(t.Context(), &buf, dir)
	if buf.Len() != 0 {
		t.Errorf("expected no output when no formula active, got %q", buf.String())
	}
}

func TestOutputCheckpointContext_WithCheckpoint(t *testing.T) {
	dir := t.TempDir()

	cp := &checkpoint.Checkpoint{
		FormulaID:   "f-test-123",
		CurrentStep: "step-2",
		StepTitle:   "Implement the widget",
		Branch:      "feature/widgets",
		Timestamp:   time.Now().Add(-30 * time.Minute),
		SessionID:   "test-sess",
	}
	if err := checkpoint.Write(dir, cp); err != nil {
		t.Fatalf("checkpoint.Write: %v", err)
	}

	var buf strings.Builder
	outputCheckpointContext(&buf, dir)
	output := buf.String()

	if !strings.Contains(output, "Previous Session Checkpoint") {
		t.Error("output should contain 'Previous Session Checkpoint'")
	}
	if !strings.Contains(output, "Implement the widget") {
		t.Error("output should contain step title")
	}
	if !strings.Contains(output, "f-test-123") {
		t.Error("output should contain formula ID")
	}
	if !strings.Contains(output, "step-2") {
		t.Error("output should contain step ID")
	}
	if !strings.Contains(output, "feature/widgets") {
		t.Error("output should contain branch")
	}
}

func TestOutputCheckpointContext_NoCheckpoint(t *testing.T) {
	dir := t.TempDir()
	var buf strings.Builder
	outputCheckpointContext(&buf, dir)
	if buf.Len() != 0 {
		t.Errorf("expected no output when no checkpoint, got %q", buf.String())
	}
}

func TestOutputCheckpointContext_StaleCheckpoint(t *testing.T) {
	dir := t.TempDir()

	cp := &checkpoint.Checkpoint{
		FormulaID: "f-stale",
		Timestamp: time.Now().Add(-25 * time.Hour), // older than 24h
		SessionID: "old-sess",
	}
	if err := checkpoint.Write(dir, cp); err != nil {
		t.Fatalf("checkpoint.Write: %v", err)
	}

	// Verify checkpoint file exists
	cpPath := checkpoint.Path(dir)
	if _, err := os.Stat(cpPath); err != nil {
		t.Fatalf("checkpoint file should exist: %v", err)
	}

	var buf strings.Builder
	outputCheckpointContext(&buf, dir)

	// Should produce no output for stale checkpoint
	if buf.Len() != 0 {
		t.Errorf("expected no output for stale checkpoint, got %q", buf.String())
	}

	// Stale checkpoint should be removed
	if _, err := os.Stat(cpPath); !os.IsNotExist(err) {
		t.Error("stale checkpoint file should have been removed")
	}
}

func TestIsGateStep_Heuristic(t *testing.T) {
	// Should detect gate from description keywords.
	// Uses an empty memstore so the blocker path returns false (fake-id is
	// not seeded → store.Get errors → stepHasOpenBlockers returns false),
	// isolating the heuristic branch.
	tests := []struct {
		desc string
		want bool
	}{
		{"Complete the work, then run af done --phase-complete --gate g1", true},
		{"This step has a gate that enforces review", true},
		{"Gate enforces quality review before proceeding", true},
		{"Gate blocks closure until review is done", true},
		{"Just a normal step description", false},
		{"", false},
	}

	store := memstore.New()
	for _, tt := range tests {
		t.Run(tt.desc, func(t *testing.T) {
			got := isGateStep(t.Context(), store, "fake-id", tt.desc)
			if got != tt.want {
				t.Errorf("isGateStep(%q) = %v, want %v", tt.desc, got, tt.want)
			}
		})
	}
}

func TestPrimeAgent_NoFormula_Unchanged(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	var buf strings.Builder

	// No .runtime/hooked_formula exists → formula context self-guards (no output)
	err := primeAgent(t.Context(), &buf, root, "manager", filepath.Join(root, ".agentfactory", "agents", "manager"))
	if err != nil {
		t.Fatalf("primeAgent failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "role:manager") {
		t.Error("output should contain role:manager")
	}
	if !strings.Contains(output, "Startup Directive") {
		t.Error("output should contain Startup Directive")
	}
	// Should NOT contain formula context (no hooked_formula file)
	if strings.Contains(output, "Formula Workflow") {
		t.Error("output should NOT contain Formula Workflow when no formula is active")
	}
	if strings.Contains(output, "Previous Session Checkpoint") {
		t.Error("output should NOT contain checkpoint context when no checkpoint exists")
	}
}

func TestPrimeAgent_NoFormulaFile_NoFormulaContext(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	// Create store dir
	os.MkdirAll(config.StoreDir(root), 0o755)

	var buf strings.Builder

	// No .runtime/hooked_formula → formula context self-guards (no output)
	err := primeAgent(t.Context(), &buf, root, "manager", filepath.Join(root, ".agentfactory", "agents", "manager"))
	if err != nil {
		t.Fatalf("primeAgent failed: %v", err)
	}

	output := buf.String()
	// Should still have normal prime output
	if !strings.Contains(output, "role:manager") {
		t.Error("output should contain role:manager")
	}
	// Should NOT have formula context (no formula file)
	if strings.Contains(output, "Formula Workflow") {
		t.Error("output should NOT contain Formula Workflow when no formula file exists")
	}
}

func TestPrimeAgent_AutoInjectsFormulaContext(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	// Create .runtime/hooked_formula with a fake instance ID
	runtimeDir := filepath.Join(root, ".agentfactory", "agents", "manager", ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("bd-test-formula-1"), 0o644)
	os.MkdirAll(config.StoreDir(root), 0o755)

	var buf strings.Builder
	err := primeAgent(t.Context(), &buf, root, "manager", filepath.Join(root, ".agentfactory", "agents", "manager"))
	if err != nil {
		t.Fatalf("primeAgent failed: %v", err)
	}

	output := buf.String()
	// Should have normal prime output
	if !strings.Contains(output, "role:manager") {
		t.Error("output should contain role:manager")
	}
	// Should have formula context (hooked_formula exists, even though bd will fail)
	if !strings.Contains(output, "Formula Workflow") {
		t.Error("output should contain Formula Workflow when hooked_formula exists")
	}
	if !strings.Contains(output, "bd-test-formula-1") {
		t.Error("output should contain the formula instance ID")
	}
	// Error-path marker (bd not available in test env)
	if !strings.Contains(output, "step query failed") {
		t.Error("output should contain error-path status when bd is unavailable")
	}
}

func TestPrimeAgent_NoFormulaContext_WhenNoHookedFormula(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	// Create .runtime/ directory but do NOT create hooked_formula file
	runtimeDir := filepath.Join(root, ".agentfactory", "agents", "manager", ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.MkdirAll(config.StoreDir(root), 0o755)

	var buf strings.Builder
	err := primeAgent(t.Context(), &buf, root, "manager", filepath.Join(root, ".agentfactory", "agents", "manager"))
	if err != nil {
		t.Fatalf("primeAgent failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "role:manager") {
		t.Error("output should contain role:manager")
	}
	// .runtime/ exists but hooked_formula does not → no formula context
	if strings.Contains(output, "Formula Workflow") {
		t.Error("output should NOT contain Formula Workflow when hooked_formula is absent")
	}
}

func TestWriteFormulaCheckpoint_NoFormula(t *testing.T) {
	dir := t.TempDir()
	os.MkdirAll(config.StoreDir(dir), 0o755)

	// No .runtime/hooked_formula → should silently return
	writeFormulaCheckpoint(t.Context(), dir)

	cpPath := checkpoint.Path(dir)
	if _, err := os.Stat(cpPath); !os.IsNotExist(err) {
		t.Error("checkpoint should NOT be written when no formula active")
	}
}

func TestPersistFormulaInstanceID(t *testing.T) {
	dir := t.TempDir()
	persistFormulaInstanceID(dir, "bd-test-instance")

	path := filepath.Join(dir, ".runtime", "hooked_formula")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("hooked_formula file not created: %v", err)
	}
	if string(data) != "bd-test-instance" {
		t.Errorf("expected bd-test-instance, got %s", string(data))
	}
}

// TestOutputFormulaContext_PreflightWiring verifies that outputCommandPreflight
// and outputStandingDirective are called from outputFormulaContext. Since the
// function shells out to bd (no mock seam), this test reads the source to
// confirm the wiring exists.
func TestOutputFormulaContext_PreflightWiring(t *testing.T) {
	src, err := os.ReadFile("prime.go")
	if err != nil {
		t.Fatalf("failed to read prime.go: %v", err)
	}
	source := string(src)

	// outputFormulaContext must call outputCommandPreflight
	if !strings.Contains(source, "outputCommandPreflight(out, description)") {
		t.Error("prime.go: outputFormulaContext does not call outputCommandPreflight(out, description)")
	}

	// outputFormulaContext must call outputStandingDirective
	if !strings.Contains(source, "outputStandingDirective(out)") {
		t.Error("prime.go: outputFormulaContext does not call outputStandingDirective(out)")
	}
}

func TestOutputFormulaContext_ListError_NoFalseAllComplete(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	os.MkdirAll(config.StoreDir(root), 0o755)
	workDir := filepath.Join(root, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	ctx := t.Context()

	epic, err := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Test Formula",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	child, err := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epic.ID,
		Title:    "Blocked step",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}
	blocker, err := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "External blocker",
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := mem.DepAdd(ctx, child.ID, blocker.ID); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}

	failStore := &errOnListStore{
		Store:   mem,
		listErr: errors.New("transient MCP server failure"),
	}
	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return failStore, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", epic.ID)

	var buf strings.Builder
	outputFormulaContext(ctx, &buf, workDir)
	output := buf.String()

	if strings.Contains(output, "all_complete") {
		t.Errorf("store.List error must NOT produce all_complete; got:\n%s", output)
	}
	if !strings.Contains(output, "error querying formula state") {
		t.Errorf("output should contain error status; got:\n%s", output)
	}
}

func TestOutputFormulaContext_ListError_NoWrongStepNumber(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	os.MkdirAll(config.StoreDir(root), 0o755)
	workDir := filepath.Join(root, ".agentfactory", "agents", "manager")

	mem := memstore.New()
	ctx := t.Context()

	epic, err := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Test Formula",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	_, err = mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epic.ID,
		Title:    "Ready step",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}

	failStore := &errOnListStore{
		Store:   mem,
		listErr: errors.New("transient MCP server failure"),
	}
	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return failStore, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeRuntimeFile(t, workDir, "hooked_formula", epic.ID)

	var buf strings.Builder
	outputFormulaContext(ctx, &buf, workDir)
	output := buf.String()

	if strings.Contains(output, "Step 2 of 1") {
		t.Errorf("store.List error must NOT produce wrong step number (totalSteps+1); got:\n%s", output)
	}
	if !strings.Contains(output, "error querying open steps") {
		t.Errorf("output should contain error status; got:\n%s", output)
	}
}

func TestStepHasOpenBlockers_Defensive(t *testing.T) {
	// Phase 3 rewrite: stepHasOpenBlockers now calls store.Get per blocker
	// and trusts Status.IsTerminal(), rather than reparsing JSON. No adapter
	// filters BlockedBy by terminal status, so the helper must do that work
	// itself (IMPLREADME Gotcha #D6 is WRONG).
	ctx := t.Context()

	t.Run("no blockers → not a gate", func(t *testing.T) {
		store := memstore.New()
		iss, err := store.Create(ctx, issuestore.CreateParams{Type: issuestore.TypeTask, Title: "step"})
		if err != nil {
			t.Fatalf("create: %v", err)
		}
		if stepHasOpenBlockers(ctx, store, iss.ID) {
			t.Error("unseeded blockers should yield false")
		}
	})

	t.Run("missing step → not a gate (defensive)", func(t *testing.T) {
		store := memstore.New()
		if stepHasOpenBlockers(ctx, store, "does-not-exist") {
			t.Error("unknown step should yield false")
		}
	})

	t.Run("open blocker → gate", func(t *testing.T) {
		store := memstore.New()
		blocker, err := store.Create(ctx, issuestore.CreateParams{Type: issuestore.TypeTask, Title: "blocker"})
		if err != nil {
			t.Fatalf("create blocker: %v", err)
		}
		step, err := store.Create(ctx, issuestore.CreateParams{Type: issuestore.TypeTask, Title: "step"})
		if err != nil {
			t.Fatalf("create step: %v", err)
		}
		if err := store.DepAdd(ctx, step.ID, blocker.ID); err != nil {
			t.Fatalf("dep add: %v", err)
		}
		if !stepHasOpenBlockers(ctx, store, step.ID) {
			t.Error("open blocker should yield true")
		}
	})

	t.Run("closed blocker → not a gate", func(t *testing.T) {
		store := memstore.New()
		blocker, err := store.Create(ctx, issuestore.CreateParams{Type: issuestore.TypeTask, Title: "blocker"})
		if err != nil {
			t.Fatalf("create blocker: %v", err)
		}
		step, err := store.Create(ctx, issuestore.CreateParams{Type: issuestore.TypeTask, Title: "step"})
		if err != nil {
			t.Fatalf("create step: %v", err)
		}
		if err := store.DepAdd(ctx, step.ID, blocker.ID); err != nil {
			t.Fatalf("dep add: %v", err)
		}
		if err := store.Close(ctx, blocker.ID, ""); err != nil {
			t.Fatalf("close blocker: %v", err)
		}
		if stepHasOpenBlockers(ctx, store, step.ID) {
			t.Error("terminal blocker should yield false — IMPLREADME Gotcha #D6 is wrong")
		}
	})

	t.Run("mixed blockers — one open → gate", func(t *testing.T) {
		store := memstore.New()
		b1, _ := store.Create(ctx, issuestore.CreateParams{Type: issuestore.TypeTask, Title: "b1"})
		b2, _ := store.Create(ctx, issuestore.CreateParams{Type: issuestore.TypeTask, Title: "b2"})
		step, _ := store.Create(ctx, issuestore.CreateParams{Type: issuestore.TypeTask, Title: "step"})
		if err := store.DepAdd(ctx, step.ID, b1.ID); err != nil {
			t.Fatalf("dep add b1: %v", err)
		}
		if err := store.DepAdd(ctx, step.ID, b2.ID); err != nil {
			t.Fatalf("dep add b2: %v", err)
		}
		if err := store.Close(ctx, b1.ID, ""); err != nil {
			t.Fatalf("close b1: %v", err)
		}
		if !stepHasOpenBlockers(ctx, store, step.ID) {
			t.Error("one open blocker should yield true")
		}
	})
}
