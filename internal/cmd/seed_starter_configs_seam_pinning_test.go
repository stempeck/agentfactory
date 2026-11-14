package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

const seamNoSource = "/nonexistent/ignored-on-fallback"

const seamFallbackNotice = "Not seeded (agentfactory source tree not found): dispatch.json, litellm.yaml, models.json, startup.json — set AF_SOURCE_ROOT to your agentfactory clone and re-run af install --init to seed them from its .agentfactory/ configs"

const seamCronDispatch = `{"repos":["marker-t1-source"],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800,"crons":[{"name":"vault-audit","agent":"manager","every":"3d"}]}`

const seamUnknownStartup = `{"bogus_startup":1,"agents":["manager"],"quality":"high","fidelity":"strict","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":["manager"]}`

func seamManifest(over map[string]string, drop ...string) map[string]string {
	m := make(map[string]string, len(t1SeedManifest))
	for name, content := range t1SeedManifest {
		m[name] = content
	}
	for name, content := range over {
		m[name] = content
	}
	for _, name := range drop {
		delete(m, name)
	}
	return m
}

func seamFreshFactory(t *testing.T) (root, configDir string) {
	t.Helper()
	root = t.TempDir()
	configDir = config.ConfigDir(root)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	return root, configDir
}

func seamSeed(configDir, afSrc string, fallback bool) (string, error) {
	out, _, err := seamSeedWarn(configDir, afSrc, fallback)
	return out, err
}

func seamSeedWarn(configDir, afSrc string, fallback bool) (out, warn string, err error) {
	var outBuf, warnBuf bytes.Buffer
	err = seedStarterConfigs(&outBuf, &warnBuf, configDir, afSrc, fallback)
	return outBuf.String(), warnBuf.String(), err
}

func seamEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func seamCloneFile(afSrc, name string) string {
	return filepath.Join(afSrc, ".agentfactory", name)
}

func seamRestoreRemedy(afSrc, name string) string {
	return "— restore " + seamCloneFile(afSrc, name) +
		" (incomplete agentfactory checkout), or point AF_SOURCE_ROOT at a complete agentfactory clone"
}

func seamFixRemedy(afSrc, name string) string {
	return "— fix " + seamCloneFile(afSrc, name) +
		" in the source clone, or rebuild af from that clone (make install) if it is newer than this binary"
}

func seamLogLines(t *testing.T, configDir string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, ".config-seed.log"))
	if err != nil {
		t.Fatalf("reading .config-seed.log: %v", err)
	}
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// Matched on the name field, not a substring: litellm.yaml's not-seeded line also
// names models.json.
func seamLogLineFor(lines []string, name string) (string, bool) {
	for _, line := range lines {
		if f := strings.Fields(line); len(f) > 1 && f[1] == name {
			return line, true
		}
	}
	return "", false
}

func seamPopulateAll(t *testing.T, configDir string) map[string][]byte {
	t.Helper()
	want := make(map[string][]byte, len(seedConfigNames))
	for _, name := range seedConfigNames {
		data := []byte("operator-owned " + name + "\n")
		if strings.HasSuffix(name, ".json") {
			data = []byte(`{"operator_owned":"` + name + `"}` + "\n")
		}
		if err := os.WriteFile(filepath.Join(configDir, name), data, 0644); err != nil {
			t.Fatal(err)
		}
		want[name] = data
	}
	return want
}

// Crons from a clone dispatch.json must not arm the dispatcher in a fresh factory.
func TestSeedStarterConfigs_CloneChannelCronsDropped(t *testing.T) {
	root, configDir := seamFreshFactory(t)
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{"dispatch.json": seamCronDispatch}))
	if _, err := seamSeed(configDir, src, false); err != nil {
		t.Fatalf("seedStarterConfigs (clone channel): %v", err)
	}

	var seeded map[string]json.RawMessage
	if err := json.Unmarshal(readSeeded(t, configDir, "dispatch.json"), &seeded); err != nil {
		t.Fatalf("seeded dispatch.json does not decode: %v", err)
	}
	if crons, ok := seeded["crons"]; ok {
		t.Errorf("seeded dispatch.json still carries crons %s; every dispatcher trigger section must be blanked", crons)
	}

	fake, _ := setupHermeticSessions(t)
	if err := startDispatch(&cobra.Command{}, root, fake); err != nil {
		t.Fatalf("startDispatch over the seeded dispatch.json must friendly-skip, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("a dispatch.json seeded from a clone carrying crons launched the dispatcher; ops=%v", fake.ops)
	}
}

