package config

import (
	"encoding/json"
	"strings"
	"testing"
)

// K1: the baked-in default with a discovered repo must be a fully valid dispatch
// config — it parses AND passes the struct-level validator (LoadDispatchConfig) and
// carries exactly the four mappings + the feature-workflow the design specifies.
func TestDefaultDispatchConfigJSON_WithRepo_Valid(t *testing.T) {
	js := DefaultDispatchConfigJSON("acme/widget")
	dir := writeDispatchJSON(t, js)
	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("default dispatch config must load+validate, got: %v\njson: %s", err, js)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0] != "acme/widget" {
		t.Errorf("repos = %v, want [acme/widget]", cfg.Repos)
	}
	if cfg.TriggerLabel != "agentic" {
		t.Errorf("trigger_label = %q, want agentic", cfg.TriggerLabel)
	}
	if cfg.IntervalSecs != 300 {
		t.Errorf("interval_seconds = %d, want 300", cfg.IntervalSecs)
	}
	if cfg.RetryAfterSecs != 1800 {
		t.Errorf("retry_after_seconds = %d, want 1800", cfg.RetryAfterSecs)
	}
	if !cfg.RemoveTriggerAfterDispatch {
		t.Errorf("remove_trigger_after_dispatch = false, want true")
	}
	if len(cfg.Mappings) != 4 {
		t.Fatalf("mappings = %d, want 4", len(cfg.Mappings))
	}
	wantAgents := map[string]string{
		"rapid-plan":     "rapid-soldesign-plan",
		"rapid-engineer": "rapid-implement",
		"pr-review":      "ultra-review",
		"pr-iterate":     "rapid-increment",
	}
	gotAgents := map[string]string{}
	for _, m := range cfg.Mappings {
		if len(m.Labels) != 1 {
			t.Errorf("mapping %v should have exactly one label", m)
			continue
		}
		gotAgents[m.Labels[0]] = m.Agent
	}
	for label, agent := range wantAgents {
		if gotAgents[label] != agent {
			t.Errorf("mapping label %q -> %q, want %q", label, gotAgents[label], agent)
		}
	}
	if len(cfg.Workflows) != 1 || cfg.Workflows[0].Label != "feature-workflow" {
		t.Fatalf("workflows = %v, want one feature-workflow", cfg.Workflows)
	}
	if got := cfg.Workflows[0].Phases; len(got) != 2 || got[0] != "rapid-plan" || got[1] != "rapid-engineer" {
		t.Errorf("feature-workflow phases = %v, want [rapid-plan rapid-engineer]", got)
	}
}

// K1: notify_on_complete is omitted from the default (Gap-7) — it defaults to
// "manager" at runtime, so an explicit value would add a brittle cross-file check.
func TestDefaultDispatchConfigJSON_OmitsNotifyOnComplete(t *testing.T) {
	js := DefaultDispatchConfigJSON("acme/widget")
	if strings.Contains(js, "notify_on_complete") {
		t.Errorf("default must OMIT notify_on_complete (Gap-7); json: %s", js)
	}
}

// K1: pr-source mappings must carry source "pr"; issue-source mappings "issue".
func TestDefaultDispatchConfigJSON_MappingSources(t *testing.T) {
	var disp DispatchConfig
	if err := json.Unmarshal([]byte(DefaultDispatchConfigJSON("a/b")), &disp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	wantSource := map[string]string{
		"rapid-plan": "issue", "rapid-engineer": "issue",
		"pr-review": "pr", "pr-iterate": "pr",
	}
	for _, m := range disp.Mappings {
		if len(m.Labels) == 1 && m.Source != wantSource[m.Labels[0]] {
			t.Errorf("mapping %q source = %q, want %q", m.Labels[0], m.Source, wantSource[m.Labels[0]])
		}
	}
}

// K1 fallback: an empty repo degrades to the loadable empty shape (status quo), not
// a repos:[] + 4-mapping config (which would fail validateDispatchConfig's repo>0 rule
// and break the dispatcher). The dispatcher treats this as "not configured".
func TestDefaultDispatchConfigJSON_EmptyRepo_DegradesToEmpty(t *testing.T) {
	var disp DispatchConfig
	if err := json.Unmarshal([]byte(DefaultDispatchConfigJSON("")), &disp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(disp.Repos) != 0 {
		t.Errorf("empty-repo default must have repos:[], got %v", disp.Repos)
	}
	if len(disp.Mappings) != 0 {
		t.Errorf("empty-repo default must have mappings:[] (else repos:[]+mappings fails to load); got %v", disp.Mappings)
	}
}

// K5: the default agents.json seeds manager + supervisor + the four specialists, each
// VALID (validateAgentConfig requires a non-empty description), with the specialists
// formula-bearing so the dispatch default cross-validates.
func TestDefaultAgentsConfigJSON_SeedsSpecialists(t *testing.T) {
	var ac AgentConfig
	if err := json.Unmarshal([]byte(DefaultAgentsConfigJSON()), &ac); err != nil {
		t.Fatalf("unmarshal default agents.json: %v", err)
	}
	// Must pass the real validator (valid-by-construction).
	if err := validateAgentConfig(&ac); err != nil {
		t.Fatalf("default agents.json must be valid-by-construction, got: %v", err)
	}
	if e := ac.Agents["manager"]; e.Type != "interactive" || e.Description == "" {
		t.Errorf("manager = %+v, want interactive with a description", e)
	}
	if e := ac.Agents["supervisor"]; e.Type != "autonomous" || e.Description == "" {
		t.Errorf("supervisor = %+v, want autonomous with a description", e)
	}
	specialists := map[string]string{
		"rapid-soldesign-plan": "rapid-soldesign-plan",
		"rapid-implement":      "rapid-implement",
		"ultra-review":         "ultra-review",
		"rapid-increment":      "rapid-increment",
	}
	for name, formula := range specialists {
		e, ok := ac.Agents[name]
		if !ok {
			t.Errorf("default agents.json must seed specialist %q", name)
			continue
		}
		if e.Type != "autonomous" {
			t.Errorf("specialist %q type = %q, want autonomous", name, e.Type)
		}
		if e.Formula != formula {
			t.Errorf("specialist %q formula = %q, want %q", name, e.Formula, formula)
		}
		if e.Description == "" {
			t.Errorf("specialist %q must have a non-empty description (validateAgentConfig requires it)", name)
		}
	}
}

// K7 golden/cross-file: the shipped default parses (struct-level) AND cross-validates
// (ValidateDispatchConfig) against the default-seeded agents.json — proving the bare
// `af install --init` factory is valid-by-construction (the cross-review C1 guarantee).
func TestDefaultDispatchConfigJSON_CrossValidatesWithDefaultAgents(t *testing.T) {
	dir := writeDispatchJSON(t, DefaultDispatchConfigJSON("acme/widget"))
	disp, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("struct-level validation failed: %v", err)
	}
	var agents AgentConfig
	if err := json.Unmarshal([]byte(DefaultAgentsConfigJSON()), &agents); err != nil {
		t.Fatalf("unmarshal default agents.json: %v", err)
	}
	if err := ValidateDispatchConfig(disp, &agents); err != nil {
		t.Fatalf("default dispatch must cross-validate against default agents.json (C1/C-6): %v", err)
	}
}
