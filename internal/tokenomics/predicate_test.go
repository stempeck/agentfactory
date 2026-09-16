package tokenomics

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// onPolicy is the posture every predicate test runs under unless it says otherwise: umbrella on,
// every mechanism at its shipped default. Tests that want inertness must earn it from the
// arithmetic, never from a toggle that was quietly left off.
func onPolicy(marginPct, minRuns int) Policy {
	return ResolvePolicy(true, config.TokenomicsConfig{
		Enabled: "on", Budget: "default", Thrift: "default", Dispatch: "default",
		Interview: "default", Effort: "default", Escalate: "default",
		AdmissionMarginPct: marginPct, LearnedMinRuns: minRuns,
	})
}

func trusted(tokens int64, runs int) Appetite {
	return Appetite{Tokens: tokens, Runs: runs, Known: true}
}

// TestAdmitClampsToBreaker proves the AC-4 clamp is WIRED INTO Admit, not merely defined (D4). This is
// the admit-then-die band #672 names: at margin 5 the raw ceiling is 95%, so a step projected at 87.5%
// is admitted and then recycled by the 85% breaker while it renders its own body. With the breaker
// injected the verdict must flip to no-fit; with none injected (pre-clamp) it stays admit — the flip
// is the proof the clamp decides, rather than a value that would have refused anyway.
func TestAdmitClampsToBreaker(t *testing.T) {
	w := ResolveWindow(map[string]string{config.EnvMaxContextTokens: "200000"}, 0)
	occ := Occupancy{Tokens: 0, Known: true}
	app := trusted(175_000, 3) // projected 87.5%: below raw ceiling 95%, above breaker 85%

	unclamped := Admit(w, occ, app, onPolicy(5, 1))
	if unclamped.Verdict != VerdictAdmit {
		t.Fatalf("no breaker injected: verdict = %s, want %s (raw ceiling 95%% admits 87.5%%) — premise broken",
			unclamped.Verdict, VerdictAdmit)
	}
	clamped := Admit(w, occ, app, onPolicy(5, 1).WithContextThreshold(85))
	if clamped.Verdict != VerdictNoFit {
		t.Errorf("breaker 85 injected: verdict = %s, want %s — a step the breaker will kill mid-body must "+
			"not be admitted (AC-4 admit-then-die band)", clamped.Verdict, VerdictNoFit)
	}
}

// TestAdmit_DoesNotDoubleCountBaseline is thread T1's AC-2: it ties the fold to the predicate.
// A step whose session started at 103485 and peaked at 108226 grew by 4741. Weighed against a real
// occupancy of 118254 in a 262144 window, the additive predicate must project on the MARGINAL 4741
// and admit — not on the absolute peak 108226, which re-counts the baseline the occupancy already
// carries and refuses a step that comfortably fits.
func TestAdmit_DoesNotDoubleCountBaseline(t *testing.T) {
	k := DigestKey{Formula: "design-v7", StepID: "phase-1", Model: "qwen3.8-27b"}
	d := NewDigest()
	d.Put(k, AggregateSamples(marginals(k, [2]int64{103_485, 108_226}), foldedAt))

	win := Window{Tokens: 262_144, Source: "declared"}
	occ := Occupancy{Tokens: 118_254, Known: true}
	policy := onPolicy(16, 1) // margin 16 ⇒ raw ceiling 84%

	// The appetite the additive decision reads. Marginal (4741) ⇒ (118254+4741)/262144 = 46.9% ⇒ admit.
	got := Admit(win, occ, d.AppetiteFor(k), policy)
	if got.Verdict != VerdictAdmit {
		t.Errorf("Admit(marginal appetite) = %s (%.1f%%), want %s — the step grew by 4741 and fits at ~47%%",
			got.Verdict, got.ProjectedPct, VerdictAdmit)
	}
	if got.ProjectedPct <= 46 || got.ProjectedPct >= 48 {
		t.Errorf("ProjectedPct = %.2f, want ~46.9 ((118254+4741)/262144) — the marginal arithmetic, not the "+
			"double-counted 86.4%%", got.ProjectedPct)
	}

	// Non-vacuity: fed the ABSOLUTE peak the same operands no-fit at 86.4%, so it is the marginal that
	// admits the step and not the fixture being trivially inside the ceiling.
	control := Admit(win, occ, trusted(108_226, 1), policy)
	if control.Verdict != VerdictNoFit {
		t.Errorf("control Admit(absolute peak) = %s (%.1f%%), want %s — if the double-counted operand also "+
			"fit, this test would prove nothing about the marginal", control.Verdict, control.ProjectedPct, VerdictNoFit)
	}
}

