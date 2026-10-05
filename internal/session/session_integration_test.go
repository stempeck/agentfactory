//go:build integration

package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/tmux"
)

// These tests drive the REAL tmux server via mgr.Start()/Stop(). Under the
// default-build GUARD their destructive ops (NewSession on production-class
// names like af-testmem / af-testagent) panic or no-op, so they run only under
// `make test-integration` (guardMode == false). The //go:build integration tag
// REPLACES the former env-gated runtime skip (Gap 5): the env-gated tests
// previously never ran in EITHER suite (the default skipped them; the
// integration suite does not set that env var). The build tag makes them
// executable under `make test-integration`.
//
// Tests that call the full Manager.Start() path additionally gate on claude via
// requireClaude: that path blocks in tmux.WaitForCommand for ClaudeStartTimeout
// (~60s) waiting for the claude binary to take over the pane. Without claude on
// PATH the wait is pure dead time, and several such tests together exceed
// `go test -timeout`. CI provides tmux but not claude, so these run only where
// claude is installed (developer machines) — the same effective scope the former
// AF_INTEGRATION_TEST gate had, since CI never set that env var either.

// requireClaude skips full-Start() tests when the claude binary is absent (e.g.
// CI), where tmux.WaitForCommand would otherwise burn ClaudeStartTimeout per test.
func requireClaude(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not on PATH — skipping full session-start path (WaitForCommand would burn ClaudeStartTimeout)")
	}
}

func TestStartAndStop(t *testing.T) {
	requireClaude(t)

	// Create a temp workspace with a worktree-style agent dir so the Phase 3.5
	// ErrWorktreeNotSet guard is satisfied and workDir() still resolves to a
	// provisioned directory.
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")
	agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "testagent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("creating agent dir: %v", err)
	}

	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := newTestManager(tmpDir, "testagent", entry)
	if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}

	// Start — should create session (Claude won't actually launch in test, but session will exist)
	// Note: This will timeout on WaitForCommand since Claude isn't installed in test env.
	// The important thing is the session gets created.
	_ = mgr.Start()

	// Check running
	running, _ := mgr.IsRunning()
	if !running {
		t.Skip("session did not start — tmux may not be available")
	}

	// Stop
	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	running, _ = mgr.IsRunning()
	if running {
		t.Fatal("session still running after Stop")
	}
}

func TestSessionStart_RefusesWhenMemoryLow(t *testing.T) {
	orig := checkAvailableMemoryFunc
	checkAvailableMemoryFunc = func() (uint64, error) { return 256, nil } // 256MB < 512MB threshold
	t.Cleanup(func() { checkAvailableMemoryFunc = orig })

	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := newTestManager("/tmp/factory", "testmem", entry)
	_ = mgr.SetWorktree("/tmp/worktree", "wt-abc123")

	// Create the workspace directory so we don't fail on ErrNotProvisioned
	workDir := mgr.WorkDir()
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		t.Fatalf("creating workspace: %v", err)
	}

	// Start requires tmux — if not available, skip
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	err := mgr.Start()
	if err == nil {
		// Clean up tmux session if it was created
		mgr.Stop()
		t.Fatal("expected error for low memory, got nil")
	}
	if !strings.Contains(err.Error(), "insufficient memory") {
		// Could also fail for other reasons (shell not ready, etc.)
		// Only fail if tmux worked but memory check didn't fire
		if strings.Contains(err.Error(), "waiting for shell") || strings.Contains(err.Error(), "tmux") {
			t.Skip("tmux shell readiness issue, cannot test memory gate")
		}
		t.Errorf("expected error about insufficient memory, got: %v", err)
	}
}

// quintet is the only env tmux carries for an af session; every config-derived value rides the
// launch line into the process instead.
var quintet = []string{"AF_ROOT", "AF_ROLE", "AF_ACTOR", "AF_WORKTREE", "AF_WORKTREE_ID"}

// afFamilyPrefixes are the key families af writes. A tmux-env key under one of them that is not
// in the quintet is a config-derived copy the launch line can disagree with.
var afFamilyPrefixes = []string{"AF_", "ANTHROPIC_", "OTEL_", "CLAUDE_CODE_", "GIT_AUTHOR_", "GIT_COMMITTER_", "GIT_CONFIG_"}

// isolateTmuxServer points every tmux call in this test at a private server started from this
// process, so the panes inherit the stub `claude` on PATH and the factory's own server is never
// touched. The login shell tmux starts resets PATH from /etc/profile, so HOME's .bash_profile
// puts the stub back in front.
func isolateTmuxServer(t *testing.T, binDir string) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("/bin/bash not available")
	}
	// t.TempDir paths carry the test name and can push the socket past the sun_path limit.
	sockDir, err := os.MkdirTemp("", "afenv")
	if err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	profile := "export PATH='" + binDir + "':\"$PATH\"\n"
	if err := os.WriteFile(filepath.Join(home, ".bash_profile"), []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_TMPDIR", sockDir)
	t.Setenv("HOME", home)
	t.Setenv("SHELL", "/bin/bash")
	t.Setenv("PATH", binDir+":"+os.Getenv("PATH"))
	t.Cleanup(func() {
		_ = exec.Command("tmux", "kill-server").Run()
		_ = os.RemoveAll(sockDir)
	})
}

