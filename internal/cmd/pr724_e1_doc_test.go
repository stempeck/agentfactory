package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPR724_T10_UsingPluginsDocStatesExecBitRule(t *testing.T) {
	b, err := os.ReadFile(filepath.Join(findModuleRoot(t), "USING_PLUGINS.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"`#!`", "exec bit"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("USING_PLUGINS.md must state the install and acquire file-mode rule; it lacks %q", want)
		}
	}
}
