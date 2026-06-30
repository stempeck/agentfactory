package cmd

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// ============================================================================
// Issue #461 Phase 1 — dispatcher source-aware linked-PR resolution. These tests
// pin the behavioral contract of phaseConsumesPR + resolvePhaseInputURL and the
// source-aware evaluatePhase handoff guard (COMP-SOURCE-AWARE / COMP-DISPATCH-RESOLVE).
// Like the rest of internal/cmd they are NOT t.Parallel-safe (package-global seams).
// ============================================================================

// recordSlingsWithURL mirrors recordSlings but ALSO captures the itemURL each
// sling receives (urls, in call order), so a test can assert WHICH URL a phase
// agent is handed — the assertion the pre-#461 recorder discarded.
func recordSlingsWithURL(t *testing.T, store *memstore.Store, slung, urls, ids *[]string) {
	t.Helper()
	orig := dispatchItem
	dispatchItem = func(root, agent, itemURL, caller string) (string, error) {
		*slung = append(*slung, agent)
		*urls = append(*urls, itemURL)
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

// stubLinkedPRsSequence swaps ghLinkedPRs to return seq[i] on the i-th call,
// repeating the last element after the sequence is exhausted — the stateful stub
// that models the gate↔sling race (e.g. [[42],[]] = one linked PR at the gate,
// zero by sling time).
func stubLinkedPRsSequence(t *testing.T, seq [][]int) {
	t.Helper()
	orig := ghLinkedPRs
	i := 0
	ghLinkedPRs = func(repo string, issueNumber int) ([]int, error) {
		prs := seq[len(seq)-1]
		if i < len(seq) {
			prs = seq[i]
		}
		i++
		return prs, nil
	}
	t.Cleanup(func() { ghLinkedPRs = orig })
}

// featureWorkflowDualSourceCfg mirrors the REAL dispatch.json shape: pr-review is
// a DUAL-SOURCE phase whose source:issue mapping is listed BEFORE its source:pr
// mapping. phaseMapping returns the first (source:issue) match, which is exactly
// what makes the pre-#461 next.Source=="pr" guard fail to fire (Gap 1 / Gap 10).
func featureWorkflowDualSourceCfg() *config.DispatchConfig {
	return &config.DispatchConfig{
		Repos:            []string{"owner/repo"},
		TriggerLabel:     "agentic",
		NotifyOnComplete: "manager",
		RetryAfterSecs:   1800,
		Mappings: []config.DispatchMapping{
			{Labels: []string{"soldesign-engineer"}, Source: "issue", Agent: "eng"},
			{Labels: []string{"pr-review"}, Source: "issue", Agent: "review"},
			{Labels: []string{"pr-review"}, Source: "pr", Agent: "review"},
		},
		Workflows: []config.Workflow{
			{Label: "feature-workflow", Phases: []string{"soldesign-engineer", "pr-review"}},
		},
	}
}

// --- AC-2: an issue-started PR-consuming phase receives the linked PR URL ---

func TestWorkflow_IssueStarted_PRPhaseReceivesPRURL(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{99}) // the single linked PR for the issue→pr handoff

	var edits []labelEdit
	var slung, urls, ids []string
	recordLabelEdits(t, &edits)
	recordSlingsWithURL(t, store, &slung, &urls, &ids)

	cmd, _, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if !equalStrs(slung, []string{"review"}) {
		t.Fatalf("slung = %v, want [review]; stderr=%s", slung, errBuf.String())
	}
	if len(urls) != 1 {
		t.Fatalf("captured %d sling URLs, want 1: %v", len(urls), urls)
	}
	if urls[0] != "https://github.com/owner/repo/pull/99" {
		t.Errorf("pr-review received URL %q, want the linked PR URL https://github.com/owner/repo/pull/99 (NOT the issue URL)", urls[0])
	}
}

// --- AC-1: a 0-linked-PR issue→pr handoff stalls; the guard keys on item source ---

func TestWorkflow_IssueStarted_NoLinkedPR_Stalls(t *testing.T) {
	store := installMemStore(t)
	cfg := featureWorkflowDualSourceCfg()
	wf := &cfg.Workflows[0]

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{}) // issue resolves to ZERO linked PRs

	entry := dispatchEntry{
		Agent: "eng", Workflow: "feature-workflow", Phase: "soldesign-engineer",
		PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
	}
	if _, ok := phaseMapping(cfg.Mappings, "soldesign-engineer"); !ok {
		t.Fatal("soldesign-engineer mapping missing from featureWorkflowDualSourceCfg")
	}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "soldesign-engineer")}

	outcome, reason := evaluatePhase(context.Background(), store, "owner/repo", item, "issue",
		entry, cfg.Mappings, wf, "soldesign-engineer")

	if outcome != phaseStall {
		t.Fatalf("evaluatePhase issue→pr handoff with 0 linked PRs = %v, want phaseStall; the dual-source guard must key on the ITEM source, not the order-dependent first-match mapping source", outcome)
	}
	if !strings.Contains(reason, "0 linked PRs") {
		t.Errorf("stall reason = %q, want it to name the 0-linked-PR handoff", reason)
	}
}

