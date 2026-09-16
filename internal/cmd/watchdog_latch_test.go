package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/statusline"
)

// latchFixture is one agent held at an occupancy that WOULD recycle it: a step-holder whose
// channel reads fresh and high. Every subtest below differs only in what the breaker says about
// an intervention latch, so the recycle has to be genuinely armed or the assertions prove nothing.
func latchFixture(t *testing.T, now time.Time) (*recoveryFixture, statusline.ChannelReading) {
	t.Helper()
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	f.writeStartupWithSupervisor(t)
	f.sessionLive(true)
	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 91, now.Add(-10*time.Second), now, f.cfg)
	if !reading.IsHealthy() {
		t.Fatalf("fixture: want a healthy reading, got %s", reading.State())
	}
	return f, reading
}

// tickFour drives four watchdog ticks and answers with the decision that fired, or the last one if
// none did.
//
// Four rather than the two confirm_ticks needs: every latch subtest asserts that NOTHING fires over
// the window, and a window only as long as the debounce cannot tell "suppressed" from "not yet
// confirmed". The first firing decision is the answer because a successful recycle arms the fence,
// so the ticks after it are blocked for a reason that has nothing to do with the latch.
func tickFour(f *recoveryFixture, reading statusline.ChannelReading, now time.Time) recoveryDecision {
	entry := config.AgentEntry{Type: "autonomous"}
	var last, fired recoveryDecision
	for i := 0; i < 4; i++ {
		last = evaluateAgent(f.root, f.agent, entry, reading, f.cfg, now)
		if last.verdict.fire && !fired.verdict.fire {
			fired = last
		}
	}
	if fired.verdict.fire {
		return fired
	}
	return last
}

