package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/session"
)

func TestWatchdog_DetectsErrorPattern(t *testing.T) {
	output := "Some output...\nInvalid signature in thinking block\nMore output"
	detected, pattern := detectErrorPattern(output)
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
		detected, pattern := detectErrorPattern(output)
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
		detected, pattern := detectErrorPattern(output)
		if detected {
			t.Errorf("status 400 should NOT trigger detection, but got pattern %q for input %q", pattern, output)
		}
	}
}

func TestWatchdog_OnlyThinkingBlockTriggers(t *testing.T) {
	detected, pattern := detectErrorPattern("Some output\nInvalid signature in thinking block\nMore output")
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
		detected, pattern := detectErrorPattern(output)
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
			detected, cause := detectErrorPattern(tc.output)
			if !detected {
				t.Fatalf("expected an endpoint signature to be detected in %q", tc.output)
			}
			if !strings.Contains(cause, "endpoint failure") {
				t.Errorf("cause should name an endpoint failure, got %q", cause)
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
	detected, cause := detectErrorPattern("the gateway returned 503 Service Unavailable")
	if !detected {
		t.Fatal("expected 503 to be detected")
	}
	subject, body := watchdogFailureMail("worker_a", cause)
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
type fakeWatchdogTmux struct{ output string }

func (f *fakeWatchdogTmux) HasSession(string) (bool, error)         { return true, nil }
func (f *fakeWatchdogTmux) IsClaudeRunning(string) bool             { return true }
func (f *fakeWatchdogTmux) CapturePane(string, int) (string, error) { return f.output, nil }

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
