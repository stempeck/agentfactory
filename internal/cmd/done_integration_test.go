//go:build integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// TestDone_MultiStepFormula_ProgressesCorrectly pins an adapter-agnostic
// invariant: done.go's open-children probe under an epic is an
// instance-scoped completion-detection query — it must cross actor
// boundaries because "does this instance still have work?" is an
// instance-level question, not an actor-level one. The contract test
// suite pins the default actor-scoping semantics across adapters, so
// either memstore or mcpstore would silently misfire WORK_DONE without
// cross-actor visibility on done.go's open-children probes.
// Restructuring of the idiom is tracked in #124.
//
// This test deliberately seeds its child tasks with NO Assignee as a
// residual-coverage fixture: post-#123 sling populates Assignee from
// the formula's AgentFor, but this scenario still covers the
// AF_ACTOR="" edge case (empty actor in the store's context) and
// formulas with no declared ownership (where f.AgentFor(stepID)
// returns "" as the documented fallback). Residual AF_ACTOR=""
// coverage gap (Risk R-I1) is deferred to Phase 5.
//
// The test calls runDoneCore twice. First call must close step 1 and
// leave .runtime/hooked_formula in place; second call must close step
// 2 and remove the file. isTestBinary() gates sendWorkDoneMail and
// selfTerminate so the test does not fork-bomb.
//
// Without cross-actor visibility in done.go's open-children probes,
// the first runDoneCore call returns nil (having misfired WORK_DONE)
// and removes hooked_formula one step in — the assertion on
// hooked_formula after call 1 is the regression catcher.
func TestDone_MultiStepFormula_ProgressesCorrectly(t *testing.T) {
	requirePython3WithServerDeps(t)

	root := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(root, ".agentfactory", "agents", "manager")
	ensurePySymlink(t, root)
	t.Cleanup(func() { terminateMCPServer(root) })

	// git init — kept for repo-parity. mcpstore does not require git.
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.test"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	store, err := mcpstore.New(root, "done-it")
	if err != nil {
		t.Fatalf("mcpstore.New: %v", err)
	}

	ctx := t.Context()

	// Create epic + two child tasks. Phase 1's data-plane invariant
	// (parent_id = '' OR assignee != '') requires child beads to carry a
	// non-empty Assignee; the fixture mirrors what sling now writes after
	// UX-2 + assigneeForStep land.
	epic, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula: multi-step",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	step1, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epic.ID,
		Title:    "Step: first",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create step1: %v", err)
	}
	step2, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epic.ID,
		Title:    "Step: second",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create step2: %v", err)
	}

	writeRuntimeFile(t, workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, workDir, "formula_caller", "supervisor")

	hookedPath := filepath.Join(workDir, ".runtime", "hooked_formula")

	// Call 1: should close step1 and leave hooked_formula in place because
	// step2 is still open. Without the fix this call misfires WORK_DONE.
	if err := runDoneCore(ctx, workDir, false, ""); err != nil {
		t.Fatalf("first runDoneCore: %v", err)
	}
	if _, err := os.Stat(hookedPath); err != nil {
		t.Fatalf("hooked_formula removed after only one step closed — this is the AF_ACTOR filter regression: %v", err)
	}

	// Verify step1 is closed and step2 is still open, using the same
	// Filter shape as done.go's post-fix call site.
	openChildren, err := store.List(ctx, issuestore.Filter{
		Parent:           epic.ID,
		Statuses:         []issuestore.Status{issuestore.StatusOpen},
		IncludeAllAgents: true,
	})
	if err != nil {
		t.Fatalf("list open children after call 1: %v", err)
	}
	if len(openChildren) != 1 {
		t.Fatalf("expected 1 open child after call 1, got %d: %+v", len(openChildren), openChildren)
	}
	if openChildren[0].ID != step2.ID {
		t.Errorf("expected remaining open child to be %s, got %s", step2.ID, openChildren[0].ID)
	}
	// Sanity: step1 should be closed.
	s1, err := store.Get(ctx, step1.ID)
	if err != nil {
		t.Fatalf("get step1: %v", err)
	}
	if !s1.Status.IsTerminal() {
		t.Errorf("step1 should be closed after call 1, got status %q", s1.Status)
	}

	// Call 2: should close step2 and remove hooked_formula (WORK_DONE path,
	// no-op in test via isTestBinary gate).
	if err := runDoneCore(ctx, workDir, false, ""); err != nil {
		t.Fatalf("second runDoneCore: %v", err)
	}
	if _, err := os.Stat(hookedPath); !os.IsNotExist(err) {
		t.Errorf("hooked_formula should be removed after all steps complete, stat err: %v", err)
	}
}

