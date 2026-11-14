package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/session"
)

func waWindowsDir(root string) string {
	return filepath.Join(root, ".agentfactory", "statusline", "windows")
}

func waKey(version string, declared int64, modelID string) string {
	return version + "|" + strconv.FormatInt(declared, 10) + "|" + modelID
}

func waRoster(t *testing.T, root string, agents ...string) {
	t.Helper()
	var b strings.Builder
	b.WriteString(`{"agents":{`)
	for i, a := range agents {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(strconv.Quote(a) + `:{"type":"autonomous","description":"d"}`)
	}
	b.WriteString("}}")
	writeAgentsJSON(t, root, b.String())
}

func waWrite(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func waFactPath(root, agent string) string {
	return filepath.Join(waWindowsDir(root), "launched", agent+".json")
}

func waRecordPath(root, profile string) string {
	return filepath.Join(waWindowsDir(root), profile+".json")
}

func waPlantFact(t *testing.T, root, agent string, f windowLaunchFact) {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	waWrite(t, waFactPath(root, agent), data)
}

// waPlantRecord files each entry under the key its own fields spell, the way the spec's key rule
// does, so a fixture never depends on the key() under test.
func waPlantRecord(t *testing.T, root, profile string, entries ...windowRecord) {
	t.Helper()
	file := windowRecordFile{Profile: profile, Identities: map[string]windowRecord{}}
	for _, e := range entries {
		file.Identities[waKey(e.HarnessVersion, e.Declared, e.ModelID)] = e
	}
	data, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	waWrite(t, waRecordPath(root, profile), data)
}

type waFixture struct {
	now      time.Time
	launched time.Time
}

func newWaFixture() waFixture {
	now := time.Now().UTC().Truncate(time.Second)
	return waFixture{now: now, launched: now.Add(-time.Hour)}
}

func (x waFixture) fact(profile, version string, declared int64) windowLaunchFact {
	return windowLaunchFact{Profile: profile, ModelID: "claude-" + profile, Declared: declared, HarnessVersion: version, LaunchedAt: x.launched}
}

// entry is the identity's record holding each agent's own figure of the fixture's launch.
func (x waFixture) entry(f windowLaunchFact, figures map[string]int64) windowRecord {
	rec := windowRecord{
		ModelID: f.ModelID, HarnessVersion: f.HarnessVersion, Class: f.Class, Declared: f.Declared,
		FirstSeen: time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC), LastSeen: x.now.Add(-time.Hour),
		Sessions: len(figures), Source: "session", Agents: map[string]agentWindow{},
	}
	for agent, fig := range figures {
		rec.Agents[agent] = agentWindow{Applied: fig, LaunchedAt: f.LaunchedAt, LastSeen: x.now.Add(-time.Hour)}
		if rec.Applied == 0 || fig < rec.Applied {
			rec.Applied = fig
		}
	}
	return rec
}

func waAlarmsFor(alarms []recoveryAlarm) map[string]recoveryAlarm {
	m := map[string]recoveryAlarm{}
	for _, a := range alarms {
		if a.class == "WINDOW" {
			m[a.agent] = a
		}
	}
	return m
}

// waPlantControl gives a root one divergent agent on its own profile, so a subtest asserting that
// another fixture raises nothing cannot pass on a reader that raises nothing at all.
func waPlantControl(t *testing.T, root string, x waFixture) {
	t.Helper()
	f := x.fact("ctlprof", "2.1.308", 1000000)
	waPlantFact(t, root, "ctl", f)
	waPlantRecord(t, root, "ctlprof", x.entry(f, map[string]int64{"ctl": 200000}))
}

func waRequireControl(t *testing.T, got map[string]recoveryAlarm) {
	t.Helper()
	if a, ok := got["ctl"]; !ok || a.reason != "mismatch" {
		t.Fatalf("the control agent raised no mismatch WINDOW (got %+v); the reader under test raised nothing at all", got)
	}
}

