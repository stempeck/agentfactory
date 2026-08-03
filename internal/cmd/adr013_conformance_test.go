package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ADR-013 conformance: design docs must engage the minimal-dependency policy
// instead of drifting past it. These guards run in `make test`, so a
// design-only PR fails here before any implementation exists — a go.mod diff
// caught at implementation time is caught after the design has already locked
// the direction in.

const adr013RelPath = "docs/architecture/adrs/ADR-013-minimal-go-mod-dependencies.md"

// foreignModulePath matches Go-module-shaped import paths. The named hosts are
// unambiguous Go module ecosystems; github.com owner/repo paths are
// module-shaped unless they are part of a URL (scheme-prefixed references are
// links, not imports — the caller checks for a preceding "://").
var foreignModulePath = regexp.MustCompile(`\b(?:golang\.org/x|google\.golang\.org|gopkg\.in|go\.opentelemetry\.io|go\.uber\.org|sigs\.k8s\.io|cloud\.google\.com/go|github\.com/[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)(?:/[A-Za-z0-9_./-]+)*`)

// approvedModulePrefixes are the module paths a design doc may mention freely:
// this repository's own modules and ADR-013's approved set (with its
// transitive pins, which appear in go.sum and may be legitimately discussed).
var approvedModulePrefixes = []string{
	"github.com/stempeck/",
	"github.com/BurntSushi/toml",
	"github.com/spf13/cobra",
	"github.com/spf13/pflag",
	"github.com/inconshreveable/mousetrap",
	// Historical references in archived design docs, not dependencies:
	// nothing under this owner is used, imported, or invoked anywhere in the
	// current system.
	"github.com/steveyegge/",
	// Placeholders: a synthetic module path used by throwaway build scripts,
	// and the literal "..." ellipsis standing in for this repo's own path.
	"github.com/x/agentfactory",
	"github.com/.../",
}

// historicalExemptFiles were authored before this guard existed and carry
// uncited (but rejecting) dependency mentions. Do not add entries for new
// designs — cite ADR-013 on the line instead.
var historicalExemptFiles = map[string]bool{
	".designs/formula-agent-ownership-122/implementation-plan/IMPLREADME_PHASE4.md": true,
}

// versionPin matches require-line syntax immediately after a module path
// ("pkg v1.2.3", "`pkg` | v1.2.3"). A version-pinned foreign module is
// adoption-shaped regardless of the prose around it.
var versionPin = regexp.MustCompile("^[`|)\\s]*v[0-9]+\\.")

// tableGrowthLanguage matches proposals to grow ADR-013's approved table
// ("six rows added to the grandfathered table"). Kept narrow on purpose:
// restating the prohibition must not trip it.
var tableGrowthLanguage = regexp.MustCompile(`(?i)\b(?:rows?|entr(?:y|ies))\b[^|]{0,60}\badded to\b[^|]{0,40}\b(?:grandfathered|approved)`)

// approvedDirectRequires is the frozen direct-require set: ADR-013's grandfathered
// table, and the only module paths permitted in any go.mod require block in this
// repository. Growing it is an operator decision — the operator updates this list,
// the ADR table, and go.mod in the same commit.
var approvedDirectRequires = []string{
	"github.com/BurntSushi/toml",
	"github.com/spf13/cobra",
}

