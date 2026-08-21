package statusline

import (
	"strings"
	"testing"
)

// Pinning tests for the UNRESOLVED review comments of PR #601 (fable-increment, issue #600 Phase 3).
// Each test names the thread it pins. RED tests fail against the PR-head code (Phase 3 render not
// landed) and must pass after the fix; protective tests pass now and must keep passing.

// T6 / B1 (Blocker) — the session token half. AC-1 requires "session cost AND tokens". The counter
// figure arrives via RenderOpts.SessionTokens; the render must show it beside the cost as `· Nk tok`.
// RED today: session is cost-only, the token figure never appears.
func TestFableIncr601_T6_SessionTokenHalfRenders(t *testing.T) {
	p := goldenStruct() // cost 12.34
	daily := DailyTotals{CostUSD: 80.64, Sessions: 1}

	got := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000})

	if !strings.Contains(got, "$ 12.34 · 487k tok") {
		t.Errorf("T6: session must render cost AND the truthful token figure `$ 12.34 · 487k tok`; got %q", got)
	}
}

// T6 / B1 — the daily token half. AC-1 requires the daily "D" summary to carry cost AND tokens.
// The figure is DailyTotals.Tokens (already summed by SumDaily). RED today: daily is cost-only.
func TestFableIncr601_T6_DailyTokenHalfRenders(t *testing.T) {
	p := goldenStruct()
	daily := DailyTotals{CostUSD: 80.64, Tokens: 1400000, Sessions: 1}

	got := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 1})

	if !strings.Contains(got, "D $ 80.64 · 1.4M tok") {
		t.Errorf("T6: daily must render cost AND the summed token figure `D $ 80.64 · 1.4M tok`; got %q", got)
	}
}

// T6 / B1 (H-R2 per-half drop) — a truthful token half must render even when the cost half is
// absent (the redirected-ANTHROPIC_BASE_URL case: cost 0, tokens > 0). RED today: cost==0 drops the
// WHOLE session element, discarding the truthful token figure — the exact silent omission AC-1 forbids.
func TestFableIncr601_T6_SessionTokensRenderWhenCostAbsent(t *testing.T) {
	p := goldenStruct()
	p.Cost.TotalCostUSD = 0 // no cost figure available
	daily := DailyTotals{CostUSD: 80.64, Sessions: 1}

	got := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000})

	if !strings.Contains(got, "487k tok") {
		t.Errorf("H-R2: a truthful session token half must render even when the cost half is absent; got %q", got)
	}
	if strings.Contains(got, "$ 0.00") || strings.Contains(got, "$0.00") {
		t.Errorf("H-R2: an absent cost half must never render as $0.00; got %q", got)
	}
}

// T6 / B1 (H-R2 protective) — the token half drops when the counter is 0/absent; the cost half
// renders alone, no fabricated "0 tok". Passes today (nothing renders tok) and MUST keep passing.
func TestFableIncr601_T6_TokenHalfDropsWhenCounterZero(t *testing.T) {
	p := goldenStruct() // cost 12.34
	daily := DailyTotals{CostUSD: 80.64, Sessions: 1}

	got := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 0})

	if !strings.Contains(got, "$ 12.34") {
		t.Errorf("cost half must still render when the counter is zero; got %q", got)
	}
	if strings.Contains(got, "$ 12.34 · ") || strings.Contains(got, "0 tok") {
		t.Errorf("a zero counter must DROP the token half, never render `0 tok`; got %q", got)
	}
}

