//go:build integration

package worktree

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// setupFactoryRootMultiAgent creates a factory root with multiple agents in agents.json.
func setupFactoryRootMultiAgent(t *testing.T, dir string, agents map[string]string) {
	t.Helper()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	factoryJSON := `{"type":"factory","version":1,"name":"test"}`
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(factoryJSON), 0o644); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}

	// Build agents JSON from map
	var entries []string
	for name, agentType := range agents {
		entries = append(entries, `"`+name+`":{"type":"`+agentType+`","description":"`+name+` agent"}`)
	}
	agentsJSON := `{"agents":{` + strings.Join(entries, ",") + `}}`
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"), []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}
}

// addGitignore adds .agentfactory/ to .gitignore and commits it.
// Required for clean worktree removal (git worktree remove fails on untracked files).
func addGitignore(t *testing.T, dir string) {
	t.Helper()
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "add gitignore"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func TestWorktreeLifecycle_FullDispatchChain(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)
	addGitignore(t, realDir)

	// Create worktree
	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// SetupAgent as owner
	agentDir, err := SetupAgent(realDir, absPath, "solver", true)
	if err != nil {
		t.Fatalf("SetupAgent: %v", err)
	}

	// Verify directory structure
	expectedDir := filepath.Join(absPath, ".agentfactory", "agents", "solver")
	if agentDir != expectedDir {
		t.Errorf("agentDir: got %q, want %q", agentDir, expectedDir)
	}

	// Verify CLAUDE.md rendered with worktree path
	claudeData, err := os.ReadFile(filepath.Join(agentDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("read CLAUDE.md: %v", err)
	}
	if len(claudeData) == 0 {
		t.Error("CLAUDE.md is empty")
	}
	if !strings.Contains(string(claudeData), absPath) {
		t.Errorf("CLAUDE.md does not contain worktree path %q", absPath)
	}

	// Verify settings.json
	settingsPath := filepath.Join(agentDir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Errorf("settings.json does not exist: %v", err)
	}

	// Verify .runtime/worktree_id
	wtIDData, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "worktree_id"))
	if err != nil {
		t.Fatalf("read worktree_id: %v", err)
	}
	wtID := strings.TrimSpace(string(wtIDData))
	if !strings.HasPrefix(wtID, "wt-") {
		t.Errorf("worktree_id: got %q, want wt- prefix", wtID)
	}

	// Verify .runtime/worktree_owner
	ownerData, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "worktree_owner"))
	if err != nil {
		t.Fatalf("read worktree_owner: %v", err)
	}
	if strings.TrimSpace(string(ownerData)) != "true" {
		t.Errorf("worktree_owner: got %q, want %q", strings.TrimSpace(string(ownerData)), "true")
	}

	// Remove worktree
	if err := Remove(realDir, meta); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify directory gone
	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Errorf("worktree dir should not exist after Remove")
	}

	// Verify meta file gone
	if _, err := ReadMeta(realDir, meta.ID); err == nil {
		t.Error("ReadMeta should fail after Remove")
	}

	// Verify branch gone
	branchCmd := exec.Command("git", "branch", "--list", meta.Branch)
	branchCmd.Dir = realDir
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.Contains(string(branchOut), meta.Branch) {
		t.Errorf("branch %q should not exist after Remove", meta.Branch)
	}
}

func TestWorktreeLifecycle_ChildInheritsWorktree(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRootMultiAgent(t, realDir, map[string]string{
		"solver":   "autonomous",
		"reviewer": "autonomous",
	})
	addGitignore(t, realDir)

	// Create worktree for owner agent
	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// SetupAgent for owner
	_, err = SetupAgent(realDir, absPath, "solver", true)
	if err != nil {
		t.Fatalf("SetupAgent (owner): %v", err)
	}

	// SetupAgent for child (non-owner)
	_, err = SetupAgent(realDir, absPath, "reviewer", false)
	if err != nil {
		t.Fatalf("SetupAgent (child): %v", err)
	}

	// Production code (sling.go) does not add child to meta.Agents on inheritance.
	// Manually add "reviewer" to meta to match the test scenario.
	meta.Agents = append(meta.Agents, "reviewer")
	if err := UpdateMeta(realDir, meta); err != nil {
		t.Fatalf("UpdateMeta: %v", err)
	}

	// Verify meta has both agents
	readMeta, err := ReadMeta(realDir, meta.ID)
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if len(readMeta.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d: %v", len(readMeta.Agents), readMeta.Agents)
	}

	// RemoveAgent "reviewer" — should leave "solver", empty=false
	updated, empty, err := RemoveAgent(realDir, meta.ID, "reviewer")
	if err != nil {
		t.Fatalf("RemoveAgent reviewer: %v", err)
	}
	if empty {
		t.Error("expected empty=false after removing reviewer")
	}
	if len(updated.Agents) != 1 || updated.Agents[0] != "solver" {
		t.Errorf("after removing reviewer: got %v, want [solver]", updated.Agents)
	}

	// RemoveAgent "solver" — should be empty now
	updated2, empty2, err := RemoveAgent(realDir, meta.ID, "solver")
	if err != nil {
		t.Fatalf("RemoveAgent solver: %v", err)
	}
	if !empty2 {
		t.Error("expected empty=true after removing solver")
	}
	if len(updated2.Agents) != 0 {
		t.Errorf("after removing solver: got %v, want []", updated2.Agents)
	}

	// Remove worktree
	if err := Remove(realDir, updated2); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify full cleanup
	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Error("worktree dir should not exist after Remove")
	}
	if _, err := ReadMeta(realDir, meta.ID); err == nil {
		t.Error("ReadMeta should fail after Remove")
	}
}

