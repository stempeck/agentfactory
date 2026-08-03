//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHookScripts_UseEnvVarFallback verifies that both hook scripts use
// AF_ROLE and AF_ROOT environment variables with fallback to the original
// detection methods. This prevents silent regressions where the env var
// pattern is removed but the drift test still passes (because both copies
// are reverted together).
//
// Required by worktree isolation (design constraint C12): hooks must
// resolve ROLE and FACTORY_ROOT from tmux-exported env vars when running
// inside a worktree, falling back to basename/af-root for manual invocation.
func TestHookScripts_UseEnvVarFallback(t *testing.T) {
	repoRoot := findRepoRoot(t)
	scripts := []struct {
		path string
		name string
	}{
		{filepath.Join(repoRoot, "hooks", "quality-gate.sh"), "quality-gate.sh"},
		{filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"), "fidelity-gate.sh"},
	}
	for _, s := range scripts {
		data, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("read %s: %v", s.path, err)
		}
		content := string(data)

		// ROLE must use AF_ROLE env var with fallback (untouched by the Phase 4b L-2 hardening)
		if !strings.Contains(content, "AF_ROLE:-") {
			t.Errorf("%s: ROLE assignment must use ${AF_ROLE:-...} env var fallback", s.name)
		}

		// FACTORY_ROOT resolves ${AF_ROOT} directly, then falls back to `af root` via an
		// explicit empty-guard (Phase 4b L-2 hardening). The old inline ${AF_ROOT:-...}
		// default is replaced, so the literal AF_ROOT:- substring must be gone while the
		// hardened assignment is present.
		if !strings.Contains(content, `FACTORY_ROOT="${AF_ROOT}"`) {
			t.Errorf("%s: FACTORY_ROOT must resolve ${AF_ROOT} directly (hardened empty-guard shape)", s.name)
		}
		if strings.Contains(content, "AF_ROOT:-") {
			t.Errorf("%s: inline ${AF_ROOT:-...} default must be replaced by the explicit empty-guard", s.name)
		}

		// af root must still appear as the explicit empty-guard fallback for non-tmux contexts
		if !strings.Contains(content, "af root") {
			t.Errorf("%s: must retain 'af root' as fallback for non-tmux contexts", s.name)
		}
	}
}
