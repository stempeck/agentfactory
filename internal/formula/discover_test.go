package formula

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func TestFindFormulaFile_FactoryRoot(t *testing.T) {
	// Create a temp factory structure: root/.agentfactory/factory.json + formulas dir
	root := t.TempDir()
	configDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	formulaDir := config.FormulasDir(root)
	if err := os.MkdirAll(formulaDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(formulaDir, "my-formula.formula.toml"), []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}

	// FindFormulaFile now takes an ALREADY-VALIDATED factory root (thread 7a); it no
	// longer walks up from a working directory.
	path, err := FindFormulaFile("my-formula", root)
	if err != nil {
		t.Fatalf("FindFormulaFile failed: %v", err)
	}
	if !strings.HasSuffix(path, "my-formula.formula.toml") {
		t.Errorf("path = %q, want suffix my-formula.formula.toml", path)
	}
}

func TestFindFormulaFile_HomeDir(t *testing.T) {
	// Create formula in home dir
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("cannot determine home dir")
	}

	formulaDir := config.FormulasDir(home)
	if err := os.MkdirAll(formulaDir, 0755); err != nil {
		t.Fatal(err)
	}

	testFile := filepath.Join(formulaDir, "home-test-formula.formula.toml")
	if err := os.WriteFile(testFile, []byte("# test"), 0644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(testFile)

	// Empty factory root ⇒ skip the factory search path, home formulas only (thread 7a).
	path, err := FindFormulaFile("home-test-formula", "")
	if err != nil {
		t.Fatalf("FindFormulaFile failed: %v", err)
	}
	if !strings.HasSuffix(path, "home-test-formula.formula.toml") {
		t.Errorf("path = %q, want suffix home-test-formula.formula.toml", path)
	}
}

func TestFindFormulaFile_NotFound(t *testing.T) {
	workDir := t.TempDir()
	_, err := FindFormulaFile("nonexistent-formula-xyz", workDir)
	if err == nil {
		t.Error("expected error for formula not found")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to contain 'not found'", err.Error())
	}
}

func TestFindFormulaFile_JSONFallback(t *testing.T) {
	// Create a temp factory with a .formula.json file (fallback extension)
	root := t.TempDir()
	configDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"test"}`), 0644); err != nil {
		t.Fatal(err)
	}

	formulaDir := config.FormulasDir(root)
	if err := os.MkdirAll(formulaDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(formulaDir, "json-formula.formula.json"), []byte("{}"), 0644); err != nil {
		t.Fatal(err)
	}

	// FindFormulaFile now takes an already-validated factory root (thread 7a).
	path, err := FindFormulaFile("json-formula", root)
	if err != nil {
		t.Fatalf("FindFormulaFile failed: %v", err)
	}
	if !strings.HasSuffix(path, "json-formula.formula.json") {
		t.Errorf("path = %q, want suffix json-formula.formula.json", path)
	}
}
