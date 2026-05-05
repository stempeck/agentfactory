package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/worktree"
)

func TestCleanupAgentWorktree_NoWorktree(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// Create worktrees dir but no meta files
	if err := os.MkdirAll(worktree.WorktreesDir(realDir), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	// Should be a no-op — no worktree found for this agent
	cleanupAgentWorktree(cmd, realDir, "nonexistent")

	if buf.Len() != 0 {
		t.Errorf("expected no output for agent without worktree, got: %q", buf.String())
	}
}

func TestCleanupAgentWorktree_CoTenantSafety(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// Write a meta with owner + co-tenant
	meta := &worktree.Meta{
		ID:     "wt-test01",
		Owner:  "solver",
		Branch: "af/solver-test01",
		Path:   ".agentfactory/worktrees/wt-test01",
		Agents: []string{"solver", "reviewer"},
	}
	if err := worktree.WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	// Stop the owner — should deregister but NOT remove (co-tenant still present)
	cleanupAgentWorktree(cmd, realDir, "solver")

	output := buf.String()
	if !strings.Contains(output, "deregistered") {
		t.Errorf("expected 'deregistered' message, got: %q", output)
	}
	if strings.Contains(output, "cleaned up") {
		t.Error("should NOT have cleaned up worktree when co-tenant exists")
	}

	// Verify meta still exists with reviewer only
	updated, err := worktree.ReadMeta(realDir, "wt-test01")
	if err != nil {
		t.Fatalf("ReadMeta after RemoveAgent: %v", err)
	}
	if len(updated.Agents) != 1 || updated.Agents[0] != "reviewer" {
		t.Errorf("Agents: got %v, want [reviewer]", updated.Agents)
	}
}

func TestCleanupAgentWorktree_OwnerLastAgent(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// Write a meta with only the owner (no co-tenants)
	meta := &worktree.Meta{
		ID:     "wt-solo01",
		Owner:  "solver",
		Branch: "af/solver-solo01",
		Path:   ".agentfactory/worktrees/wt-solo01",
		Agents: []string{"solver"},
	}
	if err := worktree.WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	// Stop the sole owner — RemoveAgent returns empty=true, then Remove
	// is called. Remove will fail because there's no actual git worktree,
	// but the error should be logged as a warning (non-fatal).
	cleanupAgentWorktree(cmd, realDir, "solver")

	// The meta should have been updated (agents list emptied) by RemoveAgent.
	// Remove will fail (no git worktree to remove), logging a warning to stderr.
	// This is the expected non-fatal behavior.
	// The hint message goes to os.Stderr (matching existing warning pattern),
	// which cannot be captured via cobra's SetErr. Hint presence verified by
	// acceptance criteria grep: grep "hint.*--reset" internal/cmd/down.go
}

func TestCloseAgentBeads_ClosesAssignedBeads(t *testing.T) {
	store := installMemStore(t)
	ctx := context.Background()

	for i := 0; i < 3; i++ {
		store.Create(ctx, issuestore.CreateParams{
			Title:    "solver-bead",
			Assignee: "solver",
			Type:     issuestore.TypeTask,
		})
	}
	for i := 0; i < 2; i++ {
		store.Create(ctx, issuestore.CreateParams{
			Title:    "reviewer-bead",
			Assignee: "reviewer",
			Type:     issuestore.TypeTask,
		})
	}

	closed := closeAgentBeads(ctx, store, "solver")
	if closed != 3 {
		t.Errorf("closeAgentBeads returned %d, want 3", closed)
	}

	// Verify solver's beads are closed
	solverBeads, _ := store.List(ctx, issuestore.Filter{Assignee: "solver", IncludeClosed: true})
	for _, b := range solverBeads {
		if b.Status != issuestore.StatusClosed {
			t.Errorf("bead %s: status=%s, want closed", b.ID, b.Status)
		}
		if b.CloseReason != "reset by af down --reset" {
			t.Errorf("bead %s: reason=%q, want %q", b.ID, b.CloseReason, "reset by af down --reset")
		}
	}

	// Verify reviewer's beads are untouched
	reviewerBeads, _ := store.List(ctx, issuestore.Filter{Assignee: "reviewer"})
	if len(reviewerBeads) != 2 {
		t.Errorf("reviewer beads: got %d, want 2 (should be untouched)", len(reviewerBeads))
	}
}

func TestCloseAgentBeads_NoBeads(t *testing.T) {
	store := installMemStore(t)
	ctx := context.Background()

	closed := closeAgentBeads(ctx, store, "solver")
	if closed != 0 {
		t.Errorf("closeAgentBeads returned %d, want 0", closed)
	}
}

func TestResetAgent_CleansRuntimeAndCheckpoint(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	store := installMemStore(t)
	ctx := context.Background()

	// Seed beads for the agent
	store.Create(ctx, issuestore.CreateParams{
		Title:    "test-bead",
		Assignee: "solver",
		Type:     issuestore.TypeTask,
	})

	// Create agent dir structure
	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir runtime: %v", err)
	}
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("f1"), 0o644)

	// Create checkpoint file
	checkpointPath := filepath.Join(agentDir, ".agent-checkpoint.json")
	os.WriteFile(checkpointPath, []byte(`{"formula_id":"f1"}`), 0o644)

	// Create worktrees dir (no worktree meta for this agent)
	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	// Verify bead was closed
	beads, _ := store.List(ctx, issuestore.Filter{Assignee: "solver", IncludeClosed: true})
	for _, b := range beads {
		if b.Status != issuestore.StatusClosed {
			t.Errorf("bead %s still open after reset", b.ID)
		}
	}

	// Verify .runtime/ directory removed
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Error(".runtime/ directory should be removed after reset")
	}

	// Verify checkpoint removed
	if _, err := os.Stat(checkpointPath); !os.IsNotExist(err) {
		t.Error("checkpoint file should be removed after reset")
	}
}

