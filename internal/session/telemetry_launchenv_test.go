package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// contentCaptureGates are the five OTel content-capture switches the design forbids this
// launch path from ever setting (design-doc Privacy Posture). They must never appear in any
// emitted environment — not in the tmux twin and not in the inline startup command.
var contentCaptureGates = []string{
	"OTEL_LOG_USER_PROMPTS",
	"OTEL_LOG_ASSISTANT_RESPONSES",
	"OTEL_LOG_TOOL_DETAILS",
	"OTEL_LOG_TOOL_CONTENT",
	"OTEL_LOG_RAW_API_BODIES",
}

// TestTelemetryEnvFullSetOn proves a telemetry-on launch emits exactly the seven-variable
// OTel family (each value single-quoted), none of the family is cleared, and no content gate
// appears. The telemetry env is built through the real telemetry.LaunchEnv constructor so the
// test pins the actual seven the session is launched with (AC #3).
func TestTelemetryEnvFullSetOn(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	cfg := config.TelemetryConfig{
		Protocol: "http/json",
		Endpoint: "https://otel.example.com",
		Headers:  map[string]string{"Authorization": "Basic abc"},
	}
	keys := telemetry.CorrelationKeys{
		FactoryID: "fac", Agent: "testagent", WorktreeID: "wt-1",
		FormulaInstance: "inst-1", ModelProfile: "opus",
	}
	mgr.SetTelemetryEnv(telemetry.LaunchEnv(cfg, keys))

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY='1'",
		"OTEL_METRICS_EXPORTER='otlp'",
		"OTEL_LOGS_EXPORTER='otlp'",
		"OTEL_EXPORTER_OTLP_PROTOCOL='http/json'",
		"OTEL_EXPORTER_OTLP_ENDPOINT='https://otel.example.com'",
		"OTEL_EXPORTER_OTLP_HEADERS='Authorization=Basic abc'",
		"OTEL_RESOURCE_ATTRIBUTES='af.factory_id=fac,af.agent=testagent,af.worktree_id=wt-1,af.formula_instance=inst-1,af.model_profile=opus'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("telemetry-on launch must emit %q, got: %s", want, cmd)
		}
	}
	// A telemetry-on launch carries the whole family, so none of it is cleared.
	for _, key := range telemetryFamilyVars {
		if strings.Contains(cmd, key+"=''") {
			t.Errorf("full telemetry set must not clear %q; got: %s", key, cmd)
		}
	}
	for _, gate := range contentCaptureGates {
		if strings.Contains(cmd, gate) {
			t.Errorf("content-capture gate %q must never appear; got: %s", gate, cmd)
		}
	}
}

// TestTelemetryEnvZeroVarsWhenOff proves a telemetry-off launch (the cmd layer never calls
// SetTelemetryEnv) carries zero OTel vars: the enable flag never turns on, no exporter value
// travels, and every family var is emitted only as the empty structural clear (AC #3).
func TestTelemetryEnvZeroVarsWhenOff(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	// Gate off ⇒ SetTelemetryEnv is never called; telemetryEnv stays empty.

	cmd := mgr.BuildStartupCommand()

	for _, banned := range []string{
		"CLAUDE_CODE_ENABLE_TELEMETRY='1'",
		"OTEL_METRICS_EXPORTER='otlp'",
		"OTEL_LOGS_EXPORTER='otlp'",
	} {
		if strings.Contains(cmd, banned) {
			t.Errorf("telemetry-off launch must not carry %q; got: %s", banned, cmd)
		}
	}
	// Every telemetry-family var is present only as the empty clear (never with a value).
	for _, key := range telemetryFamilyVars {
		if !strings.Contains(cmd, key+"=''") {
			t.Errorf("telemetry-off launch must clear %q with an empty value; got: %s", key, cmd)
		}
	}
}

// TestTelemetryEnvContentGatesNeverSet proves that even a telemetry-on launch never emits any
// of the five content-capture gates — they are not in the family and this path must never be
// what turns one on (AC #3, design-doc Privacy Posture).
func TestTelemetryEnvContentGatesNeverSet(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	cfg := config.TelemetryConfig{Protocol: "http/json", Endpoint: "https://otel.example.com"}
	mgr.SetTelemetryEnv(telemetry.LaunchEnv(cfg, telemetry.CorrelationKeys{FactoryID: "fac", Agent: "testagent"}))

	cmd := mgr.BuildStartupCommand()
	for _, gate := range contentCaptureGates {
		if strings.Contains(cmd, gate) {
			t.Errorf("content-capture gate %q must never be emitted; got: %s", gate, cmd)
		}
	}
}

