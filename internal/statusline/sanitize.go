package statusline

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// maxElementRunes caps every rendered element (SEC-2, security.md:52-56). A hostile repo can
// name a branch or path with a payload far wider than a pane; the cap keeps one element from
// crowding out the rest, and the middle-ellipsis keeps a path's head and tail legible.
const maxElementRunes = 64

// ellipsis is the single middle-ellipsis rune used when capping.
const ellipsis = "…"

// sanitize makes an untrusted, payload- or git-derived string safe to place in pane output:
// it strips every ESC (0x1b) escape sequence, every Unicode control character — the C0 set
// (< 0x20), DEL (0x7f) AND the C1 set (U+0080–U+009F, which includes the 8-bit CSI/OSC/DCS
// introducers) — and every Unicode FORMAT (Cf) rune (zero-width space/joiner, word joiner, bidi
// marks), then caps the result with a middle-ellipsis. It works rune-by-rune so a legitimate
// multibyte rune whose bytes fall in 0x80–0x9f (e.g. the en-dash "–") is preserved while a
// stray/invalid C1 byte is dropped. Escape bytes this library emits are the renderer's own palette
// SGR, added by renderElement's paint() AFTER this sanitize runs (so untrusted content can never
// carry a colour code). The Cf strip closes the watchdog-sentinel forgery vector: the sentinel is two
// Cf runes, so an unsanitized hostile branch/dir name could otherwise forge it and mask silence; the
// renderer-inserted zero-widths (the needle defuse and the sentinel) are added at the cmd layer AFTER
// sanitize, so they are unaffected (the ZWJ/ZWNJ degradation of emoji names is the accepted L-R2 cost).
// This is the statusline twin of the "nothing hostile transits scrollback" discipline; it is applied
// to EVERY model/dir/branch string before it reaches Render's output (SEC-2).
func sanitize(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i = skipEscape(s, i)
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size == 1:
			i++ // an invalid/stray byte (e.g. a lone C1 like 0x9b) — drop it
		case unicode.IsControl(r):
			i += size // C0, DEL, and C1 controls
		case unicode.Is(unicode.Cf, r):
			i += size // format runes: forbid a forged zero-width watchdog sentinel in untrusted content
		default:
			b.WriteString(s[i : i+size])
			i += size
		}
	}
	return capMiddle(b.String())
}

// skipEscape returns the index just past a single ESC-introduced sequence beginning at i
// (s[i] == 0x1b). The core guarantee is that the ESC byte itself never survives, so no escape
// can be introduced; on top of that it also swallows the now-inert parameter/string bytes of a
// CSI (ESC [ … final-byte) or OSC (ESC ] … BEL|ST) so no visible garbage like "[31m" leaks.
// Any other ESC (a lone ESC, or ESC + a standalone final byte) drops ONLY the ESC and leaves
// the following bytes as ordinary inert text. An unterminated CSI/OSC is consumed to
// end-of-string so no partial escape leaks.
func skipEscape(s string, i int) int {
	i++ // past ESC — from here no escape can be re-introduced
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameter/intermediate bytes until a final byte 0x40-0x7e
		i++
		for i < len(s) && (s[i] < 0x40 || s[i] > 0x7e) {
			i++
		}
		if i < len(s) {
			i++ // consume final byte
		}
	case ']': // OSC: string until BEL (0x07) or ST (ESC \)
		i++
		for i < len(s) && s[i] != 0x07 && s[i] != 0x1b {
			i++
		}
		if i < len(s) {
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				i += 2
			} else {
				i++ // BEL
			}
		}
	default: // lone/standalone ESC: the ESC is already dropped; keep the following bytes
	}
	return i
}

// capMiddle shortens s to maxElementRunes with a middle-ellipsis, keeping the head and tail.
func capMiddle(s string) string {
	if utf8.RuneCountInString(s) <= maxElementRunes {
		return s
	}
	runes := []rune(s)
	half := (maxElementRunes - 1) / 2
	return string(runes[:half]) + ellipsis + string(runes[len(runes)-half:])
}
