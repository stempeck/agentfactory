package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// endpointClassInventory is what EndpointClassKeys is CONTRACTED to be (issue #598 C1): the five
// requestable classes an endpoint profile must cover. It is declared here, in the test, so the
// inventory cannot be edited without an accompanying decision in the same diff.
//
// Hardcoding is legitimate on THIS side and is not the models_telemetry_test.go:24-26 anti-pattern.
// That test hardcodes the side it cannot import (session's) and therefore proves nothing about the
// boundary it claims to guard. This hardcodes the side the test can see directly, while the
// cross-boundary half below is still read from session.go's SOURCE.
var endpointClassInventory = []string{
	"ANTHROPIC_SMALL_FAST_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

// TestEndpointClassKeysSubsetOfRedirectFamilyVars pins issue #598 Decision 11: every class key the
// resolver may DERIVE must also be a key the launch chokepoint CLEARS, or a derived value from a
// previous profile survives a profile switch on a reused session.
//
// It carries two assertions, and both are load-bearing because they fail under DIFFERENT mutations:
//
//   - The SUBSET half (EndpointClassKeys ⊆ session.redirectFamilyVars) catches a key dropped from
//     the session-side family — design-doc.md:398's mutation.
//   - The EXACT-SET half catches a key dropped from, or added to, EndpointClassKeys itself. A
//     subset relation gets strictly EASIER to satisfy as the left-hand set shrinks, so a
//     subset-only test stays green when a member is deleted from the inventory — which is the
//     mutation the phase's acceptance criteria actually prescribe. The exact-set half is the
//     "+ explicit membership assertions" clause of Decision 11, and it is not decoration.
//
// The subset half deliberately does NOT assert equality of the two lists: redirectFamilyVars is
// wider on purpose (it also carries ANTHROPIC_BASE_URL, ANTHROPIC_AUTH_TOKEN, ANTHROPIC_MODEL and
// ANTHROPIC_DEFAULT_FABLE_MODEL). Whole-list byte-parity would structurally freeze the Fable key
// out of hygiene, which is exactly why Decision 11 revised the shape to subset.
//
// The session side is read from SOURCE via go/parser rather than imported: internal/session
// imports internal/config (session.go:15), so a test in package config that imported it back would
// be an import cycle. Same motive as reserved_parity_test.go and internal/cmd/kill_scope_drift_test.go.
func TestEndpointClassKeysSubsetOfRedirectFamilyVars(t *testing.T) {
	// --- EXACT-SET half: the inventory is the contracted five, no more and no fewer. ---
	got := map[string]bool{}
	for _, k := range EndpointClassKeys {
		if got[k] {
			t.Errorf("EndpointClassKeys contains %q twice; a duplicate makes the subset half vacuously stronger and double-counts downstream: %v", k, EndpointClassKeys)
		}
		got[k] = true
	}
	want := map[string]bool{}
	for _, k := range endpointClassInventory {
		want[k] = true
	}
	for _, k := range endpointClassInventory {
		if !got[k] {
			t.Errorf("class key %q was REMOVED from EndpointClassKeys. The inventory is the contracted five: removing one silently shrinks what Phase 2's derivation ladder and Phase 3a's coverage lint will ever consider, so that class quietly falls back to the host's own claude-* id — issue #598 itself. got=%v", k, EndpointClassKeys)
		}
	}
	for _, k := range EndpointClassKeys {
		if want[k] {
			continue
		}
		if k == "ANTHROPIC_DEFAULT_FABLE_MODEL" {
			t.Errorf("ANTHROPIC_DEFAULT_FABLE_MODEL was added to EndpointClassKeys. It is excluded until the Phase 0 spike pins whether the deployed CLI honors the key; adding it now smuggles a spike-gated decision in ahead of its evidence. Note the subset assertion below CANNOT catch this — the Fable key IS a redirectFamilyVars member, so the subset relation still holds. This assertion is the only guard.")
			continue
		}
		t.Errorf("unexpected key %q in EndpointClassKeys. Inventory membership is a contract that Phase 2's ladder and Phase 3a's lint both read; widening it changes their behavior. If this is deliberate, amend endpointClassInventory in the SAME change.", k)
	}
	if len(EndpointClassKeys) != len(endpointClassInventory) {
		t.Errorf("EndpointClassKeys has %d members, want exactly %d", len(EndpointClassKeys), len(endpointClassInventory))
	}

	// --- SUBSET half: every inventory member is cleared by the launch chokepoint. ---
	family := redirectFamilyVarsFromSource(t, sessionGoPathForParity(t))
	if len(family) == 0 {
		t.Fatal("parsed zero redirectFamilyVars members from session.go — the scan matches nothing, so it guards nothing")
	}
	// Anchor: a member every version of the list has carried. Without it a walker that latched onto
	// the wrong var could return a non-empty set and still be reading something else entirely.
	if !family["ANTHROPIC_MODEL"] {
		t.Fatalf("the parsed set does not contain ANTHROPIC_MODEL, so the extractor is reading the wrong declaration; got %v", sortedKeys(family))
	}
	for _, k := range EndpointClassKeys {
		if !family[k] {
			t.Errorf("class key %q is in config.EndpointClassKeys but MISSING from session.redirectFamilyVars — the launch chokepoint would not clear it, so a value derived under a previous profile survives a profile switch on a reused session (issue #508's failure class, reopened for a derived value)", k)
		}
	}
}

// TestEndpointClassDerivationTablesAgreeWithInventory guards the two SATELLITE tables against the
// inventory they describe. EndpointClassKeys is pinned from both sides above, but
// derivedEndpointClassKeys and endpointClassLabels are separate declarations that a future class key
// can miss independently — and the failure is silent rather than loud: a missing label yields
// endpointClassLabels[key] == "", so CoverageLintProfile renders "leaves , opus, sonnet" and ships a
// malformed sentence to the operator instead of failing anything.
func TestEndpointClassDerivationTablesAgreeWithInventory(t *testing.T) {
	inventory := make(map[string]bool, len(EndpointClassKeys))
	for _, k := range EndpointClassKeys {
		inventory[k] = true
	}

	for _, k := range derivedEndpointClassKeys {
		if !inventory[k] {
			t.Errorf("derivedEndpointClassKeys contains %q, which is not in EndpointClassKeys — derivation would fill a key the inventory does not claim to cover", k)
		}
		if endpointClassLabels[k] == "" {
			t.Errorf("derived class %q has no entry in endpointClassLabels — CoverageLintProfile would name it as an empty string in an operator-facing warning", k)
		}
	}

	for k := range endpointClassLabels {
		if !inventory[k] {
			t.Errorf("endpointClassLabels labels %q, which is not in EndpointClassKeys — a stale label outlives the key it described", k)
		}
	}

	// CLAUDE_CODE_SUBAGENT_MODEL is the deliberate asymmetry (Decision 14): in the inventory, never
	// derived. Pinning it here means a future edit that "completes" the derivation table has to
	// argue with this test rather than silently reverse the decision.
	for _, k := range derivedEndpointClassKeys {
		if k == "CLAUDE_CODE_SUBAGENT_MODEL" {
			t.Error("CLAUDE_CODE_SUBAGENT_MODEL must never be derived (Decision 14): the sub-agent class is chosen by the caller, not inherited from the main model")
		}
	}
}

// sessionGoPathForParity resolves internal/session/session.go relative to THIS file via
// runtime.Caller, so the test is independent of the working directory. Deliberately a distinct
// helper from reserved_parity_test.go's webValidateGoPath: the two parity tests guard unrelated
// boundaries and must not become coupled by a shared path helper.
func sessionGoPathForParity(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed — cannot locate this test's directory")
	}
	// this file: <root>/internal/config/endpoint_class_parity_test.go → <root>/internal/session/session.go
	return filepath.Join(filepath.Dir(thisFile), "..", "session", "session.go")
}

