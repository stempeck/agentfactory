package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/statusline"
)

// The K5/K8/K14/K18/K23 lane. None of these may call t.Parallel: they reassign package vars
// (sendHandoffMail, newCmdTmux, recoveryOpenStep, doRespawn) and share recoveryTracks.

// recordedMail is one captured send. Asserting the COUNT matters as much as the content: an
// implementation that sets escalation_sent before calling the seam produces sent==true with zero
// sends, and sendHandoffMail short-circuits to nil under `go test` (handoff.go:143-145), so an
// outcome check that does not override the seam is vacuously green (Gotcha 12).
type recordedMail struct{ recipient, subject, body string }

type mailRecorder struct {
	sent []recordedMail
	err  error
}

func (m *mailRecorder) install(t *testing.T) *mailRecorder {
	t.Helper()
	orig := sendHandoffMail
	sendHandoffMail = func(agentName, subject, body string) error {
		m.sent = append(m.sent, recordedMail{agentName, subject, body})
		return m.err
	}
	t.Cleanup(func() { sendHandoffMail = orig })
	return m
}

// recoveryFixture is the shared setup. Every seam that would otherwise reach a real store, a real
// tmux or a real mailbox is overridden here rather than per-test, because forgetting any one of
// them does not fail — it silently changes which branch runs. recoveryOpenStep is the sharpest:
// its production body returns ("", false, true) for every agent in the default test build (the
// store guard hands back an empty memstore), so a test that forgets it runs the K23 no-step branch
// and passes for the wrong reason.
type recoveryFixture struct {
	root     string
	agent    string
	agentDir string
	mail     *mailRecorder
	tmux     *fakeTmux
	cfg      config.RecoveryConfig
}

func newRecoveryFixture(t *testing.T, agent string) *recoveryFixture {
	t.Helper()
	root := setupTestFactoryForDone(t, agent)

	f := &recoveryFixture{
		root:     root,
		agent:    agent,
		agentDir: resolveAgentDir(root, agent),
		mail:     (&mailRecorder{}).install(t),
		tmux:     newFakeTmux(),
		cfg:      testRecoveryConfig(),
	}

	origTmux := newCmdTmux
	newCmdTmux = func() cmdTmux { return f.tmux }
	t.Cleanup(func() { newCmdTmux = origTmux })

	// Default: no open step, and the store COULD answer. Tests that exercise the step-holder
	// branches override this with stateStep.
	f.stateNoStep()

	resetRecoveryTracks()
	t.Cleanup(resetRecoveryTracks)

	// The executor must never reach real tmux: the ADR-018 guard panics on any af- pane.
	origRespawn := doRespawn
	doRespawn = func(opts RespawnOptions) error {
		opts.Tx = &mockTmux{}
		return respawnSession(opts)
	}
	t.Cleanup(func() { doRespawn = origRespawn })

	return f
}

// stateStep states that the agent holds stepID and the store answered.
func (f *recoveryFixture) stateStep(t *testing.T, stepID string) {
	t.Helper()
	orig := recoveryOpenStep
	recoveryOpenStep = func(string) (string, bool, bool) { return stepID, true, true }
	t.Cleanup(func() { recoveryOpenStep = orig })
}

// stateNoStep states that the agent holds no step and the store answered — the K23 class.
func (f *recoveryFixture) stateNoStep() {
	recoveryOpenStep = func(string) (string, bool, bool) { return "", false, true }
}

// stateStoreDown states that the step is UNKNOWN — the fail-closed case.
func (f *recoveryFixture) stateStoreDown(t *testing.T) {
	t.Helper()
	orig := recoveryOpenStep
	recoveryOpenStep = func(string) (string, bool, bool) { return "", false, false }
	t.Cleanup(func() { recoveryOpenStep = orig })
}

func (f *recoveryFixture) sessionLive(live bool) {
	f.tmux.present[session.SessionName(escalationTarget)] = live
}

// writeStartupWithSupervisor plants the escalation recipient in startup.json. Without it,
// LoadStartupConfig returns defaults whose Agents slice is nil, and the K8 membership scan reads
// nil as "nobody" — so every escalation would take the absent-recipient branch by default.
func (f *recoveryFixture) writeStartupWithSupervisor(t *testing.T) {
	t.Helper()
	writeTestJSON(t, filepath.Join(f.root, ".agentfactory", "startup.json"), map[string]any{
		"agents": []string{escalationTarget},
	})
}

func (f *recoveryFixture) state(t *testing.T) recoveryState {
	t.Helper()
	return readBreakerState(t, f.root, f.agent)
}

