package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

func TestWatchdog_DetectsErrorPattern(t *testing.T) {
	output := "Some output...\nInvalid signature in thinking block\nMore output"
	detected, pattern, _ := detectErrorPattern(output)
	if !detected {
		t.Fatal("should detect 'Invalid signature in thinking block'")
	}
	if pattern == "" {
		t.Error("pattern description should not be empty")
	}
}

func TestWatchdog_HTTP400NotDetected(t *testing.T) {
	outputs := []string{
		"error: HTTP 400 Bad Request",
		"GitHub API returned HTTP 400 rate limited",
		"HTTP 400 in response from upstream",
	}
	for _, output := range outputs {
		detected, pattern, _ := detectErrorPattern(output)
		if detected {
			t.Errorf("HTTP 400 should NOT trigger detection, but got pattern %q for input %q", pattern, output)
		}
	}
}

func TestWatchdog_Status400NotDetected(t *testing.T) {
	outputs := []string{
		"response returned status 400",
		"API call failed with status 400",
	}
	for _, output := range outputs {
		detected, pattern, _ := detectErrorPattern(output)
		if detected {
			t.Errorf("status 400 should NOT trigger detection, but got pattern %q for input %q", pattern, output)
		}
	}
}

func TestWatchdog_OnlyThinkingBlockTriggers(t *testing.T) {
	detected, pattern, _ := detectErrorPattern("Some output\nInvalid signature in thinking block\nMore output")
	if !detected {
		t.Fatal("should detect 'Invalid signature in thinking block'")
	}
	if pattern != "Invalid signature in thinking block" {
		t.Errorf("expected pattern 'Invalid signature in thinking block', got %q", pattern)
	}
}

func TestWatchdog_NoFalsePositive(t *testing.T) {
	outputs := []string{
		"Working on step 1...",
		"Reading files...\nRunning tests...\nAll 42 tests passed",
		"Analyzing code in internal/cmd/watchdog.go",
		"HTTP 200 OK",
		// Bare transport errors from local tooling (go test / curl / ssh against a
		// closed port) carry no model-gateway context and must not be mistaken for an
		// endpoint outage, or a working agent gets respawned on a false positive.
		"dial tcp 127.0.0.1:4000: connect: connection refused",
		"read tcp 127.0.0.1:54233->127.0.0.1:6379: connection timed out",
	}
	for _, output := range outputs {
		detected, pattern, _ := detectErrorPattern(output)
		if detected {
			t.Errorf("false positive on %q: detected pattern %q", output, pattern)
		}
	}
}

// TestWatchdog_DetectsEndpointSignatures is the positive half of the endpoint-failure
// detection (issue #508): detectErrorPattern recognizes the enumerated signatures a down
// or misbehaving gateway surfaces into the pane (connection refused/timeout, gateway 5xx,
// the codex 400 unsupported_api_for_model code, LiteLLM proxy error classes) and returns
// a human-readable cause naming the endpoint failure. The connection cases carry the
// model API request context (/v1/messages) that scopes them to a real gateway failure.
func TestWatchdog_DetectsEndpointSignatures(t *testing.T) {
	cases := []struct {
		name   string
		output string
	}{
		{"http_502", "upstream error: 502 Bad Gateway"},
		{"http_503", "the gateway returned 503 Service Unavailable"},
		{"http_504", "504 Gateway Timeout from the proxy"},
		{"conn_refused", `Post "http://127.0.0.1:4000/v1/messages": dial tcp 127.0.0.1:4000: connect: connection refused`},
		{"conn_timeout", `Post "https://gw.local/v1/messages": connection timed out`},
		{"unsupported_api", `{"error":{"code":"unsupported_api_for_model"}}`},
		{"litellm_500", "litellm.InternalServerError: llm provider raised an error"},
		{"litellm_503", "litellm.ServiceUnavailableError: upstream is down"},
		{"litellm_conn", "litellm.APIConnectionError: could not reach the endpoint"},
		{"litellm_timeout", "litellm.Timeout: request exceeded the deadline"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detected, cause, mailOnly := detectErrorPattern(tc.output)
			if !detected {
				t.Fatalf("expected an endpoint signature to be detected in %q", tc.output)
			}
			if !strings.Contains(cause, "endpoint failure") {
				t.Errorf("cause should name an endpoint failure, got %q", cause)
			}
			// Every signature here predates the #598 mail-only posture and must keep respawning;
			// a stray true in the table would otherwise leave a class of failures unrecovered.
			if mailOnly {
				t.Errorf("%q must not carry the mail-only posture", tc.output)
			}
		})
	}
}

// TestWatchdog_EndpointCauseNamedInEscalationMail pins the escalation-mail half of
// W11: the cause detectErrorPattern returns is threaded verbatim into the operator
// escalation mail, so the operator sees "endpoint failure: …" rather than a generic
// respawn. sendHandoffMail is a no-op under `go test`, so the mail text is proven
// through the pure watchdogFailureMail formatter that recoverAgent feeds.
func TestWatchdog_EndpointCauseNamedInEscalationMail(t *testing.T) {
	detected, cause, mailOnly := detectErrorPattern("the gateway returned 503 Service Unavailable")
	if !detected {
		t.Fatal("expected 503 to be detected")
	}
	subject, body := watchdogFailureMail("worker_a", cause, mailOnly)
	if !strings.Contains(subject, "worker_a") {
		t.Errorf("escalation subject should name the agent, got %q", subject)
	}
	if !strings.Contains(body, cause) {
		t.Errorf("escalation body should thread the detected cause %q verbatim, got %q", cause, body)
	}
	if !strings.Contains(body, "endpoint failure") {
		t.Errorf("escalation body should name the endpoint failure, got %q", body)
	}
}

