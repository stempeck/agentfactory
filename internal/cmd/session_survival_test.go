//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/mail"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
	"github.com/stempeck/agentfactory/internal/tmux"
)

// Issue #317 Phase 5 — the GATING SENTINEL (untagged, runs in the default suite),
// design AC-1. It proves, on the operator's REAL tmux socket and beside a live
// factory, that exercising the destructive production paths under the default
// (guarded + isolated) build fires ZERO real tmux subprocess calls against
// production identities. The load-bearing evidence is the Phase 2 real-exec
// counter ProductionRealOpCount()==0 (an observed, non-vacuous fact), NOT bare
// survival — the exercised paths act on their OWN names (af-manager/af-watchdog),
// never the sentinel's unique name, so survival alone is non-discriminating.
//
// H2(r2): the sentinel name is a production identity (af-, not af-test-), so the
// guarded tmux.NewTmux() client would PANIC on it. Every sentinel create /
// snapshot / cleanup therefore goes through RAW exec.Command("tmux", ...) with
// TMUX_TMPDIR=<operator original> on the command env (reached via the Phase 2b
// tmuxisolation.OriginalTMUXTMPDIR() export), never the guarded client. Because
// this file embeds raw new-session/kill-session literals it is allowlisted in
// skipIsolationSelfFiles (tmux_isolation_enforce_test.go), exactly like
// interlock_test.go.

// realSocketEnv returns an environment that points a raw `tmux` exec at the
// operator's REAL socket: $TMUX and $TMUX_TMPDIR (the package TestMain's private
// redirect) are stripped and the captured original TMUX_TMPDIR is restored. With
// an empty original the child inherits no TMUX_TMPDIR and tmux uses its default
// socket location — the operator socket the suite was launched from. Mirrors the
// env idiom in listSessionsOnRealSocket (interlock_test.go).
func realSocketEnv() []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, e := range os.Environ() {
		if strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "TMUX_TMPDIR=") {
			continue
		}
		env = append(env, e)
	}
	if orig := tmuxisolation.OriginalTMUXTMPDIR(); orig != "" {
		env = append(env, "TMUX_TMPDIR="+orig)
	}
	return env
}

// realSocketPanePID returns the pane PID of a session on the operator's REAL
// socket (leading "=" forces an exact-name match). A changed PID after the
// under-test code ran would mean the session was killed+recreated rather than
// left undisturbed. It is the untagged reimplementation of the integration-only
// sentinelPanePID idiom, carrying realSocketEnv so it reaches the sentinel on the
// real socket rather than the private throwaway server.
func realSocketPanePID(t *testing.T, sess string) string {
	t.Helper()
	cmd := exec.Command("tmux", "list-panes", "-t", "="+sess, "-F", "#{pane_pid}")
	cmd.Env = realSocketEnv()
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("capturing pane pid for %s on the real socket: %v", sess, err)
	}
	pid := strings.TrimSpace(string(out))
	if pid == "" {
		t.Fatalf("empty pane pid for %s", sess)
	}
	return pid
}