// --- AC-5: the pr-started path passes the original PR URL through unchanged ---

func TestWorkflow_PRStarted_PRPhaseReceivesOriginalURL(t *testing.T) {
	// On a pr-source workflow the resolver must take the identity branch and NEVER
	// touch ghLinkedPRs — fail loudly if it does.
	orig := ghLinkedPRs
	ghLinkedPRs = func(repo string, issueNumber int) ([]int, error) {
		t.Errorf("ghLinkedPRs called on a pr-source workflow; resolvePhaseInputURL must return the item URL unchanged")
		return nil, nil
	}
	t.Cleanup(func() { ghLinkedPRs = orig })

	cfg := crossSourceCfg()
	w := &workflowCtx{
		dispatchCfg: cfg,
		repo:        "owner/repo",
		source:      "pr",
		item:        ghItem{Number: 99, URL: "https://github.com/owner/repo/pull/99"},
	}

	got, err := w.resolvePhaseInputURL("pr-review")
	if err != nil {
		t.Fatalf("resolvePhaseInputURL on the pr path: %v", err)
	}
	if got != "https://github.com/owner/repo/pull/99" {
		t.Errorf("resolvePhaseInputURL on pr path = %q, want the original PR URL unchanged", got)
	}
}

// --- AC-5: an issue-source phase (not PR-consuming) passes the issue URL through ---

func TestResolvePhaseInputURL_IssueSourcePhase_ReturnsIssueURL(t *testing.T) {
	orig := ghLinkedPRs
	ghLinkedPRs = func(repo string, issueNumber int) ([]int, error) {
		t.Errorf("ghLinkedPRs called for an issue-source (non-PR-consuming) phase; must return item URL unchanged")
		return nil, nil
	}
	t.Cleanup(func() { ghLinkedPRs = orig })

	cfg := crossSourceCfg() // "enhancement" is a source:issue phase
	w := &workflowCtx{
		dispatchCfg: cfg,
		repo:        "owner/repo",
		source:      "issue",
		item:        ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7"},
	}

	got, err := w.resolvePhaseInputURL("enhancement")
	if err != nil {
		t.Fatalf("resolvePhaseInputURL for an issue-source phase: %v", err)
	}
	if got != "https://github.com/owner/repo/issues/7" {
		t.Errorf("resolvePhaseInputURL(enhancement) = %q, want the issue URL unchanged", got)
	}
}

// --- AC-1: >1 linked PRs is ambiguous ⇒ resolver errors (never returns a URL) ---

