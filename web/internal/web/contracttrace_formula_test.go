package web

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Issue #502 AC-8, re-grounded by #534. The Formula Editor's design contract
// (.designs/web-ui/final/design-contract.yaml, issue #445) promises screens and the accessible
// role/name of every required element. The #502 trace verified those promises against the
// regeneration's viewmodel literals; that regeneration destroyed the approved visual and is deleted.
// What ships now is the APPROVED prototype transplanted verbatim (static/formula-editor/, byte-pinned
// by reconstruct_test.go), so the trace re-anchors to what the shipped tree can prove source-level
// without a JS runtime:
//  1. every contract screen's `prototype:` file ships in the embedded tree;
//  2. every REQUIRED element's accessible name is present in that screen's source — either verbatim
//     (aria-label / attribute names) or in tag-stripped text (label text composed across inline
//     elements); composite radio-group names ("A / B / C") are checked part-by-part;
//  3. the shell routes the contract's three routes into the transplanted documents.
//
// The code is verified against the contract, never the reverse. Decision 6 chose a v2 amendment
// renaming demoSel to "Load a store formula", but that rename requires an operator sign-off not yet
// obtained — so v1 remains the current signed baseline this trace validates, and editing the yaml to
// fit the code would forge that pending sign-off (out of bounds until the signed v2 lands). The
// #502-era viewmodel-member trace retired WITH the viewmodels; the contract's `viewmodel:` lines
// remain design intent, now implemented by the approved screens' closures.
var formulaContractPath = filepath.Join("..", "..", "..", ".designs", "web-ui", "final", "design-contract.yaml")

// transplantRoot is where the approved tree ships inside the embedded static FS.
const transplantRoot = "formula-editor"

type contractElement struct {
	id       string
	name     string
	required bool
}

type contractScreen struct {
	id        string
	prototype string
	elements  []contractElement
}

var (
	reScreenID  = regexp.MustCompile(`^  - id: (\S+)$`)
	reProto     = regexp.MustCompile(`^    prototype: (\S+)$`)
	reElemID    = regexp.MustCompile(`^      - id: (.+)$`)
	reElemName  = regexp.MustCompile(`accessible: \{ role: [a-z]+, name: "([^"]+)" \}`)
	reElemReq   = regexp.MustCompile(`^        required: (true|false)$`)
	reTagStrip  = regexp.MustCompile(`<[^>]*>`)
	reWhiteRuns = regexp.MustCompile(`\s+`)
)

// parseFormulaContract is a purpose-built line scanner for the contract's screens region (stops at
// viewmodel_contracts). It is deliberately strict about the file's actual indentation so a future
// contract reshape fails loudly here rather than silently tracing nothing.
func parseFormulaContract(t *testing.T, yaml string) []contractScreen {
	t.Helper()
	var screens []contractScreen
	var cur *contractScreen
	var curElem *contractElement
	inScreens := false
	for _, line := range strings.Split(yaml, "\n") {
		if strings.HasPrefix(line, "screens:") {
			inScreens = true
			continue
		}
		if strings.HasPrefix(line, "viewmodel_contracts:") {
			break
		}
		if !inScreens {
			continue
		}
		if m := reScreenID.FindStringSubmatch(line); m != nil {
			screens = append(screens, contractScreen{id: m[1]})
			cur = &screens[len(screens)-1]
			curElem = nil
			continue
		}
		if cur == nil {
			continue
		}
		if m := reProto.FindStringSubmatch(line); m != nil {
			cur.prototype = m[1]
			continue
		}
		if m := reElemID.FindStringSubmatch(line); m != nil {
			cur.elements = append(cur.elements, contractElement{id: strings.Trim(m[1], `"`)})
			curElem = &cur.elements[len(cur.elements)-1]
			continue
		}
		if curElem == nil {
			continue
		}
		if m := reElemName.FindStringSubmatch(line); m != nil {
			curElem.name = m[1]
			continue
		}
		if m := reElemReq.FindStringSubmatch(line); m != nil {
			curElem.required = m[1] == "true"
		}
	}
	if len(screens) != 3 {
		t.Fatalf("contract screens region parsed to %d screens, want 3 — the scanner or the contract shape changed", len(screens))
	}
	return screens
}

func readFormulaContract(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(formulaContractPath)
	if err != nil {
		if os.IsNotExist(err) {
			// The public/extracted repo ships web/ but not .designs/ (todos/public_repo_files.md), so
			// absence is a legitimate checkout shape there. Monorepo CI runs on a full checkout with
			// no paths filters, so this skip never fires where the interlock matters.
			t.Skipf("editor design contract absent at %s — OSS/extracted checkout; skipping contract trace", formulaContractPath)
		}
		t.Fatalf("read %s: %v", formulaContractPath, err)
	}
	return string(raw)
}

