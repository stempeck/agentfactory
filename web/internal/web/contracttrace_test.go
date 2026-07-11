package web

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #500 Phase 3A: the class-level interlock. The design contract
// (.designs/web/final/design-contract.yaml) promises viewmodel bindings and screens; nothing
// previously read those promises back against the shipped code, which is how the contract-required
// View button dead-ended as a toast stub for months (analyst H1/H3). This test mechanically traces
// every contract `viewmodel:` binding to a real member of its named viewmodel in static/app.js
// (and rejects bare toast(...) stub bodies), and every contract screen id to its section in
// static/index.html. Same source-scan posture as nav_test.go; `staticDir`/`readAsset` are shared
// package helpers (nav_test.go) and `laterPhaseLiteral` comes from agentdetail_test.go.
//
// Scope: this interlock covers the operator-console contract ONLY, whose bindings live in app.js.
// The sibling .designs/web-ui/final/design-contract.yaml is the Formula Editor's (issue #445); its
// viewmodels now DO live in this app (static/scripts/formulas-*.js) and are traced by their own
// sibling test, contracttrace_formula_test.go. Each path stays hardcoded and must NEVER become a
// .designs/**/final/ glob: a glob would trace each contract against the other's assets and fail
// spuriously. Two contracts, two paths, one checker (traceContract).
var contractPath = filepath.Join("..", "..", "..", ".designs", "web", "final", "design-contract.yaml")

// The screen-id → index.html section-id map is explicit because it is NOT mechanical:
// existing screens follow "view-"+id, but the agent-detail screen's shipped section is
// id="view-agent" (pinned by VIEW_IDS and agentdetail_test.go; renaming is out of scope).
var contractScreenSections = map[string]string{
	"floor":        "view-floor",
	"sling":        "view-sling",
	"dispatch":     "view-dispatch",
	"prototypes":   "view-prototypes",
	"settings":     "view-settings",
	"agent-detail": "view-agent",
}

// All contract bindings are double-quoted scalars on their own line (47 at the time of writing).
var reContractBinding = regexp.MustCompile(`(?m)^\s+viewmodel: "([^"]+)"$`)

// Screen ids sit at exactly 2-space `- id:` indent; element ids sit at 6-space indent and the
// global_chrome items (also 2-space) are excluded by scoping extraction to the screens: region.
var reContractScreenID = regexp.MustCompile(`(?m)^  - id: (\S+)\s*$`)

// vmObjectBody slices the object-literal body of a top-level `var <name> = {` viewmodel out of js.
// Every viewmodel is declared at 2-space indent inside the IIFE and closes with a
// 2-space `};`, so the first "\n  };" terminates the literal; the fallbacks keep the checker
// usable on trailing declarations and fixtures.
func vmObjectBody(js, vmName string) (string, bool) {
	decl := "var " + vmName + " = {"
	start := strings.Index(js, decl)
	if start < 0 {
		return "", false
	}
	rest := js[start:]
	if end := strings.Index(rest, "\n  };"); end >= 0 {
		return rest[:end+len("\n  };")], true
	}
	if end := strings.Index(rest, "\n  var "); end >= 0 {
		return rest[:end], true
	}
	return rest, true
}

// checkBinding validates one `viewmodel:` value against the traced script text. Shapes handled (all
// 47 current values): bare object ("ConfirmViewModel"), bare member ("AppViewModel.goHome"), call-with-args
// ("FloorViewModel.viewAgent(agent.name)"), and property paths ("SettingsViewModel.data.startup.agents",
// "SettingsViewModel.data.dispatch.mappings[].label"). The FIRST segment after the viewmodel must be
// a real member (4-space member indent — the VM literal's member position); deeper segments describe
// data shape, not code, and are not traced. Method members must not be bare toast(...) stubs.
func checkBinding(binding, js string) []string {
	expr := binding
	if i := strings.Index(expr, "("); i >= 0 {
		expr = expr[:i]
	}
	segs := strings.Split(expr, ".")
	for i := range segs {
		segs[i] = strings.TrimSuffix(strings.TrimSpace(segs[i]), "[]")
	}
	root := segs[0]
	body, ok := vmObjectBody(js, root)
	if !ok {
		return []string{fmt.Sprintf("binding %q: viewmodel object %q not declared in the traced scripts (no `var %s = {`)", binding, root, root)}
	}
	if len(segs) == 1 {
		return nil // bare-object binding — the declaration itself is the contract
	}
	member := segs[1]
	memberRe := regexp.MustCompile(`(?m)^    ` + regexp.QuoteMeta(member) + `\s*:`)
	if !memberRe.MatchString(body) {
		return []string{fmt.Sprintf("binding %q: %q is not a member of %s in the traced scripts — stale binding (the phantom-claim class this test closes)", binding, member, root)}
	}
	// A body that is ONLY a toast call is a stub (the exact #500 View-button failure). Bodies
	// merely CONTAINING toast (e.g. an error branch) do not match: after the single toast
	// statement the regex requires the closing brace immediately.
	stubRe := regexp.MustCompile(regexp.QuoteMeta(member) + `\s*:\s*function\s*\([^)]*\)\s*\{\s*toast\([^;]*\);?\s*\}`)
	if stubRe.MatchString(body) {
		return []string{fmt.Sprintf("binding %q: %s.%s is a bare toast(...) stub — bind real behavior, not a placeholder", binding, root, member)}
	}
	return nil
}

