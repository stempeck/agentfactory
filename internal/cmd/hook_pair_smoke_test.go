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

// graderEnvBlock returns the grader's env-construction block: the lines from the
// `env -i` allowlist opener through the line that invokes `claude -p --model
// haiku`. Phase 4b spreads the allowlist across several ${VAR:+...} OTel-forwarding
// continuation lines, so prefix and decoy-var assertions must scan this whole
// block rather than the single line that names claude.
func graderEnvBlock(t *testing.T, content, file string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.Contains(line, "env -i HOME=") {
			start = i
			break
		}
	}
	if start == -1 {
		t.Fatalf("%s: no grader env block (`env -i HOME=`) found", file)
	}
	for i := start; i < len(lines); i++ {
		if strings.Contains(lines[i], "claude -p --model haiku") {
			return strings.Join(lines[start:i+1], "\n")
		}
	}
	t.Fatalf("%s: grader env block opened but no `claude -p --model haiku` terminator found", file)
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

	// The allowlist base — env -i carrying ONLY HOME and PATH — opens the grader
	// invocation. Phase 4b inserts ${VAR:+...} OTel-forwarding tokens between this
	// base and `claude`, so the base and the grader command are asserted separately
	// rather than as one contiguous substring (which the inserted tokens destroy).
	const allowlistBase = `env -i HOME="$HOME" PATH="$PATH"`
	const graderCmd = `claude -p --model haiku`

	for _, f := range gateHookFiles(repoRoot) {
		data, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		content := string(data)

		if !strings.Contains(content, allowlistBase) {
			t.Errorf("%s: grader is not allowlist-constructed; expected the env -i HOME/PATH allowlist base:\n  %s", f, allowlistBase)
		}
		if !strings.Contains(content, graderCmd) {
			t.Errorf("%s: grader invocation %q not found", f, graderCmd)
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
		block := graderEnvBlock(t, string(data), f)

		// The grader's env opens with an allowlist carrying ONLY HOME and PATH; Phase 4b
		// appends ${VAR:+...} OTel-forwarding tokens after this base, so the assertion
		// pins the opener rather than a whole-line prefix through `claude`.
		if !strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(block), "VERDICT=$(")), `env -i HOME="$HOME" PATH="$PATH"`) {
			t.Errorf("%s: grader env does not open with the env -i HOME/PATH allowlist base:\n  %s", f, block)
		}

		// No decoy var name may appear anywhere in the constructed env block — under
		// env -i the allowlist cannot enumerate any of them, so a decoy profile setting
		// all of them is inert. Scanning the whole block (not just the line naming
		// claude) is a strictly stronger guarantee than the pre-Phase-4b check.
		for _, v := range decoyVars {
			if strings.Contains(block, v) {
				t.Errorf("%s: decoy var %q appears in the grader env block — it could reach the grader env:\n  %s", f, v, block)
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
