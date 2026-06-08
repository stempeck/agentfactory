//go:build integration

package tmux

// guardMode is always false in the integration build: integration tests drive
// the real tmux client unguarded (run via `make test-integration`).
var guardMode = false
