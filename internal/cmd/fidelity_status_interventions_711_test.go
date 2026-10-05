//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// A pre-K12 hook writes no interventions/effort_level keys. Rendering that absence as 0 would read
// exactly like the grader having stopped seeing the standing effort line, so absent must stay "-".
func TestFidelity_StatusRendersInterventionsAndEffort(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	alpha := config.AgentDir(dir, "alpha")
	writeRuntimeFile(t, alpha, "fidelity_log.jsonl", strings.Join([]string{
		`{"ts":"2026-09-24T10:00:00Z","step_id":"bd-1","verdict_ok":true,"calls_total":3,"calls_shown":3,"violations_after":0,"escalated":false,"interventions":4,"effort_level":"low"}`,
		`{"ts":"2026-09-24T10:05:00Z","step_id":"bd-2","verdict_ok":false,"calls_total":1,"calls_shown":1,"violations_after":1,"escalated":false,"interventions":3,"effort_level":"medium"}`,
	}, "\n")+"\n")
	// Not a dash: a "-" latch cell could stand in for an interventions cell and hide a column swap.
	writeRuntimeFile(t, alpha, "fidelity_escalated_step", "bd-latched\n")
	writeRuntimeFile(t, config.AgentDir(dir, "beta"), "fidelity_log.jsonl",
		`{"ts":"2026-09-24T10:06:00Z","step_id":"bd-9","verdict_ok":true,"calls_total":1,"calls_shown":1,"violations_after":0,"escalated":false,"interventions":0,"effort_level":""}`+"\n")
	writeRuntimeFile(t, config.AgentDir(dir, "gamma"), "fidelity_log.jsonl",
		`{"ts":"2026-09-24T10:07:00Z","step_id":"bd-7","verdict_ok":true,"calls_total":1,"calls_shown":1,"violations_after":0,"escalated":false}`+"\n")

	var runErr error
	out := captureStdout(t, func() { runErr = runFidelity(fidelityCmd, []string{"status"}) })
	if runErr != nil {
		t.Fatal(runErr)
	}

	for _, c := range []struct {
		why string
		re  *regexp.Regexp
	}{
		{"the interventions and effort columns follow escalated",
			regexp.MustCompile(`agent\s.*escalated\s+interventions\s+effort`)},
		{"alpha carries the LAST record's values, neither the first's (4, low) nor a sum (7)",
			regexp.MustCompile(`alpha\s+2\s+1\s+1\s+bd-2\s+bd-latched\s+3\s+medium\s*\n`)},
		{"a K12 turn with no reduction renders 0 interventions and effort -",
			regexp.MustCompile(`beta\s+1\s+0\s+0\s+bd-9\s+-\s+0\s+-\s*\n`)},
		{"a K7-only record renders - for both, never a 0 that reads as a K12 count",
			regexp.MustCompile(`gamma\s+1\s+0\s+0\s+bd-7\s+-\s+-\s+-\s*\n`)},
	} {
		if !c.re.MatchString(out) {
			t.Errorf("%s: no match for %v in:\n%s", c.why, c.re, out)
		}
	}

	for _, column := range []string{"interventions", "effort"} {
		if !strings.Contains(fidelityCmd.Long, column) {
			t.Errorf("fidelityCmd.Long lists the status table's columns but does not name %q", column)
		}
	}
}
