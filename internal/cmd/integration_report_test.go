package cmd

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/session"
)

// driftFixture is a supervisor agent whose pin binds a recorded required acme-int snapshot (no [check], no
// [service], so the respawn composes without exec or tmux).
type driftFixture struct {
	e        *intBEnv
	agentDir string
	snap     string
}

func newDriftFixture(t *testing.T) *driftFixture {
	t.Helper()
	e := intBFactory(t)
	agentDir := config.AgentDir(e.root, "supervisor")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatal(err)
	}
	snap := intBRecord(t, e, intBName, intBSource(e, intBName, intBManifestOpts{noCheck: true}), func(p *config.PluginEntry) {
		p.Integration.Service = ""
		p.Integration.ServiceProbe = ""
	})
	pinFixtureWrite(t, agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{
		pinFixtureBinding(t, e.root, intBName, snap, true),
	}})
	return &driftFixture{e: e, agentDir: agentDir, snap: snap}
}

func (fx *driftFixture) respawn(t *testing.T) string {
	t.Helper()
	mock := &mockTmux{}
	if err := respawnSession(RespawnOptions{
		FactoryRoot:  fx.e.root,
		AgentName:    "supervisor",
		AgentEntry:   config.AgentEntry{Type: "autonomous"},
		PaneID:       "%0",
		AgentWorkDir: fx.agentDir,
		Tx:           mock,
	}); err != nil {
		t.Fatalf("respawnSession must continue past a dropped integration: %v", err)
	}
	if len(mock.respawnPaneCalls) != 1 {
		t.Fatalf("RespawnPane calls = %d, want 1 (the agent continues)", len(mock.respawnPaneCalls))
	}
	return mock.respawnPaneCalls[0].cmd
}

func TestIntegrationReport_NotMailedWhenTheLaunchFails(t *testing.T) {
	fx := newDriftFixture(t)
	recs := installIntegrationReportRecorder(t)
	driftSnapshot(t, fx.snap, intBName)
	opts := RespawnOptions{
		FactoryRoot:  fx.e.root,
		AgentName:    "supervisor",
		AgentEntry:   config.AgentEntry{Type: "autonomous"},
		PaneID:       "%0",
		AgentWorkDir: fx.agentDir,
	}

	opts.Tx = &mockTmux{respawnErr: errors.New("pane gone")}
	if err := respawnSession(opts); err == nil {
		t.Fatal("respawnSession succeeded with a failing RespawnPane")
	}
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_NOT_BOUND")); n != 0 {
		t.Errorf("a failed respawn mailed %d INTEGRATION_NOT_BOUND, want 0: the reports are mailed after launch (IR:L588)", n)
	}

	opts.Tx = &mockTmux{}
	if err := respawnSession(opts); err != nil {
		t.Fatal(err)
	}
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_NOT_BOUND")); n != 1 {
		t.Errorf("the next successful respawn mailed %d INTEGRATION_NOT_BOUND, want 1: the failed launch must not have consumed the dedup marker", n)
	}
}

func TestIntegrationReport_DriftMailedOnce(t *testing.T) {
	t.Run("intact_pin_binds", func(t *testing.T) {
		fx := newDriftFixture(t)
		recs := installIntegrationReportRecorder(t)
		line := fx.respawn(t)
		if !strings.Contains(line, "--plugin-dir '"+filepath.Join(fx.snap, "claude-plugin")+"'") {
			t.Errorf("an intact pinned snapshot must be delivered on respawn:\n%s", line)
		}
		if n := len(reportsWithPrefix(*recs, "INTEGRATION_NOT_BOUND")); n != 0 {
			t.Errorf("an intact binding must not be reported, got %d", n)
		}
	})

	t.Run("drift_dropped_reported_once_logged_every_time", func(t *testing.T) {
		fx := newDriftFixture(t)
		recs := installIntegrationReportRecorder(t)
		driftSnapshot(t, fx.snap, intBName)

		for i := 0; i < 2; i++ {
			if line := fx.respawn(t); strings.Contains(line, fx.snap) {
				t.Errorf("respawn #%d delivered the drifted snapshot:\n%s", i+1, line)
			}
		}

		notBound := reportsWithPrefix(*recs, "INTEGRATION_NOT_BOUND")
		if len(notBound) != 1 {
			t.Fatalf("want exactly one INTEGRATION_NOT_BOUND across two recycles, got %d: %+v", len(notBound), *recs)
		}
		if notBound[0].to != "manager" {
			t.Errorf("recipient = %q, want manager", notBound[0].to)
		}
		wantBody := "integration \"acme-int\" not bound: content changed since consent (1 files; run af plugin verify acme-int)"
		if !strings.Contains(notBound[0].body, wantBody) {
			t.Errorf("body = %q, want it to contain %q", notBound[0].body, wantBody)
		}

		lines := readRecoveryLogLines(t, fx.e.root)
		if len(lines) != 2 {
			t.Fatalf("recovery log lines = %d, want 2", len(lines))
		}
		for i, l := range lines {
			if !slices.Equal(l.DroppedIntegrations, []string{intBName}) {
				t.Errorf("recovery line %d dropped_integrations = %q, want [acme-int]", i+1, l.DroppedIntegrations)
			}
		}
		if _, err := os.Stat(filepath.Join(fx.e.root, ".runtime", "integration_report", intBName+".not_bound")); err != nil {
			t.Errorf("dedup marker missing: %v", err)
		}
	})
}

