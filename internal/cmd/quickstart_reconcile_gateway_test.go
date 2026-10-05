package cmd

import (
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// --- PR #694 Phase 4 (K10 identity/launch.json, K12 reconcile-and-kill, K15 write ordering) ----
//
// Pinning tests per todos/fable-implement/concern_tests.md (Test Strategist) and
// investigation_report.md (Phase 3 consensus). Written RED, before quickstart.sh gains
// _gateway_stop/_reconcile_gateway, the --identity/launch.json writer additions, and the
// write-ordering moves — see todos/fable-implement/red_predictions.md for the predicted
// failure per test.

// --- AC #1 -----------------------------------------------------------------------------------

// TestQuickstartExactlyOneKillSessionSite pins AC #1 (design-doc.md:126): _gateway_stop is the
// ONLY kill site in the whole script, even though it is called from two places (the reconcile
// decision and the build-time version-mismatch precheck).
func TestQuickstartExactlyOneKillSessionSite(t *testing.T) {
	content := quickstartScriptContent(t)
	got := strings.Count(content, "kill-session")
	if got != 1 {
		t.Errorf("kill-session count = %d, want exactly 1 (AC #1: _gateway_stop is the only kill site)", got)
	}
}

// --- AC #2 -------------------------------------------------------------------------------------

// TestQuickstartReconcileMarkerPrecedesKillWithinGatewayStop pins AC #2 and design-doc.md:126/176:
// _gateway_stop must write the .runtime/gateway/reconciling marker BEFORE it kills the session —
// a bare presence check would pass even if the marker were written after the kill, so this
// specifically checks ordering within the extracted function body.
func TestQuickstartReconcileMarkerPrecedesKillWithinGatewayStop(t *testing.T) {
	content := quickstartScriptContent(t)
	gatewayStop := extractShellFunction(content, "_gateway_stop")
	if gatewayStop == "" {
		t.Fatal("could not extract _gateway_stop() from quickstart.sh — not yet implemented (K12)")
	}
	markerIdx := strings.Index(gatewayStop, "gateway/reconciling")
	if markerIdx < 0 {
		t.Fatal("_gateway_stop() does not reference the reconcile marker path `gateway/reconciling`")
	}
	killIdx := strings.Index(gatewayStop, "kill-session")
	if killIdx < 0 {
		t.Fatal("_gateway_stop() does not call kill-session")
	}
	if markerIdx > killIdx {
		t.Errorf("_gateway_stop() writes the reconcile marker AFTER kill-session (marker at %d, kill at %d); "+
			"the marker must be written first so the interlock covers the kill window", markerIdx, killIdx)
	}
}

// TestQuickstartImportProbeFollowsPipInstall pins AC #2/design-doc.md:126 K12-e: an
// `import litellm.proxy` probe must exist in setup_litellm(), textually after the pinned
// `pip3 install` line, and run unconditionally (not only inside the fresh-install branch).
func TestQuickstartImportProbeFollowsPipInstall(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	pipIdx := strings.Index(setup, "pip3 install")
	if pipIdx < 0 {
		t.Fatal("setup_litellm() no longer contains the pinned `pip3 install` line")
	}
	probeIdx := strings.Index(setup, "import litellm.proxy")
	if probeIdx < 0 {
		t.Fatal("setup_litellm() does not contain the `import litellm.proxy` build pre-check probe (K12-e)")
	}
	if probeIdx < pipIdx {
		t.Errorf("the import-probe (at %d) appears before the pip3 install line (at %d); "+
			"K12-e requires the probe to run after the install guard closes", probeIdx, pipIdx)
	}
}

// TestQuickstartGatewayStopCalledBeforePipInstall pins the K12-d build-ordering clause
// (design-doc.md:126): before the pip step, if a `litellm` session exists and its version
// differs from the pin, _gateway_stop runs first — a mixed-version live process is worse than a
// stopped one. This is an ordering-only shape check (no execution harness): a runtime harness for
// the full precheck branch is deferred to Phase 6 once the implementer's chosen factoring (inline
// vs. a separate top-level unit, per concern_tests.md §1 bullet 3) is known.
func TestQuickstartGatewayStopCalledBeforePipInstall(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	pipIdx := strings.Index(setup, "pip3 install")
	if pipIdx < 0 {
		t.Fatal("setup_litellm() no longer contains the pinned `pip3 install` line")
	}
	stopIdx := strings.Index(setup, "_gateway_stop")
	if stopIdx < 0 {
		t.Fatal("setup_litellm() never calls _gateway_stop — K12-d's build-time version-mismatch precheck is not implemented")
	}
	if stopIdx > pipIdx {
		t.Errorf("the first _gateway_stop reference (at %d) appears after the pip3 install line (at %d); "+
			"K12-d requires stopping a mismatched live session before reinstalling", stopIdx, pipIdx)
	}
}

// --- AC #5 bullet 2: write ordering -------------------------------------------------------------

// TestQuickstartLoginGuardBodyIsRelaunchOnly pins K15-b: the login-shell guard body must contain
// ONLY the relaunch-script invocation — the inline `tmux has-session -t =litellm || …` check must
// be dropped, since the relaunch script now no-ops on its own via internal has-session logic.
func TestQuickstartLoginGuardBodyIsRelaunchOnly(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	const lbegin = "# BEGIN agentfactory litellm login guard"
	const lend = "# END agentfactory litellm login guard"
	bi := strings.Index(setup, lbegin)
	if bi < 0 {
		t.Fatal("could not find the login guard's BEGIN marker in setup_litellm()")
	}
	rest := setup[bi+len(lbegin):]
	ei := strings.Index(rest, lend)
	if ei < 0 {
		t.Fatal("could not find the login guard's END marker in setup_litellm()")
	}
	body := strings.TrimSpace(rest[:ei])

	if strings.Contains(body, "has-session") || strings.Contains(body, "if ") {
		t.Errorf("login guard body still contains a has-session/if check; K15-b requires the body "+
			"to be exactly the relaunch-script invocation, nothing else. Body:\n%s", body)
	}
	if !strings.Contains(body, `"$relaunch_script"`) {
		t.Errorf("login guard body does not invoke \"$relaunch_script\". Body:\n%s", body)
	}
}

// TestQuickstartLoginGuardWrittenBeforeReadinessPoll pins K15-a: the relaunch script and the
// login-shell guard must be written immediately after the mode record and before
// _reconcile_gateway/verification — specifically, before the readiness poll — so a run that dies
// in verification never leaves a stale-mode guard un-updated.
func TestQuickstartLoginGuardWrittenBeforeReadinessPoll(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	const guardMarker = "# BEGIN agentfactory litellm login guard"
	guardIdx := strings.Index(setup, guardMarker)
	if guardIdx < 0 {
		t.Fatal("could not find the login guard's BEGIN marker in setup_litellm()")
	}
	const readinessMarker = "# Readiness:"
	readinessIdx := strings.Index(setup, readinessMarker)
	if readinessIdx < 0 {
		t.Fatal("could not find the `# Readiness:` comment anchoring the readiness poll in setup_litellm()")
	}
	if guardIdx > readinessIdx {
		t.Errorf("the login guard is written AFTER the readiness poll (guard at %d, readiness at %d); "+
			"K15-a requires the guard to be written before verification, matching setup_telemetry's "+
			"\"written BEFORE every early return\" precedent", guardIdx, readinessIdx)
	}
}

// TestQuickstartBannerBetweenLitellmAndTelemetry pins K15-c/D25: the "Setup Complete!" banner
// must move to between the setup_litellm call and the setup_telemetry call in main(), so a run
// that dies inside setup_litellm's login/verification steps never announces completion first.
func TestQuickstartBannerBetweenLitellmAndTelemetry(t *testing.T) {
	content := quickstartScriptContent(t)

	litellmIdx := strings.Index(content, "\n        setup_litellm\n")
	if litellmIdx < 0 {
		t.Fatal("could not find the setup_litellm call site in main()")
	}
	bannerIdx := strings.Index(content, "Setup Complete!")
	if bannerIdx < 0 {
		t.Fatal("could not find the \"Setup Complete!\" banner")
	}
	telemetryIdx := strings.Index(content, "\n        setup_telemetry\n")
	if telemetryIdx < 0 {
		t.Fatal("could not find the setup_telemetry call site in main()")
	}

	if !(litellmIdx < bannerIdx && bannerIdx < telemetryIdx) {
		t.Errorf("expected order setup_litellm(%d) < banner(%d) < setup_telemetry(%d); K15-c/D25 requires "+
			"the banner to print between the two calls, not before both", litellmIdx, bannerIdx, telemetryIdx)
	}
}

// --- DO-NOT-CHANGE protective assertions -------------------------------------------------------

// TestQuickstartPortHelpersDefinedExactlyOnce protects K11's "reuse, never redefine"
// DO-NOT-CHANGE instruction: _port_in_use/_port_owner_pid must stay defined exactly once, even
// after _gateway_stop/_reconcile_gateway are added. PASSES today (protective).
func TestQuickstartPortHelpersDefinedExactlyOnce(t *testing.T) {
	content := quickstartScriptContent(t)
	for _, name := range []string{"_port_in_use() {", "_port_owner_pid() {"} {
		got := strings.Count(content, name)
		if got != 1 {
			t.Errorf("%s defined %d times, want exactly 1 (K11 DO-NOT-CHANGE: reuse, never redefine)", name, got)
		}
	}
}

// TestQuickstartHasSessionBareFormNeverAppears is the permanent half of
// TestQuickstartLitellmHasSessionUsesExactMatchCount's split (decisions.md D11, concern_tests.md
// §5): regardless of how many `has-session -t =litellm` call sites the Phase-4 diff settles on,
// the bare non-exact form `has-session -t litellm` (no `=`) must never reappear. PASSES today
// (protective) — the exact-count half of the split is resolved separately, once the Phase-4 diff
// fixes the real call sites (D11).
func TestQuickstartHasSessionBareFormNeverAppears(t *testing.T) {
	content := quickstartScriptContent(t)
	got := strings.Count(content, "has-session -t litellm")
	if got != 0 {
		t.Errorf("has-session -t litellm (bare, non-exact form) count = %d, want 0", got)
	}
}

// --- AC #5 bullet 1: identity JSON + launch.json harness ----------------------------------------

// relaunchWriterSource extracts the gateway-relaunch.sh writer block from setup_litellm()'s
// source — from the `local relaunch_script=...` declaration through the `chmod 0700
// "$relaunch_script"` line, inclusive. This bounding pair is stable across the K10 diff: the
// heredocs' CONTENT gains the --identity branch and the launch.json write, but the declaration
// and the chmod line bracket the whole writer both before and after that change.
func relaunchWriterSource(t *testing.T, setup string) string {
	t.Helper()
	const startMarker = `local relaunch_script="$factory_root/.agentfactory/gateway-relaunch.sh"`
	start := strings.Index(setup, startMarker)
	if start < 0 {
		t.Fatal("could not find the gateway-relaunch.sh writer's start (`local relaunch_script=...`) in setup_litellm()")
	}
	const endMarker = `chmod 0700 "$relaunch_script"`
	relIdx := strings.Index(setup[start:], endMarker)
	if relIdx < 0 {
		t.Fatal("could not find `chmod 0700 \"$relaunch_script\"` after the writer's start in setup_litellm()")
	}
	end := start + relIdx + len(endMarker)
	return setup[start:end]
}

// shq shell-quotes a string for safe embedding in a single-quoted bash literal.
func shq(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// hermeticGatewayBinDir extends hermeticCodexBinDir with the additional real coreutils the
// gateway-relaunch.sh writer's own text needs to actually execute (`cat >`/`cat >>` to perform
// the heredoc writes, `chmod`/`mkdir` for the file-mode and .runtime/gateway/ setup) — none of
// which the codex-consent harness ever needed, since it never writes a file to disk.
func hermeticGatewayBinDir(t *testing.T) string {
	t.Helper()
	bin := hermeticCodexBinDir(t)
	for _, tool := range []string{"cat", "chmod", "mkdir", "rm", "mv", "sed", "tr", "od", "openssl", "date", "dirname", "basename", "head"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("resolving real %s for the hermetic gateway bin dir: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(bin, tool)); err != nil {
			t.Fatalf("symlinking %s into the hermetic gateway bin dir: %v", tool, err)
		}
	}
	return bin
}

// writeIdentityStubs writes stub `litellm` and `tmux` binaries into bin, generalizing the
// hermeticCodexBinDir/writeCodexStub idiom (those two helpers are already mechanism-generic,
// nothing codex-specific in them — reused here rather than duplicated). The tmux stub logs every
// invocation to TMUX_LOG and is controlled by TMUX_HAS_SESSION / TMUX_PANE_PID env vars.
func writeIdentityStubs(t *testing.T, bin string) {
	t.Helper()
	writeCodexStub(t, bin, "litellm", `if [ "$1" = "--version" ]; then echo "litellm-fake 1.93.0"; fi
exit 0
`)
	writeCodexStub(t, bin, "tmux", `echo "tmux $*" >> "${TMUX_LOG:-/dev/null}"
case "$1" in
  has-session) [ "${TMUX_HAS_SESSION:-0}" = "1" ] && exit 0 || exit 1 ;;
  list-panes) echo "${TMUX_PANE_PID:-4242}"; exit 0 ;;
  *) exit 0 ;;
esac
`)
}

// TestGatewayRelaunchScriptIdentityAndLaunchJSON is the harness half of AC #5 bullet 1
// (concern_tests.md §1): a shape test cannot prove the --identity output is valid JSON, nor stat a
// real file's mode bits. This extracts the real writer heredocs, executes them for real (writing
// an actual gateway-relaunch.sh to a temp factory root), then runs the generated script.
func TestGatewayRelaunchScriptIdentityAndLaunchJSON(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	writer := relaunchWriterSource(t, setup)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory", "secrets"), 0o755); err != nil {
		t.Fatalf("mkdir secrets dir: %v", err)
	}
	const fakeOpenAIKey = "sk-fake-openai-key-bytes"
	const fakeMasterKey = "sk-fake-master-key-bytes"
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", "secrets", "openai.key"), []byte(fakeOpenAIKey), 0o600); err != nil {
		t.Fatalf("writing fake openai.key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", "secrets", "litellm.key"), []byte(fakeMasterKey), 0o600); err != nil {
		t.Fatalf("writing fake litellm.key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", "litellm.yaml"), []byte("model_list: []\n"), 0o644); err != nil {
		t.Fatalf("writing fake litellm.yaml: %v", err)
	}
	// The generated relaunch script derives the auth mode from .agentfactory/litellm-auth-mode at RUN
	// time (BODY-4/decisions.md D2) and refuses (warn+exit 0) when it is absent; seed it so --identity
	// and the launch path run. The `$3` writer arg no longer selects the mode.
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", "litellm-auth-mode"), []byte("api-key\n"), 0o600); err != nil {
		t.Fatalf("writing litellm-auth-mode record: %v", err)
	}

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")

	script := "set -euo pipefail\n" +
		"_write_relaunch() {\n" +
		"  local factory_root=\"$1\"\n" +
		"  local LITELLM_PORT=\"$2\"\n" +
		"  local LITELLM_AUTH_MODE=\"$3\"\n" +
		writer + "\n" +
		"  printf '%s' \"$relaunch_script\"\n" +
		"}\n" +
		"script_path=\"$(_write_relaunch " + shq(dir) + " 45999 api-key)\"\n" +
		"\"$script_path\" --identity\n"

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin, "TMUX_LOG="+tmuxLog)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("writing+running gateway-relaunch.sh --identity failed: %v\n%s", err, out)
	}

	var identity map[string]any
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(string(out))), &identity); jsonErr != nil {
		t.Fatalf("gateway-relaunch.sh --identity did not print valid JSON (K10-b not implemented): %v\noutput:\n%s", jsonErr, out)
	}
	for _, field := range []string{"v", "mode", "config_sha256", "litellm_version", "credential_ref", "port", "factory_root"} {
		if _, ok := identity[field]; !ok {
			t.Errorf("--identity JSON missing field %q; got: %v", field, identity)
		}
	}
	if strings.Contains(string(out), fakeOpenAIKey) || strings.Contains(string(out), fakeMasterKey) {
		t.Error("--identity output leaked secret bytes")
	}

	// Second run, no args: a successful tmux new-session must write launch.json (identity fields +
	// launched_at + pane_pid) under 0600, per K10-c.
	launchScript := "set -euo pipefail\n" +
		"_write_relaunch2() {\n" +
		"  local factory_root=\"$1\"\n" +
		"  local LITELLM_PORT=\"$2\"\n" +
		"  local LITELLM_AUTH_MODE=\"$3\"\n" +
		writer + "\n" +
		"  printf '%s' \"$relaunch_script\"\n" +
		"}\n" +
		"script_path=\"$(_write_relaunch2 " + shq(dir) + " 45999 api-key)\"\n" +
		"\"$script_path\"\n"
	cmd2 := exec.Command("bash", "-c", launchScript)
	cmd2.Env = hermeticCodexEnv(bin, "TMUX_LOG="+tmuxLog, "TMUX_HAS_SESSION=0", "TMUX_PANE_PID=9999")
	out2, err2 := cmd2.CombinedOutput()
	if err2 != nil {
		t.Fatalf("running gateway-relaunch.sh (no args, launch path) failed: %v\n%s", err2, out2)
	}

	launchJSONPath := filepath.Join(dir, ".runtime", "gateway", "launch.json")
	info, statErr := os.Stat(launchJSONPath)
	if statErr != nil {
		t.Fatalf("launch.json not written after a successful tmux new-session (K10-c not implemented): %v", statErr)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("launch.json mode = %o, want 0600", info.Mode().Perm())
	}
	lj, readErr := os.ReadFile(launchJSONPath)
	if readErr != nil {
		t.Fatalf("reading launch.json: %v", readErr)
	}
	var launch map[string]any
	if jsonErr := json.Unmarshal(lj, &launch); jsonErr != nil {
		t.Fatalf("launch.json is not valid JSON: %v\ncontent:\n%s", jsonErr, lj)
	}
	for _, field := range []string{"v", "mode", "config_sha256", "litellm_version", "credential_ref", "port", "factory_root", "launched_at", "pane_pid"} {
		if _, ok := launch[field]; !ok {
			t.Errorf("launch.json missing field %q; got: %v", field, launch)
		}
	}
}

