package cmd

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// closeOneStep drives a real af done against the fixture and returns the step_end record it wrote.
// The record is read back rather than reconstructed, so every assertion below names the key
// production named instead of a test's idea of it.
func closeOneStep(t *testing.T, fx lifecycleFixture) telemetry.StepEvent {
	t.Helper()

	epic, step := seedFormulaBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)
	if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
		t.Fatalf("af done: %v", err)
	}

	records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	for _, r := range records {
		if r.Event == telemetry.EventStepEnd {
			return r
		}
	}
	t.Fatal("af done recorded no step_end; the fixture is not driving a real close")
	return telemetry.StepEvent{}
}

func keyOf(ev telemetry.StepEvent) tokenomics.DigestKey {
	return tokenomics.DigestKey{Formula: ev.Formula, StepID: ev.StepLabel, Model: ev.Model}
}

// seedClosedRun writes one step_end record straight into the store. The tests that need several
// formulas in one factory use it rather than driving af done once per formula: what they assert is
// what the rebuild does with a multi-formula store, and a real close per formula would seed beads
// and sessions that none of them read.
func seedClosedRun(t *testing.T, telemetryDir, agent, formula, step string, minute int) {
	t.Helper()

	peak := int64(40_000)
	ev := telemetry.StepEvent{
		V:             telemetry.SchemaVersion,
		TS:            fmt.Sprintf("2026-08-30T09:%02d:00.000Z", minute),
		Agent:         agent,
		InstanceID:    "inst-" + formula,
		SessionID:     "sess-" + formula + "-" + step,
		Model:         "fable-5",
		Verb:          "done",
		Event:         telemetry.EventStepEnd,
		Status:        telemetry.StatusClosed,
		Formula:       formula,
		StepID:        step,
		StepLabel:     step,
		DurationMS:    1_000,
		PeakCtxTokens: &peak,
	}
	if err := telemetry.AppendEvent(telemetryDir, ev); err != nil {
		t.Fatalf("seeding a %s %s run: %v", formula, step, err)
	}
}

// addAgentToRoster puts a second name in agents.json. The roster is what the rebuild walks, and a
// single-agent fixture cannot tell a whole-roster walk from a walk of whoever invoked the verb.
func addAgentToRoster(t *testing.T, factoryRoot, name string) {
	t.Helper()

	path := config.AgentsConfigPath(factoryRoot)
	cfg, err := config.LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("loading agents.json: %v", err)
	}
	cfg.Agents[name] = config.AgentEntry{Type: "autonomous", Description: "second roster member"}
	if err := config.SaveAgentConfig(path, cfg); err != nil {
		t.Fatalf("saving agents.json: %v", err)
	}
}

// dropAgentFromRoster takes a name back out of agents.json. The rebuild reads whoever the roster
// names, so a dropped name is a log nothing will read again — the reachable spelling, inside a unit
// test, of records that have aged out of the store.
func dropAgentFromRoster(t *testing.T, factoryRoot, name string) {
	t.Helper()

	path := config.AgentsConfigPath(factoryRoot)
	cfg, err := config.LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("loading agents.json: %v", err)
	}
	delete(cfg.Agents, name)
	if err := config.SaveAgentConfig(path, cfg); err != nil {
		t.Fatalf("saving agents.json: %v", err)
	}
}

// seedForgottenHistory leaves a factory holding learned data whose records are out of reach, and
// returns the key that history sits under.
func seedForgottenHistory(t *testing.T, fx lifecycleFixture) tokenomics.DigestKey {
	t.Helper()

	dir := config.TelemetryDir(fx.root)
	addAgentToRoster(t, fx.root, "architect")
	for _, minute := range []int{1, 3, 5} {
		seedClosedRun(t, dir, "architect", "design-v7", "P1", minute)
	}

	var err error
	captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"rebuild"}) })
	if err != nil {
		t.Fatalf("seeding the digest: %v", err)
	}

	key := tokenomics.DigestKey{Formula: "design-v7", StepID: "P1", Model: "fable-5"}
	d, err := tokenomics.LoadDigest(telemetry.LearnedDigestPath(dir, "design-v7"))
	if err != nil {
		t.Fatalf("LoadDigest: %v", err)
	}
	if a, ok := d.Lookup(key); !ok || a.Runs != 3 {
		t.Fatalf("the fixture holds no three-run history to forget: %+v", d)
	}

	dropAgentFromRoster(t, fx.root, "architect")
	// Prove the records really are out of reach, or every assertion downstream would pass against
	// a cache that carries nothing forward.
	if m, _ := telemetry.RebuildLearnedDigests(dir, []string{fx.agent}, "design-v7", "2026-08-30T09:15:00.000Z"); len(m) != 0 {
		t.Fatalf("the dropped agent's records are still readable: %+v", m)
	}
	return key
}

