package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// extractScaffoldLiteral returns the backtick-delimited raw-string value that
// follows key (e.g. `"startup.json":`) in the install.go source. The starterConfigs
// scaffold JSON literals contain no internal backticks, so "first backtick after
// key → next backtick" is an exact extraction. Reading the REAL literal (rather
// than a copy) keeps the test drift-proof without exposing any production helper —
// the same hermetic os.ReadFile("install.go") idiom as install_paths_test.go.
func extractScaffoldLiteral(t *testing.T, src, key string) string {
	t.Helper()
	ki := strings.Index(src, key)
	if ki < 0 {
		t.Fatalf("scaffold key %q not found in install.go", key)
	}
	rest := src[ki+len(key):]
	open := strings.IndexByte(rest, '`')
	if open < 0 {
		t.Fatalf("no opening backtick after %q in install.go", key)
	}
	rest = rest[open+1:]
	end := strings.IndexByte(rest, '`')
	if end < 0 {
		t.Fatalf("no closing backtick after %q literal in install.go", key)
	}
	return rest[:end]
}

// TestInstallScaffold_WatchdogAgentsAreSeededAgents pins the N6 parity guarantee
// (issue #408 Phase 4 / AC-4): every name in the install scaffold's startup.json
// watchdog_agents is a real key in the scaffold agents.json — so a fresh
// `af install --init` + `af up` yields a FUNCTIONAL scoped watchdog (no
// unknown-agent warning, no refuse-on-boot). This is the regression guard the
// `mergepatrol`-class bug (a watchdog_agents name that is not a real agent) lacked.
//
// Unit-tier constraint (AC-4): runInstallInit spawns the Python MCP server and is
// gated to the integration tier, so it cannot write the real scaffold in a unit
// test. Instead this source-parses the two scaffold literals straight out of
// install.go (the established os.ReadFile("install.go") idiom), reading the REAL
// literals rather than a copy — hermetic AND drift-proof.
func TestInstallScaffold_WatchdogAgentsAreSeededAgents(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	src := string(data)

	agentsLiteral := extractScaffoldLiteral(t, src, `"agents.json":`)
	startupLiteral := extractScaffoldLiteral(t, src, `"startup.json":`)

	var agents config.AgentConfig
	if err := json.Unmarshal([]byte(agentsLiteral), &agents); err != nil {
		t.Fatalf("unmarshal scaffold agents.json literal: %v\nliteral: %s", err, agentsLiteral)
	}
	var startup config.StartupConfig
	if err := json.Unmarshal([]byte(startupLiteral), &startup); err != nil {
		t.Fatalf("unmarshal scaffold startup.json literal: %v\nliteral: %s", err, startupLiteral)
	}

	// The scaffold must ship a FUNCTIONAL watchdog, not an empty refuse-on-boot one.
	if len(startup.WatchdogAgents) == 0 {
		t.Fatalf("scaffold startup.json must seed a non-empty watchdog_agents; got %v", startup.WatchdogAgents)
	}

	known := make([]string, 0, len(agents.Agents))
	for name := range agents.Agents {
		known = append(known, name)
	}

	// N6 parity: every watchdog_agents name must be a real seeded agent. Membership
	// is map-key existence in agents.json (production's agentsCfg.Agents[name] idiom),
	// NOT session existence — the boundary R1-L1 / R2-H1 both turn on.
	for _, name := range startup.WatchdogAgents {
		if _, ok := agents.Agents[name]; !ok {
			t.Errorf("scaffold watchdog_agents %q is NOT a seeded agents.json agent (the mergepatrol-class bug); seeded agents = %v",
				name, known)
		}
	}
}

// TestInstallScaffold_StartupAgentsDefaultManagerOnly pins T1/T3 (PR #410): a fresh
// `af install --init` + `af up` must auto-start ONLY the interactive manager, NOT the
// autonomous supervisor. Issue #408 intended ONLY the watchdog_agents widening; the
// scaffold's startup.json `agents` default must stay manager-only (commit 472f64a set
// it deliberately). This source-parses the REAL install.go literal — not a copy — so a
// future re-widening of `agents` cannot slip in silently (the gap that let it in).
func TestInstallScaffold_StartupAgentsDefaultManagerOnly(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	startupLiteral := extractScaffoldLiteral(t, string(data), `"startup.json":`)

	var startup config.StartupConfig
	if err := json.Unmarshal([]byte(startupLiteral), &startup); err != nil {
		t.Fatalf("unmarshal scaffold startup.json literal: %v\nliteral: %s", err, startupLiteral)
	}

	// T1/T3: default startup scope is manager-only.
	if !reflect.DeepEqual(startup.Agents, []string{"manager"}) {
		t.Errorf("scaffold startup.json agents = %v, want [manager] (T1/T3: no unapproved supervisor auto-start)", startup.Agents)
	}
	// Regression guard: the intended #408 watchdog_agents fix must be PRESERVED, not reverted.
	if !reflect.DeepEqual(startup.WatchdogAgents, []string{"manager", "supervisor"}) {
		t.Errorf("scaffold watchdog_agents = %v, want [manager supervisor] (keep the #408 fix)", startup.WatchdogAgents)
	}
}

