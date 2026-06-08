//go:build integration

package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// These tests stand up / kill REAL af-solver tmux sessions to prove GC respects
// a live session and force-removes a dead one. The behavior depends on a real
// tmux has-session result, so it cannot be faked — the tests are gated behind
// //go:build integration and run only under `make test-integration` (#309).

func TestGC_DoesNotRemoveRunningSession(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)
	addGitignore(t, realDir)

	absPath, _, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	sessionName := "af-solver"
	_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	startCmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName)
	if out, err := startCmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	removed, err := GC(realDir)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if removed != 0 {
		t.Errorf("GC removed %d worktrees, want 0 (session af-solver is running)", removed)
	}

	if _, err := os.Stat(absPath); err != nil {
		t.Errorf("worktree dir should still exist when session is running: %v", err)
	}
}

func TestGC_ForceRemovesDeadSession(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	_ = exec.Command("tmux", "kill-session", "-t", "af-solver").Run()

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)
	addGitignore(t, realDir)

	absPath, _, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	dirtyFile := filepath.Join(absPath, "dirty.txt")
	if err := os.WriteFile(dirtyFile, []byte("uncommitted"), 0o644); err != nil {
		t.Fatalf("write dirty file: %v", err)
	}
	stageCmd := exec.Command("git", "add", "dirty.txt")
	stageCmd.Dir = absPath
	if out, err := stageCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	removed, err := GC(realDir)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}

	if removed != 1 {
		t.Errorf("GC removed %d worktrees, want 1 (session dead, dirty worktree should be force-removed)", removed)
	}

	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should not exist after GC force-removes dead session, got err: %v", err)
	}
}
