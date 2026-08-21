package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/statusline"
)

// The C-1 funnel lane. Everything here drives the REAL respawnSession, because the property under
// test is that the audit trail is anchored where no recycle class can bypass it — a test that
// called recordRecycle directly would prove only that the writer works, which was already true
// while the funnel ignored it entirely.
//
// None of these may call t.Parallel: they reassign package vars, which
// tmux_isolation_enforce_test.go's seamReassignPattern flags, and they share recoveryTracks.

// readRecoveryLogLines returns the decoded K6 lines at the factory root. It composes the path by
// hand rather than through recoveryLogPath, so a fault inside that constructor is detectable here
// (the StatuslineSessionsDir doc comment states this rule for the same reason).
func readRecoveryLogLines(t *testing.T, root string) []recoveryLogEntry {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".runtime", "recovery_log.jsonl"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("reading recovery log: %v", err)
	}
	var out []recoveryLogEntry
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var e recoveryLogEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("recovery log line is not JSON: %v (%q)", err, line)
		}
		out = append(out, e)
	}
	return out
}

// readBreakerState reads the durable latch, again composing the path by hand.
func readBreakerState(t *testing.T, root, agent string) recoveryState {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".runtime", "recovery", agent+".json"))
	if err != nil {
		t.Fatalf("reading breaker state for %s: %v", agent, err)
	}
	var st recoveryState
	if err := json.Unmarshal(data, &st); err != nil {
		t.Fatalf("breaker state is not JSON: %v", err)
	}
	return st
}

// TestRecoveryFunnel_LogsTriggerForEveryRecycleClass is AC-6's "every factory-initiated recovery":
// the funnel — not the occupancy executor — is what writes the audit line, so a crash, a
// compact-handoff and a self-handoff are logged with the same fidelity as a context exhaustion.
func TestRecoveryFunnel_LogsTriggerForEveryRecycleClass(t *testing.T) {
	for _, tc := range []struct{ name, trigger, want string }{
		{"unset_is_unknown", "", triggerUnknown},
		{"context_exhaustion", triggerContextExhaustion, "context_exhaustion"},
		{"dark_at_high_occupancy", triggerDarkAtHighOccupancy, "dark_at_high_occupancy"},
		{"progress_backstop", triggerProgressBackstop, "progress_backstop"},
		{"crash", triggerCrash, "crash"},
		{"error_pattern", triggerErrorPattern, "error_pattern"},
		{"compact_handoff", triggerCompactHandoff, "compact_handoff"},
		{"self_handoff", triggerSelfHandoff, "self_handoff"},
		{"step_boundary_handoff", triggerStepBoundaryHandoff, "step_boundary_handoff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			mock := &mockTmux{}
			if err := respawnSession(RespawnOptions{
				FactoryRoot: root,
				AgentName:   "worker",
				AgentEntry:  config.AgentEntry{Type: "autonomous"},
				PaneID:      "%0",
				Trigger:     tc.trigger,
				Tx:          mock,
			}); err != nil {
				t.Fatalf("respawnSession: %v", err)
			}

			lines := readRecoveryLogLines(t, root)
			if len(lines) != 1 {
				t.Fatalf("one recycle must append exactly one K6 line at %s, got %d",
					filepath.Join(root, ".runtime", "recovery_log.jsonl"), len(lines))
			}
			got := lines[0]
			if got.Trigger != tc.want {
				t.Errorf("trigger = %q, want %q — the funnel must name the recycle CLASS, and an "+
					"unset class must read %q rather than the empty string", got.Trigger, tc.want, triggerUnknown)
			}
			if got.Agent != "worker" {
				t.Errorf("agent = %q, want %q", got.Agent, "worker")
			}
			if got.Outcome != outcomeRespawned {
				t.Errorf("outcome = %q, want %q", got.Outcome, outcomeRespawned)
			}
			if _, err := time.Parse(time.RFC3339, got.At); err != nil {
				t.Errorf("at = %q, want RFC3339 — the fence's newer-than compare crosses this format: %v", got.At, err)
			}
		})
	}
}