// redirectFamilyVarsFromSource go/parser-parses path and returns the member SET of its top-level
// `redirectFamilyVars` []string composite literal.
//
// It cannot reuse reserved_parity_test.go's reservedNamesFromSource: that helper reads a
// map[string]bool and casts every element to *ast.KeyValueExpr, which every element of a []string
// fails — copied verbatim it would return an empty set and the parity check would silently guard
// nothing. redirectFamilyVars mixes two element shapes: *ast.BasicLit for the six string literals
// and *ast.Ident for the two that use consts (envBaseURL, envAuthToken). Identifiers are resolved
// through the file's own string consts rather than skipped, so the test can never quietly stop
// covering a member that a future refactor turns into a const.
func redirectFamilyVarsFromSource(t *testing.T, path string) map[string]bool {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	consts := stringConstsFromFile(f)

	keys := map[string]bool{}
	found := false
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
				if name.Name != "redirectFamilyVars" || i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.CompositeLit)
				if !ok {
					t.Fatalf("redirectFamilyVars is no longer a composite literal (%T) — repoint this extractor rather than letting it read nothing", vs.Values[i])
				}
				found = true
				for _, elt := range lit.Elts {
					switch e := elt.(type) {
					case *ast.BasicLit:
						if e.Kind != token.STRING {
							t.Fatalf("redirectFamilyVars holds a non-string literal %s", e.Value)
						}
						s, err := strconv.Unquote(e.Value)
						if err != nil {
							t.Fatalf("unquote %q: %v", e.Value, err)
						}
						keys[s] = true
					case *ast.Ident:
						s, ok := consts[e.Name]
						if !ok {
							t.Fatalf("redirectFamilyVars member %q is an identifier this extractor cannot resolve to a string const in %s — resolve it, do not skip it, or the parity check silently stops covering that member", e.Name, path)
						}
						keys[s] = true
					default:
						t.Fatalf("redirectFamilyVars holds an unexpected element type %T", elt)
					}
				}
			}
		}
	}
	if !found {
		t.Fatalf("no redirectFamilyVars declaration found in %s — it was renamed or moved to another file, and this test guards nothing until the extractor is repointed", path)
	}
	return keys
}