// The seed loads as "not configured" even when every trigger section is populated.
func TestSeedStarterConfigs_SeededDispatchLoadsAsNotConfigured(t *testing.T) {
	const allTriggers = `{"repos":["marker-t1-source"],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"source":"issue","agent":"manager"}],"interval_seconds":300,"retry_after_seconds":1800,"crons":[{"name":"vault-audit","agent":"manager","every":"3d"}]}`
	root, configDir := seamFreshFactory(t)
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{"dispatch.json": allTriggers}))
	if _, err := seamSeed(configDir, src, false); err != nil {
		t.Fatalf("seedStarterConfigs (clone channel): %v", err)
	}

	if _, err := config.LoadDispatchConfig(root); !errors.Is(err, config.ErrMissingField) {
		t.Errorf("LoadDispatchConfig over the seeded dispatch.json = %v, want an error wrapping config.ErrMissingField (the friendly-skip sentinel)", err)
	}
}

// An unknown key in a to-be-written clone config is refused and nothing is written.
func TestSeedStarterConfigs_UnknownKeyRefusedNothingWritten(t *testing.T) {
	cases := []struct {
		name, file, key, content string
	}{
		{"dispatch", "dispatch.json", "bogus_dispatch",
			`{"bogus_dispatch":1,"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`},
		{"factory", "factory.json", "bogus_factory",
			`{"bogus_factory":1,"type":"factory","version":1,"name":"marker-t1-source","max_worktrees":8}`},
		{"models", "models.json", "bogus_models",
			`{"bogus_models":1,"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-t1-marker"}},"agents":{"manager":"default"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, configDir := seamFreshFactory(t)
			src := newAFSourceWithConfigs(t, seamManifest(map[string]string{tc.file: tc.content}))
			_, err := seamSeed(configDir, src, false)
			if err == nil {
				t.Fatalf("unknown key %q in the clone's %s was accepted; written: %v", tc.key, tc.file, seamEntries(t, configDir))
			}
			msg := err.Error()
			for _, want := range []string{`unknown field "` + tc.key + `"`, tc.file, "make install", seamFixRemedy(src, tc.file)} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal %q does not contain %q", msg, want)
				}
			}
			if got := seamEntries(t, configDir); len(got) != 0 {
				t.Errorf("refusal must leave configDir empty, found %v", got)
			}
		})
	}

	t.Run("startup_control", func(t *testing.T) {
		_, configDir := seamFreshFactory(t)
		src := newAFSourceWithConfigs(t, seamManifest(map[string]string{"startup.json": seamUnknownStartup}))
		_, err := seamSeed(configDir, src, false)
		if err == nil {
			t.Fatalf("unknown key in the clone's startup.json was accepted; written: %v", seamEntries(t, configDir))
		}
		if !strings.Contains(err.Error(), `unknown field "bogus_startup"`) {
			t.Errorf("refusal %q does not name the unknown key", err)
		}
		if got := seamEntries(t, configDir); len(got) != 0 {
			t.Errorf("refusal must leave configDir empty, found %v", got)
		}
	})
}

// On a fully populated factory a drifted clone is never read, so --init is a silent no-op.
func TestSeedStarterConfigs_PopulatedFactoryDriftedCloneIsNoop(t *testing.T) {
	cases := []struct {
		name   string
		over   map[string]string
		drop   []string
		onDisk map[string]string
	}{
		{name: "missing_litellm_yaml", drop: []string{"litellm.yaml"}},
		{name: "invalid_json_dispatch", over: map[string]string{"dispatch.json": `{not json`}},
		{name: "invalid_on_disk_agents_json", onDisk: map[string]string{"agents.json": "operator-owned agents.json (not JSON)\n"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, configDir := seamFreshFactory(t)
			want := seamPopulateAll(t, configDir)
			for name, content := range tc.onDisk {
				if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
				want[name] = []byte(content)
			}
			src := newAFSourceWithConfigs(t, seamManifest(tc.over, tc.drop...))

			out, err := seamSeed(configDir, src, false)
			if err != nil {
				t.Errorf("a populated factory must be a no-op, got error: %v", err)
			}
			for name, data := range want {
				got, readErr := os.ReadFile(filepath.Join(configDir, name))
				if readErr != nil || !bytes.Equal(got, data) {
					t.Errorf("%s changed: got %q (err %v), want untouched %q", name, got, readErr, data)
				}
			}
			if out != "" {
				t.Errorf("a no-op run must print nothing, got stdout %q", out)
			}
		})
	}
}

// An agents.json that exists but cannot be read is a hard error, never a silent "fresh" roster.
func TestSeedStarterConfigs_UnreadableExistingAgentsJSONRefused(t *testing.T) {
	_, configDir := seamFreshFactory(t)
	// A directory makes the read fail regardless of file modes, which root would bypass.
	if err := os.Mkdir(filepath.Join(configDir, "agents.json"), 0755); err != nil {
		t.Fatal(err)
	}

	_, err := seamSeed(configDir, seamNoSource, true)
	if err == nil {
		t.Errorf("an unreadable existing agents.json must abort the seed, got nil error")
	} else if !strings.Contains(err.Error(), "agents.json") {
		t.Errorf("refusal %q does not name agents.json", err)
	}
	if got := seamEntries(t, configDir); len(got) != 1 || got[0] != "agents.json" {
		t.Errorf("refusal must write nothing new, configDir holds %v", got)
	}
}

// Every fresh-factory clone refusal names the clone file and the remedy for its class.
func TestSeedStarterConfigs_FreshRefusalNamesClonePathAndRemedy(t *testing.T) {
	cases := []struct {
		name   string
		over   map[string]string
		drop   []string
		file   string
		remedy func(afSrc, name string) string
		want   string
	}{
		{name: "missing_member", file: "models.json", remedy: seamRestoreRemedy, drop: []string{"models.json"}},
		{name: "invalid_json", file: "dispatch.json", remedy: seamFixRemedy, over: map[string]string{"dispatch.json": `{not json`}},
		{name: "dangling_agent_ref", file: "startup.json", remedy: seamFixRemedy, want: "ghost", over: map[string]string{
			"startup.json": `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":["ghost"]}`}},
		{name: "startup_loader_rule", file: "startup.json", remedy: seamFixRemedy, want: `startup fidelity must be "on", "off", or "default", got "strict"`, over: map[string]string{
			"startup.json": `{"agents":["manager"],"quality":"default","fidelity":"strict","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":["manager"]}`}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, configDir := seamFreshFactory(t)
			src := newAFSourceWithConfigs(t, seamManifest(tc.over, tc.drop...))
			_, err := seamSeed(configDir, src, false)
			if err == nil {
				t.Fatalf("expected a refusal; written: %v", seamEntries(t, configDir))
			}
			msg := err.Error()
			if path := seamCloneFile(src, tc.file); !strings.Contains(msg, path) {
				t.Errorf("refusal %q does not name the clone file %s", msg, path)
			}
			if remedy := tc.remedy(src, tc.file); !strings.Contains(msg, remedy) {
				t.Errorf("refusal %q does not carry the remedy %q", msg, remedy)
			}
			if !strings.Contains(msg, tc.want) {
				t.Errorf("refusal %q does not name its cause %q", msg, tc.want)
			}
			if got := seamEntries(t, configDir); len(got) != 0 {
				t.Errorf("refusal must leave configDir empty, found %v", got)
			}
		})
	}
}

// A refused candidate in a partially populated factory writes nothing.
func TestSeedStarterConfigs_PartialFactoryRefusalWritesNothing(t *testing.T) {
	_, configDir := seamFreshFactory(t)
	operatorFactory := []byte(`{"type":"factory","version":1,"name":"operator-kept"}` + "\n")
	if err := os.WriteFile(filepath.Join(configDir, "factory.json"), operatorFactory, 0644); err != nil {
		t.Fatal(err)
	}
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{"startup.json": seamUnknownStartup}))

	if _, err := seamSeed(configDir, src, false); err == nil {
		t.Fatalf("expected the startup.json candidate to be refused")
	}
	if got := seamEntries(t, configDir); len(got) != 1 || got[0] != "factory.json" {
		t.Errorf("refusal wrote into a partial factory: configDir holds %v, want only factory.json", got)
	}
	if got := readSeeded(t, configDir, "factory.json"); !bytes.Equal(got, operatorFactory) {
		t.Errorf("pre-existing factory.json changed: got %q, want %q", got, operatorFactory)
	}
}