// traceContract is a pure checker over content strings so the self-negative fixtures drive the
// SAME code path as the real assets (precedent: scanTree in web/internal/server/lint_test.go).
// It returns human-readable violations; empty means the contract traces clean.
//
// js, screens and exemptBindings are parameters, not package globals, because the repo carries TWO
// contracts over two disjoint asset sets — this one over app.js, and the #445 editor contract over
// the three formulas-*.js viewmodels (contracttrace_formula_test.go). One checker, two callers: a
// second copy would be the drift the traces exist to catch.
//
// exemptBindings maps a binding to the reason it cannot resolve. It exists because a contract may be
// internally inconsistent and yet unamendable (a signed v1). Passing nil — as this contract does —
// means every binding must resolve. An exempt binding is a recorded divergence, and its caller is
// expected to fail when the divergence disappears, so an entry cannot outlive its reason.
func traceContract(contractYAML, js, indexHTML string, screens, exemptBindings map[string]string) []string {
	var violations []string

	// Bindings live in global_chrome + screens — everything BEFORE viewmodel_contracts:
	// (that section restates members as data rows, not `viewmodel:` bindings).
	bindingRegion := contractYAML
	if i := strings.Index(contractYAML, "\nviewmodel_contracts:"); i >= 0 {
		bindingRegion = contractYAML[:i]
	}
	bindings := reContractBinding.FindAllStringSubmatch(bindingRegion, -1)
	if len(bindings) == 0 {
		violations = append(violations, "no viewmodel bindings extracted — contract shape or extraction regex broken (non-vacuity guard)")
	}
	for _, m := range bindings {
		if _, exempt := exemptBindings[m[1]]; exempt {
			continue
		}
		violations = append(violations, checkBinding(m[1], js)...)
	}

	// Screen ids: scoped to the screens: region (global_chrome shares the 2-space indent).
	screenRegion := ""
	if strings.HasPrefix(bindingRegion, "screens:") {
		screenRegion = bindingRegion
	} else if i := strings.Index(bindingRegion, "\nscreens:"); i >= 0 {
		screenRegion = bindingRegion[i:]
	}
	if screenRegion == "" {
		violations = append(violations, "no screens: section found in the contract (non-vacuity guard)")
		return violations
	}
	ids := reContractScreenID.FindAllStringSubmatch(screenRegion, -1)
	if len(ids) == 0 {
		violations = append(violations, "no screen ids extracted from the screens: section (non-vacuity guard)")
	}
	for _, m := range ids {
		id := m[1]
		section, ok := screens[id]
		if !ok {
			violations = append(violations, fmt.Sprintf("unknown screen id %q — extend this contract's screen→section map (the mapping is explicit, NOT \"view-\"+id: agent-detail → view-agent, roster → view-formulas)", id))
			continue
		}
		if c := strings.Count(indexHTML, `id="`+section+`"`); c != 1 {
			violations = append(violations, fmt.Sprintf("screen %q: expected exactly one id=%q section in index.html, found %d — a contract screen without its section is the dead-end class #500 closes", id, section, c))
		}
	}
	return violations
}

// TestContractTrace_AllBindingsReal: every contract binding resolves to real code and every
// contract screen has its section. Green means the contract tells the truth; a dead binding or a
// sectionless screen fails web-unit CI hard.
func TestContractTrace_AllBindingsReal(t *testing.T) {
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		if os.IsNotExist(err) {
			// The public/extracted repo ships web/ but not .designs/ (todos/public_repo_files.md),
			// so absence is a legitimate checkout shape there. Monorepo CI runs on a full checkout
			// with no paths filters, so this skip never fires where the interlock matters.
			t.Skipf("design contract absent at %s — OSS/extracted checkout; skipping contract trace", contractPath)
		}
		t.Fatalf("read %s: %v", contractPath, err)
	}
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	for _, v := range traceContract(string(raw), appJS, indexHTML, contractScreenSections, nil) {
		t.Error(v)
	}
}

