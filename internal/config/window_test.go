package config

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestPredictContextWindow(t *testing.T) {
	tests := []struct {
		name       string
		profile    map[string]string
		want       int64
		wantSource string
	}{
		{
			name:       "a declaration is the prediction",
			profile:    map[string]string{EnvMaxContextTokens: "1000000"},
			want:       1000000,
			wantSource: WindowSourceDeclared,
		},
		{
			name:       "the fallback is used when nothing is declared",
			profile:    map[string]string{},
			want:       200000,
			wantSource: WindowSourceFallback,
		},
		{
			name:       "a nil profile is the same as an empty one",
			profile:    nil,
			want:       200000,
			wantSource: WindowSourceFallback,
		},
		{
			// "" is how an operator clears the key, and how config_set writes an unset one.
			name:       "an empty declaration counts as absent",
			profile:    map[string]string{EnvMaxContextTokens: ""},
			want:       200000,
			wantSource: WindowSourceFallback,
		},
		{
			name:       "a non-numeric declaration counts as absent",
			profile:    map[string]string{EnvMaxContextTokens: "one million"},
			want:       200000,
			wantSource: WindowSourceFallback,
		},
		{
			// A window of zero is not a window. Falling through is what keeps a denominator
			// from ever being 0.
			name:       "a zero declaration counts as absent",
			profile:    map[string]string{EnvMaxContextTokens: "0"},
			want:       200000,
			wantSource: WindowSourceFallback,
		},
		{
			// DecimalTokenCount accepts the whole unsigned range, so the conversion to int64 is
			// where a wrap would happen. A wrapped window is a NEGATIVE denominator, which is
			// worse than no answer at all.
			name:       "a declaration too large for int64 is skipped, not wrapped",
			profile:    map[string]string{EnvMaxContextTokens: "18446744073709551615"},
			want:       200000,
			wantSource: WindowSourceFallback,
		},
		{
			name:       "the largest declaration that does fit is honoured",
			profile:    map[string]string{EnvMaxContextTokens: "9223372036854775807"},
			want:       9223372036854775807,
			wantSource: WindowSourceDeclared,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, source := PredictContextWindow(tc.profile)
			if got != tc.want {
				t.Errorf("window = %d, want %d", got, tc.want)
			}
			if source != tc.wantSource {
				t.Errorf("source = %q, want %q", source, tc.wantSource)
			}
			if got <= 0 {
				t.Errorf("window = %d; the prediction must never hand a caller a denominator it cannot divide by", got)
			}
		})
	}
}

