//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// #668 K15: the fidelity grader is shown what the HARNESS did to the agent this turn.
//
// The problem it closes is stated in design-doc.md:326. Every mechanism in this feature works by
// interrupting the agent — a serialization advisory tells it to wait, an effort reduction changes
// how it thinks, a handoff ends its turn early. To a grader that sees only the step contract and the
// agent's response, all three read as the agent deviating from its instructions, and the gate then
// spends the escalation ladder punishing compliance with the harness.

// plantIntervention writes one intervention record where the reader looks for it, at a timestamp
// the caller controls: the whole point of the verb is a WINDOW, and a fixture that could not place a
// record on either side of the boundary could not test one.
func plantIntervention(t *testing.T, root, agent, ts string, mechanism tokenomics.Mechanism, action string) {
	t.Helper()
	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V:         telemetry.SchemaVersion,
		TS:        ts,
		Agent:     agent,
		Event:     telemetry.EventIntervention,
		Verb:      "prime",
		Formula:   "hook-e2e",
		StepID:    "bd-k15-step-1",
		Mechanism: string(mechanism),
		Action:    action,
	}); err != nil {
		t.Fatalf("planting an intervention record: %v", err)
	}
}

// runTurnInterventions drives the verb the gate calls, and returns what the gate would splice.
func runTurnInterventions(t *testing.T, root, agent, since string) string {
	t.Helper()
	var out bytes.Buffer
	if err := runTurnInterventionsCore(&out, root, agent, since); err != nil {
		t.Fatalf("af turn interventions returned an error; ADR-007 says every transcript-side "+
			"outcome rides in the OUTPUT and still exits 0: %v", err)
	}
	return out.String()
}

