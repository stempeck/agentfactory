package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// Starter configs derive from the bootstrapping clone and are never shipped as copies inside af;
// agents.json starts as the built-in roles only; USING_AGENTFACTORY.md carries no seeding prose.

const starterSrcNoSource = "/nonexistent/no-source"

// The six clone-derived members of an OSS-shaped clone: no agents.json, and every agent name
// is a built-in role or an embedded formula (mergepatrol, fable-implement, gpt-rootcause-all,
// gpt-fable-review), the roster a factory holds once `af install --agents` has run.
func starterSrcCloneConfigs(over map[string]string, drop ...string) map[string]string {
	m := map[string]string{
		"dispatch.json":  `{"repos":["marker-clone"],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"source":"issue","agent":"fable-implement"}],"interval_seconds":300,"retry_after_seconds":1800}`,
		"factory.json":   `{"type":"factory","version":1,"name":"marker-clone","max_worktrees":8}`,
		"messaging.json": `{"groups":{"all":["manager","supervisor"]}}`,
		"startup.json":   `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":true,"watchdog_agents":["mergepatrol","fable-implement","gpt-rootcause-all"]}`,
		"models.json":    `{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-marker-model"},"codex":{"ANTHROPIC_BASE_URL":"http://localhost:4000","ANTHROPIC_AUTH_TOKEN":"file:secrets/litellm.key","ANTHROPIC_MODEL":"gpt-x"}},"agents":{"manager":"default","fable-implement":"default","gpt-fable-review":"codex"}}`,
		"litellm.yaml":   "marker: clone\n",
	}
	for name, content := range over {
		m[name] = content
	}
	for _, name := range drop {
		delete(m, name)
	}
	return m
}

func starterSrcFresh(t *testing.T) (root, configDir string) {
	t.Helper()
	root = t.TempDir()
	configDir = config.ConfigDir(root)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	return root, configDir
}

func starterSrcSeed(configDir, afSrc string, fallback bool) (string, error) {
	var out bytes.Buffer
	err := seedStarterConfigs(&out, io.Discard, configDir, afSrc, fallback)
	return out.String(), err
}

func starterSrcBuiltinRoles() []string {
	roles := make([]string, 0, len(builtinIdentities))
	for role := range builtinIdentities {
		roles = append(roles, role)
	}
	sort.Strings(roles)
	return roles
}

func starterSrcDecode(t *testing.T, path string, v any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decoding %s: %v", path, err)
	}
}

func starterSrcAgentNames(t *testing.T, configDir string) []string {
	t.Helper()
	var cfg config.AgentConfig
	starterSrcDecode(t, filepath.Join(configDir, "agents.json"), &cfg)
	names := make([]string, 0, len(cfg.Agents))
	for name := range cfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func starterSrcRequireAbsent(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("%s must not exist (stat err = %v)", path, err)
	}
}

// --- agents.json is never a copy of another repository's roster --------------------

func TestStarterConfigSource_NoEmbeddedAgentsJSON(t *testing.T) {
	starterSrcRequireAbsent(t, filepath.Join("install_configs", "agents.json"))
}

func TestStarterConfigSource_CloneRosterNotTransplanted(t *testing.T) {
	foreign := `{"agents":{"manager":{"type":"interactive","description":"foreign marker"},"foreign-agent":{"type":"autonomous","description":"f","formula":"fable-implement"}}}`
	src := newAFSourceWithConfigs(t, starterSrcCloneConfigs(map[string]string{
		"agents.json":    foreign,
		"messaging.json": `{"groups":{"all":["manager"]}}`,
		"startup.json":   `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":true,"watchdog_agents":["manager"]}`,
		"dispatch.json":  `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`,
	}))
	_, configDir := starterSrcFresh(t)
	if _, err := starterSrcSeed(configDir, src, false); err != nil {
		t.Fatalf("seed from a clone carrying its own agents.json: %v", err)
	}
	if got, want := starterSrcAgentNames(t, configDir), starterSrcBuiltinRoles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("seeded agents.json roster = %v, want only the built-in roles %v (the clone's roster is UNIQUE TO THAT REPOSITORY)", got, want)
	}
	var cfg config.AgentConfig
	starterSrcDecode(t, filepath.Join(configDir, "agents.json"), &cfg)
	if cfg.Agents["manager"].Description == "foreign marker" {
		t.Error("seeded manager entry was copied from the clone's agents.json")
	}
}

