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

// historyFilter selects every message ever delivered to this agent, read or unread. It is
// deliberately NOT built from listFilter: that filter's explicit Statuses is a pin (H-A R2), and
// IncludeClosed is documented as "also include terminal" only when Statuses is nil (memstore.go:131,
// store.py:203). Sharing a base between the two would put the pin one careless edit away from an
// inbox that shows read mail.
func (m *Mailbox) historyFilter() issuestore.Filter {
	return issuestore.Filter{
		Type:          issuestore.TypeTask,
		Labels:        []string{"mail:true"},
		Assignee:      identityToAddress(m.identity),
		IncludeClosed: true,
	}
}

// historyFilterSince is historyFilter bounded below by a creation time. A bare
// history read grows with the agent's whole life; a caller that only cares
// about one step's window (gate_flags) hands the step's start down to the store
// so the store returns that slice and not the archive (#679/T7). since is an
// RFC-3339 UTC bound; empty leaves the read unbounded (== historyFilter).
func (m *Mailbox) historyFilterSince(since string) issuestore.Filter {
	f := m.historyFilter()
	f.CreatedAfter = since
	return f
}

func (m *Mailbox) list(ctx context.Context, filter issuestore.Filter) ([]*Message, error) {
	issues, err := m.store.List(ctx, filter)
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

// List returns all unread messages for this agent.
func (m *Mailbox) List(ctx context.Context) ([]*Message, error) {
	return m.list(ctx, m.listFilter())
}

// ListAll returns every message delivered to this agent, read or unread.
//
// This is a reader's view, not an inbox: the CLI's `af mail inbox` is List and stays List (C8).
// It exists because "was this message ever sent" and "is this message still waiting" are different
// questions, and only the first one can be asked about the past — reading is destructive here
// (MarkRead closes, and Delete IS MarkRead), so an inbox read after the fact reports how diligent
// the agent was, not what happened to it.
func (m *Mailbox) ListAll(ctx context.Context) ([]*Message, error) {
	return m.list(ctx, m.historyFilter())
}

// ListAllSince is ListAll bounded below by a creation time: every message
// delivered to this agent at or after since, read or unread. It exists so the
// whole-history reader's view can be scoped to a single step's window at the
// store, rather than pulling the agent's entire mail archive on every read
// (#679/T7). since is an RFC-3339 UTC bound; an empty since is exactly ListAll.
func (m *Mailbox) ListAllSince(ctx context.Context, since string) ([]*Message, error) {
	return m.list(ctx, m.historyFilterSince(since))
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