// testRecoveryConfig is a fully-stated config. It is built by hand rather than through
// defaultRecoveryConfig because a literal never passes the validator, and every knob this layer
// reads must therefore be explicit — a zero knob is what recoveryConfigUsable exists to refuse.
func testRecoveryConfig() config.RecoveryConfig {
	enabled := true
	return config.RecoveryConfig{
		Enabled:                  &enabled,
		ContextThresholdPct:      85,
		ContextAdvisoryPct:       70,
		ConfirmTicks:             2,
		StalenessSecs:            180,
		DarkGraceSecs:            600,
		PostRecoveryProgressSecs: 900,
		ProgressBackstopSecs:     7200,
		NoStepEscalationSecs:     3600,
		MaxAttempts:              3,
		AttemptWindowSecs:        1800,
		RateCapMax:               6,
		RateCapWindowSecs:        86400,
	}
}

// plantSnapshot writes one schema-v2 occupancy snapshot and returns the reading the production
// reader produces for it. Observation has no exported constructor, so this round-trip through the
// real writer and the real reader is the ONLY way to obtain a reading that carries a datum — which
// also means these tests exercise the real decode path rather than a hand-built stand-in.
func plantSnapshot(t *testing.T, root, agent, sessionID string, pct float64, writtenAt, now time.Time, cfg config.RecoveryConfig) statusline.ChannelReading {
	t.Helper()
	dir := config.StatuslineSessionsDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Written as raw JSON rather than through WriteSnapshot: that writer throttles a second write
	// for the same session id within 10s into a silent no-op, which would make a multi-tick test
	// quietly assert against a stale file.
	body := map[string]any{
		"schema":               2,
		"session_id":           sessionID,
		"agent":                agent,
		"written_at":           writtenAt.UTC().Format(time.RFC3339),
		"context_used_pct":     pct,
		"context_tokens_used":  int64(pct * 2000),
		"context_tokens_total": int64(200000),
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, sessionID+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	return readOneReading(t, root, agent, now, cfg)
}

func readOneReading(t *testing.T, root, agent string, now time.Time, cfg config.RecoveryConfig) statusline.ChannelReading {
	t.Helper()
	readings, err := statusline.ReadObservations(config.StatuslineSessionsDir(root), statusline.ReadOptions{
		KnownAgents: map[string]struct{}{agent: {}},
		Staleness:   time.Duration(cfg.StalenessSecs) * time.Second,
		DarkAfter:   time.Duration(cfg.DarkGraceSecs) * time.Second,
	}, now)
	if err != nil {
		t.Fatalf("ReadObservations: %v", err)
	}
	return readings[agent]
}

// --- K5: the durable breaker -------------------------------------------------------------------

func TestRecovery_BreakerLatchesAtMaxAttempts(t *testing.T) {
	cfg := testRecoveryConfig()
	cfg.MaxAttempts = 2
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	var st recoveryState
	for i, want := range []string{"", "", haltReasonMaxAttempts} {
		got := noteRecoveryAttempt(&st, triggerContextExhaustion, cfg, now)
		if got != want {
			t.Errorf("attempt %d: halt reason = %q, want %q", i+1, got, want)
		}
	}
}

func TestRecovery_BreakerSurvivesProcessRestart(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.writeStartupWithSupervisor(t)
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	st := recoveryState{Attempts: 3}
	haltRecovery(f.root, f.agent, &st, haltReasonMaxAttempts, f.cfg, now)

	// A FRESH load models the watchdog process restarting — the exact case the in-memory
	// failures map (watchdog.go:343) cannot survive, and the reason AC-7 demands a durable latch.
	reloaded := loadRecoveryState(f.root, f.agent)
	if !reloaded.Halted {
		t.Error("halted must survive a process restart — an in-memory breaker forgets on restart, which is what AC-7 rejects")
	}
	if reloaded.HaltReason != haltReasonMaxAttempts {
		t.Errorf("halt_reason = %q, want %q", reloaded.HaltReason, haltReasonMaxAttempts)
	}

	raw, err := os.ReadFile(filepath.Join(f.root, ".runtime", "recovery", f.agent+".json"))
	if err != nil {
		t.Fatalf("reading breaker file: %v", err)
	}
	if !strings.Contains(string(raw), `"halted": true`) {
		t.Errorf("on-disk breaker must literally record halted:true, got %s", raw)
	}

	// And the executor must refuse to act on it.
	reading := statusline.NoReading()
	if err := recoverExhausted(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, triggerContextExhaustion, f.cfg, now); err == nil ||
		!strings.Contains(err.Error(), "halted") {
		t.Errorf("a halted breaker must refuse the recycle, got err=%v", err)
	}
}

func TestRecovery_BreakerUnreadableStateFailsClosed(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".runtime", "recovery")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	garbage := []byte("{not json at all")
	path := filepath.Join(dir, "worker.json")
	if err := os.WriteFile(path, garbage, 0o644); err != nil {
		t.Fatal(err)
	}

	st := loadRecoveryState(root, "worker")
	if !st.Halted || st.HaltReason != haltReasonCorrupt {
		t.Errorf("an undecodable breaker must read HALTED (a permissive read would resume recycling an agent "+
			"an operator was already told to investigate), got halted=%v reason=%q", st.Halted, st.HaltReason)
	}
	if err := saveRecoveryState(root, "worker", st); err == nil {
		t.Error("saving over unreadable state must be refused — the bytes are the only evidence of what went wrong")
	}
	after, _ := os.ReadFile(path)
	if string(after) != string(garbage) {
		t.Errorf("the corrupt bytes must be preserved, got %s", after)
	}
}

