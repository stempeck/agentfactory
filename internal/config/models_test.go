package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadModelsConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	cfg := &ModelsConfig{
		Default: "opus",
		Models: map[string]map[string]string{
			"opus": {
				"ANTHROPIC_MODEL":   "claude-opus-4-8",
				"ANTHROPIC_API_KEY": "", // empty value MUST survive (explicit clear semantics)
			},
			"local": {
				"ANTHROPIC_MODEL":      "claude-local",
				"ANTHROPIC_BASE_URL":   "http://localhost:8080",
				"ANTHROPIC_AUTH_TOKEN": "tok",
			},
		},
		Agents: map[string]string{"manager": "opus"},
	}

	if err := SaveModelsConfig(ModelsConfigPath(dir), cfg); err != nil {
		t.Fatalf("SaveModelsConfig: %v", err)
	}
	assertNoTempResidue(t, afDir)

	loaded, err := LoadModelsConfig(dir)
	if err != nil {
		t.Fatalf("LoadModelsConfig: %v", err)
	}
	if !reflect.DeepEqual(loaded.Models, cfg.Models) {
		t.Errorf("Models round-trip mismatch:\n got %#v\nwant %#v", loaded.Models, cfg.Models)
	}
	if v, ok := loaded.Models["opus"]["ANTHROPIC_API_KEY"]; !ok || v != "" {
		t.Errorf("empty ANTHROPIC_API_KEY did not survive round-trip: present=%v val=%q", ok, v)
	}
	if loaded.Default != cfg.Default {
		t.Errorf("Default mismatch: got %q want %q", loaded.Default, cfg.Default)
	}
	if !reflect.DeepEqual(loaded.Agents, cfg.Agents) {
		t.Errorf("Agents mismatch: got %#v want %#v", loaded.Agents, cfg.Agents)
	}
}

func TestLoadModelsConfig_AbsentFile_NoError(t *testing.T) {
	dir := t.TempDir() // no models.json written

	cfg, err := LoadModelsConfig(dir)
	if err != nil {
		t.Fatalf("expected nil error for absent file, got %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("absent file must NOT return ErrNotFound")
	}
	if cfg == nil {
		t.Fatal("expected non-nil cfg for absent file")
	}
}

func TestValidateModelsConfig(t *testing.T) {
	tests := []struct {
		name    string
		cfg     *ModelsConfig
		wantErr bool
		substr  string
	}{
		{
			name:    "rejects AF_ROLE identity denylist key",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"AF_ROLE": "manager"}}},
			wantErr: true,
			substr:  "AF_ROLE",
		},
		{
			name:    "rejects non-empty ANTHROPIC_API_KEY",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_API_KEY": "sk-secret"}}},
			wantErr: true,
			substr:  "ANTHROPIC_API_KEY",
		},
		{
			name:    "rejects base_url without auth_token (incomplete endpoint)",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_BASE_URL": "http://localhost:8080"}}},
			wantErr: true,
			substr:  "ANTHROPIC_AUTH_TOKEN",
		},
		{
			name:    "rejects malformed base_url",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_BASE_URL": "ftp://nope", "ANTHROPIC_AUTH_TOKEN": "tok"}}},
			wantErr: true,
			substr:  "base_url",
		},
		{
			name: "rejects agents naming undefined model",
			cfg: &ModelsConfig{
				Models: map[string]map[string]string{"opus": {"ANTHROPIC_MODEL": "claude-opus-4-8"}},
				Agents: map[string]string{"mgr": "ghost"},
			},
			wantErr: true,
			substr:  "ghost",
		},
		{
			name: "rejects default naming undefined model",
			cfg: &ModelsConfig{
				Models:  map[string]map[string]string{"opus": {"ANTHROPIC_MODEL": "claude-opus-4-8"}},
				Default: "ghost",
			},
			wantErr: true,
			substr:  "ghost",
		},
		{
			name:    "accepts empty ANTHROPIC_API_KEY (explicit clear)",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_MODEL": "claude-opus-4-8", "ANTHROPIC_API_KEY": ""}}},
			wantErr: false,
		},
		{
			name:    "accepts empty config",
			cfg:     &ModelsConfig{},
			wantErr: false,
		},
		{
			name: "accepts a complete endpoint profile",
			cfg: &ModelsConfig{Models: map[string]map[string]string{
				"local": {"ANTHROPIC_MODEL": "m", "ANTHROPIC_BASE_URL": "https://api.example.com", "ANTHROPIC_AUTH_TOKEN": "tok"},
			}},
			wantErr: false,
		},
		{
			name:    "rejects sk-shaped literal auth_token on non-loopback endpoint",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_BASE_URL": "https://api.example.com", "ANTHROPIC_AUTH_TOKEN": "sk-live-abc"}}},
			wantErr: true,
			substr:  "file:",
		},
		{
			name:    "accepts sk-shaped literal auth_token on loopback endpoint (locality exemption)",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_BASE_URL": "http://localhost:1234", "ANTHROPIC_AUTH_TOKEN": "sk-live-abc"}}},
			wantErr: false,
		},
		{
			name:    "accepts file: secret reference on loopback endpoint",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_BASE_URL": "http://127.0.0.1:4000", "ANTHROPIC_AUTH_TOKEN": "file:.agentfactory/secrets/x.key"}}},
			wantErr: false,
		},
		{
			name:    "rejects file: reference containing shell metacharacters",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_BASE_URL": "http://x:4000", "ANTHROPIC_AUTH_TOKEN": "file:a; rm -rf /"}}},
			wantErr: true,
			substr:  "file:",
		},
		{
			name:    "rejects empty file: reference path",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_BASE_URL": "http://x:4000", "ANTHROPIC_AUTH_TOKEN": "file:"}}},
			wantErr: true,
			substr:  "file:",
		},
		{
			// Documented, design-accepted residual gap (security.md V2): the sk- heuristic is
			// defense-in-depth, NOT the guarantee. A real credential without the sk- shape passes
			// validation on a non-loopback endpoint; the file: convention + Phase-2 deref is the
			// actual protection. Pinned so a future heuristic change is a deliberate, visible decision.
			name:    "residual gap: non-sk- literal on non-loopback passes (file: convention is the real guard)",
			cfg:     &ModelsConfig{Models: map[string]map[string]string{"p": {"ANTHROPIC_BASE_URL": "https://api.example.com", "ANTHROPIC_AUTH_TOKEN": "abcdef123456"}}},
			wantErr: false,
		},
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
			if tc.wantErr && tc.substr != "" && !strings.Contains(err.Error(), tc.substr) {
				t.Errorf("error %q should contain %q", err.Error(), tc.substr)
			}
		})
	}
}

