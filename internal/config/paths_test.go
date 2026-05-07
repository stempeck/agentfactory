package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDir(t *testing.T) {
	got := ConfigDir("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory")
	if got != want {
		t.Errorf("ConfigDir: got %q, want %q", got, want)
	}
}

func TestAgentsDir(t *testing.T) {
	got := AgentsDir("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "agents")
	if got != want {
		t.Errorf("AgentsDir: got %q, want %q", got, want)
	}
}

func TestAgentDir(t *testing.T) {
	got := AgentDir("/tmp/myproject", "manager")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "agents", "manager")
	if got != want {
		t.Errorf("AgentDir: got %q, want %q", got, want)
	}
}

func TestFactoryConfigPath(t *testing.T) {
	got := FactoryConfigPath("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "factory.json")
	if got != want {
		t.Errorf("FactoryConfigPath: got %q, want %q", got, want)
	}
}

func TestAgentsConfigPath(t *testing.T) {
	got := AgentsConfigPath("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "agents.json")
	if got != want {
		t.Errorf("AgentsConfigPath: got %q, want %q", got, want)
	}
}

func TestMessagingConfigPath(t *testing.T) {
	got := MessagingConfigPath("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "messaging.json")
	if got != want {
		t.Errorf("MessagingConfigPath: got %q, want %q", got, want)
	}
}

func TestDispatchConfigPath(t *testing.T) {
	got := DispatchConfigPath("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "dispatch.json")
	if got != want {
		t.Errorf("DispatchConfigPath: got %q, want %q", got, want)
	}
}

func TestHooksDir(t *testing.T) {
	got := HooksDir("/tmp/myproject")
	want := filepath.Join("/tmp/myproject", ".agentfactory", "hooks")
	if got != want {
		t.Errorf("HooksDir: got %q, want %q", got, want)
	}
}

func TestDetectAgentFromCwd_AtAgentRoot(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, ".agentfactory", "agents", "manager")

	got, err := DetectAgentFromCwd(cwd, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "manager" {
		t.Errorf("got %q, want %q", got, "manager")
	}
}

func TestDetectAgentFromCwd_NestedInAgent(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, ".agentfactory", "agents", "manager", "subdir")

	got, err := DetectAgentFromCwd(cwd, root)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "manager" {
		t.Errorf("got %q, want %q", got, "manager")
	}
}

func TestDetectAgentFromCwd_AtFactoryRoot(t *testing.T) {
	root := t.TempDir()

	_, err := DetectAgentFromCwd(root, root)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "cwd is factory root") {
		t.Errorf("error %q should contain %q", err.Error(), "cwd is factory root")
	}
}

func TestDetectAgentFromCwd_AtDotAgentfactory(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, ".agentfactory")

	_, err := DetectAgentFromCwd(cwd, root)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not in an agent workspace") {
		t.Errorf("error %q should contain %q", err.Error(), "not in an agent workspace")
	}
}

func TestDetectAgentFromCwd_AtAgentsDir(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, ".agentfactory", "agents")

	_, err := DetectAgentFromCwd(cwd, root)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "agents directory, not inside a specific agent") {
		t.Errorf("error %q should contain %q", err.Error(), "agents directory, not inside a specific agent")
	}
}

func TestDetectAgentFromCwd_OutsideDotAgentfactory(t *testing.T) {
	root := t.TempDir()
	cwd := filepath.Join(root, "some-random-dir")

	_, err := DetectAgentFromCwd(cwd, root)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	if !strings.Contains(err.Error(), "not inside .agentfactory") {
		t.Errorf("error %q should contain %q", err.Error(), "not inside .agentfactory")
	}
}

// TestDetectAgentFromCwd_WorktreeViaLocalRoot verifies that DetectAgentFromCwd
// succeeds when called with the worktree root (from FindLocalRoot) instead of
// the factory root. This is the core of the fix for issue #78.
func TestDetectAgentFromCwd_WorktreeViaLocalRoot(t *testing.T) {
	// Simulate: factory root is /project, worktree root is
	// /project/.agentfactory/worktrees/wt-abc123. The agent cwd is
	// /project/.agentfactory/worktrees/wt-abc123/.agentfactory/agents/solver.
	// When root = worktree root, Rel produces .agentfactory/agents/solver,
	// which satisfies parts[1] == "agents".
	worktreeRoot := t.TempDir() // stands in for the worktree root
	cwd := filepath.Join(worktreeRoot, ".agentfactory", "agents", "solver")

	got, err := DetectAgentFromCwd(cwd, worktreeRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "solver" {
		t.Errorf("got %q, want %q", got, "solver")
	}
}

// TestDetectAgentFromCwd_WorktreePathFailsWithFactoryRoot documents that
// DetectAgentFromCwd fails when given the factory root and a worktree cwd,
// because the relative path starts with .agentfactory/worktrees/ (parts[1]
// is "worktrees", not "agents"). Callers must use FindLocalRoot first.
func TestDetectAgentFromCwd_WorktreePathFailsWithFactoryRoot(t *testing.T) {
	factoryRoot := t.TempDir()
	cwd := filepath.Join(factoryRoot, ".agentfactory", "worktrees", "wt-abc123",
		".agentfactory", "agents", "solver")

	_, err := DetectAgentFromCwd(cwd, factoryRoot)
	if err == nil {
		t.Fatal("expected error when using factory root for worktree cwd")
	}
	if !strings.Contains(err.Error(), "not in an agent workspace") {
		t.Errorf("error %q should contain %q", err.Error(), "not in an agent workspace")
	}
}
