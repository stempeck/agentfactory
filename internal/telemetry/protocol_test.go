package telemetry

import (
	"slices"
	"testing"
)

func i64(v int64) *int64 { return &v }

func deref(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

func measureFixture() []StepEvent {
	return []StepEvent{
		{
			Event: EventInstanceStart, InstanceID: "af-1", Formula: "efficiency", Agent: "engineer",
			TS: "2026-09-09T10:00:00.000Z", Model: "sonnet", AFCommit: "aaaa", CheckoutCommit: "cccc",
			SlingDigest: "dddd", TokenomicsState: TokenomicsStateOn, FormulaDigest: "f-start",
		},
		{
			Event: EventSessionStart, InstanceID: "af-1", Agent: "engineer", SessionID: "s-1",
			TS: "2026-09-09T10:00:01.000Z", EffortLevel: "high",
		},
		{
			Event: EventStepEnd, InstanceID: "af-1", Formula: "efficiency", Agent: "engineer",
			TS: "2026-09-09T10:05:00.000Z", StepID: "bd-1", StepLabel: "plan", SessionID: "s-1",
			Status:      StatusClosed,
			HostVersion: "2.1.0", EffortLevel: "high", DurationMS: 300_000,
			OutTokens: i64(1_000), SubagentTokens: i64(400), ThinkTokens: i64(700), ThinkTokensEst: i64(650),
			InTokens: i64(50_000), CacheReadTokens: i64(40_000), CacheCreationTokens: i64(9_000),
			SubagentLaunches: i64(1), WorkflowLaunches: i64(0), SubagentNestedLaunches: i64(0),
			RepeatReads: i64(3), GateFlags: i64(1),
		},
		{
			Event: EventStepEnd, InstanceID: "af-1", Formula: "efficiency", Agent: "engineer",
			TS: "2026-09-09T10:09:00.000Z", StepID: "bd-2", StepLabel: "build", SessionID: "s-2",
			Status:      StatusClosed,
			HostVersion: "2.1.0", EffortLevel: "medium", DurationMS: 240_000,
			OutTokens: i64(2_000), SubagentTokens: i64(600), ThinkTokens: i64(900), ThinkTokensEst: i64(880),
			InTokens: i64(60_000), CacheReadTokens: i64(50_000), CacheCreationTokens: i64(1_000),
			SubagentLaunches: i64(2), WorkflowLaunches: i64(1), SubagentNestedLaunches: i64(4),
			RepeatReads: i64(5), GateFlags: i64(0),
		},
		{
			Event: EventIntervention, InstanceID: "af-1", Agent: "engineer",
			TS: "2026-09-09T10:06:00.000Z", Mechanism: "budget", Action: ActionAdvise,
			Objective: ObjectiveCapacity,
		},
		{
			Event: EventInstanceEnd, InstanceID: "af-1", Formula: "efficiency", Agent: "engineer",
			TS: "2026-09-09T10:10:00.000Z", FormulaDigest: "f-end", BaseCommit: "bbbb",
		},
	}
}

func TestMeasureRunFoldsOneRunToScalars(t *testing.T) {
	t.Run("the pass metric is out plus subagent over closed steps", func(t *testing.T) {
		m := MeasureRun(measureFixture(), "af-1")

		out, subagent := deref(m.OutTokens), deref(m.SubagentTokens)
		if out != 3_000 || subagent != 1_000 {
			t.Errorf("out = %d, subagent = %d, want 3000 and 1000", out, subagent)
		}
		if m.Metric != 4_000 {
			t.Errorf("metric = %d, want 4000", m.Metric)
		}
		if m.Metric != out+subagent {
			t.Errorf("metric %d is not out %d + subagent %d", m.Metric, out, subagent)
		}
		// The thinking figures are the loudest numbers in the log and the easiest to fold in by
		// accident. Adding either would make the metric measure how hard the model thought rather
		// than what it produced, and the whole comparison would be about a diagnostic.
		think, thinkEst := deref(m.ThinkTokens), deref(m.ThinkTokensEst)
		if think != 1_600 || thinkEst != 1_530 {
			t.Errorf("think = %d, est = %d, want 1600 and 1530", think, thinkEst)
		}
		if m.Metric == out+subagent+think || m.Metric == out+subagent+thinkEst {
			t.Error("a thinking figure reached the pass metric; it is a diagnostic and is never added")
		}
	})

	t.Run("the diagnostic legs are carried beside the metric", func(t *testing.T) {
		m := MeasureRun(measureFixture(), "af-1")
		for _, tc := range []struct {
			name string
			got  int64
			want int64
		}{
			{"in_tokens", deref(m.InTokens), 110_000},
			{"cache_read_tokens", deref(m.CacheReadTokens), 90_000},
			{"cache_creation_tokens", deref(m.CacheCreationTokens), 10_000},
			{"subagent_launches", deref(m.SubagentLaunches), 3},
			{"workflow_launches", deref(m.WorkflowLaunches), 1},
			{"subagent_nested_launches", deref(m.SubagentNestedLaunches), 4},
			{"repeat_reads", deref(m.RepeatReads), 8},
			{"gate_flags", deref(m.GateFlags), 1},
		} {
			if tc.got != tc.want {
				t.Errorf("%s = %d, want %d", tc.name, tc.got, tc.want)
			}
		}
	})

	t.Run("the run's provenance is lifted off the records that carry it", func(t *testing.T) {
		m := MeasureRun(measureFixture(), "af-1")
		for _, tc := range []struct{ name, got, want string }{
			{"instance_id", m.InstanceID, "af-1"},
			{"formula", m.Formula, "efficiency"},
			{"agent", m.Agent, "engineer"},
			{"started_at", m.StartedAt, "2026-09-09T10:00:00.000Z"},
			{"model", m.Model, "sonnet"},
			{"af_commit", m.AFCommit, "aaaa"},
			{"checkout_commit", m.CheckoutCommit, "cccc"},
			{"base_commit", m.BaseCommit, "bbbb"},
			{"sling_digest", m.SlingDigest, "dddd"},
			{"tokenomics_state", m.TokenomicsState, TokenomicsStateOn},
			{"host_version", m.HostVersion, "2.1.0"},
			{"formula_digest_start", m.FormulaDigestStart, "f-start"},
			{"formula_digest_end", m.FormulaDigestEnd, "f-end"},
		} {
			if tc.got != tc.want {
				t.Errorf("%s = %q, want %q", tc.name, tc.got, tc.want)
			}
		}
		// The pair is the whole point: one digest can only say what the run started from, and a
		// formula edited mid-run is invisible unless both ends are carried.
		if m.FormulaDigestStart == m.FormulaDigestEnd {
			t.Error("the two digests were folded into one; a mid-run edit would be invisible")
		}
	})

	t.Run("completion is the instance_end record and nothing else", func(t *testing.T) {
		// D-12: no StepEvent carries a formula's declared step count, and max(step_seq) is computed
		// as totalSteps-openCount+1 — self-satisfying on exactly the crashed runs this catches.
		// instance_end is written at one site and only after the completion guard passes.
		full := measureFixture()
		if m := MeasureRun(full, "af-1"); !m.Completed {
			t.Error("a run with an instance_end record reads incomplete")
		}
		crashed := full[:len(full)-1]
		m := MeasureRun(crashed, "af-1")
		if m.Completed {
			t.Error("a run with no instance_end record reads complete; an unfinished before-run " +
				"would make min(before) unbeatable")
		}
		if m.FormulaDigestEnd != "" || m.BaseCommit != "" {
			t.Errorf("a crashed run reported a closing digest %q / base commit %q it never wrote",
				m.FormulaDigestEnd, m.BaseCommit)
		}
	})

	t.Run("steps closed, measured and interrupted", func(t *testing.T) {
		m := MeasureRun(measureFixture(), "af-1")
		if m.StepsClosed != 2 {
			t.Errorf("steps_closed = %d, want 2", m.StepsClosed)
		}
		if m.MeasuredSteps != 2 {
			t.Errorf("measured_steps = %d, want 2", m.MeasuredSteps)
		}
		if m.Sessions != 2 {
			t.Errorf("sessions = %d, want 2", m.Sessions)
		}
		if m.StatusClosed != 2 || m.StatusSkipped != 0 || m.StatusGateWaiting != 0 {
			t.Errorf("status distribution = %d/%d/%d, want 2/0/0",
				m.StatusClosed, m.StatusSkipped, m.StatusGateWaiting)
		}
		if !slices.Equal(m.EffortLevels, []string{"high", "medium"}) {
			t.Errorf("effort levels = %v, want [high medium] — sorted and deduped, so two runs are "+
				"comparable without the reader re-ordering them", m.EffortLevels)
		}

		unmeasured := append(measureFixture(), StepEvent{
			Event: EventStepEnd, InstanceID: "af-1", Formula: "efficiency", StepID: "bd-3",
			StepLabel: "ship", Status: StatusGateWaiting, GenerationUnmeasuredReason: ReasonTranscriptMissing,
		})
		u := MeasureRun(unmeasured, "af-1")
		if u.StepsClosed != 3 || u.MeasuredSteps != 2 {
			t.Errorf("steps_closed = %d, measured_steps = %d, want 3 and 2 — the shortfall is the "+
				"whole reason both are printed", u.StepsClosed, u.MeasuredSteps)
		}
		if u.StatusGateWaiting != 1 {
			t.Errorf("gate-waiting closes = %d, want 1", u.StatusGateWaiting)
		}

		opened := append(measureFixture(), StepEvent{
			Event: EventStepStart, InstanceID: "af-1", Formula: "efficiency", StepID: "bd-9",
			StepLabel: "ship",
		})
		if o := MeasureRun(opened, "af-1"); o.Interrupted != 1 {
			t.Errorf("interrupted = %d, want 1 for a step opened and never closed", o.Interrupted)
		}
	})

	t.Run("a run that delegated without recording sub-agent tokens is excluded", func(t *testing.T) {
		// C-9. Not a zero: an unmeasured sub-agent tree makes the run's metric an undercount of
		// unknown size, and averaging it into an arm biases the arm toward a pass.
		records := measureFixture()
		for i := range records {
			if records[i].Event == EventStepEnd {
				records[i].SubagentTokens = nil
			}
		}
		m := MeasureRun(records, "af-1")
		if m.ExcludedReason != ExcludedUnmeasuredDelegation {
			t.Errorf("excluded_reason = %q, want %q", m.ExcludedReason, ExcludedUnmeasuredDelegation)
		}

		if m := MeasureRun(measureFixture(), "af-1"); m.ExcludedReason != "" {
			t.Errorf("a fully measured run was excluded: %q", m.ExcludedReason)
		}

		noDelegation := measureFixture()
		for i := range noDelegation {
			if noDelegation[i].Event == EventStepEnd {
				noDelegation[i].SubagentTokens = nil
				noDelegation[i].SubagentLaunches = i64(0)
				noDelegation[i].WorkflowLaunches = i64(0)
			}
		}
		if m := MeasureRun(noDelegation, "af-1"); m.ExcludedReason != "" {
			t.Errorf("a run that never delegated was excluded: %q — there was nothing to miss",
				m.ExcludedReason)
		}
	})

	t.Run("a figure no step recorded is absent, never a zero", func(t *testing.T) {
		// The Gap-1 pathology: eleven of seventeen steps with nil out_tokens. A Σ of 0 would make
		// that run read as the cheapest in its arm rather than as the unmeasured one it is.
		records := measureFixture()
		for i := range records {
			records[i].OutTokens = nil
			records[i].SubagentNestedLaunches = nil
		}
		m := MeasureRun(records, "af-1")
		if m.OutTokens != nil {
			t.Errorf("out_tokens = %d on a run where no step recorded it, want absent", *m.OutTokens)
		}
		if m.MeasuredSteps != 0 {
			t.Errorf("measured_steps = %d, want 0", m.MeasuredSteps)
		}
		// Nesting evidence, for the same reason: an arm that counted nested launches compared
		// against one that never looked would read as a change in delegation depth nobody made.
		if m.SubagentNestedLaunches != nil {
			t.Error("a run that never recorded a nested launch reports a count")
		}
		if full := MeasureRun(measureFixture(), "af-1"); full.SubagentNestedLaunches == nil {
			t.Error("a run whose steps recorded nested launches reports none")
		}

		// A recorded zero is NOT absence, which is the whole point of keeping the two apart.
		zeroed := measureFixture()
		for i := range zeroed {
			if zeroed[i].Event == EventStepEnd {
				zeroed[i].OutTokens = i64(0)
			}
		}
		if z := MeasureRun(zeroed, "af-1"); z.OutTokens == nil || *z.OutTokens != 0 || z.MeasuredSteps != 2 {
			t.Errorf("a run that recorded zero out tokens folded to %v with %d measured steps, "+
				"want an explicit 0 across 2", z.OutTokens, z.MeasuredSteps)
		}
	})

	t.Run("interventions are grouped by mechanism and objective", func(t *testing.T) {
		records := append(measureFixture(),
			StepEvent{Event: EventIntervention, InstanceID: "af-1", Mechanism: "budget",
				Action: ActionAdvise, Objective: ObjectiveCapacity},
			StepEvent{Event: EventIntervention, InstanceID: "af-1", Mechanism: "thrift",
				Action: ActionHandoff, Objective: ObjectiveEfficiency},
		)
		m := MeasureRun(records, "af-1")
		want := []InterventionCount{
			{Mechanism: "budget", Objective: ObjectiveCapacity, Count: 2},
			{Mechanism: "thrift", Objective: ObjectiveEfficiency, Count: 1},
		}
		if !slices.Equal(m.Interventions, want) {
			t.Errorf("interventions = %+v, want %+v (sorted, so two runs line up)", m.Interventions, want)
		}
	})

	t.Run("another run's records never reach this run", func(t *testing.T) {
		records := append(measureFixture(), StepEvent{
			Event: EventStepEnd, InstanceID: "af-2", Formula: "efficiency", StepID: "bd-1",
			StepLabel: "plan",
			Status:    StatusClosed, OutTokens: i64(999_999), SubagentTokens: i64(999_999),
		})
		m := MeasureRun(records, "af-2")
		if m.Metric != 1_999_998 || m.StepsClosed != 1 {
			t.Errorf("metric = %d, steps_closed = %d, want 1999998 and 1", m.Metric, m.StepsClosed)
		}
		if MeasureRun(records, "af-1").Metric != 4_000 {
			t.Error("af-2's records leaked into af-1's fold")
		}
	})

	t.Run("the fold is pure and order-independent", func(t *testing.T) {
		forward := measureFixture()
		backward := measureFixture()
		slices.Reverse(backward)
		a, b := MeasureRun(forward, "af-1"), MeasureRun(backward, "af-1")
		if a.Metric != b.Metric || a.StepsClosed != b.StepsClosed || a.Completed != b.Completed ||
			a.StartedAt != b.StartedAt || !slices.Equal(a.EffortLevels, b.EffortLevels) {
			t.Errorf("the fold depends on record order: %+v vs %+v", a, b)
		}
	})
}

// TestDelegationExclusionIsPerStep pins F2: the C-9 unmeasured-delegation exclusion is decided per
// CLOSED STEP, not from run-wide folds (MeasureRun). A run with one step that delegated and recorded
// nothing and a second step that delegated and recorded its tokens must still be EXCLUDED — a sibling
// step measuring its own delegation must not launder the first step's unmeasured spend. The fields are
// *int64, so nil (unmeasured) is distinguishable from 0 (measured as zero).
func TestDelegationExclusionIsPerStep(t *testing.T) {
	t.Run("a step that delegated without recording tokens excludes the run even when a sibling step measured its own", func(t *testing.T) {
		// bd-1 launches a sub-agent and records no tokens; bd-2 launches and records 600.
		records := measureFixture()
		for i := range records {
			if records[i].Event == EventStepEnd && records[i].StepID == "bd-1" {
				records[i].SubagentTokens = nil
			}
		}
		m := MeasureRun(records, "af-1")
		if m.ExcludedReason != ExcludedUnmeasuredDelegation {
			t.Errorf("excluded_reason = %q, want %q — bd-1 delegated and recorded no sub-agent tokens; "+
				"bd-2 measuring its own delegation must not launder bd-1's unmeasured spend",
				m.ExcludedReason, ExcludedUnmeasuredDelegation)
		}
	})

	t.Run("PROTECT: a run whose every delegated step recorded its tokens is not excluded", func(t *testing.T) {
		if m := MeasureRun(measureFixture(), "af-1"); m.ExcludedReason != "" {
			t.Errorf("a fully measured run was excluded: %q", m.ExcludedReason)
		}
	})

	t.Run("PROTECT: a run that never delegated is not excluded", func(t *testing.T) {
		noDelegation := measureFixture()
		for i := range noDelegation {
			if noDelegation[i].Event == EventStepEnd {
				noDelegation[i].SubagentTokens = nil
				noDelegation[i].SubagentLaunches = i64(0)
				noDelegation[i].WorkflowLaunches = i64(0)
			}
		}
		if m := MeasureRun(noDelegation, "af-1"); m.ExcludedReason != "" {
			t.Errorf("a run that never delegated was excluded: %q — there was nothing to miss", m.ExcludedReason)
		}
	})
}
