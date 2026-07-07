package config

import (
	"strings"
	"testing"
)

// TestValidateDispatchConfig_UnknownModelProfile_Fails (issue #480): a
// dispatch mapping whose Model names a profile absent from models.json must be
// rejected by ValidateDispatchConfig — mirroring the unknown-agent cross-check — so
// a typo'd profile is caught at the write/start boundary, not silently at sling
// time. A nil models argument must skip the cross-check (back-compat).
func TestValidateDispatchConfig_UnknownModelProfile_Fails(t *testing.T) {
	agents := &AgentConfig{Agents: map[string]AgentEntry{
		"debugger": {Type: "autonomous", Description: "d"},
		"manager":  {Type: "interactive", Description: "m"},
	}}
	models := &ModelsConfig{
		Models: map[string]map[string]string{"opus": {"ANTHROPIC_MODEL": "claude-opus-4-8"}},
	}

	// A mapping pointing at a model profile that does not exist in models.json.
	disp := &DispatchConfig{
		Repos:        []string{"owner/repo"},
		TriggerLabel: "agentic",
		Mappings:     []DispatchMapping{{Labels: []string{"bug"}, Agent: "debugger", Model: "ghost"}},
	}
	err := ValidateDispatchConfig(disp, agents, models)
	if err == nil {
		t.Fatal("ValidateDispatchConfig accepted a mapping naming an undefined model profile")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should name the undefined model %q", err.Error(), "ghost")
	}

	// A mapping naming a DEFINED profile passes.
	good := &DispatchConfig{
		Repos:        []string{"owner/repo"},
		TriggerLabel: "agentic",
		Mappings:     []DispatchMapping{{Labels: []string{"bug"}, Agent: "debugger", Model: "opus"}},
	}
	if err := ValidateDispatchConfig(good, agents, models); err != nil {
		t.Fatalf("a mapping naming a defined model profile must pass, got: %v", err)
	}

	// nil models skips the model cross-check entirely (back-compat): even a mapping
	// with a non-existent profile must not error when no registry is supplied.
	if err := ValidateDispatchConfig(disp, agents, nil); err != nil {
		t.Fatalf("nil models must skip the model cross-check, got: %v", err)
	}
}

// TestValidateDispatchConfig_RawModelId_EmptyRegistry_Passes: with no models.json (or
// one defining zero profiles) a mapping's model is a raw id passed straight to
// `claude --model`, exactly as the launch path treats it — so validation must not
// reject it. Without this carve-out a raw-id mapping aborts the entire dispatch run
// and blocks `af config dispatch set` on factories that never created a models.json
// (PR #482 review).
func TestValidateDispatchConfig_RawModelId_EmptyRegistry_Passes(t *testing.T) {
	agents := &AgentConfig{Agents: map[string]AgentEntry{
		"debugger": {Type: "autonomous", Description: "d"},
	}}
	disp := &DispatchConfig{
		Repos:        []string{"owner/repo"},
		TriggerLabel: "agentic",
		Mappings:     []DispatchMapping{{Labels: []string{"bug"}, Agent: "debugger", Model: "claude-opus-4-8"}},
	}

	// The absent-file shape, produced by the real loader: a NON-nil empty config.
	absent, err := LoadModelsConfig(t.TempDir())
	if err != nil {
		t.Fatalf("LoadModelsConfig on a root without models.json must not error, got: %v", err)
	}
	if absent == nil {
		t.Fatal("LoadModelsConfig contract changed: expected a non-nil empty config for an absent file")
	}
	if err := ValidateDispatchConfig(disp, agents, absent); err != nil {
		t.Errorf("raw model id must pass validation when models.json is absent, got: %v", err)
	}

	// A present models.json that defines zero profiles behaves the same.
	empty := &ModelsConfig{Models: map[string]map[string]string{}}
	if err := ValidateDispatchConfig(disp, agents, empty); err != nil {
		t.Errorf("raw model id must pass validation against a zero-profile registry, got: %v", err)
	}
}
