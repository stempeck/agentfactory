package config

import (
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/exec"
)

// rootPathsGo is af-core's paths.go — the file inventory the tier table's row set is answerable to.
// It resolves through the package's single repo-relative walk-up (congruence_test.go:rootConfigDir).
func rootPathsGo(t *testing.T) string {
	t.Helper()
	return filepath.Join(rootConfigDir(t), "paths.go")
}

// configFilesFromRootSource returns every config-file BASENAME af-core's paths.go names, by parsing
// the source. It keys on the ".json" filename rather than on the `…ConfigPath` naming convention,
// exactly as internal/config/paths_disposition_test.go does: keying on the name would let a
// differently-named helper add a config file the enumeration never sees, which is the hole this
// closes. paths.go's directory helpers name no .json file and are excluded by construction rather
// than by a maintained skip-list. Go cannot enumerate a package's functions at runtime, and this
// module cannot import the package at all, so a source scan is the only mechanism available.
func configFilesFromRootSource(t *testing.T, path string) map[string]string {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}

	// Package-level string consts, so hoisting a filename into a const does not hide the helper.
	consts := map[string]string{}
	for _, d := range f.Decls {
		gd, ok := d.(*ast.GenDecl)
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
				if bl, ok := vs.Values[i].(*ast.BasicLit); ok && bl.Kind == token.STRING {
					if s, err := strconv.Unquote(bl.Value); err == nil {
						consts[name.Name] = s
					}
				}
			}
		}
	}

	found := map[string]string{}
	for _, d := range f.Decls {
		fd, ok := d.(*ast.FuncDecl)
		if !ok || fd.Recv != nil || fd.Body == nil {
			continue
		}
		ast.Inspect(fd.Body, func(n ast.Node) bool {
			var s string
			switch e := n.(type) {
			case *ast.BasicLit:
				if e.Kind == token.STRING {
					s, _ = strconv.Unquote(e.Value)
				}
			case *ast.Ident:
				s = consts[e.Name]
			}
			if strings.HasSuffix(s, ".json") {
				found[fd.Name.Name] = s
			}
			return true
		})
	}
	return found
}

// TestSettings_DispositionComplete is the web-side half of the #620 disposition contract; af-core's
// internal/config/paths_disposition_test.go is the other half and names this test by hand.
//
// It answers the row set to af-core's REAL file inventory rather than to a list someone typed: add a
// tenth config file to paths.go without classifying it here, and this test goes red in the web
// module. That is the property that keeps the console from silently ignoring — or silently
// publishing — a document nobody made a decision about.
func TestSettings_DispositionComplete(t *testing.T) {
	found := configFilesFromRootSource(t, rootPathsGo(t))

	// Anti-vacuity: a scan that matches nothing would make the loop below trivially pass.
	if len(found) == 0 {
		t.Fatal("parsed zero config-file helpers from af-core's paths.go — the scan matches nothing, so it guards nothing")
	}

	// Every config file af-core knows has EXACTLY ONE row.
	for helper, file := range found {
		noun := strings.TrimSuffix(file, ".json")
		n := 0
		for _, r := range tierRows {
			if r.File == noun {
				n++
			}
		}
		switch n {
		case 1:
		case 0:
			t.Errorf("af-core's paths.go declares %s (%s) but the tier table has NO row for %q — every config "+
				"file must be classified (served raw, served through a secret-free projection, or deliberately "+
				"withheld) before the console can be trusted not to ignore or publish it. Add a row to tierRows.",
				helper, file, noun)
		default:
			t.Errorf("the tier table has %d rows for %q; exactly one is required — duplicates make the derived "+
				"write allowlist order-dependent", n, noun)
		}
	}

	// The table may carry MORE than paths.go can discover, and does: two rows have no path helper at
	// all, so this direction asserts containment, not equality.
	for _, r := range tierRows {
		if contains(tierRowsWithoutPathHelper, r.File) {
			continue
		}
		seen := false
		for _, file := range found {
			if strings.TrimSuffix(file, ".json") == r.File {
				seen = true
				break
			}
		}
		if !seen {
			t.Errorf("the tier table has a row for %q but af-core's paths.go declares no such config file, and it "+
				"is not one of the recorded helper-less rows (%v) — the row set has drifted from the code. Remove "+
				"the row, restore the helper, or record it as helper-less.", r.File, tierRowsWithoutPathHelper)
		}
	}

	t.Run("the helper-less rows are present and recorded", func(t *testing.T) {
		// Guards the honesty note in tier.go: if someone deletes the slice thinking it is unused,
		// this fails and they read why it exists.
		if len(tierRowsWithoutPathHelper) == 0 {
			t.Fatal("the tier table carries rows af-core's paths.go cannot discover; keep them recorded so this test's scope stays honest")
		}
		for _, file := range tierRowsWithoutPathHelper {
			if _, ok := rowFor(file); !ok {
				t.Errorf("%q is recorded as a helper-less tier row but has no row in the table", file)
			}
		}
	})

	t.Run("every row states a tier, a reason and an effective-when", func(t *testing.T) {
		for _, r := range tierRows {
			switch r.Tier {
			case TierRaw, TierProjected, TierExcluded:
			default:
				t.Errorf("%s has tier %q, which is not one of raw/projected/excluded", r.File, r.Tier)
			}
			if strings.TrimSpace(r.Reason) == "" {
				t.Errorf("%s has an empty reason — the reason IS the operator-facing explanation and the "+
					"400 message; a row without one classifies nothing", r.File)
			}
			// "n/a" is the legitimate value for the two rows nothing can change; the empty string is
			// not, because it renders as a blank field rather than as an answer.
			if strings.TrimSpace(r.EffectiveWhen) == "" {
				t.Errorf("%s has an empty effective_when — say when a change takes effect, or say %q", r.File, "n/a")
			}
		}
	})

	t.Run("a path constructor exists exactly where a document is read", func(t *testing.T) {
		// The interlock: an excluded row carries no way to name its file, so "the console never reads
		// telemetry.json" holds by absence rather than by an if-statement someone could delete.
		for _, r := range tierRows {
			switch r.Tier {
			case TierRaw, TierProjected:
				if r.path == nil {
					t.Errorf("%s is tier %q but has no path constructor — it can never be served", r.File, r.Tier)
				}
			case TierExcluded:
				if r.path != nil {
					t.Errorf("%s is excluded but carries a path constructor — an excluded file must have no way "+
						"to be named, or the exclusion is only a convention", r.File)
				}
			}
		}
	})
}