func TestWindowAlarm_RaisedFromLaunchFactAndRecord(t *testing.T) {
	t.Run("divergent figure raises WINDOW for each agent that reported it", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "alpha", "bravo")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "alpha", f)
		waPlantFact(t, root, "bravo", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"alpha": 200000, "bravo": 262144}))

		got := waAlarmsFor(windowAlarms(root, x.now))
		if len(got) != 2 {
			t.Fatalf("want a WINDOW alarm for alpha and bravo, got %+v", got)
		}
		for agent, fig := range map[string]string{"alpha": "applied 200000", "bravo": "applied 262144"} {
			a := got[agent]
			if a.reason != "mismatch" {
				t.Errorf("%s: reason = %q, want mismatch", agent, a.reason)
			}
			if !strings.Contains(a.detail, fig) || !strings.Contains(a.detail, "declares a 1000000-token window") {
				t.Errorf("%s: the detail must carry the agent's own figure (%s) and the declaration: %q", agent, fig, a.detail)
			}
		}
	})

	t.Run("agent with no figure of its own raises nothing beside a divergent agent", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "alpha", "bravo")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "alpha", f)
		waPlantFact(t, root, "bravo", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"alpha": 200000}))

		got := waAlarmsFor(windowAlarms(root, x.now))
		if _, ok := got["alpha"]; !ok || len(got) != 1 {
			t.Errorf("want exactly alpha's WINDOW, got %+v", got)
		}
	})

	t.Run("unobserved mark raises WINDOW with the unobserved reason", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		installFakeTmuxPresent(t, session.SessionName("manager"))
		waRoster(t, root, "manager")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		rec := x.entry(f, nil)
		rec.UnobservedNotified, rec.Source = true, ""
		waPlantRecord(t, root, "fable-5-2", rec)

		got := waAlarmsFor(windowAlarms(root, x.now))
		a, ok := got["manager"]
		if !ok || a.reason != "unobserved" {
			t.Fatalf("want manager's WINDOW with reason unobserved, got %+v", got)
		}
		want := `profile "fable-5-2" declares a 1000000-token window on Claude Code 2.1.308, and the session has reported no window since launch.`
		if a.detail != want {
			t.Errorf("unobserved detail:\ngot:  %q\nwant: %q", a.detail, want)
		}
	})

	t.Run("unobserved mark on a launch inside windowDarkGrace raises nothing", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		x.launched = x.now.Add(-time.Minute)
		installFakeTmuxPresent(t, session.SessionName("manager"))
		waRoster(t, root, "manager")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		rec := x.entry(f, nil)
		rec.UnobservedNotified, rec.Source = true, ""
		waPlantRecord(t, root, "fable-5-2", rec)

		if got := waAlarmsFor(windowAlarms(root, x.now)); len(got) != 0 {
			t.Errorf("a launch one minute old raised %+v", got)
		}
		if got := waAlarmsFor(windowAlarms(root, x.now.Add(10*time.Minute))); got["manager"].reason != "unobserved" {
			t.Errorf("the same mark past windowDarkGrace must raise unobserved, got %+v", got)
		}
	})

	t.Run("probe row does not clear the unobserved alarm", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		installFakeTmuxPresent(t, session.SessionName("manager"))
		waRoster(t, root, "manager")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		rec := x.entry(f, nil)
		rec.UnobservedNotified, rec.Source, rec.Probed, rec.Applied = true, "probe", 1000000, 1000000
		waPlantRecord(t, root, "fable-5-2", rec)

		if got := waAlarmsFor(windowAlarms(root, x.now)); got["manager"].reason != "unobserved" {
			t.Errorf("a probe row measured the harness, not this launch: want unobserved, got %+v", got)
		}
	})

	t.Run("agreeing entry raises nothing", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager", "ctl")
		waPlantControl(t, root, x)
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"manager": 1000000}))

		got := waAlarmsFor(windowAlarms(root, x.now))
		waRequireControl(t, got)
		if a, ok := got["manager"]; ok {
			t.Errorf("a figure equal to the declaration raised %+v", a)
		}
	})

	t.Run("nothing declared raises nothing", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager", "ctl")
		waPlantControl(t, root, x)
		f := x.fact("fable-5-2", "2.1.308", 0)
		waPlantFact(t, root, "manager", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"manager": 200000}))

		got := waAlarmsFor(windowAlarms(root, x.now))
		waRequireControl(t, got)
		if a, ok := got["manager"]; ok {
			t.Errorf("a profile that declares nothing raised %+v", a)
		}
	})

	t.Run("entry of another identity raises nothing", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager", "ctl")
		waPlantControl(t, root, x)
		other := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", x.fact("fable-5-2", "2.1.322", 1000000))
		waPlantRecord(t, root, "fable-5-2", x.entry(other, map[string]int64{"manager": 200000}))

		got := waAlarmsFor(windowAlarms(root, x.now))
		waRequireControl(t, got)
		if a, ok := got["manager"]; ok {
			t.Errorf("the 2.1.308 entry answered for a 2.1.322 launch: %+v", a)
		}
	})

	t.Run("relaunch on another identity ends the alarm", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"manager": 200000}))
		if got := waAlarmsFor(windowAlarms(root, x.now)); got["manager"].reason != "mismatch" {
			t.Fatalf("the fixture raised no mismatch before the relaunch: %+v", got)
		}

		relaunched := x.fact("fable-5-2", "2.1.322", 1000000)
		relaunched.LaunchedAt = x.now.Add(-time.Minute)
		waPlantFact(t, root, "manager", relaunched)
		if got := waAlarmsFor(windowAlarms(root, x.now)); len(got) != 0 {
			t.Errorf("the relaunch on 2.1.322 left %+v standing", got)
		}
	})

	t.Run("stopped agent keeps its alarm", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		installFakeTmuxPresent(t)
		waRoster(t, root, "manager")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"manager": 200000}))

		if got := waAlarmsFor(windowAlarms(root, x.now)); got["manager"].reason != "mismatch" {
			t.Errorf("WINDOW is not live-only: a stopped agent's launch still stands, got %+v", got)
		}
	})

	t.Run("stopped agent with only the unobserved mark raises nothing", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		installFakeTmuxPresent(t)
		waRoster(t, root, "manager", "ctl")
		waPlantControl(t, root, x)
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		rec := x.entry(f, nil)
		rec.UnobservedNotified, rec.Source = true, ""
		waPlantRecord(t, root, "fable-5-2", rec)

		got := waAlarmsFor(windowAlarms(root, x.now))
		waRequireControl(t, got)
		if a, ok := got["manager"]; ok {
			t.Errorf("an unobserved identity refuses nothing, so a stopped agent's mark must raise no WINDOW: %+v", a)
		}
	})

	t.Run("one marked identity alarms only its live agent", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		tmux := installFakeTmuxPresent(t, session.SessionName("alpha"))
		waRoster(t, root, "alpha", "beta", "ctl")
		waPlantControl(t, root, x)
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "alpha", f)
		waPlantFact(t, root, "beta", f)
		rec := x.entry(f, nil)
		rec.UnobservedNotified, rec.Source = true, ""
		waPlantRecord(t, root, "fable-5-2", rec)

		got := waAlarmsFor(windowAlarms(root, x.now))
		waRequireControl(t, got)
		if got["alpha"].reason != "unobserved" {
			t.Errorf("the live, silent alpha must raise unobserved, got %+v", got)
		}
		if a, ok := got["beta"]; ok {
			t.Errorf("beta is stopped: the mark alpha's silence set must not alarm it, got %+v", a)
		}

		delete(tmux.present, session.SessionName("alpha"))
		got = waAlarmsFor(windowAlarms(root, x.now))
		waRequireControl(t, got)
		for _, agent := range []string{"alpha", "beta"} {
			if a, ok := got[agent]; ok {
				t.Errorf("nobody is live: %s's unobserved WINDOW must end with its session, got %+v", agent, a)
			}
		}
	})

	t.Run("agent not in agents.json raises nothing", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "ctl")
		waPlantControl(t, root, x)
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "ghost", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"ghost": 200000}))

		got := waAlarmsFor(windowAlarms(root, x.now))
		waRequireControl(t, got)
		if a, ok := got["ghost"]; ok {
			t.Errorf("an agent removed from agents.json still alarms: %+v", a)
		}
	})

	t.Run("roster that fails to load filters nothing", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		writeAgentsJSON(t, root, "{not json")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"manager": 200000}))

		if got := waAlarmsFor(windowAlarms(root, x.now)); got["manager"].reason != "mismatch" {
			t.Errorf("an alarm errs toward showing: an unloadable roster must filter nothing, got %+v", got)
		}
	})

	t.Run("entry past the prune horizon raises nothing", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager")
		f := x.fact("fable-5-2", "2.1.308", 1000000)
		waPlantFact(t, root, "manager", f)
		rec := x.entry(f, map[string]int64{"manager": 200000})
		rec.LastSeen = x.now.Add(-29 * 24 * time.Hour)
		waPlantRecord(t, root, "fable-5-2", rec)
		if got := waAlarmsFor(windowAlarms(root, x.now)); got["manager"].reason != "mismatch" {
			t.Fatalf("an entry 29 days old is inside the horizon and must raise, got %+v", got)
		}

		rec.LastSeen = x.now.Add(-31 * 24 * time.Hour)
		waPlantRecord(t, root, "fable-5-2", rec)
		if got := waAlarmsFor(windowAlarms(root, x.now)); len(got) != 0 {
			t.Errorf("an entry 31 days old is absent at now, still in the file or not: got %+v", got)
		}
	})

	t.Run("absent oversized malformed FIFO or symlinked file raises nothing", func(t *testing.T) {
		big := func(t *testing.T, path string, valid []byte) {
			waWrite(t, path, append(valid, []byte(strings.Repeat(" ", windowFileMaxBytesForTest))...))
		}
		fifo := func(t *testing.T, path string) {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			_ = os.Remove(path)
			if err := syscall.Mkfifo(path, 0o644); err != nil {
				t.Skipf("mkfifo: %v", err)
			}
		}
		symlink := func(t *testing.T, path string, valid []byte) {
			target := filepath.Join(t.TempDir(), "target.json")
			waWrite(t, target, valid)
			_ = os.Remove(path)
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}
		rows := []struct {
			name  string
			spoil func(t *testing.T, factPath, recordPath string, fact, record []byte)
		}{
			{"absent fact", func(t *testing.T, fp, _ string, _, _ []byte) { _ = os.Remove(fp) }},
			{"absent record", func(t *testing.T, _, rp string, _, _ []byte) { _ = os.Remove(rp) }},
			{"oversized fact", func(t *testing.T, fp, _ string, f, _ []byte) { big(t, fp, f) }},
			{"oversized record", func(t *testing.T, _, rp string, _, r []byte) { big(t, rp, r) }},
			{"malformed fact", func(t *testing.T, fp, _ string, _, _ []byte) { waWrite(t, fp, []byte(`{"profile":`)) }},
			{"malformed record", func(t *testing.T, _, rp string, _, _ []byte) { waWrite(t, rp, []byte(`{"identities":[`)) }},
			{"FIFO fact", func(t *testing.T, fp, _ string, _, _ []byte) { fifo(t, fp) }},
			{"FIFO record", func(t *testing.T, _, rp string, _, _ []byte) { fifo(t, rp) }},
			{"symlinked fact", func(t *testing.T, fp, _ string, f, _ []byte) { symlink(t, fp, f) }},
			{"symlinked record", func(t *testing.T, _, rp string, _, r []byte) { symlink(t, rp, r) }},
		}
		for _, row := range rows {
			t.Run(row.name, func(t *testing.T) {
				x, root := newWaFixture(), t.TempDir()
				waRoster(t, root, "manager", "ctl")
				waPlantControl(t, root, x)
				f := x.fact("fable-5-2", "2.1.308", 1000000)
				waPlantFact(t, root, "manager", f)
				waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"manager": 200000}))
				factBytes, err := os.ReadFile(waFactPath(root, "manager"))
				if err != nil {
					t.Fatal(err)
				}
				recordBytes, err := os.ReadFile(waRecordPath(root, "fable-5-2"))
				if err != nil {
					t.Fatal(err)
				}
				row.spoil(t, waFactPath(root, "manager"), waRecordPath(root, "fable-5-2"), factBytes, recordBytes)

				done := make(chan []recoveryAlarm, 1)
				go func() { done <- windowAlarms(root, x.now) }()
				select {
				case alarms := <-done:
					got := waAlarmsFor(alarms)
					waRequireControl(t, got)
					if a, ok := got["manager"]; ok {
						t.Errorf("%s raised %+v", row.name, a)
					}
				case <-time.After(3 * time.Second):
					t.Fatalf("windowAlarms blocked on a %s", row.name)
				}
			})
		}
	})
}

