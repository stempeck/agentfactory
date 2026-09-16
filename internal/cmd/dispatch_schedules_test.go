package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// scheduleClock is the instant every test in this file renders against. Fixed, so "due now"
// versus a future next-due is a property of the fixture and never of when the suite runs.
var scheduleClock = time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)

// firedSchedules returns the two-schedule shape ux.md §C1 illustrates: one that fired 38 minutes
// ago on a 4h cadence (so its next due time is still in the future), and one that has never fired.
func firedSchedules() []cronStatusEntry {
	fired := time.Date(2026, 8, 22, 20, 22, 0, 0, time.UTC)
	return []cronStatusEntry{
		{
			Name:          "financial-patrol-wake",
			Agent:         "financial-patrol",
			Every:         "4h",
			LastFiredAt:   &fired,
			LastAttemptAt: fired,
			LastOutcome:   cronOutcomeFired,
			NextDueAt:     fired.Add(4 * time.Hour),
		},
		{
			Name:  "weekly-pm",
			Agent: "product-manager",
			Every: "7d",
		},
	}
}

// TestFormatDispatchStatus_Schedules pins issue #610 Phase 4's human arm (design AC-7, AC-3's
// observability clause, ux.md §C1). It drives the PURE renderer, so there is no chdir, no tmux and
// no store — the schedules are passed in as data, which is the only way the never-fired and
// already-overdue cases are reachable deterministically.
//
// The negative control is not optional. TestFormatDispatchStatus (dispatch_test.go:490-496) asserts
// that a stopped, entry-less, CRON-LESS factory still prints exactly "No dispatched issues.", and a
// carrier-down warning gated on !running alone would pass every other case here and break that one.
func TestFormatDispatchStatus_Schedules(t *testing.T) {
	schedules := firedSchedules()

	t.Run("a crons-only factory renders the block despite zero dispatched issues", func(t *testing.T) {
		out := formatDispatchStatus(true, map[string]dispatchEntry{}, nil, nil, schedules, scheduleClock)

		// The early return at dispatch.go:2323-2326 must not swallow the block: a crons-only
		// factory has zero dispatched issues and real schedules, and that is the whole point.
		for _, want := range []string{
			"No dispatched issues.",
			"Schedules:",
			"financial-patrol-wake", "agent=financial-patrol", "every=4h",
			"last=2026-08-22T20:22:00Z (fired)", "next=2026-08-23T00:22:00Z",
			"weekly-pm", "agent=product-manager", "every=7d",
			"last=never", "next=due now",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q\ngot:\n%s", want, out)
			}
		}
	})

	t.Run("a stopped dispatcher with crons warns that no schedule will fire", func(t *testing.T) {
		out := formatDispatchStatus(false, map[string]dispatchEntry{}, nil, nil, schedules, scheduleClock)
		if !strings.Contains(out, "Dispatcher: STOPPED") {
			t.Errorf("want the dispatcher line\ngot:\n%s", out)
		}
		if !strings.Contains(out, cronsWontFireWarning) {
			t.Errorf("a stopped dispatcher with configured crons must say so\ngot:\n%s", out)
		}
		// The warning is a verdict ON the rows, so it trails them. Leading with it would put the
		// advisory above its own evidence.
		if strings.Index(out, cronsWontFireWarning) < strings.LastIndex(out, "weekly-pm") {
			t.Errorf("the warning must follow the schedule rows it is about\ngot:\n%s", out)
		}
	})

	t.Run("a running dispatcher with crons does not warn", func(t *testing.T) {
		out := formatDispatchStatus(true, map[string]dispatchEntry{}, nil, nil, schedules, scheduleClock)
		if strings.Contains(out, cronsWontFireWarning) {
			t.Errorf("a running dispatcher must not claim schedules will not fire\ngot:\n%s", out)
		}
	})

	// NEGATIVE CONTROL — protects dispatch_test.go:488 and :495.
	t.Run("no crons means no block and no warning, running or stopped", func(t *testing.T) {
		for _, running := range []bool{true, false} {
			out := formatDispatchStatus(running, map[string]dispatchEntry{}, nil, nil, nil, scheduleClock)
			if !strings.Contains(out, "No dispatched issues.") {
				t.Errorf("running=%v: the cron-less empty-entries line regressed\ngot:\n%s", running, out)
			}
			if strings.Contains(out, "Schedules:") {
				t.Errorf("running=%v: a cron-less factory must not grow a Schedules block\ngot:\n%s", running, out)
			}
			if strings.Contains(out, cronsWontFireWarning) {
				t.Errorf("running=%v: the warning must be gated on len(schedules)>0, not on !running\ngot:\n%s", running, out)
			}
		}
	})

	t.Run("the block renders alongside a populated entries table too", func(t *testing.T) {
		entries := map[string]dispatchEntry{
			"owner/repo#1": {Agent: "debugger", DispatchedAt: scheduleClock.Add(-10 * time.Minute), Source: "issue"},
		}
		out := formatDispatchStatus(true, entries, map[string]bool{"debugger": true}, nil, schedules, scheduleClock)
		for _, want := range []string{"ISSUE", "owner/repo#1", "Schedules:", "financial-patrol-wake"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q\ngot:\n%s", want, out)
			}
		}
	})

	t.Run("an overdue schedule reads as due now", func(t *testing.T) {
		overdue := firedSchedules()[:1]
		overdue[0].NextDueAt = scheduleClock.Add(-time.Minute)
		out := formatDispatchStatus(true, map[string]dispatchEntry{}, nil, nil, overdue, scheduleClock)
		if !strings.Contains(out, "next=due now") {
			t.Errorf("a schedule whose next due time has passed must read as due now\ngot:\n%s", out)
		}
	})

	// HIGH-4 truthfulness: last_fired_at records successful fires ONLY, so a schedule that has
	// errored on every attempt has no fire stamp at all. Rendering a bare "last=never" would make
	// it indistinguishable from a brand-new schedule that is simply waiting its turn.
	t.Run("a schedule that keeps failing does not masquerade as merely never-fired", func(t *testing.T) {
		broken := []cronStatusEntry{{
			Name:                "broken",
			Agent:               "patrolman",
			Every:               "1h",
			LastAttemptAt:       scheduleClock.Add(-5 * time.Minute),
			LastOutcome:         cronOutcomeError,
			LastDetail:          "sling failed: exit status 1",
			ConsecutiveFailures: 3,
		}}
		out := formatDispatchStatus(true, map[string]dispatchEntry{}, nil, nil, broken, scheduleClock)
		for _, want := range []string{
			"last=never (" + cronOutcomeError + ")",
			"sling failed: exit status 1",
			"3 consecutive failures",
		} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q\ngot:\n%s", want, out)
			}
		}
	})

	t.Run("a busy skip surfaces its reason", func(t *testing.T) {
		skipped := []cronStatusEntry{{
			Name:          "patrol",
			Agent:         "patrolman",
			Every:         "4h",
			LastAttemptAt: scheduleClock.Add(-time.Minute),
			LastOutcome:   cronOutcomeSkippedBusy,
			LastDetail:    "agent patrolman is busy",
		}}
		out := formatDispatchStatus(true, map[string]dispatchEntry{}, nil, nil, skipped, scheduleClock)
		for _, want := range []string{cronOutcomeSkippedBusy, "agent patrolman is busy"} {
			if !strings.Contains(out, want) {
				t.Errorf("missing %q\ngot:\n%s", want, out)
			}
		}
	})

	// LastDetail is engine-authored text carrying an err.Error(), which can be multi-line. One
	// schedule must stay one row: an unfolded detail would break the table AND let arbitrary error
	// text emit lines that look like status output the renderer never wrote.
	t.Run("a multi-line failure detail cannot forge extra rows", func(t *testing.T) {
		noisy := []cronStatusEntry{{
			Name:          "patrol",
			Agent:         "patrolman",
			Every:         "4h",
			LastAttemptAt: scheduleClock.Add(-time.Minute),
			LastOutcome:   cronOutcomeError,
			LastDetail:    "sling failed: exit status 1\nDispatcher: RUNNING\r\n  forged\tcolumns",
		}}
		out := formatDispatchStatus(false, map[string]dispatchEntry{}, nil, nil, noisy, scheduleClock)

		lines := strings.Split(strings.TrimRight(out, "\n"), "\n")
		// Dispatcher: STOPPED, No dispatched issues., blank, Schedules:, one row, the warning.
		if got, want := len(lines), 6; got != want {
			t.Errorf("output has %d lines, want %d — the detail must fold into its one row\ngot:\n%s", got, want, out)
		}
		// The detail is allowed to CONTAIN that text — it must never become a line of its own.
		for i, line := range lines[1:] {
			if strings.HasPrefix(line, "Dispatcher:") {
				t.Errorf("a failure detail forged a dispatcher line at line %d\ngot:\n%s", i+2, out)
			}
		}
		if !strings.Contains(out, "sling failed: exit status 1 Dispatcher: RUNNING forged columns") {
			t.Errorf("the detail must survive verbatim apart from folded whitespace\ngot:\n%s", out)
		}
	})

	// Schedules come from a config slice, so document order is already deterministic — they are
	// deliberately NOT sorted, unlike entries, which are sorted only because they come from a map.
	t.Run("schedules render in config order", func(t *testing.T) {
		out := formatDispatchStatus(true, map[string]dispatchEntry{}, nil, nil, schedules, scheduleClock)
		if strings.Index(out, "financial-patrol-wake") > strings.Index(out, "weekly-pm") {
			t.Errorf("schedules must render in the operator's document order\ngot:\n%s", out)
		}
	})
}

