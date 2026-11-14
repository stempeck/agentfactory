package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestSeedWorthinessOfTrackedConfigs is the T9 gate (issue #654 Phase 1, Gap 9):
// the live tracked .agentfactory/*.json set must (a) strict-decode cleanly against
// the production config structs — no unknown top-level key, catching drift between
// this factory's live config and the schema af actually understands — and (b)
// resolve as a COHERENT set: every agent name referenced from dispatch.json,
// startup.json, messaging.json and models.json must exist in agents.json, and
// every agents.json formula reference must resolve against the embedded
// install_formulas/ set. It reads the live tracked set dynamically (map keys,
// embedded directory listing) — never a hardcoded agent/config count, since the
// roster grows over time.
//
// litellm.yaml is deliberately excluded from the strict-decode half: it is YAML,
// not JSON, and the root module ships no YAML library (ADR-013 forbids adding
// one — see TestGoModDirectRequireSetPinned). It is seeded byte-for-byte, so here
// it gets only the same non-empty/well-formed check parseLitellmSeedEntries already
// performs for TestCheckedInLitellmCarriesClaudeAliases.
func TestSeedWorthinessOfTrackedConfigs(t *testing.T) {
	const root = "../.."

	// 1. Strict-decode the 6 JSON configs — unknown top-level keys are rejected.
	strictTargets := []struct {
		name string
		v    any
	}{
		{"factory.json", &config.FactoryConfig{}},
		{"agents.json", &config.AgentConfig{}},
		{"messaging.json", &config.MessagingConfig{}},
		{"dispatch.json", &config.DispatchConfig{}},
		{"startup.json", &config.StartupConfig{}},
		{"models.json", &config.ModelsConfig{}},
	}
	for _, tgt := range strictTargets {
		path := filepath.Join(root, ".agentfactory", tgt.name)
		if err := strictDecodeConfigFile(path, tgt.v); err != nil {
			t.Errorf("%s: %v", tgt.name, err)
		}
	}

	// 2. litellm.yaml well-formedness — no schema decode (YAML, no library, ADR-013
	// forbids adding one); parseLitellmSeedEntries (quickstart_provisioning_shape_test.go)
	// already t.Fatals if the file advertises no model_name at all — exactly the bar
	// litellm.yaml can be held to without a YAML schema.
	litellmPath := filepath.Join(root, ".agentfactory", "litellm.yaml")
	litellmData, err := os.ReadFile(litellmPath)
	if err != nil {
		t.Fatalf("read %s: %v", litellmPath, err)
	}
	parseLitellmSeedEntries(t, string(litellmData))

	// 3. Cross-check the tracked set is a coherent whole: every agent name
	// referenced from dispatch.json / startup.json / messaging.json / models.json
	// resolves in agents.json, and every agents.json formula reference resolves
	// against the embedded install_formulas/ set.
	agentsPath := config.AgentsConfigPath(root)
	agents, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		t.Fatalf("load %s: %v", agentsPath, err)
	}

	models, err := config.LoadModelsConfig(root)
	if err != nil {
		t.Fatalf("load models.json: %v", err)
	}

	dispatch, err := config.LoadDispatchConfig(root)
	if err != nil {
		t.Fatalf("load dispatch.json: %v", err)
	}
	if err := config.ValidateDispatchConfig(dispatch, agents, models); err != nil {
		t.Errorf("dispatch.json coherence: %v", err)
	}
	// ValidateDispatchConfig does not resolve Crons[].Agent against agents.json
	// (it only checks Mappings[].Agent) — close that gap explicitly so "every
	// agent referenced ... resolves" is actually enforced, not just the subset
	// the named helper happens to cover.
	for _, c := range dispatch.Crons {
		if _, ok := agents.Agents[c.Agent]; !ok {
			t.Errorf("dispatch.json cron %q references unknown agent %q", c.Name, c.Agent)
		}
	}

	startup, err := config.LoadStartupConfig(root)
	if err != nil {
		t.Fatalf("load startup.json: %v", err)
	}
	if err := rejectUnknownStartupAgents(startup, agents); err != nil {
		t.Errorf("startup.json coherence: %v", err)
	}

	messagingPath := config.MessagingConfigPath(root)
	messaging, err := config.LoadMessagingConfig(messagingPath, agents)
	if err != nil {
		t.Fatalf("load messaging.json: %v", err)
	}
	if err := rejectUnknownGroupMembers(messaging, agents); err != nil {
		t.Errorf("messaging.json coherence: %v", err)
	}

	// models.json Agents map KEYS (agent names) vs agents.json — the direction
	// validateModelsConfig does not check (it only checks the map's VALUES, the
	// model names, against Models).
	var unknownModelAgents []string
	for agentName := range models.Agents {
		if _, ok := agents.Agents[agentName]; !ok {
			unknownModelAgents = append(unknownModelAgents, agentName)
		}
	}
	if len(unknownModelAgents) > 0 {
		sort.Strings(unknownModelAgents)
		t.Errorf("models.json pins a model for unknown agent(s): %v", unknownModelAgents)
	}

	// agents.json formula refs vs the embedded install_formulas/ set. A formula
	// field is a bare name; the embedded manifest stores it with a
	// ".formula.toml" suffix (writeFormulas' entry.Name() idiom). An ABSENT
	// formula field is valid for any agent type (e.g. manager, supervisor) — only
	// a present-but-unresolvable value is a dangling ref.
	formulaEntries, err := formulasFS.ReadDir("install_formulas")
	if err != nil {
		t.Fatalf("read embedded install_formulas: %v", err)
	}
	knownFormulas := map[string]bool{}
	for _, e := range formulaEntries {
		knownFormulas[e.Name()] = true
	}
	var danglingFormulas []string
	for name, entry := range agents.Agents {
		if entry.Formula == "" {
			continue
		}
		if !knownFormulas[entry.Formula+".formula.toml"] {
			danglingFormulas = append(danglingFormulas, fmt.Sprintf("%s -> %s", name, entry.Formula))
		}
	}
	if len(danglingFormulas) > 0 {
		sort.Strings(danglingFormulas)
		t.Errorf("agents.json names formula(s) with no matching install_formulas/ entry: %v", danglingFormulas)
	}
}

// strictDecodeConfigFile reads path and decodes it into v with unknown top-level
// keys rejected — the decodeJSONStdin idiom (config_set.go), retargeted from
// cmd.InOrStdin() to a file on disk.
func strictDecodeConfigFile(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open: %w", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("strict decode: %w", err)
	}
	return nil
}
