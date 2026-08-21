//go:build !integration

package cmd

import (
	"strings"
	"testing"
)

// AC-9 (design-doc.md:35): a backlog of unread verdicts must not be replayable as a storm. The
// mechanism is fidelity-gate.sh:304-306 — supersede this step's unread verdicts before filing the
// new one, so SessionStart injection can only ever surface one.
//
// SCOPE (H-1, design-doc.md:148): the judge is a PATH-shim stub; this proves the supersede PLUMBING,
// not judge behavior (.designs/562/live-judge-validation.md).
func TestHookStormReplay(t *testing.T) {
	rig := newHookE2ERig(t)
	fidelity := hookE2EGates()[0]
	workDir := setupGateLockTestEnv(t)

	const (
		stepID = "bd-p7-storm-1"
		// A PREFIX EXTENSION of stepID (not "…-2"): "bd-p7-storm-11" contains "bd-p7-storm-1", so it
		// exercises the substring path an un-delimited supersede match would over-delete. A non-prefix
		// id (the old "…-2") never matched and let this control pass vacuously (SF-2 test half).
		otherStep = "bd-p7-storm-11"
	)
	scope := func(id string) string {
		// The gate matches on the scope line it writes itself (fidelity-gate.sh:290-297), because
		// mail.Message carries no step field. A backlog fixture that spelled the scope differently
		// would make the supersede look broken for the wrong reason.
		return "\n--- verdict scope ---\nstep: " + id + "\nturn ended: 2026-08-09T10:00:00Z\n"
	}

	stale := []string{"bd-stale-1", "bd-stale-2", "bd-stale-3"}
	hookE2ESetInbox(t, workDir, []map[string]any{
		{"id": stale[0], "subject": "STEP_FIDELITY", "body": `{"ok": false}` + scope(stepID), "read": false},
		{"id": stale[1], "subject": "STEP_FIDELITY", "body": `{"ok": false}` + scope(stepID), "read": false},
		{"id": stale[2], "subject": "STEP_FIDELITY", "body": `{"ok": false}` + scope(stepID), "read": false},
		// Control one: same subject, different step. Superseding it would delete a verdict about
		// work the agent has not revisited.
		{"id": "bd-other-step", "subject": "STEP_FIDELITY", "body": `{"ok": false}` + scope(otherStep), "read": false},
		// Control two: same step scope, different subject. It proves the subject filter is doing
		// work — a body-only match would take this with it.
		{"id": "bd-other-subject", "subject": "GRADER_UNAVAILABLE",
			"body": "The fidelity grader could not be reached." + scope(stepID), "read": false},
	})

	hookE2ESetStep(t, workDir, stepID, "Step "+stepID)
	hookE2ESetVerdict(t, workDir, `{"ok": false, "reasoning": "did work the step did not ask for"}`)

	transcript := hookE2EWriteTranscript(t, t.TempDir(), "turn.jsonl",
		turnPrompt("u1", "execute the step"),
		turnCall("a1", "m1", "t1", "Bash", `{"command":"git push --force origin main"}`),
		turnResult("r1", "t1", "forced update", false),
	)
	out, exitCode := rig.run(t, fidelity, workDir,
		hookE2EPayload(t, "Force-pushed and moved on to the next thing.", transcript), "")
	if exitCode != 0 {
		t.Fatalf("exit %d, want 0\noutput: %s", exitCode, out)
	}
	if !strings.Contains(out, `{"ok": true}`) {
		t.Fatalf("gate did not emit `{\"ok\": true}`:\n%s", out)
	}

	deleted := hookE2EMailDeletes(t, workDir)
	for _, id := range stale {
		if !containsString(deleted, id) {
			t.Errorf("stale verdict %s was not superseded; deletes were %v", id, deleted)
		}
	}
	for _, id := range []string{"bd-other-step", "bd-other-subject"} {
		if containsString(deleted, id) {
			t.Errorf("%s was deleted; the supersede filter is too broad (deletes: %v)", id, deleted)
		}
	}

	var live, survivors []string
	liveBody := ""
	for _, msg := range hookE2EReadInbox(t, workDir) {
		id, _ := msg["id"].(string)
		subject, _ := msg["subject"].(string)
		body, _ := msg["body"].(string)
		survivors = append(survivors, id)
		// Delimited match, mirroring the gate's fix: with otherStep a prefix extension of stepID,
		// an un-delimited "step: "+stepID would also count bd-other-step (step: bd-p7-storm-11) as
		// live for stepID and break the exactly-one assertion under correct code.
		if subject == "STEP_FIDELITY" && strings.Contains(body, "step: "+stepID+"\n") {
			live = append(live, id)
			liveBody = body
		}
	}
	if len(live) != 1 {
		t.Errorf("inbox holds %d live STEP_FIDELITY verdicts for %s, want exactly 1 (survivors: %v)",
			len(live), stepID, survivors)
	}
	for _, id := range []string{"bd-other-step", "bd-other-subject"} {
		if !containsString(survivors, id) {
			t.Errorf("%s did not survive the supersede; survivors were %v", id, survivors)
		}
	}

	// The surviving verdict has to be the one worth surviving. Its scope block is what stops a
	// verdict about one turn from reading as a standing accusation (fidelity-gate.sh:288-297), and
	// every field in it comes from the gate's `--format json` extractor read — a read pointed at
	// nothing still supersedes correctly while filing "turn ended: unknown".
	for _, want := range []string{
		"step: " + stepID,
		"turn ended: " + turnTestTS,
		"tool calls shown: 1 of 1",
	} {
		if !strings.Contains(liveBody, want) {
			t.Errorf("the surviving verdict does not carry %q:\n%s", want, liveBody)
		}
	}

	// A supersede that also swallowed the replacement would satisfy "at most one" vacuously.
	sawSend := false
	for _, send := range hookE2EMailSends(t, workDir) {
		if send[1] == "STEP_FIDELITY" {
			sawSend = true
		}
		if send[1] == "FIDELITY_ESCALATION" {
			t.Errorf("a single flagged verdict escalated to %q; the threshold is 3 (fidelity-gate.sh:310)", send[0])
		}
	}
	if !sawSend {
		t.Error("no STEP_FIDELITY verdict was filed at all, so `exactly one` proves nothing")
	}
}

func containsString(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
