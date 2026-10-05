package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func g4MarkdownSection(content, heading string) (string, bool) {
	lines := strings.Split(content, "\n")
	start := -1
	for i, l := range lines {
		if strings.TrimRight(l, " \t\r") == heading {
			start = i
			break
		}
	}
	if start == -1 {
		return "", false
	}
	inFence := false
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			inFence = !inFence
			continue
		}
		if !inFence && strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	return strings.Join(lines[start+1:end], "\n"), true
}

func g4ReadRepoDoc(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return string(data)
}

// Design 695 Phase 2 (IMPLREADME_PHASE2 Slice 5): the group gains acquire, check and remove.
var g4PluginVerbs = []string{"install", "list", "verify", "acquire", "check", "remove"}

// g4BulletCommandVerbs returns the verbs of the bullet's leading `af plugin a|b|c` code span, so a
// verb word elsewhere in the prose ("install and check agents") cannot satisfy the pin.
func g4BulletCommandVerbs(bullet string) map[string]bool {
	verbs := map[string]bool{}
	rest := strings.TrimPrefix(bullet, "- `af plugin")
	end := strings.Index(rest, "`")
	if end < 0 {
		return verbs
	}
	for _, v := range strings.Split(strings.TrimSpace(rest[:end]), "|") {
		verbs[strings.TrimSpace(v)] = true
	}
	return verbs
}

// 15a (BODY-2, D18): exactly one `## CLI Commands` bullet documents af plugin and all its verbs.
func TestClaudeMdDoc_PluginVerbs(t *testing.T) {
	section, ok := g4MarkdownSection(g4ReadRepoDoc(t, "CLAUDE.md"), "## CLI Commands")
	if !ok {
		t.Fatal("CLAUDE.md has no `## CLI Commands` section")
	}
	var bullets []string
	for _, l := range strings.Split(section, "\n") {
		if strings.HasPrefix(l, "- `af plugin") {
			bullets = append(bullets, l)
		}
	}
	if len(bullets) != 1 {
		t.Fatalf("CLAUDE.md `## CLI Commands` has %d `af plugin` bullet(s), want exactly 1: %q", len(bullets), bullets)
	}
	// IMPLREADME_PHASE2 Slice 5: `af plugin list|install|verify|acquire|check|remove`, still <= 200 chars.
	cmdVerbs := g4BulletCommandVerbs(bullets[0])
	for _, verb := range g4PluginVerbs {
		if !cmdVerbs[verb] {
			t.Errorf("CLAUDE.md `af plugin` bullet does not name the %q verb in its `af plugin a|b|...` command span: %q", verb, bullets[0])
		}
	}
	// PR #539 T2: one clause like its neighbours; the detail lives in the guide it links.
	if !strings.Contains(bullets[0], "(USING_PLUGINS.md)") {
		t.Errorf("CLAUDE.md `af plugin` bullet must link USING_PLUGINS.md: %q", bullets[0])
	}
	if n := len(bullets[0]); n > g4PluginBulletMaxChars {
		t.Errorf("CLAUDE.md `af plugin` bullet is %d characters, want <= %d", n, g4PluginBulletMaxChars)
	}
}

const g4PluginBulletMaxChars = 200

var g4SourceTreeWording = regexp.MustCompile(`(?i)source tree|source-tree|make install|go install`)

var g4PluginForkTrackPhrases = []string{"Editing an installed plugin formula forks it", "Track the plugin", "Keep your edit", "acts on the plugin's repository"}

var g4PluginStatuses = []string{
	"ok", "collides-with-store-formula", "collides-with-manual-agent", "collides-with-embedded-agent",
	"invalid-name", "missing-skills", "parse-error", "out-of-contract", "name-mismatch", "changed", "removed",
}