// TestComputeCronStatus covers the join between the operator's declared schedules and the engine's
// recorded outcomes — the derivation the renderers are handed and therefore cannot second-guess.
func TestComputeCronStatus(t *testing.T) {
	fired := time.Date(2026, 8, 22, 20, 22, 0, 0, time.UTC)

	t.Run("a schedule with no crons configured yields nil, not an empty slice", func(t *testing.T) {
		// Load-bearing for the JSON contract: omitempty elides a nil slice AND an empty one, but
		// returning nil documents the intent at the source.
		if got := computeCronStatus(t.TempDir(), nil, nil); got != nil {
			t.Errorf("computeCronStatus with no crons = %#v, want nil", got)
		}
	})

	t.Run("next due time is the last successful fire plus the cadence", func(t *testing.T) {
		dir := t.TempDir()
		if err := saveCronState(dir, &cronState{Crons: map[string]cronRecord{
			"patrol": {LastFiredAt: fired, LastAttemptAt: fired, LastOutcome: cronOutcomeFired, LastCheckAt: fired},
		}}); err != nil {
			t.Fatalf("saveCronState: %v", err)
		}
		got := computeCronStatus(dir, []config.CronSchedule{{Name: "patrol", Agent: "patrolman", Every: "4h"}}, nil)
		if len(got) != 1 {
			t.Fatalf("want 1 schedule, got %d", len(got))
		}
		if got[0].LastFiredAt == nil || !got[0].LastFiredAt.Equal(fired) {
			t.Errorf("last fired = %v, want %v", got[0].LastFiredAt, fired)
		}
		// This must mirror the engine's due predicate (dispatch.go:1406-1411) exactly, or status
		// tells the operator a different story than the dispatcher acts on.
		if want := fired.Add(4 * time.Hour); !got[0].NextDueAt.Equal(want) {
			t.Errorf("next due = %v, want %v", got[0].NextDueAt, want)
		}
	})

	// The anchor is the FIRE, not the attempt. A schedule that fired at 20:22 and was then skipped
	// at 21:52 is due 4h after the fire — anchoring on the attempt would silently push every
	// contended schedule further out every time it was skipped, and no other fixture can tell the
	// two stamps apart because a successful fire writes both at once.
	t.Run("next due is anchored to the last fire, not to a later attempt", func(t *testing.T) {
		// The engine reaches this record by firing at T, erroring at T+4h when the schedule came
		// due, then erroring again at T+5h once the backoff elapsed — so the fire and attempt
		// stamps diverge, and only the fire stamp may anchor the next due time. Every other
		// fixture here happens to seed them equal, which would let the derivation read either.
		dir := t.TempDir()
		if err := saveCronState(dir, &cronState{Crons: map[string]cronRecord{
			"patrol": {
				LastFiredAt:         fired,
				LastAttemptAt:       fired.Add(5 * time.Hour),
				LastOutcome:         cronOutcomeError,
				LastDetail:          "sling failed: exit status 1",
				ConsecutiveFailures: 2,
			},
		}}); err != nil {
			t.Fatalf("saveCronState: %v", err)
		}
		got := computeCronStatus(dir, []config.CronSchedule{{Name: "patrol", Agent: "patrolman", Every: "4h"}}, nil)
		if want := fired.Add(4 * time.Hour); !got[0].NextDueAt.Equal(want) {
			t.Errorf("next due = %v, want %v — anchored on the fire stamp, not the attempt", got[0].NextDueAt, want)
		}
	})

	t.Run("a schedule with no record is never-fired and due now", func(t *testing.T) {
		got := computeCronStatus(t.TempDir(), []config.CronSchedule{{Name: "fresh", Agent: "a", Every: "4h"}}, nil)
		if len(got) != 1 {
			t.Fatalf("want 1 schedule, got %d", len(got))
		}
		if got[0].LastFiredAt != nil {
			t.Errorf("last fired = %v, want nil so omitempty elides the key", got[0].LastFiredAt)
		}
		// Deriving next due by addition here would emit 0001-01-01T04:00:00Z — a stamp that looks
		// like data and reads to a consumer as overdue by two millennia.
		if !got[0].NextDueAt.IsZero() {
			t.Errorf("next due = %v, want the zero time for a schedule that has never fired", got[0].NextDueAt)
		}
	})

	t.Run("a failed attempt never advances the fire stamp", func(t *testing.T) {
		dir := t.TempDir()
		attempt := fired.Add(time.Hour)
		if err := saveCronState(dir, &cronState{Crons: map[string]cronRecord{
			"broken": {LastAttemptAt: attempt, LastOutcome: cronOutcomeError, LastDetail: "boom", ConsecutiveFailures: 2},
		}}); err != nil {
			t.Fatalf("saveCronState: %v", err)
		}
		got := computeCronStatus(dir, []config.CronSchedule{{Name: "broken", Agent: "a", Every: "4h"}}, nil)
		if got[0].LastFiredAt != nil {
			t.Errorf("last fired = %v, want nil — last_fired_at records successful fires only (HIGH-4)", got[0].LastFiredAt)
		}
		if got[0].LastOutcome != cronOutcomeError || got[0].LastDetail != "boom" || got[0].ConsecutiveFailures != 2 {
			t.Errorf("failure detail lost: %+v", got[0])
		}
		if !got[0].LastAttemptAt.Equal(attempt) {
			t.Errorf("last attempt = %v, want %v", got[0].LastAttemptAt, attempt)
		}
		// A non-zero attempt must not make the schedule look scheduled: deriving next-due from it
		// would emit 0001-01-01T04:00:00Z for a schedule that has never fired at all.
		if !got[0].NextDueAt.IsZero() {
			t.Errorf("next due = %v, want the zero time — an attempt is not a fire", got[0].NextDueAt)
		}
	})

	t.Run("an unusable cadence renders rather than aborting", func(t *testing.T) {
		dir := t.TempDir()
		if err := saveCronState(dir, &cronState{Crons: map[string]cronRecord{
			"broken": {LastFiredAt: fired, LastAttemptAt: fired, LastOutcome: cronOutcomeFired},
		}}); err != nil {
			t.Fatalf("saveCronState: %v", err)
		}
		// Unreachable through LoadDispatchConfig — validateCrons parses every cadence at load —
		// but a status renderer may never panic or abort on one.
		got := computeCronStatus(dir, []config.CronSchedule{{Name: "broken", Agent: "a", Every: "1h30m"}}, nil)
		if len(got) != 1 || got[0].Name != "broken" {
			t.Fatalf("a schedule with an unusable cadence must still be reported: %+v", got)
		}
		if !got[0].NextDueAt.IsZero() {
			t.Errorf("next due = %v, want the zero time when the cadence cannot be parsed", got[0].NextDueAt)
		}
	})

	t.Run("config is truth: an orphaned record never renders", func(t *testing.T) {
		dir := t.TempDir()
		if err := saveCronState(dir, &cronState{Crons: map[string]cronRecord{
			"deleted-schedule": {LastFiredAt: fired, LastOutcome: cronOutcomeFired},
		}}); err != nil {
			t.Fatalf("saveCronState: %v", err)
		}
		got := computeCronStatus(dir, []config.CronSchedule{{Name: "kept", Agent: "a", Every: "4h"}}, nil)
		if len(got) != 1 || got[0].Name != "kept" {
			t.Errorf("a record whose schedule was removed must leave no status trace: %+v", got)
		}
	})

	// foldCell is a DISPLAY concern and lives ~330 lines from here, so "fold it at the source too"
	// reads like a tidy-up. It is not: the name is the key that indexes .runtime/dispatch-crons.json
	// and the identity Phase 5's read model joins on, so folding it here would silently rename the
	// schedule in the machine contract while the human table looked unchanged.
	t.Run("the name and agent are carried raw: folding is display-only", func(t *testing.T) {
		forged := "evil\nDispatcher: STOPPED"
		got := computeCronStatus(t.TempDir(), []config.CronSchedule{{Name: forged, Agent: "a\tb", Every: "4h"}}, nil)
		if got[0].Name != forged {
			t.Errorf("name = %q, want the config spelling %q verbatim", got[0].Name, forged)
		}
		if got[0].Agent != "a\tb" {
			t.Errorf("agent = %q, want the config spelling verbatim", got[0].Agent)
		}
	})

	t.Run("agent liveness comes from the precomputed map", func(t *testing.T) {
		got := computeCronStatus(t.TempDir(),
			[]config.CronSchedule{{Name: "up", Agent: "live", Every: "4h"}, {Name: "down", Agent: "dead", Every: "4h"}},
			map[string]bool{"live": true})
		if !got[0].AgentRunning {
			t.Errorf("a live cron agent must report agent_running true")
		}
		if got[1].AgentRunning {
			t.Errorf("an absent cron agent must report agent_running false")
		}
	})
}

