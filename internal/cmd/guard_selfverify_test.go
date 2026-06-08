//go:build !integration

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/tmux"
)

// Issue #317 Phase 3 — self-verifying meta-tests (proof-of-fire, design AC-4).
//
// Phase 2 (in-process GUARD) and Phase 2b (out-of-process ISOLATE backstop) added
// the protections; this file proves they actually FIRE, behaviorally, in the
// default suite — it does not add new protection. Each meta-test is non-vacuous:
// it observes the control acting (a panic naming the offender, a real op landing
// only on the private server), and the GUARD proofs carry a sub-process variant
// with the control compiled out so the pass is attributable to the control, not
// to incidental success (G3/R2 pin — sub-process-with-control-removed, never an
// in-process toggle).
//
// This file deliberately contains NO raw destructive tmux literal of the kind the
// source scan (tmux_isolation_enforce_test.go) flags, so it needs no
// skipIsolationSelfFiles allowlist entry: the offenders drive PRODUCTION GO
// FUNCTIONS (Manager.Start / tmux.NewSession), and any probing uses only the
// read-only has-session / list-sessions ops, which the scan does not flag.

// plantedOffenderSrc is the source of an INDIRECT offender: a test that drives the
// production function Manager.Start() on the production identity "af-manager".
//
//   - default (guarded) build: Start() reaches the destructive NewSession op and the
//     in-process GUARD panics, naming TestPlantedOffender (offendingTestName scans the
//     panicking goroutine's stack). The parent asserts non-zero exit + that name.
//   - control-removed build (-tags=integration → guardMode=false): no panic; the REAL
//     op fires instead (ProductionRealOpCount > 0). Start() runs in a goroutine with a
//     short deadline because the guard-off path otherwise blocks on a long claude wait.
//
// It MUST live inside the module (it imports internal/* — Go forbids that from an
// external temp module), under a testdata/ dir so `go test ./...` ignores it.
const plantedOffenderSrc = `package guardoffender

import (
	"os"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

func TestPlantedOffender(t *testing.T) {
	wt := t.TempDir()
	if err := os.MkdirAll(config.AgentDir(wt, "manager"), 0o755); err != nil {
		t.Fatalf("mkdir agent workspace: %v", err)
	}
	mgr := session.NewManager("", "manager", config.AgentEntry{})
	if err := mgr.SetWorktree(wt, ""); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}
	done := make(chan struct{})
	go func() { _ = mgr.Start(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
	}
	// In the guard-off build the goroutine may still be in Start()'s post-NewSession
	// claude-wait when we return; it is reaped when this sub-process exits and never
	// leaks into the parent runner. Reached only when the guard is compiled out
	// (guard-on panics in the goroutine and crashes the process first). The counter
	// (atomic) confirms the real destructive op fired without the guard.
	t.Logf("PLANTED_REAL_OPS_FIRED=%v", tmux.ProductionRealOpCount() > 0)
}
`

// spawnerMainSrc is the source of a SEPARATE production binary that models the #316
// hazard one process removed: built with a plain `go build`, its basename is not
// "*.test", so isTestBinary() is false and the in-process GUARD is OFF. It drives the
// production function tmux.NewSession on a production-identity name (os.Args[1]),
// issuing a REAL destructive op. Only the Phase 2b ISOLATE backstop (inherited
// TMUX_TMPDIR redirect + unset $TMUX) keeps it off the operator's real socket.
const spawnerMainSrc = `package main

import (
	"fmt"
	"os"

	"github.com/stempeck/agentfactory/internal/tmux"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: spawner <session-name>")
		os.Exit(2)
	}
	if err := tmux.NewTmux().NewSession(os.Args[1], ""); err != nil {
		fmt.Fprintf(os.Stderr, "NewSession(%q) failed: %v\n", os.Args[1], err)
		os.Exit(1)
	}
}
`

