//go:build !integration

package tmux

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// TestNewSessionWithCommand_GuardedNoServerOptions pins the integration-service launcher (spec
// L262-266; design L463): NewSessionWithCommand is guarded exactly like NewSession (a
// production-identity target panics under the default-build guard; an af-test-* target is an
// inert no-op), and it never touches server- or global-scope tmux state — a service session must
// not re-apply the agent-session history-limit/mouse/clipboard/wheel settings server-wide.
func TestNewSessionWithCommand_GuardedNoServerOptions(t *testing.T) {
	t.Run("af_production_identity_panics", func(t *testing.T) {
		tx := NewTmux()
		msg, panicked := capturePanic(func() { _ = tx.NewSessionWithCommand("af-x", "", "sleep 1") })
		if !panicked {
			t.Fatal(`NewSessionWithCommand("af-x") did not panic under the default-build guard; it must call guardOp("new-session", name) first`)
		}
		if !strings.Contains(msg, "new-session") || !strings.Contains(msg, "af-x") {
			t.Errorf("guard panic must name the op new-session and the target af-x:\n%s", msg)
		}
	})

	t.Run("af_test_namespace_inert", func(t *testing.T) {
		ResetRealOpCounter()
		tx := NewTmux()
		var err error
		msg, panicked := capturePanic(func() { err = tx.NewSessionWithCommand("af-test-ab12cd34-svc", "", "sleep 1") })
		if panicked {
			t.Fatalf("af-test-* target must be an inert no-op, got panic:\n%s", msg)
		}
		if err != nil {
			t.Errorf("af-test-* target must return nil under the guard, got %v", err)
		}
		if n := ProductionRealOpCount(); n != 0 {
			t.Errorf("production real-op count = %d, want 0", n)
		}
	})

	t.Run("body_scan", func(t *testing.T) {
		bodies := tmuxFuncBodies(t)
		fn, ok := bodies["NewSessionWithCommand"]
		if !ok || fn.Body == nil {
			t.Fatal("NewSessionWithCommand not found in the package's non-test sources")
		}
		if len(fn.Body.List) == 0 || !stmtCallsGuardNewSession(fn.Body.List[0]) {
			t.Error(`NewSessionWithCommand's first statement must be the guardOp("new-session", name) early return (same placement pin as NewSession)`)
		}

		// Transitive package-local closure: no path may reach the server/global setters, or
		// NewSession (which applies them).
		forbidden := map[string]bool{"SetGlobalOption": true, "SetServerOption": true, "BindKey": true, "NewSession": true}
		seen := map[string]bool{}
		var walk func(name, path string)
		walk = func(name, path string) {
			if seen[name] {
				return
			}
			seen[name] = true
			d, ok := bodies[name]
			if !ok || d.Body == nil {
				return
			}
			ast.Inspect(d.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var callee string
				switch f := call.Fun.(type) {
				case *ast.Ident:
					callee = f.Name
				case *ast.SelectorExpr:
					callee = f.Sel.Name
				}
				if callee == "" {
					return true
				}
				if forbidden[callee] {
					t.Errorf("NewSessionWithCommand reaches %s via %s -> %s; a service session must not set server/global tmux state", callee, path, callee)
				}
				walk(callee, path+" -> "+callee)
				return true
			})
		}
		walk("NewSessionWithCommand", "NewSessionWithCommand")

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			bl, ok := n.(*ast.BasicLit)
			if !ok || bl.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(bl.Value)
			if err != nil {
				return true
			}
			bad := s == "-g"
			for _, opt := range []string{"history-limit", "mouse", "set-clipboard"} {
				bad = bad || strings.Contains(s, opt)
			}
			if bad {
				t.Errorf("NewSessionWithCommand spells %q; it must not apply global/server options", s)
			}
			return true
		})
	})
}

// tmuxFuncBodies parses the package's non-test sources and indexes every function and method
// declaration by bare name (methods and functions share one namespace here; tmux has no
// same-named pair).
func tmuxFuncBodies(t *testing.T) map[string]*ast.FuncDecl {
	t.Helper()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}
	fset := token.NewFileSet()
	out := map[string]*ast.FuncDecl{}
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, n, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", n, err)
		}
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok {
				out[fd.Name.Name] = fd
			}
		}
	}
	if _, ok := out["NewSession"]; !ok {
		t.Fatal("control: NewSession not found; the source scan is reading the wrong files")
	}
	return out
}

// stmtCallsGuardNewSession reports whether s contains a call guardOp("new-session", ...).
func stmtCallsGuardNewSession(s ast.Stmt) bool {
	found := false
	ast.Inspect(s, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "guardOp" || len(call.Args) == 0 {
			return true
		}
		if bl, ok := call.Args[0].(*ast.BasicLit); ok && bl.Value == `"new-session"` {
			found = true
		}
		return true
	})
	return found
}
