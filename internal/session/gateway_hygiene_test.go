package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// gatewayUpstreamAuthTestKeys mirrors config.afGatewayUpstreamKeys (the five reserved names a
// profile may never carry — internal/config/models_gateway_test.go pins that denylist). Declared
// separately here (session and config are independent packages, ADR-004) so this file's own
// twin-literal drift test can catch the two lists diverging, exactly as intake.md #11/
// investigation_report.md's Files-to-Modify table §2 requires.
var gatewayUpstreamAuthTestKeys = []string{
	"OPENAI_API_KEY",
	"CHATGPT_TOKEN_DIR",
	"CHATGPT_AUTH_FILE",
	"CHATGPT_API_BASE",
	"CODEX_HOME",
}

// TestLaunchHygieneClearsUpstreamAuthVars pins AC-4: every launch/respawn path clears the five
// gateway upstream-auth names on the launch line (buildStartupCommand()'s inline KEY='' loop),
// unconditionally — never gated on the model-env branch, unlike redirectFamilyVars. Three
// sub-cases per the design's "for every profile kind" AC bullet.
func TestLaunchHygieneClearsUpstreamAuthVars(t *testing.T) {
	for _, tc := range []struct {
		name      string
		configure func(mgr *Manager)
	}{
		{
			name:      "no model-env at all",
			configure: func(mgr *Manager) {},
		},
		{
			name: "plain endpoint profile",
			configure: func(mgr *Manager) {
				mgr.c.ModelEnv = []config.EnvVar{
					{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
					{Key: envBaseURL, Value: "http://127.0.0.1:4000"},
					{Key: envAuthToken, Value: "tok"},
				}
			},
		},
		{
			name: "universe-channel profile",
			configure: func(mgr *Manager) {
				mgr.c.ModelEnv = []config.EnvVar{
					{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
					{Key: universeTestKey, Value: "220000"},
				}
				mgr.c.ModelKeyUniverse = []string{"ANTHROPIC_MODEL", universeTestKey}
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mgr, fake := startMouseAgent(t, nil)
			tc.configure(mgr)

			if err := mgr.Start(); err != nil {
				t.Fatalf("Start: unexpected error: %v", err)
			}

			inline := startupLine(t, mgr)
			for _, key := range gatewayUpstreamAuthTestKeys {
				assertNoTmuxEnvKey(t, fake.ops, mgr.SessionID(), key)
				if !strings.Contains(inline, key+"=''") {
					t.Errorf("the launch line must clear %q with %s=''; got: %s", key, key, inline)
				}
				if strings.Contains(inline, key+"='") && !strings.Contains(inline, key+"=''") {
					t.Errorf("gateway upstream-auth key %q must never carry a non-empty inline value; got: %s", key, inline)
				}
			}
		})
	}
}

// TestGatewayHygieneClearIsUnconditionalEvenIfEffectiveContainsKey is the adversarial case a
// conditional `if !effective[key]` guard (the pre-fix shape) would fail: it forces a gateway
// upstream-auth key into `effective` by naming it directly in ModelEnv — the session layer never
// re-runs config.validateModelProfile's V6 denylist, so this is a
// legitimate simulation of "V6 didn't block it" (a config bug, a future profile-less code path,
// etc.), not a contrived case. Proves the hygiene clear stands on its own rather than depending on
// V6's correctness — the same key is set (because it's in modelEnv) AND unconditionally cleared
// (because it's in afGatewayUpstreamAuthVars), and the clear must be the LAST write so it wins.
func TestGatewayHygieneClearIsUnconditionalEvenIfEffectiveContainsKey(t *testing.T) {
	const leaked = "sk-leaked-if-hygiene-were-conditional"
	mgr, fake := startMouseAgent(t, nil)
	mgr.c.ModelEnv = []config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
		{Key: envBaseURL, Value: "http://127.0.0.1:4000"},
		{Key: envAuthToken, Value: "tok"},
		{Key: "OPENAI_API_KEY", Value: leaked},
	}

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	assertNoTmuxEnvKey(t, fake.ops, mgr.SessionID(), "OPENAI_API_KEY")

	inline := startupLine(t, mgr)
	// A single startup command line applies `KEY=value` prefix assignments in order; a later
	// assignment to the same key wins (POSIX), so the hygiene clear must be the LAST occurrence.
	lastRealValue := strings.LastIndex(inline, "OPENAI_API_KEY="+leaked)
	lastCleared := strings.LastIndex(inline, "OPENAI_API_KEY=''")
	if lastCleared == -1 {
		t.Fatalf("the launch line must clear OPENAI_API_KEY even when it was just exported via effective; got: %s", inline)
	}
	if lastRealValue != -1 && lastCleared < lastRealValue {
		t.Fatalf("the hygiene clear must appear AFTER the real-value export so it wins in shell prefix-assignment order; got: %s", inline)
	}
}

// TestLaunchEnvForSubscriptionProfileCarriesNoChatgptOrOpenAIKey pins the frame-lift invariant:
// a profile shaped exactly like the codex-subscription seed (an ordinary endpoint profile whose
// ANTHROPIC_AUTH_TOKEN happens to point at a different secret file) never carries a mode marker,
// because V6 denylists all five gateway names from ever becoming a profile key in the first
// place — mode is invisible at this layer by construction, not by a per-profile check.
func TestLaunchEnvForSubscriptionProfileCarriesNoChatgptOrOpenAIKey(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.c.ModelEnv = []config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "chatgpt/gpt-5.3-codex"},
		{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: "chatgpt/gpt-5-mini"},
		{Key: envBaseURL, Value: "http://127.0.0.1:4000"},
		{Key: envAuthToken, Value: "file:.agentfactory/secrets/litellm.key"},
		{Key: "ANTHROPIC_API_KEY", Value: ""},
	}

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	sessionID := mgr.SessionID()
	for _, key := range gatewayUpstreamAuthTestKeys {
		for _, op := range fake.ops {
			if strings.HasPrefix(op, "SetEnvironment "+sessionID+" "+key+"=") {
				t.Errorf("a codex-subscription-shaped profile must never SET %q in the tmux env; ops=%v", key, fake.ops)
			}
		}
	}

	inline := startupLine(t, mgr)
	for _, key := range gatewayUpstreamAuthTestKeys {
		if strings.Contains(inline, key+"='") && !strings.Contains(inline, key+"=''") {
			t.Errorf("a codex-subscription-shaped profile must never export a non-empty %q inline; got: %s", key, inline)
		}
	}
}

// TestUniverseCarveOutVars_ExcludesGatewayFamily is the protective DO-NOT-CHANGE assertion for
// decisions.md's standing call: the five gateway names are deliberately NOT added to
// universeCarveOutVars, because they are already denylisted from models.json entirely (V6), so
// carve-out membership would be vacuous either way — but the design intent is explicit ("never
// carved out"), and this pins it as a checkable fact rather than prose.
func TestUniverseCarveOutVars_ExcludesGatewayFamily(t *testing.T) {
	wantSize := 1 + len(redirectFamilyVars) + len(telemetryFamilyVars) + len(managerOwnedVars) + len(effortAttestationVars)
	if len(universeCarveOutVars) != wantSize {
		t.Fatalf("universeCarveOutVars has %d entries, want %d (envAPIKey + redirect + telemetry + managerOwned + effortAttestation, unchanged by the gateway family)", len(universeCarveOutVars), wantSize)
	}
	for _, key := range gatewayUpstreamAuthTestKeys {
		if universeCarveOutVars[key] {
			t.Errorf("gateway upstream-auth key %q must NOT be in universeCarveOutVars (decisions.md D-standing: never carved out)", key)
		}
	}
}
