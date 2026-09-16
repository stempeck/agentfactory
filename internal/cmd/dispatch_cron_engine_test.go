package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// ============================================================================
// Issue #610 Phase 3 — cron engine acceptance tests. All drive processCrons (or
// its pure leaf helpers) in isolation: *config.DispatchConfig values are built
// directly rather than loaded, the clock is pinned through the cronNow seam, and
// side effects are asserted through the cronSling seam and the on-disk
// .runtime/dispatch-crons.json. No live gh, tmux, or sling. runDispatch itself is
// driven only by the two gh-ordering cases, which cannot be written any other way:
// checkGHAuth is seamed there, and on a repo-less factory the unseamed `gh issue
// list` is never reached because the repo loop has zero iterations.
// None of these tests may call t.Parallel (package globals).
// ============================================================================

// fixCronClock pins the cronNow seam for one test and returns a setter so a subtest can move the
// clock instead of sleeping — the only way a 14d cadence is reachable from a unit test.
func fixCronClock(t *testing.T, at time.Time) func(time.Time) {
	t.Helper()
	cur := at
	orig := cronNow
	cronNow = func() time.Time { return cur }
	t.Cleanup(func() { cronNow = orig })
	return func(next time.Time) { cur = next }
}

// recordCronSlings swaps the fire seam to a recorder that captures the full argv (defensively
// copied) and returns fireErr, so the error path is reachable without a failing subprocess.
func recordCronSlings(t *testing.T, argvs *[][]string, fireErr error) {
	t.Helper()
	orig := cronSling
	cronSling = func(root string, argv []string) (string, error) {
		*argvs = append(*argvs, append([]string(nil), argv...))
		return "", fireErr
	}
	t.Cleanup(func() { cronSling = orig })
}

// cronTestCfg builds a dispatch config WITHOUT the loader, so the engine paths are exercised on
// exactly the fields under test (the crossSourceCfg trick, dispatch_phase3_test.go:110).
// IntervalSecs is set explicitly because bypassing LoadDispatchConfig also bypasses its defaulting.
func cronTestCfg(crons ...config.CronSchedule) *config.DispatchConfig {
	return &config.DispatchConfig{
		IntervalSecs:     300,
		NotifyOnComplete: "manager",
		Crons:            crons,
	}
}

// TestDispatchCron_FireArgv pins the fire argv contract (issue #610 AC-2, N9). Three properties are
// load-bearing beyond "does it build a slice": the --var keys are SORTED (Go randomizes map
// iteration, so an unsorted implementation would produce a different command every tick and be
// untestable), --bare replaces the item path's positional itemURL entirely, and --reset is
// unconditional because the succession gate (sling.go:531-537) hard-errors on a stale prior
// instance.
func TestDispatchCron_FireArgv(t *testing.T) {
	// Reverse-declared so a range-order implementation fails rather than passing by luck.
	cron := config.CronSchedule{
		Name:  "nightly",
		Agent: "X",
		Every: "4h",
		Vars:  map[string]string{"b": "2", "a": "1"},
	}
	want := []string{"sling", "--agent", "X", "--reset", "--bare",
		"--caller", "manager", "--var", "a=1", "--var", "b=2"}

	// Repeated because one pass over a two-key map can agree by coin flip.
	for i := 0; i < 32; i++ {
		got := buildCronSlingArgs(cron, "manager")
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("buildCronSlingArgs = %v, want %v", got, want)
		}
	}

	t.Run("a bare-shape cron emits no --var and no positional task", func(t *testing.T) {
		bare := buildCronSlingArgs(config.CronSchedule{Name: "wake", Agent: "Y", Every: "4h"}, "")
		for _, a := range bare {
			if a == "--var" {
				t.Errorf("a cron with no vars must emit no --var at all; got %v", bare)
			}
			if a == "--caller" {
				t.Errorf("an empty notify_on_complete must omit --caller, not pass an empty one; got %v", bare)
			}
			if a == "--model" {
				t.Errorf("a cron with no model must omit --model; got %v", bare)
			}
		}
		if !reflect.DeepEqual(bare, []string{"sling", "--agent", "Y", "--reset", "--bare"}) {
			t.Errorf("bare fire argv = %v, want exactly sling --agent Y --reset --bare", bare)
		}
	})

	t.Run("--reset and --bare are unconditional on every fire", func(t *testing.T) {
		for _, c := range []config.CronSchedule{
			{Name: "a", Agent: "Y", Every: "1h"},
			{Name: "b", Agent: "Y", Every: "1h", Vars: map[string]string{"k": "v"}},
			{Name: "c", Agent: "Y", Every: "1h", Model: "opus"},
		} {
			got := buildCronSlingArgs(c, "manager")
			if !containsAdjacent(got, "--agent", "Y") {
				t.Errorf("%s: argv must name the agent; got %v", c.Name, got)
			}
			var sawReset, sawBare bool
			for _, a := range got {
				sawReset = sawReset || a == "--reset"
				sawBare = sawBare || a == "--bare"
			}
			if !sawReset {
				t.Errorf("%s: --reset is unconditional (succession gate); got %v", c.Name, got)
			}
			if !sawBare {
				t.Errorf("%s: --bare is unconditional (a schedule has no task); got %v", c.Name, got)
			}
		}
	})

	t.Run("a per-cron model is emitted adjacent to its flag", func(t *testing.T) {
		got := buildCronSlingArgs(config.CronSchedule{Name: "n", Agent: "Y", Every: "1h", Model: "opus"}, "")
		if !containsAdjacent(got, "--model", "opus") {
			t.Errorf("argv must contain --model immediately followed by opus; got %v", got)
		}
	})

	t.Run("a var value containing = survives verbatim", func(t *testing.T) {
		// parseCLIVars splits on the FIRST = (sling.go:689-699), so the producer must not escape.
		got := buildCronSlingArgs(config.CronSchedule{
			Name: "n", Agent: "Y", Every: "1h", Vars: map[string]string{"q": "a=b"},
		}, "")
		if !containsAdjacent(got, "--var", "q=a=b") {
			t.Errorf("argv must carry q=a=b verbatim; got %v", got)
		}
	})
}

