package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/session"
)

// None of these may call t.Parallel: they reassign package seams (the mail seams, newCmdTmux).

const ssLineSuffix = " Clears when this agent's next measured startup is within budget with its rulings block delivered, or run 'af recovery reset manager' if the agent is gone for good."

func ssTurn(tokens, budget int64, harness, rulings string) map[string]any {
	return map[string]any{"session_id": "5f0c2a9e-3b1d-4c7e-9a8f-1e2d3c4b5a69", "tokens": tokens, "budget": budget,
		"harness_version": harness, "rulings_block": rulings, "at": "2026-11-11T10:02:03Z"}
}

func ssPlant(t *testing.T, root, agent string, turn map[string]any) {
	t.Helper()
	data, err := json.Marshal(turn)
	if err != nil {
		t.Fatal(err)
	}
	scWrite(t, scPath(t, root, agent), data)
}

// ssFactory is the roster plus a live session for each named recipient, so escalateOperator finds
// them reachable; the mail seams are captured.
func ssFactory(t *testing.T, live ...string) (*scFactory, *mailRecorder) {
	t.Helper()
	f := newScFactory(t, false)
	var sessions []string
	for _, a := range live {
		sessions = append(sessions, session.SessionName(a))
	}
	installFakeTmuxPresent(t, sessions...)
	return f, (&mailRecorder{}).install(t)
}

func ssStartupAlarms(t *testing.T, root string, now time.Time) []recoveryAlarm {
	t.Helper()
	raised, err := recoveryAlarms(root, now)
	if err != nil {
		t.Fatalf("recoveryAlarms: %v", err)
	}
	var out []recoveryAlarm
	for _, a := range raised {
		if a.class == "STARTUP" {
			out = append(out, a)
		}
	}
	return out
}

func TestStartupAlarm_RaisedFromStartupTurnFile(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC)

	t.Run("over budget raises STARTUP", func(t *testing.T) {
		root := t.TempDir()
		ssPlant(t, root, "manager", ssTurn(104233, 90000, "2.1.322", "present"))
		got := ssStartupAlarms(t, root, now)
		if len(got) != 1 || got[0].agent != "manager" || got[0].reason != "" ||
			got[0].detail != "startup turn 104233 > 90000 tokens (harness 2.1.322)." {
			t.Errorf("alarms = %+v, want one STARTUP manager with detail %q", got, "startup turn 104233 > 90000 tokens (harness 2.1.322).")
		}
	})

	t.Run("rulings block absent or partial raises STARTUP", func(t *testing.T) {
		for _, tc := range []struct {
			turn   map[string]any
			detail string
		}{
			{ssTurn(1000, 90000, "2.1.322", "absent"), "startup turn rulings block absent (harness 2.1.322)."},
			{ssTurn(1000, 90000, "2.1.322", "partial"), "startup turn rulings block partial (harness 2.1.322)."},
			{ssTurn(104233, 90000, "2.1.322", "partial"), "startup turn 104233 > 90000 tokens, rulings block partial (harness 2.1.322)."},
		} {
			root := t.TempDir()
			ssPlant(t, root, "supervisor", tc.turn)
			got := ssStartupAlarms(t, root, now)
			if len(got) != 1 || got[0].agent != "supervisor" || got[0].detail != tc.detail {
				t.Errorf("alarms = %+v, want one STARTUP supervisor with detail %q", got, tc.detail)
			}
		}
	})

	t.Run("within budget with rulings delivered raises nothing", func(t *testing.T) {
		for _, turn := range []map[string]any{
			ssTurn(90000, 90000, "2.1.322", "present"),
			ssTurn(1000, 90000, "2.1.322", "no_pins"),
			ssTurn(500000, 0, "2.1.322", "present"),
			ssTurn(1000, 90000, "2.1.322", "something-new"),
		} {
			root := t.TempDir()
			ssPlant(t, root, "manager", turn)
			if got := ssStartupAlarms(t, root, now); len(got) != 0 {
				t.Errorf("measurement %v raised %+v, want nothing", turn, got)
			}
		}
	})

	t.Run("absent oversized or malformed file raises nothing", func(t *testing.T) {
		root := t.TempDir()
		if got := ssStartupAlarms(t, root, now); len(got) != 0 {
			t.Errorf("no file raised %+v", got)
		}
		over := ssTurn(104233, 90000, "2.1.322", "absent")
		over["padding"] = strings.Repeat("x", 5<<10)
		ssPlant(t, root, "manager", over)
		scWrite(t, scPath(t, root, "supervisor"), []byte(`{"tokens":104233,"budget":90000,`))
		if got := ssStartupAlarms(t, root, now); len(got) != 0 {
			t.Errorf("an oversized and a malformed measurement raised %+v", got)
		}
	})

	t.Run("symlinked or non-regular file raises nothing", func(t *testing.T) {
		root := t.TempDir()
		over := ssTurn(104233, 90000, "2.1.322", "absent")
		over["padding"] = strings.Repeat("x", 5<<10)
		data, _ := json.Marshal(over)
		target := filepath.Join(t.TempDir(), "big.json")
		scWrite(t, target, data)
		link := scPath(t, root, "manager")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		fifo := scPath(t, root, "supervisor")
		if err := syscall.Mkfifo(fifo, 0o644); err != nil {
			t.Skipf("mkfifo: %v", err)
		}
		var got []recoveryAlarm
		scWithin(t, []string{fifo}, func() { got = startupAlarms(root) })
		if len(got) != 0 {
			t.Errorf("a symlink and a FIFO raised %+v", got)
		}
	})

	t.Run("pending marker alone raises nothing", func(t *testing.T) {
		f := newScFactory(t, false)
		scArm(t, f.agentDir("manager"), "sess-pending", scLines(scAssistant("msg_1", 100000, 100000), scStop))
		if got := ssStartupAlarms(t, f.root, now); len(got) != 0 {
			t.Errorf("an unmeasured pending startup raised %+v", got)
		}
	})
}

