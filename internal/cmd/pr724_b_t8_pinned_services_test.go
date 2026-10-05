package cmd

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/worktree"
)

type p724bSnap struct {
	rel, sha, abs string
}

// p724bWriteSnapshot stages a consumed snapshot under its real content hash, so it binds once the ensure
// re-hashes before it launches.
func p724bWriteSnapshot(t *testing.T, root, name, scope, session, marker string) p724bSnap {
	t.Helper()
	stage := filepath.Join(config.IntegrationsDir(root), name, "staging-"+marker)
	manifest := fmt.Sprintf("name = %q\ndescription = \"pinned service fixture %s\"\nscope = %q\n", name, marker, scope)
	if session != "" {
		manifest += fmt.Sprintf("\n[service]\nsession = %q\nrun = \"bin/serve.sh\"\nprobe = \"tmux-session\"\n", session)
	}
	if err := os.MkdirAll(filepath.Join(stage, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, config.IntegrationManifestFile), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(stage, "bin", "serve.sh"), []byte("#!/bin/sh\n# "+marker+"\nexec sleep 3600\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	m, err := config.LoadIntegrationManifestNamed(stage, name)
	if err != nil {
		t.Fatalf("fixture manifest: %v", err)
	}
	sum, _, err := config.IntegrationContentHash(stage, config.IntegrationDeclaredPaths(m))
	if err != nil {
		t.Fatalf("fixture hash: %v", err)
	}
	abs := filepath.Join(config.IntegrationsDir(root), name, sum)
	if err := os.Rename(stage, abs); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		t.Fatal(err)
	}
	return p724bSnap{rel: filepath.ToSlash(rel), sha: sum, abs: abs}
}

func p724bRecord(cfg *config.PluginsConfig, name, scope, session string, s p724bSnap) {
	if cfg.Plugins == nil {
		cfg.Plugins = map[string]config.PluginEntry{}
	}
	cfg.Plugins[name] = config.PluginEntry{
		Source:      "https://example.invalid/" + name,
		Commit:      strings.Repeat("c", 40),
		InstalledAt: "2026-09-28T00:00:00Z",
		Integration: &config.PluginIntegration{
			ManifestSHA256:      strings.Repeat("b", 64),
			ContentSHA256:       s.sha,
			Scope:               scope,
			FactoryWide:         scope == config.IntegrationScopeFactory,
			ClaudePlugins:       []string{},
			EnvKeys:             []string{},
			Service:             session,
			ServiceProbe:        "tmux-session",
			HookFailMode:        "open",
			ExternalWrites:      []string{},
			ExternalWriteHashes: map[string]string{},
			Artifacts:           []config.IntegrationArtifact{},
			Source:              "clone",
			SnapshotDir:         s.rel,
			StagedAt:            "2026-09-28T00:00:00Z",
		},
	}
}

func p724bPin(t *testing.T, agentDir string, bindings ...integrationPinBinding) {
	t.Helper()
	pin := &integrationPin{V: integrationPinVersion, Formula: "pr724-formula", Bindings: bindings, Skipped: []integrationPinSkipped{}}
	if err := writeIntegrationPin(agentDir, pin); err != nil {
		t.Fatalf("write pin: %v", err)
	}
}

func p724bBinding(name string, s p724bSnap, required bool) integrationPinBinding {
	return integrationPinBinding{Name: name, SnapshotDir: s.rel, ContentSHA256: s.sha, EnvKeys: []string{}, Required: required, ClaudePlugins: []string{}}
}

func p724bLaunch(session string, s p724bSnap) string {
	return fmt.Sprintf("NewSessionWithCommand %s %s %s", session, s.abs, shellQuote(filepath.Join(s.abs, "bin", "serve.sh")))
}

type p724bPinEnv struct {
	root  string
	fake  *fakeTmux
	mails *[]svcSentMail
}

func p724bPinSetup(t *testing.T) p724bPinEnv {
	t.Helper()
	root := serviceTestRoot(t)
	fake := newFakeTmux()
	installServiceFake(t, fake)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	installServiceClock(t, &now)
	installIntegrationReportRecorder(t)
	return p724bPinEnv{root: root, fake: fake, mails: installServiceMail(t)}
}

// p724bEnsure is the call af up and the watchdog make through the seam.
func p724bEnsure(root string) []string {
	return ensureIntegrationServicesFn(context.Background(), &cobra.Command{}, root, serviceScopeFactory)
}

// p724bPinnedFormulaService records one formula-scope service and pins it from agent alpha of root.
func p724bPinnedFormulaService(t *testing.T, e p724bPinEnv, name string) p724bSnap {
	t.Helper()
	cfg := &config.PluginsConfig{}
	s := p724bWriteSnapshot(t, e.root, name, "formula", name, "v1")
	p724bRecord(cfg, name, "formula", name, s)
	svcSavePlugins(t, e.root, cfg)
	p724bPin(t, config.AgentDir(e.root, "alpha"), p724bBinding(name, s, true))
	return s
}

func TestPR724_T8_PinnedFormulaServiceStartedOnce(t *testing.T) {
	e := p724bPinSetup(t)
	s := p724bPinnedFormulaService(t, e, "svcformula")

	p724bEnsure(e.root)

	started := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand ")
	if len(started) != 1 || started[0] != p724bLaunch("svcformula", s) {
		t.Fatalf("a down formula-scope service a present pin names starts exactly once:\n got %v\nwant [%s]", started, p724bLaunch("svcformula", s))
	}
	if _, ok := readServiceState(t, e.root, "svcformula"); !ok {
		t.Errorf("the repair writes the name-keyed relaunch state so grace and backoff apply")
	}
}

func TestPR724_T8_PinnedOptionalFormulaServiceRepaired(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	s := p724bWriteSnapshot(t, e.root, "svcopt", "formula", "svcopt", "v1")
	p724bRecord(cfg, "svcopt", "formula", "svcopt", s)
	svcSavePlugins(t, e.root, cfg)
	p724bPin(t, config.AgentDir(e.root, "alpha"), p724bBinding("svcopt", s, false))

	p724bEnsure(e.root)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand svcopt "); len(got) != 1 {
		t.Fatalf("admission starts optional services too, so a pinned optional one is repaired; ops=%v", e.fake.ops)
	}
}