// TestRecycleFence_ArmedOnEveryRecycleClass is the other half of C-1. Without the arm, a handoff or
// compact recycle leaves the dead session's final high snapshot as the newest observation and the
// next poll recycles the replacement session on its predecessor's occupancy (Gap 2).
func TestRecycleFence_ArmedOnEveryRecycleClass(t *testing.T) {
	for _, tc := range []struct{ name, trigger, want string }{
		{"unset_is_unknown", "", triggerUnknown},
		{"context_exhaustion", triggerContextExhaustion, "context_exhaustion"},
		{"dark_at_high_occupancy", triggerDarkAtHighOccupancy, "dark_at_high_occupancy"},
		{"progress_backstop", triggerProgressBackstop, "progress_backstop"},
		{"crash", triggerCrash, "crash"},
		{"error_pattern", triggerErrorPattern, "error_pattern"},
		{"compact_handoff", triggerCompactHandoff, "compact_handoff"},
		{"self_handoff", triggerSelfHandoff, "self_handoff"},
		{"step_boundary_handoff", triggerStepBoundaryHandoff, "step_boundary_handoff"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if err := respawnSession(RespawnOptions{
				FactoryRoot: root,
				AgentName:   "worker",
				AgentEntry:  config.AgentEntry{Type: "autonomous"},
				PaneID:      "%0",
				Trigger:     tc.trigger,
				Tx:          &mockTmux{},
			}); err != nil {
				t.Fatalf("respawnSession: %v", err)
			}

			st := readBreakerState(t, root, "worker")
			if st.LastRecoveryAt == "" {
				t.Error("last_recovery_at is empty — the fence was not armed, so the dead session's " +
					"own final high snapshot would recycle its replacement (Gap 2)")
			}
			if st.LastTrigger != tc.want {
				t.Errorf("last_trigger = %q, want %q", st.LastTrigger, tc.want)
			}
			if st.AdvisorySessionID != "" {
				t.Errorf("advisory_session_id = %q, want cleared — a recycle starts a new session, so "+
					"leaving the latch set silences the new session's advisory entirely (L-2)", st.AdvisorySessionID)
			}
		})
	}
}

// The caller-wiring lane. Everything above drives respawnSession directly, which proves the funnel
// echoes the class it was handed — and nothing about whether the real callers hand it the RIGHT
// one. AC-2's check is `grep -lF 'Trigger:'` across three files, which a comment satisfies, and
// three copy-pasted identical constants satisfy it too. These four tests drive the PRODUCTION entry
// points and read the class back off disk, which is the only thing that can catch either mistake.

// tmuxPaneEnv puts the caller inside a tmux pane. The pane id is a bare "%0" rather than a session
// name, so the ADR-018 guard (which panics on af- identities that are not af-test-) never engages;
// the respawn then fails harmlessly against the isolated socket, which is exactly the
// outcome:"respawn_failed" path and does not affect the class under test.
func tmuxPaneEnv(t *testing.T) {
	t.Helper()
	t.Setenv("TMUX", "/tmp/tmux-1000/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")
}

func TestRespawnLog_SelfHandoffCallerTagsItsClass(t *testing.T) {
	tmuxPaneEnv(t)
	root := setupTestFactoryForDone(t, "manager")
	(&mailRecorder{}).install(t)

	// The error is ignored deliberately: the respawn cannot succeed against the isolated tmux
	// socket, and the assertion is about what the funnel RECORDED on the way through.
	_ = runHandoffCore(t.Context(), config.AgentDir(root, "manager"), "subject", "message", false, false, false)

	lines := readRecoveryLogLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("af handoff must leave exactly one K6 line, got %d", len(lines))
	}
	if lines[0].Trigger != triggerSelfHandoff {
		t.Errorf("trigger = %q, want %q — the self-handoff caller must name its own class, "+
			"otherwise AC-6's cause mix silently loses a whole recycle population", lines[0].Trigger, triggerSelfHandoff)
	}
	if lines[0].Agent != "manager" {
		t.Errorf("agent = %q, want %q", lines[0].Agent, "manager")
	}
}

func TestRespawnLog_CompactHandoffCallerTagsItsClass(t *testing.T) {
	tmuxPaneEnv(t)
	root := setupTestFactoryForDone(t, "manager")
	(&mailRecorder{}).install(t)

	_ = runCompactHandoffCore(t.Context(), config.AgentDir(root, "manager"), false)

	lines := readRecoveryLogLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("compact-handoff must leave exactly one K6 line, got %d", len(lines))
	}
	if lines[0].Trigger != triggerCompactHandoff {
		t.Errorf("trigger = %q, want %q", lines[0].Trigger, triggerCompactHandoff)
	}
}

