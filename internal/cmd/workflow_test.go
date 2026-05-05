package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWorkflowSupplyChainLint(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "test.yml"))
	if err != nil {
		t.Fatalf("reading test.yml: %v", err)
	}
	content := string(data)

	t.Run("supply_chain_lint_job_exists", func(t *testing.T) {
		if !strings.Contains(content, "supply-chain-lint:") {
			t.Error("test.yml missing supply-chain-lint job")
		}
	})

	t.Run("pip_require_hashes_check_step_exists", func(t *testing.T) {
		if !strings.Contains(content, "require-hashes") {
			t.Error("test.yml missing pip --require-hashes check step")
		}
	})
}