// TestTurnInterventions is the reader half — the verb `af turn interventions`, sibling of
// `af turn evidence`. It is a separate verb rather than a widening of the existing one because the
// two answer different questions from different sources: one reads the host's transcript, this
// reads af's own append-only log.
func TestTurnInterventions(t *testing.T) {
	const boundary = "2026-08-09T10:00:00Z"

	t.Run("a firing after the boundary is named with its mechanism and action", func(t *testing.T) {
		root := t.TempDir()
		plantIntervention(t, root, "agent-a", "2026-08-09T10:00:05.000Z", tokenomics.MechanismDispatch, telemetry.ActionAdvise)

		got := runTurnInterventions(t, root, "agent-a", boundary)

		if !strings.Contains(got, string(tokenomics.MechanismDispatch)) {
			t.Errorf("the section does not name the mechanism:\n%s", got)
		}
		if !strings.Contains(got, telemetry.ActionAdvise) {
			t.Errorf("the section does not name the action:\n%s", got)
		}
	})

	t.Run("a firing before the boundary belongs to an earlier turn", func(t *testing.T) {
		root := t.TempDir()
		plantIntervention(t, root, "agent-a", "2026-08-09T09:59:59.000Z", tokenomics.MechanismDispatch, telemetry.ActionAdvise)

		if got := runTurnInterventions(t, root, "agent-a", boundary); strings.TrimSpace(got) != "" {
			t.Errorf("a record from before this turn reached the grader; it would excuse a deviation "+
				"the harness did not cause:\n%s", got)
		}
	})

	t.Run("another agent's firing is not this agent's", func(t *testing.T) {
		root := t.TempDir()
		plantIntervention(t, root, "agent-b", "2026-08-09T10:00:05.000Z", tokenomics.MechanismDispatch, telemetry.ActionAdvise)

		if got := runTurnInterventions(t, root, "agent-a", boundary); strings.TrimSpace(got) != "" {
			t.Errorf("a sibling agent's intervention reached this agent's grader:\n%s", got)
		}
	})

	t.Run("a turn with no firings produces nothing at all", func(t *testing.T) {
		root := t.TempDir()

		if got := runTurnInterventions(t, root, "agent-a", boundary); got != "" {
			t.Errorf("an empty turn produced %q; the gate splices this verbatim and anything but the "+
				"empty string moves the judge input", got)
		}
	})

	t.Run("only interventions are reported", func(t *testing.T) {
		root := t.TempDir()
		if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
			V: telemetry.SchemaVersion, TS: "2026-08-09T10:00:05.000Z", Agent: "agent-a",
			Event: telemetry.EventStepStart, Verb: "prime", StepID: "bd-k15-step-1",
		}); err != nil {
			t.Fatal(err)
		}

		if got := runTurnInterventions(t, root, "agent-a", boundary); strings.TrimSpace(got) != "" {
			t.Errorf("a step_start record was reported as a system intervention:\n%s", got)
		}
	})

	t.Run("an unusable window or store costs the section and nothing else", func(t *testing.T) {
		root := t.TempDir()
		plantIntervention(t, root, "agent-a", "2026-08-09T10:00:05.000Z", tokenomics.MechanismDispatch, telemetry.ActionAdvise)

		// "unknown" is what fidelity-gate.sh:197 substitutes when the extractor could not find a
		// turn boundary, and it reaches this verb verbatim on every such turn.
		for _, since := range []string{"unknown", "", "not-a-timestamp"} {
			if got := runTurnInterventions(t, root, "agent-a", since); strings.TrimSpace(got) != "" {
				t.Errorf("since=%q produced a section; without a boundary the reader cannot say which "+
					"turn a record belongs to, and guessing attributes an old firing to this turn:\n%s",
					since, got)
			}
		}
		if got := runTurnInterventions(t, filepath.Join(root, "nope"), "agent-a", "2026-08-09T10:00:00Z"); got != "" {
			t.Errorf("a missing telemetry directory produced %q, want silence", got)
		}
		if got := runTurnInterventions(t, "", "agent-a", "2026-08-09T10:00:00Z"); got != "" {
			t.Errorf("an unresolved factory root produced %q, want silence", got)
		}
		if got := runTurnInterventions(t, root, "", "2026-08-09T10:00:00Z"); got != "" {
			t.Errorf("an unnamed agent produced %q, want silence", got)
		}
	})

	t.Run("a corrupt record costs its own line and nothing else", func(t *testing.T) {
		root := t.TempDir()
		plantIntervention(t, root, "agent-a", "2026-08-09T10:00:05.000Z", tokenomics.MechanismDispatch, telemetry.ActionAdvise)
		path := filepath.Join(config.TelemetryDir(root), "steps", "agent-a.jsonl")
		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append([]byte("{not json\n"), body...), 0o644); err != nil {
			t.Fatal(err)
		}

		got := runTurnInterventions(t, root, "agent-a", "2026-08-09T10:00:00Z")

		if !strings.Contains(got, string(tokenomics.MechanismDispatch)) {
			t.Errorf("one unparseable line cost the readable record beside it:\n%s", got)
		}
	})
}

