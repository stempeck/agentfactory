//go:build integration

package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

// TestImprovementHook_SessionSurvivesThenCompletes is TEST-5a (issue #483, Phase 5):
// the deterministic tmux-level proof of the continuous-improvement hook, with no
// model in the loop. With both toggles on and a dispatched formula, a qualifying
// final `af done` must KEEP the session alive after WORK_DONE, write the
// .runtime/improvement_pending marker, and deliver the /improve-agent instruction
// over the redundant trio (stdout + self-mail + tmux nudge). Then `af improvement
// complete` must reproduce the pre-feature dispatched-teardown state: outcome mail,
// session killed, .runtime/last_termination written, marker atomically consumed.
//
// Modeled on TestNoAutoTermination_PersistentSession (the "survives af done" spine)
// and TestAutoTermination_DispatchedSession (the post-complete teardown mirror);
// reuses setupTerminationTest. The built `af` is a real binary (not *.test), so
// isTestBinary() is false and the real mail/nudge/kill paths execute.
//
// This runs green only from a clean CI checkout via `make test-integration`;
// GuardCIOnly (TestMain) exits 1 under /.agentfactory/worktrees/, so it does not
// run from a live-factory worktree.
func TestImprovementHook_SessionSurvivesThenCompletes(t *testing.T) {
	binary, workspace, agentDir, sessionName := setupTerminationTest(t, "test-improve")
	runtimeDir := filepath.Join(agentDir, ".runtime")

	// Enable BOTH toggles through the real CLI (exercises the af improvement surface).
	// Run AFTER setup: setupTerminationTest's af sling has already read agents.json, so
	// the SaveAgentConfig rewrite here does not race it.
	runAF(t, binary, workspace, "improvement", "on")
	runAF(t, binary, workspace, "improvement", "on", "--agent", "test-improve")

	// Dispatched + caller ⇒ the fire condition holds and the ORIGINAL shouldTerminate
	// is true, so the marker records terminate_on_complete=true (mirrors
	// TestAutoTermination_DispatchedSession's marker writes).
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("manager"), 0o644)
	os.Remove(filepath.Join(runtimeDir, "formula_caller"))
	os.WriteFile(filepath.Join(runtimeDir, "formula_caller"), []byte("manager"), 0o644)

	// Prime the step (production flow: af prime runs before af done).
	runAF(t, binary, agentDir, "prime")

	// Final af done: closes the single step, mails WORK_DONE, fires the hook.
	out, err := runAFMayFail(t, binary, agentDir, "done")
	if err != nil {
		t.Logf("af done output:\n%s", out)
		t.Fatalf("af done failed: %v", err)
	}

	// --- after af done: session ALIVE, marker written, instruction delivered ---

	// Session survived WORK_DONE (teardown deferred to af improvement complete).
	if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err != nil {
		t.Fatal("tmux session should still be alive after the improvement hook fired")
	}

	// The pending marker carries every fact the completion verb needs.
	m, err := readImprovementMarkerFile(filepath.Join(runtimeDir, "improvement_pending"))
	if err != nil {
		t.Fatalf("reading .runtime/improvement_pending: %v", err)
	}
	if m.Caller != "manager" || !m.TerminateOnComplete || m.Formula != "test-terminate" ||
		m.FiredAt == "" || m.FormulaSHA256 == "" {
		t.Fatalf("unexpected improvement marker: %+v", m)
	}

	// The /improve-agent instruction is on the af done stdout (the always-emitted
	// anchor of the delivery trio). Assert em-dash-free fragments only.
	for _, frag := range []string{
		"use the Skill tool to load /improve-agent",
		".agentfactory/store/formulas/test-terminate.formula.toml",
		"af formula show test-terminate --json",
		"af improvement complete",
	} {
		if !strings.Contains(out, frag) {
			t.Fatalf("af done stdout missing instruction fragment %q; got:\n%s", frag, out)
		}
	}
	if strings.Contains(out, "Auto-terminating") {
		t.Fatalf("af done should NOT auto-terminate a surviving improvement session; got:\n%s", out)
	}

	// The self-mail bead exists. A mail bead's Title == its Subject
	// (internal/mail/translate.go), and the improvement self-mail subject is
	// "IMPROVEMENT HOOK: refine test-terminate".
	store, err := mcpstore.New(workspace, "")
	if err != nil {
		t.Fatalf("mcpstore.New for self-mail check: %v", err)
	}
	issues, err := store.List(context.Background(), issuestore.Filter{
		IncludeAllAgents: true,
		IncludeClosed:    true,
	})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	foundMail := false
	for _, iss := range issues {
		if strings.Contains(iss.Title, "IMPROVEMENT HOOK") {
			foundMail = true
			break
		}
	}
	if !foundMail {
		t.Fatalf("expected an IMPROVEMENT HOOK self-mail bead in %d issues, none found", len(issues))
	}

	// The nudge pointer is visible in the pane. Collapse ALL whitespace before the
	// substring match so tmux line-wrapping cannot split the token; match the
	// em-dash-free tail "...run: af improvement complete" ⇒ "afimprovementcomplete".
	// NudgeSession is synchronous inside af done, so the text is already in the pane;
	// the short poll only absorbs tmux render lag.
	stripped := ""
	for i := 0; i < 25; i++ {
		b, _ := exec.Command("tmux", "capture-pane", "-p", "-t", sessionName, "-S", "-200").CombinedOutput()
		stripped = strings.Join(strings.Fields(string(b)), "")
		if strings.Contains(stripped, "afimprovementcomplete") {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !strings.Contains(stripped, "afimprovementcomplete") {
		t.Fatalf("nudge pointer not visible in pane; captured:\n%s", stripped)
	}

	// --- af improvement complete: outcome mail, teardown, marker consumed ---

	// Run from agentDir, no --dir (getwd resolves the marker).
	out2 := runAF(t, binary, agentDir, "improvement", "complete")

	// Outcome subject printed to stdout (match the prefix before the em-dash).
	if !strings.Contains(out2, "IMPROVEMENT: test-terminate") {
		t.Fatalf("af improvement complete stdout missing outcome subject; got:\n%s", out2)
	}

	// Session terminated (marker.TerminateOnComplete=true ⇒ deferred teardown replayed).
	if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err == nil {
		t.Fatal("tmux session should be terminated after af improvement complete")
	}

	// Pre-feature teardown breadcrumb: last_termination "auto-terminated at <RFC3339>".
	term, err := os.ReadFile(filepath.Join(runtimeDir, "last_termination"))
	if err != nil {
		t.Fatalf("reading .runtime/last_termination: %v", err)
	}
	ts := strings.TrimSpace(string(term))
	if !strings.HasPrefix(ts, "auto-terminated at ") {
		t.Fatalf("last_termination should start with 'auto-terminated at', got: %s", ts)
	}
	if _, err := time.Parse(time.RFC3339, strings.TrimPrefix(ts, "auto-terminated at ")); err != nil {
		t.Fatalf("last_termination timestamp is not valid RFC3339: %s (err: %v)", ts, err)
	}

	// Marker consumed atomically (renamed to .consumed).
	if _, err := os.Stat(filepath.Join(runtimeDir, "improvement_pending")); !os.IsNotExist(err) {
		t.Fatal(".runtime/improvement_pending should be gone (renamed to .consumed)")
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "improvement_pending.consumed")); err != nil {
		t.Fatal(".runtime/improvement_pending.consumed should exist after af improvement complete")
	}
}