func TestIntegrationReport_DegradedMailedOnceAcrossTicks(t *testing.T) {
	root := serviceTestRoot(t)
	cfg := &config.PluginsConfig{}
	writeServiceIntegration(t, root, cfg, svcFixture{name: "svcbad", session: "svcbad"})
	svcSavePlugins(t, root, cfg)
	svcWriteCheckRecord(t, root, "svcbad", "fail", "probe says boom")

	fake := newFakeTmux()
	fake.present["svcbad"] = true
	installServiceFake(t, fake)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	installServiceClock(t, &now)
	atCap := installServiceMail(t)
	recs := installIntegrationReportRecorder(t)

	degradedReports := func(reports []string) int {
		n := 0
		for _, r := range reports {
			if strings.Contains(r, "INTEGRATION_DEGRADED svcbad") {
				n++
			}
		}
		return n
	}
	for tick := 1; tick <= 2; tick++ {
		if n := degradedReports(svcRunEnsure(root)); n != 1 {
			t.Errorf("tick %d: ensure must still return the DEGRADED report (Phase 2 contract), got %d", tick, n)
		}
		now = now.Add(time.Minute)
	}
	mailed := reportsWithPrefix(*recs, "INTEGRATION_DEGRADED svcbad")
	if len(mailed) != 1 {
		t.Fatalf("want exactly one INTEGRATION_DEGRADED mail across two ticks, got %d: %+v", len(mailed), *recs)
	}
	if mailed[0].to != "manager" {
		t.Errorf("recipient = %q, want manager", mailed[0].to)
	}
	if got := svcOpsWithPrefix(fake.ops, "KillSession"); len(got) != 0 {
		t.Errorf("a degraded live service is never killed; recorded %v", got)
	}
	for _, m := range *atCap {
		if strings.Contains(m.subject, "DEGRADED") {
			t.Errorf("DEGRADED must go through the integration report egress, not the Phase 2 at-cap mail: %+v", m)
		}
	}

	svcWriteCheckRecord(t, root, "svcbad", "ok", "fine again")
	svcRunEnsure(root)
	if _, err := os.Stat(filepath.Join(root, ".runtime", "integration_report", "svcbad.degraded")); !os.IsNotExist(err) {
		t.Errorf("the degraded marker must clear once the check is ok again (stat err %v)", err)
	}
	svcWriteCheckRecord(t, root, "svcbad", "fail", "boom again")
	svcRunEnsure(root)
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_DEGRADED svcbad")); n != 2 {
		t.Errorf("a fresh degradation after recovery must mail again; total DEGRADED mails = %d, want 2", n)
	}
}

func TestIntegrationReport_ConcurrentHolderNotDoubleMailed(t *testing.T) {
	root := t.TempDir()
	recs := installIntegrationReportRecorder(t)
	held := lock.NewWithPath(integrationReportLockPath(root, intBName, integrationReportDegraded))
	if err := held.Acquire("concurrent-report"); err != nil {
		t.Fatal(err)
	}
	if err := reportIntegration(root, intBName, integrationReportDegraded, "INTEGRATION_DEGRADED "+intBName, "b"); err != nil {
		t.Fatalf("reportIntegration under a held lock: %v", err)
	}
	if len(*recs) != 0 {
		t.Errorf("mails while another reporter holds the lock = %d, want 0 (deliverCorrective copy, IR:L571-575)", len(*recs))
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := reportIntegration(root, intBName, integrationReportDegraded, "INTEGRATION_DEGRADED "+intBName, "b"); err != nil {
			t.Fatalf("reportIntegration %d: %v", i+1, err)
		}
	}
	if len(*recs) != 1 {
		t.Errorf("mails after release = %d, want exactly 1", len(*recs))
	}
}

