//go:build !integration

package tmux

// guardMode enables the fail-closed test guard in the default (non-integration)
// build. It is true exactly when the running binary is a Go test binary, so
// production `af` binaries are never guarded; the integration build sets it
// false in guard_integration.go.
var guardMode = isTestBinary()
