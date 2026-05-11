//go:build integration

package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// setupWorktreeTestWorkspace creates a factory workspace with a specialist agent
// and formula, suitable for testing af sling --agent. Returns (binary, workspace).
func setupWorktreeTestWorkspace(t *testing.T, agentName string) (string, string) {
	t.Helper()

	requirePython3WithServerDeps(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	binary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	// git init with initial commit (required for worktree add)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// Add .gitignore for .agentfactory/ (needed for clean worktree removal)
	gitignorePath := filepath.Join(workspace, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "initial with gitignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// af install --init
	runAF(t, binary, workspace, "install", "--init")

	// Write agents.json with specialist agent (formula field required for sling --agent)
	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	agentsJSON := `{"agents":{` +
		`"manager":{"type":"interactive","description":"manager"},` +
		`"` + agentName + `":{"type":"autonomous","description":"` + agentName + ` agent","formula":"test-dispatch"}` +
		`}}`
	if err := os.WriteFile(agentsPath, []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}

	// Create formula TOML
	formulaDir := config.FormulasDir(workspace)
	formulaContent := "formula = \"test-dispatch\"\ntype = \"workflow\"\nversion = 1\n\n[[steps]]\nid = \"step1\"\ntitle = \"Do the task\"\n"
	if err := os.WriteFile(filepath.Join(formulaDir, "test-dispatch.formula.toml"), []byte(formulaContent), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}

	return binary, workspace
}

func TestSlingAgent_CreatesWorktree(t *testing.T) {
	binary, workspace := setupWorktreeTestWorkspace(t, "solver")

	// Dispatch with --no-launch (creates worktree without tmux)
	runAF(t, binary, workspace, "sling", "--agent", "solver", "test task", "--no-launch")

	// Verify *.meta.json exists in .agentfactory/worktrees/
	wtDir := filepath.Join(workspace, ".agentfactory", "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		t.Fatalf("reading worktrees dir: %v", err)
	}
	var metaFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			metaFiles = append(metaFiles, e.Name())
		}
	}
	if len(metaFiles) != 1 {
		t.Fatalf("expected 1 meta file, got %d: %v", len(metaFiles), metaFiles)
	}

	// Read and verify meta
	metaPath := filepath.Join(wtDir, metaFiles[0])
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("reading meta: %v", err)
	}
	var meta struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("parsing meta: %v", err)
	}
	if meta.Owner != "solver" {
		t.Errorf("meta.Owner: got %q, want %q", meta.Owner, "solver")
	}

	// Verify worktree directory exists
	wtPath := filepath.Join(workspace, meta.Path)
	if _, err := os.Stat(wtPath); err != nil {
		t.Fatalf("worktree dir does not exist: %v", err)
	}

	// Verify agent directory inside worktree
	agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "solver")

	// CLAUDE.md
	if _, err := os.Stat(filepath.Join(agentDir, "CLAUDE.md")); err != nil {
		t.Errorf("CLAUDE.md does not exist: %v", err)
	}

	// settings.json
	if _, err := os.Stat(filepath.Join(agentDir, ".claude", "settings.json")); err != nil {
		t.Errorf("settings.json does not exist: %v", err)
	}

	// .runtime/worktree_id
	wtIDData, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "worktree_id"))
	if err != nil {
		t.Fatalf("reading worktree_id: %v", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(string(wtIDData)), "wt-") {
		t.Errorf("worktree_id: got %q, want wt- prefix", strings.TrimSpace(string(wtIDData)))
	}

	// .runtime/worktree_owner
	ownerData, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "worktree_owner"))
	if err != nil {
		t.Fatalf("reading worktree_owner: %v", err)
	}
	if strings.TrimSpace(string(ownerData)) != "true" {
		t.Errorf("worktree_owner: got %q, want %q", strings.TrimSpace(string(ownerData)), "true")
	}
}

