package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// These tests do not run in parallel: TestBoundaryMatrix reassigns the boundaryHandoffExec
// package var, the same rule recovery_test.go:17-18 states for the recovery seams.

// boundaryTestNow is the one clock a boundary fixture reads. Every planted written_at is expressed
// as an offset from it, so freshness is a property of the fixture rather than of how long the test
// took to reach the assertion.
//
// LOCAL, deliberately. statusline.SessionTokens compares a snapshot's date field against
// time.Now().Format(dateLayout) in production (daily.go:280) — local — so a UTC fixture clock would
// write tomorrow's or yesterday's date for anyone whose zone is offset far enough, and cum_tokens
// would silently read 0 for part of every day.
func boundaryTestNow() time.Time { return time.Now() }

// plantSessionSnapshot writes one schema-v2 occupancy snapshot and returns the SESSION-keyed
// reading — the one the boundary actually consumes. plantSnapshot (recovery_test.go:158) returns
// the AGENT-keyed reading, which is precisely the read CRIT-1 forbids at a step boundary, so this
// is a twin rather than a wrapper.
//
// cumTokens is written too, because statusline.SessionTokens (daily.go:274-284) reads through the
// writer's path and requires a today-dated file — a snapshot without one yields 0, which the
// capture site must treat as absent.
func plantSessionSnapshot(t *testing.T, root, agent, sessionID string, pct float64, cumTokens int64, writtenAt, now time.Time) statusline.ChannelReading {
	t.Helper()
	dir := config.StatuslineSessionsDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"schema":               2,
		"session_id":           sessionID,
		"date":                 now.Format("2006-01-02"),
		"agent":                agent,
		"written_at":           writtenAt.UTC().Format(time.RFC3339),
		"context_used_pct":     pct,
		"context_tokens_used":  int64(pct * 2000),
		"context_tokens_total": int64(200000),
		"cum_tokens":           cumTokens,
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return readSessionReading(t, root, agent, sessionID, now)
}

// readSessionReading is the session-keyed twin of readOneReading (recovery_test.go:186).
func readSessionReading(t *testing.T, root, agent, sessionID string, now time.Time) statusline.ChannelReading {
	t.Helper()
	rec := testRecoveryConfig()
	return statusline.SessionObservation(config.StatuslineSessionsDir(root), sessionID, statusline.ReadOptions{
		KnownAgents: map[string]struct{}{agent: {}},
		Staleness:   time.Duration(rec.StalenessSecs) * time.Second,
		DarkAfter:   time.Duration(rec.DarkGraceSecs) * time.Second,
	}, now)
}

