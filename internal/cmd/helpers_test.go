package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
)

// TestResolveAgentName_WrongButNoError_HonorsAF_ROLE pins GitHub issue #88.
//
// DetectAgentFromCwd returns parts[2] with no agents.json validation, so a cwd
// at .agentfactory/agents/<typo>/ returns ("typo", nil) — no error. The old
// resolveAgentName AND-gated AF_ROLE behind err != nil, silently ignoring
// AF_ROLE even when set correctly by session.Manager. The fix validates the
// path-derived name against agents.json and consults AF_ROLE on membership
// failure.
func TestResolveAgentName_WrongButNoError_HonorsAF_ROLE(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	// Create a typo directory on disk. "typo" is NOT in agents.json.
	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "solver")

	got, err := resolveAgentName(typoDir, factoryRoot)
	if err != nil {
		t.Fatalf("resolveAgentName: %v", err)
	}
	if got != "solver" {
		t.Errorf("resolveAgentName = %q, want %q (AF_ROLE must override wrong path-derived name)", got, "solver")
	}
}

// TestResolveAgentName_WrongButNoError_NoAF_ROLE_Errors is the negative
// companion. With AF_ROLE empty and the path-derived name failing membership,
// resolveAgentName must return an error rather than silently returning the
// wrong name. This protects detectCreatingAgent and detectAgentName — the two
// callers that currently accept whatever resolveAgentName returns.
func TestResolveAgentName_WrongButNoError_NoAF_ROLE_Errors(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "")

	got, err := resolveAgentName(typoDir, factoryRoot)
	if err == nil {
		t.Fatalf("resolveAgentName should error for unknown agent, got %q", got)
	}
	if got == "typo" {
		t.Errorf("resolveAgentName must not return wrong path-derived name %q silently", got)
	}
}

// TestResolveAgentName_HappyPath_NoAF_ROLE verifies the fix doesn't regress
// legitimate path resolution: a cwd under a real agent directory returns the
// agent name without consulting AF_ROLE.
func TestResolveAgentName_HappyPath_NoAF_ROLE(t *testing.T) {
	factoryRoot, agentDir := setupFactoryFixture(t, "solver")

	t.Setenv("AF_ROLE", "")

	got, err := resolveAgentName(agentDir, factoryRoot)
	if err != nil {
		t.Fatalf("resolveAgentName: %v", err)
	}
	if got != "solver" {
		t.Errorf("resolveAgentName = %q, want %q", got, "solver")
	}
}

