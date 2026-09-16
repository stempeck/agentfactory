//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/statusline"
)

// AC-5's detection half: a session that goes idle-at-prompt after retry exhaustion — or whose
// statusline is deliberately suppressed — must raise a human-visible alarm within DarkGraceSecs
// (10 minutes by default), NOT read as a healthy agent forever. Phase-8 blind review (Issue 5) found
// the shipped mechanism (recovery.go noteChannelHealth / channelQuietFor / escalateDarkChannel) had
// NO test, so the timing claim was unevidenced. These tests pin it. They do not run in parallel with
// the chdir-ing fixtures, and need no factory: escalateRecovery fails inert on a bare root (no
// agents.json ⇒ recipient unreachable ⇒ breadcrumb, never mail).

// TestChannelQuietFor_EpisodeClockCoversTheNoDatumCase is the crux of AC-5. A "none" reading — an
// agent whose snapshots were deleted or that never rendered — carries NO datum, so Age() cannot time
// it and every age-gated escalation would exempt it. The episode clock noteChannelHealth opens is the
// only thing that makes it actionable, and this test proves the clock exists and reaches the grace
// window exactly at DarkGraceSecs.
func TestChannelQuietFor_EpisodeClockCoversTheNoDatumCase(t *testing.T) {
	cfg := testRecoveryConfig()
	grace := time.Duration(cfg.DarkGraceSecs) * time.Second
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	none := readSessionReading(t, t.TempDir(), "worker", "sess-none", t0)
	if none.IsHealthy() {
		t.Fatal("a channel with no snapshot read as healthy; the fixture is wrong")
	}
	if _, ok := none.Age(); ok {
		t.Fatal("a no-datum reading reported an age; the episode clock would be redundant and this test moot")
	}

	// Before an episode is opened there is no basis to time the outage — which is precisely why
	// noteChannelHealth must open one, and why a reader that skipped it would let a suppressed
	// channel idle forever.
	var st recoveryState
	if _, ok := channelQuietFor(st, none, t0); ok {
		t.Fatal("channelQuietFor reported a duration with no datum and no open episode; there is no clock to trust")
	}

	noteChannelHealth(&st, none, t0)
	if st.ChannelQuietSince == "" {
		t.Fatal("noteChannelHealth did not open a quiet episode for a no-datum reading")
	}

	for _, tc := range []struct {
		at      time.Duration
		wantHit bool
	}{
		{0, false},
		{grace - time.Second, false},
		{grace, true},
		{grace + time.Minute, true},
	} {
		quiet, ok := channelQuietFor(st, none, t0.Add(tc.at))
		if !ok {
			t.Fatalf("at +%s the episode clock reported no duration", tc.at)
		}
		if hit := quiet >= grace; hit != tc.wantHit {
			t.Errorf("at +%s: escalation condition (quiet %s >= grace %s) = %v, want %v",
				tc.at, quiet, grace, hit, tc.wantHit)
		}
	}
}

