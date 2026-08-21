package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestCheckedInLitellmCarriesClaudeAliases is the CI guard that closes GAP A (issue #598, Phase 3c):
// nothing else in the repo reads .agentfactory/litellm.yaml, so the checked-in gateway config could
// silently fall behind the claude-* ids the checked-in direct profiles name — the design's
// "self-catching" hard-fail only fires when an operator runs `af config models check` against the
// live gateway. This turns that into a build-time guard.
//
// It reads BOTH real files and derives the demanded set from PRODUCTION code
// (directProfileClaudeIDs) over the real registry, so adding a claude-* direct profile to
// .agentfactory/models.json fails this test until .agentfactory/litellm.yaml grows a matching alias.
func TestCheckedInLitellmCarriesClaudeAliases(t *testing.T) {
	moduleRoot := findModuleRoot(t)

	yamlBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", "litellm.yaml"))
	if err != nil {
		t.Fatalf("read checked-in .agentfactory/litellm.yaml: %v", err)
	}
	entries := parseLitellmSeedEntries(t, string(yamlBytes))

	regBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", "models.json"))
	if err != nil {
		t.Fatalf("read checked-in .agentfactory/models.json: %v", err)
	}
	var reg config.ModelsConfig
	if err := json.Unmarshal(regBytes, &reg); err != nil {
		t.Fatalf("parse checked-in .agentfactory/models.json: %v", err)
	}

	demanded := directProfileClaudeIDs(&reg)
	if len(demanded) == 0 {
		t.Fatal("the checked-in registry names no claude-* id at all, so every assertion below would " +
			"pass on an empty set — the direct profiles are what make the gateway aliases necessary")
	}

	aliasBackend := map[string]string{}
	servedLane := map[string]bool{}
	for _, e := range entries {
		if strings.HasPrefix(e.name, "claude-") {
			aliasBackend[e.name] = e.backend
			continue
		}
		servedLane[e.name] = true
	}

	for _, id := range demanded {
		backend, ok := aliasBackend[id]
		if !ok {
			t.Errorf("checked-in .agentfactory/litellm.yaml advertises no %q alias — a direct profile in "+
				".agentfactory/models.json names it, so the gateway refuses a host request for that id and "+
				"`af config models check` hard-fails on this factory. Add `- model_name: %s` routed to the "+
				"main backend. (This may also fire because an operator added a claude-* profile via "+
				"`af config models set` without updating the gateway config.)", id, id)
			continue
		}
		// backend is like "openai/gpt-5.6-sol"; the served lane is the segment after the provider slash.
		lane := backend[strings.LastIndex(backend, "/")+1:]
		if !servedLane[lane] {
			t.Errorf("alias %q routes to %q, which is not a served backend in this litellm.yaml's model_list "+
				"— an alias must point at a real (largest-window) backend or the gateway still refuses the request", id, backend)
		}
	}
}