// TestFidelityGateInterventionSection is the wiring half, driven through the real gate script. It is
// the only route that can observe EVAL_INPUT: the judge runs under `env -i` and the harness's shim
// captures the prompt it was handed.
func TestFidelityGateInterventionSection(t *testing.T) {
	rig := newHookE2ERig(t)
	fidelity := hookE2EGates()[0]

	// The turn every subtest grades. turnPrompt/turnCall stamp turnTestTS, which is therefore the
	// boundary the extractor reports and the window edge the reader is handed.
	turnLines := []string{
		turnPrompt("u1", "execute the step"),
		turnCall("a1", "m1", "t1", "Bash", `{"command":"echo working"}`),
		turnResult("r1", "t1", "working", false),
	}

	runTurn := func(t *testing.T, workDir string) string {
		t.Helper()
		hookE2ESetStep(t, workDir, "bd-k15-step-1", "Step one")
		hookE2ESetVerdict(t, workDir, `{"ok": true, "reasoning": "follows the step contract"}`)
		path := hookE2EWriteTranscript(t, t.TempDir(), "turn.jsonl", turnLines...)
		out, code := rig.run(t, fidelity, workDir, hookE2EPayload(t, "Held for the serialization advisory, then continued.", path), "")
		if code != 0 {
			t.Fatalf("gate exit %d, want 0\n%s", code, out)
		}
		return hookE2EJudgeInput(t, workDir, fidelity.name)
	}

	t.Run("a turn with no interventions produces today's judge input exactly", func(t *testing.T) {
		bare := runTurn(t, setupGateLockTestEnv(t))

		withIV := func() string {
			workDir := setupGateLockTestEnv(t)
			plantIntervention(t, workDir, hookE2ERole, "2026-08-09T10:00:05.000Z",
				tokenomics.MechanismDispatch, telemetry.ActionAdvise)
			return runTurn(t, workDir)
		}()

		if bare == withIV {
			t.Fatal("planting an intervention changed nothing in the judge input; the control below " +
				"proves nothing and neither does any subtest above it")
		}
		prefix := commonPrefix(bare, withIV)
		suffix := commonSuffix(bare[len(prefix):], withIV[len(prefix):])
		section := withIV[len(prefix) : len(withIV)-len(suffix)]
		if got := strings.Replace(withIV, section, "", 1); got != bare {
			t.Errorf("removing the intervention section does not restore the bare judge input.\n"+
				"section:\n%q\nwith (%d bytes):\n%s\nwithout (%d bytes):\n%s",
				section, len(withIV), withIV, len(bare), bare)
		}
	})

	t.Run("the grader is told what the harness did this turn", func(t *testing.T) {
		workDir := setupGateLockTestEnv(t)
		plantIntervention(t, workDir, hookE2ERole, "2026-08-09T10:00:05.000Z",
			tokenomics.MechanismDispatch, telemetry.ActionAdvise)

		judgeInput := runTurn(t, workDir)

		if !strings.Contains(judgeInput, fidelityInterventionHeading) {
			t.Fatalf("the judge input carries no system-interventions section:\n%s", judgeInput)
		}
		if !strings.Contains(judgeInput, string(tokenomics.MechanismDispatch)) {
			t.Error("the section does not name the mechanism that fired")
		}
		if !strings.Contains(judgeInput, telemetry.ActionAdvise) {
			t.Error("the section does not name the action that was taken")
		}

		// The section must sit between the assistant response and the tool evidence. The evidence
		// block is sliced off the FINAL separator (hook_e2e_harness_test.go:643-651), so a section
		// appended after it would silently replace the evidence in every comparison the pair-parity
		// tests make.
		evidence := hookE2EEvidenceBlock(t, judgeInput, "fidelity judge input")
		hookE2ERequireEvidence(t, evidence, "fidelity judge input")
		if strings.Contains(evidence, fidelityInterventionHeading) {
			t.Error("the intervention section landed inside the trailing tool-evidence block")
		}
		head := judgeInput[:strings.Index(judgeInput, fidelityInterventionHeading)]
		if !strings.Contains(head, "Assistant response:") {
			t.Error("the intervention section precedes the assistant response; the grader reads the " +
				"harness's action before it reads what the agent did with it")
		}
	})

	t.Run("the gate proceeds when the reader is unavailable", func(t *testing.T) {
		// A transcript with no plain user record yields no boundary, so the gate substitutes
		// "unknown" (fidelity-gate.sh:197) and the reader must decline rather than guess.
		workDir := setupGateLockTestEnv(t)
		plantIntervention(t, workDir, hookE2ERole, "2026-08-09T10:00:05.000Z",
			tokenomics.MechanismDispatch, telemetry.ActionAdvise)
		hookE2ESetStep(t, workDir, "bd-k15-step-1", "Step one")
		hookE2ESetVerdict(t, workDir, `{"ok": true}`)
		path := hookE2EWriteTranscript(t, t.TempDir(), "turn.jsonl",
			turnAssistantRec("a1", "m1", false, turnTextBlock("no boundary in this transcript")))

		out, code := rig.run(t, fidelity, workDir, hookE2EPayload(t, "continued", path), "")

		if code != 0 {
			t.Fatalf("gate exit %d with no turn boundary, want 0\n%s", code, out)
		}
		judgeInput := hookE2EJudgeInput(t, workDir, fidelity.name)
		if strings.Contains(judgeInput, fidelityInterventionHeading) {
			t.Error("with no boundary the reader still emitted a section; it cannot know which turn " +
				"the record belongs to")
		}
	})
}