func TestObservedContextWindow(t *testing.T) {
	const gateway = "https://gateway.example.com"
	tests := []struct {
		name       string
		profile    map[string]string
		host       int64
		want       int64
		wantSource string
	}{
		{
			name:       "1 foreign gateway id",
			profile:    map[string]string{envModel: "gpt-5.6-sol", EnvMaxContextTokens: "1050000"},
			host:       262144,
			want:       1050000,
			wantSource: WindowSourceDeclared,
		},
		{
			name:       "1 foreign gateway id nothing declared",
			profile:    map[string]string{envModel: "gpt-5.6-sol"},
			host:       262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "2 claude id recognised",
			profile:    map[string]string{envModel: "claude-opus-5", EnvMaxContextTokens: "1000000"},
			host:       1000000,
			want:       1000000,
			wantSource: WindowSourceDeclared,
		},
		{
			name:       "2 claude id host above declaration",
			profile:    map[string]string{envModel: "claude-opus-5", EnvMaxContextTokens: "200000"},
			host:       1000000,
			want:       200000,
			wantSource: WindowSourceDeclared,
		},
		{
			name:       "3 claude id unrecognised",
			profile:    map[string]string{envModel: "claude-fable-5-2", EnvMaxContextTokens: "1000000"},
			host:       200000,
			want:       200000,
			wantSource: WindowSourceHost,
		},
		{
			name:       "4 claude id via gateway",
			profile:    map[string]string{envModel: "claude-opus-5", envBaseURL: gateway, EnvMaxContextTokens: "1000000"},
			host:       262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "5 no model id",
			profile:    map[string]string{EnvMaxContextTokens: "1000000"},
			host:       200000,
			want:       200000,
			wantSource: WindowSourceHost,
		},
		{
			name:       "7 unusable declaration empty",
			profile:    map[string]string{EnvMaxContextTokens: ""},
			host:       262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "7 unusable declaration non-numeric",
			profile:    map[string]string{EnvMaxContextTokens: "one million"},
			host:       262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "7 unusable declaration zero",
			profile:    map[string]string{EnvMaxContextTokens: "0"},
			host:       262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "7 unusable declaration over MaxInt64",
			profile:    map[string]string{EnvMaxContextTokens: "18446744073709551615"},
			host:       262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
		{
			name:       "8 nil profile",
			profile:    nil,
			host:       262144,
			want:       262144,
			wantSource: WindowSourceHost,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, source, ok := ObservedContextWindow(tc.profile, tc.host)
			if !ok {
				t.Fatalf("ObservedContextWindow(%v, %d) reported no observation", tc.profile, tc.host)
			}
			if got != tc.want || source != tc.wantSource {
				t.Errorf("ObservedContextWindow(%v, %d) = (%d, %q), want (%d, %q)",
					tc.profile, tc.host, got, source, tc.want, tc.wantSource)
			}
		})
	}
	t.Run("6 no healthy observation", func(t *testing.T) {
		for _, host := range []int64{0, -1} {
			got, source, ok := ObservedContextWindow(map[string]string{EnvMaxContextTokens: "1000000"}, host)
			if got != 0 || source != "" || ok {
				t.Errorf("ObservedContextWindow(MAX 1000000, %d) = (%d, %q, %v), want (0, \"\", false)", host, got, source, ok)
			}
		}
	})
}

// TestPredictContextWindowIsPure is the ADR-004 guard stated as behavior rather than as a
// grep. The package reads no environment at all, and the temptation
// here is specific: the declared window's name IS an environment variable, so a prediction that
// fell back to reading it would look correct on the host that set it and be wrong everywhere
// else. The caller knows which agent it is asking about; the process environment does not.
func TestPredictContextWindowIsPure(t *testing.T) {
	t.Setenv(EnvMaxContextTokens, "999999")

	got, source := PredictContextWindow(map[string]string{})
	if got != 200000 || source != WindowSourceFallback {
		t.Errorf("PredictContextWindow(empty) = (%d, %q) with %s set in the environment; "+
			"want the fallback — this package resolves from its arguments only (ADR-004)",
			got, source, EnvMaxContextTokens)
	}
	if os.Getenv(EnvMaxContextTokens) != "999999" {
		t.Fatal("the test's own fixture did not take effect, so it proves nothing")
	}
}

// TestPredictContextWindowPerAgent is the substrate half of the design's D7/H-R4 requirement:
// one factory, one startup.json, two agents on different model profiles, two different windows.
// What the substrate owes is a prediction whose answer is a function of the AGENT's profile and
// nothing factory-global.
func TestPredictContextWindowPerAgent(t *testing.T) {
	cfg := &ModelsConfig{Models: map[string]map[string]string{
		"big":   {envModel: "gpt-5.6-sol", EnvMaxContextTokens: "1000000"},
		"small": {envModel: "gpt-5.6-sol"},
	}}

	big, bigSrc := PredictContextWindow(cfg.Models["big"])
	small, smallSrc := PredictContextWindow(cfg.Models["small"])

	if big != 1000000 || bigSrc != WindowSourceDeclared {
		t.Errorf("agent on the declaring profile resolved (%d, %q), want (1000000, %q)", big, bigSrc, WindowSourceDeclared)
	}
	if small != 200000 || smallSrc != WindowSourceFallback {
		t.Errorf("agent on the silent profile resolved (%d, %q), want (200000, %q)", small, smallSrc, WindowSourceFallback)
	}
	if big == small {
		t.Error("two agents on different profiles resolved the same window; the operand is not per-agent")
	}
}

