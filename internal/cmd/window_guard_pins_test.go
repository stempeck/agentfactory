package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// No test in this file calls t.Parallel: each swaps package-level seams (claudeCodeVersionFn,
// escalationMailFn, newCmdTmux, probeHarnessWindowFn) and the detector's pass memo is package state.

// body2Pass makes each agent live both through the Live map and through the fake tmux, so a
// liveness check read from either source finds the agent running and cannot be what silences a
// finding.
func body2Pass(f *wdFixture, now time.Time, live ...string) windowReport {
	for _, a := range live {
		f.tmux.present[session.SessionName(a)] = true
	}
	return f.passIn(windowDetectInput{Site: windowAtTick, Now: now, Live: wdLive(live...)})
}

func body2FindingsFor(rep windowReport, agent string) []windowFinding {
	var out []windowFinding
	for _, fd := range rep.Findings {
		if fd.Agent == agent {
			out = append(out, fd)
		}
	}
	return out
}

func body2RequireNoFigureFor(t *testing.T, root, profile, agent string) {
	t.Helper()
	rf, _ := wdReadRecord(t, root, profile)
	for key, rec := range rf.Identities {
		if fig, ok := rec.Agents[agent]; ok {
			t.Errorf("entry %q holds a figure for %s: %+v", key, agent, fig)
		}
	}
}

func TestWindowGuardPins_HostAboveDeclarationIsAMismatch(t *testing.T) {
	const declared, host = int64(200000), int64(1000000)
	f := newWDFixture(t, "alpha")
	launched := time.Now().Add(-time.Hour).Truncate(time.Second)
	wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, declared, wdOld, launched))
	wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), host)

	rep := body2Pass(f, time.Now(), "alpha")

	got := body2FindingsFor(rep, "alpha")
	if len(got) != 1 || got[0].State != "mismatch" || got[0].Applied != host || got[0].Declared != declared {
		t.Errorf("findings for alpha = %+v, want one mismatch declaring %d and applying %d", got, declared, host)
	}
	f.requireMailCount(t, 1, "a host window above the declaration is announced once")
	for _, m := range f.mail.sent {
		if !strings.Contains(m.body, fmt.Sprintf("declares a %d-token window", declared)) || !strings.Contains(m.body, fmt.Sprintf("applied %d", host)) {
			t.Errorf("mail body must carry both figures, got %q", m.body)
		}
	}
	rec := wdMustEntry(t, f.root, wdProfile, wdKey(wdOld, declared, wdModel))
	if !wdHasPair(rec.NotifiedFor, declared, host) {
		t.Errorf("notified_for = %+v, want the pair (%d, %d)", rec.NotifiedFor, declared, host)
	}
	if windowShortfall(rec) {
		t.Errorf("a window above the declaration is not a shortfall, so no launch may be refused on it: %+v", rec)
	}
}

func TestWindowGuardPins_SameLaunchAgreementClearsTheAlarm(t *testing.T) {
	f := newWDFixture(t, "alpha")
	launched := time.Now().Add(-time.Hour).Truncate(time.Second)
	wdPlantFact(t, f.root, "alpha", wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched))
	wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 200000)
	t0 := time.Now()
	body2Pass(f, t0, "alpha")
	if a, ok := wdWindowAlarms(t, f.root, t0)["alpha"]; !ok || a.reason != "mismatch" {
		t.Fatalf("precondition: the divergent first report must raise a mismatch WINDOW for alpha, got %+v (raised %v)", a, ok)
	}

	// Within windowLastSeenRefresh, so the refresh operand of the same condition cannot do the write.
	wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(time.Minute), wdDeclared)
	t1 := t0.Add(time.Hour)
	body2Pass(f, t1, "alpha")

	if got := wdMustEntry(t, f.root, wdProfile, wdKey(wdOld, wdDeclared, wdModel)).Agents["alpha"].Applied; got != wdDeclared {
		t.Errorf("after the same launch came to agree, alpha's stored figure is %d, want %d", got, wdDeclared)
	}
	if a, ok := wdWindowAlarms(t, f.root, t1)["alpha"]; ok {
		t.Errorf("the WINDOW alarm stands after the session came to agree: %+v", a)
	}
	f.requireMailCount(t, 1, "the agreement is not announced")
}