// TestEscalateDarkChannel_FiresWithinDarkGraceAndLatches proves the act, not just the clock: the
// dark-channel escalation stays silent until the grace elapses, fires exactly once per episode when
// it does (within DarkGraceSecs — AC-5's "within 10 minutes"), and re-arms only when the channel
// recovers. It never recycles (AC-4), so the only observable is the DarkEscalatedAt latch.
func TestEscalateDarkChannel_FiresWithinDarkGraceAndLatches(t *testing.T) {
	cfg := testRecoveryConfig()
	grace := time.Duration(cfg.DarkGraceSecs) * time.Second
	root := t.TempDir()
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	none := readSessionReading(t, t.TempDir(), "worker", "sess-none", t0)

	var st recoveryState
	noteChannelHealth(&st, none, t0) // open the episode at t0

	// One second short of the grace: the channel is dark but not yet dark ENOUGH to alarm on.
	escalateDarkChannel(root, "worker", none, &st, cfg, t0.Add(grace-time.Second))
	if st.DarkEscalatedAt != "" {
		t.Fatalf("escalated %s before the %s grace elapsed", grace-time.Second, grace)
	}

	// At the grace boundary it must fire — this is the AC-5 within-10-minutes guarantee for the
	// idle-at-prompt / suppressed-statusline case that carries no datum of its own.
	captureStderr(t, func() {
		escalateDarkChannel(root, "worker", none, &st, cfg, t0.Add(grace))
	})
	if st.DarkEscalatedAt == "" {
		t.Fatalf("no dark-channel escalation at the %s boundary; AC-5's detection half did not fire", grace)
	}
	fired := st.DarkEscalatedAt

	// Latched: a later tick in the SAME episode must not re-alarm (one escalation per outage, not
	// one per tick).
	escalateDarkChannel(root, "worker", none, &st, cfg, t0.Add(grace+5*time.Minute))
	if st.DarkEscalatedAt != fired {
		t.Errorf("the escalation re-fired within one episode: %q -> %q", fired, st.DarkEscalatedAt)
	}

	// Recovery re-arms: a fresh healthy reading closes the episode and clears the latch, so a
	// genuinely new outage later can alarm again.
	healthy := plantSnapshot(t, t.TempDir(), "worker", "sess-live", 40, t0.Add(-10*time.Second), t0, cfg)
	if !healthy.IsHealthy() {
		t.Fatal("the fresh snapshot did not read as healthy; the re-arm assertion would be vacuous")
	}
	noteChannelHealth(&st, healthy, t0.Add(grace+10*time.Minute))
	if st.DarkEscalatedAt != "" || st.ChannelQuietSince != "" {
		t.Errorf("a recovered channel did not clear the latch: DarkEscalatedAt=%q ChannelQuietSince=%q",
			st.DarkEscalatedAt, st.ChannelQuietSince)
	}
}

// AC-2's delivery half (#673 item 2), pinned HERE because the acceptance criterion says "beside the
// existing dark-channel tests" — and because this file's fixture already produces the condition the
// criterion is about: a bare root has no agents.json, so every escalation these tests raise is
// UNDELIVERED. What follows is the proof that an undelivered escalation still reaches an operator,
// which is only true because the alarm reads the durable latch the escalators write BEFORE they mail.
//
// The file header's "need no factory" covers the two tests above it, not these: the clearing rules
// need `af recovery reset` to run against something, and the pane test below needs the gate file, so
// those provision a factory. Like their neighbours they still do not run in parallel.

// TestRecoveryAlarm_ReachesThePaneThroughTheRenderVerb is AC-2 clauses (iii) and (iv) at VERB level,
// and it is the only test in this file that proves the wiring. Every other assertion here hands a
// note to statusline.RenderWith through alarmRenderLine1, which proves the builder builds and the
// renderer prepends — and would stay green if runStatuslineRenderCore stopped passing one to the
// other, leaving every pane in the factory silent with the suite green. That is exactly the shape of
// failure this phase exists to end, so the pane gets the real verb.
//
// Clause (iv) rides along: the note is read at the same `now` the render is driven with, so what the
// latch says at render time is what the pane shows. There is no interval to wait out and no cache to
// go stale — which is the whole content of "within one statusline refresh interval".
func TestRecoveryAlarm_ReachesThePaneThroughTheRenderVerb(t *testing.T) {
	t.Setenv(noColorKey, "1") // plain text, so the assertion is about position rather than SGR bytes
	root := enabledFactory(t)
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	if err := saveRecoveryState(root, "worker",
		recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3}); err != nil {
		t.Fatalf("plant breaker: %v", err)
	}

	payload := `{"session_id":"pane","model":{"display_name":"modelX"},"cost":{"total_cost_usd":1.00}}`
	var out bytes.Buffer
	if err := runStatuslineRenderCore(&out, root, strings.NewReader(payload), "manager", now, false); err != nil {
		t.Fatalf("render must return nil, got: %v", err)
	}

	line1 := strings.SplitN(out.String(), "\n", 2)[0]
	// Non-vacuity, both halves: an empty pane would satisfy no prefix assertion at all, and a pane
	// carrying ONLY the alarm would make "leads" indistinguishable from "is the only thing there".
	if !strings.Contains(line1, "modelX") {
		t.Fatalf("line 1 does not carry the configured elements; there is nothing for the alarm to lead: %q", line1)
	}
	if !strings.HasPrefix(line1, "⚠ HALT worker") {
		t.Errorf("the halted breaker never reached the pane through the render verb; line 1 = %q", line1)
	}
}

