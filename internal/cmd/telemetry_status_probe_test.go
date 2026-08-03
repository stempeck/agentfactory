package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// TestStatusReportsProbeVerdicts closes an enforcement hole: the reachability wiring in
// printTelemetryStatus could be deleted with the whole suite staying green.
//
// The existing status test uses a fixture whose secret file does not exist, so status takes the
// "could not read the credential" early return and never reaches the probe at all. Every assertion
// in that test is satisfied without the probe existing — which makes it a test of the error path
// wearing the costume of a test of the feature.
//
// This test supplies a REAL secret file so resolution succeeds, stubs telemetry.Probe so no socket
// is needed, and asserts the verdicts actually reach the operator's terminal. Delete the probe loop
// from printTelemetryStatus and this fails.
func TestStatusReportsProbeVerdicts(t *testing.T) {
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("seed gate: %v", err)
	}

	secretsDir := filepath.Join(root, ".agentfactory", "secrets")
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "telemetry.auth"), []byte("Basic dGVzdA=="), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
	writeTelemetryJSON(t, root, `{
  "endpoint": "http://127.0.0.1:5080/api/default",
  "otlp_http_path_traces": "/v1/traces",
  "headers": {"Authorization": "file:.agentfactory/secrets/telemetry.auth"},
  "protocol": "http/json",
  "export_timeout_ms": 500
}`)

	probed := false
	orig := telemetry.Probe
	telemetry.Probe = func(cfg config.TelemetryConfig) []telemetry.ProbeResult {
		probed = true
		// The dereferenced credential must reach the probe: probing with an unresolved reference
		// would report an authentication failure for a configuration problem.
		if got := cfg.Headers["Authorization"]; strings.HasPrefix(got, "file:") {
			t.Errorf("status handed the probe an unresolved secret reference: %q", got)
		}
		return []telemetry.ProbeResult{
			{Label: "step timings", URL: "http://127.0.0.1:5080/api/default/v1/traces", Status: 200},
			{Label: "token usage", URL: "http://127.0.0.1:5080/api/default/v1/logs", Status: 404},
		}
	}
	t.Cleanup(func() { telemetry.Probe = orig })

	out := captureStdout(t, func() {
		if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
			t.Fatalf("runTelemetry status: %v", err)
		}
	})

	if !probed {
		t.Fatal("status never called telemetry.Probe: the reachability layer is not wired in, so " +
			"an operator still cannot tell 'no data yet' from 'no data can ever arrive'")
	}
	if !strings.Contains(out, "step timings") || !strings.Contains(out, "reachable") {
		t.Errorf("stdout = %q, want the reachable verdict for the step-timing address", out)
	}
	// The 404 verdict is the one that matters most: it is the shape of the defect where the backend
	// is up and authenticating and the data still goes nowhere.
	if !strings.Contains(out, "token usage") || !strings.Contains(out, "not served") {
		t.Errorf("stdout = %q, want the token-usage address reported as not served", out)
	}
	if strings.Contains(out, "Basic dGVzdA==") {
		t.Error("status printed a header VALUE")
	}
}
