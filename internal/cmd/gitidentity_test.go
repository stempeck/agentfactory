//go:build !integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// tempDirAllowsExec reports whether scripts under dir can actually be executed.
// /tmp is mounted noexec in this environment (and t.TempDir() defaults there),
// which silently disables git hooks (Gap 2/9). Behavioral hook tests must run in
// an exec-allowing dir (e.g. via `make test`, which sets TMPDIR=$HOME/.cache/af-test).
func tempDirAllowsExec(t *testing.T, dir string) bool {
	t.Helper()
	probe := filepath.Join(dir, "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatalf("write probe: %v", err)
	}
	return exec.Command(probe).Run() == nil
}

func gitRun(t *testing.T, dir string, env []string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = env
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// setupTrailerRepo creates a fresh git repo, installs the source prepare-commit-msg
// hook into a githooks dir (mode 0755), and returns (repoDir, hooksDir, commitEnv).
// commitEnv activates the hook via core.hooksPath (GIT_CONFIG_*) and passes the
// co-author value via AF_COAUTHOR_* — exactly what session.Manager exports.
func setupTrailerRepo(t *testing.T) (string, []string) {
	t.Helper()
	repoRoot := findRepoRoot(t)
	hookSrc := filepath.Join(repoRoot, "hooks", "prepare-commit-msg")
	hookData, err := os.ReadFile(hookSrc)
	if err != nil {
		t.Fatalf("read source hook %s: %v (Phase 3 not implemented yet?)", hookSrc, err)
	}

	base := t.TempDir()
	if !tempDirAllowsExec(t, base) {
		t.Skip("temp dir is noexec; run via `make test` (TMPDIR=$HOME/.cache/af-test)")
	}

	repoDir := filepath.Join(base, "repo")
	hooksDir := filepath.Join(base, "githooks")
	if err := os.MkdirAll(repoDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hooksDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(hooksDir, "prepare-commit-msg"), hookData, 0755); err != nil {
		t.Fatal(err)
	}

	baseEnv := append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=tester", "GIT_AUTHOR_EMAIL=tester@example.com",
		"GIT_COMMITTER_NAME=tester", "GIT_COMMITTER_EMAIL=tester@example.com",
	)
	gitRun(t, repoDir, baseEnv, "init", "-q")

	commitEnv := append(baseEnv,
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=core.hooksPath",
		"GIT_CONFIG_VALUE_0="+hooksDir,
		"AF_COAUTHOR_NAME="+config.DefaultGitUserName,
		"AF_COAUTHOR_EMAIL="+config.DefaultGitUserEmail,
	)
	return repoDir, commitEnv
}

// TestPrepareCommitMsgHook_AppendsTrailer is the AC-4/C-5 behavioral test: an
// agent-session commit produces a message body ending with the exact
// Co-authored-by trailer.
func TestPrepareCommitMsgHook_AppendsTrailer(t *testing.T) {
	repoDir, commitEnv := setupTrailerRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, commitEnv, "add", "f.txt")
	gitRun(t, repoDir, commitEnv, "commit", "-qm", "subject line")

	body := gitRun(t, repoDir, commitEnv, "log", "-1", "--format=%B")
	wantTrailer := "Co-authored-by: " + config.DefaultGitUserName + " <" + config.DefaultGitUserEmail + ">"
	if !strings.Contains(body, wantTrailer) {
		t.Errorf("commit body missing trailer.\nwant trailer: %q\ngot body:\n%s", wantTrailer, body)
	}
	// C-5: trailer is at the very bottom (last non-empty line)
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	if last := lines[len(lines)-1]; last != wantTrailer {
		t.Errorf("trailer must be the last line, got last line %q", last)
	}
}

