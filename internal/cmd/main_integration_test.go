//go:build integration

package cmd

import (
	"os"
	"testing"

	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
)

// Integration build: wipe the AF_*/CLAUDE_* family (so the agent-gen tests
// cannot leak into an ambient AF_SOURCE_ROOT under `make test-integration`),
// but DELIBERATELY without the tmux redirect — integration tests legitimately
// reach the real tmux socket, so we must NOT call Setup (the tmuxisolation
// redirect) here. Do not "fix" this to call Setup. See #327 (Comp-A′ / L-2).
func TestMain(m *testing.M) {
	// #389: block the integration suite when launched inside a live factory, BEFORE
	// m.Run so no real tmux/worktree/git resource is created. Detection is by CWD under
	// a factory worktree (see ciguard.go), so it touches no env and is unaffected by the
	// NeutralizeAFEnv call below. No-op in CI / a clean checkout.
	tmuxisolation.GuardCIOnly()
	tmuxisolation.NeutralizeAFEnv()
	os.Exit(m.Run())
}