func TestRecovery_BreakerWindowExpiryResetsAttempts(t *testing.T) {
	cfg := testRecoveryConfig()
	cfg.AttemptWindowSecs = 1800
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	st := recoveryState{Attempts: 2, WindowStart: recoveryStamp(now.Add(-2 * time.Hour))}
	if reason := noteRecoveryAttempt(&st, triggerContextExhaustion, cfg, now); reason != "" {
		t.Fatalf("an expired window must not halt, got %q", reason)
	}
	if st.Attempts != 1 {
		t.Errorf("attempts = %d, want 1 after the window expired", st.Attempts)
	}
	if st.WindowStart != recoveryStamp(now) {
		t.Errorf("window_start = %q, want it re-stamped to now", st.WindowStart)
	}
}

// --- K18(a) / C-2: what may and may not rewind the breaker -------------------------------------

// TestRecovery_ReStallOccupancyGrowthNeverRewindsBreaker is the C-2 core. Under a degraded backend
// every useless turn still consumes tokens, so occupancy climbs on exactly the runs where nothing
// is happening. If growth counted as progress the breaker would be rewound by the very failure it
// exists to catch, and AC-7 would never engage.
func TestRecovery_ReStallOccupancyGrowthNeverRewindsBreaker(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	st := recoveryState{
		Attempts:          2,
		WindowStart:       recoveryStamp(now.Add(-1 * time.Minute)),
		PendingStepID:     "step-1",
		PendingStepMarker: "step-1:abcd",
		PendingDeadline:   recoveryStamp(now.Add(-1 * time.Minute)),
	}
	// The step marker is frozen at the value the recovery recorded; only occupancy has moved.
	writeRuntimeMarker(t, f.agentDir, "step_primed", "step-1:abcd")

	settled := confirmPostRecovery(f.root, f.agent, f.agentDir, &st, "step-1", true, true, f.cfg, now)
	if !settled {
		t.Fatal("an expired pending window must settle")
	}
	if st.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 — a failed recovery must neither rewind the breaker nor be "+
			"double-counted (the executor already charged this cycle)", st.Attempts)
	}
	if st.WindowStart == "" {
		t.Error("window_start must not be cleared by a FAILED confirmation — clearing it restarts the window and the breaker never latches")
	}
}

func TestRecovery_ReStallStepAdvanceRewindsBreaker(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	st := recoveryState{
		Attempts:          2,
		WindowStart:       recoveryStamp(now.Add(-1 * time.Minute)),
		PendingStepID:     "step-1",
		PendingStepMarker: "step-1:abcd",
		PendingDeadline:   recoveryStamp(now.Add(-1 * time.Minute)),
	}
	writeRuntimeMarker(t, f.agentDir, "step_primed", "step-2:ef01")

	if !confirmPostRecovery(f.root, f.agent, f.agentDir, &st, "step-1", true, true, f.cfg, now) {
		t.Fatal("an expired pending window must settle")
	}
	if st.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 — a step_primed advance is step-anchored progress", st.Attempts)
	}
}

func TestRecovery_ReStallStepCloseRewindsBreaker(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	st := recoveryState{
		Attempts:          2,
		PendingStepID:     "step-1",
		PendingStepMarker: "step-1:abcd",
		PendingDeadline:   recoveryStamp(now.Add(-1 * time.Minute)),
	}
	writeRuntimeMarker(t, f.agentDir, "step_primed", "step-1:abcd")

	// hasStep=false with stepKnown=true: the step genuinely closed.
	if !confirmPostRecovery(f.root, f.agent, f.agentDir, &st, "", false, true, f.cfg, now) {
		t.Fatal("an expired pending window must settle")
	}
	if st.Attempts != 0 {
		t.Errorf("attempts = %d, want 0 — a closed step confirms the recovery took", st.Attempts)
	}
}

