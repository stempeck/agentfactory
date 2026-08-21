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

// gateProvenanceLines returns the non-empty lines of the toggle audit log ("" when absent).
func gateProvenanceLines(t *testing.T, root string) []string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".agentfactory", ".fidelity-gate.log"))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read .fidelity-gate.log: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// TestApplyGate_FidelityProvenance covers the blanket `af up` writer half of design-doc L254-255:
// every toggle write appends exactly one provenance line, and the source field distinguishes it
// from a CLI flip so `af fidelity status` can answer "who turned it back on".
func TestApplyGate_FidelityProvenance(t *testing.T) {
	root, formulaDir := newGateRoot(t)

	if err := applyGate(root, formulaDir, "fidelity", "on"); err != nil {
		t.Fatalf("applyGate fidelity on: %v", err)
	}
	lines := gateProvenanceLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("want exactly 1 provenance line after the blanket write, got %d: %v", len(lines), lines)
	}
	fields := strings.Fields(lines[0])
	if len(fields) != 4 {
		t.Fatalf("provenance line must be `ts actor source state`, got %q", lines[0])
	}
	if fields[2] == "cli" {
		t.Errorf("the blanket af up writer must not log source %q — it is indistinguishable from a CLI flip", fields[2])
	}
	if fields[3] != "on" {
		t.Errorf("state = %q, want %q", fields[3], "on")
	}

	if err := applyGate(root, formulaDir, "fidelity", "off"); err != nil {
		t.Fatalf("applyGate fidelity off: %v", err)
	}
	if lines := gateProvenanceLines(t, root); len(lines) != 2 {
		t.Errorf("the second write must APPEND, not truncate; want 2 lines, got %d: %v", len(lines), lines)
	}
}

func TestApplyGate_FidelityNoProvenanceOnSentinelsOrRefusal(t *testing.T) {
	t.Run("sentinel states write nothing", func(t *testing.T) {
		root, formulaDir := newGateRoot(t)
		for _, state := range []string{"", "default"} {
			if err := applyGate(root, formulaDir, "fidelity", state); err != nil {
				t.Fatalf("applyGate fidelity %q: %v", state, err)
			}
		}
		if lines := gateProvenanceLines(t, root); len(lines) != 0 {
			t.Errorf("a C-4 no-op must leave no audit trail, got %v", lines)
		}
	})

	t.Run("a refused off writes nothing", func(t *testing.T) {
		root, formulaDir := newGateRoot(t)
		runtimeDir := filepath.Join(formulaDir, ".runtime")
		if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("bd-x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := applyGate(root, formulaDir, "fidelity", "off"); err == nil {
			t.Fatal("expected the active-formula guard to refuse")
		}
		if lines := gateProvenanceLines(t, root); len(lines) != 0 {
			t.Errorf("a refused write must not appear in the audit log, got %v", lines)
		}
	})
}

// TestSeedFidelityGate_Provenance covers the third writer (design-doc L254-255). The seed is
// seed-if-absent, so a re-`--init` over an operator's own toggle must add neither a byte nor a line.
func TestSeedFidelityGate_Provenance(t *testing.T) {
	root, _ := newGateRoot(t)

	if err := seedFidelityGate(root); err != nil {
		t.Fatalf("seedFidelityGate: %v", err)
	}
	data, err := os.ReadFile(fidelityGateFile(root))
	if err != nil {
		t.Fatalf("read fidelity gate: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("seed wrote %q, want %q", string(data), "on\n")
	}
	lines := gateProvenanceLines(t, root)
	if len(lines) != 1 {
		t.Fatalf("a fresh seed appends exactly one provenance line, got %d: %v", len(lines), lines)
	}
	if fields := strings.Fields(lines[0]); len(fields) != 4 || fields[3] != "on" {
		t.Errorf("seed line must be `ts actor source on`, got %q", lines[0])
	}

	if err := seedFidelityGate(root); err != nil {
		t.Fatalf("re-seed: %v", err)
	}
	if lines := gateProvenanceLines(t, root); len(lines) != 1 {
		t.Errorf("seed-if-absent must not log when it writes nothing, got %d lines: %v", len(lines), lines)
	}
}
