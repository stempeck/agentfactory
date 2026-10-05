package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// claudeUserScopePlugin is one enabledPlugins entry of Claude Code's user-scope settings.json.
type claudeUserScopePlugin struct {
	Key     string // as written: name@marketplace
	Name    string // the part of Key before "@"; a key with no "@" is the whole name
	Enabled bool
}

// claudeUserScope is what af reads from Claude Code's user-scope settings.json (spec L553-555,
// L794-796). Plugins is sorted by Key.
type claudeUserScope struct {
	Path     string
	Plugins  []claudeUserScopePlugin
	HasHooks bool
}

// readClaudeUserScope is the one reader of claudeConfigDir()/settings.json, shared by install's
// plugin-name collision check (every key) and the user-scope detector (enabled keys only), D17.
// An absent file reads as empty. A file that is present but not the expected shape is an error,
// and the caller decides: install refuses, the detector warns.
func readClaudeUserScope() (claudeUserScope, error) {
	dir := claudeConfigDir()
	if dir == "" {
		return claudeUserScope{}, fmt.Errorf("locating Claude Code's config dir: set %s or HOME", claudeConfigDirEnv)
	}
	scope := claudeUserScope{Path: filepath.Join(dir, "settings.json")}
	data, err := os.ReadFile(scope.Path)
	if errors.Is(err, os.ErrNotExist) {
		return scope, nil
	}
	if err != nil {
		return claudeUserScope{}, fmt.Errorf("reading Claude Code user settings %s: %w", scope.Path, err)
	}
	var settings struct {
		EnabledPlugins map[string]json.RawMessage `json:"enabledPlugins"`
		Hooks          map[string]json.RawMessage `json:"hooks"`
	}
	if err := json.Unmarshal(data, &settings); err != nil {
		return claudeUserScope{}, fmt.Errorf("parsing Claude Code user settings %s: %w", scope.Path, err)
	}
	for key, raw := range settings.EnabledPlugins {
		var enabled bool
		if err := json.Unmarshal(raw, &enabled); err != nil {
			return claudeUserScope{}, fmt.Errorf("parsing Claude Code user settings %s: enabledPlugins %q must be true or false, got %s", scope.Path, key, raw)
		}
		name, _, _ := strings.Cut(key, "@")
		scope.Plugins = append(scope.Plugins, claudeUserScopePlugin{Key: key, Name: name, Enabled: enabled})
	}
	slices.SortFunc(scope.Plugins, func(a, b claudeUserScopePlugin) int { return strings.Compare(a.Key, b.Key) })
	scope.HasHooks = len(settings.Hooks) > 0
	return scope, nil
}