// TestRecovery_ReStallStoreOutageDoesNotConfirm closes the fail-open hole: recoveryOpenStep returns
// no step both when the step closed and when the store could not be reached, and reading the second
// as the first rewinds the breaker on an infrastructure fault.
func TestRecovery_ReStallStoreOutageDoesNotConfirm(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStoreDown(t)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	st := recoveryState{
		Attempts:          2,
		PendingStepID:     "step-1",
		PendingStepMarker: "step-1:abcd",
		PendingDeadline:   recoveryStamp(now.Add(-1 * time.Minute)),
	}
	writeRuntimeMarker(t, f.agentDir, "step_primed", "step-1:abcd")

	settled := confirmPostRecovery(f.root, f.agent, f.agentDir, &st, "", false, false, f.cfg, now)
	if settled {
		t.Error("an unknown step state must leave the confirmation window OPEN, not settle it")
	}
	if st.Attempts != 2 {
		t.Errorf("attempts = %d, want 2 — an unreachable store must neither rewind nor charge the breaker", st.Attempts)
	}
	if st.PendingDeadline == "" {
		t.Error("the pending window must remain armed so a later tick can decide on real evidence")
	}
}

// --- K5: the absolute rate cap -----------------------------------------------------------------

// TestRecovery_RateCapHaltsDespiteProgressSignal pins the class-independent floor. The attempt
// window is rewindable by design; the rate cap is not, which is what bounds a re-stall loop whose
// period (hours, set by context-refill time) no 30-minute window can ever see two attempts inside.
func TestRecovery_RateCapHaltsDespiteProgressSignal(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	cfg := testRecoveryConfig()
	cfg.RateCapMax = 2
	cfg.MaxAttempts = 99
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	var st recoveryState
	for i := 0; i < 2; i++ {
		if reason := noteRecoveryAttempt(&st, triggerContextExhaustion, cfg, now); reason != "" {
			t.Fatalf("attempt %d halted early: %q", i+1, reason)
		}
		// A successful confirmation between attempts rewinds Attempts — and must not touch the cap.
		st.Attempts = 0
		st.WindowStart = ""
	}
	if got := noteRecoveryAttempt(&st, triggerContextExhaustion, cfg, now); got != haltReasonRateCap {
		t.Errorf("halt reason = %q, want %q — the rate cap must latch even though every attempt was "+
			"followed by a progress signal", got, haltReasonRateCap)
	}
	if st.RateCapCount != 3 {
		t.Errorf("rate_cap_count = %d, want 3 — no progress signal may rewind it", st.RateCapCount)
	}
	_ = f
}

func TestRecovery_RateCapIgnoresNonOccupancyClasses(t *testing.T) {
	cfg := testRecoveryConfig()
	cfg.RateCapMax = 1
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	var st recoveryState
	// triggerStepBoundaryHandoff is the #622 C5 addition, and it is the class with the most to lose
	// from being counted: it fires at EVERY step boundary that crosses handoff_pct, so a formula with
	// more steps than RateCapMax would drive a perfectly healthy agent into RECOVERY HALTED for doing
	// exactly what the feature asks of it. The rate cap exists to detect automatic recovery looping;
	// a cooperative recycle the agent itself chose is not that.
	for _, trigger := range []string{triggerCrash, triggerErrorPattern, triggerCompactHandoff, triggerSelfHandoff, triggerStepBoundaryHandoff} {
		if reason := noteRecoveryAttempt(&st, trigger, cfg, now); reason == haltReasonRateCap {
			t.Errorf("%s must not count against the recycle rate cap — it is not evidence that automatic "+
				"recovery is looping", trigger)
		}
	}
	if st.RateCapCount != 0 {
		t.Errorf("rate_cap_count = %d, want 0 for non-occupancy classes", st.RateCapCount)
	}
}

// --- K8: verified escalation --------------------------------------------------------------------

