package cmd

// Phase 2 (design 695 K10) pins for the integration service ensure: start-if-absent,
// never kill, a grace window and cronRetryBackoff between relaunches, one mail at the
// cap. Every test swaps package seams (newCmdTmux, integrationNowFn, integrationMailFn),
// so none of them may call t.Parallel.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/tmux"
)

// svcFixture describes one recorded integration that declares a [service].
type svcFixture struct {
	name       string // integration name (plugins.json key, snapshot parent dir)
	session    string // [service] session
	scope      string // "factory" | "formula"
	probe      string // "tmux-session" | "http-healthz"
	healthzURL string // http-healthz only
}

// writeServiceIntegration writes the consumed snapshot
// store/integrations/<name>/<sha>/{af-integration.toml,bin/serve.sh}, plus a decoy
// acquisition copy under store/plugins/<name>/ (services must never run from it), and
// records the integration in cfg. It returns the absolute snapshot dir.
//
// The record carries the snapshot's real content hash and per-file hashes, as an install
// does: the ensure re-hashes the snapshot before it runs anything, so a stand-in hash
// would read as drift and nothing would start.
func writeServiceIntegration(t *testing.T, root string, cfg *config.PluginsConfig, f svcFixture) string {
	t.Helper()
	if f.scope == "" {
		f.scope = "factory"
	}
	if f.probe == "" {
		f.probe = "tmux-session"
	}
	stage := filepath.Join(config.IntegrationsDir(root), f.name, ".stage")
	manifest := fmt.Sprintf("name = %q\ndescription = \"service fixture\"\nscope = %q\n\n[service]\nsession = %q\nrun = \"bin/serve.sh\"\nprobe = %q\n",
		f.name, f.scope, f.session, f.probe)
	if f.healthzURL != "" {
		manifest += fmt.Sprintf("healthz_url = %q\n", f.healthzURL)
	}
	for _, dir := range []string{stage, filepath.Join(config.PluginsDir(root), f.name)} {
		if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, config.IntegrationManifestFile), []byte(manifest), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "bin", "serve.sh"), []byte("#!/bin/sh\nexec sleep 3600\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	m, err := config.LoadIntegrationManifestNamed(stage, f.name)
	if err != nil {
		t.Fatalf("service fixture manifest: %v", err)
	}
	sha, files, err := config.IntegrationContentHash(stage, config.IntegrationDeclaredPaths(m))
	if err != nil {
		t.Fatalf("service fixture content hash: %v", err)
	}
	snap := filepath.Join(config.IntegrationsDir(root), f.name, sha)
	if err := os.Rename(stage, snap); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, snap)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Plugins == nil {
		cfg.Plugins = map[string]config.PluginEntry{}
	}
	cfg.Plugins[f.name] = config.PluginEntry{
		Source:      "https://example.invalid/" + f.name,
		Commit:      strings.Repeat("c", 40),
		InstalledAt: "2026-09-28T00:00:00Z",
		Integration: &config.PluginIntegration{
			ManifestSHA256:      strings.Repeat("b", 64),
			ContentSHA256:       sha,
			Files:               files,
			Scope:               f.scope,
			FactoryWide:         f.scope == "factory",
			ClaudePlugins:       []string{},
			EnvKeys:             []string{},
			Service:             f.session,
			ServiceProbe:        f.probe,
			HookFailMode:        "open",
			ExternalWrites:      []string{},
			ExternalWriteHashes: map[string]string{},
			Artifacts:           []config.IntegrationArtifact{},
			Source:              "clone",
			SnapshotDir:         rel,
			StagedAt:            "2026-09-28T00:00:00Z",
		},
	}
	return snap
}

func svcSavePlugins(t *testing.T, root string, cfg *config.PluginsConfig) {
	t.Helper()
	if err := config.SavePluginsConfig(config.PluginsConfigPath(root), cfg); err != nil {
		t.Fatalf("save plugins.json: %v", err)
	}
}

// serviceTestRoot is a factory root holding only .agentfactory/.
func serviceTestRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root
}

// installServiceFake points newCmdTmux at a recording fake for the test's lifetime.
func installServiceFake(t *testing.T, c cmdTmux) {
	t.Helper()
	orig := newCmdTmux
	newCmdTmux = func() cmdTmux { return c }
	t.Cleanup(func() { newCmdTmux = orig })
}

// installServiceClock pins integrationNowFn to *now (the test advances it).
func installServiceClock(t *testing.T, now *time.Time) {
	t.Helper()
	orig := integrationNowFn
	integrationNowFn = func() time.Time { return *now }
	t.Cleanup(func() { integrationNowFn = orig })
}

type svcSentMail struct{ to, subject, body string }

// installServiceMail records every integrationMailFn call.
func installServiceMail(t *testing.T) *[]svcSentMail {
	t.Helper()
	var sent []svcSentMail
	orig := integrationMailFn
	integrationMailFn = func(to, subject, body string) error {
		sent = append(sent, svcSentMail{to, subject, body})
		return nil
	}
	t.Cleanup(func() { integrationMailFn = orig })
	return &sent
}

func svcOpsWithPrefix(ops []string, prefix string) []string {
	var out []string
	for _, op := range ops {
		if strings.HasPrefix(op, prefix) {
			out = append(out, op)
		}
	}
	return out
}

