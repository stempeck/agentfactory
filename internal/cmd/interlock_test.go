//go:build !integration

package cmd

import (
	"os"
	"os/exec"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
)

// TestInterlock is the behavioral proof of the #317 Phase 2b out-of-process
// backstop: a raw, socket-flag-free `tmux` exec from a default-suite test must
// land on the private throwaway server installed by the package's TestMain
// (tmuxisolation.Setup), never on the operator's real socket — the #316 hazard,
// one process removed (the in-process GUARD is off in a spawned tmux because its
// isTestBinary() is false).
//
// The test snapshots the operator's real-socket session list, execs a real
// `tmux new-session` under the ambient (redirected) env, and asserts the real
// socket is byte-for-byte unchanged while the new session exists on the private
// server (non-vacuous). It refuses to exec tmux at all unless the redirect is
// active — so without the TestMain backstop it fails fast WITHOUT touching the
// operator's real socket.
func TestInterlock(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	// Fail-closed gate. The Phase 2b TestMain must have redirected this process
	// tree (TMUX_TMPDIR set to a private dir, $TMUX unset) BEFORE any raw tmux
	// exec. If it has not, we must NOT exec tmux — that would reach the
	// operator's real socket (the #316 hazard) — so fail fast here instead. This
	// is exactly what makes the test red when the backstop is absent.
	privateDir := os.Getenv("TMUX_TMPDIR")
	if privateDir == "" {
		t.Fatal("TMUX_TMPDIR is not set: the Phase 2b TestMain redirect is not active; " +
			"refusing to exec tmux against the operator's real socket")
	}
	if v := os.Getenv("TMUX"); v != "" {
		t.Fatalf("TMUX is still set (%q): the Phase 2b TestMain must unset it; "+
			"refusing to exec tmux that could fall back to the operator's real socket", v)
	}

	// Snapshot the operator's REAL socket (reached via the captured original).
	realBefore := listSessionsOnRealSocket(t)

	// Raw, socket-flag-free tmux exec — honors the ambient (redirected) env, so
	// it must land on the PRIVATE throwaway server.
	sess := "af-test-interlock-" + hashName(t.Name())
	exec.Command("tmux", "kill-session", "-t", "="+sess).Run() // best-effort pre-clean (private server)
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", sess).CombinedOutput(); err != nil {
		t.Fatalf("new-session on the private server failed: %v\n%s", err, out)
	}
	t.Cleanup(func() { exec.Command("tmux", "kill-session", "-t", "="+sess).Run() })

	// Non-vacuous: the session really exists — on the PRIVATE server.
	if err := exec.Command("tmux", "has-session", "-t", "="+sess).Run(); err != nil {
		t.Fatalf("session %q not found on the private server (redirect/exec failed): %v", sess, err)
	}

	// The operator's real socket is byte-for-byte unchanged: the spawned tmux
	// honored TMUX_TMPDIR and never reached the real socket.
	realAfter := listSessionsOnRealSocket(t)
	if !reflect.DeepEqual(realBefore, realAfter) {
		t.Fatalf("operator real-socket session list changed after a redirected tmux op:\n  before=%v\n  after =%v",
			realBefore, realAfter)
	}
	for _, s := range realAfter {
		if s == sess {
			t.Fatalf("session %q leaked onto the operator's real socket", sess)
		}
	}
}

// listSessionsOnRealSocket lists session names on the operator's real socket,
// reached via the original (pre-redirect) TMUX_TMPDIR captured by Setup. With an
// empty original the child inherits no TMUX_TMPDIR and tmux uses its default
// socket location — exactly the operator socket the suite was launched from.
// $TMUX is stripped from the child env so listing is socket-dir driven.
func listSessionsOnRealSocket(t *testing.T) []string {
	t.Helper()
	cmd := exec.Command("tmux", "list-sessions", "-F", "#{session_name}")
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
	cmd.Env = env
	out, err := cmd.Output()
	if err != nil {
		return nil // no server on the real socket → empty list
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	sort.Strings(names)
	return names
}
