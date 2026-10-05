package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// ---- g3 git fixtures ---------------------------------------------------------

const (
	g3PluginURL       = "https://github.com/acme/plugin.git"
	g3FactoryURL      = "https://github.com/operator/factory.git"
	g3Secret          = "ghp_FAKESECRET123456"
	g3CredentialedURL = "https://oauth2:" + g3Secret + "@github.com/acme/plugin.git"
)

// g3IsolateGitEnv keeps host git config and an ambient GIT_DIR out of the production
// gitOut calls, and returns the HOME the fixture git commands share.
func g3IsolateGitEnv(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	t.Setenv("GIT_CONFIG_GLOBAL", os.DevNull)
	for _, k := range []string{"GIT_DIR", "GIT_WORK_TREE", "GIT_COMMON_DIR", "GIT_INDEX_FILE",
		"GIT_CEILING_DIRECTORIES", "GIT_CONFIG_COUNT", "GIT_CONFIG_KEY_0", "GIT_CONFIG_VALUE_0"} {
		t.Setenv(k, "")
		os.Unsetenv(k)
	}
	return home
}

func g3GitEnv(home string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, "HOME="+home, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_AUTHOR_NAME=g3", "GIT_AUTHOR_EMAIL=g3@example.invalid",
		"GIT_COMMITTER_NAME=g3", "GIT_COMMITTER_EMAIL=g3@example.invalid")
}

func g3Git(t *testing.T, home, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", args...)
	c.Dir = dir
	c.Env = g3GitEnv(home)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

// g3InitRepo makes dir its own repo with one commit and (optionally) an origin, and
// returns its HEAD.
func g3InitRepo(t *testing.T, home, dir, origin string) string {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	g3Git(t, home, dir, "init", "-q")
	if origin != "" {
		g3Git(t, home, dir, "remote", "add", "origin", origin)
	}
	if err := os.WriteFile(filepath.Join(dir, ".g3seed"), []byte(dir), 0o644); err != nil {
		t.Fatal(err)
	}
	g3Git(t, home, dir, "add", "-A")
	g3Git(t, home, dir, "commit", "-q", "-m", "seed")
	return g3Git(t, home, dir, "rev-parse", "HEAD")
}

func g3OKVerify(t *testing.T) {
	t.Helper()
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })
}

func g3HasControlBytes(s string) bool {
	for i := 0; i < len(s); i++ {
		if (s[i] < 0x20 && s[i] != '\n') || s[i] == 0x7f {
			return true
		}
	}
	return false
}

// ---- 4a-4d: provenance only from the plugin dir's own repo (T4, T11; D8) ------

func TestGitProvenanceIgnoresEnclosingRepo(t *testing.T) {
	cases := []struct {
		name  string
		setup func(t *testing.T, home, enc, plug string)
	}{
		{"non_repo_dir", func(t *testing.T, home, enc, plug string) {}},
		{"empty_dot_git_dir", func(t *testing.T, home, enc, plug string) {
			if err := os.MkdirAll(filepath.Join(plug, ".git"), 0o755); err != nil {
				t.Fatal(err)
			}
		}},
		{"git_dir_env_exported", func(t *testing.T, home, enc, plug string) {
			t.Setenv("GIT_DIR", filepath.Join(enc, ".git"))
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			home := g3IsolateGitEnv(t)
			enc := filepath.Join(t.TempDir(), "factory")
			encHead := g3InitRepo(t, home, enc, g3FactoryURL)
			plug := filepath.Join(enc, ".agentfactory", "store", "plugins", "acme")
			if err := os.MkdirAll(plug, 0o755); err != nil {
				t.Fatal(err)
			}
			tc.setup(t, home, enc, plug)

			source, commit := runGitProvenance(plug)
			if source != "" || commit != "" {
				t.Errorf("provenance of a dir that is not its own repo = (source %q, commit %q); want both empty (enclosing origin %q, HEAD %s must not leak in)",
					source, commit, g3FactoryURL, encHead)
			}
		})
	}
}

