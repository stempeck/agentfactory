package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// web_design_output_dir_test.go — guards the web-design formula against the
// "{{output_dir}} nested under .agentfactory/ + git add -f" anti-pattern that
// PR #67 review flagged (15 unresolved threads from reviewer stempeck). Design
// artifacts must live at the REPOSITORY ROOT (e.g. <repo>/.designs/web-ui),
// OUTSIDE the protected, git-excluded .agentfactory/ tree, so a plain
// `git add` stages them — never `git add -f` over a bad storage location.
//
// The fix and this guard are coupled: relocating output_dir to the repo root
// is exactly what makes removing `-f` safe (repo-root .designs/ is not covered
// by any .gitignore / .git/info/exclude pattern, whereas .agentfactory/* is).
// The check lints BOTH on-disk copies of the formula (the install_formulas
// source and the installed store mirror) because the review left the same
// comment on both, and `make sync-formulas` keeps them identical.

// webDesignFormulaCopies returns every on-disk copy of the web-design formula
// that must satisfy the output_dir storage policy. Paths are relative to the
// internal/cmd/ package directory (where `go test` runs), matching the sibling
// formula tests in this package.
func webDesignFormulaCopies() []string {
	return []string{
		filepath.Join("install_formulas", "web-design.formula.toml"),
		filepath.Join("..", "..", ".agentfactory", "store", "formulas", "web-design.formula.toml"),
	}
}

// checkOutputDirStoragePolicy returns one human-readable violation per line that
// breaks the output_dir storage policy. A nil result means clean. It is a free
// function (not inlined into the test) so TestOutputDirStoragePolicySelfNegative
// can prove the check actually bites without touching the real formula.
//
// Two invariants, both encoding a reviewer ask from PR #67:
//   (1) No `git add -f` — the bad process step layered over a bad storage
//       decision. With artifacts at the repo root, a plain `git add` works.
//   (2) No line that both names `.agentfactory` AND `git-exclude` — that is the
//       rationale excusing output_dir's nesting under the protected tree; it
//       must not exist once output_dir lives at the repo root.
func checkOutputDirStoragePolicy(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		ln := i + 1
		if strings.Contains(line, "git add -f") {
			violations = append(violations, fmt.Sprintf(
				"line %d: `git add -f` is forbidden — store design artifacts at the repo root (NOT under .agentfactory/) so a plain `git add` stages them: %q",
				ln, strings.TrimSpace(line)))
			continue
		}
		if strings.Contains(line, ".agentfactory") && strings.Contains(line, "git-exclude") {
			violations = append(violations, fmt.Sprintf(
				"line %d: output_dir must NOT be nested under .agentfactory/ (a protected, git-excluded location) — anchor it at the repo root: %q",
				ln, strings.TrimSpace(line)))
			continue
		}
	}
	return violations
}

// TestWebDesignOutputDirNotUnderAgentfactory fails if either copy of the
// web-design formula reintroduces the git-add-f-over-.agentfactory anti-pattern.
func TestWebDesignOutputDirNotUnderAgentfactory(t *testing.T) {
	for _, path := range webDesignFormulaCopies() {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, v := range checkOutputDirStoragePolicy(string(data)) {
			t.Errorf("%s: %s", path, v)
		}
	}
}

// TestOutputDirStoragePolicySelfNegative proves the policy check is not vacuous:
// every forbidden construct MUST be flagged, and every legitimate replacement
// MUST stay clean. If this regresses, the green test above is worthless.
func TestOutputDirStoragePolicySelfNegative(t *testing.T) {
	mustFlag := []string{
		"   git add -f {{output_dir}}/",
		"   git add -f {{output_dir}}/final/design-contract.yaml",
		"   git add -f {{output_dir}}/   # -f: {{output_dir}} may sit under a .agentfactory/* git-exclude; a plain 'git add' would silently stage nothing",
		"  agent worktree it is often covered by a `.agentfactory/*` git-exclude, so a plain",
	}
	for _, s := range mustFlag {
		if v := checkOutputDirStoragePolicy(s); len(v) == 0 {
			t.Errorf("self-negative bite failed: policy check did NOT flag %q (the check is vacuous)", s)
		}
	}

	mustNotFlag := []string{
		"   git add {{output_dir}}/",
		`   ROOT=$(git rev-parse --show-toplevel)`,
		"Write the Problem Brief to `{{output_dir}}/problem-brief.md`",
		"- **Artifacts live at the repo root, never under .agentfactory/:** compute",
	}
	for _, s := range mustNotFlag {
		if v := checkOutputDirStoragePolicy(s); len(v) > 0 {
			t.Errorf("false-positive: policy check should not flag %q, got %v", s, v)
		}
	}
}
