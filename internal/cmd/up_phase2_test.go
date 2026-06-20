package cmd

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// Phase 2 (issue #392) K4 tests: store-backed reconstruction of a lost
// .runtime/hooked_formula pointer at `af up`. The unit of test is the
// extracted helper reconstructHookedFormula, which is what runUp invokes
// after the dispatched-marker removal and before mgr.Start().

// newInstanceEpic seeds an open `formula-instance` epic assigned to agent.
func newInstanceEpic(t *testing.T, mem *memstore.Store, agent string) issuestore.Issue {
	t.Helper()
	epic, err := mem.Create(context.Background(), issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula: phase2",
		Labels:   []string{"formula-instance"},
		Assignee: agent,
	})
	if err != nil {
		t.Fatalf("seed instance epic: %v", err)
	}
	return epic
}

// newChildStep seeds a child step under parent (Assignee required by the
// memstore data-plane invariant for parent-scoped beads).
func newChildStep(t *testing.T, mem *memstore.Store, parent, agent string) issuestore.Issue {
	t.Helper()
	step, err := mem.Create(context.Background(), issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   parent,
		Title:    "Step: work",
		Labels:   []string{"formula-step"},
		Assignee: agent,
	})
	if err != nil {
		t.Fatalf("seed child step: %v", err)
	}
	return step
}

func readPointer(t *testing.T, agentDir string) string {
	t.Helper()
	return readHookedFormulaID(agentDir)
}

// installMemStoreWithActor mirrors installMemStore but configures the store's
// actor overlay via memstore.NewWithActor, so the cross-actor `af up` path is
// exercised — the launching process's AF_ACTOR (the store actor) differs from
// the target agent. installMemStore uses memstore.New() (empty actor), which
// disables the overlay and therefore cannot reproduce the K4 child-query bug
// (#392 / PR #394). The newIssueStore seam's actor arg is ignored here because
// the store is pre-bound with the actor, faithful to production where the same
// AF_ACTOR string flows into NewWithActor via helpers.go.
func installMemStoreWithActor(t *testing.T, actor string) *memstore.Store {
	t.Helper()
	store := memstore.NewWithActor(actor)
	orig := newIssueStore
	newIssueStore = func(wd, _ string) (issuestore.Store, error) {
		return store, nil
	}
	t.Cleanup(func() { newIssueStore = orig })
	return store
}

// TestUp_ReconstructsHookedFormula_FromStore: with no pointer and exactly one
// open in-flight epic (>=1 open child), the pointer is rebound and the exact
// byte-for-byte Recovered notice is printed to stdout (AC-2).
func TestUp_ReconstructsHookedFormula_FromStore(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	epic := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, epic.ID, agent) // one OPEN child step

	var out, errw bytes.Buffer
	reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if got := readPointer(t, agentDir); got != epic.ID {
		t.Fatalf("pointer = %q, want rebound to %q", got, epic.ID)
	}
	want := "Recovered in-flight formula " + epic.ID + " for " + agent + " (rebound .runtime/hooked_formula)\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if errw.Len() != 0 {
		t.Errorf("stderr should be empty on single-match recovery, got %q", errw.String())
	}
}

// TestUp_ReconstructsHookedFormula_CrossActor (PR #394, Thread 3): `af up <agent>`
// runs reconstructHookedFormula in the LAUNCHER's process, whose AF_ACTOR is often
// a DIFFERENT agent (e.g. a manager bringing up a specialist), so the store is
// configured with that foreign actor. The agent's own in-flight formula must STILL
// be recovered. Without an explicit Filter.Assignee on the child-step query, the
// actor overlay (memstore.go:172; mcpstore listArgs) hides every child of the epic
// (actor "manager" != assignee "alice") -> 0 children -> the in-flight epic is
// dropped -> resume silently no-ops, reintroducing the exact #392 loss this PR
// fixes. RED before the up.go:593 fix, GREEN after.
func TestUp_ReconstructsHookedFormula_CrossActor(t *testing.T) {
	mem := installMemStoreWithActor(t, "manager") // foreign, non-empty actor
	agent := "alice"
	agentDir := t.TempDir()

	epic := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, epic.ID, agent) // one OPEN child assigned to alice

	var out, errw bytes.Buffer
	reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if got := readPointer(t, agentDir); got != epic.ID {
		t.Fatalf("cross-actor af up must still recover alice's in-flight formula; pointer = %q, want %q", got, epic.ID)
	}
	want := "Recovered in-flight formula " + epic.ID + " for " + agent + " (rebound .runtime/hooked_formula)\n"
	if out.String() != want {
		t.Errorf("stdout = %q, want %q", out.String(), want)
	}
	if errw.Len() != 0 {
		t.Errorf("stderr should be empty on single-match recovery, got %q", errw.String())
	}
}

