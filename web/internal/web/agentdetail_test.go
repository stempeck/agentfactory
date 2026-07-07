package web

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #500 Phase 3: the per-agent detail View. The web module is pure-Go with no JS/DOM runtime
// (web/go.mod has no require block), so — following the source-scan precedent in nav_test.go and
// web/internal/server/lint_test.go — these are SOURCE-LEVEL structural assertions over the embedded
// static assets. `readAsset` and `staticDir` are shared with nav_test.go (same package). Every
// assertion carries a self-negative proving the check is not vacuous.

// The retired placeholder literal. Scoped to static/app.js ONLY — it also lives across ~17 .designs/
// docs and in this test's own fixtures, which must NOT count (Gotcha 8 / peer review erratum #16).
const laterPhaseLiteral = "arrives in a later phase"

// (a) boot()'s poll branch is keyed on the parameterized "agent/" route (poll-only-while-open).
var reBootPollsAgentRoute = regexp.MustCompile(`function\s+boot\b[\s\S]*?currentRoute\.indexOf\('agent/'\)[\s\S]*?AgentDetailViewModel\.refresh`)

// (b) the snapshot pane (#agent-snapshot) is filled via textContent within a short window of being
// grabbed — never innerHTML (AC-3). `agent-snapshot')` matches only the pane grab, not the
// -note/-captured/-label siblings.
var rePaneTextContent = regexp.MustCompile(`agent-snapshot'\)[\s\S]{0,600}?textContent`)

// (c) innerHTML lint: EVERY innerHTML assignment module-asset-wide must be an empty-string clear.
// RE2 has no lookahead, so we count all assignments and all empty clears and require equality.
var reAnyInnerHTMLAssign = regexp.MustCompile(`innerHTML\s*=\s*[^=]`)
var reEmptyInnerHTMLAssign = regexp.MustCompile("innerHTML\\s*=\\s*(''|\"\"|" + "``" + ")")

// (d) dark cards are dimmed (ux.md U3-1): main.css must carry a .sign.dark rule for the
// 'sign s-idle dark' class darkCard emits.
var reDarkDimRule = regexp.MustCompile(`\.sign\.dark\b[^{]*\{`)

// AC-6 (site scope) — the "later phase" placeholder is gone from static/app.js.
func TestAppJS_NoLaterPhasePlaceholder(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	if n := strings.Count(appJS, laterPhaseLiteral); n != 0 {
		t.Errorf("static/app.js must not contain the %q placeholder, found %d occurrence(s)", laterPhaseLiteral, n)
	}
}

func TestAppJS_NoLaterPhasePlaceholder_SelfNegative(t *testing.T) {
	oldSite := `viewAgent: function (name) { toast('Agent detail for ' + name + ' ` + laterPhaseLiteral + `'); }`
	if strings.Count(oldSite, laterPhaseLiteral) == 0 {
		t.Error("fixture invalid: the OLD viewAgent site should contain the placeholder literal")
	}
	fixedSite := `viewAgent: function (name) { AppViewModel.navigate('agent/' + name); }`
	if strings.Count(fixedSite, laterPhaseLiteral) != 0 {
		t.Error("fixture invalid: the FIXED viewAgent site must not contain the placeholder literal")
	}
}

// The sixth view exists in index.html and is registered in VIEW_IDS (else showView blanks the page).
func TestIndexHTML_ViewAgentSectionPresent(t *testing.T) {
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	if c := strings.Count(indexHTML, `id="view-agent"`); c != 1 {
		t.Errorf(`index.html: exactly one id="view-agent" section expected, got %d`, c)
	}
	if !strings.Contains(appJS, `'view-agent'`) {
		t.Error("app.js: VIEW_IDS must contain 'view-agent' (Gotcha 2 — else showView('view-agent') hides every section)")
	}
}

// AC-2/AC-3/scale.md — boot() polls only while the agent view is open, and the snapshot pane is
// filled via textContent.
func TestAgentDetail_StructuralInvariants(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	if !reBootPollsAgentRoute.MatchString(appJS) {
		t.Error("app.js: boot() must poll AgentDetailViewModel.refresh() keyed on the 'agent/' route (poll-only-while-open)")
	}
	if !strings.Contains(appJS, `byId('agent-snapshot')`) {
		t.Error("app.js: the read-only snapshot pane #agent-snapshot must be present")
	}
	if !rePaneTextContent.MatchString(appJS) {
		t.Error("app.js: the snapshot pane must be filled via textContent (AC-3), never innerHTML")
	}
}

