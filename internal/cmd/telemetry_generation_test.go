package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/telemetry"
)

// transcriptLine renders one JSONL record in the shape Claude Code actually writes: the whole
// message's usage stamped on EVERY record of that message, and exactly one content block per
// record. Verified against a live transcript (claude 2.1.224): of 123 messages in one session, 60
// spanned multiple records, every one of which repeated the same output_tokens.
//
// thinking is variadic because ABSENCE is the interesting case (#678 K1): a host that predates
// output_tokens_details omits the object entirely, and the derivation has to tell that apart from a
// host that reported zero thinking. Passing no value reproduces the older host exactly, which is
// what every call site written before the exact leg existed should keep doing.
func transcriptLine(ts, msgID, blockType, text string, in, out, cacheRead, cacheCreate int64, thinking ...int64) string {
	block := fmt.Sprintf(`{"type":%q,"text":%q}`, blockType, text)
	if blockType == "thinking" {
		// Measured: 44 thinking blocks in one real session carried 0 characters between them.
		// The text is not written to the transcript — only a signature is.
		block = `{"type":"thinking","thinking":"","signature":"sig"}`
	}
	if blockType == "tool_use" {
		block = `{"type":"tool_use","id":"tu","name":"Read","input":{"file_path":"/x"}}`
	}
	details := ""
	if len(thinking) > 0 {
		details = fmt.Sprintf(`,"output_tokens_details":{"thinking_tokens":%d}`, thinking[0])
	}
	return fmt.Sprintf(
		`{"timestamp":%q,"type":"assistant","message":{"id":%q,"role":"assistant","content":[%s],`+
			`"usage":{"input_tokens":%d,"output_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d%s}}}`,
		ts, msgID, block, in, out, cacheRead, cacheCreate, details)
}

func writeGenerationTranscript(t *testing.T, lines ...string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "session.jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing transcript: %v", err)
	}
	return path
}

// seedTranscript writes a measurable transcript at exactly the location production derives, so a
// test that expects a REFUSAL is refusing something that was there to be read.
func seedTranscript(t *testing.T, workDir, sessionID string, lines ...string) string {
	t.Helper()
	path := sessionTranscriptPath(workDir, sessionID)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing transcript: %v", err)
	}
	return path
}

func derive(t *testing.T, path, start, end string) generationScalars {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("opening transcript: %v", err)
	}
	defer f.Close()
	return deriveGenerationScalars(f, start, end)
}

// TestMessageDedup is the counting-method proof, and it is the ONE test this whole derivation
// exists to satisfy. The ~93% thinking figure that motivated #668 came from summing JSONL lines in
// ad-hoc python: Claude Code writes one record per content block and stamps the whole message's
// usage on every one, so line-summing over-counted by ~2.2x.
//
// The proof is an equivalence, not a magic number: a transcript emitting each message as THREE
// records must yield exactly the totals of its ONE-record-per-message equivalent, and both must be
// strictly below the naive per-record sum.
//
// msg_A's FIRST record is in flight and carries a partial output count that its later records
// supersede. That is what separates MAX from first-wins, and a fixture without it would pass just as
// happily for either: first-wins would keep the 1 and report a message that generated nothing. The
// statusline's copy of this rule measured 26M output tokens lost to exactly that mistake.
func TestMessageDedup(t *testing.T) {
	const start, end = "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"

	fanned := writeGenerationTranscript(t,
		transcriptLine("2026-08-30T11:39:37.520Z", "msg_A", "text", "hello", 1000, 1, 50, 5),
		transcriptLine("2026-08-30T11:39:38.858Z", "msg_A", "tool_use", "", 1000, 261, 50, 5),
		transcriptLine("2026-08-30T11:39:39.217Z", "msg_A", "tool_use", "", 1000, 261, 50, 5),
		transcriptLine("2026-08-30T11:39:43.359Z", "msg_B", "thinking", "", 2000, 495, 60, 6),
		transcriptLine("2026-08-30T11:39:44.077Z", "msg_B", "tool_use", "", 2000, 495, 60, 6),
	)
	single := writeGenerationTranscript(t,
		transcriptLine("2026-08-30T11:39:37.520Z", "msg_A", "text", "hello", 1000, 261, 50, 5),
		transcriptLine("2026-08-30T11:39:43.359Z", "msg_B", "thinking", "", 2000, 495, 60, 6),
	)

	got, want := derive(t, fanned, start, end), derive(t, single, start, end)
	if !got.measured || !want.measured {
		t.Fatal("both transcripts are within the window and must measure")
	}
	if *got.out != *want.out {
		t.Errorf("out_tokens: 3-records-per-message gave %d, 1-record-per-message gave %d; "+
			"a value of %d means first-wins kept msg_A's in-flight partial", *got.out, *want.out, 1+495)
	}
	if *got.peak != *want.peak {
		t.Errorf("peak_ctx_tokens: %d vs %d", *got.peak, *want.peak)
	}

	if wantOut := int64(261 + 495); *got.out != wantOut {
		t.Errorf("out_tokens = %d, want %d (one count per message)", *got.out, wantOut)
	}
	// The bug this replaced: summing every record gives 1513 against a true 756.
	if naive := int64(1 + 261 + 261 + 495 + 495); *got.out >= naive {
		t.Errorf("out_tokens = %d is not below the naive per-record sum %d; the records are not being deduped", *got.out, naive)
	}
}