func TestResolvePhaseInputURL_MultipleLinkedPRs_Errors(t *testing.T) {
	stubLinkedPRs(t, []int{42, 43}) // ambiguous: two linked PRs

	cfg := crossSourceCfg() // pr-review is a PR-consuming phase
	w := &workflowCtx{
		dispatchCfg: cfg,
		repo:        "owner/repo",
		source:      "issue",
		item:        ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7"},
	}

	got, err := w.resolvePhaseInputURL("pr-review")
	if err == nil {
		t.Fatalf("resolvePhaseInputURL with 2 linked PRs = %q, nil; want an error (ambiguous handoff must not resolve a URL)", got)
	}
	if !strings.Contains(err.Error(), "2 linked PRs") {
		t.Errorf("error = %q, want it to name the 2-linked-PR ambiguity", err.Error())
	}
}

// --- ROUND-2 P-3: gate↔sling race (1 PR at gate, 0 at sling) ⇒ named stall ---

func TestWorkflow_IssueStarted_PRCountRace_Stalls(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	wf := &cfg.Workflows[0]

	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	// Exactly one linked PR at the handoff GATE, zero by SLING time — the race.
	stubLinkedPRsSequence(t, [][]int{{42}, {}})

	var edits []labelEdit
	var slung, urls, ids []string
	recordLabelEdits(t, &edits)
	recordSlingsWithURL(t, store, &slung, &urls, &ids)

	cmd, _, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": {
			Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
			PhaseInstanceID: complete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour),
		},
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "enhancement")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	// The pr-review agent must NEVER be dispatched: the resolver fails before dispatchItem.
	if len(slung) != 0 {
		t.Errorf("slung = %v, want none — the race must stall before dispatching the pr phase (no silent advance with the issue URL)", slung)
	}
	// Exactly one detectable error, surfaced as a NAMED stall (Change 5), not the
	// generic 'dispatch failed' bare-return path that defers to the resling self-heal.
	if stats.errors != 1 {
		t.Errorf("stats.errors = %d, want 1 (a single clean stall, no 5-cycle re-sling storm)", stats.errors)
	}
	if !strings.Contains(errBuf.String(), "stall") {
		t.Errorf("stderr = %q, want a distinctly-named 'stall' (resolve error routed through w.stall, not 'dispatch failed')", errBuf.String())
	}
}

// ============================================================================
// Issue #461 Phase 1b — completion-recognition latch (RC#1). These pin the
// behavioral contract of the empty-PhaseInstanceID self-heal: a genuinely-complete
// phase whose capture-time pointer was lost must ADVANCE (latch), never re-sling
// forever. All assertions route through evaluatePhase (the latch's only caller) so
// the keystone fails CLEANLY on main — main's empty-ID branch returns phaseIncomplete,
// so the want-phaseAdvance assertion fails by value, not by compile error (revert
// only internal/cmd/dispatch.go to reproduce). Not t.Parallel-safe (package globals).
// ============================================================================

// desyncEntry builds the empty-PhaseInstanceID dispatchEntry the latch targets:
// the phase WAS dispatched (Phase == phase, Agent set, PhaseDispatchedAt real) but
// capture missed (PhaseInstanceID == "") — the exact desync RC#1 describes.
func desyncEntry(agent, phase string, dispatchedAt time.Time) dispatchEntry {
	return dispatchEntry{
		Agent: agent, Workflow: "feature-workflow", Phase: phase,
		PhaseInstanceID: "", PhaseDispatchedAt: dispatchedAt,
	}
}

// evalDesyncPhase drives evaluatePhase for the dual-source feature-workflow at the
// given phase, returning only the outcome (the latch path returns before any K6b
// artifact gate, so item/source plumbing is inert here).
func evalDesyncPhase(t *testing.T, store issuestore.Store, entry dispatchEntry, phase string) phaseOutcome {
	t.Helper()
	cfg := featureWorkflowDualSourceCfg()
	wf := &cfg.Workflows[0]
	if _, ok := phaseMapping(cfg.Mappings, phase); !ok {
		t.Fatalf("phase %q has no mapping in featureWorkflowDualSourceCfg", phase)
	}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", phase)}
	outcome, _ := evaluatePhase(context.Background(), store, "owner/repo", item, "issue",
		entry, cfg.Mappings, wf, phase)
	return outcome
}