// nameFindable reports whether an accessible name from the contract can be located in the screen's
// source: verbatim (attribute/aria-label form), or in tag-stripped whitespace-normalized text (label
// text composed across inline elements).
func nameFindable(src, name string) bool {
	if strings.Contains(src, name) {
		return true
	}
	stripped := reWhiteRuns.ReplaceAllString(reTagStrip.ReplaceAllString(src, " "), " ")
	return strings.Contains(stripped, name)
}

// TestFormulaContractTrace_RequiredNamesShipVerbatim: every contract screen's prototype file ships in
// the embedded tree, and every required element's accessible name is present in that screen's source.
// Green means the contract as signed tells the truth about the SHIPPED editor.
func TestFormulaContractTrace_RequiredNamesShipVerbatim(t *testing.T) {
	screens := parseFormulaContract(t, readFormulaContract(t))
	for _, s := range screens {
		src := readAsset(t, filepath.Join(staticDir, transplantRoot, filepath.FromSlash(s.prototype)))
		for _, e := range s.elements {
			if !e.required || e.name == "" {
				continue
			}
			// Composite radio-group names list each radio's own name separated by " / ".
			parts := []string{e.name}
			if strings.Contains(e.name, " / ") {
				parts = strings.Split(e.name, " / ")
			}
			for _, part := range parts {
				if !nameFindable(src, part) {
					t.Errorf("screen %s (%s): required element %q accessible name %q not found in the shipped source",
						s.id, s.prototype, e.id, part)
				}
			}
		}
	}
}

// TestFormulaContractTrace_RequiredNames_SelfNegative proves the checker is not vacuous: a name the
// screen does not contain must be reported missing, and the tag-stripped path must match a name
// composed across inline markup.
func TestFormulaContractTrace_RequiredNames_SelfNegative(t *testing.T) {
	src := `<label class="lbl" for="x">What is this line for? <span class="opt">optional</span></label>`
	if !nameFindable(src, "What is this line for? optional") {
		t.Error("nameFindable must match a name composed across inline elements via tag-stripping")
	}
	if nameFindable(src, "Detonate the reactor") {
		t.Error("nameFindable false-positive on a name the source does not contain")
	}
}

// TestFormulaContractTrace_RoutesMountTransplant: the shell wires the contract's three routes
// (/formulas, /formulas/new, /formulas/:name) into the transplanted documents through the persistent
// iframe, and index.html carries exactly one formulas mount hosting that frame.
func TestFormulaContractTrace_RoutesMountTransplant(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	for _, needle := range []string{
		"/formula-editor/screens/roster.html",
		"/formula-editor/screens/wizard.html",
		"/formula-editor/screens/editor.html",
	} {
		if !strings.Contains(appJS, needle) {
			t.Errorf("app.js must route the formulas family into the transplanted document %q", needle)
		}
	}
	indexHTML := readAsset(t, filepath.Join(staticDir, "index.html"))
	if n := strings.Count(indexHTML, `id="view-formulas"`); n != 1 {
		t.Errorf("index.html must carry exactly one formulas mount section, found %d", n)
	}
	if !strings.Contains(indexHTML, `id="formulaFrame"`) {
		t.Error("index.html must host the persistent #formulaFrame iframe inside the formulas mount")
	}
}

// TestFormulaContractTrace_PathIsNotAGlob pins the hardcoded contract path. A .designs/**/final/ glob
// would sweep in the operator-console contract and trace it against the editor screens, so the two
// traces must each name their own file. The path is also the sign-off's identity: contract v1 is what
// Decision 6 approved, and this trace exists to be told the truth by the code, never the reverse.
func TestFormulaContractTrace_PathIsNotAGlob(t *testing.T) {
	if got := filepath.ToSlash(formulaContractPath); !strings.HasSuffix(got, ".designs/web-ui/final/design-contract.yaml") {
		t.Errorf("formulaContractPath = %q — it must name the #445 editor contract exactly", got)
	}
	if formulaContractPath == contractPath {
		t.Error("the two traces must read two different contracts")
	}
	for _, p := range []string{formulaContractPath, contractPath} {
		if strings.ContainsAny(p, "*?[") {
			t.Errorf("contract path %q contains a glob metacharacter — a glob returns both contracts and fails spuriously", p)
		}
	}
}