// TestTheLearnedDigestOutlivesTheRecords pins the property that makes a standing digest worth
// keeping at all: the store retains one rotated generation, so a step's raw records age out, and a
// writer that only ever re-derived would drop that step back to cold start with a full cache on
// disk. #668 names surviving rotation as the reason to materialise the cache rather than compute it
// at read time.
//
// It fails against a close or a rebuild that overwrites the file with a derivation of whatever
// records happen to survive — the shape every assertion here would otherwise pass against.
func TestTheLearnedDigestOutlivesTheRecords(t *testing.T) {
	dir := func(fx lifecycleFixture) string { return config.TelemetryDir(fx.root) }

	t.Run("a close keeps what the records can no longer prove", func(t *testing.T) {
		resetReportFlags(t)
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		aged := seedForgottenHistory(t, fx)

		seedClosedRun(t, dir(fx), fx.agent, "design-v7", "P2", 20)
		stderr := captureStderr(t, func() {
			updateLearnedDigest(fx.root, telemetry.StepEvent{Formula: "design-v7", TS: "2026-08-30T09:30:00.000Z"})
		})
		if stderr != "" {
			t.Errorf("the close warned: %s", stderr)
		}

		d, err := tokenomics.LoadDigest(telemetry.LearnedDigestPath(dir(fx), "design-v7"))
		if err != nil {
			t.Fatalf("LoadDigest: %v", err)
		}
		carried, ok := d.Lookup(aged)
		if !ok {
			t.Fatalf("the close erased the history its records no longer hold: %+v", d)
		}
		if carried.Runs != 3 {
			t.Errorf("carried Runs = %d, want 3", carried.Runs)
		}
		if _, ok := d.Lookup(tokenomics.DigestKey{Formula: "design-v7", StepID: "P2", Model: "fable-5"}); !ok {
			t.Error("the closing step's own row is missing; carrying history forward must not stop the cache learning")
		}
	})

	t.Run("the rebuild verb keeps it too", func(t *testing.T) {
		// The sharper half. A rebuild that re-derived and overwrote would make the operator's
		// recovery verb the one command that destroys what the cache exists to keep.
		resetReportFlags(t)
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		aged := seedForgottenHistory(t, fx)

		seedClosedRun(t, dir(fx), fx.agent, "design-v7", "P2", 20)
		var err error
		out := captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"rebuild"}) })
		if err != nil {
			t.Fatalf("af telemetry rebuild: %v", err)
		}
		if !strings.Contains(out, "rebuilt 1 formula digests (2 aggregates)") {
			t.Errorf("the verb reported neither the carried row nor the fresh one:\n%s", out)
		}

		d, err := tokenomics.LoadDigest(telemetry.LearnedDigestPath(dir(fx), "design-v7"))
		if err != nil {
			t.Fatalf("LoadDigest: %v", err)
		}
		if a, ok := d.Lookup(aged); !ok || a.Runs != 3 {
			t.Errorf("the rebuild verb erased the carried history: %+v", d)
		}
	})

	t.Run("deleting the file is still the way to start over", func(t *testing.T) {
		// Carrying history forward must not make the cache immortal. Deleting the file is the
		// operator's from-scratch path, and AC 2 rests on it meaning exactly that.
		resetReportFlags(t)
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		aged := seedForgottenHistory(t, fx)

		seedClosedRun(t, dir(fx), fx.agent, "design-v7", "P2", 20)
		if err := os.RemoveAll(telemetry.LearnedDigestDir(dir(fx))); err != nil {
			t.Fatalf("removing the cache: %v", err)
		}

		var err error
		captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"rebuild"}) })
		if err != nil {
			t.Fatalf("af telemetry rebuild: %v", err)
		}

		d, err := tokenomics.LoadDigest(telemetry.LearnedDigestPath(dir(fx), "design-v7"))
		if err != nil {
			t.Fatalf("LoadDigest: %v", err)
		}
		if _, ok := d.Lookup(aged); ok {
			t.Error("a deleted digest came back; there was nothing on disk to carry it")
		}
		if n := tokenomics.Coverage(d); n != 1 {
			t.Errorf("the rebuilt digest holds %d rows, want 1 — only what the records still prove", n)
		}
	})
}