// windowFileMaxBytesForTest is the spec's 64 KiB cap spelled out, so the oversized fixture does not
// shrink with whatever value the constant under test happens to hold.
const windowFileMaxBytesForTest = 64 << 10

func waPlantMismatch(t *testing.T, root string, x waFixture, mutate func(*windowLaunchFact)) windowRecord {
	t.Helper()
	f := windowLaunchFact{Profile: "fable-5-2", ModelID: "claude-fable-5-2", Declared: 1000000, HarnessVersion: "2.1.308", LaunchedAt: x.launched}
	if mutate != nil {
		mutate(&f)
	}
	waPlantFact(t, root, "manager", f)
	rec := x.entry(f, map[string]int64{"manager": 200000})
	rec.Sessions = 44
	waPlantRecord(t, root, "fable-5-2", rec)
	return rec
}

const waManagerHead = `agent "manager": profile "fable-5-2" declares a 1000000-token window, but Claude Code 2.1.308` + "\n" +
	`    applied 200000 to model "claude-fable-5-2" (first seen 2026-11-10, 44 sessions).`

func TestWindowAlarm_OneReadThreeRenderings(t *testing.T) {
	x, root := newWaFixture(), t.TempDir()
	waRoster(t, root, "manager")
	waPlantMismatch(t, root, x, nil)

	if note := recoveryAlarmNote(root, x.now); note != "⚠ WINDOW manager" {
		t.Errorf("pane token = %q, want %q", note, "⚠ WINDOW manager")
	} else {
		for _, leaked := range []string{"1000000", "200000", "fable-5-2", "claude-"} {
			if strings.Contains(note, leaked) {
				t.Errorf("the pane token carries %q: %q", leaked, note)
			}
		}
	}

	sess := session.SessionName("manager")
	for name, alerts := range map[string]string{
		"recoveryAlertsNote":       recoveryAlertsNote(root, x.now),
		"recoveryLaunchAlertsNote": recoveryLaunchAlertsNote(root, x.now),
	} {
		if !strings.Contains(alerts, "WINDOW manager (mismatch) — ") {
			t.Errorf("%s names no mismatch WINDOW for manager: %q", name, alerts)
		}
		if !strings.Contains(alerts, waManagerHead) {
			t.Errorf("%s does not carry the Head with both figures:\n%s", name, alerts)
		}
		for _, act := range []string{"'tmux kill-session -t =" + sess + "'", "'af up manager'", "editing models.json alone ends nothing"} {
			if !strings.Contains(alerts, act) {
				t.Errorf("%s lacks the clearing act %q: %q", name, act, alerts)
			}
		}
		if strings.Contains(alerts, "af down") {
			t.Errorf("%s tells the operator to run af down, which deletes a clean worktree's scratch: %q", name, alerts)
		}
	}
}

