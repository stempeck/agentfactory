//go:build !integration

package cmd

import (
	"os"
	"testing"

	"github.com/stempeck/agentfactory/internal/testsupport/tmuxisolation"
)

// TestMain redirects this package's default-suite test binary to a private
// throwaway tmux server (#317 Phase 2b out-of-process backstop). The
// //go:build !integration tag keeps it out of the integration build, which must
// reach the operator's real socket. See internal/testsupport/tmuxisolation.
func TestMain(m *testing.M) { os.Exit(tmuxisolation.Setup(m)) }
