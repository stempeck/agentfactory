//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// This file reproduces issue #709's faults 1 and 2 and MUST compile and run red on base commit
// 7141bc21, whose grader reads the attestation from .runtime/effort_level, and pass once the launch
// line is the attestation's only carrier. It therefore names no symbol that exists on only one side:
// it drives the real launch legs and reads what a launched session's own environment would carry.
//
// Not parallel: setupHermeticSessions swaps package-global seams, and none of the helpers here use
// t.Parallel.
// TestEffortAttestationSurvivesFormulaCleanup reproduces fault 1: `af done`'s formula-completion
// cleanup (cleanupRuntimeArtifacts) deletes the .runtime/effort_level file while the session it describes
// keeps running, so the grader is told nothing about a session that is still executing under a
// reduced reasoning effort.
func TestEffortAttestationSurvivesFormulaCleanup(t *testing.T) {
	fx, epic, step := primedFixture(t, roomyOccupancyPct)
	writeDeclaredEffortProfile(t, fx.root)
	armEfficiency(t, fx.root, nil)
	model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
	seedEfficiency(t, fx.root, "offpath", stepLabelOf(step), model, reducibleAggregate())

	line := respawnLaunchLine(t, fx.root, fx.workDir)
	launchedAttestation(t, line)

	since := time.Now().Add(-time.Hour).Format(time.RFC3339)
	before := turnInterventionsFrom(t, fx.root, fx.workDir, fx.agent, since)
	if before == "" {
		t.Fatalf("fixture did not establish a reduction before cleanup; nothing to protect. line=%q", line)
	}

	cleanupRuntimeArtifacts(fx.workDir)
	if err := sendWorkDoneAndCleanup(t.Context(), fx.mem, fx.workDir, fx.root, epic.ID, false); err != nil {
		t.Fatalf("sendWorkDoneAndCleanup: %v", err)
	}

	after := turnInterventionsFrom(t, fx.root, fx.workDir, fx.agent, since)

	if before != after {
		t.Fatalf("FAULT 1: af done's formula-completion cleanup changed what the grader sees for a "+
			"still-running session:\nbefore=%q\nafter=%q", before, after)
	}
	const want = "- effort: reduce_effort — the harness started this session at reduced reasoning effort, " +
		"which holds for every turn of the session (effort_level=medium)\n"
	if after != want {
		t.Errorf("after cleanup = %q, want %q", after, want)
	}
	if _, err := os.Stat(filepath.Join(fx.workDir, ".runtime", "effort_level")); !os.IsNotExist(err) {
		t.Errorf(".runtime/effort_level exists after cleanup (stat err=%v); the launch line must be the "+
			"only carrier of the attestation", err)
	}
}

