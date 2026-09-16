package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// #678 K9's third deliverable. The Measurement Protocol is the only definition of "improves" the
// design has, and until this verb exists the ten-run comparison is an operator with a spreadsheet.
// These tests pin the properties that make the verdict worth trusting: the arm checks that void it,
// the exclusion that keeps an undercounted run out, and the outcome audit that rides beside it so a
// cheaper run is not mistaken for a better one.

const (
	compareTestFormula = "efficiency"
	compareTestAgent   = "manager"
)

type compareRun struct {
	id       string
	metric   int64
	digest   string
	state    string
	statuses []string
}

// seedCompareRun writes one whole run: instance_start, one step_end per status, instance_end.
// Written through the shipped appender so the fixture cannot describe a log the factory would
// never produce.
func seedCompareRun(t *testing.T, dir string, r compareRun) {
	t.Helper()
	if r.statuses == nil {
		r.statuses = []string{telemetry.StatusClosed, telemetry.StatusClosed}
	}
	base := telemetry.StepEvent{
		V: telemetry.SchemaVersion, Agent: compareTestAgent, Formula: compareTestFormula,
		InstanceID: r.id, Model: "fable-5", ModelSource: telemetry.ModelSourceModelsJSON,
	}

	start := base
	start.Event, start.TS, start.Verb = telemetry.EventInstanceStart, "2026-09-09T10:00:00.000Z", "sling"
	start.AFCommit, start.CheckoutCommit = "commit-af", "commit-tree"
	start.SlingDigest, start.TokenomicsState, start.FormulaDigest = "digest-in", r.state, r.digest
	appendCompareEvent(t, dir, start)

	// The metric is split across the closed steps so the sum is the run's figure and no single
	// step carries it — the shape a real run has.
	per := r.metric / int64(len(r.statuses))
	for i, status := range r.statuses {
		step := base
		step.Event, step.TS = telemetry.EventStepEnd, fmt.Sprintf("2026-09-09T10:0%d:00.000Z", i+1)
		step.StepID, step.StepLabel = fmt.Sprintf("bd-%d", i), fmt.Sprintf("step-%d", i)
		step.StepSeq, step.SessionID, step.Verb = i+1, "sess-a", "done"
		step.Status, step.DurationMS, step.HostVersion = status, 60_000, "2.1.0"
		step.EffortLevel = "high"
		out := per
		if i == len(r.statuses)-1 {
			out = r.metric - per*int64(len(r.statuses)-1)
		}
		step.OutTokens = i64p(out)
		step.SubagentTokens = i64p(0)
		step.ThinkTokens, step.ThinkTokensEst = i64p(out/2), i64p(out/3)
		step.SubagentLaunches, step.WorkflowLaunches = i64p(0), i64p(0)
		step.SubagentNestedLaunches, step.RepeatReads = i64p(0), i64p(2)
		step.GateFlags = i64p(0)
		step.InTokens, step.CacheReadTokens, step.CacheCreationTokens = i64p(1_000), i64p(900), i64p(100)
		appendCompareEvent(t, dir, step)
	}

	end := base
	end.Event, end.TS, end.Verb = telemetry.EventInstanceEnd, "2026-09-09T10:30:00.000Z", "done"
	end.FormulaDigest, end.BaseCommit = r.digest, "commit-tree"
	appendCompareEvent(t, dir, end)
}

func appendCompareEvent(t *testing.T, dir string, ev telemetry.StepEvent) {
	t.Helper()
	if compareMutation != nil {
		compareMutation(&ev)
	}
	if err := telemetry.AppendEvent(dir, ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
}

// appendMalformedTelemetryLine puts one unparseable line in an agent's record log, which is what
// makes the degraded state reachable: a compare that dropped it silently could report a pass
// computed over a subset of the arm.
func appendMalformedTelemetryLine(t *testing.T, root, agent string) {
	t.Helper()
	path := filepath.Join(config.TelemetryDir(root), "steps", agent+".jsonl")
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	if _, err := f.WriteString("{not json at all\n"); err != nil {
		t.Fatalf("write malformed line: %v", err)
	}
}

func deref64(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// seedCompareArms writes a full surface-b comparison: five off-posture before runs and five
// on-posture after runs, one formula digest across all ten.
func seedCompareArms(t *testing.T, before, after []int64) string {
	t.Helper()
	root := setupTestFactoryForPrime(t)
	t.Chdir(root)
	seedTelemetryGate(t, root)

	dir := config.TelemetryDir(root)
	for i, metric := range before {
		seedCompareRun(t, dir, compareRun{
			id: fmt.Sprintf("af-b%d", i), metric: metric,
			digest: "digest-f", state: telemetry.TokenomicsStateOff,
		})
	}
	for i, metric := range after {
		seedCompareRun(t, dir, compareRun{
			id: fmt.Sprintf("af-a%d", i), metric: metric,
			digest: "digest-f", state: telemetry.TokenomicsStateOn,
		})
	}
	return root
}

func compareIDs(prefix string, n int) string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("af-%s%d", prefix, i))
	}
	return strings.Join(ids, ",")
}

