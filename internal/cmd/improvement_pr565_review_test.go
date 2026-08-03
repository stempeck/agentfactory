package cmd

import (
	"os"
	"strings"
	"testing"
)

// The PR #565 review found two surfaces this fix falsified but did not update: the cobra
// help string and the troubleshooting section of the guide. Both are mechanical string
// properties, so both get mechanical guards — mirroring the PR #516 review's own doc guard
// in improvement_pr516_review_test.go, the local idiom for exactly this.
//
// The relative rendering is the defect issue #563 exists to eliminate: a dispatched agent's
// cwd is a git worktree, and .agentfactory/store/formulas/ is git-tracked, so a relative
// designation resolves against the worktree duplicate rather than the factory root.

// relativeStoreFormulaTarget is the bare path fragment, matched independently of the
// surrounding punctuation: the guide wraps it in backticks, the cobra help in parentheses.
const relativeStoreFormulaTarget = ".agentfactory/store/formulas/<agent>.formula.toml"

// unwrap collapses newlines and repeated spaces so a guard pins the SENTENCE rather than a
// particular line wrapping — the help text is a hard-wrapped raw string literal, so an
// unnormalized match silently passes whenever a phrase happens to straddle a line break.
func unwrap(s string) string { return strings.Join(strings.Fields(s), " ") }

// TestUsingAgentfactoryDoc_NoRelativeStoreFormulaTarget guards the third documentation site
// (USING_AGENTFACTORY.md:752, the troubleshooting section read precisely when the hook
// misbehaves). An operator checking that precondition from a worktree cwd inspects the
// git-materialised duplicate, which always exists, and concludes the precondition is met
// while improvementInstruction stats the factory-root path. (F-1, merged with F-10.)
func TestUsingAgentfactoryDoc_NoRelativeStoreFormulaTarget(t *testing.T) {
	data, err := os.ReadFile("../../USING_AGENTFACTORY.md")
	if err != nil {
		t.Fatalf("reading USING_AGENTFACTORY.md: %v", err)
	}
	// The absolute rendering embeds the same fragment after a <factory-root>/ prefix, so
	// only an occurrence NOT preceded by that prefix is a relative designation.
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, relativeStoreFormulaTarget) {
			continue
		}
		if strings.Contains(line, "<factory-root>/"+relativeStoreFormulaTarget) {
			continue
		}
		t.Errorf("USING_AGENTFACTORY.md still designates the improvement target relatively as %q; every surface must name the absolute <factory-root>/ path (issue #563, AC7). Line:\n%s", relativeStoreFormulaTarget, strings.TrimSpace(line))
	}
}

// TestImprovementCmd_HelpMatchesPostFixReality guards the CLI's own operator-facing help —
// for AC7 a more authoritative surface than the prose docs. Two statements this fix
// falsified: the relative edit target, and the claim that a single value is substituted
// into the instruction (there are two since #563). The adjacent code comment at
// improvement.go:185-191 was updated correctly; the help string was missed. (F-12.)
func TestImprovementCmd_HelpMatchesPostFixReality(t *testing.T) {
	help := unwrap(improvementCmd.Long)

	if strings.Contains(help, relativeStoreFormulaTarget) &&
		!strings.Contains(help, "<factory-root>/"+relativeStoreFormulaTarget) {
		t.Errorf("af improvement --help still names the relative edit target %q; it must name the absolute factory-root path (issue #563, AC7)", relativeStoreFormulaTarget)
	}
	if strings.Contains(help, "only the finishing formula's own name is substituted") {
		t.Errorf("af improvement --help still claims only one value is substituted into the instruction; two are (the absolute edit target and the bare formula name), per improvement.go:185-191")
	}
}