// newCronFactory builds a hermetic factory rooted at a temp dir, chdir'd into, with the given
// dispatch.json body. It returns the root and the recording fake so a test can drive
// runDispatchStatus end-to-end — the wiring the pure renderer tests above cannot reach.
func newCronFactory(t *testing.T, dispatchJSON string) (string, *fakeTmux) {
	t.Helper()
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	fake, _ := setupHermeticSessions(t)
	if dispatchJSON != "" {
		if err := os.WriteFile(filepath.Join(afDir, "dispatch.json"), []byte(dispatchJSON), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	origDir, _ := os.Getwd()
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chdir(origDir) })
	return dir, fake
}

// runStatus drives runDispatchStatus through the cobra output seam and returns its stdout.
func runStatus(t *testing.T, jsonOut bool) string {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	if jsonOut {
		_ = cmd.Flags().Set("json", "true")
	}
	var buf strings.Builder
	cmd.SetOut(&buf)
	if err := runDispatchStatus(cmd, nil); err != nil {
		t.Fatalf("runDispatchStatus(json=%v): %v", jsonOut, err)
	}
	return buf.String()
}

// TestDispatchStatus_CronAgentLiveness pins the one thing TestComputeCronStatus cannot: that
// runDispatchStatus actually POPULATES the liveness map with cron agents. The precompute loop it
// inherits walks dispatched ITEMS only, and a Go map read of a missing key yields false — so a
// schedule whose agent has never been sent a GitHub item would report agent_running:false while its
// session was up, and every other test in this package would stay green.
func TestDispatchStatus_CronAgentLiveness(t *testing.T) {
	_, fake := newCronFactory(t, `{"crons":[`+
		`{"name":"morning","agent":"patrolman","every":"4h"},`+
		`{"name":"evening","agent":"patrolman","every":"6h"},`+
		`{"name":"weekly","agent":"newbie","every":"7d"}]}`)
	live := session.SessionName("patrolman")
	fake.present[live] = true

	var got dispatchStatusJSON
	out := strings.TrimSpace(runStatus(t, true))
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(got.Schedules) != 3 {
		t.Fatalf("want 3 schedules, got %d (%q)", len(got.Schedules), out)
	}
	for _, i := range []int{0, 1} {
		if !got.Schedules[i].AgentRunning {
			t.Errorf("schedule %q agent_running = false, want true — its agent's session is up", got.Schedules[i].Name)
		}
	}
	if got.Schedules[2].AgentRunning {
		t.Errorf("schedule %q agent_running = true, want false — its agent has no session", got.Schedules[2].Name)
	}

	// The status command is a cheap, offline-friendly read: two schedules sharing an agent must
	// cost ONE probe, not one per schedule.
	probes := 0
	for _, op := range fake.ops {
		if op == "HasSession "+live {
			probes++
		}
	}
	if probes != 1 {
		t.Errorf("probed %q %d times, want exactly 1 — one probe per DISTINCT agent", live, probes)
	}
}

