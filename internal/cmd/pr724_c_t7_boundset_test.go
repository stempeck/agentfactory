package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

func p724cRecordFactoryScope(t *testing.T, a *admEnv, name string) string {
	t.Helper()
	snap := intBRecord(t, a.intBEnv, name, intBSource(a.intBEnv, name, intBManifestOpts{noCheck: true, scope: "factory"}), func(p *config.PluginEntry) {
		p.Integration.Scope = config.IntegrationScopeFactory
	})
	a.fake.present[name+"-svc"] = true
	return snap
}

func p724cPinNames(pin integrationPin) []string {
	var names []string
	for _, b := range pin.Bindings {
		names = append(names, b.Name)
	}
	slices.Sort(names)
	return names
}

func p724cWritePluginsJSON(t *testing.T, root, body string) {
	t.Helper()
	p := config.PluginsConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPR724_T7_PinnedComposeBindsExactlyThePin(t *testing.T) {
	a := admFactory(t)
	xSnap := p724cRecordFactoryScope(t, a, "fleet-x")
	b := pinFixtureBinding(t, a.root, "fleet-x", xSnap, false)
	b.EnvKeys = []string{"FLEET_X_A", "FLEET_X_B"}
	pinPath := pinFixtureWrite(t, a.agentDir, integrationPin{Formula: "plain", Bindings: []integrationPinBinding{b}})
	before, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatal(err)
	}
	p724cRecordFactoryScope(t, a, "fleet-y")
	admWriteFormula(t, a, "plain", nil, nil, nil)

	var c session.LaunchContributions
	reports, dropped, mails := composeIntegrations(a.root, a.agentDir, config.AgentEntry{Type: "autonomous", Formula: "plain"}, &c)

	if want := []string{filepath.Join(xSnap, "claude-plugin")}; !slices.Equal(c.PluginDirs, want) {
		t.Errorf("PluginDirs = %q, want exactly the pinned %q", c.PluginDirs, want)
	}
	for _, ev := range c.IntegrationEnv {
		if strings.HasPrefix(ev.Key, "FLEET_Y_") {
			t.Errorf("IntegrationEnv carries %s from an integration installed after the pin", ev.Key)
		}
	}
	if strings.Contains(c.HookFailModes, "fleet-y") {
		t.Errorf("HookFailModes = %q names the post-pin install", c.HookFailModes)
	}
	if slices.Contains(dropped, "fleet-y") {
		t.Errorf("dropped = %q: a post-pin install is not part of this instance, so it is not a drop", dropped)
	}
	for _, r := range reports {
		if strings.Contains(r, "fleet-y") {
			t.Errorf("report names the post-pin install: %q", r)
		}
	}
	for _, m := range mails {
		if m.name == "fleet-y" {
			t.Errorf("mail for the post-pin install: %+v", m)
		}
	}
	// Unbound is not uncleared: a value an earlier launch left in the pane must still be unset.
	if !slices.Contains(c.IntegrationKeyUniverse, "FLEET_Y_A") {
		t.Errorf("IntegrationKeyUniverse = %q, want the unbound install's keys still cleared", c.IntegrationKeyUniverse)
	}
	if after, _ := os.ReadFile(pinPath); string(after) != string(before) {
		t.Errorf("compose rewrote the pin")
	}
}

