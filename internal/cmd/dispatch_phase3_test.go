package cmd

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/session"
)

// ============================================================================
// Issue #378 Phase 3 — workflow engine acceptance tests. All drive
// handleWorkflowItem (or its leaf helpers) in isolation: ghItem values are built
// directly, completion is seeded via setupHermeticSessions+seedClosedEpic, and
// side effects are asserted through the editItemLabels / dispatchItem /
// ghLinkedPRs / ghPRStatus / sendWorkflowCompleteMail package-var seams. No live
// gh, tmux, or sling. None of these tests may call t.Parallel (package globals).
// ============================================================================

// labelEdit records one editItemLabels call.
type labelEdit struct {
	repo   string
	number int
	source string
	add    []string
	remove []string
}

// phase3Cmd builds a cobra command whose Out/Err are captured buffers.
func phase3Cmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	return cmd, &out, &errBuf
}

// recordLabelEdits swaps editItemLabels to a recorder.
func recordLabelEdits(t *testing.T, edits *[]labelEdit) {
	t.Helper()
	orig := editItemLabels
	editItemLabels = func(repo string, number int, source string, add, remove []string) error {
		*edits = append(*edits, labelEdit{repo, number, source, append([]string(nil), add...), append([]string(nil), remove...)})
		return nil
	}
	t.Cleanup(func() { editItemLabels = orig })
}

// recordSlings swaps dispatchItem to a recorder that, for each call, creates a
// FRESH formula-instance epic in store (so captureInstanceID's freshness gate
// passes) and returns its ID via the sling-stdout marker. The created epic IDs are
// appended to ids in call order, and the slung agent names to slung.
func recordSlings(t *testing.T, store *memstore.Store, slung *[]string, ids *[]string) {
	t.Helper()
	orig := dispatchItem
	dispatchItem = func(root, agent, itemURL, caller, model string) (string, error) {
		*slung = append(*slung, agent)
		iss, err := store.Create(context.Background(), issuestore.CreateParams{
			Title: "Formula: " + agent, Type: issuestore.TypeEpic,
			Labels: []string{"formula-instance"}, Assignee: "mgr",
		})
		if err != nil {
			return "", err
		}
		*ids = append(*ids, iss.ID)
		return fmt.Sprintf(`Formula %q instantiated: %s (3 steps)`, agent, iss.ID), nil
	}
	t.Cleanup(func() { dispatchItem = orig })
}

// recordWorkflowMail swaps sendWorkflowCompleteMail to a recorder.
func recordWorkflowMail(t *testing.T, mails *[]string) {
	t.Helper()
	orig := sendWorkflowCompleteMail
	sendWorkflowCompleteMail = func(recipient, workflow, itemKey string) error {
		*mails = append(*mails, fmt.Sprintf("%s|%s|%s", recipient, workflow, itemKey))
		return nil
	}
	t.Cleanup(func() { sendWorkflowCompleteMail = orig })
}

// stubLinkedPRs swaps ghLinkedPRs to return a fixed PR set.
func stubLinkedPRs(t *testing.T, prs []int) {
	t.Helper()
	orig := ghLinkedPRs
	ghLinkedPRs = func(repo string, issueNumber int) ([]int, error) { return prs, nil }
	t.Cleanup(func() { ghLinkedPRs = orig })
}

// stubPRStatusGreen swaps ghPRStatus to a mergeable+approved+green PR.
func stubPRStatusGreen(t *testing.T) {
	t.Helper()
	orig := ghPRStatus
	ghPRStatus = func(repo string, prNumber int) (prStatus, error) {
		return prStatus{Mergeable: true, Approved: true, ChecksGreen: true}, nil
	}
	t.Cleanup(func() { ghPRStatus = orig })
}

