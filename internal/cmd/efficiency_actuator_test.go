package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// #678 K5-K8: token efficiency as an unconditional baseline.
//
// Every test in this file exists to hold one line: the efficiency arm decides from a step's LEARNED
// GENERATION HISTORY and never from how much room is left. The old effort actuator fired on
// `free < appetite`, which meant a roomy profile switched token efficiency off — and the profiles
// this factory actually runs on are roomy. So the fixtures below are deliberately the opposite of
// the ones that used to arm a mechanism: a one-million-token window at 5% occupancy, where every
// capacity band in the tree is arithmetically silent, and the efficiency half fires anyway.
//
// These tests do not run in parallel, for tokenomics_admission_test.go:20-21's reason.

const (
	// The learned shape that warrants a reduction: a 91% thinking share over four runs, past the 80%
	// default floor. Spelled as tokens rather than as a share because the share is derived with exact
	// integer arithmetic (efficiency.go), and a fixture that stated the answer could not catch a
	// change to the derivation.
	efficiencyOutTokens   = 10000
	efficiencyThinkTokens = 9100
	efficiencyRuns        = 4

	// A 1,000,000-token window at 5%: no capacity mechanism in the tree can fire here. Every
	// "fires without pressure" assertion below is measured against this session.
	roomyWindowTokens = 1000000
	roomyOccupancyPct = 5.0

	// A 262,144-token window whose learned PEAK does not fit even an empty session. This is the one
	// capacity trigger K5 keeps, and it is a capacity FACT rather than a scarcity heuristic: a step
	// this size fits nowhere on this profile, so no handoff can help and the level is the last lever.
	tightWindowTokens = 262144
	tightPeakTokens   = 400000
)

// seedEfficiency writes a learned aggregate carrying GENERATION figures, which seedAppetite does not:
// its aggregate has occupancy history alone, and the efficiency predicate reads GenerationRuns rather
// than Runs precisely so a key measured before the generation legs existed cannot be mistaken for one
// that has measured a thinking share.
func seedEfficiency(t *testing.T, root, formula, stepLabel, model string, a tokenomics.Aggregate) {
	t.Helper()
	path := telemetry.LearnedDigestPath(config.TelemetryDir(root), formula)
	d, err := tokenomics.LoadDigest(path)
	if err != nil {
		t.Fatalf("LoadDigest: %v", err)
	}
	if a.UpdatedAt == "" {
		a.UpdatedAt = "2026-09-09T00:00:00.000Z"
	}
	d.Put(tokenomics.DigestKey{Formula: formula, StepID: stepLabel, Model: model}, a)
	if err := tokenomics.SaveDigest(path, d); err != nil {
		t.Fatalf("SaveDigest: %v", err)
	}
}

// reducibleAggregate is the history that warrants a level reduction and nothing else. SessionsPerStep
// is 1 and MedianRepeatReads is 0 on purpose: the three efficiency conclusions are independent, and a
// fixture that armed all three at once could not tell which one a caller acted on.
func reducibleAggregate() tokenomics.Aggregate {
	return tokenomics.Aggregate{
		Runs:                    efficiencyRuns,
		GenerationRuns:          efficiencyRuns,
		MedianOutTokens:         efficiencyOutTokens,
		MedianThinkTokens:       efficiencyThinkTokens,
		MedianPeakCtxTokens:     40000,
		MedianMarginalCtxTokens: 30000,
		SessionsPerStep:         1,
	}
}

// armEfficiency writes a startup.json with the umbrella on, the effort arm on and the efficiency
// operands stated. Through the real loader for armAdvisoryPolicy's reason: a fixture the production
// path would reject must fail here rather than reappear as an unexplained inert mechanism.
func armEfficiency(t *testing.T, root string, extra map[string]any) {
	t.Helper()
	if err := os.WriteFile(tokenomicsGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write tokenomics gate: %v", err)
	}
	block := map[string]any{
		"enabled":                       "on",
		"budget":                        "on",
		"effort":                        "on",
		"efficiency":                    "on",
		"admission_margin_pct":          10,
		"learned_min_runs":              2,
		"efficiency_effort_level":       "medium",
		"efficiency_thinking_share_pct": 80,
		"efficiency_repeat_read_floor":  1,
		"efficiency_max_relaunches":     6,
	}
	for k, v := range extra {
		block[k] = v
	}
	body, err := json.Marshal(map[string]any{"tokenomics": block})
	if err != nil {
		t.Fatalf("marshal startup.json: %v", err)
	}
	if err := os.WriteFile(config.StartupConfigPath(root), body, 0o644); err != nil {
		t.Fatalf("write startup.json: %v", err)
	}
	if _, err := config.LoadStartupConfig(root); err != nil {
		t.Fatalf("the fixture's startup.json does not load: %v", err)
	}
}