func TestPR724_T8_InTreeWorktreePinEnumerated(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	s := p724bWriteSnapshot(t, e.root, "svcwt", "formula", "svcwt", "v1")
	p724bRecord(cfg, "svcwt", "formula", "svcwt", s)
	svcSavePlugins(t, e.root, cfg)
	p724bPin(t, config.AgentDir(filepath.Join(worktree.WorktreesDir(e.root), "wt-pr724"), "beta"), p724bBinding("svcwt", s, true))

	p724bEnsure(e.root)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand svcwt "); len(got) != 1 {
		t.Fatalf("a pin under an in-tree worktree agent dir names the service too; ops=%v", e.fake.ops)
	}
}

func TestPR724_T8_RelocatedWorktreePinEnumerated(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	s := p724bWriteSnapshot(t, e.root, "svcreloc", "formula", "svcreloc", "v1")
	p724bRecord(cfg, "svcreloc", "formula", "svcreloc", s)
	svcSavePlugins(t, e.root, cfg)
	wt := filepath.Join(t.TempDir(), "relocated", "wt-reloc")
	if rel, err := filepath.Rel(e.root, wt); err != nil || !strings.HasPrefix(rel, "..") {
		t.Fatalf("fixture: relocated worktree %s must sit outside the factory root %s", wt, e.root)
	}
	if err := worktree.WriteMeta(e.root, &worktree.Meta{ID: "wt-reloc", Owner: "owner", Branch: "af/owner-wt-reloc", Path: wt, Agents: []string{"owner"}}); err != nil {
		t.Fatal(err)
	}
	p724bPin(t, config.AgentDir(wt, "owner"), p724bBinding("svcreloc", s, true))

	p724bEnsure(e.root)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand svcreloc "); len(got) != 1 {
		t.Fatalf("a relocated worktree's pin, reached through its metadata, names the service too; ops=%v", e.fake.ops)
	}
}