// crossSourceCfg is the canonical AC-2 example: enhancement(issue) → pr-review(pr)
// → pr-iterate(pr). Constructed in-memory (no validator) so the cross-source
// engine is exercised.
func crossSourceCfg() *config.DispatchConfig {
	return &config.DispatchConfig{
		Repos:            []string{"owner/repo"},
		TriggerLabel:     "agentic",
		NotifyOnComplete: "manager",
		RetryAfterSecs:   1800,
		Mappings: []config.DispatchMapping{
			{Labels: []string{"enhancement"}, Source: "issue", Agent: "impl"},
			{Labels: []string{"pr-review"}, Source: "pr", Agent: "review"},
			{Labels: []string{"pr-iterate"}, Source: "pr", Agent: "iterate"},
		},
		Workflows: []config.Workflow{
			{Label: "feature-workflow", Phases: []string{"enhancement", "pr-review", "pr-iterate"}},
		},
	}
}

func labels(names ...string) []ghLabel {
	out := make([]ghLabel, len(names))
	for i, n := range names {
		out[i] = ghLabel{Name: n}
	}
	return out
}

// --- AC 1: TestWorkflow_Bootstrap_AddsFirstPhaseKeepsAgentic (AC-3) ---

func TestWorkflow_Bootstrap_AddsFirstPhaseKeepsAgentic(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	_ = fake
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow")} // no phase label yet

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	// Exactly one label edit: add [enhancement], remove nothing.
	if len(edits) != 1 {
		t.Fatalf("editItemLabels calls = %d, want 1; edits=%+v", len(edits), edits)
	}
	if got := edits[0]; !equalStrs(got.add, []string{"enhancement"}) || len(got.remove) != 0 {
		t.Errorf("bootstrap edit = add %v remove %v, want add [enhancement] remove []", got.add, got.remove)
	}
	// First phase agent slung.
	if !equalStrs(slung, []string{"impl"}) {
		t.Errorf("slung = %v, want [impl]", slung)
	}
	// Record written with workflow correlation, Phase=enhancement, Attempts=0, captured ID.
	entry, ok := state.Dispatched["owner/repo#7"]
	if !ok {
		t.Fatal("no correlation record written")
	}
	if entry.Workflow != "feature-workflow" || entry.Phase != "enhancement" || entry.Attempts != 0 {
		t.Errorf("record = %+v, want Workflow=feature-workflow Phase=enhancement Attempts=0", entry)
	}
	if len(ids) != 1 || entry.PhaseInstanceID != ids[0] {
		t.Errorf("PhaseInstanceID = %q, want captured %v", entry.PhaseInstanceID, ids)
	}
}

// --- AC 1: TestWorkflow_AdvanceOnTerminalInstance (AC-2) ---

func TestWorkflow_AdvanceOnTerminalInstance(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	// enhancement instance is genuinely complete.
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	// enhancement→pr-review is an issue→pr handoff: needs exactly one linked PR.
	stubLinkedPRs(t, []int{42})

	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour), Attempts: 2,
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}
	_ = fake

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	// One batched edit: add [pr-review], remove [enhancement].
	if len(edits) != 1 {
		t.Fatalf("editItemLabels calls = %d, want 1; %+v", len(edits), edits)
	}
	if got := edits[0]; !equalStrs(got.add, []string{"pr-review"}) || !equalStrs(got.remove, []string{"enhancement"}) {
		t.Errorf("advance edit = add %v remove %v, want add [pr-review] remove [enhancement]", got.add, got.remove)
	}
	if !equalStrs(slung, []string{"review"}) {
		t.Errorf("slung = %v, want [review]", slung)
	}
	entry := state.Dispatched["owner/repo#7"]
	if entry.Phase != "pr-review" || entry.Attempts != 0 {
		t.Errorf("record after advance = %+v, want Phase=pr-review Attempts=0 (reset)", entry)
	}
	if entry.PhaseInstanceID == complete.ID {
		t.Errorf("PhaseInstanceID still the OLD completed instance %q — must be cleared/replaced on advance", complete.ID)
	}
}

// --- AC 2: TestWorkflow_TerminalPhase_RemovesAgentic (AC-2 step4 / C-8 / LOW-3) ---