// declareWindow writes a models.json whose default profile declares a context window, so
// profileWindow resolves a DECLARED window rather than falling back. Every window-sensitive
// assertion below turns on which of the two it got.
func declareWindow(t *testing.T, root string, tokens int) {
	t.Helper()
	body := map[string]any{
		"default": "local",
		"models": map[string]map[string]string{
			"local": {
				"ANTHROPIC_MODEL":                "claude-opus-5",
				"CLAUDE_CODE_MAX_CONTEXT_TOKENS": itoa(tokens),
			},
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal models.json: %v", err)
	}
	if err := os.WriteFile(config.ModelsConfigPath(root), data, 0o644); err != nil {
		t.Fatalf("write models.json: %v", err)
	}
}

// hookFormulaName makes hookedFormulaName resolvable. It reads .runtime/last_closed_step for the
// store-free reason memoryScopeKey does, so a fixture that only writes hooked_formula resolves no
// formula and every learned read below silently misses.
func hookFormulaName(t *testing.T, workDir, formula string) {
	t.Helper()
	writeRuntimeFile(t, workDir, "last_closed_step", `{"formula":"Formula: `+formula+`"}`)
}

// launchEnv is the model env a launch leg hands to withEffortLevel, optionally declaring a level.
func launchEnv(declared string) []config.EnvVar {
	env := []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-5"}}
	if declared != "" {
		env = append(env, config.EnvVar{Key: config.EnvEffortLevel, Value: declared})
	}
	return env
}

func effortLevelIn(env []config.EnvVar) string {
	for _, kv := range env {
		if kv.Key == config.EnvEffortLevel {
			return kv.Value
		}
	}
	return ""
}

// TestEffortSelectedAtLaunchLegs is #678 AC-4 and it is deliberately in two halves.
//
// The behavioural half drives the helper the launch legs call, because that is where the level is
// chosen and capped. The source half pins that all three legs actually call it — a universal over
// call sites, which no single launch can witness, and the failure it catches is the one that matters:
// a leg that quietly stops selecting still launches sessions, still records nothing, and looks
// exactly like a factory whose steps happen not to warrant a reduction.
func TestEffortSelectedAtLaunchLegs(t *testing.T) {
	// The nextStepLabel every case below resolves against: primedFixture seeds a two-step formula and
	// step-2 is the one a launching session picks up.
	const nextStep = "step-2"

	setup := func(t *testing.T) (lifecycleFixture, string) {
		t.Helper()
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		armEfficiency(t, fx.root, nil)
		hookFormulaName(t, fx.workDir, "offpath")
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedEfficiency(t, fx.root, "offpath", nextStep, model, reducibleAggregate())
		return fx, model
	}

	t.Run("the learned baseline chooses the level", func(t *testing.T) {
		fx, _ := setup(t)

		got := withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "")

		if lvl := effortLevelIn(got); lvl != "medium" {
			t.Errorf("%s = %q, want %q — the step's learned generation history warrants a reduction "+
				"and a 1,000,000-token window at 5%% must not be able to switch it off",
				config.EnvEffortLevel, lvl, "medium")
		}
		crumb := readEffortBreadcrumb(fx.workDir)
		if crumb.Level != "medium" || crumb.Objective != string(tokenomics.ObjectiveEfficiency) {
			t.Errorf("breadcrumb = %+v, want level=medium objective=efficiency — af prime reads the "+
				"objective from here and nowhere else", crumb)
		}
		if crumb.StepLabel != nextStep {
			t.Errorf("breadcrumb step_label = %q, want %q; af done compares the next step's plan "+
				"against the level in force and cannot without it", crumb.StepLabel, nextStep)
		}
	})

	t.Run("the profile-declared level is a ceiling, never a floor", func(t *testing.T) {
		fx, _ := setup(t)

		// "low" is BELOW the selected "medium": an operator who declared a cheaper level asked for a
		// cheaper level, and an efficiency arm that raised it would be spending tokens in the name of
		// saving them.
		if lvl := effortLevelIn(withEffortLevel(fx.root, fx.workDir, launchEnv("low"), nextStep, "")); lvl != "low" {
			t.Errorf("%s = %q with a profile declaring low, want low — the selection is a ceiling",
				config.EnvEffortLevel, lvl)
		}
		// "max" is ABOVE it, so the selection stands.
		if lvl := effortLevelIn(withEffortLevel(fx.root, fx.workDir, launchEnv("max"), nextStep, "")); lvl != "medium" {
			t.Errorf("%s = %q with a profile declaring max, want medium — a profile above the "+
				"selected level does not raise it", config.EnvEffortLevel, lvl)
		}
	})

	t.Run("an auto profile imposes no numeric ceiling", func(t *testing.T) {
		fx, _ := setup(t)

		// "auto" is a host mode, not a rank: EffortRank returns -1 for it. Treating it as a ceiling
		// would compare a mode against a number and refuse every reduction on a profile that had
		// deferred to the host.
		if lvl := effortLevelIn(withEffortLevel(fx.root, fx.workDir, launchEnv("auto"), nextStep, "")); lvl != "medium" {
			t.Errorf("%s = %q with a profile declaring auto, want medium", config.EnvEffortLevel, lvl)
		}
	})

	t.Run("with the arm off the leg keeps today's drop-only behaviour", func(t *testing.T) {
		fx, _ := setup(t)
		armEfficiency(t, fx.root, map[string]any{"effort": "off"})

		for _, kv := range withEffortLevel(fx.root, fx.workDir, launchEnv("low"), nextStep, "") {
			if kv.Key == config.EnvEffortLevel {
				t.Errorf("the arm is off and the launch still exports %s=%q; the control group would "+
					"receive the treatment", kv.Key, kv.Value)
			}
		}
		if _, err := os.Stat(effortBreadcrumbPath(fx.workDir)); !os.IsNotExist(err) {
			t.Errorf("the arm is off and a breadcrumb was written (stat err %v); af prime would then "+
				"record a treatment nothing applied", err)
		}
	})

	t.Run("with no learned data the leg exports nothing and removes nothing", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		armEfficiency(t, fx.root, nil)
		hookFormulaName(t, fx.workDir, "offpath")

		// A profile-declared level with NO digest entry behind it. Absence must never arm an action,
		// and it must not disarm one either: the operator's own declaration stands.
		got := withEffortLevel(fx.root, fx.workDir, launchEnv("high"), nextStep, "")
		if lvl := effortLevelIn(got); lvl != "high" {
			t.Errorf("%s = %q with no learned data, want the profile's own high — an unmeasured step "+
				"is not evidence for anything", config.EnvEffortLevel, lvl)
		}
		if _, err := os.Stat(effortBreadcrumbPath(fx.workDir)); !os.IsNotExist(err) {
			t.Errorf("a breadcrumb was written for a selection that never happened (stat err %v)", err)
		}
	})

	// The record half. The treatment is applied at launch and reported by the session it was applied
	// to, which is the whole point of moving it: before this, a first session and every non-boundary
	// relaunch ran reduced and recorded nothing.
	t.Run("af prime reports the applied level and records the treatment", func(t *testing.T) {
		fx, _ := setup(t)
		gateOn(t, fx.root)

		withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "")

		origHook := primeHookMode
		primeHookMode = true
		t.Cleanup(func() { primeHookMode = origHook })
		primeWithHookSession(t, "sess-efficiency")

		records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		var starts, treatments int
		for _, r := range records {
			switch r.Event {
			case telemetry.EventSessionStart:
				starts++
				if r.EffortLevel != "medium" {
					t.Errorf("session_start effort_level = %q, want medium — a session that cannot say "+
						"which arm it ran on is not evidence for either", r.EffortLevel)
				}
			case telemetry.EventIntervention:
				if r.Mechanism != string(tokenomics.MechanismEffort) || r.Action != telemetry.ActionReduceEffort {
					continue
				}
				treatments++
				if r.Objective != telemetry.ObjectiveEfficiency {
					t.Errorf("objective = %q, want %q — this reduction was chosen from a generation "+
						"baseline, not from a window", r.Objective, telemetry.ObjectiveEfficiency)
				}
				if r.EffortLevel != "medium" {
					t.Errorf("intervention effort_level = %q, want medium", r.EffortLevel)
				}
			}
		}
		if starts != 1 {
			t.Errorf("session_start records = %d, want 1", starts)
		}
		if treatments != 1 {
			t.Errorf("effort/reduce_effort records = %d, want exactly 1", treatments)
		}
	})

	// The design named a `.runtime/effort_next` marker at one point and this implementation does not
	// write one: the level is exported on the launch line, where the host reads it, and a marker would
	// be a second source an agent's own process could edit between the two.
	t.Run("no effort_next marker is ever created", func(t *testing.T) {
		fx, _ := setup(t)
		withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "")

		if _, err := os.Stat(filepath.Join(fx.workDir, ".runtime", "effort_next")); !os.IsNotExist(err) {
			t.Errorf("an effort_next marker exists (stat err %v); the launch line is the only channel", err)
		}
	})

	// The wiring. TestEffortArmWiredAtEveryModelEnvSite owns the universal ("no site is unwrapped");
	// this owns the existential the acceptance criterion names — the three legs a session can start
	// through are all present, so a rename that removed one leg while leaving the interlock vacuously
	// green still fails here.
	t.Run("all three launch legs select", func(t *testing.T) {
		hits := grepPackage(t, ".", "mgr.SetModelEnv(withEffortLevel(")
		if len(hits) < 3 {
			t.Errorf("withEffortLevel is wired at %d model-env sites (%v), want at least 3 — the "+
				"watchdog respawn (helpers.go), af sling and af up", len(hits), hits)
		}
		for _, leg := range []string{"helpers.go", "sling.go", "up.go"} {
			var found bool
			for _, hit := range hits {
				if strings.Contains(hit, leg) {
					found = true
				}
			}
			if !found {
				t.Errorf("%s does not select an effort level; a session started through that leg runs "+
					"at the host default and the arm has a hole exactly where it is least visible", leg)
			}
		}
	})
}

// TestCapacityLastResortEffort is the one capacity trigger K5 keeps, and the test exists to hold it
// to a FACT rather than to the scarcity heuristic it replaces.
//
// The old band fired on `free < appetite` — a statement about the moment, which a roomy profile made
// permanently false. This one fires when the step's learned peak overruns an EMPTY session, which is
// a statement about the step and the profile: it fits nowhere, so no handoff can help and the level
// is the only lever left. The two windows below are the whole test — same step, same history, and
// the answer changes because the profile did.
func TestCapacityLastResortEffort(t *testing.T) {
	const nextStep = "step-2"

	// A history with NO generation figures, so the efficiency half declines and the capacity half is
	// the only thing that can produce a level. Without that the test could not tell them apart.
	unmeasured := tokenomics.Aggregate{
		Runs:                    efficiencyRuns,
		MedianPeakCtxTokens:     tightPeakTokens,
		MedianMarginalCtxTokens: tightPeakTokens,
	}

	setup := func(t *testing.T, window int) lifecycleFixture {
		t.Helper()
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, window)
		armEfficiency(t, fx.root, nil)
		hookFormulaName(t, fx.workDir, "offpath")
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedEfficiency(t, fx.root, "offpath", nextStep, model, unmeasured)
		return fx
	}

	t.Run("a step that fits no session on this profile runs reduced", func(t *testing.T) {
		fx := setup(t, tightWindowTokens)

		if lvl := effortLevelIn(withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "")); lvl != "medium" {
			t.Errorf("%s = %q on a %d-token window against a %d-token learned peak, want medium",
				config.EnvEffortLevel, lvl, tightWindowTokens, tightPeakTokens)
		}
		crumb := readEffortBreadcrumb(fx.workDir)
		if crumb.Objective != string(tokenomics.ObjectiveCapacity) {
			t.Errorf("objective = %q, want %q — this reduction is a capacity last resort and filing "+
				"it as efficiency would credit this issue with a step nothing can hold",
				crumb.Objective, tokenomics.ObjectiveCapacity)
		}
	})

	t.Run("the same step on a roomy window does nothing", func(t *testing.T) {
		fx := setup(t, roomyWindowTokens)

		if lvl := effortLevelIn(withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "")); lvl != "" {
			t.Errorf("%s = %q on a %d-token window, want no level at all — a step that fits is not a "+
				"capacity emergency, and the efficiency half declined for want of a generation baseline",
				config.EnvEffortLevel, lvl, roomyWindowTokens)
		}
		if _, err := os.Stat(effortBreadcrumbPath(fx.workDir)); !os.IsNotExist(err) {
			t.Errorf("a breadcrumb was written for a selection that never happened (stat err %v)", err)
		}
	})
}

