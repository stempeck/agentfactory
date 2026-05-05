package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestIssue168_SupplyChainLintCleanup(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "test.yml"))
	if err != nil {
		t.Fatalf("reading test.yml: %v", err)
	}
	content := string(data)

	t.Run("no_hadolint_step", func(t *testing.T) {
		if strings.Contains(content, "hadolint") {
			t.Error("test.yml still contains hadolint step — should be removed per issue #168")
		}
	})

	t.Run("no_pipe_to_bash_step", func(t *testing.T) {
		if strings.Contains(content, "pipe-to-bash") {
			t.Error("test.yml still contains pipe-to-bash step — should be removed per issue #168")
		}
	})

	t.Run("no_hadolint_yaml_file", func(t *testing.T) {
		hadolintPath := filepath.Join(root, ".hadolint.yaml")
		if _, err := os.Stat(hadolintPath); err == nil {
			t.Error(".hadolint.yaml still exists — should be deleted per issue #168")
		}
	})

	t.Run("header_comment_no_hadolint_mention", func(t *testing.T) {
		lines := strings.Split(content, "\n")
		for i, line := range lines {
			if i > 20 {
				break
			}
			if strings.HasPrefix(line, "#") && strings.Contains(strings.ToLower(line), "hadolint") {
				t.Errorf("header comment at line %d still mentions hadolint", i+1)
			}
		}
	})
}
