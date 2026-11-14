package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
)

// Issue #751 K13 — a workflow stall mails WORKFLOW_STALL once per distinct reason and persists that
// reason on the item's dispatch record. Every multi-tick test takes the production round trip between
// ticks (prune, save, reload), because an in-memory loop cannot see a re-created entry pruned away or a
// field lost to its JSON tag. No t.Parallel: the seams are package globals.

const stallKey = "owner/repo#7"

type stallMailCall struct {
	root, recipient, workflow, itemKey, reason string
	withStore                                  bool
}

// recordStallMail swaps sendWorkflowStallMail to a recorder. fail(n) is consulted on the n-th call
// (1-based) and may return an error to model a failed send; it returns the successful-send count.
func recordStallMail(t *testing.T, attempts *[]stallMailCall, fail func(n int) error) *int {
	t.Helper()
	ok := new(int)
	orig := sendWorkflowStallMail
	sendWorkflowStallMail = func(_ context.Context, store issuestore.Store, root, recipient, workflow, itemKey, reason string) error {
		*attempts = append(*attempts, stallMailCall{root, recipient, workflow, itemKey, reason, store != nil})
		if fail != nil {
			if err := fail(len(*attempts)); err != nil {
				return err
			}
		}
		*ok++
		return nil
	}
	t.Cleanup(func() { sendWorkflowStallMail = orig })
	return ok
}

// stubLinkedPRsFunc swaps ghLinkedPRs to f(n) on the n-th call (1-based); stubLinkedPRsSequence
// cannot return an error, which the transient-gh test needs.
func stubLinkedPRsFunc(t *testing.T, f func(n int) ([]int, error)) *int {
	t.Helper()
	calls := new(int)
	orig := ghLinkedPRs
	ghLinkedPRs = func(repo string, issueNumber int) ([]int, error) {
		*calls++
		return f(*calls)
	}
	t.Cleanup(func() { ghLinkedPRs = orig })
	return calls
}

func setDispatchDryRun(t *testing.T) {
	t.Helper()
	orig := dispatchDryRun
	dispatchDryRun = true
	t.Cleanup(func() { dispatchDryRun = orig })
}

// seedStallState writes entries through the production saver and reads them back, so the first tick
// also starts from a round-tripped record.
func seedStallState(t *testing.T, root string, entries map[string]dispatchEntry) dispatchState {
	t.Helper()
	state := dispatchState{Dispatched: entries}
	if state.Dispatched == nil {
		state.Dispatched = map[string]dispatchEntry{}
	}
	if err := saveDispatchState(root, &state); err != nil {
		t.Fatalf("seed state: %v", err)
	}
	return loadDispatchState(root)
}

// stallTick is one production dispatch cycle for one item: a fresh cmd (so stderr is per tick), the
// shared stats, then runDispatch's end-of-cycle prune + save and the next process's reload.
func stallTick(t *testing.T, root string, fake *fakeTmux, state *dispatchState, stats *dispatchCycleStats,
	cfg *config.DispatchConfig, item ghItem, source string) string {
	t.Helper()
	cmd, _, errBuf := phase3Cmd()
	handleWorkflowItem(cmd, root, fake, state, stats, cfg, "owner/repo", item, source, &cfg.Workflows[0])
	pruneDispatchState(state)
	if err := saveDispatchState(root, state); err != nil {
		t.Fatalf("save state: %v", err)
	}
	*state = loadDispatchState(root)
	return errBuf.String()
}

func stallReasons(calls []stallMailCall) []string {
	out := make([]string, len(calls))
	for i, c := range calls {
		out[i] = c.reason
	}
	return out
}

func issueItem(phaseLabels ...string) ghItem {
	return ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7",
		Labels: labels(append([]string{"agentic", "feature-workflow"}, phaseLabels...)...)}
}

// The reasons are rendered from the production formats rather than hand-typed, so the → rune and the
// %q quoting match byte for byte.
func handoffReason(count int) string {
	return fmt.Sprintf("workflow %q issue→pr handoff at phase %q resolved %d linked PRs (want exactly 1)",
		"feature-workflow", "enhancement", count)
}

func terminalReason(count int) string {
	return fmt.Sprintf("workflow %q terminal pr phase %q resolved %d linked PRs (want exactly 1)",
		"feature-workflow", "pr-iterate", count)
}

