//go:build integration

package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// TestAgentsListIntegration_OwnInstancePerAgent is the ONLY executable end-to-end
// proof of issue #458 AC-1/AC-2 and the ONLY automated catcher of Gap-1. It runs
// the full `af agents list --json` caller against the REAL Python MCP store (the
// unit-lane tests run against memstore — the reference adapter, already correct,
// so they are green before AND after the fix and cannot reproduce the defect).
//
// It seeds two agents, "alice" and "bob", each with a DISTINCT non-terminal
// formula-instance whose ready child step is assigned to that agent, then runs
// the listing process with AF_ACTOR=alice (every real agent session exports
// AF_ACTOR — session.go:292/394 — and the operator/webui process that lists
// agents is frequently a DIFFERENT actor than the agent being listed). It then
// asserts each row's `formula` AND `step_id` equal THAT agent's OWN seeded
// instance — own-instance equality, NOT the weaker "rows differ" (which would
// false-pass a cross-actor row-swap, and false-pass when two agents legitimately
// run the same formula).
//
// bob's row is the Gap-1 catcher: with AF_ACTOR=alice, populateAgentStep's
// store.Ready call for bob's molecule must bypass the actor overlay
// (agents.go:170 IncludeAllAgents:true, the Phase-2 fix) or bob's bob-assigned
// step is hidden -> 0 ready -> step_state "blocked" with an empty step_id (the
// exact #458 symptom). Revert that flag and this test goes RED on bob's row;
// restore it and it passes (the AC #5 red->green, demonstrable in a deps-present
// checkout outside .agentfactory/worktrees/ — the integration suite's CI-only
// guard, tmuxisolation/ciguard.go, fails fast inside a live factory worktree).
//
// Requires the real store, so it lives in the integration lane (//go:build
// integration) and reuses the real-store helpers from integration_test.go
// (package cmd): requirePython3WithServerDeps, buildAF, ensurePySymlink,
// terminateMCPServer, runAF.
func TestAgentsListIntegration_OwnInstancePerAgent(t *testing.T) {
	requirePython3WithServerDeps(t)

	const (
		aliceFormula = "alice-formula"
		bobFormula   = "bob-formula"
	)

	binary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	// git init — the MCP server does not require git; left for factory parity with
	// TestE2EWorkflow / setupTerminationTest.
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@agents-list.test"},
		{"config", "user.name", "Agents List Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// af install --init scaffolds .agentfactory/{store,factory.json,agents.json,...}.
	runAF(t, binary, workspace, "install", "--init")

	// Two agents, each declaring its OWN distinct formula. Overwrites the default
	// agents.json (manager/supervisor) written by install --init.
	agentsJSON := `{"agents":{` +
		`"alice":{"type":"autonomous","description":"a","formula":"` + aliceFormula + `"},` +
		`"bob":{"type":"autonomous","description":"b","formula":"` + bobFormula + `"}}}`
	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	if err := os.WriteFile(agentsPath, []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}

	// Seed the REAL store directly. An empty-actor adapter performs unrestricted
	// cross-agent writes (mirrors integration_test.go:291's mcpstore.New(workspace,
	// "")). This adapter and the `af` subprocess below share one server/datastore
	// via .runtime/mcp_server.json rendezvous (proven by
	// TestAutoTermination_MailDeliveredBeforeKill).
	store, err := mcpstore.New(workspace, "")
	if err != nil {
		t.Fatalf("mcpstore.New(%s): %v", workspace, err)
	}
	ctx := context.Background()
	aliceStep := seedAgentFormula(ctx, t, store, "alice", aliceFormula, "Alice builds the thing")
	bobStep := seedAgentFormula(ctx, t, store, "bob", bobFormula, "Bob builds the thing")

	// Run the caller with AF_ACTOR=alice. runAF threads only HOME, so set AF_ACTOR
	// explicitly (Gotcha: the actor-overlay path Gap-1 lives on is only exercised
	// when AF_ACTOR is set to a running actor).
	out := runAFWithActor(t, binary, workspace, "alice", "agents", "list", "--json")
	byName := parseAgentsListByName(t, out)

	// OWN-INSTANCE EQUALITY (NOT "rows differ"). alice is the self-actor path; bob
	// is the cross-actor Gap-1 catcher.
	assertOwnInstance(t, byName, "alice", aliceFormula, aliceStep)
	assertOwnInstance(t, byName, "bob", bobFormula, bobStep)
}

// seedAgentFormula creates one agent's non-terminal formula-instance epic
// (Assignee=agent, Title "Formula: <formula>") plus a single dependency-free
// child step (Assignee=agent, labeled formula-step) so store.Ready surfaces it as
// the agent's ready step. Returns the created step. Adapts the memstore
// newInstanceEpic/newChildStep shape (up_phase2_test.go) to the real adapter.
func seedAgentFormula(ctx context.Context, t *testing.T, store issuestore.Store, agent, formula, stepTitle string) issuestore.Issue {
	t.Helper()
	inst, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula: " + formula,
		Labels:   []string{"formula-instance"},
		Assignee: agent,
	})
	if err != nil {
		t.Fatalf("seed %s instance: %v", agent, err)
	}
	step, err := store.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   inst.ID,
		Title:    stepTitle,
		Labels:   []string{"formula-step"},
		Assignee: agent,
	})
	if err != nil {
		t.Fatalf("seed %s step: %v", agent, err)
	}
	return step
}

