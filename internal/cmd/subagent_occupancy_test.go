package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// seedRecordedRefusal plants what `af dispatch-admit` leaves behind when it refuses, through the
// PRODUCTION writer. Hand-rolling the JSON here would let the observer's tests keep passing against a
// record shape refuseLaunch stopped writing, which is precisely the divergence #673 is about.
func seedRecordedRefusal(t *testing.T, workDir, reason string, at time.Time) {
	t.Helper()
	v := tokenomics.BackendVerdict{
		Verdict:    tokenomics.VerdictNoFit,
		Reason:     reason,
		PoolTokens: 400_000,
	}
	if reason != reasonSequentialOnly {
		v.SummedTokens = 380_000
	}
	writeLastRefusal(workDir, "https://gateway.example/v1", v, at)
	if _, ok := readLastRefusal(workDir); !ok {
		t.Fatal("fixture: the breadcrumb the observer reads did not land")
	}
}

// seedSubagentTranscript writes one sub-agent's JSONL where the host actually puts it: a
// `subagents/` directory INSIDE `<sessionID>/`, a sibling of `<sessionID>.jsonl` rather than a
// child of it. Verified against a live host (claude 2.1.224): `.meta.json` sidecars sit alongside
// the transcripts, which is why the production glob is `agent-*.jsonl` and not `*`.
func seedSubagentTranscript(t *testing.T, workDir, sessionID, agentID string, lines ...string) string {
	t.Helper()
	dir := sessionSubagentDir(workDir, sessionID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir subagents: %v", err)
	}
	path := filepath.Join(dir, "agent-"+agentID+".jsonl")
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing sub-agent transcript: %v", err)
	}
	// The sidecar the glob must not read, and it carries a REAL usage-bearing record on purpose. An
	// inert `{"not":"a transcript"}` would prove nothing: transcriptSpendWindow unmarshals it into a
	// zero-valued generationRecord, reports nothing seen and skips it, so a glob widened to `*` would
	// pass every assertion below unchanged. With usage in it, widening the glob double-counts this
	// agent and every sum in this file goes red — which is the discrimination the sidecar is here for.
	if err := os.WriteFile(filepath.Join(dir, "agent-"+agentID+".meta.json"),
		[]byte(transcriptLine("2026-08-30T11:30:00.000Z", "msg-sidecar-"+agentID, "text", "z", 7777, 0, 0, 0)+"\n"),
		0o644); err != nil {
		t.Fatalf("writing sidecar: %v", err)
	}
	return path
}

