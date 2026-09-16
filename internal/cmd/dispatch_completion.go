package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// The completion-record rung of the sequential cap's release ladder. When a background child finishes,
// Claude Code writes a queue-operation/enqueue record into the LAUNCHER's transcript naming the child's
// tool_use_id with <status>completed</status>. That record exists only once the child has stopped with no
// live background children of its own, which is the one fact SubagentStop cannot give. Reading it frees
// the slot at the next launch attempt instead of after 1200s of sidechain silence.
//
// It is the single content read in a ladder that is otherwise stat/glob only, so it is bounded to a tail
// window behind a regular-file check. Every failure — no sidecar, a stamp that does not join, an
// unreadable transcript, no matching record, a bad timestamp — retains, so the rung can only turn a
// retain into an earlier release, never the reverse.

const (
	// sequentialClaimName is written beside sequential.slot at claim time and carries the launching
	// Agent's PreToolUse tool_use_id, which SubagentStop never reports. The sequential. prefix keeps both
	// ledger sweeps blind to it and lets clearDispatchReservations reap it with the slot.
	sequentialClaimName = "sequential.claim"

	// sequentialAuditName records which rung last freed the slot. If the completion record's format ever
	// drifts, releases fall back to the timer, and this file is how that shows up as "quiet" or "dwell"
	// rather than as unexplained 20-minute waits.
	sequentialAuditName = "sequential.audit"

	// slotCompletionTailWindow bounds the transcript read. The record lands within seconds of the child's
	// last write, so it is near the tail, and a PreToolUse hook must not pay for the whole file.
	slotCompletionTailWindow = 1 << 20
)

// slotClaimRecord is the sequential.claim sidecar. SlotStamp echoes sequential.slot's content so a
// sidecar left by a previous child cannot free the current one; ClaimedAt is the lower bound a matching
// record's timestamp must exceed.
type slotClaimRecord struct {
	V         int    `json:"v"`
	SlotStamp string `json:"slot_stamp"`
	ToolUseID string `json:"tool_use_id"`
	ClaimedAt string `json:"claimed_at"`
}

// recordSlotClaimToolUse writes the claim sidecar after a successful cap claim, re-reading the slot for
// its stamp as proposeSlotRelease does so both files carry the same bytes. Every failure is a silent
// no-op: without a sidecar the completion rung never fires and the slot falls to the timer. An empty
// tool_use_id writes nothing rather than a sidecar that can never match.
func recordSlotClaimToolUse(dir, toolUseID string, now time.Time) {
	if toolUseID == "" {
		return
	}
	stamp, err := os.ReadFile(filepath.Join(dir, sequentialSlotName))
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(slotClaimRecord{
		V:         sequentialStopVersion,
		SlotStamp: string(stamp),
		ToolUseID: toolUseID,
		ClaimedAt: now.UTC().Format(time.RFC3339Nano),
	}, "", "  ")
	if err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(filepath.Join(dir, sequentialClaimName), data, 0o644)
}

// readSlotClaim treats an absent, unreadable, corrupt, wrong-version or incomplete sidecar alike: nothing
// to act on, which retains.
func readSlotClaim(dir string) (slotClaimRecord, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, sequentialClaimName))
	if err != nil {
		return slotClaimRecord{}, false
	}
	var c slotClaimRecord
	if err := json.Unmarshal(raw, &c); err != nil {
		return slotClaimRecord{}, false
	}
	if c.V != sequentialStopVersion || c.SlotStamp == "" || c.ToolUseID == "" || c.ClaimedAt == "" {
		return slotClaimRecord{}, false
	}
	return c, true
}

// removeSlotClaim drops the previous child's sidecar when the slot is reclaimed so its tool_use_id cannot
// survive to match a later child's record. The stamp join already rejects a stale sidecar; this is
// defence in depth.
func removeSlotClaim(dir string) {
	_ = os.Remove(filepath.Join(dir, sequentialClaimName))
}

