package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

type p724bSvcEnv struct {
	root    string
	cfg     *config.PluginsConfig
	fake    *fakeTmux
	now     *time.Time
	capMail *[]svcSentMail
	mailed  *[]recordedReport
}

func p724bServiceEnv(t *testing.T) *p724bSvcEnv {
	t.Helper()
	e := &p724bSvcEnv{root: serviceTestRoot(t), cfg: &config.PluginsConfig{}, fake: newFakeTmux()}
	installServiceFake(t, e.fake)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	e.now = &now
	installServiceClock(t, e.now)
	e.capMail = installServiceMail(t)
	e.mailed = installIntegrationReportRecorder(t)
	return e
}

// p724bSealedService records svcdemo under its real content hash and proves the snapshot binds.
func p724bSealedService(t *testing.T, e *p724bSvcEnv) string {
	t.Helper()
	snap := writeServiceIntegration(t, e.root, e.cfg, svcFixture{name: "svcdemo", session: "svcdemo"})
	svcSavePlugins(t, e.root, e.cfg)
	if _, err := config.BindIntegration(e.root, "svcdemo", e.cfg.Plugins["svcdemo"].Integration); err != nil {
		t.Fatalf("fixture: the sealed snapshot must bind before it is altered: %v", err)
	}
	return snap
}

