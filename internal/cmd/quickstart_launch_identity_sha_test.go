package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// launchIdentitySHA64Re matches a lowercase hex sha256 digest — the shape BODY-6 requires
// config_sha256 to keep even when openssl is off the PATH (sha256sum is a coreutil, openssl is not).
var launchIdentitySHA64Re = regexp.MustCompile(`^[0-9a-f]{64}$`)

// writeRelaunchHarness builds the write-then-run bash idiom shared by the gateway-relaunch.sh
// pins: it inlines the real writer body (relaunchWriterSource), writes the script to disk under
// `dir`, and runs it with `runArgs` (e.g. "--identity", or "" for the launch path). It mirrors
// TestGatewayRelaunchScriptIdentityAndLaunchJSON's idiom byte-for-byte so the emitted script is the
// production one, not a paraphrase.
func writeRelaunchHarness(writer, dir, mode, runArgs string) string {
	invoke := "\"$script_path\""
	if runArgs != "" {
		invoke += " " + runArgs
	}
	return "set -euo pipefail\n" +
		"_write_relaunch() {\n" +
		"  local factory_root=\"$1\"\n" +
		"  local LITELLM_PORT=\"$2\"\n" +
		"  local LITELLM_AUTH_MODE=\"$3\"\n" +
		writer + "\n" +
		"  printf '%s' \"$relaunch_script\"\n" +
		"}\n" +
		"script_path=\"$(_write_relaunch " + shq(dir) + " 45999 " + mode + ")\"\n" +
		invoke + "\n"
}

// TestGatewayIdentityConfigSHAUsesSha256sumBothSites is BODY-6 (red_predictions.md BODY-6 /
// concern_tests.md §BODY-6): config_sha256 must be a real sha256 digest computed with a coreutil
// (sha256sum), not openssl, and the `--identity` site (quickstart.sh:1337) and the launch.json site
// (:1385) must emit the SAME hex for the same config file. Run both writers with a PATH that has
// sha256sum but NOT openssl.
//
// RED at head: both sites use `openssl dgst -sha256`; with openssl absent both hash to "" and
// falsely compare equal, so the 64-hex assertion fails. GREEN once both use sha256sum.
func TestGatewayIdentityConfigSHAUsesSha256sumBothSites(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	writer := relaunchWriterSource(t, setup)

	dir := t.TempDir()
	af := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(af, "secrets"), 0o755); err != nil {
		t.Fatalf("mkdir secrets dir: %v", err)
	}
	// A real config file so the digest is deterministic and non-empty once sha256sum is used.
	if err := os.WriteFile(filepath.Join(af, "litellm.yaml"), []byte("model_list: []\n# body-6 fixture\n"), 0o644); err != nil {
		t.Fatalf("writing litellm.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(af, "secrets", "openai.key"), []byte("sk-fake-openai-key-bytes"), 0o600); err != nil {
		t.Fatalf("writing openai.key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(af, "secrets", "litellm.key"), []byte("sk-fake-master-key-bytes"), 0o600); err != nil {
		t.Fatalf("writing litellm.key: %v", err)
	}
	// The generated relaunch script reads the auth mode from .agentfactory/litellm-auth-mode at RUN
	// time (BODY-4/decisions.md D2) and refuses (warn+exit 0) when absent; seed it so both the
	// --identity and launch.json sites run and hash the same config.
	if err := os.WriteFile(filepath.Join(af, "litellm-auth-mode"), []byte("api-key\n"), 0o600); err != nil {
		t.Fatalf("writing litellm-auth-mode record: %v", err)
	}

	// The post-fix coreutils: sha256sum present, openssl removed.
	bin := hermeticGatewayBinDir(t)
	if err := os.Remove(filepath.Join(bin, "openssl")); err != nil {
		t.Fatalf("removing openssl from the hermetic bin (to prove config_sha256 no longer depends on it): %v", err)
	}
	for _, tool := range []string{"sha256sum", "cut", "awk"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("resolving real %s: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(bin, tool)); err != nil {
			t.Fatalf("symlinking %s into the hermetic bin: %v", tool, err)
		}
	}
	writeIdentityStubs(t, bin)
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")

	// --identity site.
	idCmd := exec.Command("bash", "-c", writeRelaunchHarness(writer, dir, "api-key", "--identity"))
	idCmd.Env = hermeticCodexEnv(bin, "TMUX_LOG="+tmuxLog)
	idOut, idErr := idCmd.CombinedOutput()
	if idErr != nil {
		t.Fatalf("running gateway-relaunch.sh --identity failed: %v\n%s", idErr, idOut)
	}
	var identity map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(idOut))), &identity); err != nil {
		t.Fatalf("--identity did not print valid JSON: %v\noutput:\n%s", err, idOut)
	}
	idSha, _ := identity["config_sha256"].(string)
	if !launchIdentitySHA64Re.MatchString(idSha) {
		t.Errorf("--identity config_sha256 must be a 64-hex sha256 digest even without openssl (BODY-6); got %q", idSha)
	}

	// launch.json site — a successful tmux new-session writes launch.json.
	launchCmd := exec.Command("bash", "-c", writeRelaunchHarness(writer, dir, "api-key", ""))
	launchCmd.Env = hermeticCodexEnv(bin, "TMUX_LOG="+tmuxLog, "TMUX_HAS_SESSION=0", "TMUX_PANE_PID=9999")
	launchOut, launchErr := launchCmd.CombinedOutput()
	if launchErr != nil {
		t.Fatalf("running gateway-relaunch.sh (launch path) failed: %v\n%s", launchErr, launchOut)
	}
	lj, readErr := os.ReadFile(filepath.Join(dir, ".runtime", "gateway", "launch.json"))
	if readErr != nil {
		t.Fatalf("launch.json not written: %v", readErr)
	}
	var launch map[string]any
	if err := json.Unmarshal(lj, &launch); err != nil {
		t.Fatalf("launch.json is not valid JSON: %v\n%s", err, lj)
	}
	ljSha, _ := launch["config_sha256"].(string)
	if !launchIdentitySHA64Re.MatchString(ljSha) {
		t.Errorf("launch.json config_sha256 must be a 64-hex sha256 digest even without openssl (BODY-6); got %q", ljSha)
	}
	if idSha != ljSha {
		t.Errorf("the --identity and launch.json sites must hash the same config file to the SAME hex (BODY-6); identity=%q launch=%q", idSha, ljSha)
	}
}