func TestDownAgent_CleansUpWorktree(t *testing.T) {
	binary, workspace := setupWorktreeTestWorkspace(t, "solver")

	// Dispatch with --no-launch to create worktree
	runAF(t, binary, workspace, "sling", "--agent", "solver", "test task", "--no-launch")

	// Read meta to get worktree info
	wtDir := filepath.Join(workspace, ".agentfactory", "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		t.Fatalf("reading worktrees dir: %v", err)
	}
	var metaFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			metaFile = e.Name()
			break
		}
	}
	if metaFile == "" {
		t.Fatal("no meta file found after sling")
	}

	metaData, err := os.ReadFile(filepath.Join(wtDir, metaFile))
	if err != nil {
		t.Fatalf("reading meta: %v", err)
	}
	var meta struct {
		ID     string `json:"id"`
		Branch string `json:"branch"`
		Path   string `json:"path"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("parsing meta: %v", err)
	}

	wtAbsPath := filepath.Join(workspace, meta.Path)
	agentDir := filepath.Join(wtAbsPath, ".agentfactory", "agents", "solver")

	// Create tmux session matching af-{name} pattern
	sessionName := "af-solver"
	tmuxCmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", agentDir)
	if out, err := tmuxCmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %s\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	// Run af down solver
	runAF(t, binary, workspace, "down", "solver")

	// Verify tmux session gone
	if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err == nil {
		t.Error("tmux session should be gone after af down")
	}

	// Verify meta file gone
	metaEntries, _ := os.ReadDir(wtDir)
	for _, e := range metaEntries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			t.Errorf("meta file should be gone, found: %s", e.Name())
		}
	}

	// Verify worktree directory gone
	if _, err := os.Stat(wtAbsPath); !os.IsNotExist(err) {
		t.Error("worktree dir should not exist after af down")
	}

	// Verify branch deleted
	branchCmd := exec.Command("git", "branch", "--list", meta.Branch)
	branchCmd.Dir = workspace
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.Contains(string(branchOut), meta.Branch) {
		t.Errorf("branch %q should not exist after af down", meta.Branch)
	}
}

func TestUpManager_CreatesWorktree(t *testing.T) {
	requirePython3WithServerDeps(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not available; af up requires claude to complete startup")
	}

	binary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	// git init with initial commit (required for git worktree add)
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitignorePath := filepath.Join(workspace, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	runAF(t, binary, workspace, "install", "--init")
	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	if err := os.WriteFile(agentsPath, []byte(`{"agents":{"manager":{"type":"interactive","description":"manager"}}}`), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}
	runAF(t, binary, workspace, "install", "manager")

	sessionName := "af-manager"
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	runAF(t, binary, workspace, "up", "manager")

	if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err != nil {
		t.Fatal("tmux session af-manager should be running after af up")
	}

	wtDir := filepath.Join(workspace, ".agentfactory", "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		t.Fatalf("worktrees dir should exist: %v", err)
	}
	var metaFiles []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			metaFiles = append(metaFiles, e.Name())
		}
	}
	if len(metaFiles) != 1 {
		t.Fatalf("expected 1 meta file after af up manager, got %d: %v", len(metaFiles), metaFiles)
	}

	metaData, err := os.ReadFile(filepath.Join(wtDir, metaFiles[0]))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	var meta struct {
		ID    string `json:"id"`
		Owner string `json:"owner"`
		Path  string `json:"path"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("parse meta: %v", err)
	}
	if meta.Owner != "manager" {
		t.Errorf("meta.Owner: got %q, want %q", meta.Owner, "manager")
	}

	wtAbsPath := filepath.Join(workspace, meta.Path)
	mgrAgentDir := filepath.Join(wtAbsPath, ".agentfactory", "agents", "manager")
	if _, err := os.Stat(filepath.Join(mgrAgentDir, "CLAUDE.md")); err != nil {
		t.Errorf("manager's CLAUDE.md should exist inside the worktree at %s: %v", mgrAgentDir, err)
	}
	ownerData, err := os.ReadFile(filepath.Join(mgrAgentDir, ".runtime", "worktree_owner"))
	if err != nil {
		t.Fatalf("worktree_owner file: %v", err)
	}
	if strings.TrimSpace(string(ownerData)) != "true" {
		t.Errorf("manager should be worktree owner; got %q", strings.TrimSpace(string(ownerData)))
	}

	runAF(t, binary, workspace, "down", "manager")
	if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err == nil {
		t.Error("tmux session should be gone after af down manager")
	}
}

