package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// modelsRoot is a bare root with the directory models.json lives in already made. Every save below
// is asserting about VALIDATION, and a save that failed for want of a directory would read as a
// rejection this feature never made — which is exactly how the accepted-values subtest would look
// if it went green for the wrong reason.
func modelsRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(config.ModelsConfigPath(root)), 0o755); err != nil {
		t.Fatalf("create the factory config dir: %v", err)
	}
	return root
}

// #668 D16: the effort-reduction experiment arm.
//
// It is an ARM, not a policy. design-doc.md's D16 row asks for a per-profile reduced reasoning
// effort carried on the boundary-relaunch leg and recorded naming the value applied, so the harness
// can measure whether a cheaper mode finishes the same steps. Without the record the experiment has
// no readout: two runs at different effort levels would be indistinguishable in the data.
//
// #678 K5 re-homed the DECISION and left the arm. The level is no longer read off a profile at a
// boundary relaunch — it is chosen from the step's learned generation baseline at the launch legs,
// where it can be applied to a session that has not started yet rather than to one already running.
// What this file still owns is the arm's edges, which are unchanged by that move: the write boundary's
// vocabulary, the conjunction of gate and mechanism switch, and the rule that a control session must
// never receive the treatment. The boundary's own readout is now asserted ABSENT below, because a
// second effort record derived from a profile would double-count every relaunch the new actuator
// already recorded at launch.

// effortProfile writes a models.json whose profile for this fixture's agent declares an effort
// level, through the real saver so a value the write boundary would reject fails here.
func effortProfile(t *testing.T, root, agent, level string) {
	t.Helper()
	cfg := &config.ModelsConfig{
		Models: map[string]map[string]string{
			"thrifty": {"ANTHROPIC_MODEL": "claude-opus-5", config.EnvEffortLevel: level},
		},
		Agents:  map[string]string{agent: "thrifty"},
		Default: "thrifty",
	}
	if err := config.SaveModelsConfig(config.ModelsConfigPath(root), cfg); err != nil {
		t.Fatalf("SaveModelsConfig: %v", err)
	}
}