// TestPredicatesInertLargeWindow is AC-4: a 1M-class window can never trip the predicate, and the
// reason must be the arithmetic rather than a carve-out. The table therefore sweeps every operand
// the predicate reads — occupancy, learned appetite, operator margin, digest maturity — against
// three windows at once, so a large-window row that stayed green because of a `if window > X`
// short-circuit would show up as a small-window row that also stopped firing.
func TestPredicatesInertLargeWindow(t *testing.T) {
	// The 1M window is built the way production builds it (Gotcha 6): through K1's helper, from a
	// declared profile value. 200_000 comes back from the same helper with no declaration at all,
	// so neither number is a literal this package owns.
	cloudWindow := ResolveWindow(map[string]string{config.EnvMaxContextTokens: "1000000"}, 0)
	if cloudWindow.Tokens != 1_000_000 || cloudWindow.Source != config.WindowSourceDeclared {
		t.Fatalf("cloud window = %d/%s, want 1000000/declared", cloudWindow.Tokens, cloudWindow.Source)
	}
	localWindow := ResolveWindow(map[string]string{config.EnvMaxContextTokens: "262144"}, 0)
	fallbackWindow := ResolveWindow(nil, 0)
	if fallbackWindow.Source != config.WindowSourceFallback {
		t.Fatalf("fallback window source = %q, want %q", fallbackWindow.Source, config.WindowSourceFallback)
	}

	windows := []struct {
		name string
		w    Window
	}{
		{"cloud-1M", cloudWindow},
		{"local-262k", localWindow},
		{"fallback-200k", fallbackWindow},
	}
	// Occupancy and appetite are ABSOLUTE token counts, and that is the whole mechanism behind
	// AC-4: the work an agent does is the same size whether the backend gives it 200k or 1M, so
	// the pair that is decisive on 200k is inert on 1M purely because the denominator changed.
	//
	// Their upper bounds are the physical ones rather than arbitrary. An occupancy cannot exceed
	// the window it is measured in, and an appetite is a learned figure bounded the same way — the
	// additive decision reads the MARGINAL growth (peak − start), the fresh-session path the
	// absolute peak, and the marginal cannot exceed the peak, which cannot exceed the window it was
	// observed in. The largest window any of these operands could have been observed in is the local
	// 262k one. Sweeping past those bounds would not be a harder test, it would be an impossible
	// input, and a predicate that stayed inert on it would be one with a carve-out.
	occupancies := []int64{0, 40_000, 150_000, 190_000}
	appetites := []int64{1_000, 60_000, 180_000, 260_000}
	// Margins stay inside the range an operator would actually choose. 100 is admitted by
	// config (startup.go:209-215, "a margin of the entire window is a coherent way to say
	// 'admit nothing'") and is covered by its own subtest below, because at 100 the headroom is
	// zero at EVERY window size — which is the proof it is an operator choice and not a
	// large-window exception.
	margins := []int{0, 10, 25, 50}
	digests := []struct {
		name string
		app  func(int64) Appetite
	}{
		{"trusted", func(tok int64) Appetite { return trusted(tok, 5) }},
		{"below-min-runs", func(tok int64) Appetite { return trusted(tok, 1) }},
		{"absent", func(int64) Appetite { return Appetite{} }},
	}

	firedSomewhere := false
	for _, win := range windows {
		for _, occ := range occupancies {
			for _, app := range appetites {
				for _, margin := range margins {
					for _, dg := range digests {
						p := onPolicy(margin, 2)
						got := Admit(win.w, Occupancy{Tokens: occ, Known: true}, dg.app(app), p)
						name := win.name + "/occ=" + itoa(occ) + "/app=" + itoa(app) +
							"/margin=" + itoa(int64(margin)) + "/" + dg.name
						t.Run(name, func(t *testing.T) {
							if win.w.Tokens >= 1_000_000 && got.Verdict == VerdictNoFit {
								t.Errorf("Admit(%s) = %s (projected %.2f%%, headroom %.2f%%); a 1M-class window must never no-fit",
									name, got.Verdict, got.ProjectedPct, got.HeadroomPct)
							}
						})
						if got.Verdict == VerdictNoFit {
							firedSomewhere = true
						}
					}
				}
			}
		}
	}

	// Anti-vacuity. Without this, "never fires on 1M" is satisfied by a predicate that is
	// `return VerdictAdmit` — the exact bug the AC exists to prevent.
	if !firedSomewhere {
		t.Fatal("no row in the matrix produced a no-fit verdict; the inertness claim proves nothing")
	}

	t.Run("small window with the same operands does fire", func(t *testing.T) {
		p := onPolicy(10, 2)
		occ := Occupancy{Tokens: 150_000, Known: true}
		app := trusted(60_000, 5)
		small := Admit(fallbackWindow, occ, app, p)
		big := Admit(cloudWindow, occ, app, p)
		if small.Verdict != VerdictNoFit {
			t.Errorf("200k window: Admit = %s (projected %.2f%%), want %s", small.Verdict, small.ProjectedPct, VerdictNoFit)
		}
		if big.Verdict != VerdictAdmit {
			t.Errorf("1M window, identical operands: Admit = %s (projected %.2f%%), want %s", big.Verdict, big.ProjectedPct, VerdictAdmit)
		}
	})

	t.Run("exactly at the headroom boundary admits", func(t *testing.T) {
		// 180_000 of a 200_000 window at a 10% margin is projected 90.00% against headroom
		// 90.00%. This row exists to kill the `>` -> `>=` mutation: at the boundary the step
		// still fits, and only strictly exceeding the headroom is a no-fit.
		p := onPolicy(10, 2)
		at := Admit(Window{Tokens: 200_000, Source: config.WindowSourceDeclared},
			Occupancy{Tokens: 120_000, Known: true}, trusted(60_000, 5), p)
		if at.Verdict != VerdictAdmit {
			t.Errorf("at the boundary: Admit = %s (projected %.2f%%, headroom %.2f%%), want %s",
				at.Verdict, at.ProjectedPct, at.HeadroomPct, VerdictAdmit)
		}
		over := Admit(Window{Tokens: 200_000, Source: config.WindowSourceDeclared},
			Occupancy{Tokens: 120_001, Known: true}, trusted(60_000, 5), p)
		if over.Verdict != VerdictNoFit {
			t.Errorf("one token past the boundary: Admit = %s (projected %.2f%%), want %s",
				over.Verdict, over.ProjectedPct, VerdictNoFit)
		}
	})

	t.Run("margin of 100 is an operator choice, not a window carve-out", func(t *testing.T) {
		// config admits 100 as "admit nothing". The property that matters for AC-4 is that it
		// behaves IDENTICALLY at every window size — a 1M window is not treated specially, which
		// is exactly what having no profile classifier means.
		p := onPolicy(100, 2)
		for _, win := range windows {
			got := Admit(win.w, Occupancy{Tokens: 1, Known: true}, trusted(1, 5), p)
			if got.Verdict != VerdictNoFit {
				t.Errorf("%s at margin 100: Admit = %s, want %s at every window size",
					win.name, got.Verdict, VerdictNoFit)
			}
		}
	})
}