// TestUp_IntactPointer_LeftUntouched: an existing pointer is the fast path —
// the helper does nothing and prints nothing.
func TestUp_IntactPointer_LeftUntouched(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	// Seed a DIFFERENT open epic that would otherwise be recovered.
	other := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, other.ID, agent)

	// Pre-existing intact pointer.
	persistFormulaInstanceID(agentDir, "af-existing")

	var out, errw bytes.Buffer
	reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if got := readPointer(t, agentDir); got != "af-existing" {
		t.Errorf("intact pointer should be untouched, got %q", got)
	}
	if out.Len() != 0 || errw.Len() != 0 {
		t.Errorf("fast path should be silent, stdout=%q stderr=%q", out.String(), errw.String())
	}
}

// TestUp_IgnoresCompletedEpic: a closed (terminal) epic must not trigger
// recovery — no false re-hook, no warning (the K5+filter end state).
func TestUp_IgnoresCompletedEpic(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	epic := newInstanceEpic(t, mem, agent)
	child := newChildStep(t, mem, epic.ID, agent)
	if err := mem.Close(context.Background(), child.ID, ""); err != nil {
		t.Fatalf("close child: %v", err)
	}
	if err := mem.Close(context.Background(), epic.ID, "formula complete"); err != nil {
		t.Fatalf("close epic: %v", err)
	}

	var out, errw bytes.Buffer
	reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if got := readPointer(t, agentDir); got != "" {
		t.Errorf("closed epic must not rebind pointer, got %q", got)
	}
	if out.Len() != 0 || errw.Len() != 0 {
		t.Errorf("closed epic path should be silent, stdout=%q stderr=%q", out.String(), errw.String())
	}
}

// TestUp_IgnoresLegacyOpenButCompletedEpic (MED-1): an OPEN epic whose every
// child is closed — the entire pre-K5 population — must NOT recover and must
// NOT count toward the >1 WARNING. The ">=1 open child step" filter is the
// backstop, NOT K5 alone.
func TestUp_IgnoresLegacyOpenButCompletedEpic(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	epic := newInstanceEpic(t, mem, agent) // stays OPEN (legacy: never closed)
	child := newChildStep(t, mem, epic.ID, agent)
	if err := mem.Close(context.Background(), child.ID, ""); err != nil {
		t.Fatalf("close child: %v", err)
	}

	var out, errw bytes.Buffer
	reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if got := readPointer(t, agentDir); got != "" {
		t.Errorf("legacy open-but-completed epic must not rebind, got %q", got)
	}
	if out.Len() != 0 || errw.Len() != 0 {
		t.Errorf("legacy epic path should be silent, stdout=%q stderr=%q", out.String(), errw.String())
	}
}

// TestUp_OpenStepFilter_CountsReadyAndBlocked: the filter counts ALL
// non-terminal children (ready AND blocked), not just ready ones. A formula
// whose only open step is blocked (Ready returns 0 steps) must still be
// recovered — otherwise a sequential formula momentarily at "0 ready" between
// closing step N and step N+1 becoming ready would be wrongly skipped.
func TestUp_OpenStepFilter_CountsReadyAndBlocked(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()
	ctx := context.Background()

	epic := newInstanceEpic(t, mem, agent)
	step := newChildStep(t, mem, epic.ID, agent) // OPEN but will be blocked

	// An open dependency OUTSIDE the epic blocks `step`, so Ready(MoleculeID)
	// returns 0 ready steps even though the formula is in flight.
	blocker, err := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Title:    "external blocker",
		Assignee: agent,
	})
	if err != nil {
		t.Fatalf("seed blocker: %v", err)
	}
	if err := mem.DepAdd(ctx, step.ID, blocker.ID); err != nil {
		t.Fatalf("dep add: %v", err)
	}

	// Sanity: Ready sees 0 actionable steps for this molecule (the hazard).
	ready, err := mem.Ready(ctx, issuestore.Filter{MoleculeID: epic.ID})
	if err != nil {
		t.Fatalf("ready: %v", err)
	}
	if len(ready.Steps) != 0 {
		t.Fatalf("precondition: expected 0 ready steps, got %d", len(ready.Steps))
	}

	var out, errw bytes.Buffer
	reconstructHookedFormula(ctx, agentDir, agent, &out, &errw)

	if got := readPointer(t, agentDir); got != epic.ID {
		t.Errorf("blocked-but-open formula must be recovered (count via List, not Ready), pointer=%q", got)
	}
	if !strings.Contains(out.String(), "Recovered in-flight formula "+epic.ID) {
		t.Errorf("expected Recovered notice, got stdout=%q", out.String())
	}
}