// TestSubagentOccupancy is #668 K18's derivation half. event.go:156-160 states the blind spot it
// closes: the main trio covers the MAIN agent only, and a step that delegated most of its work
// records almost nothing about what that work cost.
func TestSubagentOccupancy(t *testing.T) {
	const start, end = "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"

	t.Run("the sum spans every sub-agent transcript in the tree", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		seedSubagentTranscript(t, workDir, "sess-fan", "aaa",
			transcriptLine("2026-08-30T11:10:00.000Z", "msg_1", "text", "x", 1000, 200, 0, 0))
		seedSubagentTranscript(t, workDir, "sess-fan", "bbb",
			transcriptLine("2026-08-30T11:11:00.000Z", "msg_2", "text", "y", 3000, 400, 0, 0))

		got, ok := subagentSpend(sessionSubagentDir(workDir, "sess-fan"), start, end)
		if !ok {
			t.Fatal("two readable sub-agent transcripts in the window measured nothing")
		}
		if want := int64(1000 + 200 + 3000 + 400); got.total != want {
			t.Errorf("subagent spend = %d, want %d — the sum must span the whole tree", got.total, want)
		}
	})

	t.Run("each message counts once per file, at its maximum", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		// The same fan-out shape TestMessageDedup pins for the main transcript: one record per
		// content block, the whole message's usage stamped on every one, and the FIRST record
		// carrying an in-flight partial that the later ones supersede.
		seedSubagentTranscript(t, workDir, "sess-dedup", "aaa",
			transcriptLine("2026-08-30T11:10:00.000Z", "msg_1", "text", "x", 1000, 1, 0, 0),
			transcriptLine("2026-08-30T11:10:01.000Z", "msg_1", "tool_use", "", 1000, 200, 0, 0),
			transcriptLine("2026-08-30T11:10:02.000Z", "msg_1", "tool_use", "", 1000, 200, 0, 0))

		got, ok := subagentSpend(sessionSubagentDir(workDir, "sess-dedup"), start, end)
		if !ok {
			t.Fatal("a readable sub-agent transcript in the window measured nothing")
		}
		if want := int64(1000 + 200); got.total != want {
			t.Errorf("subagent spend = %d, want %d; %d means every line was summed and %d means "+
				"first-wins kept the in-flight partial", got.total, want, 3*1000+401, 1000+1)
		}
	})

	t.Run("records outside the step's window are not this step's", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		seedSubagentTranscript(t, workDir, "sess-window", "aaa",
			transcriptLine("2026-08-30T10:59:59.000Z", "msg_before", "text", "x", 5000, 5000, 0, 0),
			transcriptLine("2026-08-30T11:30:00.000Z", "msg_in", "text", "y", 100, 20, 0, 0),
			transcriptLine("2026-08-30T12:00:00.000Z", "msg_at_close", "text", "z", 7000, 7000, 0, 0))

		got, ok := subagentSpend(sessionSubagentDir(workDir, "sess-window"), start, end)
		if !ok {
			t.Fatal("a record inside the window measured nothing")
		}
		if want := int64(120); got.total != want {
			t.Errorf("subagent spend = %d, want %d — [start, end) is half-open, so a record stamped "+
				"exactly at the close belongs to whatever comes next", got.total, want)
		}
	})

	t.Run("no sub-agent tree measures nothing rather than zero", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()

		if _, ok := subagentSpend(sessionSubagentDir(workDir, "sess-none"), start, end); ok {
			t.Error("an absent sub-agent tree measured; a step that delegated nothing and a host " +
				"that expired the tree must not be reported as the same thing")
		}
	})
}

// TestDoneWiresSubagentOccupancy is K18's interlock, and it is the test the derivation above cannot
// be: every rule it follows stays green if af done simply never calls it.
func TestDoneWiresSubagentOccupancy(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, t.TempDir())
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	const sessionID = "sess-subagent-wiring"
	runLifecycleVerbsWithSession(t, fx, sessionID, func() {
		startTS := firstStepStart(t, fx.root, fx.agent).TS
		seedTranscript(t, fx.workDir, sessionID,
			transcriptLine(startTS, "msg_main", "text", strings.Repeat("z", 40), 1000, 100, 10, 1))
		seedSubagentTranscript(t, fx.workDir, sessionID, "aaa",
			transcriptLine(startTS, "msg_sub", "text", "s", 8000, 700, 0, 0))
	})

	end := lastStepEnd(t, fx.root, fx.agent)
	if end.SubagentTokens == nil {
		t.Fatal("step_end carries no subagent_tokens: af done never called the sub-agent derivation")
	}
	if want := int64(8700); *end.SubagentTokens != want {
		t.Errorf("subagent_tokens = %d, want %d", *end.SubagentTokens, want)
	}
	// event.go:156-160 is explicit that this is the right answer for peak: a sub-agent's window is
	// not this one's, and folding it in would invent occupancy that never existed.
	if end.PeakCtxTokens == nil || *end.PeakCtxTokens != 1111 {
		t.Errorf("peak_ctx_tokens = %v, want 1111 — the sub-agent's spend must not reach the main trio",
			end.PeakCtxTokens)
	}
	if end.OutTokens == nil || *end.OutTokens != 100 {
		t.Errorf("out_tokens = %v, want 100 — the main agent generated 100 of them", end.OutTokens)
	}
}

