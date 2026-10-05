package cmd

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"testing"
	"time"
)

// reconcilePinExitCode extracts a process exit code from cmd.Wait()'s error (0 on nil).
func reconcilePinExitCode(err error) int {
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	return -1
}

// trapCleanupLineRe matches the top-level `trap … cleanup …` installation(s). At head it is the
// single `trap cleanup EXIT TERM`; the fix splits TERM into its own exiting handler, so matching by
// pattern (not a literal) keeps this pin honest across the fix.
var trapCleanupLineRe = regexp.MustCompile(`(?m)^trap .*cleanup.*$`)

// TestQuickstartTermTrapExits143 is T5 (red_predictions.md T5 / concern_tests.md §T5): a SIGTERM
// during the bootstrap must run cleanup and then EXIT 143 — it must not clear the reconcile marker
// and resume as if nothing happened. Installs the trap exactly as quickstart.sh does, blocks on a
// backgrounded sleep, sends SIGTERM, and asserts the process exits 143 without reaching the
// post-signal sentinel.
//
// RED at head: `trap cleanup EXIT TERM` runs cleanup then RESUMES, so the sentinel prints and the
// process exits 0. GREEN once TERM gets an exiting handler (`trap 'cleanup; exit 143' TERM`).
func TestQuickstartTermTrapExits143(t *testing.T) {
	content := quickstartScriptContent(t)
	cleanup := extractShellFunction(content, "cleanup")
	if cleanup == "" {
		t.Fatal("could not extract cleanup() from quickstart.sh")
	}
	trapLines := trapCleanupLineRe.FindAllString(content, -1)
	if len(trapLines) == 0 {
		t.Fatal("could not find the top-level `trap … cleanup …` installation in quickstart.sh")
	}

	bin := hermeticGatewayBinDir(t)

	dir := t.TempDir()
	readyFile := filepath.Join(t.TempDir(), "ready")

	script := "set -uo pipefail\n" +
		"CLEANUP_DIRS=()\n" +
		cleanup + "\n" +
		strings.Join(trapLines, "\n") + "\n" +
		"cd " + shq(dir) + "\n" +
		": > " + shq(readyFile) + "\n" +
		"sleep 30 >/dev/null 2>&1 &\n" +
		"wait $!\n" +
		"echo CONTINUED\n"

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the TERM-trap harness: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(readyFile); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatalf("harness never became ready; output:\n%s", buf.String())
		}
		time.Sleep(10 * time.Millisecond)
	}

	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatalf("sending SIGTERM: %v", err)
	}
	waitErr := cmd.Wait()

	if code := reconcilePinExitCode(waitErr); code != 143 {
		t.Errorf("a SIGTERM must run cleanup then exit 143, got exit code %d (T5: `trap cleanup EXIT TERM` resumes instead of exiting)", code)
	}
	if strings.Contains(buf.String(), "CONTINUED") {
		t.Errorf("execution resumed after SIGTERM (the post-signal sentinel printed) — the TERM handler must exit, not fall through (T5); output:\n%s", buf.String())
	}
}

// TestReconcileWiresPortOwnerPid is T7 (red_predictions.md T7 / concern_tests.md §T7):
// `_port_owner_pid` (quickstart.sh:983) exists solely to resolve the pid owning the gateway port,
// but has ZERO call sites — a foreign listener passes the reconcile's `tmux has-session`-only
// readiness check. Pin that the helper gains a caller so the run can fail loudly on a foreign owner.
//
// RED at head: the only occurrence of `_port_owner_pid` outside comments is its own definition.
func TestReconcileWiresPortOwnerPid(t *testing.T) {
	content := quickstartScriptContent(t)

	callSites := 0
	for _, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue // comment
		}
		if strings.HasPrefix(trimmed, "_port_owner_pid()") {
			continue // the definition itself
		}
		if strings.Contains(line, "_port_owner_pid") {
			callSites++
		}
	}
	if callSites == 0 {
		t.Errorf("_port_owner_pid has zero call sites — it is dead code; wire it into the reconcile so a foreign port owner fails loudly (log_error + exit 1) instead of passing the has-session-only check (T7)")
	}
}

// TestReconcileMalformedLaunchJSONDoesNotAbort is T8a (red_predictions.md T8 / concern_tests.md §T8):
// under the script's real `set -e`, the bare `recorded="$(jq …)"` (quickstart.sh:1051) aborts the
// whole bootstrap on jq's exit 5 for a malformed launch.json. It must instead treat the unreadable
// record as unknown and restart. Runs the reconcile under `set -e` (as quickstart.sh:2 does).
//
// RED at head: a malformed launch.json aborts the run before any restart.
func TestReconcileMalformedLaunchJSONDoesNotAbort(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := reconcileHarness(t, content)

	bin := hermeticGatewayBinDir(t)
	writeIdentityStubs(t, bin)

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".runtime", "gateway"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".runtime", "gateway", "launch.json"), []byte("{ this is not valid json"), 0o600); err != nil {
		t.Fatal(err)
	}
	relaunch := writeRelaunchStub(t, t.TempDir())
	tmuxLog := filepath.Join(t.TempDir(), "tmux.log")
	launchLog := filepath.Join(t.TempDir(), "launch.log")

	// `set -e` is activated right before the call so the reconcile runs under the same errexit the
	// real quickstart.sh (line 2) does — reconcileHarness itself only sets `-uo pipefail`.
	script := harness + "\ncd " + shq(dir) + "\nset -e\n_reconcile_gateway " + shq(relaunch) + "\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin,
		"TMUX_LOG="+tmuxLog,
		"LAUNCH_LOG="+launchLog,
		"TMUX_HAS_SESSION=1",
		"STUB_FACTORY_ROOT="+dir,
		"STUB_IDENTITY_SHA=NEW",
		"LITELLM_PORT=45998",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("_reconcile_gateway aborted on a malformed launch.json (jq exit 5 under set -e) instead of treating it as unknown and restarting (T8a): %v\n%s", err, out)
	}
	launched, _ := os.ReadFile(launchLog)
	if strings.TrimSpace(string(launched)) == "" {
		t.Errorf("_reconcile_gateway did not restart the gateway after an unreadable launch.json (T8a); launch log is empty\noutput:\n%s", out)
	}
}
