package issuestore

import (
	"context"
	"errors"
	"time"
)

// Store is the neutral interface every issue-store backend must implement.
//
// All methods take a context.Context as their first parameter so callers can
// propagate cancellation and deadlines into the underlying backend. mcpstore
// threads ctx through every JSON-RPC call to the Python server (C18 SIGTERM
// cleanliness); memstore honors ctx for cancellation.
//
// The two Render* methods are escape hatches that return human-readable
// output for `af bead show` / `af bead list`. The output is intended for
// terminal display only — DO NOT PARSE its contents. Programmatic callers
// must use Get/List, which return structured DTOs.
type Store interface {
	Get(ctx context.Context, id string) (Issue, error)
	List(ctx context.Context, filter Filter) ([]Issue, error)
	Ready(ctx context.Context, filter Filter) (ReadyResult, error)
	Create(ctx context.Context, params CreateParams) (Issue, error)
	Patch(ctx context.Context, id string, patch Patch) error
	Close(ctx context.Context, id string, reason string) error
	DepAdd(ctx context.Context, issueID, dependsOnID string) error

	// Render returns a human-readable rendering for `af bead show`.
	// Output is for display only — DO NOT PARSE.
	Render(ctx context.Context, id string) (string, error)

	// RenderList returns a human-readable listing for `af bead list`.
	// Output is for display only — DO NOT PARSE.
	RenderList(ctx context.Context, filter Filter) (string, error)
}

// Issue is the neutral DTO every backend produces.
//
// Labels preserves wire order with no dedup and no canonicalization (C13).
type Issue struct {
	ID          string     `json:"id"`
	Title       string     `json:"title"`
	Description string     `json:"description"`
	Assignee    string     `json:"assignee"`
	Type        IssueType  `json:"type"`
	Status      Status     `json:"status"`
	Priority    Priority   `json:"priority"`
	Labels      []string   `json:"labels"`
	Parent      string     `json:"parent,omitempty"`
	BlockedBy   []IssueRef `json:"blocked_by,omitempty"`
	Notes       string     `json:"notes"`
	CloseReason string     `json:"close_reason"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// IssueRef is a thin reference used in BlockedBy edges. Adapters populate
// these from the underlying store's blocked-by relation (D6, R-INT-6).
type IssueRef struct {
	ID string `json:"id"`
}

// IssueType enumerates the bd issue types.
type IssueType string

const (
	TypeTask    IssueType = "task"
	TypeEpic    IssueType = "epic"
	TypeBug     IssueType = "bug"
	TypeFeature IssueType = "feature"
	TypeGate    IssueType = "gate"
)

// Status enumerates the bd lifecycle states.
//
// The set is fixed by D11/C-1: open, hooked, pinned, in_progress, closed,
// done. Two of these (closed and done) are terminal — see IsTerminal.
type Status string

const (
	StatusOpen       Status = "open"
	StatusHooked     Status = "hooked"
	StatusPinned     Status = "pinned"
	StatusInProgress Status = "in_progress"
	StatusClosed     Status = "closed"
	StatusDone       Status = "done"
)

// IsTerminal reports whether s is a "completed lifecycle" state with no
// further transitions expected. Both closed and done are terminal.
//
// Mail's read/unread translation, and any future "is this issue done with"
// predicate, MUST go through this method instead of comparing against a
// single sentinel value (D11 / C-1 / cross-review round 1).
//
// Policy: a status is terminal iff it represents a completed lifecycle
// state with no further transitions expected. Future statuses are classified
// at definition time.
func (s Status) IsTerminal() bool {
	return s == StatusClosed || s == StatusDone
}

// Priority is the integer priority used by bd. Lower values are higher
// priority (urgent=0).
type Priority int

const (
	PriorityUrgent Priority = 0
	PriorityHigh   Priority = 1
	PriorityNormal Priority = 2
	PriorityLow    Priority = 3
)

// String returns the lowercase name of the priority. This is what
// fmt.Printf("%s", p) displays. Added in Phase 2 so the mail CLI can keep
// using %s format verbs after mail.Priority becomes an alias of this
// int-kinded type (Gotcha #2).
func (p Priority) String() string {
	switch p {
	case PriorityUrgent:
		return "urgent"
	case PriorityHigh:
		return "high"
	case PriorityNormal:
		return "normal"
	case PriorityLow:
		return "low"
	default:
		return "normal"
	}
}

// Filter describes a query against a Store.
//
// Statuses semantics (H-A R2, D14, Gotcha 12):
//   - nil: all non-terminal statuses (open, hooked, pinned, in_progress)
//   - non-empty slice: OR of the listed statuses (e.g. [StatusOpen,
//     StatusInProgress] returns both)
//
// Adapters MUST treat Statuses=nil as "all non-terminal" and MUST NOT
// collapse it to an empty filter (which would return zero results). The
// contract test pins this nil-vs-empty distinction.
//
// IncludeAllAgents and IncludeClosed split the historical `bd` CLI's
// overloaded `--all` flag into two independent axes (H-2/D13).
type Filter struct {
	Parent   string
	Statuses []Status
	Type     IssueType
	// Assignee, when non-empty, scopes the result to issues whose
	// Assignee equals this value AND suppresses the actor overlay
	// (the explicit caller value wins; the store's actor is not also
	// intersected). When empty, the actor overlay applies as configured
	// by IncludeAllAgents and the store's actor identity. Adapters
	// MUST agree on this — pinned by RunStoreContract's
	// ExplicitAssigneeWinsOverActorOverlay sub-test (#125).
	Assignee   string
	Labels     []string // ANDed
	MoleculeID string   // for Ready: triggers --no-daemon (C10)

	// IncludeAllAgents bypasses the actor overlay (cross-agent
	// visibility). Has NO effect when Assignee is non-empty — the
	// explicit Assignee already determines the result set; the overlay
	// (and therefore this opt-out) is bypassed in that case. See
	// Assignee field above and ADR-002 §"sanctioned opt-out."
	IncludeAllAgents bool
	IncludeClosed    bool // include closed/done in addition to non-terminal
}

// CreateParams describes a new issue to create.
type CreateParams struct {
	Title       string
	Description string
	Assignee    string
	Type        IssueType
	Parent      string
	Priority    Priority
	Labels      []string
	Actor       string
	Silent      bool
}

// Patch describes a partial update. Intentionally minimal: today only Notes
// is used (Gotcha 11). Future fields are additive.
type Patch struct {
	Notes *string
}

// ReadyResult is the structured output of Store.Ready.
type ReadyResult struct {
	Steps      []Issue
	TotalSteps int
	MoleculeID string
}

// ErrNotFound is the sentinel returned (wrapped) when a Store cannot locate
// an issue by id. Callers detect it with errors.Is.
var ErrNotFound = errors.New("issuestore: not found")