func t20ManagerClearingLine(t *testing.T, root string, now time.Time) string {
	t.Helper()
	for _, a := range windowAlarms(root, now) {
		if a.class == alarmClassWindow && a.agent == "manager" {
			return a.describe()
		}
	}
	t.Fatalf("the fixture raised no WINDOW for manager")
	return ""
}

func TestWindowAlarm_ClearingActFitsTheDirection(t *testing.T) {
	sess := session.SessionName("manager")

	t.Run("host above the declaration", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager")
		f := x.fact("fable-5-2", "2.1.322", 200000)
		waPlantFact(t, root, "manager", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"manager": 1000000}))

		line := t20ManagerClearingLine(t, root, x.now)
		if !strings.Contains(line, "WINDOW manager (mismatch) — ") {
			t.Errorf("the reason vocabulary must stay mismatch: %q", line)
		}
		if strings.Contains(line, "install a Claude Code version that knows the model") {
			t.Errorf("a host applying more than the declaration is not ended by a harness upgrade: %q", line)
		}
		if !strings.Contains(line, "to the window the host applies") {
			t.Errorf("the clearing act must name raising the declaration to the window the host applies: %q", line)
		}
		if strings.Contains(line, "af down") {
			t.Errorf("the clearing act tells the operator to run af down: %q", line)
		}
	})

	t.Run("host below the declaration", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager")
		f := x.fact("fable-5-2", "2.1.322", 1000000)
		waPlantFact(t, root, "manager", f)
		waPlantRecord(t, root, "fable-5-2", x.entry(f, map[string]int64{"manager": 200000}))

		line := t20ManagerClearingLine(t, root, x.now)
		for _, act := range []string{"WINDOW manager (mismatch) — ", "install a Claude Code version that knows the model",
			"'tmux kill-session -t =" + sess + "'", "'af up manager'", "editing models.json alone ends nothing"} {
			if !strings.Contains(line, act) {
				t.Errorf("the below-declaration clearing act lacks %q: %q", act, line)
			}
		}
		if strings.Contains(line, "af down") {
			t.Errorf("the clearing act tells the operator to run af down: %q", line)
		}
	})
}