// KEYSTONE (must FAIL on main): an empty-pointer entry whose phase produced a
// genuinely-complete formula-instance epic (assigned to the phase's agent, created
// after the dispatch) ⇒ phaseAdvance. On main (no latch) the empty-ID branch returns
// phaseIncomplete ⇒ resling ⇒ this assertion fails.
func TestWorkflow_DesyncedCompletion_Advances(t *testing.T) {
	store := installMemStore(t)
	// The genuinely-complete instance THIS phase produced — seeded with the PRODUCTION
	// Assignee (the phase agent "review"), NOT the recorder's "mgr" default.
	seedClosedEpicFor(t, store, config.CloseReasonFormulaComplete, "review")
	entry := desyncEntry("review", "pr-review", time.Now().Add(-time.Hour))

	if got := evalDesyncPhase(t, store, entry, "pr-review"); got != phaseAdvance {
		t.Fatalf("evaluatePhase(empty pointer + correlated formula-complete epic) = %v, want phaseAdvance — the completion latch must self-heal a lost pointer to advance, not re-sling. (FAILS on main: no latch ⇒ phaseIncomplete.)", got)
	}
}

// C-3 negative: a --reset-closed epic carries CloseReasonResetSling, NOT
// "formula complete", so instanceComplete is false and the latch must NOT fire.
func TestWorkflow_DesyncedCompletion_ResetClosed_DoesNotAdvance(t *testing.T) {
	store := installMemStore(t)
	seedClosedEpicFor(t, store, config.CloseReasonResetSling, "review")
	entry := desyncEntry("review", "pr-review", time.Now().Add(-time.Hour))

	if got := evalDesyncPhase(t, store, entry, "pr-review"); got != phaseIncomplete {
		t.Fatalf("evaluatePhase(empty pointer + --reset-closed epic) = %v, want phaseIncomplete — a CloseReasonResetSling instance must NEVER satisfy the latch (C-3, the #378/#413 false-advance guard)", got)
	}
}

// Correlation negative (agent): the only post-dispatch formula-complete epic belongs
// to a DIFFERENT agent ⇒ the latch must not fire for the phase under eval.
func TestWorkflow_DesyncedCompletion_DifferentAgent_DoesNotAdvance(t *testing.T) {
	store := installMemStore(t)
	seedClosedEpicFor(t, store, config.CloseReasonFormulaComplete, "eng") // NOT "review"
	entry := desyncEntry("review", "pr-review", time.Now().Add(-time.Hour))

	if got := evalDesyncPhase(t, store, entry, "pr-review"); got != phaseIncomplete {
		t.Fatalf("evaluatePhase(empty pointer + complete epic for a DIFFERENT agent) = %v, want phaseIncomplete — the latch must correlate on THIS phase's agent (P-1)", got)
	}
}

// Correlation negative (time): the complete epic predates this dispatch (dispatch is
// in the future) ⇒ it belongs to a prior run, not this dispatch ⇒ latch must not fire.
func TestWorkflow_DesyncedCompletion_PreDispatch_DoesNotAdvance(t *testing.T) {
	store := installMemStore(t)
	seedClosedEpicFor(t, store, config.CloseReasonFormulaComplete, "review")
	entry := desyncEntry("review", "pr-review", time.Now().Add(time.Hour)) // dispatch AFTER the epic

	if got := evalDesyncPhase(t, store, entry, "pr-review"); got != phaseIncomplete {
		t.Fatalf("evaluatePhase(empty pointer + complete epic created BEFORE PhaseDispatchedAt) = %v, want phaseIncomplete — a pre-dispatch completion is a prior run (the captureInstanceID freshness gate, mirrored)", got)
	}
}