func TestWatchdog_SilenceDetection(t *testing.T) {
	state := make(map[string]*watchdogAgentState)
	output := "some static output that never changes"
	threshold := 3

	for i := 0; i < threshold; i++ {
		silent := checkSilence("agent1", output, state, threshold)
		if i < threshold-1 && silent {
			t.Errorf("poll %d: should not be silent yet", i)
		}
		if i == threshold-1 && !silent {
			t.Error("should detect silence after threshold polls")
		}
	}
}

// TestWatchdog_SilenceDetectionWithStatusline is the Gap-2 behavioral proof (issue #591): a
// rendered statusline must NOT defeat the watchdog's silence detection, and the mechanism that
// guarantees it is the SENTINEL, not the content. It holds the STATIC case — a statusline whose
// text happens not to change across polls — so it pins that stripping sentinel-marked lines did
// not disturb the ordinary path. The CHANGING case is TestCheckSilence_StatuslineOnlyChange_StillTrips.
//
// Its original rationale ("the default element set excludes the only ticking element") no longer
// holds: issue #600 restored elapsed and daily to the default and moved the guarantee into the
// watchdog itself. The fixture is now the CURRENT eight-element render built by statuslinePane,
// replacing the pre-#595 six-element literal that still carried the removed "· N tok"
// occupancy-as-spend halves (K9 item 6).
//
// A marked static pane ALONE would only re-prove TestWatchdog_SilenceDetection: the strip removes
// the statusline before hashing, so the remaining signal is the unchanging body. The differential
// below is what this test uniquely states — the strip is driven by the sentinel, not by
// statusline-shaped content, and it removes exactly the marked lines.
func TestWatchdog_SilenceDetectionWithStatusline(t *testing.T) {
	const body = "agent1: waiting for input..."
	marked := statuslinePane(body, "2m5s", "4.56")
	unmarked := strings.ReplaceAll(marked, statuslineSentinel, "")

	// The strip must actually ENGAGE on this fixture; if statuslinePane ever stopped marking, every
	// assertion below would pass for the ordinary reason.
	if stripStatuslineLines(marked) == marked {
		t.Fatal("the fixture carries no sentinel: the strip is a no-op here, so this test would only " +
			"re-prove TestWatchdog_SilenceDetection")
	}
	// ...and it must engage ONLY on the mark. Byte-identical statusline text without the sentinel
	// must survive, or the strip is matching content and the dormant I1.2 fallback has leaked into I1.1.
	if stripStatuslineLines(unmarked) != unmarked {
		t.Fatal("an UNMARKED statusline was stripped: the strip is keying on content, not the sentinel")
	}
	// The strip is line-selective rather than a nuke: what survives is EXACTLY the agent body, which
	// is what the silence hash must then see.
	if got := strings.TrimSpace(stripStatuslineLines(marked)); got != body {
		t.Fatalf("strip must leave exactly the agent body\n got: %q\nwant: %q", got, body)
	}

	// The ordinary path is undisturbed in BOTH shapes: a static statusline pane still reaches the
	// silence threshold whether or not its lines are marked.
	for _, tc := range []struct{ name, pane string }{{"marked", marked}, {"unmarked", unmarked}} {
		state := make(map[string]*watchdogAgentState)
		threshold := 3

		for i := 0; i < threshold; i++ {
			silent := checkSilence("agent1", tc.pane, state, threshold)
			if i < threshold-1 && silent {
				t.Errorf("%s poll %d: a static statusline pane must not read as activity before threshold", tc.name, i)
			}
			if i == threshold-1 && !silent {
				t.Errorf("%s: a static statusline pane must still reach the silence threshold", tc.name)
			}
		}
	}
}

// statuslinePane builds an idle agent pane whose only variable content is a sentinel-marked
// two-line statusline, so a test can vary the chrome while holding the agent body fixed.
// The sentinel sits AFTER a visible character on each line, mirroring K6's emission
// constraint (tmux discards a zero-width rune written at column 0 — see statuslineSentinel).
func statuslinePane(body, elapsed, daily string) string {
	return strings.Join([]string{
		body,
		"Opus 4.8 | /repo | main | +12 -3 | T " + elapsed + statuslineSentinel,
		"██░░░░░░░░ 18% (35k/200k) | $ 1.23 | D $ " + daily + statuslineSentinel,
	}, "\n")
}

// TestCheckSilence_StatuslineOnlyChange_StillTrips is the AC-3 core: once the watchdog strips
// sentinel-marked lines, a hung agent whose pane differs ONLY in its statusline must still trip
// the silence threshold. Both restored-by-K4 volatile elements move here — "elapsed" ticks with
// wall time and "daily" is moved by a SECOND session's snapshot (design-doc.md:554-555), which
// is the case the 6-element default was invented to avoid and which now has no config-side
// defense at all.
//
// This test is the replacement guarantee that lets K9 ledger item 4 invert: the render-side
// byte-identical-across-spend pin (internal/statusline/fable_increment_pr595_test.go) can stop
// carrying the masking property precisely because this one now does
// (integration.md:106-109).
func TestCheckSilence_StatuslineOnlyChange_StillTrips(t *testing.T) {
	state := make(map[string]*watchdogAgentState)
	body := "agent1: waiting for input..."
	panes := []string{
		statuslinePane(body, "2m5s", "4.56"),
		statuslinePane(body, "2m15s", "9.01"),
		statuslinePane(body, "2m25s", "12.30"),
	}
	threshold := len(panes)

	// Non-vacuity: if the fixtures ever collapse to one string this degrades into
	// TestWatchdog_SilenceDetection and would pass for the wrong reason.
	for i := range panes {
		for j := i + 1; j < len(panes); j++ {
			if panes[i] == panes[j] {
				t.Fatalf("fixture panes %d and %d are identical; the test would pass vacuously", i, j)
			}
		}
	}

	for i, out := range panes {
		silent := checkSilence("agent1", out, state, threshold)
		if i < threshold-1 && silent {
			t.Errorf("poll %d: should not be silent yet", i)
		}
		if i == threshold-1 && !silent {
			t.Error("a pane changing ONLY in its sentinel-marked statusline must still reach the silence threshold")
		}
	}
}

