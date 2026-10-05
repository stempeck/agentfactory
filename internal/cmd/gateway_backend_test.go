package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// This file pins ensureGatewayBackend (fable-implement PR #694 Phase 7 / K19).
// fable-implement Phase 5 (RED): ensureGatewayBackend's body is currently empty
// (gateway_backend.go) — every behavioral test below is annotated with its current
// (RED, or documented vacuous-pass/skip) expectation. Phase 6 fills in the decision
// logic (decisions.md D1-D4).

func gatewayBackendFixture(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// writeGatewayRelaunchScript seeds .agentfactory/gateway-relaunch.sh so
// ensureGatewayBackend's "script present" check (once Phase 6 adds it) does not
// itself become the reason a down-session test no-ops.
func writeGatewayRelaunchScript(t *testing.T, root, body string) string {
	t.Helper()
	dir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if body == "" {
		body = "#!/bin/bash\ntrue\n"
	}
	script := filepath.Join(dir, "gateway-relaunch.sh")
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		t.Fatalf("write gateway-relaunch.sh: %v", err)
	}
	return script
}

// writeApiKeySecret gives gatewayAuthMode a resolvable api-key credential handle
// (config_models.go:699 apiKeySecretPresent's own path).
func writeApiKeySecret(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(config.ConfigDir(root), "secrets")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openai.key"), []byte("sk-test-key"), 0o600); err != nil {
		t.Fatalf("write openai.key: %v", err)
	}
}

// writeGatewaySubscriptionHandle gives gatewayAuthMode a resolvable
// codex-subscription credential handle (gateway_auth.go:150 gatewayAuthHandlePath).
func writeGatewaySubscriptionHandle(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(config.ConfigDir(root), "secrets", "chatgpt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir secrets/chatgpt: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"tokens":{}}`), 0o600); err != nil {
		t.Fatalf("write chatgpt auth.json: %v", err)
	}
}

// writeGatewayReconcileMarker seeds .runtime/gateway/reconciling with a raw payload
// (not the gatewayReconcileMarker Go type) so a malformed-JSON case is expressible too.
func writeGatewayReconcileMarker(t *testing.T, root, rawJSON string) {
	t.Helper()
	dir := filepath.Join(root, ".runtime", "gateway")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir .runtime/gateway: %v", err)
	}
	if err := os.WriteFile(gatewayReconcilingMarkerPath(root), []byte(rawJSON), 0o644); err != nil {
		t.Fatalf("write reconciling marker: %v", err)
	}
}

// installFakeGatewayTmux overrides newCmdTmux so "litellm" reports the given
// liveness, mirroring installFakeTmuxPresent's shape (agents_test.go:29).
func installFakeGatewayTmux(t *testing.T, present bool) *fakeTmux {
	t.Helper()
	fake := newFakeTmux()
	fake.present["litellm"] = present
	orig := newCmdTmux
	newCmdTmux = func() cmdTmux { return fake }
	t.Cleanup(func() { newCmdTmux = orig })
	return fake
}

// --- AC1: session absent + script present + mode resolvable -> relaunch exactly once ---

func TestEnsureGatewayBackend_SessionAbsentTriggersRelaunchOnce(t *testing.T) {
	// RED: ensureGatewayBackend's body is empty today, so the relaunch seam is never
	// reached — relaunchCalls stays 0, want 1.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)

	var relaunchCalls int
	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	ensureGatewayBackend(context.Background(), &cobra.Command{}, root)

	if relaunchCalls != 1 {
		t.Errorf("relaunch seam called %d times with session absent+script present+mode resolvable, want exactly 1", relaunchCalls)
	}
}

func TestEnsureGatewayBackend_SessionPresentNoOp(t *testing.T) {
	// RED (vacuous pass): the stub never reaches the relaunch seam regardless of
	// session state, so this passes today for the WRONG reason (it never checks
	// HasSession at all). Phase 6 must keep it true for the right reason.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, true)

	var relaunchCalls int
	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	ensureGatewayBackend(context.Background(), &cobra.Command{}, root)

	if relaunchCalls != 0 {
		t.Errorf("relaunch seam called %d times against a present session, want 0", relaunchCalls)
	}
}

func TestEnsureGatewayBackend_ScriptAbsentWarns(t *testing.T) {
	// RED: no script written; today's stub prints nothing at all.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	installFakeGatewayTmux(t, false)

	cmd := &cobra.Command{}
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	ensureGatewayBackend(context.Background(), cmd, root)

	if !strings.Contains(stderr.String(), "warning:") {
		t.Errorf("missing relaunch script printed no warning; got stderr=%q", stderr.String())
	}
}

