// Package memstore is an in-memory implementation of issuestore.Store used
// by unit tests so they pass without a live backend.
//
// The Render and RenderList methods produce a tests-only approximation and
// are NOT byte-compatible with any production renderer. Use them only for
// smoke-testing rendering logic.
package memstore

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

// Store is the in-memory issue store.
type Store struct {
	mu     sync.Mutex
	issues map[string]issuestore.Issue
	deps   map[string][]string // issueID → dependsOnIDs (forward edges)
	nextID int
	now    func() time.Time
	actor  string // empty string disables actor scoping
}

// New constructs an empty in-memory store with actor scoping disabled.
func New() *Store {
	return &Store{
		issues: map[string]issuestore.Issue{},
		deps:   map[string][]string{},
		now:    time.Now,
	}
}

// NewWithActor constructs an empty in-memory store whose List filter scopes
// to issues assigned to the given actor when IncludeAllAgents is false. Passing
// "" is equivalent to New() and disables the filter.
func NewWithActor(actor string) *Store {
	s := New()
	s.actor = actor
	return s
}

// nextIssueID generates a unique synthetic id of the form "mem-N".
// The mu lock must be held by the caller.
func (s *Store) nextIssueID() string {
	s.nextID++
	return fmt.Sprintf("mem-%d", s.nextID)
}

