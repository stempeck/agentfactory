package cmd

import (
	"os"
	"path/filepath"
	"testing"
)

// setupWorktreeFixture creates a realistic worktree filesystem layout:
//
//	factoryRoot/.agentfactory/factory.json
//	factoryRoot/.agentfactory/agents.json  (contains agentName)
//	factoryRoot/.agentfactory/worktrees/wt-test/.agentfactory/.factory-root -> factoryRoot
//	factoryRoot/.agentfactory/worktrees/wt-test/.agentfactory/agents/<agentName>/
//
// Returns (factoryRoot, worktreeAgentDir).
func setupWorktreeFixture(t *testing.T, agentName string) (string, string) {
	t.Helper()
	factoryRoot := t.TempDir()

	// Factory-root config
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"`+agentName+`":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	// Worktree structure
	wtRoot := filepath.Join(afDir, "worktrees", "wt-test")
	wtAfDir := filepath.Join(wtRoot, ".agentfactory")
	wtAgentDir := filepath.Join(wtAfDir, "agents", agentName)
	os.MkdirAll(wtAgentDir, 0o755)

	// .factory-root redirect so FindFactoryRoot resolves to factoryRoot
	os.WriteFile(filepath.Join(wtAfDir, ".factory-root"), []byte(factoryRoot), 0o644)

	return factoryRoot, wtAgentDir
}

// setupFactoryFixture creates a standard (non-worktree) agent filesystem layout:
//
//	factoryRoot/.agentfactory/factory.json
//	factoryRoot/.agentfactory/agents.json  (contains agentName)
//	factoryRoot/.agentfactory/agents/<agentName>/
//
// Returns (factoryRoot, agentDir).
func setupFactoryFixture(t *testing.T, agentName string) (string, string) {
	t.Helper()
	factoryRoot := t.TempDir()

	afDir := filepath.Join(factoryRoot, ".agentfactory")
	agentDir := filepath.Join(afDir, "agents", agentName)
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"`+agentName+`":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	return factoryRoot, agentDir
}

func TestDetectSender_WorktreeAgent(t *testing.T) {
	_, wtAgentDir := setupWorktreeFixture(t, "solver")

	got, err := detectSender(wtAgentDir)
	if err != nil {
		t.Fatalf("detectSender from worktree agent dir: %v", err)
	}
	if got != "solver" {
		t.Errorf("detectSender = %q, want %q", got, "solver")
	}
}

func TestDetectSender_FactoryAgent_NoRegression(t *testing.T) {
	_, agentDir := setupFactoryFixture(t, "manager")

	got, err := detectSender(agentDir)
	if err != nil {
		t.Fatalf("detectSender from factory agent dir: %v", err)
	}
	if got != "manager" {
		t.Errorf("detectSender = %q, want %q", got, "manager")
	}
}

// TestDetectSender_WrongButNoError_HonorsAF_ROLE pins the fix for GitHub
// issue #88 at the detectSender boundary. Pre-fix, a cwd at a typo directory
// raised "agent not found in agents.json" even when AF_ROLE was set correctly
// by session.Manager — because the membership check at the wrapper fired
// before the AND-gate could ever consult AF_ROLE.
func TestDetectSender_WrongButNoError_HonorsAF_ROLE(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "solver")

	got, err := detectSender(typoDir)
	if err != nil {
		t.Fatalf("detectSender with AF_ROLE fallback: %v", err)
	}
	if got != "solver" {
		t.Errorf("detectSender = %q, want %q (AF_ROLE overrides wrong path-derived name)", got, "solver")
	}
}

// TestDetectSender_WrongButNoError_NoAF_ROLE_Errors verifies that without
// AF_ROLE, detectSender errors clearly naming the membership failure.
func TestDetectSender_WrongButNoError_NoAF_ROLE_Errors(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "")

	got, err := detectSender(typoDir)
	if err == nil {
		t.Fatalf("detectSender should error for unknown agent, got %q", got)
	}
	if got == "typo" {
		t.Errorf("detectSender must not return wrong path-derived name %q silently", got)
	}
}

func TestDetectSender_WorktreeAgent_AF_ROLE_Fallback(t *testing.T) {
	// Set up a worktree where path-based detection works,
	// but also verify AF_ROLE is respected when paths fail.
	factoryRoot := t.TempDir()
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"solver":{"type":"autonomous","description":"test"}}}`), 0o644)

	// Create a worktree dir that has .factory-root but agent is NOT under
	// the standard .agentfactory/agents/ path — simulating a case where
	// path detection fails.
	wtRoot := filepath.Join(afDir, "worktrees", "wt-test")
	wtAfDir := filepath.Join(wtRoot, ".agentfactory")
	os.MkdirAll(wtAfDir, 0o755)
	os.WriteFile(filepath.Join(wtAfDir, ".factory-root"), []byte(factoryRoot), 0o644)

	// cwd is inside the worktree .agentfactory but NOT in agents/ subdir
	cwd := wtAfDir

	t.Setenv("AF_ROLE", "solver")

	got, err := detectSender(cwd)
	if err != nil {
		t.Fatalf("detectSender with AF_ROLE fallback: %v", err)
	}
	if got != "solver" {
		t.Errorf("detectSender = %q, want %q", got, "solver")
	}
}