// TestPredicateDegradesToObservation covers the cold-start and missing-reading rows the phase AC
// names alongside the large-window matrix. Each of these must be a THIRD answer, never a quiet
// admit and never a no-fit: an intervention fired on absent data is the failure mode that makes a
// capacity policy untrustworthy.
func TestPredicateDegradesToObservation(t *testing.T) {
	win := Window{Tokens: 200_000, Source: config.WindowSourceDeclared}
	// Operands chosen so that a TRUSTED digest with a known reading would no-fit; every row below
	// therefore proves the degradation, not a coincidence.
	hot := Occupancy{Tokens: 190_000, Known: true}
	fat := trusted(100_000, 5)

	if base := Admit(win, hot, fat, onPolicy(10, 2)); base.Verdict != VerdictNoFit {
		t.Fatalf("control row: Admit = %s, want %s (the degradation rows below prove nothing otherwise)", base.Verdict, VerdictNoFit)
	}

	rows := []struct {
		name       string
		occ        Occupancy
		app        Appetite
		policy     Policy
		wantReason string
	}{
		{"no occupancy reading fails open", Occupancy{}, fat, onPolicy(10, 2), ReasonNoOccupancy},
		{"a zero reading is not a missing reading", Occupancy{Tokens: 0, Known: true}, fat, onPolicy(10, 2), ""},
		{"cold start: no digest at all", hot, Appetite{}, onPolicy(10, 2), ReasonNoLearnedData},
		{"cold start: fewer runs than learned_min_runs", hot, trusted(100_000, 1), onPolicy(10, 2), ReasonBelowMinRuns},
		{"budget mechanism off", hot, fat, ResolvePolicy(true, config.TokenomicsConfig{
			Enabled: "on", Budget: "off", AdmissionMarginPct: 10, LearnedMinRuns: 2,
		}), ReasonMechanismOff},
		{"umbrella off", hot, fat, ResolvePolicy(false, config.TokenomicsConfig{
			Enabled: "on", Budget: "on", AdmissionMarginPct: 10, LearnedMinRuns: 2,
		}), ReasonMechanismOff},
		{"window unresolvable", hot, fat, onPolicy(10, 2), ReasonNoWindow},
	}

	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			w := win
			if tc.wantReason == ReasonNoWindow {
				w = Window{Tokens: 0, Source: config.WindowSourceFallback}
			}
			got := Admit(w, tc.occ, tc.app, tc.policy)
			if tc.wantReason == "" {
				// The "zero reading" row is the inverse control: a known zero must be treated as
				// real data and flow into the arithmetic, never as an absent reading.
				if got.Verdict == VerdictObserve {
					t.Fatalf("a known-zero occupancy degraded to %s (%s); a false with a 0 must never read as 'no reading'", got.Verdict, got.Reason)
				}
				return
			}
			if got.Verdict != VerdictObserve {
				t.Errorf("Admit = %s, want %s", got.Verdict, VerdictObserve)
			}
			if got.Reason != tc.wantReason {
				t.Errorf("Reason = %q, want %q", got.Reason, tc.wantReason)
			}
		})
	}
}