func writeServiceState(t *testing.T, root, name string, st integrationServiceState) {
	t.Helper()
	p := filepath.Join(root, ".runtime", "integration_service", name+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

// readServiceState returns the decoded state file and whether it exists; it never panics.
func readServiceState(t *testing.T, root, name string) (map[string]any, bool) {
	t.Helper()
	p := filepath.Join(root, ".runtime", "integration_service", name+".json")
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, false
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Errorf("state file %s is not JSON: %v (%q)", p, err, b)
		return nil, true
	}
	return m, true
}

func svcWriteCheckRecord(t *testing.T, root, name, state, output string) {
	t.Helper()
	p := filepath.Join(root, ".runtime", "integration_check", name+".json")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := map[string]any{
		"v": 1, "plugin": name, "state": state, "exit": 1, "at": "2026-09-28T00:00:00Z",
		"duration_ms": 5, "output": output, "hook_fail_mode": "open", "service_rss_kb": 0,
		"claude_code_version": "",
	}
	if state == "ok" {
		rec["exit"] = 0
	}
	b, _ := json.Marshal(rec)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
}

func svcRunEnsure(root string) []string {
	return ensureIntegrationServices(context.Background(), &cobra.Command{}, root, serviceScopeFactory)
}

// TestEnsureIntegrationServices_StartOnceWithinGrace (AC 7): an absent factory-scope
// tmux-session service is probed by its BARE name (HasSession prepends "=" itself, A1),
// started exactly once from the consumed SNAPSHOT with the absolute run path, and not
// started again inside the grace window; a state file written by another process is
// honored the same way.
func TestEnsureIntegrationServices_StartOnceWithinGrace(t *testing.T) {
	root := serviceTestRoot(t)
	cfg := &config.PluginsConfig{}
	snap := writeServiceIntegration(t, root, cfg, svcFixture{name: "svcdemo", session: "svcdemo"})
	svcSavePlugins(t, root, cfg)

	fake := newFakeTmux()
	installServiceFake(t, fake)
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	now := t0
	installServiceClock(t, &now)
	mails := installServiceMail(t)

	svcRunEnsure(root)

	if len(svcOpsWithPrefix(fake.ops, "HasSession svcdemo")) == 0 {
		t.Errorf("the tmux-session probe must call HasSession with the bare session name %q; ops=%v", "svcdemo", fake.ops)
	}
	if bad := svcOpsWithPrefix(fake.ops, "HasSession ="); len(bad) != 0 {
		t.Errorf("HasSession already prepends '=' (tmux.go HasSession); a pre-prefixed name never matches: %v", bad)
	}
	want := fmt.Sprintf("NewSessionWithCommand %s %s %s", "svcdemo", snap, shellQuote(filepath.Join(snap, "bin", "serve.sh")))
	started := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand ")
	if len(started) != 1 || started[0] != want {
		t.Fatalf("an absent service must be started exactly once from the consumed snapshot:\n got %v\nwant [%s]", started, want)
	}
	for _, op := range fake.ops {
		if strings.Contains(op, config.PluginsDir(root)) {
			t.Errorf("services run from the consumed snapshot, never the acquisition dir: %q", op)
		}
	}
	st, ok := readServiceState(t, root, "svcdemo")
	if !ok {
		t.Fatalf("a launch must write .runtime/integration_service/svcdemo.json")
	}
	if at, _ := st["launched_at"].(string); at == "" {
		t.Errorf("state file must carry launched_at; got %v", st)
	} else if parsed, err := time.Parse(time.RFC3339Nano, at); err != nil || !parsed.Equal(t0) {
		t.Errorf("launched_at = %q, want the integrationNowFn clock %s (err %v)", at, t0.Format(time.RFC3339), err)
	}
	if _, has := st["consecutive_relaunches"]; !has {
		t.Errorf("state file must carry consecutive_relaunches; got %v", st)
	}

	// A second pass 30s later is inside the grace window: nothing new starts.
	now = t0.Add(30 * time.Second)
	fake.ops = nil
	svcRunEnsure(root)
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 0 {
		t.Errorf("a second ensure inside the grace window must start nothing; got %v", got)
	}

	// Cross-process: a fresh root whose state file (written by another process) says the
	// service was launched 10s ago starts nothing; once grace and backoff have elapsed it
	// starts exactly once.
	root2 := serviceTestRoot(t)
	cfg2 := &config.PluginsConfig{}
	writeServiceIntegration(t, root2, cfg2, svcFixture{name: "svcdemo", session: "svcdemo"})
	svcSavePlugins(t, root2, cfg2)
	writeServiceState(t, root2, "svcdemo", integrationServiceState{
		LaunchedAt: t0.Add(-10 * time.Second).Format(time.RFC3339), ConsecutiveRelaunches: 1,
	})
	now = t0
	fake.ops = nil
	svcRunEnsure(root2)
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 0 {
		t.Errorf("a state file launched_at inside the grace window must be honored across processes; started %v", got)
	}
	now = t0.Add(10 * time.Minute)
	fake.ops = nil
	svcRunEnsure(root2)
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 1 {
		t.Errorf("past the grace window and cronRetryBackoff(30,1,1h) the absent service must start once; started %v", got)
	}
	if len(*mails) != 0 {
		t.Errorf("no mail below the cap; sent %v", *mails)
	}
}

// TestEnsureIntegrationServices_LiveFailingCheckNotKilled (AC 7): a live service whose
// check record says "fail" is reported INTEGRATION_DEGRADED and is never killed or
// restarted; a live sibling whose check is ok produces no report.
func TestEnsureIntegrationServices_LiveFailingCheckNotKilled(t *testing.T) {
	root := serviceTestRoot(t)
	cfg := &config.PluginsConfig{}
	writeServiceIntegration(t, root, cfg, svcFixture{name: "svcbad", session: "svcbad"})
	writeServiceIntegration(t, root, cfg, svcFixture{name: "svcgood", session: "svcgood"})
	svcSavePlugins(t, root, cfg)
	svcWriteCheckRecord(t, root, "svcbad", "fail", "probe says boom")
	svcWriteCheckRecord(t, root, "svcgood", "ok", "all fine")

	fake := newFakeTmux()
	fake.present["svcbad"] = true
	fake.present["svcgood"] = true
	installServiceFake(t, fake)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	installServiceClock(t, &now)
	installServiceMail(t)

	reports := svcRunEnsure(root)

	if len(svcOpsWithPrefix(fake.ops, "HasSession svcbad")) == 0 {
		t.Errorf("the live service must be probed (HasSession svcbad); ops=%v", fake.ops)
	}
	var degraded []string
	for _, r := range reports {
		if strings.Contains(r, "INTEGRATION_DEGRADED") {
			degraded = append(degraded, r)
		}
	}
	if len(degraded) != 1 || !strings.Contains(degraded[0], "INTEGRATION_DEGRADED svcbad: ") || !strings.Contains(degraded[0], "probe says boom") {
		t.Errorf("want exactly one report \"INTEGRATION_DEGRADED svcbad: <output>\" carrying the check output; reports=%q", reports)
	}
	for _, r := range reports {
		if strings.Contains(r, "svcgood") {
			t.Errorf("a live service with an ok check must not be reported; got %q", r)
		}
	}
	for _, prefix := range []string{"KillSession", "NewSessionWithCommand", "NewSession "} {
		if got := svcOpsWithPrefix(fake.ops, prefix); len(got) != 0 {
			t.Errorf("a live service is never killed or restarted; recorded %v", got)
		}
	}
}

// TestEnsureIntegrationServices_BackoffOneMailAtCap (AC 7, D8): relaunches wait out
// cronRetryBackoff(30, n, 1h) even past the grace window, and reaching the cap (n=8, the
// first n whose backoff is 1h) sends exactly one mail to the supervisor over two calls.
func TestEnsureIntegrationServices_BackoffOneMailAtCap(t *testing.T) {
	root := serviceTestRoot(t)
	cfg := &config.PluginsConfig{}
	writeServiceIntegration(t, root, cfg, svcFixture{name: "svcdemo", session: "svcdemo"})
	svcSavePlugins(t, root, cfg)

	fake := newFakeTmux()
	installServiceFake(t, fake)
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	now := t0
	installServiceClock(t, &now)
	mails := installServiceMail(t)

	// Backoff, not grace: launched 3m ago (grace of 120s elapsed) after 5 relaunches;
	// cronRetryBackoff(30,5,1h) = 8m has not elapsed, so nothing starts.
	writeServiceState(t, root, "svcdemo", integrationServiceState{
		LaunchedAt: t0.Add(-3 * time.Minute).Format(time.RFC3339), ConsecutiveRelaunches: 5,
	})
	svcRunEnsure(root)
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 0 {
		t.Errorf("inside cronRetryBackoff(30,5,1h)=8m the service must not be relaunched; started %v", got)
	}

	// One below the cap, long past its backoff: two relaunches two hours apart.
	writeServiceState(t, root, "svcdemo", integrationServiceState{
		LaunchedAt: t0.Add(-2 * time.Hour).Format(time.RFC3339), ConsecutiveRelaunches: integrationServiceCap - 1,
	})
	fake.ops = nil
	svcRunEnsure(root)
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 1 {
		t.Fatalf("past its backoff the absent service must be relaunched once; started %v", got)
	}
	now = t0.Add(2 * time.Hour)
	fake.ops = nil
	svcRunEnsure(root)
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 1 {
		t.Errorf("at the cap the backoff is 1h, so two hours later the service is relaunched once; started %v", got)
	}

	if len(*mails) != 1 {
		t.Fatalf("reaching the cap must send exactly one mail over two relaunches; sent %d: %v", len(*mails), *mails)
	}
	m := (*mails)[0]
	if m.to != escalationTarget {
		t.Errorf("the at-cap mail goes to %q, got %q", escalationTarget, m.to)
	}
	if !strings.Contains(m.subject+m.body, "svcdemo") {
		t.Errorf("the at-cap mail must name the integration; subject=%q body=%q", m.subject, m.body)
	}
	st, ok := readServiceState(t, root, "svcdemo")
	if !ok {
		t.Fatalf("state file missing after relaunches")
	}
	if mailed, _ := st["mailed_at_cap"].(bool); !mailed {
		t.Errorf("mailed_at_cap must be set once the mail is sent (it keeps every process from mailing again); state=%v", st)
	}

	// Ten minutes after the last relaunch: past grace, inside the 1h capped backoff.
	now = t0.Add(2*time.Hour + 10*time.Minute)
	fake.ops = nil
	svcRunEnsure(root)
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 0 {
		t.Errorf("inside the capped 1h backoff nothing starts; started %v", got)
	}
	if len(*mails) != 1 {
		t.Errorf("no second mail once mailed_at_cap is set; sent %d", len(*mails))
	}
}

// sessionExistsTmux fails NewSessionWithCommand with tmux's duplicate-session refusal
// (another process won the race) while recording the op like fakeTmux.
type sessionExistsTmux struct {
	*fakeTmux
}

func (s sessionExistsTmux) NewSessionWithCommand(name, workDir, command string) error {
	s.fakeTmux.record(fmt.Sprintf("NewSessionWithCommand %s %s %s", name, workDir, command))
	return tmux.ErrSessionExists
}

// TestEnsureIntegrationServices_Edges pins the remaining K10 rows.
func TestEnsureIntegrationServices_Edges(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	t.Run("err_session_exists_is_success_and_counts", func(t *testing.T) {
		// B15: tmux's duplicate-name refusal is the only real cross-process exclusion, so
		// it is success, not a failed start; D25: it still counts as a launch attempt.
		root := serviceTestRoot(t)
		cfg := &config.PluginsConfig{}
		writeServiceIntegration(t, root, cfg, svcFixture{name: "svcdemo", session: "svcdemo"})
		svcSavePlugins(t, root, cfg)
		fake := newFakeTmux()
		installServiceFake(t, sessionExistsTmux{fake})
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		reports := svcRunEnsure(root)

		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 1 {
			t.Fatalf("the absent service must be started once; started %v", got)
		}
		for _, r := range reports {
			l := strings.ToLower(r)
			if strings.Contains(l, "fail") || strings.Contains(l, "error") || strings.Contains(l, "already exists") {
				t.Errorf("ErrSessionExists is success, not a failure report: %q", r)
			}
		}
		st, ok := readServiceState(t, root, "svcdemo")
		if !ok {
			t.Fatalf("ErrSessionExists counts as a launch attempt (D25): the state file must be written")
		}
		if n, _ := st["consecutive_relaunches"].(float64); n != 1 {
			t.Errorf("consecutive_relaunches = %v, want 1 (ErrSessionExists counts, D25)", st["consecutive_relaunches"])
		}
		if at, _ := st["launched_at"].(string); at == "" {
			t.Errorf("launched_at must be stamped on an ErrSessionExists attempt; state=%v", st)
		}
	})

	t.Run("formula_scope_not_ensured", func(t *testing.T) {
		// No pin names svcformula, so it stays on demand: only admission starts it.
		root := serviceTestRoot(t)
		cfg := &config.PluginsConfig{}
		writeServiceIntegration(t, root, cfg, svcFixture{name: "svcfactory", session: "svcfactory", scope: "factory"})
		writeServiceIntegration(t, root, cfg, svcFixture{name: "svcformula", session: "svcformula", scope: "formula"})
		svcSavePlugins(t, root, cfg)
		fake := newFakeTmux()
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		svcRunEnsure(root)

		for _, op := range fake.ops {
			if strings.Contains(op, "svcformula") {
				t.Errorf("a formula-scope service must not be probed or started by the factory-scope ensure: %q", op)
			}
		}
		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand svcfactory "); len(got) != 1 {
			t.Errorf("the factory-scope sibling must be started once; ops=%v", fake.ops)
		}
	})

	t.Run("no_records_zero_tmux_ops", func(t *testing.T) {
		// Protective: every unstubbed caller (blanket runUp tests, tick tests) stays inert
		// when nothing is recorded — absent plugins.json, or formula-only records.
		root := serviceTestRoot(t)
		fake := newFakeTmux()
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		mails := installServiceMail(t)

		if reports := svcRunEnsure(root); len(reports) != 0 {
			t.Errorf("no plugins.json: want no reports, got %q", reports)
		}
		svcSavePlugins(t, root, &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
			"formulas-only": {Source: "https://example.invalid/f", Formulas: map[string]config.PluginFormula{"x.formula.toml": {SHA256: strings.Repeat("d", 64)}}},
		}})
		if reports := svcRunEnsure(root); len(reports) != 0 {
			t.Errorf("formula-only records: want no reports, got %q", reports)
		}
		if len(fake.ops) != 0 {
			t.Errorf("with no integration service recorded the ensure must perform zero tmux ops; got %v", fake.ops)
		}
		if _, err := os.Stat(filepath.Join(root, ".runtime", "integration_service")); !os.IsNotExist(err) {
			t.Errorf("with no integration service recorded the ensure must write nothing (stat err %v)", err)
		}
		if len(*mails) != 0 {
			t.Errorf("no mail expected; sent %v", *mails)
		}
	})

	t.Run("http_healthz_live_vs_down", func(t *testing.T) {
		// Spec L782: http-healthz liveness is a 2s loopback GET (telemetry_backend.go
		// shape); 200 = live (never touched), anything else = absent (started).
		var upHits, downHits int32
		up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&upHits, 1)
			w.WriteHeader(http.StatusOK)
		}))
		defer up.Close()
		down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&downHits, 1)
			w.WriteHeader(http.StatusServiceUnavailable)
		}))
		defer down.Close()

		root := serviceTestRoot(t)
		cfg := &config.PluginsConfig{}
		writeServiceIntegration(t, root, cfg, svcFixture{name: "svcup", session: "svcup", probe: "http-healthz", healthzURL: up.URL + "/healthz"})
		writeServiceIntegration(t, root, cfg, svcFixture{name: "svcdown", session: "svcdown", probe: "http-healthz", healthzURL: down.URL + "/healthz"})
		svcSavePlugins(t, root, cfg)
		fake := newFakeTmux()
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		svcRunEnsure(root)

		if atomic.LoadInt32(&upHits) == 0 || atomic.LoadInt32(&downHits) == 0 {
			t.Errorf("both http-healthz services must be probed over HTTP (up hits %d, down hits %d)", upHits, downHits)
		}
		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand svcup "); len(got) != 0 {
			t.Errorf("a service answering 200 on healthz is live and must not be started: %v", got)
		}
		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand svcdown "); len(got) != 1 {
			t.Errorf("a service whose healthz answers 503 is absent and must be started once; ops=%v", fake.ops)
		}
		if got := svcOpsWithPrefix(fake.ops, "KillSession"); len(got) != 0 {
			t.Errorf("never kill: %v", got)
		}
	})

	t.Run("session_name_is_the_service_session", func(t *testing.T) {
		// The probe and the launch use [service] session, not the integration name.
		root := serviceTestRoot(t)
		cfg := &config.PluginsConfig{}
		snap := writeServiceIntegration(t, root, cfg, svcFixture{name: "svcname", session: "svcsess"})
		svcSavePlugins(t, root, cfg)
		fake := newFakeTmux()
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		svcRunEnsure(root)

		if len(svcOpsWithPrefix(fake.ops, "HasSession svcsess")) == 0 {
			t.Errorf("the probe must use the [service] session %q; ops=%v", "svcsess", fake.ops)
		}
		want := fmt.Sprintf("NewSessionWithCommand svcsess %s %s", snap, shellQuote(filepath.Join(snap, "bin", "serve.sh")))
		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 1 || got[0] != want {
			t.Errorf("launch must name the [service] session:\n got %v\nwant [%s]", got, want)
		}
	})

	t.Run("missing_snapshot_reported_never_launched", func(t *testing.T) {
		// Services run from the consumed snapshot, never the acquisition dir (spec L790):
		// a recorded snapshot that is gone is reported and skipped, even though the
		// acquisition copy is still present.
		root := serviceTestRoot(t)
		cfg := &config.PluginsConfig{}
		snap := writeServiceIntegration(t, root, cfg, svcFixture{name: "svcgone", session: "svcgone"})
		svcSavePlugins(t, root, cfg)
		if err := os.RemoveAll(snap); err != nil {
			t.Fatal(err)
		}
		fake := newFakeTmux()
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		reports := svcRunEnsure(root)

		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 0 {
			t.Errorf("with the consumed snapshot gone nothing may start (never the acquisition dir): %v", got)
		}
		named := false
		for _, r := range reports {
			if strings.Contains(r, "svcgone") {
				named = true
			}
		}
		if !named {
			t.Errorf("a missing snapshot must be reported naming the integration; reports=%q", reports)
		}
	})

	t.Run("live_past_grace_resets_counter", func(t *testing.T) {
		// D8 reset rule: a session seen live past the grace window clears the relaunch
		// count and the mailed bit, so a later outage backs off from the start again.
		root := serviceTestRoot(t)
		cfg := &config.PluginsConfig{}
		writeServiceIntegration(t, root, cfg, svcFixture{name: "svcdemo", session: "svcdemo"})
		svcSavePlugins(t, root, cfg)
		writeServiceState(t, root, "svcdemo", integrationServiceState{
			LaunchedAt: t0.Add(-10 * time.Minute).Format(time.RFC3339), ConsecutiveRelaunches: 5, MailedAtCap: true,
		})
		fake := newFakeTmux()
		fake.present["svcdemo"] = true
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		svcRunEnsure(root)

		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 0 {
			t.Errorf("a live service is never restarted: %v", got)
		}
		st, ok := readServiceState(t, root, "svcdemo")
		if ok {
			n, _ := st["consecutive_relaunches"].(float64)
			mailed, _ := st["mailed_at_cap"].(bool)
			if n != 0 || mailed {
				t.Errorf("seen live past grace: consecutive_relaunches and mailed_at_cap must reset (D8); state=%v", st)
			}
		}
	})

	t.Run("live_inside_grace_keeps_counter", func(t *testing.T) {
		// A session seen live inside the grace window has not yet proven it stays up, so the
		// count and the mailed bit survive; the same state is reset once the window has passed.
		root := serviceTestRoot(t)
		cfg := &config.PluginsConfig{}
		writeServiceIntegration(t, root, cfg, svcFixture{name: "svcdemo", session: "svcdemo"})
		svcSavePlugins(t, root, cfg)
		writeServiceState(t, root, "svcdemo", integrationServiceState{
			LaunchedAt: t0.Add(-30 * time.Second).Format(time.RFC3339), ConsecutiveRelaunches: 5, MailedAtCap: true,
		})
		fake := newFakeTmux()
		fake.present["svcdemo"] = true
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		svcRunEnsure(root)

		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 0 {
			t.Errorf("a live service is never restarted: %v", got)
		}
		st, ok := readServiceState(t, root, "svcdemo")
		if !ok {
			t.Fatal("the service state file must still exist inside the grace window")
		}
		if n, _ := st["consecutive_relaunches"].(float64); n != 5 {
			t.Errorf("seen live inside grace: consecutive_relaunches = %v, want 5 (kept); state=%v", n, st)
		}
		if mailed, _ := st["mailed_at_cap"].(bool); !mailed {
			t.Errorf("seen live inside grace: mailed_at_cap must stay true; state=%v", st)
		}

		now = t0.Add(91 * time.Second)
		svcRunEnsure(root)

		st, ok = readServiceState(t, root, "svcdemo")
		if !ok {
			t.Fatal("the service state file must still exist past the grace window")
		}
		n, _ := st["consecutive_relaunches"].(float64)
		mailed, _ := st["mailed_at_cap"].(bool)
		if n != 0 || mailed {
			t.Errorf("seen live past grace: consecutive_relaunches and mailed_at_cap must reset; state=%v", st)
		}
	})

	t.Run("run_path_with_shell_metachars_is_quoted", func(t *testing.T) {
		// D48: tmux hands the command to sh -c, so the absolute run path must arrive as ONE
		// shell word even when the factory root holds a space, a single quote and a '$'.
		root := filepath.Join(t.TempDir(), "fac tory's $HOME")
		if err := os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755); err != nil {
			t.Fatal(err)
		}
		cfg := &config.PluginsConfig{}
		snap := writeServiceIntegration(t, root, cfg, svcFixture{name: "svcquote", session: "svcquote"})
		svcSavePlugins(t, root, cfg)
		fake := newFakeTmux()
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		svcRunEnsure(root)

		want := fmt.Sprintf("NewSessionWithCommand svcquote %s %s", snap, shellQuote(filepath.Join(snap, "bin", "serve.sh")))
		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand "); len(got) != 1 || got[0] != want {
			t.Errorf("a run path with shell metacharacters must reach tmux as one shellQuote'd word (D48):\n got %v\nwant [%s]", got, want)
		}
	})

	t.Run("nil_ctx_one_service_starts_once_no_panic", func(t *testing.T) {
		// D50: runUp tests (and a bare cobra.Command) hand the ensure a nil ctx; it is treated
		// as context.Background() before the per-service timeout is derived, never a panic.
		root := serviceTestRoot(t)
		cfg := &config.PluginsConfig{}
		writeServiceIntegration(t, root, cfg, svcFixture{name: "svcnilctx", session: "svcnilctx"})
		svcSavePlugins(t, root, cfg)
		fake := newFakeTmux()
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		installServiceMail(t)

		if r := svcEnsureRecover(nil, root); r != nil {
			t.Errorf("ensureIntegrationServices panicked on a nil ctx with one recorded service (D50): %v", r)
		}
		if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand svcnilctx "); len(got) != 1 {
			t.Errorf("with a nil ctx the recorded factory-scope service must still be started exactly once (D50); ops=%v", fake.ops)
		}
	})

	t.Run("nil_ctx_no_records_inert", func(t *testing.T) {
		// D50 early return: nothing recorded + nil ctx = no panic, no tmux op, no write.
		root := serviceTestRoot(t)
		fake := newFakeTmux()
		installServiceFake(t, fake)
		now := t0
		installServiceClock(t, &now)
		mails := installServiceMail(t)

		if r := svcEnsureRecover(nil, root); r != nil {
			t.Errorf("ensureIntegrationServices panicked on a nil ctx with nothing recorded (D50): %v", r)
		}
		if len(fake.ops) != 0 {
			t.Errorf("nil ctx, nothing recorded: the ensure must perform zero tmux ops; got %v", fake.ops)
		}
		if _, err := os.Stat(filepath.Join(root, ".runtime", "integration_service")); !os.IsNotExist(err) {
			t.Errorf("nil ctx, nothing recorded: the ensure must write nothing (stat err %v)", err)
		}
		if len(*mails) != 0 {
			t.Errorf("no mail expected; sent %v", *mails)
		}
	})
}

