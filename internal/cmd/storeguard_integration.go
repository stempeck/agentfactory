//go:build integration

package cmd

// storeGuardActive is always false in the integration build: integration tests
// construct the real mcpstore (run via `make test-integration`), preserving the
// production issue-store path (AC-6). Mirrors guard_integration.go in the tmux
// package.
var storeGuardActive = false