func TestSlingAgent_ResetCleansOldWorktree(t *testing.T) {
	binary, workspace := setupWorktreeTestWorkspace(t, "solver")

	// First dispatch — creates worktree
	runAF(t, binary, workspace, "sling", "--agent", "solver", "task1", "--no-launch")

	// Capture old worktree ID from meta
	wtDir := filepath.Join(workspace, ".agentfactory", "worktrees")
	entries, err := os.ReadDir(wtDir)
	if err != nil {
		t.Fatalf("reading worktrees dir: %v", err)
	}
	var oldMetaFile string
	var oldMeta struct {
		ID     string `json:"id"`
		Branch string `json:"branch"`
		Path   string `json:"path"`
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			oldMetaFile = e.Name()
			data, _ := os.ReadFile(filepath.Join(wtDir, e.Name()))
			json.Unmarshal(data, &oldMeta)
			break
		}
	}
	if oldMetaFile == "" {
		t.Fatal("no meta file found after first dispatch")
	}

	oldWtPath := filepath.Join(workspace, oldMeta.Path)

	// Second dispatch with --reset (no running session, reset still cleans worktree)
	runAF(t, binary, workspace, "sling", "--agent", "solver", "task2", "--no-launch", "--reset")

	// Verify old meta file gone
	if _, err := os.Stat(filepath.Join(wtDir, oldMetaFile)); !os.IsNotExist(err) {
		t.Error("old meta file should be gone after --reset")
	}

	// Verify old worktree directory gone
	if _, err := os.Stat(oldWtPath); !os.IsNotExist(err) {
		t.Error("old worktree dir should be gone after --reset")
	}

	// Verify old branch gone
	branchCmd := exec.Command("git", "branch", "--list", oldMeta.Branch)
	branchCmd.Dir = workspace
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.Contains(string(branchOut), oldMeta.Branch) {
		t.Errorf("old branch %q should not exist after --reset", oldMeta.Branch)
	}

	// Verify new worktree exists with different ID
	newEntries, err := os.ReadDir(wtDir)
	if err != nil {
		t.Fatalf("reading worktrees dir after reset: %v", err)
	}
	var newMetaCount int
	for _, e := range newEntries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			newMetaCount++
			if e.Name() == oldMetaFile {
				t.Error("new meta file should have different name than old")
			}
		}
	}
	if newMetaCount != 1 {
		t.Errorf("expected 1 new meta file, got %d", newMetaCount)
	}
}

// TestUpThenSling_SharedWorktree exercises subprocess-env inheritance (step 4 of
// Scenario #3 chain), not the tmux set-environment + buildStartupCommand leg
// (steps 2–3). That tmux leg is covered by session_test.go unit tests and manual QA.
func TestUpThenSling_SharedWorktree(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not available; af up requires claude to complete startup")
	}
	binary, workspace := setupWorktreeTestWorkspace(t, "solver")

	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	agentsJSON := `{"agents":{` +
		`"manager":{"type":"interactive","description":"manager"},` +
		`"solver":{"type":"autonomous","description":"solver agent","formula":"test-dispatch"}` +
		`}}`
	if err := os.WriteFile(agentsPath, []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}
	runAF(t, binary, workspace, "install", "manager")

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-manager").Run()
	})

	runAF(t, binary, workspace, "up", "manager")

	wtDir := filepath.Join(workspace, ".agentfactory", "worktrees")
	entries, _ := os.ReadDir(wtDir)
	var mgrMetaFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			mgrMetaFile = e.Name()
			break
		}
	}
	if mgrMetaFile == "" {
		t.Fatal("expected manager's worktree meta after af up")
	}
	mgrMetaData, _ := os.ReadFile(filepath.Join(wtDir, mgrMetaFile))
	var mgrMeta struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	json.Unmarshal(mgrMetaData, &mgrMeta)
	mgrWtPath := filepath.Join(workspace, mgrMeta.Path)

	mgrAgentDir := filepath.Join(mgrWtPath, ".agentfactory", "agents", "manager")
	cmd := exec.Command(binary, "sling", "--agent", "solver", "test task", "--no-launch")
	cmd.Dir = mgrAgentDir
	cmd.Env = append(os.Environ(),
		"HOME="+workspace,
		"AF_WORKTREE="+mgrWtPath,
		"AF_WORKTREE_ID="+mgrMeta.ID,
		"AF_ROLE=manager",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sling: %s\n%s", err, out)
	}

	entries, _ = os.ReadDir(wtDir)
	var metaCount int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			metaCount++
		}
	}
	if metaCount != 1 {
		t.Errorf("AC-1: expected 1 worktree meta after parent+child, got %d", metaCount)
	}

	solverAgentDir := filepath.Join(mgrWtPath, ".agentfactory", "agents", "solver")
	if _, err := os.Stat(solverAgentDir); err != nil {
		t.Fatalf("solver's agent dir should be inside manager's worktree: %v", err)
	}

	solverWtID, err := os.ReadFile(filepath.Join(solverAgentDir, ".runtime", "worktree_id"))
	if err != nil {
		t.Fatalf("solver's worktree_id: %v", err)
	}
	if strings.TrimSpace(string(solverWtID)) != mgrMeta.ID {
		t.Errorf("AC-5 clause ii: solver worktree_id %q != manager %q",
			strings.TrimSpace(string(solverWtID)), mgrMeta.ID)
	}

	if _, err := os.Stat(filepath.Join(solverAgentDir, ".runtime", "worktree_owner")); !os.IsNotExist(err) {
		t.Errorf("AC-5: solver should not be worktree owner; worktree_owner file unexpectedly exists")
	}
}