func TestWorkflow_TerminalPhase_RemovesAgentic(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubPRStatusGreen(t) // terminal pr gate: mergeable+approved+green

	var edits []labelEdit
	var slung, ids []string
	var mails []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)
	recordWorkflowMail(t, &mails)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	// Item is the PR itself for the last (pr-source) phase.
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#99": {
			Agent: "iterate", Workflow: "feature-workflow", Phase: "pr-iterate",
			PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
		},
	}}
	item := ghItem{Number: 99, URL: "https://github.com/owner/repo/pull/99",
		Labels: labels("agentic", "feature-workflow", "pr-iterate")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "pr", wf)

	// One edit removing the last phase label AND agentic together (add nothing).
	if len(edits) != 1 {
		t.Fatalf("editItemLabels calls = %d, want 1; %+v", len(edits), edits)
	}
	if got := edits[0]; len(got.add) != 0 || !equalStrs(got.remove, []string{"pr-iterate", "agentic"}) {
		t.Errorf("terminal edit = add %v remove %v, want add [] remove [pr-iterate agentic]", got.add, got.remove)
	}
	// Exactly one workflow-complete mail.
	if len(mails) != 1 {
		t.Fatalf("workflow-complete mails = %d, want exactly 1; %v", len(mails), mails)
	}
	if !strings.HasPrefix(mails[0], "manager|feature-workflow|") {
		t.Errorf("mail = %q, want recipient=manager workflow=feature-workflow", mails[0])
	}
	// No further sling; record dropped.
	if len(slung) != 0 {
		t.Errorf("slung = %v, want none on terminal", slung)
	}
	if _, ok := state.Dispatched["owner/repo#99"]; ok {
		t.Errorf("correlation record should be dropped on terminal completion")
	}
}

// --- AC 3: TestWorkflow_EndToEnd_AdvancesToCompletion (cross-source AC-2) ---

func TestWorkflow_EndToEnd_AdvancesToCompletion(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]
	root := t.TempDir()
	const key = "owner/repo#7"

	stubLinkedPRs(t, []int{42}) // the single linked PR for the cross-source gates
	stubPRStatusGreen(t)

	var edits []labelEdit
	var slung, ids []string
	var mails []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)
	recordWorkflowMail(t, &mails)

	// Live label set on the (issue) tracking item; editItemLabels applies to it.
	live := map[string]bool{"agentic": true, "feature-workflow": true}
	origEdit := editItemLabels
	editItemLabels = func(repo string, number int, source string, add, remove []string) error {
		edits = append(edits, labelEdit{repo, number, source, append([]string(nil), add...), append([]string(nil), remove...)})
		for _, l := range remove {
			delete(live, l)
		}
		for _, l := range add {
			live[l] = true
		}
		return nil
	}
	t.Cleanup(func() { editItemLabels = origEdit })

	state := dispatchState{Dispatched: map[string]dispatchEntry{}}
	cmd, _, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}

	itemFromLive := func() ghItem {
		var ls []ghLabel
		for n := range live {
			ls = append(ls, ghLabel{Name: n})
		}
		return ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7", Labels: ls}
	}

	// Drive cycles until agentic is removed (terminal) or a safety bound.
	for cycle := 0; cycle < 8 && live["agentic"]; cycle++ {
		handleWorkflowItem(cmd, root, fake, &state, stats, cfg, "owner/repo", itemFromLive(), "issue", wf)
		// Between cycles, the slung phase's formula "finishes": close its instance
		// with the completion reason so the next cycle observes a terminal instance.
		if e, ok := state.Dispatched[key]; ok && e.PhaseInstanceID != "" {
			_ = store.Close(context.Background(), e.PhaseInstanceID, config.CloseReasonFormulaComplete)
		}
	}

	if live["agentic"] {
		t.Fatalf("pipeline did not reach terminal: live labels still %v\nstderr=%s\nedits=%+v", live, errBuf.String(), edits)
	}
	// All three phase agents were slung in order.
	if !equalStrs(slung, []string{"impl", "review", "iterate"}) {
		t.Errorf("slung order = %v, want [impl review iterate]", slung)
	}
	// Exactly one terminal completion mail.
	if len(mails) != 1 {
		t.Errorf("completion mails = %d, want 1; %v", len(mails), mails)
	}
	// Final live labels: agentic + last phase removed; workflow label retained.
	if live["agentic"] || live["pr-iterate"] {
		t.Errorf("after completion live=%v, want agentic and pr-iterate removed", live)
	}
	if !live["feature-workflow"] {
		t.Errorf("workflow label should be retained after completion; live=%v", live)
	}
}

