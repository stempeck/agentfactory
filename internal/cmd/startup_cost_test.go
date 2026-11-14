package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// None of these may call t.Parallel: they chdir, set the environment and reassign package seams.

// scPath is startupTurnPath, held to the one location every reader finds from (root, agent) alone.
func scPath(t *testing.T, root, agent string) string {
	t.Helper()
	want := filepath.Join(root, ".runtime", "startup_turn", agent+".json")
	if got := startupTurnPath(root, agent); got != want {
		t.Fatalf("startupTurnPath(root, %q) = %q, want %q", agent, got, want)
	}
	return want
}

func scWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func scAssistant(id string, input, cacheRead int64) string {
	return fmt.Sprintf(`{"type":"assistant","isSidechain":false,"version":"2.1.322","message":{"id":%q,"model":"claude-opus-5","role":"assistant","usage":{"input_tokens":%d,"cache_read_input_tokens":%d,"cache_creation_input_tokens":0,"output_tokens":0}}}`,
		id, input, cacheRead)
}

const (
	scStop     = `{"type":"system","subtype":"stop_hook_summary","isSidechain":false}`
	scDuration = `{"type":"system","subtype":"turn_duration","isSidechain":false}`
	scUser     = `{"type":"user","isSidechain":false,"message":{"role":"user","content":"x"}}`
	scAPIError = `{"type":"assistant","isSidechain":false,"isApiErrorMessage":true,"message":{"id":"msg_err","model":"<synthetic>","usage":{"input_tokens":0,"output_tokens":0,"cache_read_input_tokens":0,"cache_creation_input_tokens":0}}}`
)

func scLines(lines ...string) []byte {
	var b strings.Builder
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	return []byte(b.String())
}

// scFactory is a hermetic factory whose roster holds the two measured roles, with
// CLAUDE_CONFIG_DIR pointed at a temp dir so a missing transcript never resolves under the
// host's ~/.claude.
type scFactory struct {
	root string
	now  time.Time
}

func newScFactory(t *testing.T, telemetryOn bool) *scFactory {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	writeFactoryMarker(t, root)
	writeAgentsJSON(t, root, `{"agents":{"manager":{"type":"interactive","description":"d"},"supervisor":{"type":"autonomous","description":"d"}}}`)
	for _, a := range []string{"manager", "supervisor"} {
		if err := os.MkdirAll(config.AgentDir(root, a), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if telemetryOn {
		gateOn(t, root)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", t.TempDir())
	installMemStore(t)
	return &scFactory{root: root, now: time.Date(2026, 11, 11, 10, 2, 3, 0, time.UTC)}
}

func (f *scFactory) agentDir(agent string) string { return config.AgentDir(f.root, agent) }

// arm writes what af prime --hook leaves for a fresh startup: the marker, the session id and the
// transcript path, which names a transcript in its own temp dir.
func scArm(t *testing.T, agentDir, sid string, transcript []byte) string {
	t.Helper()
	runtimeDir := filepath.Join(agentDir, ".runtime")
	scWrite(t, filepath.Join(runtimeDir, "startup_turn_pending"), []byte(sid))
	scWrite(t, filepath.Join(runtimeDir, "session_id"), []byte(sid))
	path := filepath.Join(t.TempDir(), sid+".jsonl")
	scWrite(t, path, transcript)
	persistTranscriptPath(agentDir, sid, path)
	return path
}

func scAppend(t *testing.T, path string, data []byte) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		t.Fatal(err)
	}
}

func scMarker(agentDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "startup_turn_pending"))
	return string(data), err == nil
}

func scMeasurement(t *testing.T, root, agent string) (startupTurn, bool) {
	t.Helper()
	data, err := os.ReadFile(scPath(t, root, agent))
	if os.IsNotExist(err) {
		return startupTurn{}, false
	}
	if err != nil {
		t.Fatalf("read the measurement: %v", err)
	}
	var turn startupTurn
	if err := json.Unmarshal(data, &turn); err != nil {
		t.Fatalf("the measurement does not decode: %v\n%s", err, data)
	}
	return turn, true
}

func scStartupEnds(t *testing.T, root, agent string) []telemetry.StepEvent {
	t.Helper()
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	var out []telemetry.StepEvent
	for _, r := range records {
		if r.Event == "startup_end" {
			out = append(out, r)
		}
	}
	return out
}

// scRunStartup runs af telemetry startup from dir and returns its stdout and error.
func scRunStartup(t *testing.T, dir, agent string, jsonOut bool) (string, error) {
	t.Helper()
	resetReportFlags(t)
	if agent != "" {
		if err := telemetryCmd.Flags().Set("agent", agent); err != nil {
			t.Fatal(err)
		}
	}
	if jsonOut {
		if err := telemetryCmd.Flags().Set("json", "true"); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(dir)
	var err error
	out := captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"startup"}) })
	return out, err
}

func scHookFixture(t *testing.T, role string) string {
	t.Helper()
	_, agentDir := setupFactoryFixture(t, role)
	installMemStore(t)
	t.Chdir(agentDir)
	t.Setenv("AF_ROLE", "")
	t.Setenv(config.EnvLaunchKind, "")
	return agentDir
}