// Placement guard: the latch is applied ONLY at the empty-ID branch (where
// entry.Phase == phase), NEVER at the phase-mismatch branch. A different-phase entry
// (or a zero/lost entry) must return phaseIncomplete WITHOUT consulting the latch,
// even though a complete epic for "review" exists — advancing a freshly-labeled phase
// on another phase's completion is unsound (#413 CRIT-1).
func TestWorkflow_DesyncedCompletion_PhaseMismatch_DoesNotLatchAdvance(t *testing.T) {
	store := installMemStore(t)
	seedClosedEpicFor(t, store, config.CloseReasonFormulaComplete, "review")

	mismatch := desyncEntry("review", "soldesign-engineer", time.Now().Add(-time.Hour))
	if got := evalDesyncPhase(t, store, mismatch, "pr-review"); got != phaseIncomplete {
		t.Fatalf("evaluatePhase(phase-mismatch entry) = %v, want phaseIncomplete — the latch must NOT fire at the entry.Phase != phase branch", got)
	}
	if got := evalDesyncPhase(t, store, dispatchEntry{}, "pr-review"); got != phaseIncomplete {
		t.Fatalf("evaluatePhase(zero/lost entry) = %v, want phaseIncomplete — a lost record self-heals via re-sling, not a latch advance", got)
	}
}

// (c) backoff floor: an empty-ID record dispatched seconds ago, with no correlated
// completion, must observe the floor and skip — NOT re-sling immediately (the RC#1
// burst that could burn maxWorkflowAttempts in seconds).
func TestResling_EmptyID_RecentDispatch_ObservesBackoffFloor(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := featureWorkflowDualSourceCfg()
	wf := &cfg.Workflows[0]

	var slung, ids []string
	recordSlings(t, store, &slung, &ids)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": desyncEntry("eng", "soldesign-engineer", time.Now()), // just dispatched
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "soldesign-engineer")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if len(slung) != 0 {
		t.Errorf("slung = %v, want none — an empty-ID record dispatched seconds ago must observe the backoff floor, not re-sling immediately", slung)
	}
	if stats.dispatched != 0 {
		t.Errorf("stats.dispatched = %d, want 0 (the floor skips this cycle)", stats.dispatched)
	}
	if stats.skipped != 1 {
		t.Errorf("stats.skipped = %d, want 1 (the empty-ID floor gate skips, exactly like the non-empty retry window)", stats.skipped)
	}
}

// (c) the floor is a FLOOR, not a permanent block: the same empty-ID record dispatched
// an hour ago (well beyond the floor) must still self-heal by re-slinging.
func TestResling_EmptyID_BeyondFloor_Reslings(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := featureWorkflowDualSourceCfg()
	wf := &cfg.Workflows[0]

	var slung, ids []string
	recordSlings(t, store, &slung, &ids)

	cmd, _, _ := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	state := dispatchState{Dispatched: map[string]dispatchEntry{
		"owner/repo#7": desyncEntry("eng", "soldesign-engineer", time.Now().Add(-time.Hour)),
	}}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels("agentic", "feature-workflow", "soldesign-engineer")}

	handleWorkflowItem(cmd, t.TempDir(), fake, &state, stats, cfg, "owner/repo", item, "issue", wf)

	if !equalStrs(slung, []string{"eng"}) {
		t.Errorf("slung = %v, want [eng] — an empty-ID record beyond the floor must still self-heal (re-sling); the floor bounds the burst, it does not block forever", slung)
	}
}

// flakyGetStore wraps a Store and fails the first failsLeft Get calls with a
// transient error before delegating — models a backend blip captureInstanceID's
// (b) hardening must ride out (#461). Other methods pass through the embedded Store.
type flakyGetStore struct {
	issuestore.Store
	failsLeft int
}

func (f *flakyGetStore) Get(ctx context.Context, id string) (issuestore.Issue, error) {
	if f.failsLeft > 0 {
		f.failsLeft--
		return issuestore.Issue{}, fmt.Errorf("transient store blip")
	}
	return f.Store.Get(ctx, id)
}