// 15b (BODY-2, D18; PR #539 T1): the plugin guide lives in USING_PLUGINS.md and satisfies the
// design's grep AC (IMPLREADME_PHASE5.md:301-305).
func TestUsingPluginsDoc_Guide(t *testing.T) {
	guide := g4ReadRepoDoc(t, "USING_PLUGINS.md")
	if !g4SourceTreeWording.MatchString(guide) {
		t.Error("USING_PLUGINS.md does not mention the source-tree rebuild (source tree|source-tree|make install|go install)")
	}
	for _, verb := range g4PluginVerbs {
		if !strings.Contains(guide, "af plugin "+verb) {
			t.Errorf("USING_PLUGINS.md does not show `af plugin %s`", verb)
		}
	}
	// IMPLREADME_PHASE2 Slice 5 (USING_PLUGINS.md:11): the group no longer has three verbs.
	if strings.Contains(guide, "the three verbs") {
		t.Error("USING_PLUGINS.md still says `af plugin --help` lists \"the three verbs\"; the group has six")
	}
	// Design 538 L4 (fork-vs-track for an edited installed copy) and L5 (git inside the nested clone).
	for _, want := range g4PluginForkTrackPhrases {
		if !strings.Contains(guide, want) {
			t.Errorf("USING_PLUGINS.md is missing %q (design 538 L4/L5)", want)
		}
	}
	for _, status := range g4PluginStatuses {
		if !strings.Contains(guide, "`"+status+"`") {
			t.Errorf("USING_PLUGINS.md does not document the `%s` list status", status)
		}
	}
	// IMPLREADME_PHASE2 (consumers.md USING_PLUGINS.md :4 and :91, both UNLISTED in the spec): the
	// intro no longer names three verbs, the opening paragraph no longer describes a formula-only
	// world, and the limits no longer say there is no removal verb at all.
	t.Run("intro_and_limits_not_three_verb_formula_only", func(t *testing.T) {
		intro := guide
		if i := strings.Index(guide, "\n## "); i >= 0 {
			intro = guide[:i]
		}
		introFlat := strings.Join(strings.Fields(intro), " ")
		cmdVerbs := g4DocCommandVerbs(introFlat)
		for _, verb := range g4PluginVerbs {
			if !cmdVerbs[verb] {
				t.Errorf("the USING_PLUGINS.md intro's `af plugin a|b|...` span does not name %q: %q", verb, introFlat)
			}
		}
		if !strings.Contains(strings.ToLower(introFlat), "integration") {
			t.Errorf("the USING_PLUGINS.md intro still describes a formula-only world (no mention of integrations): %q", introFlat)
		}
		repos, ok := g4MarkdownSection(guide, "## Plugin repositories")
		if !ok {
			t.Fatal("USING_PLUGINS.md has no `## Plugin repositories` section")
		}
		opening := strings.SplitN(strings.TrimSpace(repos), "\n\n", 2)[0]
		if !strings.Contains(opening, "af-integration.toml") {
			t.Errorf("the `## Plugin repositories` opening paragraph must say a plugin may carry an af-integration.toml manifest, not only *.formula.toml files: %q", opening)
		}
		limits, ok := g4MarkdownSection(guide, "### v1 limits")
		if !ok {
			t.Fatal("USING_PLUGINS.md has no `### v1 limits` section")
		}
		if strings.Contains(limits, "There is no `af plugin uninstall`") {
			t.Error("`### v1 limits` still says there is no removal verb at all; `af plugin remove` removes integrations")
		}
		if !strings.Contains(limits, "`af plugin remove`") {
			t.Error("`### v1 limits` must qualify the missing uninstall: `af plugin remove` handles integrations only")
		}
	})

	// PR #539 T10: a corrupt plugins.json refuses every non-embedded agent, on launch and on respawn.
	for _, want := range []string{"hand-authored", "plugins.json is repaired", "handoff"} {
		if !strings.Contains(guide, want) {
			t.Errorf("USING_PLUGINS.md does not state the corrupt-manifest blast radius (missing %q)", want)
		}
	}
}

// PR #539 T1: USING_AGENTFACTORY.md stays small — plugins are a Feature Guides pointer, not a section.
func TestUsingAgentfactoryDoc_PluginsIsAFeatureGuidePointer(t *testing.T) {
	using := g4ReadRepoDoc(t, "USING_AGENTFACTORY.md")
	if _, ok := g4MarkdownSection(using, "## Plugin repositories"); ok {
		t.Error("USING_AGENTFACTORY.md still has a `## Plugin repositories` section; it belongs in USING_PLUGINS.md")
	}
	guides, ok := g4MarkdownSection(using, "## Feature Guides")
	if !ok {
		t.Fatal("USING_AGENTFACTORY.md has no `## Feature Guides` section")
	}
	if n := strings.Count(guides, "[USING_PLUGINS.md](USING_PLUGINS.md)"); n != 1 {
		t.Errorf("`## Feature Guides` links USING_PLUGINS.md %d time(s), want exactly 1", n)
	}
	for _, phrase := range g4PluginForkTrackPhrases {
		if strings.Contains(using, phrase) {
			t.Errorf("USING_AGENTFACTORY.md still carries %q; the plugin guide content must move, not be copied", phrase)
		}
	}

	// IMPLREADME_PHASE2 (consumers.md, USING_AGENTFACTORY.md:797): the Feature Guides pointer's
	// `af plugin a|b|...` command span names all six verbs, parsed like the CLAUDE.md bullet.
	t.Run("guide_pointer_names_six_verbs", func(t *testing.T) {
		var pointer string
		for _, l := range strings.Split(guides, "\n") {
			if strings.Contains(l, "[USING_PLUGINS.md](USING_PLUGINS.md)") {
				pointer = l
				break
			}
		}
		if pointer == "" {
			t.Fatal("`## Feature Guides` has no USING_PLUGINS.md pointer line")
		}
		cmdVerbs := g4DocCommandVerbs(pointer)
		for _, verb := range g4PluginVerbs {
			if !cmdVerbs[verb] {
				t.Errorf("the USING_PLUGINS.md pointer's `af plugin a|b|...` command span does not name %q: %q", verb, pointer)
			}
		}
		if len(cmdVerbs) != len(g4PluginVerbs) {
			t.Errorf("the pointer's command span names %d verb(s) %v, want exactly the %d plugin verbs", len(cmdVerbs), cmdVerbs, len(g4PluginVerbs))
		}
	})
}

