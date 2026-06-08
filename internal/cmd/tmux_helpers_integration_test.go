//go:build integration

package cmd

import (
	"os/exec"
	"testing"
)

// killStaleTmuxSession kills a tmux session that may have leaked from a prior
// go test process (e.g., a crash mid-test or a real Claude launch that
// outlived its t.Cleanup). It is a REAL `tmux kill-session`, so it lives behind
// //go:build integration: the default-suite tests were migrated onto
// setupHermeticSessions and no longer need it, but the integration suite
// (which genuinely drives real tmux) still does. Safe to call when tmux is
// absent or the session does not exist (#309 Phase 3).
func killStaleTmuxSession(t *testing.T, name string) {
	t.Helper()
	_ = exec.Command("tmux", "kill-session", "-t", name).Run()
}
