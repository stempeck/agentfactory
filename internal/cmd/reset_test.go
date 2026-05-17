package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/worktree"
)

func TestResetAgentState_CustomReason(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	store := installMemStore(t)
	ctx := context.Background()

	store.Create(ctx, issuestore.CreateParams{
		Title:    "solver-bead",
		Assignee: "solver",
		Type:     issuestore.TypeTask,
	})

	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	var buf bytes.Buffer
	resetAgentState(ctx, &buf, realDir, "solver", "reset by af sling --reset")

	beads, _ := store.List(ctx, issuestore.Filter{Assignee: "solver", IncludeClosed: true})
	for _, b := range beads {
		if b.Status != issuestore.StatusClosed {
			t.Errorf("bead %s: status=%s, want closed", b.ID, b.Status)
		}
		if b.CloseReason != "reset by af sling --reset" {
			t.Errorf("bead %s: reason=%q, want %q", b.ID, b.CloseReason, "reset by af sling --reset")
		}
	}
}

func TestResetAgentState_WritesToWriter(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	store := installMemStore(t)
	ctx := context.Background()

	for i := 0; i < 2; i++ {
		store.Create(ctx, issuestore.CreateParams{
			Title:    "solver-bead",
			Assignee: "solver",
			Type:     issuestore.TypeTask,
		})
	}

	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	var buf bytes.Buffer
	resetAgentState(ctx, &buf, realDir, "solver", "test-reason")

	output := buf.String()
	if !bytes.Contains([]byte(output), []byte("closed 2 formula beads")) {
		t.Errorf("expected output to contain 'closed 2 formula beads', got: %q", output)
	}
}

func TestResetAgentState_Idempotent_DoubleCall(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	store := installMemStore(t)
	ctx := context.Background()

	store.Create(ctx, issuestore.CreateParams{
		Title:    "solver-bead",
		Assignee: "solver",
		Type:     issuestore.TypeTask,
	})

	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)
	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	var buf1 bytes.Buffer
	if err := resetAgentState(ctx, &buf1, realDir, "solver", "test-reason"); err != nil {
		t.Fatalf("first call failed: %v", err)
	}

	var buf2 bytes.Buffer
	if err := resetAgentState(ctx, &buf2, realDir, "solver", "test-reason"); err != nil {
		t.Fatalf("second call failed: %v", err)
	}

	output2 := buf2.String()
	if bytes.Contains([]byte(output2), []byte("closed")) {
		t.Errorf("second call should produce no 'closed' message, got: %q", output2)
	}
	if bytes.Contains([]byte(output2), []byte("force-removed")) {
		t.Errorf("second call should produce no 'force-removed' message, got: %q", output2)
	}
}

func TestResetAgentState_Idempotent_MissingRuntime(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	installMemStore(t)
	ctx := context.Background()

	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	var buf bytes.Buffer
	if err := resetAgentState(ctx, &buf, realDir, "solver", "test-reason"); err != nil {
		t.Fatalf("resetAgentState with missing runtime should not fail: %v", err)
	}
}

func TestResetAgentState_ForceRemoveFailure_CleansMetaFile(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	installMemStore(t)
	ctx := context.Background()

	meta := &worktree.Meta{
		ID:     "wt-gap1test",
		Owner:  "solver",
		Branch: "af/solver-gap1test",
		Path:   ".agentfactory/worktrees/wt-gap1test",
		Agents: []string{"solver"},
	}
	if err := worktree.WriteMeta(realDir, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	metaFile := filepath.Join(worktree.WorktreesDir(realDir), "wt-gap1test.meta.json")
	if _, err := os.Stat(metaFile); err != nil {
		t.Fatalf("meta file should exist before reset: %v", err)
	}

	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	var buf bytes.Buffer
	resetAgentState(ctx, &buf, realDir, "solver", "test-reason")

	if _, err := os.Stat(metaFile); !os.IsNotExist(err) {
		t.Error("meta file should be removed after resetAgentState even when ForceRemove fails (Gap 1 fix)")
	}

	found, _ := worktree.FindByAgent(realDir, "solver")
	if found != nil {
		t.Errorf("FindByAgent should return nil after reset, got: %+v", found)
	}
}

func TestResetAgentState_StoreInitFailure(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	orig := newIssueStore
	newIssueStore = func(wd, actor string) (issuestore.Store, error) {
		return nil, fmt.Errorf("MCP server unavailable")
	}
	t.Cleanup(func() { newIssueStore = orig })

	ctx := context.Background()

	agentDir := filepath.Join(realDir, ".agentfactory", "agents", "solver")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("1"), 0o644)
	os.WriteFile(checkpoint.Path(agentDir), []byte(`{"formula_id":"f1"}`), 0o644)
	os.MkdirAll(worktree.WorktreesDir(realDir), 0o755)

	var buf bytes.Buffer
	if err := resetAgentState(ctx, &buf, realDir, "solver", "test-reason"); err != nil {
		t.Fatalf("resetAgentState should not return error on store init failure: %v", err)
	}

	if _, err := os.Stat(runtimeDir); !os.IsNotExist(err) {
		t.Error(".runtime/ should be removed despite store failure")
	}

	if _, err := os.Stat(checkpoint.Path(agentDir)); !os.IsNotExist(err) {
		t.Error("checkpoint should be removed despite store failure")
	}
}
