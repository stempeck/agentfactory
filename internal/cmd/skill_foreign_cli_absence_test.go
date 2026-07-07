package cmd

import (
	"fmt"
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// skill_foreign_cli_absence_test.go — foreign-CLI/vocabulary absence lint for
// embedded skills (issue #484).
//
// plan-work and plan-review were imported verbatim from the predecessor system
// and shipped with `bd`/`gt` invocations that no factory environment provides,
// breaking gherkin-engineering at story decomposition. Every prior interlock
// stopped at the skill file's front door: ValidateSkills os.Stat's SKILL.md,
// TestEmbeddedFormulaSkillsAvailable fs.Stat's the embedded copy, the af prime
// pre-flight scans only step-bead text, and TestNoDirectBdInFormulas walks only
// install_formulas/. This lint closes the dead zone by reading the CONTENT of
// every embedded skill markdown file, in the mold of
// formula_literal_absence_test.go / formula_dispatcher_coupling_absence_test.go:
// pattern class + allowlist hook + free check function + self-negative test.
//
// The patterns enumerate the foreign command surface (verb lists) rather than
// matching any `bd `/`gt ` token, so legitimate content stays clean: shell
// `-gt 0` comparisons, `gt doctor` in docker-exec examples, and file paths like
// internal/rig/manager.go. Bare "convoy" is deliberately NOT a pattern — it is
// a native formula type (type = "convoy"); only the `gt convoy` CLI form is
// foreign.

// foreignGtInvocationPattern matches the predecessor dispatch/lifecycle CLI,
// replaced by af sling / af agents / af mail / af done. Named separately
// because the formula-scoped test applies ONLY this class.
var foreignGtInvocationPattern = regexp.MustCompile(`\bgt\s+(?:sling|convoy|rig|nudge|polecats?|handoff|done|prime|install|mail|verify|hook|formula|up|down)\b`)

var foreignSkillCLIPatterns = []*regexp.Regexp{
	// bd invocations — the predecessor issue-store CLI, replaced by `af bead`
	regexp.MustCompile(`\bbd\s+(?:create|show|update|list|close|dep|ready|prime|sync|agent|config|formula)\b`),
	foreignGtInvocationPattern,
	// command substitution over either foreign CLI
	regexp.MustCompile(`\$\((?:bd|gt)\s`),
	// predecessor-system vocabulary with no factory meaning
	regexp.MustCompile(`(?i)\btarget_rig\b`),
	regexp.MustCompile(`(?i)\bpolecats?\b`),
	regexp.MustCompile(`(?i)\bmayor\b`),
	regexp.MustCompile(`(?i)\bgas\s?town\b`),
}

// foreignSkillCLIAllowlist suppresses lines that match a pattern above but are
// known-legitimate. bd/gt inside `docker exec … bash -c '…'` (rootcause-review /
// rootcause-implementfix walkthrough examples) run against an example
// container's own toolchain, not the factory PATH.
var foreignSkillCLIAllowlist = []*regexp.Regexp{
	regexp.MustCompile(`\bdocker exec\b`),
}

// checkForeignCLIRefs returns one human-readable violation per line of content
// that references the predecessor system's CLIs or vocabulary. A nil result
// means clean. Free function so the self-negative test can prove the check
// bites without touching real skills.
func checkForeignCLIRefs(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		if foreignCLIAllowlisted(line) {
			continue
		}
		for _, re := range foreignSkillCLIPatterns {
			if m := re.FindString(line); m != "" {
				violations = append(violations,
					fmt.Sprintf("line %d: foreign reference %q in %q", i+1, m, strings.TrimSpace(line)))
				break // one violation per line is enough to fail
			}
		}
	}
	return violations
}

func foreignCLIAllowlisted(line string) bool {
	for _, re := range foreignSkillCLIAllowlist {
		if re.MatchString(line) {
			return true
		}
	}
	return false
}

// TestSkillForeignCLIAbsence fails if any embedded skill file references the
// predecessor system's CLIs or vocabulary. Skills are agent-executable
// instructions: a foreign CLI in a skill breaks the formula that delegates to
// it, at runtime, with no earlier warning (issue #484). Every embedded file is
// scanned (SKILL.md, companion docs, shell scripts) — all of it is shipped
// agent-facing content.
func TestSkillForeignCLIAbsence(t *testing.T) {
	err := fs.WalkDir(skillsFS, "install_skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := skillsFS.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		for _, v := range checkForeignCLIRefs(string(data)) {
			t.Errorf("%s: %s — replace with af-native tooling (af bead / af sling / af agents / af mail; see issue #484)", path, v)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking skillsFS: %v", err)
	}
}

// TestFormulaForeignGtAbsence extends the guard to source formulas for the gt
// class only: bd in formulas is already blocked by TestNoDirectBdInFormulas,
// and vocabulary is skill-scoped because several formulas legitimately carry
// historical prose (and `-gt 0` style shell arithmetic must stay legal, which
// the verb-list pattern guarantees).
func TestFormulaForeignGtAbsence(t *testing.T) {
	gtPattern := foreignGtInvocationPattern
	for _, name := range listFormulas(t, "install_formulas") {
		data, err := formulasFS.ReadFile("install_formulas/" + name)
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		for i, line := range strings.Split(string(data), "\n") {
			if foreignCLIAllowlisted(line) {
				continue
			}
			if m := gtPattern.FindString(line); m != "" {
				t.Errorf("%s:%d: foreign gt command %q in %q", name, i+1, m, strings.TrimSpace(line))
			}
		}
	}
}

// TestSkillForeignCLILintSelfNegative proves the lint is not vacuous: every
// foreign-class fixture MUST be flagged and every known-legitimate residual
// MUST NOT be. If this regresses, a green TestSkillForeignCLIAbsence is
// worthless.
func TestSkillForeignCLILintSelfNegative(t *testing.T) {
	mustFlag := []string{
		"bd create --type=epic --title=\"$EPIC_TITLE\"",
		"bd dep add <story-id> <dep-id>",
		"bd list --status=open",
		"EPIC_ID=$(bd create --type=epic --json | jq -r .id)",
		"gt rig list",
		"gt convoy create \"<epic-name>\" <ids> --notify",
		"gt sling <story-id> <rig>",
		"Default target_rig from gt rig list",
		"Report the plan to the Mayor",
		"Polecats receive work through beads",
		"author: \"Gas Town\"",
		"imported from gastown without adaptation",
	}
	for _, s := range mustFlag {
		if v := checkForeignCLIRefs(s); len(v) == 0 {
			t.Errorf("self-negative bite failed: lint did NOT flag %q (the check is vacuous)", s)
		}
	}

	mustNotFlag := []string{
		"if [ \"$COUNT\" -gt 0 ]; then",
		"type = \"convoy\"",
		"Convoy formulas run legs in parallel",
		"af bead create --type epic --title \"...\"",
		"af sling --agent factoryworker \"implement af-1234\"",
		"docker exec -u dev container_name bash -c 'bd config get issue-prefix'",
		"docker exec -u dev <container> bash -c 'cd ~/gt && gt doctor'",
		"see internal/rig/manager.go:604 for the old callsite",
		"the word debd should not match, nor should mgt sling",
		"git log --grep=\"bd migration\" --oneline",
	}
	for _, s := range mustNotFlag {
		if v := checkForeignCLIRefs(s); len(v) > 0 {
			t.Errorf("false-positive: lint should not flag legitimate content %q, got %v", s, v)
		}
	}
}
