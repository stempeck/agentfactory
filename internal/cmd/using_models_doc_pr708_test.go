//go:build !integration

package cmd

import (
	"slices"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/tokenomics"
)

const usingModelsEffortHeading = "### One key tokenomics may lower"

// usingModelsEffortSection returns the effort subsection of USING_MODELS.md, heading through the line
// before the next heading of any level.
func usingModelsEffortSection(t *testing.T) string {
	t.Helper()
	content := repoFile(t, "USING_MODELS.md")
	start := strings.Index(content, usingModelsEffortHeading)
	if start < 0 {
		t.Fatalf("USING_MODELS.md has no %q section", usingModelsEffortHeading)
	}
	body := content[start+len(usingModelsEffortHeading):]
	if end := strings.Index(body, "\n#"); end >= 0 {
		body = body[:end]
	}
	return body
}

// unreadableStartupNamesAfUpRefusal is conditional on purpose: prose that stops listing an unreadable
// startup.json among the arm-off conditions owes no af up caveat, but prose that lists it must not read
// as though af up launches under it — up.go refuses before starting any agent.
func unreadableStartupNamesAfUpRefusal(text string) bool {
	if !strings.Contains(text, "startup.json` that cannot be read") {
		return true
	}
	return strings.Contains(text, "`af up`") && strings.Contains(text, "refuses")
}

func TestUsingModelsDoc_UnreadableStartupNamesAfUpRefusal(t *testing.T) {
	for _, para := range strings.Split(usingModelsEffortSection(t), "\n\n") {
		if !unreadableStartupNamesAfUpRefusal(para) {
			t.Errorf("USING_MODELS.md lists an unreadable startup.json as leaving the declared level "+
				"untouched without saying af up refuses to start under it:\n%s", para)
		}
	}
}

func TestUsingTokenomicsDoc_EffortGuaranteeNamesAfUpRefusal(t *testing.T) {
	section := tokenomicsContractSection(t, readUsingTokenomicsDoc(t))
	header := tableHeader(section, "Mechanism")
	col := slices.Index(header, "Guarantee")
	if col < 0 {
		t.Fatalf("the per-mechanism table has no Guarantee column (header: %q)", header)
	}
	cells := tableCells(tableRowFor(section, string(tokenomics.MechanismEffort)))
	if len(cells) != len(header) {
		t.Fatalf("the effort row has %d cells against a %d-column header", len(cells), len(header))
	}
	if guarantee := cells[col]; !unreadableStartupNamesAfUpRefusal(guarantee) {
		t.Errorf("the effort row's Guarantee lists an unreadable startup.json as exporting the declared "+
			"level without saying af up refuses to start under it:\n%s", guarantee)
	}
}

// The effort section tells operators a fixed level belongs in the profile, not the shell rc, while
// the installer writes one into the shell rc. Whichever side changes, they must not contradict.
func TestUsingModelsDoc_EffortSectionReconcilesQuickstartExport(t *testing.T) {
	if !strings.Contains(repoFile(t, "quickstart.sh"), "export CLAUDE_CODE_EFFORT_LEVEL=") {
		t.Skip("quickstart.sh no longer exports the effort level into the shell rc")
	}
	if !strings.Contains(usingModelsEffortSection(t), "quickstart.sh") {
		t.Error("quickstart.sh exports CLAUDE_CODE_EFFORT_LEVEL into the operator's shell rc, and the " +
			"USING_MODELS.md effort section, which says a fixed level belongs in the profile, never " +
			"mentions it")
	}
}
