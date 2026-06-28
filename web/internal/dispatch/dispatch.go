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

// View is the UI-facing dispatch status: the dispatcher's own liveness plus the dispatched entries
// (sorted by issue key upstream, for deterministic rendering).
type View struct {
	DispatcherRunning bool      `json:"dispatcher_running"`
	Entries           []Entry   `json:"entries"`
	AssembledAt       time.Time `json:"assembled_at"`
}

// dispatchStatusOutput re-declares the success shape of `af dispatch status --json`
// (dispatchStatusJSON, internal/cmd/dispatch.go) plus the {state,error} envelope keys, so one decode covers
// both the success object and the error envelope. Re-declared, NOT imported — the web module cannot
// reach internal/… (Go's internal seal + the separate go.mod; compiler-enforced C-2 decoupling).
type dispatchStatusOutput struct {
	State             string  `json:"state"`
	Error             string  `json:"error"`
	DispatcherRunning bool    `json:"dispatcher_running"`
	Entries           []Entry `json:"entries"`
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
	}, nil
}
