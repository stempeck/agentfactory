package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// This file pins Phase 1 (K1) of issue #693: gatewayAuthMode(root string) (mode string, migrated
// bool, err error), a new, distinct resolver in gateway_auth.go (decisions.md D1) — it does not
// exist yet, so every call below fails to compile until Phase 6 lands it. That is the correct RED
// state for this file, recorded in red_predictions.md.
//
// Fixture paths mirror design-doc.md:115's Home column exactly:
//   record:   .agentfactory/litellm-auth-mode
//   tier 1:   .agentfactory/gateway-relaunch.sh (LITELLM_AUTH_MODE= baked line)
//   tier 2:   .agentfactory/litellm.yaml (openai/ vs chatgpt/ lane prefix)
//   tier 3/4: secrets/openai.key, secrets/chatgpt/auth.json (gatewayHandleNonEmpty /
//             apiKeySecretPresent's own paths)

func authModeRecordPath(root string) string {
	return filepath.Join(config.ConfigDir(root), "litellm-auth-mode")
}

func writeK1ApiKeyHandle(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(config.ConfigDir(root), "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "openai.key"), []byte("sk-test-key"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeK1SubscriptionHandle(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(config.ConfigDir(root), "secrets", "chatgpt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(`{"access_token":"t"}`), 0o600); err != nil {
		t.Fatal(err)
	}
}

// --- record present ------------------------------------------------------------------------

