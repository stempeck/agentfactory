package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// No test in this file calls t.Parallel: every one swaps package-level seams (claudeCodeVersionFn,
// escalationMailFn, newCmdTmux, pollOccupancyFn) and the detector's pass memo is package state.

const (
	wdProfile  = "fable-5-2"
	wdModel    = "claude-fable-5-2"
	wdDeclared = int64(1000000)
	wdOld      = "2.1.308"
	wdNew      = "2.1.322"
)

func wdWindowsDir(root string) string {
	return filepath.Join(root, ".agentfactory", "statusline", "windows")
}

func wdFactPath(root, agent string) string {
	return filepath.Join(wdWindowsDir(root), "launched", agent+".json")
}

func wdRecordPath(root, profile string) string {
	return filepath.Join(wdWindowsDir(root), profile+".json")
}

func wdKey(version string, declared int64, model string) string {
	return fmt.Sprintf("%s|%d|%s", version, declared, model)
}

func wdStubVersion(t *testing.T, version string) {
	t.Helper()
	orig := claudeCodeVersionFn
	claudeCodeVersionFn = func() string { return version + " (Claude Code)" }
	t.Cleanup(func() { claudeCodeVersionFn = orig })
}

func wdWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func wdFact(profile, model string, declared int64, version string, at time.Time) windowLaunchFact {
	return windowLaunchFact{Profile: profile, ModelID: model, Declared: declared, HarnessVersion: version, LaunchedAt: at}
}

func wdPlantFact(t *testing.T, root, agent string, f windowLaunchFact) {
	t.Helper()
	wdWriteJSON(t, wdFactPath(root, agent), f)
}

func wdPlantRecord(t *testing.T, root, profile string, entries map[string]windowRecord) {
	t.Helper()
	wdWriteJSON(t, wdRecordPath(root, profile), windowRecordFile{Profile: profile, Identities: entries})
}

// wdReadRecord decodes the record file itself, never through readWindowRecord, so a test reads what
// is on disk rather than what the reader under test chooses to return.
func wdReadRecord(t *testing.T, root, profile string) (windowRecordFile, bool) {
	t.Helper()
	data, err := os.ReadFile(wdRecordPath(root, profile))
	if err != nil {
		return windowRecordFile{}, false
	}
	var f windowRecordFile
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("record %s is not JSON: %v\n%s", wdRecordPath(root, profile), err, data)
	}
	return f, true
}

func wdEntry(t *testing.T, root, profile, key string) (windowRecord, bool) {
	t.Helper()
	f, ok := wdReadRecord(t, root, profile)
	if !ok {
		return windowRecord{}, false
	}
	rec, ok := f.Identities[key]
	return rec, ok
}

func wdMustEntry(t *testing.T, root, profile, key string) windowRecord {
	t.Helper()
	rec, ok := wdEntry(t, root, profile, key)
	if !ok {
		t.Fatalf("no entry %q in %s", key, wdRecordPath(root, profile))
	}
	return rec
}

// wdPlantSession binds a session to the agent the way the new session's SessionStart hook and its
// first render would. Both stamps are set explicitly at `at`: written at the time of the write, the
// kernel's coarse clock and written_at's whole seconds would read as at or before launched_at on
// most runs (M-R3-3).
func wdPlantSession(t *testing.T, root, agent, sid string, at time.Time, total int64) {
	t.Helper()
	wdPlantSessionID(t, root, agent, sid, at)
	plantSessionSnapshotWithTotal(t, root, agent, sid, 40, total, 1000, at, at)
}

func wdPlantSessionID(t *testing.T, root, agent, sid string, mtime time.Time) {
	t.Helper()
	runtimeDir := filepath.Join(config.AgentDir(root, agent), ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(runtimeDir, "session_id")
	if err := os.WriteFile(path, []byte(sid), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

type wdFixture struct {
	root string
	mail *mailRecorder
	tmux *fakeTmux
}

// newWDFixture is a factory whose two escalation recipients are rostered and live, so every send
// is filed and none leaves a breadcrumb unless a test makes it.
func newWDFixture(t *testing.T, agents ...string) *wdFixture {
	t.Helper()
	root := newTestFactoryRoot(t)
	roster := map[string]map[string]string{
		"manager":    {"type": "interactive", "description": "m"},
		"supervisor": {"type": "autonomous", "description": "s"},
	}
	for _, a := range agents {
		roster[a] = map[string]string{"type": "autonomous", "description": a}
	}
	wdWriteJSON(t, config.AgentsConfigPath(root), map[string]any{"agents": roster})
	resetWindowPass()
	t.Cleanup(resetWindowPass)
	return &wdFixture{
		root: root,
		mail: (&mailRecorder{}).install(t),
		tmux: installFakeTmuxPresent(t, session.SessionName("manager"), session.SessionName("supervisor")),
	}
}

func (f *wdFixture) pass(now time.Time, live ...string) windowReport {
	return f.passIn(windowDetectInput{Site: windowAtTick, Now: now, Live: wdLive(live...)})
}

func (f *wdFixture) passIn(in windowDetectInput) windowReport {
	resetWindowPass()
	return detectWindows(f.root, in)
}

// wdLive always returns a non-nil map, so the detector never falls back to loading liveness itself.
func wdLive(agents ...string) map[string]liveProfile {
	live := map[string]liveProfile{}
	for _, a := range agents {
		live[a] = liveProfile{name: wdProfile, exports: map[string]string{
			"ANTHROPIC_MODEL":                wdModel,
			"CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(wdDeclared),
		}}
	}
	return live
}

func (f *wdFixture) mailsTo(recipient string) []recordedMail {
	var out []recordedMail
	for _, m := range f.mail.sent {
		if m.recipient == recipient {
			out = append(out, m)
		}
	}
	return out
}

func (f *wdFixture) requireMailCount(t *testing.T, want int, why string) {
	t.Helper()
	for _, r := range []string{"manager", "supervisor"} {
		if got := len(f.mailsTo(r)); got != want {
			t.Errorf("%s: %d mails to %s, want %d; all sent: %+v", why, got, r, want, f.mail.sent)
		}
	}
}

func wdWindowAlarms(t *testing.T, root string, now time.Time) map[string]recoveryAlarm {
	t.Helper()
	raised, _ := recoveryAlarms(root, now)
	out := map[string]recoveryAlarm{}
	for _, a := range raised {
		if a.class == "WINDOW" {
			out[a.agent] = a
		}
	}
	return out
}

func wdHasPair(pairs []windowPair, declared, applied int64) bool {
	for _, p := range pairs {
		if p.Declared == declared && p.Applied == applied {
			return true
		}
	}
	return false
}

func wdBreadcrumbLines(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".runtime", "recovery_halt_undelivered"))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

func wdAgentsListWindow(t *testing.T, root string) map[string]map[string]any {
	t.Helper()
	t.Chdir(root)
	installMemStore(t)
	var items []map[string]any
	out := invokeAgentsList(t)
	if err := json.Unmarshal([]byte(out), &items); err != nil {
		t.Fatalf("af agents list --json is not a JSON array: %v\n%s", err, out)
	}
	byName := map[string]map[string]any{}
	for _, it := range items {
		if name, _ := it["name"].(string); name != "" {
			byName[name] = it
		}
	}
	return byName
}

func wdTreeBytes(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		data, _ := os.ReadFile(path)
		out[path] = string(data)
		return nil
	})
	return out
}

