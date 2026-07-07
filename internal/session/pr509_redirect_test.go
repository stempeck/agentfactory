package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestBuildStartupCommand_AuthTokenOnly_PreservesToken covers the PR #509 review
// finding on the inline emitter: an agents.json entry that sets auth_token but leaves
// base_url empty must keep its token. The pre-fix inline path emitted
// ANTHROPIC_AUTH_TOKEN='<tok>' and then, because base_url was empty, immediately
// emitted ANTHROPIC_AUTH_TOKEN='' in the same command — last-write-wins clobbered the
// token, so an auth_token-only agent silently authenticated with an empty token.
func TestBuildStartupCommand_AuthTokenOnly_PreservesToken(t *testing.T) {
	entry := config.AgentEntry{
		Type: "autonomous", Description: "auth-token-only, no base_url",
		AuthToken: "authonly-tok",
	}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	// A resolved model set that carries no ANTHROPIC_BASE_URL is the branch where the
	// legacy auth_token carry — and the token clobber — live.
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "ANTHROPIC_AUTH_TOKEN='authonly-tok'") {
		t.Errorf("auth_token-only config must carry its token, got: %s", cmd)
	}
	if strings.Contains(cmd, "ANTHROPIC_AUTH_TOKEN=''") {
		t.Errorf("auth_token-only config must NOT clear its just-set token; the empty clobber reappeared: %s", cmd)
	}
	// The empty base_url must still be cleared so no stale endpoint survives a reused session.
	if !strings.Contains(cmd, "ANTHROPIC_BASE_URL=''") {
		t.Errorf("an empty base_url must still emit the structural clear, got: %s", cmd)
	}
}

// TestStart_AuthTokenOnly_PreservesToken is the Start() tmux twin of the finding above:
// the recorded env ops must set the auth token and never overwrite it with an empty
// value. Drives the full Start() path through the recording hermetic fake.
func TestStart_AuthTokenOnly_PreservesToken(t *testing.T) {
	origMem := checkAvailableMemoryFunc
	checkAvailableMemoryFunc = func() (uint64, error) { return 100000, nil }
	t.Cleanup(func() { checkAvailableMemoryFunc = origMem })

	fake := installHermeticSession(t)

	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")
	agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "authagent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("creating agent dir: %v", err)
	}
	mgr := NewManager(tmpDir, "authagent", config.AgentEntry{
		Type: "autonomous", Description: "auth-token-only",
		AuthToken: "authonly-tok",
	})
	if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}
	sessionID := mgr.SessionID()

	if !hasOp(fake.ops, "SetEnvironment "+sessionID+" ANTHROPIC_AUTH_TOKEN=authonly-tok") {
		t.Errorf("Start must set the auth token for an auth_token-only config; ops=%v", fake.ops)
	}
	if hasOp(fake.ops, "SetEnvironment "+sessionID+" ANTHROPIC_AUTH_TOKEN=") {
		t.Errorf("Start must NOT clobber the just-set auth token with an empty clear; ops=%v", fake.ops)
	}
}

// TestBuildStartupCommand_ProfileSwitch_ClearsAllStaleRedirectVars covers the PR #509
// review finding that the inline emitter (the only emitter the respawn paths reach)
// cleared just ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN, leaving the six model-redirect
// vars able to survive a profile switch on a reused session. A model-only resolved set
// must clear every redirect-family var it does not carry — at parity with Start()'s
// hygiene pass — while never touching ANTHROPIC_API_KEY.
func TestBuildStartupCommand_ProfileSwitch_ClearsAllStaleRedirectVars(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "model-only profile"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{
		"ANTHROPIC_SMALL_FAST_MODEL=''",
		"ANTHROPIC_DEFAULT_OPUS_MODEL=''",
		"ANTHROPIC_DEFAULT_SONNET_MODEL=''",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=''",
		"CLAUDE_CODE_SUBAGENT_MODEL=''",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("a model-only profile switch must clear stale redirect var %q on the reused session; got: %s", want, cmd)
		}
	}
	// ANTHROPIC_API_KEY is deliberately excluded from redirect hygiene.
	if strings.Contains(cmd, "ANTHROPIC_API_KEY=''") {
		t.Errorf("ANTHROPIC_API_KEY must never be auto-cleared by redirect hygiene; got: %s", cmd)
	}
}
