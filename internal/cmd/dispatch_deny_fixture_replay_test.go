//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// This is the LIVE-PROBE tier's default-suite half (#673 item 3). Its sibling
// dispatch_admit_live_probe_integration_test.go drives the real claude CLI and therefore runs only
// where that CLI exists; this file replays the SAME platform facts from committed fixtures, through
// the SAME real decision code, on a machine with no CLI and no network.
//
// "Real decision code" is the whole point and the one rule this file may not break: slotReleasable,
// readSlotProposal, subagentSidechainQuiet and subagentTranscriptQuiet are called as they ship. The
// subagentQuietEvidence seam (dispatch_release.go:246) is NEVER assigned here — a faked ladder would
// replay our own beliefs about the platform rather than the platform — and assertLadderIsTheRealOne
// ENFORCES that rather than stating it, because 14 sibling tests do assign the seam.
//
// What the fixtures observed and what we wrote on top of it is stamped per file and enforced by
// assertFixtureStamp: see testdata/dispatch_README.md. They are committed, so a missing one is a
// FAILURE, never a skip — the green-by-skip shape this phase exists to end.

// assertFixtureStamp holds each fixture to the class it actually belongs to. The two are NOT the same
// class and the stamps must not say they are: the stop payload is the run's own bytes, while the
// sidechain file captures a layout and applies an authored timeline to it, because git carries no
// mtimes. Stamping the second "captured" would borrow the first's credibility for cases we wrote —
// the exact confusion between observed and assumed that #673 came from.
func assertFixtureStamp(t *testing.T, name string, s dispatchFixtureStamp, wantProvenance string) {
	t.Helper()
	if s.V != 1 {
		t.Errorf("%s: fixture version = %d, want 1", name, s.V)
	}
	if s.CLIVersion != dispatchFixtureCLIVersion {
		t.Errorf("%s: cli_version = %q, want %q — a captured fixture whose upstream version is "+
			"unrecorded cannot be re-judged when the platform moves", name, s.CLIVersion, dispatchFixtureCLIVersion)
	}
	if s.Provenance != wantProvenance {
		t.Errorf("%s: provenance = %q, want %q; testdata/dispatch_README.md defines the classes and "+
			"forbids mixing them", name, s.Provenance, wantProvenance)
	}
	if s.CaptureMethod == "" || s.CapturedAt == "" {
		t.Errorf("%s: capture_method/captured_at must both be recorded", name)
	}
}

// plantSidechain builds the sub-agent sidechain tree at the layout the real CLI writes and applies the
// fixture's timeline with Chtimes. Contents are empty on purpose: the ladder is stat/glob only and
// never OPENS an evidence file, so a fixture carrying bytes would imply a coupling that does not exist.
func plantSidechain(t *testing.T, workDir, sessionID string, files []dispatchFixtureFile, now time.Time) {
	t.Helper()
	dir := sessionSubagentDir(workDir, sessionID)
	if dir == "" {
		t.Fatalf("sessionSubagentDir(%q, %q) is empty; the fixture cannot be planted", workDir, sessionID)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir sidechain: %v", err)
	}
	for _, f := range files {
		path := filepath.Join(dir, f.Name)
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatalf("write %s: %v", f.Name, err)
		}
		mod := now.Add(-time.Duration(f.AgeSeconds) * time.Second)
		if err := os.Chtimes(path, mod, mod); err != nil {
			t.Fatalf("chtimes %s: %v", f.Name, err)
		}
	}
}

