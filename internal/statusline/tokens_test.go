package statusline

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The ground-truth figures below were computed by hand with jq over the committed captures and are
// recorded in testdata/transcript/README.md. They are EXACT because over-counting is the failure
// mode no monotonicity test can catch: an assertion like "result > 0" or "result <= naive" passes
// against a reader that double-counts every record.
const (
	realMainHeldBack = 7357  // Σ max-per-field over distinct message.id, excluding the trailing run
	realMainNaive    = 31761 // Σ over every record with no dedup at all (3.16× the truth)
	realSubHeldBack  = 2941  // same, over the sidechain capture — max-wins
	realSubFirstWins = 31    // what a first-wins dedup would produce instead (95× low)
)

// rec renders one assistant record in the real transcript's shape. requestID == "" omits the field
// entirely, which is how non-Anthropic gateway records actually arrive (K2-V finding (b)).
func rec(msgID, requestID string, sidechain bool, stop string, in, out int64) string {
	stopJSON := "null"
	if stop != "" {
		stopJSON = fmt.Sprintf("%q", stop)
	}
	req := ""
	if requestID != "" {
		req = fmt.Sprintf(`"requestId":%q,`, requestID)
	}
	return fmt.Sprintf(`{"type":"assistant","isSidechain":%t,%s"message":{"id":%q,"stop_reason":%s,`+
		`"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":7,"cache_read_input_tokens":9}}}`,
		sidechain, req, msgID, stopJSON, in, out)
}

// noise renders records that carry no usage — every type observed in a real transcript except
// assistant. A reader that counts these, or fails on them, is wrong.
func noise() []string {
	return []string{
		`{"type":"user","uuid":"u1"}`,
		`{"type":"system","subtype":"turn_duration"}`,
		`{"type":"attachment"}`,
		`{"type":"queue-operation"}`,
		`{not json at all`,
		``,
	}
}

func joinLines(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func scanAll(s string) TranscriptCursor {
	return ScanUsage(strings.NewReader(s), TranscriptCursor{})
}

func readFixture(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "transcript", "recorded-real", name))
	if err != nil {
		t.Fatalf("reading fixture %s: %v", name, err)
	}
	return string(b)
}

