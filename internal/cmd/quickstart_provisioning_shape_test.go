package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// shellFnsFrom extracts the named functions from quickstart.sh and prefixes the script's own shell
// options, so a fragment is exercised the way production exercises it. Testing shell under
// different options than the product sets is how a construct can be green in CI and fatal on a
// real install.
func shellFnsFrom(t *testing.T, names ...string) string {
	t.Helper()
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "set -euo pipefail") {
		t.Fatal("quickstart.sh no longer sets `set -euo pipefail`; these harnesses assume it")
	}
	out := "set -euo pipefail\n"
	for _, n := range names {
		fn := extractShellFunction(content, n)
		if fn == "" {
			t.Fatalf("could not extract %s() from quickstart.sh", n)
		}
		out += fn + "\n"
	}
	return out
}

// TestCredentialWriteSurvivesTheRealCallShape reproduces the production call shape exactly:
// `_generate_telemetry_password > "$root_pass_file"`, under the script's own options, writing to a
// real file. Asserting on captured stdout is not enough — the failure this guards against destroys
// the FILE while producing no output and no message at all.
func TestCredentialWriteSurvivesTheRealCallShape(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "telemetry.root")

	script := shellFnsFrom(t, "_telemetry_password_is_compliant", "_generate_telemetry_password") + `
_generate_telemetry_password > "` + target + `"
echo "WROTE"`

	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("the real call shape aborted (%v). Under pipefail+errexit a SIGPIPE inside the "+
			"generator kills the script mid-redirect, leaving the credential file truncated and "+
			"printing nothing:\n%s", err, out)
	}
	if !strings.Contains(string(out), "WROTE") {
		t.Errorf("execution never reached the line after the write; output: %q", out)
	}

	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("reading the credential file: %v", err)
	}
	pw := strings.TrimSpace(string(got))
	if pw == "" {
		t.Fatal("the credential file is EMPTY after generation. `[ ! -s file ]` treats 0 bytes as " +
			"absent, so every re-run repeats the failure, and the backend can never start")
	}
	if missing := openObservePasswordPolicy(pw); len(missing) > 0 {
		t.Errorf("the written credential (%d chars) fails the backend policy, missing: %s",
			len(pw), strings.Join(missing, ", "))
	}
}

// TestSeededTelemetryEndpointCarriesTheOrgSegment gives B2 the enforcement it lacked. The seeded
// values are the entire fix — no Go code changed — so with nothing asserting them, the fix could be
// reverted with both suites staying green.
//
// The two values must move together: the exporter joins endpoint + traces path, so splitting the
// organisation segment across them keeps the af plane working while the agent sessions' own usage
// events, which derive their address from the base alone, go to an unserved path.
func TestSeededTelemetryEndpointCarriesTheOrgSegment(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	setup := extractShellFunction(string(data), "setup_telemetry")
	if setup == "" {
		t.Fatal("could not extract setup_telemetry()")
	}

	// Scope to the SEED heredoc. The migration branch legitimately mentions the old values in order
	// to recognise a pre-fix config, so asserting over the whole function would fail on a correct
	// implementation.
	const seedStart = `cat > ".agentfactory/telemetry.json" << EOF`
	i := strings.Index(setup, seedStart)
	if i < 0 {
		t.Fatal("could not locate the telemetry.json seed heredoc in setup_telemetry()")
	}
	rest := setup[i+len(seedStart):]
	j := strings.Index(rest, "\nEOF")
	if j < 0 {
		t.Fatal("unterminated seed heredoc")
	}
	seed := rest[:j]

	if !strings.Contains(seed, `"endpoint": "http://127.0.0.1:$TELEMETRY_PORT/api/default"`) {
		t.Errorf(`the seeded endpoint does not end in /api/default. The agent sessions derive their `+
			`own addresses by appending /v1/{signal} to this base, so without the organisation `+
			`segment the per-request token counts — the whole point of the feature — post to a path `+
			`the backend answers 404 to. Seed block was:%s`, seed)
	}
	if !strings.Contains(seed, `"otlp_http_path_traces": "/v1/traces"`) {
		t.Error(`the seeded traces path is not "/v1/traces". It must move in step with the endpoint: ` +
			`the exporter concatenates the two, so carrying the organisation segment in both yields ` +
			`/api/default/api/default/v1/traces`)
	}
	// The old broken pair must not reappear IN THE SEED.
	if strings.Contains(seed, `"otlp_http_path_traces": "/api/default/v1/traces"`) {
		t.Error("the seeded traces path still carries the organisation segment; combined with an " +
			"endpoint that also carries it, the af-plane URL doubles the segment")
	}
}

