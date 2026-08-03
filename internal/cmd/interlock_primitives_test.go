package cmd

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/session"
)

// Phase 4 (#541) primitive-interlock tests: the K8 KillSession decorator on the newCmdTmux
// seam and the K10 orphan-sweep authority-check + package-var seam. These prove the redundant
// backstops fire even when a Phase-3 command gate is bypassed, while every self/operator path
// is preserved. Per the hermetic discipline they MUST NOT call t.Parallel (the seams are
// package globals) and touch no real tmux server or host process sweep.

// --- AC1: decorator backstop (K8) ---

// TestDecorator_AgentUpWatchdog_Backstop pins AC1: an agent-context `af up` zombie-watchdog
// recreate is refused AT THE DECORATOR even though launchWatchdog is driven directly (its
// command gate bypassed). A present-but-dead watchdog would, un-refused, be killed+recreated
// (see TestDeadWatchdogReplaced), so the absence of KillSession/NewSession is non-vacuous.
func TestDecorator_AgentUpWatchdog_Backstop(t *testing.T) {
	fake, _ := setupHermeticSessions(t)
	t.Setenv("AF_ROLE", "manager") // signal 1 => agent context
	t.Setenv("TMUX", "")           // keep signal 2 quiet

	ws := session.WatchdogSessionName()
	fake.present[ws] = true
	fake.running[ws] = false // zombie: session present but `af` not running

	cmd, _ := newTestCmd()
	scope, agentsCfg := knownScope("alpha")
	launchWatchdog(cmd, authKillGuard{fake}, "/factory/root", scope, agentsCfg)

	if hasOp(fake.ops, "KillSession "+ws) {
		t.Fatalf("agent-context watchdog kill must be refused at the decorator; ops=%v", fake.ops)
	}
	for _, op := range fake.ops {
		if strings.HasPrefix(op, "NewSession "+ws) {
			t.Fatalf("no watchdog recreate must follow a refused kill; ops=%v", fake.ops)
		}
	}
}

// TestKillSession_AgentContext_Refuses pins AC1 for the dispatch-stop surface: calling the
// decorator's KillSession directly (skipping runDispatchStop's K7 command gate) in agent
// context returns the AC-6 refusal and never touches the inner client.
func TestKillSession_AgentContext_Refuses(t *testing.T) {
	fake, _ := setupHermeticSessions(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")

	target := session.DispatchSessionName()
	err := authKillGuard{fake}.KillSession(target)

	assertTeardownRefused(t, err, "KillSession "+target)
	if opRecorded(fake.ops, "KillSession") {
		t.Fatalf("inner client must not be touched on a refused kill; ops=%v", fake.ops)
	}
}

// --- AC2: self paths preserved (K8 self-recognition incl. Ledger D9) ---

// installGuardedSeam wraps the hermetic fake in the K8 decorator behind newCmdTmux so that
// terminateSession's internal newCmdTmux() call picks up the guard.
func installGuardedSeam(t *testing.T, fake *fakeTmux) {
	t.Helper()
	orig := newCmdTmux
	newCmdTmux = func() cmdTmux { return authKillGuard{fake} }
	t.Cleanup(func() { newCmdTmux = orig })
}

// TestDone_SelfTerminate_Allowed pins AC2 (normal path): af done killing its OWN session is
// delegated through the decorator (self target), not refused.
func TestDone_SelfTerminate_Allowed(t *testing.T) {
	fake, _ := setupHermeticSessions(t)
	installGuardedSeam(t, fake)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")

	dir := t.TempDir()
	sid := session.SessionName("manager") // == session.SessionName(AF_ROLE) => isSelfSession true
	fake.present[sid] = true

	terminateSession(sid, dir)

	if !hasOp(fake.ops, "KillSession "+sid) {
		t.Fatalf("self-terminate must be allowed through the decorator; ops=%v", fake.ops)
	}
}

// TestDone_SelfSessionIdFallback_Allowed pins AC2 (Ledger D9): the af done FALLBACK path hands
// a raw .runtime/session_id value (the Claude-hook UUID, which is NOT session.SessionName(
// AF_ROLE)) to KillSession. The decorator must recognize that value as the caller's own
// session and delegate, not wrongly refuse a legitimate self-terminate.
func TestDone_SelfSessionIdFallback_Allowed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // the guard reads getWd()/.runtime/session_id — the same file done.go:616 reads
	fake, _ := setupHermeticSessions(t)
	installGuardedSeam(t, fake)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")

	rawID := "claude-hook-7f3a1e9b-not-a-session-name"
	if rawID == session.SessionName("manager") {
		t.Fatal("test precondition: rawID must differ from SessionName(AF_ROLE)")
	}
	writeRuntimeFile(t, dir, "session_id", rawID)
	fake.present[rawID] = true

	terminateSession(rawID, dir)

	if !hasOp(fake.ops, "KillSession "+rawID) {
		t.Fatalf("D9: legit self-terminate via raw session_id must be allowed; ops=%v", fake.ops)
	}
}

// TestHandoff_AgentContext_Works pins the AC2 handoff half: handoff never routes through the
// cmd-layer decorator, so it completes in agent context without a teardown refusal (dry-run
// short-circuits before tmux/mail, isolating "no gate on the handoff entry").
func TestHandoff_AgentContext_Works(t *testing.T) {
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	if err := runHandoffCore(t.Context(), workDir, "HANDOFF: test", "msg", false, false, true); err != nil {
		t.Fatalf("handoff must work in agent context (no decorator on its path); got: %v", err)
	}
}

// --- AC3/AC4: orphan-sweep seam + agent-context check (K10) ---

// installPkillRecorder saves/overrides the runPkill package-var seam with a recorder and
// restores it on cleanup, mirroring installSeamHygiene's save/override/Cleanup idiom. It
// guarantees no real pgrep/process-kill exec runs against host processes in the default suite.
func installPkillRecorder(t *testing.T) *[]string {
	t.Helper()
	orig := runPkill
	var calls []string
	runPkill = func(pattern string) error {
		calls = append(calls, pattern)
		return nil
	}
	t.Cleanup(func() { runPkill = orig })
	return &calls
}

// TestOrphan_AgentContext_ZeroExec pins AC3: killOrphanedClaudeProcesses in agent context
// returns BEFORE the runPkill seam — zero recorded calls, no real host exec.
func TestOrphan_AgentContext_ZeroExec(t *testing.T) {
	calls := installPkillRecorder(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")

	killOrphanedClaudeProcesses()

	if len(*calls) != 0 {
		t.Fatalf("agent context must never sweep host processes; runPkill calls=%v", *calls)
	}
}

// TestPkillSeam_OperatorExercised pins AC3: an operator-context sweep reaches the seam exactly
// once with the claude sweep pattern (still no real host exec because the seam is overridden).
func TestPkillSeam_OperatorExercised(t *testing.T) {
	calls := installPkillRecorder(t)
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")

	killOrphanedClaudeProcesses()

	if len(*calls) != 1 {
		t.Fatalf("operator context must reach the seam exactly once; calls=%v", *calls)
	}
	if pat := (*calls)[0]; !strings.Contains(pat, "claude") || !strings.Contains(pat, "--dangerously-skip-permissions") {
		t.Fatalf("seam must receive the claude sweep pattern; got %q", pat)
	}
}
