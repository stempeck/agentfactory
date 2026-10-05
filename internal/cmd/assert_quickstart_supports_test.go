package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// This file pins K3 (design-doc.md) of issue #693: assertQuickstartSupports
// takes a marker+feature pair. A "--"-prefixed marker (the flag form) is a
// substring match, matching pre-Phase-2a behavior and the existing
// newAFSourceDir fixture. A bare marker (a bash function name) must match an
// actual function-definition line, not merely a comment mentioning the name —
// cross-review HIGH-2 exists precisely because a stale script could otherwise
// satisfy the guard by accident (e.g. a comment referencing the feature it
// still lacks).

func TestAssertQuickstartSupports_FlagMarkerPresent(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "quickstart.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\n# --litellm-auth=<mode>\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := assertQuickstartSupports(dir, "--litellm-auth", "--litellm-auth"); err != nil {
		t.Errorf("assertQuickstartSupports: %v", err)
	}
}

func TestAssertQuickstartSupports_FlagMarkerAbsent(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "quickstart.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := assertQuickstartSupports(dir, "--litellm-auth", "--litellm-auth")
	if err == nil {
		t.Fatal("expected an error when quickstart.sh lacks the --litellm-auth marker")
	}
	if !strings.Contains(err.Error(), script) {
		t.Errorf("error %q does not name the checked script path %q", err, script)
	}
	if !strings.Contains(err.Error(), "--litellm-auth") {
		t.Errorf("error %q does not name the feature %q", err, "--litellm-auth")
	}
}

func TestAssertQuickstartSupports_FunctionMarkerPresent(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "quickstart.sh")
	content := "#!/bin/bash\n" +
		"_ensure_codex_cli() {\n" +
		"    true\n" +
		"}\n" +
		"    _reconcile_gateway() {\n" + // indented, as it is nested inside setup_litellm in the real script
		"        true\n" +
		"    }\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := assertQuickstartSupports(dir, "_ensure_codex_cli", "the codex CLI install/consent flow"); err != nil {
		t.Errorf("assertQuickstartSupports(_ensure_codex_cli): %v", err)
	}
	if err := assertQuickstartSupports(dir, "_reconcile_gateway", "the gateway reconcile path"); err != nil {
		t.Errorf("assertQuickstartSupports(_reconcile_gateway): %v", err)
	}
}

// TestAssertQuickstartSupports_FunctionMarkerCommentOnlyRejected is the
// cross-review HIGH-2 regression: a comment that merely mentions the function
// name (as a stale script's changelog or a design-doc quote might) must NOT
// satisfy the guard — only an actual function-definition line does.
func TestAssertQuickstartSupports_FunctionMarkerCommentOnlyRejected(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "quickstart.sh")
	content := "#!/bin/bash\n# TODO: add _ensure_codex_cli() and _reconcile_gateway() eventually\ntrue\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := assertQuickstartSupports(dir, "_ensure_codex_cli", "the codex CLI install/consent flow"); err == nil {
		t.Fatal("a comment mentioning _ensure_codex_cli must not satisfy the function-marker guard")
	}
	if err := assertQuickstartSupports(dir, "_reconcile_gateway", "the gateway reconcile path"); err == nil {
		t.Fatal("a comment mentioning _reconcile_gateway must not satisfy the function-marker guard")
	}
}

func TestAssertQuickstartSupports_FunctionMarkerAbsent(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "quickstart.sh")
	if err := os.WriteFile(script, []byte("#!/bin/bash\ntrue\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := assertQuickstartSupports(dir, "_ensure_codex_cli", "the codex CLI install/consent flow")
	if err == nil {
		t.Fatal("expected an error when quickstart.sh lacks the _ensure_codex_cli function")
	}
	if !strings.Contains(err.Error(), "the codex CLI install/consent flow") {
		t.Errorf("error %q does not name the feature", err)
	}
}
