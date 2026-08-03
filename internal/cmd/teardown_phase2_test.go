package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// Issue #548 Phase 2 — the two stop-capable surfaces (`af down` per-target check and
// `af sling --agent X --reset`) are wired through the Phase-1 scopedStopAllowed decision
// function. These tests pin the manager tier on `af down`, the granted-stop audit breadcrumb,
// the operator no-op parity, and the P6 worktree-cleanup repro. The sling-reset gate is pinned
// in sling_reset_gate_test.go.

// setupControlPlaneFactory writes a factory root with a richer roster than
// setupDownGateFactory: two interactive agents, a non-formula supervisor, and a formula-backed
// autonomous specialist. Used by the manager-tier control-plane refusal test (C-4).
func setupControlPlaneFactory(t *testing.T) (string, *fakeTmux) {
	t.Helper()
	root := t.TempDir()
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{
  "manager":{"type":"interactive","description":"m"},
  "manager2":{"type":"interactive","description":"m2"},
  "supervisor":{"type":"autonomous","description":"sup"},
  "solver":{"type":"autonomous","description":"s","formula":"factoryworker"}}}`)
	t.Chdir(root)
	fake, _ := setupHermeticSessions(t)
	return root, fake
}

// TestDown_ManagerContext_AllowsNonDispatchedWorker is the AC-2 core: an interactive manager may
// stop an autonomous, formula-backed specialist it did NOT dispatch (no provenance datum), via the
// manager tier of scopedStopAllowed. Before Phase 2 the down surface only inlined self+dispatcher,
// so this stop would have been refused.
func TestDown_ManagerContext_AllowsNonDispatchedWorker(t *testing.T) {
	_, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("solver")] = true

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := runDown(cmd, []string{"solver"})
	if err != nil && strings.Contains(err.Error(), "teardown refused") {
		t.Fatalf("manager must be allowed to stop a non-dispatched autonomous worker; got %v", err)
	}
	if !opRecorded(fake.ops, "KillSession "+session.SessionName("solver")) {
		t.Errorf("manager tier must stop the non-dispatched worker; ops=%v", fake.ops)
	}
}

// TestDown_ManagerContext_ScopedResetAllowed pins the post-#553 down/sling alignment: a granted
// tier covers `--reset` on its targets, because the identical stop + state-reclamation authority
// was already tier-granted via `af sling --agent <target> --reset` (sling_reset_gate_test.go) —
// refusing the down spelling only forced the sling one. The manager resetting a running worker it
// did not dispatch must not be refused; the target is stopped and its runtime state reclaimed.
func TestDown_ManagerContext_ScopedResetAllowed(t *testing.T) {
	root, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("solver")] = true
	writeRuntimeFile(t, config.AgentDir(root, "solver"), "dispatch_owner", "someone-else")

	origStore := newIssueStore
	newIssueStore = func(wd, actor string) (issuestore.Store, error) {
		return nil, errors.New("hermetic: no issue store")
	}
	t.Cleanup(func() { newIssueStore = origStore })

	downReset = true
	t.Cleanup(func() { downReset = false })

	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)

	err := runDown(cmd, []string{"solver"})
	if err != nil && strings.Contains(err.Error(), "teardown refused") {
		t.Fatalf("tier-granted scoped --reset must not refuse; got %v", err)
	}
	if !opRecorded(fake.ops, "KillSession "+session.SessionName("solver")) {
		t.Errorf("scoped --reset must stop the target; ops=%v", fake.ops)
	}
	if _, statErr := os.Stat(filepath.Join(config.AgentDir(root, "solver"), ".runtime")); !os.IsNotExist(statErr) {
		t.Errorf("scoped --reset must reclaim the target's runtime state; stat err=%v", statErr)
	}
}

// TestDown_SiblingContext_ScopedResetStillRefused pins that dropping the --reset disqualifier
// from downSelfScoped did not widen WHO may reset: a non-dispatching sibling targeting a running
// worker is refused (no tier grants it), with the --reset surface in the refusal and zero ops.
func TestDown_SiblingContext_ScopedResetStillRefused(t *testing.T) {
	_, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "sibling")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("solver")] = true

	downReset = true
	t.Cleanup(func() { downReset = false })

	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)

	err := runDown(cmd, []string{"solver"})
	assertTeardownRefused(t, err, "af down --reset")
	if opRecorded(fake.ops, "KillSession") {
		t.Errorf("refused sibling --reset must record zero KillSession; ops=%v", fake.ops)
	}
}

// TestDown_ManagerContext_RefusesBareAllReset pins AC-4: the manager tier cannot leak into a
// factory-wide shape. bare / --all / no-target --reset from an interactive manager must still
// refuse with K1 and record zero KillSession — the --all/no-args disqualifiers run BEFORE any
// per-target tier logic (the no-target --reset shape is caught by no-args).
func TestDown_ManagerContext_RefusesBareAllReset(t *testing.T) {
	_, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	for _, name := range []string{"manager", "solver", "sibling"} {
		fake.present[session.SessionName(name)] = true
	}
	fake.present[session.DispatchSessionName()] = true
	fake.present[session.WatchdogSessionName()] = true

	cases := []struct {
		name        string
		all, reset  bool
		wantSurface string
	}{
		{"bare", false, false, "af down"},
		{"all", true, false, "af down"},
		{"reset", false, true, "af down --reset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			downAll, downReset = tc.all, tc.reset
			t.Cleanup(func() { downAll, downReset = false, false })
			fake.ops = nil

			cmd := &cobra.Command{}
			var out, errb bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errb)

			err := runDown(cmd, nil)
			assertTeardownRefused(t, err, tc.wantSurface)
			if opRecorded(fake.ops, "KillSession") {
				t.Errorf("refused %s must record zero KillSession; ops=%v", tc.name, fake.ops)
			}
		})
	}
}

// TestDown_ManagerContext_RefusesControlPlaneTargets pins the C-4 decision: the manager tier only
// reaches autonomous, formula-backed specialists. A second interactive agent, a non-formula
// supervisor, and a watchdog/dispatcher name (not agents.json entries) must all refuse with K1.
func TestDown_ManagerContext_RefusesControlPlaneTargets(t *testing.T) {
	_, fake := setupControlPlaneFactory(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	for _, s := range []string{
		session.SessionName("manager2"),
		session.SessionName("supervisor"),
		session.WatchdogSessionName(),
		session.DispatchSessionName(),
	} {
		fake.present[s] = true
	}

	for _, target := range []string{"manager2", "supervisor", "watchdog", "dispatch"} {
		t.Run(target, func(t *testing.T) {
			fake.ops = nil
			cmd := &cobra.Command{}
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)

			err := runDown(cmd, []string{target})
			assertTeardownRefused(t, err, "af down")
			if opRecorded(fake.ops, "KillSession") {
				t.Errorf("manager stopping control-plane %q must record zero KillSession; ops=%v", target, fake.ops)
			}
		})
	}
}

// TestDown_ScopedStop_WritesGrantedArtifact pins Gap 7 / C-5: on the allow path of an agent-context
// scoped stop, exactly one line is appended to the CALLER's <agentDir>/.runtime/teardown_granted,
// recording the granting tier and target. Uses the dispatcher tier (solver dispatched by manager)
// so the recorded tier is deterministic.
func TestDown_ScopedStop_WritesGrantedArtifact(t *testing.T) {
	root, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("solver")] = true
	writeRuntimeFile(t, config.AgentDir(root, "solver"), "dispatch_owner", "manager")

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runDown(cmd, []string{"solver"}); err != nil && strings.Contains(err.Error(), "teardown refused") {
		t.Fatalf("granted scoped stop must not refuse; got %v", err)
	}

	granted := filepath.Join(config.AgentDir(root, "manager"), ".runtime", "teardown_granted")
	data, err := os.ReadFile(granted)
	if err != nil {
		t.Fatalf("expected teardown_granted breadcrumb at %s: %v", granted, err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly one granted line, got %d: %q", len(lines), string(data))
	}
	if !strings.Contains(lines[0], "tier=dispatcher") {
		t.Errorf("granted line must record the granting tier; got %q", lines[0])
	}
	if !strings.Contains(lines[0], "target=solver") {
		t.Errorf("granted line must record the target; got %q", lines[0])
	}
}

// TestDown_OperatorContext_NoOp pins AC #7 (down half): an operator (no AF_ROLE, no af-tmux
// identity) is unaffected by the new gate delegation and granted-audit — the target is stopped and
// NO teardown_granted breadcrumb is written (the granted write is agent-context-only).
func TestDown_OperatorContext_NoOp(t *testing.T) {
	root, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("solver")] = true

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runDown(cmd, []string{"solver"}); err != nil && strings.Contains(err.Error(), "teardown refused") {
		t.Fatalf("operator af down must not refuse; got %v", err)
	}
	if !opRecorded(fake.ops, "KillSession "+session.SessionName("solver")) {
		t.Errorf("operator af down must stop the target; ops=%v", fake.ops)
	}
	for _, name := range []string{"manager", "solver", "sibling"} {
		granted := filepath.Join(config.AgentDir(root, name), ".runtime", "teardown_granted")
		if _, err := os.Stat(granted); err == nil {
			t.Errorf("operator path must write no teardown_granted; found one at %s", granted)
		}
	}
}

// TestDown_WorktreeCaller_DeregistersTargetWorktreeAtMainRoot is the P6 repro (write FIRST). It
// asserts that a scoped stop from a worktree-resident caller deregisters the target's worktree at
// the MAIN root — because runDown resolves the root via resolveInvokerRoot -> FindFactoryRoot,
// which follows the .factory-root redirect to the main root. Expected GREEN with NO
// cleanupAgentWorktree change => P6 is a phantom and is de-scoped.
func TestDown_WorktreeCaller_DeregistersTargetWorktreeAtMainRoot(t *testing.T) {
	mainRoot := t.TempDir()
	writeAFFile(t, mainRoot, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, mainRoot, "agents.json", `{"agents":{
  "solver":{"type":"autonomous","description":"s","formula":"factoryworker"}}}`)

	// Register the target's worktree at the MAIN root (no real git worktree needed — FindByOwner
	// reads the meta). The worktree dir carries a .factory-root redirect so a caller resident in it
	// resolves back to the main root, exactly as a live worktree does.
	wtRel := filepath.Join(".agentfactory", "worktrees", "wt-solver01")
	wtPath := filepath.Join(mainRoot, wtRel)
	if err := os.MkdirAll(wtPath, 0o755); err != nil {
		t.Fatalf("mkdir worktree: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, ".factory-root"), []byte(mainRoot+"\n"), 0o644); err != nil {
		t.Fatalf("write .factory-root redirect: %v", err)
	}
	if err := os.MkdirAll(worktree.WorktreesDir(mainRoot), 0o755); err != nil {
		t.Fatalf("mkdir worktrees: %v", err)
	}
	meta := &worktree.Meta{
		ID:     "wt-solver01",
		Owner:  "solver",
		Branch: "af/solver-01",
		Path:   wtRel,
		Agents: []string{"solver"},
	}
	if err := worktree.WriteMeta(mainRoot, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	t.Chdir(wtPath)
	fake, _ := setupHermeticSessions(t)
	_ = fake // solver is not running -> the not-running cleanup branch runs

	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	if err := runDown(cmd, []string{"solver"}); err != nil {
		t.Fatalf("af down solver from a worktree caller must succeed; got %v", err)
	}

	// No leak means the cleanup ran against the MAIN root and DEREGISTERED the target: either the
	// meta is fully gone, or it survives with solver removed from meta.Agents. A leak (cleanup run
	// against the empty worktree-local registry) would leave meta.Agents == [solver] untouched.
	meta, err := worktree.ReadMeta(mainRoot, "wt-solver01")
	if err != nil {
		return // meta fully removed — deregistered, no leak
	}
	for _, a := range meta.Agents {
		if a == "solver" {
			t.Errorf("P6 leak: solver still registered in worktree meta at the MAIN root: %+v", meta)
		}
	}
}
