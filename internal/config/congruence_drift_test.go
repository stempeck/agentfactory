package config

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// congruenceFixtureDir is package-relative on purpose: `go test` runs with the package
// directory as the working directory, so no repo-root resolution is needed. (findRepoRoot,
// which the install_hooks drift test uses, lives in package cmd and is unreachable here.)
const congruenceFixtureDir = "testdata/congruence"

var updateCongruenceFixtures = flag.Bool("update", false, "rewrite testdata/congruence/*.json from the generator")

// TestConfigCongruence_NoDrift covers issue #620 Phase 1 AC 6.
//
// It is the constructive half of the congruence invariant. Committed fixtures alone would be a
// snapshot nobody is obliged to refresh; regenerating them in-memory and byte-comparing makes a
// canonical struct that gains a field FAIL CI until the fixtures are regenerated — the
// ADR-008 pattern (docs/architecture/adrs/ADR-008-embed-with-drift-test.md), whose in-repo
// implementation is internal/cmd/install_hooks_drift_test.go.
//
// This matters more here than for hooks because of fillRecoveryDefaults (startup.go:204-236):
// it rewrites a stated 0 into the default in place, so an absent-vs-zero distinction cannot
// survive a round trip. A comment asking reviewers to be careful could not close that; a
// byte-comparison against non-zero fixtures can.
func TestConfigCongruence_NoDrift(t *testing.T) {
	generated, err := CongruenceFixtures()
	if err != nil {
		t.Fatalf("CongruenceFixtures: %v", err)
	}
	// Anti-vacuity: a generator that produced nothing would make every comparison below
	// trivially pass and the invariant would guard nothing.
	if len(generated) == 0 {
		t.Fatal("the generator produced zero fixtures — the drift test would pass vacuously")
	}

	t.Run("every generated fixture matches its committed bytes", func(t *testing.T) {
		for _, name := range sortedCongruenceKeys(generated) {
			path := filepath.Join(congruenceFixtureDir, name+".json")
			committed, err := os.ReadFile(path)
			if err != nil {
				t.Errorf("read %s: %v — regenerate the fixtures with `go test ./internal/config/ -run TestConfigCongruence_Regenerate -update`", path, err)
				continue
			}
			if !bytes.Equal(committed, generated[name]) {
				t.Errorf("%s has drifted from the canonical struct — a field was added, removed or retyped.\n"+
					"Regenerate with `go test ./internal/config/ -run TestConfigCongruence_Regenerate -update`, and review the diff:\n"+
					"committed:\n%s\ngenerated:\n%s", path, committed, generated[name])
			}
		}
	})

	t.Run("every committed fixture has a generator counterpart", func(t *testing.T) {
		// The other direction. Without it, a fixture for a struct dropped from the canonical
		// set would linger on disk asserting a schema nothing produces any more.
		entries, err := os.ReadDir(congruenceFixtureDir)
		if err != nil {
			t.Fatalf("read %s: %v", congruenceFixtureDir, err)
		}
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".json")
			if _, ok := generated[name]; !ok {
				t.Errorf("%s/%s is committed but no longer generated — delete it, or restore its struct to congruenceDocuments()", congruenceFixtureDir, e.Name())
			}
		}
	})

	t.Run("fixtures cover the fields the design names", func(t *testing.T) {
		// design-doc.md:254 pins the coverage bar. These three were chosen because each is a
		// field that HAD already drifted out of a consumer's mirror struct, so a fixture set
		// that omitted them would certify a congruence it had not actually checked.
		var dispatch struct {
			Workflows []json.RawMessage `json:"workflows"`
			Mappings  []struct {
				Model string `json:"model"`
			} `json:"mappings"`
		}
		if err := json.Unmarshal(generated["dispatch"], &dispatch); err != nil {
			t.Fatalf("unmarshal dispatch fixture: %v", err)
		}
		if len(dispatch.Workflows) == 0 {
			t.Errorf("the dispatch fixture must populate workflows; got:\n%s", generated["dispatch"])
		}
		if len(dispatch.Mappings) == 0 {
			t.Fatalf("the dispatch fixture must populate mappings; got:\n%s", generated["dispatch"])
		}
		if dispatch.Mappings[0].Model == "" {
			t.Errorf("the dispatch fixture must populate mappings[].model; got:\n%s", generated["dispatch"])
		}

		var startup struct {
			Recovery map[string]json.RawMessage `json:"recovery"`
		}
		if err := json.Unmarshal(generated["startup"], &startup); err != nil {
			t.Fatalf("unmarshal startup fixture: %v", err)
		}
		// All 14 recovery fields, every one NON-ZERO. A stated 0 is indistinguishable from an
		// absent key because fillRecoveryDefaults overwrites it, so a zero-valued fixture field
		// would certify nothing.
		if len(startup.Recovery) != 14 {
			t.Errorf("the startup fixture must carry all 14 recovery fields; got %d: %v", len(startup.Recovery), sortedCongruenceKeys(startup.Recovery))
		}
		for _, k := range sortedCongruenceKeys(startup.Recovery) {
			switch v := strings.TrimSpace(string(startup.Recovery[k])); v {
			case "0", `""`, "null", "false", "[]", "{}":
				t.Errorf("recovery.%s is %s — a zero value is indistinguishable from an absent key once fillRecoveryDefaults runs", k, v)
			}
		}
	})

	t.Run("raw-tier fixtures survive the validating write path", func(t *testing.T) {
		// Phase 2 pushes these same fixtures through the validating write path, so a fixture
		// no validator accepts would surface a phase later, in another module. The generator
		// knows a field's TYPE but not its meaning, and several validators enforce RELATIONS
		// between fields — validateRecoveryRelations' four coupled arms, and a workflow phase
		// that must name a label some mapping actually carries — which no per-type default
		// can satisfy. Running the real saver is the only honest check.
		dir := t.TempDir()
		save := map[string]func(string) error{
			"dispatch": func(p string) error {
				var cfg DispatchConfig
				if err := json.Unmarshal(generated["dispatch"], &cfg); err != nil {
					return err
				}
				return SaveDispatchConfig(p, &cfg)
			},
			"startup": func(p string) error {
				var cfg StartupConfig
				if err := json.Unmarshal(generated["startup"], &cfg); err != nil {
					return err
				}
				return SaveStartupConfig(p, &cfg)
			},
			"messaging": func(p string) error {
				var cfg MessagingConfig
				if err := json.Unmarshal(generated["messaging"], &cfg); err != nil {
					return err
				}
				return SaveMessagingConfig(p, &cfg)
			},
			"statusline": func(p string) error {
				var cfg StatuslineConfig
				if err := json.Unmarshal(generated["statusline"], &cfg); err != nil {
					return err
				}
				return SaveStatuslineConfig(p, &cfg)
			},
		}
		for _, name := range sortedCongruenceKeys(save) {
			if err := save[name](filepath.Join(dir, name+".json")); err != nil {
				t.Errorf("the %s fixture must satisfy its own validator; got %v\nfixture:\n%s", name, err, generated[name])
			}
		}
	})

	t.Run("raw-tier fixtures carry no CANARY values and secret-tier fixtures do", func(t *testing.T) {
		// The two tiers exist so a reviewer can tell at a glance whether a fixture is safe to
		// paste into an issue. A credential-shaped field landing in a raw-tier schema shows up
		// as fixture churn in exactly this test's diff, which is the review chokepoint the risk
		// registry assigns here.
		for _, name := range []string{"dispatch", "startup", "messaging", "statusline", "factory"} {
			if bytes.Contains(generated[name], []byte(canaryPrefix)) {
				t.Errorf("%s is a raw-tier fixture but carries a %s value", name, canaryPrefix)
			}
		}
		for _, name := range []string{"agents", "models", "telemetry"} {
			if !bytes.Contains(generated[name], []byte(canaryPrefix)) {
				t.Errorf("%s is a secret-tier fixture and must use %s synthetic values", name, canaryPrefix)
			}
		}
	})
}

