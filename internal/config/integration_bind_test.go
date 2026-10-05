package config

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
)

type rbSpec struct {
	name, scope string
	plugins     []string // [claude] plugins, declared order
	env         map[string]string
}

func rbManifest(s rbSpec) string {
	var b strings.Builder
	b.WriteString("name = \"" + s.name + "\"\ndescription = \"resolver fixture\"\nscope = \"" + s.scope + "\"\n")
	b.WriteString("\n[service]\nsession = \"" + s.name + "-svc\"\nrun = \"af/serve.sh\"\nprobe = \"tmux-session\"\n")
	q := make([]string, len(s.plugins))
	for i, p := range s.plugins {
		q[i] = `"` + p + `"`
	}
	b.WriteString("\n[claude]\nplugins = [" + strings.Join(q, ", ") + "]\n")
	keys := make([]string, 0, len(s.env))
	for k := range s.env {
		keys = append(keys, k)
	}
	// Reverse order in the file, so a resolver that keeps file order is caught.
	sort.Sort(sort.Reverse(sort.StringSlice(keys)))
	b.WriteString("\n[env]\n")
	for _, k := range keys {
		b.WriteString(k + " = \"" + s.env[k] + "\"\n")
	}
	return b.String()
}

func rbWriteTree(t *testing.T, dir string, s rbSpec) {
	t.Helper()
	write := func(rel, body string, mode os.FileMode) {
		p := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), mode); err != nil {
			t.Fatal(err)
		}
	}
	write(IntegrationManifestFile, rbManifest(s), 0o644)
	write("af/serve.sh", "#!/bin/sh\nexec sleep 3600\n", 0o755)
	for _, p := range s.plugins {
		write(p+"/.claude-plugin/plugin.json", `{"name":"`+p+`"}`+"\n", 0o644)
		write(p+"/skills/"+p+"/SKILL.md", "---\nname: "+p+"\n---\n", 0o644)
	}
}

// rbRecord stages a snapshot at <IntegrationsDir>/<name>/<sum>/ exactly as Phase 2's install does and returns its
// plugins.json entry.
func rbRecord(t *testing.T, root string, s rbSpec) PluginEntry {
	t.Helper()
	parent := filepath.Join(IntegrationsDir(root), s.name)
	stage := filepath.Join(parent, ".tmp-stage")
	rbWriteTree(t, stage, s)
	declared := append([]string{"af"}, s.plugins...)
	sum, files, err := IntegrationContentHash(stage, declared)
	if err != nil {
		t.Fatalf("IntegrationContentHash: %v", err)
	}
	snap := filepath.Join(parent, sum)
	if err := os.Rename(stage, snap); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(root, snap)
	if err != nil {
		t.Fatal(err)
	}
	names := slices.Clone(s.plugins)
	sort.Strings(names)
	keys := make([]string, 0, len(s.env))
	for k := range s.env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return PluginEntry{
		Source: "https://example.invalid/" + s.name + ".git",
		Integration: &PluginIntegration{
			ContentSHA256: sum,
			Files:         files,
			Scope:         s.scope,
			FactoryWide:   s.scope == "factory",
			ClaudePlugins: names,
			EnvKeys:       keys,
			Service:       s.name + "-svc",
			HookFailMode:  "open",
			SnapshotDir:   filepath.ToSlash(rel),
		},
	}
}

type rbFixture struct {
	root string
	cfg  *PluginsConfig
}

func rbSetup(t *testing.T) *rbFixture {
	t.Helper()
	root := t.TempDir()
	cfg := &PluginsConfig{Version: CurrentPluginsVersion, Plugins: map[string]PluginEntry{}}
	for _, s := range []rbSpec{
		{"fac-int", "factory", []string{"fac-plugin"}, map[string]string{"FAC_INT_KEY": "f"}},
		{"req-int", "formula", []string{"zeta-plugin", "alpha-plugin"}, map[string]string{"REQ_INT_Z": "z", "REQ_INT_A": "file:.agentfactory/secrets/req"}},
		{"opt-ok", "formula", []string{"opt-plugin"}, map[string]string{"OPT_OK_KEY": "o"}},
		{"other-int", "formula", []string{"other-plugin"}, map[string]string{"OTHER_INT_KEY": "x"}},
	} {
		cfg.Plugins[s.name] = rbRecord(t, root, s)
	}
	return &rbFixture{root: root, cfg: cfg}
}