// TestShouldBoundaryHandoff is the decision half of the AC-1 matrix: every channel state crossed
// with every position relative to handoff_pct, crossed with the close kind.
//
// The readings are round-tripped through real snapshot files and the real session-keyed reader
// rather than hand-built. statusline.Observation has no exported constructor and all-unexported
// fields (observation.go:98-106), so ObservedReading refuses anything this package could fabricate
// — which means the only honest fresh/stale/dark readings are decoded ones.
func TestShouldBoundaryHandoff(t *testing.T) {
	now := boundaryTestNow()
	cfg := config.StepContextConfig{BoundTokens: 200000, HandoffPct: 75}

	// One root per state so the planted files cannot collide.
	reading := func(state string, pct float64) statusline.ChannelReading {
		root := t.TempDir()
		switch state {
		case "fresh":
			return plantSessionSnapshot(t, root, "manager", "sessa", pct, 1000, now.Add(-10*time.Second), now)
		case "stale":
			return plantSessionSnapshot(t, root, "manager", "sessa", pct, 1000, now.Add(-5*time.Minute), now)
		case "dark":
			return plantSessionSnapshot(t, root, "manager", "sessa", pct, 1000, now.Add(-30*time.Minute), now)
		case "none":
			return readSessionReading(t, root, "manager", "sessa", now)
		case "malformed":
			plantRawSnapshot(t, root, "sessa", `{"schema":2,"session_id":"sessa","agent":"manager"}`)
			return readSessionReading(t, root, "manager", "sessa", now)
		case "prior-session":
			// The dead session's snapshot reads fresh and high; this session's id is different.
			// CRIT-1: an agent-keyed read would answer with it. The session-keyed read must not.
			plantSessionSnapshot(t, root, "manager", "sessold", pct, 1000, now.Add(-10*time.Second), now)
			return readSessionReading(t, root, "manager", "sessnew", now)
		}
		t.Fatalf("unknown state %q", state)
		return statusline.NoReading()
	}

	states := []string{"fresh", "stale", "dark", "none", "malformed", "prior-session"}
	// "at" is exactly handoff_pct and MUST fire: the rule is >=, and an off-by-one to > is the
	// mutation this column exists to kill.
	levels := []struct {
		name string
		pct  float64
	}{{"below", 74}, {"at", 75}, {"above", 76}}
	// The third conjunct pair, spelled as the two production call sites spell it. AC-1's rule is
	// "not-gate-close AND (steps-remain OR improvement-fired-final)", so the last two rows are the
	// two ways work can follow a close and the third is the way nothing does — a formula's final
	// step with no improvement session queued behind it.
	kinds := []struct {
		name        string
		gateClose   bool
		workFollows bool
	}{
		{"mid-run", false, true},
		{"gate-close", true, true},
		{"final-step-improvement-fired", false, true},
		{"final-step-nothing-follows", false, false},
		{"final-step-improvement-fired-on-a-gate-close", true, true},
	}

	for _, st := range states {
		for _, lv := range levels {
			for _, kd := range kinds {
				name := st + "/" + lv.name + "/" + kd.name
				t.Run(name, func(t *testing.T) {
					// The ONLY cells that fire: a fresh, session-matched reading at or above
					// the threshold on a non-gate close that has work following it.
					want := st == "fresh" && lv.pct >= 75 && !kd.gateClose && kd.workFollows
					got := shouldBoundaryHandoff(reading(st, lv.pct), cfg, kd.gateClose, kd.workFollows)
					if got != want {
						t.Errorf("shouldBoundaryHandoff(%s) = %v, want %v", name, got, want)
					}
				})
			}
		}
	}
}

// TestShouldBoundaryHandoff_UnconfiguredThresholdNeverFires pins the fail-closed direction: a
// handoff_pct of zero is "nobody configured this", not "hand off at 0%".
func TestShouldBoundaryHandoff_UnconfiguredThresholdNeverFires(t *testing.T) {
	now := boundaryTestNow()
	root := t.TempDir()
	fresh := plantSessionSnapshot(t, root, "manager", "sessa", 99, 1000, now.Add(-10*time.Second), now)

	if shouldBoundaryHandoff(fresh, config.StepContextConfig{}, false, true) {
		t.Error("an unconfigured step_context fired the boundary; a zero threshold must be inert")
	}
}

// --- the call-site half ------------------------------------------------------------------------

// boundaryRecorder captures what the boundary seam was asked to do. The COUNT matters as much as
// the content: an implementation that prints the handoff line without executing it, or that
// executes twice, is invisible to a boolean.
type boundaryRecorder struct {
	calls int
	opts  RespawnOptions
	err   error
	// onCall observes the world AT the moment of the respawn. G15 is an ORDERING claim — the
	// marker and the self-mail must already have happened — and ordering is only checkable from
	// inside the call, never from after it.
	onCall func()
}

func (b *boundaryRecorder) install(t *testing.T) *boundaryRecorder {
	t.Helper()
	orig := boundaryHandoffExec
	boundaryHandoffExec = func(ctx context.Context, cwd string, opts RespawnOptions, subject, message string) error {
		b.calls++
		b.opts = opts
		if b.onCall != nil {
			b.onCall()
		}
		return b.err
	}
	t.Cleanup(func() { boundaryHandoffExec = orig })
	return b
}

// errBoundaryRefused stands in for a respawn that could not land.
var errBoundaryRefused = errors.New("respawn refused")

// seedTwoStepBeads seeds a formula-instance epic with TWO open steps, so that closing the first
// leaves the more-steps branch — the only branch the mid-run boundary sits on.
func seedTwoStepBeads(t *testing.T, fx lifecycleFixture) (issuestore.Issue, issuestore.Issue) {
	t.Helper()
	epic, step := seedFormulaBeads(t, fx)
	if _, err := fx.mem.Create(t.Context(), issuestore.CreateParams{
		Title: "Step 2", Parent: epic.ID, Type: issuestore.TypeTask,
		Labels: []string{"formula-step"}, Assignee: fx.agent, Description: "Second",
	}); err != nil {
		t.Fatalf("seed second step: %v", err)
	}
	return epic, step
}

