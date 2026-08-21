package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/transcript"
)

// Record builders for `af turn evidence` fixtures.
//
// The key sets reproduce the live claude-2.1.224 field census that
// internal/transcript/evidence_test.go:13-23 anchors to
// .designs/562/verification-report.md:115 — they are not invented from the issue text. They are
// re-declared here rather than reused because Go never compiles a _test.go file into an importable
// package: internal/transcript's builders are unreachable from this package under any naming.
//
// D-10 (design-doc.md:166) forbids MINTING files into internal/transcript/testdata/recorded-real/ —
// fixtures there are captured, not authored. It does not reach synthetic JSONL written to
// t.TempDir(), which is what Phase 2's own suite uses throughout.
const (
	turnTestSession = "sess-562"
	turnTestCWD     = "/repo"
	turnTestTS      = "2026-08-09T10:00:00Z"
)

func turnToolUse(id, name, inputJSON string) string {
	return fmt.Sprintf(`{"type":"tool_use","id":%q,"name":%q,"input":%s,"caller":"direct"}`, id, name, inputJSON)
}

func turnToolResult(toolUseID, content string, isError bool) string {
	return fmt.Sprintf(`{"tool_use_id":%q,"type":"tool_result","content":%q,"is_error":%t}`, toolUseID, content, isError)
}

func turnTextBlock(s string) string { return fmt.Sprintf(`{"type":"text","text":%q}`, s) }

func turnAssistantRec(uuid, msgID string, sidechain bool, block string) string {
	return fmt.Sprintf(`{"parentUuid":null,"isSidechain":%t,"message":{"id":%q,"role":"assistant","content":[%s]},`+
		`"requestId":"req_1","type":"assistant","uuid":%q,"timestamp":%q,"sessionId":%q,"cwd":%q}`,
		sidechain, msgID, block, uuid, turnTestTS, turnTestSession, turnTestCWD)
}

func turnUserRec(uuid string, sidechain bool, contentJSON string) string {
	return fmt.Sprintf(`{"parentUuid":null,"isSidechain":%t,"message":{"role":"user","content":%s},`+
		`"promptId":"p_1","type":"user","uuid":%q,"timestamp":%q,"sessionId":%q,"cwd":%q}`,
		sidechain, contentJSON, uuid, turnTestTS, turnTestSession, turnTestCWD)
}

// turnPrompt is a PLAIN user record — array content with no tool_result block. It opens a turn.
func turnPrompt(uuid, text string) string {
	return turnUserRec(uuid, false, "["+turnTextBlock(text)+"]")
}

func turnCall(uuid, msgID, toolUseID, name, inputJSON string) string {
	return turnAssistantRec(uuid, msgID, false, turnToolUse(toolUseID, name, inputJSON))
}

func turnSidechainCall(uuid, msgID, toolUseID, name, inputJSON string) string {
	return turnAssistantRec(uuid, msgID, true, turnToolUse(toolUseID, name, inputJSON))
}

// turnResult is a user record carrying a tool_result block: NOT plain, so it must not open a turn.
func turnResult(uuid, toolUseID, content string, isError bool) string {
	return turnUserRec(uuid, false, "["+turnToolResult(toolUseID, content, isError)+"]")
}

// turnFixture is the single source of truth for BOTH the text goldens and the JSON assertions. One
// list, so a fixture added for a golden is automatically schema-asserted — a second hard-coded list
// is the defect telemetry_goldens_test.go:371-377 names.
type turnFixture struct {
	name          string // -> internal/cmd/testdata/turn_<name>.golden
	lines         []string
	args          []string
	wantBoundary  bool
	wantTotal     int
	wantShown     int
	wantComplete  bool
	wantUnmatched int
	wantSidechain int
	wantSeqs      []int
	wantTools     []string
}

func turnFixtures() []turnFixture {
	var manyCalls []string
	manyCalls = append(manyCalls, turnPrompt("u1", "run the steps"))
	for i := 1; i <= 8; i++ {
		manyCalls = append(manyCalls,
			turnCall(fmt.Sprintf("a%d", i), fmt.Sprintf("m%d", i), fmt.Sprintf("t%d", i), "Bash",
				fmt.Sprintf(`{"command":"step-%d"}`, i)),
			turnResult(fmt.Sprintf("r%d", i), fmt.Sprintf("t%d", i), fmt.Sprintf("out-%d", i), false),
		)
	}

	return []turnFixture{
		{
			name: "complete",
			lines: []string{
				turnCall("a0", "m0", "t0", "Bash", `{"command":"previous turn"}`),
				turnPrompt("u1", "do the thing"),
				`{"type":"system","subtype":"turn_duration"}`,
				``,
				turnCall("a1", "m1", "t1", "Bash", `{"command":"af mail inbox"}`),
				turnResult("r1", "t1", "no new mail", false),
				turnCall("a2", "m2", "t2", "Read", `{"file_path":"/x/y.md"}`),
				turnResult("r2", "t2", "...", false),
				turnCall("a3", "m3", "t3", "Write", `{"file_path":"/x/z.md"}`),
			},
			wantBoundary: true, wantTotal: 3, wantShown: 3, wantComplete: true,
			wantSeqs: []int{1, 2, 3}, wantTools: []string{"Bash", "Read", "Write"},
		},
		{
			// A tool-free turn preceded by a turn that DID use tools: the AC-1 scoping property,
			// asserted at the CLI boundary rather than only in the derivation's own suite.
			name: "empty",
			lines: []string{
				turnCall("a0", "m0", "t0", "Bash", `{"command":"previous turn"}`),
				turnResult("r0", "t0", "previous output", false),
				turnPrompt("u1", "just think about it"),
				turnAssistantRec("a9", "m9", false, turnTextBlock("I thought about it.")),
			},
			wantBoundary: true, wantTotal: 0, wantShown: 0, wantComplete: true,
			wantSeqs: []int{}, wantTools: []string{},
		},
		{
			// A readable transcript with no turn boundary at all. An absent boundary yields no
			// evidence rather than a best-effort file-wide dump (evidence.go:101-103).
			name: "unavailable",
			lines: []string{
				turnCall("a1", "m1", "t1", "Bash", `{"command":"orphan"}`),
				turnResult("r1", "t1", "orphan output", false),
			},
			wantBoundary: false, wantTotal: 0, wantShown: 0, wantComplete: false,
			wantSeqs: []int{}, wantTools: []string{},
		},
		{
			name:         "partial",
			lines:        manyCalls,
			args:         []string{"--max-calls", "5"},
			wantBoundary: true, wantTotal: 8, wantShown: 5, wantComplete: false,
			wantSeqs:  []int{1, 2, 6, 7, 8},
			wantTools: []string{"Bash", "Bash", "Bash", "Bash", "Bash"},
		},
		{
			// Degraded WHILE STILL SHOWING CALLS: Marker() returns "" for this state, so a renderer
			// that prints only Marker() ships a clean-looking block over incomplete evidence — the
			// failure internal/transcript/evidence.go:20-23 warns about by name.
			name: "degraded",
			lines: []string{
				turnPrompt("u1", "go"),
				turnCall("a1", "m1", "t1", "Bash", `{"command":"af mail inbox"}`),
				turnResult("r1", "t1", "no new mail", false),
				turnCall("a2", "m2", "t2", "Read", `{"file_path":"/x/y.md"}`),
				turnResult("r2", "t2", "file not found", true),
				turnResult("r3", "t-ghost", "stray", false),
			},
			wantBoundary: true, wantTotal: 2, wantShown: 2, wantComplete: false, wantUnmatched: 1,
			wantSeqs: []int{1, 2}, wantTools: []string{"Bash", "Read"},
		},
		{
			// A spawned sub-agent's interleaved thread is excluded from the dispatching turn, and the
			// exclusion is COUNTED rather than silent.
			name: "sidechain",
			lines: []string{
				turnPrompt("u1", "go"),
				turnCall("a1", "m1", "t1", "Bash", `{"command":"af mail inbox"}`),
				turnResult("r1", "t1", "no new mail", false),
				turnSidechainCall("s1", "ms1", "ts1", "Grep", `{"pattern":"x"}`),
				turnCall("a2", "m2", "t2", "Read", `{"file_path":"/x/y.md"}`),
				turnResult("r2", "t2", "...", false),
			},
			wantBoundary: true, wantTotal: 2, wantShown: 2, wantComplete: true, wantSidechain: 1,
			wantSeqs: []int{1, 2}, wantTools: []string{"Bash", "Read"},
		},
	}
}

