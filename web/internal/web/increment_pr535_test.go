package web

// Pinning tests for PR #535's unresolved review threads (fable-increment). Each actionable
// thread gets one source-scan assertion that is RED at the PR head and GREEN after the fix;
// the protective test guards the DO-NOT-CHANGE demoSel/contract-version invariants so a fix
// cannot silently forge the pending operator sign-off. Same os.ReadFile source-scan idiom as
// nav_test.go / errorsurface_test.go / contracttrace_formula_test.go (no JS runtime).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func mustNotContain(t *testing.T, src, needle, label string) {
	t.Helper()
	if strings.Contains(src, needle) {
		t.Errorf("%s: source still contains the stale text %q", label, needle)
	}
}

// THREAD-1 — the recast hint must stop calling live-store data a "demo copy" (seam sub-013).
func TestIncrementPR535_T1_WizardHintNotDemo(t *testing.T) {
	wizard := esRead(t, transplantRoot, "screens", "wizard.html")
	mustNotContain(t, wizard, "Demo copies plus anything", "THREAD-1 wizard.html:72")
	mustContain(t, wizard, "Store copies plus anything in a connected store folder.", "THREAD-1 wizard.html:72")
}

// THREAD-1 — the locked-card note must stop framing a live-store row as "not embedded in the demo" (seam sub-014).
func TestIncrementPR535_T1_RosterLockedNoteNotDemo(t *testing.T) {
	roster := esRead(t, transplantRoot, "screens", "roster.html")
	mustNotContain(t, roster, "not embedded in the demo", "THREAD-1 roster.html:218")
	mustContain(t, roster, "On the real floor but not editable through the console. Connect the store folder to open it.", "THREAD-1 roster.html:218")
}

// THREAD-1 — the contracttrace comment must reflect Decision 6 truthfully (a v2 rename pending an
// operator sign-off), not invert it as "v1 is signed / never amended".
func TestIncrementPR535_T1_ContracttraceCommentReflectsDecision6(t *testing.T) {
	src := readAsset(t, "contracttrace_formula_test.go")
	mustNotContain(t, src, "v1 is signed", "THREAD-1 contracttrace comment:24-26")
	for _, need := range []string{"v2 amendment", "operator sign-off"} {
		if !strings.Contains(src, need) {
			t.Errorf("THREAD-1 contracttrace comment: must reflect Decision 6 truthfully (missing %q — a v2 rename pending an operator sign-off)", need)
		}
	}
}

// THREAD-2 — the generate success copy must not instruct "Run af up" (job.go already ran it before exit_code:0).
func TestIncrementPR535_T2_SuccessCopyNoAfUp(t *testing.T) {
	demo := esRead(t, transplantRoot, "scripts", "demo-formulas.js")
	mustNotContain(t, demo, "Run af up to restart the agents", "THREAD-2 demo-formulas.js:128")
	mustContain(t, demo, "FLOOR READY — factory regenerated and the agents are back up.", "THREAD-2 demo-formulas.js:128")
}

// THREAD-3 — the app.js comment must stop asserting a deep-link sessionStorage handoff the shell
// does not expose; it must describe the branches' real (unreachable, route-contract-only) state.
func TestIncrementPR535_T3_AppJsCommentIsTruthful(t *testing.T) {
	appJS := readAsset(t, filepath.Join(staticDir, "app.js"))
	mustNotContain(t, appJS, "use the prototype's documented sessionStorage handoff", "THREAD-3 app.js:1095-1096")
	if !strings.Contains(appJS, "satisfy the route-contract trace") {
		t.Errorf("THREAD-3 app.js comment: must explain the formulas/new + formulas/{name} branches exist to satisfy the route-contract trace and are currently unreachable")
	}
}

// PROTECTIVE (DO-NOT-CHANGE) — the demoSel v1 label and both contracts stay version:1. A fix that
// renamed demoSel would forge the pending Decision-6 operator sign-off. Passes now; must keep passing.
func TestIncrementPR535_Protective_DemoSelContractStaysV1(t *testing.T) {
	const demoSelName = "Load an embedded demo formula"
	editor := esRead(t, transplantRoot, "screens", "editor.html")
	if !strings.Contains(editor, demoSelName) {
		t.Errorf("PROTECTIVE: editor.html must still carry the v1 demoSel label %q (rename needs operator sign-off)", demoSelName)
	}
	for _, rel := range []string{
		filepath.Join(staticDir, transplantRoot, "design-contract.yaml"),
		filepath.FromSlash("../../../.designs/web-ui/final/design-contract.yaml"),
	} {
		raw, err := os.ReadFile(rel)
		if err != nil {
			if os.IsNotExist(err) && strings.Contains(rel, ".designs") {
				continue // OSS/extracted checkout ships web/ without .designs/
			}
			t.Fatalf("PROTECTIVE: read %s: %v", rel, err)
		}
		body := string(raw)
		if !strings.Contains(body, "version: 1") {
			t.Errorf("PROTECTIVE: %s must stay version: 1 until an operator signs off the Decision-6 v2 amendment", rel)
		}
		if !strings.Contains(body, demoSelName) {
			t.Errorf("PROTECTIVE: %s must still pin the v1 demoSel name %q", rel, demoSelName)
		}
	}
}