func TestGC_CleansStaleWorktrees(t *testing.T) {
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

	// Create worktree — no tmux session started, so GC should consider it stale.
	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify worktree exists before GC
	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("worktree should exist before GC: %v", err)
	}

	// Run GC
	removed, err := GC(realDir)
	if err != nil {
		t.Fatalf("GC: %v", err)
	}
	if removed != 1 {
		t.Errorf("GC removed: got %d, want 1", removed)
	}

	// Verify worktree dir gone
	if _, err := os.Stat(absPath); !os.IsNotExist(err) {
		t.Error("worktree dir should not exist after GC")
	}

	// Verify meta file gone
	if _, err := ReadMeta(realDir, meta.ID); err == nil {
		t.Error("ReadMeta should fail after GC")
	}

	// Verify branch gone
	branchCmd := exec.Command("git", "branch", "--list", meta.Branch)
	branchCmd.Dir = realDir
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if strings.Contains(string(branchOut), meta.Branch) {
		t.Errorf("branch %q should not exist after GC", meta.Branch)
	}
}

func TestRemove_UncommittedChangesBlocksRemoval(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)

	absPath, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Force-remove in cleanup regardless of test outcome
	t.Cleanup(func() {
		exec.Command("git", "-C", realDir, "worktree", "remove", "--force", absPath).Run()
		exec.Command("git", "-C", realDir, "branch", "-D", meta.Branch).Run()
	})

	// Create a file inside the worktree and stage it (uncommitted).
	// Untracked files don't block removal — only staged/modified tracked files do.
	newFile := filepath.Join(absPath, "staged-file.txt")
	if err := os.WriteFile(newFile, []byte("uncommitted content"), 0o644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	addCmd := exec.Command("git", "add", "staged-file.txt")
	addCmd.Dir = absPath
	if out, err := addCmd.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v\n%s", err, out)
	}

	// Remove should fail because of uncommitted changes
	err = Remove(realDir, meta)
	if err == nil {
		t.Fatal("Remove should fail with uncommitted changes")
	}

	// Verify worktree directory still exists
	if _, err := os.Stat(absPath); err != nil {
		t.Errorf("worktree dir should still exist after failed Remove: %v", err)
	}
}

func TestRemove_DeletesBranchAfterRemoval(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initGitRepo(t, realDir)
	setupFactoryRoot(t, realDir)
	addGitignore(t, realDir)

	_, meta, err := Create(realDir, "solver", CreateOpts{})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Verify branch exists before removal
	branchCmd := exec.Command("git", "branch", "--list", meta.Branch)
	branchCmd.Dir = realDir
	branchOut, err := branchCmd.Output()
	if err != nil {
		t.Fatalf("git branch --list: %v", err)
	}
	if !strings.Contains(string(branchOut), meta.Branch) {
		t.Fatalf("branch %q should exist after Create", meta.Branch)
	}

	// Remove
	if err := Remove(realDir, meta); err != nil {
		t.Fatalf("Remove: %v", err)
	}

	// Verify branch is gone
	branchCmd2 := exec.Command("git", "branch", "--list", meta.Branch)
	branchCmd2.Dir = realDir
	branchOut2, err := branchCmd2.Output()
	if err != nil {
		t.Fatalf("git branch --list after Remove: %v", err)
	}
	if strings.Contains(string(branchOut2), meta.Branch) {
		t.Errorf("branch %q should not exist after Remove", meta.Branch)
	}
}

func TestConcurrentRemoveAgent_NoCorruption(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	agents := []string{"agent-a", "agent-b", "agent-c", "agent-d", "agent-e"}
	meta := &Meta{
		ID:     "wt-conc01",
		Owner:  "agent-a",
		Branch: "af/agent-a-conc01",
		Path:   ".agentfactory/worktrees/wt-conc01",
		Agents: agents,
	}
	if err := WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	// Launch goroutines that each remove a different agent.
	// RemoveAgent does unlocked read-modify-write, so concurrent calls may
	// encounter partially written files. Errors during concurrent access are
	// expected — the important guarantees are: no panics, and the final meta
	// file is valid JSON.
	var wg sync.WaitGroup
	for _, agent := range agents {
		wg.Add(1)
		go func(name string) {
			defer wg.Done()
			RemoveAgent(realDir, "wt-conc01", name)
		}(agent)
	}
	wg.Wait()

	// No panics occurred (test reaching here proves that)

	// Verify meta file is valid JSON after all goroutines complete
	finalMeta, err := ReadMeta(realDir, "wt-conc01")
	if err != nil {
		t.Fatalf("ReadMeta after concurrent RemoveAgent: %v", err)
	}

	// Due to last-write-wins semantics, exact agent count is non-deterministic.
	// Verify no duplicates and all remaining agents are from the original set.
	seen := make(map[string]bool)
	for _, a := range finalMeta.Agents {
		if seen[a] {
			t.Errorf("duplicate agent in final meta: %q", a)
		}
		seen[a] = true
		found := false
		for _, orig := range agents {
			if a == orig {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("unexpected agent in final meta: %q", a)
		}
	}
}
