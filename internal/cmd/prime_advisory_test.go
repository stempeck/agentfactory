package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// These tests do not run in parallel, for tokenomics_admission_test.go:20-21's reason.

// armAdvisoryPolicy is armTokenomics with the per-mechanism keys stated. armTokenomics fixes every
// mechanism at its default, which is what K7's tests want and what K9's cannot use: the whole point
// of an advisory test is the difference between a mechanism switched on and the same mechanism
// switched off, and a fixture that cannot express "off" can only ever assert the on half.
func armAdvisoryPolicy(t *testing.T, root string, marginPct, minRuns int, mechanisms map[string]string) {
	t.Helper()
	if err := os.WriteFile(tokenomicsGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write tokenomics gate: %v", err)
	}
	block := map[string]any{
		"enabled":              "on",
		"budget":               "on",
		"admission_margin_pct": marginPct,
		"learned_min_runs":     minRuns,
	}
	for k, v := range mechanisms {
		block[k] = v
	}
	body, err := json.Marshal(map[string]any{"tokenomics": block})
	if err != nil {
		t.Fatalf("marshal startup.json: %v", err)
	}
	if err := os.WriteFile(config.StartupConfigPath(root), body, 0o644); err != nil {
		t.Fatalf("write startup.json: %v", err)
	}
	// Through the real loader, so a fixture the production path would reject fails here rather
	// than reappearing as an unexplained inert mechanism three assertions later.
	if _, err := config.LoadStartupConfig(root); err != nil {
		t.Fatalf("the fixture's startup.json does not load: %v", err)
	}
}

// advisoryFixture is the arithmetic every subtest below reasons about, stated once.
//
// plantSessionSnapshot writes context_tokens_used = pct*2000 against a 200,000-token total, so at
// 60% the session carries 120,000 and 80,000 are free, and at 92% it carries 184,000 with 16,000
// left. The two capacity bands are reached from those two occupancies: dispatch by a learned
// appetite that fits the 60% session but only just, and thrift by an occupancy at or past the 90%
// ceiling the 10% margin fixes. A 150,000-token appetite against the 60% session fits nothing and is
// what the no-fit rows use.
//
// There is deliberately no effort fixture here any more. #678 K5 deleted that band: an effort level
// is now chosen from a step's learned generation baseline at the launch legs, so it is not counsel
// this surface can give and TestEffortSelectedAtLaunchLegs owns it instead.
const (
	advisoryOccupancyPct  = 60.0
	advisoryThriftPct     = 92.0
	advisoryWindowTokens  = 200000
	advisoryFreeTokens    = 80000
	advisoryThriftFree    = 16000
	advisoryNoFitPeak     = 150000
	advisoryDispatchPeak  = 50000
	advisoryPriorRuns     = 3
	advisoryMarginPct     = 10
	advisoryMinRuns       = 1
	advisoryProjectedPctD = float64(advisoryWindowTokens-advisoryFreeTokens+advisoryDispatchPeak) /
		float64(advisoryWindowTokens) * 100
	// No appetite is seeded for the thrift fixture, so the projection is the occupancy itself: the
	// thrift band reads occupancy alone and must fire in a factory that has learned nothing.
	advisoryProjectedPctT = advisoryThriftPct
)

// wantAdvisory renders the counsel the registry owns, rather than re-typing it. A test that spelled
// the template out would pass against a shipped template that had drifted from the one it asserts.
func wantAdvisory(t *testing.T, m tokenomics.Mechanism, appetite int64, projectedPct float64) string {
	t.Helper()
	text, ok := tokenomics.RenderAdvisory(m, tokenomics.AdvisoryInputs{
		WindowTokens:   advisoryWindowTokens,
		FreeTokens:     advisoryFreeTokens,
		AppetiteTokens: appetite,
		ProjectedPct:   projectedPct,
		PriorRuns:      advisoryPriorRuns,
	})
	if !ok {
		t.Fatalf("no advisory template for mechanism %q; the fixture is asserting against nothing", m)
	}
	return text
}