// TestDispatchCron_OverlapSkip pins the scheduling decision (issue #610 AC-3, N10) against a fake
// clock. The load-bearing asymmetry is that only a SUCCESSFUL fire advances LastFiredAt
// (cross-review HIGH-4): a busy skip and a sling error both leave the schedule due, so a transient
// fault never costs a whole interval — which on a 14d cadence would be a fortnight of silence.
func TestDispatchCron_OverlapSkip(t *testing.T) {
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	cron := config.CronSchedule{Name: "patrol", Agent: "af610-patrol", Every: "4h"}

	// A real factory, not a bare temp dir: the fire-time backstop re-reads agents.json and the
	// formula from disk on every fire, so a schedule can only reach the sling on a tree where its
	// agent and formula actually resolve.
	newPass := func(t *testing.T) (string, *config.AgentConfig, *fakeTmux, *cronState, *dispatchCycleStats) {
		t.Helper()
		root := setupCronFactory(t)
		agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
		if err != nil {
			t.Fatalf("LoadAgentConfig: %v", err)
		}
		return root, agents, newFakeTmux(), &cronState{Crons: map[string]cronRecord{}}, &dispatchCycleStats{start: base}
	}

	t.Run("a cron that is not yet due does nothing and prints nothing", func(t *testing.T) {
		root, agents, fake, st, stats := newPass(t)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)
		st.Crons["patrol"] = cronRecord{LastFiredAt: base.Add(-1 * time.Hour), LastOutcome: cronOutcomeFired}

		cmd, out, errBuf := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 0 {
			t.Fatalf("a not-due cron must not fire; got %v", argvs)
		}
		if got := st.Crons["patrol"].LastFiredAt; !got.Equal(base.Add(-1 * time.Hour)) {
			t.Errorf("LastFiredAt = %v, want unchanged", got)
		}
		if got := st.Crons["patrol"].LastCheckAt; !got.Equal(base) {
			t.Errorf("LastCheckAt = %v, want %v — every evaluated schedule records that it was seen", got, base)
		}
		if out.String() != "" || errBuf.String() != "" {
			t.Errorf("a not-due cron must print nothing; stdout=%q stderr=%q", out.String(), errBuf.String())
		}
		if stats.dispatched != 0 || stats.skipped != 0 || stats.errors != 0 {
			t.Errorf("a not-due cron must touch no counter; got %+v", stats)
		}
	})

	t.Run("a schedule with no prior state is due immediately", func(t *testing.T) {
		root, agents, fake, st, stats := newPass(t)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)

		cmd, out, _ := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 1 {
			t.Fatalf("zero LastFiredAt must be due at-least-once; fired %d times", len(argvs))
		}
		rec := st.Crons["patrol"]
		if !rec.LastFiredAt.Equal(base) || !rec.LastAttemptAt.Equal(base) {
			t.Errorf("a fire sets LastFiredAt and LastAttemptAt to now; got %+v", rec)
		}
		if rec.LastOutcome != cronOutcomeFired || rec.ConsecutiveFailures != 0 {
			t.Errorf("outcome/failures = %q/%d, want %q/0", rec.LastOutcome, rec.ConsecutiveFailures, cronOutcomeFired)
		}
		if stats.dispatched != 1 {
			t.Errorf("stats.dispatched = %d, want 1", stats.dispatched)
		}
		if !strings.Contains(out.String(), "patrol") {
			t.Errorf("a fire must name the schedule on stdout; got %q", out.String())
		}
	})

	t.Run("a schedule whose interval has elapsed fires and re-anchors", func(t *testing.T) {
		root, agents, fake, st, stats := newPass(t)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)
		st.Crons["patrol"] = cronRecord{LastFiredAt: base.Add(-5 * time.Hour), LastOutcome: cronOutcomeFired}

		cmd, _, _ := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 1 {
			t.Fatalf("5h elapsed on a 4h cadence must fire once; fired %d times", len(argvs))
		}
		if got := st.Crons["patrol"].LastFiredAt; !got.Equal(base) {
			t.Errorf("LastFiredAt = %v, want re-anchored to %v", got, base)
		}
	})

	t.Run("a busy target is skipped and stays due", func(t *testing.T) {
		root, agents, fake, st, stats := newPass(t)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)
		fake.present[session.SessionName("af610-patrol")] = true

		cmd, out, _ := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 0 {
			t.Fatalf("a busy target must not be slung; got %v", argvs)
		}
		rec := st.Crons["patrol"]
		if rec.LastOutcome != cronOutcomeSkippedBusy {
			t.Errorf("LastOutcome = %q, want %q", rec.LastOutcome, cronOutcomeSkippedBusy)
		}
		if !rec.LastFiredAt.IsZero() {
			t.Errorf("a skip must leave LastFiredAt alone so the cron stays due; got %v", rec.LastFiredAt)
		}
		if stats.skipped != 1 {
			t.Errorf("stats.skipped = %d, want 1", stats.skipped)
		}
		if !strings.Contains(out.String(), "patrol") {
			t.Errorf("a skip must name the schedule; got %q", out.String())
		}

		// Still due: freeing the agent fires on the very next tick, no interval consumed.
		delete(fake.present, session.SessionName("af610-patrol"))
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)
		if len(argvs) != 1 {
			t.Fatalf("a skipped cron must remain due for the next tick; fired %d times", len(argvs))
		}
	})

	t.Run("a halted target outranks busy and names the clearing verb", func(t *testing.T) {
		root, agents, fake, st, stats := newPass(t)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)
		fake.present[session.SessionName("af610-patrol")] = true
		if err := saveRecoveryState(root, "af610-patrol", recoveryState{
			Halted: true, HaltReason: haltReasonMaxAttempts, Attempts: 3,
		}); err != nil {
			t.Fatalf("saveRecoveryState: %v", err)
		}

		cmd, _, errBuf := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 0 {
			t.Fatalf("a halted target must not be slung; got %v", argvs)
		}
		rec := st.Crons["patrol"]
		if rec.LastOutcome != cronOutcomeError {
			t.Errorf("a halted target is an error, not a busy skip; LastOutcome = %q, want %q",
				rec.LastOutcome, cronOutcomeError)
		}
		if !strings.Contains(rec.LastDetail, "af recovery reset") {
			t.Errorf("a halted skip must record the clearing verb; LastDetail = %q", rec.LastDetail)
		}
		if !rec.LastFiredAt.IsZero() {
			t.Errorf("a halted skip must leave LastFiredAt alone; got %v", rec.LastFiredAt)
		}
		if !strings.Contains(errBuf.String(), "af recovery reset") {
			t.Errorf("a halted target is an escalation, not a quiet deferral; stderr = %q", errBuf.String())
		}
	})

	t.Run("an unreadable liveness probe is treated as busy, never as free", func(t *testing.T) {
		// S-1 (commit 9199d07c): a probe fault that defaults to not-running fires --reset at a
		// possibly-live agent, force-stopping it and wiping its runtime state.
		root, agents, fake, st, stats := newPass(t)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)
		fake.hasSessionErr[session.SessionName("af610-patrol")] = errors.New("tmux unreachable")

		cmd, _, _ := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 0 {
			t.Fatalf("an unreadable probe must not fire a --reset sling; got %v", argvs)
		}
		if got := st.Crons["patrol"].LastOutcome; got != cronOutcomeSkippedBusy {
			t.Errorf("LastOutcome = %q, want %q", got, cronOutcomeSkippedBusy)
		}
		if got := st.Crons["patrol"].LastDetail; !strings.Contains(got, "tmux unreachable") {
			t.Errorf("the probe error must reach the record; LastDetail = %q", got)
		}

		// Only plain busy is INFERRED from the forced liveness; the breaker states are read from
		// disk and hold regardless of the probe. A recovering target must therefore keep saying so
		// rather than being masked by the less actionable probe-fault text.
		if err := saveRecoveryState(root, "af610-patrol", recoveryState{Attempts: 1}); err != nil {
			t.Fatalf("saveRecoveryState: %v", err)
		}
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base.Add(time.Second))
		if got := st.Crons["patrol"].LastDetail; !strings.Contains(got, "recovery in progress") {
			t.Errorf("a known breaker state outranks the probe fault; LastDetail = %q", got)
		}
	})

	t.Run("a sling error does not advance LastFiredAt", func(t *testing.T) {
		root, agents, fake, st, stats := newPass(t)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, errors.New("boom"))

		cmd, _, errBuf := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 1 {
			t.Fatalf("the fire must be attempted once; got %d attempts", len(argvs))
		}
		rec := st.Crons["patrol"]
		if rec.LastOutcome != cronOutcomeError {
			t.Errorf("LastOutcome = %q, want %q", rec.LastOutcome, cronOutcomeError)
		}
		if !rec.LastFiredAt.IsZero() {
			t.Errorf("LastFiredAt records real fires only (HIGH-4); got %v", rec.LastFiredAt)
		}
		if !rec.LastAttemptAt.Equal(base) {
			t.Errorf("LastAttemptAt = %v, want %v", rec.LastAttemptAt, base)
		}
		if rec.ConsecutiveFailures != 1 {
			t.Errorf("ConsecutiveFailures = %d, want 1", rec.ConsecutiveFailures)
		}
		if !strings.Contains(rec.LastDetail, "boom") {
			t.Errorf("the sling error text must reach the record; LastDetail = %q", rec.LastDetail)
		}
		if stats.errors != 1 {
			t.Errorf("stats.errors = %d, want 1", stats.errors)
		}
		if !strings.Contains(errBuf.String(), "patrol") {
			t.Errorf("a failed fire must name the schedule on stderr; got %q", errBuf.String())
		}
	})

	t.Run("a failing schedule retries on bounded backoff, not every tick", func(t *testing.T) {
		root, agents, fake, st, stats := newPass(t)
		advance := fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, errors.New("boom"))
		cfg := cronTestCfg(cron)

		cmd, _, _ := phase3Cmd()
		processCrons(cmd, root, fake, cfg, agents, nil, st, stats, base)
		if len(argvs) != 1 {
			t.Fatalf("first attempt: fired %d times, want 1", len(argvs))
		}

		// backoff = min(every=4h, 1h, interval=300s x 2^0) = 5m.
		wait := cronRetryBackoff(cfg.IntervalSecs, 1, 4*time.Hour)
		if wait != 5*time.Minute {
			t.Fatalf("cronRetryBackoff(300, 1, 4h) = %v, want 5m", wait)
		}

		held := base.Add(wait - time.Second)
		advance(held)
		processCrons(cmd, root, fake, cfg, agents, nil, st, stats, held)
		if len(argvs) != 1 {
			t.Fatalf("a retry inside the backoff window must be held; fired %d times", len(argvs))
		}

		released := base.Add(wait)
		advance(released)
		processCrons(cmd, root, fake, cfg, agents, nil, st, stats, released)
		if len(argvs) != 2 {
			t.Fatalf("a retry at the backoff boundary must proceed; fired %d times", len(argvs))
		}
		if got := st.Crons["patrol"].ConsecutiveFailures; got != 2 {
			t.Errorf("ConsecutiveFailures = %d, want 2", got)
		}
	})

	t.Run("the backoff is bounded and never wraps to a hot loop", func(t *testing.T) {
		// time.Duration is int64 nanoseconds: an unclamped 300s << (failures-1) exceeds it at
		// roughly 26 failures — about two hours of a permanently broken schedule — and wraps to a
		// small or negative wait, restoring exactly the tick-speed crash-retry loop AC-6 forbids.
		for _, failures := range []int{1, 2, 5, 20, 26, 64, 1 << 20} {
			got := cronRetryBackoff(300, failures, 4*time.Hour)
			if got <= 0 {
				t.Fatalf("cronRetryBackoff(300, %d, 4h) = %v, must stay positive", failures, got)
			}
			if got > time.Hour {
				t.Errorf("cronRetryBackoff(300, %d, 4h) = %v, must be bounded by 1h", failures, got)
			}
		}
		// The schedule's own cadence is the tighter bound when it is under an hour.
		if got := cronRetryBackoff(300, 9, time.Minute); got != time.Minute {
			t.Errorf("cronRetryBackoff(300, 9, 1m) = %v, want the cadence itself (1m)", got)
		}
		if got := cronRetryBackoff(300, 0, 4*time.Hour); got != 0 {
			t.Errorf("cronRetryBackoff with no failures = %v, want 0 (no gate)", got)
		}
		// A non-positive base is only reachable from a hand-built config — the loader defaults it to
		// 300 — but the arithmetic answer there is zero, which IS the tick-speed retry this bound
		// exists to stop. It must substitute the bound, not the base.
		for _, secs := range []int{0, -1, -600} {
			if got := cronRetryBackoff(secs, 1, 4*time.Hour); got != time.Hour {
				t.Errorf("cronRetryBackoff(%d, 1, 4h) = %v, want the 1h bound, never 0", secs, got)
			}
			if got := cronRetryBackoff(secs, 3, time.Minute); got != time.Minute {
				t.Errorf("cronRetryBackoff(%d, 3, 1m) = %v, want the cadence bound, never 0", secs, got)
			}
		}
	})

	t.Run("the due predicate fires exactly at the cadence, not one tick late", func(t *testing.T) {
		// The boundary itself: now == LastFiredAt+every. Before() is strict, so this instant is due.
		// A >= that slipped to > would stretch every cadence by one dispatch interval, compounding.
		for _, tc := range []struct {
			name     string
			at       time.Time
			wantFire bool
		}{
			{"one nanosecond early", base.Add(4*time.Hour - 1), false},
			{"exactly one cadence", base.Add(4 * time.Hour), true},
			{"one nanosecond late", base.Add(4*time.Hour + 1), true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root, agents, fake, st, stats := newPass(t)
				fixCronClock(t, tc.at)
				var argvs [][]string
				recordCronSlings(t, &argvs, nil)
				st.Crons["patrol"] = cronRecord{LastFiredAt: base, LastOutcome: cronOutcomeFired}

				cmd, _, _ := phase3Cmd()
				processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, tc.at)

				if fired := len(argvs) == 1; fired != tc.wantFire {
					t.Errorf("fired = %v at %v, want %v", fired, tc.at.Sub(base), tc.wantFire)
				}
			})
		}
	})

	t.Run("an unparseable cadence is recorded, never silently never-due", func(t *testing.T) {
		root, agents, fake, st, stats := newPass(t)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)

		cmd, _, errBuf := phase3Cmd()
		bad := config.CronSchedule{Name: "bad", Agent: "af610-patrol", Every: "sometimes"}
		processCrons(cmd, root, fake, cronTestCfg(bad), agents, nil, st, stats, base)

		if len(argvs) != 0 {
			t.Fatalf("an unparseable cadence must not fire; got %v", argvs)
		}
		if got := st.Crons["bad"].LastOutcome; got != cronOutcomeError {
			t.Errorf("LastOutcome = %q, want %q — silence is the one outcome a schedule must never produce", got, cronOutcomeError)
		}
		if !strings.Contains(errBuf.String(), "bad") {
			t.Errorf("the bad schedule must be named; stderr = %q", errBuf.String())
		}

		// The parse will fail identically forever, so the complaint is backoff-gated like any other
		// failure — otherwise a single typo reprints on every tick until a human notices.
		errBuf.Reset()
		next := base.Add(time.Duration(cronTestCfg(bad).IntervalSecs) * time.Second / 2)
		processCrons(cmd, root, fake, cronTestCfg(bad), agents, nil, st, stats, next)
		if errBuf.String() != "" {
			t.Errorf("a gated retry must print nothing; stderr = %q", errBuf.String())
		}
		if got := st.Crons["bad"].ConsecutiveFailures; got != 1 {
			t.Errorf("ConsecutiveFailures = %d, want 1 — a gated tick is not an attempt", got)
		}
		if got := st.Crons["bad"].LastCheckAt; !got.Equal(next) {
			t.Errorf("LastCheckAt = %v, want %v — a gated schedule was still evaluated", got, next)
		}

		errBuf.Reset()
		past := base.Add(2 * time.Hour)
		processCrons(cmd, root, fake, cronTestCfg(bad), agents, nil, st, stats, past)
		if !strings.Contains(errBuf.String(), "bad") {
			t.Errorf("past the backoff the complaint must return; stderr = %q", errBuf.String())
		}
		if got := st.Crons["bad"].ConsecutiveFailures; got != 2 {
			t.Errorf("ConsecutiveFailures = %d, want 2", got)
		}
	})
}

