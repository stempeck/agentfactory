package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
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

// window builds a profile carrying exactly one defect: the window value under test.
// A claude- prefixed model id keeps the profile otherwise valid and out of the pairing
// lint's way. validateModelProfile returns on the FIRST error and its key loop is
// random-order, so a case with two defects would report either one nondeterministically.
func window(v string) *ModelsConfig {
	return &ModelsConfig{Models: map[string]map[string]string{
		"codex": {"ANTHROPIC_MODEL": "claude-opus-4-8", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": v},
	}}
}

func companion(v string) *ModelsConfig {
	return &ModelsConfig{Models: map[string]map[string]string{
		"codex": {"ANTHROPIC_MODEL": "claude-opus-4-8", "CLAUDE_CODE_MAX_CONTEXT_TOKENS": v},
	}}
}

func TestValidateModelsConfig_CompactionKeys(t *testing.T) {
	const (
		winKey  = "CLAUDE_CODE_AUTO_COMPACT_WINDOW"
		compKey = "CLAUDE_CODE_MAX_CONTEXT_TOKENS"
	)
	// Every rejection names the profile, the key, the offending value and both bounds, so
	// the operator can fix the file without consulting the docs.
	rangeSubstrs := func(val string) []string {
		return []string{"codex", winKey, val, "100000", "1000000"}
	}

	tests := []struct {
		name    string
		cfg     *ModelsConfig
		wantErr bool
		substrs []string
	}{
		// --- CLAUDE_CODE_AUTO_COMPACT_WINDOW: the values issue #602 names ---
		{name: "accepts an in-range window", cfg: window("200000")},
		{name: "rejects a non-numeric window", cfg: window("abc"), wantErr: true, substrs: rangeSubstrs("abc")},
		{
			// The plausible typo. strconv.Atoi("2000") succeeds, so a parse-only guard
			// would let this through — only a real bounds comparison rejects it.
			name: "rejects a below-range window naming profile, key, value and range",
			cfg:  window("2000"), wantErr: true, substrs: rangeSubstrs("2000"),
		},
		{name: "rejects a zero window", cfg: window("0"), wantErr: true, substrs: rangeSubstrs("0")},
		{name: "rejects an above-range window", cfg: window("2000000"), wantErr: true, substrs: rangeSubstrs("2000000")},
		{
			// The host guards the variable with a truthy test, so "" is treated as unset —
			// an explicit deferral, exactly as ANTHROPIC_API_KEY:"" is an explicit clear.
			name: "accepts an empty window as an explicit deferral", cfg: window(""),
		},
		{
			// The Go zero value for an absent key is "", which must not be read as 0.
			name: "accepts a profile with no window key at all",
			cfg:  &ModelsConfig{Models: map[string]map[string]string{"codex": {"ANTHROPIC_MODEL": "claude-opus-4-8"}}},
		},

		// --- CLAUDE_CODE_AUTO_COMPACT_WINDOW: boundaries ---
		{name: "accepts the inclusive lower bound", cfg: window("100000")},
		{name: "accepts the inclusive upper bound", cfg: window("1000000")},
		{name: "rejects one below the lower bound", cfg: window("99999"), wantErr: true, substrs: rangeSubstrs("99999")},
		{name: "rejects one above the upper bound", cfg: window("1000001"), wantErr: true, substrs: rangeSubstrs("1000001")},
		{name: "accepts a value just under the upper bound", cfg: window("999999")},

		// --- CLAUDE_CODE_AUTO_COMPACT_WINDOW: not ASCII decimal ---
		{
			// strconv.Atoi("+200000") returns 200000 with a nil error, so a bare Atoi plus a
			// range check accepts this. The rule is digits-only; a signed value is not.
			name: "rejects a plus-signed window", cfg: window("+200000"), wantErr: true, substrs: rangeSubstrs("+200000"),
		},
		{name: "rejects a negative window", cfg: window("-5"), wantErr: true, substrs: rangeSubstrs("-5")},
		{
			// Trimming would save a value the host then reads differently. Do not be helpful.
			name: "rejects a window padded with spaces", cfg: window(" 200000 "), wantErr: true, substrs: []string{"codex", winKey},
		},
		{name: "rejects a window with a trailing newline", cfg: window("200000\n"), wantErr: true, substrs: []string{"codex", winKey}},
		{name: "rejects scientific notation", cfg: window("1e6"), wantErr: true, substrs: rangeSubstrs("1e6")},
		{name: "rejects digit separators", cfg: window("200_000"), wantErr: true, substrs: rangeSubstrs("200_000")},
		{name: "rejects a decimal point", cfg: window("200000.0"), wantErr: true, substrs: rangeSubstrs("200000.0")},
		{
			// unicode.IsDigit is true for these runes; ASCII decimal is not.
			name: "rejects Arabic-Indic digits", cfg: window("١٢٣٤٥٦"), wantErr: true, substrs: []string{"codex", winKey},
		},
		{
			// strconv saturates to MaxUint64/MaxInt64 with ErrRange here. An implementation
			// that discards the parse error would compare the saturated value, not the input.
			name: "rejects a digit string that overflows", cfg: window("99999999999999999999999999"), wantErr: true, substrs: []string{"codex", winKey},
		},
		{
			// Pinned deliberately: leading zeros are unambiguously ASCII decimal and in range,
			// so they are accepted. Recorded here so a future change is a visible decision.
			name: "accepts a window with leading zeros", cfg: window("0200000"),
		},

		// --- CLAUDE_CODE_MAX_CONTEXT_TOKENS: the companion key ---
		{name: "accepts a positive companion", cfg: companion("250000")},
		{name: "accepts an empty companion", cfg: companion("")},
		{
			name: "accepts a profile with no companion key at all",
			cfg:  &ModelsConfig{Models: map[string]map[string]string{"codex": {"ANTHROPIC_MODEL": "claude-opus-4-8"}}},
		},
		{name: "accepts the smallest positive companion", cfg: companion("1")},
		{
			// No upper bound on the companion: it declares the model's real context window,
			// which is a different quantity from the auto-compact window's ceiling.
			name: "accepts a companion above the window ceiling", cfg: companion("2000000"),
		},
		{name: "rejects a non-numeric companion", cfg: companion("garbage"), wantErr: true, substrs: []string{"codex", compKey, "garbage"}},
		{
			// The host guards the companion with n > 0, so a non-positive value silently
			// reverts the model window to its foreign-id default.
			name: "rejects a zero companion", cfg: companion("0"), wantErr: true, substrs: []string{"codex", compKey, "0"},
		},
		{name: "rejects a negative companion", cfg: companion("-1"), wantErr: true, substrs: []string{"codex", compKey, "-1"}},
		{name: "rejects a plus-signed companion", cfg: companion("+250000"), wantErr: true, substrs: []string{"codex", compKey, "+250000"}},
		{
			// With no upper bound, a discarded ErrRange leaves a saturated positive value
			// that passes a bare > 0 check.
			name: "rejects a companion that overflows", cfg: companion("99999999999999999999999999"), wantErr: true, substrs: []string{"codex", compKey},
		},

		// --- interaction: the two clauses are independent, and neither implements the lint ---
		{
			name: "accepts a coherent foreign-model pairing",
			cfg: &ModelsConfig{Models: map[string]map[string]string{"codex": {
				"ANTHROPIC_MODEL":                 "gpt-5.6-sol",
				"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000",
				"CLAUDE_CODE_MAX_CONTEXT_TOKENS":  "250000",
			}}},
		},
		{
			// The incoherent-but-legal combination from issue #602. It is a WARNING emitted by
			// the cmd layer, never a validation error — an implementation that wired the pairing
			// lint into the validator would fail here.
			name: "accepts the incoherent pairing the lint only warns about",
			cfg: &ModelsConfig{Models: map[string]map[string]string{"codex": {
				"ANTHROPIC_MODEL":                 "gpt-5.6-sol",
				"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000",
			}}},
		},
		{
			name: "accepts a window on a profile with no model id",
			cfg:  &ModelsConfig{Models: map[string]map[string]string{"codex": {"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "200000"}}},
		},
		{
			name: "attributes the error to the offending profile",
			cfg: &ModelsConfig{Models: map[string]map[string]string{
				"clean": {"ANTHROPIC_MODEL": "claude-opus-4-8"},
				"codex": {"ANTHROPIC_MODEL": "claude-opus-4-8", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "2000"},
			}},
			wantErr: true, substrs: rangeSubstrs("2000"),
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

// The write path is what makes "a rejected document leaves models.json untouched" true, so
// pin the guard on SaveModelsConfig and not only on the validator it delegates to.
func TestSaveModelsConfig_RejectsOutOfRangeWindow(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := ModelsConfigPath(dir)
	if err := SaveModelsConfig(path, window("2000")); err == nil {
		t.Fatal("expected SaveModelsConfig to reject an out-of-range window")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("rejected write must not create models.json (stat err=%v)", err)
	}
}

// The unquoted-number trap: a profile value is a JSON string, so an operator who writes
// 220000 without quotes gets a type error. The message must name the quoting rule.
func TestLoadModelsConfig_UnquotedNumber_NamesQuotingRule(t *testing.T) {
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	raw := []byte(`{"models":{"codex":{"CLAUDE_CODE_AUTO_COMPACT_WINDOW":220000}}}`)
	if err := os.WriteFile(ModelsConfigPath(dir), raw, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, err := LoadModelsConfig(dir)
	if err == nil {
		t.Fatal("expected a parse error for an unquoted number")
	}
	for _, want := range []string{"quoted", "string"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q should name the quoting rule (missing %q)", err.Error(), want)
		}
	}
}

func TestPairingLintProfile(t *testing.T) {
	foreign := func(extra map[string]string) map[string]string {
		p := map[string]string{"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000"}
		for k, v := range extra {
			p[k] = v
		}
		return p
	}
	tests := []struct {
		name    string
		profile map[string]string
		want    bool
	}{
		{name: "fires on a foreign id declaring more than the cap with no companion", profile: foreign(nil), want: true},
		{name: "an empty companion counts as absent", profile: foreign(map[string]string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS": ""}), want: true},
		{name: "a companion silences it", profile: foreign(map[string]string{"CLAUDE_CODE_MAX_CONTEXT_TOKENS": "250000"})},
		{
			// The host derives a real window for its own models, so the combination is coherent.
			name:    "a claude- model never fires",
			profile: map[string]string{"ANTHROPIC_MODEL": "claude-opus-4-8", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000"},
		},
		{
			// The Go zero value "" is not claude- prefixed; a model-less profile is legal
			// (TestResolveModelEnv_EmptyProfileMatchesNotPassthrough) and is not foreign.
			name:    "a profile with no model id never fires",
			profile: map[string]string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000"},
		},
		{name: "an empty profile never fires", profile: map[string]string{}},
		{
			name:    "a window at the cap never fires",
			profile: map[string]string{"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "200000"},
		},
		{
			name:    "a window below the cap never fires",
			profile: map[string]string{"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "150000"},
		},
		{
			// "" must not be read as 0, and an absent window must not fire either.
			name:    "an empty window never fires",
			profile: map[string]string{"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": ""},
		},
		{name: "a foreign id with no window never fires", profile: map[string]string{"ANTHROPIC_MODEL": "gpt-5.6-sol"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			warning, ok := PairingLintProfile("codex", tc.profile)
			if ok != tc.want {
				t.Fatalf("hasWarning = %v, want %v (warning=%q)", ok, tc.want, warning)
			}
			if !ok {
				if warning != "" {
					t.Errorf("no warning expected but got %q", warning)
				}
				return
			}
			for _, w := range []string{"codex", "CLAUDE_CODE_MAX_CONTEXT_TOKENS", "200000"} {
				if !strings.Contains(warning, w) {
					t.Errorf("warning %q should contain %q", warning, w)
				}
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

// --- Issue #598 Phase 1a: endpoint class-coverage helpers ---

// endpointProfile is the shape every helper test starts from: a gateway profile that authenticates
// and declares a main model, leaving every class key to derivation. It is the exact shape issue
// #598 is filed against.
func endpointProfile(extra map[string]string) map[string]string {
	p := map[string]string{
		envBaseURL:   "http://localhost:4000",
		envAuthToken: "tok",
		envModel:     "gpt-5.6-terra",
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func TestCompleteEndpointProfile(t *testing.T) {
	t.Run("no base URL is a no-op that still returns a distinct map", func(t *testing.T) {
		direct := map[string]string{envModel: "claude-opus-5"}
		got := CompleteEndpointProfile(direct)

		if !reflect.DeepEqual(got, direct) {
			t.Errorf("a profile with no %s must come back unchanged; got %v want %v", envBaseURL, got, direct)
		}
		// Aliasing guard: the returned map is handed to callers that may write to it, and the input
		// is cfg.Models[name] — a map the loaded registry shares process-wide.
		got["ANTHROPIC_MODEL"] = "mutated"
		if direct[envModel] != "claude-opus-5" {
			t.Errorf("the no-op path returned the caller's own map; writing to the result corrupted the shared registry profile: %v", direct)
		}
	})

	t.Run("never mutates its argument", func(t *testing.T) {
		p := endpointProfile(nil)
		before := map[string]string{}
		for k, v := range p {
			before[k] = v
		}

		_ = CompleteEndpointProfile(p)

		if !reflect.DeepEqual(p, before) {
			t.Errorf("CompleteEndpointProfile mutated its argument: before %v after %v", before, p)
		}
	})

	t.Run("fills absent class keys from the declared main model", func(t *testing.T) {
		got := CompleteEndpointProfile(endpointProfile(nil))

		for _, key := range []string{
			"ANTHROPIC_SMALL_FAST_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL",
			"ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		} {
			if got[key] != "gpt-5.6-terra" {
				t.Errorf("class key %q must be derived from the declared main model so the gateway is never asked for a claude-* id; got %q", key, got[key])
			}
		}
	})

	t.Run("fills a class key declared empty", func(t *testing.T) {
		got := CompleteEndpointProfile(endpointProfile(map[string]string{
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "",
		}))

		// An empty class value defers to the host, and on a gateway the host's default is a
		// claude-* id the endpoint refuses — the exact failure this issue exists to close.
		if got["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "gpt-5.6-terra" {
			t.Errorf("a class key declared as \"\" must be filled, not preserved; got %q", got["ANTHROPIC_DEFAULT_HAIKU_MODEL"])
		}
	})

	t.Run("respects a declared non-empty class value", func(t *testing.T) {
		got := CompleteEndpointProfile(endpointProfile(map[string]string{
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-luna",
		}))

		if got["ANTHROPIC_DEFAULT_HAIKU_MODEL"] != "gpt-5.6-luna" {
			t.Errorf("a declared class value must win over derivation; got %q", got["ANTHROPIC_DEFAULT_HAIKU_MODEL"])
		}
	})

	t.Run("never fills CLAUDE_CODE_SUBAGENT_MODEL", func(t *testing.T) {
		got := CompleteEndpointProfile(endpointProfile(nil))

		// The host key overrides the per-spawn model param and the sub-agent's own frontmatter, so
		// deriving it would collapse every deliberate cheap-tier spawn onto the main model.
		if v, ok := got["CLAUDE_CODE_SUBAGENT_MODEL"]; ok {
			t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL is in the inventory for hygiene parity but is deliberately excluded from derivation; got %q", v)
		}
	})

	t.Run("invents nothing when there is no declared source", func(t *testing.T) {
		modelless := map[string]string{envBaseURL: "http://localhost:4000", envAuthToken: "tok"}

		got := CompleteEndpointProfile(modelless)

		for _, key := range EndpointClassKeys {
			if v, ok := got[key]; ok {
				t.Errorf("with no declared model there is no value to copy, and derivation must never invent an id; %q was set to %q", key, v)
			}
		}
	})

	t.Run("every emitted class value is non-empty on an endpoint profile", func(t *testing.T) {
		got := CompleteEndpointProfile(endpointProfile(map[string]string{
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "",
			"ANTHROPIC_SMALL_FAST_MODEL":     "",
		}))

		for _, key := range EndpointClassKeys {
			if v, ok := got[key]; ok && v == "" {
				t.Errorf("class key %q emitted an empty value; the host reads that as unset and falls back to a claude-* id", key)
			}
		}
	})

	t.Run("preserves non-class keys verbatim including empty values", func(t *testing.T) {
		got := CompleteEndpointProfile(endpointProfile(map[string]string{
			envAPIKey: "",
			"ALPHA":   "a",
		}))

		if v, ok := got[envAPIKey]; !ok || v != "" {
			t.Errorf("%s:\"\" is an explicit operator clear and must survive verbatim; got %q ok=%v", envAPIKey, v, ok)
		}
		if got["ALPHA"] != "a" {
			t.Errorf("a non-class key must be copied verbatim; got %q", got["ALPHA"])
		}
	})

	t.Run("nil profile does not panic", func(t *testing.T) {
		if got := CompleteEndpointProfile(nil); len(got) != 0 {
			t.Errorf("a nil profile must yield an empty result; got %v", got)
		}
	})
}

func TestMissingEndpointClasses(t *testing.T) {
	t.Run("empty for a profile with no base URL", func(t *testing.T) {
		if got := MissingEndpointClasses(map[string]string{envModel: "claude-opus-5"}); len(got) != 0 {
			t.Errorf("derivation is endpoint-gated, so a direct profile leaves nothing to derivation; got %v", got)
		}
	})

	t.Run("reports every underivable class on a model-less endpoint profile", func(t *testing.T) {
		got := MissingEndpointClasses(map[string]string{envBaseURL: "http://localhost:4000", envAuthToken: "tok"})

		// Complete fills nothing here (no source to copy), but this is the profile that most needs
		// the warning — so Missing reports what is undeclared, not what Complete would fill.
		want := []string{
			"ANTHROPIC_SMALL_FAST_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL",
			"ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("missing classes:\n got %v\nwant %v", got, want)
		}
	})

	t.Run("excludes declared classes and includes empty ones", func(t *testing.T) {
		got := MissingEndpointClasses(endpointProfile(map[string]string{
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-luna",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":  "",
		}))

		want := []string{
			"ANTHROPIC_SMALL_FAST_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL",
			"ANTHROPIC_DEFAULT_SONNET_MODEL",
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("a declared class is not missing and an empty one is:\n got %v\nwant %v", got, want)
		}
	})

	t.Run("never reports CLAUDE_CODE_SUBAGENT_MODEL", func(t *testing.T) {
		for _, key := range MissingEndpointClasses(endpointProfile(nil)) {
			if key == "CLAUDE_CODE_SUBAGENT_MODEL" {
				t.Error("CLAUDE_CODE_SUBAGENT_MODEL is never derived, so reporting it as left-to-derivation would tell the operator something untrue")
			}
		}
	})

	t.Run("nothing missing when every class is declared", func(t *testing.T) {
		got := MissingEndpointClasses(endpointProfile(map[string]string{
			"ANTHROPIC_SMALL_FAST_MODEL":     "a",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   "b",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "c",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "d",
		}))
		if len(got) != 0 {
			t.Errorf("a fully declared endpoint profile leaves nothing to derivation; got %v", got)
		}
	})

	t.Run("nil profile does not panic", func(t *testing.T) {
		if got := MissingEndpointClasses(nil); len(got) != 0 {
			t.Errorf("a nil profile must yield nothing; got %v", got)
		}
	})
}

// TestCompleteAndMissingAgree pins the invariant that separates the two helpers: everything
// Complete fills was reported Missing, but not everything Missing gets filled — a model-less
// endpoint profile has nothing to copy from, and that is exactly when the operator most needs to
// be told.
func TestCompleteAndMissingAgree(t *testing.T) {
	for name, profile := range map[string]map[string]string{
		"model-less endpoint": {envBaseURL: "http://localhost:4000", envAuthToken: "tok"},
		"full endpoint":       endpointProfile(nil),
		"partly declared":     endpointProfile(map[string]string{"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-luna"}),
	} {
		t.Run(name, func(t *testing.T) {
			missing := map[string]bool{}
			for _, k := range MissingEndpointClasses(profile) {
				missing[k] = true
			}
			completed := CompleteEndpointProfile(profile)
			for _, k := range EndpointClassKeys {
				if profile[k] != "" {
					continue
				}
				if completed[k] != "" && !missing[k] {
					t.Errorf("class %q was filled by CompleteEndpointProfile but never reported by MissingEndpointClasses; the two must not disagree about what derivation owns", k)
				}
			}
		})
	}
}

func TestCoverageLintProfile(t *testing.T) {
	t.Run("silent on a profile with no base URL", func(t *testing.T) {
		for name, profile := range map[string]map[string]string{
			"default":  {envModel: "claude-opus-5", "ANTHROPIC_DEFAULT_OPUS_MODEL": "claude-opus-5"},
			"sonnet-5": {envModel: "claude-sonnet-5"},
			"fable-5":  {envModel: "claude-fable-5"},
		} {
			if warning, ok := CoverageLintProfile(name, profile); ok {
				t.Errorf("a direct Anthropic profile has no coverage problem to report; got %q", warning)
			}
		}
	})

	t.Run("silent when every derivable class is declared", func(t *testing.T) {
		warning, ok := CoverageLintProfile("codex", endpointProfile(map[string]string{
			"ANTHROPIC_SMALL_FAST_MODEL":     "a",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   "b",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "c",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "d",
		}))
		if ok {
			t.Errorf("a fully declared endpoint profile must not warn on every save forever; got %q", warning)
		}
	})

	t.Run("names the profile and every derived class", func(t *testing.T) {
		warning, ok := CoverageLintProfile("codex", endpointProfile(map[string]string{
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-luna",
		}))
		if !ok {
			t.Fatal("an endpoint profile leaving classes to derivation must be surfaced at write time")
		}
		for _, want := range []string{`"codex"`, "small/background", "opus", "sonnet", envModel} {
			if !strings.Contains(warning, want) {
				t.Errorf("the warning must name %q so the operator knows which profile and which classes; got %q", want, warning)
			}
		}
		if strings.Contains(warning, "haiku") {
			t.Errorf("haiku is declared on this profile and is not derived, so naming it would be untrue; got %q", warning)
		}
		if strings.Contains(warning, "sub-agent") {
			t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL is never derived, so the warning must not claim it is; got %q", warning)
		}
	})

	t.Run("does not promise derivation on a profile with no main model", func(t *testing.T) {
		// The profile that most needs telling: a gateway with no ANTHROPIC_MODEL at all.
		// CompleteEndpointProfile fills nothing here, so wording that says these classes are
		// "left to derivation from ANTHROPIC_MODEL" would tell the operator their gaps are
		// covered at the exact moment every class is about to fall through to a claude-* id.
		profile := map[string]string{envBaseURL: "http://localhost:4000", envAuthToken: "tok"}

		filled := CompleteEndpointProfile(profile)
		for _, key := range EndpointClassKeys {
			if filled[key] != "" {
				t.Fatalf("fixture is wrong: nothing can be derived without a main model, but %s=%q", key, filled[key])
			}
		}

		warning, ok := CoverageLintProfile("bare-gateway", profile)
		if !ok {
			t.Fatal("a profile whose every class is uncovered must warn")
		}
		if strings.Contains(warning, "to derivation from") {
			t.Errorf("nothing derives these classes, so the warning must not say they are left to derivation; got %q", warning)
		}
		for _, want := range []string{`"bare-gateway"`, "uncovered", envModel, "small/background", "opus", "sonnet", "haiku"} {
			if !strings.Contains(warning, want) {
				t.Errorf("the warning must name %q; got %q", want, warning)
			}
		}
	})

	t.Run("carries the fable-class gateway note", func(t *testing.T) {
		warning, _ := CoverageLintProfile("codex", endpointProfile(nil))

		// Nothing profile-side can map the fable class, so the note points at the only mechanism
		// that can: a gateway alias.
		if !strings.Contains(warning, "fable") || !strings.Contains(warning, "alias") {
			t.Errorf("the warning must tell the operator fable-class traffic is served only via a gateway alias; got %q", warning)
		}
	})

	t.Run("nil profile does not panic", func(t *testing.T) {
		if warning, ok := CoverageLintProfile("p", nil); ok {
			t.Errorf("a nil profile has nothing to warn about; got %q", warning)
		}
	})
}

// directScaffoldModels is the install scaffold's own registry (internal/cmd/install.go:186),
// copied verbatim so the no-delta proof is about the profiles operators actually get. The
// endpoint profile (lmstudio) is carried too, and is resolved by
// TestResolveModelEnv_ScaffoldEndpointProfileDoesDerive: without one in the SAME registry the
// no-delta suite would prove only "this config has nothing to derive from", not "derivation is
// gated per profile".
func directScaffoldModels() *ModelsConfig {
	return &ModelsConfig{
		Default: "default",
		Models: map[string]map[string]string{
			"default": {
				"ANTHROPIC_MODEL":                "claude-opus-5",
				"ANTHROPIC_DEFAULT_OPUS_MODEL":   "claude-opus-5",
				"ANTHROPIC_DEFAULT_SONNET_MODEL": "claude-sonnet-5",
			},
			"sonnet-5": {"ANTHROPIC_MODEL": "claude-sonnet-5"},
			"opus-4-8": {"ANTHROPIC_MODEL": "claude-opus-4-8"},
			"opus-5":   {"ANTHROPIC_MODEL": "claude-opus-5"},
			"fable-5":  {"ANTHROPIC_MODEL": "claude-fable-5"},
			"lmstudio": {
				"ANTHROPIC_BASE_URL":   "http://localhost:1234",
				"ANTHROPIC_AUTH_TOKEN": "lm-studio",
				"ANTHROPIC_MODEL":      "qwen2.5-coder-32b",
				"ANTHROPIC_API_KEY":    "",
			},
		},
	}
}

// TestResolveModelEnv_DirectProfilesNoDelta is the AC-4 gate for the endpoint-class derivation
// hook (issue #598 Phase 2): each of the five Anthropic-direct profiles must resolve to the same
// []EnvVar after the hook as before it. It is authored and proven green against the UNDERIVED
// resolver first, because a no-delta test whose pre-change run was never observed proves only
// that two unmeasured things agree.
//
// Derivation is gated on a non-empty ANTHROPIC_BASE_URL and none of the five carries one, so the
// gate — not the ladder — is what this pins. Mutating isEndpointProfile to report true turns every
// case red.
func TestResolveModelEnv_DirectProfilesNoDelta(t *testing.T) {
	cfg := directScaffoldModels()

	tests := []struct {
		name string
		want []EnvVar
	}{
		{
			name: "default",
			want: []EnvVar{
				{Key: "ANTHROPIC_MODEL", Value: "claude-opus-5"},
				{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: "claude-opus-5"},
				{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: "claude-sonnet-5"},
			},
		},
		{name: "sonnet-5", want: []EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-sonnet-5"}}},
		{name: "opus-4-8", want: []EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4-8"}}},
		{name: "opus-5", want: []EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-5"}}},
		{name: "fable-5", want: []EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-fable-5"}}},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if len(cfg.Models[tc.name]) == 0 {
				t.Fatalf("fixture defines no %q profile, so the resolution below takes the raw-id passthrough branch and proves nothing", tc.name)
			}
			name, env, ok, err := ResolveModelEnv(cfg, "", tc.name, "", "")
			if err != nil || !ok {
				t.Fatalf("direct profile %q must resolve: ok=%v err=%v", tc.name, ok, err)
			}
			if name != tc.name {
				t.Errorf("name: got %q want %q", name, tc.name)
			}
			if !reflect.DeepEqual(env, tc.want) {
				t.Errorf("profile %q must resolve byte-identically to its pre-derivation set; derivation is endpoint-gated and this profile declares no %s:\n got %#v\nwant %#v", tc.name, envBaseURL, env, tc.want)
			}
		})
	}

	t.Run("the default selection path is gated identically", func(t *testing.T) {
		name, env, ok, err := ResolveModelEnv(cfg, "", "", "", "")
		if err != nil || !ok || name != "default" {
			t.Fatalf("cfg.Default must resolve: name=%q ok=%v err=%v", name, ok, err)
		}
		want := []EnvVar{
			{Key: "ANTHROPIC_MODEL", Value: "claude-opus-5"},
			{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: "claude-opus-5"},
			{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: "claude-sonnet-5"},
		}
		if !reflect.DeepEqual(env, want) {
			t.Errorf("gating must not depend on which precedence rung selected the profile:\n got %#v\nwant %#v", env, want)
		}
	})

}

// TestResolveModelEnv_ScaffoldEndpointProfileDoesDerive is the companion the no-delta suite above
// needs but must not contain. Those six cases would be equally green under a resolver that derives
// NOTHING AT ALL, so on their own they prove "this registry has nothing to derive from" rather than
// "the gate is per profile"; resolving the scaffold's own endpoint profile from the SAME cfg is
// what makes the distinction observable.
//
// It is a sibling rather than a seventh sub-test because the suite above is required to be green
// against the UNDERIVED resolver (issue #598 Phase 2, AC-4) and this case is a deliberate delta —
// housing it under a name containing "NoDelta" would quietly break the property that name asserts.
func TestResolveModelEnv_ScaffoldEndpointProfileDoesDerive(t *testing.T) {
	cfg := directScaffoldModels()

	name, env, ok, err := ResolveModelEnv(cfg, "", "lmstudio", "", "")
	if err != nil || !ok || name != "lmstudio" {
		t.Fatalf("the scaffold's endpoint profile must resolve: name=%q ok=%v err=%v", name, ok, err)
	}
	want := []EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "qwen2.5-coder-32b"},
		{Key: "ANTHROPIC_API_KEY", Value: ""},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "lm-studio"},
		{Key: "ANTHROPIC_BASE_URL", Value: "http://localhost:1234"},
		{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: "qwen2.5-coder-32b"},
		{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: "qwen2.5-coder-32b"},
		{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: "qwen2.5-coder-32b"},
		{Key: "ANTHROPIC_SMALL_FAST_MODEL", Value: "qwen2.5-coder-32b"},
	}
	if !reflect.DeepEqual(env, want) {
		t.Errorf("one registry, two behaviors: the five direct profiles gain nothing and this one gains four class keys:\n got %#v\nwant %#v", env, want)
	}
	if _, declared := cfg.Models["lmstudio"]["ANTHROPIC_SMALL_FAST_MODEL"]; declared {
		t.Errorf("fixture drift: the scaffold profile must NOT declare a class key, or the four keys above are not derived")
	}
}

func endpointModels(profile map[string]string) *ModelsConfig {
	return &ModelsConfig{Models: map[string]map[string]string{"codex": profile}}
}

// gatewayProfile is the resolver-side twin of endpointProfile (models_test.go:649). It is a
// separate helper on purpose: its model ids are DISTINCT PER RUNG (gw-main-v1 vs gw-haiku-v2), so
// a derived value can be told apart from a declared one by reading the want literal. The Phase-1a
// fixture uses one id everywhere, which is why the ladder can be flattened without failing it.
func gatewayProfile(extra map[string]string) map[string]string {
	p := map[string]string{
		envBaseURL:   "http://localhost:4000",
		envAuthToken: "tok",
		envModel:     "gw-main-v1",
	}
	for k, v := range extra {
		p[k] = v
	}
	return p
}

func resolvedValue(env []EnvVar, key string) (string, bool) {
	for _, e := range env {
		if e.Key == key {
			return e.Value, true
		}
	}
	return "", false
}

// TestResolveModelEnv_ResolverDerivesEndpointClasses pins the WIRING, not the ladder. Phase 1a's
// TestCompleteEndpointProfile already proves the helper correct; a correct helper nothing calls
// changes no launch. Every case here goes through ResolveModelEnv and asserts on the emitted
// []EnvVar, which is what the launch chokepoint actually exports.
func TestResolveModelEnv_ResolverDerivesEndpointClasses(t *testing.T) {
	t.Run("ladder order and sorted placement", func(t *testing.T) {
		cfg := endpointModels(gatewayProfile(map[string]string{
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gw-haiku-v2",
		}))

		_, env, ok, err := ResolveModelEnv(cfg, "", "codex", "", "")
		if err != nil || !ok {
			t.Fatalf("endpoint profile must resolve: ok=%v err=%v", ok, err)
		}
		want := []EnvVar{
			{Key: "ANTHROPIC_MODEL", Value: "gw-main-v1"},
			{Key: "ANTHROPIC_AUTH_TOKEN", Value: "tok"},
			{Key: "ANTHROPIC_BASE_URL", Value: "http://localhost:4000"},
			{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: "gw-haiku-v2"},
			{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: "gw-main-v1"},
			{Key: "ANTHROPIC_DEFAULT_SONNET_MODEL", Value: "gw-main-v1"},
			{Key: "ANTHROPIC_SMALL_FAST_MODEL", Value: "gw-haiku-v2"},
		}
		if !reflect.DeepEqual(env, want) {
			t.Errorf("SMALL_FAST reads the declared HAIKU before the main model, and every derived key lands in sorted position:\n got %#v\nwant %#v", env, want)
		}
	})

	t.Run("every derived class is present, non-empty and a declared value", func(t *testing.T) {
		profile := gatewayProfile(nil)
		_, env, _, err := ResolveModelEnv(endpointModels(profile), "", "codex", "", "")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		declared := map[string]bool{}
		for _, v := range profile {
			declared[v] = true
		}
		// A literal want, not a range over derivedEndpointClassKeys: iterating the table under
		// test makes a deleted rung shrink the loop instead of failing it.
		for _, key := range []string{
			"ANTHROPIC_SMALL_FAST_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL",
			"ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL",
		} {
			v, found := resolvedValue(env, key)
			if !found {
				t.Errorf("class key %q is absent from the launch env, so the host answers that class with its own claude-* id — issue #598 itself; got %#v", key, env)
				continue
			}
			if v == "" {
				t.Errorf("class key %q emitted an empty value; the host reads that as unset", key)
				continue
			}
			if !declared[v] {
				t.Errorf("class key %q resolved to %q, which the profile never declared — derivation copies, it never invents", key, v)
			}
		}
	})

	t.Run("SUBAGENT and FABLE are never added by derivation", func(t *testing.T) {
		// Scoped to a fixture that declares NEITHER: a declared CLAUDE_CODE_SUBAGENT_MODEL
		// legitimately survives into the set, so "the set never contains it" is the wrong
		// invariant. What must hold is that derivation never ADDS it.
		_, env, _, err := ResolveModelEnv(endpointModels(gatewayProfile(nil)), "", "codex", "", "")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		for _, key := range []string{"CLAUDE_CODE_SUBAGENT_MODEL", "ANTHROPIC_DEFAULT_FABLE_MODEL"} {
			if v, found := resolvedValue(env, key); found {
				t.Errorf("%q must not be derived (Decision 14 / the spike-gated fable rung); got %q", key, v)
			}
		}
	})

	t.Run("a declared class value wins over derivation", func(t *testing.T) {
		cfg := endpointModels(gatewayProfile(map[string]string{
			"ANTHROPIC_SMALL_FAST_MODEL":     "gw-small",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   "gw-opus",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "gw-sonnet",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "gw-haiku",
		}))
		_, env, _, err := ResolveModelEnv(cfg, "", "codex", "", "")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		for key, want := range map[string]string{
			"ANTHROPIC_SMALL_FAST_MODEL":     "gw-small",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   "gw-opus",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "gw-sonnet",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "gw-haiku",
		} {
			if got, _ := resolvedValue(env, key); got != want {
				t.Errorf("declared %s=%q must survive derivation; got %q", key, want, got)
			}
		}
	})

	t.Run("a class key declared empty is filled, non-class empties are kept", func(t *testing.T) {
		cfg := endpointModels(gatewayProfile(map[string]string{
			"ANTHROPIC_DEFAULT_OPUS_MODEL": "",
			envAPIKey:                      "",
		}))
		_, env, _, err := ResolveModelEnv(cfg, "", "codex", "", "")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got, _ := resolvedValue(env, "ANTHROPIC_DEFAULT_OPUS_MODEL"); got != "gw-main-v1" {
			t.Errorf("a class key declared \"\" defers to the host, which on a gateway is issue #598 — it must be filled, not preserved; got %q", got)
		}
		v, found := resolvedValue(env, envAPIKey)
		if !found || v != "" {
			t.Errorf("%s:\"\" is an explicit operator clear and is NOT a class key; it must still be emitted verbatim; got %q found=%v", envAPIKey, v, found)
		}
	})

	t.Run("a fully declared profile and one left to derivation resolve identically", func(t *testing.T) {
		bare := endpointModels(gatewayProfile(nil))
		full := endpointModels(gatewayProfile(map[string]string{
			"ANTHROPIC_SMALL_FAST_MODEL":     "gw-main-v1",
			"ANTHROPIC_DEFAULT_OPUS_MODEL":   "gw-main-v1",
			"ANTHROPIC_DEFAULT_SONNET_MODEL": "gw-main-v1",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "gw-main-v1",
		}))
		_, gotBare, _, _ := ResolveModelEnv(bare, "", "codex", "", "")
		_, gotFull, _, _ := ResolveModelEnv(full, "", "codex", "", "")
		if !reflect.DeepEqual(gotBare, gotFull) {
			t.Errorf("derivation must produce exactly the env an operator would get by declaring every class by hand:\n derived  %#v\n declared %#v", gotBare, gotFull)
		}
	})

	t.Run("gating: an absent base URL derives nothing", func(t *testing.T) {
		cfg := endpointModels(map[string]string{envModel: "claude-opus-5", envAuthToken: "tok"})
		_, env, _, err := ResolveModelEnv(cfg, "", "codex", "", "")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		want := []EnvVar{
			{Key: "ANTHROPIC_MODEL", Value: "claude-opus-5"},
			{Key: "ANTHROPIC_AUTH_TOKEN", Value: "tok"},
		}
		if !reflect.DeepEqual(env, want) {
			t.Errorf("no %s means no derivation, even with an auth token present:\n got %#v\nwant %#v", envBaseURL, env, want)
		}
	})

	t.Run("gating: an empty base URL derives nothing and is still emitted", func(t *testing.T) {
		cfg := endpointModels(map[string]string{envModel: "claude-opus-5", envBaseURL: ""})
		_, env, _, err := ResolveModelEnv(cfg, "", "codex", "", "")
		if err != nil {
			t.Fatalf("an empty base URL is not an endpoint, so checkEndpointComplete must not fire: %v", err)
		}
		want := []EnvVar{
			{Key: "ANTHROPIC_MODEL", Value: "claude-opus-5"},
			{Key: "ANTHROPIC_BASE_URL", Value: ""},
		}
		if !reflect.DeepEqual(env, want) {
			t.Errorf("%s:\"\" is not an endpoint (isEndpointProfile is a bare != \"\" test):\n got %#v\nwant %#v", envBaseURL, env, want)
		}
	})

	t.Run("gating: a whitespace base URL IS an endpoint and DOES derive", func(t *testing.T) {
		// Characterization, not endorsement. isEndpointProfile is a bare != "" comparison with no
		// trimming, so " " gates open. It is unreachable through LoadModelsConfig/SaveModelsConfig —
		// validateModelProfile rejects it because url.Parse(" ") yields an empty Scheme and Host — and
		// reachable only by a hand-built config like this one. Pinned so a future trim is a deliberate,
		// visible decision rather than a silent behavior change on the launch path.
		cfg := endpointModels(map[string]string{envBaseURL: " ", envAuthToken: "tok", envModel: "gw-main-v1"})
		_, env, _, err := ResolveModelEnv(cfg, "", "codex", "", "")
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if got, found := resolvedValue(env, "ANTHROPIC_DEFAULT_OPUS_MODEL"); !found || got != "gw-main-v1" {
			t.Errorf("a whitespace base URL opens the gate; got %q found=%v", got, found)
		}
	})

	t.Run("the raw-id passthrough branch still emits ANTHROPIC_MODEL alone", func(t *testing.T) {
		cfg := endpointModels(gatewayProfile(nil))
		name, env, ok, err := ResolveModelEnv(cfg, "", "claude-opus-4-8", "", "")
		if err != nil || !ok {
			t.Fatalf("raw passthrough: ok=%v err=%v", ok, err)
		}
		want := []EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4-8"}}
		if name != "claude-opus-4-8" || !reflect.DeepEqual(env, want) {
			t.Errorf("the passthrough branch returns before the endpoint guard and must be untouched by derivation; got name=%q env=%#v want %#v", name, env, want)
		}
	})

	t.Run("an incomplete endpoint errors before anything is derived", func(t *testing.T) {
		cfg := endpointModels(map[string]string{envBaseURL: "http://localhost:4000", envModel: "gw-main-v1"})
		_, env, ok, err := ResolveModelEnv(cfg, "", "codex", "", "")
		if err == nil {
			t.Fatal("checkEndpointComplete must run BEFORE the derivation hook")
		}
		if ok || env != nil {
			t.Errorf("an unauthenticated endpoint must yield no env at all, derived or otherwise; got ok=%v env=%#v", ok, env)
		}
		if !strings.Contains(err.Error(), envAuthToken) {
			t.Errorf("error should name %s; got %v", envAuthToken, err)
		}
	})

	t.Run("the shared registry profile is never written back to", func(t *testing.T) {
		profile := gatewayProfile(nil)
		before := map[string]string{}
		for k, v := range profile {
			before[k] = v
		}
		cfg := &ModelsConfig{Models: map[string]map[string]string{"codex": profile}}

		_, first, _, _ := ResolveModelEnv(cfg, "", "codex", "", "")
		_, second, _, _ := ResolveModelEnv(cfg, "", "codex", "", "")

		if !reflect.DeepEqual(cfg.Models["codex"], before) {
			t.Errorf("cfg.Models is shared process-wide; derivation must work on a copy:\n before %v\n after  %v", before, cfg.Models["codex"])
		}
		if !reflect.DeepEqual(first, second) {
			t.Errorf("two resolves of the same registry must agree:\n first  %#v\n second %#v", first, second)
		}
	})

	t.Run("deterministic across repeats", func(t *testing.T) {
		cfg := endpointModels(gatewayProfile(map[string]string{"ZETA": "z", "ALPHA": "a"}))
		var first []EnvVar
		for i := 0; i < 50; i++ {
			_, env, _, err := ResolveModelEnv(cfg, "", "codex", "", "")
			if err != nil {
				t.Fatalf("iter %d: %v", i, err)
			}
			if first == nil {
				first = env
				continue
			}
			if !reflect.DeepEqual(env, first) {
				t.Fatalf("iter %d nondeterministic:\n got %#v\nfirst %#v", i, env, first)
			}
		}
	})

	t.Run("the resolver ignores the ambient environment", func(t *testing.T) {
		// The bare profile is what gives this its teeth: a fixture that DECLARES ANTHROPIC_MODEL
		// hides an os.Getenv fallback behind the declared value, so the test would stay green
		// through a real ADR-004 violation. Same reasoning as
		// TestEndpointClassHelpersIgnoreAmbientEnvironment's `bare` input.
		full := endpointModels(gatewayProfile(nil))
		bare := endpointModels(map[string]string{envBaseURL: "http://localhost:4000", envAuthToken: "tok"})
		_, cleanFull, _, _ := ResolveModelEnv(full, "", "codex", "", "")
		_, cleanBare, _, _ := ResolveModelEnv(bare, "", "codex", "", "")

		const junk = "af-598-resolver-sentinel"
		for _, k := range append([]string{envModel, envBaseURL, envAuthToken}, EndpointClassKeys...) {
			t.Setenv(k, junk)
		}

		_, ambientFull, _, _ := ResolveModelEnv(full, "", "codex", "", "")
		if !reflect.DeepEqual(ambientFull, cleanFull) {
			t.Errorf("the resolver reads no environment (ADR-004):\n clean   %#v\n ambient %#v", cleanFull, ambientFull)
		}
		_, ambientBare, _, _ := ResolveModelEnv(bare, "", "codex", "", "")
		if !reflect.DeepEqual(ambientBare, cleanBare) {
			t.Errorf("an endpoint profile with no declared main model must derive NOTHING, not fall back to the ambient one (ADR-004):\n clean   %#v\n ambient %#v", cleanBare, ambientBare)
		}
	})
}

// --- issue #598 Phase 3a: the accessors the reporting surfaces read the vocabulary through ---

// TestDerivedEndpointClassKeys_ExcludesSubagentAndCannotBeMutated pins both halves of the accessor's
// contract. The exclusion is the load-bearing one: a caller that iterated the wider EndpointClassKeys
// inventory would emit a verdict for CLAUDE_CODE_SUBAGENT_MODEL, which derivation never fills.
func TestDerivedEndpointClassKeys_ExcludesSubagentAndCannotBeMutated(t *testing.T) {
	got := DerivedEndpointClassKeys()
	if len(got) == 0 {
		t.Fatal("the derived-class inventory must not be empty")
	}
	for _, key := range got {
		if key == "CLAUDE_CODE_SUBAGENT_MODEL" {
			t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL is deliberately not derived; it must not appear here")
		}
	}
	for _, key := range got {
		if !slices.Contains(EndpointClassKeys, key) {
			t.Errorf("every derived key must be a member of the wider inventory; %q is not", key)
		}
	}

	got[0] = "CLOBBERED"
	if DerivedEndpointClassKeys()[0] == "CLOBBERED" {
		t.Error("the accessor must hand out a copy — the resolver shares this inventory process-wide")
	}
}

func TestEndpointClassLabel_CoversEveryDerivedClass(t *testing.T) {
	for _, key := range DerivedEndpointClassKeys() {
		label, known := EndpointClassLabel(key)
		if !known || label == "" {
			t.Errorf("every derived class needs an operator-facing word; %q has none", key)
		}
	}
	if _, known := EndpointClassLabel("ANTHROPIC_NOT_A_CLASS"); known {
		t.Error("an unknown key must report itself unknown rather than yielding an empty label")
	}
}

// TestEndpointClassSource_NamesTheRungTheLadderUsed is the accessor's whole reason for existing: a
// coverage report says which key to edit, and the two mutually referential rungs mean the answer is
// often NOT the main model.
func TestEndpointClassSource_NamesTheRungTheLadderUsed(t *testing.T) {
	cases := []struct {
		name       string
		profile    map[string]string
		key        string
		wantSource string
		wantKnown  bool
	}{
		{
			name:       "a declared class is its own source",
			profile:    map[string]string{envBaseURL: "http://gw", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "small-1", envModel: "main-1"},
			key:        "ANTHROPIC_DEFAULT_HAIKU_MODEL",
			wantSource: "ANTHROPIC_DEFAULT_HAIKU_MODEL",
			wantKnown:  true,
		},
		{
			name:       "small/background prefers the declared haiku over the main model",
			profile:    map[string]string{envBaseURL: "http://gw", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "small-1", envModel: "main-1"},
			key:        "ANTHROPIC_SMALL_FAST_MODEL",
			wantSource: "ANTHROPIC_DEFAULT_HAIKU_MODEL",
			wantKnown:  true,
		},
		{
			name:       "opus has only the main model to copy",
			profile:    map[string]string{envBaseURL: "http://gw", envModel: "main-1"},
			key:        "ANTHROPIC_DEFAULT_OPUS_MODEL",
			wantSource: envModel,
			wantKnown:  true,
		},
		{
			name:      "with no main model opus has no source at all",
			profile:   map[string]string{envBaseURL: "http://gw", "ANTHROPIC_DEFAULT_HAIKU_MODEL": "small-1"},
			key:       "ANTHROPIC_DEFAULT_OPUS_MODEL",
			wantKnown: false,
		},
		{
			name:      "a profile with no endpoint derives nothing, so nothing has a source",
			profile:   map[string]string{envModel: "claude-opus-5"},
			key:       "ANTHROPIC_DEFAULT_OPUS_MODEL",
			wantKnown: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			source, known := EndpointClassSource(tc.profile, tc.key)
			if known != tc.wantKnown || (tc.wantKnown && source != tc.wantSource) {
				t.Fatalf("EndpointClassSource = (%q, %t), want (%q, %t)", source, known, tc.wantSource, tc.wantKnown)
			}
			// The accessor and the ladder must never disagree: whatever key it names has to hold the
			// value CompleteEndpointProfile actually filled in.
			completed := CompleteEndpointProfile(tc.profile)
			if !known {
				if completed[tc.key] != "" {
					t.Errorf("reported no source, yet the ladder filled %q with %q", tc.key, completed[tc.key])
				}
				return
			}
			if completed[tc.key] != tc.profile[source] {
				t.Errorf("reported source %q (%q), but the ladder filled %q", source, tc.profile[source], completed[tc.key])
			}
		})
	}
}