func p724bDriftRunScript(t *testing.T, snap string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(snap, "bin", "serve.sh"), []byte("#!/bin/sh\necho TAMPERED\nexec sleep 3600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// p724bBindRefusal is the refusal text the bind primitive gives for the current on-disk record; the ensure must
// report exactly this text (decision D5.4).
func p724bBindRefusal(t *testing.T, root, name string) string {
	t.Helper()
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		t.Fatal(err)
	}
	_, err = config.BindIntegration(root, name, cfg.Plugins[name].Integration)
	var be *config.IntegrationBindError
	if !errors.As(err, &be) {
		t.Fatalf("fixture: %s must be refused by the bind primitive, got %v", name, err)
	}
	return err.Error()
}

func p724bStateBytes(t *testing.T, root, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(integrationServiceStatePath(root, name))
	if err != nil {
		t.Fatalf("reading service state: %v", err)
	}
	return b
}

func TestPR724_T5_DriftedServiceReportedNeverLaunched(t *testing.T) {
	e := p724bServiceEnv(t)
	snap := p724bSealedService(t, e)
	p724bDriftRunScript(t, snap)
	want := p724bBindRefusal(t, e.root, "svcdemo")

	reports := svcRunEnsure(e.root)

	if len(e.fake.ops) != 0 {
		t.Errorf("a drifted snapshot is refused before any probe or launch; tmux ops = %v", e.fake.ops)
	}
	if !slices.Equal(reports, []string{want}) {
		t.Errorf("reports = %q, want exactly the bind refusal [%q]", reports, want)
	}
	if _, ok := readServiceState(t, e.root, "svcdemo"); ok {
		t.Errorf("a refused bind is not a launch attempt: no relaunch state may be written")
	}
}

func TestPR724_T5_EveryBindRefusalReportedMailedNeverLaunched(t *testing.T) {
	for _, tc := range []struct {
		name  string
		alter func(t *testing.T, e *p724bSvcEnv, snap string)
	}{
		{"drifted_run_script", func(t *testing.T, _ *p724bSvcEnv, snap string) { p724bDriftRunScript(t, snap) }},
		{"missing_snapshot", func(t *testing.T, _ *p724bSvcEnv, snap string) {
			if err := os.RemoveAll(snap); err != nil {
				t.Fatal(err)
			}
		}},
		{"unparseable_manifest", func(t *testing.T, _ *p724bSvcEnv, snap string) {
			if err := os.WriteFile(filepath.Join(snap, config.IntegrationManifestFile), []byte("name = [unterminated\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}},
		{"acquisition_decoy_snapshot_dir", func(t *testing.T, e *p724bSvcEnv, _ string) {
			rel, err := filepath.Rel(e.root, filepath.Join(config.PluginsDir(e.root), "svcdemo"))
			if err != nil {
				t.Fatal(err)
			}
			e.cfg.Plugins["svcdemo"].Integration.SnapshotDir = filepath.ToSlash(rel)
			svcSavePlugins(t, e.root, e.cfg)
		}},
		{"absolute_snapshot_dir", func(t *testing.T, e *p724bSvcEnv, snap string) {
			e.cfg.Plugins["svcdemo"].Integration.SnapshotDir = snap
			svcSavePlugins(t, e.root, e.cfg)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := p724bServiceEnv(t)
			snap := p724bSealedService(t, e)
			tc.alter(t, e, snap)
			want := p724bBindRefusal(t, e.root, "svcdemo")

			reports := svcRunEnsure(e.root)

			if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand "); len(got) != 0 {
				t.Errorf("a snapshot that does not bind must never be launched; started %v", got)
			}
			if len(e.fake.ops) != 0 {
				t.Errorf("the refusal precedes any probe; tmux ops = %v", e.fake.ops)
			}
			if !slices.Equal(reports, []string{want}) {
				t.Errorf("reports = %q, want exactly the bind refusal [%q]", reports, want)
			}
			mails := reportsWithPrefix(*e.mailed, "INTEGRATION_NOT_BOUND svcdemo")
			if len(mails) != 1 || mails[0].subject != "INTEGRATION_NOT_BOUND svcdemo" || mails[0].to != integrationReportRecipient || mails[0].body != want {
				t.Errorf("want one INTEGRATION_NOT_BOUND svcdemo mail to %s whose body is the refusal; got %+v", integrationReportRecipient, *e.mailed)
			}
			if _, ok := readServiceState(t, e.root, "svcdemo"); ok {
				t.Errorf("a refused bind records no launch")
			}
		})
	}
}

func TestPR724_T5_DriftMailedOnceUnderSharedNotBoundMarker(t *testing.T) {
	e := p724bServiceEnv(t)
	snap := p724bSealedService(t, e)
	p724bDriftRunScript(t, snap)
	want := p724bBindRefusal(t, e.root, "svcdemo")

	for tick := 1; tick <= 2; tick++ {
		if reports := svcRunEnsure(e.root); !slices.Equal(reports, []string{want}) {
			t.Errorf("tick %d: reports = %q, want the refusal [%q] on every tick", tick, reports, want)
		}
		*e.now = e.now.Add(time.Minute)
	}

	if got := reportsWithPrefix(*e.mailed, "INTEGRATION_NOT_BOUND svcdemo"); len(got) != 1 {
		t.Errorf("one drift is mailed once across ticks; NOT_BOUND mails = %+v", *e.mailed)
	}
	if _, err := os.Stat(integrationReportMarker(e.root, "svcdemo", integrationReportNotBound)); err != nil {
		t.Errorf("the mail must be deduplicated under the composer's shared not_bound marker: %v", err)
	}
}

func TestPR724_T5_RefusedBindLeavesRelaunchStateUntouched(t *testing.T) {
	e := p724bServiceEnv(t)
	snap := p724bSealedService(t, e)
	writeServiceState(t, e.root, "svcdemo", integrationServiceState{
		LaunchedAt:            e.now.Add(-2 * time.Hour).Format(time.RFC3339Nano),
		ConsecutiveRelaunches: integrationServiceCap - 1,
	})
	before := p724bStateBytes(t, e.root, "svcdemo")
	p724bDriftRunScript(t, snap)

	svcRunEnsure(e.root)

	if after := p724bStateBytes(t, e.root, "svcdemo"); !bytes.Equal(before, after) {
		t.Errorf("a refused bind must not touch the relaunch state:\nbefore %s\nafter  %s", before, after)
	}
	if len(*e.capMail) != 0 {
		t.Errorf("a refused bind is not a relaunch: no keeps-dying mail; sent %+v", *e.capMail)
	}
	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand "); len(got) != 0 {
		t.Errorf("a drifted snapshot is never launched; started %v", got)
	}
}

func TestPR724_T5_LiveDriftedServiceReportedNotBound(t *testing.T) {
	e := p724bServiceEnv(t)
	snap := p724bSealedService(t, e)
	e.fake.present["svcdemo"] = true
	svcWriteCheckRecord(t, e.root, "svcdemo", "fail", "probe says boom")
	writeServiceState(t, e.root, "svcdemo", integrationServiceState{
		LaunchedAt:            e.now.Add(-time.Hour).Format(time.RFC3339Nano),
		ConsecutiveRelaunches: 2,
	})
	before := p724bStateBytes(t, e.root, "svcdemo")
	p724bDriftRunScript(t, snap)
	want := p724bBindRefusal(t, e.root, "svcdemo")

	reports := svcRunEnsure(e.root)

	if !slices.Equal(reports, []string{want}) {
		t.Errorf("a live service whose snapshot drifted is reported NOT_BOUND, not probed into DEGRADED; reports = %q, want [%q]", reports, want)
	}
	if len(e.fake.ops) != 0 {
		t.Errorf("the refusal precedes the liveness probe; tmux ops = %v", e.fake.ops)
	}
	if got := reportsWithPrefix(*e.mailed, "INTEGRATION_DEGRADED"); len(got) != 0 {
		t.Errorf("no DEGRADED handling runs on unverified content; mailed %+v", got)
	}
	if after := p724bStateBytes(t, e.root, "svcdemo"); !bytes.Equal(before, after) {
		t.Errorf("the relaunch counters of a drifted live service are not reset:\nbefore %s\nafter  %s", before, after)
	}
}

func TestPR724_T5_KeepLiveDriftedServiceNeverKilledOrRelaunched(t *testing.T) {
	e := p724bServiceEnv(t)
	snap := p724bSealedService(t, e)
	e.fake.present["svcdemo"] = true
	p724bDriftRunScript(t, snap)

	svcRunEnsure(e.root)

	for _, op := range e.fake.ops {
		if strings.HasPrefix(op, "KillSession ") || strings.HasPrefix(op, "NewSessionWithCommand ") {
			t.Errorf("af never kills or relaunches a live service, drifted or not: %q", op)
		}
	}
}

func TestPR724_T5_KeepIntactSealedSnapshotLaunchedOnce(t *testing.T) {
	e := p724bServiceEnv(t)
	snap := p724bSealedService(t, e)

	reports := svcRunEnsure(e.root)

	want := "NewSessionWithCommand svcdemo " + snap + " " + shellQuote(filepath.Join(snap, "bin", "serve.sh"))
	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand "); len(got) != 1 || got[0] != want {
		t.Errorf("an intact snapshot starts exactly once from its content-addressed snapshot:\n got %v\nwant [%s]", got, want)
	}
	if len(reports) != 0 {
		t.Errorf("an intact snapshot is not reported; got %q", reports)
	}
	if len(*e.mailed) != 0 {
		t.Errorf("an intact snapshot is not mailed; got %+v", *e.mailed)
	}
}

func TestPR724_T5_KeepEnsureNeverClearsNotBoundMarker(t *testing.T) {
	for _, live := range []bool{false, true} {
		name := "down_service_launched"
		if live {
			name = "live_service"
		}
		t.Run(name, func(t *testing.T) {
			e := p724bServiceEnv(t)
			p724bSealedService(t, e)
			e.fake.present["svcdemo"] = live
			marker := integrationReportMarker(e.root, "svcdemo", integrationReportNotBound)
			if err := os.MkdirAll(filepath.Dir(marker), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(marker, nil, 0o644); err != nil {
				t.Fatal(err)
			}

			svcRunEnsure(e.root)

			if _, err := os.Stat(marker); err != nil {
				t.Errorf("only the composer clears the shared not_bound marker; the ensure removed it: %v", err)
			}
		})
	}
}