func isApprovedModule(path string) bool {
	for _, prefix := range approvedModulePrefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// TestADR013DesignDocsDoNotAdoptForeignDeps scans .designs/**/*.md. A foreign
// Go module path may appear only on a line that cites ADR-013 — naming a
// package in order to reject it is the one legitimate reason to name it
// (see .designs/141/design-doc.md:120 for the healthy form).
func TestADR013DesignDocsDoNotAdoptForeignDeps(t *testing.T) {
	repoRoot := findRepoRoot(t)
	designs := filepath.Join(repoRoot, ".designs")
	if _, err := os.Stat(designs); os.IsNotExist(err) {
		t.Skip("no .designs directory in this checkout")
	}

	var violations []string
	walkErr := filepath.WalkDir(designs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || strings.ToLower(filepath.Ext(path)) != ".md" {
			return nil
		}
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(repoRoot, path)
		if historicalExemptFiles[filepath.ToSlash(rel)] {
			return nil
		}
		for i, line := range strings.Split(string(content), "\n") {
			if m := tableGrowthLanguage.FindString(line); m != "" {
				violations = append(violations,
					fmt.Sprintf("%s:%d: proposes growing ADR-013's approved dependency table (%q)", rel, i+1, m))
			}
			for _, loc := range foreignModulePath.FindAllStringIndex(line, -1) {
				match := line[loc[0]:loc[1]]
				if loc[0] >= 3 && line[loc[0]-3:loc[0]] == "://" {
					continue
				}
				if loc[0] >= 1 && line[loc[0]-1] == '.' {
					continue // subdomain URL (gist.github.com, cli.github.com), not a module path
				}
				if isApprovedModule(match) {
					continue
				}
				if versionPin.MatchString(line[loc[1]:]) {
					violations = append(violations,
						fmt.Sprintf("%s:%d: version-pinned foreign module %q (adoption syntax)", rel, i+1, match))
					continue
				}
				if strings.Contains(line, "ADR-013") {
					continue
				}
				violations = append(violations,
					fmt.Sprintf("%s:%d: foreign Go module path %q without an ADR-013 citation on the line", rel, i+1, match))
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walking .designs: %v", walkErr)
	}

	if len(violations) > 0 {
		t.Errorf("ADR-013: design documents adopt or drift toward foreign Go dependencies:\n  %s\n\n"+
			"New go.mod dependencies are an operator decision (see %s).\n"+
			"A design may name a foreign module only to reject it, citing ADR-013 on the same line.\n"+
			"If the requirements appear to need a new dependency: stop and escalate to the\n"+
			"operator — do not adopt it, and do not draft an ADR change.",
			strings.Join(violations, "\n  "), adr013RelPath)
	}
}

// TestADR013GrandfatheredTableNotGrown pins ADR-013's grandfathered
// direct-require table. The ADR itself says adding a row requires a new ADR
// or an amendment — and drafting that is an operator decision, not something
// an automated pipeline does on its own (PR #564 is the incident where one
// did). An agent hitting this failure must revert its ADR edit and escalate
// to the operator instead. The operator growing the table updates the
// expectations here in the same commit as the ADR.
func TestADR013GrandfatheredTableNotGrown(t *testing.T) {
	repoRoot := findRepoRoot(t)
	content, err := os.ReadFile(filepath.Join(repoRoot, adr013RelPath))
	if err != nil {
		t.Fatalf("reading ADR-013: %v", err)
	}

	wantModules := approvedDirectRequires
	var gotRows []string
	for _, line := range strings.Split(string(content), "\n") {
		if strings.HasPrefix(line, "| `") {
			gotRows = append(gotRows, line)
		}
	}
	if len(gotRows) != len(wantModules) {
		t.Fatalf("ADR-013's grandfathered table has %d module rows, want %d.\n"+
			"Growing this table is an operator decision; automated agents must not add rows.\n"+
			"Revert the ADR edit and escalate to the operator instead.\nRows found:\n  %s",
			len(gotRows), len(wantModules), strings.Join(gotRows, "\n  "))
	}
	for i, mod := range wantModules {
		if !strings.Contains(gotRows[i], "`"+mod+"`") {
			t.Errorf("ADR-013 grandfathered table row %d = %q, want module %q", i+1, gotRows[i], mod)
		}
	}
}

// directRequires parses a go.mod file and returns the module paths of its
// non-indirect require entries. Versions are deliberately ignored: ADR-013
// scopes version bumps to separate supply-chain review, so only the set of
// module paths is pinned.
func directRequires(t *testing.T, gomodPath string) []string {
	t.Helper()
	content, err := os.ReadFile(gomodPath)
	if err != nil {
		t.Fatalf("reading %s: %v", gomodPath, err)
	}
	var paths []string
	inBlock := false
	for _, line := range strings.Split(string(content), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "require (":
			inBlock = true
			continue
		case inBlock && trimmed == ")":
			inBlock = false
			continue
		}
		var entry string
		if inBlock {
			entry = trimmed
		} else if rest, ok := strings.CutPrefix(trimmed, "require "); ok {
			entry = strings.TrimSpace(rest)
		} else {
			continue
		}
		if entry == "" || strings.HasPrefix(entry, "//") || strings.Contains(entry, "// indirect") {
			continue
		}
		if fields := strings.Fields(entry); len(fields) > 0 {
			paths = append(paths, fields[0])
		}
	}
	return paths
}

// TestGoModDirectRequireSetPinned pins the direct-require set of every go.mod in
// the repository to ADR-013's grandfathered set. Unlike the design-doc tripwire
// above (the early warning), this guards the property itself: a dependency added
// by any path at all — a free-form dispatch running `go get`, `go mod tidy`
// drift, a phase with no design doc — fails `make test` here. Growing the set is
// an operator decision made by amending ADR-013 and this file's
// approvedDirectRequires in the same commit.
func TestGoModDirectRequireSetPinned(t *testing.T) {
	repoRoot := findRepoRoot(t)

	got := directRequires(t, filepath.Join(repoRoot, "go.mod"))
	want := map[string]bool{}
	for _, mod := range approvedDirectRequires {
		want[mod] = true
	}
	for _, mod := range got {
		if !want[mod] {
			t.Errorf("go.mod carries direct require %q, which is not in ADR-013's grandfathered set.\n"+
				"New go.mod dependencies are an operator decision (see %s).\n"+
				"If you are an automated agent: revert this change and escalate to the operator —\n"+
				"do not adopt the dependency, and do not draft an ADR change.", mod, adr013RelPath)
		}
		delete(want, mod)
	}
	for mod := range want {
		t.Errorf("go.mod is missing grandfathered direct require %q — the approved set and go.mod have drifted", mod)
	}

	if webGot := directRequires(t, filepath.Join(repoRoot, "web", "go.mod")); len(webGot) > 0 {
		t.Errorf("web/go.mod carries direct requires %v; its approved set is empty.\n"+
			"New dependencies there are likewise an operator decision (see %s).", webGot, adr013RelPath)
	}
}