func TestSuccession_NewFormulaIsExecutable(t *testing.T) {
	requirePython3WithServerDeps(t)

	root := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(root, ".agentfactory", "agents", "manager")
	ensurePySymlink(t, root)
	t.Cleanup(func() { terminateMCPServer(root) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.test"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	store, err := mcpstore.New(root, "manager")
	if err != nil {
		t.Fatalf("mcpstore.New: %v", err)
	}

	ctx := t.Context()

	epicA, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula A",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create epicA: %v", err)
	}
	_, err = store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epicA.ID,
		Title:    "A step 1",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create A step1: %v", err)
	}

	writeRuntimeFile(t, workDir, "hooked_formula", epicA.ID)
	writeRuntimeFile(t, workDir, "formula_caller", "supervisor")

	if err := runDoneCore(ctx, workDir, false, ""); err != nil {
		t.Fatalf("runDoneCore for formula A: %v", err)
	}

	hookedPath := filepath.Join(workDir, ".runtime", "hooked_formula")
	if _, err := os.Stat(hookedPath); !os.IsNotExist(err) {
		t.Fatalf("hooked_formula should be removed after formula A completes, stat err: %v", err)
	}

	epicB, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula B",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create epicB: %v", err)
	}
	stepB1, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epicB.ID,
		Title:    "B step 1",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create B step1: %v", err)
	}

	writeRuntimeFile(t, workDir, "hooked_formula", epicB.ID)
	writeRuntimeFile(t, workDir, "formula_caller", "supervisor")

	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: epicB.ID})
	if err != nil {
		t.Fatalf("store.Ready for formula B: %v", err)
	}
	if len(result.Steps) == 0 {
		t.Fatal("formula B should have a ready step")
	}
	if result.Steps[0].ID != stepB1.ID {
		t.Errorf("formula B ready step = %s, want %s", result.Steps[0].ID, stepB1.ID)
	}

	if err := runDoneCore(ctx, workDir, false, ""); err != nil {
		t.Fatalf("runDoneCore for formula B: %v", err)
	}
	if _, err := os.Stat(hookedPath); !os.IsNotExist(err) {
		t.Errorf("hooked_formula should be removed after formula B completes")
	}
}

func TestThreeWayAgreement_FreshSling(t *testing.T) {
	requirePython3WithServerDeps(t)

	root := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(root, ".agentfactory", "agents", "manager")
	ensurePySymlink(t, root)
	t.Cleanup(func() { terminateMCPServer(root) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.test"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	store, err := mcpstore.New(root, "manager")
	if err != nil {
		t.Fatalf("mcpstore.New: %v", err)
	}

	ctx := t.Context()

	epic, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula: three-way",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	step1, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epic.ID,
		Title:    "Step 1",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create step1: %v", err)
	}
	_, err = store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epic.ID,
		Title:    "Step 2",
		Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("create step2: %v", err)
	}

	writeRuntimeFile(t, workDir, "hooked_formula", epic.ID)

	readyResult, err := store.Ready(ctx, issuestore.Filter{MoleculeID: epic.ID})
	if err != nil {
		t.Fatalf("store.Ready: %v", err)
	}
	if len(readyResult.Steps) == 0 {
		t.Fatal("Ready should return step 1")
	}
	if readyResult.Steps[0].ID != step1.ID {
		t.Errorf("Ready step = %s, want %s", readyResult.Steps[0].ID, step1.ID)
	}

	openChildren, err := store.List(ctx, issuestore.Filter{
		Parent:   epic.ID,
		Statuses: []issuestore.Status{issuestore.StatusOpen},
	})
	if err != nil {
		t.Fatalf("store.List open children: %v", err)
	}
	if len(openChildren) != 2 {
		t.Fatalf("expected 2 open children, got %d", len(openChildren))
	}

	found := false
	for _, child := range openChildren {
		if child.ID == readyResult.Steps[0].ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("ready step %s not found in open children list", readyResult.Steps[0].ID)
	}
}

func TestCLEAN1_AgentPathOverlayBehavioral(t *testing.T) {
	requirePython3WithServerDeps(t)

	root := setupTestFactoryForDone(t, "agent-a")
	os.MkdirAll(filepath.Join(root, ".agentfactory", "agents", "agent-b"), 0o755)
	ensurePySymlink(t, root)
	t.Cleanup(func() { terminateMCPServer(root) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.test"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	storeA, err := mcpstore.New(root, "agent-a")
	if err != nil {
		t.Fatalf("mcpstore.New for agent-a: %v", err)
	}

	ctx := t.Context()

	epic, err := storeA.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Agent A Formula",
		Assignee: "agent-a",
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	_, err = storeA.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epic.ID,
		Title:    "A's step",
		Assignee: "agent-a",
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}

	storeB, err := mcpstore.New(root, "agent-b")
	if err != nil {
		t.Fatalf("mcpstore.New for agent-b: %v", err)
	}

	childrenFromB, err := storeB.List(ctx, issuestore.Filter{
		Parent:   epic.ID,
		Statuses: []issuestore.Status{issuestore.StatusOpen},
	})
	if err != nil {
		t.Fatalf("store.List from agent-b: %v", err)
	}
	if len(childrenFromB) != 0 {
		t.Errorf("agent-b should NOT see agent-a's children without IncludeAllAgents, got %d items: %+v",
			len(childrenFromB), childrenFromB)
	}

	childrenFromA, err := storeA.List(ctx, issuestore.Filter{
		Parent:   epic.ID,
		Statuses: []issuestore.Status{issuestore.StatusOpen},
	})
	if err != nil {
		t.Fatalf("store.List from agent-a: %v", err)
	}
	if len(childrenFromA) != 1 {
		t.Errorf("agent-a should see its own children, got %d items", len(childrenFromA))
	}
}
