package tokenomics

import (
	"math/bits"

	"github.com/stempeck/agentfactory/internal/config"
)

// Window is a resolved context window and the provenance of the number. The two travel together
// for the reason config.ResolveContextWindow returns them together: "200000" is indistinguishable
// from a measurement, a stale declaration and a guess, and a surface that printed the figure alone
// could not be argued with.
type Window struct {
	Tokens int64
	Source string
}

// ResolveWindow wraps K1's resolver so this package never re-declares the fallback constant, and
// so a change to the precedence rules reaches the predicate without a second edit. It adds only the
// pairing of the two return values into one struct.
func ResolveWindow(profile map[string]string, hostReported int64) Window {
	tokens, source := config.ResolveContextWindow(profile, hostReported)
	return Window{Tokens: tokens, Source: source}
}

// Occupancy is how much of the window is already consumed. Known is separate from Tokens because a
// zero is a real reading — an agent at the start of a step genuinely occupies almost nothing — and
// collapsing "no reading" onto 0 would make an unmeasured agent look like the emptiest one in the
// factory, which is the direction that fires interventions on absent data.
type Occupancy struct {
	Tokens int64
	Known  bool
}

// Appetite is what this (formula, step, model) has historically needed, with the sample size that
// produced it. Runs travels with the number because the predicate's trust rule is about the sample,
// not the value.
//
// SessionsPerStep and Reconstructed travel with it for the same reason, one phase early: an
// open-time decision needs the session count to read a step above 1 as a no-fit signal without
// re-deriving it, and it needs to know whether the number in front of it was measured or inferred.
// Admit does not read either field — a mechanism that branched on them would be making a decision
// this phase has no mandate to make.
type Appetite struct {
	Tokens int64
	Runs   int
	Known  bool

	SessionsPerStep int
	Reconstructed   bool
}

// Verdict is the predicate's answer. Observe is a genuine third answer rather than a soft admit:
// "we have no basis to judge" and "we judged, and it fits" lead to different operator surfaces and
// different telemetry, and collapsing them is what makes a capacity policy untrustworthy.
type Verdict string

const (
	VerdictAdmit   Verdict = "admit"
	VerdictNoFit   Verdict = "no-fit"
	VerdictObserve Verdict = "observe"
)

// Why a Verdict was Observe. Empty on Admit and NoFit: those verdicts are conclusions, and a reason
// string on them would invite a caller to branch on prose.
const (
	ReasonMechanismOff  = "mechanism off"
	ReasonNoWindow      = "no resolved context window"
	ReasonNoOccupancy   = "no occupancy reading"
	ReasonNoLearnedData = "no learned data for this step"
	ReasonBelowMinRuns  = "fewer recorded runs than learned_min_runs"
)

// Decision is comparable on purpose: the purity test asserts two calls are identical with ==, and
// a map or slice field would quietly turn that into a pointer comparison.
type Decision struct {
	Verdict Verdict
	Reason  string

	// The arithmetic, carried so a surface can show its working. Both are zero on an Observe
	// verdict, where by definition no arithmetic ran.
	ProjectedPct float64
	HeadroomPct  float64
}

