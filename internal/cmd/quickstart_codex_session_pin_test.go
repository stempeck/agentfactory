package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// seedCodexHome writes an auth.json under a fresh $CODEX_HOME and returns the home path.
func seedCodexHome(t *testing.T, authJSON string) string {
	t.Helper()
	home := filepath.Join(t.TempDir(), ".codex")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir codex home: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, "auth.json"), []byte(authJSON), 0o600); err != nil {
		t.Fatalf("writing codex auth.json: %v", err)
	}
	return home
}

// TestCodexSessionValidIgnoresStatusProse is T14bash (red_predictions.md T14 / concern_tests.md
// §T14): `_codex_session_valid` must judge a session on exit code + auth_mode + refresh token, not
// on the human status prose. A session that is genuinely authenticated (status exits 0,
// auth_mode=chatgpt, a refresh token) but whose status wording differs from the hardcoded
// "Logged in using ChatGPT" must still read VALID.
//
// RED at head: quickstart.sh:565 greps the literal "Logged in using ChatGPT", so any wording change
// reads the live session as invalid.
func TestCodexSessionValidIgnoresStatusProse(t *testing.T) {
	harness := shellFnsFrom(t, "_codex_session_valid")

	bin := hermeticCodexBinDir(t)
	writeCodexStub(t, bin, "codex", `if [ "$1" = "login" ] && [ "$2" = "status" ]; then echo "Signed in with ChatGPT"; exit 0; fi
exit 0
`)
	codexHome := seedCodexHome(t, `{"auth_mode":"chatgpt","tokens":{"refresh_token":"rt-fake"}}`)

	script := harness + "\nif _codex_session_valid; then echo VALID; else echo INVALID; fi\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin, "CODEX_HOME="+codexHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("running _codex_session_valid harness failed: %v\n%s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "VALID" {
		t.Errorf("_codex_session_valid must read a session with exit0 + auth_mode=chatgpt + refresh_token as VALID regardless of status prose (T14bash: it greps the literal \"Logged in using ChatGPT\"); got %q\noutput:\n%s", got, out)
	}
}

// TestCodexSessionValidMalformedAuthReturnsNonZero is T8b (red_predictions.md T8 / concern_tests.md
// §T8): the two bare `$(jq …)` reads in `_codex_session_valid` (quickstart.sh:575,580) must fall
// back to a clean invalid return on malformed JSON, not abort the whole bootstrap on jq's exit 5.
// Called bare under the script's real `set -e`.
//
// RED at head: a malformed auth.json aborts at the jq read (exit 5) instead of returning 1.
func TestCodexSessionValidMalformedAuthReturnsNonZero(t *testing.T) {
	harness := shellFnsFrom(t, "_codex_session_valid")

	bin := hermeticCodexBinDir(t)
	// status passes the grep so execution reaches the jq reads.
	writeCodexStub(t, bin, "codex", `if [ "$1" = "login" ] && [ "$2" = "status" ]; then echo "Logged in using ChatGPT"; exit 0; fi
exit 0
`)
	codexHome := seedCodexHome(t, "{ this is not valid json")

	script := harness + "\n_codex_session_valid\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin, "CODEX_HOME="+codexHome)
	out, err := cmd.CombinedOutput()
	code := reconcilePinExitCode(err)
	if code == 5 {
		t.Errorf("_codex_session_valid aborted the script on a malformed auth.json (jq exit 5 under set -e) instead of returning non-zero (T8b); output:\n%s", out)
	}
	if code != 1 {
		t.Errorf("_codex_session_valid must return 1 (invalid) on a malformed auth.json, got exit code %d (T8b); output:\n%s", code, out)
	}
}

