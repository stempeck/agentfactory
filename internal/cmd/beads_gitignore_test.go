package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestBeadsGitignoreCoversSQLiteFiles(t *testing.T) {
	data, err := os.ReadFile("../../.beads/.gitignore")
	if err != nil {
		t.Fatalf("read .beads/.gitignore: %v", err)
	}
	content := string(data)

	required := []string{
		"*.sqlite",
		"*.sqlite-wal",
		"*.sqlite-shm",
	}
	for _, pattern := range required {
		if !strings.Contains(content, pattern) {
			t.Errorf(".beads/.gitignore missing pattern %q — issues.sqlite files will appear in git status", pattern)
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
