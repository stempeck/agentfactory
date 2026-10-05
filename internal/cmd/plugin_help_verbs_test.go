package cmd

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

// ---- AC6: exactly six plugin verbs (L533-534; acceptance criterion 6) ---------

// intBHelpVerbLine is the acceptance block's grep -cE '^\s+(acquire|check|install|list|remove|verify)\b'.
var intBHelpVerbLine = regexp.MustCompile(`^\s+(acquire|check|install|list|remove|verify)\b`)

func TestPluginHelpListsExactlySixVerbs(t *testing.T) {
	var buf bytes.Buffer
	pluginCmd.SetOut(&buf)
	t.Cleanup(func() { pluginCmd.SetOut(nil) })
	if err := pluginCmd.Help(); err != nil {
		t.Fatalf("plugin help: %v", err)
	}
	help := buf.String()

	seen := map[string]int{}
	for _, line := range strings.Split(help, "\n") {
		if m := intBHelpVerbLine.FindStringSubmatch(line); m != nil {
			seen[m[1]]++
		}
	}
	total := 0
	for _, n := range seen {
		total += n
	}
	if total != 6 {
		t.Errorf("af plugin --help lists %d verb line(s) %v, want exactly 6 (acquire check install list remove verify):\n%s", total, seen, help)
	}
	for _, verb := range []string{"acquire", "check", "install", "list", "remove", "verify"} {
		if seen[verb] != 1 {
			t.Errorf("verb %q appears on %d help line(s), want 1", verb, seen[verb])
		}
		if !regexp.MustCompile(`\b` + verb + `\b`).MatchString(pluginCmd.Long) {
			t.Errorf("pluginCmd.Long must name the %s verb (L533-534 rewrite)", verb)
		}
	}
	if pluginInstallCmd.Flags().Lookup("factory-wide") == nil {
		t.Error("af plugin install must register --factory-wide")
	}
}