func TestStarterConfigSource_CloneWithoutAgentsJSONSeeds(t *testing.T) {
	src := newAFSourceWithConfigs(t, starterSrcCloneConfigs(nil))
	_, configDir := starterSrcFresh(t)
	if _, err := starterSrcSeed(configDir, src, false); err != nil {
		t.Fatalf("a clone without .agentfactory/agents.json must still seed: %v", err)
	}
	if got, want := starterSrcAgentNames(t, configDir), starterSrcBuiltinRoles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("seeded agents.json roster = %v, want %v", got, want)
	}
}

func TestStarterConfigSource_SeededRosterIndependentOfClone(t *testing.T) {
	withRoster := newAFSourceWithConfigs(t, starterSrcCloneConfigs(map[string]string{
		"agents.json": `{"agents":{"manager":{"type":"interactive","description":"pro-shaped"},"supervisor":{"type":"autonomous","description":"pro-shaped"},"fable-implement":{"type":"autonomous","description":"x","formula":"fable-implement"},"mergepatrol":{"type":"autonomous","description":"x","formula":"mergepatrol"},"gpt-rootcause-all":{"type":"autonomous","description":"x","formula":"gpt-rootcause-all"}}}`,
	}))
	withoutRoster := newAFSourceWithConfigs(t, starterSrcCloneConfigs(nil))
	_, dirA := starterSrcFresh(t)
	_, dirB := starterSrcFresh(t)
	if _, err := starterSrcSeed(dirA, withRoster, false); err != nil {
		t.Fatalf("seed A: %v", err)
	}
	if _, err := starterSrcSeed(dirB, withoutRoster, false); err != nil {
		t.Fatalf("seed B: %v", err)
	}
	a, _ := os.ReadFile(filepath.Join(dirA, "agents.json"))
	b, _ := os.ReadFile(filepath.Join(dirB, "agents.json"))
	if !bytes.Equal(a, b) {
		t.Errorf("seeded agents.json depends on the clone's roster:\nA=%s\nB=%s", a, b)
	}
}

func TestStarterConfigSource_RealProCloneSeedsBuiltinRolesOnly(t *testing.T) {
	_, configDir := starterSrcFresh(t)
	if _, err := starterSrcSeed(configDir, findModuleRoot(t), false); err != nil {
		t.Fatalf("seed from this repository's tracked configs: %v", err)
	}
	if got, want := starterSrcAgentNames(t, configDir), starterSrcBuiltinRoles(); !reflect.DeepEqual(got, want) {
		t.Fatalf("seeded agents.json roster = %v, want %v; `af install --agents` generates the rest", got, want)
	}
}

// Protective: agent-gen merges into an agents.json that must exist, and the built-in roles
// have no formula of their own.
func TestStarterConfigSource_SeededAgentsJSONSatisfiesAgentGen(t *testing.T) {
	channels := map[string]struct {
		afSrc    string
		fallback bool
	}{
		"source_clone": {findModuleRoot(t), false},
		"no_clone":     {starterSrcNoSource, true},
	}
	for name, ch := range channels {
		t.Run(name, func(t *testing.T) {
			_, configDir := starterSrcFresh(t)
			if _, err := starterSrcSeed(configDir, ch.afSrc, ch.fallback); err != nil {
				t.Fatalf("seed: %v", err)
			}
			cfg, err := config.LoadAgentConfig(filepath.Join(configDir, "agents.json"))
			if err != nil {
				t.Fatalf("LoadAgentConfig on the seeded agents.json: %v", err)
			}
			for role, wantType := range map[string]string{"manager": "interactive", "supervisor": "autonomous"} {
				entry, ok := cfg.Agents[role]
				if !ok {
					t.Errorf("seeded agents.json lacks built-in role %q", role)
					continue
				}
				if entry.Type != wantType || entry.Formula != "" {
					t.Errorf("%s = {type %q, formula %q}, want {type %q, no formula}", role, entry.Type, entry.Formula, wantType)
				}
			}
			for agent, entry := range cfg.Agents {
				if entry.ContinuousImprovement {
					t.Errorf("seeded agent %q has continuous_improvement on; the hook is opt-in", agent)
				}
			}
		})
	}
}