func TestStartupAlarm_OneReadThreeRenderings(t *testing.T) {
	root, now := t.TempDir(), time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC)
	ssPlant(t, root, "manager", ssTurn(104233, 90000, "2.1.322", "partial"))

	note := recoveryAlarmNote(root, now)
	if note != "⚠ STARTUP manager" {
		t.Errorf("pane token = %q, want %q", note, "⚠ STARTUP manager")
	}
	for _, leaked := range []string{"104233", "90000", "2.1.322", "partial"} {
		if strings.Contains(note, leaked) {
			t.Errorf("the pane token carries %q: %q", leaked, note)
		}
	}
	const line = "STARTUP manager — startup turn 104233 > 90000 tokens, rulings block partial (harness 2.1.322)." + ssLineSuffix
	for name, alerts := range map[string]string{
		"recoveryAlertsNote":       recoveryAlertsNote(root, now),
		"recoveryLaunchAlertsNote": recoveryLaunchAlertsNote(root, now),
	} {
		if !strings.Contains(alerts, "  "+line+"\n") {
			t.Errorf("%s lacks the alarm line\n %s\ngot:\n%s", name, line, alerts)
		}
	}
}

func TestStartupAlarm_RanksAfterWindowAheadOfWdog(t *testing.T) {
	now := time.Date(2026, 11, 11, 11, 0, 0, 0, time.UTC)

	t.Run("a halted breaker leads ahead of STARTUP and WDOG", func(t *testing.T) {
		root := t.TempDir()
		ssPlant(t, root, "manager", ssTurn(104233, 90000, "2.1.322", "present"))
		waStaleHeartbeat(t, root, now)
		scWrite(t, filepath.Join(recoveryStateDir(root), "bravo.json"), []byte(`{"v":1,"halted":true,"halt_reason":"max_attempts"}`))

		if note := recoveryAlarmNote(root, now); !strings.HasPrefix(note, "⚠ HALT bravo") || !strings.Contains(note, "+2") {
			t.Errorf("pane = %q, want HALT bravo leading and STARTUP and WDOG counted as +2", note)
		}
		alerts := recoveryAlertsNote(root, now)
		if h, s, d := waLineIndex(t, alerts, "HALT"), waLineIndex(t, alerts, "STARTUP"), waLineIndex(t, alerts, "WDOG"); !(h < s && s < d) {
			t.Errorf("order HALT < STARTUP < WDOG broken (lines %d, %d, %d):\n%s", h, s, d, alerts)
		}
	})

	t.Run("STARTUP leads ahead of a stale heartbeat", func(t *testing.T) {
		root := t.TempDir()
		ssPlant(t, root, "manager", ssTurn(104233, 90000, "2.1.322", "present"))
		waStaleHeartbeat(t, root, now)
		if note := recoveryAlarmNote(root, now); note != "⚠ STARTUP manager +1" {
			t.Errorf("pane = %q, want %q", note, "⚠ STARTUP manager +1")
		}
	})

	t.Run("the rank C5 left free", func(t *testing.T) {
		rank, ok := alarmClassRank["STARTUP"]
		if !ok || rank != 4 {
			t.Fatalf("alarmClassRank[STARTUP] = %d (present %v), want 4", rank, ok)
		}
		if w, ok := alarmClassRank["WINDOW"]; ok && w >= rank {
			t.Errorf("alarmClassRank[WINDOW] = %d, want it below STARTUP's %d", w, rank)
		}
		if d := alarmClassRank[alarmClassWdog]; d <= rank {
			t.Errorf("alarmClassRank[WDOG] = %d, want it above STARTUP's %d", d, rank)
		}
	})
}