// armBoundaryFixture puts a two-step formula in the more-steps position with a primed first step.
func armBoundaryFixture(t *testing.T, fx lifecycleFixture) issuestore.Issue {
	t.Helper()
	epic, step := seedTwoStepBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)
	return step
}

// TestBoundaryMatrix drives the PRODUCTION entry point. It is the only tier that can see a
// permanently-inert C5: the pure predicate above is green in a world where the call site never
// produces a fresh reading at all, and there are four distinct ways that happens — a raw-vs-
// sanitized session id compare (the #563 class), an empty KnownAgents roster, zero staleness
// thresholds, and an empty .runtime/session_id. Each of those turns THIS test red and none of them
// turns the predicate test red.
func TestBoundaryMatrix(t *testing.T) {
	t.Run("fires with a raw session id and the telemetry gate off", func(t *testing.T) {
		now := boundaryTestNow()
		fx := newLifecycleFixture(t)
		// Deliberately: no gateOn (kills the G12 gate-scoped-name class) and no startup.json
		// (kills the zero-threshold class — the defaults must be derived, not assumed present).
		step := armBoundaryFixture(t, fx)

		// RAW id containing a byte sanitizeSessionID strips. The snapshot lives at the SANITIZED
		// stem, so a compare of the raw id against Observation.SessionID() could never match.
		writeRuntimeFile(t, fx.workDir, "session_id", "sess.a\n")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 88, 1000, now.Add(-10*time.Second), now)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		out := captureStdout(t, func() {
			if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
				t.Fatalf("af done: %v", err)
			}
		})

		if rec.calls != 1 {
			t.Fatalf("boundary handoff executed %d times, want exactly 1", rec.calls)
		}
		if rec.opts.Trigger != triggerStepBoundaryHandoff {
			t.Errorf("trigger = %q, want %q — an exec of `af handoff` would record %q (G1)",
				rec.opts.Trigger, triggerStepBoundaryHandoff, triggerSelfHandoff)
		}
		if rec.opts.AgentName == "" || rec.opts.PaneID == "" || rec.opts.FactoryRoot == "" || rec.opts.AgentWorkDir == "" {
			t.Errorf("respawn options underpopulated: %+v", rec.opts)
		}
		if !strings.Contains(out, "handing off for a clean session") || !strings.Contains(out, step.ID) {
			t.Errorf("stdout = %q, want the boundary line naming step %s", out, step.ID)
		}
	})

	t.Run("a prior session's fresh high snapshot is inert", func(t *testing.T) {
		now := boundaryTestNow()
		fx := newLifecycleFixture(t)
		armBoundaryFixture(t, fx)

		writeRuntimeFile(t, fx.workDir, "session_id", "sessnew")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessold", 95, 1000, now.Add(-10*time.Second), now)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
		if rec.calls != 0 {
			t.Errorf("boundary fired off a prior session's snapshot (%d calls) — CRIT-1", rec.calls)
		}
	})

	t.Run("below the threshold is inert", func(t *testing.T) {
		now := boundaryTestNow()
		fx := newLifecycleFixture(t)
		armBoundaryFixture(t, fx)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 60, 1000, now.Add(-10*time.Second), now)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
		if rec.calls != 0 {
			t.Errorf("boundary fired below the threshold (%d calls)", rec.calls)
		}
	})

	t.Run("a gate close is inert but still records occupancy", func(t *testing.T) {
		now := boundaryTestNow()
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		armBoundaryFixture(t, fx)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 88, 1000, now.Add(-10*time.Second), now)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, true, "gate-1"); err != nil {
			t.Fatalf("af done --phase-complete: %v", err)
		}
		if rec.calls != 0 {
			t.Errorf("boundary fired on a gate close (%d calls) — HIGH-2", rec.calls)
		}

		end := lastStepEnd(t, fx.root, fx.agent)
		if end.Status != telemetry.StatusGateWaiting {
			t.Errorf("step_end status = %q, want %q", end.Status, telemetry.StatusGateWaiting)
		}
		if end.CtxUsedPct == nil {
			t.Error("a gate close recorded no occupancy; HIGH-2 excludes the handoff, not the measurement")
		}
	})

	t.Run("no tmux pane means no handoff and af done still exits 0", func(t *testing.T) {
		now := boundaryTestNow()
		fx := newLifecycleFixture(t)
		armBoundaryFixture(t, fx)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 88, 1000, now.Add(-10*time.Second), now)

		t.Setenv("TMUX", "")
		t.Setenv("TMUX_PANE", "")
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
		if rec.calls != 0 {
			t.Errorf("boundary executed without a pane (%d calls)", rec.calls)
		}
	})

	t.Run("a handoff failure warns and af done still exits 0", func(t *testing.T) {
		now := boundaryTestNow()
		fx := newLifecycleFixture(t)
		step := armBoundaryFixture(t, fx)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 88, 1000, now.Add(-10*time.Second), now)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{err: errBoundaryRefused}).install(t)

		stderr := captureStderr(t, func() {
			if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
				t.Fatalf("af done returned %v; a failed boundary handoff must warn and exit 0", err)
			}
		})

		if rec.calls != 1 {
			t.Fatalf("boundary handoff executed %d times, want 1", rec.calls)
		}
		if !strings.Contains(stderr, "warning:") {
			t.Errorf("stderr = %q, want a warning about the failed handoff", stderr)
		}
		// The step must still be closed: the close is durable before any recycling.
		iss, err := fx.mem.Get(t.Context(), step.ID)
		if err != nil {
			t.Fatalf("get step: %v", err)
		}
		if iss.Status == issuestore.StatusOpen {
			t.Error("the step was left open after a failed boundary handoff")
		}
	})

	// A startup.json that will not load is NOT the same as one that is absent. Absent yields
	// defaults (LoadStartupConfig, startup.go:232-234); malformed yields (nil, err), and every read
	// of the config on this path has to survive that. The boundary decision is the only reader that
	// runs after the step is already closed, so a nil dereference here takes down an af done that
	// has nothing left to do but report success.
	t.Run("a malformed startup.json is inert and af done still exits 0", func(t *testing.T) {
		now := boundaryTestNow()
		fx := newLifecycleFixture(t)
		step := armBoundaryFixture(t, fx)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		// 95%: high enough that a config which HAD loaded would fire. The inertness below is
		// therefore attributable to the unreadable config, not to a quiet channel.
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)
		if err := os.WriteFile(config.StartupConfigPath(fx.root), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
		if rec.calls != 0 {
			t.Errorf("boundary fired on a config it could not read (%d calls) — the bound is unknown, so the answer must be no", rec.calls)
		}
		iss, err := fx.mem.Get(t.Context(), step.ID)
		if err != nil {
			t.Fatalf("get step: %v", err)
		}
		if iss.Status == issuestore.StatusOpen {
			t.Error("the step was left open")
		}
	})

	// --- the final-step extension (G15) ---------------------------------------------------------
	//
	// A separate fixture from the rest of the matrix, because this branch is reachable only through
	// sendWorkDoneAndCleanup with the improvement hook armed — a formula that COMPLETED, not one
	// with steps left. It is also the branch with the most to lose: it respawns a pane after
	// cleanupRuntimeArtifacts has run and while the identity lock is deliberately still held.

	t.Run("the final step hands off, but only after the marker and the self-mail", func(t *testing.T) {
		now := boundaryTestNow()
		t.Setenv("AF_ROLE", "alpha")
		root := setupImprovementFiringFactory(t)
		writeRuntimeFile(t, root, "formula_caller", "supervisor")
		writeRuntimeFile(t, root, "session_id", "sess.imp\n")
		plantSessionSnapshot(t, root, "alpha", "sessimp", 93, 1000, now.Add(-10*time.Second), now)

		mem := memstore.New()
		instanceID := seedCompletedFormula(t, mem, "Formula: widget")

		tmuxPaneEnv(t)
		var markerAtRespawn, mailedAtRespawn bool
		var mailed int
		origMail := sendImprovementMail
		sendImprovementMail = func(agent, subject, instruction string) error { mailed++; return nil }
		t.Cleanup(func() { sendImprovementMail = origMail })

		rec := &boundaryRecorder{}
		rec.onCall = func() {
			_, err := os.Stat(improvementPendingFile(root, "alpha"))
			markerAtRespawn = err == nil
			mailedAtRespawn = mailed > 0
		}
		rec.install(t)

		captureStdout(t, func() {
			if err := sendWorkDoneAndCleanup(t.Context(), mem, root, root, instanceID, false); err != nil {
				t.Fatalf("final af done: %v", err)
			}
		})

		if rec.calls != 1 {
			t.Fatalf("the final-step boundary executed %d times, want 1 — an improvement session "+
				"inherits the dirtiest window of the whole run", rec.calls)
		}
		if !markerAtRespawn {
			t.Error("the respawn happened BEFORE the improvement marker was on disk (G15): a lost " +
				"marker leaves `af improvement complete` with nothing to consume")
		}
		if !mailedAtRespawn {
			t.Error("the respawn happened BEFORE the instruction was self-mailed (G15)")
		}
		if rec.opts.Trigger != triggerStepBoundaryHandoff {
			t.Errorf("trigger = %q, want %q", rec.opts.Trigger, triggerStepBoundaryHandoff)
		}
	})

	t.Run("a final step with no improvement session queued is inert", func(t *testing.T) {
		now := boundaryTestNow()
		t.Setenv("AF_ROLE", "alpha")
		// Same factory, improvement toggle OFF — so improvementFired stays false and nothing
		// outside that branch may read the snapshot at all.
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": false})
		writeRuntimeFile(t, root, "session_id", "sess.imp\n")
		plantSessionSnapshot(t, root, "alpha", "sessimp", 99, 1000, now.Add(-10*time.Second), now)

		mem := memstore.New()
		instanceID := seedCompletedFormula(t, mem, "Formula: widget")

		tmuxPaneEnv(t)
		rec := (&boundaryRecorder{}).install(t)

		captureStdout(t, func() {
			if err := sendWorkDoneAndCleanup(t.Context(), mem, root, root, instanceID, false); err != nil {
				t.Fatalf("final af done: %v", err)
			}
		})
		if rec.calls != 0 {
			t.Errorf("the boundary fired on a completed formula with nothing following it "+
				"(%d calls) — recycling a session with no work left buys nothing", rec.calls)
		}
	})

	t.Run("a final step that closes a gate is inert even with an improvement session queued", func(t *testing.T) {
		now := boundaryTestNow()
		t.Setenv("AF_ROLE", "alpha")
		root := setupImprovementFiringFactory(t)
		writeRuntimeFile(t, root, "formula_caller", "supervisor")
		writeRuntimeFile(t, root, "session_id", "sess.imp\n")
		plantSessionSnapshot(t, root, "alpha", "sessimp", 99, 1000, now.Add(-10*time.Second), now)

		mem := memstore.New()
		instanceID := seedCompletedFormula(t, mem, "Formula: widget")

		tmuxPaneEnv(t)
		rec := (&boundaryRecorder{}).install(t)

		captureStdout(t, func() {
			// gateClose = true: AC-1 makes not-a-gate-close a conjunct of the WHOLE rule, so the
			// final step is not a carve-out from it.
			if err := sendWorkDoneAndCleanup(t.Context(), mem, root, root, instanceID, true); err != nil {
				t.Fatalf("final af done --phase-complete: %v", err)
			}
		})
		if rec.calls != 0 {
			t.Errorf("the boundary fired on a gate close (%d calls) — HIGH-2 applies on the final "+
				"step too", rec.calls)
		}
	})
}

