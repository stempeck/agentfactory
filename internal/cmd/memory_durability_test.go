package cmd

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// T-DUR — the evidence for AC-626-1, the founding acceptance criterion of the whole subsystem:
// "a learning survives, verifiable by recording a note, tearing down, and retrieving it."
// Five phases of construction make that claim plausible; only this matrix makes it falsifiable.
//
// Three shapes here are load-bearing, and dropping any one of them turns the matrix into a
// tautology that would stay green through the exact regression it exists to catch:
//
//   - The WRITE goes through the real CLI from a WORKTREE cwd. memory.Write(root, n) and seedNote
//     take the factory root as an argument, so a suite built on them proves the durability of a
//     path no agent ever takes; the resolver — not the store — is what decides whether a note is
//     born inside the doomed tree or outside it (memory.go:7-20).
//   - Every row asserts the teardown ACTUALLY HAPPENED. A fixture that silently no-ops (a
//     git worktree that was never registered, a meta the driver could not find) leaves the note
//     standing for the wrong reason and reports it as durability.
//   - Every row asserts nothing was written under worktrees/. setupWorktreeFixture nests the
//     worktree INSIDE the factory root, so "the note is somewhere under root" is true under the
//     WRONG resolver too.
//
// The fixture is a REAL git repo with a REAL `git worktree add`, because two of the seven drivers
// reach worktree.Remove / ForceRemove, which shell out to git and abort the removal when it
// fails — on a fake worktree they would tear nothing down and every row would pass vacuously.
// Hermetic per ADR-018: temp roots only, and the package TestMain (main_test.go) redirects
// TMUX_TMPDIR so GC's `tmux has-session` can never reach the operator's server.

type durabilityFactory struct {
	root       string
	agent      string
	wtID       string
	branch     string
	wtDir      string
	wtAgentDir string
	homeDir    string
}

// inDir runs fn with the process cwd at dir and restores the previous cwd by PATH.
//
// t.Chdir is the house idiom everywhere else and is deliberately avoided here: it restores
// through a dirfd it holds open on the directory it left, and the directories this suite chdirs
// into are precisely the ones the test then destroys.
func inDir(t *testing.T, dir string, fn func()) {
	t.Helper()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
	defer func() {
		if err := os.Chdir(prev); err != nil {
			t.Fatalf("restoring cwd to %s: %v", prev, err)
		}
	}()
	fn()
}

