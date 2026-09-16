//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Pins the completion-record release rung: a finished child's queue-operation/enqueue record in the parent
// transcript, matched by the slot's own tool_use_id, frees the slot before the 1200s timer, and every
// failure leg still falls through to the timer.

// completionFixtureEnvelope mirrors the stamped testdata/dispatch_completion_enqueue_*.json shape: the
// byte-for-byte queue-operation/enqueue record captured from a real run, plus the join fields a test
// asserts against. See testdata/dispatch_README.md for what "captured" means and what it does not.
type completionFixtureEnvelope struct {
	CLIVersion string `json:"cli_version"`
	Provenance string `json:"provenance"`
	ToolUseID  string `json:"tool_use_id"`
	TaskID     string `json:"task_id"`
	Record     string `json:"record"`
}

// loadCompletionFixture is fatal on every failure leg for readDispatchFixture's reason: a committed
// capture that has gone missing is a broken test, not a skip.
func loadCompletionFixture(t *testing.T, name string) completionFixtureEnvelope {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("captured fixture %s is unreadable: %v — it is captured from a real CLI run and cannot be "+
			"regenerated from our own code, so this is a failure, not a skip", name, err)
	}
	var env completionFixtureEnvelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("fixture %s does not decode: %v", name, err)
	}
	if env.ToolUseID == "" || env.Record == "" {
		t.Fatalf("fixture %s is missing tool_use_id/record", name)
	}
	return env
}

