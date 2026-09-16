package tokenomics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// The decision half of #668 K9 and K18, tested where it lives. The verb-layer tests
// (internal/cmd/prime_advisory_test.go, subagent_occupancy_test.go) drive these functions through a
// factory fixture, which proves the WIRING and can only reach the band edges by moving an
// occupancy snapshot around. The bands themselves are arithmetic and are pinned here, at the edge,
// where an off-by-one is a one-line case rather than a fixture.

// advisoryPolicy resolves a real policy from a real config so a spelling the loader would reject
// cannot make a band look armed here and be inert in a factory.
func advisoryPolicy(t *testing.T, marginPct, minRuns int, mechanisms map[Mechanism]string) Policy {
	t.Helper()
	cfg := config.TokenomicsConfig{
		Enabled: "on", AdmissionMarginPct: marginPct, LearnedMinRuns: minRuns,
	}
	fields := map[Mechanism]*string{
		MechanismBudget: &cfg.Budget, MechanismThrift: &cfg.Thrift,
		MechanismDispatch: &cfg.Dispatch, MechanismInterview: &cfg.Interview,
		MechanismEffort: &cfg.Effort, MechanismEscalate: &cfg.Escalate,
	}
	for m, v := range mechanisms {
		f, ok := fields[m]
		if !ok {
			t.Fatalf("mechanism %q is not in the closed vocabulary", m)
		}
		*f = v
	}
	return ResolvePolicy(true, cfg)
}

func firedMechanisms(triggers []AdvisoryTrigger) map[Mechanism]bool {
	fired := map[Mechanism]bool{}
	for _, tr := range triggers {
		fired[tr.Mechanism] = true
	}
	return fired
}

// TestAdvisoriesBands walks the bands against ONE window, moving only the operand each band
// claims to read. A band that read something else would show up as a neighbour firing.
//
// The effort mechanism is armed throughout and is expected to fire NOWHERE. That is #678 K5's
// deletion asserted at the arithmetic, and it is the strongest form of it: the cases that used to
// counsel effort still supply the operands that fired it — a step larger than the headroom, one token
// past free, a full session with a large step — and the answer is now nothing. A band restored under
// any spelling turns those rows red rather than passing as a bonus trigger.
func TestAdvisoriesBands(t *testing.T) {
	const window = 200_000
	// A 10% margin puts the thrift ceiling at 90% — 180,000 tokens of this window.
	p := advisoryPolicy(t, 10, 1, map[Mechanism]string{
		MechanismThrift: "on", MechanismDispatch: "on", MechanismEffort: "on",
	})
	w := Window{Tokens: window}

	cases := []struct {
		name     string
		occupied int64
		appetite int64
		runs     int
		want     []Mechanism
	}{
		{
			name: "an empty session with no history counsels nothing",
			// Every band needs either occupancy at the ceiling or a trusted appetite; a factory that
			// has learned nothing and just started must be left alone.
			occupied: 0, appetite: 0, runs: 0, want: nil,
		},
		{
			name:     "one token below the ceiling is not at the ceiling",
			occupied: 179_999, appetite: 0, runs: 0, want: nil,
		},
		{
			name: "exactly at the ceiling counsels thrift",
			// `>=`, so a ceiling of exactly N fires at exactly N — the same rule the boundary's
			// handoff_pct comparison follows.
			occupied: 180_000, appetite: 0, runs: 0, want: []Mechanism{MechanismThrift},
		},
		{
			name: "a step larger than the headroom counsels nothing here",
			// 120,000 used leaves 80,000; a 150,000-token step does not fit. This used to be the effort
			// band. The fact is still true and is still acted on — the budget mechanism refuses or
			// hands off on it — but it is no longer a reason to counsel a shallower reasoning depth,
			// because how deeply a step should be reasoned is a property of the step and not of how much
			// room happens to be left in front of it (#678 AC-2).
			occupied: 120_000, appetite: 150_000, runs: 3, want: nil,
		},
		{
			name: "a step that fits but only just counsels serialization",
			// 80,000 free against a 50,000 step: it fits, and a second one would not.
			occupied: 120_000, appetite: 50_000, runs: 3, want: []Mechanism{MechanismDispatch},
		},
		{
			name:     "a step with room for two of itself counsels nothing",
			occupied: 120_000, appetite: 39_999, runs: 3, want: nil,
		},
		{
			name: "the dispatch band's lower edge is inclusive",
			// appetite == free is the boundary between "does not fit" and "fits exactly once".
			occupied: 120_000, appetite: 80_000, runs: 3, want: []Mechanism{MechanismDispatch},
		},
		{
			// One token past the dispatch band's upper edge, which is now the top of the range: past
			// "fits, but only just" there is nothing left for this surface to counsel.
			name:     "one token more than free leaves the dispatch band",
			occupied: 120_000, appetite: 80_001, runs: 3, want: nil,
		},
		{
			name: "an appetite below the run floor is a number, not a prediction",
			// The same operands as the over-headroom case, with one prior run against a floor of two, so
			// it stops at the trust guard rather than at any band.
			occupied: 120_000, appetite: 150_000, runs: 1, want: nil,
		},
		{
			name: "a full session with a large step counsels thrift alone",
			// Both facts are still true — "you are nearly full" and "this step is large" — and only the
			// first of them is counsel this surface still gives.
			occupied: 190_000, appetite: 150_000, runs: 3,
			want: []Mechanism{MechanismThrift},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			policy := p
			if tc.runs == 1 && tc.appetite > 0 && len(tc.want) == 0 {
				policy = advisoryPolicy(t, 10, 2, map[Mechanism]string{
					MechanismThrift: "on", MechanismDispatch: "on", MechanismEffort: "on",
				})
			}
			got := firedMechanisms(Advisories(w,
				Occupancy{Tokens: tc.occupied, Known: true},
				Appetite{Tokens: tc.appetite, Runs: tc.runs, Known: tc.appetite > 0},
				policy))

			want := firedMechanisms(nil)
			for _, m := range tc.want {
				want[m] = true
			}
			for _, m := range Mechanisms() {
				if got[m] != want[m] {
					t.Errorf("%s fired = %v, want %v (all fired: %v)", m, got[m], want[m], got)
				}
			}
		})
	}
}