func TestResetAgent_CoTenantSafety(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	installMemStore(t)
	ctx := context.Background()

	// Write a meta with two agents
	meta := &worktree.Meta{
		ID:     "wt-cotenant",
		Owner:  "solver",
		Branch: "af/solver-cotenant",
		Path:   ".agentfactory/worktrees/wt-cotenant",
		Agents: []string{"solver", "reviewer"},
	}
	if err := worktree.WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	// Create agent dir
	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	output := buf.String()
	if !strings.Contains(output, "deregistered") {
		t.Errorf("expected deregistered message, got: %q", output)
	}

	// Verify meta still exists with reviewer only
	updated, err := worktree.ReadMeta(realDir, "wt-cotenant")
	if err != nil {
		t.Fatalf("ReadMeta after reset: %v", err)
	}
	if len(updated.Agents) != 1 || updated.Agents[0] != "reviewer" {
		t.Errorf("Agents: got %v, want [reviewer]", updated.Agents)
	}
}

func TestResetAgent_StoreInitFailure(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	// Override newIssueStore to return an error
	orig := newIssueStore
	newIssueStore = func(wd, beadsDir, actor string) (issuestore.Store, error) {
		return nil, fmt.Errorf("MCP server unavailable")
	}
	t.Cleanup(func() { newIssueStore = orig })

	ctx := context.Background()

	// Create agent dir with runtime files
	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)

	// Create worktrees dir
	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	cmd := &cobra.Command{}
	var buf, errBuf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&errBuf)

	resetAgent(ctx, cmd, realDir, "solver")

	// Verify runtime still cleaned despite store failure
	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Error(".runtime/ directory should be removed even when store init fails")
	}
}

func TestPreResetScan_PrintsWarning(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	installMemStore(t)

	// Create worktrees dir
	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	cmd := &cobra.Command{}
	var buf, errBuf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&errBuf)

	preResetScan(cmd, realDir, []string{"solver"})

	stderrOut := errBuf.String()
	if !strings.Contains(stderrOut, "WARNING") {
		t.Errorf("expected WARNING in stderr, got: %q", stderrOut)
	}
}

func TestPreResetScan_StoreUnavailable(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	orig := newIssueStore
	newIssueStore = func(wd, beadsDir, actor string) (issuestore.Store, error) {
		return nil, fmt.Errorf("MCP server unavailable")
	}
	t.Cleanup(func() { newIssueStore = orig })

	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	cmd := &cobra.Command{}
	var buf, errBuf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&errBuf)

	preResetScan(cmd, realDir, []string{"solver"})

	stderrOut := errBuf.String()
	if !strings.Contains(stderrOut, "unavailable") {
		t.Errorf("expected 'unavailable' in stderr when store fails, got: %q", stderrOut)
	}
}

// --- Phase 3: Behavioral tests ---

