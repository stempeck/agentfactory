package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestPR724_T12_KeepInstallRefusesQuickstartPlaywrightName: the af up exemption is not a reader exemption.
// While quickstart's user-scope Playwright is enabled, install still refuses an integration whose Claude
// Code plugin is named playwright, so moving Playwright onto the seam still means uninstalling that copy.
func TestPR724_T12_KeepInstallRefusesQuickstartPlaywrightName(t *testing.T) {
	e := intBFactory(t)
	settings := `{"enabledPlugins":{"playwright@claude-plugins-official":true}}` + "\n"
	if err := os.WriteFile(filepath.Join(e.claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
	intBAcquire(t, e, intBName, intBMutate(e, intBManifestOpts{}, func(f map[string]intBFile) {
		f["claude-plugin/.claude-plugin/plugin.json"] = intBFile{`{"name":"playwright","version":"0.1.0"}` + "\n", 0o644}
	}))

	out, err := runPlugin(t, "install", nil, intBName)
	want := `plugin name "playwright" is already provided by a user-scope plugin; rename it`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("install must refuse with %q; got err=%v\noutput:\n%s", want, err, out)
	}
	if intBExists(e.ext) {
		t.Errorf("[install] run executed before the refusal (external write dir %s exists)", e.ext)
	}
}

// TestPR724_T12_UsingPluginsDocStatesBareUpExemption: the operator guide says the warning comes from a bare
// af up, that it leaves out quickstart's own Playwright, and that af plugin check still lists it.
func TestPR724_T12_UsingPluginsDocStatesBareUpExemption(t *testing.T) {
	doc := g4ReadRepoDoc(t, "USING_PLUGINS.md")
	for _, want := range []string{
		"A bare `af up` also prints one warning line naming the unaccounted user-scope Claude Code plugins.",
		"It leaves out `playwright@claude-plugins-official` while quickstart installs that plugin itself; `af plugin check` still lists it.",
	} {
		if !strings.Contains(doc, want) {
			t.Errorf("USING_PLUGINS.md must say: %s", want)
		}
	}
}

// TestPR724_T12_KeepUsingPluginsCheckSentence: af plugin check keeps reporting every unaccounted user-scope
// plugin, so its sentence in the operator guide stands unqualified.
func TestPR724_T12_KeepUsingPluginsCheckSentence(t *testing.T) {
	doc := g4ReadRepoDoc(t, "USING_PLUGINS.md")
	want := "The report also names user-scope Claude Code plugins (`enabledPlugins` in `settings.json` under `CLAUDE_CONFIG_DIR`, default `~/.claude`) that no installed integration accounts for."
	if !strings.Contains(doc, want) {
		t.Errorf("USING_PLUGINS.md's af plugin check bullet must still say: %s", want)
	}
}