func TestEnsureGatewayBackend_ModeUnresolvedWarnsAndSkipsRelaunch(t *testing.T) {
	// RED: decisions.md D1 (err-gated on gatewayAuthMode). Both credential handles
	// present with no record file is gatewayAuthMode's one error case
	// (gateway_auth.go:245-247) -> no-op+warn, script never invoked.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewaySubscriptionHandle(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)

	var relaunchCalls int
	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	cmd := &cobra.Command{}
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	ensureGatewayBackend(context.Background(), cmd, root)

	if !strings.Contains(stderr.String(), "warning:") {
		t.Errorf("ambiguous/unresolved auth mode printed no warning; got stderr=%q", stderr.String())
	}
	if relaunchCalls != 0 {
		t.Errorf("relaunch seam called %d times with an unresolved auth mode, want 0", relaunchCalls)
	}
}

func TestEnsureGatewayBackend_RelaunchFailureWarnsWithOutput(t *testing.T) {
	// RED: decisions.md D2 (unconditional stderr surfacing). Relaunch seam returns a
	// non-nil error with captured output -> that output must appear in the warning.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)

	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		return "warning: litellm.key missing — refusing to relaunch litellm", context.DeadlineExceeded
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	cmd := &cobra.Command{}
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	ensureGatewayBackend(context.Background(), cmd, root)

	if !strings.Contains(stderr.String(), "warning:") {
		t.Errorf("relaunch failure printed no warning; got stderr=%q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "litellm.key missing") {
		t.Errorf("relaunch failure warning dropped the script's captured output; got stderr=%q", stderr.String())
	}
}

func TestEnsureGatewayBackend_RelaunchSuccessWithOutputStillWarns(t *testing.T) {
	// RED: decisions.md D2's core case — the script itself can exit 0 while still
	// writing a stderr warning (quickstart.sh:1355-1373, missing-handle branch). A
	// literal telemetry mirror (err-gated) would silently swallow this; K19 must
	// surface non-empty output unconditionally, regardless of exit code.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)

	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		return "warning: OPENAI_API_KEY handle empty — refusing to relaunch litellm", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	cmd := &cobra.Command{}
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	ensureGatewayBackend(context.Background(), cmd, root)

	if !strings.Contains(stderr.String(), "OPENAI_API_KEY handle empty") {
		t.Errorf("successful (exit 0) relaunch with non-empty output must still surface it as a warning; got stderr=%q", stderr.String())
	}
}

// --- AC3: reconciling-marker interlock ---

func TestEnsureGatewayBackend_LiveMarkerSkipsRelaunch(t *testing.T) {
	// RED (vacuous pass): the stub never invokes relaunch regardless, so this passes
	// today for the WRONG reason. Phase 6 must keep it true because the live marker
	// short-circuits BEFORE the HasSession/script checks (decisions.md D4).
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)
	marker, err := json.Marshal(gatewayReconcileMarker{PID: os.Getpid(), Since: time.Now().Unix()})
	if err != nil {
		t.Fatalf("marshal live marker: %v", err)
	}
	writeGatewayReconcileMarker(t, root, string(marker))

	var relaunchCalls int
	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	ensureGatewayBackend(context.Background(), &cobra.Command{}, root)

	if relaunchCalls != 0 {
		t.Errorf("relaunch seam called %d times under a live reconciling marker, want 0", relaunchCalls)
	}
	if _, statErr := os.Stat(gatewayReconcilingMarkerPath(root)); statErr != nil {
		t.Errorf("a live marker must not be removed, but Stat failed: %v", statErr)
	}
}

func TestEnsureGatewayBackend_StaleMarkerDeadPidRemovesAndProceeds(t *testing.T) {
	// RED: today's stub neither removes the marker nor proceeds to relaunch.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)
	marker, err := json.Marshal(gatewayReconcileMarker{PID: 99999999, Since: time.Now().Unix()})
	if err != nil {
		t.Fatalf("marshal dead-pid marker: %v", err)
	}
	writeGatewayReconcileMarker(t, root, string(marker))

	var relaunchCalls int
	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	ensureGatewayBackend(context.Background(), &cobra.Command{}, root)

	if relaunchCalls != 1 {
		t.Errorf("relaunch seam called %d times under a dead-pid stale marker, want exactly 1", relaunchCalls)
	}
	if _, statErr := os.Stat(gatewayReconcilingMarkerPath(root)); !os.IsNotExist(statErr) {
		t.Errorf("a stale (dead-pid) marker must be removed, Stat err = %v", statErr)
	}
}