// newDurabilityFactory builds the layout an agent actually runs in: a git factory root owning a
// registered worktree, with the .factory-root redirect that is the only thing standing between a
// note and the teardown. It composes what mail_test.go's setupWorktreeFixture and
// teardown_memory_report_test.go's teardownFixture each supply half of — the configs an identity
// resolves against, and the worktree meta a teardown driver looks up.
func newDurabilityFactory(t *testing.T, agent string) durabilityFactory {
	t.Helper()
	// The repo's convention for a git-dependent test: skip rather than fail where git is absent.
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	gitTestCmd(t, root, "init", "-q", ".")
	gitTestCmd(t, root, "config", "user.email", "test@test.com")
	gitTestCmd(t, root, "config", "user.name", "Test")
	// Committed before the worktree exists: `git worktree remove` (the non-force spelling af down
	// uses) refuses a worktree holding untracked files, and .agentfactory/ is exactly that.
	writeFixtureFile(t, filepath.Join(root, ".gitignore"), ".agentfactory/\n")
	gitTestCmd(t, root, "add", ".gitignore")
	gitTestCmd(t, root, "commit", "-q", "-m", "initial")

	afDir := filepath.Join(root, ".agentfactory")
	homeDir := filepath.Join(afDir, "agents", agent)
	if err := os.MkdirAll(homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(afDir, "factory.json"), `{"type":"factory","version":1,"name":"test"}`)
	writeFixtureFile(t, filepath.Join(afDir, "agents.json"),
		`{"agents":{"`+agent+`":{"type":"autonomous","description":"test agent"}}}`)

	wtID := "wt-dur01"
	branch := "af/" + agent + "-dur01"
	wtDir := filepath.Join(afDir, "worktrees", wtID)
	gitTestCmd(t, root, "worktree", "add", "-q", "-b", branch, wtDir)

	wtAgentDir := filepath.Join(wtDir, ".agentfactory", "agents", agent)
	if err := os.MkdirAll(wtAgentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeFixtureFile(t, filepath.Join(wtDir, ".agentfactory", ".factory-root"), root)

	if err := worktree.WriteMeta(root, &worktree.Meta{
		ID:     wtID,
		Owner:  agent,
		Branch: branch,
		Path:   filepath.Join(".agentfactory", "worktrees", wtID),
		Agents: []string{agent},
	}); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	return durabilityFactory{
		root: root, agent: agent, wtID: wtID, branch: branch,
		wtDir: wtDir, wtAgentDir: wtAgentDir, homeDir: homeDir,
	}
}

// record drives the real `af memory add` from the worktree cwd — the only cwd an agent ever
// writes from — and proves on the spot that the bytes landed outside the doomed tree.
func (f durabilityFactory) record(t *testing.T, marker string, extra ...string) {
	t.Helper()
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	inDir(t, f.wtAgentDir, func() {
		args := append([]string{"-s", "durability", "-m", marker}, extra...)
		out, err := execMemoryOut(t, "add", args...)
		if err != nil {
			t.Fatalf("af memory add from the worktree cwd failed: %v (out=%q)", err, out)
		}
	})

	vault := config.AgentMemoryDir(f.root, f.agent)
	notes := noteFilesUnder(t, vault)
	if len(notes) != 1 {
		t.Fatalf("want exactly 1 note under %s, found %d: %v", vault, len(notes), notes)
	}
	if got := filepath.Dir(notes[0]); got != vault {
		t.Fatalf("note landed in %s, want exactly %s", got, vault)
	}
	f.assertNothingUnderWorktrees(t, "at write time")
}

// assertNothingUnderWorktrees is the negative control. Without it a row passes when the note was
// written into the worktree and read back from a teardown that happened not to remove it.
func (f durabilityFactory) assertNothingUnderWorktrees(t *testing.T, when string) {
	t.Helper()
	worktrees := filepath.Join(f.root, ".agentfactory", "worktrees")
	if leaked := noteFilesUnder(t, worktrees); len(leaked) != 0 {
		t.Fatalf("%s a note exists under the worktree tree %v — the root was resolved through the "+
			"nearest marker instead of the .factory-root redirect, so this row proves nothing", when, leaked)
	}
}

// successorInjection is the retrieval half of AC-626-1: the next session, standing in the agent
// home directory a respawn regenerates, receives the note through the SessionStart hook's own
// verb rather than through a direct read of the store.
func (f durabilityFactory) successorInjection(t *testing.T) string {
	t.Helper()
	if err := os.MkdirAll(f.homeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")
	var out string
	inDir(t, f.homeDir, func() {
		var err error
		out, err = execMemoryOut(t, "check", "--inject")
		if err != nil {
			t.Fatalf("successor `af memory check --inject` failed: %v (out=%q)", err, out)
		}
	})
	return out
}

// assertWorktreeDestroyed is the teardown-actually-happened guard for the six paths whose job is
// to take the worktree with them.
func assertWorktreeDestroyed(t *testing.T, f durabilityFactory) {
	t.Helper()
	if _, err := os.Stat(f.wtDir); !os.IsNotExist(err) {
		t.Fatalf("the teardown left %s standing (stat err=%v) — nothing was destroyed, so the row "+
			"would report survival of a teardown that did not happen", f.wtDir, err)
	}
}

// capturedDownCmd is the captured-stream cobra command every runDown-driven row uses.
func capturedDownCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var out, errb bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errb)
	return cmd, &out, &errb
}

