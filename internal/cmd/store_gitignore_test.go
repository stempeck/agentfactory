package cmd

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestBeadsGitignoreCoversSQLiteFiles(t *testing.T) {
	data, err := os.ReadFile("../../.agentfactory/store/.gitignore")
	if err != nil {
		t.Fatalf("read .agentfactory/store/.gitignore: %v", err)
	}
	content := string(data)

	required := []string{
		"*.sqlite",
		"*.sqlite-wal",
		"*.sqlite-shm",
	}
	for _, pattern := range required {
		if !strings.Contains(content, pattern) {
			t.Errorf(".agentfactory/store/.gitignore missing pattern %q — issues.sqlite files will appear in git status", pattern)
		}
	}
}

// TestStoreGitignoreCoversPluginsDir asserts the store .gitignore ignores the
// plugins/ directory (issue #538 K11) so acquired plugin clones under
// store/plugins/ stay untracked, while installed flat copies under store/formulas/
// remain tracked. The test name contains "Gitignore" so `-run 'Gitignore'` selects
// it (peer-review correction: the outline's TestStoreGitignore never existed).
func TestStoreGitignoreCoversPluginsDir(t *testing.T) {
	data, err := os.ReadFile("../../.agentfactory/store/.gitignore")
	if err != nil {
		t.Fatalf("read .agentfactory/store/.gitignore: %v", err)
	}
	if !strings.Contains(string(data), "plugins/") {
		t.Error(".agentfactory/store/.gitignore missing 'plugins/' — acquired plugin clones under store/plugins/ will appear in git status")
	}
	// Design 695 Phase 2 (IMPLREADME_PHASE2 AC 2): integration snapshots under store/integrations/ are
	// ignored by the STORE .gitignore, because the root .gitignore re-includes the store.
	n := 0
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimRight(l, "\r") == "integrations/" {
			n++
		}
	}
	if n != 1 {
		t.Errorf(".agentfactory/store/.gitignore has %d exact 'integrations/' line(s), want 1 — integration snapshots under store/integrations/ will appear in git status", n)
	}
	root := findModuleRoot(t)
	err = exec.Command("git", "-C", root, "check-ignore", "-q", ".agentfactory/store/integrations/x").Run()
	var exitErr *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &exitErr) && exitErr.ExitCode() == 1:
		t.Error("git check-ignore: .agentfactory/store/integrations/x is not ignored")
	default:
		t.Fatalf("git check-ignore .agentfactory/store/integrations/x: %v", err)
	}
}

func TestNoStaleBeadsRefsInShellScripts(t *testing.T) {
	scripts := []struct {
		path string
		name string
	}{
		{"../../quickstart.sh", "quickstart.sh"},
		{"../../agent-gen-all.sh", "agent-gen-all.sh"},
	}
	for _, s := range scripts {
		data, err := os.ReadFile(s.path)
		if err != nil {
			t.Fatalf("read %s: %v", s.name, err)
		}
		if strings.Contains(string(data), ".beads") {
			t.Errorf("%s still contains stale '.beads' reference — should use .agentfactory/store/", s.name)
		}
	}
}

func TestInstallCommentReferencesCorrectDBName(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	content := string(data)
	if strings.Contains(content, "beads.db") {
		t.Error("install.go still references stale 'beads.db' — should reference 'issues.sqlite'")
	}
}