// writeTurnFixture materialises a fixture into a temp transcript and returns its path. It reuses
// writeTranscript (statusline_tokens_test.go:25) — redeclaring that helper would break every test
// file in the package, not just this one.
func writeTurnFixture(t *testing.T, f turnFixture) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "transcript.jsonl")
	writeTranscript(t, path, f.lines...)
	return path
}

// resetTurnEvidenceFlags restores every `turn evidence` flag to its default and clears Changed.
//
// pflag state lives on the package-level evidenceCmd, so it survives every invocation regardless of
// how the command was driven (mail_test.go:154-157 documents the same leak for `mail send`). A
// leaked --max-calls would silently change a sibling subtest's calls_shown, which reads as a
// renderer bug rather than as contamination. It doubles as the registration probe: a flag this
// phase failed to declare fails here by name.
func resetTurnEvidenceFlags(t *testing.T) {
	t.Helper()
	cmd, _, err := rootCmd.Find([]string{"turn", "evidence"})
	if err != nil {
		t.Fatalf("finding `turn evidence` on the cobra root: %v", err)
	}
	for _, name := range []string{"transcript", "format", "max-calls", "max-bytes"} {
		f := cmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag --%s is not registered on `turn evidence`", name)
		}
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatalf("resetting --%s: %v", name, err)
		}
		f.Changed = false
	}
}

// execTurnEvidence drives `af turn evidence` through the real cobra root, so the assertions cover
// the registration and flag parsing a direct RunE call would skip.
//
// The writers are restored to nil (cobra's "use os.Stdout/os.Stderr" default) rather than left
// dangling: telemetry_json.go:368-374 records that OutOrStdout resolves to the ROOT command's writer
// when a leaf has none, and that sibling tests in this package redirect it and never restore it.
func execTurnEvidence(t *testing.T, args ...string) (string, string, error) {
	t.Helper()
	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs(append([]string{"turn", "evidence"}, args...))
	err := rootCmd.Execute()
	resetTurnEvidenceFlags(t)
	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)
	rootCmd.SetArgs(nil)
	return out.String(), errOut.String(), err
}

func runTurnFixture(t *testing.T, f turnFixture, extra ...string) (string, error) {
	t.Helper()
	args := append([]string{"--transcript", writeTurnFixture(t, f)}, f.args...)
	out, _, err := execTurnEvidence(t, append(args, extra...)...)
	return out, err
}

func turnGoldenPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "internal", "cmd", "testdata", "turn_"+name+".golden")
}

func readTurnGolden(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(turnGoldenPath(t, name))
	if err != nil {
		t.Fatalf("read golden turn_%s.golden: %v", name, err)
	}
	return string(b)
}

// turnJSONObject decodes one JSON object into its key/raw-value map.
//
// It never decodes into transcript.Evidence. Decoding both sides of a comparison into the same
// struct is the trap telemetry_goldens_test.go:37-40 names: a renamed json tag leaves the field
// zero-valued on BOTH sides, so the assertion passes while asserting nothing.
func turnJSONObject(t *testing.T, raw []byte, what string) map[string]json.RawMessage {
	t.Helper()
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("%s does not parse as a JSON object: %v\nraw: %s", what, err, raw)
	}
	return m
}

func assertTurnKeySet(t *testing.T, got map[string]json.RawMessage, want []string, what string) {
	t.Helper()
	gotKeys := keysOf(got)
	sort.Strings(gotKeys)
	wantSorted := append([]string(nil), want...)
	sort.Strings(wantSorted)
	if strings.Join(gotKeys, ",") != strings.Join(wantSorted, ",") {
		t.Fatalf("%s key set = %v, want %v", what, gotKeys, wantSorted)
	}
}

func turnJSONInt(t *testing.T, m map[string]json.RawMessage, key, what string) int {
	t.Helper()
	var n int
	if err := json.Unmarshal(m[key], &n); err != nil {
		t.Fatalf("%s.%s is not a number: %v", what, key, err)
	}
	return n
}