// TestResolveAgentName_CorruptAgentsJSON_NoAF_ROLE_Errors pins a silent-skip
// bug in the membership gate at helpers.go:60-66. When LoadAgentConfig fails
// (missing, unreadable, or malformed agents.json), the `if cfgErr == nil`
// branch is skipped, err stays nil, and the wrong-but-no-error path-derived
// name is returned without error.
//
// This is the same silent-fallback-to-buggy-behavior pattern issue #88 is
// meant to eliminate — just at a different layer. If the function cannot
// validate the path-derived name, it must not trust it.
func TestResolveAgentName_CorruptAgentsJSON_NoAF_ROLE_Errors(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	if err := os.WriteFile(
		filepath.Join(factoryRoot, ".agentfactory", "agents.json"),
		[]byte("this is not json{{{"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "")

	got, err := resolveAgentName(typoDir, factoryRoot)
	if err == nil {
		t.Fatalf("resolveAgentName silently returned %q — membership gate was skipped because agents.json failed to load", got)
	}
	if got == "typo" {
		t.Errorf("resolveAgentName must not return wrong path-derived name %q when membership cannot be validated", got)
	}
}

// TestResolveAgentName_CorruptAgentsJSON_WithAF_ROLE_HonorsEnv is the companion
// case. With a corrupt agents.json AND AF_ROLE set to a legitimate name by
// session.Manager, the function should honor AF_ROLE — the whole point of
// AF_ROLE is to be the trusted fallback when path-derived identity cannot be
// validated. The silent-skip bug currently swallows AF_ROLE entirely by
// leaving err==nil and returning the (wrong) path-derived name.
func TestResolveAgentName_CorruptAgentsJSON_WithAF_ROLE_HonorsEnv(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	if err := os.WriteFile(
		filepath.Join(factoryRoot, ".agentfactory", "agents.json"),
		[]byte("this is not json{{{"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "solver")

	got, err := resolveAgentName(typoDir, factoryRoot)
	if err != nil {
		t.Fatalf("resolveAgentName: %v", err)
	}
	if got != "solver" {
		t.Errorf("resolveAgentName = %q, want %q — AF_ROLE must be honored when agents.json cannot be loaded (session.Manager's trusted value is the whole point of AF_ROLE)", got, "solver")
	}
}

// --- respawnSession tests ---

type mockTmux struct {
	clearHistoryCalls []string
	respawnPaneCalls  []struct{ pane, cmd string }
	respawnErr        error
}

func (m *mockTmux) ClearHistory(pane string) error {
	m.clearHistoryCalls = append(m.clearHistoryCalls, pane)
	return nil
}

func (m *mockTmux) RespawnPane(pane, command string) error {
	m.respawnPaneCalls = append(m.respawnPaneCalls, struct{ pane, cmd string }{pane, command})
	return m.respawnErr
}

func TestRespawnSession_CallsFullSequence(t *testing.T) {
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot: "/tmp/factory",
		AgentName:   "test-agent",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%5",
		Tx:          mock,
	}

	err := respawnSession(opts)
	if err != nil {
		t.Fatalf("respawnSession: %v", err)
	}

	if len(mock.clearHistoryCalls) != 1 || mock.clearHistoryCalls[0] != "%5" {
		t.Errorf("ClearHistory should be called once with pane %%5, got %v", mock.clearHistoryCalls)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Fatalf("RespawnPane should be called once, got %d calls", len(mock.respawnPaneCalls))
	}
	if mock.respawnPaneCalls[0].pane != "%5" {
		t.Errorf("RespawnPane pane = %q, want %%5", mock.respawnPaneCalls[0].pane)
	}
	if mock.respawnPaneCalls[0].cmd == "" {
		t.Error("RespawnPane command should not be empty")
	}
}

func TestRespawnSession_PrependsCommandPrefix(t *testing.T) {
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot: "/tmp/factory",
		AgentName:   "test-agent",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%0",
		CmdPrefix:   "sleep 60 && ",
		Tx:          mock,
	}

	err := respawnSession(opts)
	if err != nil {
		t.Fatalf("respawnSession: %v", err)
	}

	if len(mock.respawnPaneCalls) != 1 {
		t.Fatalf("RespawnPane should be called once, got %d", len(mock.respawnPaneCalls))
	}
	cmd := mock.respawnPaneCalls[0].cmd
	if !strings.HasPrefix(cmd, "sleep 60 && ") {
		t.Errorf("command should start with prefix, got: %s", cmd)
	}
}

func TestRespawnSession_SetsWorktreeWhenPathProvided(t *testing.T) {
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot:  "/tmp/factory",
		AgentName:    "test-agent",
		AgentEntry:   config.AgentEntry{Type: "autonomous"},
		PaneID:       "%0",
		WorktreePath: "/tmp/factory/.agentfactory/worktrees/wt-abc",
		WorktreeID:   "wt-abc",
		Tx:           mock,
	}

	err := respawnSession(opts)
	if err != nil {
		t.Fatalf("respawnSession: %v", err)
	}

	cmd := mock.respawnPaneCalls[0].cmd
	if !strings.Contains(cmd, "AF_WORKTREE=") {
		t.Errorf("command should contain AF_WORKTREE env export when worktree is set, got: %s", cmd)
	}
}

func TestRespawnSession_SkipsWorktreeWhenPathEmpty(t *testing.T) {
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot: "/tmp/factory",
		AgentName:   "test-agent",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%0",
		Tx:          mock,
	}

	err := respawnSession(opts)
	if err != nil {
		t.Fatalf("respawnSession: %v", err)
	}

	cmd := mock.respawnPaneCalls[0].cmd
	if strings.Contains(cmd, "AF_WORKTREE=") {
		t.Errorf("command should NOT contain AF_WORKTREE when worktree is not set, got: %s", cmd)
	}
}

func TestRespawnSession_ReturnsRespawnPaneError(t *testing.T) {
	mock := &mockTmux{respawnErr: fmt.Errorf("tmux respawn failed")}
	opts := RespawnOptions{
		FactoryRoot: "/tmp/factory",
		AgentName:   "test-agent",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%0",
		Tx:          mock,
	}

	err := respawnSession(opts)
	if err == nil {
		t.Fatal("expected error from respawnSession when RespawnPane fails")
	}
	if !strings.Contains(err.Error(), "tmux respawn failed") {
		t.Errorf("error should propagate RespawnPane error, got: %v", err)
	}
}