// (b): a TRANSIENT store.Get blip — a candidate id IS in hand — must NOT collapse to
// a permanently-lost "" (which would drive the RC#1 loop). captureInstanceID retries
// past a bounded number of blips and captures the id; a PERSISTENT failure still
// yields "" safely (never an unverified id), and the latch (a) recovers it.
func TestCaptureInstanceID_TransientGetError_RetriesThenSafelyGivesUp(t *testing.T) {
	store := installMemStore(t)
	ctx := context.Background()
	iss, err := store.Create(ctx, issuestore.CreateParams{
		Title: "Formula: eng", Type: issuestore.TypeEpic,
		Labels: []string{"formula-instance"}, Assignee: "eng",
	})
	if err != nil {
		t.Fatalf("seed epic: %v", err)
	}
	stdout := fmt.Sprintf(`Formula "eng" instantiated: %s (3 steps)`, iss.ID)
	past := iss.CreatedAt.Add(-time.Minute) // dispatch before the epic ⇒ passes the freshness gate

	// captureGetAttempts is 3; two transient blips then success must still capture.
	transient := &flakyGetStore{Store: store, failsLeft: 2}
	if got := captureInstanceID(ctx, transient, "", stdout, past); got != iss.ID {
		t.Errorf("captureInstanceID with 2 transient Get blips = %q, want %q (the retry must ride out a transient blip, not lose the pointer)", got, iss.ID)
	}

	// A persistent failure (more blips than attempts) still yields "" — no unverified
	// id is ever returned, so the #413 stale-instance class stays closed.
	persistent := &flakyGetStore{Store: store, failsLeft: 99}
	if got := captureInstanceID(ctx, persistent, "", stdout, past); got != "" {
		t.Errorf("captureInstanceID with a persistent Get failure = %q, want \"\" (give up safely; the completion latch recovers it next cycle)", got)
	}
}

// featureWorkflowDualSourceFullCfg is featureWorkflowDualSourceCfg extended to the
// FULL production phase list [soldesign-plan, soldesign-engineer, pr-review,
// pr-iterate] (mirrors .agentfactory/dispatch.json). The two PR-consuming phases are
// dual-source with their source:issue mapping listed BEFORE source:pr — the exact
// order that defeated the pre-#461 next.Source=="pr" guard (Gap 1 / Gap 10). It is a
// SIBLING of the 2-phase featureWorkflowDualSourceCfg (kept separate so the existing
// #461 callers of the 2-phase shape are untouched). COMP-TEST-FEATUREWF.
func featureWorkflowDualSourceFullCfg() *config.DispatchConfig {
	return &config.DispatchConfig{
		Repos:            []string{"owner/repo"},
		TriggerLabel:     "agentic",
		NotifyOnComplete: "manager",
		RetryAfterSecs:   1800,
		Mappings: []config.DispatchMapping{
			{Labels: []string{"soldesign-plan"}, Source: "issue", Agent: "plan"},
			{Labels: []string{"soldesign-engineer"}, Source: "issue", Agent: "eng"},
			{Labels: []string{"pr-review"}, Source: "issue", Agent: "review"}, // dual: issue BEFORE pr
			{Labels: []string{"pr-review"}, Source: "pr", Agent: "review"},
			{Labels: []string{"pr-iterate"}, Source: "issue", Agent: "iterate"}, // dual: issue BEFORE pr
			{Labels: []string{"pr-iterate"}, Source: "pr", Agent: "iterate"},
		},
		Workflows: []config.Workflow{
			{Label: "feature-workflow", Phases: []string{"soldesign-plan", "soldesign-engineer", "pr-review", "pr-iterate"}},
		},
	}
}

// --- AC-1 / AC-3: an issue-started feature-workflow advances through the PR phases
// end-to-end WITHOUT re-dispatching a completed phase (the positive end-to-end
// counterpart to the single-step TestWorkflow_AdvanceOnTerminalInstance). Drives the
// production dual-source shape so a regression in the source-aware handoff guard
// cannot pass silently (Gotcha "Gap 7"). ---