func TestWindowGuardPins_AgreeingFigureBesideTheUnobservedMarkRaisesNothing(t *testing.T) {
	x, root := newWaFixture(), t.TempDir()
	installFakeTmuxPresent(t, session.SessionName("manager"), session.SessionName("ctl"))
	waRoster(t, root, "manager", "ctl")
	waPlantControl(t, root, x)
	f := x.fact("fable-5-2", "2.1.308", 1000000)
	waPlantFact(t, root, "manager", f)

	marked := x.entry(f, nil)
	marked.UnobservedNotified, marked.Source = true, ""
	waPlantRecord(t, root, "fable-5-2", marked)
	if a := waAlarmsFor(windowAlarms(root, x.now))["manager"]; a.reason != "unobserved" {
		t.Fatalf("precondition: the live manager's mark alone must raise unobserved, got %+v", a)
	}

	agreed := x.entry(f, map[string]int64{"manager": 1000000})
	agreed.UnobservedNotified = true
	waPlantRecord(t, root, "fable-5-2", agreed)
	got := waAlarmsFor(windowAlarms(root, x.now))
	waRequireControl(t, got)
	if a, ok := got["manager"]; ok {
		t.Errorf("an agreeing figure beside the unobserved mark raised %+v", a)
	}
}

func TestWindowGuardPins_LaunchWithNoFactRemovesThePreviousFact(t *testing.T) {
	for _, row := range []struct {
		name    string
		arrange func(t *testing.T, fx *k14Fixture)
	}{
		{"no harness version", func(t *testing.T, fx *k14Fixture) { wgStubNoVersion(t) }},
		{"no profile resolves", func(t *testing.T, fx *k14Fixture) {
			writeValidModels(t, fx.root, &config.ModelsConfig{Models: map[string]map[string]string{wdProfile: wgProfileExports()}})
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			fx, fake := newWGK14(t)
			row.arrange(t, fx)
			wdPlantFact(t, fx.root, "manager", wdFact(wdProfile, wdModel, wdDeclared, wdOld, time.Now().Add(-time.Hour)))

			wgRequireLaunched(t, fx.up(t, fake), "a launch that records no window fact")
			if _, err := os.Lstat(wdFactPath(fx.root, "manager")); !os.IsNotExist(err) {
				data, _ := os.ReadFile(wdFactPath(fx.root, "manager"))
				t.Errorf("the previous launch's fact still stands for a launch that wrote none (Lstat err %v):\n%s", err, data)
			}
		})
	}
}

func body2PlantMarkOnly(t *testing.T, root string) {
	t.Helper()
	now := time.Now()
	wdPlantRecord(t, root, wdProfile, map[string]windowRecord{wdKey(wdNew, wdDeclared, wdModel): {
		ModelID: wdModel, HarnessVersion: wdNew, Declared: wdDeclared, UnobservedNotified: true,
		FirstSeen: now.Add(-time.Hour), LastSeen: now,
	}})
}

func TestWindowGuardPins_MarkOnlyEntryIsAbsentToTheGate(t *testing.T) {
	t.Run("a watched launch still probes", func(t *testing.T) {
		fx, fake := newWGK14(t)
		body2PlantMarkOnly(t, fx.root)
		s := wgStubProbe(t, harnessWindowProbe{ModelID: wdModel, ContextWindow: wgApplied, HarnessVersion: wdNew}, nil)
		u := fx.up(t, fake)
		if s.calls != 1 {
			t.Errorf("the probe ran %d times for an identity holding only the unobserved mark, want once", s.calls)
		}
		wgRequireRefused(t, u, "a mark-only identity whose probe measures 200000")
	})

	t.Run("a respawn warns nothing from zero", func(t *testing.T) {
		fx, _ := newWGK14(t)
		body2PlantMarkOnly(t, fx.root)
		s := wgStubProbe(t, harnessWindowProbe{}, context.DeadlineExceeded)
		r := fx.respawn(t, false)
		if r.err != nil || r.line == "" {
			t.Fatalf("respawn on a mark-only identity must launch: err=%v\n%s", r.err, r.output)
		}
		if s.calls != 0 {
			t.Errorf("a respawn probed %d times, want never", s.calls)
		}
		if strings.Contains(r.output, "--accept-window") || strings.Contains(r.output, fmt.Sprintf("declares a %d-token window", wdDeclared)) {
			t.Errorf("a respawn on a mark-only identity printed window text:\n%s", r.output)
		}
	})

	t.Run("a failed probe leaves the next watched launch unrefused", func(t *testing.T) {
		fx, fake := newWGK14(t)
		body2PlantMarkOnly(t, fx.root)
		s := wgStubProbe(t, harnessWindowProbe{}, context.DeadlineExceeded)
		wgRequireLaunched(t, fx.up(t, fake), "the watched launch whose probe fails")
		fake.present[session.SessionName("manager")] = false
		u := fx.up(t, fake)
		if s.calls != 1 {
			t.Fatalf("the probe ran %d times, want once: the second launch must reach the gate unprobed", s.calls)
		}
		wgRequireLaunched(t, u, "a watched launch of a mark-only identity whose probe already failed")
		if strings.Contains(u.output, "--accept-window") {
			t.Errorf("the unprobed launch printed window text:\n%s", u.output)
		}
	})

	t.Run("--accept-window records no acceptance from zero", func(t *testing.T) {
		fx, fake := newWGK14(t)
		body2PlantMarkOnly(t, fx.root)
		wgStubProbe(t, harnessWindowProbe{}, context.DeadlineExceeded)
		setSlingFlag(t, &upAcceptWindow, true)
		u := fx.up(t, fake)
		wgRequireLaunched(t, u, "--accept-window on a mark-only identity")
		if v, has := wgFactJSON(t, fx.root, "manager")["accept_window"]; has {
			t.Errorf("the launch fact records accept_window=%v for an identity with no recorded shortfall", v)
		}
		if strings.Contains(u.output, "warning: --accept-window") {
			t.Errorf("--accept-window warned of a shortfall that is not recorded:\n%s", u.output)
		}
	})
}