// TestExistingFactoryConfigIsMigrated pins the migration branch, which had no test at all: the seed
// is write-if-absent, so without it the fix reaches no factory that was ever provisioned — and per
// the defect's own nature, that is every factory installed before it.
func TestExistingFactoryConfigIsMigrated(t *testing.T) {
	// The REAL function is extracted and executed. An earlier version of this test ran its own
	// pasted copy of the sed, which meant the shipped rewrite could be changed to write any address
	// at all with the whole suite still green — the same asymmetry the review objected to.
	fns := shellFnsFrom(t, "_migrate_telemetry_endpoint")

	writePre := func(t *testing.T, dir, endpoint, tracesPath string) string {
		t.Helper()
		cfg := filepath.Join(dir, "telemetry.json")
		pre := `{
  "endpoint": "` + endpoint + `",
  "otlp_http_path_traces": "` + tracesPath + `",
  "headers": { "Authorization": "file:.agentfactory/secrets/telemetry.auth" },
  "protocol": "http/json",
  "export_timeout_ms": 500,
  "resource_attributes_extra": {}
}
`
		if err := os.WriteFile(cfg, []byte(pre), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return cfg
	}

	run := func(t *testing.T, cfg string) (int, string) {
		t.Helper()
		script := fns + "\n_migrate_telemetry_endpoint " + shellQuote(cfg) + " 5080\n"
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		code := 0
		if err != nil {
			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("running the migration: %v\n%s", err, out)
			}
			code = ee.ExitCode()
		}
		return code, string(out)
	}

	t.Run("repairs the shipped pre-fix pair", func(t *testing.T) {
		cfg := writePre(t, t.TempDir(), "http://127.0.0.1:5080", "/api/default/v1/traces")
		if code, out := run(t, cfg); code != 0 {
			t.Fatalf("migration returned %d, want 0 (repaired)\n%s", code, out)
		}
		got, err := os.ReadFile(cfg)
		if err != nil {
			t.Fatalf("read back: %v", err)
		}
		s := string(got)
		// Asserting the exact destination values is what makes this an interlock: a rewrite that
		// runs cleanly but writes the wrong address fails here.
		if !strings.Contains(s, `"endpoint": "http://127.0.0.1:5080/api/default"`) {
			t.Errorf("migration did not move the organisation segment into the endpoint:\n%s", s)
		}
		if !strings.Contains(s, `"otlp_http_path_traces": "/v1/traces"`) {
			t.Errorf("migration did not standardise the traces path:\n%s", s)
		}
		if _, err := os.Stat(cfg + ".bak"); err == nil {
			t.Error("the migration left a .bak file in the operator's config directory")
		}
		// The af-plane URL must be unchanged by the move — that property is what makes it safe.
		if strings.Contains(s, "/api/default/api/default") {
			t.Error("the organisation segment is now doubled")
		}
	})

	// An operator who edited either value must be left alone, which is the whole reason the guard
	// matches the shipped pair exactly rather than pattern-matching the shape.
	for _, tc := range []struct {
		name, endpoint, tracesPath string
	}{
		{"operator changed the endpoint", "http://otel.internal:4318", "/api/default/v1/traces"},
		{"operator changed the traces path", "http://127.0.0.1:5080", "/v1/traces"},
		{"already migrated", "http://127.0.0.1:5080/api/default", "/v1/traces"},
	} {
		t.Run(tc.name+" is left untouched", func(t *testing.T) {
			dir := t.TempDir()
			cfg := writePre(t, dir, tc.endpoint, tc.tracesPath)
			before, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			code, out := run(t, cfg)
			if code != 1 {
				t.Fatalf("migration returned %d, want 1 (nothing to repair)\n%s", code, out)
			}
			after, err := os.ReadFile(cfg)
			if err != nil {
				t.Fatalf("read back: %v", err)
			}
			if string(before) != string(after) {
				t.Errorf("an operator-edited config was rewritten:\nbefore:\n%s\nafter:\n%s", before, after)
			}
		})
	}

	t.Run("an absent config is not an error", func(t *testing.T) {
		if code, out := run(t, filepath.Join(t.TempDir(), "telemetry.json")); code != 1 {
			t.Fatalf("migration returned %d for an absent config, want 1\n%s", code, out)
		}
	})
}

