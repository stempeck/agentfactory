package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
)

// ---- fixtures ----------------------------------------------------------------

func validPluginFormula(name string) string {
	return "formula = \"" + name + "\"\ntype = \"workflow\"\nversion = 1\ndescription = \"" + name + " plugin agent\"\n\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
}

func validPluginFormulaWithSkills(name, skill string) string {
	return "formula = \"" + name + "\"\ntype = \"workflow\"\nversion = 1\nskills = [\"" + skill + "\"]\n\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
}

// storeFormulaVariant is a valid formula whose BYTES differ from validPluginFormula
// (a leading comment) — used to seed a genuine store collision (different content), as
// opposed to a byte-identical copy, which the three-valued check treats as idempotent.
func storeFormulaVariant(name string) string {
	return "# operator's own variant\n" + validPluginFormula(name)
}

// writePluginFixture simulates an inert `git clone` into store/plugins/<name>/.
func writePluginFixture(t *testing.T, root, pluginName string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(config.PluginsDir(root), pluginName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		p := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// runPlugin drives a plugin subcommand's RunE with an isolated cobra.Command (no
// rootCmd global-flag pollution). Tests must t.Chdir into the factory first.
func runPlugin(t *testing.T, sub string, setFlags map[string]string, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := &cobra.Command{}
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.Flags().Bool("json", false, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().Bool("no-build", false, "")
	// R5: without factory-wide a GetBool on it reads false silently and hides the scope refusals.
	cmd.Flags().Bool("factory-wide", false, "")
	for k, v := range setFlags {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatalf("set flag %s=%s: %v", k, v, err)
		}
	}
	var err error
	switch sub {
	case "list":
		err = runPluginList(cmd, args)
	case "install":
		// The registered RunE: runPluginInstall today, the D27 classifying dispatcher after GREEN.
		err = pluginInstallCmd.RunE(cmd, args)
	case "verify":
		err = runPluginVerify(cmd, args)
	case "acquire":
		err = runPluginAcquire(cmd, args)
	case "check":
		err = runPluginCheck(cmd, args)
	case "remove":
		err = runPluginRemove(cmd, args)
	default:
		t.Fatalf("unknown plugin sub %q", sub)
	}
	return buf.String(), err
}

// stubInstallPipeline neutralizes the rebuild pipeline + K15 report tail so plugin
// install stays hermetic (no scripts, no exec, no noexec-/tmp trap) and reaches its
// staging/manifest/verify steps. AF_SOURCE_ROOT satisfies installAgentsPipeline Guard 2.
func stubInstallPipeline(t *testing.T) {
	t.Helper()
	origAG, origQS, origReport := runAgentGenScript, runQuickstartScript, runPluginVerifyReport
	origAFSrcFlag, origCompiled, origNoBuild := agentGenAFSrc, compiledSourceRoot, installNoBuildFlag
	t.Cleanup(func() {
		runAgentGenScript, runQuickstartScript, runPluginVerifyReport = origAG, origQS, origReport
		agentGenAFSrc, compiledSourceRoot, installNoBuildFlag = origAFSrcFlag, origCompiled, origNoBuild
	})
	agentGenAFSrc, compiledSourceRoot, installNoBuildFlag = "", "", false
	runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error { return nil }
	stubQuickstart(func(string, []string) error { return nil })
	runPluginVerifyReport = func(cmd *cobra.Command, projectDir string) {}
	src := newAFSourceDir(t, []string{"agent-gen-all.sh", "quickstart.sh"}, nil)
	t.Setenv("AF_SOURCE_ROOT", src)
}

// stubQuickstart is the one place plugin tests replace runQuickstartScript. It hands fn the
// quickstart args so a test can assert what a verb forwards. Callers restore it.
func stubQuickstart(fn func(projectDir string, extraArgs []string) error) {
	runQuickstartScript = func(cmd *cobra.Command, afSrc, projectDir string, extraArgs []string) error {
		return fn(projectDir, extraArgs)
	}
}

// PR #539 T2/BODY-5: the stub must forward main's extraArgs, or a forwarding assertion passes vacuously.
func TestStubQuickstartForwardsExtraArgs(t *testing.T) {
	orig := runQuickstartScript
	t.Cleanup(func() { runQuickstartScript = orig })
	var got []string
	stubQuickstart(func(_ string, extraArgs []string) error { got = extraArgs; return nil })

	if err := runQuickstartScript(nil, "", "d", []string{"x", "y"}); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{"x", "y"}) {
		t.Errorf("stubQuickstart dropped the quickstart args: got %q, want [x y]", got)
	}
}

func stubPluginVerify(t *testing.T, fn func(cmd *cobra.Command, root string, names []string) error) {
	t.Helper()
	orig := pluginInstallVerify
	t.Cleanup(func() { pluginInstallVerify = orig })
	pluginInstallVerify = fn
}

// ---- K3/K4: acquisition inertness (AC-2) ------------------------------------

func TestPluginAcquisitionInert(t *testing.T) {
	dir := setupFactoryDir(t)
	cfgRoot := filepath.Join(dir, ".agentfactory")
	before := snapshotTree(t, cfgRoot)

	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})

	after := snapshotTree(t, cfgRoot)
	pluginsPrefix := filepath.Join("store", "plugins") + string(filepath.Separator)
	for k, v := range after {
		if strings.HasPrefix(k, pluginsPrefix) {
			continue // the clone itself
		}
		if before[k] != v {
			t.Errorf("acquisition mutated %s (before=%q after=%q) — a clone must change nothing", k, before[k], v)
		}
	}
	for k := range before {
		if _, ok := after[k]; !ok {
			t.Errorf("acquisition removed %s", k)
		}
	}

	if _, err := formula.FindFormulaFile("acme-triage", dir); err == nil {
		t.Error("FindFormulaFile must MISS for an un-installed plugin formula")
	}
	if _, err := resolveSpecialistAgent(dir, "acme-triage"); err == nil || !strings.Contains(err.Error(), "not found in agents.json") {
		t.Errorf(`resolveSpecialistAgent should error "not found in agents.json", got: %v`, err)
	}
}