func touchAged(t *testing.T, path string, age time.Duration, now time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	mod := now.Add(-age)
	if err := os.Chtimes(path, mod, mod); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

// assertLadderIsTheRealOne makes this file's central rule falsifiable. The suite is one binary and
// dispatch_release_test.go's fakeSubagentQuietEvidence reassigns the seam in 14 tests; one that leaked
// past its t.Cleanup would turn every verdict below into a replay of our own beliefs, silently and in
// the green direction. A canned seam answers the same way whatever the tree looks like, so two probes
// at different ages are enough to tell the real ladder from a stand-in.
func assertLadderIsTheRealOne(t *testing.T) {
	t.Helper()
	now := time.Now().Truncate(time.Second)
	workDir := t.TempDir()
	t.Setenv(claudeConfigDirEnv, t.TempDir())
	for _, age := range []time.Duration{30 * time.Second, 4000 * time.Second} {
		session := fmt.Sprintf("ladder-probe-%d", int(age.Seconds()))
		plantSidechain(t, workDir, session,
			[]dispatchFixtureFile{{Name: "agent-probe.jsonl", AgeSeconds: int(age.Seconds())}}, now)
		quiet, measured := subagentQuietEvidence(workDir, session, "", now)
		if delta := quiet - age; !measured || delta > time.Second || delta < -time.Second {
			t.Fatalf("the evidence ladder reported (%v, %v) for a sidechain planted %v ago; it is not the "+
				"shipped one, so nothing below this line is evidence about the shipped one", quiet, measured, age)
		}
	}
}

func TestDispatchDeny_FixtureReplay(t *testing.T) {
	assertLadderIsTheRealOne(t)

	var stopFx dispatchStopFixture
	readDispatchFixture(t, "dispatch_stop_payload_2_1_258.json", &stopFx)
	var sideFx dispatchSidechainFixture
	readDispatchFixture(t, "dispatch_sidechain_timeline_2_1_258.json", &sideFx)

	t.Run("both fixtures carry their provenance and the CLI version they were captured from", func(t *testing.T) {
		assertFixtureStamp(t, "dispatch_stop_payload_2_1_258.json", stopFx.dispatchFixtureStamp, "captured")
		assertFixtureStamp(t, "dispatch_sidechain_timeline_2_1_258.json", sideFx.dispatchFixtureStamp, "captured-layout")
		if len(stopFx.Payloads) != 2 {
			t.Fatalf("the stop fixture carries %d payloads, want 2 — one per child of the same session, "+
				"which is what makes the both-still-running observation legible", len(stopFx.Payloads))
		}
		if len(sideFx.Cases) == 0 {
			t.Fatal("the sidechain fixture carries no cases")
		}
	})

	// Decoded as raw fields rather than into our struct: a JSON-tag rename in dispatchRetirePayload
	// would silently start reading nothing, and every seam-faked test in the tree would stay green.
	t.Run("the captured SubagentStop payload keeps the wire shape the retire path reads", func(t *testing.T) {
		for i, raw := range stopFx.Payloads {
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatalf("payload %d: %v", i, err)
			}
			for _, key := range []string{
				"session_id", "transcript_path", "cwd", "hook_event_name",
				"agent_id", "agent_transcript_path", "background_tasks",
			} {
				if _, ok := fields[key]; !ok {
					t.Errorf("payload %d is missing the observed field %q", i, key)
				}
			}
			var event string
			if err := json.Unmarshal(fields["hook_event_name"], &event); err != nil || event != "SubagentStop" {
				t.Errorf("payload %d: hook_event_name = %q (err %v), want SubagentStop", i, event, err)
			}

			var p dispatchRetirePayload
			if err := json.Unmarshal(raw, &p); err != nil {
				t.Fatalf("payload %d does not decode into dispatchRetirePayload: %v", i, err)
			}
			if p.Cwd == "" || p.SessionID == "" || p.TranscriptPath == "" {
				t.Errorf("payload %d left a modelled field empty: %+v — the release ladder reads all "+
					"three as evidence hints", i, p)
			}
		}
	})

	// The design assumed transcript_path named the CHILD. It does not, and the capture is the proof:
	// E2 measures the PARENT session transcript, which is why E1 outranks it.
	t.Run("transcript_path is the parent session transcript, not the child's", func(t *testing.T) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(stopFx.Payloads[0], &fields); err != nil {
			t.Fatal(err)
		}
		var parent, child, session string
		mustString := func(key string, into *string) {
			if err := json.Unmarshal(fields[key], into); err != nil {
				t.Fatalf("%s: %v", key, err)
			}
		}
		mustString("transcript_path", &parent)
		mustString("agent_transcript_path", &child)
		mustString("session_id", &session)

		if filepath.Base(parent) != session+".jsonl" {
			t.Errorf("transcript_path %q does not name the parent session %q", parent, session)
		}
		if !strings.Contains(child, filepath.Join(session, "subagents")) {
			t.Errorf("agent_transcript_path %q is not under the session's subagents dir", child)
		}
		if parent == child {
			t.Error("the capture no longer distinguishes the parent transcript from the child's")
		}
	})

	t.Run("background_tasks is observed and E0 stays dark", func(t *testing.T) {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(stopFx.Payloads[0], &fields); err != nil {
			t.Fatal(err)
		}
		var tasks []map[string]json.RawMessage
		if err := json.Unmarshal(fields["background_tasks"], &tasks); err != nil {
			t.Fatalf("background_tasks: %v", err)
		}
		if len(tasks) != 2 {
			t.Fatalf("background_tasks carries %d entries, want 2", len(tasks))
		}
		running := 0
		for i, task := range tasks {
			for _, key := range []string{"id", "type", "status", "description", "agent_type"} {
				if _, ok := task[key]; !ok {
					t.Errorf("background_tasks[%d] is missing %q", i, key)
				}
			}
			var status string
			if err := json.Unmarshal(task["status"], &status); err == nil && status == "running" {
				running++
			}
		}
		// The observation #673 rests on: at the moment ONE child reported SubagentStop, the host still
		// described BOTH children as running. A stop event is not a completion event.
		if running != 2 {
			t.Errorf("%d of 2 background_tasks read \"running\" at SubagentStop time; the capture no "+
				"longer supports leaving E0 dark", running)
		}

		var p dispatchRetirePayload
		modelled := reflect.TypeOf(p)
		for i := 0; i < modelled.NumField(); i++ {
			if tag := modelled.Field(i).Tag.Get("json"); strings.HasPrefix(tag, "background_tasks") {
				t.Errorf("dispatchRetirePayload now models %q; E0 must stay dark until PAYLOAD-CAPTURE "+
					"has shown what the host actually sends across a real run", tag)
			}
		}
		assertE0StaysDark(t)
	})

	t.Run("PAYLOAD-CAPTURE strips the free text and keeps everything else", func(t *testing.T) {
		workDir := t.TempDir()
		captureDispatchStopPayload(workDir, stopFx.Payloads[0])

		written, err := os.ReadFile(filepath.Join(workDir, ".runtime", "dispatch_stop_payload.json"))
		if err != nil {
			t.Fatalf("PAYLOAD-CAPTURE wrote nothing for a real payload: %v", err)
		}
		var got, want map[string]json.RawMessage
		if err := json.Unmarshal(written, &got); err != nil {
			t.Fatalf("captured document does not decode: %v", err)
		}
		if err := json.Unmarshal(stopFx.Payloads[0], &want); err != nil {
			t.Fatal(err)
		}
		if _, ok := want[dispatchStopPayloadFreeText]; !ok {
			t.Fatalf("the fixture carries no %s, so this test cannot prove it is stripped",
				dispatchStopPayloadFreeText)
		}
		if _, ok := got[dispatchStopPayloadFreeText]; ok {
			t.Errorf("%s survived capture", dispatchStopPayloadFreeText)
		}
		delete(want, dispatchStopPayloadFreeText)
		for key, raw := range want {
			// Checked before the compare, not left to canonicalJSON: a dropped key arrives there as a
			// nil RawMessage and fatals with "unexpected end of JSON input", which reads like a broken
			// test rather than the capture regression it would be.
			gotRaw, ok := got[key]
			if !ok {
				t.Errorf("capture dropped %q; PAYLOAD-CAPTURE strips the free text and nothing else", key)
				continue
			}
			if !bytes.Equal(canonicalJSON(t, gotRaw), canonicalJSON(t, raw)) {
				t.Errorf("capture altered %q: %s -> %s", key, raw, gotRaw)
			}
		}
		if len(got) != len(want) {
			t.Errorf("capture wrote %d fields, the payload minus its free text has %d", len(got), len(want))
		}
	})

	t.Run("the captured sidechain timeline replays through the real E1 rung", func(t *testing.T) {
		for _, c := range sideFx.Cases {
			t.Run(c.Name, func(t *testing.T) {
				now := time.Now().Truncate(time.Second)
				workDir := t.TempDir()
				t.Setenv(claudeConfigDirEnv, t.TempDir())

				if c.Files != nil {
					plantSidechain(t, workDir, sideFx.SessionID, *c.Files, now)
				}

				quiet, measured := subagentSidechainQuiet(workDir, sideFx.SessionID, now)
				if measured != c.Measured {
					t.Fatalf("measured = %v, want %v (%s)", measured, c.Measured, c.Why)
				}
				if !measured {
					if quiet != 0 {
						t.Errorf("an unmeasured rung reported quiet = %v, want 0", quiet)
					}
					return
				}
				want := time.Duration(c.QuietSeconds) * time.Second
				if delta := quiet - want; delta > time.Second || delta < -time.Second {
					t.Errorf("quiet = %v, want ~%v (%s)", quiet, want, c.Why)
				}
				if got := quiet >= subagentQuietReleaseSecs; got != c.Releasable {
					t.Errorf("quiet %v crosses the %v release threshold = %v, want %v",
						quiet, subagentQuietReleaseSecs, got, c.Releasable)
				}
			})
		}
	})

	// E2's legs, the ladder's E1-over-E2 order and slotReleasable's degrade-to-retain table are already
	// pinned by name next door — TestSubagentTranscriptQuiet_VanishedFile,
	// TestSubagentTranscriptQuiet_NonRegularPathCannotTell, TestSubagentQuietEvidence_LadderOrder,
	// TestSlotReleasable_StampMismatchRetains and TestSlotReleasable_DegradesToRetain. Restating them
	// here would be in-code cases in a file whose whole premise is captured ones, and this phase's own
	// headline change was deleting duplicated helpers.

	// The headline. A captured platform payload and a captured sidechain layout, replayed through the
	// real gate: the deny is produced by the shipped decision code, not asserted about it.
	t.Run("a captured stop payload denies a second launcher until the child goes quiet", func(t *testing.T) {
		now := time.Now().Truncate(time.Second)
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		writeCapBackendModels(t, fx.root)
		t.Setenv(claudeConfigDirEnv, t.TempDir())

		var captured dispatchRetirePayload
		if err := json.Unmarshal(stopFx.Payloads[0], &captured); err != nil {
			t.Fatal(err)
		}
		// session_id is the capture's and stays the capture's: it is what BOTH evidence rungs derive
		// their path from, and it is the field the proposal carries across processes. cwd and the E2
		// transcript path cannot be — the capture's transcript_path names the operator's real
		// ~/.claude and nothing may be planted there — so the transcript path is recomputed with
		// sessionTranscriptPath, the same helper the ladder uses. That makes the path this replay
		// exercises OURS; that the shape our helper derives still matches the capture's is a separate
		// claim, asserted above against the capture itself.
		captured.Cwd = fx.workDir
		captured.TranscriptPath = sessionTranscriptPath(fx.workDir, captured.SessionID)

		var out bytes.Buffer
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Agent", Cwd: fx.workDir}, now); err != nil {
			t.Fatalf("runDispatchAdmitCore (first): %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("the first launcher under an idle cap was not admitted:\n%s", out.String())
		}
		ledger := reservationDir(fx.workDir, capBackendKey)
		if _, err := os.Stat(filepath.Join(ledger, sequentialSlotName)); err != nil {
			t.Fatalf("the admitted launcher left no sequential.slot: %v", err)
		}

		// The real retire path, driven by the captured payload: it PROPOSES, it does not release.
		stopAt := now.Add(30 * time.Second)
		retireOneReservation(captured, "", stopAt)
		prop, ok := readSlotProposal(ledger)
		if !ok {
			t.Fatal("the captured payload produced no sequential.stop proposal")
		}
		if prop.SessionID != captured.SessionID || prop.TranscriptPath != captured.TranscriptPath {
			t.Errorf("the proposal dropped the payload's evidence hints: %+v", prop)
		}

		// The capture's own sidechain shape, still active.
		active := sidechainCase(t, sideFx, "active-two-children")
		plantSidechain(t, fx.workDir, captured.SessionID, *active.Files, stopAt)
		touchAged(t, captured.TranscriptPath, 30*time.Second, stopAt)

		out.Reset()
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Agent", Cwd: fx.workDir}, stopAt.Add(time.Second)); err != nil {
			t.Fatalf("runDispatchAdmitCore (second): %v", err)
		}
		if !strings.Contains(out.String(), `"permissionDecision":"deny"`) {
			t.Fatalf("a stop payload alone freed the cap slot while the sidechain showed a child still "+
				"writing — the #673 regression:\n%s", out.String())
		}
		if !strings.Contains(out.String(), "AF_DISABLE_PARALLEL_SUBAGENTS") {
			t.Errorf("the deny does not name the cap that produced it:\n%s", out.String())
		}

		refusals := dispatchRefusals(t, fx.root, fx.agent)
		if len(refusals) != 1 {
			t.Fatalf("the replay recorded %d refusals, want exactly 1 — 0 means the gate never fired, "+
				"more than 1 means it over-refused", len(refusals))
		}
		assertSequentialOnlyRefusal(t, refusals[0])
		if refusals[0].PoolTokens == nil || *refusals[0].PoolTokens != 262144 {
			t.Errorf("record pool_tokens = %v, want 262144", refusals[0].PoolTokens)
		}
		assertRefusalBreadcrumb(t, fx.workDir)

		// The same replay, advanced: the child goes quiet past the release threshold and the next
		// launcher is admitted. A test that only proved the deny would pass just as well against a gate
		// wedged shut, which is the other half of the same defect.
		later := stopAt.Add(subagentQuietReleaseSecs + time.Minute)
		quiet := sidechainCase(t, sideFx, "quiet-past-release")
		plantSidechain(t, fx.workDir, captured.SessionID, *quiet.Files, later)
		touchAged(t, captured.TranscriptPath, 1300*time.Second, later)

		out.Reset()
		if err := runDispatchAdmitCore(t.Context(), &out, dispatchAdmitPayload{ToolName: "Agent", Cwd: fx.workDir}, later); err != nil {
			t.Fatalf("runDispatchAdmitCore (third): %v", err)
		}
		if out.Len() != 0 {
			t.Fatalf("after the captured sidechain went quiet past the release threshold the next "+
				"sub-agent was still refused; sequential progress stalled:\n%s", out.String())
		}
		if _, err := os.Stat(filepath.Join(ledger, sequentialSlotName)); err != nil {
			t.Errorf("the re-admit did not re-claim the slot: %v", err)
		}
		if again := dispatchRefusals(t, fx.root, fx.agent); len(again) != 1 {
			t.Errorf("the admit leg wrote %d refusals in total, want the original 1", len(again))
		}
	})
}

