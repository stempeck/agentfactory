package cmd

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNoBeadsReferences(t *testing.T) {
	repoRoot := findRepoRoot(t)

	patterns := []string{".beads", "BD_ACTOR", "BEADS_DIR"}

	skipDirs := map[string]bool{
		".designs":      true,
		"vendor":        true,
		".git":          true,
		".agentfactory": true,
		"todos":         true,
	}

	skipFiles := map[string]bool{
		"enforce_naming_test.go":  true,
		"store_gitignore_test.go": true,
	}

	excludedFuncs := []string{
		"func migrateBeadsDir(",
		"func TestMigrateBeadsDir",
	}

	var violations []string

	filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}

		rel, _ := filepath.Rel(repoRoot, path)

		if d.IsDir() {
			top := strings.SplitN(rel, string(filepath.Separator), 2)[0]
			if skipDirs[top] {
				return filepath.SkipDir
			}
			return nil
		}

		ext := filepath.Ext(rel)
		isRoleTemplate := strings.HasPrefix(rel, filepath.Join("internal", "templates", "roles"))
		if ext != ".go" && ext != ".sh" && ext != ".toml" && !(isRoleTemplate && (ext == ".md" || strings.HasSuffix(rel, ".md.tmpl"))) {
			return nil
		}

		if skipFiles[filepath.Base(path)] {
			return nil
		}

		if rel == filepath.Join("internal", "config", "paths.go") {
			return nil
		}

		f, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer f.Close()

		inExcludedFunc := false
		braceDepth := 0
		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()

			if !inExcludedFunc {
				for _, prefix := range excludedFuncs {
					if strings.Contains(line, prefix) {
						inExcludedFunc = true
						braceDepth = 0
						break
					}
				}
			}
			if inExcludedFunc {
				braceDepth += strings.Count(line, "{") - strings.Count(line, "}")
				if braceDepth <= 0 && strings.Contains(line, "}") {
					inExcludedFunc = false
				}
				continue
			}

			for _, pat := range patterns {
				if strings.Contains(line, pat) {
					violations = append(violations, fmt.Sprintf("%s:%d: %s", rel, lineNum, pat))
				}
			}
		}

		return scanner.Err()
	})

	for _, v := range violations {
		t.Errorf("stale reference found: %s", v)
	}
}

func TestNoBeadsReferencesInDocs(t *testing.T) {
	repoRoot := findRepoRoot(t)

	docFiles := []string{
		"CLAUDE.md",
		"USING_AGENTFACTORY.md",
		"README.md",
		filepath.Join("docs", "architecture", "adrs", "ADR-015-formula-three-location-lifecycle.md"),
		filepath.Join("docs", "architecture", "subsystems", "session.md"),
	}

	var violations []string

	for _, rel := range docFiles {
		path := filepath.Join(repoRoot, rel)
		f, err := os.Open(path)
		if err != nil {
			t.Errorf("cannot open %s: %v", rel, err)
			continue
		}

		scanner := bufio.NewScanner(f)
		lineNum := 0
		for scanner.Scan() {
			lineNum++
			line := scanner.Text()
			if strings.Contains(line, ".beads") {
				violations = append(violations, fmt.Sprintf("%s:%d: .beads", rel, lineNum))
			}
		}
		f.Close()

		if err := scanner.Err(); err != nil {
			t.Errorf("scanning %s: %v", rel, err)
		}
	}

	for _, v := range violations {
		t.Errorf("stale doc reference: %s", v)
	}
}