func turnJSONBool(t *testing.T, m map[string]json.RawMessage, key, what string) bool {
	t.Helper()
	var b bool
	if err := json.Unmarshal(m[key], &b); err != nil {
		t.Fatalf("%s.%s is not a bool: %v", what, key, err)
	}
	return b
}

func turnJSONString(t *testing.T, m map[string]json.RawMessage, key, what string) string {
	t.Helper()
	var s string
	if err := json.Unmarshal(m[key], &s); err != nil {
		t.Fatalf("%s.%s is not a string: %v", what, key, err)
	}
	return s
}

// turnCallObjects decodes the "calls" array element-wise, keeping each element as a key/raw map so
// per-element key sets stay assertable (omitempty on Call.Result is a real contract detail).
func turnCallObjects(t *testing.T, raw json.RawMessage) []map[string]json.RawMessage {
	t.Helper()
	var arr []map[string]json.RawMessage
	if err := json.Unmarshal(raw, &arr); err != nil {
		t.Fatalf("calls is not an array of objects: %v\nraw: %s", err, raw)
	}
	return arr
}

// turnMarkerLine is the bracketed scope line of a text render, or "" when the render makes no
// partial claim. The markers are the only part of the block a judge is instructed to trust
// unconditionally, so tests read them back rather than re-deriving what they should say.
func turnMarkerLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "[showing first ") {
			return line
		}
	}
	return ""
}

// turnTextCallSeqs is the window a TEXT render shows, read back off the numbered call lines. The two
// formats are fitted to --max-bytes independently — json is the bulkier encoding, so the same budget
// buys it fewer calls — which is why a text marker may only ever be checked against text call lines.
func turnTextCallSeqs(t *testing.T, out string) []int {
	t.Helper()
	var seqs []int
	for _, line := range strings.Split(out, "\n") {
		var seq int
		var rest string
		if n, _ := fmt.Sscanf(line, "%d. %s", &seq, &rest); n == 2 {
			seqs = append(seqs, seq)
		}
	}
	return seqs
}

// turnCallSeqs is the window a render actually shows, in order. The head+tail shape is the property
// design-doc.md:101 cares about — the first calls prove sequencing ("Read X before editing it") so a
// judge can PASS affirmatively instead of abstaining — and only a seq list can observe it.
func turnCallSeqs(t *testing.T, out string) []int {
	t.Helper()
	doc := turnJSONObject(t, []byte(out), "evidence document")
	var seqs []int
	for i, c := range turnCallObjects(t, doc["calls"]) {
		seqs = append(seqs, turnJSONInt(t, c, "seq", fmt.Sprintf("calls[%d]", i)))
	}
	return seqs
}

