package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// Phase 4 (issue #392) K8 tests: the durable AC-4 channel (factory-root
// breadcrumb + escalation mail) and the watchdog relocation-safety fix.

// TestReconstructHookedFormula_ReturnsRecovered: a single open in-flight epic
// returns a recoveryResult flagged Recovered with the rebound instance id, so
// the post-loop aggregation can record the per-agent outcome.
func TestReconstructHookedFormula_ReturnsRecovered(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	epic := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, epic.ID, agent)

	var out, errw bytes.Buffer
	rr := reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if !rr.Recovered {
		t.Errorf("Recovered = false, want true")
	}
	if rr.InstanceID != epic.ID {
		t.Errorf("InstanceID = %q, want %q", rr.InstanceID, epic.ID)
	}
	if rr.Ambiguous {
		t.Errorf("Ambiguous = true, want false on single match")
	}
}

// TestReconstructHookedFormula_ReturnsAmbiguous: >1 open in-flight instance
// returns a recoveryResult flagged Ambiguous with the open count — the AC-4
// signal the breadcrumb and escalation mail consume.
func TestReconstructHookedFormula_ReturnsAmbiguous(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	e1 := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, e1.ID, agent)
	e2 := newInstanceEpic(t, mem, agent)
	newChildStep(t, mem, e2.ID, agent)

	var out, errw bytes.Buffer
	rr := reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if !rr.Ambiguous {
		t.Errorf("Ambiguous = false, want true on >1 open instance")
	}
	if rr.OpenCount != 2 {
		t.Errorf("OpenCount = %d, want 2", rr.OpenCount)
	}
	if rr.Recovered {
		t.Errorf("Recovered = true, want false on ambiguous case")
	}
	// Existing byte-for-byte stderr WARNING must be preserved (no regression).
	want := "WARNING: " + agent + ": 2 open formula instances — cannot auto-resume; resolve manually\n"
	if errw.String() != want {
		t.Errorf("stderr = %q, want %q (existing print must be unchanged)", errw.String(), want)
	}
}

// TestReconstructHookedFormula_ReturnsZeroOnNoMatch: a closed/no-match epic
// yields the zero recoveryResult (not recovered, not ambiguous).
func TestReconstructHookedFormula_ReturnsZeroOnNoMatch(t *testing.T) {
	mem := installMemStore(t)
	agent := "alice"
	agentDir := t.TempDir()

	epic := newInstanceEpic(t, mem, agent)
	child := newChildStep(t, mem, epic.ID, agent)
	if err := mem.Close(context.Background(), child.ID, ""); err != nil {
		t.Fatalf("close child: %v", err)
	}
	if err := mem.Close(context.Background(), epic.ID, "done"); err != nil {
		t.Fatalf("close epic: %v", err)
	}

	var out, errw bytes.Buffer
	rr := reconstructHookedFormula(context.Background(), agentDir, agent, &out, &errw)

	if rr.Recovered || rr.Ambiguous || rr.OpenCount != 0 || rr.InstanceID != "" {
		t.Errorf("expected zero recoveryResult on no-match, got %+v", rr)
	}
}

// TestFormatUpRunSummary names each agent with its Outcome and flags ambiguity.
func TestFormatUpRunSummary(t *testing.T) {
	now := time.Date(2026, 6, 16, 12, 0, 0, 0, time.UTC)
	records := []agentRunRecord{
		{Agent: "alice", Outcome: worktree.Created},
		{Agent: "bob", Outcome: worktree.Recovered, Recovered: true},
		{Agent: "carol", Outcome: worktree.Reattached, Ambiguous: true, OpenCount: 3},
	}
	got := formatUpRunSummary(records, now)

	for _, want := range []string{
		"2026-06-16T12:00:00Z",
		"alice", "Created",
		"bob", "Recovered",
		"carol", "Reattached",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\n--- summary ---\n%s", want, got)
		}
	}
	// The ambiguous agent must be flagged distinctly with its open count.
	if !strings.Contains(got, "carol") || !strings.Contains(strings.ToLower(got), "ambiguous") {
		t.Errorf("ambiguous agent carol not flagged ambiguous:\n%s", got)
	}
	if !strings.Contains(got, "3") {
		t.Errorf("ambiguous open count (3) not surfaced:\n%s", got)
	}
}

// TestWriteUpLastRun_FactoryRootRuntime: the breadcrumb is written to the
// FACTORY-ROOT .runtime (LOW-1 / AC-4), NOT a per-agent in-worktree .runtime.
func TestWriteUpLastRun_FactoryRootRuntime(t *testing.T) {
	root := t.TempDir()
	content := "af up last run: test\nagent alice: outcome=Created\n"

	if err := writeUpLastRun(root, content); err != nil {
		t.Fatalf("writeUpLastRun: %v", err)
	}

	// AC-4: the path is exactly {root}/.runtime/af_up_last_run.
	want := filepath.Join(root, ".runtime", "af_up_last_run")
	got, err := os.ReadFile(want)
	if err != nil {
		t.Fatalf("breadcrumb not at factory-root .runtime (%s): %v", want, err)
	}
	if string(got) != content {
		t.Errorf("breadcrumb content = %q, want %q", got, content)
	}
}

// TestWriteUpLastRun_NonFatalContractIndependent: a write into an unwritable
// .runtime returns an error (which the call site warns-and-continues on),
// proving the helper surfaces failure rather than panicking (C-5).
func TestWriteUpLastRun_ReturnsErrorWhenUnwritable(t *testing.T) {
	root := t.TempDir()
	// Pre-create .runtime as a regular FILE so MkdirAll fails.
	if err := os.WriteFile(filepath.Join(root, ".runtime"), []byte("x"), 0o644); err != nil {
		t.Fatalf("seed unwritable .runtime: %v", err)
	}
	if err := writeUpLastRun(root, "data"); err == nil {
		t.Errorf("expected error when .runtime cannot be created, got nil")
	}
}

// TestWatchdog_ResolvesRelocatedWorktree pins Change 1's actual fix: when an
// agent's worktree meta carries an ABSOLUTE (relocated) Path, both watchdog
// resolvers must resolve under that absolute path — NOT filepath.Join(root,
// meta.Path), which would corrupt it (issue #392 K8 watchdog path-safety).
func TestWatchdog_ResolvesRelocatedWorktree(t *testing.T) {
	root := t.TempDir()
	relocated := filepath.Join(t.TempDir(), "relocated-wt")
	const agent = "solver"

	meta := &worktree.Meta{
		ID:     "wt-reloc1",
		Owner:  agent,
		Branch: "af/solver-reloc1",
		Path:   relocated, // absolute → relocation case
		Agents: []string{agent},
	}
	if err := worktree.WriteMeta(root, meta); err != nil {
		t.Fatalf("WriteMeta: %v", err)
	}

	gotDir := resolveAgentDir(root, agent)
	wantDir := config.AgentDir(relocated, agent)
	if gotDir != wantDir {
		t.Errorf("resolveAgentDir = %q, want %q (relocated absolute path)", gotDir, wantDir)
	}
	corrupt := config.AgentDir(filepath.Join(root, relocated), agent)
	if gotDir == corrupt {
		t.Errorf("resolveAgentDir used the relocation-unsafe join: %q", gotDir)
	}

	_, gotWt := resolveWorktreeMeta(root, agent)
	if gotWt != relocated {
		t.Errorf("resolveWorktreeMeta path = %q, want %q (verbatim absolute)", gotWt, relocated)
	}
	if gotWt == filepath.Join(root, relocated) {
		t.Errorf("resolveWorktreeMeta used the relocation-unsafe join: %q", gotWt)
	}
}
