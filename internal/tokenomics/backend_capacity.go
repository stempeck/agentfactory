package tokenomics

import "github.com/stempeck/agentfactory/internal/config"

// BackendVerdict is WithinBackendCapacity's answer. Like Decision it is comparable on purpose — the
// purity test asserts two calls are identical with ==, so no map or slice field may enter here — and
// it carries its own arithmetic so the refusal record (AC-3) can show the working that justified it.
type BackendVerdict struct {
	Verdict Verdict
	Reason  string

	// The arithmetic, carried for the record. PoolTokens is the declared shared capacity;
	// SummedTokens is Σ of the known live occupancies (including the reservation the caller appended
	// for the launch under consideration); the two percentages are for display only, both zero on an
	// Observe verdict where by definition no arithmetic ran.
	PoolTokens   int64
	SummedTokens uint64
	ProjectedPct float64
	HeadroomPct  float64
}

// ReasonUndeclaredPool is why a backend verdict was Observe rather than a refusal: the launcher's
// backend declared no capacity fact (its window did not resolve with WindowSourceDeclared), so there
// is nothing to divide and the gate is inert BY CONSTRUCTION — this is AC-6, and it is arithmetic
// (the absence of a declared pool), never a profile classifier or a model-name heuristic.
const ReasonUndeclaredPool = "backend declares no capacity fact"

// WithinBackendCapacity answers whether admitting one more launch keeps a shared backend pool within
// its declared capacity. It is the AC-2 predicate: the collapsing operand is the SUM of live
// contexts on one backend versus that backend's declared pool, not the launcher's own window.
//
// It is pure: the verb layer does the tmux.ListSessions + per-session reading gather and appends a
// structural reservation entry for the launch under consideration (a just-launched session has no
// reading of its own yet); this function only sums and compares. `live` is therefore every session
// occupancy already sharing the backend, INCLUDING that reservation slot.
//
// Three properties are load-bearing and each is arithmetic rather than a special case:
//   - Cloud inertness (AC-6): a pool whose window did not resolve with WindowSourceDeclared carries
//     no capacity fact, so no arithmetic runs and the verdict is Observe. Grouping by base URL only
//     PARTITIONS sessions; the declared-window source is what keeps cloud inert.
//   - Run-#1 (AC-2): the operand is occupancy alone — no learned appetite, no digest — so it fires
//     on a formula the factory has never seen.
//   - Fail open as a lower bound (AC-8): a session whose reading did not resolve (Known:false) is
//     skipped, never counted as zero and never refused on. The sum is then an honest LOWER bound, so
//     a refusal only happens when the resolved sessions ALONE already breach the pool — missing data
//     can only make the sum smaller, i.e. more likely to admit.
func WithinBackendCapacity(pool Window, live []Occupancy, p Policy) BackendVerdict {
	if !p.On(MechanismDispatch) {
		return BackendVerdict{Verdict: VerdictObserve, Reason: ReasonMechanismOff, PoolTokens: pool.Tokens}
	}
	if !declaredPool(pool) {
		return BackendVerdict{Verdict: VerdictObserve, Reason: ReasonUndeclaredPool, PoolTokens: pool.Tokens}
	}
	if pool.Tokens <= 0 {
		return BackendVerdict{Verdict: VerdictObserve, Reason: ReasonNoWindow}
	}

	// AC-8 fail-open lower bound: a session whose reading did not resolve (Known:false) is SKIPPED,
	// never counted as zero and never refused on. Missing data can only make the sum smaller — i.e.
	// more likely to admit — so a refusal only fires when the resolved sessions ALONE breach the pool.
	var summed uint64
	for _, occ := range live {
		if !occ.Known {
			continue
		}
		summed += uint64(ClampAppetite(occ.Tokens, pool.Tokens))
	}

	// The ceiling is clamped to the exhaustion breaker (AC-4, D4): admitting Σ up to a ceiling ABOVE
	// the breaker would seat a session the breaker kills mid-body — and for a shared KV backend the
	// pool IS the per-session window, so Σ <= breaker%·pool keeps every single session under the
	// breaker. EffectiveAdmissionCeilingPct is a no-op when the breaker was not injected (pre-clamp).
	headroomPct := EffectiveAdmissionCeilingPct(p.AdmissionMarginPct, p.ContextThresholdPct)
	v := BackendVerdict{
		PoolTokens:   pool.Tokens,
		SummedTokens: summed,
		ProjectedPct: float64(summed) / float64(pool.Tokens) * 100,
		HeadroomPct:  float64(headroomPct),
	}
	if exceedsHeadroom(summed, pool.Tokens, headroomPct) {
		v.Verdict = VerdictNoFit
		return v
	}
	v.Verdict = VerdictAdmit
	return v
}

// EffectiveAdmissionCeilingPct is the projected-occupancy percentage at or below which a step is
// admitted, clamped so it can never sit ABOVE the exhaustion breaker — the AC-4 no-admit-then-die
// guarantee. The raw ceiling is 100 - margin (predicate.go), but a step admitted at a ceiling above
// the breaker is a step the breaker will kill mid-body; clamping the effective ceiling down to the
// breaker threshold makes "admitted ⇒ not breaker-eligible" hold regardless of how the two
// operator-owned thresholds are configured. It reads both thresholds in one place so they cannot
// drift independently into contradiction.
func EffectiveAdmissionCeilingPct(admissionMarginPct, contextThresholdPct int) int {
	raw := 100 - ClampMarginPct(admissionMarginPct)
	// A non-positive threshold means the breaker was not injected into this Policy (the pre-clamp
	// default, and a value config never validates to — context_threshold_pct is 1-99). Clamping to it
	// would collapse the ceiling to "admit nothing"; instead it means "no clamp", so a decision site
	// that never set the breaker keeps the raw ceiling it had before this guarantee existed.
	if contextThresholdPct > 0 && raw > contextThresholdPct {
		return contextThresholdPct
	}
	return raw
}

// declaredPool is a tiny predicate kept beside the arithmetic that reads it: a backend carries a
// capacity fact only when its window resolved from an operator declaration. Named so the AC-6
// inertness reads as one sentence at every call site, and so the cloud-inertness interlock test can
// assert the gate keys on THIS and never on a profile name.
func declaredPool(pool Window) bool {
	return pool.Source == config.WindowSourceDeclared
}