// TestFidelityGateEmptySectionIsGolden pins the claim fidelity-gate.sh:232 makes and that no
// within-build comparison can: with no interventions the prompt is byte-identical to the one this
// gate built BEFORE Phase 5.
//
// TestFidelityGateInterventionSection compares this build with a section against this build without
// one, which is a statement about the section's own boundaries — it stays green through any edit
// that touches both arms equally, including an unconditional blank line added beside the splice,
// which would change the judge prompt on every intervention-free turn. The golden below is the
// pre-Phase-5 shape spelled out, so that edit fails here.
//
// The shape rather than the whole prompt: what Phase 5 could break is the two lines around the
// splice, and a golden of the entire EVAL_INPUT would fail on every unrelated FIDELITY-DELTA a later
// phase adds and would be deleted the first time it did.
func TestFidelityGateEmptySectionIsGolden(t *testing.T) {
	// $INTERVENTION_SECTION is "" on an intervention-free turn, so the three lines collapse to
	// exactly this: the response, one empty line where the variable expanded, then the separator.
	const golden = "Assistant response: $MESSAGE\n$INTERVENTION_SECTION\n---\n"

	repoRoot := findRepoRoot(t)
	for _, path := range fidelityGateFiles(repoRoot) {
		content := readGateFile(t, path)
		if !strings.Contains(content, golden) {
			t.Errorf("%s: the EVAL_INPUT region around the intervention splice is no longer the "+
				"pre-Phase-5 shape.\nwant to contain:\n%q\n\nWith no interventions the variable "+
				"expands to nothing, so anything added between these lines changes the judge prompt "+
				"on EVERY turn — including the turns this feature is not involved in.", path, golden)
		}
	}
}

// TestFidelityGatePromptExcusesHarnessActions is AC 2's other half. The section reaching the judge
// achieves nothing unless the judge is told what to do with it: a grader shown "the harness told
// this agent to wait" and no instruction will read the wait as the deviation it is looking for.
func TestFidelityGatePromptExcusesHarnessActions(t *testing.T) {
	repoRoot := findRepoRoot(t)
	for _, path := range fidelityGateFiles(repoRoot) {
		content := readGateFile(t, path)
		requireContains(t, path, content, fidelityInterventionHeading,
			"K15: the section the reader's output is spliced under")
		requireContains(t, path, content, "af turn interventions",
			"K15: the section is read through the verb, not reconstructed inline")
		requireContains(t, path, content, "harness-initiated",
			"AC 2: the judge must be told a listed intervention is the harness's action, not the agent's")
		requireContains(t, path, content, "not a deviation",
			"AC 2: and that it must not be graded as a deviation from the step contract")
	}
}

// commonPrefix / commonSuffix isolate the inserted section without re-declaring its rendering. The
// byte-identity claim is "everything else is untouched", and deriving the difference is the only way
// to state it that a change to the section's own wording cannot silently satisfy.
func commonPrefix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return a[:n]
}

func commonSuffix(a, b string) string {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return a[len(a)-n:]
}

