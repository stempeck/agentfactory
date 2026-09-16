package tokenomics

import "testing"

func TestEfficiencyProtocolVerdict_MedianAfterBelowMinBefore(t *testing.T) {
	before := []int64{140_000, 100_000, 130_000, 110_000, 120_000}

	t.Run("pass iff the after median beats the whole before arm", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			after []int64
			want  string
		}{
			{"every after run below the cheapest before run", []int64{80_000, 85_000, 90_000, 95_000, 99_000}, ProtocolPass},
			{"the median beats it while two runs do not", []int64{80_000, 85_000, 99_000, 200_000, 300_000}, ProtocolPass},
			{"the median ties the cheapest before run", []int64{80_000, 85_000, 100_000, 105_000, 110_000}, ProtocolFail},
			{"the median misses by one token", []int64{80_000, 85_000, 100_001, 105_000, 110_000}, ProtocolFail},
			{"three cheap runs are not enough to carry the median", []int64{80_000, 90_000, 120_000, 130_000, 140_000}, ProtocolFail},
			{"the after arm is uniformly worse", []int64{200_000, 210_000, 220_000, 230_000, 240_000}, ProtocolFail},
		} {
			t.Run(tc.name, func(t *testing.T) {
				verdict, medianAfter, minBefore := ProtocolCompare(before, tc.after)
				if verdict != tc.want {
					t.Errorf("verdict = %q, want %q (median(after)=%d, min(before)=%d)",
						verdict, tc.want, medianAfter, minBefore)
				}
				if want := (medianAfter < minBefore); (verdict == ProtocolPass) != want {
					t.Errorf("verdict %q disagrees with median(after) %d < min(before) %d — the bar is the "+
						"only definition of improves the design has", verdict, medianAfter, minBefore)
				}
			})
		}
	})

	t.Run("the reported figures are this package's own folds", func(t *testing.T) {
		after := []int64{80_000, 85_000, 99_000, 200_000, 300_000}
		_, medianAfter, minBefore := ProtocolCompare(before, after)
		if medianAfter != median(after) {
			t.Errorf("medianAfter = %d, want %d — a second spelling of median would let the bar drift",
				medianAfter, median(after))
		}
		if minBefore != minOf(before) {
			t.Errorf("minBefore = %d, want %d", minBefore, minOf(before))
		}
	})

	t.Run("an arm that is not the protocol size voids rather than fails", func(t *testing.T) {
		// Void, never fail: an under-sized arm is an absence of evidence, and reporting it as a fail
		// would let a truncated run claim a measured result.
		cheap := []int64{80_000, 85_000, 90_000, 95_000, 99_000}
		for _, tc := range []struct {
			name          string
			before, after []int64
		}{
			{"both arms empty", nil, nil},
			{"no before arm", nil, cheap},
			{"no after arm", before, nil},
			{"short before arm", before[:4], cheap},
			{"short after arm", before, cheap[:4]},
			{"long before arm", append(append([]int64{}, before...), 150_000), cheap},
			{"long after arm", before, append(append([]int64{}, cheap...), 70_000)},
		} {
			t.Run(tc.name, func(t *testing.T) {
				verdict, _, _ := ProtocolCompare(tc.before, tc.after)
				if verdict != ProtocolVoid {
					t.Errorf("verdict = %q, want %q for %d before / %d after (protocol size %d)",
						verdict, ProtocolVoid, len(tc.before), len(tc.after), ProtocolArmSize)
				}
			})
		}
	})

	t.Run("the comparison is pure", func(t *testing.T) {
		after := []int64{80_000, 85_000, 99_000, 200_000, 300_000}
		v1, m1, n1 := ProtocolCompare(before, after)
		v2, m2, n2 := ProtocolCompare(before, after)
		if v1 != v2 || m1 != m2 || n1 != n2 {
			t.Errorf("two identical calls disagreed: (%q,%d,%d) vs (%q,%d,%d)", v1, m1, n1, v2, m2, n2)
		}
		if before[0] != 140_000 || after[0] != 80_000 {
			t.Error("ProtocolCompare sorted its caller's slices in place")
		}
	})

	t.Run("the verdict vocabulary is closed", func(t *testing.T) {
		known := map[string]bool{ProtocolPass: true, ProtocolFail: true, ProtocolVoid: true}
		for _, after := range [][]int64{nil, {1}, {0, 0, 0, 0, 0}, {1 << 40, 1, 1, 1, 1}} {
			if v, _, _ := ProtocolCompare(before, after); !known[v] {
				t.Errorf("verdict %q is outside the closed vocabulary", v)
			}
		}
	})
}

func TestProtocolBarIsReportedAlongsideTheVerdict(t *testing.T) {
	// Design L204: the before-arm spread, the effect size the bar demands and the bar's own
	// false-pass rate under a pure-noise null are printed beside the verdict, so a pass is read for
	// what it is rather than as a discovery.
	t.Run("the before-arm spread", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			before []int64
			want   int
		}{
			{"a 1.6x spread", []int64{100_000, 110_000, 120_000, 130_000, 160_000}, 160},
			{"a flat arm", []int64{100_000, 100_000, 100_000, 100_000, 100_000}, 100},
			{"an empty arm", nil, 0},
			{"a zero-floor arm", []int64{0, 100_000, 100_000, 100_000, 100_000}, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := ProtocolSpreadPct(tc.before); got != tc.want {
					t.Errorf("ProtocolSpreadPct = %d, want %d", got, tc.want)
				}
			})
		}
	})

	t.Run("the reduction the bar demands", func(t *testing.T) {
		for _, tc := range []struct {
			name   string
			before []int64
			want   int
		}{
			{"median 120000 over min 100000", []int64{100_000, 110_000, 120_000, 130_000, 140_000}, 16},
			{"a flat arm demands nothing", []int64{100_000, 100_000, 100_000, 100_000, 100_000}, 0},
			{"an empty arm", nil, 0},
		} {
			t.Run(tc.name, func(t *testing.T) {
				if got := ProtocolRequiredReductionPct(tc.before); got != tc.want {
					t.Errorf("ProtocolRequiredReductionPct = %d, want %d", got, tc.want)
				}
			})
		}
	})

	t.Run("the bar's null false-pass odds", func(t *testing.T) {
		// Under exchangeability the bar passes exactly when the three cheapest of the ten runs all
		// land in the after arm: C(7,2) of the C(10,5) splits, ~8.3%.
		favourable, total := ProtocolNullFalsePassOdds(ProtocolArmSize, ProtocolArmSize)
		if favourable != 21 || total != 252 {
			t.Errorf("odds = %d/%d, want 21/252", favourable, total)
		}
		if favourable, total := ProtocolNullFalsePassOdds(0, 5); favourable != 0 || total != 0 {
			t.Errorf("odds = %d/%d, want 0/0 for a degenerate arm", favourable, total)
		}
	})
}
