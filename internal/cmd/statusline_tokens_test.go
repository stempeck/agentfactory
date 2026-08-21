package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/statusline"
)

func transcriptRecord(msgID string, in, out int64) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"requestId":"req_%s",`+
		`"message":{"id":%q,"stop_reason":"end_turn",`+
		`"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":5,"cache_read_input_tokens":9}}}`,
		msgID, msgID, in, out)
}

func writeTranscript(t *testing.T, path string, lines ...string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("write transcript: %v", err)
	}
}

func readSnapshotRaw(t *testing.T, root, sid string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(config.StatuslineDir(root), "sessions", sid+".json"))
	if err != nil {
		t.Fatalf("reading snapshot: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("decoding snapshot: %v", err)
	}
	return m
}

func enabledFactory(t *testing.T) string {
	t.Helper()
	root := setupConfigFactory(t)
	if err := os.WriteFile(statuslineGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("gate: %v", err)
	}
	return root
}

// Without this, K2 could be fully unit-tested and fully UNWIRED: every library pin would stay green
// while the render path never opened a transcript. This is the only test that proves the payload's
// transcript_path reaches the persisted counter.
func TestRenderCore_TranscriptTokensReachTheSnapshot(t *testing.T) {
	root := enabledFactory(t)
	tp := filepath.Join(t.TempDir(), "session.jsonl")
	// A trailing message is always held back, so two are needed for the first to be counted.
	writeTranscript(t, tp,
		transcriptRecord("msg_one", 10, 100),
		transcriptRecord("msg_two", 20, 200),
	)

	payload := fmt.Sprintf(`{"session_id":"wired","transcript_path":%q,"model":{"display_name":"X"},`+
		`"cost":{"total_cost_usd":1.00}}`, tp)

	var out bytes.Buffer
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", now, false); err != nil {
		t.Fatalf("render must return nil, got: %v", err)
	}

	snap := readSnapshotRaw(t, root, "wired")
	if got := snap["cum_tokens"]; got != float64(110) {
		t.Fatalf("cum_tokens = %v, want 110 (msg_one; msg_two is the held-back trailing run)", got)
	}
	if got := snap["transcript_offset"]; got == float64(0) {
		t.Fatal("transcript_offset = 0: the cursor never advanced, so every render re-reads from byte zero")
	}
	if got := snap["first_seen"]; got == "" || got == nil {
		t.Fatal("first_seen not recorded")
	}

	t.Run("DailySumSeesIt", func(t *testing.T) {
		sessionsDir := filepath.Join(config.StatuslineDir(root), "sessions")
		if d := statusline.SumDaily(sessionsDir, now); d.Tokens != 110 {
			t.Fatalf("SumDaily Tokens = %d, want 110", d.Tokens)
		}
	})

	t.Run("GrowthIsIncremental", func(t *testing.T) {
		// Appending must advance the counter without re-counting msg_one.
		f, err := os.OpenFile(tp, os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		fmt.Fprintln(f, transcriptRecord("msg_three", 30, 300))
		f.Close()

		var out2 bytes.Buffer
		later := now.Add(30 * time.Second) // past the throttle window
		if err := runStatuslineRenderCore(&out2, root, strings.NewReader(payload), "manager", later, false); err != nil {
			t.Fatalf("render: %v", err)
		}
		if got := readSnapshotRaw(t, root, "wired")["cum_tokens"]; got != float64(330) {
			t.Fatalf("cum_tokens = %v, want 330 (msg_one + msg_two); a re-read from zero would show more", got)
		}
	})
}

// T6 / B1 (evolved from the Phase-2 "not-yet-rendered" pin): the render path now DOES render the
// session token figure, sourced from the truthful cumulative counter (K2) via SessionTokens, shown
// compactly (6000 ⇒ "6k") beside the cost — never the raw integer. RED against PR-head code, whose
// render path is still cost-only.
func TestRenderCore_RendersSessionTokenFigure(t *testing.T) {
	root := enabledFactory(t)
	tp := filepath.Join(t.TempDir(), "session.jsonl")
	writeTranscript(t, tp,
		transcriptRecord("msg_a", 1000, 5000),
		transcriptRecord("msg_b", 1000, 5000),
	)
	payload := fmt.Sprintf(`{"session_id":"render","transcript_path":%q,"model":{"display_name":"X"},`+
		`"cost":{"total_cost_usd":1.00}}`, tp)

	var out bytes.Buffer
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", now, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if got := readSnapshotRaw(t, root, "render")["cum_tokens"]; got != float64(6000) {
		t.Fatalf("setup: cum_tokens = %v, want 6000", got)
	}
	if !strings.Contains(out.String(), "6k tok") {
		t.Fatalf("render must show the session token figure `6k tok` from the counter; got %q", out.String())
	}
	// The compact figure only — never the raw integer.
	if strings.Contains(out.String(), "6000") {
		t.Fatalf("render leaked the raw counter instead of the compact figure: %q", out.String())
	}
}

// security.md E2.1 (T1): transcript_path is a semi-trusted payload field. Every hostile or broken
// shape must degrade to "no new tokens" — never a non-nil error, never stderr, and never a counter
// reset, because a reset would let the figure go backwards.
func TestRenderCore_HostileTranscriptPathDegradesSilently(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real.jsonl")
	writeTranscript(t, real, transcriptRecord("msg_seed", 10, 100), transcriptRecord("msg_tail", 1, 1))

	cases := []struct {
		name string
		path string
	}{
		{"absent file", filepath.Join(dir, "nope.jsonl")},
		{"a directory", dir},
		{"traversal-looking path", filepath.Join(dir, "..", "..", "etc", "passwd")},
		{"character device", "/dev/null"},
		{"empty file", func() string {
			p := filepath.Join(dir, "empty.jsonl")
			os.WriteFile(p, nil, 0o644)
			return p
		}()},
		{"binary garbage", func() string {
			p := filepath.Join(dir, "garbage.jsonl")
			os.WriteFile(p, []byte{0x00, 0xff, 0xfe, 0x01, 0x02, '\n'}, 0o644)
			return p
		}()},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := enabledFactory(t)
			now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)

			// Seed a real counter first, so a reset would be visible.
			seed := fmt.Sprintf(`{"session_id":"h","transcript_path":%q,"cost":{"total_cost_usd":1.0}}`, real)
			var b0 bytes.Buffer
			if err := runStatuslineRenderCore(&b0, root, strings.NewReader(seed), "manager", now, false); err != nil {
				t.Fatalf("seed render: %v", err)
			}
			if got := readSnapshotRaw(t, root, "h")["cum_tokens"]; got != float64(110) {
				t.Fatalf("seed cum_tokens = %v, want 110", got)
			}

			payload := fmt.Sprintf(`{"session_id":"h","transcript_path":%q,"cost":{"total_cost_usd":1.0}}`, tc.path)
			var out bytes.Buffer
			if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", now.Add(time.Minute), false); err != nil {
				t.Fatalf("render must return nil for %s, got: %v", tc.name, err)
			}
			if got := readSnapshotRaw(t, root, "h")["cum_tokens"]; got != float64(110) {
				t.Fatalf("cum_tokens = %v after %s, want 110 preserved — the counter must never be reset by a bad path", got, tc.name)
			}
		})
	}
}

// security.md E2.1 (T4): numbers only. No transcript STRING may reach the pane or the snapshot —
// including a watchdog needle, which in the pane would trigger a false endpoint-failure respawn of
// a healthy agent.
func TestRenderCore_TranscriptStringsNeverCrossTheBoundary(t *testing.T) {
	root := enabledFactory(t)
	tp := filepath.Join(t.TempDir(), "hostile.jsonl")
	needle := "unsupported_api_for_model"
	// A real transcript escapes control bytes, so this record is VALID JSON whose DECODED content
	// carries a live ANSI sequence plus a watchdog needle - the shape that would reach a pane.
	hostile := fmt.Sprintf(`{"type":"assistant","isSidechain":false,"requestId":"req_h",`+
		`"message":{"id":"msg_h","stop_reason":"end_turn","content":[{"type":"text","text":"\\u001b[31m%s\\u001b[0m SECRETLEAK"}],`+
		`"usage":{"input_tokens":7,"output_tokens":13,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`, needle)
	writeTranscript(t, tp, hostile, transcriptRecord("msg_tail", 1, 1))

	payload := fmt.Sprintf(`{"session_id":"hostile","transcript_path":%q,"model":{"display_name":"X"},`+
		`"cost":{"total_cost_usd":1.00}}`, tp)

	var out bytes.Buffer
	now := time.Date(2026, 8, 2, 9, 0, 0, 0, time.UTC)
	if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", now, false); err != nil {
		t.Fatalf("render: %v", err)
	}

	if got := readSnapshotRaw(t, root, "hostile")["cum_tokens"]; got != float64(20) {
		t.Fatalf("cum_tokens = %v, want 20 — the numbers must still be read", got)
	}

	rendered := out.String()
	snapBytes, err := os.ReadFile(filepath.Join(config.StatuslineDir(root), "sessions", "hostile.json"))
	if err != nil {
		t.Fatalf("reading snapshot bytes: %v", err)
	}
	// The pane now carries the renderer's OWN palette SGR (colour is ON by default) — a legitimate,
	// grammar-checked escape (statusline TestRenderColor_OnlyOwnPaletteSGR). The security property that
	// still holds absolutely: the transcript's own content (its needle, its "SECRETLEAK", its hostile
	// ESC) must never reach the pane, and the snapshot stays numbers-only (no content, no ESC ever).
	paneNoSGR := regexp.MustCompile("\\[[0-9;]*m").ReplaceAllString(rendered, "")
	if strings.ContainsRune(paneNoSGR, 0x1b) {
		t.Fatalf("a non-palette ESC reached the pane: %q", rendered)
	}
	for _, probe := range []string{needle, "SECRETLEAK"} {
		if strings.Contains(rendered, probe) {
			t.Fatalf("transcript content %q reached the pane: %q", probe, rendered)
		}
	}
	for _, probe := range []string{needle, "SECRETLEAK", ""} {
		if strings.Contains(string(snapBytes), probe) {
			t.Fatalf("transcript content %q was persisted into the snapshot: %s", probe, snapBytes)
		}
	}
}

// scale.md S3.1: when nothing has been appended the reader must not read at all. Without the
// stat-first short-circuit, every idle refreshInterval tick re-reads the transcript tail.
func TestReadTranscriptDelta_StatFirstShortCircuit(t *testing.T) {
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, tp, transcriptRecord("msg_1", 10, 100), transcriptRecord("msg_2", 20, 200))

	first, err := readTranscriptDelta(tp, statusline.TranscriptCursor{})
	if err != nil {
		t.Fatalf("readTranscriptDelta: %v", err)
	}
	if first.CumTokens != 110 {
		t.Fatalf("CumTokens = %d, want 110", first.CumTokens)
	}

	fi, err := os.Stat(tp)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// A real at-EOF cursor carries the head fingerprint stamped by the read that advanced it there;
	// carry first.HeadSig so this models production rather than an artificial zero-signature cursor.
	atEOF := statusline.TranscriptCursor{CumTokens: first.CumTokens, RederiveTokens: first.RederiveTokens, Offset: fi.Size(), HeadSig: first.HeadSig}
	again, err := readTranscriptDelta(tp, atEOF)
	if err != nil {
		t.Fatalf("readTranscriptDelta: %v", err)
	}
	if again != atEOF {
		t.Fatalf("cursor changed on an unchanged file: %+v -> %+v", atEOF, again)
	}

	t.Run("TruncationResetsOffsetKeepingCounter", func(t *testing.T) {
		writeTranscript(t, tp, transcriptRecord("msg_small", 1, 2))
		got, err := readTranscriptDelta(tp, atEOF)
		if err != nil {
			t.Fatalf("readTranscriptDelta: %v", err)
		}
		if got.CumTokens < first.CumTokens {
			t.Fatalf("CumTokens = %d dropped below %d after truncation", got.CumTokens, first.CumTokens)
		}
	})
}

// Gap 14: the render path is required to stay silent about transcript failures, so status is the
// only place an operator can find out the counter is not advancing.
func TestStatuslineStatus_NamesTranscriptTokenState(t *testing.T) {
	root := enabledFactory(t)
	now := time.Now()

	if note := statuslineTokenNote(root, now); !strings.Contains(note, "no session snapshots") {
		t.Fatalf("fresh factory note = %q, want it to name the absence of snapshots", note)
	}

	// A session that rendered but counted nothing (no transcript_path) must be named as a FAILURE,
	// not silently reported as healthy.
	payload := `{"session_id":"notok","model":{"display_name":"X"},"cost":{"total_cost_usd":1.00}}`
	var out bytes.Buffer
	if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", now, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	note := statuslineTokenNote(root, now)
	if !strings.Contains(note, "FAILED") || !strings.Contains(note, "transcript_path") {
		t.Fatalf("note = %q, want it to NAME that no transcript tokens were counted", note)
	}

	// With a real transcript the same line reports ok.
	tp := filepath.Join(t.TempDir(), "s.jsonl")
	writeTranscript(t, tp, transcriptRecord("msg_1", 10, 100), transcriptRecord("msg_2", 1, 1))
	good := fmt.Sprintf(`{"session_id":"okses","transcript_path":%q,"cost":{"total_cost_usd":1.00}}`, tp)
	var out2 bytes.Buffer
	if err := runStatuslineRenderCore(&out2, root, strings.NewReader(good), "manager", now, false); err != nil {
		t.Fatalf("render: %v", err)
	}
	if note := statuslineTokenNote(root, now); !strings.Contains(note, "tokens: ok") {
		t.Fatalf("note = %q, want \"tokens: ok\" once a transcript has been counted", note)
	}
}