// wantThriftAdvisory is wantAdvisory for the 92% fixture, whose free-token and appetite operands
// differ from the 60% one. Spelled separately rather than by widening wantAdvisory with two more
// parameters: every existing call site would then have to restate operands its band does not read.
func wantThriftAdvisory(t *testing.T) string {
	t.Helper()
	text, ok := tokenomics.RenderAdvisory(tokenomics.MechanismThrift, tokenomics.AdvisoryInputs{
		WindowTokens: advisoryWindowTokens,
		FreeTokens:   advisoryThriftFree,
		ProjectedPct: advisoryProjectedPctT,
	})
	if !ok {
		t.Fatal("no thrift template; the fixture is asserting against nothing")
	}
	return text
}

// interventionsByMechanism returns the intervention records grouped by the mechanism that wrote
// them. Grouping rather than counting, because "one firing leaves one record" is a claim about each
// mechanism separately once more than one of them can fire on the same verb.
func interventionsByMechanism(t *testing.T, root, agent string) map[string][]telemetry.StepEvent {
	t.Helper()
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	byMechanism := map[string][]telemetry.StepEvent{}
	for _, r := range records {
		if r.Event == telemetry.EventIntervention {
			byMechanism[r.Mechanism] = append(byMechanism[r.Mechanism], r)
		}
	}
	return byMechanism
}