// TestTelemetryHeadersFileRefVerbatimInTmuxEnv locks the telemetry twin asymmetry (AC #3): a
// file: header ref is mirrored into tmux env as the RAW placeholder verbatim (tmux does no
// shell evaluation and a resolved secret would be readable via `tmux show-environment`), while
// the $(cat …) deref appears ONLY in the inline startup command. Drives the full Start() path.
func TestTelemetryHeadersFileRefVerbatimInTmuxEnv(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	cfg := config.TelemetryConfig{
		Protocol: "http/json", Endpoint: "https://otel.example.com",
		Headers: map[string]string{"Authorization": "file:secrets/otel.tok"},
	}
	mgr.SetTelemetryEnv(telemetry.LaunchEnv(cfg, telemetry.CorrelationKeys{Agent: "mouseagent"}))

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	sessionID := mgr.SessionID()
	wantTwin := "SetEnvironment " + sessionID + " OTEL_EXPORTER_OTLP_HEADERS=Authorization=file:secrets/otel.tok"
	var foundTwin bool
	for _, op := range fake.ops {
		if op == wantTwin {
			foundTwin = true
		}
		if strings.HasPrefix(op, "SetEnvironment ") &&
			strings.Contains(op, "OTEL_EXPORTER_OTLP_HEADERS") && strings.Contains(op, "$(cat") {
			t.Errorf("tmux twin must carry the raw file: placeholder, not the $(cat …) deref; got op %q", op)
		}
	}
	if !foundTwin {
		t.Errorf("tmux twin must SetEnvironment the raw file: placeholder verbatim; want %q, ops=%v", wantTwin, fake.ops)
	}

	// The inline command dereferences the secret with $(cat …), preserving the header name.
	inline := mgr.BuildStartupCommand()
	if !strings.Contains(inline, "OTEL_EXPORTER_OTLP_HEADERS='Authorization='\"$(cat ") {
		t.Errorf("inline command must deref the headers file: ref with $(cat …); got: %s", inline)
	}
	if strings.Contains(inline, "OTEL_EXPORTER_OTLP_HEADERS=Authorization=file:") {
		t.Errorf("inline command must not carry the raw headers file: ref; got: %s", inline)
	}
}

// TestTelemetryOffRelaunchClearsFamily proves the Start() hygiene twin for telemetry: a launch
// with no telemetry env (gate off) unsets every one of the seven family vars on the reused tmux
// session AND emits the inline KEY='' clear for each, so a session that previously ran with
// telemetry on carries none of its OTel vars after a telemetry-off relaunch (AC #3).
func TestTelemetryOffRelaunchClearsFamily(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	// No SetTelemetryEnv: models a telemetry-off (re)launch of a reused session.

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	sessionID := mgr.SessionID()
	for _, key := range telemetryFamilyVars {
		wantUnset := "UnsetEnvironment " + sessionID + " " + key
		var found bool
		for _, op := range fake.ops {
			if op == wantUnset {
				found = true
			}
		}
		if !found {
			t.Errorf("telemetry-off Start must unset stale telemetry var %q; ops=%v", key, fake.ops)
		}
	}
	// The inline twin (the only clear a respawn emits) clears the whole family too.
	inline := mgr.BuildStartupCommand()
	for _, key := range telemetryFamilyVars {
		if !strings.Contains(inline, key+"=''") {
			t.Errorf("telemetry-off inline command must clear %q; got: %s", key, inline)
		}
	}
}

