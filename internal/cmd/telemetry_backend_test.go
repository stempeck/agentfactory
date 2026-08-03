package cmd

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// This file pins ensureTelemetryBackend (fable-implement Step 1, Root Cause A: the
// telemetry backend's liveness has no autonomous owner). fable-implement Phase 5
// (RED): ensureTelemetryBackend currently only implements its two cheap gate checks
// (telemetry_backend.go) — the healthz probe and relaunch-script invocation are
// Phase 6 work. Tests below are annotated with their current (RED) expectation.

// telemetryBackendFixture is newUsageFixture's shape reused for this file: an
// endpoint-configured factory root, without the credential dereference concerns the
// usage-query tests carry (ensureTelemetryBackend never sends the header-bearing
// query path).
func telemetryBackendFixture(t *testing.T, endpoint string) string {
	t.Helper()
	fx := newUsageFixture(t, endpoint)
	return fx.root
}

func TestEnsureTelemetryBackend_GateOffNoOp(t *testing.T) {
	root := telemetryBackendFixture(t, "http://127.0.0.1:1/api/default") // gate off: never dialed
	var healthzCalls, relaunchCalls int
	oldHealthz, oldRelaunch := telemetryHealthzDo, telemetryRelaunchDo
	telemetryHealthzDo = func(req *http.Request) (*http.Response, error) {
		healthzCalls++
		return nil, context.DeadlineExceeded
	}
	telemetryRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { telemetryHealthzDo, telemetryRelaunchDo = oldHealthz, oldRelaunch }()

	ensureTelemetryBackend(context.Background(), &cobra.Command{}, root)

	if healthzCalls != 0 {
		t.Errorf("healthz seam called %d times with gate off, want 0", healthzCalls)
	}
	if relaunchCalls != 0 {
		t.Errorf("relaunch seam called %d times with gate off, want 0", relaunchCalls)
	}
}

func TestEnsureTelemetryBackend_HealthyBackendNoOp(t *testing.T) {
	// RED (vacuous pass): the gate/loopback checks pass, then the stub returns
	// before ever probing healthz — so this passes for the WRONG reason today (it
	// never checks health at all, rather than correctly detecting a healthy
	// backend). Phase 6 must make this pass for the RIGHT reason.
	endpoint, _ := fakeBackend(t, okBackend)
	root := telemetryBackendFixture(t, endpoint)
	gateOn(t, root)

	var relaunchCalls int
	oldRelaunch := telemetryRelaunchDo
	telemetryRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "", nil
	}
	defer func() { telemetryRelaunchDo = oldRelaunch }()

	ensureTelemetryBackend(context.Background(), &cobra.Command{}, root)

	if relaunchCalls != 0 {
		t.Errorf("relaunch seam called %d times against a healthy backend, want 0", relaunchCalls)
	}
}

func TestEnsureTelemetryBackend_DownBackendTriggersRelaunchOnce(t *testing.T) {
	// RED: expected to fail until Phase 6 implements the probe-then-relaunch
	// decision — the stub never reaches either seam, so relaunchCalls stays 0.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close() // refused, not merely a 4xx — matches the "backend down" shape
	root := telemetryBackendFixture(t, srv.URL+"/api/default")
	gateOn(t, root)
	writeRelaunchScript(t, root)

	var relaunchCalls int
	oldRelaunch := telemetryRelaunchDo
	telemetryRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		relaunchCalls++
		return "telemetry backend: relaunch attempted (tmux session 'telemetry'; cold start may take up to 90s)", nil
	}
	defer func() { telemetryRelaunchDo = oldRelaunch }()

	ensureTelemetryBackend(context.Background(), &cobra.Command{}, root)

	if relaunchCalls != 1 {
		t.Errorf("relaunch seam called %d times against a down backend, want exactly 1", relaunchCalls)
	}
}

