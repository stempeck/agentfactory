package cmd

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// ============================================================================
// PR #473 unresolved-review-thread regression tests (issue #461 follow-up).
//   BODY-1: the TERMINAL pr-artifact gate must be dual-source-aware (phaseConsumesPR),
//           not the order-dependent first-match m.Source.
//   BODY-2: resling() must stall immediately on a gate↔sling resolve race, mirroring
//           advance(), instead of burning the attempt ceiling.
// Same harness conventions as dispatch_phase3_test.go / dispatch_source_aware_resolution_test.go:
// package-var seams, no t.Parallel (package globals), no live gh/tmux/sling. Each
// fails on the PR-head by VALUE (not a compile error) when only dispatch.go is reverted.
// ============================================================================

// BODY-1 KEYSTONE: a terminal DUAL-SOURCE phase of an ISSUE-started workflow must reach the
// PR gate. pr-iterate is dual-source with source:issue listed BEFORE source:pr (mirrors
// dispatch.json), so phaseMapping's first match is source:issue and m.Source=="issue". With a
// complete instance and a CLOSED linked PR, only a gate keyed on phaseConsumesPR(mappings, phase)
// stalls; the order-dependent m.Source guard would skip it and return phaseAdvance.
func TestEvaluatePhase_TerminalDualSourceIssueStarted_GatesOnPRState(t *testing.T) {
	store := installMemStore(t)
	cfg := featureWorkflowDualSourceFullCfg() // pr-iterate terminal, source:issue BEFORE source:pr
	wf := &cfg.Workflows[0]

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{42})
	stubItemState(t, "CLOSED")

	if _, ok := phaseMapping(cfg.Mappings, "pr-iterate"); !ok {
		t.Fatal("pr-iterate mapping missing from featureWorkflowDualSourceFullCfg")
	}
	outcome, reason := evaluatePhase(context.Background(), store, "owner/repo", terminalIssueItem(), "issue",
		terminalIterateRecord(complete.ID), cfg.Mappings, wf, "pr-iterate")

	if outcome != phaseStall {
		t.Fatalf("evaluatePhase(terminal dual-source pr-iterate, issue-started, PR CLOSED) = %v, "+
			"want phaseStall — the terminal gate must fire via phaseConsumesPR, not the order-dependent m.Source", outcome)
	}
	if want := `terminal pr phase "pr-iterate": linked PR #42 is closed without merging`; reason != want {
		t.Errorf("stall reason = %q, want %q", reason, want)
	}
}

// BODY-1 companion positive: the same terminal advances while the linked PR is open for the
// operator's review, approved or not and green or not (#755 D1).
func TestEvaluatePhase_TerminalDualSourceIssueStarted_AdvancesWhenPROpen(t *testing.T) {
	store := installMemStore(t)
	cfg := featureWorkflowDualSourceFullCfg()
	wf := &cfg.Workflows[0]

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{42})
	var asked []string
	stubItemStates(t, func(repo string, number int, source string) (string, error) {
		asked = append(asked, fmt.Sprintf("%s#%d/%s", repo, number, source))
		return "OPEN", nil
	})

	outcome, _ := evaluatePhase(context.Background(), store, "owner/repo", terminalIssueItem(), "issue",
		terminalIterateRecord(complete.ID), cfg.Mappings, wf, "pr-iterate")

	if outcome != phaseAdvance {
		t.Fatalf("evaluatePhase(terminal dual-source pr-iterate, issue-started, PR OPEN) = %v, want phaseAdvance", outcome)
	}
	if !equalStrs(asked, []string{"owner/repo#42/pr"}) {
		t.Errorf("ghItemState calls = %v, want the linked PR's state read once as a pr: [owner/repo#42/pr]", asked)
	}
}

