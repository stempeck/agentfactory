package statusline

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Fixtures and assertions for the K2 occupancy reader (issue #596, design-doc.md:309-312).
//
// The invariant every test here defends: a channel with no trustworthy datum must NEVER read as
// healthy. Absent, malformed, stale, dark, suppressed and forged all resolve to a non-fresh state,
// and none of them may surface an occupancy percentage — because a 0% reading on a wedged agent is
// worse than no reading at all (AC-5). Legacy v1 files are covered explicitly (Gotcha 12) so a
// future contributor cannot "fix" them into a healthy default.

const (
	testStaleness = 180 * time.Second // Phase 0B default, passed as a parameter — never read from config here
	testDarkAfter = 600 * time.Second
)

func testOpts(agents ...string) ReadOptions {
	known := make(map[string]struct{}, len(agents))
	for _, a := range agents {
		known[a] = struct{}{}
	}
	return ReadOptions{KnownAgents: known, Staleness: testStaleness, DarkAfter: testDarkAfter}
}

// writeRawJSON writes an arbitrary body, so a fixture can express "this key is absent" — something
// a sessionSnapshot struct literal cannot do.
func writeRawJSON(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

// writeRawJSONFromSnapshot writes a well-formed snapshot under an ARBITRARY filename, so the
// filename-grammar cases can pair a valid body with a name the reader must reject.
func writeRawJSONFromSnapshot(t *testing.T, dir, name string, snap sessionSnapshot) {
	t.Helper()
	raw, err := json.Marshal(snap)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	writeRawJSON(t, dir, name, string(raw))
}

// v2Snapshot builds a well-formed schema-v2 fixture for one session.
func v2Snapshot(sessionID, agent string, writtenAt time.Time, pct float64) sessionSnapshot {
	used, total := int64(100000), int64(200000)
	p := pct
	return sessionSnapshot{
		Schema: schemaVersion, SessionID: sessionID, Date: writtenAt.Format(dateLayout),
		Agent: agent, WrittenAt: writtenAt.UTC().Format(time.RFC3339),
		ContextUsedPct: &p, ContextTokensUsed: &used, ContextTokensTotal: &total,
		UpdatedAt: writtenAt.UTC().Format(time.RFC3339),
	}
}

func mustReading(t *testing.T, got map[string]ChannelReading, agent string) ChannelReading {
	t.Helper()
	r, ok := got[agent]
	if !ok {
		t.Fatalf("no reading returned for known agent %q; every known agent must appear in the map", agent)
	}
	return r
}

// assertNoDatum is the shared AC-5 assertion: a non-fresh state must expose no occupancy at all.
// Asserting "not fresh" alone is insufficient — the defect being prevented is a reading that is
// correctly labelled yet still hands a caller a 0 it can act on.
func assertNoDatum(t *testing.T, r ChannelReading, context string) {
	t.Helper()
	if r.IsHealthy() {
		t.Errorf("%s: reading is healthy, want not healthy", context)
	}
	if pct, ok := r.UsedPct(); ok {
		t.Errorf("%s: reading surfaced occupancy %v; a channel with no trustworthy datum must expose none", context, pct)
	}
	if _, ok := r.Observation(); ok {
		t.Errorf("%s: reading carries an Observation; there is no validated datum behind it", context)
	}
}

// TestClassifyChannel_ThreeStateMatrix pins the fresh/stale/dark/none/malformed boundaries against
// the INJECTED clock, at the Phase 0B thresholds (staleness 180s, dark-grace 600s).
func TestClassifyChannel_ThreeStateMatrix(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	cases := []struct {
		name string
		age  time.Duration
		want ChannelState
	}{
		{"JustWritten", 5 * time.Second, StateFresh},
		{"AtStalenessBoundary", 180 * time.Second, StateFresh},
		{"JustPastStaleness", 181 * time.Second, StateStale},
		{"AtDarkBoundary", 600 * time.Second, StateStale},
		{"JustPastDarkGrace", 601 * time.Second, StateDark},
		{"LongDark", 6 * time.Hour, StateDark},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRawSnapshot(t, dir, v2Snapshot("s1", "manager", now.Add(-tc.age), 42))

			got, err := ReadObservations(dir, testOpts("manager"), now)
			if err != nil {
				t.Fatalf("ReadObservations: %v", err)
			}
			r := mustReading(t, got, "manager")
			if r.State() != tc.want {
				t.Errorf("state = %q, want %q at age %s", r.State(), tc.want, tc.age)
			}
			if tc.want == StateFresh && !r.IsHealthy() {
				t.Error("a fresh reading must report healthy")
			}
			if tc.want != StateFresh && r.IsHealthy() {
				t.Errorf("state %q must not report healthy", r.State())
			}
			// fresh/stale/dark all rest on a real datum, so occupancy must be readable —
			// the dark-at-high-occupancy branch in Phase 2 depends on the last reading.
			if pct, ok := r.UsedPct(); !ok || !approx(pct, 42) {
				t.Errorf("UsedPct() = (%v, %v), want (42, true)", pct, ok)
			}
		})
	}

	t.Run("KnownAgentWithNoFileIsNone", func(t *testing.T) {
		got, err := ReadObservations(t.TempDir(), testOpts("manager"), now)
		if err != nil {
			t.Fatalf("ReadObservations: %v", err)
		}
		r := mustReading(t, got, "manager")
		if r.State() != StateNone {
			t.Errorf("state = %q, want %q", r.State(), StateNone)
		}
		assertNoDatum(t, r, "agent that has never rendered")
	})

	// Named for what it asserts: an undecodable file cannot be attributed to any agent (its body
	// never parsed, so its `agent` claim is unreadable), which leaves that agent at `none`. Both
	// none and malformed are dark-class; the load-bearing property is that neither is healthy.
	t.Run("UnparseableJSONNeverReadsHealthy", func(t *testing.T) {
		dir := t.TempDir()
		writeRawJSON(t, dir, "s1.json", `{"schema":2,"agent":"manager",`)

		got, err := ReadObservations(dir, testOpts("manager"), now)
		if err != nil {
			t.Fatalf("ReadObservations: %v", err)
		}
		r := mustReading(t, got, "manager")
		// A file that cannot be decoded cannot be attributed either, so the agent has no
		// evidence at all — none. What matters is that it is never healthy.
		if r.IsHealthy() {
			t.Errorf("undecodable file produced a healthy reading (%q)", r.State())
		}
		assertNoDatum(t, r, "undecodable file")
	})
}