// svcEnsureRecover runs the factory-scope ensure with ctx (nil allowed) and returns the value
// of any panic instead of letting it abort the package test binary (D50 pins "must not panic").
func svcEnsureRecover(ctx context.Context, root string) (panicked any) {
	defer func() { panicked = recover() }()
	ensureIntegrationServices(ctx, &cobra.Command{}, root, serviceScopeFactory)
	return nil
}

type svcEnsureResult struct {
	reports  []string
	panicked any
}

// svcRunEnsureAsync runs the factory-scope ensure on its own goroutine, converting a panic into a
// result so a misbehaving ensure can never crash the test binary from a goroutine.
func svcRunEnsureAsync(ctx context.Context, root string) <-chan svcEnsureResult {
	out := make(chan svcEnsureResult, 1)
	go func() {
		var res svcEnsureResult
		defer func() {
			res.panicked = recover()
			out <- res
		}()
		res.reports = ensureIntegrationServices(ctx, &cobra.Command{}, root, serviceScopeFactory)
	}()
	return out
}

// blockingSvcTmux is a goroutine-safe cmdTmux double for the per-service bound and
// single-flight pins: HasSession reports every session absent and NewSessionWithCommand
// hangs until release is closed (a wedged tmux server). The ensure must not reach any other
// method; those fall through to the embedded (unsynchronized) fakeTmux.
type blockingSvcTmux struct {
	*fakeTmux
	mu          sync.Mutex
	probes      []string
	launches    []string
	returned    int
	entered     chan struct{}
	release     chan struct{}
	releaseOnce sync.Once
}

