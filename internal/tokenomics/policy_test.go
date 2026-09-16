package tokenomics

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestPolicyIgnoresFormulaContent is AC-2, and the design's own elevation review names it as the
// claim that reopens the architecture question if it slips: a formula nobody special-cased must
// benefit with no author change, which holds only while the policy core takes no formula-content
// input at all.
//
// It has two halves on purpose. The behavioural half proves the answers do not vary with the
// formula name for the names it enumerates; the structural half proves no branch on a formula name
// EXISTS, which is the part a fixture can never establish — a switch the fixture happens not to hit
// passes a behavioural test and still breaks generalization for the formula it does hit.
func TestPolicyIgnoresFormulaContent(t *testing.T) {
	t.Run("behavioural: identical numbers give identical answers under any formula name", func(t *testing.T) {
		names := []string{
			"design-v7", // a real shipped formula, the one the design was measured on
			"a-formula-nobody-ever-wrote-a-branch-for", // never special-cased anywhere
			"", // absent join key
			"formula with spaces/and 슬래시 and 😀", // unusual bytes
			"design-v7 ", // a near-miss on a real name
		}

		cfg := config.TokenomicsConfig{
			Enabled: "on", Budget: "default", Thrift: "default", Dispatch: "default",
			Interview: "default", Effort: "default", Escalate: "default",
			AdmissionMarginPct: 10, LearnedMinRuns: 2,
		}
		win := ResolveWindow(map[string]string{config.EnvMaxContextTokens: "262144"}, 0)
		occ := Occupancy{Tokens: 190_000, Known: true}
		// MedianMarginalCtxTokens matches the peak so the additive AppetiteFor returns a known figure and
		// the decision stays a real no-fit across every name — without it the marginal fix would degrade
		// each name to observe and the invariance would hold trivially.
		agg := Aggregate{Runs: 5, MedianPeakCtxTokens: 90_000, MedianMarginalCtxTokens: 90_000, MaxPeakCtxTokens: 120_000, UpdatedAt: "2026-08-30T00:00:00Z"}

		var (
			firstPolicy   Policy
			firstDecision Decision
			firstAdvisory string
			firstKey      string
		)
		for i, name := range names {
			// The digest is keyed on the formula NAME — that is the join key AC-2 permits — and
			// each name gets its own entry holding byte-identical numbers. If the name were a
			// branch rather than a key, these would diverge.
			d := NewDigest()
			key := DigestKey{Formula: name, StepID: "phase-2", Model: "qwen3.8-27b"}
			d.Put(key, agg)

			p := ResolvePolicy(true, cfg)
			dec := Admit(win, occ, d.AppetiteFor(key), p)
			adv, ok := RenderAdvisory(MechanismBudget, AdvisoryInputs{
				WindowTokens:   win.Tokens,
				FreeTokens:     win.Tokens - occ.Tokens,
				AppetiteTokens: agg.MedianPeakCtxTokens,
				ProjectedPct:   dec.ProjectedPct,
				PriorRuns:      agg.Runs,
			})
			if !ok {
				t.Fatal("the budget advisory has no template; this assertion would prove nothing")
			}

			if i == 0 {
				firstPolicy, firstDecision, firstAdvisory, firstKey = p, dec, adv, key.String()
				continue
			}
			if !p.Equal(firstPolicy) {
				t.Errorf("formula %q resolved a different policy:\n got %+v\nwant %+v", name, p, firstPolicy)
			}
			if dec != firstDecision {
				t.Errorf("formula %q produced a different decision:\n got %+v\nwant %+v", name, dec, firstDecision)
			}
			if adv != firstAdvisory {
				t.Errorf("formula %q rendered a different advisory:\n got %q\nwant %q", name, adv, firstAdvisory)
			}
			// The join key MUST differ — that is what makes it a key. A codec that collapsed
			// every formula onto one aggregate would pass every assertion above for the wrong
			// reason.
			if key.String() == firstKey {
				t.Errorf("formula %q produced the same digest key as %q; the name is not joining anything", name, names[0])
			}
		}
	})

	t.Run("behavioural: the decision tracks the numbers, not the name", func(t *testing.T) {
		// The inverse control. If every name gives the same answer because the predicate always
		// gives the same answer, the half above is vacuous.
		cfg := config.TokenomicsConfig{Enabled: "on", AdmissionMarginPct: 10, LearnedMinRuns: 2}
		p := ResolvePolicy(true, cfg)
		win := Window{Tokens: 200_000, Source: config.WindowSourceDeclared}
		cheap := Admit(win, Occupancy{Tokens: 10_000, Known: true}, trusted(10_000, 5), p)
		dear := Admit(win, Occupancy{Tokens: 150_000, Known: true}, trusted(100_000, 5), p)
		if cheap.Verdict != VerdictAdmit || dear.Verdict != VerdictNoFit {
			t.Fatalf("the predicate does not discriminate on numbers (cheap=%s, dear=%s); the name-invariance assertions prove nothing",
				cheap.Verdict, dear.Verdict)
		}
	})

	// The scan keys on identifier SPELLING, so a branch reached one hop away through a renamed
	// parameter — func f(name string) { switch name {...} } — escapes it. That bound is deliberate:
	// closing it needs dataflow analysis, and this is a drift guard against the well-intentioned
	// special case, not a defence against someone hiding one. The subtest is named for what it
	// proves rather than for what it is aimed at.
	t.Run("structural: nothing in the package branches on an identifier spelled like a formula", func(t *testing.T) {
		scanned, findings := scanForFormulaNameBranches(t, ".")
		if scanned == 0 {
			t.Fatal("scanned no source files; the guard proves nothing")
		}
		for _, f := range findings {
			t.Errorf("%s: %s\n"+
				"The formula name is a join key into the learned digest, never a branch (#668 AC-2). "+
				"A policy that reads formula content stops generalizing to formulas nobody special-cased, "+
				"which is the claim the design's elevation review says reopens the architecture question.", f.pos, f.what)
		}
	})

	t.Run("structural: no shipped formula name appears as a literal in the package", func(t *testing.T) {
		shipped := shippedFormulaNames(t)
		if len(shipped) == 0 {
			t.Fatal("found no shipped formula names to search for; the guard proves nothing")
		}
		scanned, hits := scanForFormulaNameLiterals(t, ".", shipped)
		if scanned == 0 {
			t.Fatal("scanned no source files; the guard proves nothing")
		}
		for _, h := range hits {
			t.Errorf("%s: string literal %q names a shipped formula. "+
				"Nothing in this package may know what any particular formula is called.", h.pos, h.what)
		}
	})
}