func TestWindowDetector_OncePerRecord(t *testing.T) {
	f := newWDFixture(t, "alpha", "beta")
	// A launched_at at a half-hour offset: every JSON read of it builds a new uncached
	// location, so only time.Equal recognises the same launch on the next pass (V4-F10).
	launched := time.Now().Add(-time.Hour).Truncate(time.Second).In(time.FixedZone("", 5400))
	for _, a := range []string{"alpha", "beta"} {
		wdPlantFact(t, f.root, a, wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
		wdPlantSession(t, f.root, a, "sess-"+a, launched.Add(2*time.Second), 200000)
	}

	// The third pass comes after windowLastSeenRefresh, so it rewrites the entry: a launch counted
	// again on any pass would be persisted there.
	now := time.Now()
	for _, at := range []time.Time{now, now.Add(time.Minute), now.Add(windowLastSeenRefresh + time.Hour)} {
		f.pass(at, "alpha", "beta")
	}

	f.requireMailCount(t, 1, "one identity, one (declared, applied) pair, three passes")
	for _, m := range f.mail.sent {
		if !strings.HasPrefix(m.subject, "WINDOW_MISMATCH: ") || !strings.Contains(m.subject, "on profile "+wdProfile) {
			t.Errorf("mail subject = %q, want WINDOW_MISMATCH: <agent> on profile %s", m.subject, wdProfile)
		}
		if !strings.Contains(m.body, "applied 200000") || !strings.Contains(m.body, "1000000-token window") {
			t.Errorf("mail body must carry both figures, got %q", m.body)
		}
	}

	rec, ok := wdEntry(t, f.root, wdProfile, wdKey(wdOld, wdDeclared, wdModel))
	if !ok {
		t.Fatalf("no entry for the launch identity in %s", wdRecordPath(f.root, wdProfile))
	}
	if rec.Sessions != 2 {
		t.Errorf("Sessions = %d after three passes over two launches, want 2", rec.Sessions)
	}
	if !wdHasPair(rec.NotifiedFor, wdDeclared, 200000) {
		t.Errorf("notified_for = %+v, want the pair (1000000, 200000)", rec.NotifiedFor)
	}

	alarms := wdWindowAlarms(t, f.root, now)
	for _, a := range []string{"alpha", "beta"} {
		if _, ok := alarms[a]; !ok {
			t.Errorf("no WINDOW alarm for %s; raised: %+v", a, alarms)
		}
	}
	items := wdAgentsListWindow(t, f.root)
	for _, a := range []string{"alpha", "beta"} {
		it := items[a]
		if it["window_declared"] != float64(wdDeclared) || it["window_applied"] != float64(200000) {
			t.Errorf("af agents list --json %s: window_declared=%v window_applied=%v, want 1000000 and 200000", a, it["window_declared"], it["window_applied"])
		}
	}
}

func TestWindowDetector_OneIdentityTwoWindowsMailOncePerPair(t *testing.T) {
	f := newWDFixture(t, "alpha", "beta")
	launched := time.Now().Add(-time.Hour).Truncate(time.Second)
	wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
	wdPlantFact(t, f.root, "beta", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
	wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
	wdPlantSession(t, f.root, "beta", "sess-beta", launched.Add(2*time.Second), 262144)
	key := wdKey(wdOld, wdDeclared, wdModel)

	now := time.Now()
	for i := 0; i < 3; i++ {
		f.pass(now.Add(time.Duration(i)*time.Minute), "alpha", "beta")
		if rec, ok := wdEntry(t, f.root, wdProfile, key); !ok || rec.Applied != 200000 {
			t.Errorf("after pass %d the entry's Applied = %d (exists %v), want the smallest figure 200000", i+1, rec.Applied, ok)
		}
	}

	f.requireMailCount(t, 2, "two (declared, applied) pairs over three passes")
	for _, r := range []string{"manager", "supervisor"} {
		var saw200, saw262 int
		for _, m := range f.mailsTo(r) {
			if strings.Contains(m.body, "applied 200000") {
				saw200++
			}
			if strings.Contains(m.body, "applied 262144") {
				saw262++
			}
		}
		if saw200 != 1 || saw262 != 1 {
			t.Errorf("mails to %s: %d name applied 200000 and %d applied 262144, want one each", r, saw200, saw262)
		}
	}
	rec := wdMustEntry(t, f.root, wdProfile, key)
	if !wdHasPair(rec.NotifiedFor, wdDeclared, 200000) || !wdHasPair(rec.NotifiedFor, wdDeclared, 262144) {
		t.Errorf("notified_for = %+v, want both pairs", rec.NotifiedFor)
	}

	alarms := wdWindowAlarms(t, f.root, now)
	for agent, fig := range map[string]string{"alpha": "200000", "beta": "262144"} {
		a, ok := alarms[agent]
		if !ok {
			t.Errorf("no WINDOW alarm for %s", agent)
			continue
		}
		if !strings.Contains(a.detail, "applied "+fig) {
			t.Errorf("%s's WINDOW alarm detail %q must carry its own figure %s", agent, a.detail, fig)
		}
	}
	items := wdAgentsListWindow(t, f.root)
	if items["alpha"]["window_applied"] != float64(200000) || items["beta"]["window_applied"] != float64(262144) {
		t.Errorf("window_applied alpha=%v beta=%v, want each agent's own figure 200000 and 262144", items["alpha"]["window_applied"], items["beta"]["window_applied"])
	}

	relaunched := launched.Add(10 * time.Minute)
	wdPlantFact(t, f.root, "beta", wdFact(wdProfile, wdModel, wdDeclared, wdOld, relaunched))
	wdPlantSession(t, f.root, "beta", "sess-beta-2", relaunched.Add(2*time.Second), wdDeclared)
	f.pass(now.Add(5*time.Minute), "alpha", "beta")
	rec = wdMustEntry(t, f.root, wdProfile, key)
	if rec.Applied != 200000 {
		t.Errorf("with beta now reporting the declaration, Applied = %d, want alpha's 200000 to keep refusing", rec.Applied)
	}
	if got := rec.Agents["beta"].Applied; got != wdDeclared {
		t.Errorf("beta's figure = %d, want its new session's %d", got, wdDeclared)
	}
}

func TestWindowDetector_SnapshotBeforeFactPersistedIsNotMisfiled(t *testing.T) {
	t.Run("respawn files the new session under the new identity only", func(t *testing.T) {
		f := newWDFixture(t, "alpha", "beta")
		writeValidModels(t, f.root, &config.ModelsConfig{
			Models: map[string]map[string]string{wdProfile: {
				"ANTHROPIC_MODEL":                wdModel,
				"CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(wdDeclared),
			}},
			Agents: map[string]string{"alpha": wdProfile},
		})
		earlier := time.Now().Add(-2 * time.Hour).Truncate(time.Second)
		oldKey := wdKey(wdOld, wdDeclared, wdModel)
		wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, earlier))
		oldEntry := windowRecord{
			ModelID: wdModel, HarnessVersion: wdOld, Declared: wdDeclared, Applied: 200000,
			Agents: map[string]agentWindow{
				"alpha": {Applied: 200000, LaunchedAt: earlier, LastSeen: earlier.Add(time.Minute)},
				"beta":  {Applied: 200000, LaunchedAt: earlier, LastSeen: earlier.Add(time.Minute)},
			},
			FirstSeen: earlier, LastSeen: earlier.Add(time.Minute), Sessions: 2, Source: "session",
			NotifiedFor: []windowPair{{Declared: wdDeclared, Applied: 200000}},
		}
		wdPlantRecord(t, f.root, wdProfile, map[string]windowRecord{oldKey: oldEntry})
		before := wdMustEntry(t, f.root, wdProfile, oldKey)

		wdStubVersion(t, wdNew)
		var hookRan, factSeen bool
		mock := &mockTmux{}
		mock.onRespawn = func() {
			hookRan = true
			data, err := os.ReadFile(wdFactPath(f.root, "alpha"))
			if err != nil {
				t.Errorf("no launch fact on disk when RespawnPane ran: %v", err)
				return
			}
			var fact windowLaunchFact
			if err := json.Unmarshal(data, &fact); err != nil {
				t.Errorf("launch fact is not JSON: %v", err)
				return
			}
			if fact.HarnessVersion != wdNew || !fact.LaunchedAt.After(earlier) {
				t.Errorf("the fact on disk at RespawnPane is %+v, want this respawn's (version %s, a fresh launched_at)", fact, wdNew)
				return
			}
			factSeen = true
			at := fact.LaunchedAt.Add(2 * time.Second)
			wdPlantSession(t, f.root, "alpha", "sess-alpha-new", at, wdDeclared)
			f.passIn(windowDetectInput{Site: windowAtTick, Now: at.Add(8 * time.Second), Live: wdLive()})
		}
		resetWindowPass()
		err := respawnSession(RespawnOptions{
			FactoryRoot: f.root,
			AgentName:   "alpha",
			AgentEntry:  config.AgentEntry{Type: "autonomous"},
			PaneID:      "%0",
			Tx:          mock,
		})
		if err != nil {
			t.Fatalf("respawnSession: %v", err)
		}
		if !hookRan {
			t.Fatalf("RespawnPane was never reached")
		}
		if !factSeen {
			t.Fatalf("the respawn did not persist its launch fact before RespawnPane")
		}

		newRec, ok := wdEntry(t, f.root, wdProfile, wdKey(wdNew, wdDeclared, wdModel))
		if !ok {
			t.Fatalf("the new session's figure was filed under no %s entry", wdNew)
		}
		if got := newRec.Agents["alpha"].Applied; got != wdDeclared {
			t.Errorf("the %s entry holds alpha's figure %d, want %d", wdNew, got, wdDeclared)
		}
		after := wdMustEntry(t, f.root, wdProfile, oldKey)
		if _, still := after.Agents["alpha"]; still {
			t.Errorf("the %s entry still holds alpha's figure of its earlier launch: %+v", wdOld, after.Agents)
		}
		if got := after.Agents["beta"]; !got.LaunchedAt.Equal(earlier) || got.Applied != 200000 {
			t.Errorf("beta's figure in the %s entry changed: %+v", wdOld, got)
		}
		before.Agents, after.Agents = nil, nil
		if !reflect.DeepEqual(wdComparable(before), wdComparable(after)) {
			t.Errorf("the %s entry changed beyond losing alpha's figure:\n before %+v\n after  %+v", wdOld, before, after)
		}
		for _, m := range f.mail.sent {
			if strings.Contains(m.body, wdOld) || strings.Contains(m.subject, wdOld) {
				t.Errorf("a mail names %s: %+v", wdOld, m)
			}
		}
	})

	t.Run("snapshot of another session is filed under no identity", func(t *testing.T) {
		f := newWDFixture(t, "alpha")
		launched := time.Now().Add(-time.Hour).Truncate(time.Second)
		wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
		at := launched.Add(2 * time.Second)
		wdPlantSessionID(t, f.root, "alpha", "sess-current", at)
		plantSessionSnapshotWithTotal(t, f.root, "alpha", "sess-replaced", 40, 200000, 1000, at, at)
		f.pass(time.Now())
		wdRequireNoFigure(t, f, "alpha")

		plantSessionSnapshotWithTotal(t, f.root, "alpha", "sess-current", 40, 200000, 1000, at, at)
		f.pass(time.Now())
		wdRequireFigure(t, f, "alpha", 200000)
	})

	t.Run("session id written at launched_at is filed under no identity", func(t *testing.T) {
		f := newWDFixture(t, "alpha")
		launched := time.Now().Add(-time.Hour).Truncate(time.Second)
		wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
		wdPlantSessionID(t, f.root, "alpha", "sess-a", launched)
		plantSessionSnapshotWithTotal(t, f.root, "alpha", "sess-a", 40, 200000, 1000, launched.Add(2*time.Second), launched.Add(2*time.Second))
		f.pass(time.Now())
		wdRequireNoFigure(t, f, "alpha")

		wdPlantSessionID(t, f.root, "alpha", "sess-a", launched.Add(2*time.Second))
		f.pass(time.Now())
		wdRequireFigure(t, f, "alpha", 200000)
	})
}