func TestGitProvenanceOwnRepo(t *testing.T) {
	t.Run("own_repo_with_remote_inside_enclosing_repo", func(t *testing.T) {
		home := g3IsolateGitEnv(t)
		enc := filepath.Join(t.TempDir(), "factory")
		encHead := g3InitRepo(t, home, enc, g3FactoryURL)
		plug := filepath.Join(enc, ".agentfactory", "store", "plugins", "acme")
		plugHead := g3InitRepo(t, home, plug, g3PluginURL)

		source, commit := runGitProvenance(plug)
		if source != g3PluginURL {
			t.Errorf("source = %q, want %q", source, g3PluginURL)
		}
		if commit != plugHead || commit == encHead {
			t.Errorf("commit = %q, want the plugin's own HEAD %q (enclosing HEAD %q)", commit, plugHead, encHead)
		}
	})

	t.Run("own_repo_no_remote", func(t *testing.T) {
		home := g3IsolateGitEnv(t)
		enc := filepath.Join(t.TempDir(), "factory")
		g3InitRepo(t, home, enc, g3FactoryURL)
		plug := filepath.Join(enc, ".agentfactory", "store", "plugins", "acme")
		plugHead := g3InitRepo(t, home, plug, "")

		source, commit := runGitProvenance(plug)
		if source != "" {
			t.Errorf("origin-less own repo: source = %q, want empty", source)
		}
		if commit != plugHead {
			t.Errorf("origin-less own repo: commit = %q, want its own HEAD %q", commit, plugHead)
		}
	})

	t.Run("worktree_dot_git_file", func(t *testing.T) {
		home := g3IsolateGitEnv(t)
		base := t.TempDir()
		mainRepo := filepath.Join(base, "plugin-main")
		g3InitRepo(t, home, mainRepo, g3PluginURL)
		plug := filepath.Join(base, "plugins", "acme")
		if err := os.MkdirAll(filepath.Dir(plug), 0o755); err != nil {
			t.Fatal(err)
		}
		g3Git(t, home, mainRepo, "worktree", "add", "-q", "--detach", plug)
		plugHead := g3Git(t, home, plug, "rev-parse", "HEAD")

		source, commit := runGitProvenance(plug)
		if source != g3PluginURL || commit != plugHead {
			t.Errorf("worktree plugin (.git is a file): (source %q, commit %q), want (%q, %q)", source, commit, g3PluginURL, plugHead)
		}
	})
}

func TestGitProvenanceOwnRepoViaSymlinkedPath(t *testing.T) {
	home := g3IsolateGitEnv(t)
	realParent := t.TempDir()
	plugHead := g3InitRepo(t, home, filepath.Join(realParent, "acme"), g3PluginURL)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(realParent, alias); err != nil {
		t.Skipf("symlink unsupported: %v", err)
	}

	source, commit := runGitProvenance(filepath.Join(alias, "acme"))
	if source != g3PluginURL || commit != plugHead {
		t.Errorf("own repo reached through a symlinked parent: (source %q, commit %q), want (%q, %q)", source, commit, g3PluginURL, plugHead)
	}
}

