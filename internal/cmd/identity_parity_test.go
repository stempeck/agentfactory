//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/templates"
)

// deployedFactoryRootRe extracts the absolute factory root baked into a deployed CLAUDE.md. The
// corpus was rendered on whatever host provisioned each agent, so the two absolute paths in the
// Workspace block are the ONLY legitimate reason two otherwise-identical identity files differ.
// Re-rendering the canonical form with the file's own root normalizes them away, leaving a byte
// comparison that answers the question that matters: is the TEXT the same?
var deployedFactoryRootRe = regexp.MustCompile("(?m)^- \\*\\*Factory root\\*\\*: `([^`]+)`$")

// deployedWorkDirRe extracts the absolute working directory baked into a deployed CLAUDE.md. It is
// the SECOND host-specific absolute path in the Workspace block: in a launched worktree the working
// directory points at the worktree agent dir and need not equal the factory-root-derived dir, so it
// is a legitimate second reason two otherwise-identical identity files differ and must be normalized
// away alongside the factory root (#681 T4).
var deployedWorkDirRe = regexp.MustCompile("(?m)^- \\*\\*Working directory\\*\\*: `([^`]+)`$")

func deployedFactoryRoot(deployed []byte) string {
	m := deployedFactoryRootRe.FindSubmatch(deployed)
	if m == nil {
		return ""
	}
	return string(m[1])
}

func deployedWorkDir(deployed []byte) string {
	m := deployedWorkDirRe.FindSubmatch(deployed)
	if m == nil {
		return ""
	}
	return string(m[1])
}

// canonicalIdentity renders the embedded template for an agent exactly as every provisioning site
// does, against the supplied factory root and working directory. Rendering with the deployed file's
// OWN working directory (rather than one derived from the factory root) normalizes the two absolute
// path lines away, leaving a byte comparison that answers the question that matters: is the TEXT the
// same? — which is what lets the walk stay green inside a launched worktree (#681 T4).
func canonicalIdentity(t *testing.T, tmpl *templates.Templates, name string, entry config.AgentEntry, factoryRoot, workDir string) []byte {
	t.Helper()
	content, err := templates.RenderIdentity(tmpl, name, entry, factoryRoot, workDir)
	if err != nil {
		t.Fatalf("RenderIdentity(%s): %v", name, err)
	}
	return content
}

// TestDeployedAgentIdentityMatchesEmbeddedTemplate is the identity half of the parity lock that
// TestDeployedAgentSettingsMatchEmbeddedTemplate already provides for settings.json. Nothing in the
// suite has ever compared a deployed CLAUDE.md to anything: make check-regen diffs only
// internal/templates/roles/ and is not part of make test, so the identity file every surviving
// context carrier depends on (the harness's own CLAUDE.md load, and every tool-result af prime)
// could rot indefinitely. Driven by the agents.json roster, so a missing deployment is a failure
// rather than a silent skip.
func TestDeployedAgentIdentityMatchesEmbeddedTemplate(t *testing.T) {
	root := findRepoRoot(t)
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	if agents == nil || len(agents.Agents) == 0 {
		t.Fatal("agents.json holds no agents; the parity walk would be vacuous")
	}

	tmpl := templates.New()
	compared := 0
	for name, entry := range agents.Agents {
		deployedPath := filepath.Join(root, ".agentfactory", "agents", name, "CLAUDE.md")
		deployed, err := os.ReadFile(deployedPath)
		if err != nil {
			t.Errorf("agent %q is in agents.json but has no deployed CLAUDE.md (%v)", name, err)
			continue
		}
		deployedRoot := deployedFactoryRoot(deployed)
		if deployedRoot == "" {
			t.Errorf("agent %q CLAUDE.md carries no `- **Factory root**:` line; path normalization is impossible", name)
			continue
		}
		deployedWD := deployedWorkDir(deployed)
		if deployedWD == "" {
			t.Errorf("agent %q CLAUDE.md carries no `- **Working directory**:` line; path normalization is impossible", name)
			continue
		}
		canonical := canonicalIdentity(t, tmpl, name, entry, deployedRoot, deployedWD)
		if !bytes.Equal(deployed, canonical) {
			t.Errorf("agent %q CLAUDE.md has drifted from its embedded template (deployed %d bytes, canonical %d bytes, first diff at byte %d)",
				name, len(deployed), len(canonical), firstDiffOffset(deployed, canonical))
			continue
		}
		compared++
	}
	if compared < len(agents.Agents) {
		t.Errorf("parity compared %d of %d roster agents; every agent in agents.json must have a matching "+
			"deployed CLAUDE.md", compared, len(agents.Agents))
	}
}