func (fx *rbFixture) resolve(t *testing.T) ([]IntegrationBinding, []string, []string) {
	t.Helper()
	bound, universe, reports, err := ResolveIntegrationBindings(fx.root, fx.cfg, []string{"req-int"}, []string{"opt-ok", "opt-absent"})
	if err != nil {
		t.Fatalf("ResolveIntegrationBindings: %v", err)
	}
	return bound, universe, reports
}

func rbNames(bound []IntegrationBinding) []string {
	var n []string
	for _, b := range bound {
		n = append(n, b.Name)
	}
	sort.Strings(n)
	return n
}

func rbFind(bound []IntegrationBinding, name string) *IntegrationBinding {
	for i := range bound {
		if bound[i].Name == name {
			return &bound[i]
		}
	}
	return nil
}

func rbReportNaming(reports []string, name, fragment string) bool {
	for _, r := range reports {
		if strings.Contains(r, `"`+name+`"`) && strings.Contains(r, fragment) {
			return true
		}
	}
	return false
}

func TestResolveIntegrationBindings_BoundSetAndDrift(t *testing.T) {
	t.Run("fixture snapshots load and hash as recorded", func(t *testing.T) {
		fx := rbSetup(t)
		for name, e := range fx.cfg.Plugins {
			snap := filepath.Join(fx.root, filepath.FromSlash(e.Integration.SnapshotDir))
			m, err := LoadIntegrationManifestNamed(snap, name)
			if err != nil {
				t.Fatalf("%s: fixture manifest does not load: %v", name, err)
			}
			sum, _, err := IntegrationContentHash(snap, append([]string{"af"}, m.Claude.Plugins...))
			if err != nil || sum != e.Integration.ContentSHA256 {
				t.Errorf("%s: re-hash = %q, %v; want the recorded %q", name, sum, err, e.Integration.ContentSHA256)
			}
		}
	})

	t.Run("bound set is factory scope plus required plus healthy optional", func(t *testing.T) {
		fx := rbSetup(t)
		bound, _, _ := fx.resolve(t)
		if got, want := rbNames(bound), []string{"fac-int", "opt-ok", "req-int"}; !slices.Equal(got, want) {
			t.Errorf("bound = %v, want %v", got, want)
		}
		if b := rbFind(bound, "req-int"); b == nil || !b.Required {
			t.Errorf("req-int binding = %+v, want Required", b)
		}
		if b := rbFind(bound, "opt-ok"); b != nil && b.Required {
			t.Error("opt-ok bound as Required")
		}
	})

	t.Run("plugin dirs absolute in declared order", func(t *testing.T) {
		fx := rbSetup(t)
		bound, _, _ := fx.resolve(t)
		b := rbFind(bound, "req-int")
		if b == nil {
			t.Fatal("req-int not bound")
		}
		snap := filepath.Join(fx.root, filepath.FromSlash(fx.cfg.Plugins["req-int"].Integration.SnapshotDir))
		want := []string{filepath.Join(snap, "zeta-plugin"), filepath.Join(snap, "alpha-plugin")}
		if !slices.Equal(b.PluginDirs, want) {
			t.Errorf("PluginDirs = %v, want %v", b.PluginDirs, want)
		}
		if b.SnapshotDir == "" || b.ContentSHA256 != fx.cfg.Plugins["req-int"].Integration.ContentSHA256 {
			t.Errorf("binding snapshot/sha = %q/%q, want the recorded snapshot and hash", b.SnapshotDir, b.ContentSHA256)
		}
	})

	t.Run("env sorted by key with file refs verbatim", func(t *testing.T) {
		fx := rbSetup(t)
		bound, _, _ := fx.resolve(t)
		b := rbFind(bound, "req-int")
		if b == nil {
			t.Fatal("req-int not bound")
		}
		want := []EnvVar{{"REQ_INT_A", "file:.agentfactory/secrets/req"}, {"REQ_INT_Z", "z"}}
		if !slices.Equal(b.Env, want) {
			t.Errorf("Env = %v, want %v", b.Env, want)
		}
	})

	t.Run("key universe covers every installed integration", func(t *testing.T) {
		fx := rbSetup(t)
		_, universe, _ := fx.resolve(t)
		for _, k := range []string{"FAC_INT_KEY", "REQ_INT_A", "REQ_INT_Z", "OPT_OK_KEY", "OTHER_INT_KEY"} {
			if !slices.Contains(universe, k) {
				t.Errorf("envKeyUniverse = %v, missing %s", universe, k)
			}
		}
	})

	t.Run("absent optional is not bound and is reported", func(t *testing.T) {
		fx := rbSetup(t)
		bound, _, reports := fx.resolve(t)
		if rbFind(bound, "opt-absent") != nil {
			t.Error("opt-absent bound though it is not installed")
		}
		if !rbReportNaming(reports, "opt-absent", "not installed") {
			t.Errorf("reports = %q, want one naming \"opt-absent\" as not installed", reports)
		}
	})

	t.Run("drift drops the binding and reports", func(t *testing.T) {
		fx := rbSetup(t)
		snap := filepath.Join(fx.root, filepath.FromSlash(fx.cfg.Plugins["req-int"].Integration.SnapshotDir))
		if err := os.WriteFile(filepath.Join(snap, "af", "serve.sh"), []byte("#!/bin/sh\necho tampered\n"), 0o755); err != nil {
			t.Fatal(err)
		}
		bound, _, reports := fx.resolve(t)
		if rbFind(bound, "req-int") != nil {
			t.Error("req-int bound though its snapshot content changed since consent")
		}
		if !rbReportNaming(reports, "req-int", "content changed") {
			t.Errorf("reports = %q, want a content-changed report naming \"req-int\"", reports)
		}
		if got := rbNames(bound); !slices.Equal(got, []string{"fac-int", "opt-ok"}) {
			t.Errorf("bound = %v, want the other integrations still bound", got)
		}
	})

	t.Run("snapshot outside IntegrationsDir refused", func(t *testing.T) {
		fx := rbSetup(t)
		inside := filepath.Join(fx.root, filepath.FromSlash(fx.cfg.Plugins["req-int"].Integration.SnapshotDir))
		outside := filepath.Join(PluginsDir(fx.root), "req-int")
		if err := os.MkdirAll(filepath.Dir(outside), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(inside, outside); err != nil {
			t.Fatal(err)
		}
		e := fx.cfg.Plugins["req-int"]
		rel, _ := filepath.Rel(fx.root, outside)
		e.Integration.SnapshotDir = filepath.ToSlash(rel)
		fx.cfg.Plugins["req-int"] = e
		bound, _, reports := fx.resolve(t)
		if rbFind(bound, "req-int") != nil {
			t.Errorf("req-int bound from %s, outside IntegrationsDir", rel)
		}
		if !rbReportNaming(reports, "req-int", "") {
			t.Errorf("reports = %q, want one naming \"req-int\"", reports)
		}
	})

	t.Run("binds with store plugins dir deleted", func(t *testing.T) {
		fx := rbSetup(t)
		src := filepath.Join(PluginsDir(fx.root), "req-int")
		if err := os.MkdirAll(src, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.RemoveAll(PluginsDir(fx.root)); err != nil {
			t.Fatal(err)
		}
		bound, _, _ := fx.resolve(t)
		if rbFind(bound, "req-int") == nil {
			t.Error("req-int not bound with store/plugins deleted: the resolver must read only the consumed snapshot")
		}
	})
}
