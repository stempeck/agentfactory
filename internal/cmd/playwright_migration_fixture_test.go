package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPlaywrightMigration_RefusalNamesUninstallThenFormulaInstallSucceeds walks the per-container
// migration on a fixture Claude Code config: while the old user-scope Playwright is enabled, installing
// an integration whose plugin is also named playwright is refused with the exact uninstall command; once
// the operator has run it, the same install succeeds with no --factory-wide. af itself removes nothing.
func TestPlaywrightMigration_RefusalNamesUninstallThenFormulaInstallSucceeds(t *testing.T) {
	e := intBFactory(t)
	settings := filepath.Join(e.claudeDir, "settings.json")
	enabled := `{"enabledPlugins":{"playwright@claude-plugins-official":true}}` + "\n"
	if err := os.WriteFile(settings, []byte(enabled), 0o644); err != nil {
		t.Fatal(err)
	}
	intBAcquire(t, e, intBName, intBMutate(e, intBManifestOpts{}, func(f map[string]intBFile) {
		f["claude-plugin/.claude-plugin/plugin.json"] = intBFile{`{"name":"playwright","version":"0.1.0"}` + "\n", 0o644}
	}))

	_, err := runPlugin(t, "install", nil, intBName)
	const uninstall = "claude plugin uninstall playwright@claude-plugins-official --scope user"
	if err == nil || !strings.Contains(err.Error(), uninstall) {
		t.Fatalf("install beside the user-scope Playwright must be refused naming %q; got %v", uninstall, err)
	}
	if after, _ := os.ReadFile(settings); string(after) != enabled {
		t.Fatalf("the refusal changed the user's Claude Code settings:\n%s", after)
	}

	if err := os.WriteFile(settings, []byte(`{"enabledPlugins":{}}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := runPlugin(t, "install", nil, intBName); err != nil {
		t.Fatalf("after the uninstall, the formula-scope install must succeed without --factory-wide: %v\n%s", err, out)
	}
}
