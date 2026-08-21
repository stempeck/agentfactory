package cmd

import (
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// TestPrimeStepStartCarriesOccupancy pins C4's prime half: the step_start record a first prime
// writes carries the occupancy of the session that is about to run the step.
func TestPrimeStepStartCarriesOccupancy(t *testing.T) {
	now := boundaryTestNow()
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	epic, _ := seedFormulaBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "session_id", "sess.a")
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 42, 4242, now.Add(-10*time.Second), now)

	if err := runPrimeInFixture(t); err != nil {
		t.Fatalf("af prime: %v", err)
	}

	start := firstStepStart(t, fx.root, fx.agent)
	if start.CtxUsedPct == nil || *start.CtxUsedPct != 42 {
		t.Errorf("ctx_used_pct = %v, want 42", start.CtxUsedPct)
	}
	if start.CtxTokensUsed == nil || *start.CtxTokensUsed != 84000 {
		t.Errorf("ctx_tokens_used = %v, want 84000", start.CtxTokensUsed)
	}
	if start.CtxTokensTotal == nil || *start.CtxTokensTotal != 200000 {
		t.Errorf("ctx_tokens_total = %v, want 200000", start.CtxTokensTotal)
	}
	if start.CtxObservedAt == "" {
		t.Error("ctx_observed_at is empty; the datum's own capture time is what makes it auditable")
	}
	if start.CumTokens == nil || *start.CumTokens != 4242 {
		t.Errorf("cum_tokens = %v, want 4242", start.CumTokens)
	}
}

// TestPrimeStepStartOccupancyNilWhenNotMeasured is the absent-not-zero invariant, which is the
// whole reason these fields are pointers. A zero here would be indistinguishable from an empty
// context window, and the improvement loop would draw the opposite conclusion from the truth.
func TestPrimeStepStartOccupancyNilWhenNotMeasured(t *testing.T) {
	cases := []struct {
		name  string
		plant func(t *testing.T, root, agent string, now time.Time)
	}{
		{"absent", func(*testing.T, string, string, time.Time) {}},
		{"stale", func(t *testing.T, root, agent string, now time.Time) {
			plantSessionSnapshot(t, root, agent, "sessa", 90, 1000, now.Add(-5*time.Minute), now)
		}},
		{"dark", func(t *testing.T, root, agent string, now time.Time) {
			plantSessionSnapshot(t, root, agent, "sessa", 90, 1000, now.Add(-30*time.Minute), now)
		}},
		{"malformed", func(t *testing.T, root, agent string, now time.Time) {
			plantRawSnapshot(t, root, "sessa", `{"schema":2,"session_id":"sessa","agent":"`+agent+`"}`)
		}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			now := boundaryTestNow()
			fx := newLifecycleFixture(t)
			gateOn(t, fx.root)

			epic, _ := seedFormulaBeads(t, fx)
			writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
			writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
			tc.plant(t, fx.root, fx.agent, now)

			if err := runPrimeInFixture(t); err != nil {
				t.Fatalf("af prime: %v", err)
			}

			start := firstStepStart(t, fx.root, fx.agent)
			if start.CtxUsedPct != nil || start.CtxTokensUsed != nil || start.CtxTokensTotal != nil {
				t.Errorf("%s occupancy recorded a value (%v/%v/%v); absent is not the same fact as zero",
					tc.name, start.CtxUsedPct, start.CtxTokensUsed, start.CtxTokensTotal)
			}
			if start.CtxObservedAt != "" {
				t.Errorf("%s recorded ctx_observed_at %q for a datum it never trusted", tc.name, start.CtxObservedAt)
			}
			if start.CumTokens != nil {
				t.Errorf("%s recorded cum_tokens %v", tc.name, *start.CumTokens)
			}
		})
	}
}