// models.json references to undefined profiles are refused at seed time.
func TestSeedStarterConfigs_UndefinedProfileRefsRefused(t *testing.T) {
	cases := []struct {
		name, ref, models string
	}{
		{"orphan_pin", "no-such-profile",
			`{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-t1-marker"}},"agents":{"manager":"no-such-profile"}}`},
		{"orphan_default", "no-such-default",
			`{"default":"no-such-default","models":{"default":{"ANTHROPIC_MODEL":"claude-t1-marker"}},"agents":{"manager":"default"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, configDir := seamFreshFactory(t)
			src := newAFSourceWithConfigs(t, seamManifest(map[string]string{"models.json": tc.models}))
			_, err := seamSeed(configDir, src, false)
			if err == nil {
				t.Fatalf("models.json referencing undefined profile %q was seeded; written: %v", tc.ref, seamEntries(t, configDir))
			}
			msg := err.Error()
			for _, want := range []string{"models.json", tc.ref, seamFixRemedy(src, "models.json")} {
				if !strings.Contains(msg, want) {
					t.Errorf("refusal %q does not contain %q", msg, want)
				}
			}
			if got := seamEntries(t, configDir); len(got) != 0 {
				t.Errorf("refusal must leave configDir empty, found %v", got)
			}
		})
	}
}

// A models.json default naming a non-portable profile is refused.
func TestSeedStarterConfigs_NonPortableDefaultRefused(t *testing.T) {
	const models = `{"default":"codex","models":{"default":{"ANTHROPIC_MODEL":"claude-t1-marker"},"codex":{"ANTHROPIC_BASE_URL":"http://localhost:4000","ANTHROPIC_AUTH_TOKEN":"file:secrets/litellm.key","ANTHROPIC_MODEL":"gpt-t1-marker"}},"agents":{"manager":"default"}}`
	_, configDir := seamFreshFactory(t)
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{"models.json": models}))

	_, err := seamSeed(configDir, src, false)
	if err == nil {
		t.Fatalf("non-portable default \"codex\" was seeded; written: %v", seamEntries(t, configDir))
	}
	msg := err.Error()
	for _, want := range []string{
		`models.json: default "codex" names a non-portable profile (gateway endpoint or file: secret reference)`,
		seamFixRemedy(src, "models.json"),
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("refusal %q does not contain %q", msg, want)
		}
	}
	if got := seamEntries(t, configDir); len(got) != 0 {
		t.Errorf("refusal must leave configDir empty, found %v", got)
	}
}

// A fresh seed from a clone whose startup.json says telemetry "on" leaves
// telemetry off at the next af up.
func TestSeedStarterConfigs_FreshSeedDoesNotArmTelemetry(t *testing.T) {
	root, configDir := seamFreshFactory(t)
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{
		"startup.json": `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"on","start_dispatch":false,"watchdog_agents":["manager"]}`,
	}))
	if _, err := seamSeed(configDir, src, false); err != nil {
		t.Fatalf("seedStarterConfigs (clone channel): %v", err)
	}

	var seeded map[string]any
	if err := json.Unmarshal(readSeeded(t, configDir, "startup.json"), &seeded); err != nil {
		t.Fatalf("seeded startup.json does not decode: %v", err)
	}
	if seeded["telemetry"] != "default" {
		t.Errorf("seeded startup.json telemetry = %v, want \"default\" (no telemetry.json exists)", seeded["telemetry"])
	}

	st, err := config.LoadStartupConfig(root)
	if err != nil {
		t.Fatalf("LoadStartupConfig over the seed: %v", err)
	}
	if err := applyGate(root, root, "telemetry", st.Telemetry); err != nil {
		t.Fatalf("applyGate(telemetry=%q): %v", st.Telemetry, err)
	}
	var warn bytes.Buffer
	env := telemetryLaunchEnv(root, config.AgentDir(root, "manager"), "manager", "", &warn)
	for _, e := range env {
		if e.Key == "CLAUDE_CODE_ENABLE_TELEMETRY" && e.Value == "1" {
			t.Errorf("a freshly seeded factory arms telemetry at launch (startup.telemetry=%q); env=%v", st.Telemetry, env)
		}
	}
}

// A fresh seed from a clone whose startup.json says improvement "on" leaves the factory
// improvement hook unarmed at the next af up.
func TestSeedStarterConfigs_FreshSeedDoesNotArmImprovementHook(t *testing.T) {
	root, configDir := seamFreshFactory(t)
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{
		"startup.json": `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"on","telemetry":"off","start_dispatch":false,"watchdog_agents":["manager"]}`,
	}))
	if _, err := seamSeed(configDir, src, false); err != nil {
		t.Fatalf("seedStarterConfigs (clone channel): %v", err)
	}

	st, err := config.LoadStartupConfig(root)
	if err != nil {
		t.Fatalf("LoadStartupConfig over the seed: %v", err)
	}
	if err := applyGate(root, root, "improvement", st.Improvement); err != nil {
		t.Fatalf("applyGate(improvement=%q): %v", st.Improvement, err)
	}
	if improvementFactoryEnabled(root) {
		t.Errorf("a freshly seeded factory armed .improvement-hook (startup.improvement=%q)", st.Improvement)
	}
}

// telemetry.json is never seeded, even from a clone that has one.
func TestSeedStarterConfigs_TelemetryJSONNeverSeeded(t *testing.T) {
	_, configDir := seamFreshFactory(t)
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{"telemetry.json": `{"enabled":true}`}))
	if _, err := seamSeed(configDir, src, false); err != nil {
		t.Fatalf("seedStarterConfigs (clone channel): %v", err)
	}
	if _, err := os.Stat(filepath.Join(configDir, "telemetry.json")); !os.IsNotExist(err) {
		t.Errorf("telemetry.json must never be seeded (stat err = %v)", err)
	}
}

// A no-op re-run is silent on stdout and appends nothing to the log: it wrote nothing, so a
// line per kept member would grow the log forever without telling anyone anything.
func TestSeedStarterConfigs_NoOpRerunStdoutSilent(t *testing.T) {
	_, configDir := seamFreshFactory(t)
	src := writeT1SourceTree(t)
	if _, err := seamSeed(configDir, src, false); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstRunLines := len(seamLogLines(t, configDir))
	second, err := seamSeed(configDir, src, false)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}

	t.Run("stdout_silent", func(t *testing.T) {
		if second != "" {
			t.Errorf("a no-op re-run must print nothing (no kept-existing lines, no notice), got %q", second)
		}
	})

	t.Run("log_appends_nothing", func(t *testing.T) {
		if appended := seamLogLines(t, configDir)[firstRunLines:]; len(appended) != 0 {
			t.Errorf("a no-op re-run appended %d log lines: %q", len(appended), appended)
		}
	})
}

func TestSeedStarterConfigs_NoSourceRerunAppendsNoLogLines(t *testing.T) {
	_, configDir := seamFreshFactory(t)
	if _, err := seamSeed(configDir, seamNoSource, true); err != nil {
		t.Fatalf("first run: %v", err)
	}
	firstRunLines := len(seamLogLines(t, configDir))
	second, err := seamSeed(configDir, seamNoSource, true)
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if appended := seamLogLines(t, configDir)[firstRunLines:]; len(appended) != 0 {
		t.Errorf("a re-run that wrote nothing appended %d log lines: %q", len(appended), appended)
	}
	if !strings.Contains(second, seamFallbackNotice) {
		t.Errorf("the re-run dropped the not-seeded notice; stdout:\n%s", second)
	}
}

// Kept-existing files are not validated, so they draw no warnings.
func TestSeedStarterConfigs_NoWarningsForKeptExistingFiles(t *testing.T) {
	_, configDir := seamFreshFactory(t)
	legacy := map[string]string{
		"agents.json":   `{"agents":{"manager":{"type":"interactive","description":"m"},"supervisor":{"type":"autonomous","description":"s"}}}`,
		"dispatch.json": `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[]}`,
		"startup.json":  `{"agents":["manager"],"watchdog_agents":["manager","supervisor"]}`,
	}
	for name, content := range legacy {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	out, warn, err := seamSeedWarn(configDir, seamNoSource, true)
	if err != nil {
		t.Fatalf("seedStarterConfigs over a legacy factory: %v", err)
	}
	for _, kept := range []string{"warning: dispatch.json", "warning: startup.json"} {
		if strings.Contains(out+warn, kept) {
			t.Errorf("the seed warns about a kept-existing file (%q):\nstdout:\n%s\nwarnings:\n%s", kept, out, warn)
		}
	}
}

// Warnings are not provenance: they go to the warn stream (stderr in af install --init), so
// stdout stays the list of what was seeded.
func TestSeedStarterConfigs_WarningsGoToWarnStreamNotStdout(t *testing.T) {
	_, configDir := seamFreshFactory(t)
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{
		"models.json": `{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-t1-marker"},"gem":{"GEMINI_API_KEY":"AIzaPlaintextValue"}},"agents":{"manager":"default"}}`,
	}))
	out, warn, err := seamSeedWarn(configDir, src, false)
	if err != nil {
		t.Fatalf("a plaintext provider key warns, it does not refuse: %v", err)
	}
	if want := seedUnitLintLine("gem", "GEMINI_API_KEY"); !strings.Contains(warn, want) {
		t.Errorf("warn stream %q does not carry %q", warn, want)
	}
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if !strings.Contains(line, ": seeded (") && !strings.HasPrefix(line, "  sanitized: ") {
			t.Errorf("stdout line %q is not provenance:\n%s", line, out)
		}
	}
}

