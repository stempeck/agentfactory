package server

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// This source-lint lives in the server package (not internal/exec) on purpose: it must embed the
// very patterns it forbids as fixtures. It scans the WHOLE web module by WalkDir (the
// extractability_test.go:30-55 precedent), replacing the former hardcoded 7-dir list that missed 5
// of the 11 internal packages and silently skipped a typo'd/absent dir — so a NEW package can no
// longer escape the lint (design-doc L416). Three forbidden classes, each self-negatived:
//   - shell-string exec (argv arrays only, never a shell) — AC#1/AC#6
//   - real mutating af invocation (mutations go through the injectable Runner, or through the one
//     path-keyed exemption named in isExemptFromMutateLint) — AC#6
//   - tmux INPUT/interaction primitive (the web tmux surface is READ-only) — AC-3

// forbiddenShell flags a shell interpreter spawned via exec.Command / osexec.CommandContext with
// "sh"/"bash" as the program. The CommandContext spelling is included (peer review Gap 2) so the
// readmodel seam's exact shape can never smuggle a shell.
var forbiddenShell = regexp.MustCompile(`sh -c|Command(Context)?\((ctx, )?"(sh|bash)"`)

// mutatingVerbs are the af verbs that change factory state. `mail` joined down/sling in #500 and
// `install` joined them in #502 Phase 1d. Every one of them belongs to the injectable Runner seam
// (ExecRunner / RunStream) — with exactly one sanctioned exception, genjob (see
// isExemptFromMutateLint), which must self-spawn a detached child the Runner cannot host.
var mutatingVerbs = map[string]bool{"down": true, "sling": true, "mail": true, "install": true}

// forbiddenTmuxInput flags any tmux INPUT/interaction primitive. Matched in QUOTED-argv context so
// prose and identifiers ("attachment", "// attach …", sendKeys) never false-positive; the READ
// primitives the web tmux surface legitimately uses ("capture-pane", "list-sessions",
// "has-session") are deliberately absent from the set. Verified 2026-07-03: ZERO occurrences
// module-wide, so the pattern starts clean — non-vacuity is proven by the self-negative below.
var forbiddenTmuxInput = regexp.MustCompile(`"(send-keys|paste-buffer|load-buffer|set-buffer|attach-session|attach)"`)

// isExemptFromShellLint reports whether path is the ONE file allowed to carry a shell-exec literal:
// internal/entrypoint/guard_test.go legitimately runs the shipped quickstart.sh bash guard via
// exec.Command("bash", …) to make the IFF-available launch contract CI-visible. The exemption is
// narrow — keyed on the exact path suffix — so no OTHER file inherits it (proven non-broad by the
// self-negative). It scopes ONLY the shell class; the mutating-af and tmux-input classes still apply.
func isExemptFromShellLint(path string) bool {
	return strings.HasSuffix(filepath.ToSlash(path), "internal/entrypoint/guard_test.go")
}

// isExemptFromMutateLint reports whether path is the ONE file allowed to spawn a mutating af verb
// directly: internal/genjob/job.go, which self-spawns a DETACHED `af install --agents` child writing
// an O_APPEND log — a lifetime the request-scoped Runner seam cannot host (design Phase 2 JOB / H-3).
// Its argv is fixed and carries zero caller input (AC-10). The exemption is keyed on the exact path
// suffix, so no sibling inherits it, and it scopes ONLY the mutating-af class.
//
// The exemption is explicit because the detector below SEES genjob's shape:
// TestLint_MutatingExec_FlagsPackageVarProgram plants that exact shape at a non-exempt path and
// requires an offense. Before this, genjob passed only because the literal-matching regex could not
// resolve a variable program name — an accident that also let any rogue file bypass the lint.
func isExemptFromMutateLint(path string) bool {
	return strings.HasSuffix(filepath.ToSlash(path), "internal/genjob/job.go")
}