// C-R1 / AC-2 clause (i). Claude Code writes ONE RECORD PER CONTENT BLOCK and stamps the whole
// message's usage on every one, so real transcripts repeat message.id 1.76×-3.16× (K2-V capture,
// testdata/transcript/README.md). An un-deduplicated sum is roughly double the truth, and a
// first-wins dedup is worse still: it records the in-flight partial instead of the final count.
func TestTokenCounter_DuplicateRecordsCountOnce(t *testing.T) {
	// A trailing sentinel message is appended wherever the assertion targets earlier messages,
	// because the reader deliberately holds back the trailing message.id run (K2-V finding (d)).
	sentinel := rec("msg_sentinel", "req_s", false, "end_turn", 1, 1)

	t.Run("ExactCloneDuplicates", func(t *testing.T) {
		// One message emitted as 3 identical records (thinking/text/tool_use blocks), plus a second
		// message. Truth = (10+500) + (20+700) = 1230. Naive = 3*(510) + 720 = 2250.
		in := joinLines(
			rec("msg_a", "req_a", false, "tool_use", 10, 500),
			rec("msg_a", "req_a", false, "tool_use", 10, 500),
			rec("msg_a", "req_a", false, "tool_use", 10, 500),
			rec("msg_b", "req_b", false, "end_turn", 20, 700),
			sentinel,
		)
		if got := scanAll(in).CumTokens; got != 1230 {
			t.Fatalf("CumTokens = %d, want 1230 (naive would be 2250)", got)
		}
	})

	t.Run("StreamingPartialTakesFinal", func(t *testing.T) {
		// The decisive case: the same message.id appears first as an in-flight snapshot and then as
		// the completed message. First-wins yields 3, no dedup yields 265, max-wins yields 262+2.
		in := joinLines(
			rec("msg_p", "req_p", false, "", 2, 1),
			rec("msg_p", "req_p", false, "", 2, 1),
			rec("msg_p", "req_p", false, "tool_use", 2, 262),
			sentinel,
		)
		got := scanAll(in).CumTokens
		if got == 3 {
			t.Fatal("CumTokens = 3: first-wins dedup recorded the in-flight partial, not the completed message")
		}
		if got != 264 {
			t.Fatalf("CumTokens = %d, want 264 (max-per-field: input 2 + output 262)", got)
		}
	})

	t.Run("SidechainRecordsCount", func(t *testing.T) {
		// C-R1: sub-agent tokens are session-caused spend, so the reader must carry NO sidechain
		// filter. Proven by removing the sidechain records and observing the total DROP.
		withSidechain := joinLines(
			rec("msg_main", "req_m", false, "end_turn", 10, 100),
			rec("msg_side", "req_s2", true, "end_turn", 20, 200),
			sentinel,
		)
		without := joinLines(
			rec("msg_main", "req_m", false, "end_turn", 10, 100),
			sentinel,
		)
		with, less := scanAll(withSidechain).CumTokens, scanAll(without).CumTokens
		if with != 330 {
			t.Fatalf("CumTokens with sidechain = %d, want 330", with)
		}
		if less != 110 {
			t.Fatalf("CumTokens without sidechain = %d, want 110", less)
		}
		if with <= less {
			t.Fatal("removing the sidechain record did not lower the total: the reader is filtering sidechain records")
		}
	})

	t.Run("MissingRequestIdStillCounts", func(t *testing.T) {
		// requestId is absent on gateway-model records (1,736 in the K2-V corpus). Keying on it, or
		// requiring it, silently drops that spend.
		in := joinLines(
			rec("msg_g1", "", false, "end_turn", 5, 50),
			rec("msg_g1", "", false, "end_turn", 5, 50),
			rec("msg_g2", "", false, "end_turn", 6, 60),
			sentinel,
		)
		if got := scanAll(in).CumTokens; got != 121 {
			t.Fatalf("CumTokens = %d, want 121 (55 + 66); records lacking requestId must still count", got)
		}
	})

	t.Run("NestedRollupsAreNotDoubleCounted", func(t *testing.T) {
		// cache_creation and iterations[] are EXACT roll-ups of the flat scalars (461/461 and
		// 1359/1359 verified). Summing them double-counts; only input_tokens+output_tokens count.
		in := joinLines(
			`{"type":"assistant","isSidechain":false,"requestId":"req_n","message":{"id":"msg_n","stop_reason":"end_turn",`+
				`"usage":{"input_tokens":11,"output_tokens":22,"cache_creation_input_tokens":1000,"cache_read_input_tokens":90000,`+
				`"cache_creation":{"ephemeral_5m_input_tokens":1000,"ephemeral_1h_input_tokens":0},`+
				`"iterations":[{"input_tokens":11,"output_tokens":22,"cache_creation_input_tokens":1000,"cache_read_input_tokens":90000}]}}}`,
			sentinel,
		)
		if got := scanAll(in).CumTokens; got != 33 {
			t.Fatalf("CumTokens = %d, want 33 (input 11 + output 22 only)", got)
		}
	})

	t.Run("ForeignRecordsAndMalformedLinesSkipped", func(t *testing.T) {
		lines := append([]string{rec("msg_x", "req_x", false, "end_turn", 3, 30)}, noise()...)
		lines = append(lines, sentinel)
		if got := scanAll(joinLines(lines...)).CumTokens; got != 33 {
			t.Fatalf("CumTokens = %d, want 33; only assistant records carry usage", got)
		}
	})

	t.Run("ReplayIsIdempotent", func(t *testing.T) {
		in := joinLines(
			rec("msg_r1", "req_r1", false, "end_turn", 4, 40),
			rec("msg_r1", "req_r1", false, "end_turn", 4, 40),
			rec("msg_r2", "req_r2", false, "end_turn", 5, 50),
			sentinel,
		)
		first, second := scanAll(in), scanAll(in)
		if first != second {
			t.Fatalf("replay over identical bytes diverged: %+v vs %+v", first, second)
		}
	})

	t.Run("RecordedRealCapture", func(t *testing.T) {
		// The anti-tautology proof: the deduped figure must equal the hand-computed ground truth
		// AND be strictly below the naive sum, so a no-op "dedup" cannot pass.
		got := scanAll(readFixture(t, "main_excerpt.jsonl")).CumTokens
		if got != realMainHeldBack {
			t.Fatalf("real main capture: CumTokens = %d, want %d", got, realMainHeldBack)
		}
		if got >= realMainNaive {
			t.Fatalf("real main capture: CumTokens = %d is not below the naive sum %d — dedup never fired", got, realMainNaive)
		}
	})

	t.Run("RecordedRealSidechainCapture", func(t *testing.T) {
		// Every record in this capture is isSidechain:true, and 6 of its 7 message groups carry an
		// in-flight partial alongside the completed record. Max-wins yields 2941; first-wins 31.
		got := scanAll(readFixture(t, "subagent_excerpt.jsonl")).CumTokens
		if got == realSubFirstWins {
			t.Fatalf("real sidechain capture: CumTokens = %d — first-wins dedup kept the in-flight partials", got)
		}
		if got != realSubHeldBack {
			t.Fatalf("real sidechain capture: CumTokens = %d, want %d", got, realSubHeldBack)
		}
	})
}