// TestResolvePolicyDefaults pins the "default" resolution the design fixes at design-doc.md:137:
// budget/interview/effort/thrift/dispatch resolve ON when the umbrella is on; escalate resolves
// OFF, because it moves work to a different backend. Nothing resolves on when the umbrella is off.
func TestResolvePolicyDefaults(t *testing.T) {
	allDefault := config.TokenomicsConfig{
		Enabled: "default", Budget: "default", Thrift: "default", Dispatch: "default",
		Interview: "default", Effort: "default", Escalate: "default",
		AdmissionMarginPct: 10, LearnedMinRuns: 2,
	}

	t.Run("umbrella on", func(t *testing.T) {
		p := ResolvePolicy(true, allDefault)
		want := map[Mechanism]bool{
			MechanismBudget: true, MechanismThrift: true, MechanismDispatch: true,
			MechanismInterview: true, MechanismEffort: true, MechanismEscalate: false,
		}
		for m, w := range want {
			if got := p.On(m); got != w {
				t.Errorf("with the umbrella on, %q resolved to %v, want %v", m, got, w)
			}
		}
	})

	t.Run("umbrella off silences everything, including explicit on", func(t *testing.T) {
		explicit := allDefault
		explicit.Budget, explicit.Thrift, explicit.Dispatch = "on", "on", "on"
		explicit.Interview, explicit.Effort, explicit.Escalate = "on", "on", "on"
		p := ResolvePolicy(false, explicit)
		for _, m := range Mechanisms() {
			if p.On(m) {
				t.Errorf("with the umbrella off, %q resolved on; the umbrella is the whole switch", m)
			}
		}
	})

	t.Run("an unset key resolves exactly as the word default does", func(t *testing.T) {
		// An operator who omits a key entirely lands here, and it is the commonest way to reach
		// this package: config's loader is tolerant, so an absent mechanism arrives as "". Reading
		// that as anything other than "default" would give the omitted spelling and the written
		// spelling different postures for the same intent.
		unset := ResolvePolicy(true, config.TokenomicsConfig{Enabled: "on", AdmissionMarginPct: 10, LearnedMinRuns: 2})
		written := ResolvePolicy(true, allDefault)
		for _, m := range Mechanisms() {
			if unset.On(m) != written.On(m) {
				t.Errorf("mechanism %q: unset resolved %v but \"default\" resolved %v", m, unset.On(m), written.On(m))
			}
		}
		// Anti-vacuity: the two agreeing on "everything off" would satisfy the loop above without
		// saying anything about the default posture.
		if !unset.On(MechanismBudget) || unset.On(MechanismEscalate) {
			t.Errorf("unset policy = %+v; want budget on and escalate off, which is what \"default\" means", unset)
		}
	})

	t.Run("explicit values override the default", func(t *testing.T) {
		cfg := allDefault
		cfg.Budget = "off"
		cfg.Escalate = "on"
		p := ResolvePolicy(true, cfg)
		if p.On(MechanismBudget) {
			t.Error("an explicit budget=off resolved on")
		}
		if !p.On(MechanismEscalate) {
			t.Error("an explicit escalate=on resolved off; the operator asked for it")
		}
	})

	t.Run("numeric knobs are clamped into the range config validates", func(t *testing.T) {
		p := ResolvePolicy(true, config.TokenomicsConfig{Enabled: "on", AdmissionMarginPct: 900, LearnedMinRuns: 0})
		if p.AdmissionMarginPct != 100 {
			t.Errorf("AdmissionMarginPct = %d, want 100", p.AdmissionMarginPct)
		}
		if p.LearnedMinRuns != 1 {
			t.Errorf("LearnedMinRuns = %d, want 1", p.LearnedMinRuns)
		}
	})

	t.Run("efficiency comes on with the umbrella and off with it", func(t *testing.T) {
		// Efficiency resolves ON at "default", where escalate resolves off: escalate moves work to
		// another backend and is a deliberate choice, while reducing a step's wasted generation
		// changes no backend and costs nothing to inherit. That asymmetry is the whole of #678 K3.
		if p := ResolvePolicy(true, allDefault); !p.EfficiencyOn {
			t.Error("an unset efficiency key resolved off; the baseline is unconditional")
		}
		explicit := allDefault
		explicit.Efficiency = "on"
		if p := ResolvePolicy(false, explicit); p.EfficiencyOn {
			t.Error("the umbrella off with efficiency explicitly on resolved on; the umbrella is absolute")
		}
		off := allDefault
		off.Efficiency = "off"
		if p := ResolvePolicy(true, off); p.EfficiencyOn {
			t.Error("an explicit efficiency=off resolved on")
		}
	})

	t.Run("efficiency is not a mechanism", func(t *testing.T) {
		// Every member of the mechanism vocabulary answers "does this step fit its window?".
		// Efficiency answers "is this step generating more than its work needs?", which has no window
		// in it — so a caller walking Mechanisms() to render or resolve a capacity posture must not
		// pick it up. config.tokenomicsEnums mirrors this list, and a seventh entry here would make
		// the contract-doc test demand a USING_TOKENOMICS.md row for a mechanism that is not one.
		for _, m := range Mechanisms() {
			if string(m) == "efficiency" {
				t.Fatal("efficiency joined the mechanism vocabulary; it is Policy.EfficiencyOn, a switch a caller has to name")
			}
		}
	})

	t.Run("the efficiency operands are clamped into the range config validates", func(t *testing.T) {
		p := ResolvePolicy(true, config.TokenomicsConfig{
			Enabled: "on", EfficiencyEffortLevel: "low",
			EfficiencyThinkingSharePct: 900, EfficiencyRepeatReadFloor: -3, EfficiencyMaxRelaunches: -1,
		})
		if p.EfficiencyThinkingSharePct != 100 {
			t.Errorf("EfficiencyThinkingSharePct = %d, want 100", p.EfficiencyThinkingSharePct)
		}
		if p.EfficiencyRepeatReadFloor != 0 || p.EfficiencyMaxRelaunches != 0 {
			t.Errorf("counted operands = %d/%d, want 0/0 — a negative count inverts the comparison it feeds",
				p.EfficiencyRepeatReadFloor, p.EfficiencyMaxRelaunches)
		}
		// The effort level is carried, not clamped: it is the one operand config validates by
		// membership, so it arrives already checked and there is no nearest legal value to fall to.
		if p.EfficiencyEffortLevel != "low" {
			t.Errorf("EfficiencyEffortLevel = %q, want %q", p.EfficiencyEffortLevel, "low")
		}
		below := ResolvePolicy(true, config.TokenomicsConfig{Enabled: "on", EfficiencyThinkingSharePct: -1})
		if below.EfficiencyThinkingSharePct != 1 {
			t.Errorf("EfficiencyThinkingSharePct = %d, want 1 — a threshold of nothing is met by every "+
				"step that ever generated anything", below.EfficiencyThinkingSharePct)
		}
	})

	t.Run("the mechanism vocabulary matches the config block", func(t *testing.T) {
		// The seven names api.md fixes, minus the umbrella. A mechanism that exists in one place
		// and not the other is a policy nobody can address.
		want := []Mechanism{MechanismBudget, MechanismThrift, MechanismDispatch,
			MechanismInterview, MechanismEffort, MechanismEscalate}
		got := Mechanisms()
		if len(got) != len(want) {
			t.Fatalf("Mechanisms() = %v, want %v", got, want)
		}
		for i := range want {
			if got[i] != want[i] {
				t.Errorf("Mechanisms()[%d] = %q, want %q", i, got[i], want[i])
			}
		}
	})
}