// The two rules run in OPPOSITE directions over the same pass, and getting either backwards is
// silent. Usage is repeated on every record of a message, so it must be deduped. Content blocks
// appear exactly ONCE each, so deduping them would throw away every text block after the first and
// inflate the thinking estimate — which is the very figure this feature is trying to stop
// over-stating.
func TestGenerationScalarsDedupUsageButNotText(t *testing.T) {
	const start, end = "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"
	// 400 characters of visible text spread over two records of one message ⇒ 100 estimated
	// visible tokens against 500 output ⇒ 400 estimated thinking.
	half := strings.Repeat("x", 200)

	path := writeGenerationTranscript(t,
		transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "thinking", "", 1000, 500, 0, 0),
		transcriptLine("2026-08-30T11:10:01.000Z", "msg_A", "text", half, 1000, 500, 0, 0),
		transcriptLine("2026-08-30T11:10:02.000Z", "msg_A", "text", half, 1000, 500, 0, 0),
		transcriptLine("2026-08-30T11:10:03.000Z", "msg_A", "tool_use", "", 1000, 500, 0, 0),
	)

	got := derive(t, path, start, end)
	if !got.measured {
		t.Fatal("want measured")
	}
	if *got.out != 500 {
		t.Errorf("out_tokens = %d, want 500 counted once for the one message", *got.out)
	}
	if *got.thinkEst != 400 {
		t.Errorf("think_tokens_est = %d, want 400 (500 output - 400 visible chars / 4); "+
			"a value of 450 means only the first text block was counted", *got.thinkEst)
	}
}

// The estimate is a subtraction and a subtraction can go negative. PR #595's daily figure went
// negative for a structurally identical reason, so the clamp is stated as behavior here.
func TestThinkTokensEstNeverNegative(t *testing.T) {
	const start, end = "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"
	path := writeGenerationTranscript(t,
		// 4000 characters ⇒ 1000 estimated visible tokens against 10 reported output.
		transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "text", strings.Repeat("y", 4000), 100, 10, 0, 0),
	)

	got := derive(t, path, start, end)
	if !got.measured {
		t.Fatal("want measured")
	}
	if *got.thinkEst != 0 {
		t.Errorf("think_tokens_est = %d, want 0; the estimate is clamped, never negative", *got.thinkEst)
	}
}

// Peak is the MAXIMUM occupancy across the step, not the reading at either end. That is what makes
// it a single-pass appetite figure: a step that peaked at 190k and closed at 40k after a compaction
// did not fit in a 128k window, and neither endpoint says so.
func TestPeakCtxTokensIsTheMaximum(t *testing.T) {
	const start, end = "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"
	path := writeGenerationTranscript(t,
		transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "text", "a", 10_000, 100, 1_000, 100),
		transcriptLine("2026-08-30T11:11:00.000Z", "msg_B", "text", "b", 90_000, 200, 9_000, 900),
		transcriptLine("2026-08-30T11:12:00.000Z", "msg_C", "text", "c", 5_000, 50, 500, 50),
	)

	got := derive(t, path, start, end)
	if want := int64(90_000 + 200 + 9_000 + 900); *got.peak != want {
		t.Errorf("peak_ctx_tokens = %d, want %d (the middle message, not the last)", *got.peak, want)
	}
}