// ---- K4: list ---------------------------------------------------------------

func TestPluginList(t *testing.T) {
	t.Run("zero_state_teaching_message", func(t *testing.T) {
		dir := setupFactoryDir(t)
		t.Chdir(dir)
		out, err := runPlugin(t, "list", nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(out, "no plugins acquired") {
			t.Errorf("missing zero-state teaching message; got:\n%s", out)
		}
	})

	t.Run("status_classes_json", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writeProjectFormula(t, dir, "shipped.formula.toml", storeFormulaVariant("shipped"))
		writePluginFixture(t, dir, "acme", map[string]string{
			"acme-triage.formula.toml": validPluginFormula("acme-triage"),                  // ok
			"1bad.formula.toml":        validPluginFormula("1bad"),                         // invalid-name (stem)
			"needsskill.formula.toml":  validPluginFormulaWithSkills("needsskill", "nope"), // missing-skills
			"shipped.formula.toml":     validPluginFormula("shipped"),                      // collides-with-store (diff bytes)
			"manager.formula.toml":     validPluginFormula("manager"),                      // collides-with-manual-agent
			"brokentoml.formula.toml":  "this = = not valid toml ][",                       // parse-error
		})
		t.Chdir(dir)
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		if err != nil {
			t.Fatalf("list --json: %v", err)
		}
		var got []pluginListJSON
		if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); e != nil {
			t.Fatalf("json unmarshal: %v\noutput:\n%s", e, out)
		}
		statuses := map[string]string{}
		for _, p := range got {
			for _, f := range p.Formulas {
				statuses[f.File] = f.Status
			}
		}
		want := map[string]string{
			"acme-triage.formula.toml": pluginStatusOK,
			"1bad.formula.toml":        pluginStatusInvalidName,
			"needsskill.formula.toml":  pluginStatusMissingSkills,
			"shipped.formula.toml":     pluginStatusCollideStore,
			"manager.formula.toml":     pluginStatusCollideManual,
			"brokentoml.formula.toml":  pluginStatusParseError,
		}
		for f, w := range want {
			if statuses[f] != w {
				t.Errorf("status[%s] = %q, want %q", f, statuses[f], w)
			}
		}
	})

	t.Run("shipped_builtin_name_collides", func(t *testing.T) {
		// A plugin squatting a BUILT-IN (embedded install_formulas) name must collide
		// even in a fresh factory where the built-in is not yet under store/formulas.
		dir := setupFactoryDir(t)
		writePluginFixture(t, dir, "squat", map[string]string{
			"design-v7.formula.toml": validPluginFormula("design-v7"),
		})
		t.Chdir(dir)
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		if err != nil {
			t.Fatalf("list --json: %v", err)
		}
		var got []pluginListJSON
		if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); e != nil {
			t.Fatalf("json: %v\n%s", e, out)
		}
		found := false
		for _, p := range got {
			for _, f := range p.Formulas {
				if f.File == "design-v7.formula.toml" {
					found = true
					if f.Status != pluginStatusCollideStore {
						t.Errorf("built-in squat status = %q, want %q", f.Status, pluginStatusCollideStore)
					}
				}
			}
		}
		if !found {
			t.Fatalf("design-v7.formula.toml not enumerated:\n%s", out)
		}
		// install must refuse it (naming the built-in).
		_, ierr := runPlugin(t, "install", nil, "squat")
		if ierr == nil || !strings.Contains(ierr.Error(), "built-in shipped formula") {
			t.Errorf("install must refuse a built-in squat, got: %v", ierr)
		}
	})

	t.Run("third_party_label_human", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writePluginFixture(t, dir, "acme", map[string]string{
			"acme-triage.formula.toml": validPluginFormula("acme-triage"),
		})
		t.Chdir(dir)
		out, err := runPlugin(t, "list", nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(out, "review the formulas before installing") {
			t.Errorf("missing third-party-content label; got:\n%s", out)
		}
	})
}