func TestPrimeHook_StartupWritesMarkerForManagerAndSupervisor(t *testing.T) {
	for _, tc := range []struct {
		role   string
		marked bool
	}{
		{"manager", true},
		{"supervisor", true},
		{"worker", false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			agentDir := scHookFixture(t, tc.role)
			sid := "sess-" + tc.role + "-1"
			primeHookCapturing(t, `{"session_id":"`+sid+`","source":"startup"}`)

			if got := readRuntime(t, agentDir, "session_id"); got != sid {
				t.Fatalf("fixture: the hook did not persist the session id (got %q)", got)
			}
			marker, ok := scMarker(agentDir)
			if tc.marked && (!ok || marker != sid) {
				t.Errorf("a %s startup must arm the marker with its session id: present=%v content=%q", tc.role, ok, marker)
			}
			if !tc.marked && ok {
				t.Errorf("a %s startup is not measured, yet the marker was written: %q", tc.role, marker)
			}
		})
	}
}

func TestPrimeHook_ResumeCompactClearWriteNoMarker(t *testing.T) {
	armed := func(t *testing.T, agentDir, sid string) {
		t.Helper()
		primeHookCapturing(t, `{"session_id":"`+sid+`","source":"startup"}`)
		if marker, ok := scMarker(agentDir); !ok || marker != sid {
			t.Fatalf("fixture: the startup did not arm the marker (present=%v content=%q)", ok, marker)
		}
		if err := os.Remove(filepath.Join(agentDir, ".runtime", "startup_turn_pending")); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct{ name, source, nextID string }{
		{"resume", "resume", "sess-1"},
		{"compact", "compact", "sess-1"},
		{"clear", "clear", "sess-2"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			agentDir := scHookFixture(t, "manager")
			armed(t, agentDir, "sess-1")
			primeHookCapturing(t, `{"session_id":"`+tc.nextID+`","source":"`+tc.source+`"}`)
			if got := readRuntime(t, agentDir, "session_id"); got != tc.nextID {
				t.Fatalf("fixture: session_id = %q, want %q", got, tc.nextID)
			}
			if marker, ok := scMarker(agentDir); ok {
				t.Errorf("a %s session must not arm the marker, got %q", tc.source, marker)
			}
		})
	}

	t.Run("grader", func(t *testing.T) {
		agentDir := scHookFixture(t, "manager")
		primeHookCapturing(t, `{"session_id":"sess-agent","source":"startup"}`)
		markerBefore, ok := scMarker(agentDir)
		if !ok || markerBefore != "sess-agent" {
			t.Fatalf("fixture: the agent's own startup did not arm the marker (present=%v content=%q)", ok, markerBefore)
		}
		sidBefore, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "session_id"))
		if err != nil {
			t.Fatal(err)
		}

		t.Setenv("TMUX_PANE", "")
		orig := primeHookMode
		primeHookMode = true
		t.Cleanup(func() { primeHookMode = orig })
		primeWithHookPayload(t, `{"session_id":"sess-grader","source":"startup"}`)

		if markerAfter, _ := scMarker(agentDir); markerAfter != markerBefore {
			t.Errorf("a grader session re-armed the marker: %q -> %q", markerBefore, markerAfter)
		}
		if sidAfter, _ := os.ReadFile(filepath.Join(agentDir, ".runtime", "session_id")); !bytes.Equal(sidAfter, sidBefore) {
			t.Errorf("a grader session took the session id: %q -> %q", sidBefore, sidAfter)
		}
	})
}

func TestPrimeHook_RespawnLaunchWritesNoMarker(t *testing.T) {
	for _, role := range []string{"manager", "supervisor"} {
		t.Run(role, func(t *testing.T) {
			agentDir := scHookFixture(t, role)

			t.Setenv(config.EnvLaunchKind, "respawn")
			primeHookCapturing(t, `{"session_id":"sess-respawned","source":"startup"}`)
			if got := readRuntime(t, agentDir, "session_id"); got != "sess-respawned" {
				t.Fatalf("fixture: session_id = %q, want sess-respawned", got)
			}
			if marker, ok := scMarker(agentDir); ok {
				t.Errorf("a session a respawn launched armed the marker: %q", marker)
			}

			t.Setenv(config.EnvLaunchKind, "")
			primeHookCapturing(t, `{"session_id":"sess-fresh","source":"startup"}`)
			if marker, ok := scMarker(agentDir); !ok || marker != "sess-fresh" {
				t.Errorf("a fresh launch (AF_LAUNCH_KIND empty) must arm the marker: present=%v content=%q", ok, marker)
			}
		})
	}
}

func TestRespawnSession_LineCarriesLaunchKindRespawn(t *testing.T) {
	fx := newK14Fixture(t)
	fake, _ := setupHermeticSessions(t)
	up := fx.up(t, fake)
	if up.line == "" {
		t.Fatalf("af up sent no launch line (err=%v): %s", up.err, up.output)
	}
	if !strings.Contains(up.line, "AF_LAUNCH_KIND=''") || strings.Contains(up.line, "AF_LAUNCH_KIND='respawn'") {
		t.Errorf("af up's line must clear the launch kind: %s", up.line)
	}

	respawnLine := func(t *testing.T, agent string, entry config.AgentEntry, agentDir string) string {
		t.Helper()
		mock := &mockTmux{}
		var err error
		captureStderr(t, func() {
			err = respawnSession(RespawnOptions{FactoryRoot: fx.root, AgentName: agent, AgentEntry: entry, AgentWorkDir: agentDir, PaneID: "%0", Tx: mock})
		})
		if err != nil || len(mock.respawnPaneCalls) != 1 {
			t.Fatalf("respawnSession(%s): err=%v, RespawnPane calls=%d", agent, err, len(mock.respawnPaneCalls))
		}
		return mock.respawnPaneCalls[0].cmd
	}

	workerDir := config.AgentDir(fx.root, "worker")
	if err := os.MkdirAll(workerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, line := range map[string]string{
		"manager": respawnLine(t, "manager", fx.entry(t), fx.agentDir),
		"worker":  respawnLine(t, "worker", config.AgentEntry{Type: "autonomous", Description: "d"}, workerDir),
	} {
		if n := strings.Count(line, "AF_LAUNCH_KIND='respawn'"); n != 1 {
			t.Errorf("%s: the respawn line carries AF_LAUNCH_KIND='respawn' %d times, want once: %s", name, n, line)
		}
		if strings.Contains(line, "AF_LAUNCH_KIND=''") {
			t.Errorf("%s: the respawn line also clears the mark: %s", name, line)
		}
	}

	_ = filepath.WalkDir(fx.root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && d.Name() == "launch_kind" {
			t.Errorf("a launch_kind file exists at %s; the launch line is the mark's only carrier", path)
		}
		return nil
	})
}