func TestWorkflow_IssueStarted_AdvancesThroughPRPhases_NoRedispatch(t *testing.T) {
	fake, store := setupHermeticSessions(t)
	cfg := featureWorkflowDualSourceFullCfg()
	wf := &cfg.Workflows[0]
	root := t.TempDir()
	const key = "owner/repo#7"
	const issueURL = "https://github.com/owner/repo/issues/7"
	const pullURL = "https://github.com/owner/repo/pull/42"

	// Every issue→pr handoff (the gate) and every pr-phase sling (the URL rewrite)
	// resolves to exactly one linked PR. A single fixed PR every call is the stable
	// happy path. The terminal pr-iterate is dual-source (phaseConsumesPR ⇒ true), so
	// the terminal pr-status gate DOES fire; stub a green PR so the merge-ready gate
	// passes and the workflow terminates.
	stubLinkedPRs(t, []int{42})
	stubPRStatusGreen(t)

	var slung, urls, ids []string
	recordSlingsWithURL(t, store, &slung, &urls, &ids)
	var mails []string
	recordWorkflowMail(t, &mails)

	// Live label set on the issue tracking item; the cursor is the label, so
	// editItemLabels must mutate it for the multi-cycle driving loop to progress.
	live := map[string]bool{"agentic": true, "feature-workflow": true}
	var edits []labelEdit
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

	itemFromLive := func() ghItem {
		var ls []ghLabel
		for n := range live {
			ls = append(ls, ghLabel{Name: n})
		}
		return ghItem{Number: 7, URL: issueURL, Labels: ls}
	}

	state := dispatchState{Dispatched: map[string]dispatchEntry{}}
	cmd, _, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}

	// Drive cycle-by-cycle until the pipeline reaches terminal (agentic removed) or a
	// safety bound. Between cycles the freshly-slung phase's formula "finishes": close
	// its instance with the completion reason so the next cycle reads it terminal —
	// exactly the input under which a buggy engine would RE-SLING the completed phase.
	for cycle := 0; cycle < 10 && live["agentic"]; cycle++ {
		handleWorkflowItem(cmd, root, fake, &state, stats, cfg, "owner/repo", itemFromLive(), "issue", wf)
		if e, ok := state.Dispatched[key]; ok && e.PhaseInstanceID != "" {
			_ = store.Close(context.Background(), e.PhaseInstanceID, config.CloseReasonFormulaComplete)
		}
	}

	if live["agentic"] {
		t.Fatalf("pipeline did not reach terminal (possible re-dispatch loop): live=%v slung=%v\nstderr=%s", live, slung, errBuf.String())
	}

	// THE no-redispatch contract: a completed phase is never slung a second time.
	seen := map[string]int{}
	for _, a := range slung {
		seen[a]++
	}
	for _, a := range []string{"plan", "eng", "review", "iterate"} {
		if seen[a] > 1 {
			t.Errorf("phase agent %q was slung %d times — a completed phase was re-dispatched (RC#1 loop); slung=%v", a, seen[a], slung)
		}
	}
	// Each phase advanced exactly once, in order.
	if !equalStrs(slung, []string{"plan", "eng", "review", "iterate"}) {
		t.Fatalf("slung order = %v, want [plan eng review iterate] (each phase exactly once); stderr=%s", slung, errBuf.String())
	}
	// The source-aware rewrite ran: soldesign-* phases get the issue URL; the
	// PR-consuming phases get the linked-PR URL, never the raw issue URL (AC-2).
	if !equalStrs(urls, []string{issueURL, issueURL, pullURL, pullURL}) {
		t.Errorf("sling URLs = %v, want [issue issue pull pull] = %v", urls, []string{issueURL, issueURL, pullURL, pullURL})
	}
	// Exactly one workflow-complete mail; the correlation record is dropped; no stalls.
	if len(mails) != 1 {
		t.Errorf("workflow-complete mails = %d, want exactly 1; %v", len(mails), mails)
	}
	if _, ok := state.Dispatched[key]; ok {
		t.Errorf("correlation record should be dropped on terminal completion; state=%+v", state.Dispatched)
	}
	if !live["feature-workflow"] || live["pr-iterate"] {
		t.Errorf("final live labels = %v, want feature-workflow retained and pr-iterate removed", live)
	}
	if stats.errors != 0 {
		t.Errorf("stats.errors = %d, want 0 (the happy path advances without stalls); stderr=%s", stats.errors, errBuf.String())
	}
}
