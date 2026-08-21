package statusline

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitize(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain unchanged", "main", "main"},
		{"unicode preserved", "función·té", "función·té"},
		{"strips low control bytes", "a\x00b\x01c\x1fd", "abcd"},
		{"strips DEL 0x7f", "a\x7fb", "ab"},
		{"strips BEL", "a\x07b", "ab"},
		{"strips CSI color sequence", "\x1b[31mred\x1b[0m", "red"},
		{"strips CSI clear", "ma\x1b[2Jin", "main"},
		{"strips OSC title with BEL terminator", "\x1b]0;title\x07keep", "keep"},
		{"strips OSC title with ST terminator", "\x1b]0;title\x1b\\keep", "keep"},
		{"strips lone ESC keeps following byte", "a\x1bb", "ab"},
		{"strips ESC keeps standalone final byte as text", "x\x1bcyz", "xcyz"},
		{"strips valid C1 control rune U+009B (8-bit CSI)", "a\xc2\x9bb", "ab"},
		{"drops lone invalid C1 byte", "a\x9fb", "ab"},
		{"preserves en-dash whose UTF-8 has an 0x80 byte", "a\xe2\x80\x93b", "a\xe2\x80\x93b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := sanitize(tc.in)
			if got != tc.want {
				t.Errorf("sanitize(%q) = %q, want %q", tc.in, got, tc.want)
			}
			// No control RUNE (C0, DEL, or C1) may survive.
			for _, r := range got {
				if isControlRune(r) {
					t.Errorf("residual control rune U+%04X in %q", r, got)
				}
			}
		})
	}
}

func isControlRune(r rune) bool {
	return r < 0x20 || (r >= 0x7f && r <= 0x9f)
}

func TestSanitize_MiddleEllipsisCap(t *testing.T) {
	long := strings.Repeat("x", 200)
	got := sanitize(long)
	if n := utf8.RuneCountInString(got); n > 64 {
		t.Errorf("sanitize cap: %d runes, want <= 64", n)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("expected middle-ellipsis, got %q", got)
	}
	if !strings.HasPrefix(got, "x") || !strings.HasSuffix(got, "x") {
		t.Errorf("middle-ellipsis should keep head and tail, got %q", got)
	}

	// A short string is never capped.
	if got := sanitize("short"); got != "short" {
		t.Errorf("short string altered: %q", got)
	}
}
