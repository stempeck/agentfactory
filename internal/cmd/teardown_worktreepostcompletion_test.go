package cmd

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/session"
)

// #548 Phase 3 — behavioral AC-1: a dispatcher may stop the specialist it dispatched AFTER the
// specialist's formula completed, authorized by the SURVIVING dispatch_owner. This is the
// end-to-end proof that complements the unit pin TestFormulaCaller_LifecycleUnchanged
// (dispatch_owner survives cleanupRuntimeArtifacts): here the survival is exercised through the
// real scoped-stop gate, in a worktree-shaped topology, with provenance built by the PRODUCTION
// write path — never a hand-seeded writeRuntimeFile (design AC-8).

// TestDown_DispatcherContext_AllowsDispatchedSpecialist_WorktreePostCompletion drives the AC-1
// lifecycle end to end:
//
//  1. a worktree-shaped factory whose target "solver" is a formula-backed specialist living in
//     the worktree agent dir;
//  2. provenance minted by the REAL writeDispatchOwner (sling.go — the helper the af sling
//     dispatch path calls), NOT writeRuntimeFile;
//  3. the REAL cleanupRuntimeArtifacts (done.go — formula-completion cleanup) which deletes
//     formula_caller but LEAVES dispatch_owner;
//  4. a scoped `af down <solver>` run from inside the worktree as the non-interactive dispatcher
//     "planner".
//
// The stop must be authorized off the surviving dispatch_owner (dispatcher tier), not refused.
// The topology is load-bearing: runDown resolves the roster from the MAIN root (via the
// .factory-root redirect) while callerDispatched reads provenance from the WORKTREE agent dir —
// they only line up when cwd is inside the worktree, which is exactly the post-completion shape
// this test proves. A non-interactive dispatcher identity is used so a broken provenance read
// cannot pass vacuously through the manager tier.
func TestDown_DispatcherContext_AllowsDispatchedSpecialist_WorktreePostCompletion(t *testing.T) {
	factoryRoot, wtAgentDir := setupWorktreeFixture(t, "solver")

	// The fixture's default roster gives the target no formula (isSpecialistTarget would be
	// false, refusing the dispatcher tier). Overwrite the MAIN roster so the target is a
	// formula-backed specialist and a distinct, NON-interactive dispatcher identity exists.
	writeAFFile(t, factoryRoot, "agents.json", `{"agents":{
  "planner":{"type":"autonomous","description":"dispatcher","formula":"factoryworker"},
  "solver":{"type":"autonomous","description":"target","formula":"factoryworker"}}}`)

	fake, _ := setupHermeticSessions(t)

	// cwd inside the worktree makes callerDispatched read provenance from the worktree agent dir
	// (where the real write landed). The caller resolves to AF_ROLE="planner": the worktree has no
	// agents.json, so resolveAgentName's membership gate rejects the path-derived "solver" and
	// falls back to AF_ROLE — the dispatcher, distinct from the target.
	t.Chdir(wtAgentDir)
	t.Setenv("AF_ROLE", "planner")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("solver")] = true

	// REAL dispatch write + REAL completion cleanup (no hand-seeded provenance).
	writeDispatchOwner(wtAgentDir, "planner")
	cleanupRuntimeArtifacts(wtAgentDir)

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := runDown(cmd, []string{"solver"})
	if err != nil && strings.Contains(err.Error(), "teardown refused") {
		t.Fatalf("dispatcher stopping its dispatched specialist post-completion must not refuse; got %v", err)
	}
	// "HasSession is false" == the session was torn down. fakeTmux.KillSession only records (it
	// never clears present), and Manager.Stop early-returns unless present is seeded, so the
	// recorded KillSession op is the real "session is gone" proof (the :138/:166 idiom).
	if !opRecorded(fake.ops, "KillSession "+session.SessionName("solver")) {
		t.Errorf("scoped af down must stop the dispatched specialist off the surviving dispatch_owner; ops=%v", fake.ops)
	}
}