// TestInstallScaffold_ModelsJsonSeeded pins the models.json seed (issue #480): a fresh
// `af install --init` must seed a models.json that PASSES ITS OWN VALIDATOR — because
// LoadModelsConfig runs validateModelsConfig on every launch, a seed with an incomplete
// endpoint (base_url without auth_token) or a non-empty ANTHROPIC_API_KEY would brick
// the next launch / teach the broken local-profile pattern. This source-parses the REAL
// install.go literal and round-trips it through the REAL LoadModelsConfig (not a grep),
// then asserts the lmstudio profile ships a complete local-endpoint set — with the
// explicit ANTHROPIC_API_KEY clear and a genuinely local model id.
func TestInstallScaffold_ModelsJsonSeeded(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	modelsLiteral := extractScaffoldLiteral(t, string(data), `"models.json":`)

	// Round-trip through the REAL loader/validator: write the seed to a temp
	// factory and LoadModelsConfig must return nil error.
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(config.ModelsConfigPath(root), []byte(modelsLiteral), 0o644); err != nil {
		t.Fatalf("write seed models.json: %v", err)
	}
	loaded, err := config.LoadModelsConfig(root)
	if err != nil {
		t.Fatalf("seed models.json must pass its own validator; LoadModelsConfig: %v\nliteral: %s", err, modelsLiteral)
	}

	// The default profile must exist; its model id is asserted against quickstart.sh
	// by TestInstallScaffold_DefaultModel_MatchesQuickstart, not pinned here.
	if _, ok := loaded.Models["default"]; !ok {
		t.Fatalf("seed must define a `default` profile; got models=%v", loaded.Models)
	}

	// lmstudio profile ships the COMPLETE local-endpoint set: base_url + auth_token
	// + ANTHROPIC_MODEL + the ANTHROPIC_API_KEY:"" clear.
	lm, ok := loaded.Models["lmstudio"]
	if !ok {
		t.Fatalf("seed must define an `lmstudio` example profile; got models=%v", loaded.Models)
	}
	if lm["ANTHROPIC_BASE_URL"] == "" {
		t.Error("lmstudio profile must set a non-empty ANTHROPIC_BASE_URL")
	}
	if lm["ANTHROPIC_AUTH_TOKEN"] == "" {
		t.Error("lmstudio profile must set a non-empty ANTHROPIC_AUTH_TOKEN (base_url requires auth_token)")
	}
	// The local-endpoint example must name a model an LM Studio server can actually
	// serve — a cloud id here means copying the example verbatim yields an agent
	// requesting a nonexistent model (PR #482 review).
	if got := lm["ANTHROPIC_MODEL"]; got != "qwen2.5-coder-32b" {
		t.Errorf("lmstudio profile ANTHROPIC_MODEL = %q, want the local id %q", got, "qwen2.5-coder-32b")
	}
	if v, ok := lm["ANTHROPIC_API_KEY"]; !ok || v != "" {
		t.Errorf("lmstudio profile must ship ANTHROPIC_API_KEY:\"\" (explicit key-clear); present=%v val=%q", ok, v)
	}

	// The durable per-agent home must be present (even if empty) for
	// `af config models set` to write into.
	if loaded.Agents == nil {
		t.Error("seed should ship an `agents` map (the regen-immune per-agent default home)")
	}
}

// TestInstallScaffold_DefaultModel_MatchesQuickstart pins install-seed ↔ quickstart
// consistency: a fresh `af install --init` and a quickstart.sh bootstrap must yield the
// SAME default model. The expected id is parsed from quickstart.sh's own
// ${ANTHROPIC_MODEL:-...} fallback — never hardcoded — so a future quickstart model bump
// fails here until the seed follows. The drift this guards against actually happened:
// quickstart.sh moved to claude-fable-5 while the seed stayed on claude-opus-4-8, and a
// literal-vs-literal assertion kept CI green (PR #482 review).
func TestInstallScaffold_DefaultModel_MatchesQuickstart(t *testing.T) {
	qs, err := os.ReadFile("../../quickstart.sh")
	if err != nil {
		t.Fatalf("read quickstart.sh: %v", err)
	}
	m := regexp.MustCompile(`\$\{ANTHROPIC_MODEL:-([^}]+)\}`).FindSubmatch(qs)
	if m == nil {
		t.Fatal("quickstart.sh no longer contains an ${ANTHROPIC_MODEL:-...} default — update this parity test alongside it")
	}
	want := string(m[1])

	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	modelsLiteral := extractScaffoldLiteral(t, string(data), `"models.json":`)
	var seed config.ModelsConfig
	if err := json.Unmarshal([]byte(modelsLiteral), &seed); err != nil {
		t.Fatalf("unmarshal scaffold models.json literal: %v\nliteral: %s", err, modelsLiteral)
	}

	if got := seed.Models["default"]["ANTHROPIC_MODEL"]; got != want {
		t.Errorf("seeded default profile ANTHROPIC_MODEL = %q, but quickstart.sh defaults to %q — af install --init and a quickstart bootstrap must agree on the default model", got, want)
	}
}
