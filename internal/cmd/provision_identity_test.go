//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestProvisionIdentity_RefreshesStaleFile is #675 K2 on the recycle funnel. Every respawn replaces
// the pane, so the relaunched session re-reads CLAUDE.md — and before this the funnel refreshed the
// settings.json the HARNESS reads while leaving stale the file the MODEL reads.
func TestProvisionIdentity_RefreshesStaleFile(t *testing.T) {
	factoryRoot, agentDir := setupFactoryFixture(t, "manager")
	entry := config.AgentEntry{Type: "interactive", Description: "test agent"}
	opts := RespawnOptions{
		FactoryRoot:  factoryRoot,
		AgentName:    "manager",
		AgentEntry:   entry,
		AgentWorkDir: agentDir,
	}
	identityPath := filepath.Join(agentDir, "CLAUDE.md")

	t.Run("a stale identity file is rewritten", func(t *testing.T) {
		if err := os.WriteFile(identityPath, []byte("stale\n"), 0644); err != nil {
			t.Fatal(err)
		}
		provisionIdentity(opts)

		got, err := os.ReadFile(identityPath)
		if err != nil {
			t.Fatalf("reading CLAUDE.md: %v", err)
		}
		if string(got) == "stale\n" {
			t.Fatal("the recycle funnel left a stale CLAUDE.md in place")
		}
		if !strings.Contains(string(got), "# Agent Identity: manager") {
			t.Errorf("CLAUDE.md was not re-rendered from the embedded template, got:\n%s", got)
		}
	})

	t.Run("an identical identity file is not rewritten", func(t *testing.T) {
		// The file is already canonical from the subtest above. Age its mtime so a write would be
		// unmistakable: a respawn runs on every recycle, and churning the mtime of a file agents
		// read at session start is noise no one asked for.
		past := time.Now().Add(-time.Hour)
		if err := os.Chtimes(identityPath, past, past); err != nil {
			t.Fatal(err)
		}
		before, err := os.Stat(identityPath)
		if err != nil {
			t.Fatal(err)
		}

		provisionIdentity(opts)

		after, err := os.Stat(identityPath)
		if err != nil {
			t.Fatal(err)
		}
		if !after.ModTime().Equal(before.ModTime()) {
			t.Errorf("an unchanged identity file was rewritten (mtime %v -> %v); the write must be write-if-different",
				before.ModTime(), after.ModTime())
		}
	})

	t.Run("a funnel with no factory root does nothing", func(t *testing.T) {
		provisionIdentity(RespawnOptions{AgentName: "manager", AgentEntry: entry, AgentWorkDir: agentDir})
	})
}

// TestReprovisionAgentSettings_RefreshesStaleIdentity is #675 K2 on the other provisioning funnel.
// `af install --init` re-provisions every agent in the factory, and before this it refreshed the
// settings.json the harness reads while leaving stale the CLAUDE.md the model reads.
func TestReprovisionAgentSettings_RefreshesStaleIdentity(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	stale := []byte("stale identity\n")
	for _, agent := range []string{"manager", "supervisor"} {
		if err := os.WriteFile(filepath.Join(config.AgentDir(root, agent), "CLAUDE.md"), stale, 0644); err != nil {
			t.Fatal(err)
		}
	}

	var out strings.Builder
	if err := reprovisionAgentSettings(root, &out); err != nil {
		t.Fatalf("reprovisionAgentSettings: %v", err)
	}

	for _, agent := range []string{"manager", "supervisor"} {
		got, err := os.ReadFile(filepath.Join(config.AgentDir(root, agent), "CLAUDE.md"))
		if err != nil {
			t.Fatalf("reading %s CLAUDE.md: %v", agent, err)
		}
		if !strings.Contains(string(got), "# Agent Identity: "+agent) {
			t.Errorf("af install --init left a stale CLAUDE.md for %s, got:\n%s", agent, got)
		}
		if !strings.Contains(string(got), root) {
			t.Errorf("%s CLAUDE.md must be rendered against the factory root %s, got:\n%s", agent, root, got)
		}
	}
	if w := out.String(); w != "" {
		t.Errorf("re-provisioning a healthy factory must warn about nothing, got: %s", w)
	}
}
