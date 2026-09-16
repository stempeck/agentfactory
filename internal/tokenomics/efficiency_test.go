package tokenomics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// efficiencyPolicy is the posture every efficiency test runs under unless it says otherwise:
// umbrella on, efficiency at its shipped default, the four knobs stated so a test reads its own
// thresholds rather than inheriting them.
func efficiencyPolicy(minRuns, sharePct, repeatFloor int) Policy {
	return ResolvePolicy(true, config.TokenomicsConfig{
		Enabled: "on", Budget: "default", Thrift: "default", Dispatch: "default",
		Interview: "default", Effort: "default", Escalate: "default",
		Efficiency: "default", EfficiencyEffortLevel: "medium",
		EfficiencyThinkingSharePct: sharePct,
		EfficiencyRepeatReadFloor:  repeatFloor,
		EfficiencyMaxRelaunches:    6,
		LearnedMinRuns:             minRuns,
	})
}

// generationBaseline is an aggregate that fires: trusted on generation runs, exact share 91 %.
func generationBaseline() Aggregate {
	return Aggregate{
		Runs:              5,
		GenerationRuns:    5,
		MedianOutTokens:   1_000,
		MedianThinkTokens: 910,
		SessionsPerStep:   1,
	}
}

// TestEfficiencyTriggerIgnoresWindowPressure is #678 AC-2, and the phase's own acceptance criterion.
//
// The claim is an ABSENCE — that no window reading and no occupancy reading can switch efficiency
// off — and an absence cannot be established by any behavioural row, because a function that
// consulted a window would still return the right answer for whatever window the test happened to
// choose. The structural subtest at the bottom is therefore the load-bearing one: it reads the
// declaration of Efficiency and asserts its parameter list cannot name such a reading. The
// behavioural rows above it prove the arithmetic that absence leaves behind is the design's.
func TestEfficiencyTriggerIgnoresWindowPressure(t *testing.T) {
	t.Run("a trusted generation baseline fires", func(t *testing.T) {
		plan := Efficiency(generationBaseline(), true, efficiencyPolicy(2, 80, 1))
		if plan.EffortLevel != "medium" {
			t.Fatalf("EffortLevel = %q, want %q (reason %q); the exact share is 910*100/1000 = 91, over the 80 threshold",
				plan.EffortLevel, "medium", plan.Reason)
		}
		if plan.Reason != "" {
			t.Errorf("Reason = %q, want empty: a plan that fires has no reason to carry", plan.Reason)
		}
		if plan.Inputs.ThinkingSharePct != 91 {
			t.Errorf("Inputs.ThinkingSharePct = %d, want 91; the share must be exact integer arithmetic, not a rounded float",
				plan.Inputs.ThinkingSharePct)
		}
	})

	t.Run("the share is integer arithmetic at the boundary", func(t *testing.T) {
		p := efficiencyPolicy(2, 80, 1)

		at := generationBaseline()
		at.MedianThinkTokens = 800
		if plan := Efficiency(at, true, p); plan.EffortLevel == "" {
			t.Errorf("share 80 against threshold 80 declined with %q; the boundary admits", plan.Reason)
		}

		below := generationBaseline()
		below.MedianThinkTokens = 799
		if plan := Efficiency(below, true, p); plan.Reason != ReasonNoGain {
			t.Errorf("share 79 against threshold 80: Reason = %q, want %q", plan.Reason, ReasonNoGain)
		}
	})

	t.Run("a large run count cannot buy generation trust", func(t *testing.T) {
		// Runs is deliberately enormous and GenerationRuns deliberately below the floor. This row is
		// the whole of cross-review HIGH-1: a trust floor keyed on Runs passes it, and a key whose
		// history predates the generation fields would be trusted on occupancy history alone.
		a := generationBaseline()
		a.Runs = 100
		a.GenerationRuns = 1

		plan := Efficiency(a, true, efficiencyPolicy(2, 80, 1))
		if plan.Reason != ReasonBelowMinRuns {
			t.Fatalf("Reason = %q, want %q; the trust floor must read GenerationRuns (1), never Runs (100)",
				plan.Reason, ReasonBelowMinRuns)
		}
		if plan.EffortLevel != "" {
			t.Errorf("EffortLevel = %q, want empty on an untrusted key", plan.EffortLevel)
		}
	})

	t.Run("no generation baseline is unmeasured, never zero", func(t *testing.T) {
		p := efficiencyPolicy(2, 80, 1)

		noThink := generationBaseline()
		noThink.MedianThinkTokens = 0
		if plan := Efficiency(noThink, true, p); plan.Reason != ReasonNoGenerationBaseline {
			t.Errorf("median think tokens 0: Reason = %q, want %q", plan.Reason, ReasonNoGenerationBaseline)
		}

		noOut := generationBaseline()
		noOut.MedianOutTokens = 0
		if plan := Efficiency(noOut, true, p); plan.Reason != ReasonNoGenerationBaseline {
			t.Errorf("median out tokens 0: Reason = %q, want %q", plan.Reason, ReasonNoGenerationBaseline)
		}
	})

	t.Run("an unlearned key observes", func(t *testing.T) {
		if plan := Efficiency(generationBaseline(), false, efficiencyPolicy(2, 80, 1)); plan.Reason != ReasonNoLearnedData {
			t.Errorf("found=false: Reason = %q, want %q", plan.Reason, ReasonNoLearnedData)
		}
	})

	t.Run("the switch is absolute", func(t *testing.T) {
		off := ResolvePolicy(false, config.TokenomicsConfig{
			Enabled: "on", Efficiency: "on", EfficiencyEffortLevel: "medium",
			EfficiencyThinkingSharePct: 80, EfficiencyRepeatReadFloor: 1, LearnedMinRuns: 2,
		})
		if plan := Efficiency(generationBaseline(), true, off); plan.Reason != ReasonMechanismOff {
			t.Errorf("umbrella off with efficiency explicitly on: Reason = %q, want %q", plan.Reason, ReasonMechanismOff)
		}
		// Control. Without it the row above is satisfied by a function that never fires at all.
		if plan := Efficiency(generationBaseline(), true, efficiencyPolicy(2, 80, 1)); plan.Reason != "" {
			t.Errorf("control: the same aggregate under an ON policy declined with %q", plan.Reason)
		}
	})

	t.Run("clean start reads the learned session count", func(t *testing.T) {
		p := efficiencyPolicy(2, 80, 1)

		spanned := generationBaseline()
		spanned.SessionsPerStep = 2
		if plan := Efficiency(spanned, true, p); !plan.CleanStart {
			t.Errorf("sessions per step 2: CleanStart = false, want true")
		}
		if plan := Efficiency(generationBaseline(), true, p); plan.CleanStart {
			t.Errorf("sessions per step 1: CleanStart = true, want false")
		}
	})

	t.Run("thrift counsel reads the repeat-read floor", func(t *testing.T) {
		p := efficiencyPolicy(2, 80, 2)
		for _, tc := range []struct {
			reads int64
			want  bool
		}{{1, false}, {2, true}, {3, true}} {
			a := generationBaseline()
			a.MedianRepeatReads = tc.reads
			if got := Efficiency(a, true, p).ThriftCounsel; got != tc.want {
				t.Errorf("median repeat reads %d against floor 2: ThriftCounsel = %v, want %v", tc.reads, got, tc.want)
			}
		}
	})

	t.Run("the plan is a function of its arguments", func(t *testing.T) {
		a, p := generationBaseline(), efficiencyPolicy(2, 80, 1)
		first := Efficiency(a, true, p)
		for i := 0; i < 8; i++ {
			if got := Efficiency(a, true, p); got != first {
				t.Fatalf("call %d = %+v, first call = %+v; Efficiency is not a pure function of its arguments", i+2, got, first)
			}
		}
	})

	// This is the structural half, and it is the strongest statement of AC-2 available: the design's
	// frame-lift is that the predicate cannot be switched off by a roomy window BECAUSE it never
	// learns a window exists. A behavioural test can only ever sample; the declaration is the claim.
	t.Run("the signature names no capacity operand", func(t *testing.T) {
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, "efficiency.go", nil, 0)
		if err != nil {
			t.Fatalf("parse efficiency.go: %v", err)
		}
		var fn *ast.FuncDecl
		for _, decl := range file.Decls {
			if d, ok := decl.(*ast.FuncDecl); ok && d.Name.Name == "Efficiency" {
				fn = d
			}
		}
		if fn == nil {
			t.Fatal("no Efficiency in efficiency.go; the guard proves nothing")
		}
		banned := map[string]string{
			"Window":    "a resolved context window; a roomy one would switch efficiency off, which is the pathology AC-2 removes",
			"Occupancy": "a live occupancy reading; efficiency is keyed on learned history, not on how full the session is now",
			"Appetite":  "a projected occupancy; the same capacity frame under another name",
		}
		ast.Inspect(fn.Type.Params, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.Ident:
				if why, ok := banned[node.Name]; ok {
					t.Errorf("Efficiency takes a %s operand: %s", node.Name, why)
				}
			case *ast.SelectorExpr:
				if why, ok := banned[node.Sel.Name]; ok {
					t.Errorf("Efficiency takes a %s operand: %s", node.Sel.Name, why)
				}
			}
			return true
		})
	})
}