// enhancementHandoffEntry is a completed enhancement phase whose issue→pr handoff the next tick gates.
func enhancementHandoffEntry(instanceID string) dispatchEntry {
	return dispatchEntry{
		Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
		PhaseInstanceID: instanceID, PhaseDispatchedAt: time.Now().Add(-time.Hour).UTC(),
	}
}

// terminalIterateEntry is a completed pr-iterate phase on an ISSUE item: it stalls on a linked-PR count
// other than one or on a closed PR, and completes on an open one.
func terminalIterateEntry(instanceID string) dispatchEntry {
	return dispatchEntry{
		Agent: "iterate", Workflow: "feature-workflow", Phase: "pr-iterate",
		PhaseInstanceID: instanceID, PhaseDispatchedAt: time.Now().Add(-time.Hour).UTC(),
	}
}

func TestWorkflowStall_MailsOncePerReasonAcrossThreeCycles(t *testing.T) {
	root := t.TempDir()
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{})
	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)
	var attempts []stallMailCall
	recordStallMail(t, &attempts, nil)

	state := seedStallState(t, root, map[string]dispatchEntry{stallKey: enhancementHandoffEntry(complete.ID)})
	stats := &dispatchCycleStats{start: time.Now()}
	want := handoffReason(0)
	for tick := 1; tick <= 3; tick++ {
		stderr := stallTick(t, root, fake, &state, stats, cfg, issueItem("enhancement"), "issue")
		if !strings.Contains(stderr, "stall "+stallKey+": "+want+"\n") {
			t.Errorf("tick %d stderr = %q, want the stall line every tick", tick, stderr)
		}
		if got := state.Dispatched[stallKey].Stall; got != want {
			t.Errorf("tick %d: stored Stall = %q, want %q", tick, got, want)
		}
	}

	wantCall := stallMailCall{root, "manager", "feature-workflow", stallKey, want, true}
	if len(attempts) != 1 || attempts[0] != wantCall {
		t.Errorf("stall mails = %+v, want exactly [%+v]", attempts, wantCall)
	}
	if stats.errors != 3 {
		t.Errorf("stats.errors = %d, want 3 (each tick's stall still counts once)", stats.errors)
	}
	data, err := os.ReadFile(filepath.Join(root, ".runtime", "dispatch-state.json"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if !strings.Contains(string(data), `"stall"`) {
		t.Errorf("state file carries no \"stall\" key:\n%s", data)
	}
	if len(slung) != 0 || len(edits) != 0 {
		t.Errorf("a stall must not sling (%v) or edit labels (%v)", slung, edits)
	}

	t.Run("the seam files in-process under the WORKFLOW_STALL subject", func(t *testing.T) {
		src := readRepoFile(t, "internal/cmd/dispatch.go")
		seam := regexp.MustCompile(`(?ms)^var sendWorkflowStallMail = func\(.*?^}`).FindString(src)
		if seam == "" {
			t.Fatal("internal/cmd/dispatch.go has no sendWorkflowStallMail var")
		}
		for _, want := range []string{
			"func(ctx context.Context, store issuestore.Store, root, recipient, workflow, itemKey, reason string) error",
			"isTestBinary()",
			"fileDispatcherMail(ctx, store, root, recipient,",
			`"WORKFLOW_STALL: "+workflow`,
			`fmt.Sprintf("Workflow %q stalled for %s: %s", workflow, itemKey, reason)`,
		} {
			if !strings.Contains(seam, want) {
				t.Errorf("sendWorkflowStallMail lacks %q:\n%s", want, seam)
			}
		}
		if strings.Contains(seam, "exec.Command") {
			t.Error("sendWorkflowStallMail runs a subprocess")
		}
		if !strings.Contains(src, "sendWorkflowStallMail(w.ctx, w.store, w.root, ") {
			t.Error("stall does not pass the tick's open store to sendWorkflowStallMail")
		}
	})
}

func TestWorkflowStall_NewReasonMailsAgain(t *testing.T) {
	root := t.TempDir()
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRsSequence(t, [][]int{{}, {41, 42}})
	var attempts []stallMailCall
	recordStallMail(t, &attempts, nil)

	state := seedStallState(t, root, map[string]dispatchEntry{stallKey: enhancementHandoffEntry(complete.ID)})
	stats := &dispatchCycleStats{start: time.Now()}
	wantStored := []string{handoffReason(0), handoffReason(2), handoffReason(2)}
	for tick := 1; tick <= 3; tick++ {
		stallTick(t, root, fake, &state, stats, cfg, issueItem("enhancement"), "issue")
		if got := state.Dispatched[stallKey].Stall; got != wantStored[tick-1] {
			t.Errorf("tick %d: stored Stall = %q, want %q", tick, got, wantStored[tick-1])
		}
	}

	if got, want := stallReasons(attempts), []string{handoffReason(0), handoffReason(2)}; !equalStrs(got, want) {
		t.Errorf("stall mail reasons = %q, want %q (a new reason mails again, a repeated one does not)", got, want)
	}
	if stats.errors != 3 {
		t.Errorf("stats.errors = %d, want 3", stats.errors)
	}
}

func TestWorkflowStall_SurvivesPruneAndReload(t *testing.T) {
	root := t.TempDir()
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	editErr := errors.New("label API 502")
	edits := 0
	orig := editItemLabels
	editItemLabels = func(repo string, number int, source string, add, remove []string) error {
		edits++
		return editErr
	}
	t.Cleanup(func() { editItemLabels = orig })
	var slung, ids []string
	recordSlings(t, store, &slung, &ids)
	var attempts []stallMailCall
	recordStallMail(t, &attempts, nil)

	state := seedStallState(t, root, nil)
	stats := &dispatchCycleStats{start: time.Now()}
	item := issueItem() // no phase label ⇒ bootstrap, whose label edit fails before any entry exists
	reason := fmt.Sprintf("workflow %q bootstrap label edit failed: %v", "feature-workflow", editErr)
	wantEntry := dispatchEntry{ItemURL: item.URL, Source: "issue", Workflow: "feature-workflow", Stall: reason}
	for tick := 1; tick <= 3; tick++ {
		stallTick(t, root, fake, &state, stats, cfg, item, "issue")
		e, ok := state.Dispatched[stallKey]
		if !ok {
			t.Fatalf("tick %d: no entry survived prune + reload (a re-created entry needs Workflow so prune keeps it)", tick)
		}
		if e != wantEntry {
			t.Errorf("tick %d: entry = %+v, want exactly %+v (no DispatchedAt, no phase record)", tick, e, wantEntry)
		}
	}

	if got := stallReasons(attempts); !equalStrs(got, []string{reason}) {
		t.Errorf("stall mail reasons = %q, want exactly [%q]", got, reason)
	}
	if edits != 3 || stats.errors != 3 || len(slung) != 0 {
		t.Errorf("label edits = %d, stats.errors = %d, slung = %v; want 3, 3, none", edits, stats.errors, slung)
	}
}

func TestWorkflowStall_FailedSendIsRetriedNextCycle(t *testing.T) {
	root := t.TempDir()
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{})
	var attempts []stallMailCall
	sent := recordStallMail(t, &attempts, func(n int) error {
		if n == 1 {
			return errors.New("mail store down")
		}
		return nil
	})

	state := seedStallState(t, root, map[string]dispatchEntry{stallKey: enhancementHandoffEntry(complete.ID)})
	stats := &dispatchCycleStats{start: time.Now()}
	want := handoffReason(0)

	stderr := stallTick(t, root, fake, &state, stats, cfg, issueItem("enhancement"), "issue")
	if !strings.Contains(stderr, "warning: workflow-stall mail for "+stallKey+" failed: mail store down\n") {
		t.Errorf("tick 1 stderr = %q, want the failed-send warning", stderr)
	}
	if !strings.Contains(stderr, "stall "+stallKey+": "+want+"\n") {
		t.Errorf("tick 1 stderr = %q, want the stall line kept alongside the warning", stderr)
	}
	if got := state.Dispatched[stallKey].Stall; got != "" {
		t.Errorf("after a failed send Stall = %q, want empty so the next tick retries", got)
	}
	for tick := 2; tick <= 3; tick++ {
		stallTick(t, root, fake, &state, stats, cfg, issueItem("enhancement"), "issue")
		if got := state.Dispatched[stallKey].Stall; got != want {
			t.Errorf("tick %d: stored Stall = %q, want %q", tick, got, want)
		}
	}

	if len(attempts) != 2 || *sent != 1 {
		t.Errorf("send attempts = %d, successful = %d; want 2 and 1 (a failed send is retried once, then dedupes)", len(attempts), *sent)
	}
	if stats.errors != 3 {
		t.Errorf("stats.errors = %d, want 3 (a failed stall mail is a warning, not a cycle error)", stats.errors)
	}
}

