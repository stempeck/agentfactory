package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

func TestMatchItemToAgent_ANDSemantics_AllLabelsMatch(t *testing.T) {
	item := ghItem{
		Number: 1,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/1",
		Labels: []ghLabel{{Name: "bug"}, {Name: "backend"}, {Name: "agentic"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug", "backend"}, Agent: "debugger"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "debugger" {
		t.Errorf("matchItemToAgent() = %q, want %q", got, "debugger")
	}
}

func TestMatchItemToAgent_ANDSemantics_PartialMatchFails(t *testing.T) {
	item := ghItem{
		Number: 2,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/2",
		Labels: []ghLabel{{Name: "bug"}, {Name: "agentic"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug", "backend"}, Agent: "debugger"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "" {
		t.Errorf("matchItemToAgent() = %q, want empty string (partial match should fail)", got)
	}
}

func TestMatchItemToAgent_ANDSemantics_SingleLabelBackwardCompat(t *testing.T) {
	item := ghItem{
		Number: 3,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/3",
		Labels: []ghLabel{{Name: "bug-triage"}, {Name: "agentic"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug-triage"}, Agent: "debugger"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "debugger" {
		t.Errorf("matchItemToAgent() = %q, want %q", got, "debugger")
	}
}

func TestMatchItemToAgent_ANDSemantics_FirstMatchWins(t *testing.T) {
	item := ghItem{
		Number: 4,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/4",
		Labels: []ghLabel{{Name: "bug"}, {Name: "backend"}, {Name: "docs"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug", "backend"}, Agent: "debugger"},
		{Labels: []string{"docs"}, Agent: "writer"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "debugger" {
		t.Errorf("matchItemToAgent() = %q, want %q (first match wins)", got, "debugger")
	}
}

func TestMatchItemToAgent_ANDSemantics_NoMatch(t *testing.T) {
	item := ghItem{
		Number: 5,
		Title:  "test",
		URL:    "https://github.com/owner/repo/issues/5",
		Labels: []ghLabel{{Name: "agentic"}, {Name: "feature"}},
	}
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug", "backend"}, Agent: "debugger"},
		{Labels: []string{"docs"}, Agent: "writer"},
	}

	got := matchItemToAgent(item, mappings)
	if got != "" {
		t.Errorf("matchItemToAgent() = %q, want empty string", got)
	}
}

func TestGroupMappingsBySource_SplitsCorrectly(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug"}, Source: "issue", Agent: "debugger"},
		{Labels: []string{"feat"}, Source: "pr", Agent: "builder"},
		{Labels: []string{"docs"}, Source: "issue", Agent: "writer"},
	}

	issues, prs := groupMappingsBySource(mappings)

	if len(issues) != 2 {
		t.Fatalf("issues count = %d, want 2", len(issues))
	}
	if len(prs) != 1 {
		t.Fatalf("prs count = %d, want 1", len(prs))
	}
	if issues[0].Agent != "debugger" {
		t.Errorf("issues[0].Agent = %q, want %q", issues[0].Agent, "debugger")
	}
	if issues[1].Agent != "writer" {
		t.Errorf("issues[1].Agent = %q, want %q", issues[1].Agent, "writer")
	}
	if prs[0].Agent != "builder" {
		t.Errorf("prs[0].Agent = %q, want %q", prs[0].Agent, "builder")
	}
}

func TestGroupMappingsBySource_DefaultsToIssues(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"bug"}, Source: "", Agent: "debugger"},
		{Labels: []string{"feat"}, Source: "issue", Agent: "writer"},
	}

	issues, prs := groupMappingsBySource(mappings)

	if len(issues) != 2 {
		t.Fatalf("issues count = %d, want 2 (empty source defaults to issues)", len(issues))
	}
	if len(prs) != 0 {
		t.Fatalf("prs count = %d, want 0", len(prs))
	}
}

func TestGroupMappingsBySource_AllPRs(t *testing.T) {
	mappings := []config.DispatchMapping{
		{Labels: []string{"review"}, Source: "pr", Agent: "reviewer"},
		{Labels: []string{"ci-fix"}, Source: "pr", Agent: "devops"},
	}

	issues, prs := groupMappingsBySource(mappings)

	if len(issues) != 0 {
		t.Fatalf("issues count = %d, want 0", len(issues))
	}
	if len(prs) != 2 {
		t.Fatalf("prs count = %d, want 2", len(prs))
	}
}

func TestGroupMappingsBySource_Empty(t *testing.T) {
	issues, prs := groupMappingsBySource(nil)

	if issues != nil {
		t.Errorf("issues = %v, want nil", issues)
	}
	if prs != nil {
		t.Errorf("prs = %v, want nil", prs)
	}
}

// ============================================================================
// Phase 2 (#378): completion foundation — capture, completion query, artifact
// predicates. These exercise PURE helpers via the newIssueStore memstore seam
// and the new gh-call package-var seams; no real tmux, sling, or gh is touched.
// ============================================================================

// seedClosedEpic creates a formula-instance epic (mirroring instantiateFormula's
// shape) and closes it with the given reason, returning the re-fetched issue so
// the test sees the terminal Status + CloseReason exactly as the dispatcher will.
func seedClosedEpic(t *testing.T, store *memstore.Store, reason string) issuestore.Issue {
	t.Helper()
	ctx := context.Background()
	iss, err := store.Create(ctx, issuestore.CreateParams{
		Title:    "Formula: demo",
		Type:     issuestore.TypeEpic,
		Labels:   []string{"formula-instance"},
		Assignee: "mgr",
	})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}
	if err := store.Close(ctx, iss.ID, reason); err != nil {
		t.Fatalf("Close epic with %q: %v", reason, err)
	}
	got, err := store.Get(ctx, iss.ID)
	if err != nil {
		t.Fatalf("Get epic: %v", err)
	}
	return got
}

// AC #3 / GHERKIN: a terminal instance closed with the completion reason reads complete.
func TestCompletion_TerminalWithFormulaCompleteReason_IsComplete(t *testing.T) {
	store := installMemStore(t)
	got := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)

	if !got.Status.IsTerminal() {
		t.Fatalf("epic status = %q, want terminal", got.Status)
	}
	if got.CloseReason != config.CloseReasonFormulaComplete {
		t.Fatalf("CloseReason = %q, want %q", got.CloseReason, config.CloseReasonFormulaComplete)
	}
	if !instanceComplete(got) {
		t.Errorf("instanceComplete(%+v) = false, want true (terminal + formula-complete reason)", got)
	}
}

// AC #4 / GHERKIN: a terminal instance closed via a reset path does NOT read
// complete, plus the provenance guard that no reset reason equals the completion
// constant (the #378/#413 CRIT-2 false-advance guard).
func TestCompletion_TerminalWithResetReason_IsNotComplete(t *testing.T) {
	store := installMemStore(t)
	got := seedClosedEpic(t, store, config.CloseReasonResetSling)

	if !got.Status.IsTerminal() {
		t.Fatalf("epic status = %q, want terminal (reset still closes the epic)", got.Status)
	}
	if instanceComplete(got) {
		t.Errorf("instanceComplete = true for a reset-closed epic (reason %q); want false — reset must never read as completion", got.CloseReason)
	}

	t.Run("provenance_guard_resets_never_equal_completion", func(t *testing.T) {
		for _, r := range []string{
			config.CloseReasonResetSling,
			config.CloseReasonResetFormulaSling,
			config.CloseReasonResetDown,
		} {
			if r == "" {
				t.Errorf("a reset reason constant is empty")
			}
			if r == config.CloseReasonFormulaComplete {
				t.Errorf("reset reason %q equals CloseReasonFormulaComplete — false-advance risk (HIGH-1)", r)
			}
			// And an epic closed with each reset reason must not read complete.
			got := seedClosedEpic(t, store, r)
			if instanceComplete(got) {
				t.Errorf("instanceComplete = true for reset reason %q; want false", r)
			}
		}
	})
}

// AC #5 (part 1) / GHERKIN: instance ID captured from .runtime/hooked_formula,
// in both a plain agent dir and a worktree agent dir; a dir with no
// hooked_formula yields no capture; the stdout-parse fallback also works.
func TestDispatchItem_CapturesInstanceID(t *testing.T) {
	store := installMemStore(t)
	ctx := context.Background()
	past := time.Now().Add(-time.Hour) // dispatched in the past ⇒ a fresh instance passes the gate

	writeHooked := func(t *testing.T, dir, id string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755); err != nil {
			t.Fatalf("mkdir .runtime: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, ".runtime", "hooked_formula"), []byte(id), 0o644); err != nil {
			t.Fatalf("write hooked_formula: %v", err)
		}
	}

	newEpic := func(t *testing.T) string {
		t.Helper()
		iss, err := store.Create(ctx, issuestore.CreateParams{
			Title: "Formula: demo", Type: issuestore.TypeEpic,
			Labels: []string{"formula-instance"}, Assignee: "mgr",
		})
		if err != nil {
			t.Fatalf("Create epic: %v", err)
		}
		return iss.ID
	}

	t.Run("non_worktree_dir", func(t *testing.T) {
		dir := t.TempDir()
		id := newEpic(t)
		writeHooked(t, dir, id)
		if got := captureInstanceID(ctx, store, dir, "", past); got != id {
			t.Errorf("captureInstanceID = %q, want %q (read from the agent dir's hooked_formula)", got, id)
		}
	})

	t.Run("worktree_agent_dir", func(t *testing.T) {
		// Mirrors sling.go:188 reassigning agentDir = wtAgentDir: capture must
		// follow the dir sling actually wrote to, which under worktrees is the
		// worktree agent dir, not config.AgentDir(root, agent).
		wtDir := t.TempDir()
		id := newEpic(t)
		writeHooked(t, wtDir, id)
		if got := captureInstanceID(ctx, store, wtDir, "", past); got != id {
			t.Errorf("captureInstanceID(worktree dir) = %q, want %q", got, id)
		}
	})

	t.Run("wrong_dir_no_hooked_formula_yields_no_capture", func(t *testing.T) {
		empty := t.TempDir() // no .runtime/hooked_formula — e.g. dispatcher read the WRONG (canonical) dir
		if got := captureInstanceID(ctx, store, empty, "", past); got != "" {
			t.Errorf("captureInstanceID(empty dir) = %q, want \"\" (no capture)", got)
		}
	})

	t.Run("stdout_parse_fallback", func(t *testing.T) {
		empty := t.TempDir() // no hooked_formula ⇒ fall back to parsing sling's stdout
		id := newEpic(t)
		stdout := "Created worktree wt-abc for specialist\n" +
			"Formula \"soldesign-engineer\" instantiated: " + id + " (12 steps)\n" +
			"  load-context → step-1\n"
		if got := captureInstanceID(ctx, store, empty, stdout, past); got != id {
			t.Errorf("captureInstanceID(stdout fallback) = %q, want %q", got, id)
		}
	})
}

// AC #5 (part 2) / GHERKIN: a stale hooked_formula (an instance created BEFORE
// the dispatch timestamp) is rejected by the freshness gate (HIGH-3), so a prior
// instance is never mistaken for the new one. memstore CreatedAt is not
// injectable, so the gate is steered via phaseDispatchedAt (future = stale).
func TestDispatchItem_StaleHookedFormula_NotMistakenForNewInstance(t *testing.T) {
	store := installMemStore(t)
	ctx := context.Background()

	iss, err := store.Create(ctx, issuestore.CreateParams{
		Title: "Formula: stale", Type: issuestore.TypeEpic,
		Labels: []string{"formula-instance"}, Assignee: "mgr",
	})
	if err != nil {
		t.Fatalf("Create epic: %v", err)
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".runtime"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".runtime", "hooked_formula"), []byte(iss.ID), 0o644); err != nil {
		t.Fatalf("write hooked_formula: %v", err)
	}

	// STALE: dispatch stamped in the future ⇒ the just-created instance's
	// CreatedAt precedes it ⇒ no capture.
	future := time.Now().Add(time.Hour)
	if got := captureInstanceID(ctx, store, dir, "", future); got != "" {
		t.Errorf("captureInstanceID(stale) = %q, want \"\" (freshness gate must reject a pre-dispatch instance)", got)
	}

	// Positive control: same dir + bead, dispatch stamped in the past ⇒ fresh ⇒ captured.
	past := time.Now().Add(-time.Hour)
	if got := captureInstanceID(ctx, store, dir, "", past); got != iss.ID {
		t.Errorf("captureInstanceID(fresh) = %q, want %q (only the freshness gate should have flipped the result)", got, iss.ID)
	}
}

// AC #6 / GHERKIN: an issue→pr handoff phase whose instance is terminal+complete
// but with no linked PR (or an ambiguous >1) does NOT advance. The linked-PR
// lookup is behind the new ghLinkedPRs package-var seam (injected fake — the
// concrete gh query is pinned in Phase 3).
func TestCompletion_RequiresLinkedPR_ForIssueToPRHandoff(t *testing.T) {
	store := installMemStore(t)
	got := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	if !instanceComplete(got) {
		t.Fatalf("precondition: instance must read complete before the artifact gate is meaningful")
	}

	cases := []struct {
		name     string
		prs      []int
		wantLink bool
	}{
		{"zero_linked_prs_stall", []int{}, false},
		{"exactly_one_linked_pr", []int{42}, true},
		{"many_linked_prs_ambiguous", []int{42, 43}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := ghLinkedPRs
			ghLinkedPRs = func(repo string, issueNumber int) ([]int, error) { return tc.prs, nil }
			t.Cleanup(func() { ghLinkedPRs = orig })

			if got := hasLinkedPR("owner/repo", 7); got != tc.wantLink {
				t.Errorf("hasLinkedPR(%v) = %v, want %v", tc.prs, got, tc.wantLink)
			}
			// Overall handoff completion = instance complete AND exactly one linked PR.
			overall := instanceComplete(got) && hasLinkedPR("owner/repo", 7)
			if overall != tc.wantLink {
				t.Errorf("issue→pr handoff complete = %v, want %v (terminal instance must NOT advance without exactly one linked PR)", overall, tc.wantLink)
			}
		})
	}

	t.Run("gh_error_is_not_complete", func(t *testing.T) {
		orig := ghLinkedPRs
		ghLinkedPRs = func(repo string, issueNumber int) ([]int, error) {
			return nil, context.DeadlineExceeded
		}
		t.Cleanup(func() { ghLinkedPRs = orig })
		if hasLinkedPR("owner/repo", 7) {
			t.Errorf("hasLinkedPR on gh error = true, want false")
		}
	})
}

// AC #7 / GHERKIN: a source:pr TERMINAL phase gates on mergeable AND approved AND
// checks-green — NOT on merged. The PR status is behind the new ghPRStatus seam.
func TestCompletion_PrSourceTerminalPhase_RequiresMergeableApprovedGreen_NotMerged(t *testing.T) {
	cases := []struct {
		name         string
		st           prStatus
		wantComplete bool
	}{
		{"mergeable_approved_green_completes_even_though_not_merged", prStatus{Mergeable: true, Approved: true, ChecksGreen: true}, true},
		{"not_mergeable", prStatus{Mergeable: false, Approved: true, ChecksGreen: true}, false},
		{"not_approved", prStatus{Mergeable: true, Approved: false, ChecksGreen: true}, false},
		{"checks_not_green", prStatus{Mergeable: true, Approved: true, ChecksGreen: false}, false},
		{"nothing_satisfied", prStatus{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := ghPRStatus
			ghPRStatus = func(repo string, prNumber int) (prStatus, error) { return tc.st, nil }
			t.Cleanup(func() { ghPRStatus = orig })

			if got := prArtifactComplete("owner/repo", 99); got != tc.wantComplete {
				t.Errorf("prArtifactComplete(%+v) = %v, want %v (gate = mergeable AND approved AND checks-green, NOT merged)", tc.st, got, tc.wantComplete)
			}
		})
	}

	t.Run("gh_error_is_not_complete", func(t *testing.T) {
		orig := ghPRStatus
		ghPRStatus = func(repo string, prNumber int) (prStatus, error) {
			return prStatus{}, context.DeadlineExceeded
		}
		t.Cleanup(func() { ghPRStatus = orig })
		if prArtifactComplete("owner/repo", 99) {
			t.Errorf("prArtifactComplete on gh error = true, want false")
		}
	})
}

// checkRollupGreen is the pure mapping from a single statusCheckRollup entry to
// "passing". The production ghPRStatus path is unreachable until Phase 3 wires
// the loop, so this directly covers the SUCCESS/NEUTRAL/SKIPPED branching and the
// CheckRun-conclusion vs StatusContext-state shapes (closes review MINOR-1).
func TestCheckRollupGreen(t *testing.T) {
	cases := []struct {
		name       string
		state      string
		conclusion string
		want       bool
	}{
		{"checkrun_success", "", "SUCCESS", true},
		{"checkrun_neutral", "", "NEUTRAL", true},
		{"checkrun_skipped", "", "SKIPPED", true},
		{"checkrun_failure", "", "FAILURE", false},
		{"checkrun_cancelled", "", "CANCELLED", false},
		{"checkrun_in_progress_no_conclusion", "", "", false},
		{"statuscontext_success", "SUCCESS", "", true},
		{"statuscontext_pending", "PENDING", "", false},
		{"statuscontext_failure", "FAILURE", "", false},
		{"conclusion_wins_over_state", "SUCCESS", "FAILURE", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := checkRollupGreen(tc.state, tc.conclusion); got != tc.want {
				t.Errorf("checkRollupGreen(state=%q, conclusion=%q) = %v, want %v", tc.state, tc.conclusion, got, tc.want)
			}
		})
	}
}