func TestEnsureTelemetryBackend_RelaunchScriptFailureWarnOnly(t *testing.T) {
	// RED: the stub never reaches the relaunch seam, so no warning is ever printed —
	// this fails until Phase 6 wires the down-backend path to call it and warn on error.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	root := telemetryBackendFixture(t, srv.URL+"/api/default")
	gateOn(t, root)
	writeRelaunchScript(t, root)

	oldRelaunch := telemetryRelaunchDo
	telemetryRelaunchDo = func(ctx context.Context, scriptPath string) (string, error) {
		return "", context.DeadlineExceeded
	}
	defer func() { telemetryRelaunchDo = oldRelaunch }()

	cmd := &cobra.Command{}
	var stderr strings.Builder
	cmd.SetErr(&stderr)
	ensureTelemetryBackend(context.Background(), cmd, root)

	if !strings.Contains(stderr.String(), "warning:") {
		t.Errorf("relaunch script failure printed no warning; got stderr=%q", stderr.String())
	}
}

func TestEnsureTelemetryBackend_ProbeBounded2s(t *testing.T) {
	// RED (vacuous pass): the stub returns immediately without probing at all, so
	// this is trivially fast today — not because the real bounded-probe logic
	// exists. Phase 6 must keep it true for the right reason.
	// Cleanup order matters and is easy to get backward: t.Cleanup runs LIFO, and
	// httptest.Server.Close() blocks until every in-flight handler returns. Closing
	// the server before unblocking the handler deadlocks cleanup itself (Close waits
	// on the handler; the handler waits on hang). Registering Close FIRST makes it
	// run LAST, after hang is already closed and the handler has exited.
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-hang
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(hang) })
	root := telemetryBackendFixture(t, srv.URL+"/api/default")
	gateOn(t, root)
	writeRelaunchScript(t, root)

	start := time.Now()
	ensureTelemetryBackend(context.Background(), &cobra.Command{}, root)
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("ensureTelemetryBackend took %s against a hung healthz endpoint, want bounded ~2s", elapsed)
	}
}

func TestTelemetryHealthzURL_StripsOrgSegment(t *testing.T) {
	// RED: telemetryHealthzURL is deliberately stubbed to the wrong (naive-append)
	// behavior; this fails until Phase 6 fixes the derivation
	// (concern_blast.md §1.2).
	got := telemetryHealthzURL("http://127.0.0.1:5080/api/default")
	want := "http://127.0.0.1:5080/healthz"
	if got != want {
		t.Errorf("telemetryHealthzURL(%q) = %q, want %q — the org segment must be stripped, "+
			"not appended onto (a naive append 404s against a perfectly healthy backend)",
			"http://127.0.0.1:5080/api/default", got, want)
	}
}

func TestRunRelaunchScript_TimeoutEnforced(t *testing.T) {
	// PASSES now: runRelaunchScript is real infrastructure (a thin exec.CommandContext
	// wrapper with no decision logic of its own) — the timeout guarantee here comes
	// from the stdlib's context-cancellation-kills-the-process behavior, not from any
	// fable-implement-authored logic, so there is nothing to leave stubbed.
	dir := t.TempDir()
	script := filepath.Join(dir, "slow.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\nsleep 30\n"), 0o755); err != nil {
		t.Fatalf("write slow script: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := runRelaunchScript(ctx, script)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected the killed process to return an error")
	}
	if elapsed > 3*time.Second {
		t.Errorf("runRelaunchScript took %s against a 500ms context deadline and a 30s sleep, want it killed promptly", elapsed)
	}
}

func TestTelemetryRelaunchDo_NoOpsUnderTestBinary(t *testing.T) {
	// PASSES now: mirrors reapImprovementSession's isTestBinary() guard exactly —
	// under `go test`, telemetryRelaunchDo must never actually shell out, so a test
	// exercising the real seam (not a package-var override) is safe to run without
	// spawning a real subprocess.
	out, err := telemetryRelaunchDo(context.Background(), "/nonexistent/should-never-run.sh")
	if err != nil {
		t.Errorf("telemetryRelaunchDo under go test returned %v, want nil (isTestBinary no-op)", err)
	}
	if out != "" {
		t.Errorf("telemetryRelaunchDo under go test returned output %q, want empty", out)
	}
}

// writeRelaunchScript seeds .agentfactory/telemetry/relaunch.sh so
// ensureTelemetryBackend's "script present" check (once Phase 6 adds it) does not
// itself become the reason a down-backend test no-ops.
func writeRelaunchScript(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".agentfactory", "telemetry")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir telemetry dir: %v", err)
	}
	script := filepath.Join(dir, "relaunch.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatalf("write relaunch.sh: %v", err)
	}
}
