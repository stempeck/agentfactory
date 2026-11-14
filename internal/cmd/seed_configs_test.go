package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// newAFSourceWithConfigs builds on newAFSourceDir's go.mod convention (a module path
// containing "agentfactory" — the one validateAFSource accepts) and additionally
// populates <src>/.agentfactory/<name> for each entry in files, the resolved-clone
// shape baseConfigContent's resolved-source branch reads. Neither existing AF-source
// helper (newAFSourceDir, setupFormulaFactoryWithAFSource) populates this directory.
func newAFSourceWithConfigs(t *testing.T, files map[string]string) string {
	t.Helper()
	src := newAFSourceDir(t, nil, nil)
	cfgDir := filepath.Join(src, ".agentfactory")
	if err := os.MkdirAll(cfgDir, 0755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(cfgDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	return src
}

// --- baseConfigContent: four selection-outcome tests (AC #2) --------------------

func TestBaseConfigContent_ResolvedSourceRead(t *testing.T) {
	marker := []byte(`{"repos":["marker-resolved-source"]}` + "\n")
	src := newAFSourceWithConfigs(t, map[string]string{"dispatch.json": string(marker)})

	got, provenance, err := baseConfigContent("dispatch.json", src, false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !bytes.Equal(got, marker) {
		t.Errorf("content = %q, want %q (must read the RESOLVED clone, not the embed)", got, marker)
	}
	wantPath := filepath.Join(src, ".agentfactory", "dispatch.json")
	wantProvenance := "source clone: " + wantPath
	if provenance != wantProvenance {
		t.Errorf("provenance = %q, want %q", provenance, wantProvenance)
	}
}

func TestBaseConfigContent_NoSourceTree(t *testing.T) {
	for _, name := range []string{"dispatch.json", "startup.json", "models.json", "litellm.yaml"} {
		got, _, err := baseConfigContent(name, "/nonexistent/ignored", true)
		if !errors.Is(err, errNoSourceTree) {
			t.Errorf("%s: err = %v, want errNoSourceTree (nothing to derive it from)", name, err)
		}
		if got != nil {
			t.Errorf("%s: content = %q, want none", name, got)
		}
	}
	for _, name := range []string{"agents.json", "messaging.json", "factory.json"} {
		got, provenance, err := baseConfigContent(name, "/nonexistent/ignored", true)
		if err != nil {
			t.Fatalf("%s: unexpected error: %v", name, err)
		}
		if !json.Valid(got) {
			t.Errorf("%s: content %q is not valid JSON", name, got)
		}
		if provenance != "built-in" {
			t.Errorf("%s: provenance = %q, want %q", name, provenance, "built-in")
		}
	}
}

func TestBaseConfigContent_MissingUnderResolvedIsHardError(t *testing.T) {
	src := newAFSourceWithConfigs(t, nil) // .agentfactory/ exists, dispatch.json does not

	got, _, err := baseConfigContent("dispatch.json", src, false)
	if err == nil {
		t.Fatal("expected hard error for missing file under resolved source, got nil")
	}
	if got != nil {
		t.Errorf("expected nil content on hard error, got %q", got)
	}
	wantPath := filepath.Join(src, ".agentfactory", "dispatch.json")
	if !strings.Contains(err.Error(), wantPath) {
		t.Errorf("error %q does not name the missing path %q", err.Error(), wantPath)
	}
}

func TestBaseConfigContent_InvalidJSONUnderResolvedIsHardError(t *testing.T) {
	src := newAFSourceWithConfigs(t, map[string]string{"models.json": "{not valid json"})

	_, _, err := baseConfigContent("models.json", src, false)
	if err == nil {
		t.Fatal("expected hard error for malformed JSON under resolved source, got nil")
	}
}

// TestBaseConfigContent_LitellmYAMLNotJSONValidated pins that the JSON-syntax hard
// error only applies to the six JSON-named manifest members: litellm.yaml is YAML and
// must be readable through the resolved-source branch without being run through a
// JSON-validity check (decisions.md D2).
func TestBaseConfigContent_LitellmYAMLNotJSONValidated(t *testing.T) {
	yamlContent := "not: json\nthis: [is, yaml]\n"
	src := newAFSourceWithConfigs(t, map[string]string{"litellm.yaml": yamlContent})

	got, _, err := baseConfigContent("litellm.yaml", src, false)
	if err != nil {
		t.Fatalf("litellm.yaml must not be JSON-validated, got error: %v", err)
	}
	if string(got) != yamlContent {
		t.Errorf("content = %q, want %q", got, yamlContent)
	}
}

// --- sanitizeSeed: four rewrite tests + identity test (AC #2) -------------------

func TestSanitizeSeed_DispatchReposCleared(t *testing.T) {
	in := []byte(`{"repos":["some/repo"],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"agent":"x"}],"interval_seconds":300,"retry_after_seconds":1800}`)

	out, _, err := sanitizeSeed("dispatch.json", in, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	repos, ok := got["repos"].([]any)
	if !ok || len(repos) != 0 {
		t.Errorf("repos = %#v, want an empty array (not null, not omitted)", got["repos"])
	}
	// identity check: fields sanitizeSeed does not own must survive untouched.
	if got["trigger_label"] != "agentic" {
		t.Errorf("trigger_label mutated: %#v", got["trigger_label"])
	}
	if mappings, _ := got["mappings"].([]any); len(mappings) != 1 {
		t.Errorf("mappings mutated: %#v", got["mappings"])
	}
}

func TestSanitizeSeed_FactoryNameSetToBasename(t *testing.T) {
	in := []byte(`{"type":"factory","version":1,"name":"agentfactory","max_worktrees":8}`)
	factoryRoot := "/home/dev/projects/coolproject-42"

	out, _, err := sanitizeSeed("factory.json", in, factoryRoot)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if got["name"] != filepath.Base(factoryRoot) {
		t.Errorf("name = %#v, want %q", got["name"], filepath.Base(factoryRoot))
	}
	if _, present := got["git_identity"]; present {
		t.Errorf("git_identity must NOT be seeded (DO-NOT-CHANGE), got %#v", got["git_identity"])
	}
	if got["type"] != "factory" || got["version"] != float64(1) || got["max_worktrees"] != float64(8) {
		t.Errorf("unrelated fields mutated: %#v", got)
	}
}

// TestSanitizeSeed_ModelsAgentPinsDropped is the load-bearing regression test for the
// consensus-identified critical bug: the literal classifier predicate, applied without
// an endpoint-presence gate, would drop every plain-cloud pin alongside codex/lmstudio
// (decisions.md D1). "fable-implement" here has no ANTHROPIC_BASE_URL and MUST survive.
func TestSanitizeSeed_ModelsAgentPinsDropped(t *testing.T) {
	in := []byte(`{
		"default": "default",
		"models": {
			"codex":    {"ANTHROPIC_BASE_URL":"http://localhost:4000","ANTHROPIC_AUTH_TOKEN":"file:.agentfactory/secrets/litellm.key","ANTHROPIC_MODEL":"gpt-5.6-sol"},
			"lmstudio": {"ANTHROPIC_BASE_URL":"http://host.docker.internal:11434","ANTHROPIC_AUTH_TOKEN":"lmstudio","ANTHROPIC_MODEL":"qwen/qwen3.8-27b"},
			"sonnet-5": {"ANTHROPIC_MODEL":"claude-sonnet-5"}
		},
		"agents": {
			"gpt-fable-review": "codex",
			"gpt-rootcause-all": "codex",
			"some-lmstudio-agent": "lmstudio",
			"fable-implement": "sonnet-5"
		}
	}`)

	out, _, err := sanitizeSeed("models.json", in, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var cfg config.ModelsConfig
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output does not decode as ModelsConfig: %v", err)
	}
	for agent, model := range cfg.Agents {
		if model == "codex" || model == "lmstudio" {
			t.Errorf("agent %q still pinned to non-portable profile %q", agent, model)
		}
	}
	if got, ok := cfg.Agents["fable-implement"]; !ok || got != "sonnet-5" {
		t.Errorf("portable pin fable-implement->sonnet-5 was dropped (got %q, ok=%v) — "+
			"classifier likely applied the OR-test to a profile with no declared endpoint", got, ok)
	}
	// models (profile DEFINITIONS) must be untouched — only the agents MAP is targeted.
	if _, ok := cfg.Models["codex"]; !ok {
		t.Errorf("models.codex profile definition was removed — sanitizeSeed must only drop AGENTS-MAP pins, never MODELS profile definitions (DO-NOT-CHANGE)")
	}
	if _, ok := cfg.Models["lmstudio"]; !ok {
		t.Errorf("models.lmstudio profile definition was removed")
	}
}

// TestSanitizeSeed_ModelsPurePlainCloudPinSurvives isolates the case that catches the
// critical bug on its own: a profile with NO ANTHROPIC_BASE_URL at all (the "default"
// shape most live agent pins use) must never be treated as non-portable.
func TestSanitizeSeed_ModelsPurePlainCloudPinSurvives(t *testing.T) {
	in := []byte(`{"models":{"default":{"ANTHROPIC_MODEL":"claude-opus-4-8"}},"agents":{"manager":"default"}}`)

	out, _, err := sanitizeSeed("models.json", in, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var cfg config.ModelsConfig
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output does not decode as ModelsConfig: %v", err)
	}
	if _, ok := cfg.Agents["manager"]; !ok {
		t.Fatal("plain-cloud pin (no ANTHROPIC_BASE_URL) was dropped — classifier is over-broad; see decisions.md D1")
	}
}

// TestSanitizeSeed_ModelsNonLoopbackWithoutFileTokenAlsoDropped isolates the
// "!IsLoopbackEndpoint(base)" half of the OR from the "HasPrefix(token,\"file:\")" half:
// a profile that declares a non-loopback endpoint but a plain (non-file:) token must
// still drop, independent of the endpoint-presence-gate fix.
func TestSanitizeSeed_ModelsNonLoopbackWithoutFileTokenAlsoDropped(t *testing.T) {
	in := []byte(`{"models":{"lmstudio":{"ANTHROPIC_BASE_URL":"http://host.docker.internal:11434","ANTHROPIC_AUTH_TOKEN":"lmstudio","ANTHROPIC_MODEL":"m"}},"agents":{"some-lmstudio-agent":"lmstudio"}}`)

	out, _, err := sanitizeSeed("models.json", in, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var cfg config.ModelsConfig
	if err := json.Unmarshal(out, &cfg); err != nil {
		t.Fatalf("output does not decode as ModelsConfig: %v", err)
	}
	if _, ok := cfg.Agents["some-lmstudio-agent"]; ok {
		t.Error("non-loopback pin without a file: token was not dropped")
	}
}

func TestSanitizeSeed_IdentityForOtherNames(t *testing.T) {
	for _, name := range []string{"messaging.json", "startup.json", "litellm.yaml"} {
		in := []byte("arbitrary content for " + name)
		out, _, err := sanitizeSeed(name, in, t.TempDir())
		if err != nil {
			t.Errorf("%s: unexpected error: %v", name, err)
			continue
		}
		if !bytes.Equal(out, in) {
			t.Errorf("%s: content mutated, want byte-identical passthrough", name)
		}
	}
}

// --- T4: sanitizer-policy invariant test (AC #3) --------------------------------
//
// Each sanitization must also come back as a rewrite record: the records are the
// provenance seedStarterConfigs prints and appends to .config-seed.log.
func TestSanitizeSeed_T4SeededPolicyInvariant(t *testing.T) {
	factoryRoot := "/tmp/example-project-xyz"
	assertRecords := func(t *testing.T, got []string, want ...string) {
		t.Helper()
		if !slices.Equal(got, want) {
			t.Errorf("rewrite records = %q, want %q", got, want)
		}
	}

	t.Run("dispatch_repos_empty", func(t *testing.T) {
		in := []byte(`{"repos":["stempeck/agentfactory-pro"],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"agent":"x"}],"interval_seconds":300,"retry_after_seconds":1800}`)
		out, records, err := sanitizeSeed("dispatch.json", in, factoryRoot)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if repos, _ := got["repos"].([]any); len(repos) != 0 {
			t.Errorf("repos = %#v, want []", got["repos"])
		}
		assertRecords(t, records, "repos cleared (was: stempeck/agentfactory-pro)")
	})

	t.Run("factory_name_basename", func(t *testing.T) {
		in := []byte(`{"type":"factory","version":1,"name":"agentfactory","max_worktrees":8}`)
		out, records, err := sanitizeSeed("factory.json", in, factoryRoot)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(out, &got); err != nil {
			t.Fatal(err)
		}
		if got["name"] != filepath.Base(factoryRoot) {
			t.Errorf("name = %#v, want %q", got["name"], filepath.Base(factoryRoot))
		}
		assertRecords(t, records, `name set to "example-project-xyz" (was "agentfactory")`)
	})

	t.Run("models_no_codex_or_lmstudio_pin", func(t *testing.T) {
		in := []byte(`{"models":{"codex":{"ANTHROPIC_BASE_URL":"http://localhost:4000","ANTHROPIC_AUTH_TOKEN":"file:x","ANTHROPIC_MODEL":"m"},"lmstudio":{"ANTHROPIC_BASE_URL":"http://host.docker.internal:11434","ANTHROPIC_AUTH_TOKEN":"lmstudio","ANTHROPIC_MODEL":"m2"},"sonnet-5":{"ANTHROPIC_MODEL":"claude-sonnet-5"}},"agents":{"gpt-fable-review":"codex","some-lmstudio-agent":"lmstudio","fable-implement":"sonnet-5"}}`)
		out, records, err := sanitizeSeed("models.json", in, factoryRoot)
		if err != nil {
			t.Fatal(err)
		}
		var cfg config.ModelsConfig
		if err := json.Unmarshal(out, &cfg); err != nil {
			t.Fatal(err)
		}
		for agent, model := range cfg.Agents {
			if model == "codex" || model == "lmstudio" {
				t.Errorf("agent %q still pinned to %q", agent, model)
			}
		}
		if _, ok := cfg.Agents["fable-implement"]; !ok {
			t.Error("portable pin fable-implement->sonnet-5 dropped")
		}
		assertRecords(t, records, "agent pins dropped (gpt-fable-review, some-lmstudio-agent)")
	})

	// A record claims the seed changed something; a rewrite that changed nothing must not claim it.
	for label, c := range map[string]struct{ name, data string }{
		"dispatch_repos_already_empty_no_record":  {"dispatch.json", `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"agent":"x"}],"interval_seconds":300,"retry_after_seconds":1800}`},
		"factory_name_already_basename_no_record": {"factory.json", `{"type":"factory","version":1,"name":"example-project-xyz","max_worktrees":8}`},
		"models_every_pin_portable_no_record":     {"models.json", `{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-x"}},"agents":{"manager":"default"}}`},
	} {
		t.Run(label, func(t *testing.T) {
			_, records, err := sanitizeSeed(c.name, []byte(c.data), factoryRoot)
			if err != nil {
				t.Fatal(err)
			}
			assertRecords(t, records)
		})
	}
}

// --- AC #4: classifier reused, not re-implemented -------------------------------

func TestSanitizeSeed_ReusesIsLoopbackEndpoint(t *testing.T) {
	// Sanity probe pinning IsLoopbackEndpoint's own empty-string behavior, since
	// sanitizeSeed's models.json branch depends on this exact fact (decisions.md D1):
	// an absent ANTHROPIC_BASE_URL must never be treated as "non-loopback".
	if config.IsLoopbackEndpoint("") {
		t.Fatal("test assumption violated: IsLoopbackEndpoint(\"\") must be false")
	}
	if !config.IsLoopbackEndpoint("http://localhost:4000") {
		t.Fatal("test assumption violated: IsLoopbackEndpoint(loopback) must be true")
	}
}

// --- validateSeed: Phase 3 (CFG-4) seed-validation pass -------------------------
//
// validateSeed(name, data, agents, agentsAreFresh, warn) runs the strict-decode +
// cross-file coherence pass on one sanitized seed. agents is the reference roster
// dispatch/startup/messaging refs are checked against; agentsAreFresh selects the
// severity per decisions.md D2 — true (this agents.json is itself part of the
// current seed batch) makes a dangling reference a hard refusal, false (agents.json
// is pre-existing on disk) makes it a warning written to warn instead. Phase 3 has
// no live caller (Phase 4 wires the seam) — these tests call the function directly,
// per IMPLREADME_PHASE3.md L205-207, L329-333.
//
// twoAgentRoster mirrors setupConfigFactory's ("debugger","manager") shape
// (config_set_test.go:16-30) — the design doc's own named "pre-existing 2-agent
// agents.json" scenario (six_sigma_gaps.md Gap 5).
func twoAgentRoster() *config.AgentConfig {
	return &config.AgentConfig{Agents: map[string]config.AgentEntry{
		"debugger": {Type: "autonomous", Description: "d"},
		"manager":  {Type: "interactive", Description: "m"},
	}}
}

// --- Group 1: strict decode (unknown-key reject + JSON-name scoping) -----------

func TestSeedValidation_StrictDecodeRejectsUnknownTopLevelKey(t *testing.T) {
	agents := twoAgentRoster()
	cases := []struct {
		name string
		data string
	}{
		{"dispatch.json", `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800,"bogus_field":true}`},
		{"startup.json", `{"agents":["manager"],"quality":"high","fidelity":"strict","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":[],"bogus_field":true}`},
		{"messaging.json", `{"groups":{"all":["manager"]},"bogus_field":true}`},
		{"models.json", `{"default":"default","models":{},"agents":{},"bogus_field":true}`},
		{"factory.json", `{"type":"factory","version":1,"name":"x","bogus_field":true}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			err := validateSeed(c.name, []byte(c.data), agents, true, io.Discard)
			if err == nil {
				t.Fatalf("expected unknown-field rejection for %s, got nil", c.name)
			}
			if !strings.Contains(err.Error(), c.name) {
				t.Errorf("error %q does not name the seed %q", err.Error(), c.name)
			}
			if !strings.Contains(err.Error(), "bogus_field") {
				t.Errorf("error %q does not name the unknown key", err.Error())
			}
		})
	}
}

func TestSeedValidation_ValidSeedNoFalsePositive(t *testing.T) {
	agents := twoAgentRoster()
	cases := []struct {
		name string
		data string
	}{
		{"dispatch.json", `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`},
		{"startup.json", `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":[]}`},
		{"messaging.json", `{"groups":{"all":["manager"]}}`},
		{"models.json", `{"default":"default","models":{"default":{}},"agents":{}}`},
		{"factory.json", `{"type":"factory","version":1,"name":"x"}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf bytes.Buffer
			if err := validateSeed(c.name, []byte(c.data), agents, true, &buf); err != nil {
				t.Fatalf("unexpected error on a clean %s: %v", c.name, err)
			}
			if buf.Len() != 0 {
				t.Errorf("unexpected warning on a clean %s: %q", c.name, buf.String())
			}
		})
	}
}

func TestSeedValidation_LitellmYAMLBypassesStrictDecode(t *testing.T) {
	yamlContent := "not: json\nthis: [is, yaml]\n"
	var buf bytes.Buffer
	if err := validateSeed("litellm.yaml", []byte(yamlContent), twoAgentRoster(), true, &buf); err != nil {
		t.Fatalf("litellm.yaml must bypass strict-decode entirely, got error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning for litellm.yaml: %q", buf.String())
	}
}

// --- Group 2: agent-ref cross-check, fresh-set branch (AC #3 scenario 1) -------

func TestSeedValidation_DanglingAgentRef_FreshSet_HardFails(t *testing.T) {
	agents := twoAgentRoster() // debugger, manager

	t.Run("startup_watchdog_agents", func(t *testing.T) {
		data := `{"agents":[],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":["ghost-watchdog"]}`
		err := validateSeed("startup.json", []byte(data), agents, true, io.Discard)
		if err == nil {
			t.Fatal("expected refusal for dangling watchdog_agents ref, got nil")
		}
		if !strings.Contains(err.Error(), "startup.json") || !strings.Contains(err.Error(), "ghost-watchdog") {
			t.Errorf("error %q does not name file+ref", err.Error())
		}
	})

	t.Run("dispatch_mapping_agent", func(t *testing.T) {
		data := `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"agent":"ghost"}],"interval_seconds":300,"retry_after_seconds":1800}`
		err := validateSeed("dispatch.json", []byte(data), agents, true, io.Discard)
		if err == nil {
			t.Fatal("expected refusal for dangling mapping agent ref, got nil")
		}
		if !strings.Contains(err.Error(), "dispatch.json") || !strings.Contains(err.Error(), "ghost") {
			t.Errorf("error %q does not name file+ref", err.Error())
		}
	})

	t.Run("messaging_group_member", func(t *testing.T) {
		data := `{"groups":{"all":["ghost-member"]}}`
		err := validateSeed("messaging.json", []byte(data), agents, true, io.Discard)
		if err == nil {
			t.Fatal("expected refusal for dangling group member ref, got nil")
		}
		if !strings.Contains(err.Error(), "messaging.json") || !strings.Contains(err.Error(), "ghost-member") {
			t.Errorf("error %q does not name file+ref", err.Error())
		}
	})
}

func TestSeedValidation_KnownAgentRefsPassClean(t *testing.T) {
	agents := twoAgentRoster()

	t.Run("startup_watchdog_agents", func(t *testing.T) {
		data := `{"agents":[],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":["manager"]}`
		if err := validateSeed("startup.json", []byte(data), agents, true, io.Discard); err != nil {
			t.Errorf("unexpected refusal for a known watchdog agent: %v", err)
		}
	})
	t.Run("dispatch_mapping_agent", func(t *testing.T) {
		data := `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"agent":"debugger"}],"interval_seconds":300,"retry_after_seconds":1800}`
		if err := validateSeed("dispatch.json", []byte(data), agents, true, io.Discard); err != nil {
			t.Errorf("unexpected refusal for a known mapping agent: %v", err)
		}
	})
	t.Run("messaging_group_member", func(t *testing.T) {
		data := `{"groups":{"all":["manager","debugger"]}}`
		if err := validateSeed("messaging.json", []byte(data), agents, true, io.Discard); err != nil {
			t.Errorf("unexpected refusal for known group members: %v", err)
		}
	})
}

// --- Group 4: mixed-provenance policy, pre-existing branch (AC #3 scenario 3) --

func TestSeedValidation_PreExistingAgentsJson_IncoherenceWarnsNotFails(t *testing.T) {
	agents := twoAgentRoster() // pre-existing 2-agent roster, per six_sigma_gaps.md Gap 5

	t.Run("startup_rich_seed_warns", func(t *testing.T) {
		data := `{"agents":[],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":["watchdog-01","watchdog-02"]}`
		var buf bytes.Buffer
		if err := validateSeed("startup.json", []byte(data), agents, false, &buf); err != nil {
			t.Fatalf("pre-existing incoherence must warn, not fail: %v", err)
		}
		if !strings.Contains(buf.String(), "startup.json") {
			t.Errorf("warning %q does not name the seed", buf.String())
		}
		if !strings.Contains(buf.String(), "watchdog-01") && !strings.Contains(buf.String(), "watchdog-02") {
			t.Errorf("warning %q does not name an unresolvable watchdog agent", buf.String())
		}
	})

	t.Run("dispatch_rich_seed_warns", func(t *testing.T) {
		data := `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"agent":"ghost-agent"}],"interval_seconds":300,"retry_after_seconds":1800}`
		var buf bytes.Buffer
		if err := validateSeed("dispatch.json", []byte(data), agents, false, &buf); err != nil {
			t.Fatalf("pre-existing incoherence must warn, not fail: %v", err)
		}
		if !strings.Contains(buf.String(), "dispatch.json") || !strings.Contains(buf.String(), "ghost-agent") {
			t.Errorf("warning %q does not name file+ref", buf.String())
		}
	})

	t.Run("messaging_rich_seed_warns", func(t *testing.T) {
		data := `{"groups":{"all":["ghost-member"]}}`
		var buf bytes.Buffer
		if err := validateSeed("messaging.json", []byte(data), agents, false, &buf); err != nil {
			t.Fatalf("pre-existing incoherence must warn, not fail: %v", err)
		}
		if !strings.Contains(buf.String(), "messaging.json") || !strings.Contains(buf.String(), "ghost-member") {
			t.Errorf("warning %q does not name file+ref", buf.String())
		}
	})
}

func TestSeedValidation_PreExistingAgentsJson_CoherentSeedIsSilent(t *testing.T) {
	agents := twoAgentRoster()
	data := `{"agents":[],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":["manager"]}`
	var buf bytes.Buffer
	if err := validateSeed("startup.json", []byte(data), agents, false, &buf); err != nil {
		t.Fatalf("unexpected refusal for a coherent pre-existing pairing: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning for a coherent pre-existing pairing: %q", buf.String())
	}
}

// --- Group 5: secret-lint (models.json) ----------------------------------------

func TestSeedValidation_SecretLint_NonPlaceholderTokenWarns(t *testing.T) {
	data := `{"default":"p","models":{"p":{"ANTHROPIC_AUTH_TOKEN":"plaintext-abc123"}},"agents":{}}`
	var buf bytes.Buffer
	if err := validateSeed("models.json", []byte(data), twoAgentRoster(), true, &buf); err != nil {
		t.Fatalf("secret-lint is warn-only, must never fail: %v", err)
	}
	if !strings.Contains(buf.String(), "p") || !strings.Contains(buf.String(), authTokenKey) {
		t.Errorf("warning %q does not name the profile and %s", buf.String(), authTokenKey)
	}
}

func TestSeedValidation_SecretLint_FileRefSilent(t *testing.T) {
	data := `{"default":"default","models":{"default":{},"p":{"ANTHROPIC_AUTH_TOKEN":"file:.agentfactory/secrets/litellm.key"}},"agents":{}}`
	var buf bytes.Buffer
	if err := validateSeed("models.json", []byte(data), twoAgentRoster(), true, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning for a file: reference: %q", buf.String())
	}
}

func TestSeedValidation_SecretLint_EmptyTokenSilent(t *testing.T) {
	data := `{"default":"p","models":{"p":{"ANTHROPIC_AUTH_TOKEN":""}},"agents":{}}`
	var buf bytes.Buffer
	if err := validateSeed("models.json", []byte(data), twoAgentRoster(), true, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning for an empty token: %q", buf.String())
	}
}

// TestSeedValidation_SecretLint_KnownPlaceholderLmstudioSilent pins decisions.md D5:
// the tracked seed's own lmstudio profile carries a literal, non-file: auth token
// ("lmstudio") that the binding IMPLREADME's "non-placeholder" carve-out exempts from
// the warning — otherwise every fresh `af install --init` using the shipped seed would
// warn on its own default profile.
func TestSeedValidation_SecretLint_KnownPlaceholderLmstudioSilent(t *testing.T) {
	data := `{"default":"default","models":{"default":{},"lmstudio":{"ANTHROPIC_API_KEY":"","ANTHROPIC_AUTH_TOKEN":"lmstudio","ANTHROPIC_BASE_URL":"http://host.docker.internal:11434","ANTHROPIC_MODEL":"qwen/qwen3.8-27b"}},"agents":{}}`
	var buf bytes.Buffer
	if err := validateSeed("models.json", []byte(data), twoAgentRoster(), true, &buf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("known placeholder %q must not warn (decisions.md D5): %q", "lmstudio", buf.String())
	}
}
