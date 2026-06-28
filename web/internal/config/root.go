package config

// This file re-implements the small af-core slice the web module needs to resolve a TRUSTWORTHY
// factory root (#432). It deliberately does NOT import internal/… — Go's internal seal plus the
// separate web go.mod make that compiler-impossible, and the duplication is the point
// (compiler-enforced C-2 decoupling; mirrors the precedent in web/internal/exec/validate.go and
// web/internal/rendezvous/rendezvous.go).
//
// Sources (copied, not imported):
//   - FindFactoryRoot (full walk-up + .factory-root redirect re-validation + old-layout migration
//     hint + exact no-factory error): internal/config/root.go:10-40
//   - dotDir / FactoryConfigPath equivalents (reused here as the package-existing dotDir +
//     factoryPath, settings.go:35,39): internal/config/paths.go:10,16
//   - ResolveFactoryRoot's AF_ROOT-first-then-cwd precedence: internal/cmd/watchdog.go:226-243

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FindFactoryRoot walks up from startDir to find .agentfactory/factory.json. It is a behavioural
// mirror of af-core internal/config.FindFactoryRoot: an old-layout config/factory.json at startDir
// produces a migration hint; a worktree .factory-root redirect is followed only after re-validating
// that its target actually holds a factory.json (a stale redirect must not short-circuit the
// walk-up); and reaching the filesystem root fails loud with the exact af-core error string.
//
// It reuses the package's existing dotDir (settings.go:35) and factoryPath (settings.go:39) — where
// af-core calls FactoryConfigPath(root), this calls factoryPath(root) (both yield
// <root>/.agentfactory/factory.json).
func FindFactoryRoot(startDir string) (string, error) {
	// Old-layout check at startDir before walking — a migration hint when a user is inside an
	// old-format workspace (af-core root.go:14-18).
	if _, err := os.Stat(filepath.Join(startDir, "config", "factory.json")); err == nil {
		if _, err := os.Stat(factoryPath(startDir)); err != nil {
			return "", fmt.Errorf("found old-layout config/factory.json in %s — run 'af install --init' to migrate to new layout", startDir)
		}
	}

	dir := startDir
	for {
		// Worktree redirect (af-core root.go:22-29): follow .factory-root only after confirming its
		// target holds a factory.json. Trim a trailing newline; a stale redirect (target has no
		// factory.json) must NOT short-circuit — fall through and keep walking up.
		redirectPath := filepath.Join(dir, dotDir, ".factory-root")
		if data, err := os.ReadFile(redirectPath); err == nil {
			realRoot := strings.TrimSpace(string(data))
			if _, err := os.Stat(factoryPath(realRoot)); err == nil {
				return realRoot, nil
			}
		}
		// Standard context: factory.json directly under dir.
		if _, err := os.Stat(factoryPath(dir)); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in an agentfactory workspace (no .agentfactory/factory.json found)")
		}
		dir = parent
	}
}

// ResolveFactoryRoot resolves the factory root with AF_ROOT-first-then-cwd precedence, mirroring
// af-core's resolveWatchdogRoot (watchdog.go:232-243). AF_ROOT, when set, is validated by a FULL
// FindFactoryRoot (it may itself be a worktree dir carrying a .factory-root redirect, or a factory
// subdirectory) — never a shallow stat; an AF_ROOT that does not resolve falls through to the cwd.
// AF_ROOT-first is the recorded #432 choice: afweb is launched from $HOME with AF_ROOT=$factory_root.
func ResolveFactoryRoot() (string, error) {
	if afRoot := os.Getenv("AF_ROOT"); afRoot != "" {
		if root, err := FindFactoryRoot(afRoot); err == nil {
			return root, nil
		}
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return FindFactoryRoot(wd)
}
