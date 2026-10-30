package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var integrationLandingRepos = []string{
	"https://github.com/aws/agent-toolkit-for-aws",
	"https://github.com/CiscoDevNet/foundry-security-spec",
	"https://github.com/gastownhall/gastown",
	"https://github.com/smtg-ai/claude-squad",
}

func TestUsingIntegrationsDoc_Guide(t *testing.T) {
	guide := g4ReadRepoDoc(t, "USING_INTEGRATIONS.md")
	for _, verb := range g4PluginVerbs {
		if !strings.Contains(guide, "af plugin "+verb) {
			t.Errorf("USING_INTEGRATIONS.md does not show `af plugin %s`", verb)
		}
	}
	for _, want := range append([]string{
		"integrations_optional",
		"--factory-wide",
		"claude plugin uninstall playwright@claude-plugins-official --scope user",
	}, integrationLandingRepos...) {
		if !strings.Contains(guide, want) {
			t.Errorf("USING_INTEGRATIONS.md does not mention %q", want)
		}
	}
}

func TestUsingIntegrationsDoc_FeatureGuidePointer(t *testing.T) {
	doc := g4ReadRepoDoc(t, "USING_AGENTFACTORY.md")
	if n := strings.Count(doc, "(USING_INTEGRATIONS.md)"); n != 1 {
		t.Errorf("USING_AGENTFACTORY.md links USING_INTEGRATIONS.md %d times, want exactly once", n)
	}
	if _, ok := g4MarkdownSection(doc, "## Integrations"); ok {
		t.Error("USING_AGENTFACTORY.md has a `## Integrations` section; integrations are a Feature Guides pointer")
	}
}

func TestUsingIntegrationsDoc_LandingTable(t *testing.T) {
	guide := g4ReadRepoDoc(t, "USING_INTEGRATIONS.md")
	rows := map[string]int{}
	for _, line := range strings.Split(guide, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "|") {
			continue
		}
		for _, repo := range integrationLandingRepos {
			if strings.Contains(line, repo) {
				rows[repo]++
			}
		}
	}
	for _, repo := range integrationLandingRepos {
		if rows[repo] != 1 {
			t.Errorf("the landing table has %d rows for %s, want 1", rows[repo], repo)
		}
	}
}

var integrationsDocCitation = regexp.MustCompile("`([A-Za-z0-9_./-]+\\.(?:go|sh|md|toml|json)):([0-9]+)(?:-([0-9]+))?`")

// TestUsingIntegrationsDoc_CitationsResolve: every `file:line` the guide cites names a repo file long
// enough to hold that line, so a moved anchor fails here instead of misleading an operator.
func TestUsingIntegrationsDoc_CitationsResolve(t *testing.T) {
	root := findModuleRoot(t)
	guide := g4ReadRepoDoc(t, "USING_INTEGRATIONS.md")
	for _, m := range integrationsDocCitation.FindAllStringSubmatch(guide, -1) {
		b, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(m[1])))
		if err != nil {
			t.Errorf("USING_INTEGRATIONS.md cites %s, which does not exist", m[0])
			continue
		}
		last := m[2]
		if m[3] != "" {
			last = m[3]
		}
		n, _ := strconv.Atoi(last)
		if lines := strings.Count(string(b), "\n"); n > lines {
			t.Errorf("USING_INTEGRATIONS.md cites %s, but the file has %d lines", m[0], lines)
		}
	}
}

// TestUsingIntegrationsDoc_LinksOnlyShippedFiles: a relative link in a published guide must land on a
// file in this repo; the proof script and design notes are named as code, never linked.
func TestUsingIntegrationsDoc_LinksOnlyShippedFiles(t *testing.T) {
	root := findModuleRoot(t)
	guide := g4ReadRepoDoc(t, "USING_INTEGRATIONS.md")
	for _, m := range regexp.MustCompile(`\]\(([^)#]+)(?:#[^)]*)?\)`).FindAllStringSubmatch(guide, -1) {
		target := m[1]
		if strings.HasPrefix(target, "https://") {
			continue
		}
		if strings.HasPrefix(target, ".designs/") || strings.HasPrefix(target, "todos/") || strings.HasPrefix(target, "http://") {
			t.Errorf("USING_INTEGRATIONS.md links %s, which the published repo does not carry", target)
			continue
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(target))); err != nil {
			t.Errorf("USING_INTEGRATIONS.md links %s, which does not exist", target)
		}
	}
}