// alarmRenderLine1 renders a statusline carrying note and returns line 1, so the note tests can
// assert the operator-facing half (AC-2 clause iii) without duplicating render plumbing five times.
func alarmRenderLine1(note string) string {
	cfg := &config.StatuslineConfig{Elements: config.DefaultStatuslineElements()}
	out := statusline.RenderWith(cfg, statusline.Payload{}, "", statusline.DailyTotals{},
		statusline.RenderOpts{Alert: note})
	return strings.SplitN(out, "\n", 2)[0]
}

// TestRecoveryAlarmNote_HaltReachesTheOperatorWithTheRecipientUnreachable is AC-2 clauses (i), (iii),
// (v) and (vi) in one test. The halt it raises cannot be delivered — there is no agents.json, so
// escalateRecovery takes its unreachable branch and writes a breadcrumb nothing reads back. The
// alarm is non-empty anyway, because it reads the latch haltRecovery persisted before it tried to
// mail. "Regardless of the escalation recipient's liveness" is therefore a property of WHERE the
// alarm reads, not a delivery guarantee anybody had to build.
func TestRecoveryAlarmNote_HaltReachesTheOperatorWithTheRecipientUnreachable(t *testing.T) {
	root := t.TempDir()
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	st := recoveryState{Attempts: 3}
	captureStderr(t, func() {
		haltRecovery(root, "worker", &st, haltReasonMaxAttempts, testRecoveryConfig(), t0)
	})

	// Non-vacuity: if this escalation had actually been delivered the test would be proving nothing
	// about clause (v).
	if st.EscalationSent {
		t.Fatal("the fixture delivered the escalation; clause (v) would be untested")
	}
	if !readBreakerState(t, root, "worker").Halted {
		t.Fatal("haltRecovery did not persist the latch; the alarm has nothing to read")
	}

	note := recoveryAlarmNote(root, t0)
	if note == "" {
		t.Fatal("a halted breaker raised no alarm: the escalation reached nobody at all")
	}
	if !strings.Contains(note, "HALT") || !strings.Contains(note, "worker") {
		t.Errorf("the alarm must name the class and the agent, got %q", note)
	}
	if n := utf8.RuneCountInString(note); n > 64 {
		t.Errorf("the alarm is %d runes; the pane cap is 64", n)
	}

	if line1 := alarmRenderLine1(note); !strings.HasPrefix(line1, note) {
		t.Errorf("the alarm does not lead line 1 of the rendered pane: %q", line1)
	}
}

// TestRecoveryAlarmNote_DarkEscalationReachesTheOperator is AC-2 clause (ii), raised through the
// same escalator the test above it pins. The class assertion is what stops a note that always says
// HALT from passing both tests.
func TestRecoveryAlarmNote_DarkEscalationReachesTheOperator(t *testing.T) {
	cfg := testRecoveryConfig()
	grace := time.Duration(cfg.DarkGraceSecs) * time.Second
	root := t.TempDir()
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	none := readSessionReading(t, t.TempDir(), "worker", "sess-none", t0)
	var st recoveryState
	noteChannelHealth(&st, none, t0)
	captureStderr(t, func() {
		escalateDarkChannel(root, "worker", none, &st, cfg, t0.Add(grace))
	})
	if st.DarkEscalatedAt == "" {
		t.Fatal("the dark escalation did not fire; the fixture is wrong")
	}
	// The escalators mutate the state in memory; only haltRecovery persists. The watchdog tick
	// saves it (recovery.go evaluateAgent), and so must this test, or the alarm reads an empty
	// directory and passes vacuously.
	if err := saveRecoveryState(root, "worker", st); err != nil {
		t.Fatalf("persist breaker: %v", err)
	}

	note := recoveryAlarmNote(root, t0.Add(grace))
	if !strings.Contains(note, "DARK") || !strings.Contains(note, "worker") {
		t.Errorf("a dark-channel escalation must reach the pane as DARK <agent>, got %q", note)
	}
	if strings.Contains(note, "HALT") {
		t.Errorf("a dark channel is not a halted breaker; the alarm conflated the classes: %q", note)
	}
	if line1 := alarmRenderLine1(note); !strings.HasPrefix(line1, note) {
		t.Errorf("the alarm does not lead line 1 of the rendered pane: %q", line1)
	}
}