// newBlockingSvcTmux builds the double and, on cleanup, releases every hung launch and waits
// (bounded) for it to return, so no launch goroutine outlives the test's temp dirs.
func newBlockingSvcTmux(t *testing.T) *blockingSvcTmux {
	t.Helper()
	b := &blockingSvcTmux{fakeTmux: newFakeTmux(), entered: make(chan struct{}, 16), release: make(chan struct{})}
	t.Cleanup(func() {
		b.releaseAll()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			b.mu.Lock()
			done := b.returned >= len(b.launches)
			b.mu.Unlock()
			if done {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	})
	return b
}

func (b *blockingSvcTmux) releaseAll() { b.releaseOnce.Do(func() { close(b.release) }) }

func (b *blockingSvcTmux) HasSession(name string) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.probes = append(b.probes, name)
	return false, nil
}

func (b *blockingSvcTmux) NewSessionWithCommand(name, workDir, command string) error {
	b.mu.Lock()
	b.launches = append(b.launches, name)
	b.mu.Unlock()
	select {
	case b.entered <- struct{}{}:
	default:
	}
	<-b.release
	b.mu.Lock()
	b.returned++
	b.mu.Unlock()
	return nil
}

func (b *blockingSvcTmux) launchCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.launches)
}

// TestEnsureIntegrationServices_PerServiceStartBound (spec L786 B16, D8, concern_tests §5.11):
// a launch into a wedged tmux never hangs the ensure. The per-service timeout is derived from
// the caller's ctx (D50), so a caller deadline shorter than integrationServiceStartTimeout wins,
// and with no deadline at all the 10s start timeout still bounds it.
func TestEnsureIntegrationServices_PerServiceStartBound(t *testing.T) {
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	t.Run("start_timeout_is_10s_d8", func(t *testing.T) {
		if integrationServiceStartTimeout != 10*time.Second {
			t.Errorf("integrationServiceStartTimeout = %s, want 10s (D8)", integrationServiceStartTimeout)
		}
	})

	for _, tc := range []struct {
		name     string
		svc      string
		deadline time.Duration // 0 = context.Background()
		within   time.Duration
	}{
		{name: "caller_deadline_bounds_blocked_launch", svc: "svcbounda", deadline: 300 * time.Millisecond, within: 5 * time.Second},
		{name: "start_timeout_bounds_blocked_launch_without_deadline", svc: "svcboundb", within: integrationServiceStartTimeout + 5*time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := serviceTestRoot(t)
			cfg := &config.PluginsConfig{}
			writeServiceIntegration(t, root, cfg, svcFixture{name: tc.svc, session: tc.svc})
			svcSavePlugins(t, root, cfg)
			fake := newBlockingSvcTmux(t)
			installServiceFake(t, fake)
			now := t0
			installServiceClock(t, &now)
			installServiceMail(t)

			ctx := context.Background()
			if tc.deadline > 0 {
				var cancel context.CancelFunc
				ctx, cancel = context.WithTimeout(ctx, tc.deadline)
				defer cancel()
			}
			start := time.Now()
			select {
			case res := <-svcRunEnsureAsync(ctx, root):
				if res.panicked != nil {
					t.Fatalf("the ensure panicked on a hung launch: %v", res.panicked)
				}
			case <-time.After(tc.within):
				t.Fatalf("the ensure did not return within %s while NewSessionWithCommand hung: the per-service launch is unbounded (B16)", tc.within)
			}
			elapsed := time.Since(start)
			if got := fake.launchCount(); got != 1 {
				t.Errorf("the absent service's launch must be attempted exactly once (NewSessionWithCommand entered) before the bound cuts it off; entered %d (probes %v)", got, fake.probes)
			}
			t.Logf("ensure returned after %s with the launch hung", elapsed)
		})
	}
}

