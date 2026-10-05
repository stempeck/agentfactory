//go:build !integration

package tmux

import (
	"go/ast"
	"go/token"
	"strconv"
	"testing"
)

// TestGetPanePID_ExactSessionTarget pins the list-panes target to the "="+session+":" exact-match
// form. A bare target is a prefix match: with the service session gone, `af plugin check` would
// report the RSS of any other session whose name starts with it. "="+session alone is not enough:
// list-panes resolves -t as a window, and its fallback session lookup drops the exact flag, so only
// the trailing ":" puts the "=" on the session part. The guard makes a real exec impossible in unit
// tests, so the pin reads the call shape from the source.
func TestGetPanePID_ExactSessionTarget(t *testing.T) {
	fn, ok := tmuxFuncBodies(t)["GetPanePID"]
	if !ok || fn.Body == nil {
		t.Fatal("GetPanePID not found in the package's non-test sources")
	}
	var target ast.Expr
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "run" || len(call.Args) < 3 {
			return true
		}
		if lit, ok := call.Args[0].(*ast.BasicLit); !ok || lit.Value != strconv.Quote("list-panes") {
			return true
		}
		for i := 1; i+1 < len(call.Args); i++ {
			if lit, ok := call.Args[i].(*ast.BasicLit); ok && lit.Value == strconv.Quote("-t") {
				target = call.Args[i+1]
			}
		}
		return true
	})
	if target == nil {
		t.Fatal(`GetPanePID has no t.run("list-panes", ..., "-t", <target>, ...) call`)
	}
	outer, ok := target.(*ast.BinaryExpr)
	if !ok || outer.Op != token.ADD {
		t.Fatalf(`GetPanePID's list-panes target must be "="+session+":" (exact session match), got a %T`, target)
	}
	if lit, ok := outer.Y.(*ast.BasicLit); !ok || lit.Value != strconv.Quote(":") {
		t.Errorf(`GetPanePID's list-panes target must end with ":" so the "=" applies to the session, not a window`)
	}
	bin, ok := outer.X.(*ast.BinaryExpr)
	if !ok || bin.Op != token.ADD {
		t.Fatalf(`GetPanePID's list-panes target must be "="+session+":", got a %T before the ":"`, outer.X)
	}
	if lit, ok := bin.X.(*ast.BasicLit); !ok || lit.Value != strconv.Quote("=") {
		t.Errorf(`GetPanePID's list-panes target must start with the "=" exact-match prefix`)
	}
	if id, ok := bin.Y.(*ast.Ident); !ok || id.Name != "session" {
		t.Errorf(`GetPanePID's list-panes target must be "="+session+":"`)
	}
}
