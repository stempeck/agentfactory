package formula

import (
	"strconv"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func TestValidateAgents_RejectsNilRegistry(t *testing.T) {
	f := &Formula{Name: "x"}
	err := f.ValidateAgents(nil)
	if err == nil {
		t.Fatal("expected error for nil registry, got nil")
	}
	if !strings.Contains(err.Error(), "non-nil agent registry") {
		t.Errorf("err = %q, want it to contain %q", err.Error(), "non-nil agent registry")
	}
}

func TestValidateAgents_RejectsUnknownAgent(t *testing.T) {
	f := &Formula{
		Name: "testformula",
		Steps: []Step{
			{ID: "step1", Agent: "ghost"},
		},
	}
	registry := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"manager":    {},
			"supervisor": {},
		},
	}
	err := f.ValidateAgents(registry)
	if err == nil {
		t.Fatal("expected error for unknown step agent, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, `step "step1": unknown agent "ghost"`) {
		t.Errorf("err = %q, want it to contain %q", msg, `step "step1": unknown agent "ghost"`)
	}
	if !strings.Contains(msg, "manager") {
		t.Errorf("err = %q, want it to contain %q", msg, "manager")
	}
	if !strings.Contains(msg, "supervisor") {
		t.Errorf("err = %q, want it to contain %q", msg, "supervisor")
	}
}

func TestValidateAgents_AcceptsKnownAgent(t *testing.T) {
	f := &Formula{
		Name: "x",
		Steps: []Step{
			{ID: "step1", Agent: "manager"},
		},
	}
	registry := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"manager": {},
		},
	}
	if err := f.ValidateAgents(registry); err != nil {
		t.Errorf("ValidateAgents returned unexpected error: %v", err)
	}
}

func TestValidateAgents_EmptyAgentOK(t *testing.T) {
	f := &Formula{
		Name: "x",
		Steps: []Step{
			{ID: "step1"},
		},
	}
	registry := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"manager": {},
		},
	}
	if err := f.ValidateAgents(registry); err != nil {
		t.Errorf("ValidateAgents returned unexpected error for empty-agent formula: %v", err)
	}
}

func TestValidateAgents_ErrorMessageLimitsList(t *testing.T) {
	// describeKnown switches from full list to summary form when len(known) > 10.
	// Construct a 20-agent registry and assert the error message uses the summary
	// phrase rather than enumerating all 20.
	known := make(map[string]config.AgentEntry, 20)
	for i := 1; i <= 20; i++ {
		known["agent"+strconv.Itoa(i)] = config.AgentEntry{}
	}
	registry := &config.AgentConfig{Agents: known}
	f := &Formula{
		Name: "x",
		Steps: []Step{
			{ID: "step1", Agent: "ghost"},
		},
	}
	err := f.ValidateAgents(registry)
	if err == nil {
		t.Fatal("expected error for bogus agent against 20-agent registry, got nil")
	}
	if !strings.Contains(err.Error(), "20 known agents") {
		t.Errorf("err = %q, want it to contain %q (summary form, not full list)", err.Error(), "20 known agents")
	}
}

func TestValidateAgents_FormulaLevelAgentChecked(t *testing.T) {
	f := &Formula{Name: "X", Agent: "ghost"}
	registry := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"manager": {},
		},
	}
	err := f.ValidateAgents(registry)
	if err == nil {
		t.Fatal("expected error for unknown formula-level agent, got nil")
	}
	if !strings.Contains(err.Error(), `formula "X": unknown agent "ghost"`) {
		t.Errorf("err = %q, want it to contain %q", err.Error(), `formula "X": unknown agent "ghost"`)
	}
}