// TestPredicateIsScaleInvariant states AC-4 as a PROPERTY rather than as a fixture, and that is the
// difference that matters. The matrix above asserts "no no-fit at 1M", which a carve-out satisfies
// just as well as the arithmetic does. This asserts the reason: multiply the window, the occupancy
// and the learned appetite by the same k and the verdict must not move, because the predicate sees
// only their ratio. A `if window >= 500_000 { admit }` short-circuit passes the matrix and fails
// here, because it changes the answer for the scaled row while leaving the base row alone.
func TestPredicateIsScaleInvariant(t *testing.T) {
	base := Window{Tokens: 200_000, Source: config.WindowSourceDeclared}
	factors := []int64{2, 5, 10, 1000}
	occupancies := []int64{0, 40_000, 150_000, 190_000}
	appetites := []int64{1_000, 60_000, 180_000, 260_000}
	margins := []int{0, 10, 25, 50, 100}

	saw := map[Verdict]int{}
	for _, occ := range occupancies {
		for _, app := range appetites {
			for _, margin := range margins {
				p := onPolicy(margin, 2)
				want := Admit(base, Occupancy{Tokens: occ, Known: true}, trusted(app, 5), p)
				saw[want.Verdict]++
				for _, k := range factors {
					scaled := Admit(
						Window{Tokens: base.Tokens * k, Source: base.Source},
						Occupancy{Tokens: occ * k, Known: true},
						trusted(app*k, 5), p)
					// Only the verdict is compared. ProjectedPct is a float carried for display,
					// and demanding bit-identical doubles across a 1000x scaling would be asserting
					// something about IEEE rounding rather than about the policy.
					if scaled.Verdict != want.Verdict {
						t.Errorf("occ=%d app=%d margin=%d: window %d = %s but window %d = %s; the verdict moved under a pure change of scale",
							occ, app, margin, base.Tokens, want.Verdict, base.Tokens*k, scaled.Verdict)
					}
				}
			}
		}
	}

	// Anti-vacuity: an `Admit` that answered one verdict for everything would be trivially scale
	// invariant. Both real verdicts have to appear in the base sweep for the property to bite.
	if saw[VerdictAdmit] == 0 || saw[VerdictNoFit] == 0 {
		t.Fatalf("the base sweep produced %d admits and %d no-fits; invariance over a constant answer proves nothing",
			saw[VerdictAdmit], saw[VerdictNoFit])
	}
}

