package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// gcSnapshots returns acme-int's two sibling snapshots from intBRemoveSetup as absolute paths: the one the
// record no longer names (the first install) and the one it does (the re-install).
func gcSnapshots(t *testing.T, f *intBRemoveFixture) (old, current string) {
	t.Helper()
	snaps, _ := intBSnapshotDirs(t, f.e.root, intBName)
	cur := filepath.Join(f.e.root, filepath.FromSlash(intBLoadEntry(t, f.e.root, intBName).Integration.SnapshotDir))
	for _, s := range snaps {
		abs := filepath.Join(snapshotParent(f.e.root, intBName), s)
		if abs != cur {
			old = abs
		}
	}
	if old == "" {
		t.Fatalf("fixture: no snapshot other than the recorded %s among %v", cur, snaps)
	}
	return old, cur
}

func TestPluginRemove_GCSparesPinnedSnapshot(t *testing.T) {
	cases := []struct {
		name      string
		pinRoot   func(old, cur string) string // snapshot the root-agent pin names ("" = no root pin)
		pinWT     func(old, cur string) string // snapshot the worktree-agent pin names ("" = pins another integration)
		spared    func(old, cur string) []string
		collected func(old, cur string) []string
	}{
		{
			name:      "root_agent_pin_spares_old_snapshot",
			pinRoot:   func(old, _ string) string { return old },
			pinWT:     func(_, _ string) string { return "" },
			spared:    func(old, _ string) []string { return []string{old} },
			collected: func(_, cur string) []string { return []string{cur} },
		},
		{
			name:      "worktree_agent_pin_spares_current_snapshot",
			pinRoot:   func(_, _ string) string { return "" },
			pinWT:     func(_, cur string) string { return cur },
			spared:    func(_, cur string) []string { return []string{cur} },
			collected: func(old, _ string) []string { return []string{old} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := intBRemoveSetup(t)
			root := f.e.root
			old, cur := gcSnapshots(t, f)

			rootAgent := config.AgentDir(root, "manager")
			wtAgent := config.AgentDir(filepath.Join(root, ".agentfactory", "worktrees", "wt-x"), "y")
			if s := tc.pinRoot(old, cur); s != "" {
				pinFixtureWrite(t, rootAgent, integrationPin{Formula: "f", Bindings: []integrationPinBinding{pinFixtureBinding(t, root, intBName, s, true)}})
			}
			wtBinding := pinFixtureBinding(t, root, "other-int", filepath.Join(snapshotParent(root, "other-int"), strings.Repeat("c", 64)), false)
			if s := tc.pinWT(old, cur); s != "" {
				wtBinding = pinFixtureBinding(t, root, intBName, s, true)
			}
			pinFixtureWrite(t, wtAgent, integrationPin{Formula: "g", Bindings: []integrationPinBinding{wtBinding}})

			out, err := runPlugin(t, "remove", nil, intBName)
			if err != nil {
				t.Fatalf("remove: %v\n%s", err, out)
			}
			for _, s := range tc.spared(old, cur) {
				if !intBExists(filepath.Join(s, config.IntegrationManifestFile)) {
					t.Errorf("pinned snapshot %s was garbage-collected; a running instance still binds it", s)
				}
				if !strings.Contains(out, filepath.Base(s)) {
					t.Errorf("remove output does not name the spared snapshot %s:\n%s", filepath.Base(s), out)
				}
			}
			for _, s := range tc.collected(old, cur) {
				if intBExists(s) {
					t.Errorf("unpinned snapshot %s survived remove", s)
				}
			}
			if _, ok := intBPluginsRaw(t, root)[intBName]; ok {
				t.Error("remove left the record entry even though a snapshot is pinned")
			}
			for _, p := range f.runtime {
				if intBExists(p) {
					t.Errorf("remove kept runtime record %s", p)
				}
			}
		})
	}

	t.Run("no_pins_removes_the_parent", func(t *testing.T) {
		f := intBRemoveSetup(t)
		pinFixtureWrite(t, config.AgentDir(f.e.root, "manager"), integrationPin{Formula: "f", Bindings: []integrationPinBinding{
			pinFixtureBinding(t, f.e.root, "other-int", filepath.Join(snapshotParent(f.e.root, "other-int"), strings.Repeat("c", 64)), false),
		}})
		if out, err := runPlugin(t, "remove", nil, intBName); err != nil {
			t.Fatalf("remove: %v\n%s", err, out)
		}
		if intBExists(snapshotParent(f.e.root, intBName)) {
			t.Error("with no pin naming acme-int, the whole IntegrationsDir/acme-int must be gone, not just emptied")
		}
	})
}

