package cmd

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// #596 Phase 3 (K9/K20/K22). These tests live in their own file because
// watchdog_test.go is FROZEN by this phase's acceptance criterion 2 (it must stay
// byte-identical to commit 64b419a4, which is what pins the two C-6 silence tests and
// watchdogTick's 6-argument signature). Nothing here calls t.Parallel: the seams these
// tests reassign are package globals (tmux_isolation_enforce_test.go:47-50).

// startedWatchdog runs runWatchdog to its first select and returns what it printed.
//
// The pre-cancelled context is load-bearing, not tidiness. runWatchdog reaches
// signal.NotifyContext(cmd.Context(), …) only once resolveWatchdogScope stops returning an
// error, and cobra hands back Command.ctx verbatim — a bare &cobra.Command{} carries a nil
// ctx, and signal.NotifyContext(nil, …) panics with "cannot create context from nil parent",
// crashing the whole package test binary. A context that is already done also makes the
// ticker loop take <-ctx.Done() on entry, so the startup lines are assertable without
// waiting a full tick.
func startedWatchdog(t *testing.T, root string) (string, error) {
	t.Helper()
	t.Setenv("AF_ROOT", root)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	cmd, buf := newTestCmd()
	cmd.SetContext(ctx)
	err := runWatchdog(cmd, nil)
	return buf.String(), err
}

// TestWatchdog_EmptyScopeStartsInRecoveryOnlyMode is the central behavioural claim of the
// #408 launch-semantics revision (design-doc.md Decision 4, conflicts.md:61-80). Before this
// phase an empty watchdog_agents was a hard refusal, so on the factory's own default
// configuration there was no recovery process running at all — which is precisely the
// incident condition: the exhausted agent was outside the watchdog scope, so nothing was
// watching it. The process must now always start; an empty scope narrows what it DOES, never
// whether it RUNS.
func TestWatchdog_EmptyScopeStartsInRecoveryOnlyMode(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeTestStartupConfig(t, root, `{"agents":["alpha"]}`) // present, but no watchdog_agents
	setupHermeticSessions(t)

	out, err := startedWatchdog(t, root)

	if err != nil {
		t.Fatalf("an empty watchdog_agents must no longer abort the watchdog PROCESS (#596 Decision 4); got %v\nout=%q", err, out)
	}
	if !strings.Contains(out, "watchdog: started") {
		t.Errorf("the watchdog must announce that it started; out=%q", out)
	}
	// Both scopes are printed: the pane surface is inert, occupancy recovery is not.
	if !strings.Contains(out, "pane scope") || !strings.Contains(out, "recovery scope") {
		t.Errorf("the startup line must name BOTH scopes (design-doc.md:338-342); out=%q", out)
	}
	if !strings.Contains(strings.ToLower(out), "inert") {
		t.Errorf("an empty pane scope must be announced as inert; out=%q", out)
	}
	// Inertness must be stated, not merely implied by an empty list — an operator
	// skimming one line should learn the blast radius, not have to infer it.
	for _, promise := range []string{"no pane captured", "no silence nudges", "no keystrokes sent"} {
		if !strings.Contains(out, promise) {
			t.Errorf("the inert pane scope must spell out %q; out=%q", promise, out)
		}
	}
	// An omitted watchdog_agents is a SUPPORTED configuration under the revision, not an
	// error, so it must not leave an error breadcrumb behind.
	breadcrumb := filepath.Join(root, ".runtime", "watchdog_last_error")
	if _, statErr := os.Stat(breadcrumb); statErr == nil {
		t.Errorf("a supported empty-scope start must NOT write the error breadcrumb %s", breadcrumb)
	}
	// Ties acceptance criterion 4 to behaviour rather than to a source grep alone.
	if strings.Contains(out, "never monitors all agents") {
		t.Errorf("the removed refusal's claim must not survive in output; out=%q", out)
	}
}