func TestResolveModelEnv_ExpandsFullSet(t *testing.T) {
	cfg := &ModelsConfig{
		Models: map[string]map[string]string{
			"opus": {
				"ANTHROPIC_MODEL":   "claude-opus-4-8",
				"ANTHROPIC_API_KEY": "", // empty preserved
				"ANTHROPIC_BETA":    "context-1m",
				"EXTRA_FLAG":        "1",
			},
		},
	}
	name, env, ok, err := ResolveModelEnv(cfg, "", "opus", "", "")
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !ok {
		t.Fatal("expected ok=true")
	}
	if name != "opus" {
		t.Errorf("name: got %q want %q", name, "opus")
	}
	want := []EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4-8"},
		{Key: "ANTHROPIC_API_KEY", Value: ""},
		{Key: "ANTHROPIC_BETA", Value: "context-1m"},
		{Key: "EXTRA_FLAG", Value: "1"},
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env ordering:\n got %#v\nwant %#v", env, want)
	}
}

func TestResolveModelEnv_Precedence(t *testing.T) {
	cfg := &ModelsConfig{
		Default: "dft",
		Models: map[string]map[string]string{
			"cli":    {"ANTHROPIC_MODEL": "m-cli"},
			"mark":   {"ANTHROPIC_MODEL": "m-mark"},
			"agentm": {"ANTHROPIC_MODEL": "m-agent"},
			"legacy": {"ANTHROPIC_MODEL": "m-legacy"},
			"dft":    {"ANTHROPIC_MODEL": "m-default"},
		},
		Agents: map[string]string{"mgr": "agentm"},
	}

	if name, _, ok, _ := ResolveModelEnv(cfg, "mgr", "cli", "mark", "legacy"); !ok || name != "cli" {
		t.Errorf("cli precedence: got name=%q ok=%v, want cli", name, ok)
	}
	if name, _, ok, _ := ResolveModelEnv(cfg, "mgr", "", "mark", "legacy"); !ok || name != "mark" {
		t.Errorf("marker precedence: got name=%q ok=%v, want mark", name, ok)
	}
	if name, _, ok, _ := ResolveModelEnv(cfg, "mgr", "", "", "legacy"); !ok || name != "agentm" {
		t.Errorf("agents precedence: got name=%q ok=%v, want agentm", name, ok)
	}
	if name, _, ok, _ := ResolveModelEnv(cfg, "unknown", "", "", "legacy"); !ok || name != "legacy" {
		t.Errorf("legacy precedence: got name=%q ok=%v, want legacy", name, ok)
	}
	if name, _, ok, _ := ResolveModelEnv(cfg, "unknown", "", "", ""); !ok || name != "dft" {
		t.Errorf("default precedence: got name=%q ok=%v, want dft", name, ok)
	}

	empty := &ModelsConfig{Models: map[string]map[string]string{}}
	if name, env, ok, err := ResolveModelEnv(empty, "nobody", "", "", ""); ok || err != nil || env != nil || name != "" {
		t.Errorf("empty selection: got name=%q env=%v ok=%v err=%v, want zero/false", name, env, ok, err)
	}
}