// TestSettings_RawTierExcludesSecretFiles is design S1: the raw pipeline and the secret-bearing files
// do not intersect. Asserted through BEHAVIOUR over a factory where every file genuinely exists on
// disk — a set-intersection over the table alone would only restate the table.
func TestSettings_RawTierExcludesSecretFiles(t *testing.T) {
	root := fixtureFactory(t, "dispatch", "startup", "messaging", "statusline", "factory", "agents", "models", "telemetry")
	svc := New(root, nil)
	view, err := svc.Read(context.Background())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}

	// Negative arm: nothing secret-bearing is served as a document, however populated it is on disk.
	for _, file := range []string{"agents", "models", "telemetry", "litellm.yaml", ".agentfactory/secrets/"} {
		fv, ok := view.Files[file]
		if !ok {
			continue // no row is served ⇒ nothing exposed; the completeness test owns row presence
		}
		if len(fv.Doc) != 0 {
			t.Errorf("files[%q].doc is populated — a secret-bearing file must never be served raw:\n%s", file, fv.Doc)
		}
		if strings.TrimSpace(fv.Reason) == "" {
			t.Errorf("files[%q] serves no document and no reason — the operator is told nothing", file)
		}
	}

	// Positive arm: without it, a Read that returned nothing at all would pass the negative arm.
	for _, file := range []string{"dispatch", "startup", "messaging", "statusline", "factory"} {
		if len(view.Files[file].Doc) == 0 {
			t.Errorf("files[%q].doc is empty, but the file is on disk and its tier is raw", file)
		}
	}

	// The table and the behaviour must agree.
	for _, r := range tierRows {
		if r.Tier != TierRaw {
			continue
		}
		for _, secret := range []string{"agents", "models", "telemetry", "litellm.yaml", ".agentfactory/secrets/"} {
			if r.File == secret {
				t.Errorf("%q is in the raw tier and in the secret set — raw ∩ secret must be empty", r.File)
			}
		}
	}

	// The projections still work, and are projections rather than pass-throughs.
	if len(view.Agents) == 0 {
		t.Error("the agent roster is empty — the AgentSummary projection stopped working")
	}
	if len(view.Profiles) == 0 {
		t.Error("the model-profile list is empty — the names-only projection stopped working")
	}
	for _, p := range view.Profiles {
		if strings.Contains(p, "{") || strings.Contains(p, "ANTHROPIC") {
			t.Errorf("profile entry %q looks like a profile BODY, not a name — the projection is leaking", p)
		}
	}

	// Every projected row must actually HAVE a decode target. Read's switch fails loud on a row that
	// does not, and this drives that arm directly: a future secret-bearing file marked TierProjected
	// and then forgotten would otherwise present as an empty picker rather than as an error.
	for _, r := range tierRows {
		if r.Tier != TierProjected {
			continue
		}
		if err := svc.project(r, &Settings{}); err != nil {
			t.Errorf("the projected row %q has no secret-free decode target: %v", r.File, err)
		}
	}
	if err := svc.project(Row{File: "a-row-nobody-wrote-a-projection-for", path: agentsPath}, &Settings{}); err == nil {
		t.Error("an unprojectable row was accepted silently — the fail-loud arm is gone")
	}
}

