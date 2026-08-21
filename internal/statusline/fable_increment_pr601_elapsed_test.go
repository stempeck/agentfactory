package statusline

import (
	"os"
	"strings"
	"testing"
)

// T6 (PR #601) — elapsed must render in the #591 screenshot format, not Go's Duration.String().
// ux.md C2.1: `T <H>h <MM>m` (hours + zero-padded minutes, seconds dropped); `<1h => <N>m`;
// `<1m => 0m`. The requirement holder made screenshot parity binding (unresolved_threads T6).
// formatDuration returns the body; the render caller prepends "T ". RED at head: it returns Go
// durations like "1h6m5s" / "2m5s".
func TestFableIncr601_ElapsedScreenshotFormat(t *testing.T) {
	cases := []struct {
		ms   int64
		want string
	}{
		{3965000, "1h 06m"}, // 1h6m5s -> hours + zero-padded minutes, seconds dropped
		{3600000, "1h 00m"}, // exactly one hour
		{7200000, "2h 00m"}, // two hours, zero minutes
		{125000, "2m"},      // 2m5s, under an hour -> minutes only (no hour, no zero-pad)
		{600000, "10m"},     // 10m exactly
		{45000, "0m"},       // 45s, under a minute -> 0m (seconds dropped)
	}
	for _, c := range cases {
		if got := formatDuration(c.ms); got != c.want {
			t.Errorf("formatDuration(%d) = %q, want %q (screenshot parity, PR #601 T6 / ux.md C2.1)", c.ms, got, c.want)
		}
	}
}

// F-G (PR #601 T7) — RenderSingleLine is dead code (zero non-test callers) that, if ever wired, would
// silently drop tokens/color/sentinel (it builds a zero-value RenderOpts). It must be deleted; this
// source-scan guard prevents its reintroduction. RED at head: the function is still defined.
func TestFableIncr601_FG_RenderSingleLineDeleted(t *testing.T) {
	src, err := os.ReadFile("render.go")
	if err != nil {
		t.Fatalf("read render.go: %v", err)
	}
	if strings.Contains(string(src), "func RenderSingleLine") {
		t.Errorf("F-G: RenderSingleLine is still defined in render.go — it is dead code that drops " +
			"tokens/color/sentinel; delete it (PR #601 T7)")
	}
}
