//go:build integration

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

// Issue #317 Phase 5 — the AC-5 REPRO (build-tagged: drives REAL tmux, so it runs
// only under `make test-integration`, never the default suite). It reproduces the
// PRECISE #316 kill vector — an idle-shell session misread as a zombie and killed
// by Manager.Start — plus the co-equal raw kill-session and #288 launchWatchdog
// vectors, and narrates the breadcrumb forensic (done.go:512 writes the only
// breadcrumb, before its KillSession; the #316 corpse had none).
//
// Every real kill targets a UNIQUELY-NAMED sentinel (af-sentinel-repro-…), never
// the literal af-manager/af-watchdog, so it is safe beside a live factory; the
// watchdog vector is reproduced structurally through the recording fake. The
// integration build compiles the GUARD out (guardMode=false) and excludes the
// Phase 2b TestMain redirect, so these ops reach the operator's real socket — the
// environment in which the corpse was made.
func TestReproduce_ManagerKillVector(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Vector 1 (PRECISE #316): idle-shell session misread as a zombie by
	// Manager.Start, whose KillSession (session.go:214) then destroys it.
	t.Run("idle_shell_manager_start", func(t *testing.T) {
		agentName := fmt.Sprintf("sentinel-repro-%d-%s", os.Getpid(), hashName(t.Name()))
		sessionName := session.SessionName(agentName) // af-sentinel-repro-… (never af-manager)

		root := t.TempDir()
		// Stand the sentinel at an IDLE SHELL — a LIVE session, not "no session".
		killStaleTmuxSession(t, sessionName)
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", root).CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session %s: %v\n%s", sessionName, err, out)
		}
		t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", "="+sessionName).Run() })

		// The #316 false-positive: IsClaudeRunning is false for a bare idle shell,
		// so Manager.Start classifies the live session as a zombie.
		tx := tmux.NewTmux() // integration build: guard OFF → real tmux
		if tx.IsClaudeRunning(sessionName) {
			t.Fatalf("idle-shell sentinel %q misreported as running Claude; cannot reproduce the vector", sessionName)
		}
		pidBefore := sentinelPanePID(t, sessionName)

		// Drive the precise vector. The guard-off Start() blocks on the
		// post-NewSession claude wait, so run it with a deadline (mirrors the
		// guard_selfverify planted offender).
		wt := t.TempDir()
		if err := os.MkdirAll(config.AgentDir(wt, agentName), 0o755); err != nil {
			t.Fatalf("mkdir agent workspace: %v", err)
		}
		mgr := session.NewManager("", agentName, config.AgentEntry{})
		if err := mgr.SetWorktree(wt, ""); err != nil {
			t.Fatalf("SetWorktree: %v", err)
		}
		done := make(chan struct{})
		go func() { _ = mgr.Start(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}

		// The original idle pane is gone: the session was killed (and possibly
		// recreated with a fresh pane PID). Either way the live session the
		// operator had was destroyed — the #316 corpse.
		killed := false
		if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err != nil {
			killed = true // session gone
		} else if sentinelPanePID(t, sessionName) != pidBefore {
			killed = true // killed + recreated → fresh pane PID
		}
		if !killed {
			t.Fatalf("vector NOT reproduced: idle-shell sentinel %q survived Manager.Start untouched (pane %s)",
				sessionName, pidBefore)
		}

		// Breadcrumb forensic: Manager.Start's KillSession leaves NO breadcrumb,
		// matching the #316 corpse (which had none). Only the `af done`
		// terminateSession path writes one (asserted in breadcrumb_forensic).
		if _, err := os.Stat(filepath.Join(root, ".runtime", "last_termination")); err == nil {
			t.Errorf("unexpected last_termination breadcrumb for a Manager.Start kill; the #316 corpse had none")
		}
		t.Logf("reproduced: idle-shell sentinel %q killed by Manager.Start, no breadcrumb (matches #316)", sessionName)
	})

	// Vector 2 (co-equal): a raw kill-session destroys a live session directly —
	// the exact op the #317 change makes unconstructable in the default suite.
	t.Run("raw_kill_session", func(t *testing.T) {
		agentName := fmt.Sprintf("sentinel-repro-raw-%d-%s", os.Getpid(), hashName(t.Name()))
		sessionName := session.SessionName(agentName)
		root := t.TempDir()
		killStaleTmuxSession(t, sessionName)
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", root).CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session %s: %v\n%s", sessionName, err, out)
		}
		t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", "="+sessionName).Run() })
		if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err != nil {
			t.Fatalf("sentinel %q was not created", sessionName)
		}
		if out, err := exec.Command("tmux", "kill-session", "-t", "="+sessionName).CombinedOutput(); err != nil {
			t.Fatalf("kill-session %s: %v\n%s", sessionName, err, out)
		}
		if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err == nil {
			t.Fatalf("raw kill-session did not destroy %q", sessionName)
		}
	})

	// Vector 3 (#288 launchWatchdog, up.go:198-220): a present-but-dead watchdog is
	// killed and recreated. Driven through the recording fake so it targets the
	// production watchdog name WITHOUT a real op against a live factory's
	// af-watchdog.
	t.Run("launchwatchdog_288", func(t *testing.T) {
		fake := newFakeTmux()
		ws := session.WatchdogSessionName() // af-watchdog (production identity)
		fake.present[ws] = true
		fake.running[ws] = false // tmux session alive but `af` not running → zombie
		cmd, _ := newTestCmd()
		launchWatchdog(cmd, fake, t.TempDir())
		if !hasOp(fake.ops, "KillSession "+ws) {
			t.Fatalf("the #288 vector did not issue KillSession against %q; ops=%v", ws, fake.ops)
		}
	})

	// Breadcrumb forensic (contrast): terminateSession (the `af done` path) writes
	// the .runtime/last_termination breadcrumb BEFORE its KillSession (done.go:512
	// then 516) — the marker the #316 corpse lacked, evidence the fatal kill came
	// from a Manager.Start-class path, not from `af done`.
	t.Run("breadcrumb_forensic", func(t *testing.T) {
		agentName := fmt.Sprintf("sentinel-repro-crumb-%d-%s", os.Getpid(), hashName(t.Name()))
		sessionName := session.SessionName(agentName)
		cwd := t.TempDir()
		if err := os.MkdirAll(filepath.Join(cwd, ".runtime"), 0o755); err != nil {
			t.Fatalf("mkdir .runtime: %v", err)
		}
		killStaleTmuxSession(t, sessionName)
		if out, err := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", cwd).CombinedOutput(); err != nil {
			t.Fatalf("tmux new-session %s: %v\n%s", sessionName, err, out)
		}
		t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", "="+sessionName).Run() })

		terminateSession(sessionName, cwd) // guard OFF → HasSession true → breadcrumb then KillSession

		if _, err := os.Stat(filepath.Join(cwd, ".runtime", "last_termination")); err != nil {
			t.Errorf("terminateSession did not write the last_termination breadcrumb: %v", err)
		}
		if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err == nil {
			t.Errorf("terminateSession did not kill %q after writing the breadcrumb", sessionName)
		}
	})
}