// TestRecoveryAlarmNote_ClearSemanticsAreTheWritersOwn pins design-doc.md:114's three clearing
// rules. Each subtest's discriminating half is the NEGATIVE one: that the wrong act does NOT clear
// the alarm. Without it, an alarm that cleared on any state change would pass every positive case.
func TestRecoveryAlarmNote_ClearSemanticsAreTheWritersOwn(t *testing.T) {
	cfg := testRecoveryConfig()
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	t.Run("halt clears only on af recovery reset", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)
		if err := saveRecoveryState(root, "worker",
			recoveryState{Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3}); err != nil {
			t.Fatalf("plant breaker: %v", err)
		}
		if note := recoveryAlarmNote(root, t0); !strings.Contains(note, "HALT") {
			t.Fatalf("a halted breaker did not alarm: %q", note)
		}

		// A recovering channel does NOT clear a halt: noteChannelHealth never touches Halted, and
		// an agent an operator was told to investigate must not un-alarm itself by looking busy.
		healthy := plantSnapshot(t, t.TempDir(), "worker", "sess-live", 40, t0.Add(-10*time.Second), t0, cfg)
		st := readBreakerState(t, root, "worker")
		noteChannelHealth(&st, healthy, t0)
		if err := saveRecoveryState(root, "worker", st); err != nil {
			t.Fatalf("persist breaker: %v", err)
		}
		if note := recoveryAlarmNote(root, t0); !strings.Contains(note, "HALT") {
			t.Errorf("a healthy reading silenced a halted breaker; only the operator may: %q", note)
		}

		if _, _, err := invokeRecoveryReset(t, "worker"); err != nil {
			t.Fatalf("runRecoveryReset: %v", err)
		}
		if note := recoveryAlarmNote(root, t0); note != "" {
			t.Errorf("the alarm survived the operator's reset: %q", note)
		}
	})

	// Split from the DARK case below rather than planted alongside it: agentAlarm returns the
	// LEADING class, so a breaker carrying both latches is answered DARK in both directions and the
	// NOSTEP branch is never observed. One latch per subtest is what makes each class discriminating.
	t.Run("dark clears when the episode ends", func(t *testing.T) {
		root := t.TempDir()
		if err := saveRecoveryState(root, "worker", recoveryState{
			ChannelQuietSince: recoveryStamp(t0),
			DarkEscalatedAt:   recoveryStamp(t0),
		}); err != nil {
			t.Fatalf("plant breaker: %v", err)
		}
		if note := recoveryAlarmNote(root, t0); !strings.Contains(note, "DARK") {
			t.Fatalf("the dark latch did not alarm: %q", note)
		}
		if alerts := recoveryAlertsNote(root, t0); !strings.Contains(alerts, "af recovery reset worker") {
			t.Errorf("the DARK line promises a self-clear with no manual fallback: %q", alerts)
		}

		healthy := plantSnapshot(t, t.TempDir(), "worker", "sess-live", 40, t0.Add(-10*time.Second), t0, cfg)
		st := readBreakerState(t, root, "worker")
		noteChannelHealth(&st, healthy, t0.Add(time.Minute))
		if err := saveRecoveryState(root, "worker", st); err != nil {
			t.Fatalf("persist breaker: %v", err)
		}
		if note := recoveryAlarmNote(root, t0.Add(time.Minute)); note != "" {
			t.Errorf("the episode ended but the alarm held: %q", note)
		}
	})

	t.Run("nostep clears when the episode ends", func(t *testing.T) {
		root := t.TempDir()
		if err := saveRecoveryState(root, "worker", recoveryState{
			ChannelQuietSince: recoveryStamp(t0),
			NoStepEscalatedAt: recoveryStamp(t0),
		}); err != nil {
			t.Fatalf("plant breaker: %v", err)
		}
		note := recoveryAlarmNote(root, t0)
		if !strings.Contains(note, "NOSTEP") {
			t.Fatalf("a live session with no open step did not alarm: %q", note)
		}
		if strings.Contains(note, "DARK") {
			t.Errorf("a no-step escalation is not a dark channel; the alarm conflated the classes: %q", note)
		}
		// A channel latch clears itself only for an agent that is on the roster AND live. Left by an
		// agent since decommissioned it has no path back to healthy, so the loud line must offer the
		// operator's escape hatch as well as the automatic one — otherwise the only alarm they
		// cannot wait out is the only one that never tells them what to do.
		if alerts := recoveryAlertsNote(root, t0); !strings.Contains(alerts, "af recovery reset worker") {
			t.Errorf("the NOSTEP line promises a self-clear with no manual fallback: %q", alerts)
		}

		healthy := plantSnapshot(t, t.TempDir(), "worker", "sess-live", 40, t0.Add(-10*time.Second), t0, cfg)
		st := readBreakerState(t, root, "worker")
		noteChannelHealth(&st, healthy, t0.Add(time.Minute))
		if err := saveRecoveryState(root, "worker", st); err != nil {
			t.Fatalf("persist breaker: %v", err)
		}
		if note := recoveryAlarmNote(root, t0.Add(time.Minute)); note != "" {
			t.Errorf("the episode ended but the alarm held: %q", note)
		}
	})

	t.Run("wdog appears on a stale heartbeat and goes on a fresh one", func(t *testing.T) {
		root := t.TempDir()
		beat := watchdogHeartbeatPath(root)
		if err := os.MkdirAll(filepath.Dir(beat), 0o755); err != nil {
			t.Fatal(err)
		}
		writeBeat := func(at time.Time) {
			t.Helper()
			if err := os.WriteFile(beat, []byte(at.UTC().Format(time.RFC3339Nano)+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Chtimes(beat, at, at); err != nil {
				t.Fatal(err)
			}
		}

		// The boundary, not a comfortable distance from it: a single skipped poll must not alarm,
		// and the threshold must actually be the three ticks the constant claims. Any multiplier at
		// all satisfies a 10-minute-stale fixture, which is how a "3×" comment outlives a 1× const.
		for _, tc := range []struct {
			off  time.Duration
			want bool
		}{
			{watchdogTickSecs * time.Second, false},
			{watchdogHeartbeatStaleAfter - time.Second, false},
			{watchdogHeartbeatStaleAfter, true},
		} {
			writeBeat(t0.Add(-tc.off))
			note := recoveryAlarmNote(root, t0)
			if got := strings.Contains(note, "WDOG"); got != tc.want {
				t.Errorf("a heartbeat %s old: WDOG raised = %v, want %v (threshold %s); note %q",
					tc.off, got, tc.want, watchdogHeartbeatStaleAfter, note)
			}
		}

		// Days, not 2880m: formatQuietFor's coarse branch is what an operator reads on a watchdog
		// that died before the weekend.
		writeBeat(t0.Add(-50 * time.Hour))
		if note := recoveryAlarmNote(root, t0); !strings.Contains(note, "2d") {
			t.Errorf("a watchdog dead for 50 hours must read in days, got %q", note)
		}

		writeBeat(t0)
		if note := recoveryAlarmNote(root, t0); note != "" {
			t.Errorf("a live watchdog alarmed: %q", note)
		}
	})
}

// TestRecoveryAlarmNote_QuietFactoryRaisesNothingAndStaysSilent is the strobe guard. A safety alarm
// that fires on a healthy factory trains an operator to ignore it, so every quiet shape is pinned
// explicitly — including the one that is easiest to get wrong: a watchdog that has NEVER run is not
// a watchdog that DIED.
func TestRecoveryAlarmNote_QuietFactoryRaisesNothingAndStaysSilent(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	t.Run("a bare root has nothing to say", func(t *testing.T) {
		root := t.TempDir()
		if note := recoveryAlarmNote(root, t0); note != "" {
			t.Errorf("a factory with no .runtime at all alarmed: %q", note)
		}
	})

	t.Run("a watchdog that never ran is not a dead watchdog", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(recoveryStateDir(root), 0o755); err != nil {
			t.Fatal(err)
		}
		if note := recoveryAlarmNote(root, t0); note != "" {
			t.Errorf("an absent heartbeat read as a stale one: %q", note)
		}
	})

	t.Run("an unraised breaker is not an alarm", func(t *testing.T) {
		root := t.TempDir()
		if err := saveRecoveryState(root, "worker", recoveryState{Attempts: 1, LastTrigger: "occupancy"}); err != nil {
			t.Fatal(err)
		}
		if note := recoveryAlarmNote(root, t0); note != "" {
			t.Errorf("an agent that merely recovered once alarmed: %q", note)
		}
	})

	t.Run("the read never writes to stderr", func(t *testing.T) {
		root := t.TempDir()
		if err := saveRecoveryState(root, "worker", recoveryState{Halted: true, HaltReason: haltReasonRateCap}); err != nil {
			t.Fatal(err)
		}
		// The render path returns nil on every leg and emits nothing to stderr (statusline.go's
		// containment contract, pinned by TestRender_ExitZeroAlways). A diagnostic printed here
		// would land in the operator's pane, not a log.
		if errOut := captureStderr(t, func() { _ = recoveryAlarmNote(root, t0) }); errOut != "" {
			t.Errorf("the alarm read wrote to stderr: %q", errOut)
		}
	})

	t.Run("an unreadable recovery dir silences the pane", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root ignores the permission bits this case depends on")
		}
		root := t.TempDir()
		dir := recoveryStateDir(root)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(dir, 0o755) })

		var note string
		if errOut := captureStderr(t, func() { note = recoveryAlarmNote(root, t0) }); errOut != "" {
			t.Errorf("an unreadable dir wrote to stderr: %q", errOut)
		}
		if note != "" {
			t.Errorf("an unreadable dir produced pane text: %q", note)
		}

		// The pane's silence here is an accepted residual ONLY because a loud surface separates
		// "nothing is wrong" from "I cannot tell". Without this half, an unreadable breaker
		// directory is the same failure the phase exists to end: every agent invisible, nobody told.
		alerts := recoveryAlertsNote(root, t0)
		if !strings.Contains(alerts, "UNREADABLE") {
			t.Errorf("`af statusline status` did not name the broken scan the pane stayed silent about: %q", alerts)
		}
	})
}