func waStaleHeartbeat(t *testing.T, root string, now time.Time) {
	t.Helper()
	path := watchdogHeartbeatPath(root)
	waWrite(t, path, []byte("x"))
	old := now.Add(-2 * time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func waLineIndex(t *testing.T, alerts, prefix string) int {
	t.Helper()
	for i, l := range strings.Split(alerts, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			return i
		}
	}
	t.Fatalf("no alert line starts with %q:\n%s", prefix, alerts)
	return -1
}

func TestWindowAlarm_RanksAfterRecoveryClassesAheadOfWdog(t *testing.T) {
	t.Run("a halted breaker leads ahead of WINDOW and WDOG", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager", "bravo")
		waPlantMismatch(t, root, x, nil)
		waStaleHeartbeat(t, root, x.now)
		waWrite(t, filepath.Join(recoveryStateDir(root), "bravo.json"), []byte(`{"v":1,"halted":true,"halt_reason":"max_attempts"}`))

		if note := recoveryAlarmNote(root, x.now); !strings.HasPrefix(note, "⚠ HALT bravo") || !strings.Contains(note, "+2") {
			t.Errorf("pane = %q, want it to lead with HALT bravo and count the WINDOW and WDOG as +2", note)
		}
		alerts := recoveryAlertsNote(root, x.now)
		if h, w, d := waLineIndex(t, alerts, "HALT"), waLineIndex(t, alerts, "WINDOW"), waLineIndex(t, alerts, "WDOG"); !(h < w && w < d) {
			t.Errorf("order HALT < WINDOW < WDOG broken (lines %d, %d, %d):\n%s", h, w, d, alerts)
		}
	})

	t.Run("WINDOW leads ahead of a stale heartbeat", func(t *testing.T) {
		x, root := newWaFixture(), t.TempDir()
		waRoster(t, root, "manager")
		waPlantMismatch(t, root, x, nil)
		waStaleHeartbeat(t, root, x.now)

		if note := recoveryAlarmNote(root, x.now); note != "⚠ WINDOW manager +1" {
			t.Errorf("pane = %q, want %q", note, "⚠ WINDOW manager +1")
		}
	})

	for _, tc := range []struct {
		reason string
		mutate func(*windowLaunchFact)
	}{
		{"accepted", func(f *windowLaunchFact) { f.AcceptWindow = true }},
		{"foreign", func(f *windowLaunchFact) { f.Class, f.ModelID = "foreign", "gpt-5.6-sol" }},
	} {
		t.Run(tc.reason+" WINDOW ranks after WDOG", func(t *testing.T) {
			x, root := newWaFixture(), t.TempDir()
			waRoster(t, root, "manager")
			waPlantMismatch(t, root, x, tc.mutate)
			waStaleHeartbeat(t, root, x.now)

			if note := recoveryAlarmNote(root, x.now); !strings.HasPrefix(note, "⚠ WDOG") || !strings.Contains(note, "+1") {
				t.Errorf("pane = %q, want WDOG leading and the standing WINDOW counted as +1", note)
			}
			alerts := recoveryAlertsNote(root, x.now)
			d, w := waLineIndex(t, alerts, "WDOG"), waLineIndex(t, alerts, "WINDOW manager ("+tc.reason+")")
			if !(d < w) {
				t.Errorf("the %s WINDOW (line %d) must be listed after WDOG (line %d):\n%s", tc.reason, w, d, alerts)
			}
		})
	}
}

