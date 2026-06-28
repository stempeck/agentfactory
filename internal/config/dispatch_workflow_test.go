package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeDispatchJSON writes body to <tmp>/.agentfactory/dispatch.json and returns
// the factory root, matching the temp-dir convention used across dispatch_test.go.
func writeDispatchJSON(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	cfgDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(cfgDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfgDir, "dispatch.json"), []byte(body), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// --- Positive (keystone) tests: a valid/absent workflow must load unchanged ---

func TestLoadDispatchConfig_AbsentWorkflows_LoadsUnchanged(t *testing.T) {
	dir := writeDispatchJSON(t, `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"labels": ["build"], "agent": "builder"}]
	}`)
	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Workflows) != 0 {
		t.Errorf("expected no workflows (truthful absent default), got %d", len(cfg.Workflows))
	}
}

func TestLoadDispatchConfig_ValidWorkflow_Loads(t *testing.T) {
	dir := writeDispatchJSON(t, `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [
			{"labels": ["build"], "agent": "builder"},
			{"labels": ["test"], "agent": "tester"}
		],
		"workflows": [
			{"label": "ship", "phases": ["build", "test"]}
		]
	}`)
	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("valid workflow rejected: %v", err)
	}
	if len(cfg.Workflows) != 1 {
		t.Fatalf("expected 1 workflow, got %d", len(cfg.Workflows))
	}
	if cfg.Workflows[0].Label != "ship" {
		t.Errorf("workflow label = %q, want %q", cfg.Workflows[0].Label, "ship")
	}
	if len(cfg.Workflows[0].Phases) != 2 ||
		cfg.Workflows[0].Phases[0] != "build" || cfg.Workflows[0].Phases[1] != "test" {
		t.Errorf("workflow phases = %v, want [build test]", cfg.Workflows[0].Phases)
	}
}

// --- Struct-level negative tests (via LoadDispatchConfig) ---

func TestValidateDispatchConfig_WorkflowPhaseWithoutMapping_Rejected(t *testing.T) {
	dir := writeDispatchJSON(t, `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"labels": ["build"], "agent": "builder"}],
		"workflows": [{"label": "ship", "phases": ["nonexistent"]}]
	}`)
	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for a phase absent from every mapping")
	}
	if !strings.Contains(err.Error(), "is not in any mapping") {
		t.Errorf("error %q should mention 'is not in any mapping'", err.Error())
	}
}

// CRITICAL-2: a phase that appears ONLY inside a multi-label ANDed mapping passes
// a naive membership check but resolves to NO agent at advance time. It must be
// rejected on a distinct message ("on the phase label alone") to prove the rule
// fired for the right reason, not the "not in any mapping" rule.
func TestValidateDispatchConfig_PhaseNotResolvableOnLabelAlone_Rejected(t *testing.T) {
	dir := writeDispatchJSON(t, `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"labels": ["enhancement", "backend"], "agent": "builder"}],
		"workflows": [{"label": "ship", "phases": ["backend"]}]
	}`)
	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error: phase resolvable only via a multi-label mapping")
	}
	if !strings.Contains(err.Error(), "on the phase label alone") {
		t.Errorf("error %q should mention 'on the phase label alone'", err.Error())
	}
}

func TestValidateDispatchConfig_DuplicateWorkflowLabel_Rejected(t *testing.T) {
	dir := writeDispatchJSON(t, `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"labels": ["build"], "agent": "builder"}],
		"workflows": [
			{"label": "ship", "phases": ["build"]},
			{"label": "ship", "phases": ["build"]}
		]
	}`)
	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for duplicate workflow label")
	}
	if !strings.Contains(err.Error(), "duplicate label") {
		t.Errorf("error %q should mention 'duplicate label'", err.Error())
	}
}

// LOW-2: label-in-own-phases, label==trigger, phase==trigger collisions.
func TestValidateDispatchConfig_LabelPhaseTriggerCollision_Rejected(t *testing.T) {
	cases := []struct {
		name     string
		workflow string
		want     string
	}{
		{
			name:     "label appears in its own phases",
			workflow: `{"label": "build", "phases": ["build"]}`,
			want:     "also appears in its phases",
		},
		{
			name:     "label equals trigger_label",
			workflow: `{"label": "agentic", "phases": ["build"]}`,
			want:     "collides with trigger_label",
		},
		{
			name:     "phase equals trigger_label",
			workflow: `{"label": "ship", "phases": ["agentic"]}`,
			want:     "collides with trigger_label",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := `{
				"repos": ["owner/repo"],
				"trigger_label": "agentic",
				"mappings": [{"labels": ["build"], "agent": "builder"}],
				"workflows": [` + tc.workflow + `]
			}`
			_, err := LoadDispatchConfig(writeDispatchJSON(t, body))
			if err == nil {
				t.Fatalf("expected error for %s", tc.name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err.Error(), tc.want)
			}
		})
	}
}