func setCompareFlags(t *testing.T, pairs ...string) {
	t.Helper()
	for i := 0; i < len(pairs); i += 2 {
		if err := telemetryCmd.Flags().Set(pairs[i], pairs[i+1]); err != nil {
			t.Fatalf("set --%s: %v", pairs[i], err)
		}
	}
}

func runCompareJSON(t *testing.T, pairs ...string) compareReportJSON {
	t.Helper()
	enableTelemetryJSON(t)
	setCompareFlags(t, append([]string{
		"formula", compareTestFormula,
		"surface", compareSurfaceB,
		"before", compareIDs("b", 5),
		"after", compareIDs("a", 5),
	}, pairs...)...)

	out, err := runTelemetryJSON(t, "compare")
	if err != nil {
		t.Fatalf("compare --json: %v", err)
	}
	var dto compareReportJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	return dto
}

func compareCheck(t *testing.T, dto compareReportJSON, name string) compareCheckJSON {
	t.Helper()
	for _, c := range dto.Checks {
		if c.Name == name {
			return c
		}
	}
	t.Fatalf("no check named %q; the checks are the audit trail and a missing one cannot be "+
		"distinguished from a passing one. Got %+v", name, dto.Checks)
	return compareCheckJSON{}
}

func TestCompareGoldenMatrix(t *testing.T) {
	// The bar: median(after) = 80_000 sits below min(before) = 100_000.
	before := []int64{100_000, 110_000, 120_000, 130_000, 140_000}
	after := []int64{60_000, 70_000, 80_000, 90_000, 95_000}

	t.Run("every per-run column the protocol names is rendered", func(t *testing.T) {
		seedCompareArms(t, before, after)
		dto := runCompareJSON(t)

		if len(dto.Runs) != 10 {
			t.Fatalf("len(runs) = %d, want 10", len(dto.Runs))
		}
		// Asserted as a KEY SET on the marshalled row rather than through the struct, because the
		// json tags are the contract a consumer joins on and a renamed field is invisible to a
		// struct-level assertion.
		row, err := json.Marshal(dto.Runs[0])
		if err != nil {
			t.Fatalf("marshal row: %v", err)
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(row, &keys); err != nil {
			t.Fatalf("unmarshal row: %v", err)
		}
		for _, name := range []string{
			"instance_id", "arm", "started", "model", "af_commit", "host_version",
			"checkout_commit", "base_commit", "formula_digest_start", "formula_digest_end",
			"tokenomics_state", "input_digest", "completed", "steps_closed", "measured_steps",
			"interrupted", "sessions", "effort_levels", "out_tokens", "subagent_tokens", "metric",
			"think_tokens", "think_tokens_est", "subagent_launches", "workflow_launches",
			"nested_launches", "repeat_reads", "in_tokens", "cache_read_tokens",
			"cache_creation_tokens", "gate_flags", "trusted_keys", "learned_min_runs",
			"excluded_reason",
		} {
			if _, ok := keys[name]; !ok {
				t.Errorf("the matrix has no %q column; it is a per-run figure the protocol names",
					name)
			}
		}
	})

	t.Run("the verdict is the pure comparison and its two figures", func(t *testing.T) {
		seedCompareArms(t, before, after)
		dto := runCompareJSON(t)

		if dto.Verdict.Verdict != tokenomics.ProtocolPass {
			t.Errorf("verdict = %q, want %q; void_because = %v",
				dto.Verdict.Verdict, tokenomics.ProtocolPass, dto.Verdict.VoidBecause)
		}
		if dto.Verdict.MedianAfter != 80_000 || dto.Verdict.MinBefore != 100_000 {
			t.Errorf("median(after) = %d, min(before) = %d, want 80000 and 100000",
				dto.Verdict.MedianAfter, dto.Verdict.MinBefore)
		}
		// A pass with no error rate beside it invites the reader to treat it as a discovery.
		if dto.Verdict.BeforeSpreadPct != 140 {
			t.Errorf("before spread = %d%%, want 140", dto.Verdict.BeforeSpreadPct)
		}
		if dto.Verdict.RequiredReductionPct != 16 {
			t.Errorf("required reduction = %d%%, want 16", dto.Verdict.RequiredReductionPct)
		}
		if dto.Verdict.NullFalsePassFavourable != 21 || dto.Verdict.NullFalsePassTotal != 252 {
			t.Errorf("null false-pass odds = %d/%d, want 21/252",
				dto.Verdict.NullFalsePassFavourable, dto.Verdict.NullFalsePassTotal)
		}
		if dto.State != telemetryStateOK {
			t.Errorf("state = %q, want %q", dto.State, telemetryStateOK)
		}
	})

	t.Run("an after arm that did not beat the floor fails rather than voids", func(t *testing.T) {
		seedCompareArms(t, before, []int64{100_000, 110_000, 120_000, 130_000, 140_000})
		dto := runCompareJSON(t)
		if dto.Verdict.Verdict != tokenomics.ProtocolFail {
			t.Errorf("verdict = %q, want %q; void_because = %v",
				dto.Verdict.Verdict, tokenomics.ProtocolFail, dto.Verdict.VoidBecause)
		}
	})

	t.Run("the thinking figures never reach the metric", func(t *testing.T) {
		seedCompareArms(t, before, after)
		dto := runCompareJSON(t)
		for _, run := range dto.Runs {
			if run.ThinkTokens == nil || *run.ThinkTokens == 0 {
				t.Fatalf("%s recorded no thinking, so this assertion proves nothing", run.InstanceID)
			}
			if run.Metric != deref64(run.OutTokens)+deref64(run.SubagentTokens) {
				t.Errorf("%s metric = %d, want out %d + subagent %d", run.InstanceID, run.Metric,
					deref64(run.OutTokens), deref64(run.SubagentTokens))
			}
		}
	})

	t.Run("the human rendering carries the verdict and its bar", func(t *testing.T) {
		seedCompareArms(t, before, after)
		resetReportFlags(t)
		setCompareFlags(t,
			"formula", compareTestFormula, "surface", compareSurfaceB,
			"before", compareIDs("b", 5), "after", compareIDs("a", 5))

		var err error
		out := captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"compare"}) })
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		for _, needle := range []string{
			tokenomics.ProtocolPass, "median(after)", "min(before)", "spread", "false-pass",
			"af-b0", "af-a0", "METRIC",
		} {
			if !strings.Contains(out, needle) {
				t.Errorf("the human rendering has no %q in:\n%s", needle, out)
			}
		}
	})
}

