//go:build integration

package tmux

import "testing"

// TestGuard_UnguardedRealClient verifies that under the integration build the
// guard is compiled out: guardMode is false and NewTmux() returns an unguarded
// real client (AC #5 / AC-6 parity). It does NOT exec any destructive op
// against a production session.
func TestGuard_UnguardedRealClient(t *testing.T) {
	if guardMode {
		t.Fatal("guardMode must be false in the integration build")
	}
	tx := NewTmux()
	if tx.guard {
		t.Fatal("NewTmux().guard must be false in the integration build (unguarded real client)")
	}
}