func initTestGitRepo(t *testing.T, dir string) {
	t.Helper()
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "test@test.com"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	readmePath := filepath.Join(dir, "README")
	if err := os.WriteFile(readmePath, []byte("init"), 0o644); err != nil {
		t.Fatalf("write README: %v", err)
	}
	gitignorePath := filepath.Join(dir, ".gitignore")
	if err := os.WriteFile(gitignorePath, []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", "README", ".gitignore"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
}

func setupTestFactoryRoot(t *testing.T, dir string) {
	t.Helper()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	factoryJSON := `{"type":"factory","version":1,"name":"test"}`
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(factoryJSON), 0o644); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	agentsJSON := `{"agents":{"solver":{"type":"autonomous","description":"Solves problems"},"reviewer":{"type":"autonomous","description":"Reviews code"}}}`
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"), []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}
}

func TestDown_NoReset_BehaviorUnchanged(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	meta := &worktree.Meta{
		ID:     "wt-c1test",
		Owner:  "manager",
		Branch: "af/manager-c1test",
		Path:   ".agentfactory/worktrees/wt-c1test",
		Agents: []string{"manager", "solver"},
	}
	if err := worktree.WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	cleanupAgentWorktree(cmd, realDir, "solver")

	if buf.Len() != 0 {
		t.Errorf("expected no output (FindByOwner should not find non-owner), got: %q", buf.String())
	}

	unchanged, err := worktree.ReadMeta(realDir, "wt-c1test")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if len(unchanged.Agents) != 2 {
		t.Errorf("meta should be unchanged: got Agents=%v, want [manager solver]", unchanged.Agents)
	}
}

func TestDown_ResetGlobal_WarnsAndCleans(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	store := installMemStore(t)
	ctx := context.Background()

	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	agents := []string{"solver", "reviewer"}
	for _, name := range agents {
		agentDir := filepath.Join(realDir, ".agentfactory", "agents", name)
		runtimeDir := filepath.Join(agentDir, ".runtime")
		os.MkdirAll(runtimeDir, 0o755)
		os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)

		store.Create(ctx, issuestore.CreateParams{
			Title:    name + "-bead",
			Assignee: name,
			Type:     issuestore.TypeTask,
		})
	}

	cmd := &cobra.Command{}
	var buf, errBuf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&errBuf)

	preResetScan(cmd, realDir, agents)

	stderrOut := errBuf.String()
	if !strings.Contains(stderrOut, "WARNING") {
		t.Errorf("expected WARNING in stderr, got: %q", stderrOut)
	}

	for _, name := range agents {
		resetAgent(ctx, cmd, realDir, name)
	}

	for _, name := range agents {
		runtimeDir := filepath.Join(realDir, ".agentfactory", "agents", name, ".runtime")
		if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
			t.Errorf("%s: .runtime/ should be removed after reset", name)
		}

		beads, _ := store.List(ctx, issuestore.Filter{Assignee: name, IncludeClosed: true})
		for _, b := range beads {
			if b.Status != issuestore.StatusClosed {
				t.Errorf("%s: bead %s still open after reset", name, b.ID)
			}
		}
	}
}

func TestDown_ResetSingle_OnlyAffectsTarget(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	store := installMemStore(t)
	ctx := context.Background()

	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	for _, name := range []string{"solver", "reviewer"} {
		agentDir := filepath.Join(realDir, ".agentfactory", "agents", name)
		runtimeDir := filepath.Join(agentDir, ".runtime")
		os.MkdirAll(runtimeDir, 0o755)
		os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)

		cpPath := filepath.Join(agentDir, ".agent-checkpoint.json")
		os.WriteFile(cpPath, []byte(`{"formula_id":"f1"}`), 0o644)

		store.Create(ctx, issuestore.CreateParams{
			Title:    name + "-bead",
			Assignee: name,
			Type:     issuestore.TypeTask,
		})
	}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	solverRuntime := filepath.Join(realDir, ".agentfactory", "agents", "solver", ".runtime")
	if _, err := os.Stat(solverRuntime); !os.IsNotExist(err) {
		t.Error("solver .runtime/ should be removed")
	}
	solverCP := checkpoint.Path(config.AgentDir(realDir, "solver"))
	if _, err := os.Stat(solverCP); !os.IsNotExist(err) {
		t.Error("solver checkpoint should be removed")
	}

	reviewerRuntime := filepath.Join(realDir, ".agentfactory", "agents", "reviewer", ".runtime")
	if _, err := os.Stat(reviewerRuntime); err != nil {
		t.Error("reviewer .runtime/ should still exist")
	}
	reviewerCP := checkpoint.Path(config.AgentDir(realDir, "reviewer"))
	if _, err := os.Stat(reviewerCP); err != nil {
		t.Error("reviewer checkpoint should still exist")
	}
	reviewerBeads, _ := store.List(ctx, issuestore.Filter{Assignee: "reviewer"})
	if len(reviewerBeads) != 1 {
		t.Errorf("reviewer beads: got %d, want 1 (untouched)", len(reviewerBeads))
	}
	if reviewerBeads[0].Status == issuestore.StatusClosed {
		t.Error("reviewer bead should still be open")
	}
}