// TestRecovery_EscalationSentButRecipientNotLiveTakesUndeliveredBranch is AC-7 (iii): a send whose
// outcome is unknown is not delivery. `supervisor` is a roster member whose 2,443 lifetime messages
// were all purged unread, which is why bare membership cannot be the delivery test.
func TestRecovery_EscalationSentButRecipientNotLiveTakesUndeliveredBranch(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.writeStartupWithSupervisor(t)
	f.sessionLive(false)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	st := recoveryState{Attempts: 3}
	haltRecovery(f.root, f.agent, &st, haltReasonMaxAttempts, f.cfg, now)

	if len(f.mail.sent) != 1 {
		t.Fatalf("escalation must be sent exactly once, got %d sends", len(f.mail.sent))
	}
	if f.mail.sent[0].recipient != escalationTarget {
		t.Errorf("recipient = %q, want %q", f.mail.sent[0].recipient, escalationTarget)
	}
	if !strings.Contains(f.mail.sent[0].subject, "RECOVERY HALTED") {
		t.Errorf("subject = %q, want it to name the halt", f.mail.sent[0].subject)
	}
	if !st.EscalationSent {
		t.Error("escalation_sent must be true — the send itself succeeded")
	}
	if st.RecipientSessionLive {
		t.Error("recipient_session_live must be false, recorded SEPARATELY from escalation_sent: " +
			"conflating them is what lets a send into a dead mailbox read as delivery (H-3)")
	}
	breadcrumb := filepath.Join(f.root, ".runtime", "recovery_halt_undelivered")
	data, err := os.ReadFile(breadcrumb)
	if err != nil {
		t.Fatalf("the undelivered breadcrumb is the floor beneath every delivery claim: %v", err)
	}
	if !strings.Contains(string(data), "undelivered") {
		t.Errorf("breadcrumb = %q, want it to name the undelivered escalation", data)
	}
}

func TestRecovery_EscalationRecipientAbsentSkipsSend(t *testing.T) {
	for _, tc := range []struct {
		name        string
		writeRoster bool
	}{
		{"absent_from_agents_json", false},
		{"present_in_roster_but_no_live_session_and_no_startup_membership", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			configDir := filepath.Join(root, ".agentfactory")
			if err := os.MkdirAll(configDir, 0o755); err != nil {
				t.Fatal(err)
			}
			agents := map[string]any{"agents": map[string]any{}}
			if tc.writeRoster {
				agents = map[string]any{"agents": map[string]any{
					escalationTarget: map[string]string{"type": "autonomous", "description": "x"},
				}}
			}
			writeTestJSON(t, filepath.Join(configDir, "factory.json"), map[string]any{"type": "factory", "version": 1, "name": "f"})
			writeTestJSON(t, filepath.Join(configDir, "agents.json"), agents)

			rec := (&mailRecorder{}).install(t)
			fake := newFakeTmux()
			origTmux := newCmdTmux
			newCmdTmux = func() cmdTmux { return fake }
			t.Cleanup(func() { newCmdTmux = origTmux })

			st := recoveryState{Attempts: 3}
			haltRecovery(root, "supervisor", &st, haltReasonMaxAttempts, testRecoveryConfig(), time.Now())

			if len(rec.sent) != 0 {
				t.Errorf("existence must be checked BEFORE the send, got %d sends", len(rec.sent))
			}
			if st.EscalationSent {
				t.Error("escalation_sent must be false when no send was attempted")
			}
			if !st.Halted {
				t.Error("the halt must take effect regardless of delivery — AC-7 orders the latch first")
			}
			if _, err := os.Stat(filepath.Join(root, ".runtime", "recovery_halt_undelivered")); err != nil {
				t.Errorf("an unreachable recipient must still leave the breadcrumb floor: %v", err)
			}
		})
	}
}

func TestRecovery_EscalationSendFailureRecordsNotSent(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.writeStartupWithSupervisor(t)
	f.sessionLive(true)
	f.mail.err = fmt.Errorf("mail send to supervisor: exit 1")

	st := recoveryState{Attempts: 3}
	haltRecovery(f.root, f.agent, &st, haltReasonMaxAttempts, f.cfg, time.Now())

	if st.EscalationSent {
		t.Error("escalation_sent must be false when the send returned an error — this is the branch that " +
			"sendHandoffMail's isTestBinary() short-circuit makes unreachable unless the seam is overridden")
	}
	if !st.Halted {
		t.Error("halting must never be blocked on delivery")
	}
	data, err := os.ReadFile(filepath.Join(f.root, ".runtime", "recovery_halt_undelivered"))
	if err != nil || !strings.Contains(string(data), "send failed") {
		t.Errorf("breadcrumb must record the send failure, got %q err=%v", data, err)
	}
}

func TestRecovery_EscalationFiresExactlyOnce(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.writeStartupWithSupervisor(t)
	f.sessionLive(true)
	now := time.Now()

	st := recoveryState{Attempts: 3}
	haltRecovery(f.root, f.agent, &st, haltReasonMaxAttempts, f.cfg, now)
	haltRecovery(f.root, f.agent, &st, haltReasonMaxAttempts, f.cfg, now)

	if len(f.mail.sent) != 1 {
		t.Errorf("each agent halts loudly exactly once, got %d sends", len(f.mail.sent))
	}
}

