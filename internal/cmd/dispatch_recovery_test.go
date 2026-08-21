package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// K11 (#596 Phase 4A). The load-bearing claim: a breaker-halted agent is never going
// to become un-busy on its own — it is latched until an operator clears it — so
// recording it as an ordinary deferral queues work behind an agent that will never
// take it. Halted must count as an error, on the distinctly-named stall path.
//
// None of these may call t.Parallel: they reassign package seams.

// TestDispatchTargetState_HaltedRecoveringDarkBusy unit-tests the pure decision the
// three busy-skip sites share. runDispatch itself shells out to `gh auth status` and
// `gh issue list` with no seam, so its two item-loop sites are unreachable by any
// hermetic test — extracting the decision is what makes them testable at all.
func TestDispatchTargetState_HaltedRecoveringDarkBusy(t *testing.T) {
	root := t.TempDir()
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}

	if err := saveRecoveryState(realRoot, "halted",
		recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3}); err != nil {
		t.Fatalf("plant halted: %v", err)
	}
	if err := saveRecoveryState(realRoot, "attempted",
		recoveryState{Attempts: 1, WindowStart: recoveryStamp(time.Now())}); err != nil {
		t.Fatalf("plant attempted: %v", err)
	}
	if err := saveRecoveryState(realRoot, "pending",
		recoveryState{PendingStepID: "step-1", PendingDeadline: recoveryStamp(time.Now().Add(time.Hour))}); err != nil {
		t.Fatalf("plant pending: %v", err)
	}
	// A corrupt latch reads HALTED: loadRecoveryState fails closed, and the dispatcher
	// must inherit that direction rather than dispatching into an unknown state.
	breakerDir := filepath.Join(realRoot, ".runtime", "recovery")
	if err := os.WriteFile(filepath.Join(breakerDir, "corrupt.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatalf("plant corrupt: %v", err)
	}
	// darkworker's channel has a validated datum well past dark_grace_secs.
	now := time.Now()
	plantSnapshot(t, realRoot, "darkworker", "sess-dark", 40, now.Add(-2*time.Hour), now, testRecoveryConfig())

	cases := []struct {
		agent   string
		running bool
		want    string
		why     string
	}{
		{"halted", true, targetStateHalted, "a latched breaker outranks plain liveness"},
		{"corrupt", true, targetStateHalted, "an undecodable breaker fails closed"},
		{"attempted", true, targetStateRecovering, "a non-zero in-window attempt count"},
		{"pending", true, targetStateRecovering, "an open post-recovery confirmation"},
		{"darkworker", true, targetStateDark, "busy with a dark occupancy channel"},
		{"plain", true, targetStateBusy, "busy with nothing else to report"},
		{"halted", false, targetStateFree, "not running ⇒ the busy decision does not arise"},
		{"plain", false, targetStateFree, "not running ⇒ free"},
	}
	for _, c := range cases {
		if got := dispatchTargetState(realRoot, c.agent, c.running); got != c.want {
			t.Errorf("dispatchTargetState(%q, running=%v) = %q, want %q (%s)",
				c.agent, c.running, got, c.want, c.why)
		}
	}
}

// TestDispatch_HaltedAgentRoutesThroughStall drives the real workflow path end to
// end for the three busy states. The plain-busy row is the regression guard that
// keeps TestWorkflow_BusyAgent_SkipsNonBlocking honest.
func TestDispatch_HaltedAgentRoutesThroughStall(t *testing.T) {
	cases := []struct {
		name        string
		breaker     *recoveryState
		wantErrors  int
		wantSkipped int
		wantStream  string // "err" or "out"
		wantSubstr  string
	}{
		{
			name:       "halted target is an error, not a deferral",
			breaker:    &recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3},
			wantErrors: 1, wantSkipped: 0,
			wantStream: "err", wantSubstr: "stall owner/repo#7:",
		},
		{
			name:       "recovering target is still an ordinary deferral",
			breaker:    &recoveryState{Attempts: 1, WindowStart: recoveryStamp(time.Now())},
			wantErrors: 0, wantSkipped: 1,
			wantStream: "out", wantSubstr: "skip owner/repo#7: workflow agent impl is busy (recovery in progress)\n",
		},
		{
			name:    "plain busy target keeps its byte-unchanged line",
			breaker: nil,
			// The exact pre-4A line: no parenthetical, no state suffix.
			wantErrors: 0, wantSkipped: 1,
			wantStream: "out", wantSubstr: "skip owner/repo#7: workflow agent impl is busy\n",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			realRoot, err := filepath.EvalSymlinks(root)
			if err != nil {
				t.Fatalf("eval symlinks: %v", err)
			}
			fake, store := setupHermeticSessions(t)
			cfg := crossSourceCfg()
			wf := &cfg.Workflows[0]

			notComplete := seedClosedEpic(t, store, config.CloseReasonResetSling)
			fake.present[session.SessionName("impl")] = true // busy
			if c.breaker != nil {
				if err := saveRecoveryState(realRoot, "impl", *c.breaker); err != nil {
					t.Fatalf("plant breaker: %v", err)
				}
			}

			var edits []labelEdit
			var slung, ids []string
			recordLabelEdits(t, &edits)
			recordSlings(t, store, &slung, &ids)

			cmd, outBuf, errBuf := phase3Cmd()
			stats := &dispatchCycleStats{start: time.Now()}
			before := dispatchEntry{
				Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
				PhaseInstanceID: notComplete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour), Attempts: 1,
			}
			state := dispatchState{Dispatched: map[string]dispatchEntry{"owner/repo#7": before}}
			item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
				Labels: labels("agentic", "feature-workflow", "enhancement")}

			handleWorkflowItem(cmd, realRoot, fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

			if stats.errors != c.wantErrors {
				t.Errorf("stats.errors = %d, want %d (stdout=%q stderr=%q)",
					stats.errors, c.wantErrors, outBuf.String(), errBuf.String())
			}
			if stats.skipped != c.wantSkipped {
				t.Errorf("stats.skipped = %d, want %d", stats.skipped, c.wantSkipped)
			}
			got := outBuf.String()
			if c.wantStream == "err" {
				got = errBuf.String()
				if outBuf.Len() != 0 {
					t.Errorf("a halted target must not print a skip line on stdout: %q", outBuf.String())
				}
			}
			if !strings.Contains(got, c.wantSubstr) {
				t.Errorf("%s = %q, want it to contain %q", c.wantStream, got, c.wantSubstr)
			}
			if len(slung) != 0 || len(edits) != 0 {
				t.Errorf("a busy/halted target must not sling (%v) or edit labels (%v)", slung, edits)
			}
			if state.Dispatched["owner/repo#7"] != before {
				t.Errorf("record changed: %+v, want unchanged %+v", state.Dispatched["owner/repo#7"], before)
			}
		})
	}
}

