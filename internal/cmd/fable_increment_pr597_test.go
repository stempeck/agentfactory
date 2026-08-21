package cmd

import (
	"errors"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// Pinning tests for PR #597 unresolved review comments (fable-increment), cmd layer.
// These reassign package vars (doRespawn, recoveryTracks via the fixture) — none may call t.Parallel.

// T1 / S1 (recovery.go:705) — recordRecycleAt arms the recycle fence UNCONDITIONALLY, including on a
// failed respawn. A failed RespawnPane starts no new session, so arming the fence on the still-alive
// wedged session makes recycleFenceBlocks suppress every re-fire — the breaker never climbs to
// max_attempts and AC-7's halt+escalate never engages. The fix arms the fence only when
// respawnErr == nil, keeping the K6 log write unconditional.
func TestFableIncrPr597_T1_FailedRespawnDoesNotArmFence(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	opts := RespawnOptions{
		FactoryRoot:   root,
		AgentName:     "worker",
		Trigger:       triggerContextExhaustion,
		TriggerDetail: recycleDetail{SessionID: "sess-x"},
	}
	recordRecycleAt(opts, errors.New("tmux respawn failed"), now)

	st := loadRecoveryState(root, "worker")
	if st.LastRecoveryAt != "" {
		t.Errorf("a FAILED respawn must NOT arm the recycle fence (AC-7): LastRecoveryAt=%q, want empty", st.LastRecoveryAt)
	}
	if st.LastRecoverySessionID != "" {
		t.Errorf("a FAILED respawn must NOT record a fenced session: LastRecoverySessionID=%q, want empty", st.LastRecoverySessionID)
	}

	// DO-NOT-CHANGE: the K6 recovery-log write stays unconditional even on a failed respawn.
	lines := readRecoveryLogLines(t, root)
	if len(lines) != 1 || lines[0].Outcome != outcomeRespawnFailed {
		t.Fatalf("the K6 log write must remain unconditional on failure: got %d lines (want 1, outcome %q)", len(lines), outcomeRespawnFailed)
	}
}

// T1 / S1 (AC-7) — the end-to-end consequence: when RespawnPane keeps failing, the breaker must climb
// to halted and escalate. Before the fix the fence armed on the first failed respawn blocks every
// later re-fire, so Attempts sticks at 1 and the agent shows "recovering" forever.
func TestFableIncrPr597_T1_FailedRespawnClimbsToHaltAndEscalates(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	f.writeStartupWithSupervisor(t)
	f.sessionLive(true)

	// Route the executor's respawn through the real funnel but with a FAILING RespawnPane, so
	// recordRecycle still runs (logs the failure and, pre-fix, arms the fence) and the funnel returns
	// the error — the production failed-respawn path.
	sentinel := errors.New("tmux respawn failed")
	doRespawn = func(opts RespawnOptions) error {
		opts.Tx = &mockTmux{respawnErr: sentinel}
		return respawnSession(opts)
	}

	base := time.Now().UTC()
	var final recoveryState
	for i := 0; i < 8; i++ {
		now := base.Add(time.Duration(i) * time.Minute)
		reading := plantSnapshot(t, f.root, f.agent, "sess-a", 92, now.Add(-5*time.Second), now, f.cfg)
		evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now)
		final = f.state(t)
		if final.Halted {
			break
		}
	}

	if !final.Halted {
		t.Fatalf("AC-7: repeated failed respawns must climb the breaker to halted; got attempts=%d halted=%v", final.Attempts, final.Halted)
	}
	if final.HaltReason != haltReasonMaxAttempts {
		t.Errorf("halt_reason = %q, want %q", final.HaltReason, haltReasonMaxAttempts)
	}
	if !final.EscalationSent {
		t.Error("AC-7: a halt must escalate to an existing recipient (escalation_sent must be true)")
	}
}

// T1 / S1 (DO-NOT-CHANGE) — a SUCCESSFUL respawn must still arm the fence; the fix must not disarm the
// success path (which protects the freshly-started session from its predecessor's final high snapshot).
func TestFableIncrPr597_T1_SuccessfulRespawnStillArmsFence(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	opts := RespawnOptions{
		FactoryRoot:   root,
		AgentName:     "worker",
		Trigger:       triggerContextExhaustion,
		TriggerDetail: recycleDetail{SessionID: "sess-x"},
	}
	recordRecycleAt(opts, nil, now)

	st := loadRecoveryState(root, "worker")
	if st.LastRecoveryAt == "" {
		t.Error("a SUCCESSFUL respawn must still arm the fence")
	}
	if st.LastRecoverySessionID != "sess-x" {
		t.Errorf("LastRecoverySessionID = %q, want %q", st.LastRecoverySessionID, "sess-x")
	}
}

// T2 / C1 (recovery.go:823) — the confirm_ticks debounce is not session-segmented: consecutiveHigh
// persists across a session replacement, so a fresh replacement session's FIRST high reading can fire
// on a single reading, bypassing the debounce. The fix resets consecutiveHigh on a session-id change.
func TestFableIncrPr597_T2_NewSessionResetsDebounce(t *testing.T) {
	cfg := testRecoveryConfig() // ConfirmTicks=2, ContextThresholdPct=85
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	// Separate roots keep each reading pinned to exactly its own session id.
	readingA := plantSnapshot(t, t.TempDir(), "worker", "sess-a", 92, now, now, cfg)
	readingB := plantSnapshot(t, t.TempDir(), "worker", "sess-b", 92, now, now, cfg)

	track := &agentRecoveryTrack{}

	// Session A: one high reading climbs the counter to 1 (< confirm_ticks) — no fire.
	if v := evaluateExhaustion(readingA, track, cfg); v.fire {
		t.Fatal("one high reading must not fire (confirm_ticks=2)")
	}

	// A NEW session's first high reading must NOT fire — consecutiveHigh must reset on session change.
	if v := evaluateExhaustion(readingB, track, cfg); v.fire {
		t.Fatalf("a NEW session's first high reading must not fire — consecutiveHigh must reset on session change (counter now %d)", track.consecutiveHigh)
	}
}

// T2 / C1 (DO-NOT-CHANGE) — same-session sustained readings must still climb and fire at confirm_ticks;
// the session-segmentation fix must not weaken the normal debounce.
func TestFableIncrPr597_T2_SameSessionStillDebouncesAndFires(t *testing.T) {
	cfg := testRecoveryConfig()
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	reading := plantSnapshot(t, t.TempDir(), "worker", "sess-a", 92, now, now, cfg)

	track := &agentRecoveryTrack{}
	if v := evaluateExhaustion(reading, track, cfg); v.fire {
		t.Fatal("first high reading must not fire (confirm_ticks=2)")
	}
	if v := evaluateExhaustion(reading, track, cfg); !v.fire {
		t.Fatal("second same-session high reading must fire (confirm_ticks=2)")
	}
}
