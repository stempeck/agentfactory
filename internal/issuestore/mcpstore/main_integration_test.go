//go:build integration

package mcpstore_test

import (
	"os"
	"testing"

	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
)

// TestMain blocks the integration suite from running inside a live agent factory
// (issue #389) — before m.Run(), so no real Python MCP server / sqlite resource is
// spawned — then runs the package's tests unchanged. The integration tests here live
// in package mcpstore_test, so this TestMain matches that package. GuardCIOnly is a
// no-op in CI / a clean checkout, and this package has no other TestMain in either
// build, so there is no collision.
func TestMain(m *testing.M) {
	tmuxisolation.GuardCIOnly()
	os.Exit(m.Run())
}
