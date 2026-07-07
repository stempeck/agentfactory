package cmd

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestWatchdog_BareConnectionErrorNoFalsePositive covers the PR #509 review finding
// that "connection refused" / "connection timed out" were matched as bare substrings.
// "connection refused" is Go's ECONNREFUSED text — an agent running go test, curl, or
// ssh against a closed port prints it for reasons unrelated to its model gateway, and a
// first match forcibly checkpoints and respawns the session. A benign local connection
// error with no model-gateway context must NOT trip detection.
func TestWatchdog_BareConnectionErrorNoFalsePositive(t *testing.T) {
	outputs := []string{
		"dial tcp 127.0.0.1:4000: connect: connection refused",
		"read tcp 127.0.0.1:54233->127.0.0.1:6379: connection timed out",
		"go: could not connect to proxy: dial tcp 10.0.0.5:443: connect: connection refused",
	}
	for _, output := range outputs {
		detected, pattern := detectErrorPattern(output)
		if detected {
			t.Errorf("benign local connection error must not trigger a respawn: %q -> %q", output, pattern)
		}
	}
}

// TestRespawn_ProfileSwitch_ClearsStaleRedirectVars covers the PR #509 review finding
// that the redirect-var hygiene ran only in Start(), never on the respawn paths. It
// drives the real respawn path (respawnSession -> session.NewManager ->
// BuildStartupCommand -> RespawnPane) with a model-only profile and asserts the
// respawned command clears the stale model-redirect vars a prior profile could have
// left on the reused session.
func TestRespawn_ProfileSwitch_ClearsStaleRedirectVars(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{"sonnet": {"ANTHROPIC_MODEL": "claude-sonnet-4-6"}},
		Agents: map[string]string{"manager": "sonnet"},
	})

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
	cmd := mock.respawnPaneCalls[0].cmd
	for _, want := range []string{
		"ANTHROPIC_SMALL_FAST_MODEL=''",
		"ANTHROPIC_DEFAULT_OPUS_MODEL=''",
		"ANTHROPIC_DEFAULT_SONNET_MODEL=''",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL=''",
		"CLAUDE_CODE_SUBAGENT_MODEL=''",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("respawn with a model-only profile must clear stale redirect var %q so a prior profile cannot survive the reused session; got: %s", want, cmd)
		}
	}
}
