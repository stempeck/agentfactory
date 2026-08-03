package config

import (
	"strings"
	"testing"
)

// TestModelProfileRejectsTelemetryEnv proves the one-writer guarantee (issue #329 K5, review
// L-1): a models.json profile that names any of the seven telemetry env keys is rejected — on
// read (validateModelsConfig, run by LoadModelsConfig) AND on write (SaveModelsConfig) — so a
// profile can never inject OTel env and race session.Manager, the single writer of that family.
func TestModelProfileRejectsTelemetryEnv(t *testing.T) {
	telemetryKeys := []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY",
		"OTEL_METRICS_EXPORTER",
		"OTEL_LOGS_EXPORTER",
		"OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_ENDPOINT",
		"OTEL_EXPORTER_OTLP_HEADERS",
		"OTEL_RESOURCE_ATTRIBUTES",
	}
	// The denylist must be exactly the seven-var family — no more (an extra key would reject a
	// legitimate profile export), no fewer (a gap reopens the injection conduit).
	if len(afTelemetryKeys) != len(telemetryKeys) {
		t.Fatalf("afTelemetryKeys has %d entries, want %d (the telemetry family)", len(afTelemetryKeys), len(telemetryKeys))
	}

	for _, key := range telemetryKeys {
		t.Run(key, func(t *testing.T) {
			cfg := &ModelsConfig{Models: map[string]map[string]string{
				"p": {key: "x"},
			}}

			// Rejected on read.
			err := validateModelsConfig(cfg)
			if err == nil {
				t.Fatalf("a profile naming telemetry var %q must be rejected", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("the rejection must name the offending telemetry var %q; got %v", key, err)
			}

			// Rejected on write too (SaveModelsConfig validates before writing).
			dir := t.TempDir()
			if saveErr := SaveModelsConfig(ModelsConfigPath(dir), cfg); saveErr == nil {
				t.Errorf("SaveModelsConfig must reject a profile naming telemetry var %q", key)
			}
		})
	}
}