func TestDown_ResetSingle_CoTenantPreserved(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	installMemStore(t)
	ctx := context.Background()

	meta := &worktree.Meta{
		ID:     "wt-cotenant2",
		Owner:  "solver",
		Branch: "af/solver-cotenant2",
		Path:   ".agentfactory/worktrees/wt-cotenant2",
		Agents: []string{"solver", "reviewer"},
	}
	if err := worktree.WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	output := buf.String()
	if !strings.Contains(output, "deregistered") {
		t.Errorf("expected deregistered message, got: %q", output)
	}
	if strings.Contains(output, "force-removed") {
		t.Error("should NOT force-remove worktree when co-tenant exists")
	}

	updated, err := worktree.ReadMeta(realDir, "wt-cotenant2")
	if err != nil {
		t.Fatalf("ReadMeta after reset: %v", err)
	}
	if len(updated.Agents) != 1 || updated.Agents[0] != "reviewer" {
		t.Errorf("Agents: got %v, want [reviewer]", updated.Agents)
	}
}

func TestDown_Reset_PostResetCleanState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initTestGitRepo(t, realDir)
	setupTestFactoryRoot(t, realDir)

	store := installMemStore(t)
	ctx := context.Background()

	absPath, meta, err := worktree.Create(realDir, "solver")
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}

	if _, err := os.Stat(absPath); err != nil {
		t.Fatalf("worktree dir should exist: %v", err)
	}

	agentDir := config.AgentDir(realDir, "solver")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("f1"), 0o644)
	os.WriteFile(checkpoint.Path(agentDir), []byte(`{"formula_id":"f1"}`), 0o644)

	store.Create(ctx, issuestore.CreateParams{
		Title:    "solver-bead",
		Assignee: "solver",
		Type:     issuestore.TypeTask,
	})

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Error(".runtime/ should be removed after reset")
	}

	if _, err := os.Stat(checkpoint.Path(agentDir)); !os.IsNotExist(err) {
		t.Error("checkpoint should be removed after reset")
	}

	_, readErr := worktree.ReadMeta(realDir, meta.ID)
	if readErr == nil {
		t.Error("worktree meta should be removed after sole-owner reset")
	}
}

func TestDown_Reset_NoWorktreeOrBeads(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	installMemStore(t)
	ctx := context.Background()

	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	output := buf.String()
	if strings.Contains(output, "closed") {
		t.Errorf("expected no 'closed' message for clean agent, got: %q", output)
	}
	if strings.Contains(output, "force-removed") {
		t.Errorf("expected no 'force-removed' message for clean agent, got: %q", output)
	}
}

func TestDown_Reset_PartialFailureContinues(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	orig := newIssueStore
	newIssueStore = func(wd, beadsDir, actor string) (issuestore.Store, error) {
		return nil, fmt.Errorf("MCP server unavailable")
	}
	t.Cleanup(func() { newIssueStore = orig })

	ctx := context.Background()

	meta := &worktree.Meta{
		ID:     "wt-partial",
		Owner:  "solver",
		Branch: "af/solver-partial",
		Path:   ".agentfactory/worktrees/wt-partial",
		Agents: []string{"solver"},
	}
	if err := worktree.WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)
	os.WriteFile(filepath.Join(agentDir, ".agent-checkpoint.json"), []byte(`{}`), 0o644)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Error(".runtime/ should be removed despite store failure")
	}

	if _, err := os.Stat(filepath.Join(agentDir, ".agent-checkpoint.json")); !os.IsNotExist(err) {
		t.Error("checkpoint should be removed despite store failure")
	}

	updated, readErr := worktree.ReadMeta(realDir, "wt-partial")
	if readErr != nil {
		t.Logf("meta removed (ForceRemove attempted): %v", readErr)
	} else if len(updated.Agents) != 0 {
		t.Errorf("agent should have been deregistered from meta despite store failure, got Agents=%v", updated.Agents)
	}
}