// K2-V finding (d) / L-R3. A logical message is a CONTIGUOUS RUN of records spanning 7.0s of wall
// clock on average against a 10s throttle window, so an incremental read lands INSIDE a run most of
// the time. Advancing merely "past the last complete line" re-counts that message on the next read:
// measured +28.9% over-count across 249 real transcripts. Holding back the trailing message.id run
// measured 0.0000%.
func TestTokenCounter_StraddledMessageCountsOnce(t *testing.T) {
	a1 := rec("msg_A", "req_A", false, "", 2, 5)
	a2 := rec("msg_A", "req_A", false, "", 2, 5)
	a3 := rec("msg_A", "req_A", false, "tool_use", 2, 348)
	b1 := rec("msg_B", "req_B", false, "", 3, 7)
	b2 := rec("msg_B", "req_B", false, "tool_use", 3, 900)
	c1 := rec("msg_C", "req_C", false, "end_turn", 4, 11)
	whole := joinLines(a1, a2, a3, b1, b2, c1)

	singlePass := scanAll(whole).CumTokens
	if singlePass != 1253 { // A(2+348) + B(3+900); C is the held-back trailing run
		t.Fatalf("single-pass CumTokens = %d, want 1253", singlePass)
	}

	// Now read the SAME bytes incrementally, with every window boundary landing mid-run.
	cut1 := int64(len(joinLines(a1, a2)))         // inside message A's run
	cut2 := int64(len(joinLines(a1, a2, a3, b1))) // inside message B's run

	cur := TranscriptCursor{}
	for _, end := range []int64{cut1, cut2, int64(len(whole))} {
		if cur.Offset > end {
			t.Fatalf("offset %d ran past the window end %d", cur.Offset, end)
		}
		cur = ScanUsage(strings.NewReader(whole[cur.Offset:end]), cur)
	}
	if cur.CumTokens != singlePass {
		t.Fatalf("incremental CumTokens = %d, single-pass = %d: a message straddling the read boundary was counted twice or dropped",
			cur.CumTokens, singlePass)
	}
}

// AC-2 clause (ii). Compaction APPENDS a system/compact_boundary record and a user/isCompactSummary
// record; it never rewrites or truncates the file. Context-window occupancy collapses across that
// boundary — that collapse IS finding F1 — while API-reported spend keeps accruing.
func TestTokenCounter_MonotonicAcrossCompaction(t *testing.T) {
	boundary := `{"type":"system","subtype":"compact_boundary","compactMetadata":{"trigger":"auto","preTokens":467234,"postTokens":10548,"cumulativeDroppedTokens":456686}}`
	summary := `{"type":"user","isCompactSummary":true}`

	// The file GROWS between reads and each read resumes at the stored offset — modelling how the
	// render path actually sees a live transcript. Compaction appends into the same file.
	appends := []string{
		joinLines(rec("msg_1", "req_1", false, "end_turn", 10, 100), rec("msg_2", "req_2", false, "end_turn", 10, 100)),
		joinLines(boundary, summary, rec("msg_3", "req_3", false, "end_turn", 10, 100)),
		joinLines(rec("msg_4", "req_4", false, "end_turn", 10, 100), rec("msg_5", "req_5", false, "end_turn", 10, 100)),
	}

	var cur TranscriptCursor
	file := ""
	seen := []int64{}
	for i, more := range appends {
		file += more
		next := ScanUsage(strings.NewReader(file[cur.Offset:]), cur)
		if next.CumTokens < cur.CumTokens {
			t.Fatalf("round %d: CumTokens went backwards %d -> %d", i, cur.CumTokens, next.CumTokens)
		}
		cur = next
		seen = append(seen, cur.CumTokens)
	}
	// A counter stuck at zero is also "never decreasing" — require real growth across the boundary.
	if seen[len(seen)-1] <= seen[0] {
		t.Fatalf("counter did not grow across the compaction boundary: %v", seen)
	}
	if cur.CumTokens != 440 { // msg_1..msg_4 counted; msg_5 is the held-back trailing run
		t.Fatalf("CumTokens = %d, want 440", cur.CumTokens)
	}
}

