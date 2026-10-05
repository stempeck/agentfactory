package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Phase 5 (K14, issue #693) pinning tests for the bootstrap-flow half of
// IMPLREADME_PHASE5.md's Required change: a single-shot `--first` probe, a bounded
// device-code poll, and a once-only forced re-login ladder
// (`_ensure_codex_session` -> `af gateway auth import` -> `_reconcile_gateway` -> one
// more first probe) inserted into setup_litellm(), ahead of the untouched `--live` line.
//
// The ladder is factored into its own nested function, `_first_probe_bootstrap`, the same
// idiom `_gateway_stop`/`_reconcile_gateway` already established (Phase 4, K12) specifically
// so it could be extracted via extractShellFunction and stub-harness-tested in isolation —
// see TestFirstProbeBootstrap* below for the two design-doc function-harness ACs (stub-driven
// proof the ladder fires exactly once, and that only a THIRD device-code-poll response
// triggers it). The occurrence-count tests below additionally pin the verbatim ladder text.

// TestQuickstartBootstrapUsesFirstProbe is the outline's own mechanical AC #1:
// `grep -c -- '--first' quickstart.sh` must be >= 1.
func TestQuickstartBootstrapUsesFirstProbe(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	if !strings.Contains(setup, "--first") {
		t.Error("setup_litellm() does not invoke the new single-shot `--first` probe anywhere")
	}
}

// TestQuickstartPollsDeviceCodeRequested is the outline's own mechanical AC #2:
// `grep -c 'device_code_requested' quickstart.sh` must be >= 1.
func TestQuickstartPollsDeviceCodeRequested(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	if !strings.Contains(setup, "device_code_requested") {
		t.Error("setup_litellm() does not poll for device_code_requested anywhere")
	}
}

// TestQuickstartForcedReloginCallsEnsureCodexSessionTwice pins the verbatim ladder text
// (IMPLREADME_PHASE5.md L274 / design-doc.md L62-63): the forced re-login re-invokes
// _ensure_codex_session. Today it is called exactly once, in the initial
// LITELLM_AUTH_MODE=codex-subscription branch — the ladder's own call is a SECOND site.
func TestQuickstartForcedReloginCallsEnsureCodexSessionTwice(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	got := strings.Count(setup, "_ensure_codex_session")
	if got < 2 {
		t.Errorf("setup_litellm() calls _ensure_codex_session %d time(s); want >= 2 (the existing initial-login call plus the forced-relogin ladder's own call)", got)
	}
}

// TestQuickstartForcedReloginCallsGatewayAuthImportTwice pins the same ladder's
// `af gateway auth import` step. Today it is called exactly once (:1151, the initial
// subscription branch) — the ladder's own call is a SECOND site.
func TestQuickstartForcedReloginCallsGatewayAuthImportTwice(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	// Matches only the real invocation ("af gateway auth import;") — a doc comment two
	// lines above the existing call site also names the command in backticks with no
	// trailing semicolon, so a bare substring match would double-count that prose.
	got := strings.Count(setup, "af gateway auth import;")
	if got < 2 {
		t.Errorf("setup_litellm() calls `af gateway auth import` %d time(s); want >= 2 (the existing initial-login call plus the forced-relogin ladder's own call)", got)
	}
}

// TestQuickstartForcedReloginCallsReconcileGatewayTwice pins the same ladder's
// `_reconcile_gateway` step. Today it is called exactly once (:1423) — the ladder's own
// call, after `af gateway auth import`, is a SECOND site.
func TestQuickstartForcedReloginCallsReconcileGatewayTwice(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	// Matches only the real call ("_reconcile_gateway \"$relaunch_script\"") — the
	// function's own doc comment above its definition names it as
	// "_reconcile_gateway <relaunch_script>", which a bare substring match would
	// double-count.
	got := strings.Count(setup, `_reconcile_gateway "$relaunch_script"`)
	if got < 2 {
		t.Errorf("setup_litellm() calls _reconcile_gateway %d time(s); want >= 2 (the existing readiness-poll predecessor plus the forced-relogin ladder's own call)", got)
	}
}

// --- AC2/AC3 (design-doc L449-451): function-harness proof of the forced-relogin ladder ------

// firstProbeBootstrapHarness extracts _first_probe_bootstrap and every function it calls
// (transitively) from the real quickstart.sh, plus the CODEX_LOGIN_TIMEOUT/LITELLM_AUTH_MODE
// globals those functions read — same idiom as codexFnsHarness/reconcileHarness. It runs the
// ladder under the script's REAL `set -euo pipefail` (quickstart.sh:2), not a slackened `-uo`
// (T11/D18): a `-uo` harness certifies a ladder the real errexit script cannot reach, because the
// bare `_first_out="$(… --first …)"` split-assignment aborts at the assignment under `-e` before
// the branch runs. The `sleep` no-op keeps the bounded poll's `sleep 2` calls at zero wall-clock.
func firstProbeBootstrapHarness(t *testing.T, content string) string {
	t.Helper()
	names := []string{"command_exists", "log_step", "log_info", "log_success", "log_warn", "log_error",
		"_port_in_use", "_port_owner_pid", "_codex_session_valid", "_ensure_codex_session", "_run_with_timeout",
		"_gateway_stop", "_reconcile_gateway", "_first_probe_bootstrap"}
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("RED='' GREEN='' YELLOW='' BLUE='' NC=''\n")
	b.WriteString("CODEX_LOGIN_TIMEOUT=900\n")
	b.WriteString("LITELLM_AUTH_MODE=codex-subscription\n")
	b.WriteString("sleep() { return 0; }\n")
	for _, n := range names {
		fn := extractShellFunction(content, n)
		if fn == "" {
			t.Fatalf("could not extract %s() from quickstart.sh", n)
		}
		b.WriteString(fn)
		b.WriteString("\n")
	}
	return b.String()
}

// writeCodexAlreadySignedInStub stubs `codex` so `_codex_session_valid` (called by
// `_ensure_codex_session`) reports a valid session immediately — this harness exists to prove
// _first_probe_bootstrap's ORCHESTRATION of _ensure_codex_session, not _ensure_codex_session's
// own internal login-wait behavior (already pinned separately by T-2/T-3/T-4 in
// quickstart_litellm_auth_shape_test.go). Every invocation is appended to CODEX_CALL_LOG so
// tests can assert whether _ensure_codex_session ran at all.
func writeCodexAlreadySignedInStub(t *testing.T, bin string) {
	writeCodexStub(t, bin, "codex", `printf 'codex %s\n' "$*" >> "${CODEX_CALL_LOG:-/dev/null}"
if [ "$1" = "login" ] && [ "$2" = "status" ]; then
  echo "Logged in using ChatGPT"
  exit 0
fi
exit 0
`)
}

// writeCodexAuthJSON seeds a CODEX_HOME/auth.json that _codex_session_valid's jq reads as a
// valid ChatGPT-subscription session (auth_mode + a non-empty refresh_token).
func writeCodexAuthJSON(t *testing.T, codexHome string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"),
		[]byte(`{"auth_mode":"chatgpt","tokens":{"refresh_token":"rt-fake"}}`), 0o600); err != nil {
		t.Fatalf("writing fake auth.json: %v", err)
	}
}