// TestEffortIsNotAWindowBand is #678 AC-2 and AC-3 held at the file that used to break them: this
// surface must contain no mention of the effort mechanism at all.
//
// The behavioural version lives in TestAdvisoriesBands above, and it is not sufficient on its own. A
// band restored under a different arithmetic — a different threshold, a different operand pair —
// would fire on rows that test does not enumerate, and it would fire for the reason the whole issue
// exists to remove: how deeply a step should be reasoned would once again depend on how much room
// happens to be in front of it. What has to be pinned is that the operand set of this file cannot
// reach an effort decision at all, which is a claim about the source and not about any one call.
//
// Structural rather than a text grep so a mention inside a comment is not a failure. The file's own
// prose explains where the actuator went, and that explanation is the thing a future reader needs
// most.
func TestEffortIsNotAWindowBand(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "advisory.go", nil, 0)
	if err != nil {
		t.Fatalf("parse advisory.go: %v", err)
	}

	found := 0
	ast.Inspect(file, func(n ast.Node) bool {
		id, ok := n.(*ast.Ident)
		if ok && id.Name == "MechanismEffort" {
			found++
			t.Errorf("%s: advisory.go names MechanismEffort. The effort actuator is keyed on a step's "+
				"learned generation baseline at the launch legs (#678 K5), where a level can be applied "+
				"rather than suggested. A band here can only key it on a window, which makes token "+
				"efficiency conditional on scarcity again",
				fset.Position(id.Pos()))
		}
		return true
	})

	// Non-vacuity: the same scan over a file that DOES name the mechanism must find it, or a scan
	// that silently parsed nothing would read as a pass.
	control := token.NewFileSet()
	policySrc, err := parser.ParseFile(control, "policy.go", nil, 0)
	if err != nil {
		t.Fatalf("parse policy.go: %v", err)
	}
	var controlHits int
	ast.Inspect(policySrc, func(n ast.Node) bool {
		if id, ok := n.(*ast.Ident); ok && id.Name == "MechanismEffort" {
			controlHits++
		}
		return true
	})
	if controlHits == 0 {
		t.Fatal("the control scan found no MechanismEffort in policy.go, where the vocabulary is " +
			"declared; this guard is scanning nothing and would stay green with the band restored")
	}
}