// TestDoneUpdatesTheLearnedDigest is the wiring half of #668 K6: Phase 2 shipped a digest codec
// with no caller, so every appetite the predicate could ever ask for was unknown by construction.
//
// This test fails against a hook that is never called, one placed outside the telemetry gate, one
// that lets a cache failure reach the verb, and one that stamps the cache from its own clock
// rather than from the record it was written beside.
func TestDoneUpdatesTheLearnedDigest(t *testing.T) {
	t.Run("closing a step writes the formula's digest", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)

		end := closeOneStep(t, fx)
		if end.Formula == "" {
			t.Fatal("the closing record carries no formula name; the path below would be composed from nothing")
		}

		path := telemetry.LearnedDigestPath(config.TelemetryDir(fx.root), end.Formula)
		// Stat first: LoadDigest answers an absent file with an empty digest and no error, so a
		// missing file would otherwise read as a digest that simply learned nothing.
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("no digest at %s after a close: %v", path, err)
		}
		d, err := tokenomics.LoadDigest(path)
		if err != nil {
			t.Fatalf("LoadDigest: %v", err)
		}
		a, ok := d.Lookup(keyOf(end))
		if !ok {
			t.Fatalf("the digest holds no aggregate for the step just closed: %+v", d)
		}
		if a.Runs != 1 {
			t.Errorf("Runs = %d, want 1", a.Runs)
		}
		if a.UpdatedAt != end.TS {
			t.Errorf("digest UpdatedAt = %q, record TS = %q; the two describe one instant and must agree", a.UpdatedAt, end.TS)
		}
	})

	t.Run("a cache that cannot be written warns and the close still succeeds", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		// Occupy the digest directory path with a plain file. The records directory beside it is
		// untouched, so this poisons the cache write and nothing else — which is what makes the
		// record assertion below meaningful.
		if err := os.MkdirAll(config.TelemetryDir(fx.root), 0o755); err != nil {
			t.Fatalf("mkdir telemetry dir: %v", err)
		}
		if err := os.WriteFile(telemetry.LearnedDigestDir(config.TelemetryDir(fx.root)), []byte("x"), 0o644); err != nil {
			t.Fatalf("poison digest dir: %v", err)
		}

		epic, step := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)

		var doneErr error
		stderr := captureStderr(t, func() { doneErr = runDoneCore(t.Context(), fx.workDir, false, "") })
		if doneErr != nil {
			t.Fatalf("af done failed because the learned cache could not be written: %v", doneErr)
		}
		if got, err := fx.mem.Get(t.Context(), step.ID); err != nil {
			t.Fatalf("get step: %v", err)
		} else if !got.Status.IsTerminal() {
			t.Error("the step was not closed")
		}
		if !strings.Contains(stderr, "could not update the learned digest") {
			t.Errorf("a failed cache write produced no warning; the error was dropped silently:\n%s", stderr)
		}
		// A cache failure must not cost the record it was derived from — the record store is the
		// truth, and a rebuild recovers everything this close could not write.
		if n := countEvents(t, fx.root, fx.agent, telemetry.EventStepEnd); n != 1 {
			t.Errorf("recorded %d step_end events, want 1", n)
		}
	})

	t.Run("a record with no formula name writes nothing at all", func(t *testing.T) {
		// An empty name means "every formula" to the shared writer, so a hook that passed one
		// through would turn a single step's close into a full-store rewrite. runDoneCore cannot
		// produce that record today, which is exactly why the guard needs a test of its own.
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		seedClosedRun(t, config.TelemetryDir(fx.root), fx.agent, "design-v7", "P1", 5)

		stderr := captureStderr(t, func() {
			updateLearnedDigest(fx.root, telemetry.StepEvent{TS: "2026-08-30T09:15:00.000Z"})
		})
		if stderr != "" {
			t.Errorf("a nameless record produced output: %s", stderr)
		}
		if _, err := os.Stat(telemetry.LearnedDigestDir(config.TelemetryDir(fx.root))); !os.IsNotExist(err) {
			t.Errorf("a nameless record rebuilt the store's digests; stat err = %v", err)
		}
	})

	t.Run("a corrupt cache is replaced rather than obeyed", func(t *testing.T) {
		// The cache is never the truth. A stored file this binary cannot parse must cost the
		// history it held and nothing else — least of all the factory's ability to keep learning,
		// which is what refusing to proceed would take.
		resetReportFlags(t)
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		dir := config.TelemetryDir(fx.root)
		seedClosedRun(t, dir, fx.agent, "design-v7", "P1", 5)

		path := telemetry.LearnedDigestPath(dir, "design-v7")
		if err := os.MkdirAll(telemetry.LearnedDigestDir(dir), 0o755); err != nil {
			t.Fatalf("mkdir digest dir: %v", err)
		}
		if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
			t.Fatalf("corrupting the digest: %v", err)
		}

		var err error
		out := captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"rebuild"}) })
		if err != nil {
			t.Fatalf("a corrupt cache blocked the rebuild: %v", err)
		}
		if !strings.Contains(out, "replaced 1 unreadable formula digests") {
			t.Errorf("the replacement was silent; the history it took went unreported:\n%s", out)
		}
		d, err := tokenomics.LoadDigest(path)
		if err != nil {
			t.Fatalf("the digest is still unreadable after the rebuild: %v", err)
		}
		if _, ok := d.Lookup(tokenomics.DigestKey{Formula: "design-v7", StepID: "P1", Model: "fable-5"}); !ok {
			t.Errorf("the repaired digest holds nothing: %+v", d)
		}
	})

	t.Run("a formula that cannot name a file says so and writes nothing", func(t *testing.T) {
		// The engine skips a name it cannot file under, which is right for a bulk rebuild and
		// silent for a single close. A close that learned nothing from the one step it had is the
		// case where silence reads as success.
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)

		stderr := captureStderr(t, func() {
			updateLearnedDigest(fx.root, telemetry.StepEvent{Formula: "../escape", TS: "2026-08-30T09:15:00.000Z"})
		})
		if !strings.Contains(stderr, "cannot name a digest file") {
			t.Errorf("an unfileable formula name was skipped silently:\n%s", stderr)
		}
		if _, err := os.Stat(telemetry.LearnedDigestDir(config.TelemetryDir(fx.root))); !os.IsNotExist(err) {
			t.Errorf("an unfileable formula name still wrote a digest; stat err = %v", err)
		}
	})

	t.Run("a dark gate writes no digest", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		// No gateOn: the shipped default.

		epic, step := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)
		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
		if got, err := fx.mem.Get(t.Context(), step.ID); err != nil {
			t.Fatalf("get step: %v", err)
		} else if !got.Status.IsTerminal() {
			t.Fatal("the step was not closed; the fixture is not exercising a real close")
		}

		if _, err := os.Stat(telemetry.LearnedDigestDir(config.TelemetryDir(fx.root))); !os.IsNotExist(err) {
			t.Errorf("a digest directory exists with the gate off; stat err = %v", err)
		}
	})
}