// T6 / B1 / AC-4 (Blocker) — colour. ColorEnabled defaults ON; the render must colorize via the
// fixed SGR palette when RenderOpts.Color is true, so an attached session shows the screenshot's
// coloured bar. RED today: bar is plain block chars, ColorEnabled has zero render callers, no ESC.
func TestFableIncr601_T6_ColorEmitsSGR(t *testing.T) {
	p := goldenStruct()
	daily := DailyTotals{CostUSD: 80.64, Tokens: 1400000, Sessions: 1}

	colored := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000, Color: true})
	if !strings.ContainsRune(colored, 0x1b) {
		t.Errorf("AC-4: Color:true must emit ANSI SGR (an ESC byte); got %q", colored)
	}

	// Protective: Color:false stays byte-plain (no ESC) — the env-free plain path is preserved.
	plain := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000, Color: false})
	if strings.ContainsRune(plain, 0x1b) {
		t.Errorf("Color:false must never emit an ESC byte; got %q", plain)
	}
}

// T9 / AC-4 grammar pin (security.md E1.1 / K9 item 5): every escape a coloured render emits is one of
// OUR palette constants — stripping them leaves NO residual ESC. This replaces the #595 "no-ESC" pin at
// equal rigour: colour is delivered, and it is ONLY our colour.
func TestRenderColor_OnlyOwnPaletteSGR(t *testing.T) {
	p := goldenStruct()
	daily := DailyTotals{CostUSD: 80.64, Tokens: 1400000, Sessions: 1}
	colored := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000, Color: true})
	if !strings.ContainsRune(colored, 0x1b) {
		t.Fatalf("expected a coloured render to contain SGR; got %q", colored)
	}
	if residual := stripSGR(colored); strings.ContainsRune(residual, 0x1b) {
		t.Errorf("an escape outside the fixed palette survived: %q", residual)
	}
}

// T9 grammar pin: stripping the palette SGR from the coloured render yields the byte-identical PLAIN
// render. This ties the coloured bytes to the plain golden (TestRender_GoldenFixture2_1_212) exactly,
// so a palette tweak cannot silently change layout/figures, and no brittle ESC sequence is hard-coded.
func TestRenderColor_StripSGREqualsPlain(t *testing.T) {
	p := goldenStruct()
	daily := DailyTotals{CostUSD: 80.64, Tokens: 1400000, Sessions: 1}
	colored := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000, Color: true})
	plain := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000, Color: false})
	if got := stripSGR(colored); got != plain {
		t.Errorf("stripSGR(coloured) != plain:\n strip: %q\n plain: %q", got, plain)
	}
}

// T9 / AC-4 escape hygiene: hostile ESC/OSC/Cf in an untrusted model/dir/branch name is stripped by
// sanitize BEFORE colour is applied, so even on hostile input the ONLY escapes in the output are the
// palette's own. This is the coloured-path successor to TestRender_HostileStringsSanitized.
func TestRenderColor_HostileInputOnlyPaletteEscapes(t *testing.T) {
	p := goldenStruct()
	p.Model.DisplayName = "Op\x1b[31mus\x07 4.8"                  // CSI colour + BEL
	p.Workspace.ProjectDir = "/home/\x1b]0;pwn\x07dev​⁠/p\x7froj" // OSC + a forged sentinel + DEL
	branch := "ma\x1b[2Jin\x00⁠"                                  // CSI clear + NUL + a stray word-joiner
	daily := DailyTotals{CostUSD: 80.64, Tokens: 1400000, Sessions: 1}

	colored := RenderWith(defaultCfg(), p, branch, daily, RenderOpts{SessionTokens: 487000, Color: true})
	if residual := stripSGR(colored); strings.ContainsRune(residual, 0x1b) {
		t.Errorf("a hostile escape survived alongside the palette: %q", residual)
	}
	// The forged two-Cf-rune sentinel (​⁠) in the dir name must NOT survive sanitize (watchdog
	// forgery guard, T7/D10).
	if strings.Contains(colored, "​⁠") {
		t.Errorf("a forged watchdog sentinel survived in untrusted content: %q", colored)
	}
	// The visible letters survive.
	if !strings.Contains(stripSGR(colored), "main") {
		t.Errorf("expected sanitized branch to read 'main'; got %q", stripSGR(colored))
	}
}
