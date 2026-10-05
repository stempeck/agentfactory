//go:build !integration

package cmd

import (
	"context"
	"io"
	"os"
	"testing"

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
	os.Exit(tmuxisolation.Setup(m))
}