func TestWorkflowStall_DryRunNeitherMailsNorPersists(t *testing.T) {
	t.Run("an entry present and unstalled is left untouched", func(t *testing.T) {
		root := t.TempDir()
		fake, store := setupHermeticSessions(t)
		setDispatchDryRun(t)
		cfg := crossSourceCfg()
		complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
		stubLinkedPRs(t, []int{})
		var attempts []stallMailCall
		recordStallMail(t, &attempts, nil)

		before := enhancementHandoffEntry(complete.ID)
		state := seedStallState(t, root, map[string]dispatchEntry{stallKey: before})
		stats := &dispatchCycleStats{start: time.Now()}
		for tick := 1; tick <= 3; tick++ {
			stderr := stallTick(t, root, fake, &state, stats, cfg, issueItem("enhancement"), "issue")
			if !strings.Contains(stderr, "stall "+stallKey+": ") {
				t.Errorf("tick %d stderr = %q, want the stall line under dry run too", tick, stderr)
			}
			if got := state.Dispatched[stallKey]; got != before {
				t.Errorf("tick %d: record = %+v, want unchanged %+v", tick, got, before)
			}
		}
		if len(attempts) != 0 || stats.errors != 3 {
			t.Errorf("dry run: stall mails = %+v, stats.errors = %d; want none and 3", attempts, stats.errors)
		}
	})

	t.Run("an entry-less stall creates no entry", func(t *testing.T) {
		root := t.TempDir()
		fake, _ := setupHermeticSessions(t)
		setDispatchDryRun(t)
		cfg := crossSourceCfg()
		var attempts []stallMailCall
		recordStallMail(t, &attempts, nil)

		state := seedStallState(t, root, nil)
		stats := &dispatchCycleStats{start: time.Now()}
		for tick := 1; tick <= 3; tick++ {
			stallTick(t, root, fake, &state, stats, cfg, issueItem("enhancement", "pr-review"), "issue")
			if e, ok := state.Dispatched[stallKey]; ok {
				t.Errorf("tick %d: dry run created entry %+v", tick, e)
			}
		}
		if len(attempts) != 0 || stats.errors != 3 {
			t.Errorf("dry run: stall mails = %+v, stats.errors = %d; want none and 3", attempts, stats.errors)
		}
	})

	t.Run("a stored reason survives a dry-run completion", func(t *testing.T) {
		root := t.TempDir()
		fake, store := setupHermeticSessions(t)
		setDispatchDryRun(t)
		cfg := crossSourceCfg()
		complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
		stubLinkedPRs(t, []int{42})
		stubItemState(t, "OPEN")
		var attempts []stallMailCall
		recordStallMail(t, &attempts, nil)

		seeded := terminalIterateEntry(complete.ID)
		seeded.Stall = terminalReason(0)
		state := seedStallState(t, root, map[string]dispatchEntry{stallKey: seeded})
		stats := &dispatchCycleStats{start: time.Now()}
		for tick := 1; tick <= 3; tick++ {
			stallTick(t, root, fake, &state, stats, cfg, issueItem("pr-iterate"), "issue")
			if got := state.Dispatched[stallKey].Stall; got != seeded.Stall {
				t.Errorf("tick %d: Stall = %q, want %q kept (a dry run never writes)", tick, got, seeded.Stall)
			}
		}
		if len(attempts) != 0 || stats.dispatched != 3 {
			t.Errorf("dry run: stall mails = %+v, stats.dispatched = %d; want none and 3 would-completes", attempts, stats.dispatched)
		}
	})

	t.Run("a stored reason survives a dry-run stall with a different reason", func(t *testing.T) {
		root := t.TempDir()
		fake, store := setupHermeticSessions(t)
		setDispatchDryRun(t)
		cfg := crossSourceCfg()
		complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
		stubLinkedPRs(t, []int{})
		var attempts []stallMailCall
		recordStallMail(t, &attempts, nil)

		seeded := enhancementHandoffEntry(complete.ID)
		seeded.Stall = "an earlier reason"
		state := seedStallState(t, root, map[string]dispatchEntry{stallKey: seeded})
		stats := &dispatchCycleStats{start: time.Now()}
		for tick := 1; tick <= 3; tick++ {
			stallTick(t, root, fake, &state, stats, cfg, issueItem("enhancement"), "issue")
			if got := state.Dispatched[stallKey].Stall; got != seeded.Stall {
				t.Errorf("tick %d: Stall = %q, want %q kept", tick, got, seeded.Stall)
			}
		}
		if len(attempts) != 0 {
			t.Errorf("dry run mailed %+v, want nothing", attempts)
		}
	})
}