// --- T-6 / T-7: _reconcile_gateway harness -------------------------------------------------------

// reconcileHarness extracts _gateway_stop, _reconcile_gateway, _port_in_use, _port_owner_pid,
// cleanup, and the log_*/command_exists helpers they call, from the real quickstart.sh, and seeds
// the CLEANUP_DIRS/trap-cleanup scaffolding that lives at quickstart.sh's top level (outside any
// function) so JC-8 (trap composition) can be exercised for real rather than assumed.
func reconcileHarness(t *testing.T, content string) string {
	t.Helper()
	names := []string{"command_exists", "log_step", "log_info", "log_success", "log_warn", "log_error",
		"_port_in_use", "_port_owner_pid", "cleanup", "_gateway_stop", "_reconcile_gateway"}
	var b strings.Builder
	b.WriteString("set -uo pipefail\n")
	b.WriteString("RED='' GREEN='' YELLOW='' BLUE='' NC=''\n")
	b.WriteString("CLEANUP_DIRS=()\n")
	for _, n := range names {
		fn := extractShellFunction(content, n)
		if fn == "" {
			t.Fatalf("could not extract %s() from quickstart.sh — not yet implemented (K12)", n)
		}
		b.WriteString(fn)
		b.WriteString("\n")
	}
	b.WriteString("trap cleanup EXIT\n")
	return b.String()
}