// TestPredicateClampsItsOperands proves the clamps are wired into the DECISION and not merely
// exported. TestClamps below calls them directly, which an Admit that never invoked them would
// still pass — so every row here is chosen to invert the verdict when a clamp is removed.
//
// The inputs look absurd on purpose. Phase 4 feeds this predicate values derived from recorded
// telemetry rather than from validated config, and a corrupt counter is exactly a negative or
// impossibly large token count.
func TestPredicateClampsItsOperands(t *testing.T) {
	win := Window{Tokens: 200_000, Source: config.WindowSourceDeclared}

	rows := []struct {
		name   string
		occ    int64
		app    int64
		margin int
		want   Verdict
	}{
		// Unclamped these two cancel to a projected zero and admit everything. The negative
		// occupancy is the shape a wrapped or garbage counter takes.
		{"a negative occupancy cannot buy headroom", -1_000_000_000, 1_000_000_000, 10, VerdictNoFit},
		// Unclamped, an occupancy larger than the window it was measured in refuses a step that
		// physically fits; clamped, the reading is capped at the window and the step is admitted.
		{"an occupancy past the window is capped at the window", 500_000, 0, 0, VerdictAdmit},
		// One token of appetite past the window, at a margin of zero: unclamped this is 100.0005%
		// and a no-fit, clamped it is exactly 100% and therefore still a fit.
		{"an appetite past the window is capped at the window", 0, 200_001, 0, VerdictAdmit},
	}
	for _, tc := range rows {
		t.Run(tc.name, func(t *testing.T) {
			got := Admit(win, Occupancy{Tokens: tc.occ, Known: true}, trusted(tc.app, 5), onPolicy(tc.margin, 2))
			if got.Verdict != tc.want {
				t.Errorf("Admit(occ=%d, app=%d, margin=%d) = %s (projected %.4f%%, headroom %.2f%%), want %s",
					tc.occ, tc.app, tc.margin, got.Verdict, got.ProjectedPct, got.HeadroomPct, tc.want)
			}
		})
	}
}