// hasMutatingAfExec reports whether src spawns a mutating af verb through os/exec.
//
// It resolves the program name and the argv through the file's string bindings instead of matching a
// source literal. A literal match is defeated by a single assignment — `p := "af"; exec.Command(p,
// argv...)` — which is exactly the shape genjob uses, so "no literal match" never meant "no direct
// mutation". Resolving also removes the regex's false positives for free: prose in a comment and a
// verb in an unrelated string are not call arguments, so they cannot trip the detector.
//
// Unresolvable arguments (a parameter, a computed slice) simply do not resolve, and an exec whose
// program does not resolve to "af" is never an offense. The lint therefore under-approximates rather
// than guesses; what it does flag, it flags on evidence.
func hasMutatingAfExec(src string) (bool, error) {
	f, err := parser.ParseFile(token.NewFileSet(), "", src, 0)
	if err != nil {
		return false, err
	}

	execPkgs := map[string]bool{}
	for _, imp := range f.Imports {
		path, uerr := strconv.Unquote(imp.Path.Value)
		if uerr != nil || path != "os/exec" {
			continue
		}
		name := "exec"
		if imp.Name != nil {
			name = imp.Name.Name // the `osexec "os/exec"` spelling used across the module
		}
		execPkgs[name] = true
	}
	if len(execPkgs) == 0 {
		return false, nil
	}

	binds := stringBindings(f)
	found := false
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok || !execPkgs[pkg.Name] {
			return true
		}
		var progIdx int
		switch sel.Sel.Name {
		case "Command":
			progIdx = 0
		case "CommandContext":
			progIdx = 1
		default:
			return true
		}
		if len(call.Args) <= progIdx {
			return true
		}
		if prog := resolveStrings(binds, call.Args[progIdx]); len(prog) != 1 || prog[0] != "af" {
			return true
		}
		for _, arg := range call.Args[progIdx+1:] {
			for _, v := range resolveStrings(binds, arg) {
				if mutatingVerbs[v] {
					found = true
					return false
				}
			}
		}
		return true
	})
	return found, nil
}

// stringBindings maps each identifier in f that is bound EXACTLY ONCE to a string literal or to a
// literal slice of strings. An identifier bound twice is dropped rather than guessed at, so the
// resolver never claims a value the code does not unambiguously give it.
func stringBindings(f *ast.File) map[string][]string {
	out := map[string][]string{}
	bound := map[string]bool{}
	bind := func(name string, expr ast.Expr) {
		if name == "_" {
			return
		}
		if bound[name] {
			delete(out, name)
			return
		}
		bound[name] = true
		if vs := literalStrings(expr); vs != nil {
			out[name] = vs
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.ValueSpec: // var / const, package-level or inside a function
			for i, name := range d.Names {
				if i < len(d.Values) {
					bind(name.Name, d.Values[i])
				}
			}
		case *ast.AssignStmt:
			for i, lhs := range d.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok || i >= len(d.Rhs) {
					continue
				}
				bind(id.Name, d.Rhs[i])
			}
		}
		return true
	})
	return out
}

