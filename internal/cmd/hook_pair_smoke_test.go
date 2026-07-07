//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookPair_SequentialSmoke catches lock/env/stdin/trap collisions
// between quality-gate.sh and fidelity-gate.sh by piping a synthetic
// Stop-hook JSON payload into both scripts back-to-back in BOTH orders.
// Both scripts must exit 0 and emit `{"ok": true}` via the silent-exit
// path (`af root` returns empty when cwd is not a factory, so the scripts
// exit early before touching jq, claude, locks, or traps).
//
// This test does NOT prove Claude Code multi-sibling fan-out — that's
// Phase 3b's manual check (AC3.11). It resolves the sequential-execution
// slice of R-INT-10 (Q1 in the design doc) at `make test` time instead
// of post-merge.
//
// Relies on the silent-exit path because CI has no `claude`, no `jq`,
// and no `af` on PATH. The test runs with cwd = t.TempDir() so
// `af root` (if the binary exists) walks up to an ancestor that has no
// .agentfactory directory and returns empty — guaranteeing silent exit
// even on developer machines with a real factory in the filesystem.
func TestHookPair_SequentialSmoke(t *testing.T) {
	repoRoot := findRepoRoot(t)
	qualityGate := filepath.Join(repoRoot, "hooks", "quality-gate.sh")
	fidelityGate := filepath.Join(repoRoot, "hooks", "fidelity-gate.sh")

	// Minimal Stop-hook JSON payload. All fields the scripts read
	// (stop_hook_active, last_assistant_message, transcript_path) are
	// present so a parser that reaches them will not crash.
	payload := []byte(`{"stop_hook_active": false, "last_assistant_message": "test", "transcript_path": ""}`)

	tmpDir := t.TempDir()

	// Pair 1: quality then fidelity
	runHookSmoke(t, qualityGate, payload, tmpDir)
	runHookSmoke(t, fidelityGate, payload, tmpDir)

	// Pair 2: fidelity then quality (catches lock-order races and
	// trap/cleanup ordering issues)
	runHookSmoke(t, fidelityGate, payload, tmpDir)
	runHookSmoke(t, qualityGate, payload, tmpDir)
}

// gateHookFiles are the four copies of the two Stop-hook gate scripts: the two
// top-level runtime copies and their two embedded install_hooks twins. All four
// must carry the identical grader construction (TestInstallHooks_NoDrift enforces
// byte-identity per pair; the tests below enforce the intended content).
func gateHookFiles(repoRoot string) []string {
	return []string{
		filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"),
		filepath.Join(repoRoot, "hooks", "quality-gate.sh"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "fidelity-gate.sh"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "quality-gate.sh"),
	}
}

// graderLine returns the single line of a gate script that invokes the haiku
// grader (the line containing `claude -p --model haiku`). The env construction
// under test lives entirely on this line.
func graderLine(t *testing.T, content, file string) string {
	t.Helper()
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "claude -p --model haiku") {
			return line
		}
	}
	t.Fatalf("%s: no grader line (`claude -p --model haiku`) found", file)
	return ""
}

// TestHookGrader_UsesAllowlistNotDenylist pins Issue #508 W10 (AC-1): each hook's
// haiku grader is built from an ALLOWLIST (`env -i HOME=… PATH=…`) rather than the
// old six-var `env -u` DENYLIST. The allowlist is structurally immune to every
// current and future redirect var a per-agent local-model profile may inject —
// only HOME (for ~/.claude creds) and PATH (to find the binary) survive into the
// grader's environment, so it always reaches the ambient Anthropic endpoint.
//
// Asserting the allowlist form across all four copies also locks the source and
// embedded mirrors together at the grader line; TestInstallHooks_NoDrift enforces
// byte-identity, this test enforces the intended content.
func TestHookGrader_UsesAllowlistNotDenylist(t *testing.T) {
	repoRoot := findRepoRoot(t)

	const graderAllowlist = `env -i HOME="$HOME" PATH="$PATH" claude -p --model haiku`

	for _, f := range gateHookFiles(repoRoot) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		content := string(data)

		if !strings.Contains(content, graderAllowlist) {
			t.Errorf("%s: grader is not allowlist-constructed; expected to contain:\n  %s", f, graderAllowlist)
		}

		// The env -u denylist must be gone entirely (it is drift-prone: it omits at
		// least ANTHROPIC_MODEL/SMALL_FAST_MODEL/DEFAULT_HAIKU_MODEL/SUBAGENT_MODEL).
		if strings.Contains(content, "env -u ") {
			t.Errorf("%s: an env -u denylist remains — W10 replaces it with the env -i allowlist", f)
		}

		// No bare (unwrapped) grader invocation.
		if strings.Contains(content, "$(claude -p --model haiku") {
			t.Errorf("%s: bare grader invocation remains (must be env -i wrapped)", f)
		}
	}
}

// TestHookGrader_DecoyVarsNeutralizedByAllowlist is the HARD decoy-var test (AC-3).
// A decoy profile may set EVERY known redirect/model-shaping var; under `env -i`
// none of them can reach the grader's environment because only HOME and PATH are
// passed through. This asserts structurally on the constructed grader command form
// — no real network and no real claude binary are needed. It is the drift-proof
// property the allowlist inversion buys over extending the denylist.
func TestHookGrader_DecoyVarsNeutralizedByAllowlist(t *testing.T) {
	repoRoot := findRepoRoot(t)

	decoyVars := []string{
		"ANTHROPIC_BASE_URL",
		"ANTHROPIC_AUTH_TOKEN",
		"ANTHROPIC_API_KEY",
		"ANTHROPIC_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		"CLAUDE_CODE_SUBAGENT_MODEL",
		"CLAUDE_CODE_EFFORT_LEVEL",
	}

	for _, f := range gateHookFiles(repoRoot) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		line := graderLine(t, string(data), f)

		// The grader's env is constructed from an allowlist that carries ONLY HOME and PATH.
		if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "VERDICT=$(")), `env -i HOME="$HOME" PATH="$PATH" claude`) {
			t.Errorf("%s: grader line is not the exact env -i HOME/PATH allowlist form:\n  %s", f, line)
		}

		// No decoy var name may appear on the grader line — under env -i the allowlist
		// cannot enumerate any of them, so a decoy profile setting all of them is inert.
		for _, v := range decoyVars {
			if strings.Contains(line, v) {
				t.Errorf("%s: decoy var %q appears on the grader line — it could reach the grader env:\n  %s", f, v, line)
			}
		}
	}
}

func runHookSmoke(t *testing.T, scriptPath string, payload []byte, workDir string) {
	t.Helper()
	cmd := exec.Command("bash", scriptPath)
	cmd.Stdin = bytes.NewReader(payload)
	cmd.Dir = workDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%s exit non-zero: %v\noutput: %s", scriptPath, err, out)
	}
	if !bytes.Contains(out, []byte(`{"ok": true}`)) {
		t.Fatalf("%s did not emit `{\"ok\": true}`:\n%s", scriptPath, out)
	}
}

// findRepoRoot walks up from the test binary's working directory until
// it finds go.mod. Avoids hard-coded relative paths that break under
// different `go test` invocations.
//
// NOTE: an identical helper exists at internal/cmd/integration_test.go:15
// but that file is gated by //go:build integration, so its symbols are
// not visible in the unit build. Phase 3's install_hooks_drift_test.go
// (when it lands) is also in the unit build and should REUSE this
// helper rather than redefining it — both tests live in package cmd.
func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("could not locate go.mod walking up from test cwd")
		}
		dir = parent
	}
}