// --- #598 Phase 3b: the seeded gateway config must satisfy the gate quickstart itself runs -------

// litellmSeedEntry is one `- model_name:` block of the seeded litellm.yaml: the id the gateway
// advertises and the backend it routes to.
type litellmSeedEntry struct {
	name    string
	backend string
}

// setupLitellmSource returns the body of setup_litellm() from the REAL quickstart.sh.
//
// extractShellFunction, not shellFnsFrom: this test READS the function, it must never execute it.
// setup_litellm pip-installs litellm, writes secrets, launches tmux and makes billable calls.
func setupLitellmSource(t *testing.T, moduleRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(moduleRoot, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	setup := extractShellFunction(string(data), "setup_litellm")
	if setup == "" {
		t.Fatal("could not extract setup_litellm() from quickstart.sh")
	}
	return setup
}

// litellmSeedBlock returns the body of the litellm.yaml seed heredoc.
//
// Scoping matters twice over. setup_litellm() contains a SECOND heredoc — the login-shell relaunch
// guard appended to ~/.bash_profile — so asserting over the whole function would read yaml rules out
// of a bash fragment. And unlike the telemetry seed this one is QUOTED (<< 'EOF'), so nothing inside
// expands: the block is literal text and the delimiter string must carry the inner single quotes.
func litellmSeedBlock(t *testing.T, setup string) string {
	t.Helper()
	const seedStart = `cat > ".agentfactory/litellm.yaml" << 'EOF'`
	i := strings.Index(setup, seedStart)
	if i < 0 {
		t.Fatalf("could not locate the litellm.yaml seed heredoc in setup_litellm(); expected the line %q "+
			"(the quoting is load-bearing — an unquoted << EOF would expand $ inside the seed)", seedStart)
	}
	rest := setup[i+len(seedStart):]
	j := strings.Index(rest, "\nEOF")
	if j < 0 {
		t.Fatal("unterminated litellm.yaml seed heredoc")
	}
	return rest[:j]
}

// parseLitellmSeedEntries reads the seed's model_list as (advertised id → backend) pairs.
//
// A line scan rather than a yaml parse: the root module ships no yaml library and ADR-013 forbids an
// agent adding one. `^\s+model:` cannot collide with `  - model_name:` because a dash follows the
// indent there, and the trailing `#` comments the seed carries are stripped with the value.
func parseLitellmSeedEntries(t *testing.T, seed string) []litellmSeedEntry {
	t.Helper()
	nameRe := regexp.MustCompile(`^\s*-\s*model_name:\s*([^\s#]+)`)
	backendRe := regexp.MustCompile(`^\s+model:\s*([^\s#]+)`)

	var entries []litellmSeedEntry
	for _, line := range strings.Split(seed, "\n") {
		if m := nameRe.FindStringSubmatch(line); m != nil {
			entries = append(entries, litellmSeedEntry{name: strings.Trim(m[1], `"'`)})
			continue
		}
		if m := backendRe.FindStringSubmatch(line); m != nil {
			if len(entries) == 0 {
				t.Fatalf("seed declares a backend before any model_name: %q", line)
			}
			last := &entries[len(entries)-1]
			if last.backend != "" {
				t.Fatalf("model_name %q declares two backends; the second is %q", last.name, m[1])
			}
			last.backend = strings.Trim(m[1], `"'`)
		}
	}
	if len(entries) == 0 {
		t.Fatal("the litellm.yaml seed advertises no model_name at all")
	}
	return entries
}

// freshBootstrapRegistry rebuilds the models.json a fresh `./quickstart.sh --litellm` leaves behind:
// install.go's scaffold literal plus the codex profile setup_litellm injects with jq. Both halves are
// parsed out of the shipped sources, never copied — a hand-written registry here would assert the
// test's own assumptions instead of the artifact's behaviour.
func freshBootstrapRegistry(t *testing.T, moduleRoot, setup string) *config.ModelsConfig {
	t.Helper()
	src, err := os.ReadFile(filepath.Join(moduleRoot, "internal", "cmd", "install.go"))
	if err != nil {
		t.Fatalf("reading install.go: %v", err)
	}
	var reg config.ModelsConfig
	literal := extractScaffoldLiteral(t, string(src), `"models.json":`)
	if err := json.Unmarshal([]byte(literal), &reg); err != nil {
		t.Fatalf("scaffold models.json literal is not valid JSON: %v", err)
	}

	m := regexp.MustCompile(`(?s)\.models\.codex = (\{.*?\})`).FindStringSubmatch(setup)
	if m == nil {
		t.Fatal("could not locate the codex jq injection in setup_litellm()")
	}
	// $url is a jq variable, not JSON. The port is irrelevant to coverage; loopback is what keeps the
	// profile launchable without a fitness attestation.
	var codex map[string]string
	if err := json.Unmarshal([]byte(strings.Replace(m[1], "$url", `"http://localhost:4000"`, 1)), &codex); err != nil {
		t.Fatalf("the codex jq injection is not a JSON object: %v", err)
	}
	reg.Models["codex"] = codex
	return &reg
}

// TestQuickstartLitellmSeedCarriesClaudeAliases is the drift half of #598 AC-3, and the only thing
// standing between a hardened `check` and a bootstrap that aborts on its own gate.
//
// Four claude-* ids on any factory are asked for by the host BY NAME — no ANTHROPIC_* key redirects
// them — so only a gateway alias can answer them, and Phase 3a's check demands them by exact match.
// quickstart.sh:915-919 runs that very check, so a seed missing one turns `./quickstart.sh --litellm`
// into `exit 1`. Every Phase-3a fixture hand-writes both the profile and the served list, which is
// exactly how the check could be hardened past the seed with `make test` fully green.
//
// The demanded set is therefore taken from PRODUCTION directProfileClaudeIDs over the real scaffold,
// never from a list written here: add a claude-* id to install.go:186 and this test fails until the
// seed follows.
func TestQuickstartLitellmSeedCarriesClaudeAliases(t *testing.T) {
	// Every source read happens BEFORE setupConfigFactory, which chdirs into a temp root that has no
	// go.mod above it — findModuleRoot resolves against the working directory.
	moduleRoot := findModuleRoot(t)
	setup := setupLitellmSource(t, moduleRoot)
	entries := parseLitellmSeedEntries(t, litellmSeedBlock(t, setup))
	reg := freshBootstrapRegistry(t, moduleRoot, setup)

	// Adding aliases must not turn the seed into a rewrite. The guard is what makes a rerun safe, and
	// an operator who has tuned their gateway would lose that work silently — the seed writes no
	// backup and says nothing.
	if !strings.Contains(setup, `if [ ! -f ".agentfactory/litellm.yaml" ]; then`) {
		t.Error("the seed-when-absent guard is gone from setup_litellm(); a rerun would overwrite an " +
			"operator's edited litellm.yaml")
	}

	backendOf := map[string]string{}
	served := make([]string, 0, len(entries))
	for _, e := range entries {
		backendOf[e.name] = e.backend
		served = append(served, e.name)
	}

	// The two backends are read off the codex profile the same seed injects — the main model it puts
	// agents on, and the small model it names for background calls. Naming them here instead would
	// let the seed move to a different pair with this test still passing.
	codex := reg.Models["codex"]
	mainBackend, smallBackend := backendOf[codex["ANTHROPIC_MODEL"]], backendOf[codex["ANTHROPIC_DEFAULT_HAIKU_MODEL"]]
	if mainBackend == "" || smallBackend == "" {
		t.Fatalf("the seed does not advertise the models the codex profile names: main %q → %q, small %q → %q",
			codex["ANTHROPIC_MODEL"], mainBackend, codex["ANTHROPIC_DEFAULT_HAIKU_MODEL"], smallBackend)
	}

	demanded := directProfileClaudeIDs(reg)
	if len(demanded) == 0 {
		t.Fatal("the fresh-bootstrap registry names no claude-* id at all, so every assertion below would " +
			"pass on an empty set — the scaffold's direct profiles are what make the aliases necessary")
	}
	for _, id := range demanded {
		backend, ok := backendOf[id]
		if !ok {
			t.Errorf("the seeded litellm.yaml advertises no %q alias, so a fresh `./quickstart.sh --litellm` "+
				"fails its own `af config models check codex` gate (quickstart.sh:915-919); seed advertises %v",
				id, served)
			continue
		}
		// The host sizes its context window by the claude- id it asked for, so aliasing one of these to the
		// small backend trades an unserved-model failure for an over-window one.
		if backend != mainBackend {
			t.Errorf("alias %q routes to %q, want the main backend %q — a claude-* id aliased to the small "+
				"model creates a new over-window failure class (design-doc.md:280)", id, backend, mainBackend)
		}
	}

	// The haiku-class entry is defence-in-depth for a host that asks for a haiku id by name. No check
	// row can demand it — no direct profile declares a haiku id, so directProfileClaudeIDs never
	// collects one — which makes this assertion the only thing that keeps it in the seed.
	haiku := ""
	for _, e := range entries {
		if strings.HasPrefix(e.name, "claude-haiku") {
			haiku = e.name
			if e.backend != smallBackend {
				t.Errorf("haiku-class alias %q routes to %q, want the small backend %q", e.name, e.backend, smallBackend)
			}
			// Enumerated per-id, never a glob: modelIDPresent matches exactly, so a literal wildcard would
			// satisfy no request, and LiteLLM wildcard routing was rejected unverified (design-doc.md:293).
			if strings.Contains(e.name, "*") {
				t.Errorf("haiku-class alias %q is a wildcard; aliases are enumerated per-id (security.md:153)", e.name)
			}
		}
	}
	if haiku == "" {
		t.Errorf("the seed advertises no claude-haiku* alias (integration.md:98); seed advertises %v", served)
	}

	// The fresh-bootstrap shape: the registry that bootstrap leaves behind, probed against the ids that
	// same bootstrap's yaml advertises, through the REAL check. Nothing here is hand-written, so this
	// passes only if the two shipped artifacts genuinely agree.
	root := setupConfigFactory(t)
	writeValidModels(t, root, reg)
	writeSecretFile(t, root, ".agentfactory/secrets/litellm.key", "sk-litellm-testvalue")
	stubModelsProbe(t, served, nil)

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err != nil {
		t.Fatalf("a fresh --litellm bootstrap must pass the gate it runs itself (quickstart.sh:915-919): %v\n%s", err, out)
	}
	// A clean exit alone would also be produced by a check that stopped emitting alias rows entirely, so
	// require each demanded id to have been reported on — the exit code and the reasoning both.
	for _, id := range demanded {
		if !strings.Contains(out, id) {
			t.Errorf("the check reported no verdict for %q; a silent pass is the hole this surface exists to "+
				"close:\n%s", id, out)
		}
	}
}