func ssHasControlRune(s string) (rune, bool) {
	for _, r := range s {
		if (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return r, true
		}
	}
	return 0, false
}

func TestStartupAlarm_SpeaksOnlyBoundedValues(t *testing.T) {
	f, mail := ssFactory(t, "supervisor")
	hostile := map[string]any{
		"session_id":      "sess\x1b[31m-evil",
		"tokens":          104233,
		"budget":          90000,
		"harness_version": "2.1\x1b]0;PWNED\x07",
		"rulings_block":   "partial\x07\x1b[2J",
		"at":              "2026-11-11T10:02:03Z",
	}
	ssPlant(t, f.root, "manager", hostile)

	pollStartupSignal(f.root, f.now)
	if len(mail.sent) != 1 {
		t.Fatalf("an over-budget measurement sent %d mails, want 1", len(mail.sent))
	}
	text, err := scRunStartup(t, f.root, "manager", false)
	if err != nil {
		t.Fatalf("af telemetry startup: %v", err)
	}
	jsonOut, err := scRunStartup(t, f.root, "manager", true)
	if err != nil {
		t.Fatalf("af telemetry startup --json: %v", err)
	}

	surfaces := map[string]string{
		"af statusline status": recoveryAlertsNote(f.root, f.now),
		"af up":                recoveryLaunchAlertsNote(f.root, f.now),
		"the mail subject":     mail.sent[0].subject,
		"the mail body":        mail.sent[0].body,
		"telemetry text":       text,
		"telemetry --json":     jsonOut,
	}
	for name, s := range surfaces {
		if r, bad := ssHasControlRune(s); bad {
			t.Errorf("%s carries control rune %U: %q", name, r, s)
		}
		for _, planted := range []string{"PWNED", "[31m", "[2J", "evil", `\u001b`, `\u0007`} {
			if strings.Contains(s, planted) {
				t.Errorf("%s carries planted bytes %q: %q", name, planted, s)
			}
		}
		if name != "telemetry --json" && !strings.Contains(s, "unknown") {
			t.Errorf("%s must read the harness version as unknown: %q", name, s)
		}
	}
	for _, name := range []string{"af statusline status", "af up", "the mail subject"} {
		if !strings.Contains(surfaces[name], "(harness unknown)") {
			t.Errorf("%s must name the version unknown: %q", name, surfaces[name])
		}
	}
	for _, want := range []string{"session: unknown", "harness: unknown", "rulings_block: unknown"} {
		if !strings.Contains(mail.sent[0].body, want) {
			t.Errorf("the mail body lacks %q:\n%s", want, mail.sent[0].body)
		}
	}
	if !strings.Contains(text, "harness unknown; rulings_block: unknown; session unknown;") {
		t.Errorf("the text output must read version, rulings and session as unknown:\n%s", text)
	}
	doc := scDecodeStartup(t, jsonOut)
	if len(doc.Agents) != 1 || doc.Agents[0].SessionID != "unknown" || doc.Agents[0].RulingsBlock != "unknown" || doc.Agents[0].HarnessVersion != "unknown" {
		t.Errorf("--json must read session_id, rulings_block and harness_version as unknown: %s", jsonOut)
	}
	if note := recoveryAlarmNote(f.root, f.now); note != "⚠ STARTUP manager" {
		t.Errorf("pane token = %q, want %q and no planted byte", note, "⚠ STARTUP manager")
	}
}

