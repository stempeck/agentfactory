package tokenomics

import (
	"math"
	"testing"
)

// TestBandReportArithmetic is the pure half of #668 K10, and it exists because the verb-level test
// cannot reach most of it. internal/cmd's TestBandReport seeds one median — the peak — so the delta
// and duration tolerances are 0 on every fixture there, both figures read no_baseline, and
// BandDeltaTolerancePct, BandDurationTolerancePct and the figure-to-tolerance assignment in
// JudgeBand's table can all be changed with that suite staying green.
//
// The comment at the top of band.go says the design's purpose is that "the whole matrix" is
// table-testable. This is the table.
func TestBandReportArithmetic(t *testing.T) {
	t.Run("the band is the median plus and minus the stated tolerance", func(t *testing.T) {
		// Spelled out rather than recomputed from the constants: arithmetic that checked itself
		// against its own formula would pass whatever the formula became.
		for _, tc := range []struct {
			name              string
			median            int64
			tolPct            int
			observed          int64
			low, high         int64
			wantVerdict       string
			whatWouldGoUnseen string
		}{
			{"inside", 100_000, BandPeakTolerancePct, 105_000, 80_000, 120_000, BandWithin, ""},
			{"the low edge is inclusive", 100_000, BandPeakTolerancePct, 80_000, 80_000, 120_000, BandWithin,
				"a run exactly on the edge is inside; an exclusive comparison would report the boundary run as drift"},
			{"the high edge is inclusive", 100_000, BandPeakTolerancePct, 120_000, 80_000, 120_000, BandWithin, ""},
			{"one below the low edge", 100_000, BandPeakTolerancePct, 79_999, 80_000, 120_000, BandOutside, ""},
			{"one above the high edge", 100_000, BandPeakTolerancePct, 120_001, 80_000, 120_000, BandOutside, ""},
			{"the delta tolerance is wider than the peak's", 100_000, BandDeltaTolerancePct, 124_000,
				75_000, 125_000, BandWithin,
				"124,000 is outside the peak's 20% band and inside the delta's 25% one, so a figure " +
					"handed the wrong tolerance flips this row"},
			{"the duration tolerance is wider still", 100_000, BandDurationTolerancePct, 149_000,
				50_000, 150_000, BandWithin,
				"149,000 is outside both of the other two bands"},
			{"a small median still gets a band", 50, BandPeakTolerancePct, 45, 40, 60, BandWithin,
				"dividing before multiplying would collapse this to [50,50] and report 45 as drift"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := judgeBandFigure("f", &tc.observed, tc.median, tc.tolPct, true)
				if got.Low != tc.low || got.High != tc.high {
					t.Errorf("band = [%d, %d], want [%d, %d] for median %d ±%d%%",
						got.Low, got.High, tc.low, tc.high, tc.median, tc.tolPct)
				}
				if got.Verdict != tc.wantVerdict {
					t.Errorf("verdict = %q, want %q (observed %d in [%d, %d]). %s",
						got.Verdict, tc.wantVerdict, tc.observed, tc.low, tc.high, tc.whatWouldGoUnseen)
				}
			})
		}
	})

	t.Run("the three tolerances are ordered peak < delta < duration", func(t *testing.T) {
		// The ORDERING is the claim band.go's comment makes — peak tightest because it is what the
		// admission predicate divides with, duration loosest because it is dominated by backend
		// latency. Swapping two of them would leave every band non-empty and every case above still
		// arithmetically consistent, so the relation is asserted directly.
		if !(BandPeakTolerancePct < BandDeltaTolerancePct && BandDeltaTolerancePct < BandDurationTolerancePct) {
			t.Errorf("tolerances are peak=%d delta=%d duration=%d; the band widens with how much of "+
				"the figure is a property of the host rather than of the step",
				BandPeakTolerancePct, BandDeltaTolerancePct, BandDurationTolerancePct)
		}
	})

	t.Run("no baseline is not a judgement", func(t *testing.T) {
		observed := int64(105_000)
		for _, tc := range []struct {
			name             string
			median           int64
			trusted          bool
			whyItIsNotAThing string
		}{
			{"a median of zero", 0, true,
				"zero is the aggregate's only spelling of `nothing was measured`, so a [0,0] band " +
					"would report every real run as outside one that was never drawn"},
			{"a median below the operator's run floor", 100_000, false,
				"a band drawn around too few samples would let the first run of a step define " +
					"correctness for every run after it"},
			{"a negative median from a malformed record", -5, true,
				"an inverted band would put Low above High and report everything as drift"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				got := judgeBandFigure("f", &observed, tc.median, BandPeakTolerancePct, tc.trusted)
				if got.Verdict != BandNoBaseline {
					t.Errorf("verdict = %q, want %q: %s", got.Verdict, BandNoBaseline, tc.whyItIsNotAThing)
				}
				if got.Low != 0 || got.High != 0 {
					t.Errorf("band = [%d, %d] on a no_baseline figure; there is no band to report",
						got.Low, got.High)
				}
				if got.Median != 0 {
					t.Errorf("median = %d on a no_baseline figure. The verdict says the aggregate is "+
						"not usable, so attaching its median hands a consumer a number to join on "+
						"that the verdict beside it disowns — and the human path suppresses it, so "+
						"the two surfaces would report the same row differently", got.Median)
				}
			})
		}
	})

	t.Run("an unmeasured figure is unmeasurable, never within", func(t *testing.T) {
		// The pointer is the whole reason Observation keeps the record's pointers. A plain int64 would
		// arrive as 0, fall outside a [80000, 120000] band, and report a step nobody measured as the
		// worst drift in the report.
		got := judgeBandFigure("f", nil, 100_000, BandPeakTolerancePct, true)
		if got.Verdict != BandUnmeasurable {
			t.Errorf("verdict = %q, want %q", got.Verdict, BandUnmeasurable)
		}
		if got.Low != 80_000 || got.High != 120_000 {
			t.Errorf("band = [%d, %d], want [80000, 120000] — the baseline exists even though the run "+
				"did not record the figure, and withholding it would hide what the run was measured against",
				got.Low, got.High)
		}
	})

	t.Run("an absurd median cannot invert the band", func(t *testing.T) {
		// Digest values come off records rather than validated config, and there are TWO ways to wrap.
		// median*tolPct overflows above MaxInt64/100, which is why that product falls back to the
		// lossy divide-first order. median+delta overflows much later, only as the median approaches
		// MaxInt64 itself. Either wrap lands negative, puts Low above High, and reports every
		// observation as outside — the loudest wrong answer from the quietest input.
		for _, median := range []int64{
			math.MaxInt64 / 2, // past MaxInt64/100: the product wraps, the sum does not
			math.MaxInt64/100*99 + 500,
			math.MaxInt64, // the sum wraps too; only saturating High keeps the band ordered
		} {
			observed := median
			got := judgeBandFigure("f", &observed, median, BandPeakTolerancePct, true)
			if got.Low > got.High {
				t.Errorf("median %d: band = [%d, %d] is inverted", median, got.Low, got.High)
				continue
			}
			if got.Verdict != BandWithin {
				t.Errorf("median %d: verdict = %q, want %q — the observation IS the median",
					median, got.Verdict, BandWithin)
			}
		}
	})

	t.Run("the row verdict is worst-first", func(t *testing.T) {
		// A step that held its peak and doubled its consumption has drifted. A roll-up that averaged
		// or that let the first figure win would report it as fine.
		for _, tc := range []struct {
			name     string
			verdicts []string
			want     string
		}{
			{"one outside beats every within", []string{BandWithin, BandOutside, BandWithin}, BandOutside},
			{"one outside beats an unmeasurable", []string{BandUnmeasurable, BandOutside}, BandOutside},
			{"a judged figure beats an unjudged one", []string{BandUnmeasurable, BandWithin}, BandWithin},
			{"unmeasurable beats no_baseline", []string{BandNoBaseline, BandUnmeasurable}, BandUnmeasurable},
			{"nothing to compare", []string{BandNoBaseline, BandNoBaseline}, BandNoBaseline},
			{"no figures at all", nil, BandNoBaseline},
		} {
			t.Run(tc.name, func(t *testing.T) {
				figures := make([]BandFigure, 0, len(tc.verdicts))
				for _, v := range tc.verdicts {
					figures = append(figures, BandFigure{Verdict: v})
				}
				if got := rollUpBand(figures); got != tc.want {
					t.Errorf("rollUpBand(%v) = %q, want %q", tc.verdicts, got, tc.want)
				}
			})
		}
	})

	t.Run("each figure is judged against its own median and its own tolerance", func(t *testing.T) {
		// The table literal inside JudgeBand pairs three medians with three tolerances, and a
		// transposition there is invisible to any test that seeds only one of them. Every median is
		// distinct here and every observation is chosen to be inside its OWN band and outside at
		// least one of the others.
		// The three generation figures (#678 K9) share BandDeltaTolerancePct with cum_tokens_delta, so
		// the transposition this sub-test guards against cannot be caught by distinct tolerances among
		// them. Their medians are distinct instead, and each observation below sits inside its OWN band
		// and outside all three of the others.
		key := DigestKey{Formula: "offpath", StepID: "s-1", Model: "lmstudio"}
		d := Digest{Entries: map[string]Aggregate{
			key.String(): {
				Runs:                 4,
				MedianPeakCtxTokens:  100_000,
				MedianCumTokensDelta: 40_000,
				MedianDurationMS:     600_000,
				GenerationRuns:       4,
				MedianOutTokens:      12_000,
				MedianSubagentTokens: 20_000,
				MedianThinkTokens:    8_000,
			},
		}}

		peak, delta, duration := int64(119_000), int64(49_000), int64(890_000)
		out, subagent, think := int64(13_000), int64(23_000), int64(9_500)
		row := JudgeBand(d, Observation{
			Key: key, PeakCtxTokens: &peak, CumTokensDelta: &delta, DurationMS: &duration,
			OutTokens: &out, SubagentTokens: &subagent, ThinkTokens: &think,
		}, 1)

		if row.Runs != 4 {
			t.Errorf("runs = %d, want 4 — a verdict withheld for thin evidence must be visible as such",
				row.Runs)
		}
		if row.Verdict != BandWithin {
			t.Errorf("row verdict = %q, want %q", row.Verdict, BandWithin)
		}
		want := map[string]struct {
			median    int64
			tolPct    int
			low, high int64
		}{
			BandFigurePeakCtxTokens:  {100_000, BandPeakTolerancePct, 80_000, 120_000},
			BandFigureCumTokensDelta: {40_000, BandDeltaTolerancePct, 30_000, 50_000},
			BandFigureDurationMS:     {600_000, BandDurationTolerancePct, 300_000, 900_000},
			BandFigureOutTokens:      {12_000, BandDeltaTolerancePct, 9_000, 15_000},
			BandFigureSubagentTokens: {20_000, BandDeltaTolerancePct, 15_000, 25_000},
			BandFigureThinkTokens:    {8_000, BandDeltaTolerancePct, 6_000, 10_000},
		}
		if len(row.Figures) != len(want) {
			t.Fatalf("row has %d figures, want %d", len(row.Figures), len(want))
		}
		for _, f := range row.Figures {
			w, ok := want[f.Name]
			if !ok {
				t.Errorf("unexpected figure %q; the names are join keys and part of the contract", f.Name)
				continue
			}
			if f.Median != w.median || f.TolerancePct != w.tolPct || f.Low != w.low || f.High != w.high {
				t.Errorf("%s: median %d ±%d%% ⇒ [%d, %d], want median %d ±%d%% ⇒ [%d, %d] — a figure "+
					"handed another figure's median or tolerance judges the run against the wrong history",
					f.Name, f.Median, f.TolerancePct, f.Low, f.High, w.median, w.tolPct, w.low, w.high)
			}
			if f.Verdict != BandWithin {
				t.Errorf("%s verdict = %q, want %q", f.Name, f.Verdict, BandWithin)
			}
			if f.Direction != BandDirectionWithin {
				t.Errorf("%s direction = %q, want %q", f.Name, f.Direction, BandDirectionWithin)
			}
			delete(want, f.Name)
		}
		for name := range want {
			t.Errorf("no figure named %q was judged", name)
		}
	})

	t.Run("a key the digest has never seen is judged against nothing", func(t *testing.T) {
		peak := int64(105_000)
		row := JudgeBand(Digest{Entries: map[string]Aggregate{}},
			Observation{Key: DigestKey{Formula: "offpath", StepID: "s-9"}, PeakCtxTokens: &peak}, 1)
		if row.Verdict != BandNoBaseline {
			t.Errorf("verdict = %q, want %q for a step with no learned history", row.Verdict, BandNoBaseline)
		}
		if row.Runs != 0 {
			t.Errorf("runs = %d, want 0", row.Runs)
		}
	})

	t.Run("the judge is pure", func(t *testing.T) {
		// #519's rule for this package, asserted rather than asserted-in-a-comment: same digest and
		// same observation in, same figures out, with no clock and no filesystem between them.
		key := DigestKey{Formula: "offpath", StepID: "s-1"}
		d := Digest{Entries: map[string]Aggregate{key.String(): {Runs: 3, MedianPeakCtxTokens: 90_000}}}
		peak := int64(91_000)
		obs := Observation{Key: key, PeakCtxTokens: &peak}

		first, second := JudgeBand(d, obs, 2), JudgeBand(d, obs, 2)
		if first.Verdict != second.Verdict || len(first.Figures) != len(second.Figures) {
			t.Fatalf("two calls disagreed: %+v vs %+v", first, second)
		}
		for i := range first.Figures {
			if first.Figures[i] != second.Figures[i] {
				t.Errorf("figure %d differs between two identical calls: %+v vs %+v",
					i, first.Figures[i], second.Figures[i])
			}
		}
	})
}