// TestWatchdog_AllUnknownScopeStartsInRecoveryOnlyMode is the all-unknown half of the same
// revision, and it deliberately does NOT mirror the empty-scope case exactly: names that do
// not exist in agents.json remain a genuine operator misconfiguration, so the process starts
// but stays loud and leaves the durable breadcrumb.
func TestWatchdog_AllUnknownScopeStartsInRecoveryOnlyMode(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{"realagent":{"type":"autonomous","description":"x"}}}`)
	writeTestStartupConfig(t, root, `{"watchdog_agents":["ghost"]}`)
	setupHermeticSessions(t)

	out, err := startedWatchdog(t, root)

	if err != nil {
		t.Fatalf("an all-unknown scope must no longer abort the watchdog PROCESS; got %v\nout=%q", err, out)
	}
	if !strings.Contains(out, "ghost") {
		t.Errorf("the misconfigured name must still be named; out=%q", out)
	}
	if !strings.Contains(out, "watchdog: started") {
		t.Errorf("the watchdog must still start; out=%q", out)
	}
	breadcrumb := filepath.Join(root, ".runtime", "watchdog_last_error")
	if _, statErr := os.Stat(breadcrumb); statErr != nil {
		t.Errorf("a name that does not exist in agents.json is a real misconfiguration and must still leave the breadcrumb %s: %v", breadcrumb, statErr)
	}
}

// TestWatchdog_PollAgentsFunctionBodyNeverReferencesPollOccupancy mirrors the protective
// source-text scan the telemetry guard already carries (watchdog_test.go:730), for the same
// reason: the behavioural tests around it would stay green even if a future edit folded the
// occupancy sweep INSIDE pollAgents' per-agent loop, where it would run once per agent in the
// fleet instead of once per tick and would re-read every snapshot N times.
//
// It adds the positive half that precedent lacks — asserting watchdogTick's own body names
// both calls — because a scan that only forbids can be satisfied by deleting the wiring.
func TestWatchdog_PollAgentsFunctionBodyNeverReferencesPollOccupancy(t *testing.T) {
	src, err := os.ReadFile("watchdog.go")
	if err != nil {
		t.Fatalf("read watchdog.go: %v", err)
	}
	body := string(src)

	if got := functionBody(t, body, "func pollAgents("); strings.Contains(got, "pollOccupancy") {
		t.Error("pollAgents's function body references pollOccupancy — the occupancy sweep must sit " +
			"BESIDE pollAgents in watchdogTick, never inside it")
	}

	tick := functionBody(t, body, "func watchdogTick(")
	for _, needle := range []string{"pollAgents(", "pollOccupancy"} {
		if !strings.Contains(tick, needle) {
			t.Errorf("watchdogTick's function body must call %s — a wiring that silently disappears "+
				"would otherwise still satisfy the forbidding half of this scan", needle)
		}
	}
}

// functionBody returns the source text of the function introduced by decl, bounded by the
// next top-level func (the function may be the last in the file, in which case the remainder
// is its body).
func functionBody(t *testing.T, src, decl string) string {
	t.Helper()
	start := strings.Index(src, decl)
	if start < 0 {
		t.Fatalf("could not locate %q in watchdog.go", decl)
	}
	rest := src[start+len(decl):]
	if next := strings.Index(rest, "\nfunc "); next >= 0 {
		return rest[:next]
	}
	return rest
}

// TestWatchdog_HeartbeatAdvancesPerTick pins K22 behaviourally; the acceptance criterion is
// only a grep for the filename, which a commented-out writer would satisfy. The heartbeat is
// what makes the supervisor's OWN absence observable between af up runs — without it a dead
// watchdog and a healthy one look identical from the outside.
func TestWatchdog_HeartbeatAdvancesPerTick(t *testing.T) {
	root := newTestFactoryRoot(t)
	writeTestAgentsConfig(t, root, `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	setupHermeticSessions(t)
	stubTelemetryBackendGuard(t, nil)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	t.Cleanup(func() { newWatchdogTmux = oldTmux })

	heartbeat := filepath.Join(root, ".runtime", "watchdog_heartbeat")
	if _, err := os.Stat(heartbeat); err == nil {
		t.Fatalf("precondition: %s must not exist before the first tick", heartbeat)
	}

	watchdogTick(&cobra.Command{}, root, buildWatchdogScope([]string{"alpha"}, ""),
		map[string]*watchdogAgentState{}, map[string]int{}, 2)

	first, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatalf("a completed tick must write %s: %v", heartbeat, err)
	}
	firstAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(first)))
	if err != nil {
		t.Fatalf("heartbeat %q must be an RFC3339 timestamp: %v", first, err)
	}

	watchdogTick(&cobra.Command{}, root, buildWatchdogScope([]string{"alpha"}, ""),
		map[string]*watchdogAgentState{}, map[string]int{}, 2)

	second, err := os.ReadFile(heartbeat)
	if err != nil {
		t.Fatalf("read heartbeat after second tick: %v", err)
	}
	secondAt, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(second)))
	if err != nil {
		t.Fatalf("heartbeat %q must be an RFC3339 timestamp: %v", second, err)
	}
	if !secondAt.After(firstAt) {
		t.Errorf("the heartbeat must strictly advance per tick, got %s then %s "+
			"(second-granularity RFC3339 would make this flaky — the writer must use RFC3339Nano)",
			firstAt, secondAt)
	}
}