// cronDurabilityFactory is the shared substrate for the durability subtests that actually fire:
// the fire-time backstop resolves the agent and its formula from disk, so a bare temp dir would
// downgrade every schedule to an error before the state write under test was ever reached.
func cronDurabilityFactory(t *testing.T) (string, *config.AgentConfig) {
	t.Helper()
	root := setupCronFactory(t)
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	return root, agents
}

// TestDispatchCron_RestartDurability pins the durability contract (issue #610 AC-4, N8): cron
// timing lives in its own file precisely so the 24h dispatch-state prune cannot silently reset a
// cadence longer than a day, and so a GitHub failure later in the cycle cannot lose a fire that
// already happened.
func TestDispatchCron_RestartDurability(t *testing.T) {
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	cron := config.CronSchedule{Name: "patrol", Agent: "af610-patrol", Every: "4h"}

	t.Run("cron timing survives the 24h dispatch-state prune", func(t *testing.T) {
		root := t.TempDir()
		old := cronRecord{
			LastFiredAt: base.Add(-48 * time.Hour),
			LastOutcome: cronOutcomeFired,
			LastCheckAt: base.Add(-48 * time.Hour),
		}
		if err := saveCronState(root, &cronState{Crons: map[string]cronRecord{"patrol": old}}); err != nil {
			t.Fatalf("saveCronState: %v", err)
		}

		// The prune operates on a structurally different file and struct; a 48h-old cron record is
		// out of its reach by construction (C-3).
		ds := dispatchState{Dispatched: map[string]dispatchEntry{
			"o/r#1": {Agent: "a", DispatchedAt: base.Add(-48 * time.Hour)},
		}}
		pruneDispatchState(&ds)
		if len(ds.Dispatched) != 0 {
			t.Fatalf("precondition: the 24h prune must drop a 48h-old dispatch entry; got %d", len(ds.Dispatched))
		}

		got := loadCronState(root)
		if len(got.Crons) != 1 {
			t.Fatalf("the cron record must survive the prune; got %+v", got)
		}
		if !got.Crons["patrol"].LastFiredAt.Equal(old.LastFiredAt) {
			t.Errorf("LastFiredAt round-trip = %v, want %v", got.Crons["patrol"].LastFiredAt, old.LastFiredAt)
		}
	})

	t.Run("the state write is atomic and leaves no temp file", func(t *testing.T) {
		root := t.TempDir()
		if err := saveCronState(root, &cronState{Crons: map[string]cronRecord{
			"patrol": {LastFiredAt: base, LastOutcome: cronOutcomeFired},
		}}); err != nil {
			t.Fatalf("saveCronState: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, ".runtime", "dispatch-crons.json")); err != nil {
			t.Fatalf(".runtime and the state file must be created: %v", err)
		}
		if _, err := os.Stat(filepath.Join(root, ".runtime", ".dispatch-crons.json.tmp")); !os.IsNotExist(err) {
			t.Errorf("the temp file must not remain after rename; stat err = %v", err)
		}
	})

	t.Run("an unwritable state file warns and counts, but never aborts the tick", func(t *testing.T) {
		// The one durability failure the pass cannot recover from: the fire has already spawned a
		// sling, so aborting here would lose the record AND leave the work done. It degrades to a
		// warning instead — the next tick simply re-fires, which is the at-least-once posture the
		// whole design already accepts.
		root, agents := cronDurabilityFactory(t)
		if err := os.MkdirAll(filepath.Join(root, ".runtime", "dispatch-crons.json"), 0o755); err != nil {
			t.Fatalf("mkdir: %v", err) // a directory where the file goes makes the rename fail
		}
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)
		st := &cronState{Crons: map[string]cronRecord{}}
		stats := &dispatchCycleStats{start: base}

		cmd, _, errBuf := phase3Cmd()
		processCrons(cmd, root, newFakeTmux(), cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 1 {
			t.Fatalf("the fire must still happen; fired %d times", len(argvs))
		}
		if !strings.Contains(errBuf.String(), "saving cron state") {
			t.Errorf("an unwritable state file must warn; stderr = %q", errBuf.String())
		}
		if stats.errors != 1 {
			t.Errorf("stats.errors = %d, want 1 — a lost record is a real fault, not silence", stats.errors)
		}
		if got := st.Crons["patrol"].LastOutcome; got != cronOutcomeFired {
			t.Errorf("the in-memory record still reflects the fire; got %q", got)
		}
	})

	t.Run("a missing or undecodable state file loads as empty, never nil", func(t *testing.T) {
		root := t.TempDir()
		if got := loadCronState(root); got.Crons == nil || len(got.Crons) != 0 {
			t.Errorf("missing file must load an initialized empty map; got %+v", got)
		}
		runtimeDir := filepath.Join(root, ".runtime")
		if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(filepath.Join(runtimeDir, "dispatch-crons.json"), []byte("{not json"), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := loadCronState(root); got.Crons == nil || len(got.Crons) != 0 {
			t.Errorf("undecodable file must load an initialized empty map; got %+v", got)
		}
	})

	t.Run("a fresh schedule fires once, not once per tick", func(t *testing.T) {
		root, agents := cronDurabilityFactory(t)
		fake := newFakeTmux()
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)
		st := &cronState{Crons: map[string]cronRecord{}}
		stats := &dispatchCycleStats{start: base}
		cmd, _, _ := phase3Cmd()

		for i := 0; i < 3; i++ {
			processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)
		}
		if len(argvs) != 1 {
			t.Fatalf("three ticks inside one 4h window must fire once; fired %d times", len(argvs))
		}

		// And the record is durable across a reload, so a restart does not re-fire.
		reloaded := loadCronState(root)
		if !reloaded.Crons["patrol"].LastFiredAt.Equal(base) {
			t.Fatalf("the pass must persist its own fire record; got %+v", reloaded.Crons["patrol"])
		}
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, &reloaded, stats, base.Add(time.Hour))
		if len(argvs) != 1 {
			t.Errorf("a restart inside the window must not re-fire; fired %d times", len(argvs))
		}
	})

	t.Run("dry run reports what would fire and writes no state", func(t *testing.T) {
		root, agents := cronDurabilityFactory(t)
		fake := newFakeTmux()
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)
		dispatchDryRun = true
		t.Cleanup(func() { dispatchDryRun = false })

		st := &cronState{Crons: map[string]cronRecord{}}
		stats := &dispatchCycleStats{start: base}
		cmd, out, _ := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if len(argvs) != 0 {
			t.Fatalf("--dry-run must sling nothing; got %v", argvs)
		}
		if !strings.Contains(out.String(), "would fire") || !strings.Contains(out.String(), "patrol") {
			t.Errorf("--dry-run must report what would fire; got %q", out.String())
		}
		if _, err := os.Stat(filepath.Join(root, ".runtime", "dispatch-crons.json")); !os.IsNotExist(err) {
			t.Errorf("--dry-run must commit no state; stat err = %v", err)
		}
	})

	t.Run("a removed schedule leaves no orphan record", func(t *testing.T) {
		root, agents := cronDurabilityFactory(t)
		fake := newFakeTmux()
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)

		st := &cronState{Crons: map[string]cronRecord{
			"gone":   {LastFiredAt: base.Add(-time.Hour), LastOutcome: cronOutcomeFired},
			"patrol": {LastFiredAt: base.Add(-time.Hour), LastOutcome: cronOutcomeFired},
		}}
		stats := &dispatchCycleStats{start: base}
		cmd, _, _ := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, nil, st, stats, base)

		if _, ok := st.Crons["gone"]; ok {
			t.Errorf("a schedule absent from config must leave no status trace; got %+v", st.Crons)
		}
		if _, ok := st.Crons["patrol"]; !ok {
			t.Errorf("a configured schedule must be kept; got %+v", st.Crons)
		}
		if _, ok := loadCronState(root).Crons["gone"]; ok {
			t.Errorf("the orphan must be gone from the persisted file too")
		}
	})

	t.Run("the cron pass runs inside the lock and before gh-auth", func(t *testing.T) {
		// The ordering itself is inside runDispatch, which has no seam around `gh issue list`, so
		// it is pinned two ways: structurally over the source, and behaviorally with checkGHAuth
		// failing (which returns the cycle before the unseamed query is ever reached).
		src, err := os.ReadFile("dispatch.go")
		if err != nil {
			t.Fatalf("read dispatch.go: %v", err)
		}
		acquire := strings.Index(string(src), "lk.Acquire(")
		pass := strings.Index(string(src), "processCrons(cmd, root, t,")
		auth := strings.Index(string(src), "checkGHAuth()")
		if acquire < 0 || pass < 0 || auth < 0 {
			t.Fatalf("ordering anchors missing: acquire=%d processCrons=%d checkGHAuth=%d", acquire, pass, auth)
		}
		if !(acquire < pass && pass < auth) {
			t.Errorf("cycle order must be lock -> crons -> gh-auth; got acquire=%d processCrons=%d checkGHAuth=%d",
				acquire, pass, auth)
		}
	})

	t.Run("a gh-auth failure after the pass still leaves cron state on disk", func(t *testing.T) {
		root := setupCronFactory(t)
		writeDispatchJSON(t, root, `{"repos":["o/r"],"trigger_label":"af-dispatch",`+
			`"mappings":[{"labels":["bug"],"agent":"af610-patrol"}],`+
			`"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h"}]}`)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)

		origAuth := checkGHAuth
		checkGHAuth = func() error { return errors.New("gh: not logged in") }
		t.Cleanup(func() { checkGHAuth = origAuth })

		cmd, _, _ := phase3Cmd()
		err := runDispatch(cmd, nil)
		if err == nil {
			t.Fatalf("a gh-auth failure must still fail the cycle")
		}
		if !strings.Contains(err.Error(), "not authenticated") {
			t.Errorf("cycle error = %v, want the gh-auth failure", err)
		}
		if len(argvs) != 1 {
			t.Fatalf("the cron must have fired before gh-auth was consulted; fired %d times", len(argvs))
		}
		rec := loadCronState(root).Crons["patrol"]
		if rec.LastOutcome != cronOutcomeFired || !rec.LastFiredAt.Equal(base) {
			t.Errorf("the fire record must survive the aborted cycle; got %+v", rec)
		}
	})

	t.Run("a crons-only factory never consults gh", func(t *testing.T) {
		root := setupCronFactory(t)
		writeDispatchJSON(t, root, `{"crons":[{"name":"patrol","agent":"af610-patrol","every":"4h"}]}`)
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)

		origAuth := checkGHAuth
		var authCalls int
		checkGHAuth = func() error { authCalls++; return errors.New("gh: not logged in") }
		t.Cleanup(func() { checkGHAuth = origAuth })

		cmd, _, _ := phase3Cmd()
		if err := runDispatch(cmd, nil); err != nil {
			t.Fatalf("a crons-only cycle must not fail on GitHub: %v", err)
		}
		if authCalls != 0 {
			t.Errorf("checkGHAuth called %d times on a repo-less factory, want 0", authCalls)
		}
		if len(argvs) != 1 {
			t.Errorf("the schedule must still fire; fired %d times", len(argvs))
		}
		if rec := loadCronState(root).Crons["patrol"]; rec.LastOutcome != cronOutcomeFired {
			t.Errorf("the fire must be durable on a repo-less factory too; got %+v", rec)
		}
	})
}