// writeRelaunchStub writes a fake `"$relaunch_script"` used as _reconcile_gateway's argument:
// `--identity` prints a fixture identity JSON (content controlled by env vars so tests can make it
// match or mismatch a pre-seeded launch.json); no-arg mode appends a line to LAUNCH_LOG and writes
// a fresh launch.json under .runtime/gateway/.
func writeRelaunchStub(t *testing.T, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "fake-relaunch.sh")
	body := `#!/bin/sh
if [ "$1" = "--identity" ]; then
  printf '{"v":1,"mode":"api-key","config_sha256":"%s","litellm_version":"1.93.0","credential_ref":"%s","port":4000,"factory_root":"%s"}' \
    "${STUB_IDENTITY_SHA:-aaa}" "${STUB_CREDENTIAL_REF-ref}" "${STUB_FACTORY_ROOT:-/tmp}"
  exit 0
fi
echo "launch $*" >> "${LAUNCH_LOG:-/dev/null}"
mkdir -p "${STUB_FACTORY_ROOT:-/tmp}/.runtime/gateway"
printf '{"v":1,"mode":"api-key","config_sha256":"%s","litellm_version":"1.93.0","credential_ref":"ref","port":4000,"factory_root":"%s","launched_at":1,"pane_pid":"${STUB_PANE_PID:-4242}"}' \
  "${STUB_IDENTITY_SHA:-aaa}" "${STUB_FACTORY_ROOT:-/tmp}" > "${STUB_FACTORY_ROOT:-/tmp}/.runtime/gateway/launch.json"
exit 0
`
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("writing fake relaunch stub: %v", err)
	}
	return path
}

