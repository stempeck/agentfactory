package mail

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/stempeck/agentfactory/internal/issuestore"
)

var (
	ErrMessageNotFound = errors.New("message not found")
	ErrEmptyInbox      = errors.New("inbox is empty")
)

// Mailbox provides inbox operations for a single agent.
//
// The store is injected by the caller (typically cmd/mail.go constructs an
// mcpstore.MCPStore; tests inject memstore.New()).
type Mailbox struct {
	identity string
	store    issuestore.Store
}

// NewMailbox creates a Mailbox for the given agent identity backed by the
// provided Store.
func NewMailbox(identity string, store issuestore.Store) *Mailbox {
	return &Mailbox{identity: identity, store: store}
}

// listFilter is the canonical Filter for mail inbox queries. It pins H-A R2:
// Statuses is an explicit single-element slice []Status{StatusOpen}, NOT nil.
// The nil semantics ("all non-terminal") would surface hooked/pinned/
// in_progress mail in `af mail inbox` and violate C8 (af CLI surface
// unchanged). See outline Gotcha #4 and the H-A R2 pin in cross-review.
//
// IncludeAllAgents is intentionally NOT set: this is an own-mailbox read, not
// a cross-actor probe. The explicit Assignee suffices on both adapters —
// memstore's actor overlay is suppressed when an explicit Assignee is present,
// mirroring mcpstore. The cross-adapter invariant is pinned by
// RunStoreContract.ExplicitAssigneeWinsOverActorOverlay (issue #125).
func (m *Mailbox) listFilter() issuestore.Filter {
	return issuestore.Filter{
		Type:     issuestore.TypeTask,
		Labels:   []string{"mail:true"},
		Assignee: identityToAddress(m.identity),
		Statuses: []issuestore.Status{issuestore.StatusOpen}, // H-A R2
	}
}

// List returns all unread messages for this agent.
func (m *Mailbox) List(ctx context.Context) ([]*Message, error) {
	issues, err := m.store.List(ctx, m.listFilter())
	if err != nil {
		return nil, fmt.Errorf("listing messages: %w", err)
	}
	messages := make([]*Message, 0, len(issues))
	for _, iss := range issues {
		messages = append(messages, issueToMessage(iss))
	}
	sort.Slice(messages, func(i, j int) bool {
		return messages[i].Timestamp.After(messages[j].Timestamp)
	})
	return messages, nil
}

// Get retrieves a single message by ID.
func (m *Mailbox) Get(ctx context.Context, id string) (*Message, error) {
	iss, err := m.store.Get(ctx, id)
	if err != nil {
		if errors.Is(err, issuestore.ErrNotFound) {
			return nil, ErrMessageNotFound
		}
		return nil, fmt.Errorf("getting message %s: %w", id, err)
	}
	return issueToMessage(iss), nil
}

// MarkRead marks a message as read by closing it in the issue store.
// The mail convention is: closed == read.
func (m *Mailbox) MarkRead(ctx context.Context, id string) error {
	if err := m.store.Close(ctx, id, ""); err != nil {
		if errors.Is(err, issuestore.ErrNotFound) {
			return ErrMessageNotFound
		}
		return fmt.Errorf("marking read %s: %w", id, err)
	}
	return nil
}

// Delete removes a message (delegates to MarkRead — close = delete in the
// beads model, preserved across the Store migration).
func (m *Mailbox) Delete(ctx context.Context, id string) error {
	return m.MarkRead(ctx, id)
}

// Count returns the number of unread messages.
func (m *Mailbox) Count(ctx context.Context) (int, error) {
	issues, err := m.store.List(ctx, m.listFilter())
	if err != nil {
		return 0, fmt.Errorf("counting messages: %w", err)
	}
	return len(issues), nil
}