// TestTelemetryRebuildVerb pins the operator's escape hatch: the cache is derivable, so deleting it
// must always be safe. It fails against a rebuild that reads only the invoking agent's records, and
// against one that is a second implementation of the fold rather than the same one at a wider
// scope — the aggregate it writes is compared with the one af done wrote, modulo the timestamp
// each caller supplies.
func TestTelemetryRebuildVerb(t *testing.T) {
	resetReportFlags(t)
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	end := closeOneStep(t, fx)
	fromClose, err := tokenomics.LoadDigest(telemetry.LearnedDigestPath(config.TelemetryDir(fx.root), end.Formula))
	if err != nil {
		t.Fatalf("LoadDigest: %v", err)
	}
	closed, ok := fromClose.Lookup(keyOf(end))
	if !ok {
		t.Fatalf("af done wrote no aggregate; the comparison below would prove nothing")
	}

	if err := os.RemoveAll(telemetry.LearnedDigestDir(config.TelemetryDir(fx.root))); err != nil {
		t.Fatalf("removing the cache: %v", err)
	}

	// Wait out the millisecond the close was stamped in. telemetryTimestamp resolves to
	// milliseconds, so a rebuild that ran inside that same millisecond would stamp the identical
	// string no matter where it read the clock — and the inequality below would fail on a fast
	// machine for a reason that has nothing to do with what it is asserting.
	for telemetryTimestamp() == closed.UpdatedAt {
		time.Sleep(time.Millisecond)
	}

	var runErr error
	out := captureStdout(t, func() { runErr = runTelemetry(telemetryCmd, []string{"rebuild"}) })
	if runErr != nil {
		t.Fatalf("af telemetry rebuild: %v", runErr)
	}
	// The aggregate count is asserted alongside the formula count because it is the figure that
	// tells an operator the rebuild found anything: one formula file holding zero rows is exactly
	// what a broken rebuild writes, and "rebuilt 1 formula digests" alone would call that success.
	if !strings.Contains(out, "rebuilt 1 formula digests (1 aggregates)") {
		t.Errorf("the verb did not report what it wrote:\n%s", out)
	}

	rebuilt, err := tokenomics.LoadDigest(telemetry.LearnedDigestPath(config.TelemetryDir(fx.root), end.Formula))
	if err != nil {
		t.Fatalf("LoadDigest after rebuild: %v", err)
	}
	got, ok := rebuilt.Lookup(keyOf(end))
	if !ok {
		t.Fatalf("the rebuild did not restore the deleted aggregate: %+v", rebuilt)
	}
	if got.UpdatedAt == closed.UpdatedAt {
		t.Error("the rebuild reused the close's timestamp; each caller stamps its own")
	}
	got.UpdatedAt, closed.UpdatedAt = "", ""
	if got != closed {
		t.Errorf("the rebuild differs from what af done wrote by more than its timestamp:\ngot  %+v\nwant %+v", got, closed)
	}
}

