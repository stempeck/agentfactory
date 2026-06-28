package server

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// rootInternalPrefix is the ROOT af module's sealed import prefix the web module must NEVER import.
// Assembled from fragments so this file's raw bytes never carry the contiguous literal as an import
// path (defence-in-depth; the go/parser walk inspects import specs only, so a plain string constant
// here is never mistaken for an import anyway).
var rootInternalPrefix = "github.com/stempeck/agentfactory" + "/internal/"

// TestExtractability_OwnModule_NoInternalImport — AC #3. Walks every .go source under the web
// module and asserts none imports the ROOT af module's internal/ packages. The Go internal seal +
// the separate web go.mod (no require on root) already make such an import compile-impossible; this
// test makes the guarantee explicit and CI-visible under the new web-unit job. The web module's OWN
// internal (github.com/stempeck/agentfactory-web/internal/…) is allowed and must NOT match — note
// the char after "agentfactory" is "-" there, not "/", so the fully-qualified prefix excludes it.
func TestExtractability_OwnModule_NoInternalImport(t *testing.T) {
	moduleRoot := filepath.Join("..", "..") // web/internal/server -> web
	fset := token.NewFileSet()
	var offenders []string

	err := filepath.WalkDir(moduleRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() { // skip non-source dirs that may appear under the module root
			case ".gotmp", "vendor", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		f, perr := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if perr != nil {
			return perr
		}
		for _, imp := range f.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if strings.HasPrefix(p, rootInternalPrefix) {
				offenders = append(offenders, path+" imports "+p)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking web module: %v", err)
	}
	if len(offenders) > 0 {
		t.Errorf("web module must not import the root af module's internal/ packages (own-module decoupling broken):\n%s", strings.Join(offenders, "\n"))
	}

	// Belt-and-suspenders: web/go.mod must declare the own module and carry NO require on the root
	// af module (the compiler-enforced half of the decoupling).
	gomod, err := os.ReadFile(filepath.Join(moduleRoot, "go.mod"))
	if err != nil {
		t.Fatalf("read web/go.mod: %v", err)
	}
	src := string(gomod)
	if !strings.Contains(src, "module github.com/stempeck/agentfactory-web") {
		t.Errorf("web/go.mod is not the expected own module:\n%s", src)
	}
	// A require on the root module would name "github.com/stempeck/agentfactory " followed by a
	// version (space-delimited) — the trailing space distinguishes it from "agentfactory-web".
	if strings.Contains(src, "github.com/stempeck/agentfactory v") ||
		strings.Contains(src, "require github.com/stempeck/agentfactory ") {
		t.Errorf("web/go.mod must not require the root af module:\n%s", src)
	}
}

// TestExtractability_MatcherNonVacuous proves the prefix check is non-vacuous and does not
// false-positive on the web module's OWN internal path (mirrors lint_test.go's SelfNegative).
func TestExtractability_MatcherNonVacuous(t *testing.T) {
	forbidden := "github.com/stempeck/agentfactory" + "/internal/lock"
	if !strings.HasPrefix(forbidden, rootInternalPrefix) {
		t.Errorf("matcher failed to flag a root-internal import %q", forbidden)
	}
	allowed := "github.com/stempeck/agentfactory-web" + "/internal/server"
	if strings.HasPrefix(allowed, rootInternalPrefix) {
		t.Errorf("matcher false-positived on the web module's own internal %q", allowed)
	}
	if strings.HasPrefix("net/http", rootInternalPrefix) {
		t.Errorf("matcher false-positived on a stdlib import")
	}
}