// p724d2RelocatedWorktree registers worktree id with an absolute meta.Path outside root, the shape
// relocationAwarePath stores for a relocated worktree, and returns that path. meta.Agents lists only the
// first agent: a co-tenant attached later is never appended there.
func p724d2RelocatedWorktree(t *testing.T, root, id string, agents ...string) string {
	t.Helper()
	wtPath := filepath.Join(t.TempDir(), "relocated", id)
	if rel, err := filepath.Rel(root, wtPath); err != nil || !strings.HasPrefix(rel, "..") {
		t.Fatalf("fixture: relocated worktree %s is not outside the factory root %s", wtPath, root)
	}
	if err := worktree.WriteMeta(root, &worktree.Meta{ID: id, Owner: agents[0], Branch: "af/" + agents[0] + "-" + id, Path: wtPath, Agents: agents}); err != nil {
		t.Fatal(err)
	}
	return wtPath
}

func p724d2PinAcme(t *testing.T, root, agentDir, snap string) {
	t.Helper()
	pinFixtureWrite(t, agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{pinFixtureBinding(t, root, intBName, snap, true)}})
}

func p724d2PinOther(t *testing.T, root, agentDir string) {
	t.Helper()
	pinFixtureWrite(t, agentDir, integrationPin{Formula: "g", Bindings: []integrationPinBinding{
		pinFixtureBinding(t, root, "other-int", filepath.Join(snapshotParent(root, "other-int"), strings.Repeat("c", 64)), false),
	}})
}

func p724d2RequireSpared(t *testing.T, out, snap string) {
	t.Helper()
	if !intBExists(filepath.Join(snap, config.IntegrationManifestFile)) {
		t.Errorf("pinned snapshot %s was garbage-collected; a running instance still binds it", filepath.Base(snap))
	}
	if n := strings.Count(out, "kept snapshot "+filepath.Base(snap)); n != 1 {
		t.Errorf("remove output names spared snapshot %s %d times, want exactly 1:\n%s", filepath.Base(snap), n, out)
	}
}

func p724d2RequireCollected(t *testing.T, snap string) {
	t.Helper()
	if intBExists(snap) {
		t.Errorf("unpinned snapshot %s survived remove", filepath.Base(snap))
	}
}

func p724d2RemoveOK(t *testing.T) string {
	t.Helper()
	out, err := runPlugin(t, "remove", nil, intBName)
	if err != nil {
		t.Fatalf("remove: %v\n%s", err, out)
	}
	return out
}

// TestPR724_T11_RelocatedPinSparesSnapshot: a relocated worktree's pin keeps its snapshot through GC,
// whether the pinning agent is the owner meta.Agents lists or a co-tenant it never records.
func TestPR724_T11_RelocatedPinSparesSnapshot(t *testing.T) {
	for _, tc := range []struct{ name, pinAgent string }{
		{"owner_listed_in_meta_agents", "owner"},
		{"cotenant_absent_from_meta_agents", "cotenant"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := intBRemoveSetup(t)
			root := f.e.root
			old, cur := gcSnapshots(t, f)
			wt := p724d2RelocatedWorktree(t, root, "wt-reloc", "owner")
			p724d2PinAcme(t, root, config.AgentDir(wt, tc.pinAgent), old)

			out := p724d2RemoveOK(t)
			p724d2RequireSpared(t, out, old)
			p724d2RequireCollected(t, cur)
			if _, ok := intBPluginsRaw(t, root)[intBName]; ok {
				t.Error("remove left the record entry even though a snapshot is pinned")
			}
		})
	}
}

