package formula

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/stempeck/agentfactory/internal/config"
)

// FindFormulaFile searches for a formula file by name in standard locations.
//
// Search order:
//  1. Factory root formulas/ (found via config.FindFactoryRoot from workDir)
//  2. User ~/formulas/ (via config.FormulasDir)
//
// File extensions tried: .formula.toml (primary), .formula.json (fallback)
func FindFormulaFile(name string, workDir string) (string, error) {
	var searchPaths []string

	// 1. Factory root formulas (via config.FormulasDir)
	if factoryRoot, err := config.FindFactoryRoot(workDir); err == nil {
		searchPaths = append(searchPaths, config.FormulasDir(factoryRoot))
	}

	// 2. User home formulas (via config.FormulasDir)
	if home, err := os.UserHomeDir(); err == nil {
		searchPaths = append(searchPaths, config.FormulasDir(home))
	}

	extensions := []string{".formula.toml", ".formula.json"}
	for _, basePath := range searchPaths {
		for _, ext := range extensions {
			path := filepath.Join(basePath, name+ext)
			if _, err := os.Stat(path); err == nil {
				return path, nil
			}
		}
	}

	return "", fmt.Errorf("formula %q not found in search paths", name)
}
