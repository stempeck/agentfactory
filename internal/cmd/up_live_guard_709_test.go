//go:build !integration

package cmd

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tokenomics"
	"github.com/stempeck/agentfactory/internal/worktree"
)

const liveGuardBackend = "http://127.0.0.1:1234"

type liveAgentFixture struct {
	root       string
	wtPath     string
	wtAgentDir string
	epicID     string
	sess       string
	fake       *fakeTmux
}

// newLiveAgentFixture provisions manager in an owned worktree with an in-flight formula, so af up
// resolves the same worktree (Reattached) and reconstructHookedFormula has an epic to rebind.
func newLiveAgentFixture(t *testing.T) liveAgentFixture {
	t.Helper()
	const wtID = "wt-709live"
	root := setupTestFactoryForDone(t, "manager")
	initTestGitRepo(t, root)
	wtRel := filepath.Join(".agentfactory", "worktrees", wtID)
	wtPath := filepath.Join(root, wtRel)
	wtAgentDir := config.AgentDir(wtPath, "manager")
	if err := os.MkdirAll(wtAgentDir, 0o755); err != nil {
		t.Fatalf("provision worktree agent dir: %v", err)
	}
	if err := worktree.WriteMeta(root, &worktree.Meta{
		ID: wtID, Owner: "manager", Branch: "af/manager-709live", Path: wtRel, Agents: []string{"manager"},
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	if err := os.WriteFile(filepath.Join(wtPath, ".agentfactory", ".factory-root"), []byte(root+"\n"), 0o644); err != nil {
		t.Fatalf("write .factory-root: %v", err)
	}
	t.Setenv("AF_WORKTREE", wtPath)
	t.Setenv("AF_WORKTREE_ID", wtID)
	t.Chdir(root)
	fake, mem := setupHermeticSessions(t)

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

	return liveAgentFixture{
		root: root, wtPath: wtPath, wtAgentDir: wtAgentDir, epicID: epic.ID,
		sess: session.SessionName("manager"), fake: fake,
	}
}

// seedRelaunchState plants every .runtime file af up's relaunch cleanup and formula recovery
// would touch, at both agent dirs the cleanup reaches.
func (fx liveAgentFixture) seedRelaunchState(t *testing.T) {
	t.Helper()
	for _, dir := range []string{config.AgentDir(fx.root, "manager"), fx.wtAgentDir} {
		writeRuntimeFile(t, dir, "dispatched", "1\n")
		writeRuntimeFile(t, dir, "dispatch_owner", "manager\n")
		writeLastRefusal(dir, liveGuardBackend,
			tokenomics.BackendVerdict{Verdict: tokenomics.VerdictNoFit, PoolTokens: 400000, SummedTokens: 380000}, time.Now())
		slotDir := reservationDir(dir, liveGuardBackend)
		if err := os.MkdirAll(slotDir, 0o755); err != nil {
			t.Fatalf("mkdir reservation dir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(slotDir, sequentialSlotName), []byte("held\n"), 0o644); err != nil {
			t.Fatalf("write reservation slot: %v", err)
		}
	}
	writeRuntimeFile(t, fx.wtAgentDir, "hooked_formula", fx.epicID)
	// SetupAgent rewrites worktree_id before the guard runs; seeding the bytes it writes keeps the
	// comparison about what af up's relaunch path does, not about SetupAgent.
	writeRuntimeFile(t, fx.wtAgentDir, "worktree_id", filepath.Base(fx.wtPath)+"\n")
}

func (fx liveAgentFixture) runtimeDirs() []string {
	return []string{
		filepath.Join(config.AgentDir(fx.root, "manager"), ".runtime"),
		filepath.Join(fx.wtAgentDir, ".runtime"),
	}
}

// snapshotRuntime maps every path under dirs to its bytes (directories to a sentinel), so a
// removed file, an emptied reservation dir, or a rewritten file all show up as a difference.
func snapshotRuntime(t *testing.T, dirs []string) map[string]string {
	t.Helper()
	snap := map[string]string{}
	for _, dir := range dirs {
		err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				snap[path] = "<dir>"
				return nil
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			snap[path] = string(b)
			return nil
		})
		if err != nil {
			t.Fatalf("snapshot %s: %v", dir, err)
		}
	}
	return snap
}

func diffSnapshots(before, after map[string]string) []string {
	var diffs []string
	for path, was := range before {
		now, ok := after[path]
		switch {
		case !ok:
			diffs = append(diffs, "removed: "+path)
		case now != was:
			diffs = append(diffs, "changed: "+path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			diffs = append(diffs, "added: "+path)
		}
	}
	return diffs
}

func (fx liveAgentFixture) lastRun(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(fx.root, ".runtime", "af_up_last_run"))
	if err != nil {
		t.Fatalf("read af_up_last_run: %v", err)
	}
	return string(b)
}

// TestUpLeavesLiveAgentRuntimeUntouched pins that `af up` against a live agent changes nothing in
// its .runtime/ — the dispatch markers, refusal breadcrumb, reservation ledger and formula pointer
// are the running session's state, not a previous session's leftovers.
func TestUpLeavesLiveAgentRuntimeUntouched(t *testing.T) {
	fx := newLiveAgentFixture(t)
	fx.seedRelaunchState(t)
	fx.fake.present[fx.sess] = true
	fx.fake.claudeRunning[fx.sess] = true

	before := snapshotRuntime(t, fx.runtimeDirs())
	cmd, out := launchCmd(t)
	if err := runUp(cmd, []string{"manager"}); err != nil {
		t.Fatalf("af up: %v\n%s", err, out)
	}

	if n := strings.Count(out.String(), fx.sess+": already running\n"); n != 1 {
		t.Errorf("af up printed %q %d times, want exactly once; output=%q", fx.sess+": already running", n, out.String())
	}
	if strings.Contains(out.String(), "--model") {
		t.Errorf("af up without --model printed a --model note: %q", out.String())
	}
	if diffs := diffSnapshots(before, snapshotRuntime(t, fx.runtimeDirs())); len(diffs) > 0 {
		t.Errorf("af up against a live agent mutated its .runtime/:\n  %s", strings.Join(diffs, "\n  "))
	}
	for _, op := range fx.fake.ops {
		for _, verb := range []string{"NewSession ", "SetEnvironment ", "UnsetEnvironment ", "SendKeysDelayed ", "KillSession "} {
			if strings.HasPrefix(op, verb+fx.sess) {
				t.Errorf("af up against a live agent issued a session-mutating op: %q", op)
			}
		}
	}
	lastRun := fx.lastRun(t)
	if !strings.Contains(lastRun, "agent manager: outcome=") || !strings.Contains(lastRun, " recovered=false") {
		t.Errorf("af_up_last_run does not record the live agent with recovered=false:\n%s", lastRun)
	}
	if strings.Contains(lastRun, "(no agents resolved a worktree this run)") {
		t.Errorf("af_up_last_run dropped the live agent:\n%s", lastRun)
	}

	t.Run("absent pointer is not rebound", func(t *testing.T) {
		fx := newLiveAgentFixture(t)
		fx.seedRelaunchState(t)
		pointer := filepath.Join(fx.wtAgentDir, ".runtime", "hooked_formula")
		if err := os.Remove(pointer); err != nil {
			t.Fatalf("remove hooked_formula: %v", err)
		}
		fx.fake.present[fx.sess] = true
		fx.fake.claudeRunning[fx.sess] = true

		cmd, out := launchCmd(t)
		if err := runUp(cmd, []string{"manager"}); err != nil {
			t.Fatalf("af up: %v\n%s", err, out)
		}
		if _, err := os.Stat(pointer); !os.IsNotExist(err) {
			t.Errorf("af up rebound a live agent's formula pointer (stat err=%v)", err)
		}
		if strings.Contains(out.String(), "Recovered in-flight formula") {
			t.Errorf("af up announced a formula recovery for a live agent: %q", out.String())
		}
		if lastRun := fx.lastRun(t); !strings.Contains(lastRun, "agent manager: outcome=") || !strings.Contains(lastRun, " recovered=false") {
			t.Errorf("af_up_last_run = %q, want the live agent with recovered=false", lastRun)
		}
	})

	t.Run("unresolvable model no longer fails a live agent", func(t *testing.T) {
		fx := newLiveAgentFixture(t)
		writeDeclaredEffortProfile(t, fx.root)
		origModel := upModel
		upModel = "no-such-profile"
		t.Cleanup(func() { upModel = origModel })
		fx.fake.present[fx.sess] = true
		fx.fake.claudeRunning[fx.sess] = true

		cmd, out := launchCmd(t)
		if err := runUp(cmd, []string{"manager"}); err != nil {
			t.Fatalf("af up against a live agent with an unresolvable --model = %v, want nil\n%s", err, out)
		}
		if !strings.Contains(out.String(), fx.sess+": already running\n") {
			t.Errorf("af up output = %q, want %q", out.String(), fx.sess+": already running")
		}
		var notes []string
		for _, line := range strings.Split(out.String(), "\n") {
			if strings.Contains(line, "--model") && strings.Contains(line, "no-such-profile") {
				notes = append(notes, line)
			}
		}
		if len(notes) != 1 {
			t.Errorf("af up --model against a live agent printed %d notes naming --model no-such-profile, "+
				"want exactly one; output=%q", len(notes), out.String())
		}
	})
}

// TestUpLiveGuardFailsOpenOnProbeError pins that a tmux probe fault reads as "not live": the guard
// must fall through to Start, which probes again, rather than refusing a launch on an error.
func TestUpLiveGuardFailsOpenOnProbeError(t *testing.T) {
	fx := newLiveAgentFixture(t)
	writeRuntimeFile(t, fx.wtAgentDir, "dispatched", "1\n")
	fx.fake.hasSessionErr[fx.sess] = errors.New("tmux probe fault")
	fx.fake.claudeRunning[fx.sess] = true

	cmd, out := launchCmd(t)
	if err := runUp(cmd, []string{"manager"}); err != nil {
		t.Fatalf("af up: %v\n%s", err, out)
	}
	if strings.Contains(out.String(), ": already running") {
		t.Errorf("a probe fault was read as live: %q", out.String())
	}

	create := -1
	probes := 0
	for i, op := range fx.fake.ops {
		if strings.HasPrefix(op, "NewSession "+fx.sess+" ") {
			create = i
			break
		}
		if op == "HasSession "+fx.sess {
			probes++
		}
		if op == "IsClaudeRunning "+fx.sess {
			t.Errorf("claude liveness was probed although the session probe failed: ops=%v", fx.fake.ops)
		}
	}
	if create < 0 {
		t.Fatalf("af up did not fall through to Start's launch: ops=%v", fx.fake.ops)
	}
	if probes != 2 {
		t.Errorf("session probed %d times before launch, want 2 (the guard, then Start's own re-probe): ops=%v", probes, fx.fake.ops)
	}
	if _, err := os.Stat(filepath.Join(fx.wtAgentDir, ".runtime", "dispatched")); !os.IsNotExist(err) {
		t.Errorf("relaunch cleanup did not run after a probe fault (dispatched stat err=%v)", err)
	}
}