// writeFirstProbeAfStub stubs `af` for the three subcommands _first_probe_bootstrap calls.
// Behavior is entirely env-var driven so each test case configures its own scenario:
//   - `config models check <profile> --first`: fails (exit 1, stderr text from
//     AF_FIRST_FAIL_TEXT) for the first AF_FIRST_FAIL_COUNT calls, succeeds after; every call
//     appends a line to AF_FIRST_LOG and increments the counter at FIRST_COUNT_FILE.
//   - `gateway auth status --json`: reports device_code_requested:true starting at call
//     number AF_STATUS_TRUE_AT (1-based; never if unset/huge); every call appends to
//     AF_STATUS_LOG and increments STATUS_COUNT_FILE.
//   - `gateway auth import`: fails iff AF_IMPORT_FAIL=1; every call appends to AF_IMPORT_LOG.
func writeFirstProbeAfStub(t *testing.T, bin string) {
	writeCodexStub(t, bin, "af", `if [ "$1" = "config" ] && [ "$2" = "models" ] && [ "$3" = "check" ]; then
  flag="${5:-}"
  if [ "$flag" = "--first" ]; then
    profile="$4"
    cnt_file="${FIRST_COUNT_FILE:-/dev/null}"
    n=$(( $(cat "$cnt_file" 2>/dev/null || echo 0) + 1 ))
    printf '%s' "$n" > "$cnt_file"
    [ -n "${AF_FIRST_LOG:-}" ] && printf 'call %s profile=%s\n' "$n" "$profile" >> "$AF_FIRST_LOG"
    if [ "$n" -le "${AF_FIRST_FAIL_COUNT:-0}" ]; then
      printf '%s\n' "${AF_FIRST_FAIL_TEXT:-profile \"$profile\": --first → AUTH — POST /v1/messages returned 401 Unauthorized}" >&2
      exit 1
    fi
    echo "profile \"$profile\": --first -> OK"
    exit 0
  fi
  exit 0
fi
if [ "$1" = "gateway" ] && [ "$2" = "auth" ] && [ "$3" = "status" ]; then
  cnt_file="${STATUS_COUNT_FILE:-/dev/null}"
  n=$(( $(cat "$cnt_file" 2>/dev/null || echo 0) + 1 ))
  printf '%s' "$n" > "$cnt_file"
  [ -n "${AF_STATUS_LOG:-}" ] && printf 'call %s\n' "$n" >> "$AF_STATUS_LOG"
  if [ "$n" -ge "${AF_STATUS_TRUE_AT:-999999}" ]; then
    printf '{"device_code_requested":true}'
  else
    printf '{"device_code_requested":false}'
  fi
  exit 0
fi
if [ "$1" = "gateway" ] && [ "$2" = "auth" ] && [ "$3" = "import" ]; then
  [ -n "${AF_IMPORT_LOG:-}" ] && printf 'import\n' >> "$AF_IMPORT_LOG"
  [ "${AF_IMPORT_FAIL:-0}" = "1" ] && exit 1
  exit 0
fi
exit 0
`)
}

