package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestDerefSites_ExactlyFourPermitted is the first mechanical enforcement the deref rule has ever
// had. Until now the rule at telemetry_secret.go:39-57 was prose, and prose drifted: the tree
// carried "of the four sites that load" while five sites loaded, because a phase added one and the
// comment did not move.
//
// The name says Four and the set below has five entries. That is deliberate and it is the honest
// reading: the name is fixed by the acceptance criteria, and renaming it would break the contract
// that names it, so the NAME is a label while the ASSERTION is the truth. Asserting the exact set
// rather than a cardinality is what makes that survivable — a bare count would have to be wrong
// either way, and a wrong number is exactly the defect this test exists to prevent.
func TestDerefSites_ExactlyFourPermitted(t *testing.T) {
	// Named by the function that performs the deref, not by line number. Line numbers are what
	// went stale in the prose rule; a function name survives every edit that does not move the
	// call, and a call that moves to a different function IS the change worth failing on.
	permitted := map[string]string{
		"exportTelemetryBacklog": "telemetry.go — the operator-invoked backlog drain",
		"printTelemetryStatus":   "telemetry.go — resolves before probing the backend",
		"drainTelemetryBounded":  "telemetry_record.go — the af done hook path",
		"telemetryStateDTO":      "telemetry_json.go — the machine-readable status surface",
		"telemetryUsageDTO":      "telemetry_usage.go — the backend query command",
	}

	found := derefCallSites(t)

	for name, why := range permitted {
		if _, ok := found[name]; !ok {
			t.Errorf("permitted deref site %q (%s) performs no derefTelemetryHeaders call.\n"+
				"Either the site was removed — in which case delete it from this set and from the "+
				"rule comment in telemetry_secret.go — or the deref moved somewhere this test "+
				"cannot see, which is the drift the rule exists to prevent.", name, why)
		}
	}
	for name, where := range found {
		if _, ok := permitted[name]; !ok {
			t.Errorf("UNPERMITTED deref site: %s dereferences a telemetry credential at %s.\n"+
				"Dereferencing is a deliberate act by a caller that knows the factory root "+
				"(telemetry_secret.go:39-57). A new site is not forbidden — it is forbidden to be "+
				"SILENT. Add it to this set and amend the rule comment's arithmetic in the same "+
				"change, or the comment becomes a record that lies.", name, where)
		}
	}
}

// TestDerefSites_SessionLaunchNeverDerefs is the other half of the rule, and the half with teeth:
// the launch path hands the file: reference through verbatim so the pane shell reads the secret at
// exec time and the plaintext never lands in a tmux environment listing.
//
// Asserted structurally rather than by grepping the file, so it stays true if telemetryLaunchEnv
// moves to another file.
func TestDerefSites_SessionLaunchNeverDerefs(t *testing.T) {
	found := derefCallSites(t)
	if where, bad := found["telemetryLaunchEnv"]; bad {
		t.Fatalf("telemetryLaunchEnv dereferences a telemetry credential at %s.\n"+
			"It must NOT resolve anything: the launch plane passes the file: reference through so "+
			"$(cat ...) reads it inside the pane. Resolving here puts the plaintext into a tmux "+
			"environment listing — the exact regression telemetry_secret.go:48-54 names.", where)
	}
}

// derefCallSites maps each non-test internal/cmd function that calls derefTelemetryHeaders to the
// file:line of its call. AST rather than grep, matching countCallsTo (telemetry_lifecycle_test.go),
// so a mention in a comment or a string literal is not mistaken for a call.
func derefCallSites(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}

	sites := map[string]string{}
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++

		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", name, parseErr)
		}

		// The whole file, not only its FuncDecls. A package-level `var x = func(...) {...}` holds
		// real, reachable code that no FuncDecl walk can see — telemetryQueryDo is exactly that
		// shape — so a scan restricted to declared functions would report a clean tree while a
		// deref sat in a function literal beside it.
		for _, decl := range file.Decls {
			owner := declOwnerName(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				if ident, isIdent := call.Fun.(*ast.Ident); isIdent && ident.Name == "derefTelemetryHeaders" {
					sites[owner] = filepath.Join(name) + ":" + itoa(fset.Position(call.Pos()).Line)
				}
				return true
			})
		}

		// Belt and braces on the attribution itself: count every call in the file independently and
		// require the walk above to have named all of them. Anything this scan cannot attribute is
		// reported rather than silently dropped, because a deref the enumeration cannot see is
		// precisely the drift the rule exists to prevent.
		total := 0
		ast.Inspect(file, func(n ast.Node) bool {
			if call, isCall := n.(*ast.CallExpr); isCall {
				if ident, isIdent := call.Fun.(*ast.Ident); isIdent && ident.Name == "derefTelemetryHeaders" {
					total++
				}
			}
			return true
		})
		if total > 0 && countSitesIn(sites, name) == 0 {
			t.Errorf("%s contains %d derefTelemetryHeaders call(s) that this scan could not "+
				"attribute to any declaration", name, total)
		}
	}

	// Without this the whole test passes vacuously against a directory that failed to list, or a
	// build tag that hid every file.
	if scanned == 0 {
		t.Fatal("scanned no non-test .go files — the enumeration proves nothing")
	}
	if len(sites) == 0 {
		t.Fatalf("scanned %d files and found no derefTelemetryHeaders call at all; either the "+
			"helper was renamed or this scan is broken", scanned)
	}
	return sites
}