// TestDoneRecordsNoSubagentTokensWithoutATree is the other half of the wiring and the reason the
// field is a pointer: a step that never delegated must be distinguishable from one whose sub-agent
// transcripts the host has since expired, and neither is a zero.
func TestDoneRecordsNoSubagentTokensWithoutATree(t *testing.T) {
	t.Setenv(claudeConfigDirEnv, t.TempDir())
	fx := newLifecycleFixture(t)
	gateOn(t, fx.root)

	const sessionID = "sess-no-subagents"
	runLifecycleVerbsWithSession(t, fx, sessionID, func() {
		startTS := firstStepStart(t, fx.root, fx.agent).TS
		seedTranscript(t, fx.workDir, sessionID,
			transcriptLine(startTS, "msg_main", "text", "z", 1000, 100, 0, 0))
	})

	end := lastStepEnd(t, fx.root, fx.agent)
	if end.SubagentTokens != nil {
		t.Errorf("subagent_tokens = %d on a step that delegated nothing; absence is the honest answer",
			*end.SubagentTokens)
	}
	if end.OutTokens == nil {
		t.Error("the main derivation stopped working when the sub-agent tree was absent")
	}
}

// TestSubagentScanStaysOffTheRenderPath is DEC-3 restated as an interlock. The statusline renders on
// a 10s cadence under a 500 ms budget (fable_increment_pr595_test.go:111,145) and reads a bounded
// 4 KB snapshot; a sibling-tree glob on that path would put unbounded host I/O inside it.
//
// It asserts on the CODE rather than on a timing, because a timing assertion passes on a fast host
// with the glob wired in and fails on a slow one without it.
//
// It scans THIS package, not internal/statusline, which is where a first pass looked. That scan
// could never have failed whatever the implementation did: both names are unexported members of
// package cmd, and the library cannot refer to them at all — it is imported BY cmd, not the other
// way round. The render path that can reach them is the one in this package, `af statusline render`
// (statusline.go:208 and statusline_tokens.go), so the interlock is an allowlist of the files that
// may name them and a failure directs the next author to classify a new site.
func TestSubagentScanStaysOffTheRenderPath(t *testing.T) {
	allowed := map[string]bool{
		"subagent_occupancy.go":   true, // the definitions, and the only file that may call them freely
		"telemetry_generation.go": true, // af done's step-close derivation — the one intended caller
		// #673's cap-slot release ladder. A DIFFERENT classification from the two above, and the
		// distinction is the one this interlock cares about: it names sessionSubagentDir only to stat
		// the tree's newest mtime, never subagentSpend, so it opens no transcript and pays none of the
		// unbounded read cost that must stay off the 500 ms render path. It runs on the dispatch gate.
		"dispatch_release.go": true,
	}
	for _, name := range []string{"subagentSpend", "sessionSubagentDir"} {
		hits := grepPackage(t, ".", name)
		if len(hits) == 0 {
			t.Fatalf("no production file names %s; this interlock is scanning the wrong tree and "+
				"would stay green with the glob wired straight into the render path", name)
		}
		for _, hit := range hits {
			file := filepath.Base(hit[:strings.LastIndex(hit, ":")])
			if !allowed[file] {
				t.Errorf("%s is named at %s; the sub-agent scan is unbounded host I/O and belongs at "+
					"step close, not on the 500 ms render path. If this site is legitimate, add its "+
					"file to the allowlist in the SAME change", name, hit)
			}
		}
	}
}

// grepPackage returns the file:line of every reference to needle in dir's PRODUCTION .go files.
// It is a source read rather than a call-graph analysis for the reason the caller gives: the claim
// being pinned is "these files and no others name that", which is exactly what a reader would check
// by hand. Test files are skipped because a test that names the thing it is pinning is not a wiring.
func grepPackage(t *testing.T, dir, needle string) []string {
	t.Helper()
	var hits []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("reading %s: %v", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		for i, line := range strings.Split(string(body), "\n") {
			if strings.Contains(line, needle) {
				hits = append(hits, filepath.Join(dir, e.Name())+":"+strconv.Itoa(i+1))
			}
		}
	}
	return hits
}

