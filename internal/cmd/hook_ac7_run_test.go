//go:build !integration

package cmd

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/transcript"
)

// AC-7 (design-doc.md:33, :276): a compliant run leaves zero violations, sends no verdict mail and
// writes a clean run record. It is the only acceptance criterion whose subject is what does NOT
// happen, which is exactly why it needs a mechanism rather than a text pin — the `af` shim's
// invocation ledger is that mechanism.
//
// SCOPE (H-1, design-doc.md:148): the judge is a PATH-shim stub. These subtests prove PLUMBING —
// verdict -> counter -> latch -> run record -> mail. Judge behavior is proven only by Phase 6's
// live-judge validation gate (.designs/562/live-judge-validation.md).
//
// Mutation record. Each defect below was reintroduced one at a time and the suite re-run; each was
// killed, which is what distinguishes these assertions from a run that merely happens to be clean.
// The mutants are deliberately NOT committed — this record is (same convention as
// internal/transcript/evidence_test.go:151-154). Each entry names the edit precisely enough to
// reapply by hand.
//
//	M1  fidelity-gate.sh:336  `elif [ "$VERDICT_STATE" = "passed" ]` -> `else`, so an ungradeable
//	                          verdict clears the count   -> killed by "an ungradeable verdict does
//	                          not launder the violation history" (sequence reads 1,1 not 1,2)
//	M2  fidelity-gate.sh:169-173  step-change reset deleted  -> killed by the counted-step assertion
//	                          and by "a violation count is spent at the step boundary" (1,2 not 1,1)
//	M3  fidelity-gate.sh:304-306  supersede loop deleted  -> killed by TestHookStormReplay
//	M4  internal/transcript/evidence.go:261-263  boundary no longer resets the accumulated calls,
//	                          so the previous turn leaks in  -> killed by
//	                          TestHookPair_EvidenceParityOverRealTranscript (the earlier turn's Grep
//	                          reaches the judge)
//	M5  fidelity-gate.sh:190  the `--format json` extractor read pointed at nothing, so every record
//	                          line says 0/0 and every verdict says "turn ended: unknown" while the
//	                          text evidence stays correct  -> killed by the calls_total/calls_shown
//	                          assertions here and by TestHookStormReplay's scope-block assertion.
//	                          This one SURVIVED the first cut of these tests; the counts exist
//	                          because of it.
//	M6  fidelity-gate.sh:357-358  calls_total and calls_shown swapped in the record writer  ->
//	                          killed by the over-cap turn, the only fixture where the two differ.
//	                          It also survived until that fixture was added.

// hookAC7Turn is one graded turn of a simulated formula run.
type hookAC7Turn struct {
	name string
	// stepID changes between steps precisely so the step-scoped counter and latch are exercised;
	// fidelity-gate.sh:167-173 keys that reset on .runtime/fidelity_counted_step.
	stepID string
	// verdict is what the judge stub returns. "" means the default pass.
	verdict string
	message string
	lines   []string
	// wantCalls is the turn's call count as the run record must report it. Without it the three
	// turn shapes below are decorative — three copies of one shape would assert exactly as much —
	// and a gate that read its evidence JSON from nowhere would still look clean, while every
	// record line said 0/0 and every verdict mail said "turn ended: unknown".
	wantCalls int
	// wantShown defaults to wantCalls; only the over-cap turn sets it, and it is what stops the two
	// count fields from being interchangeable.
	wantShown int
}

func (turn hookAC7Turn) shown() int {
	if turn.wantShown != 0 {
		return turn.wantShown
	}
	return turn.wantCalls
}