// readLineCount returns the number of non-empty lines in path, or 0 if it does not exist.
func readLineCount(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n := 0
	for _, line := range strings.Split(strings.TrimRight(string(data), "\n"), "\n") {
		if line != "" {
			n++
		}
	}
	return n
}

// firstProbeBootstrapEnv assembles the env for one _first_probe_bootstrap harness run: PATH
// exclusively the hermetic stub bin dir, CODEX_HOME pointed at a fake, already-signed-in
// session, plus whatever per-scenario AF_*/*_LOG/*_COUNT_FILE vars the caller adds.
func firstProbeBootstrapEnv(bin, codexHome string, extra ...string) []string {
	return hermeticCodexEnv(bin, append([]string{"CODEX_HOME=" + codexHome}, extra...)...)
}

// TestFirstProbeBootstrapAuthVerdictTriggersOnceOnlyRelogin is AC2 (design-doc L450): a stub
// `--first` auth verdict + a stub `status --json` reporting device_code_requested immediately
// must force _ensure_codex_session, `af gateway auth import`, _reconcile_gateway, and one more
// `--first` probe — and a second sub-test proves the reprobe failing does NOT trigger a second
// login ("no further login on a second failure").
func TestFirstProbeBootstrapAuthVerdictTriggersOnceOnlyRelogin(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := firstProbeBootstrapHarness(t, content)

	run := func(t *testing.T, failCount int) (exitErr error, out []byte, firstLog, statusLog, importLog, codexLog string) {
		bin := hermeticGatewayBinDir(t)
		writeIdentityStubs(t, bin)
		writeCodexAlreadySignedInStub(t, bin)
		writeFirstProbeAfStub(t, bin)

		dir := t.TempDir()
		codexHome := t.TempDir()
		writeCodexAuthJSON(t, codexHome)
		relaunch := writeRelaunchStub(t, t.TempDir())

		firstLog = filepath.Join(t.TempDir(), "first.log")
		statusLog = filepath.Join(t.TempDir(), "status.log")
		importLog = filepath.Join(t.TempDir(), "import.log")
		codexLog = filepath.Join(t.TempDir(), "codex.log")

		script := harness + "\ncd " + shq(dir) + "\n_first_probe_bootstrap codex-subscription " + shq(relaunch) + "\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = firstProbeBootstrapEnv(bin, codexHome,
			"LITELLM_PORT=45996",
			"TMUX_HAS_SESSION=0",
			"STUB_FACTORY_ROOT="+dir,
			"AF_FIRST_FAIL_COUNT="+strconv.Itoa(failCount),
			"AF_FIRST_LOG="+firstLog,
			"AF_STATUS_LOG="+statusLog,
			"AF_STATUS_TRUE_AT=1",
			"AF_IMPORT_LOG="+importLog,
			"FIRST_COUNT_FILE="+filepath.Join(t.TempDir(), "first.count"),
			"STATUS_COUNT_FILE="+filepath.Join(t.TempDir(), "status.count"),
			"CODEX_CALL_LOG="+codexLog,
		)
		out, exitErr = cmd.CombinedOutput()
		return exitErr, out, firstLog, statusLog, importLog, codexLog
	}

	t.Run("relogin_fires_and_reprobes", func(t *testing.T) {
		exitErr, out, firstLog, statusLog, importLog, codexLog := run(t, 1)
		if exitErr != nil {
			t.Fatalf("_first_probe_bootstrap failed: %v\n%s", exitErr, out)
		}
		if got := readLineCount(t, firstLog); got != 2 {
			t.Errorf("--first called %d time(s), want 2 (initial auth failure + the ladder's reprobe)", got)
		}
		if readLineCount(t, statusLog) < 1 {
			t.Error("gateway auth status --json was never polled")
		}
		if readLineCount(t, importLog) != 1 {
			t.Errorf("af gateway auth import called %d time(s), want exactly 1", readLineCount(t, importLog))
		}
		if readLineCount(t, codexLog) < 1 {
			t.Error("_ensure_codex_session never invoked codex (no forced re-login observed)")
		}
	})

	t.Run("reprobe_failure_ends_loud_without_retriggering_login", func(t *testing.T) {
		exitErr, out, firstLog, _, importLog, codexLog := run(t, 2)
		if code := reconcilePinExitCode(exitErr); code != 1 {
			t.Errorf("a reprobe that still fails after the forced re-login must exit 1, got %d; output:\n%s", code, out)
		}
		if !strings.Contains(string(out), "--first → AUTH — POST /v1/messages returned 401 Unauthorized") {
			t.Errorf("the failed reprobe's own verdict must reach the operator verbatim; output:\n%s", out)
		}
		if got := readLineCount(t, firstLog); got != 2 {
			t.Errorf("--first called %d time(s), want exactly 2 — a second failure on the reprobe must not trigger a THIRD --first call via a second login attempt", got)
		}
		if got := readLineCount(t, importLog); got != 1 {
			t.Errorf("af gateway auth import called %d time(s), want exactly 1 — no further login on a second failure", got)
		}
		if got := readLineCount(t, codexLog); got != 1 {
			t.Errorf("codex invoked %d time(s) by _ensure_codex_session, want exactly 1 — no further login on a second failure", got)
		}
	})
}