func TestPR724_T8_ReinstalledRecordSnapshotStartedNotPinnedOne(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	old := p724bWriteSnapshot(t, e.root, "svcre", "formula", "svcre", "v1")
	cur := p724bWriteSnapshot(t, e.root, "svcre", "formula", "svcre", "v2")
	p724bRecord(cfg, "svcre", "formula", "svcre", cur)
	svcSavePlugins(t, e.root, cfg)
	p724bPin(t, config.AgentDir(e.root, "alpha"), p724bBinding("svcre", old, true))

	p724bEnsure(e.root)

	started := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand ")
	if len(started) != 1 || started[0] != p724bLaunch("svcre", cur) {
		t.Fatalf("one service session per name starts from the snapshot plugins.json records now, not the pin's:\n got %v\nwant [%s]", started, p724bLaunch("svcre", cur))
	}
}

func TestPR724_T8_RespawnSiteRepairsPinnedService(t *testing.T) {
	e := p724bPinSetup(t)
	s := p724bPinnedFormulaService(t, e, "svcsite")
	var w bytes.Buffer

	ensureFactoryIntegrationServices(context.Background(), nil, e.root, &w)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand "); len(got) != 1 || got[0] != p724bLaunch("svcsite", s) {
		t.Fatalf("respawn repairs the pinned formula-scope service; ops=%v stderr=%q", e.fake.ops, w.String())
	}
}

func TestPR724_T8_SlingSiteRepairsPinnedService(t *testing.T) {
	e := p724bPinSetup(t)
	s := p724bPinnedFormulaService(t, e, "svcsite")
	cmd := &cobra.Command{}
	var w bytes.Buffer

	ensureFactoryIntegrationServices(cmd.Context(), cmd, e.root, &w)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand "); len(got) != 1 || got[0] != p724bLaunch("svcsite", s) {
		t.Fatalf("sling's launch repairs the pinned formula-scope service; ops=%v stderr=%q", e.fake.ops, w.String())
	}
}

func TestPR724_T8_WatchdogSiteRepairsPinnedService(t *testing.T) {
	e := p724bPinSetup(t)
	s := p724bPinnedFormulaService(t, e, "svcsite")
	cmd := &cobra.Command{}
	var w bytes.Buffer
	cmd.SetErr(&w)

	triggerIntegrationServicesGuard(cmd, e.root)
	deadline := time.Now().Add(10 * time.Second)
	for !integrationServicesGuardInFlight.CompareAndSwap(false, true) {
		if time.Now().After(deadline) {
			t.Fatal("the watchdog trigger did not finish")
		}
		time.Sleep(5 * time.Millisecond)
	}
	integrationServicesGuardInFlight.Store(false)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand "); len(got) != 1 || got[0] != p724bLaunch("svcsite", s) {
		t.Fatalf("the watchdog tick repairs the pinned formula-scope service; ops=%v stderr=%q", e.fake.ops, w.String())
	}
}