// Protective (AC-4): an operator's agents.json is never replaced or merged with the built-ins.
func TestStarterConfigSource_PreExistingAgentsJSONNeverReplacedByBuiltins(t *testing.T) {
	src := newAFSourceWithConfigs(t, starterSrcCloneConfigs(nil))
	_, configDir := starterSrcFresh(t)
	existing := []byte(`{"agents":{"manager":{"type":"interactive","description":"operator"},"alice":{"type":"autonomous","description":"custom"},"fable-implement":{"type":"autonomous","description":"x","formula":"fable-implement"}}}`)
	if err := os.WriteFile(filepath.Join(configDir, "agents.json"), existing, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := starterSrcSeed(configDir, src, false); err != nil {
		t.Fatalf("seed beside a pre-existing agents.json: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(configDir, "agents.json"))
	if !bytes.Equal(got, existing) {
		t.Errorf("pre-existing agents.json was rewritten:\n%s", got)
	}
	log, _ := os.ReadFile(filepath.Join(configDir, ".config-seed.log"))
	if !strings.Contains(string(log), "agents.json kept-existing") {
		t.Errorf(".config-seed.log does not record agents.json as kept-existing:\n%s", log)
	}
}

// --- no embedded models.json; pins come from the bootstrapping clone ---------------

func TestStarterConfigSource_NoEmbeddedModelsJSON(t *testing.T) {
	starterSrcRequireAbsent(t, filepath.Join("install_configs", "models.json"))
}

func TestStarterConfigSource_NoEmbeddedConfigMirror(t *testing.T) {
	starterSrcRequireAbsent(t, "install_configs")
	sources, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range sources {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "//go:embed install_configs") {
			t.Errorf("%s still embeds install_configs/", path)
		}
	}
}

func TestStarterConfigSource_NoCloneSeedNamesOnlyBuiltinRoles(t *testing.T) {
	_, configDir := starterSrcFresh(t)
	out, err := starterSrcSeed(configDir, starterSrcNoSource, true)
	if err != nil {
		t.Fatalf("no-clone seed: %v", err)
	}
	for _, name := range []string{"factory.json", "agents.json", "messaging.json"} {
		if _, err := os.Stat(filepath.Join(configDir, name)); err != nil {
			t.Errorf("no-clone seed must still write %s (a factory cannot run without it): %v", name, err)
		}
	}
	for _, name := range []string{"dispatch.json", "startup.json", "models.json", "litellm.yaml"} {
		starterSrcRequireAbsent(t, filepath.Join(configDir, name))
	}
	if got, want := starterSrcAgentNames(t, configDir), starterSrcBuiltinRoles(); !reflect.DeepEqual(got, want) {
		t.Errorf("no-clone agents.json roster = %v, want %v", got, want)
	}
	var msg config.MessagingConfig
	starterSrcDecode(t, filepath.Join(configDir, "messaging.json"), &msg)
	for group, members := range msg.Groups {
		for _, member := range members {
			if !builtinIdentities[member] {
				t.Errorf("no-clone messaging group %q names %q, not a built-in role", group, member)
			}
		}
	}
	if !strings.Contains(out, "AF_SOURCE_ROOT") {
		t.Errorf("no-clone seed must say how to seed the clone's configs (AF_SOURCE_ROOT); stdout:\n%s", out)
	}
	log, _ := os.ReadFile(filepath.Join(configDir, ".config-seed.log"))
	if !strings.Contains(string(log), "dispatch.json not-seeded") {
		t.Errorf(".config-seed.log does not record dispatch.json as not-seeded:\n%s", log)
	}
}

func TestStarterConfigSource_OSSShapedCloneSeedsItsOwnPins(t *testing.T) {
	src := newAFSourceWithConfigs(t, starterSrcCloneConfigs(nil))
	_, configDir := starterSrcFresh(t)
	if _, err := starterSrcSeed(configDir, src, false); err != nil {
		t.Fatalf("seed from an OSS-shaped clone: %v", err)
	}
	var models config.ModelsConfig
	starterSrcDecode(t, filepath.Join(configDir, "models.json"), &models)
	want := map[string]string{"manager": "default", "fable-implement": "default"}
	if !reflect.DeepEqual(models.Agents, want) {
		t.Errorf("seeded models.json agents = %v, want the clone's portable pins %v", models.Agents, want)
	}
}

// Protective: a pro bootstrap still seeds pro's pins (minus the non-portable ones).
func TestStarterConfigSource_ProCloneSeedsProPins(t *testing.T) {
	root := findModuleRoot(t)
	_, configDir := starterSrcFresh(t)
	if _, err := starterSrcSeed(configDir, root, false); err != nil {
		t.Fatalf("seed from this repository: %v", err)
	}
	var tracked, seeded config.ModelsConfig
	starterSrcDecode(t, filepath.Join(root, ".agentfactory", "models.json"), &tracked)
	starterSrcDecode(t, filepath.Join(configDir, "models.json"), &seeded)
	if seeded.Default != tracked.Default {
		t.Errorf("seeded default = %q, tracked %q", seeded.Default, tracked.Default)
	}
	for agent, profile := range tracked.Agents {
		got, ok := seeded.Agents[agent]
		if portable := !nonPortableProfile(tracked.Models[profile]); portable != ok || (ok && got != profile) {
			t.Errorf("pin %s→%s: seeded %q (present %v), want present=%v", agent, profile, got, ok, portable)
		}
	}
	for agent := range seeded.Agents {
		if _, ok := tracked.Agents[agent]; !ok {
			t.Errorf("seeded pin %q is not in the tracked models.json", agent)
		}
	}
}

func TestStarterConfigSource_NoHandRunConfigSync(t *testing.T) {
	root := findModuleRoot(t)
	makefile, err := os.ReadFile(filepath.Join(root, "Makefile"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(makefile), "\n") {
		if strings.HasPrefix(line, "sync-configs:") {
			t.Error("Makefile still defines the hand-run sync-configs target")
		}
		if strings.HasPrefix(line, ".PHONY:") && strings.Contains(" "+line+" ", " sync-configs ") {
			t.Error("Makefile .PHONY still lists sync-configs")
		}
	}
	claude, err := os.ReadFile(filepath.Join(root, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(claude), "sync-configs") {
		t.Error("CLAUDE.md still documents make sync-configs")
	}
}

// --- no embedded startup.json; watchdog_agents comes from the bootstrapping clone ---

func TestStarterConfigSource_NoEmbeddedStartupJSON(t *testing.T) {
	starterSrcRequireAbsent(t, filepath.Join("install_configs", "startup.json"))
}

func TestStarterConfigSource_WatchdogListDerivedWithoutCopiedRoster(t *testing.T) {
	configs := starterSrcCloneConfigs(nil)
	src := newAFSourceWithConfigs(t, configs)
	_, configDir := starterSrcFresh(t)
	if _, err := starterSrcSeed(configDir, src, false); err != nil {
		t.Fatalf("a clone whose watchdog_agents name formula agents must seed: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(configDir, "startup.json"))
	if string(got) != configs["startup.json"] {
		t.Errorf("seeded startup.json differs from the clone's:\ngot  %s\nwant %s", got, configs["startup.json"])
	}
}

func TestStarterConfigSource_RealProCloneWatchdogSurvivesBuiltinRoster(t *testing.T) {
	root := findModuleRoot(t)
	_, configDir := starterSrcFresh(t)
	if _, err := starterSrcSeed(configDir, root, false); err != nil {
		t.Fatalf("seed from this repository: %v", err)
	}
	var tracked, seeded config.StartupConfig
	starterSrcDecode(t, filepath.Join(root, ".agentfactory", "startup.json"), &tracked)
	starterSrcDecode(t, filepath.Join(configDir, "startup.json"), &seeded)
	if !reflect.DeepEqual(seeded.WatchdogAgents, tracked.WatchdogAgents) {
		t.Errorf("seeded watchdog_agents = %v, tracked %v", seeded.WatchdogAgents, tracked.WatchdogAgents)
	}
	if got, want := starterSrcAgentNames(t, configDir), starterSrcBuiltinRoles(); !reflect.DeepEqual(got, want) {
		t.Errorf("seeded agents.json roster = %v, want %v", got, want)
	}
}

// The widened reference roster still refuses a name that is neither a built-in role nor an
// embedded formula, and messaging.json — loaded strictly by the mail router — is held to the
// agents.json actually written.
func TestStarterConfigSource_FreshSeedRefusesNameOutsideBuiltinsAndFormulas(t *testing.T) {
	cases := map[string]struct {
		over map[string]string
		name string
	}{
		"dispatch_mapping_unknown": {
			over: map[string]string{"dispatch.json": `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"source":"issue","agent":"not-a-formula-xyz"}],"interval_seconds":300,"retry_after_seconds":1800}`},
			name: "not-a-formula-xyz",
		},
		"watchdog_unknown": {
			over: map[string]string{"startup.json": `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":true,"watchdog_agents":["not-a-formula-xyz"]}`},
			name: "not-a-formula-xyz",
		},
		"messaging_formula_agent_not_yet_generated": {
			over: map[string]string{"messaging.json": `{"groups":{"all":["manager","fable-implement"]}}`},
			name: "fable-implement",
		},
	}
	for label, tc := range cases {
		t.Run(label, func(t *testing.T) {
			src := newAFSourceWithConfigs(t, starterSrcCloneConfigs(tc.over))
			_, configDir := starterSrcFresh(t)
			_, err := starterSrcSeed(configDir, src, false)
			if err == nil || !strings.Contains(err.Error(), tc.name) {
				t.Fatalf("seed error = %v, want a refusal naming %q", err, tc.name)
			}
			entries, _ := os.ReadDir(configDir)
			if len(entries) != 0 {
				t.Errorf("a refused seed wrote files: %v", entries)
			}
		})
	}
}

// --- USING_AGENTFACTORY.md carries no starter-config seeding prose -------------------

func TestStarterConfigSource_UsingDocCarriesNoStarterConfigSeedingProse(t *testing.T) {
	doc := strings.ToLower(readUsingAgentfactoryDoc(t))
	for _, phrase := range []string{"starter config", "sync-configs", "config-seed", "install_configs", "source clone", "maintaining starter configs", "verifying a fresh factory"} {
		if n := strings.Count(doc, phrase); n > 0 {
			t.Errorf("USING_AGENTFACTORY.md mentions %q %d time(s)", phrase, n)
		}
	}
}

// --- consumers of the removed mirror -------------------------------------------------

func TestStarterConfigSource_InstallHelpClaimsNoBuiltinCopies(t *testing.T) {
	long := installCmd.Long
	if strings.Contains(long, "built-in copies") {
		t.Error("af install --help still promises built-in copies of the tracked configs")
	}
	for _, want := range []string{"AF_SOURCE_ROOT", "manager", "supervisor", "never overwritten"} {
		if !strings.Contains(long, want) {
			t.Errorf("af install --help does not mention %q", want)
		}
	}
}

func TestStarterConfigSource_ADR028RecordsNoEmbeddedMirror(t *testing.T) {
	adrDir := filepath.Join(findModuleRoot(t), "docs", "architecture", "adrs")
	matches, err := filepath.Glob(filepath.Join(adrDir, "ADR-028-*.md"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("want exactly one ADR-028-*.md, got %v (err %v)", matches, err)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	title, _, _ := strings.Cut(string(data), "\n")
	if strings.Contains(title, "embedded mirror") || strings.Contains(title, "sync-configs") {
		t.Errorf("ADR-028 title still records the embedded mirror: %s", title)
	}
	if !strings.Contains(string(data), "ADR-020") {
		t.Error("ADR-028 does not cite ADR-020 (init bootstraps manager + supervisor only)")
	}
	readme, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(readme), "\n") {
		if strings.Contains(line, "](ADR-028-") && strings.Contains(line, "embedded mirror") {
			t.Errorf("ADR README index row still records the embedded mirror: %s", line)
		}
	}
}