// wdComparable drops the time zone and monotonic reading a JSON round trip changes, so two reads of
// the same bytes compare equal.
func wdComparable(r windowRecord) windowRecord {
	r.FirstSeen, r.LastSeen = r.FirstSeen.UTC(), r.LastSeen.UTC()
	return r
}

func wdRequireNoFigure(t *testing.T, f *wdFixture, agent string) {
	t.Helper()
	rf, _ := wdReadRecord(t, f.root, wdProfile)
	for key, rec := range rf.Identities {
		if _, ok := rec.Agents[agent]; ok {
			t.Errorf("entry %q holds a figure for %s although no observation belongs to its launch: %+v", key, agent, rec.Agents)
		}
	}
	if len(f.mail.sent) != 0 {
		t.Errorf("mail sent for an observation of another session: %+v", f.mail.sent)
	}
}

func wdRequireFigure(t *testing.T, f *wdFixture, agent string, want int64) {
	t.Helper()
	rec, ok := wdEntry(t, f.root, wdProfile, wdKey(wdOld, wdDeclared, wdModel))
	if !ok || rec.Agents[agent].Applied != want {
		t.Errorf("control: once the observation belongs to this launch, %s's figure must be %d; entry exists=%v agents=%+v", agent, want, ok, rec.Agents)
	}
}

func TestWindowDetector_TwoIdentitiesOneProfileMailOnce(t *testing.T) {
	for _, tc := range []struct {
		name                string
		alphaV, betaV       string
		alphaDecl, betaDecl int64
	}{
		{name: "harness versions differ", alphaV: wdOld, betaV: wdNew, alphaDecl: wdDeclared, betaDecl: wdDeclared},
		{name: "declarations differ", alphaV: wdOld, betaV: wdOld, alphaDecl: wdDeclared, betaDecl: 500000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWDFixture(t, "alpha", "beta")
			launched := time.Now().Add(-time.Hour).Truncate(time.Second)
			wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, tc.alphaDecl, tc.alphaV, launched))
			wdPlantFact(t, f.root, "beta", wdFact(wdProfile, wdModel, tc.betaDecl, tc.betaV, launched))
			wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
			wdPlantSession(t, f.root, "beta", "sess-beta", launched.Add(2*time.Second), 200000)

			now := time.Now()
			f.pass(now, "alpha", "beta")
			f.requireMailCount(t, 2, "first pass: one mail per identity")
			f.pass(now.Add(time.Minute), "alpha", "beta")
			f.pass(now.Add(2*time.Minute), "alpha", "beta")
			f.requireMailCount(t, 2, "the next two passes mail nothing")

			for agent, key := range map[string]string{
				"alpha": wdKey(tc.alphaV, tc.alphaDecl, wdModel),
				"beta":  wdKey(tc.betaV, tc.betaDecl, wdModel),
			} {
				rec, ok := wdEntry(t, f.root, wdProfile, key)
				if !ok {
					t.Errorf("no entry %q for %s's identity in %s", key, agent, wdRecordPath(f.root, wdProfile))
					continue
				}
				if rec.Sessions != 1 {
					t.Errorf("entry %q Sessions = %d after three passes, want 1", key, rec.Sessions)
				}
				if len(rec.NotifiedFor) != 1 || !wdHasPair(rec.NotifiedFor, rec.Declared, 200000) {
					t.Errorf("entry %q notified_for = %+v, want only its own pair (%d, 200000)", key, rec.NotifiedFor, rec.Declared)
				}
				if _, other := rec.Agents[map[string]string{"alpha": "beta", "beta": "alpha"}[agent]]; other {
					t.Errorf("entry %q holds the other identity's agent: %+v", key, rec.Agents)
				}
			}
		})
	}
}

func TestWindowDetector_UndeliveredBreadcrumb(t *testing.T) {
	setup := func(t *testing.T) (*wdFixture, string) {
		f := newWDFixture(t, "alpha")
		launched := time.Now().Add(-time.Hour).Truncate(time.Second)
		wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
		wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
		return f, wdKey(wdOld, wdDeclared, wdModel)
	}
	requireMarkedAndAlarmed := func(t *testing.T, f *wdFixture, key string) {
		t.Helper()
		rec, ok := wdEntry(t, f.root, wdProfile, key)
		if !ok || !wdHasPair(rec.NotifiedFor, wdDeclared, 200000) {
			t.Errorf("notified_for must hold (1000000, 200000) once the send or its breadcrumb is done; entry exists=%v notified_for=%+v", ok, rec.NotifiedFor)
		}
		if _, ok := wdWindowAlarms(t, f.root, time.Now())["alpha"]; !ok {
			t.Errorf("the WINDOW alarm must stand whatever the mail's fate")
		}
	}
	requireWindowCrumb := func(t *testing.T, root, recipient string) {
		t.Helper()
		for _, line := range wdBreadcrumbLines(t, root) {
			fields := strings.Fields(line)
			if len(fields) > 1 && fields[1] == "WINDOW" && strings.Contains(line, fmt.Sprintf("%q", recipient)) {
				return
			}
		}
		t.Errorf("no WINDOW-led breadcrumb line naming %q in .runtime/recovery_halt_undelivered: %q", recipient, wdBreadcrumbLines(t, root))
	}

	t.Run("undeliverable recipient writes the shared breadcrumb and sets notified_for", func(t *testing.T) {
		f, key := setup(t)
		wdWriteJSON(t, config.AgentsConfigPath(f.root), map[string]any{"agents": map[string]any{
			"manager": map[string]string{"type": "interactive", "description": "m"},
			"alpha":   map[string]string{"type": "autonomous", "description": "a"},
		}})
		f.pass(time.Now(), "alpha")
		if got := len(f.mailsTo("manager")); got != 1 {
			t.Errorf("%d mails to the reachable manager, want 1", got)
		}
		if got := len(f.mailsTo("supervisor")); got != 0 {
			t.Errorf("%d mails filed to the unrostered supervisor, want 0", got)
		}
		requireWindowCrumb(t, f.root, "supervisor")
		requireMarkedAndAlarmed(t, f, key)
	})

	t.Run("failed send writes the shared breadcrumb and sets notified_for", func(t *testing.T) {
		f, key := setup(t)
		f.mail.err = errors.New("store unavailable")
		f.pass(time.Now(), "alpha")
		requireWindowCrumb(t, f.root, "manager")
		requireWindowCrumb(t, f.root, "supervisor")
		requireMarkedAndAlarmed(t, f, key)
		f.mail.err = nil
		f.pass(time.Now().Add(time.Minute), "alpha")
		f.requireMailCount(t, 1, "a pair marked after its failed send is not mailed again")
	})

	t.Run("empty notified_for is mailed by the next run", func(t *testing.T) {
		f, key := setup(t)
		launched := time.Now().Add(-time.Hour).Truncate(time.Second)
		seen := time.Now().Add(-time.Minute)
		wdPlantRecord(t, f.root, wdProfile, map[string]windowRecord{key: {
			ModelID: wdModel, HarnessVersion: wdOld, Declared: wdDeclared, Applied: 200000,
			Agents:    map[string]agentWindow{"alpha": {Applied: 200000, LaunchedAt: launched, LastSeen: seen}},
			FirstSeen: seen, LastSeen: seen, Sessions: 1, Source: "session",
		}})
		f.pass(time.Now(), "alpha")
		f.requireMailCount(t, 1, "a divergent figure whose pair was never announced")
		requireMarkedAndAlarmed(t, f, key)
	})
}