// TestPackageReadsNoAmbientState is the structural half of the purity rule #668 gives this package
// and #519 gives the codebase (internal/formula/discover.go:19-25): a package below the CLI seam
// takes its inputs as arguments and discovers nothing for itself.
//
// TestPredicateIsPure asserts the consequence — repeated calls agree — but agreement is exactly what
// an ambient read gives you within a single fast test run: a clock read twice inside one millisecond
// returns the same value, and an env var nobody sets is stably empty. Only the absence of the call
// can be asserted, and only structurally.
//
// The filesystem is deliberately NOT on this list. LoadDigest and SaveDigest take a path argument,
// which is the injected form the rule permits; what the rule forbids is finding that path.
func TestPackageReadsNoAmbientState(t *testing.T) {
	banned := map[string]string{
		"Now":       "reads the clock; every decision here must be a function of its arguments",
		"Getenv":    "reads the environment; config values arrive as a config.TokenomicsConfig argument",
		"LookupEnv": "reads the environment; config values arrive as a config.TokenomicsConfig argument",
		"Getwd":     "discovers a location; paths arrive as arguments (#519)",
		"FindRoot":  "resolves the factory root, which belongs at the CLI seam (#519)",
		"Command":   "shells out; nothing below the seam may run a subprocess",
	}

	fset := token.NewFileSet()
	scanned := 0
	for _, path := range packageSources(t, ".") {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if why, bad := banned[sel.Sel.Name]; bad {
				t.Errorf("%s: call to %s — %s", fset.Position(call.Pos()), sel.Sel.Name, why)
			}
			return true
		})
	}
	if scanned == 0 {
		t.Fatal("scanned no source files; the guard proves nothing")
	}
}

