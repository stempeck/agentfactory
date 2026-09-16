package tokenomics

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// dispatchPolicy is the posture the backend-capacity gate runs under: umbrella on, dispatch at its
// shipped default (on), the given admission margin. A variant with dispatch explicitly off is built
// inline in the one row that needs it — inertness must be earned from arithmetic, never from a toggle
// quietly left off, so the default posture keeps the mechanism ON.
func dispatchPolicy(marginPct int) Policy {
	return ResolvePolicy(true, config.TokenomicsConfig{
		Enabled: "on", Budget: "default", Thrift: "default", Dispatch: "default",
		Interview: "default", Effort: "default", Escalate: "default",
		AdmissionMarginPct: marginPct, LearnedMinRuns: 2,
	})
}

func known(tokens int64) Occupancy { return Occupancy{Tokens: tokens, Known: true} }
func unknownOcc() Occupancy        { return Occupancy{Known: false} }
func declared(tokens int64) Window {
	return Window{Tokens: tokens, Source: config.WindowSourceDeclared}
}

// TestWithinBackendCapacity is AC-2/AC-6/AC-8 on the pure cross-session predicate. The pool is
// 200_000 declared and the margin 20, so the headroom is exactly 80% = 160_000 tokens: the round
// numbers make every boundary unambiguous, which the step demands of a divisor-under-test.
//
// `live` is every occupancy already sharing the backend INCLUDING the reservation slot the verb
// layer appends for the launch under consideration — so "orchestrator + one" is a two-entry list and
// "orchestrator + two" a three-entry one.
func TestWithinBackendCapacity(t *testing.T) {
	pool := declared(200_000)      // shared KV pool, e.g. lmstudio's 262_144 in production
	cloud := ResolveWindow(nil, 0) // no declaration ⇒ {200000, fallback}: same size, no capacity fact
	if cloud.Source != config.WindowSourceFallback {
		t.Fatalf("cloud window source = %q, want %q", cloud.Source, config.WindowSourceFallback)
	}
	bigDeclared := declared(1_000_000) // a large but DECLARED pool: still enforced (inertness is Source, not size)

	rows := []struct {
		name string
		pool Window
		live []Occupancy
		p    Policy
		want Verdict
	}{
		{"orchestrator plus one within capacity admits", pool,
			[]Occupancy{known(80_000), known(80_000)}, dispatchPolicy(20), VerdictAdmit},
		{"orchestrator plus two oversubscribes", pool,
			[]Occupancy{known(80_000), known(80_000), known(80_000)}, dispatchPolicy(20), VerdictNoFit},
		{"exactly at the headroom boundary admits", pool,
			[]Occupancy{known(100_000), known(60_000)}, dispatchPolicy(20), VerdictAdmit}, // sum 160_000 == 80%
		{"one token past the boundary no-fits", pool,
			[]Occupancy{known(100_000), known(60_001)}, dispatchPolicy(20), VerdictNoFit},
		{"cloud pool with no capacity fact is inert at every count", cloud,
			[]Occupancy{known(190_000), known(190_000), known(190_000)}, dispatchPolicy(20), VerdictObserve},
		{"a large but DECLARED pool is still enforced", bigDeclared,
			[]Occupancy{known(500_000), known(500_000), known(500_000)}, dispatchPolicy(20), VerdictNoFit},
		{"dispatch mechanism off is silent", pool,
			[]Occupancy{known(190_000), known(190_000)}, ResolvePolicy(true, config.TokenomicsConfig{
				Enabled: "on", Dispatch: "off", AdmissionMarginPct: 20, LearnedMinRuns: 2}), VerdictObserve},
		{"an unknown reading is skipped, decision proceeds on the lower bound (admits)", pool,
			[]Occupancy{known(80_000), unknownOcc(), known(80_000)}, dispatchPolicy(20), VerdictAdmit},
		{"an unknown reading cannot rescue a set the resolved sessions already breach", pool,
			[]Occupancy{known(90_000), known(90_000), unknownOcc()}, dispatchPolicy(20), VerdictNoFit},
	}

	firedSomewhere := false
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			got := WithinBackendCapacity(tc.pool, tc.live, tc.p)
			if got.Verdict != tc.want {
				t.Errorf("WithinBackendCapacity = %s (reason %q, projected %.2f%%, headroom %.2f%%), want %s",
					got.Verdict, got.Reason, got.ProjectedPct, got.HeadroomPct, tc.want)
			}
		})
		if tc.want == VerdictNoFit {
			firedSomewhere = true
		}
	}

	// Anti-vacuity, both directions. Without a no-fit row "never oversubscribes" is satisfied by
	// `return admit` — the exact pre-fix stub. Without an admit row it is satisfied by `return no-fit`.
	if !firedSomewhere {
		t.Fatal("no row produced a no-fit verdict; the capacity claim proves nothing")
	}
}