// TestTelemetryFamilyVarsMatchLaunchEnv guards the drift the design's single-writer story
// depends on: the hygiene family (telemetryFamilyVars) must be EXACTLY the key set
// telemetry.LaunchEnv injects. If LaunchEnv gained an eighth var not in this list it would be
// emitted but never cleared (a leak on a telemetry-off relaunch); a stale entry here would clear
// a var the family no longer owns. Pins the two lists together so an addition to one fails here.
func TestTelemetryFamilyVarsMatchLaunchEnv(t *testing.T) {
	injected := map[string]bool{}
	for _, ev := range telemetry.LaunchEnv(config.TelemetryConfig{}, telemetry.CorrelationKeys{}) {
		injected[ev.Key] = true
	}
	family := map[string]bool{}
	for _, k := range telemetryFamilyVars {
		family[k] = true
	}
	if len(injected) != len(family) {
		t.Fatalf("LaunchEnv injects %d vars, telemetryFamilyVars has %d — they must match", len(injected), len(family))
	}
	for k := range injected {
		if !family[k] {
			t.Errorf("LaunchEnv injects %q but the hygiene family omits it — it would leak on a telemetry-off relaunch", k)
		}
	}
	for k := range family {
		if !injected[k] {
			t.Errorf("hygiene family lists %q but LaunchEnv never injects it — stale hygiene entry", k)
		}
	}
}

// TestTelemetryAndModelEnvCoexist proves the two orthogonal channels never contaminate each
// other (the outline's redirected-profile correctness case): a redirected model profile
// (base_url + auth_token) and a telemetry env are both emitted in full, the model-env redirect
// hygiene never clears a present telemetry var, and the telemetry hygiene never clears a present
// model var — the separate effective maps hold.
func TestTelemetryAndModelEnvCoexist(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "gpt-4o"},
		{Key: "ANTHROPIC_BASE_URL", Value: "http://localhost:1234"},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "tok"},
	})
	mgr.SetTelemetryEnv(telemetry.LaunchEnv(
		config.TelemetryConfig{Protocol: "http/json", Endpoint: "https://otel.example.com"},
		telemetry.CorrelationKeys{Agent: "testagent", ModelProfile: "gpt-4o"},
	))

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{
		"ANTHROPIC_MODEL='gpt-4o'",
		"ANTHROPIC_BASE_URL='http://localhost:1234'",
		"ANTHROPIC_AUTH_TOKEN='tok'",
		"CLAUDE_CODE_ENABLE_TELEMETRY='1'",
		"OTEL_EXPORTER_OTLP_ENDPOINT='https://otel.example.com'",
		"--model 'gpt-4o'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("model-env and telemetry-env must coexist in full; missing %q in: %s", want, cmd)
		}
	}
	// Neither hygiene pass clears the OTHER family's present vars.
	if strings.Contains(cmd, "OTEL_EXPORTER_OTLP_ENDPOINT=''") {
		t.Errorf("model-env hygiene must not clear a present telemetry var: %s", cmd)
	}
	if strings.Contains(cmd, "ANTHROPIC_BASE_URL=''") {
		t.Errorf("telemetry hygiene must not clear a present model var: %s", cmd)
	}
	// Model-env hygiene still clears a redirect var absent from the profile (own family intact).
	if !strings.Contains(cmd, "ANTHROPIC_SMALL_FAST_MODEL=''") {
		t.Errorf("model-env hygiene should still clear absent redirect vars: %s", cmd)
	}
}

// TestTelemetryEnvSeparateFromModelEnv is the load-bearing regression guard (Gotcha #1): a
// telemetry-only launch (telemetry set, NO model profile) must still emit the legacy model
// fallback — proving telemetry env never landed in modelEnv and so never elided the legacy
// Model/BaseURL/AuthToken emission (the PR #482 regression class).
func TestTelemetryEnvSeparateFromModelEnv(t *testing.T) {
	entry := config.AgentEntry{
		Type: "autonomous", Description: "test",
		Model: "legacy-model", BaseURL: "http://legacy:1234", AuthToken: "legacy-tok",
	}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	// Telemetry ON, no model profile resolved (modelEnv empty).
	mgr.SetTelemetryEnv(telemetry.LaunchEnv(
		config.TelemetryConfig{Protocol: "http/json", Endpoint: "https://otel.example.com"},
		telemetry.CorrelationKeys{Agent: "testagent"},
	))

	cmd := mgr.BuildStartupCommand()

	// The legacy fields must still emit — telemetry must not have tripped the modelEnv gate.
	for _, want := range []string{
		"ANTHROPIC_BASE_URL='http://legacy:1234'",
		"ANTHROPIC_AUTH_TOKEN='legacy-tok'",
		"--model 'legacy-model'",
		"CLAUDE_CODE_ENABLE_TELEMETRY='1'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("telemetry-only launch must keep the legacy model path AND telemetry; missing %q in: %s", want, cmd)
		}
	}
}
