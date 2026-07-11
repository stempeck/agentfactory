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

// TestFindFactoryRoot_NestedMarkerNearestWins (T-INT-1, #519) pins the library
// invariant the cmd-layer guard exists to compensate for: with TWO factory markers
// on one ancestor path, the nearest-marker walk returns the NEARER one. This is the
// exact substrate of the nested-clone silent capture — enforcement therefore lives in
// internal/cmd (resolveInvokerRoot), not here.
func TestFindFactoryRoot_NestedMarkerNearestWins(t *testing.T) {
	outer := t.TempDir()
	realOuter, err := filepath.EvalSymlinks(outer)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	// Outer marker.
	outerAF := filepath.Join(realOuter, ".agentfactory")
	if err := os.MkdirAll(outerAF, 0755); err != nil {
		t.Fatalf("mkdir outer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outerAF, "factory.json"), []byte(`{"type":"factory","version":1,"name":"outer"}`), 0644); err != nil {
		t.Fatalf("write outer: %v", err)
	}
	// Nearer (inner) marker on the same ancestor path.
	inner := filepath.Join(realOuter, "sub", "inner")
	innerAF := filepath.Join(inner, ".agentfactory")
	if err := os.MkdirAll(innerAF, 0755); err != nil {
		t.Fatalf("mkdir inner: %v", err)
	}
	if err := os.WriteFile(filepath.Join(innerAF, "factory.json"), []byte(`{"type":"factory","version":1,"name":"inner"}`), 0644); err != nil {
		t.Fatalf("write inner: %v", err)
	}

	deep := filepath.Join(inner, "a", "b", "c")
	if err := os.MkdirAll(deep, 0755); err != nil {
		t.Fatalf("mkdir deep: %v", err)
	}

	got, err := FindFactoryRoot(deep)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != inner {
		t.Errorf("nearest marker must win: got %q, want inner %q (outer was %q)", got, inner, realOuter)
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

// TestFindEnclosingRoot_ReturnsDistinctAncestor (K5, #519 Phase 3) pins the
// env-free enclosing scan: from a nested clone's own root, the first ancestor
// carrying a DISTINCT factory marker is returned.
func TestFindEnclosingRoot_ReturnsDistinctAncestor(t *testing.T) {
	outer := t.TempDir()
	realOuter, err := filepath.EvalSymlinks(outer)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	outerAF := filepath.Join(realOuter, ".agentfactory")
	if err := os.MkdirAll(outerAF, 0755); err != nil {
		t.Fatalf("mkdir outer: %v", err)
	}
	if err := os.WriteFile(filepath.Join(outerAF, "factory.json"), []byte(`{"type":"factory","version":1,"name":"outer"}`), 0644); err != nil {
		t.Fatalf("write outer: %v", err)
	}
	clone := filepath.Join(realOuter, "sub", "clone")
	cloneAF := filepath.Join(clone, ".agentfactory")
	if err := os.MkdirAll(cloneAF, 0755); err != nil {
		t.Fatalf("mkdir clone: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cloneAF, "factory.json"), []byte(`{"type":"factory","version":1,"name":"clone"}`), 0644); err != nil {
		t.Fatalf("write clone: %v", err)
	}

	got, err := FindEnclosingRoot(clone)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != realOuter {
		t.Errorf("got %q, want enclosing outer %q", got, realOuter)
	}
}

// TestFindEnclosingRoot_NoneWhenTopLevel: a factory with no enclosing marker
// returns "" and no error (the scan is best-effort observability, not a lookup).
func TestFindEnclosingRoot_NoneWhenTopLevel(t *testing.T) {
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	afDir := filepath.Join(realRoot, ".agentfactory")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"top"}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := FindEnclosingRoot(realRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (no enclosing marker)", got)
	}
}

// TestFindEnclosingRoot_DedupesWorktreeRedirect: a worktree whose .factory-root
// redirect resolves to its outer factory must stay quiet — the enclosing marker
// resolves to the SAME root, so dedupe-by-resolved-root suppresses it.
func TestFindEnclosingRoot_DedupesWorktreeRedirect(t *testing.T) {
	factoryRoot := t.TempDir()
	realFactoryRoot, err := filepath.EvalSymlinks(factoryRoot)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	afDir := filepath.Join(realFactoryRoot, ".agentfactory")
	if err := os.MkdirAll(afDir, 0755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"f"}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	worktree := filepath.Join(realFactoryRoot, ".agentfactory", "worktrees", "wt-x")
	wtAF := filepath.Join(worktree, ".agentfactory")
	if err := os.MkdirAll(wtAF, 0755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtAF, ".factory-root"), []byte(realFactoryRoot+"\n"), 0644); err != nil {
		t.Fatalf("write redirect: %v", err)
	}

	got, err := FindEnclosingRoot(worktree)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "" {
		t.Errorf("got %q, want empty (worktree redirect resolves to same root — must dedupe)", got)
	}
}