// TestCheckSilence_WrappedNarrowPane_StillTrips pins Gap 3. On a narrow pane tmux wraps one
// logical statusline across several physical rows and only the FIRST row carries the sentinel,
// so a per-line strip over a plain capture leaves the ticking figure in the continuation
// fragment and the hash keeps moving. Capturing with -J rejoins the wrap into one logical line
// that the strip removes whole.
//
// Both shapes are asserted: the joined one because it is the shipped behavior, and the plain
// one because pinning the known-bad variant is the clearest statement of WHY the joined capture
// is load-bearing rather than a nicety.
func TestCheckSilence_WrappedNarrowPane_StillTrips(t *testing.T) {
	body := "agent1: waiting for input..."

	// One logical statusline row as tmux -J returns it: the wrap boundary is gone.
	joined := func(elapsed, daily string) string {
		return strings.Join([]string{
			body,
			"Opus 4.8 | /very/long/repo/path | main | +12 -3 | T " + elapsed + statuslineSentinel,
			"██░░░░░░░░ 18% (35k/200k) | $ 1.23 | D $ " + daily + statuslineSentinel,
		}, "\n")
	}
	// The same content as a PLAIN capture of a 30-column pane: tmux emits each wrapped
	// fragment as its own physical line and only the first fragment carries the sentinel.
	plain := func(elapsed, daily string) string {
		return strings.Join([]string{
			body,
			"Opus 4.8 | /very/long/repo/" + statuslineSentinel,
			"path | main | +12 -3 | T " + elapsed,
			"██░░░░░░░░ 18% (35k/200k) |" + statuslineSentinel,
			" $ 1.23 | D $ " + daily,
		}, "\n")
	}

	ticks := []struct{ elapsed, daily string }{{"2m5s", "4.56"}, {"2m15s", "9.01"}, {"2m25s", "12.30"}}
	threshold := len(ticks)

	joinedState := make(map[string]*watchdogAgentState)
	for i, tk := range ticks {
		silent := checkSilence("agent1", joined(tk.elapsed, tk.daily), joinedState, threshold)
		if i == threshold-1 && !silent {
			t.Error("a joined (-J) wrapped statusline must be stripped whole and still trip silence")
		}
	}

	// PLAIN capture: tmux emits each wrapped fragment as its own physical line, so only the fragments
	// carrying the sentinel are stripped — the others (`… | T <elapsed>` and `$ 1.23 | D $ <daily>`)
	// leak their ticking figures past the strip. Before I1.2 was armed this leak made the plain capture
	// fail to trip, which is why -J (joined capture) is the strip's primary mechanism. With the mask now
	// ARMED (PR #601 BODY-1/F-A) those leaked volatile figures are normalized to placeholders, so the
	// plain capture ALSO trips: the mask backstops a wrap the strip missed. -J still matters — it strips
	// the whole statusline cleanly instead of relying on per-figure masking of whatever leaked.
	plainState := make(map[string]*watchdogAgentState)
	var plainTripped bool
	for _, tk := range ticks {
		if checkSilence("agent1", plain(tk.elapsed, tk.daily), plainState, threshold) {
			plainTripped = true
		}
	}
	if !plainTripped {
		t.Error("a plain (non -J) wrapped statusline leaks ticking figures past the strip; the armed " +
			"I1.2 mask must normalize them so a hung agent's statusline-only churn still trips silence")
	}
}

// TestCheckSilence_RealActivity_ResetsHash is the anti-vacuity partner of the two tests above:
// without it the strip could degenerate into hashing a constant and both would still pass.
func TestCheckSilence_RealActivity_ResetsHash(t *testing.T) {
	state := make(map[string]*watchdogAgentState)
	threshold := 5

	for i := 0; i < 3; i++ {
		checkSilence("agent1", statuslinePane("agent1: waiting for input...", "2m5s", "4.56"), state, threshold)
	}
	if state["agent1"].silenceCount != 3 {
		t.Fatalf("expected silence count 3 before real activity, got %d", state["agent1"].silenceCount)
	}

	// Same statusline, different agent body: genuine work must still reset the counter.
	if checkSilence("agent1", statuslinePane("agent1: running go test ./...", "2m5s", "4.56"), state, threshold) {
		t.Error("genuine pane activity must not read as silence")
	}
	if state["agent1"].silenceCount != 0 {
		t.Errorf("silence counter should reset on real agent output change, got %d", state["agent1"].silenceCount)
	}
}

