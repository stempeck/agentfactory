//go:build !integration

package tmux

import "testing"

// TestCurrentSessionNameBenignUnderGuard proves K2's self-session query is a read-only
// probe: in the guarded default test build it returns a benign zero value ("", nil) and
// never shells out to a real tmux, mirroring ListSessions. It must NOT route through
// guardOp, which panics on a production identity and would break every hermetic test
// (IMPLREADME AC #3).
func TestCurrentSessionNameBenignUnderGuard(t *testing.T) {
	got, err := NewTmux().CurrentSessionName()
	if err != nil {
		t.Fatalf("CurrentSessionName() under guard returned error %v, want nil", err)
	}
	if got != "" {
		t.Fatalf("CurrentSessionName() under guard = %q, want %q (benign zero value)", got, "")
	}
}