// TestPR724_T11_RelocatedAndMetalessInTreePinsBothSpared: the metadata arm is added to the in-tree glob,
// not swapped for it, so an in-tree worktree whose sidecar is lost still counts beside a relocated one.
func TestPR724_T11_RelocatedAndMetalessInTreePinsBothSpared(t *testing.T) {
	f := intBRemoveSetup(t)
	root := f.e.root
	old, cur := gcSnapshots(t, f)
	wt := p724d2RelocatedWorktree(t, root, "wt-reloc", "owner")
	p724d2PinAcme(t, root, config.AgentDir(wt, "owner"), old)
	p724d2PinAcme(t, root, config.AgentDir(filepath.Join(worktree.WorktreesDir(root), "wt-x"), "y"), cur)
	if _, err := os.Stat(filepath.Join(worktree.WorktreesDir(root), "wt-x.meta.json")); !os.IsNotExist(err) {
		t.Fatalf("fixture: wt-x must have no meta sidecar (lost-sidecar case), stat err=%v", err)
	}

	out := p724d2RemoveOK(t)
	p724d2RequireSpared(t, out, old)
	p724d2RequireSpared(t, out, cur)
}

// TestPR724_T11_UnreadableMetadataFailsClosed: when worktree metadata cannot be read, a pin behind it may
// still bind, so remove keeps every snapshot and surfaces the error instead of guessing. The record still
// goes first, so the error is the existing "snapshots remain" report.
func TestPR724_T11_UnreadableMetadataFailsClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		namesID  bool
		breakFix func(t *testing.T, root, old string)
	}{
		{"corrupt_meta_json", true, func(t *testing.T, root, _ string) {
			if err := os.MkdirAll(worktree.WorktreesDir(root), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(worktree.WorktreesDir(root), "wt-z.meta.json"), []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"meta_file_unreadable", true, func(t *testing.T, root, _ string) {
			p724d2RelocatedWorktree(t, root, "wt-z", "owner")
			p := filepath.Join(worktree.WorktreesDir(root), "wt-z.meta.json")
			if err := os.Chmod(p, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(p, 0o644) })
		}},
		{"worktrees_dir_unreadable", false, func(t *testing.T, root, _ string) {
			p724d2RelocatedWorktree(t, root, "wt-z", "owner")
			d := worktree.WorktreesDir(root)
			if err := os.Chmod(d, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(d, 0o755) })
		}},
		{"relocated_agents_dir_unreadable", true, func(t *testing.T, root, old string) {
			wt := p724d2RelocatedWorktree(t, root, "wt-z", "owner")
			agentDir := config.AgentDir(wt, "owner")
			p724d2PinAcme(t, root, agentDir, old)
			agents := filepath.Dir(agentDir)
			if err := os.Chmod(agents, 0); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(agents, 0o755) })
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if os.Geteuid() == 0 {
				t.Skip("root reads through mode 0")
			}
			f := intBRemoveSetup(t)
			root := f.e.root
			old, cur := gcSnapshots(t, f)
			tc.breakFix(t, root, old)

			out, err := runPlugin(t, "remove", nil, intBName)
			if err == nil {
				t.Fatalf("remove succeeded although worktree metadata is unreadable; it must fail closed:\n%s", out)
			}
			if !strings.Contains(err.Error(), "snapshots remain") {
				t.Errorf("error does not tell the operator the snapshots were kept: %v", err)
			}
			if tc.namesID && !strings.Contains(err.Error(), "wt-z") {
				t.Errorf("error does not name the worktree wt-z whose metadata is unreadable: %v", err)
			}
			for _, s := range []string{old, cur} {
				if !intBExists(filepath.Join(s, config.IntegrationManifestFile)) {
					t.Errorf("snapshot %s was deleted although the pins behind unreadable metadata are unknown", filepath.Base(s))
				}
			}
			if _, ok := intBPluginsRaw(t, root)[intBName]; ok {
				t.Error("the record goes first: a GC that fails closed still leaves the record removed")
			}
		})
	}
}