// The no-source-tree notice prints exactly once, and only on that channel.
func TestSeedStarterConfigs_FallbackNotice(t *testing.T) {
	t.Run("fallback_notice_once", func(t *testing.T) {
		_, configDir := seamFreshFactory(t)
		out, err := seamSeed(configDir, seamNoSource, true)
		if err != nil {
			t.Fatalf("seedStarterConfigs (no source tree): %v", err)
		}
		if n := strings.Count(out, seamFallbackNotice); n != 1 {
			t.Errorf("fallback notice printed %d times, want exactly 1; stdout:\n%s", n, out)
		}
	})

	t.Run("clone_channel_no_notice", func(t *testing.T) {
		_, configDir := seamFreshFactory(t)
		out, err := seamSeed(configDir, writeT1SourceTree(t), false)
		if err != nil {
			t.Fatalf("seedStarterConfigs (clone channel): %v", err)
		}
		if strings.Contains(out, "Not seeded") {
			t.Errorf("the clone channel must not print the fallback notice; stdout:\n%s", out)
		}
	})
}

// litellm.yaml is seeded only together with models.json.
func TestSeedStarterConfigs_LitellmNotSeededWhenModelsPreExists(t *testing.T) {
	t.Run("models_preexists_litellm_not_seeded", func(t *testing.T) {
		_, configDir := seamFreshFactory(t)
		if err := os.WriteFile(filepath.Join(configDir, "models.json"), []byte(`{"default":"default","models":{"default":{}}}`+"\n"), 0644); err != nil {
			t.Fatal(err)
		}

		out, err := seamSeed(configDir, writeT1SourceTree(t), false)
		if err != nil {
			t.Fatalf("seedStarterConfigs (clone channel): %v", err)
		}
		if _, err := os.Stat(filepath.Join(configDir, "litellm.yaml")); !os.IsNotExist(err) {
			t.Errorf("litellm.yaml must not be seeded when models.json pre-exists (stat err = %v)", err)
		}
		if log := strings.Join(seamLogLines(t, configDir), "\n"); !strings.Contains(log, "litellm.yaml not-seeded (models.json pre-exists)") {
			t.Errorf(".config-seed.log does not record litellm.yaml as not-seeded:\n%s", log)
		}
		for _, line := range strings.Split(out, "\n") {
			if strings.Contains(line, "litellm.yaml") {
				t.Errorf("stdout must stay silent about litellm.yaml, got line %q", line)
			}
		}
	})

	t.Run("both_absent_both_seeded", func(t *testing.T) {
		_, configDir := seamFreshFactory(t)
		if _, err := seamSeed(configDir, writeT1SourceTree(t), false); err != nil {
			t.Fatalf("seedStarterConfigs (clone channel): %v", err)
		}
		for _, name := range []string{"models.json", "litellm.yaml"} {
			if _, err := os.Stat(filepath.Join(configDir, name)); err != nil {
				t.Errorf("%s not seeded on a fresh factory: %v", name, err)
			}
		}
	})
}