// TestQuickstartReconcileGatewayRestartsOnIdentityMismatch is T-6 (concern_tests.md §2): the
// six-branch reconcile-decision matrix (design-doc.md:126), covered as subtests since no AC
// requires new top-level function names for the fresh-bring-up / post-launch-mismatch branches.
func TestQuickstartReconcileGatewayRestartsOnIdentityMismatch(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := reconcileHarness(t, content)

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)

	newEnv := func(dir, tmuxLog, launchLog string, hasSession bool, extra ...string) []string {
		hs := "0"
		if hasSession {
			hs = "1"
		}
		base := []string{
			"TMUX_LOG=" + tmuxLog,
			"LAUNCH_LOG=" + launchLog,
			"TMUX_HAS_SESSION=" + hs,
			"STUB_FACTORY_ROOT=" + dir,
			"LITELLM_PORT=45998",
		}
		return hermeticCodexEnv(bin, append(base, extra...)...)
	}

	t.Run("mismatch_present_kills_then_launches", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".runtime", "gateway"), 0o755); err != nil {
			t.Fatal(err)
		}
		// Pre-seed a launch.json whose config_sha256 differs from the stub's --identity report.
		seeded := `{"v":1,"mode":"api-key","config_sha256":"OLD","litellm_version":"1.93.0","credential_ref":"ref","port":4000,"factory_root":"` + dir + `","launched_at":1,"pane_pid":"1"}`
		if err := os.WriteFile(filepath.Join(dir, ".runtime", "gateway", "launch.json"), []byte(seeded), 0o600); err != nil {
			t.Fatal(err)
		}
		tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
		launchLog := filepath.Join(t.TempDir(), "launch.log")
		relaunch := writeRelaunchStub(t, t.TempDir())

		script := harness + "\ncd " + shq(dir) + "\n_reconcile_gateway " + shq(relaunch) + "\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = newEnv(dir, tmuxLog, launchLog, true, "STUB_IDENTITY_SHA=NEW")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("_reconcile_gateway (mismatch) failed: %v\n%s", err, out)
		}
		killLog, _ := os.ReadFile(tmuxLog)
		if !strings.Contains(string(killLog), "kill-session") {
			t.Errorf("expected a kill-session call on identity mismatch; tmux log:\n%s", killLog)
		}
		launched, _ := os.ReadFile(launchLog)
		if strings.TrimSpace(string(launched)) == "" {
			t.Error("expected a no-arg relaunch invocation after the kill; launch log is empty")
		}
	})

	t.Run("equal_present_no_restart", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".runtime", "gateway"), 0o755); err != nil {
			t.Fatal(err)
		}
		seeded := `{"v":1,"mode":"api-key","config_sha256":"SAME","litellm_version":"1.93.0","credential_ref":"ref","port":4000,"factory_root":"` + dir + `"}`
		if err := os.WriteFile(filepath.Join(dir, ".runtime", "gateway", "launch.json"), []byte(seeded), 0o600); err != nil {
			t.Fatal(err)
		}
		tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
		launchLog := filepath.Join(t.TempDir(), "launch.log")
		relaunch := writeRelaunchStub(t, t.TempDir())

		script := harness + "\ncd " + shq(dir) + "\n_reconcile_gateway " + shq(relaunch) + "\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = newEnv(dir, tmuxLog, launchLog, true, "STUB_IDENTITY_SHA=SAME")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("_reconcile_gateway (equal) failed: %v\n%s", err, out)
		}
		killLog, _ := os.ReadFile(tmuxLog)
		if strings.Contains(string(killLog), "kill-session") {
			t.Errorf("no kill-session expected when identity matches; tmux log:\n%s", killLog)
		}
		launched, _ := os.ReadFile(launchLog)
		if strings.TrimSpace(string(launched)) != "" {
			t.Errorf("no relaunch expected when identity matches; launch log:\n%s", launched)
		}
	})

	// design-doc.md:175 "Missing/unparseable ⇒ unknown ⇒ restart": an empty credential_ref means the
	// credential's version could not be read, so two empties are not evidence of the same credential.
	t.Run("unknown_credential_ref_restarts_even_when_record_matches", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".runtime", "gateway"), 0o755); err != nil {
			t.Fatal(err)
		}
		seeded := `{"v":1,"mode":"api-key","config_sha256":"SAME","litellm_version":"1.93.0","credential_ref":"","port":4000,"factory_root":"` + dir + `"}`
		if err := os.WriteFile(filepath.Join(dir, ".runtime", "gateway", "launch.json"), []byte(seeded), 0o600); err != nil {
			t.Fatal(err)
		}
		tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
		launchLog := filepath.Join(t.TempDir(), "launch.log")
		relaunch := writeRelaunchStub(t, t.TempDir())

		script := harness + "\ncd " + shq(dir) + "\n_reconcile_gateway " + shq(relaunch) + "\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = newEnv(dir, tmuxLog, launchLog, true, "STUB_IDENTITY_SHA=SAME", "STUB_CREDENTIAL_REF=")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("_reconcile_gateway (unknown credential_ref) failed: %v\n%s", err, out)
		}
		killLog, _ := os.ReadFile(tmuxLog)
		if !strings.Contains(string(killLog), "kill-session") {
			t.Errorf("an empty (unknown) credential_ref compared equal and kept the running gateway; a missing credential source must restart. tmux log:\n%s", killLog)
		}
		if !strings.Contains(string(out), "credential") {
			t.Errorf("the restart on an unknown credential_ref must say why; output:\n%s", out)
		}
	})

	t.Run("no_record_present_treated_as_mismatch", func(t *testing.T) {
		dir := t.TempDir()
		tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
		launchLog := filepath.Join(t.TempDir(), "launch.log")
		relaunch := writeRelaunchStub(t, t.TempDir())

		script := harness + "\ncd " + shq(dir) + "\n_reconcile_gateway " + shq(relaunch) + "\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = newEnv(dir, tmuxLog, launchLog, true, "STUB_IDENTITY_SHA=NEW")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("_reconcile_gateway (no record) failed: %v\n%s", err, out)
		}
		killLog, _ := os.ReadFile(tmuxLog)
		if !strings.Contains(string(killLog), "kill-session") {
			t.Errorf("expected a kill-session call when no launch.json record exists (treated as unequal/unreadable); tmux log:\n%s", killLog)
		}
	})

	t.Run("fresh_bring_up_launches_without_kill", func(t *testing.T) {
		dir := t.TempDir()
		tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
		launchLog := filepath.Join(t.TempDir(), "launch.log")
		relaunch := writeRelaunchStub(t, t.TempDir())

		script := harness + "\ncd " + shq(dir) + "\n_reconcile_gateway " + shq(relaunch) + "\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = newEnv(dir, tmuxLog, launchLog, false, "STUB_IDENTITY_SHA=NEW")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("_reconcile_gateway (fresh bring-up) failed: %v\n%s", err, out)
		}
		killLog, _ := os.ReadFile(tmuxLog)
		if strings.Contains(string(killLog), "kill-session") {
			t.Errorf("no kill-session expected on a fresh bring-up (no prior session); tmux log:\n%s", killLog)
		}
		launched, _ := os.ReadFile(launchLog)
		if strings.TrimSpace(string(launched)) == "" {
			t.Error("expected a launch on fresh bring-up with the port free; launch log is empty")
		}
	})
}