// TestGatewayRelaunchReadsAuthModeRecordNotBaked is BODY-4 (red_predictions.md BODY-4 /
// concern_tests.md §BODY-4): the generated gateway-relaunch.sh must derive LITELLM_AUTH_MODE from
// the on-disk record `.agentfactory/litellm-auth-mode` at run time, not from a write-time baked
// literal — and a run with NO record must warn and exit 0 without launching (the silent-fallback
// the doctrine forbids: "guard and surface", never re-bake the stale value).
//
// RED at head: quickstart.sh:1325 bakes `LITELLM_AUTH_MODE="$LITELLM_AUTH_MODE"`, so (a) the writer
// body never reads the record, and (b) a run with no record still launches with the baked value.
func TestGatewayRelaunchReadsAuthModeRecordNotBaked(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	writer := relaunchWriterSource(t, setup)

	// Source arm.
	if strings.Contains(writer, `LITELLM_AUTH_MODE="$LITELLM_AUTH_MODE"`) {
		t.Errorf("the gateway-relaunch.sh writer bakes a write-time LITELLM_AUTH_MODE literal (quickstart.sh:1325); it must read .agentfactory/litellm-auth-mode at run time (BODY-4)")
	}
	if !strings.Contains(writer, "litellm-auth-mode") {
		t.Errorf("the gateway-relaunch.sh writer never references .agentfactory/litellm-auth-mode; the mode must come from the record, not a baked value (BODY-4)")
	}

	// Behavioral arm: no record on disk ⇒ warn + exit 0, no launch.
	dir := t.TempDir()
	af := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(af, "secrets"), 0o755); err != nil {
		t.Fatalf("mkdir secrets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(af, "litellm.yaml"), []byte("model_list: []\n"), 0o644); err != nil {
		t.Fatalf("writing litellm.yaml: %v", err)
	}
	if err := os.WriteFile(filepath.Join(af, "secrets", "openai.key"), []byte("sk-fake-openai-key-bytes"), 0o600); err != nil {
		t.Fatalf("writing openai.key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(af, "secrets", "litellm.key"), []byte("sk-fake-master-key-bytes"), 0o600); err != nil {
		t.Fatalf("writing litellm.key: %v", err)
	}
	// Deliberately DO NOT write .agentfactory/litellm-auth-mode.

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")

	cmd := exec.Command("bash", "-c", writeRelaunchHarness(writer, dir, "api-key", ""))
	cmd.Env = hermeticCodexEnv(bin, "TMUX_LOG="+tmuxLog, "TMUX_HAS_SESSION=0", "TMUX_PANE_PID=9999")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running gateway-relaunch.sh (missing-record path) failed: %v\n%s", err, out)
	}

	if _, statErr := os.Stat(filepath.Join(dir, ".runtime", "gateway", "launch.json")); statErr == nil {
		t.Errorf("with no .agentfactory/litellm-auth-mode record the relaunch must warn and exit 0 WITHOUT launching, yet launch.json was written (BODY-4)")
	}
	if logData, _ := os.ReadFile(tmuxLog); strings.Contains(string(logData), "new-session") {
		t.Errorf("with no .agentfactory/litellm-auth-mode record the relaunch must not emit a `tmux new-session` (BODY-4); tmux log:\n%s", logData)
	}
}

