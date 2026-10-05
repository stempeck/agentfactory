package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestSavePluginsConfig_RoundTrip proves plugins.json survives a save→load cycle
// with full fidelity — every field (provenance + nested per-formula sha256) is
// populated and compared via reflect.DeepEqual — and that the save leaves no *.tmp
// scratch file behind. (The atomic-vs-fixed-`.tmp` idiom itself is pinned by AC-5's
// grep over plugins.go; assertNoTempResidue here guards the complementary property
// that no scratch file leaks on the happy path.)
func TestSavePluginsConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	// WriteFileAtomic CreateTemps in the target's parent dir, which must exist.
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &PluginsConfig{Version: CurrentPluginsVersion, Plugins: map[string]PluginEntry{
		"acme-agents": {
			Source:      "https://github.com/acme/agentfactory-plugin-agents",
			Commit:      "abc1234",
			InstalledAt: "2026-07-12T21:00:00Z",
			Formulas: map[string]PluginFormula{
				"acme-triage": {SHA256: "deadbeef"},
				"acme-review": {SHA256: "cafef00d"},
			},
		},
	}}

	if err := SavePluginsConfig(PluginsConfigPath(dir), cfg); err != nil {
		t.Fatalf("SavePluginsConfig: %v", err)
	}
	assertNoTempResidue(t, afDir)

	loaded, err := LoadPluginsConfig(PluginsConfigPath(dir))
	if err != nil {
		t.Fatalf("LoadPluginsConfig after save: %v", err)
	}
	if !reflect.DeepEqual(loaded, cfg) {
		t.Errorf("round-trip mismatch:\n got  = %+v\n want = %+v", loaded, cfg)
	}
}

// TestLoadPluginsConfig_AbsentIsEmpty pins the AC-6 dormancy keystone: an absent
// manifest is "zero plugins", NOT an error (the deliberate divergence from
// LoadAgentConfig's ErrNotFound). The Plugins map must be non-nil so every call
// site can range/index it without a nil-map panic.
func TestLoadPluginsConfig_AbsentIsEmpty(t *testing.T) {
	dir := t.TempDir()

	cfg, err := LoadPluginsConfig(PluginsConfigPath(dir))
	if err != nil {
		t.Fatalf("LoadPluginsConfig on absent file must not error, got: %v", err)
	}
	if cfg == nil {
		t.Fatal("LoadPluginsConfig on absent file returned nil config")
	}
	if cfg.Plugins == nil {
		t.Error("LoadPluginsConfig on absent file must return a non-nil Plugins map")
	}
	if len(cfg.Plugins) != 0 {
		t.Errorf("absent file must yield zero plugins, got %d", len(cfg.Plugins))
	}
}