// TestEnsureIntegrationServices_PerServiceSingleFlight (spec L780, concern_tests §5.11): two
// ensures racing on the same absent service launch it at most once. While the first is inside
// NewSessionWithCommand (no state file written yet), the second must skip the service through the
// per-service CAS, not wait on it and not launch it again.
func TestEnsureIntegrationServices_PerServiceSingleFlight(t *testing.T) {
	root := serviceTestRoot(t)
	cfg := &config.PluginsConfig{}
	writeServiceIntegration(t, root, cfg, svcFixture{name: "svcflight", session: "svcflight"})
	svcSavePlugins(t, root, cfg)
	fake := newBlockingSvcTmux(t)
	installServiceFake(t, fake)
	now := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)
	installServiceClock(t, &now)
	installServiceMail(t)

	first := svcRunEnsureAsync(context.Background(), root)
	select {
	case <-fake.entered:
	case <-time.After(3 * time.Second):
		t.Fatalf("the first ensure must reach the launch of the absent service (NewSessionWithCommand entered); it never did (probes %v)", fake.probes)
	}

	select {
	case res := <-svcRunEnsureAsync(context.Background(), root):
		if res.panicked != nil {
			t.Errorf("the second ensure panicked: %v", res.panicked)
		}
	case <-time.After(3 * time.Second):
		t.Errorf("with the service's launch in flight a second ensure must skip it (per-service CAS), not block on it")
	}
	if got := fake.launchCount(); got != 1 {
		t.Errorf("two concurrent ensures started the same service %d times, want exactly 1 (per-service single-flight)", got)
	}

	fake.releaseAll()
	select {
	case res := <-first:
		if res.panicked != nil {
			t.Errorf("the first ensure panicked: %v", res.panicked)
		}
	case <-time.After(integrationServiceStartTimeout + 5*time.Second):
		t.Errorf("the first ensure never returned after its launch was released")
	}
	if got := fake.launchCount(); got != 1 {
		t.Errorf("after both ensures returned the service was started %d times, want exactly 1", got)
	}
}