func TestDispatchDiskFallback(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not available; af up requires claude to complete startup")
	}
	binary, workspace := setupWorktreeTestWorkspace(t, "solver")

	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	agentsJSON := `{"agents":{` +
		`"manager":{"type":"interactive","description":"manager"},` +
		`"solver":{"type":"autonomous","description":"solver agent","formula":"test-dispatch"}` +
		`}}`
	os.WriteFile(agentsPath, []byte(agentsJSON), 0o644)
	runAF(t, binary, workspace, "install", "manager")

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-manager").Run()
	})

	runAF(t, binary, workspace, "up", "manager")

	cmd := exec.Command(binary, "sling", "--agent", "solver", "task", "--no-launch")
	cmd.Dir = workspace
	cmd.Env = append(os.Environ(),
		"HOME="+workspace,
		"AF_ROLE=manager",
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sling: %s\n%s", err, out)
	}

	wtDir := filepath.Join(workspace, ".agentfactory", "worktrees")
	entries, _ := os.ReadDir(wtDir)
	var metaCount int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			metaCount++
		}
	}
	if metaCount != 1 {
		t.Errorf("AC-4: expected 1 worktree (disk fallback), got %d", metaCount)
	}
}

func TestParentTwoChildren_SharedWorktree(t *testing.T) {
	if _, err := exec.LookPath("claude"); err != nil {
		t.Skip("claude not available; af up requires claude to complete startup")
	}
	binary, workspace := setupWorktreeTestWorkspace(t, "solver")

	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	agentsJSON := `{"agents":{` +
		`"manager":{"type":"interactive","description":"manager"},` +
		`"solver":{"type":"autonomous","description":"solver","formula":"test-dispatch"},` +
		`"checker":{"type":"autonomous","description":"checker","formula":"test-dispatch"}` +
		`}}`
	os.WriteFile(agentsPath, []byte(agentsJSON), 0o644)
	runAF(t, binary, workspace, "install", "manager")

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-manager").Run()
	})

	runAF(t, binary, workspace, "up", "manager")

	wtDir := filepath.Join(workspace, ".agentfactory", "worktrees")
	entries, _ := os.ReadDir(wtDir)
	var mgrMetaFile string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			mgrMetaFile = e.Name()
			break
		}
	}
	mgrMetaData, _ := os.ReadFile(filepath.Join(wtDir, mgrMetaFile))
	var mgrMeta struct {
		ID   string `json:"id"`
		Path string `json:"path"`
	}
	json.Unmarshal(mgrMetaData, &mgrMeta)
	mgrWtPath := filepath.Join(workspace, mgrMeta.Path)
	mgrAgentDir := filepath.Join(mgrWtPath, ".agentfactory", "agents", "manager")

	env := append(os.Environ(),
		"HOME="+workspace,
		"AF_WORKTREE="+mgrWtPath,
		"AF_WORKTREE_ID="+mgrMeta.ID,
		"AF_ROLE=manager",
	)

	for _, child := range []string{"solver", "checker"} {
		cmd := exec.Command(binary, "sling", "--agent", child, "task", "--no-launch")
		cmd.Dir = mgrAgentDir
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("sling %s: %s\n%s", child, err, out)
		}
	}

	entries, _ = os.ReadDir(wtDir)
	var metaCount int
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".meta.json") {
			metaCount++
		}
	}
	if metaCount != 1 {
		t.Errorf("AC-2/5: expected 1 meta across parent+2 children, got %d", metaCount)
	}

	readID := func(agentName string) string {
		data, err := os.ReadFile(filepath.Join(mgrWtPath, ".agentfactory", "agents", agentName, ".runtime", "worktree_id"))
		if err != nil {
			t.Fatalf("read worktree_id for %s: %v", agentName, err)
		}
		return strings.TrimSpace(string(data))
	}
	mgrID := readID("manager")
	solverID := readID("solver")
	checkerID := readID("checker")
	if mgrID != solverID || mgrID != checkerID {
		t.Errorf("AC-5: three-way ID equality failed — mgr=%q solver=%q checker=%q",
			mgrID, solverID, checkerID)
	}

	solverDir := filepath.Join(mgrWtPath, ".agentfactory", "agents", "solver")
	checkerDir := filepath.Join(mgrWtPath, ".agentfactory", "agents", "checker")
	testFile := filepath.Join(solverDir, "output.txt")
	if err := os.WriteFile(testFile, []byte("hello from solver"), 0o644); err != nil {
		t.Fatalf("write test file: %v", err)
	}
	relRead, err := os.ReadFile(filepath.Join(checkerDir, "..", "solver", "output.txt"))
	if err != nil {
		t.Errorf("AC-2: cross-child relative read failed: %v", err)
	} else if string(relRead) != "hello from solver" {
		t.Errorf("AC-2: got %q, want %q", string(relRead), "hello from solver")
	}
}

