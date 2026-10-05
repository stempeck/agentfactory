//go:build integration

package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

func extractSyncBlock(t *testing.T, repoRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "agent-gen-all.sh"))
	if err != nil {
		t.Fatalf("reading agent-gen-all.sh: %v", err)
	}
	body := string(data)

	const startMarker = "# --- Sync formulas from source"
	const endMarker = "# --- Regenerate each formula"

	startIdx := strings.Index(body, startMarker)
	if startIdx == -1 {
		t.Fatal("agent-gen-all.sh missing sync block start marker")
	}
	endIdx := strings.Index(body[startIdx:], endMarker)
	if endIdx == -1 {
		t.Fatal("agent-gen-all.sh missing sync block end marker")
	}

	block := body[startIdx : startIdx+endIdx]
	return "#!/usr/bin/env bash\nset -euo pipefail\n" + block
}

func TestFormulaSyncBehavior(t *testing.T) {
	repoRoot := findRepoRoot(t)
	sourceDir := filepath.Join(repoRoot, "internal", "cmd", "install_formulas")

	sourceEntries, err := os.ReadDir(sourceDir)
	if err != nil {
		t.Fatalf("reading source formulas: %v", err)
	}
	var sourceNames []string
	for _, e := range sourceEntries {
		if !e.IsDir() && filepath.Ext(e.Name()) == ".toml" {
			sourceNames = append(sourceNames, e.Name())
		}
	}
	if len(sourceNames) == 0 {
		t.Fatal("no source formulas found")
	}

	syncScript := extractSyncBlock(t, repoRoot)

	t.Run("replaces_stale_content", func(t *testing.T) {
		tmpDir := t.TempDir()
		formulaDir := config.FormulasDir(tmpDir)
		if err := os.MkdirAll(formulaDir, 0755); err != nil {
			t.Fatalf("creating formula dir: %v", err)
		}

		staleName := sourceNames[0]
		stalePath := filepath.Join(formulaDir, staleName)
		staleContent := []byte("# stale content that should be replaced")
		if err := os.WriteFile(stalePath, staleContent, 0644); err != nil {
			t.Fatalf("writing stale formula: %v", err)
		}
		past := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		os.Chtimes(stalePath, past, past)

		runSyncScript(t, syncScript, repoRoot, formulaDir, repoRoot)

		got, err := os.ReadFile(filepath.Join(formulaDir, staleName))
		if err != nil {
			t.Fatalf("reading synced formula: %v", err)
		}
		want, err := os.ReadFile(filepath.Join(sourceDir, staleName))
		if err != nil {
			t.Fatalf("reading source formula: %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("stale formula %s was not replaced with source content", staleName)
		}
	})

	t.Run("removes_orphan", func(t *testing.T) {
		tmpDir := t.TempDir()
		formulaDir := config.FormulasDir(tmpDir)
		if err := os.MkdirAll(formulaDir, 0755); err != nil {
			t.Fatalf("creating formula dir: %v", err)
		}

		orphanPath := filepath.Join(formulaDir, "orphan-does-not-exist.formula.toml")
		if err := os.WriteFile(orphanPath, []byte("# orphan"), 0644); err != nil {
			t.Fatalf("writing orphan formula: %v", err)
		}

		runSyncScript(t, syncScript, repoRoot, formulaDir, repoRoot)

		if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
			t.Error("orphan formula was not removed by sync")
		}
	})

	t.Run("preserves_customer_formula", func(t *testing.T) {
		tmpDir := t.TempDir()
		formulaDir := config.FormulasDir(tmpDir)
		if err := os.MkdirAll(formulaDir, 0755); err != nil {
			t.Fatalf("creating formula dir: %v", err)
		}

		customerPath := filepath.Join(formulaDir, "my-custom-workflow.formula.toml")
		if err := os.WriteFile(customerPath, []byte("# customer formula"), 0644); err != nil {
			t.Fatalf("writing customer formula: %v", err)
		}

		runSyncScript(t, syncScript, repoRoot, formulaDir, tmpDir)

		if _, err := os.Stat(customerPath); os.IsNotExist(err) {
			t.Error("customer formula was deleted by sync in non-source context")
		}
	})

	t.Run("preserves_plugin_formula_recorded_in_manifest", func(t *testing.T) {
		// K10 (issue #538 Phase 4): the SOURCE-repo orphan pass must preserve a plugin
		// formula recorded in .agentfactory/plugins.json instead of deleting it as an
		// orphan. This is the only test proving the exemption actually FIRES (rather than
		// being merely inert — that byte-identical-without-manifest path is covered by
		// removes_orphan + preserves_customer_formula). projectDir == repoRoot == AF_SRC ⇒
		// is_source_repo=true, the branch that deletes. The script derives the manifest path
		// from FORMULA_DIR (== config.PluginsConfigPath under tmpDir), so the fixture is
		// test-controllable even though source detection forces PROJECT == repoRoot.
		tmpDir := t.TempDir()
		formulaDir := config.FormulasDir(tmpDir)
		if err := os.MkdirAll(formulaDir, 0755); err != nil {
			t.Fatalf("creating formula dir: %v", err)
		}

		// A plugin-staged formula with no install_formulas counterpart — without the
		// manifest the source-repo orphan pass would delete it (like removes_orphan).
		pluginPath := filepath.Join(formulaDir, "acme-agent.formula.toml")
		if err := os.WriteFile(pluginPath, []byte("# plugin formula"), 0644); err != nil {
			t.Fatalf("writing plugin formula: %v", err)
		}
		// A genuine orphan NOT recorded in the manifest — must still be deleted, proving the
		// exemption is selective, not a blanket skip of the orphan pass.
		orphanPath := filepath.Join(formulaDir, "not-a-plugin.formula.toml")
		if err := os.WriteFile(orphanPath, []byte("# orphan"), 0644); err != nil {
			t.Fatalf("writing orphan formula: %v", err)
		}

		// Record the plugin formula keyed by BARE STEM, mirroring plugin.go:568.
		manifest := &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
			"acme": {Formulas: map[string]config.PluginFormula{"acme-agent": {SHA256: "deadbeef"}}},
		}}
		if err := config.SavePluginsConfig(config.PluginsConfigPath(tmpDir), manifest); err != nil {
			t.Fatalf("writing plugins manifest: %v", err)
		}

		runSyncScript(t, syncScript, repoRoot, formulaDir, repoRoot)

		if _, err := os.Stat(pluginPath); os.IsNotExist(err) {
			t.Error("plugin formula recorded in plugins.json was deleted by the source-repo orphan pass — K10 exemption did not fire")
		}
		if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
			t.Error("a formula NOT recorded in plugins.json survived — the K10 exemption must be selective, not a blanket skip")
		}
	})

	t.Run("customer_branch_inert_to_manifest", func(t *testing.T) {
		// XR-7 closes the customer (non-source) branch, which make check-regen never
		// exercises. K10's exemption lives ONLY in the source passes, so a plugins.json must
		// be completely inert here — the customer branch preserves everything regardless.
		// This proves the shared plugins_manifest/plugin_owner_of_stem setup neither misfires
		// nor aborts the customer branch under `set -euo pipefail` even with a manifest present.
		tmpDir := t.TempDir()
		formulaDir := config.FormulasDir(tmpDir)
		if err := os.MkdirAll(formulaDir, 0755); err != nil {
			t.Fatalf("creating formula dir: %v", err)
		}
		customerPath := filepath.Join(formulaDir, "my-custom-workflow.formula.toml")
		if err := os.WriteFile(customerPath, []byte("# customer formula"), 0644); err != nil {
			t.Fatalf("writing customer formula: %v", err)
		}
		manifest := &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
			"acme": {Formulas: map[string]config.PluginFormula{"acme-agent": {SHA256: "x"}}},
		}}
		if err := config.SavePluginsConfig(config.PluginsConfigPath(tmpDir), manifest); err != nil {
			t.Fatalf("writing plugins manifest: %v", err)
		}

		// projectDir=tmpDir != AF_SRC ⇒ is_source_repo=false ⇒ customer branch (no template pass).
		runSyncScript(t, syncScript, repoRoot, formulaDir, tmpDir)

		if _, err := os.Stat(customerPath); os.IsNotExist(err) {
			t.Error("customer formula was deleted in the customer branch with a manifest present — the source-only K10 exemption must not affect the customer branch")
		}
	})

	t.Run("copies_all_source_formulas", func(t *testing.T) {
		tmpDir := t.TempDir()
		formulaDir := config.FormulasDir(tmpDir)
		if err := os.MkdirAll(formulaDir, 0755); err != nil {
			t.Fatalf("creating formula dir: %v", err)
		}

		runSyncScript(t, syncScript, repoRoot, formulaDir, repoRoot)

		for _, name := range sourceNames {
			destPath := filepath.Join(formulaDir, name)
			got, err := os.ReadFile(destPath)
			if err != nil {
				t.Errorf("source formula %s not copied to dest: %v", name, err)
				continue
			}
			want, err := os.ReadFile(filepath.Join(sourceDir, name))
			if err != nil {
				t.Fatalf("reading source %s: %v", name, err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("formula %s content mismatch after sync", name)
			}
		}

		destEntries, err := os.ReadDir(formulaDir)
		if err != nil {
			t.Fatalf("reading dest dir: %v", err)
		}
		if len(destEntries) < len(sourceNames) {
			t.Errorf("dest has %d files, source has %d", len(destEntries), len(sourceNames))
		}
	})
}

func TestAgentGenAllDocumentsWorktreeLimitation(t *testing.T) {
	repoRoot := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "agent-gen-all.sh"))
	if err != nil {
		t.Fatalf("reading agent-gen-all.sh: %v", err)
	}

	if !bytes.Contains(data, []byte("worktree")) {
		t.Error("agent-gen-all.sh header does not document worktree limitation")
	}
	if !bytes.Contains(data, []byte("main repo checkout")) {
		t.Error("agent-gen-all.sh header does not mention running from the main repo checkout")
	}
}

func runSyncScript(t *testing.T, script, repoRoot, formulaDir, projectDir string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"AF_SRC="+repoRoot,
		"FORMULA_DIR="+formulaDir,
		"PROJECT="+projectDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sync script failed: %v\nOutput:\n%s", err, out)
	}
	t.Logf("sync output:\n%s", out)
}
