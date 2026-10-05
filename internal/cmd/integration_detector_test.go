package cmd

// Phase 2 (design 695 K10, AC 8) pins for the read-only user-scope plugin detector. Every
// test pins CLAUDE_CONFIG_DIR to a fixture: this host's real ~/.claude enables user-scope
// plugins, so an unpinned read would make these tests environment-dependent (H5-4).

import (
	"bytes"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// writeUserScopeSettings writes <dir>/settings.json enabling frontend-design@x and
// playwright@x, with one disabled key that must never be reported (D17).
func writeUserScopeSettings(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	settings := `{"enabledPlugins":{"frontend-design@x":true,"playwright@x":true,"disabled-one@x":false}}`
	if err := os.WriteFile(filepath.Join(dir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
}

func rowsNaming(rows []string, name string) int {
	n := 0
	for _, r := range rows {
		if strings.Contains(r, name) {
			n++
		}
	}
	return n
}

// TestUserScopeDetector_OneWarningNamesBoth (AC 8): with frontend-design@x and
// playwright@x enabled at user scope and no integration installed, the detector names
// both (and not the disabled key), and bare af up prints exactly one stderr warning line
// naming both.
func TestUserScopeDetector_OneWarningNamesBoth(t *testing.T) {
	root := integrationUpRoot(t)
	claudeDir := filepath.Join(root, "claude-config")
	writeUserScopeSettings(t, claudeDir)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	rows := detectUnaccountedUserScope(root)
	if rowsNaming(rows, "frontend-design") != 1 || rowsNaming(rows, "playwright") != 1 {
		t.Errorf("the detector must name frontend-design and playwright once each; rows=%q", rows)
	}
	if rowsNaming(rows, "disabled-one") != 0 {
		t.Errorf("a disabled enabledPlugins key is not an active user-scope channel (D17); rows=%q", rows)
	}

	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)
	stubIntegrationServicesEnsure(t, nil, nil)

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	_ = runUp(cmd, nil)

	var lines []string
	for _, l := range strings.Split(errBuf.String(), "\n") {
		if strings.Contains(l, "frontend-design") || strings.Contains(l, "playwright") {
			lines = append(lines, l)
		}
	}
	if len(lines) != 1 {
		t.Fatalf("af up must print exactly one stderr line naming the unaccounted user-scope plugins; got %d: %q\nstderr=%s",
			len(lines), lines, errBuf.String())
	}
	if !strings.Contains(lines[0], "frontend-design") || !strings.Contains(lines[0], "playwright") {
		t.Errorf("the one warning line must name both plugins: %q", lines[0])
	}
	if !strings.Contains(strings.ToLower(lines[0]), "warning") {
		t.Errorf("the detector line is a warning: %q", lines[0])
	}
	if strings.Contains(out.String(), "frontend-design") {
		t.Errorf("the detector warning goes to stderr, not stdout: %q", out.String())
	}
}

// TestUserScopeDetector_RecordedPluginSuppressed (AC 8): a user-scope plugin whose name an
// installed integration records in ClaudePlugins is accounted for and not reported.
func TestUserScopeDetector_RecordedPluginSuppressed(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	claudeDir := filepath.Join(root, "claude-config")
	writeUserScopeSettings(t, claudeDir)
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)

	cfg := &config.PluginsConfig{Plugins: map[string]config.PluginEntry{
		"playwright": {
			Source: "https://example.invalid/playwright",
			Integration: &config.PluginIntegration{
				ContentSHA256: strings.Repeat("e", 64),
				Scope:         "formula",
				ClaudePlugins: []string{"playwright"},
				EnvKeys:       []string{},
				Source:        "embedded",
				SnapshotDir:   filepath.Join(".agentfactory", "store", "integrations", "playwright", strings.Repeat("e", 64)),
			},
		},
	}}
	if err := config.SavePluginsConfig(config.PluginsConfigPath(root), cfg); err != nil {
		t.Fatalf("save plugins.json: %v", err)
	}

	rows := detectUnaccountedUserScope(root)
	if rowsNaming(rows, "frontend-design") != 1 {
		t.Errorf("frontend-design is still unaccounted and must be reported once; rows=%q", rows)
	}
	if rowsNaming(rows, "playwright") != 0 {
		t.Errorf("playwright is recorded in an installed integration's ClaudePlugins and must be suppressed; rows=%q", rows)
	}
}

const p724d2QuickstartKey = "playwright@claude-plugins-official"

// p724d2BareUpWarnings runs a bare af up whose user-scope settings.json holds settings (written verbatim)
// and returns the stderr lines carrying the user-scope warning. It fails unless af up consulted the
// detector exactly once, so a missing line means the row was filtered, never that af up returned before
// it looked.
func p724d2BareUpWarnings(t *testing.T, settings string, setup func(t *testing.T, root string)) (root string, lines []string) {
	t.Helper()
	root = integrationUpRoot(t)
	t.Setenv("AF_ROOT", root)
	claudeDir := filepath.Join(root, "claude-config")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CLAUDE_CONFIG_DIR", claudeDir)
	if setup != nil {
		setup(t, root)
	}
	stubTelemetryBackendGuard(t, nil)
	stubGatewayBackendGuard(t, nil)
	stubIntegrationServicesEnsure(t, nil, nil)
	calls := 0
	orig := detectUnaccountedUserScopeFn
	t.Cleanup(func() { detectUnaccountedUserScopeFn = orig })
	detectUnaccountedUserScopeFn = func(r string) []string {
		calls++
		return orig(r)
	}

	cmd := &cobra.Command{}
	var out, errBuf bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errBuf)
	_ = runUp(cmd, nil)
	if calls != 1 {
		t.Fatalf("bare af up must consult the user-scope detector once, got %d calls; stderr=%s", calls, errBuf.String())
	}
	for _, l := range strings.Split(errBuf.String(), "\n") {
		if strings.Contains(l, "user-scope Claude Code channels") {
			lines = append(lines, l)
		}
	}
	return root, lines
}