func TestResetAgentState_ClearsStartupMeasurement(t *testing.T) {
	fixture := func(t *testing.T) string {
		t.Helper()
		realDir, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatalf("eval symlinks: %v", err)
		}
		installMemStore(t)
		if err := os.MkdirAll(worktree.WorktreesDir(realDir), 0o755); err != nil {
			t.Fatal(err)
		}
		return realDir
	}

	t.Run("clears the measurement and spares the sibling", func(t *testing.T) {
		root := fixture(t)
		scWrite(t, scPath(t, root, "manager"), []byte(`{"session_id":"s1","tokens":1}`))
		scWrite(t, scPath(t, root, "supervisor"), []byte(`{"session_id":"s2","tokens":2}`))

		var buf bytes.Buffer
		if err := resetAgentState(context.Background(), &buf, root, "manager", "test-reason"); err != nil {
			t.Fatalf("resetAgentState: %v", err)
		}
		if _, err := os.Stat(scPath(t, root, "manager")); !os.IsNotExist(err) {
			t.Errorf("a reset agent must not come back alarmed: its measurement survives (stat err = %v)", err)
		}
		if _, err := os.Stat(scPath(t, root, "supervisor")); err != nil {
			t.Errorf("a sibling's measurement must survive: %v", err)
		}
	})

	t.Run("clears the agent's startup mail latches and spares a sibling's", func(t *testing.T) {
		root := fixture(t)
		scWrite(t, scPath(t, root, "manager"), []byte(`{"session_id":"s1","tokens":1}`))
		latches := t19PlantLatches(t, root)

		var buf bytes.Buffer
		if err := resetAgentState(context.Background(), &buf, root, "manager", "test-reason"); err != nil {
			t.Fatalf("resetAgentState: %v", err)
		}
		t19WantLatches(t, latches)
	})

	t.Run("an absent measurement does not fail the reset", func(t *testing.T) {
		root := fixture(t)
		var buf bytes.Buffer
		var err error
		stderr := captureStderr(t, func() { err = resetAgentState(context.Background(), &buf, root, "manager", "test-reason") })
		if err != nil {
			t.Fatalf("resetAgentState with no measurement: %v", err)
		}
		if strings.Contains(stderr, "warning") {
			t.Errorf("an absent measurement is not a warning: %q", stderr)
		}
		if _, err := os.Stat(scPath(t, root, "manager")); !os.IsNotExist(err) {
			t.Errorf("the reset created a measurement (stat err = %v)", err)
		}
	})
}

func TestRecoveryReset_ClearsStartupMeasurement(t *testing.T) {
	const cleared = "recovery reset manager: cleared the startup measurement (STARTUP alarm)\n"
	const noBreaker = "recovery reset manager: no breaker state to clear\n"

	t.Run("clears the measurement with no breaker", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)
		scWrite(t, scPath(t, root, "manager"), []byte(`{"session_id":"s1","tokens":1}`))

		out, _, err := invokeRecoveryReset(t, "manager")
		if err != nil {
			t.Fatalf("runRecoveryReset: %v", err)
		}
		if _, err := os.Stat(scPath(t, root, "manager")); !os.IsNotExist(err) {
			t.Errorf("the measurement must be removed (stat err = %v)", err)
		}
		if !strings.Contains(out, cleared) {
			t.Errorf("the reset must say it cleared the measurement; got %q", out)
		}
		if !strings.Contains(out, noBreaker) {
			t.Errorf("the breaker half must still run and say there was no breaker; got %q", out)
		}
	})

	t.Run("spares the sibling", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)
		scWrite(t, scPath(t, root, "manager"), []byte(`{"session_id":"s1","tokens":1}`))
		scWrite(t, scPath(t, root, "supervisor"), []byte(`{"session_id":"s2","tokens":2}`))

		if _, _, err := invokeRecoveryReset(t, "manager"); err != nil {
			t.Fatalf("runRecoveryReset: %v", err)
		}
		if _, err := os.Stat(scPath(t, root, "manager")); !os.IsNotExist(err) {
			t.Errorf("the measurement must be removed (stat err = %v)", err)
		}
		if _, err := os.Stat(scPath(t, root, "supervisor")); err != nil {
			t.Errorf("the supervisor's measurement must survive a reset of manager: %v", err)
		}
	})

	t.Run("clears the agent's startup mail latches and spares a sibling's", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)
		scWrite(t, scPath(t, root, "manager"), []byte(`{"session_id":"s1","tokens":1}`))
		latches := t19PlantLatches(t, root)

		out, _, err := invokeRecoveryReset(t, "manager")
		if err != nil {
			t.Fatalf("runRecoveryReset: %v", err)
		}
		if !strings.Contains(out, cleared) {
			t.Errorf("the reset must still say it cleared the measurement; got %q", out)
		}
		t19WantLatches(t, latches)
	})

	t.Run("an absent measurement adds no line", func(t *testing.T) {
		root := setupRecoveryResetFactory(t)
		installFakeTmuxPresent(t)

		out, _, err := invokeRecoveryReset(t, "manager")
		if err != nil {
			t.Fatalf("runRecoveryReset: %v", err)
		}
		if out != noBreaker {
			t.Errorf("with no measurement and no breaker the output must be today's, byte for byte:\n got:  %q\n want: %q", out, noBreaker)
		}
		if _, err := os.Stat(scPath(t, root, "manager")); !os.IsNotExist(err) {
			t.Errorf("the reset created a measurement (stat err = %v)", err)
		}
	})
}