// Each effective rewrite is recorded on its own file's log line and beneath its stdout line.
func TestSeedStarterConfigs_RewriteRecordsOnOwnFileLine(t *testing.T) {
	src := newAFSourceWithConfigs(t, seamManifest(map[string]string{
		"dispatch.json": seamCronDispatch,
		"models.json":   `{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-t1-marker"},"codex":{"ANTHROPIC_BASE_URL":"http://localhost:4000","ANTHROPIC_AUTH_TOKEN":"file:secrets/litellm.key","ANTHROPIC_MODEL":"gpt-t1-marker"}},"agents":{"manager":"codex"}}`,
		"startup.json":  `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"on","start_dispatch":false,"watchdog_agents":["manager"]}`,
	}))

	t.Run("fresh_all_records", func(t *testing.T) {
		root, configDir := seamFreshFactory(t)
		records := map[string][]string{
			"dispatch.json":  {"repos cleared (was: marker-t1-source)", "crons dropped (vault-audit)"},
			"factory.json":   {`name set to "` + filepath.Base(root) + `" (was "marker-t1-source")`},
			"agents.json":    nil,
			"models.json":    {"agent pins dropped (manager)"},
			"startup.json":   {`telemetry "on" -> "default" (no telemetry.json)`},
			"messaging.json": nil,
			"litellm.yaml":   nil,
		}
		out, err := seamSeed(configDir, src, false)
		if err != nil {
			t.Fatalf("seedStarterConfigs (clone channel): %v", err)
		}

		logLines := seamLogLines(t, configDir)
		log := strings.Join(logLines, "\n")
		stdoutLines := strings.Split(out, "\n")
		for name, want := range records {
			line, ok := seamLogLineFor(logLines, name)
			if !ok {
				t.Errorf("no .config-seed.log line for %s:\n%s", name, log)
				continue
			}
			if len(want) == 0 && strings.Contains(line, "sanitized:") {
				t.Errorf("%s was not rewritten but its log line carries a record: %q", name, line)
			}
			if len(want) > 0 && !strings.Contains(line, " sanitized: ") {
				t.Errorf("%s log line %q carries no ` sanitized: ` suffix", name, line)
			}
			for _, r := range want {
				if !strings.Contains(line, r) {
					t.Errorf("%s log line %q lacks its record %q", name, line, r)
				}
				if n := strings.Count(log, r); n != 1 {
					t.Errorf("record %q appears %d times in .config-seed.log, want once (on the %s line only)", r, n, name)
				}
			}

			idx := -1
			for i, l := range stdoutLines {
				if strings.HasPrefix(l, name+": seeded (") {
					idx = i
					break
				}
			}
			if idx < 0 {
				t.Errorf("stdout has no `%s: seeded (...)` line:\n%s", name, out)
				continue
			}
			beneath := ""
			if idx+1 < len(stdoutLines) {
				beneath = stdoutLines[idx+1]
			}
			if len(want) == 0 && strings.HasPrefix(beneath, "  sanitized:") {
				t.Errorf("%s was not rewritten but stdout prints a record beneath it: %q", name, beneath)
			}
			if len(want) > 0 && !strings.HasPrefix(beneath, "  sanitized: ") {
				t.Errorf("stdout line beneath `%s: seeded` is %q, want an indented `  sanitized: ` line", name, beneath)
			}
			for _, r := range want {
				if !strings.Contains(beneath, r) {
					t.Errorf("stdout sanitized line beneath %s (%q) lacks its record %q", name, beneath, r)
				}
				if n := strings.Count(out, r); n != 1 {
					t.Errorf("record %q appears %d times on stdout, want once (beneath %s only)", r, n, name)
				}
			}
		}
	})

	t.Run("kept_existing_factory_no_record", func(t *testing.T) {
		_, configDir := seamFreshFactory(t)
		operatorFactory := []byte(`{"type":"factory","version":1,"name":"operator-kept"}` + "\n")
		if err := os.WriteFile(filepath.Join(configDir, "factory.json"), operatorFactory, 0644); err != nil {
			t.Fatal(err)
		}
		out, err := seamSeed(configDir, src, false)
		if err != nil {
			t.Fatalf("seedStarterConfigs (clone channel): %v", err)
		}

		logLines := seamLogLines(t, configDir)
		line, ok := seamLogLineFor(logLines, "factory.json")
		if !ok {
			t.Fatalf("no .config-seed.log line for factory.json:\n%s", strings.Join(logLines, "\n"))
		}
		if f := strings.Fields(line); len(f) < 3 || f[2] != "kept-existing" || strings.Contains(line, "sanitized:") {
			t.Errorf("factory.json log line = %q, want a kept-existing outcome with no sanitized: record", line)
		}
		if strings.Contains(strings.Join(logLines, "\n"), "name set to") || strings.Contains(out, "name set to") {
			t.Errorf("a kept-existing factory.json must carry no rewrite record anywhere; stdout:\n%s", out)
		}
		for _, l := range strings.Split(out, "\n") {
			if strings.Contains(l, "factory.json") {
				t.Errorf("stdout must stay silent about the kept-existing factory.json, got line %q", l)
			}
		}
		if got := readSeeded(t, configDir, "factory.json"); !bytes.Equal(got, operatorFactory) {
			t.Errorf("pre-existing factory.json changed: got %q, want %q", got, operatorFactory)
		}
	})
}

