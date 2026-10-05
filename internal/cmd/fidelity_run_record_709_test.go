//go:build !integration

package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// TestFidelityGateRunRecordCarriesInterventionCount pins that the run record says how many
// intervention lines the grader was shown and the session's standing effort level, so a silently
// empty intervention section is visible after the fact. The empty-section turn is the load-bearing
// case: its record must still be written, with a zero count.
func TestFidelityGateRunRecordCarriesInterventionCount(t *testing.T) {
	rig := newHookE2ERig(t)
	fidelity := hookE2EGates()[0]
	turnLines := []string{
		turnPrompt("u1", "execute the step"),
		turnCall("a1", "m1", "t1", "Bash", `{"command":"echo working"}`),
		turnResult("r1", "t1", "working", false),
	}

	runTurn := func(t *testing.T, workDir string) (record map[string]json.RawMessage, debugLog, judgeInput string) {
		t.Helper()
		hookE2ESetStep(t, workDir, "bd-k15-step-1", "Step one")
		hookE2ESetVerdict(t, workDir, `{"ok": true, "reasoning": "follows the step contract"}`)
		path := hookE2EWriteTranscript(t, t.TempDir(), "turn.jsonl", turnLines...)
		out, code := rig.run(t, fidelity, workDir, hookE2EPayload(t, "Held for the serialization advisory, then continued.", path), "")
		if code != 0 {
			t.Fatalf("gate exit %d, want 0\n%s", code, out)
		}
		records := hookE2ERunRecord(t, workDir)
		if len(records) != 1 {
			t.Fatalf("run records = %d, want 1 (a missing record means the count broke the record writer)", len(records))
		}
		for _, key := range []string{"interventions", "effort_level"} {
			if _, ok := records[0][key]; !ok {
				t.Fatalf("run record has no %q key: %v", key, keysOf(records[0]))
			}
		}
		assertTurnKeySet(t, records[0], hookFidelityRecordKeys(), "run record")
		assertReaderKeepsKeys(t, hookE2EReadRuntime(t, workDir, "fidelity_log.jsonl"))
		return records[0], hookE2EDebugLog(t, workDir, fidelity), hookE2EJudgeInput(t, workDir, fidelity.name)
	}

	t.Run("with interventions", func(t *testing.T) {
		workDir := setupGateLockTestEnv(t)
		plantIntervention(t, workDir, hookE2ERole, "2026-08-09T10:00:05.000Z",
			tokenomics.MechanismDispatch, telemetry.ActionAdvise)
		plantLaunchEffort(t, "medium", "efficiency", "bd-k15-step-1", "hook-e2e")

		record, debugLog, judgeInput := runTurn(t, workDir)

		for _, want := range []string{fidelityInterventionHeading, "- dispatch: advise", "(effort_level=medium)"} {
			if !strings.Contains(judgeInput, want) {
				t.Fatalf("fixture did not splice %q into the judge input; nothing to count:\n%s", want, judgeInput)
			}
		}
		if got := turnJSONInt(t, record, "interventions", "run record"); got != 2 {
			t.Errorf("interventions = %d, want 2 (the dispatch line and the standing effort line)", got)
		}
		if got := turnJSONString(t, record, "effort_level", "run record"); got != "medium" {
			t.Errorf("effort_level = %q, want %q", got, "medium")
		}
		assertCountsBesideEvalLine(t, debugLog, "interventions=2 effort=medium")
	})

	t.Run("empty section", func(t *testing.T) {
		workDir := setupGateLockTestEnv(t)
		t.Setenv("AF_EFFORT_OBJECTIVE", "")

		record, debugLog, judgeInput := runTurn(t, workDir)

		if strings.Contains(judgeInput, fidelityInterventionHeading) {
			t.Fatalf("fixture spliced an intervention section into a turn that should have none:\n%s", judgeInput)
		}
		if got := turnJSONInt(t, record, "interventions", "run record"); got != 0 {
			t.Errorf("interventions = %d, want 0", got)
		}
		if got := turnJSONString(t, record, "effort_level", "run record"); got != "" {
			t.Errorf("effort_level = %q, want empty", got)
		}
		assertCountsBesideEvalLine(t, debugLog, "interventions=0 effort=none")
	})
}

// assertReaderKeepsKeys decodes the record through the struct af fidelity's reader uses, so a key
// the writer emits but the reader drops fails here rather than vanishing silently.
func assertReaderKeepsKeys(t *testing.T, raw string) {
	t.Helper()
	var rec fidelityRecord
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &rec); err != nil {
		t.Fatalf("fidelity reader cannot decode the run record: %v\n%s", err, raw)
	}
	b, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("re-encode fidelityRecord: %v", err)
	}
	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(b, &decoded); err != nil {
		t.Fatalf("decode re-encoded record: %v", err)
	}
	for _, key := range []string{"interventions", "effort_level"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("fidelity's reader drops %q from the run record", key)
		}
	}
}

func assertCountsBesideEvalLine(t *testing.T, debugLog, counts string) {
	t.Helper()
	var sawEval, sawCounts bool
	for _, line := range strings.Split(debugLog, "\n") {
		if strings.Contains(line, "EVAL: step=bd-k15-step-1 turn=") {
			sawEval = true
			if strings.Contains(line, "interventions=") {
				t.Errorf("the counts were written onto the EVAL line: %q", line)
			}
		}
		if strings.Contains(line, counts) {
			sawCounts = true
		}
	}
	if !sawEval {
		t.Errorf("debug log has no EVAL line:\n%s", debugLog)
	}
	if !sawCounts {
		t.Errorf("debug log has no %q line:\n%s", counts, debugLog)
	}
}
