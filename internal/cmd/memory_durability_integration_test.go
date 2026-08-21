//go:build integration

package cmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/worktree"
)

// The live-tmux half of T-DUR's GC row (ADR-018: a leg whose behaviour depends on a real
// has-session result cannot be faked, so it is tagged `integration` and runs only under
// `make test-integration`).
//
// The hermetic row in memory_durability_test.go proves the note outlives a reaping. It cannot
// prove the OTHER half — that GC declines to reap while the owner is alive — because under the
// package's TMUX_TMPDIR redirect every has-session answers "no", so the skip branch is
// unreachable there and a regression that reaped live worktrees would leave it green. Both
// branches are asserted here against one real session, so the vault claim covers the reaping and
// the sparing alike.
func TestMemoryDurability_NoteSurvivesGCByAStrangerWithLiveTmux(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	f := newDurabilityFactory(t, "solver")
	const marker = "gc by a stranger is the teardown nobody is watching"
	f.record(t, marker)

	// The production identity GC probes, spelled the way GC spells it ("=af-"+meta.Owner).
	const sessionName = "af-solver"
	_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %v\n%s", err, out)
	}
	killed := false
	t.Cleanup(func() {
		if !killed {
			_ = exec.Command("tmux", "kill-session", "-t", sessionName).Run()
		}
	})

	removed, err := worktree.GC(f.root)
	if err != nil {
		t.Fatalf("GC with a live owner session: %v", err)
	}
	if removed != 0 {
		t.Fatalf("GC reaped %d worktrees while %s was alive, want 0", removed, sessionName)
	}
	if _, err := os.Stat(f.wtDir); err != nil {
		t.Fatalf("GC destroyed a live agent's worktree %s: %v", f.wtDir, err)
	}
	if out := f.successorInjection(t); !strings.Contains(out, marker) {
		t.Errorf("the note was lost with no teardown at all; injection was:\n%s", out)
	}

	if out, err := exec.Command("tmux", "kill-session", "-t", sessionName).CombinedOutput(); err != nil {
		t.Fatalf("tmux kill-session: %v\n%s", err, out)
	}
	killed = true

	removed, err = worktree.GC(f.root)
	if err != nil {
		t.Fatalf("GC after the owner died: %v", err)
	}
	if removed != 1 {
		t.Fatalf("GC reaped %d worktrees after %s died, want 1", removed, sessionName)
	}
	assertWorktreeDestroyed(t, f)
	f.assertNothingUnderWorktrees(t, "after the reaping")

	if out := f.successorInjection(t); !strings.Contains(out, marker) {
		t.Errorf("the note did not survive a real GC reaping; injection was:\n%s", out)
	}
}
