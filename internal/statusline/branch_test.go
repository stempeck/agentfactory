package statusline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadBranch_OnDiskRepo(t *testing.T) {
	repo := t.TempDir()
	initGitRepo(t, repo)
	// Put the repo on a known branch name.
	cmd := exec.Command("git", "checkout", "-b", "feature/xyz")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("checkout: %v\n%s", err, out)
	}
	if got := ReadBranch(repo); got != "feature/xyz" {
		t.Errorf("ReadBranch = %q, want feature/xyz", got)
	}
}

func TestReadBranch_WorktreeGitFile(t *testing.T) {
	base := t.TempDir()
	// A real .git directory elsewhere with a HEAD ref.
	realGit := filepath.Join(base, "realgit")
	if err := os.MkdirAll(realGit, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(realGit, "HEAD"), []byte("ref: refs/heads/wt-branch\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// A worktree dir whose .git is a FILE pointing at realGit (gitdir: ...).
	wt := filepath.Join(base, "wt")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+realGit+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := ReadBranch(wt); got != "wt-branch" {
		t.Errorf("ReadBranch (worktree .git file) = %q, want wt-branch", got)
	}
}

func TestReadBranch_NonRepoReturnsEmpty(t *testing.T) {
	dir := t.TempDir() // no .git, and /tmp is not inside a repo
	if got := ReadBranch(dir); got != "" {
		t.Errorf("ReadBranch on a non-repo = %q, want empty", got)
	}
}

func TestReadBranch_DetachedHeadDoesNotErrorViaDisk(t *testing.T) {
	base := t.TempDir()
	// A .git dir whose HEAD is a raw SHA (detached) — the on-disk fast path must not
	// return a bogus branch; it falls through (subprocess handles it, or "" if no git).
	gitDir := filepath.Join(base, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got := ReadBranch(base)
	// Whatever the fallback yields, it must never surface a control byte or the raw ref line.
	if strings.ContainsAny(got, "\n\x1b") {
		t.Errorf("ReadBranch leaked raw/control content: %q", got)
	}
}
