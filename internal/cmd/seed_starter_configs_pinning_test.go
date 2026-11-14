package cmd

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// seed_starter_configs_pinning_test.go — fable-implement Phase 5 pinning tests for
// Phase 4 (af-654 CFG-2/CFG-5/CFG-6/CFG-7): T1 (seed-from-source), T3
// (no-overwrite / byte-identity on both channels), the
// provenance-log test, and the dispatcher friendly-skip-over-seeded-config test.
//
// All five call seedStarterConfigs(out, warn io.Writer, configDir, afSrc string, fallback
// bool) error (investigation_report.md row 3), which does not exist until Phase 6
// lands it. RED today: this file fails the internal/cmd package build with
// "undefined: seedStarterConfigs" — a Go compile-time RED, not a runtime one, because
// the function these tests pin does not exist yet. That failure necessarily takes the
// whole internal/cmd test binary down with it (Go compiles a package as one unit), so
// every other internal/cmd test — including TestStarterConfigLiteralAbsence and
// TestInstallConfigsManifestClosure in this same package — will also report as
// build-failed until Phase 6 lands the function, even though their own logic doesn't
// depend on it. See todos/fable-implement/red_predictions.md.

// t1SeedManifest is a fresh-batch, mutually-coherent clone for the source-clone
// (fallback=false) channel: startup.json's watchdog_agents and models.json's agents map name
// "manager", a built-in role of the agents.json the seed writes itself, so validateSeed's
// fresh-batch agent-ref cross-check passes cleanly. Every member's content is a deliberately
// atypical "-t1-source" marker so a byte-content assertion can prove the resolved-clone path
// was actually read.
var t1SeedManifest = map[string]string{
	"dispatch.json":  `{"repos":["marker-t1-source"],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800}`,
	"factory.json":   `{"type":"factory","version":1,"name":"marker-t1-source","max_worktrees":8}`,
	"messaging.json": `{"groups":{"all":["manager"],"t1-marker":["manager"]}}`,
	"startup.json":   `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","start_dispatch":false,"watchdog_agents":["manager"]}`,
	"models.json":    `{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-t1-marker"}},"agents":{"manager":"default"}}`,
	"litellm.yaml":   "marker: t1-source\n",
}

func writeT1SourceTree(t *testing.T) string {
	t.Helper()
	return newAFSourceWithConfigs(t, t1SeedManifest)
}

func readSeeded(t *testing.T, configDir, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(configDir, name))
	if err != nil {
		t.Fatalf("reading seeded %s: %v", name, err)
	}
	return data
}

// --- T1: seed-from-source ------------------------------------------------------

func TestSeedStarterConfigs_T1_SeedFromSource(t *testing.T) {
	factoryRoot := t.TempDir()
	configDir := config.ConfigDir(factoryRoot)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	src := writeT1SourceTree(t)

	var out bytes.Buffer
	if err := seedStarterConfigs(&out, io.Discard, configDir, src, false); err != nil {
		t.Fatalf("seedStarterConfigs (source-clone channel): %v", err)
	}

	// Pass-through members must be byte-identical to the resolved-source marker,
	// proving the clone was actually read (sanitizeSeed does not touch these names).
	for _, name := range []string{"messaging.json", "startup.json", "litellm.yaml"} {
		got := readSeeded(t, configDir, name)
		if string(got) != t1SeedManifest[name] {
			t.Errorf("%s: seeded content = %q, want the t1-source marker %q (resolved clone not read)",
				name, got, t1SeedManifest[name])
		}
	}

	// dispatch.json: repos must be sanitized to [] even though the source marker set it.
	var dispatch config.DispatchConfig
	if err := json.Unmarshal(readSeeded(t, configDir, "dispatch.json"), &dispatch); err != nil {
		t.Fatalf("seeded dispatch.json does not decode: %v", err)
	}
	if len(dispatch.Repos) != 0 {
		t.Errorf("dispatch.json repos = %v, want empty (sanitizeSeed must clear repos even from a resolved-source seed)", dispatch.Repos)
	}

	// factory.json: name must be rewritten to the factory root's basename, not the
	// t1-source marker name.
	var factory config.FactoryConfig
	if err := json.Unmarshal(readSeeded(t, configDir, "factory.json"), &factory); err != nil {
		t.Fatalf("seeded factory.json does not decode: %v", err)
	}
	if want := filepath.Base(factoryRoot); factory.Name != want {
		t.Errorf("factory.json name = %q, want %q (filepath.Base(factoryRoot))", factory.Name, want)
	}

	// Provenance: stdout must attribute at least one file to the resolved source clone
	// (intake.md: "prints a provenance line per file").
	if !strings.Contains(out.String(), "source clone") {
		t.Errorf("stdout provenance output %q does not mention the resolved source clone", out.String())
	}
}

// --- T3: no-overwrite / byte-identity, BOTH channels ----------------------------

// t3PreExistingDispatch is deliberately different from what EITHER channel would
// produce (a real seed's repos always sanitizes to []; this fixture's repos is
// non-empty and its trigger_label/interval are operator-customized), so only a
// genuine write-if-missing implementation — not a same-content coincidence — can pass
// this test.
const t3PreExistingDispatch = `{"repos":["do-not-touch-me"],"trigger_label":"custom-operator-label","notify_on_complete":"nobody","mappings":[],"interval_seconds":999,"retry_after_seconds":1}`

