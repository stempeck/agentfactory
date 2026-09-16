package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestUsingAgentfactoryDoc_Crons pins the operator-facing documentation of the dispatcher's
// recurring scheduled slings: the section must carry a copy-paste JSON example declaring
// `crons`, name the bare re-sling shape, and sit under the dispatch.json subsection. The
// example's cross-file validity is proven separately by the two example tests below.
func TestUsingAgentfactoryDoc_Crons(t *testing.T) {
	content := readUsingAgentfactoryDoc(t)
	section := cronsDocSection(t, content)

	for _, want := range []string{
		"```json",
		`"crons"`,
		"--bare",
	} {
		if !strings.Contains(section, want) {
			t.Errorf("the `#### Crons` section of USING_AGENTFACTORY.md is missing %q", want)
		}
	}

	// The section documents a dispatch.json key, so it must live inside the dispatch section,
	// between the sibling workflows subsection and the next top-level heading.
	workflows := strings.Index(content, "\n#### Workflows")
	crons := strings.Index(content, "\n#### Crons")
	next := strings.Index(content, "\n### Adding more agents")
	if !(workflows < crons && crons < next) {
		t.Errorf("`#### Crons` must sit between `#### Workflows` and `### Adding more agents` "+
			"(offsets: workflows=%d crons=%d next=%d)", workflows, crons, next)
	}
}

// TestUsingAgentfactoryDoc_CronsExampleIsValid loads the documented dispatch.json through the
// real validators, so we never publish a config an operator copies that fails to load. The
// cross-file checks (agent existence, formula-bearing, var satisfiability) need an agents.json
// and real formulas and stay out of scope here.
func TestUsingAgentfactoryDoc_CronsExampleIsValid(t *testing.T) {
	cfg := loadCronsDocExample(t)

	if len(cfg.Crons) == 0 {
		t.Errorf("the documented example parsed but declares no crons; it must show the feature it documents")
	}
}

// loadCronsDocExample parses the documented dispatch.json through the real loader, so the
// example is proven against the validators an operator hits rather than against a copy.
func loadCronsDocExample(t *testing.T) *config.DispatchConfig {
	t.Helper()
	example := cronsDocJSONExample(t, cronsDocSection(t, readUsingAgentfactoryDoc(t)))

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Dir(config.DispatchConfigPath(root)), 0o755); err != nil {
		t.Fatalf("creating factory dir: %v", err)
	}
	if err := os.WriteFile(config.DispatchConfigPath(root), []byte(example), 0o644); err != nil {
		t.Fatalf("writing dispatch.json: %v", err)
	}
	cfg, err := config.LoadDispatchConfig(root)
	if err != nil {
		t.Fatalf("the documented dispatch.json example is rejected by the validators an operator "+
			"would hit on first paste: %v\n%s", err, example)
	}
	return cfg
}

func readUsingAgentfactoryDoc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "USING_AGENTFACTORY.md"))
	if err != nil {
		t.Fatalf("reading USING_AGENTFACTORY.md: %v", err)
	}
	return string(data)
}

// cronsDocSection returns the `#### Crons` section body — its heading through the line before
// the next ATX heading. Fenced code blocks are tracked so a `#` inside the example cannot end
// the section early.
func cronsDocSection(t *testing.T, content string) string {
	t.Helper()
	lines := strings.Split(content, "\n")

	start := -1
	for i, line := range lines {
		if !strings.HasPrefix(line, "#### Crons") {
			continue
		}
		if start >= 0 {
			t.Fatalf("USING_AGENTFACTORY.md has duplicate `#### Crons` headings (lines %d and %d)", start+1, i+1)
		}
		start = i
	}
	if start < 0 {
		t.Fatal("USING_AGENTFACTORY.md has no `#### Crons` section: the dispatcher's recurring " +
			"scheduled slings are undocumented")
	}

	inFence := false
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			inFence = !inFence
			continue
		}
		if !inFence && isATXHeading(lines[i]) {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// cronsDocJSONExample returns the body of the first ```json fence in the crons section.
func cronsDocJSONExample(t *testing.T, section string) string {
	t.Helper()
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != "```json" {
			continue
		}
		for j := i + 1; j < len(lines); j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "```") {
				return strings.Join(lines[i+1:j], "\n")
			}
		}
		t.Fatal("the crons JSON example has no closing fence")
	}
	t.Fatal("the `#### Crons` section carries no ```json example")
	return ""
}

func isATXHeading(line string) bool {
	hashes := len(line) - len(strings.TrimLeft(line, "#"))
	return hashes >= 1 && hashes <= 6 && strings.HasPrefix(line[hashes:], " ")
}