func TestPluginInstallProvenanceNonRepoPluginRecordsEmpty(t *testing.T) {
	home := g3IsolateGitEnv(t)
	dir := setupFactoryDir(t)
	g3InitRepo(t, home, dir, g3FactoryURL)
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})
	stubInstallPipeline(t)
	g3OKVerify(t)
	t.Chdir(dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	m, err := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	e := m.Plugins["acme"]
	if e.Source != "" || e.Commit != "" {
		t.Errorf("non-repo plugin recorded (source %q, commit %q); want both empty, never the factory's own origin/HEAD", e.Source, e.Commit)
	}
	if strings.Contains(out, g3FactoryURL) {
		t.Errorf("install narration shows the factory's origin as the plugin's source:\n%s", out)
	}

	list, err := runPlugin(t, "list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(list, "(source:") {
		t.Errorf("list shows provenance for a plugin that is not its own repo:\n%s", list)
	}
}

// ---- 5a, 5c: credentials never recorded or printed (T10; D9) -------------------

// D9(iv): redaction sits right after the seam in enumeratePlugin, so it is pinned there.
func TestGitProvenanceStripsCredentials(t *testing.T) {
	home := g3IsolateGitEnv(t)
	dir := setupFactoryDir(t)
	plug := writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})
	g3InitRepo(t, home, plug, g3CredentialedURL)

	u, err := buildCollisionUniverse(dir)
	if err != nil {
		t.Fatalf("buildCollisionUniverse: %v", err)
	}
	pi, err := enumeratePlugin(dir, "acme", u)
	if err != nil {
		t.Fatalf("enumeratePlugin: %v", err)
	}
	if strings.Contains(pi.Source, g3Secret) {
		t.Errorf("enumerated Source carries the credential: %q", pi.Source)
	}
	if pi.Source != g3PluginURL {
		t.Errorf("enumerated Source = %q, want the userinfo-free %q", pi.Source, g3PluginURL)
	}
}

func TestPluginInstallAndListNeverPrintCredentials(t *testing.T) {
	home := g3IsolateGitEnv(t)
	dir := setupFactoryDir(t)
	plug := writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
	})
	g3InitRepo(t, home, plug, g3CredentialedURL)
	stubInstallPipeline(t)
	g3OKVerify(t)
	t.Chdir(dir)

	out, err := runPlugin(t, "install", nil, "acme")
	if err != nil {
		t.Fatalf("install: %v\n%s", err, out)
	}
	if strings.Contains(out, g3Secret) {
		t.Errorf("install narration prints the credential:\n%s", out)
	}
	raw, err := os.ReadFile(config.PluginsConfigPath(dir))
	if err != nil {
		t.Fatalf("read plugins.json: %v", err)
	}
	if bytes.Contains(raw, []byte(g3Secret)) {
		t.Errorf("plugins.json (git-tracked) records the credential:\n%s", raw)
	}
	m, err := config.LoadPluginsConfig(config.PluginsConfigPath(dir))
	if err != nil {
		t.Fatalf("load manifest: %v", err)
	}
	if got := m.Plugins["acme"].Source; got != g3PluginURL {
		t.Errorf("recorded Source = %q, want %q", got, g3PluginURL)
	}

	human, err := runPlugin(t, "list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if strings.Contains(human, g3Secret) {
		t.Errorf("list prints the credential:\n%s", human)
	}
	js, err := runPlugin(t, "list", map[string]string{"json": "true"})
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	if strings.Contains(js, g3Secret) {
		t.Errorf("list --json carries the credential:\n%s", js)
	}
}

// ---- 6a, 6a': byte-identical match never transfers ownership (T5, BODY-14; D10) --

