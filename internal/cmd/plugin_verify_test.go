//go:build !integration

package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

const g2ConflictManifest = "<<<<<<< HEAD\n{\"plugins\":{\"acme\":{\"formulas\":{\"acme-triage\":{\"sha256\":\"x\"}}}}}\n=======\n{\"plugins\":{\"beta\":{\"formulas\":{\"beta-scout\":{\"sha256\":\"y\"}}}}}\n>>>>>>> other-branch\n"

const g2ConflictAgents = "<<<<<<< HEAD\n{\"agents\":{\"factoryworker\":{\"type\":\"autonomous\",\"description\":\"x\",\"formula\":\"factoryworker\"}}}\n=======\n{\"agents\":{}}\n>>>>>>> other-branch\n"

type g2VerifyRow struct {
	Plugin     string `json:"plugin"`
	Agent      string `json:"agent"`
	Registered bool   `json:"registered"`
	Embedded   bool   `json:"embedded"`
	HashClean  *bool  `json:"hash_clean"`
	OK         bool   `json:"ok"`
	Message    string `json:"message"`
}

type g2VerifyPayload struct {
	Binary  string        `json:"binary"`
	OK      bool          `json:"ok"`
	State   string        `json:"state"`
	Error   string        `json:"error"`
	Results []g2VerifyRow `json:"results"`
}

func g2WriteManifestBytes(t *testing.T, root, body string) {
	t.Helper()
	p := config.PluginsConfigPath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// g2StoreCleanFormula writes store/formulas/<stem>.formula.toml and returns its sha256,
// so a manifest can record it hash-clean.
func g2StoreCleanFormula(t *testing.T, root, stem string) string {
	t.Helper()
	dir := config.FormulasDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte(validPluginFormula(stem))
	if err := os.WriteFile(filepath.Join(dir, stem+".formula.toml"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	return hashHex(body)
}

func g2SaveManifest(t *testing.T, root string, owners map[string]map[string]string) {
	t.Helper()
	cfg := &config.PluginsConfig{Plugins: map[string]config.PluginEntry{}}
	for plugin, stems := range owners {
		fs := map[string]config.PluginFormula{}
		for stem, sha := range stems {
			fs[stem] = config.PluginFormula{SHA256: sha}
		}
		cfg.Plugins[plugin] = config.PluginEntry{Formulas: fs}
	}
	if err := config.SavePluginsConfig(config.PluginsConfigPath(root), cfg); err != nil {
		t.Fatal(err)
	}
}

func g2EnterFactory(t *testing.T, dir string) {
	t.Helper()
	t.Chdir(dir)
	t.Setenv("AF_ROOT", dir)
}

// g2VerifyJSON runs `af plugin verify --json` and decodes the last JSON object line.
func g2VerifyJSON(t *testing.T, flags map[string]string, args ...string) (g2VerifyPayload, []map[string]any, string, error) {
	t.Helper()
	all := map[string]string{"json": "true"}
	for k, v := range flags {
		all[k] = v
	}
	out, err := runPlugin(t, "verify", all, args...)
	var line string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "{") {
			line = strings.TrimSpace(l)
		}
	}
	var p g2VerifyPayload
	var raw struct {
		Results []map[string]any `json:"results"`
	}
	if line == "" {
		t.Fatalf("verify --json printed no JSON object; err=%v output:\n%s", err, out)
	}
	if jerr := json.Unmarshal([]byte(line), &p); jerr != nil {
		t.Fatalf("verify --json output is not JSON (%v):\n%s", jerr, out)
	}
	_ = json.Unmarshal([]byte(line), &raw)
	return p, raw.Results, out, err
}

func g2Combined(out string, err error) string {
	if err == nil {
		return out
	}
	return out + "\n" + err.Error()
}

func g2RowsByAgent(rows []g2VerifyRow) map[string][]g2VerifyRow {
	m := map[string][]g2VerifyRow{}
	for _, r := range rows {
		m[r.Agent] = append(m[r.Agent], r)
	}
	return m
}

// ---- 2a / 2d / D5: K14 guard on a present-but-unusable manifest ------------------

// PR #539 T2/T7: a corrupt plugins.json is an error state, not "zero plugins" (D4/D5).
func TestPluginRuntimeRefusalCorruptManifestFailsClosed(t *testing.T) {
	rows := []struct {
		name, body, parseErr string
		asDir                bool
	}{
		{name: "conflict_markers", body: g2ConflictManifest, parseErr: "invalid character '<'"},
		{name: "zero_byte", body: "", parseErr: "unexpected end of JSON input"},
		{name: "truncated", body: `{"plugins":{"acme":`, parseErr: "unexpected end of JSON input"},
		{name: "top_level_array", body: `[1]`, parseErr: "cannot unmarshal array"},
		{name: "empty_object_no_key", body: `{}`},
		{name: "json_null", body: `null`},
		{name: "plugins_null", body: `{"plugins":null}`},
		{name: "directory", asDir: true, parseErr: "is a directory"},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			dir := k14Factory(t)
			if row.asDir {
				p := config.PluginsConfigPath(dir)
				if err := os.Remove(p); err != nil {
					t.Fatal(err)
				}
				if err := os.MkdirAll(p, 0o755); err != nil {
					t.Fatal(err)
				}
			} else {
				g2WriteManifestBytes(t, dir, row.body)
			}
			err := refusePluginAgentWithoutTemplate(dir, "acme-triage")
			if err == nil {
				t.Fatal("K14 must refuse a non-embedded agent while plugins.json is present but unusable (fail closed)")
			}
			if !strings.Contains(err.Error(), "plugins.json") {
				t.Errorf("refusal must name plugins.json; got: %v", err)
			}
			if row.parseErr != "" && !strings.Contains(err.Error(), row.parseErr) {
				t.Errorf("refusal must carry the load error %q; got: %v", row.parseErr, err)
			}
			if strings.Contains(err.Error(), "af plugin verify") {
				t.Errorf("no owner is knowable from a corrupt manifest, so no `af plugin verify <x>` hint (D5); got: %v", err)
			}
		})
	}

	t.Run("non_plugin_unembedded_agent", func(t *testing.T) {
		dir := k14Factory(t)
		writeAgentsJSON(t, dir, `{"agents":{"foo":{"type":"autonomous","description":"x","formula":"foo"}}}`)
		g2WriteManifestBytes(t, dir, g2ConflictManifest)
		err := refusePluginAgentWithoutTemplate(dir, "foo")
		if err == nil || !strings.Contains(err.Error(), "plugins.json") {
			t.Fatalf("ownership is unknowable while plugins.json is corrupt: a non-embedded agent is treated as owned and refused (D5 [B]); got: %v", err)
		}
	})
}

