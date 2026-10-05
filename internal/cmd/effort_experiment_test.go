package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
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

// #668 D16 asked for a per-profile reduced reasoning effort, recorded naming the value applied, so the
// harness could measure whether a cheaper mode finishes the same steps. #678 K5 moved the decision to
// the launch legs: the level is chosen from the step's learned generation baseline, where it can be
// applied to a session that has not started yet rather than to one already running.
//
// What this file still owns is the effort arm's edges: the write boundary's vocabulary, the
// conjunction of gate and mechanism switch that governs the actuator's selection, and a declared
// level passing through whichever way the arm is switched (#707). The boundary's own readout is
// asserted ABSENT below, because a second effort record derived from a profile would double-count
// every relaunch the actuator already recorded at launch.

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

	// The arm switch governs the actuator's SELECTION, never the operator's configuration (#707). Since
	// #678 K5 the treatment is a level the actuator chooses; a level the profile declares is the
	// operator's, and the records already tell the two apart — only a launch that selected exports the
	// attestation af prime attests from, and every other launch exports it empty.
	t.Run("a declared level passes through whichever way the arm is switched", func(t *testing.T) {
		declared := []config.EnvVar{
			{Key: "ANTHROPIC_MODEL", Value: "claude-opus-5"},
			{Key: config.EnvEffortLevel, Value: "low"},
		}
		unattested := append(slices.Clone(declared),
			config.EnvVar{Key: config.EnvEffortObjective},
			config.EnvVar{Key: config.EnvEffortStepLabel},
			config.EnvVar{Key: config.EnvEffortFormula})

		t.Run("off keeps the declared level", func(t *testing.T) {
			fx := newLifecycleFixture(t)
			gateOn(t, fx.root)
			armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "off"})

			got := withEffortLevel(fx.root, fx.workDir, declared, "", "")
			if !slices.Equal(got, unattested) {
				t.Errorf("the arm is off and the launch env became %v, want the profile's %v unchanged "+
					"beside an empty attestation; an operator who turned tokenomics off would lose the level "+
					"they configured", got, unattested)
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

		t.Run("the umbrella off keeps the declared level", func(t *testing.T) {
			// The umbrella off is the posture a fresh factory starts in (no .tokenomics file), so this
			// is the case that decides whether a declared level works at all out of the box.
			fx := newLifecycleFixture(t)
			armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "on"})
			// armAdvisoryPolicy turns the gate on as part of arming, so the umbrella is closed here
			// explicitly. startup.json is left saying "effort: on" on purpose: the two switches are a
			// conjunction, and this is the leg that proves the mechanism's own switch cannot carry it.
			if err := os.WriteFile(tokenomicsGateFile(fx.root), []byte("off\n"), 0o644); err != nil {
				t.Fatalf("close the tokenomics gate: %v", err)
			}

			if got := withEffortLevel(fx.root, fx.workDir, declared, "", ""); !slices.Equal(got, unattested) {
				t.Errorf("af tokenomics is off at the gate and the launch env became %v, want the "+
					"profile's %v unchanged beside an empty attestation", got, unattested)
			}
		})
	})
}

// TestEffortArmWiredAtEveryModelEnvSite pins the WIRING, which the subtests above cannot: they drive
// withEffortLevel directly, so deleting the wrapper from a call site leaves them all green while
// that leg silently stops applying the actuator's selection. It is invisible in a unit test of the
// helper.
//
// #678 K5 renamed the wrapper and widened what it does — it SELECTS a level from the step's learned
// baseline, capped at the declared one — and a launch leg that misses it loses the treatment entirely
// while still launching sessions that look like steps which simply warranted no reduction. The
// literal below was updated with the rename and must never be relaxed to a substring that both
// spellings satisfy.
//
// A source read rather than a launch, because the claim is "no production site sets the model env
// unfiltered" — a universal over call sites, which no single launch can witness. Same idiom, and same
// reasoning, as TestSubagentScanStaysOffTheRenderPath.
func TestEffortArmWiredAtEveryModelEnvSite(t *testing.T) {
	all := grepPackage(t, ".", ".ModelEnv = ")
	if len(all) == 0 {
		t.Fatal("no production file in package cmd assigns a launch's ModelEnv; this interlock is scanning " +
			"the wrong tree and would stay green with every launch exporting the effort key unfiltered")
	}
	armedAt := map[string]bool{}
	for _, hit := range grepPackage(t, ".", ".ModelEnv = withEffortLevel(") {
		armedAt[hit] = true
	}
	for _, hit := range all {
		if !armedAt[hit] {
			t.Errorf("%s sets the model env without withEffortLevel; the step's learned baseline "+
				"would never lower %s on that leg, and a relaunch through it would leave the previous "+
				"session's attestation standing for af prime. Wrap it, or if this site genuinely cannot carry an "+
				"agent's profile, say why in the SAME change", hit, config.EnvEffortLevel)
		}
		if filepath.Base(strings.Split(hit, ":")[0]) != "launch_contributions.go" {
			t.Errorf("%s assigns a launch's ModelEnv outside the launch composer; every leg must reach "+
				"the model env through launchContributions so no leg can skip the selection", hit)
		}
	}
	// A keyed literal would set the field without an assignment this scan can see.
	if hits := grepPackage(t, ".", "ModelEnv:"); len(hits) != 0 {
		t.Errorf("%v set ModelEnv through a struct-literal key, which bypasses this interlock", hits)
	}
}
