package session

import (
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// fakeTmuxClient is a no-op tmuxClient double used to prove the newManagerTmux
// seam wires an injected client into NewManager. It implements the exact
// 11-method tmuxClient union.
type fakeTmuxClient struct{ id string }

func (f *fakeTmuxClient) HasSession(name string) (bool, error)            { return false, nil }
func (f *fakeTmuxClient) IsClaudeRunning(session string) bool             { return false }
func (f *fakeTmuxClient) KillSession(name string) error                   { return nil }
func (f *fakeTmuxClient) NewSession(name, workDir string) error           { return nil }
func (f *fakeTmuxClient) SetEnvironment(session, key, value string) error { return nil }
func (f *fakeTmuxClient) WaitForShellReady(session string, timeout time.Duration) error {
	return nil
}
func (f *fakeTmuxClient) SendKeysDelayed(session, keys string, delayMs int) error { return nil }
func (f *fakeTmuxClient) WaitForCommand(session string, excludeCommands []string, timeout time.Duration) error {
	return nil
}
func (f *fakeTmuxClient) AcceptBypassPermissionsWarning(session string) error { return nil }
func (f *fakeTmuxClient) NudgeSession(session, message string) error          { return nil }
func (f *fakeTmuxClient) SendKeysRaw(session, keys string) error              { return nil }

func TestSessionPrefixFnSeam(t *testing.T) {
	orig := sessionPrefixFn
	defer func() { sessionPrefixFn = orig }()

	if got := SessionName("manager"); got != "af-manager" {
		t.Fatalf("production SessionName(manager) = %q, want af-manager", got)
	}
	sessionPrefixFn = func() string { return "t123-" }
	if got := SessionName("manager"); got != "t123-manager" {
		t.Fatalf("seamed SessionName(manager) = %q, want t123-manager", got)
	}
	sessionPrefixFn = orig
	if got := SessionName("manager"); got != "af-manager" {
		t.Fatalf("restored SessionName(manager) = %q, want af-manager", got)
	}
}

func TestWatchdogSessionNameAuthority(t *testing.T) {
	if got := WatchdogSessionName(); got != "af-watchdog" {
		t.Fatalf("WatchdogSessionName() = %q, want af-watchdog", got)
	}
	orig := sessionPrefixFn
	defer func() { sessionPrefixFn = orig }()
	sessionPrefixFn = func() string { return "ns-" }
	if got := WatchdogSessionName(); got != "ns-watchdog" {
		t.Fatalf("seamed WatchdogSessionName() = %q, want ns-watchdog", got)
	}
}

func TestDispatchSessionNameAuthority(t *testing.T) {
	if got := DispatchSessionName(); got != "af-dispatch" {
		t.Fatalf("DispatchSessionName() = %q, want af-dispatch", got)
	}
}

func TestNewManagerUsesTmuxSeam(t *testing.T) {
	orig := newManagerTmux
	defer func() { newManagerTmux = orig }()

	fake := &fakeTmuxClient{id: "fake"}
	newManagerTmux = func() tmuxClient { return fake }

	m := NewManager("/root", "manager", config.AgentEntry{})
	if m.tmux != fake {
		t.Fatalf("NewManager did not use newManagerTmux seam; m.tmux = %v", m.tmux)
	}
}