// Admit answers whether a step's projected occupancy fits the resolved window.
//
// The comparison is done in INTEGERS. The float percentages are carried for display only, and
// deciding on them would put a decision on the wrong side of a boundary for inputs that are
// exactly at it — 180000 of 200000 against a 10% margin is 90% by construction, and whether the
// double nearest 90.0 compares greater than 90.0 is not a policy question.
//
// The boundary admits: only strictly exceeding the headroom is a no-fit. A step that fills the
// margin exactly has, by the operator's own definition of the margin, still fit.
//
// There is no window-size branch anywhere below, and that absence IS AC-4. A 1M-class window
// cannot no-fit because the same absolute occupancy and the same learned appetite divide into a
// five-times larger denominator, not because anything here recognises the number.
func Admit(w Window, occ Occupancy, app Appetite, p Policy) Decision {
	if !p.On(MechanismBudget) {
		return observe(ReasonMechanismOff)
	}
	if w.Tokens <= 0 {
		return observe(ReasonNoWindow)
	}
	if !occ.Known {
		return observe(ReasonNoOccupancy)
	}
	if !app.Known {
		return observe(ReasonNoLearnedData)
	}
	if app.Runs < p.LearnedMinRuns {
		return observe(ReasonBelowMinRuns)
	}

	// The sum is UNSIGNED because it is the one value here that does not fit int64. Each TERM is
	// clamped to [0, window], which bounds the sum at 2·window — so a window declared anywhere
	// above MaxInt64/2 wraps it negative. uint64 holds 2·MaxInt64 exactly, so nothing is lost, and
	// ProjectedPct — the field whose whole purpose is to let a surface show its working — reports a
	// window that is 200% full as 200% rather than as a negative percentage.
	projected := uint64(ClampAppetite(occ.Tokens, w.Tokens)) + uint64(ClampAppetite(app.Tokens, w.Tokens))
	// The ceiling is clamped DOWN to the exhaustion breaker (AC-4, D4): a step admitted at a projected
	// occupancy above the breaker is one the breaker recycles while it renders its own body — the
	// admit-then-die band #672 names (admitted 83%, breaker-killed 86% against a 90% raw ceiling).
	// Clamping makes "admitted ⇒ projected <= breaker" hold for ANY operator margin. It is a no-op when
	// the breaker was not injected into the policy (ContextThresholdPct == 0), preserving prior behavior.
	headroomPct := EffectiveAdmissionCeilingPct(p.AdmissionMarginPct, p.ContextThresholdPct)

	d := Decision{
		ProjectedPct: float64(projected) / float64(w.Tokens) * 100,
		HeadroomPct:  float64(headroomPct),
	}
	if exceedsHeadroom(projected, w.Tokens, headroomPct) {
		d.Verdict = VerdictNoFit
		return d
	}
	d.Verdict = VerdictAdmit
	return d
}

// exceedsHeadroom answers `projected/window > headroomPct/100` exactly, in 128 bits.
//
// The naive int64 form — projected*100 > window*headroomPct — overflows once the window passes
// MaxInt64/100. The product wraps NEGATIVE, every comparison becomes true, and a huge window
// starts refusing everything: the precise inversion of the invariant this predicate exists to
// hold. config.ResolveContextWindow accepts any declared window up to MaxInt64, so an operator
// typo in CLAUDE_CODE_MAX_CONTEXT_TOKENS is all it takes to reach it.
//
// Widening rather than dividing keeps the boundary exact, which the boundary test depends on:
// division would round and a step that fills the margin precisely would stop admitting.
//
// Both products are total in 128 bits with no sign to reinterpret: projected is unsigned by type,
// window is known positive (Admit returns ReasonNoWindow before reaching here), and headroomPct is
// ClampMarginPct's output subtracted from 100, hence in [0,100].
func exceedsHeadroom(projected uint64, window int64, headroomPct int) bool {
	lhsHi, lhsLo := bits.Mul64(projected, 100)
	rhsHi, rhsLo := bits.Mul64(uint64(window), uint64(headroomPct))
	if lhsHi != rhsHi {
		return lhsHi > rhsHi
	}
	return lhsLo > rhsLo
}

// atLeastPct answers `value/window >= pct/100` exactly, in 128 bits. It is exceedsHeadroom's
// non-strict twin, widened for the same overflow reason that function's doc gives, and non-strict
// because the two comparisons are asking opposite questions: admission asks whether a projection
// BREACHES a ceiling, so the boundary must admit; thrift asks whether a session has REACHED one, so
// the boundary must trigger. A session sitting exactly on the ceiling is the case both rules are
// about, and it cannot be allowed to fall through neither.
//
// It inherits the twin's precondition and restates it because non-strictness makes the failure
// louder: window must be POSITIVE, and every caller refuses an unresolved window before reaching
// here (Advisories returns early). Against a zero window this answers true
// for any value — everything is at least 0% of nothing — which is the reading a caller that skipped
// its guard would experience as "every session is full".
func atLeastPct(value uint64, window int64, pct int) bool {
	lhsHi, lhsLo := bits.Mul64(value, 100)
	rhsHi, rhsLo := bits.Mul64(uint64(window), uint64(pct))
	if lhsHi != rhsHi {
		return lhsHi > rhsHi
	}
	return lhsLo >= rhsLo
}

func observe(reason string) Decision {
	return Decision{Verdict: VerdictObserve, Reason: reason}
}