// TestMemoryDurability_NoteSurvivesEveryTeardownPath is the seven-row matrix named in
// integration.md:118-122. Each row names the operator-visible action and drives the same call
// site that action reaches, so a refactor that reroutes one of them fails here rather than
// silently narrowing the coverage this criterion rests on.
func TestMemoryDurability_NoteSurvivesEveryTeardownPath(t *testing.T) {
	for _, row := range []struct {
		name      string
		tearDown  func(t *testing.T, f durabilityFactory)
		destroyed func(t *testing.T, f durabilityFactory)
	}{
		{
			// af done, dispatched session — finishDispatchedSession (done.go:386).
			name: "af done (dispatched session)",
			tearDown: func(t *testing.T, f durabilityFactory) {
				cwd := newDispatchedSessionDir(t, f.root, f.wtID)
				t.Setenv("AF_ROLE", f.agent)
				captureStderr(t, func() { finishDispatchedSession(cwd, f.root) })
			},
			destroyed: assertWorktreeDestroyed,
		},
		{
			// af down <agent> — the scoped stop, through cleanupAgentWorktree (down.go:229).
			name: "af down <agent>",
			tearDown: func(t *testing.T, f durabilityFactory) {
				setupHermeticSessions(t)
				cmd, _, _ := capturedDownCmd()
				inDir(t, f.root, func() {
					if err := runDown(cmd, []string{f.agent}); err != nil {
						t.Fatalf("runDown %s: %v", f.agent, err)
					}
				})
			},
			destroyed: assertWorktreeDestroyed,
		},
		{
			// af down --reset — resetAgent → resetAgentState (down.go:305), the loudest teardown
			// an operator has and the one most likely to be read as having taken the vault too.
			name: "af down --reset <agent>",
			tearDown: func(t *testing.T, f durabilityFactory) {
				setupHermeticSessions(t)
				downReset = true
				t.Cleanup(func() { downAll, downReset = false, false })
				cmd, _, _ := capturedDownCmd()
				inDir(t, f.root, func() {
					if err := runDown(cmd, []string{f.agent}); err != nil {
						t.Fatalf("runDown --reset %s: %v", f.agent, err)
					}
				})
			},
			destroyed: assertWorktreeDestroyed,
		},
		{
			// af sling --agent <name> --reset — the same reclamation reached from the dispatch
			// surface instead of the teardown one (sling.go:205).
			name: "af sling --reset",
			tearDown: func(t *testing.T, f durabilityFactory) {
				installMemStore(t)
				var buf bytes.Buffer
				if err := resetAgentState(context.Background(), &buf, f.root, f.agent,
					config.CloseReasonResetSling); err != nil {
					t.Fatalf("resetAgentState: %v", err)
				}
			},
			destroyed: assertWorktreeDestroyed,
		},
		{
			// GC by a stranger — worktree.GC (worktree.go:830), reached from another agent's
			// ResolveOrCreate with both return values discarded. The vault's owner is not even
			// running when this one destroys their worktree.
			name: "worktree GC run by a stranger",
			tearDown: func(t *testing.T, f durabilityFactory) {
				removed, err := worktree.GC(f.root)
				if err != nil {
					t.Fatalf("worktree.GC: %v", err)
				}
				if removed != 1 {
					t.Fatalf("GC reaped %d worktrees, want 1 — the row needs a real reaping", removed)
				}
			},
			destroyed: assertWorktreeDestroyed,
		},
		{
			// Agent reset — the whole agent workspace wiped and regenerated, which is what
			// `af install --agents` does per role via agent-gen-all.sh. Alone among these rows it
			// drives a primitive rather than a production function, because the production spelling
			// is a shell script and the only in-process spelling, `af formula agent-gen --delete`,
			// also os.Removes the REAL role template under the running factory (formula.go:419).
			//
			// What it therefore pins is a LAYOUT invariant rather than a code path: that
			// config.AgentMemoryDir is not reachable from config.AgentDir. That is worth a row —
			// siting the vault under the agent directory is the obvious place to put it, and doing
			// so would make every regeneration of an agent a silent erasure of its learnings.
			name: "agent workspace wiped and regenerated",
			tearDown: func(t *testing.T, f durabilityFactory) {
				agentDir := config.AgentDir(f.root, f.agent)
				vault := config.AgentMemoryDir(f.root, f.agent)
				if rel, err := filepath.Rel(agentDir, vault); err == nil && !strings.HasPrefix(rel, "..") {
					t.Fatalf("the vault %s sits inside the agent workspace %s — regenerating the agent "+
						"would erase its learnings", vault, agentDir)
				}
				if err := os.RemoveAll(agentDir); err != nil {
					t.Fatalf("wiping the agent workspace: %v", err)
				}
			},
			destroyed: func(t *testing.T, f durabilityFactory) {
				if _, err := os.Stat(f.homeDir); !os.IsNotExist(err) {
					t.Fatalf("the agent workspace %s survived its own wipe (stat err=%v)", f.homeDir, err)
				}
			},
		},
		{
			// Factory shutdown and restart — `af down --all`, the last thing an operator runs
			// before the container is a candidate for docker rm, followed by a fresh session.
			name: "factory-wide shutdown and restart",
			tearDown: func(t *testing.T, f durabilityFactory) {
				setupHermeticSessions(t)
				// Mandatory for any downAll row: an operator-tier runDown reaches
				// killOrphanedClaudeProcesses, which would otherwise sweep matching
				// processes across the whole host.
				installPkillRecorder(t)
				downAll = true
				t.Cleanup(func() { downAll, downReset = false, false })
				cmd, _, _ := capturedDownCmd()
				inDir(t, f.root, func() {
					if err := runDown(cmd, nil); err != nil {
						t.Fatalf("runDown --all: %v", err)
					}
				})
			},
			destroyed: assertWorktreeDestroyed,
		},
	} {
		t.Run(row.name, func(t *testing.T) {
			f := newDurabilityFactory(t, "solver")
			const marker = "the ci job needs GOTMPDIR or the worktree probe fails"
			f.record(t, marker)

			row.tearDown(t, f)
			row.destroyed(t, f)
			f.assertNothingUnderWorktrees(t, "after the teardown")

			out := f.successorInjection(t)
			if !strings.Contains(out, marker) {
				t.Errorf("the note did not reach the successor session after %q; injection was:\n%s",
					row.name, out)
			}
		})
	}
}