// TestEnsureCodexSessionImmediateFailureNotMaskedAsTimeout is T13 (red_predictions.md T13 /
// concern_tests.md §T13): `codex login --device-auth`'s non-timeout failures must not be masked by
// `|| true` and then misreported as a 15-minute timeout. An IMMEDIATE device-auth failure must be
// surfaced as the real failure, not a timeout that did not happen.
//
// RED at head: quickstart.sh:594 `… || true` swallows the exit, and :596 prints the 15-minute
// timeout message for any subsequent invalid session.
func TestEnsureCodexSessionImmediateFailureNotMaskedAsTimeout(t *testing.T) {
	harness := shellFnsFrom(t, "log_info", "log_success", "log_warn", "log_error", "_codex_session_valid", "_ensure_codex_session", "_run_with_timeout")

	bin := hermeticCodexBinDir(t)
	writeCodexStub(t, bin, "codex", `if [ "$1" = "login" ] && [ "$2" = "status" ]; then exit 1; fi
if [ "$1" = "login" ] && [ "$2" = "--device-auth" ]; then echo "device authorization failed: connection refused" >&2; exit 1; fi
exit 0
`)

	// log_info/log_error reference the color vars (${BLUE}/${NC}); shellFnsFrom extracts only the
	// named functions, so without these defs the harness aborts on `set -u` unbound-variable at the
	// first log line — before reaching the timeout mislabel — and the test would falsely pass. Same
	// `RED='' … NC=''` seam every other quickstart log_* harness uses.
	script := "RED='' GREEN='' YELLOW='' BLUE='' NC=''\n" + harness + "\n_ensure_codex_session\n"
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = hermeticCodexEnv(bin, "CODEX_LOGIN_TIMEOUT=2")
	out, _ := cmd.CombinedOutput() // _ensure_codex_session exits 1 on the (still) invalid session
	outStr := string(out)
	// The timeout mislabel is the sentinel "Codex login was not completed within 15 minutes"; match
	// that message specifically via "not completed within". The bare "15 minutes" substring also
	// appears in the benign pre-login info line ("the code expires in 15 minutes"), printed in every
	// run regardless of the fix, so it cannot distinguish the bug and would keep this pin red forever.
	if strings.Contains(strings.ToLower(outStr), "not completed within") {
		t.Errorf("an IMMEDIATE `codex login --device-auth` failure (exit 1, not a timeout) must not be reported as a 15-minute timeout — `|| true` masks the real exit (T13); output:\n%s", outStr)
	}
}

// TestRunWithTimeoutKeepsTheTimeoutContract pins _run_with_timeout to the coreutils timeout(1)
// contract _ensure_codex_session's 124 branch reads: a command that finishes in time returns its own
// exit code, one still running at the deadline is terminated and reported as 124. Elapsed time is
// the termination proof: the helper returns only once the command has exited.
func TestRunWithTimeoutKeepsTheTimeoutContract(t *testing.T) {
	harness := shellFnsFrom(t, "_run_with_timeout")
	bin := hermeticCodexBinDir(t)
	writeCodexStub(t, bin, "exits-3", "exit 3\n")

	cases := []struct {
		name   string
		call   string
		want   int
		within time.Duration
	}{
		{"finishes in time", "_run_with_timeout 5 exits-3", 3, 4 * time.Second},
		{"still running at the deadline", "_run_with_timeout 1 sleep 30", 124, 10 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-c", harness+"rc=0\n"+tc.call+" || rc=$?\nexit \"$rc\"\n")
			cmd.Env = hermeticCodexEnv(bin)
			start := time.Now()
			out, err := cmd.CombinedOutput()
			elapsed := time.Since(start)
			if got := reconcilePinExitCode(err); got != tc.want {
				t.Errorf("exit = %d, want %d; output:\n%s", got, tc.want, out)
			}
			if elapsed > tc.within {
				t.Errorf("took %s, want under %s: the command outlived its deadline or the watchdog held the output open", elapsed, tc.within)
			}
			if len(out) != 0 {
				t.Errorf("want no output — bash 3.2 announces every signal-ended job (\"Terminated: 15\") on the waiting shell's stderr; got:\n%s", out)
			}
		})
	}
}