func TestCompareExcludesUnmeasuredDelegation(t *testing.T) {
	// C-9. The run delegated and nobody counted what the delegation spent, so its metric
	// undercounts by an unknown amount — and it is the CHEAPEST run in the before arm, which is
	// exactly where an undercount does the most damage: it lowers the floor the after arm has to
	// beat, biasing the comparison toward a pass.
	root := seedCompareArms(t,
		[]int64{100_000, 110_000, 120_000, 130_000, 140_000},
		[]int64{60_000, 70_000, 80_000, 90_000, 95_000})

	// Written record by record rather than through seedCompareRun, because the pathology IS the
	// absence: seedCompareRun records subagent_tokens on every step, and a run that recorded the
	// figure — even as a zero — is a run somebody counted.
	dir := config.TelemetryDir(root)
	base := telemetry.StepEvent{
		V: telemetry.SchemaVersion, Agent: compareTestAgent, Formula: compareTestFormula,
		InstanceID: "af-b9", Model: "fable-5",
	}
	start := base
	start.Event, start.TS, start.Verb = telemetry.EventInstanceStart, "2026-09-09T10:00:00.000Z", "sling"
	start.AFCommit, start.CheckoutCommit = "commit-af", "commit-tree"
	start.SlingDigest, start.TokenomicsState = "digest-in", telemetry.TokenomicsStateOff
	start.FormulaDigest = "digest-f"
	appendCompareEvent(t, dir, start)

	step := base
	step.Event, step.TS, step.Verb = telemetry.EventStepEnd, "2026-09-09T10:09:00.000Z", "done"
	step.StepID, step.StepLabel, step.StepSeq = "bd-9", "step-9", 1
	step.SessionID, step.Status, step.HostVersion = "sess-a", telemetry.StatusClosed, "2.1.0"
	step.OutTokens, step.SubagentLaunches = i64p(10_000), i64p(3)
	appendCompareEvent(t, dir, step)

	end := base
	end.Event, end.TS, end.Verb = telemetry.EventInstanceEnd, "2026-09-09T10:30:00.000Z", "done"
	end.FormulaDigest, end.BaseCommit = "digest-f", "commit-tree"
	appendCompareEvent(t, dir, end)

	dto := runCompareJSON(t, "before", "af-b9,af-b0,af-b1,af-b2,af-b3")

	if len(dto.Excluded) != 1 || dto.Excluded[0].InstanceID != "af-b9" {
		t.Fatalf("excluded = %+v, want exactly af-b9", dto.Excluded)
	}
	if dto.Excluded[0].ExcludedReason != telemetry.ExcludedUnmeasuredDelegation {
		t.Errorf("excluded_reason = %q, want %q — an arm silently short by one run cannot be audited",
			dto.Excluded[0].ExcludedReason, telemetry.ExcludedUnmeasuredDelegation)
	}
	for _, run := range dto.Runs {
		if run.InstanceID == "af-b9" {
			t.Error("the excluded run was still counted into its arm")
		}
	}
	// Four runs is not five, so the arm is under-sized and the comparison is void — not a fail.
	if dto.Verdict.Verdict != tokenomics.ProtocolVoid {
		t.Errorf("verdict = %q, want %q after an exclusion left the arm short",
			dto.Verdict.Verdict, tokenomics.ProtocolVoid)
	}
	if c := compareCheck(t, dto, compareCheckArmSize); c.Passed {
		t.Error("the arm-size check passed on a four-run arm")
	}
}