// T-META. A note that survives without its provenance is a disembodied instruction in a channel
// that reads as one (security.md T4): the successor is told to weigh a recorded observation, and
// weighing it requires knowing who recorded it, under what, when, and on what evidence. So the
// durability claim covers the attribution, not just the body.
func TestMemoryDurability_AttributionAndEvidenceSurviveTheTeardown(t *testing.T) {
	f := newDurabilityFactory(t, "solver")
	const marker = "the export marker is armed only after a fully streamed archive"
	f.record(t, marker,
		"--type", "gotcha",
		"--formula", "scenario",
		"--evidence", "issue#626",
		"--evidence", "commit:43e3724f")

	setupHermeticSessions(t)
	installPkillRecorder(t)
	downAll = true
	t.Cleanup(func() { downAll, downReset = false, false })
	cmd, _, _ := capturedDownCmd()
	inDir(t, f.root, func() {
		if err := runDown(cmd, nil); err != nil {
			t.Fatalf("runDown --all: %v", err)
		}
	})
	assertWorktreeDestroyed(t, f)

	out := f.successorInjection(t)
	for _, want := range []string{
		marker,
		"type=gotcha",
		"formula=scenario",
		"evidence: issue#626, commit:43e3724f",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the successor's injection lost %q; injection was:\n%s", want, out)
		}
	}
}
