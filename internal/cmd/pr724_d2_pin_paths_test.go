package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/worktree"
)

func p724d2TouchPin(t *testing.T, agentDir string) string {
	t.Helper()
	p := integrationPinPath(agentDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func p724d2WriteMeta(t *testing.T, root, id, path string, agents ...string) {
	t.Helper()
	if err := worktree.WriteMeta(root, &worktree.Meta{ID: id, Owner: agents[0], Branch: "af/" + agents[0] + "-" + id, Path: path, Agents: agents}); err != nil {
		t.Fatal(err)
	}
}

// TestPR724_T11_PinPathsListsEveryArmOnce: GC and the service pass share one lister, and it returns every
// reachable pin path exactly once, whichever arm reaches it: the root agents, an in-tree worktree with or
// without its sidecar, a relocated worktree's owner and its unrecorded co-tenant, and an in-root worktree
// nested below the in-tree glob's single level.
func TestPR724_T11_PinPathsListsEveryArmOnce(t *testing.T) {
	root := t.TempDir()
	wts := worktree.WorktreesDir(root)
	reloc := p724d2RelocatedWorktree(t, root, "wt-reloc", "owner")
	p724d2WriteMeta(t, root, "wt-both", filepath.Join(".agentfactory", "worktrees", "wt-both"), "y")
	nested := filepath.Join(".agentfactory", "worktrees", "nest", "wt-n")
	p724d2WriteMeta(t, root, "wt-n", nested, "z")

	want := map[string]string{
		"root_agent":           p724d2TouchPin(t, config.AgentDir(root, "manager")),
		"in_tree_without_meta": p724d2TouchPin(t, config.AgentDir(filepath.Join(wts, "wt-x"), "y")),
		"in_tree_with_meta":    p724d2TouchPin(t, config.AgentDir(filepath.Join(wts, "wt-both"), "y")),
		"relocated_owner":      p724d2TouchPin(t, config.AgentDir(reloc, "owner")),
		"relocated_cotenant":   p724d2TouchPin(t, config.AgentDir(reloc, "cotenant")),
		"nested_in_root":       p724d2TouchPin(t, config.AgentDir(filepath.Join(root, nested), "z")),
	}

	paths, err := integrationPinPaths(root)
	if err != nil {
		t.Fatalf("integrationPinPaths: %v", err)
	}
	seen := map[string]int{}
	for _, p := range paths {
		seen[filepath.Clean(p)]++
	}
	for p, n := range seen {
		if n > 1 {
			t.Errorf("pin path %s listed %d times; the lister returns each path once", p, n)
		}
	}
	for arm, p := range want {
		if seen[p] == 0 {
			t.Errorf("%s: pin %s not listed; got %q", arm, p, paths)
		}
	}
}

// TestPR724_T11_PinPathsFailsClosedAllOrNothing: a pin behind unreadable worktree metadata may still bind,
// so the lister returns no paths and an error naming the worktree, never a partial list a caller could
// mistake for the whole fleet.
func TestPR724_T11_PinPathsFailsClosedAllOrNothing(t *testing.T) {
	for _, tc := range []struct {
		name     string
		breakFix func(t *testing.T, root string)
	}{
		{"corrupt_meta_json", func(t *testing.T, root string) {
			if err := os.MkdirAll(worktree.WorktreesDir(root), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(worktree.WorktreesDir(root), "wt-z.meta.json"), []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"relocated_agents_dir_unreadable", func(t *testing.T, root string) {
			if os.Geteuid() == 0 {
				t.Skip("root reads through mode 0")
			}
			wt := p724d2RelocatedWorktree(t, root, "wt-z", "owner")
			agentDir := config.AgentDir(wt, "owner")
			p724d2TouchPin(t, agentDir)
			agents := filepath.Dir(agentDir)
			if err := os.Chmod(agents, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(agents, 0o755) })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			p724d2TouchPin(t, config.AgentDir(root, "manager"))
			tc.breakFix(t, root)

			paths, err := integrationPinPaths(root)
			if err == nil || !strings.Contains(err.Error(), "wt-z") {
				t.Errorf("unreadable metadata of worktree wt-z must be an error naming it; err=%v", err)
			}
			if paths != nil {
				t.Errorf("the lister is all-or-nothing: with an error it returns no paths, got %q", paths)
			}
		})
	}
}
