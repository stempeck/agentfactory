package config

import (
	"os"
	"path/filepath"
	"testing"
)

// TestUsingDocTelemetryExampleValidates keeps the telemetry.json example an operator copies
// out of USING_AGENTFACTORY.md honest: it is loaded through the same LoadTelemetryConfig the
// factory itself uses, so a documented block that drifts from what quickstart seeds — or that
// was never valid — fails here rather than in the operator's terminal.
func TestUsingDocTelemetryExampleValidates(t *testing.T) {
	docPath := filepath.Join("..", "..", "USING_AGENTFACTORY.md")
	data, err := os.ReadFile(docPath)
	if err != nil {
		t.Fatalf("read %s: %v", docPath, err)
	}

	block, ok := extractJSONBlockContaining(string(data), `"otlp_http_path_traces"`)
	if !ok {
		t.Fatalf("USING_AGENTFACTORY.md has no ```json telemetry example containing \"otlp_http_path_traces\" (issue #329 P6a: the Telemetry section must show the telemetry.json that quickstart.sh seeds)")
	}

	cfg, err := LoadTelemetryConfig(writeTelemetryRoot(t, block))
	if err != nil {
		t.Fatalf("the documented telemetry.json example does not load/validate: %v\n--- documented block ---\n%s", err, block)
	}

	// An absent file loads as an empty config with a nil error, so err == nil on its own is
	// also true of a block that parsed to nothing. These assertions are what make the test bite.
	if cfg.Endpoint == "" {
		t.Errorf("the documented telemetry.json example parsed but carries no endpoint\n--- documented block ---\n%s", block)
	}
	if cfg.OTLPHTTPPathTraces == "" {
		t.Errorf("the documented telemetry.json example parsed but carries no traces path\n--- documented block ---\n%s", block)
	}
	if len(cfg.Headers) != 1 {
		t.Errorf("the documented telemetry.json example configures %d headers, want the 1 that quickstart.sh seeds", len(cfg.Headers))
	}
}