func TestPluginSymlinkRefused(t *testing.T) {
	dir := setupFactoryDir(t)
	// a valid formula the install could otherwise stage...
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})
	// ...plus a symlinked *.formula.toml (T5 exfil/clobber vector).
	target := filepath.Join(t.TempDir(), "outside.toml")
	if err := os.WriteFile(target, []byte(validPluginFormula("evil")), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(config.PluginsDir(dir), "acme", "evil.formula.toml")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported in this environment: %v", err)
	}
	stubInstallPipeline(t)
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })
	t.Chdir(dir)

	// list marks the symlink out-of-contract (non-regular)...
	out, err := runPlugin(t, "list", map[string]string{"json": "true"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out, pluginStatusOutOfContract) || !strings.Contains(out, "evil.formula.toml") {
		t.Errorf("list should flag the symlink as out-of-contract; got:\n%s", out)
	}
	// ...and install REFUSES it (fail-loud, never copies through a symlink).
	_, ierr := runPlugin(t, "install", nil, "acme")
	if ierr == nil || !strings.Contains(ierr.Error(), "not a regular file") {
		t.Fatalf("install must refuse a non-regular formula (T5), got: %v", ierr)
	}
	if _, statErr := os.Stat(filepath.Join(config.FormulasDir(dir), "acme-triage.formula.toml")); statErr == nil {
		t.Error("a T5-refused install must stage nothing (even the valid sibling formula)")
	}
	if _, statErr := os.Stat(config.PluginsConfigPath(dir)); statErr == nil {
		t.Error("a T5-refused install must not write plugins.json")
	}
}

func TestPluginNestedFormulaIgnored(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml":   validPluginFormula("acme-triage"), // top-level
		"subdir/nested.formula.toml": validPluginFormula("nested"),      // out-of-contract
	})
	stubInstallPipeline(t)
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })
	t.Chdir(dir)

	out, err := runPlugin(t, "list", map[string]string{"json": "true"})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var got []pluginListJSON
	if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); e != nil {
		t.Fatalf("json: %v\n%s", e, out)
	}
	for _, p := range got {
		for _, f := range p.Formulas {
			if strings.Contains(f.File, "nested") && f.Status != pluginStatusOutOfContract {
				t.Errorf("nested formula status = %q, want out-of-contract", f.Status)
			}
		}
	}
	// install succeeds and stages ONLY the top-level formula (nested ignored).
	if _, ierr := runPlugin(t, "install", nil, "acme"); ierr != nil {
		t.Fatalf("install with a nested formula must succeed (nested ignored), got: %v", ierr)
	}
	if _, statErr := os.Stat(filepath.Join(config.FormulasDir(dir), "nested.formula.toml")); statErr == nil {
		t.Error("nested formula was staged into store/formulas (must be ignored)")
	}
	if _, statErr := os.Stat(filepath.Join(config.FormulasDir(dir), "acme-triage.formula.toml")); statErr != nil {
		t.Errorf("top-level formula was NOT staged: %v", statErr)
	}
}

// ---- K5/K7: staging + install ------------------------------------------------

