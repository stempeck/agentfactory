//go:build integration

package session

import (
	"os"
	"testing"

	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
)

// TestMain blocks the integration suite from running inside a live agent factory
// (issue #389) — before m.Run(), so no real tmux session is started — then runs the
// package's tests unchanged. GuardCIOnly is a no-op in CI / a clean checkout. The
// default-suite TestMain in main_test.go is //go:build !integration, so this
// integration-tagged TestMain does not collide with it.
func TestMain(m *testing.M) {
	tmuxisolation.GuardCIOnly()
	os.Exit(m.Run())
}