func waHasControlRune(s string) (rune, bool) {
	for _, r := range s {
		if (r < 0x20 && r != '\n') || r == 0x7f || (r >= 0x80 && r <= 0x9f) {
			return r, true
		}
	}
	return 0, false
}

func TestWindowAlarm_SpeaksOnlyBoundedValues(t *testing.T) {
	x, root := newWaFixture(), t.TempDir()
	waRoster(t, root, "alpha", "bravo", "charlie", "delta", "echo")
	divergent := func(f windowLaunchFact, agent string) windowRecord {
		return x.entry(f, map[string]int64{agent: 200000})
	}

	hostileModel := "claude-\x1b]0;PWNED\x07"
	alpha := windowLaunchFact{Profile: "aprof", ModelID: hostileModel, Declared: 1000000, HarnessVersion: "2.1.308", LaunchedAt: x.launched}
	waPlantFact(t, root, "alpha", alpha)
	waPlantRecord(t, root, "aprof", divergent(alpha, "alpha"))

	bravo := windowLaunchFact{Profile: "bprof\x1b[2J", ModelID: "claude-b", Declared: 1000000, HarnessVersion: "2.1.308", LaunchedAt: x.launched}
	waPlantFact(t, root, "bravo", bravo)

	charlie := windowLaunchFact{Profile: "cprof", ModelID: "claude-c", Declared: 1000000, HarnessVersion: "2.1.308\x1b[31m", LaunchedAt: x.launched}
	waPlantFact(t, root, "charlie", charlie)
	waPlantRecord(t, root, "cprof", divergent(charlie, "charlie"))

	delta := windowLaunchFact{Profile: "dprof", ModelID: "claude-d", Declared: 1000000, HarnessVersion: "2.1.308", LaunchedAt: x.launched}
	waPlantFact(t, root, "delta", delta)
	deltaRec := windowRecordFile{Profile: "dprof\x1b]0;PWNED\x07", Identities: map[string]windowRecord{
		waKey("2.1.308", 1000000, "claude-d"): divergent(delta, "delta"),
	}}
	data, err := json.Marshal(deltaRec)
	if err != nil {
		t.Fatal(err)
	}
	waWrite(t, waRecordPath(root, "dprof"), data)

	echo := windowLaunchFact{Profile: "eprof", ModelID: "claude-e", Declared: 1000000, HarnessVersion: "2.1.308", LaunchedAt: x.launched}
	waPlantFact(t, root, "echo", echo)
	echoEntry := divergent(echo, "echo")
	echoEntry.HarnessVersion = "2.1.308\x1b[31m"
	data, err = json.Marshal(windowRecordFile{Profile: "eprof", Identities: map[string]windowRecord{
		waKey("2.1.308", 1000000, "claude-e"): echoEntry,
	}})
	if err != nil {
		t.Fatal(err)
	}
	waWrite(t, waRecordPath(root, "eprof"), data)

	badName := windowLaunchFact{Profile: "aprof", ModelID: hostileModel, Declared: 1000000, HarnessVersion: "2.1.308", LaunchedAt: x.launched}
	waPlantFact(t, root, "9bad name", badName)

	pane := recoveryAlarmNote(root, x.now)
	alerts := recoveryAlertsNote(root, x.now) + recoveryLaunchAlertsNote(root, x.now)
	if !strings.Contains(alerts, "WINDOW alpha (mismatch)") || !strings.Contains(alerts, "WINDOW delta (mismatch)") {
		t.Fatalf("want WINDOW for alpha (hostile model id, printed quoted) and delta (hostile file profile, ignored), got:\n%s", alerts)
	}
	if !strings.Contains(alerts, `profile "dprof" declares`) {
		t.Errorf("delta's profile must come from the validated name, not the file's own field: %q", alerts)
	}
	for _, refused := range []string{"WINDOW bravo", "WINDOW charlie", "WINDOW echo", "9bad"} {
		if strings.Contains(alerts, refused) {
			t.Errorf("%q reached the loud surface:\n%s", refused, alerts)
		}
	}
	if r, bad := waHasControlRune(alerts); bad {
		t.Errorf("control rune %U reached the loud surface:\n%q", r, alerts)
	}
	if r, bad := waHasControlRune(pane); bad || strings.Contains(pane, "PWNED") || strings.Contains(pane, "prof") {
		t.Errorf("a planted byte (%U) reached the pane: %q", r, pane)
	}
}