// credentialRefFixture lays out a factory root the real gateway-relaunch.sh can run against in the
// given mode, and a hermetic bin carrying every coreutil an mtime/field read could reasonably use, so
// a credential_ref that comes out empty is the script's doing, not a tool the harness withheld.
func credentialRefFixture(t *testing.T, mode string) (dir, bin string) {
	t.Helper()
	dir = t.TempDir()
	af := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(af, "secrets", "chatgpt"), 0o700); err != nil {
		t.Fatalf("mkdir secrets dir: %v", err)
	}
	files := map[string]string{
		"litellm.yaml":                          "model_list: []\n",
		"litellm.codex-subscription.yaml":       "model_list: []\n",
		filepath.Join("secrets", "litellm.key"): "sk-fake-master-key-bytes",
		"litellm-auth-mode":                     mode + "\n",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(af, name), []byte(body), 0o600); err != nil {
			t.Fatalf("writing %s: %v", name, err)
		}
	}
	bin = hermeticGatewayBinDir(t)
	for _, tool := range []string{"stat", "sha256sum", "cut", "awk"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("resolving real %s: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(bin, tool)); err != nil {
			t.Fatalf("symlinking %s into the hermetic bin: %v", tool, err)
		}
	}
	writeIdentityStubs(t, bin)
	return dir, bin
}

func relaunchIdentityCredentialRef(t *testing.T, writer, dir, bin, mode string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", writeRelaunchHarness(writer, dir, mode, "--identity"))
	cmd.Env = hermeticCodexEnv(bin, "TMUX_LOG="+filepath.Join(t.TempDir(), "tmux.log"))
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running gateway-relaunch.sh --identity failed: %v\n%s", err, out)
	}
	var identity map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &identity); err != nil {
		t.Fatalf("--identity did not print valid JSON: %v\noutput:\n%s", err, out)
	}
	ref, ok := identity["credential_ref"].(string)
	if !ok {
		t.Fatalf("--identity has no string credential_ref: %s", out)
	}
	return ref
}