// X6. A byte offset is only safe on an append-only file. If the transcript is replaced by a shorter
// one, the reader RE-DERIVES FROM ZERO and must never subtract: the displayed figure stays
// non-decreasing throughout catch-up (L-R3), including at observations taken mid-re-derivation.
func TestTokenCounter_NonDecreasingAcrossOffsetReset(t *testing.T) {
	long := joinLines(
		rec("msg_l1", "req_l1", false, "end_turn", 100, 1000),
		rec("msg_l2", "req_l2", false, "end_turn", 100, 1000),
		rec("msg_l3", "req_l3", false, "end_turn", 100, 1000),
		rec("msg_l4", "req_l4", false, "end_turn", 100, 1000),
	)
	before := scanAll(long)
	if before.CumTokens != 3300 {
		t.Fatalf("setup: CumTokens = %d, want 3300", before.CumTokens)
	}

	// The replacement is strictly shorter AND its true total is far below the counter, so a naive
	// re-derive would visibly go backwards.
	short := joinLines(
		rec("msg_s1", "req_s1", false, "end_turn", 1, 5),
		rec("msg_s2", "req_s2", false, "end_turn", 1, 5),
	)
	shortSize := int64(len(short))
	if shortSize >= before.Offset {
		t.Fatalf("setup: replacement (%d bytes) must be shorter than the stored offset (%d)", shortSize, before.Offset)
	}

	reset := before.ResetIfTruncated(shortSize)
	if reset.Offset != 0 {
		t.Fatalf("offset = %d after truncation, want 0 (re-derive from zero)", reset.Offset)
	}
	if reset.CumTokens != before.CumTokens {
		t.Fatalf("CumTokens = %d after reset, want %d preserved — the counter must never be rebuilt by subtraction",
			reset.CumTokens, before.CumTokens)
	}

	// Mid-catch-up observation: only part of the replacement has been re-read.
	firstLineLen := strings.Index(short, "\n") + 1
	mid := ScanUsage(strings.NewReader(short[:firstLineLen]), reset)
	if mid.CumTokens < before.CumTokens {
		t.Fatalf("mid-catch-up CumTokens = %d dropped below %d: the display clamp max(stored, rederived) is missing",
			mid.CumTokens, before.CumTokens)
	}

	after := ScanUsage(strings.NewReader(short[mid.Offset:]), mid)
	if after.CumTokens < before.CumTokens {
		t.Fatalf("post-catch-up CumTokens = %d dropped below %d", after.CumTokens, before.CumTokens)
	}
	if after.CumTokens != before.CumTokens {
		t.Fatalf("CumTokens = %d, want %d — the re-derived total is far lower, so the counter must hold at its previous value",
			after.CumTokens, before.CumTokens)
	}
}

// K2-V finding (e). A real transcript line can reach 2.4 MB. A reader that re-attempts an over-long
// line never advances its offset: the cursor deadlocks and the counter freezes forever. The line is
// discarded, but its bytes MUST still be consumed (telemetry/store.go:readRecordLine idiom).
func TestTokenCounter_OverlongLineSkippedWithoutStalling(t *testing.T) {
	huge := `{"type":"assistant","pad":"` + strings.Repeat("x", maxTranscriptLineBytes+1024) + `"}`
	in := joinLines(
		rec("msg_before", "req_b", false, "end_turn", 10, 100),
		huge,
		rec("msg_after", "req_a", false, "end_turn", 20, 200),
		rec("msg_tail", "req_t", false, "end_turn", 1, 1),
	)
	got := scanAll(in)
	if got.Offset <= int64(len(joinLines(rec("msg_before", "req_b", false, "end_turn", 10, 100)))) {
		t.Fatalf("offset = %d did not advance past the over-long line: the cursor is deadlocked", got.Offset)
	}
	if got.CumTokens != 330 { // msg_before + msg_after; msg_tail is held back, the huge line has no usage
		t.Fatalf("CumTokens = %d, want 330", got.CumTokens)
	}
}

// A window that ends mid-line must not consume the partial line, or the next read starts inside a
// record and loses it.
func TestTokenCounter_PartialTrailingLineNotConsumed(t *testing.T) {
	full := joinLines(
		rec("msg_1", "req_1", false, "end_turn", 10, 100),
		rec("msg_2", "req_2", false, "end_turn", 20, 200),
	)
	truncated := full[:len(full)-10] // cut inside the final record
	cur := ScanUsage(strings.NewReader(truncated), TranscriptCursor{})
	if cur.Offset != 0 {
		// msg_1 is complete but msg_2's run is the trailing key, so the holdback pins the offset at
		// the start of msg_2 — which is exactly where the partial line begins.
		firstLen := int64(strings.Index(full, "\n") + 1)
		if cur.Offset != firstLen {
			t.Fatalf("offset = %d, want 0 or %d (start of the incomplete trailing record)", cur.Offset, firstLen)
		}
	}
	if cur.Offset > int64(len(truncated)) {
		t.Fatalf("offset %d ran past the bytes actually supplied (%d)", cur.Offset, len(truncated))
	}
}