// The delivered respawn line, executed, of a pinned instance after a later factory-scope install.
func TestPR724_T7_RespawnIgnoresLaterFactoryScopeInstall(t *testing.T) {
	fx := newK14Fixture(t)
	x := recordFactoryIntegration(t, fx.root, "xint", "X_TOKEN", "x")
	setupHermeticSessions(t)
	b := pinFixtureBinding(t, fx.root, "xint", x, false)
	b.EnvKeys = []string{"X_TOKEN"}
	pinPath := pinFixtureWrite(t, fx.agentDir, integrationPin{Formula: "f", Bindings: []integrationPinBinding{b}})
	before, err := os.ReadFile(pinPath)
	if err != nil {
		t.Fatal(err)
	}
	recordFactoryIntegration(t, fx.root, "yint", "LATER_TOKEN", "l")

	l := fx.respawn(t, false)
	run := fx.exec(t, l.line)

	if dirs := pinArgPluginDirs(run.args); !slices.Equal(dirs, []string{filepath.Join(x, "claude-plugin")}) {
		t.Errorf("respawn --plugin-dir = %q, want exactly the pinned %s", dirs, filepath.Join(x, "claude-plugin"))
	}
	if v, ok := run.env["LATER_TOKEN"]; ok {
		t.Errorf("respawn delivered LATER_TOKEN=%q from an integration installed after the pin", v)
	}
	if got := run.env["AF_INTEGRATION_HOOK_FAIL_MODES"]; strings.Contains(got, "yint") {
		t.Errorf("AF_INTEGRATION_HOOK_FAIL_MODES=%q names the post-pin install", got)
	}
	if run.env["X_TOKEN"] != "x" {
		t.Errorf("X_TOKEN = %q, want the pinned x", run.env["X_TOKEN"])
	}
	if strings.Contains(l.output, "yint") {
		t.Errorf("respawn output names the post-pin install:\n%s", l.output)
	}
	if after, _ := os.ReadFile(pinPath); string(after) != string(before) {
		t.Errorf("respawn rewrote the pin")
	}
}

// A factory-scope integration that cannot bind at pin time is still part of the instance's consent: the pin
// names it, so every launch reports it and af prime shows it, instead of a pin-exact composer losing it.
func TestPR724_T7_PinTimeDriftedFactoryScopeIsPinnedAndReported(t *testing.T) {
	a := admFactory(t)
	xSnap := p724cRecordFactoryScope(t, a, "fleet-x")
	ySnap := p724cRecordFactoryScope(t, a, "fleet-y")
	driftSnapshot(t, xSnap, "fleet-x")
	admWriteFormula(t, a, "plain", nil, nil, nil)
	if out, err := admInstantiate(t, a, "plain", true); err != nil {
		t.Fatalf("instantiate: %v\n%s", err, out)
	}

	pin, ok := pinFixtureRead(t, a.agentDir)
	if !ok {
		t.Fatal("no pin written, but the healthy factory-scope fleet-y binds")
	}
	if got := p724cPinNames(pin); !slices.Equal(got, []string{"fleet-x", "fleet-y"}) {
		t.Errorf("pin names %q, want [fleet-x fleet-y]: a pin-time drop must be pinned so each launch reports it", got)
	}
	for _, pb := range pin.Bindings {
		if pb.Name == "fleet-x" && pb.ContentSHA256 != filepath.Base(xSnap) {
			t.Errorf("pinned fleet-x content = %q, want the consented %q", pb.ContentSHA256, filepath.Base(xSnap))
		}
	}

	var c session.LaunchContributions
	reports, dropped, mails := composeIntegrations(a.root, a.agentDir, config.AgentEntry{Type: "autonomous", Formula: "plain"}, &c)
	if want := []string{filepath.Join(ySnap, "claude-plugin")}; !slices.Equal(c.PluginDirs, want) {
		t.Errorf("PluginDirs = %q, want %q", c.PluginDirs, want)
	}
	if !slices.Contains(dropped, "fleet-x") {
		t.Errorf("dropped = %q, want fleet-x", dropped)
	}
	if !slices.ContainsFunc(reports, func(r string) bool { return strings.Contains(r, `"fleet-x"`) && strings.Contains(r, "content changed") }) {
		t.Errorf("no drift report for fleet-x; reports %q", reports)
	}
	if !slices.ContainsFunc(mails, func(m integrationMail) bool { return m.subject == "INTEGRATION_NOT_BOUND fleet-x" }) {
		t.Errorf("no INTEGRATION_NOT_BOUND fleet-x mail; mails %+v", mails)
	}

	var prime bytes.Buffer
	outputIntegrationLines(&prime, a.root, a.agentDir)
	if !strings.Contains(prime.String(), "integration fleet-x: not bound: content changed (") {
		t.Errorf("af prime integration lines lack fleet-x's drift:\n%s", prime.String())
	}
}

// The write-or-clear condition is unchanged: a pin turns the guard on, so a failed factory-scope bind
// alone must not create one.
func TestPR724_T7_KeepNoPinWhenOnlyFactoryScopeFailsToBind(t *testing.T) {
	a := admFactory(t)
	xSnap := p724cRecordFactoryScope(t, a, "fleet-x")
	driftSnapshot(t, xSnap, "fleet-x")
	admWriteFormula(t, a, "plain", nil, nil, nil)
	if out, err := admInstantiate(t, a, "plain", true); err != nil {
		t.Fatalf("instantiate: %v\n%s", err, out)
	}
	if pin, ok := pinFixtureRead(t, a.agentDir); ok {
		t.Errorf("a pin was written for a non-declaring formula whose only factory-scope integration fails to bind: %+v", pin)
	}
}

