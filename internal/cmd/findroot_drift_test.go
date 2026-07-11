package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolverCall is one config.FindFactoryRoot / config.FindLocalRoot call site
// discovered by the drift scan, attributed to its enclosing function.
type resolverCall struct {
	file  string // base name, e.g. "helpers.go"
	line  int
	label string // enclosing func name, or file base for a package-var closure
	fn    string // "FindFactoryRoot" or "FindLocalRoot"
}

// scanConfigResolvers parses every non-test .go file under dir and returns every
// config.FindFactoryRoot / config.FindLocalRoot call, attributed to its enclosing
// top-level func (or the file base name for a package-level var-initializer
// closure). Implemented with go/parser (not a per-line regex) so comment lines are
// excluded by construction and each call is keyed to its function.
func scanConfigResolvers(t *testing.T, dir string) []resolverCall {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	fset := token.NewFileSet()
	var calls []resolverCall
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		path := filepath.Join(dir, name)
		f, err := parser.ParseFile(fset, path, nil, 0) // mode 0: comments not attached to the AST
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		base := strings.TrimSuffix(name, ".go")
		for _, decl := range f.Decls {
			label := base
			if fd, ok := decl.(*ast.FuncDecl); ok {
				label = fd.Name.Name
			}
			ast.Inspect(decl, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				pkg, ok := sel.X.(*ast.Ident)
				if !ok || pkg.Name != "config" {
					return true
				}
				if fn := sel.Sel.Name; fn == "FindFactoryRoot" || fn == "FindLocalRoot" {
					calls = append(calls, resolverCall{
						file:  name,
						line:  fset.Position(call.Pos()).Line,
						label: label,
						fn:    fn,
					})
				}
				return true
			})
		}
	}
	return calls
}

// TestT_INT_4_FindRootResolversConfinedToSeam (#519) is the structural drift guard:
// a future verb that resolves the factory root directly — bypassing the
// resolveInvokerRoot chokepoint — must fail CI rather than silently re-open the
// nested-clone capture hole. Modeled on env_hermetic_test.go's source scans, but
// implemented with go/parser instead of a per-line regex so comment lines are
// excluded by construction and every call is attributed to its enclosing function.
//
// It covers internal/cmd (allowlisted resolvers only) AND the two library seams the
// #519 review pulled under the guard — internal/mail and internal/formula — which
// must NEVER resolve the root ambiently: mail.NewRouter and formula.FindFormulaFile
// now take an already-validated root from the cmd layer, so ANY config.FindFactoryRoot
// / config.FindLocalRoot call in those packages is a violation (review follow-up,
// thread 7a). Previously those seams were shielded only by call-order luck.
//
// Allowlists are FUNCTION-scoped: a call inside a named top-level func is keyed by
// that func's name; a call inside a package-level var-initializer closure (the
// rootSubCmd cobra RunE at root_cmd.go:18, which has no enclosing FuncDecl) is keyed
// by the file base name ("root_cmd").
func TestT_INT_4_FindRootResolversConfinedToSeam(t *testing.T) {
	root := findModuleRoot(t)

	// config.FindFactoryRoot: only the invoker seam, the watchdog's deliberate
	// AF_ROOT-first resolver, and the containment NOTIFICATION-routing resolver may
	// call it. resolveInvokerRoot calls it twice (cwd + AF_ROOT); both are inside the
	// function, so the single allowlist entry covers them.
	factoryAllow := map[string]bool{
		"resolveInvokerRoot":     true,
		"resolveWatchdogRoot":    true,
		"containmentRoutingRoot": true, // containment.go — AF_ROOT-first routing of the containment mail (thread 7b); routes by session identity, never decides the escape boundary
	}
	// config.FindLocalRoot: the five identity/containment/local-root readers (NOT
	// invoker-root binding). Peer-review-corrected 5-entry set — the outline body's
	// 4-entry list omitted runContainmentCheckCore and would fail on day one.
	localAllow := map[string]bool{
		"resolveAgentName":        true, // helpers.go — identity read
		"primeAgent":              true, // prime.go — worktree RootDir read
		"runContainmentCheckCore": true, // containment.go — containment-boundary fallback (D-5 class)
		"resolveBoundary":         true, // containment.go — containment-boundary read
		"root_cmd":                true, // root_cmd.go rootSubCmd closure — `af root` prints the LOCAL root
	}

	var violations []string
	var sawFactory, sawLocal bool

	// --- internal/cmd: allowlisted resolvers only ---
	for _, c := range scanConfigResolvers(t, filepath.Join(root, "internal", "cmd")) {
		switch c.fn {
		case "FindFactoryRoot":
			sawFactory = true
			if !factoryAllow[c.label] {
				violations = append(violations, fmt.Sprintf(
					"internal/cmd/%s:%d: config.FindFactoryRoot called in %q — not in the invoker-seam allowlist {resolveInvokerRoot, resolveWatchdogRoot, containmentRoutingRoot}",
					c.file, c.line, c.label))
			}
		case "FindLocalRoot":
			sawLocal = true
			if !localAllow[c.label] {
				violations = append(violations, fmt.Sprintf(
					"internal/cmd/%s:%d: config.FindLocalRoot called in %q — not in the local-root allowlist",
					c.file, c.line, c.label))
			}
		}
	}

	// Guard against a scan that silently matches nothing (e.g. a package-name refactor
	// making the SelectorExpr check dead): the invariant is only meaningful if the scan
	// actually observed both call families it constrains.
	if !sawFactory {
		t.Fatal("drift scan found zero config.FindFactoryRoot calls in internal/cmd — the scan is not matching (guards nothing)")
	}
	if !sawLocal {
		t.Fatal("drift scan found zero config.FindLocalRoot calls in internal/cmd — the scan is not matching (guards nothing)")
	}

	// --- internal/mail + internal/formula: library seams take an explicit root ---
	// ANY ambient config.FindFactoryRoot / config.FindLocalRoot in these packages
	// re-opens the laundering hole thread 7a closed, with zero CI signal otherwise.
	for _, pkg := range []string{"mail", "formula"} {
		for _, c := range scanConfigResolvers(t, filepath.Join(root, "internal", pkg)) {
			violations = append(violations, fmt.Sprintf(
				"internal/%s/%s:%d: config.%s called in %q — internal/%s seams must receive an ALREADY-VALIDATED root from the cmd layer, never resolve it ambiently (#519 review, thread 7a)",
				pkg, c.file, c.line, c.fn, c.label, pkg))
		}
	}

	if len(violations) > 0 {
		t.Errorf("factory-root resolution escaped the resolveInvokerRoot chokepoint (#519).\n"+
			"State-writing verbs MUST call resolveInvokerRoot(wd), never config.FindFactoryRoot directly; "+
			"config.FindLocalRoot is limited to identity/containment/local-root readers; and internal/mail + "+
			"internal/formula must never resolve ambiently at all. If you added a legitimate new resolver, "+
			"extend the allowlist in this test with a one-line rationale.\n"+
			"Violations:\n  %s", strings.Join(violations, "\n  "))
	}
}