// runSubagentObserve drives the observer's core the way the hook would, and returns what it wrote to
// the hook's stdout channel.
func runSubagentObserve(t *testing.T, toolName string) string {
	t.Helper()
	var out bytes.Buffer
	if err := runSubagentObserveCore(t.Context(), &out, subagentObservePayload{
		ToolName: toolName,
		Cwd:      mustGetwd(t),
	}); err != nil {
		t.Fatalf("the observer returned an error; ADR-007 says a hook never blocks: %v", err)
	}
	return out.String()
}

func mustGetwd(t *testing.T) string {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	return wd
}

// TestInterposeNonBlocking is #668 K18's observer half. It fires on PostToolUse/Task — AFTER the
// sub-agent has already run — so its ONLY lever is counsel that reaches the next launch. ADR-007
// makes that a hard rule rather than a preference: a hook that blocks stops the agent.
//
// As of #673 item 1 the trigger is a refusal the GATE recorded, not a verdict this hook reached. Each
// case below therefore seeds the breadcrumb rather than an occupancy, and the occupancy the fixture
// still carries is there to prove it plays no part.
func TestInterposeNonBlocking(t *testing.T) {
	t.Run("a Task completion after a recorded refusal counsels once per episode", func(t *testing.T) {
		fx, _, _ := primedFixture(t, 90)
		gateOn(t, fx.root)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"dispatch": "on"})
		seedRecordedRefusal(t, fx.workDir, "", time.Now())
		sends := captureSubagentMail(t)

		first := runSubagentObserve(t, "Task")
		second := runSubagentObserve(t, "Task")

		if len(*sends) != 1 {
			t.Errorf("TOKENOMICS_DISPATCH sends = %d, want exactly 1 for the episode; the second Task "+
				"completion arrives while the first counsel is still unread", len(*sends))
		}
		if len(*sends) == 1 && (*sends)[0].subject != "TOKENOMICS_DISPATCH" {
			t.Errorf("subject = %q, want TOKENOMICS_DISPATCH", (*sends)[0].subject)
		}
		by := interventionsByMechanism(t, fx.root, fx.agent)
		if got := len(by[string(tokenomics.MechanismDispatch)]); got != 1 {
			t.Errorf("dispatch intervention records = %d, want 1", got)
		}
		if len(by[string(tokenomics.MechanismDispatch)]) == 1 {
			// The label every other recording site sets. A hook that fired without it files its
			// evidence outside the formula the improvement loop reads by.
			if got := by[string(tokenomics.MechanismDispatch)][0].Formula; got != "offpath" {
				t.Errorf("formula = %q, want %q", got, "offpath")
			}
		}
		for _, o := range []string{first, second} {
			assertNoBlockingDecision(t, o)
		}
	})

	t.Run("a mechanism that is off is silent and still exits 0", func(t *testing.T) {
		fx, _, _ := primedFixture(t, 90)
		gateOn(t, fx.root)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"dispatch": "off"})
		// Seeded so the silence below is attributable to the policy guard and to nothing else. Without
		// it this case would pass on an absent breadcrumb and stop testing the guard at all.
		seedRecordedRefusal(t, fx.workDir, "", time.Now())
		sends := captureSubagentMail(t)

		out := runSubagentObserve(t, "Task")

		if len(*sends) != 0 {
			t.Errorf("dispatch is off and %d mails were sent", len(*sends))
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 0 {
			t.Errorf("dispatch is off and %d intervention records were written", got)
		}
		assertNoBlockingDecision(t, out)
	})

	t.Run("a tool that is not Task is not this observer's business", func(t *testing.T) {
		fx, _, _ := primedFixture(t, 90)
		gateOn(t, fx.root)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"dispatch": "on"})
		// Same reason as the case above: everything except the tool name is arranged to fire.
		seedRecordedRefusal(t, fx.workDir, "", time.Now())
		sends := captureSubagentMail(t)

		out := runSubagentObserve(t, "Bash")

		if len(*sends) != 0 {
			t.Errorf("a Bash call produced %d sub-agent counsels", len(*sends))
		}
		assertNoBlockingDecision(t, out)
	})

	// The demotion, stated as behaviour. Before #673 this observer computed its own verdict from the
	// session's occupancy, so a 95%-full session was counselled whether or not the gate had ever
	// objected — two components answering one question, free to disagree. Now the gate's record is the
	// only trigger, and the occupancy below is deliberately extreme to prove it is not consulted.
	t.Run("a gate that never refused leaves the session alone", func(t *testing.T) {
		fx, _, _ := primedFixture(t, 95)
		gateOn(t, fx.root)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"dispatch": "on"})
		sends := captureSubagentMail(t)

		out := runSubagentObserve(t, "Task")

		if len(*sends) != 0 {
			t.Errorf("a session the gate never refused was told to serialize (%d mails) — the observer "+
				"is computing a capacity verdict of its own again (#673 AC-1)", len(*sends))
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 0 {
			t.Errorf("nothing fired but %d intervention records were written", got)
		}
		assertNoBlockingDecision(t, out)
	})

	t.Run("a refusal older than one fan-out window is not today's episode", func(t *testing.T) {
		fx, _, _ := primedFixture(t, 90)
		gateOn(t, fx.root)
		armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"dispatch": "on"})
		seedRecordedRefusal(t, fx.workDir, "", time.Now().Add(-fanOutLatchTTL-time.Minute))
		sends := captureSubagentMail(t)

		out := runSubagentObserve(t, "Task")

		if len(*sends) != 0 {
			t.Errorf("a refusal older than %v was relayed as current counsel (%d mails)", fanOutLatchTTL, len(*sends))
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 0 {
			t.Errorf("nothing fired but %d intervention records were written", got)
		}
		assertNoBlockingDecision(t, out)
	})

	// Four ways the record can be unusable, one answer. This is the inversion loadAdvisoryLedger does
	// not make: that ledger is read permissively because a bad read costs one duplicate advisory, but a
	// bad read HERE would tell an operator their backend is full when the gate never said so.
	t.Run("an unreadable refusal costs silence, never false counsel", func(t *testing.T) {
		cases := []struct {
			name string
			body string
		}{
			{"absent", ""},
			{"empty", "  "},
			{"corrupt", `{"v":1,"ts":`},
			{"a version this binary does not speak", `{"v":99,"ts":"2099-01-01T00:00:00Z","backend":"b","reason":"","pool_tokens":1,"summed_tokens":1}`},
			{"no timestamp to judge freshness by", `{"v":1,"backend":"b","reason":"","pool_tokens":1,"summed_tokens":1}`},
			{"a reason outside the closed vocabulary", `{"v":1,"ts":"` + time.Now().UTC().Format(time.RFC3339Nano) + `","backend":"b","reason":"invented","pool_tokens":1,"summed_tokens":1}`},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fx, _, _ := primedFixture(t, 90)
				gateOn(t, fx.root)
				armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"dispatch": "on"})
				if tc.body != "" {
					writeRuntimeFile(t, fx.workDir, dispatchLastRefusalName, tc.body)
				}
				sends := captureSubagentMail(t)

				out := runSubagentObserve(t, "Task")

				if len(*sends) != 0 {
					t.Errorf("an unusable breadcrumb produced %d counsels; it must produce none", len(*sends))
				}
				if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 0 {
					t.Errorf("an unusable breadcrumb produced %d intervention records", got)
				}
				assertNoBlockingDecision(t, out)
			})
		}
	})

	// The relay half: what the bead SAYS must be the gate's own recorded integers, per reason, and
	// nothing derived from them. A percentage appearing here would mean the arithmetic came back.
	t.Run("the counsel restates the recorded figures and computes nothing", func(t *testing.T) {
		cases := []struct {
			name    string
			reason  string
			want    []string
			notWant []string
		}{
			{
				name:   "headroom",
				reason: "",
				want:   []string{"380000 of 400000 pool tokens", "one at a time"},
			},
			{
				name:   "the launcher's own child floor",
				reason: reasonChildFloorHandoff,
				want:   []string{"380000 of 400000 pool tokens", "af handoff"},
			},
			{
				// A semaphore, not token arithmetic. The gate's own deny omits the figures here
				// (#669 C1 / #672 hard cap) and so must the relay — a pool figure printed against a
				// refusal that never measured one is the invented number this design forbids.
				name:    "the sequential cap",
				reason:  reasonSequentialOnly,
				want:    []string{"AF_DISABLE_PARALLEL_SUBAGENTS", "sequential"},
				notWant: []string{"pool tokens", "400000"},
			},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				fx, _, _ := primedFixture(t, 90)
				gateOn(t, fx.root)
				armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"dispatch": "on"})
				seedRecordedRefusal(t, fx.workDir, tc.reason, time.Now())
				sends := captureSubagentMail(t)

				out := runSubagentObserve(t, "Task")

				if len(*sends) != 1 {
					t.Fatalf("counsels = %d, want 1", len(*sends))
				}
				body := (*sends)[0].body
				for _, w := range tc.want {
					if !strings.Contains(body, w) {
						t.Errorf("the counsel does not restate %q:\n%s", w, body)
					}
				}
				for _, w := range tc.notWant {
					if strings.Contains(body, w) {
						t.Errorf("the counsel invented %q for a refusal that measured no tokens:\n%s", w, body)
					}
				}
				if strings.Contains(body, "%") {
					t.Errorf("the counsel carries a percentage, so something computed one:\n%s", body)
				}
				// The same-loop nudge must carry the same sentence: an agent that never reads its mail
				// before the next launch sees only this one.
				if !strings.Contains(out, "dispatch-admit") {
					t.Errorf("additionalContext does not carry the relayed counsel:\n%s", out)
				}
				assertNoBlockingDecision(t, out)
			})
		}
	})

	t.Run("an unusable environment costs the counsel and nothing else", func(t *testing.T) {
		// No factory, no role, no reading: every resolution the observer does fails. It must still
		// return nil, because the alternative is a non-zero exit on a PostToolUse hook.
		t.Setenv("AF_ROOT", "")
		t.Setenv("AF_ROLE", "")
		dir := t.TempDir()
		var out bytes.Buffer
		err := runSubagentObserveCore(t.Context(), &out, subagentObservePayload{ToolName: "Task", Cwd: dir})
		if err != nil {
			t.Errorf("the observer returned %v outside a factory; ADR-007 says it exits 0", err)
		}
		assertNoBlockingDecision(t, out.String())
	})
}

