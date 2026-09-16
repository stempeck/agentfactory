package statusline

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stempeck/agentfactory/internal/config"
)

// #673 item 2 / AC-2 clause (iii)-(iv): the alert is the one token on the pane that is NOT an
// operator-electable element. A safety alarm is not a layout preference, so it must appear whatever
// the operator's statusline.json says — including the two configurations that render nothing at all
// today. These tests pin that, and pin the price of it: an ABSENT alarm must cost zero bytes, or
// every pre-existing render golden becomes a lie.

const alertToken = "⚠ HALT worker"

func TestRender_AlertLeadsLine1WhateverTheConfigSays(t *testing.T) {
	p := loadGoldenPayload(t)
	daily := DailyTotals{CostUSD: 80.64, Tokens: 1400000, Sessions: 1}

	t.Run("a pre-existing eight-element config is untouched beneath it", func(t *testing.T) {
		golden := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000})
		got := RenderWith(defaultCfg(), p, "af/soldesign", daily,
			RenderOpts{SessionTokens: 487000, Alert: alertToken})

		want := alertToken + " | " + golden
		if got != want {
			t.Errorf("the alert did not lead line 1 with the render otherwise byte-identical:\n got: %q\nwant: %q", got, want)
		}
		assertNoOrphanSeparators(t, got)
	})

	t.Run("a config electing nothing on line 1", func(t *testing.T) {
		cfg := &config.StatuslineConfig{Elements: []string{"context"}}
		got := RenderWith(cfg, p, "af/soldesign", daily, RenderOpts{Alert: alertToken})

		line1 := strings.SplitN(got, "\n", 2)[0]
		if line1 != alertToken {
			t.Errorf("line 1 must be exactly the alert, got %q", line1)
		}
		assertNoOrphanSeparators(t, got)
	})

	t.Run("a nil config", func(t *testing.T) {
		// collectTokens returns nothing for a nil config, so this is the case that forces the
		// prepend to live in RenderWith rather than inside the elements loop.
		if got := RenderWith(nil, p, "af/soldesign", daily, RenderOpts{Alert: alertToken}); got != alertToken {
			t.Errorf("a nil config must still render the alarm, got %q", got)
		}
	})

	t.Run("an absent alarm costs zero bytes", func(t *testing.T) {
		// The explicit no-regression twin of TestRender_GoldenFixture2_1_212: whatever shape the
		// prepend takes, an empty Alert may not add a separator, a space or a painted empty token.
		withField := RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000, Alert: ""})
		legacy := Render(defaultCfg(), p, "af/soldesign", daily, false)
		if withField != RenderWith(defaultCfg(), p, "af/soldesign", daily, RenderOpts{SessionTokens: 487000}) {
			t.Error("an explicitly empty Alert differs from an unset one")
		}
		if strings.HasPrefix(withField, " ") || strings.HasPrefix(legacy, " ") || strings.HasPrefix(legacy, "|") {
			t.Errorf("an empty alarm left a leading artifact: %q / %q", withField, legacy)
		}
		assertNoOrphanSeparators(t, withField)
		assertNoOrphanSeparators(t, legacy)
	})
}

// TestRenderColor_AlertPaintsOnlyFromTheRegisteredPalette is the test the palette's own grammar
// tests cannot be: they never set an Alert, so they emit no sgrAlert byte and cannot notice an
// unregistered one. This one does, which is what makes "every sequence the renderer emits is in
// paletteSGR" self-enforcing rather than a claim maintained by hand.
func TestRenderColor_AlertPaintsOnlyFromTheRegisteredPalette(t *testing.T) {
	p := loadGoldenPayload(t)
	daily := DailyTotals{CostUSD: 80.64, Tokens: 1400000, Sessions: 1}

	colored := RenderWith(defaultCfg(), p, "af/soldesign", daily,
		RenderOpts{SessionTokens: 487000, Color: true, Alert: alertToken})
	if !strings.ContainsRune(colored, 0x1b) {
		t.Fatal("the coloured render carries no ESC byte at all; the assertions below would be vacuous")
	}
	if residual := stripSGR(colored); strings.ContainsRune(residual, 0x1b) {
		t.Errorf("an SGR the palette does not list survived stripSGR — register sgrAlert in paletteSGR: %q", residual)
	}

	plain := RenderWith(defaultCfg(), p, "af/soldesign", daily,
		RenderOpts{SessionTokens: 487000, Color: false, Alert: alertToken})
	if strings.ContainsRune(plain, 0x1b) {
		t.Errorf("colour is off yet the alert emitted an ESC byte: %q", plain)
	}
	if got := stripSGR(colored); got != plain {
		t.Errorf("the coloured alert does not reduce to the plain one:\n got: %q\nwant: %q", got, plain)
	}
}

// TestRender_AlertIsSanitizedLikeEveryOtherToken closes the gap the alert's own construction path
// opens: it is assembled in the cmd layer, outside renderElement, so nothing in the elements loop
// applies the SEC-2 strip or the 64-rune cap to it. The renderer applies both itself, so the pane's
// guarantee holds for any caller rather than for one careful one.
func TestRender_AlertIsSanitizedLikeEveryOtherToken(t *testing.T) {
	hostile := "⚠ HALT \x1b[31mPWNED\x07​ " + strings.Repeat("x", 200)
	got := RenderWith(nil, Payload{}, "", DailyTotals{}, RenderOpts{Alert: hostile})

	if strings.ContainsRune(got, 0x1b) || strings.Contains(got, "\x07") {
		t.Errorf("an escape or control byte reached the pane: %q", got)
	}
	if strings.Contains(got, "​") {
		t.Errorf("a format rune reached the pane and could forge the watchdog sentinel: %q", got)
	}
	if strings.Contains(got, strings.Repeat("x", 100)) {
		t.Errorf("the alert was not capped: %q", got)
	}
	if n := utf8.RuneCountInString(got); n > maxElementRunes {
		t.Errorf("the rendered alert is %d runes, above the %d-rune element cap: %q", n, maxElementRunes, got)
	}
}

// TestSanitizeToken_IsTheSameRoutineTheRendererUses pins the reason the cmd layer has an exported
// entry at all: so the alarm it assembles is capped and stripped by THIS implementation rather than
// by a second copy of the rules that can drift from it.
func TestSanitizeToken_IsTheSameRoutineTheRendererUses(t *testing.T) {
	for _, s := range []string{
		"",
		"⚠ HALT worker",
		"⚠ HALT \x1b[31mPWNED\x07",
		strings.Repeat("⚠", 300),
	} {
		if got, want := SanitizeToken(s), sanitize(s); got != want {
			t.Errorf("SanitizeToken(%q) = %q, want %q", s, got, want)
		}
		if n := utf8.RuneCountInString(SanitizeToken(s)); n > maxElementRunes {
			t.Errorf("SanitizeToken(%q) returned %d runes, above the %d-rune cap", s, n, maxElementRunes)
		}
	}
}