// assertCredentialRefCarriesNoSecret enforces design-doc.md:175: credential_ref is never token
// bytes, their hashes, or a path to the secret.
func assertCredentialRefCarriesNoSecret(t *testing.T, ref, dir string, secrets ...string) {
	t.Helper()
	if strings.Contains(ref, "/") || strings.Contains(ref, dir) {
		t.Errorf("credential_ref %q is a filesystem path; it must identify the credential's version, not its location", ref)
	}
	for _, secret := range secrets {
		sum := sha256.Sum256([]byte(secret))
		if strings.Contains(ref, secret) || strings.Contains(ref, hex.EncodeToString(sum[:])) {
			t.Errorf("credential_ref %q carries secret bytes or their sha256", ref)
		}
	}
}

// TestGatewayRelaunchCredentialRefTracksOpenAIKeyMtime pins T3 (api-key): rotating openai.key must
// change the launch identity so the next _reconcile_gateway restarts the gateway, while an untouched
// key keeps it stable across runs.
func TestGatewayRelaunchCredentialRefTracksOpenAIKeyMtime(t *testing.T) {
	writer := relaunchWriterSource(t, setupLitellmSource(t, findModuleRoot(t)))
	dir, bin := credentialRefFixture(t, "api-key")

	const key = "sk-fake-openai-key-bytes"
	keyPath := filepath.Join(dir, ".agentfactory", "secrets", "openai.key")
	if err := os.WriteFile(keyPath, []byte(key), 0o600); err != nil {
		t.Fatalf("writing openai.key: %v", err)
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(keyPath, t0, t0); err != nil {
		t.Fatal(err)
	}

	ref1 := relaunchIdentityCredentialRef(t, writer, dir, bin, "api-key")
	ref2 := relaunchIdentityCredentialRef(t, writer, dir, bin, "api-key")
	if ref1 == "" {
		t.Fatal("credential_ref is empty with openai.key present; the identity cannot track the key")
	}
	if ref1 != ref2 {
		t.Errorf("credential_ref changed across runs with an untouched openai.key (%q then %q); every bootstrap would restart the gateway", ref1, ref2)
	}

	t1 := t0.Add(time.Hour)
	if err := os.Chtimes(keyPath, t1, t1); err != nil {
		t.Fatal(err)
	}
	ref3 := relaunchIdentityCredentialRef(t, writer, dir, bin, "api-key")
	if ref3 == ref1 {
		t.Errorf("credential_ref stayed %q after openai.key was rotated; _reconcile_gateway will never restart the gateway onto the new key", ref1)
	}
	for _, ref := range []string{ref1, ref3} {
		assertCredentialRefCarriesNoSecret(t, ref, dir, key, key+"\n")
	}
}

// TestGatewayRelaunchCredentialRefTracksImportedAt pins T3 (subscription): only a fresh
// `af gateway auth import` (a new imported_at) may change the identity; LiteLLM's own in-place
// refresh of the handle must not, or every token refresh would restart the gateway.
func TestGatewayRelaunchCredentialRefTracksImportedAt(t *testing.T) {
	writer := relaunchWriterSource(t, setupLitellmSource(t, findModuleRoot(t)))
	dir, bin := credentialRefFixture(t, "codex-subscription")

	handle := filepath.Join(dir, ".agentfactory", "secrets", "chatgpt", "auth.json")
	writeHandle := func(access, refresh string, mtime time.Time) {
		t.Helper()
		body := `{"access_token":"` + access + `","refresh_token":"` + refresh + `"}`
		if err := os.WriteFile(handle, []byte(body), 0o600); err != nil {
			t.Fatalf("writing handle: %v", err)
		}
		if err := os.Chtimes(handle, mtime, mtime); err != nil {
			t.Fatal(err)
		}
	}
	t0 := time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)
	writeHandle("at-fake-1", "rt-fake-1", t0)
	writeSubscriptionState(t, dir, gatewayAuthState{Mode: gatewayAuthProfileName, ImportedAt: "2026-09-01T00:00:00Z", State: gatewayAuthStateOK})

	ref1 := relaunchIdentityCredentialRef(t, writer, dir, bin, "codex-subscription")
	ref2 := relaunchIdentityCredentialRef(t, writer, dir, bin, "codex-subscription")
	if ref1 == "" {
		t.Fatal("credential_ref is empty with an imported_at on record; the identity cannot track imports")
	}
	if ref1 != ref2 {
		t.Errorf("credential_ref changed across runs with nothing re-imported (%q then %q)", ref1, ref2)
	}

	writeHandle("at-fake-2", "rt-fake-2", t0.Add(time.Hour))
	if ref := relaunchIdentityCredentialRef(t, writer, dir, bin, "codex-subscription"); ref != ref1 {
		t.Errorf("credential_ref moved %q -> %q on an in-place handle refresh; only a new import may restart the gateway", ref1, ref)
	}

	writeSubscriptionState(t, dir, gatewayAuthState{Mode: gatewayAuthProfileName, ImportedAt: "2026-09-02T00:00:00Z", State: gatewayAuthStateOK})
	ref3 := relaunchIdentityCredentialRef(t, writer, dir, bin, "codex-subscription")
	if ref3 == ref1 {
		t.Errorf("credential_ref stayed %q after a fresh import; the new handle never reaches the running gateway", ref1)
	}
	for _, ref := range []string{ref1, ref3} {
		assertCredentialRefCarriesNoSecret(t, ref, dir, "at-fake-1", "rt-fake-1", "at-fake-2", "rt-fake-2")
	}
}

