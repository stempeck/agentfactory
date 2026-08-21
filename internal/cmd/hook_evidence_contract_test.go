//go:build !integration

package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/stempeck/agentfactory/internal/transcript"
)

// These tests cover design 562 Phase 6: both Stop gates deriving their judge evidence from
// `af turn evidence` instead of reconstructing it inline, and the state the fidelity gate now
// writes for `af fidelity status` to read.
//
// They are TEXT pins over shell scripts. Text pins are honest about SHAPE and silent about
// REACHABILITY, so where a pin guards a new early exit it asserts POSITION as well as presence —
// see TestQualityGate_HasAfPathProbe. Phase 7 supplied the reachability half: TestHookAC7CompliantRun,
// TestHookStormReplay and TestHookPair_EvidenceParityOverRealTranscript drive real .jsonl
// transcripts through both scripts behind PATH shims (hook_e2e_harness_test.go). Those tests skip
// honestly when jq is absent, which is why the pins here stay — they hold on every machine.

// contractTruncationMarker is the one frozen marker (design-doc.md:102) with no Go constant:
// nothing in Go emits it, the fidelity hook does. Spelling it once here and asserting that both
// the script that emits it and the prompt that interprets it contain it is what stops those two
// from drifting apart.
const contractTruncationMarker = "[step description truncated: showing "

func fidelityGateFiles(repoRoot string) []string {
	return []string{
		filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "fidelity-gate.sh"),
	}
}

func qualityGateFiles(repoRoot string) []string {
	return []string{
		filepath.Join(repoRoot, "hooks", "quality-gate.sh"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "quality-gate.sh"),
	}
}

func gatePromptFiles(repoRoot string) []string {
	return []string{
		filepath.Join(repoRoot, "hooks", "fidelity-gate-prompt.txt"),
		filepath.Join(repoRoot, "hooks", "quality-gate-prompt.txt"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "fidelity-gate-prompt.txt"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "quality-gate-prompt.txt"),
	}
}

// gateLabel names a hook copy by its last two path elements, so hooks/fidelity-gate.sh and
// install_hooks/fidelity-gate.sh are distinguishable in a failure message.
func gateLabel(path string) string {
	return filepath.Join(filepath.Base(filepath.Dir(path)), filepath.Base(path))
}

