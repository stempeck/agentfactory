package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFindFactoryRoot_FromRootItself(t *testing.T) {
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Resolve symlinks for macOS/temp dir consistency
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	got, err := FindFactoryRoot(realRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != realRoot {
		t.Errorf("got %q, want %q", got, realRoot)
	}
}

func TestFindFactoryRoot_FromNestedDir(t *testing.T) {
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	nested := filepath.Join(root, "some", "deep", "nested", "dir")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	realNested, err := filepath.EvalSymlinks(nested)
	if err != nil {
		t.Fatalf("eval symlinks nested: %v", err)
	}

	got, err := FindFactoryRoot(realNested)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != realRoot {
		t.Errorf("got %q, want %q", got, realRoot)
	}
}

func TestFindFactoryRoot_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := FindFactoryRoot(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}

func TestFindFactoryRoot_OldLayoutDiagnostic(t *testing.T) {
	root := t.TempDir()
	// Create old-layout config/factory.json but NOT .agentfactory/factory.json
	oldConfigDir := filepath.Join(root, "config")
	if err := os.MkdirAll(oldConfigDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(oldConfigDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	_, err = FindFactoryRoot(realRoot)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	errMsg := err.Error()
	if !strings.Contains(errMsg, "old-layout") {
		t.Errorf("error %q should contain %q", errMsg, "old-layout")
	}
	if !strings.Contains(errMsg, "af install --init") {
		t.Errorf("error %q should contain %q", errMsg, "af install --init")
	}
}

func TestFindFactoryRoot_WithRedirectFile(t *testing.T) {
	// Set up a real factory root
	factoryRoot := t.TempDir()
	realFactoryRoot, err := filepath.EvalSymlinks(factoryRoot)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	afDir := filepath.Join(realFactoryRoot, ".agentfactory")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Set up a worktree-like directory with .factory-root redirect
	worktreeDir := t.TempDir()
	realWorktreeDir, err := filepath.EvalSymlinks(worktreeDir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	wtAfDir := filepath.Join(realWorktreeDir, ".agentfactory")
	if err := os.MkdirAll(wtAfDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtAfDir, ".factory-root"), []byte(realFactoryRoot+"\n"), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	got, err := FindFactoryRoot(realWorktreeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != realFactoryRoot {
		t.Errorf("got %q, want %q", got, realFactoryRoot)
	}
}

func TestFindFactoryRoot_InvalidRedirectFile(t *testing.T) {
	// Redirect file points to directory without factory.json — should NOT follow it
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	afDir := filepath.Join(realDir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	// Redirect to a directory that does NOT have factory.json
	if err := os.WriteFile(filepath.Join(afDir, ".factory-root"), []byte("/nonexistent\n"), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	// FindFactoryRoot should NOT return /nonexistent and should fail (no factory.json anywhere)
	_, err = FindFactoryRoot(realDir)
	if err == nil {
		t.Fatal("expected error for invalid redirect, got nil")
	}
}

func TestFindLocalRoot_ReturnsWorktreeRoot(t *testing.T) {
	// Set up factory root
	factoryRoot := t.TempDir()
	realFactoryRoot, err := filepath.EvalSymlinks(factoryRoot)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	afDir := filepath.Join(realFactoryRoot, ".agentfactory")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Set up worktree dir INSIDE factory root (as it would be in practice)
	worktreeDir := filepath.Join(realFactoryRoot, ".agentfactory", "worktrees", "wt-test01")
	wtAfDir := filepath.Join(worktreeDir, ".agentfactory")
	if err := os.MkdirAll(wtAfDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtAfDir, ".factory-root"), []byte(realFactoryRoot+"\n"), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	// FindLocalRoot from inside worktree should return worktree root (nearest .agentfactory/)
	got, err := FindLocalRoot(worktreeDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != worktreeDir {
		t.Errorf("got %q, want %q (worktree root)", got, worktreeDir)
	}
}

func TestFindLocalRoot_ReturnsFactoryRoot(t *testing.T) {
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	afDir := filepath.Join(realRoot, ".agentfactory")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	nested := filepath.Join(realRoot, "some", "deep", "dir")
	if err := os.MkdirAll(nested, 0755); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}

	got, err := FindLocalRoot(nested)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != realRoot {
		t.Errorf("got %q, want %q", got, realRoot)
	}
}

func TestFindLocalRoot_NotFound(t *testing.T) {
	dir := t.TempDir()
	_, err := FindLocalRoot(dir)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
}
