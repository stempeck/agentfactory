//go:build !integration

package cmd

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// proseOf711 collapses whitespace and drops comment leaders: a phrase wrapped across comment lines
// must still match, so a banned phrase cannot hide behind a line break.
func proseOf711(src string) string {
	var words []string
	for _, w := range strings.Fields(src) {
		if w == "//" || w == "#" {
			continue
		}
		words = append(words, w)
	}
	return strings.Join(words, " ")
}

func readRepoFile711(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(findModuleRoot(t), rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

func funcDoc711(t *testing.T, rel, recv, name string) (*ast.FuncDecl, string) {
	t.Helper()
	file, err := parser.ParseFile(token.NewFileSet(), filepath.Join(findModuleRoot(t), rel), nil, parser.ParseComments)
	if err != nil {
		t.Fatalf("parse %s: %v", rel, err)
	}
	for _, d := range file.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Name.Name != name {
			continue
		}
		if recv != "" {
			if fn.Recv == nil || len(fn.Recv.List) != 1 {
				continue
			}
			star, ok := fn.Recv.List[0].Type.(*ast.StarExpr)
			if !ok {
				continue
			}
			if id, ok := star.X.(*ast.Ident); !ok || id.Name != recv {
				continue
			}
		}
		if fn.Doc == nil {
			return fn, ""
		}
		return fn, fn.Doc.Text()
	}
	return nil, ""
}

var sentenceSplit711 = regexp.MustCompile(`\.\s+`)

func TestFableIncr711_T1_InterventionsHelpScopesSilenceToRecords(t *testing.T) {
	var para string
	for _, p := range strings.Split(interventionsCmd.Long, "\n\n") {
		if strings.Contains(p, "--since") {
			para = proseOf711(p)
		}
	}
	if para == "" {
		t.Fatal("`af turn interventions --help` no longer describes the --since boundary")
	}
	if strings.Contains(para, "Nothing is printed when") {
		t.Errorf("T1: the help says nothing is printed on an unknown boundary or unreadable log; a reduced "+
			"session's standing line is printed regardless:\n%s", para)
	}
	for _, want := range []string{"record", "standing", "ADR-007", "byte-identical"} {
		if !strings.Contains(para, want) {
			t.Errorf("T1: the --since paragraph must scope its silence to the per-turn records, say the standing "+
				"line is still printed, and keep the ADR-007 exit-0 and byte-identical guarantees (missing %q):\n%s",
				want, para)
		}
	}
	for _, sentence := range sentenceSplit711.Split(para, -1) {
		if strings.Contains(sentence, "byte-identical") &&
			!strings.Contains(sentence, "empty") && !strings.Contains(strings.ToLower(sentence), "nothing is printed") {
			t.Errorf("T1: byte-identical is only true when nothing is printed; the sentence does not say so: %q", sentence)
		}
	}
	if strings.Contains(para, "breadcrumb") {
		t.Errorf("T1: the --since paragraph names a breadcrumb:\n%s", para)
	}
}

func TestFableIncr711_T3_WithEffortLevelDocNamesDeclaredLevelNotEnv(t *testing.T) {
	fn, raw := funcDoc711(t, "internal/cmd/tokenomics_admission.go", "", "withEffortLevel")
	if fn == nil || raw == "" {
		t.Fatal("withEffortLevel or its doc comment is gone")
	}
	doc := proseOf711(raw)
	if strings.Contains(strings.ToLower(doc), "returns the env unchanged") {
		t.Errorf("T3: withEffortLevel's doc says a non-selecting path returns the env UNCHANGED; every such path "+
			"appends the empty AF_EFFORT_* triple:\n%s", doc)
	}
	named := false
	for _, sentence := range sentenceSplit711.Split(doc, -1) {
		if strings.Contains(sentence, "declared") && strings.Contains(sentence, "CLAUDE_CODE_EFFORT_LEVEL") &&
			strings.Contains(sentence, "untouched") {
			named = true
		}
		if strings.Contains(strings.ToLower(sentence), "unchanged") && !strings.Contains(sentence, "level") {
			t.Errorf("T3: withEffortLevel's doc says something other than the declared level is unchanged: %q", sentence)
		}
	}
	if !named {
		t.Errorf("T3: withEffortLevel's doc must say a non-selecting path leaves the declared "+
			"CLAUDE_CODE_EFFORT_LEVEL untouched:\n%s", doc)
	}
	if !strings.Contains(doc, "AF_EFFORT_") || !strings.Contains(doc, "empty") {
		t.Errorf("T3: withEffortLevel's doc must say the AF_EFFORT_* attestation is exported empty:\n%s", doc)
	}
}

func TestFableIncr711_T4_StepIDRationaleNamesTheRealReader(t *testing.T) {
	src := readRepoFile711(t, "internal/cmd/prime.go")
	start := strings.Index(src, "the effort treatment's record is written HERE")
	if start < 0 {
		t.Fatal("prime.go: cannot locate the effort record's rationale comment")
	}
	end := strings.Index(src[start:], "if sessionChanged && verbTelemetryFrom(ctx).enabled")
	if end < 0 {
		t.Fatal("prime.go: the effort record's rationale comment no longer precedes its write")
	}
	comment := proseOf711(src[start : start+end])
	for _, stale := range []string{"[step X] display that reads it", "`af turn evidence` [step X]"} {
		if strings.Contains(comment, stale) {
			t.Errorf("T4: prime.go justifies the effort record's StepID by %q; `af turn evidence` never "+
				"reads it", stale)
		}
	}
	for _, want := range []string{"readTokenomicsInterventionTail", "`af tokenomics status`"} {
		if !strings.Contains(comment, want) {
			t.Errorf("T4: the StepID rationale must name the reader that prints [step X] (missing %q):\n%s",
				want, comment)
		}
	}
}

func TestFableIncr711_T11_IsRunningIsDeprecatedInFavourOfLive(t *testing.T) {
	fn, raw := funcDoc711(t, "internal/session/session.go", "Manager", "IsRunning")
	if fn == nil {
		t.Fatal("(*Manager).IsRunning is gone; the spec said leave it and tests still call it")
	}
	var deprecation string
	for _, para := range strings.Split(raw, "\n\n") {
		if strings.HasPrefix(para, "Deprecated: ") {
			deprecation = proseOf711(para)
		}
	}
	if deprecation == "" {
		t.Errorf("T11: IsRunning's doc has no paragraph starting \"Deprecated: \" (the form go doc, gopls and "+
			"staticcheck recognise):\n%s", raw)
	} else if !strings.Contains(deprecation, "Live") {
		t.Errorf("T11: the deprecation paragraph must point to Live:\n%s", deprecation)
	}
	doc := proseOf711(raw)
	if strings.Contains(doc, "checks if the agent session is active") {
		t.Errorf("T11: IsRunning's summary still reads as liveness; it reports only that the tmux session "+
			"exists:\n%s", doc)
	}
	if !strings.Contains(doc, "zombie") {
		t.Errorf("T11: IsRunning's doc must say a zombie session reads as running:\n%s", doc)
	}
	for _, banned := range []string{"no callers", "unused"} {
		if strings.Contains(strings.ToLower(doc), banned) {
			t.Errorf("T11: IsRunning's doc claims %q, but tests call it:\n%s", banned, doc)
		}
	}
}

func TestFableIncr711_BODY1_HookCommentDropsSameEmptyStringClaim(t *testing.T) {
	for _, rel := range []string{"hooks/fidelity-gate.sh", "internal/cmd/install_hooks/fidelity-gate.sh"} {
		if strings.Contains(proseOf711(readRepoFile711(t, rel)), "all produce the same empty string") {
			t.Errorf("BODY-1 %s: the comment says an unknown boundary, an unreadable log and a no-intervention "+
				"turn all produce the same empty string; a reduced session gets its standing line on each", rel)
		}
	}
}
