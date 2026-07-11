package web

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// #502 Phase 4 — palette / rarity / embed audits over the shipped formula-editor assets. Source-level
// structural lints in package web (the executable DOM regression is Phase 5b). Each carries a
// self-negative proving the check is not vacuous, mirroring nav_test.go / agentdetail_test.go.

// A raw hex color literal: # followed by 3–6 hex digits. `#502`/`#fff`/`#575072` all match.
var reHexColor = regexp.MustCompile(`#[0-9a-fA-F]{3,6}\b`)

// A literal rarity CLASS token (the prototype roster.html:216 locked-card `r-common` shape). The word
// boundary before `r-` makes `--rarity-common` NOT match (the char before `r` is `-`, still a \w… so use
// a guard: require the token is not immediately preceded by a letter/dash of `rarity`). We assert on the
// class-attribute form and the computed-className exception is handled by matching only quoted tokens.
var reRarityClass = regexp.MustCompile(`\br-(legendary|epic|rare|common)\b`)

// #534: the regeneration-era formula-editor.css (and its no-raw-hex audit) is gone. The formula
// editor's styles are the APPROVED prototype's own (static/formula-editor/styles/*), whose bytes are
// pinned by reconstruct_test.go — a style audit must never demand edits to signed bytes. The audit
// below keeps only its self-negative to pin reHexColor for the shell checks that remain.
func TestFormulaEditorCSS_NoRawHexOutsideVariables_SelfNegative(t *testing.T) {
	if !reHexColor.MatchString("color:#575072;") {
		t.Error("reHexColor should match a raw hex literal")
	}
	if reHexColor.MatchString("color:var(--rarity-common);") {
		t.Error("reHexColor false-positive on a var() token")
	}
}

// IMPLREADME File-4 — the raw #fff at main.css:538 is tokenized as --viewer-bg, and the token is
// declared in variables.css. The viewer iframe rule must reference the token, not a raw hex.
func TestMainCSS_ViewerBgTokenized(t *testing.T) {
	mainCSS := readAsset(t, filepath.Join(staticDir, "styles", "main.css"))
	varsCSS := readAsset(t, filepath.Join(staticDir, "styles", "variables.css"))
	if strings.Contains(mainCSS, "background:#fff") {
		t.Error("main.css: the .viewer iframe rule must use var(--viewer-bg), not a raw background:#fff")
	}
	if !strings.Contains(mainCSS, "background:var(--viewer-bg)") {
		t.Error("main.css: the .viewer iframe rule must reference var(--viewer-bg)")
	}
	if !strings.Contains(varsCSS, "--viewer-bg:") {
		t.Error("variables.css: --viewer-bg token must be declared")
	}
}

// Deps — the four rarity alias tokens are declared in variables.css (they back the computed rarity frames).
func TestVariablesCSS_RarityTokens(t *testing.T) {
	varsCSS := readAsset(t, filepath.Join(staticDir, "styles", "variables.css"))
	for _, tok := range []string{"--rarity-legendary:", "--rarity-epic:", "--rarity-rare:", "--rarity-common:"} {
		if !strings.Contains(varsCSS, tok) {
			t.Errorf("variables.css: rarity alias token %q must be declared", tok)
		}
	}
}

// AC-6, #534 re-scope — no literal r-(legendary|epic|rare|common) CLASS token in the SHELL assets
// (index.html, app.js). The transplanted tree is exempt by design: the APPROVED roster.html carries
// a literal r-common on its locked-card branch, and those bytes are signed (reconstruct_test.go);
// a shell-authoring rule must never demand edits to the approved artifact.
func TestShippedStatic_NoRarityClassTokens(t *testing.T) {
	for _, rel := range []string{
		"index.html",
		"app.js",
	} {
		src := readAsset(t, filepath.Join(staticDir, rel))
		if hits := reRarityClass.FindAllString(src, -1); len(hits) != 0 {
			t.Errorf("%s must not carry a literal r-(legendary|epic|rare|common) class token (found %v) — shell rarity is computed, never hand-assigned", rel, hits)
		}
	}
}