// hookAC7CompliantTurns are the three turn shapes design-doc.md:33 names, in the order a real run
// produces them, plus a fourth step that overruns the render cap.
//
// The fourth is not a design-named shape: it is the only fixture where calls_shown and calls_total
// DIFFER, and without one the two record fields are interchangeable — a writer that swapped them
// would leave every assertion green while `af fidelity status` reported the wrong numbers.
func hookAC7CompliantTurns() []hookAC7Turn {
	// transcript.DefaultOptions().MaxCalls is 20 and is not re-spelled here; the fixture is built
	// from it so a future cap change moves this turn with it rather than silently un-truncating it.
	capped := transcript.DefaultOptions().MaxCalls
	overCap := []string{turnPrompt("u1", "run every step of the checklist")}
	for i := 1; i <= capped+5; i++ {
		overCap = append(overCap,
			turnCall(fmt.Sprintf("a%d", i), fmt.Sprintf("m%d", i), fmt.Sprintf("t%d", i), "Bash",
				fmt.Sprintf(`{"command":"checklist-item-%d"}`, i)),
			turnResult(fmt.Sprintf("r%d", i), fmt.Sprintf("t%d", i), fmt.Sprintf("item %d done", i), false),
		)
	}

	return []hookAC7Turn{
		{
			// Multi-turn completion: the work landed in an earlier turn and only the completion
			// claim lands in this one. The gate must grade THIS turn, so the evidence it shows is
			// the single `af done` call — not the four calls that did the work.
			name:    "multi-turn completion",
			stepID:  "bd-p7-ac7-step-1",
			message: "The design artifacts are committed and the PR is open; closing the step.",
			// One, not four: the boundary is the SECOND prompt, so the work the earlier turn did
			// must not be counted here. This is AC-1 asserted at the run record.
			wantCalls: 1,
			lines: []string{
				turnPrompt("u1", "produce the design artifacts"),
				turnCall("a1", "m1", "t1", "Write", `{"file_path":"/w/.designs/562/design-doc.md"}`),
				turnResult("r1", "t1", "File created successfully", false),
				turnCall("a2", "m2", "t2", "Bash", `{"command":"git commit -m design"}`),
				turnResult("r2", "t2", "[main 1a2b3c4] design", false),
				turnPrompt("u2", "the reviewer replied; continue the step"),
				turnCall("a3", "m3", "t3", "Bash", `{"command":"af done"}`),
				turnResult("r3", "t3", "Step closed. Next step ready.", false),
			},
		},
		{
			// Zero-tool wait turn: an authoritative "no tools were used", which the extractor
			// reports with its own frozen marker rather than with an empty block.
			name:      "zero-tool wait turn",
			stepID:    "bd-p7-ac7-step-2",
			message:   "Waiting on the designer's reply before continuing; nothing to execute this turn.",
			wantCalls: 0,
			lines: []string{
				turnPrompt("u1", "anything back from the designer yet?"),
				turnAssistantRec("a1", "m1", false, turnTextBlock("Nothing yet — holding.")),
			},
		},
		{
			// False-guard conditional: a guard command that legitimately evaluates false. The turn
			// did real work; a judge shown only "the commit produced no output" would read it as
			// the agent doing nothing.
			name:      "false-guard conditional",
			stepID:    "bd-p7-ac7-step-3",
			message:   "Nothing was newly staged, so the commit was correctly skipped and the push was a no-op.",
			wantCalls: 2,
			lines: []string{
				turnPrompt("u1", "commit anything outstanding and push"),
				turnCall("a1", "m1", "t1", "Bash", `{"command":"git diff --cached --quiet || git commit -m artifacts"}`),
				turnResult("r1", "t1", "(no output — nothing newly staged)", false),
				turnCall("a2", "m2", "t2", "Bash", `{"command":"git push origin HEAD"}`),
				turnResult("r2", "t2", "Everything up-to-date", false),
			},
		},
		{
			name:      "turn that overruns the render cap",
			stepID:    "bd-p7-ac7-step-4",
			message:   "Worked through the whole checklist; every item is done.",
			wantCalls: capped + 5,
			wantShown: capped,
			lines:     overCap,
		},
	}
}