func TestPluginInstallStagingAtomicity(t *testing.T) {
	dir := setupFactoryDir(t)
	writeProjectFormula(t, dir, "existing.formula.toml", storeFormulaVariant("existing"))
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
		"existing.formula.toml":    validPluginFormula("existing"), // collides (different bytes than store)
	})
	stubInstallPipeline(t)
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })
	t.Chdir(dir)

	storeBefore := snapshotTree(t, config.FormulasDir(dir))
	agentsBefore, _ := os.ReadFile(config.AgentsConfigPath(dir))

	_, err := runPlugin(t, "install", nil, "acme")
	if err == nil {
		t.Fatal("expected all-or-nothing refusal on a store collision")
	}
	if !strings.Contains(err.Error(), "collides with existing store formula") {
		t.Errorf("wrong refusal: %v", err)
	}

	if after := snapshotTree(t, config.FormulasDir(dir)); !reflect.DeepEqual(storeBefore, after) {
		t.Errorf("store/ mutated on a REFUSED install (writes leaked):\n before=%v\n after=%v", storeBefore, after)
	}
	if agentsAfter, _ := os.ReadFile(config.AgentsConfigPath(dir)); !bytes.Equal(agentsBefore, agentsAfter) {
		t.Error("agents.json mutated on a refused install")
	}
	if _, statErr := os.Stat(config.PluginsConfigPath(dir)); statErr == nil {
		t.Error("plugins.json created despite refusal — zero-writes-on-failure violated")
	}
}

func TestPluginInstallThreeValuedResumable(t *testing.T) {
	dir := setupFactoryDir(t)
	content := validPluginFormula("acme-triage")
	writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": content})
	// Crash-after-stage, before-manifest: the store copy already exists with the SAME
	// bytes as the plugin, but plugins.json has no record. A re-run must proceed (H1).
	writeProjectFormula(t, dir, "acme-triage.formula.toml", content)
	stubInstallPipeline(t)
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })
	t.Chdir(dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err != nil {
		t.Fatalf("resumable re-install must proceed idempotently, got: %v\n%s", err, out)
	}
	m, _ := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
	if _, ok := m.Plugins["acme"]; !ok {
		t.Error("resumable install did not record the manifest")
	}
}

func TestPluginInstallThreeValuedOperatorEdited(t *testing.T) {
	dir := setupFactoryDir(t)
	incoming := validPluginFormula("acme-triage")
	writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": incoming})
	// Store copy differs from the plugin AND from the recorded manifest hash → operator edit.
	writeProjectFormula(t, dir, "acme-triage.formula.toml", incoming+"\n# operator edit\n")
	config.SavePluginsConfig(config.PluginsConfigPath(dir), &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
		"acme": {InstalledAt: "2026-01-01T00:00:00Z", Formulas: map[string]config.PluginFormula{"acme-triage": {SHA256: "0000deadbeef"}}},
	}})
	stubInstallPipeline(t)
	t.Chdir(dir)

	_, err := runPlugin(t, "install", nil, "acme")
	if err == nil {
		t.Fatal("operator-edited store copy must be refused")
	}
	if !strings.Contains(err.Error(), "operator-edited") {
		t.Errorf("wrong refusal for operator-edited store copy: %v", err)
	}
}

// ---- K7: provenance (AC-5) --------------------------------------------------

func TestPluginInstallProvenance(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})
	stubInstallPipeline(t)
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })
	t.Chdir(dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	m, err := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	entry, ok := m.Plugins["acme"]
	if !ok {
		t.Fatal("plugins.json has no record for acme after install")
	}
	if entry.InstalledAt == "" {
		t.Error("InstalledAt not populated (ADR-016 provenance)")
	}
	pf, ok := entry.Formulas["acme-triage"]
	if !ok || pf.SHA256 == "" {
		t.Errorf("per-formula SHA256 not populated: %+v", entry.Formulas)
	}
}

// ---- K9: install-time embed gate (negative path, AC-4) ----------------------