// stubOccupancy replaces the tick's occupancy sweep with a stated set of decisions, so the
// suppression pre-check can be exercised over the whole verdict matrix without planting
// snapshots, sessions and breaker state for each case. It also clears both pieces of
// cross-tick state, which a shared test binary otherwise leaks between cases.
func stubOccupancy(t *testing.T, decisions ...recoveryDecision) {
	t.Helper()
	resetRecoveredAgents()
	resetRecoveryTracks()
	orig := pollOccupancyFn
	pollOccupancyFn = func(string, *config.AgentConfig, config.RecoveryConfig, time.Time) []recoveryDecision {
		return decisions
	}
	t.Cleanup(func() {
		pollOccupancyFn = orig
		resetRecoveredAgents()
		resetRecoveryTracks()
	})
}

// firingDecision is a verdict that reached for the session — evaluateAgent clears fire when
// the recycle was fenced, when the agent is interactive, or when the breaker has halted, so
// a fire that survives to the caller means a recycle was attempted.
func firingDecision(agent string) recoveryDecision {
	return recoveryDecision{agent: agent, verdict: exhaustionVerdict{fire: true, trigger: triggerContextExhaustion}, executed: true}
}

// TestWatchdog_PaneScopeIsUntouchedWithoutRecoveryVerdict is the L-4 byte-equivalence proof
// (cross-review/analyst-review-design.md:224-232). The C-6 tests prove the silence path's
// CODE is unchanged; this proves its INPUTS are too, which is the claim that actually
// matters. Without it, a suppression pre-check keyed on mere presence in the decision slice
// would silently delete the silence surface for every live agent on every tick — and every
// existing test would stay green, because pollOccupancy returns one decision per evaluated
// agent regardless of outcome.
func TestWatchdog_PaneScopeIsUntouchedWithoutRecoveryVerdict(t *testing.T) {
	scope := buildWatchdogScope([]string{"alpha", "bravo"}, "")
	now := time.Now()

	// Every non-firing shape pollOccupancy can return for an agent it evaluated.
	resetRecoveredAgents()
	t.Cleanup(resetRecoveredAgents)
	noteRecoveredAgents([]recoveryDecision{
		{agent: "alpha", halted: true, verdict: exhaustionVerdict{reason: "breaker halted"}},
		{agent: "bravo", fenced: true, verdict: exhaustionVerdict{reason: "recycle fence"}},
		{agent: "alpha", verdict: exhaustionVerdict{escalate: true, reason: "channel dark at low occupancy"}},
		{agent: "bravo", verdict: exhaustionVerdict{advise: true}},
	}, testRecoveryConfig(), now)

	got := paneScopeExcludingRecovered(scope, now)

	if len(recoveredAgentUntil) != 0 {
		t.Errorf("no non-firing verdict may arm suppression, got %v", recoveredAgentUntil)
	}
	// Identity, not equality: pollAgents must receive the very map watchdogTick was handed,
	// so there is no copy in which a future edit could quietly drop a name.
	if reflect.ValueOf(got).Pointer() != reflect.ValueOf(scope).Pointer() {
		t.Errorf("with no firing verdict the pre-check must return the caller's own scope map "+
			"unmodified, got a different map (%v vs %v)", got, scope)
	}
}

