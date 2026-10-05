package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// PR #539 T2/T7 (D4): a PRESENT plugins.json must hold an object-valued "plugins";
// every other shape is corrupt, and the error names the file.
func TestLoadPluginsConfig_PresentButUnusableIsError(t *testing.T) {
	rows := map[string]string{
		"conflict_markers":    "<<<<<<< HEAD\n{\"plugins\":{}}\n=======\n{\"plugins\":{}}\n>>>>>>> other\n",
		"zero_byte":           "",
		"truncated":           `{"plugins":{"acme":`,
		"top_level_array":     `[1]`,
		"empty_object_no_key": `{}`,
		"json_null":           `null`,
		"plugins_null":        `{"plugins":null}`,
		"plugins_array":       `{"plugins":[]}`,
		"plugins_string":      `{"plugins":"acme"}`,
	}
	for name, body := range rows {
		t.Run(name, func(t *testing.T) {
			path := PluginsConfigPath(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			cfg, err := LoadPluginsConfig(path)
			if err == nil {
				t.Fatalf("present-but-unusable plugins.json (%q) must be an error, got cfg=%+v", body, cfg)
			}
			if cfg != nil {
				t.Errorf("error return must carry a nil config, got %+v", cfg)
			}
			if !strings.Contains(err.Error(), path) {
				t.Errorf("error must name the file %s; got: %v", path, err)
			}
		})
	}
}

func TestLoadPluginsConfig_DirectoryIsError(t *testing.T) {
	path := PluginsConfigPath(t.TempDir())
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPluginsConfig(path)
	if err == nil {
		t.Fatalf("a directory at the plugins.json path is present, not absent; got cfg=%+v", cfg)
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("error must name the file %s; got: %v", path, err)
	}
}

func TestLoadPluginsConfig_EmptyPluginsObjectIsZeroPlugins(t *testing.T) {
	path := PluginsConfigPath(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"plugins":{}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadPluginsConfig(path)
	if err != nil {
		t.Fatalf(`{"plugins":{}} is a valid zero-plugin manifest, got: %v`, err)
	}
	if cfg == nil || cfg.Plugins == nil || len(cfg.Plugins) != 0 {
		t.Errorf("want a non-nil empty Plugins map, got %+v", cfg)
	}
}