// TestRepurposedMechanismFiresWithoutPressure is #678's headline claim, held at the surface an agent
// actually sees. Every fixture here is a one-million-token window at 5% occupancy: the thrift band
// needs 90%, the dispatch band needs a step that only just fits, and the budget verdict needs a
// projection past the ceiling. None of them can fire. The efficiency half fires anyway, because it
// never learns how much room there is.
func TestRepurposedMechanismFiresWithoutPressure(t *testing.T) {
	t.Run("thrift counsel fires on learned re-reads with the window empty", func(t *testing.T) {
		fx, _, step := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		gateOn(t, fx.root)
		armEfficiency(t, fx.root, nil)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")

		a := reducibleAggregate()
		a.MedianRepeatReads = 4
		seedEfficiency(t, fx.root, "offpath", stepLabelOf(step), model, a)

		out := runPrimeCapturing(t)

		want, ok := tokenomics.RenderAdvisoryKey(tokenomics.AdvisoryKeyEfficiencyThrift,
			tokenomics.EfficiencyAdvisory(tokenomics.EfficiencyInputs{
				MedianRepeatReads: 4, MedianOutTokens: efficiencyOutTokens, PriorRuns: efficiencyRuns,
			}))
		if !ok {
			t.Fatal("no efficiency thrift template; the fixture asserts against nothing")
		}
		if !strings.Contains(out, want) {
			t.Errorf("the efficiency thrift did not reach the agent at 5%% of a 1,000,000-token "+
				"window.\nwant to contain:\n%s\ngot:\n%s", want, out)
		}
		// The capacity thrift must NOT also fire: it reads occupancy against the ceiling, and 5% is
		// not at any ceiling. Two thrift blocks in one prime would mean the composite key is not
		// keeping the two entries apart.
		if strings.Count(out, "Thrift: this step projects") != 0 {
			t.Errorf("the CAPACITY thrift fired at 5%% occupancy:\n%s", out)
		}

		thrift := interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismThrift)]
		if len(thrift) != 1 {
			t.Fatalf("thrift intervention records = %d, want exactly 1", len(thrift))
		}
		if thrift[0].Objective != telemetry.ObjectiveEfficiency {
			t.Errorf("objective = %q, want %q", thrift[0].Objective, telemetry.ObjectiveEfficiency)
		}
		if thrift[0].Action != telemetry.ActionAdvise {
			t.Errorf("action = %q, want %q", thrift[0].Action, telemetry.ActionAdvise)
		}
	})

	// The dedup key. The capacity thrift ledgers under "thrift" and the efficiency one under
	// "thrift|efficiency", so neither can suppress the other — and an old ledger written before the
	// composite key existed still reads as "the capacity thrift has fired", which is what it meant.
	t.Run("the two thrift entries do not suppress each other", func(t *testing.T) {
		fx, _, step := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		gateOn(t, fx.root)
		armEfficiency(t, fx.root, nil)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")

		a := reducibleAggregate()
		a.MedianRepeatReads = 4
		seedEfficiency(t, fx.root, "offpath", stepLabelOf(step), model, a)

		// A ledger for this step naming the bare mechanism, exactly as a binary that predates the
		// composite key would have left it.
		saveAdvisoryLedger(fx.workDir, advisoryLedger{StepID: step.ID, Keys: []string{"thrift"}})

		out := runPrimeCapturing(t)

		if !strings.Contains(out, "Efficiency: prior runs of this step re-read") {
			t.Errorf("a pre-existing \"thrift\" ledger entry suppressed the efficiency thrift; the two "+
				"are different counsel under one mechanism and the composite key is what keeps them "+
				"apart:\n%s", out)
		}

		ledger := loadAdvisoryLedger(fx.workDir, step.ID)
		var sawComposite bool
		for _, k := range ledger.Keys {
			if k == tokenomics.AdvisoryKeyEfficiencyThrift {
				sawComposite = true
			}
		}
		if !sawComposite {
			t.Errorf("the ledger does not record %q after the efficiency thrift fired (%v); a re-prime "+
				"of the same step would repeat it", tokenomics.AdvisoryKeyEfficiencyThrift, ledger.Keys)
		}

		// And it is once per step, like every other advisory.
		if second := runPrimeCapturing(t); strings.Contains(second, "Efficiency: prior runs of this step re-read") {
			t.Error("a re-prime of the SAME step repeated the efficiency thrift")
		}
	})

	t.Run("a second prime in the same session receives less", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		gateOn(t, fx.root)
		armEfficiency(t, fx.root, nil)

		first := runPrimeCapturing(t)
		second := runPrimeCapturing(t)

		// Named rather than measured. A byte count alone would also shrink if some unrelated
		// once-per-step block deduped between the two primes, so the assertion is on the marker the
		// identity block itself emits — outputStartupDirective's heading, which is inside the gate.
		if !strings.Contains(first, "## Startup Directive") {
			t.Fatal("the FIRST prime carried no identity block, so its absence from the second proves nothing")
		}
		if strings.Contains(second, "## Startup Directive") {
			t.Error("the second prime of one session re-sent the identity block; a session that has " +
				"already been primed does not need its role template read to it again")
		}
		if len(second) >= len(first) {
			t.Errorf("the second prime is not smaller: %d bytes vs %d", len(second), len(first))
		}
		// The identity block is what is withheld; the step contract is not, because an agent that
		// resumes without its instructions has nothing to resume (design-doc.md:228).
		assertStepContract(t, second)
		if !strings.Contains(second, "[AGENT FACTORY]") {
			t.Error("the slimmed prime dropped the [AGENT FACTORY] header; an agent must still be able " +
				"to tell it is inside a factory")
		}

		interview := interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismInterview)]
		if len(interview) != 1 {
			t.Fatalf("interview intervention records = %d, want exactly 1 — once per session, not "+
				"once per prime", len(interview))
		}
		if interview[0].Objective != telemetry.ObjectiveEfficiency {
			t.Errorf("objective = %q, want %q", interview[0].Objective, telemetry.ObjectiveEfficiency)
		}
		if interview[0].Action != telemetry.ActionAdvise {
			t.Errorf("action = %q, want %q", interview[0].Action, telemetry.ActionAdvise)
		}

		// A THIRD prime must not record again: the latch is per session, and a per-prime record would
		// report one reduction as a dozen.
		runPrimeCapturing(t)
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismInterview)]); got != 1 {
			t.Errorf("interview records after three primes = %d, want still 1", got)
		}
	})

	t.Run("the interview switch gates every reduction", func(t *testing.T) {
		fx, _, step := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		gateOn(t, fx.root)
		armEfficiency(t, fx.root, map[string]any{"interview": "off"})
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		a := reducibleAggregate()
		a.SessionsPerStep = 3
		seedEfficiency(t, fx.root, "offpath", stepLabelOf(step), model, a)

		runPrimeCapturing(t)
		second := runPrimeCapturing(t)

		if !strings.Contains(second, "## Startup Directive") {
			t.Error("the interview mechanism is off and the second prime still withheld the identity " +
				"block; the operator's switch did not reach the reduction")
		}
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismInterview)]); got != 0 {
			t.Errorf("interview intervention records = %d with the mechanism off", got)
		}

		// The clean start is the same switch: a relaunch that recycles a session IS the interview's
		// act, so an operator who turned the interview off has turned that off too.
		adm := admission{
			policy:     resolvedPolicy(fx.root, mustStartupTokenomics(t, fx.root)),
			efficiency: tokenomics.EfficiencyPlan{CleanStart: true},
		}
		if eff := boundaryEfficiencyRelaunch(fx.workDir, "inst-1", adm); eff.warranted {
			t.Error("a clean start was warranted with the interview mechanism off")
		}
	})

	t.Run("a single-session step declines the clean start", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		armEfficiency(t, fx.root, nil)

		// SessionsPerStep == 1: the step has never needed a second session, so recycling one at its
		// boundary would spend a relaunch to solve a problem it does not have.
		plan := tokenomics.Efficiency(reducibleAggregate(), true,
			resolvedPolicy(fx.root, mustStartupTokenomics(t, fx.root)))
		if plan.CleanStart {
			t.Error("a step that has always fitted one session asked for a clean start")
		}
		if plan.EffortLevel == "" {
			t.Error("the same plan warranted no level either, so the assertion above proves nothing " +
				"about CleanStart specifically")
		}
	})
}

// mustStartupTokenomics loads the fixture's tokenomics block through the real loader, so a policy
// assembled in a test is the same policy the verb layer would assemble.
func mustStartupTokenomics(t *testing.T, root string) config.TokenomicsConfig {
	t.Helper()
	cfg, err := config.LoadStartupConfig(root)
	if err != nil {
		t.Fatalf("LoadStartupConfig: %v", err)
	}
	return cfg.Tokenomics
}

// TestEfficiencyRelaunchBound is the D-10 bound. An actuator that hands off at every step boundary of
// a formula whose steps all warrant a clean start would recycle a session per step forever, and each
// recycle costs a full re-prime — the exact cost this issue exists to remove, spent by the mechanism
// meant to save it.
func TestEfficiencyRelaunchBound(t *testing.T) {
	adm := func(t *testing.T, root string) admission {
		t.Helper()
		return admission{
			policy:     resolvedPolicy(root, mustStartupTokenomics(t, root)),
			efficiency: tokenomics.EfficiencyPlan{CleanStart: true},
		}
	}

	t.Run("the counter bounds the relaunches", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		armEfficiency(t, fx.root, map[string]any{"efficiency_max_relaunches": 2})
		a := adm(t, fx.root)

		for i := 0; i < 2; i++ {
			eff := boundaryEfficiencyRelaunch(fx.workDir, "inst-1", a)
			if !eff.warranted {
				t.Fatalf("relaunch %d was refused below the bound", i+1)
			}
			if eff.atCap {
				t.Fatalf("relaunch %d reported at-cap below the bound", i+1)
			}
			bumpEfficiencyRelaunches(fx.workDir, "inst-1")
		}

		eff := boundaryEfficiencyRelaunch(fx.workDir, "inst-1", a)
		if eff.warranted {
			t.Error("the third relaunch was warranted against a bound of 2")
		}
		if !eff.atCap {
			t.Error("the bound refused a relaunch and did not say so; an operator reading the records " +
				"would see a mechanism that silently stopped working")
		}
	})

	t.Run("the counter is per formula instance", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		armEfficiency(t, fx.root, map[string]any{"efficiency_max_relaunches": 1})
		a := adm(t, fx.root)

		bumpEfficiencyRelaunches(fx.workDir, "inst-1")
		if eff := boundaryEfficiencyRelaunch(fx.workDir, "inst-1", a); eff.warranted {
			t.Fatal("the bound did not hold for the instance that reached it")
		}
		// A different instance is a different run of the formula, and a count carried across would
		// refuse the first relaunch of a run that has had none.
		if eff := boundaryEfficiencyRelaunch(fx.workDir, "inst-2", a); !eff.warranted {
			t.Error("a second formula instance inherited the first one's relaunch count")
		}
	})

	t.Run("the counter is swept with the other runtime artifacts", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		armEfficiency(t, fx.root, nil)

		bumpEfficiencyRelaunches(fx.workDir, "inst-1")
		writeEffortBreadcrumb(fx.workDir, effortBreadcrumb{Level: "medium", Objective: "efficiency"})
		if _, err := os.Stat(efficiencyRelaunchPath(fx.workDir)); err != nil {
			t.Fatalf("the fixture wrote no counter, so the sweep below proves nothing: %v", err)
		}

		cleanupRuntimeArtifacts(fx.workDir)

		for _, path := range []string{efficiencyRelaunchPath(fx.workDir), effortBreadcrumbPath(fx.workDir)} {
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Errorf("%s survived the formula's runtime cleanup (stat err %v); a stale count or a "+
					"stale level would then answer for the next formula", filepath.Base(path), err)
			}
		}
	})
}