// --- AC 3: TestWorkflow_AmbiguousCursor_Stalls (LOW-1) ---

func TestWorkflow_AmbiguousCursor_Stalls(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	_ = store
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)

	cmd, _, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{}}
	// TWO phase labels present ⇒ ambiguous cursor.
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement", "pr-review")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if stats.errors != 1 {
		t.Errorf("stats.errors = %d, want 1 (detectable stall)", stats.errors)
	}
	if !strings.Contains(errBuf.String(), "stall") || !strings.Contains(errBuf.String(), "ambiguous") {
		t.Errorf("stderr = %q, want a distinctly-named ambiguous-cursor stall", errBuf.String())
	}
	if len(edits) != 0 || len(slung) != 0 {
		t.Errorf("ambiguous cursor must not edit labels (%v) or sling (%v)", edits, slung)
	}
}

// --- AC 4: TestDispatch_NonWorkflowItem_Unchanged (C-10) ---

func TestDispatch_NonWorkflowItem_Unchanged(t *testing.T) {
	cfg := crossSourceCfg()

	// A non-workflow item (no workflow label) is NOT recognized → falls through.
	nonWf := ghItem{Number: 1, Labels: labels("agentic", "bug")}
	if wf := matchWorkflow(nonWf, cfg.Workflows); wf != nil {
		t.Errorf("matchWorkflow(non-workflow item) = %+v, want nil (must fall through to unchanged path)", wf)
	}
	// An item carrying only a phase label but NOT the workflow label is also not a
	// workflow item (recognition is the exact workflow label, C-9).
	phaseOnly := ghItem{Number: 2, Labels: labels("agentic", "enhancement")}
	if wf := matchWorkflow(phaseOnly, cfg.Workflows); wf != nil {
		t.Errorf("matchWorkflow(phase-label-only item) = %+v, want nil", wf)
	}
	// With NO workflows configured, nothing is ever a workflow item (C-10 default).
	if wf := matchWorkflow(ghItem{Labels: labels("agentic", "feature-workflow")}, nil); wf != nil {
		t.Errorf("matchWorkflow with no workflows = %+v, want nil", wf)
	}
	// A real workflow item IS recognized (so the pre-branch owns it, not the old path).
	wfItem := ghItem{Number: 3, Labels: labels("agentic", "feature-workflow")}
	if wf := matchWorkflow(wfItem, cfg.Workflows); wf == nil || wf.Label != "feature-workflow" {
		t.Errorf("matchWorkflow(workflow item) = %+v, want the feature-workflow", wf)
	}
}

// --- AC 4: TestWorkflow_NotComplete_IdleAgent_ReslingsSamePhase_ClearsRecord (#413 CRIT-2) ---

func TestWorkflow_NotComplete_IdleAgent_ReslingsSamePhase_ClearsRecord(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	// A reset-closed instance is terminal but NOT complete (provenance guard).
	notComplete := seedClosedEpic(t, store, config.CloseReasonResetSling)

	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID:   notComplete.ID,
			PhaseDispatchedAt: time.Now().Add(-time.Hour), // beyond the retry window
			Attempts:          1,
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}
	_ = fake

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	// No advance: labels untouched.
	if len(edits) != 0 {
		t.Errorf("re-sling must NOT edit labels (no false advance); edits=%+v", edits)
	}
	// Re-slung the SAME phase, with Attempts incremented and a fresh instance.
	if !equalStrs(slung, []string{"impl"}) {
		t.Errorf("slung = %v, want [impl] (re-sling same phase)", slung)
	}
	entry := state.Dispatched["owner/repo#7"]
	if entry.Phase != "enhancement" || entry.Attempts != 2 {
		t.Errorf("record = %+v, want Phase=enhancement Attempts=2", entry)
	}
	if entry.PhaseInstanceID == notComplete.ID {
		t.Errorf("PhaseInstanceID still the OLD reset instance %q — re-sling must clear and re-capture", notComplete.ID)
	}
}