func TestWindowDetector_Unobserved(t *testing.T) {
	type fx struct {
		*wdFixture
		launched time.Time
		key      string
	}
	setup := func(t *testing.T, gateOn, statusLine bool) fx {
		f := newWDFixture(t, "alpha", "beta")
		f.tmux.present[session.SessionName("alpha")] = true
		if gateOn {
			if err := os.WriteFile(statuslineGateFile(f.root), []byte("on\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if statusLine {
			wdWriteJSON(t, filepath.Join(config.AgentDir(f.root, "alpha"), ".claude", "settings.json"),
				map[string]any{"statusLine": map[string]string{"type": "command", "command": "af statusline render"}})
		}
		launched := time.Now().Add(-time.Hour).Truncate(time.Second)
		wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
		return fx{wdFixture: f, launched: launched, key: wdKey(wdOld, wdDeclared, wdModel)}
	}
	requireUnobservedMail := func(t *testing.T, f fx, cause string) {
		t.Helper()
		f.requireMailCount(t, 1, "the unobserved state is mailed once per identity")
		for _, m := range f.mail.sent {
			if !strings.Contains(m.body, "has reported no window ("+cause+")") {
				t.Errorf("unobserved body must name the cause %q, got %q", cause, m.body)
			}
			if !strings.Contains(m.body, `agent "alpha": profile "`+wdProfile+`" declares a 1000000-token window on Claude Code `+wdOld) {
				t.Errorf("unobserved body must name the agent, profile, declaration and version, got %q", m.body)
			}
			if strings.Contains(m.body, "applied 0") {
				t.Errorf("unobserved body must never say applied 0, got %q", m.body)
			}
			if m.subject != "WINDOW_MISMATCH: alpha on profile "+wdProfile {
				t.Errorf("subject = %q, want the one K5 subject", m.subject)
			}
		}
		rec, ok := wdEntry(t, f.root, wdProfile, f.key)
		if !ok || !rec.UnobservedNotified {
			t.Errorf("the entry must carry unobserved_notified; exists=%v rec=%+v", ok, rec)
		}
		if fig, has := rec.Agents["alpha"]; has {
			t.Errorf("the unobserved mark wrote a figure for alpha: %+v", fig)
		}
		if len(rec.Agents) == 0 && rec.Applied != 0 {
			t.Errorf("the unobserved mark wrote a positive applied %d", rec.Applied)
		}
		if a, ok := wdWindowAlarms(t, f.root, time.Now())["alpha"]; !ok || a.reason != "unobserved" {
			t.Errorf("want a WINDOW alarm with reason unobserved for alpha, got %+v (raised %v)", a, ok)
		}
	}

	t.Run("statusline gate off names the gate", func(t *testing.T) {
		f := setup(t, false, true)
		f.pass(time.Now(), "alpha")
		requireUnobservedMail(t, f, "statusline gate off")
	})

	t.Run("no snapshot yet", func(t *testing.T) {
		f := setup(t, true, true)
		wdPlantSessionID(t, f.root, "alpha", "sess-alpha", f.launched.Add(2*time.Second))
		f.pass(time.Now(), "alpha")
		requireUnobservedMail(t, f, "no snapshot yet")
	})

	t.Run("snapshot without a window", func(t *testing.T) {
		f := setup(t, true, true)
		at := f.launched.Add(2 * time.Second)
		wdPlantSessionID(t, f.root, "alpha", "sess-alpha", at)
		wdWriteJSON(t, filepath.Join(config.StatuslineSessionsDir(f.root), "sess-alpha.json"), map[string]any{
			"schema": 2, "session_id": "sess-alpha", "agent": "alpha",
			"date": at.Format("2006-01-02"), "written_at": at.UTC().Format(time.RFC3339),
		})
		f.pass(time.Now(), "alpha")
		requireUnobservedMail(t, f, "snapshot without a window")
	})

	t.Run("observed identity gets no unobserved mail", func(t *testing.T) {
		f := setup(t, true, true)
		wdPlantSession(t, f.root, "alpha", "sess-alpha", f.launched.Add(2*time.Second), wdDeclared)
		f.pass(time.Now(), "alpha")
		if len(f.mail.sent) != 0 {
			t.Errorf("an agreeing observed launch mailed: %+v", f.mail.sent)
		}
		rec, ok := wdEntry(t, f.root, wdProfile, f.key)
		if !ok || rec.UnobservedNotified || rec.Agents["alpha"].Applied != wdDeclared {
			t.Errorf("want the entry to hold alpha's agreeing figure and no unobserved mark; exists=%v rec=%+v", ok, rec)
		}
	})

	t.Run("session row of another identity does not count as observed", func(t *testing.T) {
		f := setup(t, true, true)
		seen := time.Now().Add(-time.Minute)
		wdPlantRecord(t, f.root, wdProfile, map[string]windowRecord{wdKey(wdNew, wdDeclared, wdModel): {
			ModelID: wdModel, HarnessVersion: wdNew, Declared: wdDeclared, Applied: 200000,
			Agents:    map[string]agentWindow{"beta": {Applied: 200000, LaunchedAt: f.launched, LastSeen: seen}},
			FirstSeen: seen, LastSeen: seen, Sessions: 1, Source: "session",
		}})
		f.pass(time.Now(), "alpha")
		requireUnobservedMail(t, f, "no snapshot yet")
	})

	t.Run("figure of another agent does not count as observed", func(t *testing.T) {
		f := setup(t, true, true)
		seen := time.Now().Add(-time.Minute)
		wdPlantRecord(t, f.root, wdProfile, map[string]windowRecord{f.key: {
			ModelID: wdModel, HarnessVersion: wdOld, Declared: wdDeclared, Applied: 200000,
			Agents:    map[string]agentWindow{"beta": {Applied: 200000, LaunchedAt: f.launched, LastSeen: seen}},
			FirstSeen: seen, LastSeen: seen, Sessions: 1, Source: "session",
			NotifiedFor: []windowPair{{Declared: wdDeclared, Applied: 200000}},
		}})
		f.pass(time.Now(), "alpha")
		requireUnobservedMail(t, f, "no snapshot yet")
		if got := wdMustEntry(t, f.root, wdProfile, f.key).Agents["beta"].Applied; got != 200000 {
			t.Errorf("the mark overwrote beta's figure: %d", got)
		}
	})

	t.Run("stopped session gets no unobserved mail", func(t *testing.T) {
		f := setup(t, true, true)
		f.tmux.present[session.SessionName("alpha")] = false
		f.pass(time.Now())
		if len(f.mail.sent) != 0 {
			t.Errorf("an agent whose session stopped was mailed unobserved: %+v", f.mail.sent)
		}
		f.tmux.present[session.SessionName("alpha")] = true
		f.pass(time.Now(), "alpha")
		requireUnobservedMail(t, f, "no snapshot yet")
	})

	t.Run("stopped agent on a marked identity raises no alarm", func(t *testing.T) {
		f := setup(t, true, true)
		wdPlantFact(t, f.root, "beta", wdFact(wdProfile, wdModel, wdDeclared, wdOld, f.launched))
		f.pass(time.Now(), "alpha")
		requireUnobservedMail(t, f, "no snapshot yet")
		for _, m := range f.mail.sent {
			if strings.Contains(m.body, `"beta"`) || strings.Contains(m.subject, "beta") {
				t.Errorf("the stopped beta was mailed: %+v", m)
			}
		}
		if a, ok := wdWindowAlarms(t, f.root, time.Now())["beta"]; ok {
			t.Errorf("beta is stopped: the mark alpha's silence set on the shared identity must not alarm it, got %+v", a)
		}
	})

	t.Run("standing unobserved entry is refreshed and not re-mailed", func(t *testing.T) {
		f := setup(t, true, true)
		t0 := time.Now()
		f.pass(t0, "alpha")
		requireUnobservedMail(t, f, "no snapshot yet")
		for day := 1; day <= 31; day++ {
			now := t0.Add(time.Duration(day) * (windowLastSeenRefresh + time.Hour))
			f.pass(now, "alpha")
			rec, ok := wdEntry(t, f.root, wdProfile, f.key)
			if !ok {
				t.Fatalf("day %d: the standing unobserved entry is gone", day)
			}
			if !rec.LastSeen.Equal(now) {
				t.Errorf("day %d: last_seen = %s, want this pass's %s (re-stamped)", day, rec.LastSeen, now)
			}
			if !rec.UnobservedNotified || len(rec.Agents) != 0 || rec.Applied != 0 {
				t.Errorf("day %d: the refresh changed the entry: %+v", day, rec)
			}
		}
		f.requireMailCount(t, 1, "31 daily refreshes of a standing mark")
	})
}

// wdStatusFactory is the advisory fixture with one live agent, manager, launched on the profile
// fable-5-2 and reporting 200000 from its own session.
func wdStatusFactory(t *testing.T) (string, *mailRecorder) {
	t.Helper()
	root, _ := advisoryRoot(t, &config.ModelsConfig{
		Models: map[string]map[string]string{wdProfile: {
			"ANTHROPIC_MODEL":                wdModel,
			"CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(wdDeclared),
		}},
		Agents: map[string]string{"manager": wdProfile},
	}, "manager")
	mail := (&mailRecorder{}).install(t)
	wdStubVersion(t, wdOld)
	resetWindowPass()
	t.Cleanup(resetWindowPass)
	launched := time.Now().Add(-time.Hour).Truncate(time.Second)
	wdPlantFact(t, root, "manager", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
	at := time.Now().Add(-freshSnapshotAge).Truncate(time.Second)
	wdPlantSessionID(t, root, "manager", "sess-manager", at)
	plantSessionSnapshotWithTotal(t, root, "manager", "sess-manager", 40, 200000, 1000, at, time.Now())
	return root, mail
}

func TestWindowDetector_StatusFirstRunShowsItsAlarm(t *testing.T) {
	root, _ := wdStatusFactory(t)
	if _, ok := wdReadRecord(t, root, wdProfile); ok {
		t.Fatalf("precondition: no record before the first status run")
	}
	out := statusStdout(t)
	if line := findLineWith(out, "WINDOW manager", "(mismatch)"); line == "" {
		t.Errorf("the status run that first records the divergence must print its WINDOW alarm; got:\n%s", out)
	}
	alerts := strings.Index(out, "alerts:")
	window := strings.Index(out, "WINDOW manager")
	if alerts < 0 || window < alerts {
		t.Errorf("the WINDOW line must sit in the alerts block; got:\n%s", out)
	}
}

func TestWindowDetector_StatusReadOnlyOnDowngradedRoot(t *testing.T) {
	root, mail := wdStatusFactory(t)
	other := newTestFactoryRoot(t)
	t.Setenv("AF_ROOT", other)
	before := wdTreeBytes(t, wdWindowsDir(root))

	out := statusStdout(t)

	if after := wdTreeBytes(t, wdWindowsDir(root)); !reflect.DeepEqual(before, after) {
		t.Errorf("status on a downgraded root wrote under windows/:\n before %v\n after  %v", before, after)
	}
	if len(mail.sent) != 0 {
		t.Errorf("status on a downgraded root sent mail: %+v", mail.sent)
	}
	if lines := wdBreadcrumbLines(t, root); len(lines) != 0 {
		t.Errorf("status on a downgraded root wrote a breadcrumb: %q", lines)
	}
	if line := findLineWith(out, "manager", wdProfile, "operator not yet notified"); line == "" {
		t.Errorf("the finding must still be printed with `operator not yet notified`; got:\n%s", out)
	}
}

func TestWindowDetector_EndedSessionStillRecorded(t *testing.T) {
	f := newWDFixture(t, "alpha")
	launched := time.Now().Add(-3 * time.Hour).Truncate(time.Second)
	wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
	wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)

	f.pass(time.Now())

	rec, ok := wdEntry(t, f.root, wdProfile, wdKey(wdOld, wdDeclared, wdModel))
	if !ok || rec.Agents["alpha"].Applied != 200000 {
		t.Errorf("a stale snapshot of an ended session bound to its launch must be recorded; exists=%v agents=%+v", ok, rec.Agents)
	}
	f.requireMailCount(t, 1, "an ended session's divergence")
}

func TestWindowDetector_IgnoresPredecessorSnapshot(t *testing.T) {
	for _, tc := range []struct {
		name            string
		profile, newVer string
		sidSurvives     bool
	}{
		{name: "new version", profile: wdProfile, newVer: wdNew},
		{name: "new profile", profile: "fable-5-2-large", newVer: wdOld},
		{name: "session id rewritten after the relaunch, snapshot from before it", profile: wdProfile, newVer: wdNew, sidSurvives: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWDFixture(t, "alpha")
			resetHarnessProbeFailures()
			t.Cleanup(resetHarnessProbeFailures)
			earlier := time.Now().Add(-time.Hour).Truncate(time.Second)
			oldKey := wdKey(wdOld, wdDeclared, wdModel)
			wdPlantSessionID(t, f.root, "alpha", "sess-old", earlier.Add(2*time.Second))
			fresh := time.Now().Add(-10 * time.Second).Truncate(time.Second)
			plantSessionSnapshotWithTotal(t, f.root, "alpha", "sess-old", 40, 200000, 1000, fresh, time.Now())
			seen := time.Now().Add(-time.Minute)
			wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, earlier))
			wdPlantRecord(t, f.root, wdProfile, map[string]windowRecord{oldKey: {
				ModelID: wdModel, HarnessVersion: wdOld, Declared: wdDeclared, Applied: 200000,
				Agents:    map[string]agentWindow{"alpha": {Applied: 200000, LaunchedAt: earlier, LastSeen: seen}},
				FirstSeen: seen, LastSeen: seen, Sessions: 1, Source: "session",
				NotifiedFor: []windowPair{{Declared: wdDeclared, Applied: 200000}},
			}})
			if _, ok := wdWindowAlarms(t, f.root, time.Now())["alpha"]; !ok {
				t.Errorf("precondition: alpha's old launch must raise WINDOW")
			}
			oldBytes, _ := os.ReadFile(wdRecordPath(f.root, wdProfile))

			relaunched := time.Now().Add(-5 * time.Second).Truncate(time.Second)
			wdPlantFact(t, f.root, "alpha", wdFact(tc.profile, wdModel, wdDeclared, tc.newVer, relaunched))
			if tc.sidSurvives {
				wdPlantSessionID(t, f.root, "alpha", "sess-old", relaunched.Add(time.Second))
			}
			newKey := wdKey(tc.newVer, wdDeclared, wdModel)
			f.pass(time.Now(), "alpha")

			if _, ok := wdEntry(t, f.root, tc.profile, newKey); ok {
				t.Errorf("the predecessor's fresh snapshot created an entry for the new identity")
			}
			if len(f.mail.sent) != 0 {
				t.Errorf("the predecessor's snapshot was mailed: %+v", f.mail.sent)
			}
			newProfile := map[string]string{"ANTHROPIC_MODEL": wdModel, config.EnvMaxContextTokens: "1000000"}
			gates := launchGates{ReportCoverage: true, HarnessVersion: tc.newVer}
			if err := windowGate(f.root, "alpha", tc.profile, newProfile, gates, &strings.Builder{}); err != nil {
				t.Errorf("the predecessor's snapshot refused the new identity's watched launch: %v", err)
			}
			if _, ok := wdWindowAlarms(t, f.root, time.Now())["alpha"]; ok {
				t.Errorf("alpha's old WINDOW alarm must end at its relaunch on another identity")
			}
			if now, _ := os.ReadFile(wdRecordPath(f.root, wdProfile)); string(now) != string(oldBytes) {
				t.Errorf("the old identity's record changed:\n before %s\n after  %s", oldBytes, now)
			}

			at := relaunched.Add(2 * time.Second)
			wdPlantSession(t, f.root, "alpha", "sess-new", at, wdDeclared)
			f.pass(at.Add(8*time.Second), "alpha")
			rec, ok := wdEntry(t, f.root, tc.profile, newKey)
			if !ok || rec.Agents["alpha"].Applied != wdDeclared {
				t.Errorf("the first snapshot after launched_at must create the new entry with alpha's figure; exists=%v agents=%+v", ok, rec.Agents)
			}
			old := wdMustEntry(t, f.root, wdProfile, oldKey)
			_, kept := old.Agents["alpha"]
			if wantKept := tc.profile != wdProfile; kept != wantKept {
				t.Errorf("old entry keeps alpha's earlier figure = %v, want %v (dropped only when the new identity shares the profile file)", kept, wantKept)
			}
		})
	}
}

