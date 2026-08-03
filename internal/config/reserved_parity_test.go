package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"
)

// TestReservedNamesParity pins C-1 (PR #553): the reservedNames map here and its hand-maintained
// mirror in web/internal/exec/validate.go must stay identical. The web console is a SEPARATE Go
// module (its own go.mod + internal/ seal), so a single test binary cannot import both maps — the
// mirror is read from SOURCE via go/parser, exactly as the cross-file drift tests (e.g.
// kill_scope_drift_test.go) compare a value across a boundary they cannot import. A future reserved
// name added on one side and forgotten on the other now fails this test, instead of silently letting
// the web console accept a name the CLI rejects.
func TestReservedNamesParity(t *testing.T) {
	webKeys := reservedNamesFromSource(t, webValidateGoPath(t))
	if len(webKeys) == 0 {
		t.Fatal("parsed zero reservedNames keys from web validate.go — the scan matches nothing, so it guards nothing")
	}

	for name := range reservedNames {
		if !webKeys[name] {
			t.Errorf("reserved name %q is in internal/config but MISSING from web/internal/exec/validate.go — the mirrored maps have diverged", name)
		}
	}
	for name := range webKeys {
		if !reservedNames[name] {
			t.Errorf("reserved name %q is in web/internal/exec/validate.go but MISSING from internal/config — the mirrored maps have diverged", name)
		}
	}
}

// webValidateGoPath resolves the on-disk path of the web module's validate.go relative to THIS test
// file (via runtime.Caller), so it is independent of the test's working directory.
func webValidateGoPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed — cannot locate this test's directory")
	}
	// this file: <root>/internal/config/reserved_parity_test.go  →  <root>/web/internal/exec/validate.go
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "web", "internal", "exec", "validate.go")
}

// reservedNamesFromSource go/parser-parses the named .go file and returns the KEY SET of its
// top-level `reservedNames` map[string]bool composite literal. Reading source (not importing) is the
// only way to reach the value across the web module's separate go.mod + internal/ seal.
func reservedNamesFromSource(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	keys := map[string]bool{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if name.Name != "reservedNames" || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					continue
				}
				for _, elt := range lit.Elts {
					kv, ok := elt.(*ast.KeyValueExpr)
					if !ok {
						continue
					}
					bl, ok := kv.Key.(*ast.BasicLit)
					if !ok || bl.Kind != token.STRING {
						continue
					}
					s, err := strconv.Unquote(bl.Value)
					if err != nil {
						t.Fatalf("unquote key %q: %v", bl.Value, err)
					}
					keys[s] = true
				}
			}
		}
	}
	return keys
}