// writeClaimSidecar plants the sequential.claim claim-time sidecar the release rung joins on:
// {v, slot_stamp, tool_use_id, claimed_at}, slot_stamp echoing the slot content for the anti-replay join.
func writeClaimSidecar(t *testing.T, dir, slotStamp, toolUseID string, claimedAt time.Time) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"v":           1,
		"slot_stamp":  slotStamp,
		"tool_use_id": toolUseID,
		"claimed_at":  claimedAt.UTC().Format(time.RFC3339Nano),
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "sequential.claim"), raw, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeStopProposal(t *testing.T, dir, slotStamp, transcriptPath string, stopAt time.Time) {
	t.Helper()
	prop, err := json.Marshal(slotReleaseProposal{
		V:              sequentialStopVersion,
		SlotStamp:      slotStamp,
		StopAt:         stopAt.UTC().Format(time.RFC3339Nano),
		TranscriptPath: transcriptPath,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sequentialStopName), prop, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestSlotReleasable_CompletionRecordReleasesBeforeTimer is the load-bearing pin for the rung. With a
// matching completion record in the parent transcript — the slot's tool_use_id, <status>completed</status>,
// timestamp after the claim — the slot must release NOW rather than waiting out subagentQuietReleaseSecs.
//
// The evidence ladder is faked to a recent, measured quiet (10s) so that WITHOUT the completion rung the
// quiet compare RETAINS; anything releasable came back on the completion record's authority alone.
func TestSlotReleasable_CompletionRecordReleasesBeforeTimer(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const held = "cap-stamp-42\n"

	evidence := fakeSubagentQuietEvidence(t)
	evidence.quiet, evidence.measured = 10*time.Second, true // the timer would still be waiting

	env := loadCompletionFixture(t, "dispatch_completion_enqueue_2_1_251.json")
	transcript := filepath.Join(dir, "parent-transcript.jsonl")
	if err := os.WriteFile(transcript,
		[]byte(`{"type":"user","content":"noise before"}`+"\n"+env.Record+"\n"+
			`{"type":"assistant","content":"noise after"}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStopProposal(t, dir, held, transcript, now.Add(-time.Minute))
	// Claim recorded well before the captured record's own timestamp, so "timestamp after the claim" holds.
	writeClaimSidecar(t, dir, held, env.ToolUseID, time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))

	if !slotReleasable(dir, held, now) {
		t.Error("a matching completion record in the parent transcript did not release the slot; the rung " +
			"must free a finished child before the 1200s timer")
	}
}

// TestSlotReleasable_CompletionMismatchFallsToTimer is the fail-safe (C-4) guard: a claim sidecar plus a
// transcript that carries a completion record for a DIFFERENT child (wrong tool_use_id) must NOT release
// early — the ambiguity falls through to the unchanged quiet/timer ladder. Holds before AND after C ships.
func TestSlotReleasable_CompletionMismatchFallsToTimer(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const held = "cap-stamp-99\n"

	evidence := fakeSubagentQuietEvidence(t)
	evidence.quiet, evidence.measured = 10*time.Second, true // recent ⇒ timer retains

	other := `{"type":"queue-operation","operation":"enqueue","timestamp":"2026-08-29T00:33:23.020Z",` +
		`"content":"<task-notification>\n<tool-use-id>toolu_SOMEONE_ELSE</tool-use-id>\n<status>completed</status>\n</task-notification>"}`
	transcript := filepath.Join(dir, "parent-transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(other+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStopProposal(t, dir, held, transcript, now.Add(-time.Minute))
	writeClaimSidecar(t, dir, held, "toolu_THE_HELD_CHILD", time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC))

	if slotReleasable(dir, held, now) {
		t.Error("slotReleasable released on a completion record belonging to a DIFFERENT child; the rung must " +
			"match the slot's own tool_use_id or fall through to the timer")
	}
}

// TestSlotReleasable_CompletionBeforeClaimFallsToTimer guards the anti-replay direction: a record whose
// timestamp is NOT after the claim is a leftover from a previous child and must never free the current
// slot. Holds before AND after C ships.
func TestSlotReleasable_CompletionBeforeClaimFallsToTimer(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	const held = "cap-stamp-7\n"

	evidence := fakeSubagentQuietEvidence(t)
	evidence.quiet, evidence.measured = 10*time.Second, true

	env := loadCompletionFixture(t, "dispatch_completion_enqueue_2_1_224.json")
	transcript := filepath.Join(dir, "parent-transcript.jsonl")
	if err := os.WriteFile(transcript, []byte(env.Record+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeStopProposal(t, dir, held, transcript, now.Add(-time.Minute))
	// Claim recorded AFTER the captured record's timestamp (2026-08-22): the record predates this claim.
	writeClaimSidecar(t, dir, held, env.ToolUseID, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC))

	if slotReleasable(dir, held, now) {
		t.Error("slotReleasable released on a completion record older than the claim; a record must be strictly " +
			"after claimed_at to free THIS child's slot, or a previous child's record frees every slot after it")
	}
}

// --- Write side: the payload field, the claim sidecar, the scanner's negative legs, and the audit
// breadcrumb are pinned here directly.

// TestDispatchAdmitPayload_DecodesToolUseID pins clause A1: tool_use_id is a top-level PreToolUse sibling
// that decodes through the existing whole-object Decode, and an absent one decodes to "" (degrade to timer).
func TestDispatchAdmitPayload_DecodesToolUseID(t *testing.T) {
	var p dispatchAdmitPayload
	if err := json.Unmarshal([]byte(`{"tool_name":"Task","cwd":"/w","tool_use_id":"toolu_abc"}`), &p); err != nil {
		t.Fatal(err)
	}
	if p.ToolUseID != "toolu_abc" {
		t.Errorf("decoded tool_use_id = %q, want %q — the completion rung has nothing to join on without it", p.ToolUseID, "toolu_abc")
	}
	var absent dispatchAdmitPayload
	if err := json.Unmarshal([]byte(`{"tool_name":"Task"}`), &absent); err != nil {
		t.Fatal(err)
	}
	if absent.ToolUseID != "" {
		t.Errorf("a payload with no tool_use_id decoded to %q, want \"\" so the fast-path degrades to the timer", absent.ToolUseID)
	}
}

// TestRecordSlotClaimToolUse pins clause A2's writer: a valid claim echoes the slot stamp and round-trips
// through readSlotClaim; an empty id writes nothing (degrade to timer, never an unmatchable sidecar); an
// absent slot writes nothing (no stamp to echo).
func TestRecordSlotClaimToolUse(t *testing.T) {
	now := time.Now()

	empty := t.TempDir()
	if err := os.WriteFile(filepath.Join(empty, sequentialSlotName), []byte("stamp-A\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	recordSlotClaimToolUse(empty, "", now)
	if _, ok := readSlotClaim(empty); ok {
		t.Error("an empty tool_use_id wrote a claim sidecar; an id-less host must degrade to the timer, not to a sidecar that can never match")
	}

	noSlot := t.TempDir()
	recordSlotClaimToolUse(noSlot, "toolu_X", now)
	if _, ok := readSlotClaim(noSlot); ok {
		t.Error("a claim was recorded with no slot to echo; without the slot stamp there is no anti-replay join")
	}

	recordSlotClaimToolUse(empty, "toolu_X", now)
	claim, ok := readSlotClaim(empty)
	if !ok {
		t.Fatal("a valid claim left no readable sidecar")
	}
	if claim.SlotStamp != "stamp-A\n" || claim.ToolUseID != "toolu_X" || claim.V != sequentialStopVersion {
		t.Errorf("claim = %+v, want slot_stamp echo of the slot, tool_use_id toolu_X, v %d", claim, sequentialStopVersion)
	}
	if _, err := time.Parse(time.RFC3339Nano, claim.ClaimedAt); err != nil {
		t.Errorf("claimed_at %q is not RFC3339Nano: %v — the release rung parses it to compare against the record", claim.ClaimedAt, err)
	}
}

// completionLine builds one transcript queue-operation/enqueue line the scanner reads. Callers mutate the
// struct to drive each negative-predicate leg.
func completionLine(t *testing.T, rec queueOperationRecord) string {
	t.Helper()
	raw, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

// TestTranscriptHasCompletion_MatchAndNegatives pins the five mandatory predicates: drop any one and the
// match must fall through to a no-match.
func TestTranscriptHasCompletion_MatchAndNegatives(t *testing.T) {
	claimedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	const id = "toolu_MATCH"
	good := queueOperationRecord{
		Type:      "queue-operation",
		Operation: "enqueue",
		Timestamp: claimedAt.Add(time.Hour).Format(time.RFC3339Nano),
		Content:   "<task-notification>\n<tool-use-id>" + id + "</tool-use-id>\n<status>completed</status>\n</task-notification>",
	}
	cases := []struct {
		name   string
		mutate func(r *queueOperationRecord)
		want   bool
	}{
		{"exact match releases", func(*queueOperationRecord) {}, true},
		{"wrong type retains", func(r *queueOperationRecord) { r.Type = "assistant" }, false},
		{"wrong operation retains", func(r *queueOperationRecord) { r.Operation = "dequeue" }, false},
		{"status not completed retains", func(r *queueOperationRecord) {
			r.Content = strings.Replace(r.Content, "<status>completed</status>", "<status>in_progress</status>", 1)
		}, false},
		{"different tool_use_id retains", func(r *queueOperationRecord) {
			r.Content = strings.Replace(r.Content, id, "toolu_OTHER", 1)
		}, false},
		{"timestamp before claim retains", func(r *queueOperationRecord) {
			r.Timestamp = claimedAt.Add(-time.Hour).Format(time.RFC3339Nano)
		}, false},
		{"timestamp equal to claim retains (must be strictly after)", func(r *queueOperationRecord) {
			r.Timestamp = claimedAt.Format(time.RFC3339Nano)
		}, false},
		{"unparseable timestamp retains", func(r *queueOperationRecord) { r.Timestamp = "not-a-time" }, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			rec := good
			tc.mutate(&rec)
			transcript := filepath.Join(dir, "t.jsonl")
			if err := os.WriteFile(transcript,
				[]byte(`{"type":"user"}`+"\n"+completionLine(t, rec)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if got := transcriptHasCompletion(transcript, id, claimedAt); got != tc.want {
				t.Errorf("transcriptHasCompletion = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestTranscriptHasCompletion_FailSafeLegs pins the input-failure legs: an empty/absent/non-regular path,
// an empty id, and a transcript of only non-JSON noise are every one a no-match (retain).
func TestTranscriptHasCompletion_FailSafeLegs(t *testing.T) {
	claimedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	dir := t.TempDir()

	if transcriptHasCompletion("", "toolu_X", claimedAt) {
		t.Error("an empty transcript path matched; a missing hint must retain")
	}
	if transcriptHasCompletion(filepath.Join(dir, "nope.jsonl"), "toolu_X", claimedAt) {
		t.Error("an absent transcript matched; a vanished path must retain")
	}
	if transcriptHasCompletion(dir, "", claimedAt) {
		t.Error("an empty tool_use_id matched; with nothing to join on the rung must retain")
	}
	if transcriptHasCompletion(dir, "toolu_X", claimedAt) {
		t.Error("a DIRECTORY matched; a non-regular path is not a measurement of a child (retain)")
	}
	noise := filepath.Join(dir, "noise.jsonl")
	if err := os.WriteFile(noise, []byte("not json\n\nstill not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if transcriptHasCompletion(noise, "toolu_X", claimedAt) {
		t.Error("a transcript of only non-JSON lines matched; unparseable content must retain")
	}
}

// TestSlotCompletionRecorded_Guards pins the anti-replay join: no sidecar, or a sidecar
// whose slot_stamp does not equal the held claim, is a no-match — a leftover sidecar from a previous child
// can never free the current slot even when the transcript carries its completion record.
func TestSlotCompletionRecorded_Guards(t *testing.T) {
	now := time.Now()
	const held = "held-stamp\n"
	const id = "toolu_HELD"
	claimedAt := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	rec := queueOperationRecord{
		Type:      "queue-operation",
		Operation: "enqueue",
		Timestamp: claimedAt.Add(time.Hour).Format(time.RFC3339Nano),
		Content:   "<task-notification>\n<tool-use-id>" + id + "</tool-use-id>\n<status>completed</status>\n</task-notification>",
	}

	dir := t.TempDir()
	transcript := filepath.Join(dir, "t.jsonl")
	if err := os.WriteFile(transcript, []byte(completionLine(t, rec)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if slotCompletionRecorded(dir, held, transcript, now) {
		t.Error("released with NO claim sidecar; the rung must have a claim to join on")
	}

	writeClaimSidecar(t, dir, "SOME-OTHER-STAMP\n", id, claimedAt)
	if slotCompletionRecorded(dir, held, transcript, now) {
		t.Error("released on a sidecar whose slot_stamp does not equal the held claim; a previous child's sidecar must never free the current slot")
	}

	writeClaimSidecar(t, dir, held, id, claimedAt)
	if !slotCompletionRecorded(dir, held, transcript, now) {
		t.Error("a sidecar whose stamp joins AND whose id matches a completed record after the claim did not release")
	}
}

// TestWriteSlotReleaseAudit_RoundTrips pins the audit breadcrumb: the rung that freed
// the slot is recorded under the fixed sequential.audit name, versioned, so a completion-format drift
// surfaces as the timer firing rather than a silent 20-minute wait.
func TestWriteSlotReleaseAudit_RoundTrips(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()
	writeSlotReleaseAudit(dir, rungCompletion, now)

	raw, err := os.ReadFile(filepath.Join(dir, sequentialAuditName))
	if err != nil {
		t.Fatalf("no audit breadcrumb written: %v", err)
	}
	var a slotReleaseAudit
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatalf("audit breadcrumb does not decode: %v", err)
	}
	if a.Rung != string(rungCompletion) || a.V != sequentialStopVersion {
		t.Errorf("audit = %+v, want rung %q, v %d", a, rungCompletion, sequentialStopVersion)
	}
	if !isCapStateFile(sequentialAuditName) {
		t.Error("sequential.audit is not swept-blind; a cap sweep deleting it is harmless but reading it as an arithmetic marker miscounts")
	}
}

// TestCapAdmit_RecordsClaimSidecarWithToolUseID is the end-to-end wiring pin (clauses A1+A2): a first cap
// sub-agent admitted with a PreToolUse tool_use_id leaves a sequential.claim sidecar carrying that id, its
// slot_stamp echoing the slot content — exactly the join the completion rung checks. Without this the whole
// candidate-C fast-path is dark.
func TestCapAdmit_RecordsClaimSidecarWithToolUseID(t *testing.T) {
	now := time.Now()
	fakeSubagentQuietEvidence(t)
	fx := newLifecycleFixture(t)
	armTokenomics(t, fx.root, 10, 1)
	writeCapBackendModels(t, fx.root)
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 10, 1000, now.Add(-10*time.Second), now)

	const id = "toolu_LAUNCHER_9"
	var out bytes.Buffer
	if err := runDispatchAdmitCore(t.Context(), &out,
		dispatchAdmitPayload{ToolName: "Task", Cwd: fx.workDir, ToolUseID: id}, now); err != nil {
		t.Fatalf("runDispatchAdmitCore: %v", err)
	}
	if out.Len() != 0 {
		t.Fatalf("the first cap sub-agent was not admitted silently:\n%s", out.String())
	}

	ledger := reservationDir(fx.workDir, capBackendKey)
	claim, ok := readSlotClaim(ledger)
	if !ok {
		t.Fatal("a cap claim carrying a tool_use_id left no sequential.claim sidecar; the completion rung can never fire without it")
	}
	if claim.ToolUseID != id {
		t.Errorf("claim sidecar carries tool_use_id %q, want %q", claim.ToolUseID, id)
	}
	stamp, err := os.ReadFile(filepath.Join(ledger, sequentialSlotName))
	if err != nil {
		t.Fatal(err)
	}
	if claim.SlotStamp != string(stamp) {
		t.Errorf("claim slot_stamp %q does not echo the slot content %q; the SlotStamp==held join would reject it",
			claim.SlotStamp, string(stamp))
	}
}