func TestStartupAlarm_ReaderWritesNothing(t *testing.T) {
	f := newScFactory(t, false)
	ssPlant(t, f.root, "manager", ssTurn(104233, 90000, "2.1.322", "present"))
	scArm(t, f.agentDir("supervisor"), "sess-pending", scLines(scAssistant("msg_1", 100, 200), scStop))
	marker := filepath.Join(f.agentDir("supervisor"), ".runtime", "startup_turn_pending")
	beforeTurn, _ := os.ReadFile(scPath(t, f.root, "manager"))
	beforeMarker, _ := os.ReadFile(marker)

	raised := startupAlarms(f.root)
	_, _ = recoveryAlarms(f.root, f.now)
	_ = recoveryAlarmNote(f.root, f.now)
	_ = recoveryAlertsNote(f.root, f.now)
	_ = recoveryLaunchAlertsNote(f.root, f.now)

	if len(raised) != 1 || raised[0].agent != "manager" {
		t.Fatalf("startupAlarms = %+v, want the manager's STARTUP alarm", raised)
	}
	if after, _ := os.ReadFile(scPath(t, f.root, "manager")); !bytes.Equal(beforeTurn, after) {
		t.Errorf("the reader rewrote the measurement")
	}
	if after, err := os.ReadFile(marker); err != nil || !bytes.Equal(beforeMarker, after) {
		t.Errorf("the reader moved the pending marker (err %v)", err)
	}
	if _, ok := scMeasurement(t, f.root, "supervisor"); ok {
		t.Error("the reader measured a pending startup; only the tick and the verb measure")
	}
	if _, err := os.Stat(filepath.Join(f.root, ".runtime", "startup_signal")); err == nil {
		t.Error("the reader wrote a latch")
	}
}