// TestWatchdogLatch covers #668 K17: a mechanism that has told an agent to wait must not have that
// wait read as a stall. Without the latch the two halves of the factory fight — one telling the
// agent to hold, the other recycling it for holding — and the agent walks its attempts up to
// RECOVERY HALTED for doing what it was told (design-doc.md:220 grades this High).
func TestWatchdogLatch(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	t.Run("an unlatched agent at the same occupancy is recycled", func(t *testing.T) {
		// The non-vacuity control, and it comes first on purpose: every assertion below is about
		// something NOT happening, and they are all worthless if this setup never fires.
		f, reading := latchFixture(t, now)

		d := tickFour(f, reading, now)

		if !d.verdict.fire {
			t.Fatalf("fixture does not fire without a latch: %s", d.verdict.reason)
		}
		if !d.executed {
			t.Fatal("fixture fired but did not execute; the latch subtests would prove nothing")
		}
		if got := f.state(t).Attempts; got != 1 {
			t.Errorf("attempts = %d, want 1", got)
		}
	})

	t.Run("a live latch stops the recycle and burns no attempt", func(t *testing.T) {
		f, reading := latchFixture(t, now)
		if !armInterventionLatch(f.root, f.agent, "admission refused at step open",
			15*time.Minute, now) {
			t.Fatal("the latch refused to arm on a clean breaker")
		}

		d := tickFour(f, reading, now)

		if d.verdict.fire || d.executed {
			t.Errorf("a latched agent was recycled: fire=%v executed=%v reason=%q",
				d.verdict.fire, d.executed, d.verdict.reason)
		}
		if !d.latched {
			t.Error("the decision does not report the latch; an operator reading it would see an unexplained non-recycle")
		}
		if got := f.state(t).Attempts; got != 0 {
			t.Errorf("attempts = %d, want 0 — a wait the factory ordered must not count toward RECOVERY HALTED", got)
		}
		if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
			t.Errorf("recovery log has %d lines, want 0", got)
		}
		if f.state(t).Halted {
			t.Error("a latched agent was halted; a wait the factory ordered must not reach the breaker at all")
		}
	})

	t.Run("an expired latch protects nothing", func(t *testing.T) {
		f, reading := latchFixture(t, now)
		if !armInterventionLatch(f.root, f.agent, "admission refused", -time.Second, now) {
			t.Fatal("the latch refused to arm")
		}

		d := tickFour(f, reading, now)

		if !d.verdict.fire {
			t.Errorf("an expired latch suppressed the recycle: %s", d.verdict.reason)
		}
	})

	t.Run("a malformed expiry protects nothing", func(t *testing.T) {
		// Fail-CLOSED, the opposite grain from K7's admission: an unreadable deadline that read as
		// "still waiting" would disable recovery for that agent permanently, and it is exactly the
		// state a truncated write leaves behind.
		f, reading := latchFixture(t, now)
		st := loadRecoveryState(f.root, f.agent)
		st.InterventionLatchUntil = "whenever"
		st.InterventionLatchReason = "admission refused"
		if err := saveRecoveryState(f.root, f.agent, st); err != nil {
			t.Fatal(err)
		}

		d := tickFour(f, reading, now)

		if !d.verdict.fire {
			t.Errorf("a latch with an undecodable deadline suppressed the recycle: %s", d.verdict.reason)
		}
	})

	t.Run("arming is once per episode", func(t *testing.T) {
		// The caller writes one intervention record per episode and keys that on this answer, so a
		// refresh reporting itself as a new arming would multiply one wait into many records.
		f, _ := latchFixture(t, now)
		if !armInterventionLatch(f.root, f.agent, "first", 15*time.Minute, now) {
			t.Fatal("first arm reported no arming")
		}
		if armInterventionLatch(f.root, f.agent, "second", 20*time.Minute, now) {
			t.Error("re-arming a live latch reported a new episode")
		}
		if got := loadRecoveryState(f.root, f.agent).InterventionLatchReason; got != "first" {
			t.Errorf("reason = %q, want the episode's original reason", got)
		}
	})

	t.Run("the latch lives at the factory root, never in the worktree", func(t *testing.T) {
		// L-1: a worktree-resident latch evaporates with the worktree, and the agent it was
		// protecting comes back visible to the watchdog with no record that a wait was ever ordered.
		f, _ := latchFixture(t, now)
		if !armInterventionLatch(f.root, f.agent, "admission refused", 15*time.Minute, now) {
			t.Fatal("the latch refused to arm")
		}

		want := filepath.Join(f.root, ".runtime", "recovery", f.agent+".json")
		if got := recoveryStatePath(f.root, f.agent); got != want {
			t.Errorf("latch path = %q, want %q", got, want)
		}
		if _, err := os.Stat(want); err != nil {
			t.Errorf("nothing was written at the factory root: %v", err)
		}
		data, err := os.ReadFile(want)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "intervention_latch_until") {
			t.Errorf("the factory-root breaker does not carry the latch: %s", data)
		}
	})

	t.Run("a corrupt breaker refuses the latch and keeps the evidence", func(t *testing.T) {
		f, _ := latchFixture(t, now)
		path := recoveryStatePath(f.root, f.agent)
		if err := os.MkdirAll(recoveryStateDir(f.root), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("{ not json"), 0o644); err != nil {
			t.Fatal(err)
		}

		var armed bool
		stderr := captureStderr(t, func() {
			armed = armInterventionLatch(f.root, f.agent, "admission refused", 15*time.Minute, now)
		})

		if armed {
			t.Error("the latch armed over a breaker that could not be decoded")
		}
		// A corrupt breaker and a live latch both answer false, and only one is a refusal. Silence
		// here would leave a mechanism that asked for a wait, did not get one, and said nothing —
		// the state Phase 5's first armers would inherit.
		if !strings.Contains(stderr, "intervention latch refused") {
			t.Errorf("the refusal was swallowed; stderr = %q", stderr)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != "{ not json" {
			t.Errorf("the corrupt breaker was overwritten: %q", data)
		}
	})
}