// t19Latches are the startup mail latches a reset of manager must remove, and the ones it must
// leave: another agent's, including two whose names start with "manager".
type t19Latches struct{ gone, kept []string }

func t19PlantLatches(t *testing.T, root string) t19Latches {
	t.Helper()
	l := t19Latches{
		gone: []string{
			startupSignalLatchPath(root, "manager", "2.1.322"),
			startupSignalLatchPath(root, "manager", "2.1.330"),
		},
		kept: []string{
			startupSignalLatchPath(root, "supervisor", "2.1.322"),
			startupSignalLatchPath(root, "manager2", "2.1.322"),
			startupSignalLatchPath(root, "manager-2", "2.1.322"),
		},
	}
	for _, p := range append(append([]string{}, l.gone...), l.kept...) {
		scWrite(t, p, nil)
	}
	return l
}

func t19WantLatches(t *testing.T, l t19Latches) {
	t.Helper()
	for _, p := range l.gone {
		if _, err := os.Lstat(p); !os.IsNotExist(err) {
			t.Errorf("a reset ends the alarm episode, so the agent's mail latch %s must go (lstat err = %v)", filepath.Base(p), err)
		}
	}
	for _, p := range l.kept {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("another agent's mail latch %s must survive a reset of manager: %v", filepath.Base(p), err)
		}
	}
}

func scWantMeasured(t *testing.T, f *scFactory, agent, sid string, tokens int64) startupTurn {
	t.Helper()
	turn, ok := scMeasurement(t, f.root, agent)
	if !ok {
		t.Fatalf("no measurement for %s at %s", agent, scPath(t, f.root, agent))
	}
	if turn.SessionID != sid || turn.Tokens != tokens || turn.Budget != startupTurnBudgetTokens || turn.HarnessVersion != "2.1.322" {
		t.Errorf("measurement = %+v, want session %s, tokens %d, budget %d, harness 2.1.322", turn, sid, tokens, startupTurnBudgetTokens)
	}
	if _, err := time.Parse(time.RFC3339, turn.At); err != nil {
		t.Errorf("measurement at = %q, want RFC 3339: %v", turn.At, err)
	}
	return turn
}

func TestStartupCost_MeasuresPendingSessionOnce(t *testing.T) {
	transcript := scLines(scUser, scAssistant("msg_1", 1000, 50000), scStop, scUser, scAssistant("msg_2", 1000, 90000))
	const tokens = 51000

	for _, tc := range []struct {
		name  string
		first func(t *testing.T, f *scFactory)
		then  func(t *testing.T, f *scFactory)
	}{
		{"tick first",
			func(t *testing.T, f *scFactory) { pollStartupSignal(f.root, f.now) },
			func(t *testing.T, f *scFactory) {
				if _, err := scRunStartup(t, f.agentDir("manager"), "", false); err != nil {
					t.Fatalf("af telemetry startup: %v", err)
				}
			}},
		{"af telemetry startup first",
			func(t *testing.T, f *scFactory) {
				if _, err := scRunStartup(t, f.agentDir("manager"), "", false); err != nil {
					t.Fatalf("af telemetry startup: %v", err)
				}
			},
			func(t *testing.T, f *scFactory) { pollStartupSignal(f.root, f.now) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newScFactory(t, true)
			(&mailRecorder{}).install(t)
			installFakeTmuxPresent(t, session.SessionName("supervisor"))
			agentDir := f.agentDir("manager")
			scArm(t, agentDir, "sess-once", transcript)

			tc.first(t, f)
			scWantMeasured(t, f, "manager", "sess-once", tokens)
			if _, ok := scMarker(agentDir); ok {
				t.Error("the marker must be consumed by the caller that measured")
			}
			if _, err := os.Lstat(filepath.Join(agentDir, ".runtime", "startup_turn_claimed")); !os.IsNotExist(err) {
				t.Errorf("the claimed file must be removed after the write (lstat err = %v)", err)
			}
			if n := len(scStartupEnds(t, f.root, "manager")); n != 1 {
				t.Fatalf("startup_end records after the first caller = %d, want 1", n)
			}
			before, _ := os.ReadFile(scPath(t, f.root, "manager"))

			tc.then(t, f)
			if after, _ := os.ReadFile(scPath(t, f.root, "manager")); !bytes.Equal(before, after) {
				t.Errorf("the second caller rewrote the measurement:\n before: %s\n after:  %s", before, after)
			}
			if n := len(scStartupEnds(t, f.root, "manager")); n != 1 {
				t.Errorf("startup_end records after the second caller = %d, want still 1", n)
			}
		})
	}

	t.Run("a marker for another session is measured by neither caller", func(t *testing.T) {
		f := newScFactory(t, true)
		(&mailRecorder{}).install(t)
		installFakeTmuxPresent(t, session.SessionName("supervisor"))
		agentDir := f.agentDir("manager")
		scArm(t, agentDir, "sess-new", transcript)
		scWrite(t, filepath.Join(agentDir, ".runtime", "startup_turn_pending"), []byte("sess-old"))

		pollStartupSignal(f.root, f.now)
		if _, err := scRunStartup(t, agentDir, "", false); err != nil {
			t.Fatalf("af telemetry startup: %v", err)
		}
		if turn, ok := scMeasurement(t, f.root, "manager"); ok {
			t.Errorf("a marker naming another session was measured: %+v", turn)
		}
		if marker, _ := scMarker(agentDir); marker != "sess-old" {
			t.Errorf("the marker must be left where it is, got %q", marker)
		}
	})
}