func TestShippedStatic_NoRarityClassTokens_SelfNegative(t *testing.T) {
	if !reRarityClass.MatchString(`class="line-card framed r-common"`) {
		t.Error("reRarityClass should match a literal r-common class token")
	}
	// The computed-className form `'r-' + rarity` builds the token at runtime — no literal token present.
	if reRarityClass.MatchString("li.className = 'framed r-' + s.rarity;") {
		t.Error("reRarityClass false-positive on a computed 'r-' + rarity className")
	}
}

// AC-6 / AC #5 / production note 4 — demo data must NOT ship. The regeneration-era path stays absent,
// and the transplanted tree's scripts/demo-formulas.js must be the declared replaced-file seam (the
// live-store module), never the prototype's GENERATED demo bundle. Asserted via the embed FS so it
// holds for the shipped binary, not just the working tree.
func TestStatic_DemoFormulasNotShipped(t *testing.T) {
	if _, err := fs.Stat(Static(), "scripts/demo-formulas.js"); err == nil {
		t.Error("scripts/demo-formulas.js must NOT ship (AC-6): the demo bundle is re-based onto the live store")
	}
	raw, err := fs.ReadFile(Static(), "formula-editor/scripts/demo-formulas.js")
	if err != nil {
		t.Fatalf("formula-editor/scripts/demo-formulas.js (the replaced-file data seam) must be embedded: %v", err)
	}
	src := string(raw)
	if strings.Contains(src, "GENERATED byte-exact copies") {
		t.Error("formula-editor/scripts/demo-formulas.js is the prototype demo bundle — the replaced-file seam did not ship (production note 4)")
	}
	if !strings.Contains(src, "/api/formulas") {
		t.Error("formula-editor/scripts/demo-formulas.js must read the live store via /api/formulas (production note 4)")
	}
}

// AC-1 / P1a / AC #5 — the shipped span-tracking engine is embedded (#534: at its transplanted home).
func TestStatic_TomlEnginePresent(t *testing.T) {
	if _, err := fs.Stat(Static(), "formula-editor/scripts/toml-engine.js"); err != nil {
		t.Errorf("formula-editor/scripts/toml-engine.js (the approved span-tracking engine) must be embedded: %v", err)
	}
}

// AC #4 (font posture) — Decision 7 (self-host WOFF2 + remove @import) is CONTINGENT on the Phase-5a
// operator sign-off, which does NOT exist at Phase-4 time. Phase 4 takes the documented IDENTICAL-STACKS
// FALLBACK: the CSP-blocked @import stays (harmless — the full offline fallback stacks below carry the
// type), and no WOFF2 ships. This test PINS that documented decision so it is deliberate, not accidental:
// the @import is present AND the fallback font stacks are declared. When Phase 5a lands the sign-off, this
// test is replaced by the @import-gone / @font-face-present assertion.
func TestVariablesCSS_FontFallbackPosture(t *testing.T) {
	varsCSS := readAsset(t, filepath.Join(staticDir, "styles", "variables.css"))
	if !strings.Contains(varsCSS, "@import") {
		t.Error("variables.css: Phase 4 keeps the @import (identical-stacks fallback, Decision 7 gated on the Phase-5a sign-off) — its removal must land with the self-host swap, not before")
	}
	// The families must still carry full offline fallback stacks so the type reads right with the CDN blocked.
	for _, stack := range []string{"--font-display:", "--font-body:", "--font-mono:"} {
		if !strings.Contains(varsCSS, stack) {
			t.Errorf("variables.css: font stack %q must be declared (the fallback that carries the type under CSP)", stack)
		}
	}
	// No self-hosted WOFF2 @font-face this phase (would need the sign-off + shipped fonts).
	if strings.Contains(varsCSS, "@font-face") {
		t.Error("variables.css: no @font-face self-host this phase — WOFF2 self-hosting is gated on the Phase-5a operator sign-off (Decision 7)")
	}
}