// TestEfficiencyInterventionRecorded is Gap 19 held across every efficiency act at once: each writes
// an intervention naming its own mechanism with objective=efficiency, and NONE of them is filed under
// budget. Budget is the capacity mechanism's name, and an efficiency act wearing it would make the
// two objectives impossible to tell apart in the one place the experiment is read from.
func TestEfficiencyInterventionRecorded(t *testing.T) {
	fx, _, step := primedFixture(t, roomyOccupancyPct)
	declareWindow(t, fx.root, roomyWindowTokens)
	gateOn(t, fx.root)
	armEfficiency(t, fx.root, nil)
	hookFormulaName(t, fx.workDir, "offpath")
	model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")

	a := reducibleAggregate()
	a.MedianRepeatReads = 4
	seedEfficiency(t, fx.root, "offpath", stepLabelOf(step), model, a)
	seedEfficiency(t, fx.root, "offpath", "step-2", model, a)

	// The launch leg, then the session's primes. #681 T1: a --hook prime no longer counts toward the
	// re-prime reduction (K1 withholds its identity unconditionally, so it delivers nothing to slim),
	// so the interview reduction now fires on a genuine PLAIN re-prime rather than the opening hook
	// prime -- the real-world shape (SessionStart hook, then a plain prime after each af done). The
	// opening hook prime opens the session and fires the thrift; the first plain prime renders identity
	// (count 1, not slimmed); the second plain prime is the same-session re-prime that slims and
	// records the interview reduction (count > 1). Three efficiency acts, three mechanisms.
	withEffortLevel(fx.root, fx.workDir, launchEnv(""), "step-2", "")
	origHook := primeHookMode
	primeHookMode = true
	t.Cleanup(func() { primeHookMode = origHook })
	primeWithHookSession(t, "sess-record")
	primeHookMode = false
	runPrimeCapturing(t)
	runPrimeCapturing(t)

	records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	seen := map[string]bool{}
	for _, r := range records {
		if r.Event != telemetry.EventIntervention {
			continue
		}
		if r.Objective == telemetry.ObjectiveEfficiency {
			if r.Mechanism == string(tokenomics.MechanismBudget) {
				t.Errorf("an efficiency act was filed under the budget mechanism (action %q); budget is "+
					"the capacity mechanism and this is how the two objectives stop being separable",
					r.Action)
			}
			// #679 T1: the join key tracks the record's real arm semantics, not a blanket StepID.
			// A reduce_effort record decides the REDUCED arm and that split is SESSION-keyed
			// (rebuild.go objectivePerSession), so its load-bearing key is SessionID — an empty StepID
			// is legitimate (a step-less opening prime writes one). The thrift/interview ADVISORIES are
			// per-step counsel and still join on the step they were rendered for.
			if r.Action == telemetry.ActionReduceEffort {
				if r.SessionID == "" {
					t.Errorf("a reduce_effort record carries no session; the reduced/baseline split is " +
						"session-keyed and a record naming no session joins no arm")
				}
			} else if r.StepID == "" {
				t.Errorf("an efficiency advisory %s record joins to no step", r.Action)
			}
			seen[r.Mechanism+"/"+r.Action] = true
		}
	}
	for _, want := range []string{
		string(tokenomics.MechanismEffort) + "/" + telemetry.ActionReduceEffort,
		string(tokenomics.MechanismThrift) + "/" + telemetry.ActionAdvise,
		string(tokenomics.MechanismInterview) + "/" + telemetry.ActionAdvise,
	} {
		if !seen[want] {
			t.Errorf("no %s record with objective=efficiency; the act happened and left no evidence "+
				"(saw %v)", want, seen)
		}
	}
}

// TestReduceEffortRecordJoinsReducedArm is #679 F2 (AC-1): a step reduced by the effort actuator
// ALONE — not also slimmed or thrifted — must still enter the REDUCED arm.
//
// The reduce_effort record is the effort actuator's only treatment marker, and the reduced/baseline
// split joins Objective per step on {InstanceID, StepID}. A record written with no StepID joins
// nothing: its step_end gets Objective="" and folds into BASELINE, so ReducedRuns stays 0 and
// reductionStillPays is stuck true — the quality guard never fires for this actuator and the baseline
// self-pollutes. This drives the REAL prime write path plus a step_end for the same step through the
// exported rebuild and pins that the treated run lands in the reduced arm (ReducedRuns increments).
func TestReduceEffortRecordJoinsReducedArm(t *testing.T) {
	const formula = "reducejoin"
	fx, epic, step := primedFixture(t, roomyOccupancyPct)
	declareWindow(t, fx.root, roomyWindowTokens)
	gateOn(t, fx.root)
	armEfficiency(t, fx.root, nil)
	hookFormulaName(t, fx.workDir, formula)
	model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")

	// The history that warrants a level reduction and NOTHING else: no MedianRepeatReads, so the thrift
	// arm stays silent and the reduce_effort record is the only efficiency intervention on the step. The
	// treated step is the step this prime picks up, so whichever bead id the fix attributes to the
	// record equals the step_end.StepID closed below.
	seedEfficiency(t, fx.root, formula, stepLabelOf(step), model, reducibleAggregate())

	withEffortLevel(fx.root, fx.workDir, launchEnv(""), stepLabelOf(step), "")
	origHook := primeHookMode
	primeHookMode = true
	t.Cleanup(func() { primeHookMode = origHook })
	// ONE prime opens the session and fires the reduce_effort record. A second prime of the same session
	// would arm the interview slim, whose record carries primed.stepID and would join the step_end on its
	// own — masking whether the reduce_effort record itself joined. One prime keeps the pin honest.
	primeWithHookSession(t, "sess-reducejoin")

	// The closing record for the SAME instance and step. samplesFrom skips a step_end with no StepLabel;
	// the digest key's StepID leg is the StepLabel, while the Objective join keys on the SESSION (#679
	// F1). Production stamps every step record with the current session (telemetry_record.go:181), so
	// this close carries the same sess-reducejoin the prime above filed the reduce_effort record under —
	// the two share the session, which is what puts the run in the reduced arm.
	if err := telemetry.AppendEvent(config.TelemetryDir(fx.root), telemetry.StepEvent{
		V:          telemetry.SchemaVersion,
		Event:      telemetry.EventStepEnd,
		Agent:      fx.agent,
		Formula:    formula,
		InstanceID: epic.ID,
		StepID:     step.ID,
		StepLabel:  stepLabelOf(step),
		SessionID:  "sess-reducejoin",
		Model:      model,
		Status:     telemetry.StatusClosed,
	}); err != nil {
		t.Fatalf("append step_end: %v", err)
	}

	d, _ := telemetry.RebuildLearnedDigests(config.TelemetryDir(fx.root), []string{fx.agent}, formula, "2026-09-09T00:00:00.000Z")
	a, ok := d[formula].Lookup(tokenomics.DigestKey{Formula: formula, StepID: stepLabelOf(step), Model: model})
	if !ok {
		t.Fatalf("no aggregate for the treated step %q — the step_end did not fold at all", stepLabelOf(step))
	}
	if a.ReducedRuns != 1 {
		t.Errorf("ReducedRuns = %d, want 1 — a step reduced by effort alone must enter the reduced arm; "+
			"the reduce_effort record joined no step because it was written without the treated StepID", a.ReducedRuns)
	}
}