// TestAdvisoryEmission is #668 K9: the advisory half of the mechanism surface. Templates, budget
// and renderer all shipped in Phase 2 with no production caller; this is the caller.
func TestAdvisoryEmission(t *testing.T) {
	// The control comes FIRST and it is the hardest requirement in ux.md:76 — "toggle off restores
	// today's output byte-for-byte". Everything below it asserts that an advisory appeared; without
	// this, a mechanism wired to emit unconditionally would satisfy every one of them.
	t.Run("a mechanism switched off restores the prime output byte-for-byte", func(t *testing.T) {
		fx, _, step := primedFixture(t, advisoryOccupancyPct)
		gateOn(t, fx.root)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, advisoryDispatchPeak, advisoryPriorRuns)

		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{
			"thrift": "off", "dispatch": "off",
		})
		// The FIRST prime of a fixture prints a different tail from every prime after it: it writes
		// the checkpoint the next one reports, and it opens the step the next one RESUMES, which is
		// what K16's slimming keys on. Both readings below are therefore taken from the steady state,
		// or this would be comparing the checkpoint block and reporting it as the advisory.
		runPrimeCapturing(t)
		silent := runPrimeCapturing(t)

		// Counted while the mechanism is still off, because the log has no per-run discriminator: a
		// count taken after the armed run below would include that run's own record and could never
		// be zero. A mechanism that recorded while switched off would leave the audit trail claiming
		// a firing the byte-identity check above proves never reached the agent.
		if by := interventionsByMechanism(t, fx.root, fx.agent); len(by[string(tokenomics.MechanismDispatch)]) != 0 {
			t.Errorf("the dispatch mechanism is off and recorded %d interventions across two primes: %v",
				len(by[string(tokenomics.MechanismDispatch)]), by)
		}

		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{
			"thrift": "off", "dispatch": "on",
		})
		counselled := runPrimeCapturing(t)

		if silent == counselled {
			t.Fatal("switching the dispatch mechanism on changed nothing; the control proves nothing " +
				"and neither does any subtest below it")
		}
		block := advisorySection(wantAdvisory(t, tokenomics.MechanismDispatch, advisoryDispatchPeak, advisoryProjectedPctD))
		if got := strings.Replace(counselled, block, "", 1); got != silent {
			t.Errorf("removing the advisory block does not restore the silent output.\n"+
				"with advisory (%d bytes):\n%s\nwithout (%d bytes):\n%s", len(counselled), counselled, len(silent), silent)
		}
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismDispatch)]); got != 1 {
			t.Errorf("dispatch interventions after the armed prime = %d, want exactly 1 — the counsel "+
				"that reached the agent above, and nothing from the two silent primes before it", got)
		}
	})

	// The thrift band, which the effort band used to stand in for at this surface. It is the band
	// worth exercising here rather than a second dispatch case, because it is the only capacity
	// counsel that fires with NO learned appetite at all: a factory that has measured nothing still
	// gets told to read narrowly when it is nearly full.
	t.Run("a session at the ceiling counsels thrift, once, and records it", func(t *testing.T) {
		fx, _, step := primedFixture(t, advisoryThriftPct)
		gateOn(t, fx.root)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{
			"thrift": "on", "dispatch": "off",
		})

		out := runPrimeCapturing(t)

		want := wantThriftAdvisory(t)
		if !strings.Contains(out, want) {
			t.Errorf("the thrift advisory did not reach the agent.\nwant to contain:\n%s\ngot:\n%s", want, out)
		}
		assertStepContract(t, out)

		thrift := interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismThrift)]
		if len(thrift) != 1 {
			t.Fatalf("thrift intervention records = %d, want exactly 1", len(thrift))
		}
		if thrift[0].Action != telemetry.ActionAdvise {
			t.Errorf("action = %q, want %q", thrift[0].Action, telemetry.ActionAdvise)
		}
		// #678 K7 puts a SECOND template under this mechanism, so the objective is what tells the two
		// apart in the record store. A capacity thrift filed as efficiency would credit this issue
		// with counsel a window drove.
		if thrift[0].Objective != telemetry.ObjectiveCapacity {
			t.Errorf("objective = %q, want %q — this counsel is keyed on a window",
				thrift[0].Objective, telemetry.ObjectiveCapacity)
		}
		if thrift[0].StepID != step.ID {
			t.Errorf("step_id = %q, want %q — a record that cannot be joined to its step is not evidence",
				thrift[0].StepID, step.ID)
		}
		if thrift[0].CtxUsedPct == nil {
			t.Error("the record carries no occupancy; the arithmetic that triggered it is not recoverable")
		}
	})

	t.Run("the same mechanism counsels at most once per step", func(t *testing.T) {
		fx, _, step := primedFixture(t, advisoryOccupancyPct)
		gateOn(t, fx.root)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, advisoryDispatchPeak, advisoryPriorRuns)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{
			"thrift": "off", "dispatch": "on",
		})

		first := runPrimeCapturing(t)
		second := runPrimeCapturing(t)

		want := wantAdvisory(t, tokenomics.MechanismDispatch, advisoryDispatchPeak, advisoryProjectedPctD)
		if !strings.Contains(first, want) {
			t.Fatal("the first prime did not counsel, so the second proves nothing")
		}
		if strings.Contains(second, want) {
			t.Error("a re-prime of the SAME step repeated the advisory; design-doc.md's K9 row " +
				"fixes it at most one per mechanism per step")
		}
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismDispatch)]); got != 1 {
			t.Errorf("dispatch intervention records = %d, want 1 — one episode leaves one record", got)
		}
	})

	t.Run("a serialization advisory declares a wait and counsel advisories do not", func(t *testing.T) {
		fx, _, step := primedFixture(t, advisoryOccupancyPct)
		gateOn(t, fx.root)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, advisoryDispatchPeak, advisoryPriorRuns)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{
			"thrift": "on", "dispatch": "on",
		})

		out := runPrimeCapturing(t)

		want := wantAdvisory(t, tokenomics.MechanismDispatch, advisoryDispatchPeak, advisoryProjectedPctD)
		if !strings.Contains(out, want) {
			t.Errorf("the serialization advisory did not reach the agent.\nwant to contain:\n%s\ngot:\n%s", want, out)
		}
		// Thrift is ARMED here and must stay silent: 60% is below the 90% ceiling, and a dispatch
		// fixture that also drew thrift counsel would mean the two bands read the same operand.
		if strings.Contains(out, wantAdvisory(t, tokenomics.MechanismThrift, advisoryDispatchPeak, advisoryProjectedPctD)) {
			t.Error("both the thrift and the dispatch advisory fired on one prime at 60% occupancy; " +
				"the thrift band is supposed to read occupancy against the ceiling")
		}

		// No advisory arms the latch, the serialization one included. Counsel is not evidence: this
		// runs at a step's OPEN and the agent has launched nothing. K18's observer arms it on the
		// first Task completion, where a fan-out is demonstrably under way — and if this ever starts
		// arming, the watchdog goes blind for fifteen minutes on the strength of advice.
		if interventionLatchHolds(loadRecoveryState(fx.root, fx.agent), boundaryTestNow()) {
			t.Error("a prime-time advisory latched the watchdog off. The latch suppresses every fire " +
				"class including exhaustion, and nothing has been observed to wait for yet")
		}
	})

	t.Run("counsel that describes work rather than waiting never arms the latch", func(t *testing.T) {
		fx, _, _ := primedFixture(t, advisoryThriftPct)
		gateOn(t, fx.root)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{
			"thrift": "on", "dispatch": "off",
		})

		out := runPrimeCapturing(t)

		if !strings.Contains(out, wantThriftAdvisory(t)) {
			t.Fatal("the thrift advisory never fired, so the latch assertion below proves nothing")
		}
		if interventionLatchHolds(loadRecoveryState(fx.root, fx.agent), boundaryTestNow()) {
			t.Error("a thrift advisory latched the watchdog off. It tells the agent to read narrowly, " +
				"not to wait, and the latch suppresses every fire class including exhaustion")
		}
	})

	t.Run("the umbrella off emits nothing and records nothing", func(t *testing.T) {
		fx, _, step := primedFixture(t, advisoryOccupancyPct)
		gateOn(t, fx.root)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, advisoryNoFitPeak, advisoryPriorRuns)

		out := runPrimeCapturing(t)

		if strings.Contains(out, advisoryHeading) {
			t.Errorf("tokenomics is off and prime still emitted an advisory block:\n%s", out)
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 0 {
			t.Errorf("intervention records = %d, want 0 with the umbrella off", got)
		}
	})
}

