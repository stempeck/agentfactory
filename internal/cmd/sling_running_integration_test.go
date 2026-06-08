//go:build integration

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestDispatchToSpecialist_RunningAgentWithoutResetErrors proves that
// dispatching to an already-running agent without --reset fails BEFORE creating
// beads. The "already running" pre-flight in dispatchToSpecialist uses a raw
// tmux.NewTmux() client, so it can only be exercised against a REAL tmux
// session — it cannot be faked without changing production code. Gated behind
// //go:build integration; runs only under `make test-integration` (#309).
func TestDispatchToSpecialist_RunningAgentWithoutResetErrors(t *testing.T) {
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	// Create a tmux session to simulate a running agent
	sessionName := "af-specialist-agent"
	createErr := exec.Command("tmux", "new-session", "-d", "-s", sessionName).Run()
	if createErr != nil {
		t.Skip("tmux not available, skipping integration test")
	}
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	// Set --reset to false
	origReset := slingReset
	origNoLaunch := slingNoLaunch
	slingReset = false
	slingNoLaunch = false
	defer func() {
		slingReset = origReset
		slingNoLaunch = origNoLaunch
	}()

	err := dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "some task")
	if err == nil {
		t.Fatal("expected error when dispatching to running agent without --reset")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error should contain 'already running', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "--reset") {
		t.Errorf("error should mention '--reset', got: %s", err.Error())
	}

	// Verify no bead was created (error fired BEFORE instantiation)
	hookedPath := filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "hooked_formula")
	if _, statErr := os.Stat(hookedPath); statErr == nil {
		t.Error("hooked_formula should NOT exist — error should fire before bead creation")
	}
}