// TestPredicateSurvivesAnEnormousWindow guards the arithmetic itself. The obvious int64 spelling of
// the comparison — projected*100 > window*headroomPct — wraps NEGATIVE once the window passes
// MaxInt64/100, at which point every comparison is true and the largest windows in the factory
// refuse everything: the exact inversion of AC-4, reached by an operator typing one extra zero into
// CLAUDE_CODE_MAX_CONTEXT_TOKENS.
//
// The window here is constructed directly rather than through ResolveWindow because the point is
// the multiplication, not the resolution — and a value this size is a typo, not a declaration any
// backend would honour.
func TestPredicateSurvivesAnEnormousWindow(t *testing.T) {
	huge := Window{Tokens: 9_000_000_000_000_000_000, Source: config.WindowSourceDeclared}
	got := Admit(huge, Occupancy{Tokens: 1_000_000, Known: true}, trusted(1_000_000, 5), onPolicy(10, 2))
	if got.Verdict != VerdictAdmit {
		t.Errorf("a 2M-token step in a %d-token window = %s (projected %.6f%%), want %s; the headroom comparison overflowed",
			huge.Tokens, got.Verdict, got.ProjectedPct, VerdictAdmit)
	}
	// The control: the same window must still be capable of a no-fit, so the row above is not
	// passing because huge windows short-circuit to admit.
	full := Admit(huge, Occupancy{Tokens: huge.Tokens, Known: true}, trusted(1, 5), onPolicy(10, 2))
	if full.Verdict != VerdictNoFit {
		t.Errorf("a fully occupied %d-token window = %s, want %s", huge.Tokens, full.Verdict, VerdictNoFit)
	}

	// The row above stops one token short of the second overflow. Each TERM is clamped to the
	// window, so the SUM reaches twice it — and at this window size that is past MaxInt64. The
	// verdict survives a wrap by two's-complement accident, but ProjectedPct is the field a surface
	// shows an operator, and a 200%-full window rendered as -4.96% is a working shown wrongly.
	both := Admit(huge, Occupancy{Tokens: huge.Tokens, Known: true}, trusted(huge.Tokens, 5), onPolicy(10, 2))
	if both.Verdict != VerdictNoFit {
		t.Errorf("a doubly-oversubscribed %d-token window = %s, want %s", huge.Tokens, both.Verdict, VerdictNoFit)
	}
	if both.ProjectedPct < 0 {
		t.Errorf("ProjectedPct = %v for a window filled twice over; the projected sum wrapped negative", both.ProjectedPct)
	}
	if both.ProjectedPct < 199 || both.ProjectedPct > 201 {
		t.Errorf("ProjectedPct = %v, want ~200 for occupancy and appetite each equal to the window", both.ProjectedPct)
	}
}

// TestPredicateIsPure pins the property that makes the whole decision matrix table-testable: the
// answer is a function of the arguments alone. A clock read or a filesystem read inside Admit
// would not fail any single-call assertion above, but it would fail this one.
func TestPredicateIsPure(t *testing.T) {
	win := Window{Tokens: 262_144, Source: config.WindowSourceDeclared}
	occ := Occupancy{Tokens: 100_000, Known: true}
	app := trusted(80_000, 4)
	p := onPolicy(10, 2)

	first := Admit(win, occ, app, p)
	for i := 0; i < 8; i++ {
		if got := Admit(win, occ, app, p); got != first {
			t.Fatalf("call %d = %+v, first call = %+v; Admit is not a pure function of its arguments", i+2, got, first)
		}
	}
}

// TestClamps covers the bounds the design names (appetite in [0, resolved window], the
// learned_min_runs floor, the margin percentage). They exist because Phase 4 will feed this
// predicate values derived from recorded telemetry rather than from validated config, and a
// negative or window-exceeding operand must not be able to invert a decision.
func TestClamps(t *testing.T) {
	appetites := []struct{ in, window, want int64 }{
		{-1, 200_000, 0},
		{0, 200_000, 0},
		{50_000, 200_000, 50_000},
		{200_000, 200_000, 200_000},
		{400_000, 200_000, 200_000},
		{50_000, 0, 0},
	}
	for _, tc := range appetites {
		if got := ClampAppetite(tc.in, tc.window); got != tc.want {
			t.Errorf("ClampAppetite(%d, %d) = %d, want %d", tc.in, tc.window, got, tc.want)
		}
	}

	margins := []struct{ in, want int }{{-5, 0}, {0, 0}, {10, 10}, {100, 100}, {101, 100}}
	for _, tc := range margins {
		if got := ClampMarginPct(tc.in); got != tc.want {
			t.Errorf("ClampMarginPct(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}

	minRuns := []struct{ in, want int }{{-1, 1}, {0, 1}, {1, 1}, {7, 7}}
	for _, tc := range minRuns {
		if got := ClampMinRuns(tc.in); got != tc.want {
			t.Errorf("ClampMinRuns(%d) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

// itoa keeps the matrix subtest names free of a fmt dependency in the hot loop and, more
// importantly, free of any formatting that could vary between runs.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
