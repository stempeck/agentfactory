package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// ADR-014 forbids interactive prompting anywhere in af Go code (C-4). Consent is
// the verb itself (`af plugin install`), never a (y/N) prompt. This is a mechanical
// content-lint over the plugin verb and integration sources (NOT embedded content), modeled on
// skill_foreign_cli_absence_test.go: a pattern class + a free check fn + a
// self-negative test proving the lint is not vacuous.
var adr014ForbiddenPromptPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\(y/N\)`),
	regexp.MustCompile(`\(Y/n\)`),
	regexp.MustCompile(`Proceed\?`),
	regexp.MustCompile(`Continue\?`),
	regexp.MustCompile(`bufio\.NewReader\(os\.Stdin\)`),
	regexp.MustCompile(`os\.Stdin`),
	regexp.MustCompile(`ModeCharDevice`),
}

func checkADR014PromptRefs(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		for _, re := range adr014ForbiddenPromptPatterns {
			if m := re.FindString(line); m != "" {
				violations = append(violations,
					fmt.Sprintf("line %d: forbidden prompt pattern %q in %q", i+1, m, strings.TrimSpace(line)))
				break
			}
		}
	}
	return violations
}

// adr014LintedFiles is every file that holds a plugin verb or integration code (IMPLREADME Phase
// 2 L664-669, R4). A listed file that is missing fails the lint; it is never skipped.
var adr014LintedFiles = []string{
	"plugin.go",
	"plugin_acquire.go",
	"plugin_check.go",
	"plugin_remove.go",
	"plugin_integration_install.go",
	"integration_services.go",
	"integration_admission.go",
	"integration_pin.go",
	"integration_report.go",
	"plugin_guard.go",
}

func TestPluginADR014PromptAbsence(t *testing.T) {
	// cwd == internal/cmd during `go test`, so each file is readable by basename.
	for _, name := range adr014LintedFiles {
		src, err := os.ReadFile(name)
		if err != nil {
			t.Errorf("read %s: %v (every ADR-014-linted plugin/integration file must exist in internal/cmd)", name, err)
			continue
		}
		if v := checkADR014PromptRefs(string(src)); len(v) > 0 {
			t.Errorf("ADR-014 violation: %s must not contain interactive prompts (consent is the verb):\n  %s",
				name, strings.Join(v, "\n  "))
		}
	}
}

func TestPluginADR014LintSelfNegative(t *testing.T) {
	mustFlag := []string{
		`fmt.Print("install agents from plugin? (y/N) ")`,
		`if answer == "y" { // Proceed?`,
		`reader := bufio.NewReader(os.Stdin)`,
		`fmt.Println("Continue?")`,
	}
	for _, s := range mustFlag {
		if v := checkADR014PromptRefs(s); len(v) == 0 {
			t.Errorf("self-negative bite failed: lint did NOT flag %q (the check is vacuous)", s)
		}
	}
	mustNotFlag := []string{
		`fmt.Fprintln(cmd.OutOrStdout(), "no plugins acquired")`,
		`return fmt.Errorf("plugin %q not found under %s", name, dir)`,
		`review the formulas before installing`,
	}
	for _, s := range mustNotFlag {
		if v := checkADR014PromptRefs(s); len(v) > 0 {
			t.Errorf("false-positive: lint flagged legitimate content %q: %v", s, v)
		}
	}
}