// TestMaskVolatileRenders_CatchesShippedFormats is the I1.2 fallback's drift defense
// (integration.md:62: "a guard test renders examples and asserts the masks catch them").
// The fallback is dormant, so nothing else would notice if a render format moved out from under
// its regexes — this test is the only thing standing between a format change and a fallback that
// silently masks nothing on the day it is activated.
func TestMaskVolatileRenders_CatchesShippedFormats(t *testing.T) {
	// The SHIPPED forms come first — these are what the renderer emits today, taken from the
	// golden at internal/statusline/render_test.go. A mask that only handles the format the design
	// doc anticipated would be a fallback that masks nothing on the day it is switched on.
	for _, tc := range []struct{ name, in, want string }{
		{"bar (shipped)", "████░░░░░░ 35% (352k/1.0M)", "<bar>"},
		{"session cost (shipped, spaced)", "$ 12.34", "<session>"},
		{"daily cost (shipped, spaced)", "D $ 80.64", "<daily>"},
		{"daily redirected (shipped, spaced)", "D ~$ 80.64", "<daily>"},
		// Every duration band Go's Duration.String() produces — it emits no spaces between
		// components, so the hour band ("1h6m6s") looks nothing like K5's planned "1h 06m".
		// A long-running autonomous session is the normal case, not an edge case.
		{"elapsed seconds (shipped)", "T 45s", "<elapsed>"},
		{"elapsed minutes (shipped)", "T 2m5s", "<elapsed>"},
		{"elapsed hours (shipped)", "T 1h6m6s", "<elapsed>"},
		{"elapsed exact hour (shipped)", "T 1h0m0s", "<elapsed>"},
		{"elapsed half-day (shipped)", "T 12h34m56s", "<elapsed>"},

		// K5 (Phase 3) reformats these; both forms must mask so the reformat cannot disarm the
		// fallback silently.
		{"session cost+tokens (K5)", "$21.95 · 352k tok", "<session>"},
		{"daily cost+tokens (K5)", "D $80.64 · 1.4M tok", "<daily>"},
		{"daily redirected (K5)", "D ~$80.64", "<daily>"},
		{"elapsed hours (K5)", "T 1h 06m", "<elapsed>"},
	} {
		if got := maskVolatileRenders(tc.in); got != tc.want {
			t.Errorf("%s: maskVolatileRenders(%q) = %q, want %q", tc.name, tc.in, got, tc.want)
		}
	}

	// Elapsed fixtures DERIVED, not hand-copied. Both format gaps found in review came from
	// fixtures written by hand that the renderer never emits, so the elapsed band — the one that
	// ticks every second — is generated the same way internal/statusline's formatDuration does
	// (Duration.Round(time.Second).String()). A future duration shape is then covered mechanically
	// instead of depending on someone remembering to add a row.
	for _, ms := range []int64{
		300, // 0s        — sub-second rounds to "T 0s", and renderElement drops elapsed
		//                        only when the duration is EXACTLY 0, so this really renders
		1000,      // 1s
		45000,     // 45s
		60000,     // 1m0s
		125000,    // 2m5s      — the golden fixture's value
		3599000,   // 59m59s    — last value before the hour band
		3600000,   // 1h0m0s    — exact hour boundary
		3966000,   // 1h6m6s
		45296000,  // 12h34m56s
		356400000, // 99h0m0s   — three-digit hours
	} {
		in := "T " + (time.Duration(ms) * time.Millisecond).Round(time.Second).String()
		if got := maskVolatileRenders(in); got != "<elapsed>" {
			t.Errorf("elapsed mask misses a shipped duration: maskVolatileRenders(%q) = %q, want %q",
				in, got, "<elapsed>")
		}
	}

	// Whole-pane sweep in BOTH formats: nothing volatile may survive. The shipped case is the one
	// that matters — cross-session daily movement is the hazard this fallback exists to absorb.
	for _, pane := range []string{
		"Opus 4.8 | /repo | main | +12 -3 | T 2m5s\n████░░░░░░ 35% (352k/1.0M) | $ 12.34 | D $ 80.64",
		"Opus 4.8 | /repo | main | +12 -3 | T 1h6m6s\n████░░░░░░ 35% (352k/1.0M) | $ 12.34 | D $ 80.64",
		"Opus 4.8 | /repo | main | +12 -3 | T 1h 06m\n████░░░░░░ 35% (352k/1.0M) | $21.95 · 352k tok | D $80.64 · 1.4M tok",
	} {
		masked := maskVolatileRenders(pane)
		for _, leaked := range []string{"2m5s", "1h6m6s", "1h 06m", "35%", "352k/1.0M", "12.34", "21.95", "80.64"} {
			if strings.Contains(masked, leaked) {
				t.Errorf("volatile figure %q survived masking:\n in=%q\nout=%q", leaked, pane, masked)
			}
		}
	}

	// Non-volatile content must be left alone — a mask that ate the whole pane would "pass" every
	// assertion above while destroying the watchdog's ability to see real activity.
	const body = "agent1: running go test ./..."
	if got := maskVolatileRenders(body); got != body {
		t.Errorf("ordinary agent output must pass through unmasked: %q -> %q", body, got)
	}
}

// TestMaskVolatileRenders_IsArmed pins that I1.2 IS wired into the silence path as a belt-and-
// suspenders alongside I1.1 (the sentinel strip). The Phase-5a live probe that would confirm the
// sentinel survives Claude's display hop CANNOT run in an autonomous factory (ADR-018 — CI cannot
// observe the live factory), so the display hop is permanently UNVERIFIED: masking (a superset of
// stripping) closes the watchdog-masking hazard whether or not the sentinel survives (PR #601
// BODY-1/F-A). It is one-way safe — masking can only make silence MORE willing to trip, never less,
// so it can never hide a real hang.
//
// The distinguishing case is an UNMARKED volatile figure — a statusline whose sentinel was eaten by
// the display hop: the strip lets it through (hash moves), but the armed mask normalizes it so a
// statusline-only pane change still trips silence.
func TestMaskVolatileRenders_IsArmed(t *testing.T) {
	state := make(map[string]*watchdogAgentState)
	threshold := 3
	// No sentinel anywhere: this is indistinguishable from real agent output.
	pane := func(daily string) string {
		return "agent1: waiting for input...\n████░░░░░░ 18% (35k/200k) | $ 1.23 | D $ " + daily
	}

	dailies := []string{"4.56", "9.01", "12.30"}
	// Non-vacuity, same guard as the AC-3 matrix: if the fixtures ever collapse to one string the
	// hash would be stable for the ordinary reason and this test would stop testing dormancy.
	for i := range dailies {
		for j := i + 1; j < len(dailies); j++ {
			if pane(dailies[i]) == pane(dailies[j]) {
				t.Fatalf("fixture panes %d and %d are identical; the test would pass vacuously", i, j)
			}
		}
	}
	// The masks must actually cover this fixture, or "silence did not trip" proves nothing.
	if maskVolatileRenders(pane("4.56")) == pane("4.56") {
		t.Fatal("the fixture contains nothing the I1.2 masks recognize, so it cannot distinguish " +
			"strip from mask; update it alongside volatileRenderMasks")
	}

	var tripped bool
	for _, d := range dailies {
		if checkSilence("agent1", pane(d), state, threshold) {
			tripped = true
		}
	}
	if !tripped {
		t.Error("checkSilence did NOT normalize UNMARKED volatile figures — the I1.2 mask fallback " +
			"must be ARMED (mask-after-strip) so an un-sentineled statusline still trips silence " +
			"detection; the sentinel strip alone cannot cover Claude's display hop (PR #601 BODY-1/F-A)")
	}
}