func TestWindowDetector_FirstObservationWithoutDeclaration(t *testing.T) {
	f := newWDFixture(t, "alpha")
	launched := time.Now().Add(-time.Hour).Truncate(time.Second)
	wdPlantFact(t, f.root, "alpha", wdFact("local", "claude-opus-5", 0, wdOld, launched))
	wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)

	f.pass(time.Now(), "alpha")
	f.requireMailCount(t, 1, "the first observation of an undeclared profile")
	want := `profile "local" declares no window; Claude Code 2.1.308 applied 200000 to model "claude-opus-5"`
	for _, m := range f.mail.sent {
		if strings.TrimSpace(m.body) != want {
			t.Errorf("first-observation body = %q, want %q", m.body, want)
		}
	}
	f.pass(time.Now().Add(time.Minute), "alpha")
	f.requireMailCount(t, 1, "a second pass over the same figure")
	if rec, ok := wdEntry(t, f.root, "local", wdKey(wdOld, 0, "claude-opus-5")); !ok || !wdHasPair(rec.NotifiedFor, 0, 200000) {
		t.Errorf("notified_for must hold (0, 200000); exists=%v rec=%+v", ok, rec)
	}
	if a, ok := wdWindowAlarms(t, f.root, time.Now())["alpha"]; ok {
		t.Errorf("nothing declared raises no WINDOW alarm, got %+v", a)
	}
}

