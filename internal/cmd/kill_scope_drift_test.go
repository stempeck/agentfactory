package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"regexp"
	"strconv"
	"testing"

	"github.com/stempeck/agentfactory/internal/session"
)

// firstDefinedString returns the value of the first `<name> := "..."` short variable
// declaration (a `:=` whose sole right-hand side is a string literal) in the Go source
// file at path. It parses with go/parser in mode 0 so commented-out or docstring text is
// excluded by construction. ok is false when no such assignment exists — the drift-scan
// sentinel that makes a renamed/moved value fail loudly rather than pass vacuously.
func firstDefinedString(t *testing.T, path, name string) (val string, ok bool) {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	ast.Inspect(f, func(n ast.Node) bool {
		if ok {
			return false
		}
		asn, isAsn := n.(*ast.AssignStmt)
		if !isAsn || asn.Tok != token.DEFINE || len(asn.Lhs) != 1 || len(asn.Rhs) != 1 {
			return true
		}
		id, isID := asn.Lhs[0].(*ast.Ident)
		if !isID || id.Name != name {
			return true
		}
		lit, isLit := asn.Rhs[0].(*ast.BasicLit)
		if !isLit || lit.Kind != token.STRING {
			return true
		}
		if s, uerr := strconv.Unquote(lit.Value); uerr == nil {
			val, ok = s, true
			return false
		}
		return true
	})
	return val, ok
}

// stringLiteralsInFunc returns every string-literal value inside the top-level function
// funcName in the Go source file at path (go/parser mode 0, so comments are excluded). It
// reads the "af-"/"af-test-" literals hardcoded inside tmux.isProductionIdentity without
// importing that unexported function (internal/tmux exports no equivalent symbol).
func stringLiteralsInFunc(t *testing.T, path, funcName string) []string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var lits []string
	for _, decl := range f.Decls {
		fd, isFunc := decl.(*ast.FuncDecl)
		if !isFunc || fd.Name.Name != funcName {
			continue
		}
		ast.Inspect(fd, func(n ast.Node) bool {
			if lit, ok := n.(*ast.BasicLit); ok && lit.Kind == token.STRING {
				if s, err := strconv.Unquote(lit.Value); err == nil {
					lits = append(lits, s)
				}
			}
			return true
		})
	}
	return lits
}

// TestKillScopeDrift pins the three hand-coupled kill-scope values that six_sigma_gaps.md
// Gap 8 flagged as silent-drift risks. They are not linked by any production code path and
// already mutated once together (edaecb2c); this test IS the enforcing link. It lives in
// package cmd because only cmd imports BOTH internal/session (the launch string + Prefix)
// and internal/tmux (the isProductionIdentity prefix literal) — session imports tmux, so an
// import cycle forbids a single shared const, which is why the design pins them with a drift
// test rather than extracting a constant.
//
// It fails red if ANY one value is changed in isolation:
//   - session/session.go   launch command  ("claude --dangerously-skip-permissions")
//   - internal/cmd/down.go orphan-cleanup regex ("claude.*--dangerously-skip-permissions")
//   - internal/session/names.go Prefix ("af-"), mirrored by tmux.isProductionIdentity
func TestKillScopeDrift(t *testing.T) {
	root := findModuleRoot(t)

	launch, ok := firstDefinedString(t, filepath.Join(root, "internal", "session", "session.go"), "claude")
	if !ok {
		t.Fatal(`drift scan found no 'claude := "..."' in internal/session/session.go — the scan matches nothing and guards nothing`)
	}
	pattern, ok := firstDefinedString(t, filepath.Join(root, "internal", "cmd", "down.go"), "pattern")
	if !ok {
		t.Fatal(`drift scan found no 'pattern := "..."' in internal/cmd/down.go — the scan matches nothing and guards nothing`)
	}

	// Coupling 1: the orphan-cleanup regex must still match the launch command it is meant
	// to reap. The two are NOT byte-identical (one is a command, one a regex derived from
	// it); the invariant is that the regex still matches the command.
	re, err := regexp.Compile(pattern)
	if err != nil {
		t.Fatalf("orphan-cleanup pattern %q is not a valid regexp: %v", pattern, err)
	}
	if !re.MatchString(launch) {
		t.Errorf("kill-scope drift: orphan-cleanup regex %q no longer matches the launch command %q.\n"+
			"session.go's launch command and down.go's cleanup regex must stay in lockstep (six_sigma_gaps.md Gap 8).",
			pattern, launch)
	}

	// Coupling 2: isProductionIdentity hardcodes the session prefix instead of referencing
	// session.Prefix (an import cycle forbids the reference), so the two must be asserted to
	// agree here. session.Prefix is read as the exported symbol; the tmux.go literal is read
	// from source.
	lits := stringLiteralsInFunc(t, filepath.Join(root, "internal", "tmux", "tmux.go"), "isProductionIdentity")
	if len(lits) == 0 {
		t.Fatal("drift scan found no string literals in tmux.isProductionIdentity — the scan matches nothing and guards nothing")
	}
	found := false
	for _, l := range lits {
		if l == session.Prefix {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("kill-scope drift: session.Prefix = %q is not among the literals hardcoded in tmux.isProductionIdentity (%q).\n"+
			"names.go's Prefix and tmux.go's isProductionIdentity prefix must stay in lockstep (six_sigma_gaps.md Gap 8).",
			session.Prefix, lits)
	}

	// Coupling 3 (PR 544 N1): the "af-test-" hermetic-namespace exclusion is a raw literal duplicated in
	// BOTH tmux.isProductionIdentity AND its cmd-package reimplementation cmd.isAfProductionSession
	// (authority.go) — neither can reference a shared constant (import cycle / unexported symbol). A
	// rename of the hermetic prefix must be caught here, or the two production-session classifiers
	// silently diverge. Coupling 1/2 above pin only the "af-" half and only tmux.go; this pins "af-test-"
	// in both functions.
	const hermeticPrefix = "af-test-"
	for _, fn := range []struct{ path, name string }{
		{filepath.Join(root, "internal", "tmux", "tmux.go"), "isProductionIdentity"},
		{filepath.Join(root, "internal", "cmd", "authority.go"), "isAfProductionSession"},
	} {
		fnLits := stringLiteralsInFunc(t, fn.path, fn.name)
		has := false
		for _, l := range fnLits {
			if l == hermeticPrefix {
				has = true
				break
			}
		}
		if !has {
			t.Errorf("kill-scope drift: %s no longer hardcodes the hermetic test prefix %q (literals: %q).\n"+
				"tmux.isProductionIdentity and cmd.isAfProductionSession must exclude the SAME hermetic namespace, "+
				"or the production-session classifier diverges (six_sigma_gaps.md Gap 8; PR 544 N1).",
				fn.name, hermeticPrefix, fnLits)
		}
	}
}
