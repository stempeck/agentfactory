package cmd

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/session"
)

// TestAgentsList_WritesThroughCmdOutSeam locks in the T6 fix: emitAgents must write
// through the cobra output seam (cmd.OutOrStdout()), not fmt.Println to os.Stdout, so a
// consumer that captures output via cmd.SetOut receives it. The existing agents-list
// tests capture os.Stdout, which masks the missing seam.
func TestAgentsList_WritesThroughCmdOutSeam(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{"w":{"type":"autonomous","description":"d"}}}`)
	installMemStore(t)
	installFakeTmuxPresent(t, session.SessionName("w"))

	var buf bytes.Buffer
	agentsListCmd.SetContext(t.Context())
	agentsListCmd.SetOut(&buf)
	t.Cleanup(func() { agentsListCmd.SetOut(nil) })

	// Absorb any leak to the real os.Stdout (the pre-fix behaviour) so it does not
	// pollute test output; the assertion is on the cobra seam buffer.
	var runErr error
	_ = captureStdout(t, func() {
		runErr = runAgentsList(agentsListCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("runAgentsList: %v", runErr)
	}
	if buf.Len() == 0 {
		t.Fatalf("agents list output did not go through cmd.OutOrStdout(): the cobra seam buffer is empty (emitAgents must use cmd.OutOrStdout())")
	}
	var items []agentListItem
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &items); err != nil {
		t.Fatalf("cobra seam output is not a JSON agent array: %q (%v)", buf.String(), err)
	}
}