func TestEnsureGatewayBackend_StaleMarkerOldSinceRemovesAndProceeds(t *testing.T) {
	// RED: same as the dead-pid case, but staleness comes from age, not liveness —
	// pins the two independent stale triggers (decisions.md D3/D4) separately.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)
	marker, err := json.Marshal(gatewayReconcileMarker{PID: os.Getpid(), Since: time.Now().Add(-20 * time.Minute).Unix()})
	if err != nil {
		t.Fatalf("marshal old-since marker: %v", err)
	}
	writeGatewayReconcileMarker(t, root, string(marker))

	var relaunchCalls int
	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	ensureGatewayBackend(context.Background(), &cobra.Command{}, root)

	if relaunchCalls != 1 {
		t.Errorf("relaunch seam called %d times under a 20-minute-old stale marker, want exactly 1", relaunchCalls)
	}
	if _, statErr := os.Stat(gatewayReconcilingMarkerPath(root)); !os.IsNotExist(statErr) {
		t.Errorf("a stale (aged) marker must be removed, Stat err = %v", statErr)
	}
}

func TestEnsureGatewayBackend_CorruptMarkerTreatedAsStaleAndRemoved(t *testing.T) {
	// RED: decisions.md D3 (option A) — a JSON-unmarshal failure folds into "stale":
	// remove, distinctly-worded log, proceed. Today's stub does neither.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)
	writeGatewayReconcileMarker(t, root, "{not valid json")

	var relaunchCalls int
	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	ensureGatewayBackend(context.Background(), &cobra.Command{}, root)

	if relaunchCalls != 1 {
		t.Errorf("relaunch seam called %d times under a corrupt marker, want exactly 1 (treat-as-stale)", relaunchCalls)
	}
	if _, statErr := os.Stat(gatewayReconcilingMarkerPath(root)); !os.IsNotExist(statErr) {
		t.Errorf("a corrupt marker must be removed, Stat err = %v", statErr)
	}
}

func TestEnsureGatewayBackend_MarkerAbsentProceedsSilently(t *testing.T) {
	// RED: the common no-reconcile-in-progress case — no marker file at all is
	// neither live nor stale; proceeds with no removal attempt and no log line.
	root := gatewayBackendFixture(t)
	writeApiKeySecret(t, root)
	writeGatewayRelaunchScript(t, root, "")
	installFakeGatewayTmux(t, false)

	var relaunchCalls int
	oldRelaunch := gatewayRelaunchDo
	gatewayRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { gatewayRelaunchDo = oldRelaunch }()

	cmd := &cobra.Command{}
	var stdout strings.Builder
	cmd.SetOut(&stdout)
	ensureGatewayBackend(context.Background(), cmd, root)

	if relaunchCalls != 1 {
		t.Errorf("relaunch seam called %d times with no marker present, want exactly 1", relaunchCalls)
	}
	if strings.Contains(stdout.String(), "marker") {
		t.Errorf("no marker present must not log a removal line; got stdout=%q", stdout.String())
	}
}

// --- Pure infrastructure (PASSES now — real, decision-free logic per telemetry precedent) ---

func TestGatewayPidAlive_SelfProcess(t *testing.T) {
	if !gatewayPidAlive(os.Getpid()) {
		t.Error("gatewayPidAlive(os.Getpid()) = false, want true")
	}
}

func TestGatewayPidAlive_DeadPid(t *testing.T) {
	if gatewayPidAlive(99999999) {
		t.Error("gatewayPidAlive(99999999) = true, want false")
	}
}

func TestGatewayReconcileMarkerStale_LiveWithinWindow(t *testing.T) {
	m := gatewayReconcileMarker{PID: os.Getpid(), Since: time.Now().Unix()}
	if gatewayReconcileMarkerStale(m, time.Now()) {
		t.Error("a live pid with age 0 must not be stale")
	}
}

func TestGatewayReconcileMarkerStale_DeadPid(t *testing.T) {
	m := gatewayReconcileMarker{PID: 99999999, Since: time.Now().Unix()}
	if !gatewayReconcileMarkerStale(m, time.Now()) {
		t.Error("a dead pid must be stale regardless of age")
	}
}