// PR #539 T2/T7 D5 [B]: an embedded agent can never be substituted, so a corrupt
// manifest must not block it (manager/supervisor keep launching).
func TestPluginRuntimeRefusalCorruptManifestSparesEmbeddedAgent(t *testing.T) {
	dir := k14Factory(t)
	g2WriteManifestBytes(t, dir, g2ConflictManifest)
	for _, agent := range []string{"supervisor", "manager"} {
		if err := refusePluginAgentWithoutTemplate(dir, agent); err != nil {
			t.Errorf("embedded agent %q must not be refused by a corrupt manifest: %v", agent, err)
		}
	}
}

// PR #539 T2/T7 AC-6: absent and {"plugins":{}} stay dormant.
func TestPluginRuntimeRefusalDormantManifestShapes(t *testing.T) {
	t.Run("absent", func(t *testing.T) {
		dir := k14Factory(t)
		if err := os.Remove(config.PluginsConfigPath(dir)); err != nil {
			t.Fatal(err)
		}
		if err := refusePluginAgentWithoutTemplate(dir, "acme-triage"); err != nil {
			t.Errorf("absent plugins.json must stay dormant: %v", err)
		}
	})
	t.Run("empty_plugins_object", func(t *testing.T) {
		dir := k14Factory(t)
		g2WriteManifestBytes(t, dir, `{"plugins":{}}`)
		if err := refusePluginAgentWithoutTemplate(dir, "acme-triage"); err != nil {
			t.Errorf(`{"plugins":{}} is zero plugins and must stay dormant: %v`, err)
		}
	})
}

// ---- 2b: every K14 consumer inherits the fail-closed guard -----------------------

