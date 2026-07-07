package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func TestHandoff_NotInsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	dir := setupTestFactoryForDone(t, "manager")
	err := runHandoffCore(t.Context(), filepath.Join(dir, ".agentfactory", "agents", "manager"), "test", "msg", false, false, false)
	if err == nil {
		t.Fatal("expected error when not inside tmux")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error should mention tmux, got: %v", err)
	}
}

func TestHandoff_NoTmuxPane(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "")

	dir := setupTestFactoryForDone(t, "manager")
	err := runHandoffCore(t.Context(), filepath.Join(dir, ".agentfactory", "agents", "manager"), "test", "msg", false, false, false)
	if err == nil {
		t.Fatal("expected error when TMUX_PANE not set")
	}
	if !strings.Contains(err.Error(), "TMUX_PANE") {
		t.Errorf("error should mention TMUX_PANE, got: %v", err)
	}
}

func TestHandoff_DryRun(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	err := runHandoffCore(t.Context(), workDir, "HANDOFF: test", "Test message", false, false, true)
	if err != nil {
		t.Fatalf("dry-run should not error, got: %v", err)
	}

	// Verify no checkpoint was written
	cpPath := filepath.Join(workDir, ".agent-checkpoint.json")
	if _, err := os.Stat(cpPath); !os.IsNotExist(err) {
		t.Error("dry-run should not write checkpoint")
	}
}

func TestHandoff_WritesCheckpoint(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	// runHandoffCore will write checkpoint then fail at tmux/mail commands — that's expected.
	// We just verify the checkpoint was written.
	_ = runHandoffCore(t.Context(), workDir, "Test handoff subject", "Test message", false, false, false)

	cpPath := filepath.Join(workDir, ".agent-checkpoint.json")
	if _, err := os.Stat(cpPath); err != nil {
		t.Fatalf("checkpoint should exist after handoff: %v", err)
	}

	data, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Test handoff subject") {
		t.Error("checkpoint should contain the handoff subject in Notes")
	}
}

func TestHandoff_PreservesRuntimeFiles(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	writeRuntimeFile(t, workDir, "hooked_formula", "bd-instance-1")
	writeRuntimeFile(t, workDir, "formula_caller", "supervisor")

	// Run handoff — will fail at tmux/mail, but runtime files must survive
	_ = runHandoffCore(t.Context(), workDir, "test", "msg", false, false, false)

	if _, err := os.Stat(filepath.Join(workDir, ".runtime", "hooked_formula")); err != nil {
		t.Error("hooked_formula should be preserved after handoff")
	}
	if _, err := os.Stat(filepath.Join(workDir, ".runtime", "formula_caller")); err != nil {
		t.Error("formula_caller should be preserved after handoff")
	}
}

func TestHandoff_MailSkipUnderTest(t *testing.T) {
	err := sendHandoffMail("manager", "HANDOFF: test", "body")
	if err != nil {
		t.Errorf("sendHandoffMail should return nil under go test, got: %v", err)
	}
}

func TestHandoff_FlagRegistration(t *testing.T) {
	flags := []string{"subject", "message", "collect", "dry-run", "idle"}
	for _, name := range flags {
		if handoffCmd.Flags().Lookup(name) == nil {
			t.Errorf("handoff command should have --%s flag", name)
		}
	}
}

func TestHandoff_IdleIncrementsCounter(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	writeRuntimeFile(t, workDir, "idle_cycles", "2")

	// Run handoff with idle=true — will fail at tmux/mail, but .runtime/idle_cycles should be incremented
	_ = runHandoffCore(t.Context(), workDir, "test", "msg", false, true, false)

	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "idle_cycles"))
	if err != nil {
		t.Fatalf("idle_cycles should exist after idle handoff: %v", err)
	}
	if strings.TrimSpace(string(data)) != "3" {
		t.Errorf("idle_cycles should be incremented to 3, got: %q", strings.TrimSpace(string(data)))
	}
}

func TestHandoff_IdleFirstCycleCreatesCounter(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	// No idle_cycles file exists initially
	_ = runHandoffCore(t.Context(), workDir, "test", "msg", false, true, false)

	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "idle_cycles"))
	if err != nil {
		t.Fatalf("idle_cycles should be created on first idle handoff: %v", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Errorf("idle_cycles should be 1 on first idle cycle, got: %q", strings.TrimSpace(string(data)))
	}
}

func TestHandoff_NonIdleResetsCounter(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	writeRuntimeFile(t, workDir, "idle_cycles", "5")

	// Run handoff without idle flag — counter should be reset
	_ = runHandoffCore(t.Context(), workDir, "test", "msg", false, false, false)

	_, err := os.Stat(filepath.Join(workDir, ".runtime", "idle_cycles"))
	if err == nil {
		t.Error("idle_cycles should be removed after productive (non-idle) handoff")
	}
}

func TestHandoff_TransitiveEndpointDependency(t *testing.T) {
	src, err := os.ReadFile("handoff.go")
	if err != nil {
		t.Fatalf("reading handoff.go: %v", err)
	}
	code := string(src)

	if !strings.Contains(code, "respawnSession(") {
		t.Error("handoff.go must call respawnSession() which transitively inherits endpoint exports via session.NewManager + BuildStartupCommand")
	}
	if !strings.Contains(code, "*agentEntry") {
		t.Error("handoff.go must dereference agentEntry to pass full struct (including BaseURL/AuthToken)")
	}
	if strings.Contains(code, "ANTHROPIC_BASE_URL") || strings.Contains(code, "ANTHROPIC_AUTH_TOKEN") {
		t.Error("handoff.go must NOT contain hardcoded endpoint env var names — it should use BuildStartupCommand transitively")
	}

	helperSrc, err := os.ReadFile("helpers.go")
	if err != nil {
		t.Fatalf("reading helpers.go: %v", err)
	}
	helperCode := string(helperSrc)
	if !strings.Contains(helperCode, "session.NewManager(") {
		t.Error("helpers.go respawnSession must use session.NewManager to pass full AgentEntry")
	}
	if !strings.Contains(helperCode, "BuildStartupCommand()") {
		t.Error("helpers.go respawnSession must call BuildStartupCommand() for endpoint transitivity")
	}
}

