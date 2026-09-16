package tokenomics

// #678 K9 — the Measurement Protocol's verdict, and the only definition of "improves" the design
// has. Every other reading of the record log in this feature is a diagnostic; this is the bar.
//
// It lives here, in the pure package, for the same reason JudgeBand does: the arithmetic that
// decides whether a change worked must be reproducible from two slices of numbers, with no clock,
// no environment and no filesystem between the inputs and the answer. internal/telemetry already
// imports this package, so the split is also the only shape that compiles — the record-side fold
// (telemetry.MeasureRun) reduces StepEvents to the scalars this function takes, and this function
// never learns what a record looks like.

const (
	ProtocolPass = "pass"
	ProtocolFail = "fail"
	ProtocolVoid = "void"
)

// ProtocolArmSize is the five-run arm the protocol calls for, on both sides.
//
// Enforced here rather than only at the call site, because every figure the verb prints beside the
// verdict — the false-pass odds most of all — is a statement about a 5 + 5 split. A comparison run
// over four runs would print an 8.3% null rate that does not describe it, and a shorter arm is an
// absence of evidence rather than a fail: nothing was disproved, so nothing may be claimed.
const ProtocolArmSize = 5

// ProtocolCompare answers whether the after arm beat the before arm, and returns the two figures
// the answer rests on so the verdict can be checked by hand.
//
// median(after) < min(before), strictly. Not mean against mean, and not median against median:
// the arms are five runs of a stochastic generator, means are dominated by one long run, and
// median-against-median passes on a coin flip. Requiring the after arm's median to fall below the
// CHEAPEST before run makes the bar hard to clear by luck — under a pure-noise null it takes the
// three cheapest of the ten runs all landing in the after arm, which is 21 of 252 splits.
func ProtocolCompare(before, after []int64) (verdict string, medianAfter, minBefore int64) {
	if len(before) != ProtocolArmSize || len(after) != ProtocolArmSize {
		return ProtocolVoid, 0, 0
	}
	medianAfter, minBefore = median(after), minOf(before)
	if medianAfter < minBefore {
		return ProtocolPass, medianAfter, minBefore
	}
	return ProtocolFail, medianAfter, minBefore
}

// ProtocolSpreadPct reports the before arm's own dispersion as max*100/min — 160 for a 1.6x spread.
//
// Printed beside the verdict because it is the context that makes the verdict readable: an arm that
// varies by 1.6x on its own is telling the operator how much of any observed change is the
// generator rather than the intervention. Zero when the floor is not positive, which is the same
// "nothing was measured" spelling the aggregate uses.
func ProtocolSpreadPct(before []int64) int {
	lo, hi := minOf(before), maxOf(before)
	if len(before) == 0 || lo <= 0 {
		return 0
	}
	return int(hi * 100 / lo)
}

// ProtocolRequiredReductionPct reports how far a typical before run must fall to clear the bar:
// the distance from the before arm's median down to its minimum, as a percentage of the median.
//
// This is the effect size the protocol demands, and it is a property of the before arm alone — it
// is knowable before a single after run exists, which is exactly when an operator wants it.
func ProtocolRequiredReductionPct(before []int64) int {
	mid, lo := median(before), minOf(before)
	if len(before) == 0 || mid <= 0 || lo >= mid {
		return 0
	}
	return int((mid - lo) * 100 / mid)
}

// ProtocolNullFalsePassOdds reports how often the bar passes on noise alone, as an exact fraction.
//
// Under exchangeability every split of the pooled runs into arms is equally likely, and the bar
// passes exactly when the (afterN/2 + 1) cheapest pooled runs ALL land in the after arm — that many
// runs must sit below every before run for the after median to fall under the before minimum. The
// count of such splits is C(N-k, afterN-k) out of C(N, afterN).
//
// Returned as a fraction rather than a percentage because the rounding is the caller's to choose,
// and because an exact 21/252 is checkable where an 8.3 is not.
func ProtocolNullFalsePassOdds(beforeN, afterN int) (favourable, total int) {
	if beforeN <= 0 || afterN <= 0 {
		return 0, 0
	}
	n, k := beforeN+afterN, afterN/2+1
	return binomial(n-k, afterN-k), binomial(n, afterN)
}

func binomial(n, k int) int {
	if k < 0 || k > n {
		return 0
	}
	if k > n-k {
		k = n - k
	}
	result := 1
	for i := 0; i < k; i++ {
		result = result * (n - i) / (i + 1)
	}
	return result
}
