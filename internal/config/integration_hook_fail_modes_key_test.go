package config

import (
	"strings"
	"testing"
)

// AF_INTEGRATION_HOOK_FAIL_MODES is the one launch key af derives for integrations (U15); neither an
// integration's [env] nor a models.json profile may set it, or a manifest could rewrite every integration's
// hook fail mode.
func TestIntegrationEnv_RefusesReservedHookFailModesKey(t *testing.T) {
	const key = "AF_INTEGRATION_HOOK_FAIL_MODES"
	if err := validateIntegrationEnv("acme-int", map[string]string{key: "acme-int=closed"}); err == nil || !strings.Contains(err.Error(), key) {
		t.Errorf("integration [env] setting %s: err = %v, want a refusal naming the key", key, err)
	}
	if err := validateModelProfile("hi", map[string]string{"ANTHROPIC_MODEL": "claude-opus-5-5", key: "x"}); err == nil || !strings.Contains(err.Error(), key) {
		t.Errorf("models.json profile setting %s: err = %v, want a refusal naming the key", key, err)
	}
	if err := validateIntegrationEnv("acme-int", map[string]string{"ACME_INT_TOKEN": "t"}); err != nil {
		t.Errorf("an ordinary [env] key must stay accepted: %v", err)
	}
}
