package cmd

import (
	"os"
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
