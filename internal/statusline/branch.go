package statusline

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const headRefPrefix = "ref: refs/heads/"

// ReadBranch returns the current branch name for the repository at dir, or "" if it cannot be
// determined. It NEVER errors: a missing repo, an unreadable HEAD, a detached HEAD with no git
// available, or a git that is not installed all degrade to "". It tries an on-disk
// .git/HEAD parse first (no subprocess), then falls back to `git rev-parse --abbrev-ref HEAD`
// — the branch-read idiom used everywhere in this repo (checkpoint.go:160-166); `--abbrev-ref`
// (not `--show-current`, which appears nowhere here and needs git ≥ 2.22).
func ReadBranch(dir string) string {
	if b := branchFromDisk(dir); b != "" {
		return b
	}
	return branchFromGit(dir)
}

// branchFromDisk reads dir/.git — a directory (normal repo) or a "gitdir: …" file (worktree) —
// and returns the branch from HEAD when it is a symbolic ref. A detached HEAD (raw SHA) or any
// read failure returns "", deferring to the subprocess fallback.
func branchFromDisk(dir string) string {
	gitPath := filepath.Join(dir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	gitDir := gitPath
	if !info.IsDir() {
		data, err := os.ReadFile(gitPath)
		if err != nil {
			return ""
		}
		line := strings.TrimSpace(string(data))
		const p = "gitdir:"
		if !strings.HasPrefix(line, p) {
			return ""
		}
		gd := strings.TrimSpace(strings.TrimPrefix(line, p))
		if !filepath.IsAbs(gd) {
			gd = filepath.Join(dir, gd)
		}
		gitDir = gd
	}
	head, err := os.ReadFile(filepath.Join(gitDir, "HEAD"))
	if err != nil {
		return ""
	}
	line := strings.TrimSpace(string(head))
	if strings.HasPrefix(line, headRefPrefix) {
		return strings.TrimPrefix(line, headRefPrefix)
	}
	return ""
}

func branchFromGit(dir string) string {
	cmd := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	branch := strings.TrimSpace(string(out))
	// On a detached HEAD (tag/SHA checkout, rebase, bisect) `--abbrev-ref` prints the literal
	// sentinel "HEAD". Surfacing it would show a bogus branch element reading "HEAD"; degrade
	// to "" instead, honoring ReadBranch's "degrade to ''" contract (PR #595 T2/F9).
	if branch == "HEAD" {
		return ""
	}
	return branch
}
