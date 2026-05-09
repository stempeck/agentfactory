package issuestore

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// SetStatusFn is a test-only helper that places a seeded issue into a target
// lifecycle state. Each adapter passes its own implementation:
//
//   - memstore wraps its private map writer.
//   - mcpstore calls the server's issuestore_patch tool with a status field.
//
// This avoids polluting the public Store interface with a test-only setter,
// while still allowing RunStoreContract to seed all six Status values per
// Gotcha 9 / D11 / C-1.
type SetStatusFn func(store Store, id string, status Status) error

// RunStoreContract is the cross-adapter behavioral contract every Store
// implementation must satisfy. memstore and mcpstore each call this from
// their own test files; the assertions live here so the adapters cannot
// drift.
//
// This is intentionally NOT a _test.go file: sub-package tests must be able
// to import it. Importing the testing package outside of _test.go is fine —
// it's a normal Go package and the linker drops it from production builds
// because no production code references RunStoreContract.
//
// factory must return a fresh, empty Store on every call. The actor argument
// configures the store's actor-scoping identity — most sub-tests pass "" to
// disable scoping; the Actor_scoping sub-test passes "test-actor" to exercise
// the filter. Actor must flow in as an explicit parameter; adapters must not
// read it from process environment (#98 env-isolation invariant).
//
// setStatus is the per-adapter helper described above. It MUST be able to
// place a seeded issue into any of the six Status values; if not, contract
// assertions on hooked/pinned/in_progress/done will be skipped silently and
// the test loses its bug-prevention value.
//
// The contract pins (in addition to compilation against the interface):
//   - IsTerminal returns true for closed AND done; false for the other four
//     statuses (D11/C-1).
//   - Filter.Statuses=nil returns all non-terminal issues (H-A R2; adapters
//     must NOT pass an empty `--status`-equivalent filter when nil —
//     Gotcha 12).
//   - Filter.Statuses=[StatusOpen] returns ONLY open (not hooked/pinned/
//     in_progress); critical for mail's C8 preservation in Phase 2.
//   - Filter.Statuses=[StatusOpen, StatusInProgress] returns both as an OR
//     (D14/H-3).
//   - Filter.IncludeClosed=true admits closed/done.
//   - Filter.IncludeAllAgents=true admits non-AF_ACTOR assignees.
//   - Filter.Parent returns only issues whose Parent equals the filter
//     value (no parents themselves, no unrelated children).
//   - Labels round-trip with insertion-order preserved: every input label
//     is observable on Get in the order it was supplied, and no labels
//     are introduced. Insertion-order preservation is pinned across ALL
//     adapters (C13).
//   - Get on a missing id returns a wrapped ErrNotFound.
//   - Every one of the 9 Store methods runs at least once.
func RunStoreContract(t *testing.T, factory func(actor string) Store, setStatus SetStatusFn) {
	t.Helper()

	t.Run("IsTerminal_policy", func(t *testing.T) {
		// Pure type-level test, no factory needed.
		terminal := []Status{StatusClosed, StatusDone}
		nonTerminal := []Status{StatusOpen, StatusHooked, StatusPinned, StatusInProgress}
		for _, s := range terminal {
			if !s.IsTerminal() {
				t.Errorf("IsTerminal(%q) = false, want true", string(s))
			}
		}
		for _, s := range nonTerminal {
			if s.IsTerminal() {
				t.Errorf("IsTerminal(%q) = true, want false", string(s))
			}
		}
	})

	t.Run("Get_missing_returns_ErrNotFound", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		_, err := store.Get(ctx, "does-not-exist-zzzzz")
		if err == nil {
			t.Fatal("Get on missing id returned nil error")
		}
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("Get error = %v, want errors.Is(...ErrNotFound)", err)
		}
	})

	t.Run("Create_then_Get_round_trips", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		created, err := store.Create(ctx, CreateParams{
			Title:       "round trip",
			Description: "the body",
			Type:        TypeTask,
			Priority:    PriorityNormal,
			Labels:      []string{"a", "b"},
			Assignee:    "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		if created.ID == "" {
			t.Fatal("Create returned empty ID")
		}
		fetched, err := store.Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get(%s): %v", created.ID, err)
		}
		if fetched.Title != "round trip" {
			t.Errorf("Title = %q, want %q", fetched.Title, "round trip")
		}
	})

	t.Run("Labels_round_trip_no_dedup_no_canonicalization", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		// C13: the translation layer must not dedup or canonicalize labels.
		// This test asserts set-equal semantics (no labels lost or added).
		// The separate Labels_preserve_insertion_order test below pins
		// exact ordering across all adapters.
		created, err := store.Create(ctx, CreateParams{
			Title:    "label round trip",
			Type:     TypeTask,
			Labels:   []string{"alpha", "mu", "zeta"},
			Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}
		fetched, err := store.Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		// Set semantics: every input label must come back; no labels added.
		want := map[string]bool{"alpha": true, "mu": true, "zeta": true}
		got := map[string]bool{}
		for _, l := range fetched.Labels {
			got[l] = true
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Labels = %v, want set %v (no dedup, no canonicalization — C13)",
				fetched.Labels, want)
		}
	})

	// seedSixStatuses creates one issue for each of the six lifecycle states.
	// All assignees are "AF_ACTOR" except one ("other-agent") used by the
	// IncludeAllAgents test.
	type seeded struct {
		open, hooked, pinned, inProgress, closed, done string
		otherAgent                                     string
	}
	seedSixStatuses := func(t *testing.T, store Store) seeded {
		t.Helper()
		ctx := context.Background()
		mk := func(title string) string {
			iss, err := store.Create(ctx, CreateParams{
				Title: title, Type: TypeTask, Assignee: "AF_ACTOR",
			})
			if err != nil {
				t.Fatalf("seed Create %q: %v", title, err)
			}
			return iss.ID
		}
		var s seeded
		s.open = mk("open-fixture")
		s.hooked = mk("hooked-fixture")
		s.pinned = mk("pinned-fixture")
		s.inProgress = mk("in_progress-fixture")
		s.closed = mk("closed-fixture")
		s.done = mk("done-fixture")

		// One non-AF_ACTOR fixture for IncludeAllAgents.
		other, err := store.Create(ctx, CreateParams{
			Title: "other-agent-fixture", Type: TypeTask, Assignee: "other-agent",
		})
		if err != nil {
			t.Fatalf("seed other agent: %v", err)
		}
		s.otherAgent = other.ID

		// Transition fixtures into their target lifecycle states via the
		// adapter-supplied setStatus helper. Per Gotcha 9, all six states
		// must be exercised.
		if err := setStatus(store, s.hooked, StatusHooked); err != nil {
			t.Fatalf("setStatus hooked: %v", err)
		}
		if err := setStatus(store, s.pinned, StatusPinned); err != nil {
			t.Fatalf("setStatus pinned: %v", err)
		}
		if err := setStatus(store, s.inProgress, StatusInProgress); err != nil {
			t.Fatalf("setStatus in_progress: %v", err)
		}
		if err := setStatus(store, s.closed, StatusClosed); err != nil {
			t.Fatalf("setStatus closed: %v", err)
		}
		if err := setStatus(store, s.done, StatusDone); err != nil {
			t.Fatalf("setStatus done: %v", err)
		}
		return s
	}

	titlesOf := func(issues []Issue) []string {
		out := make([]string, 0, len(issues))
		for _, i := range issues {
			out = append(out, i.Title)
		}
		sort.Strings(out)
		return out
	}
	contains := func(haystack []string, needle string) bool {
		for _, h := range haystack {
			if h == needle {
				return true
			}
		}
		return false
	}

	t.Run("Filter_nil_Statuses_returns_all_non_terminal", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		seedSixStatuses(t, store)
		got, err := store.List(ctx, Filter{Statuses: nil, IncludeAllAgents: true})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		titles := titlesOf(got)
		// All four non-terminal fixtures must be present.
		for _, want := range []string{"open-fixture", "hooked-fixture", "pinned-fixture", "in_progress-fixture"} {
			if !contains(titles, want) {
				t.Errorf("nil Statuses missing %s; got %v", want, titles)
			}
		}
		// Both terminal fixtures must be excluded.
		for _, bad := range []string{"closed-fixture", "done-fixture"} {
			if contains(titles, bad) {
				t.Errorf("nil Statuses leaked terminal fixture %s", bad)
			}
		}
	})

	t.Run("Filter_single_status_open_returns_only_open", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		seedSixStatuses(t, store)
		got, err := store.List(ctx, Filter{
			Statuses:         []Status{StatusOpen},
			IncludeAllAgents: true,
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, iss := range got {
			if iss.Status != StatusOpen {
				t.Errorf("Statuses=[Open] returned %s (status=%s); H-A R2 violated",
					iss.Title, iss.Status)
			}
		}
		titles := titlesOf(got)
		if !contains(titles, "open-fixture") {
			t.Errorf("Statuses=[Open] missing open-fixture; got %v", titles)
		}
		// In particular: hooked/pinned/in_progress must NOT appear.
		for _, bad := range []string{"hooked-fixture", "pinned-fixture", "in_progress-fixture"} {
			if contains(titles, bad) {
				t.Errorf("Statuses=[Open] leaked %s", bad)
			}
		}
	})

	t.Run("Filter_multi_status_OR_semantics", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		seedSixStatuses(t, store)
		got, err := store.List(ctx, Filter{
			Statuses:         []Status{StatusOpen, StatusInProgress},
			IncludeAllAgents: true,
		})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, iss := range got {
			if iss.Status != StatusOpen && iss.Status != StatusInProgress {
				t.Errorf("multi-status filter returned %s with status %s",
					iss.Title, iss.Status)
			}
		}
		titles := titlesOf(got)
		if !contains(titles, "open-fixture") {
			t.Error("multi-status filter missing open-fixture")
		}
		if !contains(titles, "in_progress-fixture") {
			t.Error("multi-status filter missing in_progress-fixture")
		}
	})

	t.Run("Filter_IncludeClosed_admits_terminal", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		seedSixStatuses(t, store)
		got, err := store.List(ctx, Filter{IncludeClosed: true, IncludeAllAgents: true})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		titles := titlesOf(got)
		if !contains(titles, "closed-fixture") {
			t.Errorf("IncludeClosed=true did not return closed-fixture")
		}
		if !contains(titles, "done-fixture") {
			t.Errorf("IncludeClosed=true did not return done-fixture")
		}
	})

	t.Run("Filter_IncludeAllAgents_admits_other_agents", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		seedSixStatuses(t, store)
		got, err := store.List(ctx, Filter{IncludeAllAgents: true})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		titles := titlesOf(got)
		if !contains(titles, "other-agent-fixture") {
			t.Errorf("IncludeAllAgents=true did not return other-agent-fixture")
		}
	})

	t.Run("Filter_IncludeAllAgents_false_hides_other_agents", func(t *testing.T) {
		ctx := context.Background()
		// Scope the store to "AF_ACTOR" (the Assignee seedSixStatuses uses
		// for its majority fixtures) so the filter has something to hide
		// the "other-agent" fixture against. Actor flows in via the factory
		// parameter; no process env involved.
		store := factory("AF_ACTOR")
		seedSixStatuses(t, store)
		got, err := store.List(ctx, Filter{IncludeAllAgents: false})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		for _, iss := range got {
			if strings.Contains(iss.Title, "other-agent") {
				t.Errorf("IncludeAllAgents=false leaked other-agent-fixture")
			}
		}
	})

	t.Run("Filter_Parent_returns_only_matching_children", func(t *testing.T) {
		// Pins that Filter.Parent on List returns only issues whose Parent
		// equals the filter value: not parents themselves, not children of
		// other parents. Adapters implement Parent by direct equality against
		// the parent ID; without this assertion, either could regress
		// silently. One-line follow-up #4 from the Phase 1 commit message —
		// pinned here so Phase 2's mail/cmd migrations cannot rely on broken
		// parent filtering.
		ctx := context.Background()
		store := factory("")

		// Create two parent epics. Children below reference these IDs, so
		// the parents must exist first (bd's --parent enforces existence).
		epic1, err := store.Create(ctx, CreateParams{
			Title: "epic-one", Type: TypeEpic, Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create epic1: %v", err)
		}
		epic2, err := store.Create(ctx, CreateParams{
			Title: "epic-two", Type: TypeEpic, Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create epic2: %v", err)
		}

		// Two children of epic1, one child of epic2.
		for _, title := range []string{"child-1a", "child-1b"} {
			if _, err := store.Create(ctx, CreateParams{
				Title: title, Type: TypeTask, Parent: epic1.ID, Assignee: "AF_ACTOR",
			}); err != nil {
				t.Fatalf("Create %s: %v", title, err)
			}
		}
		if _, err := store.Create(ctx, CreateParams{
			Title: "child-2a", Type: TypeTask, Parent: epic2.ID, Assignee: "AF_ACTOR",
		}); err != nil {
			t.Fatalf("Create child-2a: %v", err)
		}

		got, err := store.List(ctx, Filter{Parent: epic1.ID})
		if err != nil {
			t.Fatalf("List Parent=%s: %v", epic1.ID, err)
		}
		titles := titlesOf(got)
		for _, want := range []string{"child-1a", "child-1b"} {
			if !contains(titles, want) {
				t.Errorf("Filter.Parent=epic1 missing %s; got %v", want, titles)
			}
		}
		if contains(titles, "child-2a") {
			t.Errorf("Filter.Parent=epic1 leaked child-2a from epic2; got %v", titles)
		}
		if contains(titles, "epic-one") || contains(titles, "epic-two") {
			t.Errorf("Filter.Parent=epic1 returned a parent epic itself; got %v", titles)
		}
	})

	t.Run("All_nine_methods_callable", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")
		// Create
		a, err := store.Create(ctx, CreateParams{Title: "a", Type: TypeTask, Assignee: "AF_ACTOR"})
		if err != nil {
			t.Fatalf("Create a: %v", err)
		}
		b, err := store.Create(ctx, CreateParams{Title: "b", Type: TypeTask, Assignee: "AF_ACTOR"})
		if err != nil {
			t.Fatalf("Create b: %v", err)
		}
		// Get
		if _, err := store.Get(ctx, a.ID); err != nil {
			t.Errorf("Get: %v", err)
		}
		// List
		if _, err := store.List(ctx, Filter{}); err != nil {
			t.Errorf("List: %v", err)
		}
		// Ready
		if _, err := store.Ready(ctx, Filter{}); err != nil {
			t.Errorf("Ready: %v", err)
		}
		// DepAdd
		if err := store.DepAdd(ctx, a.ID, b.ID); err != nil {
			t.Errorf("DepAdd: %v", err)
		}
		// Patch
		notes := "added"
		if err := store.Patch(ctx, a.ID, Patch{Notes: &notes}); err != nil {
			t.Errorf("Patch: %v", err)
		}
		// Render
		if _, err := store.Render(ctx, a.ID); err != nil {
			t.Errorf("Render: %v", err)
		}
		// RenderList
		if _, err := store.RenderList(ctx, Filter{}); err != nil {
			t.Errorf("RenderList: %v", err)
		}
		// Close
		if err := store.Close(ctx, a.ID, "done with it"); err != nil {
			t.Errorf("Close: %v", err)
		}
	})

	// ---------------------------------------------------------------
	// Phase 2 assertions: 9 new sub-tests pinning behaviors that were
	// previously unasserted and could silently drift across adapters.
	// ---------------------------------------------------------------

	t.Run("Ready_ordering_by_created_at_ASC", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")

		// Create 3 issues with small sleep gaps to ensure distinct created_at.
		var ids []string
		for i := 0; i < 3; i++ {
			if i > 0 {
				time.Sleep(2 * time.Millisecond)
			}
			iss, err := store.Create(ctx, CreateParams{
				Title:    "ready-order-" + strings.Repeat("x", i),
				Type:     TypeTask,
				Assignee: "AF_ACTOR",
			})
			if err != nil {
				t.Fatalf("Create %d: %v", i, err)
			}
			ids = append(ids, iss.ID)
		}

		result, err := store.Ready(ctx, Filter{IncludeAllAgents: true})
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}

		// Assert Steps are ordered by CreatedAt ascending.
		for i := 1; i < len(result.Steps); i++ {
			prev := result.Steps[i-1]
			curr := result.Steps[i]
			if curr.CreatedAt.Before(prev.CreatedAt) {
				t.Errorf("Steps[%d].CreatedAt (%v) < Steps[%d].CreatedAt (%v): not ASC",
					i, curr.CreatedAt, i-1, prev.CreatedAt)
			}
		}

		// Create 2 more issues rapidly (may share the same created_at at
		// millisecond granularity) to exercise ID tie-breaking.
		for i := 0; i < 2; i++ {
			iss, err := store.Create(ctx, CreateParams{
				Title:    fmt.Sprintf("rapid-%d", i),
				Type:     TypeTask,
				Assignee: "AF_ACTOR",
			})
			if err != nil {
				t.Fatalf("Create rapid %d: %v", i, err)
			}
			ids = append(ids, iss.ID)
		}

		result, err = store.Ready(ctx, Filter{IncludeAllAgents: true})
		if err != nil {
			t.Fatalf("Ready (tie-break): %v", err)
		}
		for i := 1; i < len(result.Steps); i++ {
			prev := result.Steps[i-1]
			curr := result.Steps[i]
			if curr.CreatedAt.Before(prev.CreatedAt) {
				t.Errorf("tie-break: Steps[%d].CreatedAt (%v) < Steps[%d].CreatedAt (%v)",
					i, curr.CreatedAt, i-1, prev.CreatedAt)
			}
			if curr.CreatedAt.Equal(prev.CreatedAt) && curr.ID < prev.ID {
				t.Errorf("tie-break: Steps[%d].ID (%s) < Steps[%d].ID (%s) with same CreatedAt",
					i, curr.ID, i-1, prev.ID)
			}
		}
		_ = ids // used for creation; ordering verified via Steps
	})

	t.Run("Ready_dependency_filtering", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")

		a, err := store.Create(ctx, CreateParams{
			Title: "dep-blocked", Type: TypeTask, Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create A: %v", err)
		}
		b, err := store.Create(ctx, CreateParams{
			Title: "dep-target", Type: TypeTask, Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create B: %v", err)
		}

		if err := store.DepAdd(ctx, a.ID, b.ID); err != nil {
			t.Fatalf("DepAdd: %v", err)
		}

		// A should be blocked (B is open).
		result, err := store.Ready(ctx, Filter{IncludeAllAgents: true})
		if err != nil {
			t.Fatalf("Ready (blocked): %v", err)
		}
		for _, step := range result.Steps {
			if step.ID == a.ID {
				t.Errorf("Ready returned blocked issue A (%s) while dep B is open", a.ID)
			}
		}

		// Close B — A should become unblocked.
		if err := store.Close(ctx, b.ID, "done"); err != nil {
			t.Fatalf("Close B: %v", err)
		}

		result, err = store.Ready(ctx, Filter{IncludeAllAgents: true})
		if err != nil {
			t.Fatalf("Ready (unblocked): %v", err)
		}
		found := false
		for _, step := range result.Steps {
			if step.ID == a.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Ready did not return A (%s) after dep B was closed", a.ID)
		}
	})

	t.Run("Ready_orphan_deps_dont_block", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")

		a, err := store.Create(ctx, CreateParams{
			Title: "orphan-dep-issue", Type: TypeTask, Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create A: %v", err)
		}
		b, err := store.Create(ctx, CreateParams{
			Title: "orphan-dep-target", Type: TypeTask, Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create B: %v", err)
		}

		if err := store.DepAdd(ctx, a.ID, b.ID); err != nil {
			t.Fatalf("DepAdd: %v", err)
		}

		// Close B BEFORE ever calling Ready.
		if err := store.Close(ctx, b.ID, "already done"); err != nil {
			t.Fatalf("Close B: %v", err)
		}

		result, err := store.Ready(ctx, Filter{IncludeAllAgents: true})
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		found := false
		for _, step := range result.Steps {
			if step.ID == a.ID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("Ready did not return A (%s) when dep B is already closed", a.ID)
		}
	})

	t.Run("TotalSteps_ge_steps_len", func(t *testing.T) {
		ctx := context.Background()
		store := factory("test-agent")

		epic, err := store.Create(ctx, CreateParams{
			Title: "parent-epic", Type: TypeEpic, Assignee: "test-agent",
		})
		if err != nil {
			t.Fatalf("Create epic: %v", err)
		}

		child0, err := store.Create(ctx, CreateParams{
			Title: "child-0", Type: TypeTask, Parent: epic.ID, Assignee: "test-agent",
		})
		if err != nil {
			t.Fatalf("Create child-0: %v", err)
		}
		child1, err := store.Create(ctx, CreateParams{
			Title: "child-1", Type: TypeTask, Parent: epic.ID, Assignee: "test-agent",
		})
		if err != nil {
			t.Fatalf("Create child-1: %v", err)
		}
		child2, err := store.Create(ctx, CreateParams{
			Title: "child-2", Type: TypeTask, Parent: epic.ID, Assignee: "test-agent",
		})
		if err != nil {
			t.Fatalf("Create child-2: %v", err)
		}

		if err := store.DepAdd(ctx, child1.ID, child0.ID); err != nil {
			t.Fatalf("DepAdd child-1 -> child-0: %v", err)
		}
		if err := store.DepAdd(ctx, child2.ID, child1.ID); err != nil {
			t.Fatalf("DepAdd child-2 -> child-1: %v", err)
		}

		result, err := store.Ready(ctx, Filter{MoleculeID: epic.ID})
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if result.TotalSteps != 3 {
			t.Errorf("TotalSteps = %d, want 3 (all children counted)", result.TotalSteps)
		}
		if len(result.Steps) != 1 {
			t.Errorf("len(Steps) = %d, want 1 (only child-0 ready; child-1 and child-2 blocked)", len(result.Steps))
		}
	})

	t.Run("Patch_notes_populates_Notes_field", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")

		created, err := store.Create(ctx, CreateParams{
			Title:       "patch-notes-test",
			Description: "orig",
			Type:        TypeTask,
			Assignee:    "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		notes1 := "my notes"
		if err := store.Patch(ctx, created.ID, Patch{Notes: &notes1}); err != nil {
			t.Fatalf("Patch 1: %v", err)
		}

		fetched, err := store.Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get after Patch 1: %v", err)
		}
		if fetched.Notes != "my notes" {
			t.Errorf("Notes = %q, want %q", fetched.Notes, "my notes")
		}
		if fetched.Description != "orig" {
			t.Errorf("Description = %q, want %q (Patch should not mutate Description)",
				fetched.Description, "orig")
		}

		// Second Patch: last-writer-wins, not append.
		notes2 := "second"
		if err := store.Patch(ctx, created.ID, Patch{Notes: &notes2}); err != nil {
			t.Fatalf("Patch 2: %v", err)
		}

		fetched, err = store.Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get after Patch 2: %v", err)
		}
		if fetched.Notes != "second" {
			t.Errorf("Notes after second Patch = %q, want %q (last-writer-wins)",
				fetched.Notes, "second")
		}
		if fetched.Description != "orig" {
			t.Errorf("Description = %q, want %q (still untouched)",
				fetched.Description, "orig")
		}
	})

	t.Run("Close_reason_populates_CloseReason_field", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")

		created, err := store.Create(ctx, CreateParams{
			Title:       "close-reason-test",
			Description: "orig",
			Type:        TypeTask,
			Assignee:    "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		if err := store.Close(ctx, created.ID, "done"); err != nil {
			t.Fatalf("Close: %v", err)
		}

		fetched, err := store.Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get after Close: %v", err)
		}
		if fetched.CloseReason != "done" {
			t.Errorf("CloseReason = %q, want %q", fetched.CloseReason, "done")
		}
		if fetched.Description != "orig" {
			t.Errorf("Description = %q, want %q (Close should not mutate Description)",
				fetched.Description, "orig")
		}
	})

	t.Run("Labels_preserve_insertion_order", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")

		want := []string{"z", "a", "m"}
		created, err := store.Create(ctx, CreateParams{
			Title:    "label-order-test",
			Type:     TypeTask,
			Labels:   want,
			Assignee: "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		fetched, err := store.Get(ctx, created.ID)
		if err != nil {
			t.Fatalf("Get: %v", err)
		}
		if len(fetched.Labels) != len(want) {
			t.Fatalf("Labels len = %d, want %d", len(fetched.Labels), len(want))
		}
		for i := range want {
			if fetched.Labels[i] != want[i] {
				t.Errorf("Labels[%d] = %q, want %q (insertion order not preserved)",
					i, fetched.Labels[i], want[i])
			}
		}
	})

	t.Run("Render_semantic_parity", func(t *testing.T) {
		ctx := context.Background()
		store := factory("")

		created, err := store.Create(ctx, CreateParams{
			Title:       "render-parity-issue",
			Description: "the description body",
			Type:        TypeBug,
			Priority:    PriorityHigh,
			Labels:      []string{"render-label"},
			Assignee:    "AF_ACTOR",
		})
		if err != nil {
			t.Fatalf("Create: %v", err)
		}

		notes := "some notes"
		if err := store.Patch(ctx, created.ID, Patch{Notes: &notes}); err != nil {
			t.Fatalf("Patch: %v", err)
		}
		if err := store.Close(ctx, created.ID, "fixed"); err != nil {
			t.Fatalf("Close: %v", err)
		}

		output, err := store.Render(ctx, created.ID)
		if err != nil {
			t.Fatalf("Render: %v", err)
		}

		// Semantic parity: output must contain all key fields.
		// Use strings.Contains, NOT exact format matching.
		checks := map[string]string{
			"title":        "render-parity-issue",
			"status":       string(StatusClosed),
			"type":         string(TypeBug),
			"priority":     "1", // PriorityHigh = 1
			"label":        "render-label",
			"description":  "the description body",
			"notes":        "some notes",
			"close reason": "fixed",
		}
		for field, needle := range checks {
			if !strings.Contains(output, needle) {
				t.Errorf("Render output missing %s (%q); got:\n%s", field, needle, output)
			}
		}
	})

	t.Run("Actor_scoping", func(t *testing.T) {
		// Actor flows into the store via the factory parameter, not the
		// process env (#98 env-isolation invariant).
		ctx := context.Background()
		store := factory("test-actor")

		_, err := store.Create(ctx, CreateParams{
			Title: "my-issue", Type: TypeTask, Assignee: "test-actor",
		})
		if err != nil {
			t.Fatalf("Create test-actor issue: %v", err)
		}
		_, err = store.Create(ctx, CreateParams{
			Title: "other-issue", Type: TypeTask, Assignee: "other-agent",
		})
		if err != nil {
			t.Fatalf("Create other-agent issue: %v", err)
		}

		got, err := store.List(ctx, Filter{IncludeAllAgents: false, Assignee: ""})
		if err != nil {
			t.Fatalf("List: %v", err)
		}

		for _, iss := range got {
			if iss.Assignee == "other-agent" {
				t.Errorf("Actor_scoping: List with IncludeAllAgents=false returned other-agent issue %q", iss.Title)
			}
		}
		titles := titlesOf(got)
		if !contains(titles, "my-issue") {
			t.Errorf("Actor_scoping: List did not return test-actor issue; got %v", titles)
		}
	})

	t.Run("actor_scoping_positive_and_negative", func(t *testing.T) {
		ctx := context.Background()

		// Seed under agent-a: parent epic + 3 children (a-owned, b-owned, empty).
		storeA := factory("agent-a")
		parent, err := storeA.Create(ctx, CreateParams{
			Title: "epic", Type: TypeEpic, Assignee: "agent-a",
		})
		if err != nil {
			t.Fatalf("Create epic: %v", err)
		}
		if _, err := storeA.Create(ctx, CreateParams{
			Title: "child-a", Type: TypeTask, Parent: parent.ID, Assignee: "agent-a",
		}); err != nil {
			t.Fatalf("Create child-a: %v", err)
		}
		if _, err := storeA.Create(ctx, CreateParams{
			Title: "child-b", Type: TypeTask, Parent: parent.ID, Assignee: "agent-b",
		}); err != nil {
			t.Fatalf("Create child-b: %v", err)
		}
		if _, err := storeA.Create(ctx, CreateParams{
			Title: "child-empty", Type: TypeTask, Parent: parent.ID, Assignee: "agent-a",
		}); err != nil {
			t.Fatalf("Create child-empty: %v", err)
		}

		// Positive + negative under default filter, as agent-a.
		gotA, err := storeA.List(ctx, Filter{Parent: parent.ID, IncludeAllAgents: false, Assignee: ""})
		if err != nil {
			t.Fatalf("List as agent-a: %v", err)
		}
		titlesA := titlesOf(gotA)
		if !contains(titlesA, "child-a") {
			t.Errorf("agent-a must see child-a under default filter; got %v", titlesA)
		}
		for _, iss := range gotA {
			if iss.Assignee == "agent-b" {
				t.Errorf("agent-a must NOT see agent-b's child under default filter; got %q", iss.Title)
			}
		}

		// Symmetry: re-seed under agent-b and mirror.
		// factory() returns a fresh store — re-seeding is required (factory per
		// adapter-contract returns a fresh, empty Store on every call).
		storeB := factory("agent-b")
		parentB, err := storeB.Create(ctx, CreateParams{
			Title: "epic", Type: TypeEpic, Assignee: "agent-b",
		})
		if err != nil {
			t.Fatalf("Create epic (agent-b run): %v", err)
		}
		if _, err := storeB.Create(ctx, CreateParams{
			Title: "child-a", Type: TypeTask, Parent: parentB.ID, Assignee: "agent-a",
		}); err != nil {
			t.Fatalf("Create child-a (agent-b run): %v", err)
		}
		if _, err := storeB.Create(ctx, CreateParams{
			Title: "child-b", Type: TypeTask, Parent: parentB.ID, Assignee: "agent-b",
		}); err != nil {
			t.Fatalf("Create child-b (agent-b run): %v", err)
		}
		if _, err := storeB.Create(ctx, CreateParams{
			Title: "child-empty", Type: TypeTask, Parent: parentB.ID, Assignee: "agent-b",
		}); err != nil {
			t.Fatalf("Create child-empty (agent-b run): %v", err)
		}

		gotB, err := storeB.List(ctx, Filter{Parent: parentB.ID, IncludeAllAgents: false, Assignee: ""})
		if err != nil {
			t.Fatalf("List as agent-b: %v", err)
		}
		titlesB := titlesOf(gotB)
		if !contains(titlesB, "child-b") {
			t.Errorf("agent-b must see child-b under default filter; got %v", titlesB)
		}
		for _, iss := range gotB {
			if iss.Assignee == "agent-a" {
				t.Errorf("agent-b must NOT see agent-a's child under default filter; got %q", iss.Title)
			}
		}

	})

	t.Run("Ready_actor_scoping", func(t *testing.T) {
		ctx := context.Background()
		store := factory("agent-a")

		epic, err := store.Create(ctx, CreateParams{
			Title: "epic", Type: TypeEpic, Assignee: "agent-a",
		})
		if err != nil {
			t.Fatalf("Create epic: %v", err)
		}
		_, err = store.Create(ctx, CreateParams{
			Title: "child-b", Type: TypeTask, Parent: epic.ID, Assignee: "agent-b",
		})
		if err != nil {
			t.Fatalf("Create child-b: %v", err)
		}

		result, err := store.Ready(ctx, Filter{MoleculeID: epic.ID})
		if err != nil {
			t.Fatalf("Ready: %v", err)
		}
		if len(result.Steps) != 0 {
			t.Errorf("Ready (actor=agent-a): len(Steps) = %d, want 0 (agent-b step hidden)", len(result.Steps))
		}
		if result.TotalSteps != 1 {
			t.Errorf("Ready (actor=agent-a): TotalSteps = %d, want 1 (total children not actor-filtered)", result.TotalSteps)
		}

		storeB := factory("agent-b")
		epicB, err := storeB.Create(ctx, CreateParams{
			Title: "epic", Type: TypeEpic, Assignee: "agent-b",
		})
		if err != nil {
			t.Fatalf("Create epic (agent-b store): %v", err)
		}
		_, err = storeB.Create(ctx, CreateParams{
			Title: "child-b", Type: TypeTask, Parent: epicB.ID, Assignee: "agent-b",
		})
		if err != nil {
			t.Fatalf("Create child-b (agent-b store): %v", err)
		}

		resultB, err := storeB.Ready(ctx, Filter{MoleculeID: epicB.ID})
		if err != nil {
			t.Fatalf("Ready (agent-b store): %v", err)
		}
		if len(resultB.Steps) != 1 {
			t.Errorf("Ready (actor=agent-b): len(Steps) = %d, want 1 (agent-b step visible to agent-b)", len(resultB.Steps))
		}
	})

	t.Run("ParentWithEmptyAssigneeRejected", func(t *testing.T) {
		ctx := context.Background()
		store := factory("agent-Z")
		parent, err := store.Create(ctx, CreateParams{Title: "epic", Type: TypeEpic, Assignee: "agent-Z"})
		if err != nil {
			t.Fatalf("Create epic: %v", err)
		}
		_, err = store.Create(ctx, CreateParams{Title: "child", Type: TypeTask, Parent: parent.ID, Assignee: ""})
		if err == nil {
			t.Fatalf("Create with Parent set and Assignee empty should have errored")
		}
		if !strings.Contains(strings.ToLower(err.Error()), "assignee") {
			t.Errorf("Expected error mentioning 'assignee' invariant, got: %v", err)
		}
	})

	t.Run("CallerOwnChildrenVisibleUnderNormalOverlay", func(t *testing.T) {
		ctx := context.Background()
		store := factory("alice")
		parent, err := store.Create(ctx, CreateParams{Title: "epic", Type: TypeEpic, Assignee: "alice"})
		if err != nil {
			t.Fatalf("Create epic: %v", err)
		}
		_, err = store.Create(ctx, CreateParams{Title: "child", Type: TypeTask, Parent: parent.ID, Assignee: "alice"})
		if err != nil {
			t.Fatalf("Create child: %v", err)
		}
		got, err := store.List(ctx, Filter{Parent: parent.ID})
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		if len(got) != 1 {
			t.Fatalf("Expected 1 child under normal overlay, got %d", len(got))
		}
	})

	t.Run("NonOwnerChildrenRemainInvisible", func(t *testing.T) {
		ctx := context.Background()
		storeBob := factory("bob")
		parent, err := storeBob.Create(ctx, CreateParams{Title: "epic", Type: TypeEpic, Assignee: "bob"})
		if err != nil {
			t.Fatalf("Create parent as bob: %v", err)
		}
		_, err = storeBob.Create(ctx, CreateParams{Title: "child", Type: TypeTask, Parent: parent.ID, Assignee: "bob"})
		if err != nil {
			t.Fatalf("Create child as bob: %v", err)
		}
		storeAlice := factory("alice")
		got, err := storeAlice.List(ctx, Filter{Parent: parent.ID})
		if err != nil {
			t.Fatalf("List as alice: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("Expected bob's child invisible to alice; got %d rows", len(got))
		}
	})

	t.Run("ExplicitAssigneeWinsOverActorOverlay", func(t *testing.T) {
		// AC-2 from #125: the explicit Filter.Assignee suppresses the actor
		// overlay; the overlay does not further filter when the caller
		// supplied an explicit Assignee. Cross-adapter equivalence pin —
		// memstore and mcpstore both run this assertion.
		ctx := context.Background()
		store := factory("agent-Y")

		// Direction 1 — positive: explicit Assignee for a DIFFERENT actor
		// than the store overlay. Today: memstore returns 0 (bug),
		// mcpstore returns 1 (correct). Post-fix: both return 1.
		seeded, err := store.Create(ctx, CreateParams{
			Title: "explicit-wins", Type: TypeTask, Assignee: "agent-X",
		})
		if err != nil {
			t.Fatalf("Create explicit-wins: %v", err)
		}
		got, err := store.List(ctx, Filter{
			Assignee:         "agent-X",
			IncludeAllAgents: false,
		})
		if err != nil {
			t.Fatalf("List Direction 1: %v", err)
		}
		if len(got) != 1 || got[0].ID != seeded.ID {
			t.Fatalf("Direction 1: explicit Assignee=agent-X with overlay actor=agent-Y must return the agent-X issue; got %d issues (%v)",
				len(got), titlesOf(got))
		}

		// Direction 2 — negative: explicit Assignee for an unmatched actor
		// returns nothing.
		got, err = store.List(ctx, Filter{
			Assignee:         "agent-Z",
			IncludeAllAgents: false,
		})
		if err != nil {
			t.Fatalf("List Direction 2: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("Direction 2: explicit Assignee=agent-Z must return zero issues (none seeded); got %d", len(got))
		}

		// Direction 3 — regression pin for AC-5 / C-4: when the caller
		// does NOT specify Assignee, the overlay still applies. Seed an
		// agent-Y-owned issue and confirm it (a) returns under no-Assignee
		// + IncludeAllAgents=false, (b) and the agent-X issue does NOT.
		if _, err := store.Create(ctx, CreateParams{
			Title: "agent-Y-own", Type: TypeTask, Assignee: "agent-Y",
		}); err != nil {
			t.Fatalf("Create agent-Y-own: %v", err)
		}
		got, err = store.List(ctx, Filter{IncludeAllAgents: false})
		if err != nil {
			t.Fatalf("List Direction 3: %v", err)
		}
		titles := titlesOf(got)
		if !contains(titles, "agent-Y-own") {
			t.Errorf("Direction 3: no-Assignee + actor=agent-Y must still return agent-Y's own issue; got %v", titles)
		}
		if contains(titles, "explicit-wins") {
			t.Errorf("Direction 3: no-Assignee + actor=agent-Y must NOT return agent-X's issue (overlay applies); got %v", titles)
		}
	})
}