// TestDispatch_NonWorkflowSkipSitesUseTheSharedDecision is a source-level interlock.
// runDispatch's item loop cannot be driven hermetically, so the only mechanical way
// to prove its two busy-skip sites are state-aware is to assert they route through
// the shared helper and that the old unconditional literal is gone. Mirrors the
// TestRunDispatch_NoInlineCrossValidationDuplicate idiom.
func TestDispatch_NonWorkflowSkipSitesUseTheSharedDecision(t *testing.T) {
	data, err := os.ReadFile("dispatch.go")
	if err != nil {
		t.Fatalf("read dispatch.go: %v", err)
	}
	src := string(data)

	if got := strings.Count(src, `"skip %s: agent %s is busy\n"`); got != 0 {
		t.Errorf("the unconditional non-workflow busy-skip literal still appears %d× — "+
			"both sites must render a state-aware line", got)
	}
	if !strings.Contains(src, "dispatchTargetState(root, agent, agentRunning)") {
		t.Error("runDispatch's item loop must resolve the target state through dispatchTargetState")
	}
	if got := strings.Count(src, "stallHaltedTarget(cmd, stats, itemKey, agent)"); got != 2 {
		t.Errorf("stallHaltedTarget is called %d×, want 2 — both non-workflow busy-skip sites "+
			"must route a halted target through the stall shape (stderr + stats.errors++)", got)
	}
}

// TestDispatchStatus_JSON_RecoveryFieldIsAdditive pins the K11 contract rev: the
// field is omitempty, so an agent with nothing to report leaves the entry shape
// byte-identical, and only a latched agent renders the key.
func TestDispatchStatus_JSON_RecoveryFieldIsAdditive(t *testing.T) {
	dir := t.TempDir()
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	afDir := filepath.Join(realDir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1}`+"\n"), 0o644); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	t.Chdir(realDir)
	installMemStore(t)
	installFakeTmuxPresent(t)

	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#1": {Agent: "clean", ItemURL: "u1", Source: "issue", DispatchedAt: time.Now().UTC()},
		"owner/repo#2": {Agent: "latched", ItemURL: "u2", Source: "issue", DispatchedAt: time.Now().UTC()},
	}}
	if err := saveDispatchState(realDir, &state); err != nil {
		t.Fatalf("save dispatch state: %v", err)
	}
	if err := saveRecoveryState(realDir, "latched",
		recoveryState{Halted: true, HaltReason: haltReasonRateCap}); err != nil {
		t.Fatalf("plant breaker: %v", err)
	}

	cmd := &cobra.Command{}
	cmd.Flags().Bool("json", false, "")
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set --json: %v", err)
	}
	var buf strings.Builder
	cmd.SetOut(&buf)
	if err := runDispatchStatus(cmd, nil); err != nil {
		t.Fatalf("runDispatchStatus: %v", err)
	}

	var top struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(buf.String())), &top); err != nil {
		t.Fatalf("unmarshal %q: %v", buf.String(), err)
	}
	if len(top.Entries) != 2 {
		t.Fatalf("want 2 entries, got %d: %s", len(top.Entries), buf.String())
	}
	// entries are sorted by issue key: #1 = clean, #2 = latched.
	if _, ok := top.Entries[0]["recovery"]; ok {
		t.Errorf("an agent with no breaker must OMIT the recovery key (omitempty preserves "+
			"the pinned 6-key non-workflow contract): %s", buf.String())
	}
	raw, ok := top.Entries[1]["recovery"]
	if !ok {
		t.Fatalf("a latched agent must render the recovery key: %s", buf.String())
	}
	var recovery string
	if err := json.Unmarshal(raw, &recovery); err != nil {
		t.Fatalf("recovery is not a string: %v", err)
	}
	if recovery != "halted" {
		t.Errorf("recovery = %q, want %q", recovery, "halted")
	}
}