// plantPackage writes src to <repoRoot>/internal/cmd/testdata/<uniq>/<filename> and
// returns the directory. testdata/ is ignored by `go test ./...` and `go build ./...`
// wildcard expansion, yet the package is still explicitly buildable/testable and —
// because it sits inside the module — may import internal/* packages. The dir is
// removed on test cleanup so a concurrent suite never sees it.
func plantPackage(t *testing.T, repoRoot, filename, src string) string {
	t.Helper()
	testdataDir := filepath.Join(repoRoot, "internal", "cmd", "testdata")
	if err := os.MkdirAll(testdataDir, 0o755); err != nil {
		t.Fatalf("mkdir testdata: %v", err)
	}
	dir, err := os.MkdirTemp(testdataDir, "guardselfverify")
	if err != nil {
		t.Fatalf("mkdtemp planted package: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(src), 0o644); err != nil {
		t.Fatalf("write planted source: %v", err)
	}
	return dir
}

// relUnderRepo renders dir as a "./…"-prefixed package path relative to repoRoot,
// suitable as a `go test`/`go build` argument with cmd.Dir == repoRoot.
func relUnderRepo(t *testing.T, repoRoot, dir string) string {
	t.Helper()
	rel, err := filepath.Rel(repoRoot, dir)
	if err != nil {
		t.Fatalf("rel(%q,%q): %v", repoRoot, dir, err)
	}
	return "./" + filepath.ToSlash(rel)
}

// TestGuardSelfVerify_SubprocessPlantedOffender is the out-of-process GUARD proof.
// It plants an indirect offender (Manager.Start on af-manager), `go test`s it in a
// sub-process WITHOUT -tags=integration (so the GUARD is compiled in), and asserts
// the sub-process exits non-zero with the guard panic naming the offender. The
// sub-process is required so the planted panic does not crash this parent runner.
//
// Non-vacuity (sub-process-with-control-removed): the SAME planted package is then
// `go test`ed WITH -tags=integration (GUARD compiled out); it must NOT emit the guard
// message, and the real op must fire instead — proving the first run's failure is
// caused specifically by the GUARD.
func TestGuardSelfVerify_SubprocessPlantedOffender(t *testing.T) {
	repoRoot := findRepoRoot(t)
	plantedDir := plantPackage(t, repoRoot, "offender_test.go", plantedOffenderSrc)
	pkg := relUnderRepo(t, repoRoot, plantedDir)

	run := func(integration bool) (string, error) {
		args := []string{"test", "-count=1", "-v", "-run", "TestPlantedOffender"}
		if integration {
			args = append(args, "-tags=integration")
		}
		args = append(args, pkg)
		cmd := exec.Command("go", args...)
		cmd.Dir = repoRoot
		// Inherit GOTMPDIR + the Phase 2b TMUX redirect; pin CGO_ENABLED=0 to mirror
		// the buildAF model and stay independent of the parent's CGO setting.
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	// GUARD compiled in: the indirect offender must trip the panic.
	out, err := run(false)
	if err == nil {
		t.Fatalf("planted offender sub-process exited 0; the GUARD did not fire.\n%s", out)
	}
	if !strings.Contains(out, "af test isolation") {
		t.Fatalf("planted offender output missing the guard message %q.\n%s", "af test isolation", out)
	}
	if !strings.Contains(out, "TestPlantedOffender") {
		t.Fatalf("planted offender output does not name the offending test (TestPlantedOffender).\n%s", out)
	}

	// Non-vacuity: GUARD compiled out → no guard message, the real op fires.
	t.Run("control_removed_no_guard", func(t *testing.T) {
		out, _ := run(true)
		if strings.Contains(out, "af test isolation") {
			t.Fatalf("control-removed (-tags=integration) run still emitted the guard message; "+
				"the proof is vacuous.\n%s", out)
		}
		if !strings.Contains(out, "PLANTED_REAL_OPS_FIRED=true") {
			t.Fatalf("control-removed run did not record a real production tmux op firing; "+
				"expected PLANTED_REAL_OPS_FIRED=true.\n%s", out)
		}
	})
}

// TestGuard_PanicFiresInProcess is the in-process GUARD proof: in the default test
// build (guardMode == isTestBinary() == true) the guarded *tmux.Tmux panics on a
// destructive op against a production identity, with the named-failure message.
func TestGuard_PanicFiresInProcess(t *testing.T) {
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected the GUARD to panic on NewSession against a production identity, got none " +
				"(is this the guarded default build?)")
		}
		msg := fmt.Sprint(r)
		for _, want := range []string{"af test isolation", "new-session", "af-manager"} {
			if !strings.Contains(msg, want) {
				t.Errorf("guard panic message missing %q; got: %s", want, msg)
			}
		}
	}()
	_ = tmux.NewTmux().NewSession("af-manager", "")
}