// declOwnerName names whatever a call sits inside, so a function literal assigned to a package-level
// var is attributed to that var rather than vanishing.
func declOwnerName(decl ast.Decl) string {
	switch d := decl.(type) {
	case *ast.FuncDecl:
		return d.Name.Name
	case *ast.GenDecl:
		for _, spec := range d.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok || len(vs.Names) == 0 {
				continue
			}
			return vs.Names[0].Name
		}
	}
	return "<unattributed declaration>"
}

func countSitesIn(sites map[string]string, file string) int {
	n := 0
	for _, where := range sites {
		if strings.HasPrefix(where, file+":") {
			n++
		}
	}
	return n
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// TestDerefRuleCommentRecordsItsOwnArithmetic closes the loop the prose rule never had: the comment
// claims to be the record of which sites deref, so the numbers in it are checked against the tree
// rather than trusted.
//
// This is the test that would have caught the drift on the tree it was written against, where the
// comment said "four sites that load" and five sites loaded.
func TestDerefRuleCommentRecordsItsOwnArithmetic(t *testing.T) {
	src, err := os.ReadFile("telemetry_secret.go")
	if err != nil {
		t.Fatalf("reading telemetry_secret.go: %v", err)
	}
	comment := string(src)

	loading := loadCallSites(t)
	deref := derefCallSites(t)

	wantLoading := numberWord(len(loading))
	wantDeref := numberWord(len(deref))

	if !strings.Contains(comment, wantLoading+" sites that load") {
		t.Errorf("the deref rule does not state the true number of loading sites.\n"+
			"  measured: %d sites call config.LoadTelemetryConfig (%s)\n"+
			"  expected the comment to contain: %q\n"+
			"A rule that claims to be the record while being internally false is worse than no "+
			"rule, because it is trusted.", len(loading), sortedKeysOf(loading), wantLoading+" sites that load")
	}
	if !strings.Contains(comment, wantDeref+" sites are deref-permitted") {
		t.Errorf("the deref rule does not state the true number of deref-permitted sites.\n"+
			"  measured: %d sites call derefTelemetryHeaders (%s)\n"+
			"  expected the comment to contain: %q",
			len(deref), sortedKeysOf(deref), wantDeref+" sites are deref-permitted")
	}
	if strings.Contains(comment, "four sites that load") && len(loading) != 4 {
		t.Errorf("the stale arithmetic 'four sites that load' survives while %d sites load", len(loading))
	}
}

// loadCallSites is derefCallSites' sibling for config.LoadTelemetryConfig, which is a selector
// expression rather than a bare identifier.
func loadCallSites(t *testing.T) map[string]string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("reading package dir: %v", err)
	}

	sites := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parsing %s: %v", name, parseErr)
		}
		// Whole-file, for the same reason derefCallSites is: a loader hidden in a package-level
		// function literal would otherwise go uncounted and make the rule's arithmetic wrong in the
		// one direction nothing else checks.
		for _, decl := range file.Decls {
			owner := declOwnerName(decl)
			ast.Inspect(decl, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				sel, isSel := call.Fun.(*ast.SelectorExpr)
				if !isSel || sel.Sel.Name != "LoadTelemetryConfig" {
					return true
				}
				if pkg, isIdent := sel.X.(*ast.Ident); isIdent && pkg.Name == "config" {
					sites[owner] = name + ":" + itoa(fset.Position(call.Pos()).Line)
				}
				return true
			})
		}
	}
	return sites
}

func sortedKeysOf(m map[string]string) string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// numberWord spells the small counts the rule comment uses. The comment is prose an operator reads,
// so it says "six", not "6".
func numberWord(n int) string {
	words := []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten"}
	if n < 0 || n >= len(words) {
		return itoa(n)
	}
	return words[n]
}
