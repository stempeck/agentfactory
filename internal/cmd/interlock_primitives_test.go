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

// TestDone_SelfSessionIdFallback_Allowed pins AC2 (Ledger D9): a raw .runtime/session_id value
// (the Claude-hook UUID, which is NOT session.SessionName(AF_ROLE)) reaching KillSession must be
// recognised as the caller's own session and delegated, not wrongly refused.
//
// af done USED to hand that value over; #622 Phase 2 removed the caller, because passing a Claude
// session UUID where a tmux session name belongs was the LOW-3 bug (the fallback now resolves a
// real name — see TestDone_SelfTmuxSessionFallback_Allowed). This test is therefore no longer
// pinning a live path: it is what keeps the isSelfSessionID permit honest until ADR-021 decides
// whether a permit with no caller should stay in a kill guard at all.
func TestDone_SelfSessionIdFallback_Allowed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // the guard reads getWd()/.runtime/session_id
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

// installGuardedFake wraps a bare fakeTmux in the K8 decorator behind newCmdTmux, WITHOUT the
// hermetic session-name override. installGuardedSeam's setupHermeticSessions makes
// session.SessionName produce af-test-* names, and isAfProductionSession excludes those — so
// callerAuthority reads Operator and the guard never engages at all. A guard test built on it
// passes whether or not the permit under test exists. These two need production-shaped names, and
// take them as literals the way authority_test.go:104 already does; the fake reaches no tmux server.
func installGuardedFake(t *testing.T) *fakeTmux {
	t.Helper()
	fake := newFakeTmux()
	orig := newCmdTmux
	newCmdTmux = func() cmdTmux { return authKillGuard{fake} }
	t.Cleanup(func() { newCmdTmux = orig })
	return fake
}

// TestDone_SelfTmuxSessionFallback_Allowed pins the #622 G10 repair: the af done fallback that
// asks tmux for its own session name must be allowed to kill it.
//
// This is the one permit axis with a LIVE production caller, and until this test it was the one
// with no coverage — every other guard test in this file sets TMUX="" and so returns false from
// isSelfTmuxSession without ever entering it. AF_ROLE is empty deliberately: that is not a
// convenience of the fixture but the defining condition of the branch, since resolveAgentName
// consults AF_ROLE last and detectAgentName can only have failed with it unset.
//
// Without the permit, callerAuthority reads Agent (signal 2, the same CurrentSessionName query),
// isSelfSession compares against session.SessionName("") and misses, the guard refuses — and
// terminateSession has by then already written .runtime/last_termination, so the factory carries a
// durable record of a termination that never happened.
func TestDone_SelfTmuxSessionFallback_Allowed(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // isSelfSessionID reads getWd()/.runtime/session_id; left absent so it cannot permit
	fake := installGuardedFake(t)
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")

	sid := "af-" + hashName(t.Name()) // production-shaped: af-test-* would read as Operator
	fake.currentSession = sid         // signal 2: this process is running in that session
	fake.present[sid] = true

	// Precondition, asserted rather than assumed: the guard must really be engaged, or this test
	// would pass on a permit set that refuses nothing.
	if callerAuthority() != AuthorityAgent {
		t.Fatal("precondition: the guard only refuses in agent context; this fixture is not in one")
	}
	if isSelfSession(sid) || isSelfSessionID(sid) {
		t.Fatal("precondition: the other two permit axes must MISS, or this proves nothing")
	}

	terminateSession(sid, dir)

	if !hasOp(fake.ops, "KillSession "+sid) {
		t.Fatalf("G10: the AF_ROLE-less self-terminate must be allowed through the decorator; ops=%v", fake.ops)
	}
}

// TestGuard_ForeignSessionRefusedOnEveryPermitAxis is the other half: the permit added above must
// not have widened the guard to anything that is not the caller's own session.
//
// Each case fails a DIFFERENT axis, because a single foreign-name case would pass even if two of
// the three predicates had been wired to return true unconditionally.
func TestGuard_ForeignSessionRefusedOnEveryPermitAxis(t *testing.T) {
	for _, tc := range []struct {
		name           string
		afRole         string
		tmuxEnv        string
		currentSession string
	}{
		{"a different live session", "", "/tmp/tmux-501/default,12345,0", "af-someone-else"},
		{"tmux reports no session name", "manager", "/tmp/tmux-501/default,12345,0", ""},
		{"agent context established by AF_ROLE, outside tmux", "manager", "", ""},
		// The $TMUX gate, on its own. Without it isSelfTmuxSession asks tmux for #S from a process
		// that is not in a tmux session, and the answer is some unrelated session's name — which
		// this row makes equal to the target, so an ungated predicate would permit killing it.
		// The row above cannot catch that: it clears the session name too, so the two conjuncts
		// mask each other and the mutation survives.
		{"outside tmux, where a #S query answers about somebody else", "manager", "", "af-victim-target"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir) // isSelfSessionID reads getWd()/.runtime/session_id; leave it absent
			fake := installGuardedFake(t)
			t.Setenv("AF_ROLE", tc.afRole)
			t.Setenv("TMUX", tc.tmuxEnv)
			fake.currentSession = tc.currentSession

			target := "af-victim-target"
			fake.present[target] = true

			if callerAuthority() != AuthorityAgent {
				t.Fatal("precondition: the guard only refuses in agent context")
			}

			terminateSession(target, dir)

			if hasOp(fake.ops, "KillSession "+target) {
				t.Fatalf("an agent killed a session that is not its own; ops=%v", fake.ops)
			}
		})
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
