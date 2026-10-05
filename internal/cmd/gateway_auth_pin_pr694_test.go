package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// PR #694 pinning tests (RED pins): T14 Go arm (codexSessionValid must reject an api-key
// auth.json), T19 (device_code_requested_at must decode a float epoch).

// TestCodexSessionValidRejectsApiKeyFile (T14, Go arm). The bash predicate keys on exit code +
// auth_mode + refresh-token; the Go twin keys ONLY on a present refresh token. An api-key auth.json
// (auth_mode "apikey") that happens to carry a stale refresh token must read INVALID, matching the
// bash side. RED at head: codexSessionValid ignores auth_mode and returns true for any file with a
// refresh token.
func TestCodexSessionValidRejectsApiKeyFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	authJSON := `{"auth_mode":"apikey","OPENAI_API_KEY":"sk-fixture","tokens":{"refresh_token":"stale-refresh-token"}}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(authJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if codexSessionValid() {
		t.Error("codexSessionValid must reject an auth_mode=\"apikey\" file even when a stale refresh token is present; the Go twin must key on auth_mode like the bash predicate (install.go:1160-1178, T14)")
	}
}

// TestCodexSessionValidAcceptsChatGPTFile is the protective companion to T14: a genuine ChatGPT
// login (auth_mode "chatgpt" + refresh token) must still read valid after the predicate is
// tightened. PASS now and after the fix.
func TestCodexSessionValidAcceptsChatGPTFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	authJSON := `{"auth_mode":"chatgpt","tokens":{"refresh_token":"live-refresh-token"}}`
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(authJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if !codexSessionValid() {
		t.Error("codexSessionValid must accept a ChatGPT auth.json with a refresh token")
	}
}

// TestGatewayAuthStatusJSONDecodesFloatDeviceCodeRequestedAt (T19). LiteLLM writes
// device_code_requested_at with time.time() — a JSON float. status --json must surface it as a
// populated RFC3339 timestamp. RED at head: gateway_auth.go:636 unmarshals into int64, which
// rejects a fractional literal, so the field is silently dropped.
func TestGatewayAuthStatusJSONDecodesFloatDeviceCodeRequestedAt(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")

	p := gatewayAuthHandlePath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	body := map[string]any{
		"access_token":             "PLANTED-JSON-ACCESS-MARKER",
		"refresh_token":            "PLANTED-JSON-REFRESH-MARKER",
		"expires_at":               time.Now().Add(24 * time.Hour).Unix(),
		"device_code_requested_at": 1758500000.123, // LiteLLM time.time() — a float epoch
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	writeSubscriptionState(t, root, healthySubscriptionState())
	if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("codex-subscription\n"), 0o644); err != nil {
		t.Fatalf("write gatewayAuthMode record: %v", err)
	}

	if err := gatewayAuthStatusCmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gatewayAuthStatusCmd.Flags().Set("json", "false"); err != nil {
			t.Fatal(err)
		}
	})
	var out strings.Builder
	gatewayAuthStatusCmd.SetOut(&out)
	if err := runGatewayAuthStatus(gatewayAuthStatusCmd, nil); err != nil {
		t.Fatalf("runGatewayAuthStatus: %v", err)
	}

	var rec struct {
		DeviceCodeRequested   bool   `json:"device_code_requested"`
		DeviceCodeRequestedAt string `json:"device_code_requested_at"`
	}
	if err := json.Unmarshal([]byte(out.String()), &rec); err != nil {
		t.Fatalf("unmarshal status json: %v; out=%q", err, out.String())
	}
	if !rec.DeviceCodeRequested {
		t.Errorf("device_code_requested must be true when the handle carries a float device_code_requested_at; out=%q", out.String())
	}
	if rec.DeviceCodeRequestedAt == "" {
		t.Errorf("device_code_requested_at must decode a float epoch to a populated RFC3339 timestamp; got empty (int64 unmarshal drops the fractional value) out=%q", out.String())
	}
}

// TestGatewayAuthStatusJSONSurfacesLaunchIdentity (T17). When .runtime/gateway/launch.json exists,
// status --json must surface the recorded launch identity (mode, config sha, litellm version, pane
// pid) so an operator can see what the running gateway was actually launched with — and must leak no
// token/PII. RED at head: gatewayAuthStatusJSON carries no launch-identity fields and nothing reads
// .runtime/gateway/launch.json (greenfield reader; this test defines the contract).
func TestGatewayAuthStatusJSONSurfacesLaunchIdentity(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")

	launchDir := filepath.Join(root, ".runtime", "gateway")
	if err := os.MkdirAll(launchDir, 0o755); err != nil {
		t.Fatal(err)
	}
	launch := map[string]any{
		"mode":            "codex-subscription",
		"config_file":     ".agentfactory/litellm.codex-subscription.yaml",
		"config_sha256":   "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
		"credential_ref":  ".agentfactory/secrets/chatgpt/auth.json",
		"litellm_version": "1.93.0",
		"pane_pid":        4242,
		"launched_at":     time.Now().UTC().Format(time.RFC3339),
	}
	data, err := json.Marshal(launch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(launchDir, "launch.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	writeSubscriptionState(t, root, healthySubscriptionState())
	if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("codex-subscription\n"), 0o644); err != nil {
		t.Fatalf("write gatewayAuthMode record: %v", err)
	}

	if err := gatewayAuthStatusCmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := gatewayAuthStatusCmd.Flags().Set("json", "false"); err != nil {
			t.Fatal(err)
		}
	})
	var out strings.Builder
	gatewayAuthStatusCmd.SetOut(&out)
	if err := runGatewayAuthStatus(gatewayAuthStatusCmd, nil); err != nil {
		t.Fatalf("runGatewayAuthStatus: %v", err)
	}

	var rec struct {
		Launch *struct {
			Mode           string `json:"mode"`
			ConfigSHA256   string `json:"config_sha256"`
			LitellmVersion string `json:"litellm_version"`
			PanePID        int    `json:"pane_pid"`
		} `json:"launch"`
	}
	if err := json.Unmarshal([]byte(out.String()), &rec); err != nil {
		t.Fatalf("unmarshal status json: %v; out=%q", err, out.String())
	}
	if rec.Launch == nil {
		t.Fatalf("status --json must surface a launch-identity object when .runtime/gateway/launch.json exists; out=%q", out.String())
	}
	if rec.Launch.Mode != "codex-subscription" {
		t.Errorf("launch.mode = %q, want the recorded launch mode", rec.Launch.Mode)
	}
	if rec.Launch.ConfigSHA256 != launch["config_sha256"] {
		t.Errorf("launch.config_sha256 = %q, want the recorded config digest", rec.Launch.ConfigSHA256)
	}
	if rec.Launch.LitellmVersion != "1.93.0" {
		t.Errorf("launch.litellm_version = %q, want the recorded version", rec.Launch.LitellmVersion)
	}
	if rec.Launch.PanePID != 4242 {
		t.Errorf("launch.pane_pid = %d, want the recorded pane pid", rec.Launch.PanePID)
	}
}

// TestGatewayAuthImportClearsCoverageOnResolvedModeSwitchWithHandlePresent (BODY-2). modeSwitch must
// compare the RESOLVED mode to the recorded state .Mode, not `!subscriptionHandlePresent`. With a
// subscription handle already present but a prior state record naming api-key, a real switch has
// occurred and the stale coverage record must be cleared. RED at head: gateway_auth.go:510 keys
// modeSwitch on !subscriptionHandlePresent(root), which is false when a handle is present, so the
// switch is missed and the coverage record survives.
func TestGatewayAuthImportClearsCoverageOnResolvedModeSwitchWithHandlePresent(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")

	// A subscription handle already exists, backdated so the fresh CODEX source out-ages it and
	// import is not short-circuited as "already newer — nothing to import".
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "prior-refresh", "prior-access")
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(gatewayAuthHandlePath(root), old, old); err != nil {
		t.Fatal(err)
	}
	if !subscriptionHandlePresent(root) {
		t.Fatal("precondition: a subscription handle must already be present")
	}

	// The prior state record says api-key while a subscription handle is present, so the resolved
	// mode is codex-subscription — a REAL switch the head predicate (!subscriptionHandlePresent) misses.
	prev := healthySubscriptionState()
	prev.Mode = "api-key"
	writeSubscriptionState(t, root, prev)
	if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("codex-subscription\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	codexHome := codexAuthFixture(t, 1900000000, 1800000000, "acct-body2")
	t.Setenv("CODEX_HOME", codexHome)

	stalePath := filepath.Join(root, ".runtime", "model_coverage", "codex-subscription.json")
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePath, []byte(`{"v":1,"profile":"codex-subscription"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	if err := runGatewayAuthImport(cmd, nil); err != nil {
		t.Fatalf("runGatewayAuthImport: %v", err)
	}

	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("import must clear the stale coverage record when the resolved mode (codex-subscription) differs from the recorded state .Mode (api-key), even with a subscription handle already present (BODY-2); head keys modeSwitch on !subscriptionHandlePresent and misses it")
	}

	stData, err := os.ReadFile(gatewayAuthStatePath(root))
	if err != nil {
		t.Fatalf("read state record: %v", err)
	}
	var st gatewayAuthState
	if err := json.Unmarshal(stData, &st); err != nil {
		t.Fatal(err)
	}
	if st.Mode != gatewayAuthProfileName {
		t.Errorf("written state .Mode = %q, want the resolved subscription mode %q", st.Mode, gatewayAuthProfileName)
	}
}