func TestPR724_T8_PinListerErrorReportedFactoryPassContinues(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	fac := p724bWriteSnapshot(t, e.root, "svcfac6", "factory", "svcfac6", "v1")
	p724bRecord(cfg, "svcfac6", "factory", "svcfac6", fac)
	for _, name := range []string{"svcform6a", "svcform6b"} {
		p724bRecord(cfg, name, "formula", name, p724bWriteSnapshot(t, e.root, name, "formula", name, "v1"))
	}
	svcSavePlugins(t, e.root, cfg)
	if err := os.MkdirAll(worktree.WorktreesDir(e.root), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktree.WorktreesDir(e.root), "wt-z.meta.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reports := p724bEnsure(e.root)

	const prefix = "pinned formula-scope integration services not ensured: "
	n := 0
	for _, r := range reports {
		if strings.HasPrefix(r, prefix) {
			n++
		}
	}
	if n != 1 {
		t.Errorf("an unreadable worktree meta is reported once per ensure with %q; reports = %q", prefix, reports)
	}
	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand "); len(got) != 1 || got[0] != p724bLaunch("svcfac6", fac) {
		t.Errorf("a pin lister error never stops the factory-scope pass; ops=%v", e.fake.ops)
	}
}

func TestPR724_T8_KeepUnpinnedFormulaServiceOnDemand(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	pinned := p724bWriteSnapshot(t, e.root, "svcpinned", "formula", "svcpinned", "v1")
	p724bRecord(cfg, "svcpinned", "formula", "svcpinned", pinned)
	p724bRecord(cfg, "svcloose", "formula", "svcloose", p724bWriteSnapshot(t, e.root, "svcloose", "formula", "svcloose", "v1"))
	svcSavePlugins(t, e.root, cfg)
	p724bPin(t, config.AgentDir(e.root, "alpha"), p724bBinding("svcpinned", pinned, true))

	p724bEnsure(e.root)

	for _, op := range e.fake.ops {
		if strings.Contains(op, "svcloose") {
			t.Errorf("a formula-scope service no pin names is never probed or started: %q", op)
		}
	}
	if _, ok := readServiceState(t, e.root, "svcloose"); ok {
		t.Errorf("an unpinned formula-scope service gets no relaunch state")
	}
}

func TestPR724_T8_KeepPinWithoutServiceInert(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	s := p724bWriteSnapshot(t, e.root, "nosvc", "formula", "", "v1")
	p724bRecord(cfg, "nosvc", "formula", "", s)
	svcSavePlugins(t, e.root, cfg)
	p724bPin(t, config.AgentDir(e.root, "alpha"), p724bBinding("nosvc", s, true))

	if reports := p724bEnsure(e.root); len(reports) != 0 {
		t.Errorf("a pin naming no [service] produces no reports; got %q", reports)
	}
	if len(e.fake.ops) != 0 {
		t.Errorf("a pin naming no [service] causes zero tmux ops; got %v", e.fake.ops)
	}
	if _, err := os.Stat(filepath.Join(e.root, ".runtime", "integration_service")); !os.IsNotExist(err) {
		t.Errorf("a pin naming no [service] writes no service state (stat err %v)", err)
	}
	if len(*e.mails) != 0 {
		t.Errorf("no mail expected; sent %v", *e.mails)
	}
}

func TestPR724_T8_KeepPinnedFactoryServiceEnsuredOnce(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	s := p724bWriteSnapshot(t, e.root, "svcfac", "factory", "svcfac", "v1")
	p724bRecord(cfg, "svcfac", "factory", "svcfac", s)
	svcSavePlugins(t, e.root, cfg)
	p724bPin(t, config.AgentDir(e.root, "alpha"), p724bBinding("svcfac", s, true))
	p724bPin(t, config.AgentDir(e.root, "gamma"), p724bBinding("svcfac", s, true))

	reports := p724bEnsure(e.root)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand svcfac "); len(got) != 1 {
		t.Errorf("a factory-scope service is ensured exactly once, pinned or not; ops=%v", e.fake.ops)
	}
	if got := svcOpsWithPrefix(e.fake.ops, "HasSession svcfac"); len(got) != 1 {
		t.Errorf("a factory-scope service is probed once per ensure, not once per pin; ops=%v", e.fake.ops)
	}
	if len(reports) != 0 {
		t.Errorf("no duplicate reports expected; got %q", reports)
	}
}

