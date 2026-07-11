package web

import (
	"io/fs"
	"testing"
)

// The Floor view assets — plus the #534 transplanted formula-editor tree — must be embedded in the
// binary (CWD-independent serving). The embed want-list is the GUARANTEE that new assets ship: the
// //go:embed static glob picks up new files automatically, but this test only *fails* on a listed asset
// that is absent, so every asset the served view depends on must be enumerated here. WOFF2 fonts are
// deliberately NOT listed — Phase 4 keeps the identical-stacks font fallback (Decision 7 is gated on the
// Phase-5a operator sign-off), so no self-hosted font ships this phase.
func TestStatic_BundlesFloorView(t *testing.T) {
	want := []string{
		"index.html", "app.js", "styles/variables.css", "styles/main.css", "assets/logo.svg",
		// #534 — the APPROVED prototype transplanted verbatim (byte-fidelity enforced by
		// reconstruct_test.go); every file of the approved tree ships, plus the one
		// declared replaced-file seam (the live-store data module).
		"formula-editor/index.html",
		"formula-editor/design-contract.yaml",
		"formula-editor/assets/mark.svg",
		"formula-editor/components/controls.html",
		"formula-editor/components/inspection.html",
		"formula-editor/components/station.html",
		"formula-editor/screens/editor.html",
		"formula-editor/screens/roster.html",
		"formula-editor/screens/wizard.html",
		"formula-editor/scripts/demo-formulas.js",
		"formula-editor/scripts/shared.js",
		"formula-editor/scripts/test-engine.js",
		"formula-editor/scripts/toml-engine.js",
		"formula-editor/styles/main.css",
		"formula-editor/styles/variables.css",
	}
	sfs := Static()
	for _, p := range want {
		if _, err := fs.Stat(sfs, p); err != nil {
			t.Errorf("embedded static FS missing %q: %v", p, err)
		}
	}
}