// A stall ends only on positive evidence. Under #755 D1 the terminal's evidence is the PR's state: a
// closed PR stalls and mails, and the same PR reopened completes the item and drops its record.
func TestWorkflowStall_ClosedPRStallEndsWhenThePRReopens(t *testing.T) {
	root := t.TempDir()
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
	stubLinkedPRs(t, []int{42})
	states := []string{"CLOSED", "OPEN"}
	stubItemStates(t, func(string, int, string) (string, error) {
		if len(states) == 0 {
			return "", errors.New("unexpected third state read")
		}
		st := states[0]
		states = states[1:]
		return st, nil
	})
	var attempts []stallMailCall
	recordStallMail(t, &attempts, nil)
	var mails []string
	recordWorkflowMail(t, &mails)
	var edits []labelEdit
	recordLabelEdits(t, &edits)
	var slung, ids []string
	recordSlings(t, store, &slung, &ids)

	state := seedStallState(t, root, map[string]dispatchEntry{stallKey: terminalIterateEntry(complete.ID)})
	stats := &dispatchCycleStats{start: time.Now()}

	stallTick(t, root, fake, &state, stats, cfg, issueItem("pr-iterate"), "issue")
	closed := fmt.Sprintf("workflow %q terminal pr phase %q: linked PR #42 is closed without merging", "feature-workflow", "pr-iterate")
	if got := state.Dispatched[stallKey].Stall; got != closed {
		t.Errorf("tick 1: Stall = %q, want %q", got, closed)
	}
	stallTick(t, root, fake, &state, stats, cfg, issueItem("pr-iterate"), "issue")
	if e, ok := state.Dispatched[stallKey]; ok {
		t.Errorf("tick 2 (reopened): record = %+v, want it dropped by completion", e)
	}
	if got := stallReasons(attempts); !equalStrs(got, []string{closed}) {
		t.Errorf("stall mail reasons = %q, want exactly [%q]", got, closed)
	}
	if want := []string{"manager|feature-workflow|" + stallKey}; !equalStrs(mails, want) {
		t.Errorf("completion mails = %v, want %v", mails, want)
	}
	if len(slung) != 0 {
		t.Errorf("slung = %v, want nothing: a closed-PR stall and its reopening re-sling no phase", slung)
	}
	if stats.errors != 1 || stats.dispatched != 1 {
		t.Errorf("stats.errors = %d, stats.dispatched = %d; want 1 and 1", stats.errors, stats.dispatched)
	}
}