func TestPluginInstallEmbedFail(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})
	stubInstallPipeline(t) // pipeline "succeeds"
	// K9 seam runs the REAL in-process embed check, which fails for the fictional agent
	// (templates.New().HasRole("acme-triage") == false) and emits api.md L94.
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error {
		report, ok, err := verifyPluginsReport(root, names, false)
		if err != nil {
			return err
		}
		fmt.Fprint(cmd.OutOrStdout(), report)
		if !ok {
			return fmt.Errorf("plugin verify failed after install (see report above)")
		}
		return nil
	})
	t.Chdir(dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err == nil {
		t.Fatal("install must return non-nil when the embed check fails")
	}
	if !strings.Contains(out, "NOT embedded in the rebuilt binary") {
		t.Errorf("missing api.md L94 per-agent report; output:\n%s", out)
	}
}

// ---- K8: verify -------------------------------------------------------------

func TestPluginVerifyReportsUnembedded(t *testing.T) {
	// PR #539 BODY-16: registered + hash-clean, so the embed check is the only failing predicate.
	t.Run("embed_is_the_only_failing_predicate", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writeAgentsJSON(t, dir, `{"agents":{"acme-triage":{"type":"autonomous","description":"x","formula":"acme-triage"}}}`)
		body := []byte(validPluginFormula("acme-triage"))
		if err := os.MkdirAll(config.FormulasDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(config.FormulasDir(dir), "acme-triage.formula.toml"), body, 0o644); err != nil {
			t.Fatal(err)
		}
		config.SavePluginsConfig(config.PluginsConfigPath(dir), &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
			"acme": {Formulas: map[string]config.PluginFormula{"acme-triage": {SHA256: hashHex(body)}}},
		}})
		t.Chdir(dir)
		out, err := runPlugin(t, "verify", map[string]string{"json": "true"}, "acme")
		if err == nil {
			t.Fatal("verify must exit non-zero for an un-embedded plugin agent")
		}
		var payload struct {
			OK      bool `json:"ok"`
			Results []struct {
				Agent      string `json:"agent"`
				Registered bool   `json:"registered"`
				Embedded   bool   `json:"embedded"`
				HashClean  *bool  `json:"hash_clean"`
				OK         bool   `json:"ok"`
				Message    string `json:"message"`
			} `json:"results"`
		}
		if jerr := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); jerr != nil {
			t.Fatalf("verify --json output is not JSON (%v):\n%s", jerr, out)
		}
		if len(payload.Results) != 1 || payload.Results[0].Agent != "acme-triage" {
			t.Fatalf("want exactly the acme-triage row; got:\n%s", out)
		}
		r := payload.Results[0]
		if !r.Registered || r.HashClean == nil || !*r.HashClean {
			t.Fatalf("fixture must be registered and hash-clean so only embed can fail; got:\n%s", out)
		}
		if r.Embedded || r.OK || payload.OK {
			t.Errorf("embed check not pinned: an unembedded agent must report embedded=false ok=false; got:\n%s", out)
		}
		if !strings.Contains(r.Message, "NOT embedded") {
			t.Errorf("row message must say NOT embedded; got %q", r.Message)
		}
	})

	// PR #539 T17: a recorded-but-unregistered agent must stay in --all (the union pin).
	t.Run("recorded_unregistered_stays_in_all", func(t *testing.T) {
		dir := setupFactoryDir(t)
		config.SavePluginsConfig(config.PluginsConfigPath(dir), &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
			"acme": {Formulas: map[string]config.PluginFormula{"acme-triage": {SHA256: "x"}}},
		}})
		t.Chdir(dir)
		out, err := runPlugin(t, "verify", map[string]string{"all": "true"})
		if err == nil {
			t.Fatal("verify must exit non-zero for an un-embedded plugin agent")
		}
		if !strings.Contains(out, "acme-triage") || !strings.Contains(out, currentBinaryPath()) {
			t.Errorf("verify output should name the agent and the answering binary; got:\n%s", out)
		}
	})
}

// ---- K14: gated runtime refusal (AC-3) --------------------------------------

