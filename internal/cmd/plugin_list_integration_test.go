package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// ---- AC8: af plugin list shows integrations (L584-589; decisions D20, D21) ---

// intBListIntegrationKeys is the exact key set of a row's "integration" object (L585).
var intBListIntegrationKeys = []string{"check", "hook_fail_mode", "kinds", "probe", "scope", "snapshot_dir"}

// intBListFormulaRowKeys is every key a formula row may carry: the pre-Phase-2 five plus
// "integration" (DO-NOT-CHANGE for the old five).
var intBListFormulaRowKeys = map[string]bool{"name": true, "source": true, "commit": true, "error": true, "formulas": true, "integration": true}

// intBListFixture records acme-int (snapshot + v2 entry, acquisition dir present) and acquires
// the formula plugin zeta.
func intBListFixture(t *testing.T) (*intBEnv, string) {
	t.Helper()
	e := intBFactory(t)
	src := intBSource(e, intBName, intBManifestOpts{})
	intBAcquire(t, e, intBName, src)
	snap := intBRecord(t, e, intBName, src, nil)
	intBAcquireFormulaPlugin(t, e, "zeta", "zeta-triage")
	return e, snap
}

func intBListJSONRows(t *testing.T) (map[string]map[string]json.RawMessage, string) {
	t.Helper()
	out, err := runPlugin(t, "list", map[string]string{"json": "true"})
	if err != nil {
		t.Fatalf("plugin list --json: %v\n%s", err, out)
	}
	var rows []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &rows); err != nil {
		t.Fatalf("list --json is not an array of objects: %v\n%s", err, out)
	}
	byName := map[string]map[string]json.RawMessage{}
	for _, r := range rows {
		var name string
		_ = json.Unmarshal(r["name"], &name)
		byName[name] = r
	}
	return byName, out
}

func intBIntegrationObject(t *testing.T, rows map[string]map[string]json.RawMessage, name, out string) map[string]json.RawMessage {
	t.Helper()
	row, ok := rows[name]
	if !ok {
		t.Fatalf("list --json has no %s row:\n%s", name, out)
	}
	raw, ok := row["integration"]
	if !ok || strings.TrimSpace(string(raw)) == "null" {
		t.Fatalf("the %s row carries no integration object (got %s):\n%s", name, raw, out)
	}
	var obj map[string]json.RawMessage
	if err := json.Unmarshal(raw, &obj); err != nil {
		t.Fatalf("integration is not an object: %v (%s)", err, raw)
	}
	return obj
}

func intBRealPath(t *testing.T, p string) string {
	t.Helper()
	r, err := filepath.EvalSymlinks(p)
	if err != nil {
		return filepath.Clean(p)
	}
	return r
}