// A clone member that EXISTS but cannot be read
// gets the "fix it in the clone" remedy, not "restore (incomplete checkout)" — only an absent
// member is a missing one. A directory in the member's place fails the read for every uid,
// unlike a permission bit, which root would bypass.
func TestSeedStarterConfigs_UnreadablePresentCloneMemberGetsFixRemedy(t *testing.T) {
	_, configDir := seamFreshFactory(t)
	src := newAFSourceWithConfigs(t, seamManifest(nil))
	member := seamCloneFile(src, "models.json")
	if err := os.Remove(member); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(member, 0755); err != nil {
		t.Fatal(err)
	}

	_, err := seamSeed(configDir, src, false)
	if err == nil {
		t.Fatalf("an unreadable clone models.json was accepted; written: %v", seamEntries(t, configDir))
	}
	msg := err.Error()
	if want := seamFixRemedy(src, "models.json"); !strings.Contains(msg, want) {
		t.Errorf("refusal %q does not carry the fix remedy %q", msg, want)
	}
	if notWant := seamRestoreRemedy(src, "models.json"); strings.Contains(msg, notWant) {
		t.Errorf("refusal tells the operator to restore a member that is present:\n%s", msg)
	}
	if got := seamEntries(t, configDir); len(got) != 0 {
		t.Errorf("refusal must leave configDir empty, found %v", got)
	}
}

