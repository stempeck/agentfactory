// Package entrypoint hosts the hermetic test for the container-bootstrap web-UI launch guard
// (quickstart.sh, Phase 5 / C0). It lives in its OWN package — outside the dir list scanned by
// web/internal/server/lint_test.go's no-shell lint — so that running the real guard via
// `exec.Command("bash", ...)` here does not trip that lint. There is no production code in this
// package; it exists to make the IFF-available launch contract CI-visible.
package entrypoint

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	guardStart = "# >>> phase5 webui launch guard >>>"
	guardEnd   = "# <<< phase5 webui launch guard <<<"
)

// repoRoot walks up from the package dir (the test's CWD) to the directory that holds quickstart.sh.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "quickstart.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate quickstart.sh by walking up from %q", dir)
	return ""
}

// extractGuard slices the real guard block out of quickstart.sh by its sentinel markers, so the
// test exercises the byte-exact shell that ships (not a copy that could drift).
func extractGuard(t *testing.T, script string) string {
	t.Helper()
	i := strings.Index(script, guardStart)
	j := strings.Index(script, guardEnd)
	if i < 0 || j < 0 || j < i {
		t.Fatalf("guard sentinels not found in quickstart.sh (start=%d end=%d)", i, j)
	}
	return script[i+len(guardStart) : j]
}

// runGuard executes the extracted guard under quickstart's own error posture (set -euo pipefail),
// with HOME pointed at homeDir and no-op log shims, then prints a trailing sentinel that appears in
// output ONLY if control flowed past the guard. Returns combined output and the run error.
func runGuard(t *testing.T, guard, homeDir string) (string, error) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash not available: %v", err)
	}
	script := "set -euo pipefail\n" +
		"log_info() { echo \"INFO: $*\"; }\n" +
		"log_success() { echo \"SUCCESS: $*\"; }\n" +
		"log_warn() { echo \"WARN: $*\"; }\n" +
		guard + "\n" +
		"echo PAST_GUARD_SENTINEL\n"
	cmd := exec.Command("bash", "-c", script)
	// Minimal, hermetic environment: HOME drives the guard's binary path; PATH lets bash resolve
	// any externals. AF_ROOT pins the guard's rendezvous-file root ("${AF_ROOT:-$PWD}/.runtime/...")
	// to the test's temp HOME, so the launch-branch poll stays inside the hermetic sandbox and never
	// touches $PWD (the repo tree). The real $HOME is deliberately NOT inherited.
	cmd.Env = []string{"HOME=" + homeDir, "AF_ROOT=" + homeDir, "PATH=" + os.Getenv("PATH")}
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// TestEntrypoint_WebuiAbsent_NoOp — AC #2. With no webui binary under $HOME/.local/bin, the real
// quickstart.sh guard takes its else-branch (skip silently), control flows past it, and nothing
// aborts (exit 0). Hermetic: a temp HOME with no binary; the if-branch (nohup webui) is never
// entered, so no process is spawned and the live tree is untouched (FR-3 / ADR-018).
func TestEntrypoint_WebuiAbsent_NoOp(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatal(err)
	}
	guard := extractGuard(t, string(src))

	home := t.TempDir() // no $HOME/.local/bin/webui here → absent case

	out, err := runGuard(t, guard, home)
	if err != nil {
		t.Fatalf("guard aborted with %v; output:\n%s", err, out)
	}
	if !strings.Contains(out, "PAST_GUARD_SENTINEL") {
		t.Errorf("control did not flow past the guard (nothing should abort); output:\n%s", out)
	}
	if !strings.Contains(out, "skipping optional web UI") {
		t.Errorf("else-branch not taken (expected the skip message); output:\n%s", out)
	}
	if strings.Contains(out, "Web UI started") {
		t.Errorf("if-branch wrongly taken with no binary present; output:\n%s", out)
	}
}

// TestEntrypoint_WebuiPresent_TakesLaunchBranch is the non-vacuity counterpart (mirrors
// lint_test.go's SelfNegative): it proves the guard actually DISCRIMINATES on the executable bit.
// With an executable webui present, the if-branch is taken ("Web UI started") and the skip message
// is NOT printed. `[ -x ]` tests the mode bit only, so this holds even where the FS would refuse to
// exec the (throwaway) file — the backgrounded `nohup … &` returns 0 regardless. Still a no-op vs
// the live tree (temp HOME; the only external write is a throwaway /tmp/webui.log).
func TestEntrypoint_WebuiPresent_TakesLaunchBranch(t *testing.T) {
	root := repoRoot(t)
	src, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatal(err)
	}
	guard := extractGuard(t, string(src))

	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "webui"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Publish the rendezvous file the guard polls for ("${AF_ROOT:-$PWD}/.runtime/webui_server.json").
	// The throwaway webui exits without writing it, so without this the guard would poll 5s, time out,
	// and take the warn branch — never printing the success message this test asserts. Pre-publishing it
	// (under the test-controlled AF_ROOT=home that runGuard exports) drives the guard's success branch
	// deterministically and instantly. We do NOT rely on the fake webui actually executing (the [ -x ]
	// gate is mode-bit only), matching this suite's hermetic, exec-FS-independent contract.
	runtimeDir := filepath.Join(home, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "webui_server.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := runGuard(t, guard, home)
	if err != nil {
		t.Fatalf("guard aborted with %v; output:\n%s", err, out)
	}
	if !strings.Contains(out, "Web UI started") {
		t.Errorf("if-branch not taken with an executable webui present; output:\n%s", out)
	}
	if strings.Contains(out, "skipping optional web UI") {
		t.Errorf("else-branch wrongly taken with a binary present; output:\n%s", out)
	}
}