// AC-3 (D5) — no non-empty innerHTML assignment anywhere in the module assets.
func TestAppJS_NoNonEmptyInnerHTML(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	all := reAnyInnerHTMLAssign.FindAllString(appJS, -1)
	empt := reEmptyInnerHTMLAssign.FindAllString(appJS, -1)
	if len(all) != len(empt) {
		t.Errorf("app.js: every innerHTML assignment must be an empty-string clear (found %d assignments, %d empty clears) — render via textContent / DOM nodes, never innerHTML = markup", len(all), len(empt))
	}
}

// Self-negatives for the structural regexes — each matches the FIXED form and rejects the OLD form.
func TestAgentDetail_Structural_SelfNegative(t *testing.T) {
	oldBoot := `function boot() { wire(); FloorViewModel.refresh(); setInterval(function(){ FloorViewModel.refresh(); if (AppViewModel.currentRoute === 'dispatch') { DispatchViewModel.refresh(); } }, 5000); }`
	newBoot := `function boot() { setInterval(function(){ if (AppViewModel.currentRoute.indexOf('agent/') === 0) { AgentDetailViewModel.refresh(); } }, 5000); }`
	if reBootPollsAgentRoute.MatchString(oldBoot) {
		t.Error("reBootPollsAgentRoute false-positive on the OLD boot() (no agent poll)")
	}
	if !reBootPollsAgentRoute.MatchString(newBoot) {
		t.Error("reBootPollsAgentRoute failed to match the FIXED boot()")
	}

	oldSnap := `var pre = byId('agent-snapshot'); pre.innerHTML = tail.output;`
	newSnap := `var pre = byId('agent-snapshot'); pre.textContent = tail.output;`
	if rePaneTextContent.MatchString(oldSnap) {
		t.Error("rePaneTextContent false-positive on an innerHTML-filled snapshot")
	}
	if !rePaneTextContent.MatchString(newSnap) {
		t.Error("rePaneTextContent failed to match the textContent-filled snapshot")
	}
}

// AC-2b — the stopped-agents dark group has its host markup, pinned from BOTH ends of the JS↔HTML
// contract: renderDarkGroup's first line silently no-ops when #dark-grid is absent (Gotcha 11), so
// the ids must exist in index.html exactly once AND app.js must still bind them by the same names.
func TestIndexHTML_DarkGroupHostPresent(t *testing.T) {
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	for _, id := range []string{"dark-label", "dark-count", "dark-grid"} {
		if c := strings.Count(indexHTML, `id="`+id+`"`); c != 1 {
			t.Errorf(`index.html: exactly one id=%q expected, got %d (renderDarkGroup no-ops without its host)`, id, c)
		}
		if !strings.Contains(appJS, `byId('`+id+`')`) {
			t.Errorf("app.js: renderDarkGroup must bind byId('%s') — the JS side of the dark-group contract", id)
		}
	}
}

// AC-2b / ux.md U3-1 — dark cards are visually dimmed: darkCard emits class 'sign s-idle dark'
// and main.css styles it.
func TestMainCSS_DarkCardsDimmed(t *testing.T) {
	mainCSS := readAsset(t, filepath.Join(staticDir, "styles", "main.css"))
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	if !strings.Contains(appJS, `'sign s-idle dark'`) {
		t.Error("app.js: darkCard must emit class 'sign s-idle dark' (ux.md U3-1)")
	}
	if !reDarkDimRule.MatchString(mainCSS) {
		t.Error("main.css: a .sign.dark rule is required so dark cards render dimmed relative to lit cards (ux.md U3-1)")
	}
}

func TestMainCSS_DarkCardsDimmed_SelfNegative(t *testing.T) {
	with := `.app .sign.dark{ opacity:.55; }`
	without := `.app .sign.s-idle{    --lit:var(--line); }` + "\n" + `.app .sign .darken{ color:red; }`
	if !reDarkDimRule.MatchString(with) {
		t.Error("reDarkDimRule failed to match a valid dim rule")
	}
	if reDarkDimRule.MatchString(without) {
		t.Error("reDarkDimRule false-positive on CSS without a .sign.dark rule")
	}
}

func TestAppJS_NoNonEmptyInnerHTML_SelfNegative(t *testing.T) {
	good := `host.innerHTML = '';`
	bad := `host.innerHTML = card(a);`
	if len(reAnyInnerHTMLAssign.FindAllString(good, -1)) != 1 {
		t.Error("reAnyInnerHTMLAssign should match an innerHTML clear exactly once")
	}
	if len(reAnyInnerHTMLAssign.FindAllString(good, -1)) != len(reEmptyInnerHTMLAssign.FindAllString(good, -1)) {
		t.Error("an empty clear must be counted as an allowed (empty) assignment")
	}
	if len(reAnyInnerHTMLAssign.FindAllString(bad, -1)) == len(reEmptyInnerHTMLAssign.FindAllString(bad, -1)) {
		t.Error("a non-empty innerHTML assignment must NOT be counted as an empty clear")
	}
}