// wdFuncDecls parses one production file of this package and returns the body of each top-level
// function by name (methods by their own name), and of each `var name = func(...)` seam.
func wdFuncDecls(t *testing.T, file string) (*token.FileSet, map[string]*ast.BlockStmt) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, file, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", file, err)
	}
	out := map[string]*ast.BlockStmt{}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Body != nil {
				out[d.Name.Name] = d.Body
			}
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				vs, ok := spec.(*ast.ValueSpec)
				if !ok {
					continue
				}
				for i, v := range vs.Values {
					if lit, ok := v.(*ast.FuncLit); ok && i < len(vs.Names) {
						out[vs.Names[i].Name] = lit.Body
					}
				}
			}
		}
	}
	return fset, out
}

func wdCalleeName(call *ast.CallExpr) string {
	switch fn := call.Fun.(type) {
	case *ast.Ident:
		return fn.Name
	case *ast.SelectorExpr:
		return fn.Sel.Name
	}
	return ""
}

func wdIsSelector(e ast.Expr, x, sel string) bool {
	s, ok := e.(*ast.SelectorExpr)
	if !ok || s.Sel.Name != sel {
		return false
	}
	id, ok := s.X.(*ast.Ident)
	return ok && id.Name == x
}

// wdCallPositions returns the position of every call to name inside body.
func wdCallPositions(body ast.Node, name string) []token.Pos {
	var out []token.Pos
	ast.Inspect(body, func(n ast.Node) bool {
		if c, ok := n.(*ast.CallExpr); ok && wdCalleeName(c) == name {
			out = append(out, c.Pos())
		}
		return true
	})
	return out
}

func wdRequireOneCallBefore(t *testing.T, file, fn, call, before, after string) {
	t.Helper()
	_, decls := wdFuncDecls(t, file)
	body := decls[fn]
	if body == nil {
		t.Errorf("%s declares no func %s", file, fn)
		return
	}
	got := wdCallPositions(body, call)
	if len(got) != 1 {
		t.Errorf("%s: %s calls %s %d times, want exactly 1", file, fn, call, len(got))
		return
	}
	if before != "" {
		b := wdCallPositions(body, before)
		if len(b) == 0 || got[0] < b[0] {
			t.Errorf("%s: in %s the %s call must come after %s(", file, fn, call, before)
		}
	}
	if after != "" {
		a := wdCallPositions(body, after)
		if len(a) == 0 || got[0] > a[0] {
			t.Errorf("%s: in %s the %s call must come before %s(", file, fn, call, after)
		}
	}
}

// wdStartOnlyThroughGate checks that every reference to x.sel inside fn is the start passed as the
// last argument of startWithWindowLaunchFact (or, for a literal, lies inside that argument).
func wdStartOnlyThroughGate(t *testing.T, file, fn, x, sel string, literal bool) {
	t.Helper()
	_, decls := wdFuncDecls(t, file)
	body := decls[fn]
	if body == nil {
		t.Errorf("%s declares no func %s", file, fn)
		return
	}
	allowed := map[ast.Node]bool{}
	gates := 0
	ast.Inspect(body, func(n ast.Node) bool {
		c, ok := n.(*ast.CallExpr)
		if !ok || wdCalleeName(c) != "startWithWindowLaunchFact" || len(c.Args) == 0 {
			return true
		}
		gates++
		last := c.Args[len(c.Args)-1]
		if literal {
			if lit, ok := last.(*ast.FuncLit); ok {
				ast.Inspect(lit, func(m ast.Node) bool {
					if s, ok := m.(ast.Expr); ok && wdIsSelector(s, x, sel) {
						allowed[m] = true
					}
					return true
				})
			}
		} else if wdIsSelector(last, x, sel) {
			allowed[last] = true
		}
		return true
	})
	if gates != 1 {
		t.Errorf("%s: %s calls startWithWindowLaunchFact %d times, want exactly 1", file, fn, gates)
	}
	refs, stray := 0, 0
	ast.Inspect(body, func(n ast.Node) bool {
		if e, ok := n.(ast.Expr); ok && wdIsSelector(e, x, sel) {
			refs++
			if !allowed[n] {
				stray++
			}
		}
		return true
	})
	if refs == 0 || stray != 0 {
		t.Errorf("%s: %s references %s.%s %d times, %d of them outside startWithWindowLaunchFact's start argument", file, fn, x, sel, refs, stray)
	}
}

func TestWindowDetector_Placement(t *testing.T) {
	t.Run("source", func(t *testing.T) {
		wdRequireOneCallBefore(t, "watchdog.go", "watchdogTick", "detectWindowsFn", "pollOccupancyFn", "pollAgents")
		wdRequireOneCallBefore(t, "launch_contributions.go", "launchContributions", "detectWindowsFn", "", "resolveModelEnvForSession")
		wdRequireOneCallBefore(t, "statusline.go", "printStatuslineStatus", "detectWindowsFn", "", "recoveryAlertsNote")
		for _, file := range []string{"watchdog.go", "launch_contributions.go", "statusline.go"} {
			_, decls := wdFuncDecls(t, file)
			n := 0
			for _, body := range decls {
				n += len(wdCallPositions(body, "detectWindowsFn"))
			}
			if n != 1 {
				t.Errorf("%s calls detectWindowsFn %d times, want exactly 1", file, n)
			}
		}

		_, decls := wdFuncDecls(t, "watchdog.go")
		if tick := decls["watchdogTick"]; tick == nil || len(tick.List) == 0 {
			t.Errorf("watchdog.go declares no watchdogTick body")
		} else {
			first, ok := tick.List[0].(*ast.ExprStmt)
			call, isCall := (*ast.CallExpr)(nil), false
			if ok {
				call, isCall = first.X.(*ast.CallExpr)
			}
			if !isCall || wdCalleeName(call) != "resetWindowPass" {
				t.Errorf("the first statement of watchdogTick must be resetWindowPass()")
			}
		}

		wdStartOnlyThroughGate(t, "up.go", "runUp", "mgr", "Start", false)
		wdStartOnlyThroughGate(t, "sling.go", "launchAgentSession", "mgr", "Start", false)
		wdStartOnlyThroughGate(t, "helpers.go", "respawnSession", "tx", "RespawnPane", true)

		files, err := filepath.Glob("*.go")
		if err != nil {
			t.Fatal(err)
		}
		inside, outside := 0, []string{}
		for _, file := range files {
			if strings.HasSuffix(file, "_test.go") {
				continue
			}
			_, decls := wdFuncDecls(t, file)
			for name, body := range decls {
				n := len(wdCallPositions(body, "persistWindowLaunchFact"))
				if name == "startWithWindowLaunchFact" {
					inside += n
				} else if n > 0 {
					outside = append(outside, file+":"+name)
				}
			}
		}
		sort.Strings(outside)
		if inside != 1 || len(outside) != 0 {
			t.Errorf("persistWindowLaunchFact must be called once, inside startWithWindowLaunchFact only: inside=%d elsewhere=%v", inside, outside)
		}
	})

	t.Run("tick with recovery off and the agent excluded still records", func(t *testing.T) {
		f := newWDFixture(t, "alpha")
		wdWriteJSON(t, config.StartupConfigPath(f.root), map[string]any{"recovery": map[string]any{"enabled": false, "exclude": []string{"alpha"}}})
		launched := time.Now().Add(-time.Hour).Truncate(time.Second)
		wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
		wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
		wdQuietTick(t)
		stubIntegrationServicesGuard(t, nil)

		watchdogTick(&cobra.Command{}, f.root, buildWatchdogScope([]string{"alpha"}, ""),
			map[string]*watchdogAgentState{}, map[string]int{}, 2)

		if rec, ok := wdEntry(t, f.root, wdProfile, wdKey(wdOld, wdDeclared, wdModel)); !ok || rec.Agents["alpha"].Applied != 200000 {
			t.Errorf("the tick's detector must record alpha's 200000 whatever recovery's own scope; exists=%v agents=%+v", ok, rec.Agents)
		}
	})

	t.Run("respawn inside a tick stamps the version read in that tick", func(t *testing.T) {
		f := newWDFixture(t, "alpha")
		writeValidModels(t, f.root, &config.ModelsConfig{
			Models: map[string]map[string]string{wdProfile: {
				"ANTHROPIC_MODEL":                wdModel,
				"CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(wdDeclared),
			}},
			Agents: map[string]string{"alpha": wdProfile},
		})
		wdQuietTick(t)
		wdStubVersion(t, wdOld)
		if got := windowHarnessVersion(f.root); got != wdOld {
			t.Errorf("precondition: windowHarnessVersion = %q, want %q", got, wdOld)
		}
		wdStubVersion(t, wdNew)

		origPoll := pollOccupancyFn
		t.Cleanup(func() { pollOccupancyFn = origPoll })
		var respawnErr error
		pollOccupancyFn = func(root string, _ *config.AgentConfig, _ config.RecoveryConfig, _ time.Time) []recoveryDecision {
			respawnErr = respawnSession(RespawnOptions{
				FactoryRoot: root, AgentName: "alpha", AgentEntry: config.AgentEntry{Type: "autonomous"},
				PaneID: "%0", Tx: &mockTmux{},
			})
			return nil
		}
		stubIntegrationServicesGuard(t, nil)
		watchdogTick(&cobra.Command{}, f.root, buildWatchdogScope([]string{"alpha"}, ""),
			map[string]*watchdogAgentState{}, map[string]int{}, 2)

		if respawnErr != nil {
			t.Fatalf("respawn inside the tick: %v", respawnErr)
		}
		data, err := os.ReadFile(wdFactPath(f.root, "alpha"))
		if err != nil {
			t.Fatalf("the respawn persisted no launch fact: %v", err)
		}
		var fact windowLaunchFact
		if err := json.Unmarshal(data, &fact); err != nil {
			t.Fatalf("launch fact: %v", err)
		}
		if fact.HarnessVersion != wdNew {
			t.Errorf("the tick's respawn stamped %q, want the version read in this tick, %q (M2)", fact.HarnessVersion, wdNew)
		}
	})
}

