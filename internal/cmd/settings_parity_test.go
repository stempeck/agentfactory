//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
)

// canonicalSettings returns the embedded template bytes for a role type, obtained through
// EnsureSettings (which writes the embedded template VERBATIM), so a byte comparison against a deployed
// file is exact rather than structural.
func canonicalSettings(t *testing.T, roleType claude.RoleType) []byte {
	t.Helper()
	dir := t.TempDir()
	if err := claude.EnsureSettings(dir, roleType); err != nil {
		t.Fatalf("EnsureSettings: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("reading canonical settings: %v", err)
	}
	return data
}

// firstDiffOffset returns the first byte offset at which a and b differ, or -1 if identical.
func firstDiffOffset(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	if len(a) != len(b) {
		return n
	}
	return -1
}

// TestDeployedAgentSettingsMatchEmbeddedTemplate is F2 (r3906601303) guardrail (a): every DEPLOYED
// agent settings.json under .agentfactory/agents/ must be byte-identical to the embedded template for
// its role type. This is the diff that was missing when PR #669's 0/43 Task|Agent and 0/43 SubagentStop
// drift shipped green — the templates were fixed but nothing compared the deployed files against them,
// and make check-regen diffs only internal/templates/roles/ and is not in make test. The check is
// driven by the agents.json roster so a missing deployment is a failure, never a silent skip, and it is
// a GREEN regression LOCK (43/43 identical today) whose negative control proves it has teeth.
func TestDeployedAgentSettingsMatchEmbeddedTemplate(t *testing.T) {
	root := findRepoRoot(t)
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	if agents == nil || len(agents.Agents) == 0 {
		t.Fatal("agents.json holds no agents; the parity walk would be vacuous")
	}

	canon := map[claude.RoleType][]byte{
		claude.Autonomous:  canonicalSettings(t, claude.Autonomous),
		claude.Interactive: canonicalSettings(t, claude.Interactive),
	}

	compared := 0
	for name := range agents.Agents {
		deployedPath := filepath.Join(root, ".agentfactory", "agents", name, ".claude", "settings.json")
		deployed, err := os.ReadFile(deployedPath)
		if err != nil {
			t.Errorf("agent %q is in agents.json but has no deployed settings.json (%v)", name, err)
			continue
		}
		canonical := canon[claude.RoleTypeFor(name, agents)]
		if !bytes.Equal(deployed, canonical) {
			t.Errorf("agent %q settings.json has drifted from its embedded template (first diff at byte %d)",
				name, firstDiffOffset(deployed, canonical))
			continue
		}
		compared++
	}
	if compared < len(agents.Agents) {
		t.Errorf("parity compared %d of %d roster agents; every agent in agents.json must have a matching "+
			"deployed settings.json", compared, len(agents.Agents))
	}

	// Negative control (teeth): the reported F2a drift — Task|Agent narrowed back to Task — must NOT
	// compare equal, proving the byte comparison the loop relies on actually detects that regression.
	auto := canon[claude.Autonomous]
	if !bytes.Contains(auto, []byte("Task|Agent")) {
		t.Fatal("the autonomous template no longer contains the Task|Agent matcher; the negative control is moot")
	}
	drifted := bytes.Replace(auto, []byte("Task|Agent"), []byte("Task"), 1)
	if bytes.Equal(auto, drifted) {
		t.Error("the byte comparison did not detect a Task|Agent -> Task drift; the parity check is toothless")
	}
}
