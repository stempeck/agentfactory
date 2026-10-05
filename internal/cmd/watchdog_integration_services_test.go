package cmd

// Phase 2 (design 695 K10) pins for the watchdog's integration service trigger. These tests
// swap package seams, so none may call t.Parallel.

import (
	"context"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// stubIntegrationServicesGuard substitutes ensureIntegrationServicesFn with a call counter for
// watchdogTick tests (the twin of stubTelemetryBackendGuard), restoring it on cleanup.
func stubIntegrationServicesGuard(t *testing.T, body func()) *int32 {
	t.Helper()
	var calls int32
	orig := ensureIntegrationServicesFn
	ensureIntegrationServicesFn = func(ctx context.Context, cmd *cobra.Command, root string, scope serviceScope) []string {
		atomic.AddInt32(&calls, 1)
		if body != nil {
			body()
		}
		return nil
	}
	t.Cleanup(func() { ensureIntegrationServicesFn = orig })
	return &calls
}

type integrationTriggerCall struct {
	scope       serviceScope
	hasDeadline bool
	remaining   time.Duration
}

// TestWatchdog_IntegrationServicesTrigger (spec L838-839, D8): the tick fires the integration
// ensure beside pollAgents through its OWN single-flight, once per tick, never overlapping an
// in-flight attempt, never blocking the tick, under a context bounded to at most 30s, and for
// factory-scope services.
func TestWatchdog_IntegrationServicesTrigger(t *testing.T) {
	// Wiring, by source: watchdogTick calls the trigger after pollAgents returns and before
	// the heartbeat (a complete tick), never inside pollAgents.
	src, err := os.ReadFile("watchdog.go")
	if err != nil {
		t.Fatalf("read watchdog.go: %v", err)
	}
	tick := functionBody(t, string(src), "func watchdogTick(")
	poll := strings.Index(tick, "pollAgents(")
	trig := strings.Index(tick, "triggerIntegrationServicesGuard(")
	beat := strings.Index(tick, "writeWatchdogHeartbeat(")
	if trig < 0 || poll < 0 || beat < 0 || !(poll < trig && trig < beat) {
		t.Errorf("watchdogTick must call triggerIntegrationServicesGuard( after pollAgents( and before writeWatchdogHeartbeat( "+
			"(indexes pollAgents=%d trigger=%d heartbeat=%d)", poll, trig, beat)
	}

	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"worker":{"type":"autonomous","description":"test worker"}}}`)
	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	defer func() { newWatchdogTmux = oldTmux }()
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error { return nil }
	defer func() { watchdogNudgeFn = oldNudge }()
	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)

	var mu sync.Mutex
	var calls []integrationTriggerCall
	var entered, exited int32
	release := make(chan struct{})
	var releaseOnce sync.Once
	var block atomic.Bool
	orig := ensureIntegrationServicesFn
	ensureIntegrationServicesFn = func(ctx context.Context, cmd *cobra.Command, root string, scope serviceScope) []string {
		atomic.AddInt32(&entered, 1)
		defer atomic.AddInt32(&exited, 1)
		dl, ok := ctx.Deadline()
		mu.Lock()
		calls = append(calls, integrationTriggerCall{scope: scope, hasDeadline: ok, remaining: time.Until(dl)})
		mu.Unlock()
		if block.Load() {
			<-release
		}
		return nil
	}
	t.Cleanup(func() {
		releaseOnce.Do(func() { close(release) })
		// Only goroutines that actually entered the stub can still be reading it; wait for
		// them before restoring the var (never block on an attempt that never started).
		deadline := time.Now().Add(2 * time.Second)
		for atomic.LoadInt32(&exited) < atomic.LoadInt32(&entered) && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		ensureIntegrationServicesFn = orig
	})
	waitEntered := func(n int32) bool {
		deadline := time.Now().Add(2 * time.Second)
		for atomic.LoadInt32(&entered) < n && time.Now().Before(deadline) {
			time.Sleep(5 * time.Millisecond)
		}
		return atomic.LoadInt32(&entered) >= n
	}
	scope := buildWatchdogScope([]string{"worker"}, "")

	// One tick → exactly one ensure, factory scope, bounded context.
	watchdogTick(&cobra.Command{}, root, scope, map[string]*watchdogAgentState{}, map[string]int{}, 2)
	if !waitEntered(1) {
		t.Fatalf("one watchdog tick must fire the integration service ensure once; fired 0")
	}
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	first := append([]integrationTriggerCall(nil), calls...)
	mu.Unlock()
	if len(first) != 1 {
		t.Fatalf("one tick fired the ensure %d times, want exactly 1", len(first))
	}
	c := first[0]
	if c.scope != serviceScopeFactory {
		t.Errorf("the watchdog ensures factory-scope services (spec L791); got scope %v", c.scope)
	}
	if !c.hasDeadline || c.remaining <= 0 || c.remaining > integrationServicesWatchdogBound {
		t.Errorf("the trigger must bound the ensure with a context deadline of at most %s (D8, B16); hasDeadline=%v remaining=%s",
			integrationServicesWatchdogBound, c.hasDeadline, c.remaining)
	}

	// In flight: a blocked attempt makes later ticks skip, never stack, and never wait.
	deadline := time.Now().Add(2 * time.Second)
	for integrationServicesGuardInFlight.Load() && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	block.Store(true)
	before := atomic.LoadInt32(&entered)
	start := time.Now()
	for i := 0; i < 3; i++ {
		watchdogTick(&cobra.Command{}, root, scope, map[string]*watchdogAgentState{}, map[string]int{}, 2)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Errorf("three ticks took %s with the ensure blocked; the trigger must be async and never block the tick", elapsed)
	}
	if !waitEntered(before + 1) {
		t.Fatalf("the tick after the first attempt finished must fire the ensure again (the in-flight flag must clear)")
	}
	time.Sleep(100 * time.Millisecond)
	if got := atomic.LoadInt32(&entered) - before; got != 1 {
		t.Errorf("with one attempt in flight, three ticks entered the ensure %d times, want exactly 1 (own CompareAndSwap single-flight)", got)
	}
	if !integrationServicesGuardInFlight.Load() {
		t.Errorf("integrationServicesGuardInFlight must be held while an attempt is running")
	}
	releaseOnce.Do(func() { close(release) })
}