// TestEffortExperiment is D16's four legs: the profile is accepted, a value outside the host's
// vocabulary is refused, an inherited value cannot survive a profile that declares none, and — since
// #678 K5 moved the decision to the launch legs — a boundary relaunch records NO profile-derived
// effort level. TestEffortSelectedAtLaunchLegs owns the leg that does record one.
func TestEffortExperiment(t *testing.T) {
	t.Run("a profile may declare a reduced reasoning effort", func(t *testing.T) {
		root := modelsRoot(t)
		effortProfile(t, root, "manager", "low")

		cfg, err := config.LoadModelsConfig(root)
		if err != nil {
			t.Fatalf("LoadModelsConfig: %v", err)
		}
		_, env, ok, err := config.ResolveModelEnv(cfg, "manager", "", "", "")
		if err != nil || !ok {
			t.Fatalf("ResolveModelEnv: ok=%v err=%v", ok, err)
		}
		var found bool
		for _, kv := range env {
			if kv.Key == config.EnvEffortLevel {
				found = true
				if kv.Value != "low" {
					t.Errorf("%s = %q on the launch line, want %q", config.EnvEffortLevel, kv.Value, "low")
				}
			}
		}
		if !found {
			t.Errorf("the declared effort level does not ride the launch line: %+v", env)
		}
	})

	t.Run("a value outside the host's vocabulary is refused at the write boundary", func(t *testing.T) {
		// "maximum" is the plausible mistake: the host's word is "max", and a value it does not
		// recognise is dropped in favour of its default — so a profile saved with this would run at
		// FULL effort while the operator's file and this feature's records both claim otherwise.
		for _, bad := range []string{"maximum", "LOW", "1", "high ", "xhigh\n"} {
			root := modelsRoot(t)
			err := config.SaveModelsConfig(config.ModelsConfigPath(root), &config.ModelsConfig{
				Models: map[string]map[string]string{"thrifty": {config.EnvEffortLevel: bad}},
			})
			if err == nil {
				t.Errorf("%s=%q was accepted; the host would silently ignore it", config.EnvEffortLevel, bad)
				continue
			}
			if !errors.Is(err, config.ErrInvalidType) {
				t.Errorf("%s=%q rejected with %v, want an ErrInvalidType", config.EnvEffortLevel, bad, err)
			}
			for _, want := range []string{"thrifty", config.EnvEffortLevel, bad} {
				if !strings.Contains(err.Error(), strings.TrimRight(want, " \n")) {
					t.Errorf("the rejection does not name %q: %v", want, err)
				}
			}
			// The operator has to be able to fix the file without consulting the docs, which is the
			// same standard the compaction-window bounds are held to (models_test.go:215-217).
			if !strings.Contains(err.Error(), "max") || !strings.Contains(err.Error(), "low") {
				t.Errorf("the rejection does not name the accepted values: %v", err)
			}
		}
	})

	t.Run("every value the host documents is accepted, and so is the empty deferral", func(t *testing.T) {
		for _, good := range []string{"low", "medium", "high", "xhigh", "max", "auto", ""} {
			root := modelsRoot(t)
			if err := config.SaveModelsConfig(config.ModelsConfigPath(root), &config.ModelsConfig{
				Models: map[string]map[string]string{"thrifty": {config.EnvEffortLevel: good}},
			}); err != nil {
				t.Errorf("%s=%q was refused: %v", config.EnvEffortLevel, good, err)
			}
		}
	})

	// #678 K5's deletion, asserted under the conditions MOST favourable to the readout it removes: the
	// gate open, the effort arm on, a profile declaring a level, and an occupancy that genuinely fires
	// the boundary. Those are exactly the inputs that used to produce an effort/reduce_effort record
	// here, so a re-introduction — of boundaryEffortLevel, or of any other read of the profile at this
	// seam — fails this subtest rather than surviving as a second record beside the launch leg's.
	//
	// Deleted rather than kept as a zero-assertion: the two subtests this replaces asserted that an
	// arm-off and a no-profile relaunch recorded nothing, and with the code path gone both would pass
	// on a tree where the whole boundary was broken.
	t.Run("a boundary relaunch records no profile-derived effort level", func(t *testing.T) {
		now := boundaryTestNow()
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "on"})
		armBoundaryFixture(t, fx)
		effortProfile(t, fx.root, fx.agent, "low")
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 88, 1000, now.Add(-10*time.Second), now)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		captureStdout(t, func() {
			if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
				t.Fatalf("af done: %v", err)
			}
		})
		if rec.calls != 1 {
			t.Fatalf("boundary handoff executed %d times, want 1; with no relaunch this subtest could "+
				"not tell a deleted readout from a boundary that never fired", rec.calls)
		}

		byMechanism := interventionsByMechanism(t, fx.root, fx.agent)
		for _, ev := range byMechanism[string(tokenomics.MechanismEffort)] {
			if ev.Action == telemetry.ActionReduceEffort {
				t.Errorf("the boundary wrote effort/reduce_effort (level %q); #678 K5 moved that "+
					"decision to the launch legs, and a second record here double-counts every "+
					"relaunch the launch leg already recorded", ev.EffortLevel)
			}
		}
		// This boundary is occupancy-driven, so no budget/handoff record is expected here — that record
		// belongs to an admission-driven handoff and TestBoundaryAdmission owns it. What is worth
		// pinning at THIS seam is that the deletion above did not turn the capacity readout into a
		// second efficiency one: a budget record carrying the efficiency objective would credit this
		// issue with every recycle the capacity half has always done.
		for _, ev := range byMechanism[string(tokenomics.MechanismBudget)] {
			if ev.Objective == telemetry.ObjectiveEfficiency {
				t.Errorf("a budget record was written with objective=efficiency (action %q); capacity "+
					"acts stay capacity acts (#678 Gap 19)", ev.Action)
			}
		}
	})

	// The helper the readout above used to call. A source read, because "it no longer exists" is a
	// claim about the package and not about any one execution — and the compiler only enforces it while
	// something still calls it.
	t.Run("boundaryEffortLevel no longer exists", func(t *testing.T) {
		if hits := grepPackage(t, ".", "func boundaryEffortLevel("); len(hits) != 0 {
			t.Errorf("boundaryEffortLevel is back at %v; the profile is no longer the boundary's "+
				"source for an effort level (#678 K5)", hits)
		}
	})

	// The arm switch has to govern the TREATMENT, not merely the bookkeeping. design-doc.md:330 says
	// the relaunch env carries the reduced setting "only when the policy arm is enabled", and it says
	// so because an experiment whose control group receives the treatment measures nothing: gating the
	// record alone would run every relaunch reduced and record half of them.
	t.Run("the arm switch governs the relaunch env, not just the record", func(t *testing.T) {
		declared := []config.EnvVar{
			{Key: "ANTHROPIC_MODEL", Value: "claude-opus-5"},
			{Key: config.EnvEffortLevel, Value: "low"},
		}

		t.Run("off drops the key", func(t *testing.T) {
			fx := newLifecycleFixture(t)
			gateOn(t, fx.root)
			armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "off"})

			got := withEffortLevel(fx.root, fx.workDir, declared, "", "")
			for _, kv := range got {
				if kv.Key == config.EnvEffortLevel {
					t.Errorf("the relaunch still exports %s=%q with the arm off; every respawn runs the "+
						"treatment and Phase 7 has no control group", kv.Key, kv.Value)
				}
			}
			if len(got) != len(declared)-1 {
				t.Errorf("kept %d of %d keys; only the effort key may be dropped", len(got), len(declared))
			}
		})

		t.Run("on keeps it", func(t *testing.T) {
			fx := newLifecycleFixture(t)
			gateOn(t, fx.root)
			armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "on"})

			var found bool
			for _, kv := range withEffortLevel(fx.root, fx.workDir, declared, "", "") {
				if kv.Key == config.EnvEffortLevel && kv.Value == "low" {
					found = true
				}
			}
			if !found {
				t.Error("the arm is on and the relaunch does not carry the declared level; the record " +
					"written beside it would name a level nothing applied")
			}
		})

		t.Run("the umbrella off is the arm off", func(t *testing.T) {
			// The gate file and the mechanism switch are a conjunction, and a treatment that survived
			// an operator turning the whole feature off would be the worst version of this bug: it
			// would be invisible in a factory that never opted in at all.
			fx := newLifecycleFixture(t)
			armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "on"})
			// armAdvisoryPolicy turns the gate on as part of arming, so the umbrella is closed here
			// explicitly. startup.json is left saying "effort: on" on purpose: the two switches are a
			// conjunction, and this is the leg that proves the mechanism's own switch cannot carry it.
			if err := os.WriteFile(tokenomicsGateFile(fx.root), []byte("off\n"), 0o644); err != nil {
				t.Fatalf("close the tokenomics gate: %v", err)
			}

			for _, kv := range withEffortLevel(fx.root, fx.workDir, declared, "", "") {
				if kv.Key == config.EnvEffortLevel {
					t.Error("af tokenomics is off at the gate and the relaunch still carries the effort key")
				}
			}
		})
	})
}

