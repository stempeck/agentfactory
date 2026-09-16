package tokenomics

import (
	"strings"
	"testing"
)

// representativeAdvisoryInputs are deliberately the widest plausible numerics — a 1M-class window
// and seven-figure token counts — so the rendered length assertion is measured against the longest
// substitution a real factory can produce, not against a convenient small one.
var representativeAdvisoryInputs = AdvisoryInputs{
	WindowTokens:   1_000_000,
	FreeTokens:     999_999,
	AppetiteTokens: 987_654,
	ProjectedPct:   99.9,
	PriorRuns:      1_234,

	// #678 K7's two operands, at the same widest-plausible scale for the same reason: a step that
	// re-read four files and generated ten thousand tokens would measure the efficiency template
	// against a substitution far shorter than one a long-lived formula-step really produces.
	RepeatReads:     9_876,
	GeneratedTokens: 987_654,
}

// TestAdvisoryTemplateLength is the terseness gate of the IMPLREADME's fourth acceptance
// criterion (ux.md:69: "each advisory ≤ ~150 tokens"). It is NOT the design's AC-4, which is
// cloud-inertness and lives in predicate_test.go — two documents number their criteria
// differently, and this file measures text while that one measures arithmetic. It walks the production registry rather than a re-typed list, so a template added in a
// later phase is covered with no test change — the same reason config's tokenomicsEnums returns
// its pairs from one function instead of duplicating the list at each use.
func TestAdvisoryTemplateLength(t *testing.T) {
	templates := AdvisoryTemplates()
	if len(templates) == 0 {
		t.Fatal("the advisory registry is empty; the length bound guards nothing")
	}

	// The estimator is the measuring stick, so it is checked before anything is measured with it.
	// An estimator that returned 0 would satisfy every bound trivially.
	if got := EstimateTokens("the quick brown fox jumps over the lazy dog"); got <= 0 {
		t.Fatalf("EstimateTokens(non-empty) = %d, want > 0; every length bound below would be vacuous", got)
	}
	if got := EstimateTokens(""); got != 0 {
		t.Errorf("EstimateTokens(\"\") = %d, want 0", got)
	}
	// The rounding DIRECTION is the conservative half of the heuristic and has to be pinned
	// separately: a bound measured with a truncating estimator under-counts every template that is
	// not a multiple of four characters, which is a budget that quietly grants three free
	// characters per advisory. The boundary pair below is what distinguishes the two spellings.
	for _, tc := range []struct {
		in   string
		want int
	}{{"abcd", 1}, {"abcde", 2}, {"   ", 0}} {
		if got := EstimateTokens(tc.in); got != tc.want {
			t.Errorf("EstimateTokens(%q) = %d, want %d (the estimator must round up)", tc.in, got, tc.want)
		}
	}

	// Keyed on Key() rather than Mechanism since #678 K7: two templates now share the thrift
	// mechanism, so a per-mechanism uniqueness check would fail on a registry that is correct. What
	// still must be unique is the KEY, and for a stronger reason than tidiness — RenderAdvisoryKey
	// returns the first match, so a duplicate key makes one template permanently unreachable.
	seen := map[string]bool{}
	for _, tpl := range templates {
		t.Run(tpl.Key(), func(t *testing.T) {
			if seen[tpl.Key()] {
				t.Fatalf("key %q appears twice in the registry; the second entry is unreachable", tpl.Key())
			}
			seen[tpl.Key()] = true

			if strings.TrimSpace(tpl.Text) == "" {
				t.Fatal("template text is empty")
			}
			if raw := EstimateTokens(tpl.Text); raw > AdvisoryTokenBudget {
				t.Errorf("unrendered template is ~%d tokens, budget is %d", raw, AdvisoryTokenBudget)
			}

			rendered, ok := RenderAdvisoryKey(tpl.Key(), representativeAdvisoryInputs)
			if !ok {
				t.Fatalf("RenderAdvisoryKey(%q) reported no template, but the registry lists one", tpl.Key())
			}
			if strings.Contains(rendered, "%!") {
				// Go writes %!d(string=…) and friends when the verbs and the arguments disagree.
				t.Fatalf("rendered advisory contains a formatting error: %q", rendered)
			}
			if got := EstimateTokens(rendered); got > AdvisoryTokenBudget {
				t.Errorf("rendered advisory is ~%d tokens, budget is %d:\n%s", got, AdvisoryTokenBudget, rendered)
			}
		})
	}
}