// TestUpAgainstRunningAgentLeavesGraderUnchanged reproduces fault 2: `af up` against an
// already-running agent resolves the next step and runs withEffortLevel BEFORE mgr.Start() returns
// ErrAlreadyRunning, so a launch leg that selects nothing (or is itself off) rewrites or clears the
// running session's attestation without ever relaunching it.
func TestUpAgainstRunningAgentLeavesGraderUnchanged(t *testing.T) {
	const wtID = "wt-709fault2"
	root := setupTestFactoryForDone(t, "manager")
	initTestGitRepo(t, root)
	writeDeclaredEffortProfile(t, root)
	wtRel := filepath.Join(".agentfactory", "worktrees", wtID)
	wtPath := filepath.Join(root, wtRel)
	wtAgentDir := config.AgentDir(wtPath, "manager")
	if err := os.MkdirAll(wtAgentDir, 0o755); err != nil {
		t.Fatalf("provision worktree agent dir: %v", err)
	}
	// af up drops a non-specialist caller's AF_WORKTREE (#188) and resolves the agent's worktree by
	// owner, so without this sidecar it would create a fresh worktree and launch into a directory the
	// grader never reads.
	if err := worktree.WriteMeta(root, &worktree.Meta{
		ID: wtID, Owner: "manager", Branch: "af/manager-709fault2", Path: wtRel, Agents: []string{"manager"},
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	// resolveAgentName (selectEffortLevel's first step) needs a local root marker to detect "manager"
	// from a path nested under the worktree rather than under the factory root's own agents/ dir.
	if err := os.WriteFile(filepath.Join(wtPath, ".agentfactory", ".factory-root"), []byte(root+"\n"), 0o644); err != nil {
		t.Fatalf("write .factory-root: %v", err)
	}
	t.Setenv("AF_WORKTREE", wtPath)
	t.Setenv("AF_WORKTREE_ID", wtID)
	t.Chdir(root)
	fake, mem := setupHermeticSessions(t)

	// nextReadyStep (driven by the real launch legs) resolves the label from the open step and the
	// formula from the epic title (#679 F3), since no last_closed_step exists yet.
	epic, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title: "Formula: offpath", Type: issuestore.TypeEpic,
		Labels: []string{"formula-instance"}, Assignee: "manager",
	})
	if err != nil {
		t.Fatalf("seed epic: %v", err)
	}
	if _, err := mem.Create(t.Context(), issuestore.CreateParams{
		Title: "Step 1", Parent: epic.ID, Type: issuestore.TypeTask,
		Labels: []string{"formula-step", stepIDLabelPrefix + "step-1"}, Assignee: "manager", Description: "First",
	}); err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(wtAgentDir, ".runtime"), 0o755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtAgentDir, ".runtime", "hooked_formula"), []byte(epic.ID), 0o644); err != nil {
		t.Fatalf("write hooked_formula: %v", err)
	}

	armEfficiency(t, root, nil)
	model, _ := resolveRecordModel(root, wtAgentDir, "manager", "")
	seedEfficiency(t, root, "offpath", "step-1", model, reducibleAggregate())

	// An env-only plant would read "" before and after on the base tree, whose grader reads a file
	// only a real withEffortLevel call writes — the test would pass there for the wrong reason.
	line := respawnLaunchLine(t, root, wtAgentDir)
	launchedAttestation(t, line)

	// Only the effort arm goes off: that is the condition under which every non-selecting exit of
	// withEffortLevel clears.
	armAdvisoryPolicy(t, root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "off"})

	sessionName := session.SessionName("manager")
	fake.present[sessionName] = true
	fake.claudeRunning[sessionName] = true

	since := time.Now().Add(-time.Hour).Format(time.RFC3339)
	before := turnInterventionsFrom(t, root, wtAgentDir, "manager", since)
	if before == "" {
		t.Fatalf("fixture did not establish a reduction before af up; nothing to protect. line=%q", line)
	}

	upAgainstRunning := func(t *testing.T) {
		t.Helper()
		opsBefore := len(fake.ops)
		cmd, out := launchCmd(t)
		if err := runUp(cmd, []string{"manager"}); err != nil {
			t.Fatalf("af up: %v\n%s", err, out)
		}
		if got := out.String(); !strings.Contains(got, sessionName+": already running") {
			t.Fatalf("af up output = %q, want it to report %q already running", got, sessionName)
		}
		for _, op := range fake.ops[opsBefore:] {
			for _, verb := range []string{"NewSession ", "SetEnvironment ", "UnsetEnvironment ", "SendKeysDelayed "} {
				if strings.HasPrefix(op, verb+sessionName) {
					t.Errorf("af up against a running agent issued a session-mutating op: %q", op)
				}
			}
		}
	}

	upAgainstRunning(t)
	after := turnInterventionsFrom(t, root, wtAgentDir, "manager", since)
	if before != after {
		t.Errorf("FAULT 2: af up against an already-running agent changed what the grader sees:\n"+
			"before=%q\nafter=%q", before, after)
	}

	t.Run("would-select", func(t *testing.T) {
		// The running session was launched with nothing selected; a launch leg that WOULD select a
		// level must still not attest it.
		launchedAttestation(t, respawnLaunchLine(t, root, wtAgentDir))
		armEfficiency(t, root, nil)

		before := turnInterventionsFrom(t, root, wtAgentDir, "manager", since)
		if before != "" {
			t.Fatalf("fixture did not establish an unreduced running session: %q", before)
		}

		upAgainstRunning(t)

		after := turnInterventionsFrom(t, root, wtAgentDir, "manager", since)
		if after != "" {
			t.Errorf("FAULT 2: af up against a running agent attested a reduction it never launched: %q", after)
		}
	})

	t.Run("zombie", func(t *testing.T) {
		fake.claudeRunning[sessionName] = false
		opsBefore := len(fake.ops)

		cmd, out := launchCmd(t)
		if err := runUp(cmd, []string{"manager"}); err != nil {
			t.Fatalf("af up: %v\n%s", err, out)
		}

		var sawKill, sawNew bool
		var launchOp string
		for _, op := range fake.ops[opsBefore:] {
			switch {
			case op == "KillSession "+sessionName:
				sawKill = true
			case strings.HasPrefix(op, "NewSession "+sessionName):
				sawNew = true
			case strings.HasPrefix(op, "SendKeysDelayed "+sessionName):
				launchOp = op
			}
		}
		if !sawKill {
			t.Errorf("a zombie session (tmux alive, claude dead) was not killed: ops=%v", fake.ops)
		}
		if !sawNew {
			t.Errorf("a zombie session was killed but never recreated: ops=%v", fake.ops)
		}
		if launchOp == "" {
			t.Fatalf("no launch was sent to the recreated session: ops=%v", fake.ops)
		}
		if !strings.Contains(launchOp, "AF_EFFORT_OBJECTIVE=") {
			t.Errorf("the recreated session's launch line does not carry AF_EFFORT_OBJECTIVE: %s", launchOp)
		}
	})
}