// TestAdvisoriesRefuseAbsentEvidence is step_context.go's rule restated for this surface: absence
// must never arm an action. An advisory injected into a context on the strength of a reading nobody
// took is exactly that.
func TestAdvisoriesRefuseAbsentEvidence(t *testing.T) {
	p := advisoryPolicy(t, 10, 1, map[Mechanism]string{
		MechanismThrift: "on", MechanismDispatch: "on", MechanismEffort: "on",
	})
	full := Occupancy{Tokens: 190_000, Known: true}
	app := Appetite{Tokens: 150_000, Runs: 3, Known: true}

	if got := Advisories(Window{Tokens: 200_000}, Occupancy{Tokens: 190_000}, app, p); got != nil {
		t.Errorf("an UNKNOWN occupancy counselled %v; a number nobody measured is not evidence", got)
	}
	if got := Advisories(Window{}, full, app, p); got != nil {
		t.Errorf("an unresolved window counselled %v; every band is a fraction of it", got)
	}
	if got := Advisories(Window{Tokens: -1}, full, app, p); got != nil {
		t.Errorf("a negative window counselled %v", got)
	}
	off := advisoryPolicy(t, 10, 1, map[Mechanism]string{
		MechanismThrift: "off", MechanismDispatch: "off", MechanismEffort: "off",
	})
	if got := Advisories(Window{Tokens: 200_000}, full, app, off); got != nil {
		t.Errorf("every mechanism is off and %v fired", got)
	}
}

// TestAdvisoriesClampHostReadings pins the arithmetic against a host that reports more occupancy
// than the operator declared a window for. The projection is what reaches the agent's context, and
// a percentage above 100 with a negative headroom is counsel nobody can act on.
func TestAdvisoriesClampHostReadings(t *testing.T) {
	p := advisoryPolicy(t, 10, 1, map[Mechanism]string{MechanismThrift: "on"})
	got := Advisories(Window{Tokens: 200_000},
		Occupancy{Tokens: 250_000, Known: true}, Appetite{}, p)

	if len(got) != 1 {
		t.Fatalf("an over-full session counselled %d advisories, want thrift alone", len(got))
	}
	if got[0].Inputs.FreeTokens != 0 {
		t.Errorf("free = %d on an over-full window, want 0", got[0].Inputs.FreeTokens)
	}
	if got[0].Inputs.ProjectedPct != 100 {
		t.Errorf("projected = %.1f%%, want 100", got[0].Inputs.ProjectedPct)
	}
}

// TestAtLeastPct pins the non-strict 128-bit comparison the admission and thrift rules share. The overflow
// case is the reason it exists: value*100 overflows int64 for windows an operator's
// CLAUDE_CODE_MAX_CONTEXT_TOKENS declaration is allowed to reach, and a wrapped product compares as
// a small number — a full window read as an empty one.
func TestAtLeastPct(t *testing.T) {
	cases := []struct {
		name   string
		value  uint64
		window int64
		pct    int
		expect bool
	}{
		{"exactly at the threshold", 90, 100, 90, true},
		{"one below", 89, 100, 90, false},
		{"one above", 91, 100, 90, true},
		{"zero against a zero threshold", 0, 100, 0, true},
		{"anything against 100%", 99, 100, 100, false},
		{"exactly 100%", 100, 100, 100, true},
		{
			// The documented precondition, pinned as behaviour rather than left to a reader: against
			// an unresolved window everything is "at least" the ceiling, which is why every caller
			// refuses one before asking (TestAdvisoriesRefuseAbsentEvidence). A change here that made
			// this false would silently make those guards look redundant and invite their removal.
			name:  "an unresolved window is the caller's to refuse, not this function's",
			value: 5, window: 0, pct: 50, expect: true,
		},
		{
			// value*100 wraps int64 here; the 128-bit form does not.
			name:  "a window large enough to overflow the naive product",
			value: 1 << 62, window: 1 << 62, pct: 100, expect: true,
		},
		{"the same window, one token short", 1<<62 - 1, 1 << 62, 100, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := atLeastPct(tc.value, tc.window, tc.pct); got != tc.expect {
				t.Errorf("atLeastPct(%d, %d, %d) = %v, want %v", tc.value, tc.window, tc.pct, got, tc.expect)
			}
		})
	}
}
