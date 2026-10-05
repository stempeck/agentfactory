package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
)

func TestPluginList_MissingIntegrationsIsListOnly(t *testing.T) {
	dir := setupFactoryDir(t)
	needsInt := "formula = \"needsint\"\ntype = \"workflow\"\nversion = 1\nintegrations = [\"nope\"]\n\n[[steps]]\nid = \"step1\"\ntitle = \"Step 1\"\n"
	writePluginFixture(t, dir, "acme", map[string]string{"needsint.formula.toml": needsInt})
	stubInstallPipeline(t)
	stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })
	t.Chdir(dir)

	t.Run("json row keeps status ok and lists the missing integration separately", func(t *testing.T) {
		out, err := runPlugin(t, "list", map[string]string{"json": "true"})
		if err != nil {
			t.Fatalf("list --json: %v", err)
		}
		var got []struct {
			Formulas []map[string]json.RawMessage `json:"formulas"`
		}
		if e := json.Unmarshal([]byte(strings.TrimSpace(out)), &got); e != nil {
			t.Fatalf("json: %v\n%s", e, out)
		}
		var row map[string]json.RawMessage
		for _, p := range got {
			for _, f := range p.Formulas {
				if string(f["file"]) == `"needsint.formula.toml"` {
					row = f
				}
			}
		}
		if row == nil {
			t.Fatalf("no row for needsint.formula.toml in %s", out)
		}
		if string(row["status"]) != `"`+pluginStatusOK+`"` {
			t.Errorf("status = %s, want %q: missing integrations are not a Status value", row["status"], pluginStatusOK)
		}
		if strings.ReplaceAll(string(row["missing_integrations"]), " ", "") != `["nope"]` {
			t.Errorf("missing_integrations = %s, want [\"nope\"] (row: %v)", row["missing_integrations"], keysOf(row))
		}
	})

	t.Run("text list shows it", func(t *testing.T) {
		out, err := runPlugin(t, "list", nil)
		if err != nil {
			t.Fatalf("list: %v", err)
		}
		if !strings.Contains(out, "nope") {
			t.Errorf("text list does not name the missing integration nope:\n%s", out)
		}
	})

	t.Run("install is not refused", func(t *testing.T) {
		if _, err := runPlugin(t, "install", nil, "acme"); err != nil {
			t.Fatalf("install of a formula plugin whose formula needs an uninstalled integration refused: %v", err)
		}
		if _, err := os.Stat(filepath.Join(config.FormulasDir(dir), "needsint.formula.toml")); err != nil {
			t.Errorf("needsint.formula.toml not staged: %v", err)
		}
	})
}