// TestPredictContextWindowIgnoresBoundTokens pins the other half of D7/H-R4. bound_tokens is
// factory-global and StepContextConfig's doc says it is a default "and nowhere else in the decision
// path" — it is written once as ctx_bound_tokens for annotation. A prediction that took a
// StartupConfig would let one factory-wide number become every agent's denominator, which is
// the mistake the decision exists to prevent. Since the bound default IS the fallback window (K6),
// the values cannot tell the two apart; the signature is the proof.
func TestPredictContextWindowIgnoresBoundTokens(t *testing.T) {
	cfg := defaultStartupConfig()
	if cfg.StepContext.BoundTokens == 0 {
		t.Fatal("the shipped default for bound_tokens is 0, so this test cannot distinguish anything")
	}

	got, source := PredictContextWindow(map[string]string{})
	if source != WindowSourceFallback {
		t.Errorf("source = %q, want %q: bound_tokens must never reach the prediction", source, WindowSourceFallback)
	}
	if got != 200000 {
		t.Errorf("window = %d, want the foreign-model fallback", got)
	}
}

func TestPairingLintUsesTheResolver(t *testing.T) {
	// The lint's cap and the prediction's fallback are the same host assumption, and they were
	// separately open-coded before #668. Two copies of one number drift; this asserts the lint
	// still speaks the prediction's answer.
	fallback, _ := PredictContextWindow(nil)
	warning, ok := PairingLintProfile("codex", map[string]string{
		envModel:         "gpt-5.6-sol",
		envCompactWindow: "220000",
	})
	if !ok {
		t.Fatal("the lint stopped firing on the shape it exists for")
	}
	if !strings.Contains(warning, "200000") {
		t.Errorf("warning %q no longer names the cap", warning)
	}
	if fallback != 200000 {
		t.Errorf("prediction fallback = %d but the lint warns about 200000; the two have drifted", fallback)
	}
}

// TestWindowFunctionsReadNoFile holds the prediction away from the observation record however a
// reader spells the path. A denylist of os and io/fs calls misses os.ReadFile passed as a value,
// a helper in another package, or a package var holding a reader, so the walk is an allowlist:
// from the two window functions, every reference reached must be provably file-free.
func TestWindowFunctionsReadNoFile(t *testing.T) {
	pkg := parseWindowPackage(t)
	roots := []string{"PredictContextWindow", "ObservedContextWindow"}
	for _, r := range roots {
		if pkg.funcs[r] == nil {
			t.Fatalf("%s is absent from the package's non-test files", r)
		}
	}

	w := &windowWalk{pkg: pkg, seen: map[ast.Node]bool{}}
	for _, r := range roots {
		w.walkFunc(pkg.funcs[r].decl, pkg.funcs[r].file)
	}
	for _, v := range w.violations {
		t.Error(v)
	}

	var consts, vars []string
	for _, f := range pkg.files {
		for _, d := range f.ast.Decls {
			gd, ok := d.(*ast.GenDecl)
			if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
				continue
			}
			for _, s := range gd.Specs {
				for _, n := range s.(*ast.ValueSpec).Names {
					if !strings.HasPrefix(n.Name, "WindowSource") {
						continue
					}
					if gd.Tok == token.CONST {
						consts = append(consts, n.Name)
					} else {
						vars = append(vars, fmt.Sprintf("%s (%s)", n.Name, pkg.fset.Position(n.Pos())))
					}
				}
			}
		}
	}
	sort.Strings(consts)
	if len(consts) != 3 || len(vars) != 0 {
		t.Errorf("WindowSource names: consts %v, vars %v; want exactly three, all constants", consts, vars)
	}
	sources := map[string]bool{}
	for _, c := range consts {
		sources[c] = true
	}

	for _, r := range roots {
		fn := pkg.funcs[r].decl
		allowEmpty := r == "ObservedContextWindow"
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if _, lit := n.(*ast.FuncLit); lit {
				return false
			}
			ret, ok := n.(*ast.ReturnStmt)
			if !ok {
				return true
			}
			pos := pkg.fset.Position(ret.Pos())
			if len(ret.Results) < 2 {
				t.Errorf("%s: a return in %s does not list its results, so its source cannot be checked", pos, r)
				return true
			}
			switch src := ret.Results[1].(type) {
			case *ast.Ident:
				if !sources[src.Name] {
					t.Errorf("%s: %s returns source %s, which is not one of %v", pos, r, src.Name, consts)
				}
			case *ast.BasicLit:
				if !(allowEmpty && src.Value == `""`) {
					t.Errorf("%s: %s returns the literal %s as a source; only the WindowSource constants are sources", pos, r, src.Value)
				}
			default:
				t.Errorf("%s: %s returns a source that is not a WindowSource constant", pos, r)
			}
			return true
		})
	}
}