func TestGatewayAuthMode_RecordPresentApiKey(t *testing.T) {
	root := gatewayFactoryRoot(t)
	if err := os.WriteFile(authModeRecordPath(root), []byte("api-key\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mode, migrated, err := gatewayAuthMode(root)
	if err != nil {
		t.Fatalf("gatewayAuthMode: %v", err)
	}
	if mode != "api-key" {
		t.Errorf("mode = %q, want api-key", mode)
	}
	if migrated {
		t.Error("migrated = true for a present record; record reads must never report migration")
	}
}

func TestGatewayAuthMode_RecordPresentCodexSubscription(t *testing.T) {
	root := gatewayFactoryRoot(t)
	if err := os.WriteFile(authModeRecordPath(root), []byte("codex-subscription\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mode, migrated, err := gatewayAuthMode(root)
	if err != nil {
		t.Fatalf("gatewayAuthMode: %v", err)
	}
	if mode != "codex-subscription" {
		t.Errorf("mode = %q, want codex-subscription", mode)
	}
	if migrated {
		t.Error("migrated = true for a present record")
	}
}

// --- invalid content (design-doc.md:115: "any other content ⇒ error naming the file") -------

func TestGatewayAuthMode_RecordInvalidContentErrorsNamingFile(t *testing.T) {
	root := gatewayFactoryRoot(t)
	recPath := authModeRecordPath(root)
	if err := os.WriteFile(recPath, []byte("garbage-not-a-mode\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := gatewayAuthMode(root)
	if err == nil {
		t.Fatal("expected an error for invalid record content, got nil")
	}
	if !strings.Contains(err.Error(), recPath) {
		t.Errorf("error %q does not name the record file %q (decisions.md D4)", err, recPath)
	}
}

// decisions.md D4: a present-but-zero-byte record file is invalid content, not "absent" — it must
// hard-error naming the file, never silently re-migrate.
func TestGatewayAuthMode_ZeroByteRecordFileIsInvalidNotAbsent(t *testing.T) {
	root := gatewayFactoryRoot(t)
	recPath := authModeRecordPath(root)
	if err := os.WriteFile(recPath, []byte{}, 0o644); err != nil {
		t.Fatal(err)
	}
	// Also seed handle presence so a wrongly-"absent" implementation would migrate cleanly instead
	// of erroring — this makes the zero-byte-treated-as-absent bug observable.
	writeK1ApiKeyHandle(t, root)

	_, migrated, err := gatewayAuthMode(root)
	if err == nil {
		t.Fatalf("expected a hard error for a zero-byte record file (decisions.md D4); got migrated=%v, no error", migrated)
	}
	if !strings.Contains(err.Error(), recPath) {
		t.Errorf("error %q does not name the record file %q", err, recPath)
	}
}

// --- absent record: 5-tier migration, in priority order --------------------------------------

// Tier 1: an existing gateway-relaunch.sh's baked LITELLM_AUTH_MODE= line outranks every other
// signal (design-doc.md:115, tier 1).
func TestGatewayAuthMode_Tier1RelaunchScriptEnvLine(t *testing.T) {
	root := gatewayFactoryRoot(t)
	relaunch := filepath.Join(config.ConfigDir(root), "gateway-relaunch.sh")
	script := "#!/usr/bin/env bash\nLITELLM_AUTH_MODE=\"codex-subscription\"\necho hi\n"
	if err := os.WriteFile(relaunch, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	// A contradicting handle must NOT outrank tier 1 — the relaunch script is the strongest signal.
	writeK1ApiKeyHandle(t, root)

	mode, migrated, err := gatewayAuthMode(root)
	if err != nil {
		t.Fatalf("gatewayAuthMode: %v", err)
	}
	if mode != "codex-subscription" {
		t.Errorf("mode = %q, want codex-subscription (tier 1: gateway-relaunch.sh)", mode)
	}
	if !migrated {
		t.Error("migrated = false; an absent-record migration must report migrated=true")
	}
}

// Tier 2: litellm.yaml's model_list lane prefix, when no relaunch script exists yet.
func TestGatewayAuthMode_Tier2LitellmYamlLanePrefix(t *testing.T) {
	root := gatewayFactoryRoot(t)
	yaml := filepath.Join(config.ConfigDir(root), "litellm.yaml")
	seed := "model_list:\n  - model_name: claude-haiku\n    litellm_params:\n      model: chatgpt/gpt-5\n"
	if err := os.WriteFile(yaml, []byte(seed), 0o644); err != nil {
		t.Fatal(err)
	}
	mode, migrated, err := gatewayAuthMode(root)
	if err != nil {
		t.Fatalf("gatewayAuthMode: %v", err)
	}
	if mode != "codex-subscription" {
		t.Errorf("mode = %q, want codex-subscription (tier 2: litellm.yaml chatgpt/ lane)", mode)
	}
	if !migrated {
		t.Error("migrated = false; an absent-record migration must report migrated=true")
	}
}

// Tier 3: exactly one non-empty handle, when no relaunch script or litellm.yaml lane signal exists.
func TestGatewayAuthMode_Tier3SingleHandleApiKey(t *testing.T) {
	root := gatewayFactoryRoot(t)
	writeK1ApiKeyHandle(t, root)
	mode, migrated, err := gatewayAuthMode(root)
	if err != nil {
		t.Fatalf("gatewayAuthMode: %v", err)
	}
	if mode != "api-key" {
		t.Errorf("mode = %q, want api-key (tier 3: single handle)", mode)
	}
	if !migrated {
		t.Error("migrated = false; an absent-record migration must report migrated=true")
	}
}

func TestGatewayAuthMode_Tier3SingleHandleCodexSubscription(t *testing.T) {
	root := gatewayFactoryRoot(t)
	writeK1SubscriptionHandle(t, root)
	mode, migrated, err := gatewayAuthMode(root)
	if err != nil {
		t.Fatalf("gatewayAuthMode: %v", err)
	}
	if mode != "codex-subscription" {
		t.Errorf("mode = %q, want codex-subscription (tier 3: single handle)", mode)
	}
	if !migrated {
		t.Error("migrated = false; an absent-record migration must report migrated=true")
	}
}

// Tier 4: both handles present and no stronger signal ⇒ the single surviving refusal, which must
// name --litellm-auth (intake.md AC verbatim; design-doc.md T-14 verification note).
func TestGatewayAuthMode_Tier4BothHandlesNoRecordRefusesNamingFlag(t *testing.T) {
	root := gatewayFactoryRoot(t)
	writeK1ApiKeyHandle(t, root)
	writeK1SubscriptionHandle(t, root)

	_, _, err := gatewayAuthMode(root)
	if err == nil {
		t.Fatal("expected a refusal when both handles are present with no record and no other signal")
	}
	if !strings.Contains(err.Error(), "--litellm-auth") {
		t.Errorf("refusal %q does not name --litellm-auth (intake.md AC)", err)
	}
}

// Tier 5: neither handle, no relaunch script, no litellm.yaml ⇒ default api-key.
func TestGatewayAuthMode_Tier5DefaultApiKeyWhenNeither(t *testing.T) {
	root := gatewayFactoryRoot(t)
	mode, migrated, err := gatewayAuthMode(root)
	if err != nil {
		t.Fatalf("gatewayAuthMode: %v", err)
	}
	if mode != "api-key" {
		t.Errorf("mode = %q, want api-key (tier 5: default)", mode)
	}
	if !migrated {
		t.Error("migrated = false; an absent-record migration must report migrated=true")
	}
}