// TestContractTrace_AllBindingsReal_SelfNegative proves the checker is not vacuous: the SAME
// traceContract code path must pass a consistent fixture, flag a planted toast-stub binding
// (the historical #500 failure, rebuilt verbatim), flag a mapped-but-sectionless screen id, flag
// an unmapped screen id, and flag a member that does not exist.
func TestContractTrace_AllBindingsReal_SelfNegative(t *testing.T) {
	has := func(vs []string, substr string) bool {
		for _, v := range vs {
			if strings.Contains(v, substr) {
				return true
			}
		}
		return false
	}

	cleanContract := `version: 1

screens:
  - id: agent-detail
    route: "(in-memory) agent/<name>"
    elements:
      - id: agent-view
        viewmodel: "FloorViewModel.viewAgent(agent.name)"
      - id: agent-facts
        viewmodel: "AgentDetailViewModel.refresh"

viewmodel_contracts:
  - name: "AgentDetailViewModel"
`
	fixedViewAgent := "    viewAgent: function (name) { AppViewModel.navigate('agent/' + name); },\n"
	cleanAppJS := "  var FloorViewModel = {\n" +
		fixedViewAgent +
		"  };\n" +
		"  var AgentDetailViewModel = {\n" +
		"    data: null,\n" +
		"    refresh: function () {\n" +
		"      return API.get('/x').then(render).catch(function (e) { toast(String(e)); });\n" +
		"    },\n" +
		"  };\n" +
		"  var AppViewModel = {\n" +
		"    navigate: function (route) { showView(route); },\n" +
		"  };\n"
	cleanIndexHTML := `<main><section id="view-agent" hidden></section></main>`

	// (a) Consistent fixture → ZERO violations (guards against an always-failing checker). This
	// also proves two non-flags: a toast call inside a larger body (refresh's catch) is not a
	// stub, and 6-space element ids are not extracted as screen ids.
	if vs := traceContract(cleanContract, cleanAppJS, cleanIndexHTML, contractScreenSections, nil); len(vs) != 0 {
		t.Errorf("checker must report ZERO violations on the consistent fixture, got %d: %v", len(vs), vs)
	}

	// (b) Planted toast-stub binding — the OLD viewAgent body, verbatim (agentdetail_test.go
	// fixture precedent; the placeholder scan reads only static/app.js, so fixtures are safe).
	oldStub := "    viewAgent: function (name) { toast('Agent detail for ' + name + ' " + laterPhaseLiteral + "'); },\n"
	stubAppJS := strings.Replace(cleanAppJS, fixedViewAgent, oldStub, 1)
	if stubAppJS == cleanAppJS {
		t.Fatal("fixture invalid: stub replacement did not apply")
	}
	if vs := traceContract(cleanContract, stubAppJS, cleanIndexHTML, contractScreenSections, nil); !has(vs, "toast(...) stub") {
		t.Errorf("checker must flag a binding whose body is ONLY a toast(...) call, got: %v", vs)
	}

	// (c) Mapped screen id whose section is missing from index.html.
	if vs := traceContract(cleanContract, cleanAppJS, `<main></main>`, contractScreenSections, nil); !has(vs, `id="view-agent"`) {
		t.Errorf("checker must flag the agent-detail screen when index.html lacks its view-agent section, got: %v", vs)
	}

	// (d) Unmapped screen id — forces the explicit map to grow with the contract.
	phantomContract := strings.Replace(cleanContract, "  - id: agent-detail", "  - id: phantom", 1)
	if vs := traceContract(phantomContract, cleanAppJS, cleanIndexHTML, contractScreenSections, nil); !has(vs, "unknown screen id") {
		t.Errorf("checker must flag a screen id missing from contractScreenSections, got: %v", vs)
	}

	// (e) Binding to a member that does not exist (the stale-binding / phantom-claim class).
	staleContract := strings.Replace(cleanContract,
		`viewmodel: "AgentDetailViewModel.refresh"`,
		`viewmodel: "AgentDetailViewModel.launchSequence"`, 1)
	if vs := traceContract(staleContract, cleanAppJS, cleanIndexHTML, contractScreenSections, nil); !has(vs, "not a member") {
		t.Errorf("checker must flag a binding whose member does not exist on the viewmodel, got: %v", vs)
	}

	// (f) Binding whose viewmodel object is not declared at all.
	ghostContract := strings.Replace(cleanContract,
		`viewmodel: "AgentDetailViewModel.refresh"`,
		`viewmodel: "GhostViewModel.run"`, 1)
	if vs := traceContract(ghostContract, cleanAppJS, cleanIndexHTML, contractScreenSections, nil); !has(vs, "not declared") {
		t.Errorf("checker must flag a binding to an undeclared viewmodel object, got: %v", vs)
	}
}
