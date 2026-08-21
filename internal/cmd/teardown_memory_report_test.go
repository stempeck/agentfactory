package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// T-REPORT (AC-626-6). Every teardown path preserves the vault by construction — it lives in a
// different subtree from worktrees/ and agents/, so none of them can reach it. What these tests
// hold is the half that CAN regress: that the operator watching a teardown scroll by is TOLD.
//
// Two clauses beyond "the line is present" carry the acceptance criterion:
//   - suppression at N=0, so a factory that never recorded a learning shows no new output at all;
//   - ORDERING, asserted against the removal ITSELF, so the report is a statement about a vault
//     that is still standing rather than a claim made after the fact.
//
// Ordering is asserted with atRemovalTime rather than by comparing line positions. The criterion
// says "before worktree removal", and removal is a mutation of the on-disk registration, not a
// line of text: a report emitted between RemoveAgent and its announcement reads perfectly and is
// still made after the fact, and no output-order comparison can tell those two apart. So the probe
// asks the disk at the instant the line is written.
//
// The GC path is deliberately absent here: it is best-effort on internal/worktree's package
// stderr writer, which is unreachable from this package. Its assertion lives beside TestGC_* in
// internal/worktree.

const preservedMarker = "memory preserved:"

// preservedLineFor is the exact string a teardown must carry — composed the way an operator
// reads it, not the way the code builds it.
func preservedLineFor(agent string, n int) string {
	return fmt.Sprintf("memory preserved: %s%c (%d notes)",
		filepath.Join(".agentfactory", "memory", agent), filepath.Separator, n)
}

// teardownFixture builds a temp factory root owning one worktree for agentName, registers any
// coTenants alongside it, and seeds noteCount notes into agentName's vault.
func teardownFixture(t *testing.T, agentName string, noteCount int, coTenants ...string) (root, wtID string) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("eval symlinks: %v", err)
	}
	wtID = "wt-solo01"
	meta := &worktree.Meta{
		ID:     wtID,
		Owner:  agentName,
		Branch: "af/" + agentName + "-solo01",
		Path:   filepath.Join(".agentfactory", "worktrees", wtID),
		Agents: append([]string{agentName}, coTenants...),
	}
	if err := worktree.WriteMeta(root, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}
	for i := 0; i < noteCount; i++ {
		seedNote(t, root, agentName, memory.Note{
			ID:   fmt.Sprintf("teardown-note-%d", i),
			Body: "a learning worth keeping\n",
		})
	}
	return root, wtID
}

// atRemovalTime is a writer that answers one question: was the agent still registered on disk at
// the instant the preserved-memory line was written? meta.Agents is the list RemoveAgent narrows
// and persists, and that ForceRemove deletes outright with the rest of the meta, so membership in
// it goes false at exactly the moment "removal" happens on either teardown path.
//
// It reads the meta directly rather than through worktree.FindByAgent, which falls back to
// matching meta.Owner and so keeps answering yes for the very agent that was just deregistered —
// a fallback that makes the probe blind to the violation it exists to catch.
type atRemovalTime struct {
	buf               bytes.Buffer
	root, wtID, agent string
	written           bool
	registered        bool
}

func (a *atRemovalTime) Write(p []byte) (int, error) {
	if !a.written && strings.Contains(string(p), preservedMarker) {
		a.written = true
		a.registered = false
		if meta, err := worktree.ReadMeta(a.root, a.wtID); err == nil && meta != nil {
			for _, name := range meta.Agents {
				if name == a.agent {
					a.registered = true
				}
			}
		}
	}
	return a.buf.Write(p)
}

// assertReportedBeforeRemoval fails unless the line was written at all AND the agent was still
// registered when it was. The `written` guard is load-bearing: a run that never emits the line
// leaves registered false for the wrong reason, and without the guard the failure would blame
// ordering for what is actually a missing report.
func (a *atRemovalTime) assertReportedBeforeRemoval(t *testing.T) {
	t.Helper()
	if !a.written {
		t.Fatalf("output is missing %q:\n%s", preservedMarker, a.buf.String())
	}
	if !a.registered {
		t.Errorf("%q was written after %s had already been removed from its worktree — the "+
			"report must describe a vault that is still standing:\n%s",
			preservedMarker, a.agent, a.buf.String())
	}
}