// TestClassifyChannel_AbsentFieldIsMalformedNotZeroPercent is the phase's headline case. Each
// fixture is valid JSON naming a known agent, missing exactly ONE required field. Every one must
// read malformed AND surface no percentage — a struct decode would silently turn the absent field
// into a 0 that reads as maximally healthy (Six-Sigma Gap 3).
func TestClassifyChannel_AbsentFieldIsMalformedNotZeroPercent(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	stamp := now.Add(-5 * time.Second).UTC().Format(time.RFC3339)

	full := map[string]string{
		"schema":               `2`,
		"agent":                `"manager"`,
		"written_at":           `"` + stamp + `"`,
		"context_used_pct":     `35`,
		"context_tokens_used":  `70000`,
		"context_tokens_total": `200000`,
	}

	for _, omit := range []string{"schema", "written_at", "context_used_pct", "context_tokens_used", "context_tokens_total"} {
		t.Run("Missing_"+omit, func(t *testing.T) {
			dir := t.TempDir()
			var parts []string
			for k, v := range full {
				if k != omit {
					parts = append(parts, `"`+k+`":`+v)
				}
			}
			writeRawJSON(t, dir, "s1.json", "{"+strings.Join(parts, ",")+"}")

			got, err := ReadObservations(dir, testOpts("manager"), now)
			if err != nil {
				t.Fatalf("ReadObservations: %v", err)
			}
			r := mustReading(t, got, "manager")
			if r.State() != StateMalformed {
				t.Errorf("state = %q, want %q with %q absent", r.State(), StateMalformed, omit)
			}
			assertNoDatum(t, r, "snapshot missing "+omit)
		})
	}

	// An explicit null must behave exactly like an absent key — this is what the writer emits
	// when the host reported no occupancy, and it is the single most likely real-world input.
	t.Run("ExplicitNullPct", func(t *testing.T) {
		dir := t.TempDir()
		writeRawJSON(t, dir, "s1.json", `{"schema":2,"agent":"manager","written_at":"`+stamp+
			`","context_used_pct":null,"context_tokens_used":null,"context_tokens_total":null}`)

		got, err := ReadObservations(dir, testOpts("manager"), now)
		if err != nil {
			t.Fatalf("ReadObservations: %v", err)
		}
		r := mustReading(t, got, "manager")
		if r.State() != StateMalformed {
			t.Errorf("state = %q, want %q for an explicit null occupancy", r.State(), StateMalformed)
		}
		assertNoDatum(t, r, "explicit null occupancy")
	})
}

