package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// startAgentWithEntry mirrors startMouseAgent (mouseoption_test.go) but lets the
// caller supply the AgentEntry, so the W12 read-path guard can be exercised through
// the real Manager.Start() path with a legacy remote endpoint + credential token.
func startAgentWithEntry(t *testing.T, entry config.AgentEntry) *Manager {
	t.Helper()

	origMem := checkAvailableMemoryFunc
	checkAvailableMemoryFunc = func() (uint64, error) { return 100000, nil }
	t.Cleanup(func() { checkAvailableMemoryFunc = origMem })

	fake := newFakeTmux()
	restore := InstallHermeticForTest("af-test-", func() TmuxForTest { return fake })
	t.Cleanup(restore)

	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")
	agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "legacyagent")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("creating agent dir: %v", err)
	}

	mgr := NewManager(tmpDir, "legacyagent", entry)
	if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}
	return mgr
}

// credentialShapedToken is a fake provider-key-shaped literal used only to trip the
// sk- heuristic in tests. Split so no scanner mistakes it for a real leaked key.
const credentialShapedToken = "sk-" + "REDACTEDdeadbeefcafe0123456789"

// TestStart_WarnsOnCredentialTokenOnRemoteBaseURL pins Issue #508 W12 (AC-5): a
// credential-shaped literal auth_token on a NON-loopback legacy base_url produces a
// LOUD, non-fatal stderr warning at the session boundary that names the agent, the
// agents.json file, and the file: secret-reference alternative. It must NOT fail
// the launch (the 43052536 warn-only posture).
func TestStart_WarnsOnCredentialTokenOnRemoteBaseURL(t *testing.T) {
	mgr := startAgentWithEntry(t, config.AgentEntry{
		Type:        "autonomous",
		Description: "legacy remote endpoint",
		BaseURL:     "https://api.openai.com/v1",
		AuthToken:   credentialShapedToken,
	})

	var startErr error
	out := captureStderr(t, func() { startErr = mgr.Start() })

	if startErr != nil {
		t.Fatalf("W12 guard is warn-only; Start() must not fail, got: %v", startErr)
	}
	for _, want := range []string{"legacyagent", "agents.json", "file:", "credential-shaped auth_token"} {
		if !strings.Contains(out, want) {
			t.Errorf("credential-on-remote warning must name %q; stderr:\n%s", want, out)
		}
	}
}

// TestStart_NoCredentialWarnOnLoopbackBaseURL proves loopback endpoints are exempt:
// the same credential-shaped token on http://localhost does NOT warn (the seeded
// lmstudio/loopback profiles are legitimate and must stay quiet).
func TestStart_NoCredentialWarnOnLoopbackBaseURL(t *testing.T) {
	mgr := startAgentWithEntry(t, config.AgentEntry{
		Type:        "autonomous",
		Description: "loopback endpoint",
		BaseURL:     "http://localhost:1234/v1",
		AuthToken:   credentialShapedToken,
	})

	out := captureStderr(t, func() { _ = mgr.Start() })

	if strings.Contains(out, "credential-shaped auth_token") {
		t.Errorf("loopback endpoint must be exempt from the credential-on-remote warning; stderr:\n%s", out)
	}
}

// TestStart_NoCredentialWarnForNonCredentialToken proves the guard is narrow: a
// non-credential literal (e.g. the legitimate "tok" fixture) on a remote base_url
// does not warn — only the sk- provider-key shape trips it.
func TestStart_NoCredentialWarnForNonCredentialToken(t *testing.T) {
	mgr := startAgentWithEntry(t, config.AgentEntry{
		Type:        "autonomous",
		Description: "remote endpoint, dummy token",
		BaseURL:     "https://api.openai.com/v1",
		AuthToken:   "tok",
	})

	out := captureStderr(t, func() { _ = mgr.Start() })

	if strings.Contains(out, "credential-shaped auth_token") {
		t.Errorf("a non-credential token must not warn; stderr:\n%s", out)
	}
}