func TestWorkflowStall_CeilingMailsOnceAcrossThreeCycles(t *testing.T) {
	root := t.TempDir()
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	notComplete := seedClosedEpic(t, store, config.CloseReasonResetSling)
	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)
	var attempts []stallMailCall
	recordStallMail(t, &attempts, nil)

	seeded := dispatchEntry{
		Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
		PhaseInstanceID: notComplete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour).UTC(),
		Attempts: maxWorkflowAttempts,
	}
	state := seedStallState(t, root, map[string]dispatchEntry{stallKey: seeded})
	stats := &dispatchCycleStats{start: time.Now()}
	reason := fmt.Sprintf("workflow %q phase %q exceeded the re-sling ceiling (%d attempts)",
		"feature-workflow", "enhancement", maxWorkflowAttempts)
	want := seeded
	want.Stall = reason
	for tick := 1; tick <= 3; tick++ {
		stderr := stallTick(t, root, fake, &state, stats, cfg, issueItem("enhancement"), "issue")
		if !strings.Contains(stderr, "ceiling") {
			t.Errorf("tick %d stderr = %q, want the ceiling stall", tick, stderr)
		}
		if got := state.Dispatched[stallKey]; got != want {
			t.Errorf("tick %d: record = %+v, want %+v", tick, got, want)
		}
	}

	if got := stallReasons(attempts); !equalStrs(got, []string{reason}) {
		t.Errorf("stall mail reasons = %q, want exactly [%q]", got, reason)
	}
	if stats.errors != 3 || len(slung) != 0 || len(edits) != 0 {
		t.Errorf("stats.errors = %d, slung = %v, edits = %v; want 3, none, none", stats.errors, slung, edits)
	}
}

