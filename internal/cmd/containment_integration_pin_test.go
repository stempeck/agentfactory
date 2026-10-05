package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// pinnedContainmentEnv is a worktree agent whose pin binds acme-int at a snapshot under the factory's
// IntegrationsDir, which lies outside the worktree boundary. It returns the agent dir, the pinned snapshot and
// an unpinned sibling snapshot of the same integration.
func pinnedContainmentEnv(t *testing.T, writePin bool) (agentDir, pinned, unpinned string) {
	t.Helper()
	worktreeRoot, agentDir := setupWorktreeContainmentEnv(t, "solver")
	factoryRoot := containmentFactoryRoot(worktreeRoot)
	if _, err := os.Stat(config.FactoryConfigPath(factoryRoot)); err != nil {
		t.Fatalf("fixture: %s is not the factory root: %v", factoryRoot, err)
	}
	pinned = filepath.Join(snapshotParent(factoryRoot, intBName), strings.Repeat("a", 64))
	unpinned = filepath.Join(snapshotParent(factoryRoot, intBName), strings.Repeat("b", 64))
	for _, d := range []string{pinned, unpinned} {
		if err := os.MkdirAll(filepath.Join(d, "claude-plugin"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if writePin {
		pinFixtureWrite(t, agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{
			pinFixtureBinding(t, factoryRoot, intBName, pinned, true),
		}})
	}
	return agentDir, pinned, unpinned
}

// containmentFactoryRoot maps setupWorktreeFixture's <root>/.agentfactory/worktrees/<id> back to <root>.
func containmentFactoryRoot(worktreeRoot string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(worktreeRoot)))
}

func containmentBeads(t *testing.T, p containmentPayload) (int, string) {
	t.Helper()
	recs := installContainmentRecorder(t)
	var stdout bytes.Buffer
	if err := runContainmentCheckCore(&stdout, p); err != nil {
		t.Fatalf("runContainmentCheckCore must always exit 0: %v", err)
	}
	return len(*recs), stdout.String()
}

func TestContainment_PinnedIntegrationDirAllowed(t *testing.T) {
	payloads := []struct {
		name string
		mk   func(snap, cwd string) containmentPayload
	}{
		{"cd_into_pinned_dir", func(snap, cwd string) containmentPayload {
			return bashPayload("cd "+filepath.Join(snap, "claude-plugin"), cwd)
		}},
		{"git_C_pinned_dir", func(snap, cwd string) containmentPayload {
			return bashPayload("git -C "+snap+" status", cwd)
		}},
		{"write_under_pinned_dir", func(snap, cwd string) containmentPayload {
			return writePayload("Write", filepath.Join(snap, "claude-plugin", "x"), cwd)
		}},
		{"edit_under_pinned_dir", func(snap, cwd string) containmentPayload {
			return writePayload("Edit", filepath.Join(snap, "claude-plugin", "x"), cwd)
		}},
	}
	for _, pl := range payloads {
		t.Run(pl.name, func(t *testing.T) {
			agentDir, pinned, _ := pinnedContainmentEnv(t, true)
			if n, out := containmentBeads(t, pl.mk(pinned, agentDir)); n != 0 || strings.TrimSpace(out) != "" {
				t.Errorf("a target under the pinned consumed snapshot must be in bounds; got %d bead(s), stdout %q", n, out)
			}
		})
		t.Run(pl.name+"_unpinned_sibling_is_out", func(t *testing.T) {
			agentDir, _, unpinned := pinnedContainmentEnv(t, true)
			if n, _ := containmentBeads(t, pl.mk(unpinned, agentDir)); n != 1 {
				t.Errorf("an unpinned snapshot of the same integration must stay out of bounds; got %d bead(s), want 1", n)
			}
		})
		t.Run(pl.name+"_without_pin_is_out", func(t *testing.T) {
			agentDir, pinned, _ := pinnedContainmentEnv(t, false)
			if n, _ := containmentBeads(t, pl.mk(pinned, agentDir)); n != 1 {
				t.Errorf("with no pin the snapshot dir must stay out of bounds; got %d bead(s), want 1", n)
			}
		})
	}

	t.Run("forged_pin_outside_integrations_dir_is_out", func(t *testing.T) {
		agentDir, _, _ := pinnedContainmentEnv(t, false)
		factoryRoot := containmentFactoryRoot(os.Getenv("AF_WORKTREE"))
		elsewhere := filepath.Join(factoryRoot, "elsewhere", strings.Repeat("a", 64))
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		pinFixtureWrite(t, agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{
			pinFixtureBinding(t, factoryRoot, intBName, elsewhere, true),
		}})
		if n, _ := containmentBeads(t, bashPayload("cd "+elsewhere, agentDir)); n != 1 {
			t.Errorf("a pin naming a dir outside IntegrationsDir/<name>/<sha> must not widen the allowlist; got %d bead(s), want 1", n)
		}
	})

	t.Run("forged_pin_traversal_name_and_sha_is_out", func(t *testing.T) {
		agentDir, _, _ := pinnedContainmentEnv(t, false)
		factoryRoot := containmentFactoryRoot(os.Getenv("AF_WORKTREE"))
		elsewhere := filepath.Join(factoryRoot, "elsewhere")
		if err := os.MkdirAll(elsewhere, 0o755); err != nil {
			t.Fatal(err)
		}
		rel, err := filepath.Rel(config.IntegrationsDir(factoryRoot), factoryRoot)
		if err != nil {
			t.Fatal(err)
		}
		pinFixtureWrite(t, agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{{
			Name: filepath.ToSlash(rel), ContentSHA256: "elsewhere", SnapshotDir: "elsewhere", Required: true,
		}}})
		if n, _ := containmentBeads(t, bashPayload("cd "+elsewhere, agentDir)); n != 1 {
			t.Errorf("a pin whose name and sha walk out of IntegrationsDir must not widen the allowlist; got %d bead(s), want 1", n)
		}
	})
}

// Working inside a pinned snapshot is exempt from being an escape, but it is not a return in bounds: it must
// not wipe the one-shot marker of a real escape, or that escape re-notifies on its next repeat.
func TestContainment_PinnedSnapshotKeepsEscapeDedup(t *testing.T) {
	agentDir, pinned, unpinned := pinnedContainmentEnv(t, true)
	escape := bashPayload("cd "+unpinned, agentDir)
	if n, _ := containmentBeads(t, escape); n != 1 {
		t.Fatalf("fixture: first escape must notify once; got %d bead(s)", n)
	}
	if n, _ := containmentBeads(t, bashPayload("cd "+filepath.Join(pinned, "claude-plugin"), agentDir)); n != 0 {
		t.Fatalf("fixture: the pinned snapshot must be allowlisted; got %d bead(s)", n)
	}
	if n, _ := containmentBeads(t, escape); n != 0 {
		t.Errorf("repeating the same escape after visiting the pinned snapshot re-notified (%d bead(s)); the allowlist cleared the dedup marker", n)
	}
	if n, _ := containmentBeads(t, bashPayload("cd "+agentDir, agentDir)); n != 0 {
		t.Fatalf("fixture: a return in bounds must be silent; got %d bead(s)", n)
	}
	if n, _ := containmentBeads(t, escape); n != 1 {
		t.Errorf("after a real return in bounds the escape must re-notify; got %d bead(s), want 1", n)
	}
}