// assertNoBlockingDecision reads the observer's stdout as the host does. Claude Code treats a
// `permissionDecision` of "deny" or "ask", or a top-level `continue: false`, as a stop; the observer
// may only ever emit additionalContext.
func assertNoBlockingDecision(t *testing.T, out string) {
	t.Helper()
	if strings.TrimSpace(out) == "" {
		return
	}
	var payload struct {
		Continue           *bool `json:"continue"`
		HookSpecificOutput struct {
			PermissionDecision string `json:"permissionDecision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		t.Fatalf("the observer wrote non-JSON to the hook channel: %v\n%s", err, out)
	}
	if payload.Continue != nil && !*payload.Continue {
		t.Error(`the observer emitted "continue": false — ADR-007: hooks never block`)
	}
	if d := payload.HookSpecificOutput.PermissionDecision; d != "" && d != "allow" {
		t.Errorf("the observer emitted permissionDecision %q — ADR-007: hooks never block", d)
	}
}

type subagentMailSend struct {
	subject string
	body    string
}

// captureSubagentMail swaps the observer's send seam for a recorder, the containment_test.go idiom.
// It returns a pointer so the caller reads the sends AFTER the run rather than capturing an empty
// slice by value.
func captureSubagentMail(t *testing.T) *[]subagentMailSend {
	t.Helper()
	sends := &[]subagentMailSend{}
	orig := sendSubagentMail
	sendSubagentMail = func(_, _, subject, body string) error {
		*sends = append(*sends, subagentMailSend{subject: subject, body: body})
		return nil
	}
	t.Cleanup(func() { sendSubagentMail = orig })
	return sends
}

// The deployment interlock — that both settings templates actually invoke this verb — lives with
// the templates in internal/claude (TestEnsureSettings_PostToolUseSubagentObserver). Every assertion
// in this file passes against an observer no session ever runs.

// seedWorkflowSubagentTranscript writes a sub-agent transcript where a Workflow tool's children
// actually land: subagents/workflows/wf_<id>/, one level BELOW the direct children rather than
// beside them. The flat glob this replaced could not see this directory at all.
//
// The journal.jsonl written alongside is the decoy recursion introduces. The flat glob never had to
// exclude one because it never descended far enough to meet one, so the agent-*.jsonl pattern
// becomes MORE load-bearing under a recursive walk, not less.
func seedWorkflowSubagentTranscript(t *testing.T, workDir, sessionID, workflowID, agentID string, lines ...string) {
	t.Helper()
	dir := filepath.Join(sessionSubagentDir(workDir, sessionID), "workflows", "wf_"+workflowID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir workflow subagents: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agent-"+agentID+".jsonl"),
		[]byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatalf("writing workflow sub-agent transcript: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"),
		[]byte(transcriptLine("2026-08-30T11:30:00.000Z", "msg-journal-"+workflowID, "text", "j", 6666, 0, 0, 0)+"\n"),
		0o644); err != nil {
		t.Fatalf("writing workflow journal: %v", err)
	}
}

// subagentLaunchLine is one sub-agent's record of delegating further. The tool name is the host's
// own (isSubagentTool), and the block carries usage so the line is also a legitimate spend record —
// a launch that only counted on usage-free lines would be a count of something else.
func subagentLaunchLine(ts, msgID, toolName string, in, out int64) string {
	return fmt.Sprintf(
		`{"timestamp":%q,"type":"assistant","message":{"id":%q,"role":"assistant",`+
			`"content":[{"type":"tool_use","id":"tu","name":%q,"input":{}}],`+
			`"usage":{"input_tokens":%d,"output_tokens":%d}}}`,
		ts, msgID, toolName, in, out)
}

// TestSubagentOccupancyWalksWorkflowTree is six-sigma C-10 (#678 K1): the walk that measures a
// step's delegated work must reach EVERY sub-agent transcript, and the flat glob it replaces reached
// only the direct children.
//
// The under-count was not marginal. A session that fans out through the Workflow tool keeps its
// direct children in subagents/ and the workflow's children in subagents/workflows/wf_<id>/, so a
// step with 4 direct and 26 workflow sub-agents measured 4 — and read as a step that barely
// delegated, on exactly the runs where delegation dominated the bill.
func TestSubagentOccupancyWalksWorkflowTree(t *testing.T) {
	const start, end = "2026-08-30T11:00:00.000Z", "2026-08-30T12:00:00.000Z"

	t.Run("the sum reaches sub-agents at every depth", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		seedSubagentTranscript(t, workDir, "sess-wf", "direct",
			transcriptLine("2026-08-30T11:10:00.000Z", "msg_direct", "text", "x", 1000, 200, 0, 0))
		seedWorkflowSubagentTranscript(t, workDir, "sess-wf", "abc", "nested",
			transcriptLine("2026-08-30T11:11:00.000Z", "msg_nested", "text", "y", 3000, 400, 0, 0))

		got, ok := subagentSpend(sessionSubagentDir(workDir, "sess-wf"), start, end)
		if !ok {
			t.Fatal("a tree with a workflow child measured nothing")
		}
		if want := int64(1000 + 200 + 3000 + 400); got.total != want {
			t.Errorf("subagent spend = %d, want %d; %d means the walk stopped at the top directory "+
				"and the whole workflow subtree went unmeasured", got.total, want, 1000+200)
		}
		// The legs are the same walk's answer, split (#678 K1). If they disagreed with the total the
		// total would still be right — it is Spend(), the figure that has shipped — so they are
		// asserted against it rather than independently.
		if got.in+got.out != got.total {
			t.Errorf("in+out = %d but total = %d; the split must explain the shipped figure, not "+
				"redefine it", got.in+got.out, got.total)
		}
	})

	t.Run("only transcripts are read, at any depth", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		// seedSubagentTranscript plants a usage-bearing .meta.json sidecar and
		// seedWorkflowSubagentTranscript a usage-bearing journal.jsonl. Both would be counted by a
		// walk that matched on anything wider than agent-*.jsonl, and both are inside the window.
		seedSubagentTranscript(t, workDir, "sess-decoy", "direct",
			transcriptLine("2026-08-30T11:10:00.000Z", "msg_direct", "text", "x", 100, 20, 0, 0))
		seedWorkflowSubagentTranscript(t, workDir, "sess-decoy", "abc", "nested",
			transcriptLine("2026-08-30T11:11:00.000Z", "msg_nested", "text", "y", 300, 40, 0, 0))

		got, ok := subagentSpend(sessionSubagentDir(workDir, "sess-decoy"), start, end)
		if !ok {
			t.Fatal("a tree with a workflow child measured nothing")
		}
		if want := int64(100 + 20 + 300 + 40); got.total != want {
			t.Errorf("subagent spend = %d, want %d; the extra is a sidecar or a workflow journal "+
				"counted as a sub-agent", got.total, want)
		}
	})

	t.Run("delegations the sub-agents themselves made are counted", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		seedSubagentTranscript(t, workDir, "sess-nested", "direct",
			subagentLaunchLine("2026-08-30T11:10:00.000Z", "msg_a", "Task", 100, 20))
		seedWorkflowSubagentTranscript(t, workDir, "sess-nested", "abc", "nested",
			subagentLaunchLine("2026-08-30T11:11:00.000Z", "msg_b", "Agent", 300, 40),
			transcriptLine("2026-08-30T11:12:00.000Z", "msg_c", "tool_use", "", 300, 40, 0, 0))

		got, ok := subagentSpend(sessionSubagentDir(workDir, "sess-nested"), start, end)
		if !ok {
			t.Fatal("a tree of delegating sub-agents measured nothing")
		}
		// Two, not three: msg_c's block is a Read, and both host spellings of the launcher count.
		if got.nestedLaunches != 2 {
			t.Errorf("nested launches = %d, want 2; this is the number that turns C-10's depth "+
				"question from a directory test into a measurement", got.nestedLaunches)
		}
	})

	t.Run("a tree of only decoys measures nothing rather than zero", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		workDir := t.TempDir()
		dir := filepath.Join(sessionSubagentDir(workDir, "sess-empty"), "workflows", "wf_abc")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "journal.jsonl"),
			[]byte(transcriptLine("2026-08-30T11:30:00.000Z", "msg_j", "text", "j", 9, 9, 0, 0)+"\n"), 0o644); err != nil {
			t.Fatalf("writing journal: %v", err)
		}

		if _, ok := subagentSpend(sessionSubagentDir(workDir, "sess-empty"), start, end); ok {
			t.Error("a tree containing no sub-agent transcript measured; a host journal is not a " +
				"sub-agent, and reporting one as a measured zero would put a fabricated baseline in " +
				"the log")
		}
	})
}
