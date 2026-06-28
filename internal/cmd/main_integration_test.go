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

// afRequireRealStore captures AF_REQUIRE_REAL_STORE BEFORE NeutralizeAFEnv()
// wipes the AF_* family (tmuxisolation.AFEnvKeepPrefixes keeps only AF_TEST_*),
// so requirePython3WithServerDeps can still hard-fail under the CI signal. It
// MUST be read here, not inside the helper: the helper runs during m.Run() —
// after the wipe — where os.Getenv("AF_REQUIRE_REAL_STORE") would return "" and
// the guard would be dead code. (The sibling mcpstore_test.go reads os.Getenv
// inline only because its TestMain does not call NeutralizeAFEnv.) See issue
// #458 Gap-4 / the unresolved review thread on agents_list_integration_test.go.
var afRequireRealStore bool

func TestMain(m *testing.M) {
	// #389: block the integration suite when launched inside a live factory, BEFORE
	// m.Run so no real tmux/worktree/git resource is created. Detection is by CWD under
	// a factory worktree (see ciguard.go), so it touches no env and is unaffected by the
	// NeutralizeAFEnv call below. No-op in CI / a clean checkout.
	tmuxisolation.GuardCIOnly()
	// Capture the CI signal before NeutralizeAFEnv unsets it (see afRequireRealStore).
	afRequireRealStore = os.Getenv("AF_REQUIRE_REAL_STORE") == "1"
	tmuxisolation.NeutralizeAFEnv()
	os.Exit(m.Run())
}