func TestPluginInstallByteIdenticalOwnedByOtherPluginRefused(t *testing.T) {
	content := validPluginFormula("acme-triage")
	cases := []struct {
		name   string
		owners []string
	}{
		{"one_other_owner", []string{"rival"}},
		{"two_other_owners_named_sorted", []string{"zeta", "beta"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupFactoryDir(t)
			writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": content})
			writeProjectFormula(t, dir, "acme-triage.formula.toml", content)
			cfg := &config.PluginsConfig{Plugins: map[string]config.PluginEntry{}}
			for _, o := range tc.owners {
				cfg.Plugins[o] = config.PluginEntry{InstalledAt: "2026-01-01T00:00:00Z",
					Formulas: map[string]config.PluginFormula{"acme-triage": {SHA256: hashHex([]byte(content))}}}
			}
			if err := config.SavePluginsConfig(config.PluginsConfigPath(dir), cfg); err != nil {
				t.Fatal(err)
			}
			stubInstallPipeline(t)
			g3OKVerify(t)
			t.Chdir(dir)

			afDir := filepath.Join(dir, ".agentfactory")
			before := snapshotTree(t, afDir)
			manifestBefore, _ := os.ReadFile(config.PluginsConfigPath(dir))

			out, err := runPlugin(t, "install", nil, "acme")
			if err == nil {
				t.Fatalf("install of a stem another plugin owns must be refused; it succeeded:\n%s", out)
			}
			sorted := append([]string(nil), tc.owners...)
			sort.Strings(sorted)
			last := -1
			for _, o := range sorted {
				i := strings.Index(err.Error(), `"`+o+`"`)
				if i < 0 {
					i = strings.Index(err.Error(), o)
				}
				if i < 0 {
					t.Errorf("refusal does not name owner %q: %v", o, err)
					continue
				}
				if i < last {
					t.Errorf("owners not named in sorted order %v: %v", sorted, err)
				}
				last = i
			}
			if manifestAfter, _ := os.ReadFile(config.PluginsConfigPath(dir)); !bytes.Equal(manifestBefore, manifestAfter) {
				t.Errorf("plugins.json changed on a refused install:\n before=%s\n after=%s", manifestBefore, manifestAfter)
			}
			if after := snapshotTree(t, afDir); !reflect.DeepEqual(before, after) {
				t.Errorf(".agentfactory mutated on a refused install:\n before=%v\n after=%v", before, after)
			}
		})
	}
}

func TestClassifyCollisionByteIdentical(t *testing.T) {
	content := validPluginFormula("acme-triage")
	incoming := []byte(content)
	sum := hashHex(incoming)
	const (
		roster       = `"manager":{"type":"interactive","description":"m"},"supervisor":{"type":"autonomous","description":"s"}`
		formulaAgent = `{"agents":{` + roster + `,"acme-triage":{"type":"autonomous","description":"op","formula":"acme-triage"}}}`
		manualAgent  = `{"agents":{` + roster + `,"acme-triage":{"type":"autonomous","description":"op"}}}`
	)
	cases := []struct {
		name       string
		owner      string // "" = unrecorded
		storeCopy  bool
		agentsJSON string // "" = setupFactoryDir roster
		wantOK     bool
	}{
		{"recorded_to_other_plugin", "rival", true, "", false},
		{"recorded_to_this_plugin", "acme", true, "", true},
		{"unrecorded_not_registered_h1_heal", "", true, "", true},
		{"unrecorded_registered_formula_agent", "", true, formulaAgent, false},
		{"recorded_to_other_plugin_store_absent", "rival", false, "", false},
		{"unrecorded_registered_manual_agent", "", true, manualAgent, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupFactoryDir(t)
			if tc.storeCopy {
				writeProjectFormula(t, dir, "acme-triage.formula.toml", content)
			}
			if tc.agentsJSON != "" {
				writeAgentsJSON(t, dir, tc.agentsJSON)
			}
			if tc.owner != "" {
				if err := config.SavePluginsConfig(config.PluginsConfigPath(dir), &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
					tc.owner: {Formulas: map[string]config.PluginFormula{"acme-triage": {SHA256: sum}}},
				}}); err != nil {
					t.Fatal(err)
				}
			}
			u, err := buildCollisionUniverse(dir)
			if err != nil {
				t.Fatalf("buildCollisionUniverse: %v", err)
			}
			status, detail := classifyCollision(u, "acme", "acme-triage", incoming)
			if gotOK := status == pluginStatusOK; gotOK != tc.wantOK {
				t.Errorf("classifyCollision = (%q, %q); want ok=%v", status, detail, tc.wantOK)
			}
		})
	}
}

// ---- 21a, 21d, 22a, 22b: list --json contract and invalid dirs (BODY-9, BODY-11; D16, D23)