type windowFile struct {
	ast     *ast.File
	imports map[string]string
}

type windowFunc struct {
	decl *ast.FuncDecl
	file *windowFile
}

type windowVar struct {
	init     ast.Expr
	file     *windowFile
	assigned bool
}

type windowPackage struct {
	fset    *token.FileSet
	files   []*windowFile
	funcs   map[string]*windowFunc
	methods map[string][]*windowFunc
	types   map[string]bool
	consts  map[string]bool
	vars    map[string]*windowVar
}

func parseWindowPackage(t *testing.T) *windowPackage {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	pkg := &windowPackage{
		fset:    token.NewFileSet(),
		funcs:   map[string]*windowFunc{},
		methods: map[string][]*windowFunc{},
		types:   map[string]bool{},
		consts:  map[string]bool{},
		vars:    map[string]*windowVar{},
	}
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(pkg.fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		wf := &windowFile{ast: f, imports: map[string]string{}}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			name := path[strings.LastIndex(path, "/")+1:]
			if imp.Name != nil {
				name = imp.Name.Name
			}
			wf.imports[name] = path
		}
		pkg.files = append(pkg.files, wf)
		for _, d := range f.Decls {
			switch d := d.(type) {
			case *ast.FuncDecl:
				fn := &windowFunc{decl: d, file: wf}
				if d.Recv == nil {
					pkg.funcs[d.Name.Name] = fn
				} else {
					pkg.methods[d.Name.Name] = append(pkg.methods[d.Name.Name], fn)
				}
			case *ast.GenDecl:
				for _, s := range d.Specs {
					switch s := s.(type) {
					case *ast.TypeSpec:
						pkg.types[s.Name.Name] = true
					case *ast.ValueSpec:
						for i, n := range s.Names {
							if d.Tok == token.CONST {
								pkg.consts[n.Name] = true
								continue
							}
							v := &windowVar{file: wf}
							if i < len(s.Values) {
								v.init = s.Values[i]
							}
							pkg.vars[n.Name] = v
						}
					}
				}
			}
		}
	}
	for _, f := range pkg.files {
		ast.Inspect(f.ast, func(n ast.Node) bool {
			as, ok := n.(*ast.AssignStmt)
			if !ok || as.Tok == token.DEFINE {
				return true
			}
			for _, lhs := range as.Lhs {
				if id, ok := lhs.(*ast.Ident); ok && pkg.vars[id.Name] != nil {
					pkg.vars[id.Name].assigned = true
				}
			}
			return true
		})
	}
	return pkg
}

var windowUniverse = map[string]bool{
	"bool": true, "byte": true, "complex64": true, "complex128": true, "error": true, "float32": true,
	"float64": true, "int": true, "int8": true, "int16": true, "int32": true, "int64": true, "rune": true,
	"string": true, "uint": true, "uint8": true, "uint16": true, "uint32": true, "uint64": true,
	"uintptr": true, "any": true, "comparable": true, "true": true, "false": true, "iota": true, "nil": true,
	"append": true, "cap": true, "clear": true, "complex": true, "copy": true, "delete": true, "imag": true,
	"len": true, "make": true, "max": true, "min": true, "new": true, "panic": true, "real": true, "recover": true,
}

var windowAllowedImports = map[string]bool{"strings": true, "strconv": true, "math": true}

type windowWalk struct {
	pkg        *windowPackage
	seen       map[ast.Node]bool
	violations []string
}

func (w *windowWalk) reject(n ast.Node, format string, args ...any) {
	w.violations = append(w.violations, fmt.Sprintf("%s: %s", w.pkg.fset.Position(n.Pos()), fmt.Sprintf(format, args...)))
}