// The loader's mapping and workflow rules apply to a seeded dispatch.json even while it is inert:
// otherwise the seed is written "not configured" and fails only when the operator adds a repo.
var seamArmedLaterRejectedDispatches = []struct {
	label, mapping, workflows, want string
}{
	{"label_and_labels", `{"label":"bug","labels":["bug"],"source":"issue","agent":"fable-implement"}`, ``, "mapping has both label and labels (ambiguous)"},
	{"no_label", `{"source":"issue","agent":"fable-implement"}`, ``, "mapping must have at least one label"},
	{"bad_source", `{"labels":["bug"],"source":"bogus","agent":"fable-implement"}`, ``, `mapping source must be "issue" or "pr", got "bogus"`},
	{"no_agent", `{"labels":["bug"],"source":"issue"}`, ``, "mapping must have an agent"},
	{"workflow_phase_unmapped", `{"labels":["bug"],"source":"issue","agent":"fable-implement"}`, `,"workflows":[{"label":"pipeline","phases":["nope"]}]`,
		`workflow "pipeline" references phase "nope" that is not in any mapping`},
}

func seamDispatchWithMapping(mapping, workflows string) string {
	return `{"repos":["marker-clone"],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[` + mapping + `]` +
		workflows + `,"interval_seconds":300,"retry_after_seconds":1800}`
}

func TestSeedStarterConfigs_DispatchStructRulesEnforcedWhileInert(t *testing.T) {
	for _, c := range seamArmedLaterRejectedDispatches {
		for _, agentsPreExist := range []bool{false, true} {
			t.Run(c.label+map[bool]string{false: "/fresh_agents", true: "/pre_existing_agents"}[agentsPreExist], func(t *testing.T) {
				_, configDir := starterSrcFresh(t)
				if agentsPreExist {
					if err := os.WriteFile(filepath.Join(configDir, "agents.json"), []byte(`{"agents":{"manager":{"type":"interactive","description":"m"}}}`), 0644); err != nil {
						t.Fatal(err)
					}
				}
				src := newAFSourceWithConfigs(t, starterSrcCloneConfigs(map[string]string{"dispatch.json": seamDispatchWithMapping(c.mapping, c.workflows)}))
				_, err := starterSrcSeed(configDir, src, false)
				if err == nil {
					t.Fatalf("seeded a dispatch.json the loader rejects once a repo is added; written: %v", seamEntries(t, configDir))
				}
				for _, want := range []string{"dispatch.json", c.want, seamFixRemedy(src, "dispatch.json")} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("refusal %q missing %q", err, want)
					}
				}
				wantEntries := 0
				if agentsPreExist {
					wantEntries = 1
				}
				if got := seamEntries(t, configDir); len(got) != wantEntries {
					t.Errorf("refusal wrote %v", got)
				}
			})
		}
	}
}

