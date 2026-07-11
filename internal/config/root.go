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
		if root := resolveMarker(dir); root != "" {
			return root, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("not in an agentfactory workspace (no .agentfactory/factory.json found)")
		}
		dir = parent
	}
}

// resolveMarker returns the factory root that dir's marker denotes: the
// .factory-root redirect target if dir carries a valid one (worktree context),
// else dir itself if it holds factory.json (standard context), else "" when dir
// carries no valid marker. Env-free (ADR-004) — it stats/reads the filesystem only.
func resolveMarker(dir string) string {
	redirectPath := filepath.Join(dir, dotDir, ".factory-root")
	if data, err := os.ReadFile(redirectPath); err == nil {
		realRoot := strings.TrimSpace(string(data))
		if _, err := os.Stat(FactoryConfigPath(realRoot)); err == nil {
			return realRoot
		}
	}
	if _, err := os.Stat(FactoryConfigPath(dir)); err == nil {
		return dir
	}
	return ""
}

// FindEnclosingRoot scans the ancestors ABOVE root for an enclosing factory
// marker whose resolved root is DISTINCT from root's own. It returns the first
// such enclosing root, or "" (no error) when none is found — the scan is
// best-effort observability, so "none" is not an error.
//
// It is env-free (ADR-004): the AF_ROOT awareness stays in internal/cmd's
// resolveInvokerRoot. Candidates are deduped by RESOLVED root (each ancestor
// marker is followed through its redirect), so a worktree's .factory-root
// redirect + its outer factory — which resolve to the same root — stay quiet.
func FindEnclosingRoot(root string) (string, error) {
	self := resolveMarker(root)
	dir := filepath.Dir(root)
	for {
		if candidate := resolveMarker(dir); candidate != "" {
			if !SameResolvedRoot(candidate, root) && (self == "" || !SameResolvedRoot(candidate, self)) {
				return candidate, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// SameResolvedRoot reports whether two factory-root paths denote the same
// directory. It canonicalizes both via filepath.EvalSymlinks (cleaned-path
// fallback) and, as a tiebreaker for paths that canonicalize differently yet
// share an inode (hardlinked / bind-mounted factory.json), compares the two
// factory.json inodes via os.SameFile. This is the SINGLE equality comparator
// shared by K1's AF_ROOT cross-check (internal/cmd) and K5's enclosing scan
// (FindEnclosingRoot above), so the two can never reach opposite conclusions
// about "the same factory" on hardlink/bind-mount edges (issue #519 review
// follow-up). Equality is not resolution — sharing this comparator does NOT
// violate the four-resolver do-not-unify rule (T-INT-4).
func SameResolvedRoot(a, b string) bool {
	if canonicalPath(a) == canonicalPath(b) {
		return true
	}
	infoA, errA := os.Stat(FactoryConfigPath(a))
	infoB, errB := os.Stat(FactoryConfigPath(b))
	if errA != nil || errB != nil {
		return false
	}
	return os.SameFile(infoA, infoB)
}

func canonicalPath(p string) string {
	if real, err := filepath.EvalSymlinks(p); err == nil {
		return real
	}
	return filepath.Clean(p)
}
