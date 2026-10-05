package cmd

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// PR #539 T1/T6 (D3): needs relinkSelfForReinstall to be an ADR-009 package-var seam.
func TestPluginInstallRelinksBeforeStaging(t *testing.T) {
	for _, tc := range []struct {
		name    string
		plugins []string
	}{
		{"single", []string{"acme"}},
		{"batch", []string{"acme", "beta"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupFactoryDir(t)
			writePluginFixture(t, dir, "acme", map[string]string{"acme-triage.formula.toml": validPluginFormula("acme-triage")})
			writePluginFixture(t, dir, "beta", map[string]string{"beta-scout.formula.toml": validPluginFormula("beta-scout")})
			stubInstallPipeline(t)
			stubPluginVerify(t, func(cmd *cobra.Command, root string, names []string) error { return nil })

			var events []string
			runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error {
				events = append(events, "agentgen")
				return nil
			}
			orig := relinkSelfForReinstall
			t.Cleanup(func() { relinkSelfForReinstall = orig })
			relinkSelfForReinstall = func(cmd *cobra.Command) {
				events = append(events, "relink")
				if _, err := os.Stat(filepath.Join(config.FormulasDir(dir), "acme-triage.formula.toml")); err == nil {
					t.Error("relink ran after the store copy was staged")
				}
				if _, err := os.Stat(config.PluginsConfigPath(dir)); err == nil {
					t.Error("relink ran after plugins.json was written")
				}
			}
			t.Chdir(dir)

			out, err := runPlugin(t, "install", nil, tc.plugins...)
			if err != nil {
				t.Fatalf("install: %v\n%s", err, out)
			}
			relinks := 0
			for _, e := range events {
				if e == "relink" {
					relinks++
				}
			}
			if relinks != 1 {
				t.Errorf("relink ran %d times, want exactly 1; events=%v", relinks, events)
			}
			if len(events) == 0 || events[0] != "relink" {
				t.Errorf("relink must precede the agent-gen seam; events=%v", events)
			}
		})
	}
}
