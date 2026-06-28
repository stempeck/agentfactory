package config

import "testing"

// TestValidateDispatchConfig_EmptyNotify_NoManagerAgent_Accepted locks in the T1 fix:
// an EMPTY notify_on_complete must remain valid even when no "manager" agent exists.
// The validator must require the notify agent to exist only when it is EXPLICITLY set;
// otherwise a factory without a manager agent (but an otherwise-valid dispatch.json) is
// wrongly blocked from every `af config dispatch set` / web PUT /api/settings/dispatch.
func TestValidateDispatchConfig_EmptyNotify_NoManagerAgent_Accepted(t *testing.T) {
	agents := &AgentConfig{Agents: map[string]AgentEntry{
		"debugger": {Type: "autonomous", Description: "d"},
	}} // deliberately NO "manager"
	disp := &DispatchConfig{
		Repos:        []string{"owner/repo"},
		TriggerLabel: "agentic",
		Mappings:     []DispatchMapping{{Labels: []string{"bug"}, Agent: "debugger"}},
		// NotifyOnComplete intentionally left empty: it defaults to "manager" at
		// runtime, but an absent manager must not fail validation.
	}
	if err := ValidateDispatchConfig(disp, agents); err != nil {
		t.Fatalf("empty notify_on_complete with no manager agent must be accepted, got: %v", err)
	}

	// Guard: an EXPLICITLY set, non-existent notify agent is still rejected.
	dispExplicit := &DispatchConfig{
		Repos:            []string{"owner/repo"},
		TriggerLabel:     "agentic",
		Mappings:         []DispatchMapping{{Labels: []string{"bug"}, Agent: "debugger"}},
		NotifyOnComplete: "ghost",
	}
	if err := ValidateDispatchConfig(dispExplicit, agents); err == nil {
		t.Fatal("explicitly set notify_on_complete=ghost (absent) must still be rejected")
	}
}
