package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// TestConfigMessagingSet covers the messaging write seam (issue #620 Phase 1 AC 1).
// messaging.json was load-only: the web console had no seam through which an operator could
// edit groups. The setter is modeled on `config startup set` for the write half and on
// `config dispatch set` for the cross-file half, because a group member IS an agent
// reference and dispatch is the established precedent for checking one.
func TestConfigMessagingSet(t *testing.T) {
	t.Run("a valid document is written atomically and reloads", func(t *testing.T) {
		root := setupConfigFactory(t)

		out, err := runConfigSet(t, runConfigMessagingSet, `{"groups":{"all":["manager","debugger"],"eng":["debugger"]}}`)
		if err != nil {
			t.Fatalf("a document naming only real agents must save; got %v (out=%q)", err, out)
		}
		if !strings.Contains(out, "saved") {
			t.Errorf("a successful write must report it; got %q", out)
		}

		// The atomic writer must leave no residue, exactly as the sibling setters assert.
		entries, err := os.ReadDir(filepath.Join(root, ".agentfactory"))
		if err != nil {
			t.Fatalf("read .agentfactory: %v", err)
		}
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".tmp") {
				t.Errorf("temp residue after an atomic write: %s", e.Name())
			}
		}

		// Reloading through the real loader proves the bytes are a document af-core accepts,
		// not merely something that marshaled.
		agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
		if err != nil {
			t.Fatalf("load agents.json: %v", err)
		}
		got, err := config.LoadMessagingConfig(config.MessagingConfigPath(root), agents)
		if err != nil {
			t.Fatalf("reload messaging.json: %v", err)
		}
		if len(got.Groups) != 2 || len(got.Groups["all"]) != 2 || got.Groups["eng"][0] != "debugger" {
			t.Errorf("round-trip lost content: %+v", got.Groups)
		}

		// House shape: two-space indent plus a trailing newline, same as every other setter.
		data, err := os.ReadFile(config.MessagingConfigPath(root))
		if err != nil {
			t.Fatalf("read messaging.json: %v", err)
		}
		if !bytes.HasSuffix(data, []byte("\n")) {
			t.Errorf("messaging.json must end with a newline; got %q", data)
		}
		if !bytes.Contains(data, []byte("\n  \"groups\": {")) {
			t.Errorf("messaging.json must be indented two spaces like its siblings; got %s", data)
		}
	})

	t.Run("a group member absent from agents.json is rejected and the file is untouched", func(t *testing.T) {
		root := setupConfigFactory(t)

		if _, err := runConfigSet(t, runConfigMessagingSet, `{"groups":{"all":["manager"]}}`); err != nil {
			t.Fatalf("seeding a valid config: %v", err)
		}
		before, err := os.ReadFile(config.MessagingConfigPath(root))
		if err != nil {
			t.Fatalf("read seeded config: %v", err)
		}

		out, err := runConfigSet(t, runConfigMessagingSet, `{"groups":{"all":["manager","ghost"]}}`)
		if err == nil {
			t.Fatal("a group naming an agent absent from agents.json must be rejected")
		}
		// The rejection has to name BOTH coordinates, or an operator with many groups cannot
		// tell which line to fix.
		for _, want := range []string{"ghost", "all"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("rejection must name %q; got %q", want, err.Error())
			}
		}
		if strings.Contains(out, "saved") {
			t.Errorf("a rejected write must not print a success message; got %q", out)
		}
		after, _ := os.ReadFile(config.MessagingConfigPath(root))
		if !bytes.Equal(before, after) {
			t.Errorf("messaging.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
		}
	})

	t.Run("a rejected write creates nothing on a fresh factory", func(t *testing.T) {
		// The seeded case alone would still pass if the guard ran AFTER a write that happened
		// to reproduce the same bytes — the same reasoning the statusline reject test records.
		fresh := setupConfigFactory(t)
		if _, err := runConfigSet(t, runConfigMessagingSet, `{"groups":{"all":["ghost"]}}`); err == nil {
			t.Fatal("a dangling member must be rejected on a fresh factory too")
		}
		if _, err := os.Stat(config.MessagingConfigPath(fresh)); !os.IsNotExist(err) {
			t.Errorf("a rejected write created messaging.json on a fresh factory (stat err = %v)", err)
		}
	})

	t.Run("an unreadable agents.json is fatal, matching dispatch", func(t *testing.T) {
		root := setupConfigFactory(t)
		if err := os.Remove(config.AgentsConfigPath(root)); err != nil {
			t.Fatalf("remove agents.json: %v", err)
		}

		// api.md:130-134: messaging references agents just as dispatch does, so the dispatch
		// precedent (agents.json load failure IS fatal, config_set.go:134-137) is the correct
		// and consistent choice — a messaging write must not proceed uncross-checked.
		_, err := runConfigSet(t, runConfigMessagingSet, `{"groups":{"all":["manager"]}}`)
		if err == nil {
			t.Fatal("an unreadable agents.json must be fatal for a messaging write, as it is for dispatch")
		}
		if !strings.Contains(err.Error(), "agents.json") {
			t.Errorf("the error must name agents.json so the operator knows what to repair; got %q", err.Error())
		}
		if _, err := os.Stat(config.MessagingConfigPath(root)); !os.IsNotExist(err) {
			t.Errorf("messaging.json must not be written when the cross-check cannot run")
		}
	})

	t.Run("an empty group set is legal", func(t *testing.T) {
		root := setupConfigFactory(t)
		if _, err := runConfigSet(t, runConfigMessagingSet, `{"groups":{}}`); err != nil {
			t.Fatalf("an explicit empty group set must remain valid; got %v", err)
		}
		data, err := os.ReadFile(config.MessagingConfigPath(root))
		if err != nil {
			t.Fatalf("read messaging.json: %v", err)
		}
		// Never null: a null groups map reloads as "no groups" but reads to an operator as a
		// broken document, and it is the shape SaveMessagingConfig would emit if the cmd layer
		// forgot to normalize (validateMessagingConfig, which normalizes, cannot run on the
		// save path — it needs an *AgentConfig that ADR-004 keeps out of internal/config).
		if bytes.Contains(data, []byte("null")) {
			t.Errorf("an empty group set must marshal as {} not null; got %s", data)
		}
	})

	t.Run("a document with no groups key is rejected rather than silently erasing every group", func(t *testing.T) {
		root := setupConfigFactory(t)
		if _, err := runConfigSet(t, runConfigMessagingSet, `{"groups":{"all":["manager"]}}`); err != nil {
			t.Fatalf("seeding a valid config: %v", err)
		}
		before, _ := os.ReadFile(config.MessagingConfigPath(root))

		// Same failure mode as the statusline nil-"elements" guard: on the LOAD path an absent
		// key is tolerable, but on the WRITE path it silently erases the operator's groups.
		// DisallowUnknownFields does not catch this — {} has no unknown field.
		if _, err := runConfigSet(t, runConfigMessagingSet, `{}`); err == nil {
			t.Fatal(`a document with no "groups" key must be rejected, never treated as "delete every group"`)
		}
		after, _ := os.ReadFile(config.MessagingConfigPath(root))
		if !bytes.Equal(before, after) {
			t.Errorf("messaging.json was modified on a rejected write:\nbefore=%s\nafter=%s", before, after)
		}
	})
}

