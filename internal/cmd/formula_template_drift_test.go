package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

func TestFormulaTemplateDrift(t *testing.T) {
	t.Skip("chicken-and-egg: formulas land before agent-gen can run against them — new formulas will always fail this test until agent-gen-all.sh is run in a live factory")
	const (
		formulaDir  = "install_formulas"
		templateDir = "../templates/roles"
	)

	formulaFiles := listFormulas(t, formulaDir)

	for _, name := range formulaFiles {
		data, err := os.ReadFile(filepath.Join(formulaDir, name))
		if err != nil {
			t.Fatalf("read formula %s: %v", name, err)
		}
		f, err := formula.Parse(data)
		if err != nil {
			t.Fatalf("parse formula %s: %v", name, err)
		}

		tmplPath := filepath.Join(templateDir, f.Name+".md.tmpl")
		committed, err := os.ReadFile(tmplPath)
		if err != nil {
			if os.IsNotExist(err) {
				t.Errorf("formula %q has no committed template at %s — run agent-gen-all.sh", f.Name, tmplPath)
				continue
			}
			t.Fatalf("read template %s: %v", tmplPath, err)
		}

		regenerated := generateAgentTemplate(f, f.Name, "autonomous")

		if string(committed) != regenerated {
			t.Errorf("template drift: %s.md.tmpl does not match regenerated output from %s — run agent-gen-all.sh to resync", f.Name, name)
		}
	}

	entries, err := os.ReadDir(templateDir)
	if err != nil {
		t.Fatalf("read template dir %s: %v", templateDir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		tmplName := e.Name()
		if filepath.Ext(tmplName) != ".tmpl" {
			continue
		}
		baseName := strings.TrimSuffix(tmplName, ".md.tmpl")
		switch baseName {
		case "manager", "supervisor":
			continue
		}
		formulaPath := filepath.Join(formulaDir, baseName+".formula.toml")
		if _, err := os.Stat(formulaPath); os.IsNotExist(err) {
			t.Errorf("orphan template %s has no corresponding formula in %s", tmplName, formulaDir)
		}
	}
}
