//go:build integration

package worktree

import (
	"os"
	"testing"

	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
)

// TestMain blocks the integration suite from running inside a live agent factory
// (issue #389) — before m.Run(), so no real git worktree/branch is created — then runs
// the package's tests unchanged. GuardCIOnly is a no-op in CI / a clean checkout, so
// the legitimate `make test-integration` CI path is unaffected. This package has no
// other TestMain (the default suite uses Go's implicit one), so there is no collision.
func TestMain(m *testing.M) {
	tmuxisolation.GuardCIOnly()
	os.Exit(m.Run())
}
