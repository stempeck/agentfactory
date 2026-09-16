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

// dispatchSchedulesJSON is the real `af dispatch status --json` payload as of #610 Phase 4. It
// carries all three per-schedule shapes af-core freezes in TestDispatchStatus_JSON_SchemaSnapshot_Crons
// — fired (8 keys), never-fired (6), erroring (9) — in af-core's config document order. Keeping the
// whole emitted shape rather than only the new keys is deliberate: the fixture doubles as the record
// of what af actually emits.
const dispatchSchedulesJSON = `{"dispatcher_running":true,"entries":[],"schedules":[` +
	`{"name":"patrol","agent":"patrolman","agent_running":true,"every":"4h",` +
	`"next_due_at":"2023-11-15T02:13:20Z","last_fired_at":"2023-11-14T22:13:20Z",` +
	`"last_outcome":"fired","last_attempt_at":"2023-11-14T22:13:20Z"},` +
	`{"name":"fresh","agent":"newbie","agent_running":false,"every":"7d",` +
	`"next_due_at":"0001-01-01T00:00:00Z","last_attempt_at":"0001-01-01T00:00:00Z"},` +
	`{"name":"broken","agent":"patrolman","agent_running":true,"every":"1h",` +
	`"next_due_at":"0001-01-01T00:00:00Z","last_attempt_at":"2023-11-14T22:13:20Z",` +
	`"last_outcome":"error","last_detail":"boom","consecutive_failures":2}` +
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

// Scheduled slings (#610 N13b). This test exists because the af↔web contract is a hand-mirrored
// STRING schema and encoding/json discards an undeclared key silently and without error, so the only
// thing between a mistyped tag and an invisible schedule is an assertion on the value it decoded to.
// Every assertion below fails if a field is dropped from Schedule or from the View construction,
// which is the property that makes this a guard rather than decoration.
func TestReader_Status_ParsesSchedules(t *testing.T) {
	r := New(fakeStatus{out: dispatchSchedulesJSON})
	v, err := r.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !v.DispatcherRunning || v.Entries == nil || len(v.Entries) != 0 {
		t.Fatalf("the additive schedules field disturbed the existing copy-through: %+v", v)
	}
	if len(v.Schedules) != 3 {
		t.Fatalf("schedules = %d, want 3 — af-core's top-level `schedules` array must land in View.Schedules", len(v.Schedules))
	}
	// Config document order, not sorted: schedules come from a config slice, unlike Entries which
	// af-core sorts by issue key.
	for i, want := range []string{"patrol", "fresh", "broken"} {
		if got := v.Schedules[i].Name; got != want {
			t.Errorf("schedules[%d].Name = %q, want %q — af-core emits schedules in the operator's document order", i, got, want)
		}
	}

	fired := time.Date(2023, 11, 14, 22, 13, 20, 0, time.UTC)

	p := v.Schedules[0]
	if p.Agent != "patrolman" || p.Every != "4h" {
		t.Errorf("schedules[0] agent/every = %q/%q, want patrolman/4h", p.Agent, p.Every)
	}
	// Asserting TRUE is what makes the agent_running tag load-bearing: false is the zero value, so a
	// broken tag would satisfy the opposite assertion.
	if !p.AgentRunning {
		t.Error("schedules[0].AgentRunning = false, want true")
	}
	if p.LastFiredAt == nil || !p.LastFiredAt.Equal(fired) {
		t.Errorf("schedules[0].LastFiredAt = %v, want %v", p.LastFiredAt, fired)
	}
	if want := fired.Add(4 * time.Hour); !p.NextDueAt.Equal(want) {
		t.Errorf("schedules[0].NextDueAt = %v, want %v", p.NextDueAt, want)
	}
	if !p.LastAttemptAt.Equal(fired) {
		t.Errorf("schedules[0].LastAttemptAt = %v, want %v", p.LastAttemptAt, fired)
	}
	if p.LastOutcome != "fired" || p.LastDetail != "" || p.ConsecutiveFailures != 0 {
		t.Errorf("schedules[0] outcome/detail/failures = %q/%q/%d, want \"fired\"/\"\"/0", p.LastOutcome, p.LastDetail, p.ConsecutiveFailures)
	}

	// The load-bearing assertion of the whole mirror: a schedule that has never fired omits
	// last_fired_at, and a value-typed time.Time would decode that absence to 0001-01-01T00:00:00Z —
	// rendering a fire the factory never performed. Only a pointer can say "never".
	f := v.Schedules[1]
	if f.LastFiredAt != nil {
		t.Errorf("schedules[1].LastFiredAt = %v, want nil — `fresh` has never fired", *f.LastFiredAt)
	}
	if f.Every != "7d" || f.LastOutcome != "" || f.AgentRunning {
		t.Errorf("schedules[1] every/outcome/agent_running = %q/%q/%v, want \"7d\"/\"\"/false", f.Every, f.LastOutcome, f.AgentRunning)
	}
	// These two carry no omitempty in af-core because zero IS the honest answer for a schedule that
	// has never fired; they must decode as zero rather than be absent from the mirror.
	if !f.NextDueAt.IsZero() || !f.LastAttemptAt.IsZero() {
		t.Errorf("schedules[1] next_due_at/last_attempt_at = %v/%v, want both zero", f.NextDueAt, f.LastAttemptAt)
	}

	// An erroring schedule records an ATTEMPT, never a fire, and next-due stays anchored on fires.
	b := v.Schedules[2]
	if b.LastFiredAt != nil {
		t.Errorf("schedules[2].LastFiredAt = %v, want nil — a failed attempt is not a fire", *b.LastFiredAt)
	}
	if b.LastOutcome != "error" || b.LastDetail != "boom" || b.ConsecutiveFailures != 2 {
		t.Errorf("schedules[2] outcome/detail/failures = %q/%q/%d, want \"error\"/\"boom\"/2", b.LastOutcome, b.LastDetail, b.ConsecutiveFailures)
	}
	if !b.LastAttemptAt.Equal(fired) || !b.NextDueAt.IsZero() {
		t.Errorf("schedules[2] last_attempt_at/next_due_at = %v/%v, want %v/zero", b.LastAttemptAt, b.NextDueAt, fired)
	}

	// The field is additive: a factory with no crons emits no `schedules` key at all, and the reader
	// must neither fabricate a schedule nor fail.
	t.Run("absent schedules key yields no schedules", func(t *testing.T) {
		v, err := New(fakeStatus{out: dispatchJSON}).Status(context.Background())
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if len(v.Schedules) != 0 {
			t.Fatalf("Schedules = %+v, want none for a payload with no `schedules` key", v.Schedules)
		}
		if len(v.Entries) != 2 {
			t.Fatalf("entries = %d, want 2 — the additive field must not disturb the existing decode", len(v.Entries))
		}
	})
}
