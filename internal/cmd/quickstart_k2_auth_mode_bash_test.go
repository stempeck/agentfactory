package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// This file pins Phase 1 (K2) of issue #693: the bash twins _litellm_auth_mode_read /
// _litellm_auth_mode_write. decisions.md D6: read is a thin, non-migrating accessor (record
// present -> echo it; absent -> empty); write is a literal, unguarded
// `printf '%s\n' "$mode" > .agentfactory/litellm-auth-mode`.

func TestQuickstartAuthModeWriteThenReadRoundTrips(t *testing.T) {
	dir := t.TempDir()
	script := shellFnsFrom(t, "_litellm_auth_mode_write", "_litellm_auth_mode_read") + `
cd "` + dir + `"
mkdir -p .agentfactory
_litellm_auth_mode_write "codex-subscription"
_litellm_auth_mode_read
`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("K2 bash helpers do not exist yet: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "codex-subscription" {
		t.Errorf("_litellm_auth_mode_read after write = %q, want codex-subscription", strings.TrimSpace(string(out)))
	}
	recPath := filepath.Join(dir, ".agentfactory", "litellm-auth-mode")
	data, statErr := os.ReadFile(recPath)
	if statErr != nil {
		t.Fatalf("record file not written at %s: %v", recPath, statErr)
	}
	if string(data) != "codex-subscription\n" {
		t.Errorf("record file content = %q, want %q (design-doc.md:116's literal printf form)", data, "codex-subscription\n")
	}
}

func TestQuickstartAuthModeReadReturnsEmptyWhenAbsent(t *testing.T) {
	dir := t.TempDir()
	script := shellFnsFrom(t, "_litellm_auth_mode_read") + `
cd "` + dir + `"
mkdir -p .agentfactory
out="$(_litellm_auth_mode_read)"
echo "READ=[$out]"
`
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	if err != nil {
		t.Fatalf("_litellm_auth_mode_read does not exist yet: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "READ=[]") {
		t.Errorf("expected empty read when record is absent (decisions.md D6, no migration in K2), got: %s", out)
	}
}