// TestReadObservations_LegacyV1SnapshotNeverReadsHealthy covers Gotcha 12. Every snapshot on disk
// today was written by PR #595 with no schema key and no occupancy. Under the presence rule they
// carry no trustworthy datum, and they self-heal on the owning session's first v2 render.
//
// DO NOT "fix" this into a healthy default: a v1 file is a cost record, and treating its absent
// occupancy as 0% would report a wedged agent as maximally healthy.
func TestReadObservations_LegacyV1SnapshotNeverReadsHealthy(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct{ name, body string }{
		{"PR595CostOnly", `{"session_id":"s2","date":"2026-08-04","baseline_cost_usd":0,` +
			`"cost_usd":3,"input_tokens":3000,"output_tokens":0,"updated_at":"2026-08-04T11:59:55Z"}`},
		{"ExplicitSchema1", `{"schema":1,"agent":"manager","session_id":"s2","cost_usd":3,` +
			`"updated_at":"2026-08-04T11:59:55Z"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeRawJSON(t, dir, "s2.json", tc.body)

			got, err := ReadObservations(dir, testOpts("manager"), now)
			if err != nil {
				t.Fatalf("ReadObservations: %v", err)
			}
			r := mustReading(t, got, "manager")
			if r.State() == StateFresh {
				t.Error("a legacy v1 snapshot read as fresh")
			}
			assertNoDatum(t, r, "legacy v1 snapshot")
		})
	}

	// A v1 file must not mask a live v2 file for the same agent.
	t.Run("DoesNotMaskALiveV2Sibling", func(t *testing.T) {
		dir := t.TempDir()
		writeRawJSON(t, dir, "old.json", `{"session_id":"old","cost_usd":3,"updated_at":"2026-08-04T11:00:00Z"}`)
		writeRawSnapshot(t, dir, v2Snapshot("new", "manager", now.Add(-5*time.Second), 77))

		got, err := ReadObservations(dir, testOpts("manager"), now)
		if err != nil {
			t.Fatalf("ReadObservations: %v", err)
		}
		r := mustReading(t, got, "manager")
		if r.State() != StateFresh {
			t.Errorf("state = %q, want fresh — a v1 leftover must not suppress a live v2 reading", r.State())
		}
		if pct, ok := r.UsedPct(); !ok || !approx(pct, 77) {
			t.Errorf("UsedPct() = (%v, %v), want (77, true)", pct, ok)
		}
	})
}

// TestReadObservations_NewestWrittenAtWinsPerAgent: one agent accumulates several session files
// across recycles (Gotcha 15), so the reading must be the newest datum — not whichever file
// os.ReadDir happens to return last. The newest file is deliberately named so it sorts FIRST.
func TestReadObservations_NewestWrittenAtWinsPerAgent(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	writeRawSnapshot(t, dir, v2Snapshot("aaa-newest", "manager", now.Add(-5*time.Second), 90))
	writeRawSnapshot(t, dir, v2Snapshot("mmm-oldest", "manager", now.Add(-600*time.Second), 10))
	writeRawSnapshot(t, dir, v2Snapshot("zzz-middle", "manager", now.Add(-300*time.Second), 50))

	got, err := ReadObservations(dir, testOpts("manager"), now)
	if err != nil {
		t.Fatalf("ReadObservations: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d readings, want exactly 1 (one entry per known agent)", len(got))
	}
	r := mustReading(t, got, "manager")
	pct, ok := r.UsedPct()
	if !ok || !approx(pct, 90) {
		t.Errorf("UsedPct() = (%v, %v), want (90, true) — the newest written_at must win", pct, ok)
	}
	if r.State() != StateFresh {
		t.Errorf("state = %q, want fresh", r.State())
	}
	obs, ok := r.Observation()
	if !ok || obs.SessionID() != "aaa-newest" {
		t.Errorf("observation session = %q, want aaa-newest", obs.SessionID())
	}
}

// TestObservation_PercentClampedToRange: the snapshot dir is agent-writable, so the percentage is
// semi-trusted input. Clamping is NOT rejection — 0 and 100 are legitimate readings and must pass
// through intact, while a non-finite value is meaningless and is rejected outright.
func TestObservation_PercentClampedToRange(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name      string
		raw       string
		wantPct   float64
		wantState ChannelState
	}{
		{"NegativeClampedToZero", `-5`, 0, StateFresh},
		{"ZeroIsLegitimate", `0`, 0, StateFresh},
		{"HundredIsLegitimate", `100`, 100, StateFresh},
		{"AboveRangeClampedTo100", `250`, 100, StateFresh},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			stamp := now.Add(-5 * time.Second).UTC().Format(time.RFC3339)
			writeRawJSON(t, dir, "s1.json", `{"schema":2,"agent":"manager","written_at":"`+stamp+
				`","context_used_pct":`+tc.raw+`,"context_tokens_used":1,"context_tokens_total":2}`)

			got, err := ReadObservations(dir, testOpts("manager"), now)
			if err != nil {
				t.Fatalf("ReadObservations: %v", err)
			}
			r := mustReading(t, got, "manager")
			if r.State() != tc.wantState {
				t.Errorf("state = %q, want %q", r.State(), tc.wantState)
			}
			if pct, ok := r.UsedPct(); !ok || !approx(pct, tc.wantPct) {
				t.Errorf("UsedPct() = (%v, %v), want (%v, true)", pct, ok, tc.wantPct)
			}
		})
	}
}

