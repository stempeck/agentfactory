package config

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// v2WritePlugins writes raw bytes to a fresh factory's plugins.json and returns its path.
func v2WritePlugins(t *testing.T, raw string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	path := PluginsConfigPath(dir)
	if err := os.WriteFile(path, []byte(raw), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// v2IntegrationFixture is a fully populated integration block: every design K3 field carries a
// non-zero value, so a field the loader or the saver drops shows up as a diff.
func v2IntegrationFixture() *PluginIntegration {
	return &PluginIntegration{
		ManifestSHA256:      strings.Repeat("a", 64),
		ContentSHA256:       strings.Repeat("b", 64),
		Files:               map[string]string{"af-integration.toml": strings.Repeat("c", 64), "claude-plugin/.claude-plugin/plugin.json": strings.Repeat("d", 64)},
		Scope:               "factory",
		FactoryWide:         true,
		UpstreamRepo:        "https://github.com/acme/upstream",
		UpstreamCommit:      strings.Repeat("e", 40),
		ClaudePlugins:       []string{"acme-guard"},
		EnvKeys:             []string{"ACME_TOKEN"},
		Service:             "acme-svc",
		ServiceProbe:        "http-healthz",
		HookFailMode:        "open",
		ExternalWrites:      []string{"~/.local/bin/acme"},
		ExternalWriteHashes: map[string]string{"~/.local/bin/acme": strings.Repeat("f", 64)},
		Artifacts:           []IntegrationArtifact{{URL: "https://example.invalid/acme", SHA256: strings.Repeat("0", 64)}},
		Source:              "clone",
		SnapshotDir:         ".agentfactory/store/integrations/acme/" + strings.Repeat("b", 64),
		StagedAt:            "2026-09-28T00:00:00Z",
	}
}

// TestLoadPluginsConfig_AcceptsV1AndV2 pins the record-v2 version policy (spec L351-352, L402;
// design S3 L373): the loader accepts 1 and 2, a versionless file still reads as version 1 (D2),
// and a newer version is refused with the existing error shape naming both versions.
func TestLoadPluginsConfig_AcceptsV1AndV2(t *testing.T) {
	t.Run("v1_file_loads", func(t *testing.T) {
		path := v2WritePlugins(t, `{"version":1,"plugins":{"acme":{"source":"https://github.com/acme/p.git","formulas":{"acme-triage":{"sha256":"x"}}}}}`)
		cfg, err := LoadPluginsConfig(path)
		if err != nil {
			t.Fatalf("a version-1 plugins.json must keep loading after the v2 bump: %v", err)
		}
		if cfg.Version != 1 {
			t.Errorf("v1 file loaded with Version=%d, want 1", cfg.Version)
		}
		e, ok := cfg.Plugins["acme"]
		if !ok || e.Formulas["acme-triage"].SHA256 != "x" {
			t.Errorf("v1 formula entry not loaded intact: %+v", cfg.Plugins)
		}
		if e.Integration != nil {
			t.Errorf("v1 formula entry grew an integration block: %+v", e.Integration)
		}
	})

	t.Run("versionless_file_reads_as_1", func(t *testing.T) {
		// D2: plugins.go's doc comment and ADR-025 §2 say a versionless file reads as version 1.
		// After the bump the default must not silently become CurrentPluginsVersion.
		path := v2WritePlugins(t, `{"plugins":{"acme":{"formulas":{"acme-triage":{"sha256":"x"}}}}}`)
		cfg, err := LoadPluginsConfig(path)
		if err != nil {
			t.Fatalf("a versionless plugins.json must load: %v", err)
		}
		if cfg.Version != 1 {
			t.Errorf("versionless plugins.json loaded with Version=%d, want 1 (D2: versionless reads as version 1)", cfg.Version)
		}
	})

	t.Run("absent_file_is_current_version", func(t *testing.T) {
		cfg, err := LoadPluginsConfig(filepath.Join(t.TempDir(), "plugins.json"))
		if err != nil {
			t.Fatalf("absent plugins.json must load as empty: %v", err)
		}
		if cfg.Version != CurrentPluginsVersion {
			t.Errorf("absent plugins.json Version=%d, want CurrentPluginsVersion=%d", cfg.Version, CurrentPluginsVersion)
		}
	})

	t.Run("v2_file_with_integration_loads", func(t *testing.T) {
		raw, err := json.Marshal(map[string]any{
			"version": 2,
			"plugins": map[string]any{"acme": PluginEntry{Integration: v2IntegrationFixture()}},
		})
		if err != nil {
			t.Fatal(err)
		}
		path := v2WritePlugins(t, string(raw))
		cfg, err := LoadPluginsConfig(path)
		if err != nil {
			t.Fatalf("a version-2 plugins.json with an integration block must load: %v", err)
		}
		if cfg.Version != 2 {
			t.Errorf("v2 file loaded with Version=%d, want 2", cfg.Version)
		}
		got := cfg.Plugins["acme"].Integration
		if got == nil {
			t.Fatalf("v2 integration block not decoded: %+v", cfg.Plugins["acme"])
		}
		if want := v2IntegrationFixture(); !reflect.DeepEqual(got, want) {
			t.Errorf("v2 integration block decoded lossily:\n got %+v\nwant %+v", got, want)
		}
	})

	t.Run("v2_integration_block_round_trips_byte_equal", func(t *testing.T) {
		path := v2WritePlugins(t, "")
		in := &PluginsConfig{Plugins: map[string]PluginEntry{"acme": {Integration: v2IntegrationFixture()}}}
		if err := SavePluginsConfig(path, in); err != nil {
			t.Fatalf("SavePluginsConfig: %v", err)
		}
		first, _ := os.ReadFile(path)
		cfg, err := LoadPluginsConfig(path)
		if err != nil {
			t.Fatalf("reload of a saved v2 record failed: %v\n%s", err, first)
		}
		if err := SavePluginsConfig(path, cfg); err != nil {
			t.Fatalf("re-save: %v", err)
		}
		second, _ := os.ReadFile(path)
		if !bytes.Equal(first, second) {
			t.Errorf("Save→Load→Save changed plugins.json bytes:\nfirst:\n%s\nsecond:\n%s", first, second)
		}
	})

	t.Run("v2_integration_null_loads", func(t *testing.T) {
		path := v2WritePlugins(t, `{"version":2,"plugins":{"acme":{"integration":null,"formulas":{"acme-triage":{"sha256":"x"}}}}}`)
		cfg, err := LoadPluginsConfig(path)
		if err != nil {
			t.Fatalf(`a v2 entry with "integration":null must load: %v`, err)
		}
		if cfg.Plugins["acme"].Integration != nil {
			t.Errorf(`"integration":null decoded to a non-nil block: %+v`, cfg.Plugins["acme"].Integration)
		}
	})

	t.Run("v2_integration_non_object_refused_for_shape", func(t *testing.T) {
		// The Go half of the Go↔jq mirror (spec L382-383, H3-6): "integration":7 fails on its
		// shape, not on the version gate.
		path := v2WritePlugins(t, `{"version":2,"plugins":{"acme":{"integration":7}}}`)
		cfg, err := LoadPluginsConfig(path)
		if err == nil {
			t.Fatalf(`"integration":7 must be refused; loaded %+v`, cfg)
		}
		if strings.Contains(err.Error(), "newer than supported") {
			t.Errorf(`"integration":7 in a v2 file was refused by the version gate, not its shape: %v`, err)
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("shape error must name the file %s: %v", path, err)
		}
	})

	t.Run("v3_refused_naming_both_versions", func(t *testing.T) {
		path := v2WritePlugins(t, `{"version":3,"plugins":{}}`)
		cfg, err := LoadPluginsConfig(path)
		if err == nil {
			t.Fatalf("a version-3 plugins.json must fail closed; loaded %+v", cfg)
		}
		want := "plugins config " + path + " has schema version 3, newer than supported version 2: upgrade af"
		if err.Error() != want {
			t.Errorf("newer-version error changed shape:\n got %q\nwant %q", err.Error(), want)
		}
	})

	t.Run("v0_still_refused", func(t *testing.T) {
		path := v2WritePlugins(t, `{"version":0,"plugins":{}}`)
		if _, err := LoadPluginsConfig(path); err == nil || !strings.Contains(err.Error(), "version must be >= 1") {
			t.Errorf("version 0 must stay refused with the >= 1 text; err=%v", err)
		}
	})
}

// TestPluginsConfig_V2RoundTripPreservesFormulaFields is the DO-NOT-CHANGE guard for formula
// records (concern_tests §2): under v2, a formula-only entry serializes exactly as it did under v1
// (same keys, same order, no integration key), and only the top-level stamp moves to 2.
func TestPluginsConfig_V2RoundTripPreservesFormulaFields(t *testing.T) {
	formulaOnly := &PluginsConfig{Plugins: map[string]PluginEntry{
		"acme": {
			Source:      "https://github.com/acme/plugin.git",
			Commit:      "abc123",
			InstalledAt: "2026-01-01T00:00:00Z",
			Formulas:    map[string]PluginFormula{"acme-triage": {SHA256: "x"}},
		},
	}}
	path, raw := g3SavePlugins(t, formulaOnly)
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("saved plugins.json is not a JSON object: %v\n%s", err, raw)
	}

	t.Run("stamps_version_2", func(t *testing.T) {
		if v := strings.TrimSpace(string(top["version"])); v != "2" {
			t.Errorf(`saved plugins.json "version" = %s, want 2 (spec L351):\n%s`, v, raw)
		}
	})

	t.Run("formula_entry_bytes_unchanged", func(t *testing.T) {
		var compact bytes.Buffer
		if err := json.Compact(&compact, top["plugins"]); err != nil {
			t.Fatalf("compact plugins object: %v", err)
		}
		want := `{"acme":{"source":"https://github.com/acme/plugin.git","commit":"abc123","installed_at":"2026-01-01T00:00:00Z","formulas":{"acme-triage":{"sha256":"x"}}}}`
		if compact.String() != want {
			t.Errorf("formula-only entry serialization changed:\n got %s\nwant %s", compact.String(), want)
		}
	})

	t.Run("reload_equal", func(t *testing.T) {
		cfg, err := LoadPluginsConfig(path)
		if err != nil {
			t.Fatalf("reload saved formula-only record: %v", err)
		}
		if !reflect.DeepEqual(cfg.Plugins, formulaOnly.Plugins) {
			t.Errorf("formula-only record changed across Save→Load:\n got %+v\nwant %+v", cfg.Plugins, formulaOnly.Plugins)
		}
	})

	t.Run("integration_block_key_set", func(t *testing.T) {
		// Design K3 (L244) / spec L353-360: the record's key set, and no Claude Code version (G15).
		_, raw := g3SavePlugins(t, &PluginsConfig{Plugins: map[string]PluginEntry{"acme": {Integration: v2IntegrationFixture()}}})
		var doc struct {
			Plugins map[string]map[string]json.RawMessage `json:"plugins"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("decode saved record: %v\n%s", err, raw)
		}
		var block map[string]json.RawMessage
		if err := json.Unmarshal(doc.Plugins["acme"]["integration"], &block); err != nil {
			t.Fatalf("integration block is not an object: %v\n%s", err, raw)
		}
		var got []string
		for k := range block {
			got = append(got, k)
		}
		sort.Strings(got)
		want := []string{"artifacts", "claude_plugins", "content_sha256", "env_keys", "external_write_hashes", "external_writes", "factory_wide", "files", "hook_fail_mode", "manifest_sha256", "scope", "service", "service_probe", "snapshot_dir", "source", "staged_at", "upstream_commit", "upstream_repo"}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("integration block keys:\n got %v\nwant %v", got, want)
		}
		for _, k := range got {
			if strings.Contains(k, "claude_code_version") {
				t.Errorf("the Claude Code version belongs in the check record, not plugins.json (spec L360): key %q", k)
			}
		}
	})
}
