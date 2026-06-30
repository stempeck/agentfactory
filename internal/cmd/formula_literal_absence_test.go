package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// formula_literal_absence_test.go — K7/K8 literal-absence lint (af-13234830 Phase 3,
// six_sigma_gaps Gap 1, peer-review-broadened).
//
// Phases 1 and 2 made every formula's git ops default-branch-agnostic: detection
// injects an ambient {{default_branch}} token, and the source formulas were rolled
// over to use it. This lint is the mechanical interlock that keeps them that way —
// it fails CI if any *executable* branch literal (a hardcoded `main` used as a git
// ref argument) is re-introduced into a source formula, whether by editing an
// existing formula or adding a brand-new one. Without it, a future
// `git rebase origin/main` or `gh pr create --base main` could silently merge and
// break every agent generated from that formula on a non-`main` (e.g. `master`) repo.
//
// It lints the EXECUTABLE class ONLY — never the bare word `main`. Prose/comment
// mentions of "main" that legitimately remain by design (six_sigma_gaps Gap 4:
// "Tests pass on main", "fast-forward from main", trailing `# … on main` comments,
// the Swift `@main` attribute, and the false-positives domain/remain/maintain/
// "main context") must NOT trip it. Two design constraints, both verified against
// the current tree (0 hits):
//   - The `git <verb> … main` pattern uses [^#\n], so it stops at a trailing shell
//     comment — `git diff origin/{{default_branch}}...HEAD  # All changes vs main`
//     does NOT flag (the `main` is inside the comment, not executed).
//   - The refspec patterns are anchored to a *glued* ref token, so prose colons like
//     `**4. Sync with main:**` or `… exists on main:` do NOT flag — only real
//     refspecs (`HEAD:main`, `main:feature`) do.

// executableBranchLiteralPatterns is the broadened executable-branch-literal class.
var executableBranchLiteralPatterns = []*regexp.Regexp{
	// remote-tracking ref to the literal default branch
	regexp.MustCompile(`\borigin/main\b`),
	// a git subcommand consuming `main` as a ref argument (comment-stripped via [^#\n])
	regexp.MustCompile(`\bgit\s+(?:checkout|switch|merge|rebase|reset|pull|fetch|push|log|diff|show|branch|rev-parse|cherry-pick|merge-base|range-diff|worktree)\b[^#\n]*\bmain\b`),
	// range refs: main...<x> or <x>...main
	regexp.MustCompile(`\bmain\.\.\.`),
	regexp.MustCompile(`\.\.\.main\b`),
	// PR base/head selectors (gh pr create --base main / --head main)
	regexp.MustCompile(`--base[ =]main\b`),
	regexp.MustCompile(`--head[ =]main\b`),
	// `main` filtered out of piped git output (e.g. `… | grep main`)
	regexp.MustCompile(`\|\s*grep[^#\n]*\bmain\b`),
	// glued refspecs only — anchored to a ref token so prose colons don't match
	regexp.MustCompile(`[\w./~^-]+:main\b`),
	regexp.MustCompile(`\bmain:[\w./~^-]+`),
	// literal @cli recipient — the unroutable mail-synthesis sentinel #321 removed;
	// it must never be reintroduced at the formula layer (constraint C-4). `\b` so it
	// does not over-match tokens like @client (mirrors the @main allowlist care —
	// see the "   @main" mustNotFlag fixture).
	regexp.MustCompile(`@cli\b`),
}

// branchLiteralAllowlist suppresses lines that match an executable pattern above but
// are known-legitimate exceptions. It is empty on today's tree: the executable
// patterns are precise enough that nothing currently trips them (verified). The hook
// exists so a future *deliberate* exception (an example command that must stay
// literal `main`) can be allowlisted by exact line without weakening the class for
// every other formula. Enumerate additions here from the Phase 2 false-positive
// catalog, never by broadening the patterns.
var branchLiteralAllowlist = []*regexp.Regexp{
	// (intentionally empty — see comment)
}

// checkExecutableBranchLiterals returns one human-readable violation per line of
// content that contains an executable branch literal not covered by the allowlist.
// A nil result means clean. Pulled out as a free function so the self-negative test
// can prove the check bites without touching real formulas.
func checkExecutableBranchLiterals(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		if branchLiteralAllowlisted(line) {
			continue
		}
		for _, re := range executableBranchLiteralPatterns {
			if m := re.FindString(line); m != "" {
				violations = append(violations,
					fmt.Sprintf("line %d: executable branch literal %q in %q", i+1, m, strings.TrimSpace(line)))
				break // one violation per line is enough to fail
			}
		}
	}
	return violations
}

func branchLiteralAllowlisted(line string) bool {
	for _, re := range branchLiteralAllowlist {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// TestFormulaLiteralAbsence fails if any source formula re-introduces an executable
// branch literal. It passes on today's (post-Phase-2) tree; its value is forward —
// blocking a future regression or a new formula that hardcodes `main`.
func TestFormulaLiteralAbsence(t *testing.T) {
	const sourceDir = "install_formulas"
	for _, name := range listFormulas(t, sourceDir) {
		data, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, v := range checkExecutableBranchLiterals(string(data)) {
			t.Errorf("%s: %s — replace the hardcoded branch with the {{default_branch}} token (af-13234830)", name, v)
		}
	}
}

// TestBranchLiteralLintSelfNegative proves the lint is NOT vacuous (AC #2): every
// broadened-class fixture MUST be flagged, and every known-legitimate residual MUST
// NOT be. If this regresses, the green TestFormulaLiteralAbsence above is worthless.
func TestBranchLiteralLintSelfNegative(t *testing.T) {
	// Each of these is a deliberately re-introduced executable literal spanning the
	// broadened class. The lint must bite on every one.
	mustFlag := []string{
		"git push origin main",
		"git rebase origin/main",
		"gh pr create --base main",
		"git checkout main",
		"git log main",
		"git diff main...feature",
		"git diff feature...main",
		"git push origin HEAD:main",
		"git push origin main:feature",
		"git branch --contains $sha | grep main",
		"gh pr create --base develop --head main",
		// #321 Phase 4 (C-4): a literal @cli mail recipient must never be
		// reintroduced at the formula layer — the lint must bite on it too.
		"af mail send @cli -s WORK_DONE",
	}
	for _, s := range mustFlag {
		if v := checkExecutableBranchLiterals(s); len(v) == 0 {
			t.Errorf("self-negative bite failed: lint did NOT flag %q (the check is vacuous)", s)
		}
	}

	// Legitimate residual prose / comments / false-positives that MUST stay clean,
	// or the lint false-fails on today's tree (six_sigma_gaps Gap 4).
	mustNotFlag := []string{
		"**4. Sync with main:**",
		"**1. Check tests on main:**",
		"# Should be empty if content is on main",
		"git diff origin/{{default_branch}}...HEAD     # All changes vs main",
		"2. Rebase on current main: git rebase origin/{{default_branch}}",
		`git log origin/{{default_branch}} --oneline | grep "<issue>"`,
		"MAIN_SHA=$(git rev-parse origin/{{default_branch}})",
		"the domain remains; we maintain main context here",
		"   @main",
		"still a fast-forward from main, so merge-push works",
		"verify the work is actually on main",
	}
	for _, s := range mustNotFlag {
		if v := checkExecutableBranchLiterals(s); len(v) > 0 {
			t.Errorf("false-positive: lint should not flag legitimate prose %q, got %v", s, v)
		}
	}
}
