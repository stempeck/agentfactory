//go:build integration

package cmd

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

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

// afRequireLiveClaude is the same signal for the same reason, one dependency over: it turns the live
// deny probe's only permitted skip (no claude CLI on PATH) into a failure, for a lane that installs
// the CLI on purpose. Captured here for the identical reason as above — read inside the subtest it
// would always be "", and a dead switch reads exactly like a satisfied one.
var afRequireLiveClaude bool

// liveClaudeBinary is the one way a test of the integration build gets the real harness. The CI job
// names it in AF_TEST_CLAUDE_BIN and never puts it on the job's PATH, so only the tests that call
// this wake it; the AF_TEST_ prefix survives NeutralizeAFEnv. With the variable set, the claude the
// lookup resolves must be that very file (resolveLiveClaudeBinary), so a negative control cannot run
// the pinned harness by accident and pass green.
func liveClaudeBinary(t *testing.T) string {
	t.Helper()
	named := os.Getenv("AF_TEST_CLAUDE_BIN")
	if named != "" {
		t.Setenv("PATH", filepath.Dir(named)+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	path, found, err := resolveLiveClaudeBinary(named, exec.LookPath)
	if err != nil {
		t.Fatalf("AF_TEST_CLAUDE_BIN: %v", err)
	}
	if !found {
		if afRequireLiveClaude {
			t.Fatalf("%s=1 but no claude CLI is on PATH and AF_TEST_CLAUDE_BIN is unset; this lane asked to "+
				"guarantee live coverage, so a skip here is the vacuous green it was set to prevent", liveProbeRequireEnv)
		}
		t.Skipf("no claude CLI on PATH and AF_TEST_CLAUDE_BIN unset; set %s=1 to make this a failure", liveProbeRequireEnv)
	}
	return path
}

func TestMain(m *testing.M) {
	// #389: block the integration suite when launched inside a live factory, BEFORE
	// m.Run so no real tmux/worktree/git resource is created. Detection is by CWD under
	// a factory worktree (see ciguard.go), so it touches no env and is unaffected by the
	// NeutralizeAFEnv call below. No-op in CI / a clean checkout.
	tmuxisolation.GuardCIOnly()
	// Capture the CI signals before NeutralizeAFEnv unsets them (see afRequireRealStore).
	afRequireRealStore = os.Getenv("AF_REQUIRE_REAL_STORE") == "1"
	afRequireLiveClaude = os.Getenv(liveProbeRequireEnv) == "1"
	tmuxisolation.NeutralizeAFEnv()
	// The untagged launch tests run under this TestMain too, with the tmux guard compiled out and
	// whatever claude the host has on PATH: without these defaults a launch would exec that
	// `claude --version`, a watched launch would probe that harness under a 15 s deadline and could
	// store a row that refuses it, and a sling would create a real af-watchdog session on the runner.
	claudeCodeVersionFn = func() string { return "" }
	probeHarnessWindowFn = func(context.Context, map[string]string) (harnessWindowProbe, error) {
		return harnessWindowProbe{}, errHarnessAbsent
	}
	userScopeEnvFn = func() map[string]json.RawMessage { return nil }
	ensureSlingWatchdog = func(*cobra.Command, string) {}
	os.Exit(m.Run())
}