func waTreeDigest(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, path)
		if d.IsDir() {
			out[rel] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		sum := sha256.Sum256(data)
		out[rel] = hex.EncodeToString(sum[:]) + " " + info.ModTime().String()
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestWindowAlarm_ReaderWritesNothing(t *testing.T) {
	x, root := newWaFixture(), t.TempDir()
	waRoster(t, root, "manager")
	waPlantMismatch(t, root, x, nil)
	plantSessionSnapshotWithTotal(t, root, "manager", "sess-1", 20, 200000, 1000, x.now.Add(-time.Minute), x.now)

	statuslineDir := filepath.Join(root, ".agentfactory", "statusline")
	before := waTreeDigest(t, statuslineDir)
	runtimeBefore := waTreeDigest(t, root)

	alarms := windowAlarms(root, x.now)
	_, _, _ = windowJoin(root, "manager", x.now)
	_, _, _ = windowAgentFigures(root, "manager", x.now)
	_, _ = recoveryAlarms(root, x.now)
	_ = recoveryAlarmNote(root, x.now)
	_ = recoveryAlertsNote(root, x.now)
	_ = recoveryLaunchAlertsNote(root, x.now)

	if got := waAlarmsFor(alarms); got["manager"].reason != "mismatch" {
		t.Fatalf("the read raised no WINDOW for the planted fact and entry; the byte check would be vacuous: %+v", got)
	}
	after := waTreeDigest(t, statuslineDir)
	if len(after) != len(before) {
		t.Errorf("the reader changed the statusline tree: before %v, after %v", before, after)
	}
	for path, sum := range before {
		if after[path] != sum {
			t.Errorf("%s changed under a read (%s -> %s)", path, sum, after[path])
		}
	}
	if runtimeAfter := waTreeDigest(t, root); len(runtimeAfter) != len(runtimeBefore) {
		t.Errorf("the reader created or removed files under the root: before %d entries, after %d", len(runtimeBefore), len(runtimeAfter))
	}
}
