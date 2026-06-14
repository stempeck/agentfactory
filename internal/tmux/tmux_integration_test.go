//go:build integration

package tmux

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// These functions drive the REAL tmux server (create/kill af-test-* sessions and
// assert real tmux state). Under the default-build GUARD their destructive ops are
// inert no-ops, so their real-state assertions would fail (H2). They run only under
// `make test-integration` (guardMode == false). The untagged hasTmux() helper in
// tmux_test.go compiles into both builds, so it is reused here, not duplicated.

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

	for {
		content, err := tx.CapturePane(name, 20)
		if err != nil {
			t.Fatalf("CapturePane: %v", err)
		}
		if strings.Contains(content, "TESTMARKER123") {
			return
		}
		time.Sleep(200 * time.Millisecond)
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

func TestSendNotificationBanner_Integration(t *testing.T) {
	// Skipped: intermittently fails in CI with "banner not found in pane output"
	// due to unpinned pane geometry / locale / polling-window assumptions.
	// See https://github.com/stempeck/agentfactory-pro/issues/376
	t.Skip("flaky in CI — see issue #376")

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
	// Poll generously: CI runners render the multi-line banner well after the
	// echo command returns, so a short window flakes (banner body present but the
	// ━━━━ border not yet flushed). Capture more history lines too, so a longer
	// banner can't scroll the top border out of the captured region.
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		var err error
		content, err = tx.CapturePane(name, 50)
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