// createLocked is the shared write path used by Create and Seed.
// The mu lock must be held by the caller.
func (s *Store) createLocked(p issuestore.CreateParams) issuestore.Issue {
	id := s.nextIssueID()
	now := s.now().UTC()
	iss := issuestore.Issue{
		ID:          id,
		Title:       p.Title,
		Description: p.Description,
		Assignee:    p.Assignee,
		Type:        p.Type,
		Status:      issuestore.StatusOpen,
		Priority:    p.Priority,
		Labels:      append([]string(nil), p.Labels...), // copy; preserve order, no dedup
		Parent:      p.Parent,
		BlockedBy:   nil,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	s.issues[id] = iss
	return iss
}

// Get returns the issue with the given id. ErrNotFound (wrapped) if absent.
func (s *Store) Get(_ context.Context, id string) (issuestore.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss, ok := s.issues[id]
	if !ok {
		return issuestore.Issue{}, fmt.Errorf("get %s: %w", id, issuestore.ErrNotFound)
	}
	// Refresh BlockedBy from the dep graph so callers see live edges.
	if depIDs, ok := s.deps[id]; ok && len(depIDs) > 0 {
		refs := make([]issuestore.IssueRef, 0, len(depIDs))
		for _, d := range depIDs {
			refs = append(refs, issuestore.IssueRef{ID: d})
		}
		iss.BlockedBy = refs
	}
	return iss, nil
}

// List returns issues matching the filter.
//
// Statuses semantics:
//   - nil → all non-terminal statuses (open, hooked, pinned, in_progress)
//   - non-empty → exact OR of the listed statuses
//
// IncludeClosed admits closed/done in addition to whatever Statuses selected.
// IncludeAllAgents=false scopes the result to the actor configured on the store
// (via NewWithActor); when the store has no actor, the filter is disabled.
func (s *Store) List(_ context.Context, filter issuestore.Filter) ([]issuestore.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	// Stable order: by id (mem-1, mem-2, ...) for determinism in tests.
	ids := make([]string, 0, len(s.issues))
	for id := range s.issues {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	out := make([]issuestore.Issue, 0, len(ids))
	for _, id := range ids {
		iss := s.issues[id]
		if !s.matchesFilter(iss, filter) {
			continue
		}
		out = append(out, iss)
	}
	return out, nil
}

func (s *Store) matchesFilter(iss issuestore.Issue, f issuestore.Filter) bool {
	// Status filtering — see Filter doc for nil semantics.
	if len(f.Statuses) == 0 {
		// nil/empty: all non-terminal unless IncludeClosed
		if iss.Status.IsTerminal() && !f.IncludeClosed {
			return false
		}
	} else {
		// non-empty: must match one of the listed statuses (OR semantics)
		matched := false
		for _, want := range f.Statuses {
			if iss.Status == want {
				matched = true
				break
			}
		}
		if !matched {
			// IncludeClosed acts as "...also include terminal" — but only for
			// terminal statuses not already matched.
			if !(f.IncludeClosed && iss.Status.IsTerminal()) {
				return false
			}
		}
	}

	// Parent.
	if f.Parent != "" && iss.Parent != f.Parent {
		return false
	}
	// Type.
	if f.Type != "" && iss.Type != f.Type {
		return false
	}
	// Assignee — only matched explicitly if requested.
	if f.Assignee != "" && iss.Assignee != f.Assignee {
		return false
	}
	// Actor overlay: hides issues whose assignee is not the store's
	// configured actor. Bypassed in three cases:
	//   - the caller pinned an explicit Filter.Assignee (explicit wins;
	//     symmetric with mcpstore.go:209-214 — pinned by
	//     RunStoreContract.ExplicitAssigneeWinsOverActorOverlay);
	//   - IncludeAllAgents=true (operator opt-out per ADR-002);
	//   - the store has no actor (s.actor == "").
	if f.Assignee == "" && !f.IncludeAllAgents && iss.Assignee != "" && s.actor != "" && iss.Assignee != s.actor {
		return false
	}
	// Labels — ANDed.
	for _, want := range f.Labels {
		if !containsLabel(iss.Labels, want) {
			return false
		}
	}
	return true
}

func containsLabel(labels []string, want string) bool {
	for _, l := range labels {
		if l == want {
			return true
		}
	}
	return false
}

// Ready returns issues whose dependencies are all closed/done.
//
// MoleculeID, when non-empty, scopes the result to issues whose Parent
// matches. This is the adapter-neutral MoleculeID semantic; the contract
// test pins it across all adapters.
func (s *Store) Ready(_ context.Context, filter issuestore.Filter) (issuestore.ReadyResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	totalChildren := 0
	if filter.MoleculeID != "" {
		for _, iss := range s.issues {
			if iss.Parent == filter.MoleculeID {
				totalChildren++
			}
		}
	}

	ids := make([]string, 0, len(s.issues))
	for id := range s.issues {
		ids = append(ids, id)
	}
	sort.Strings(ids)

	steps := make([]issuestore.Issue, 0)
	for _, id := range ids {
		iss := s.issues[id]
		if iss.Status.IsTerminal() {
			continue
		}
		if filter.MoleculeID != "" && iss.Parent != filter.MoleculeID {
			continue
		}
		if !s.matchesFilter(iss, filter) {
			continue
		}
		ready := true
		for _, dep := range s.deps[id] {
			d, ok := s.issues[dep]
			if !ok || !d.Status.IsTerminal() {
				ready = false
				break
			}
		}
		if ready {
			steps = append(steps, iss)
		}
	}
	total := totalChildren
	if total == 0 {
		total = len(steps)
	}
	return issuestore.ReadyResult{
		Steps:      steps,
		TotalSteps: total,
		MoleculeID: filter.MoleculeID,
	}, nil
}

// Create adds a new issue.
//
// Enforces the data-plane invariant parent_id = '' OR assignee != '' as a
// pre-delegation guard (mirrors the SQLite CHECK added in schema v2). The
// check lives on Create, NOT createLocked, so Seed paths that must be able
// to represent pre-invariant fixture state keep their unguarded write path.
func (s *Store) Create(_ context.Context, params issuestore.CreateParams) (issuestore.Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if params.Parent != "" && params.Assignee == "" {
		return issuestore.Issue{}, fmt.Errorf(
			"create: parent-scoped bead requires non-empty Assignee (data-plane invariant)",
		)
	}
	return s.createLocked(params), nil
}

// Patch applies a partial update. Today only Notes is supported (Gotcha 11).
// Notes is assigned to the dedicated Notes field with last-writer-wins
// semantics; Description is never mutated.
func (s *Store) Patch(_ context.Context, id string, patch issuestore.Patch) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss, ok := s.issues[id]
	if !ok {
		return fmt.Errorf("patch %s: %w", id, issuestore.ErrNotFound)
	}
	if patch.Notes != nil {
		iss.Notes = *patch.Notes
	}
	iss.UpdatedAt = s.now().UTC()
	s.issues[id] = iss
	return nil
}

// Close transitions an issue into the closed state. If reason is non-empty,
// it is assigned to the dedicated CloseReason field; Description is never
// mutated.
func (s *Store) Close(_ context.Context, id string, reason string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss, ok := s.issues[id]
	if !ok {
		return fmt.Errorf("close %s: %w", id, issuestore.ErrNotFound)
	}
	iss.Status = issuestore.StatusClosed
	if reason != "" {
		iss.CloseReason = reason
	}
	iss.UpdatedAt = s.now().UTC()
	s.issues[id] = iss
	return nil
}

// DepAdd records issueID depends on dependsOnID. Both must already exist.
func (s *Store) DepAdd(_ context.Context, issueID, dependsOnID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.issues[issueID]; !ok {
		return fmt.Errorf("dep add %s: %w", issueID, issuestore.ErrNotFound)
	}
	if _, ok := s.issues[dependsOnID]; !ok {
		return fmt.Errorf("dep add target %s: %w", dependsOnID, issuestore.ErrNotFound)
	}
	s.deps[issueID] = append(s.deps[issueID], dependsOnID)
	return nil
}

// Render returns a tests-only human approximation of the issue.
//
// This output is for smoke-testing rendering paths in unit tests only.
func (s *Store) Render(_ context.Context, id string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss, ok := s.issues[id]
	if !ok {
		return "", fmt.Errorf("render %s: %w", id, issuestore.ErrNotFound)
	}
	var b strings.Builder
	fmt.Fprintf(&b, "ID: %s\nTitle: %s\nType: %s\nStatus: %s\nPriority: %d\n",
		iss.ID, iss.Title, iss.Type, iss.Status, iss.Priority)
	if len(iss.Labels) > 0 {
		fmt.Fprintf(&b, "Labels: %s\n", strings.Join(iss.Labels, ","))
	}
	if iss.Description != "" {
		fmt.Fprintf(&b, "\n%s\n", iss.Description)
	}
	if iss.Notes != "" {
		fmt.Fprintf(&b, "Notes: %s\n", iss.Notes)
	}
	if iss.CloseReason != "" {
		fmt.Fprintf(&b, "Close reason: %s\n", iss.CloseReason)
	}
	return b.String(), nil
}

// RenderList returns a tests-only human approximation of a list. NOT byte-
// compatible with real bd.
func (s *Store) RenderList(ctx context.Context, filter issuestore.Filter) (string, error) {
	issues, err := s.List(ctx, filter)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, iss := range issues {
		fmt.Fprintf(&b, "%s  %s  %s\n", iss.ID, iss.Status, iss.Title)
	}
	return b.String(), nil
}

// SetStatus directly assigns a lifecycle state to a seeded issue.
//
// Test-only helper used by RunStoreContract via the SetStatusFn callback.
// Production code MUST NOT call this — lifecycle transitions belong to the
// Store interface (Close, Patch, ...). The contract test uses it to seed
// fixtures across all six Status values per Gotcha 9 / D11 / C-1.
func (s *Store) SetStatus(id string, status issuestore.Status) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	iss, ok := s.issues[id]
	if !ok {
		return fmt.Errorf("set status %s: %w", id, issuestore.ErrNotFound)
	}
	iss.Status = status
	iss.UpdatedAt = s.now().UTC()
	s.issues[id] = iss
	return nil
}

// Compile-time check: *Store satisfies issuestore.Store.
var _ issuestore.Store = (*Store)(nil)