func TestPluginRuntimeRefusalCorruptManifest_AllThreeSites(t *testing.T) {
	t.Run("resolveSpecialistAgent", func(t *testing.T) {
		dir := k14Factory(t)
		g2WriteManifestBytes(t, dir, g2ConflictManifest)
		entry, err := resolveSpecialistAgent(dir, "acme-triage")
		if err == nil || !strings.Contains(err.Error(), "plugins.json") {
			t.Fatalf("resolveSpecialistAgent must refuse naming plugins.json; got entry=%+v err=%v", entry, err)
		}
	})
	t.Run("af_up", func(t *testing.T) {
		dir := k14Factory(t)
		g2WriteManifestBytes(t, dir, g2ConflictManifest)
		g2EnterFactory(t, dir)
		setupHermeticSessions(t)
		cmd := &cobra.Command{}
		var buf bytes.Buffer
		cmd.SetOut(&buf)
		cmd.SetErr(&buf)
		err := runUp(cmd, []string{"acme-triage"})
		if err == nil {
			t.Fatal("`af up` must fail while plugins.json is corrupt")
		}
		if !strings.Contains(buf.String(), "plugins.json") {
			t.Errorf("up output must name plugins.json for the refused agent; got:\n%s", buf.String())
		}
	})
}

// ---- 2c: verify fails closed on a corrupt manifest (D6) --------------------------

// PR #539 T7: verify must not print clean on the file the guard cannot read.
func TestPluginVerifyCorruptManifestFailsClosed(t *testing.T) {
	modes := []struct {
		name  string
		flags map[string]string
		args  []string
	}{
		{name: "bare", flags: nil},
		{name: "all", flags: map[string]string{"all": "true"}},
		{name: "named", flags: nil, args: []string{"acme"}},
	}
	for _, m := range modes {
		t.Run(m.name, func(t *testing.T) {
			dir := setupFactoryDir(t)
			g2WriteManifestBytes(t, dir, g2ConflictManifest)
			g2EnterFactory(t, dir)
			out, err := runPlugin(t, "verify", m.flags, m.args...)
			if err == nil {
				t.Fatalf("verify must exit non-zero on a corrupt plugins.json; output:\n%s", out)
			}
			text := g2Combined(out, err)
			if !strings.Contains(text, "plugins.json") || !strings.Contains(text, "invalid character '<'") {
				t.Errorf("verify must surface plugins.json and its parse error; got:\n%s", text)
			}
		})
	}
	t.Run("all_json", func(t *testing.T) {
		dir := setupFactoryDir(t)
		g2WriteManifestBytes(t, dir, g2ConflictManifest)
		g2EnterFactory(t, dir)
		p, _, out, err := g2VerifyJSON(t, map[string]string{"all": "true"})
		if err == nil {
			t.Errorf("verify --json must exit non-zero on a load error (K9 reads the exit code)")
		}
		if p.State != "error" {
			t.Errorf(`verify --json load error must be {"state":"error",...} (D16); got:\n%s`, out)
		}
		if !strings.Contains(p.Error, "plugins.json") {
			t.Errorf("JSON error must name plugins.json; got %q", p.Error)
		}
		if p.OK {
			t.Errorf("ok must not be true on a load error; got:\n%s", out)
		}
	})
	t.Run("all_empty_object_no_key", func(t *testing.T) {
		dir := setupFactoryDir(t)
		g2WriteManifestBytes(t, dir, `{}`)
		g2EnterFactory(t, dir)
		out, err := runPlugin(t, "verify", map[string]string{"all": "true"})
		if err == nil || !strings.Contains(g2Combined(out, err), "plugins.json") {
			t.Fatalf("a present manifest without an object-valued plugins key is corrupt (D4); err=%v output:\n%s", err, out)
		}
	})
}

// ---- 2f: install and list already fail closed (protective) -----------------------