// TestSaveMessagingConfig_StaysPureOfAgentsJson is the ADR-004 tripwire for the messaging
// half, mirroring TestSaveStartupConfig_StaysPureOfAgentsJson. internal/config must not reach
// for agents.json, so the group cross-check lives in the cmd layer and the saver stays a pure
// write. The saver COULD derive a factory root from its path argument and load agents.json —
// this fails if anyone ever does, rather than trusting the signature to discourage it.
func TestSaveMessagingConfig_StaysPureOfAgentsJson(t *testing.T) {
	path := filepath.Join(t.TempDir(), "messaging.json")
	cfg := &config.MessagingConfig{Groups: map[string][]string{"all": {"ghost"}}}

	if err := config.SaveMessagingConfig(path, cfg); err != nil {
		t.Fatalf("SaveMessagingConfig must not cross-check against agents.json (there is none here); got %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("the document must have been written; got %v", err)
	}
}

// TestConfigMessagingSet_RegisteredUnderConfig mirrors the sibling registration tests
// (TestConfigModelsSet_RegisteredUnderConfig, TestConfigStatuslineSet_RegisteredUnderConfig):
// a handler nobody can reach is not a seam.
func TestConfigMessagingSet_RegisteredUnderConfig(t *testing.T) {
	findChild := func(parent *cobra.Command, name string) *cobra.Command {
		for _, c := range parent.Commands() {
			if c.Name() == name {
				return c
			}
		}
		return nil
	}
	messaging := findChild(configCmd, "messaging")
	if messaging == nil || findChild(messaging, "set") == nil {
		t.Error("`config messaging set` is not registered under configCmd")
	}
}
