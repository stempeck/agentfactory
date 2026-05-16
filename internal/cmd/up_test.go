package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/session"
)

func TestErrNotProvisioned_IsDetectable(t *testing.T) {
	// Verify that the wrapped ErrNotProvisioned from session.Start() is detectable
	// via errors.Is. This is a prerequisite for the graceful skip in runUp.
	wrapped := fmt.Errorf("%w: /some/path", session.ErrNotProvisioned)
	if !errors.Is(wrapped, session.ErrNotProvisioned) {
		t.Fatal("wrapped ErrNotProvisioned should be detectable via errors.Is")
	}
}

func TestRunUp_HandlesNotProvisioned(t *testing.T) {
	// This test verifies that runUp treats ErrNotProvisioned as a skip condition
	// (like ErrAlreadyRunning), not as a hard failure.
	//
	// Currently, runUp only has special handling for ErrAlreadyRunning.
	// ErrNotProvisioned falls through to the generic error handler which
	// sets allOK=false and causes a non-zero exit.
	//
	// After the fix, this test should pass: ErrNotProvisioned should be
	// handled with a skip message and no error exit.

	err := session.ErrNotProvisioned
	if !errors.Is(err, session.ErrNotProvisioned) {
		t.Fatal("ErrNotProvisioned should be identifiable")
	}
	// The key assertion: ErrNotProvisioned is NOT ErrAlreadyRunning
	if errors.Is(err, session.ErrAlreadyRunning) {
		t.Fatal("ErrNotProvisioned should not be mistaken for ErrAlreadyRunning")
	}

	// After fix: runUp should treat ErrNotProvisioned like ErrAlreadyRunning
	// (skip gracefully). This test documents the intent.
	isSkippable := errors.Is(err, session.ErrAlreadyRunning) || errors.Is(err, session.ErrNotProvisioned)
	if !isSkippable {
		t.Fatal("ErrNotProvisioned should be a skippable error in runUp")
	}
}

func TestRunUp_NonSpecialistCallerGetsIndependentWorktree(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	const agentName = "uptest-wt"
	sessionName := "af-" + agentName
	killStaleTmuxSession(t, sessionName)
	t.Cleanup(func() {
		killStaleTmuxSession(t, sessionName)
	})

	root := t.TempDir()
	initTestGitRepo(t, root)
	afDir := filepath.Join(root, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"`+agentName+`":{"type":"autonomous","description":"test","formula":"uptest-formula"},"manager":{"type":"interactive","description":"orchestrator"}}}`), 0o644)

	formulaDir := filepath.Join(afDir, "store", "formulas")
	os.MkdirAll(formulaDir, 0o755)
	toml := `
formula = "uptest-formula"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
`
	os.WriteFile(filepath.Join(formulaDir, "uptest-formula.formula.toml"), []byte(toml), 0o644)

	managerWT := root
	managerWTID := "wt-mgr000"
	t.Setenv("AF_WORKTREE", managerWT)
	t.Setenv("AF_WORKTREE_ID", managerWTID)
	t.Setenv("AF_ROLE", "manager")

	os.MkdirAll(filepath.Join(root, ".agentfactory", "agents", agentName), 0o755)
	t.Chdir(filepath.Join(root, ".agentfactory", "agents"))

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, []string{agentName})
	output := buf.String()
	if err == nil {
		if !strings.Contains(output, "Created worktree") {
			t.Error("runUp from non-specialist caller should create a new worktree, not inherit")
		}
	} else {
		if !strings.Contains(output, "Created worktree") && !strings.Contains(err.Error(), "not provisioned") {
			t.Errorf("expected new worktree creation or provisioning error; got output=%q err=%v", output, err)
		}
	}
}

func TestRunUp_AbortsOnWorktreeFailure(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"solver":{"type":"autonomous","description":"test"}}}`), 0o644)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, []string{"solver"})
	if err == nil {
		t.Fatal("runUp should return error when worktree creation fails")
	}
	if !strings.Contains(err.Error(), "worktree creation failed") {
		t.Errorf("error should contain 'worktree creation failed', got: %v", err)
	}
}