func TestPluginListJSON_IntegrationRow(t *testing.T) {
	t.Run("formula_row_keys", func(t *testing.T) {
		intBListFixture(t)
		rows, out := intBListJSONRows(t)
		row, ok := rows["zeta"]
		if !ok {
			t.Fatalf("no zeta row:\n%s", out)
		}
		for k := range row {
			if !intBListFormulaRowKeys[k] {
				t.Errorf("formula row carries unexpected key %q", k)
			}
		}
		raw, ok := row["integration"]
		if !ok {
			t.Fatalf(`a formula row must carry "integration": null (present, not omitempty):\n%s`, out)
		}
		if strings.TrimSpace(string(raw)) != "null" {
			t.Errorf("a formula row's integration = %s, want null", raw)
		}
	})

	t.Run("integration_row_keys", func(t *testing.T) {
		intBListFixture(t)
		rows, out := intBListJSONRows(t)
		obj := intBIntegrationObject(t, rows, intBName, out)
		if got := intBSortedKeys(obj); !reflect.DeepEqual(got, intBListIntegrationKeys) {
			t.Errorf("integration keys = %v, want exactly %v", got, intBListIntegrationKeys)
		}
		var check map[string]json.RawMessage
		if err := json.Unmarshal(obj["check"], &check); err != nil {
			t.Fatalf("integration.check is not an object: %v (%s)", err, obj["check"])
		}
		if got := intBSortedKeys(check); !reflect.DeepEqual(got, []string{"at", "state"}) {
			t.Errorf("integration.check keys = %v, want [at state]", got)
		}
	})

	t.Run("integration_row_values", func(t *testing.T) {
		_, snap := intBListFixture(t)
		rows, out := intBListJSONRows(t)
		obj := intBIntegrationObject(t, rows, intBName, out)
		var kinds []string
		if err := json.Unmarshal(obj["kinds"], &kinds); err != nil || kinds == nil {
			t.Errorf("kinds = %s, want a non-null array (%v)", obj["kinds"], err)
		}
		if want := []string{"check", "claude", "env", "install", "service"}; !reflect.DeepEqual(kinds, want) {
			t.Errorf("kinds = %v, want the present sections sorted %v (D21)", kinds, want)
		}
		if s := intBJSONString(t, obj["scope"]); s != "formula" {
			t.Errorf("scope = %q, want formula", s)
		}
		if p := intBJSONString(t, obj["probe"]); p != "tmux-session" {
			t.Errorf("probe = %q, want tmux-session", p)
		}
		if h := intBJSONString(t, obj["hook_fail_mode"]); h != "open" {
			t.Errorf("hook_fail_mode = %q, want open", h)
		}
		sd := intBJSONString(t, obj["snapshot_dir"])
		if !filepath.IsAbs(sd) || intBRealPath(t, sd) != intBRealPath(t, snap) {
			t.Errorf("snapshot_dir = %q, want the absolute consumed snapshot %q", sd, snap)
		}
		var check map[string]string
		if err := json.Unmarshal(obj["check"], &check); err != nil {
			t.Fatalf("check: %v", err)
		}
		if !reflect.DeepEqual(check, map[string]string{"state": "", "at": ""}) {
			t.Errorf(`with no check record, check = %v, want {"state":"","at":""} (D21)`, check)
		}
	})

	t.Run("check_record_reflected", func(t *testing.T) {
		e, _ := intBListFixture(t)
		rec := `{"v":1,"plugin":"acme-int","state":"fail","exit":2,"at":"2026-09-28T01:02:03Z","duration_ms":5,"output":"x","hook_fail_mode":"open","service_rss_kb":0,"claude_code_version":""}` + "\n"
		p := intBCheckRecordPath(e.root, intBName)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(rec), 0o644); err != nil {
			t.Fatal(err)
		}
		rows, out := intBListJSONRows(t)
		obj := intBIntegrationObject(t, rows, intBName, out)
		var check map[string]string
		if err := json.Unmarshal(obj["check"], &check); err != nil {
			t.Fatalf("check: %v", err)
		}
		if want := map[string]string{"state": "fail", "at": "2026-09-28T01:02:03Z"}; !reflect.DeepEqual(check, want) {
			t.Errorf("check = %v, want %v from the check record", check, want)
		}
	})

	t.Run("listed_after_acquisition_dir_removed", func(t *testing.T) {
		e, _ := intBListFixture(t)
		if err := os.RemoveAll(filepath.Join(config.PluginsDir(e.root), intBName)); err != nil {
			t.Fatal(err)
		}
		rows, out := intBListJSONRows(t)
		obj := intBIntegrationObject(t, rows, intBName, out)
		if got := intBSortedKeys(obj); !reflect.DeepEqual(got, intBListIntegrationKeys) {
			t.Errorf("record-only row keys = %v, want %v", got, intBListIntegrationKeys)
		}
		if _, ok := rows["zeta"]; !ok {
			t.Errorf("the formula plugin row disappeared:\n%s", out)
		}
	})

	t.Run("listed_when_plugins_dir_absent", func(t *testing.T) {
		e, _ := intBListFixture(t)
		if err := os.RemoveAll(config.PluginsDir(e.root)); err != nil {
			t.Fatal(err)
		}
		rows, out := intBListJSONRows(t)
		intBIntegrationObject(t, rows, intBName, out)

		text, err := runPlugin(t, "list", nil)
		if err != nil {
			t.Fatalf("plugin list (text): %v", err)
		}
		if strings.Contains(text, "no plugins acquired") {
			t.Errorf("an installed integration suppresses the zero-state line:\n%s", text)
		}
		if !strings.Contains(text, intBName) {
			t.Errorf("text list must show the installed integration %s:\n%s", intBName, text)
		}
	})

	t.Run("arrays_never_null", func(t *testing.T) {
		// L585 "Arrays are never null": a jq consumer can iterate every array of an integration
		// row. acme-int is listed from its acquisition dir; acme-min is a record-only integration
		// whose manifest declares no section at all, so its kinds are empty.
		e, _ := intBListFixture(t)
		minimal := map[string]intBFile{
			config.IntegrationManifestFile: {"name = \"acme-min\"\ndescription = \"minimal\"\n", 0o644},
		}
		intBRecord(t, e, "acme-min", minimal, func(entry *config.PluginEntry) {
			in := entry.Integration
			in.ClaudePlugins, in.EnvKeys, in.ExternalWrites = []string{}, []string{}, []string{}
			in.ExternalWriteHashes = map[string]string{}
			in.Service, in.ServiceProbe = "", ""
		})
		rows, out := intBListJSONRows(t)
		for _, name := range []string{intBName, "acme-min"} {
			obj := intBIntegrationObject(t, rows, name, out)
			if path, isNull := intBFindJSONNull(rows[name], name); isNull {
				t.Errorf("the %s row has a JSON null at %s; arrays are never null (L585):\n%s", name, path, out)
			}
			if f := strings.TrimSpace(string(rows[name]["formulas"])); f != "[]" {
				t.Errorf("the %s row's formulas = %s, want [] (an integration records no formulas)", name, f)
			}
			if k := strings.TrimSpace(string(obj["kinds"])); !strings.HasPrefix(k, "[") {
				t.Errorf("the %s row's kinds = %s, want an array", name, k)
			}
		}
		if obj := intBIntegrationObject(t, rows, "acme-min", out); strings.TrimSpace(string(obj["kinds"])) != "[]" {
			t.Errorf("a manifest with no sections lists kinds = %s, want []", obj["kinds"])
		}
	})

	t.Run("upstream_formulas_not_listed", func(t *testing.T) {
		// H3-16 / spec L537: appendNestedFormulas skips .upstream/, so the pinned upstream checkout's
		// *.formula.toml files are not surfaced as out-of-contract formulas of the plugin.
		e, _ := intBListFixture(t)
		intBWriteTree(t, filepath.Join(config.PluginsDir(e.root), intBName, ".upstream"), map[string]intBFile{
			"agents/up-agent.formula.toml": {validPluginFormula("up-agent"), 0o644},
		})
		rows, out := intBListJSONRows(t)
		row, ok := rows[intBName]
		if !ok {
			t.Fatalf("list --json has no %s row:\n%s", intBName, out)
		}
		var formulas []pluginListFormulaJSON
		if err := json.Unmarshal(row["formulas"], &formulas); err != nil {
			t.Fatalf("formulas: %v (%s)", err, row["formulas"])
		}
		for _, f := range formulas {
			if strings.HasPrefix(filepath.ToSlash(f.File), ".upstream/") {
				t.Errorf("list must not surface .upstream/ content as a plugin formula (H3-16); the %s row lists %s [%s]", intBName, f.File, f.Status)
			}
		}
		text, err := runPlugin(t, "list", nil)
		if err != nil {
			t.Fatalf("plugin list (text): %v", err)
		}
		if strings.Contains(text, "up-agent") {
			t.Errorf("text list surfaces the .upstream/ formula up-agent:\n%s", text)
		}
	})

	t.Run("corrupt_record_same_error_when_plugins_dir_absent", func(t *testing.T) {
		e := intBFactory(t)
		if err := os.WriteFile(config.PluginsConfigPath(e.root), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
		intBAcquireFormulaPlugin(t, e, "zeta", "zeta-triage")
		_, presentErr := runPlugin(t, "list", nil)
		if presentErr == nil {
			t.Fatal("precondition: a corrupt plugins.json with store/plugins/ present is an error today")
		}
		if err := os.RemoveAll(config.PluginsDir(e.root)); err != nil {
			t.Fatal(err)
		}
		out, absentErr := runPlugin(t, "list", nil)
		if absentErr == nil {
			t.Fatalf("a corrupt plugins.json must be an error even when store/plugins/ is absent (D20); got success:\n%s", out)
		}
		if absentErr.Error() != presentErr.Error() {
			t.Errorf("absent-dir error %q differs from the present-dir error %q (D20: same error)", absentErr, presentErr)
		}
	})
}

// intBFindJSONNull reports the first JSON null at any depth under v (as a dotted path).
func intBFindJSONNull(v any, path string) (string, bool) {
	switch x := v.(type) {
	case map[string]json.RawMessage:
		for k, raw := range x {
			if p, ok := intBFindJSONNull(raw, path+"."+k); ok {
				return p, true
			}
		}
	case json.RawMessage:
		var decoded any
		if err := json.Unmarshal(x, &decoded); err != nil {
			return path, false
		}
		return intBFindJSONNull(decoded, path)
	case map[string]any:
		for k, sub := range x {
			if p, ok := intBFindJSONNull(sub, path+"."+k); ok {
				return p, true
			}
		}
	case []any:
		for i, sub := range x {
			if p, ok := intBFindJSONNull(sub, fmt.Sprintf("%s[%d]", path, i)); ok {
				return p, true
			}
		}
	case nil:
		return path, true
	}
	return "", false
}