// --- AC 4: TestWorkflow_BusyAgent_SkipsNonBlocking (AC-5 / C-13) ---

func TestWorkflow_BusyAgent_SkipsNonBlocking(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	notComplete := seedClosedEpic(t, store, config.CloseReasonResetSling)
	fake.present[session.SessionName("impl")] = true // enhancement agent is BUSY

	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	before := dispatchEntry{
		Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
		PhaseInstanceID: notComplete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour), Attempts: 1,
	}
	state := dispatchState{Dispatched: map[string]dispatchEntry{"owner/repo#7": before}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if stats.skipped != 1 {
		t.Errorf("stats.skipped = %d, want 1 (busy ⇒ non-blocking skip)", stats.skipped)
	}
	if len(slung) != 0 || len(edits) != 0 {
		t.Errorf("busy item must not sling (%v) or edit labels (%v)", slung, edits)
	}
	if state.Dispatched["owner/repo#7"] != before {
		t.Errorf("record changed on busy skip: %+v, want unchanged %+v", state.Dispatched["owner/repo#7"], before)
	}
}

// --- AC 5: TestWorkflow_Item_NeverTouchesNonWorkflowRetryWindow (HIGH-3 round 2) ---

func TestWorkflow_Item_NeverTouchesNonWorkflowRetryWindow(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	// (1) A workflow item is caught by the pre-branch, so it never reaches the
	//     non-workflow retry-window/record code below it in the loop.
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}
	if matchWorkflow(item, cfg.Workflows) == nil {
		t.Fatal("workflow item not recognized by the pre-branch — would fall into the non-workflow path")
	}

	// (2) A completable workflow item whose record sits WITHIN the non-workflow
	//     retry window is still processed by the workflow path (it owns its own
	//     logic and is NOT skipped by the shared retry-window).
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{42})
	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID:   complete.ID,
			DispatchedAt:      time.Now(), // within the non-workflow retry window
			PhaseDispatchedAt: time.Now(),
		},
	}}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	// The workflow path advanced (it did NOT honor the non-workflow retry window),
	// and the record it wrote is a WORKFLOW record (Workflow set), never the bare
	// non-workflow dispatchEntry{Agent,DispatchedAt,ItemURL,Source}.
	if len(edits) != 1 {
		t.Fatalf("expected the workflow path to advance despite the within-window record; edits=%+v", edits)
	}
	entry := state.Dispatched["owner/repo#7"]
	if entry.Workflow == "" {
		t.Errorf("record after handling lacks workflow correlation (%+v) — the non-workflow record path was taken", entry)
	}
}

// --- AC 6: TestWorkflow_PhaseResolvesToNoAgent_DetectableStall (CRITICAL-2 runtime) ---

func TestWorkflow_PhaseResolvesToNoAgent_DetectableStall(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	_ = store
	// Workflow has a phase "ghost" with NO single-label mapping.
	cfg := &config.DispatchConfig{
		Repos: []string{"owner/repo"}, TriggerLabel: "agentic", NotifyOnComplete: "manager",
		RetryAfterSecs: 1800,
		Mappings: []config.DispatchMapping{
			{Labels: []string{"enhancement"}, Source: "issue", Agent: "impl"},
		},
		Workflows: []config.Workflow{
			{Label: "feature-workflow", Phases: []string{"enhancement", "ghost"}},
		},
	}
	wf := &cfg.Workflows[0]

	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)

	cmd, _, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{}}
	// Item is in-flight on the unresolvable "ghost" phase.
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "ghost")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if stats.errors != 1 {
		t.Errorf("stats.errors = %d, want 1 (detectable stall, not silent skip)", stats.errors)
	}
	if !strings.Contains(errBuf.String(), "stall") || !strings.Contains(errBuf.String(), "no agent") {
		t.Errorf("stderr = %q, want a distinctly-named no-agent stall", errBuf.String())
	}
	if len(slung) != 0 || len(edits) != 0 {
		t.Errorf("no-agent stall must not sling (%v) or edit labels (%v)", slung, edits)
	}
}