// configurableWatchdogTmux exists because the in-tree fakeWatchdogTmux hardcodes
// IsClaudeRunning→true and benign pane output, so no existing test can reach recoverAgent at all —
// the crash and error_pattern classes have had zero coverage of any kind.
type configurableWatchdogTmux struct {
	claudeRunning bool
	output        string
}

func (f *configurableWatchdogTmux) HasSession(string) (bool, error)         { return true, nil }
func (f *configurableWatchdogTmux) IsClaudeRunning(string) bool             { return f.claudeRunning }
func (f *configurableWatchdogTmux) CapturePane(string, int) (string, error) { return f.output, nil }
func (f *configurableWatchdogTmux) CapturePaneJoined(string, int) (string, error) {
	return f.output, nil
}

// TestRespawnLog_WatchdogCallersTagTheirClasses drives the real poll loop down both watchdog
// recycle paths. recoverAgent takes the class as a parameter alongside the free-text pattern, and a
// single forwarding mistake would collapse both classes into one — which no direct-call test can
// see, because both call sites pass through the same function.
func TestRespawnLog_WatchdogCallersTagTheirClasses(t *testing.T) {
	for _, tc := range []struct {
		name string
		tx   *configurableWatchdogTmux
		want string
	}{
		{"dead_claude_process_is_crash", &configurableWatchdogTmux{claudeRunning: false}, triggerCrash},
		{
			"pane_error_signature_is_error_pattern",
			&configurableWatchdogTmux{claudeRunning: true, output: "Invalid signature in thinking block"},
			triggerErrorPattern,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// "test-worker" makes the pane af-test-worker, which the ADR-018 guard deliberately
			// exempts — a bare "worker" would produce af-worker and panic the test binary.
			root := t.TempDir()
			writeTestAgentsConfig(t, root, `{"agents":{"test-worker":{"type":"autonomous","description":"w"}}}`)
			writeTestJSON(t, filepath.Join(root, ".agentfactory", "factory.json"),
				map[string]any{"type": "factory", "version": 1, "name": "f"})
			(&mailRecorder{}).install(t)

			origTmux := newWatchdogTmux
			newWatchdogTmux = func() watchdogTmux { return tc.tx }
			t.Cleanup(func() { newWatchdogTmux = origTmux })

			pollAgents(&cobra.Command{}, root, map[string]struct{}{"test-worker": {}},
				map[string]*watchdogAgentState{}, map[string]int{}, 2)

			lines := readRecoveryLogLines(t, root)
			if len(lines) != 1 {
				t.Fatalf("the watchdog recycle must leave exactly one K6 line, got %d", len(lines))
			}
			if lines[0].Trigger != tc.want {
				t.Errorf("trigger = %q, want %q — the watchdog's two recycle causes must stay "+
					"distinguishable in the log", lines[0].Trigger, tc.want)
			}
		})
	}
}

// TestRespawnLog_ResolvesUnderTheFactoryRoot closes AC-6's vacuity hole: the criterion greps for the
// string "recovery_log.jsonl", which a comment satisfies. This asserts the bytes land at the one
// path, derived from a root the test itself chose — never from the worktree's .runtime symlink.
func TestRespawnLog_ResolvesUnderTheFactoryRoot(t *testing.T) {
	root := t.TempDir()
	if err := respawnSession(RespawnOptions{
		FactoryRoot: root,
		AgentName:   "worker",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%0",
		Trigger:     triggerContextExhaustion,
		Tx:          &mockTmux{},
	}); err != nil {
		t.Fatalf("respawnSession: %v", err)
	}

	for _, want := range []string{
		filepath.Join(root, ".runtime", "recovery_log.jsonl"),
		filepath.Join(root, ".runtime", "recovery", "worker.json"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Errorf("recovery artifact must resolve under the FACTORY ROOT at %s: %v", want, err)
		}
	}
}