func TestCompareVoidsOnDigestDrift(t *testing.T) {
	before := []int64{100_000, 110_000, 120_000, 130_000, 140_000}
	after := []int64{60_000, 70_000, 80_000, 90_000, 95_000}

	t.Run("surface b requires one formula digest across all ten runs", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedTelemetryGate(t, root)
		dir := config.TelemetryDir(root)
		for i, m := range before {
			seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-b%d", i), metric: m,
				digest: "digest-f", state: telemetry.TokenomicsStateOff})
		}
		for i, m := range after {
			digest := "digest-f"
			if i == 2 {
				// One run of a formula that was edited mid-experiment. Its cheaper metric is a
				// different formula's cost, and averaging it in compares two things.
				digest = "digest-EDITED"
			}
			seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-a%d", i), metric: m,
				digest: digest, state: telemetry.TokenomicsStateOn})
		}

		dto := runCompareJSON(t)
		if dto.Verdict.Verdict != tokenomics.ProtocolVoid {
			t.Errorf("verdict = %q, want %q", dto.Verdict.Verdict, tokenomics.ProtocolVoid)
		}
		c := compareCheck(t, dto, compareCheckFormulaDigest)
		if c.Passed {
			t.Error("the digest check passed across two different formulas")
		}
		if !strings.Contains(strings.Join(dto.Verdict.VoidBecause, " "), compareCheckFormulaDigest) {
			t.Errorf("void_because = %v, want it to name %q — a void with no named check is an "+
				"unfalsifiable refusal", dto.Verdict.VoidBecause, compareCheckFormulaDigest)
		}
	})

	t.Run("surface a requires one digest per arm and two across arms", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedTelemetryGate(t, root)
		dir := config.TelemetryDir(root)
		for i, m := range before {
			seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-b%d", i), metric: m,
				digest: "digest-old", state: telemetry.TokenomicsStateOff})
		}
		for i, m := range after {
			seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-a%d", i), metric: m,
				digest: "digest-new", state: telemetry.TokenomicsStateOff})
		}

		if dto := runCompareJSON(t, "surface", compareSurfaceA); !compareCheck(t, dto, compareCheckFormulaDigest).Passed {
			t.Errorf("surface a rejected two distinct per-arm digests, which is the shape it "+
				"exists to measure: %+v", compareCheck(t, dto, compareCheckFormulaDigest))
		}

		// Same digest on both arms means the formula never changed, so surface a measured nothing.
		root = seedCompareArms(t, before, after)
		_ = root
		if dto := runCompareJSON(t, "surface", compareSurfaceA); compareCheck(t, dto, compareCheckFormulaDigest).Passed {
			t.Error("surface a accepted one digest across both arms; the formula never changed, so " +
				"there was no treatment to measure")
		}
	})

	t.Run("a crashed run voids without anyone reading the formula", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedTelemetryGate(t, root)
		dir := config.TelemetryDir(root)
		for i, m := range before {
			seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-b%d", i), metric: m,
				digest: "digest-f", state: telemetry.TokenomicsStateOff})
		}
		for i, m := range after {
			seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-a%d", i), metric: m,
				digest: "digest-f", state: telemetry.TokenomicsStateOn})
		}
		// A run that opened one step, closed it, and never wrote instance_end. Its metric is the
		// cheapest in the arm precisely because it stopped early.
		appendCompareEvent(t, dir, telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventInstanceStart,
			TS: "2026-09-09T11:00:00.000Z", Agent: compareTestAgent, Formula: compareTestFormula,
			InstanceID: "af-b9", Model: "fable-5", AFCommit: "commit-af",
			CheckoutCommit: "commit-tree", SlingDigest: "digest-in", FormulaDigest: "digest-f",
			TokenomicsState: telemetry.TokenomicsStateOff,
		})
		appendCompareEvent(t, dir, telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd, TS: "2026-09-09T11:01:00.000Z",
			Agent: compareTestAgent, Formula: compareTestFormula, InstanceID: "af-b9",
			Model: "fable-5", StepID: "bd-0", StepLabel: "step-0", Status: telemetry.StatusClosed,
			HostVersion: "2.1.0", OutTokens: i64p(5_000), SubagentTokens: i64p(0),
			SubagentLaunches: i64p(0), WorkflowLaunches: i64p(0), SubagentNestedLaunches: i64p(0),
		})

		dto := runCompareJSON(t, "before", "af-b9,af-b0,af-b1,af-b2,af-b3")
		if dto.Verdict.Verdict != tokenomics.ProtocolVoid {
			t.Errorf("verdict = %q, want %q — an unfinished before-run makes min(before) unbeatable",
				dto.Verdict.Verdict, tokenomics.ProtocolVoid)
		}
		if c := compareCheck(t, dto, compareCheckRunCompletion); c.Passed {
			t.Error("the completion check passed on a run with no instance_end record")
		}
		if c := compareCheck(t, dto, compareCheckStepsClosedConstant); c.Passed {
			t.Error("the steps-closed constancy check passed across a one-step and a two-step run")
		}
	})

	t.Run("every arm-constancy check names itself", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			check  string
			mutate func(ev *telemetry.StepEvent)
		}{
			{"af_commit", compareCheckAFCommit, func(ev *telemetry.StepEvent) {
				if ev.Event == telemetry.EventInstanceStart {
					ev.AFCommit = "commit-other"
				}
			}},
			{"checkout_commit", compareCheckCheckoutCommit, func(ev *telemetry.StepEvent) {
				if ev.Event == telemetry.EventInstanceStart {
					ev.CheckoutCommit = "commit-other"
				}
			}},
			{"model", compareCheckModel, func(ev *telemetry.StepEvent) { ev.Model = "sonnet" }},
			{"host_version", compareCheckHostVersion, func(ev *telemetry.StepEvent) {
				if ev.Event == telemetry.EventStepEnd {
					ev.HostVersion = "9.9.9"
				}
			}},
			{"input_digest", compareCheckInputDigest, func(ev *telemetry.StepEvent) {
				if ev.Event == telemetry.EventInstanceStart {
					ev.SlingDigest = "digest-other"
				}
			}},
			{"base_commit", compareCheckBaseCommit, func(ev *telemetry.StepEvent) {
				if ev.Event == telemetry.EventInstanceEnd {
					ev.BaseCommit = "commit-drifted"
				}
			}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root := setupTestFactoryForPrime(t)
				t.Chdir(root)
				seedTelemetryGate(t, root)
				dir := config.TelemetryDir(root)
				for i, m := range before {
					seedCompareRunMutated(t, dir, compareRun{id: fmt.Sprintf("af-b%d", i), metric: m,
						digest: "digest-f", state: telemetry.TokenomicsStateOff}, i == 3, tc.mutate)
				}
				for i, m := range after {
					seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-a%d", i), metric: m,
						digest: "digest-f", state: telemetry.TokenomicsStateOn})
				}

				dto := runCompareJSON(t)
				if dto.Verdict.Verdict != tokenomics.ProtocolVoid {
					t.Errorf("verdict = %q, want %q", dto.Verdict.Verdict, tokenomics.ProtocolVoid)
				}
				if c := compareCheck(t, dto, tc.check); c.Passed {
					t.Errorf("the %s check passed on a drifted arm", tc.check)
				}
			})
		}
	})

	t.Run("the attested input digest is verified when the operator supplies one", func(t *testing.T) {
		seedCompareArms(t, before, after)
		if dto := runCompareJSON(t, "verify-input-digest", "digest-in"); !compareCheck(t, dto, compareCheckInputDigestVerified).Passed {
			t.Error("the matching attestation was rejected")
		}
		dto := runCompareJSON(t, "verify-input-digest", "digest-somethingelse")
		if c := compareCheck(t, dto, compareCheckInputDigestVerified); c.Passed {
			t.Error("a mismatched attestation passed; a digest of the inputs says nothing if it is " +
				"not the digest the runs were slung with")
		}
		if dto.Verdict.Verdict != tokenomics.ProtocolVoid {
			t.Errorf("verdict = %q, want %q", dto.Verdict.Verdict, tokenomics.ProtocolVoid)
		}
	})
}