// An http-healthz service whose tmux session is running but whose healthz fails is degraded, not
// absent: relaunching it can only hit ErrSessionExists, so counting that toward the cap would end
// in a false "keeps dying" mail (blind review iteration 1, F3; D64).
func TestEnsureIntegrationServices_HealthzUnhealthySessionPresent(t *testing.T) {
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer sick.Close()
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	root := serviceTestRoot(t)
	cfg := &config.PluginsConfig{}
	writeServiceIntegration(t, root, cfg, svcFixture{name: "svcsick", session: "svcsick", probe: "http-healthz", healthzURL: sick.URL + "/healthz"})
	svcSavePlugins(t, root, cfg)
	fake := newFakeTmux()
	fake.present["svcsick"] = true
	installServiceFake(t, fake)
	now := t0
	installServiceClock(t, &now)
	sent := installServiceMail(t)

	for i := 0; i < integrationServiceCap+2; i++ {
		reports := svcRunEnsure(root)
		joined := strings.Join(reports, "\n")
		if !strings.Contains(joined, "INTEGRATION_DEGRADED svcsick") || !strings.Contains(joined, "healthz") {
			t.Fatalf("pass %d: a running session with a failing healthz must report INTEGRATION_DEGRADED naming healthz; got %q", i, reports)
		}
		now = now.Add(2 * time.Hour)
	}
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand svcsick "); len(got) != 0 {
		t.Errorf("a running session must not be relaunched: %v", got)
	}
	if got := svcOpsWithPrefix(fake.ops, "KillSession"); len(got) != 0 {
		t.Errorf("never kill: %v", got)
	}
	if len(*sent) != 0 {
		t.Errorf("a degraded (running) service must not trigger the relaunch-cap mail: %+v", *sent)
	}
	if st, ok := readServiceState(t, root, "svcsick"); ok {
		if n, _ := st["consecutive_relaunches"].(float64); n != 0 {
			t.Errorf("consecutive_relaunches = %v, want 0: no launch was attempted", n)
		}
	}
}