type sourceFinding struct {
	pos  string
	what string
}

// formulaIdents are the spellings a formula name can plausibly carry in this package. The scan is
// deliberately over-broad: a false positive costs one rename, a false negative costs the AC.
var formulaIdents = map[string]bool{"formula": true, "Formula": true, "formulaname": true, "formulaName": true, "FormulaName": true}

// scanForFormulaNameBranches parses every non-test source file in dir and reports switch tags and
// equality comparisons whose operands reach a formula name. It follows the AST-scan idiom the
// internal/cmd guards use (telemetry_deref_sites_test.go, findroot_drift_test.go): parse rather
// than grep, so a mention inside a comment or a string is not mistaken for a branch.
func scanForFormulaNameBranches(t *testing.T, dir string) (scanned int, findings []sourceFinding) {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range packageSources(t, dir) {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			switch node := n.(type) {
			case *ast.SwitchStmt:
				if node.Tag != nil && mentionsFormula(node.Tag) {
					findings = append(findings, sourceFinding{fset.Position(node.Pos()).String(), "switch on a formula name"})
				}
			case *ast.BinaryExpr:
				if node.Op != token.EQL && node.Op != token.NEQ {
					return true
				}
				if mentionsFormula(node.X) || mentionsFormula(node.Y) {
					findings = append(findings, sourceFinding{fset.Position(node.Pos()).String(), "equality comparison against a formula name"})
				}
			}
			return true
		})
	}
	return scanned, findings
}