func TestGatewayReconcileMarkerStale_AgeBoundary(t *testing.T) {
	now := time.Unix(2000000000, 0)
	live := gatewayReconcileMarker{PID: os.Getpid(), Since: now.Unix() - 900}
	if gatewayReconcileMarkerStale(live, now) {
		t.Error("age exactly 900s must still be live (intake: now_epoch - since <= 900 => live)")
	}
	stale := gatewayReconcileMarker{PID: os.Getpid(), Since: now.Unix() - 901}
	if !gatewayReconcileMarkerStale(stale, now) {
		t.Error("age 901s must be stale")
	}
}

func TestGatewayReconcileMarker_FieldNamesMatchWriterShape(t *testing.T) {
	// Guards against silently drifting from quickstart.sh's field names (pid/since) —
	// no shared Go type exists with the bash writer to enforce this at compile time.
	data, err := json.Marshal(gatewayReconcileMarker{PID: 123, Since: 456})
	if err != nil {
		t.Fatalf("marshal marker: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal marker: %v", err)
	}
	if _, ok := decoded["pid"]; !ok {
		t.Errorf("marker JSON missing %q field; got %s", "pid", data)
	}
	if _, ok := decoded["since"]; !ok {
		t.Errorf("marker JSON missing %q field; got %s", "since", data)
	}
}

func TestGatewayRelaunchDo_NoOpsUnderTestBinary(t *testing.T) {
	// PASSES now: mirrors telemetryRelaunchDo's isTestBinary() guard exactly.
	out, err := gatewayRelaunchDo(context.Background(), "/nonexistent/should-never-run.sh")
	if err != nil {
		t.Errorf("gatewayRelaunchDo under go test returned %v, want nil (isTestBinary no-op)", err)
	}
	if out != "" {
		t.Errorf("gatewayRelaunchDo under go test returned output %q, want empty", out)
	}
}

func TestRunGatewayRelaunchScript_TimeoutEnforced(t *testing.T) {
	// PASSES now: real infrastructure, no decision logic of its own — mirrors
	// TestRunRelaunchScript_TimeoutEnforced (telemetry_backend_test.go:168-192).
	dir := t.TempDir()
	script := filepath.Join(dir, "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write slow script: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := runGatewayRelaunchScript(ctx, script)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the killed process to return an error")
	}
	if elapsed > 3*time.Second {
		t.Errorf("runGatewayRelaunchScript took %s against a 500ms context deadline and a 30s sleep, want it killed promptly", elapsed)
	}
}

// --- AC2: watchdog wiring (protective + CHANGES-consumer tests for watchdog.go) ---

// stubGatewayBackendGuard substitutes ensureGatewayBackendFn with a call recorder,
// restoring the original on cleanup — mirrors stubTelemetryBackendGuard
// (watchdog_test.go:954).
func stubGatewayBackendGuard(t *testing.T, body func()) *int32 {
	t.Helper()
	var calls int32
	orig := ensureGatewayBackendFn
	ensureGatewayBackendFn = func(ctx context.Context, cmd *cobra.Command, root string) {
		atomic.AddInt32(&calls, 1)
		if body != nil {
			body()
		}
	}
	t.Cleanup(func() { ensureGatewayBackendFn = orig })
	return &calls
}

func TestWatchdog_GatewayGuardFiresBesidePollAgents(t *testing.T) {
	// RED: watchdogTick does not call any gateway trigger yet (Phase 6 wiring) — the
	// guard never fires, calls stays 0, want 1.
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"worker":{"type":"autonomous","description":"test worker"}}}`)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	defer func() { newWatchdogTmux = oldTmux }()
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error { return nil }
	defer func() { watchdogNudgeFn = oldNudge }()

	calls := stubGatewayBackendGuard(t, nil)
	stubIntegrationServicesGuard(t, nil) // D51: keep the tick's integration trigger off the real ensure
	scope := buildWatchdogScope([]string{"worker"}, "")

	watchdogTick(&cobra.Command{}, root, scope, map[string]*watchdogAgentState{}, map[string]int{}, 2)

	deadline := time.Now().Add(2 * time.Second)
	for atomic.LoadInt32(calls) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := atomic.LoadInt32(calls); got != 1 {
		t.Errorf("gateway backend guard fired %d times on one tick, want exactly 1 (Phase 6 wires triggerGatewayBackendGuard into watchdogTick)", got)
	}
}

func TestWatchdog_GatewayGuardDoesNotBlockPollAgents(t *testing.T) {
	// RED (vacuous pass): watchdogTick returns promptly today only because it never
	// calls the gateway guard at all — Phase 6 must keep this true for the right
	// reason once the guard is actually wired in and async. The deferred close(blocked)
	// is unconditional, so this cannot hang even before the guard is wired.
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"worker":{"type":"autonomous","description":"test worker"}}}`)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	defer func() { newWatchdogTmux = oldTmux }()
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error { return nil }
	defer func() { watchdogNudgeFn = oldNudge }()
	stubIntegrationServicesGuard(t, nil) // D51

	blocked := make(chan struct{})
	orig := ensureGatewayBackendFn
	ensureGatewayBackendFn = func(ctx context.Context, cmd *cobra.Command, root string) {
		<-blocked
	}
	defer func() {
		close(blocked)
		ensureGatewayBackendFn = orig
	}()

	scope := buildWatchdogScope([]string{"worker"}, "")
	start := time.Now()
	watchdogTick(&cobra.Command{}, root, scope, map[string]*watchdogAgentState{}, map[string]int{}, 2)
	if elapsed := time.Since(start); elapsed > 1*time.Second {
		t.Errorf("watchdogTick took %s with the gateway guard permanently blocked, want it to return promptly (pollAgents must not wait on it)", elapsed)
	}
}

