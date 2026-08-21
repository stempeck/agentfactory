package transcript

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// Record builders. The key sets below are NOT invented from the issue text (D-1's sibling decision
// D-10, design-doc.md:166): they reproduce the live claude-2.1.224 field census recorded at
// .designs/562/verification-report.md:115 (row 61) over a 2,575-record transcript —
//   message-bearing record: parentUuid, isSidechain, message, type, uuid, timestamp, sessionId, cwd
//                           (+requestId on assistant, +promptId/toolUseResult on user-side)
//   tool_use block:         exactly {type, id, name, input, caller}          ×218
//   tool_result block:      {tool_use_id, type, content} ×218, +is_error on 205/218
//   bookkeeping record:     {permissionMode, sessionId, type} — no uuid      ×121
// Assistant records are ONE CONTENT BLOCK EACH (461/461 live, 30/30 in the committed capture;
// verification-report.md row 63) — the shape that makes block identity, not record index, the only
// usable dedup key.
//
// Phase 1's captured corpus is a separate obligation; see TestRecordedRealFixtureSeam.

const (
	testSession = "sess-562"
	testCWD     = "/repo"
)

func joinLines(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func toolUseBlock(id, name, inputJSON string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s,"caller":"direct"}`, id, name, inputJSON)
}

func toolResultBlock(toolUseID, content string, isError bool) string {
	return fmt.Sprintf(`{"tool_use_id":%q,"type":"tool_result","content":%q,"is_error":%t}`, toolUseID, content, isError)
}

func textBlock(s string) string { return fmt.Sprintf(`{"type":"text","text":%q}`, s) }

func assistantRec(uuid, msgID, ts string, sidechain bool, block string) string {
	return fmt.Sprintf(`{"parentUuid":null,"isSidechain":%t,"message":{"id":%q,"role":"assistant","content":[%s]},`+
		`"requestId":"req_1","type":"assistant","uuid":%q,"timestamp":%q,"sessionId":%q,"cwd":%q}`,
		sidechain, msgID, block, uuid, ts, testSession, testCWD)
}

func userRec(uuid, ts string, sidechain bool, contentJSON string) string {
	return fmt.Sprintf(`{"parentUuid":null,"isSidechain":%t,"message":{"role":"user","content":%s},`+
		`"promptId":"p_1","type":"user","uuid":%q,"timestamp":%q,"sessionId":%q,"cwd":%q}`,
		sidechain, contentJSON, uuid, ts, testSession, testCWD)
}

// call is an assistant record carrying one tool_use block.
func call(uuid, msgID, toolUseID, name string) string {
	return callAt(uuid, msgID, toolUseID, name, "2026-08-09T10:00:00Z")
}

func callAt(uuid, msgID, toolUseID, name, ts string) string {
	return assistantRec(uuid, msgID, ts, false, toolUseBlock(toolUseID, name, `{"path":"a.go"}`))
}

func sidechainCall(uuid, msgID, toolUseID, name string) string {
	return assistantRec(uuid, msgID, "2026-08-09T10:00:00Z", true, toolUseBlock(toolUseID, name, `{"path":"a.go"}`))
}

func assistantText(uuid, msgID, text string) string {
	return assistantRec(uuid, msgID, "2026-08-09T10:00:00Z", false, textBlock(text))
}

// prompt is a PLAIN user record — array content with no tool_result block. It opens a turn.
func prompt(uuid, text string) string {
	return userRec(uuid, "2026-08-09T10:00:00Z", false, "["+textBlock(text)+"]")
}

// stringPrompt is the bare-string content shape (`{"role":"user","content":"hello"}`). A string
// cannot contain a tool_result block, so it is PLAIN and opens a turn.
func stringPrompt(uuid, text string) string {
	return userRec(uuid, "2026-08-09T10:00:00Z", false, fmt.Sprintf("%q", text))
}

func sidechainPrompt(uuid, text string) string {
	return userRec(uuid, "2026-08-09T10:00:00Z", true, "["+textBlock(text)+"]")
}

// result is a user record carrying a tool_result block. It is NOT plain and must not open a turn.
func result(uuid, toolUseID, content string) string {
	return userRec(uuid, "2026-08-09T10:00:00Z", false, "["+toolResultBlock(toolUseID, content, false)+"]")
}

func errResult(uuid, toolUseID, content string) string {
	return userRec(uuid, "2026-08-09T10:00:00Z", false, "["+toolResultBlock(toolUseID, content, true)+"]")
}

// noise mirrors internal/statusline/tokens_test.go:40-49 plus the no-uuid bookkeeping keyset
// measured ×121 in the live census. A reader that counts these, or fails on them, is wrong.
// noise is the traffic a real transcript carries alongside the turn: valid JSON that is simply not a
// message-bearing record. It is deliberately free of corrupt lines — those are a different class with
// a different contract (see corrupt), and mixing them would let a test claim "malformed lines are
// harmless" while actually only proving it for well-formed ones.
func noise() []string {
	return []string{
		`{"type":"system","subtype":"turn_duration"}`,
		`{"type":"attachment"}`,
		`{"type":"queue-operation"}`,
		`{"permissionMode":"default","sessionId":"sess-562","type":"permission-mode"}`,
		``,
	}
}

func corrupt() string { return `{not json at all` }

func derive(t *testing.T, s string) Evidence {
	t.Helper()
	return Derive(strings.NewReader(s), DefaultOptions())
}

func toolNames(ev Evidence) []string {
	out := make([]string, 0, len(ev.Calls))
	for _, c := range ev.Calls {
		out = append(out, c.Tool)
	}
	return out
}

func seqs(ev Evidence) []int {
	out := make([]int, 0, len(ev.Calls))
	for _, c := range ev.Calls {
		out = append(out, c.Seq)
	}
	return out
}

func recordJSON(t *testing.T, ev Evidence) string {
	t.Helper()
	b, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshalling evidence record: %v", err)
	}
	return string(b)
}

// ---------------------------------------------------------------------------------------------
// AC-1 — a gate evaluation judges only tool calls made during the turn that triggered it.
// ---------------------------------------------------------------------------------------------

func TestGateEvidence_TurnScoping(t *testing.T) {
	t.Run("OnlyTheFinalTurnSurvives", func(t *testing.T) {
		// Fixture geometry is load-bearing for the phase's mutation check: the FINAL turn holds
		// only TWO tool-bearing records, so a tail extractor's five-record window necessarily
		// reaches back across the boundary and drags in Grep and GitStatus.
		in := joinLines(
			prompt("u-turn1", "check the repo"),
			call("a1", "msg_1", "tu_1", "GitStatus"),
			result("r1", "tu_1", "on branch main"),
			prompt("u-turn2", "find the caller"),
			call("a2", "msg_2", "tu_2", "Grep"),
			result("r2", "tu_2", "3 matches"),
			prompt("u-turn3", "now fix it"),
			call("a3", "msg_3", "tu_3", "Read"),
			result("r3", "tu_3", "file contents"),
			call("a4", "msg_4", "tu_4", "Edit"),
		)
		ev := derive(t, in)

		if ev.Turn.CallsTotal != 2 {
			t.Fatalf("calls_total = %d, want 2; a session-global tail yields 4 (GitStatus and Grep belong to earlier turns)", ev.Turn.CallsTotal)
		}
		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Read", "Edit"}) {
			t.Fatalf("tools = %v, want [Read Edit]", got)
		}
		// AC-1 clause (ii): earlier-turn calls CANNOT APPEAR — anywhere in the record, not merely
		// in the call list.
		rec := recordJSON(t, ev)
		for _, leaked := range []string{"GitStatus", "Grep", "on branch main", "3 matches"} {
			if strings.Contains(rec, leaked) {
				t.Fatalf("earlier-turn material %q appeared in this turn's evidence (AC-1 clause ii)", leaked)
			}
		}
		if ev.Turn.BoundaryUUID != "u-turn3" {
			t.Fatalf("boundary_uuid = %q, want %q (the last PLAIN non-sidechain user record)", ev.Turn.BoundaryUUID, "u-turn3")
		}
		if !ev.Turn.Complete {
			t.Fatalf("complete = false, want true: nothing was capped, skipped or unmatched")
		}
	})

	t.Run("TrailingToolResultCarrierDoesNotOpenTurn", func(t *testing.T) {
		// Without this case "last plain user record" and "last user record" are indistinguishable
		// and the boundary predicate is untested.
		in := joinLines(
			prompt("u-turn1", "first"),
			call("a1", "msg_1", "tu_1", "GitStatus"),
			prompt("u-turn2", "second"),
			call("a2", "msg_2", "tu_2", "Read"),
			result("r2", "tu_2", "file contents"),
			call("a3", "msg_3", "tu_3", "Edit"),
			result("r3", "tu_3", "edit applied"), // <- the file's LAST user record, a tool_result carrier
		)
		ev := derive(t, in)

		if ev.Turn.BoundaryUUID != "u-turn2" {
			t.Fatalf("boundary_uuid = %q, want %q; a 'last user record' rule would pick %q and report zero calls",
				ev.Turn.BoundaryUUID, "u-turn2", "r3")
		}
		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Read", "Edit"}) {
			t.Fatalf("tools = %v, want [Read Edit]", got)
		}
	})

	t.Run("StringContentPromptIsPlain", func(t *testing.T) {
		// `{"role":"user","content":"hello"}` is the other observed user-content shape. A string
		// cannot carry a tool_result block, so it must open a turn. Decoding it as malformed would
		// push the boundary EARLIER and re-import the previous turn's calls — the AC-1 violation
		// this package exists to remove.
		in := joinLines(
			prompt("u-turn1", "first"),
			call("a1", "msg_1", "tu_1", "GitStatus"),
			stringPrompt("u-turn2", "now fix it"),
			call("a2", "msg_2", "tu_2", "Edit"),
		)
		ev := derive(t, in)

		if ev.Turn.BoundaryUUID != "u-turn2" {
			t.Fatalf("boundary_uuid = %q, want %q: a string-content user record is a plain prompt", ev.Turn.BoundaryUUID, "u-turn2")
		}
		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Edit"}) {
			t.Fatalf("tools = %v, want [Edit]; GitStatus belongs to the previous turn", got)
		}
	})

	t.Run("BoundaryTimestampIsReported", func(t *testing.T) {
		in := joinLines(
			userRec("u-turn1", "2026-08-09T11:22:33Z", false, "["+textBlock("go")+"]"),
			call("a1", "msg_1", "tu_1", "Read"),
		)
		if got := derive(t, in).Turn.BoundaryTS; got != "2026-08-09T11:22:33Z" {
			t.Fatalf("boundary_ts = %q, want %q", got, "2026-08-09T11:22:33Z")
		}
	})
}

// ---------------------------------------------------------------------------------------------
// AC-2 — tool-call evidence reaches the judge in the order the calls actually executed.
// ---------------------------------------------------------------------------------------------

func TestGateEvidence_Ordering(t *testing.T) {
	t.Run("EncounterOrderOldestFirst", func(t *testing.T) {
		// Distinct, non-palindromic names: a reversing extractor must produce a DIFFERENT slice.
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			result("r1", "tu_1", "contents"),
			call("a2", "msg_2", "tu_2", "Edit"),
			result("r2", "tu_2", "applied"),
			call("a3", "msg_3", "tu_3", "Bash"),
			result("r3", "tu_3", "exit 0"),
		)
		ev := derive(t, in)

		want := []string{"Read", "Edit", "Bash"}
		if got := toolNames(ev); !reflect.DeepEqual(got, want) {
			t.Fatalf("tools = %v, want %v; a newest-first (tac) reader yields [Bash Edit Read]", got, want)
		}
		if got := seqs(ev); !reflect.DeepEqual(got, []int{1, 2, 3}) {
			t.Fatalf("seq = %v, want [1 2 3]", got)
		}
	})

	t.Run("TimestampsDoNotReorder", func(t *testing.T) {
		// Encounter order IS execution order (the transcript is append-ordered, data.md:25-30).
		// A reader that sorts by timestamp would emit [Edit Read Bash] here.
		in := joinLines(
			prompt("u-1", "go"),
			callAt("a1", "msg_1", "tu_1", "Read", "2026-08-09T10:00:05Z"),
			callAt("a2", "msg_2", "tu_2", "Edit", "2026-08-09T10:00:01Z"),
			callAt("a3", "msg_3", "tu_3", "Bash", "2026-08-09T10:00:09Z"),
		)
		want := []string{"Read", "Edit", "Bash"}
		if got := toolNames(derive(t, in)); !reflect.DeepEqual(got, want) {
			t.Fatalf("tools = %v, want %v; a timestamp sort yields [Edit Read Bash]", got, want)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// AC-3 — each tool result presented alongside a call is the result that call produced.
// ---------------------------------------------------------------------------------------------

func TestGateEvidence_CallResultPairing(t *testing.T) {
	t.Run("ResultsArrivingOutOfOrderStillPairByToolUseID", func(t *testing.T) {
		// The out-of-order arrangement is what makes this non-tautological: if results were
		// emitted in call order, positional pairing and the tool_use_id join would coincide.
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			call("a2", "msg_2", "tu_2", "Grep"),
			call("a3", "msg_3", "tu_3", "Bash"),
			result("r3", "tu_3", "RESULT-FOR-3"),
			result("r1", "tu_1", "RESULT-FOR-1"),
			result("r2", "tu_2", "RESULT-FOR-2"),
		)
		ev := derive(t, in)

		if len(ev.Calls) != 3 {
			t.Fatalf("calls = %d, want 3", len(ev.Calls))
		}
		for i, want := range []string{"RESULT-FOR-1", "RESULT-FOR-2", "RESULT-FOR-3"} {
			c := ev.Calls[i]
			if c.Result == nil {
				t.Fatalf("call %d (%s) has no result", i+1, c.Tool)
			}
			if c.Result.Content != want {
				t.Fatalf("call %d (%s) result = %q, want %q; positional pairing would give %q",
					i+1, c.Tool, c.Result.Content, want, "RESULT-FOR-3")
			}
		}
		if ev.Turn.UnmatchedResults != 0 {
			t.Fatalf("unmatched_results = %d, want 0", ev.Turn.UnmatchedResults)
		}
	})

	t.Run("OrphanResultIsDroppedCountedAndDisclosed", func(t *testing.T) {
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			result("r1", "tu_1", "RESULT-FOR-1"),
			result("r9", "tu_orphan", "ORPHAN-CONTENT"),
		)
		ev := derive(t, in)

		if ev.Turn.UnmatchedResults != 1 {
			t.Fatalf("unmatched_results = %d, want 1", ev.Turn.UnmatchedResults)
		}
		if strings.Contains(recordJSON(t, ev), "ORPHAN-CONTENT") {
			t.Fatalf("an unmatched result was attached to a call instead of being dropped")
		}
		// design-doc.md:108 — unmatched_results > 0 ⇒ degraded, never a guess.
		if ev.Turn.Complete {
			t.Fatalf("complete = true with unmatched_results = 1; the degradation must be disclosed")
		}
	})

	t.Run("IsErrorAndTruncationFlagRideWithTheResult", func(t *testing.T) {
		long := strings.Repeat("x", 400)
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Bash"),
			errResult("r1", "tu_1", long),
		)
		ev := derive(t, in)

		if len(ev.Calls) != 1 || ev.Calls[0].Result == nil {
			t.Fatalf("expected exactly one call carrying a result, got %d", len(ev.Calls))
		}
		res := ev.Calls[0].Result
		if !res.IsError {
			t.Fatalf("is_error = false, want true")
		}
		if !res.Truncated {
			t.Fatalf("truncated = false, want true for a 400-char result against the 300c policy")
		}
		if len([]rune(res.Content)) != 300 {
			t.Fatalf("result content = %d chars, want 300 (design-doc.md:100)", len([]rune(res.Content)))
		}
	})

	t.Run("CallWithNoResultYetIsReportedWithoutOne", func(t *testing.T) {
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
		)
		ev := derive(t, in)
		if len(ev.Calls) != 1 {
			t.Fatalf("calls = %d, want 1", len(ev.Calls))
		}
		if ev.Calls[0].Result != nil {
			t.Fatalf("result = %+v, want nil; nothing may be invented for a call with no result", ev.Calls[0].Result)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// AC-4 — when a turn's tool activity exceeds the evidence cap, the judge can distinguish partial
// evidence from complete evidence.
// ---------------------------------------------------------------------------------------------

func TestGateEvidence_TruncationMarker(t *testing.T) {
	build := func(n int) string {
		lines := []string{prompt("u-1", "go")}
		for i := 1; i <= n; i++ {
			lines = append(lines, call(
				fmt.Sprintf("a%02d", i), fmt.Sprintf("msg_%02d", i),
				fmt.Sprintf("tu_%02d", i), fmt.Sprintf("T%02d", i)))
		}
		return joinLines(lines...)
	}

	t.Run("AboveCapKeepsHeadAndTailWindow", func(t *testing.T) {
		ev := derive(t, build(25))

		if ev.Turn.CallsTotal != 25 {
			t.Fatalf("calls_total = %d, want 25 (the cap counts TOOL CALLS, and total is the honest count)", ev.Turn.CallsTotal)
		}
		if ev.Turn.CallsShown != 20 {
			t.Fatalf("calls_shown = %d, want 20", ev.Turn.CallsShown)
		}
		if ev.Turn.Complete {
			t.Fatalf("complete = true with 5 calls omitted")
		}

		want := make([]string, 0, 20)
		for i := 1; i <= 5; i++ {
			want = append(want, fmt.Sprintf("T%02d", i))
		}
		for i := 11; i <= 25; i++ {
			want = append(want, fmt.Sprintf("T%02d", i))
		}
		got := toolNames(ev)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("shown calls = %v,\nwant %v;\nfirst-N would give T01..T20 and the SUPERSEDED most-recent-N (data.md:30) would give T06..T25", got, want)
		}
		// The differential: head+tail is not most-recent-N. T01 proves the head survived.
		if got[0] != "T01" {
			t.Fatalf("first shown call = %q, want T01; most-recent-N truncation would start at T06 and the 'Read X first' sequencing proof would be lost", got[0])
		}
		// seq is the call's position in the WHOLE turn, so the omission is visible in the data too.
		if s := seqs(ev); s[4] != 5 || s[5] != 11 {
			t.Fatalf("seq around the omission = %d then %d, want 5 then 11", s[4], s[5])
		}

		wantMarker := "[showing first 5 + last 15 of 25 calls; 5 middle calls in this turn omitted — EVIDENCE IS PARTIAL]"
		if got := ev.Marker(); got != wantMarker {
			t.Fatalf("marker =\n%q\nwant\n%q", got, wantMarker)
		}
	})

	t.Run("ExactlyAtCapIsComplete", func(t *testing.T) {
		ev := derive(t, build(20))
		if !ev.Turn.Complete {
			t.Fatalf("complete = false at exactly the cap; a >= comparison is the off-by-one that mislabels a whole turn")
		}
		if ev.Turn.CallsShown != 20 || ev.Turn.CallsTotal != 20 {
			t.Fatalf("shown/total = %d/%d, want 20/20", ev.Turn.CallsShown, ev.Turn.CallsTotal)
		}
		if m := ev.Marker(); m != "" {
			t.Fatalf("marker = %q, want empty for complete evidence", m)
		}
	})

	t.Run("BelowCapIsComplete", func(t *testing.T) {
		ev := derive(t, build(19))
		if !ev.Turn.Complete || ev.Marker() != "" {
			t.Fatalf("complete = %t, marker = %q; want true and empty", ev.Turn.Complete, ev.Marker())
		}
	})

	t.Run("CapCountsToolCallsNotJSONLLines", func(t *testing.T) {
		// The defect being replaced counted JSONL messages (`head -5`). Interleaving results and
		// noise multiplies lines without adding calls: a line-counted cap truncates here, a
		// call-counted cap does not.
		lines := []string{prompt("u-1", "go")}
		for i := 1; i <= 6; i++ {
			lines = append(lines,
				call(fmt.Sprintf("a%d", i), fmt.Sprintf("msg_%d", i), fmt.Sprintf("tu_%d", i), fmt.Sprintf("T%02d", i)),
				result(fmt.Sprintf("r%d", i), fmt.Sprintf("tu_%d", i), "ok"))
			lines = append(lines, noise()...)
		}
		ev := derive(t, joinLines(lines...))
		if ev.Turn.CallsTotal != 6 || ev.Turn.CallsShown != 6 {
			t.Fatalf("shown/total = %d/%d, want 6/6 across %d JSONL lines", ev.Turn.CallsShown, ev.Turn.CallsTotal, len(lines))
		}
		if !ev.Turn.Complete {
			t.Fatalf("complete = false; 6 calls are below the cap of 20 however many lines carried them")
		}
	})
}

// ---------------------------------------------------------------------------------------------
// Sidechain exclusion — defense in depth. Real sub-agent records live in sibling files
// (<sessionId>/subagents/agent-<id>.jsonl) that transcript_path does not reference
// (internal/statusline/testdata/transcript/README.md:66-79), so this guard normally sees nothing.
// These cases prove it fires when such a record IS present inline.
// ---------------------------------------------------------------------------------------------

func TestGateEvidence_SidechainExcluded(t *testing.T) {
	withSidechain := joinLines(
		prompt("u-1", "go"),
		call("a1", "msg_1", "tu_1", "Read"),
		sidechainCall("s1", "msg_s1", "tu_s1", "SubAgentGrep"),
		result("r1", "tu_1", "contents"),
		sidechainCall("s2", "msg_s2", "tu_s2", "SubAgentBash"),
		call("a2", "msg_2", "tu_2", "Edit"),
	)
	without := joinLines(
		prompt("u-1", "go"),
		call("a1", "msg_1", "tu_1", "Read"),
		result("r1", "tu_1", "contents"),
		call("a2", "msg_2", "tu_2", "Edit"),
	)

	t.Run("DifferentialWithAndWithout", func(t *testing.T) {
		got := toolNames(derive(t, withSidechain))
		want := toolNames(derive(t, without))
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("with sidechain = %v, without = %v; the two must be identical", got, want)
		}
		if !reflect.DeepEqual(got, []string{"Read", "Edit"}) {
			t.Fatalf("tools = %v, want [Read Edit]", got)
		}
	})

	t.Run("ExclusionIsCountedNotSilent", func(t *testing.T) {
		// Anti-tautology: an implementation with NO sidechain filter passes the differential above
		// whenever the fixture happens to hold no sidechain records. The count proves the guard saw
		// and dropped them.
		ev := derive(t, withSidechain)
		if ev.Turn.SidechainExcluded != 2 {
			t.Fatalf("sidechain_excluded = %d, want 2; a missing filter reports 0 while leaking SubAgentGrep", ev.Turn.SidechainExcluded)
		}
		if rec := recordJSON(t, ev); strings.Contains(rec, "SubAgent") {
			t.Fatalf("a sidechain call leaked into the turn evidence")
		}
	})

	t.Run("SidechainUserRecordCannotOpenTurn", func(t *testing.T) {
		// Separable clause: an implementation that filters isSidechain at COLLECTION time but not
		// at BOUNDARY time passes both cases above and fails this one.
		in := joinLines(
			prompt("u-real", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			sidechainPrompt("u-side", "sub-agent instructions"),
			call("a2", "msg_2", "tu_2", "Edit"),
		)
		ev := derive(t, in)
		if ev.Turn.BoundaryUUID != "u-real" {
			t.Fatalf("boundary_uuid = %q, want %q; a spawned sub-agent's prompt must not truncate the dispatching turn", ev.Turn.BoundaryUUID, "u-real")
		}
		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Read", "Edit"}) {
			t.Fatalf("tools = %v, want [Read Edit]; Read was lost to a sidechain boundary", got)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// Empty turn — an authoritative answer, NOT a degraded one. The judge may rely on it.
// ---------------------------------------------------------------------------------------------

func TestGateEvidence_EmptyTurn(t *testing.T) {
	in := joinLines(
		prompt("u-turn1", "check the repo"),
		call("a1", "msg_1", "tu_1", "GitStatus"),
		result("r1", "tu_1", "on branch main"),
		prompt("u-turn2", "summarise what you found"),
		assistantText("a2", "msg_2", "Here is the summary."),
		assistantText("a3", "msg_3", "Nothing further."),
	)
	ev := derive(t, in)

	t.Run("ZeroCallsReportedAuthoritatively", func(t *testing.T) {
		if ev.Turn.CallsTotal != 0 || len(ev.Calls) != 0 {
			t.Fatalf("calls_total = %d, len(calls) = %d, want 0/0", ev.Turn.CallsTotal, len(ev.Calls))
		}
		// The clause that separates EMPTY (authoritative — the judge may conclude "no tools were
		// used") from UNAVAILABLE (degraded — grade the text only). One boolean for both would let
		// a parse failure masquerade as a confident "no tools were used".
		if !ev.Turn.Complete {
			t.Fatalf("complete = false for an empty turn; an empty turn is an answer, not a degradation")
		}
		if got := ev.Marker(); got != "No tool calls were made in this turn." {
			t.Fatalf("marker = %q, want %q", got, "No tool calls were made in this turn.")
		}
	})

	t.Run("NoPriorTurnLeak", func(t *testing.T) {
		if rec := recordJSON(t, ev); strings.Contains(rec, "GitStatus") || strings.Contains(rec, "on branch main") {
			t.Fatalf("prior-turn material leaked into an empty turn's evidence")
		}
	})

	t.Run("DifferentialTheSameFileWithoutTheFinalPromptHasCalls", func(t *testing.T) {
		// Proves the emptiness comes from the boundary rule and not from a builder that never
		// produced a call.
		in := joinLines(
			prompt("u-turn1", "check the repo"),
			call("a1", "msg_1", "tu_1", "GitStatus"),
			result("r1", "tu_1", "on branch main"),
			assistantText("a2", "msg_2", "Here is the summary."),
		)
		if got := derive(t, in).Turn.CallsTotal; got != 1 {
			t.Fatalf("calls_total = %d, want 1 without the second prompt; the empty result above would be vacuous", got)
		}
	})
}

// ---------------------------------------------------------------------------------------------
// Fail open — ADR-007: a gate must never be blocked by gate infrastructure. Every degraded state
// is DISCLOSED, never guessed (design-doc.md:108).
// ---------------------------------------------------------------------------------------------

func TestGateEvidence_FailOpen(t *testing.T) {
	t.Run("MissingFile", func(t *testing.T) {
		ev := DeriveFile(filepath.Join(t.TempDir(), "does-not-exist.jsonl"), DefaultOptions())
		if ev.Marker() != "[tool evidence unavailable this turn]" {
			t.Fatalf("marker = %q, want the unavailable marker", ev.Marker())
		}
		if len(ev.Calls) != 0 || ev.Turn.Complete {
			t.Fatalf("a missing file produced calls=%d complete=%t; want 0/false", len(ev.Calls), ev.Turn.Complete)
		}
	})

	t.Run("EmptyFile", func(t *testing.T) {
		if got := derive(t, "").Marker(); got != "[tool evidence unavailable this turn]" {
			t.Fatalf("marker = %q, want the unavailable marker", got)
		}
	})

	t.Run("NoBoundaryEmitsNothingEvenThoughCallsExist", func(t *testing.T) {
		// design-doc.md:108, verbatim: no boundary ⇒ degraded/unavailable marker, NEVER A GUESS.
		// Asserting only "returns unavailable" would pass an implementation that emits the whole
		// file AND the marker.
		in := joinLines(
			call("a1", "msg_1", "tu_1", "Read"),
			call("a2", "msg_2", "tu_2", "Edit"),
			call("a3", "msg_3", "tu_3", "Bash"),
		)
		ev := derive(t, in)
		if got := ev.Marker(); got != "[tool evidence unavailable this turn]" {
			t.Fatalf("marker = %q, want the unavailable marker", got)
		}
		if ev.Turn.CallsTotal != 0 || len(ev.Calls) != 0 {
			t.Fatalf("calls_total = %d, len(calls) = %d; with no boundary the whole file is a guess and must not be emitted",
				ev.Turn.CallsTotal, len(ev.Calls))
		}
		// The record itself must carry the doubt, not only Marker(): a consumer that reads the JSON
		// and never calls Marker() would otherwise see a clean, empty, "complete" turn.
		if ev.Turn.Complete {
			t.Fatalf("complete = true with no boundary; an unfound turn is the least complete state there is")
		}
		if rec := recordJSON(t, ev); strings.Contains(rec, "Read") {
			t.Fatalf("unbounded file contents leaked into the evidence record")
		}
	})

	t.Run("ForeignAndBookkeepingLinesAreExpectedTraffic", func(t *testing.T) {
		lines := []string{prompt("u-1", "go")}
		lines = append(lines, noise()...)
		lines = append(lines, call("a1", "msg_1", "tu_1", "Read"))
		lines = append(lines, noise()...)
		lines = append(lines, result("r1", "tu_1", "contents"), call("a2", "msg_2", "tu_2", "Edit"))
		lines = append(lines, noise()...)

		ev := derive(t, joinLines(lines...))
		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Read", "Edit"}) {
			t.Fatalf("tools = %v, want [Read Edit]; one foreign line must never fail the scan", got)
		}
		// Bookkeeping records and blank lines are ordinary traffic. Degrading on them would cry wolf
		// on every real transcript and train the reader to ignore the disclosure that matters.
		if !ev.Turn.Complete {
			t.Fatalf("complete = false; foreign, bookkeeping and blank lines are expected traffic, not a degradation")
		}
	})

	t.Run("UnparseableLineIsDisclosedNotSilentlySkipped", func(t *testing.T) {
		// A line that will not parse has an UNKNOWABLE type, so it may have been the prompt that
		// opens this turn — and a missed boundary re-imports the previous turn's calls. Skipping it
		// is mandatory (ADR-007); skipping it silently is the one remaining route to an AC-1
		// over-report, so the doubt has to reach the record.
		in := joinLines(
			prompt("u-1", "first"),
			call("a1", "msg_1", "tu_1", "GitStatus"),
			result("r1", "tu_1", "clean"),
			corrupt(), // this WAS the second turn's prompt
			call("a2", "msg_2", "tu_2", "Edit"),
			result("r2", "tu_2", "ok"),
		)
		ev := derive(t, in)

		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"GitStatus", "Edit"}) {
			t.Fatalf("tools = %v, want [GitStatus Edit]; the scan must survive the bad line", got)
		}
		if ev.Turn.Complete {
			t.Fatalf("complete = true while GitStatus — a PREVIOUS turn's call — is reported as this turn's; "+
				"an unparseable line is lost evidence and must clear complete (tools = %v)", toolNames(ev))
		}
		// Same file, bad line repaired: the over-report disappears, proving the assertion above is
		// about the corruption and not about the fixture's shape.
		clean := derive(t, joinLines(
			prompt("u-1", "first"),
			call("a1", "msg_1", "tu_1", "GitStatus"),
			result("r1", "tu_1", "clean"),
			prompt("u-2", "second"),
			call("a2", "msg_2", "tu_2", "Edit"),
			result("r2", "tu_2", "ok"),
		))
		if got := toolNames(clean); !reflect.DeepEqual(got, []string{"Edit"}) {
			t.Fatalf("repaired tools = %v, want [Edit]", got)
		}
		if !clean.Turn.Complete {
			t.Fatalf("repaired complete = false; the degradation must not outlive the corruption")
		}
	})

	t.Run("OversizeLineSkippedWithoutStalling", func(t *testing.T) {
		// The skip-AND-ADVANCE proof (internal/statusline/tokens.go:168-172). A reader that
		// re-attempts an over-long line never advances, so the assertion that matters is on the
		// call AFTER the huge one.
		huge := assistantRec("a2", "msg_2", "2026-08-09T10:00:00Z", false,
			toolUseBlock("tu_2", "Huge", fmt.Sprintf(`{"blob":%q}`, strings.Repeat("x", 5<<20))))
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			huge,
			call("a3", "msg_3", "tu_3", "Edit"),
		)
		ev := derive(t, in)

		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Read", "Edit"}) {
			t.Fatalf("tools = %v, want [Read Edit]; a stalled reader returns only [Read]", got)
		}
		// A dropped record is lost evidence and must be disclosed, not silently absorbed.
		if ev.Turn.Complete {
			t.Fatalf("complete = true after a record was skipped; the loss must be disclosed")
		}
	})

	t.Run("UnterminatedTrailingLine", func(t *testing.T) {
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
		) + `{"type":"assistant","uuid":"a2","message":{"id":"msg_2","content":[{"type":"tool_u`
		ev := derive(t, in)

		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Read"}) {
			t.Fatalf("tools = %v, want [Read]", got)
		}
		if ev.Turn.Complete {
			t.Fatalf("complete = true although a trailing partial record was not consumed")
		}
	})

	t.Run("LostCallsNeverRenderAsAnEmptyTurn", func(t *testing.T) {
		// The turn's ONLY call is the record the reader has to drop, so calls_total falls to zero
		// by the same arithmetic an genuinely tool-free turn produces. Rendering the authoritative
		// "No tool calls were made in this turn." here would tell the judge the agent did nothing
		// and earn it a BLOCK for a 5MB Write — a verdict manufactured by gate infrastructure.
		oversize := assistantRec("a1", "msg_1", "2026-08-09T10:00:00Z", false,
			toolUseBlock("tu_1", "Write", fmt.Sprintf(`{"content":%q}`, strings.Repeat("x", 5<<20))))
		lost := derive(t, joinLines(prompt("u-1", "write the file"), oversize))

		if lost.Turn.CallsTotal != 0 || lost.Turn.Complete {
			t.Fatalf("precondition: want calls_total 0 and complete false, got %d/%t", lost.Turn.CallsTotal, lost.Turn.Complete)
		}
		if got := lost.Marker(); got != "[tool evidence unavailable this turn]" {
			t.Fatalf("marker = %q, want the unavailable marker; a zero count the reader CAUSED is not an empty turn", got)
		}

		// The differential: the identical shape with a readable record is an empty turn and MUST
		// still say so authoritatively, or the fix has simply broken the other case.
		clean := derive(t, joinLines(prompt("u-1", "just summarise"), assistantText("a1", "msg_1", "done")))
		if got := clean.Marker(); got != "No tool calls were made in this turn." {
			t.Fatalf("marker = %q, want the empty-turn marker for a genuinely tool-free turn", got)
		}
	})

	t.Run("UndecodableContentArrayIsDisclosed", func(t *testing.T) {
		// A wrongly-typed scalar makes the whole content array fail to decode, so the record's
		// blocks vanish. Under-reporting is the fail-safe direction, but an undisclosed
		// under-report is a guess.
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			`{"isSidechain":false,"message":{"role":"assistant","content":[{"type":"tool_use","id":"tu_2","name":123}]},"type":"assistant","uuid":"a2"}`,
		)
		ev := derive(t, in)
		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Read"}) {
			t.Fatalf("tools = %v, want [Read]", got)
		}
		if ev.Turn.Complete {
			t.Fatalf("complete = true although a content array could not be decoded")
		}
	})

	t.Run("DegradationDoesNotOutliveItsTurn", func(t *testing.T) {
		// The Stop hook re-reads a GROWING transcript every turn, so a loss that stayed sticky would
		// mark every later turn degraded for the rest of the session — and since Marker() consults
		// Complete, a genuinely tool-free turn would then render "unavailable" forever.
		oversize := assistantRec("a1", "msg_1", "2026-08-09T10:00:00Z", false,
			toolUseBlock("tu_1", "Write", fmt.Sprintf(`{"content":%q}`, strings.Repeat("x", 5<<20))))
		in := joinLines(
			prompt("u-turn1", "write the file"),
			oversize,
			prompt("u-turn2", "now read it back"),
			call("a2", "msg_2", "tu_2", "Read"),
			result("r2", "tu_2", "contents"),
		)
		ev := derive(t, in)

		if !ev.Turn.Complete {
			t.Fatalf("complete = false; the skipped record is provably BEFORE this turn's boundary and says nothing about it")
		}
		if got := toolNames(ev); !reflect.DeepEqual(got, []string{"Read"}) {
			t.Fatalf("tools = %v, want [Read]", got)
		}
		// The differential: the SAME loss inside the final turn must still be disclosed, so the
		// reset cannot have simply stopped tracking losses.
		still := derive(t, joinLines(
			prompt("u-turn1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			prompt("u-turn2", "write the file"),
			oversize,
			call("a3", "msg_3", "tu_3", "Edit"),
		))
		if still.Turn.Complete {
			t.Fatalf("complete = true although the skipped record is INSIDE this turn")
		}
	})

	t.Run("UndecodableBoundaryRecordStaysDegraded", func(t *testing.T) {
		// A user record whose content array will not decode might have carried a tool_result, so
		// treating it as a plain prompt may be a spurious boundary. The reset must not clear that
		// doubt — it must carry the boundary record's own decode result.
		in := joinLines(
			prompt("u-turn1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			`{"isSidechain":false,"message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"tu_1","is_error":"NOT-A-BOOL"}]},"type":"user","uuid":"r1"}`,
			call("a2", "msg_2", "tu_2", "Edit"),
		)
		ev := derive(t, in)
		if ev.Turn.BoundaryUUID != "r1" {
			t.Fatalf("boundary_uuid = %q, want %q (the undecodable record was read as plain)", ev.Turn.BoundaryUUID, "r1")
		}
		if ev.Turn.Complete {
			t.Fatalf("complete = true; a boundary that may be spurious must stay disclosed")
		}
	})

	t.Run("BareBookkeepingFileHasNoBoundary", func(t *testing.T) {
		ev := derive(t, joinLines(noise()...))
		if got := ev.Marker(); got != "[tool evidence unavailable this turn]" {
			t.Fatalf("marker = %q, want the unavailable marker", got)
		}
	})

	t.Run("NoPanicOnPathologicalInput", func(t *testing.T) {
		for _, in := range []string{
			"\n\n\n",
			`{"type":"user","message":{"content":null}}` + "\n",
			`{"type":"user","message":{"content":[]}}` + "\n",
			`{"type":"user","message":{"content":[{"type":"tool_result"}]}}` + "\n",
			`{"type":"assistant","message":{"content":[{"type":"tool_use"}]}}` + "\n",
			`{"type":"assistant","message":{"content":"a bare string"}}` + "\n",
			`{"type":"user","message":42}` + "\n",
		} {
			func() {
				defer func() {
					if r := recover(); r != nil {
						t.Fatalf("panic on input %q: %v", in, r)
					}
				}()
				_ = derive(t, in)
			}()
		}
	})
}

// ---------------------------------------------------------------------------------------------
// Supplementary coverage (deliberately outside the TestGateEvidence_ prefix so the phase's
// seven-test acceptance count stays exact).
// ---------------------------------------------------------------------------------------------

func TestEvidenceRecord_FieldTruncation(t *testing.T) {
	long := strings.Repeat("y", 512)
	in := joinLines(
		prompt("u-1", "go"),
		assistantRec("a1", "msg_1", "2026-08-09T10:00:00Z", false,
			toolUseBlock("tu_1", "Write", fmt.Sprintf(`{"path":"a.go","content":%q,"limit":10,"opts":{"n":1}}`, long))),
	)
	ev := derive(t, in)
	if len(ev.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(ev.Calls))
	}
	in1 := ev.Calls[0].Input

	if got := len([]rune(in1["content"])); got != 300 {
		t.Fatalf("input.content = %d chars, want 300 (per-field truncation, design-doc.md:100)", got)
	}
	if in1["path"] != "a.go" {
		t.Fatalf("input.path = %q, want %q; a short field must be untouched", in1["path"], "a.go")
	}
	// Truncation is PER FIELD: a long value must not consume the budget of its siblings.
	if in1["limit"] != "10" {
		t.Fatalf("input.limit = %q, want %q; non-string values render as compact JSON", in1["limit"], "10")
	}
	if in1["opts"] != `{"n":1}` {
		t.Fatalf("input.opts = %q, want %q", in1["opts"], `{"n":1}`)
	}
}

func TestEvidenceRecord_TruncationCountsCharactersNotBytes(t *testing.T) {
	// design-doc.md:100 pins 300 CHARS. Every other truncation fixture here is ASCII, where bytes
	// and runes coincide and a `s[:limit]` implementation passes the whole suite — so this is the
	// only case that can kill that mutant. It also proves the cut lands on a rune boundary rather
	// than producing U+FFFD.
	const wide = "日" // 3 bytes, 1 rune

	t.Run("ResultContent", func(t *testing.T) {
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Bash"),
			result("r1", "tu_1", strings.Repeat(wide, 400)),
		)
		res := derive(t, in).Calls[0].Result
		if got := len([]rune(res.Content)); got != 300 {
			t.Fatalf("result = %d runes (%d bytes), want 300 runes; byte slicing yields 100 runes", got, len(res.Content))
		}
		if strings.ContainsRune(res.Content, '�') {
			t.Fatalf("truncation split a multi-byte rune")
		}
		if !res.Truncated {
			t.Fatalf("truncated = false for a 400-rune result")
		}
	})

	t.Run("InputField", func(t *testing.T) {
		in := joinLines(
			prompt("u-1", "go"),
			assistantRec("a1", "msg_1", "2026-08-09T10:00:00Z", false,
				toolUseBlock("tu_1", "Write", fmt.Sprintf(`{"content":%q}`, strings.Repeat(wide, 400)))),
		)
		got := derive(t, in).Calls[0].Input["content"]
		if len([]rune(got)) != 300 {
			t.Fatalf("input.content = %d runes (%d bytes), want 300 runes", len([]rune(got)), len(got))
		}
	})

	t.Run("ShortInRunesButLongInBytesIsNotTruncated", func(t *testing.T) {
		// 200 runes / 600 bytes: over the limit by the byte measure, under it by the char measure.
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Bash"),
			result("r1", "tu_1", strings.Repeat(wide, 200)),
		)
		res := derive(t, in).Calls[0].Result
		if res.Truncated || len([]rune(res.Content)) != 200 {
			t.Fatalf("truncated = %t at %d runes / %d bytes; want false at 200 runes",
				res.Truncated, len([]rune(res.Content)), len(res.Content))
		}
	})
}

func TestEvidenceRecord_DedupByBlockIdentity(t *testing.T) {
	// Gap 4. One message.id spans several single-block records (measured: msg_011Cdi8... spans 6
	// records, 4 of them tool_use, EVERY ONE at content[0]). A dedup key of
	// (message.id, index-within-record) collapses four real calls into one.
	t.Run("SameMessageIDDistinctCallsAllSurvive", func(t *testing.T) {
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_shared", "tu_1", "Read"),
			call("a2", "msg_shared", "tu_2", "Grep"),
			call("a3", "msg_shared", "tu_3", "Bash"),
			call("a4", "msg_shared", "tu_4", "Edit"),
		)
		want := []string{"Read", "Grep", "Bash", "Edit"}
		if got := toolNames(derive(t, in)); !reflect.DeepEqual(got, want) {
			t.Fatalf("tools = %v, want %v; a (message.id, block-index) key collapses these to one call", got, want)
		}
	})

	t.Run("RepeatedBlockIDCountsOnce", func(t *testing.T) {
		// The multi-block duplication mechanism is absent from the current single-block format but
		// the design requires tolerance of both shapes: the same tool_use id emitted twice is one
		// call.
		in := joinLines(
			prompt("u-1", "go"),
			call("a1", "msg_1", "tu_1", "Read"),
			assistantRec("a2", "msg_1", "2026-08-09T10:00:00Z", false,
				toolUseBlock("tu_1", "Read", `{"path":"a.go"}`)+","+toolUseBlock("tu_2", "Edit", `{"path":"a.go"}`)),
		)
		want := []string{"Read", "Edit"}
		if got := toolNames(derive(t, in)); !reflect.DeepEqual(got, want) {
			t.Fatalf("tools = %v, want %v; a repeated tool_use id must count once", got, want)
		}
	})
}

// TestRecordedRealFixtureSeam activates the moment Phase 1's capture gate lands its corpus at
// internal/transcript/testdata/recorded-real/. Until then it SKIPS — deliberately, because D-10
// (design-doc.md:166) rules that fixtures are captured, not authored, and minting a file into
// recorded-real/ would be a false provenance claim rather than a test.
//
// It is named outside the TestGateEvidence_ prefix so it can never inflate or deflate the phase's
// seven-test acceptance count.
func TestRecordedRealFixtureSeam(t *testing.T) {
	dir := filepath.Join("testdata", "recorded-real")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("Phase 1 capture gate has not landed: " + dir + " does not exist")
		}
		t.Fatalf("reading %s: %v", dir, err)
	}
	found := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		found++
		path := filepath.Join(dir, e.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading %s: %v", path, err)
		}
		ev := DeriveFile(path, DefaultOptions())

		// Anti-tautology bar (internal/statusline/tokens_test.go:182-192): pin the derived figure
		// AND a differential against what a session-global reader would produce, so a no-op
		// "boundary" cannot pass on real bytes.
		fileWide := strings.Count(string(raw), `"type":"tool_use"`)
		if ev.Turn.CallsTotal > fileWide {
			t.Fatalf("%s: turn calls %d exceed the file's %d tool_use blocks — the derivation invented calls",
				e.Name(), ev.Turn.CallsTotal, fileWide)
		}
		if fileWide > 0 && ev.Turn.CallsTotal == fileWide && ev.Turn.BoundaryUUID == "" {
			t.Fatalf("%s: every tool_use in the file was reported with no boundary found — that is the session-global tail this package replaces",
				e.Name())
		}
		if ev.Turn.CallsTotal > 0 && ev.Turn.BoundaryUUID == "" {
			t.Fatalf("%s: %d calls reported with an empty boundary_uuid — evidence without a turn is a guess",
				e.Name(), ev.Turn.CallsTotal)
		}
	}
	if found == 0 {
		t.Skip("Phase 1 capture gate has not landed: no .jsonl fixtures in " + dir)
	}
}