// mentionsFormula reports whether expr reads a formula name, through a bare identifier, a field
// selector, or a method call on either.
func mentionsFormula(expr ast.Expr) bool {
	found := false
	ast.Inspect(expr, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.Ident:
			if formulaIdents[node.Name] {
				found = true
			}
		case *ast.SelectorExpr:
			if formulaIdents[node.Sel.Name] {
				found = true
			}
		}
		return !found
	})
	return found
}

// scanForFormulaNameLiterals reports any string literal in the package that equals a shipped
// formula name. Even outside a branch, such a literal is a formula this package knows about.
func scanForFormulaNameLiterals(t *testing.T, dir string, shipped map[string]bool) (scanned int, hits []sourceFinding) {
	t.Helper()
	fset := token.NewFileSet()
	for _, path := range packageSources(t, dir) {
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}
		scanned++
		ast.Inspect(file, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			val := strings.Trim(lit.Value, "`\"")
			if shipped[val] {
				hits = append(hits, sourceFinding{fset.Position(lit.Pos()).String(), val})
			}
			return true
		})
	}
	return scanned, hits
}

// packageSources lists the non-test .go files of the package under test. `go test` runs with the
// package directory as the working directory, so "." is this package and nothing else.
func packageSources(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var paths []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		paths = append(paths, filepath.Join(dir, name))
	}
	return paths
}

// shippedFormulaNames reads the names of the formulas this binary installs. They are the concrete
// values a well-intentioned special case would be written against.
func shippedFormulaNames(t *testing.T) map[string]bool {
	t.Helper()
	dir := filepath.Join("..", "cmd", "install_formulas")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	names := map[string]bool{}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".formula.toml")
		if name == e.Name() {
			continue
		}
		names[name] = true
	}
	return names
}

// TestPolicyEqualCoversEveryField is a structural guard on Policy.Equal, and it exists because the
// failure it catches is silent. Equal is written field by field, so a Policy that gains an operand
// keeps compiling and keeps passing every behavioural test — it just starts reporting two different
// postures as the same one, and the tests that use Equal to assert "these two resolutions agree"
// quietly stop asserting anything about the new field.
//
// The walk is reflective rather than a list of names for the same reason: a list is a second place
// to remember, and the guard would then need the very discipline it exists to replace.
func TestPolicyEqualCoversEveryField(t *testing.T) {
	base := ResolvePolicy(true, config.TokenomicsConfig{
		Enabled: "on", AdmissionMarginPct: 10, LearnedMinRuns: 2,
		Efficiency: "on", EfficiencyEffortLevel: "medium",
		EfficiencyThinkingSharePct: 80, EfficiencyRepeatReadFloor: 1, EfficiencyMaxRelaunches: 6,
	}).WithContextThreshold(85)

	typ := reflect.TypeOf(base)
	perturbed := 0
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue // the mechanism map, covered by the subtest below
		}
		other := base
		fv := reflect.ValueOf(&other).Elem().Field(i)
		switch fv.Kind() {
		case reflect.Bool:
			fv.SetBool(!fv.Bool())
		case reflect.Int:
			fv.SetInt(fv.Int() + 1)
		case reflect.String:
			fv.SetString(fv.String() + "-other")
		default:
			t.Fatalf("Policy.%s is a %s and this guard cannot perturb it; teach the switch about the "+
				"new kind rather than letting the field go unchecked", f.Name, fv.Kind())
		}
		perturbed++
		if base.Equal(other) {
			t.Errorf("Policy.Equal ignores %s: two policies differing only in that field compare equal", f.Name)
		}
	}
	if perturbed == 0 {
		t.Fatal("perturbed no fields; the guard proves nothing")
	}

	t.Run("a differing mechanism is a differing policy", func(t *testing.T) {
		for _, m := range Mechanisms() {
			other := ResolvePolicy(true, config.TokenomicsConfig{
				Enabled: "on", AdmissionMarginPct: 10, LearnedMinRuns: 2,
				Efficiency: "on", EfficiencyEffortLevel: "medium",
				EfficiencyThinkingSharePct: 80, EfficiencyRepeatReadFloor: 1, EfficiencyMaxRelaunches: 6,
			}).WithContextThreshold(85)
			other.on[m] = !other.on[m]
			if base.Equal(other) {
				t.Errorf("Policy.Equal ignores mechanism %q", m)
			}
		}
	})

	t.Run("a policy equals itself", func(t *testing.T) {
		// Anti-vacuity: an Equal that always returned false would satisfy every row above.
		if !base.Equal(base) {
			t.Error("a policy does not equal itself; every assertion above passes for free")
		}
	})
}