func TestWorkflowStall_DeleteThenStallMailsOnce(t *testing.T) {
	root := t.TempDir()
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	open := seedOpenEpic(t, store)
	stubLinkedPRs(t, []int{})
	var slung, ids []string
	recordSlings(t, store, &slung, &ids)
	var attempts []stallMailCall
	recordStallMail(t, &attempts, nil)

	item := issueItem("pr-review")
	state := seedStallState(t, root, map[string]dispatchEntry{stallKey: {
		Agent: "review", Workflow: "feature-workflow", Phase: "pr-review",
		PhaseInstanceID: open.ID, PhaseDispatchedAt: time.Now().Add(-100 * time.Hour).UTC(),
	}})
	stats := &dispatchCycleStats{start: time.Now()}
	reason := fmt.Sprintf("workflow %q phase %q %v", "feature-workflow", "pr-review",
		fmt.Errorf("%w for %q: %w", errResolvePhaseInput, "pr-review",
			fmt.Errorf("issue→pr handoff at phase %q resolved %d linked PRs (want exactly 1)", "pr-review", 0)))
	wantEntry := dispatchEntry{Agent: "review", ItemURL: item.URL, Source: "issue", Workflow: "feature-workflow", Stall: reason}
	for tick := 1; tick <= 3; tick++ {
		stallTick(t, root, fake, &state, stats, cfg, item, "issue")
		e, ok := state.Dispatched[stallKey]
		if !ok {
			t.Fatalf("tick %d: the entry resling deleted was not re-created", tick)
		}
		if e != wantEntry {
			t.Errorf("tick %d: entry = %+v, want exactly %+v (no phase record, no attempt burned)", tick, e, wantEntry)
		}
	}

	if got := stallReasons(attempts); !equalStrs(got, []string{reason}) {
		t.Errorf("stall mail reasons = %q, want exactly [%q]", got, reason)
	}
	if stats.errors != 3 || len(slung) != 0 {
		t.Errorf("stats.errors = %d, slung = %v; want 3 and none", stats.errors, slung)
	}
}

func TestWorkflowStall_TransientGhErrorDoesNotClear(t *testing.T) {
	cases := []struct {
		name   string
		entry  func(id string) dispatchEntry
		phase  string
		reason string
	}{
		{"issue→pr handoff", enhancementHandoffEntry, "enhancement", handoffReason(0)},
		{"terminal pr phase", terminalIterateEntry, "pr-iterate", terminalReason(0)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			root := t.TempDir()
			fake, store := setupHermeticSessions(t)
			cfg := crossSourceCfg()
			complete := seedClosedEpic(t, store, config.CloseReasonFormulaComplete)
			calls := stubLinkedPRsFunc(t, func(n int) ([]int, error) {
				if n == 2 {
					return nil, errors.New("HTTP 403: API rate limit exceeded")
				}
				return []int{}, nil
			})
			var attempts []stallMailCall
			recordStallMail(t, &attempts, nil)

			state := seedStallState(t, root, map[string]dispatchEntry{stallKey: c.entry(complete.ID)})
			stats := &dispatchCycleStats{start: time.Now()}
			for tick := 1; tick <= 3; tick++ {
				stderr := stallTick(t, root, fake, &state, stats, cfg, issueItem(c.phase), "issue")
				if got := state.Dispatched[stallKey].Stall; got != c.reason {
					t.Errorf("tick %d: Stall = %q, want %q (a transient gh error defers, never clears)", tick, got, c.reason)
				}
				hold := fmt.Sprintf("hold %s: workflow %q phase %q: ", stallKey, "feature-workflow", c.phase)
				if tick == 2 && (!strings.Contains(stderr, hold) || !strings.Contains(stderr, "HTTP 403: API rate limit exceeded")) {
					t.Errorf("tick 2 stderr = %q, want a %q line naming the gh error (a held item is never silent)", stderr, hold)
				}
				if line := regexp.MustCompile(`(?m)^retry .*$`).FindString(stderr); line != "" {
					t.Errorf("tick %d stderr has %q: a held item must not share the prefix of the lines that dispatch again", tick, line)
				}
			}

			if *calls != 3 {
				t.Errorf("ghLinkedPRs calls = %d, want 3", *calls)
			}
			if got := stallReasons(attempts); !equalStrs(got, []string{c.reason}) {
				t.Errorf("stall mail reasons = %q, want exactly [%q]", got, c.reason)
			}
			if stats.skipped != 1 || stats.errors != 2 {
				t.Errorf("stats.skipped = %d, stats.errors = %d; want 1 and 2", stats.skipped, stats.errors)
			}

			stubLinkedPRsFunc(t, func(int) ([]int, error) { return nil, errors.New("HTTP 502") })
			outcome, reason := evaluatePhase(context.Background(), store, "owner/repo", issueItem(c.phase), "issue",
				c.entry(complete.ID), cfg.Mappings, &cfg.Workflows[0], c.phase)
			if outcome != phaseRetry || !strings.Contains(reason, "HTTP 502") {
				t.Errorf("evaluatePhase on a gh error = (%v, %q), want phaseRetry with a reason naming the error", outcome, reason)
			}
		})
	}
}

