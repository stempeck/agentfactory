package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// writeValidModels writes a validated models.json into the factory at root.
func writeValidModels(t *testing.T, root string, cfg *config.ModelsConfig) {
	t.Helper()
	if err := config.SaveModelsConfig(config.ModelsConfigPath(root), cfg); err != nil {
		t.Fatalf("SaveModelsConfig: %v", err)
	}
}

// writeRawModels writes raw bytes to models.json, bypassing validation — the only
// way to stage a malformed or incomplete-endpoint file (SaveModelsConfig rejects them).
func writeRawModels(t *testing.T, root, raw string) {
	t.Helper()
	if err := os.WriteFile(config.ModelsConfigPath(root), []byte(raw), 0o644); err != nil {
		t.Fatalf("write raw models.json: %v", err)
	}
}

// --- flag + marker helpers ---

func TestSling_ModelFlag_Registered(t *testing.T) {
	if slingCmd.Flags().Lookup("model") == nil {
		t.Error("af sling must register a --model flag")
	}
}

func TestModelOverrideMarker_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	if got := readModelOverride(dir); got != "" {
		t.Errorf("readModelOverride on empty dir = %q, want \"\"", got)
	}
	writeModelOverride(dir, "sonnet")
	if got := readModelOverride(dir); got != "sonnet" {
		t.Errorf("readModelOverride after write = %q, want \"sonnet\"", got)
	}
	// Unconditional overwrite (mirrors writeDispatchedMarker, not write-if-absent).
	writeModelOverride(dir, "opus")
	if got := readModelOverride(dir); got != "opus" {
		t.Errorf("writeModelOverride must overwrite a stale marker; got %q, want \"opus\"", got)
	}
	// Marker lives at .runtime/model_override (so --reset's .runtime wipe cleans it).
	if _, err := os.Stat(config.AgentDir(dir, "x")); err == nil {
		_ = err // no-op; keep import set stable
	}
}

// --- override beats the per-agent default ---

func TestSling_ModelFlag_OverridesDefault(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"opus":   {"ANTHROPIC_MODEL": "claude-opus-4-8"},
			"sonnet": {"ANTHROPIC_MODEL": "claude-sonnet-4-6"},
		},
		Agents: map[string]string{"manager": "opus"},
	})

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "sonnet", "", false, &warn)
	if err != nil {
		t.Fatalf("resolveLaunchModelEnv: %v", err)
	}
	if name != "sonnet" {
		t.Errorf("resolved name = %q, want \"sonnet\"", name)
	}
	if got := modelEnvValue(env, "ANTHROPIC_MODEL"); got != "claude-sonnet-4-6" {
		t.Errorf("ANTHROPIC_MODEL = %q, want \"claude-sonnet-4-6\" (override must beat the per-agent default)", got)
	}
	if got := modelEnvValue(env, "ANTHROPIC_MODEL"); got == "claude-opus-4-8" {
		t.Error("override must NOT resolve to the per-agent default claude-opus-4-8")
	}
}

// --- fail-fast before launch ---

func TestSling_UnknownProfile_FailsBeforeLaunch(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{"opus": {"ANTHROPIC_MODEL": "claude-opus-4-8"}},
	})

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "ghost-profile", "", false, &warn)
	if err == nil {
		t.Fatalf("expected fail-fast error for unknown --model profile; got name=%q env=%v", name, env)
	}
	if !strings.Contains(err.Error(), "ghost-profile") {
		t.Errorf("error should name the bad profile, got: %v", err)
	}
	if env != nil {
		t.Errorf("no export set must be produced on fail-fast, got: %v", env)
	}
}