// TestHandoff_OverrideSurvives — a --model override persisted to
// .runtime/model_override is re-resolved on respawn and the rebuilt startup
// command carries the override's model. models.json has NO agents-map default,
// so the ONLY source of the model is the marker.
func TestHandoff_OverrideSurvives(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{"sonnet": {"ANTHROPIC_MODEL": "claude-sonnet-4-6"}},
	})

	t.Run("non_worktree", func(t *testing.T) {
		agentDir := config.AgentDir(dir, "manager")
		writeRuntimeFile(t, agentDir, "model_override", "sonnet")

		mock := &mockTmux{}
		err := respawnSession(RespawnOptions{
			FactoryRoot:  dir,
			AgentName:    "manager",
			AgentEntry:   config.AgentEntry{Type: "interactive"}, // Model empty → no legacy mask
			AgentWorkDir: agentDir,
			PaneID:       "%0",
			Tx:           mock,
		})
		if err != nil {
			t.Fatalf("respawnSession: %v", err)
		}
		if _, statErr := os.Stat(filepath.Join(agentDir, ".runtime", "model_override")); statErr != nil {
			t.Error("model_override marker must persist across respawn")
		}
		if len(mock.respawnPaneCalls) != 1 {
			t.Fatalf("RespawnPane should be called once, got %d", len(mock.respawnPaneCalls))
		}
		if cmd := mock.respawnPaneCalls[0].cmd; !strings.Contains(cmd, "claude-sonnet-4-6") {
			t.Errorf("respawn command must carry the override model claude-sonnet-4-6, got: %s", cmd)
		}
	})

	// Worktree case: handoff/compact pass NO WorktreePath but the agent
	// dir lives under a worktree. The marker is written there; the respawn marker-read
	// must match the write via AgentWorkDir. A read/write path mismatch fails here.
	t.Run("worktree_via_agentworkdir", func(t *testing.T) {
		wtAgentDir := filepath.Join(dir, ".agentfactory", "worktrees", "wt-xyz", ".agentfactory", "agents", "manager")
		if err := os.MkdirAll(wtAgentDir, 0o755); err != nil {
			t.Fatal(err)
		}
		writeRuntimeFile(t, wtAgentDir, "model_override", "sonnet")

		mock := &mockTmux{}
		err := respawnSession(RespawnOptions{
			FactoryRoot:  dir,
			AgentName:    "manager",
			AgentEntry:   config.AgentEntry{Type: "interactive"},
			AgentWorkDir: wtAgentDir, // handoff/compact pass cwd (the worktree agent dir)
			PaneID:       "%0",
			Tx:           mock,
		})
		if err != nil {
			t.Fatalf("respawnSession: %v", err)
		}
		if cmd := mock.respawnPaneCalls[0].cmd; !strings.Contains(cmd, "claude-sonnet-4-6") {
			t.Errorf("respawn must read the worktree marker via AgentWorkDir and carry claude-sonnet-4-6, got: %s", cmd)
		}
	})
}

// TestHandoff_AgentsMapDefaultSurvives — a durable
// models.json.agents default with NO --model flag and NO marker must re-apply on
// every respawn. AgentEntry.Model is kept EMPTY so the legacy emit cannot mask the
// bug. This FAILS if respawn reads only the marker (the marker path test would
// still pass), proving respawn re-resolves the FULL precedence chain.
func TestHandoff_AgentsMapDefaultSurvives(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{"sonnet": {"ANTHROPIC_MODEL": "claude-sonnet-4-6"}},
		Agents: map[string]string{"manager": "sonnet"},
	})
	// Deliberately: NO marker written, NO --model, AgentEntry.Model empty.

	mock := &mockTmux{}
	err := respawnSession(RespawnOptions{
		FactoryRoot:  dir,
		AgentName:    "manager",
		AgentEntry:   config.AgentEntry{Type: "interactive"},
		AgentWorkDir: config.AgentDir(dir, "manager"),
		PaneID:       "%0",
		Tx:           mock,
	})
	if err != nil {
		t.Fatalf("respawnSession: %v", err)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Fatalf("RespawnPane should be called once, got %d", len(mock.respawnPaneCalls))
	}
	if cmd := mock.respawnPaneCalls[0].cmd; !strings.Contains(cmd, "claude-sonnet-4-6") {
		t.Errorf("a durable agents-map default must survive respawn; respawn must re-resolve "+
			"the full chain, not just the marker. got: %s", cmd)
	}
}

func TestHandoff_IdleDelayComputation(t *testing.T) {
	tests := []struct {
		cycles   int
		expected int // seconds
	}{
		{1, 60},
		{2, 120},
		{5, 300},
		{10, 600},
		{30, 1800},
		{31, 1800}, // capped
		{100, 1800},
	}

	for _, tt := range tests {
		delay := idleBackoffSeconds(tt.cycles)
		if delay != tt.expected {
			t.Errorf("idleBackoffSeconds(%d) = %d, want %d", tt.cycles, delay, tt.expected)
		}
	}
}