func TestRecovery_EscalationRecipientLiveSessionCountsAsDelivered(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.sessionLive(true) // live session alone suffices; no startup.json needed

	st := recoveryState{Attempts: 3}
	haltRecovery(f.root, f.agent, &st, haltReasonMaxAttempts, f.cfg, time.Now())

	if !st.EscalationSent || !st.RecipientSessionLive {
		t.Errorf("a live recipient must record sent=true live=true, got sent=%v live=%v",
			st.EscalationSent, st.RecipientSessionLive)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".runtime", "recovery_halt_undelivered")); !os.IsNotExist(err) {
		t.Errorf("a delivered escalation must NOT write the undelivered breadcrumb (err=%v)", err)
	}
}

// TestRecovery_EscalationDarkChannelAtLowOccupancyNeverRecycles pins the AC-4/AC-5 pair that is
// easy to half-implement: the verdict distinguishes "dark at high occupancy" (recycle) from "dark
// at low occupancy" (tell someone), and the second must actually reach a human. Computing that
// verdict and discarding it leaves a suppressed channel silently invisible rather than merely
// un-recycled.
func TestRecovery_EscalationDarkChannelAtLowOccupancyNeverRecycles(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1") // a step-holder, so the K23 path is NOT what escalates
	f.writeStartupWithSupervisor(t)
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	// 20% written well past dark_grace: dark, but far below the threshold.
	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 20, now.Add(-2*time.Hour), now, f.cfg)
	if reading.State() != statusline.StateDark {
		t.Fatalf("fixture: want a dark reading, got %s", reading.State())
	}

	d := evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now)

	if d.verdict.fire {
		t.Error("dark at LOW occupancy must never recycle: the channel died, which is not evidence the agent is exhausted (AC-4)")
	}
	if !d.verdict.escalate {
		t.Fatal("dark at low occupancy must produce an escalate verdict")
	}
	if len(f.mail.sent) != 1 {
		t.Fatalf("the dark-channel escalation must be SENT, not just computed; got %d sends %v",
			len(f.mail.sent), mailSubjects(f.mail.sent))
	}
	if !strings.Contains(f.mail.sent[0].subject, "occupancy channel dark") {
		t.Errorf("subject = %q, want the catalogue's dark-channel shape", f.mail.sent[0].subject)
	}
	if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
		t.Errorf("no recycle may occur: recovery log has %d lines, want 0", got)
	}

	// Once per episode, not once per tick.
	evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now.Add(time.Minute))
	if len(f.mail.sent) != 1 {
		t.Errorf("the dark escalation is raised once per episode, got %d sends", len(f.mail.sent))
	}
}

// --- K23: the no-step terminal state ------------------------------------------------------------

// TestRecovery_NoStepEscalatesOnceAndNeverRecycles covers the attested class: the post-formula
// persistent consultant whose af done closed its epic. The K18 backstop's "open ready step"
// precondition never starts for it, so without K23 nothing in the design would ever fire.
func TestRecovery_NoStepEscalatesOnceAndNeverRecycles(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.writeStartupWithSupervisor(t)
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	// Low occupancy, written well past the no-step window: stale/dark, and AC-4 forbids recycling it.
	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 20, now.Add(-2*time.Hour), now, f.cfg)

	d := evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now)
	if d.verdict.fire {
		t.Error("a low-occupancy channel must NEVER be auto-recycled, however dark (AC-4)")
	}
	if len(f.mail.sent) == 0 {
		t.Fatal("the no-step class must escalate through the verified path")
	}
	var noStep *recordedMail
	for i := range f.mail.sent {
		if strings.Contains(f.mail.sent[i].subject, "no open step") {
			noStep = &f.mail.sent[i]
		}
	}
	if noStep == nil {
		t.Fatalf("want a no-open-step escalation, got subjects %v", mailSubjects(f.mail.sent))
	}
	if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
		t.Errorf("the no-step path must not recycle: recovery log has %d lines, want 0", got)
	}

	// A second tick must not re-send: this is a terminal state, not a per-tick alarm.
	before := len(f.mail.sent)
	evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now.Add(time.Minute))
	if len(f.mail.sent) != before {
		t.Errorf("the no-step escalation is raised once, got %d sends after a second tick", len(f.mail.sent))
	}
}

func TestRecovery_NoStepHealthyChannelDoesNotEscalate(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 20, now.Add(-10*time.Second), now, f.cfg)
	if !reading.IsHealthy() {
		t.Fatalf("fixture: want a fresh reading, got %s", reading.State())
	}
	evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now)

	if len(f.mail.sent) != 0 {
		t.Errorf("a healthy channel must not escalate, got %v", mailSubjects(f.mail.sent))
	}
}

