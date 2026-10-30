package cmd

import (
	"io/fs"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

// TestEmbeddedFormulaIntegrationsDeclared: a shipped formula may only name integrations the binary
// itself can acquire, so every integrations / integrations_optional name has an embedded manifest.
func TestEmbeddedFormulaIntegrationsDeclared(t *testing.T) {
	matches, err := filepath.Glob("install_formulas/*.formula.toml")
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) == 0 {
		t.Fatal("no install_formulas/*.formula.toml matched; the glob is relative to internal/cmd")
	}
	for _, path := range matches {
		f, err := formula.ParseFile(path)
		if err != nil {
			t.Errorf("ParseFile(%s): %v", path, err)
			continue
		}
		for _, name := range slices.Concat(f.Integrations, f.IntegrationsOptional) {
			if _, err := fs.Stat(embeddedIntegrations, "integrations/"+name+"/af-integration.toml"); err != nil {
				t.Errorf("%s declares integration %q, but embeddedIntegrations has no integrations/%s/af-integration.toml", path, name, name)
			}
		}
	}
}