// TestPR724_T11_KeepUnreferencedSnapshotsDeleted: metadata widens what counts as a reference, never what
// counts as pinned. A snapshot no readable pin names is still deleted, with or without relocated worktrees.
func TestPR724_T11_KeepUnreferencedSnapshotsDeleted(t *testing.T) {
	t.Run("relocated_pin_names_another_integration", func(t *testing.T) {
		f := intBRemoveSetup(t)
		root := f.e.root
		wt := p724d2RelocatedWorktree(t, root, "wt-reloc", "owner")
		p724d2PinOther(t, root, config.AgentDir(wt, "owner"))

		out := p724d2RemoveOK(t)
		if intBExists(snapshotParent(root, intBName)) {
			t.Errorf("no pin names %s, so IntegrationsDir/%s must be gone:\n%s", intBName, intBName, out)
		}
		if strings.Contains(out, "kept snapshot") {
			t.Errorf("remove spared a snapshot no pin names:\n%s", out)
		}
	})

	t.Run("relocated_worktree_dir_gone", func(t *testing.T) {
		f := intBRemoveSetup(t)
		root := f.e.root
		wt := p724d2RelocatedWorktree(t, root, "wt-gone", "owner")
		if _, err := os.Stat(wt); !os.IsNotExist(err) {
			t.Fatalf("fixture: relocated dir %s must not exist, stat err=%v", wt, err)
		}

		p724d2RemoveOK(t)
		if intBExists(snapshotParent(root, intBName)) {
			t.Error("a meta whose relocated worktree no longer exists holds no pin; the snapshots must be collected")
		}
	})

	t.Run("malformed_pin_in_relocated_worktree_is_skipped", func(t *testing.T) {
		f := intBRemoveSetup(t)
		root := f.e.root
		wt := p724d2RelocatedWorktree(t, root, "wt-reloc", "owner")
		p := integrationPinPath(config.AgentDir(wt, "owner"))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}

		p724d2RemoveOK(t)
		if intBExists(snapshotParent(root, intBName)) {
			t.Error("an unreadable pin binds nothing at launch either; it must not spare a snapshot")
		}
	})

	t.Run("no_worktrees_dir_is_not_an_error", func(t *testing.T) {
		f := intBRemoveSetup(t)
		root := f.e.root
		if err := os.RemoveAll(worktree.WorktreesDir(root)); err != nil {
			t.Fatal(err)
		}

		p724d2RemoveOK(t)
		if intBExists(snapshotParent(root, intBName)) {
			t.Error("with no worktrees dir and no pin, the snapshots must be collected")
		}
	})
}

// TestPR724_T11_KeepPinReachedTwiceNamedOnce: an in-tree worktree with a meta is reached by both the glob
// and its metadata; it is still one reference, spared and named once.
func TestPR724_T11_KeepPinReachedTwiceNamedOnce(t *testing.T) {
	for _, tc := range []struct {
		name string
		path func(root, id string) string
	}{
		{"meta_path_relative", func(_, id string) string { return filepath.Join(".agentfactory", "worktrees", id) }},
		{"meta_path_absolute_in_tree", func(root, id string) string { return filepath.Join(worktree.WorktreesDir(root), id) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := intBRemoveSetup(t)
			root := f.e.root
			old, cur := gcSnapshots(t, f)
			const id = "wt-both"
			if err := worktree.WriteMeta(root, &worktree.Meta{ID: id, Owner: "y", Branch: "af/y-" + id, Path: tc.path(root, id), Agents: []string{"y"}}); err != nil {
				t.Fatal(err)
			}
			p724d2PinAcme(t, root, config.AgentDir(filepath.Join(worktree.WorktreesDir(root), id), "y"), old)

			out := p724d2RemoveOK(t)
			p724d2RequireSpared(t, out, old)
			p724d2RequireCollected(t, cur)
		})
	}
}