// lastStepEnd returns the most recent step_end record for an agent.
func lastStepEnd(t *testing.T, root, agent string) telemetry.StepEvent {
	t.Helper()
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	var end *telemetry.StepEvent
	for i := range records {
		if records[i].Event == telemetry.EventStepEnd {
			end = &records[i]
		}
	}
	if end == nil {
		t.Fatal("no step_end record was written")
	}
	return *end
}

// TestBoundaryHandoffMessage pins B-2: the final-step (improvement) boundary handoff must not tell
// the reader to run af prime for the next step — the formula is complete and the improvement
// session inherits (marker + urgent self-mail). The mid-workflow message is unchanged.
func TestBoundaryHandoffMessage(t *testing.T) {
	mid := boundaryHandoffMessage(82, "step s1", false)
	if !strings.Contains(mid, "run af prime for the next step") {
		t.Errorf("mid-workflow handoff must point at the next step, got %q", mid)
	}

	final := boundaryHandoffMessage(82, "formula fx", true)
	if strings.Contains(final, "run af prime for the next step") {
		t.Errorf("final-step handoff must NOT say 'run af prime for the next step' (no next step), got %q", final)
	}
	if !strings.Contains(final, "af improvement complete") {
		t.Errorf("final-step handoff must name the improvement-session inheritance, got %q", final)
	}
}
