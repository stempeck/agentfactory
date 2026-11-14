//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
)

// TestMain redirects this package's default-suite test binary to a private
// throwaway tmux server (#317 Phase 2b out-of-process backstop). The
// //go:build !integration tag keeps it out of the integration build, which must
// reach the operator's real socket. See internal/testsupport/tmuxisolation.
func TestMain(m *testing.M) {
	// The real codex binary is present in dev/CI containers, so any install-agents
	// test that reaches preflightCodexSubscription's CLI-present branch without an
	// explicit seam override would exec `codex login --device-auth` and hang to the
	// test timeout. Default the device-auth seam to a no-op; tests that exercise
	// device-auth behavior override it themselves (D9).
	runCodexDeviceAuth = func(context.Context, io.Writer, io.Writer) error { return nil }
	// The same holds for the window plumbing: a launch would otherwise exec the host's own
	// `claude --version` and probe its harness, `af up` would read the host's ~/.claude/settings.json,
	// and a sling would reach a real tmux to start af-watchdog. Tests that need any of them set the
	// seam and restore it.
	claudeCodeVersionFn = func() string { return "" }
	probeHarnessWindowFn = func(context.Context, map[string]string) (harnessWindowProbe, error) {
		return harnessWindowProbe{}, errHarnessAbsent
	}
	userScopeEnvFn = func() map[string]json.RawMessage { return nil }
	ensureSlingWatchdog = func(*cobra.Command, string) {}
	os.Exit(tmuxisolation.Setup(m))
}