// Only the closing step's window is measured. A transcript is one SESSION's, and a session outlives
// a step — without the filter every step in a session would report the session's whole generation.
func TestGenerationScalarsWindowExcludesOtherSteps(t *testing.T) {
	path := writeGenerationTranscript(t,
		transcriptLine("2026-08-30T10:00:00.000Z", "msg_before", "text", "old", 1000, 999, 0, 0),
		transcriptLine("2026-08-30T11:10:00.000Z", "msg_in", "text", "now", 1000, 100, 0, 0),
		transcriptLine("2026-08-30T11:59:00.000Z", "msg_after", "text", "new", 1000, 777, 0, 0),
	)

	got := derive(t, path, "2026-08-30T11:00:00.000Z", "2026-08-30T11:30:00.000Z")
	if !got.measured {
		t.Fatal("want measured")
	}
	if *got.out != 100 {
		t.Errorf("out_tokens = %d, want only the in-window message's 100", *got.out)
	}
}

// The window is half-open [start, end). A record stamped exactly at the close belongs to whatever
// comes next, and one stamped exactly at the open belongs to this step — the same rule the
// telemetry join uses, which is why TimestampLayout carries milliseconds at all.
func TestGenerationScalarsWindowIsHalfOpen(t *testing.T) {
	path := writeGenerationTranscript(t,
		transcriptLine("2026-08-30T11:00:00.000Z", "msg_at_start", "text", "s", 1000, 11, 0, 0),
		transcriptLine("2026-08-30T11:30:00.000Z", "msg_at_end", "text", "e", 1000, 22, 0, 0),
	)

	got := derive(t, path, "2026-08-30T11:00:00.000Z", "2026-08-30T11:30:00.000Z")
	if *got.out != 11 {
		t.Errorf("out_tokens = %d, want 11: the start is inclusive and the end exclusive", *got.out)
	}
}

// Nothing to measure is reported as nothing, never as zero. Absent and zero are different facts and
// the pointers exist to keep them apart.
func TestGenerationScalarsUnmeasurable(t *testing.T) {
	t.Run("an empty window", func(t *testing.T) {
		path := writeGenerationTranscript(t, transcriptLine("2026-08-30T09:00:00.000Z", "m", "text", "x", 1, 1, 0, 0))
		if got := derive(t, path, "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"); got.measured {
			t.Errorf("no records in the window but got %+v", got)
		}
	})

	t.Run("a transcript of garbage", func(t *testing.T) {
		path := writeGenerationTranscript(t, "not json", "{", `{"no":"message"}`)
		if got := derive(t, path, "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"); got.measured {
			t.Errorf("a malformed transcript must degrade to unmeasured, got %+v", got)
		}
	})

	t.Run("an absent file", func(t *testing.T) {
		got := transcriptGenerationScalars(filepath.Join(t.TempDir(), "nope.jsonl"),
			"2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z")
		if got.measured {
			t.Errorf("an unreadable transcript must degrade to unmeasured, got %+v", got)
		}
	})
}

// A malformed line must not blank the whole reading. This is the telemetry parseRecordFile idiom
// and the same tolerance internal/statusline's scanner has: one bad line is a bad line, not a
// broken step.
func TestGenerationScalarsSkipsMalformedLines(t *testing.T) {
	path := writeGenerationTranscript(t,
		"{{{ not json",
		transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "text", "a", 1000, 100, 0, 0),
		`{"timestamp":"2026-08-30T11:11:00.000Z","message":{"id":"","content":[]}}`,
		transcriptLine("2026-08-30T11:12:00.000Z", "msg_B", "text", "b", 1000, 200, 0, 0),
	)

	got := derive(t, path, "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z")
	if !got.measured || *got.out != 300 {
		t.Errorf("got %+v, want the two well-formed messages' 300", got)
	}
}

// One oversized record must not end the reading. A bufio.Scanner ABANDONS the rest of the file at
// the first token above its cap, so every message after a 4MB tool result would silently vanish
// while the step still reported itself measured. Real transcripts reach 2.4MB on a single line, so
// this is a live hazard rather than a hypothetical one: the shared reader drops the line's content
// and keeps going.
func TestGenerationScalarsSkipsOversizedLine(t *testing.T) {
	body := strings.Join([]string{
		transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "text", "a", 1000, 100, 0, 0),
		transcriptLine("2026-08-30T11:11:00.000Z", "msg_huge", "text", strings.Repeat("q", 5<<20), 1000, 999, 0, 0),
		transcriptLine("2026-08-30T11:12:00.000Z", "msg_B", "text", "b", 1000, 200, 0, 0),
	}, "\n") + "\n"

	got := deriveGenerationScalars(strings.NewReader(body), "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z")
	if !got.measured {
		t.Fatal("want measured")
	}
	if *got.out != 300 {
		t.Errorf("out_tokens = %d, want 300: the oversized record is skipped and the message AFTER "+
			"it is still read; 100 means the reader stopped at the oversized line", *got.out)
	}
}

