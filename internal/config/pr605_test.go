package config

import (
	"errors"
	"strings"
	"testing"
)

// TestValidateModelsConfig_KeyShape pins PR #605 T-F1: profile KEY NAMES ride into the launch
// line's `unset`/`export` segments unquoted, so a name that is not a safe shell identifier is a
// silent-misconfig (and, for a metacharacter, an injection) vector. validateModelProfile must
// reject a malformed key name at the write boundary — the value-validation twin the PR shipped
// for values but left open for keys. A well-formed but system-critical name like PATH is a VALID
// shape (its safety is enforced at cleanup, T-P1), so it is NOT rejected here.
func TestValidateModelsConfig_KeyShape(t *testing.T) {
	profileWithKey := func(key string) *ModelsConfig {
		return &ModelsConfig{Models: map[string]map[string]string{
			"codex": {"ANTHROPIC_MODEL": "claude-opus-4-8", key: "x"},
		}}
	}
	tests := []struct {
		name    string
		key     string
		wantErr bool
	}{
		{"accepts a conventional upper-snake key", "GOOD_KEY", false},
		{"accepts a leading underscore", "_OK", false},
		{"accepts digits after the first char", "A1_B2", false},
		{"accepts a well-formed system name (guarded at cleanup, not here)", "PATH", false},
		{"rejects an embedded space", "BAD KEY", true},
		{"rejects a shell metacharacter", "BAD;KEY", true},
		{"rejects a dollar sign", "BAD$KEY", true},
		{"rejects a hyphen", "BAD-KEY", true},
		{"rejects a leading digit", "1BADKEY", true},
		{"rejects an empty key", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateModelsConfig(profileWithKey(tc.key))
			if tc.wantErr && err == nil {
				t.Fatalf("key %q must be rejected for its shape", tc.key)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("key %q is well-formed and must be accepted; got: %v", tc.key, err)
			}
			if tc.wantErr {
				if !errors.Is(err, ErrInvalidType) {
					t.Errorf("shape rejection must wrap ErrInvalidType; got: %v", err)
				}
				if tc.key != "" && !strings.Contains(err.Error(), tc.key) {
					t.Errorf("error must name the offending key %q; got: %v", tc.key, err)
				}
			}
		})
	}
}
