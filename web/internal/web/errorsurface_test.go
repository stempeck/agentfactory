package web

import (
	"path/filepath"
	"strings"
	"testing"
)

// #534 — error-surface assertions over the TRANSPLANTED formula editor. The #502 Phase-4 inventory
// tested the regeneration's own invented surfaces (save banners, fault-card builders, generate chips);
// that regeneration is deleted. What ships now is the APPROVED prototype byte-for-byte plus the
// declared seams, so the surfaces under test are exactly: (a) the prototype's own approved error
// affordances, and (b) the error paths the seam manifest declares (todos are mirrored in
// testdata/fable-frontend-seams). The handler halves stay pinned in the server package
// (server_test.go, formula_roundtrip_test.go, oversized_body_test.go). These are Go SOURCE-LEVEL
// assertions (no JS runtime in this module); byte-fidelity itself is reconstruct_test.go's job.

func esRead(t *testing.T, parts ...string) string {
	t.Helper()
	return readAsset(t, filepath.Join(append([]string{staticDir}, parts...)...))
}

func mustContain(t *testing.T, src, needle, row string) {
	t.Helper()
	if !strings.Contains(src, needle) {
		t.Errorf("Error-Surface %s: expected the source to contain %q", row, needle)
	}
}

// Row 1 — Recast parse failure fails CLOSED (production note 2, seams sub-008/sub-009): the wizard
// surfaces the source-parse error as a slip and the lever refuses a null cast.
func TestErrorSurface_RecastFailsClosed(t *testing.T) {
	wizard := esRead(t, "formula-editor", "screens", "wizard.html")
	mustContain(t, wizard, "The recast source does not parse: ", "row1/wizard")
	mustContain(t, wizard, "if (castText === null) return;", "row1/wizard")
}

// Row 2 — Store save failure (CAS 409 conflict, af-validate 422, oversize 400, token 401 after the
// one prompted retry): every refusal funnels through the pour seam's error path (sub-001), which
// slips the server envelope's verbatim message. The status-specific handler halves are pinned in the
// server package.
func TestErrorSurface_PourFailureSlip(t *testing.T) {
	editor := esRead(t, "formula-editor", "screens", "editor.html")
	mustContain(t, editor, "Pour failed: ", "row2/editor")
	seam := esRead(t, "formula-editor", "scripts", "demo-formulas.js")
	mustContain(t, seam, "save failed (HTTP ", "row2/seam")
	mustContain(t, seam, "base_sha256", "row2/seam")
}

// Row 3 — Store unreachable at load (design gap G1): the data seam marks the roster's existing
// store tag and logs; no invented UI.
func TestErrorSurface_StoreUnreachableMark(t *testing.T) {
	seam := esRead(t, "formula-editor", "scripts", "demo-formulas.js")
	mustContain(t, seam, "store unreachable", "row3")
	mustContain(t, seam, "storeTag", "row3")
}

// Row 4 — Generate-All failures (refusal to start, transient poll error, non-zero exit): the
// generateAll seam writes explicit lines into the roster's existing console element (sub-006).
func TestErrorSurface_GenerateFailureLines(t *testing.T) {
	seam := esRead(t, "formula-editor", "scripts", "demo-formulas.js")
	mustContain(t, seam, "could not start: ", "row4")
	mustContain(t, seam, "progress unavailable: ", "row4")
	mustContain(t, seam, "Regeneration ended (exit ", "row4")
}

// Row 5 — Unparseable TOML: the APPROVED editor's dark-board state and the roster's does-not-parse
// card (both prototype bytes).
func TestErrorSurface_UnparseableApprovedStates(t *testing.T) {
	editor := esRead(t, "formula-editor", "screens", "editor.html")
	roster := esRead(t, "formula-editor", "screens", "roster.html")
	mustContain(t, editor, "The machines are dark", "row5/editor")
	mustContain(t, roster, "Does not parse: ", "row5/roster")
}

// Row 6 — Unsaved edits: the APPROVED editor carries its own beforeunload guard and dirty
// confirm dialog (prototype bytes); the shell's iframe mount keeps the frame alive across view
// switches, so the shell needs no duplicate guard.
func TestErrorSurface_ApprovedDirtyGuard(t *testing.T) {
	editor := esRead(t, "formula-editor", "screens", "editor.html")
	mustContain(t, editor, "beforeunload", "row6")
	mustContain(t, editor, "guardDirty", "row6")
	appJS := esRead(t, "app.js")
	if strings.Contains(appJS, "EditorViewModel.isDirty") {
		t.Error("Error-Surface row6: the shell must not re-implement the editor's dirty guard against the retired viewmodels")
	}
}

// Row 7 — FS write-permission denial (Chromium file mode, prototype bytes): explicit slip, and the
// download fallback stays available.
func TestErrorSurface_FSWriteDenial(t *testing.T) {
	editor := esRead(t, "formula-editor", "screens", "editor.html")
	mustContain(t, editor, "Write permission denied", "row7")
	mustContain(t, editor, "downloadBtn", "row7")
}

// Row 8 — Wizard lever refusal on a flawed cast (prototype bytes): the cast is validated before it
// rolls out and the failure is slipped.
func TestErrorSurface_WizardLeverRefusal(t *testing.T) {
	wizard := esRead(t, "formula-editor", "screens", "wizard.html")
	mustContain(t, wizard, "The cast came out flawed: ", "row8")
	mustContain(t, wizard, "The cast did not parse: ", "row8")
}