// TestObservation_FutureWrittenAtClampedToNow: a clock-skewed or forged future stamp must not buy
// a channel unbounded freshness.
//
// Clamping the AGE alone does not achieve that, and asserting only "age >= 0 at now" hides it: the
// clamp is re-applied on every read, so a stamp D into the future would keep the channel fresh for
// the whole of D no matter how long the agent had been dead. A host on a fast clock would have
// recovery silently disabled. So the tolerance is bounded — inside it the stamp is clamped, beyond
// it the datum is refused (security.md:49 states only the clamp; this is the gap in that remedy).
func TestObservation_FutureWrittenAtClampedToNow(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("SmallSkewIsClampedAndFresh", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("s1", "manager", now.Add(30*time.Second), 50))

		got, err := ReadObservations(dir, testOpts("manager"), now)
		if err != nil {
			t.Fatalf("ReadObservations: %v", err)
		}
		r := mustReading(t, got, "manager")
		age, ok := r.Age()
		if !ok {
			t.Fatal("a reading with a datum must expose an age")
		}
		if age < 0 {
			t.Errorf("age = %s, want >= 0", age)
		}
		if r.State() != StateFresh {
			t.Errorf("state = %q, want fresh — benign skew must not report a healthy agent stale", r.State())
		}
		// No caller may ever observe a timestamp from the future.
		if obs, _ := r.Observation(); obs.WrittenAt().After(now) {
			t.Errorf("WrittenAt() = %s, after now = %s; the stamp must be clamped", obs.WrittenAt(), now)
		}
	})

	// THE REGRESSION GUARD: the same fixture, read at successively later clocks. A clamp-only
	// implementation returns fresh at every one of these.
	t.Run("FarFutureStampCannotBuyPermanentFreshness", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("s1", "manager", now.Add(24*time.Hour), 95))

		for _, at := range []time.Time{now, now.Add(time.Hour), now.Add(12 * time.Hour)} {
			got, err := ReadObservations(dir, testOpts("manager"), at)
			if err != nil {
				t.Fatalf("ReadObservations: %v", err)
			}
			r := mustReading(t, got, "manager")
			if r.IsHealthy() {
				t.Errorf("at %s a 24h-future stamp read healthy; a forged or skewed stamp must not "+
					"grant freshness it did not earn", at)
			}
			if r.State() != StateMalformed {
				t.Errorf("at %s state = %q, want %q", at, r.State(), StateMalformed)
			}
			assertNoDatum(t, r, "far-future stamp at "+at.String())
		}
	})
}

// TestObservation_ZeroThresholdsAreFailClosed: a caller that forgot to configure the reader must
// get a visibly dark factory, not a silently healthy one — the watchdog's "a nil scope is never
// all" posture. The subtle case is a datum written exactly at now: age 0 satisfies `age <= 0`, so
// without an explicit guard a zero-valued ReadOptions reports fresh.
func TestObservation_ZeroThresholdsAreFailClosed(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	writeRawSnapshot(t, dir, v2Snapshot("s1", "manager", now, 42))

	// HALF-configured is as disqualifying as unconfigured: the two thresholds describe one
	// staleness ladder, so either being unset makes the reader untrustworthy. The zero-age datum
	// is the case that slips through a `&&` guard, because age 0 satisfies `age <= 0`.
	for _, tc := range []struct {
		name string
		opts ReadOptions
	}{
		{"BothUnset", ReadOptions{KnownAgents: map[string]struct{}{"manager": {}}}},
		{"StalenessUnset", ReadOptions{KnownAgents: map[string]struct{}{"manager": {}}, DarkAfter: testDarkAfter}},
		{"DarkAfterUnset", ReadOptions{KnownAgents: map[string]struct{}{"manager": {}}, Staleness: testStaleness}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadObservations(dir, tc.opts, now)
			if err != nil {
				t.Fatalf("ReadObservations: %v", err)
			}
			r := mustReading(t, got, "manager")
			if r.IsHealthy() {
				t.Error("an under-configured reader reported a healthy channel")
			}
			if r.State() != StateDark {
				t.Errorf("state = %q, want %q", r.State(), StateDark)
			}
		})
	}
}