// TestFirstProbeBootstrapNonAuthVerdictExitsWithoutLogin is AC2's other half (design-doc L450)
// under T6/D10: a verdict that is neither AUTH nor TIMEOUT must exit 1 without ever touching the
// login path. The stubbed verdict line is UNEXPECTED yet deliberately carries the substring
// "AUTH" (ANTHROPIC_AUTH_TOKEN in the error tail) — the token-anchored ladder
// (`grep -qE -- '--first → (AUTH|TIMEOUT)'`) must NOT treat that stray substring as an auth
// verdict, the exact false-positive the old `grep -qi 'auth'` matched.
func TestFirstProbeBootstrapNonAuthVerdictExitsWithoutLogin(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := firstProbeBootstrapHarness(t, content)

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)
	writeCodexAlreadySignedInStub(t, bin)
	writeFirstProbeAfStub(t, bin)

	dir := t.TempDir()
	codexHome := t.TempDir()
	writeCodexAuthJSON(t, codexHome)
	relaunch := writeRelaunchStub(t, t.TempDir())
	importLog := filepath.Join(t.TempDir(), "import.log")
	codexLog := filepath.Join(t.TempDir(), "codex.log")
	statusLog := filepath.Join(t.TempDir(), "status.log")

	script := harness + "\ncd " + shq(dir) + "\n_first_probe_bootstrap codex-subscription " + shq(relaunch) + "\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = firstProbeBootstrapEnv(bin, codexHome,
		"LITELLM_PORT=45995",
		"TMUX_HAS_SESSION=0",
		"STUB_FACTORY_ROOT="+dir,
		"AF_FIRST_FAIL_COUNT=1",
		"AF_FIRST_FAIL_TEXT=profile \"codex-subscription\": --first → UNEXPECTED — POST /v1/messages returned 418; sent with ANTHROPIC_AUTH_TOKEN",
		"AF_IMPORT_LOG="+importLog,
		"AF_STATUS_LOG="+statusLog,
		"FIRST_COUNT_FILE="+filepath.Join(t.TempDir(), "first.count"),
		"STATUS_COUNT_FILE="+filepath.Join(t.TempDir(), "status.count"),
		"CODEX_CALL_LOG="+codexLog,
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected _first_probe_bootstrap to exit non-zero on a non-auth verdict; got exit 0, output:\n%s", out)
	}
	if readLineCount(t, statusLog) != 0 {
		t.Error("gateway auth status --json was polled on a non-auth verdict; the 'auth' substring in ANTHROPIC_AUTH_TOKEN must not trigger the poll (T6 false-positive)")
	}
	if readLineCount(t, importLog) != 0 {
		t.Error("af gateway auth import was called on a non-auth verdict; must exit without any login attempt")
	}
	if readLineCount(t, codexLog) != 0 {
		t.Error("codex was invoked (_ensure_codex_session ran) on a non-auth verdict; must exit without any login attempt")
	}
}

