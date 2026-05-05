package config

import (
	"os"
	"path/filepath"
	"testing"
)

func setupSourceTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	realDir, _ := filepath.EvalSymlinks(dir)
	if err := os.WriteFile(filepath.Join(realDir, "go.mod"), []byte("module github.com/stempeck/agentfactory\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	return realDir
}

func TestResolveSourceRoot_FactoryRootIsSourceTree(t *testing.T) {
	root := setupSourceTree(t)

	SetBuildSourceRoot("")
	SetEnvSourceRoot("")

	got, err := ResolveSourceRoot(root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != root {
		t.Errorf("got %q, want %q", got, root)
	}
}

func TestResolveSourceRoot_BuildTimeSourceRoot(t *testing.T) {
	factoryRoot := t.TempDir()
	sourceRoot := setupSourceTree(t)

	SetBuildSourceRoot(sourceRoot)
	t.Setenv("AF_SOURCE_ROOT", "")

	got, err := ResolveSourceRoot(factoryRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sourceRoot {
		t.Errorf("got %q, want %q", got, sourceRoot)
	}
}

func TestResolveSourceRoot_EnvVar(t *testing.T) {
	factoryRoot := t.TempDir()
	sourceRoot := setupSourceTree(t)

	SetBuildSourceRoot("")
	SetEnvSourceRoot(sourceRoot)

	got, err := ResolveSourceRoot(factoryRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != sourceRoot {
		t.Errorf("got %q, want %q", got, sourceRoot)
	}
}

func TestResolveSourceRoot_ErrorWhenNoneAvailable(t *testing.T) {
	factoryRoot := t.TempDir()

	SetBuildSourceRoot("")
	SetEnvSourceRoot("")

	_, err := ResolveSourceRoot(factoryRoot)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestResolveSourceRoot_ValidatesEnvVar(t *testing.T) {
	factoryRoot := t.TempDir()
	badDir := t.TempDir()
	// badDir has no go.mod

	SetBuildSourceRoot("")
	SetEnvSourceRoot(badDir)

	_, err := ResolveSourceRoot(factoryRoot)
	if err == nil {
		t.Fatal("expected error for invalid AF_SOURCE_ROOT, got nil")
	}
}

func TestResolveSourceRoot_ValidatesBuildTime(t *testing.T) {
	factoryRoot := t.TempDir()
	badDir := t.TempDir()
	// badDir has no go.mod

	SetBuildSourceRoot(badDir)
	t.Setenv("AF_SOURCE_ROOT", "")

	_, err := ResolveSourceRoot(factoryRoot)
	if err == nil {
		t.Fatal("expected error for invalid build-time source root, got nil")
	}
}

func TestResolveSourceRoot_PriorityOrder(t *testing.T) {
	factoryRoot := setupSourceTree(t)
	buildRoot := setupSourceTree(t)
	envRoot := setupSourceTree(t)

	SetBuildSourceRoot(buildRoot)
	SetEnvSourceRoot(envRoot)

	// Factory root (self-hosted) wins over build-time and env
	got, err := ResolveSourceRoot(factoryRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != factoryRoot {
		t.Errorf("got %q, want %q (factory root should win)", got, factoryRoot)
	}
}

func TestResolveSourceRoot_BuildTimeOverEnv(t *testing.T) {
	factoryRoot := t.TempDir()
	buildRoot := setupSourceTree(t)
	envRoot := setupSourceTree(t)

	SetBuildSourceRoot(buildRoot)
	SetEnvSourceRoot(envRoot)

	// Build-time wins over env when factory root is not source tree
	got, err := ResolveSourceRoot(factoryRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != buildRoot {
		t.Errorf("got %q, want %q (build-time should win over env)", got, buildRoot)
	}
}

func TestIsAgentFactorySourceTree(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    bool
	}{
		{"valid", "module github.com/stempeck/agentfactory\n\ngo 1.24\n", true},
		{"no module line", "go 1.24\n", false},
		{"wrong module", "module github.com/other/project\n\ngo 1.24\n", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			if tt.content != "" {
				if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(tt.content), 0644); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			if got := isAgentFactorySourceTree(dir); got != tt.want {
				t.Errorf("isAgentFactorySourceTree() = %v, want %v", got, tt.want)
			}
		})
	}
}
