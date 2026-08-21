//go:build integration

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/tmux"
)

// TestStatuslinePaneRoundTrip_Integration proves the one thing the string-level unit tests cannot:
// that both zero-width mechanisms survive a REAL tmux pane write and capture-pane -J read-back.
// A terminal grid is exactly the class of layer entitled to normalise a wcwidth==0 rune away — and
// it demonstrably does at column 0 (watchdog.go:127-132) — so "works on a Go string literal" and
// "works once it touches a terminal" are different claims. The unit tier only ever made the first.
//
// The two mechanisms, and what breaks if the grid eats them:
//
//   - statuslineSentinel (watchdog.go:133) marks statusline lines so stripStatuslineLines removes
//     them before the silence hash. If tmux dropped it, the strip becomes a silent no-op and a
//     hung-but-alive agent whose statusline keeps ticking is never nudged — the #591 failure.
//   - the mid-needle U+200B spliced by defuseNeedle (statusline.go:248-253) keeps rendered content
//     that legitimately reads like an endpoint failure from tripping detectErrorPattern. If tmux
//     dropped it, a healthy agent is force-respawned on a false endpoint failure.
//
// SCOPE: this covers the tmux hop ONLY. It does not exercise the render→Claude-display hop (that is
// the K11 manual probe, Phase 5a). The shipped render core now DOES emit the sentinel per line
// (markStatuslineLines, pinned by TestFableIncr601_T7_RenderCoreEmitsSentinelPerLine); this test still
// composes the pane content from the internal/cmd primitives directly, to isolate the tmux hop from
// the render path rather than because the core omits the sentinel.
func TestStatuslinePaneRoundTrip_Integration(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	const (
		body = "agent1: waiting for input..."
		// An endpointFailureSignatures entry whose context is empty (watchdog.go:147), so the bare
		// substring trips detectErrorPattern. The context-scoped needles ("connection refused",
		// "connection timed out") additionally require apiRequestMarker, which would make the
		// control below fail for a reason unrelated to this test.
		rawNeedle = "504 Gateway Timeout"
	)

	// The pane under test: an agent body plus a two-line statusline whose path element is LITERALLY
	// named like an endpoint-failure needle — the false-positive case scrubWatchdogNeedles exists
	// for (statusline.go:228-234). Both statusline lines carry the sentinel AFTER visible text;
	// emitting it at column 0 would have tmux discard it and fail this test for the wrong reason.
	statusLine := func(elapsed string) string {
		return defuseNeedle("Opus 4.8 | /repo/"+rawNeedle+" | main | +12 -3 | T "+elapsed, rawNeedle) + statuslineSentinel
	}
	dailyLine := func(daily string) string {
		return "██░░░░░░░░ 18% (35k/200k) | $ 1.23 | D $ " + daily + statuslineSentinel
	}
	pane := func(elapsed, daily string) string {
		return strings.Join([]string{body, statusLine(elapsed), dailyLine(daily)}, "\n") + "\n"
	}

	// Control: the UNDEFUSED needle must actually trip the watchdog. Without this, "detectErrorPattern
	// stayed false after the round trip" is a statement about an arbitrary string.
	if bad, _, _ := detectErrorPattern("Opus 4.8 | /repo/" + rawNeedle); !bad {
		t.Fatalf("%q must be a watchdog endpoint-failure needle or the defuse leg of this test proves nothing", rawNeedle)
	}
	// Control: the defuse works in-process, so any later failure is attributable to the tmux hop
	// rather than to defuseNeedle itself.
	if bad, cause, _ := detectErrorPattern(statusLine("2m5s")); bad {
		t.Fatalf("defuseNeedle failed before tmux was involved: detectErrorPattern reported %q", cause)
	}

	ticks := []struct{ elapsed, daily string }{
		{"2m5s", "4.56"},
		{"2m15s", "9.01"},
		{"2m25s", "12.30"},
	}
	sessionBase := "af-test-" + hashName(t.Name())
	captures := make([]string, len(ticks))
	for i, tk := range ticks {
		captures[i] = capturePaneRoundTrip(t, fmt.Sprintf("%s-%d", sessionBase, i), pane(tk.elapsed, tk.daily), "T "+tk.elapsed)
	}

	captured, marked := captures[0], statusLine(ticks[0].elapsed)

	// THE load-bearing assertion. Everything below it is vacuous without it, because
	// stripStatuslineLines short-circuits when the sentinel is absent (watchdog.go:188-190) and
	// returns its input unchanged: "no sentinel after the strip" is therefore TRUE in exactly the
	// world where tmux ate the sentinel — the failure this test exists to detect. Asserting the
	// written line came back VERBATIM states in one shot that the sentinel survived, that its two
	// runes stayed contiguous and in position, and that the mid-needle U+200B survived too.
	// The line is built from the shipped constants, never hand-typed, so this is a round trip of
	// the real bytes rather than of a look-alike.
	if !strings.Contains(captured, marked) {
		t.Fatalf("the sentinel-marked line did not survive the tmux round trip\n  wrote: %q\ncaptured: %q", marked, captured)
	}

	// The defuse still holds on real captured bytes...
	if bad, cause, _ := detectErrorPattern(captured); bad {
		t.Fatalf("a defused needle tripped the watchdog after the round trip: %s\ncaptured=%q", cause, captured)
	}
	// ...and it holds because the U+200B is still splicing the needle, not because the content
	// never arrived: detectErrorPattern("") is false, so an empty capture would pass the line above.
	if !strings.Contains(captured, "04 Gateway Timeout") {
		t.Fatalf("the needle's visible text never reached the pane; captured=%q", captured)
	}

	// The strip removes the marked lines from real captured bytes...
	stripped := stripStatuslineLines(captured)
	if strings.Contains(stripped, statuslineSentinel) {
		t.Fatalf("stripStatuslineLines left a sentinel behind after the round trip; stripped=%q", stripped)
	}
	if strings.Contains(stripped, "Gateway Timeout") {
		t.Fatalf("the marked statusline line survived the strip; stripped=%q", stripped)
	}
	// ...and only those: the agent body is what the silence hash must still see. Without this, a
	// strip that deleted everything would satisfy the two assertions above.
	if !strings.Contains(stripped, body) {
		t.Fatalf("the strip removed the agent body; stripped=%q", stripped)
	}

	// The silence statement, over REAL captured bytes rather than synthetic ones: three panes
	// differing ONLY in their sentinel-marked lines must still reach the threshold. This is the
	// integration-tier counterpart of TestCheckSilence_StatuslineOnlyChange_StillTrips.
	for i := range captures {
		for j := i + 1; j < len(captures); j++ {
			if captures[i] == captures[j] {
				t.Fatalf("captures %d and %d are identical; the silence leg would pass vacuously", i, j)
			}
		}
	}
	state := make(map[string]*watchdogAgentState)
	threshold := len(captures)
	for i, out := range captures {
		silent := checkSilence("agent1", out, state, threshold)
		if i < threshold-1 && silent {
			t.Errorf("poll %d: should not be silent yet", i)
		}
		if i == threshold-1 && !silent {
			t.Error("real captured panes differing ONLY in their sentinel-marked statusline must still reach the silence threshold")
		}
	}
}