func TestCaptureCheckpointWithFormula_WritesCheckpoint(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	err := captureCheckpointWithFormula(t.Context(), workDir, "test notes", nil)
	if err != nil {
		t.Fatalf("captureCheckpointWithFormula: %v", err)
	}

	cpPath := filepath.Join(workDir, ".agent-checkpoint.json")
	data, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatalf("checkpoint should exist: %v", err)
	}
	if !strings.Contains(string(data), "test notes") {
		t.Error("checkpoint should contain notes")
	}
}

func TestCaptureCheckpointWithFormula_AppliesMutator(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	err := captureCheckpointWithFormula(t.Context(), workDir, "compaction notes", func(cp *checkpoint.Checkpoint) {
		cp.CompactionHandoff = true
	})
	if err != nil {
		t.Fatalf("captureCheckpointWithFormula: %v", err)
	}

	cpPath := filepath.Join(workDir, ".agent-checkpoint.json")
	data, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "compaction_handoff") {
		t.Error("checkpoint should contain compaction_handoff when mutator sets it")
	}
}

func TestCaptureCheckpointWithFormula_SetsSessionID(t *testing.T) {
	t.Setenv("CLAUDE_SESSION_ID", "test-session-123")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	err := captureCheckpointWithFormula(t.Context(), workDir, "session test", nil)
	if err != nil {
		t.Fatalf("captureCheckpointWithFormula: %v", err)
	}

	cpPath := filepath.Join(workDir, ".agent-checkpoint.json")
	data, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "test-session-123") {
		t.Error("checkpoint should contain CLAUDE_SESSION_ID")
	}
}

func TestHandoff_NoInlineFormulaEnrichment(t *testing.T) {
	src, err := os.ReadFile("handoff.go")
	if err != nil {
		t.Fatalf("reading handoff.go: %v", err)
	}
	code := string(src)

	if strings.Contains(code, "writeHandoffCheckpoint") {
		t.Error("handoff.go should not use writeHandoffCheckpoint — use captureCheckpointWithFormula()")
	}
	if strings.Contains(code, "checkpoint.Write(") {
		t.Error("handoff.go should not call checkpoint.Write directly — use captureCheckpointWithFormula()")
	}
	if !strings.Contains(code, "captureCheckpointWithFormula(") {
		t.Error("handoff.go should call captureCheckpointWithFormula() for checkpoint writing")
	}
}

func TestCompactHandoff_NoInlineFormulaEnrichment(t *testing.T) {
	src, err := os.ReadFile("compact_handoff.go")
	if err != nil {
		t.Fatalf("reading compact_handoff.go: %v", err)
	}
	code := string(src)

	if strings.Contains(code, "readHookedFormulaID") {
		t.Error("compact_handoff.go should not call readHookedFormulaID directly — use captureCheckpointWithFormula()")
	}
	if strings.Contains(code, "checkpoint.Write(") {
		t.Error("compact_handoff.go should not call checkpoint.Write directly — use captureCheckpointWithFormula()")
	}
	if !strings.Contains(code, "captureCheckpointWithFormula(") {
		t.Error("compact_handoff.go should call captureCheckpointWithFormula() for checkpoint writing")
	}
}

func TestHandoff_UsesRespawnSession(t *testing.T) {
	src, err := os.ReadFile("handoff.go")
	if err != nil {
		t.Fatalf("reading handoff.go: %v", err)
	}
	code := string(src)

	if !strings.Contains(code, "respawnSession(") {
		t.Error("handoff.go must call respawnSession() instead of inline respawn logic")
	}
	if strings.Contains(code, "mgr := session.NewManager(") {
		t.Error("handoff.go should not call session.NewManager directly — use respawnSession()")
	}
}

func TestCompactHandoff_UsesRespawnSession(t *testing.T) {
	src, err := os.ReadFile("compact_handoff.go")
	if err != nil {
		t.Fatalf("reading compact_handoff.go: %v", err)
	}
	code := string(src)

	if !strings.Contains(code, "respawnSession(") {
		t.Error("compact_handoff.go must call respawnSession() instead of inline respawn logic")
	}
	if strings.Contains(code, "mgr := session.NewManager(") {
		t.Error("compact_handoff.go should not call session.NewManager directly — use respawnSession()")
	}
}

func TestWatchdog_UsesRespawnSession(t *testing.T) {
	src, err := os.ReadFile("watchdog.go")
	if err != nil {
		t.Fatalf("reading watchdog.go: %v", err)
	}
	code := string(src)

	if !strings.Contains(code, "respawnSession(") {
		t.Error("watchdog.go must call respawnSession() instead of inline respawn logic")
	}
	if strings.Contains(code, "mgr := session.NewManager(") {
		t.Error("watchdog.go should not call session.NewManager directly — use respawnSession()")
	}
}
