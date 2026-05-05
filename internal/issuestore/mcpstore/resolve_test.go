package mcpstore_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

func makePyDir(t *testing.T, root string) {
	t.Helper()
	pyDir := filepath.Join(root, "py")
	if err := os.MkdirAll(pyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pyDir, "__init__.py"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolvePyPath(t *testing.T) {
	t.Cleanup(func() {
		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot("")
	})

	t.Run("factoryRoot_wins", func(t *testing.T) {
		factoryRoot := t.TempDir()
		makePyDir(t, factoryRoot)

		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot("")

		got, err := mcpstore.ResolvePyPath(factoryRoot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != factoryRoot {
			t.Errorf("got %q, want %q", got, factoryRoot)
		}
	})

	t.Run("sourceRoot_fallback", func(t *testing.T) {
		factoryRoot := t.TempDir()
		sourceRoot := t.TempDir()
		makePyDir(t, sourceRoot)

		mcpstore.SetSourceRoot(sourceRoot)
		mcpstore.SetEnvSourceRoot("")

		got, err := mcpstore.ResolvePyPath(factoryRoot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != sourceRoot {
			t.Errorf("got %q, want %q", got, sourceRoot)
		}
	})

	t.Run("env_var_fallback", func(t *testing.T) {
		factoryRoot := t.TempDir()
		envRoot := t.TempDir()
		makePyDir(t, envRoot)

		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot(envRoot)

		got, err := mcpstore.ResolvePyPath(factoryRoot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != envRoot {
			t.Errorf("got %q, want %q", got, envRoot)
		}
	})

	t.Run("all_missing_errors", func(t *testing.T) {
		factoryRoot := t.TempDir()

		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot("")

		_, err := mcpstore.ResolvePyPath(factoryRoot)
		if err == nil {
			t.Fatal("expected error when py/ not found anywhere")
		}
		if !strings.Contains(err.Error(), "cannot locate py/ package") {
			t.Errorf("error should mention 'cannot locate py/ package', got: %v", err)
		}
	})

	t.Run("priority_order", func(t *testing.T) {
		factoryRoot := t.TempDir()
		sourceRoot := t.TempDir()
		envRoot := t.TempDir()
		makePyDir(t, factoryRoot)
		makePyDir(t, sourceRoot)
		makePyDir(t, envRoot)

		mcpstore.SetSourceRoot(sourceRoot)
		mcpstore.SetEnvSourceRoot(envRoot)

		got, err := mcpstore.ResolvePyPath(factoryRoot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != factoryRoot {
			t.Errorf("factoryRoot should win when all three have py/; got %q, want %q", got, factoryRoot)
		}
	})

	t.Run("sourceRoot_empty_skips_to_env", func(t *testing.T) {
		factoryRoot := t.TempDir()
		envRoot := t.TempDir()
		makePyDir(t, envRoot)

		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot(envRoot)

		got, err := mcpstore.ResolvePyPath(factoryRoot)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != envRoot {
			t.Errorf("got %q, want %q", got, envRoot)
		}
	})
}
