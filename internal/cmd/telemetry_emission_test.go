package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// The tests in this file are the behavioural twins of the static ordering guards in the phase's
// acceptance criteria. Those guards read done.go and prove that no line naming a closing event
// kind sits above the corresponding store.Close — which is a statement about the text of the
// file. These prove the thing the text was arranged to achieve: that a run which does not
// actually close anything records no completion.

// primeWithHookSession drives af prime in --hook mode with the given session id on stdin, the
// way Claude Code's SessionStart hook invokes it. The stdin swap is what makes this the only
// honest way to exercise the session_start path: the id is read from the hook payload, never
// from a flag or a file.
func primeWithHookSession(t *testing.T, sessionID string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(`{"session_id":"` + sessionID + `"}`); err != nil {
		t.Fatalf("write hook payload: %v", err)
	}
	w.Close()

	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig; r.Close() }()

	if err := runPrimeInFixture(t); err != nil {
		t.Fatalf("af prime --hook: %v", err)
	}
}

// gateOn switches the factory gate on for the duration of a test.
func gateOn(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
}

// TestDoneWritesNoStepEndWhenAGuardRejectsTheClose is the reason step_end is recorded beside the
// success marker rather than beside the last_closed_step write the design originally named.
//
// Two guards run between that write and the real close, and either can reject the invocation.
// Recording at the earlier site would stamp a completion on a step that is still open, and
// nothing downstream — not the report, not a backend — could tell that record apart from a real
// one. The static guard proves the lines are in the right order; this proves the consequence.
func TestDoneWritesNoStepEndWhenAGuardRejectsTheClose(t *testing.T) {
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	epic, step := seedFormulaBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	// No .runtime/step_primed: checkStepPrimed refuses the close.

	err := runDoneCore(t.Context(), fx.workDir, false, "")
	if err == nil {
		t.Fatal("af done succeeded on an unprimed step; the fixture is not exercising the guard")
	}
	if !strings.Contains(err.Error(), "not primed") {
		t.Fatalf("af done failed for the wrong reason: %v", err)
	}

	if got, err := fx.mem.Get(t.Context(), step.ID); err != nil {
		t.Fatalf("get step: %v", err)
	} else if got.Status.IsTerminal() {
		t.Fatal("the step was closed despite the guard; the fixture is wrong")
	}

	if n := countEvents(t, fx.root, fx.agent, telemetry.EventStepEnd); n != 0 {
		t.Errorf("recorded %d step_end events for a step that was never closed, want 0", n)
	}
}

// TestDoneWritesNoInstanceEndWhenCompletionGuardTrips is the same property one level up. The
// completion-velocity guard returns an error WITHOUT closing the formula-instance epic, so a
// run it blocks has not finished — and an instance_end there would make a blocked formula
// indistinguishable from a completed one in every per-instance view.
func TestDoneWritesNoInstanceEndWhenCompletionGuardTrips(t *testing.T) {
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	ctx := t.Context()
	epic, err := fx.mem.Create(ctx, issuestore.CreateParams{
		Title: "Formula: offpath", Type: issuestore.TypeEpic,
		Labels: []string{"formula-instance"}, Assignee: fx.agent,
	})
	if err != nil {
		t.Fatalf("seed epic: %v", err)
	}
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "formula_caller", "manager")

	// Three unprimed closes is the documented trigger threshold.
	for i := 0; i < 3; i++ {
		if err := recordDoneTimestamp(fx.workDir, "skipped-step", false, ""); err != nil {
			t.Fatalf("seed done_velocity %d: %v", i, err)
		}
	}
	if triggered, _ := checkFormulaCompletionVelocity(fx.workDir); !triggered {
		t.Fatal("the completion-velocity guard did not trigger; the fixture is not exercising it")
	}

	origEscalation := sendEscalationMail
	sendEscalationMail = func(string, string, string, string) error { return nil }
	t.Cleanup(func() { sendEscalationMail = origEscalation })

	// The epic has no children at all, so runDoneCore takes the "all complete" branch straight
	// into the completion path, where the guard refuses.
	err = runDoneCore(ctx, fx.workDir, false, "")
	if err == nil || !strings.Contains(err.Error(), "guard triggered") {
		t.Fatalf("af done error = %v, want the completion guard to have blocked it", err)
	}

	if got, err := fx.mem.Get(ctx, epic.ID); err != nil {
		t.Fatalf("get epic: %v", err)
	} else if got.Status.IsTerminal() {
		t.Fatal("the instance was closed despite the guard; the fixture is wrong")
	}

	if n := countEvents(t, fx.root, fx.agent, telemetry.EventInstanceEnd); n != 0 {
		t.Errorf("recorded %d instance_end events for a formula the guard blocked, want 0", n)
	}
}