// capturePaneRoundTrip writes content into a fresh detached tmux session and reads it back through
// the SHIPPED joined capture the watchdog's silence path uses (tmux.CapturePaneJoined, called at
// watchdog.go:672), so the bytes under test travel the production route rather than a hand-rolled
// flag list. Under //go:build integration the client's fail-closed guard is compiled out
// (internal/tmux/guard_integration.go), so this reaches real tmux.
//
// Content is delivered as a FILE read by the pane's own command rather than typed with send-keys:
// an interactive shell echoes the command line it is given, which would put the UNDEFUSED needle
// into the pane and trip detectErrorPattern for a reason that has nothing to do with the round trip.
// Routing through cat also keeps content containing "|", "$" and "%" out of the shell's reach.
func capturePaneRoundTrip(t *testing.T, session, content, readyMarker string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "pane.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write pane content: %v", err)
	}

	killStaleTmuxSession(t, session)
	// The trailing sleep holds the pane open: without it the command exits, tmux tears the session
	// down and there is nothing left to capture. Explicit geometry keeps the fixture clear of the
	// default 80-column wrap so the capture is deterministic (-J would rejoin a wrap anyway).
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", session,
		"-x", "200", "-y", "50", "cat "+path+"; sleep 60").CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session %s: %s\n%s", session, err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", session).Run() })

	// The capture is kept RAW. Whitespace-normalising it (as improvement_integration_test.go does
	// for its own readiness check) would collapse the pane into one line and destroy the very line
	// structure stripStatuslineLines operates on.
	tx := tmux.NewTmux()
	var captured string
	for i := 0; i < 25; i++ {
		out, err := tx.CapturePaneJoined(session, 50)
		if err == nil {
			captured = out
			if strings.Contains(captured, readyMarker) {
				return captured
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("pane content never appeared in %s within 5s (marker %q); last capture=%q", session, readyMarker, captured)
	return ""
}
