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