func TestStartupSignal_OverBudgetMailsOnce(t *testing.T) {
	f, mail := ssFactory(t, "supervisor")
	ssPlant(t, f.root, "manager", ssTurn(104233, 90000, "2.1.322", "present"))

	pollStartupSignal(f.root, f.now)
	pollStartupSignal(f.root, f.now.Add(time.Minute))
	if len(mail.sent) != 1 {
		t.Fatalf("two ticks sent %d mails, want 1: %+v", len(mail.sent), mail.sent)
	}
	m := mail.sent[0]
	if m.recipient != "supervisor" || m.subject != "STARTUP: manager 104233 > 90000 tokens (harness 2.1.322)" {
		t.Errorf("mail = %+v, want to supervisor with subject %q", m, "STARTUP: manager 104233 > 90000 tokens (harness 2.1.322)")
	}
	for _, want := range []string{"manager", "5f0c2a9e-3b1d-4c7e-9a8f-1e2d3c4b5a69", "2.1.322", "104233", "90000", "present",
		"af telemetry startup --agent manager", "sent once per agent and harness version"} {
		if !strings.Contains(m.body, want) {
			t.Errorf("the mail body lacks %q:\n%s", want, m.body)
		}
	}
	if _, err := os.Stat(startupSignalLatchPath(f.root, "manager", "2.1.322")); err != nil {
		t.Errorf("no latch at %s: %v", startupSignalLatchPath(f.root, "manager", "2.1.322"), err)
	}
	if want := filepath.Join(f.root, ".runtime", "startup_signal", "manager@2.1.322.sent"); startupSignalLatchPath(f.root, "manager", "2.1.322") != want {
		t.Errorf("latch path = %q, want %q", startupSignalLatchPath(f.root, "manager", "2.1.322"), want)
	}

	ssPlant(t, f.root, "manager", ssTurn(99000, 90000, "2.1.330", "present"))
	pollStartupSignal(f.root, f.now.Add(2*time.Minute))
	pollStartupSignal(f.root, f.now.Add(3*time.Minute))
	if len(mail.sent) != 2 {
		t.Fatalf("a new harness version mails once more: %d mails, want 2", len(mail.sent))
	}
	if got := mail.sent[1].subject; got != "STARTUP: manager 99000 > 90000 tokens (harness 2.1.330)" {
		t.Errorf("second subject = %q", got)
	}
}

// TestStartupSignal_ResetRearmsTheMail holds K10's "once per agent and harness version" to one alarm
// episode (PR #756 T19): a reset ends the episode, so a startup that goes over budget again on the
// same harness mails again instead of raising STARTUP to nobody.
func TestStartupSignal_ResetRearmsTheMail(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reset func(t *testing.T, root string)
	}{
		{"resetAgentState", func(t *testing.T, root string) {
			var buf bytes.Buffer
			var err error
			captureStderr(t, func() { err = resetAgentState(context.Background(), &buf, root, "manager", "test-reason") })
			if err != nil {
				t.Fatalf("resetAgentState: %v", err)
			}
		}},
		{"af recovery reset", func(t *testing.T, root string) {
			t.Chdir(root)
			if _, _, err := invokeRecoveryReset(t, "manager"); err != nil {
				t.Fatalf("runRecoveryReset: %v", err)
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, mail := ssFactory(t, "supervisor")
			over := ssTurn(104233, 90000, "2.1.322", "present")
			ssPlant(t, f.root, "manager", over)

			pollStartupSignal(f.root, f.now)
			if len(mail.sent) != 1 {
				t.Fatalf("the first over-budget startup sent %d mails, want 1: %+v", len(mail.sent), mail.sent)
			}

			tc.reset(t, f.root)
			if _, ok := scMeasurement(t, f.root, "manager"); ok {
				t.Fatal("fixture: the reset left the measurement in place")
			}
			ssPlant(t, f.root, "manager", over)
			pollStartupSignal(f.root, f.now.Add(time.Hour))
			pollStartupSignal(f.root, f.now.Add(time.Hour+time.Minute))
			if n := len(mail.sent) - 1; n != 1 {
				t.Errorf("after the reset the over-budget startup on the same harness mailed %d time(s), want 1: %+v", n, mail.sent)
			}
		})
	}
}

func TestStartupSignal_UnderBudgetNoSignal(t *testing.T) {
	f, mail := ssFactory(t, "supervisor", "manager")
	ssPlant(t, f.root, "manager", ssTurn(80000, 90000, "2.1.322", "present"))
	ssPlant(t, f.root, "supervisor", ssTurn(10, 90000, "2.1.322", "no_pins"))

	pollStartupSignal(f.root, f.now)
	for name, s := range map[string]string{
		"pane":                     recoveryAlarmNote(f.root, f.now),
		"recoveryAlertsNote":       recoveryAlertsNote(f.root, f.now),
		"recoveryLaunchAlertsNote": recoveryLaunchAlertsNote(f.root, f.now),
	} {
		if strings.Contains(s, "STARTUP") {
			t.Errorf("%s raised STARTUP for a measurement within budget: %q", name, s)
		}
	}
	if len(mail.sent) != 0 {
		t.Errorf("a measurement within budget mailed: %+v", mail.sent)
	}
	if _, err := os.Stat(filepath.Join(f.root, ".runtime", "startup_signal")); err == nil {
		t.Error("a latch was written for a measurement with no reason")
	}
	if _, ok := scMeasurement(t, f.root, "manager"); !ok {
		t.Fatal("fixture: the planted measurement is not where readers find it")
	}
}

