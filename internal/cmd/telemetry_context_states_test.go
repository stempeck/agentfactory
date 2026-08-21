package cmd

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// These tests pin #622 C6: what the report says about a step's context window, and — the whole
// point of the issue — how many DIFFERENT things "no number" can mean. Five of them must stay
// distinguishable: nobody measured, no bound was recorded, the session changed mid-step
// (unattributable), the step_start was never recorded (unmeasurable), and the funnel recorded no
// occupancy (UNKNOWN). Collapsing any pair reports a wedged agent as maximally healthy.

func i64p(v int64) *int64     { return &v }
func f64p(v float64) *float64 { return &v }

// contextStateFixture seeds one record set covering every state C6 must render distinctly, plus
// the two drift markers. It is shared by the table and JSON tests so the two surfaces are proven
// to describe the SAME data — the property telemetry_json.go:317-324 states as a contract.
func contextStateFixture(t *testing.T) string {
	t.Helper()
	root := setupTestFactoryForPrime(t)
	t.Chdir(root)
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
	resetReportFlags(t)

	start := func(step, title, session string, ctxUsed, ctxTotal, cum *int64, pct *float64) telemetry.StepEvent {
		return telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: "2026-07-22T18:31:04.112Z", Agent: "manager", Formula: "offpath",
			InstanceID: "i1", StepID: step, StepTitle: title, SessionID: session,
			Verb: "prime", VerbMS: 12,
			CtxUsedPct: pct, CtxTokensUsed: ctxUsed, CtxTokensTotal: ctxTotal,
			CtxObservedAt: "2026-07-22T18:31:04.000Z", CumTokens: cum,
		}
	}
	// ctxStart is the step_start echo done.go writes onto the close (done.go:204). It is passed
	// explicitly rather than derived so a fixture can carry the echo WITHOUT a matching
	// step_start — the shape that proves the reader's source precedence.
	end := func(step, title, session string, ctxStart, ctxUsed, ctxTotal, cum *int64, pct *float64, bound int64) telemetry.StepEvent {
		// The write side records occupancy all-or-none (step_context.go), so a record with no
		// figures carries no observation stamp either. A fixture that broke that pairing would be
		// asserting against a record af never writes.
		observedAt := "2026-07-22T18:41:10.000Z"
		if ctxUsed == nil && pct == nil {
			observedAt = ""
		}
		return telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-07-22T18:41:10.500Z", Agent: "manager", Formula: "offpath",
			InstanceID: "i1", StepID: step, StepTitle: title, SessionID: session,
			Verb: "done", VerbMS: 77, DurationMS: 6388, Status: telemetry.StatusClosed,
			CtxUsedPct: pct, CtxTokensUsed: ctxUsed, CtxTokensTotal: ctxTotal,
			CtxObservedAt: observedAt, CumTokens: cum,
			CtxTokensStart: ctxStart, CtxBoundTokens: bound,
		}
	}

	events := []telemetry.StepEvent{
		// 1. under-bound — everything measured, nothing exceeded.
		start("s1", "Under bound", "sess-a", i64p(84000), i64p(200000), i64p(10000), f64p(42)),
		end("s1", "Under bound", "sess-a", i64p(84000), i64p(120000), i64p(200000), i64p(26000), f64p(60), 200000),

		// 2. over-consumption — the delta exceeds the bound while the window at close does not.
		start("s2", "Over consumption", "sess-a", i64p(84000), i64p(200000), i64p(10000), f64p(42)),
		end("s2", "Over consumption", "sess-a", i64p(84000), i64p(120000), i64p(200000), i64p(260000), f64p(60), 200000),

		// 3. over-occupancy — the window at close exceeds the bound while the delta does not.
		start("s3", "Over occupancy", "sess-a", i64p(84000), i64p(220000), i64p(10000), f64p(38)),
		end("s3", "Over occupancy", "sess-a", i64p(84000), i64p(210000), i64p(220000), i64p(26000), f64p(95), 200000),

		// 4. compacted mid-step — occupancy FELL across the step; cum_tokens still rose.
		start("s4", "Compacted", "sess-a", i64p(84000), i64p(200000), i64p(10000), f64p(42)),
		end("s4", "Compacted", "sess-a", i64p(84000), i64p(30000), i64p(200000), i64p(40000), f64p(15), 200000),

		// 5. unmeasured — records predating the feature, on a factory whose bound could not be read.
		start("s5", "Unmeasured", "sess-a", nil, nil, nil, nil),
		end("s5", "Unmeasured", "sess-a", nil, nil, nil, nil, nil, 0),

		// 6. recycled mid-step — the session changed between the two ends. The end also carries a
		// cum_tokens_delta as an older binary would have written it; the read side must still refuse.
		start("s6", "Recycled", "sess-a", i64p(84000), i64p(200000), i64p(10000), f64p(42)),
		func() telemetry.StepEvent {
			ev := end("s6", "Recycled", "sess-b", i64p(84000), i64p(120000), i64p(200000), i64p(5000), f64p(60), 200000)
			ev.CumTokensDelta = i64p(999999)
			return ev
		}(),

		// 7. close-without-start — the seventh, pre-existing state. cum_tokens at open is unknown,
		// so consumption is UNMEASURABLE, never unattributable. Nothing at all is known about the
		// open, so the start figure is absent too.
		end("s7", "Close without start", "sess-a", nil, i64p(120000), i64p(200000), i64p(26000), f64p(60), 200000),

		// 8. unreachable-bound drift — the bound cannot be reached in the window it is judged against.
		start("s8", "Bound drift", "sess-a", i64p(84000), i64p(200000), i64p(10000), f64p(42)),
		end("s8", "Bound drift", "sess-a", i64p(84000), i64p(120000), i64p(200000), i64p(26000), f64p(60), 300000),

		// 10. rotation echo — the step_start was rotated out of the log, but done.go's own span read
		// captured the opening window onto the close. The recorded echo is the fallback source.
		end("s10", "Rotated open", "sess-a", i64p(70000), i64p(120000), i64p(200000), i64p(26000), f64p(60), 200000),

		// 11. session UNKNOWN — the non-vacuity twin of state 6. telemetry_record.go:100-106 writes
		// "" for the id whenever .runtime/session_id cannot be read, and an absent id says the
		// session is unknown, not that it changed. Everything else here is measured, so only the
		// session gate can decide the row — and it must decide UNMEASURABLE. Treating absent as a
		// mismatch would print "recycled mid-step" over a step that was never recycled, which Phase
		// 4 reads as its worst-class signal.
		//
		// The state is currently LATENT rather than observed: every record without a session id in
		// this repository's own telemetry today is an unpaired one, so none reaches this comparison.
		// That is why it is pinned here — a latent wrong answer has nothing else to catch it.
		start("s11", "Session unknown", "", i64p(84000), i64p(200000), i64p(10000), f64p(42)),
		end("s11", "Session unknown", "", i64p(84000), i64p(120000), i64p(200000), i64p(26000), f64p(60), 200000),
	}

	// 9. staleness — the occupancy was observed ten minutes before the record that carries it.
	staleStart := start("s9", "Stale reading", "sess-a", i64p(84000), i64p(200000), i64p(10000), f64p(42))
	staleEnd := end("s9", "Stale reading", "sess-a", i64p(84000), i64p(120000), i64p(200000), i64p(26000), f64p(60), 200000)
	staleEnd.CtxObservedAt = "2026-07-22T18:21:10.000Z"
	events = append(events, staleStart, staleEnd)

	for _, ev := range events {
		if err := telemetry.AppendEvent(config.TelemetryDir(root), ev); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	return root
}