// --- AC 6b: TestWorkflow_PhaseAgent_DirectLabelLookup_NotShadowedByWorkflowLabelMapping (HIGH-B) ---

func TestWorkflow_PhaseAgent_DirectLabelLookup_NotShadowedByWorkflowLabelMapping(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	wf := &config.Workflow{Label: "feature-workflow", Phases: []string{"enhancement"}}
	cfg := &config.DispatchConfig{
		Repos: []string{"owner/repo"}, TriggerLabel: "agentic", NotifyOnComplete: "manager",
		RetryAfterSecs: 1800,
		// A mapping keyed on the BARE workflow label is listed FIRST: matchItemToAgent
		// on the full live label set would return "WRONG" via first-match-wins.
		Mappings: []config.DispatchMapping{
			{Labels: []string{"feature-workflow"}, Source: "issue", Agent: "WRONG"},
			{Labels: []string{"enhancement"}, Source: "issue", Agent: "impl"},
		},
		Workflows: []config.Workflow{*wf},
	}
	wf = &cfg.Workflows[0]

	// Sanity: the shadowing first-match path WOULD mis-route.
	shadowItem := ghItem{Labels: labels("agentic", "feature-workflow", "enhancement")}
	if got, _ := matchItemToAgent(shadowItem, cfg.Mappings); got != "WRONG" {
		t.Fatalf("precondition: matchItemToAgent first-match = %q, want WRONG (shadow setup)", got)
	}

	// In-flight, not complete, idle ⇒ re-sling resolves the phase agent by DIRECT lookup.
	notComplete := seedClosedEpic(t, store, config.CloseReasonResetSling)
	var slung, ids []string
	var edits []labelEdit
	recordSlings(t, store, &slung, &ids)
	recordLabelEdits(t, &edits)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID: notComplete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if !equalStrs(slung, []string{"impl"}) {
		t.Errorf("slung = %v, want [impl] — phase agent must be resolved by DIRECT phase-label lookup, not shadowed to WRONG", slung)
	}
}

// --- W-7: stall ceiling exceeded ⇒ detectable stall, no further re-sling ---

func TestWorkflow_StallCeiling_Exceeded(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	notComplete := seedClosedEpic(t, store, config.CloseReasonResetSling)
	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)

	cmd, _, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID:   notComplete.ID,
			PhaseDispatchedAt: time.Now().Add(-time.Hour), // beyond the retry window
			Attempts:          maxWorkflowAttempts,        // already at the ceiling
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if stats.errors != 1 {
		t.Errorf("stats.errors = %d, want 1 (stall ceiling exceeded ⇒ detectable stall)", stats.errors)
	}
	if !strings.Contains(errBuf.String(), "stall") || !strings.Contains(errBuf.String(), "ceiling") {
		t.Errorf("stderr = %q, want a distinctly-named stall-ceiling message", errBuf.String())
	}
	if len(slung) != 0 || len(edits) != 0 {
		t.Errorf("ceiling exceeded must NOT re-sling (%v) or edit labels (%v)", slung, edits)
	}
}

// --- HIGH-A: terminal pr phase complete but PR not green ⇒ WAIT (defer), not advance/stall ---