// TestInterlockNeutralizesSpawnedBinary is the out-of-process ISOLATE proof for a
// spawned *binary* (the variant TestInterlock does not cover — TestInterlock raw-execs
// tmux; this proves the backstop holds when a SEPARATE production binary with the
// in-process GUARD OFF issues the op). A planted main (guard off via plain `go build`)
// issues a real tmux new-session on a production-identity name; the Phase 2b redirect
// must land it on the private throwaway server, leaving the operator's real socket
// byte-for-byte unchanged.
func TestInterlockNeutralizesSpawnedBinary(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Fail-closed gate (mirrors TestInterlock): the Phase 2b TestMain must have
	// redirected this process tree BEFORE we spawn anything that execs tmux. If it
	// has not, we must NOT spawn — that could reach the operator's real socket (the
	// #316 hazard) — so fail fast here. This is what makes the test red when the
	// ISOLATE backstop is absent, without touching the operator's real socket.
	if os.Getenv("TMUX_TMPDIR") == "" {
		t.Fatal("TMUX_TMPDIR is not set: the Phase 2b TestMain redirect is not active; " +
			"refusing to spawn a binary that could reach the operator's real socket")
	}
	if v := os.Getenv("TMUX"); v != "" {
		t.Fatalf("TMUX is still set (%q): the Phase 2b TestMain must unset it; "+
			"refusing to spawn a binary that could fall back to the operator's real socket", v)
	}

	repoRoot := findRepoRoot(t)
	plantedDir := plantPackage(t, repoRoot, "main.go", spawnerMainSrc)

	// Build into the planted dir (under the repo tree, an exec-capable fs) rather
	// than t.TempDir(): /tmp may be mounted noexec, which makes fork/exec of a temp
	// binary fail. The dir is removed on cleanup by plantPackage.
	binary := filepath.Join(plantedDir, "spawner")
	build := exec.Command("go", "build", "-o", binary, relUnderRepo(t, repoRoot, plantedDir))
	build.Dir = repoRoot
	build.Env = append(os.Environ(), "CGO_ENABLED=0") // mirror the buildAF model
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building planted spawner: %v\n%s", err, out)
	}

	sess := "af-spawn-" + hashName(t.Name()) // production identity (af-, NOT af-test-)

	// Snapshot the operator's REAL socket (reached via the captured original).
	realBefore := listSessionsOnRealSocket(t)

	// Spawn the guard-off binary; it inherits the redirected env, so its real tmux
	// op must land on the PRIVATE throwaway server.
	spawn := exec.Command(binary, sess)
	spawn.Env = os.Environ()
	if out, err := spawn.CombinedOutput(); err != nil {
		t.Fatalf("spawned guard-off binary failed to create %q on the private server: %v\n%s", sess, err, out)
	}

	// Non-vacuous: the session really exists — on the PRIVATE server (ambient
	// redirected env). has-session is a read-only probe (not a destructive literal).
	if err := exec.Command("tmux", "has-session", "-t", "="+sess).Run(); err != nil {
		t.Fatalf("session %q not found on the private server (redirect/exec failed): %v", sess, err)
	}
	// No explicit kill-session cleanup (that literal would trip the source scan and
	// require an allowlist entry): the throwaway server — and this session with it —
	// is reaped by the package TestMain's tmuxisolation.Setup (kill-server).

	// The operator's real socket is byte-for-byte unchanged: the spawned binary
	// honored TMUX_TMPDIR and never reached the real socket.
	realAfter := listSessionsOnRealSocket(t)
	if !reflect.DeepEqual(realBefore, realAfter) {
		t.Fatalf("operator real-socket session list changed after a spawned guard-off binary op:\n  before=%v\n  after =%v",
			realBefore, realAfter)
	}
	for _, s := range realAfter {
		if s == sess {
			t.Fatalf("session %q leaked onto the operator's real socket", sess)
		}
	}
}
