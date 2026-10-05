package cmd

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// This file pins Phase 1 (K4) of issue #693: seven new var-typed ADR-009 seams beside
// promptOpenAIKey (install.go:1030), landed with real behavior per design-doc.md:118 (decisions.md
// D8 — not stubs). Nothing calls them yet in Phase 1 (Phases 2a/5 wire them into the flow); these
// tests exercise each seam's own contract directly. None of these seams exist yet, so this file
// fails to compile until Phase 6 lands them — the correct RED state.

// promptCodexInstallConsent: "stdin is not a terminal" refusal when not a char device (mirrors
// promptOpenAIKey's own :1031-1034 check, since tests run with a non-terminal stdin).
func TestPromptCodexInstallConsent_RefusesWithoutTerminal(t *testing.T) {
	var errW bytes.Buffer
	_, err := promptCodexInstallConsent(&errW)
	if err == nil {
		t.Fatal("expected a refusal when stdin is not a terminal (test harness stdin is never a tty)")
	}
}

// lookPathCodex wraps exec.LookPath("codex") — must report false/error when codex is not on PATH.
func TestLookPathCodex_ReportsAbsentWhenNotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // an empty PATH guarantees codex cannot resolve
	if _, err := lookPathCodex(); err == nil {
		t.Error("expected lookPathCodex to fail when codex is not on PATH")
	}
}

// sudoNonInteractiveOK runs `sudo -n true`; with PATH pointed at a stub sudo that always exits
// nonzero, it must report false and never hang.
func TestSudoNonInteractiveOK_FalseWhenSudoRefuses(t *testing.T) {
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "sudo")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nexit 1\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir)
	if sudoNonInteractiveOK() {
		t.Error("expected sudoNonInteractiveOK() to be false when `sudo -n true` fails")
	}
}

// npmGlobalRootWritable runs `npm root -g` then probes it for O_CREATE|O_EXCL writability. With a
// stub npm printing a writable temp dir, it must report true.
func TestNpmGlobalRootWritable_TrueForWritableRoot(t *testing.T) {
	npmRoot := t.TempDir()
	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "npm")
	script := "#!/bin/sh\necho '" + npmRoot + "'\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir)
	if !npmGlobalRootWritable() {
		t.Error("expected npmGlobalRootWritable() to be true for a writable temp dir")
	}
}

// codexHomeWritable checks $CODEX_HOME (or ~/.codex) for write access, creating it if absent.
func TestCodexHomeWritable_CreatesAndConfirmsWritable(t *testing.T) {
	home := filepath.Join(t.TempDir(), "codex-home")
	t.Setenv("CODEX_HOME", home)
	if !codexHomeWritable() {
		t.Errorf("expected codexHomeWritable() to create %s and report true", home)
	}
	if info, err := os.Stat(home); err != nil || !info.IsDir() {
		t.Errorf("codexHomeWritable() did not create %s", home)
	}
}

// codexSessionValid is the Go twin of K8's bash predicate: codex login status exit code + auth_mode
// + refresh-token presence. With CODEX_HOME pointed at an empty dir (no auth.json), it must be false.
func TestCodexSessionValid_FalseWithNoAuthFile(t *testing.T) {
	t.Setenv("CODEX_HOME", t.TempDir())
	if codexSessionValid() {
		t.Error("expected codexSessionValid() to be false with no auth.json present")
	}
}

// runCodexDeviceAuth(ctx, out, errW) exists as an overridable seam (design-doc.md:118) — Phase 1
// lands the seam; Phase 2a/5 wire real exec behavior into the flow. This test pins that the var is
// present, reassignable, and matches the spec's (ctx, out, errW) shape, following the ADR-009
// idiom every sibling seam in this file already uses.
func TestRunCodexDeviceAuth_IsAnOverridableSeam(t *testing.T) {
	orig := runCodexDeviceAuth
	t.Cleanup(func() { runCodexDeviceAuth = orig })

	called := false
	runCodexDeviceAuth = func(ctx context.Context, out, errW io.Writer) error {
		called = true
		return nil
	}
	if err := runCodexDeviceAuth(context.Background(), &bytes.Buffer{}, &bytes.Buffer{}); err != nil {
		t.Fatalf("stub runCodexDeviceAuth returned an error: %v", err)
	}
	if !called {
		t.Error("stub runCodexDeviceAuth was not invoked")
	}
}