// TestWatchdog_SilencePathReadsJoinedCapture pins WHICH capture feeds the silence hash. The
// strip is only sound on a -J capture: tmux marks just the first physical row of a wrapped line,
// so a plain capture leaks continuation fragments past it. A refactor that kept checkSilence
// correct but quietly reverted the poll loop to the plain capture would leave every other test
// green, which is exactly what this one exists to catch.
func TestWatchdog_SilencePathReadsJoinedCapture(t *testing.T) {
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{
		"agents": {
			"factoryworker": {"type": "autonomous", "description": "autonomous worker"}
		}
	}`)

	// The plain capture never repeats (so it could never trip silence); the joined capture is
	// static (so it trips at the threshold). A nudge therefore proves the joined view was used.
	var pollCount int
	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux {
		pollCount++
		return &fakeWatchdogTmux{
			output: fmt.Sprintf("plain capture, poll %d, never repeats", pollCount),
			joined: "idle, waiting for input",
		}
	}
	defer func() { newWatchdogTmux = oldTmux }()

	nudged := map[string]int{}
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error {
		nudged[sessionID]++
		return nil
	}
	defer func() { watchdogNudgeFn = oldNudge }()

	agentStates := make(map[string]*watchdogAgentState)
	failures := make(map[string]int)
	scope := map[string]struct{}{"factoryworker": {}}
	for i := 0; i < 3; i++ {
		pollAgents(&cobra.Command{}, root, scope, agentStates, failures, 3)
	}

	if len(nudged) == 0 {
		t.Error("silence never tripped: the poll loop is hashing the plain capture, not the joined one")
	}
}

func TestWatchdog_SilenceResets(t *testing.T) {
	state := make(map[string]*watchdogAgentState)
	threshold := 5

	for i := 0; i < 3; i++ {
		checkSilence("agent1", "same output", state, threshold)
	}
	if state["agent1"].silenceCount != 3 {
		t.Fatalf("expected silence count 3 before reset, got %d", state["agent1"].silenceCount)
	}

	checkSilence("agent1", "different output now", state, threshold)

	if state["agent1"].silenceCount != 0 {
		t.Errorf("silence counter should reset on output change, got %d", state["agent1"].silenceCount)
	}
}

func TestWatchdog_CircuitBreaker(t *testing.T) {
	failures := make(map[string]int)
	maxFailures := 3

	for i := 0; i < maxFailures; i++ {
		failures["agent1"]++
	}

	if shouldRespawn(failures, "agent1", maxFailures) {
		t.Error("should NOT respawn after circuit breaker threshold reached")
	}

	if !shouldRespawn(failures, "agent2", maxFailures) {
		t.Error("agent2 has no failures, should be allowed to respawn")
	}
}

func TestWatchdog_CircuitBreakerResets(t *testing.T) {
	failures := make(map[string]int)
	failures["agent1"] = 5

	resetCircuitBreaker(failures, "agent1")

	if failures["agent1"] != 0 {
		t.Errorf("circuit breaker should reset to 0, got %d", failures["agent1"])
	}
}

func TestWatchdog_SilenceNudgesNotKills(t *testing.T) {
	agentStates := make(map[string]*watchdogAgentState)
	failures := make(map[string]int)

	output := "static output"
	threshold := 3
	for i := 0; i < threshold; i++ {
		checkSilence("agent1", output, agentStates, threshold)
	}

	nudged := false
	old := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error {
		nudged = true
		return nil
	}
	defer func() { watchdogNudgeFn = old }()

	handleSilenceNudge("test-session", "agent1", agentStates, failures)

	if !nudged {
		t.Error("expected nudge function to be called on silence")
	}
	if failures["agent1"] != 0 {
		t.Errorf("silence nudge should NOT increment failures, got %d", failures["agent1"])
	}
	if agentStates["agent1"].silenceCount != 0 {
		t.Errorf("silence counter should reset after nudge, got %d", agentStates["agent1"].silenceCount)
	}
}

func TestWatchdog_SilenceNudgeNoCircuitBreakerIncrement(t *testing.T) {
	agentStates := make(map[string]*watchdogAgentState)
	failures := make(map[string]int)
	failures["agent1"] = 2

	agentStates["agent1"] = &watchdogAgentState{silenceCount: 5}

	old := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error { return nil }
	defer func() { watchdogNudgeFn = old }()

	handleSilenceNudge("test-session", "agent1", agentStates, failures)

	if failures["agent1"] != 2 {
		t.Errorf("silence nudge should NOT change failure count, was 2 got %d", failures["agent1"])
	}
}

func TestWatchdog_CheckpointBeforeKill(t *testing.T) {
	tmpDir := t.TempDir()
	agentDir := filepath.Join(tmpDir, ".agentfactory", "agents", "test-agent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("git", "init")
	cmd.Dir = agentDir
	if err := cmd.Run(); err != nil {
		t.Fatalf("git init failed: %v", err)
	}

	checkpointBeforeKill(agentDir, "test error pattern")

	cp, err := checkpoint.Read(agentDir)
	if err != nil {
		t.Fatalf("reading checkpoint: %v", err)
	}
	if cp == nil {
		t.Fatal("checkpoint should exist after checkpointBeforeKill")
	}
	if cp.Notes == "" {
		t.Error("checkpoint should contain notes describing the recovery reason")
	}
}

func TestWatchdog_InteractiveAgentNoRespawn(t *testing.T) {
	if shouldAutoRecover("interactive") {
		t.Error("interactive agents should get alert-only, not auto-respawn")
	}
	if !shouldAutoRecover("autonomous") {
		t.Error("autonomous agents should be auto-recoverable")
	}
	if !shouldAutoRecover("") {
		t.Error("agents with empty type should default to auto-recoverable")
	}
}

// fakeWatchdogTmux reports every session as alive with static output so the
// silence threshold trips on every agent, letting a test drive the poll loop.
// joined is the -J view the silence path reads; it falls back to output so the
// existing construction sites keep meaning what they meant, and a test that needs
// the two capture paths to differ sets it explicitly.
type fakeWatchdogTmux struct{ output, joined string }

func (f *fakeWatchdogTmux) HasSession(string) (bool, error)         { return true, nil }
func (f *fakeWatchdogTmux) IsClaudeRunning(string) bool             { return true }
func (f *fakeWatchdogTmux) CapturePane(string, int) (string, error) { return f.output, nil }
func (f *fakeWatchdogTmux) CapturePaneJoined(string, int) (string, error) {
	if f.joined != "" {
		return f.joined, nil
	}
	return f.output, nil
}

func writeTestAgentsConfig(t *testing.T, root, json string) {
	t.Helper()
	dotDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(dotDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "agents.json"), []byte(json), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestWatchdog_PollSilenceRespectsAgentType drives the actual poll loop with a
// mixed fleet under a nil scope and asserts at the loop level (not via the
// isolated helper) that a nil/empty scope is fail-closed: NO agent (interactive
// OR autonomous) is silence-nudged (issue #408). The interactive-vs-autonomous
// distinction is exercised by TestWatchdog_PollScopeFiltersAgents (populated scope).
func TestWatchdog_PollSilenceRespectsAgentType(t *testing.T) {
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{
		"agents": {
			"manager": {"type": "interactive", "description": "human-supervised manager"},
			"factoryworker": {"type": "autonomous", "description": "autonomous worker"}
		}
	}`)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "idle, waiting for input"} }
	defer func() { newWatchdogTmux = oldTmux }()

	nudged := map[string]int{}
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error {
		nudged[sessionID]++
		return nil
	}
	defer func() { watchdogNudgeFn = oldNudge }()

	agentStates := make(map[string]*watchdogAgentState)
	failures := make(map[string]int)
	const threshold = 2

	// Several ticks so silence would trip for every agent regardless of map order.
	// nil/empty scope monitors NOTHING (fail-closed, issue #408) — no agent is polled.
	for i := 0; i < 4; i++ {
		pollAgents(&cobra.Command{}, root, nil, agentStates, failures, threshold)
	}

	managerSession := session.SessionName("manager")
	workerSession := session.SessionName("factoryworker")

	if nudged[managerSession] != 0 {
		t.Errorf("nil scope must monitor nothing: 'manager' got %d nudges, want 0", nudged[managerSession])
	}
	if nudged[workerSession] != 0 {
		t.Errorf("nil scope must monitor nothing: 'factoryworker' got %d nudges, want 0", nudged[workerSession])
	}
}

