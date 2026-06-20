package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestBuildStartupCommand_GitIdentityWhenSet verifies that once SetGitIdentity is
// called, the inline export block carries GIT_AUTHOR_*/GIT_COMMITTER_* (AC-2 env
// half), shell-quoted, before the `&&` (so the Claude Bash tool inherits them).
func TestBuildStartupCommand_GitIdentityWhenSet(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetGitIdentity("agentfactory-cli", "293373236+agentfactory-cli@users.noreply.github.com")

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{
		"GIT_AUTHOR_NAME='agentfactory-cli'",
		"GIT_AUTHOR_EMAIL='293373236+agentfactory-cli@users.noreply.github.com'",
		"GIT_COMMITTER_NAME='agentfactory-cli'",
		"GIT_COMMITTER_EMAIL='293373236+agentfactory-cli@users.noreply.github.com'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command should contain %q, got: %s", want, cmd)
		}
	}
	sepIdx := strings.Index(cmd, "&&")
	if idx := strings.Index(cmd, "GIT_AUTHOR_NAME"); idx < 0 || idx > sepIdx {
		t.Errorf("GIT_AUTHOR_NAME (at %d) must appear before && (at %d)", idx, sepIdx)
	}
}

// TestBuildStartupCommand_GitIdentityOmittedWhenUnset verifies the presence-gate
// (C-4): with no identity set, NO GIT_AUTHOR_*/GIT_COMMITTER_* are exported, so
// git env vars cannot clobber an ambient identity.
func TestBuildStartupCommand_GitIdentityOmittedWhenUnset(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)

	cmd := mgr.BuildStartupCommand()

	for _, banned := range []string{"GIT_AUTHOR_NAME", "GIT_AUTHOR_EMAIL", "GIT_COMMITTER_NAME", "GIT_COMMITTER_EMAIL"} {
		if strings.Contains(cmd, banned) {
			t.Errorf("command without identity must not contain %q, got: %s", banned, cmd)
		}
	}
}

// TestBuildStartupCommand_GitTrailerWhenSet verifies SetGitTrailer activates the
// git-native trailer channel: GIT_CONFIG_* sets core.hooksPath, and the co-author
// value is passed to the hook via env (AC-4/AC-5; one source of truth, no literal
// hard-coded in the shell hook).
func TestBuildStartupCommand_GitTrailerWhenSet(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetGitTrailer("/tmp/factory/.agentfactory/githooks", "agentfactory-cli", "293373236+agentfactory-cli@users.noreply.github.com")

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{
		"GIT_CONFIG_COUNT=",
		"GIT_CONFIG_KEY_0='core.hooksPath'",
		"GIT_CONFIG_VALUE_0='/tmp/factory/.agentfactory/githooks'",
		"AF_COAUTHOR_NAME='agentfactory-cli'",
		"AF_COAUTHOR_EMAIL='293373236+agentfactory-cli@users.noreply.github.com'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command should contain %q, got: %s", want, cmd)
		}
	}
	sepIdx := strings.Index(cmd, "&&")
	if idx := strings.Index(cmd, "GIT_CONFIG_COUNT"); idx < 0 || idx > sepIdx {
		t.Errorf("GIT_CONFIG_COUNT (at %d) must appear before && (at %d)", idx, sepIdx)
	}
}

// TestBuildStartupCommand_GitTrailerOmittedWhenUnset verifies the trailer channel
// is opt-in at the Manager level, so unit-level startup commands stay clean.
func TestBuildStartupCommand_GitTrailerOmittedWhenUnset(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)

	cmd := mgr.BuildStartupCommand()

	for _, banned := range []string{"GIT_CONFIG_COUNT", "core.hooksPath", "AF_COAUTHOR_NAME", "AF_COAUTHOR_EMAIL"} {
		if strings.Contains(cmd, banned) {
			t.Errorf("command without trailer activation must not contain %q, got: %s", banned, cmd)
		}
	}
}

// TestBuildStartupCommand_GitIdentityShellQuoted verifies identity values are
// POSIX-quoted (injection-safe), mirroring the ANTHROPIC_* quoting discipline.
func TestBuildStartupCommand_GitIdentityShellQuoted(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetGitIdentity("O'Brien; rm -rf /", "evil@example.com")

	cmd := mgr.BuildStartupCommand()

	quoted := shellQuote("O'Brien; rm -rf /")
	if !strings.Contains(cmd, "GIT_AUTHOR_NAME="+quoted) {
		t.Errorf("identity name with metacharacters should be shell-quoted, got: %s", cmd)
	}
}