// TestQuickstartReconcileRefusesForeignPort is T-7 (concern_tests.md §2): session absent AND the
// port occupied by a non-litellm process must refuse (exit 1, naming the port), never launch.
// Binds a REAL listener from the Go test itself so the real, unstubbed _port_in_use is exercised.
func TestQuickstartReconcileRefusesForeignPort(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := reconcileHarness(t, content)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("binding a real ephemeral listener: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)

	dir := t.TempDir()
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	launchLog := filepath.Join(t.TempDir(), "launch.log")
	relaunch := writeRelaunchStub(t, t.TempDir())

	script := harness + "\ncd " + shq(dir) + "\n_reconcile_gateway " + shq(relaunch) + "\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin,
		"TMUX_LOG="+tmuxLog,
		"LAUNCH_LOG="+launchLog,
		"TMUX_HAS_SESSION=0",
		"STUB_FACTORY_ROOT="+dir,
		"LITELLM_PORT="+strconv.Itoa(port),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected _reconcile_gateway to exit non-zero when the port is foreign-occupied; got exit 0, output:\n%s", out)
	}
	if !strings.Contains(string(out), strconv.Itoa(port)) {
		t.Errorf("expected the refusal to name the occupied port %d; output:\n%s", port, out)
	}
	launched, _ := os.ReadFile(launchLog)
	if strings.TrimSpace(string(launched)) != "" {
		t.Errorf("no launch attempt expected when the port is foreign-occupied; launch log:\n%s", launched)
	}
	killLog, _ := os.ReadFile(tmuxLog)
	if strings.Contains(string(killLog), "kill-session") {
		t.Errorf("no kill-session expected when the session is absent (nothing to kill); tmux log:\n%s", killLog)
	}
}

