package statusline

import (
	"fmt"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
)

const (
	barCells = 10
	barFull  = "█"
	barEmpty = "░"
	sep      = " | "
	// halfSep joins the two halves of the session/daily elements — `$ 21.95 · 352k tok` (ux.md C2.1).
	halfSep = " · "
)

// The fixed SGR palette, transcribed from the #591 screenshot (source.md:196-204) and adopted by
// ux.md C3.1: basic 8/16-colour, hand-rolled (ADR-013 — no colour library), applied only when the
// cmd layer's effective-colour decision (config `color` AND no NO_COLOR) is passed as RenderOpts.Color.
// It matches the screenshot's COLOURS, never its numeric values — the figures are the truthful counter
// (AC-2, ux.md:79-82). Every sequence the renderer emits is one of these constants (plus sgrReset), so
// stripSGR mechanically reduces a coloured render to its plain form (K9 grammar pins, security.md E1.1).
const (
	sgrReset    = "\x1b[0m"
	sgrModel    = "\x1b[36m" // cyan
	sgrDir      = "\x1b[34m" // blue
	sgrBranch   = "\x1b[35m" // magenta
	sgrAdd      = "\x1b[32m" // green
	sgrRemove   = "\x1b[31m" // red
	sgrElapsed  = "\x1b[90m" // gray
	sgrBarFull  = "\x1b[32m" // green
	sgrBarEmpty = "\x1b[90m" // gray
	sgrPercent  = "\x1b[32m" // green
	sgrCost     = "\x1b[37m" // white
	sgrTok      = "\x1b[90m" // gray
	sgrDaily    = "\x1b[34m" // blue (the "D")
)

// paletteSGR is every non-reset SGR the renderer may emit. stripSGR and the grammar tests derive from
// this single list so a palette change cannot silently escape the "only our palette" guarantee.
var paletteSGR = []string{
	sgrModel, sgrDir, sgrBranch, sgrAdd, sgrRemove, sgrElapsed,
	sgrBarFull, sgrBarEmpty, sgrPercent, sgrCost, sgrTok, sgrDaily,
}

// paint wraps s in an SGR colour + reset when colour is on and s is non-empty. An empty string is
// returned untouched so a dropped half never emits a bare colour+reset with nothing between.
func paint(on bool, code, s string) string {
	if !on || s == "" {
		return s
	}
	return code + s + sgrReset
}

// stripSGR removes every SGR escape (our palette + the reset) from s, reducing a coloured render to
// the byte-identical plain render. It underpins TestStripSGR_EqualsScrubbedPlain and lets the plain
// golden pin the coloured bytes without hard-coding brittle escape sequences.
func stripSGR(s string) string {
	for _, code := range paletteSGR {
		s = strings.ReplaceAll(s, code, "")
	}
	return strings.ReplaceAll(s, sgrReset, "")
}

// lineOf maps each canonical element to the layout line it belongs on: line 1 is the
// identity/activity row, line 2 is the context/cost row (ux.md:28-29). An element not in this
// map is unknown and is silently skipped (fail-open at config-drift time; the loud rejection
// lives in config.validateStatuslineConfig).
var lineOf = map[string]int{
	"model": 1, "dir": 1, "branch": 1, "diff": 1, "elapsed": 1,
	"context": 2, "session": 2, "daily": 2,
}

// RenderOpts carries the cmd-computed inputs the env-free library cannot derive itself (ADR-004/
// SEC-5): the session's cumulative token figure (K2 accumulator), whether ANSI color is effective
// (config color AND no NO_COLOR — computed at the cmd layer, passed here), and the redirect decision
// (a proxied ANTHROPIC_BASE_URL ⇒ a "~" cost-estimate prefix). Redirect is folded in here; the legacy
// Render wrapper builds a zero-value-plus-Redirect RenderOpts for callers that do not supply
// token/color state (the status self-test and the pre-existing test corpus).
type RenderOpts struct {
	SessionTokens int64
	Color         bool
	Redirect      bool
}

