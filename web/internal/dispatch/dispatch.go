// Package dispatch is the read projection of `af dispatch status --json` for the web module.
//
// It mirrors the formschema reader idiom: one seam to the af binary (DispatchStatusJSON, read
// through the exec wrapper — never by importing internal/…), one decode into a struct that folds
// the success object and the {state,error} envelope together, and a branch on the .state shape
// rather than the process exit code (af read commands always exit 0 and encode failure as
// {"state":"error","error":"…"}).
//
// af-core already computes dispatcher liveness (dispatcher_running) and per-entry agent liveness
// (agent_running) inside the payload (internal/cmd/dispatch.go:545-557,591-621), so the View
// surfaces those directly — the web module needs no second tmux probe.
package dispatch

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// StatusReader yields the raw stdout of `af dispatch status --json`. exec.Wrapper satisfies it
// (via DispatchStatusJSON); tests inject a hermetic fake. This is the only seam between the reader
// and the af binary — the reader never spawns a process itself.
type StatusReader interface {
	DispatchStatusJSON(ctx context.Context) (string, error)
}

// Entry is one dispatched issue/PR, projected for the UI. Field names mirror the frozen af-core
// contract (dispatchStatusEntry, internal/cmd/dispatch.go, pinned by
// TestDispatchStatus_JSON_SchemaSnapshot) so the front end binds the same keys end-to-end. The
// module cannot import internal/… (Go's internal seal + the separate go.mod; compiler-enforced C-2
// decoupling), so this contract is hand-mirrored and must be kept in sync by hand.
type Entry struct {
	Issue        string    `json:"issue"`
	Agent        string    `json:"agent"`
	AgentRunning bool      `json:"agent_running"`
	ItemURL      string    `json:"item_url"`
	Source       string    `json:"source"`
	DispatchedAt time.Time `json:"dispatched_at"`

	// Workflow observability (issue #378 K9, additive — mirrors the af-core contract).
	// Populated only for workflow-dispatched entries; PhaseComplete is the real
	// instanceComplete() signal, NOT tmux session absence.
	Workflow      string `json:"workflow,omitempty"`
	Phase         string `json:"phase,omitempty"`
	PhaseComplete bool   `json:"phase_complete,omitempty"`
}

// Schedule is one recurring scheduled sling ("cron"), projected for the UI. Field names mirror the
// frozen af-core contract (cronStatusEntry, internal/cmd/dispatch.go:2248-2272 as of #610 Phase 4,
// pinned by TestDispatchStatus_JSON_SchemaSnapshot_Crons); like Entry it is hand-mirrored, not
// imported, and must be kept in sync by hand. Trust the type name over the line range: cites on this
// seam rot about every second af-core phase.
//
// LastFiredAt is a POINTER because encoding/json's omitempty has no effect on a struct-typed field:
// a value time.Time would decode an absent key to 0001-01-01T00:00:00Z and render a fire the factory
// never performed. It records SUCCESSFUL fires only, so an erroring schedule has a LastAttemptAt but
// no LastFiredAt. NextDueAt and LastAttemptAt carry no omitempty, mirroring af-core, because their
// zero value is the honest answer for a schedule that has never fired.
type Schedule struct {
	Name                string     `json:"name"`
	Agent               string     `json:"agent"`
	AgentRunning        bool       `json:"agent_running"`
	Every               string     `json:"every"`
	NextDueAt           time.Time  `json:"next_due_at"`
	LastFiredAt         *time.Time `json:"last_fired_at,omitempty"`
	LastOutcome         string     `json:"last_outcome,omitempty"` // "fired" | "skipped_busy" | "error" | ""
	LastDetail          string     `json:"last_detail,omitempty"`  // skip reason / error text
	LastAttemptAt       time.Time  `json:"last_attempt_at"`
	ConsecutiveFailures int        `json:"consecutive_failures,omitempty"`
}

// View is the UI-facing dispatch status: the dispatcher's own liveness plus the dispatched entries
// (sorted by issue key upstream, for deterministic rendering).
type View struct {
	DispatcherRunning bool      `json:"dispatcher_running"`
	Entries           []Entry   `json:"entries"`
	AssembledAt       time.Time `json:"assembled_at"`

	// Scheduled slings (issue #610 N13b, additive — mirrors the af-core contract). They are a
	// separate list from Entries on purpose: entries are item-keyed (<repo>#<n>), schedules are
	// name-keyed, and they arrive in the operator's config document order rather than sorted.
	// omitempty mirrors af-core, where it is what keeps a factory with no crons emitting the frozen
	// two-key top level.
	Schedules []Schedule `json:"schedules,omitempty"`
}

// dispatchStatusOutput re-declares the success shape of `af dispatch status --json`
// (dispatchStatusJSON, internal/cmd/dispatch.go) plus the {state,error} envelope keys, so one decode covers
// both the success object and the error envelope. Re-declared, NOT imported — the web module cannot
// reach internal/… (Go's internal seal + the separate go.mod; compiler-enforced C-2 decoupling).
//
// It now folds in TWO af-core contracts, dispatchStatusEntry through Entry and cronStatusEntry
// through Schedule (dispatchStatusJSON.Schedules, internal/cmd/dispatch.go:2315). A key this struct
// does not declare is discarded by encoding/json silently and without error, which is why every
// additive af-core key has to be chased here by hand.
//
// KNOWN UNMAPPED: af-core's dispatchStatusEntry also emits `recovery` (#596 K11); Entry does not
// carry it. Recorded rather than silently tolerated — it is the same drift this file's comments warn
// about, caught in the act.
type dispatchStatusOutput struct {
	State             string     `json:"state"`
	Error             string     `json:"error"`
	DispatcherRunning bool       `json:"dispatcher_running"`
	Entries           []Entry    `json:"entries"`
	Schedules         []Schedule `json:"schedules,omitempty"`
}

// Reader reads the dispatch status through a StatusReader.
type Reader struct {
	src StatusReader
	now func() time.Time
}

// New builds a Reader over the given source (production: an *exec.Wrapper).
func New(src StatusReader) *Reader {
	return &Reader{src: src, now: time.Now}
}

// Status returns the current dispatch view. It branches on the JSON .state (the error envelope),
// never on the exit code, and stamps AssembledAt for the front end's staleness clock.
func (r *Reader) Status(ctx context.Context) (View, error) {
	raw, err := r.src.DispatchStatusJSON(ctx)
	if err != nil {
		return View{}, fmt.Errorf("dispatch status: %w", err)
	}
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return View{}, fmt.Errorf("empty dispatch status payload")
	}

	var out dispatchStatusOutput
	if jerr := json.Unmarshal([]byte(trimmed), &out); jerr != nil {
		return View{}, fmt.Errorf("decode dispatch status: %w", jerr)
	}
	if out.State == "error" {
		return View{}, fmt.Errorf("dispatch status failed: %s", out.Error)
	}

	entries := out.Entries
	if entries == nil {
		entries = []Entry{} // serialize as [] not null, so the front end always iterates an array
	}
	return View{
		DispatcherRunning: out.DispatcherRunning,
		Entries:           entries,
		AssembledAt:       r.now(),
		// Passed through unnormalised, unlike Entries: omitempty elides a nil and an empty slice
		// alike, so a factory with no crons serves the same payload either way.
		Schedules: out.Schedules,
	}, nil
}