// TestDoneRecordsInstanceEndOnAGenuineCompletion is the positive half the test above needs to
// mean anything. Without it, an implementation that never emits instance_end at all — because
// the telemetry context stopped reaching sendWorkDoneAndCleanup, say — passes every ordering
// guard and the whole suite, since those guards only read where the constant APPEARS in the
// file, not whether that line is ever reached.
func TestDoneRecordsInstanceEndOnAGenuineCompletion(t *testing.T) {
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	epic, step := seedFormulaBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)

	if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
		t.Fatalf("af done: %v", err)
	}

	if got, err := fx.mem.Get(t.Context(), epic.ID); err != nil {
		t.Fatalf("get epic: %v", err)
	} else if !got.Status.IsTerminal() {
		t.Fatal("the formula instance was not closed; the fixture never reached the completion path")
	}

	records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	var end *telemetry.StepEvent
	for i := range records {
		if records[i].Event == telemetry.EventInstanceEnd {
			if end != nil {
				t.Fatal("recorded more than one instance_end for a single completion")
			}
			end = &records[i]
		}
	}
	if end == nil {
		t.Fatalf("no instance_end recorded for a formula that genuinely completed (records: %+v)", records)
	}
	if end.InstanceID != epic.ID {
		t.Errorf("instance_end instance_id = %q, want %q", end.InstanceID, epic.ID)
	}
	if end.Formula != "offpath" {
		t.Errorf("instance_end formula = %q, want the bare name %q", end.Formula, "offpath")
	}
	if end.Agent != fx.agent {
		t.Errorf("instance_end agent = %q, want %q", end.Agent, fx.agent)
	}
	if end.Verb != "done" {
		t.Errorf("instance_end verb = %q, want %q", end.Verb, "done")
	}
}

// TestPrimeRecordsSessionStartOnlyWhenTheSessionIDChanges pins the qualifier the design states
// and the code did not previously have: the session-start hook fires on resume as well as on a
// fresh session, and the writer it hangs off was an unconditional overwrite.
func TestPrimeRecordsSessionStartOnlyWhenTheSessionIDChanges(t *testing.T) {
	t.Run("the hook records one session_start per genuinely new session", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)

		epic, _ := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)

		origHook := primeHookMode
		primeHookMode = true
		t.Cleanup(func() { primeHookMode = origHook })

		primeWithHookSession(t, "sess-alpha")
		if n := countEvents(t, fx.root, fx.agent, telemetry.EventSessionStart); n != 1 {
			t.Fatalf("recorded %d session_start events for a new session, want exactly 1", n)
		}

		// The SessionStart hook fires on resume too. A second firing carrying the same id is
		// the same session continuing, and a record for it would multiply one session into
		// many in every per-session view.
		primeWithHookSession(t, "sess-alpha")
		if n := countEvents(t, fx.root, fx.agent, telemetry.EventSessionStart); n != 1 {
			t.Errorf("re-firing the hook with the same session id recorded %d events, want still 1", n)
		}

		primeWithHookSession(t, "sess-beta")
		if n := countEvents(t, fx.root, fx.agent, telemetry.EventSessionStart); n != 2 {
			t.Errorf("a genuinely new session id recorded %d events in total, want 2", n)
		}

		records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		for _, r := range records {
			if r.Event != telemetry.EventSessionStart {
				continue
			}
			// The role is not resolved until after the hook branch, which is why the record is
			// written further down runPrime than the write it describes.
			if r.Agent != fx.agent {
				t.Errorf("session_start agent = %q, want %q", r.Agent, fx.agent)
			}
			if r.Verb != "prime" {
				t.Errorf("session_start verb = %q, want %q", r.Verb, "prime")
			}
			if r.SessionID == "" {
				t.Error("session_start carries no session id")
			}
			if r.InstanceID != epic.ID {
				t.Errorf("session_start instance_id = %q, want %q", r.InstanceID, epic.ID)
			}
		}
	})

	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	sessionPath := filepath.Join(fx.workDir, ".runtime", "session_id")

	t.Run("the first write is a change", func(t *testing.T) {
		if changed := persistSessionID(fx.workDir, "sess-one"); !changed {
			t.Error("writing a session id where there was none reported no change")
		}
	})

	t.Run("rewriting the same id is not", func(t *testing.T) {
		if changed := persistSessionID(fx.workDir, "sess-one"); changed {
			t.Error("rewriting the identical session id reported a change")
		}
	})

	t.Run("a trailing newline is not a change", func(t *testing.T) {
		if err := os.WriteFile(sessionPath, []byte("sess-one\n"), 0o644); err != nil {
			t.Fatalf("seed: %v", err)
		}
		if changed := persistSessionID(fx.workDir, "sess-one"); changed {
			t.Error("a trailing newline in the persisted file was read as a different session")
		}
	})

	t.Run("a genuinely new id is", func(t *testing.T) {
		if changed := persistSessionID(fx.workDir, "sess-two"); !changed {
			t.Error("a new session id reported no change")
		}
		if data, err := os.ReadFile(sessionPath); err != nil || string(data) != "sess-two" {
			t.Errorf("persisted file = %q (err %v), want the new id written bare", string(data), err)
		}
	})
}

