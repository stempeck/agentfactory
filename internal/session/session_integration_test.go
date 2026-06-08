//go:build integration

package session

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
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
	mgr := NewManager(tmpDir, "testagent", entry)
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
	mgr := NewManager("/tmp/factory", "testmem", entry)
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

func TestStart_SetsAnthropicModelEnv(t *testing.T) {
	requireClaude(t)
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")

	// Test with model set
	t.Run("with_model", func(t *testing.T) {
		agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "testmodel")
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			t.Fatalf("creating agent dir: %v", err)
		}

		entry := config.AgentEntry{Type: "interactive", Description: "test", Model: "sonnet"}
		mgr := NewManager(tmpDir, "testmodel", entry)
		if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
			t.Fatalf("SetWorktree: %v", err)
		}

		_ = mgr.Start()

		running, _ := mgr.IsRunning()
		if !running {
			t.Skip("session did not start — tmux may not be available")
		}
		defer mgr.Stop()

		out, err := exec.Command("tmux", "show-environment", "-t", mgr.SessionID(), "ANTHROPIC_MODEL").Output()
		if err != nil {
			t.Fatalf("failed to read ANTHROPIC_MODEL from tmux: %v", err)
		}
		if !strings.Contains(string(out), "ANTHROPIC_MODEL=sonnet") {
			t.Errorf("expected ANTHROPIC_MODEL=sonnet, got: %s", string(out))
		}
	})

	// Test without model
	t.Run("without_model", func(t *testing.T) {
		agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "testnomodel")
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			t.Fatalf("creating agent dir: %v", err)
		}

		entry := config.AgentEntry{Type: "interactive", Description: "test"}
		mgr := NewManager(tmpDir, "testnomodel", entry)
		if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
			t.Fatalf("SetWorktree: %v", err)
		}

		_ = mgr.Start()

		running, _ := mgr.IsRunning()
		if !running {
			t.Skip("session did not start — tmux may not be available")
		}
		defer mgr.Stop()

		out, err := exec.Command("tmux", "show-environment", "-t", mgr.SessionID(), "ANTHROPIC_MODEL").CombinedOutput()
		if err == nil && strings.Contains(string(out), "ANTHROPIC_MODEL=") {
			t.Errorf("ANTHROPIC_MODEL should NOT be set when model is empty, got: %s", string(out))
		}
	})
}

func TestStart_SetsEndpointEnvVars(t *testing.T) {
	requireClaude(t)
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")

	t.Run("with_endpoint", func(t *testing.T) {
		agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "testendpoint")
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			t.Fatalf("creating agent dir: %v", err)
		}

		entry := config.AgentEntry{
			Type: "interactive", Description: "test",
			BaseURL: "http://localhost:9999/v1/messages", AuthToken: "endpoint-tok-42",
		}
		mgr := NewManager(tmpDir, "testendpoint", entry)
		if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
			t.Fatalf("SetWorktree: %v", err)
		}

		_ = mgr.Start()

		running, _ := mgr.IsRunning()
		if !running {
			t.Skip("session did not start — tmux may not be available")
		}
		defer mgr.Stop()

		out, err := exec.Command("tmux", "show-environment", "-t", mgr.SessionID(), "ANTHROPIC_BASE_URL").Output()
		if err != nil {
			t.Fatalf("failed to read ANTHROPIC_BASE_URL from tmux: %v", err)
		}
		if !strings.Contains(string(out), "ANTHROPIC_BASE_URL=http://localhost:9999/v1/messages") {
			t.Errorf("expected ANTHROPIC_BASE_URL=http://localhost:9999/v1/messages, got: %s", string(out))
		}

		out, err = exec.Command("tmux", "show-environment", "-t", mgr.SessionID(), "ANTHROPIC_AUTH_TOKEN").Output()
		if err != nil {
			t.Fatalf("failed to read ANTHROPIC_AUTH_TOKEN from tmux: %v", err)
		}
		if !strings.Contains(string(out), "ANTHROPIC_AUTH_TOKEN=endpoint-tok-42") {
			t.Errorf("expected ANTHROPIC_AUTH_TOKEN=endpoint-tok-42, got: %s", string(out))
		}
	})

	t.Run("without_endpoint", func(t *testing.T) {
		agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "testnoendpoint")
		if err := os.MkdirAll(agentDir, 0755); err != nil {
			t.Fatalf("creating agent dir: %v", err)
		}

		entry := config.AgentEntry{Type: "interactive", Description: "test"}
		mgr := NewManager(tmpDir, "testnoendpoint", entry)
		if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
			t.Fatalf("SetWorktree: %v", err)
		}

		_ = mgr.Start()

		running, _ := mgr.IsRunning()
		if !running {
			t.Skip("session did not start — tmux may not be available")
		}
		defer mgr.Stop()

		out, err := exec.Command("tmux", "show-environment", "-t", mgr.SessionID(), "ANTHROPIC_BASE_URL").CombinedOutput()
		if err == nil && strings.Contains(string(out), "ANTHROPIC_BASE_URL=") {
			t.Errorf("ANTHROPIC_BASE_URL should NOT be set when endpoint is empty, got: %s", string(out))
		}

		out, err = exec.Command("tmux", "show-environment", "-t", mgr.SessionID(), "ANTHROPIC_AUTH_TOKEN").CombinedOutput()
		if err == nil && strings.Contains(string(out), "ANTHROPIC_AUTH_TOKEN=") {
			t.Errorf("ANTHROPIC_AUTH_TOKEN should NOT be set when endpoint is empty, got: %s", string(out))
		}
	})
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
	mgr := NewManager(tmpDir, "testagent", entry)
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