func TestReportRendersContextStates(t *testing.T) {
	contextStateFixture(t)
	out := renderReport(t)

	row := func(step string) string { return lineContaining(t, out, step) }

	t.Run("under bound carries every figure and no verdict", func(t *testing.T) {
		line := row("Under bound")
		for _, want := range []string{"84000", "120000", "16000", "200000", verdictUnderBound} {
			if !strings.Contains(line, want) {
				t.Errorf("row is missing %q:\n%s", want, line)
			}
		}
		if strings.Contains(line, verdictOverOccupancy) || strings.Contains(line, verdictOverConsumption) {
			t.Errorf("a step inside its bound was flagged:\n%s", line)
		}
	})

	t.Run("over consumption is flagged independently of occupancy", func(t *testing.T) {
		line := row("Over consumption")
		if !strings.Contains(line, verdictOverConsumption) {
			t.Errorf("a step whose delta exceeded its bound was not flagged:\n%s", line)
		}
		if strings.Contains(line, verdictOverOccupancy) {
			t.Errorf("over_consumption dragged over_occupancy with it:\n%s", line)
		}
		if !strings.Contains(line, "250000") {
			t.Errorf("row does not carry the consumed delta:\n%s", line)
		}
	})

	t.Run("over occupancy is flagged independently of consumption", func(t *testing.T) {
		line := row("Over occupancy")
		if !strings.Contains(line, verdictOverOccupancy) {
			t.Errorf("a window that closed over its bound was not flagged:\n%s", line)
		}
		if strings.Contains(line, verdictOverConsumption) {
			t.Errorf("over_occupancy dragged over_consumption with it:\n%s", line)
		}
	})

	t.Run("a compacted step says so and shows no negative figure", func(t *testing.T) {
		line := row("Compacted")
		if !strings.Contains(line, markerCompacted) {
			t.Errorf("a step whose occupancy fell was not marked %q:\n%s", markerCompacted, line)
		}
		if strings.Contains(line, "-54000") {
			t.Errorf("row renders a negative occupancy delta:\n%s", line)
		}
	})

	t.Run("an unmeasured step renders dashes, never zeroes", func(t *testing.T) {
		line := row("Unmeasured")
		// Token-exact rather than a substring search: the STARTED column is a timestamp and is
		// full of zeroes. What must never appear is a context CELL that reads as a measurement.
		for _, field := range strings.Fields(line) {
			switch field {
			case "0", "0%", "0.0%":
				t.Errorf("an unmeasured row rendered %q; absent is not zero:\n%s", field, line)
			}
		}
		if strings.Contains(line, verdictOverOccupancy) || strings.Contains(line, verdictOverConsumption) {
			t.Errorf("an unmeasured row was judged against a bound of 0:\n%s", line)
		}
		if !strings.Contains(line, verdictUnmeasurable) {
			t.Errorf("an unmeasured row does not say %q:\n%s", verdictUnmeasurable, line)
		}
	})

	t.Run("a session mismatch is unattributable and carries no delta", func(t *testing.T) {
		line := row("Recycled")
		if !strings.Contains(line, markerRecycledMidStep) {
			t.Errorf("row does not state %q:\n%s", markerRecycledMidStep, line)
		}
		if !strings.Contains(line, verdictUnattributable) {
			t.Errorf("row does not mark consumption %q:\n%s", verdictUnattributable, line)
		}
		if strings.Contains(line, "999999") {
			t.Errorf("row carried the recorded cum_tokens_delta across a session boundary:\n%s", line)
		}
		if strings.Contains(line, verdictOverConsumption) {
			t.Errorf("an unattributable delta produced a consumption verdict:\n%s", line)
		}
	})

	t.Run("a close without a start is unmeasurable, not unattributable", func(t *testing.T) {
		line := row("Close without start")
		if !strings.Contains(line, verdictUnmeasurable) {
			t.Errorf("row does not mark consumption %q:\n%s", verdictUnmeasurable, line)
		}
		if strings.Contains(line, verdictUnattributable) {
			t.Errorf("an unrecorded start was reported as a session mismatch:\n%s", line)
		}
	})

	t.Run("an unknown session is unmeasurable and claims no recycle", func(t *testing.T) {
		line := row("Session unknown")
		if !strings.Contains(line, verdictUnmeasurable) {
			t.Errorf("row does not mark consumption %q:\n%s", verdictUnmeasurable, line)
		}
		if strings.Contains(line, verdictUnattributable) {
			t.Errorf("an absent session id was reported as a session mismatch:\n%s", line)
		}
		if strings.Contains(line, markerRecycledMidStep) {
			t.Errorf("a step with no session id was declared recycled:\n%s", line)
		}
		// The occupancy half is untouched by the session gate — proving the row is unmeasurable
		// for the one reason it should be, rather than because the fixture measured nothing.
		if !strings.Contains(line, verdictUnderBound) {
			t.Errorf("the occupancy verdict was lost with the session id:\n%s", line)
		}
	})

	t.Run("a bound larger than the window is flagged as drift", func(t *testing.T) {
		line := row("Bound drift")
		if !strings.Contains(line, markerBoundDrift) {
			t.Errorf("an unreachable bound was not flagged:\n%s", line)
		}
	})

	t.Run("a lagging observation is marked stale", func(t *testing.T) {
		line := row("Stale reading")
		if !strings.Contains(line, markerStaleOccupancy) {
			t.Errorf("an occupancy observed long before the close was not marked stale:\n%s", line)
		}
	})

	t.Run("the seven original columns survive", func(t *testing.T) {
		lower := strings.ToLower(out)
		for _, col := range []string{"agent", "step", "status", "duration", "started", "model", "verb_ms"} {
			if !strings.Contains(lower, col) {
				t.Errorf("report header lost the %q column:\n%s", col, out)
			}
		}
	})

	t.Run("absent figures render ASCII dash, never an em dash", func(t *testing.T) {
		if strings.Contains(row("Unmeasured"), "\u2014") {
			t.Errorf("an em dash reached the table as rendered data:\n%s", row("Unmeasured"))
		}
	})
}