func TestTeardownMemoryReport_DownReportsPreservedVault(t *testing.T) {
	root, wtID := teardownFixture(t, "solver", 3, "reviewer")

	cmd := &cobra.Command{}
	w := &atRemovalTime{root: root, wtID: wtID, agent: "solver"}
	cmd.SetOut(w)

	cleanupAgentWorktree(cmd, root, "solver")

	out := w.buf.String()
	if want := preservedLineFor("solver", 3); !strings.Contains(out, want) {
		t.Errorf("af down output missing %q, got:\n%s", want, out)
	}
	if !strings.Contains(out, "deregistered from worktree "+wtID) {
		t.Errorf("af down must report the teardown it did, got:\n%s", out)
	}
	w.assertReportedBeforeRemoval(t)
}

func TestTeardownMemoryReport_DownSuppressesAtZeroNotes(t *testing.T) {
	root, wtID := teardownFixture(t, "solver", 0, "reviewer")

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	cleanupAgentWorktree(cmd, root, "solver")

	out := buf.String()
	if strings.Contains(out, preservedMarker) {
		t.Errorf("af down must stay silent when no note was preserved, got:\n%s", out)
	}
	if !strings.Contains(out, "deregistered from worktree "+wtID) {
		t.Errorf("af down must still report the teardown it did, got:\n%s", out)
	}
}

// The in-flight-formula guard returns without removing anything. Claiming preservation there
// would tell the operator a teardown happened when none did.
func TestTeardownMemoryReport_DownSilentWhenNothingIsTornDown(t *testing.T) {
	root, wtID := teardownFixture(t, "solver", 3)
	hookedDir := filepath.Join(root, ".agentfactory", "worktrees", wtID,
		".agentfactory", "agents", "solver", ".runtime")
	if err := os.MkdirAll(hookedDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(hookedDir, "hooked_formula"), []byte("some-formula"), 0o644); err != nil {
		t.Fatalf("writing hooked_formula: %v", err)
	}

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)

	cleanupAgentWorktree(cmd, root, "solver")

	out := buf.String()
	if !strings.Contains(out, "kept worktree") {
		t.Fatalf("fixture did not reach the in-flight guard, got:\n%s", out)
	}
	if strings.Contains(out, preservedMarker) {
		t.Errorf("af down must not claim preservation on a run that tore nothing down, got:\n%s", out)
	}
}

func TestTeardownMemoryReport_ResetNamesBothFacts(t *testing.T) {
	root, wtID := teardownFixture(t, "solver", 3, "reviewer")
	store := installMemStore(t)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := store.Create(ctx, issuestore.CreateParams{
			Title:    "solver-bead",
			Assignee: "solver",
			Type:     issuestore.TypeTask,
		}); err != nil {
			t.Fatalf("creating bead: %v", err)
		}
	}

	w := &atRemovalTime{root: root, wtID: wtID, agent: "solver"}
	if err := resetAgentState(ctx, w, root, "solver", "test-reason"); err != nil {
		t.Fatalf("resetAgentState: %v", err)
	}

	out := w.buf.String()
	if want := preservedLineFor("solver", 3); !strings.Contains(out, want) {
		t.Errorf("reset output missing %q, got:\n%s", want, out)
	}
	if !strings.Contains(out, "memory vault NOT touched by --reset (beads closed: 2)") {
		t.Errorf("reset must name what it destroyed alongside what it did not touch, got:\n%s", out)
	}
	if !strings.Contains(out, "deregistered from worktree "+wtID) {
		t.Errorf("reset must report the teardown it did, got:\n%s", out)
	}
	w.assertReportedBeforeRemoval(t)
}

func TestTeardownMemoryReport_ResetSuppressesAtZeroNotes(t *testing.T) {
	root, _ := teardownFixture(t, "solver", 0)
	installMemStore(t)

	var buf bytes.Buffer
	if err := resetAgentState(context.Background(), &buf, root, "solver", "test-reason"); err != nil {
		t.Fatalf("resetAgentState: %v", err)
	}

	// Both memory lines are suppressed together: at zero notes there is no agent-authored state,
	// so there is nothing to reassure the operator about either.
	out := buf.String()
	if strings.Contains(out, preservedMarker) {
		t.Errorf("reset must stay silent when no note was preserved, got:\n%s", out)
	}
	if strings.Contains(out, "memory vault") {
		t.Errorf("reset must not describe a vault that holds nothing, got:\n%s", out)
	}
}