// The path convention is a fact about the host, not about af, so it is pinned rather than inferred.
// Verified against claude 2.1.224: <configDir>/projects/<cwd with [/._] mapped to ->/<session>.jsonl.
func TestSessionTranscriptPath(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)

	got := sessionTranscriptPath("/home/dev/af/x.y/.agentfactory/agents/manager", "abc-123")
	want := filepath.Join(dir, "projects", "-home-dev-af-x-y--agentfactory-agents-manager", "abc-123.jsonl")
	if got != want {
		t.Errorf("sessionTranscriptPath = %q, want %q", got, want)
	}

	// Underscore is the character that bites silently. ValidateAgentName permits it, so an agent
	// named soldesign_engineer under a repo called my_repo would resolve to a directory that does
	// not exist and record no generation figures at all, forever, with nothing to say why.
	t.Run("underscores fold to hyphens in every component", func(t *testing.T) {
		got := sessionTranscriptPath("/home/dev/my_repo/.agentfactory/agents/soldesign_engineer", "s1")
		want := filepath.Join(dir, "projects", "-home-dev-my-repo--agentfactory-agents-soldesign-engineer", "s1.jsonl")
		if got != want {
			t.Errorf("sessionTranscriptPath = %q, want %q", got, want)
		}
	})

	t.Run("no session id yields no path", func(t *testing.T) {
		if got := sessionTranscriptPath("/home/dev", ""); got != "" {
			t.Errorf("got %q, want \"\" — there is no transcript for a session nobody recorded", got)
		}
	})
}

// attachGenerationScalars refuses BEFORE it opens anything when the step spanned a session recycle.
// The guard is stepCumTokensDelta's and exists for the same reason: a transcript is ONE session's,
// so a step whose first half lives in a file this code will never open would report part of a step
// while looking like the whole of one.
//
// Each case SEEDS a measurable transcript at the path a guard-less version would open. Without that
// the test passes for the wrong reason — an empty config dir has nothing to find, so deleting the
// guard entirely would leave it green.
func TestAttachGenerationScalarsRefusesAcrossSessions(t *testing.T) {
	const workDir = "/home/dev/af/.agentfactory/agents/manager"

	for _, tc := range []struct {
		name       string
		span       stepSpan
		ev         telemetry.StepEvent
		wantReason string
	}{
		{
			"a session recycle mid-step",
			stepSpan{startTS: "2026-08-30T11:00:00.000Z", sessionID: "session-one"},
			telemetry.StepEvent{TS: "2026-08-30T11:30:00.000Z", SessionID: "session-two"},
			telemetry.ReasonSessionMismatch,
		},
		{
			"no session recorded at the open",
			stepSpan{startTS: "2026-08-30T11:00:00.000Z"},
			telemetry.StepEvent{TS: "2026-08-30T11:30:00.000Z", SessionID: "session-one"},
			telemetry.ReasonSessionMismatch,
		},
		{
			// Also belt-and-braces: an empty session id yields no path to open, so the != clause
			// above already covers it. Stated as behaviour because "no session id" is the state an
			// agent launched outside a formula is in, and it must never measure a stranger's file.
			"no session recorded at the close",
			stepSpan{startTS: "2026-08-30T11:00:00.000Z", sessionID: "session-one"},
			telemetry.StepEvent{TS: "2026-08-30T11:30:00.000Z"},
			telemetry.ReasonSessionMismatch,
		},
		{
			// Telemetry switched on mid-formula: there is no step_start, so there is no window. The
			// reason differs from the three above and that is the point — this is the expected
			// consequence of an operator turning the feature on, not two ends of one step
			// disagreeing about which session ran it.
			"no step_start to open the window",
			stepSpan{sessionID: "session-one"},
			telemetry.StepEvent{TS: "2026-08-30T11:30:00.000Z", SessionID: "session-one"},
			telemetry.ReasonNoRecordsInWindow,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
			for _, sessionID := range []string{"session-one", "session-two"} {
				seedTranscript(t, workDir, sessionID,
					transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "text", "a", 1000, 100, 10, 1))
			}

			ev := tc.ev
			attachGenerationScalars(&ev, tc.span, workDir)
			if ev.OutTokens != nil || ev.ThinkTokensEst != nil || ev.PeakCtxTokens != nil {
				t.Errorf("scalars attached across an unmeasurable span: %+v", ev)
			}
			if ev.GenerationUnmeasuredReason != tc.wantReason {
				t.Errorf("generation_unmeasured_reason = %q, want %q — the reason is what makes the "+
					"degrade path auditable, and a wrong one reports a defect that did not happen",
					ev.GenerationUnmeasuredReason, tc.wantReason)
			}
		})
	}
}