// TestWithinBackendCapacityInertnessIsArithmetic is the AC-6 protective assertion stated as a
// property: a pool of the SAME size is enforced when declared and inert when not, so inertness is the
// declared-source fact and not the window magnitude. A `if pool.Tokens >= X { admit }` carve-out
// would pass the size-varying rows above and fail here.
func TestWithinBackendCapacityInertnessIsArithmetic(t *testing.T) {
	over := []Occupancy{known(150_000), known(150_000)} // 300_000, well past any 200k headroom
	declaredPool := declared(200_000)
	hostPool := Window{Tokens: 200_000, Source: config.WindowSourceHost}

	if got := WithinBackendCapacity(declaredPool, over, dispatchPolicy(20)); got.Verdict != VerdictNoFit {
		t.Errorf("declared 200k pool oversubscribed = %s, want %s", got.Verdict, VerdictNoFit)
	}
	if got := WithinBackendCapacity(hostPool, over, dispatchPolicy(20)); got.Verdict != VerdictObserve {
		t.Errorf("host-sourced 200k pool (no capacity fact), identical operands = %s, want %s (inert)",
			got.Verdict, VerdictObserve)
	}
}

// TestEffectiveAdmissionCeilingClampsToBreaker is AC-4: the admission ceiling can never sit above the
// exhaustion breaker, because a step admitted above the breaker is one the breaker kills mid-body.
// The shipped defaults (margin 10, threshold 85) are the admit-then-die band this closes: raw ceiling
// 90 > breaker 85. The invariant is `effectiveCeiling <= threshold` for EVERY input.
func TestEffectiveAdmissionCeilingClampsToBreaker(t *testing.T) {
	rows := []struct {
		margin, threshold, want int
	}{
		{10, 85, 85}, // the shipped-default band: raw 90 clamped down to the breaker
		{16, 85, 84}, // below the breaker already: unchanged
		{20, 85, 80}, // comfortably below
		{5, 85, 85},  // raw 95 clamped to the breaker
		{10, 90, 90}, // an operator who raised the breaker: raw 90 == breaker, admitted
		{0, 85, 85},  // margin 0 (raw ceiling 100) still clamps to the breaker
	}
	for _, tc := range rows {
		got := EffectiveAdmissionCeilingPct(tc.margin, tc.threshold)
		if got != tc.want {
			t.Errorf("EffectiveAdmissionCeilingPct(margin=%d, threshold=%d) = %d, want %d",
				tc.margin, tc.threshold, got, tc.want)
		}
		if got > tc.threshold {
			t.Errorf("EffectiveAdmissionCeilingPct(margin=%d, threshold=%d) = %d, ABOVE the breaker %d; "+
				"that is the admit-then-die band AC-4 forbids", tc.margin, tc.threshold, got, tc.threshold)
		}
	}
}

// TestWithinBackendCapacityClampsToBreaker proves the AC-4 clamp is WIRED INTO the predicate, not
// merely defined beside it (D4). At margin 5 the raw ceiling is 95%; a Σ of 87.5% of the pool sits
// below that raw ceiling and would be admitted — but it sits ABOVE the 85% breaker, so a session at
// that share is one the breaker recycles mid-body. With the breaker injected the verdict must flip to
// no-fit; with no breaker injected (the pre-clamp default) it stays admit, which is exactly what makes
// this a proof that the clamp changed the decision rather than a tautology.
func TestWithinBackendCapacityClampsToBreaker(t *testing.T) {
	pool := declared(200_000)
	live := []Occupancy{known(175_000)} // 87.5% of the pool: below raw ceiling 95%, above breaker 85%

	unclamped := WithinBackendCapacity(pool, live, dispatchPolicy(5))
	if unclamped.Verdict != VerdictAdmit {
		t.Fatalf("no breaker injected: verdict = %s, want %s (raw ceiling 95%% admits 87.5%%) — the "+
			"test's own premise is broken if this is not an admit", unclamped.Verdict, VerdictAdmit)
	}
	clamped := WithinBackendCapacity(pool, live, dispatchPolicy(5).WithContextThreshold(85))
	if clamped.Verdict != VerdictNoFit {
		t.Errorf("breaker 85 injected: verdict = %s, want %s — the ceiling must clamp to the breaker so "+
			"a Σ the breaker would kill is refused, not admitted (AC-4)", clamped.Verdict, VerdictNoFit)
	}
}

// TestBackendVerdictIsComparable pins the property the record path depends on: BackendVerdict has no
// map or slice field, so two calls on the same inputs are == identical. A future field that broke
// this would silently turn the purity assertion into a pointer comparison.
func TestBackendVerdictIsComparable(t *testing.T) {
	pool := declared(200_000)
	live := []Occupancy{known(80_000), known(80_000)}
	first := WithinBackendCapacity(pool, live, dispatchPolicy(20))
	for i := 0; i < 4; i++ {
		if got := WithinBackendCapacity(pool, live, dispatchPolicy(20)); got != first {
			t.Fatalf("call %d = %+v, first = %+v; WithinBackendCapacity is not pure", i+2, got, first)
		}
	}
}
