//go:build integration

package mcpstore_test

import (
	"context"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// TestK4RecoveryFilter_ReturnsOpenExcludesClosed pins the Go↔Python parity
// (issue #392 R9) of the K4 formula-resume recovery filter against the REAL
// Python server.
//
// The filter `af up` uses to reconstruct a lost .runtime/hooked_formula pointer
// is List(Filter{Assignee, Labels:["formula-instance"]}). It relies on three
// composed semantics that must match between the Go memstore and the Python
// mcpstore server:
//   - explicit Assignee suppresses the actor overlay (self-scoped read, ADR-002);
//   - Labels are ANDed;
//   - the default (non-IncludeClosed) filter excludes terminal epics.
//
// This mirrors ExplicitAssigneeWinsOverActorOverlay (contract.go:1067)
// Direction 1 (the explicit Assignee wins) plus the K4-specific closed-exclusion
// twist: the OPEN formula-instance epic is returned, the CLOSED one is not.
//
// Skips cleanly when python3 / aiohttp / sqlalchemy are absent (mirrors
// mcpstore_test.go); CI's integration job provides the venv.
func TestK4RecoveryFilter_ReturnsOpenExcludesClosed(t *testing.T) {
	requirePython3WithDeps(t)
	root := newFactoryRoot(t)
	t.Cleanup(func() { terminateServer(root) })

	const agent = "agent-A"
	store, err := mcpstore.New(root, agent)
	if err != nil {
		t.Fatalf("mcpstore.New(%s, %q): %v", root, agent, err)
	}
	ctx := context.Background()

	// OPEN instance: a formula-instance epic with one open child step.
	open, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula: open",
		Labels:   []string{"formula-instance"},
		Assignee: agent,
	})
	if err != nil {
		t.Fatalf("create open epic: %v", err)
	}
	if _, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   open.ID,
		Title:    "step",
		Labels:   []string{"formula-step"},
		Assignee: agent,
	}); err != nil {
		t.Fatalf("create open child step: %v", err)
	}

	// CLOSED instance: a formula-instance epic that is then closed (terminal).
	closed, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula: closed",
		Labels:   []string{"formula-instance"},
		Assignee: agent,
	})
	if err != nil {
		t.Fatalf("create closed epic: %v", err)
	}
	if err := store.Close(ctx, closed.ID, "formula complete"); err != nil {
		t.Fatalf("close epic: %v", err)
	}

	// The K4 recovery filter against the REAL Python server.
	got, err := store.List(ctx, issuestore.Filter{
		Assignee: agent,
		Labels:   []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("List(recovery filter): %v", err)
	}

	var sawOpen, sawClosed bool
	for _, iss := range got {
		switch iss.ID {
		case open.ID:
			sawOpen = true
		case closed.ID:
			sawClosed = true
		}
	}
	if !sawOpen {
		t.Errorf("recovery filter must return the OPEN formula-instance epic %s; got %d issue(s): %+v", open.ID, len(got), got)
	}
	if sawClosed {
		t.Errorf("recovery filter must NOT return the CLOSED formula-instance epic %s (default filter excludes terminal)", closed.ID)
	}
}
