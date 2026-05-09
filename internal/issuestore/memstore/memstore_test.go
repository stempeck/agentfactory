package memstore_test

import (
	"context"
	"sync"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// TestMemStoreContract runs the cross-adapter behavioral contract against the
// in-memory adapter. memstore and mcpstore both call this same suite to
// guarantee they cannot drift.
func TestMemStoreContract(t *testing.T) {
	factory := func(actor string) issuestore.Store {
		return memstore.NewWithActor(actor)
	}
	setStatus := func(s issuestore.Store, id string, status issuestore.Status) error {
		return s.(*memstore.Store).SetStatus(id, status)
	}
	issuestore.RunStoreContract(t, factory, setStatus)
}

// TestMemStoreSeed verifies the bulk-load helper goes through the same code
// path as Create and assigns unique IDs.
func TestMemStoreSeed(t *testing.T) {
	store := memstore.New()
	err := store.Seed(
		issuestore.CreateParams{Title: "one", Type: issuestore.TypeTask, Assignee: "AF_ACTOR"},
		issuestore.CreateParams{Title: "two", Type: issuestore.TypeTask, Assignee: "AF_ACTOR"},
		issuestore.CreateParams{Title: "three", Type: issuestore.TypeTask, Assignee: "AF_ACTOR"},
	)
	if err != nil {
		t.Fatalf("Seed: %v", err)
	}
	got, err := store.List(context.Background(), issuestore.Filter{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Errorf("List after Seed returned %d issues, want 3", len(got))
	}
	seen := map[string]bool{}
	for _, iss := range got {
		if seen[iss.ID] {
			t.Errorf("Seed produced duplicate ID %q", iss.ID)
		}
		seen[iss.ID] = true
	}
}

// TestMemStoreConcurrent exercises the mutex by hammering Create + List from
// many goroutines. The race detector catches mutation races.
func TestMemStoreConcurrent(t *testing.T) {
	store := memstore.New()
	ctx := context.Background()
	const goroutines = 20
	const perGoroutine = 10

	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_, err := store.Create(ctx, issuestore.CreateParams{
					Title:    "concurrent",
					Type:     issuestore.TypeTask,
					Assignee: "AF_ACTOR",
				})
				if err != nil {
					t.Errorf("Create: %v", err)
				}
				if _, err := store.List(ctx, issuestore.Filter{}); err != nil {
					t.Errorf("List: %v", err)
				}
			}
		}()
	}
	wg.Wait()

	got, err := store.List(ctx, issuestore.Filter{})
	if err != nil {
		t.Fatalf("final List: %v", err)
	}
	if want := goroutines * perGoroutine; len(got) != want {
		t.Errorf("got %d issues after concurrent creates, want %d", len(got), want)
	}
}