func TestStartupCost_NoFirstStopYetKeepsMarker(t *testing.T) {
	f := newScFactory(t, false)
	agentDir := f.agentDir("manager")
	path := scArm(t, agentDir, "sess-running", scLines(scUser, scAssistant("msg_1", 2000, 30000)))

	for i := 0; i < 2; i++ {
		measureStartupTurn(f.root, "manager")
		if turn, ok := scMeasurement(t, f.root, "manager"); ok {
			t.Fatalf("call %d: a startup turn with no Stop yet was measured: %+v", i+1, turn)
		}
		if marker, ok := scMarker(agentDir); !ok || marker != "sess-running" {
			t.Fatalf("call %d: the marker must stay (present=%v content=%q)", i+1, ok, marker)
		}
	}

	scAppend(t, path, scLines(scDuration, scUser, scAssistant("msg_2", 2000, 80000)))
	measureStartupTurn(f.root, "manager")
	scWantMeasured(t, f, "manager", "sess-running", 32000)
	if _, ok := scMarker(agentDir); ok {
		t.Error("the marker must be consumed once the turn is measured")
	}
}

func TestStartupCost_ClaimIsExclusiveAndGivenBack(t *testing.T) {
	markerOf := func(dir string) string { return filepath.Join(dir, ".runtime", "startup_turn_pending") }
	claimedOf := func(dir string) string { return filepath.Join(dir, ".runtime", "startup_turn_claimed") }

	t.Run("only the first of two claims wins", func(t *testing.T) {
		dir := t.TempDir()
		scWrite(t, markerOf(dir), []byte("sess-a"))
		if !claimStartupTurn(dir, "sess-a") {
			t.Fatal("the first claim of a pending session must win")
		}
		if claimStartupTurn(dir, "sess-a") {
			t.Error("a second claim of the same session must lose")
		}
	})

	t.Run("a marker for another session is given back", func(t *testing.T) {
		dir := t.TempDir()
		scWrite(t, markerOf(dir), []byte("sess-other"))
		if claimStartupTurn(dir, "sess-mine") {
			t.Fatal("a claim must not win a marker that names another session")
		}
		if data, err := os.ReadFile(markerOf(dir)); err != nil || string(data) != "sess-other" {
			t.Errorf("the marker must be given back as startup_turn_pending holding sess-other: %q, %v", data, err)
		}
		if _, err := os.Lstat(claimedOf(dir)); !os.IsNotExist(err) {
			t.Errorf("the claimed file must be gone after the give-back (lstat err = %v)", err)
		}
	})

	// The newer marker has to land between the claim's rename and its give-back, a window only a
	// concurrent writer can hit. Every interleaving must leave a newer marker in place byte for
	// byte; a give-back by rename replaces it whenever the writer lands inside the window.
	t.Run("a newer marker written meanwhile stays", func(t *testing.T) {
		for i := 0; i < 300; i++ {
			dir := t.TempDir()
			scWrite(t, markerOf(dir), []byte("sess-old"))
			var stop, created atomic.Bool
			done := make(chan struct{})
			go func() {
				defer close(done)
				for !stop.Load() {
					f, err := os.OpenFile(markerOf(dir), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
					if err == nil {
						f.WriteString("sess-newer")
						f.Close()
						created.Store(true)
						return
					}
				}
			}()
			won := claimStartupTurn(dir, "sess-mine")
			stop.Store(true)
			<-done

			if won {
				t.Fatalf("iteration %d: a claim won a marker that never named its session", i)
			}
			want := "sess-old"
			if created.Load() {
				want = "sess-newer"
			}
			if data, _ := os.ReadFile(markerOf(dir)); string(data) != want {
				t.Fatalf("iteration %d: marker = %q, want %q (the newer marker was written: %v)", i, data, want, created.Load())
			}
			if _, err := os.Lstat(claimedOf(dir)); !os.IsNotExist(err) {
				t.Fatalf("iteration %d: the claimed file must be gone (lstat err = %v)", i, err)
			}
		}
	})
}

func TestStartupCost_MalformedTranscriptWritesNothing(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, agentDir string)
	}{
		{"malformed", func(t *testing.T, agentDir string) {
			scArm(t, agentDir, "sess-bad", scLines("{not json", `{"type":`, scStop))
		}},
		{"absent", func(t *testing.T, agentDir string) {
			path := scArm(t, agentDir, "sess-gone", scLines(scAssistant("msg_1", 10, 10), scStop))
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tc.name+" transcript", func(t *testing.T) {
			f := newScFactory(t, false)
			agentDir := f.agentDir("manager")
			tc.plant(t, agentDir)
			markerBefore, _ := scMarker(agentDir)

			measureStartupTurn(f.root, "manager")
			if turn, ok := scMeasurement(t, f.root, "manager"); ok {
				t.Errorf("a %s transcript was measured: %+v", tc.name, turn)
			}
			if marker, ok := scMarker(agentDir); !ok || marker != markerBefore {
				t.Errorf("the marker must stay (present=%v content=%q)", ok, marker)
			}
		})
	}

	t.Run("the tick goes on to the next agent", func(t *testing.T) {
		f := newScFactory(t, false)
		(&mailRecorder{}).install(t)
		installFakeTmuxPresent(t, session.SessionName("manager"), session.SessionName("supervisor"))
		scArm(t, f.agentDir("manager"), "sess-bad", scLines("{not json", scStop))
		scArm(t, f.agentDir("supervisor"), "sess-good", scLines(scAssistant("msg_1", 100, 400), scStop))

		pollStartupSignal(f.root, f.now)
		if _, ok := scMeasurement(t, f.root, "manager"); ok {
			t.Error("the malformed manager transcript was measured")
		}
		scWantMeasured(t, f, "supervisor", "sess-good", 500)
	})

	t.Run("a first Stop with no usage before it waits for the next turn", func(t *testing.T) {
		f := newScFactory(t, false)
		agentDir := f.agentDir("manager")
		path := scArm(t, agentDir, "sess-err", scLines(scUser, scAPIError, scDuration))

		measureStartupTurn(f.root, "manager")
		if turn, ok := scMeasurement(t, f.root, "manager"); ok {
			t.Fatalf("an API-error-only first turn was measured: %+v", turn)
		}
		if marker, ok := scMarker(agentDir); !ok || marker != "sess-err" {
			t.Fatalf("the marker must stay (present=%v content=%q)", ok, marker)
		}

		scAppend(t, path, scLines(scUser, scAssistant("msg_2", 700, 7000), scStop))
		measureStartupTurn(f.root, "manager")
		scWantMeasured(t, f, "manager", "sess-err", 7700)
	})
}