// TestEffortArmWiredAtEveryModelEnvSite pins the WIRING, which the subtests above cannot: they drive
// withEffortLevel directly, so deleting the wrapper from a call site leaves them all green while
// the control arm silently receives the treatment. That is the exact failure design-doc.md:330 rules
// out, and it is invisible in a unit test of the helper.
//
// #678 K5 renamed the wrapper and widened what it does — it now SELECTS a level from the step's
// learned baseline as well as filtering a declared one — which makes this interlock carry more weight
// than it did, not less: a launch leg that misses the wrapper now loses the treatment entirely rather
// than merely leaking a declared level. The literal below was updated with the rename and must never
// be relaxed to a substring that both spellings satisfy.
//
// A source read rather than a launch, because the claim is "no production site sets the model env
// unfiltered" — a universal over call sites, which no single launch can witness. Same idiom, and same
// reasoning, as TestSubagentScanStaysOffTheRenderPath.
func TestEffortArmWiredAtEveryModelEnvSite(t *testing.T) {
	all := grepPackage(t, ".", "mgr.SetModelEnv(")
	if len(all) == 0 {
		t.Fatal("no production file in package cmd calls mgr.SetModelEnv; this interlock is scanning " +
			"the wrong tree and would stay green with every launch exporting the effort key unfiltered")
	}
	armed := grepPackage(t, ".", "mgr.SetModelEnv(withEffortLevel(")
	if len(armed) == len(all) {
		return
	}
	armedAt := map[string]bool{}
	for _, hit := range armed {
		armedAt[hit] = true
	}
	for _, hit := range all {
		if !armedAt[hit] {
			t.Errorf("%s sets the model env without withEffortLevel; a profile declaring %s would "+
				"then reach a session whose D16 arm is off, and the step's learned baseline would "+
				"reach nothing at all. Wrap it, or if this site genuinely cannot carry an agent's "+
				"profile, say why in the SAME change", hit, config.EnvEffortLevel)
		}
	}
}
