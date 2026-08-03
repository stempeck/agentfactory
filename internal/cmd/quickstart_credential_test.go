package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// openObservePasswordPolicy is the pinned backend's own rule, quoted from the panic it
// raises when the rule is broken:
//
//	"ZO_ROOT_USER_PASSWORD is too weak: Password must be 8-128 characters and contain at
//	 least one lowercase letter, one uppercase letter, one digit, and one special character."
//
// Encoded here rather than described, so the test fails for the reason the backend fails.
func openObservePasswordPolicy(pw string) []string {
	var missing []string
	if len(pw) < 8 || len(pw) > 128 {
		missing = append(missing, "length 8-128")
	}
	var lower, upper, digit, special bool
	for _, r := range pw {
		switch {
		case r >= 'a' && r <= 'z':
			lower = true
		case r >= 'A' && r <= 'Z':
			upper = true
		case r >= '0' && r <= '9':
			digit = true
		default:
			special = true
		}
	}
	if !lower {
		missing = append(missing, "lowercase letter")
	}
	if !upper {
		missing = append(missing, "uppercase letter")
	}
	if !digit {
		missing = append(missing, "digit")
	}
	if !special {
		missing = append(missing, "special character")
	}
	return missing
}

// TestTelemetryCredentialSatisfiesBackendPolicy EXECUTES the credential generation instead of
// grepping for it. That distinction is the whole finding: every shell-side acceptance check in
// this PR is text analysis of a script, never execution of it, which is why a credential that
// cannot start the backend passed CI.
//
// A hex string can never contain an uppercase letter or a special character, so the shipped
// generator fails the policy on every clean install, deterministically, in about two seconds —
// and the readiness poll then reports it as "not ready after 30s", which is how it was
// misdiagnosed as a slow cold start.
//
// This test runs in the DEFAULT lane on purpose. The readiness integration test carries a
// //go:build integration tag and does not run there, so a credential defect guarded only
// beside it would stay invisible to the lane that gates merges.
func TestTelemetryCredentialSatisfiesBackendPolicy(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	// The generator must be a named function so it can be executed in isolation. Extracting it
	// is also what proves the fix did not simply inline a stronger literal somewhere.
	gen := extractShellFunction(content, "_generate_telemetry_password")
	if gen == "" {
		t.Fatal("quickstart.sh has no _generate_telemetry_password() function to execute. " +
			"The credential generation must live in a named function so a test can run it " +
			"rather than pattern-match the script text")
	}
	// The generator checks its own output against the policy predicate, so both come along. That
	// dependency is deliberate: the generator and the repair path must ask the same question, and
	// a second copy of the rule is a second thing to get wrong.
	policy := extractShellFunction(content, "_telemetry_password_is_compliant")
	if policy == "" {
		t.Fatal("quickstart.sh has no _telemetry_password_is_compliant() function. The password " +
			"policy must be a predicate both the generator and the re-run repair path can call")
	}

	// Generate several times: the policy must hold for every draw, not for a lucky one. A
	// generator that satisfies the rule only probabilistically fails installs at random, which
	// is harder to diagnose than failing always.
	//
	// `set -euo pipefail` is prepended deliberately, and it is the whole reason this test is
	// trustworthy. quickstart.sh sets those options on line 2, and a generator can pass in a
	// permissive shell while aborting the real install: `tr < /dev/urandom | head -c N` looks
	// correct, but head closes the pipe, tr dies of SIGPIPE, pipefail promotes 141, and errexit
	// kills the script mid-write. A harness that omits the options cannot see that, and testing
	// under a shell the product never uses is the same asymmetry finding S4 is about.
	shellOpts := "set -euo pipefail\n"
	if !strings.Contains(content, "set -euo pipefail") {
		t.Fatal("quickstart.sh no longer sets `set -euo pipefail`; update this harness rather than " +
			"testing the generator under options the script does not use")
	}
	script := shellOpts + policy + "\n" + gen + "\nfor i in 1 2 3 4 5 6 7 8 9 10; do _generate_telemetry_password; echo; done"
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("executing _generate_telemetry_password under the script's own shell options "+
			"aborted (%v). A generator that only works in a permissive shell fails every real "+
			"install:\n%s", err, out)
	}

	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) != 10 {
		t.Fatalf("expected 10 generated credentials, got %d: %q", len(lines), out)
	}
	seen := make(map[string]bool, len(lines))
	for i, pw := range lines {
		pw = strings.TrimSpace(pw)
		if pw == "" {
			t.Errorf("draw %d produced an EMPTY credential: the generator wrote nothing, which on "+
				"the real call path means the operator's credential file is truncated", i+1)
			continue
		}
		if missing := openObservePasswordPolicy(pw); len(missing) > 0 {
			t.Errorf("draw %d (%d chars) fails the backend's password policy, missing: %s. "+
				"The backend panics on this at job init and exits 1, so the factory reports "+
				"'not ready' rather than 'crashed'", i+1, len(pw), strings.Join(missing, ", "))
		}
		if seen[pw] {
			t.Errorf("draw %d repeated an earlier credential: the generator is not random", i+1)
		}
		seen[pw] = true
	}
}

