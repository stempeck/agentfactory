package formula

import (
	"os"
	"path/filepath"
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

func TestValidateSkillNames_Valid(t *testing.T) {
	f := &Formula{
		Name:   "x",
		Type:   TypeWorkflow,
		Steps:  []Step{{ID: "s1"}},
		Skills: []string{"rootcause-analysis", "ultra-implement"},
	}
	if err := f.Validate(); err != nil {
		t.Errorf("Validate returned unexpected error: %v", err)
	}
}

func TestValidateSkillNames_Invalid(t *testing.T) {
	f := &Formula{
		Name:   "x",
		Type:   TypeWorkflow,
		Steps:  []Step{{ID: "s1"}},
		Skills: []string{"../etc", "foo/bar", "", "1bad"},
	}
	err := f.Validate()
	if err == nil {
		t.Fatal("expected error for invalid skill names, got nil")
	}
	msg := err.Error()
	for _, want := range []string{"../etc", "foo/bar", "1bad"} {
		if !strings.Contains(msg, want) {
			t.Errorf("err = %q, want it to contain %q", msg, want)
		}
	}
	if !strings.Contains(msg, "invalid skill name") {
		t.Errorf("err = %q, want it to mention invalid skill name", msg)
	}
}

func TestValidateSkillNames_Duplicates(t *testing.T) {
	f := &Formula{
		Name:   "x",
		Type:   TypeWorkflow,
		Steps:  []Step{{ID: "s1"}},
		Skills: []string{"skill-a", "skill-a"},
	}
	err := f.Validate()
	if err == nil {
		t.Fatal("expected error for duplicate skill names, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "duplicate") {
		t.Errorf("err = %q, want it to contain %q", msg, "duplicate")
	}
	if !strings.Contains(msg, "skill-a") {
		t.Errorf("err = %q, want it to contain %q", msg, "skill-a")
	}
}

func TestValidateSkills_AllPresent(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"rootcause-analysis", "ultra-implement"} {
		skillDir := filepath.Join(dir, name)
		if err := os.MkdirAll(skillDir, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# test"), 0644); err != nil {
			t.Fatal(err)
		}
	}
	f := &Formula{
		Name:   "x",
		Skills: []string{"rootcause-analysis", "ultra-implement"},
	}
	if err := f.ValidateSkills(dir); err != nil {
		t.Errorf("ValidateSkills returned unexpected error: %v", err)
	}
}

func TestValidateSkills_SomeMissing(t *testing.T) {
	dir := t.TempDir()
	skillDir := filepath.Join(dir, "skill-a")
	if err := os.MkdirAll(skillDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(skillDir, "SKILL.md"), []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}
	f := &Formula{
		Name:   "x",
		Skills: []string{"skill-a", "skill-b", "skill-c"},
	}
	err := f.ValidateSkills(dir)
	if err == nil {
		t.Fatal("expected error for missing skills, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, "skill-b") {
		t.Errorf("err = %q, want it to contain %q", msg, "skill-b")
	}
	if !strings.Contains(msg, "skill-c") {
		t.Errorf("err = %q, want it to contain %q", msg, "skill-c")
	}
	notFoundSection := msg
	if idx := strings.Index(msg, "hint:"); idx > 0 {
		notFoundSection = msg[:idx]
	}
	if strings.Contains(notFoundSection, "skill-a") {
		t.Errorf("not-found section = %q, should NOT contain present skill %q", notFoundSection, "skill-a")
	}
}

func TestValidateSkills_EmptySkills(t *testing.T) {
	f := &Formula{Name: "x", Skills: nil}
	if err := f.ValidateSkills("/nonexistent"); err != nil {
		t.Errorf("ValidateSkills(nil skills) returned unexpected error: %v", err)
	}
	f.Skills = []string{}
	if err := f.ValidateSkills("/nonexistent"); err != nil {
		t.Errorf("ValidateSkills(empty skills) returned unexpected error: %v", err)
	}
}

func TestValidateSkills_EmptySkillsDir(t *testing.T) {
	f := &Formula{
		Name:   "x",
		Skills: []string{"something"},
	}
	err := f.ValidateSkills("")
	if err == nil {
		t.Fatal("expected error for empty skillsDir, got nil")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("err = %q, want it to mention empty path", err.Error())
	}
}

func TestDetectSkillInvocations(t *testing.T) {
	t.Run("detects Skill() call pattern", func(t *testing.T) {
		steps := []Step{
			{Description: `Invoke the skill using the Skill tool:
Skill(skill: "rootcause-analysis", args: "todos/IMPLREADME.md")
Do NOT read the SKILL.md.`},
		}
		got := DetectSkillInvocations(steps)
		if len(got) != 1 || got[0] != "rootcause-analysis" {
			t.Errorf("got %v, want [rootcause-analysis]", got)
		}
	})

	t.Run("detects claude -p double quotes", func(t *testing.T) {
		steps := []Step{
			{Description: `claude -p "/design-plan-impl some-args"`},
		}
		got := DetectSkillInvocations(steps)
		if len(got) != 1 || got[0] != "design-plan-impl" {
			t.Errorf("got %v, want [design-plan-impl]", got)
		}
	})

	t.Run("detects claude -p single quotes", func(t *testing.T) {
		steps := []Step{
			{Description: `claude -p '/terraform-fix path'`},
		}
		got := DetectSkillInvocations(steps)
		if len(got) != 1 || got[0] != "terraform-fix" {
			t.Errorf("got %v, want [terraform-fix]", got)
		}
	})

	t.Run("deduplicates and sorts", func(t *testing.T) {
		steps := []Step{
			{Description: `Skill(skill: "zebra", args: "x") and Skill(skill: "alpha", args: "y")`},
			{Description: `Skill(skill: "zebra", args: "z")`},
		}
		got := DetectSkillInvocations(steps)
		if len(got) != 2 || got[0] != "alpha" || got[1] != "zebra" {
			t.Errorf("got %v, want [alpha zebra]", got)
		}
	})

	t.Run("empty for no invocations", func(t *testing.T) {
		steps := []Step{
			{Description: "This step has no skill invocations at all."},
		}
		got := DetectSkillInvocations(steps)
		if len(got) != 0 {
			t.Errorf("got %v, want empty slice", got)
		}
	})

	t.Run("does not match prose mentions", func(t *testing.T) {
		steps := []Step{
			{Description: "Use the /plan-work skill to decompose stories."},
			{Description: "Invoke the /github-issue skill to create the issue."},
		}
		got := DetectSkillInvocations(steps)
		if len(got) != 0 {
			t.Errorf("got %v, want empty slice (prose should not match)", got)
		}
	})
}

