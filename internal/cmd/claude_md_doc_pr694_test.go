package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// readClaudeMdDoc reads the repo-root CLAUDE.md — the operator/agent contract doc that inventories
// the `af` command surface. Sibling of readUsingLitellmDoc / readUsingAgentfactoryDoc.
func readClaudeMdDoc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md: %v", err)
	}
	return string(data)
}

// TestClaudeMdDoc_GatewayAuthTokens pins PR #694 BODY-9: CLAUDE.md's `af gateway auth status` /
// `af config models check` documentation must name the `--first` liveness probe and the two
// `status --json` fields the subscription-mode work adds — `selected_mode` and
// `device_code_requested`. GREP-RED at head (none of the three tokens appear); GREEN once the
// command inventory is updated.
func TestClaudeMdDoc_GatewayAuthTokens(t *testing.T) {
	content := readClaudeMdDoc(t)
	for _, want := range []string{"--first", "selected_mode", "device_code_requested"} {
		if !strings.Contains(content, want) {
			t.Errorf("CLAUDE.md does not document %q; the `af gateway auth status`/`af config models check` "+
				"entry must name the --first probe and the status --json fields it now emits (PR #694 BODY-9)", want)
		}
	}
}