func TestResolveModelEnv_RawStringPassthrough(t *testing.T) {
	want := []EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4-8"}}

	name, env, ok, err := ResolveModelEnv(&ModelsConfig{}, "", "claude-opus-4-8", "", "")
	if err != nil || !ok {
		t.Fatalf("raw passthrough: ok=%v err=%v", ok, err)
	}
	if name != "claude-opus-4-8" {
		t.Errorf("name: got %q want claude-opus-4-8", name)
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("env: got %#v want %#v", env, want)
	}

	if _, env2, ok2, err2 := ResolveModelEnv(nil, "", "claude-sonnet-4-6", "", ""); !ok2 || err2 != nil ||
		!reflect.DeepEqual(env2, []EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-sonnet-4-6"}}) {
		t.Errorf("nil cfg passthrough failed: env=%#v ok=%v err=%v", env2, ok2, err2)
	}

	cfg := &ModelsConfig{Models: map[string]map[string]string{"opus": {"ANTHROPIC_MODEL": "claude-opus-4-8"}}}
	if name3, env3, ok3, err3 := ResolveModelEnv(cfg, "", "claude-haiku-4-5", "", ""); !ok3 || err3 != nil ||
		name3 != "claude-haiku-4-5" || !reflect.DeepEqual(env3, []EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-haiku-4-5"}}) {
		t.Errorf("unknown-name passthrough failed: name=%q env=%#v ok=%v err=%v", name3, env3, ok3, err3)
	}
}

func TestResolveModelEnv_IncompleteEndpoint_Errors(t *testing.T) {
	cfg := &ModelsConfig{
		Models: map[string]map[string]string{
			"local": {
				"ANTHROPIC_MODEL":    "claude-local",
				"ANTHROPIC_BASE_URL": "http://localhost:8080",
				// no ANTHROPIC_AUTH_TOKEN — incomplete endpoint
			},
		},
	}
	_, env, ok, err := ResolveModelEnv(cfg, "", "local", "", "")
	if err == nil {
		t.Fatal("expected error for incomplete endpoint, got nil")
	}
	if ok {
		t.Errorf("expected ok=false on error, got ok=true")
	}
	if env != nil {
		t.Errorf("expected nil env on error, got %#v", env)
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_AUTH_TOKEN") {
		t.Errorf("error should mention ANTHROPIC_AUTH_TOKEN, got %v", err)
	}
}

// A profile that is present but declares zero exports resolves as a MATCHED
// profile (ok=true, empty env) — distinct from an unmatched raw id, which would
// emit ANTHROPIC_MODEL. Pins the chosen semantics for the empty-profile edge.
func TestResolveModelEnv_EmptyProfileMatchesNotPassthrough(t *testing.T) {
	cfg := &ModelsConfig{Models: map[string]map[string]string{"blank": {}}}
	name, env, ok, err := ResolveModelEnv(cfg, "", "blank", "", "")
	if err != nil || !ok {
		t.Fatalf("empty profile should match: ok=%v err=%v", ok, err)
	}
	if name != "blank" {
		t.Errorf("name: got %q want blank", name)
	}
	if len(env) != 0 {
		t.Errorf("matched empty profile should emit no exports, got %#v", env)
	}
}

func TestResolveModelEnv_Deterministic(t *testing.T) {
	cfg := &ModelsConfig{
		Models: map[string]map[string]string{
			"multi": {
				"ANTHROPIC_MODEL": "claude-opus-4-8",
				"ZETA":            "z",
				"ALPHA":           "a",
				"MIKE":            "m",
				"BRAVO":           "", // empty preserved
			},
		},
	}
	var first []EnvVar
	for i := 0; i < 50; i++ {
		_, env, ok, err := ResolveModelEnv(cfg, "", "multi", "", "")
		if err != nil || !ok {
			t.Fatalf("iter %d: ok=%v err=%v", i, ok, err)
		}
		if first == nil {
			first = env
			continue
		}
		if !reflect.DeepEqual(env, first) {
			t.Fatalf("iter %d nondeterministic:\n got %#v\nfirst %#v", i, env, first)
		}
	}
	want := []EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4-8"},
		{Key: "ALPHA", Value: "a"},
		{Key: "BRAVO", Value: ""},
		{Key: "MIKE", Value: "m"},
		{Key: "ZETA", Value: "z"},
	}
	if !reflect.DeepEqual(first, want) {
		t.Errorf("ordering:\n got %#v\nwant %#v", first, want)
	}
}