func TestWorkflowStall_HaltedAgentIsNotMailed(t *testing.T) {
	realRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	fake, store := setupHermeticSessions(t)
	cfg := crossSourceCfg()
	if err := saveRecoveryState(realRoot, "impl", recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3}); err != nil {
		t.Fatalf("plant breaker: %v", err)
	}
	fake.present[session.SessionName("impl")] = true
	notComplete := seedClosedEpic(t, store, config.CloseReasonResetSling)
	var edits []labelEdit
	var slung, ids []string
	recordLabelEdits(t, &edits)
	recordSlings(t, store, &slung, &ids)
	var attempts []stallMailCall
	recordStallMail(t, &attempts, nil)

	before := dispatchEntry{
		Agent: "impl", Workflow: "feature-workflow", Phase: "enhancement",
		PhaseInstanceID: notComplete.ID, PhaseDispatchedAt: time.Now().Add(-time.Hour).UTC(), Attempts: 1,
	}
	state := seedStallState(t, realRoot, map[string]dispatchEntry{stallKey: before})
	stats := &dispatchCycleStats{start: time.Now()}
	for tick := 1; tick <= 3; tick++ {
		stderr := stallTick(t, realRoot, fake, &state, stats, cfg, issueItem("enhancement"), "issue")
		if !strings.Contains(stderr, "stall "+stallKey+": agent impl recovery breaker is halted") {
			t.Errorf("tick %d stderr = %q, want the halted stall line", tick, stderr)
		}
		if got := state.Dispatched[stallKey]; got != before {
			t.Errorf("tick %d: record = %+v, want unchanged %+v", tick, got, before)
		}
	}

	if len(attempts) != 0 {
		t.Errorf("a halted agent was mailed %+v; the breaker's own escalation already reports it", attempts)
	}
	data, err := os.ReadFile(filepath.Join(realRoot, ".runtime", "dispatch-state.json"))
	if err != nil {
		t.Fatalf("read state file: %v", err)
	}
	if strings.Contains(string(data), `"stall"`) {
		t.Errorf("a halted stall was persisted:\n%s", data)
	}
	if stats.errors != 3 || len(slung) != 0 || len(edits) != 0 {
		t.Errorf("stats.errors = %d, slung = %v, edits = %v; want 3, none, none", stats.errors, slung, edits)
	}
}

