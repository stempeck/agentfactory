package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

// TestT_INT_5_RegressionFlows (#519 AC-4) pins the four legitimate C-1 flows that must
// keep working AFTER the resolveInvokerRoot seam is in place — the guard that the
// refusal did not over-reach into normal use:
//
//	(a) worktree-agent dir with AF_ROOT=outer  -> resolves outer via redirect, proceeds
//	(b) factory-on-itself root shell           -> AF_ROOT set and unset both proceed
//	(c) redirect-chain resolution unchanged     -> AF_ROOT itself a worktree redirect, proceeds
//	(d) af install --init                       -> never resolves the invoker root (bootstrap unaffected)
func TestT_INT_5_RegressionFlows(t *testing.T) {
	fx := buildNestedFactoryFixture(t)

	// (a) Worktree agent dir: cwd redirects to outer, session AF_ROOT is outer.
	t.Run("a/worktree-agent-dir AF_ROOT=outer proceeds to outer", func(t *testing.T) {
		t.Setenv("AF_ROOT", fx.outer)
		got, err := resolveInvokerRoot(fx.worktree)
		if err != nil {
			t.Fatalf("worktree-agent flow must proceed, got error: %v", err)
		}
		if got != fx.outer {
			t.Errorf("got %q, want outer %q (redirect ⇒ outer, matches AF_ROOT)", got, fx.outer)
		}
	})

	// (b) Factory-on-itself root shell, AF_ROOT set and unset.
	t.Run("b/factory-on-itself AF_ROOT=outer proceeds", func(t *testing.T) {
		t.Setenv("AF_ROOT", fx.outer)
		got, err := resolveInvokerRoot(fx.outer)
		if err != nil || got != fx.outer {
			t.Fatalf("root shell (AF_ROOT set) must resolve outer; got %q err %v", got, err)
		}
	})
	t.Run("b/factory-on-itself AF_ROOT unset proceeds", func(t *testing.T) {
		got, err := resolveInvokerRoot(fx.outer)
		if err != nil || got != fx.outer {
			t.Fatalf("root shell (AF_ROOT unset) must resolve outer; got %q err %v", got, err)
		}
	})

	// (c) Redirect chain: AF_ROOT itself points at a worktree that redirects to outer.
	// This exercises the FULL-resolve-AF_ROOT branch (afweb rationale): the env root is
	// resolved through its .factory-root before comparison, so it still matches.
	t.Run("c/redirect-chain AF_ROOT=worktree(->outer), cwd=outer proceeds", func(t *testing.T) {
		t.Setenv("AF_ROOT", fx.worktree)
		got, err := resolveInvokerRoot(fx.outer)
		if err != nil {
			t.Fatalf("redirect-chain AF_ROOT must full-resolve and match, got error: %v", err)
		}
		if got != fx.outer {
			t.Errorf("got %q, want outer %q", got, fx.outer)
		}
	})
	t.Run("c/redirect resolution unchanged, cwd=worktree AF_ROOT unset", func(t *testing.T) {
		got, err := resolveInvokerRoot(fx.worktree)
		if err != nil || got != fx.outer {
			t.Fatalf("redirect from worktree must resolve outer; got %q err %v", got, err)
		}
	})

	// (d) af install --init never resolves the invoker root. runInstallInit is
	// integration-tier (it spawns the Python MCP server), so — following the established
	// source-parse-install.go idiom — this asserts STRUCTURALLY that its body calls
	// neither resolveInvokerRoot nor config.FindFactoryRoot. Because init never routes
	// through the seam, AF_ROOT (scrubbed OR set-to-elsewhere) cannot make bootstrap
	// refuse — the flow-(d) guarantee.
	t.Run("d/install --init never resolves the invoker root", func(t *testing.T) {
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, "install.go", nil, 0)
		if err != nil {
			t.Fatalf("parse install.go: %v", err)
		}
		var fn *ast.FuncDecl
		for _, d := range f.Decls {
			if fd, ok := d.(*ast.FuncDecl); ok && fd.Name.Name == "runInstallInit" {
				fn = fd
				break
			}
		}
		if fn == nil {
			t.Fatal("runInstallInit not found in install.go (flow-d guard cannot run)")
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			switch callee := call.Fun.(type) {
			case *ast.Ident:
				if callee.Name == "resolveInvokerRoot" {
					t.Errorf("runInstallInit must NOT call resolveInvokerRoot — init never resolves the invoker root (#519 flow d)")
				}
			case *ast.SelectorExpr:
				if x, ok := callee.X.(*ast.Ident); ok && x.Name == "config" && callee.Sel.Name == "FindFactoryRoot" {
					t.Errorf("runInstallInit must NOT call config.FindFactoryRoot — init never resolves (#519 flow d)")
				}
			}
			return true
		})
	})
}
