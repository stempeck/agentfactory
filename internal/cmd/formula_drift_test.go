package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// TestFormulaDriftSourceVsInstalled fails if any formula TOML in
// internal/cmd/install_formulas/ differs from its counterpart in
// .beads/formulas/, or vice versa. The two locations must stay in lockstep:
// install_formulas/ is the //go:embed source baked into the af binary,
// .beads/formulas/ is the on-disk copy installed into factories.
//
// Drift here means the running binary and the deployed factory disagree on
// formula content — silent identity divergence for any agent generated from
// the affected formula. This test runs under `go test ./...` so CI catches
// drift on every PR.
func TestFormulaDriftSourceVsInstalled(t *testing.T) {
	const (
		sourceDir = "install_formulas"
		mirrorDir = "../../.beads/formulas"
	)

	sourceFiles := listFormulas(t, sourceDir)
	mirrorFiles := listFormulas(t, mirrorDir)

	sourceSet := map[string]bool{}
	for _, n := range sourceFiles {
		sourceSet[n] = true
	}
	mirrorSet := map[string]bool{}
	for _, n := range mirrorFiles {
		mirrorSet[n] = true
	}

	for _, name := range sourceFiles {
		if !mirrorSet[name] {
			t.Errorf("formula %q exists in %s but is missing from %s — run `make sync-formulas` or copy by hand", name, sourceDir, mirrorDir)
			continue
		}
		srcBytes, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatalf("read source %s: %v", name, err)
		}
		mirBytes, err := os.ReadFile(filepath.Join(mirrorDir, name))
		if err != nil {
			t.Fatalf("read mirror %s: %v", name, err)
		}
		if !bytes.Equal(srcBytes, mirBytes) {
			t.Errorf("formula drift: %s differs between %s and %s — both copies must be identical (run `make sync-formulas` to push source→mirror, or hand-merge if mirror has fixes)", name, sourceDir, mirrorDir)
		}
	}

}

func listFormulas(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".toml" {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