// TestReducedSessionStepLessPrimeJoinsReducedArm is #679 T2 (AC-2): a reduced session whose OPENING
// prime resolves no step must still enter the REDUCED arm.
//
// The reduce_effort record is written at prime time, but the gate that writes it also required
// `primed != nil` (prime.go:137) — so an opening prime that resolves no ready step (a fresh session
// before its formula is hooked, or one the store yields no step for) wrote NO record even though the
// launch leg had already applied and attested the reduction on disk. The session then folds to
// BASELINE: with no reduce_effort record to name it, objectivePerSession credits it to no arm and its
// closing step lands in the control group the treatment was meant to be measured against.
//
// The arm is a property of the SESSION (rebuild.go objectivePerSession keys on SessionID and never on
// StepID), so the record has everything it needs to join even with an empty step context — which is
// exactly what makes dropping the `primed != nil` conjunct safe. This drives the REAL opening --hook
// prime on the step-less path and pins that the treated session lands in the reduced arm.
func TestReducedSessionStepLessPrimeJoinsReducedArm(t *testing.T) {
	const formula = "offpath"
	const sessionID = "sess-stepless"
	// newLifecycleFixture deliberately does NOT write .runtime/hooked_formula, so outputFormulaContext
	// returns nil and primeAgent hands back a nil primedStep — the step-less opening prime this pins.
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)
	armEfficiency(t, fx.root, nil)
	model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")

	// The attestation the launch leg leaves on disk: this session was launched at a reduced level for
	// an efficiency reason. It is written whether or not a step is in flight, which is the whole shape
	// the step-less prime must not drop on the floor.
	writeEffortBreadcrumb(fx.workDir, effortBreadcrumb{
		Level: "medium", Objective: string(tokenomics.ObjectiveEfficiency),
	})

	origHook := primeHookMode
	primeHookMode = true
	t.Cleanup(func() { primeHookMode = origHook })
	primeWithHookSession(t, sessionID)

	// (a) the record itself, keyed on the session it acted on.
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	var reduceRecords int
	for _, r := range records {
		if r.Event != telemetry.EventIntervention || r.Action != telemetry.ActionReduceEffort {
			continue
		}
		reduceRecords++
		if r.SessionID != sessionID {
			t.Errorf("reduce_effort record session = %q, want %q — the arm join is session-keyed and a "+
				"record naming no session marks nothing", r.SessionID, sessionID)
		}
		if r.Objective != telemetry.ObjectiveEfficiency {
			t.Errorf("reduce_effort objective = %q, want %q", r.Objective, telemetry.ObjectiveEfficiency)
		}
	}
	if reduceRecords != 1 {
		t.Fatalf("reduce_effort records after a step-less opening prime = %d, want 1 — the session ran "+
			"reduced and the opening prime dropped its attestation because it resolved no step", reduceRecords)
	}

	// (b) the consequence: the treated session's closing step lands in the REDUCED arm. A step_end for
	// the SAME session (production stamps every step record with the current session,
	// telemetry_record.go:181) folds into the aggregate under the session's objective.
	if err := telemetry.AppendEvent(config.TelemetryDir(fx.root), telemetry.StepEvent{
		V:          telemetry.SchemaVersion,
		Event:      telemetry.EventStepEnd,
		Agent:      fx.agent,
		Formula:    formula,
		InstanceID: "inst-stepless",
		StepID:     "bead-stepless",
		StepLabel:  "step-1",
		SessionID:  sessionID,
		Model:      model,
		Status:     telemetry.StatusClosed,
	}); err != nil {
		t.Fatalf("append step_end: %v", err)
	}

	d, _ := telemetry.RebuildLearnedDigests(config.TelemetryDir(fx.root), []string{fx.agent}, formula, "2026-09-09T00:00:00.000Z")
	a, ok := d[formula].Lookup(tokenomics.DigestKey{Formula: formula, StepID: "step-1", Model: model})
	if !ok {
		t.Fatalf("no aggregate for the treated step — the step_end did not fold at all")
	}
	if a.ReducedRuns != 1 {
		t.Errorf("ReducedRuns = %d, want 1 — a session reduced by a step-less opening prime must enter "+
			"the reduced arm; the dropped reduce_effort record left it folded into the baseline", a.ReducedRuns)
	}
}

// TestDispatchCapStaysLocalGated is #678 AC-5: the genuine capacity mechanisms are LOCAL-PHYSICS
// mechanisms, and nothing this issue adds may make them fire where they could not before.
//
// The dispatch gate divides an operator-DECLARED backend pool. A cloud profile declares none, so
// there is nothing to divide and the gate admits with zero arithmetic. The efficiency arm is fully
// armed here — and it has no pool operand at all, which is why it cannot reach this decision.
func TestDispatchCapStaysLocalGated(t *testing.T) {
	t.Run("an undeclared pool still admits with efficiency armed", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armEfficiency(t, fx.root, nil)
		// The cloud shape: a base URL and a per-request window, and deliberately NO pool fact.
		models := `{"default":"codex","models":{"codex":{` +
			`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",` +
			`"ANTHROPIC_AUTH_TOKEN":"tok",` +
			`"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"}}}`
		if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
			t.Fatal(err)
		}
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out,
			dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if out.Len() != 0 {
			t.Errorf("a cloud profile with no declared pool produced gate output at 95%% launcher "+
				"occupancy with the efficiency arm on:\n%s", out.String())
		}
		if recs := dispatchInterventionRecords(t, fx.root, fx.agent); len(recs) != 0 {
			t.Errorf("an inert cloud profile wrote %d dispatch records with efficiency armed, want 0; "+
				"no efficiency code path may reach the pool gate", len(recs))
		}
	})

	t.Run("a declared pool still refuses with efficiency armed", func(t *testing.T) {
		now := time.Now()
		fx := newLifecycleFixture(t)
		armEfficiency(t, fx.root, nil)
		// The local shape: a declared pool, which is the operator fact the gate divides.
		models := `{"default":"lmstudio","models":{"lmstudio":{` +
			`"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234",` +
			`"ANTHROPIC_AUTH_TOKEN":"tok",` +
			`"AF_BACKEND_POOL_TOKENS":"200000"}}}`
		if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
			t.Fatal(err)
		}
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 95, 1000, now.Add(-10*time.Second), now)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out,
			dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore: %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Errorf("a declared pool at 95%% launcher occupancy did not refuse with efficiency "+
				"armed; the capacity mechanism was weakened:\n%s", out.String())
		}
		// And the refusal is still a CAPACITY act. Every efficiency act in this tree carries the
		// efficiency objective, and a pool refusal that started carrying it would fold the two
		// experiments into one number.
		for _, r := range dispatchInterventionRecords(t, fx.root, fx.agent) {
			if r.Objective != telemetry.ObjectiveCapacity {
				t.Errorf("a dispatch pool refusal was recorded objective=%q, want %q",
					r.Objective, telemetry.ObjectiveCapacity)
			}
		}
	})
}

// TestEffortBreadcrumbIsNotStale is the regression test for the defect the Phase 2 review found, and
// the reason it existed is worth stating: every test above asserted that a NON-selecting launch writes
// no breadcrumb into a FRESH fixture. None of them put a breadcrumb there first. The one shape that
// matters — a real factory, where the previous step DID warrant a reduction and the next one does not
// — was the shape nothing covered.
//
// The breadcrumb is an attestation, not a cache. af prime reads it and writes a record saying THIS
// session ran at a reduced level for an efficiency reason. Left stale, the next session attests a
// treatment it never received, into an append-only log, and Phase 7 counts a control run as a firing.
func TestEffortBreadcrumbIsNotStale(t *testing.T) {
	const measured, unmeasured = "step-1", "step-2"

	// A factory where the CLOSING step has learned history and the next one has none, which is what
	// makes the second launch a non-selecting one.
	setup := func(t *testing.T) lifecycleFixture {
		t.Helper()
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		armEfficiency(t, fx.root, nil)
		hookFormulaName(t, fx.workDir, "offpath")
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedEfficiency(t, fx.root, "offpath", measured, model, reducibleAggregate())

		if lvl := effortLevelIn(withEffortLevel(fx.root, fx.workDir, launchEnv(""), measured, "")); lvl != "medium" {
			t.Fatalf("the fixture's FIRST launch selected %q, want medium; without a breadcrumb on disk "+
				"the staleness assertions below prove nothing", lvl)
		}
		return fx
	}

	t.Run("a launch that selects nothing clears the previous launch's attestation", func(t *testing.T) {
		fx := setup(t)

		withEffortLevel(fx.root, fx.workDir, launchEnv(""), unmeasured, "")

		if crumb := readEffortBreadcrumb(fx.workDir); crumb.Level != "" {
			t.Errorf("the breadcrumb still says %+v after a launch that applied no level; the next "+
				"session would attest a treatment it never received", crumb)
		}
	})

	t.Run("turning the arm off clears it too", func(t *testing.T) {
		fx := setup(t)
		armEfficiency(t, fx.root, map[string]any{"effort": "off"})

		// Same step, still warranted by the digest — only the switch changed. This is the control
		// group, and a control group carrying the treatment's own attestation is the one failure that
		// makes the whole experiment unreadable.
		withEffortLevel(fx.root, fx.workDir, launchEnv(""), measured, "")

		if crumb := readEffortBreadcrumb(fx.workDir); crumb.Level != "" {
			t.Errorf("the arm is off and the breadcrumb still says %+v; this session is in the control "+
				"group and would be recorded as treated", crumb)
		}
	})

	t.Run("af prime attests nothing after a cleared breadcrumb", func(t *testing.T) {
		fx := setup(t)
		gateOn(t, fx.root)
		withEffortLevel(fx.root, fx.workDir, launchEnv(""), unmeasured, "")

		origHook := primeHookMode
		primeHookMode = true
		t.Cleanup(func() { primeHookMode = origHook })
		primeWithHookSession(t, "sess-stale")

		records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		for _, r := range records {
			if r.Event == telemetry.EventSessionStart && r.EffortLevel != "" {
				t.Errorf("session_start claims effort_level=%q for a session launched at no chosen "+
					"level", r.EffortLevel)
			}
			if r.Event == telemetry.EventIntervention && r.Action == telemetry.ActionReduceEffort {
				t.Errorf("a reduce_effort record (objective %q, level %q) was written for a session "+
					"that received no reduction", r.Objective, r.EffortLevel)
			}
		}
	})
}