func TestWorkflow_TerminalPrNotGreen_Waits(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	// PR is mergeable + green but NOT approved ⇒ prArtifactComplete == false.
	orig := ghPRStatus
	ghPRStatus = func(repo string, prNumber int) (prStatus, error) {
		return prStatus{Mergeable: true, Approved: false, ChecksGreen: true}, nil
	}
	t.Cleanup(func() { ghPRStatus = orig })

	var edits []labelEdit
	var slung, ids []string
	var mails []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)
	recordWorkflowMail(t, &mails)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	before := dispatchEntry{
		Agent: "iterate", Workflow: "feature-workflow", Phase: "pr-iterate",
		PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
	}
	state := dispatchState{Dispatched: map[string]dispatchEntry{"owner/repo#99": before}}
	item := ghItem{Number: 99, URL: "https://github.com/owner/repo/pull/99",
		Labels: labels("agentic", "feature-workflow", "pr-iterate")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "pr", wf)

	// A completed formula whose terminal artifact is not yet ready waits — no
	// advance, no mail, no re-sling (re-slinging a done formula would --reset it).
	if len(edits) != 0 || len(mails) != 0 || len(slung) != 0 {
		t.Errorf("terminal-not-green must WAIT: edits=%v mails=%v slung=%v (want none)", edits, mails, slung)
	}
	if stats.skipped != 1 {
		t.Errorf("stats.skipped = %d, want 1 (phaseWait defers to next cycle)", stats.skipped)
	}
	if stats.errors != 0 {
		t.Errorf("terminal-not-green is a WAIT, not a stall; stats.errors=%d, want 0", stats.errors)
	}
	if state.Dispatched["owner/repo#99"] != before {
		t.Errorf("record must remain unchanged on wait, got %+v", state.Dispatched["owner/repo#99"])
	}
}

// --- K7 cursor: side-effect-free leaf, table-tested directly ---

func TestWorkflowCursor(t *testing.T) {
	phases := []string{"enhancement", "pr-review", "pr-iterate"}
	cases := []struct {
		name      string
		labels    []string
		want      string
		ambiguous bool
	}{
		{"no_phase_label_bootstrap", []string{"agentic", "feature-workflow"}, "", false},
		{"single_phase", []string{"agentic", "feature-workflow", "pr-review"}, "pr-review", false},
		{"first_phase", []string{"agentic", "enhancement"}, "enhancement", false},
		{"two_phases_ambiguous", []string{"enhancement", "pr-iterate"}, "", true},
		{"all_three_ambiguous", []string{"enhancement", "pr-review", "pr-iterate"}, "", true},
		{"unrelated_labels_only", []string{"agentic", "bug"}, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, amb := workflowCursor(ghItem{Labels: labels(tc.labels...)}, phases)
			if got != tc.want || amb != tc.ambiguous {
				t.Errorf("workflowCursor(%v) = (%q, %v), want (%q, %v)", tc.labels, got, amb, tc.want, tc.ambiguous)
			}
		})
	}
}

// --- Change 5: workflow-aware prune keeps active pipeline records (Gap 6) ---

func TestPruneDispatchState_WorkflowAware(t *testing.T) {
	old := time.Now().Add(-48 * time.Hour)
	state := &dispatchState{Dispatched: map[string]dispatchEntry{
		// Non-workflow, stale ⇒ pruned (unchanged behavior, C-10).
		"owner/repo#1": {Agent: "debugger", DispatchedAt: old},
		// Workflow, stale ⇒ KEPT (active pipeline, Gap 6).
		"owner/repo#2": {Agent: "impl", DispatchedAt: old, Workflow: "feature-workflow", Phase: "enhancement"},
	}}
	pruneDispatchState(state)
	if _, ok := state.Dispatched["owner/repo#1"]; ok {
		t.Errorf("stale non-workflow entry should be pruned")
	}
	if _, ok := state.Dispatched["owner/repo#2"]; !ok {
		t.Errorf("stale-but-active workflow entry should be KEPT (workflow-aware prune)")
	}
}

// --- W-6 response parsing: pins the exact spike JSON shape hermetically ---

func TestParseLinkedPRs(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []int
	}{
		{"one_linked_pr", `{"data":{"repository":{"issue":{"closedByPullRequestsReferences":{"nodes":[{"number":439}]}}}}}`, []int{439}},
		{"none", `{"data":{"repository":{"issue":{"closedByPullRequestsReferences":{"nodes":[]}}}}}`, []int{}},
		{"many", `{"data":{"repository":{"issue":{"closedByPullRequestsReferences":{"nodes":[{"number":42},{"number":43}]}}}}}`, []int{42, 43}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseLinkedPRs([]byte(tc.in))
			if err != nil {
				t.Fatalf("parseLinkedPRs: %v", err)
			}
			if !equalInts(got, tc.want) {
				t.Errorf("parseLinkedPRs = %v, want %v", got, tc.want)
			}
		})
	}
}

// --- small comparison helpers ---

func equalStrs(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
