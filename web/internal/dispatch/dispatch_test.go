package dispatch

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStatus is a hermetic StatusReader double — it returns canned `af dispatch status --json`
// stdout (or a canned error) and never spawns a process.
type fakeStatus struct {
	out string
	err error
}

func (f fakeStatus) DispatchStatusJSON(ctx context.Context) (string, error) { return f.out, f.err }

var _ StatusReader = fakeStatus{}

// canned `af dispatch status --json` payload — the frozen 2-key/6-per-entry shape.
const dispatchJSON = `{"dispatcher_running":true,"entries":[` +
	`{"issue":"o/r#407","agent":"soldesign-plan","agent_running":true,"item_url":"https://x/407","source":"issue","dispatched_at":"2026-06-20T00:00:00Z"},` +
	`{"issue":"o/r#392","agent":"rootcause","agent_running":false,"item_url":"https://x/392","source":"issue","dispatched_at":"2026-06-20T01:00:00Z"}` +
	`]}`

func TestReader_Status_ParsesContract(t *testing.T) {
	r := New(fakeStatus{out: dispatchJSON})
	v, err := r.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !v.DispatcherRunning {
		t.Fatalf("DispatcherRunning = false, want true")
	}
	if len(v.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(v.Entries))
	}
	if v.Entries[0].Issue != "o/r#407" || v.Entries[0].Agent != "soldesign-plan" || !v.Entries[0].AgentRunning {
		t.Fatalf("entry[0] = %+v", v.Entries[0])
	}
	if v.Entries[1].AgentRunning {
		t.Fatalf("entry[1] agent_running should be false")
	}
	if !v.Entries[0].DispatchedAt.Equal(time.Date(2026, 6, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("dispatched_at not parsed: %v", v.Entries[0].DispatchedAt)
	}
}

// A read NEVER branches on exit code: a {"state":"error"} envelope (exit 0) must surface as an error.
func TestReader_Status_ErrorEnvelope(t *testing.T) {
	r := New(fakeStatus{out: `{"state":"error","error":"state file unreadable"}`})
	if _, err := r.Status(context.Background()); err == nil {
		t.Fatalf("an error envelope must surface as an error")
	}
}

// A wrapper-level error (e.g. af missing) is surfaced, not swallowed.
func TestReader_Status_WrapperError(t *testing.T) {
	r := New(fakeStatus{err: errors.New("boom")})
	if _, err := r.Status(context.Background()); err == nil {
		t.Fatalf("a wrapper error must surface")
	}
}

// Empty entries serialize as [], not nil (the front end always iterates an array).
func TestReader_Status_EmptyEntriesNonNil(t *testing.T) {
	r := New(fakeStatus{out: `{"dispatcher_running":false}`})
	v, err := r.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if v.Entries == nil {
		t.Fatalf("Entries should be non-nil empty slice")
	}
	if len(v.Entries) != 0 {
		t.Fatalf("Entries = %v, want empty", v.Entries)
	}
}