func TestEnsureGatewayBackend_WatchdogNeverOverlapsInFlightAttempts(t *testing.T) {
	// Pins AC2's single-flight guarantee: triggerGatewayBackendGuard's CompareAndSwap
	// must ensure maxConcurrent never exceeds 1 across overlapping ticks.
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"worker":{"type":"autonomous","description":"test worker"}}}`)

	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	defer func() { newWatchdogTmux = oldTmux }()
	oldNudge := watchdogNudgeFn
	watchdogNudgeFn = func(sessionID string) error { return nil }
	defer func() { watchdogNudgeFn = oldNudge }()
	stubIntegrationServicesGuard(t, nil) // D51

	release := make(chan struct{})
	var entries, inFlight, maxConcurrent int32
	orig := ensureGatewayBackendFn
	ensureGatewayBackendFn = func(ctx context.Context, cmd *cobra.Command, root string) {
		atomic.AddInt32(&entries, 1)
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
		ensureGatewayBackendFn = orig
	}()

	scope := buildWatchdogScope([]string{"worker"}, "")
	agentStates := map[string]*watchdogAgentState{}
	failures := map[string]int{}
	for i := 0; i < 3; i++ {
		watchdogTick(&cobra.Command{}, root, scope, agentStates, failures, 2)
	}
	time.Sleep(100 * time.Millisecond)

	if atomic.LoadInt32(&entries) == 0 {
		t.Fatal("gateway backend guard never entered across 3 ticks, want at least 1")
	}
	if got := atomic.LoadInt32(&maxConcurrent); got > 1 {
		t.Errorf("max concurrent gateway-backend-guard attempts = %d, want at most 1", got)
	}
}

// TestWatchdog_TickCallersStubIntegrationServicesGuard (D51, spec L862): watchdogTick fires the
// integration service ensure on its own goroutine, so a tick test that leaves
// ensureIntegrationServicesFn real launches a straggler that outlives the test against its
// temp root. Source hygiene scan: every top-level test function in this package that calls
// watchdogTick( must stub the ensure (stubIntegrationServicesGuard or a direct
// ensureIntegrationServicesFn swap).
func TestWatchdog_TickCallersStubIntegrationServicesGuard(t *testing.T) {
	files, err := filepath.Glob("*_test.go")
	if err != nil {
		t.Fatal(err)
	}
	call := regexp.MustCompile(`(?m)^\s*watchdogTick\(`)
	var missing []string
	scanned := 0
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, fn := range strings.Split(string(src), "\nfunc ")[1:] {
			if !strings.HasPrefix(fn, "Test") || !call.MatchString(fn) {
				continue
			}
			scanned++
			if !strings.Contains(fn, "stubIntegrationServicesGuard(") && !strings.Contains(fn, "ensureIntegrationServicesFn =") {
				name, _, _ := strings.Cut(fn, "(")
				missing = append(missing, f+":"+name)
			}
		}
	}
	if scanned == 0 {
		t.Fatalf("found no test function calling watchdogTick( — the scan is broken")
	}
	sort.Strings(missing)
	if len(missing) != 0 {
		t.Errorf("these watchdogTick tests leave the integration service trigger real (D51); add stubIntegrationServicesGuard(t, nil): %v", missing)
	}
}