// TestReadObservations_NonRegularFileCannotWedgeTheSweep: the snapshot directory is
// agent-writable, so an agent can create things that are not files. A FIFO named *.json blocks
// os.Open until a writer appears — a size cap bounds how MUCH we read, never how LONG. An
// unbounded block here would wedge the Phase 2 watchdog sweep and silently disable recovery for
// the whole factory, so entries that are not regular files are skipped before they are opened.
//
// The test enforces its own deadline: a regression here HANGS rather than failing, and a hung
// test is indistinguishable from a slow one until CI times out.
func TestReadObservations_NonRegularFileCannotWedgeTheSweep(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	writeRawSnapshot(t, dir, v2Snapshot("good", "manager", now.Add(-5*time.Second), 42))
	if err := syscall.Mkfifo(filepath.Join(dir, "evil.json"), 0o644); err != nil {
		t.Skipf("cannot create a FIFO on this platform/filesystem: %v", err)
	}
	if err := os.Symlink(filepath.Join(dir, "good.json"), filepath.Join(dir, "link.json")); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	type result struct {
		readings map[string]ChannelReading
		err      error
	}
	done := make(chan result, 1)
	go func() {
		r, err := ReadObservations(dir, testOpts("manager"), now)
		done <- result{r, err}
	}()

	select {
	case got := <-done:
		if got.err != nil {
			t.Fatalf("ReadObservations: %v", got.err)
		}
		r := mustReading(t, got.readings, "manager")
		if pct, ok := r.UsedPct(); !ok || !approx(pct, 42) {
			t.Errorf("UsedPct() = (%v, %v), want (42, true) — the valid sibling must still be read", pct, ok)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("ReadObservations blocked on a non-regular file; a single mkfifo in the snapshot " +
			"directory would wedge the watchdog sweep and silently disable recovery")
	}
}

// TestReadObservations_FilenameGrammarRejected: the reader mirrors the writer's own filename
// grammar via sanitizeSessionID, so the two cannot drift (the issue #563 lesson).
func TestReadObservations_FilenameGrammarRejected(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	writeRawSnapshot(t, dir, v2Snapshot("ok-Session_1", "manager", now.Add(-5*time.Second), 42))

	bad := v2Snapshot("ignored", "manager", now.Add(-5*time.Second), 99)
	for _, name := range []string{"has space.json", "sess$id.json", ".json", strings.Repeat("x", 129) + ".json"} {
		bad.SessionID = strings.TrimSuffix(name, ".json")
		writeRawJSONFromSnapshot(t, dir, name, bad)
	}
	writeRawJSON(t, dir, "not-json.txt", `{"schema":2,"agent":"manager"}`)
	if err := os.MkdirAll(filepath.Join(dir, "subdir.json"), 0o755); err != nil {
		t.Fatal(err)
	}

	got, err := ReadObservations(dir, testOpts("manager"), now)
	if err != nil {
		t.Fatalf("ReadObservations must tolerate junk in an agent-writable dir: %v", err)
	}
	r := mustReading(t, got, "manager")
	if pct, ok := r.UsedPct(); !ok || !approx(pct, 42) {
		t.Errorf("UsedPct() = (%v, %v), want (42, true) — only the grammar-valid file may contribute", pct, ok)
	}
}

// TestReadObservations_SizeCapIs64KBNotWriterCap enforces Gotcha 9: the reader owns its own 64 KB
// cap because it guards a different trust boundary than the writer's 4 KB read-back. The
// load-bearing half is (b) — a VALID file between the two caps must still be accepted, which is
// what goes red if the constants are ever collapsed into one.
func TestReadObservations_SizeCapIs64KBNotWriterCap(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	stamp := now.Add(-5 * time.Second).UTC().Format(time.RFC3339)

	body := func(pad int) string {
		return `{"schema":2,"agent":"manager","written_at":"` + stamp +
			`","context_used_pct":42,"context_tokens_used":1,"context_tokens_total":2,"pad":"` +
			strings.Repeat("p", pad) + `"}`
	}

	t.Run("ValidFileLargerThanWriterCapIsAccepted", func(t *testing.T) {
		dir := t.TempDir()
		writeRawJSON(t, dir, "s1.json", body(8*1024)) // > maxSnapshotBytes (4 KB), < 64 KB
		got, err := ReadObservations(dir, testOpts("manager"), now)
		if err != nil {
			t.Fatalf("ReadObservations: %v", err)
		}
		r := mustReading(t, got, "manager")
		if pct, ok := r.UsedPct(); !ok || !approx(pct, 42) {
			t.Errorf("UsedPct() = (%v, %v), want (42, true) — the reader must not reuse the writer's 4 KB cap", pct, ok)
		}
	})

	t.Run("OverCapIsRejected", func(t *testing.T) {
		dir := t.TempDir()
		writeRawJSON(t, dir, "s1.json", body(65*1024)) // > 64 KB
		got, err := ReadObservations(dir, testOpts("manager"), now)
		if err != nil {
			t.Fatalf("ReadObservations: %v", err)
		}
		assertNoDatum(t, mustReading(t, got, "manager"), "oversized file")
	})
}

// TestReadObservations_UnknownAgentRejected: membership in agents.json is the trust boundary's
// attribution gate (design-doc.md:179). A snapshot naming an agent outside the roster contributes
// to nothing — it cannot invent an agent, and it cannot pollute a real one.
func TestReadObservations_UnknownAgentRejected(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	writeRawSnapshot(t, dir, v2Snapshot("real", "manager", now.Add(-5*time.Second), 42))
	writeRawSnapshot(t, dir, v2Snapshot("ghost", "ghost", now.Add(-1*time.Second), 99))
	writeRawSnapshot(t, dir, v2Snapshot("empty", "", now.Add(-1*time.Second), 99))
	writeRawSnapshot(t, dir, v2Snapshot("traverse", "../manager", now.Add(-1*time.Second), 99))
	writeRawSnapshot(t, dir, v2Snapshot("toolong", strings.Repeat("a", 65), now.Add(-1*time.Second), 99))

	got, err := ReadObservations(dir, testOpts("manager"), now)
	if err != nil {
		t.Fatalf("ReadObservations: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d readings %v, want exactly 1 (only the known agent)", len(got), got)
	}
	if _, ok := got["ghost"]; ok {
		t.Error("an agent outside agents.json appeared in the reading map")
	}
	r := mustReading(t, got, "manager")
	if pct, ok := r.UsedPct(); !ok || !approx(pct, 42) {
		t.Errorf("UsedPct() = (%v, %v), want (42, true) — a forged sibling must not displace the real reading", pct, ok)
	}
}

// TestReadObservations_MissingDirIsNoneNotError: with the factory gate off (Gotcha 10) — or before
// any agent has ever rendered — the directory does not exist. Every agent must read none, and that
// must not be an error the caller might mishandle into "assume fine".
func TestReadObservations_MissingDirIsNoneNotError(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	missing := filepath.Join(t.TempDir(), "no-such-dir")

	got, err := ReadObservations(missing, testOpts("manager", "supervisor"), now)
	if err != nil {
		t.Fatalf("a missing snapshot dir must not be an error, got: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d readings, want one per known agent", len(got))
	}
	for _, agent := range []string{"manager", "supervisor"} {
		r := mustReading(t, got, agent)
		if r.State() != StateNone {
			t.Errorf("%s: state = %q, want %q", agent, r.State(), StateNone)
		}
		assertNoDatum(t, r, agent+" with no snapshot dir")
	}
}

// TestReadObservations_MalformedFileDoesNotPoisonSiblings mirrors SumDaily's contract: one bad file
// is skipped, never fatal, and never blanks the whole sweep.
func TestReadObservations_MalformedFileDoesNotPoisonSiblings(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	writeRawSnapshot(t, dir, v2Snapshot("good1", "manager", now.Add(-5*time.Second), 42))
	writeRawSnapshot(t, dir, v2Snapshot("good2", "supervisor", now.Add(-300*time.Second), 71))
	writeRawJSON(t, dir, "corrupt.json", "{not json")
	writeRawJSON(t, dir, "huge.json", `{"schema":2,"pad":"`+strings.Repeat("p", 70*1024)+`"}`)

	got, err := ReadObservations(dir, testOpts("manager", "supervisor", "architect"), now)
	if err != nil {
		t.Fatalf("ReadObservations: %v", err)
	}
	if r := mustReading(t, got, "manager"); r.State() != StateFresh {
		t.Errorf("manager state = %q, want fresh", r.State())
	}
	if r := mustReading(t, got, "supervisor"); r.State() != StateStale {
		t.Errorf("supervisor state = %q, want stale", r.State())
	}
	if r := mustReading(t, got, "architect"); r.State() != StateNone {
		t.Errorf("architect state = %q, want none", r.State())
	}
}

// TestReadObservations_MalformedDoesNotMaskALiveDatum: an agent accumulates session files across
// recycles, so a broken leftover from an old session must not bury the live one. The sibling here
// is ATTRIBUTABLE (schema 2, known agent) but has no written_at, so it genuinely enters the
// malformed set — unlike a legacy v1 file, which is unattributable and never reaches that code
// path at all. Without the "only when the agent has no valid datum" condition, a healthy agent
// would read malformed and Phase 2 would escalate on it.
func TestReadObservations_MalformedDoesNotMaskALiveDatum(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	writeRawJSON(t, dir, "broken.json",
		`{"schema":2,"agent":"manager","context_used_pct":5,"context_tokens_used":1,"context_tokens_total":2}`)
	writeRawSnapshot(t, dir, v2Snapshot("live", "manager", now.Add(-5*time.Second), 88))

	got, err := ReadObservations(dir, testOpts("manager"), now)
	if err != nil {
		t.Fatalf("ReadObservations: %v", err)
	}
	r := mustReading(t, got, "manager")
	if r.State() != StateFresh {
		t.Errorf("state = %q, want fresh — a broken leftover must not mask the live session", r.State())
	}
	if pct, ok := r.UsedPct(); !ok || !approx(pct, 88) {
		t.Errorf("UsedPct() = (%v, %v), want (88, true)", pct, ok)
	}

	// And with no live sibling, that same broken file MUST surface as malformed rather than
	// vanish into none — otherwise the guard above would be indistinguishable from ignoring
	// malformed files entirely.
	only := t.TempDir()
	writeRawJSON(t, only, "broken.json",
		`{"schema":2,"agent":"manager","context_used_pct":5,"context_tokens_used":1,"context_tokens_total":2}`)
	got2, err := ReadObservations(only, testOpts("manager"), now)
	if err != nil {
		t.Fatalf("ReadObservations: %v", err)
	}
	if r := mustReading(t, got2, "manager"); r.State() != StateMalformed {
		t.Errorf("state = %q, want %q when the only attributable file is broken", r.State(), StateMalformed)
	}
}

// TestReadObservations_ClockIsInjectedNotAmbient proves the clock is genuinely a parameter
// (ADR-004; peer-review correction F6). The SAME unchanged fixture must reclassify purely as a
// function of the injected time — a time.Now() slip inside the library makes this red.
func TestReadObservations_ClockIsInjectedNotAmbient(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)
	dir := t.TempDir()
	writeRawSnapshot(t, dir, v2Snapshot("s1", "manager", now.Add(-5*time.Second), 42))

	for _, tc := range []struct {
		name string
		at   time.Time
		want ChannelState
	}{
		{"AtWriteTime", now, StateFresh},
		{"FiveMinutesLater", now.Add(5 * time.Minute), StateStale},
		{"OneHourLater", now.Add(time.Hour), StateDark},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ReadObservations(dir, testOpts("manager"), tc.at)
			if err != nil {
				t.Fatalf("ReadObservations: %v", err)
			}
			if r := mustReading(t, got, "manager"); r.State() != tc.want {
				t.Errorf("state = %q, want %q at injected time %s", r.State(), tc.want, tc.at)
			}
		})
	}
}

