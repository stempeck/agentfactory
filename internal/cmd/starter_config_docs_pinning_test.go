package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestADR028_RecordsConfigSeedSource pins that the starter-config seed decision gets its own
// ADR, Proposed and indexed.
func TestADR028_RecordsConfigSeedSource(t *testing.T) {
	adrDir := filepath.Join(findModuleRoot(t), "docs", "architecture", "adrs")
	matches, err := filepath.Glob(filepath.Join(adrDir, "ADR-028-*.md"))
	if err != nil {
		t.Fatalf("glob ADR-028: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("want exactly one docs/architecture/adrs/ADR-028-*.md (the starter-config seed ADR), got %d: %v", len(matches), matches)
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read %s: %v", matches[0], err)
	}
	body := strings.ToLower(string(raw))
	for _, want := range []string{"af_source_root", "af install --agents"} {
		if !strings.Contains(body, want) {
			t.Errorf("ADR-028 does not name %q (%s)", want, matches[0])
		}
	}
	if !strings.Contains(body, "**status:** proposed") && !strings.Contains(body, "status: proposed") {
		t.Errorf("ADR-028 must carry a Proposed status line (no corpus file cites it yet)")
	}
	// docs/architecture ships to the OSS repository and .designs/ does not, so a design path is
	// only honest as text, said to be text (ADR-027's convention).
	if strings.Contains(body, ".designs/") && !strings.Contains(body, "does not carry `.designs/`") {
		t.Errorf("ADR-028 cites .designs/ paths without saying the OSS repository does not carry `.designs/` (%s)", matches[0])
	}

	readme, err := os.ReadFile(filepath.Join(adrDir, "README.md"))
	if err != nil {
		t.Fatalf("read adrs/README.md: %v", err)
	}
	var row string
	for _, l := range strings.Split(string(readme), "\n") {
		if strings.Contains(l, "](ADR-028-") {
			row = l
			break
		}
	}
	if row == "" {
		t.Fatal("docs/architecture/adrs/README.md Index must list ADR-028 (a `](ADR-028-...)` link row)")
	}
	if !strings.Contains(strings.ToLower(row), "proposed") {
		t.Errorf("the README index row for ADR-028 does not carry the Proposed status:\n%s", row)
	}
}

// TestGitignore_TrackedConfigsAllowlisted pins that the three tracked seed configs are
// allowlisted at the merge-clean spot, and O-3 no longer calls them ignored-tier.
func TestGitignore_TrackedConfigsAllowlisted(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join(findModuleRoot(t), ".gitignore"))
	if err != nil {
		t.Fatalf("read .gitignore: %v", err)
	}
	lines := strings.Split(string(raw), "\n")

	anchor := -1
	for i, l := range lines {
		if strings.TrimSpace(l) == "!.agentfactory/factory.json" {
			anchor = i
			break
		}
	}
	if anchor < 0 {
		t.Fatal(".gitignore has no `!.agentfactory/factory.json` line to anchor the allowlist")
	}
	want := map[string]bool{
		"!.agentfactory/models.json":  true,
		"!.agentfactory/startup.json": true,
		"!.agentfactory/litellm.yaml": true,
	}
	var next []string
	for _, l := range lines[anchor+1 : min(anchor+1+len(want), len(lines))] {
		next = append(next, strings.TrimSpace(l))
	}
	got := map[string]bool{}
	for _, l := range next {
		got[l] = true
	}
	for w := range want {
		if !got[w] {
			t.Errorf(".gitignore: %q is not among the %d lines directly after `!.agentfactory/factory.json` (got %q)", w, len(want), next)
		}
	}

	var o3 []string
	for i, l := range lines {
		if !strings.Contains(l, "Decision O-3") {
			continue
		}
		for _, c := range lines[i:] {
			if !strings.HasPrefix(c, "#") {
				break
			}
			o3 = append(o3, c)
		}
		break
	}
	if len(o3) == 0 {
		t.Fatal(".gitignore has no `Decision O-3` comment")
	}
	comment := strings.Join(o3, "\n")
	for _, stale := range []string{"models.json", "litellm.yaml"} {
		if strings.Contains(comment, stale) {
			t.Errorf(".gitignore O-3 comment still lists %s as ignored-tier:\n%s", stale, comment)
		}
	}
}