func TestStartupSignal_RulingsBlockStates(t *testing.T) {
	for _, tc := range []struct {
		rulings string
		signals bool
	}{
		{"absent", true},
		{"partial", true},
		{"present", false},
		{"no_pins", false},
	} {
		t.Run(tc.rulings, func(t *testing.T) {
			f, mail := ssFactory(t, "supervisor")
			ssPlant(t, f.root, "manager", ssTurn(1000, 90000, "2.1.322", tc.rulings))
			pollStartupSignal(f.root, f.now)

			alarmed := strings.Contains(recoveryAlertsNote(f.root, f.now), "STARTUP manager — startup turn rulings block "+tc.rulings)
			if alarmed != tc.signals {
				t.Errorf("rulings_block %s: alarm raised = %v, want %v", tc.rulings, alarmed, tc.signals)
			}
			want := 0
			if tc.signals {
				want = 1
			}
			if len(mail.sent) != want {
				t.Fatalf("rulings_block %s: %d mails, want %d", tc.rulings, len(mail.sent), want)
			}
			if tc.signals && mail.sent[0].subject != "STARTUP: manager rulings block "+tc.rulings+" (harness 2.1.322)" {
				t.Errorf("subject = %q", mail.sent[0].subject)
			}
		})
	}
}

func TestStartupSignal_SupervisorMailsManager(t *testing.T) {
	f, mail := ssFactory(t, "manager")
	ssPlant(t, f.root, "supervisor", ssTurn(120000, 90000, "2.1.322", "present"))
	pollStartupSignal(f.root, f.now)
	if len(mail.sent) != 1 || mail.sent[0].recipient != "manager" {
		t.Fatalf("the supervisor's startup alarm must mail manager, sent %+v", mail.sent)
	}
	if mail.sent[0].subject != "STARTUP: supervisor 120000 > 90000 tokens (harness 2.1.322)" {
		t.Errorf("subject = %q", mail.sent[0].subject)
	}
}

func ssBreadcrumbs(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(recoveryUndeliveredPath(root))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimRight(string(data), "\n"), "\n")
}