func TestHookAC7CompliantRun(t *testing.T) {
	rig := newHookE2ERig(t)
	fidelity := hookE2EGates()[0]

	// runTurns drives a sequence through the fidelity gate in one workdir, so the counter, the
	// latch and the run record carry across turns exactly as they do in a live session.
	runTurns := func(t *testing.T, workDir string, turns []hookAC7Turn) {
		t.Helper()
		for _, turn := range turns {
			hookE2ESetStep(t, workDir, turn.stepID, "Step "+turn.stepID)
			if turn.verdict == "" {
				hookE2ESetVerdict(t, workDir, `{"ok": true, "reasoning": "follows the step contract"}`)
			} else {
				hookE2ESetVerdict(t, workDir, turn.verdict)
			}
			transcript := hookE2EWriteTranscript(t, t.TempDir(), "turn.jsonl", turn.lines...)
			out, exitCode := rig.run(t, fidelity, workDir, hookE2EPayload(t, turn.message, transcript), "")
			if exitCode != 0 {
				t.Fatalf("turn %q: exit %d, want 0\noutput: %s", turn.name, exitCode, out)
			}
			if !strings.Contains(out, `{"ok": true}`) {
				t.Fatalf("turn %q did not emit `{\"ok\": true}`:\n%s", turn.name, out)
			}
		}
	}

	t.Run("a compliant multi-step run leaves no violations, no mail and a clean record", func(t *testing.T) {
		workDir := setupGateLockTestEnv(t)
		turns := hookAC7CompliantTurns()

		// Seeded deliberately: a run that starts from zero cannot tell a working counter reset from
		// a counter that was never touched. Starting at 2 on the run's OWN first step means the
		// first record line can only read violations_after 0 if the pass branch
		// (fidelity-gate.sh:336-342) actually cleared it.
		hookE2EWriteRuntime(t, workDir, "fidelity_violations", "2\n")
		hookE2EWriteRuntime(t, workDir, "fidelity_counted_step", turns[0].stepID+"\n")

		runTurns(t, workDir, turns)

		if got := strings.TrimSpace(hookE2EReadRuntime(t, workDir, "fidelity_violations")); got != "0" {
			t.Errorf("violation counter = %q after a compliant run, want %q", got, "0")
		}

		ledger := hookE2ELedger(t, workDir)
		if len(ledger) == 0 {
			t.Fatal("the af shim was never invoked — the gate never reached the evidence path, so `no mail` proves nothing")
		}
		// Asserted as an ALLOWLIST of nothing rather than as a denylist of the two verdict subjects:
		// a compliant run has no reason to mail at all, and the gate's other senders are its
		// degradation notices (fidelity-gate.sh:24-46). A denylist would let a GRADER_UNAVAILABLE
		// storm — the exact failure ADR-007's amendment clause 2 forbids — pass as compliant.
		if sends := hookE2EMailSends(t, workDir); len(sends) != 0 {
			t.Errorf("a compliant run sent mail: %v", sends)
		}

		records := hookE2ERunRecord(t, workDir)
		if len(records) != len(turns) {
			t.Fatalf("run record has %d lines, want one per evaluation (%d)", len(records), len(turns))
		}
		if len(turns) < 3 {
			t.Fatalf("AC-7 asks for a run of at least three steps; this fixture has %d", len(turns))
		}

		// The key set is taken by reflection off the reader's own struct, so a writer that drifts
		// from internal/cmd/fidelity.go:302-312 fails here rather than silently producing counts
		// the status command skips (fidelity.go:362-364 drops a line it cannot parse).
		wantKeys := hookFidelityRecordKeys()
		for i, record := range records {
			what := "run record line " + turns[i].name
			assertTurnKeySet(t, record, wantKeys, what)
			if !turnJSONBool(t, record, "verdict_ok", what) {
				t.Errorf("%s: verdict_ok = false, want true", what)
			}
			if n := turnJSONInt(t, record, "violations_after", what); n != 0 {
				t.Errorf("%s: violations_after = %d, want 0", what, n)
			}
			if turnJSONBool(t, record, "escalated", what) {
				t.Errorf("%s: escalated = true, want false", what)
			}
			if got := turnJSONString(t, record, "step_id", what); got != turns[i].stepID {
				t.Errorf("%s: step_id = %q, want %q", what, got, turns[i].stepID)
			}
			// The counts are what make the three turn SHAPES load-bearing, and they are the only
			// assertion that reaches the gate's second extractor call — the `--format json` read at
			// fidelity-gate.sh:190 that also supplies the verdict mail's turn boundary. Point that
			// read at nothing and every field here silently reports 0.
			if n := turnJSONInt(t, record, "calls_total", what); n != turns[i].wantCalls {
				t.Errorf("%s: calls_total = %d, want %d", what, n, turns[i].wantCalls)
			}
			if n := turnJSONInt(t, record, "calls_shown", what); n != turns[i].shown() {
				t.Errorf("%s: calls_shown = %d, want %d", what, n, turns[i].shown())
			}
		}

		// The only observable that distinguishes a live step-change reset from a pass branch that
		// happens to clear the same counter: after three steps the counted step must be the LAST
		// one. Deleting fidelity-gate.sh:167-173 leaves it pinned at the first.
		if got := strings.TrimSpace(hookE2EReadRuntime(t, workDir, "fidelity_counted_step")); got != turns[len(turns)-1].stepID {
			t.Errorf("counted step = %q after a three-step run, want the last step %q", got, turns[len(turns)-1].stepID)
		}
	})

	t.Run("a violation count is spent at the step boundary", func(t *testing.T) {
		workDir := setupGateLockTestEnv(t)
		flagged := `{"ok": false, "reasoning": "deviated from the step contract"}`
		turns := []hookAC7Turn{
			{name: "flagged on step one", stepID: "bd-p7-boundary-1", verdict: flagged,
				message: "did something else", lines: hookAC7MinimalTurn()},
			{name: "flagged on step two", stepID: "bd-p7-boundary-2", verdict: flagged,
				message: "did something else again", lines: hookAC7MinimalTurn()},
		}
		runTurns(t, workDir, turns)

		hookAC7AssertViolationSequence(t, workDir, []int{1, 1})
	})

	t.Run("an ungradeable verdict does not launder the violation history", func(t *testing.T) {
		workDir := setupGateLockTestEnv(t)
		flagged := `{"ok": false, "reasoning": "deviated from the step contract"}`
		turns := []hookAC7Turn{
			{name: "flagged", stepID: "bd-p7-unparsed", verdict: flagged,
				message: "did something else", lines: hookAC7MinimalTurn()},
			// Non-empty so the grader-unavailable notice does not fire, and unparseable so the
			// verdict is neither a pass nor a flag. A turn the grader could not grade writes no
			// record line at all, and must leave the counter exactly as it was.
			{name: "ungradeable", stepID: "bd-p7-unparsed", verdict: "the judge returned prose, not JSON",
				message: "another turn", lines: hookAC7MinimalTurn()},
			{name: "flagged again", stepID: "bd-p7-unparsed", verdict: flagged,
				message: "and again", lines: hookAC7MinimalTurn()},
		}
		runTurns(t, workDir, turns)

		hookAC7AssertViolationSequence(t, workDir, []int{1, 2})
	})
}