// TestPrepareCommitMsgHook_Idempotent verifies the trailer survives --amend
// without duplicating (idempotent grep-guard).
func TestPrepareCommitMsgHook_Idempotent(t *testing.T) {
	repoDir, commitEnv := setupTrailerRepo(t)

	if err := os.WriteFile(filepath.Join(repoDir, "f.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, repoDir, commitEnv, "add", "f.txt")
	gitRun(t, repoDir, commitEnv, "commit", "-qm", "subject line")
	gitRun(t, repoDir, commitEnv, "commit", "--amend", "--no-edit", "-q")

	body := gitRun(t, repoDir, commitEnv, "log", "-1", "--format=%B")
	wantTrailer := "Co-authored-by: " + config.DefaultGitUserName + " <" + config.DefaultGitUserEmail + ">"
	if n := strings.Count(body, wantTrailer); n != 1 {
		t.Errorf("trailer count after --amend = %d, want 1\nbody:\n%s", n, body)
	}
}

// TestRenderGitHooks_Executable verifies the installer renders both git hooks
// into the af-managed githooks dir at mode 0755 (Phase 3 deliverable; render at
// install time, not lazily).
func TestRenderGitHooks_Executable(t *testing.T) {
	dir := t.TempDir()
	gitHooksDir := config.GitHooksDir(dir)
	if err := renderGitHooks(gitHooksDir); err != nil {
		t.Fatalf("renderGitHooks: %v", err)
	}
	for _, name := range []string{"prepare-commit-msg", "pre-commit"} {
		p := filepath.Join(gitHooksDir, name)
		info, err := os.Stat(p)
		if err != nil {
			t.Errorf("hook %s not rendered: %v", name, err)
			continue
		}
		if info.Mode().Perm()&0100 == 0 {
			t.Errorf("hook %s is not executable (mode %v)", name, info.Mode().Perm())
		}
	}
}

// TestNoFormulaReferencesCoauthor enforces AC-5 / C-7: the centralized trailer is
// referenced by ZERO formula TOMLs (no per-formula awareness).
func TestNoFormulaReferencesCoauthor(t *testing.T) {
	repoRoot := findRepoRoot(t)
	formulasDir := filepath.Join(repoRoot, "internal", "cmd", "install_formulas")
	entries, err := os.ReadDir(formulasDir)
	if err != nil {
		t.Fatalf("read formulas dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".toml") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(formulasDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(strings.ToLower(string(data)), "co-author") {
			t.Errorf("%s references the trailer — AC-5 forbids per-formula awareness", e.Name())
		}
	}
}

// TestWireGitIdentity_RespectsRepoLocalIdentity is the C-4 regression for the
// presence-gate: when the COMMIT directory (the agent's worktree) already has a
// repo-local identity, the default identity must NOT be exported — otherwise the
// unconditional GIT_AUTHOR_* override would silently re-author the commit. The
// centralized trailer stays active regardless.
func TestWireGitIdentity_RespectsRepoLocalIdentity(t *testing.T) {
	repo := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	gitRun(t, repo, env, "init", "-q")
	gitRun(t, repo, env, "config", "user.name", "Repo Local")
	gitRun(t, repo, env, "config", "user.email", "local@repo.test")

	mgr := session.NewManager(t.TempDir(), "agent", config.AgentEntry{Type: "autonomous", Description: "test"})
	// workDir = the repo with a present identity ⇒ presence-gate must skip the export.
	wireGitIdentity(mgr, t.TempDir(), repo)

	cmd := mgr.BuildStartupCommand()
	if strings.Contains(cmd, "GIT_AUTHOR_NAME") {
		t.Errorf("present repo-local identity must NOT be overridden (C-4), got: %s", cmd)
	}
	if !strings.Contains(cmd, "AF_COAUTHOR_NAME") {
		t.Errorf("trailer should still be active regardless of identity, got: %s", cmd)
	}
}

// TestDetectGitIdentity reads the ambient git identity from a repo's local config
// (the cmd-layer I/O that feeds the pure config.ResolveIdentity).
func TestDetectGitIdentity(t *testing.T) {
	dir := t.TempDir()
	env := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	gitRun(t, dir, env, "init", "-q")
	gitRun(t, dir, env, "config", "user.name", "Ambient User")
	gitRun(t, dir, env, "config", "user.email", "ambient@example.com")

	name, email := detectGitIdentity(dir)
	if name != "Ambient User" {
		t.Errorf("name = %q, want %q", name, "Ambient User")
	}
	if email != "ambient@example.com" {
		t.Errorf("email = %q, want %q", email, "ambient@example.com")
	}
}