// literalStrings yields the strings an expression literally denotes: one for a string literal, and
// every string element for a composite literal (the `[]string{"install", "--agents"}` argv shape).
func literalStrings(e ast.Expr) []string {
	switch v := e.(type) {
	case *ast.BasicLit:
		if v.Kind != token.STRING {
			return nil
		}
		s, err := strconv.Unquote(v.Value)
		if err != nil {
			return nil
		}
		return []string{s}
	case *ast.CompositeLit:
		var out []string
		for _, el := range v.Elts {
			lit, ok := el.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				continue
			}
			if s, err := strconv.Unquote(lit.Value); err == nil {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// resolveStrings reads an argument as the strings it stands for: a literal directly, an identifier
// through its binding. `exec.Command(p, argv...)` reaches here as the two idents p and argv — the
// ellipsis is not part of the argument expression — so a spread argv resolves like any other.
func resolveStrings(binds map[string][]string, e ast.Expr) []string {
	if id, ok := e.(*ast.Ident); ok {
		return binds[id.Name]
	}
	return literalStrings(e)
}

type offense struct {
	path string
	kind string
}

const (
	kindShell     = "shell-string exec"
	kindMutate    = "mutating af invocation"
	kindTmuxInput = "tmux input primitive"
)

// scanTree walks root and flags every .go file (INCLUDING _test.go — build tags don't exempt raw
// bytes) that carries a forbidden pattern or a mutating af spawn. A .go file that does not parse is
// a hard failure, not a silent pass. lint_test.go itself is skipped (it holds the fixtures);
// non-source dirs are skipped (the extractability_test.go skip switch). Taking root as a parameter
// lets the planted-file test drive a fresh temp tree through the same code path.
func scanTree(t *testing.T, root string) []offense {
	t.Helper()
	var offenses []offense
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".gotmp", "vendor", "testdata", ".git":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		if d.Name() == "lint_test.go" { // this file holds the fixtures; never scan it
			return nil
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		src := string(b)
		if forbiddenShell.MatchString(src) && !isExemptFromShellLint(path) {
			offenses = append(offenses, offense{path, kindShell})
		}
		mutates, perr := hasMutatingAfExec(src)
		if perr != nil {
			return perr
		}
		if mutates && !isExemptFromMutateLint(path) {
			offenses = append(offenses, offense{path, kindMutate})
		}
		if forbiddenTmuxInput.MatchString(src) {
			offenses = append(offenses, offense{path, kindTmuxInput})
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking %s: %v", root, err)
	}
	return offenses
}

func moduleRoot() string { return filepath.Join("..", "..") } // web/internal/server -> web

// AC1/AC6 — no shell-string exec, and no real live-tree mutation, anywhere in the web module
// source. Mutations go only through the injectable Runner/fake.
func TestExec_NoLiveTreeMutation(t *testing.T) {
	for _, o := range scanTree(t, moduleRoot()) {
		switch o.kind {
		case kindShell:
			t.Errorf("shell-string exec found in %s — use argv arrays only (never a shell)", o.path)
		case kindMutate:
			t.Errorf("real mutating af invocation found in %s — mutations must go through the Runner seam, "+
				"or the file must be named in isExemptFromMutateLint with a reason", o.path)
		}
	}
}

// TestLint_NoSessionInputPrimitives — AC-3's read-only invariant as a CI property: no tmux INPUT
// primitive may appear anywhere in the web tmux surface. The web console reads panes; it never
// types into them.
func TestLint_NoSessionInputPrimitives(t *testing.T) {
	for _, o := range scanTree(t, moduleRoot()) {
		if o.kind == kindTmuxInput {
			t.Errorf("tmux INPUT primitive found in %s — the web tmux surface is READ-ONLY (AC-3): no send-keys/paste-buffer/load-buffer/set-buffer/attach", o.path)
		}
	}
}

// TestLint_WalkDetectsNewDir — design-doc L416: the module WalkDir (not a hardcoded dir list)
// detects a planted forbidden file in a NEW, never-listed dir. Hermetic: a temp tree with a nested
// package the lint has no knowledge of.
func TestLint_WalkDetectsNewDir(t *testing.T) {
	root := t.TempDir()
	newpkg := filepath.Join(root, "internal", "totally", "newpkg")
	if err := os.MkdirAll(newpkg, 0o755); err != nil {
		t.Fatal(err)
	}
	// Assembled from fragments so THIS file's raw bytes never carry the contiguous forbidden literal.
	planted := "package newpkg\n\nfunc x() { _ = " + `exec.Command(` + `"sh", "-c", "boom") }` + "\n"
	if err := os.WriteFile(filepath.Join(newpkg, "rogue.go"), []byte(planted), 0o644); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, o := range scanTree(t, root) {
		if o.kind == kindShell {
			found = true
		}
	}
	if !found {
		t.Error("module walk failed to detect a planted shell-exec in a NEW dir — a new package could silently escape the lint")
	}
}

// TestLint_EntrypointExemptionIsNarrow proves the shell-lint exemption is scoped to exactly
// entrypoint/guard_test.go and does not leak to any sibling (e.g. a same-basename file elsewhere).
func TestLint_EntrypointExemptionIsNarrow(t *testing.T) {
	if !isExemptFromShellLint(filepath.FromSlash("web/internal/entrypoint/guard_test.go")) {
		t.Error("the legitimate entrypoint/guard_test.go bash-guard test must be exempt")
	}
	for _, p := range []string{
		filepath.FromSlash("web/internal/server/guard_test.go"),
		filepath.FromSlash("web/internal/entrypoint/other_test.go"),
		filepath.FromSlash("web/cmd/afweb/main.go"),
	} {
		if isExemptFromShellLint(p) {
			t.Errorf("exemption wrongly leaked to %q — it must be narrow", p)
		}
	}
}

// Self-negative — proves the shell lint is not vacuous. The fixtures are assembled so the raw source
// here never contains the contiguous forbidden literal (the regex still matches at runtime).
func TestExec_NoLiveTreeMutation_SelfNegative(t *testing.T) {
	shDashC := "sh -" + "c"
	mustFlagShell := []string{
		shDashC,
		"bash -" + "c " + "script",
		`exec.Command(` + `"sh", "-c", payload)`,
		`osexec.CommandContext(` + `ctx, "bash", "-c", payload)`, // CommandContext spelling (Gap 2)
	}
	for _, s := range mustFlagShell {
		if !forbiddenShell.MatchString(s) {
			t.Errorf("forbiddenShell failed to flag %q", s)
		}
	}
	mustNotFlag := []string{
		`exec.Command(` + `"af", "agents", "list", "--json")`,
		`afArgv("down", name)`,
		`cmd.Stdout = &stdout`,
		`osexec.CommandContext(` + `ctx, "tmux", "list-sessions")`, // a tmux READ is not a shell
	}
	for _, s := range mustNotFlag {
		if forbiddenShell.MatchString(s) {
			t.Errorf("forbiddenShell false-positive on %q", s)
		}
	}
}

// goSrc wraps declarations in a parseable file importing os/exec under localName. The detector only
// needs syntax, so undeclared identifiers in the fixtures (ctx, name, payload) are fine.
func goSrc(localName, decls string) string {
	spec := "\t" + `"os/exec"`
	if localName != "exec" {
		spec = "\t" + localName + " " + `"os/exec"`
	}
	return "package p\n\nimport (\n" + spec + "\n)\n\n" + decls + "\n"
}

func detectsMutate(t *testing.T, src string) bool {
	t.Helper()
	got, err := hasMutatingAfExec(src)
	if err != nil {
		t.Fatalf("fixture does not parse: %v\n%s", err, src)
	}
	return got
}

// Self-negative for the mutating-af class — the detector flags every mutating verb, through both
// spellings, under an import alias, and through an indirected program name; and it flags nothing
// else. The fixtures are assembled from fragments so this file's raw bytes never carry a contiguous
// forbidden literal, preserving the invariant scanTree's skip of lint_test.go rests on.
func TestExec_NoLiveTreeMutation_MutateSelfNegative(t *testing.T) {
	mustFlag := map[string]string{
		"down":                     "func f(name string) { _ = " + `exec.Command(` + `"af", "down", name) }`,
		"sling":                    "func f(name string) { _ = " + `exec.Command(` + `"af", "sling", "--agent", name) }`,
		"mail (#500)":              "func f(name string) { _ = " + `exec.Command(` + `"af", "mail", "send", name) }`,
		"install (#502 Phase 1d)":  "func f() { _ = " + `exec.Command(` + `"af", "install", "--agents") }`,
		"indirected program name":  "func f() {\n\tp := " + `"af"` + "\n\t_ = " + `exec.Command(` + `p, "down", "--all") }`,
		"indirected program+argv":  "var b = " + `"af"` + "\nvar a = []string{" + `"sling", "--agent"` + "}\nfunc f() { _ = " + `exec.Command(` + "b, a...) }",
		"verb reached via a slice": "func f() {\n\ta := []string{" + `"install", "--agents"` + "}\n\t_ = " + `exec.Command(` + `"af", a...) }`,
	}
	for name, decls := range mustFlag {
		if !detectsMutate(t, goSrc("exec", decls)) {
			t.Errorf("hasMutatingAfExec failed to flag %s", name)
		}
	}

	// The CommandContext spelling under the module's `osexec` alias (peer review Gap 2).
	aliased := "func f(ctx context.Context) { _ = " + `osexec.CommandContext(` + `ctx, "af", "down", "--all") }`
	if !detectsMutate(t, goSrc("osexec", aliased)) {
		t.Error("hasMutatingAfExec failed to flag an aliased osexec.CommandContext mutation")
	}

	mustNotFlag := map[string]string{
		"a read verb":                       "func f() { _ = " + `exec.Command(` + `"af", "agents", "list", "--json") }`,
		"a non-af program":                  "func f() { _ = " + `exec.Command(` + `"sleep", "30") }`,
		"a resolved non-af binary":          "var bin = " + `"tmux"` + "\nfunc f() { _ = " + `exec.Command(` + `bin, "list-sessions") }`,
		"an unresolvable program":           "func f(prog string) { _ = " + `exec.Command(` + `prog, "down") }`,
		"a helper that is not an exec call": "func f(name string) { afArgv(" + `"down", name) }`,
		"a Command on another package":      "func f() { runner.Command(" + `"af", "down") }`,
		// The regex this detector replaced flagged the shape wherever it appeared, including prose.
		"the shape quoted inside a comment": "// never write " + `exec.Command(` + `"af", "down", name)` + "\nfunc f() {}",
		"the shape quoted inside a string":  "func f() { msg := " + "`" + `exec.Command(` + `"af", "install")` + "`" + "; _ = msg }",
	}
	for name, decls := range mustNotFlag {
		if detectsMutate(t, goSrc("exec", decls)) {
			t.Errorf("hasMutatingAfExec false-positive on %s", name)
		}
	}
}

// TestLint_MutateExemptionIsNarrow proves the mutating-af exemption is scoped to exactly
// genjob/job.go and leaks to no sibling — the same narrowness contract the shell exemption carries.
func TestLint_MutateExemptionIsNarrow(t *testing.T) {
	if !isExemptFromMutateLint(filepath.FromSlash("web/internal/genjob/job.go")) {
		t.Error("genjob/job.go is the sanctioned detached-spawn path and must be exempt")
	}
	for _, p := range []string{
		filepath.FromSlash("web/internal/genjob/job_test.go"),
		filepath.FromSlash("web/internal/genjob/state.go"),
		filepath.FromSlash("web/internal/server/job.go"),
		filepath.FromSlash("web/cmd/afweb/main.go"),
	} {
		if isExemptFromMutateLint(p) {
			t.Errorf("exemption wrongly leaked to %q — it must be narrow", p)
		}
	}
}

// TestLint_NoSessionInputPrimitives_SelfNegative proves forbiddenTmuxInput matches every input
// primitive (in quoted-argv context) and never false-positives on the READ primitives or prose.
func TestLint_NoSessionInputPrimitives_SelfNegative(t *testing.T) {
	mustFlag := []string{
		`execCommand(ctx, "tmux", ` + `"send-` + `keys", "-t", s)`,
		`"paste-` + `buffer"`,
		`"load-` + `buffer"`,
		`"set-` + `buffer"`,
		`"` + `attach"`,
		`"attach-` + `session"`,
	}
	for _, s := range mustFlag {
		if !forbiddenTmuxInput.MatchString(s) {
			t.Errorf("forbiddenTmuxInput failed to flag %q", s)
		}
	}
	mustNotFlag := []string{
		`"capture-pane"`,        // the web surface's actual snapshot read
		`"list-sessions"`,       // the liveness/probe read
		`"has-session"`,         // the exact-match membership probe
		`attachment := true`,    // prose/identifier — no quotes around a primitive
		`// attach to the flow`, // comment
		`var sendKeys = false`,  // camelCase identifier, no hyphen, no quotes
	}
	for _, s := range mustNotFlag {
		if forbiddenTmuxInput.MatchString(s) {
			t.Errorf("forbiddenTmuxInput false-positive on %q", s)
		}
	}
}

// ---- T3 (PRRT_kwDORt0n_M6Pw23X): the mutating-exec lint must not be bypassable by indirection ----

// plantGo writes a valid Go source file into a fresh temp tree and returns the tree root, so the
// lint can be driven over source it has never seen. Fragments are assembled so THIS file's bytes
// never carry a contiguous forbidden literal (mirrors TestLint_WalkDetectsNewDir).
func plantGo(t *testing.T, body string) string {
	t.Helper()
	root := t.TempDir()
	pkg := filepath.Join(root, "internal", "planted")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "rogue.go"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func hasMutateOffense(t *testing.T, root string) bool {
	t.Helper()
	for _, o := range scanTree(t, root) {
		if o.kind == kindMutate {
			return true
		}
	}
	return false
}

// TestLint_MutatingExec_FlagsVariableProgram — the reviewer's exact bypass. A real, un-sanctioned
// factory mutation whose program name arrives through a variable must be caught. The literal-only
// regex cannot resolve `p`, so this passes the lint green today and gives false assurance that
// "mutations go only through the injectable Runner".
func TestLint_MutatingExec_FlagsVariableProgram(t *testing.T) {
	body := "package planted\n\nimport \"os/exec\"\n\nfunc rogue() {\n\tp := " + `"af"` + "\n\ta := []string{" + `"down", "--all"` + "}\n\t_ = " + `exec.Command(` + "p, a...)\n}\n"
	if !hasMutateOffense(t, plantGo(t, body)) {
		t.Error("no kindMutate offense — the indirection bypass passed the lint: a variable-program `af` exec must be flagged, literal or not")
	}
}

// TestLint_MutatingExec_FlagsPackageVarProgram — genjob's exact shape, planted at a path that no
// exemption covers. This proves the detector SEES genjob's spawn; the path-keyed exemption added
// alongside it is therefore load-bearing rather than vacuous. Without this, an exemption could be
// "proven" by a detector that never fires.
func TestLint_MutatingExec_FlagsPackageVarProgram(t *testing.T) {
	body := "package planted\n\nimport \"os/exec\"\n\nvar afBinary = " + `"af"` + "\nvar installArgv = []string{" + `"install", "--agents"` + "}\n\nfunc spawn() {\n\t_ = " + `exec.Command(` + "afBinary, installArgv...)\n}\n"
	if !hasMutateOffense(t, plantGo(t, body)) {
		t.Error("no kindMutate offense — genjob's package-var spawn shape is invisible to the lint, so its exemption is implicit rather than explicit")
	}
}

// TestLint_MutatingExec_NoFalsePositiveOnNonAfProgram — the strengthened detector must not flag an
// exec whose program is not `af`. Guards the hermetic fakes (genjob/job_test.go spawns sleep/true/false)
// and the readmodel/bridge test helpers.
func TestLint_MutatingExec_NoFalsePositiveOnNonAfProgram(t *testing.T) {
	body := "package planted\n\nimport \"os/exec\"\n\nvar bin = " + `"sleep"` + "\n\nfunc harmless() {\n\t_ = " + `exec.Command(` + "bin, " + `"30"` + ")\n\t_ = " + `exec.Command("true")` + "\n}\n"
	if hasMutateOffense(t, plantGo(t, body)) {
		t.Error("kindMutate false-positive on a non-`af` program — the detector must resolve the program name, not merely notice a variable")
	}
}