// TestDispatchStatus_TolerantConfigLoad pins the posture that a broken dispatch.json must never take
// `af dispatch status` down. LoadDispatchConfig fails four different ways — absent, unreadable,
// unparseable, and failing its own validation — and only the first is ErrNotFound, so a guard
// written as errors.Is(err, config.ErrNotFound) would break the status command on exactly the
// half-edited config an operator runs it to diagnose.
func TestDispatchStatus_TolerantConfigLoad(t *testing.T) {
	for _, tc := range []struct {
		name string
		body string
	}{
		{"absent", ""},
		{"unparseable", `{`},
		{"fails validation", `{"repos":[]}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			newCronFactory(t, tc.body)

			out := strings.TrimSpace(runStatus(t, true)) // a non-nil error would have failed here

			var top map[string]json.RawMessage
			if err := json.Unmarshal([]byte(out), &top); err != nil {
				t.Fatalf("stdout is not one JSON object: %v (%q)", err, out)
			}
			if _, ok := top["schedules"]; ok {
				t.Errorf("a config that would not load must yield no schedules key, got %q", out)
			}
			if _, ok := top["dispatcher_running"]; !ok {
				t.Errorf("the frozen top level must survive a broken config, got %q", out)
			}
		})
	}
}

// TestDispatchStatus_CronsOnlyFactory_EndToEnd is the AC-2 scenario as an operator meets it, and the
// guard that the human advisory never leaks into the machine contract: a warning printed on the
// --json path would make stdout unparseable for every consumer.
func TestDispatchStatus_CronsOnlyFactory_EndToEnd(t *testing.T) {
	newCronFactory(t, `{"crons":[{"name":"patrol","agent":"patrolman","every":"4h"}]}`)
	// The dispatcher session is absent by default in the hermetic fake, which is the carrier-down
	// case the warning exists for.

	human := runStatus(t, false)
	for _, want := range []string{"Dispatcher: STOPPED", "No dispatched issues.", "Schedules:", "patrol", "agent=patrolman", "every=4h", cronsWontFireWarning} {
		if !strings.Contains(human, want) {
			t.Errorf("human status missing %q\ngot:\n%s", want, human)
		}
	}

	jsonOut := strings.TrimSpace(runStatus(t, true))
	if strings.Contains(jsonOut, cronsWontFireWarning) {
		t.Errorf("the carrier-down warning must never reach --json stdout\ngot:\n%s", jsonOut)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(jsonOut), &top); err != nil {
		t.Fatalf("--json stdout must be exactly one JSON object: %v (%q)", err, jsonOut)
	}
	if _, ok := top["schedules"]; !ok {
		t.Errorf("a crons-bearing factory must emit the schedules key, got %q", jsonOut)
	}
}

// TestDispatchStatus_EntriesAndSchedulesCoexist closes the one gap every other JSON test leaves
// open. The two snapshot fixtures are each single-sided — one has dispatched entries and no crons,
// the other crons and no entries — so a regression that populated Schedules only on the
// empty-entries branch would ship green, even though that is precisely the mistake the human
// renderer needed an explicit early-return guard to avoid. A mixed factory is the only shape that
// can catch it.
func TestDispatchStatus_EntriesAndSchedulesCoexist(t *testing.T) {
	dir, _ := newCronFactory(t, `{"crons":[{"name":"patrol","agent":"patrolman","every":"4h"}]}`)
	if err := saveDispatchState(dir, &dispatchState{Dispatched: map[string]dispatchEntry{
		"42": {Agent: "coder", DispatchedAt: time.Now().Add(-time.Hour), ItemURL: "https://example.test/42", Source: "repo"},
	}}); err != nil {
		t.Fatalf("saveDispatchState: %v", err)
	}

	var top struct {
		Entries   []dispatchStatusEntry `json:"entries"`
		Schedules []cronStatusEntry     `json:"schedules"`
	}
	jsonOut := strings.TrimSpace(runStatus(t, true))
	if err := json.Unmarshal([]byte(jsonOut), &top); err != nil {
		t.Fatalf("--json stdout must be one JSON object: %v (%q)", err, jsonOut)
	}
	if len(top.Entries) != 1 {
		t.Errorf("want the dispatched entry to survive alongside the schedules, got %d\n%s", len(top.Entries), jsonOut)
	}
	if len(top.Schedules) != 1 {
		t.Errorf("want the schedule to survive alongside the entries, got %d\n%s", len(top.Schedules), jsonOut)
	}

	// The human table has the same two-sidedness: the schedules block hangs off the path AFTER the
	// entries table, not only off the no-entries early return.
	human := runStatus(t, false)
	for _, want := range []string{"42", "Schedules:", "patrol"} {
		if !strings.Contains(human, want) {
			t.Errorf("human status missing %q\ngot:\n%s", want, human)
		}
	}
}

// TestFormatDispatchStatus_ForgedCellsCannotBreakTheBlock pins the row invariant against the two
// operator-authored cells. validateCrons enforces non-empty and unique on a cron name, but no
// charset, so a name carrying a newline reaches the renderer through the sanctioned
// `af config dispatch set` path — and one schedule must still be one row.
func TestFormatDispatchStatus_ForgedCellsCannotBreakTheBlock(t *testing.T) {
	// The dispatcher is RUNNING, so a line reading "Dispatcher: STOPPED" can only have come out of
	// the forged name — there is no legitimate way for the renderer to emit it.
	out := formatDispatchStatus(true, map[string]dispatchEntry{}, nil, nil, []cronStatusEntry{{
		Name:  "evil\nDispatcher: STOPPED\n" + cronsWontFireWarning,
		Agent: "x\ty",
		Every: "1h",
	}}, scheduleClock)

	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "Dispatcher:") {
			t.Errorf("a forged cell produced a line that reads as status output: %q\ngot:\n%s", line, out)
		}
	}
	if strings.Contains(out, "\n"+cronsWontFireWarning) {
		t.Errorf("a forged cell reproduced the carrier-down advisory as its own line\ngot:\n%s", out)
	}
	// status, "No dispatched issues.", blank, "Schedules:", and exactly ONE row.
	if len(lines) != 5 {
		t.Errorf("one schedule must be one row: want 5 lines, got %d\n%s", len(lines), out)
	}
}