// scWithin runs fn under a deadline and fails the test if it blocks; on a timeout it opens every
// FIFO for writing so the stuck reader returns.
func scWithin(t *testing.T, fifos []string, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() { defer close(done); fn() }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		for _, p := range fifos {
			if w, err := os.OpenFile(p, os.O_WRONLY|syscall.O_NONBLOCK, 0); err == nil {
				w.Close()
			}
		}
		<-done
		t.Fatal("the measurement blocked on a non-regular file")
	}
}

func TestStartupCost_AgentDirReadsUnderTheOpenRule(t *testing.T) {
	transcript := scLines(scAssistant("msg_1", 1000, 9000), scStop)
	const sid = "sess-open-rule"

	type target struct {
		name    string
		path    func(f *scFactory, agentDir, transcriptPath string) string
		content func(agentDir, transcriptPath string) []byte
		writes  bool
	}
	targets := []target{
		{"marker",
			func(_ *scFactory, d, _ string) string { return filepath.Join(d, ".runtime", "startup_turn_pending") },
			func(string, string) []byte { return []byte(sid) }, false},
		{"session_id",
			func(_ *scFactory, d, _ string) string { return filepath.Join(d, ".runtime", "session_id") },
			func(string, string) []byte { return []byte(sid) }, false},
		{"transcript_path",
			func(_ *scFactory, d, _ string) string { return filepath.Join(d, ".runtime", "transcript_path") },
			func(_ string, tp string) []byte { return []byte(sid + "\t" + tp) }, false},
		{"memory_injected.json",
			func(_ *scFactory, d, _ string) string { return filepath.Join(d, ".runtime", "memory_injected.json") },
			func(string, string) []byte {
				return []byte(`{"sessions":{"` + sid + `":{"pins_rendered":3,"pins_total":3,"updated":"2026-11-11T10:00:00Z"}}}`)
			}, true},
		{"transcript",
			func(_ *scFactory, _ string, tp string) string { return tp },
			func(string, string) []byte { return transcript }, false},
	}

	for _, tg := range targets {
		for _, kind := range []string{"fifo", "symlink"} {
			t.Run(tg.name+"/"+kind, func(t *testing.T) {
				f := newScFactory(t, false)
				agentDir := f.agentDir("manager")
				tp := scArm(t, agentDir, sid, transcript)
				path := tg.path(f, agentDir, tp)
				var fifos []string
				switch kind {
				case "fifo":
					_ = os.Remove(path)
					if err := syscall.Mkfifo(path, 0o644); err != nil {
						t.Skipf("mkfifo: %v", err)
					}
					fifos = append(fifos, path)
					if tg.name == "transcript" {
						// The persisted path skips a non-regular file, so the FIFO also sits at
						// the derived path the lookup falls back to.
						derived := filepath.Join(claudeConfigDir(), "projects", transcriptDirSlug.Replace(agentDir), sid+".jsonl")
						if err := os.MkdirAll(filepath.Dir(derived), 0o755); err != nil {
							t.Fatal(err)
						}
						if err := syscall.Mkfifo(derived, 0o644); err != nil {
							t.Skipf("mkfifo: %v", err)
						}
						fifos = append(fifos, derived)
					}
				case "symlink":
					realFile := filepath.Join(t.TempDir(), "real")
					scWrite(t, realFile, tg.content(agentDir, tp))
					_ = os.Remove(path)
					if err := os.Symlink(realFile, path); err != nil {
						t.Fatal(err)
					}
				}

				scWithin(t, fifos, func() { measureStartupTurn(f.root, "manager") })

				turn, measured := scMeasurement(t, f.root, "manager")
				if !tg.writes {
					if measured {
						t.Errorf("a %s %s was read through: measured %+v", kind, tg.name, turn)
					}
					if _, err := os.Lstat(filepath.Join(agentDir, ".runtime", "startup_turn_pending")); err != nil {
						t.Errorf("the marker must stay in place: %v", err)
					}
					return
				}
				if !measured {
					t.Fatalf("a %s memory_injected.json is no record, not a stop: nothing was measured", kind)
				}
				if turn.RulingsBlock != "absent" {
					t.Errorf("a %s memory_injected.json must read as no record: rulings_block = %q, want absent", kind, turn.RulingsBlock)
				}
			})
		}
	}

	for name, id := range map[string]string{
		"path-shaped id":  "../x",
		"a 129-byte id":   strings.Repeat("a", 129),
		"an id with a NL": "sess\nx",
	} {
		t.Run(name, func(t *testing.T) {
			f := newScFactory(t, false)
			agentDir := f.agentDir("manager")
			scArm(t, agentDir, "sess-placeholder", transcript)
			scWrite(t, filepath.Join(agentDir, ".runtime", "startup_turn_pending"), []byte(id))
			scWrite(t, filepath.Join(agentDir, ".runtime", "session_id"), []byte(id))
			tp := filepath.Join(t.TempDir(), "t.jsonl")
			scWrite(t, tp, transcript)
			scWrite(t, filepath.Join(agentDir, ".runtime", "transcript_path"), []byte(id+"\t"+tp))

			measureStartupTurn(f.root, "manager")
			if turn, ok := scMeasurement(t, f.root, "manager"); ok {
				t.Errorf("session id %q was measured: %+v", id, turn)
			}
		})
	}

	t.Run("rulings_block follows the injection record", scRulingsBlockCases)

	t.Run("a regular memory_injected.json is read", func(t *testing.T) {
		f := newScFactory(t, false)
		agentDir := f.agentDir("manager")
		scArm(t, agentDir, sid, transcript)
		scWrite(t, filepath.Join(agentDir, ".runtime", "memory_injected.json"),
			[]byte(`{"sessions":{"`+sid+`":{"pins_rendered":3,"pins_total":3,"updated":"2026-11-11T10:00:00Z"}}}`))
		measureStartupTurn(f.root, "manager")
		if turn, ok := scMeasurement(t, f.root, "manager"); !ok || turn.RulingsBlock != "present" {
			t.Errorf("a regular injection record of 3/3 pins must read present: %+v (measured %v)", turn, ok)
		}
	})
}