func TestWindowGuardPins_UnobservedExemptions(t *testing.T) {
	setup := func(t *testing.T, alpha windowLaunchFact, withControl bool) *wdFixture {
		t.Helper()
		f := newWDFixture(t, "alpha", "beta")
		if err := os.WriteFile(statuslineGateFile(f.root), []byte("on\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		for _, a := range []string{"alpha", "beta"} {
			wdWriteJSON(t, filepath.Join(config.AgentDir(f.root, a), ".claude", "settings.json"),
				map[string]any{"statusLine": map[string]string{"type": "command", "command": "af statusline render"}})
		}
		wdPlantFact(t, f.root, "alpha", alpha)
		if withControl {
			wdPlantFact(t, f.root, "beta", wdFact(wdProfile, wdModel, wdDeclared, wdOld, alpha.LaunchedAt))
		}
		return f
	}
	launched := time.Now().Add(-time.Hour).Truncate(time.Second)
	requireOnlyControlMailed := func(t *testing.T, f *wdFixture, rep windowReport) {
		t.Helper()
		f.requireMailCount(t, 1, "only the control beta is unobserved")
		for _, m := range f.mail.sent {
			if !strings.Contains(m.body, `agent "beta"`) || strings.Contains(m.body, `agent "alpha"`) {
				t.Errorf("want only beta's unobserved mail, got %q", m.body)
			}
		}
		if got := body2FindingsFor(rep, "alpha"); len(got) != 0 {
			t.Errorf("an exempt launch produced findings %+v", got)
		}
		if got := body2FindingsFor(rep, "beta"); len(got) != 1 || got[0].State != "unobserved" {
			t.Errorf("control: beta must be unobserved, got %+v", got)
		}
	}

	t.Run("foreign launch is exempt", func(t *testing.T) {
		const foreign, foreignModel = "sol", "gpt-5.6-sol"
		alpha := wdFact(foreign, foreignModel, 1050000, wdOld, launched)
		alpha.Class = "foreign"
		f := setup(t, alpha, true)
		rep := body2Pass(f, time.Now(), "alpha", "beta")
		requireOnlyControlMailed(t, f, rep)
		if rf, ok := wdReadRecord(t, f.root, foreign); ok {
			t.Errorf("a foreign launch was marked unobserved: %+v", rf.Identities)
		}
	})

	t.Run("undeclared launch is exempt", func(t *testing.T) {
		f := setup(t, wdFact(wdProfile, wdModel, 0, wdOld, launched), true)
		rep := body2Pass(f, time.Now(), "alpha", "beta")
		requireOnlyControlMailed(t, f, rep)
		if rec, ok := wdEntry(t, f.root, wdProfile, wdKey(wdOld, 0, wdModel)); ok {
			t.Errorf("a launch that declares nothing was marked unobserved: %+v", rec)
		}
	})

	t.Run("a zero host total is not an observation", func(t *testing.T) {
		f := setup(t, wdFact(wdProfile, wdModel, wdDeclared, wdOld, launched), false)
		wdPlantSession(t, f.root, "alpha", "sess-alpha", launched.Add(2*time.Second), 0)
		rep := body2Pass(f, time.Now(), "alpha")

		if got := body2FindingsFor(rep, "alpha"); len(got) != 1 || got[0].State != "unobserved" {
			t.Errorf("findings for alpha = %+v, want one unobserved", got)
		}
		f.requireMailCount(t, 1, "the unobserved state is mailed once")
		for _, m := range f.mail.sent {
			if !strings.Contains(m.body, "has reported no window (no snapshot yet)") {
				t.Errorf("want the unobserved mail naming cause \"no snapshot yet\", got %q", m.body)
			}
			if strings.Contains(m.body, "applied 0") {
				t.Errorf("a zero host total was announced as a window: %q", m.body)
			}
		}
		body2RequireNoFigureFor(t, f.root, wdProfile, "alpha")
	})
}
