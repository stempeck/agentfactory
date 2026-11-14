package cmd

import (
	"strings"
	"testing"
)

// TestUsingAgentfactoryDoc_OptInContractByteExact protects the improvement-hook opt-in contract
// line (intake.md DO-NOT-CHANGE).
func TestUsingAgentfactoryDoc_OptInContractByteExact(t *testing.T) {
	content := readUsingAgentfactoryDoc(t)
	const want = "Unlike the fidelity gate, `.improvement-hook` is **never** seeded by `af install --init` " +
		"— absent means off, so the whole capability stays inert until an operator explicitly enables " +
		"both sides. `af improvement` (no args) prints the factory line, a per-agent effective (AND) " +
		"table, and any pending sessions."
	if !strings.Contains(content, want) {
		t.Errorf("USING_AGENTFACTORY.md's improvement-hook opt-in contract line is missing or was " +
			"edited; it must stay byte-exact (DO-NOT-CHANGE)")
	}
}

// TestUsingAgentfactoryDoc_ExistingSourceRootMentionUntouched protects the pre-existing
// AF_SOURCE_ROOT mention (agent-gen --af-src template-write resolution) from being edited
// (intake.md DO-NOT-CHANGE).
func TestUsingAgentfactoryDoc_ExistingSourceRootMentionUntouched(t *testing.T) {
	content := readUsingAgentfactoryDoc(t)
	const want = "`--af-src` overrides where the template `.md.tmpl` file is written. Resolution " +
		"chain: `--af-src` flag > `AF_SOURCE_ROOT` environment variable > compiled source root > " +
		"factory root fallback."
	if !strings.Contains(content, want) {
		t.Error("USING_AGENTFACTORY.md's existing agent-gen --af-src / AF_SOURCE_ROOT sentence is " +
			"missing or was edited; it must stay untouched")
	}
}
