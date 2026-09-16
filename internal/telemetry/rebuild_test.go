package telemetry

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

const (
	rebuildFormula = "design-v7"
	rebuildModel   = "fable-5"
	rebuildStep    = "P3"
	rebuiltAt      = "2026-08-30T09:15:00.000Z"
)

func rebuiltKey() tokenomics.DigestKey {
	return tokenomics.DigestKey{Formula: rebuildFormula, StepID: rebuildStep, Model: rebuildModel}
}

// at stamps a record. Minute resolution is enough to order a fixture and keeps every timestamp in
// the layout the store writes, which is the layout the window join compares as a string.
func at(minute int) string {
	return fmt.Sprintf("2026-08-30T09:%02d:00.000Z", minute)
}

// recorded marks a figure as measured. The store's scalars are pointers precisely so that an
// absence stays distinguishable from a zero, and these fixtures honour that distinction.
func recorded(v int64) *int64 { return &v }

// rebuildRecord assembles the fields every record carries regardless of kind, the way
// telemetryRecordFor does at the verb layer.
func rebuildRecord(agent, instance, session, ts string) StepEvent {
	return StepEvent{
		V: SchemaVersion, TS: ts,
		Agent: agent, WorktreeID: "wt-b2fd81",
		InstanceID: instance, SessionID: session,
		Model: rebuildModel, ModelSource: ModelSourceModelsJSON,
		Verb: "done",
	}
}

// oneRun is a step that opened and closed inside a single session — the shape that records a
// per-session delta, and the shape a healthy step has.
func oneRun(agent, instance, session string, startMin int, peak, delta int64) []StepEvent {
	open := rebuildRecord(agent, instance, session, at(startMin))
	open.Event, open.Formula, open.StepID = EventStepStart, rebuildFormula, rebuildStep
	open.StepLabel = rebuildStep

	closed := rebuildRecord(agent, instance, session, at(startMin+1))
	closed.Event, closed.Formula, closed.StepID = EventStepEnd, rebuildFormula, rebuildStep
	closed.StepLabel = rebuildStep
	closed.Status = StatusClosed
	closed.DurationMS = 1_000
	closed.PeakCtxTokens, closed.CumTokensDelta = recorded(peak), recorded(delta)
	closed.OutTokens, closed.ThinkTokensEst = recorded(1_000), recorded(200)

	return []StepEvent{open, closed}
}