func TestPluginListJSONEmptyIsArray(t *testing.T) {
	t.Run("plugins_dir_present_but_empty", func(t *testing.T) {
		dir := setupFactoryDir(t)
		if err := os.MkdirAll(config.PluginsDir(dir), 0o755); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		if err != nil {
			t.Fatalf("list --json: %v", err)
		}
		if got := strings.TrimSpace(out); got != "[]" {
			t.Errorf("list --json with an empty plugins dir = %q, want []", got)
		}
	})

	t.Run("only_invalid_dir_is_still_an_array", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writePluginFixture(t, dir, "acme.agents", map[string]string{
			"acme-triage.formula.toml": validPluginFormula("acme-triage"),
		})
		t.Chdir(dir)
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		if err != nil {
			t.Fatalf("list --json: %v", err)
		}
		if got := strings.TrimSpace(out); !strings.HasPrefix(got, "[") {
			t.Errorf("list --json must always be an array, got %q", got)
		}
	})

	t.Run("plugin_without_formulas_has_empty_formulas_array", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writePluginFixture(t, dir, "acme", map[string]string{"README.md": "no formulas yet\n"})
		t.Chdir(dir)
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		if err != nil {
			t.Fatalf("list --json: %v", err)
		}
		var rows []map[string]json.RawMessage
		if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); e != nil {
			t.Fatalf("list --json not an array of objects: %v\n%s", e, out)
		}
		if len(rows) != 1 {
			t.Fatalf("want one row, got %d:\n%s", len(rows), out)
		}
		if got := string(rows[0]["formulas"]); got != "[]" {
			t.Errorf(`nested "formulas" = %s, want []`, got)
		}
	})
}

func TestPluginListJSONInternalErrorIsJSON(t *testing.T) {
	assertStateError := func(t *testing.T, out string, err error, wantInError string) {
		t.Helper()
		if err != nil {
			t.Errorf("list --json infrastructure error must exit 0 with a JSON body, got err: %v", err)
		}
		var obj map[string]any
		if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &obj); e != nil {
			t.Fatalf("list --json error output is not a JSON object: %v\nstdout:\n%s", e, out)
		}
		if obj["state"] != "error" {
			t.Errorf(`state = %v, want "error": %s`, obj["state"], out)
		}
		msg, _ := obj["error"].(string)
		if msg == "" || !strings.Contains(msg, wantInError) {
			t.Errorf("error field %q must be non-empty and mention %q", msg, wantInError)
		}
	}

	t.Run("unresolvable_root", func(t *testing.T) {
		t.Chdir(t.TempDir())
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		assertStateError(t, out, err, "")
	})

	t.Run("corrupt_manifest", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writePluginFixture(t, dir, "acme", map[string]string{
			"acme-triage.formula.toml": validPluginFormula("acme-triage"),
		})
		if err := os.WriteFile(config.PluginsConfigPath(dir), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Chdir(dir)
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		assertStateError(t, out, err, "plugins.json")
	})
}

