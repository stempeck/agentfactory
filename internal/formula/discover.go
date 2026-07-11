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
//  1. Factory root formulas/ (config.FormulasDir(factoryRoot))
//  2. User ~/formulas/ (via config.FormulasDir)
//
// File extensions tried: .formula.toml (primary), .formula.json (fallback)
//
// factoryRoot must be an ALREADY-VALIDATED root supplied by the caller — this
// function does NOT resolve it from a working directory. Ambient cwd→root
// resolution here would launder around the internal/cmd resolveInvokerRoot seam
// (the #519 cross-check), so the cmd layer passes the root it already holds and the
// drift guard enforces that this package never reintroduces config.FindFactoryRoot
// (issue #519 review follow-up, thread 7a). An empty factoryRoot skips the factory
// search path (home formulas only).
func FindFormulaFile(name string, factoryRoot string) (string, error) {
	var searchPaths []string

	// 1. Factory root formulas (via config.FormulasDir)
	if factoryRoot != "" {
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