func TestTurnEvidence(t *testing.T) {
	fixtures := turnFixtures()

	t.Run("Registration", func(t *testing.T) {
		cmd, _, err := rootCmd.Find([]string{"turn", "evidence"})
		if err != nil {
			t.Fatalf("`af turn evidence` is not registered on the cobra root: %v", err)
		}
		if cmd.Name() != "evidence" {
			t.Fatalf("resolved command = %q, want %q", cmd.Name(), "evidence")
		}
		if cmd.Parent() == nil || cmd.Parent().Name() != "turn" {
			t.Fatalf("parent = %v, want a command named %q", cmd.Parent(), "turn")
		}
		if cmd.RunE == nil {
			t.Fatal("`turn evidence` has no RunE")
		}

		// The help contract (api.md:56, step.go:21-23 precedent): the text states WHICH consumers
		// parse this output, so a reader of `--help` learns the contract is hook-facing.
		for _, want := range []string{"fidelity-gate", "quality-gate", "hook"} {
			if !strings.Contains(strings.ToLower(cmd.Long), want) {
				t.Errorf("Long help does not name %q:\n%s", want, cmd.Long)
			}
		}
		// ADR-014 clause 4: no interactive prompting text in command help.
		for _, banned := range []string{"(y/N)", "[y/N]", "Proceed?", "Continue?"} {
			if strings.Contains(cmd.Long, banned) {
				t.Errorf("Long help contains interactive prompt text %q (ADR-014 clause 4)", banned)
			}
		}

		// Defaults pinned by design-doc.md:100 so the Phase-6 format-contract test has a fixed target.
		for _, tc := range []struct{ flag, want string }{
			{"transcript", ""},
			{"format", "text"},
			{"max-calls", fmt.Sprint(transcript.DefaultOptions().MaxCalls)},
			{"max-bytes", "16384"},
		} {
			f := cmd.Flags().Lookup(tc.flag)
			if f == nil {
				t.Errorf("flag --%s is not registered", tc.flag)
				continue
			}
			if f.DefValue != tc.want {
				t.Errorf("--%s default = %q, want %q", tc.flag, f.DefValue, tc.want)
			}
		}
		if got := fmt.Sprint(transcript.DefaultOptions().MaxCalls); got != "20" {
			t.Errorf("transcript.DefaultOptions().MaxCalls = %s, want 20 (design-doc.md:100)", got)
		}
	})

	t.Run("Text", func(t *testing.T) {
		for _, f := range fixtures {
			t.Run(f.name, func(t *testing.T) {
				out, err := runTurnFixture(t, f)
				if err != nil {
					t.Fatalf("RunE returned %v, want nil (ADR-007: the gate is never blocked)", err)
				}
				if want := readTurnGolden(t, f.name); out != want {
					t.Fatalf("rendered text does not match testdata/turn_%s.golden\n--- got ---\n%s\n--- want ---\n%s",
						f.name, out, want)
				}
			})
		}
	})

	t.Run("JSON", func(t *testing.T) {
		for _, f := range fixtures {
			t.Run(f.name, func(t *testing.T) {
				out, err := runTurnFixture(t, f, "--format", "json")
				if err != nil {
					t.Fatalf("RunE returned %v, want nil", err)
				}
				if !strings.HasSuffix(out, "\n") {
					t.Errorf("json output is not newline-terminated (jq/read consumers expect a line)")
				}

				doc := turnJSONObject(t, []byte(out), "evidence document")
				assertTurnKeySet(t, doc, []string{"turn", "calls"}, "evidence document")

				turn := turnJSONObject(t, doc["turn"], "turn")
				assertTurnKeySet(t, turn, []string{
					"boundary_uuid", "boundary_ts", "complete", "calls_total",
					"calls_shown", "sidechain_excluded", "unmatched_results",
				}, "turn")

				if got := turnJSONInt(t, turn, "calls_total", "turn"); got != f.wantTotal {
					t.Errorf("calls_total = %d, want %d", got, f.wantTotal)
				}
				if got := turnJSONInt(t, turn, "calls_shown", "turn"); got != f.wantShown {
					t.Errorf("calls_shown = %d, want %d", got, f.wantShown)
				}
				if got := turnJSONBool(t, turn, "complete", "turn"); got != f.wantComplete {
					t.Errorf("complete = %t, want %t", got, f.wantComplete)
				}
				if got := turnJSONInt(t, turn, "unmatched_results", "turn"); got != f.wantUnmatched {
					t.Errorf("unmatched_results = %d, want %d", got, f.wantUnmatched)
				}
				if got := turnJSONInt(t, turn, "sidechain_excluded", "turn"); got != f.wantSidechain {
					t.Errorf("sidechain_excluded = %d, want %d", got, f.wantSidechain)
				}
				if got := turnJSONString(t, turn, "boundary_uuid", "turn"); (got != "") != f.wantBoundary {
					t.Errorf("boundary_uuid = %q, want non-empty=%t", got, f.wantBoundary)
				}

				// `calls` must be an ARRAY in every state, including the empty and unavailable ones.
				// Evidence.Calls is a nil slice there, and a bare `null` makes `jq '.calls[]'` fail
				// with exit 5 — so an empty turn would reach the Phase-6 hook as a broken command
				// rather than as the authoritative "no tools were used" it is.
				if got := strings.TrimSpace(string(doc["calls"])); got == "null" {
					t.Errorf("calls = null, want an array (design-doc.md:108 pins calls as a list)")
				}

				calls := turnCallObjects(t, doc["calls"])
				if len(calls) != len(f.wantSeqs) {
					t.Fatalf("len(calls) = %d, want %d", len(calls), len(f.wantSeqs))
				}
				for i, c := range calls {
					for _, required := range []string{"seq", "tool", "input"} {
						if _, ok := c[required]; !ok {
							t.Errorf("calls[%d] is missing required key %q", i, required)
						}
					}
					for k := range c {
						switch k {
						case "seq", "tool", "input", "result":
						default:
							t.Errorf("calls[%d] carries unexpected key %q", i, k)
						}
					}
					if got := turnJSONInt(t, c, "seq", fmt.Sprintf("calls[%d]", i)); got != f.wantSeqs[i] {
						t.Errorf("calls[%d].seq = %d, want %d", i, got, f.wantSeqs[i])
					}
					if got := turnJSONString(t, c, "tool", fmt.Sprintf("calls[%d]", i)); got != f.wantTools[i] {
						t.Errorf("calls[%d].tool = %q, want %q", i, got, f.wantTools[i])
					}
				}
			})
		}
	})

	// Call.Result is omitempty (evidence.go:126). Exercising only one direction would let the tag be
	// dropped or added without any test noticing — the same trap step_test.go:157-160 documents for
	// the `is_gate` field.
	t.Run("JSON/OmitemptyResultBothDirections", func(t *testing.T) {
		var complete turnFixture
		for _, f := range fixtures {
			if f.name == "complete" {
				complete = f
			}
		}
		out, err := runTurnFixture(t, complete, "--format", "json")
		if err != nil {
			t.Fatalf("RunE returned %v, want nil", err)
		}
		calls := turnCallObjects(t, turnJSONObject(t, []byte(out), "evidence document")["calls"])
		if len(calls) != 3 {
			t.Fatalf("len(calls) = %d, want 3", len(calls))
		}
		if _, ok := calls[0]["result"]; !ok {
			t.Error("calls[0] has no \"result\" key, but its call was answered")
		}
		if _, ok := calls[2]["result"]; ok {
			t.Error("calls[2] carries a \"result\" key, but no result was recorded for it — nothing may be invented")
		}
	})

	// The three frozen strings (design-doc.md:100-102). Each is asserted against BOTH the exported
	// constant and its literal text: the constant alone lets a package-side rename move the contract
	// silently, and the literal alone lets the two drift apart.
	t.Run("FrozenMarkers", func(t *testing.T) {
		if transcript.MarkerNoCalls != "No tool calls were made in this turn." {
			t.Errorf("MarkerNoCalls = %q, want the frozen empty-turn text", transcript.MarkerNoCalls)
		}
		if transcript.MarkerUnavailable != "[tool evidence unavailable this turn]" {
			t.Errorf("MarkerUnavailable = %q, want the frozen unavailable text", transcript.MarkerUnavailable)
		}
		if transcript.MarkerNoCalls == transcript.MarkerUnavailable {
			t.Error("the empty-turn and unavailable markers must be distinguishable (api.md:116): " +
				"the judge grades them under different rules")
		}
		if got, want := readTurnGolden(t, "empty"), transcript.MarkerNoCalls+"\n"; got != want {
			t.Errorf("empty-turn render = %q, want %q", got, want)
		}
		if got, want := readTurnGolden(t, "unavailable"), transcript.MarkerUnavailable+"\n"; got != want {
			t.Errorf("unavailable render = %q, want %q", got, want)
		}

		// markerPartialFormat is UNEXPORTED (evidence.go:42), so the partial marker can only be
		// pinned through Marker() — a re-spelled literal in turn.go would pass every AC in this
		// phase and break Phase 6's format-contract test instead.
		var partial turnFixture
		for _, f := range fixtures {
			if f.name == "partial" {
				partial = f
			}
		}
		opts := transcript.DefaultOptions()
		opts.MaxCalls = 5
		want := transcript.DeriveFile(writeTurnFixture(t, partial), opts).Marker()
		if want == "" {
			t.Fatal("the partial fixture did not produce a truncation marker — the fixture is wrong")
		}
		if !strings.Contains(want, "EVIDENCE IS PARTIAL") {
			t.Errorf("Marker() = %q, want it to carry the pinned fragment %q", want, "EVIDENCE IS PARTIAL")
		}
		if golden := readTurnGolden(t, "partial"); !strings.Contains(golden, want) {
			t.Errorf("partial render does not carry Marker()'s exact text\nmarker: %q\nrender:\n%s", want, golden)
		}
	})

	// Marker() returns "" for a turn that is degraded while still showing calls, so a renderer that
	// prints only Marker() hides the degradation (evidence.go:20-23). The disclosure must carry
	// Turn.Complete and Turn.UnmatchedResults, alongside the marker and never instead of it.
	t.Run("DegradationIsDisclosed", func(t *testing.T) {
		var degraded turnFixture
		for _, f := range fixtures {
			if f.name == "degraded" {
				degraded = f
			}
		}
		path := writeTurnFixture(t, degraded)
		ev := transcript.DeriveFile(path, transcript.DefaultOptions())
		if ev.Marker() != "" {
			t.Fatalf("fixture precondition broken: Marker() = %q, want \"\" (degraded WITH calls shown)", ev.Marker())
		}
		if ev.Turn.Complete || ev.Turn.UnmatchedResults != 1 {
			t.Fatalf("fixture precondition broken: complete=%t unmatched=%d, want false/1",
				ev.Turn.Complete, ev.Turn.UnmatchedResults)
		}

		out, _, err := execTurnEvidence(t, "--transcript", path)
		if err != nil {
			t.Fatalf("RunE returned %v, want nil", err)
		}
		for _, want := range []string{"complete=false", "unmatched_results=1"} {
			if !strings.Contains(out, want) {
				t.Errorf("degraded render does not disclose %q:\n%s", want, out)
			}
		}
		for _, want := range []string{`1. Bash(command="af mail inbox")`, `2. Read(file_path="/x/y.md")`} {
			if !strings.Contains(out, want) {
				t.Errorf("degraded render dropped the calls it did derive (%q):\n%s", want, out)
			}
		}
		if !strings.Contains(out, `-> result[error]: "file not found"`) {
			t.Errorf("an errored result is not rendered as result[error]:\n%s", out)
		}
	})

	// A result longer than the pinned 300-char cap must SAY it was cut. Without the annotation the
	// judge reads a truncated string as the tool's whole output.
	t.Run("ResultTruncationIsAnnotated", func(t *testing.T) {
		long := strings.Repeat("x", 400)
		f := turnFixture{lines: []string{
			turnPrompt("u1", "go"),
			turnCall("a1", "m1", "t1", "Bash", `{"command":"noisy"}`),
			turnResult("r1", "t1", long, false),
		}}
		out, err := runTurnFixture(t, f)
		if err != nil {
			t.Fatalf("RunE returned %v, want nil", err)
		}
		if !strings.Contains(out, "(truncated at 300 chars)") {
			t.Fatalf("truncated result is not annotated:\n%s", out)
		}
		if strings.Contains(out, long) {
			t.Error("the untruncated 400-char result reached the output")
		}
		if n := strings.Count(out, "x"); n != 300 {
			t.Errorf("rendered result content is %d chars, want the pinned 300 (design-doc.md:100)", n)
		}

		// The cap counts CHARACTERS, not bytes (evidence.go:467-471 slices runes). A byte-counting
		// annotation would tell the judge a 300-rune multibyte result was cut at 600 — an ASCII-only
		// fixture cannot tell the two apart, so one multibyte result is asserted on its own.
		multibyte := strings.Repeat("é", 400)
		out, err = runTurnFixture(t, turnFixture{lines: []string{
			turnPrompt("u1", "go"),
			turnCall("a1", "m1", "t1", "Bash", `{"command":"noisy"}`),
			turnResult("r1", "t1", multibyte, false),
		}})
		if err != nil {
			t.Fatalf("RunE returned %v, want nil", err)
		}
		if !strings.Contains(out, "(truncated at 300 chars)") {
			t.Fatalf("a 400-rune multibyte result is annotated with a byte count, not a char count:\n%s", out)
		}
		if n := strings.Count(out, "é"); n != 300 {
			t.Errorf("rendered multibyte content is %d runes, want the pinned 300", n)
		}
	})

	// Call.Input is a map and Go randomises map iteration, so an unsorted renderer emits different
	// bytes for the same transcript from one run to the next. Every other fixture here carries a
	// single-key input, which cannot detect that — so the ordering is pinned on its own.
	t.Run("InputFieldsAreSortedForReproducibility", func(t *testing.T) {
		f := turnFixture{lines: []string{
			turnPrompt("u1", "go"),
			turnCall("a1", "m1", "t1", "Edit", `{"replace_all":"true","file_path":"/x/y.md","old_string":"a"}`),
			turnResult("r1", "t1", "done", false),
		}}
		const want = `1. Edit(file_path="/x/y.md", old_string="a", replace_all="true")`
		for i := range 8 {
			out, err := runTurnFixture(t, f)
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			if !strings.Contains(out, want) {
				t.Fatalf("run %d rendered input fields out of sorted order\nwant line: %s\ngot:\n%s", i, want, out)
			}
		}
	})

	// ADR-007: a Stop hook is never blocked by gate infrastructure. root.go:29-39 maps ANY non-nil
	// RunE error to exit 1, so "exit 0 best-effort" is literally "RunE returns nil" — and "exit 0"
	// alone would be vacuous if stdout were empty, hence the output assertion.
	t.Run("FailOpen", func(t *testing.T) {
		dir := t.TempDir()

		garbage := filepath.Join(dir, "garbage.jsonl")
		writeTranscript(t, garbage, `{not json at all`, `also not json`, `{"dangling":`)

		emptyFile := filepath.Join(dir, "empty.jsonl")
		if err := os.WriteFile(emptyFile, nil, 0o644); err != nil {
			t.Fatalf("writing empty file: %v", err)
		}

		cases := []struct {
			name string
			path string
		}{
			{"MissingTranscript", filepath.Join(dir, "does-not-exist.jsonl")},
			{"MalformedTranscript", garbage},
			{"EmptyFile", emptyFile},
			{"DirectoryAsPath", dir},
			{"EmptyTranscriptFlag", ""},
		}
		for _, tc := range cases {
			for _, format := range []string{"text", "json"} {
				t.Run(tc.name+"/"+format, func(t *testing.T) {
					out, _, err := execTurnEvidence(t, "--transcript", tc.path, "--format", format)
					if err != nil {
						t.Fatalf("RunE returned %v, want nil so the process exits 0 (ADR-007)", err)
					}
					if out == "" {
						t.Fatal("stdout is empty: the contract is exit 0 WITH best-effort output")
					}
					if format == "text" {
						if want := transcript.MarkerUnavailable + "\n"; out != want {
							t.Errorf("text output = %q, want %q", out, want)
						}
						return
					}
					turn := turnJSONObject(t, turnJSONObject(t, []byte(out), "evidence document")["turn"], "turn")
					if got := turnJSONString(t, turn, "boundary_uuid", "turn"); got != "" {
						t.Errorf("boundary_uuid = %q, want \"\" — nothing may be guessed when the turn is unknown", got)
					}
				})
			}
		}
	})

	// api.md:39 draws the one line the outline's ACs do not test: "non-zero only on hard usage
	// errors, which the hook treats as 'evidence unavailable' and fails open". A bad --format is a
	// usage error, not malformed input, so it is the one path that does NOT return nil. Pinned here
	// so the Phase-6 hook rewrite meets no surprise.
	t.Run("UnknownFormatIsAUsageError", func(t *testing.T) {
		f := turnFixtures()[0]
		out, _, err := execTurnEvidence(t, "--transcript", writeTurnFixture(t, f), "--format", "xml")
		if err == nil {
			t.Fatal("an unknown --format returned nil, so the process would exit 0 on a usage error")
		}
		for _, want := range []string{"xml", "text", "json"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
		if strings.Contains(out, "Tool activity") {
			t.Errorf("an evidence block was written despite the usage error:\n%s", out)
		}
	})

	t.Run("Flags", func(t *testing.T) {
		// 25 calls: more than the pinned cap of 20, so the default window is observable.
		var wide turnFixture
		wide.lines = append(wide.lines, turnPrompt("u1", "run everything"))
		for i := 1; i <= 25; i++ {
			wide.lines = append(wide.lines,
				turnCall(fmt.Sprintf("a%d", i), fmt.Sprintf("m%d", i), fmt.Sprintf("t%d", i), "Bash",
					fmt.Sprintf(`{"command":"step-%02d"}`, i)),
				turnResult(fmt.Sprintf("r%d", i), fmt.Sprintf("t%d", i), fmt.Sprintf("out-%02d", i), false))
		}

		t.Run("MaxCallsIsWired", func(t *testing.T) {
			out, err := runTurnFixture(t, wide, "--format", "json", "--max-calls", "3")
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			turn := turnJSONObject(t, turnJSONObject(t, []byte(out), "doc")["turn"], "turn")
			if got := turnJSONInt(t, turn, "calls_shown", "turn"); got != 3 {
				t.Errorf("calls_shown with --max-calls 3 = %d, want 3 (the flag is not wired to Options)", got)
			}
			if got := turnJSONInt(t, turn, "calls_total", "turn"); got != 25 {
				t.Errorf("calls_total = %d, want 25", got)
			}
			if turnJSONBool(t, turn, "complete", "turn") {
				t.Error("complete = true after dropping 22 calls")
			}
		})

		// design-doc.md:100-101 — cap 20, head+tail window of first 5 + most recent 15, so the
		// sequencing proof ("Read X before editing it") survives truncation.
		t.Run("PinnedDefaults", func(t *testing.T) {
			out, err := runTurnFixture(t, wide, "--format", "json")
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			doc := turnJSONObject(t, []byte(out), "doc")
			turn := turnJSONObject(t, doc["turn"], "turn")
			if got := turnJSONInt(t, turn, "calls_shown", "turn"); got != 20 {
				t.Fatalf("calls_shown = %d, want the pinned default cap of 20", got)
			}
			calls := turnCallObjects(t, doc["calls"])
			var gotSeqs []int
			for i, c := range calls {
				gotSeqs = append(gotSeqs, turnJSONInt(t, c, "seq", fmt.Sprintf("calls[%d]", i)))
			}
			want := []int{1, 2, 3, 4, 5, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25}
			if fmt.Sprint(gotSeqs) != fmt.Sprint(want) {
				t.Errorf("window = %v, want first 5 + last 15 %v", gotSeqs, want)
			}
		})

		// design-doc.md:100 pins --max-bytes at 16KiB but specifies no trimming algorithm. What is
		// asserted here is the property the flag claims: the rendered block is bounded, the drop is
		// disclosed through the EXISTING frozen marker, and json stays a parseable document.
		t.Run("MaxBytesBoundsAndDisclosesTheOutput", func(t *testing.T) {
			const budget = 600

			full, err := runTurnFixture(t, wide)
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			if len(full) <= budget {
				t.Fatalf("fixture precondition broken: unbounded render is %d bytes, not above the %d-byte budget",
					len(full), budget)
			}

			text, err := runTurnFixture(t, wide, "--max-bytes", fmt.Sprint(budget))
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			if len(text) > budget {
				t.Errorf("text render is %d bytes, above the --max-bytes budget of %d", len(text), budget)
			}
			if !strings.Contains(text, "EVIDENCE IS PARTIAL") {
				t.Errorf("bytes were dropped without disclosure:\n%s", text)
			}
			if !strings.HasPrefix(text, "Tool activity for THIS TURN ONLY") {
				t.Errorf("the judge-facing header did not survive the byte bound:\n%s", text)
			}

			asJSON, err := runTurnFixture(t, wide, "--format", "json", "--max-bytes", fmt.Sprint(budget))
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			if len(asJSON) > budget {
				t.Errorf("json render is %d bytes, above the --max-bytes budget of %d", len(asJSON), budget)
			}
			doc := turnJSONObject(t, []byte(asJSON), "byte-bounded evidence document")
			turn := turnJSONObject(t, doc["turn"], "turn")
			if got := turnJSONInt(t, turn, "calls_shown", "turn"); got >= 20 {
				t.Errorf("calls_shown = %d, want fewer than the 20 an unbounded render shows", got)
			}
			if turnJSONBool(t, turn, "complete", "turn") {
				t.Error("complete = true after the byte bound dropped calls")
			}
			if got := turnJSONInt(t, turn, "calls_total", "turn"); got != 25 {
				t.Errorf("calls_total = %d, want the whole turn's 25 — the total may not be rewritten", got)
			}

			// The byte bound must narrow the SAME head+tail window --max-calls uses. A tail-only or
			// head-only trim keeps every assertion above green while destroying the sequencing proof.
			// The budget here is deliberately looser than the one above: a window at or below the
			// 5-call head is all head, and only a wider window can show the split.
			roomy, err := runTurnFixture(t, wide, "--format", "json", "--max-bytes", "1400")
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			seqs := turnCallSeqs(t, roomy)
			if len(seqs) < 6 || len(seqs) >= 20 {
				t.Fatalf("byte-bounded window kept %v — need a partial window wider than the 5-call head", seqs)
			}
			if head := fmt.Sprint(seqs[:5]); head != fmt.Sprint([]int{1, 2, 3, 4, 5}) {
				t.Errorf("byte-bounded window starts %s, want the pinned head 1..5", head)
			}
			if last := seqs[len(seqs)-1]; last != 25 {
				t.Errorf("byte-bounded window ends at seq %d, want the turn's most recent call 25", last)
			}
		})

		// A budget below even the empty-window floor still has to produce something a judge can read:
		// ADR-007 says the hook never blocks, so the renderer degrades to header + disclosure rather
		// than emitting nothing or overshooting the bound with a call it could not afford.
		t.Run("MaxBytesBelowTheFloorStillRendersTheHeader", func(t *testing.T) {
			out, err := runTurnFixture(t, wide, "--max-bytes", "1")
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			if !strings.HasPrefix(out, "Tool activity for THIS TURN ONLY") {
				t.Errorf("the judge-facing header did not survive a 1-byte budget:\n%s", out)
			}
			if !strings.Contains(out, "EVIDENCE IS PARTIAL") {
				t.Errorf("everything was dropped without disclosure:\n%s", out)
			}
			for _, line := range strings.Split(out, "\n") {
				if line != "" && line[0] >= '1' && line[0] <= '9' {
					t.Errorf("a call line survived a 1-byte budget: %q", line)
				}
			}
		})

		// M1 guard: `wide` is already incomplete from the 20-call cap, so it cannot observe whether the
		// byte bound sets Complete=false itself. A turn under the cap starts complete, so only here does
		// dropping that assignment become visible.
		t.Run("MaxBytesMarksAnOtherwiseCompleteTurnIncomplete", func(t *testing.T) {
			var narrow turnFixture
			narrow.lines = append(narrow.lines, turnPrompt("u1", "run a few"))
			for i := 1; i <= 12; i++ {
				narrow.lines = append(narrow.lines,
					turnCall(fmt.Sprintf("a%d", i), fmt.Sprintf("m%d", i), fmt.Sprintf("t%d", i), "Bash",
						fmt.Sprintf(`{"command":"step-%02d"}`, i)),
					turnResult(fmt.Sprintf("r%d", i), fmt.Sprintf("t%d", i), fmt.Sprintf("out-%02d", i), false))
			}

			unbounded, err := runTurnFixture(t, narrow, "--format", "json")
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			turn := turnJSONObject(t, turnJSONObject(t, []byte(unbounded), "doc")["turn"], "turn")
			if !turnJSONBool(t, turn, "complete", "turn") {
				t.Fatalf("fixture precondition broken: 12 calls under the cap of 20 must render complete:\n%s", unbounded)
			}

			const budget = 500
			if len(unbounded) <= budget {
				t.Fatalf("fixture precondition broken: unbounded render is %d bytes, not above the %d-byte budget",
					len(unbounded), budget)
			}
			bounded, err := runTurnFixture(t, narrow, "--format", "json", "--max-bytes", fmt.Sprint(budget))
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			turn = turnJSONObject(t, turnJSONObject(t, []byte(bounded), "doc")["turn"], "turn")
			if turnJSONBool(t, turn, "complete", "turn") {
				t.Error("complete stayed true after the byte bound dropped calls from a complete turn")
			}

			boundedText, err := runTurnFixture(t, narrow, "--max-bytes", fmt.Sprint(budget))
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			if !strings.Contains(boundedText, "EVIDENCE IS PARTIAL") {
				t.Errorf("a complete turn was trimmed without disclosure:\n%s", boundedText)
			}
		})

		// A budget nothing exceeds must leave the render byte-identical: a bound that trims when it
		// does not need to would silently degrade every ordinary turn.
		t.Run("GenerousMaxBytesChangesNothing", func(t *testing.T) {
			base, err := runTurnFixture(t, wide)
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			bounded, err := runTurnFixture(t, wide, "--max-bytes", fmt.Sprint(1<<20))
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			if base != bounded {
				t.Errorf("a 1MiB budget changed a %d-byte render:\n--- unbounded ---\n%s\n--- bounded ---\n%s",
					len(base), base, bounded)
			}
		})

		// Evidence.Marker() does not receive the window — it RECOVERS the head/tail split by walking
		// Seq (evidence.go:156-162). So a byte bound that trims to an all-head window makes the marker
		// announce a "last N" that is not the turn's last N: with --max-calls 5 the derivation already
		// shows seqs 1,2,6,7,8, and keeping the first 3 of those renders 1,2,6 under the claim "first 2
		// + last 1 of 8". A marker describing a window other than the one on screen is the exact
		// failure this evidence block exists to prevent, so the two are cross-checked here.
		t.Run("MarkerDescribesTheWindowActuallyShown", func(t *testing.T) {
			for _, maxCalls := range []string{"1", "2", "3", "4", "5", "6", "7", "9", "11", "20", "25"} {
				for _, maxBytes := range []string{"300", "400", "600", "750", "900", "1100", "1250", "1500", "2000", "3000"} {
					flags := []string{"--max-calls", maxCalls, "--max-bytes", maxBytes}

					text, err := runTurnFixture(t, wide, flags...)
					if err != nil {
						t.Fatalf("%v: RunE returned %v, want nil", flags, err)
					}
					var head, tail, total, omitted int
					if n, _ := fmt.Sscanf(turnMarkerLine(t, text),
						"[showing first %d + last %d of %d calls; %d middle calls in this turn omitted",
						&head, &tail, &total, &omitted); n != 4 {
						continue // an unwindowed render has no partial marker to cross-check
					}

					seqs := turnTextCallSeqs(t, text)
					var want []int
					for i := 1; i <= head; i++ {
						want = append(want, i)
					}
					for i := total - tail + 1; i <= total; i++ {
						want = append(want, i)
					}
					if fmt.Sprint(seqs) != fmt.Sprint(want) {
						t.Errorf("%v: marker claims first %d + last %d of %d, but the render shows %v (want %v)",
							flags, head, tail, total, seqs, want)
					}
					if head+tail+omitted != total {
						t.Errorf("%v: marker arithmetic %d+%d+%d does not total %d",
							flags, head, tail, omitted, total)
					}

					// The same invariant on the json side, where there is no marker to read back:
					// calls_shown is what a consumer trusts about the array it was handed.
					asJSON, err := runTurnFixture(t, wide, append(flags, "--format", "json")...)
					if err != nil {
						t.Fatalf("%v: RunE returned %v, want nil", flags, err)
					}
					doc := turnJSONObject(t, []byte(asJSON), "doc")
					shown := turnJSONInt(t, turnJSONObject(t, doc["turn"], "turn"), "calls_shown", "turn")
					if n := len(turnCallObjects(t, doc["calls"])); n != shown {
						t.Errorf("%v: json reports calls_shown=%d but carries %d calls", flags, shown, n)
					}
				}
			}
		})

		// The flag's own help promises "0 or less: unbounded". Without this, a budget of 0 reads as a
		// 0-byte ceiling and silently empties the block the hook feeds the judge.
		t.Run("NonPositiveMaxBytesIsUnbounded", func(t *testing.T) {
			base, err := runTurnFixture(t, wide)
			if err != nil {
				t.Fatalf("RunE returned %v, want nil", err)
			}
			for _, budget := range []string{"0", "-1"} {
				got, err := runTurnFixture(t, wide, "--max-bytes", budget)
				if err != nil {
					t.Fatalf("--max-bytes %s: RunE returned %v, want nil", budget, err)
				}
				if got != base {
					t.Errorf("--max-bytes %s trimmed a %d-byte render down to %d, but the flag help calls it unbounded",
						budget, len(base), len(got))
				}
			}
		})
	})

	// runTurnEvidence always passes DefaultOptions().HeadCalls, so the CLI cannot reach a headCalls
	// wider than the window it is narrowing — but renderTurnEvidence is package-visible and Phase 6's
	// hook rewrite is the intended second caller. Driven directly, because the property is about the
	// argument the CLI happens to pin.
	t.Run("NarrowingHonoursAnAlreadyWindowedInput", func(t *testing.T) {
		ev := transcript.Evidence{
			Turn: transcript.TurnMeta{BoundaryUUID: "u1", CallsTotal: 25, CallsShown: 7},
			Calls: []transcript.Call{
				{Seq: 1, Tool: "Bash"}, {Seq: 20, Tool: "Bash"}, {Seq: 21, Tool: "Bash"},
				{Seq: 22, Tool: "Bash"}, {Seq: 23, Tool: "Bash"}, {Seq: 24, Tool: "Bash"},
				{Seq: 25, Tool: "Bash"},
			},
		}

		got := narrowTurnEvidence(ev, 5, 4)
		var seqs []int
		for _, c := range got.Calls {
			seqs = append(seqs, c.Seq)
		}
		var head, tail, total, omitted int
		if n, _ := fmt.Sscanf(got.Marker(),
			"[showing first %d + last %d of %d calls; %d middle calls in this turn omitted",
			&head, &tail, &total, &omitted); n != 4 {
			t.Fatalf("narrowed evidence produced no partial marker: %q", got.Marker())
		}
		var want []int
		for i := 1; i <= head; i++ {
			want = append(want, i)
		}
		for i := total - tail + 1; i <= total; i++ {
			want = append(want, i)
		}
		if fmt.Sprint(seqs) != fmt.Sprint(want) {
			t.Errorf("marker claims first %d + last %d of %d, but the window is %v (want %v) — a headCalls "+
				"wider than the input's own head took gap-crossing calls into the tail",
				head, tail, total, seqs, want)
		}
	})

	// The failure Pattern Matcher predicted and evidence.go:34-37 names: re-spelling a frozen marker
	// as a literal passes every AC in this phase and breaks Phase 6's format-contract test instead.
	// A source scan is the only assertion that can catch a SECOND copy of a string that is also
	// correct today.
	t.Run("FrozenMarkersAreNotRespelledInSource", func(t *testing.T) {
		src, err := os.ReadFile(filepath.Join(moduleRoot(t), "internal", "cmd", "turn.go"))
		if err != nil {
			t.Fatalf("reading internal/cmd/turn.go: %v", err)
		}
		for _, literal := range []string{
			transcript.MarkerNoCalls,
			transcript.MarkerUnavailable,
			"middle calls in this turn omitted",
		} {
			if bytes.Contains(src, []byte(literal)) {
				t.Errorf("turn.go spells the frozen marker %q as a literal; reference the "+
					"internal/transcript constant or Evidence.Marker() instead (evidence.go:34-37)", literal)
			}
		}
	})

	// The design's phase AC is "--format json validates for every Phase-0 fixture"
	// (design-doc.md:230). Phase 1 (the capture gate) has not landed, so that corpus is empty. This
	// seam makes the clause honestly satisfiable the moment it does, and mirrors
	// internal/transcript/evidence_test.go:996-1038, which skips for the same reason. A skipped
	// SUBTEST leaves the parent's "--- PASS: TestTurnEvidence" line intact; the synthetic subtests
	// above carry the assertion weight so this one skipping proves nothing either way.
	t.Run("RecordedRealFixtures", func(t *testing.T) {
		dir := filepath.Join(moduleRoot(t), "internal", "transcript", "testdata", "recorded-real")
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Skipf("Phase 1 capture gate has not landed: %s does not exist", dir)
		}
		var captured []string
		for _, e := range entries {
			if !e.IsDir() && strings.HasSuffix(e.Name(), ".jsonl") {
				captured = append(captured, filepath.Join(dir, e.Name()))
			}
		}
		if len(captured) == 0 {
			t.Skipf("Phase 1 capture gate has not landed: no .jsonl fixtures in %s", dir)
		}
		for _, path := range captured {
			t.Run(filepath.Base(path), func(t *testing.T) {
				out, _, err := execTurnEvidence(t, "--transcript", path, "--format", "json")
				if err != nil {
					t.Fatalf("RunE returned %v, want nil", err)
				}
				doc := turnJSONObject(t, []byte(out), "evidence document")
				assertTurnKeySet(t, doc, []string{"turn", "calls"}, "evidence document")
				assertTurnKeySet(t, turnJSONObject(t, doc["turn"], "turn"), []string{
					"boundary_uuid", "boundary_ts", "complete", "calls_total",
					"calls_shown", "sidechain_excluded", "unmatched_results",
				}, "turn")
			})
		}
	})
}