func assertNoOverwrite(t *testing.T, fallback bool, afSrc string) {
	t.Helper()
	factoryRoot := t.TempDir()
	configDir := config.ConfigDir(factoryRoot)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	dispatchPath := filepath.Join(configDir, "dispatch.json")
	if err := os.WriteFile(dispatchPath, []byte(t3PreExistingDispatch), 0644); err != nil {
		t.Fatal(err)
	}

	if err := seedStarterConfigs(&bytes.Buffer{}, io.Discard, configDir, afSrc, fallback); err != nil {
		t.Fatalf("seedStarterConfigs (fallback=%v): %v", fallback, err)
	}

	got, err := os.ReadFile(dispatchPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != t3PreExistingDispatch {
		t.Errorf("fallback=%v: pre-existing dispatch.json was overwritten — got %q, want untouched %q",
			fallback, got, t3PreExistingDispatch)
	}
}

func TestSeedStarterConfigs_T3_NoOverwrite_SourceCloneChannel(t *testing.T) {
	assertNoOverwrite(t, false, writeT1SourceTree(t))
}

func TestSeedStarterConfigs_T3_NoOverwrite_NoSourceTreeChannel(t *testing.T) {
	assertNoOverwrite(t, true, "/nonexistent/ignored-on-fallback")
}

// --- Provenance-log test ---------------------------------------------------------

// TestSeedStarterConfigs_ProvenanceLogAppendsPerFilePerRun pins AC #7: a run that writes a file
// gets one .config-seed.log line per manifest file, kept-existing outcomes included, and the log is
// append-only (never truncated/rewritten).
func TestSeedStarterConfigs_ProvenanceLogAppendsPerFilePerRun(t *testing.T) {
	factoryRoot := t.TempDir()
	configDir := config.ConfigDir(factoryRoot)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(configDir, ".config-seed.log")

	if err := seedStarterConfigs(&bytes.Buffer{}, io.Discard, configDir, "/nonexistent/ignored-on-fallback", true); err != nil {
		t.Fatalf("first run: %v", err)
	}
	first, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf(".config-seed.log not created by the first run: %v", err)
	}
	firstLines := strings.Split(strings.TrimRight(string(first), "\n"), "\n")
	if len(firstLines) != len(seedConfigNames) {
		t.Errorf("first run: %d log lines, want %d (one per manifest file)", len(firstLines), len(seedConfigNames))
	}

	// The second run must write something, or it has nothing to log.
	if err := os.Remove(filepath.Join(configDir, "messaging.json")); err != nil {
		t.Fatal(err)
	}
	if err := seedStarterConfigs(&bytes.Buffer{}, io.Discard, configDir, "/nonexistent/ignored-on-fallback", true); err != nil {
		t.Fatalf("second run: %v", err)
	}
	second, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(second, first) {
		t.Fatalf("second run did not append: log was rewritten rather than extended")
	}
	secondLines := strings.Split(strings.TrimRight(string(second), "\n"), "\n")
	if len(secondLines) != 2*len(seedConfigNames) {
		t.Errorf("after two writing runs: %d total log lines, want %d (append-only, one per file per run)",
			len(secondLines), 2*len(seedConfigNames))
	}
	appended := secondLines[len(firstLines):]
	for name, outcome := range map[string]string{"messaging.json": "seeded", "agents.json": "kept-existing", "factory.json": "kept-existing"} {
		line, ok := seamLogLineFor(appended, name)
		if f := strings.Fields(line); !ok || len(f) < 3 || f[2] != outcome {
			t.Errorf("second-run log line for %s = %q, want a %s outcome", name, line, outcome)
		}
	}
}

// --- Dispatcher friendly-skip over a seeded config ------------------------------

// TestSeedStarterConfigs_DispatcherFriendlySkipsSeededOutput proves the EXISTING
// friendly-skip machinery (internal/config/dispatch.go, dispatch.go's startDispatch)
// holds over the ACTUAL output of seedStarterConfigs — not a hand-rolled fixture that
// merely resembles it (consumers.md row 49). sanitizeSeed always clears repos to [],
// so a freshly seeded dispatch.json must always be politely skipped, never launched.
func TestSeedStarterConfigs_DispatcherFriendlySkipsSeededOutput(t *testing.T) {
	root := t.TempDir()
	configDir := config.ConfigDir(root)
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := seedStarterConfigs(&bytes.Buffer{}, io.Discard, configDir, writeT1SourceTree(t), false); err != nil {
		t.Fatalf("seedStarterConfigs: %v", err)
	}

	fake, _ := setupHermeticSessions(t)
	if err := startDispatch(&cobra.Command{}, root, fake); err != nil {
		t.Fatalf("startDispatch over a freshly seeded dispatch.json must friendly-skip, got error: %v", err)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("a freshly seeded dispatch.json (repos always sanitized to []) must not launch a dispatcher session; ops=%v", fake.ops)
	}
}