// HIGH-B: a workflow label that is also a mapping label can shadow a phase mapping
// via first-match-wins. The label here is NOT in its own phases, isolating HIGH-B
// from the LOW-2 label-in-phases rule.
func TestValidateDispatchConfig_WorkflowLabelCollidesWithMappingLabel_Rejected(t *testing.T) {
	dir := writeDispatchJSON(t, `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [
			{"labels": ["build"], "agent": "builder"},
			{"labels": ["test"], "agent": "tester"}
		],
		"workflows": [{"label": "build", "phases": ["test"]}]
	}`)
	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error: workflow label is also a mapping label")
	}
	if !strings.Contains(err.Error(), "must not also be a mapping label") {
		t.Errorf("error %q should mention 'must not also be a mapping label'", err.Error())
	}
}

// HIGH-2: phases resolving to mappings with differing source must be rejected in v1
// (cross-source resolution is a Phase-3 feature).
func TestValidateDispatchConfig_MixedSourceWithoutCrossSource_Rejected(t *testing.T) {
	dir := writeDispatchJSON(t, `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [
			{"labels": ["build"], "agent": "builder", "source": "issue"},
			{"labels": ["test"], "agent": "tester", "source": "pr"}
		],
		"workflows": [{"label": "ship", "phases": ["build", "test"]}]
	}`)
	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for a mixed-source workflow")
	}
	if !strings.Contains(err.Error(), "mixed source") {
		t.Errorf("error %q should mention 'mixed source'", err.Error())
	}
}

// --- Cross-file negative/positive tests (direct ValidateDispatchConfig call) ---

// The phase agent exists but has no formula, so it can never signal completion.
// The agent MUST exist in agents.Agents (else the pre-existing "unknown agent"
// rule fires first) — only Formula is empty.
func TestValidateDispatchConfig_WorkflowPhaseAgentHasNoFormula_Rejected(t *testing.T) {
	agents := &AgentConfig{Agents: map[string]AgentEntry{
		"builder": {Type: "autonomous", Description: "b"}, // no formula
		"manager": {Type: "interactive", Description: "m"},
	}}
	disp := &DispatchConfig{
		Repos:        []string{"owner/repo"},
		TriggerLabel: "agentic",
		Mappings:     []DispatchMapping{{Labels: []string{"build"}, Agent: "builder"}},
		Workflows:    []Workflow{{Label: "ship", Phases: []string{"build"}}},
	}
	err := ValidateDispatchConfig(disp, agents)
	if err == nil {
		t.Fatal("expected error: phase agent has no formula")
	}
	if !strings.Contains(err.Error(), "no formula") {
		t.Errorf("error %q should mention 'no formula'", err.Error())
	}
	if !strings.Contains(err.Error(), "builder") {
		t.Errorf("error %q should name the agent 'builder'", err.Error())
	}
}

func TestValidateDispatchConfig_WorkflowPhaseAgentWithFormula_OK(t *testing.T) {
	agents := &AgentConfig{Agents: map[string]AgentEntry{
		"builder": {Type: "autonomous", Description: "b", Formula: "build-flow"},
		"tester":  {Type: "autonomous", Description: "t", Formula: "test-flow"},
	}}
	disp := &DispatchConfig{
		Repos:        []string{"owner/repo"},
		TriggerLabel: "agentic",
		Mappings: []DispatchMapping{
			{Labels: []string{"build"}, Agent: "builder", Source: "issue"},
			{Labels: []string{"test"}, Agent: "tester", Source: "issue"},
		},
		Workflows: []Workflow{{Label: "ship", Phases: []string{"build", "test"}}},
	}
	if err := ValidateDispatchConfig(disp, agents); err != nil {
		t.Fatalf("valid formula-bearing workflow rejected: %v", err)
	}
}
