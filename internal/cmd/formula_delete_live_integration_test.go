//go:build integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/session"
)

// TestFormulaAgentGen_DeleteRefusesLiveSession proves that `agent-gen --delete`
// refuses to remove an agent whose tmux session is live. This requires a REAL
// tmux session (the refusal is driven by a real has-session check), so it
// cannot be faked and is gated behind //go:build integration — it runs only
// under `make test-integration`, never in the default suite (#309 Phase 3).
func TestFormulaAgentGen_DeleteRefusesLiveSession(t *testing.T) {
	// AC3: requires tmux binary
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	dir := setupFormulaFactory(t)

	// Create agent
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen create failed: %v", err)
	}

	// Start a tmux session matching the agent's session name
	sessionID := session.SessionName("investigate") // "af-investigate"
	startCmd := exec.Command("tmux", "new-session", "-d", "-s", sessionID)
	if err := startCmd.Run(); err != nil {
		t.Fatalf("creating tmux session: %v", err)
	}
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionID).Run()
	})

	// Attempt delete — should refuse
	_, _, err = runFormulaAgentGenInDir(t, dir, "investigate", "--delete")
	if err == nil {
		t.Fatal("expected error when deleting agent with live session")
	}
	if !strings.Contains(err.Error(), "live tmux session") {
		t.Errorf("error should mention 'live tmux session', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "af down investigate") {
		t.Errorf("error should mention 'af down investigate', got: %s", err.Error())
	}

	// Verify artifacts are NOT removed
	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	if !strings.Contains(string(agentsData), `"investigate"`) {
		t.Error("investigate entry should still be in agents.json after refused delete")
	}
}
