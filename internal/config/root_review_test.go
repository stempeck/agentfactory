package config

import (
	"os"
	"path/filepath"
	"testing"
)

// writeFactoryJSON drops <dir>/.agentfactory/factory.json, creating parents.
func writeFactoryJSON(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(FactoryConfigPath(dir)), 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if err := os.WriteFile(FactoryConfigPath(dir), []byte(`{"type":"factory","version":1}`+"\n"), 0o644); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
}

// TestResolveMarker_BothMarkersPresent_RedirectWins is the #519 review follow-up
// (unresolved thread 2, root.go:37). A real worktree carries BOTH the git-tracked
// factory.json AND the untracked .factory-root redirect; the whole worktree model
// hangs on the redirect being read FIRST. Every prior fixture writes the redirect
// alone, so this load-bearing ordering was never exercised with both markers
// contending in one directory. If the ordering ever flipped, every worktree agent
// would resolve to its own worktree, mismatch its baked AF_ROOT, and hard-refuse
// every state-writing verb with zero CI signal.
func TestResolveMarker_BothMarkersPresent_RedirectWins(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// The redirect target: a separate, real factory.
	target := filepath.Join(base, "target")
	writeFactoryJSON(t, target)

	// The directory under test carries BOTH its own git-tracked factory.json AND an
	// untracked .factory-root redirect pointing at target — the real worktree shape.
	dir := filepath.Join(base, "wt")
	writeFactoryJSON(t, dir)
	redirect := filepath.Join(filepath.Dir(FactoryConfigPath(dir)), ".factory-root")
	if err := os.WriteFile(redirect, []byte(target+"\n"), 0o644); err != nil {
		t.Fatalf("write .factory-root: %v", err)
	}

	if got := resolveMarker(dir); got != target {
		t.Fatalf("resolveMarker(dir with BOTH markers) = %q, want the redirect target %q (the redirect must beat the local factory.json)", got, target)
	}
	root, err := FindFactoryRoot(dir)
	if err != nil {
		t.Fatalf("FindFactoryRoot: %v", err)
	}
	if root != target {
		t.Fatalf("FindFactoryRoot(dir with BOTH markers) = %q, want %q", root, target)
	}
}

// TestSameResolvedRoot_HardlinkTiebreak is the #519 review follow-up (unresolved
// thread 5, root.go:81). SameResolvedRoot is the single equality comparator now
// shared by K1 (internal/cmd cross-check) and K5 (FindEnclosingRoot). On a
// hardlink/bind-mount edge two DIFFERENT paths can canonicalize differently yet
// point at the same factory.json inode; a string-only comparison (the pre-fix
// sameResolvedRoot) would call them different factories, letting K1 and K5 reach
// opposite conclusions. The os.SameFile tiebreak must equate them.
func TestSameResolvedRoot_HardlinkTiebreak(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	a := filepath.Join(base, "a")
	b := filepath.Join(base, "b")
	writeFactoryJSON(t, a)
	if err := os.MkdirAll(filepath.Dir(FactoryConfigPath(b)), 0o755); err != nil {
		t.Fatalf("mkdir b/.agentfactory: %v", err)
	}
	// b's factory.json is a hardlink to a's — same inode, different directory path.
	if err := os.Link(FactoryConfigPath(a), FactoryConfigPath(b)); err != nil {
		t.Skipf("hardlink unsupported on this filesystem: %v", err)
	}

	// Precondition: the two directory paths canonicalize differently, so a
	// string-only comparator would (wrongly) call them distinct factories.
	if canonicalPath(a) == canonicalPath(b) {
		t.Fatalf("precondition failed: %q and %q canonicalize identically", a, b)
	}
	if !SameResolvedRoot(a, b) {
		t.Fatalf("SameResolvedRoot(%q, %q) = false; the os.SameFile tiebreak must equate hardlinked factory.json inodes", a, b)
	}
	// Symmetric, and a genuinely-distinct pair still compares unequal.
	if !SameResolvedRoot(b, a) {
		t.Fatalf("SameResolvedRoot is not symmetric")
	}
	c := filepath.Join(base, "c")
	writeFactoryJSON(t, c)
	if SameResolvedRoot(a, c) {
		t.Fatalf("SameResolvedRoot equated two genuinely distinct factories %q and %q", a, c)
	}
}