// assertE0StaysDark walks the whole release path for the background_tasks key rather than grepping one
// file's lines. Grepping needs a comment filter, and the WHY comments in dispatch_release.go discuss
// E0 at length — so the filter has to be exactly right or the guard is either permanently red or
// quietly blind to a trailing // and a /* */ block. The parser already knows which bytes are code.
func assertE0StaysDark(t *testing.T) {
	t.Helper()
	fset := token.NewFileSet()
	for _, name := range []string{"dispatch_release.go", "dispatch_admit.go", "subagent_occupancy.go"} {
		path := filepath.Join(findModuleRoot(t), "internal", "cmd", name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !strings.Contains(lit.Value, "background_tasks") {
				return true
			}
			t.Errorf("%s wires background_tasks into the release path: %s",
				fset.Position(lit.Pos()), lit.Value)
			return true
		})
	}
}

func sidechainCase(t *testing.T, fx dispatchSidechainFixture, name string) dispatchSidechainCase {
	t.Helper()
	for _, c := range fx.Cases {
		if c.Name == name {
			if c.Files == nil {
				t.Fatalf("fixture case %q carries no files", name)
			}
			return c
		}
	}
	t.Fatalf("the sidechain fixture has no case named %q", name)
	return dispatchSidechainCase{}
}

// canonicalJSON re-marshals a raw value so the comparison is over structure, not over the indentation
// captureDispatchStopPayload chose.
func canonicalJSON(t *testing.T, raw json.RawMessage) []byte {
	t.Helper()
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("re-marshalling %s: %v", raw, err)
	}
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-marshalling %s: %v", raw, err)
	}
	return data
}
