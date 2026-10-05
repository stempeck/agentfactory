//go:build !integration

package cmd

import (
	"strings"
	"testing"
)

// TestUsingTokenomicsDescribesStandingEffortLine pins issue #709 AC-6: the guide must describe what
// the grader is actually told — a standing reduction read off the launch line on every turn — and
// how an operator checks it, not a file that formula cleanup can delete out from under a live session.
func TestUsingTokenomicsDescribesStandingEffortLine(t *testing.T) {
	content := readUsingTokenomicsDoc(t)

	lines := strings.Split(content, "\n")
	if len(lines) < 43 || !strings.Contains(strings.Join(lines[38:43], "\n"), "standing") {
		t.Errorf("USING_TOKENOMICS.md lines 39-43 (the command block) must say the interventions verb also " +
			"reports the session's standing effort reduction")
	}

	var row string
	for _, line := range lines {
		if strings.HasPrefix(line, "| `effort` |") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatal("USING_TOKENOMICS.md has no `effort` row in the per-mechanism table")
	}
	for _, want := range []string{
		"`AF_EFFORT_OBJECTIVE`",
		"every turn",
		"formula completion",
		"`af up` on a running agent",
		"/environ",
		"af install --init",
	} {
		if !strings.Contains(row, want) {
			t.Errorf("the effort row does not say %q; it must name the launch-line carrier, that it is read on "+
				"every turn and survives formula completion and `af up` on a running agent, how to check a running "+
				"pane's process env, and that an installed hook refreshes only on `af install --init`", want)
		}
	}
	// tmux carries only the identity quintet, so show-environment can never be the check.
	if !strings.Contains(row, "There is no tmux copy; read the process.") {
		t.Error("the effort row does not say there is no tmux copy of the attestation and the process is the read")
	}
	if strings.Contains(row, "show-environment") {
		t.Error("the effort row offers `tmux show-environment` as a check; the tmux session env carries no attestation")
	}
	if strings.Contains(row, "breadcrumb") {
		t.Error("the effort row still describes a breadcrumb; the launch line is the only carrier")
	}
	// Composed so this file does not itself trip the phase AC's repo-wide grep for the phrases.
	for _, qualifier := range []string{"launch", "effort"} {
		stale := qualifier + " breadcrumb"
		if strings.Contains(content, stale) {
			t.Errorf("USING_TOKENOMICS.md still says %q", stale)
		}
	}
}

func TestUsingModelsNamesAttestationKeysAsReserved(t *testing.T) {
	section := usingModelsEffortSection(t)
	for _, key := range []string{"AF_EFFORT_OBJECTIVE", "AF_EFFORT_STEP_LABEL", "AF_EFFORT_FORMULA"} {
		if !strings.Contains(section, key) {
			t.Errorf("USING_MODELS.md's effort section does not name %s as a factory-owned export a profile may "+
				"not declare", key)
		}
	}
}

func TestUsingAgentfactoryInterventionsLineNamesStandingReduction(t *testing.T) {
	for _, line := range strings.Split(readUsingAgentfactoryDoc(t), "\n") {
		if strings.HasPrefix(line, "af turn interventions ") {
			if !strings.Contains(line, "standing effort reduction") {
				t.Errorf("USING_AGENTFACTORY.md's interventions line does not mention the standing effort "+
					"reduction: %q", line)
			}
			return
		}
	}
	t.Fatal("USING_AGENTFACTORY.md has no `af turn interventions` line")
}

// The help is what an operator reads when the section is missing, and the carrier is process env: a
// run from a shell outside the session shows no standing line, which the help must say.
func TestTurnInterventionsHelpNamesTheCarrier(t *testing.T) {
	help := strings.Join(strings.Fields(interventionsCmd.Long), " ")
	if !strings.Contains(help, "read from this process's own environment") {
		t.Errorf("`af turn interventions --help` does not say the standing reduction is read from this "+
			"process's own environment:\n%s", help)
	}
	if strings.Contains(help, "breadcrumb") {
		t.Errorf("`af turn interventions --help` still names a breadcrumb:\n%s", help)
	}
}