// TestFidelityGateReadsStepContractSlimOff pins that hooks/fidelity-gate.sh reads the step contract via
// `af step current --json` and, on a non-`af done` turn whose `.state` is not "ready", emits {"ok":true}
// and exits 0 — slimming/grading is OFF when the contract read does not yield a ready step. It is a PURE
// test (option A: no .sh edit) driven end-to-end through the real gate script with a stubbed `af`.
//
// CRITICAL: this touches ONLY test files. It edits neither hooks/fidelity-gate.sh nor
// internal/cmd/install_hooks/fidelity-gate.sh (TestInstallHooks_NoDrift requires them byte-identical).
// The gate stub-af/PATH harness is hook_e2e_harness_test.go's; the ready-state grading path mirrors
// fidelity_gate_intervention_test.go's runTurn.
func TestFidelityGateReadsStepContractSlimOff(t *testing.T) {
	rig := newHookE2ERig(t)
	fidelity := hookE2EGates()[0]

	turnLines := []string{
		turnPrompt("u1", "execute the step"),
		turnCall("a1", "m1", "t1", "Bash", `{"command":"echo working"}`),
		turnResult("r1", "t1", "working", false),
	}

	// A fresh workDir has no recent last_closed_step, so IS_AF_DONE_TURN is false and the gate takes
	// the `af step current --json` branch — the read under test.
	t.Run("a non-ready step contract turns grading off", func(t *testing.T) {
		workDir := setupGateLockTestEnv(t)

		// `af step current --json` answers a non-ready state: no formula step is in flight to grade.
		hookE2EWriteRuntime(t, workDir, "stub_step.json", `{"state":"in_progress"}`+"\n")
		hookE2ESetVerdict(t, workDir, `{"ok": false, "reasoning": "would flag if reached"}`)
		path := hookE2EWriteTranscript(t, t.TempDir(), "turn.jsonl", turnLines...)

		out, code := rig.run(t, fidelity, workDir,
			hookE2EPayload(t, "continued without an active formula step", path), "")

		if code != 0 {
			t.Fatalf("gate exit %d on a non-ready step, want 0\n%s", code, out)
		}
		if strings.TrimSpace(out) != `{"ok": true}` {
			t.Errorf("gate output = %q, want exactly {\"ok\": true} — a non-ready contract read must "+
				"slim grading off, not grade", strings.TrimSpace(out))
		}
		// The judge stub records its input only when the gate reaches grading. Its absence proves the
		// gate short-circuited before the (would-be failing) verdict.
		if _, err := os.Stat(hookE2ERuntimePath(workDir, "judge_input_"+fidelity.name+".txt")); err == nil {
			t.Errorf("the gate reached the judge on a non-ready step contract; grading was not off")
		}
		for _, send := range hookE2EMailSends(t, workDir) {
			if send[1] == "STEP_FIDELITY" {
				t.Errorf("a STEP_FIDELITY verdict was mailed on a non-ready step contract: %v", send)
			}
		}
	})

	// The companion: a ready step contract on a non-af-done turn ⇒ the gate PROCEEDS to grade. This
	// is the control that proves the case above turns grading off for the contract read, not for some
	// unrelated reason that would suppress grading on every turn.
	t.Run("a ready step contract proceeds to grade", func(t *testing.T) {
		workDir := setupGateLockTestEnv(t)

		hookE2ESetStep(t, workDir, "bd-contract-step-1", "Step one")
		hookE2ESetVerdict(t, workDir, `{"ok": true, "reasoning": "follows the step contract"}`)
		path := hookE2EWriteTranscript(t, t.TempDir(), "turn.jsonl", turnLines...)

		out, code := rig.run(t, fidelity, workDir,
			hookE2EPayload(t, "executed the step as written", path), "")

		if code != 0 {
			t.Fatalf("gate exit %d on a ready step, want 0\n%s", code, out)
		}
		// Reaching the judge is the proof the ready contract was graded.
		if _, err := os.Stat(hookE2ERuntimePath(workDir, "judge_input_"+fidelity.name+".txt")); err != nil {
			t.Errorf("the gate did not reach the judge on a ready step contract; grading did not "+
				"proceed: %v\n%s", err, out)
		}
	})
}