// TestEfficiencyRelaunchWarrantedOnlyByAChange covers boundaryEfficiencyRelaunch's levelChanges
// branch, which had no test at all — every other fixture in this file drives the CleanStart branch.
// That gap is why a comparison between an UNCAPPED plan and a CAPPED applied level survived review:
// the branch containing it was never executed.
//
// What a level-driven relaunch is for: the session is running DEEPER than the next step needs, so
// recycling it into a shallower one saves tokens. Every other shape is a respawn that changes nothing,
// and a bounded mechanism that spends its budget on those disarms itself.
func TestEfficiencyRelaunchWarrantedOnlyByAChange(t *testing.T) {
	plan := func(level string) tokenomics.EfficiencyPlan {
		return tokenomics.EfficiencyPlan{EffortLevel: level}
	}

	for _, tc := range []struct {
		name  string
		crumb effortBreadcrumb
		plan  tokenomics.EfficiencyPlan
		next  string
		want  bool
		why   string
	}{
		{
			name:  "a session running deeper than the plan is recycled",
			crumb: effortBreadcrumb{Level: "high", StepLabel: "step-1"},
			plan:  plan("medium"), next: "step-2", want: true,
			why: "this is the whole point of the branch: high > medium, so the recycle buys a reduction",
		},
		{
			name:  "a profile-capped level is not a change",
			crumb: effortBreadcrumb{Level: "low", StepLabel: "step-1"},
			plan:  plan("medium"), next: "step-2", want: false,
			why: "the launch leg caps its selection by what the profile declares, so a profile " +
				"declaring low under a medium plan applies low again on every relaunch — warranting one " +
				"here recycles the session at every boundary until the cap burns out, six full " +
				"re-primes spent by the mechanism that exists to save them",
		},
		{
			name:  "an equal level is not a change",
			crumb: effortBreadcrumb{Level: "medium", StepLabel: "step-1"},
			plan:  plan("medium"), next: "step-2", want: false,
			why: "the session is already running what the plan asks for",
		},
		{
			name:  "an absent breadcrumb is not a change",
			crumb: effortBreadcrumb{},
			plan:  plan("medium"), next: "step-2", want: false,
			why: "no breadcrumb means the launch leg selected nothing — the arm is off, the step has no " +
				"history, or the leg was skipped for an empty model env. A leg that did not run cannot " +
				"be made to run by recycling into it again",
		},
		{
			name:  "a breadcrumb for the step about to open is not a change",
			crumb: effortBreadcrumb{Level: "high", StepLabel: "step-2"},
			plan:  plan("medium"), next: "step-2", want: false,
			why: "that session was launched targeting this very step, so its level already reflects " +
				"this plan; this is what the breadcrumb carries a step label for",
		},
		{
			name:  "an unranked plan warrants nothing",
			crumb: effortBreadcrumb{Level: "high", StepLabel: "step-1"},
			plan:  plan(config.EffortLevelAuto), next: "step-2", want: false,
			why: "auto is the host's own default and has no position in the order, so no comparison " +
				"against it can prove a reduction",
		},
		{
			name:  "no plan warrants nothing",
			crumb: effortBreadcrumb{Level: "high", StepLabel: "step-1"},
			plan:  plan(""), next: "step-2", want: false,
			why: "there is nothing to move toward",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := reducesEffort(tc.crumb, tc.plan.EffortLevel, tc.next); got != tc.want {
				t.Errorf("reducesEffort(%+v, %q, %q) = %v, want %v\n%s",
					tc.crumb, tc.plan.EffortLevel, tc.next, got, tc.want, tc.why)
			}
		})
	}

	// And the same claim through the real assembler, so the table above is not testing a helper the
	// production path has stopped calling.
	t.Run("the boundary asks reducesEffort", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		armEfficiency(t, fx.root, nil)
		policy := resolvedPolicy(fx.root, mustStartupTokenomics(t, fx.root))

		adm := admission{policy: policy, stepLabel: "step-2", efficiency: plan("medium")}

		writeEffortBreadcrumb(fx.workDir, effortBreadcrumb{Level: "low", StepLabel: "step-1"})
		if eff := boundaryEfficiencyRelaunch(fx.workDir, "inst-1", adm); eff.warranted {
			t.Error("a capped level warranted a relaunch that could not change it")
		}

		writeEffortBreadcrumb(fx.workDir, effortBreadcrumb{Level: "high", StepLabel: "step-1"})
		eff := boundaryEfficiencyRelaunch(fx.workDir, "inst-1", adm)
		if !eff.warranted {
			t.Fatal("a session running deeper than the plan was not recycled; the refusal above proves nothing")
		}
		if eff.mechanism != tokenomics.MechanismEffort {
			t.Errorf("mechanism = %q, want %q — a level change is the effort arm's act, and the level "+
				"is the only thing it has to record", eff.mechanism, tokenomics.MechanismEffort)
		}
		if eff.level != "medium" {
			t.Errorf("level = %q, want medium — the record must name the level the relaunch will "+
				"apply, or the two arms are indistinguishable (D-14)", eff.level)
		}
	})
}

// TestEfficiencyIsNotBilledForCapacitysWork pins efficiencyCausedBoundary. The boundary is a
// disjunction, so an occupancy handoff and a warranted relaunch can be true at the same moment — and
// the relaunch budget is BOUNDED, so every recycle the efficiency arm claims without causing is one it
// cannot make later for a step it would have acted on.
func TestEfficiencyIsNotBilledForCapacitysWork(t *testing.T) {
	cfg := config.StepContextConfig{BoundTokens: 200000, HandoffPct: 75}
	now := boundaryTestNow()
	root := t.TempDir()

	// 92% is past the 75% handoff threshold: this boundary fires on occupancy alone.
	crowded := plantSessionSnapshot(t, root, "manager", "sessa", 92, 1000, now.Add(-10*time.Second), now)
	// 5% is not past anything.
	roomy := plantSessionSnapshot(t, root, "manager", "sessb", 5, 1000, now.Add(-10*time.Second), now)

	quiet := admission{}

	t.Run("an occupancy handoff is not billed to efficiency", func(t *testing.T) {
		if !shouldBoundaryHandoff(crowded, cfg, false, true, false, false) {
			t.Fatal("the fixture does not fire on occupancy alone, so the assertion below proves nothing")
		}
		if efficiencyCausedBoundary(crowded, cfg, false, quiet) {
			t.Error("a boundary that fires at 92% occupancy with no efficiency operand was attributed " +
				"to efficiency; the arm would claim credit for capacity's recycle and spend a bounded " +
				"budget on a respawn that was going to happen anyway")
		}
	})

	t.Run("a relaunch on an empty window is billed to efficiency", func(t *testing.T) {
		if shouldBoundaryHandoff(roomy, cfg, false, true, false, false) {
			t.Fatal("the roomy fixture fires without the efficiency operand, so it cannot show causation")
		}
		// The operand assertion that has to live at a LOW occupancy. Every other one in the tree runs
		// at 95% against a 75% threshold, where `pct >= HandoffPct` carries the disjunction on its own
		// and deleting `efficiencyRelaunch ||` outright leaves them all green.
		if !shouldBoundaryHandoff(roomy, cfg, false, true, false, true) {
			t.Fatal("at 5% of the window the efficiency operand alone did not fire the boundary; " +
				"#678 K6's whole claim is that the relaunch does not need pressure")
		}
		if !efficiencyCausedBoundary(roomy, cfg, false, quiet) {
			t.Error("a boundary at 5% of the window, which nothing else would have fired, was not " +
				"attributed to efficiency — the arm would record none of its own firings")
		}
	})

	t.Run("a capacity no-fit is not billed to efficiency", func(t *testing.T) {
		// The other operand that fires on an empty window: a step whose learned peak does not fit here
		// but would fit a fresh session. It coincides with a warranted relaunch at 5% occupancy, which
		// is exactly the overlap the occupancy case above cannot show.
		noFit := admission{decision: tokenomics.Decision{Verdict: tokenomics.VerdictNoFit}, freshFits: true}
		if !noFit.handoffHelps() {
			t.Fatal("the fixture's admission does not ask for a handoff, so the assertion below proves nothing")
		}
		if efficiencyCausedBoundary(roomy, cfg, false, noFit) {
			t.Error("a boundary capacity had already decided on was billed to efficiency; the arm " +
				"would spend a bounded relaunch on a respawn admission was taking anyway")
		}
	})
}

