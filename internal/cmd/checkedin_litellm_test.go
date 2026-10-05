package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestCheckedInLitellmCarriesClaudeAliases is the CI guard that closes GAP A (issue #598, Phase 3c):
// nothing else in the repo reads .agentfactory/litellm.yaml, so the checked-in gateway config could
// silently fall behind the claude-* ids the checked-in direct profiles name — the design's
// "self-catching" hard-fail only fires when an operator runs `af config models check` against the
// live gateway. This turns that into a build-time guard.
//
// It reads the real files and derives the demanded set from PRODUCTION code
// (directProfileClaudeIDs) over the real registry, so adding a claude-* direct profile to
// .agentfactory/models.json fails this test until every checked-in gateway config grows a matching
// alias. Every mode file is guarded; they serve the same registry from the same port, one at a
// time. This file lists the api-key litellm.yaml, and checkedin_litellm_subscription_pro_test.go
// appends the subscription litellm.codex-subscription.yaml, which the public repo holds back.
//
// It also demands that every class id a gateway profile names (main, haiku, opus, sonnet, fable —
// effectiveModelIDs, the production universe `check` uses) is a served lane. The alias check alone
// could not see a profile whose sonnet class named a lane one mode file lacked; a launch on that
// profile only warns, and the first sonnet-class sub-agent then fails with a 404.
func TestCheckedInLitellmCarriesClaudeAliases(t *testing.T) {
	moduleRoot := findModuleRoot(t)

	regBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", "models.json"))
	if err != nil {
		t.Fatalf("read checked-in .agentfactory/models.json: %v", err)
	}
	var reg config.ModelsConfig
	if err := json.Unmarshal(regBytes, &reg); err != nil {
		t.Fatalf("parse checked-in .agentfactory/models.json: %v", err)
	}

	demanded := directProfileClaudeIDs(&reg)
	if len(demanded) == 0 {
		t.Fatal("the checked-in registry names no claude-* id at all, so every assertion below would " +
			"pass on an empty set — the direct profiles are what make the gateway aliases necessary")
	}

	for _, file := range checkedInLitellmFiles {
		t.Run(file, func(t *testing.T) {
			yamlBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", file))
			if err != nil {
				t.Fatalf("read checked-in .agentfactory/%s: %v", file, err)
			}
			entries := parseLitellmSeedEntries(t, string(yamlBytes))

			aliasBackend := map[string]string{}
			servedLane := map[string]bool{}
			for _, e := range entries {
				if strings.HasPrefix(e.name, "claude-") {
					aliasBackend[e.name] = e.backend
					continue
				}
				servedLane[e.name] = true
			}

			for _, id := range demanded {
				backend, ok := aliasBackend[id]
				if !ok {
					t.Errorf("checked-in .agentfactory/%s advertises no %q alias — a direct profile in "+
						".agentfactory/models.json names it, so the gateway refuses a host request for that id and "+
						"`af config models check` hard-fails on this factory. Add `- model_name: %s` routed to the "+
						"main backend. (This may also fire because an operator added a claude-* profile via "+
						"`af config models set` without updating the gateway config.)", file, id, id)
					continue
				}
				// backend is like "openai/gpt-5.6-sol"; the served lane is the segment after the provider slash.
				lane := backend[strings.LastIndex(backend, "/")+1:]
				if !servedLane[lane] {
					t.Errorf("alias %q routes to %q, which is not a served backend in %s's model_list "+
						"— an alias must point at a real (largest-window) backend or the gateway still refuses the request", id, backend, file)
				}
			}

			for name, profile := range reg.Models {
				// A gateway profile is one whose token is a file: reference to the master key — the
				// same discriminator `check` uses. A direct or foreign endpoint (lmstudio) serves its
				// own ids and is not this gateway's concern.
				if !strings.HasPrefix(profile[authTokenKey], secretPrefix) {
					continue
				}
				for _, id := range effectiveModelIDs(profile, nil) {
					if !servedLane[id] && aliasBackend[id] == "" {
						t.Errorf("profile %q names class id %q, which %s does not serve — a launch on that profile "+
							"only warns, and the first request for that class fails with a 404. Add `- model_name: %s`.", name, id, file, id)
					}
				}
			}
		})
	}
}

// checkedInLitellmFiles are the gateway configs this factory ships, one per upstream auth mode;
// the launch line selects one by the persisted mode record (quickstart.sh gateway-relaunch.sh).
// The subscription mode file is appended by checkedin_litellm_subscription_pro_test.go, because the
// public repo holds it back.
var checkedInLitellmFiles = []string{"litellm.yaml"}

// TestCheckedInLitellmDeclaresALongRouterTimeout guards the daily-use timeout, which is the
// operator's and nothing to do with the probe's own five-minute budget (liveSmokeDeadline): each
// checked-in gateway config must declare router_settings.timeout explicitly (LiteLLM's default cuts
// long agentic turns), at no less than five minutes, and every mode file must agree, since a
// session behaves the same whichever credential mode the gateway was launched in.
func TestCheckedInLitellmDeclaresALongRouterTimeout(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	const floor = 5 * time.Minute
	timeouts := map[string]time.Duration{}
	for _, file := range checkedInLitellmFiles {
		yamlBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", file))
		if err != nil {
			t.Fatalf("read checked-in .agentfactory/%s: %v", file, err)
		}
		m := regexp.MustCompile(`(?m)^\s*timeout:\s*(\d+)`).FindStringSubmatch(string(yamlBytes))
		if m == nil {
			t.Errorf("%s declares no router_settings.timeout; without it LiteLLM's default cuts long agentic turns", file)
			continue
		}
		seconds, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("%s: parse router timeout %q: %v", file, m[1], err)
		}
		timeouts[file] = time.Duration(seconds) * time.Second
		if timeouts[file] < floor {
			t.Errorf("%s router_settings.timeout is %v; agentic turns run long, keep it at %v or more", file, timeouts[file], floor)
		}
	}
	first := checkedInLitellmFiles[0]
	for _, file := range routerTimeoutDisagreements(checkedInLitellmFiles, timeouts) {
		t.Errorf("%s and %s disagree on router_settings.timeout (%v vs %v); a session's turn budget must not depend on the credential mode", first, file, timeouts[first], timeouts[file])
	}
}