// TestRespawnLog_FailedRespawnRecordsFailedOutcomeAndReturnsError pins both halves at once: the log
// records the failure, AND the funnel still returns the caller's original error. Ordering the K6
// write so that it replaced or masked the respawn error would break the existing
// TestRespawnSession_ReturnsRespawnPaneError, and would make a failed recycle look successful.
func TestRespawnLog_FailedRespawnRecordsFailedOutcomeAndReturnsError(t *testing.T) {
	root := t.TempDir()
	sentinel := &respawnFailure{}
	err := respawnSession(RespawnOptions{
		FactoryRoot: root,
		AgentName:   "worker",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%0",
		Trigger:     triggerCrash,
		Tx:          &mockTmux{respawnErr: sentinel},
	})
	if err != sentinel {
		t.Fatalf("respawnSession must return the underlying respawn error unchanged, got %v", err)
	}

	lines := readRecoveryLogLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("a FAILED recycle must still be logged, got %d lines", len(lines))
	}
	if lines[0].Outcome != outcomeRespawnFailed {
		t.Errorf("outcome = %q, want %q — a recycle that could not respawn must not read as success",
			lines[0].Outcome, outcomeRespawnFailed)
	}
}

// respawnFailure is a distinguishable error identity, so the test can assert the funnel returned
// THAT error rather than merely some error.
type respawnFailure struct{}

func (*respawnFailure) Error() string { return "tmux respawn failed" }

// TestRecoveryFunnel_EmptyRootIsANoOp guards the one input that would scatter recovery state across
// the filesystem: with no root, filepath.Join composes a RELATIVE .runtime path and writes into
// whatever directory the process happens to be in.
func TestRecoveryFunnel_EmptyRootIsANoOp(t *testing.T) {
	mock := &mockTmux{}
	if err := respawnSession(RespawnOptions{
		FactoryRoot: "",
		AgentName:   "worker",
		AgentEntry:  config.AgentEntry{Type: "autonomous"},
		PaneID:      "%0",
		Trigger:     triggerCrash,
		Tx:          mock,
	}); err != nil {
		t.Fatalf("respawnSession: %v", err)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Errorf("the recycle itself must still happen, got %d RespawnPane calls", len(mock.respawnPaneCalls))
	}
	if _, err := os.Stat(filepath.Join(".runtime", "recovery_log.jsonl")); !os.IsNotExist(err) {
		t.Errorf("a rootless recycle must not create .runtime relative to the working directory (err=%v)", err)
	}
}

// TestRespawnLog_RotatesAtLineCap exercises the size guard. It is only reachable because the cap is
// a var (the telemetry/store.go:28-37 idiom) — at the shipped 10k it would be dead code.
func TestRespawnLog_RotatesAtLineCap(t *testing.T) {
	orig := recoveryLogRotateLines
	recoveryLogRotateLines = 3
	t.Cleanup(func() { recoveryLogRotateLines = orig })

	root := t.TempDir()
	for i := 0; i < 5; i++ {
		if err := respawnSession(RespawnOptions{
			FactoryRoot: root,
			AgentName:   "worker",
			AgentEntry:  config.AgentEntry{Type: "autonomous"},
			PaneID:      "%0",
			Trigger:     triggerCrash,
			Tx:          &mockTmux{},
		}); err != nil {
			t.Fatalf("respawnSession %d: %v", i, err)
		}
	}

	if _, err := os.Stat(filepath.Join(root, ".runtime", "recovery_log.jsonl.1")); err != nil {
		t.Errorf("the log must rotate once past its cap: %v", err)
	}
	if got := len(readRecoveryLogLines(t, root)); got > recoveryLogRotateLines {
		t.Errorf("live log holds %d lines, want <= %d", got, recoveryLogRotateLines)
	}
}

// --- K4: the trigger rule, and the negatives that bound it ---------------------------------------

// TestRecoveryFunnel_SustainedHighOccupancyFires is AC-2's attested shape: the decision rests on
// occupancy alone, so it fires for an agent whose pane is still repainting and whose process still
// matches — the signals the incident defeated.
func TestRecoveryFunnel_SustainedHighOccupancyFires(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 92, now.Add(-10*time.Second), now, f.cfg)

	// confirm_ticks is 2: one high reading is not enough.
	if d := evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now); d.verdict.fire {
		t.Fatal("a single high reading must not fire — confirm_ticks exists to require a sustained one")
	}
	if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
		t.Fatalf("no recycle may have happened yet, got %d log lines", got)
	}

	d := evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now.Add(30*time.Second))
	if !d.verdict.fire {
		t.Fatalf("sustained high occupancy must fire, got reason %q", d.verdict.reason)
	}
	if d.verdict.trigger != triggerContextExhaustion {
		t.Errorf("trigger = %q, want %q", d.verdict.trigger, triggerContextExhaustion)
	}
	lines := readRecoveryLogLines(t, f.root)
	if len(lines) != 1 {
		t.Fatalf("the recycle must be logged exactly once, got %d", len(lines))
	}
	if lines[0].Trigger != triggerContextExhaustion || lines[0].ThresholdPct != 85 {
		t.Errorf("line = %+v, want trigger %q and threshold 85", lines[0], triggerContextExhaustion)
	}
	if lines[0].ObservedPct < 91 || lines[0].ObservedPct > 93 {
		t.Errorf("observed_pct = %v, want ~92", lines[0].ObservedPct)
	}
	if lines[0].ResumedStep != "step-1" {
		t.Errorf("resumed_step = %q, want %q — AC-6 (iii) is what was resumed", lines[0].ResumedStep, "step-1")
	}
}