func TestPR724_T8_KeepTwoPinsOneFormulaServiceLaunchedOnce(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	s := p724bWriteSnapshot(t, e.root, "svcshared", "formula", "svcshared", "v1")
	p724bRecord(cfg, "svcshared", "formula", "svcshared", s)
	svcSavePlugins(t, e.root, cfg)
	p724bPin(t, config.AgentDir(e.root, "alpha"), p724bBinding("svcshared", s, true))
	p724bPin(t, config.AgentDir(e.root, "gamma"), p724bBinding("svcshared", s, true))

	p724bEnsure(e.root)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand svcshared "); len(got) > 1 {
		t.Errorf("two pins naming one service never launch it twice in one ensure; ops=%v", e.fake.ops)
	}
}

func TestPR724_T8_KeepDriftedPinnedSnapshotNeverLaunched(t *testing.T) {
	e := p724bPinSetup(t)
	s := p724bPinnedFormulaService(t, e, "svcdrift")
	if err := os.WriteFile(filepath.Join(s.abs, "bin", "serve.sh"), []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
		t.Fatal(err)
	}

	p724bEnsure(e.root)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand svcdrift "); len(got) != 0 {
		t.Errorf("a pinned service whose snapshot no longer hashes to consent is never launched; ops=%v", e.fake.ops)
	}
}

func TestPR724_T8_KeepLivePinnedServiceNeverKilled(t *testing.T) {
	e := p724bPinSetup(t)
	e.fake.present["svclive"] = true
	p724bPinnedFormulaService(t, e, "svclive")

	p724bEnsure(e.root)

	for _, op := range e.fake.ops {
		if strings.HasPrefix(op, "KillSession") || strings.HasPrefix(op, "NewSessionWithCommand") {
			t.Errorf("a live pinned service is neither killed nor relaunched: %q", op)
		}
	}
}

func TestPR724_T8_KeepUnreadablePinDoesNotStopFactoryPass(t *testing.T) {
	e := p724bPinSetup(t)
	cfg := &config.PluginsConfig{}
	fac := p724bWriteSnapshot(t, e.root, "svcfac2", "factory", "svcfac2", "v1")
	p724bRecord(cfg, "svcfac2", "factory", "svcfac2", fac)
	p724bRecord(cfg, "svcform2", "formula", "svcform2", p724bWriteSnapshot(t, e.root, "svcform2", "formula", "svcform2", "v1"))
	svcSavePlugins(t, e.root, cfg)
	p := integrationPinPath(config.AgentDir(e.root, "alpha"))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	reports := p724bEnsure(e.root)

	if got := svcOpsWithPrefix(e.fake.ops, "NewSessionWithCommand "); len(got) != 1 || got[0] != p724bLaunch("svcfac2", fac) {
		t.Errorf("an unreadable pin neither stops the factory-scope pass nor names a service; ops=%v", e.fake.ops)
	}
	if len(reports) != 0 {
		t.Errorf("an unreadable pin is skipped without a report; got %q", reports)
	}
}

func TestPR724_T8_KeepRemovedRecordPinnedSnapshotNotRelaunched(t *testing.T) {
	e := p724bPinSetup(t)
	s := p724bWriteSnapshot(t, e.root, "svcgone", "formula", "svcgone", "v1")
	svcSavePlugins(t, e.root, &config.PluginsConfig{Plugins: map[string]config.PluginEntry{}})
	p724bPin(t, config.AgentDir(e.root, "alpha"), p724bBinding("svcgone", s, true))

	reports := p724bEnsure(e.root)

	if len(e.fake.ops) != 0 {
		t.Errorf("af plugin remove told the operator to stop the service; a pin of a removed record relaunches nothing; ops=%v", e.fake.ops)
	}
	if _, ok := readServiceState(t, e.root, "svcgone"); ok {
		t.Errorf("a removed record gets no relaunch state")
	}
	if len(reports) != 0 {
		t.Errorf("a pinned-but-removed integration is not reported on every ensure; got %q", reports)
	}
}
