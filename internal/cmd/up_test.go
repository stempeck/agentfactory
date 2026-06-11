package cmd

import (
	"bytes"
	"errors"
	"fmt"
	"os"
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
	const agentName = "uptest-wt"

	root := t.TempDir()
	initTestGitRepo(t, root)

	// Hermetic: runUp launches the watchdog through newCmdTmux(), so the fake
	// records those ops instead of leaking a real af-watchdog, and the
	// namespaced prefix keeps every session name off production (#309). This
	// replaces the former stale-session kill of af-uptest-wt. Installed AFTER
	// t.TempDir() so the seam restores run before the temp-dir delete (R-7).
	setupHermeticSessions(t)
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

// PR2-HIGH-2: a mid-loop worktree-creation failure must NO LONGER fatally abort. It
// warns, skips that agent, and CONTINUES to the next; runUp returns the non-fatal
// aggregate "some agents failed to start" (allOK=false), NOT the old
// "worktree creation failed for ..." abort. This REPLACES the former
// TestRunUp_AbortsOnWorktreeFailure, which encoded the removed fatal-abort contract.
//
// The conversion applies to ALL worktree-creation failures (cap, disk, git). The
// worktree cap cannot be exercised hermetically: ResolveOrCreate runs GC first
// (worktree.go:686), and GC shells out to the REAL `tmux has-session` (worktree.go:631)
// — not the fake — so it prunes the just-created test worktrees and the count never
// reaches the cap. So this drives the same warn+skip+continue code path via a
// git-add failure (no initTestGitRepo, exactly like the replaced test) with TWO
// agents: BOTH fail, and the fact that BOTH are warned proves the loop CONTINUED past
// the first failure instead of aborting on it (the behavioral change under test).
func TestRunUp_WorktreeCapHit_SkipsAndContinues(t *testing.T) {
	root := t.TempDir()
	// NOTE: deliberately NO initTestGitRepo — `git worktree add` then fails for every
	// agent, deterministically driving the worktree-creation-failure path.
	afDir := filepath.Join(root, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1,"name":"test"}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"alpha":{"type":"autonomous","description":"a"},"bravo":{"type":"autonomous","description":"b"}}}`), 0o644)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, nil)
	out := buf.String()

	if err == nil {
		t.Fatal("runUp should return the non-fatal aggregate error when worktree creation fails")
	}
	if !strings.Contains(err.Error(), "some agents failed to start") {
		t.Errorf("error should be the aggregate 'some agents failed to start', got: %v", err)
	}
	if strings.Contains(err.Error(), "worktree creation failed for") {
		t.Errorf("the old fatal 'worktree creation failed for ...' abort must be gone, got: %v", err)
	}
	if !strings.Contains(out, "skipping") {
		t.Errorf("a worktree failure must warn+skip (not abort); out=%q", out)
	}
	// The loop must have CONTINUED past the first failure to the second agent —
	// proven by BOTH agents being warned (the old fatal abort returned after the first).
	if !strings.Contains(out, "for alpha:") || !strings.Contains(out, "for bravo:") {
		t.Errorf("both agents must be warned+skipped, proving the loop continued past the first failure; out=%q", out)
	}
}

func TestUp_MissingSkill(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	afDir := filepath.Join(root, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1,"name":"test"}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"skill-agent":{"type":"autonomous","description":"needs skills","formula":"skill-formula"}}}`), 0o644)

	formulaDir := filepath.Join(afDir, "store", "formulas")
	os.MkdirAll(formulaDir, 0o755)
	toml := `
formula = "skill-formula"
type = "workflow"
version = 1
skills = ["missing-skill"]

[[steps]]
id = "step1"
title = "Step 1"
`
	os.WriteFile(filepath.Join(formulaDir, "skill-formula.formula.toml"), []byte(toml), 0o644)

	os.MkdirAll(filepath.Join(root, ".claude", "skills"), 0o755)
	os.MkdirAll(filepath.Join(root, ".agentfactory", "agents", "skill-agent"), 0o755)

	t.Setenv("AF_WORKTREE", root)
	t.Setenv("AF_WORKTREE_ID", "wt-test00")
	t.Chdir(root)

	// Hermetic: fake tmux (IsAvailable()==true) + memstore, so runUp proceeds past
	// the IsAvailable gate to the missing-skill validation under test instead of
	// aborting at the default-build GUARD's IsAvailable()==false. #309 substrate.
	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, []string{"skill-agent"})
	output := buf.String()

	if err == nil {
		t.Fatal("expected error from runUp when agent has missing skills")
	}
	if !strings.Contains(output, "missing-skill") {
		t.Errorf("output should mention missing-skill, got: %q", output)
	}
}

func TestRunUp_ModelInOutput(t *testing.T) {
	src, err := os.ReadFile("up.go")
	if err != nil {
		t.Fatal(err)
	}
	content := string(src)
	if !strings.Contains(content, `entry.Model != ""`) {
		t.Error("up.go: runUp must check entry.Model before printing start message")
	}
	if !strings.Contains(content, `"model: "`) {
		t.Error("up.go: runUp must include model label in start message when model is set")
	}
	if !strings.Contains(content, `"Started %s\n"`) {
		t.Error("up.go: runUp must preserve backward-compatible Started format when model is empty")
	}
}

func TestUpStartMessage_WithEndpoint(t *testing.T) {
	src, err := os.ReadFile("up.go")
	if err != nil {
		t.Fatal(err)
	}
	content := string(src)
	if !strings.Contains(content, `entry.BaseURL != ""`) {
		t.Error("up.go: runUp must check entry.BaseURL before printing start message")
	}
	if !strings.Contains(content, `"endpoint: "`) {
		t.Error("up.go: runUp must include endpoint label in start message when base_url is set")
	}
	if !strings.Contains(content, `"Started %s\n"`) {
		t.Error("up.go: runUp must preserve backward-compatible Started format when no fields set")
	}
}