// writeRouterConfig writes the minimal factory config a mail.Router needs so
// notifyRecipient can be exercised from package cmd (it is unexported in package
// mail, reached only via the exported Router.Send). The recipient "manager"
// resolves to the production identity af-manager, on which the guarded HasSession
// no-ops — so Send short-circuits with no real op.
func writeRouterConfig(t *testing.T, root string) {
	t.Helper()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(afDir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	write("factory.json", `{"type":"factory","version":1,"name":"test"}`)
	write("agents.json", `{"agents":{"manager":{"type":"interactive","description":"manager"},`+
		`"watchdog":{"type":"autonomous","description":"watchdog"}}}`)
	write("messaging.json", `{}`)
}

// sentinelNonVacuitySrc is the source of the G3/R2 sub-process-with-control-removed
// non-vacuity proof: a tiny package that drives the production function
// tmux.NewSession on a production identity. `go test`ed under -tags=integration
// the GUARD is compiled out, so the REAL op fires and bumps the production
// counter. It inherits the parent default-suite process's redirected
// TMUX_TMPDIR / unset TMUX, so the op lands on the PRIVATE throwaway server (no
// operator session touched). recordRealExec runs immediately before the
// subprocess exec, so the counter is > 0 regardless of the private-server result.
// It uses a name distinct from the survival sentinel and runs guard-OFF, so it is
// neither a guarded call nor a call against the sentinel name (AC #3).
const sentinelNonVacuitySrc = `package sentinelnonvac

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/tmux"
)

func TestSentinelNonVacuity(t *testing.T) {
	tmux.ResetRealOpCounter()
	_ = tmux.NewTmux().NewSession("af-nonvacuity-probe", "")
	if tmux.ProductionRealOpCount() == 0 {
		t.Fatal("SENTINEL_REAL_OPS_FIRED=false: expected a real production tmux op to fire without the guard")
	}
	t.Log("SENTINEL_REAL_OPS_FIRED=true")
}
`

func TestProductionSessionsSurviveDefaultSuite(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Fail-closed gate (mirrors TestInterlock): the Phase 2b TestMain must have
	// redirected this process tree (TMUX_TMPDIR set to a private dir, $TMUX unset)
	// BEFORE any raw tmux exec. If it has not, we must NOT exec tmux — that would
	// reach the operator's real socket (the #316 hazard) — so fail fast here.
	if os.Getenv("TMUX_TMPDIR") == "" {
		t.Fatal("TMUX_TMPDIR is not set: the Phase 2b TestMain redirect is not active; " +
			"refusing to exec tmux against the operator's real socket")
	}
	if v := os.Getenv("TMUX"); v != "" {
		t.Fatalf("TMUX is still set (%q): the Phase 2b TestMain must unset it; "+
			"refusing to exec tmux that could fall back to the operator's real socket", v)
	}

	// AC #4 + H2(r2): a UNIQUE production-identity sentinel name (af-, not af-test-,
	// never the literal af-manager), built from pid + sha256(t.Name())[:8].
	sentinel := fmt.Sprintf("af-sentinel-survival-%d-%s", os.Getpid(), hashName(t.Name()))

	root := t.TempDir()

	// Create the sentinel on the operator's REAL socket via RAW exec carrying the
	// captured original TMUX_TMPDIR — NEVER the guarded client (it would panic on
	// this production identity). Best-effort raw pre-clean first.
	preclean := exec.Command("tmux", "kill-session", "-t", "="+sentinel)
	preclean.Env = realSocketEnv()
	_ = preclean.Run()
	create := exec.Command("tmux", "new-session", "-d", "-s", sentinel, "-c", root)
	create.Env = realSocketEnv()
	if out, err := create.CombinedOutput(); err != nil {
		t.Fatalf("creating sentinel %q on the real socket: %v\n%s", sentinel, err, out)
	}
	t.Cleanup(func() {
		kill := exec.Command("tmux", "kill-session", "-t", "="+sentinel)
		kill.Env = realSocketEnv()
		_ = kill.Run()
	})

	// Non-vacuous: the sentinel really exists on the real socket.
	has := exec.Command("tmux", "has-session", "-t", "="+sentinel)
	has.Env = realSocketEnv()
	if err := has.Run(); err != nil {
		t.Fatalf("sentinel %q not found on the real socket after create: %v", sentinel, err)
	}

	// Snapshot the operator's real socket: all sessions + the sentinel pane PID.
	sessionsBefore := listSessionsOnRealSocket(t)
	pidBefore := realSocketPanePID(t, sentinel)

	// === PRIMARY (AC-1, load-bearing): zero production real ops ===
	tmux.ResetRealOpCounter()

	// exercise drives one destructive production path and recovers the guard panic
	// (the panic IS the in-process protection firing on a production identity).
	exercise := func(name string, fn func()) {
		defer func() {
			if r := recover(); r != nil {
				t.Logf("path %s panicked under the guard (in-process protection fired): %v", name, r)
			}
		}()
		fn()
	}

	// Manager.Start with a worktree set + workspace present reaches the destructive
	// NewSession(af-manager) → guard panics (recovered).
	exercise("Manager.Start", func() {
		wt := t.TempDir()
		if err := os.MkdirAll(config.AgentDir(wt, "manager"), 0o755); err != nil {
			t.Fatalf("mkdir agent workspace: %v", err)
		}
		mgr := session.NewManager("", "manager", config.AgentEntry{})
		if err := mgr.SetWorktree(wt, ""); err != nil {
			t.Fatalf("SetWorktree: %v", err)
		}
		_ = mgr.Start()
	})

	// terminateSession on a production identity short-circuits at the read-only
	// HasSession no-op → early return (no panic, no real op, no breadcrumb).
	exercise("terminateSession", func() {
		terminateSession(session.SessionName("manager"), t.TempDir())
	})

	// launchWatchdog passes the HasSession no-op and reaches NewSession(af-watchdog)
	// → guard panics (recovered).
	exercise("launchWatchdog", func() {
		cmd, _ := newTestCmd()
		launchWatchdog(cmd, newCmdTmux(), t.TempDir())
	})

	// notifyRecipient (via the exported Router.Send) short-circuits at the
	// HasSession no-op on af-manager → early return (no panic, no real op).
	exercise("notifyRecipient", func() {
		routerRoot := t.TempDir()
		writeRouterConfig(t, routerRoot)
		r, err := mail.NewRouter(routerRoot, memstore.New())
		if err != nil {
			t.Fatalf("NewRouter: %v", err)
		}
		if err := r.Send(context.Background(), mail.NewMessage("watchdog", "manager", "subj", "body")); err != nil {
			t.Fatalf("Router.Send: %v", err)
		}
	})

	if got := tmux.ProductionRealOpCount(); got != 0 {
		t.Fatalf("AC-1 FAILED: %d real tmux op(s) fired on production identities while exercising the "+
			"destructive paths under the interlock; want 0", got)
	}

	// === SECONDARY smoke: survival/snapshot (non-discriminating; counter carries AC-1) ===
	if pidAfter := realSocketPanePID(t, sentinel); pidAfter != pidBefore {
		t.Errorf("sentinel pane PID changed: before=%s after=%s (sentinel was disturbed)", pidBefore, pidAfter)
	}
	sessionsAfter := listSessionsOnRealSocket(t)
	if !reflect.DeepEqual(sessionsBefore, sessionsAfter) {
		t.Errorf("operator real-socket session list changed after exercising the paths:\n  before=%v\n  after =%v",
			sessionsBefore, sessionsAfter)
	}

	// === non-vacuity (G3/R2 pin): sub-process with the control removed ===
	// Proves the counter==0 above is meaningful: with the GUARD compiled out
	// (-tags=integration) the same kind of production op DOES fire (count > 0).
	t.Run("non_vacuity_control_removed", func(t *testing.T) {
		repoRoot := findRepoRoot(t)
		plantedDir := plantPackage(t, repoRoot, "sentinel_nonvac_test.go", sentinelNonVacuitySrc)
		pkg := relUnderRepo(t, repoRoot, plantedDir)

		cmd := exec.Command("go", "test", "-count=1", "-v", "-tags=integration", "-run", "TestSentinelNonVacuity", pkg)
		cmd.Dir = repoRoot
		// Inherit GOTMPDIR + the Phase 2b TMUX redirect; pin CGO_ENABLED=0 to mirror
		// the guard_selfverify model.
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("control-removed sub-process failed: %v\n%s", err, out)
		}
		if !strings.Contains(string(out), "SENTINEL_REAL_OPS_FIRED=true") {
			t.Fatalf("non-vacuity: the control-removed (-tags=integration) run did not record a real "+
				"production tmux op firing; the counter==0 proof would be vacuous.\n%s", out)
		}
	})
}