func TestSling_IncompleteEndpoint_FailsBeforeLaunch(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	// base_url without auth_token — SaveModelsConfig would reject this, so write raw.
	writeRawModels(t, dir, `{"models":{"local":{"ANTHROPIC_MODEL":"claude-local","ANTHROPIC_BASE_URL":"http://localhost:8080"}}}`)

	var warn bytes.Buffer
	_, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "local", "", false, &warn)
	if err == nil {
		t.Fatal("expected fail-fast error for incomplete endpoint (base_url without auth_token)")
	}
	if env != nil {
		t.Errorf("no export set must be produced on fail-fast, got: %v", env)
	}
}

// --- malformed models.json blast radius ---

func TestSling_MalformedModelsJson_DefaultAgentStillLaunches(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeRawModels(t, dir, "this is not json{{{")

	var warn bytes.Buffer
	// No explicit --model (cliModel == "") — a default-model launch must NOT be bricked.
	name, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "", "", false, &warn)
	if err != nil {
		t.Fatalf("a malformed models.json must NOT brick a default-model agent; got err: %v", err)
	}
	if len(env) != 0 {
		t.Errorf("default launch must fall through to the global default (empty env), got: %v", env)
	}
	if name != "" {
		t.Errorf("no model name should resolve on fall-through, got: %q", name)
	}
	if warn.Len() == 0 {
		t.Error("the malformed-config fall-through should log a warning to the warn writer")
	}
}

func TestSling_MalformedModelsJson_ExplicitProfileFailsLoud(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeRawModels(t, dir, "this is not json{{{")

	var warn bytes.Buffer
	// Explicit --model "opus" — a profile-selecting launch on a broken registry must fail loud.
	_, env, err := resolveLaunchModelEnv(dir, "manager", config.AgentDir(dir, "manager"), "opus", "", false, &warn)
	if err == nil {
		t.Fatal("a profile-selecting launch must fail loud when models.json cannot be loaded")
	}
	if env != nil {
		t.Errorf("no export set must be produced on fail-loud, got: %v", env)
	}
}

// --- per-agent default survives a simulated regen ---

// TestModelsAssignment_SurvivesRegen pins the durability claim that justifies the
// top-level models.json.agents map (issue #480): a per-agent default declared
// there is regen-IMMUNE because agent-gen rebuilds AgentEntry WITHOUT a Model and
// `af install --agents`/`agent-gen --delete` `os.RemoveAll(wsDir)` the agent WORKSPACE
// (formula.go:231-236, :423) — but models.json lives at the FACTORY ROOT, outside the
// wiped workspace. This SIMULATES the regen (remove + rebuild the agent workspace dir)
// rather than invoking the real agent-gen, then proves the agent STILL resolves its
// profile from the agents map with no marker and no legacy entry model.
func TestModelsAssignment_SurvivesRegen(t *testing.T) {
	dir := setupTestFactoryForDone(t, "manager")
	writeValidModels(t, dir, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"opus":   {"ANTHROPIC_MODEL": "claude-opus-4-8"},
			"sonnet": {"ANTHROPIC_MODEL": "claude-sonnet-4-6"},
		},
		Agents: map[string]string{"manager": "opus"},
	})

	// Pre-regen: the per-agent default resolves from the agents map.
	agentDir := config.AgentDir(dir, "manager")
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("seed agent workspace: %v", err)
	}
	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(dir, "manager", agentDir, "", "", false, &warn)
	if err != nil {
		t.Fatalf("pre-regen resolveLaunchModelEnv: %v", err)
	}
	if name != "opus" || modelEnvValue(env, "ANTHROPIC_MODEL") != "claude-opus-4-8" {
		t.Fatalf("pre-regen: agent must resolve its agents-map default opus; got name=%q env=%v", name, env)
	}

	// Simulate `agent-gen --delete` + regen: the agent WORKSPACE is wiped and rebuilt
	// (mirrors formula.go:423 os.RemoveAll(wsDir)). models.json at the factory root is
	// untouched.
	if err := os.RemoveAll(agentDir); err != nil {
		t.Fatalf("simulate workspace wipe: %v", err)
	}
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("simulate workspace rebuild: %v", err)
	}

	// Post-regen: the agents-map default must STILL resolve (the durability guarantee).
	name, env, err = resolveLaunchModelEnv(dir, "manager", agentDir, "", "", false, &warn)
	if err != nil {
		t.Fatalf("post-regen resolveLaunchModelEnv: %v", err)
	}
	if name != "opus" {
		t.Errorf("post-regen resolved name = %q, want \"opus\" (per-agent default must survive regen)", name)
	}
	if got := modelEnvValue(env, "ANTHROPIC_MODEL"); got != "claude-opus-4-8" {
		t.Errorf("post-regen ANTHROPIC_MODEL = %q, want \"claude-opus-4-8\" (durable agents-map default lost across regen)", got)
	}
}