func TestIntegrationReport_FallbackOptionalReportedSkipped(t *testing.T) {
	a := admFactory(t)
	recs := installIntegrationReportRecorder(t)
	admRecordHealthy(t, a, "other-int", nil)
	admWriteFormula(t, a, "maybe-claw", nil, []string{"defenseclaw"}, nil)

	var c session.LaunchContributions
	_, _, mails := composeIntegrations(a.root, a.agentDir, config.AgentEntry{Type: "autonomous", Formula: "maybe-claw"}, &c)
	if len(*recs) != 0 {
		t.Errorf("the composer mailed %d reports before any launch, want 0 (IR:L588): %+v", len(*recs), *recs)
	}
	launchReports{IntegrationMails: mails}.mailIntegrations(a.root, io.Discard)

	if n := len(reportsWithPrefix(*recs, "INTEGRATION_SKIPPED defenseclaw")); n != 1 {
		t.Errorf("INTEGRATION_SKIPPED defenseclaw mails = %d, want 1: an absent optional is skipped, not unbound (IR:L322); all=%+v", n, *recs)
	}
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_NOT_BOUND defenseclaw")); n != 0 {
		t.Errorf("INTEGRATION_NOT_BOUND defenseclaw mails = %d, want 0", n)
	}
}

// Factory scope binds unconditionally (DD:249), so an optional listing adds nothing: a drifted factory-scope
// snapshot is hash drift (DD:252), never the skipped-optional fallback.
func TestIntegrationReport_FallbackFactoryScopeOptionalReportedNotBound(t *testing.T) {
	a := admFactory(t)
	recs := installIntegrationReportRecorder(t)
	snap := intBRecord(t, a.intBEnv, "fleet-int", intBSource(a.intBEnv, "fleet-int", intBManifestOpts{noCheck: true, scope: "factory"}), func(p *config.PluginEntry) {
		p.Integration.Scope = config.IntegrationScopeFactory
	})
	a.fake.present["fleet-int-svc"] = true
	driftSnapshot(t, snap, "fleet-int")
	admWriteFormula(t, a, "maybe-fleet", nil, []string{"fleet-int"}, nil)

	var c session.LaunchContributions
	_, dropped, mails := composeIntegrations(a.root, a.agentDir, config.AgentEntry{Type: "autonomous", Formula: "maybe-fleet"}, &c)
	launchReports{IntegrationMails: mails}.mailIntegrations(a.root, io.Discard)

	if n := len(reportsWithPrefix(*recs, "INTEGRATION_NOT_BOUND fleet-int")); n != 1 {
		t.Errorf("INTEGRATION_NOT_BOUND fleet-int mails = %d, want 1: a drifted factory-scope integration is unbound even when listed optional; all=%+v", n, *recs)
	}
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_SKIPPED fleet-int")); n != 0 {
		t.Errorf("INTEGRATION_SKIPPED fleet-int mails = %d, want 0: factory scope outranks the optional listing", n)
	}
	if !slices.Contains(dropped, "fleet-int") {
		t.Errorf("dropped = %q, want fleet-int", dropped)
	}
}

// A service with no check record degrades only through its healthz; when the healthz recovers the condition
// has cleared, so the marker must clear and the next outage must mail again.
func TestIntegrationReport_DegradedClearsWithoutCheckRecord(t *testing.T) {
	var healthy atomic.Bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !healthy.Load() {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	}))
	defer srv.Close()

	root := serviceTestRoot(t)
	cfg := &config.PluginsConfig{}
	writeServiceIntegration(t, root, cfg, svcFixture{name: "svcflap", session: "svcflap", probe: "http-healthz", healthzURL: srv.URL + "/healthz"})
	svcSavePlugins(t, root, cfg)
	fake := newFakeTmux()
	fake.present["svcflap"] = true
	installServiceFake(t, fake)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	installServiceClock(t, &now)
	installServiceMail(t)
	recs := installIntegrationReportRecorder(t)
	marker := filepath.Join(root, ".runtime", "integration_report", "svcflap.degraded")

	svcRunEnsure(root)
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_DEGRADED svcflap")); n != 1 {
		t.Fatalf("fixture: a failing healthz must mail DEGRADED once; got %d", n)
	}
	healthy.Store(true)
	now = now.Add(time.Minute)
	svcRunEnsure(root)
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Errorf("the degraded marker must clear once the healthz recovers and no check record says otherwise (stat err %v)", err)
	}
	healthy.Store(false)
	now = now.Add(time.Minute)
	svcRunEnsure(root)
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_DEGRADED svcflap")); n != 2 {
		t.Errorf("a fresh outage after recovery must mail again; total DEGRADED mails = %d, want 2", n)
	}
}