// TestMemStoreLabelWireOrder verifies that memstore preserves wire order and
// duplicates exactly (C13). The cross-adapter contract uses set semantics
// because bd itself sorts labels; memstore is the canonical reference for the
// "preserves wire order" guarantee that the design language calls for.
func TestMemStoreLabelWireOrder(t *testing.T) {
	store := memstore.New()
	ctx := context.Background()
	in := []string{"zeta", "alpha", "alpha", "mu"}
	created, err := store.Create(ctx, issuestore.CreateParams{
		Title:    "wire order",
		Type:     issuestore.TypeTask,
		Labels:   in,
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	fetched, err := store.Get(ctx, created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(fetched.Labels) != len(in) {
		t.Fatalf("Labels len = %d, want %d (no dedup)", len(fetched.Labels), len(in))
	}
	for i := range in {
		if fetched.Labels[i] != in[i] {
			t.Errorf("Labels[%d] = %q, want %q (preserve wire order)", i, fetched.Labels[i], in[i])
		}
	}
}

// TestMemStoreUniqueIDs verifies sequential creates produce distinct ids.
func TestMemStoreUniqueIDs(t *testing.T) {
	store := memstore.New()
	ctx := context.Background()
	seen := map[string]bool{}
	for i := 0; i < 50; i++ {
		iss, err := store.Create(ctx, issuestore.CreateParams{
			Title: "u", Type: issuestore.TypeTask, Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if seen[iss.ID] {
			t.Errorf("duplicate ID after %d creates: %q", i, iss.ID)
		}
		seen[iss.ID] = true
	}
}

// TestMemStore_ExplicitAssigneeWinsOverActorOverlay is the AC-1 direct
// pin for #125. The cross-adapter contract test
// (RunStoreContract.ExplicitAssigneeWinsOverActorOverlay) covers the
// same invariant; this one stays as a memstore-targeted regression so a
// future memstore-only edit cannot break the predicate without local
// failure.
func TestMemStore_ExplicitAssigneeWinsOverActorOverlay(t *testing.T) {
	store := memstore.NewWithActor("agent-Y")
	ctx := context.Background()
	seeded, err := store.Create(ctx, issuestore.CreateParams{
		Title: "x-issue", Type: issuestore.TypeTask, Assignee: "agent-X",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	got, err := store.List(ctx, issuestore.Filter{Assignee: "agent-X"})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != seeded.ID {
		t.Fatalf("explicit Assignee=agent-X must win over actor overlay; got %d issues", len(got))
	}
}

// TestMemStore_Ready_TotalSteps_LinearChain verifies that TotalSteps equals the
// total number of children under a formula instance, not just the count of
// currently ready steps. This is the Go-side equivalent of the Python test
// test_linear_chain_total_steps_equals_child_count.
func TestMemStore_Ready_TotalSteps_LinearChain(t *testing.T) {
	store := memstore.NewWithActor("alice")
	ctx := context.Background()

	epic, err := store.Create(ctx, issuestore.CreateParams{
		Title: "epic", Type: issuestore.TypeEpic, Assignee: "alice",
	})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}

	child1, err := store.Create(ctx, issuestore.CreateParams{
		Title: "child-1", Type: issuestore.TypeTask, Parent: epic.ID, Assignee: "alice",
	})
	if err != nil {
		t.Fatalf("Create child-1: %v", err)
	}
	child2, err := store.Create(ctx, issuestore.CreateParams{
		Title: "child-2", Type: issuestore.TypeTask, Parent: epic.ID, Assignee: "alice",
	})
	if err != nil {
		t.Fatalf("Create child-2: %v", err)
	}
	child3, err := store.Create(ctx, issuestore.CreateParams{
		Title: "child-3", Type: issuestore.TypeTask, Parent: epic.ID, Assignee: "alice",
	})
	if err != nil {
		t.Fatalf("Create child-3: %v", err)
	}

	if err := store.DepAdd(ctx, child2.ID, child1.ID); err != nil {
		t.Fatalf("DepAdd child-2 -> child-1: %v", err)
	}
	if err := store.DepAdd(ctx, child3.ID, child2.ID); err != nil {
		t.Fatalf("DepAdd child-3 -> child-2: %v", err)
	}

	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: epic.ID})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}

	if result.TotalSteps != 3 {
		t.Errorf("TotalSteps = %d, want 3 (total children under epic, not just ready count)", result.TotalSteps)
	}
	if len(result.Steps) != 1 {
		t.Errorf("len(Steps) = %d, want 1 (only child-1 should be ready)", len(result.Steps))
	}
	if len(result.Steps) > 0 && result.Steps[0].ID != child1.ID {
		t.Errorf("Steps[0].ID = %q, want %q (child-1)", result.Steps[0].ID, child1.ID)
	}
}

// TestMemStore_Ready_ActorScoping verifies that Ready respects the actor
// overlay from matchesFilter, hiding steps assigned to other agents.
func TestMemStore_Ready_ActorScoping(t *testing.T) {
	store := memstore.NewWithActor("agent-a")
	ctx := context.Background()

	epic, err := store.Create(ctx, issuestore.CreateParams{
		Title: "epic", Type: issuestore.TypeEpic, Assignee: "agent-a",
	})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}

	childA, err := store.Create(ctx, issuestore.CreateParams{
		Title: "child-a", Type: issuestore.TypeTask, Parent: epic.ID, Assignee: "agent-a",
	})
	if err != nil {
		t.Fatalf("Create child-a: %v", err)
	}
	// Create child-b assigned to agent-b. Since we're store actor agent-a,
	// we use IncludeAllAgents to list it, but Ready should hide it.
	_, err = store.Create(ctx, issuestore.CreateParams{
		Title: "child-b", Type: issuestore.TypeTask, Parent: epic.ID, Assignee: "agent-b",
	})
	if err != nil {
		t.Fatalf("Create child-b: %v", err)
	}

	// Ready without IncludeAllAgents: should only see agent-a's child
	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: epic.ID})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}

	if len(result.Steps) != 1 {
		t.Errorf("Ready (actor=agent-a): len(Steps) = %d, want 1 (only agent-a child)", len(result.Steps))
	}
	if len(result.Steps) == 1 && result.Steps[0].ID != childA.ID {
		t.Errorf("Ready (actor=agent-a): Steps[0].ID = %q, want %q", result.Steps[0].ID, childA.ID)
	}

	// Ready with IncludeAllAgents: should see both children
	resultAll, err := store.Ready(ctx, issuestore.Filter{MoleculeID: epic.ID, IncludeAllAgents: true})
	if err != nil {
		t.Fatalf("Ready (IncludeAllAgents): %v", err)
	}
	if len(resultAll.Steps) != 2 {
		t.Errorf("Ready (IncludeAllAgents=true): len(Steps) = %d, want 2", len(resultAll.Steps))
	}
}

// TestMemstoreCreate_ParentWithEmptyAssigneeRejected pins the Phase 1
// data-plane invariant (parent_id = '' OR assignee != '') at the memstore
// adapter's Create seam. The guard mirrors the SQLite CHECK added in
// schema v2 so unit tests and the Python-backed mcpstore surface the same
// failure mode.
func TestMemstoreCreate_ParentWithEmptyAssigneeRejected(t *testing.T) {
	store := memstore.New()
	ctx := context.Background()

	parent, err := store.Create(ctx, issuestore.CreateParams{
		Title:    "epic",
		Type:     issuestore.TypeEpic,
		Assignee: "alice",
	})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}

	_, err = store.Create(ctx, issuestore.CreateParams{
		Title:    "child",
		Type:     issuestore.TypeTask,
		Parent:   parent.ID,
		Assignee: "",
	})
	if err == nil {
		t.Fatal("Create(parent-scoped, empty Assignee) should have errored")
	}

	all, listErr := store.List(ctx, issuestore.Filter{IncludeAllAgents: true, IncludeClosed: true})
	if listErr != nil {
		t.Fatalf("List: %v", listErr)
	}
	if len(all) != 1 {
		t.Errorf("store count = %d, want 1 (epic only; guarded child must not be written)", len(all))
	}

	// Top-level bead (Parent == "") with empty Assignee must still be permitted
	// — the left branch of the CHECK (parent_id = '') admits it.
	if _, err := store.Create(ctx, issuestore.CreateParams{
		Title: "top-level",
		Type:  issuestore.TypeTask,
	}); err != nil {
		t.Errorf("Create(top-level, empty Assignee) must succeed; got %v", err)
	}
}
