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
	tmuxisolation.NeutralizeAFEnv()
	os.Exit(m.Run())
}