// TestRecoveryFunnel_LowOccupancyLongStepNeverFires is AC-4 (i): a legitimately long step is the
// case a wrong trigger would destroy, and duration alone must never be evidence.
func TestRecoveryFunnel_LowOccupancyLongStepNeverFires(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	for i := 0; i < 10; i++ {
		at := now.Add(time.Duration(i) * 30 * time.Second)
		reading := plantSnapshot(t, f.root, f.agent, "sess-a", 40, at.Add(-5*time.Second), at, f.cfg)
		if d := evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, at); d.verdict.fire {
			t.Fatalf("tick %d fired at 40%% occupancy — low occupancy must never fire, however long the step runs", i)
		}
	}
	if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
		t.Errorf("recovery log has %d lines, want 0 — the recording funnel proves no recycle occurred", got)
	}
}

// TestRecoveryFunnel_InteractiveAgentIsGated is Decision 7: a human is present, and every other
// automatic response in the factory already stops for them.
func TestRecoveryFunnel_InteractiveAgentIsGated(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	f.stateStep(t, "step-1")
	f.sessionLive(true)
	now := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	reading := plantSnapshot(t, f.root, f.agent, "sess-a", 95, now.Add(-10*time.Second), now, f.cfg)
	var d recoveryDecision
	for i := 0; i < 3; i++ {
		d = evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "interactive"}, reading, f.cfg, now.Add(time.Duration(i)*30*time.Second))
	}
	if d.verdict.fire {
		t.Error("an interactive agent must never be auto-recycled")
	}
	if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
		t.Errorf("recovery log has %d lines, want 0", got)
	}
}

func TestRecoveryFunnel_DisabledConfigIsGated(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(f.root))
	if err != nil {
		t.Fatal(err)
	}
	disabled := false
	cfg := testRecoveryConfig()
	cfg.Enabled = &disabled

	if got := pollOccupancy(f.root, agentsCfg, cfg, time.Now()); got != nil {
		t.Errorf("recovery disabled must evaluate nothing, got %d decisions", len(got))
	}
}

// TestRecoveryFunnel_UnusableConfigRefusesToEvaluate is the fail-closed guard. A zero threshold
// makes EVERY reading "high", which would recycle the entire factory on the first tick — the one
// misconfiguration whose blast radius is the whole fleet.
func TestRecoveryFunnel_UnusableConfigRefusesToEvaluate(t *testing.T) {
	f := newRecoveryFixture(t, "supervisor")
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(f.root))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		mut  func(*config.RecoveryConfig)
	}{
		{"zero_threshold", func(c *config.RecoveryConfig) { c.ContextThresholdPct = 0 }},
		{"zero_staleness", func(c *config.RecoveryConfig) { c.StalenessSecs = 0 }},
		{"zero_dark_grace", func(c *config.RecoveryConfig) { c.DarkGraceSecs = 0 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := testRecoveryConfig()
			tc.mut(&cfg)
			if got := pollOccupancy(f.root, agentsCfg, cfg, time.Now()); got != nil {
				t.Errorf("an unusable config must refuse to evaluate, got %d decisions", len(got))
			}
			if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
				t.Errorf("recovery log has %d lines, want 0", got)
			}
		})
	}
}

// --- K19: the fence's two-condition rule ----------------------------------------------------------

