package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSeededDirectives_ManagerAndSupervisorNameRealMemoryVerbs pins that the
// directives a fresh factory seeds for manager and supervisor point at the vault through an af
// memory verb the cobra tree actually has. It reads the agents.json a fresh seed writes, because
// that is what `af install --init` leaves behind.
func TestSeededDirectives_ManagerAndSupervisorNameRealMemoryVerbs(t *testing.T) {
	configDir := filepath.Join(t.TempDir(), ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := seedStarterConfigs(io.Discard, io.Discard, configDir, "/nonexistent/no-source", true); err != nil {
		t.Fatalf("seedStarterConfigs: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, "agents.json"))
	if err != nil {
		t.Fatalf("read seeded agents.json: %v", err)
	}
	var manifest struct {
		Agents map[string]struct {
			Directive string `json:"directive"`
		} `json:"agents"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		t.Fatalf("decode seeded agents.json: %v", err)
	}

	for _, name := range []string{"manager", "supervisor"} {
		entry, ok := manifest.Agents[name]
		if !ok {
			t.Errorf("seeded agents.json has no %q agent", name)
			continue
		}
		memoryRefs := 0
		for _, ref := range extractAFRefs("agents.json:"+name, entry.Directive) {
			if err := resolveAFRef(ref); err != nil {
				t.Errorf("%s directive names a verb the cobra tree does not have: %v", name, err)
			}
			if len(ref.path) >= 2 && ref.path[0] == "memory" {
				memoryRefs++
			}
		}
		if memoryRefs == 0 {
			t.Errorf("%s directive %q names no `af memory` verb; #515 wired these directives to the vault",
				name, entry.Directive)
		}
	}
}

// af's own seed must never trip the stale-directive warning `af up` raises over a live agents.json.
func TestBuiltinRoleDirectiveOrdersNoFullList(t *testing.T) {
	if strings.Contains(builtinRoleDirective, "(af memory list)") {
		t.Errorf("builtinRoleDirective %q orders a full `af memory list` read", builtinRoleDirective)
	}
	if staleRoleDirective(builtinRoleDirective) {
		t.Errorf("staleRoleDirective(builtinRoleDirective) = true for %q; af up would warn on af's own seed",
			builtinRoleDirective)
	}
	const fullListDirective = "Read your memory (af memory list) and docs, and prove it. Record durable learnings with af memory add."
	if !staleRoleDirective(fullListDirective) {
		t.Errorf("staleRoleDirective(%q) = false; the negative above proves nothing if the predicate never fires",
			fullListDirective)
	}
}