// The vault-untouched fact is reported even with no beads to close, because the operator's
// question on a --reset is what it destroyed, and "nothing here" is the answer.
func TestTeardownMemoryReport_ResetNamesVaultWithZeroBeads(t *testing.T) {
	root, _ := teardownFixture(t, "solver", 1)
	installMemStore(t)

	var buf bytes.Buffer
	if err := resetAgentState(context.Background(), &buf, root, "solver", "test-reason"); err != nil {
		t.Fatalf("resetAgentState: %v", err)
	}

	if out := buf.String(); !strings.Contains(out, "memory vault NOT touched by --reset (beads closed: 0)") {
		t.Errorf("reset must name the vault even with no beads closed, got:\n%s", out)
	}
}

func TestTeardownMemoryReport_DoneReportsPreservedVault(t *testing.T) {
	root, wtID := teardownFixture(t, "solver", 3, "reviewer")
	cwd := newDispatchedSessionDir(t, root, wtID)
	t.Setenv("AF_ROLE", "solver")

	out := captureStderr(t, func() { finishDispatchedSession(cwd, root) })

	if want := preservedLineFor("solver", 3); !strings.Contains(out, want) {
		t.Errorf("af done stderr missing %q, got:\n%s", want, out)
	}
}

func TestTeardownMemoryReport_DoneSuppressesAtZeroNotes(t *testing.T) {
	root, wtID := teardownFixture(t, "solver", 0, "reviewer")
	cwd := newDispatchedSessionDir(t, root, wtID)
	t.Setenv("AF_ROLE", "solver")

	out := captureStderr(t, func() { finishDispatchedSession(cwd, root) })

	if strings.Contains(out, preservedMarker) {
		t.Errorf("af done must stay silent when no note was preserved, got:\n%s", out)
	}
}

// assertEmittedBefore is the line-order assertion, used only where the second line is emitted BY
// the removal call rather than after it — the failed-RemoveAgent warning below. Everywhere else
// the atRemovalTime probe is stricter and is what these tests use.
//
// The >= 0 guards are load-bearing: strings.Index returns -1 for an absent line, and -1 < n is
// true, so an unguarded comparison passes vacuously on exactly the output it exists to reject.
func assertEmittedBefore(t *testing.T, out, first, second string) {
	t.Helper()
	i := strings.Index(out, first)
	j := strings.Index(out, second)
	if i < 0 {
		t.Fatalf("output is missing %q:\n%s", first, out)
	}
	if j < 0 {
		t.Fatalf("output is missing %q:\n%s", second, out)
	}
	if i >= j {
		t.Errorf("%q must be emitted before %q, got:\n%s", first, second, out)
	}
}

// done prints nothing on a successful removal, so ordering is asserted against the warning a
// FAILED removal emits: the report must already be on the stream by the time RemoveAgent runs.
func TestTeardownMemoryReport_DoneReportsBeforeWorktreeRemoval(t *testing.T) {
	root, _ := teardownFixture(t, "solver", 3)
	cwd := newDispatchedSessionDir(t, root, "wt-ghost")
	t.Setenv("AF_ROLE", "solver")

	out := captureStderr(t, func() { finishDispatchedSession(cwd, root) })

	assertEmittedBefore(t, out, preservedMarker, "worktree RemoveAgent")
}

// newDispatchedSessionDir builds the .runtime state finishDispatchedSession reads: the worktree
// id it is tearing down and the owner flag that sends it down the removal branch.
func newDispatchedSessionDir(t *testing.T, root, wtID string) string {
	t.Helper()
	cwd := filepath.Join(root, "session")
	runtimeDir := filepath.Join(cwd, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "worktree_id"), []byte(wtID), 0o644); err != nil {
		t.Fatalf("writing worktree_id: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "worktree_owner"), []byte("true"), 0o644); err != nil {
		t.Fatalf("writing worktree_owner: %v", err)
	}
	return cwd
}
