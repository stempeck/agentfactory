//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
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