// TestConfigCongruence_MistypedPinIsReported pins the guard on the one way this generator can
// take down a shipped binary. congruence_gen.go is deliberately not a _test.go file so that
// `af config fingerprint` shares the walk, which means an unguarded reflect.Convert on a pin
// whose type does not fit its field would panic in an operator's `af`, not in CI.
func TestConfigCongruence_MistypedPinIsReported(t *testing.T) {
	const path = "startup.recovery.confirm_ticks"
	original, ok := congruencePinned[path]
	if !ok {
		t.Fatalf("%s is no longer pinned; pick another int pin for this test", path)
	}
	congruencePinned[path] = []string{"not an int"}
	defer func() { congruencePinned[path] = original }()

	fixtures, err := CongruenceFixtures()
	if err == nil {
		t.Fatalf("a pin whose type does not fit its field must be reported, not silently dropped; got %d fixtures", len(fixtures))
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("the error must name the offending pin %q; got %v", path, err)
	}
}

// TestConfigCongruence_Regenerate rewrites the committed fixtures from the generator. It is a
// no-op without -update, so it is safe in CI: regeneration is an explicit operator action, and
// a run that silently rewrote the fixtures it is meant to compare against would turn the drift
// test into a self-portrait.
func TestConfigCongruence_Regenerate(t *testing.T) {
	if !*updateCongruenceFixtures {
		t.Skip("pass -update to rewrite testdata/congruence/*.json")
	}
	fixtures, err := CongruenceFixtures()
	if err != nil {
		t.Fatalf("CongruenceFixtures: %v", err)
	}
	if err := os.MkdirAll(congruenceFixtureDir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", congruenceFixtureDir, err)
	}
	for name, data := range fixtures {
		path := filepath.Join(congruenceFixtureDir, name+".json")
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
		t.Logf("wrote %s", path)
	}
}

func sortedCongruenceKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