// TestRecovery_NoStepNeverRenderedStillEscalates closes the gap where the escalation was gated on a
// datum's age: an agent that never rendered has no age at all, and it is exactly the
// suppressed-channel case AC-5 cares about.
func TestRecovery_NoStepNeverRenderedStillEscalates(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.writeStartupWithSupervisor(t)
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	reading := statusline.NoReading() // never rendered: no datum, therefore no Age()
	// First tick opens the quiet episode; a later tick crosses the window.
	evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now)
	evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now.Add(2*time.Hour))

	// Assert the NO-STEP escalation specifically. Counting sends alone would pass on the strength
	// of the dark-channel escalation, which also fires for a datum-less reading — a different
	// mechanism reaching a human about a different thing.
	var noStep bool
	for _, m := range f.mail.sent {
		if strings.Contains(m.subject, "no open step") {
			noStep = true
		}
	}
	if !noStep {
		t.Errorf("an agent that has NEVER rendered must still reach the no-step terminal state — gating on "+
			"the datum's age exempts the very class that has no datum; got subjects %v", mailSubjects(f.mail.sent))
	}
}

// --- K14: the advisory ---------------------------------------------------------------------------

func TestRecovery_AdvisorySentOncePerSession(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 75, now.Add(-10*time.Second), now, f.cfg)
	evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now)

	if len(f.mail.sent) != 1 {
		t.Fatalf("one advisory per session, got %d: %v", len(f.mail.sent), mailSubjects(f.mail.sent))
	}
	adv := f.mail.sent[0]
	if adv.recipient != f.agent {
		t.Errorf("the advisory goes to the AGENT (it is the one that can act while still capable), got %q", adv.recipient)
	}
	if !strings.HasPrefix(adv.subject, "CONTEXT ADVISORY") {
		t.Errorf("subject = %q, want the catalogue shape", adv.subject)
	}
	if strings.Contains(adv.body, "af done") {
		t.Error("the advisory must be class-safe: instructing af done would make a persistent consultant " +
			"close its epic on a context warning")
	}

	// Same session again: nothing more.
	evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now.Add(time.Minute))
	if len(f.mail.sent) != 1 {
		t.Errorf("re-evaluating the same session must not re-advise, got %d", len(f.mail.sent))
	}

	// A NEW session at the same occupancy is eligible again — the latch is per session, not per process.
	r2 := plantSnapshot(t, f.root, f.agent, "sess-b", 75, now.Add(time.Minute), now.Add(2*time.Minute), f.cfg)
	evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, r2, f.cfg, now.Add(2*time.Minute))
	if len(f.mail.sent) != 2 {
		t.Errorf("a new session must be advised, got %d sends", len(f.mail.sent))
	}
}

func TestRecovery_AdvisoryResetsAfterRecycle(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	st := recoveryState{AdvisorySessionID: "sess-a"}
	if err := saveRecoveryState(f.root, f.agent, st); err != nil {
		t.Fatal(err)
	}
	if err := armRecycleFence(f.root, f.agent, triggerContextExhaustion, "sess-a", now); err != nil {
		t.Fatal(err)
	}
	if got := f.state(t).AdvisorySessionID; got != "" {
		t.Errorf("advisory_session_id = %q, want cleared by the recycle — leaving it set silences the "+
			"replacement session's advisory entirely (L-2)", got)
	}
}

// TestRecovery_AdvisoryIsNeverCountedAsActivity pins L-2: mail banners repaint the pane, which is
// what the silence detector hashes. If the advisory were treated as evidence of life, sending it
// would postpone the backstop that is supposed to catch a frozen agent.
func TestRecovery_AdvisoryIsNeverCountedAsActivity(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 75, now.Add(-10*time.Second), now, f.cfg)
	st := recoveryState{
		LastProgressAt:     recoveryStamp(now.Add(-3 * time.Hour)),
		LastProgressMarker: "step-1:abcd",
		LastProgressPct:    75,
	}
	writeRuntimeMarker(t, f.agentDir, "step_primed", "step-1:abcd")

	before := st.LastProgressAt
	sendContextAdvisory(f.root, f.agent, reading, 75, &st)
	if st.LastProgressAt != before {
		t.Errorf("the advisory must not re-stamp progress: last_progress_at moved %q -> %q", before, st.LastProgressAt)
	}
	if !backstopFires(&st, reading, f.agentDir, f.cfg, now) {
		t.Error("the backstop must still trip on schedule after an advisory was sent")
	}
}

// --- K18(b): the progress backstop ---------------------------------------------------------------

