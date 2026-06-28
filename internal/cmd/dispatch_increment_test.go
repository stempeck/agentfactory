package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
)

// ============================================================================
// PR #448 ultra-increment regression tests for the unresolved review threads:
//   THREAD-2 resling() attempt-counter loss      (dispatch.go:~1107)
//   THREAD-3 terminal() persist-before-label-edit (dispatch.go:~1066)
//   THREAD-4 advance() persist-before-label-edit  (dispatch.go:~1037)
//   THREAD-4 evaluatePhase() stale-phase guard    (dispatch.go:~813)
// Same harness conventions as dispatch_phase3_test.go: package-var seams, no
// t.Parallel (package globals), no live gh/tmux/sling.
// ============================================================================

// seedOpenEpic creates a NOT-complete formula instance (open epic): instanceComplete
// is false, so evaluatePhase routes to resling (the lost/incomplete-instance path).
func seedOpenEpic(t *testing.T, store interface {
	Create(context.Context, issuestore.CreateParams) (issuestore.Issue, error)
}) issuestore.Issue {
	t.Helper()
	iss, err := store.Create(context.Background(), issuestore.CreateParams{
		Title: "Formula: impl", Type: issuestore.TypeEpic,
		Labels: []string{"formula-instance"}, Assignee: "mgr",
	})
	if err != nil {
		t.Fatalf("create open epic: %v", err)
	}
	return iss
}

// --- THREAD-2: resling() must not lose the attempt count on a failed sling ---

func TestResling_PreservesAttemptsWhenSlingFails(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]
	open := seedOpenEpic(t, store) // incomplete ⇒ evaluatePhase ⇒ resling

	var slingCount int
	orig := dispatchItem
	dispatchItem = func(root, agent, itemURL, caller string) (string, error) {
		slingCount++
		return "", fmt.Errorf("sling boom (af unreachable)")
	}
	t.Cleanup(func() { dispatchItem = orig })

	const key = "owner/repo#7"
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		key: {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID: open.ID, PhaseDispatchedAt: time.Now().Add(-100 * time.Hour), Attempts: 2,
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if slingCount != 1 {
		t.Fatalf("dispatchItem called %d times, want 1", slingCount)
	}
	entry, ok := state.Dispatched[key]
	if !ok {
		t.Fatalf("record was lost after a failed re-sling — Attempts resets to 0 every cycle and the maxWorkflowAttempts ceiling can never fire")
	}
	if entry.Attempts != 3 {
		t.Errorf("Attempts after one failed re-sling = %d, want 3 (prior 2 + this attempt)", entry.Attempts)
	}
}

func TestResling_CeilingFiresOnPersistentSlingFailure(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]
	open := seedOpenEpic(t, store)

	var slingCount int
	orig := dispatchItem
	dispatchItem = func(root, agent, itemURL, caller string) (string, error) {
		slingCount++
		return "", fmt.Errorf("sling boom (af unreachable)")
	}
	t.Cleanup(func() { dispatchItem = orig })

	const key = "owner/repo#7"
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		key: {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID: open.ID, PhaseDispatchedAt: time.Now().Add(-100 * time.Hour), Attempts: 0,
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}

	root := t.TempDir()
	var sawCeiling bool
	for i := 0; i < 8 && !sawCeiling; i++ {
		cmd, _, errBuf := phase3Cmd()
		stats := &dispatchCycleStats{start: time.Now()}
		handleWorkflowItem(cmd, root, fake, &state, stats, cfg, "owner/repo", item, "issue", wf)
		if strings.Contains(errBuf.String(), "exceeded the re-sling ceiling") {
			sawCeiling = true
		}
	}

	if !sawCeiling {
		t.Errorf("re-sling ceiling never fired across 8 cycles of persistent sling failure — the 0→delete→fail→0 loop runs forever and no stall is surfaced")
	}
	if slingCount != maxWorkflowAttempts {
		t.Errorf("dispatchItem was called %d times, want bounded at maxWorkflowAttempts=%d", slingCount, maxWorkflowAttempts)
	}
}

// --- THREAD-3: terminal() must persist (delete+save) before editing GitHub labels ---