func scRulingsBlockCases(t *testing.T) {
	for _, tc := range []struct {
		record string
		want   string
	}{
		{"", "absent"},
		{`{"sessions":{"sess-r":{"pins_rendered":0,"pins_total":0}}}`, "no_pins"},
		{`{"sessions":{"sess-r":{"pins_rendered":1,"pins_total":3}}}`, "partial"},
		{`{"sessions":{"sess-r":{"pins_rendered":3,"pins_total":3}}}`, "present"},
		{`{"sessions":{"sess-other":{"pins_rendered":3,"pins_total":3}}}`, "absent"},
	} {
		t.Run(tc.want+"/"+fmt.Sprint(len(tc.record)), func(t *testing.T) {
			f := newScFactory(t, false)
			agentDir := f.agentDir("manager")
			scArm(t, agentDir, "sess-r", scLines(scAssistant("msg_1", 10, 90), scStop))
			if tc.record != "" {
				scWrite(t, filepath.Join(agentDir, ".runtime", "memory_injected.json"), []byte(tc.record))
			}
			measureStartupTurn(f.root, "manager")
			turn, ok := scMeasurement(t, f.root, "manager")
			if !ok || turn.RulingsBlock != tc.want {
				t.Errorf("rulings_block = %q (measured %v), want %q", turn.RulingsBlock, ok, tc.want)
			}
		})
	}
}

// t14ScanCap is the bound D22 sets on the startup transcript scan: about 12 times the latest first
// Stop measured across this host's manager and supervisor transcripts.
const t14ScanCap = 16 << 20

// t14Transcript is firstTurn followed by usage-bearing turns that never reach a Stop record, grown
// to at least size bytes.
func t14Transcript(firstTurn []byte, size int) []byte {
	var b bytes.Buffer
	b.Write(firstTurn)
	for i := 0; b.Len() < size; i++ {
		b.WriteString(scUser + "\n")
		b.WriteString(scAssistant(fmt.Sprintf("msg_%d", i), 1000, 20000) + "\n")
	}
	return b.Bytes()
}

func t14WarningLines(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, "warning:") {
			out = append(out, l)
		}
	}
	return out
}

func t14MarkerRetired(t *testing.T, agentDir string) {
	t.Helper()
	if marker, ok := scMarker(agentDir); ok {
		t.Errorf("the marker of an uncut transcript past the cap still stands (%q), so every tick decodes it again", marker)
	}
	if _, err := os.Lstat(filepath.Join(agentDir, ".runtime", "startup_turn_claimed")); !os.IsNotExist(err) {
		t.Errorf("the retired marker must not be left behind as startup_turn_claimed (lstat err = %v)", err)
	}
}