func hookAC7MinimalTurn() []string {
	return []string{
		turnPrompt("u1", "execute the step"),
		turnCall("a1", "m1", "t1", "Bash", `{"command":"echo working"}`),
		turnResult("r1", "t1", "working", false),
	}
}

func hookAC7AssertViolationSequence(t *testing.T, workDir string, want []int) {
	t.Helper()
	records := hookE2ERunRecord(t, workDir)
	if len(records) != len(want) {
		t.Fatalf("run record has %d lines, want %d", len(records), len(want))
	}
	for i, record := range records {
		what := "run record line " + string(rune('1'+i))
		if got := turnJSONInt(t, record, "violations_after", what); got != want[i] {
			t.Errorf("%s: violations_after = %d, want %d", what, got, want[i])
		}
	}
}

// hookFidelityRecordKeys derives the record's key set from the struct `af fidelity status` decodes,
// so writer and reader cannot drift apart. Same technique as
// TestFidelityGate_AppendsRunRecord (hook_evidence_contract_test.go:291-318), applied to the
// written bytes rather than to the script's source text.
func hookFidelityRecordKeys() []string {
	recordType := reflect.TypeOf(fidelityRecord{})
	keys := make([]string, 0, recordType.NumField())
	for i := 0; i < recordType.NumField(); i++ {
		tag := strings.Split(recordType.Field(i).Tag.Get("json"), ",")[0]
		if tag != "" {
			keys = append(keys, tag)
		}
	}
	return keys
}