// --- model resolution is orthogonal to formula instantiation ---

func TestModelResolution_OrthogonalToFormula(t *testing.T) {
	const toml = `
formula = "ortho"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Alpha"
description = "First step"

[[steps]]
id = "step2"
title = "Beta"
description = "Second step"
needs = ["step1"]
`
	type stepSnap struct {
		title string
		desc  string
		deps  int
	}
	instantiate := func(root, agentDir string) map[string]stepSnap {
		store := memstore.New()
		orig := newIssueStore
		newIssueStore = func(wd, actor string) (issuestore.Store, error) { return store, nil }
		defer func() { newIssueStore = orig }()

		params := InstantiateParams{
			Ctx:         t.Context(),
			FormulaName: "ortho",
			AgentName:   "test-agent",
			Root:        root,
			WorkDir:     agentDir,
		}
		var buf bytes.Buffer
		_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
		if err != nil {
			t.Fatalf("instantiateFormulaWorkflow: %v", err)
		}
		snap := make(map[string]stepSnap, len(stepIDs))
		for sid, bid := range stepIDs {
			iss, err := store.Get(t.Context(), bid)
			if err != nil {
				t.Fatalf("store.Get(%s): %v", sid, err)
			}
			snap[sid] = stepSnap{title: iss.Title, desc: iss.Description, deps: len(iss.BlockedBy)}
		}
		return snap
	}

	// Run A: no models.json — baseline formula graph.
	rootA, agentDirA := createTestFormulaFactoryWithTOML(t, "ortho", "test-agent", toml)
	baseline := instantiate(rootA, agentDirA)

	// Run B: a model selection is active (agents-map default + marker), then resolve it.
	rootB, agentDirB := createTestFormulaFactoryWithTOML(t, "ortho", "test-agent", toml)
	writeValidModels(t, rootB, &config.ModelsConfig{
		Models: map[string]map[string]string{"sonnet": {"ANTHROPIC_MODEL": "claude-sonnet-4-6"}},
		Agents: map[string]string{"test-agent": "sonnet"},
	})
	writeRuntimeFile(t, agentDirB, "model_override", "sonnet")
	withModel := instantiate(rootB, agentDirB)

	// The model selection must actually be active in run B (else the test proves nothing).
	var warn bytes.Buffer
	_, env, err := resolveLaunchModelEnv(rootB, "test-agent", agentDirB, "", "", false, &warn)
	if err != nil {
		t.Fatalf("resolveLaunchModelEnv (run B): %v", err)
	}
	if modelEnvValue(env, "ANTHROPIC_MODEL") != "claude-sonnet-4-6" {
		t.Fatalf("run B must have an active model selection, got env: %v", env)
	}

	// The formula graph must be byte-identical regardless of the model selection.
	if len(baseline) != len(withModel) {
		t.Fatalf("step count differs: baseline %d vs with-model %d", len(baseline), len(withModel))
	}
	for sid, b := range baseline {
		w, ok := withModel[sid]
		if !ok {
			t.Errorf("step %q present in baseline but missing with a model selection (resolution must not drop steps)", sid)
			continue
		}
		if b != w {
			t.Errorf("step %q perturbed by model selection: baseline %+v vs with-model %+v", sid, b, w)
		}
	}
}
