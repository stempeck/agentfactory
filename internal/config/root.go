package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FindFactoryRoot walks up from startDir to find .agentfactory/factory.json
func FindFactoryRoot(startDir string) (string, error) {
	// Check for old layout at startDir before walking — gives a helpful
	// migration hint when a user is inside an old-format workspace.
	if _, err := os.Stat(filepath.Join(startDir, "config", "factory.json")); err == nil {
		if _, err := os.Stat(FactoryConfigPath(startDir)); err != nil {
			return "", fmt.Errorf("found old-layout config/factory.json in %s — run 'af install --init' to migrate to new layout", startDir)
		}
	}

	dir := startDir
	for {
		// Check for redirect file (worktree context)
		redirectPath := filepath.Join(dir, dotDir, ".factory-root")
		if data, err := os.ReadFile(redirectPath); err == nil {
			realRoot := strings.TrimSpace(string(data))
			if _, err := os.Stat(FactoryConfigPath(realRoot)); err == nil {
				return realRoot, nil
			}
		}
		// Check for factory.json (standard context)
		if _, err := os.Stat(FactoryConfigPath(dir)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in an agentfactory workspace (no .agentfactory/factory.json found)")
		}
		dir = parent
	}
}