// TestAdvisorySubstitutionIsNumericOnly is the half that makes the length bound un-bypassable. A
// %s verb would let agent-controlled or config-controlled text into an injected surface, at which
// point no static length bound holds and the template stops being reviewable.
func TestAdvisorySubstitutionIsNumericOnly(t *testing.T) {
	templates := AdvisoryTemplates()
	if len(templates) == 0 {
		t.Fatal("the advisory registry is empty; the substitution rule guards nothing")
	}

	sawAnyVerb := false
	for _, tpl := range templates {
		t.Run(tpl.Key(), func(t *testing.T) {
			verbs := formatVerbs(tpl.Text)
			if len(verbs) == 0 {
				// Permitted — a template with no substitution is trivially numeric-only — but it
				// must not be the ONLY kind in the registry, which the package-level check below
				// enforces.
				return
			}
			sawAnyVerb = true
			// The permitted set is exactly Go's integer and floating-point verbs. %s, %v and %q
			// are absent by construction, and so is every other verb that can render a string.
			const numericVerbs = "bdoOxXeEfFgG"
			for _, v := range verbs {
				if !strings.ContainsRune(numericVerbs, rune(v)) {
					t.Errorf("template uses verb %%%c; only the numeric verbs %q are permitted, "+
						"because a text verb admits arbitrary content into an injected surface and "+
						"no static length bound survives it", v, numericVerbs)
				}
			}
		})
	}
	if !sawAnyVerb {
		t.Fatal("no template in the registry substitutes anything; the numeric-only rule guards nothing")
	}
}

// TestRenderAdvisoryUnknownMechanism pins the closed vocabulary: a mechanism with no template
// reports that fact rather than rendering an empty string a caller would inject blindly.
func TestRenderAdvisoryUnknownMechanism(t *testing.T) {
	if _, ok := RenderAdvisory(Mechanism("no-such-mechanism"), representativeAdvisoryInputs); ok {
		t.Error("RenderAdvisory reported a template for an unknown mechanism")
	}
	// Escalate and interview are deterministic mechanisms, not advisory ones — they act, they do
	// not counsel — so the registry must not carry text for them.
	for _, m := range []Mechanism{MechanismInterview, MechanismEscalate} {
		if _, ok := RenderAdvisory(m, representativeAdvisoryInputs); ok {
			t.Errorf("mechanism %q has an advisory template, but it is a deterministic mechanism", m)
		}
	}

	// #678 K7's reachability rule, which is what makes two thrift-family templates safe: a bare
	// mechanism name addresses the CAPACITY entry and can never address the composite-keyed one. Both
	// directions matter. If the bare name reached the efficiency entry, every capacity thrift trigger
	// would silently render generation counsel from operands it never assembled — two zeros and a run
	// count — and an operator would read it as a measurement.
	capacity, ok := RenderAdvisory(MechanismThrift, representativeAdvisoryInputs)
	if !ok {
		t.Fatal("the bare thrift mechanism addresses no template; capacity thrift counsel is unreachable")
	}
	if !strings.HasPrefix(capacity, "Thrift:") {
		t.Errorf("RenderAdvisory(thrift) = %q, want the capacity template; the composite-keyed entry "+
			"must not be reachable by mechanism name", capacity)
	}
	efficiency, ok := RenderAdvisoryKey(AdvisoryKeyEfficiencyThrift, representativeAdvisoryInputs)
	if !ok {
		t.Fatalf("%q addresses no template; #678 K7's counsel is unreachable", AdvisoryKeyEfficiencyThrift)
	}
	if efficiency == capacity {
		t.Error("the two thrift-family keys render the same text; one of them is shadowing the other")
	}
}

