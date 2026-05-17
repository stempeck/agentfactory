package templates

import (
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	tmpl := New()
	if tmpl == nil {
		t.Fatal("New() returned nil")
	}
}

func TestRenderRole_Manager(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "manager") {
		t.Error("output should contain role name 'manager'")
	}
	if !strings.Contains(output, "Factory coordinator") {
		t.Error("output should contain description")
	}
}

func TestRenderRole_Supervisor(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "supervisor",
		Description: "Autonomous worker",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/supervisor",
	}
	output, err := tmpl.RenderRole("supervisor", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "supervisor") {
		t.Error("output should contain role name 'supervisor'")
	}
	if !strings.Contains(strings.ToLower(output), "autonomous") {
		t.Error("supervisor template should mention 'autonomous'")
	}
}

func TestRenderRole_UnknownRole(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "unknown",
		Description: "test",
		RootDir:     "/tmp",
		WorkDir:     "/tmp",
	}
	_, err := tmpl.RenderRole("unknown", data)
	if err == nil {
		t.Fatal("RenderRole should return error for unknown role")
	}
}

func TestRenderRole_AllFieldsSubstituted(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if strings.Contains(output, "{{ .") {
		t.Error("output contains unresolved template variables")
	}
}



func TestManagerTemplate_HasBehavioralSections(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Interactive agent for human-supervised work",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}

	requiredSections := []string{
		"## Role Boundary",
		"## Specialist Catalog",
		"## Behavioral Discipline",
		"## Failure Modes",
		"## Anti-Patterns to Avoid",
		"## Escalation Protocol",
	}
	for _, section := range requiredSections {
		if !strings.Contains(output, section) {
			t.Errorf("manager template missing required section: %s", section)
		}
	}

	preservedSections := []string{
		"## Mail Protocol",
		"## Constraints",
		"## Startup Protocol",
	}
	for _, section := range preservedSections {
		if !strings.Contains(output, section) {
			t.Errorf("manager template lost existing section: %s", section)
		}
	}

	if !strings.Contains(output, "| Situation | Action |") {
		t.Error("Failure Modes section missing '| Situation | Action |' table header")
	}

	if !strings.Contains(output, "| Anti-Pattern | Prevention |") {
		t.Error("Anti-Patterns section missing '| Anti-Pattern | Prevention |' table header")
	}

	if strings.Count(output, "af sling") < 2 {
		t.Error("manager template should contain at least 2 references to 'af sling'")
	}

	if !strings.Contains(output, "routine operational tasks") {
		t.Error("Startup Protocol step 2 should contain 'routine operational tasks'")
	}
}

func TestManagerTemplate_ContainsMonitoringSection(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "## Monitoring Dispatched Work") {
		t.Error("manager template should contain '## Monitoring Dispatched Work' section")
	}
	if !strings.Contains(output, "capture-pane") {
		t.Error("manager template should contain 'capture-pane' monitoring mechanism")
	}
}

func TestManagerTemplate_ReferencesAgentsMD(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "## Specialist Catalog") {
		t.Error("manager template should contain '## Specialist Catalog' section")
	}
	if !strings.Contains(output, "AGENTS.md") {
		t.Error("manager template should reference AGENTS.md for dynamic agent catalog")
	}
}

func TestManagerTemplate_NoHardcodedAgentNames(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	hardcodedAgents := []string{"| rootcause-all |", "| design-v7 |", "| ultra-implement |"}
	for _, agent := range hardcodedAgents {
		if strings.Contains(output, agent) {
			t.Errorf("manager template should NOT hardcode agent names, found: %s", agent)
		}
	}
}

func TestManagerTemplate_MonitoringIncludesFollowUpProtocol(t *testing.T) {
	tmpl := New()
	data := RoleData{
		Role:        "manager",
		Description: "Factory coordinator",
		RootDir:     "/home/dev/factory",
		WorkDir:     "/home/dev/factory/manager",
	}
	output, err := tmpl.RenderRole("manager", data)
	if err != nil {
		t.Fatalf("RenderRole failed: %v", err)
	}
	if !strings.Contains(output, "af-<agent>") {
		t.Error("monitoring section should show session naming convention 'af-<agent>'")
	}
	if !strings.Contains(output, "progress") {
		t.Error("monitoring section should include guidance on checking agent progress")
	}
}

func TestHasRole(t *testing.T) {
	tmpl := New()

	if !tmpl.HasRole("manager") {
		t.Error("HasRole should return true for manager")
	}
	if !tmpl.HasRole("supervisor") {
		t.Error("HasRole should return true for supervisor")
	}
	if tmpl.HasRole("nonexistent") {
		t.Error("HasRole should return false for nonexistent role")
	}
}