func TestStartupCost_UncutTranscriptPastTheCapIsNotRescanned(t *testing.T) {
	past := t14Transcript(nil, t14ScanCap+256<<10)
	under := t14Transcript(nil, t14ScanCap-1<<20)
	if len(past) <= t14ScanCap || len(under) >= t14ScanCap {
		t.Fatalf("fixture: past = %d B, under = %d B, cap %d B", len(past), len(under), t14ScanCap)
	}

	t.Run("measureStartupTurn retires the marker and writes nothing", func(t *testing.T) {
		f := newScFactory(t, true)
		agentDir := f.agentDir("manager")
		scArm(t, agentDir, "sess-big", past)

		captureStderr(t, func() { measureStartupTurn(f.root, "manager") })
		t14MarkerRetired(t, agentDir)
		if turn, ok := scMeasurement(t, f.root, "manager"); ok {
			t.Errorf("a transcript with no first Stop inside the cap was measured: %+v", turn)
		}
		if n := len(scStartupEnds(t, f.root, "manager")); n != 0 {
			t.Errorf("startup_end records = %d, want 0: nothing was measured", n)
		}
	})

	t.Run("af telemetry startup warns once and keeps the latest figure", func(t *testing.T) {
		f := newScFactory(t, false)
		agentDir := f.agentDir("manager")
		latest := []byte(`{"session_id":"sess-old","tokens":80000,"budget":90000,"harness_version":"2.1.322","rulings_block":"present","at":"2026-11-10T09:00:00Z"}` + "\n")
		scWrite(t, scPath(t, f.root, "manager"), latest)
		scArm(t, agentDir, "sess-big", past)

		// The warning may be written to os.Stderr or to the command's error writer; both are read.
		var cmdErr bytes.Buffer
		telemetryCmd.SetErr(&cmdErr)
		t.Cleanup(func() { telemetryCmd.SetErr(nil) })
		run := func() (stdout, stderr string) {
			t.Helper()
			cmdErr.Reset()
			var err error
			stderr = captureStderr(t, func() { stdout, err = scRunStartup(t, f.root, "", false) })
			if err != nil {
				t.Fatalf("af telemetry startup must exit 0: %v", err)
			}
			return stdout, stderr + cmdErr.String()
		}

		stdout, stderr := run()
		if lines := t14WarningLines(stderr); len(lines) != 1 || !strings.Contains(lines[0], "manager") {
			t.Errorf("stderr carries %d warning: line(s), want 1 naming manager:\n%s", len(lines), stderr)
		}
		if strings.Contains(stdout, "warning:") {
			t.Errorf("the warning belongs on stderr, not in the report:\n%s", stdout)
		}
		t14MarkerRetired(t, agentDir)
		if got, _ := os.ReadFile(scPath(t, f.root, "manager")); !bytes.Equal(got, latest) {
			t.Errorf("the latest figure must stay byte for byte:\n got:  %s\n want: %s", got, latest)
		}
		if !strings.Contains(stdout, "manager: 80000 tokens at the first Stop") {
			t.Errorf("the report must still show the latest figure:\n%s", stdout)
		}

		_, stderr = run()
		if lines := t14WarningLines(stderr); len(lines) != 0 {
			t.Errorf("a retired marker is not scanned again, so the next read warns nothing:\n%s", stderr)
		}
	})

	t.Run("a first Stop before the cap is measured however long the session runs", func(t *testing.T) {
		f := newScFactory(t, false)
		agentDir := f.agentDir("manager")
		scArm(t, agentDir, "sess-long", t14Transcript(scLines(scUser, scAssistant("msg_first", 2000, 40000), scStop), t14ScanCap+256<<10))

		measureStartupTurn(f.root, "manager")
		scWantMeasured(t, f, "manager", "sess-long", 42000)
		if _, ok := scMarker(agentDir); ok {
			t.Error("the marker must be consumed once the turn is measured")
		}
	})

	t.Run("an uncut transcript under the cap keeps its marker", func(t *testing.T) {
		f := newScFactory(t, false)
		agentDir := f.agentDir("manager")
		scArm(t, agentDir, "sess-running", under)

		stderr := captureStderr(t, func() { measureStartupTurn(f.root, "manager") })
		if marker, ok := scMarker(agentDir); !ok || marker != "sess-running" {
			t.Errorf("a startup turn under the cap with no Stop yet must keep its marker (present=%v content=%q)", ok, marker)
		}
		if turn, ok := scMeasurement(t, f.root, "manager"); ok {
			t.Errorf("a startup turn with no Stop yet was measured: %+v", turn)
		}
		if lines := t14WarningLines(stderr); len(lines) != 0 {
			t.Errorf("a turn under the cap is not retired, so nothing warns:\n%s", stderr)
		}
	})
}

// TestStartupHarnessToken holds the startup half to the window half's harness-version grammar
// (PR #756 T23): the characters [0-9A-Za-z.+-], 1 to 32 of them, matched against the whole string,
// with "unknown" for anything else. No test compares the two halves; C-6 keeps them apart.
func TestStartupHarnessToken(t *testing.T) {
	for _, tc := range []struct {
		name, version, want string
	}{
		{"a release", "2.1.322", "2.1.322"},
		{"a semver build suffix", "1.0.0-beta+build.7", "1.0.0-beta+build.7"},
		{"an underscore", "2.1.322_b", startupUnknown},
		{"32 characters", strings.Repeat("9", 32), strings.Repeat("9", 32)},
		{"33 characters", strings.Repeat("9", 33), startupUnknown},
		{"64 characters", strings.Repeat("9", 64), startupUnknown},
		{"empty", "", startupUnknown},
		{"transcript text with spaces", "CANARY zq7 transcript text", startupUnknown},
		{"a path separator", "2.1.322/x", startupUnknown},
		{"a control rune", "2.1.322\x1b", startupUnknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := startupHarnessToken(tc.version); got != tc.want {
				t.Errorf("startupHarnessToken(%q) = %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}