// TestEfficiencyQualityGuardDeclines covers the arm the design puts between "this step generates
// wastefully" and "reducing its effort was actually free": a treatment arm that spent fewer tokens
// while tripping more gates bought nothing, and the plan must decline rather than keep paying for it.
//
// Every row runs on a base that fires, and the last row proves it — otherwise a decline below is
// explained by the baseline rather than by the guard.
func TestEfficiencyQualityGuardDeclines(t *testing.T) {
	base := func() Aggregate {
		a := generationBaseline()
		a.MedianGateFlags = 2
		return a
	}
	p := efficiencyPolicy(2, 80, 1)

	t.Run("the trial arm is admitted on thin evidence", func(t *testing.T) {
		// The reduced figures are deliberately terrible. With fewer reduced runs than the learned
		// floor there is no comparison to make yet, so the row proves the RUN-COUNT arm and not the
		// comparison — a guard that consulted the medians here would decline.
		a := base()
		a.ReducedRuns = 1
		a.ReducedMedianOutTokens = 5_000
		a.ReducedMedianGateFlags = 9

		if plan := Efficiency(a, true, p); plan.EffortLevel != "medium" {
			t.Fatalf("EffortLevel = %q, want %q (reason %q); below learned_min_runs the arm is still on trial",
				plan.EffortLevel, "medium", plan.Reason)
		}
	})

	t.Run("lower output at equal flags continues", func(t *testing.T) {
		a := base()
		a.ReducedRuns = 2
		a.ReducedMedianOutTokens = 600
		a.ReducedMedianGateFlags = 2

		if plan := Efficiency(a, true, p); plan.EffortLevel != "medium" {
			t.Fatalf("EffortLevel = %q, want %q (reason %q); 600 < 1000 and 2 <= 2 is the admitting case",
				plan.EffortLevel, "medium", plan.Reason)
		}
	})

	t.Run("more gate flags declines", func(t *testing.T) {
		a := base()
		a.ReducedRuns = 2
		a.ReducedMedianOutTokens = 600
		a.ReducedMedianGateFlags = 3

		plan := Efficiency(a, true, p)
		if plan.Reason != ReasonQualityGuard {
			t.Fatalf("Reason = %q, want %q; the arm spent less and tripped more gates", plan.Reason, ReasonQualityGuard)
		}
		if plan.EffortLevel != "" {
			t.Errorf("EffortLevel = %q, want empty when the guard declines", plan.EffortLevel)
		}
	})

	t.Run("no output gain declines", func(t *testing.T) {
		a := base()
		a.ReducedRuns = 2
		a.ReducedMedianOutTokens = 1_000
		a.ReducedMedianGateFlags = 2

		if plan := Efficiency(a, true, p); plan.Reason != ReasonQualityGuard {
			t.Errorf("Reason = %q, want %q; equal output is not a gain, the comparison is strict",
				plan.Reason, ReasonQualityGuard)
		}
	})

	t.Run("control: the base fires with no treatment arm recorded", func(t *testing.T) {
		if plan := Efficiency(base(), true, p); plan.Reason != "" {
			t.Fatalf("the base aggregate declined with %q; every decline above would then prove nothing", plan.Reason)
		}
	})
}