func seedStore(t *testing.T, telemetryDir string, records ...StepEvent) {
	t.Helper()
	for _, r := range records {
		if err := AppendEvent(telemetryDir, r); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
}

func TestDigestRebuild(t *testing.T) {
	// The digest is a CACHE, and this test is what makes that word true. It fails against: a hook
	// that folds one closing record into a loaded digest, because a median cannot be maintained
	// incrementally and the rebuild would then disagree with it; a rebuild that walks one agent's
	// log, because the same formula step is closed by several agents and each one's runs belong in
	// the same row; a reader that treats a deleted cache as anything but a cold start; and any
	// engine that lets one corrupt line at the tail of a log hide every record before it.

	root := t.TempDir()
	dir := config.TelemetryDir(root)
	roster := []string{"architect", "engineer"}

	var seed []StepEvent
	for _, run := range [][]StepEvent{
		oneRun("architect", "inst-a1", "sess-a1", 1, 40_000, 30_000),
		oneRun("architect", "inst-a2", "sess-a2", 3, 50_000, 35_000),
		oneRun("engineer", "inst-e1", "sess-e1", 5, 90_000, 60_000),
		oneRun("engineer", "inst-e2", "sess-e2", 7, 95_000, 65_000),
		oneRun("engineer", "inst-e3", "sess-e3", 9, 99_000, 70_000),
	} {
		seed = append(seed, run...)
	}
	seedStore(t, dir, seed...)

	digestPath := LearnedDigestPath(dir, rebuildFormula)

	// The af done path: at step close, re-derive the closing formula's aggregates from the store
	// and write them atomically.
	closing, stats := RebuildLearnedDigests(dir, roster, rebuildFormula, rebuiltAt)
	if stats.Malformed != 0 {
		t.Fatalf("Malformed = %d on a clean store, want 0", stats.Malformed)
	}
	if len(closing) != 1 {
		t.Fatalf("scoped rebuild returned %d formulas, want 1", len(closing))
	}
	if err := tokenomics.SaveDigest(digestPath, closing[rebuildFormula]); err != nil {
		t.Fatalf("SaveDigest: %v", err)
	}
	before, err := os.ReadFile(digestPath)
	if err != nil {
		t.Fatalf("reading the digest af done wrote: %v", err)
	}

	key := rebuiltKey()
	seeded, ok := closing[rebuildFormula].Lookup(key)
	if !ok {
		t.Fatalf("the scoped rebuild holds no aggregate for %+v; every comparison below would be between two empty digests", key)
	}
	// Every K6 figure named, not just the two the byte-equivalence rests on. The FOLD is pinned in
	// internal/tokenomics; what this pins is the MAPPING — which record field feeds which figure —
	// the seam where a scalar can be attached to the wrong figure and still produce a number that
	// looks plausible. Swapping out_tokens for think_tokens_est is the cheapest way to get a
	// thinking share of 5.0 and no test to say so.
	//
	// GenerationRuns is 0 while MedianOutTokens is 1000, and the pair is the mapping's sharpest
	// assertion (#678 K2): these records carry out_tokens and no exact think_tokens, so the median
	// folds from the runs that measured it while the generation trust floor counts the runs that
	// measured BOTH. A fold that let a run count toward the floor on one leg would report five
	// trusted generation runs here and let the efficiency predicate fire on a step whose thinking was
	// never measured at all.
	want := tokenomics.Aggregate{
		Runs:                 5,
		MedianPeakCtxTokens:  90_000,
		MaxPeakCtxTokens:     99_000,
		MedianCumTokensDelta: 60_000,
		MaxCumTokensDelta:    70_000,
		MedianDurationMS:     1_000,
		SessionsPerStep:      1,
		ThinkingShare:        0.2,
		MedianOutTokens:      1_000,
		UpdatedAt:            rebuiltAt,
	}
	if seeded != want {
		t.Fatalf("aggregate =\n  %+v\nwant\n  %+v", seeded, want)
	}

	t.Run("deleting the digest is safe", func(t *testing.T) {
		if err := os.Remove(digestPath); err != nil {
			t.Fatalf("removing the digest: %v", err)
		}
		d, err := tokenomics.LoadDigest(digestPath)
		if err != nil {
			t.Fatalf("LoadDigest after deletion: %v, want the cold start every factory begins in", err)
		}
		if n := tokenomics.Coverage(d); n != 0 {
			t.Errorf("coverage = %d after deletion, want 0", n)
		}
		if app := d.AppetiteFor(key); app.Known {
			t.Errorf("appetite = %+v, want unknown — a deleted cache falls back to observation-only, never to a wrong answer", app)
		}
	})

	t.Run("the rebuild reproduces the deleted digest byte for byte", func(t *testing.T) {
		// An empty formula scope is the operator verb's shape: every formula in the store. The file
		// it writes must be the one af done would have written, or the two callers are two
		// implementations of one cache and only one of them can be right.
		all, _ := RebuildLearnedDigests(dir, roster, "", rebuiltAt)
		if err := tokenomics.SaveDigest(digestPath, all[rebuildFormula]); err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}
		after, err := os.ReadFile(digestPath)
		if err != nil {
			t.Fatalf("reading the rebuilt digest: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Errorf("the rebuild is not byte-equivalent\nbefore: %s\n after: %s", before, after)
		}
	})

	t.Run("only the timestamp moves when the stamp changes", func(t *testing.T) {
		const laterStamp = "2027-01-01T00:00:00.000Z"

		later, _ := RebuildLearnedDigests(dir, roster, "", laterStamp)
		got, ok := later[rebuildFormula].Lookup(key)
		if !ok {
			t.Fatalf("no entry for %+v", key)
		}
		if got.UpdatedAt != laterStamp {
			t.Errorf("UpdatedAt = %q, want %q — the caller stamps the digest, the engine holds no clock", got.UpdatedAt, laterStamp)
		}
		want := seeded
		got.UpdatedAt, want.UpdatedAt = "", ""
		if got != want {
			t.Errorf("the rebuild differs by more than its timestamp:\ngot  %+v\nwant %+v", got, want)
		}
	})

	t.Run("a rebuild that read one agent's log would report a different median", func(t *testing.T) {
		one, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		partial, ok := one[rebuildFormula].Lookup(key)
		if !ok {
			t.Fatalf("no entry for %+v", key)
		}
		if partial.Runs != 2 {
			t.Errorf("Runs = %d, want 2 — one agent's contribution", partial.Runs)
		}
		if partial.MedianPeakCtxTokens == seeded.MedianPeakCtxTokens {
			t.Fatal("the partial roster reports the same median as the full one; the roster walk proves nothing")
		}
	})

	t.Run("a corrupt record line never blocks the rebuild", func(t *testing.T) {
		root := t.TempDir()
		dir := config.TelemetryDir(root)
		var records []StepEvent
		for _, run := range [][]StepEvent{
			oneRun("architect", "inst-c1", "sess-c1", 1, 40_000, 30_000),
			oneRun("architect", "inst-c2", "sess-c2", 3, 50_000, 35_000),
			oneRun("architect", "inst-c3", "sess-c3", 5, 60_000, 45_000),
		} {
			records = append(records, run...)
		}
		seedStore(t, dir, records...)

		// The shape a crash mid-write leaves behind.
		f, err := os.OpenFile(filepath.Join(dir, "steps", "architect.jsonl"), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("opening the log: %v", err)
		}
		if _, err := f.WriteString(`{"v":1,"event":"step_end"` + "\n"); err != nil {
			t.Fatalf("appending a truncated line: %v", err)
		}
		if err := f.Close(); err != nil {
			t.Fatalf("closing the log: %v", err)
		}

		m, stats := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		if stats.Malformed != 1 {
			t.Errorf("Malformed = %d, want 1 — the line is counted, never swallowed", stats.Malformed)
		}
		a, ok := m[rebuildFormula].Lookup(key)
		if !ok {
			t.Fatalf("no entry for %+v", key)
		}
		if a.Runs != 3 {
			t.Errorf("Runs = %d, want 3 — every parseable record before the corruption still aggregates", a.Runs)
		}
	})

	t.Run("a formula name that is not a safe path segment produces no digest", func(t *testing.T) {
		root := t.TempDir()
		dir := config.TelemetryDir(root)

		var records []StepEvent
		for i, name := range []string{"", ".", "..", "../escape", "steps" + string(os.PathSeparator) + "x"} {
			run := oneRun("architect", fmt.Sprintf("inst-u%d", i), fmt.Sprintf("sess-u%d", i), 1+2*i, 40_000, 30_000)
			for j := range run {
				run[j].Formula = name
			}
			records = append(records, run...)
		}
		records = append(records, oneRun("architect", "inst-safe", "sess-safe", 20, 40_000, 30_000)...)
		seedStore(t, dir, records...)

		m, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		if len(m) != 1 {
			t.Fatalf("rebuild returned %d formulas, want 1 — a name that cannot be a filename must never compose one", len(m))
		}
		if _, ok := m[rebuildFormula]; !ok {
			t.Fatalf("the safe formula is missing from %v; the count above proves nothing", m)
		}
		digestDir := LearnedDigestDir(dir)
		for name := range m {
			if got := filepath.Dir(LearnedDigestPath(dir, name)); got != digestDir {
				t.Errorf("formula %q composes a path in %q, outside %q", name, got, digestDir)
			}
		}
	})

	t.Run("an opening record is not a run", func(t *testing.T) {
		root := t.TempDir()
		dir := config.TelemetryDir(root)

		run := oneRun("architect", "inst-o1", "sess-o1", 1, 40_000, 30_000)
		// An opening record carries occupancy figures of its own. Folding it in would count one
		// step as two runs and mix an opening level into a peak.
		run[0].PeakCtxTokens = recorded(900_000)
		seedStore(t, dir, run...)

		m, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		a, ok := m[rebuildFormula].Lookup(key)
		if !ok {
			t.Fatalf("no entry for %+v", key)
		}
		if a.Runs != 1 {
			t.Errorf("Runs = %d, want 1", a.Runs)
		}
		if a.MedianPeakCtxTokens != 40_000 {
			t.Errorf("MedianPeakCtxTokens = %d, want 40000 — the closing record's figure", a.MedianPeakCtxTokens)
		}
	})

	t.Run("a step that spanned three sessions reports three", func(t *testing.T) {
		root := t.TempDir()
		dir := config.TelemetryDir(root)

		open := rebuildRecord("architect", "inst-f1", "sess-1", at(1))
		open.Event, open.Formula, open.StepID = EventStepStart, rebuildFormula, rebuildStep
		open.StepLabel = rebuildStep
		second := rebuildRecord("architect", "inst-f1", "sess-2", at(2))
		second.Event = EventSessionStart
		third := rebuildRecord("architect", "inst-f1", "sess-3", at(3))
		third.Event = EventSessionStart
		// No delta: af done refuses one when the step did not close in the session that opened it,
		// which is exactly why H-R5 has to reconstruct the appetite from the session count.
		closed := rebuildRecord("architect", "inst-f1", "sess-3", at(4))
		closed.Event, closed.Formula, closed.StepID = EventStepEnd, rebuildFormula, rebuildStep
		closed.StepLabel = rebuildStep
		closed.Status, closed.DurationMS = StatusClosed, 1_000
		seedStore(t, dir, open, second, third, closed)

		m, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		a, ok := m[rebuildFormula].Lookup(key)
		if !ok {
			t.Fatalf("no entry for %+v", key)
		}
		if a.Runs != 1 {
			t.Errorf("Runs = %d, want 1 — the session_start records are not runs", a.Runs)
		}
		if a.SessionsPerStep != 3 {
			t.Errorf("SessionsPerStep = %d, want 3 — the sessions between the open and the close are the fragmentation", a.SessionsPerStep)
		}
	})

	t.Run("a session that never announced itself is not a session the step crossed", func(t *testing.T) {
		// #678 K1 / six-sigma Gap 23. Before the pane guard in af prime, the quality-gate grader's
		// `claude -p` fired the agent's SessionStart hook from the agent's own directory and took the
		// agent's session identity. Its records land inside the step's window carrying the instance
		// id, so the old count — every distinct session id in the window — read a graded step as a
		// step that recycled. The steps graded hardest scored worst on fragmentation, which is the
		// exact opposite of the truth.
		//
		// The interloper here writes NEITHER a session_start (the guard stops that) NOR a step
		// boundary (af prime and af done write those; a grader runs neither), so it is invisible to
		// both halves of the union rule while remaining a real record inside the window.
		root := t.TempDir()
		dir := config.TelemetryDir(root)

		open := rebuildRecord("architect", "inst-g1", "sess-agent", at(1))
		open.Event, open.Formula, open.StepID = EventStepStart, rebuildFormula, rebuildStep
		open.StepLabel = rebuildStep

		grader := rebuildRecord("architect", "inst-g1", "sess-grader", at(2))
		grader.Event, grader.Verb = EventIntervention, "done"
		grader.Mechanism, grader.Action = "gate", ActionAdvise

		closed := rebuildRecord("architect", "inst-g1", "sess-agent", at(3))
		closed.Event, closed.Formula, closed.StepID = EventStepEnd, rebuildFormula, rebuildStep
		closed.StepLabel = rebuildStep
		closed.Status, closed.DurationMS = StatusClosed, 1_000
		closed.PeakCtxTokens, closed.CumTokensDelta = recorded(40_000), recorded(9_000)
		seedStore(t, dir, open, grader, closed)

		m, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		a, ok := m[rebuildFormula].Lookup(key)
		if !ok {
			t.Fatalf("no entry for %+v", key)
		}
		if a.SessionsPerStep != 1 {
			t.Errorf("SessionsPerStep = %d, want 1 — the step ran in one session; a record from a "+
				"session that announced neither a start nor a step boundary is not fragmentation", a.SessionsPerStep)
		}
	})

	t.Run("a session the step did not span is not counted against it", func(t *testing.T) {
		root := t.TempDir()
		dir := config.TelemetryDir(root)

		// One instance, two steps, a recycle between them. Each step fits one session; the instance
		// used two. Counting every session the INSTANCE ever used rather than the ones inside the
		// step's own window would report 2 for both, turning sessions_per_step > 1 — a no-fit
		// signal in its own right — into a false positive on a step that fit perfectly, and
		// doubling the reconstructed appetite for a peakless one.
		first := oneRun("architect", "inst-w1", "sess-1", 1, 40_000, 30_000)
		recycle := rebuildRecord("architect", "inst-w1", "sess-2", at(5))
		recycle.Event = EventSessionStart
		second := rebuildRecord("architect", "inst-w1", "sess-2", at(7))
		second.Event, second.Formula, second.StepID = EventStepStart, rebuildFormula, "P4"
		second.StepLabel = "P4"
		secondClose := rebuildRecord("architect", "inst-w1", "sess-2", at(8))
		secondClose.Event, secondClose.Formula, secondClose.StepID = EventStepEnd, rebuildFormula, "P4"
		secondClose.StepLabel = "P4"
		secondClose.Status, secondClose.DurationMS = StatusClosed, 1_000
		secondClose.PeakCtxTokens = recorded(50_000)

		seedStore(t, dir, append(first, recycle, second, secondClose)...)

		m, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		for _, step := range []string{rebuildStep, "P4"} {
			a, ok := m[rebuildFormula].Lookup(tokenomics.DigestKey{Formula: rebuildFormula, StepID: step, Model: rebuildModel})
			if !ok {
				t.Fatalf("no entry for step %s", step)
			}
			if a.SessionsPerStep != 1 {
				t.Errorf("step %s SessionsPerStep = %d, want 1 — it closed in the session that opened it", step, a.SessionsPerStep)
			}
		}
	})

	t.Run("two runs of one step are two runs, not one", func(t *testing.T) {
		root := t.TempDir()
		dir := config.TelemetryDir(root)

		// The same step id recurs on every instantiation of a formula. Collapse the instance and the
		// later run's session count overwrites the earlier one's, so the fragmented run — the case
		// H-R5 exists for — reports the healthy run's count and falls through to unknown.
		open := rebuildRecord("architect", "inst-x1", "sess-1", at(1))
		open.Event, open.Formula, open.StepID = EventStepStart, rebuildFormula, rebuildStep
		open.StepLabel = rebuildStep
		second := rebuildRecord("architect", "inst-x1", "sess-2", at(2))
		second.Event = EventSessionStart
		third := rebuildRecord("architect", "inst-x1", "sess-3", at(3))
		third.Event = EventSessionStart
		fragmented := rebuildRecord("architect", "inst-x1", "sess-3", at(4))
		fragmented.Event, fragmented.Formula, fragmented.StepID = EventStepEnd, rebuildFormula, rebuildStep
		fragmented.StepLabel = rebuildStep
		fragmented.Status, fragmented.DurationMS = StatusClosed, 1_000
		fragmented.CumTokensDelta = recorded(60_000)

		// A later instance of the same step that fit in one session. It sorts after the fragmented
		// run, so a collapsed key ends holding ITS count.
		healthy := oneRun("architect", "inst-x2", "sess-9", 20, 0, 60_000)
		healthy[1].PeakCtxTokens = nil

		seedStore(t, dir, append([]StepEvent{open, second, third, fragmented}, healthy...)...)

		m, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		a, ok := m[rebuildFormula].Lookup(key)
		if !ok {
			t.Fatalf("no entry for %+v", key)
		}
		if a.Runs != 2 {
			t.Fatalf("Runs = %d, want 2", a.Runs)
		}
		if a.SessionsPerStep != 3 {
			t.Errorf("SessionsPerStep = %d, want 3 — each run carries its own count, and the fragmented one is the median", a.SessionsPerStep)
		}
		if app := m[rebuildFormula].PeakAppetiteFor(key); !app.Known || !app.Reconstructed {
			t.Errorf("appetite = %+v, want a reconstruction — collapsing the runs is how H-R5 loses the step it exists for", app)
		}
	})

	t.Run("an unmeasured duration is skipped, never folded in as a zero", func(t *testing.T) {
		root := t.TempDir()
		dir := config.TelemetryDir(root)

		var records []StepEvent
		for i, ms := range []int{0, 1_000, 3_000} {
			run := oneRun("architect", fmt.Sprintf("inst-d%d", i), fmt.Sprintf("sess-d%d", i), 1+2*i, 40_000, 30_000)
			run[1].DurationMS = ms
			records = append(records, run...)
		}
		seedStore(t, dir, records...)

		m, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		a, ok := m[rebuildFormula].Lookup(key)
		if !ok {
			t.Fatalf("no entry for %+v", key)
		}
		if a.Runs != 3 {
			t.Errorf("Runs = %d, want 3 — an unmeasured duration still happened", a.Runs)
		}
		// Duration is the one figure the record stores unboxed, so absence and zero are the same
		// bytes on the wire and the lift onto the pointer discipline is what keeps them apart. Fold
		// the zero in and the median is 1000 instead of 3000: a step reported as faster than any
		// run of it that was actually timed.
		if a.MedianDurationMS != 3_000 {
			t.Errorf("MedianDurationMS = %d, want 3000 — the zero is an absence, not a run that took no time", a.MedianDurationMS)
		}
	})

	t.Run("the closing formula's scope narrows the write to it", func(t *testing.T) {
		const other = "review-v2"

		root := t.TempDir()
		dir := config.TelemetryDir(root)

		records := oneRun("architect", "inst-s1", "sess-s1", 1, 40_000, 30_000)
		otherRun := oneRun("architect", "inst-s2", "sess-s2", 3, 50_000, 35_000)
		for i := range otherRun {
			otherRun[i].Formula = other
		}
		records = append(records, otherRun...)
		seedStore(t, dir, records...)

		// af done's shape. A close of one formula must not rewrite every other formula's file: the
		// hot path already re-derives the whole store, and widening the WRITE would multiply that
		// cost by the number of formulas a factory has ever run.
		scoped, _ := RebuildLearnedDigests(dir, []string{"architect"}, rebuildFormula, rebuiltAt)
		if len(scoped) != 1 {
			t.Fatalf("the scoped rebuild returned %d formulas, want 1: %v", len(scoped), scoped)
		}
		if _, ok := scoped[other]; ok {
			t.Errorf("closing %q also rebuilt %q", rebuildFormula, other)
		}

		// The operator verb's shape, in the same fixture — otherwise "returned 1" would be
		// satisfied by a store that only ever held one formula.
		all, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		if len(all) != 2 {
			t.Fatalf("the unscoped rebuild returned %d formulas, want 2: %v", len(all), all)
		}
	})

	t.Run("an empty store rebuilds to nothing, not to an error", func(t *testing.T) {
		dir := config.TelemetryDir(t.TempDir())

		m, _ := RebuildLearnedDigests(dir, []string{"architect"}, "", rebuiltAt)
		if len(m) != 0 {
			t.Errorf("rebuild returned %d formulas, want 0", len(m))
		}
	})

	t.Run("an unreadable agent log costs only that agent", func(t *testing.T) {
		dir := config.TelemetryDir(t.TempDir())
		seedStore(t, dir, oneRun("engineer", "inst-e1", "sess-e1", 1, 90_000, 60_000)...)
		// A directory where a record file belongs. Unreadable whatever uid the suite runs as,
		// which a permission bit cannot promise when the tests run as root.
		if err := os.MkdirAll(filepath.Join(dir, "steps", "architect.jsonl"), 0o755); err != nil {
			t.Fatalf("occupying architect's log path: %v", err)
		}

		m, stats := RebuildLearnedDigests(dir, []string{"architect", "engineer"}, "", rebuiltAt)
		if stats.UnreadableAgents != 1 {
			t.Errorf("UnreadableAgents = %d, want 1 — an unreadable log the caller cannot see is one it cannot report", stats.UnreadableAgents)
		}
		a, ok := m[rebuildFormula].Lookup(rebuiltKey())
		if !ok {
			t.Fatalf("one unreadable log cost the whole rebuild; the readable agent's runs are gone: %+v", m)
		}
		if a.Runs != 1 {
			t.Errorf("Runs = %d, want 1", a.Runs)
		}
	})

	t.Run("the standing digest outlives the records it was built from", func(t *testing.T) {
		// The horizon this cache exists to cross. One kept generation means a step's records
		// eventually age out, and a digest that only ever re-derived would drop the step back to
		// cold start with a full cache on disk — the flaw that makes deriving alone insufficient.
		dir := config.TelemetryDir(t.TempDir())
		roster := []string{"engineer"}
		seedStore(t, dir,
			append(oneRun("engineer", "inst-e1", "sess-e1", 1, 90_000, 60_000),
				oneRun("engineer", "inst-e2", "sess-e2", 3, 95_000, 65_000)...)...)

		derived, _ := RebuildLearnedDigests(dir, roster, rebuildFormula, rebuiltAt)
		stored := tokenomics.MergeDigests(tokenomics.NewDigest(), derived[rebuildFormula])
		if a, ok := stored.Lookup(rebuiltKey()); !ok || a.Runs != 2 {
			t.Fatalf("the fixture did not record two runs to lose: %+v", stored)
		}

		// Caps below one record: every append rotates. Two appends are needed, not one — a
		// rotation moves the live log onto the kept generation, so it takes the SECOND to
		// overwrite the seeded runs and put them genuinely out of reach.
		shrinkCaps(t, 1, 2)
		later := func(instance string, minute int) StepEvent {
			r := rebuildRecord("engineer", instance, "sess-"+instance, at(minute))
			r.Event, r.Formula, r.StepID = EventStepEnd, rebuildFormula, "P9"
			r.StepLabel = "P9"
			r.Status, r.DurationMS = StatusClosed, 1_000
			r.PeakCtxTokens, r.CumTokensDelta = recorded(10_000), recorded(5_000)
			return r
		}
		seedStore(t, dir, later("inst-e9", 30), later("inst-e10", 32))

		aged, _ := RebuildLearnedDigests(dir, roster, rebuildFormula, at(31))
		if _, ok := aged[rebuildFormula].Lookup(rebuiltKey()); ok {
			t.Fatal("the records did not age out; this subtest would pass against a digest that carries nothing forward")
		}

		merged := tokenomics.MergeDigests(stored, aged[rebuildFormula])
		carried, ok := merged.Lookup(rebuiltKey())
		if !ok {
			t.Fatalf("the aged-out step is gone from the digest too; the cache learned nothing it can keep: %+v", merged)
		}
		if carried.Runs != 2 || carried.MedianPeakCtxTokens != 95_000 {
			t.Errorf("carried aggregate = %+v, want the two runs the records no longer hold", carried)
		}
		if _, ok := merged.Lookup(tokenomics.DigestKey{Formula: rebuildFormula, StepID: "P9", Model: rebuildModel}); !ok {
			t.Error("the surviving record's own row is missing; carrying history forward must not stop the cache learning")
		}
	})
}

// TestRebuild_FoldsMarginalFromStart is thread T1's telemetry consumer pin (plan items 2 and 3): the
// record already carries ctx_tokens_start (event.go, written at done.go), so the ONLY missing link is
// the rebuild — samplesFrom must map it onto the sample, and the fold must take the median of
// (peak − start) as MedianMarginalCtxTokens, the figure the additive admission predicate reads. This
// pins the MAPPING and the FOLD together: a marginal that never leaves the record leaves the additive
// decision with no learned data, degrading it to occupancy-only exactly where the double-count was.
func TestRebuild_FoldsMarginalFromStart(t *testing.T) {
	root := t.TempDir()
	dir := config.TelemetryDir(root)

	// One healthy run whose closing record carries both the step's start and its peak occupancy.
	withStart := func(instance, session string, startMin, start, peak int64) []StepEvent {
		open := rebuildRecord("architect", instance, session, at(int(startMin)))
		open.Event, open.Formula, open.StepID = EventStepStart, rebuildFormula, rebuildStep
		open.StepLabel = rebuildStep

		closed := rebuildRecord("architect", instance, session, at(int(startMin)+1))
		closed.Event, closed.Formula, closed.StepID = EventStepEnd, rebuildFormula, rebuildStep
		closed.StepLabel = rebuildStep
		closed.Status, closed.DurationMS = StatusClosed, 1_000
		closed.PeakCtxTokens, closed.CtxTokensStart = recorded(peak), recorded(start)
		return []StepEvent{open, closed}
	}

	var seed []StepEvent
	// Marginals 4000 / 5000 / 6000 ⇒ median 5000; peaks 104000 / 105000 / 106000 ⇒ median 105000.
	seed = append(seed, withStart("inst-1", "sess-1", 1, 100_000, 104_000)...)
	seed = append(seed, withStart("inst-2", "sess-2", 3, 100_000, 105_000)...)
	seed = append(seed, withStart("inst-3", "sess-3", 5, 100_000, 106_000)...)
	seedStore(t, dir, seed...)

	m, _ := RebuildLearnedDigests(dir, []string{"architect"}, rebuildFormula, rebuiltAt)
	a, ok := m[rebuildFormula].Lookup(rebuiltKey())
	if !ok {
		t.Fatalf("no entry for %+v", rebuiltKey())
	}
	if a.MedianMarginalCtxTokens != 5_000 {
		t.Errorf("MedianMarginalCtxTokens = %d, want 5000 — the median of (peak − start); the record's "+
			"ctx_tokens_start must map through samplesFrom and fold into the aggregate", a.MedianMarginalCtxTokens)
	}
	// The absolute peak is folded independently — the marginal is added BESIDE it, never in place of it.
	if a.MedianPeakCtxTokens != 105_000 {
		t.Errorf("MedianPeakCtxTokens = %d, want 105000 — the marginal fold must not disturb the peak fold", a.MedianPeakCtxTokens)
	}
}

// TestSamplesFromJoinsFormulaDigestFromInstanceStart pins the two legs of a sample that are NOT on
// the record the sample is built from (#678 K2).
//
// The records are append-only, so neither leg can be moved onto step_end retroactively: which
// program a run executed is on its instance_start and which objective chose its effort level is on
// the intervention the actuator wrote. A join that silently produced nothing would leave the digest
// partition with no source and the treatment arm indistinguishable from the control arm, and every
// aggregation test in internal/tokenomics would still pass — they take the sample as given.
func TestSamplesFromJoinsFormulaDigestFromInstanceStart(t *testing.T) {
	open := func(instance, ts, digest string) StepEvent {
		r := rebuildRecord("engineer", instance, "sess-"+instance, ts)
		r.Event, r.Formula, r.FormulaDigest = EventInstanceStart, rebuildFormula, digest
		return r
	}
	// reduceEffort attests that a SESSION ran reduced (#679 F1). It carries the step it happened to
	// name, but the arm it decides is the session's — a step id here is not a per-step join key.
	reduceEffort := func(instance, session, stepID, objective string) StepEvent {
		r := rebuildRecord("engineer", instance, session, at(2))
		r.Event, r.Formula, r.StepID, r.Objective = EventIntervention, rebuildFormula, stepID, objective
		r.Mechanism, r.Action, r.EffortLevel = "effort", ActionReduceEffort, "medium"
		return r
	}
	// advise is counsel the agent could ignore: it keeps its objective but changes no effort level,
	// so it must NOT decide the arm.
	advise := func(instance, session, objective string) StepEvent {
		r := rebuildRecord("engineer", instance, session, at(2))
		r.Event, r.Formula, r.Objective = EventIntervention, rebuildFormula, objective
		r.Mechanism, r.Action = "thrift", ActionAdvise
		return r
	}

	t.Run("the digest and start stamp reach the sample", func(t *testing.T) {
		records := append([]StepEvent{open("inst-a1", at(0), "sha-abc")}, oneRun("engineer", "inst-a1", "sess-a1", 1, 40_000, 30_000)...)

		got := samplesFrom(records)
		if len(got) != 1 {
			t.Fatalf("samplesFrom returned %d samples, want 1", len(got))
		}
		if got[0].FormulaDigest != "sha-abc" {
			t.Errorf("FormulaDigest = %q, want %q — it lives only on instance_start", got[0].FormulaDigest, "sha-abc")
		}
		if got[0].InstanceStartedAt != at(0) {
			t.Errorf("InstanceStartedAt = %q, want %q — the partition orders programs by it", got[0].InstanceStartedAt, at(0))
		}
	})

	t.Run("the join is by instance, not by proximity in the log", func(t *testing.T) {
		// Two instances interleaved. A join that carried the last-seen opening record forward would
		// stamp both steps with whichever instance opened most recently.
		records := []StepEvent{open("inst-a1", at(0), "sha-abc"), open("inst-b1", at(4), "sha-def")}
		records = append(records, oneRun("engineer", "inst-b1", "sess-b1", 5, 50_000, 35_000)...)
		records = append(records, oneRun("engineer", "inst-a1", "sess-a1", 1, 40_000, 30_000)...)

		byDigest := map[string]int64{}
		for _, s := range samplesFrom(records) {
			byDigest[s.FormulaDigest] = *s.PeakCtxTokens
		}
		if byDigest["sha-abc"] != 40_000 || byDigest["sha-def"] != 50_000 {
			t.Errorf("samples joined to %v, want sha-abc→40000 and sha-def→50000", byDigest)
		}
	})

	t.Run("a step with no opening record carries no digest", func(t *testing.T) {
		// The rotation case, and every record written before the leg existed. An empty digest is
		// "unknown", which the aggregation reads as a wildcard rather than as a program of its own.
		got := samplesFrom(oneRun("engineer", "inst-a1", "sess-a1", 1, 40_000, 30_000))
		if len(got) != 1 || got[0].FormulaDigest != "" {
			t.Errorf("samples = %+v, want one sample with an empty digest", got)
		}
	})

	t.Run("the reduce_effort record names the arm the run's session belongs to", func(t *testing.T) {
		// #679 F1 reconcile: the arm is now derived from the SESSION's reduce_effort record rather
		// than from a per-step {instance, step} join. The reduce_effort is on the run's own session.
		run := oneRun("engineer", "inst-a1", "sess-a1", 1, 40_000, 30_000)
		records := append(run, reduceEffort("inst-a1", "sess-a1", rebuildStep, ObjectiveEfficiency))

		got := samplesFrom(records)
		if len(got) != 1 {
			t.Fatalf("samplesFrom returned %d samples, want 1", len(got))
		}
		if got[0].Objective != tokenomics.ObjectiveEfficiency {
			t.Errorf("Objective = %q, want %q — the run's session ran reduced", got[0].Objective, tokenomics.ObjectiveEfficiency)
		}
	})

	t.Run("efficiency wins over any other objective on the same session", func(t *testing.T) {
		// #679 F1 reconcile: the tiebreak now lives on the SESSION's reduce_effort records. A session
		// whose effort level was chosen by the efficiency actuator is treatment whatever else fired on
		// it; a capacity handoff later in the same session does not put it back in the control arm.
		run := oneRun("engineer", "inst-a1", "sess-a1", 1, 40_000, 30_000)
		records := append(run,
			reduceEffort("inst-a1", "sess-a1", rebuildStep, ObjectiveEfficiency),
			reduceEffort("inst-a1", "sess-a1", rebuildStep, ObjectiveCapacity))

		if got := samplesFrom(records); got[0].Objective != tokenomics.ObjectiveEfficiency {
			t.Errorf("Objective = %q, want %q", got[0].Objective, tokenomics.ObjectiveEfficiency)
		}
	})

	t.Run("the arm follows the session, not a per-step intervention", func(t *testing.T) {
		// #679 F1 reconcile: a run in a REDUCED session is treatment even with no intervention on its
		// own step — the reduce_effort record need not name the step. A run in a default session with
		// no reduce_effort stays control. This is the anti-vacuity row: samplesFrom does not always
		// report efficiency.
		reduced := oneRun("engineer", "inst-a1", "sess-reduced", 1, 40_000, 30_000)
		reduced = append(reduced, reduceEffort("inst-a1", "sess-reduced", "P9", ObjectiveEfficiency))
		if got := samplesFrom(reduced); got[0].Objective != tokenomics.ObjectiveEfficiency {
			t.Errorf("Objective = %q, want efficiency — the run's session was reduced though no intervention named its step", got[0].Objective)
		}
		if got := samplesFrom(oneRun("engineer", "inst-a2", "sess-default", 3, 40_000, 30_000)); got[0].Objective != "" {
			t.Errorf("Objective = %q, want empty — a default session no reduce_effort marked stays control", got[0].Objective)
		}
	})

	t.Run("the arm's scope is exactly the session, not the step and not the instance", func(t *testing.T) {
		// #679 F1 reconcile, the highest-risk row. Under session-keying a reduce_effort in the run's
		// SESSION marks it regardless of which step the record names — so scope must be proven to be
		// the session and nothing wider or narrower. A reduce_effort on the same session but a
		// different step DOES reach (not step-scoped); one on a different session of the same instance
		// does NOT reach (not instance-scoped); and an advisory on the run's own session does NOT
		// decide the arm. Together these pin the scope at the session, which is what separates
		// this reconcile from a weakening to "any step in the instance."
		run := oneRun("engineer", "inst-a1", "sess-a1", 1, 40_000, 30_000)

		sameSessionOtherStep := append(append([]StepEvent{}, run...), reduceEffort("inst-a1", "sess-a1", "P9", ObjectiveEfficiency))
		if got := samplesFrom(sameSessionOtherStep); got[0].Objective != tokenomics.ObjectiveEfficiency {
			t.Errorf("Objective = %q, want efficiency — a reduce_effort on this run's session marks it whatever step it names", got[0].Objective)
		}

		otherSessionSameInstance := append(append([]StepEvent{}, run...), reduceEffort("inst-a1", "sess-other", rebuildStep, ObjectiveEfficiency))
		if got := samplesFrom(otherSessionSameInstance); got[0].Objective != "" {
			t.Errorf("Objective = %q, want empty — a reduce_effort on another session of the same instance must not reach this one", got[0].Objective)
		}

		advisedOwnSession := append(append([]StepEvent{}, run...), advise("inst-a1", "sess-a1", ObjectiveEfficiency))
		if got := samplesFrom(advisedOwnSession); got[0].Objective != "" {
			t.Errorf("Objective = %q, want empty — an advisory keeps its objective but does not decide the arm", got[0].Objective)
		}
	})

	t.Run("the generation legs P1 landed reach the sample", func(t *testing.T) {
		run := oneRun("engineer", "inst-a1", "sess-a1", 1, 40_000, 30_000)
		end := &run[len(run)-1]
		end.ThinkTokens, end.SubagentTokens = recorded(910), recorded(5_000)
		end.SubagentLaunches, end.RepeatReads, end.GateFlags = recorded(3), recorded(7), recorded(2)
		end.EffortLevel = "medium"

		got := samplesFrom(run)[0]
		for _, tc := range []struct {
			name string
			got  *int64
			want int64
		}{
			{"ThinkTokens", got.ThinkTokens, 910},
			{"SubagentTokens", got.SubagentTokens, 5_000},
			{"SubagentLaunches", got.SubagentLaunches, 3},
			{"RepeatReads", got.RepeatReads, 7},
			{"GateFlags", got.GateFlags, 2},
		} {
			if tc.got == nil || *tc.got != tc.want {
				t.Errorf("%s = %v, want %d — a mapping that attached the wrong scalar still produces a plausible number",
					tc.name, tc.got, tc.want)
			}
		}
		if got.EffortLevel != "medium" {
			t.Errorf("EffortLevel = %q, want %q", got.EffortLevel, "medium")
		}
	})
}

// TestSamplesFromKeysArmOnSession pins F1/T1: the experiment arm is a property of the SESSION, not of
// a per-step intervention join. samplesFrom derives each step's arm from the session's reduce_effort
// record, so two cases a per-step {instance, step} join would misfile come out right:
//
//	(a) A session launched reduced (a reduce_effort record attesting the treatment on that session)
//	    whose step wrote no intervention of its own still belongs to the REDUCED arm — the session was
//	    reduced whether or not that particular step fired, so its Objective is efficiency and it folds
//	    into ReducedRuns.
//	(b) A default-level session that received only an ADVISORY efficiency intervention (Action=advise,
//	    no effort change) stays BASELINE — an advisory keeps its objective but does NOT decide the arm;
//	    only an Action=reduce_effort record marks a session reduced.
func TestSamplesFromKeysArmOnSession(t *testing.T) {
	// (a) inst-r ran a step s4 in session sess-reduced, which a reduce_effort record marked as reduced.
	// The reduce_effort attests the SESSION's treatment and carries no step id of its own, so a
	// per-step join could not reach s4.
	reduceEffort := rebuildRecord("engineer", "inst-r", "sess-reduced", at(1))
	reduceEffort.Event, reduceEffort.Formula = EventIntervention, rebuildFormula
	reduceEffort.Mechanism, reduceEffort.Action = "effort", ActionReduceEffort
	reduceEffort.Objective, reduceEffort.EffortLevel = ObjectiveEfficiency, "low"

	s4open := rebuildRecord("engineer", "inst-r", "sess-reduced", at(2))
	s4open.Event, s4open.Formula, s4open.StepID, s4open.StepLabel = EventStepStart, rebuildFormula, "bd-s4", "s4"
	s4close := rebuildRecord("engineer", "inst-r", "sess-reduced", at(3))
	s4close.Event, s4close.Formula, s4close.StepID, s4close.StepLabel = EventStepEnd, rebuildFormula, "bd-s4", "s4"
	s4close.Status, s4close.DurationMS = StatusClosed, 1_000
	s4close.OutTokens, s4close.ThinkTokens = recorded(1_000), recorded(500)

	// (b) inst-d ran a step s5 in a default-level session sess-default. A thrift advisory fired on that
	// step carrying the objective but changing no effort level.
	advisory := rebuildRecord("engineer", "inst-d", "sess-default", at(5))
	advisory.Event, advisory.Formula = EventIntervention, rebuildFormula
	advisory.StepID = "bd-s5"
	advisory.Mechanism, advisory.Action = "thrift", ActionAdvise
	advisory.Objective = ObjectiveEfficiency

	s5open := rebuildRecord("engineer", "inst-d", "sess-default", at(6))
	s5open.Event, s5open.Formula, s5open.StepID, s5open.StepLabel = EventStepStart, rebuildFormula, "bd-s5", "s5"
	s5close := rebuildRecord("engineer", "inst-d", "sess-default", at(7))
	s5close.Event, s5close.Formula, s5close.StepID, s5close.StepLabel = EventStepEnd, rebuildFormula, "bd-s5", "s5"
	s5close.Status, s5close.DurationMS = StatusClosed, 1_000
	s5close.OutTokens, s5close.ThinkTokens = recorded(2_000), recorded(600)

	records := []StepEvent{reduceEffort, s4open, s4close, advisory, s5open, s5close}

	byStep := map[string][]tokenomics.StepSample{}
	for _, s := range samplesFrom(records) {
		byStep[s.Key.StepID] = append(byStep[s.Key.StepID], s)
	}

	s4 := byStep["s4"]
	if len(s4) != 1 {
		t.Fatalf("samplesFrom produced %d samples for s4, want 1", len(s4))
	}
	if s4[0].Objective != tokenomics.ObjectiveEfficiency {
		t.Errorf("s4 Objective = %q, want %q — its session was launched reduced (a reduce_effort record "+
			"on sess-reduced); the arm is a property of the session, not of a per-step intervention join",
			s4[0].Objective, tokenomics.ObjectiveEfficiency)
	}
	if agg := tokenomics.AggregateSamples(s4, rebuiltAt); agg.ReducedRuns != 1 {
		t.Errorf("s4 ReducedRuns = %d, want 1 — a reduced session's step belongs to the treatment arm", agg.ReducedRuns)
	}

	s5 := byStep["s5"]
	if len(s5) != 1 {
		t.Fatalf("samplesFrom produced %d samples for s5, want 1", len(s5))
	}
	if s5[0].Objective == tokenomics.ObjectiveEfficiency {
		t.Errorf("s5 Objective = %q, want it NOT efficiency — an advisory (Action=advise) keeps its "+
			"objective but does not decide the arm; only a reduce_effort marks a session reduced",
			s5[0].Objective)
	}
	if agg := tokenomics.AggregateSamples(s5, rebuiltAt); agg.ReducedRuns != 0 {
		t.Errorf("s5 ReducedRuns = %d, want 0 — an advisory must not move a default-level run into the reduced arm", agg.ReducedRuns)
	}
}
