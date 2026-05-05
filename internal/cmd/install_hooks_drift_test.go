//go:build !integration

package cmd

import (
	"embed"
	"os"
	"path/filepath"
	"testing"
)

//go:embed install_hooks/quality-gate.sh
//go:embed install_hooks/quality-gate-prompt.txt
//go:embed install_hooks/fidelity-gate.sh
//go:embed install_hooks/fidelity-gate-prompt.txt
var embeddedHooksForDriftTest embed.FS

// TestInstallHooks_NoDrift asserts that the embedded install_hooks/ copies
// are byte-identical to the top-level hooks/ copies. The two locations must
// stay in lockstep — the top-level is what runs at agent runtime; the
// embedded source is what `af install --init` ships into a fresh factory.
// Drift would silently produce factories that differ from the developer's
// local environment.
//
// The findRepoRoot helper is reused from hook_pair_smoke_test.go (same
// package, same unit build); do NOT redefine it here or the file will fail
// to compile with "findRepoRoot redeclared in this block".
//
// Adding a new hook? Append to the pairs slice below.
func TestInstallHooks_NoDrift(t *testing.T) {
	repoRoot := findRepoRoot(t)
	pairs := []struct {
		top      string // path relative to repo root
		embedded string // path relative to internal/cmd (embed root)
	}{
		{"hooks/quality-gate.sh", "install_hooks/quality-gate.sh"},
		{"hooks/quality-gate-prompt.txt", "install_hooks/quality-gate-prompt.txt"},
		{"hooks/fidelity-gate.sh", "install_hooks/fidelity-gate.sh"},
		{"hooks/fidelity-gate-prompt.txt", "install_hooks/fidelity-gate-prompt.txt"},
	}
	for _, p := range pairs {
		topData, err := os.ReadFile(filepath.Join(repoRoot, p.top))
		if err != nil {
			t.Errorf("read %s: %v", p.top, err)
			continue
		}
		embData, err := embeddedHooksForDriftTest.ReadFile(p.embedded)
		if err != nil {
			t.Errorf("read embedded %s: %v", p.embedded, err)
			continue
		}
		if string(topData) != string(embData) {
			t.Errorf("%s and %s have drifted — keep them byte-identical (run `diff` to see the difference)", p.top, p.embedded)
		}
	}
}
