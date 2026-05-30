package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestFormulaCreateSkill_AllowedToolsTarget(t *testing.T) {
	data, err := os.ReadFile("../../.claude/skills/formula-create/SKILL.md")
	if err != nil {
		t.Fatalf("reading SKILL.md: %v", err)
	}
	content := string(data)

	for _, line := range strings.Split(content, "\n") {
		if !strings.Contains(line, "allowed-tools") {
			continue
		}
		if !strings.Contains(line, ".agentfactory/store/formulas/") {
			t.Errorf("allowed-tools should target .agentfactory/store/formulas/, got: %s", line)
		}
		if strings.Contains(line, "internal/cmd/install_formulas") {
			t.Errorf("allowed-tools must NOT target internal/cmd/install_formulas, got: %s", line)
		}
		return
	}
	t.Fatal("no allowed-tools line found in SKILL.md")
}

func TestFormulaCreateSkill_SkillsArrayInTemplate(t *testing.T) {
	data, err := os.ReadFile("../../.claude/skills/formula-create/SKILL.md")
	if err != nil {
		t.Fatalf("reading SKILL.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, `skills = ["skill-a", "skill-b"]`) {
		t.Error("SKILL.md TOML template must include skills = [...] field after version = 1")
	}
}

func TestFormulaCreateSkillmdMode_SkillsPopulationGuidance(t *testing.T) {
	data, err := os.ReadFile("../../.claude/skills/formula-create/skillmd-mode.md")
	if err != nil {
		t.Fatalf("reading skillmd-mode.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "Skills array population") {
		t.Error("skillmd-mode.md must contain skills array population guidance between Section 10.2 and 10.3")
	}
}

func TestFormulaCreateSkillmdMode_SkillInvocationPattern(t *testing.T) {
	data, err := os.ReadFile("../../.claude/skills/formula-create/skillmd-mode.md")
	if err != nil {
		t.Fatalf("reading skillmd-mode.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, `Skill(skill: "name", args: "...")`) {
		t.Error("skillmd-mode.md must document the Skill() invocation pattern")
	}
}

func TestFormulaCreateSkillmdMode_SkillsArrayCompletenessCheck(t *testing.T) {
	data, err := os.ReadFile("../../.claude/skills/formula-create/skillmd-mode.md")
	if err != nil {
		t.Fatalf("reading skillmd-mode.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "Skills array completeness") {
		t.Error("skillmd-mode.md Section 10.10 must include check #7 for skills array completeness")
	}
}

func TestUsingAgentfactoryDoc_SyncBehavior(t *testing.T) {
	data, err := os.ReadFile("../../USING_AGENTFACTORY.md")
	if err != nil {
		t.Fatalf("reading USING_AGENTFACTORY.md: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "Unpromoted formulas get deleted") {
		t.Error("USING_AGENTFACTORY.md still warns about formula deletion; should reflect safe sync behavior")
	}
	if !strings.Contains(content, "Customer formulas are safe") {
		t.Error("USING_AGENTFACTORY.md should mention that customer formulas are safe")
	}
}
