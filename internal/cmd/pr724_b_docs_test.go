package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// p724bPluginsDocLine returns the one USING_PLUGINS.md line that starts with prefix.
func p724bPluginsDocLine(t *testing.T, prefix string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(findModuleRoot(t), "USING_PLUGINS.md"))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, line := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(line, prefix) {
			found = append(found, line)
		}
	}
	if len(found) != 1 {
		t.Fatalf("USING_PLUGINS.md: want exactly one line starting %q, found %d", prefix, len(found))
	}
	return found[0]
}

func TestPR724_T5_DocPluginCheckStatesDriftIsNotRun(t *testing.T) {
	bullet := p724bPluginsDocLine(t, "- **`af plugin check")
	for _, needle := range []string{"consent", "`error`"} {
		if !strings.Contains(bullet, needle) {
			t.Errorf("the af plugin check bullet must say a snapshot changed since consent is not run and is recorded as `error`; missing %q in:\n%s", needle, bullet)
		}
	}
}

func TestPR724_T5_DocServicesStatesDriftReportedNotStarted(t *testing.T) {
	services := p724bPluginsDocLine(t, "**Services.**")
	for _, needle := range []string{"consent", "INTEGRATION_NOT_BOUND"} {
		if !strings.Contains(services, needle) {
			t.Errorf("the Services paragraph must say a service whose snapshot changed since consent is not started and is reported as INTEGRATION_NOT_BOUND; missing %q in:\n%s", needle, services)
		}
	}
}

func TestPR724_T8_DocServicesNamesPinnedFormulaScope(t *testing.T) {
	services := p724bPluginsDocLine(t, "**Services.**")
	if !strings.Contains(services, "formula-scope") || !regexp.MustCompile(`\bpin(s|ned)?\b`).MatchString(services) {
		t.Errorf("the Services paragraph must name the formula-scope services a pin names alongside factory scope:\n%s", services)
	}
	if !strings.Contains(services, "plugins.json") {
		t.Errorf("the Services paragraph must say a shared service starts from the snapshot plugins.json records, not a pin's:\n%s", services)
	}
}