// TestObservation_NoHealthyWithoutDatum is the AC-5 elevation check (design-doc.md:312).
//
// Per ADR-005 this is a RUNTIME precondition plus construction discipline, NOT a compile-time
// interlock — Go 1.24 has no sum types, so a "compile-time impossible" claim would be false. The
// mechanical half is the API scan below: it parses this package's own source and fails if any
// exported function can return a ChannelReading without consuming an Observation. That is what
// stops a future contributor adding a convenient FreshReading(pct float64) helper.
func TestObservation_NoHealthyWithoutDatum(t *testing.T) {
	t.Run("ZeroValuesAreNotHealthy", func(t *testing.T) {
		var r ChannelReading
		if r.IsHealthy() {
			t.Error("the zero ChannelReading reports healthy; an unconstructed reading is not evidence of health")
		}
		if r.State() != StateMalformed {
			t.Errorf("zero ChannelReading state = %q, want %q", r.State(), StateMalformed)
		}
		assertNoDatum(t, r, "zero ChannelReading")

		var o Observation
		if o.UsedPct() != 0 || o.Agent() != "" {
			t.Error("the zero Observation carries data it never received")
		}
	})

	t.Run("ZeroObservationCannotBePromoted", func(t *testing.T) {
		var o Observation
		r := ObservedReading(o, testOpts("manager"), time.Now())
		if r.IsHealthy() {
			t.Error("a zero Observation was promoted into a healthy reading")
		}
		if r.State() != StateMalformed {
			t.Errorf("state = %q, want %q — an unvalidated Observation must not classify", r.State(), StateMalformed)
		}
		assertNoDatum(t, r, "reading built from a zero Observation")
	})

	t.Run("NoDatumFreeConstructorsExist", func(t *testing.T) {
		// Exported symbols that may hand back a ChannelReading without taking an Observation.
		// Each entry is a deliberate, reviewable exemption — adding one is the decision this
		// guard exists to force into the open.
		//
		//   NoReading / MalformedReading — can only ever produce a non-healthy state.
		//   ReadObservations             — IS the validating decoder; it manufactures the
		//                                  Observations everything else must be handed.
		//   SessionObservation           — the session-keyed sibling of ReadObservations (#622 C3).
		//                                  It decodes through the SAME validating path
		//                                  (readObservationFile then ObservedReading), so like
		//                                  ReadObservations it manufactures its own Observation
		//                                  rather than accepting one, and it has no route to a
		//                                  healthy state that does not pass every check. The
		//                                  exemption is honest ONLY while that remains true: a
		//                                  future rewrite onto raw readSnapshot would keep this
		//                                  test green while removing the validation, so the two
		//                                  must be reviewed together.
		datumFree := map[string]bool{
			"NoReading":          true,
			"MalformedReading":   true,
			"ReadObservations":   true,
			"SessionObservation": true,
		}

		// mentions reports whether a type expression names ident ANYWHERE inside it, so a
		// map[string]ChannelReading, a []ChannelReading, a *ChannelReading or a (ChannelReading,
		// error) tuple are all caught. Matching only a bare *ast.Ident would miss every one of
		// them — including the shape ReadObservations itself uses, which is the most plausible
		// shape for a future addition.
		var mentions func(ast.Expr, string) bool
		mentions = func(e ast.Expr, ident string) bool {
			found := false
			ast.Inspect(e, func(n ast.Node) bool {
				if id, ok := n.(*ast.Ident); ok && id.Name == ident {
					found = true
				}
				return !found
			})
			return found
		}

		fset := token.NewFileSet()
		pkgs, err := parser.ParseDir(fset, ".", func(fi os.FileInfo) bool {
			return !strings.HasSuffix(fi.Name(), "_test.go")
		}, 0)
		if err != nil {
			t.Fatalf("parsing package source: %v", err)
		}
		var checked int
		for _, pkg := range pkgs {
			for _, file := range pkg.Files {
				for _, decl := range file.Decls {
					// METHODS ARE IN SCOPE TOO (fn.Recv != nil is not skipped): a
					// `func (x Anything) Reading() ChannelReading` would otherwise be an
					// unguarded back door to a healthy reading.
					fn, ok := decl.(*ast.FuncDecl)
					if !ok || !fn.Name.IsExported() || fn.Type.Results == nil {
						continue
					}
					// A method on ChannelReading/Observation itself is an accessor, not a
					// constructor — its receiver already IS the validated datum.
					if fn.Recv != nil && len(fn.Recv.List) > 0 &&
						(mentions(fn.Recv.List[0].Type, "ChannelReading") || mentions(fn.Recv.List[0].Type, "Observation")) {
						continue
					}
					returnsReading := false
					for _, res := range fn.Type.Results.List {
						if mentions(res.Type, "ChannelReading") {
							returnsReading = true
						}
					}
					if !returnsReading {
						continue
					}
					checked++
					if datumFree[fn.Name.Name] {
						continue
					}
					takesObservation := false
					for _, p := range fn.Type.Params.List {
						if mentions(p.Type, "Observation") {
							takesObservation = true
						}
					}
					if !takesObservation {
						t.Errorf("exported %s returns a ChannelReading without consuming an Observation; "+
							"every path to a healthy reading must require a validated datum (AC-5). "+
							"If it can only produce a non-healthy state, add it to the datumFree allowlist and say why.",
							fn.Name.Name)
					}
				}
			}
		}
		if checked == 0 {
			t.Fatal("the API scan matched no exported ChannelReading producer — the guard is vacuous")
		}
	})
}