// seedCompareRunMutated writes one run, optionally passing every record through a mutation first.
func seedCompareRunMutated(t *testing.T, dir string, r compareRun, mutate bool, f func(*telemetry.StepEvent)) {
	t.Helper()
	if !mutate {
		seedCompareRun(t, dir, r)
		return
	}
	compareMutation = f
	defer func() { compareMutation = nil }()
	seedCompareRun(t, dir, r)
}

// compareMutation is the test seam seedCompareRunMutated drives. It is a package-level variable in
// a _test.go file, so it exists only in the test binary.
var compareMutation func(*telemetry.StepEvent)

func TestCompareOutcomeAudit(t *testing.T) {
	before := []int64{100_000, 110_000, 120_000, 130_000, 140_000}
	after := []int64{60_000, 70_000, 80_000, 90_000, 95_000}

	t.Run("the audit reports each arm's outcomes beside the verdict", func(t *testing.T) {
		seedCompareArms(t, before, after)
		dto := runCompareJSON(t,
			"fidelity", "af-b0=9/1,af-a0=8/2")

		if len(dto.Audit.Arms) != 2 {
			t.Fatalf("audit arms = %d, want 2: %+v", len(dto.Audit.Arms), dto.Audit.Arms)
		}
		for _, arm := range dto.Audit.Arms {
			if arm.Runs != 5 {
				t.Errorf("%s arm covers %d runs, want 5", arm.Arm, arm.Runs)
			}
			if arm.StatusClosed != 10 {
				t.Errorf("%s arm closed %d steps, want 10 — a cheaper arm that also stopped "+
					"finishing steps is not a cheaper arm", arm.Arm, arm.StatusClosed)
			}
			if arm.Interrupted != 0 {
				t.Errorf("%s arm reports %d interrupted", arm.Arm, arm.Interrupted)
			}
			if arm.GateFlags != 0 {
				t.Errorf("%s arm reports %d gate flags", arm.Arm, arm.GateFlags)
			}
		}
		if dto.Audit.Arms[0].FidelityVerified != 9 || dto.Audit.Arms[0].FidelityInaccurate != 1 {
			t.Errorf("before fidelity = %d/%d, want 9/1", dto.Audit.Arms[0].FidelityVerified,
				dto.Audit.Arms[0].FidelityInaccurate)
		}
		if dto.Audit.Arms[1].FidelityVerified != 8 || dto.Audit.Arms[1].FidelityInaccurate != 2 {
			t.Errorf("after fidelity = %d/%d, want 8/2", dto.Audit.Arms[1].FidelityVerified,
				dto.Audit.Arms[1].FidelityInaccurate)
		}
	})

	t.Run("a gate-waiting close and an interrupted step are visible", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedTelemetryGate(t, root)
		dir := config.TelemetryDir(root)
		for i, m := range before {
			r := compareRun{id: fmt.Sprintf("af-b%d", i), metric: m,
				digest: "digest-f", state: telemetry.TokenomicsStateOff}
			if i == 0 {
				r.statuses = []string{telemetry.StatusClosed, telemetry.StatusGateWaiting}
			}
			seedCompareRun(t, dir, r)
		}
		for i, m := range after {
			seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-a%d", i), metric: m,
				digest: "digest-f", state: telemetry.TokenomicsStateOn})
		}
		appendCompareEvent(t, dir, telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: "2026-09-09T10:20:00.000Z", Agent: compareTestAgent, Formula: compareTestFormula,
			InstanceID: "af-a0", Model: "fable-5", StepID: "bd-99", StepLabel: "step-99",
		})

		dto := runCompareJSON(t)
		if dto.Audit.Arms[0].StatusGateWaiting != 1 {
			t.Errorf("before arm gate-waiting closes = %d, want 1", dto.Audit.Arms[0].StatusGateWaiting)
		}
		if dto.Audit.Arms[1].Interrupted != 1 {
			t.Errorf("after arm interrupted = %d, want 1 — a step opened and never closed",
				dto.Audit.Arms[1].Interrupted)
		}
	})

	t.Run("interventions are grouped by arm, mechanism and objective", func(t *testing.T) {
		root := seedCompareArms(t, before, after)
		dir := config.TelemetryDir(root)
		appendCompareEvent(t, dir, telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
			TS: "2026-09-09T10:04:00.000Z", Agent: compareTestAgent, Formula: compareTestFormula,
			InstanceID: "af-a0", Mechanism: string(tokenomics.MechanismEffort),
			Action: telemetry.ActionReduceEffort, Objective: telemetry.ObjectiveEfficiency,
		})

		dto := runCompareJSON(t)
		var found bool
		for _, row := range dto.Audit.Interventions {
			if row.Arm == compareArmAfter && row.Objective == telemetry.ObjectiveEfficiency {
				found = true
				if row.Count != 1 {
					t.Errorf("efficiency firings = %d, want 1", row.Count)
				}
			}
		}
		if !found {
			t.Errorf("no efficiency intervention in the audit: %+v — without the objective leg the "+
				"two arms of the experiment are one series", dto.Audit.Interventions)
		}
	})

	t.Run("a partial read is degraded rather than silently short", func(t *testing.T) {
		root := seedCompareArms(t, before, after)
		appendMalformedTelemetryLine(t, root, compareTestAgent)

		dto := runCompareJSON(t)
		if dto.State != telemetryStateDegraded {
			t.Errorf("state = %q, want %q; a compare that silently dropped unreadable lines could "+
				"report a pass computed over a subset", dto.State, telemetryStateDegraded)
		}
		if dto.Stats.Malformed == 0 {
			t.Error("stats.malformed = 0 beside a malformed line")
		}
	})

	t.Run("the human audit renders beside the matrix", func(t *testing.T) {
		seedCompareArms(t, before, after)
		resetReportFlags(t)
		setCompareFlags(t,
			"formula", compareTestFormula, "surface", compareSurfaceB,
			"before", compareIDs("b", 5), "after", compareIDs("a", 5),
			"fidelity", "af-b0=9/1")

		var err error
		out := captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"compare"}) })
		if err != nil {
			t.Fatalf("compare: %v", err)
		}
		for _, needle := range []string{"outcome audit", "gate-waiting", "interrupted", "fidelity"} {
			if !strings.Contains(out, needle) {
				t.Errorf("the human audit has no %q in:\n%s", needle, out)
			}
		}
	})
}

