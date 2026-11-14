package cmd

import (
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
)

// starter_config_literal_test.go — T7 (literal-absence), fable-implement Phase 5
// pinning test for Phase 4 (af-654 CFG-2/CFG-7). Modeled directly on
// formula_literal_absence_test.go's pattern (regex-list + pure-check-fn +
// self-negative test). Mirrors AC #2's two greps exactly:
//   grep -c 'starterConfigs' internal/cmd/install.go                                # want 0
//   grep -Ec '"(agents|dispatch|startup|models|messaging)\.json":' internal/cmd/install.go  # want 0
//
// Phase 6 deleted the starterConfigs literal map and replaced it with seedStarterConfigs.

var starterConfigLiteralPatterns = []*regexp.Regexp{
	regexp.MustCompile(`\bstarterConfigs\b`),
	regexp.MustCompile(`"(agents|dispatch|startup|models|messaging)\.json":`),
}

// checkStarterConfigLiterals returns one violation per line of content containing a
// forbidden starter-config literal. A nil result means clean.
func checkStarterConfigLiterals(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		for _, re := range starterConfigLiteralPatterns {
			if m := re.FindString(line); m != "" {
				violations = append(violations, fmt.Sprintf("line %d: %q in %q", i+1, m, strings.TrimSpace(line)))
				break
			}
		}
	}
	return violations
}

// TestStarterConfigLiteralAbsence fails if install.go still carries the starterConfigs
// map or any config-JSON literal key. Passes once Phase 6 lands seedStarterConfigs.
func TestStarterConfigLiteralAbsence(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	if v := checkStarterConfigLiterals(string(data)); len(v) > 0 {
		t.Errorf("install.go still carries config JSON literals (AC #2): %v — "+
			"delete starterConfigs and replace with seedStarterConfigs", v)
	}
}

// TestStarterConfigLiteralAbsenceSelfNegative proves the lint is not vacuous: every
// fixture spanning the two AC #2 patterns must be flagged, and legitimate residual
// prose (the "starter configs" phrase in installCmd.Long, a bare "agents.json" name
// with no literal-key colon) must not be.
func TestStarterConfigLiteralAbsenceSelfNegative(t *testing.T) {
	mustFlag := []string{
		`starterConfigs := map[string]string{`,
		`for name, content := range starterConfigs {`,
		`"agents.json":    ` + "`{}`" + `,`,
		`"dispatch.json":  "{}",`,
	}
	for _, s := range mustFlag {
		if v := checkStarterConfigLiterals(s); len(v) == 0 {
			t.Errorf("self-negative bite failed: lint did NOT flag %q (the check is vacuous)", s)
		}
	}

	mustNotFlag := []string{
		`Factory initialization creates the config directory, starter configs, issue store database, and hooks directory.`,
		`func seedStarterConfigs(out io.Writer, configDir, afSrc string, fallback bool) error {`,
		`data, err := configsFS.ReadFile("install_configs/" + name)`,
		`// see install_configs/agents.json for the shipped roster`,
	}
	for _, s := range mustNotFlag {
		if v := checkStarterConfigLiterals(s); len(v) > 0 {
			t.Errorf("false-positive: lint should not flag legitimate text %q, got %v", s, v)
		}
	}
}