func TestStartupSignal_UndeliveredBreadcrumb(t *testing.T) {
	latched := func(t *testing.T, root string) {
		t.Helper()
		if _, err := os.Stat(startupSignalLatchPath(root, "manager", "2.1.322")); err != nil {
			t.Errorf("the latch must be kept whatever the send's outcome: %v", err)
		}
	}
	alarmStands := func(t *testing.T, root string, now time.Time) {
		t.Helper()
		if !strings.Contains(recoveryAlertsNote(root, now), "STARTUP manager — ") {
			t.Error("the STARTUP alarm must stand")
		}
	}

	t.Run("undeliverable recipient writes the shared breadcrumb and keeps the latch", func(t *testing.T) {
		f, mail := ssFactory(t)
		writeAgentsJSON(t, f.root, `{"agents":{"manager":{"type":"interactive","description":"d"}}}`)
		ssPlant(t, f.root, "manager", ssTurn(104233, 90000, "2.1.322", "present"))

		captureStderr(t, func() { pollStartupSignal(f.root, f.now) })
		crumbs := ssBreadcrumbs(t, f.root)
		if len(crumbs) != 1 || !strings.Contains(crumbs[0], ` STARTUP (escalation undelivered to "supervisor"): `) {
			t.Fatalf("breadcrumb = %q, want one STARTUP line naming supervisor", crumbs)
		}
		latched(t, f.root)
		captureStderr(t, func() { pollStartupSignal(f.root, f.now.Add(time.Minute)) })
		if n := len(ssBreadcrumbs(t, f.root)); n != 1 || len(mail.sent) != 0 {
			t.Errorf("the next tick tried again: %d breadcrumb lines, %d mails", n, len(mail.sent))
		}
		alarmStands(t, f.root, f.now)
	})

	t.Run("failed send writes the shared breadcrumb and keeps the latch", func(t *testing.T) {
		f, mail := ssFactory(t, "supervisor")
		mail.err = errors.New("store unavailable")
		ssPlant(t, f.root, "manager", ssTurn(104233, 90000, "2.1.322", "present"))

		captureStderr(t, func() { pollStartupSignal(f.root, f.now) })
		crumbs := ssBreadcrumbs(t, f.root)
		if len(crumbs) != 1 || !strings.Contains(crumbs[0], ` STARTUP (escalation undelivered to "supervisor"): send failed: store unavailable`) {
			t.Fatalf("breadcrumb = %q, want one STARTUP send-failed line", crumbs)
		}
		latched(t, f.root)
		captureStderr(t, func() { pollStartupSignal(f.root, f.now.Add(time.Minute)) })
		if len(mail.sent) != 1 {
			t.Errorf("a failed send is latched like any other: %d attempts, want 1", len(mail.sent))
		}
		alarmStands(t, f.root, f.now)
	})

	t.Run("measurement with no latch is mailed by the next tick", func(t *testing.T) {
		f, mail := ssFactory(t, "supervisor")
		scArm(t, f.agentDir("manager"), "sess-verb", scLines(scAssistant("msg_1", 4000, 100000), scStop))
		if _, err := scRunStartup(t, f.root, "", false); err != nil {
			t.Fatalf("af telemetry startup: %v", err)
		}
		if _, ok := scMeasurement(t, f.root, "manager"); !ok {
			t.Fatal("the verb did not measure the pending turn")
		}
		if len(mail.sent) != 0 {
			t.Fatalf("the verb sent mail: %+v", mail.sent)
		}
		pollStartupSignal(f.root, f.now)
		if len(mail.sent) != 1 || mail.sent[0].recipient != "supervisor" {
			t.Errorf("the next tick must mail the verb's measurement once: %+v", mail.sent)
		}
		alarmStands(t, f.root, f.now)
	})
}

func TestWatchdog_TickCallsStartupSignalBeforeHeartbeat(t *testing.T) {
	src, err := os.ReadFile("watchdog.go")
	if err != nil {
		t.Fatalf("read watchdog.go: %v", err)
	}
	tick := functionBody(t, string(src), "func watchdogTick(")
	trig := strings.Index(tick, "triggerIntegrationServicesGuard(")
	call := strings.Index(tick, "pollStartupSignalFn(root, now)")
	beat := strings.Index(tick, "writeWatchdogHeartbeat(")
	if trig < 0 || call < 0 || beat < 0 || !(trig < call && call < beat) {
		t.Errorf("watchdogTick must call pollStartupSignalFn(root, now) after triggerIntegrationServicesGuard( and before "+
			"writeWatchdogHeartbeat( (indexes trigger=%d startup=%d heartbeat=%d)", trig, call, beat)
	}
	if n := strings.Count(tick, "pollStartupSignalFn("); n != 1 {
		t.Errorf("watchdogTick calls pollStartupSignalFn %d times, want once", n)
	}
	recoverySrc, err := os.ReadFile("recovery.go")
	if err != nil {
		t.Fatalf("read recovery.go: %v", err)
	}
	if body := functionBody(t, string(recoverySrc), "func evaluateAgent("); strings.Contains(body, "pollStartupSignal") {
		t.Error("the startup signal must be a sibling call of the tick, not inside evaluateAgent, which returns early for halted recovery")
	}
}
