package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakefileSupplyChainInvariants(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatalf("reading Makefile: %v", err)
	}
	content := string(data)

	t.Run("pip_require_hashes", func(t *testing.T) {
		if !strings.Contains(content, "--require-hashes") {
			t.Error("Makefile missing --require-hashes in pip install line")
		}
	})
}