func TestTerminal_PersistsBeforeLabelEdit(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]
	root := t.TempDir()
	const key = "owner/repo#99"

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubPRStatusGreen(t) // terminal pr gate
	var mails []string
	recordWorkflowMail(t, &mails)

	state := dispatchState{Dispatched: map[string]dispatchEntry{
		key: {
			Agent: "iterate", Workflow: "feature-workflow", Phase: "pr-iterate",
			PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
		},
	}}
	// Pre-seed the on-disk state so a missing key at edit time PROVES persist-before-edit.
	if err := saveDispatchState(root, &state); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	presentAtEdit := recordLabelEditDiskState(t, root, key)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	item := ghItem{Number: 99, URL: "https://github.com/owner/repo/pull/99",
		Labels: labels("agentic", "feature-workflow", "pr-iterate")}
	handleWorkflowItem(cmd, root, fake, &state, stats, cfg, "owner/repo", item, "pr", wf)

	if len(*presentAtEdit) != 1 {
		t.Fatalf("editItemLabels calls = %d, want exactly 1", len(*presentAtEdit))
	}
	if (*presentAtEdit)[0] {
		t.Errorf("terminal() removed GitHub labels while the record was STILL on disk — a crash here orphans the Workflow!=\"\" cursor; persist (delete + saveDispatchState) before editItemLabels")
	}
}

// --- THREAD-4a: advance() must persist (delete+save) before editing GitHub labels ---

func TestAdvance_PersistsBeforeLabelEdit(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]
	root := t.TempDir()
	const key = "owner/repo#7"

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{42}) // enhancement→pr-review handoff needs exactly one linked PR
	var slung, ids []string
	recordSlings(t, store, &slung, &ids)

	state := dispatchState{Dispatched: map[string]dispatchEntry{
		key: {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour), Attempts: 2,
		},
	}}
	if err := saveDispatchState(root, &state); err != nil {
		t.Fatalf("seed save: %v", err)
	}

	presentAtEdit := recordLabelEditDiskState(t, root, key)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}
	handleWorkflowItem(cmd, root, fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if len(*presentAtEdit) != 1 {
		t.Fatalf("editItemLabels calls = %d, want exactly 1", len(*presentAtEdit))
	}
	if (*presentAtEdit)[0] {
		t.Errorf("advance() swapped GitHub labels while the stale record was STILL on disk — a crash here silently skips the next phase; persist (delete + saveDispatchState) before editItemLabels")
	}
}

// --- THREAD-4b: evaluatePhase() must not advance when entry.Phase != the live cursor ---

func TestEvaluatePhase_StaleRecordedPhaseDoesNotAdvance(t *testing.T) {
	store := installMemStore(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)

	// The recorded entry points at the PREVIOUS phase's genuinely-complete instance,
	// but the live GitHub cursor (the phase argument) is the NEXT phase — the exact
	// post-crash window where advance() moved the label before the record was persisted.
	entry := dispatchEntry{
		Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
		PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
	}
	m, ok := phaseMapping(cfg.Mappings, "pr-review")
	if !ok {
		t.Fatal("pr-review mapping missing from crossSourceCfg")
	}
	item := ghItem{Number: 42, URL: "https://github.com/owner/repo/pull/42",
		Labels: labels("agentic", "feature-workflow", "pr-review")}

	outcome, _ := evaluatePhase(context.Background(), store, "owner/repo", item, "pr",
		entry, cfg.Mappings, wf, "pr-review", m)

	if outcome != phaseIncomplete {
		t.Errorf("evaluatePhase with stale entry.Phase=%q vs live phase=%q = %v, want phaseIncomplete (the next phase must be re-slung, never silently skipped)",
			entry.Phase, "pr-review", outcome)
	}
}

// recordLabelEditDiskState swaps editItemLabels to a recorder that, at each call,
// reads the ON-DISK dispatch state and appends whether key is still present. A
// false means the record was persisted-deleted before the (irreversible) label edit.
func recordLabelEditDiskState(t *testing.T, root, key string) *[]bool {
	t.Helper()
	present := &[]bool{}
	orig := editItemLabels
	editItemLabels = func(repo string, number int, source string, add, remove []string) error {
		onDisk := loadDispatchState(root)
		_, ok := onDisk.Dispatched[key]
		*present = append(*present, ok)
		return nil
	}
	t.Cleanup(func() { editItemLabels = orig })
	return present
}