// TestRecycleFence_DeadSessionSnapshotDoesNotRefire is Gap 2. Re-firing requires an observation that
// is BOTH newer than the recycle AND from a different session. Either half alone is insufficient,
// and the (older, different session) cell is the one a "different id ⇒ allow" implementation fails.
func TestRecycleFence_DeadSessionSnapshotDoesNotRefire(t *testing.T) {
	recycledAt := time.Date(2026, 8, 5, 12, 0, 0, 0, time.UTC)

	for _, tc := range []struct {
		name      string
		sessionID string
		writtenAt time.Time
		noDatum   bool
		wantBlock bool
	}{
		{name: "newer_same_session_is_the_dying_session_still_rendering", sessionID: "sess-old", writtenAt: recycledAt.Add(time.Minute), wantBlock: true},
		{name: "older_different_session_is_a_leftover_file", sessionID: "sess-new", writtenAt: recycledAt.Add(-time.Minute), wantBlock: true},
		{name: "newer_different_session_is_genuinely_new_evidence", sessionID: "sess-new", writtenAt: recycledAt.Add(time.Minute), wantBlock: false},
		{name: "no_datum_cannot_demonstrate_anything", noDatum: true, wantBlock: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRecoveryFixture(t, "supervisor")
			f.stateStep(t, "step-1")
			f.sessionLive(true)
			now := recycledAt.Add(2 * time.Minute)

			if err := armRecycleFence(f.root, f.agent, triggerContextExhaustion, "sess-old", recycledAt); err != nil {
				t.Fatal(err)
			}

			reading := statusline.NoReading()
			if !tc.noDatum {
				reading = plantSnapshot(t, f.root, f.agent, tc.sessionID, 95, tc.writtenAt, now, f.cfg)
			}

			st := loadRecoveryState(f.root, f.agent)
			if got := recycleFenceBlocks(st, reading); got != tc.wantBlock {
				t.Errorf("recycleFenceBlocks = %v, want %v", got, tc.wantBlock)
			}

			// And prove the ordering: the fence is consulted before anything acts, so a blocked
			// agent produces no recycle even at 95%% sustained.
			if tc.wantBlock {
				for i := 0; i < 3; i++ {
					d := evaluateAgent(f.root, f.agent, config.AgentEntry{Type: "autonomous"}, reading, f.cfg, now.Add(time.Duration(i)*30*time.Second))
					if d.verdict.fire {
						t.Fatalf("tick %d fired despite the fence", i)
					}
				}
				if got := len(readRecoveryLogLines(t, f.root)); got != 0 {
					t.Errorf("a fenced agent must not be recycled, got %d log lines", got)
				}
			}
		})
	}
}

// --- K20: re-provisioning on every class ------------------------------------------------------------

// TestRecoveryFunnel_ReprovisionsSettingsOnEveryClass covers the pure-respawn provisioning gap: a
// recycled session reaches neither af up nor af sling, so without this it could relaunch forever
// without the statusLine registration the whole occupancy channel depends on — the observer
// silently missing for the one agent that most needs observing.
//
// EnsureSettings is idempotent by unconditional overwrite, so the file is deleted between cases:
// a leftover from an earlier class would make every later class pass for free.
func TestRecoveryFunnel_ReprovisionsSettingsOnEveryClass(t *testing.T) {
	for _, trigger := range []string{
		triggerContextExhaustion, triggerDarkAtHighOccupancy, triggerProgressBackstop,
		triggerCrash, triggerErrorPattern, triggerCompactHandoff, triggerSelfHandoff,
		triggerStepBoundaryHandoff,
	} {
		t.Run(trigger, func(t *testing.T) {
			root := setupTestFactoryForDone(t, "supervisor")
			agentDir := config.AgentDir(root, "supervisor")
			settings := filepath.Join(agentDir, ".claude", "settings.json")
			if err := os.RemoveAll(filepath.Join(agentDir, ".claude")); err != nil {
				t.Fatal(err)
			}

			if err := respawnSession(RespawnOptions{
				FactoryRoot:  root,
				AgentName:    "supervisor",
				AgentEntry:   config.AgentEntry{Type: "autonomous"},
				PaneID:       "%0",
				AgentWorkDir: agentDir,
				Trigger:      trigger,
				Tx:           &mockTmux{},
			}); err != nil {
				t.Fatalf("respawnSession: %v", err)
			}

			data, err := os.ReadFile(settings)
			if err != nil {
				t.Fatalf("every recycle class must re-provision settings at %s: %v", settings, err)
			}
			var parsed map[string]any
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Errorf("settings.json must be valid JSON: %v", err)
			}
		})
	}
}
