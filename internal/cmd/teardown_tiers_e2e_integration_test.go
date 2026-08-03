//go:build integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestE2E_Teardown_Tiers drives the #548 scoped-stop tiers through the REAL af binary and real
// tmux, end to end:
//
//   - AC-1 / Gap A: an autonomous dispatcher stops the specialist it dispatched. Provenance is
//     minted by the production dispatch write (af sling --agent <target> --caller <dispatcher>,
//     which calls writeDispatchOwner) — never hand-seeded.
//   - AC-2 / Gap B: an interactive manager stops an autonomous specialist it did NOT dispatch,
//     authorized by the manager tier.
//
// This test runs only under `make test-integration` (`-tags=integration`) and is excluded from
// the default `make test` by the build constraint. It runs from the MAIN factory root; it does
// NOT run inside a worktree, because GuardCIOnly (installed by the integration TestMain in
// main_integration_test.go) refuses when the cwd is under /.agentfactory/worktrees/. The
// worktree read-path robustness of the authority gate is proved by the hermetic unit test
// TestDown_DispatcherContext_AllowsDispatchedSpecialist_WorktreePostCompletion, not here.
func TestE2E_Teardown_Tiers(t *testing.T) {
	requirePython3WithServerDeps(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	binary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", args, err, out)
		}
	}

	// Initial commit (required before af sling dispatches a specialist: worktree creation detects
	// the parent branch via `git rev-parse --abbrev-ref HEAD` (worktree.go), which exits 128 on an
	// unborn HEAD, so a repo with no commits cannot create a worktree).
	if err := os.WriteFile(filepath.Join(workspace, ".gitignore"), []byte(".agentfactory/\n"), 0o644); err != nil {
		t.Fatalf("write .gitignore: %v", err)
	}
	for _, args := range [][]string{
		{"add", ".gitignore"},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", args, err, out)
		}
	}

	runAF(t, binary, workspace, "install", "--init")

	// Interactive manager (reaches the manager tier) + two autonomous, formula-backed
	// specialists: "worker" is dispatched by "dispatcher" (Gap A), "orphan" is dispatched by
	// nobody (Gap B). "dispatcher" is itself an autonomous specialist so the dispatcher tier,
	// not the manager tier, authorizes Gap A.
	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	agentsJSON := `{"agents":{` +
		`"manager":{"type":"interactive","description":"manager"},` +
		`"dispatcher":{"type":"autonomous","description":"dispatcher","formula":"factoryworker"},` +
		`"worker":{"type":"autonomous","description":"worker","formula":"factoryworker"},` +
		`"orphan":{"type":"autonomous","description":"orphan","formula":"factoryworker"}}}`
	if err := os.WriteFile(agentsPath, []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("writing agents.json: %v", err)
	}

	agentDir := func(name string) string {
		d := filepath.Join(workspace, ".agentfactory", "agents", name)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatalf("mkdir agent dir %s: %v", name, err)
		}
		return d
	}
	newSession := func(name, dir string) string {
		s := "af-" + name
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", s, "-c", dir).CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session %s: %s\n%s", s, err, out)
		}
		t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", s).Run() })
		return s
	}
	assertSessionGone := func(session, label string) {
		if err := exec.Command("tmux", "has-session", "-t", "="+session).Run(); err == nil {
			t.Fatalf("%s: session %s should have been killed but is still alive", label, session)
		}
	}

	dispatcherDir := agentDir("dispatcher")
	managerDir := agentDir("manager")
	workerDir := agentDir("worker")
	orphanDir := agentDir("orphan")

	// ---- AC-1 / Gap A: dispatcher stops the specialist it dispatched -------------------------
	workerSession := newSession("worker", workerDir)
	// REAL provenance: the production dispatch write records dispatch_owner[worker]="dispatcher".
	runAF(t, binary, dispatcherDir, "sling", "--agent", "worker", "--caller", "dispatcher", "--no-launch", "task")
	if out, err := runAFAsRole(t, binary, dispatcherDir, "dispatcher", "down", "worker"); err != nil {
		t.Fatalf("Gap A: dispatcher stopping its own dispatched specialist must not refuse; got %v\n%s", err, out)
	}
	assertSessionGone(workerSession, "Gap A")

	// ---- AC-2 / Gap B: interactive manager stops a NON-dispatched specialist -----------------
	orphanSession := newSession("orphan", orphanDir)
	if out, err := runAFAsRole(t, binary, managerDir, "manager", "down", "orphan"); err != nil {
		t.Fatalf("Gap B: interactive manager stopping a non-dispatched specialist must not refuse; got %v\n%s", err, out)
	}
	assertSessionGone(orphanSession, "Gap B")
}

// runAFAsRole runs the af binary with an explicit AF_ROLE so the authority gate classifies the
// caller as an agent (not an operator). The shared runAF/runAFMayFail helpers set only HOME, so
// af down would otherwise see AuthorityOperator and bypass the tier checks — the tiers would then
// pass vacuously. cwd is the caller's own agent dir, because the caller identity is path-derived
// and validated against AF_ROLE.
func runAFAsRole(t *testing.T, binary, dir, role string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+dir, "AF_ROLE="+role)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
