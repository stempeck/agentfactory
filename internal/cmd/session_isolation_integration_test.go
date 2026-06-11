//go:build integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/session"
)

// Issue #309 Phase 6 — the real-tmux belt-and-suspenders isolation PROOF
// (design-doc AC-1/AC-2/AC-6 "Verified by"; security.md SEC-5). It is the
// integration sibling of the untagged pure-fake test TestDefaultSuiteIssuesNoRealTmux
// (Phase 3): it stands up its own production-class "factory" session — a sentinel
// named af-sentinel-<hex> via REAL tmux — then runs the under-test session
// lifecycle code (the launchWatchdog spawn path and the `af down` kill path)
// through the hermetic seam installed by setupHermeticSessions(t), and asserts the
// sentinel is byte-for-byte untouched (still alive, same pane PID).
//
// Because the hermetic seam redirects every would-be tmux op to the recording
// fake under an af-test-<hex>- namespace, the sentinel survives trivially in the
// happy path. The test's value is FAIL-ON-REVERT: if the Phase-1 name/client seam
// or the Phase-2 hermetic helper regressed, `af down sentinel-<hex>` would resolve
// to the production name af-sentinel-<hex> and issue a REAL `tmux kill-session`,
// killing the sentinel and turning this test red.
//
// REAL tmux ⇒ this file MUST be //go:build integration (never the default suite —
// the #309 hazard is real tmux ops in `go test ./...`). The function name contains
// "SessionIsolation" so it matches the AC-4 `-run SessionIsolation` exclusion check
// and the AC-3 `-run 'Isolation|Orphan'` integration filter.

// sentinelPanePID returns the pane PID of a tmux session (leading "=" forces an
// exact-name match). A changed PID after the under-test code ran would mean the
// session was killed+recreated rather than left undisturbed.
func sentinelPanePID(t *testing.T, sess string) string {
	t.Helper()
	out, err := exec.Command("tmux", "list-panes", "-t", "="+sess, "-F", "#{pane_pid}").Output()
	if err != nil {
		t.Fatalf("capturing pane pid for %s: %v", sess, err)
	}
	pid := strings.TrimSpace(string(out))
	if pid == "" {
		t.Fatalf("empty pane pid for %s", sess)
	}
	return pid
}

func TestSessionIsolation_SentinelUntouched(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Deterministic per-test hex (sha256 of t.Name(), never rand/clock — ADR-018).
	// The sentinel's production session name is the af- prefixed resolution of the
	// agent name, i.e. exactly what the under-test code would target on a revert.
	hex := hashName(t.Name())
	agentName := "sentinel-" + hex
	sentinelSession := "af-sentinel-" + hex

	// Factory root with the sentinel agent registered: runDown resolves the agent
	// from agents.json, and an unregistered name short-circuits ("unknown agent")
	// before ever reaching Stop().
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1,"name":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	agentsJSON := `{"agents":{"manager":{"type":"interactive","description":"manager"},"` +
		agentName + `":{"type":"autonomous","description":"sentinel agent"}}}`
	if err := os.WriteFile(filepath.Join(afDir, "agents.json"), []byte(agentsJSON), 0o644); err != nil {
		t.Fatal(err)
	}

	// Stand up the REAL production-class sentinel session. killStaleTmuxSession
	// first defends against a leak from a prior crashed run of the same name.
	killStaleTmuxSession(t, sentinelSession)
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", sentinelSession, "-c", root).CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session %s: %s\n%s", sentinelSession, err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", sentinelSession).Run() })

	pidBefore := sentinelPanePID(t, sentinelSession)

	// Install the hermetic seam AFTER t.TempDir() (design R-7: seam restores must
	// run before the temp-dir delete). All four session seams now resolve to an
	// af-test-<hex>- namespace backed by the recording fake.
	fake, _ := setupHermeticSessions(t)

	// Mark the NAMESPACED sentinel present so runDown's Stop() clears its
	// HasSession pre-flight and proceeds into the destructive KillSession — which
	// hits the fake, never real tmux.
	hermeticName := session.SessionName(agentName)
	fake.present[hermeticName] = true

	t.Chdir(root)

	cmd, _ := newTestCmd()

	// Drive the under-test session-lifecycle code through the fake:
	//   spawn path — launchWatchdog -> NewSession(af-test-<hex>-watchdog)
	//   kill  path — af down sentinel-<hex> -> Stop() -> KillSession(af-test-<hex>-sentinel-<hex>)
	launchWatchdog(cmd, newCmdTmux(), root, nil)
	_ = runDown(cmd, []string{agentName})

	// 1. The under-test code actually went through the fake (seam is engaged).
	if len(fake.ops) == 0 {
		t.Fatal("under-test code issued no ops through the fake; cannot prove isolation")
	}

	// 2. The destructive kill happened AND was redirected to the namespaced name.
	if !hasOp(fake.ops, "KillSession "+hermeticName) {
		t.Fatalf("expected a KillSession against the namespaced name %q; ops=%v", hermeticName, fake.ops)
	}

	// 3. No recorded op may target a real production session name.
	for _, op := range fake.ops {
		if strings.Contains(op, sentinelSession) {
			t.Errorf("recorded op targeted the production sentinel name %q: %q", sentinelSession, op)
		}
		if strings.Contains(op, "af-watchdog") {
			t.Errorf("recorded op targeted the production watchdog name af-watchdog: %q", op)
		}
	}

	// 4. The real sentinel is untouched: still alive (exact-name match)...
	if err := exec.Command("tmux", "has-session", "-t", "="+sentinelSession).Run(); err != nil {
		t.Fatalf("real sentinel %q was disturbed by the under-test code (has-session failed: %v)", sentinelSession, err)
	}
	// ...and not killed+recreated (pane PID unchanged).
	if pidAfter := sentinelPanePID(t, sentinelSession); pidAfter != pidBefore {
		t.Errorf("sentinel pane PID changed: before=%s after=%s (session was replaced)", pidBefore, pidAfter)
	}

	// The non-reset `af down` path and the watchdog spawn create no issuestore, so
	// there is no py.issuestore.server child to reap here (terminateMCPServer would
	// be a no-op). The orphan backstop is proven separately by File 2
	// (mcpstore.TestOrphanServerBackstop, -run Orphan).
}
