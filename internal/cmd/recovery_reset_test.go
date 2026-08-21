package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// K13 + L-1 (#596 Phase 4A). haltRecovery has been telling operators to run
// `af recovery reset <agent>` since Phase 2 — a verb that did not exist. These tests
// pin that it exists, that it is operator-only, that it clears the ONE state it is
// allowed to touch, and that it can clear the state most in need of clearing: the
// corrupt latch a load→clear→save implementation provably cannot.
//
// None of these may call t.Parallel: they reassign newCmdTmux and mutate the env.

// setupRecoveryResetFactory returns a hermetic, symlink-resolved factory root with
// the caller already chdir'd into it, so the verb resolves the same root the test
// plants into.
func setupRecoveryResetFactory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	writeAgentsJSON(t, realDir, `{"agents":{"worker":{"type":"autonomous","description":"d","formula":"minimalworker"}}}`)
	if err := os.WriteFile(config.FactoryConfigPath(realDir), []byte(`{"type":"factory","version":1}`+"\n"), 0o644); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	t.Chdir(realDir)
	return realDir
}

func invokeRecoveryReset(t *testing.T, agent string) (string, string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	err := runRecoveryReset(cmd, []string{agent})
	return out.String(), errBuf.String(), err
}

func breakerPath(root, agent string) string {
	return filepath.Join(root, ".runtime", "recovery", agent+".json")
}

func TestRecoveryReset(t *testing.T) {
	t.Run("clears a halted breaker and prints the action", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)
		if err := saveRecoveryState(root, "worker",
			recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3}); err != nil {
			t.Fatalf("plant breaker: %v", err)
		}

		out, _, err := invokeRecoveryReset(t, "worker")
		if err != nil {
			t.Fatalf("runRecoveryReset: %v", err)
		}
		if _, statErr := os.Stat(breakerPath(root, "worker")); !os.IsNotExist(statErr) {
			t.Errorf("breaker file must be removed (stat err = %v)", statErr)
		}
		if st := loadRecoveryState(root, "worker"); st.Halted {
			t.Errorf("breaker still reads halted after reset: %+v", st)
		}
		if !strings.Contains(out, "worker") || strings.TrimSpace(out) == "" {
			t.Errorf("the action taken must be printed, got %q", out)
		}
	})

	t.Run("clears a CORRUPT breaker", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)
		dir := filepath.Join(root, ".runtime", "recovery")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "worker.json"), []byte("{not json at all"), 0o644); err != nil {
			t.Fatal(err)
		}
		// Precondition: this is exactly the state a load→clear→save reset cannot clear.
		st := loadRecoveryState(root, "worker")
		if !st.Halted || st.HaltReason != haltReasonCorrupt {
			t.Fatalf("fixture is wrong: %+v", st)
		}
		if err := saveRecoveryState(root, "worker", st); err == nil {
			t.Fatal("fixture is wrong: saveRecoveryState must refuse a corrupt state")
		}

		if _, _, err := invokeRecoveryReset(t, "worker"); err != nil {
			t.Fatalf("runRecoveryReset: %v", err)
		}
		if _, statErr := os.Stat(breakerPath(root, "worker")); !os.IsNotExist(statErr) {
			t.Errorf("a corrupt latch must be removable (stat err = %v)", statErr)
		}
		if st := loadRecoveryState(root, "worker"); st.Halted {
			t.Errorf("breaker still reads halted after reset: %+v", st)
		}
	})

	t.Run("absent breaker is a benign no-op", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)

		out, _, err := invokeRecoveryReset(t, "worker")
		if err != nil {
			t.Fatalf("resetting an un-latched agent must not be an error: %v", err)
		}
		if strings.TrimSpace(out) == "" {
			t.Error("the no-op must still say what happened")
		}
		_ = root
	})

	t.Run("refuses agent authority and leaves the latch byte-unchanged", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)
		if err := saveRecoveryState(root, "worker",
			recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3}); err != nil {
			t.Fatalf("plant breaker: %v", err)
		}
		before, err := os.ReadFile(breakerPath(root, "worker"))
		if err != nil {
			t.Fatal(err)
		}

		t.Setenv("AF_ROLE", "manager") // signal 1 ⇒ AuthorityAgent
		t.Setenv("TMUX", "")

		_, _, runErr := invokeRecoveryReset(t, "worker")
		if runErr == nil {
			t.Fatal("agent authority must be refused with a non-nil error so cobra exits 1")
		}
		after, err := os.ReadFile(breakerPath(root, "worker"))
		if err != nil {
			t.Fatalf("the latch must survive a refusal: %v", err)
		}
		if !bytes.Equal(before, after) {
			t.Error("a refused reset must not touch the breaker file")
		}
		// ux.md L36-39: never hand the agent a bypass recipe.
		for _, leak := range []string{"AF_ROLE", "TMUX"} {
			if strings.Contains(runErr.Error(), leak) {
				t.Errorf("the refusal must not name the detection mechanism (%q): %q", leak, runErr.Error())
			}
		}
	})

	t.Run("mutates breaker state only", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		fake := installFakeTmuxPresent(t, session.SessionName("worker"))
		installMemStore(t)
		if err := saveRecoveryState(root, "worker",
			recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts}); err != nil {
			t.Fatalf("plant breaker: %v", err)
		}
		agentDir := config.AgentDir(root, "worker")
		writeRuntimeFile(t, agentDir, "dispatch_owner", "manager")
		logPath := filepath.Join(root, ".runtime", "recovery_log.jsonl")
		if err := os.WriteFile(logPath, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, _, err := invokeRecoveryReset(t, "worker"); err != nil {
			t.Fatalf("runRecoveryReset: %v", err)
		}

		for _, op := range fake.ops {
			if strings.HasPrefix(op, "KillSession") {
				t.Errorf("the reset must never touch sessions, saw %q (respawn power stays with the watchdog)", op)
			}
		}
		if _, err := os.Stat(filepath.Join(agentDir, ".runtime", "dispatch_owner")); err != nil {
			t.Errorf("the agent's own runtime state must survive: %v", err)
		}
		if _, err := os.Stat(logPath); err != nil {
			t.Errorf("the K6 recovery log is evidence and must survive: %v", err)
		}
	})

	t.Run("rejects an invalid agent name", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)
		sentinel := filepath.Join(root, "sentinel.json")
		if err := os.WriteFile(sentinel, []byte("keep me"), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, _, err := invokeRecoveryReset(t, "../../sentinel"); err == nil {
			t.Error("a path-shaped agent name must be refused before any path is composed")
		}
		if _, err := os.Stat(sentinel); err != nil {
			t.Errorf("nothing outside the recovery state dir may be removed: %v", err)
		}
	})
}