// TestLaunchLegSkipsStoreWhenDisarmed is MAJOR-2's regression test. nextReadyStepLabel builds an
// issuestore, which in production discovers or spawns the Python MCP server and waits up to 30s for
// it. It is passed as an ARGUMENT to withEffortLevel, and Go evaluates arguments before the call — so
// without its own policy gate, the cost lands on every factory including the ones with tokenomics off,
// which is the default, and on the watchdog respawn path, which did no store I/O at all before #678.
func TestLaunchLegSkipsStoreWhenDisarmed(t *testing.T) {
	countingSeam := func(t *testing.T, built *int) {
		t.Helper()
		orig := newIssueStore
		newIssueStore = func(wd, actor string) (issuestore.Store, error) {
			*built++
			return orig(wd, actor)
		}
		t.Cleanup(func() { newIssueStore = orig })
	}

	t.Run("with the arm off no store is built", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		armEfficiency(t, fx.root, map[string]any{"effort": "off"})

		var built int
		countingSeam(t, &built)
		if got := nextReadyStepLabel(t.Context(), fx.root, fx.workDir); got != "" {
			t.Errorf("nextReadyStepLabel = %q with the arm off, want empty", got)
		}
		if built != 0 {
			t.Errorf("%d issuestore(s) built for a mechanism that is switched off; a watchdog respawn "+
				"would block on this for up to 30s to resolve a label nothing will read", built)
		}
	})

	t.Run("with tokenomics off entirely no store is built", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		// The default: no gate file, no startup block. This is the shape most factories run in.
		var built int
		countingSeam(t, &built)
		nextReadyStepLabel(t.Context(), fx.root, fx.workDir)
		if built != 0 {
			t.Errorf("%d issuestore(s) built on the default off path", built)
		}
	})

	t.Run("with the arm on it resolves the next step", func(t *testing.T) {
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		armEfficiency(t, fx.root, nil)

		var built int
		countingSeam(t, &built)
		got := nextReadyStepLabel(t.Context(), fx.root, fx.workDir)
		if got != "step-1" {
			t.Errorf("nextReadyStepLabel = %q, want step-1 — the refusals above are only meaningful "+
				"if the armed path still resolves a label", got)
		}
		if built == 0 {
			t.Error("the armed path built no store, so the counter above cannot distinguish gated " +
				"from broken")
		}
	})
}

// TestCapacityCounselOnTheNoFitPath covers design-doc L182's one sentence: when a step would not fit
// even a fresh session, af prime says the level was reduced and records it as a CAPACITY act. It is
// the one effort record in the tree that is not an efficiency act, which is exactly why it needs its
// own assertion — a copy-paste that filed it as efficiency would inflate this issue's own numbers with
// a step nothing can hold.
func TestCapacityCounselOnTheNoFitPath(t *testing.T) {
	const counsel = "Reasoning effort is reduced for a step this size"

	// A step whose learned peak overruns the window even from EMPTY. advisoryNoFitPeak is deliberately
	// not reused: at 150,000 of a 200,000-token window with a 10% margin the step still fits a session
	// that had just started, which is the handoff branch rather than this one.
	const noFreshFitPeak = 190000

	setup := func(t *testing.T, extra map[string]any) (lifecycleFixture, string) {
		t.Helper()
		fx, _, step := primedFixture(t, advisoryOccupancyPct)
		gateOn(t, fx.root)
		armEfficiency(t, fx.root, extra)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, noFreshFitPeak, advisoryPriorRuns)
		// What the launch leg leaves behind when the capacity last resort fires. The sentence attests
		// to a level that was actually applied, so without this the fixture is the first-session shape
		// the last subtest covers rather than the one this one is about.
		writeEffortBreadcrumb(fx.workDir, effortBreadcrumb{
			Level:     "medium",
			Objective: string(tokenomics.ObjectiveCapacity),
			StepLabel: stepLabelOf(step),
		})
		return fx, model
	}

	t.Run("the sentence and its record are capacity", func(t *testing.T) {
		fx, _ := setup(t, nil)

		out := runPrimeCapturing(t)
		if !strings.Contains(out, counsel) {
			t.Fatalf("the no-fit path did not say the level was reduced:\n%s", out)
		}

		effort := interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismEffort)]
		if len(effort) != 1 {
			t.Fatalf("effort intervention records = %d, want exactly 1", len(effort))
		}
		if effort[0].Objective != telemetry.ObjectiveCapacity {
			t.Errorf("objective = %q, want %q — this reduction is because no session on this profile "+
				"can hold the step, which is a capacity fact, and filing it as efficiency would credit "+
				"#678 with work it did not do", effort[0].Objective, telemetry.ObjectiveCapacity)
		}
		if effort[0].Action != telemetry.ActionAdvise {
			t.Errorf("action = %q, want %q — af prime counsels, it does not recycle",
				effort[0].Action, telemetry.ActionAdvise)
		}
	})

	t.Run("with the effort arm off the sentence is withheld", func(t *testing.T) {
		fx, _ := setup(t, map[string]any{"effort": "off"})

		out := runPrimeCapturing(t)
		if strings.Contains(out, counsel) {
			t.Errorf("the arm is off and the session was still told its effort was reduced; no level "+
				"was applied, so the sentence describes a treatment it never received:\n%s", out)
		}
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismEffort)]); got != 0 {
			t.Errorf("effort intervention records = %d with the arm off", got)
		}
	})

	t.Run("a session that was launched at no chosen level is not told this", func(t *testing.T) {
		fx, _ := setup(t, nil)
		// The first session of a formula instance: nothing has closed a step, so the launch leg could
		// resolve no formula, read no learned peak, and applied nothing. The step still does not fit —
		// the sentence's REASON is true — but the reduction it reports never happened.
		if err := os.Remove(effortBreadcrumbPath(fx.workDir)); err != nil {
			t.Fatalf("removing the breadcrumb: %v", err)
		}

		out := runPrimeCapturing(t)
		if strings.Contains(out, counsel) {
			t.Errorf("a session running at the host default was told its reasoning effort had been "+
				"reduced:\n%s", out)
		}
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismEffort)]); got != 0 {
			t.Errorf("effort intervention records = %d for a reduction that never happened", got)
		}
	})

	t.Run("a breadcrumb for a different step is not this step's attestation", func(t *testing.T) {
		fx, _ := setup(t, nil)
		writeEffortBreadcrumb(fx.workDir, effortBreadcrumb{
			Level:     "medium",
			Objective: string(tokenomics.ObjectiveCapacity),
			StepLabel: "step-2",
		})

		if out := runPrimeCapturing(t); strings.Contains(out, counsel) {
			t.Errorf("a level applied for a neighbouring step was reported as this step's:\n%s", out)
		}
	})

	t.Run("a step that WOULD fit a fresh session is not told this", func(t *testing.T) {
		fx, _, step := primedFixture(t, advisoryOccupancyPct)
		gateOn(t, fx.root)
		armEfficiency(t, fx.root, nil)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		// Fits an empty window, does not fit the current one: the handoff branch, not this one.
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, 100000, advisoryPriorRuns)
		writeEffortBreadcrumb(fx.workDir, effortBreadcrumb{
			Level:     "medium",
			Objective: string(tokenomics.ObjectiveCapacity),
			StepLabel: stepLabelOf(step),
		})

		if out := runPrimeCapturing(t); strings.Contains(out, counsel) {
			t.Errorf("a step that fits a fresh session was told no session can hold it:\n%s", out)
		}
	})
}

