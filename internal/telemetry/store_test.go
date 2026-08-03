package telemetry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// The rotation contract has no precedent anywhere in this repository — there is no other JSONL
// writer, no other generational rotation, and no other export cursor — so this test was written
// before store.go and the store was built to satisfy it. The accounting invariant it enforces,
// at every point in the sequence, is:
//
//	appended == readable + dropped
//
// with `dropped` reported through the same surface the status verb will read. An implementation
// where records vanish while `dropped` stays zero is the silent-loss failure this phase exists
// to forbid, and it is indistinguishable from correct behaviour without this assertion.

// mkStore resolves the records directory the way production will, through the shared path
// helper, so this suite and the factory layout cannot drift apart without something failing.
func mkStore(t *testing.T) string {
	t.Helper()
	return config.TelemetryDir(t.TempDir())
}

func ev(agent, instance, step string, seq int) StepEvent {
	return StepEvent{
		V: SchemaVersion, Event: EventStepEnd,
		TS:    fmt.Sprintf("2026-07-22T18:%02d:%02d.%03dZ", seq/60%60, seq%60, seq%1000),
		Agent: agent, WorktreeID: "wt-03478d", Formula: "design-v7",
		InstanceID: instance, StepID: step, StepSeq: seq,
		StepTitle: "Phase " + step, SessionID: "sess-1",
		Model: "fable-5", ModelSource: ModelSourceModelsJSON,
		Verb: "done", VerbMS: 40 + seq, DurationMS: 1000 + seq, Status: StatusClosed,
	}
}