// TestRecoveryResetCmd_RegisteredWithAdvertisedSpelling turns Gotcha 7's dangling
// reference into a pinned one: the halt escalation names a verb, and this asserts
// the registered command spells it the same way.
func TestRecoveryResetCmd_RegisteredWithAdvertisedSpelling(t *testing.T) {
	if recoveryCmd.Use != "recovery" {
		t.Errorf("recoveryCmd.Use = %q, want %q", recoveryCmd.Use, "recovery")
	}
	var found *cobra.Command
	for _, c := range recoveryCmd.Commands() {
		if c.Name() == "reset" {
			found = c
		}
	}
	if found == nil {
		t.Fatalf("no 'reset' subcommand under recoveryCmd (have %v)", recoveryCmd.Commands())
	}
	if found.Use != "reset <agent>" {
		t.Errorf("reset Use = %q, want %q (matches the `attach <agent>` spelling)", found.Use, "reset <agent>")
	}

	var registered bool
	for _, c := range rootCmd.Commands() {
		if c.Name() == "recovery" {
			registered = true
		}
	}
	if !registered {
		t.Error("recoveryCmd must self-register on rootCmd in recovery.go's own init()")
	}

	data, err := os.ReadFile("recovery.go")
	if err != nil {
		t.Fatalf("read recovery.go: %v", err)
	}
	if !strings.Contains(string(data), "af recovery reset %s") {
		t.Error("the halt escalation's advertised spelling changed — keep it identical to the registered verb")
	}
}

// TestResetAgentState_ClearsFactoryRootBreaker pins review L-1: the breaker lives at
// the FACTORY root, which resetAgentState's `<agentDir>/.runtime` wipe never reached,
// so `af sling --reset` (and `af down --reset`) left recovery latched.
func TestResetAgentState_ClearsFactoryRootBreaker(t *testing.T) {
	t.Run("clears the halted latch and spares siblings", func(t *testing.T) {
		dir := t.TempDir()
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("eval symlinks: %v", err)
		}
		installMemStore(t)
		if err := os.MkdirAll(worktree.WorktreesDir(realDir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := saveRecoveryState(realDir, "solver",
			recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts}); err != nil {
			t.Fatal(err)
		}
		if err := saveRecoveryState(realDir, "manager",
			recoveryState{Halted: true, HaltReason: haltReasonRateCap}); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := resetAgentState(context.Background(), &buf, realDir, "solver", "test-reason"); err != nil {
			t.Fatalf("resetAgentState: %v", err)
		}
		if _, err := os.Stat(breakerPath(realDir, "solver")); !os.IsNotExist(err) {
			t.Errorf("the factory-root breaker must be cleared so `af sling --reset` re-arms recovery (L-1); stat err = %v", err)
		}
		if _, err := os.Stat(breakerPath(realDir, "manager")); err != nil {
			t.Errorf("a sibling's latch must survive: %v", err)
		}
	})

	t.Run("clears a corrupt latch", func(t *testing.T) {
		dir := t.TempDir()
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("eval symlinks: %v", err)
		}
		installMemStore(t)
		if err := os.MkdirAll(worktree.WorktreesDir(realDir), 0o755); err != nil {
			t.Fatal(err)
		}
		bdir := filepath.Join(realDir, ".runtime", "recovery")
		if err := os.MkdirAll(bdir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(bdir, "solver.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		if err := resetAgentState(context.Background(), &buf, realDir, "solver", "test-reason"); err != nil {
			t.Fatalf("resetAgentState: %v", err)
		}
		if _, err := os.Stat(breakerPath(realDir, "solver")); !os.IsNotExist(err) {
			t.Errorf("a corrupt latch must be removable by the reset path too; stat err = %v", err)
		}
	})

	t.Run("an absent breaker does not fail the reset", func(t *testing.T) {
		dir := t.TempDir()
		realDir, err := filepath.EvalSymlinks(dir)
		if err != nil {
			t.Fatalf("eval symlinks: %v", err)
		}
		installMemStore(t)
		if err := os.MkdirAll(worktree.WorktreesDir(realDir), 0o755); err != nil {
			t.Fatal(err)
		}
		ownerPath := filepath.Join(config.AgentDir(realDir, "solver"), ".runtime", "dispatch_owner")
		writeRuntimeFile(t, config.AgentDir(realDir, "solver"), "dispatch_owner", "manager")

		var buf bytes.Buffer
		if err := resetAgentState(context.Background(), &buf, realDir, "solver", "test-reason"); err != nil {
			t.Fatalf("resetAgentState with no breaker: %v", err)
		}
		if _, err := os.Stat(ownerPath); !os.IsNotExist(err) {
			t.Error("the rest of the reset must still complete")
		}
	})
}
