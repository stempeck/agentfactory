package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// Designer caps on an integration's declared content (design L371, scale.md A1).
const (
	integrationMaxFiles = 2000
	integrationMaxBytes = 16 << 20
)

// IntegrationUpstreamDir holds the pinned upstream checkout; it is hashed only where a declared
// path points into it (spec L266).
const IntegrationUpstreamDir = ".upstream"

// IntegrationContentHash is the canonical content hash of an integration snapshot (spec L260-267,
// design L371). It walks the declared paths (plus the manifest, D14) under snapshotDir and hashes
// one "<relpath> <mode> <sha256>\n" line per regular file in sorted relpath order, where <mode> is
// 0755 when any exec bit is set and 0644 otherwise, so clones made under different umasks agree.
//
// files maps each slash-separated relpath to "<mode> <sha256>", so a caller comparing two maps
// names every differing file, including a mode-only change (D35). Symlinks, devices and other
// irregular entries are refused, .git/ is excluded, and more than 2,000 files or 16 MiB is refused.
func IntegrationContentHash(snapshotDir string, declared []string) (sum string, files map[string]string, err error) {
	roots := []string{IntegrationManifestFile}
	for _, d := range declared {
		rel, err := cleanDeclaredPath(d)
		if err != nil {
			return "", nil, err
		}
		roots = append(roots, rel)
	}

	files = map[string]string{}
	var total int64
	for i, root := range roots {
		start := filepath.Join(snapshotDir, filepath.FromSlash(root))
		if i == 0 {
			if _, err := os.Lstat(start); os.IsNotExist(err) {
				continue // a manifest the caller did not declare is hashed only when present
			}
		}
		if err := refuseSymlinkedParents(snapshotDir, root); err != nil {
			return "", nil, err
		}
		err := filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(snapshotDir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			switch {
			case d.Type()&fs.ModeSymlink != 0:
				return fmt.Errorf("integration content %s is a symlink; symlinks are refused", rel)
			case d.IsDir():
				if d.Name() == ".git" || (rel == IntegrationUpstreamDir && !pathWithin(root, IntegrationUpstreamDir)) {
					return filepath.SkipDir
				}
				return nil
			case !d.Type().IsRegular():
				return fmt.Errorf("integration content %s is not a regular file (%s)", rel, d.Type())
			}
			if _, seen := files[rel]; seen {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if len(files) >= integrationMaxFiles {
				return fmt.Errorf("integration content has more than %d files (at %s)", integrationMaxFiles, rel)
			}
			total += info.Size()
			if total > integrationMaxBytes {
				return fmt.Errorf("integration content exceeds %d bytes (at %s)", integrationMaxBytes, rel)
			}
			contentSum, err := FileSHA256(p)
			if err != nil {
				return err
			}
			mode := "0644"
			if info.Mode().Perm()&0o111 != 0 {
				mode = "0755"
			}
			files[rel] = mode + " " + contentSum
			return nil
		})
		if err != nil {
			return "", nil, fmt.Errorf("hashing %s: %w", snapshotDir, err)
		}
	}

	h := sha256.New()
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		fmt.Fprintf(h, "%s %s\n", rel, files[rel])
	}
	return hex.EncodeToString(h.Sum(nil)), files, nil
}

// refuseSymlinkedParents refuses a declared root reached through a symlinked parent dir: WalkDir
// sees only the root's own last component, so the hash would silently cover another tree.
func refuseSymlinkedParents(snapshotDir, root string) error {
	parts := strings.Split(root, "/")
	for i := 1; i < len(parts); i++ {
		rel := strings.Join(parts[:i], "/")
		info, err := os.Lstat(filepath.Join(snapshotDir, filepath.FromSlash(rel)))
		if err != nil {
			return nil // a missing parent surfaces as WalkDir's own error on the root
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("hashing %s: integration content %s is a symlink; symlinks are refused", snapshotDir, rel)
		}
	}
	return nil
}

// cleanDeclaredPath returns d as a clean slash-separated path that stays inside the snapshot.
func cleanDeclaredPath(d string) (string, error) {
	rel := path.Clean(filepath.ToSlash(d))
	if d == "" || path.IsAbs(rel) || rel == ".." || strings.HasPrefix(rel, "../") {
		return "", fmt.Errorf("declared path %q must be relative and stay inside the integration", d)
	}
	return rel, nil
}

// pathWithin reports whether the slash-separated rel is dir or lies under it.
func pathWithin(rel, dir string) bool {
	return rel == dir || strings.HasPrefix(rel, dir+"/")
}

// FileSHA256 streams p through the hash so a large file is never held in memory.
func FileSHA256(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