// writeStubClaude installs a `claude` that records its environment as launch-<n>.env in envDir
// and then stays alive, so Start's WaitForCommand sees a non-shell foreground command.
func writeStubClaude(t *testing.T, binDir, envDir string) {
	t.Helper()
	script := "#!/bin/sh\n" +
		"n=$(ls '" + envDir + "' | grep -c '^launch-')\n" +
		"env > '" + envDir + "'/launch-$n.env.tmp && mv '" + envDir + "'/launch-$n.env.tmp '" + envDir + "'/launch-$n.env\n" +
		"exec sleep 600\n"
	if err := os.WriteFile(filepath.Join(binDir, "claude"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// awaitLaunchEnv returns the environment the n-th launch of the stub recorded.
func awaitLaunchEnv(t *testing.T, envDir string, n int) map[string]string {
	t.Helper()
	path := filepath.Join(envDir, fmt.Sprintf("launch-%d.env", n))
	deadline := time.Now().Add(15 * time.Second)
	for {
		data, err := os.ReadFile(path)
		if err == nil {
			env := map[string]string{}
			for _, line := range strings.Split(string(data), "\n") {
				if k, v, ok := strings.Cut(line, "="); ok {
					env[k] = v
				}
			}
			return env
		}
		if time.Now().After(deadline) {
			t.Fatalf("launch %d never recorded its env at %s", n, path)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// TestStart_LaunchEnvInProcessNotTmux drives real tmux end to end: the config-derived env reaches
// the launched process on the first Start and again after a respawn, and the tmux session env holds
// no af-family key beyond the quintet for a manually opened window to inherit.
func TestStart_LaunchEnvInProcessNotTmux(t *testing.T) {
	binDir, envDir := t.TempDir(), t.TempDir()
	writeStubClaude(t, binDir, envDir)
	isolateTmuxServer(t, binDir)

	root := t.TempDir()
	wtPath := filepath.Join(root, ".worktrees", "wt-env")
	agent := "test-" + hashName(t.Name())
	if err := os.MkdirAll(filepath.Join(wtPath, ".agentfactory", "agents", agent), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(root, agent, config.AgentEntry{Type: "autonomous", Description: "test"})
	if err := mgr.SetWorktree(wtPath, "wt-env"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}
	mgr.c.ModelEnv = []config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-launchenv"},
		{Key: "ANTHROPIC_BASE_URL", Value: "http://127.0.0.1:9/v1"},
	}
	mgr.c.GitAuthorName, mgr.c.GitAuthorEmail = "Launch Env", "launchenv@example.com"
	mgr.c.BuildHost = &config.BuildHostConfig{Mode: "local"}

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })

	want := map[string]string{
		"AF_ROOT":            root,
		"AF_ROLE":            agent,
		"AF_ACTOR":           agent,
		"AF_WORKTREE":        wtPath,
		"AF_WORKTREE_ID":     "wt-env",
		"ANTHROPIC_MODEL":    "claude-launchenv",
		"ANTHROPIC_BASE_URL": "http://127.0.0.1:9/v1",
		envGitAuthorName:     "Launch Env",
		envGitAuthorEmail:    "launchenv@example.com",
		"AF_BUILD_MODE":      "local",
	}
	assertProcessEnv := func(label string, env map[string]string) {
		t.Helper()
		for k, v := range want {
			if env[k] != v {
				t.Errorf("%s: process %s=%q, want %q", label, k, env[k], v)
			}
		}
	}
	assertProcessEnv("Start", awaitLaunchEnv(t, envDir, 0))

	line, err := mgr.BuildStartupCommand()
	if err != nil {
		t.Fatalf("BuildStartupCommand: %v", err)
	}
	if err := tmux.NewTmux().RespawnPane(mgr.SessionID(), line); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	assertProcessEnv("respawn", awaitLaunchEnv(t, envDir, 1))

	out, err := exec.Command("tmux", "show-environment", "-t", mgr.SessionID()).Output()
	if err != nil {
		t.Fatalf("tmux show-environment: %v", err)
	}
	got := map[string]bool{}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, _, _ := strings.Cut(strings.TrimPrefix(line, "-"), "=")
		got[key] = true
	}
	for _, key := range quintet {
		if !got[key] {
			t.Errorf("tmux env lacks quintet key %s:\n%s", key, out)
		}
	}
	for key := range got {
		if slices.Contains(quintet, key) {
			continue
		}
		for _, prefix := range afFamilyPrefixes {
			if strings.HasPrefix(key, prefix) {
				t.Errorf("tmux env carries af-family key %s beyond the quintet:\n%s", key, out)
			}
		}
	}
}

// TestPR724_T9_RespawnScrubDropsStaleSessionCopiesKeepsGlobal mirrors a session an older af left
// behind: its tmux session env still holds config-derived copies. Scrubbing them before the respawn
// must hand the new process none of the stale values while a tmux-global value shows through.
func TestPR724_T9_RespawnScrubDropsStaleSessionCopiesKeepsGlobal(t *testing.T) {
	binDir, envDir := t.TempDir(), t.TempDir()
	writeStubClaude(t, binDir, envDir)
	isolateTmuxServer(t, binDir)

	tm := tmux.NewTmux()
	unsetter, ok := any(tm).(interface {
		UnsetEnvironment(target string, keys ...string) error
	})
	if !ok {
		t.Fatal("*tmux.Tmux has no UnsetEnvironment(target string, keys ...string) error, so a recycle cannot scrub stale session env")
	}

	root := t.TempDir()
	wtPath := filepath.Join(root, ".worktrees", "wt-scrub")
	agent := "test-" + hashName(t.Name())
	if err := os.MkdirAll(filepath.Join(wtPath, ".agentfactory", "agents", agent), 0o755); err != nil {
		t.Fatal(err)
	}
	mgr := newTestManager(root, agent, config.AgentEntry{Type: "autonomous", Description: "test"})
	if err := mgr.SetWorktree(wtPath, "wt-scrub"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}
	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Stop() })
	awaitLaunchEnv(t, envDir, 0)

	session := mgr.SessionID()
	stale := map[string]string{
		"AF_BUILD_HOST":       "stale-mac.example",
		envGitAuthorName:      "stale-sess",
		config.EnvEffortLevel: "stale-high",
	}
	for k, v := range stale {
		if out, err := exec.Command("tmux", "set-environment", "-t", session, k, v).CombinedOutput(); err != nil {
			t.Fatalf("seeding session-scope %s: %v: %s", k, err, out)
		}
	}
	if out, err := exec.Command("tmux", "set-environment", "-g", envGitAuthorName, "global-op").CombinedOutput(); err != nil {
		t.Fatalf("seeding global %s: %v: %s", envGitAuthorName, err, out)
	}
	paneOut, err := exec.Command("tmux", "display-message", "-p", "-t", session, "#{pane_id}").Output()
	if err != nil {
		t.Fatalf("resolving pane id: %v", err)
	}
	pane := strings.TrimSpace(string(paneOut))

	if err := unsetter.UnsetEnvironment(pane, mgr.StaleTmuxEnvKeys()...); err != nil {
		t.Fatalf("UnsetEnvironment(%s): %v", pane, err)
	}
	line, err := mgr.BuildStartupCommand()
	if err != nil {
		t.Fatalf("BuildStartupCommand: %v", err)
	}
	if err := tm.RespawnPane(session, line); err != nil {
		t.Fatalf("RespawnPane: %v", err)
	}
	env := awaitLaunchEnv(t, envDir, 1)

	for k, v := range stale {
		if env[k] == v {
			t.Errorf("respawned process inherited the stale session copy %s=%q", k, v)
		}
	}
	if got := env[envGitAuthorName]; got != "global-op" {
		t.Errorf("respawned process %s=%q, want the tmux-global %q to show through once the session copy is gone", envGitAuthorName, got, "global-op")
	}

	out, err := exec.Command("tmux", "show-environment", "-t", session).Output()
	if err != nil {
		t.Fatalf("tmux show-environment: %v", err)
	}
	for _, entry := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		key, _, _ := strings.Cut(strings.TrimPrefix(entry, "-"), "=")
		if slices.Contains(quintet, key) {
			continue
		}
		for _, prefix := range afFamilyPrefixes {
			if strings.HasPrefix(key, prefix) {
				t.Errorf("tmux session env still lists af-family key %s after the scrub:\n%s", key, out)
			}
		}
	}
}

func TestStop_CleansUpGateLocks(t *testing.T) {
	requireClaude(t)
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")
	agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "testagent")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0755); err != nil {
		t.Fatalf("creating runtime dir: %v", err)
	}

	gateLocks := []string{"fidelity-gate.lock", "quality-gate.lock"}
	for _, name := range gateLocks {
		lockPath := filepath.Join(runtimeDir, name)
		data := `{"pid":99999999,"acquired_at":"2026-01-01T00:00:00Z"}`
		if err := os.WriteFile(lockPath, []byte(data), 0o644); err != nil {
			t.Fatalf("creating %s: %v", name, err)
		}
	}

	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := newTestManager(tmpDir, "testagent", entry)
	if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}

	_ = mgr.Start()
	running, _ := mgr.IsRunning()
	if !running {
		t.Skip("session did not start — tmux may not be available")
	}

	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	for _, name := range gateLocks {
		lockPath := filepath.Join(runtimeDir, name)
		if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
			t.Errorf("%s should be removed after Stop(), but still exists", name)
		}
	}
}