// The whole derivation is fail-closed: a close whose transcript is missing records the step without
// the generation figures rather than failing the verb. `af done` closes work; it does not depend on
// a file the host owns and may have expired.
func TestAttachGenerationScalarsWithNoTranscript(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir()) // exists, but holds no projects/ tree at all

	ev := telemetry.StepEvent{TS: "2026-08-30T11:30:00.000Z", SessionID: "session-one"}
	attachGenerationScalars(&ev, stepSpan{startTS: "2026-08-30T11:00:00.000Z", sessionID: "session-one"}, "/home/dev/x")

	if ev.OutTokens != nil || ev.ThinkTokensEst != nil || ev.PeakCtxTokens != nil {
		t.Errorf("an absent transcript must leave every scalar nil, got %+v", ev)
	}
}

// The happy path end to end: a transcript at exactly the derived location, read through the same
// path construction production uses.
func TestAttachGenerationScalarsReadsTheDerivedPath(t *testing.T) {
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())

	const workDir, sessionID = "/home/dev/af/.agentfactory/agents/manager", "sess-abc"
	seedTranscript(t, workDir, sessionID,
		transcriptLine("2026-08-30T11:10:00.000Z", "msg_A", "text", strings.Repeat("z", 40), 1000, 100, 10, 1),
		transcriptLine("2026-08-30T11:10:01.000Z", "msg_A", "tool_use", "", 1000, 100, 10, 1),
	)

	ev := telemetry.StepEvent{TS: "2026-08-30T11:30:00.000Z", SessionID: sessionID}
	attachGenerationScalars(&ev, stepSpan{startTS: "2026-08-30T11:00:00.000Z", sessionID: sessionID}, workDir)

	if ev.OutTokens == nil || *ev.OutTokens != 100 {
		t.Fatalf("out_tokens = %v, want 100", ev.OutTokens)
	}
	if *ev.ThinkTokensEst != 90 {
		t.Errorf("think_tokens_est = %d, want 90 (100 - 40/4)", *ev.ThinkTokensEst)
	}
	if *ev.PeakCtxTokens != 1111 {
		t.Errorf("peak_ctx_tokens = %d, want 1111 (1000 + 10 + 1 + 100)", *ev.PeakCtxTokens)
	}
}

// TestTranscriptPathMustBeRegular pins that persistedTranscriptPath returns "" for a marker naming a
// non-regular path (here a directory), matching readTranscriptDelta's fi.Mode().IsRegular() guard
// (statusline_tokens.go:66-67): a path that exists but is not a readable append-only file is not a
// transcript, so the resolver falls back to the derivation rather than returning it. A directory is the
// portable stand-in for "exists but is not a regular file" (a FIFO would not be portable).
func TestTranscriptPathMustBeRegular(t *testing.T) {
	workDir := t.TempDir()
	runtimeDir := filepath.Join(workDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}

	notAFile := filepath.Join(workDir, "transcript_dir")
	if err := os.MkdirAll(notAFile, 0o755); err != nil {
		t.Fatalf("mkdir transcript dir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(runtimeDir, "transcript_path"),
		[]byte("sess-1\t"+notAFile+"\n"), 0o644); err != nil {
		t.Fatalf("write marker: %v", err)
	}

	if got := persistedTranscriptPath(workDir, "sess-1"); got != "" {
		t.Errorf("persistedTranscriptPath returned %q for a marker naming a directory; a non-regular "+
			"path is not a transcript and must fall back to the derivation (\"\")", got)
	}
}