// TestQuickstartGatewayStopTrapComposesWithCleanup pins JC-8 (concern_decisions.md, cross-checked
// against quickstart.sh:27-34): _reconcile_gateway must extend the existing global
// `trap cleanup EXIT` rather than installing a second, competing trap — bash's trap slot per
// signal is last-write-wins, not a stack, so a second bare `trap … EXIT` would silently disable
// setup_telemetry's own tmpdir cleanup on every run that reaches setup_litellm.
func TestQuickstartGatewayStopTrapComposesWithCleanup(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := reconcileHarness(t, content)

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)

	dir := t.TempDir()
	sentinel := filepath.Join(t.TempDir(), "sentinel-cleanup-dir")
	if err := os.MkdirAll(sentinel, 0o755); err != nil {
		t.Fatal(err)
	}
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	launchLog := filepath.Join(t.TempDir(), "launch.log")
	relaunch := writeRelaunchStub(t, t.TempDir())

	script := harness +
		"\nCLEANUP_DIRS+=(" + shq(sentinel) + ")\n" +
		"cd " + shq(dir) + "\n_reconcile_gateway " + shq(relaunch) + "\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin,
		"TMUX_LOG="+tmuxLog,
		"LAUNCH_LOG="+launchLog,
		"TMUX_HAS_SESSION=0",
		"STUB_FACTORY_ROOT="+dir,
		"LITELLM_PORT=45997",
		"STUB_IDENTITY_SHA=NEW",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("_reconcile_gateway failed: %v\n%s", err, out)
	}
	if _, statErr := os.Stat(sentinel); !os.IsNotExist(statErr) {
		t.Errorf("sentinel CLEANUP_DIRS entry %s still exists after the script exited — "+
			"_reconcile_gateway's EXIT trap clobbered the global `trap cleanup EXIT` instead of composing with it (JC-8)", sentinel)
	}
}