// A failed check on a factory-scope binding is the composer's DEGRADED report (DD:252, DD:349, DD:383): the
// integration stays bound, the report is mailed once per state change, and only a record for the bound
// snapshot counts.
func TestIntegrationReport_FactoryScopeFailedCheckDegraded(t *testing.T) {
	a := admFactory(t)
	recs := installIntegrationReportRecorder(t)
	intBRecord(t, a.intBEnv, "fleet-int", intBSource(a.intBEnv, "fleet-int", intBManifestOpts{noCheck: true, scope: "factory"}), func(p *config.PluginEntry) {
		p.Integration.Scope = config.IntegrationScopeFactory
	})
	a.fake.present["fleet-int-svc"] = true
	admWriteFormula(t, a, "plain", nil, nil, nil)
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(a.root))
	if err != nil {
		t.Fatal(err)
	}
	sha := cfg.Plugins["fleet-int"].Integration.ContentSHA256
	entry := config.AgentEntry{Type: "autonomous", Formula: "plain"}
	compose := func() (session.LaunchContributions, []string) {
		var c session.LaunchContributions
		reports, _, mails := composeIntegrations(a.root, a.agentDir, entry, &c)
		launchReports{IntegrationMails: mails}.mailIntegrations(a.root, io.Discard)
		return c, reports
	}
	degraded := func(reports []string) int {
		n := 0
		for _, r := range reports {
			if strings.HasPrefix(r, "INTEGRATION_DEGRADED fleet-int: ") {
				n++
			}
		}
		return n
	}

	admWriteCheckRecordState(t, a.root, "fleet-int", "fail", strings.Repeat("0", 64), time.Now())
	if _, reports := compose(); degraded(reports) != 0 {
		t.Errorf("a failing record for another snapshot says nothing about the bound one; reports %q", reports)
	}

	admWriteCheckRecordState(t, a.root, "fleet-int", "fail", sha, time.Now())
	for launch := 1; launch <= 2; launch++ {
		c, reports := compose()
		if degraded(reports) != 1 || !strings.Contains(strings.Join(reports, "\n"), "check-broken") {
			t.Errorf("launch %d: want one INTEGRATION_DEGRADED fleet-int report carrying the check output; got %q", launch, reports)
		}
		if len(c.PluginDirs) == 0 {
			t.Errorf("launch %d: a factory-scope integration with a failed check is bound anyway (D4-a); PluginDirs empty", launch)
		}
	}
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_DEGRADED fleet-int")); n != 1 {
		t.Errorf("INTEGRATION_DEGRADED fleet-int mails over two launches = %d, want 1", n)
	}

	admWriteCheckRecordState(t, a.root, "fleet-int", integrationCheckOK, sha, time.Now())
	compose()
	admWriteCheckRecordState(t, a.root, "fleet-int", "fail", sha, time.Now())
	compose()
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_DEGRADED fleet-int")); n != 2 {
		t.Errorf("a check that recovers and fails again must mail again; total = %d, want 2", n)
	}
}

// NOT_BOUND is deduplicated per condition, not forever: once the pinned snapshot binds again the condition
// has cleared, so a later drift must mail again (IR C22).
func TestIntegrationReport_NotBoundRearmsAfterRebind(t *testing.T) {
	fx := newDriftFixture(t)
	recs := installIntegrationReportRecorder(t)
	skill := filepath.Join(fx.snap, "claude-plugin", "skills", intBName, "SKILL.md")
	original, err := os.ReadFile(skill)
	if err != nil {
		t.Fatal(err)
	}

	driftSnapshot(t, fx.snap, intBName)
	fx.respawn(t)
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_NOT_BOUND")); n != 1 {
		t.Fatalf("fixture: the first drift must mail once; got %d", n)
	}

	if err := intBChmodTree(fx.snap, true); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(skill, original, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := intBChmodTree(fx.snap, false); err != nil {
		t.Fatal(err)
	}
	if line := fx.respawn(t); !strings.Contains(line, fx.snap) {
		t.Fatalf("fixture: the restored snapshot must bind again:\n%s", line)
	}

	driftSnapshot(t, fx.snap, intBName)
	fx.respawn(t)
	if n := len(reportsWithPrefix(*recs, "INTEGRATION_NOT_BOUND")); n != 2 {
		t.Errorf("a drift after the snapshot bound again must mail again; total INTEGRATION_NOT_BOUND = %d, want 2", n)
	}
}