// TestCodexSessionPredicatesAgreeOnAuthShapes pins the Go codexSessionValid and the bash
// _codex_session_valid to one verdict per auth.json shape. The preflight trusts the Go answer to
// skip the pre-teardown login, and the bootstrap then re-asks the bash one with agents already down,
// so any disagreement forces a device-auth login at the worst moment. An absent/null/empty
// auth_mode with a refresh token is VALID on both sides, as import accepts it. `codex login status`
// is stubbed to exit 0 so only the auth.json shape varies.
func TestCodexSessionPredicatesAgreeOnAuthShapes(t *testing.T) {
	harness := shellFnsFrom(t, "_codex_session_valid")

	cases := []struct {
		name     string
		authJSON string
		want     bool
	}{
		{"auth_mode absent with refresh token", `{"tokens":{"refresh_token":"rt"}}`, true},
		{"auth_mode null with refresh token", `{"auth_mode":null,"tokens":{"refresh_token":"rt"}}`, true},
		{"auth_mode empty with refresh token", `{"auth_mode":"","tokens":{"refresh_token":"rt"}}`, true},
		{"auth_mode chatgpt", `{"auth_mode":"chatgpt","tokens":{"refresh_token":"rt"}}`, true},
		{"auth_mode chatgptAuthTokens any case", `{"auth_mode":"ChatGPTAuthTokens","tokens":{"refresh_token":"rt"}}`, true},
		{"auth_mode apikey", `{"auth_mode":"apikey","tokens":{"refresh_token":"rt"}}`, false},
		{"auth_mode false", `{"auth_mode":false,"tokens":{"refresh_token":"rt"}}`, false},
		{"auth_mode number", `{"auth_mode":5,"tokens":{"refresh_token":"rt"}}`, false},
		{"no auth_mode no tokens", `{"OPENAI_API_KEY":"sk"}`, false},
		{"empty tokens", `{"tokens":{}}`, false},
		{"malformed json", `{ not json`, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			codexHome := seedCodexHome(t, tc.authJSON)

			t.Setenv("CODEX_HOME", codexHome)
			goValid := codexSessionValid()

			bin := hermeticCodexBinDir(t)
			writeCodexStub(t, bin, "codex", `if [ "$1" = "login" ] && [ "$2" = "status" ]; then echo "Logged in using ChatGPT"; exit 0; fi
exit 0
`)
			script := harness + "\nif _codex_session_valid; then echo VALID; else echo INVALID; fi\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = hermeticCodexEnv(bin, "CODEX_HOME="+codexHome)
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("running _codex_session_valid harness failed: %v\n%s", err, out)
			}
			bashValid := strings.TrimSpace(string(out)) == "VALID"

			if goValid != bashValid {
				t.Errorf("Go codexSessionValid = %v but bash _codex_session_valid = %v for %s; the twins must agree", goValid, bashValid, tc.authJSON)
			}
			if goValid != tc.want || bashValid != tc.want {
				t.Errorf("verdict for %s: go=%v bash=%v, want %v", tc.authJSON, goValid, bashValid, tc.want)
			}
		})
	}
}

// TestEnsureCodexSessionSkipsLoginForTokensWithoutAuthMode pins the impact end of the twin
// disagreement: a session the Go preflight already accepted (refresh token, no auth_mode) must not
// send the bootstrap into `codex login --device-auth`.
func TestEnsureCodexSessionSkipsLoginForTokensWithoutAuthMode(t *testing.T) {
	harness := codexFnsHarness(t, quickstartScriptContent(t))

	bin := hermeticCodexBinDir(t)
	codexLog := filepath.Join(t.TempDir(), "codex.log")
	writeCodexStub(t, bin, "codex", `printf '%s\n' "$*" >> "$CODEX_LOG"
if [ "$1" = login ] && [ "$2" = status ]; then echo "Logged in using ChatGPT"; exit 0; fi
exit 0
`)
	codexHome := seedCodexHome(t, `{"tokens":{"refresh_token":"rt-abc123"}}`)

	cmd := exec.Command("bash", "-c", harness+"\n_ensure_codex_session\n")
	cmd.Env = hermeticCodexEnv(bin, "CODEX_LOG="+codexLog, "CODEX_HOME="+codexHome)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Errorf("_ensure_codex_session failed on a session the Go preflight accepts: %v\n%s", err, out)
	}
	calls, readErr := os.ReadFile(codexLog)
	if readErr != nil {
		t.Fatalf("reading codex log: %v", readErr)
	}
	if strings.Contains(string(calls), "login --device-auth") {
		t.Errorf("_ensure_codex_session ran `codex login --device-auth` for a refresh-token session without auth_mode; codex log:\n%s", calls)
	}
}