// TestWatchdog_PollScopeFiltersAgents verifies the SC5 set-membership scope: a
// non-nil scope only polls in-scope agents (an out-of-scope autonomous agent is
// never nudged), and an interactive in-scope agent stays alert-only.
func TestWatchdog_PollScopeFiltersAgents(t *testing.T) {
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{
		"agents": {
			"manager": {"type": "interactive", "description": "human-supervised manager"},
			"worker_a": {"type": "autonomous", "description": "in-scope autonomous worker"},
			"worker_b": {"type": "autonomous", "description": "out-of-scope autonomous worker"}
		}
	}`)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "idle, waiting for input"} }
	defer func() { newWatchdogTmux = oldTmux }()

	nudged := map[string]int{}
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error {
		nudged[sessionID]++
		return nil
	}
	defer func() { watchdogNudgeFn = oldNudge }()

	// Scope to {manager (interactive), worker_a (autonomous)} — worker_b excluded.
	scope := buildWatchdogScope([]string{"manager", "worker_a"}, "")

	agentStates := make(map[string]*watchdogAgentState)
	failures := make(map[string]int)
	const threshold = 2

	for i := 0; i < 4; i++ {
		pollAgents(&cobra.Command{}, root, scope, agentStates, failures, threshold)
	}

	managerSession := session.SessionName("manager")
	workerASession := session.SessionName("worker_a")
	workerBSession := session.SessionName("worker_b")

	if nudged[workerBSession] != 0 {
		t.Errorf("out-of-scope agent 'worker_b' must NOT be polled/nudged, got %d nudges", nudged[workerBSession])
	}
	if nudged[managerSession] != 0 {
		t.Errorf("interactive in-scope agent 'manager' must stay alert-only, got %d nudges", nudged[managerSession])
	}
	if nudged[workerASession] == 0 {
		t.Error("autonomous in-scope agent 'worker_a' must be silence-nudged, got 0 nudges")
	}
}

func TestBuildWatchdogScope(t *testing.T) {
	if scope := buildWatchdogScope(nil, ""); scope == nil {
		t.Error("no flags must yield a non-nil no-scope set (monitor nothing), got nil")
	} else if len(scope) != 0 {
		t.Errorf("no flags must yield an EMPTY no-scope set, got %v", scope)
	}
	if scope := buildWatchdogScope([]string{"  ", ""}, ""); scope == nil {
		t.Error("only-blank entries must yield a non-nil no-scope set, got nil")
	} else if len(scope) != 0 {
		t.Errorf("only-blank entries must yield an EMPTY no-scope set, got %v", scope)
	}

	scope := buildWatchdogScope([]string{"a", " b "}, "c")
	if scope == nil {
		t.Fatal("expected a non-nil scope set")
	}
	for _, want := range []string{"a", "b", "c"} {
		if _, in := scope[want]; !in {
			t.Errorf("scope missing %q; got %v", want, scope)
		}
	}
	if len(scope) != 3 {
		t.Errorf("scope size = %d, want 3 (got %v)", len(scope), scope)
	}

	// The legacy single --agent alone forms a one-element scope.
	single := buildWatchdogScope(nil, "solo")
	if _, in := single["solo"]; !in || len(single) != 1 {
		t.Errorf("single --agent should yield scope {solo}, got %v", single)
	}
}

// --- Phase 4: REAP-1 watchdog reap matrix (AC-4) ---

// reapCall records one reap invocation: the agent dir and the agentName the seam was
// given (the AF_ROLE the reap subprocess needs for worktree removal).
type reapCall struct{ dir, agentName string }

// stubReapSession replaces the reap-session shell seam with a recorder of the reap
// calls; auto-restored. Also installs a benign fake tmux so the non-reaped poll paths
// do not touch a real session.
func stubReapSession(t *testing.T) *[]reapCall {
	t.Helper()
	var reaped []reapCall
	origReap := reapImprovementSession
	reapImprovementSession = func(dir, agentName string) error {
		reaped = append(reaped, reapCall{dir, agentName})
		return nil
	}
	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	t.Cleanup(func() {
		reapImprovementSession = origReap
		newWatchdogTmux = oldTmux
	})
	return &reaped
}

func pollOnce(root string, scope map[string]struct{}) {
	pollAgents(&cobra.Command{}, root, scope, map[string]*watchdogAgentState{}, map[string]int{}, 2)
}

func TestWatchdog_ReapImprovement_AgedReaped(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	m := improvementMarker{Formula: "fx", Caller: "manager", FiredAt: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	reaped := stubReapSession(t)

	pollOnce(root, buildWatchdogScope([]string{"alpha"}, ""))

	want := config.AgentDir(root, "alpha")
	if len(*reaped) != 1 || (*reaped)[0].dir != want {
		t.Fatalf("aged marker must be reaped for %q, got %v", want, *reaped)
	}
	// The reap must carry the agent name so the subprocess can inject AF_ROLE and
	// actually remove the worktree (design HIGH-1: reclaim the MaxWorktrees slot).
	if (*reaped)[0].agentName != "alpha" {
		t.Errorf("reap must pass agentName 'alpha' (for AF_ROLE), got %q", (*reaped)[0].agentName)
	}
}

func TestWatchdog_ReapImprovement_YoungUntouched(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	m := improvementMarker{Formula: "fx", Caller: "manager", FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	reaped := stubReapSession(t)

	pollOnce(root, buildWatchdogScope([]string{"alpha"}, ""))

	if len(*reaped) != 0 {
		t.Fatalf("young marker must be left untouched, got reaped %v", *reaped)
	}
}

func TestWatchdog_ReapImprovement_NoMarkerNoOp(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	reaped := stubReapSession(t)

	pollOnce(root, buildWatchdogScope([]string{"alpha"}, ""))

	if len(*reaped) != 0 {
		t.Fatalf("no marker must be a no-op, got reaped %v", *reaped)
	}
}

func TestWatchdog_ReapImprovement_EnvOverride(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	// A 2-minute-old marker: younger than the 30m default (no reap), older than a
	// 1-minute override (reap).
	m := improvementMarker{Formula: "fx", Caller: "manager", FiredAt: time.Now().Add(-2 * time.Minute).UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	reaped := stubReapSession(t)
	scope := buildWatchdogScope([]string{"alpha"}, "")

	// Default 30m ceiling: 2-minute marker is young ⇒ no reap.
	pollOnce(root, scope)
	if len(*reaped) != 0 {
		t.Fatalf("under the 30m default a 2-min marker must NOT reap, got %v", *reaped)
	}

	// Override to 1 minute ⇒ the same marker is now aged ⇒ reap.
	t.Setenv("AF_IMPROVEMENT_REAP_AFTER", "1")
	pollOnce(root, scope)
	if len(*reaped) != 1 {
		t.Fatalf("AF_IMPROVEMENT_REAP_AFTER=1 must reap a 2-min marker, got %v", *reaped)
	}
}

func TestWatchdog_ReapImprovement_OutOfScopeSkipped(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true, "beta": true})
	// beta has an aged marker but is NOT in the watchdog scope ⇒ never reaped.
	m := improvementMarker{Formula: "fx", Caller: "manager", FiredAt: time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "beta", m); err != nil {
		t.Fatal(err)
	}
	reaped := stubReapSession(t)

	pollOnce(root, buildWatchdogScope([]string{"alpha"}, ""))

	if len(*reaped) != 0 {
		t.Fatalf("an out-of-scope agent's marker must not be reaped, got %v", *reaped)
	}
}

// --- fable-implement Step 1 (Root Cause A, R4): telemetry-backend liveness at the
// watchdog's periodic tick ---

// stubTelemetryBackendGuard substitutes ensureTelemetryBackendFn with a call
// recorder, restoring the original (the real ensureTelemetryBackend) on cleanup.
func stubTelemetryBackendGuard(t *testing.T, body func()) *int32 {
	t.Helper()
	var calls int32
	orig := ensureTelemetryBackendFn
	ensureTelemetryBackendFn = func(ctx context.Context, cmd *cobra.Command, root string) {
		atomic.AddInt32(&calls, 1)
		if body != nil {
			body()
		}
	}
	t.Cleanup(func() { ensureTelemetryBackendFn = orig })
	return &calls
}

func TestWatchdog_TelemetryGuardFiresBesidePollAgents(t *testing.T) {
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"worker":{"type":"autonomous","description":"test worker"}}}`)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	defer func() { newWatchdogTmux = oldTmux }()
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error { return nil }
	defer func() { watchdogNudgeFn = oldNudge }()

	calls := stubTelemetryBackendGuard(t, nil)
	scope := buildWatchdogScope([]string{"worker"}, "")

	watchdogTick(&cobra.Command{}, root, scope, map[string]*watchdogAgentState{}, map[string]int{}, 2)

	// The in-flight goroutine's completion is not synchronized with the tick
	// returning by design (async, per DO-NOT-CHANGE latency bound) — wait briefly
	// for it rather than asserting immediately after a non-blocking dispatch.
	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("telemetry backend guard fired %d times on one tick, want exactly 1", got)
	}
}