// TestLaunchAgentSession_EmptyWorktreePath exercises the launchAgentSession package
// var directly with empty worktree path/ID. Post-Phase 3.5, Start() without
// SetWorktree returns session.ErrWorktreeNotSet. This test is the regression guard.
func TestLaunchAgentSession_EmptyWorktreePath(t *testing.T) {
	requirePython3WithServerDeps(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	binary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}
	gitignorePath := filepath.Join(workspace, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	runAF(t, binary, workspace, "install", "--init")
	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	if err := os.WriteFile(agentsPath, []byte(`{"agents":{"manager":{"type":"interactive","description":"manager"}}}`), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}
	runAF(t, binary, workspace, "install", "manager")

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-manager").Run()
	})

	var stdout, stderr bytes.Buffer
	cobraCmd := &cobra.Command{}
	cobraCmd.SetOut(&stdout)
	cobraCmd.SetErr(&stderr)

	err := launchAgentSession(cobraCmd, workspace, "manager", "", "")
	if err == nil {
		t.Fatal("expected error from launchAgentSession with empty worktree path, got nil")
	}
	if !errors.Is(err, session.ErrWorktreeNotSet) {
		t.Errorf("expected session.ErrWorktreeNotSet, got: %v", err)
	}

	if err := exec.Command("tmux", "has-session", "-t", "=af-manager").Run(); err == nil {
		t.Error("tmux session af-manager should NOT exist after ErrWorktreeNotSet")
	}
}

func TestInstallInit_RejectsWorktree(t *testing.T) {
	requirePython3WithServerDeps(t)

	binary := buildAF(t)
	workspace := t.TempDir()
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("eval symlinks on workspace: %v", err)
	}
	ensurePySymlink(t, realWorkspace)
	t.Cleanup(func() { terminateMCPServer(realWorkspace) })

	// git init with initial commit
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realWorkspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// .gitignore + commit
	if err := os.WriteFile(filepath.Join(realWorkspace, ".gitignore"), []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "initial with gitignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realWorkspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// af install --init at factory root
	runAF(t, binary, realWorkspace, "install", "--init")

	// Create worktree via Go API (avoids tmux/claude dependency)
	wtPath, _, err := worktree.Create(realWorkspace, "solver", worktree.CreateOpts{})
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}

	// Run af install --init from inside the worktree — should fail
	out, cmdErr := runAFMayFail(t, binary, wtPath, "install", "--init")
	if cmdErr == nil {
		t.Fatal("expected af install --init to fail inside worktree, but it succeeded")
	}
	if !strings.Contains(out, "cannot run") || !strings.Contains(out, "inside a worktree") {
		t.Errorf("error output should contain 'cannot run' and 'inside a worktree', got: %s", out)
	}
}