// TestFirstProbeBootstrapTimeoutVerdictTriggersRelogin pins T6/D10's added arm: a TIMEOUT
// verdict (the transport/deadline bucket) drives the same forced re-login an AUTH verdict does —
// the old `grep -qi 'auth'` dropped it because "TIMEOUT" carries no "auth" substring.
func TestFirstProbeBootstrapTimeoutVerdictTriggersRelogin(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := firstProbeBootstrapHarness(t, content)

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)
	writeCodexAlreadySignedInStub(t, bin)
	writeFirstProbeAfStub(t, bin)

	dir := t.TempDir()
	codexHome := t.TempDir()
	writeCodexAuthJSON(t, codexHome)
	relaunch := writeRelaunchStub(t, t.TempDir())
	firstLog := filepath.Join(t.TempDir(), "first.log")
	importLog := filepath.Join(t.TempDir(), "import.log")
	codexLog := filepath.Join(t.TempDir(), "codex.log")
	statusLog := filepath.Join(t.TempDir(), "status.log")

	script := harness + "\ncd " + shq(dir) + "\n_first_probe_bootstrap codex-subscription " + shq(relaunch) + "\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = firstProbeBootstrapEnv(bin, codexHome,
		"LITELLM_PORT=45992",
		"TMUX_HAS_SESSION=0",
		"STUB_FACTORY_ROOT="+dir,
		"AF_FIRST_FAIL_COUNT=1",
		"AF_FIRST_FAIL_TEXT=profile \"codex-subscription\": --first → TIMEOUT — POST /v1/messages: context deadline exceeded",
		"AF_FIRST_LOG="+firstLog,
		"AF_STATUS_LOG="+statusLog,
		"AF_STATUS_TRUE_AT=1",
		"AF_IMPORT_LOG="+importLog,
		"FIRST_COUNT_FILE="+filepath.Join(t.TempDir(), "first.count"),
		"STATUS_COUNT_FILE="+filepath.Join(t.TempDir(), "status.count"),
		"CODEX_CALL_LOG="+codexLog,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("_first_probe_bootstrap failed on a TIMEOUT verdict that should trigger re-login: %v\n%s", err, out)
	}
	if got := readLineCount(t, firstLog); got != 2 {
		t.Errorf("--first called %d time(s), want 2 (initial TIMEOUT failure + the ladder's reprobe)", got)
	}
	if readLineCount(t, statusLog) < 1 {
		t.Error("gateway auth status --json was never polled on a TIMEOUT verdict; T6/D10 requires TIMEOUT to trigger the re-login poll")
	}
	if got := readLineCount(t, importLog); got != 1 {
		t.Errorf("af gateway auth import called %d time(s), want exactly 1 — a TIMEOUT verdict must trigger the forced re-login", got)
	}
	if readLineCount(t, codexLog) < 1 {
		t.Error("_ensure_codex_session never invoked codex on a TIMEOUT verdict (no forced re-login observed)")
	}
}

