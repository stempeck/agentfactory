//go:build integration

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

// TestGitIdentityEndToEnd is the AC-1 end-to-end evidence: the ACTUAL env that
// session.Manager exports (GIT_AUTHOR_*/GIT_COMMITTER_* for identity, GIT_CONFIG_*
// for core.hooksPath, AF_COAUTHOR_* for the trailer value), when applied to a real
// `git commit` in a fresh no-ambient-identity repo, produces a commit authored by
// agentfactory-cli (AC-2) whose message ends with the exact Co-authored-by trailer
// (AC-4). It ties the session export channel to git's real behavior — not a mock.
//
// Runs under `make test-integration`. Ambient git config is isolated
// (GIT_CONFIG_GLOBAL/SYSTEM=/dev/null) so a host ~/.gitconfig cannot mask the result.
func TestGitIdentityEndToEnd(t *testing.T) {
	base := t.TempDir()
	// Hook execution requires an exec-allowing dir; /tmp is noexec here.
	probe := filepath.Join(base, "probe.sh")
	if err := os.WriteFile(probe, []byte("#!/bin/sh\nexit 0\n"), 0755); err != nil {
		t.Fatal(err)
	}
	if exec.Command(probe).Run() != nil {
		t.Skip("temp dir is noexec; run via `make test-integration` (TMPDIR=$HOME/.cache/af-test)")
	}

	repo := filepath.Join(base, "repo")
	hooksDir := filepath.Join(base, "githooks")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	if err := renderGitHooks(hooksDir); err != nil {
		t.Fatalf("renderGitHooks: %v", err)
	}

	isolatedEnv := append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
	initCmd := exec.Command("git", "init", "-q")
	initCmd.Dir = repo
	initCmd.Env = isolatedEnv
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, "f.txt"), []byte("hi"), 0644); err != nil {
		t.Fatal(err)
	}

	// The resolved state when no ambient identity is present: identity fallback +
	// trailer both active (ResolveIdentity's IFF logic is unit-covered separately).
	mgr := session.NewManager("/tmp/factory", "agent", config.AgentEntry{Type: "autonomous", Description: "test"})
	mgr.SetGitIdentity(config.DefaultGitUserName, config.DefaultGitUserEmail)
	mgr.SetGitTrailer(hooksDir, config.DefaultGitUserName, config.DefaultGitUserEmail)

	startup := mgr.BuildStartupCommand()
	exports := startup[:strings.Index(startup, "&& claude")]

	script := exports + "\n" +
		"git -C '" + repo + "' add f.txt\n" +
		"git -C '" + repo + "' commit -qm 'subject line'\n" +
		"git -C '" + repo + "' log -1 --format='%an|%ae|%B'\n"
	runCmd := exec.Command("bash", "-c", script)
	runCmd.Env = isolatedEnv
	out, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("commit script failed: %v\n%s", err, out)
	}

	got := string(out)
	parts := strings.SplitN(got, "|", 3)
	if len(parts) != 3 {
		t.Fatalf("unexpected log output: %q", got)
	}
	author, email, body := parts[0], parts[1], parts[2]
	if author != config.DefaultGitUserName {
		t.Errorf("author = %q, want %q (AC-2)", author, config.DefaultGitUserName)
	}
	if email != config.DefaultGitUserEmail {
		t.Errorf("author email = %q, want %q (AC-2/AC-3)", email, config.DefaultGitUserEmail)
	}
	wantTrailer := "Co-authored-by: " + config.DefaultGitUserName + " <" + config.DefaultGitUserEmail + ">"
	if !strings.Contains(body, wantTrailer) {
		t.Errorf("commit body missing trailer (AC-4).\nwant: %q\nbody:\n%s", wantTrailer, body)
	}
}