// wdQuietTick stubs every tick effect that would otherwise reach a real backend or tmux.
func wdQuietTick(t *testing.T) {
	t.Helper()
	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)
	stubIntegrationServicesGuard(t, nil)
	oldTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return &fakeWatchdogTmux{output: "working"} }
	t.Cleanup(func() { newWatchdogTmux = oldTmux })
}

// wdFlakyTmux fails the agent's first HasSession: up.go:273-274 reads that probe error as not live,
// and every later probe sees the running session.
type wdFlakyTmux struct {
	*fakeTmux
	session string
	calls   int
}

func (f *wdFlakyTmux) HasSession(name string) (bool, error) {
	if name == f.session {
		f.calls++
		if f.calls == 1 {
			f.record("HasSession " + name)
			return false, errors.New("tmux probe failed")
		}
	}
	return f.fakeTmux.HasSession(name)
}

// wdFactWatchTmux checks the agent's launch fact at every probe of its session, so a launch that
// persists and then undoes a fact around Start is seen even though the file ends byte-identical.
type wdFactWatchTmux struct {
	*fakeTmux
	session, path, want string
	probes              int
	touched             bool
}

func (w *wdFactWatchTmux) HasSession(name string) (bool, error) {
	if name == w.session {
		w.probes++
		if got, _ := os.ReadFile(w.path); string(got) != w.want {
			w.touched = true
		}
	}
	return w.fakeTmux.HasSession(name)
}

func TestWindowLaunchFact_SlingToLiveAgentLeavesFactByteIdentical(t *testing.T) {
	const agent = "test-agent"
	profile := map[string]string{"ANTHROPIC_MODEL": wdModel, "CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(wdDeclared)}
	prior := wdFact(wdProfile, wdModel, wdDeclared, wdOld, time.Now().Add(-time.Hour).Truncate(time.Second))

	setup := func(t *testing.T) string {
		t.Helper()
		installMemStore(t)
		root, _ := createTestFormulaFactory(t, "test-formula", agent)
		writeValidModels(t, root, &config.ModelsConfig{
			Models: map[string]map[string]string{wdProfile: profile},
			Agents: map[string]string{agent: wdProfile},
		})
		wdStubVersion(t, wdNew)
		resetWindowPass()
		resetHarnessProbeFailures()
		t.Cleanup(resetWindowPass)
		t.Cleanup(resetHarnessProbeFailures)
		(&mailRecorder{}).install(t)
		t.Chdir(root)
		return root
	}
	requirePersistedForThisLaunch := func(t *testing.T, root string) {
		t.Helper()
		data, err := os.ReadFile(wdFactPath(root, agent))
		var fact windowLaunchFact
		if err == nil {
			err = json.Unmarshal(data, &fact)
		}
		if err != nil || fact.HarnessVersion != wdNew || !fact.LaunchedAt.After(prior.LaunchedAt) {
			t.Errorf("control: a launch of a stopped agent must persist its own fact (version %s); got %+v (err %v)", wdNew, fact, err)
		}
	}

	t.Run("sling formula to a live agent", func(t *testing.T) {
		root := setup(t)
		fake, _ := setupHermeticSessions(t)
		sess := session.SessionName(agent)
		fake.present[sess] = true
		fake.claudeRunning[sess] = true
		wdPlantFact(t, root, agent, prior)
		before, _ := os.ReadFile(wdFactPath(root, agent))
		watch := &wdFactWatchTmux{fakeTmux: fake, session: sess, path: wdFactPath(root, agent), want: string(before)}
		restore := session.InstallHermeticForTest(strings.TrimSuffix(sess, agent), func() session.TmuxForTest { return watch })
		t.Cleanup(restore)

		for name, v := range map[*string]string{&slingFormulaName: "test-formula", &slingAgent: agent} {
			orig := *name
			*name = v
			t.Cleanup(func() { *name = orig })
		}
		cmd, buf := launchCmd(t)
		if err := runFormulaInstantiation(cmd, root, config.AgentDir(root, agent), nil); err != nil {
			t.Fatalf("sling: %v\n%s", err, buf)
		}
		if !strings.Contains(buf.String(), "already running") {
			t.Errorf("the sling must reach Start and be refused as already running; output:\n%s", buf)
		}
		if after, _ := os.ReadFile(wdFactPath(root, agent)); string(after) != string(before) {
			t.Errorf("a sling to a live agent changed its launch fact:\n before %s\n after  %s", before, after)
		}
		if watch.probes == 0 || watch.touched {
			t.Errorf("a sling to a live agent must touch no fact, not even for the duration of Start (probes %d, fact replaced while probing %v)", watch.probes, watch.touched)
		}

		fake.present[sess], fake.claudeRunning[sess] = false, false
		cmd, buf = launchCmd(t)
		if err := launchAgentSession(cmd, root, agent, root, "wt-test00", "", launchGates{}); err != nil {
			t.Fatalf("control launch: %v\n%s", err, buf)
		}
		requirePersistedForThisLaunch(t, root)
	})

	t.Run("up whose liveness probe errors", func(t *testing.T) {
		root := setup(t)
		base, _ := setupHermeticSessions(t)
		sess := session.SessionName(agent)
		base.present[sess] = true
		base.claudeRunning[sess] = true
		flaky := &wdFlakyTmux{fakeTmux: base, session: sess}
		restore := session.InstallHermeticForTest(strings.TrimSuffix(sess, agent), func() session.TmuxForTest { return flaky })
		t.Cleanup(restore)
		origCmdTmux := newCmdTmux
		newCmdTmux = func() cmdTmux { return flaky }
		t.Cleanup(func() { newCmdTmux = origCmdTmux })
		wdPlantFact(t, root, agent, prior)
		before, _ := os.ReadFile(wdFactPath(root, agent))

		cmd, buf := launchCmd(t)
		_ = captureStderr(t, func() { _ = runUp(cmd, []string{agent}) })
		if flaky.calls < 2 {
			t.Fatalf("the launch never probed the session past the failed guard (HasSession calls %d); output:\n%s", flaky.calls, buf)
		}
		if !strings.Contains(buf.String(), "already running") {
			t.Errorf("Start must refuse the running session; output:\n%s", buf)
		}
		if after, _ := os.ReadFile(wdFactPath(root, agent)); string(after) != string(before) {
			t.Errorf("af up against a running session left another fact:\n before %s\n after  %s", before, after)
		}

		base.present[sess], base.claudeRunning[sess] = false, false
		flaky.calls = 1
		cmd, buf = launchCmd(t)
		_ = captureStderr(t, func() { _ = runUp(cmd, []string{agent}) })
		requirePersistedForThisLaunch(t, root)
	})
}

// t10WatchClaim records, at every send that reaches the mail seam, whether the profile's record
// claim was held at that moment.
func t10WatchClaim(t *testing.T, root, profile string) *[]bool {
	t.Helper()
	claim := strings.TrimSuffix(wdRecordPath(root, profile), ".json") + ".claim"
	held := &[]bool{}
	inner := escalationMailFn
	escalationMailFn = func(r, to, subject, body string) error {
		_, err := os.Lstat(claim)
		*held = append(*held, !os.IsNotExist(err))
		return inner(r, to, subject, body)
	}
	t.Cleanup(func() { escalationMailFn = inner })
	return held
}

func TestWindowDetector_MailIsSentOutsideTheClaim(t *testing.T) {
	for _, tc := range []struct {
		name  string
		plant func(t *testing.T, f *wdFixture, launched time.Time)
	}{
		{"mismatch", func(t *testing.T, f *wdFixture, launched time.Time) {
			wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
			wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
		}},
		{"unobserved", func(t *testing.T, f *wdFixture, launched time.Time) {
			wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
		}},
		{"bound", func(t *testing.T, f *wdFixture, launched time.Time) {
			wdWriteJSON(t, config.StartupConfigPath(f.root), map[string]any{"step_context": map[string]any{"bound_tokens": 1000000}})
			wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, 200000, wdOld, launched))
			wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWDFixture(t, "alpha")
			f.tmux.present[session.SessionName("alpha")] = true
			tc.plant(t, f, time.Now().Add(-time.Hour).Truncate(time.Second))
			held := t10WatchClaim(t, f.root, wdProfile)

			f.pass(time.Now(), "alpha")

			f.requireMailCount(t, 1, tc.name+": one mail per recipient")
			if len(*held) == 0 {
				t.Fatalf("no send reached the mail seam; the claim check would be vacuous")
			}
			for i, h := range *held {
				if h {
					t.Errorf("send %d of %d ran while the record's claim was held: decide under the claim, send after it", i+1, len(*held))
				}
			}
		})
	}
}