// TestTelemetryRebuildSurvivesOneUnwritableFormula pins the verb's whole promise: delete the cache
// and rebuild it. A rebuild that stopped at the first formula it could not write would leave every
// alphabetically later formula unrebuilt, and say only that something went wrong.
//
// The fixture is deliberately the smallest one that is not degenerate: two formulas so "continued"
// differs from "stopped", two steps of the surviving formula so the aggregate count differs from
// the formula count, and two agents on the roster so a whole-roster walk differs from a walk of
// whoever invoked the verb. Every one of those is a figure the verb reports or a row it writes.
func TestTelemetryRebuildSurvivesOneUnwritableFormula(t *testing.T) {
	resetReportFlags(t)
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)
	addAgentToRoster(t, fx.root, "architect")

	dir := config.TelemetryDir(fx.root)
	seedClosedRun(t, dir, fx.agent, "alpha-v1", "P1", 5)
	seedClosedRun(t, dir, fx.agent, "zeta-v1", "P1", 7)
	// The second step of zeta is closed by the OTHER agent. The digest is keyed by formula and the
	// store by agent, so this row exists only if the rebuild walks the whole roster — which is what
	// makes the cache cross-agent, and what af telemetry rebuild's help promises.
	seedClosedRun(t, dir, "architect", "zeta-v1", "P2", 9)

	// A directory cannot be renamed over, so alpha's write fails and zeta's does not. Alpha sorts
	// first, which is what makes this fixture distinguish "continued" from "stopped".
	if err := os.MkdirAll(telemetry.LearnedDigestPath(dir, "alpha-v1"), 0o755); err != nil {
		t.Fatalf("occupying alpha's path: %v", err)
	}

	var runErr error
	out := captureStdout(t, func() { runErr = runTelemetry(telemetryCmd, []string{"rebuild"}) })
	if runErr == nil {
		t.Error("a rebuild that could not write a formula reported success")
	}
	// Two aggregates from one formula: the row count and the file count are different figures, and
	// an operator reads the row count to know the rebuild found anything.
	if !strings.Contains(out, "rebuilt 1 formula digests (2 aggregates)") {
		t.Errorf("the verb did not report the formula it did write:\n%s", out)
	}
	if !strings.Contains(out, "could not write 1 formula digests") {
		t.Errorf("the verb did not report the formula it could not write:\n%s", out)
	}
	// Alpha's path is a directory, so reading it fails exactly as a corrupt file would. Claiming it
	// was replaced would tell an operator their history is gone on the one path where the write
	// never happened and the file is untouched.
	if strings.Contains(out, "replaced") {
		t.Errorf("the verb reported replacing a digest it could not write:\n%s", out)
	}

	d, err := tokenomics.LoadDigest(telemetry.LearnedDigestPath(dir, "zeta-v1"))
	if err != nil {
		t.Fatalf("LoadDigest for zeta: %v", err)
	}
	if n := tokenomics.Coverage(d); n != 2 {
		t.Errorf("zeta's digest holds %d aggregates, want 2 — one per step, across both agents", n)
	}
}

// TestTelemetryRebuildIsOnTheSurface is the companion of TestTelemetryUsage_HelpListsUsage: a verb
// the dispatcher accepts but the help never names is a verb no operator can find.
func TestTelemetryRebuildIsOnTheSurface(t *testing.T) {
	if !strings.Contains(telemetryCmd.Use, "rebuild") {
		t.Errorf("Use = %q, want it to list the rebuild verb", telemetryCmd.Use)
	}
	if !strings.Contains(telemetryCmd.Long, "af telemetry rebuild") {
		t.Errorf("the Long help does not contain the literal %q:\n%s", "af telemetry rebuild", telemetryCmd.Long)
	}
}