// TestFirstProbeBootstrapPollTriggersOnThirdStatusCall is AC3's first half (design-doc L451):
// a stub `status --json` that reports device_code_requested:true only on its THIRD call must
// still trigger the forced re-login — proving the poll actually reads repeatedly, not once.
func TestFirstProbeBootstrapPollTriggersOnThirdStatusCall(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := firstProbeBootstrapHarness(t, content)

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)
	writeCodexAlreadySignedInStub(t, bin)
	writeFirstProbeAfStub(t, bin)

	dir := t.TempDir()
	codexHome := t.TempDir()
	writeCodexAuthJSON(t, codexHome)
	relaunch := writeRelaunchStub(t, t.TempDir())
	statusLog := filepath.Join(t.TempDir(), "status.log")
	importLog := filepath.Join(t.TempDir(), "import.log")

	script := harness + "\ncd " + shq(dir) + "\n_first_probe_bootstrap codex-subscription " + shq(relaunch) + "\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = firstProbeBootstrapEnv(bin, codexHome,
		"LITELLM_PORT=45994",
		"TMUX_HAS_SESSION=0",
		"STUB_FACTORY_ROOT="+dir,
		"AF_FIRST_FAIL_COUNT=1",
		"AF_STATUS_LOG="+statusLog,
		"AF_STATUS_TRUE_AT=3",
		"AF_IMPORT_LOG="+importLog,
		"FIRST_COUNT_FILE="+filepath.Join(t.TempDir(), "first.count"),
		"STATUS_COUNT_FILE="+filepath.Join(t.TempDir(), "status.count"),
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("_first_probe_bootstrap failed: %v\n%s", err, out)
	}
	if got := readLineCount(t, statusLog); got != 3 {
		t.Errorf("gateway auth status --json polled %d time(s), want exactly 3 (bounded poll, not a single read)", got)
	}
	if got := readLineCount(t, importLog); got != 1 {
		t.Errorf("af gateway auth import called %d time(s), want exactly 1 — the third-call true must still trigger the forced re-login", got)
	}
}

// TestFirstProbeBootstrapPollExhaustionEndsLoudWithoutLogin is AC3's other half (design-doc
// L451): a stub `status --json` that never reports device_code_requested must end the run
// loud (log_error naming the bound) and exit 1 — without ever attempting a login. The harness's
// `sleep` no-op (firstProbeBootstrapHarness) makes the poll's `sleep 2` calls cost zero
// wall-clock instead of 20 real seconds per run, and — being a real success under `set -euo
// pipefail` — cannot abort the loop, so the iteration count this test verifies is exact.
func TestFirstProbeBootstrapPollExhaustionEndsLoudWithoutLogin(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := firstProbeBootstrapHarness(t, content)

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)
	writeCodexAlreadySignedInStub(t, bin)
	writeFirstProbeAfStub(t, bin)

	dir := t.TempDir()
	codexHome := t.TempDir()
	writeCodexAuthJSON(t, codexHome)
	relaunch := writeRelaunchStub(t, t.TempDir())
	statusLog := filepath.Join(t.TempDir(), "status.log")
	importLog := filepath.Join(t.TempDir(), "import.log")
	codexLog := filepath.Join(t.TempDir(), "codex.log")

	script := harness + "\ncd " + shq(dir) + "\n_first_probe_bootstrap codex-subscription " + shq(relaunch) + "\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = firstProbeBootstrapEnv(bin, codexHome,
		"LITELLM_PORT=45993",
		"TMUX_HAS_SESSION=0",
		"STUB_FACTORY_ROOT="+dir,
		"AF_FIRST_FAIL_COUNT=1",
		"AF_STATUS_LOG="+statusLog,
		"AF_IMPORT_LOG="+importLog,
		"CODEX_CALL_LOG="+codexLog,
		"FIRST_COUNT_FILE="+filepath.Join(t.TempDir(), "first.count"),
		"STATUS_COUNT_FILE="+filepath.Join(t.TempDir(), "status.count"),
	)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("expected _first_probe_bootstrap to exit non-zero when device_code_requested is never reported; got exit 0, output:\n%s", out)
	}
	if !strings.Contains(string(out), "20s") {
		t.Errorf("expected the loud exit to name the 20s bound; output:\n%s", out)
	}
	if got := readLineCount(t, statusLog); got != 10 {
		t.Errorf("gateway auth status --json polled %d time(s), want exactly 10 (10 attempts x 2s = 20s bound)", got)
	}
	if readLineCount(t, importLog) != 0 {
		t.Error("af gateway auth import was called after poll exhaustion; must end without any login attempt")
	}
	if readLineCount(t, codexLog) != 0 {
		t.Error("codex was invoked after poll exhaustion; must end without any login attempt")
	}
}
