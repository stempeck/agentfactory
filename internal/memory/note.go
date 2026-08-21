package memory

import "time"

// Status values. Transitions are monotone: a note leaves active exactly once. The read paths serve
// only active notes, so graduation and expiry are how the injected block shrinks — without
// anything being destroyed. The single route back is Reopen (store.go), taken only when a
// formula:<name>@<hash> graduation's recorded hash no longer matches the store formula it names —
// i.e. only when the store can PROVE the note's own claim false (Gap 11).
const (
	StatusActive    = "active"
	StatusGraduated = "graduated"
	StatusExpired   = "expired"
)

// Note types. The vocabulary is closed on purpose: it is what keeps the vault a journal of
// operational learnings rather than a second documentation system, and it is what the per-type
// TTLs key on.
const (
	TypeGotcha        = "gotcha"
	TypeModelBehavior = "model-behavior"
	TypeOps           = "ops"
	TypeOutcome       = "outcome"
	TypeImprovement   = "improvement"
)

// knownStatus and knownType decide whether a hand-edited value is one this package can act on.
// They are what turns "unservable" into "flagged": a status outside the vocabulary makes every
// read path drop the note, and a type outside it keys the wrong TTL, so a note carrying one has
// to come back Malformed rather than silently absent. An EMPTY value is not a violation — it is
// the documented unset state, and status specifically reads as active.

func knownStatus(s string) bool {
	switch s {
	case StatusActive, StatusGraduated, StatusExpired:
		return true
	}
	return false
}

func knownType(t string) bool {
	switch t {
	case TypeGotcha, TypeModelBehavior, TypeOps, TypeOutcome, TypeImprovement:
		return true
	}
	return false
}

// Note is one recorded learning: restricted frontmatter plus a Markdown body, stored as one
// file in the authoring agent's vault.
type Note struct {
	// ID is the note's address and its filename stem. It is sanitized on write and is always
	// resolved against a directory listing on read, never path-joined from caller input.
	ID string
	// Agent is the vault the note lives in — the claimed identity of whoever recorded it.
	Agent string
	// Formula is the optional scoping key a slice ranks on when a run is hooked to a formula.
	Formula string
	// Run is attribution: worktree/session. It records where a learning came from, which is the
	// only provenance available once the worktree it was learned in is gone.
	Run string
	// Type is one of the Type* constants and selects the TTL.
	Type string
	// Created is when the note was recorded, in UTC.
	Created time.Time
	// Evidence holds durable references the note rests on (issue, pr, commit).
	Evidence []string
	// Status is one of the Status* constants.
	Status string
	// GraduatedTo names where the learning went when it stopped being a note — restricted to
	// the durable-reference vocabulary, because free-text graduation to derived state silently
	// becomes false on the next redeploy.
	GraduatedTo string
	// Expires is the TTL stamp. The ZERO value means "no TTL" — the note is review-nagged
	// rather than expired. It must never be read as "expired at the epoch".
	Expires time.Time
	// Body is the Markdown text after the frontmatter, preserved verbatim.
	Body string
	// Malformed reports that the frontmatter failed the restricted dialect. Such a note is
	// served body-only to status so an operator can find and fix it, and is never served to
	// injection. It is a value, not an error: the parse happens on a hook path.
	Malformed bool
}

// Filter narrows a List. The ZERO VALUE reads every note in the agent's vault, including
// malformed ones — that is what a status surface and a teardown count want. Serving paths add
// Status and Formula. (Shape precedent: telemetry.Filter, internal/telemetry/store.go:44.)
type Filter struct {
	// Status matches Note.Status exactly. Empty means any. Note that a MALFORMED note parses
	// with Status "active" — its own status line is one of the things that could not be read —
	// so Filter{Status: StatusActive} includes malformed notes. That is right for a status
	// surface and wrong for a serving one, which is why Slice excludes them itself.
	Status string
	// Type matches Note.Type exactly. Empty means any.
	Type string
	// Formula matches Note.Formula exactly. Empty means any.
	Formula string
}

func (f Filter) matches(n Note) bool {
	if f.Status != "" && n.Status != f.Status {
		return false
	}
	if f.Type != "" && n.Type != f.Type {
		return false
	}
	if f.Formula != "" && n.Formula != f.Formula {
		return false
	}
	return true
}
