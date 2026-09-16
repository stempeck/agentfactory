package config

import (
	"errors"
	"strings"
	"testing"
)

// poolTokens builds a profile carrying exactly one field under test — the AF_BACKEND_POOL_TOKENS
// value — mirroring companion(v) for the sibling CLAUDE_CODE_MAX_CONTEXT_TOKENS key. THREAD-2 pins
// numeric-shape validation for the new pool key onto the same per-profile validation the companion
// rides, so these rows are the companion's proven set retargeted at the pool key. A claude- prefixed
// model id keeps the profile otherwise valid; validateModelProfile returns on the FIRST error and its
// key loop is random-order, so a case carries only the one defect under test.
func poolTokens(v string) *ModelsConfig {
	return &ModelsConfig{Models: map[string]map[string]string{
		"lmstudio": {"ANTHROPIC_MODEL": "claude-opus-4-8", "AF_BACKEND_POOL_TOKENS": v},
	}}
}

// TestValidateModelsConfig_BackendPoolTokens pins THREAD-2 pin #4: AF_BACKEND_POOL_TOKENS must be a
// positive decimal token count when present, empty or absent otherwise — the same rule and message
// shape as the CLAUDE_CODE_MAX_CONTEXT_TOKENS companion clause at models.go:230-233. Every rejection
// wraps ErrInvalidType and names the profile, the key and the offending value.
//
// RED today: there is no validation clause for AF_BACKEND_POOL_TOKENS, so "garbage"/"0"/"-5"/"+5"/an
// overflowing digit string are all ACCEPTED and each reject subtest fails "expected error, got nil".
// The accept subtests already pass. GREEN after: the new positive-decimal-or-empty clause rejects the
// bad shapes and keeps accepting "262144", "" and an absent key.
func TestValidateModelsConfig_BackendPoolTokens(t *testing.T) {
	const key = "AF_BACKEND_POOL_TOKENS"
	tests := []struct {
		name    string
		cfg     *ModelsConfig
		wantErr bool
		substrs []string
	}{
		{name: "accepts this factory's lmstudio pool", cfg: poolTokens("262144")},
		{name: "accepts an empty pool as an explicit deferral", cfg: poolTokens("")},
		{
			name: "accepts a profile with no pool key at all",
			cfg:  &ModelsConfig{Models: map[string]map[string]string{"lmstudio": {"ANTHROPIC_MODEL": "claude-opus-4-8"}}},
		},
		{name: "accepts the smallest positive pool", cfg: poolTokens("1")},
		{name: "rejects a non-numeric pool", cfg: poolTokens("garbage"), wantErr: true, substrs: []string{"lmstudio", key, "garbage"}},
		{name: "rejects a zero pool", cfg: poolTokens("0"), wantErr: true, substrs: []string{"lmstudio", key, "0"}},
		{name: "rejects a negative pool", cfg: poolTokens("-5"), wantErr: true, substrs: []string{"lmstudio", key, "-5"}},
		{name: "rejects a plus-signed pool", cfg: poolTokens("+5"), wantErr: true, substrs: []string{"lmstudio", key, "+5"}},
		{name: "rejects a pool that overflows", cfg: poolTokens("99999999999999999999999999"), wantErr: true, substrs: []string{"lmstudio", key}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateModelsConfig(tc.cfg)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if !tc.wantErr {
				return
			}
			if !errors.Is(err, ErrInvalidType) {
				t.Errorf("error %v should wrap ErrInvalidType", err)
			}
			for _, want := range tc.substrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should contain %q", err.Error(), want)
				}
			}
		})
	}
}

// childFloorTokens builds a profile carrying exactly the AF_BACKEND_CHILD_FLOOR_TOKENS value under
// test, mirroring poolTokens(v) above. F1 (r3906601296) / BAD-4 (r3906161527) pins the SAME
// numeric-shape validation for the new child-floor key that the pool key already carries: a positive
// decimal when present, empty or absent otherwise. "never zero" is enforced here at load — a declared
// "0" is a LOAD ERROR, never a silent default.
func childFloorTokens(v string) *ModelsConfig {
	return &ModelsConfig{Models: map[string]map[string]string{
		"lmstudio": {"ANTHROPIC_MODEL": "claude-opus-4-8", "AF_BACKEND_CHILD_FLOOR_TOKENS": v},
	}}
}

// TestValidateModelsConfig_BackendChildFloorTokens mirrors TestValidateModelsConfig_BackendPoolTokens
// for AF_BACKEND_CHILD_FLOOR_TOKENS: a positive decimal or empty/absent, same rule and message shape as
// the pool clause at models.go:243-246. Every rejection wraps ErrInvalidType and names the profile, the
// key and the offending value.
//
// RED today: there is no validation clause for AF_BACKEND_CHILD_FLOOR_TOKENS, so
// "garbage"/"0"/"-5"/"+5"/an overflowing digit string are all ACCEPTED and each reject subtest fails
// "expected error, got nil". The accept subtests already pass.
func TestValidateModelsConfig_BackendChildFloorTokens(t *testing.T) {
	const key = "AF_BACKEND_CHILD_FLOOR_TOKENS"
	tests := []struct {
		name    string
		cfg     *ModelsConfig
		wantErr bool
		substrs []string
	}{
		{name: "accepts a conservative default-sized floor", cfg: childFloorTokens("50000")},
		{name: "accepts an empty floor as an explicit deferral to the default", cfg: childFloorTokens("")},
		{
			name: "accepts a profile with no child-floor key at all",
			cfg:  &ModelsConfig{Models: map[string]map[string]string{"lmstudio": {"ANTHROPIC_MODEL": "claude-opus-4-8"}}},
		},
		{name: "accepts the smallest positive floor", cfg: childFloorTokens("1")},
		{name: "rejects a non-numeric floor", cfg: childFloorTokens("garbage"), wantErr: true, substrs: []string{"lmstudio", key, "garbage"}},
		{name: "rejects a zero floor (never zero)", cfg: childFloorTokens("0"), wantErr: true, substrs: []string{"lmstudio", key, "0"}},
		{name: "rejects a negative floor", cfg: childFloorTokens("-5"), wantErr: true, substrs: []string{"lmstudio", key, "-5"}},
		{name: "rejects a plus-signed floor", cfg: childFloorTokens("+5"), wantErr: true, substrs: []string{"lmstudio", key, "+5"}},
		{name: "rejects a floor that overflows", cfg: childFloorTokens("99999999999999999999999999"), wantErr: true, substrs: []string{"lmstudio", key}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateModelsConfig(tc.cfg)
			if tc.wantErr && err == nil {
				t.Fatalf("expected error, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("expected nil error, got %v", err)
			}
			if !tc.wantErr {
				return
			}
			if !errors.Is(err, ErrInvalidType) {
				t.Errorf("error %v should wrap ErrInvalidType", err)
			}
			for _, want := range tc.substrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q should contain %q", err.Error(), want)
				}
			}
		})
	}
}
