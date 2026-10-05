package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestSubscriptionLaunchLineNamesSubscriptionConfig is T1 (red_predictions.md T1 /
// concern_tests.md §T1): the codex-subscription relaunch `tmux new-session` line must name the
// subscription config `.agentfactory/litellm.codex-subscription.yaml`, not `.agentfactory/litellm.yaml`.
//
// The bug (quickstart.sh:1370) survived because the relaunch-script shape tests assert the
// WRITER's text, never the argv the generated script actually emits (mutation B9). This pins the
// emitted argv the reviewer's own way: run the real writer to disk, run the generated
// gateway-relaunch.sh in its codex-subscription launch path, and read the `tmux new-session`
// command it hands off — via an argv-logging tmux stub (writeIdentityStubs).
//
// RED at head: the emitted new-session argv names `.agentfactory/litellm.yaml`, so the
// subscription-config assertion fails. GREEN once quickstart.sh:1370 names the subscription file.
//
// Protective (P2): this test only inspects the codex-subscription branch's argv — it asserts
// nothing about the api-key launch line, which must stay `--config .agentfactory/litellm.yaml`.
func TestSubscriptionLaunchLineNamesSubscriptionConfig(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	writer := relaunchWriterSource(t, setup)

	dir := t.TempDir()
	chatgptDir := filepath.Join(dir, ".agentfactory", "secrets", "chatgpt")
	if err := os.MkdirAll(chatgptDir, 0o700); err != nil {
		t.Fatalf("mkdir chatgpt secrets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", "secrets", "litellm.key"), []byte("sk-fake-master-key-bytes"), 0o600); err != nil {
		t.Fatalf("writing fake litellm.key: %v", err)
	}
	// The generated relaunch script reads the auth mode from .agentfactory/litellm-auth-mode at RUN
	// time (BODY-4/decisions.md D2), never from a write-time baked value, and refuses (warn+exit 0)
	// when the record is absent — so the codex-subscription launch branch is reached only when the
	// record on disk names it. Seed the record; the `$3` writer arg no longer selects the mode.
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", "litellm-auth-mode"), []byte("codex-subscription\n"), 0o600); err != nil {
		t.Fatalf("writing litellm-auth-mode record: %v", err)
	}
	// The codex-subscription launch path refuses to relaunch unless the subscription handle exists.
	if err := os.WriteFile(filepath.Join(chatgptDir, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"refresh_token":"rt-fake"}}`), 0o600); err != nil {
		t.Fatalf("writing fake chatgpt auth.json: %v", err)
	}

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")

	// Same write-then-run idiom as TestGatewayRelaunchScriptIdentityAndLaunchJSON, but driven with
	// LITELLM_AUTH_MODE=codex-subscription so the codex-subscription launch branch executes.
	script := "set -euo pipefail\n" +
		"_write_relaunch() {\n" +
		"  local factory_root=\"$1\"\n" +
		"  local LITELLM_PORT=\"$2\"\n" +
		"  local LITELLM_AUTH_MODE=\"$3\"\n" +
		writer + "\n" +
		"  printf '%s' \"$relaunch_script\"\n" +
		"}\n" +
		"script_path=\"$(_write_relaunch " + shq(dir) + " 45999 codex-subscription)\"\n" +
		"\"$script_path\"\n"

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin, "TMUX_LOG="+tmuxLog, "TMUX_HAS_SESSION=0", "TMUX_PANE_PID=9999")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("writing+running the codex-subscription gateway-relaunch.sh failed: %v\n%s", err, out)
	}

	logData, readErr := os.ReadFile(tmuxLog)
	if readErr != nil {
		t.Fatalf("reading the tmux argv log: %v", readErr)
	}
	var newSession string
	for _, line := range strings.Split(string(logData), "\n") {
		if strings.HasPrefix(line, "tmux new-session") {
			newSession = line
			break
		}
	}
	if newSession == "" {
		t.Fatalf("the codex-subscription relaunch never emitted a `tmux new-session`; tmux log:\n%s", logData)
	}

	const wantConfig = "--config .agentfactory/litellm.codex-subscription.yaml"
	if !strings.Contains(newSession, wantConfig) {
		t.Errorf("codex-subscription `tmux new-session` must name the subscription config;\nemitted argv: %s\nwant substring: %q", newSession, wantConfig)
	}
}
