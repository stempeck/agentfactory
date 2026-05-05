package mail

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

func TestNewMailbox(t *testing.T) {
	store := memstore.New()
	mbox := NewMailbox("manager", store)
	if mbox == nil {
		t.Fatal("NewMailbox returned nil")
	}
	if mbox.identity != "manager" {
		t.Errorf("identity = %q, want %q", mbox.identity, "manager")
	}
	if mbox.store == nil {
		t.Error("store should not be nil")
	}
}

func TestMailbox_ListIgnoresNonOpenStatuses(t *testing.T) {
	store := memstore.New()
	// Seed one fixture per non-terminal status + one closed, all matching
	// the mail label/type/assignee filter. Only the StatusOpen fixture
	// must appear in List's output.
	statuses := []issuestore.Status{
		issuestore.StatusOpen,
		issuestore.StatusHooked,
		issuestore.StatusPinned,
		issuestore.StatusInProgress,
		issuestore.StatusClosed,
	}
	fixtures := make([]issuestore.CreateParams, len(statuses))
	for i := range statuses {
		fixtures[i] = issuestore.CreateParams{
			Title:    fmt.Sprintf("msg %d", i),
			Assignee: "test",
			Type:     issuestore.TypeTask,
			Labels:   []string{"mail:true", "from:sender", "to:test", "thread:t", "msg-type:task"},
		}
	}
	if err := store.SeedAt(statuses, fixtures...); err != nil {
		t.Fatalf("SeedAt: %v", err)
	}

	mbox := NewMailbox("test", store)
	msgs, err := mbox.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 open message, got %d", len(msgs))
	}
	if msgs[0].Subject != "msg 0" {
		t.Errorf("expected the open fixture, got %q", msgs[0].Subject)
	}
}

func TestMailbox_ListEmpty(t *testing.T) {
	store := memstore.New()
	mbox := NewMailbox("test", store)
	msgs, err := mbox.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if msgs == nil {
		t.Error("expected non-nil slice, got nil")
	}
	if len(msgs) != 0 {
		t.Errorf("expected 0 messages, got %d", len(msgs))
	}
}

// TestMailbox_ListSortsNewestFirst pins the newest-first ordering of
// Mailbox.List. Replaces the deleted TestParseMessageListSortsNewestFirst;
// exercises the sort at mailbox.go:61-63 via the full List path against a
// memstore. memstore.List returns fixtures in creation order (mem-1 before
// mem-2), so without the newest-first sort the "older" fixture would come
// first — the assertion below catches that regression.
func TestMailbox_ListSortsNewestFirst(t *testing.T) {
	store := memstore.New()

	// Seed the older fixture first.
	if err := store.Seed(issuestore.CreateParams{
		Title:    "older",
		Assignee: "test",
		Type:     issuestore.TypeTask,
		Labels:   []string{"mail:true", "from:sender", "to:test", "thread:t1", "msg-type:task"},
	}); err != nil {
		t.Fatalf("Seed older: %v", err)
	}

	// Sleep so the newer fixture gets a strictly later CreatedAt stamp.
	// memstore's createLocked uses s.now().UTC() per call; a small sleep
	// guarantees a monotonic gap even on fast hardware.
	time.Sleep(2 * time.Millisecond)

	if err := store.Seed(issuestore.CreateParams{
		Title:    "newer",
		Assignee: "test",
		Type:     issuestore.TypeTask,
		Labels:   []string{"mail:true", "from:sender", "to:test", "thread:t2", "msg-type:task"},
	}); err != nil {
		t.Fatalf("Seed newer: %v", err)
	}

	mbox := NewMailbox("test", store)
	msgs, err := mbox.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Subject != "newer" {
		t.Errorf("msgs[0].Subject = %q, want %q (newest should be first)", msgs[0].Subject, "newer")
	}
	if msgs[1].Subject != "older" {
		t.Errorf("msgs[1].Subject = %q, want %q", msgs[1].Subject, "older")
	}
	if !msgs[0].Timestamp.After(msgs[1].Timestamp) {
		t.Errorf("msgs[0].Timestamp (%v) should be after msgs[1].Timestamp (%v)",
			msgs[0].Timestamp, msgs[1].Timestamp)
	}
}

// TestMailbox_ListWorks_WithoutIncludeAllAgents_AcrossActorAddressing pins the
// post-#125 invariant that an own-mailbox read against a memstore whose actor
// differs from the mailbox identity returns the seeded mail without needing
// the IncludeAllAgents=true workaround. Before #125 Phase 1, memstore's actor
// overlay (memstore.go) would silently drop every issue whose Assignee did not
// match s.actor — masked in production by the IncludeAllAgents=true escape
// hatch in Mailbox.listFilter. Phase 1 added the `f.Assignee == ""` guard so
// an explicit Assignee filter suppresses the overlay on both adapters; Phase 2
// removes the workaround. This test exercises the differing-actor path that
// none of the other mail tests hit (they all use memstore.New(), which has
// s.actor == "" and short-circuits the overlay check). If Phase 1 were
// reverted, this test would fail — that is its RED proof.
func TestMailbox_ListWorks_WithoutIncludeAllAgents_AcrossActorAddressing(t *testing.T) {
	store := memstore.NewWithActor("dispatcher-actor")
	if err := store.Seed(issuestore.CreateParams{
		Title:    "to:test message",
		Assignee: identityToAddress("test-agent"),
		Type:     issuestore.TypeTask,
		Labels:   []string{"mail:true", "from:dispatcher", "to:test-agent", "thread:t", "msg-type:task"},
	}); err != nil {
		t.Fatalf("Seed: %v", err)
	}

	mbox := NewMailbox("test-agent", store)
	msgs, err := mbox.List(context.Background())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %d", len(msgs))
	}
	if msgs[0].Subject != "to:test message" {
		t.Errorf("msgs[0].Subject = %q, want %q", msgs[0].Subject, "to:test message")
	}
}