// TestRespawnAttribution covers #668 H-R3: a session that was replaced without the factory doing
// it left no funnel entry at all, so the recycle counts simply did not include it. The class is
// new; the funnel that records it is the existing one.
func TestRespawnAttribution(t *testing.T) {
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)
	entry := config.AgentEntry{Type: "autonomous"}

	// replaceSession retires the current snapshot and plants the successor's, which is what a
	// session replacement looks like from the channel's side.
	replaceSession := func(t *testing.T, f *recoveryFixture, oldID, newID string, pct float64,
		writtenAt, at time.Time) statusline.ChannelReading {
		t.Helper()
		if err := os.Remove(config.StatuslineSessionsDir(f.root) + "/" + oldID + ".json"); err != nil {
			t.Fatal(err)
		}
		return plantSnapshot(t, f.root, f.agent, newID, pct, writtenAt, at, f.cfg)
	}

	t.Run("a replacement nobody recorded is logged as unattributed", func(t *testing.T) {
		f := newRecoveryFixture(t, "supervisor")
		f.stateStep(t, "step-1")
		reading := plantSnapshot(t, f.root, f.agent, "sess-a", 20, now.Add(-10*time.Second), now, f.cfg)
		evaluateAgent(f.root, f.agent, entry, reading, f.cfg, now)
		if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
			t.Fatalf("the first sighting of a session is not a replacement; log has %d lines", got)
		}

		later := now.Add(time.Minute)
		next := replaceSession(t, f, "sess-a", "sess-b", 20, later.Add(-10*time.Second), later)
		evaluateAgent(f.root, f.agent, entry, next, f.cfg, later)

		lines := readRecoveryLogLines(t, f.root)
		if len(lines) != 1 {
			t.Fatalf("an unattributed replacement must append exactly one funnel line, got %d", len(lines))
		}
		if lines[0].Trigger != triggerUnattributedRespawn {
			t.Errorf("trigger = %q, want %q", lines[0].Trigger, triggerUnattributedRespawn)
		}
		if lines[0].SessionID != "sess-a" {
			t.Errorf("session_id = %q, want the session that was REPLACED", lines[0].SessionID)
		}

		// Once per replacement, not once per tick.
		evaluateAgent(f.root, f.agent, entry, next, f.cfg, later.Add(time.Minute))
		if got := len(readRecoveryLogLines(t, f.root)); got != 1 {
			t.Errorf("log has %d lines after a second tick on the same session, want 1", got)
		}

		// The observer does not arm the recycle fence. It replaced no pane, so there is no
		// replacement of its own to protect — and a fence armed here would be written underneath
		// evaluateAgent's in-memory breaker and lost on the way out, which is a mechanism whose only
		// defence is that its write is discarded.
		st := loadRecoveryState(f.root, f.agent)
		if st.LastRecoveryAt != "" || st.LastRecoverySessionID != "" {
			t.Errorf("the observation path armed the recycle fence (at=%q session=%q)",
				st.LastRecoveryAt, st.LastRecoverySessionID)
		}
		if st.LastSeenSessionID != "sess-b" {
			t.Errorf("last_seen_session_id = %q, want %q — the caller's copy is what persists",
				st.LastSeenSessionID, "sess-b")
		}
	})

	t.Run("a replacement the factory made is not logged twice", func(t *testing.T) {
		f := newRecoveryFixture(t, "supervisor")
		f.stateStep(t, "step-1")
		reading := plantSnapshot(t, f.root, f.agent, "sess-a", 20, now.Add(-10*time.Second), now, f.cfg)
		evaluateAgent(f.root, f.agent, entry, reading, f.cfg, now)

		// What the funnel leaves behind when af itself recycled sess-a.
		st := loadRecoveryState(f.root, f.agent)
		st.LastRecoverySessionID = "sess-a"
		st.LastTrigger = triggerContextExhaustion
		if err := saveRecoveryState(f.root, f.agent, st); err != nil {
			t.Fatal(err)
		}

		later := now.Add(time.Minute)
		next := replaceSession(t, f, "sess-a", "sess-b", 20, later.Add(-10*time.Second), later)
		evaluateAgent(f.root, f.agent, entry, next, f.cfg, later)

		if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
			t.Errorf("the factory's own recycle was re-logged as unattributed: %d lines", got)
		}
	})

	t.Run("a replacement out of a quiet channel names the backend class", func(t *testing.T) {
		f := newRecoveryFixture(t, "supervisor")
		f.stateStep(t, "step-1")
		// Written well past dark_grace: the channel had already gone quiet when the session was
		// replaced, which is the stall signature H-R3 measured.
		reading := plantSnapshot(t, f.root, f.agent, "sess-a", 20, now.Add(-2*time.Hour), now, f.cfg)
		if reading.State() != statusline.StateDark {
			t.Fatalf("fixture: want a dark reading, got %s", reading.State())
		}
		evaluateAgent(f.root, f.agent, entry, reading, f.cfg, now)
		if loadRecoveryState(f.root, f.agent).ChannelQuietSince == "" {
			t.Fatal("fixture: the quiet episode was not recorded, so there is no stall evidence to read")
		}

		later := now.Add(time.Minute)
		next := replaceSession(t, f, "sess-a", "sess-b", 20, later.Add(-10*time.Second), later)
		evaluateAgent(f.root, f.agent, entry, next, f.cfg, later)

		lines := readRecoveryLogLines(t, f.root)
		if len(lines) != 1 {
			t.Fatalf("want one funnel line, got %d", len(lines))
		}
		if lines[0].Trigger != triggerBackendStallRespawn {
			t.Errorf("trigger = %q, want %q", lines[0].Trigger, triggerBackendStallRespawn)
		}
	})

	t.Run("neither class counts against the absolute rate cap", func(t *testing.T) {
		// Both classes describe something the factory did NOT initiate. Counting them would walk an
		// agent toward RECOVERY HALTED — an operator action — for the backend's behaviour.
		for _, trigger := range []string{triggerUnattributedRespawn, triggerBackendStallRespawn} {
			if isRateCappedTrigger(trigger) {
				t.Errorf("%s counts against the rate cap", trigger)
			}
		}
	})

	t.Run("the funnel records both classes verbatim and arms nothing", func(t *testing.T) {
		// Driven through appendRecycleRecord, which is the entry point the observation path takes —
		// testing recordRecycleAt here would prove the closed switch admits the class while exercising
		// a fence arm no caller of these two classes performs.
		for _, trigger := range []string{triggerUnattributedRespawn, triggerBackendStallRespawn} {
			t.Run(trigger, func(t *testing.T) {
				root := t.TempDir()
				appendRecycleRecord(RespawnOptions{
					FactoryRoot: root,
					AgentName:   "worker",
					Trigger:     trigger,
				}, nil, now)

				lines := readRecoveryLogLines(t, root)
				if len(lines) != 1 {
					t.Fatalf("want one line, got %d", len(lines))
				}
				if lines[0].Trigger != trigger {
					t.Errorf("trigger = %q, want %q — a class the closed switch does not admit is "+
						"rewritten to %q and the literal is inert", lines[0].Trigger, trigger, triggerUnknown)
				}
				if st := loadRecoveryState(root, "worker"); st.LastRecoveryAt != "" {
					t.Errorf("appending a funnel line armed the fence (at=%q); the split exists so an "+
						"observer can log without claiming a recycle", st.LastRecoveryAt)
				}
			})
		}
	})
}

// TestRecoveryStateDecodesWithoutLatch pins the additive half of the K17 fields: a breaker written
// by a binary that never heard of the latch must still decode, and must not read as latched.
func TestRecoveryStateDecodesWithoutLatch(t *testing.T) {
	root := t.TempDir()
	dir := recoveryStateDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]any{"v": recoveryStateVersion, "attempts": 2, "halted": false}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recoveryStatePath(root, "worker"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	st := loadRecoveryState(root, "worker")
	if st.Halted {
		t.Fatal("a legacy breaker decoded as corrupt")
	}
	if st.Attempts != 2 {
		t.Errorf("attempts = %d, want 2", st.Attempts)
	}
	if interventionLatchHolds(st, time.Now()) {
		t.Error("a breaker with no latch fields read as latched")
	}
}
