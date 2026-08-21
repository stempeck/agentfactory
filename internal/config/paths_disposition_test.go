package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// configFileDispositions is the row set every config FILE under .agentfactory/ must appear in.
// It is the root-side contract for issue #620's disposition table: the web console's Settings
// surface must make a deliberate choice about each config document — expose it raw, expose it
// behind a secret-redacting tier, or deliberately withhold it — and a file nobody classified is
// a file the console will silently ignore or, worse, silently publish.
//
// The tier semantics themselves ship in Phase 2 (design-doc.md:119-137); this test lands the
// ENUMERATION now, so a config file added between the phases cannot slip past unclassified.
// The web-side completeness test (TestSettings_DispositionComplete) is Phase 2's half.
//
// Adding a config file? Add its paths.go helper AND a row here, in the same change.
var configFileDispositions = map[string]string{
	"FactoryConfigPath":    "raw",
	"AgentsConfigPath":     "secret",
	"MessagingConfigPath":  "raw",
	"DispatchConfigPath":   "raw",
	"StartupConfigPath":    "raw",
	"ModelsConfigPath":     "secret",
	"TelemetryConfigPath":  "secret",
	"StatuslineConfigPath": "raw",
	"BuildHostConfigPath":  "raw",
}

// Two rows in the design's tier table — litellm.yaml and .agentfactory/secrets/ — have NO
// paths.go helper at all, so no paths.go-derived enumeration can discover them and this test
// must not claim a completeness it cannot have. They are carried by the tier table
// independently. Recorded here so a future reader does not "fix" the apparent omission.
var dispositionRowsWithoutPathHelper = []string{"litellm.yaml", ".agentfactory/secrets/"}

// TestConfigPaths_DispositionEnumeration covers issue #620 Phase 1 AC 7.
//
// paths.go is the single place a new config file gets a path helper, so it is the only place
// where "a config file exists" is mechanically observable. The test reads paths.go from SOURCE
// (go/parser, the reserved_parity_test.go idiom) rather than using reflection, because Go
// cannot enumerate a package's functions at runtime.
//
// The scan keys on the JSON FILENAME rather than on the `…ConfigPath` naming convention.
// Keying on the name would let a differently-named helper — say
// `func SecretsManifest(root string) string` returning "secrets.json" — add a config file that
// the enumeration never sees, which is precisely the hole it exists to close. The filename is
// resolved through paths.go's own package-level string constants as well as through inline
// literals, so hoisting a name into a const does not hide the helper either.
func TestConfigPaths_DispositionEnumeration(t *testing.T) {
	found := configFileHelpersFromSource(t, pathsGoPath(t))

	// Anti-vacuity: a scan that matches nothing would make both loops below trivially pass.
	if len(found) == 0 {
		t.Fatal("parsed zero config-file helpers from paths.go — the scan matches nothing, so it guards nothing")
	}

	for name, file := range found {
		if _, ok := configFileDispositions[name]; !ok {
			t.Errorf("paths.go declares %s (%s) but it has NO disposition row — every config file must be classified "+
				"(expose raw, expose secret-redacted, or deliberately withhold) before the web console can be trusted "+
				"not to silently ignore or silently publish it. Add a row to configFileDispositions.", name, file)
		}
	}
	for name := range configFileDispositions {
		if _, ok := found[name]; !ok {
			t.Errorf("configFileDispositions has a row for %s but paths.go declares no such config-file helper — "+
				"the row set has drifted from the code. Remove the row, or restore the helper.", name)
		}
	}

	t.Run("every disposition is a known tier", func(t *testing.T) {
		for _, name := range sortedCongruenceKeys(configFileDispositions) {
			switch configFileDispositions[name] {
			case "raw", "secret":
			default:
				t.Errorf("%s has disposition %q; must be \"raw\" or \"secret\"", name, configFileDispositions[name])
			}
		}
	})

	t.Run("the table-only rows are recorded", func(t *testing.T) {
		// Guards the honesty note above: if someone deletes the slice thinking it is unused,
		// this fails and they read why it exists.
		if len(dispositionRowsWithoutPathHelper) == 0 {
			t.Error("the tier table carries rows with no paths.go helper; keep them recorded so this test's scope stays honest")
		}
	})
}

// pathsGoPath resolves paths.go relative to THIS test file (runtime.Caller), so the test is
// independent of the working directory — the same reason reserved_parity_test.go does it.
func pathsGoPath(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed — cannot locate this test's directory")
	}
	return filepath.Join(filepath.Dir(thisFile), "paths.go")
}

// configFileHelpersFromSource returns every top-level func in the named file that names a
// ".json" file, mapped to the filename. Those are exactly the helpers that locate a config
// FILE; the directory helpers (ConfigDir, AgentsDir, HooksDir, …) and the non-config artifacts
// (AgentsMdPath, FormulaStorePath, StatuslineSessionsDir) name no .json file and are excluded
// by construction rather than by a maintained skip-list.
//
// The filename may be written inline or referenced through a package-level string constant;
// resolving both closes the hole where hoisting "secrets.json" into a const would make the
// helper invisible to a literals-only scan. A filename computed at runtime — concatenated,
// or returned by a function — is still out of reach, and no source scan can reach it; that is
// the residual limit of this test, not a limit anyone should route around.
func configFileHelpersFromSource(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	consts := packageStringConsts(f)
	jsonName := func(n ast.Node) (string, bool) {
		var s string
		switch e := n.(type) {
		case *ast.BasicLit:
			if e.Kind != token.STRING {
				return "", false
			}
			unquoted, err := strconv.Unquote(e.Value)
			if err != nil {
				return "", false
			}
			s = unquoted
		case *ast.Ident:
			v, ok := consts[e.Name]
			if !ok {
				return "", false
			}
			s = v
		default:
			return "", false
		}
		return s, strings.HasSuffix(s, ".json")
	}

	out := map[string]string{}
	for _, decl := range f.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv != nil || fn.Body == nil {
			continue
		}
		ast.Inspect(fn.Body, func(n ast.Node) bool {
			if name, ok := jsonName(n); ok {
				out[fn.Name.Name] = name
			}
			return true
		})
	}
	return out
}

// packageStringConsts maps each top-level const/var bound to a plain string literal to its
// value, so a helper referencing one by name resolves to the same filename an inline literal
// would have produced.
func packageStringConsts(f *ast.File) map[string]string {
	out := map[string]string{}
	for _, decl := range f.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || (gd.Tok != token.CONST && gd.Tok != token.VAR) {
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
				if v, err := strconv.Unquote(bl.Value); err == nil {
					out[name.Name] = v
				}
			}
		}
	}
	return out
}
