package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"slices"
	"strings"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// PluginsConfig holds the contents of .agentfactory/plugins.json — the record of
// which plugin repositories have been installed into this factory and, per plugin,
// which formulas were staged from them (issue #538).
//
// The manifest is LOAD-BEARING, not advisory: orphan-deletion protection and the
// runtime refusal key on the formulas recorded here, and verify checks each one's sha256.
// In a source-repo factory, hand-deleting plugins.json forfeits that protection: on
// the next redeploy the orphan passes will treat both the recorded plugin formulas
// AND their generated role templates as orphans and delete them. Preserve this file
// (it is git-tracked via the root .gitignore negation) rather than treating it as a
// disposable cache.
//
// A present file this package cannot decode (a merge conflict, a truncated write, {}
// or null, a mistyped field, a newer schema version) is corrupt, NOT zero plugins:
// reading it as empty would drop exactly the protection a deleted file forfeits,
// silently. LoadPluginsConfig therefore errors on it, and agent-gen-all.sh applies the
// same shape rule in jq (TestPluginManifestShapeParity) and preserves every orphan
// candidate instead of reaping.
//
// An absent file means zero plugins — every plugin behavior stays dormant and the
// factory is byte-identical to one that never knew about plugins (AC-6). This is the
// deliberate divergence from LoadAgentConfig, which errors when its file is absent.
type PluginsConfig struct {
	Version int                    `json:"version"`
	Plugins map[string]PluginEntry `json:"plugins"`
}

// CurrentPluginsVersion is the plugins.json schema version SavePluginsConfig stamps.
// Manifests written before the stamp carry no version and load as version 1; a newer
// version fails closed because this binary cannot know what the newer fields protect.
const CurrentPluginsVersion = 2

// PluginEntry records one installed plugin. Source/Commit/InstalledAt are provenance
// pointers into git, populated by `af plugin install`.
type PluginEntry struct {
	Source      string                   `json:"source,omitempty"`
	Commit      string                   `json:"commit,omitempty"`
	InstalledAt string                   `json:"installed_at,omitempty"`
	Formulas    map[string]PluginFormula `json:"formulas,omitempty"`
	Integration *PluginIntegration       `json:"integration,omitempty"`
}

// PluginIntegration is the consent record for an af-integration.toml plugin (design K3).
type PluginIntegration struct {
	ManifestSHA256      string                `json:"manifest_sha256"`
	ContentSHA256       string                `json:"content_sha256"`
	Files               map[string]string     `json:"files"`
	Scope               string                `json:"scope"`
	FactoryWide         bool                  `json:"factory_wide"`
	UpstreamRepo        string                `json:"upstream_repo,omitempty"`
	UpstreamCommit      string                `json:"upstream_commit,omitempty"`
	ClaudePlugins       []string              `json:"claude_plugins"`
	EnvKeys             []string              `json:"env_keys"`
	Service             string                `json:"service,omitempty"`
	ServiceProbe        string                `json:"service_probe,omitempty"`
	HookFailMode        string                `json:"hook_fail_mode,omitempty"`
	ExternalWrites      []string              `json:"external_writes"`
	ExternalWriteHashes map[string]string     `json:"external_write_hashes"`
	Artifacts           []IntegrationArtifact `json:"artifacts"`
	Source              string                `json:"source"`
	SnapshotDir         string                `json:"snapshot_dir"`
	StagedAt            string                `json:"staged_at"`
}

// PluginFormula is the per-formula record. The sha256 is the keystone datum that
// later drives the preserve/delete distinction, drift detection, and update diffs.
type PluginFormula struct {
	SHA256 string `json:"sha256,omitempty"`
}