// formatVerbs extracts the conversion verb of every non-literal format directive in s. It skips
// %% (an escaped percent sign, not a directive), which the dispatch template uses to print a
// percentage.
func formatVerbs(s string) []byte {
	const flagsWidthPrec = "+-# 0123456789.*"
	var verbs []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '%' {
			continue
		}
		j := i + 1
		if j < len(s) && s[j] == '%' {
			i = j
			continue
		}
		for j < len(s) && strings.IndexByte(flagsWidthPrec, s[j]) >= 0 {
			j++
		}
		if j < len(s) {
			verbs = append(verbs, s[j])
			i = j
		}
	}
	return verbs
}

// TestAdvisoryTemplatesAreGolden is design-doc.md:327's "template text byte-fixed (snapshot test)",
// and it is the only thing in the repo that can observe a reworded advisory.
//
// The other two template tests bound a PROPERTY — length, and substitution being numeric-only — so
// both stay green through any rewrite that keeps the shape. That is not enough here. These strings
// are INJECTED into an agent's context, and the safety argument for injecting them at all is that
// they are fixed, reviewable and cannot be influenced by an agent or an operator. A test that
// derives its expectation from RenderAdvisory would restate that argument without checking it.
//
// So the literals are spelled out. Moving one is meant to be the deliberate act of editing an
// injected surface: change the registry, watch this fail, read the new words, then move the golden
// in the same commit. A test that is annoying to update is the correct cost here — the failure mode
// it exists to catch is an advisory whose meaning was inverted (\"one at a time\" to \"in parallel\")
// with nothing anywhere noticing.
// Keyed on Key() since #678 K7, for the same reason the length test is: the effort entry left the
// registry (its band was deleted — the actuator now reads a learned baseline at the launch legs) and
// the efficiency thrift arrived under a composite key. A golden still keyed on Mechanism could not
// hold both thrift entries at once, and the one it dropped would be an injected surface with no
// snapshot at all.
func TestAdvisoryTemplatesAreGolden(t *testing.T) {
	golden := map[string]string{
		string(MechanismBudget): "Context budget: this step has historically needed about %d tokens, and %d of a " +
			"%d-token window are free — projecting %.1f%% occupancy. Consider narrowing the scope " +
			"or handing off before you begin rather than part-way through.",
		string(MechanismThrift): "Thrift: this step projects %.1f%% of a %d-token window. Prefer targeted reads over " +
			"whole-file reads, and do not re-read what is already in context.",
		string(MechanismDispatch): "Serialization: this step projects %.1f%% of a %d-token window. Launch sub-agents one " +
			"at a time and wait for each to return before starting the next — a fresh specialist " +
			"begins at 0%% and spends its own window, but every concurrent one reports back into " +
			"this one.",
		AdvisoryKeyEfficiencyThrift: "Efficiency: prior runs of this step re-read %d files and generated about %d tokens " +
			"across %d runs. Read each file once; work from what is already in context.",
	}

	for _, tpl := range AdvisoryTemplates() {
		want, ok := golden[tpl.Key()]
		if !ok {
			t.Errorf("key %q has a template and no golden; a new injected surface was added "+
				"without anyone spelling out what it says", tpl.Key())
			continue
		}
		if tpl.Text != want {
			t.Errorf("%s template text changed.\n got: %q\nwant: %q\n\nThis text is injected into an "+
				"agent's context. If the new wording is intended, move the golden in the same commit.",
				tpl.Key(), tpl.Text, want)
		}
		delete(golden, tpl.Key())
	}
	for k := range golden {
		t.Errorf("golden names %q but the registry has no template for it; a counsel surface was "+
			"removed and this test would otherwise have stayed green", k)
	}
}
