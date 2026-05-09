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