// stringConstsFromFile returns every `name = "value"` const declared at the top level of f, so an
// identifier used inside a composite literal can be resolved to the string it stands for.
func stringConstsFromFile(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.CONST {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				bl, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || bl.Kind != token.STRING {
					continue
				}
				s, err := strconv.Unquote(bl.Value)
				if err != nil {
					continue
				}
				out[name.Name] = s
			}
		}
	}
	return out
}

// sortedKeys sorts because it feeds a t.Fatalf diagnostic: an unsorted dump of a Go map varies
// between runs of the SAME failure, which makes a red test look like two different reds.
func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// --- ADR-004 purity of the three #598 helpers ---

// purityForbidden lists the package-qualified call shapes that would make a helper impure.
//
// os/exec/http/bufio/ioutil are banned wholesale. `net` is NOT: net.ParseIP (endpoint.go:33) is a
// pure string parse with no I/O, and a blunt net ban would turn the first refactor that routes a
// helper past IsLoopbackEndpoint into a red test with a wrong reason. Only net's actually-dialling
// entry points are forbidden, by prefix.
var purityForbiddenPkgs = map[string]bool{
	"os":     true,
	"exec":   true,
	"http":   true,
	"bufio":  true,
	"ioutil": true,
}

var purityForbiddenNetPrefixes = []string{"Dial", "Listen", "Lookup", "Resolve"}

