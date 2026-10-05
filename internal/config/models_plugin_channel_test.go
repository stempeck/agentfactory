package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateModelProfile_RefusesPluginChannelKeys pins spec L225-228: a models.json profile may
// not set CLAUDE_CODE_PLUGIN_DIRS or CLAUDE_CONFIG_DIR. Both are toolchain keys that the plugin
// channel and the per-agent config dir own; a profile riding them into the launch line would
// replace the integration's plugin set or the agent's whole Claude config.
func TestValidateModelProfile_RefusesPluginChannelKeys(t *testing.T) {
	for _, key := range []string{"CLAUDE_CODE_PLUGIN_DIRS", "CLAUDE_CONFIG_DIR"} {
		t.Run(key, func(t *testing.T) {
			err := validateModelProfile("m", map[string]string{key: "/x"})
			if err == nil {
				t.Fatalf("validateModelProfile accepted a profile setting %s; it must be refused", key)
			}
			if !errors.Is(err, ErrInvalidType) {
				t.Errorf("refusal must wrap ErrInvalidType like the other reserved-key refusals: %v", err)
			}
			if !strings.Contains(err.Error(), key) || !strings.Contains(err.Error(), `"m"`) {
				t.Errorf("refusal must name the key %s and the model %q: %v", key, "m", err)
			}
		})
	}

	t.Run("secret_ref_shape_text_unchanged", func(t *testing.T) {
		// Protective (DO-NOT-CHANGE): D10 widens where a file: reference may point, but the shape
		// refusal a profile already gets keeps its exact text.
		const tok = "file:bad path"
		err := validateModelProfile("m", map[string]string{"ANTHROPIC_AUTH_TOKEN": tok})
		want := fmt.Sprintf("%v: model %q has an invalid %s %q: a file: reference must be a non-empty path with no shell metacharacters", ErrInvalidType, "m", "ANTHROPIC_AUTH_TOKEN", tok)
		if err == nil || err.Error() != want {
			t.Errorf("secret-ref shape refusal = %v\nwant %q", err, want)
		}
	})
}

// USING_MODELS.md says a profile cannot name these keys "whatever the value (even \"\")": the refusal
// tests the key alone, unlike ANTHROPIC_API_KEY where "" is the documented way to clear.
func TestPR724_T19_KeepRefusalForEmptyValue(t *testing.T) {
	for _, key := range []string{"CLAUDE_CODE_PLUGIN_DIRS", "CLAUDE_CONFIG_DIR"} {
		t.Run(key, func(t *testing.T) {
			err := validateModelProfile("m", map[string]string{key: ""})
			if !errors.Is(err, ErrInvalidType) {
				t.Errorf("validateModelProfile of %s=\"\": err = %v, want a refusal wrapping ErrInvalidType", key, err)
			}
		})
	}
}

// USING_MODELS.md's migration note says the refusal is enforced at load, so a models.json written
// before the rule existed is rejected when read, not only when written.
func TestPR724_T19_KeepLoadTimeRefusal(t *testing.T) {
	for _, key := range []string{"CLAUDE_CODE_PLUGIN_DIRS", "CLAUDE_CONFIG_DIR"} {
		for label, val := range map[string]string{"nonempty": "/x", "empty": ""} {
			t.Run(key+"_"+label, func(t *testing.T) {
				dir := t.TempDir()
				if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
					t.Fatal(err)
				}
				raw := fmt.Sprintf(`{"models":{"m":{%q:%q}}}`, key, val)
				if err := os.WriteFile(ModelsConfigPath(dir), []byte(raw), 0o644); err != nil {
					t.Fatal(err)
				}
				if _, err := LoadModelsConfig(dir); !errors.Is(err, ErrInvalidType) {
					t.Errorf("LoadModelsConfig of a profile setting %s=%q: err = %v, want a refusal wrapping ErrInvalidType", key, val, err)
				}
			})
		}
	}
}