// TestDeployedAgentIdentityParityHasTeeth is the negative control for the walk above: the byte
// comparison it relies on must actually detect a corrupted render.
func TestDeployedAgentIdentityParityHasTeeth(t *testing.T) {
	root := findRepoRoot(t)
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	tmpl := templates.New()

	const probe = "rootcause-all"
	entry, ok := agents.Agents[probe]
	if !ok {
		t.Skipf("roster no longer carries %q; the negative control needs a formula agent", probe)
	}
	canonical := canonicalIdentity(t, tmpl, probe, entry, "/factory", filepath.Join("/factory", ".agentfactory", "agents", probe))
	if !bytes.Contains(canonical, []byte("## Workspace")) {
		t.Fatal("the canonical render has no `## Workspace` heading; the negative control is moot")
	}
	corrupted := bytes.Replace(canonical, []byte("## Workspace"), []byte("## Workspaces"), 1)
	if bytes.Equal(canonical, corrupted) {
		t.Error("the byte comparison did not detect a corrupted render; the identity parity check is toothless")
	}
}

// TestParity_NormalizesDivergentWorkDir (T4-b) pins the requirement T4 adds: the parity walk must
// normalize BOTH host-specific absolute lines (Factory root AND Working directory), not just the
// Factory-root line. A launched worktree deploys a CLAUDE.md whose `- **Working directory**:` line
// points at the worktree agent dir, which need not equal the factory-root-derived dir the walk
// re-renders — so the two lines legitimately diverge and the byte comparison should still report a
// match once both are normalized away.
//
// The normalization recipe re-renders with BOTH the deployed file's own factory root
// (deployedFactoryRoot) AND its own working directory (deployedWorkDir), mirroring
// TestDeployedAgentIdentityMatchesEmbeddedTemplate. Rendering against the deployed file's OWN
// work-dir is what makes a divergent worktree work-dir line normalize away; at head, where the
// recipe derived the work-dir from the factory root, that divergent line failed the compare — the
// T4 report this now closes.
func TestParity_NormalizesDivergentWorkDir(t *testing.T) {
	const name = "t4581113-probe"
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	tmpl := templates.New()
	const root = "/factory"

	derivedWD := filepath.Join(root, ".agentfactory", "agents", name)
	divergentWD := filepath.Join(root, ".agentfactory", "worktrees", "wt-x", ".agentfactory", "agents", name)
	canonical := canonicalIdentity(t, tmpl, name, entry, root, derivedWD)

	deployed := bytes.Replace(canonical, []byte("`"+derivedWD+"`"), []byte("`"+divergentWD+"`"), 1)
	if bytes.Equal(deployed, canonical) {
		t.Fatalf("setup: the Working-directory line was not rewritten; expected to replace %q", derivedWD)
	}

	// The walk's current normalization recipe: extract the Factory root, re-render canonically.
	deployedRoot := deployedFactoryRoot(deployed)
	if deployedRoot == "" {
		t.Fatal("synthetic deployed file carries no `- **Factory root**:` line")
	}
	recanonical := canonicalIdentity(t, tmpl, name, entry, deployedRoot, deployedWorkDir(deployed))

	if !bytes.Equal(deployed, recanonical) {
		t.Errorf("parity must normalize the Working-directory line too: a worktree-deployed CLAUDE.md whose "+
			"work-dir line diverges from the factory-root-derived dir should still match after normalization "+
			"(first diff at byte %d)", firstDiffOffset(deployed, recanonical))
	}
}