// TestSettings_AllowlistsEqual pins the two write allowlists — config.Service.Write's, derived from
// the tier table, and exec.Wrapper.ConfigSet's module-local copy — to identical verdicts.
//
// Comparing two exported variables would be structural and would pass trivially the moment both read
// the same symbol; it would prove nothing about the code paths an actual request takes. So each probe
// is pushed through BOTH real entry points and the verdicts are required to agree with each other and
// with the tier table.
func TestSettings_AllowlistsEqual(t *testing.T) {
	probes := []string{
		"dispatch", "startup", "messaging", "statusline", // writable
		"factory", "agents", "models", "telemetry", "build-host", "litellm.yaml", ".agentfactory/secrets/",
		"", "../dispatch", "dispatch set", "DISPATCH", "nonexistent", // never
	}

	writable := map[string]bool{}
	for _, f := range WritableFiles() {
		writable[f] = true
	}
	if len(writable) == 0 {
		t.Fatal("the tier table derives an EMPTY write allowlist — nothing would be editable")
	}

	accepted, rejected := 0, 0
	for _, file := range probes {
		svcFR := &fakeRunner{}
		svc := New(t.TempDir(), exec.NewWrapper(svcFR, ""))
		_, svcErr := svc.Write(context.Background(), file, []byte(`{}`), "")

		wrapFR := &fakeRunner{}
		_, wrapErr := exec.NewWrapper(wrapFR, "").ConfigSet(context.Background(), file, []byte(`{}`), "")

		if (svcErr == nil) != (wrapErr == nil) {
			t.Errorf("the two allowlists disagree about %q: config.Service.Write err=%v, exec.Wrapper.ConfigSet err=%v — "+
				"one layer accepts what the other refuses, so growing one without the other has gone unnoticed",
				file, svcErr, wrapErr)
		}
		if svcFR.writes != wrapFR.writes {
			t.Errorf("the two allowlists disagree about reachability for %q: %d vs %d af invocations", file, svcFR.writes, wrapFR.writes)
		}

		if want := writable[file]; want != (svcErr == nil) {
			t.Errorf("the tier table says writable(%q)=%v but config.Service.Write %s it (err=%v) — the allowlist "+
				"has drifted from the disposition the GET payload advertises", file, want, acceptedWord(svcErr == nil), svcErr)
		}

		if svcErr == nil {
			accepted++
			continue
		}
		rejected++
		if !errors.Is(svcErr, ErrNotWritable) {
			t.Errorf("rejecting %q produced %v, which does not match ErrNotWritable — the handler maps that sentinel to a 400", file, svcErr)
		}
		if svcFR.writes != 0 {
			t.Errorf("rejecting %q still spawned af (%d writes) — the membership check must run BEFORE any exec, so a "+
				"caller can never smuggle an arbitrary subcommand as the second argv element", file, svcFR.writes)
		}
		// A refusal must explain itself from the table, not from a sentence that goes stale.
		if row, ok := rowFor(file); ok && !strings.Contains(svcErr.Error(), row.Reason) {
			t.Errorf("refusing %q did not carry its disposition reason.\n got: %v\nwant it to contain: %s", file, svcErr, row.Reason)
		}
	}

	if accepted == 0 || rejected == 0 {
		t.Fatalf("anti-vacuity: %d accepted, %d rejected — a probe set that is all one way tests nothing", accepted, rejected)
	}
	if got := WritableFiles(); !equalStrings(got, []string{"dispatch", "messaging", "startup", "statusline"}) {
		t.Errorf("the derived write allowlist is %v, want the four files af-core ships a console setter for", got)
	}
}

func acceptedWord(ok bool) string {
	if ok {
		return "accepts"
	}
	return "refuses"
}

func contains(hay []string, needle string) bool {
	for _, s := range hay {
		if s == needle {
			return true
		}
	}
	return false
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	a = append([]string(nil), a...)
	b = append([]string(nil), b...)
	sort.Strings(a)
	sort.Strings(b)
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