// TestEndpointClassHelpersArePure proves ADR-004 for the three #598 helpers by walking the
// package's own source: the TRANSITIVE call graph rooted at each helper must reach no file, process
// or network I/O. Transitive matters — a helper that calls a package-local function that reads a
// file is impure, and only the walk sees it.
//
// The env-read third of ADR-004 is already enforced repo-wide by
// internal/cmd/env_hermetic_test.go's TestNoEnvReadsInLibraryPackages, which scans this very file's
// package; it is deliberately not duplicated here.
//
// Known limitation: calls through function values and interface methods are invisible to this
// scan. The three helpers are leaf-shaped pure functions over a map[string]string, like their
// PairingLintProfile template, so there is no such indirection to miss today.
func TestEndpointClassHelpersArePure(t *testing.T) {
	funcs, scanned := packageCallGraph(t, ".")
	if scanned == 0 {
		t.Fatal("purity scan read zero production files — every verdict below would be vacuous")
	}

	// Positive control. LoadModelsConfig demonstrably calls os.ReadFile, so a scanner that found it
	// clean is broken or matched nothing, and the three "pure" verdicts below would be theatre.
	if len(impureChain(funcs, "LoadModelsConfig")) == 0 {
		t.Fatal("the purity scanner found NOTHING impure in LoadModelsConfig, which calls os.ReadFile — the scanner is broken, so every purity verdict in this test is vacuous")
	}

	// ResolveModelEnv joined the roster in Phase 2: it now calls CompleteEndpointProfile, so the
	// resolver's own purity is only as good as the chain below it, and the launch chokepoint is
	// exactly where an env read would be least visible and most damaging.
	for _, name := range []string{"ResolveModelEnv", "CompleteEndpointProfile", "MissingEndpointClasses", "CoverageLintProfile"} {
		if _, ok := funcs[name]; !ok {
			t.Errorf("%s not found in the package source — the purity scan cannot cover a function it did not parse", name)
			continue
		}
		if chain := impureChain(funcs, name); len(chain) > 0 {
			t.Errorf("%s is not pure (ADR-004: no env reads, no file reads, no network): %s", name, strings.Join(chain, "; "))
		}
	}
}

type funcInfo struct {
	forbidden []string
	callees   []string
}

// packageCallGraph parses every non-test .go file in dir and records, per top-level function, the
// forbidden calls it makes directly and the package-local functions it calls.
func packageCallGraph(t *testing.T, dir string) (map[string]funcInfo, int) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read package dir %s: %v", dir, err)
	}
	funcs := map[string]funcInfo{}
	scanned := 0
	fset := token.NewFileSet()
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		scanned++
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Body == nil {
				continue
			}
			info := funcs[fd.Name.Name]
			ast.Inspect(fd.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				switch fun := call.Fun.(type) {
				case *ast.SelectorExpr:
					pkg, ok := fun.X.(*ast.Ident)
					if !ok {
						return true
					}
					if purityForbiddenPkgs[pkg.Name] {
						info.forbidden = append(info.forbidden, fmt.Sprintf("%s.%s (%s)", pkg.Name, fun.Sel.Name, fset.Position(call.Pos())))
					}
					if pkg.Name == "net" {
						for _, p := range purityForbiddenNetPrefixes {
							if strings.HasPrefix(fun.Sel.Name, p) {
								info.forbidden = append(info.forbidden, "net."+fun.Sel.Name)
							}
						}
					}
				case *ast.Ident:
					info.callees = append(info.callees, fun.Name)
				}
				return true
			})
			funcs[fd.Name.Name] = info
		}
	}
	return funcs, scanned
}

// impureChain depth-first walks the call graph from root and returns a human-readable chain for
// every forbidden call it can reach.
func impureChain(funcs map[string]funcInfo, root string) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(name, path string)
	walk = func(name, path string) {
		if seen[name] {
			return
		}
		seen[name] = true
		info, ok := funcs[name]
		if !ok {
			return
		}
		for _, f := range info.forbidden {
			out = append(out, path+" -> "+f)
		}
		for _, c := range info.callees {
			walk(c, path+" -> "+c)
		}
	}
	walk(root, root)
	return out
}

