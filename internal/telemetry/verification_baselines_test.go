package telemetry

import (
	"math"
	"strings"
	"testing"
)

// The registry is float64 and two literals of the same decimal parse identically, so exact equality
// would work today. The tolerance is here for the direction this table will actually be edited in:
// a share re-derived as 0.7740000000000001 by a spreadsheet is the same measurement, and a test
// that failed on it would teach the next author to round the EVIDENCE to fit the test.
const figureEpsilon = 1e-9

func sameFigure(a, b float64) bool { return math.Abs(a-b) <= figureEpsilon }

// TestVerificationThresholds is #668 K12, and its entire value is that it was written BEFORE the
// run it judges. Phase 7 is one operator-executed run on a physical backend — n≈1, permanently
// non-CI-able — so "did it work" is only a computable question if the numbers were registered
// first. A threshold moved after seeing the result is not a threshold.
//
// The golden table is spelled out rather than derived from VerificationBaselines(). A table that
// computed its own expectation would restate the registry instead of checking it, and the mutation
// this exists to catch is a figure quietly relaxed to match a disappointing measurement.
func TestVerificationThresholds(t *testing.T) {
	type want struct {
		low, high          float64
		unit, kind, source string
	}
	golden := map[string]want{
		"s1_s6_out_tokens":             {186_282, 186_282, UnitTokens, FigureKindWallCost, ".analysis/668/rootcause_concern_3.md:120"},
		"s1_s6_think_tokens_est":       {144_219, 144_219, UnitTokens, FigureKindWallCost, ".analysis/668/rootcause_concern_3.md:120"},
		"s5_out_tokens":                {112_217, 112_217, UnitTokens, FigureKindWallCost, ".analysis/668/rootcause_concern_3.md:120"},
		"thinking_share":               {0.683, 0.774, UnitRatio, FigureKindShare, ".analysis/668/rootcause_concern_3.md:120"},
		"peak_simultaneous_fanout_sum": {334_350, 334_350, UnitTokens, FigureKindPeak, ".analysis/668/rootcause_concern_4.md:29,65,87"},
		"reprefill_proxy_turns":        {82, 82, UnitTurns, FigureKindCount, ".analysis/668/rootcause_concern_4.md:31,71,90"},
		"fable5_main_session_peak":     {417_526, 417_526, UnitTokens, FigureKindPeak, ".analysis/668/rootcause_concern_1.md:11,59,157"},
	}

	figures := VerificationBaselines()

	// The count is asserted separately from the walk below, because the walk alone cannot fail on an
	// EMPTIED registry: zero figures means zero mismatches, and only the leftover check at the bottom
	// would notice. Stated twice, so the failure names which way the table drifted.
	if len(figures) != len(golden) {
		t.Errorf("VerificationBaselines() registers %d figures, want %d — a pre-registered table that "+
			"changed size after the fact is the one thing K12 exists to make impossible",
			len(figures), len(golden))
	}

	for _, f := range figures {
		w, ok := golden[f.ID]
		if !ok {
			t.Errorf("the registry carries figure %q with no golden; a baseline nobody pinned is a "+
				"baseline Phase 7 can be judged against and nobody agreed to", f.ID)
			continue
		}
		if !sameFigure(f.Low, w.low) || !sameFigure(f.High, w.high) {
			t.Errorf("%s = [%v, %v], want [%v, %v]. These are the CORRECTED message-dedup figures; if "+
				"a re-derivation moved one, move the analysis citation in the same commit.",
				f.ID, f.Low, f.High, w.low, w.high)
		}
		if f.Unit != w.unit {
			t.Errorf("%s unit = %q, want %q", f.ID, f.Unit, w.unit)
		}
		if f.Kind != w.kind {
			t.Errorf("%s kind = %q, want %q", f.ID, f.Kind, w.kind)
		}
		if f.Source != w.source {
			t.Errorf("%s source = %q, want %q", f.ID, f.Source, w.source)
		}
		if !strings.HasPrefix(f.Source, ".analysis/668/") || !strings.Contains(f.Source, ".md:") {
			t.Errorf("%s cites %q, which names no analysis file and line. A figure with no citation "+
				"cannot be re-derived, and an unre-derivable baseline is an opinion.", f.ID, f.Source)
		}
		// scale.md:17 still says "~93%" and :18/:93 still say "339K" — both are the line-summing
		// artifacts these baselines correct. Citing it would re-import the error being fixed.
		if strings.Contains(f.Source, "scale.md") {
			t.Errorf("%s cites scale.md, which still carries the pre-correction ~93%% share and 339K "+
				"fan-out figures; cite the .analysis/668 re-derivation instead", f.ID)
		}
		if strings.TrimSpace(f.Definition) == "" {
			t.Errorf("%s states no definition; a number whose quantity is not written down cannot be "+
				"reproduced by whoever runs Phase 7", f.ID)
		}
		delete(golden, f.ID)
	}

	for id := range golden {
		t.Errorf("the golden names %q and the registry has no such figure. A REMOVED threshold must "+
			"fail as loudly as a changed one — deleting the row is the cheapest way to make a run "+
			"pass, so it is the one this test is most responsible for catching", id)
	}

	t.Run("the corrections are the registered values, not the originals", func(t *testing.T) {
		byID := map[string]VerificationFigure{}
		for _, f := range VerificationBaselines() {
			byID[f.ID] = f
		}

		// The three headline corrections, asserted as REFUSALS as well as values. Pinning 112,217
		// alone would pass on a registry that also still carried the superseded figure somewhere.
		if s5 := byID["s5_out_tokens"]; sameFigure(s5.High, 373_416) {
			t.Error("s5_out_tokens is registered as 373,416, the pre-dedup line-summed figure; the " +
				"message-dedup value is 112,217 (.analysis/668/rootcause_concern_3.md:120)")
		}
		share := byID["thinking_share"]
		if share.High >= 0.93 {
			t.Errorf("thinking_share upper bound = %v; the famous ~93%% was a line-summing artifact and "+
				"the corrected band is 68.3%%–77.4%%", share.High)
		}
		if !sameFigure(share.Low, 0.683) || !sameFigure(share.High, 0.774) {
			t.Errorf("thinking_share band = [%v, %v], want [0.683, 0.774] — 68.3%% is the direct-chars "+
				"share and 77.4%% the estimate, and the band is the pair", share.Low, share.High)
		}
		if fanout := byID["peak_simultaneous_fanout_sum"]; !sameFigure(fanout.High, 334_350) {
			t.Errorf("peak_simultaneous_fanout_sum = %v, want 334350; scale.md's 339K is superseded",
				fanout.High)
		}
	})

	t.Run("thinking share and thinking wall-cost are separate figures", func(t *testing.T) {
		// H-R1: the share is an authoring artifact that barely moves, the wall cost falls with turn
		// count. One figure standing for both would report a successful reduction as a regression in
		// the share, or hide one behind the other.
		kindOf := map[string]string{}
		var share, wall int
		for _, f := range VerificationBaselines() {
			kindOf[f.ID] = f.Kind
			switch f.Kind {
			case FigureKindShare:
				share++
			case FigureKindWallCost:
				wall++
			}
		}
		if share == 0 || wall == 0 {
			t.Fatalf("registry has %d share figures and %d wall-cost figures; H-R1 requires both, "+
				"tracked apart", share, wall)
		}

		// The two figures that would collapse into one if H-R1 were ignored: both are derived from the
		// same thinking estimate, and it is the KIND that keeps them apart. Asserted per ID rather
		// than as a set disjointness, which one switch statement makes true by construction and which
		// therefore says nothing about whether the right figure got the right kind.
		for id, want := range map[string]string{
			"thinking_share":         FigureKindShare,
			"s1_s6_think_tokens_est": FigureKindWallCost,
			"s1_s6_out_tokens":       FigureKindWallCost,
			"s5_out_tokens":          FigureKindWallCost,
		} {
			if got := kindOf[id]; got != want {
				t.Errorf("%q is registered as kind %q, want %q — H-R1 turns on this distinction: a "+
					"reduction in turn count moves the wall cost and leaves the share where it was, so "+
					"reading one as the other reports a success as a regression", id, got, want)
			}
		}
	})

	t.Run("the counting method the figures were derived under is named", func(t *testing.T) {
		// Without this the numbers are unreproducible: summing usage records instead of deduping them
		// over-counts by ~2.2x, which is precisely how the superseded figures were produced.
		for _, want := range []string{"message.id", "MAX", "internal/statusline/tokens.go"} {
			if !strings.Contains(VerificationCountingMethod, want) {
				t.Errorf("VerificationCountingMethod does not name %q:\n%s", want, VerificationCountingMethod)
			}
		}
	})

	t.Run("the byte-identity procedure is written down", func(t *testing.T) {
		for _, want := range []string{"sha256", "af telemetry rebuild", "digest"} {
			if !strings.Contains(VerificationDigestProcedure, want) {
				t.Errorf("VerificationDigestProcedure does not name %q:\n%s", want, VerificationDigestProcedure)
			}
		}
	})
}
