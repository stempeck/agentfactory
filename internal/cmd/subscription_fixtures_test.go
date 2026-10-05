package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func subscriptionModels() *config.ModelsConfig {
	return &config.ModelsConfig{
		Models: map[string]map[string]string{
			gatewayAuthProfileName: {
				"ANTHROPIC_MODEL":      "gpt-5.6-sol",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex-subscription.key",
			},
		},
	}
}

func writeSubscriptionHandle(t *testing.T, root string, expiresAt int64, refreshToken, accessToken string) {
	t.Helper()
	p := gatewayAuthHandlePath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir subscription handle dir: %v", err)
	}
	body := map[string]any{
		"access_token":  accessToken,
		"refresh_token": refreshToken,
		"id_token":      "",
		"expires_at":    expiresAt,
		"account_id":    "acct-fixture",
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal subscription handle fixture: %v", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write subscription handle fixture: %v", err)
	}
}

func writeSubscriptionState(t *testing.T, root string, st gatewayAuthState) {
	t.Helper()
	if err := writeGatewayAuthState(root, st); err != nil {
		t.Fatalf("writeGatewayAuthState fixture: %v", err)
	}
}