func k14Factory(t *testing.T) string {
	t.Helper()
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{"acme-triage":{"type":"autonomous","description":"plugin agent","formula":"acme-triage"}}}`)
	config.SavePluginsConfig(config.PluginsConfigPath(dir), &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
		"acme": {Formulas: map[string]config.PluginFormula{"acme-triage": {SHA256: "x"}}},
	}})
	return dir
}

func TestPluginRuntimeRefusalResolve(t *testing.T) {
	dir := k14Factory(t)
	_, err := resolveSpecialistAgent(dir, "acme-triage")
	if err == nil {
		t.Fatal("K14: resolveSpecialistAgent must refuse a manifest-owned, un-embedded agent")
	}
	if !strings.Contains(err.Error(), `owned by plugin "acme"`) {
		t.Errorf("wrong K14 error: %v", err)
	}
}

func TestPluginRuntimeRefusalLaunch(t *testing.T) {
	dir := k14Factory(t)
	// The launch site's guard IS refusePluginAgentWithoutTemplate — assert it deterministically.
	if err := refusePluginAgentWithoutTemplate(dir, "acme-triage"); err == nil || !strings.Contains(err.Error(), `owned by plugin "acme"`) {
		t.Fatalf("launch-site K14 guard did not refuse: %v", err)
	}
	// End-to-end through launchAgentSession when tmux is available in this env.
	setupHermeticSessions(t)
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := launchAgentSession(cmd, dir, "acme-triage", "", "", "", false)
	if err == nil {
		t.Fatal("launchAgentSession must error (K14, or tmux-unavailable)")
	}
	if strings.Contains(err.Error(), "tmux") {
		t.Logf("tmux unavailable; launch-site K14 verified via the shared guard only")
	} else if !strings.Contains(err.Error(), `owned by plugin "acme"`) {
		t.Errorf("launchAgentSession errored but NOT via K14: %v", err)
	}
}

func TestPluginRuntimeRefusalUp(t *testing.T) {
	dir := k14Factory(t)
	t.Chdir(dir)
	setupHermeticSessions(t)
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := runUp(cmd, []string{"acme-triage"})
	out := buf.String()
	if err == nil {
		t.Fatal("K14: `af up` must refuse a manifest-owned, un-embedded agent")
	}
	if !strings.Contains(out, `owned by plugin "acme"`) {
		t.Errorf("missing K14 per-agent error in up output:\n%s", out)
	}
}

func TestPluginRuntimeRefusalDormantWithoutManifest(t *testing.T) {
	dir := setupFactoryDir(t)
	// Same registered agent, but NO plugins.json: the guard must never fire.
	writeAgentsJSON(t, dir, `{"agents":{"acme-triage":{"type":"autonomous","description":"x","formula":"acme-triage"}}}`)
	entry, err := resolveSpecialistAgent(dir, "acme-triage")
	if err != nil {
		t.Fatalf("dormant path must not refuse without plugins.json, got: %v", err)
	}
	if entry.Formula != "acme-triage" {
		t.Errorf("unexpected entry: %+v", entry)
	}
}

// ---- K15: report-only pipeline tail gating (AC-6) ---------------------------

func TestK15PipelineTailGatedOnManifest(t *testing.T) {
	origAG, origQS, origReport := runAgentGenScript, runQuickstartScript, runPluginVerifyReport
	origAFSrcFlag, origCompiled, origNoBuild := agentGenAFSrc, compiledSourceRoot, installNoBuildFlag
	t.Cleanup(func() {
		runAgentGenScript, runQuickstartScript, runPluginVerifyReport = origAG, origQS, origReport
		agentGenAFSrc, compiledSourceRoot, installNoBuildFlag = origAFSrcFlag, origCompiled, origNoBuild
	})
	agentGenAFSrc, compiledSourceRoot, installNoBuildFlag = "", "", false
	runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error { return nil }
	stubQuickstart(func(string, []string) error { return nil })
	called := false
	runPluginVerifyReport = func(cmd *cobra.Command, projectDir string) { called = true }
	src := newAFSourceDir(t, []string{"agent-gen-all.sh", "quickstart.sh"}, nil)
	t.Setenv("AF_SOURCE_ROOT", src)

	dir := setupFactoryDir(t)
	t.Chdir(dir)
	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// No plugins.json ⇒ tail SKIPPED (byte-identical zero-plugin path, AC-6).
	if err := runInstallAgents(cmd); err != nil {
		t.Fatalf("pipeline (no manifest): %v", err)
	}
	if called {
		t.Error("K15 tail fired without plugins.json (AC-6 zero-plugin path not byte-identical)")
	}

	// With plugins.json ⇒ tail fires (report-only).
	config.SavePluginsConfig(config.PluginsConfigPath(dir), &config.PluginsConfig{Plugins: map[string]config.PluginEntry{}})
	called = false
	if err := runInstallAgents(cmd); err != nil {
		t.Fatalf("pipeline (with manifest): %v", err)
	}
	if !called {
		t.Error("K15 tail did not fire with plugins.json present")
	}
}
