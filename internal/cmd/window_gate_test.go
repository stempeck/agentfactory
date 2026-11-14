package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/session"
)

// No test in this file calls t.Parallel: each swaps package-level seams (claudeCodeVersionFn,
// newCmdTmux, escalationMailFn, probeHarnessWindowFn) and the sling/up flag variables, and the gate's
// notice dedupe and failed-probe set are package state.

const (
	wgApplied    = int64(200000)
	wgRecordPath = ".agentfactory/statusline/windows/" + wdProfile + ".json"
	wgAgent      = "wg-specialist"
	wgFormula    = "wg-formula"
	wgTask       = "https://github.com/owner/repo/issues/9"
)

func wgReset(t *testing.T) {
	t.Helper()
	resetWindowPass()
	resetWindowGateNotices()
	resetHarnessProbeFailures()
	t.Cleanup(resetWindowPass)
	t.Cleanup(resetWindowGateNotices)
	t.Cleanup(resetHarnessProbeFailures)
}

func wgStubNoVersion(t *testing.T) {
	t.Helper()
	orig := claudeCodeVersionFn
	claudeCodeVersionFn = func() string { return "" }
	t.Cleanup(func() { claudeCodeVersionFn = orig })
}

func wgProfileExports() map[string]string {
	return map[string]string{"ANTHROPIC_MODEL": wdModel, "CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(wdDeclared)}
}

// wgWriteModels maps every named agent to the declaring claude- profile.
func wgWriteModels(t *testing.T, root string, agents ...string) {
	t.Helper()
	mapping := map[string]string{}
	for _, a := range agents {
		mapping[a] = wdProfile
	}
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{wdProfile: wgProfileExports()},
		Agents: mapping,
	})
}

// wgShortfall is a session-sourced entry whose smallest reported figure is applied, held by holder.
func wgShortfall(version string, declared, applied int64, model, holder string) windowRecord {
	now := time.Now()
	return windowRecord{
		ModelID:        model,
		HarnessVersion: version,
		Declared:       declared,
		Applied:        applied,
		Agents:         map[string]agentWindow{holder: {Applied: applied, LaunchedAt: now.Add(-2 * time.Hour), LastSeen: now.Add(-time.Hour)}},
		FirstSeen:      now.Add(-2 * time.Hour),
		LastSeen:       now.Add(-time.Hour),
		Sessions:       1,
		Source:         "session",
	}
}

// wgPlantShortfall plants the divergent entry for the declaring profile's identity on version and
// returns it as readWindowRecord would hand it to the gate.
func wgPlantShortfall(t *testing.T, root, version, holder string) windowRecord {
	t.Helper()
	rec := wgShortfall(version, wdDeclared, wgApplied, wdModel, holder)
	wdPlantRecord(t, root, wdProfile, map[string]windowRecord{wdKey(version, wdDeclared, wdModel): rec})
	rec.Profile = wdProfile
	return rec
}

func wgAcceptWarning(agent string) string {
	return fmt.Sprintf("warning: --accept-window: launching agent %q on profile %q despite a recorded window of %d (declared %d)", agent, wdProfile, wgApplied, wdDeclared)
}

func wgMarker(profile string) string {
	return fmt.Sprintf("%s profile=%q class=%s", slingRefusedMarker, profile, refusalWindow)
}

// wgFactJSON decodes the agent's launch fact as written, never through readWindowLaunchFact.
func wgFactJSON(t *testing.T, root, agent string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(wdFactPath(root, agent))
	if err != nil {
		t.Fatalf("no launch fact for %s: %v", agent, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("launch fact of %s is not JSON: %v\n%s", agent, err, data)
	}
	return m
}

func wgRequireAccepted(t *testing.T, root, agent string) map[string]any {
	t.Helper()
	fact := wgFactJSON(t, root, agent)
	if fact["accept_window"] != true {
		t.Errorf("the launch fact of %s has accept_window=%v, want true: %v", agent, fact["accept_window"], fact)
	}
	return fact
}

// wgCmd separates stdout from stderr, because the refusal marker is a stdout contract.
func wgCmd(t *testing.T) (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	c := &cobra.Command{}
	c.SetContext(t.Context())
	var out, errOut bytes.Buffer
	c.SetOut(&out)
	c.SetErr(&errOut)
	return c, &out, &errOut
}

// newWGK14 is the k14 launch fixture with the manager mapped to the declaring profile and the
// harness reporting version wdNew.
func newWGK14(t *testing.T, agents ...string) (*k14Fixture, *fakeTmux) {
	t.Helper()
	fx := newK14Fixture(t)
	fake, _ := setupHermeticSessions(t)
	if len(agents) == 0 {
		agents = []string{"manager"}
	}
	wgWriteModels(t, fx.root, agents...)
	wdStubVersion(t, wdNew)
	wgReset(t)
	(&mailRecorder{}).install(t)
	return fx, fake
}

type wgDispatchFixture struct {
	root, agentDir, callerWd string
	fake                     *fakeTmux
}

// newWGDispatch is a specialist the real dispatchToSpecialist launches through the real
// launchAgentSession against a fake tmux, as the dispatcher's `af sling --agent` does.
func newWGDispatch(t *testing.T) *wgDispatchFixture {
	t.Helper()
	toml := "formula = \"" + wgFormula + "\"\ntype = \"workflow\"\nversion = 1\n\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
	root, agentDir := createTestFormulaFactoryWithTOML(t, wgFormula, wgAgent, toml)
	fake, _ := setupHermeticSessions(t)
	writeSpecialistAgentsJSON(t, root, wgAgent, wgFormula)
	wgWriteModels(t, root, wgAgent)
	wdStubVersion(t, wdNew)
	wgReset(t)
	(&mailRecorder{}).install(t)
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "1")
	fake.currentSession = session.DispatchSessionName()
	setSlingFlag(t, &slingCaller, "manager")
	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	if err := os.MkdirAll(callerWd, 0o755); err != nil {
		t.Fatal(err)
	}
	return &wgDispatchFixture{root: root, agentDir: agentDir, callerWd: callerWd, fake: fake}
}

func (d *wgDispatchFixture) dispatch(t *testing.T) (stdout, stderr string, err error) {
	t.Helper()
	c, out, errOut := wgCmd(t)
	err = dispatchToSpecialist(c, d.root, d.callerWd, wgAgent, wgTask)
	return out.String(), errOut.String(), err
}

