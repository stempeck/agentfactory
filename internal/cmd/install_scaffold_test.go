package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// seededFromThisClone returns the name file `af install --init` writes into a factory
// bootstrapped from this repository's clone.
func seededFromThisClone(t *testing.T, name string) []byte {
	t.Helper()
	configDir := config.ConfigDir(t.TempDir())
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := seedStarterConfigs(io.Discard, io.Discard, configDir, findModuleRoot(t), false); err != nil {
		t.Fatalf("seeding from this repository's clone: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(configDir, name))
	if err != nil {
		t.Fatalf("read seeded %s: %v", name, err)
	}
	return data
}

// TestInstallScaffold_WatchdogAgentsAreSeededAgents pins the N6 parity guarantee
// (issue #408 Phase 4 / AC-4): every name in the seeded startup.json watchdog_agents
// is a real agent once the factory is bootstrapped — a built-in role of the seeded
// agents.json, or an agent `af install --agents` generates from an installed formula —
// so `af up` yields a FUNCTIONAL scoped watchdog (no unknown-agent warning, no
// refuse-on-boot). This is the regression guard the `mergepatrol`-class bug (a
// watchdog_agents name that is not a real agent) lacked.
func TestInstallScaffold_WatchdogAgentsAreSeededAgents(t *testing.T) {
	var agents config.AgentConfig
	if err := json.Unmarshal(seededFromThisClone(t, "agents.json"), &agents); err != nil {
		t.Fatalf("unmarshal seeded agents.json: %v", err)
	}
	var startup config.StartupConfig
	if err := json.Unmarshal(seededFromThisClone(t, "startup.json"), &startup); err != nil {
		t.Fatalf("unmarshal seeded startup.json: %v", err)
	}
	formulas, err := availableFormulaNames()
	if err != nil {
		t.Fatal(err)
	}

	// The scaffold must ship a FUNCTIONAL watchdog, not an empty refuse-on-boot one.
	if len(startup.WatchdogAgents) == 0 {
		t.Fatalf("scaffold startup.json must seed a non-empty watchdog_agents; got %v", startup.WatchdogAgents)
	}

	known := make([]string, 0, len(agents.Agents))
	for name := range agents.Agents {
		known = append(known, name)
	}

	// N6 parity: every watchdog_agents name must be a real agent. Membership is map-key
	// existence in agents.json (production's agentsCfg.Agents[name] idiom), NOT session
	// existence — the boundary R1-L1 / R2-H1 both turn on.
	for _, name := range startup.WatchdogAgents {
		if _, ok := agents.Agents[name]; !ok && !formulas[name] {
			t.Errorf("scaffold watchdog_agents %q is neither a seeded agents.json agent nor an installed formula's agent (the mergepatrol-class bug); seeded agents = %v",
				name, known)
		}
	}
}

// TestInstallScaffold_StartupAgentsDefaultManagerOnly pins T1/T3 (PR #410): a fresh
// `af install --init` + `af up` must auto-start ONLY the interactive manager, NOT the
// autonomous supervisor. Issue #408 intended ONLY the watchdog_agents widening; the
// scaffold's startup.json `agents` default must stay manager-only (commit 472f64a set
// it deliberately).
//
// The watchdog roster is tracked data that changes whenever an agent is added, so only
// the invariant is pinned here, never the list.
func TestInstallScaffold_StartupAgentsDefaultManagerOnly(t *testing.T) {
	var startup config.StartupConfig
	if err := json.Unmarshal(seededFromThisClone(t, "startup.json"), &startup); err != nil {
		t.Fatalf("unmarshal seeded startup.json: %v", err)
	}

	// T1/T3: default startup scope is manager-only.
	if !reflect.DeepEqual(startup.Agents, []string{"manager"}) {
		t.Errorf("scaffold startup.json agents = %v, want [manager] (T1/T3: no unapproved supervisor auto-start)", startup.Agents)
	}
	// Regression guard: the #408 watchdog fix must be PRESERVED, not reverted — every
	// watchdog_agents name must still resolve against agents.json
	// (TestInstallScaffold_WatchdogAgentsAreSeededAgents), and the list itself must
	// stay non-empty here.
	if len(startup.WatchdogAgents) == 0 {
		t.Errorf("scaffold watchdog_agents is empty; it must stay non-empty (keep the #408 fix)")
	}
}

// TestInstallScaffold_ModelsJsonSeeded pins the models.json seed (issue #480): a fresh
// `af install --init` must seed a models.json that PASSES ITS OWN VALIDATOR — because
// LoadModelsConfig runs validateModelsConfig on every launch, a seed with an incomplete
// endpoint (base_url without auth_token) or a non-empty ANTHROPIC_API_KEY would brick
// the next launch / teach the broken local-profile pattern.
//
// Round-trips the seeded models.json through the REAL LoadModelsConfig (not a grep).
func TestInstallScaffold_ModelsJsonSeeded(t *testing.T) {
	modelsLiteral := seededFromThisClone(t, "models.json")

	// Round-trip through the REAL loader/validator: write the seed to a temp
	// factory and LoadModelsConfig must return nil error.
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(config.ModelsConfigPath(root), modelsLiteral, 0o644); err != nil {
		t.Fatalf("write seed models.json: %v", err)
	}
	loaded, err := config.LoadModelsConfig(root)
	if err != nil {
		t.Fatalf("seed models.json must pass its own validator; LoadModelsConfig: %v\nliteral: %s", err, modelsLiteral)
	}

	// The default profile must exist.
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
	// requesting a nonexistent model (PR #482 review). Which local id it names is tracked
	// data, so only that intent is pinned.
	if got := lm["ANTHROPIC_MODEL"]; got == "" || strings.HasPrefix(got, "claude-") {
		t.Errorf("lmstudio profile ANTHROPIC_MODEL = %q, want a non-empty local model id, not a claude-* cloud id", got)
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

// shellPinModelBlockLiteralViolations inspects the block configure_shell actually writes — from
// `echo "$begin_marker"` to the next `echo "$end_marker"` — because the marker TEXT first appears in
// the `local` declarations, and a slice taken there holds no echo line at all.
func shellPinModelBlockLiteralViolations(configureShell string) []string {
	const open, close = `echo "$begin_marker"`, `echo "$end_marker"`
	i := strings.Index(configureShell, open)
	if i < 0 {
		return []string{"configure_shell no longer writes its model block with " + open}
	}
	j := strings.Index(configureShell[i:], close)
	if j < 0 {
		return []string{"configure_shell's model block has no " + close + " after " + open}
	}
	block := configureShell[i : i+j]

	var violations []string
	if m := regexp.MustCompile(`_MODEL:-[A-Za-z0-9]`).FindString(block); m != "" {
		violations = append(violations, "the written block hardcodes a model default ("+m+"…)")
	}
	if strings.Contains(block, ":-claude") {
		violations = append(violations, "the written block hardcodes a claude-* default")
	}
	return violations
}

func TestInstallScaffold_TrackedStartupDoesNotArmImprovementHook(t *testing.T) {
	var st config.StartupConfig
	if err := json.Unmarshal(seededFromThisClone(t, "startup.json"), &st); err != nil {
		t.Fatalf("unmarshal seeded startup.json: %v", err)
	}
	if st.Improvement == "on" {
		t.Error(`the startup.json seeded from this clone says improvement "on", which arms .improvement-hook at the first af up`)
	}
}

func TestInstallScaffold_TrackedDispatchSeedParsesOnceARepoIsAdded(t *testing.T) {
	var cfg map[string]any
	if err := json.Unmarshal(seededFromThisClone(t, "dispatch.json"), &cfg); err != nil {
		t.Fatalf("unmarshal seeded dispatch.json: %v", err)
	}
	cfg["repos"] = []string{"owner/repo"}
	armed, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := config.ParseDispatchConfig(armed); err != nil {
		t.Errorf("the dispatch.json seeded from this clone fails the loader once a repo is added: %v", err)
	}
}

// TestInstallScaffold_QuickstartHasNoHardcodedDefaultModel is the static half of CFG-9: the
// model exports configure_shell writes carry no literal default, ANTHROPIC_MODEL is read strictly
// from the seeded default profile, and the class keys in a form that cannot yield `null`. The
// execution half, which proves the written VALUES, lives beside shellFnsFrom.
func TestInstallScaffold_QuickstartHasNoHardcodedDefaultModel(t *testing.T) {
	qs, err := os.ReadFile("../../quickstart.sh")
	if err != nil {
		t.Fatalf("read quickstart.sh: %v", err)
	}
	fn := extractShellFunction(string(qs), "configure_shell")
	if fn == "" {
		t.Fatal("could not extract configure_shell() from quickstart.sh")
	}

	t.Run("written block carries no literal default", func(t *testing.T) {
		if v := shellPinModelBlockLiteralViolations(fn); len(v) > 0 {
			t.Errorf("configure_shell: %s", strings.Join(v, "; "))
		}
	})

	t.Run("literal default mutant is flagged", func(t *testing.T) {
		mutant := regexp.MustCompile(`\$\{ANTHROPIC_MODEL:-\$[A-Za-z_][A-Za-z0-9_]*\}`).
			ReplaceAllLiteralString(fn, "${ANTHROPIC_MODEL:-claude-opus-5}")
		if mutant == fn {
			t.Fatal("could not plant the mutant: the ANTHROPIC_MODEL echo no longer has the ${ANTHROPIC_MODEL:-$var} shape")
		}
		if len(shellPinModelBlockLiteralViolations(mutant)) == 0 {
			t.Error("the check passed a configure_shell that hardcodes ANTHROPIC_MODEL:-claude-opus-5")
		}
	})

	t.Run("derives each model key from the default profile without a null path", func(t *testing.T) {
		if probe := "jq -er '.models[.default].ANTHROPIC_MODEL'"; !strings.Contains(fn, probe) {
			t.Errorf("configure_shell does not read ANTHROPIC_MODEL with %s; a lenient read turns a missing key "+
				"into a `:-null` export instead of an error", probe)
		}
		for _, key := range []string{"ANTHROPIC_DEFAULT_OPUS_MODEL", "ANTHROPIC_DEFAULT_SONNET_MODEL", "ANTHROPIC_DEFAULT_HAIKU_MODEL"} {
			if probe := "jq -r '.models[.default]." + key + " // empty'"; !strings.Contains(fn, probe) {
				t.Errorf("configure_shell does not read %s with %s; any other lenient read turns an undeclared "+
					"class into a `:-null` export", key, probe)
			}
		}
	})
}

// TestInstallScaffold_LmstudioClassCoverage closes GAP B (#598 Phase 3b): the one endpoint profile
// af ships had four shape assertions above and nothing at all about the model CLASSES a session or
// its sub-agents can request. Since Phase 2 those classes are filled by derivation from the single
// declared ANTHROPIC_MODEL, so the seed's correctness now depends on a ladder no test held it to —
// dropping ANTHROPIC_MODEL from the example, or declaring a class key that contradicts it, would
// still pass TestInstallScaffold_ModelsJsonSeeded.
//
// Which classes the tracked lmstudio profile declares is data, so the expected missing set is not
// listed: it is the set the derivation ladder actually filled, which MissingEndpointClasses must
// report.
func TestInstallScaffold_LmstudioClassCoverage(t *testing.T) {
	var seed config.ModelsConfig
	if err := json.Unmarshal(seededFromThisClone(t, "models.json"), &seed); err != nil {
		t.Fatalf("unmarshal seeded models.json: %v", err)
	}
	lm, ok := seed.Models["lmstudio"]
	if !ok {
		t.Fatalf("seed must define an `lmstudio` example profile; got models=%v", seed.Models)
	}
	main := lm["ANTHROPIC_MODEL"]
	if main == "" {
		t.Fatal("the lmstudio example declares no ANTHROPIC_MODEL, so every class it can be asked for derives to nothing")
	}

	// Derivation, not configuration: every derived class key must resolve to the
	// declared main model, whether declared outright or filled by the ladder.
	completed := config.CompleteEndpointProfile(lm)
	for _, key := range config.DerivedEndpointClassKeys() {
		label, known := config.EndpointClassLabel(key)
		if !known {
			t.Fatalf("derived class key %s has no label; the inventory and the label table have diverged", key)
		}
		if got := completed[key]; got != main {
			t.Errorf("class %s (%s) resolves to %q, want the declared main model %q — a class that derives "+
				"to anything else asks an LM Studio server for a model it does not serve", label, key, got, main)
		}
	}

	var filled []string
	for _, key := range config.DerivedEndpointClassKeys() {
		if completed[key] != lm[key] {
			filled = append(filled, key)
		}
	}
	missing := config.MissingEndpointClasses(lm)
	if !reflect.DeepEqual(missing, filled) {
		t.Errorf("MissingEndpointClasses(lmstudio) = %v, but the derivation ladder filled %v — the coverage report "+
			"and the launch env disagree about which classes the profile leaves to derivation", missing, filled)
	}

	// GAP B: the residual af cannot fix must at least be said out loud — and a profile that
	// declares every derivable class must not warn on every save.
	warning, has := config.CoverageLintProfile("lmstudio", lm)
	if has != (len(filled) > 0) {
		t.Fatalf("lmstudio coverage warning present=%v, want present iff the profile leaves classes to derivation "+
			"(filled = %v); warning = %q", has, filled, warning)
	}
	if !has {
		return
	}
	if !strings.Contains(warning, "claude-fable-") {
		t.Errorf("the lmstudio warning does not name the claude-fable- alias requirement, so the one gap af "+
			"can neither derive nor seed stays invisible; warning = %q", warning)
	}
	for _, key := range filled {
		label, known := config.EndpointClassLabel(key)
		if !known {
			t.Fatalf("class key %s has no label, so the warning assertions below would compare against an empty string", key)
		}
		if !strings.Contains(warning, label) {
			t.Errorf("the lmstudio warning omits the %q class it leaves to derivation; warning = %q", label, warning)
		}
	}
}
