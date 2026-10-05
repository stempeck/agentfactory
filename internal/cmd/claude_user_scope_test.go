package cmd

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestClaudeUserScopeReader pins the shared user-scope settings reader (spec L553-555, L794-796;
// D17). Every row points CLAUDE_CONFIG_DIR at a fixture so the host's real ~/.claude is never read.
func TestClaudeUserScopeReader(t *testing.T) {
	withSettings := func(t *testing.T, content string) string {
		t.Helper()
		dir := t.TempDir()
		t.Setenv(claudeConfigDirEnv, dir)
		if content != "" {
			if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(content), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return filepath.Join(dir, "settings.json")
	}

	t.Run("absent_file_is_empty", func(t *testing.T) {
		path := withSettings(t, "")
		scope, err := readClaudeUserScope()
		if err != nil {
			t.Fatalf("an absent settings.json must read as empty, got %v", err)
		}
		if scope.Path != path || len(scope.Plugins) != 0 || scope.HasHooks {
			t.Errorf("absent settings.json read as %+v, want an empty scope at %s", scope, path)
		}
	})

	t.Run("every_key_with_its_flag_and_name", func(t *testing.T) {
		withSettings(t, `{"enabledPlugins":{"playwright@x":true,"frontend-design@claude-plugins":true,"off@x":false,"bare":true},"theme":"dark"}`)
		scope, err := readClaudeUserScope()
		if err != nil {
			t.Fatal(err)
		}
		want := []claudeUserScopePlugin{
			{Key: "bare", Name: "bare", Enabled: true},
			{Key: "frontend-design@claude-plugins", Name: "frontend-design", Enabled: true},
			{Key: "off@x", Name: "off", Enabled: false},
			{Key: "playwright@x", Name: "playwright", Enabled: true},
		}
		if !reflect.DeepEqual(scope.Plugins, want) {
			t.Errorf("Plugins =\n %+v\nwant (sorted by key, disabled keys kept for install's collision check)\n %+v", scope.Plugins, want)
		}
		if scope.HasHooks {
			t.Error("a settings.json with no hooks reported HasHooks")
		}
	})

	t.Run("hooks_presence", func(t *testing.T) {
		withSettings(t, `{"hooks":{"PreToolUse":[{"matcher":"Bash","hooks":[{"type":"command","command":"x"}]}]}}`)
		scope, err := readClaudeUserScope()
		if err != nil {
			t.Fatal(err)
		}
		if !scope.HasHooks {
			t.Error("a settings.json declaring hooks must report HasHooks")
		}
	})

	for _, tc := range []struct{ name, content string }{
		{"malformed_json", `{"enabledPlugins":{"playwright@x":true`},
		{"enabled_plugins_not_object", `{"enabledPlugins":["playwright@x"]}`},
		{"enabled_value_not_bool", `{"enabledPlugins":{"playwright@x":"yes"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := withSettings(t, tc.content)
			scope, err := readClaudeUserScope()
			if err == nil {
				t.Fatalf("an unusable settings.json must be an error for the caller to act on; read %+v", scope)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("the error must name the file %s: %v", path, err)
			}
		})
	}
}
