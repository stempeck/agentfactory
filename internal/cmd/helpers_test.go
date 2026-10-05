package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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
	unsetCalls        []struct {
		target string
		keys   []string
	}
	unsetErr error
	// calls is the cross-method call order, so a test can pin that a tmux effect happens
	// before the pane is respawned rather than merely at some point.
	calls []string
}

func (m *mockTmux) ClearHistory(pane string) error {
	m.calls = append(m.calls, "ClearHistory")
	m.clearHistoryCalls = append(m.clearHistoryCalls, pane)
	return nil
}

func (m *mockTmux) RespawnPane(pane, command string) error {
	m.calls = append(m.calls, "RespawnPane")
	m.respawnPaneCalls = append(m.respawnPaneCalls, struct{ pane, cmd string }{pane, command})
	return m.respawnErr
}

func (m *mockTmux) UnsetEnvironment(target string, keys ...string) error {
	m.calls = append(m.calls, "UnsetEnvironment")
	m.unsetCalls = append(m.unsetCalls, struct {
		target string
		keys   []string
	}{target, append([]string(nil), keys...)})
	return m.unsetErr
}

func TestRespawnSession_CallsFullSequence(t *testing.T) {
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot: t.TempDir(),
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

// An older af wrote every launch family into the tmux SESSION env, and respawn-pane -k hands those
// copies to the new process, so the recycle must unset them before the pane is replaced.
func TestPR724_T9_RespawnUnsetsStaleSessionEnvBeforeRespawnPane(t *testing.T) {
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot: t.TempDir(),
		AgentName:   "test-agent",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%5",
		Tx:          mock,
	}

	if err := respawnSession(opts); err != nil {
		t.Fatalf("respawnSession: %v", err)
	}

	if len(mock.unsetCalls) != 1 {
		t.Fatalf("respawnSession issued %d UnsetEnvironment calls, want exactly 1 batched scrub; tmux call order: %v", len(mock.unsetCalls), mock.calls)
	}
	unset := mock.unsetCalls[0]
	if unset.target != "%5" {
		t.Errorf("scrub targets %q, want the respawned pane %%5", unset.target)
	}
	if u, r := slices.Index(mock.calls, "UnsetEnvironment"), slices.Index(mock.calls, "RespawnPane"); r < 0 || u > r {
		t.Errorf("scrub must run before RespawnPane, or the new process inherits the stale copies; tmux call order: %v", mock.calls)
	}

	named := slices.Concat([]string{
		"AF_BUILD_MODE", "AF_BUILD_HOST", "AF_BUILD_USER", "AF_HOST_MOUNT",
		"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL",
		"GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0",
		"AF_COAUTHOR_NAME", "AF_COAUTHOR_EMAIL",
		config.EnvEffortLevel,
	}, config.RedirectFamilyEnvVars)
	for _, k := range named {
		if !slices.Contains(unset.keys, k) {
			t.Errorf("scrub does not unset %s, so its stale session copy survives the recycle\nkeys: %v", k, unset.keys)
		}
	}
	for _, k := range []string{"AF_ROOT", "AF_ROLE", "AF_ACTOR", "AF_WORKTREE", "AF_WORKTREE_ID"} {
		if slices.Contains(unset.keys, k) {
			t.Errorf("scrub unsets identity key %s, which tmux must keep for the session\nkeys: %v", k, unset.keys)
		}
	}
}

func TestPR724_T9_RefusedRespawnScrubsNothing(t *testing.T) {
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot: k14Factory(t),
		AgentName:   "acme-triage",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%5",
		Tx:          mock,
	}

	if err := respawnSession(opts); err == nil {
		t.Fatal("respawnSession must refuse a plugin agent whose template is not embedded; this test needs a refused recycle")
	}
	if len(mock.unsetCalls) != 0 {
		t.Errorf("a refused recycle scrubbed tmux env it will not replace: %v", mock.unsetCalls)
	}
}

// A failed scrub leaves the stale copies in place, which is the defect itself, so it must be loud;
// but it must not cost the agent its respawn, whose error alone decides the return value.
func TestPR724_T9_RespawnSurfacesScrubFailureAndStillRespawns(t *testing.T) {
	const scrubErr = "pr724-d1 unset exploded"
	mock := &mockTmux{unsetErr: errors.New(scrubErr)}
	opts := RespawnOptions{
		FactoryRoot: t.TempDir(),
		AgentName:   "test-agent",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%5",
		Tx:          mock,
	}

	var err error
	stderr := captureStderr(t, func() { err = respawnSession(opts) })

	if err != nil {
		t.Errorf("a failed scrub must not fail the recycle; respawnSession = %v", err)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Errorf("RespawnPane must still run once after a failed scrub, ran %d times", len(mock.respawnPaneCalls))
	}
	surfaced := slices.ContainsFunc(strings.Split(stderr, "\n"), func(line string) bool {
		return strings.Contains(line, "test-agent") && strings.Contains(line, scrubErr)
	})
	if !surfaced {
		t.Errorf("stderr carries no line naming the agent and the scrub error %q:\n%s", scrubErr, stderr)
	}
}

// The unset primitive belongs to the respawn seam only; keeping it off the wider cmd seam keeps
// every non-recycle launch path structurally unable to scrub.
func TestPR724_T9_KeepCmdTmuxSeamFreeOfUnset(t *testing.T) {
	if _, ok := reflect.TypeOf((*cmdTmux)(nil)).Elem().MethodByName("UnsetEnvironment"); ok {
		t.Error("cmdTmux must not carry UnsetEnvironment; the scrub belongs to respawnTmux only")
	}
}

func TestRespawnSession_PrependsCommandPrefix(t *testing.T) {
	mock := &mockTmux{}
	opts := RespawnOptions{
		FactoryRoot: t.TempDir(),
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
	root := t.TempDir()
	opts := RespawnOptions{
		FactoryRoot:  root,
		AgentName:    "test-agent",
		AgentEntry:   config.AgentEntry{Type: "autonomous"},
		PaneID:       "%0",
		WorktreePath: filepath.Join(root, ".agentfactory", "worktrees", "wt-abc"),
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
		FactoryRoot: t.TempDir(),
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
		FactoryRoot: t.TempDir(),
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