// TestStepEndCarriesOccupancyAndSpanFields pins C4's done half, including the two figures that
// can only come from the step_start record: ctx_tokens_start and the cum_tokens delta. Both ride
// the SAME telemetryStepSpan read that already yields step_seq and duration_ms (G13, scale.md S1).
func TestStepEndCarriesOccupancyAndSpanFields(t *testing.T) {
	now := boundaryTestNow()
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	epic, _ := seedTwoStepBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 30, 10000, now.Add(-10*time.Second), now)

	if err := runPrimeInFixture(t); err != nil {
		t.Fatalf("af prime: %v", err)
	}

	// The step consumed context: a second snapshot for the same session, later and higher.
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 55, 26000, now.Add(-2*time.Second), now)

	(&mailRecorder{}).install(t)
	(&boundaryRecorder{}).install(t)
	if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
		t.Fatalf("af done: %v", err)
	}

	start := firstStepStart(t, fx.root, fx.agent)
	end := lastStepEnd(t, fx.root, fx.agent)

	if end.CtxUsedPct == nil || *end.CtxUsedPct != 55 {
		t.Errorf("step_end ctx_used_pct = %v, want the occupancy at close (55)", end.CtxUsedPct)
	}
	if end.CtxTokensStart == nil || start.CtxTokensUsed == nil || *end.CtxTokensStart != *start.CtxTokensUsed {
		t.Errorf("ctx_tokens_start = %v, want it carried from the step_start record (%v)",
			end.CtxTokensStart, start.CtxTokensUsed)
	}
	if end.CumTokensDelta == nil || *end.CumTokensDelta != 16000 {
		t.Errorf("cum_tokens_delta = %v, want 26000-10000", end.CumTokensDelta)
	}
	if end.CtxBoundTokens != 200000 {
		t.Errorf("ctx_bound_tokens = %d, want the configured bound (200000)", end.CtxBoundTokens)
	}
	// The G13 no-regression leg: the two figures the span already carried must be unchanged.
	if end.StepSeq != start.StepSeq || end.StepSeq < 1 {
		t.Errorf("step_seq = %d, want it carried from the start record (%d)", end.StepSeq, start.StepSeq)
	}
	if end.DurationMS < 0 {
		t.Errorf("duration_ms = %d, want a non-negative measurement", end.DurationMS)
	}
}

// TestStepEndDeltaSuppressedAcrossSessions is HIGH-3 at the write site: cum_tokens is a
// per-session counter, so a step whose two ends were measured in different sessions has no
// meaningful consumption figure — and a fabricated one would be the loop's PRIMARY number.
func TestStepEndDeltaSuppressedAcrossSessions(t *testing.T) {
	now := boundaryTestNow()
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	epic, _ := seedTwoStepBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 30, 10000, now.Add(-10*time.Second), now)

	if err := runPrimeInFixture(t); err != nil {
		t.Fatalf("af prime: %v", err)
	}

	// A recycle happened mid-step: a new session id, and its own counter starting low.
	writeRuntimeFile(t, fx.workDir, "session_id", "sessb")
	plantSessionSnapshot(t, fx.root, fx.agent, "sessb", 20, 500, now.Add(-2*time.Second), now)

	(&mailRecorder{}).install(t)
	(&boundaryRecorder{}).install(t)
	if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
		t.Fatalf("af done: %v", err)
	}

	end := lastStepEnd(t, fx.root, fx.agent)
	if end.CumTokensDelta != nil {
		t.Errorf("cum_tokens_delta = %v across a session change; want nil (HIGH-3) rather than a "+
			"negative figure the report would render as a measurement", *end.CumTokensDelta)
	}
	if end.CtxUsedPct == nil {
		t.Error("the end occupancy is still a fact about this session and must be recorded")
	}
}

// firstStepStart returns the earliest step_start record for an agent.
func firstStepStart(t *testing.T, root, agent string) telemetry.StepEvent {
	t.Helper()
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	for i := range records {
		if records[i].Event == telemetry.EventStepStart {
			return records[i]
		}
	}
	t.Fatal("no step_start record was written")
	return telemetry.StepEvent{}
}