// TestAdvisoryTemplateSubstitutionIsNumeric is the [baseline] half of AC 1 restated at the emission
// site: ux.md:66 forbids agent- or config-controlled text from entering an injected surface, and the
// only thing standing between the two is that every operand is a number.
func TestAdvisoryTemplateSubstitutionIsNumeric(t *testing.T) {
	for _, tpl := range tokenomics.AdvisoryTemplates() {
		text, ok := tokenomics.RenderAdvisory(tpl.Mechanism, tokenomics.AdvisoryInputs{
			WindowTokens: 200000, FreeTokens: 80000, AppetiteTokens: 150000,
			ProjectedPct: 135, PriorRuns: 3,
		})
		if !ok {
			t.Fatalf("RenderAdvisory(%q) reported no template for a mechanism the registry lists", tpl.Mechanism)
		}
		if strings.Contains(text, "%!") {
			t.Errorf("%s renders a format error: %s", tpl.Mechanism, text)
		}
		if n := tokenomics.EstimateTokens(text); n > tokenomics.AdvisoryTokenBudget {
			t.Errorf("%s renders %d estimated tokens, over the %d budget", tpl.Mechanism, n, tokenomics.AdvisoryTokenBudget)
		}
		if strings.Contains(text, "offpath") || strings.Contains(text, "Step 1") {
			t.Errorf("%s interpolated a formula or step name: %s", tpl.Mechanism, text)
		}
	}
}

// advisorySection renders exactly what outputAdvisoryContext writes for one advisory, so the
// byte-identity control above compares against the production shape rather than a re-typed guess.
func advisorySection(texts ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\n%s\n", advisoryHeading)
	for _, text := range texts {
		fmt.Fprintf(&b, "\n%s\n", text)
	}
	b.WriteString("\n")
	return b.String()
}
