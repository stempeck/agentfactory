//go:build integration

package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// 3e (T8 "Untested: neither tier"): the source-repo orphan pass preserves a plugin formula when
// plugins.json is present but unparseable. Companion of TestFormulaSyncBehavior's
// preserves_plugin_formula_recorded_in_manifest, kept in its own file for this increment.
func TestFormulaSyncPreservesPluginFormulaWhenManifestUnreadable(t *testing.T) {
	repoRoot := findRepoRoot(t)
	syncScript := extractSyncBlock(t, repoRoot)

	tmpDir := t.TempDir()
	formulaDir := config.FormulasDir(tmpDir)
	if err := os.MkdirAll(formulaDir, 0755); err != nil {
		t.Fatalf("creating formula dir: %v", err)
	}
	pluginPath := filepath.Join(formulaDir, "acme-agent.formula.toml")
	if err := os.WriteFile(pluginPath, []byte("# plugin formula"), 0644); err != nil {
		t.Fatalf("writing plugin formula: %v", err)
	}
	conflict := "<<<<<<< HEAD\n{\"plugins\":{\"acme\":{\"formulas\":{\"acme-agent\":{}}}}}\n=======\n{}\n>>>>>>> origin/main\n"
	if err := os.WriteFile(config.PluginsConfigPath(tmpDir), []byte(conflict), 0644); err != nil {
		t.Fatalf("writing unreadable plugins manifest: %v", err)
	}

	runSyncScript(t, syncScript, repoRoot, formulaDir, repoRoot)

	if _, err := os.Stat(pluginPath); err != nil {
		t.Errorf("plugin formula deleted by the source-repo orphan pass while plugins.json was unreadable: %v", err)
	}
}