func TestWorktreeLifecycle_SymlinksAndTeardown(t *testing.T) {
	requirePython3WithServerDeps(t)

	binary := buildAF(t)
	workspace := t.TempDir()
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("eval symlinks on workspace: %v", err)
	}
	ensurePySymlink(t, realWorkspace)
	t.Cleanup(func() { terminateMCPServer(realWorkspace) })

	// git init with initial commit
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realWorkspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// .gitignore with .agentfactory/ (standard)
	if err := os.WriteFile(filepath.Join(realWorkspace, ".gitignore"), []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "initial with gitignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realWorkspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// af install --init
	runAF(t, binary, realWorkspace, "install", "--init")

	// Write agents.json with solver agent
	agentsPath := filepath.Join(realWorkspace, ".agentfactory", "agents.json")
	if err := os.WriteFile(agentsPath, []byte(`{"agents":{"manager":{"type":"interactive","description":"manager"},"solver":{"type":"autonomous","description":"solver agent"}}}`), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}

	// Verify .git/info/exclude contains sentinel
	excludeData, err := os.ReadFile(filepath.Join(realWorkspace, ".git", "info", "exclude"))
	if err != nil {
		t.Fatalf("reading .git/info/exclude: %v", err)
	}
	if !strings.Contains(string(excludeData), "agentfactory managed paths") {
		t.Error(".git/info/exclude missing 'agentfactory managed paths' sentinel")
	}

	// Create factory-root resources that symlinks will target
	os.MkdirAll(filepath.Join(realWorkspace, ".claude", "skills"), 0o755)
	os.MkdirAll(filepath.Join(realWorkspace, ".runtime"), 0o755)
	if _, err := os.Stat(filepath.Join(realWorkspace, "AGENTS.md")); os.IsNotExist(err) {
		os.WriteFile(filepath.Join(realWorkspace, "AGENTS.md"), []byte("# Agents\n"), 0o644)
	}

	// Create worktree via Go API
	wtPath, meta, err := worktree.Create(realWorkspace, "solver", worktree.CreateOpts{})
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}

	// Verify symlinks
	for _, rel := range []string{filepath.Join(".claude", "skills"), ".runtime", "AGENTS.md"} {
		link := filepath.Join(wtPath, rel)
		target, err := os.Readlink(link)
		if err != nil {
			t.Errorf("readlink %s: %v", rel, err)
			continue
		}
		expected := filepath.Join(realWorkspace, rel)
		if target != expected {
			t.Errorf("symlink %s: got target %q, want %q", rel, target, expected)
		}
	}

	// Verify .agentfactory/ is a real directory (not a symlink)
	afInfo, err := os.Lstat(filepath.Join(wtPath, ".agentfactory"))
	if err != nil {
		t.Fatalf("lstat .agentfactory: %v", err)
	}
	if afInfo.Mode()&os.ModeSymlink != 0 {
		t.Error(".agentfactory should be a real directory, not a symlink")
	}

	// SetupAgent and verify CLAUDE.md contains factory root path
	_, err = worktree.SetupAgent(realWorkspace, wtPath, "solver", true)
	if err != nil {
		t.Fatalf("worktree.SetupAgent: %v", err)
	}
	claudeData, err := os.ReadFile(filepath.Join(wtPath, ".agentfactory", "agents", "solver", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claudeData), realWorkspace) {
		t.Errorf("CLAUDE.md should contain factory root %q", realWorkspace)
	}

	// Create test file at factory root's .claude/skills/test-skill/ and verify visible via symlink
	testSkillDir := filepath.Join(realWorkspace, ".claude", "skills", "test-skill")
	os.MkdirAll(testSkillDir, 0o755)
	os.WriteFile(filepath.Join(testSkillDir, "SKILL.md"), []byte("# Test Skill\n"), 0o644)

	wtSkillPath := filepath.Join(wtPath, ".claude", "skills", "test-skill", "SKILL.md")
	if _, err := os.Stat(wtSkillPath); err != nil {
		t.Errorf("test skill file not visible in worktree via symlink: %v", err)
	}

	// ForceRemove and verify factory root resources intact
	if err := worktree.ForceRemove(realWorkspace, meta); err != nil {
		t.Fatalf("worktree.ForceRemove: %v", err)
	}

	for _, rel := range []string{filepath.Join(".claude", "skills"), ".runtime", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(realWorkspace, rel)); err != nil {
			t.Errorf("factory root resource %s missing after teardown: %v", rel, err)
		}
	}
}