// LoadPluginsConfig reads plugins.json from path. An absent file returns an empty
// config with a non-nil Plugins map and a nil error (absent ⇒ zero plugins ⇒
// dormant), NOT a not-found error. A present file must be a JSON object whose
// "plugins" is an object and whose version is not newer than CurrentPluginsVersion;
// anything else is an error naming path. The map is guaranteed non-nil on success,
// so callers can range or index it without a nil-map panic.
func LoadPluginsConfig(path string) (*PluginsConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &PluginsConfig{Version: CurrentPluginsVersion, Plugins: map[string]PluginEntry{}}, nil
		}
		return nil, fmt.Errorf("reading plugins config %s: %w", path, err)
	}
	var shape struct {
		Version *int            `json:"version"`
		Plugins json.RawMessage `json:"plugins"`
	}
	if err := json.Unmarshal(data, &shape); err != nil {
		return nil, fmt.Errorf("parsing plugins config %s: %w", path, err)
	}
	if !isJSONObject(shape.Plugins) {
		return nil, fmt.Errorf("parsing plugins config %s: \"plugins\" must be a JSON object", path)
	}
	version := 1
	if shape.Version != nil {
		version = *shape.Version
	}
	if version < 1 {
		return nil, fmt.Errorf("parsing plugins config %s: version must be >= 1, got %d", path, version)
	}
	if version > CurrentPluginsVersion {
		return nil, fmt.Errorf("plugins config %s has schema version %d, newer than supported version %d: upgrade af", path, version, CurrentPluginsVersion)
	}
	cfg := PluginsConfig{Version: version}
	if err := json.Unmarshal(shape.Plugins, &cfg.Plugins); err != nil {
		return nil, fmt.Errorf("parsing plugins config %s: %w", path, err)
	}
	return &cfg, nil
}

func isJSONObject(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) > 0 && trimmed[0] == '{'
}

// OwnsAgent reports which installed plugin owns the role identity named agentName,
// if any. Template ownership is DERIVED, not stored: the schema has no Templates
// field (issue #538 peer-review gap #2), and agent name == formula name == template
// stem (formula.go:153). A plugin owns role template <stem> iff it owns formula
// <stem>.formula.toml, so ownership is decided against the Formulas map keys with the
// .formula.toml suffix normalized off BOTH the query and the stored key — a lookup
// stays correct whether K7 recorded keys as "<stem>" or "<stem>.formula.toml".
//
// When several plugins record the stem, the sorted-first plugin name is the owner, so
// the answer (and every refusal message naming it) is stable across calls.
//
// An absent/empty manifest (the dormant default, LoadPluginsConfig on a missing file)
// makes this always return ("", false), so every K14 runtime guard stays dormant with
// zero plugins (AC-6). A nil receiver is treated as empty for the same reason.
func (c *PluginsConfig) OwnsAgent(agentName string) (string, bool) {
	if c == nil {
		return "", false
	}
	want := normalizePluginFormulaKey(agentName)
	for _, pluginName := range slices.Sorted(maps.Keys(c.Plugins)) {
		for formulaKey := range c.Plugins[pluginName].Formulas {
			if normalizePluginFormulaKey(formulaKey) == want {
				return pluginName, true
			}
		}
	}
	return "", false
}

// normalizePluginFormulaKey strips the .formula.toml suffix so an agent-name query
// and a stored formula key compare on the same stem.
func normalizePluginFormulaKey(name string) string {
	return strings.TrimSuffix(name, ".formula.toml")
}

// SavePluginsConfig atomically writes plugins.json to path via fsutil.WriteFileAtomic
// (unique temp + rename), mirroring SaveModelsConfig/SaveDispatchConfig — NOT the
// fixed-temp-name SaveAgentConfig idiom, which is not concurrency-safe. It always
// stamps CurrentPluginsVersion, whatever cfg.Version holds.
func SavePluginsConfig(path string, cfg *PluginsConfig) error {
	stamped := *cfg
	stamped.Version = CurrentPluginsVersion
	data, err := json.MarshalIndent(&stamped, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling plugins config: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(path, data, 0644)
}