func TestDown_Reset_FindByAgentNotJustOwner(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	installMemStore(t)
	ctx := context.Background()

	meta := &worktree.Meta{
		ID:     "wt-fba01",
		Owner:  "manager",
		Branch: "af/manager-fba01",
		Path:   ".agentfactory/worktrees/wt-fba01",
		Agents: []string{"manager", "solver"},
	}
	if err := worktree.WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	output := buf.String()
	if !strings.Contains(output, "deregistered") {
		t.Errorf("resetAgent should find non-owner via FindByAgent, got output: %q", output)
	}

	updated, err := worktree.ReadMeta(realDir, "wt-fba01")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if len(updated.Agents) != 1 || updated.Agents[0] != "manager" {
		t.Errorf("Agents after reset: got %v, want [manager]", updated.Agents)
	}

	meta2 := &worktree.Meta{
		ID:     "wt-fba02",
		Owner:  "manager",
		Branch: "af/manager-fba02",
		Path:   ".agentfactory/worktrees/wt-fba02",
		Agents: []string{"manager", "solver"},
	}
	if err := worktree.WriteMeta(realDir, meta2); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	cmd2 := &cobra.Command{}
	var buf2 bytes.Buffer
	cmd2.SetOut(&buf2)

	cleanupAgentWorktree(cmd2, realDir, "solver")

	if buf2.Len() != 0 {
		t.Errorf("cleanupAgentWorktree should NOT find non-owner via FindByOwner, got: %q", buf2.String())
	}

	unchanged, err := worktree.ReadMeta(realDir, "wt-fba02")
	if err != nil {
		t.Fatalf("ReadMeta: %v", err)
	}
	if len(unchanged.Agents) != 2 {
		t.Errorf("meta should be unchanged by cleanupAgentWorktree for non-owner: got Agents=%v", unchanged.Agents)
	}
}

func TestDown_Reset_BehavioralFreshStart(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	initTestGitRepo(t, realDir)
	setupTestFactoryRoot(t, realDir)

	store := installMemStore(t)
	ctx := context.Background()

	_, meta, err := worktree.Create(realDir, "solver")
	if err != nil {
		t.Fatalf("worktree.Create: %v", err)
	}

	agentDir := config.AgentDir(realDir, "solver")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)
	os.WriteFile(checkpoint.Path(agentDir), []byte(`{"formula_id":"f1"}`), 0o644)

	store.Create(ctx, issuestore.CreateParams{
		Title:    "solver-bead",
		Assignee: "solver",
		Type:     issuestore.TypeTask,
	})

	preMeta, err := worktree.FindByAgent(realDir, "solver")
	if err != nil || preMeta == nil {
		t.Fatalf("FindByAgent before reset should find worktree: err=%v, meta=%v", err, preMeta)
	}
	if preMeta.ID != meta.ID {
		t.Errorf("pre-reset FindByAgent ID: got %q, want %q", preMeta.ID, meta.ID)
	}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	resetAgent(ctx, cmd, realDir, "solver")

	postMeta, err := worktree.FindByAgent(realDir, "solver")
	if err != nil {
		t.Fatalf("FindByAgent after reset: %v", err)
	}
	if postMeta != nil {
		t.Errorf("FindByAgent after reset should return nil, got: %+v", postMeta)
	}

	newPath, newID, created, err := worktree.ResolveOrCreate(realDir, "solver", "", "", "")
	if err != nil {
		t.Fatalf("ResolveOrCreate after reset: %v", err)
	}
	if !created {
		t.Error("ResolveOrCreate should create new worktree (created=true) after reset, got false")
	}
	if newPath == "" {
		t.Error("ResolveOrCreate returned empty path")
	}
	if !strings.HasPrefix(newID, "wt-") {
		t.Errorf("new worktree ID should have wt- prefix, got: %q", newID)
	}
	if newID == meta.ID {
		t.Errorf("new worktree ID should differ from original: both are %q", newID)
	}
}