func TestEnsureTelemetryBackend_WatchdogNeverOverlapsInFlightAttempts(t *testing.T) {
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"worker":{"type":"autonomous","description":"test worker"}}}`)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	defer func() { newWatchdogTmux = oldTmux }()
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error { return nil }
	defer func() { watchdogNudgeFn = oldNudge }()

	release := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1) // exactly one attempt ever enters — CompareAndSwap skips the other two ticks
	var inFlight int32
	var maxConcurrent int32
	orig := ensureTelemetryBackendFn
	ensureTelemetryBackendFn = func(ctx context.Context, cmd *cobra.Command, root string) {
		defer wg.Done()
		n := atomic.AddInt32(&inFlight, 1)
		for {
			old := atomic.LoadInt32(&maxConcurrent)
			if n <= old || atomic.CompareAndSwapInt32(&maxConcurrent, old, n) {
				break
			}
		}
		<-release
		atomic.AddInt32(&inFlight, -1)
	}
	defer func() {
		close(release)
		wg.Wait() // the entered goroutine's read of ensureTelemetryBackendFn must fully finish
		// before this restores the var, or the restore races with that read (found by -race)
		ensureTelemetryBackendFn = orig
	}()

	scope := buildWatchdogScope([]string{"worker"}, "")
	agentStates := map[string]*watchdogAgentState{}
	failures := map[string]int{}

	// Three ticks fired back-to-back while the first attempt is still "running"
	// (blocked on release) — none should be allowed to overlap it.
	for i := 0; i < 3; i++ {
		watchdogTick(&cobra.Command{}, root, scope, agentStates, failures, 2)
	}
	time.Sleep(100 * time.Millisecond) // let the first goroutine's CAS actually land before reading maxConcurrent

	if got := atomic.LoadInt32(&maxConcurrent); got > 1 {
		t.Errorf("max concurrent telemetry-backend-guard attempts = %d, want at most 1 — a second tick must skip, never stack, while one is in flight", got)
	}
}

func TestWatchdog_TelemetryGuardDoesNotBlockPollAgents(t *testing.T) {
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"worker":{"type":"autonomous","description":"test worker"}}}`)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	defer func() { newWatchdogTmux = oldTmux }()
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error { return nil }
	defer func() { watchdogNudgeFn = oldNudge }()

	blocked := make(chan struct{})
	done := make(chan struct{})
	orig := ensureTelemetryBackendFn
	ensureTelemetryBackendFn = func(ctx context.Context, cmd *cobra.Command, root string) {
		<-blocked // held until the test's own assertions have run — simulates the ~12s worst case
		close(done)
	}
	defer func() {
		close(blocked)
		<-done // wait for the leaked goroutine to finish reading the stub before restoring the
		// var — restoring first races the read with this defer's write (found by -race)
		ensureTelemetryBackendFn = orig
	}()

	scope := buildWatchdogScope([]string{"worker"}, "")
	start := time.Now()
	watchdogTick(&cobra.Command{}, root, scope, map[string]*watchdogAgentState{}, map[string]int{}, 2)
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("watchdogTick took %s with the telemetry guard permanently blocked, want it to return promptly (pollAgents must not wait on it)", elapsed)
	}
}