func TestPR724_T7_KeepMalformedPinBindsFactoryScopeOnly(t *testing.T) {
	a := admFactory(t)
	xSnap := p724cRecordFactoryScope(t, a, "fleet-x")
	admRecordHealthy(t, a, "acme-int", nil)
	admWriteFormula(t, a, "needs-acme", []string{"acme-int"}, nil, nil)
	p := integrationPinPath(a.agentDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var c session.LaunchContributions
	reports, _, _ := composeIntegrations(a.root, a.agentDir, config.AgentEntry{Type: "autonomous", Formula: "needs-acme"}, &c)

	if want := []string{filepath.Join(xSnap, "claude-plugin")}; !slices.Equal(c.PluginDirs, want) {
		t.Errorf("PluginDirs = %q, want the factory-scope set only %q", c.PluginDirs, want)
	}
	if !slices.ContainsFunc(reports, func(r string) bool {
		return strings.Contains(r, "is malformed") && strings.HasSuffix(r, "; binding factory-scope integrations only")
	}) {
		t.Errorf("an unreadable pin must be reported with its fallback; reports = %q", reports)
	}
}

// Before an instance is pinned the composer only re-hashes; it never runs or reads [check] or [service], so
// an optional whose service is down still binds when its snapshot is intact.
func TestPR724_T7_KeepUnpinnedComposeBindsIntactOptional(t *testing.T) {
	a := admFactory(t)
	snap := intBRecord(t, a.intBEnv, "opt-int", intBSource(a.intBEnv, "opt-int", intBManifestOpts{noCheck: true}), nil)
	admWriteFormula(t, a, "opt-only", nil, []string{"opt-int"}, nil)

	var c session.LaunchContributions
	composeIntegrations(a.root, a.agentDir, config.AgentEntry{Type: "autonomous", Formula: "opt-only"}, &c)

	if want := []string{filepath.Join(snap, "claude-plugin")}; !slices.Equal(c.PluginDirs, want) {
		t.Errorf("PluginDirs = %q, want the intact optional %q", c.PluginDirs, want)
	}
}

// Writing a pin that silently lacks the factory-scope set would bind that partial set for the whole
// instance, so an unreadable plugins.json refuses the write when admission bound something.
func TestPR724_T7_PinWriterSurfacesPluginsLoadError(t *testing.T) {
	a := admFactory(t)
	p724cWritePluginsJSON(t, a.root, "{not json\n")
	bound := []config.IntegrationBinding{{Name: "acme-int", SnapshotDir: ".agentfactory/integrations/acme-int/x", ContentSHA256: "x"}}

	err := pinAdmittedIntegrations(a.root, a.agentDir, "needs-acme", bound, nil)

	if err == nil || !strings.Contains(err.Error(), "writing integration pin") {
		t.Errorf("err = %v, want the plugins.json load error surfaced as a pin-write error", err)
	}
	if pin, ok := pinFixtureRead(t, a.agentDir); ok {
		t.Errorf("a pin was written without the factory-scope set: %+v", pin)
	}
}

// With nothing bound or skipped the instance stays unpinned, and the unpinned composer reports the same
// load error at every launch, so this case needs no refusal.
func TestPR724_T7_KeepPinWriterClearsPinWhenNothingBoundOnLoadError(t *testing.T) {
	a := admFactory(t)
	pinFixtureWrite(t, a.agentDir, integrationPin{Formula: "old", Bindings: []integrationPinBinding{{Name: "stale", ContentSHA256: "s"}}})
	p724cWritePluginsJSON(t, a.root, "{not json\n")

	if err := pinAdmittedIntegrations(a.root, a.agentDir, "plain", nil, nil); err != nil {
		t.Errorf("err = %v, want nil: nothing would be pinned", err)
	}
	if pin, ok := pinFixtureRead(t, a.agentDir); ok {
		t.Errorf("the stale pin survived: %+v", pin)
	}
}