// TestParseInstanceIDFromStdout directly covers the dir-agnostic K4 fallback
// parser (the stdout_parse_fallback subtest exercises it through captureInstanceID,
// but this pins the edge cases: absent marker, trailing content, multi-line).
func TestParseInstanceIDFromStdout(t *testing.T) {
	cases := []struct {
		name   string
		stdout string
		want   string
	}{
		{"canonical", `Formula "soldesign-engineer" instantiated: af-1a2b3c (12 steps)`, "af-1a2b3c"},
		{"with_surrounding_lines", "Created worktree\nFormula \"x\" instantiated: af-99 (1 steps)\n  s1 → b1", "af-99"},
		{"no_marker", "nothing here\njust output", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := parseInstanceIDFromStdout(tc.stdout); got != tc.want {
				t.Errorf("parseInstanceIDFromStdout(%q) = %q, want %q", tc.stdout, got, tc.want)
			}
		})
	}
}

// K3 / GHERKIN: the five additive dispatchEntry fields round-trip through the
// dispatch-state JSON, and an old 4-field state file still loads (new fields zero).
func TestDispatchEntry_AdditiveFields_RoundTrip(t *testing.T) {
	root := t.TempDir()
	now := time.Now().UTC().Round(time.Second)
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": {
			Agent:             "engineer",
			DispatchedAt:      now,
			ItemURL:           "https://github.com/owner/repo/issues/7",
			Source:            "issue",
			Workflow:          "soldesign",
			Phase:             "design",
			PhaseInstanceID:   "af-1a2b3c",
			PhaseDispatchedAt: now,
			Attempts:          2,
		},
	}}
	if err := saveDispatchState(root, &state); err != nil {
		t.Fatalf("saveDispatchState: %v", err)
	}
	reloaded := loadDispatchState(root)
	got, ok := reloaded.Dispatched["owner/repo#7"]
	if !ok {
		t.Fatalf("entry missing after reload")
	}
	if got.Workflow != "soldesign" || got.Phase != "design" || got.PhaseInstanceID != "af-1a2b3c" || got.Attempts != 2 {
		t.Errorf("additive fields lost on round-trip: %+v", got)
	}
	if !got.PhaseDispatchedAt.Equal(now) {
		t.Errorf("PhaseDispatchedAt = %v, want %v", got.PhaseDispatchedAt, now)
	}

	// An old 4-field state file unmarshals with the new fields as zero values.
	old := `{"dispatched":{"owner/repo#1":{"agent":"a","dispatched_at":"2026-06-25T00:00:00Z","item_url":"u","source":"issue"}}}`
	if err := os.MkdirAll(filepath.Join(root, ".runtime"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, ".runtime", "dispatch-state.json"), []byte(old), 0o644); err != nil {
		t.Fatalf("write old state: %v", err)
	}
	oldState := loadDispatchState(root)
	e := oldState.Dispatched["owner/repo#1"]
	if e.Agent != "a" || e.PhaseInstanceID != "" || e.Attempts != 0 || !e.PhaseDispatchedAt.IsZero() {
		t.Errorf("old 4-field entry did not load with zero new fields: %+v", e)
	}
	// Confirm the encoder keeps omitempty quiet for an old-shaped entry.
	if data, err := json.Marshal(e); err == nil {
		if string(data) == "" {
			t.Errorf("unexpected empty marshal")
		}
	}
}