// TestWatchdog_PollAgentsFunctionBodyNeverReferencesTelemetryGuard is a protective,
// source-text scan (DO-NOT-CHANGE): the behavioral tests above call pollAgents
// directly and would stay green even if a future edit mistakenly folded the
// telemetry guard INSIDE pollAgents's per-agent loop instead of beside it in
// watchdogTick — they assert only on nudge/reap counts, never on whether a
// telemetry-guard call occurred inside that function. This closes that blind spot
// mechanically.
func TestWatchdog_PollAgentsFunctionBodyNeverReferencesTelemetryGuard(t *testing.T) {
	src, err := os.ReadFile("watchdog.go")
	if err != nil {
		t.Fatalf("read watchdog.go: %v", err)
	}
	body := string(src)

	start := strings.Index(body, "func pollAgents(")
	if start < 0 {
		t.Fatal("could not locate func pollAgents( in watchdog.go")
	}
	rest := body[start+len("func pollAgents("):]
	fnBody := rest
	if next := strings.Index(rest, "\nfunc "); next >= 0 {
		// pollAgents is not necessarily the last function in the file — bound the
		// scan to its own body when a later top-level func exists; otherwise (as
		// today) pollAgents runs to end-of-file and the whole remainder is its body.
		fnBody = rest[:next]
	}

	for _, needle := range []string{"ensureTelemetryBackend", "triggerTelemetryBackendGuard"} {
		if strings.Contains(fnBody, needle) {
			t.Errorf("pollAgents's function body references %q — the telemetry-backend guard must sit "+
				"BESIDE pollAgents in watchdogTick, never inside it (it would otherwise fire once per "+
				"agent in the fleet instead of once per tick)", needle)
		}
	}
}