// Render is the backward-compatible entry: the cost-only, no-color render with just the redirect
// decision. It delegates to RenderWith so the single render engine has one home. The live pane path
// (cmd/statusline.go) calls RenderWith directly with the session token figure and the effective color.
func Render(cfg *config.StatuslineConfig, p Payload, branch string, daily DailyTotals, redirect bool) string {
	return RenderWith(cfg, p, branch, daily, RenderOpts{Redirect: redirect})
}

// RenderWith returns the two-line statusline for one payload. Layout is ux.md:28-29; the element
// set and order are the operator's config. It is always safe: a nil config renders nothing,
// every element fails open (a missing/nulled source field drops that element with no orphan
// separator), the zero/absent cost family drops the $/diff/elapsed elements (never "$0.00" as
// fact — Gap 12). Per-half drop (H-R2): session/daily render any truthful half — the cost half is
// governed by the never-$0.00 rule, the token half by the empty-counter rule; the element vanishes
// only when both are absent. ANSI color is applied last, gated on opts.Color.
func RenderWith(cfg *config.StatuslineConfig, p Payload, branch string, daily DailyTotals, opts RenderOpts) string {
	line1, line2 := collectTokens(cfg, p, branch, daily, opts)
	return joinNonEmpty([]string{strings.Join(line1, sep), strings.Join(line2, sep)}, "\n")
}

// collectTokens renders each configured element to its display token (dropping the ones whose
// source is absent) and buckets them onto their layout line, preserving config order.
func collectTokens(cfg *config.StatuslineConfig, p Payload, branch string, daily DailyTotals, opts RenderOpts) (line1, line2 []string) {
	if cfg == nil {
		return nil, nil
	}
	for _, el := range cfg.Elements {
		tok, ok := renderElement(el, p, branch, daily, opts)
		if !ok {
			continue
		}
		switch lineOf[el] {
		case 1:
			line1 = append(line1, tok)
		case 2:
			line2 = append(line2, tok)
		}
	}
	return line1, line2
}

// renderElement returns one element's token and whether it should appear. ok=false means the
// element's source is absent/zero and it is silently omitted (per-element fail-open). Untrusted
// strings are sanitized BEFORE any colour is applied, so the only escape bytes in a coloured token
// are the palette's own.
func renderElement(el string, p Payload, branch string, daily DailyTotals, opts RenderOpts) (string, bool) {
	switch el {
	case "model":
		s := sanitize(p.Model.DisplayName)
		return paint(opts.Color, sgrModel, s), s != ""
	case "dir":
		s := sanitize(resolveDir(p))
		return paint(opts.Color, sgrDir, s), s != ""
	case "branch":
		s := sanitize(branch)
		return paint(opts.Color, sgrBranch, s), s != ""
	case "diff":
		if p.Cost.TotalLinesAdded == 0 && p.Cost.TotalLinesRemoved == 0 {
			return "", false
		}
		plus := paint(opts.Color, sgrAdd, fmt.Sprintf("+%d", p.Cost.TotalLinesAdded))
		minus := paint(opts.Color, sgrRemove, fmt.Sprintf("-%d", p.Cost.TotalLinesRemoved))
		return plus + " " + minus, true
	case "elapsed":
		if p.Cost.TotalDurationMS == 0 {
			return "", false
		}
		return paint(opts.Color, sgrElapsed, "T "+formatDuration(p.Cost.TotalDurationMS)), true
	case "context":
		if p.ContextWindow.UsedPercentage == nil {
			return "", false
		}
		return renderContext(p, opts.Color), true
	case "session":
		// Per-half (H-R2): the cost half is governed by the never-$0.00 rule; the token half — the
		// session's LIFETIME cumulative tokens from the K2 counter (RenderOpts.SessionTokens), the
		// same span as the lifetime cost half — is governed by the empty-counter rule. The element
		// disappears only when BOTH halves are absent. The counter is truthful cumulative spend, NOT
		// the payload's context-window occupancy (which the context bar shows honestly; PR #595 T1/F1).
		var half []string
		if p.Cost.TotalCostUSD != 0 { // never render "$0.00" as fact
			half = append(half, paint(opts.Color, sgrCost, fmt.Sprintf("%s$ %.2f", costPrefix(opts.Redirect), p.Cost.TotalCostUSD)))
		}
		if opts.SessionTokens > 0 {
			half = append(half, paint(opts.Color, sgrTok, formatTokens(opts.SessionTokens)+" tok"))
		}
		if len(half) == 0 {
			return "", false
		}
		return strings.Join(half, halfSep), true
	case "daily":
		// Same per-half rule as session; DailyTotals.Tokens is SumDaily's Σ max(0, cum−baseline)
		// (clamped ≥ 0), so it can never render the negative occupancy figure PR #595 dropped.
		var half []string
		if daily.CostUSD != 0 {
			half = append(half, paint(opts.Color, sgrCost, fmt.Sprintf("%s$ %.2f", costPrefix(opts.Redirect), daily.CostUSD)))
		}
		if daily.Tokens > 0 {
			half = append(half, paint(opts.Color, sgrTok, formatTokens(daily.Tokens)+" tok"))
		}
		if len(half) == 0 {
			return "", false
		}
		return paint(opts.Color, sgrDaily, "D") + " " + strings.Join(half, halfSep), true
	default:
		return "", false
	}
}