// TestDispatchCron_FireTimeVarError pins the fire-time backstop (issue #610 AC-6, N4/N5). Phase 2
// validates a schedule when it is WRITTEN; this is the arm that catches a schedule which has gone
// stale since — agent uninstalled, formula edited, required var no longer supplied — and downgrades
// it to a per-schedule error instead of letting it hard-error inside sling on every tick forever,
// or abort the tick that also serves labelled items.
func TestDispatchCron_FireTimeVarError(t *testing.T) {
	base := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	load := func(t *testing.T, root string) (*config.AgentConfig, *config.ModelsConfig) {
		t.Helper()
		agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
		if err != nil {
			t.Fatalf("LoadAgentConfig: %v", err)
		}
		return agents, nil
	}

	t.Run("a required var missing at fire time records an actionable error", func(t *testing.T) {
		root := setupCronFactory(t)
		agents, models := load(t, root)
		fake := newFakeTmux()
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)

		// af610-pm's formula declares a required cli var "issue" with no default; the schedule
		// supplies none. Phase 2 rejects this at write time (cron_check_test.go:260) — here it
		// arrives already written, as an edit to the formula would leave it.
		cron := config.CronSchedule{Name: "weekly-pm", Agent: "af610-pm", Every: "1m"}
		st := &cronState{Crons: map[string]cronRecord{}}
		stats := &dispatchCycleStats{start: base}
		cmd, _, errBuf := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(cron), agents, models, st, stats, base)

		if len(argvs) != 0 {
			t.Fatalf("the backstop must fire BEFORE the sling, not after it errors; got %v", argvs)
		}
		rec := st.Crons["weekly-pm"]
		if rec.LastOutcome != cronOutcomeError {
			t.Fatalf("LastOutcome = %q, want %q", rec.LastOutcome, cronOutcomeError)
		}
		for _, want := range []string{"weekly-pm", "issue"} {
			if !strings.Contains(rec.LastDetail, want) {
				t.Errorf("LastDetail must be actionable and name %q; got %q", want, rec.LastDetail)
			}
		}
		if !rec.LastAttemptAt.Equal(base) {
			t.Errorf("LastAttemptAt = %v, want %v", rec.LastAttemptAt, base)
		}
		if !rec.LastFiredAt.IsZero() {
			t.Errorf("a backstop rejection is not a fire; LastFiredAt = %v", rec.LastFiredAt)
		}
		if rec.ConsecutiveFailures != 1 {
			t.Errorf("ConsecutiveFailures = %d, want 1", rec.ConsecutiveFailures)
		}
		if stats.errors != 1 {
			t.Errorf("stats.errors = %d, want 1", stats.errors)
		}
		if !strings.Contains(errBuf.String(), "weekly-pm") {
			t.Errorf("the operator must see which schedule is broken; stderr = %q", errBuf.String())
		}
	})

	t.Run("a stale schedule never aborts the tick that also serves items", func(t *testing.T) {
		root := setupCronFactory(t)
		agents, models := load(t, root)
		fake := newFakeTmux()
		fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)

		broken := config.CronSchedule{Name: "weekly-pm", Agent: "af610-pm", Every: "1m"}
		ghost := config.CronSchedule{Name: "ghost", Agent: "af610-not-installed", Every: "1m"}
		healthy := config.CronSchedule{Name: "patrol", Agent: "af610-patrol", Every: "1m"}

		st := &cronState{Crons: map[string]cronRecord{}}
		stats := &dispatchCycleStats{start: base}
		cmd, _, _ := phase3Cmd()
		processCrons(cmd, root, fake, cronTestCfg(broken, ghost, healthy), agents, models, st, stats, base)

		if st.Crons["weekly-pm"].LastOutcome != cronOutcomeError {
			t.Errorf("the var-less schedule must be downgraded; got %+v", st.Crons["weekly-pm"])
		}
		if st.Crons["ghost"].LastOutcome != cronOutcomeError {
			t.Errorf("a schedule whose agent is gone must be downgraded; got %+v", st.Crons["ghost"])
		}
		if st.Crons["patrol"].LastOutcome != cronOutcomeFired {
			t.Errorf("a healthy sibling must still fire; got %+v", st.Crons["patrol"])
		}
		if len(argvs) != 1 {
			t.Errorf("exactly the healthy schedule fires; got %v", argvs)
		}
	})

	t.Run("the broken schedule retries on the backoff, not every tick", func(t *testing.T) {
		root := setupCronFactory(t)
		agents, models := load(t, root)
		fake := newFakeTmux()
		advance := fixCronClock(t, base)
		var argvs [][]string
		recordCronSlings(t, &argvs, nil)

		// every="1m" with the default 300s tick makes min(every, 1h, interval) == every, so the
		// retry lands exactly one cadence later.
		cron := config.CronSchedule{Name: "weekly-pm", Agent: "af610-pm", Every: "1m"}
		cfg := cronTestCfg(cron)
		st := &cronState{Crons: map[string]cronRecord{}}
		stats := &dispatchCycleStats{start: base}
		cmd, _, _ := phase3Cmd()

		processCrons(cmd, root, fake, cfg, agents, models, st, stats, base)
		if st.Crons["weekly-pm"].ConsecutiveFailures != 1 {
			t.Fatalf("first evaluation must record one failure; got %+v", st.Crons["weekly-pm"])
		}

		if got := cronRetryBackoff(cfg.IntervalSecs, 1, time.Minute); got != time.Minute {
			t.Fatalf("cronRetryBackoff(300, 1, 1m) = %v, want one cadence (1m)", got)
		}

		held := base.Add(30 * time.Second)
		advance(held)
		processCrons(cmd, root, fake, cfg, agents, models, st, stats, held)
		if got := st.Crons["weekly-pm"].ConsecutiveFailures; got != 1 {
			t.Fatalf("a retry inside the backoff must be held; ConsecutiveFailures = %d", got)
		}
		if got := st.Crons["weekly-pm"].LastAttemptAt; !got.Equal(base) {
			t.Errorf("a held tick must not restamp LastAttemptAt; got %v", got)
		}

		released := base.Add(time.Minute)
		advance(released)
		processCrons(cmd, root, fake, cfg, agents, models, st, stats, released)
		if got := st.Crons["weekly-pm"].ConsecutiveFailures; got != 2 {
			t.Errorf("a retry one cadence later must proceed; ConsecutiveFailures = %d, want 2", got)
		}
		if len(argvs) != 0 {
			t.Errorf("a permanently broken schedule must never reach the sling; got %v", argvs)
		}
	})
}
