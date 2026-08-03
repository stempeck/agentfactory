package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/session"
)

// Issue #548 Phase 2 (P5) — the `af sling --agent X --reset` stop is now gated through the same
// scopedStopAllowed authority model as `af down`. The gate refuses only when ALL hold: the caller
// is AuthorityAgent, the target session is running, scopedStopAllowed is not-ok, AND the caller is
// not the dispatch daemon. The operator path short-circuits to a byte-identical no-op before any
// probe (web-console parity). These tests pin that matrix; the operator-context reset behaviors
// (dirty worktree / beads / co-tenant / no-worktree) stay in sling_test.go and prove the no-op.

// assertNotTeardownRefused fails only if err is the K1 teardown refusal — the reset may still
// return unrelated pipeline errors, which are not the gate's concern.
func assertNotTeardownRefused(t *testing.T, err error) {
	t.Helper()
	if err != nil && strings.Contains(err.Error(), "teardown refused") {
		t.Fatalf("sling --reset must not be refused here; got %v", err)
	}
}

// writeSpecialistAgentsJSON overwrites the factory's agents.json with a single autonomous,
// formula-backed specialist — the shape resolveSpecialistAgent + scopedStopAllowed expect.
func writeSpecialistAgentsJSON(t *testing.T, root, agentName, formula string) {
	t.Helper()
	doc := map[string]interface{}{
		"agents": map[string]interface{}{
			agentName: map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     formula,
			},
		},
	}
	data, _ := json.Marshal(doc)
	if err := os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}
}

// TestSlingReset_AgentContext_RefusesRunningNonDispatchedTarget pins AC-3 on the sling surface: an
// agent-context caller that is neither dispatcher nor manager of a RUNNING target, and is not the
// dispatch daemon, is refused with the K1 message — mgr.Stop() is never reached.
func TestSlingReset_AgentContext_RefusesRunningNonDispatchedTarget(t *testing.T) {
	root, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "sibling") // autonomous specialist caller: not interactive, not the dispatcher
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("solver")] = true // target is running

	origReset := slingReset
	slingReset = true
	t.Cleanup(func() { slingReset = origReset })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := dispatchToSpecialist(cmd, root, root, "solver", "task")
	assertTeardownRefused(t, err, "af sling --reset")
}

// TestSlingReset_AgentContext_RefusesOnHasSessionError pins S-1 (PR #553): when the running-state
// probe (HasSession) itself FAILS, the gate must fail CLOSED — treat the target as running so an
// unauthorized agent-context caller is refused, never falling through to mgr.Stop() + resetAgentState
// (the destructive reset). Before the fix the probe error is swallowed (running defaults to false),
// the refusal is skipped, and a transient tmux fault turns an unauthorized stop into a destructive
// reset of a running non-dispatched worker. present stays false, so ONLY the fail-closed fix refuses.
func TestSlingReset_AgentContext_RefusesOnHasSessionError(t *testing.T) {
	root, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "sibling") // autonomous specialist caller: not interactive, not the dispatcher
	t.Setenv("TMUX", "")
	fake.hasSessionErr[session.SessionName("solver")] = errors.New("tmux fault: has-session failed")

	origReset := slingReset
	slingReset = true
	t.Cleanup(func() { slingReset = origReset })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := dispatchToSpecialist(cmd, root, root, "solver", "task")
	assertTeardownRefused(t, err, "af sling --reset")
}

// TestSlingReset_AgentContext_NotRunningProceeds pins the not-running carve-out: the first-dispatch
// and redispatch-after-crash flows must keep working. An agent-context caller resetting a target
// that is NOT running is not refused.
func TestSlingReset_AgentContext_NotRunningProceeds(t *testing.T) {
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")
	_, _ = setupHermeticSessions(t)
	writeSpecialistAgentsJSON(t, root, "specialist-agent", "test-specialist-formula")
	installNoopLaunchSession(t)

	agentDir := filepath.Join(root, ".agentfactory", "agents", "specialist-agent")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	t.Setenv("AF_ROLE", "caller-agent") // AuthorityAgent, but target is not running -> carve-out
	t.Setenv("TMUX", "")

	origReset := slingReset
	slingReset = true
	t.Cleanup(func() { slingReset = origReset })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	err := dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "test task")
	assertNotTeardownRefused(t, err)
}

// TestSlingReset_DispatchDaemonProceeds pins the dispatch-daemon carve-out: the af-dispatch daemon
// re-slings specialists it did not dispatch (unconditional --reset). It classifies AuthorityAgent
// by signal 2, but the gate admits it by its own session identity
// (CurrentSessionName() == DispatchSessionName()), so a running, non-provenanced target is not refused.
func TestSlingReset_DispatchDaemonProceeds(t *testing.T) {
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")
	fake, _ := setupHermeticSessions(t)
	writeSpecialistAgentsJSON(t, root, "specialist-agent", "test-specialist-formula")
	installNoopLaunchSession(t)

	agentDir := filepath.Join(root, ".agentfactory", "agents", "specialist-agent")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	// The daemon runs inside the af-dispatch tmux session: AuthorityAgent by signal 2, and the
	// carve-out identity.
	fake.currentSession = session.DispatchSessionName()
	fake.present[session.SessionName("specialist-agent")] = true // running target
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "1")

	origReset := slingReset
	slingReset = true
	t.Cleanup(func() { slingReset = origReset })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	err := dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "test task")
	assertNotTeardownRefused(t, err)
}

// TestSlingReset_OperatorContext_NoOp pins AC #7 (sling half): an operator (no AF_ROLE, no af-tmux
// identity) resetting a RUNNING target is NOT refused — the gate short-circuits before any probe,
// so the web console (which spawns this as an Operator) keeps working.
func TestSlingReset_OperatorContext_NoOp(t *testing.T) {
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")
	fake, _ := setupHermeticSessions(t)
	writeSpecialistAgentsJSON(t, root, "specialist-agent", "test-specialist-formula")
	installNoopLaunchSession(t)

	agentDir := filepath.Join(root, ".agentfactory", "agents", "specialist-agent")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	fake.present[session.SessionName("specialist-agent")] = true // running target
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")

	origReset := slingReset
	slingReset = true
	t.Cleanup(func() { slingReset = origReset })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	err := dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "test task")
	assertNotTeardownRefused(t, err)
}