func TestDispatchStatus_JSON_StallFieldIsAdditive(t *testing.T) {
	now := time.Now().UTC()
	reason := handoffReason(0)
	plain := dispatchEntry{Agent: "impl", ItemURL: "u", Source: "issue", DispatchedAt: now,
		Workflow: "feature-workflow", Phase: "enhancement"}
	stalled := plain
	stalled.Stall = reason

	cmd, out, _ := phase3Cmd()
	entries := map[string]dispatchEntry{"owner/repo#1": plain, "owner/repo#2": stalled}
	if err := emitDispatchStatusJSON(cmd, false, entries, map[string]bool{}, map[string]bool{}, map[string]string{}, nil); err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Entries []map[string]json.RawMessage `json:"entries"`
	}
	if err := json.Unmarshal(out.Bytes(), &doc); err != nil {
		t.Fatalf("status JSON: %v\n%s", err, out)
	}
	if len(doc.Entries) != 2 {
		t.Fatalf("entries = %d, want 2:\n%s", len(doc.Entries), out)
	}
	if _, ok := doc.Entries[0]["stall"]; ok {
		t.Errorf("an unstalled row carries a \"stall\" key; omitempty must elide it:\n%s", out)
	}
	raw, ok := doc.Entries[1]["stall"]
	if !ok {
		t.Fatalf("the stalled row lacks \"stall\":\n%s", out)
	}
	var got string
	if err := json.Unmarshal(raw, &got); err != nil || got != reason {
		t.Errorf("stall = %s (%v), want %q", raw, err, reason)
	}
	if len(doc.Entries[1]) != len(doc.Entries[0])+1 {
		t.Errorf("the stalled row has %d keys and the plain one %d; stall must be the only addition",
			len(doc.Entries[1]), len(doc.Entries[0]))
	}
}

func TestDispatch_UnmatchedTriggerItemLogsOneLine(t *testing.T) {
	cmd, out, errBuf := phase3Cmd()
	stats := &dispatchCycleStats{start: time.Now()}
	item := ghItem{Number: 7, URL: "https://github.com/owner/repo/issues/7", Labels: labels("agentic", "question", "docs")}

	reportUnmatchedItem(cmd, stats, "owner/repo", item)

	want := "skip owner/repo#7: no mapping or workflow matches its labels (agentic, question, docs)\n"
	if errBuf.String() != want {
		t.Errorf("stderr = %q, want exactly %q", errBuf.String(), want)
	}
	if out.Len() != 0 {
		t.Errorf("stdout = %q, want nothing (the line is diagnostic)", out.String())
	}
	if stats.skipped != 1 || stats.errors != 0 || stats.dispatched != 0 {
		t.Errorf("stats = %+v, want only skipped 1", *stats)
	}

	reportUnmatchedItem(cmd, stats, "owner/repo", item)
	if strings.Count(errBuf.String(), "\n") != 2 || stats.skipped != 2 {
		t.Errorf("a second call printed %q with skipped %d; want a second line and skipped 2", errBuf.String(), stats.skipped)
	}
}

// The item loop of runDispatch cannot be driven hermetically (it shells out to gh), so the only
// mechanical proof that its unmatched skip logs is that the site calls the tested helper.
func TestDispatch_UnmatchedSkipSiteUsesTheSharedHelper(t *testing.T) {
	data, err := os.ReadFile("dispatch.go")
	if err != nil {
		t.Fatalf("read dispatch.go: %v", err)
	}
	src := string(data)

	const startAnchor = "agent, model := matchItemToAgent(item, itemMappings[i])"
	const endAnchor = "dispatchNonWorkflowItem(cmd, root, t, &state, stats,"
	if n := strings.Count(src, startAnchor); n != 1 {
		t.Fatalf("%q appears %d×, want 1", startAnchor, n)
	}
	start := strings.Index(src, startAnchor)
	end := strings.Index(src[start:], endAnchor)
	if end < 0 {
		t.Fatalf("no %q after the unmatched-item check", endAnchor)
	}
	block := src[start : start+end]

	if !regexp.MustCompile(`if agent == "" \{\s*reportUnmatchedItem\(cmd, stats, repo, item\)\s*continue\s*\}`).MatchString(block) {
		t.Errorf("the unmatched-item branch does not call reportUnmatchedItem(cmd, stats, repo, item) then continue:\n%s", block)
	}
	if strings.Contains(block, "stats.skipped++") {
		t.Errorf("the unmatched-item branch still counts a bare skip:\n%s", block)
	}
	if n := strings.Count(src, "func reportUnmatchedItem("); n != 1 {
		t.Errorf("func reportUnmatchedItem( appears %d×, want 1", n)
	}
}
