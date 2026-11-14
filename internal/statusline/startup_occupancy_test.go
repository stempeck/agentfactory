package statusline

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sourceSessionFirstStopOccupancy = 134117

func occAssistant(id string, input, cacheRead, cacheCreate, output int64) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"version":"2.1.322","message":{"id":%q,"model":"claude-opus-5","role":"assistant","usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":%d,"output_tokens":%d}}}`,
		id, input, cacheRead, cacheCreate, output)
}

func occSidechain(id string, input int64) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":true,"version":"2.1.322","message":{"id":%q,"model":"claude-opus-5","usage":{"input_tokens":%d,"output_tokens":1}}}`, id, input)
}

func occStop(subtype string) string {
	return `{"type":"system","subtype":"` + subtype + `","isSidechain":false}`
}

func occSidechainStop() string {
	return `{"type":"system","subtype":"stop_hook_summary","isSidechain":true}`
}

// occAPIError is the shape Claude Code writes for a failed request: a message id, the synthetic
// model and an all-zero usage, just before the turn's turn_duration record.
func occAPIError(id string) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"isApiErrorMessage":true,"version":"2.1.322","message":{"id":%q,"model":"<synthetic>","role":"assistant","usage":{"input_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0,"output_tokens":0}}}`, id)
}

const occUser = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"next request"}}`

// occRead writes the lines to a file in t.TempDir(), every line ending in "\n" because
// ReadTranscriptLine leaves an unterminated last line unread, and scans it.
func occRead(t *testing.T, lines ...string) (PeakOccupancy, bool) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l)
		b.WriteString("\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return TranscriptPeakOccupancy(f)
}

func occWant(t *testing.T, got PeakOccupancy, ok bool, wantTokens int64, wantID string, wantCut bool) {
	t.Helper()
	if !ok {
		t.Fatalf("ok = false (%+v), want a figure of %d", got, wantTokens)
	}
	if got.Tokens != wantTokens || got.MessageID != wantID || got.Cut != wantCut {
		t.Errorf("got {Tokens:%d MessageID:%q Cut:%v}, want {Tokens:%d MessageID:%q Cut:%v}",
			got.Tokens, got.MessageID, got.Cut, wantTokens, wantID, wantCut)
	}
}

func TestTranscriptPeakOccupancy_PeakMainMessageOncePerID(t *testing.T) {
	// msg_a is written once per content block with the whole message's usage on each line, the
	// in-flight ones carrying a partial output count. msg_c comes after a compaction and occupies
	// less than msg_b did.
	got, ok := occRead(t,
		occAssistant("msg_a", 100, 20000, 3000, 10),
		occAssistant("msg_a", 100, 20000, 3000, 40),
		occAssistant("msg_a", 100, 20000, 3000, 25),
		occUser,
		occAssistant("msg_b", 200, 45000, 5000, 300),
		occAssistant("msg_b", 200, 45000, 5000, 300),
		occAssistant("msg_c", 50, 9000, 1000, 20),
		occStop("stop_hook_summary"),
	)
	occWant(t, got, ok, 200+45000+5000+300, "msg_b", true)
	if got.HarnessVersion != "2.1.322" {
		t.Errorf("HarnessVersion = %q, want %q", got.HarnessVersion, "2.1.322")
	}

	t.Run("one id counted once, at its per-field maximum", func(t *testing.T) {
		got, ok := occRead(t,
			occAssistant("msg_a", 100, 20000, 3000, 10),
			occAssistant("msg_a", 100, 20000, 3000, 40),
			occAssistant("msg_a", 100, 20000, 3000, 25),
			occStop("turn_duration"),
		)
		occWant(t, got, ok, 100+20000+3000+40, "msg_a", true)
	})

	t.Run("the last non-empty version is kept", func(t *testing.T) {
		got, ok := occRead(t,
			strings.Replace(occAssistant("msg_a", 10, 0, 0, 1), "2.1.322", "2.1.300", 1),
			strings.Replace(occAssistant("msg_b", 20, 0, 0, 1), `"version":"2.1.322",`, "", 1),
			occStop("turn_duration"),
		)
		occWant(t, got, ok, 21, "msg_b", true)
		if got.HarnessVersion != "2.1.300" {
			t.Errorf("HarnessVersion = %q, want the last non-empty %q", got.HarnessVersion, "2.1.300")
		}
	})
}

func TestTranscriptPeakOccupancy_CutsAtFirstStop(t *testing.T) {
	for _, subtype := range []string{"stop_hook_summary", "turn_duration"} {
		t.Run(subtype, func(t *testing.T) {
			got, ok := occRead(t,
				occAssistant("msg_a", 1000, 0, 0, 10),
				occStop(subtype),
				occUser,
				occAssistant("msg_b", 50000, 0, 0, 10),
				occStop(subtype),
			)
			occWant(t, got, ok, 1010, "msg_a", true)
		})
	}

	t.Run("the same lines without the Stop record read to EOF, not cut", func(t *testing.T) {
		got, ok := occRead(t,
			occAssistant("msg_a", 1000, 0, 0, 10),
			occUser,
			occAssistant("msg_b", 50000, 0, 0, 10),
		)
		occWant(t, got, ok, 50010, "msg_b", false)
	})
}

func TestTranscriptPeakOccupancy_IgnoresSidechain(t *testing.T) {
	got, ok := occRead(t,
		occAssistant("msg_a", 1000, 0, 0, 0),
		occSidechain("msg_side", 90000),
		occSidechainStop(),
		occAssistant("msg_b", 2000, 0, 0, 0),
		occStop("stop_hook_summary"),
		occAssistant("msg_c", 9000, 0, 0, 0),
	)
	occWant(t, got, ok, 2000, "msg_b", true)

	t.Run("a side-chain record alone is no figure and its Stop does not cut", func(t *testing.T) {
		got, ok := occRead(t,
			occSidechain("msg_side", 90000),
			occSidechainStop(),
			occStop("stop_hook_summary"),
		)
		if ok || got.Cut {
			t.Errorf("got %+v ok=%v, want no figure and no cut", got, ok)
		}
	})
}

func TestTranscriptPeakOccupancy_SkipsAPIErrorRecord(t *testing.T) {
	t.Run("an error after a usage-bearing message lowers nothing", func(t *testing.T) {
		got, ok := occRead(t,
			occAssistant("msg_a", 4000, 0, 0, 100),
			occAPIError("msg_err"),
			occStop("turn_duration"),
		)
		occWant(t, got, ok, 4100, "msg_a", true)
	})

	t.Run("an error as the only usage-bearing record does not cut", func(t *testing.T) {
		got, ok := occRead(t,
			occAPIError("msg_err"),
			occStop("turn_duration"),
		)
		if ok || got.Cut {
			t.Errorf("got %+v ok=%v, want ok and Cut false", got, ok)
		}
	})

	t.Run("after an error-only first turn the next turn's Stop cuts", func(t *testing.T) {
		got, ok := occRead(t,
			occAPIError("msg_err"),
			occStop("turn_duration"),
			occUser,
			occAssistant("msg_b", 7000, 0, 0, 50),
			occStop("stop_hook_summary"),
			occAssistant("msg_c", 80000, 0, 0, 50),
		)
		occWant(t, got, ok, 7050, "msg_b", true)
	})

	for name, line := range map[string]string{
		"isApiErrorMessage alone": strings.Replace(occAssistant("msg_err", 99999, 0, 0, 1), `"isSidechain":false,`, `"isSidechain":false,"isApiErrorMessage":true,`, 1),
		"synthetic model alone":   strings.Replace(occAssistant("msg_err", 99999, 0, 0, 1), "claude-opus-5", "<synthetic>", 1),
	} {
		t.Run(name+" is skipped", func(t *testing.T) {
			got, ok := occRead(t,
				occAssistant("msg_a", 3000, 0, 0, 0),
				line,
				occStop("turn_duration"),
			)
			occWant(t, got, ok, 3000, "msg_a", true)
		})
	}
}

func TestTranscriptPeakOccupancy_MalformedOrEmptyNotOK(t *testing.T) {
	for name, lines := range map[string][]string{
		"empty":                       nil,
		"undecodable lines":           {"{not json", `{"type":`, "garbage"},
		"undecodable lines and Stop":  {"{not json", occStop("stop_hook_summary")},
		"user records and Stop":       {occUser, occStop("turn_duration")},
		"zero usage then Stop":        {occAssistant("msg_zero", 0, 0, 0, 0), occStop("stop_hook_summary")},
		"usage without a message id":  {strings.Replace(occAssistant("msg_x", 500, 0, 0, 5), `"id":"msg_x",`, "", 1), occStop("turn_duration")},
		"only a side-chain and Stops": {occSidechain("msg_s", 700), occSidechainStop()},
	} {
		t.Run(name, func(t *testing.T) {
			got, ok := occRead(t, lines...)
			if ok || got.Cut || got.Tokens != 0 {
				t.Errorf("got %+v ok=%v, want ok false, Cut false and no figure", got, ok)
			}
		})
	}

	t.Run("a zero-usage first turn does not cut, the next turn's Stop does", func(t *testing.T) {
		got, ok := occRead(t,
			occAssistant("msg_zero", 0, 0, 0, 0),
			occStop("stop_hook_summary"),
			occUser,
			occAssistant("msg_b", 6000, 0, 0, 6),
			occStop("stop_hook_summary"),
		)
		occWant(t, got, ok, 6006, "msg_b", true)
	})

	t.Run("an unterminated last line is not read", func(t *testing.T) {
		got, ok := TranscriptPeakOccupancy(strings.NewReader(occAssistant("msg_a", 500, 0, 0, 5) + "\n" + occStop("turn_duration")))
		if !ok || got.Cut {
			t.Errorf("got %+v ok=%v, want the figure without a cut: the Stop line has no terminator", got, ok)
		}
	})
}

func occFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "transcript", "recorded-real", name))
	if err != nil {
		t.Fatalf("the committed recorded-real fixture %s is missing: %v", name, err)
	}
	return data
}

func TestTranscriptPeakOccupancy_MainExcerptFirstStop(t *testing.T) {
	data := occFixture(t, "main_excerpt.jsonl")
	got, ok := TranscriptPeakOccupancy(bytes.NewReader(data))
	if !ok || !got.Cut || got.Tokens != 78984 {
		t.Errorf("main_excerpt.jsonl = %+v ok=%v, want 78984 with Cut true", got, ok)
	}

	var kept bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 1<<20), 1<<24)
	for sc.Scan() {
		if line := sc.Text(); !strings.Contains(line, `"subtype":"stop_hook_summary"`) && !strings.Contains(line, `"subtype":"turn_duration"`) {
			kept.WriteString(line + "\n")
		}
	}
	uncut, ok := TranscriptPeakOccupancy(&kept)
	if !ok || uncut.Cut || uncut.Tokens != 93341 {
		t.Errorf("main_excerpt.jsonl without its Stop records = %+v ok=%v, want 93341 with Cut false: the cut is what makes 78984", uncut, ok)
	}
}

func TestValidSessionID_IsTheSanitizeGrammar(t *testing.T) {
	for id, want := range map[string]bool{
		"5f0c2a9e-3b1d-4c7e-9a8f-1e2d3c4b5a69": true,
		"sess_A-1":                             true,
		strings.Repeat("a", 128):               true,
		"":                                     false,
		"../x":                                 false,
		"has space":                            false,
		"ctl\x01byte":                          false,
		"ctl\nbyte":                            false,
		strings.Repeat("a", 129):               false,
	} {
		if got := ValidSessionID(id); got != want {
			t.Errorf("ValidSessionID(%q) = %v, want %v", id, got, want)
		}
	}
}

func TestStartupCost_SourceSession(t *testing.T) {
	data := occFixture(t, "source_754_first_stop.jsonl")
	got, ok := TranscriptPeakOccupancy(bytes.NewReader(data))
	if !ok || !got.Cut {
		t.Fatalf("source_754_first_stop.jsonl = %+v ok=%v, want a figure cut at its first Stop", got, ok)
	}
	if got.Tokens != sourceSessionFirstStopOccupancy {
		t.Errorf("source session e51b5585 first-Stop occupancy = %d, want %d (the analyst's end-of-startup figure)",
			got.Tokens, sourceSessionFirstStopOccupancy)
	}
}