// #755 D1 per-state table for a PR-consuming terminal phase: OPEN and MERGED complete, CLOSED and
// any unknown state stall with a reason, and a gh error retries with a reason naming the error.
func TestEvaluatePhase_TerminalPRStateTable(t *testing.T) {
	cases := []struct {
		name       string
		state      string
		err        error
		want       phaseOutcome
		wantReason string
	}{
		{"open_unapproved_red_completes", "OPEN", nil, phaseAdvance, ""},
		{"merged_completes", "MERGED", nil, phaseAdvance, ""},
		{"closed_stalls", "CLOSED", nil, phaseStall, `terminal pr phase "pr-iterate": linked PR #42 is closed without merging`},
		{"unknown_state_stalls", "DRAFTISH", nil, phaseStall, `terminal pr phase "pr-iterate": linked PR #42 has unknown state "DRAFTISH"`},
		{"gh_error_retries", "", errors.New("HTTP 502"), phaseRetry, "HTTP 502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := installMemStore(t)
			cfg := featureWorkflowDualSourceFullCfg()
			complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
			stubLinkedPRs(t, []int{42})
			stubItemStates(t, func(string, int, string) (string, error) { return tc.state, tc.err })

			outcome, reason := evaluatePhase(context.Background(), store, "owner/repo", terminalIssueItem(), "issue",
				terminalIterateRecord(complete.ID), cfg.Mappings, &cfg.Workflows[0], "pr-iterate")

			if outcome != tc.want {
				t.Errorf("outcome = %v, want %v", outcome, tc.want)
			}
			if tc.err != nil {
				if !strings.Contains(reason, tc.wantReason) {
					t.Errorf("retry reason = %q, want it to name the gh error %q", reason, tc.wantReason)
				}
			} else if reason != tc.wantReason {
				t.Errorf("reason = %q, want %q", reason, tc.wantReason)
			}
		})
	}
}

func terminalIssueItem() ghItem {
	return ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "pr-iterate")}
}

func terminalIterateRecord(instanceID string) dispatchEntry {
	return dispatchEntry{
		Agent: "iterate", Workflow: "feature-workflow", Phase: "pr-iterate",
		PhaseInstanceID: instanceID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
	}
}

// BODY-2 KEYSTONE (must FAIL on head): a gate↔sling resolve race in the RESLING path
// (current phase PR-consuming + issue-sourced, instance NOT complete, linked-PR count
// now 0) must route to an immediate detectable stall — NOT burn the attempt ceiling.
// advance() already does this (TestWorkflow_IssueStarted_PRCountRace_Stalls); resling()
// must too. On head resling's error block logs "dispatch failed" and increments Attempts.
func TestResling_ResolvePhaseInputRace_StallsImmediately_DoesNotBurnAttempt(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := featureWorkflowDualSourceCfg() // pr-review dual-source ⇒ phaseConsumesPR("pr-review")==true
	wf := &cfg.Workflows[0]

	open := seedOpenEpic(t, store) // NOT complete ⇒ evaluatePhase ⇒ phaseIncomplete ⇒ resling
	stubLinkedPRs(t, []int{})      // 0 linked PRs ⇒ resolvePhaseInputURL ⇒ errResolvePhaseInput

	const key = "owner/repo#7"
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		key: {
			Agent: "review", Workflow: "feature-workflow", Phase: "pr-review",
			PhaseInstanceID: open.ID, PhaseDispatchedAt: time.Now().Add(-100 * time.Hour), Attempts: 0,
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "pr-review")}

	cmd, _, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if !strings.Contains(errBuf.String(), "stall") {
		t.Errorf("stderr = %q, want a distinctly-named 'stall' — resling's errResolvePhaseInput must route "+
			"through w.stall (like advance()), not the 'dispatch failed' attempt-burning path", errBuf.String())
	}
	// The resolve race is not a real attempt: it must NOT be counted toward the ceiling.
	// (Mirroring advance(), the stall arm leaves the record deleted; tolerate either absent
	// or present-but-not-incremented.)
	if e, ok := state.Dispatched[key]; ok && e.Attempts >= 1 {
		t.Errorf("Attempts after a resolve-race re-sling = %d, want it NOT burned toward the maxWorkflowAttempts ceiling "+
			"(an immediate stall, like advance()'s path)", e.Attempts)
	}
}
