package cmd

import (
	"context"
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

// BODY-1 KEYSTONE (must FAIL on head): a terminal DUAL-SOURCE phase of an
// ISSUE-started workflow must gate advancement on PR merge-readiness. pr-iterate is
// dual-source with source:issue listed BEFORE source:pr (mirrors dispatch.json), so
// phaseMapping's first match is source:issue and m.Source=="issue". With a complete
// instance but a NOT-merge-ready PR, head SKIPS the terminal gate (m.Source!="pr")
// and returns phaseAdvance — silently terminating an un-merge-ready PR. The fix gates
// on phaseConsumesPR(mappings, phase) and returns phaseWait.
func TestEvaluatePhase_TerminalDualSourceIssueStarted_GatesOnPRMergeReadiness(t *testing.T) {
	store := installMemStore(t)
	cfg := featureWorkflowDualSourceFullCfg() // pr-iterate terminal, source:issue BEFORE source:pr
	wf := &cfg.Workflows[0]

	// The terminal phase's own instance is genuinely complete (non-empty pointer ⇒
	// the latch path is not taken; instanceComplete gates on the close reason only).
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{42}) // the issue resolves to exactly one linked PR

	// The linked PR is NOT merge-ready (mergeable + green but NOT approved) ⇒
	// prArtifactComplete is false ⇒ a fired gate must return phaseWait.
	orig := ghPRStatus
	ghPRStatus = func(repo string, prNumber int) (prStatus, error) {
		return prStatus{Mergeable: true, Approved: false, ChecksGreen: true}, nil
	}
	t.Cleanup(func() { ghPRStatus = orig })

	// pr-iterate's FIRST mapping is source:issue (listed before source:pr) — the exact
	// shape that made the old m.Source guard skip the gate.
	if _, ok := phaseMapping(cfg.Mappings, "pr-iterate"); !ok {
		t.Fatal("pr-iterate mapping missing from featureWorkflowDualSourceFullCfg")
	}
	entry := dispatchEntry{
		Agent: "iterate", Workflow: "feature-workflow", Phase: "pr-iterate",
		PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
	}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "pr-iterate")}

	outcome, _ := evaluatePhase(context.Background(), store, "owner/repo", item, "issue",
		entry, cfg.Mappings, wf, "pr-iterate")

	if outcome != phaseWait {
		t.Fatalf("evaluatePhase(terminal dual-source pr-iterate, issue-started, PR not merge-ready) = %v, "+
			"want phaseWait — the terminal gate must fire via phaseConsumesPR, not the order-dependent m.Source. "+
			"(FAILS on head: m.Source==\"issue\" skips the gate ⇒ phaseAdvance.)", outcome)
	}
}

// BODY-1 companion positive: the same terminal dual-source pr-iterate advances ONLY
// when the linked PR is genuinely merge-ready. Documents the happy path the gate guards.
func TestEvaluatePhase_TerminalDualSourceIssueStarted_AdvancesWhenPRMergeReady(t *testing.T) {
	store := installMemStore(t)
	cfg := featureWorkflowDualSourceFullCfg()
	wf := &cfg.Workflows[0]

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{42})
	stubPRStatusGreen(t) // mergeable + approved + green

	if _, ok := phaseMapping(cfg.Mappings, "pr-iterate"); !ok {
		t.Fatal("pr-iterate mapping missing from featureWorkflowDualSourceFullCfg")
	}
	entry := dispatchEntry{
		Agent: "iterate", Workflow: "feature-workflow", Phase: "pr-iterate",
		PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
	}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "pr-iterate")}

	outcome, _ := evaluatePhase(context.Background(), store, "owner/repo", item, "issue",
		entry, cfg.Mappings, wf, "pr-iterate")

	if outcome != phaseAdvance {
		t.Fatalf("evaluatePhase(terminal dual-source pr-iterate, issue-started, PR merge-ready) = %v, want phaseAdvance", outcome)
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