// resolveDir picks the directory element's source: project_dir, then current_dir, then cwd
// (design-doc.md:158-161).
func resolveDir(p Payload) string {
	if p.Workspace.ProjectDir != "" {
		return p.Workspace.ProjectDir
	}
	if p.Workspace.CurrentDir != "" {
		return p.Workspace.CurrentDir
	}
	return p.Cwd
}

func renderContext(p Payload, color bool) string {
	pct := *p.ContextWindow.UsedPercentage
	used := p.ContextWindow.TotalInputTokens + p.ContextWindow.TotalOutputTokens
	pctStr := paint(color, sgrPercent, fmt.Sprintf("%.0f%%", pct))
	occStr := paint(color, sgrTok, fmt.Sprintf("(%s/%s)", formatTokens(used), formatTokens(p.ContextWindow.ContextWindowSize)))
	return bar(pct, color) + " " + pctStr + " " + occStr
}

// bar renders a fixed-width block-char context bar: filled cells green, empty cells gray when colour
// is on (the screenshot's bar — the colour is the bar's identity, not a health signal). filled =
// round(pct/10), clamped.
func bar(pct float64, color bool) string {
	filled := int(pct/10 + 0.5)
	if filled < 0 {
		filled = 0
	}
	if filled > barCells {
		filled = barCells
	}
	return paint(color, sgrBarFull, strings.Repeat(barFull, filled)) + paint(color, sgrBarEmpty, strings.Repeat(barEmpty, barCells-filled))
}

// formatTokens renders a token count compactly: N, Nk, or N.NM.
func formatTokens(n int64) string {
	switch {
	case n < 1000:
		return fmt.Sprintf("%d", n)
	case n < 1_000_000:
		return fmt.Sprintf("%dk", n/1000)
	default:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	}
}

// formatDuration renders a session's elapsed time in the #591 screenshot format (ux.md C2.1): hours
// and zero-padded minutes with seconds dropped — "1h 06m"; under an hour, minutes only — "2m"; under
// a minute, "0m". The render caller prepends "T " (the elapsed case). Screenshot parity is binding
// (PR #601 T6, requirement holder); minute granularity also steadies the idle pane for the watchdog.
func formatDuration(ms int64) string {
	totalMin := ms / 60000
	h := totalMin / 60
	m := totalMin % 60
	if h > 0 {
		return fmt.Sprintf("%dh %02dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func costPrefix(redirect bool) string {
	if redirect {
		return "~"
	}
	return ""
}

func joinNonEmpty(parts []string, glue string) string {
	kept := make([]string, 0, len(parts))
	for _, p := range parts {
		if p != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, glue)
}