// routerTimeoutDisagreements compares nothing until every file parsed: an unparsed file has already
// failed the test, and its zero timeout would only add a second, misleading failure.
func routerTimeoutDisagreements(files []string, timeouts map[string]time.Duration) []string {
	if len(files) == 0 || len(timeouts) != len(files) {
		return nil
	}
	var disagree []string
	for _, file := range files[1:] {
		if timeouts[file] != timeouts[files[0]] {
			disagree = append(disagree, file)
		}
	}
	return disagree
}

// The checked-in mode files agree today, so only synthetic timeouts can show the comparison still
// catches a mismatch.
func TestRouterTimeoutDisagreementsCatchesAMismatch(t *testing.T) {
	five, ten := 5*time.Minute, 10*time.Minute
	for name, c := range map[string]struct {
		files    []string
		timeouts map[string]time.Duration
		want     []string
	}{
		"disagree":     {[]string{"a.yaml", "b.yaml"}, map[string]time.Duration{"a.yaml": five, "b.yaml": ten}, []string{"b.yaml"}},
		"agree":        {[]string{"a.yaml", "b.yaml"}, map[string]time.Duration{"a.yaml": five, "b.yaml": five}, nil},
		"single file":  {[]string{"a.yaml"}, map[string]time.Duration{"a.yaml": five}, nil},
		"one unparsed": {[]string{"a.yaml", "b.yaml"}, map[string]time.Duration{"a.yaml": five}, nil},
	} {
		if got := routerTimeoutDisagreements(c.files, c.timeouts); !slices.Equal(got, c.want) {
			t.Errorf("%s: disagreements = %q, want %q", name, got, c.want)
		}
	}
}

// litellmModeEntry is one `- model_name:` block's backend + api_key line, scoped to this file's own
// mode-invariant check rather than extending the shared litellmSeedEntry fixture
// (quickstart_provisioning_shape_test.go) other tests depend on.
type litellmModeEntry struct {
	name, backend, apiKey string
}