// g4DocCommandVerbs parses the first `af plugin a|b|c` code span anywhere in a line, reusing the
// CLAUDE.md bullet parser.
func g4DocCommandVerbs(line string) map[string]bool {
	i := strings.Index(line, "`af plugin")
	if i < 0 {
		return map[string]bool{}
	}
	return g4BulletCommandVerbs("- " + line[i:])
}

// 18 (BODY-6, D20): ADR-025 records the plugin trust boundary, Status Proposed, indexed in README.
func TestADR025PluginTrustBoundary(t *testing.T) {
	adrDir := filepath.Join(findModuleRoot(t), "docs", "architecture", "adrs")
	matches, err := filepath.Glob(filepath.Join(adrDir, "ADR-025-*.md"))
	if err != nil {
		t.Fatalf("glob ADR-025: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one docs/architecture/adrs/ADR-025-*.md (plugin trust boundary), got %d: %v", len(matches), matches)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	body := strings.ToLower(string(raw))
	for _, want := range []string{"store/plugins", "plugins.json", "provenance", "trust", "credential", "owner"} {
		if !strings.Contains(body, want) {
			t.Errorf("ADR-025 missing %q in %s", want, matches[0])
		}
	}
	if !regexp.MustCompile(`fail[- ]closed`).MatchString(body) {
		t.Errorf("ADR-025 must record the fail-closed manifest readers (D4-D7)")
	}
	if !strings.Contains(string(raw), "USING_PLUGINS.md") {
		t.Errorf("ADR-025 must point at the plugin guide USING_PLUGINS.md")
	}
	if strings.Contains(string(raw), "`USING_AGENTFACTORY.md` \"Plugin repositories\"") {
		t.Errorf("ADR-025 still points at USING_AGENTFACTORY.md \"Plugin repositories\", which moved to USING_PLUGINS.md")
	}
	// IMPLREADME_PHASE2 Slice 5 amendment: §1 names the consumed integration tree, §2 the v2 record,
	// and the Consequences replace the "skills unhashed" and "no removal" sentences.
	if !strings.Contains(string(raw), "store/integrations/<name>/<sha>/") {
		t.Error("ADR-025 §1 must name the consumed integration tree `store/integrations/<name>/<sha>/`")
	}
	if !strings.Contains(string(raw), `"version": 2`) {
		t.Error("ADR-025 §2 must say plugins.json carries `\"version\": 2`")
	}
	for _, want := range []string{"[install]", "[check]", "[service]", "af plugin remove"} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("ADR-025 Consequences must record %q (integration execution and removal now exist)", want)
		}
	}
	flat := strings.Join(strings.Fields(string(raw)), " ")
	for _, stale := range []string{
		"Skills are not installed by plugins and stay outside the embed and hash checks",
		"Removal and update-diff (issue #525) are not part of this decision",
	} {
		if strings.Contains(flat, stale) {
			t.Errorf("ADR-025 still says %q; Phase 2 hashes skills and adds `af plugin remove`", stale)
		}
	}
	if !regexp.MustCompile(`(?m)^\*\*status:\*\* proposed`).MatchString(body) {
		t.Errorf("ADR-025 must carry a `**Status:** Proposed` line (D20)")
	}
	readme, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
	if err != nil {
		t.Fatalf("read adrs/README.md: %v", err)
	}
	var row string
	for _, l := range strings.Split(string(readme), "\n") {
		if strings.Contains(l, "](ADR-025-") {
			row = l
			break
		}
	}
	if row == "" {
		t.Fatal("docs/architecture/adrs/README.md Index has no `](ADR-025-` row")
	}
	if !strings.Contains(row, "Proposed") {
		t.Errorf("ADR-025 index row must show status Proposed: %q", row)
	}
}