// runAFWithActor runs the built `af` binary with both HOME and AF_ACTOR threaded
// into the child env. runAF (integration_test.go:99) only sets HOME, but the
// actor-overlay path #458 Gap-1 lives on requires AF_ACTOR to be set (the value
// every real agent session exports), so this controlled variant is needed.
func runAFWithActor(t *testing.T, binary, dir, actor string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+dir, "AF_ACTOR="+actor)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("af %s (AF_ACTOR=%s): %s\n%s", strings.Join(args, " "), actor, err, out)
	}
	return string(out)
}

// parseAgentsListByName parses `af agents list --json` output into a name->row
// map. The command emits the array on a single line via emitAgents; any other
// lines (e.g. subprocess logging captured by CombinedOutput) are ignored. A
// {"state":"error",...} envelope has no line starting with "[" and so fatals
// with the full output.
func parseAgentsListByName(t *testing.T, out string) map[string]agentListItem {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "[") {
			continue
		}
		var items []agentListItem
		if err := json.Unmarshal([]byte(line), &items); err != nil {
			continue
		}
		byName := make(map[string]agentListItem, len(items))
		for _, it := range items {
			byName[it.Name] = it
		}
		return byName
	}
	t.Fatalf("no JSON agent array in `af agents list --json` output:\n%s", out)
	return nil
}

// assertOwnInstance is the load-bearing own-instance-equality check: the named
// agent's row must report ITS OWN formula and ITS OWN ready step — never a shared
// global instance and never an empty/blocked step from a cross-actor overlay.
func assertOwnInstance(t *testing.T, byName map[string]agentListItem, agent, wantFormula string, ownStep issuestore.Issue) {
	t.Helper()
	row, ok := byName[agent]
	if !ok {
		t.Fatalf("%s missing from `af agents list` output (rows: %v)", agent, keysOfRows(byName))
	}
	// Formula column (Phase 1): this agent's own instance formula, not a global one.
	if row.Formula != wantFormula {
		t.Errorf("%s.formula = %q, want %q (own instance, not a shared/global one)", agent, row.Formula, wantFormula)
	}
	// Step columns (Phase 2 / Gap-1): this agent's own ready step. For a non-actor
	// agent (e.g. bob when AF_ACTOR=alice) the pre-fix Ready overlay hid the step ->
	// empty step_id / step_state "blocked"; equality to the OWN seeded step id
	// catches that regression where "rows differ" would not.
	if row.StepID != ownStep.ID {
		t.Errorf("%s.step_id = %q, want %q (own ready step); step_state=%q — a non-actor row blanked here is the #458 Gap-1 defect", agent, row.StepID, ownStep.ID, row.StepState)
	}
	if row.StepTitle != ownStep.Title {
		t.Errorf("%s.step_title = %q, want %q (own ready step)", agent, row.StepTitle, ownStep.Title)
	}
	if row.StepState != "ready" {
		t.Errorf("%s.step_state = %q, want \"ready\" (operator actor must not hide a non-actor agent's own ready step)", agent, row.StepState)
	}
}

// keysOfRows lists the agent names present in a parsed listing — used only to
// make a missing-row failure message actionable.
func keysOfRows(byName map[string]agentListItem) []string {
	names := make([]string, 0, len(byName))
	for n := range byName {
		names = append(names, n)
	}
	return names
}
