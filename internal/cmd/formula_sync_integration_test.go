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
		formulaDir := filepath.Join(tmpDir, ".beads", "formulas")
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

		runSyncScript(t, syncScript, repoRoot, formulaDir)

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
		formulaDir := filepath.Join(tmpDir, ".beads", "formulas")
		if err := os.MkdirAll(formulaDir, 0755); err != nil {
			t.Fatalf("creating formula dir: %v", err)
		}

		orphanPath := filepath.Join(formulaDir, "orphan-does-not-exist.formula.toml")
		if err := os.WriteFile(orphanPath, []byte("# orphan"), 0644); err != nil {
			t.Fatalf("writing orphan formula: %v", err)
		}

		runSyncScript(t, syncScript, repoRoot, formulaDir)

		if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
			t.Error("orphan formula was not removed by sync")
		}
	})

	t.Run("copies_all_source_formulas", func(t *testing.T) {
		tmpDir := t.TempDir()
		formulaDir := filepath.Join(tmpDir, ".beads", "formulas")
		if err := os.MkdirAll(formulaDir, 0755); err != nil {
			t.Fatalf("creating formula dir: %v", err)
		}

		runSyncScript(t, syncScript, repoRoot, formulaDir)

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
		if len(destEntries) != len(sourceNames) {
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

func runSyncScript(t *testing.T, script, repoRoot, formulaDir string) {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"AF_SRC="+repoRoot,
		"FORMULA_DIR="+formulaDir,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("sync script failed: %v\nOutput:\n%s", err, out)
	}
	t.Logf("sync output:\n%s", out)
}