// plantBreakerBytes writes raw bytes as one agent's breaker, bypassing saveRecoveryState. The
// closed-vocabulary tests are ABOUT bytes the schema would never produce, so they cannot go through
// the writer that produces the schema.
func plantBreakerBytes(t *testing.T, root, name, body string) {
	t.Helper()
	dir := recoveryStateDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// plantHostileBreaker plants a breaker that is VALID JSON carrying attacker-chosen bytes in
// halt_reason. json.Marshal is what makes it valid: it encodes the control bytes as legal \u
// escapes, so the document decodes cleanly and the fixture lands on the free-text path. Writing the
// bytes raw would make the document undecodable and route the fixture to the corrupt branch
// instead, silently testing a different defence than the one named.
func plantHostileBreaker(t *testing.T, root, agent, reason string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"v": 1, "halted": true, "halt_reason": reason})
	if err != nil {
		t.Fatal(err)
	}
	plantBreakerBytes(t, root, agent+".json", string(body))
	if got := readBreakerState(t, root, agent).HaltReason; got != reason {
		t.Fatalf("the fixture did not decode as free text (halt_reason=%q, want %q); the corrupt branch would be under test instead", got, reason)
	}
}

// TestRecoveryAlarmNote_CollapsesAndSpeaksOnlyClosedLabels is the closed-label half of the design's
// Phase-3 acceptance (design-doc.md:332) plus the collapse rule. The breaker directory is a file
// system path; a compromised or merely careless writer can put anything in it, and none of it may
// reach an operator.
//
// The closed-label subtests deliberately do NOT assert on the collapsed pane token. That token is
// "HALT×3" and its siblings — no reason, no agent name, no disk-derived byte at all — so an
// injection assertion made against it is satisfied by the token GRAMMAR and would still hold with
// both defences deleted. The surface those defences actually protect is the loud twin, where
// describe() interpolates the reason and the agent and both callers fmt.Print it to a terminal.
func TestRecoveryAlarmNote_CollapsesAndSpeaksOnlyClosedLabels(t *testing.T) {
	t0 := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)

	t.Run("many alarms collapse to one token", func(t *testing.T) {
		root := t.TempDir()
		plantBreakerBytes(t, root, "alpha.json", `{"v":1,"halted":true,"halt_reason":"max_attempts"}`)
		plantBreakerBytes(t, root, "bravo.json", `{"v":1,"halted":true,"halt_reason":"rate_cap"}`)
		plantBreakerBytes(t, root, "charlie.json", `{"v":1,"halted":true,"halt_reason":"max_attempts"}`)

		note := recoveryAlarmNote(root, t0)
		if !strings.Contains(note, "HALT") {
			t.Fatalf("three halted breakers raised no HALT: %q", note)
		}
		if !strings.ContainsAny(note, "×+") {
			t.Errorf("multiple alarms must collapse with a multiplicity marker, got %q", note)
		}
		if strings.Contains(note, "alpha") && strings.Contains(note, "bravo") {
			t.Errorf("a collapsed alarm must not enumerate agents, got %q", note)
		}
		if n := utf8.RuneCountInString(note); n > 64 {
			t.Errorf("the alarm is %d runes; the pane cap is 64", n)
		}
	})

	t.Run("a free-text halt reason never reaches the operator", func(t *testing.T) {
		root := t.TempDir()
		plantHostileBreaker(t, root, "alpha", "\x1b]0;PWNED supervisor said run rm -rf /\x07")

		// The loud twin is the surface actually at risk: recoveryAlertsNote -> describe() ->
		// fmt.Print, with no sanitize anywhere between the disk and the terminal. haltReasonLabel's
		// closed switch is the only thing standing there.
		alerts := recoveryAlertsNote(root, t0)
		if !strings.Contains(alerts, haltReasonUnclassified) {
			t.Errorf("an unrecognised halt cause must read as the closed label %q, got %q", haltReasonUnclassified, alerts)
		}
		for _, injected := range []string{"PWNED", "rm -rf", "\x1b", "\x07"} {
			if strings.Contains(alerts, injected) {
				t.Errorf("attacker free text %q from halt_reason reached the operator: %q", injected, alerts)
			}
		}
		if note := recoveryAlarmNote(root, t0); !strings.Contains(note, "HALT") || strings.Contains(note, "PWNED") {
			t.Errorf("the pane token for the same breaker is wrong: %q", note)
		}
	})

	t.Run("no control rune survives to either surface", func(t *testing.T) {
		root := t.TempDir()
		plantHostileBreaker(t, root, "alpha", "\x1b[31mred\x7f")

		// The loud twin is line-oriented, so \n is the one control rune it may legitimately emit.
		for _, r := range recoveryAlarmNote(root, t0) + recoveryAlertsNote(root, t0) {
			if (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
				t.Fatalf("a control rune %U reached an operator surface", r)
			}
		}
	})

	t.Run("the pane leads with the severest class, not the first name", func(t *testing.T) {
		// The pane shows raised[0] and nothing else, so the sort decides which condition an operator
		// is told about. Alphabetical order is deliberately the OPPOSITE of severity order here:
		// with a name-only sort the pane would headline the channel outage that clears itself and
		// demote the halted breaker only a human can clear to an anonymous "+1".
		root := t.TempDir()
		plantBreakerBytes(t, root, "alpha.json", `{"v":1,"halted":false,"dark_escalated_at":"2026-09-01T12:00:00Z"}`)
		plantBreakerBytes(t, root, "bravo.json", `{"v":1,"halted":true,"halt_reason":"max_attempts"}`)

		if got := readBreakerState(t, root, "alpha"); got.DarkEscalatedAt == "" || got.Halted {
			t.Fatalf("the DARK fixture did not decode as dark-and-unhalted: %+v", got)
		}

		note := recoveryAlarmNote(root, t0)
		if !strings.HasPrefix(note, "⚠ HALT bravo") {
			t.Errorf("the pane demoted a halted breaker below a self-clearing dark channel: %q", note)
		}
		if !strings.Contains(note, "+1") {
			t.Errorf("the other alarm vanished instead of being counted: %q", note)
		}
	})

	t.Run("the cmd layer caps the token itself", func(t *testing.T) {
		// The renderer sanitizes again, so this cap is belt-and-braces on the pane path — but
		// recoveryAlarmNote's result also reaches callers that never render (and its own doc promises
		// the cap), so the promise has to hold at the builder, not only downstream of it.
		root := t.TempDir()
		long := "a" + strings.Repeat("b", 63) // 64 runes, still a valid agent name
		if err := config.ValidateAgentName(long); err != nil {
			t.Fatalf("the fixture name is not a valid agent name: %v", err)
		}
		plantBreakerBytes(t, root, long+".json", `{"v":1,"halted":true,"halt_reason":"rate_cap"}`)

		note := recoveryAlarmNote(root, t0)
		if note == "" {
			t.Fatal("the long-named breaker raised nothing; the cap assertion would be vacuous")
		}
		if n := utf8.RuneCountInString(note); n > 64 {
			t.Errorf("recoveryAlarmNote returned %d runes uncapped: %q", n, note)
		}
	})

	t.Run("a filename that is not an agent name is not an agent", func(t *testing.T) {
		root := t.TempDir()
		// Alone, so nothing collapses: a bad name among several breakers is hidden inside the
		// multiplicity token whether or not the filter exists, and would prove nothing.
		plantBreakerBytes(t, root, "9bad name.json", `{"v":1,"halted":true,"halt_reason":"rate_cap"}`)

		if note := recoveryAlarmNote(root, t0); note != "" {
			t.Errorf("a file whose name is not a roster-shaped agent name reached the pane: %q", note)
		}
		if alerts := recoveryAlertsNote(root, t0); alerts != "" {
			t.Errorf("the same file reached the loud twin: %q", alerts)
		}
	})

	t.Run("an undecodable breaker alarms as a HALT for that agent", func(t *testing.T) {
		// The undecodable file is not skipped: loadRecoveryState reads an undecodable breaker as
		// HALTED, and that fail-closed posture is what `af agents list --json` already publishes.
		// The alarm must agree with it rather than invent a third answer.
		root := t.TempDir()
		plantBreakerBytes(t, root, "torn.json", `{"v":1,"halted":`)

		if note := recoveryAlarmNote(root, t0); !strings.Contains(note, "HALT") || !strings.Contains(note, "torn") {
			t.Errorf("an unreadable breaker must alarm as a HALT for that agent, got %q", note)
		}
		if alerts := recoveryAlertsNote(root, t0); !strings.Contains(alerts, haltReasonCorrupt) {
			t.Errorf("the loud twin must name the documented cause %q, got %q", haltReasonCorrupt, alerts)
		}
	})

	t.Run("a breaker that is not a regular file is never opened", func(t *testing.T) {
		// The size check runs on DirEntry.Info(), which is an LSTAT: a symlink reports the LINK's
		// size, so the cap alone waves this through and loadRecoveryState reads the target — on the
		// pane's render tick, for every render, unbounded. /dev/zero makes that a hang rather than a
		// slow read, which is why the mode check has to come first.
		root := t.TempDir()
		dir := recoveryStateDir(root)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		big := filepath.Join(t.TempDir(), "big")
		if err := os.WriteFile(big, []byte(`{"v":1,"halted":false,"pad":"`+strings.Repeat("y", maxBreakerBytes)+`"}`), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(big, filepath.Join(dir, "sneaky.json")); err != nil {
			t.Skipf("symlinks unavailable here: %v", err)
		}

		// Non-vacuity: if lstat already reported the target's size the cap would fire on its own and
		// this test would credit the mode check for something the size check did.
		info, err := os.Lstat(filepath.Join(dir, "sneaky.json"))
		if err != nil {
			t.Fatal(err)
		}
		if info.Size() > maxBreakerBytes {
			t.Fatalf("lstat reported the target's size (%d); the size cap alone would catch this", info.Size())
		}

		if note := recoveryAlarmNote(root, t0); !strings.Contains(note, "HALT") || !strings.Contains(note, "sneaky") {
			t.Errorf("a symlinked breaker must fail closed to a HALT without being read, got %q", note)
		}
	})

	t.Run("a breaker too large to be this schema reads as unreadable", func(t *testing.T) {
		// The size cap short-circuits before the read, so this is the one alarm whose class is
		// decided without decoding anything. Nothing in the schema writes a file this size; the
		// branch exists to keep the render tick's read bounded, and it must still fail CLOSED —
		// the planted payload says halted:false and the alarm must not believe it.
		root := t.TempDir()
		plantBreakerBytes(t, root, "whale.json",
			`{"v":1,"halted":false,"pad":"`+strings.Repeat("x", maxBreakerBytes)+`"}`)

		if note := recoveryAlarmNote(root, t0); !strings.Contains(note, "HALT") || !strings.Contains(note, "whale") {
			t.Errorf("an oversized breaker must fail closed to a HALT, got %q", note)
		}
	})
}
