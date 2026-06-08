//go:build !integration

package cmd

// storeGuardActive enables the fail-closed STORE-GUARD in the default
// (non-integration) build. It is true exactly when the running binary is a Go
// test binary (isTestBinary, prime.go), so production `af` binaries always use
// the real mcpstore; the integration build sets it false in
// storeguard_integration.go. This mirrors the tmux guardMode build-split: the
// `&& !integration` semantics cannot be read from this non-test .go file at
// runtime, so they MUST come from the build tag (CR1) — a bare isTestBinary()
// is also true under -tags=integration and would wrongly force memstore there.
var storeGuardActive = isTestBinary()
