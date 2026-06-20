package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestQuickstartGitDefaultsCommentDescribesRealFlow guards PR #379 Thread 1:
// the configure_git_defaults comment must not claim the git-identity values are
// read from a factory.json "written ... above". In main(), configure_git_defaults
// runs BEFORE configure_factory — which is where `af install --init` writes
// factory.json and `cd "$repo_dir"` happens. So on a clean install factory.json
// does not exist yet, the `[ -f ]` guard fails, the jq read is skipped, and the
// literal fallback (matching the canonical Go constants) is what actually runs.
func TestQuickstartGitDefaultsCommentDescribesRealFlow(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	fn := extractShellFunction(content, "configure_git_defaults")
	if fn == "" {
		t.Fatal("could not extract configure_git_defaults() function body")
	}

	// Negative: the inverted "written ... above" data-flow claim must be gone.
	if strings.Contains(fn, "above") {
		t.Error(`configure_git_defaults() comment must not claim factory.json is written "above": af install --init runs below, inside configure_factory`)
	}

	// Positive: the comment must anchor to where factory.json is actually
	// written (configure_factory), so the corrected data flow is unambiguous.
	if !strings.Contains(fn, "configure_factory") {
		t.Error("configure_git_defaults() comment should reference configure_factory (where af install --init writes factory.json, below this block)")
	}

	// Positive: the literal fallback must still be documented as the source.
	if !strings.Contains(fn, "fallback") {
		t.Error("configure_git_defaults() comment should describe the literal fallback (the clean-install source)")
	}
}