func readGateFile(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func requireContains(t *testing.T, path, content, want, why string) {
	t.Helper()
	if !strings.Contains(content, want) {
		t.Errorf("%s must contain %q (%s)", gateLabel(path), want, why)
	}
}

func requireAbsent(t *testing.T, path, content, unwanted, why string) {
	t.Helper()
	if strings.Contains(content, unwanted) {
		t.Errorf("%s must no longer contain %q (%s)", gateLabel(path), unwanted, why)
	}
}

// TestGateHooks_UseSharedEvidenceExtractor is AC #1 and AC #11 as a test: both gates call
// `af turn evidence`, the call tolerates a binary that predates the subcommand, the fallback is
// the extractor's own unavailable marker rather than a second spelling of it, and the replaced
// construct is gone rather than merely shadowed.
func TestGateHooks_UseSharedEvidenceExtractor(t *testing.T) {
	repoRoot := findRepoRoot(t)
	// A bare call would exit 1 into an unset variable on an old binary; the failure has to be
	// absorbed on the call line itself.
	probed := regexp.MustCompile(`af turn evidence.*(2>/dev/null|\|\||if )`)

	for _, path := range gateHookFiles(repoRoot) {
		content := readGateFile(t, path)

		requireContains(t, path, content, "af turn evidence",
			"AC #1: both gates derive evidence from the one extractor")
		requireContains(t, path, content, `--transcript "$TRANSCRIPT"`,
			"`af turn evidence` ignores positional args; the path must be passed as a flag")
		requireContains(t, path, content, transcript.MarkerUnavailable,
			"the fallback must be the extractor's own frozen marker, not a second spelling")

		if !probed.MatchString(content) {
			t.Errorf("%s: the `af turn evidence` call must be probed / failure-tolerant (AC #11)",
				gateLabel(path))
		}

		// The replaced construct: a session-global reversed file, two independent head -5
		// passes, and no pairing between a call and the result it produced.
		requireAbsent(t, path, content, "head -5", "the reversed two-pass extraction is replaced")
		requireAbsent(t, path, content, "REVERSE=", "the tac/tail -r platform switch is replaced")
		requireAbsent(t, path, content, "select(.message.content", "the inline jq extraction is replaced")
	}
}

// TestGatePrompts_DocumentEveryExtractorMarker is AC #9, the format-contract test
// internal/cmd/turn.go:15-24 was written expecting: "the gate prompt Phase 6 ships documents
// exactly these strings, and its format-contract test asserts the prompt and this renderer
// agree."
//
// Every expected string is DERIVED from the renderer, never re-spelled here — a second,
// correct-today copy would pass this test and break the next change to the format instead of
// failing loudly. The two markers carrying variable numbers are split on their digits, so EVERY
// literal segment between the numbers is asserted, not just the outer two.
func TestGatePrompts_DocumentEveryExtractorMarker(t *testing.T) {
	repoRoot := findRepoRoot(t)

	markers := []struct {
		text string
		why  string
	}{
		{evidenceHeader, "rule 1 (ordering) keys on the block's own header"},
		{transcript.MarkerNoCalls, "rule 3 (empty turn) treats this line as authoritative"},
		{transcript.MarkerUnavailable, "rule 4 (evidence unavailable) must be distinguishable from rule 3"},
	}
	for _, seg := range literalSegments(partialMarkerFor(25, 7, 3)) {
		markers = append(markers, struct {
			text string
			why  string
		}{seg, "rule 2 (partial evidence) must quote every word of the cap marker it keys on"})
	}
	for _, seg := range literalSegments(fmt.Sprintf(turnIncompleteFormat, false, 2)) {
		markers = append(markers, struct {
			text string
			why  string
		}{seg, "a degraded-but-non-empty turn has its own disclosure the judge must know"})
	}

	for _, path := range gatePromptFiles(repoRoot) {
		content := readGateFile(t, path)
		for _, m := range markers {
			requireContains(t, path, content, m.text, m.why)
		}
	}
}

// TestFidelityContractTruncation_ScriptAndPromptAgree pins AC #4 on both sides of the seam: the
// script that annotates a cut contract, and the prompt rule that tells the judge not to block for
// directives that annotation says it was never shown.
func TestFidelityContractTruncation_ScriptAndPromptAgree(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for _, path := range fidelityGateFiles(repoRoot) {
		content := readGateFile(t, path)
		requireContains(t, path, content, "head -c 4096",
			"the 4KB cap value is unchanged; only the annotation is added")
		requireContains(t, path, content, contractTruncationMarker,
			"AC #4: a contract the judge was shown only part of must say so")
	}

	for _, name := range []string{
		filepath.Join(repoRoot, "hooks", "fidelity-gate-prompt.txt"),
		filepath.Join(repoRoot, "internal", "cmd", "install_hooks", "fidelity-gate-prompt.txt"),
	} {
		requireContains(t, name, readGateFile(t, name), contractTruncationMarker,
			"the partial-contract rule must quote the marker the script emits")
	}
}

// TestGateHooks_SelfSendsWake enforces the owner directive on PR #608: `--no-wake` was removed
// and mail must always wake, so a gate self-send must NOT suppress the wake. It is LINE-scoped on
// purpose: a stray `--no-wake` on a backslash-continued next line reads as correct otherwise.
func TestGateHooks_SelfSendsWake(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for _, path := range gateHookFiles(repoRoot) {
		selfSends := 0
		for _, line := range strings.Split(readGateFile(t, path), "\n") {
			if strings.Contains(line, `af mail send "$ROLE"`) {
				selfSends++
				if strings.Contains(line, "--no-wake") {
					t.Errorf("%s: self-addressed send must NOT carry --no-wake — mail must wake (PR #608): %s",
						gateLabel(path), strings.TrimSpace(line))
				}
			}
			// D-7 / C-1: the supervisor channel is the one push path to an overseer. Silencing
			// it would make every escalation invisible.
			if strings.Contains(line, "af mail send supervisor") {
				if strings.Contains(line, "--no-wake") {
					t.Errorf("%s: the supervisor send must stay waking: %s",
						gateLabel(path), strings.TrimSpace(line))
				}
				if !strings.Contains(line, "--report-delivery") {
					t.Errorf("%s: the supervisor send must report real delivery (ADR-007 amendment 1): %s",
						gateLabel(path), strings.TrimSpace(line))
				}
			}
		}
		// Without this the rule above is vacuously satisfiable by having no self-sends at all.
		if selfSends < 1 {
			t.Errorf("%s: expected at least one self-addressed gate mail", gateLabel(path))
		}
	}
}

// TestQualityGate_HasAfPathProbe is AC #3 plus the placement that makes it safe. The quality gate
// previously had no `af` probe at all while calling `af root` and `af mail send`. Position is
// asserted because TestGateLockFile_StaleRecovery and _LivePIDBlocks EXECUTE this script and
// assert EXIT4a/EXIT4b reach the debug log: a probe placed before the lock block short-circuits
// before those labels are ever written, which passes on a developer box (where `af` is on PATH)
// and fails in CI (where it is not).
func TestQualityGate_HasAfPathProbe(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for _, path := range qualityGateFiles(repoRoot) {
		content := readGateFile(t, path)
		probe := strings.Index(content, "command -v af")
		if probe < 0 {
			t.Errorf("%s: must fail open when `af` is not on PATH (AC #3)", gateLabel(path))
			continue
		}
		lock := strings.Index(content, "LOCKFILE=")
		if lock < 0 || probe < lock {
			t.Errorf("%s: the `af` probe must come after the lock block, or the lock tests lose "+
				"their EXIT4a/EXIT4b evidence in CI", gateLabel(path))
		}
	}
}

// TestFidelityGate_EscalationLatchesOncePerStep covers AC #2 and AC-6 together: the deterrent text
// stays byte-for-byte (owner decision, PR #608 — retraction NOT ACCEPTED) and the accuracy fixes
// compose around it.
func TestFidelityGate_EscalationLatchesOncePerStep(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for _, path := range fidelityGateFiles(repoRoot) {
		content := readGateFile(t, path)

		requireContains(t, path, content, "SESSION WILL BE TERMINATED",
			"AC #2: retained verbatim by owner decision")
		requireContains(t, path, content, "fidelity_escalated_step",
			"data.md R-2: escalation fires once per (step, threshold crossing)")
		requireContains(t, path, content, "--report-delivery",
			"the supervisor claim must be worded from a real delivery report")
		// "Supervisor notified" may only be claimed when the delivery report said so; the default
		// claim is the weaker one that is always true.
		requireContains(t, path, content, "Supervisor notified.",
			"the strong claim exists and is reachable only from a confirmed delivery report")
		requireContains(t, path, content, "Supervisor copy filed",
			"knowability-scoped default claim (C-1)")
		requireAbsent(t, path, content, "consecutive",
			"the counter measures flagged evaluations on this step since the last pass (Gap 10b)")
	}
}

// TestFidelityGate_CoalescesVerdictBeads covers K6: a mail backlog must not be replayable as a
// storm through SessionStart injection. Deleting a bead is closing it (internal/mail/mailbox.go:92-96).
func TestFidelityGate_CoalescesVerdictBeads(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for _, path := range fidelityGateFiles(repoRoot) {
		content := readGateFile(t, path)
		requireContains(t, path, content, "af mail inbox --json",
			"the prior unread verdict has to be found before it can be superseded")
		requireContains(t, path, content, "af mail delete",
			"close = delete in the beads model")
		// mail.Message has no step field, so the step id can only be matched out of the body —
		// which is why the verdict scope block is load-bearing rather than decorative.
		requireContains(t, path, content, "verdict scope",
			"the scope block carries the step id that scopes coalescing")
	}
}

// TestFidelityGate_AppendsRunRecord is the K7 writer side of the record Phase 5 already reads.
// The key set is taken by reflection off the reader's own struct rather than from a literal list,
// so writer and reader cannot drift.
func TestFidelityGate_AppendsRunRecord(t *testing.T) {
	repoRoot := findRepoRoot(t)

	recordType := reflect.TypeOf(fidelityRecord{})
	for _, path := range fidelityGateFiles(repoRoot) {
		content := readGateFile(t, path)

		requireContains(t, path, content, "fidelity_log.jsonl",
			"AC-7 needs a run record to be verifiable from")
		requireContains(t, path, content, "--format json",
			"calls_total/calls_shown come from the extractor's json form")

		for i := 0; i < recordType.NumField(); i++ {
			tag := strings.Split(recordType.Field(i).Tag.Get("json"), ",")[0]
			if tag == "" {
				continue
			}
			requireContains(t, path, content, tag,
				"the writer must emit every key internal/cmd/fidelity.go reads")
		}

		// A `>` here would silently destroy the whole history the status reader tails.
		if !regexp.MustCompile(`>>\s*"?\$(\{)?RUN_RECORD`).MatchString(content) &&
			!strings.Contains(content, `>> "$AGENT_RUNTIME/fidelity_log.jsonl"`) {
			t.Errorf("%s: the run record must be APPENDED, never truncated", gateLabel(path))
		}
	}
}

// TestFidelityGate_CounterAndNoticeSemantics covers data.md R-1 and required change 4: an
// unparseable verdict is not a pass, and a transcript that yielded nothing is surfaced once
// rather than every turn.
func TestFidelityGate_CounterAndNoticeSemantics(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for _, path := range fidelityGateFiles(repoRoot) {
		content := readGateFile(t, path)

		requireContains(t, path, content, ".ok == true",
			"R-1: the counter resets only on an explicitly parsed pass")
		requireContains(t, path, content, "extraction_unavailable",
			"required change 4: the transcript grew but nothing parsed")
		requireContains(t, path, content, "grader_notice_",
			"the notice reuses the idempotent .runtime marker idiom, so it is one mail, not a storm")
	}
}

// TestFidelityGate_CounterIsStepScoped pins the counter and the latch to the SAME scope.
//
// Both escalation messages state the count is "on this step" and the IMPLREADME defines it that way
// ("flagged evaluations on this step since the last pass"), while the latch clears on a step change
// per data.md R-2. Clear one without the other and the first flagged turn of a fresh step escalates
// on a count inherited from the previous one — carrying the retained SESSION WILL BE TERMINATED
// threat, over a number the message misdescribes. The two are one step-scoped fact.
func TestFidelityGate_CounterIsStepScoped(t *testing.T) {
	repoRoot := findRepoRoot(t)

	for _, path := range fidelityGateFiles(repoRoot) {
		content := readGateFile(t, path)

		clear := regexp.MustCompile(`(?s)!= "\$STEP_ID".*?\bfi\b`).FindString(content)
		if clear == "" {
			t.Fatalf("%s: no step-change branch found; the counter and latch scopes cannot be checked",
				gateLabel(path))
		}
		// LATCH_FILE rather than the filename: the branch clears it through the variable, and
		// TestFidelityGate_EscalationLatchesOncePerStep already pins what that variable holds.
		for _, state := range []string{"LATCH_FILE", "fidelity_violations"} {
			if !strings.Contains(clear, state) {
				t.Errorf("%s: the step-change branch does not clear %s.\n"+
					"Both escalation messages claim the count is scoped to this step, so the counter "+
					"must be spent where the latch is.\nBranch:\n%s", gateLabel(path), state, clear)
			}
		}
	}
}

// partialMarkerFor renders the frozen cap marker for a turn of `total` calls showing `shown` of
// them, by asking the evidence record for it. The format string is unexported on purpose
// (internal/transcript/evidence.go:42) — this is the supported way to obtain it.
func partialMarkerFor(total, shown, head int) string {
	calls := make([]transcript.Call, 0, shown)
	for i := 1; i <= head; i++ {
		calls = append(calls, transcript.Call{Seq: i})
	}
	for i := total - (shown - head) + 1; i <= total; i++ {
		calls = append(calls, transcript.Call{Seq: i})
	}
	return transcript.Evidence{
		Turn:  transcript.TurnMeta{BoundaryUUID: "u1", CallsTotal: total, CallsShown: shown},
		Calls: calls,
	}.Marker()
}

// literalSegments splits one rendering of a marker on its runs of digits, returning the frozen
// words between them. Everything in these markers that varies per turn is a number, so what is left
// is exactly the wording the prompt has to quote — derived from the renderer, never re-spelled here.
//
// Comparing two renderings and keeping the common prefix and suffix would be the obvious way to do
// this and is not enough: it pins only the outer two segments, so " + last %d of %d calls; " could
// be reworded with this test still green and prompt rule E2 left quoting a marker that no longer
// exists.
func literalSegments(rendered string) []string {
	var segments []string
	for _, seg := range strings.FieldsFunc(rendered, unicode.IsDigit) {
		if seg != "" {
			segments = append(segments, seg)
		}
	}
	return segments
}
