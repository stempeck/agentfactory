// Package main pins the --codex-auth relay contract added to quickdocker.sh in PR #688 Phase 4.
// It lives at the repo root, not under internal/cmd/, because AC #3's grading command
// (`go test . -run 'TestQuickdocker' -v`) only ever compiles the package in the literal current
// directory — internal/cmd/quickdocker_test.go (package cmd) is invisible to that invocation.
package main

import (
	"os"
	"strings"
	"testing"
)

func readQuickdockerSource(t *testing.T) string {
	t.Helper()
	content, err := os.ReadFile("quickdocker.sh")
	if err != nil {
		t.Fatalf("reading quickdocker.sh: %v", err)
	}
	return string(content)
}

// stripShellComments drops every line whose first non-whitespace character is '#'. The
// --codex-auth arm's leading comment (quickdocker.sh:404) names "docker cp" and "chmod 600" in
// prose, so a plain substring check over the raw arm passes even if the real command is deleted;
// command assertions must match against command lines only.
func stripShellComments(s string) string {
	var kept []string
	for _, line := range strings.Split(s, "\n") {
		if strings.HasPrefix(strings.TrimLeft(line, " \t"), "#") {
			continue
		}
		kept = append(kept, line)
	}
	return strings.Join(kept, "\n")
}

// TestQuickdockerCodexAuthRelay pins the four assertions IMPLREADME_PHASE4.md's AC #3 names as
// the contract: the --codex-auth arm exists, runs before the create/recreate path, copies to
// /home/dev/.codex/auth.json with chmod 600, and the AF_CODEX_HOST_DIR mount uses :ro.
func TestQuickdockerCodexAuthRelay(t *testing.T) {
	content := readQuickdockerSource(t)

	t.Run("arm_exists", func(t *testing.T) {
		// A real case arm, not a comment mentioning the flag by name.
		if !strings.Contains(content, "--codex-auth)") {
			t.Error("quickdocker.sh does not contain a --codex-auth) case arm")
		}
	})

	t.Run("runs_before_create_recreate", func(t *testing.T) {
		idxArm := strings.Index(content, "--codex-auth)")
		if idxArm < 0 {
			t.Fatal("--codex-auth) arm not found; cannot check ordering")
		}
		idxRecreate := strings.Index(content, "Remove and recreate")
		idxRun := strings.Index(content, "docker run -dit")
		idxUnknown := strings.Index(content, "Unknown option")
		if idxRecreate < 0 || idxRun < 0 || idxUnknown < 0 {
			t.Fatal("could not locate one of the create/recreate/unknown-option anchors")
		}
		if idxArm >= idxRecreate {
			t.Error("--codex-auth) arm must run before the 'Remove and recreate' prompt")
		}
		if idxArm >= idxRun {
			t.Error("--codex-auth) arm must run before the docker run -dit invocation")
		}
		// Not one of AC #3's four graded assertions, but directly traceable to the CHANGE
		// section's stated placement inside the early short-circuit loop: if the arm falls
		// through into the unknown-option abort loop instead of exiting, the relay would have
		// already run before the script dies on a misleading "Unknown option" error.
		if idxArm >= idxUnknown {
			t.Error("--codex-auth) arm must run before the unknown-option abort loop")
		}
	})

	t.Run("copies_target_and_chmod_600", func(t *testing.T) {
		idxArm := strings.Index(content, "--codex-auth)")
		if idxArm < 0 {
			t.Fatal("--codex-auth) arm not found; cannot isolate its body")
		}
		body := content[idxArm:]
		end := strings.Index(body, ";;")
		if end < 0 {
			t.Fatal("--codex-auth) arm has no terminating ;;")
		}
		arm := stripShellComments(body[:end])

		for _, want := range []string{"docker cp", "/home/dev/.codex/auth.json", "chown dev:dev", "chmod 600"} {
			if !strings.Contains(arm, want) {
				t.Errorf("--codex-auth) arm command lines (comments stripped) missing %q:\n%s", want, arm)
			}
		}
	})

	t.Run("ro_mount", func(t *testing.T) {
		idxGuard := strings.Index(content, `AF_CODEX_HOST_DIR:-`)
		if idxGuard < 0 {
			t.Fatal("no AF_CODEX_HOST_DIR guard found in quickdocker.sh")
		}
		// The guard block is a short if/fi; a fixed window after the guard's own condition line
		// is enough to reach the mount flag it introduces without also picking up unrelated,
		// later parts of the file (same proximity idiom TestQuickdockerMemoryVaultMount uses in
		// internal/cmd/quickdocker_test.go for AF_MEMORY_HOST_DIR).
		window := content[idxGuard:]
		if len(window) > 400 {
			window = window[:400]
		}
		if !strings.Contains(window, "-v ") {
			t.Errorf("AF_CODEX_HOST_DIR guard does not introduce a -v mount flag:\n%s", window)
		}
		if !strings.Contains(window, ":ro") {
			t.Errorf("AF_CODEX_HOST_DIR mount is missing :ro:\n%s", window)
		}
	})
}

// TestQuickdockerMemoryMountStaysReadWrite is a DO-NOT-CHANGE protective assertion: the
// AF_MEMORY_HOST_DIR mount (an unrelated, pre-existing sibling of the new AF_CODEX_HOST_DIR
// mount) must stay read-write. It guards against the specific copy-paste failure mode of adding
// the new mount's :ro suffix onto the wrong variable.
func TestQuickdockerMemoryMountStaysReadWrite(t *testing.T) {
	content := readQuickdockerSource(t)

	idx := strings.Index(content, `MEMORY_DOCKER_ARGS="-v `)
	if idx < 0 {
		t.Fatal("MEMORY_DOCKER_ARGS=\"-v assignment not found in quickdocker.sh")
	}
	line := content[idx:]
	if nl := strings.IndexByte(line, '\n'); nl >= 0 {
		line = line[:nl]
	}
	if strings.Contains(line, ":ro") {
		t.Errorf("AF_MEMORY_HOST_DIR mount must stay read-write, but found :ro on its assignment line: %s", line)
	}
}