// parseLitellmModeEntries reads (name, backend, api_key) triples from a checked-in litellm.yaml's
// model_list, by the same line-scan approach as parseLitellmSeedEntries (no yaml library, ADR-013).
func parseLitellmModeEntries(t *testing.T, yamlBytes []byte) []litellmModeEntry {
	t.Helper()
	nameRe := regexp.MustCompile(`^\s*-\s*model_name:\s*([^\s#]+)`)
	backendRe := regexp.MustCompile(`^\s+model:\s*([^\s#]+)`)
	apiKeyRe := regexp.MustCompile(`^\s+api_key:\s*([^\s#]+)`)

	var entries []litellmModeEntry
	for _, line := range strings.Split(string(yamlBytes), "\n") {
		if m := nameRe.FindStringSubmatch(line); m != nil {
			entries = append(entries, litellmModeEntry{name: strings.Trim(m[1], `"'`)})
			continue
		}
		if len(entries) == 0 {
			continue
		}
		last := &entries[len(entries)-1]
		if m := backendRe.FindStringSubmatch(line); m != nil {
			last.backend = strings.Trim(m[1], `"'`)
			continue
		}
		if m := apiKeyRe.FindStringSubmatch(line); m != nil {
			last.apiKey = strings.Trim(m[1], `"'`)
		}
	}
	return entries
}

// TestCheckedInLitellmYamlHasNoMixedModeCredentials is a PROTECTIVE assertion (fable-implement
// Phase 5: one per DO-NOT-CHANGE item) for the mode invariant this phase's design doc carries: for
// any entry, a subscription-mode backend (chatgpt/ prefix) must never declare
// os.environ/OPENAI_API_KEY, and a key-mode backend must never route through a chatgpt/ lane —
// checking the FULL provider prefix (before the slash), not the post-slash lane
// TestCheckedInLitellmCarriesClaudeAliases above already asserts against
// (todos/fable-implement/intake.md File 4). The checked-in .agentfactory/litellm.yaml carries only
// openai/ backends today (Phase 2 does not touch it — DO-NOT-CHANGE), so this test is expected to
// PASS now and keep passing; it exists to catch a future edit that mixes the two modes on one entry.
func TestCheckedInLitellmYamlHasNoMixedModeCredentials(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	for _, file := range checkedInLitellmFiles {
		t.Run(file, func(t *testing.T) {
			yamlBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", file))
			if err != nil {
				t.Fatalf("read checked-in .agentfactory/%s: %v", file, err)
			}
			entries := parseLitellmModeEntries(t, yamlBytes)
			if len(entries) == 0 {
				t.Fatal("no model_list entries parsed — every assertion below would pass vacuously")
			}

			for _, e := range entries {
				provider := e.backend[:strings.Index(e.backend, "/")]
				switch provider {
				case "chatgpt":
					if e.apiKey != "" {
						t.Errorf("entry %q routes through chatgpt/ (subscription mode) but also declares api_key %q; "+
							"a subscription lane must carry no os.environ/OPENAI_API_KEY", e.name, e.apiKey)
					}
				case "openai":
					if e.apiKey == "os.environ/OPENAI_API_KEY" {
						continue
					}
					t.Errorf("entry %q routes through openai/ (key mode) but does not declare api_key: os.environ/OPENAI_API_KEY; "+
						"got %q", e.name, e.apiKey)
				}
			}
		})
	}
}

// yamlBlockSettings returns the `key: value` lines directly under a top-level YAML block, comments
// and surrounding whitespace stripped, in file order.
func yamlBlockSettings(yaml, block string) []string {
	var settings []string
	in := false
	for _, line := range strings.Split(yaml, "\n") {
		if !strings.HasPrefix(line, " ") && strings.TrimSpace(line) != "" {
			in = strings.HasPrefix(line, block+":")
			continue
		}
		if !in {
			continue
		}
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if s := strings.TrimSpace(line); s != "" {
			settings = append(settings, s)
		}
	}
	return settings
}
