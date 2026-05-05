package tmux

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func hasTmux() bool {
	return exec.Command("tmux", "-V").Run() == nil
}

func TestIsAvailable(t *testing.T) {
	tx := NewTmux()
	if !tx.IsAvailable() {
		t.Skip("tmux not available")
	}
	// If we got here, IsAvailable returned true — which is correct since tmux is installed
}

func TestSessionLifecycle(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not available")
	}

	tx := NewTmux()
	name := "af-test-lifecycle"

	// Ensure clean state
	_ = tx.KillSession(name)

	// Create session
	if err := tx.NewSession(name, "/tmp"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tx.KillSession(name)

	// HasSession should return true
	exists, err := tx.HasSession(name)
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if !exists {
		t.Fatal("HasSession: expected true after NewSession")
	}

	// ListSessions should include our session
	sessions, err := tx.ListSessions()
	if err != nil {
		t.Fatalf("ListSessions: %v", err)
	}
	found := false
	for _, s := range sessions {
		if s == name {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListSessions: %s not found in %v", name, sessions)
	}

	// Kill session
	if err := tx.KillSession(name); err != nil {
		t.Fatalf("KillSession: %v", err)
	}

	// HasSession should return false
	exists, err = tx.HasSession(name)
	if err != nil {
		t.Fatalf("HasSession after kill: %v", err)
	}
	if exists {
		t.Fatal("HasSession: expected false after KillSession")
	}
}

func TestDuplicateSession(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not available")
	}

	tx := NewTmux()
	name := "af-test-dup"

	_ = tx.KillSession(name)

	if err := tx.NewSession(name, "/tmp"); err != nil {
		t.Fatalf("first NewSession: %v", err)
	}
	defer tx.KillSession(name)

	err := tx.NewSession(name, "/tmp")
	if !errors.Is(err, ErrSessionExists) {
		t.Fatalf("expected ErrSessionExists, got: %v", err)
	}
}

func TestHasSessionNoServer(t *testing.T) {
	// This test verifies that HasSession returns false (not error) when
	// checking a non-existent session. It works even when tmux server is running.
	tx := NewTmux()
	exists, err := tx.HasSession("af-test-definitely-does-not-exist")
	if err != nil {
		t.Fatalf("HasSession: %v", err)
	}
	if exists {
		t.Fatal("HasSession: expected false for non-existent session")
	}
}

func TestSendKeysAndCapture(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not available")
	}

	tx := NewTmux()
	name := "af-test-sendkeys"

	_ = tx.KillSession(name)

	if err := tx.NewSession(name, "/tmp"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tx.KillSession(name)

	// Wait for shell
	if err := tx.WaitForShellReady(name, 5*1e9); err != nil {
		t.Fatalf("WaitForShellReady: %v", err)
	}

	// Send a command
	if err := tx.SendKeys(name, "echo TESTMARKER123"); err != nil {
		t.Fatalf("SendKeys: %v", err)
	}

	// Brief wait for command to execute
	time.Sleep(200 * time.Millisecond)

	// Capture pane
	content, err := tx.CapturePane(name, 20)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}

	if !strings.Contains(content, "TESTMARKER123") {
		t.Fatalf("expected pane to contain TESTMARKER123, got: %s", content)
	}
}

func TestWrapError(t *testing.T) {
	tx := NewTmux()

	tests := []struct {
		name     string
		stderr   string
		expected error
	}{
		{"no server", "no server running on /tmp/tmux-501/default", ErrNoServer},
		{"error connecting", "error connecting to /tmp/tmux-501/default", ErrNoServer},
		{"duplicate", "duplicate session: foo", ErrSessionExists},
		{"not found", "session not found: bar", ErrSessionNotFound},
		{"cant find", "can't find session: baz", ErrSessionNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tx.wrapError(errors.New("exit 1"), tt.stderr, []string{"test-cmd"})
			if !errors.Is(err, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, err)
			}
		})
	}
}

func TestIsClaudeRunning(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not available")
	}

	tx := NewTmux()
	name := "af-test-claude-check"

	_ = tx.KillSession(name)

	if err := tx.NewSession(name, "/tmp"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tx.KillSession(name)

	// Shell is running, not Claude — should return false
	if tx.IsClaudeRunning(name) {
		t.Fatal("IsClaudeRunning: expected false when shell is running")
	}
}

func TestSetEnvironment(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not available")
	}

	tx := NewTmux()
	name := "af-test-env"

	_ = tx.KillSession(name)

	if err := tx.NewSession(name, "/tmp"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tx.KillSession(name)

	if err := tx.SetEnvironment(name, "TEST_VAR", "hello"); err != nil {
		t.Fatalf("SetEnvironment: %v", err)
	}
}