// slotCompletionRecorded joins the claim sidecar against the held slot and asks whether the parent
// transcript records this child's completion after the claim.
func slotCompletionRecorded(dir, held, transcriptPath string, now time.Time) bool {
	claim, ok := readSlotClaim(dir)
	if !ok {
		return false
	}
	if claim.SlotStamp != held {
		return false
	}
	claimedAt, err := time.Parse(time.RFC3339Nano, claim.ClaimedAt)
	if err != nil {
		return false
	}
	return transcriptHasCompletion(transcriptPath, claim.ToolUseID, claimedAt)
}

// queueOperationRecord is the subset of a transcript line the rung inspects. content is a string blob,
// not the block array the other transcript readers parse, which is why this needs its own scanner.
type queueOperationRecord struct {
	Type      string `json:"type"`
	Operation string `json:"operation"`
	Timestamp string `json:"timestamp"`
	Content   string `json:"content"`
}

// transcriptHasCompletion tail-reads the parent transcript for a queue-operation/enqueue record that names
// toolUseID with <status>completed</status> and a timestamp after the claim. All of those are required; a
// missing field, an unparseable line or a non-regular path is a no-match. The regular-file check is the
// posture subagentTranscriptQuiet takes on this host-supplied path: a directory's or FIFO's contents are
// not a measurement of a child.
func transcriptHasCompletion(transcriptPath, toolUseID string, claimedAt time.Time) bool {
	if transcriptPath == "" || toolUseID == "" {
		return false
	}
	f, err := os.Open(transcriptPath)
	if err != nil {
		return false
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	seeked := info.Size() > slotCompletionTailWindow
	if seeked {
		if _, err := f.Seek(info.Size()-slotCompletionTailWindow, io.SeekStart); err != nil {
			return false
		}
	}
	data, err := io.ReadAll(io.LimitReader(f, slotCompletionTailWindow))
	if err != nil {
		return false
	}
	lines := strings.Split(string(data), "\n")
	// A seek into the middle of the file can land mid-line, so the first fragment is not a whole record
	// and is dropped. A whole-file read (no seek) keeps every line — its first line is complete.
	if seeked && len(lines) > 0 {
		lines = lines[1:]
	}
	idToken := "<tool-use-id>" + toolUseID + "</tool-use-id>"
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, "{") {
			continue
		}
		var rec queueOperationRecord
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			continue
		}
		if rec.Type != "queue-operation" || rec.Operation != "enqueue" {
			continue
		}
		if !strings.Contains(rec.Content, idToken) || !strings.Contains(rec.Content, "<status>completed</status>") {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, rec.Timestamp)
		if err != nil || !ts.After(claimedAt) {
			continue
		}
		return true
	}
	return false
}

// slotReleaseRung names which rung of the ladder freed a slot. The zero value is the retain answer.
type slotReleaseRung string

const (
	rungRetain     slotReleaseRung = ""
	rungCompletion slotReleaseRung = "completion"
	rungQuiet      slotReleaseRung = "quiet"
	rungDwell      slotReleaseRung = "dwell"
)

// slotReleaseAudit is the sequential.audit breadcrumb: which rung freed the slot and when.
type slotReleaseAudit struct {
	V       int       `json:"v"`
	Rung    string    `json:"rung"`
	FreedAt time.Time `json:"freed_at"`
}

// writeSlotReleaseAudit records the rung that freed dir's slot. It writes into the ledger dir, the one
// directory every caller owns (tests hand claimSubagentSlot bare dirs), under the sequential. prefix so it
// is swept-blind and reaped with the slot. Best-effort: a lost breadcrumb costs counsel, never a wrong
// release.
func writeSlotReleaseAudit(dir string, rung slotReleaseRung, now time.Time) {
	data, err := json.MarshalIndent(slotReleaseAudit{V: sequentialStopVersion, Rung: string(rung), FreedAt: now.UTC()}, "", "  ")
	if err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(filepath.Join(dir, sequentialAuditName), data, 0o644)
}
