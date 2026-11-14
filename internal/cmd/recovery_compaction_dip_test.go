package cmd

import (
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestRecoveryIgnoresHostCompactionDip pins that a host auto-compaction between two readings above
// the threshold breaks the confirmation run. Each 85.4 reading passes the 85 threshold, and with
// ConfirmTicks 2 only the dip between them keeps them from counting as consecutive, so a breaker
// that skipped the dip ticks would fire on the last one.
func TestRecoveryIgnoresHostCompactionDip(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	for i, pct := range []float64{60, 75, 84, 85.4, 30, 35, 85.4} {
		at := now.Add(time.Duration(i) * 30 * time.Second)
		reading := plantSnapshot(t, f.root, f.agent, "sess-a", pct, at.Add(-5*time.Second), at, f.cfg)
		if d := evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, at); d.verdict.fire {
			t.Fatalf("tick %d fired at %.1f%% — a compaction dip between two high readings must break the confirmation run", i, pct)
		}
	}
	if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
		t.Errorf("recovery log has %d lines, want 0 — no recycle may follow a host compaction dip", got)
	}
}