// TestStepPrimedReportsOnlyGenuinelyNewSteps pins that a re-prime is not a new step. The marker
// carries a description hash as well as the step id, and a step whose description changed is
// still the same step — treating it as new would restart a clock that is already running.
func TestStepPrimedReportsOnlyGenuinelyNewSteps(t *testing.T) {
	dir := t.TempDir()

	if isNew := writeStepPrimed(dir, "step-1", "first"); !isNew {
		t.Error("the first prime of a step reported it as not new")
	}
	if isNew := writeStepPrimed(dir, "step-1", "first"); isNew {
		t.Error("re-priming the same step reported it as new")
	}
	if isNew := writeStepPrimed(dir, "step-1", "a rewritten description"); isNew {
		t.Error("re-priming a step whose description changed reported a new step; only the id decides")
	}
	if isNew := writeStepPrimed(dir, "step-2", "second"); !isNew {
		t.Error("moving to the next step reported it as not new")
	}

	// The marker's byte format is a contract with the close-side check that parses it.
	data, err := os.ReadFile(filepath.Join(dir, ".runtime", "step_primed"))
	if err != nil {
		t.Fatalf("read step_primed: %v", err)
	}
	id, hash, found := strings.Cut(string(data), ":")
	if !found || id != "step-2" || len(hash) != 8 {
		t.Errorf("step_primed = %q, want \"step-2:<8 hex>\"", string(data))
	}
}

// TestTelemetryFailuresNeverFailTheVerb pins the ADR-007 posture at both failure surfaces this
// phase adds. Observability that can break a lifecycle verb is worse than no observability: the
// factory would stop making progress because of a subsystem whose only job is to describe it.
func TestTelemetryFailuresNeverFailTheVerb(t *testing.T) {
	t.Run("a record that cannot be written warns and the verb still succeeds", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		// Occupy the records directory path with a plain file so every append fails.
		if err := os.WriteFile(config.TelemetryDir(fx.root), []byte("x"), 0o644); err != nil {
			t.Fatalf("poison telemetry dir: %v", err)
		}

		epic, step := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)

		var doneErr error
		stderr := captureStderr(t, func() { doneErr = runDoneCore(t.Context(), fx.workDir, false, "") })
		if doneErr != nil {
			t.Fatalf("af done failed because telemetry could not record: %v", doneErr)
		}
		if got, err := fx.mem.Get(t.Context(), step.ID); err != nil {
			t.Fatalf("get step: %v", err)
		} else if !got.Status.IsTerminal() {
			t.Error("the step was not closed")
		}
		if !strings.Contains(stderr, "warning:") {
			t.Errorf("a failed record wrote no warning; the error was dropped silently:\n%s", stderr)
		}
	})

	t.Run("an export the backend refuses warns once and the verb still succeeds", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		writeTelemetryJSON(t, fx.root, `{"endpoint":"http://127.0.0.1:5080","export_timeout_ms":500}`)

		orig := telemetry.Export
		calls := 0
		telemetry.Export = func(config.TelemetryConfig, []byte) error {
			calls++
			return errBackendRefused
		}
		t.Cleanup(func() { telemetry.Export = orig })

		epic, step := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)

		var doneErr error
		stderr := captureStderr(t, func() { doneErr = runDoneCore(t.Context(), fx.workDir, false, "") })
		if doneErr != nil {
			t.Fatalf("af done failed because the export was refused: %v", doneErr)
		}
		if calls != 1 {
			t.Errorf("export attempted %d times in one af done, want exactly 1 (bounded, done-only)", calls)
		}
		if n := strings.Count(stderr, "telemetry export failed"); n != 1 {
			t.Errorf("export failure produced %d warnings, want exactly 1:\n%s", n, stderr)
		}
		// The cursor must not have moved: a refused batch is offered again, not lost.
		if _, err := os.Stat(filepath.Join(config.TelemetryDir(fx.root), ".cursor-"+fx.agent)); !os.IsNotExist(err) {
			t.Errorf("the export cursor advanced past a refused batch; stat err = %v", err)
		}
	})

	t.Run("a local-only factory is never warned about export", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		// No telemetry.json at all: the supported "report works with no backend" end state.

		epic, step := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)

		var doneErr error
		stderr := captureStderr(t, func() { doneErr = runDoneCore(t.Context(), fx.workDir, false, "") })
		if doneErr != nil {
			t.Fatalf("af done: %v", doneErr)
		}
		if strings.Contains(stderr, "export") {
			t.Errorf("a factory with no configured endpoint was warned about exporting:\n%s", stderr)
		}
		if n := countEvents(t, fx.root, fx.agent, telemetry.EventStepEnd); n != 1 {
			t.Errorf("recorded %d step_end events, want 1 — the records must still be kept locally", n)
		}
	})

	t.Run("nothing exportable is not a failure", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		writeTelemetryJSON(t, fx.root, `{"endpoint":"http://127.0.0.1:5080","export_timeout_ms":500}`)

		orig := telemetry.Export
		telemetry.Export = func(config.TelemetryConfig, []byte) error {
			t.Error("an export was attempted with only an open step recorded")
			return nil
		}
		t.Cleanup(func() { telemetry.Export = orig })

		epic, _ := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)

		// af prime records a step_start and nothing else; an open step yields no span, so the
		// drain has nothing to carry. Every non-final af done reaches this state.
		if err := runPrimeInFixture(t); err != nil {
			t.Fatalf("af prime: %v", err)
		}
		stderr := captureStderr(t, func() { drainTelemetryBounded(fx.root, fx.agent) })
		if strings.TrimSpace(stderr) != "" {
			t.Errorf("a drain with nothing exportable warned:\n%s", stderr)
		}
	})
}