func TestReportRendersContextStatesJSON(t *testing.T) {
	contextStateFixture(t)
	enableTelemetryJSON(t)

	out, err := runTelemetryJSON(t, "report")
	if err != nil {
		t.Fatalf("report --json: %v", err)
	}
	row := func(step string) map[string]json.RawMessage { return jsonReportRowByStep(t, out, step) }
	raw := func(r map[string]json.RawMessage, key string) string {
		return strings.TrimSpace(string(r[key]))
	}

	t.Run("under bound carries comparable numbers and false verdicts", func(t *testing.T) {
		r := row("Under bound")
		for key, want := range map[string]string{
			"ctx_tokens_start": "84000",
			"ctx_tokens_end":   "120000",
			"ctx_tokens_total": "200000",
			"ctx_used_pct":     "60",
			"cum_tokens_delta": "16000",
			"ctx_bound_tokens": "200000",
			"over_occupancy":   "false",
			"over_consumption": "false",
		} {
			if got := raw(r, key); got != want {
				t.Errorf("%s = %s, want %s", key, got, want)
			}
		}
		if got := jsonString(t, r["consumption_state"]); got != consumptionMeasured {
			t.Errorf("consumption_state = %q, want %q", got, consumptionMeasured)
		}
	})

	t.Run("the two verdicts are independent", func(t *testing.T) {
		if got := raw(row("Over consumption"), "over_consumption"); got != "true" {
			t.Errorf("over_consumption = %s, want true", got)
		}
		if got := raw(row("Over consumption"), "over_occupancy"); got != "false" {
			t.Errorf("over_occupancy = %s, want false", got)
		}
		if got := raw(row("Over occupancy"), "over_occupancy"); got != "true" {
			t.Errorf("over_occupancy = %s, want true", got)
		}
		if got := raw(row("Over occupancy"), "over_consumption"); got != "false" {
			t.Errorf("over_consumption = %s, want false", got)
		}
	})

	t.Run("an unmeasured step spells every absence null, never 0", func(t *testing.T) {
		r := row("Unmeasured")
		for _, key := range []string{
			"ctx_tokens_start", "ctx_tokens_end", "ctx_tokens_total", "ctx_used_pct",
			"cum_tokens_delta", "ctx_bound_tokens", "over_occupancy", "over_consumption",
			"compacted_mid_step", "ctx_observed_stale", "bound_exceeds_window",
			"interrupted_observed_pct",
		} {
			if got := raw(r, key); got != "null" {
				t.Errorf("%s = %s, want null", key, got)
			}
		}
		if got := jsonString(t, r["consumption_state"]); got != consumptionUnmeasurable {
			t.Errorf("consumption_state = %q, want %q", got, consumptionUnmeasurable)
		}
	})

	t.Run("the JSON surface never carries the table's dash sentinel", func(t *testing.T) {
		r := row("Unmeasured")
		for key, v := range r {
			if s := string(v); strings.Contains(s, `"-"`) || strings.Contains(s, "\u2014") {
				t.Errorf("%s = %s: a dash is a thing to print, not a thing to parse", key, s)
			}
		}
	})

	t.Run("a session mismatch carries no delta and no consumption verdict", func(t *testing.T) {
		r := row("Recycled")
		if got := raw(r, "cum_tokens_delta"); got != "null" {
			t.Errorf("cum_tokens_delta = %s, want null across a session boundary", got)
		}
		if got := raw(r, "over_consumption"); got != "null" {
			t.Errorf("over_consumption = %s, want null across a session boundary", got)
		}
		if got := jsonString(t, r["consumption_state"]); got != consumptionUnattributable {
			t.Errorf("consumption_state = %q, want %q", got, consumptionUnattributable)
		}
		// The occupancy verdict survives: both of its inputs live on the step_end record.
		if got := raw(r, "over_occupancy"); got != "false" {
			t.Errorf("over_occupancy = %s, want false — a session mismatch does not blind it", got)
		}
	})

	t.Run("a close without a start is unmeasurable", func(t *testing.T) {
		r := row("Close without start")
		if got := jsonString(t, r["consumption_state"]); got != consumptionUnmeasurable {
			t.Errorf("consumption_state = %q, want %q", got, consumptionUnmeasurable)
		}
		if got := raw(r, "ctx_tokens_start"); got != "null" {
			t.Errorf("ctx_tokens_start = %s, want null when no start was recorded", got)
		}
	})

	t.Run("an unknown session is unmeasurable, not unattributable", func(t *testing.T) {
		r := row("Session unknown")
		if got := jsonString(t, r["consumption_state"]); got != consumptionUnmeasurable {
			t.Errorf("consumption_state = %q, want %q — an absent id is not a mismatch", got, consumptionUnmeasurable)
		}
		if got := raw(r, "cum_tokens_delta"); got != "null" {
			t.Errorf("cum_tokens_delta = %s, want null with no session to attribute it to", got)
		}
		if got := raw(r, "over_occupancy"); got != "false" {
			t.Errorf("over_occupancy = %s, want false — the session gate governs consumption only", got)
		}
	})

	t.Run("the close's own start echo is used when the step_start was rotated away", func(t *testing.T) {
		r := row("Rotated open")
		if got := raw(r, "ctx_tokens_start"); got != "70000" {
			t.Errorf("ctx_tokens_start = %s, want 70000 from the close's recorded echo", got)
		}
		if got := raw(r, "compacted_mid_step"); got != "false" {
			t.Errorf("compacted_mid_step = %s, want false — the echo is enough to decide it", got)
		}
	})

	t.Run("the markers are booleans, nil only when undecidable", func(t *testing.T) {
		if got := raw(row("Compacted"), "compacted_mid_step"); got != "true" {
			t.Errorf("compacted_mid_step = %s, want true", got)
		}
		if got := raw(row("Under bound"), "compacted_mid_step"); got != "false" {
			t.Errorf("compacted_mid_step = %s, want false", got)
		}
		if got := raw(row("Bound drift"), "bound_exceeds_window"); got != "true" {
			t.Errorf("bound_exceeds_window = %s, want true", got)
		}
		if got := raw(row("Stale reading"), "ctx_observed_stale"); got != "true" {
			t.Errorf("ctx_observed_stale = %s, want true", got)
		}
		if got := raw(row("Under bound"), "ctx_observed_stale"); got != "false" {
			t.Errorf("ctx_observed_stale = %s, want false", got)
		}
	})

	t.Run("a row that was never interrupted says so without inventing a trigger", func(t *testing.T) {
		r := row("Under bound")
		if got := jsonString(t, r["interrupted_trigger"]); got != "" {
			t.Errorf("interrupted_trigger = %q, want empty", got)
		}
		if got := raw(r, "interrupted_observed_pct"); got != "null" {
			t.Errorf("interrupted_observed_pct = %s, want null", got)
		}
	})
}