func (w *windowWalk) walkFunc(fn *ast.FuncDecl, file *windowFile) {
	if w.seen[fn] {
		return
	}
	w.seen[fn] = true
	if fn.Body == nil {
		w.reject(fn, "%s has no body, so nothing in it can be walked", fn.Name.Name)
		return
	}
	locals := windowLocals(fn)
	if fn.Recv != nil {
		w.check(fn.Recv, file, locals)
	}
	w.check(fn.Type, file, locals)
	w.check(fn.Body, file, locals)
}

func (w *windowWalk) walkLiteral(lit *ast.FuncLit, file *windowFile) {
	if w.seen[lit] {
		return
	}
	w.seen[lit] = true
	locals := windowLocals(lit)
	w.check(lit, file, locals)
}

// windowLocals collects every name the function defines: parameters, results, receiver, type
// parameters, := and var/const/type declarations, range variables and nested literals' own.
func windowLocals(root ast.Node) map[string]bool {
	locals := map[string]bool{}
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			for _, id := range n.Names {
				locals[id.Name] = true
			}
		case *ast.AssignStmt:
			if n.Tok == token.DEFINE {
				for _, lhs := range n.Lhs {
					if id, ok := lhs.(*ast.Ident); ok {
						locals[id.Name] = true
					}
				}
			}
		case *ast.RangeStmt:
			if n.Tok == token.DEFINE {
				for _, e := range []ast.Expr{n.Key, n.Value} {
					if id, ok := e.(*ast.Ident); ok {
						locals[id.Name] = true
					}
				}
			}
		case *ast.ValueSpec:
			for _, id := range n.Names {
				locals[id.Name] = true
			}
		case *ast.TypeSpec:
			locals[n.Name.Name] = true
		}
		return true
	})
	return locals
}

func (w *windowWalk) check(root ast.Node, file *windowFile, locals map[string]bool) {
	ast.Inspect(root, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.Field:
			if n.Type != nil {
				w.check(n.Type, file, locals)
			}
			return false
		case *ast.LabeledStmt:
			w.check(n.Stmt, file, locals)
			return false
		case *ast.BranchStmt:
			return false
		case *ast.KeyValueExpr:
			if _, ok := n.Key.(*ast.Ident); !ok {
				w.check(n.Key, file, locals)
			}
			w.check(n.Value, file, locals)
			return false
		case *ast.SelectorExpr:
			if x, ok := n.X.(*ast.Ident); ok && !locals[x.Name] {
				if path, imported := file.imports[x.Name]; imported {
					if !windowAllowedImports[path] {
						w.reject(n, "%s.%s reaches package %q, which is not strings, strconv or math", x.Name, n.Sel.Name, path)
					}
					return false
				}
			}
			w.check(n.X, file, locals)
			for _, m := range w.pkg.methods[n.Sel.Name] {
				w.walkFunc(m.decl, m.file)
			}
			return false
		case *ast.Ident:
			w.resolve(n, locals)
			return false
		}
		return true
	})
}

func (w *windowWalk) resolve(id *ast.Ident, locals map[string]bool) {
	name := id.Name
	switch {
	case name == "_" || locals[name]:
	case w.pkg.funcs[name] != nil:
		w.walkFunc(w.pkg.funcs[name].decl, w.pkg.funcs[name].file)
	case w.pkg.types[name], w.pkg.consts[name]:
	case w.pkg.vars[name] != nil:
		v := w.pkg.vars[name]
		lit, isLit := v.init.(*ast.FuncLit)
		switch {
		case ast.IsExported(name):
			w.reject(id, "%s is an exported package var, which another package can assign", name)
		case !isLit:
			w.reject(id, "%s is a package var whose initializer is not a function literal", name)
		case v.assigned:
			w.reject(id, "%s is a package var that a non-test file assigns", name)
		default:
			w.walkLiteral(lit, v.file)
		}
	case windowUniverse[name]:
	default:
		w.reject(id, "%s is not a local, a package declaration or a universe name", name)
	}
}