// TestStepEndCarriesDurationAndSequenceFromItsStart pins the sourcing decision for the two
// fields af done cannot derive in its own frame. The step sequence has exactly one derivation
// in this repository and it lives in af prime; the primed marker carries no timestamp. Both are
// therefore read back from the record af prime wrote, rather than derived a second time here.
func TestStepEndCarriesDurationAndSequenceFromItsStart(t *testing.T) {
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	epic, step := seedFormulaBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)

	if err := runPrimeInFixture(t); err != nil {
		t.Fatalf("af prime: %v", err)
	}
	if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
		t.Fatalf("af done: %v", err)
	}

	records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}

	var start, end *telemetry.StepEvent
	for i := range records {
		switch records[i].Event {
		case telemetry.EventStepStart:
			start = &records[i]
		case telemetry.EventStepEnd:
			end = &records[i]
		}
	}
	if start == nil || end == nil {
		t.Fatalf("want both a start and an end record, got %+v", records)
	}

	if start.StepSeq < 1 {
		t.Errorf("step_start step_seq = %d, want the position af prime computed", start.StepSeq)
	}
	if end.StepSeq != start.StepSeq {
		t.Errorf("step_end step_seq = %d, want it carried from the start record (%d) rather than derived again",
			end.StepSeq, start.StepSeq)
	}
	if end.StepID != step.ID {
		t.Errorf("step_end step_id = %q, want %q", end.StepID, step.ID)
	}
	if end.DurationMS < 0 {
		t.Errorf("step_end duration_ms = %d, want a non-negative measurement", end.DurationMS)
	}
	if end.Status != telemetry.StatusClosed {
		t.Errorf("step_end status = %q, want %q", end.Status, telemetry.StatusClosed)
	}
	if end.Verb != "done" || start.Verb != "prime" {
		t.Errorf("verbs = %q/%q, want prime/done", start.Verb, end.Verb)
	}
	if end.Formula != "offpath" || start.Formula != "offpath" {
		t.Errorf("formula names = %q/%q, want both to be the bare name %q "+
			"(the instance bead title carries a prefix the records must not)",
			start.Formula, end.Formula, "offpath")
	}
}

// errBackendRefused stands in for a backend rejecting an export.
var errBackendRefused = &backendRefusal{}

type backendRefusal struct{}

func (*backendRefusal) Error() string { return "backend rejected the export with 503" }