func wgWorktrees(root string) []string {
	entries, _ := os.ReadDir(filepath.Join(root, ".agentfactory", "worktrees"))
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func TestWindowGate_RefusesWatchedLaunch(t *testing.T) {
	t.Run("matching record refuses af up", func(t *testing.T) {
		fx, fake := newWGK14(t)
		rec := wgPlantShortfall(t, fx.root, wdNew, "scout")
		u := fx.up(t, fake)
		if u.err == nil || !strings.Contains(u.err.Error(), "some agents failed to start") {
			t.Fatalf("af up of an agent on a recorded shortfall = %v, want \"some agents failed to start\"\n%s", u.err, u.output)
		}
		if u.line != "" {
			t.Errorf("a refused af up still launched: %s", u.line)
		}
		if !strings.Contains(u.output, `manager: agent "manager": profile `) {
			t.Errorf("af up's per-agent line must carry the refusal text from `agent \"manager\": profile`:\n%s", u.output)
		}
		if want := formatWindowText(fx.root, "manager", rec, rec.Applied).Refusal; want == "" || !strings.Contains(u.output, "manager: "+want+"\n") {
			t.Errorf("af up's output lacks the full refusal block %q:\n%s", want, u.output)
		}
	})

	t.Run("matching record refuses af sling agent", func(t *testing.T) {
		fx, fake := newWGK14(t)
		rec := wgPlantShortfall(t, fx.root, wdNew, "scout")
		s := fx.sling(t, fake, "")
		var wr *windowRefusal
		if !errors.As(s.err, &wr) {
			t.Fatalf("af sling of an agent on a recorded shortfall = %v, want a *windowRefusal\n%s", s.err, s.output)
		}
		if s.line != "" {
			t.Errorf("a refused sling still launched: %s", s.line)
		}
		if wr.Profile != wdProfile {
			t.Errorf("windowRefusal.Profile = %q, want %q", wr.Profile, wdProfile)
		}
		if want := formatWindowText(fx.root, "manager", rec, rec.Applied).Refusal; wr.Text != want || wr.Error() != want {
			t.Errorf("first refusal of the identity:\n Text  %q\n Error %q\n want  %q", wr.Text, wr.Error(), want)
		}
	})

	t.Run("other harness version never refuses", func(t *testing.T) {
		fx, fake := newWGK14(t)
		wgPlantShortfall(t, fx.root, wdOld, "scout")
		u := fx.up(t, fake)
		if u.err != nil || u.line == "" {
			t.Fatalf("an entry of another harness version refused the launch: err=%v\n%s", u.err, u.output)
		}
		if strings.Contains(u.output, "--accept-window") {
			t.Errorf("an entry of another harness version printed window text:\n%s", u.output)
		}
	})

	t.Run("malformed record never refuses", func(t *testing.T) {
		fx, fake := newWGK14(t)
		wfWrite(t, wdRecordPath(fx.root, wdProfile), []byte("{not json"))
		u := fx.up(t, fake)
		if u.err != nil || u.line == "" {
			t.Fatalf("a malformed record refused the launch: err=%v\n%s", u.err, u.output)
		}
		if strings.Contains(u.output, "--accept-window") {
			t.Errorf("a malformed record printed window text:\n%s", u.output)
		}
	})

	t.Run("empty claude version never refuses", func(t *testing.T) {
		fx, fake := newWGK14(t)
		wgStubNoVersion(t)
		wgPlantShortfall(t, fx.root, wdNew, "scout")
		u := fx.up(t, fake)
		if u.err != nil || u.line == "" {
			t.Fatalf("a launch with no harness version was refused: err=%v\n%s", u.err, u.output)
		}
		if strings.Contains(u.output, "--accept-window") {
			t.Errorf("a launch with no harness version printed window text:\n%s", u.output)
		}
	})

	t.Run("foreign profile launch line unchanged", func(t *testing.T) {
		const foreign, foreignModel = "sol", "gpt-5.6-sol"
		const foreignDeclared = int64(1050000)
		launch := func(t *testing.T, plant bool) string {
			t.Helper()
			fx := newK14Fixture(t)
			fake, _ := setupHermeticSessions(t)
			writeValidModels(t, fx.root, &config.ModelsConfig{
				Models: map[string]map[string]string{foreign: {"ANTHROPIC_MODEL": foreignModel, "CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(foreignDeclared)}},
				Agents: map[string]string{"manager": foreign},
			})
			wdStubVersion(t, wdNew)
			wgReset(t)
			if plant {
				rec := wgShortfall(wdNew, foreignDeclared, wgApplied, foreignModel, "scout")
				rec.Class = "foreign"
				wdPlantRecord(t, fx.root, foreign, map[string]windowRecord{wdKey(wdNew, foreignDeclared, foreignModel): rec})
			}
			u := fx.up(t, fake)
			if u.err != nil || u.line == "" {
				t.Fatalf("foreign profile launch (record planted: %v): err=%v\n%s", plant, u.err, u.output)
			}
			return strings.ReplaceAll(u.line, fx.root, "<ROOT>")
		}
		var without, with string
		t.Run("without record", func(t *testing.T) { without = launch(t, false) })
		t.Run("with record", func(t *testing.T) { with = launch(t, true) })
		if without == "" || with == "" {
			t.Fatal("a leg captured no launch line")
		}
		if with != without {
			t.Errorf("a foreign profile's launch line moved with a recorded shortfall:\n with:    %s\n without: %s", with, without)
		}
	})
}

func TestWindowGate_RepeatRefusalIsOneSelfContainedLine(t *testing.T) {
	fx, fake := newWGK14(t, "manager", "supervisor")
	rec := wgPlantShortfall(t, fx.root, wdNew, "scout")
	t.Setenv("AF_WORKTREE", fx.wtPath)
	t.Setenv("AF_WORKTREE_ID", fx.wtID)
	t.Setenv("AF_ROOT", fx.root)
	t.Chdir(fx.root)
	start := len(fake.ops)
	cmd, buf := launchCmd(t)
	var err error
	stderr := captureStderr(t, func() { err = runUp(cmd, []string{"manager", "supervisor"}) })
	out := buf.String() + stderr
	if err == nil || !strings.Contains(err.Error(), "some agents failed to start") {
		t.Fatalf("af up of two agents on one divergent identity = %v, want \"some agents failed to start\"\n%s", err, out)
	}
	for _, op := range fake.ops[start:] {
		for _, a := range []string{"manager", "supervisor"} {
			if strings.HasPrefix(op, "SendKeysDelayed "+session.SessionName(a)+" ") {
				t.Errorf("a refused agent was launched: %s", op)
			}
		}
	}

	first := formatWindowText(fx.root, "manager", rec, rec.Applied)
	if lines := strings.Split(first.Refusal, "\n"); len(lines) != 7 {
		t.Fatalf("fixture: the refusal block has %d lines, want 7: %q", len(lines), first.Refusal)
	}
	if !strings.Contains(out, "manager: "+first.Refusal+"\n") {
		t.Errorf("the first agent's error is not the seven-line block %q:\n%s", first.Refusal, out)
	}

	second := formatWindowText(fx.root, "supervisor", rec, rec.Applied).Summary
	var got []string
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "supervisor: ") {
			got = append(got, line)
		}
	}
	if len(got) != 1 || got[0] != "supervisor: "+second {
		t.Fatalf("the second agent's error lines = %q, want exactly [%q]\n%s", got, "supervisor: "+second, out)
	}
	for _, needle := range []string{fmt.Sprint(wdDeclared), fmt.Sprint(wgApplied), "--accept-window", wgRecordPath} {
		if !strings.Contains(got[0], needle) {
			t.Errorf("the repeat line does not stand alone: it lacks %q: %s", needle, got[0])
		}
	}
	if n := strings.Count(out, "Do one of:"); n != 1 {
		t.Errorf("the refusal block appears %d times, want once (agent 2 must get the one-line summary):\n%s", n, out)
	}
}

func TestWindowGate_AcceptWindow(t *testing.T) {
	t.Run("af sling agent", func(t *testing.T) {
		d := newWGDispatch(t)
		wgPlantShortfall(t, d.root, wdNew, "scout")
		setSlingFlag(t, &slingAcceptWindow, true)
		stdout, stderr, err := d.dispatch(t)
		if err != nil {
			t.Fatalf("af sling --agent --accept-window on a recorded shortfall = %v, want a launch\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, "Launched "+session.SessionName(wgAgent)) {
			t.Errorf("the accepted sling did not launch:\n%s", stdout)
		}
		if n := strings.Count(stderr, wgAcceptWarning(wgAgent)+"\n"); n != 1 {
			t.Errorf("the --accept-window warning appears %d times, want once:\n%s", n, stderr)
		}
		wgRequireAccepted(t, d.root, wgAgent)
	})

	t.Run("af sling formula", func(t *testing.T) {
		const agent = "test-agent"
		installMemStore(t)
		root, _ := createTestFormulaFactory(t, "test-formula", agent)
		setupHermeticSessions(t)
		wgWriteModels(t, root, agent)
		wdStubVersion(t, wdNew)
		wgReset(t)
		(&mailRecorder{}).install(t)
		t.Chdir(root)
		wgPlantShortfall(t, root, wdNew, "scout")
		setSlingFlag(t, &slingFormulaName, "test-formula")
		setSlingFlag(t, &slingAgent, agent)
		setSlingFlag(t, &slingAcceptWindow, true)
		c, out, errOut := wgCmd(t)
		if err := runFormulaInstantiation(c, root, config.AgentDir(root, agent), nil); err != nil {
			t.Fatalf("af sling --formula --accept-window: %v\n%s%s", err, out, errOut)
		}
		if strings.Contains(errOut.String(), "session launch failed") || !strings.Contains(out.String(), "Launched "+session.SessionName(agent)) {
			t.Fatalf("af sling --formula --accept-window did not launch:\nstdout:\n%s\nstderr:\n%s", out, errOut)
		}
		if n := strings.Count(errOut.String(), wgAcceptWarning(agent)+"\n"); n != 1 {
			t.Errorf("the --accept-window warning appears %d times, want once:\n%s", n, errOut)
		}
		wgRequireAccepted(t, root, agent)
	})

	t.Run("af up", func(t *testing.T) {
		fx, fake := newWGK14(t)
		wgPlantShortfall(t, fx.root, wdNew, "scout")
		setSlingFlag(t, &upAcceptWindow, true)
		u := fx.up(t, fake)
		if u.err != nil || u.line == "" {
			t.Fatalf("af up --accept-window on a recorded shortfall: err=%v\n%s", u.err, u.output)
		}
		if n := strings.Count(u.output, wgAcceptWarning("manager")+"\n"); n != 1 {
			t.Errorf("the --accept-window warning appears %d times, want once:\n%s", n, u.output)
		}
		accepted := wgRequireAccepted(t, fx.root, "manager")

		r := fx.respawn(t, false)
		if r.err != nil || r.line == "" {
			t.Fatalf("respawn of the accepted agent: err=%v\n%s", r.err, r.output)
		}
		after := wgRequireAccepted(t, fx.root, "manager")
		if after["launched_at"] == accepted["launched_at"] {
			t.Errorf("the respawn did not rewrite the launch fact (launched_at %v), so it proves nothing about keeping accept_window", after["launched_at"])
		}
	})

	t.Run("flag without a shortfall records no acceptance", func(t *testing.T) {
		fx, fake := newWGK14(t)
		agree := wgShortfall(wdNew, wdDeclared, wdDeclared, wdModel, "scout")
		wdPlantRecord(t, fx.root, wdProfile, map[string]windowRecord{wdKey(wdNew, wdDeclared, wdModel): agree})
		setSlingFlag(t, &upAcceptWindow, true)
		u := fx.up(t, fake)
		if u.err != nil || u.line == "" {
			t.Fatalf("af up --accept-window with an agreeing entry: err=%v\n%s", u.err, u.output)
		}
		if strings.Contains(u.output, "--accept-window") {
			t.Errorf("a launch nothing was short for printed the --accept-window warning:\n%s", u.output)
		}
		fact := wgFactJSON(t, fx.root, "manager")
		if _, has := fact["accept_window"]; has {
			t.Errorf("a launch nothing was short for recorded accept_window: %v", fact)
		}
	})
}

// wgStoppableTmux makes a KillSession end the session, so a reset sling can relaunch the agent it stopped.
type wgStoppableTmux struct{ *fakeTmux }

func (w wgStoppableTmux) KillSession(name string) error {
	w.present[name], w.claudeRunning[name] = false, false
	return w.fakeTmux.KillSession(name)
}

func TestWindowGate_PrecheckRefusesBeforeReset(t *testing.T) {
	d := newWGDispatch(t)
	wgPlantShortfall(t, d.root, wdNew, "scout")
	sess := session.SessionName(wgAgent)
	d.fake.present[sess] = true
	d.fake.claudeRunning[sess] = true
	stoppable := wgStoppableTmux{d.fake}
	t.Cleanup(session.InstallHermeticForTest(strings.TrimSuffix(sess, wgAgent), func() session.TmuxForTest { return stoppable }))
	origCmdTmux := newCmdTmux
	newCmdTmux = func() cmdTmux { return stoppable }
	t.Cleanup(func() { newCmdTmux = origCmdTmux })
	setSlingFlag(t, &slingReset, true)
	runtimeDir := filepath.Join(d.agentDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "session_id"), []byte("live-session\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("wg-instance\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	before := wdTreeBytes(t, runtimeDir)
	opsBefore := len(d.fake.ops)

	stdout, stderr, err := d.dispatch(t)
	assertNotTeardownRefused(t, err)
	var wr *windowRefusal
	if !errors.As(err, &wr) {
		t.Fatalf("af sling --agent --reset on a recorded shortfall = %v, want the window refusal\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stdout, wgMarker(wdProfile)+"\n") {
		t.Errorf("stdout lacks the %q line:\n%s", wgMarker(wdProfile), stdout)
	}
	for _, op := range d.fake.ops[opsBefore:] {
		if strings.HasPrefix(op, "KillSession ") {
			t.Errorf("the refused dispatch stopped the live agent: %s", op)
		}
	}
	if wts := wgWorktrees(d.root); len(wts) != 0 {
		t.Errorf("the refused dispatch created worktree(s) %v", wts)
	}
	if after := wdTreeBytes(t, runtimeDir); !reflect.DeepEqual(after, before) {
		t.Errorf("the refused dispatch changed the agent's .runtime:\n before %v\n after  %v", before, after)
	}
	if strings.Contains(stdout, "Dispatched to") {
		t.Errorf("the refused dispatch printed a Dispatched to line:\n%s", stdout)
	}

	setSlingFlag(t, &slingAcceptWindow, true)
	stdout, stderr, err = d.dispatch(t)
	if err != nil {
		t.Fatalf("with --accept-window the same sling = %v, want a launch\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, slingRefusedMarker) {
		t.Errorf("an --accept-window sling printed the refusal marker:\n%s", stdout)
	}
	killed := false
	for _, op := range d.fake.ops[opsBefore:] {
		killed = killed || op == "KillSession "+sess
	}
	if !killed {
		t.Errorf("fixture: the --reset leg never stopped the live agent, so the relaunch below proves nothing")
	}
	if !strings.Contains(stdout, "Dispatched to") || !strings.Contains(stdout, "Launched "+sess) {
		t.Errorf("the --accept-window sling did not dispatch and launch:\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	if n := strings.Count(stderr, wgAcceptWarning(wgAgent)+"\n"); n != 1 {
		t.Errorf("the --accept-window warning appears %d times, want once (the launch prints it, the pre-check is skipped):\n%s", n, stderr)
	}
}

func TestWindowGate_RespawnWarns(t *testing.T) {
	fx, _ := newWGK14(t)
	rec := wgPlantShortfall(t, fx.root, wdNew, "scout")
	text := formatWindowText(fx.root, "manager", rec, rec.Applied)

	first := fx.respawn(t, false)
	if first.err != nil || first.line == "" {
		t.Fatalf("a respawn on a recorded shortfall must launch: err=%v\n%s", first.err, first.output)
	}
	if text.Refusal == "" || !strings.Contains(first.output, "warning: "+text.Refusal+"\n") {
		t.Errorf("the first respawn's stderr lacks `warning: ` + the full refusal %q:\n%s", text.Refusal, first.output)
	}

	second := fx.respawn(t, false)
	if second.err != nil || second.line == "" {
		t.Fatalf("a second respawn on a recorded shortfall must launch: err=%v\n%s", second.err, second.output)
	}
	if text.Summary == "" || !strings.Contains(second.output, "warning: "+text.Summary+"\n") {
		t.Errorf("a later respawn's stderr lacks `warning: ` + the one-line summary %q:\n%s", text.Summary, second.output)
	}
	if strings.Contains(second.output, "Do one of:") {
		t.Errorf("a later respawn repeated the refusal block:\n%s", second.output)
	}
}

func TestWindowDivergence_SyntheticClaudeID(t *testing.T) {
	const profile, model = "zeta", "claude-zeta-9-9"
	fx := newK14Fixture(t)
	fake, _ := setupHermeticSessions(t)
	writeValidModels(t, fx.root, &config.ModelsConfig{
		Models: map[string]map[string]string{profile: {"ANTHROPIC_MODEL": model, "CLAUDE_CODE_MAX_CONTEXT_TOKENS": fmt.Sprint(wdDeclared)}},
		Agents: map[string]string{"manager": profile},
	})
	armTokenomics(t, fx.root, 10, 1)
	wdStubVersion(t, wdNew)
	wgReset(t)
	mail := (&mailRecorder{}).install(t)
	fake.present[session.SessionName("manager")] = true
	fake.present[session.SessionName("supervisor")] = true

	launched := time.Now().Add(-time.Hour).Truncate(time.Second)
	wdPlantFact(t, fx.root, "manager", wdFact(profile, model, wdDeclared, wdNew, launched))
	wdPlantSession(t, fx.root, "manager", "sess-zeta", launched.Add(2*time.Second), wgApplied)
	now := time.Now()
	resetWindowPass()
	detectWindows(fx.root, windowDetectInput{Site: windowAtTick, Now: now, Live: map[string]liveProfile{}})

	rec, ok := wdEntry(t, fx.root, profile, wdKey(wdNew, wdDeclared, model))
	if !ok || rec.Applied != wgApplied || rec.Declared != wdDeclared {
		t.Fatalf("the detector did not record the divergence of %s: entry %+v (present %v)", model, rec, ok)
	}
	mailed := false
	for _, m := range mail.sent {
		if strings.HasPrefix(m.subject, "WINDOW_MISMATCH: manager on profile "+profile) && strings.Contains(m.body, fmt.Sprint(wgApplied)) {
			mailed = true
		}
	}
	if !mailed {
		t.Errorf("the detector sent no WINDOW_MISMATCH mail naming %d; sent: %+v", wgApplied, mail.sent)
	}

	fake.present[session.SessionName("manager")] = false
	resetWindowPass()
	s := fx.sling(t, fake, "")
	var wr *windowRefusal
	if !errors.As(s.err, &wr) {
		t.Fatalf("the next watched launch of %s = %v, want the window refusal\n%s", model, s.err, s.output)
	}
	if !strings.Contains(wr.Text, model) || !strings.Contains(wr.Text, fmt.Sprint(wgApplied)) {
		t.Errorf("the refusal does not name %s and %d: %q", model, wgApplied, wr.Text)
	}

	workDir := config.AgentDir(fx.root, "manager")
	plantSessionSnapshotWithTotal(t, fx.root, "manager", "sess-zeta", 40, wgApplied, 1000, now.Add(-10*time.Second), now)
	startupCfg, err := config.LoadStartupConfig(fx.root)
	if err != nil {
		t.Fatalf("LoadStartupConfig: %v", err)
	}
	r := stepContextReading(fx.root, workDir, "manager", startupCfg.Recovery, now)
	if !r.IsHealthy() {
		t.Fatalf("fixture: the session reading is not healthy, so stepAdmission would not see the host's figure")
	}
	w := stepAdmission(fx.root, workDir, "manager", "offpath", "s-1", r, startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct).window
	if w.Tokens != wgApplied || w.Source != config.WindowSourceHost {
		t.Errorf("stepAdmission's window = {%d, %q}, want {%d, %q}: the advisory threshold must be computed on the window the host applies", w.Tokens, w.Source, wgApplied, config.WindowSourceHost)
	}
}

func TestSling_AcceptWindowFlag_Registered(t *testing.T) {
	if slingCmd.Flags().Lookup("accept-window") == nil {
		t.Error("af sling must register an --accept-window flag")
	}
}

func TestUp_AcceptWindowFlag_Registered(t *testing.T) {
	if upCmd.Flags().Lookup("accept-window") == nil {
		t.Error("af up must register an --accept-window flag")
	}
}

func TestSlingAgent_WindowRefusalEmitsMarker(t *testing.T) {
	t.Run("the real dispatch path", func(t *testing.T) {
		d := newWGDispatch(t)
		wgPlantShortfall(t, d.root, wdNew, "scout")
		stdout, stderr, err := d.dispatch(t)
		var wr *windowRefusal
		if !errors.As(err, &wr) {
			t.Fatalf("af sling --agent on a recorded shortfall = %v, want the window refusal\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
		}
		if !strings.Contains(stdout, wgMarker(wdProfile)+"\n") {
			t.Errorf("stdout lacks %q:\n%s", wgMarker(wdProfile), stdout)
		}
		if got := parseSlingRefusal(stdout); got != refusalWindow {
			t.Errorf("parseSlingRefusal(stdout) = %q, want %q\n%s", got, refusalWindow, stdout)
		}
	})

	for _, name := range []string{"a profile", "a class=x"} {
		t.Run(name, func(t *testing.T) {
			var out bytes.Buffer
			reportWindowRefusal(&out, &windowRefusal{Profile: name, Text: "refused"})
			if want := wgMarker(name) + "\n"; out.String() != want {
				t.Errorf("reportWindowRefusal printed %q, want %q", out.String(), want)
			}
			if got := parseSlingRefusal(out.String()); got != refusalWindow {
				t.Errorf("profile %q: parseSlingRefusal = %q, want %q", name, got, refusalWindow)
			}
		})
	}
}

type wgProbeStub struct {
	calls     int
	deadlines []time.Duration
}

// wgStubProbe answers every probe with p and err, and counts the calls.
func wgStubProbe(t *testing.T, p harnessWindowProbe, err error) *wgProbeStub {
	t.Helper()
	s := &wgProbeStub{}
	orig := probeHarnessWindowFn
	probeHarnessWindowFn = func(ctx context.Context, profile map[string]string) (harnessWindowProbe, error) {
		s.calls++
		if dl, ok := ctx.Deadline(); ok {
			s.deadlines = append(s.deadlines, time.Until(dl))
		}
		return p, err
	}
	t.Cleanup(func() { probeHarnessWindowFn = orig })
	return s
}

func wgProbeNotCompleted(err error) string {
	return fmt.Sprintf("warning: window probe for profile %q did not complete (%v); launching without a measurement", wdProfile, err)
}

// wgRawEntry is one identity's entry as stored, compacted, so a rewrite of the file in another
// layout still compares equal while any change to the entry does not.
func wgRawEntry(t *testing.T, root, key string) string {
	t.Helper()
	data, err := os.ReadFile(wdRecordPath(root, wdProfile))
	if err != nil {
		t.Fatalf("no record file: %v", err)
	}
	var f struct {
		Identities map[string]json.RawMessage `json:"identities"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("record is not JSON: %v\n%s", err, data)
	}
	raw, ok := f.Identities[key]
	if !ok {
		t.Fatalf("no entry %q in the record:\n%s", key, data)
	}
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

func wgRequireLaunched(t *testing.T, u k14Launch, why string) {
	t.Helper()
	if u.err != nil || u.line == "" {
		t.Fatalf("%s: the launch did not proceed: err=%v\n%s", why, u.err, u.output)
	}
}

func wgRequireRefused(t *testing.T, u k14Launch, why string) {
	t.Helper()
	if u.err == nil || !strings.Contains(u.err.Error(), "some agents failed to start") || u.line != "" {
		t.Fatalf("%s: af up = %v (line %q), want the window refusal\n%s", why, u.err, u.line, u.output)
	}
}

func wgRequireNoRecord(t *testing.T, root string) {
	t.Helper()
	if data, err := os.ReadFile(wdRecordPath(root, wdProfile)); err == nil {
		t.Errorf("a probe that measured nothing stored a record:\n%s", data)
	}
}

func TestWindowGate_ProbesUnobservedIdentity(t *testing.T) {
	newKey, oldKey := wdKey(wdNew, wdDeclared, wdModel), wdKey(wdOld, wdDeclared, wdModel)

	t.Run("never observed identity refused on probe 200000", func(t *testing.T) {
		fx, fake := newWGK14(t)
		s := wgStubProbe(t, harnessWindowProbe{ModelID: "claude-fable-5-2-as-reported", ContextWindow: wgApplied, HarnessVersion: wdNew}, nil)
		u := fx.up(t, fake)
		wgRequireRefused(t, u, "a never-observed identity whose probe measures 200000")
		if s.calls != 1 {
			t.Errorf("the probe ran %d times, want once", s.calls)
		}
		if len(s.deadlines) != 1 || s.deadlines[0] <= 0 || s.deadlines[0] > harnessProbeLaunchTimeout {
			t.Errorf("the probe ran with deadlines %v, want one within harnessProbeLaunchTimeout (%v)", s.deadlines, harnessProbeLaunchTimeout)
		}
		rec := wdMustEntry(t, fx.root, wdProfile, newKey)
		if rec.Applied != wgApplied || rec.Probed != wgApplied || rec.Source != "probe" || rec.ModelID != wdModel || rec.Sessions != 0 {
			t.Errorf("stored entry = %+v, want applied = probed = %d, source probe, the profile's model id %q, sessions 0", rec, wgApplied, wdModel)
		}
		rec.Profile = wdProfile
		if want := formatWindowText(fx.root, "manager", rec, rec.Applied).Refusal; want == "" || !strings.Contains(u.output, "manager: "+want+"\n") {
			t.Errorf("af up's output lacks the refusal block %q for the probe row:\n%s", want, u.output)
		}
	})

	t.Run("session entry of another harness version kept and probe row refused at first launch", func(t *testing.T) {
		fx, fake := newWGK14(t)
		wdStubVersion(t, wdOld)
		resetWindowPass()
		wdPlantRecord(t, fx.root, wdProfile, map[string]windowRecord{newKey: wgShortfall(wdNew, wdDeclared, wdDeclared, wdModel, "scout")})
		before := wgRawEntry(t, fx.root, newKey)
		s := wgStubProbe(t, harnessWindowProbe{ModelID: wdModel, ContextWindow: wgApplied, HarnessVersion: wdOld}, nil)
		u := fx.up(t, fake)
		wgRequireRefused(t, u, "the first watched launch on "+wdOld+" whose probe measures 200000")
		if s.calls != 1 {
			t.Errorf("the probe ran %d times, want once", s.calls)
		}
		f, ok := wdReadRecord(t, fx.root, wdProfile)
		if !ok || len(f.Identities) != 2 {
			t.Fatalf("the record holds %d entries (present %v), want the kept %s session entry and the %s probe row: %+v", len(f.Identities), ok, wdNew, wdOld, f.Identities)
		}
		if probe := f.Identities[oldKey]; probe.Source != "probe" || probe.Applied != wgApplied {
			t.Errorf("the %s entry = %+v, want the probe row at %d", wdOld, probe, wgApplied)
		}
		if after := wgRawEntry(t, fx.root, newKey); after != before {
			t.Errorf("the %s session entry changed:\n before %s\n after  %s", wdNew, before, after)
		}
	})

	t.Run("same-identity session entry not replaced by probe", func(t *testing.T) {
		fx, fake := newWGK14(t)
		wdPlantRecord(t, fx.root, wdProfile, map[string]windowRecord{newKey: wgShortfall(wdNew, wdDeclared, wdDeclared, wdModel, "scout")})
		s := wgStubProbe(t, harnessWindowProbe{ModelID: wdModel, ContextWindow: wgApplied, HarnessVersion: wdNew}, nil)
		wgRequireLaunched(t, fx.up(t, fake), "an agreeing session entry of the launch's own identity")
		if s.calls != 0 {
			t.Errorf("the probe ran %d times for an identity that has a session entry, want never", s.calls)
		}
		if rec := wdMustEntry(t, fx.root, wdProfile, newKey); rec.Source != "session" || rec.Probed != 0 || rec.Applied != wdDeclared {
			t.Errorf("the session entry became %+v; it must be judged as it stands", rec)
		}
	})

	t.Run("empty probe version writes nothing", func(t *testing.T) {
		fx, fake := newWGK14(t)
		s := wgStubProbe(t, harnessWindowProbe{ModelID: wdModel, ContextWindow: wgApplied}, nil)
		u := fx.up(t, fake)
		wgRequireLaunched(t, u, "a probe that reports no harness version")
		if s.calls != 1 || !strings.Contains(u.output, fmt.Sprintf("warning: window probe for profile %q did not complete (", wdProfile)) {
			t.Errorf("want one probe (ran %d) and its did-not-complete warning:\n%s", s.calls, u.output)
		}
		wgRequireNoRecord(t, fx.root)
	})

	t.Run("probe on another version writes nothing and refuses nothing", func(t *testing.T) {
		fx, fake := newWGK14(t)
		wdStubVersion(t, wdOld)
		resetWindowPass()
		s := wgStubProbe(t, harnessWindowProbe{ModelID: wdModel, ContextWindow: wgApplied, HarnessVersion: wdNew}, nil)
		u := fx.up(t, fake)
		wgRequireLaunched(t, u, "a probe that ran "+wdNew+" while the launch keys on "+wdOld)
		if s.calls != 1 {
			t.Errorf("the probe ran %d times, want once", s.calls)
		}
		var line string
		for _, l := range strings.Split(u.output, "\n") {
			if strings.HasPrefix(l, fmt.Sprintf("warning: window probe for profile %q did not complete (", wdProfile)) {
				line = l
			}
		}
		if !strings.HasSuffix(line, "); launching without a measurement") || !strings.Contains(line, `"`+wdNew+`"`) || !strings.Contains(line, `"`+wdOld+`"`) {
			t.Errorf("want the did-not-complete warning naming both %q and %q, got %q\n%s", wdNew, wdOld, line, u.output)
		}
		wgRequireNoRecord(t, fx.root)
	})

	t.Run("probe timeout refuses nothing", func(t *testing.T) {
		fx, fake := newWGK14(t)
		wgStubProbe(t, harnessWindowProbe{}, context.DeadlineExceeded)
		u := fx.up(t, fake)
		wgRequireLaunched(t, u, "a probe that timed out")
		if !strings.Contains(u.output, wgProbeNotCompleted(context.DeadlineExceeded)+"\n") {
			t.Errorf("the output lacks %q:\n%s", wgProbeNotCompleted(context.DeadlineExceeded), u.output)
		}
		wgRequireNoRecord(t, fx.root)
	})

	t.Run("respawn never probes", func(t *testing.T) {
		fx, _ := newWGK14(t)
		s := wgStubProbe(t, harnessWindowProbe{ModelID: wdModel, ContextWindow: wgApplied, HarnessVersion: wdNew}, nil)
		r := fx.respawn(t, false)
		if r.err != nil || r.line == "" {
			t.Fatalf("respawn: err=%v\n%s", r.err, r.output)
		}
		if s.calls != 0 {
			t.Errorf("a respawn probed the harness %d times, want never", s.calls)
		}
		wgRequireNoRecord(t, fx.root)
	})

	t.Run("failed probe not retried in process", func(t *testing.T) {
		fx, fake := newWGK14(t)
		s := wgStubProbe(t, harnessWindowProbe{}, context.DeadlineExceeded)
		for i := 1; i <= 2; i++ {
			fake.present[session.SessionName("manager")] = false
			l := fx.sling(t, fake, "")
			if l.err != nil || l.line == "" {
				t.Fatalf("watched launch %d: err=%v\n%s", i, l.err, l.output)
			}
		}
		if s.calls != 1 {
			t.Errorf("two watched launches of one identity probed %d times, want once: a failed probe is not retried in the process", s.calls)
		}
	})
}

func TestWindowGate_ProbeRefusalMailsOnce(t *testing.T) {
	key := wdKey(wdNew, wdDeclared, wdModel)
	setup := func(t *testing.T) (*k14Fixture, *fakeTmux, *mailRecorder, *wgProbeStub) {
		t.Helper()
		fx, fake := newWGK14(t)
		mail := (&mailRecorder{}).install(t)
		wdWriteJSON(t, config.StartupConfigPath(fx.root), map[string]any{"agents": []string{"manager", "supervisor"}})
		s := wgStubProbe(t, harnessWindowProbe{ModelID: wdModel, ContextWindow: wgApplied, HarnessVersion: wdNew}, nil)
		return fx, fake, mail, s
	}

	t.Run("three refusals mail once", func(t *testing.T) {
		fx, fake, mail, s := setup(t)
		count := func(to string) (n int) {
			for _, m := range mail.sent {
				if m.recipient == to {
					n++
				}
			}
			return n
		}
		for i := 1; i <= 3; i++ {
			wgRequireRefused(t, fx.up(t, fake), fmt.Sprintf("watched launch %d of a probe-measured shortfall", i))
			if i > 1 {
				continue
			}
			rec := wdMustEntry(t, fx.root, wdProfile, key)
			if !wdHasPair(rec.NotifiedFor, wdDeclared, wgApplied) {
				t.Errorf("after the first refusal notified_for = %+v, want it to hold (%d, %d)", rec.NotifiedFor, wdDeclared, wgApplied)
			}
			rec.Profile = wdProfile
			text := formatWindowText(fx.root, "manager", rec, rec.Applied)
			for _, m := range mail.sent {
				if text.Subject == "" || m.subject != text.Subject || m.body != text.Body {
					t.Errorf("the refusal mail to %s is %q / %q, want the detector's text %q / %q", m.recipient, m.subject, m.body, text.Subject, text.Body)
				}
			}
		}
		for _, to := range []string{"manager", escalationTarget} {
			if n := count(to); n != 1 {
				t.Errorf("three refusals of one identity sent %d mails to %s, want exactly 1; sent: %+v", n, to, mail.sent)
			}
		}
		if s.calls != 1 {
			t.Errorf("the probe ran %d times, want once: the first refusal's row stands for the identity", s.calls)
		}

		sent := len(mail.sent)
		if r := fx.respawn(t, false); r.err != nil || r.line == "" || !strings.Contains(r.output, "warning: ") {
			t.Fatalf("respawn on the probe row must launch with a warning: err=%v\n%s", r.err, r.output)
		}
		setSlingFlag(t, &upAcceptWindow, true)
		wgRequireLaunched(t, fx.up(t, fake), "--accept-window on the probe row")
		if len(mail.sent) != sent {
			t.Errorf("a respawn warning and an --accept-window launch sent %d more mail(s), want none: %+v", len(mail.sent)-sent, mail.sent[sent:])
		}
	})

	// In the subtest above the pair is already in notified_for, so only an identity never refused
	// shows that an --accept-window launch sends nothing of its own.
	t.Run("accept-window on a never-refused identity mails nothing", func(t *testing.T) {
		fx, fake, mail, _ := setup(t)
		setSlingFlag(t, &upAcceptWindow, true)
		wgRequireLaunched(t, fx.up(t, fake), "--accept-window on a never-observed identity whose probe measures 200000")
		if len(mail.sent) != 0 {
			t.Errorf("an --accept-window launch on a probe-measured shortfall sent %+v, want no mail", mail.sent)
		}
		if rec := wdMustEntry(t, fx.root, wdProfile, key); rec.Source != "probe" || len(rec.NotifiedFor) != 0 {
			t.Errorf("after the --accept-window launch the entry = %+v, want the probe row with nothing notified", rec)
		}
	})
}

const (
	t1Agent   = "test-agent"
	t1Formula = "test-formula"
)

// t1FormulaSlingFixture is `af sling --formula test-formula --agent test-agent` with the agent on the
// declaring profile. createTestFormulaFactory inherits the factory root as the worktree (Reattached).
func t1FormulaSlingFixture(t *testing.T) (string, *fakeTmux, *memstore.Store) {
	t.Helper()
	root, _ := createTestFormulaFactory(t, t1Formula, t1Agent)
	fake, store := setupHermeticSessions(t)
	wgWriteModels(t, root, t1Agent)
	wdStubVersion(t, wdNew)
	wgReset(t)
	(&mailRecorder{}).install(t)
	t.Setenv("AF_ROLE", "")
	t.Chdir(root)
	setSlingFlag(t, &slingFormulaName, t1Formula)
	setSlingFlag(t, &slingAgent, t1Agent)
	return root, fake, store
}

func t1Sling(t *testing.T, root string) (stdout, stderr string, err error) {
	t.Helper()
	c, out, errOut := wgCmd(t)
	err = runFormulaInstantiation(c, root, config.AgentDir(root, t1Agent), nil)
	return out.String(), errOut.String(), err
}

func t1RequireWindowRefusal(t *testing.T, stdout, stderr string, err error) {
	t.Helper()
	var wr *windowRefusal
	if !errors.As(err, &wr) {
		t.Errorf("af sling --formula on a recorded shortfall = %v, want an error carrying the *windowRefusal\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	} else if wr.Profile != wdProfile {
		t.Errorf("windowRefusal.Profile = %q, want %q", wr.Profile, wdProfile)
	}
	if !strings.Contains(stdout, wgMarker(wdProfile)+"\n") {
		t.Errorf("stdout lacks %q:\n%s", wgMarker(wdProfile), stdout)
	}
	if got := parseSlingRefusal(stdout); got != refusalWindow {
		t.Errorf("parseSlingRefusal(stdout) = %q, want %q\n%s", got, refusalWindow, stdout)
	}
	if strings.Contains(stderr, "warning: session launch failed:") {
		t.Errorf("the refusal is the command's error, so it must not also be printed as a launch warning:\n%s", stderr)
	}
}

func t1RequireNoLaunch(t *testing.T, fake *fakeTmux, from int, stdout string) {
	t.Helper()
	sess := session.SessionName(t1Agent)
	if strings.Contains(stdout, "Launched "+sess) {
		t.Errorf("a refused sling launched %s:\n%s", sess, stdout)
	}
	for _, op := range fake.ops[from:] {
		for _, verb := range []string{"KillSession ", "NewSession ", "NewSessionWithCommand ", "SendKeysDelayed "} {
			if strings.HasPrefix(op, verb+sess) {
				t.Errorf("the sling touched the agent's session: %s", op)
			}
		}
	}
}

func TestSlingFormula_WindowRefusalExitsNonZeroWithMarker(t *testing.T) {
	root, fake, store := t1FormulaSlingFixture(t)
	wgPlantShortfall(t, root, wdNew, "scout")

	stdout, stderr, err := t1Sling(t, root)

	t1RequireWindowRefusal(t, stdout, stderr, err)
	if want := "formula beads for " + t1Agent + " were created, but its session was not launched"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the error must say the beads exist and the session did not launch (%q); got %v", want, err)
	}
	t1RequireNoLaunch(t, fake, 0, stdout)
	if !strings.Contains(stdout, `Formula "`+t1Formula+`" instantiated`) {
		t.Errorf("fixture: the formula was never instantiated, so the refusal came too early to test the launch site:\n%s", stdout)
	}
	if beads, lerr := store.List(t.Context(), issuestore.Filter{Assignee: t1Agent}); lerr != nil || len(beads) == 0 {
		t.Errorf("the refusal took back the formula beads its error says were created (%d beads, %v)", len(beads), lerr)
	}
}

func TestSlingFormula_OtherLaunchFailureStaysNonFatal(t *testing.T) {
	root, _, _ := t1FormulaSlingFixture(t)
	setSlingFlag(t, &slingModel, "ghost-profile")

	stdout, stderr, err := t1Sling(t, root)

	if err != nil {
		t.Fatalf("a launch failure that is not a window refusal must stay a warning, the beads being created: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if !strings.Contains(stderr, "warning: session launch failed: ") || !strings.Contains(stderr, `unknown model profile "ghost-profile"`) {
		t.Errorf("stderr lacks the launch warning naming the unknown profile:\n%s", stderr)
	}
	if strings.Contains(stdout, slingRefusedMarker) {
		t.Errorf("a launch failure that is not a refusal printed the refusal marker:\n%s", stdout)
	}
}

func TestSlingFormula_PrecheckRefusesBeforeReset(t *testing.T) {
	root, fake, store := t1FormulaSlingFixture(t)
	wgPlantShortfall(t, root, wdNew, "scout")
	setSlingFlag(t, &slingReset, true)
	prior, err := store.Create(t.Context(), issuestore.CreateParams{Title: "previous formula step", Assignee: t1Agent, Type: issuestore.TypeTask})
	if err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Join(config.AgentDir(root, t1Agent), ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"session_id": "previous-session\n", "hooked_formula": "previous-instance\n"} {
		if err := os.WriteFile(filepath.Join(runtimeDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	before := wdTreeBytes(t, runtimeDir)

	stdout, stderr, err := t1Sling(t, root)

	t1RequireWindowRefusal(t, stdout, stderr, err)
	if err != nil && strings.Contains(err.Error(), "were created") {
		t.Errorf("the reset sling was refused before any bead was created, yet its error says beads were created: %v", err)
	}
	if strings.Contains(stdout, t1Agent+": closed ") {
		t.Errorf("the refused reset closed the agent's previous beads:\n%s", stdout)
	}
	if b, gerr := store.Get(t.Context(), prior.ID); gerr != nil || b.Status == issuestore.StatusClosed {
		t.Errorf("the agent's previous bead did not survive the refused reset (bead %+v, %v)", b, gerr)
	}
	if after := wdTreeBytes(t, runtimeDir); !reflect.DeepEqual(after, before) {
		t.Errorf("the refused reset changed the agent's .runtime:\n before %v\n after  %v", before, after)
	}
	if strings.Contains(stdout, "instantiated") {
		t.Errorf("the refused reset instantiated a new formula:\n%s", stdout)
	}
	t1RequireNoLaunch(t, fake, 0, stdout)
}

func TestSlingFormula_WindowRefusalOnLiveAgentStaysNonFatal(t *testing.T) {
	root, fake, _ := t1FormulaSlingFixture(t)
	wgPlantShortfall(t, root, wdNew, "scout")
	sess := session.SessionName(t1Agent)
	fake.present[sess], fake.claudeRunning[sess] = true, true
	opsBefore := len(fake.ops)

	stdout, stderr, err := t1Sling(t, root)

	if err != nil {
		t.Errorf("a sling to a running agent launches nothing and the session receives the new beads, so a window refusal must not fail it: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, slingRefusedMarker) {
		t.Errorf("a sling to a running agent printed the refusal marker:\n%s", stdout)
	}
	if !strings.Contains(stderr, "warning: session launch failed: ") || !strings.Contains(stderr, `agent "`+t1Agent+`": profile `) {
		t.Errorf("stderr lacks the non-fatal launch warning carrying the refusal:\n%s", stderr)
	}
	t1RequireNoLaunch(t, fake, opsBefore, stdout)
}

func TestSlingFormula_ResetOnLiveAgentIsNotRefusedBeforeTheReset(t *testing.T) {
	root, fake, _ := t1FormulaSlingFixture(t)
	wgPlantShortfall(t, root, wdNew, "scout")
	setSlingFlag(t, &slingReset, true)
	sess := session.SessionName(t1Agent)
	fake.present[sess], fake.claudeRunning[sess] = true, true
	opsBefore := len(fake.ops)

	stdout, stderr, err := t1Sling(t, root)

	if err != nil {
		t.Errorf("--reset never stops a running session, so a sling to a live agent launches nothing and must not be refused: %v\nstdout:\n%s\nstderr:\n%s", err, stdout, stderr)
	}
	if strings.Contains(stdout, slingRefusedMarker) {
		t.Errorf("a reset sling to a running agent printed the refusal marker:\n%s", stdout)
	}
	if !strings.Contains(stdout, "instantiated") {
		t.Errorf("a reset sling to a running agent must still instantiate the formula:\n%s", stdout)
	}
	t1RequireNoLaunch(t, fake, opsBefore, stdout)
}

func TestSlingFormula_WindowRefusalTakesBackItsCreatedWorktree(t *testing.T) {
	root, _, _ := t1FormulaSlingFixture(t)
	t18TrackedScaffoldRepo(t, root, t1Agent)
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	wgPlantShortfall(t, root, wdNew, "scout")

	stdout, stderr, err := t1Sling(t, root)

	if !strings.Contains(stdout, "Created worktree ") {
		t.Fatalf("fixture: the sling created no worktree, so there is nothing to take back\nstdout:\n%s\nstderr:\n%s", stdout, stderr)
	}
	t1RequireWindowRefusal(t, stdout, stderr, err)
	if want := "formula beads for " + t1Agent + " were created, but its session was not launched"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("the error must say the beads exist and the session did not launch (%q); got %v", want, err)
	}
	if strings.Contains(stderr, "warning: removing refused worktree") {
		t.Errorf("the refusal could not take back the worktree it created:\n%s", stderr)
	}
	t18RequireNoWorktree(t, root, t1Agent, "after the refused af sling --formula")
}
