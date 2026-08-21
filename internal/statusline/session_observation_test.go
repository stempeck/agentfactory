package statusline

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestSessionObservation covers the session-keyed occupancy read (#622 C3).
//
// The property that makes it a different function from ReadObservations rather than a convenience
// wrapper is WrongSessionNeverAnswers: at a step boundary an agent-keyed read would hand back a
// DEAD session's last snapshot for up to Staleness after a recycle, and the boundary would act on
// a window that was never this session's (cross-review CRIT-1). Everything else here is the
// inherited trust boundary, re-asserted on the new entry point because it is a new entry point.
func TestSessionObservation(t *testing.T) {
	now := time.Date(2026, 8, 4, 12, 0, 0, 0, time.UTC)

	t.Run("FreshSnapshotForThisSessionReadsHealthy", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessa", "manager", now.Add(-10*time.Second), 42))

		r := SessionObservation(dir, "sessa", testOpts("manager"), now)

		if !r.IsHealthy() {
			t.Fatalf("state = %q, want %q", r.State(), StateFresh)
		}
		if pct, ok := r.UsedPct(); !ok || !approx(pct, 42) {
			t.Errorf("UsedPct() = (%v, %v), want (42, true)", pct, ok)
		}
		obs, ok := r.Observation()
		if !ok {
			t.Fatal("a healthy reading must carry its datum")
		}
		if obs.SessionID() != "sessa" {
			t.Errorf("Observation.SessionID() = %q, want the sanitized filename stem %q", obs.SessionID(), "sessa")
		}
		if obs.Agent() != "manager" {
			t.Errorf("Observation.Agent() = %q, want %q", obs.Agent(), "manager")
		}
	})

	t.Run("WrongSessionNeverAnswers", func(t *testing.T) {
		// CRIT-1. The same agent's PREVIOUS session left a perfectly fresh snapshot behind; the
		// respawned session has not rendered yet. ReadObservations resolves this by
		// newest-written_at-wins per AGENT (observation.go:347-349) and would answer with the dead
		// session's occupancy — a handoff fired off a window that no longer exists, escalating
		// through the recovery rate cap to a false RECOVERY HALTED.
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessold", "manager", now.Add(-5*time.Second), 96))

		r := SessionObservation(dir, "sessnew", testOpts("manager"), now)

		if r.State() != StateNone {
			t.Errorf("state = %q, want %q — a session with no snapshot of its own has no datum", r.State(), StateNone)
		}
		assertNoDatum(t, r, "a sibling session's snapshot")

		// And the agent-keyed reader really would have answered, so the property above is not
		// vacuously true of this fixture.
		agentKeyed, err := ReadObservations(dir, testOpts("manager"), now)
		if err != nil {
			t.Fatalf("ReadObservations: %v", err)
		}
		if pct, ok := mustReading(t, agentKeyed, "manager").UsedPct(); !ok || !approx(pct, 96) {
			t.Fatalf("fixture no longer discriminates: the agent-keyed reader gave (%v, %v), want (96, true)", pct, ok)
		}
	})

	t.Run("RawSessionIDIsSanitizedInternally", func(t *testing.T) {
		// G3 / the #563 drift class. The caller's .runtime/session_id holds the RAW id and
		// sanitizeSessionID is unexported, so a caller that had to build the path itself would
		// have to restate the grammar — and the two spellings would drift, leaving the boundary
		// decision permanently inert.
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessa", "manager", now.Add(-10*time.Second), 42))

		r := SessionObservation(dir, " sess/a\n", testOpts("manager"), now)

		if !r.IsHealthy() {
			t.Errorf("state = %q, want %q — the raw id must resolve to the sanitized stem", r.State(), StateFresh)
		}
	})

	t.Run("UnsanitizableSessionIDYieldsNoDatum", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessa", "manager", now.Add(-10*time.Second), 42))

		for _, id := range []string{"", "///", "..."} {
			r := SessionObservation(dir, id, testOpts("manager"), now)
			if r.State() != StateNone {
				t.Errorf("id %q: state = %q, want %q", id, r.State(), StateNone)
			}
			assertNoDatum(t, r, "unsanitizable session id "+id)
		}
	})

	t.Run("AbsentSnapshotIsNoneNotMalformed", func(t *testing.T) {
		dir := t.TempDir()

		r := SessionObservation(dir, "sessa", testOpts("manager"), now)

		if r.State() != StateNone {
			t.Errorf("state = %q, want %q — this session has simply never rendered", r.State(), StateNone)
		}
		assertNoDatum(t, r, "absent snapshot")
	})

	t.Run("MissingDirectoryIsNoneNotMalformed", func(t *testing.T) {
		r := SessionObservation(filepath.Join(t.TempDir(), "never-created"), "sessa", testOpts("manager"), now)

		if r.State() != StateNone {
			t.Errorf("state = %q, want %q — the factory statusline gate may simply be off", r.State(), StateNone)
		}
	})

	t.Run("AbsentOccupancyYieldsNilNeverZero", func(t *testing.T) {
		// The all-or-none writer (daily.go:225-235) never persists a partial occupancy block, so a
		// snapshot without those keys is a file that exists and carries no measurement. It must
		// read malformed and surface NOTHING: a 0% reading on a wedged agent is worse than no
		// reading at all, because the boundary would read it as maximally healthy.
		dir := t.TempDir()
		writeRawJSON(t, dir, "sessa.json",
			`{"schema":2,"session_id":"sessa","agent":"manager","written_at":"`+
				now.Add(-10*time.Second).UTC().Format(time.RFC3339)+`"}`)

		r := SessionObservation(dir, "sessa", testOpts("manager"), now)

		if r.State() != StateMalformed {
			t.Errorf("state = %q, want %q", r.State(), StateMalformed)
		}
		assertNoDatum(t, r, "snapshot with no occupancy keys")
	})

	t.Run("PartialOccupancyYieldsNoDatum", func(t *testing.T) {
		dir := t.TempDir()
		writeRawJSON(t, dir, "sessa.json",
			`{"schema":2,"session_id":"sessa","agent":"manager","written_at":"`+
				now.Add(-10*time.Second).UTC().Format(time.RFC3339)+`","context_used_pct":42}`)

		assertNoDatum(t, SessionObservation(dir, "sessa", testOpts("manager"), now), "partial occupancy block")
	})

	t.Run("LegacyV1SnapshotYieldsNoDatum", func(t *testing.T) {
		dir := t.TempDir()
		writeRawJSON(t, dir, "sessa.json",
			`{"session_id":"sessa","agent":"manager","written_at":"`+
				now.Add(-10*time.Second).UTC().Format(time.RFC3339)+
				`","context_used_pct":42,"context_tokens_used":1,"context_tokens_total":2}`)

		assertNoDatum(t, SessionObservation(dir, "sessa", testOpts("manager"), now), "a #595 v1 file with no schema key")
	})

	t.Run("SizeCapIsTheReaderCapNotTheWriterCap", func(t *testing.T) {
		// G4(b). This read goes through readObservationFile, so it inherits the READER's 64 KB
		// cap (observation.go:38-45), not daily.go's 4 KB writer cap — those two guard different
		// boundaries and TestReadObservations_SizeCapIs64KBNotWriterCap forbids collapsing them.
		// The design doc's ">4KB" wording describes the writer's read-back path and is unreachable
		// from here.
		body := func(pad int) string {
			return `{"schema":2,"session_id":"sessa","agent":"manager","written_at":"` +
				now.Add(-10*time.Second).UTC().Format(time.RFC3339) +
				`","context_used_pct":42,"context_tokens_used":1,"context_tokens_total":2,"pad":"` +
				strings.Repeat("p", pad) + `"}`
		}

		t.Run("ValidFileLargerThanWriterCapIsAccepted", func(t *testing.T) {
			dir := t.TempDir()
			writeRawJSON(t, dir, "sessa.json", body(8*1024)) // > maxSnapshotBytes (4 KB), < 64 KB

			r := SessionObservation(dir, "sessa", testOpts("manager"), now)
			if pct, ok := r.UsedPct(); !ok || !approx(pct, 42) {
				t.Errorf("UsedPct() = (%v, %v), want (42, true) — this reader must not adopt the writer's 4 KB cap", pct, ok)
			}
		})

		t.Run("OversizedIsMalformed", func(t *testing.T) {
			dir := t.TempDir()
			writeRawJSON(t, dir, "sessa.json", body(65*1024)) // > 64 KB

			r := SessionObservation(dir, "sessa", testOpts("manager"), now)
			if r.State() != StateMalformed {
				t.Errorf("state = %q, want %q — a file exists and could not be trusted, which is a "+
					"different fact from nothing being there", r.State(), StateMalformed)
			}
			assertNoDatum(t, r, "oversized snapshot")
		})
	})

	t.Run("UnknownAgentYieldsNoDatum", func(t *testing.T) {
		// G4(a). The roster arrives in opts and is not bypassed, so a snapshot claiming an agent
		// the caller does not know contributes nothing — the same rule ReadObservations applies.
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessa", "impostor", now.Add(-10*time.Second), 42))

		assertNoDatum(t, SessionObservation(dir, "sessa", testOpts("manager"), now), "snapshot naming an off-roster agent")
	})

	t.Run("EmptyRosterTrustsNothing", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessa", "manager", now.Add(-10*time.Second), 42))

		opts := ReadOptions{Staleness: testStaleness, DarkAfter: testDarkAfter}
		assertNoDatum(t, SessionObservation(dir, "sessa", opts, now), "an empty KnownAgents roster")
	})

	t.Run("UnsetThresholdsClassifyDarkNeverFresh", func(t *testing.T) {
		// Inherited from ObservedReading (observation.go:224-226) and deliberate: a caller that
		// forgot to configure the reader gets a visibly dark channel, never a silently healthy one.
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessa", "manager", now.Add(-10*time.Second), 42))

		r := SessionObservation(dir, "sessa", ReadOptions{KnownAgents: map[string]struct{}{"manager": {}}}, now)

		if r.State() != StateDark {
			t.Errorf("state = %q, want %q", r.State(), StateDark)
		}
		if r.IsHealthy() {
			t.Error("an under-configured reader must never emit health")
		}
	})

	t.Run("StaleAndDarkStillClassify", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessa", "manager", now.Add(-5*time.Minute), 42))
		if got := SessionObservation(dir, "sessa", testOpts("manager"), now).State(); got != StateStale {
			t.Errorf("5m old: state = %q, want %q", got, StateStale)
		}

		dir2 := t.TempDir()
		writeRawSnapshot(t, dir2, v2Snapshot("sessa", "manager", now.Add(-30*time.Minute), 42))
		if got := SessionObservation(dir2, "sessa", testOpts("manager"), now).State(); got != StateDark {
			t.Errorf("30m old: state = %q, want %q", got, StateDark)
		}
	})

	t.Run("FutureStampBeyondToleranceIsRefused", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("sessa", "manager", now.Add(10*time.Minute), 42))

		assertNoDatum(t, SessionObservation(dir, "sessa", testOpts("manager"), now), "a stamp beyond the forward-skew tolerance")
	})

	t.Run("NonRegularFileCannotWedgeTheBoundary", func(t *testing.T) {
		// A size cap bounds how MUCH a hostile file makes us read but not how LONG: opening a FIFO
		// read-only blocks until a writer appears, and this directory is agent-writable. The
		// directory sweep guards this in its loop (observation.go:328); a direct-path read has to
		// repeat the check or reintroduce the wedge on the boundary-decision path.
		dir := t.TempDir()
		if err := syscall.Mkfifo(filepath.Join(dir, "sessa.json"), 0o644); err != nil {
			t.Skipf("cannot create a FIFO on this platform/filesystem: %v", err)
		}

		done := make(chan ChannelReading, 1)
		go func() { done <- SessionObservation(dir, "sessa", testOpts("manager"), now) }()

		select {
		case r := <-done:
			assertNoDatum(t, r, "a FIFO in the snapshot directory")
		case <-time.After(10 * time.Second):
			t.Fatal("SessionObservation blocked on a non-regular file; a single mkfifo in the " +
				"snapshot directory would wedge every step boundary for that session")
		}
	})

	t.Run("SymlinkIsRefusedRatherThanFollowed", func(t *testing.T) {
		dir := t.TempDir()
		writeRawSnapshot(t, dir, v2Snapshot("real", "manager", now.Add(-10*time.Second), 42))
		if err := os.Symlink(filepath.Join(dir, "real.json"), filepath.Join(dir, "sessa.json")); err != nil {
			t.Skipf("cannot create a symlink on this platform/filesystem: %v", err)
		}

		assertNoDatum(t, SessionObservation(dir, "sessa", testOpts("manager"), now), "a symlinked snapshot")
	})
}