func TestCompareAlwaysExitsZeroOnTheJSONSurface(t *testing.T) {
	// The report family's contract: a consumer branches on .state and never on the exit code.
	seedCompareArms(t, []int64{1, 2, 3, 4, 5}, []int64{1, 2, 3, 4, 5})
	enableTelemetryJSON(t)
	setCompareFlags(t, "formula", "", "surface", "zzz", "before", "", "after", "")

	out, err := runTelemetryJSON(t, "compare")
	if err != nil {
		t.Fatalf("compare --json returned %v; the machine surface exits 0 and reports as data", err)
	}
	var envelope struct {
		V     int    `json:"v"`
		State string `json:"state"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &envelope); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if envelope.State == telemetryStateOK {
		t.Errorf("state = %q for an unusable invocation, want an error state", envelope.State)
	}
	if envelope.V != telemetry.SchemaVersion {
		t.Errorf("v = %d, want %d on every payload a consumer can receive", envelope.V,
			telemetry.SchemaVersion)
	}
}

// TestCompareRejectsAnUnsoundNomination covers the ways a comparison can be assembled out of
// evidence that does not support it while every individual run is perfectly well-formed. Each one
// produced a silent `pass` before it was checked — the single direction a measurement protocol must
// never be biased in, and the one a reader of the report cannot detect from the outside.
func TestCompareRejectsAnUnsoundNomination(t *testing.T) {
	before := []int64{100_000, 110_000, 120_000, 130_000, 140_000}
	after := []int64{60_000, 70_000, 80_000, 90_000, 95_000}

	t.Run("one run nominated five times is not a five-run arm", func(t *testing.T) {
		seedCompareArms(t, before, after)

		dto := runCompareJSON(t,
			"before", "af-b0,af-b0,af-b0,af-b0,af-b0",
			"after", "af-a0,af-a0,af-a0,af-a0,af-a0")

		// Undeduped, every figure here would be a lie that agrees with itself: five identical rows
		// per arm, every constancy check comparing a one-element set, and the 21-in-252 null
		// false-pass rate — a statement about ten INDEPENDENT runs — printed over two.
		if len(dto.Runs) != 2 {
			t.Errorf("runs = %d rows, want 2 — one per DISTINCT nominated id", len(dto.Runs))
		}
		distinct := compareCheck(t, dto, compareCheckDistinctRuns)
		if !distinct.Applies || distinct.Passed {
			t.Errorf("%s = %+v over a nomination that repeated one id five times",
				compareCheckDistinctRuns, distinct)
		}
		if size := compareCheck(t, dto, compareCheckArmSize); size.Passed {
			t.Error("the arm-size check passed on a one-run arm")
		}
		if dto.Verdict.Verdict != tokenomics.ProtocolVoid {
			t.Errorf("verdict = %q over two runs nominated ten times, want %q",
				dto.Verdict.Verdict, tokenomics.ProtocolVoid)
		}
	})

	t.Run("an id nominated in both arms is counted in the first and named", func(t *testing.T) {
		seedCompareArms(t, before, after)

		dto := runCompareJSON(t, "after", "af-b0,af-a1,af-a2,af-a3,af-a4")

		distinct := compareCheck(t, dto, compareCheckDistinctRuns)
		if distinct.Passed {
			t.Error("af-b0 was nominated in both arms and the distinct-runs check passed")
		}
		if !strings.Contains(distinct.Detail, "af-b0") {
			t.Errorf("the failure does not name the repeated run: %q", distinct.Detail)
		}
		for _, row := range dto.Runs {
			if row.InstanceID == "af-b0" && row.Arm != compareArmBefore {
				t.Errorf("af-b0 counted in the %s arm; the first nomination holds it", row.Arm)
			}
		}
	})

	t.Run("a constancy check over a field no run recorded does not apply", func(t *testing.T) {
		// af_commit is an ldflags stamp and input_digest rides an OPTIONAL --input-digest, so ten
		// empty strings is the NORMAL shape rather than a corner of it. A set comparison sees one
		// value and reports "constant" — the report vouching for something nobody recorded, and for
		// base_commit, vouching that a rebase nobody recorded landed on a pin nobody recorded.
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedTelemetryGate(t, root)
		dir := config.TelemetryDir(root)

		blank := func(ev *telemetry.StepEvent) {
			ev.AFCommit, ev.CheckoutCommit, ev.BaseCommit = "", "", ""
			ev.SlingDigest, ev.HostVersion = "", ""
		}
		for i := range before {
			seedCompareRunMutated(t, dir, compareRun{id: fmt.Sprintf("af-b%d", i), metric: before[i],
				digest: "digest-f", state: telemetry.TokenomicsStateOff}, true, blank)
			seedCompareRunMutated(t, dir, compareRun{id: fmt.Sprintf("af-a%d", i), metric: after[i],
				digest: "digest-f", state: telemetry.TokenomicsStateOn}, true, blank)
		}

		dto := runCompareJSON(t)
		for _, name := range []string{
			compareCheckAFCommit, compareCheckHostVersion, compareCheckCheckoutCommit,
			compareCheckInputDigest, compareCheckBaseCommit,
		} {
			check := compareCheck(t, dto, name)
			if check.Applies {
				t.Errorf("%s applies over a field no run recorded; ten empty strings are one value, "+
					"so a set comparison calls an unstamped arm constant", name)
			}
			if check.Detail == "" {
				t.Errorf("%s does not apply and says nothing about why", name)
			}
		}
		// It does NOT void. An unasserted check is not a failed one, and a comparison of ten
		// otherwise-sound runs is still a comparison — the report simply declines to vouch for the
		// provenance nobody stamped.
		if dto.Verdict.Verdict != tokenomics.ProtocolPass {
			t.Errorf("verdict = %q (void because %v); an unasserted provenance check must decline to "+
				"vouch, not void a sound comparison", dto.Verdict.Verdict, dto.Verdict.VoidBecause)
		}
	})

	t.Run("a field some runs recorded and others did not is a mismatch", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedTelemetryGate(t, root)
		dir := config.TelemetryDir(root)
		for i := range before {
			r := compareRun{id: fmt.Sprintf("af-b%d", i), metric: before[i],
				digest: "digest-f", state: telemetry.TokenomicsStateOff}
			seedCompareRunMutated(t, dir, r, i == 0, func(ev *telemetry.StepEvent) { ev.AFCommit = "" })
			seedCompareRun(t, dir, compareRun{id: fmt.Sprintf("af-a%d", i), metric: after[i],
				digest: "digest-f", state: telemetry.TokenomicsStateOn})
		}

		// The absence rule is per-check, not per-run: four runs stamped a commit and one stamped
		// nothing, so the empty string is one of the differing values rather than an absence, and
		// that run was not measured against the same binary as its arm.
		check := compareCheck(t, runCompareJSON(t), compareCheckAFCommit)
		if !check.Applies || check.Passed {
			t.Errorf("%s = %+v; one run of five stamped no commit, which is a mismatch and not an "+
				"absence", compareCheckAFCommit, check)
		}
	})

	t.Run("runs of another formula do not answer for the one named", func(t *testing.T) {
		// --formula is required, is echoed into the payload, and decides which learned digest
		// trusted_keys is read from. Runs of a different formula make all three statements false at
		// once while every other check passes, because those runs are perfectly self-consistent.
		seedCompareArms(t, before, after)

		dto := runCompareJSON(t, "formula", "some-other-formula")

		check := compareCheck(t, dto, compareCheckFormula)
		if !check.Applies || check.Passed {
			t.Errorf("%s = %+v over runs of a different formula", compareCheckFormula, check)
		}
		if !strings.Contains(check.Detail, compareTestFormula) {
			t.Errorf("the failure does not name the formula the evidence is actually of: %q", check.Detail)
		}
		if dto.Verdict.Verdict != tokenomics.ProtocolVoid {
			t.Errorf("verdict = %q over evidence from another formula, want %q",
				dto.Verdict.Verdict, tokenomics.ProtocolVoid)
		}
	})

	t.Run("an attestation that reaches no counted run is reported, not dropped", func(t *testing.T) {
		seedCompareArms(t, before, after)

		dto := runCompareJSON(t, "fidelity", "af-b0=9/1,af-typo=7/3")

		if len(dto.Audit.FidelityUnattributed) != 1 ||
			!strings.Contains(dto.Audit.FidelityUnattributed[0], "af-typo") {
			t.Errorf("fidelity_unattributed = %v, want the unmatched id named; a typo'd id that "+
				"vanished would read as an arm nobody attested", dto.Audit.FidelityUnattributed)
		}
		if got := dto.Audit.Arms[0].FidelityRuns; got != 1 {
			t.Errorf("before arm attributed %d attestations, want 1 — the typo must not be counted", got)
		}
		if got := dto.Audit.Arms[1].FidelityRuns; got != 0 {
			t.Errorf("after arm attributed %d attestations, want 0", got)
		}
	})
}
