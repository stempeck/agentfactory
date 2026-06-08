//go:build integration

package mail

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

// TestNotifyRecipient_SkipsWhenClaudeNotRunning drives the REAL tmux server: it
// creates an af-test-notify-guard session and asserts notifyRecipient does not
// banner a bare shell. Under the default-build GUARD its NewSession would be an
// inert no-op (making the assertion vacuous), so it runs only under
// `make test-integration`. TestNotifyRecipientBestEffort stays untagged: it only
// drives the read-only HasSession probe and is safe under the guard.
func TestNotifyRecipient_SkipsWhenClaudeNotRunning(t *testing.T) {
	if exec.Command("tmux", "-V").Run() != nil {
		t.Skip("tmux not available")
	}

	tx := tmux.NewTmux()
	agentName := "test-notify-guard"
	sessionName := session.SessionName(agentName)

	_ = tx.KillSession(sessionName)
	var sessionErr error
	for range 3 {
		sessionErr = tx.NewSession(sessionName, "/tmp")
		if sessionErr == nil {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if sessionErr != nil {
		t.Fatalf("NewSession: %v", sessionErr)
	}
	defer tx.KillSession(sessionName)

	if err := tx.WaitForShellReady(sessionName, 5*time.Second); err != nil {
		t.Fatalf("WaitForShellReady: %v", err)
	}

	r := &Router{}
	msg := NewMessage("manager", agentName, "SHOULD_NOT_APPEAR", "body")
	r.notifyRecipient(msg)

	time.Sleep(300 * time.Millisecond)

	content, err := tx.CapturePane(sessionName, 30)
	if err != nil {
		t.Fatalf("CapturePane: %v", err)
	}

	if strings.Contains(content, "SHOULD_NOT_APPEAR") {
		t.Fatal("banner was sent to bare shell — IsClaudeRunning guard is missing or broken")
	}
}
