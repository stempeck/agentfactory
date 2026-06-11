package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newGateRoot creates a root with a writable .agentfactory dir and returns the
// root and a separate formulaDir (where .runtime/hooked_formula is checked).
func newGateRoot(t *testing.T) (root, formulaDir string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	formulaDir = t.TempDir()
	return root, formulaDir
}

func TestApplyFidelityGate_On(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	if err := applyFidelityGate(root, formulaDir, "on"); err != nil {
		t.Fatalf("applyFidelityGate on: %v", err)
	}
	data, err := os.ReadFile(fidelityGateFile(root))
	if err != nil {
		t.Fatalf("read gate: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("gate = %q, want %q", string(data), "on\n")
	}
}

func TestApplyFidelityGate_OffNoFormula(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	if err := applyFidelityGate(root, formulaDir, "off"); err != nil {
		t.Fatalf("applyFidelityGate off: %v", err)
	}
	data, err := os.ReadFile(fidelityGateFile(root))
	if err != nil {
		t.Fatalf("read gate: %v", err)
	}
	if string(data) != "off\n" {
		t.Errorf("gate = %q, want %q", string(data), "off\n")
	}
}

func TestApplyFidelityGate_OffBlockedByActiveFormula(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	runtimeDir := filepath.Join(formulaDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("bd-x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := applyFidelityGate(root, formulaDir, "off")
	if err == nil {
		t.Fatal("expected refusal while formula active, got nil")
	}
	if !strings.Contains(err.Error(), "cannot disable fidelity gate") {
		t.Errorf("error %q does not contain expected message", err.Error())
	}
	// Must NOT have written "off".
	if data, rerr := os.ReadFile(fidelityGateFile(root)); rerr == nil && strings.TrimSpace(string(data)) == "off" {
		t.Error("gate was written to 'off' despite active formula")
	}
}

func TestApplyFidelityGate_BadState(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	if err := applyFidelityGate(root, formulaDir, "weird"); err == nil {
		t.Fatal("expected usage error for bad state, got nil")
	}
}

func TestApplyGate_NoOpOnSentinels(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	for _, state := range []string{"", "default"} {
		for _, gate := range []string{"quality", "fidelity"} {
			if err := applyGate(root, formulaDir, gate, state); err != nil {
				t.Fatalf("applyGate(%q,%q): %v", gate, state, err)
			}
		}
	}
	// No gate files should have been created.
	if _, err := os.Stat(qualityGateFile(root)); err == nil {
		t.Error("quality gate file written for sentinel state")
	}
	if _, err := os.Stat(fidelityGateFile(root)); err == nil {
		t.Error("fidelity gate file written for sentinel state")
	}
}

func TestApplyGate_QualityDirectWriteUsesRoot(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	if err := applyGate(root, formulaDir, "quality", "off"); err != nil {
		t.Fatalf("applyGate quality off: %v", err)
	}
	data, err := os.ReadFile(qualityGateFile(root))
	if err != nil {
		t.Fatalf("read quality gate under root: %v", err)
	}
	if string(data) != "off\n" {
		t.Errorf("quality gate = %q, want %q", string(data), "off\n")
	}
}

func TestApplyGate_FidelityRoutesThroughGuard(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	runtimeDir := filepath.Join(formulaDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("bd-x"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := applyGate(root, formulaDir, "fidelity", "off")
	if err == nil {
		t.Fatal("expected fidelity off to be refused via guard, got nil")
	}
	if !strings.Contains(err.Error(), "cannot disable fidelity gate") {
		t.Errorf("error %q does not route through the active-formula guard", err.Error())
	}
}

func TestApplyGate_FidelityOnWritesUnderRoot(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	if err := applyGate(root, formulaDir, "fidelity", "on"); err != nil {
		t.Fatalf("applyGate fidelity on: %v", err)
	}
	data, err := os.ReadFile(fidelityGateFile(root))
	if err != nil {
		t.Fatalf("read fidelity gate: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("fidelity gate = %q, want %q", string(data), "on\n")
	}
}