// TestUp_AmbiguousMultipleEpics_Warns (design AC-4(iii)): more than one open
// in-flight instance prints the exact WARNING to stderr and rebinds nothing.
func TestUp_AmbiguousMultipleEpics_Warns(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	e1 := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, e1.ID, agent)
	e2 := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, e2.ID, agent)

	var out, errw bytes.Buffer
	reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if got := readPointer(t, agentDir); got != "" {
		t.Errorf("ambiguous case must rebind nothing, got %q", got)
	}
	want := "WARNING: " + agent + ": 2 open formula instances — cannot auto-resume; resolve manually\n"
	if errw.String() != want {
		t.Errorf("stderr = %q, want %q", errw.String(), want)
	}
	if out.Len() != 0 {
		t.Errorf("ambiguous case stdout should be empty, got %q", out.String())
	}
}

// TestUp_StoreUnavailable_ProceedsAsToday (R12): a store/daemon error during
// reconstruction must never block or crash af up — warn and return so bring-up
// proceeds to mgr.Start().
func TestUp_StoreUnavailable_ProceedsAsToday(t *testing.T) {
	installFailingIssueStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	var out, errw bytes.Buffer
	// Must not panic.
	reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if got := readPointer(t, agentDir); got != "" {
		t.Errorf("store-unavailable path must not rebind, got %q", got)
	}
	if errw.Len() == 0 {
		t.Errorf("store-unavailable path should emit a non-fatal warning to stderr")
	}
	if out.Len() != 0 {
		t.Errorf("store-unavailable path stdout should be empty, got %q", out.String())
	}
}

// TestUp_SelfScopedRead_NoCrossActorLeak: the recovery query is self-scoped by
// explicit Filter.Assignee (ADR-002), so another agent's open in-flight epic is
// never recovered into this agent's pointer.
func TestUp_SelfScopedRead_NoCrossActorLeak(t *testing.T) {
	mem := installMemStore(t)
	agentDir := t.TempDir()

	// An open in-flight epic belonging to BOB.
	bobEpic := newInstanceEpic(t, mem, "bob")
	newChildStep(t, mem, bobEpic.ID, "bob")

	var out, errw bytes.Buffer
	reconstructHookedFormula(context.Background(), agentDir, "alice", &out, &errw)

	if got := readPointer(t, agentDir); got != "" {
		t.Errorf("alice must not recover bob's epic, got %q", got)
	}
	if out.Len() != 0 || errw.Len() != 0 {
		t.Errorf("self-scoped read should be silent for alice, stdout=%q stderr=%q", out.String(), errw.String())
	}
}

// TestPrime_ResumesAfterReattach: behavioral end-to-end — after the pointer is
// rebound by reconstruction, `af prime`'s outputFormulaContext renders a
// NON-EMPTY formula section (the #392 symptom is that it was silently empty).
func TestPrime_ResumesAfterReattach(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()
	ctx := context.Background()

	epic := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, epic.ID, agent) // open, ready step

	// Before rebind: prime renders nothing (the lost-pointer symptom).
	var before bytes.Buffer
	outputFormulaContext(ctx, &before, agentDir)
	if before.Len() != 0 {
		t.Fatalf("precondition: prime should be empty before rebind, got %q", before.String())
	}

	// Reconstruct the pointer.
	var recOut, recErr bytes.Buffer
	reconstructHookedFormula(ctx, agentDir, agent, &recOut, &recErr)
	if readPointer(t, agentDir) != epic.ID {
		t.Fatalf("pointer not rebound: %q", readPointer(t, agentDir))
	}

	// After rebind: prime resumes — output is non-empty and names the formula.
	var after bytes.Buffer
	outputFormulaContext(ctx, &after, agentDir)
	if after.Len() == 0 {
		t.Fatalf("af prime should resume (non-empty) after rebind, but output was empty")
	}
	if !strings.Contains(after.String(), "Formula Workflow") {
		t.Errorf("expected resumed formula context, got %q", after.String())
	}
}