func t10FailedWriteLines(warn string) int {
	n := 0
	for _, l := range strings.Split(warn, "\n") {
		if strings.Contains(l, `profile "`+wdProfile+`"`) && strings.Contains(l, "could not be written") {
			n++
		}
	}
	return n
}

func TestWindowDetector_FailedRecordWriteIsSaid(t *testing.T) {
	setup := func(t *testing.T, observed bool) *wdFixture {
		t.Helper()
		f := newWDFixture(t, "alpha")
		f.tmux.present[session.SessionName("alpha")] = true
		launched := time.Now().Add(-time.Hour).Truncate(time.Second)
		wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
		if observed {
			wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
		}
		// A non-empty directory at the record path: the entry reads absent and the claim is taken,
		// but the atomic rename onto it fails, for root too (a chmod would not).
		if err := os.MkdirAll(filepath.Join(wdRecordPath(f.root, wdProfile), "pin"), 0o755); err != nil {
			t.Fatal(err)
		}
		return f
	}
	requireStillUnwritable := func(t *testing.T, f *wdFixture) {
		t.Helper()
		if info, err := os.Lstat(wdRecordPath(f.root, wdProfile)); err != nil || !info.IsDir() {
			t.Fatalf("the record path is no longer the planted directory (err %v); the writes did not fail", err)
		}
	}

	for _, tc := range []struct {
		name     string
		observed bool
	}{{"mismatch", true}, {"unobserved", false}} {
		t.Run(tc.name, func(t *testing.T) {
			f := setup(t, tc.observed)
			var warn strings.Builder
			said := 0
			now := time.Now()
			for pass := 1; pass <= 3; pass++ {
				f.passIn(windowDetectInput{Site: windowAtTick, Now: now.Add(time.Duration(pass) * time.Minute), Live: wdLive("alpha"), Warn: &warn})
				n := t10FailedWriteLines(warn.String())
				if n <= said {
					t.Errorf("pass %d said nothing naming the failed write of profile %q; Warn so far: %q", pass, wdProfile, warn.String())
				}
				said = n
				f.requireMailCount(t, pass, fmt.Sprintf("after pass %d over an unwritable record the mail is still the only signal", pass))
			}
			requireStillUnwritable(t, f)
		})
	}

	t.Run("nil Warn discards", func(t *testing.T) {
		f := setup(t, true)
		f.passIn(windowDetectInput{Site: windowAtTick, Now: time.Now(), Live: wdLive("alpha")})
		f.requireMailCount(t, 1, "an unwritable record with no Warn writer still mails")
		requireStillUnwritable(t, f)
	})
}

// A second pass deciding while the first is between its send and its record is the interleaving
// two detectors on one identity produce; the claim cannot order them once the mail is sent outside it.
func TestWindowDetector_TwoPassesDecidingOnePairRecordItOnce(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   string
		plant func(t *testing.T, f *wdFixture, launched time.Time)
		pairs func(rec windowRecord) int
	}{
		{"mismatch", wdKey(wdOld, wdDeclared, wdModel), func(t *testing.T, f *wdFixture, launched time.Time) {
			wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
			wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
		}, func(rec windowRecord) int { return len(rec.NotifiedFor) }},
		{"bound", wdKey(wdOld, 200000, wdModel), func(t *testing.T, f *wdFixture, launched time.Time) {
			wdWriteJSON(t, config.StartupConfigPath(f.root), map[string]any{"step_context": map[string]any{"bound_tokens": 1000000}})
			wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, 200000, wdOld, launched))
			wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
		}, func(rec windowRecord) int { return len(rec.BoundNotifiedFor) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWDFixture(t, "alpha")
			tc.plant(t, f, time.Now().Add(-time.Hour).Truncate(time.Second))
			now := time.Now()
			inner := escalationMailFn
			second := false
			escalationMailFn = func(r, to, subject, body string) error {
				if !second {
					second = true
					detectWindows(f.root, windowDetectInput{Site: windowAtTick, Now: now})
				}
				return inner(r, to, subject, body)
			}
			t.Cleanup(func() { escalationMailFn = inner })

			f.passIn(windowDetectInput{Site: windowAtTick, Now: now})

			f.requireMailCount(t, 2, "both passes decided before either recorded, so both mail; D16 accepts the duplicate")
			rec := wdMustEntry(t, f.root, wdProfile, tc.key)
			if n := tc.pairs(rec); n != 1 {
				t.Errorf("%s: the pair is recorded %d times, want once (re-checked under the second claim): %+v", tc.name, n, rec)
			}
		})
	}
}

func TestWindowDetector_LivenessIndependentOfModelResolution(t *testing.T) {
	profile := map[string]string{"ANTHROPIC_MODEL": wdModel, "CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(wdDeclared)}
	for _, tc := range []struct{ name, defaultProfile string }{
		{"registry with no default selects nothing for alpha", ""},
		{"control: a default resolves alpha", wdProfile},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newWDFixture(t, "alpha")
			f.tmux.present[session.SessionName("alpha")] = true
			writeValidModels(t, f.root, &config.ModelsConfig{Default: tc.defaultProfile, Models: map[string]map[string]string{wdProfile: profile}})
			wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, time.Now().Add(-time.Hour).Truncate(time.Second)))

			f.passIn(windowDetectInput{Site: windowAtTick, Now: time.Now()})

			f.requireMailCount(t, 1, "a live alpha silent for an hour is mailed whatever its model resolution")
			for _, m := range f.mail.sent {
				if !strings.Contains(m.body, `agent "alpha"`) || !strings.Contains(m.body, "has reported no window") {
					t.Errorf("want alpha's unobserved mail, got %+v", m)
				}
			}
			if rec, ok := wdEntry(t, f.root, wdProfile, wdKey(wdOld, wdDeclared, wdModel)); !ok || !rec.UnobservedNotified {
				t.Errorf("the entry must carry unobserved_notified; exists=%v rec=%+v", ok, rec)
			}
			if a, ok := wdWindowAlarms(t, f.root, time.Now())["alpha"]; !ok || a.reason != "unobserved" {
				t.Errorf("want a WINDOW alarm with reason unobserved for alpha, got %+v (raised %v)", a, ok)
			}
		})
	}
}