// TestReconcileGatewayRestartsWhenOpenAIKeyRotates drives the real generated script through the real
// _reconcile_gateway: a gateway launched on one key is kept while the key is untouched and restarted
// once the key is rotated.
func TestReconcileGatewayRestartsWhenOpenAIKeyRotates(t *testing.T) {
	writer := relaunchWriterSource(t, setupLitellmSource(t, findModuleRoot(t)))
	dir, bin := credentialRefFixture(t, "api-key")
	keyPath := filepath.Join(dir, ".agentfactory", "secrets", "openai.key")
	if err := os.WriteFile(keyPath, []byte("sk-fake-openai-key-bytes"), 0o600); err != nil {
		t.Fatalf("writing openai.key: %v", err)
	}
	t0 := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	if err := os.Chtimes(keyPath, t0, t0); err != nil {
		t.Fatal(err)
	}

	launch := exec.Command("bash", "-c", writeRelaunchHarness(writer, dir, "api-key", ""))
	launch.Env = hermeticCodexEnv(bin, "TMUX_LOG="+filepath.Join(t.TempDir(), "tmux.log"), "TMUX_HAS_SESSION=0", "TMUX_PANE_PID=9999")
	if out, err := launch.CombinedOutput(); err != nil {
		t.Fatalf("initial launch of gateway-relaunch.sh failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, ".runtime", "gateway", "launch.json")); err != nil {
		t.Fatalf("initial launch did not record launch.json: %v", err)
	}

	harness := reconcileHarness(t, quickstartScriptContent(t))
	relaunch := filepath.Join(dir, ".agentfactory", "gateway-relaunch.sh")
	reconcile := func() string {
		t.Helper()
		tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
		cmd := exec.Command("bash", "-c", harness+"\ncd "+shq(dir)+"\n_reconcile_gateway "+shq(relaunch)+"\n")
		cmd.Env = hermeticCodexEnv(bin, "TMUX_LOG="+tmuxLog, "TMUX_HAS_SESSION=1", "LITELLM_PORT=45998")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("_reconcile_gateway failed: %v\n%s", err, out)
		}
		data, _ := os.ReadFile(tmuxLog)
		return string(data)
	}

	if log := reconcile(); strings.Contains(log, "kill-session") {
		t.Errorf("_reconcile_gateway restarted a gateway whose key is unchanged; tmux log:\n%s", log)
	}
	t1 := t0.Add(time.Hour)
	if err := os.Chtimes(keyPath, t1, t1); err != nil {
		t.Fatal(err)
	}
	if log := reconcile(); !strings.Contains(log, "kill-session") {
		t.Errorf("_reconcile_gateway kept the running gateway after openai.key was rotated; it still holds the old OPENAI_API_KEY. tmux log:\n%s", log)
	}
}