func readAll(t *testing.T, dir, agent string) ([]StepEvent, ReadStats) {
	t.Helper()
	evs, stats, err := ReadEvents(dir, Filter{Agent: agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	return evs, stats
}

func TestRotationNeverDropsUnexportedRecords(t *testing.T) {
	// This test fails against: a store that never rotates (the positive control below proves
	// rotation does occur once it is safe); a reader that ignores the rotated generation (the
	// post-rotation read asserts every record is still returned); a rotation implemented as a
	// truncate rather than a rename (the kept generation is asserted to exist with the old
	// content); and any implementation that discards records while reporting zero drops.

	live := func(dir, agent string) string { return filepath.Join(dir, "steps", agent+".jsonl") }
	kept := func(dir, agent string) string { return filepath.Join(dir, "steps", agent+".jsonl.1") }

	t.Run("records land at the factory-root telemetry dir, never in an agent runtime dir", func(t *testing.T) {
		dir := mkStore(t)
		if err := AppendEvent(dir, ev("design-v7", "i1", "s1", 1)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		if _, err := os.Stat(live(dir, "design-v7")); err != nil {
			t.Fatalf("expected records at <telemetryDir>/steps/<agent>.jsonl: %v", err)
		}
		// The prior attempt at this feature stored records where the reset path removes them.
		// Placement is the entire mechanism by which records survive formula completion,
		// worktree teardown and a reset, so it is asserted rather than assumed.
		var strayed []string
		_ = filepath.Walk(filepath.Dir(filepath.Dir(dir)), func(p string, info os.FileInfo, err error) error {
			if err == nil && !info.IsDir() && strings.Contains(p, ".runtime") {
				strayed = append(strayed, p)
			}
			return nil
		})
		if len(strayed) != 0 {
			t.Errorf("records written under a runtime dir: %v", strayed)
		}
	})

	t.Run("appending never truncates an earlier record", func(t *testing.T) {
		dir := mkStore(t)
		for i := 1; i <= 3; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent %d: %v", i, err)
			}
		}
		raw, err := os.ReadFile(live(dir, "a"))
		if err != nil {
			t.Fatalf("reading log: %v", err)
		}
		lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
		if len(lines) != 3 {
			t.Fatalf("log has %d lines, want 3", len(lines))
		}
		got, stats := readAll(t, dir, "a")
		if len(got) != 3 {
			t.Fatalf("ReadEvents returned %d records, want 3", len(got))
		}
		for i, e := range got {
			if want := fmt.Sprintf("s%d", i+1); e.StepID != want {
				t.Errorf("record %d step_id = %q, want %q (append order is not preserved)", i, e.StepID, want)
			}
		}
		if stats.Dropped != 0 || stats.Malformed != 0 {
			t.Errorf("stats = %+v, want zero drops and zero malformed", stats)
		}
	})

	t.Run("a hostile agent name cannot escape the telemetry dir", func(t *testing.T) {
		dir := mkStore(t)
		for _, bad := range []string{"../escape", "..", "a/b", "", ".", "sub/../../out"} {
			e := ev(bad, "i1", "s1", 1)
			if err := AppendEvent(dir, e); err == nil {
				t.Errorf("AppendEvent accepted agent name %q; the record path is built from this field", bad)
			}
		}
		outside := filepath.Join(filepath.Dir(dir), "escape.jsonl")
		if _, err := os.Stat(outside); err == nil {
			t.Errorf("a file was created outside the telemetry dir at %s", outside)
		}
	})

	t.Run("rotation with a lagging cursor loses nothing", func(t *testing.T) {
		dir := mkStore(t)
		restore := shrinkCaps(t, 1200, 1<<20)
		defer restore()

		const n = 12
		for i := 1; i <= n; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent %d: %v", i, err)
			}
		}
		if _, err := os.Stat(kept(dir, "a")); err != nil {
			t.Fatalf("expected a rotated generation at <agent>.jsonl.1 after crossing the cap: %v", err)
		}
		// The core claim: rotation is a rename, not a deletion, and the reader spans both
		// generations. The cursor has never advanced, so nothing may be gone.
		got, stats := readAll(t, dir, "a")
		if len(got) != n {
			t.Fatalf("ReadEvents returned %d records after rotation, want all %d", len(got), n)
		}
		for i, e := range got {
			if want := fmt.Sprintf("s%d", i+1); e.StepID != want {
				t.Errorf("record %d step_id = %q, want %q — order is not preserved across generations", i, e.StepID, want)
			}
		}
		if stats.Dropped != 0 {
			t.Errorf("dropped = %d after a safe rotation, want 0", stats.Dropped)
		}
	})

	t.Run("a forced drop is counted before the rename and surfaced durably", func(t *testing.T) {
		dir := mkStore(t)
		// A hard ceiling low enough that a second rotation is forced while the kept
		// generation still holds records the cursor never passed.
		restore := shrinkCaps(t, 800, 2400)
		defer restore()

		appended := 0
		for i := 1; i <= 60; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent %d: %v", i, err)
			}
			appended++
		}
		got, stats := readAll(t, dir, "a")
		if len(got)+stats.Dropped != appended {
			t.Errorf("appended=%d readable=%d dropped=%d — the accounting invariant "+
				"appended == readable + dropped is broken; records went missing without being counted",
				appended, len(got), stats.Dropped)
		}
		if stats.Dropped == 0 {
			t.Errorf("no drop was recorded even though the hard ceiling forced one; a drop that is " +
				"not counted is exactly the silent loss the design forbids")
		}
		// Nothing was ever exported here, so every discarded record is real loss rather than
		// retention. Separating the two counts is what lets a status surface say how much data
		// a backend will never see, instead of reporting a number dominated by records it
		// already has.
		if stats.DroppedUnexported == 0 {
			t.Errorf("dropped=%d but droppedUnexported=0, although the cursor never advanced; "+
				"the count that measures actual loss is not being kept", stats.Dropped)
		}
		if stats.DroppedUnexported > stats.Dropped {
			t.Errorf("droppedUnexported=%d exceeds dropped=%d", stats.DroppedUnexported, stats.Dropped)
		}
		// Durable: a fresh read in a process that did no appending still sees the count.
		_, again := readAll(t, dir, "a")
		if again.Dropped != stats.Dropped {
			t.Errorf("dropped count is not durable: %d then %d", stats.Dropped, again.Dropped)
		}
		// And reachable through the surface the status verb will call.
		st, err := Stats(dir, "a")
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if st.Dropped != stats.Dropped {
			t.Errorf("Stats reports dropped=%d, ReadEvents reports %d", st.Dropped, stats.Dropped)
		}
	})

	t.Run("rotation proceeds freely once the cursor has passed the records", func(t *testing.T) {
		dir := mkStore(t)
		// A high hard ceiling on purpose: this subtest isolates the SAFE path, so the store
		// must never be pushed into a forced drop here or the assertion below could not tell
		// "rotated safely" apart from "rotated because it had no choice".
		restore := shrinkCaps(t, 800, 1<<20)
		defer restore()

		for i := 1; i <= 10; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent %d: %v", i, err)
			}
		}
		pending, _ := readAll(t, dir, "a")
		if err := markExported(dir, "a", pending); err != nil {
			t.Fatalf("advancing cursor: %v", err)
		}
		before, err := Stats(dir, "a")
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		// Backlog is what an operator reads to decide whether the exporter is keeping up, so
		// it is asserted rather than merely populated. Zero here proves the drain accounting
		// and the cursor agree.
		if before.Backlog != 0 {
			t.Errorf("backlog = %d immediately after exporting everything, want 0", before.Backlog)
		}
		for i := 11; i <= 30; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent %d: %v", i, err)
			}
		}
		after, err := Stats(dir, "a")
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		// The positive control. Without it, an implementation that simply never rotates
		// satisfies every "nothing was lost" assertion above by doing nothing at all.
		if _, err := os.Stat(kept(dir, "a")); err != nil {
			t.Fatalf("rotation did not occur even though the cursor had passed every record: %v", err)
		}
		if after.Dropped != before.Dropped {
			t.Errorf("dropped rose from %d to %d while discarding only records the cursor had passed",
				before.Dropped, after.Dropped)
		}
		if after.Backlog != 20 {
			t.Errorf("backlog = %d after appending 20 records past the cursor, want 20", after.Backlog)
		}
	})

	t.Run("the cursor survives rotation", func(t *testing.T) {
		dir := mkStore(t)
		restore := shrinkCaps(t, 900, 1<<20)
		defer restore()

		for i := 1; i <= 5; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent %d: %v", i, err)
			}
		}
		first, _ := readAll(t, dir, "a")
		if err := markExported(dir, "a", first); err != nil {
			t.Fatalf("advancing cursor: %v", err)
		}
		for i := 6; i <= 14; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent %d: %v", i, err)
			}
		}
		if _, err := os.Stat(kept(dir, "a")); err != nil {
			t.Fatalf("expected a rotation to have occurred: %v", err)
		}
		// A byte-offset or record-index cursor is silently re-based to zero by the rename.
		// Only a content-derived cursor survives, and the consequence of it not surviving is
		// that already-exported records are offered again as if new.
		unexported, _, err := ReadEvents(dir, Filter{Agent: "a", Unexported: true})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(unexported) != 9 {
			t.Fatalf("after rotation, %d records are pending export, want 9 — the cursor did not "+
				"survive the rename", len(unexported))
		}
		if unexported[0].StepID != "s6" {
			t.Errorf("first pending record is %q, want s6", unexported[0].StepID)
		}
	})

	t.Run("a malformed line is skipped and counted, never fatal", func(t *testing.T) {
		dir := mkStore(t)
		for i := 1; i <= 3; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent %d: %v", i, err)
			}
		}
		f, err := os.OpenFile(live(dir, "a"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("opening log: %v", err)
		}
		if _, err := f.WriteString("{this is not json\n\n{\"v\":1,\"event\":\n"); err != nil {
			t.Fatalf("writing corruption: %v", err)
		}
		f.Close()
		if err := AppendEvent(dir, ev("a", "i1", "s4", 4)); err != nil {
			t.Fatalf("AppendEvent after corruption: %v", err)
		}

		got, stats, err := ReadEvents(dir, Filter{Agent: "a"})
		if err != nil {
			t.Fatalf("ReadEvents returned an error for a corrupt line; a malformed line must be "+
				"skipped and counted, not fatal: %v", err)
		}
		if len(got) != 4 {
			t.Errorf("returned %d valid records, want 4 (the records surrounding the corruption)", len(got))
		}
		if stats.Malformed != 2 {
			t.Errorf("malformed = %d, want 2 (blank lines are not malformed)", stats.Malformed)
		}
		if stats.Dropped != 0 {
			t.Errorf("dropped = %d; a malformed line is not a drop and the two counts must stay distinct", stats.Dropped)
		}
	})

	t.Run("an oversized line does not silently end the scan", func(t *testing.T) {
		dir := mkStore(t)
		if err := AppendEvent(dir, ev("a", "i1", "before", 1)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		f, err := os.OpenFile(live(dir, "a"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("opening log: %v", err)
		}
		// Well past the 64 KiB default token limit of the obvious line reader. If the reader
		// stops here, the records after it disappear with no error and no count — a hole in
		// the data arriving through the reader instead of the rotator.
		if _, err := fmt.Fprintf(f, "%s\n", strings.Repeat("x", 512*1024)); err != nil {
			t.Fatalf("writing oversized line: %v", err)
		}
		f.Close()
		if err := AppendEvent(dir, ev("a", "i1", "after", 2)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		got, stats, err := ReadEvents(dir, Filter{Agent: "a"})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("returned %d records, want 2 — the oversized line truncated the read", len(got))
		}
		if got[1].StepID != "after" {
			t.Errorf("second record is %q, want the record written after the oversized line", got[1].StepID)
		}
		if stats.Malformed != 1 {
			t.Errorf("malformed = %d, want 1 (the oversized line is counted, not silently dropped)", stats.Malformed)
		}
	})

	t.Run("reads are filterable and bounded", func(t *testing.T) {
		dir := mkStore(t)
		for i := 1; i <= 4; i++ {
			if err := AppendEvent(dir, ev("a", "i1", fmt.Sprintf("s%d", i), i)); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		for i := 1; i <= 2; i++ {
			if err := AppendEvent(dir, ev("a", "i2", fmt.Sprintf("t%d", i), i)); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		byInstance, _, err := ReadEvents(dir, Filter{Agent: "a", InstanceID: "i2"})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(byInstance) != 2 {
			t.Errorf("instance filter returned %d records, want 2", len(byInstance))
		}
		limited, _, err := ReadEvents(dir, Filter{Agent: "a", Limit: 3})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(limited) != 3 {
			t.Errorf("limit returned %d records, want 3 — the bounded drain depends on this", len(limited))
		}
	})

	t.Run("an absent store reads empty rather than failing", func(t *testing.T) {
		dir := mkStore(t)
		got, stats, err := ReadEvents(dir, Filter{Agent: "never-wrote"})
		if err != nil {
			t.Fatalf("ReadEvents on an absent store: %v", err)
		}
		if len(got) != 0 || stats.Dropped != 0 || stats.Malformed != 0 {
			t.Errorf("got %d records, stats %+v; want an empty result", len(got), stats)
		}
	})

	t.Run("the record on disk is one JSON object per line", func(t *testing.T) {
		dir := mkStore(t)
		if err := AppendEvent(dir, ev("a", "i1", "s1", 1)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		raw, err := os.ReadFile(live(dir, "a"))
		if err != nil {
			t.Fatalf("reading log: %v", err)
		}
		if !strings.HasSuffix(string(raw), "\n") {
			t.Error("record is not newline-terminated; the next append would join two records")
		}
		if strings.Count(strings.TrimRight(string(raw), "\n"), "\n") != 0 {
			t.Error("record spans multiple lines; JSONL requires one object per line")
		}
		var decoded StepEvent
		if err := json.Unmarshal(raw, &decoded); err != nil {
			t.Fatalf("record does not round-trip: %v", err)
		}
		if decoded.StepID != "s1" || decoded.Status != StatusClosed {
			t.Errorf("round-tripped record = %+v", decoded)
		}
	})
}