// TestReadinessPollDistinguishesCrashFromWarmup pins the second half of the same finding: no
// readiness window can rescue a process that has already exited. The shipped poll only asks
// "is the health endpoint answering yet", so a backend that died two seconds in is
// indistinguishable from one still warming up, and the operator waits out the entire window
// before being told something vague.
//
// The liveness check must be an OPTIONAL argument. The existing integration test extracts this
// same function and runs it standalone in a bare `bash -c` with no tmux session, so a
// hard-wired liveness call inside the function would turn a passing test red against a correct
// fix — the change has to be additive to the signature, not to the body's assumptions.
func TestReadinessPollDistinguishesCrashFromWarmup(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	awaitFn := extractShellFunction(string(data), "_await_telemetry_ready")
	if awaitFn == "" {
		t.Fatal("could not extract _await_telemetry_ready() from quickstart.sh")
	}

	// A URL that will never answer, a generous window, and a liveness probe that reports the
	// process as dead. A correct implementation returns promptly with a distinct code; the
	// shipped one ignores the third argument and burns the whole window.
	script := awaitFn + `
_dead_probe() { return 1; }
_await_telemetry_ready "http://127.0.0.1:1/healthz" 30 _dead_probe
echo "rc=$?"`

	start := time.Now()
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	elapsed := time.Since(start)
	_ = err // a non-zero exit is expected; the exit CODE is what we assert on

	rc := strings.TrimSpace(string(out))
	if !strings.Contains(rc, "rc=") {
		t.Fatalf("could not read the return code from the poll: %q", out)
	}

	// The window is 30s; a poll that detects a dead process must return in a small fraction of
	// it. Five seconds is loose enough to survive a slow CI box and tight enough that burning
	// the window still fails.
	if elapsed > 5*time.Second {
		t.Errorf("_await_telemetry_ready took %v with a liveness probe reporting the process "+
			"dead: a crashed backend must be reported at once, not after the full readiness "+
			"window. This is the 'long delay (or timeouts)' the operator made the default-on "+
			"install conditional on", elapsed)
	}

	// A distinct code is what lets the caller print "crashed" instead of "not ready after Ns".
	// rc=1 is the existing timeout code, so a crash must not reuse it.
	if strings.Contains(rc, "rc=1") {
		t.Errorf("a dead process returned the same code as a timeout (%s): the caller cannot "+
			"then tell 'crashed on startup' from 'still warming up', which is the whole finding", rc)
	}
	if strings.Contains(rc, "rc=0") {
		t.Errorf("a dead process returned success (%s)", rc)
	}
}

// TestReadinessCallerSurvivesErrexit guards the CALLER, under the shell options quickstart.sh
// actually sets. The test above verifies the readiness function in isolation and is blind to how
// its result is consumed — which is precisely where a regression landed once: a bare call followed
// by `case "$?"` reads correctly and, under `set -e`, aborts the script before the case is ever
// evaluated. An optional component's warning became a failed bootstrap, on the ordinary
// slow-cold-start path as well as the crash path.
//
// This is the S4 lesson applied one level up: testing the function and not the call site is the
// same asymmetry as testing the guard and not the guarded path.
func TestReadinessCallerSurvivesErrexit(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	// The options this script really runs under. If it ever stops setting them, this test's
	// premise is gone and it should be revisited rather than silently weakened.
	if !strings.Contains(content, "set -euo pipefail") {
		t.Fatal("quickstart.sh no longer sets `set -euo pipefail`; this test's premise no longer holds")
	}

	setup := extractShellFunction(content, "setup_telemetry")
	if setup == "" {
		t.Fatal("could not extract setup_telemetry() from quickstart.sh")
	}

	// The consumption must be errexit-safe. A bare invocation on its own line, whose result is
	// then read from $?, is the shape that aborts.
	for _, bad := range []string{
		"_await_telemetry_ready \"http://127.0.0.1:$TELEMETRY_PORT/healthz\" \"$TELEMETRY_READY_TIMEOUT\" _telemetry_session_alive\n",
	} {
		if strings.Contains(setup, bad) {
			t.Error("setup_telemetry calls _await_telemetry_ready as a bare command and then reads " +
				"$?. Under `set -e` a non-zero return aborts the script there, so the branch that " +
				"warns and returns 0 is unreachable — telemetry being unavailable would fail the " +
				"whole bootstrap. Put the call in a condition context (`|| rc=$?`, `if !`, etc.)")
		}
	}

	// And prove the real consumption survives, rather than only asserting a shape. The extracted
	// readiness function plus the caller's own branch logic, executed under the real options
	// against a URL that never answers and a liveness probe reporting the process dead.
	awaitFn := extractShellFunction(content, "_await_telemetry_ready")
	if awaitFn == "" {
		t.Fatal("could not extract _await_telemetry_ready()")
	}
	script := "set -euo pipefail\n" + awaitFn + `
_dead() { return 1; }
ready_rc=0
_await_telemetry_ready "http://127.0.0.1:1/healthz" 2 _dead || ready_rc=$?
case "$ready_rc" in
    0) echo "READY" ;;
    2) echo "CRASH-BRANCH" ;;
    *) echo "TIMEOUT-BRANCH" ;;
esac
echo "SURVIVED"`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("the readiness consumption aborted under `set -euo pipefail`: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "SURVIVED") {
		t.Errorf("execution did not reach the line after the branch; output:\n%s", out)
	}
	if !strings.Contains(string(out), "CRASH-BRANCH") {
		t.Errorf("a dead process did not take the crash branch; output:\n%s", out)
	}
}
