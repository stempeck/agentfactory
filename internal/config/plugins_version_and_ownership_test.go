package config

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// 6e (BODY-14, D10): with several owners of one stem, OwnsAgent names the sorted-first
// owner every time, never a map-order pick.
func TestPluginsConfigOwnsAgentDeterministic(t *testing.T) {
	owners := []string{"theta", "eta", "zeta", "epsilon", "delta", "gamma", "beta", "alpha"}
	m := &PluginsConfig{Plugins: map[string]PluginEntry{}}
	for _, o := range owners {
		m.Plugins[o] = PluginEntry{Formulas: map[string]PluginFormula{"x": {SHA256: o}}}
	}
	const calls = 200
	seen := map[string]int{}
	for i := 0; i < calls; i++ {
		p, ok := m.OwnsAgent("x")
		if !ok {
			t.Fatalf(`OwnsAgent("x") = %q,false; want an owner`, p)
		}
		seen[p]++
	}
	if len(seen) != 1 || seen["alpha"] != calls {
		var names []string
		for n := range seen {
			names = append(names, n)
		}
		sort.Strings(names)
		t.Errorf("OwnsAgent(\"x\") over %d calls returned %d distinct owners %v (counts %v); want \"alpha\" every time", calls, len(seen), names, seen)
	}
}

func g3SavePlugins(t *testing.T, cfg *PluginsConfig) (string, []byte) {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := PluginsConfigPath(dir)
	if err := SavePluginsConfig(path, cfg); err != nil {
		t.Fatalf("SavePluginsConfig: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, raw
}

func g3TopLevelVersion(t *testing.T, raw []byte) (json.RawMessage, bool) {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("saved plugins.json is not a JSON object: %v\n%s", err, raw)
	}
	v, ok := top["version"]
	return v, ok
}

// 20a (BODY-8, D22): Save stamps "version": 2 (record v2, spec L351, L400-401).
func TestSavePluginsConfig_WritesSchemaVersion(t *testing.T) {
	t.Run("fresh_write", func(t *testing.T) {
		_, raw := g3SavePlugins(t, &PluginsConfig{Plugins: map[string]PluginEntry{
			"acme": {Formulas: map[string]PluginFormula{"acme-triage": {SHA256: "x"}}},
		}})
		v, ok := g3TopLevelVersion(t, raw)
		if !ok || strings.TrimSpace(string(v)) != "2" {
			t.Errorf(`saved plugins.json top-level "version" = %s (present=%v); want 2:\n%s`, v, ok, raw)
		}
	})

	t.Run("legacy_rewrite_stamps_version", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
			t.Fatal(err)
		}
		path := PluginsConfigPath(dir)
		legacy := `{"plugins":{"acme":{"formulas":{"acme-triage":{"sha256":"x"}}}}}`
		if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
			t.Fatal(err)
		}
		cfg, err := LoadPluginsConfig(path)
		if err != nil {
			t.Fatalf("load legacy: %v", err)
		}
		if err := SavePluginsConfig(path, cfg); err != nil {
			t.Fatalf("re-save: %v", err)
		}
		raw, _ := os.ReadFile(path)
		v, ok := g3TopLevelVersion(t, raw)
		if !ok || strings.TrimSpace(string(v)) != "2" {
			t.Errorf(`re-saved legacy plugins.json "version" = %s (present=%v); want 2:\n%s`, v, ok, raw)
		}
	})
}

// D22: a newer schema version fails closed, naming the file and both versions.
func TestLoadPluginsConfig_NewerVersionFailsClosed(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := PluginsConfigPath(dir)
	if err := os.WriteFile(path, []byte(`{"version":7,"plugins":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPluginsConfig(path)
	if err == nil {
		t.Fatalf("a plugins.json with a newer schema version (7) must fail closed; loaded %+v", cfg)
	}
	for _, want := range []string{path, "7", "2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("newer-version error must mention %q: %v", want, err)
		}
	}
	// The bare digit checks above can match digits inside the temp path, so pin the
	// supported version in its own clause as well (spec L352: names both versions).
	if !strings.Contains(err.Error(), "schema version 7, newer than supported version 2") {
		t.Errorf("newer-version error must name file version 7 and supported version 2: %v", err)
	}
}

// 20b (BODY-8, D22 back-compat): every manifest written before the stamp is versionless
// and must keep loading.
func TestLoadPluginsConfig_VersionlessLegacyLoads(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := PluginsConfigPath(dir)
	legacy := `{"plugins":{"acme":{"source":"https://github.com/acme/plugin.git","formulas":{"acme-triage":{"sha256":"x"}}}}}`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPluginsConfig(path)
	if err != nil {
		t.Fatalf("versionless legacy plugins.json must load: %v", err)
	}
	if got := cfg.Plugins["acme"].Formulas["acme-triage"].SHA256; got != "x" {
		t.Errorf("legacy entry not loaded intact: %+v", cfg.Plugins)
	}
	if p, ok := cfg.OwnsAgent("acme-triage"); !ok || p != "acme" {
		t.Errorf(`legacy manifest OwnsAgent("acme-triage") = %q,%v; want "acme",true`, p, ok)
	}
}
