//go:build !integration

package cmd

import (
	"strings"
	"testing"
)

const p724a2RefusalsHeading = "### What else `af config models set` refuses"

// p724a2RefusalsSection returns the refusals subsection of USING_MODELS.md, heading through the line
// before the next heading of any level.
func p724a2RefusalsSection(t *testing.T) string {
	t.Helper()
	content := repoFile(t, "USING_MODELS.md")
	start := strings.Index(content, p724a2RefusalsHeading)
	if start < 0 {
		t.Fatalf("USING_MODELS.md has no %q section", p724a2RefusalsHeading)
	}
	body := content[start+len(p724a2RefusalsHeading):]
	if end := strings.Index(body, "\n#"); end >= 0 {
		body = body[:end]
	}
	return body
}

// p724a2Flat collapses whitespace runs so a reflowed sentence still matches.
func p724a2Flat(s string) string { return strings.Join(strings.Fields(s), " ") }

// p724a2Bullets returns each top-level "- " list item of a section, flattened, together with its
// indented continuation lines — so a migration note written as an indented paragraph stays with its
// bullet.
func p724a2Bullets(section string) []string {
	var bullets, cur []string
	flush := func() {
		if cur != nil {
			bullets = append(bullets, p724a2Flat(strings.Join(cur, "\n")))
			cur = nil
		}
	}
	for _, line := range strings.Split(section, "\n") {
		switch {
		case strings.HasPrefix(line, "- "):
			flush()
			cur = []string{line}
		case cur != nil && (strings.TrimSpace(line) == "" || line[0] == ' ' || line[0] == '\t'):
			cur = append(cur, line)
		default:
			flush()
		}
	}
	flush()
	return bullets
}

// p724a2PluginChannelBullet scopes the T19 wording checks to the one bullet that names the refused
// keys; the gateway-credential bullet beside it already carries a "Migration note" and "*load*".
func p724a2PluginChannelBullet(t *testing.T) string {
	t.Helper()
	for _, b := range p724a2Bullets(p724a2RefusalsSection(t)) {
		if strings.Contains(b, "`CLAUDE_CODE_PLUGIN_DIRS`") {
			return b
		}
	}
	t.Fatalf("USING_MODELS.md %q has no bullet naming `CLAUDE_CODE_PLUGIN_DIRS`, which validateModelProfile refuses", p724a2RefusalsHeading)
	return ""
}

func TestPR724_T19_RefusalsNamePluginChannelKeys(t *testing.T) {
	section := p724a2RefusalsSection(t)
	for _, key := range []string{"CLAUDE_CODE_PLUGIN_DIRS", "CLAUDE_CONFIG_DIR"} {
		if !strings.Contains(section, "`"+key+"`") {
			t.Errorf("USING_MODELS.md %q does not name `%s`, which a models.json profile may not set", p724a2RefusalsHeading, key)
		}
	}
}

func TestPR724_T19_RefusalNamesIntegrationRemedy(t *testing.T) {
	bullet := p724a2PluginChannelBullet(t)
	if !strings.Contains(strings.ToLower(bullet), "install an integration") {
		t.Errorf("the plugin-channel refusal does not tell the operator to install an integration instead: %q", bullet)
	}
	if !strings.Contains(bullet, "(USING_PLUGINS.md)") {
		t.Errorf("the plugin-channel refusal does not link USING_PLUGINS.md for the remedy: %q", bullet)
	}
}

// The gateway note's "every `af` verb fail" over-states a launch: resolveModelEnvForSession fails only a
// --model launch and otherwise warns and drops models.json.
func TestPR724_T19_RefusalCarriesLoadTimeMigrationNote(t *testing.T) {
	bullet := p724a2PluginChannelBullet(t)
	plain := strings.ReplaceAll(bullet, "*", "")
	for _, want := range []string{
		"Migration note",
		"enforced at load, not only at write",
		"--model",
		"ignoring models.json",
		"global default model",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("the plugin-channel refusal's load-time migration note lacks %q: %q", want, bullet)
		}
	}
	if strings.Contains(bullet, "every `af` verb") {
		t.Errorf("the plugin-channel refusal claims every af verb fails; a launch without --model warns and starts on the global default model: %q", bullet)
	}
}

func TestPR724_T19_RefusalsLeadInIsCountFree(t *testing.T) {
	flat := p724a2Flat(p724a2RefusalsSection(t))
	for _, n := range []string{"two", "three", "four", "five", "six", "seven", "eight"} {
		if strings.Contains(flat, "enforces "+n+" extra rules") {
			t.Errorf("USING_MODELS.md %q still counts its rules (%q); a counted lead-in goes false whenever a bullet is added", p724a2RefusalsHeading, "enforces "+n+" extra rules")
		}
	}
	const want = "enforces these extra rules; each names the offending profile and the fix"
	if !strings.Contains(flat, want) {
		t.Errorf("USING_MODELS.md %q lead-in does not read %q", p724a2RefusalsHeading, want)
	}
}

func TestPR724_T19_KeepExistingRefusalBullets(t *testing.T) {
	flat := p724a2Flat(p724a2RefusalsSection(t))
	for _, want := range []string{
		"A profile still pinned by `dispatch.json` cannot be dropped or renamed.",
		"`****` is refused as an auth token",
		"A rewritten profile loses its fitness attestation.",
		"A profile cannot name an upstream gateway credential.",
		"`OPENAI_API_KEY`",
		"`CHATGPT_TOKEN_DIR`",
		"`CHATGPT_AUTH_FILE`",
		"`CHATGPT_API_BASE`",
		"`CODEX_HOME`",
	} {
		if !strings.Contains(flat, want) {
			t.Errorf("USING_MODELS.md %q lost %q", p724a2RefusalsHeading, want)
		}
	}
}
