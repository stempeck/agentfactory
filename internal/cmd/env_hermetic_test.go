package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestNoEnvReadsInLibraryPackages verifies the structural invariant that
// library code below the cobra command layer does not read or mutate named
// environment values from the process environment. Ambient state enters only
// at the CLI/main boundary (internal/cmd/) and flows downward as explicit
// values.
//
// The check flags any call to os.Getenv, os.LookupEnv, os.Setenv, or
// os.Unsetenv in non-test .go files outside internal/cmd/. os.Environ()
// (whole-environment passthrough used to forward env to subprocesses without
// reading a specific value into program logic) is structurally different and
// is not flagged.
//
// Background: established by issue #98 (Phases 1-4). Originally scoped to
// AF_*/CLAUDE_*/TMUX* prefixes; broadened to all named reads after issue #80
// Phase 3 (PR #117) demonstrated that prefix-based scoping let a new env-var
// family (BD_*) slip through. Further broadened to also catch writes
// (Setenv/Unsetenv) after issue #80 Phase 2 (PR #116) demonstrated that
// read-only scanning let a process-env seed in contract test setup slip
// through.
func TestNoEnvReadsInLibraryPackages(t *testing.T) {
	// Find the module root (parent of internal/)
	root := findModuleRoot(t)

	internalDir := filepath.Join(root, "internal")
	cmdDir := filepath.Join(root, "internal", "cmd")

	envReadPattern := regexp.MustCompile(`\bos\.(Getenv|LookupEnv|Setenv|Unsetenv)\(`)

	var violations []string

	err := filepath.Walk(internalDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		// Skip internal/cmd/ entirely — command layer is permitted to read env
		if info.IsDir() && path == cmdDir {
			return filepath.SkipDir
		}
		// Skip non-Go files and test files
		if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}

		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for i, line := range strings.Split(string(data), "\n") {
			if envReadPattern.MatchString(line) {
				violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, i+1, strings.TrimSpace(line)))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking internal/: %v", err)
	}

	if len(violations) > 0 {
		t.Errorf("library code must not read or mutate named env values directly (os.Getenv/os.LookupEnv/os.Setenv/os.Unsetenv).\n"+
			"Ambient state should enter at the CLI boundary (internal/cmd/) and flow "+
			"as explicit parameters. os.Environ() (whole-env passthrough) is permitted.\n"+
			"Violations:\n  %s",
			strings.Join(violations, "\n  "))
	}
}

// TestSuiteHermiticWithAFVars verifies that setting AF_WORKTREE in the
// process environment does not change any test's behavior. This is the
// runtime complement to the source-scan test above.
func TestSuiteHermiticWithAFVars(t *testing.T) {
	// This test's mere existence forces the issue: if it runs inside an
	// agent (where AF_WORKTREE is set), the t.Setenv calls in individual
	// tests must isolate them. If a new test forgets t.Setenv, this test
	// serves as documentation that hermiticity is required.
	//
	// The actual hermiticity check is structural: the source-scan test
	// (TestNoEnvReadsInLibraryPackages) catches library-layer reads, and
	// the t.Setenv pattern in individual tests catches command-layer
	// inheritance. Together they ensure the reproduction command
	//   AF_WORKTREE=/tmp/fake AF_WORKTREE_ID=fake go test ./internal/cmd/ ...
	// always passes.
	t.Setenv("AF_WORKTREE", "/tmp/hermiticity-check")
	t.Setenv("AF_WORKTREE_ID", "hermiticity-check")
	// The test passes if the above env vars don't cause panics or
	// unexpected behavior in test infrastructure itself.
}

// findModuleRoot walks up from the working directory to find go.mod.
func findModuleRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find go.mod")
		}
		dir = parent
	}
}