func TestSeedStarterConfigs_SeededDispatchParsesOnceARepoIsAdded(t *testing.T) {
	fixtures := map[string]string{"valid": seamDispatchWithMapping(`{"labels":["bug"],"source":"issue","agent":"fable-implement"}`, ``)}
	for _, c := range seamArmedLaterRejectedDispatches {
		fixtures[c.label] = seamDispatchWithMapping(c.mapping, c.workflows)
	}
	for label, dispatch := range fixtures {
		t.Run(label, func(t *testing.T) {
			_, configDir := starterSrcFresh(t)
			src := newAFSourceWithConfigs(t, starterSrcCloneConfigs(map[string]string{"dispatch.json": dispatch}))
			if _, err := starterSrcSeed(configDir, src, false); err != nil {
				if label == "valid" {
					t.Fatalf("a valid dispatch.json was refused: %v", err)
				}
				return
			}
			var cfg map[string]any
			if err := json.Unmarshal(readSeeded(t, configDir, "dispatch.json"), &cfg); err != nil {
				t.Fatal(err)
			}
			cfg["repos"] = []string{"owner/repo"}
			armed, err := json.Marshal(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := config.ParseDispatchConfig(armed); err != nil {
				t.Errorf("the seeded dispatch.json fails the loader once a repo is added: %v", err)
			}
		})
	}
}

// messaging.json and factory.json are files af holds its own content for, so a clone lacking
// one gets that content, and the provenance says the clone lacked it.
func TestSeedStarterConfigs_CloneLackingAfOwnedMemberSeedsBuiltin(t *testing.T) {
	for _, name := range []string{"messaging.json", "factory.json"} {
		t.Run(name, func(t *testing.T) {
			src := newAFSourceWithConfigs(t, starterSrcCloneConfigs(nil, name))
			root, configDir := starterSrcFresh(t)
			out, err := starterSrcSeed(configDir, src, false)
			if err != nil {
				t.Fatalf("a clone lacking %s refused the bootstrap: %v", name, err)
			}
			absent := "absent from source clone " + seamCloneFile(src, name)
			if !strings.Contains(out, name+": seeded (built-in") || !strings.Contains(out, absent) {
				t.Errorf("stdout does not show %s seeded from af's content with %q:\n%s", name, absent, out)
			}
			if line, ok := seamLogLineFor(seamLogLines(t, configDir), name); !ok || !strings.Contains(line, "built-in") || !strings.Contains(line, absent) {
				t.Errorf(".config-seed.log line for %s = %q, want built-in provenance with %q", name, line, absent)
			}
			switch name {
			case "messaging.json":
				if _, err := config.LoadMessagingConfig(config.MessagingConfigPath(root), builtinAgentsConfig()); err != nil {
					t.Errorf("seeded messaging.json does not load: %v", err)
				}
			case "factory.json":
				var cfg config.FactoryConfig
				starterSrcDecode(t, filepath.Join(configDir, "factory.json"), &cfg)
				if cfg.Name != filepath.Base(root) {
					t.Errorf("seeded factory.json name = %q, want %q", cfg.Name, filepath.Base(root))
				}
			}
		})
	}
}

// Only an absent af-owned member gets af's content: one that is present but unreadable is
// still something to fix in the clone.
func TestSeedStarterConfigs_UnreadableAfOwnedCloneMemberStillRefused(t *testing.T) {
	for _, name := range []string{"messaging.json", "factory.json"} {
		t.Run(name, func(t *testing.T) {
			_, configDir := seamFreshFactory(t)
			src := newAFSourceWithConfigs(t, seamManifest(nil))
			member := seamCloneFile(src, name)
			if err := os.Remove(member); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(member, 0755); err != nil {
				t.Fatal(err)
			}
			_, err := seamSeed(configDir, src, false)
			if err == nil {
				t.Fatalf("an unreadable clone %s was accepted; written: %v", name, seamEntries(t, configDir))
			}
			if want := seamFixRemedy(src, name); !strings.Contains(err.Error(), want) {
				t.Errorf("refusal %q does not carry the fix remedy %q", err, want)
			}
			if got := seamEntries(t, configDir); len(got) != 0 {
				t.Errorf("refusal must leave configDir empty, found %v", got)
			}
		})
	}
}