// TestWatchdog_RecoveredAgentLeavesPaneScope is the non-vacuity companion: without it the
// test above would still pass if the pre-check were deleted outright.
func TestWatchdog_RecoveredAgentLeavesPaneScope(t *testing.T) {
	scope := buildWatchdogScope([]string{"alpha", "bravo"}, "")
	now := time.Now()

	resetRecoveredAgents()
	t.Cleanup(resetRecoveredAgents)
	noteRecoveredAgents([]recoveryDecision{
		firingDecision("alpha"),
		{agent: "bravo", verdict: exhaustionVerdict{advise: true}},
	}, testRecoveryConfig(), now)

	got := paneScopeExcludingRecovered(scope, now)

	if _, still := got["alpha"]; still {
		t.Error("an agent the occupancy surface recycled this tick must leave the pane scope — " +
			"respawnSession keeps the tmux session alive, so pollAgents would otherwise find " +
			"IsClaudeRunning false and double-recycle it through the crash branch")
	}
	if _, dropped := got["bravo"]; !dropped {
		t.Error("an agent with only an advisory verdict must stay in the pane scope")
	}
	if _, mutated := scope["alpha"]; !mutated {
		t.Error("the pre-check must not mutate the caller's scope map — the suppression is per-tick")
	}
}

// TestWatchdog_PaneSuppressionExpiresAfterGraceWindow pins that suppression is a window, not
// a latch. An agent that never returned to the pane scope would be permanently unmonitored
// by the surface that catches crashes and error patterns.
func TestWatchdog_PaneSuppressionExpiresAfterGraceWindow(t *testing.T) {
	scope := buildWatchdogScope([]string{"alpha"}, "")
	cfg := testRecoveryConfig()
	now := time.Now()

	resetRecoveredAgents()
	t.Cleanup(resetRecoveredAgents)
	noteRecoveredAgents([]recoveryDecision{firingDecision("alpha")}, cfg, now)

	grace := recoveryPaneGrace(cfg)
	if _, still := paneScopeExcludingRecovered(scope, now.Add(grace-time.Second))["alpha"]; still {
		t.Error("alpha must stay suppressed inside the grace window")
	}
	if _, back := paneScopeExcludingRecovered(scope, now.Add(grace+time.Second))["alpha"]; !back {
		t.Error("alpha must return to the pane scope once the grace window closes")
	}
	if len(recoveredAgentUntil) != 0 {
		t.Errorf("an expired entry must be reaped, not accumulated; got %v", recoveredAgentUntil)
	}
}

// TestWatchdog_RecoveredAgentIsNotNudgedByTheSilencePath drives the whole tick end to end,
// because the unit tests above assert the pre-check's contract and not that the tick
// actually applies it. Its paired cases are what make it meaningful: the SAME silent agent,
// the SAME number of ticks, differing only in whether the occupancy surface fired.
func TestWatchdog_RecoveredAgentIsNotNudgedByTheSilencePath(t *testing.T) {
	for _, tc := range []struct {
		name      string
		decisions []recoveryDecision
		wantNudge bool
	}{
		{"no recovery verdict ⇒ the silence path behaves exactly as before", []recoveryDecision{
			{agent: "alpha", verdict: exhaustionVerdict{advise: true}},
		}, true},
		{"recycled this tick ⇒ the silence path leaves it alone", []recoveryDecision{
			firingDecision("alpha"),
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := newTestFactoryRoot(t)
			writeTestAgentsConfig(t, root, `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
			setupHermeticSessions(t)
			stubTelemetryBackendGuard(t, nil)
			stubOccupancy(t, tc.decisions...)

			oldTmux := newWatchdogTmux
			newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "frozen pane"} }
			t.Cleanup(func() { newWatchdogTmux = oldTmux })

			nudges := 0
			oldNudge := watchdogNudgeFn
			watchdogNudgeFn = func(string) error { nudges++; return nil }
			t.Cleanup(func() { watchdogNudgeFn = oldNudge })

			scope := buildWatchdogScope([]string{"alpha"}, "")
			agentStates := map[string]*watchdogAgentState{}
			failures := map[string]int{}

			// Two ticks over an unchanging pane: the first records the hash, the second
			// reaches the silence threshold.
			for i := 0; i < 2; i++ {
				watchdogTick(&cobra.Command{}, root, scope, agentStates, failures, 2)
			}

			if tc.wantNudge && nudges == 0 {
				t.Error("an agent with no recovery verdict must still be nudged — the suppression " +
					"pre-check must not fire for it (L-4 byte-equivalence)")
			}
			if !tc.wantNudge && nudges != 0 {
				t.Errorf("a recycled agent must not also be nudged, got %d nudges", nudges)
			}
			if failures["alpha"] != 0 {
				t.Errorf("neither path may touch the circuit breaker here, got failures=%d", failures["alpha"])
			}
		})
	}
}
