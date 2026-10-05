package config

import (
	"strings"
	"testing"
)

// TestLoadRejectsUpstreamAuthKeys pins V6: a models.json profile that names any of the five
// gateway upstream-auth env keys is rejected — on read (validateModelsConfig, run by
// LoadModelsConfig) AND on write (SaveModelsConfig) — exactly as afIdentityKeys/afTelemetryKeys
// already are, so a profile can never carry upstream-auth material into a launch line. Modeled
// byte-for-byte on TestModelProfileRejectsTelemetryEnv (models_telemetry_test.go).
func TestLoadRejectsUpstreamAuthKeys(t *testing.T) {
	gatewayKeys := []string{
		"OPENAI_API_KEY",
		"CHATGPT_TOKEN_DIR",
		"CHATGPT_AUTH_FILE",
		"CHATGPT_API_BASE",
		"CODEX_HOME",
	}
	// The denylist must be exactly this five-var family — no more (an extra key would reject a
	// legitimate profile export), no fewer (a gap reopens the injection conduit this V6 clause
	// exists to close).
	if len(afGatewayUpstreamKeys) != len(gatewayKeys) {
		t.Fatalf("afGatewayUpstreamKeys has %d entries, want %d (the gateway upstream-auth family)", len(afGatewayUpstreamKeys), len(gatewayKeys))
	}

	for _, key := range gatewayKeys {
		t.Run(key, func(t *testing.T) {
			cfg := &ModelsConfig{Models: map[string]map[string]string{
				"p": {key: "x"},
			}}

			// Rejected on read.
			err := validateModelsConfig(cfg)
			if err == nil {
				t.Fatalf("a profile naming gateway upstream-auth var %q must be rejected", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("the rejection must name the offending var %q; got %v", key, err)
			}

			// Rejected on write too (SaveModelsConfig validates before writing).
			dir := t.TempDir()
			if saveErr := SaveModelsConfig(ModelsConfigPath(dir), cfg); saveErr == nil {
				t.Errorf("SaveModelsConfig must reject a profile naming gateway upstream-auth var %q", key)
			}
		})
	}
}

// TestLoadRejectsUpstreamAuthKeys_ValueIndependent pins the load-bearing clause shape: the
// reject must NOT be gated on a non-empty value (unlike the envAPIKey clause at models.go, which
// IS gated on val != ""). A profile setting one of the five names to "" must still be rejected —
// otherwise "" would be a way to smuggle the reserved name past validation for a later write.
func TestLoadRejectsUpstreamAuthKeys_ValueIndependent(t *testing.T) {
	gatewayKeys := []string{
		"OPENAI_API_KEY",
		"CHATGPT_TOKEN_DIR",
		"CHATGPT_AUTH_FILE",
		"CHATGPT_API_BASE",
		"CODEX_HOME",
	}
	for _, key := range gatewayKeys {
		t.Run(key, func(t *testing.T) {
			cfg := &ModelsConfig{Models: map[string]map[string]string{
				"p": {key: ""},
			}}
			err := validateModelsConfig(cfg)
			if err == nil {
				t.Fatalf("a profile naming gateway upstream-auth var %q with an empty value must still be rejected (value-independent clause)", key)
			}
			if !strings.Contains(err.Error(), key) {
				t.Errorf("the rejection must name the offending var %q; got %v", key, err)
			}
		})
	}
}