func TestRecovery_BackstopFiresOnFrozenStepAndFlatOccupancy(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 40, now.Add(-10*time.Second), now, f.cfg)
	writeRuntimeMarker(t, f.agentDir, "step_primed", "step-1:abcd")
	st := recoveryState{
		LastProgressAt:     recoveryStamp(now.Add(-3 * time.Hour)),
		LastProgressMarker: "step-1:abcd",
		LastProgressPct:    40,
	}
	if !backstopFires(&st, reading, f.agentDir, f.cfg, now) {
		t.Error("a step frozen past progress_backstop_secs with flat occupancy must trip the backstop")
	}
}

func TestRecovery_BackstopDoesNotFireWhenOccupancyGrows(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 45, now.Add(-10*time.Second), now, f.cfg)
	writeRuntimeMarker(t, f.agentDir, "step_primed", "step-1:abcd")
	st := recoveryState{
		LastProgressAt:     recoveryStamp(now.Add(-3 * time.Hour)),
		LastProgressMarker: "step-1:abcd",
		LastProgressPct:    40, // grew to 45
	}
	if backstopFires(&st, reading, f.agentDir, f.cfg, now) {
		t.Error("growing occupancy is not progress, but it IS evidence the host is not frozen — and the " +
			"backstop is aimed at the frozen case")
	}
	if st.LastProgressAt != recoveryStamp(now) {
		t.Errorf("last_progress_at = %q, want re-stamped to now", st.LastProgressAt)
	}
}

// --- K21: kill-residue hygiene --------------------------------------------------------------------

func TestRecovery_HygieneClearsStaleKillResidue(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	identity := writeRuntimeMarker(t, f.agentDir, "agent.lock", "12345")
	repoDir := t.TempDir()
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	indexLock := filepath.Join(gitDir, "index.lock")
	if err := os.WriteFile(indexLock, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-time.Hour)
	for _, p := range []string{identity, indexLock} {
		if err := os.Chtimes(p, stale, stale); err != nil {
			t.Fatal(err)
		}
	}

	clearKillResidue(f.agentDir, repoDir, now)

	for _, p := range []string{identity, indexLock} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("stale residue must be removed: %s still present (err=%v)", p, err)
		}
	}
}

// TestRecovery_HygienePreservesFreshKillResidue is the fence. Removing residue unconditionally
// would delete a lock a LIVE process is holding — turning hygiene into corruption.
func TestRecovery_HygienePreservesFreshKillResidue(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	identity := writeRuntimeMarker(t, f.agentDir, "agent.lock", "12345")
	repoDir := t.TempDir()
	gitDir := filepath.Join(repoDir, ".git")
	if err := os.MkdirAll(gitDir, 0o755); err != nil {
		t.Fatal(err)
	}
	indexLock := filepath.Join(gitDir, "index.lock")
	if err := os.WriteFile(indexLock, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	fresh := now.Add(time.Hour)
	for _, p := range []string{identity, indexLock} {
		if err := os.Chtimes(p, fresh, fresh); err != nil {
			t.Fatal(err)
		}
	}

	clearKillResidue(f.agentDir, repoDir, now)

	for _, p := range []string{identity, indexLock} {
		if _, err := os.Stat(p); err != nil {
			t.Errorf("residue NEWER than the recovery must be preserved: %s gone (err=%v)", p, err)
		}
	}
}

// TestRecovery_HygieneResolvesGitDirThroughWorktreePointerFile matters because agents work in
// worktrees, where .git is a FILE naming the real git dir. Treating it as always-a-directory makes
// K21 a no-op in exactly the place it is needed.
func TestRecovery_HygieneResolvesGitDirThroughWorktreePointerFile(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	realGitDir := t.TempDir()
	repoDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoDir, ".git"), []byte("gitdir: "+realGitDir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	indexLock := filepath.Join(realGitDir, "index.lock")
	if err := os.WriteFile(indexLock, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	stale := now.Add(-time.Hour)
	if err := os.Chtimes(indexLock, stale, stale); err != nil {
		t.Fatal(err)
	}

	clearKillResidue(f.agentDir, repoDir, now)

	if _, err := os.Stat(indexLock); !os.IsNotExist(err) {
		t.Errorf("the index.lock behind a worktree .git pointer file must be removed (err=%v)", err)
	}
}

// --- helpers --------------------------------------------------------------------------------------

func writeRuntimeMarker(t *testing.T, dir, name, content string) string {
	t.Helper()
	runtimeDir := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runtimeDir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func mailSubjects(sent []recordedMail) []string {
	out := make([]string, 0, len(sent))
	for _, m := range sent {
		out = append(out, m.subject)
	}
	return out
}
