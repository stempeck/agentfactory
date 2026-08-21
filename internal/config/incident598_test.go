package config

import (
	"reflect"
	"sort"
	"testing"
)

// incident598Profile is the profile the incident actually ran on: the shape `./quickstart.sh
// --litellm` seeds (quickstart.sh:893-903) and the shape USING_AGENTFACTORY.md documents. It
// declares a main model and a small one, and nothing else — which is precisely why a
// /gpt-fable-review sub-agent spawn died with `Invalid model name passed in model=claude-fable-5`.
func docEnvValue(env []EnvVar, key string) (string, bool) {
	for _, e := range env {
		if e.Key == key {
			return e.Value, true
		}
	}
	return "", false
}

func incident598Profile() map[string]string {
	return map[string]string{
		"ANTHROPIC_BASE_URL":            "http://localhost:4000",
		"ANTHROPIC_AUTH_TOKEN":          "file:.agentfactory/secrets/litellm.key",
		"ANTHROPIC_API_KEY":             "",
		"ANTHROPIC_MODEL":               "gpt-4o",
		"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-4o-mini",
	}
}

func incident598Registry() *ModelsConfig {
	return &ModelsConfig{
		Default: "default",
		Models: map[string]map[string]string{
			"default": {"ANTHROPIC_MODEL": "claude-opus-5"},
			"codex":   incident598Profile(),
		},
	}
}

// incident598PreDesignEnv is what ResolveModelEnv emitted for that profile BEFORE this design
// landed: orderedEnv over the declared keys and nothing else — ANTHROPIC_MODEL first, the rest
// sorted (models.go:603-620).
//
// It is held here as a literal rather than produced by running anything, because it is no longer
// producible: Phase 2 wired the ladder into the one resolver, so there is no switch to turn off. The
// fable_increment_pr595 tests established this inverted-pin idiom — an old pin whose requirement
// moved, kept as the thing the new behaviour is measured AGAINST rather than deleted. Keeping it is
// what makes the delta below a demonstration instead of a claim.
var incident598PreDesignEnv = []EnvVar{
	{Key: "ANTHROPIC_MODEL", Value: "gpt-4o"},
	{Key: "ANTHROPIC_API_KEY", Value: ""},
	{Key: "ANTHROPIC_AUTH_TOKEN", Value: "file:.agentfactory/secrets/litellm.key"},
	{Key: "ANTHROPIC_BASE_URL", Value: "http://localhost:4000"},
	{Key: "ANTHROPIC_DEFAULT_HAIKU_MODEL", Value: "gpt-4o-mini"},
}

// TestIncident598_C11_CodexShapedProfileCoversEveryClass is the incident's own path at the config
// layer: a session launched on the profile from the incident asks for a class, and the class has to
// resolve to a model the operator chose rather than to the host's built-in claude-* id.
//
// The opus half is the one that killed the spawn. Under pre-design behaviour ANTHROPIC_DEFAULT_OPUS_MODEL
// was simply absent from the launch env, so the host fell back to its own claude-opus id and the
// gateway — whose model_list is closed — refused it.
func TestIncident598_C11_CodexShapedProfileCoversEveryClass(t *testing.T) {
	_, env, ok, err := ResolveModelEnv(incident598Registry(), "", "codex", "", "")
	if err != nil {
		t.Fatalf("resolving the incident's profile: %v", err)
	}
	if !ok {
		t.Fatal("the incident's profile resolved to nothing at all")
	}

	for _, key := range DerivedEndpointClassKeys() {
		label, _ := EndpointClassLabel(key)
		value, found := docEnvValue(env, key)
		if !found {
			t.Errorf("%s is absent from the launch env, so a %s-class request falls back to the host's built-in "+
				"claude-* id and the gateway refuses it — the incident, exactly\nemitted: %v", key, label, env)
			continue
		}
		if value == "" {
			t.Errorf("%s is emitted as \"\", which defers to the host just as an absent key does\nemitted: %v", key, env)
		}
	}

	// The sub-agent default is never derived (models.go:300-307). Filling it would override both a
	// per-invocation model parameter and a sub-agent definition's own frontmatter, routing every
	// deliberate cheap-tier spawn to the main model — a cost and quality inversion no operator chose,
	// and it would make the per-class keys above dead letters for exactly the traffic that died here.
	if _, found := docEnvValue(env, "CLAUDE_CODE_SUBAGENT_MODEL"); found {
		t.Errorf("CLAUDE_CODE_SUBAGENT_MODEL was filled by derivation; it is the operator's override alone\nemitted: %v", env)
	}

	// The fable half, and the reason the docs this phase writes exist. No env key covers the fable
	// class: ANTHROPIC_DEFAULT_FABLE_MODEL is not in the inventory (endpoint_class_parity_test.go
	// fails by name if it is added), it has no label, and nothing emits it. The class is answerable
	// only by a gateway alias, which is what `af config models check` demands and what the manual
	// now tells an operator to write.
	if _, found := docEnvValue(env, "ANTHROPIC_DEFAULT_FABLE_MODEL"); found {
		t.Errorf("ANTHROPIC_DEFAULT_FABLE_MODEL is emitted, but nothing in this repo honours it — it exists only in "+
			"session.redirectFamilyVars, where it is CLEARED. Emitting it would teach a coverage that does not "+
			"exist\nemitted: %v", env)
	}
	for _, key := range EndpointClassKeys {
		if key == "ANTHROPIC_DEFAULT_FABLE_MODEL" {
			t.Error("ANTHROPIC_DEFAULT_FABLE_MODEL joined EndpointClassKeys; the manual documents the fable class as " +
				"coverable only by a gateway alias, and that row now needs rewriting")
		}
	}
	if _, known := EndpointClassLabel("ANTHROPIC_DEFAULT_FABLE_MODEL"); known {
		t.Error("a fable class label now exists; the manual documents fable-class as the check's own label for a row " +
			"no profile key can satisfy")
	}
}