func TestBandJudgesGenerationFigures(t *testing.T) {
	key := DigestKey{Formula: "offpath", StepID: "s-1", Model: "lmstudio"}
	digestWithGenerationBaseline := Digest{Entries: map[string]Aggregate{
		key.String(): {
			Runs:                 6,
			MedianPeakCtxTokens:  100_000,
			MedianCumTokensDelta: 40_000,
			MedianDurationMS:     600_000,
			GenerationRuns:       6,
			MedianOutTokens:      12_000,
			MedianSubagentTokens: 20_000,
			MedianThinkTokens:    8_000,
		},
	}}
	ptr := func(v int64) *int64 { return &v }
	figureNamed := func(t *testing.T, row BandRow, name string) BandFigure {
		t.Helper()
		for _, f := range row.Figures {
			if f.Name == name {
				return f
			}
		}
		t.Fatalf("no figure named %q in %+v", name, row.Figures)
		return BandFigure{}
	}

	t.Run("the three generation figures join the occupancy figures", func(t *testing.T) {
		row := JudgeBand(digestWithGenerationBaseline, Observation{
			Key:            key,
			PeakCtxTokens:  ptr(100_000),
			CumTokensDelta: ptr(40_000),
			DurationMS:     ptr(600_000),
			OutTokens:      ptr(12_000),
			SubagentTokens: ptr(20_000),
			ThinkTokens:    ptr(8_000),
		}, 1)

		want := []string{
			BandFigurePeakCtxTokens,
			BandFigureCumTokensDelta,
			BandFigureDurationMS,
			BandFigureOutTokens,
			BandFigureSubagentTokens,
			BandFigureThinkTokens,
		}
		if len(row.Figures) != len(want) {
			t.Fatalf("row has %d figures, want %d: %+v", len(row.Figures), len(want), row.Figures)
		}
		for i, name := range want {
			if row.Figures[i].Name != name {
				t.Errorf("figure %d is %q, want %q — the order is the rendered order on both surfaces",
					i, row.Figures[i].Name, name)
			}
		}
		for _, name := range []string{BandFigureOutTokens, BandFigureSubagentTokens, BandFigureThinkTokens} {
			if got := figureNamed(t, row, name).TolerancePct; got != BandDeltaTolerancePct {
				t.Errorf("%s tolerance = %d%%, want %d%% — generation is a token-delta figure and shares "+
					"the delta band, not the occupancy or duration band", name, got, BandDeltaTolerancePct)
			}
		}
	})

	t.Run("a cheaper run and a costlier run are distinguishable", func(t *testing.T) {
		// Without a direction the two runs below are byte-identical on every surface, so an efficiency
		// win reads exactly like an efficiency regression. That is the whole point of the leg.
		cheaper := figureNamed(t, JudgeBand(digestWithGenerationBaseline,
			Observation{Key: key, OutTokens: ptr(8_000)}, 1), BandFigureOutTokens)
		costlier := figureNamed(t, JudgeBand(digestWithGenerationBaseline,
			Observation{Key: key, OutTokens: ptr(20_000)}, 1), BandFigureOutTokens)

		if cheaper.Verdict != BandOutside || costlier.Verdict != BandOutside {
			t.Fatalf("verdicts = %q / %q, want both %q", cheaper.Verdict, costlier.Verdict, BandOutside)
		}
		if cheaper.Direction != BandDirectionBelow {
			t.Errorf("8000 against a 9000..15000 band: direction = %q, want %q",
				cheaper.Direction, BandDirectionBelow)
		}
		if costlier.Direction != BandDirectionAbove {
			t.Errorf("20000 against a 9000..15000 band: direction = %q, want %q",
				costlier.Direction, BandDirectionAbove)
		}
		if cheaper.Direction == costlier.Direction {
			t.Error("a cheaper run and a costlier run reported the same direction")
		}
	})

	t.Run("an in-band run reports within", func(t *testing.T) {
		f := figureNamed(t, JudgeBand(digestWithGenerationBaseline,
			Observation{Key: key, OutTokens: ptr(12_500)}, 1), BandFigureOutTokens)
		if f.Verdict != BandWithin {
			t.Errorf("verdict = %q, want %q", f.Verdict, BandWithin)
		}
		if f.Direction != BandDirectionWithin {
			t.Errorf("direction = %q, want %q", f.Direction, BandDirectionWithin)
		}
	})

	t.Run("a figure with no band under it states no direction", func(t *testing.T) {
		// A direction with no band under it is unfalsifiable: there is nothing to be above or below.
		noGeneration := Digest{Entries: map[string]Aggregate{
			key.String(): {Runs: 6, MedianPeakCtxTokens: 100_000},
		}}
		row := JudgeBand(noGeneration, Observation{
			Key: key, PeakCtxTokens: ptr(100_000), OutTokens: ptr(12_000),
		}, 1)

		out := figureNamed(t, row, BandFigureOutTokens)
		if out.Verdict != BandNoBaseline {
			t.Errorf("verdict = %q, want %q for a zero median", out.Verdict, BandNoBaseline)
		}
		if out.Direction != "" {
			t.Errorf("direction = %q, want empty", out.Direction)
		}

		untrusted := figureNamed(t, JudgeBand(digestWithGenerationBaseline,
			Observation{Key: key, OutTokens: ptr(20_000)}, 99), BandFigureOutTokens)
		if untrusted.Verdict != BandNoBaseline || untrusted.Direction != "" {
			t.Errorf("below the trust floor: verdict %q direction %q, want %q and empty",
				untrusted.Verdict, untrusted.Direction, BandNoBaseline)
		}
	})

	t.Run("an unmeasured generation figure states no direction", func(t *testing.T) {
		f := figureNamed(t, JudgeBand(digestWithGenerationBaseline,
			Observation{Key: key, PeakCtxTokens: ptr(100_000)}, 1), BandFigureOutTokens)
		if f.Verdict != BandUnmeasurable {
			t.Errorf("verdict = %q, want %q", f.Verdict, BandUnmeasurable)
		}
		if f.Direction != "" {
			t.Errorf("direction = %q, want empty", f.Direction)
		}
	})

	t.Run("the verdict vocabulary stays closed", func(t *testing.T) {
		verdicts := map[string]bool{
			BandWithin: true, BandOutside: true, BandNoBaseline: true, BandUnmeasurable: true,
		}
		directions := map[string]bool{
			"": true, BandDirectionBelow: true, BandDirectionWithin: true, BandDirectionAbove: true,
		}
		for _, obs := range []Observation{
			{Key: key},
			{Key: key, OutTokens: ptr(1), SubagentTokens: ptr(1), ThinkTokens: ptr(1)},
			{Key: key, OutTokens: ptr(1 << 40), SubagentTokens: ptr(20_000), ThinkTokens: ptr(8_000)},
			{Key: DigestKey{Formula: "unseen"}, OutTokens: ptr(12_000)},
		} {
			row := JudgeBand(digestWithGenerationBaseline, obs, 1)
			if !verdicts[row.Verdict] {
				t.Errorf("row verdict %q is outside the closed vocabulary", row.Verdict)
			}
			for _, f := range row.Figures {
				if !verdicts[f.Verdict] {
					t.Errorf("%s verdict %q is outside the closed vocabulary", f.Name, f.Verdict)
				}
				if !directions[f.Direction] {
					t.Errorf("%s direction %q is outside the closed vocabulary", f.Name, f.Direction)
				}
			}
		}
	})
}
