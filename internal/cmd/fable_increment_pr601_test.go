package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/statusline"
)

// Pinning tests for the UNRESOLVED review comments of PR #601 (fable-increment, issue #600 Phase 3).
// Each names the thread it pins. RED against PR-head code; must pass after the fix.

// T5 — a transcript REPLACED with a SAME-SIZE file whose pre-offset content differs must reset the
// cursor and re-derive, not seek to the stale offset and skip the new pre-offset records.
// RED today: ResetIfTruncated only fires on shrink, so the "new-A" record before the stale offset is
// permanently skipped and the counter undercounts.
func TestFableIncr601_T5_SameSizeReplacementResets(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "s.jsonl")

	// Original: countable A (40+60=100) then a held-back trailing B. Read advances the offset past A.
	origA := transcriptRecord("aa1", 40, 60)
	origB := transcriptRecord("aa2", 11, 11)
	writeTranscript(t, tp, origA, origB)

	cur, err := readTranscriptDelta(tp, statusline.TranscriptCursor{})
	if err != nil {
		t.Fatalf("first read: %v", err)
	}
	if cur.CumTokens != 100 {
		t.Fatalf("setup: CumTokens = %d, want 100 (A counted, B held back)", cur.CumTokens)
	}

	// Replace with a SAME-SIZE file: new countable A' (70+80=150) then trailing B'. Equal-length
	// message ids and equal-digit token counts keep the byte size identical, so the shrink check
	// cannot see the replacement.
	before, _ := os.Stat(tp)
	newA := transcriptRecord("bb1", 70, 80)
	newB := transcriptRecord("bb2", 11, 11)
	writeTranscript(t, tp, newA, newB)
	after, _ := os.Stat(tp)
	if before.Size() != after.Size() {
		t.Fatalf("test precondition: replacement is not same-size (%d vs %d)", before.Size(), after.Size())
	}

	got, err := readTranscriptDelta(tp, cur)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	// The replacement's A' (150 tokens) sits BEFORE the stale offset. The bug skips it (CumTokens
	// stays 100); the fix resets on the head-signature mismatch and counts it.
	if got.CumTokens < 150 {
		t.Fatalf("T5: CumTokens = %d after a same-size replacement, want >= 150 — the new pre-offset "+
			"record was skipped (rotation undercount)", got.CumTokens)
	}
}

// T5 — a LARGER replacement (offset still fits the new size) has the same blind spot as same-size:
// the shrink check passes, so without a generation signal the stale offset skips the new head.
func TestFableIncr601_T5_LargerReplacementResets(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, tp, transcriptRecord("aa1", 40, 60), transcriptRecord("aa2", 11, 11))

	cur, err := readTranscriptDelta(tp, statusline.TranscriptCursor{})
	if err != nil || cur.CumTokens != 100 {
		t.Fatalf("setup: cur=%+v err=%v, want CumTokens 100", cur, err)
	}

	// Replace with a LARGER file (three records) whose head differs.
	writeTranscript(t, tp,
		transcriptRecord("bb1", 70, 80),
		transcriptRecord("bb2", 90, 90),
		transcriptRecord("bb3", 11, 11),
	)
	got, err := readTranscriptDelta(tp, cur)
	if err != nil {
		t.Fatalf("second read: %v", err)
	}
	// New head bb1 (150) + bb2 (180) = 330 are before the trailing bb3; the bug skips the ones
	// before the stale offset.
	if got.CumTokens < 330 {
		t.Fatalf("T5: CumTokens = %d after a larger replacement, want >= 330 — pre-offset records skipped", got.CumTokens)
	}
}

// T5 protective — a NORMAL append (append-only growth) must NOT reset: the head is unchanged, so the
// offset stays valid and msg_a is not re-counted. Guards against a signal that resets on every append.
func TestFableIncr601_T5_NormalAppendDoesNotReset(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, tp, transcriptRecord("aa1", 40, 60), transcriptRecord("aa2", 11, 11))
	cur, err := readTranscriptDelta(tp, statusline.TranscriptCursor{})
	if err != nil || cur.CumTokens != 100 {
		t.Fatalf("setup: cur=%+v err=%v", cur, err)
	}

	// Append a third record (same head bytes).
	f, err := os.OpenFile(tp, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	fmt.Fprintln(f, transcriptRecord("aa3", 30, 30))
	f.Close()

	got, err := readTranscriptDelta(tp, cur)
	if err != nil {
		t.Fatalf("append read: %v", err)
	}
	// aa1(100) already counted; aa2(22) now counted (aa3 held back). A spurious reset would
	// re-derive from zero — still monotonic, but it must count aa2 exactly once = 122.
	if got.CumTokens != 122 {
		t.Fatalf("T5: CumTokens = %d after a normal append, want 122 (aa1+aa2, aa3 held back)", got.CumTokens)
	}
}

// T8 / S1 — a single JSONL record in the 1–4 MB range (above the 1 MB read window, below the 4 MB
// oversize threshold) makes the LimitReader hit EOF mid-record, so the offset never advances and the
// counter freezes permanently. This drives the REAL io.LimitReader path (readTranscriptDelta), which
// the shipped guarding test bypasses. RED today: CumTokens stays 0.
func TestFableIncr601_T8_LargeRecordDoesNotFreeze(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	// A ~2 MB valid JSONL line (no usage ⇒ skipped, but its bytes must be CONSUMED so the offset
	// advances past it), then a countable record and a held-back trailing record.
	big := `{"type":"pad","x":"` + strings.Repeat("x", 2<<20) + `"}`
	writeTranscript(t, tp, big, transcriptRecord("real_a", 50, 50), transcriptRecord("real_b", 1, 1))

	var cur statusline.TranscriptCursor
	for i := 0; i < 3; i++ {
		got, err := readTranscriptDelta(tp, cur)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		cur = got
	}
	if cur.CumTokens == 0 {
		t.Fatalf("T8: CumTokens frozen at 0 — the 1 MB read window EOFs inside the 2 MB record, so the " +
			"offset never advances and real_a is never reached")
	}
	if cur.CumTokens != 100 {
		t.Fatalf("T8: CumTokens = %d, want 100 (real_a counted, real_b held back)", cur.CumTokens)
	}
}