// TestRecordObjectiveRefusesAnUnknownLabel pins the import edge at the one place it is enforced rather
// than assumed. internal/telemetry must never learn what a decision looks like, so a breadcrumb
// written by a binary whose vocabulary this one does not share must produce no record at all — an
// absent record is a gap, a wrong one is a false claim about which arm a session ran in.
func TestRecordObjectiveRefusesAnUnknownLabel(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{string(tokenomics.ObjectiveEfficiency), telemetry.ObjectiveEfficiency},
		{string(tokenomics.ObjectiveCapacity), telemetry.ObjectiveCapacity},
		{"", ""},
		{"throughput", ""},
		{"Efficiency", ""},
	} {
		if got := recordObjective(tc.in); got != tc.want {
			t.Errorf("recordObjective(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Non-vacuity: the two recognised spellings must actually differ, or the table above would pass
	// against a function that returned one constant.
	if telemetry.ObjectiveEfficiency == telemetry.ObjectiveCapacity {
		t.Fatal("the two objectives spell the same string; nothing downstream can tell the arms apart")
	}
}

// TestBoundarySuccessorIsSlimmed is K8(a), the half of the re-prime reduction that had no test at
// all. K8(b) — the second prime of one session — is covered above; this is the other one, and it is
// the harder of the two to see: the session that inherits a boundary handoff is STARTING a step, not
// resuming one, so the #668 K16 same-step rule cannot reach it and it re-received every section of a
// brief that had just been written FOR it.
func TestBoundarySuccessorIsSlimmed(t *testing.T) {
	brief := func(currentStep, nextStep string) *checkpoint.Checkpoint {
		return &checkpoint.Checkpoint{
			CurrentStep:      currentStep,
			ResumeNextStepID: nextStep,
			ResumeNextAction: "open step-2 and read the formula",
			ResumeVerified:   "step-1 closed clean",
			ModifiedFiles:    []string{"internal/cmd/prime.go"},
			StepTitle:        "Step One",
			FormulaID:        "inst-1",
		}
	}

	t.Run("the rule", func(t *testing.T) {
		for _, tc := range []struct {
			name                    string
			cp                      *checkpoint.Checkpoint
			resuming, priming       string
			interviewOn             bool
			wantSlim, wantSuccessor bool
			why                     string
		}{
			{
				name: "the successor of a boundary handoff is slimmed",
				cp:   brief("step-1", "step-2"), priming: "step-2", interviewOn: true,
				wantSlim: true, wantSuccessor: true,
				why: "this is K8(a) itself: the recycling session wrote the brief naming the step it " +
					"would not get to, and this session is starting exactly that step",
			},
			{
				name: "the interview switch gates it",
				cp:   brief("step-1", "step-2"), priming: "step-2", interviewOn: false,
				wantSlim: false, wantSuccessor: false,
				why: "with tokenomics off — the default — a factory must see exactly what it saw yesterday",
			},
			{
				name: "a brief for a DIFFERENT step is not slimmed",
				cp:   brief("step-1", "step-3"), priming: "step-2", interviewOn: true,
				wantSlim: false, wantSuccessor: false,
				why: "the brief describes work this session is not about to do",
			},
			{
				name: "a brief with no next step id matches nothing",
				cp:   brief("step-2", ""), priming: "step-2", interviewOn: true,
				wantSlim: false, wantSuccessor: false,
				why: "absence must never arm an action, and here the action is withholding context. " +
					"Keying on CurrentStep instead would slim on this very row, for the step the " +
					"RECYCLING session was on",
			},
			{
				name: "a same-step resume is slimmed by the OLD rule, ungated",
				cp:   brief("step-1", "step-2"), resuming: "step-1", priming: "step-1", interviewOn: false,
				wantSlim: true, wantSuccessor: false,
				why: "#668 K16 shipped before the switch had any readers and its behaviour is not this " +
					"issue's to change; successor must read false so the record is not attributed to K8(a)",
			},
			{
				name:    "a checkpoint with no brief is never slimmed",
				cp:      &checkpoint.Checkpoint{CurrentStep: "step-1", ResumeNextStepID: "step-2"},
				priming: "step-2", interviewOn: true,
				wantSlim: false, wantSuccessor: false,
				why: "the only thing that licenses dropping a section is a brief that already says what " +
					"the section says",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				slim, successor := checkpointSlims(tc.cp, tc.resuming, tc.priming, tc.interviewOn)
				if slim != tc.wantSlim || successor != tc.wantSuccessor {
					t.Errorf("checkpointSlims = (slim %v, successor %v), want (%v, %v)\n%s",
						slim, successor, tc.wantSlim, tc.wantSuccessor, tc.why)
				}
			})
		}
	})

	// And through the renderer, so the table above is not testing a decision nothing consumes.
	t.Run("the sections it withholds", func(t *testing.T) {
		const marker = "**Modified files:**"

		render := func(t *testing.T, interviewOn bool) (string, bool) {
			t.Helper()
			dir := t.TempDir()
			cp := brief("step-1", "step-2")
			cp.Timestamp = time.Now()
			if err := checkpoint.Write(dir, cp); err != nil {
				t.Fatalf("checkpoint.Write: %v", err)
			}
			var buf bytes.Buffer
			successor := outputCheckpointContext(&buf, dir, "", "step-2", interviewOn)
			return buf.String(), successor
		}

		full, successorOff := render(t, false)
		if successorOff {
			t.Error("outputCheckpointContext reported a K8(a) reduction with the interview switch off")
		}
		if !strings.Contains(full, marker) {
			t.Fatalf("the unslimmed block carries no %q, so its absence below proves nothing:\n%s", marker, full)
		}

		slimmed, successorOn := render(t, true)
		if !successorOn {
			t.Error("the successor of a boundary handoff was not reported as reduced, so no interview " +
				"record is written for a reduction that happened")
		}
		if strings.Contains(slimmed, marker) {
			t.Errorf("the successor re-received the modified-file list its own brief already carries "+
				"as **Artifacts**:\n%s", slimmed)
		}
		if !strings.Contains(slimmed, "**Next action:**") {
			t.Errorf("the slimmed block dropped the brief itself; the brief is what the previous "+
				"session went to the trouble of writing down:\n%s", slimmed)
		}
		if len(slimmed) >= len(full) {
			t.Errorf("the slimmed block is not smaller: %d bytes vs %d", len(slimmed), len(full))
		}
	})
}

// TestEfficiencyRelaunchRecordAtTheBoundary drives the whole af done leg, because everything else in
// this file stops at the assembler. The mutation that motivates it: deleting the efficiency
// enforcement record from done.go left the suite green, which means the record #678 AC-4 asks for was
// asserted nowhere — and a record nothing asserts is a record that can silently stop being written,
// leaving Phase 7 to measure an actuator it cannot see fire.
func TestEfficiencyRelaunchRecordAtTheBoundary(t *testing.T) {
	// Well below any handoff threshold, and no appetite is seeded: nothing but the efficiency operand
	// can fire this boundary, which is what makes the record's objective checkable. The crowded
	// occupancy is the opposite fixture — past the threshold, where the boundary fires on its own.
	const quietOccupancyPct, crowdedOccupancyPct = 30.0, 95.0

	arm := func(t *testing.T, occupancyPct float64) (lifecycleFixture, *boundaryRecorder) {
		t.Helper()
		fx := newLifecycleFixture(t)
		now := boundaryTestNow()
		epic, step := seedTwoStepBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", occupancyPct, 1000, now.Add(-10*time.Second), now)
		armEfficiency(t, fx.root, nil)

		next := nextStepLabel(t, fx, epic.ID, step.ID)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedEfficiency(t, fx.root, "offpath", next, model, reducibleAggregate())
		// The session is running deeper than the next step's plan asks for, which is the one shape a
		// level-driven relaunch acts on.
		writeEffortBreadcrumb(fx.workDir, effortBreadcrumb{Level: "high", StepLabel: stepLabelOf(step)})

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		return fx, (&boundaryRecorder{}).install(t)
	}

	t.Run("it recycles and records", func(t *testing.T) {
		fx, rec := arm(t, quietOccupancyPct)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("runDoneCore: %v", err)
		}
		if rec.calls != 1 {
			t.Fatalf("boundary respawns = %d, want 1 — at %v%% occupancy nothing but the efficiency "+
				"relaunch can fire this boundary, so a 0 means the operand is not wired into af done",
				rec.calls, quietOccupancyPct)
		}

		effort := interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismEffort)]
		if len(effort) != 1 {
			t.Fatalf("effort intervention records = %d, want exactly 1", len(effort))
		}
		got := effort[0]
		if got.Action != telemetry.ActionHandoff {
			t.Errorf("action = %q, want %q — a recycle is an act, not counsel", got.Action, telemetry.ActionHandoff)
		}
		if got.Objective != telemetry.ObjectiveEfficiency {
			t.Errorf("objective = %q, want %q", got.Objective, telemetry.ObjectiveEfficiency)
		}
		if got.EffortLevel != "medium" {
			t.Errorf("effort_level = %q, want medium — the record must name the level the relaunch "+
				"applies, or the two efficiency arms are indistinguishable downstream", got.EffortLevel)
		}
		if got.Mechanism == string(tokenomics.MechanismBudget) {
			t.Error("the relaunch was filed under budget; a budget record claims the window would not " +
				"fit, and this one fired at 30% of it")
		}
	})

	t.Run("the bound is charged", func(t *testing.T) {
		fx, _ := arm(t, quietOccupancyPct)
		before := loadEfficiencyRelaunches(fx.workDir, readHookedFormulaID(fx.workDir))

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("runDoneCore: %v", err)
		}

		after := loadEfficiencyRelaunches(fx.workDir, readHookedFormulaID(fx.workDir))
		if after != before+1 {
			t.Errorf("the relaunch ledger went %d -> %d, want +1; an uncharged relaunch makes "+
				"efficiency_max_relaunches unenforceable and the actuator unbounded", before, after)
		}
	})

	// The causation gate, asked where it actually lives rather than at the helper. This is the shape
	// that survives a helper-only test: delete efficiencyCausedBoundary from af done's call site and
	// every assertion above still passes, because at 30%% occupancy the two answers coincide.
	t.Run("a boundary occupancy would have fired anyway is not billed to efficiency", func(t *testing.T) {
		fx, rec := arm(t, crowdedOccupancyPct)
		before := loadEfficiencyRelaunches(fx.workDir, readHookedFormulaID(fx.workDir))

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("runDoneCore: %v", err)
		}
		if rec.calls != 1 {
			t.Fatalf("boundary respawns = %d at %v%% occupancy, want 1; without a handoff there is no "+
				"attribution to get wrong", rec.calls, crowdedOccupancyPct)
		}

		if got := interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismEffort)]; len(got) != 0 {
			t.Errorf("effort intervention records = %d for a handoff the occupancy rule had already "+
				"decided on; the efficiency arm is claiming credit for capacity's recycle", len(got))
		}
		if after := loadEfficiencyRelaunches(fx.workDir, readHookedFormulaID(fx.workDir)); after != before {
			t.Errorf("the relaunch ledger went %d -> %d for a respawn that was going to happen anyway; "+
				"a bounded budget spent here is a step later in the formula the arm can no longer act on",
				before, after)
		}
	})

	t.Run("with the umbrella off it neither recycles nor records", func(t *testing.T) {
		fx, rec := arm(t, quietOccupancyPct)
		if err := os.Remove(tokenomicsGateFile(fx.root)); err != nil {
			t.Fatalf("removing the tokenomics gate: %v", err)
		}

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("runDoneCore: %v", err)
		}
		if rec.calls != 0 {
			t.Errorf("boundary respawns = %d with tokenomics off; #678's baseline is unconditional "+
				"on PRESSURE, never on the operator's switch", rec.calls)
		}
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismEffort)]); got != 0 {
			t.Errorf("effort intervention records = %d with tokenomics off", got)
		}
	})
}
