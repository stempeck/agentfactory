package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// gatewayModelsForLaunch is the checked-in codex shape: a gateway that authenticates, declares a
// main model and one class key, and leaves the other three to derivation. Distinct ids per rung
// so a derived value is distinguishable from a declared one by inspection.
func gatewayModelsForLaunch() *config.ModelsConfig {
	return &config.ModelsConfig{Models: map[string]map[string]string{
		"codex": {
			"ANTHROPIC_BASE_URL":            "http://localhost:4000",
			"ANTHROPIC_AUTH_TOKEN":          "tok",
			"ANTHROPIC_MODEL":               "gw-main-v1",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gw-haiku-v2",
		},
	}}
}

// TestLaunchLine_EndpointProfile_CarriesDerivedClassExports is the end-to-end pin for issue #598
// Phase 2: registry → ResolveModelEnv → SetModelEnv → launch line. Every layer above this one can
// be correct while the launch line still carries KEY=” for a class the gateway must serve, which
// is the incident shape — so the assertion that matters is made here, on the emitted command.
//
// internal/session may import internal/config (the import runs one way; config parses session.go
// as SOURCE for its parity test), so this test can drive the real resolver rather than a
// hand-built export slice.
func TestLaunchLine_EndpointProfile_CarriesDerivedClassExports(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)

	_, env, ok, err := config.ResolveModelEnv(gatewayModelsForLaunch(), "", "codex", "", "")
	if err != nil || !ok {
		t.Fatalf("fixture must resolve: ok=%v err=%v", ok, err)
	}
	mgr.SetModelEnv(env)
	mgr.SetModelKeyUniverse([]string{"ANTHROPIC_MODEL", "ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_DEFAULT_HAIKU_MODEL"})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	inline := mgr.BuildStartupCommand()

	for key, val := range map[string]string{
		"ANTHROPIC_SMALL_FAST_MODEL":     "gw-haiku-v2",
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "gw-main-v1",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "gw-main-v1",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL":  "gw-haiku-v2",
	} {
		if !strings.Contains(inline, " "+key+"='"+val+"'") {
			t.Errorf("the launch line must export %s=%s — the class the host would otherwise answer with its own claude-* id, which the gateway refuses (issue #598); got: %s", key, val, inline)
		}
		if hasUnsetToken(inline, key) || strings.Contains(inline, key+"=''") {
			t.Errorf("%s is emitted by this launch, so the hygiene pass must not also clear it; got: %s", key, inline)
		}
		if want := "SetEnvironment " + mgr.SessionID() + " " + key + "=" + val; !hasOp(fake.ops, want) {
			t.Errorf("the tmux twin must carry %q too, or `tmux show-environment` and the launch line disagree; ops=%v", want, fake.ops)
		}
	}

	// SUBAGENT is a redirectFamilyVars member that derivation never fills, so the correct state is
	// CLEARED, not exported — an "absent" assertion would be red for the wrong reason.
	if !strings.Contains(inline, "CLAUDE_CODE_SUBAGENT_MODEL=''") {
		t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL must be cleared, never derived onto the launch line (Decision 14); got: %s", inline)
	}
}

// TestLaunchLine_DirectProfile_ExportsNoDerivedClasses is DO-NOT-CHANGE #1 at the deepest seam:
// an Anthropic-direct launch line must be exactly what it was before the hook.
func TestLaunchLine_DirectProfile_ExportsNoDerivedClasses(t *testing.T) {
	mgr, _ := startMouseAgent(t, nil)

	cfg := &config.ModelsConfig{Models: map[string]map[string]string{
		"opus-5": {"ANTHROPIC_MODEL": "claude-opus-5"},
	}}
	_, env, ok, err := config.ResolveModelEnv(cfg, "", "opus-5", "", "")
	if err != nil || !ok {
		t.Fatalf("fixture must resolve: ok=%v err=%v", ok, err)
	}
	mgr.SetModelEnv(env)

	inline := mgr.BuildStartupCommand()
	if !strings.Contains(inline, " ANTHROPIC_MODEL='claude-opus-5'") {
		t.Fatalf("positive control failed: the direct profile's own model must still be exported; got: %s", inline)
	}
	for _, key := range []string{
		"ANTHROPIC_SMALL_FAST_MODEL",
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	} {
		if strings.Contains(inline, " "+key+"='claude-opus-5'") {
			t.Errorf("derivation is endpoint-gated: an Anthropic-direct launch must not gain %s; got: %s", key, inline)
		}
	}
}