// TestPR724_T12_BlanketUpOmitsQuickstartPlaywright: bare af up leaves out the one plugin quickstart installs
// at user scope, by its exact key: the same plugin from another marketplace and another plugin from the same
// marketplace still warn. The detector itself, which af plugin check reads, still reports all three.
func TestPR724_T12_BlanketUpOmitsQuickstartPlaywright(t *testing.T) {
	settings := `{"enabledPlugins":{"` + p724d2QuickstartKey + `":true,"playwright@x":true,"frontend-design@claude-plugins-official":true}}`
	root, lines := p724d2BareUpWarnings(t, settings, nil)

	rows := detectUnaccountedUserScope(root)
	for _, key := range []string{p724d2QuickstartKey, "playwright@x", "frontend-design@claude-plugins-official"} {
		if !slices.Contains(rows, key) {
			t.Errorf("the detector feeds af plugin check and must still report %s; rows=%q", key, rows)
		}
	}

	if len(lines) != 1 {
		t.Fatalf("bare af up must print exactly one user-scope warning line; got %d: %q", len(lines), lines)
	}
	for _, key := range []string{"playwright@x", "frontend-design@claude-plugins-official"} {
		if !strings.Contains(lines[0], key) {
			t.Errorf("the warning must still name %s, a channel quickstart did not install: %q", key, lines[0])
		}
	}
	if strings.Contains(lines[0], p724d2QuickstartKey) {
		t.Errorf("the warning must leave out %s while quickstart installs it: %q", p724d2QuickstartKey, lines[0])
	}
}

// TestPR724_T12_BlanketUpQuietOnQuickstartDefault: a default quickstart factory, whose only user-scope
// plugin is the Playwright quickstart installed, gets no user-scope warning from bare af up.
func TestPR724_T12_BlanketUpQuietOnQuickstartDefault(t *testing.T) {
	_, lines := p724d2BareUpWarnings(t, `{"enabledPlugins":{"`+p724d2QuickstartKey+`":true}}`, nil)
	if len(lines) != 0 {
		t.Errorf("bare af up on a default quickstart factory must print no user-scope warning; got %q", lines)
	}
}

// TestPR724_T12_KeepNonPluginRowsWarned: the exemption drops a plugin key, never a fault or a hooks channel.
// Each of those rows still reaches the bare af up warning beside quickstart's Playwright.
func TestPR724_T12_KeepNonPluginRowsWarned(t *testing.T) {
	enabled := `{"enabledPlugins":{"` + p724d2QuickstartKey + `":true}`
	for _, tc := range []struct {
		name     string
		settings string
		setup    func(t *testing.T, root string)
		want     string
	}{
		{"hooks_row", enabled + `,"hooks":{"PreToolUse":[]}}`, nil, "hooks in "},
		{"settings_unreadable_row", "{not json", nil, "parsing Claude Code user settings"},
		{"plugins_json_error_row", enabled + "}", func(t *testing.T, root string) {
			p := config.PluginsConfigPath(root)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "no plugin is treated as accounted for"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, lines := p724d2BareUpWarnings(t, tc.settings, tc.setup)
			if len(lines) != 1 || !strings.Contains(lines[0], tc.want) {
				t.Errorf("bare af up must still warn with the %q row; got %q", tc.want, lines)
			}
		})
	}
}

// TestPR724_T12_QuickstartUserScopeInstallsMatchExemptSet: the af up exemption lives exactly as long as
// quickstart.sh installs those plugins at user scope. Deleting the installer, or adding another user-scope
// install, fails here until the exemption is changed in the same commit.
func TestPR724_T12_QuickstartUserScopeInstallsMatchExemptSet(t *testing.T) {
	installs := map[string]bool{}
	for _, m := range regexp.MustCompile(`claude plugin install (\S+) --scope[ =]user`).FindAllStringSubmatch(quickstartScriptContent(t), -1) {
		installs[m[1]] = true
	}
	exempt := map[string]bool{}
	for key, on := range quickstartUserScopePlugins {
		if on {
			exempt[key] = true
		}
	}
	if !maps.Equal(installs, exempt) {
		t.Errorf("the bare af up exemption %q must equal the plugins quickstart.sh installs at user scope %q: empty and delete it together with that installer, and exempt a new install only deliberately",
			slices.Sorted(maps.Keys(exempt)), slices.Sorted(maps.Keys(installs)))
	}
}
