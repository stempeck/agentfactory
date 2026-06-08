// Package tmuxisolation redirects a test binary's entire process tree to a
// private, throwaway tmux server so that a default-suite test — or any child it
// execs (a spawned `af`, or a raw `tmux`) — can never reach the operator's real
// tmux server. It exists for issue #317 (incident #316: a default-suite test
// killed a live af-manager session).
//
// It is wired from each package's TestMain:
//
//	//go:build !integration
//	func TestMain(m *testing.M) { os.Exit(tmuxisolation.Setup(m)) }
//
// # Load-bearing: os.Unsetenv("TMUX")
//
// The tmux client execs `exec.Command("tmux", …)` with no -L/-S socket flag
// (internal/tmux/tmux.go), so tmux selects its server from the ambient
// environment. Setting TMUX_TMPDIR alone is NOT sufficient: when the suite runs
// inside a tmux session — the actual #316 environment, since agents run in tmux
// panes — $TMUX encodes the operator's real server socket and TAKES PRECEDENCE
// over TMUX_TMPDIR, pointing a child tmux back at the operator's real server.
// $TMUX MUST therefore also be unset, or the out-of-process backstop is
// ineffective in exactly the #316 scenario. Do not "optimize away" the
// Unsetenv("TMUX").
//
// # Cross-phase contract: OriginalTMUXTMPDIR
//
// Setup captures the operator's original TMUX_TMPDIR/TMUX BEFORE redirecting,
// and exposes the socket dir via OriginalTMUXTMPDIR(). It is the ONLY mechanism
// by which a test can reach the operator's real socket after the redirect (a raw
// exec carrying TMUX_TMPDIR=<original>). The Phase 5 SENTINEL consumes this; do
// not drop or rename it.
//
// # Why this is not a library env read
//
// This is a test-support package, not library code. The env it writes
// (TMUX_TMPDIR/TMUX) is consumed only by spawned `tmux` subprocesses, never read
// by any internal/* library package, so it is exempt from ADR-004's no-env rule
// (see internal/cmd/env_hermetic_test.go's directory exemption and ADR-004's
// "Sanctioned Exemption" note). It imports only stdlib so the four packages' main_test.go can import it with no cycle, and
// the testing import is dead-code-eliminated from the production `af` binary.
package tmuxisolation

import (
	"os"
	"os/exec"
	"testing"
)

// originalTMUXTMPDIR and originalTMUX hold the operator's environment as captured
// by Setup BEFORE any redirect, so the real socket remains reachable afterwards.
var (
	originalTMUXTMPDIR string
	originalTMUX       string
)

// Setup redirects the calling test binary's process tree to a private throwaway
// tmux server for the duration of m.Run(), then reaps it. It must be called from
// TestMain and its return value passed to os.Exit:
//
//	func TestMain(m *testing.M) { os.Exit(tmuxisolation.Setup(m)) }
//
// Order matters: the operator's original TMUX_TMPDIR/TMUX are captured FIRST (so
// OriginalTMUXTMPDIR can later reach the real socket), then TMUX_TMPDIR is
// pointed at a fresh private dir and TMUX is unset (load-bearing — see package
// doc), then the tests run, then the throwaway server is killed and the dir
// removed.
func Setup(m *testing.M) int {
	// 1. Capture the operator's original environment FIRST, before any mutation.
	originalTMUXTMPDIR = os.Getenv("TMUX_TMPDIR")
	originalTMUX = os.Getenv("TMUX")

	// 2. Create a private temp dir to host the throwaway server's socket.
	dir, err := os.MkdirTemp("", "af-tmux-isolation-")
	if err != nil {
		// Fail closed: without isolation we must not run tests that could reach
		// the operator's real socket.
		panic("tmuxisolation: cannot create private TMUX_TMPDIR: " + err.Error())
	}

	// 3 + 4. Redirect the whole process tree to the private socket and remove the
	// inside-tmux fallback. Both are required (see package doc).
	os.Setenv("TMUX_TMPDIR", dir)
	os.Unsetenv("TMUX")

	// 5. Run the package's tests against the private server.
	code := m.Run()

	// 6. Reap the throwaway server (best-effort; it lives under the redirected
	// TMUX_TMPDIR, so this never touches the operator's real socket).
	_ = exec.Command("tmux", "kill-server").Run()

	// 7. Remove the private dir (and its socket).
	_ = os.RemoveAll(dir)

	// 8. Propagate the test exit code.
	return code
}

// OriginalTMUXTMPDIR returns the operator's TMUX_TMPDIR as captured BEFORE Setup
// redirected it (empty string if the operator had none set). It is the
// cross-phase handle the Phase 5 SENTINEL uses to reach the operator's real tmux
// socket via a raw exec carrying TMUX_TMPDIR=<this value>. Valid only after
// Setup has run.
func OriginalTMUXTMPDIR() string {
	return originalTMUXTMPDIR
}

// OriginalTMUX returns the operator's $TMUX as captured BEFORE Setup unset it
// (empty string if the suite was not running inside tmux). Exposed alongside
// OriginalTMUXTMPDIR so a later phase can fully reconstruct the operator's
// pre-redirect tmux environment if needed. Valid only after Setup has run.
func OriginalTMUX() string {
	return originalTMUX
}