// T7 / B2 — the watchdog silence strip has nothing to strip because no production code emits the
// sentinel. The real render path must emit statuslineSentinel on every rendered statusline line so a
// statusline-only pane change is stripped before the silence hash. RED today: emission is absent.
func TestFableIncr601_T7_RenderCoreEmitsSentinelPerLine(t *testing.T) {
	root := enabledFactory(t)
	payload := `{"session_id":"sent","model":{"display_name":"Opus 4.8"},` +
		`"workspace":{"project_dir":"/repo"},` +
		`"context_window":{"total_input_tokens":1000,"total_output_tokens":500,"context_window_size":200000,"used_percentage":25},` +
		`"cost":{"total_cost_usd":1.23,"total_duration_ms":65000,"total_lines_added":10,"total_lines_removed":2}}`

	var out bytes.Buffer
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", now, false); err != nil {
		t.Fatalf("render: %v", err)
	}

	rendered := strings.TrimRight(out.String(), "\n")
	if rendered == "" {
		t.Fatalf("setup: the fixture rendered an empty statusline")
	}
	if !strings.Contains(rendered, statuslineSentinel) {
		t.Fatalf("T7: render path emitted no statuslineSentinel — the watchdog strip is a no-op on real panes: %q", rendered)
	}
	for _, line := range strings.Split(rendered, "\n") {
		if line == "" {
			continue
		}
		if !strings.Contains(line, statuslineSentinel) {
			t.Errorf("T7: every rendered statusline line must carry the sentinel; this one does not: %q", line)
		}
	}
}

// paddedUsageRecord is a JSONL record carrying message.id + usage, padded to `pad` bytes so a run of
// them exceeds the 5 MB read window. The usage decoder ignores the "pad" field, so it counts exactly
// like transcriptRecord but with a controllable byte size.
func paddedUsageRecord(msgID string, in, out int64, pad int) string {
	return fmt.Sprintf(`{"type":"assistant","message":{"id":%q,"usage":{"input_tokens":%d,"output_tokens":%d}},"pad":%q}`,
		msgID, in, out, strings.Repeat("x", pad))
}

// F-B (PR #601 T1) — a single JSONL record whose byte length >= the 5 MB read window never reaches its
// terminating '\n' inside the io.LimitReader, so readTranscriptLine returns (nil,0,false), the offset
// never advances, and the counter freezes permanently. This drives the REAL cmd io.LimitReader path
// (the library tests use an uncapped reader and cannot reproduce it). RED at head: Offset/CumTokens=0.
func TestFableIncr601_FB_OversizeLineDoesNotFreeze(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	huge := `{"type":"pad","x":"` + strings.Repeat("x", 6<<20) + `"}` // ~6 MB single line, > the 5 MB window
	writeTranscript(t, tp, huge, transcriptRecord("real_a", 50, 50), transcriptRecord("real_b", 1, 1))

	var cur statusline.TranscriptCursor
	for i := 0; i < 6; i++ {
		got, err := readTranscriptDelta(tp, cur)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		cur = got
	}
	if cur.Offset == 0 {
		t.Fatalf("F-B: offset pinned at 0 — the >=5 MB line EOFs inside the LimitReader before its '\\n', so the read never advances")
	}
	if cur.CumTokens != 100 {
		t.Fatalf("F-B: CumTokens = %d, want 100 (real_a counted once, real_b held back) once the freeze is broken", cur.CumTokens)
	}
}

// F-H (PR #601 T2) — when ONE message.id's content-block run fills the entire >=5 MB read window, the
// trailing-run holdback collapses to offset 0 and the reducer skips every record (added=0), so the
// offset never advances and the counter freezes; the run exceeds the window so the next message.id
// that would release the holdback is never reached. Drives the REAL LimitReader path. RED at head.
func TestFableIncr601_FH_SingleMessageRunDoesNotFreeze(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	// Three ~2 MB records sharing id "run" (usage 100/100 each, deduped to 200) form a >5 MB single-id
	// run; then a countable tail_a (1000) and a trailing tail_b (held back).
	r1 := paddedUsageRecord("run", 100, 100, 2<<20)
	r2 := paddedUsageRecord("run", 100, 100, 2<<20)
	r3 := paddedUsageRecord("run", 100, 100, 2<<20)
	writeTranscript(t, tp, r1, r2, r3, transcriptRecord("tail_a", 500, 500), transcriptRecord("tail_b", 1, 1))

	var cur statusline.TranscriptCursor
	for i := 0; i < 6; i++ {
		got, err := readTranscriptDelta(tp, cur)
		if err != nil {
			t.Fatalf("read %d: %v", i, err)
		}
		cur = got
	}
	if cur.Offset == 0 {
		t.Fatalf("F-H: offset pinned at 0 — the whole window is one message.id, so the holdback never releases")
	}
	// Freeze broken => tail_a (1000) is counted. The "run" message is counted AT MOST once (max-per-id
	// dedup), so the truthful total is in [1000, 1200]: below 1000 proves the freeze; above 1200 proves
	// a cross-window double-count of the run (AC-2 over-count).
	if cur.CumTokens < 1000 || cur.CumTokens > 1200 {
		t.Fatalf("F-H: CumTokens = %d, want 1000..1200 (tail_a counted; run counted at most once)", cur.CumTokens)
	}
}