// TestIncident598_C11_FableAndOpusPathsDifferFromPreDesignGolden demonstrates the delta rather than
// asserting it: it computes what the design changed about the incident's profile and pins that set
// exactly.
//
// A test that only pinned today's output would still pass if derivation were deleted and the golden
// updated in the same sweep — which is how #600 shipped. Measuring against the retained pre-design
// golden is what makes deleting the behaviour visible.
func TestIncident598_C11_FableAndOpusPathsDifferFromPreDesignGolden(t *testing.T) {
	_, env, ok, err := ResolveModelEnv(incident598Registry(), "", "codex", "", "")
	if err != nil || !ok {
		t.Fatalf("resolving the incident's profile: ok=%v err=%v", ok, err)
	}

	before := map[string]string{}
	for _, e := range incident598PreDesignEnv {
		before[e.Key] = e.Value
	}
	after := map[string]string{}
	for _, e := range env {
		after[e.Key] = e.Value
	}

	var added []string
	for key := range after {
		if _, existed := before[key]; !existed {
			added = append(added, key)
		}
	}
	sort.Strings(added)

	// Exactly the three classes the ladder can reach from a main model that the incident's profile
	// left to the host. HAIKU is absent from this list because the profile declared it — the delta is
	// what derivation ADDED, not what the profile ended up carrying.
	wantAdded := []string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL",
		"ANTHROPIC_DEFAULT_SONNET_MODEL",
		"ANTHROPIC_SMALL_FAST_MODEL",
	}
	if !reflect.DeepEqual(added, wantAdded) {
		t.Errorf("the design's delta on the incident's profile is %v, want %v\npre-design: %v\npost-design: %v",
			added, wantAdded, incident598PreDesignEnv, env)
	}

	// Nothing the operator declared may move. Derivation fills gaps; a rung that overwrote a declared
	// value would silently re-route traffic the operator had already routed.
	for key, value := range before {
		got, present := after[key]
		if !present {
			t.Errorf("%s was emitted before the design and is gone now; derivation may only add", key)
			continue
		}
		if got != value {
			t.Errorf("%s changed from %q to %q; derivation may not overwrite a declared value", key, value, got)
		}
	}

	// And the ids the new keys carry are the operator's own, not invented ones — opus and sonnet from
	// the main model, small/background from the haiku model the profile declared (models.go:422-441).
	for key, want := range map[string]string{
		"ANTHROPIC_DEFAULT_OPUS_MODEL":   "gpt-4o",
		"ANTHROPIC_DEFAULT_SONNET_MODEL": "gpt-4o",
		"ANTHROPIC_SMALL_FAST_MODEL":     "gpt-4o-mini",
	} {
		if after[key] != want {
			t.Errorf("%s derived to %q, want %q", key, after[key], want)
		}
	}
}