func TestPluginInstallAndListRefuseCorruptManifest(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})
	g2WriteManifestBytes(t, dir, g2ConflictManifest)
	stubInstallPipeline(t)
	stubPluginVerify(t, func(*cobra.Command, string, []string) error { return nil })
	g2EnterFactory(t, dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err == nil || !strings.Contains(err.Error(), "invalid character '<'") {
		t.Fatalf("install must refuse on a corrupt plugins.json; err=%v output:\n%s", err, out)
	}
	if _, serr := os.Stat(filepath.Join(config.FormulasDir(dir), "acme-triage.formula.toml")); serr == nil {
		t.Error("install staged a store copy despite the corrupt manifest")
	}
	b, _ := os.ReadFile(config.PluginsConfigPath(dir))
	if string(b) != g2ConflictManifest {
		t.Errorf("install rewrote the corrupt plugins.json; now:\n%s", b)
	}

	lout, lerr := runPlugin(t, "list", nil)
	if lerr == nil || !strings.Contains(g2Combined(lout, lerr), "invalid character '<'") {
		t.Errorf("human list must surface the corrupt manifest; err=%v output:\n%s", lerr, lout)
	}
}

// ---- 8a / 8b / 8c: remediation strings (T12 items 1 and 2) -----------------------

// PR #539 T12.1: the unembedded remediation names `af plugin install <plugin>`.
func TestPluginVerifyUnembeddedRemediationNamesPluginInstall(t *testing.T) {
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{"acme-triage":{"type":"autonomous","description":"x","formula":"acme-triage"}}}`)
	g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"acme-triage": g2StoreCleanFormula(t, dir, "acme-triage")}})
	g2EnterFactory(t, dir)

	p, _, out, _ := g2VerifyJSON(t, nil, "acme")
	rows := g2RowsByAgent(p.Results)["acme-triage"]
	if len(rows) != 1 {
		t.Fatalf("want one acme-triage row; got:\n%s", out)
	}
	msg := rows[0].Message
	if !strings.Contains(msg, "af plugin install acme") {
		t.Errorf("remediation must name `af plugin install acme`; got %q", msg)
	}
	if strings.Contains(msg, "af install --agents") {
		t.Errorf("remediation must not name `af install --agents` (it bypasses K9); got %q", msg)
	}
	if !strings.Contains(msg, "NOT embedded in the rebuilt binary") {
		t.Errorf("the unembedded phrase must survive the rewrite; got %q", msg)
	}
}

// PR #539 T12.2: verify takes a PLUGIN name, so the K14 message must pass the plugin.
func TestPluginRuntimeRefusalRemediationVerifyTakesPluginName(t *testing.T) {
	dir := k14Factory(t)
	err := refusePluginAgentWithoutTemplate(dir, "acme-triage")
	if err == nil {
		t.Fatal("K14 must refuse")
	}
	if !strings.Contains(err.Error(), "`af plugin verify acme`") {
		t.Errorf("K14 remediation must be `af plugin verify acme`; got: %v", err)
	}
	if strings.Contains(err.Error(), "`af plugin verify acme-triage`") {
		t.Errorf("K14 remediation passes the agent name to verify, which takes plugin names; got: %v", err)
	}
}

var (
	g2VerifyArgRE  = regexp.MustCompile("af plugin verify ([A-Za-z0-9._-]+)")
	g2InstallArgRE = regexp.MustCompile("af plugin install ([A-Za-z0-9._-]+)")
)

// PR #539 T12: every remediation verb an operator is told to run must accept its argument.
func TestPluginRemediationVerbsAreRunnable(t *testing.T) {
	dir := k14Factory(t)
	g2EnterFactory(t, dir)
	manifest, lerr := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
	if lerr != nil {
		t.Fatal(lerr)
	}

	k14 := refusePluginAgentWithoutTemplate(dir, "acme-triage")
	if k14 == nil {
		t.Fatal("K14 must refuse")
	}
	m := g2VerifyArgRE.FindStringSubmatch(k14.Error())
	if m == nil {
		t.Fatalf("K14 message names no `af plugin verify <x>`: %v", k14)
	}
	p, _, out, _ := g2VerifyJSON(t, nil, m[1])
	for _, r := range p.Results {
		if strings.Contains(r.Message, "not recorded in plugins.json") {
			t.Errorf("K14 tells the operator to run `af plugin verify %s`, which answers %q", m[1], r.Message)
		}
	}
	if len(p.Results) == 0 {
		t.Errorf("`af plugin verify %s` reported nothing:\n%s", m[1], out)
	}

	p, _, out, _ = g2VerifyJSON(t, nil, "acme")
	for _, r := range p.Results {
		if r.Embedded {
			continue
		}
		im := g2InstallArgRE.FindStringSubmatch(r.Message)
		if im == nil {
			t.Errorf("unembedded verify row names no `af plugin install <x>`: %q", r.Message)
			continue
		}
		if _, ok := manifest.Plugins[im[1]]; !ok {
			t.Errorf("verify remediation names `af plugin install %s`, but %q is not an installed plugin", im[1], im[1])
		}
	}
	if len(p.Results) == 0 {
		t.Errorf("verify acme reported nothing:\n%s", out)
	}
}

// ---- 13a / 13b / 13c / 13g: verify --all is class-wide (T17, D15) ----------------

func g2ClassWideFactory(t *testing.T) string {
	t.Helper()
	dir := setupFactoryDir(t)
	writeAgentsJSON(t, dir, `{"agents":{`+
		`"manager":{"type":"interactive","description":"x"},`+
		`"handmade":{"type":"autonomous","description":"manual agent"},`+
		`"foo":{"type":"autonomous","description":"x","formula":"foo"},`+
		`"supervisor":{"type":"autonomous","description":"x","formula":"supervisor"},`+
		`"acme-triage":{"type":"autonomous","description":"x","formula":"acme-triage"}}}`)
	g2SaveManifest(t, dir, map[string]map[string]string{
		"acme": {"acme-triage": g2StoreCleanFormula(t, dir, "acme-triage")},
		"gp":   {"ghost": "x"},
	})
	return dir
}

// PR #539 T17: --all = recorded stems ∪ agents.json formula agents, deduped by stem.
func TestPluginVerifyAllCoversEveryFormulaAgent(t *testing.T) {
	dir := g2ClassWideFactory(t)
	g2EnterFactory(t, dir)
	p, raw, out, err := g2VerifyJSON(t, map[string]string{"all": "true"})
	if err == nil {
		t.Error("--all must fail: foo is unembedded")
	}
	by := g2RowsByAgent(p.Results)

	for _, absent := range []string{"manager", "handmade"} {
		if len(by[absent]) != 0 {
			t.Errorf("agent %q has no formula field and must not be verified; got %+v", absent, by[absent])
		}
	}
	for _, stem := range []string{"foo", "supervisor", "acme-triage", "ghost"} {
		if len(by[stem]) != 1 {
			t.Errorf("--all must report exactly one row for %q; got %d rows. output:\n%s", stem, len(by[stem]), out)
		}
	}
	if r := by["foo"]; len(r) == 1 && (r[0].Plugin != "" || r[0].Embedded || r[0].OK || !r[0].Registered) {
		t.Errorf("foo: want plugin=\"\" registered embedded=false ok=false; got %+v", r[0])
	}
	if r := by["supervisor"]; len(r) == 1 && (r[0].Plugin != "" || !r[0].Embedded || !r[0].OK) {
		t.Errorf("supervisor: registered+embedded non-plugin agent must be ok without a hash; got %+v", r[0])
	}
	if r := by["acme-triage"]; len(r) == 1 && r[0].Plugin != "acme" {
		t.Errorf("a recorded stem keeps its plugin row (plugin row wins the dedupe); got %+v", r[0])
	}
	if r := by["ghost"]; len(r) == 1 && (r[0].Plugin != "gp" || r[0].Registered || r[0].OK) {
		t.Errorf("recorded-but-unregistered ghost must stay in --all as a failing row; got %+v", r[0])
	}
	for _, row := range raw {
		agent, _ := row["agent"].(string)
		_, has := row["hash_clean"]
		switch agent {
		case "foo", "supervisor":
			if has {
				t.Errorf("non-plugin row %q must omit hash_clean (D15 (i)[B]); got %v", agent, row)
			}
		case "acme-triage", "ghost":
			if !has {
				t.Errorf("plugin row %q must keep hash_clean; got %v", agent, row)
			}
		}
	}
	if p.OK {
		t.Errorf("overall ok must be false; got:\n%s", out)
	}
}

// PR #539 T17: bare verify stays = recorded plugins, a distinct mode from --all.
func TestPluginVerifyBareIsRecordedPluginsOnly(t *testing.T) {
	dir := g2ClassWideFactory(t)
	g2EnterFactory(t, dir)
	bare, _, bareOut, _ := g2VerifyJSON(t, nil)
	var got []string
	for _, r := range bare.Results {
		got = append(got, r.Agent)
	}
	if strings.Join(got, ",") != "acme-triage,ghost" {
		t.Errorf("bare verify must cover exactly the recorded plugin stems [acme-triage ghost]; got %v", got)
	}
	all, _, allOut, _ := g2VerifyJSON(t, map[string]string{"all": "true"})
	if len(all.Results) == len(bare.Results) {
		t.Errorf("bare and --all must differ (--all adds agents.json formula agents); bare:\n%s\nall:\n%s", bareOut, allOut)
	}
}

func g2EmbeddedPluginFactory(t *testing.T, withFoo bool) string {
	t.Helper()
	dir := setupFactoryDir(t)
	agents := `"factoryworker":{"type":"autonomous","description":"x","formula":"factoryworker"},` +
		`"supervisor":{"type":"autonomous","description":"x","formula":"supervisor"}`
	if withFoo {
		agents += `,"foo":{"type":"autonomous","description":"x","formula":"foo"}`
	}
	writeAgentsJSON(t, dir, `{"agents":{`+agents+`}}`)
	g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"factoryworker": g2StoreCleanFormula(t, dir, "factoryworker")}})
	return dir
}

// PR #539 T17 D15 (ii)/(iii): an unembedded non-plugin formula agent fails --all and
// is told the ordinary rebuild.
func TestPluginVerifyAllFailsOnUnembeddedNonPluginAgent(t *testing.T) {
	dir := g2EmbeddedPluginFactory(t, true)
	g2EnterFactory(t, dir)
	out, err := runPlugin(t, "verify", map[string]string{"all": "true"})
	if err == nil {
		t.Errorf("--all must exit non-zero when a non-plugin formula agent is unembedded; output:\n%s", out)
	}
	if !strings.Contains(out, "foo") {
		t.Errorf("--all output must name foo; got:\n%s", out)
	}
	if !strings.Contains(out, "af install --agents") {
		t.Errorf("a non-plugin unembedded row's remediation names `af install --agents`; got:\n%s", out)
	}
}

// PR #539 T17 control: every formula agent embedded ⇒ --all stays clean.
func TestPluginVerifyAllCleanWhenEveryFormulaAgentEmbedded(t *testing.T) {
	dir := g2EmbeddedPluginFactory(t, false)
	g2EnterFactory(t, dir)
	out, err := runPlugin(t, "verify", map[string]string{"all": "true"})
	if err != nil {
		t.Errorf("--all must pass when every formula agent is registered and embedded: %v\n%s", err, out)
	}
}

// PR #539 T17 D6: under --all an unloadable agents.json is an error, not a shrunken report.
func TestPluginVerifyAllCorruptAgentsJSONNotClean(t *testing.T) {
	t.Run("human", func(t *testing.T) {
		dir := g2EmbeddedPluginFactory(t, false)
		writeAgentsJSON(t, dir, g2ConflictAgents)
		g2EnterFactory(t, dir)
		out, err := runPlugin(t, "verify", map[string]string{"all": "true"})
		if err == nil {
			t.Fatalf("--all must fail on a corrupt agents.json; output:\n%s", out)
		}
		text := g2Combined(out, err)
		if !strings.Contains(text, "agents.json") || !strings.Contains(text, "invalid character '<'") {
			t.Errorf("--all must surface agents.json and its parse error; got:\n%s", text)
		}
	})
	t.Run("json", func(t *testing.T) {
		dir := g2EmbeddedPluginFactory(t, false)
		writeAgentsJSON(t, dir, g2ConflictAgents)
		g2EnterFactory(t, dir)
		p, _, out, _ := g2VerifyJSON(t, map[string]string{"all": "true"})
		if p.State != "error" || !strings.Contains(p.Error, "agents.json") {
			t.Errorf(`--all --json on a corrupt agents.json must be {"state":"error"} naming agents.json; got:\n%s`, out)
		}
	})
}

// ---- 21b: verify --json exit contract (BODY-9, D16) ------------------------------

func TestPluginVerifyJSONExitContract(t *testing.T) {
	t.Run("fail", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writeAgentsJSON(t, dir, `{"agents":{"acme-triage":{"type":"autonomous","description":"x","formula":"acme-triage"}}}`)
		g2SaveManifest(t, dir, map[string]map[string]string{"acme": {"acme-triage": g2StoreCleanFormula(t, dir, "acme-triage")}})
		g2EnterFactory(t, dir)
		p, _, out, err := g2VerifyJSON(t, nil)
		if err == nil {
			t.Error("verify --json keeps a non-zero exit on FAIL (K9 consumes it)")
		}
		if p.OK || p.State != "fail" {
			t.Errorf(`want ok=false state="fail"; got:\n%s`, out)
		}
	})
	t.Run("ok", func(t *testing.T) {
		dir := g2EmbeddedPluginFactory(t, false)
		g2EnterFactory(t, dir)
		p, _, out, err := g2VerifyJSON(t, nil)
		if err != nil {
			t.Errorf("verify --json on a clean factory must exit 0: %v", err)
		}
		if !p.OK || p.State != "ok" {
			t.Errorf(`want ok=true state="ok"; got:\n%s`, out)
		}
		if p.Results == nil {
			t.Errorf("results must be an array; got:\n%s", out)
		}
	})
}

// ---- 19a + control: stale-PATH diagnosis in K9 (BODY-7, D21) ---------------------

func g2FakeAF(t *testing.T, script string) string {
	t.Helper()
	bin := t.TempDir()
	if !tempDirAllowsExec(t, bin) {
		t.Skip("temp dir is noexec; run with TMPDIR=$HOME/.cache/af-test")
	}
	p := filepath.Join(bin, "af")
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return p
}

func g2PinAFSource(t *testing.T) string {
	t.Helper()
	origFlag, origCompiled := agentGenAFSrc, compiledSourceRoot
	t.Cleanup(func() { agentGenAFSrc, compiledSourceRoot = origFlag, origCompiled })
	agentGenAFSrc, compiledSourceRoot = "", ""
	src := newAFSourceDir(t, []string{"agent-gen-all.sh", "quickstart.sh"}, nil)
	t.Setenv("AF_SOURCE_ROOT", src)
	real, err := filepath.EvalSymlinks(src)
	if err != nil {
		t.Fatal(err)
	}
	return real
}

// PR #539 BODY-7: a rebuilt af that lacks the plugin verb is diagnosed, not just "failed".
func TestPluginInstallVerifyDiagnosesStalePathBinary(t *testing.T) {
	dir := setupFactoryDir(t)
	src := g2PinAFSource(t)
	afPath := g2FakeAF(t, "#!/bin/sh\necho 'Error: unknown command \"plugin\" for \"af\"'\necho \"Run 'af --help' for usage.\"\nexit 1\n")

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := pluginInstallVerify(cmd, dir, []string{"acme"})
	if err == nil {
		t.Fatal("a stale af on PATH must still be an error")
	}
	text := g2Combined(buf.String(), err)
	for _, want := range []string{afPath, src, "PATH", "af plugin install acme"} {
		if !strings.Contains(text, want) {
			t.Errorf("stale-PATH diagnosis must mention %q; got:\n%s", want, text)
		}
	}
}

// PR #539 BODY-7 control (D21): a genuine FAIL report keeps its current message.
func TestPluginInstallVerifyGenuineFailKeepsMessage(t *testing.T) {
	dir := setupFactoryDir(t)
	src := g2PinAFSource(t)
	report := `{"binary":"/x/af","ok":false,"results":[{"plugin":"acme","agent":"acme-triage","registered":true,"embedded":false,"hash_clean":true,"ok":false,"message":"NOT embedded"}]}`
	afPath := g2FakeAF(t, "#!/bin/sh\necho '"+report+"'\nexit 1\n")

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := pluginInstallVerify(cmd, dir, []string{"acme"})
	want := fmt.Sprintf("plugin verify failed after install against %s (see report above)", afPath)
	if err == nil || err.Error() != want {
		t.Errorf("genuine FAIL message changed; want %q, got %v", want, err)
	}
	if strings.Contains(g2Combined(buf.String(), err), src) {
		t.Errorf("a genuine FAIL must not be diagnosed as a source-tree/PATH mismatch; got:\n%s", buf.String())
	}
}