func TestGetPaneCommand(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not available")
	}

	tx := NewTmux()
	name := "af-test-panecmd"

	_ = tx.KillSession(name)

	if err := tx.NewSession(name, "/tmp"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tx.KillSession(name)

	cmd, err := tx.GetPaneCommand(name)
	if err != nil {
		t.Fatalf("GetPaneCommand: %v", err)
	}

	// Should be running a shell
	isShell := false
	for _, shell := range supportedShells {
		if cmd == shell {
			isShell = true
			break
		}
	}
	if !isShell {
		t.Fatalf("expected shell command, got: %s", cmd)
	}
}

func TestSupportedShells(t *testing.T) {
	shells := SupportedShells()
	if len(shells) == 0 {
		t.Fatal("SupportedShells returned empty list")
	}
	// Must contain at least bash and zsh
	hasBash := false
	hasZsh := false
	for _, s := range shells {
		if s == "bash" {
			hasBash = true
		}
		if s == "zsh" {
			hasZsh = true
		}
	}
	if !hasBash {
		t.Fatal("SupportedShells missing bash")
	}
	if !hasZsh {
		t.Fatal("SupportedShells missing zsh")
	}
}

func TestSendNotificationBanner_Injection(t *testing.T) {
	payloads := []struct {
		name    string
		subject string
	}{
		{"single quote breakout", "'; rm -rf / #"},
		{"backtick command sub", "`whoami`"},
		{"dollar command sub", "$(id)"},
		{"semicolon injection", "; echo pwned"},
		{"nested quote payload", "'$(cat /etc/passwd)'"},
	}

	for _, tt := range payloads {
		t.Run(tt.name, func(t *testing.T) {
			quoted := shellQuote(tt.subject)
			if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
				t.Errorf("shellQuote(%q) = %q, want single-quoted wrapper", tt.subject, quoted)
			}
			inner := quoted[1 : len(quoted)-1]
			if strings.Contains(inner, "'") && !strings.Contains(inner, `'\''`) {
				t.Errorf("shellQuote(%q) has unescaped single quote in inner: %q", tt.subject, inner)
			}
		})
	}
}

func TestSendNotificationBanner_Display(t *testing.T) {
	cases := []struct {
		name    string
		subject string
	}{
		{"apostrophe", "it's working"},
		{"double quotes", `error: "not found"`},
		{"percent", "100% complete"},
		{"ampersand", "foo & bar"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			quoted := shellQuote(tt.subject)
			if !strings.HasPrefix(quoted, "'") || !strings.HasSuffix(quoted, "'") {
				t.Errorf("shellQuote(%q) = %q, want single-quoted wrapper", tt.subject, quoted)
			}
		})
	}
}

func TestSendNotificationBanner_Normal(t *testing.T) {
	cases := []struct {
		name    string
		subject string
	}{
		{"work done", "WORK_DONE"},
		{"quality gate", "QUALITY_GATE"},
		{"help with colon", "HELP: unclear requirements"},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			quoted := shellQuote(tt.subject)
			unquoted := quoted[1 : len(quoted)-1]
			if unquoted != tt.subject {
				t.Errorf("shellQuote(%q) inner = %q, want %q", tt.subject, unquoted, tt.subject)
			}
		})
	}
}

func TestSendNotificationBanner_Integration(t *testing.T) {
	if !hasTmux() {
		t.Skip("tmux not available")
	}

	tx := NewTmux()
	name := "af-test-banner-injection"

	_ = tx.KillSession(name)

	if err := tx.NewSession(name, "/tmp"); err != nil {
		t.Fatalf("NewSession: %v", err)
	}
	defer tx.KillSession(name)

	if err := tx.WaitForShellReady(name, 5*time.Second); err != nil {
		t.Fatalf("WaitForShellReady: %v", err)
	}

	if err := tx.SendNotificationBanner(name, "testagent", "'; echo INJECTED #"); err != nil {
		t.Fatalf("SendNotificationBanner: %v", err)
	}

	var content string
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		content, err = tx.CapturePane(name, 20)
		if err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		if strings.Contains(content, "━━━━") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}

	if !strings.Contains(content, "━━━━") {
		t.Fatalf("banner not found in pane output:\n%s", content)
	}

	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == "INJECTED" {
			t.Fatalf("injection payload was executed: INJECTED appears on its own line:\n%s", content)
		}
	}
}