// A freshly launched http-healthz service is still starting, so a failing healthz inside the grace
// window is not a degradation yet; past the window the same failure is reported and mailed once.
func TestEnsureIntegrationServices_HealthzUnhealthyInsideGraceSilent(t *testing.T) {
	sick := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer sick.Close()
	t0 := time.Date(2026, 9, 28, 12, 0, 0, 0, time.UTC)

	root := serviceTestRoot(t)
	cfg := &config.PluginsConfig{}
	writeServiceIntegration(t, root, cfg, svcFixture{name: "svcsick", session: "svcsick", probe: "http-healthz", healthzURL: sick.URL + "/healthz"})
	svcSavePlugins(t, root, cfg)
	writeServiceState(t, root, "svcsick", integrationServiceState{LaunchedAt: t0.Add(-30 * time.Second).Format(time.RFC3339)})
	fake := newFakeTmux()
	fake.present["svcsick"] = true
	installServiceFake(t, fake)
	now := t0
	installServiceClock(t, &now)
	installServiceMail(t)
	recs := installIntegrationReportRecorder(t)

	degraded := func(reports []string) []string {
		var out []string
		for _, r := range reports {
			if strings.HasPrefix(r, "INTEGRATION_DEGRADED svcsick") {
				out = append(out, r)
			}
		}
		return out
	}

	if got := degraded(svcRunEnsure(root)); len(got) != 0 {
		t.Errorf("inside the grace window a failing healthz must not be reported: %q", got)
	}
	if got := reportsWithPrefix(*recs, "INTEGRATION_DEGRADED"); len(got) != 0 {
		t.Errorf("inside the grace window a failing healthz must not be mailed: %+v", got)
	}

	now = t0.Add(91 * time.Second)
	if got := degraded(svcRunEnsure(root)); len(got) != 1 {
		t.Errorf("past the grace window the failing healthz must be reported once; got %q", got)
	}
	if got := reportsWithPrefix(*recs, "INTEGRATION_DEGRADED svcsick"); len(got) != 1 {
		t.Errorf("past the grace window the failing healthz must be mailed once; got %+v", got)
	}
	if got := svcOpsWithPrefix(fake.ops, "NewSessionWithCommand svcsick "); len(got) != 0 {
		t.Errorf("a running session must not be relaunched: %v", got)
	}
}

// The healthz probe targets a loopback URL; following a redirect would let that URL send the probe
// anywhere, and a 200 reached that way is not the service's own answer (blind review F4a).
func TestIntegrationServiceLive_HealthzRedirectNotLive(t *testing.T) {
	var targetHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&targetHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/healthz", http.StatusFound)
	}))
	defer redir.Close()

	live, err := integrationServiceLive(context.Background(), newFakeTmux(), &config.IntegrationService{
		Session: "svcredir", Probe: config.IntegrationProbeHTTPHealthz, HealthzURL: redir.URL + "/healthz",
	})
	if err != nil {
		t.Fatalf("probe error: %v", err)
	}
	if live {
		t.Error("a healthz URL that redirects must not count as live")
	}
	if n := atomic.LoadInt32(&targetHits); n != 0 {
		t.Errorf("the probe followed the redirect (%d hits on the target)", n)
	}
}