func TestWorktreeLifecycle_WithHostGitignore(t *testing.T) {
	requirePython3WithServerDeps(t)

	binary := buildAF(t)
	workspace := t.TempDir()
	realWorkspace, err := filepath.EvalSymlinks(workspace)
	if err != nil {
		t.Fatalf("eval symlinks on workspace: %v", err)
	}
	ensurePySymlink(t, realWorkspace)
	t.Cleanup(func() { terminateMCPServer(realWorkspace) })

	// git init with initial commit
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realWorkspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// .gitignore with BOTH .agentfactory/ AND .claude/ (AC-4 scenario)
	if err := os.WriteFile(filepath.Join(realWorkspace, ".gitignore"), []byte(".agentfactory/\n.claude/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "initial with gitignore excluding .claude/"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = realWorkspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// af install --init
	runAF(t, binary, realWorkspace, "install", "--init")

	// Write agents.json with solver agent
	agentsPath := filepath.Join(realWorkspace, ".agentfactory", "agents.json")
	if err := os.WriteFile(agentsPath, []byte(`{"agents":{"manager":{"type":"interactive","description":"manager"},"solver":{"type":"autonomous","description":"solver agent"}}}`), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}

	// Create factory-root resources
	os.MkdirAll(filepath.Join(realWorkspace, ".claude", "skills"), 0o755)
	os.MkdirAll(filepath.Join(realWorkspace, ".runtime"), 0o755)
	if _, err := os.Stat(filepath.Join(realWorkspace, "AGENTS.md")); os.IsNotExist(err) {
		os.WriteFile(filepath.Join(realWorkspace, "AGENTS.md"), []byte("# Agents\n"), 0o644)
	}

	// Create worktree via Go API
	wtPath, meta, err := worktree.Create(realWorkspace, "solver", worktree.CreateOpts{})
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}

	// Verify symlinks work despite .claude/ being gitignored
	for _, rel := range []string{filepath.Join(".claude", "skills"), ".runtime", "AGENTS.md"} {
		link := filepath.Join(wtPath, rel)
		target, err := os.Readlink(link)
		if err != nil {
			t.Errorf("readlink %s: %v", rel, err)
			continue
		}
		expected := filepath.Join(realWorkspace, rel)
		if target != expected {
			t.Errorf("symlink %s: got target %q, want %q", rel, target, expected)
		}
	}

	// SetupAgent and verify CLAUDE.md contains factory root
	_, err = worktree.SetupAgent(realWorkspace, wtPath, "solver", true)
	if err != nil {
		t.Fatalf("worktree.SetupAgent: %v", err)
	}
	claudeData, err := os.ReadFile(filepath.Join(wtPath, ".agentfactory", "agents", "solver", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	if !strings.Contains(string(claudeData), realWorkspace) {
		t.Errorf("CLAUDE.md should contain factory root %q", realWorkspace)
	}

	// ForceRemove and verify factory root resources intact
	if err := worktree.ForceRemove(realWorkspace, meta); err != nil {
		t.Fatalf("worktree.ForceRemove: %v", err)
	}

	for _, rel := range []string{filepath.Join(".claude", "skills"), ".runtime", "AGENTS.md"} {
		if _, err := os.Stat(filepath.Join(realWorkspace, rel)); err != nil {
			t.Errorf("factory root resource %s missing after teardown: %v", rel, err)
		}
	}
}