// TestEndpointClassHelpersIgnoreAmbientEnvironment is the behavioral twin of the source scan above:
// with every class key exported to a sentinel value, the helpers must produce byte-identical
// results to a clean run. The two fail differently — this one names the leaked value — and a
// behavioral failure is far easier to read than an AST finding.
//
// The sentinel is seeded EXPLICITLY rather than asserting os.Getenv(k) == "": the inverted form is
// vacuous whenever the ambient environment happens to be clean, which is most of the time.
func TestEndpointClassHelpersIgnoreAmbientEnvironment(t *testing.T) {
	const junk = "af-598-purity-sentinel"

	endpoint := map[string]string{
		"ANTHROPIC_BASE_URL":   "http://localhost:4000",
		"ANTHROPIC_AUTH_TOKEN": "tok",
		"ANTHROPIC_MODEL":      "gpt-4o",
	}
	direct := map[string]string{"ANTHROPIC_MODEL": "claude-opus-5"}
	// The fixture that gives this test its teeth. endpoint and direct both DECLARE
	// ANTHROPIC_MODEL, so an os.Getenv("ANTHROPIC_MODEL") fallback behind the declared value would
	// never fire on them and this test would stay green through a real ADR-004 violation. bare
	// declares no main model, so it is the only input on which such a fallback is observable.
	bare := map[string]string{"ANTHROPIC_BASE_URL": "http://localhost:4000"}

	cleanEndpoint := CompleteEndpointProfile(endpoint)
	cleanMissing := MissingEndpointClasses(endpoint)
	cleanWarning, cleanOK := CoverageLintProfile("codex", endpoint)
	cleanDirect := CompleteEndpointProfile(direct)
	cleanBare := CompleteEndpointProfile(bare)
	cleanBareMissing := MissingEndpointClasses(bare)
	cleanBareWarning, cleanBareOK := CoverageLintProfile("bare", bare)

	for _, k := range append([]string{"ANTHROPIC_BASE_URL", "ANTHROPIC_MODEL", "ANTHROPIC_AUTH_TOKEN"}, endpointClassInventory...) {
		t.Setenv(k, junk)
	}

	if got := CompleteEndpointProfile(endpoint); !sameStringMap(got, cleanEndpoint) {
		t.Errorf("CompleteEndpointProfile read the ambient environment (ADR-004): clean=%v ambient=%v", cleanEndpoint, got)
	}
	if got := CompleteEndpointProfile(direct); !sameStringMap(got, cleanDirect) {
		t.Errorf("CompleteEndpointProfile read the ambient environment on a non-endpoint profile: clean=%v ambient=%v", cleanDirect, got)
	}
	if got := CompleteEndpointProfile(bare); !sameStringMap(got, cleanBare) {
		t.Errorf("CompleteEndpointProfile fell back to the ambient environment for the main model (ADR-004): clean=%v ambient=%v", cleanBare, got)
	}
	if got := MissingEndpointClasses(endpoint); !sameStringSlice(got, cleanMissing) {
		t.Errorf("MissingEndpointClasses read the ambient environment (ADR-004): clean=%v ambient=%v", cleanMissing, got)
	}
	if got := MissingEndpointClasses(bare); !sameStringSlice(got, cleanBareMissing) {
		t.Errorf("MissingEndpointClasses read the ambient environment on a model-less profile (ADR-004): clean=%v ambient=%v", cleanBareMissing, got)
	}
	if w, ok := CoverageLintProfile("bare", bare); w != cleanBareWarning || ok != cleanBareOK {
		t.Errorf("CoverageLintProfile read the ambient environment on a model-less profile (ADR-004): clean=(%q,%v) ambient=(%q,%v)", cleanBareWarning, cleanBareOK, w, ok)
	}
	warning, ok := CoverageLintProfile("codex", endpoint)
	if warning != cleanWarning || ok != cleanOK {
		t.Errorf("CoverageLintProfile read the ambient environment (ADR-004): clean=(%q,%v) ambient=(%q,%v)", cleanWarning, cleanOK, warning, ok)
	}
	for _, v := range CompleteEndpointProfile(endpoint) {
		if strings.Contains(v, junk) {
			t.Errorf("CompleteEndpointProfile leaked the ambient sentinel into its output: %v", CompleteEndpointProfile(endpoint))
			break
		}
	}
	if strings.Contains(warning, junk) {
		t.Errorf("CoverageLintProfile leaked the ambient sentinel into its warning: %q", warning)
	}
}

func sameStringMap(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