func TestPluginListNamesInvalidPluginDir(t *testing.T) {
	t.Run("name_fails_agent_regex_human", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writePluginFixture(t, dir, "acme.agents", map[string]string{
			"acme-triage.formula.toml": validPluginFormula("acme-triage"),
		})
		t.Chdir(dir)
		out, err := runPlugin(t, "list", nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		for _, want := range []string{"acme.agents", "NOT INSTALLABLE", "not a valid agent name"} {
			if !strings.Contains(out, want) {
				t.Errorf("list output missing %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, "no plugins acquired") {
			t.Errorf("zero-state message printed although a plugin dir exists:\n%s", out)
		}
	})

	t.Run("name_fails_agent_regex_json", func(t *testing.T) {
		dir := setupFactoryDir(t)
		writePluginFixture(t, dir, "acme.agents", map[string]string{
			"acme-triage.formula.toml": validPluginFormula("acme-triage"),
		})
		t.Chdir(dir)
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		if err != nil {
			t.Fatalf("list --json: %v", err)
		}
		var rows []map[string]json.RawMessage
		if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); e != nil {
			t.Fatalf("list --json not an array of objects: %v\n%s", e, out)
		}
		var row map[string]json.RawMessage
		for _, r := range rows {
			if string(r["name"]) == `"acme.agents"` {
				row = r
			}
		}
		if row == nil {
			t.Fatalf("list --json has no row for acme.agents:\n%s", out)
		}
		var msg string
		if e := json.Unmarshal(row["error"], &msg); e != nil || msg == "" {
			t.Errorf(`row for acme.agents lacks a non-empty "error" field: %s`, out)
		}
		if got := string(row["formulas"]); got != "[]" {
			t.Errorf(`row for acme.agents "formulas" = %s, want []`, got)
		}
	})

	t.Run("unreadable_dir_human", func(t *testing.T) {
		if os.Geteuid() == 0 {
			t.Skip("root reads a 000 directory")
		}
		dir := setupFactoryDir(t)
		plug := writePluginFixture(t, dir, "locked", map[string]string{
			"locked-triage.formula.toml": validPluginFormula("locked-triage"),
		})
		if err := os.Chmod(plug, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { os.Chmod(plug, 0o755) })
		t.Chdir(dir)
		out, err := runPlugin(t, "list", nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(out, "locked") || !strings.Contains(out, "NOT INSTALLABLE") {
			t.Errorf("list must name the unreadable plugin dir as NOT INSTALLABLE:\n%s", out)
		}
	})
}

// ---- 25a-25c: third-party strings are display-escaped (BODY-15; D25) -----------

const (
	g3EscFile   = "\x1b[31mred.formula.toml"
	g3EscSource = "/srv/\x1b[2Jevil"
)

// Parses as TOML but fails validation with a Detail that quotes the raw ESC.
const g3EscDetailFormula = "formula = \"baddeps\"\ntype = \"workflow\"\nversion = 1\ndescription = \"d\"\n\n[[steps]]\nid = \"s\"\ntitle = \"t\"\nneeds = [\"n\\u001b[2Jo\"]\n"

func g3StubEscProvenance(t *testing.T) {
	t.Helper()
	orig := runGitProvenance
	t.Cleanup(func() { runGitProvenance = orig })
	runGitProvenance = func(string) (string, string) { return g3EscSource, "abc1234def" }
}

func TestPluginListEscapesControlBytes(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{
		g3EscFile:              validPluginFormula("red"),
		"baddeps.formula.toml": g3EscDetailFormula,
	})
	g3StubEscProvenance(t)
	t.Chdir(dir)

	out, err := runPlugin(t, "list", nil)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if g3HasControlBytes(out) {
		t.Errorf("human list output carries raw control bytes:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf(`human list output must show the escape visibly (e.g. \x1b):\n%q`, out)
	}
}

func TestPluginInstallNarrationEscapesControlBytes(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{
		"acme-triage.formula.toml": validPluginFormula("acme-triage"),
		g3EscFile:                  validPluginFormula("red"),
	})
	g3StubEscProvenance(t)
	stubInstallPipeline(t)
	g3OKVerify(t)
	t.Chdir(dir)

	out, _ := runPlugin(t, "install", nil, "acme")
	if !strings.Contains(out, "installing 1 plugin(s)") {
		t.Fatalf("install narration not printed:\n%q", out)
	}
	if g3HasControlBytes(out) {
		t.Errorf("install narration carries raw control bytes:\n%q", out)
	}
	if !strings.Contains(out, `\x1b`) {
		t.Errorf(`install narration must show the escape visibly (e.g. \x1b):\n%q`, out)
	}
}

func TestPluginListJSONKeepsExactFilename(t *testing.T) {
	dir := setupFactoryDir(t)
	writePluginFixture(t, dir, "acme", map[string]string{g3EscFile: validPluginFormula("red")})
	t.Chdir(dir)

	out, err := runPlugin(t, "list", map[string]string{"json": "true"})
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	if strings.Contains(out, "\x1b") {
		t.Errorf("list --json carries a raw ESC byte:\n%q", out)
	}
	var got []pluginListJSON
	if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); e != nil {
		t.Fatalf("json: %v\n%s", e, out)
	}
	found := false
	for _, p := range got {
		for _, f := range p.Formulas {
			if f.File == g3EscFile {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("list --json does not round-trip the exact filename %q:\n%s", g3EscFile, out)
	}
}
