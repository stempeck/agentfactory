package web

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Phase 4B (Issue #596, K10-web / review finding H-4): the Floor must stop asserting a bare
// "Working" badge for a context-exhausted or breaker-halted agent. Phase 4A made the truth
// available on `af agents list --json`; this phase renders it.
//
// The web module is pure-Go with no JS/DOM runtime (web/go.mod has no require block), so —
// following the source-scan precedent in web/internal/web/nav_test.go and sling_test.go — these
// are SOURCE-LEVEL structural assertions over the embedded static assets. `readAsset` and
// `staticDir` are shared with nav_test.go (same package).
//
// The assertions here are paired with TestRecoveryBadge_SelfNegative, which proves they cannot
// match trivially — a structural assertion that would pass against the pre-change file is worth
// nothing. Behaviour (what the badge actually EMITS for a given agent) is proven separately, and
// mechanically, by web/conformance/test-recovery-badge.js.

// These assertions are scoped with the package's existing brace-matching funcBody helper
// (agentdetail_layout_test.go) rather than with a regex.
//
// A regex of the form `function\s+foo\s*\([^)]*\)\s*\{[\s\S]*?TOKEN` cannot express "inside foo's
// body": `[\s\S]*?` happily runs past the closing brace, so such an assertion silently passes on a
// TOKEN belonging to some later function. That is not hypothetical — the first draft of this file
// "pinned" recoveryLook's 'none' gate with exactly that shape and was satisfied by contextLook's
// own `state === 'none'` thirty lines further down, so deleting the real gate kept the suite green.
//
// body wraps funcBody with the "it must actually be there" check the callers below all want.
func body(t *testing.T, src, name string) string {
	t.Helper()
	b := funcBody(src, name)
	if b == "" {
		t.Fatalf("app.js: no function %s", name)
	}
	return b
}

// TestRecoveryBadge_FloorRendersOccupancyAndRecovery pins the K10-web badge: an agent whose
// breaker has latched, or whose occupancy channel is not fresh, carries a badge beside the status
// badge on the Floor card.
func TestRecoveryBadge_FloorRendersOccupancyAndRecovery(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	// recoveryLook must exclude 'none' — the NORMAL healthy verdict every agent carries — and
	// must name 'halted', the state meaning the watchdog will not recycle this agent again
	// until `af recovery reset`.
	recovery := body(t, appJS, "recoveryLook")
	for _, want := range []string{"'none'", "'halted'", "'recovering'"} {
		if !strings.Contains(recovery, want) {
			t.Errorf("recoveryLook must handle %s; body:\n%s", want, recovery)
		}
	}

	// contextLook must read the five-literal ChannelState domain. 'malformed' is the load-bearing
	// one: af-core never collapses it into 'none', so a UI that ignores it renders a malformed
	// occupancy channel as healthy — the exact defect class this design closes.
	context := body(t, appJS, "contextLook")
	for _, want := range []string{"'fresh'", "'stale'", "'dark'", "'none'", "'malformed'"} {
		if !strings.Contains(context, want) {
			t.Errorf("contextLook must handle %s; body:\n%s", want, context)
		}
	}

	// The badges must be WIRED, not merely declared — at every surface that renders an agent.
	// A helper nothing calls is decoration; H-4 is about what the operator actually sees.
	for _, site := range []string{"card", "renderAgentDetail", "darkCard"} {
		if !strings.Contains(body(t, appJS, site), "appendHealthBadges(") {
			t.Errorf("%s() must call appendHealthBadges", site)
		}
	}

	// The badge must consume the af-core field names verbatim; the af↔web contract is a string
	// match, not a type match.
	appender := body(t, appJS, "appendHealthBadges")
	for _, key := range []string{"context_state", "context_pct", "recovery"} {
		if !strings.Contains(appender, key) {
			t.Errorf("appendHealthBadges must read the %q field from the read-model projection", key)
		}
	}
}

// TestRecoveryBadge_NeverReDerivesStatus is the scope guard. The design (Decision 13) chose
// passthrough + badge over re-deriving status in the web module, and the Floor's filter chips are
// driven by data-status and FILTERS. Folding a recovery value into either would re-derive the
// honesty enum by the back door and silently change filtering semantics.
func TestRecoveryBadge_NeverReDerivesStatus(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	// The STATUS map keeps exactly its six honest statuses — no recovery/context literal joins it.
	statusMap := regexp.MustCompile(`var\s+STATUS\s*=\s*\{[\s\S]*?\n\s*\};`).FindString(appJS)
	if statusMap == "" {
		t.Fatalf("app.js: could not locate the STATUS map")
	}
	for _, forbidden := range []string{"halted", "recovering", "malformed", "context_state"} {
		if strings.Contains(statusMap, forbidden) {
			t.Errorf("STATUS map must not carry %q: status stays INHERITED from Phase 0", forbidden)
		}
	}

	filtersMap := regexp.MustCompile(`var\s+FILTERS\s*=\s*\{[\s\S]*?\n\s*\};`).FindString(appJS)
	if filtersMap == "" {
		t.Fatalf("app.js: could not locate the FILTERS map")
	}
	for _, forbidden := range []string{"halted", "recovering", "context"} {
		if strings.Contains(filtersMap, forbidden) {
			t.Errorf("FILTERS map must not carry %q: filtering semantics are unchanged", forbidden)
		}
	}

	// data-status is written from the honest status alone.
	if regexp.MustCompile(`setAttribute\('data-status',\s*a\.recovery`).MatchString(appJS) {
		t.Errorf("data-status must carry only the honest status enum, never the recovery verdict")
	}
}

// TestRecoveryBadge_UsesDeclaredTokensNotLit pins the CSS half. `--lit` is set PER CARD by the
// .sign.s-* rules, so a badge inheriting it would glow magenta on a Working card — reinforcing the
// "everything's fine" reading this phase removes — and would render colorless in the agent-detail
// badge host, which sits outside any .sign.
func TestRecoveryBadge_UsesDeclaredTokensNotLit(t *testing.T) {
	css := readAsset(t, filepath.Join(staticDir, "styles", "main.css"))

	for _, cls := range []string{`.app .badge.halt{`, `.app .badge.warn{`} {
		if !strings.Contains(css, cls) {
			t.Fatalf("main.css must declare %s", cls)
		}
	}

	// Each new rule sets its own hue from a declared token rather than inheriting --lit.
	haltRule := regexp.MustCompile(`\.app \.badge\.halt\{[\s\S]*?\}`).FindString(css)
	if !strings.Contains(haltRule, "var(--danger)") {
		t.Errorf(".badge.halt must colour itself from --danger, got: %s", haltRule)
	}
	if strings.Contains(haltRule, "var(--lit)") {
		t.Errorf(".badge.halt must not inherit --lit (undefined outside .sign)")
	}
	warnRule := regexp.MustCompile(`\.app \.badge\.warn\{[\s\S]*?\}`).FindString(css)
	if strings.Contains(warnRule, "var(--lit)") {
		t.Errorf(".badge.warn must not inherit --lit (undefined outside .sign)")
	}
	if !regexp.MustCompile(`var\(--(neon-violet|signage-amber|danger|muted|line)\)`).MatchString(warnRule) {
		t.Errorf(".badge.warn must colour itself from a declared token, got: %s", warnRule)
	}

	// No raw hex literals — the shipped CSS reads its palette from styles/variables.css.
	for _, rule := range []string{haltRule, warnRule} {
		if regexp.MustCompile(`#[0-9A-Fa-f]{3,8}\b`).MatchString(rule) {
			t.Errorf("new badge rules must use declared tokens, not raw hex: %s", rule)
		}
	}
}

// TestRecoveryBadge_SelfNegative proves the assertions above are non-vacuous.
//
// It negates against the REAL app.js rather than a synthetic stub. That distinction is the whole
// lesson of this file's first draft: its stub contained no contextLook at all, so it could not
// reveal that the recoveryLook assertion was being satisfied by contextLook's body. Cutting the
// real source at each function's own boundary is what makes the negative meaningful.
func TestRecoveryBadge_SelfNegative(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))

	// (a) funcBody must actually BOUND each function. recoveryLook's body must not contain
	// contextLook's literals, and vice versa — if it does, funcBody is over-reaching and every
	// assertion built on it is worthless.
	recovery := body(t, appJS, "recoveryLook")
	context := body(t, appJS, "contextLook")
	if strings.Contains(recovery, "function contextLook") {
		t.Errorf("VACUOUS: recoveryLook's extracted body runs past its closing brace")
	}
	for _, foreign := range []string{"'malformed'", "'stale'", "'fresh'"} {
		if strings.Contains(recovery, foreign) {
			t.Errorf("VACUOUS: recoveryLook's body contains %s — extraction is not bounded", foreign)
		}
	}
	if strings.Contains(context, "'halted'") || strings.Contains(context, "'recovering'") {
		t.Errorf("VACUOUS: contextLook's body contains a recovery literal — extraction is not bounded")
	}

	// (b) The call-site assertion must be able to FAIL. Each render surface's body must be
	// bounded too, or "card() calls appendHealthBadges" would be satisfied by darkCard's call.
	for _, site := range []string{"card", "renderAgentDetail", "darkCard"} {
		b := body(t, appJS, site)
		for _, other := range []string{"card", "renderAgentDetail", "darkCard"} {
			if other != site && strings.Contains(b, "function "+other+"(") {
				t.Errorf("VACUOUS: %s's body swallowed %s", site, other)
			}
		}
	}

	// (c) A body that genuinely lacks the token must not match.
	if strings.Contains(body(t, appJS, "el"), "appendHealthBadges(") {
		t.Errorf("VACUOUS: the el() helper appears to call appendHealthBadges")
	}

	// (d) The CSS assertions must fail against the pre-change stylesheet.
	cssBefore := `.app .badge.gate{ border-color:var(--signage-amber); }`
	if strings.Contains(cssBefore, ".app .badge.halt{") || strings.Contains(cssBefore, ".app .badge.warn{") {
		t.Errorf("VACUOUS: the badge-class assertions match the pre-change stylesheet")
	}
}
