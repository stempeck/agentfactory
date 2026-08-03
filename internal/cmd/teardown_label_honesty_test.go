package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The false framings for the `af sling --agent X --reset` teardown path. These needles live ONLY
// in this (unscanned) test file; the assertion below scans the K13 scanner ARTIFACT for them, so a
// needle here can never self-match.
//
// #548 Phase 2 INVERTED this test's premise. Before #548 the sling `--reset` path was UNGATED (no
// callerDispatched check), so the honest label was "loopable residual, NOT provenance-bound" and
// the LIES were any ownership/provenance certifications. Phase 2 (P5) gates the reset stop through
// scopedStopAllowed — the same self/dispatcher/manager tiers as `af down`, with not-running and
// dispatch-daemon carve-outs — so the polarity flips: the honest label now certifies the GATE, and
// the now-false framings are the old "ungated / NOT-provenance-bound / loopable per-specialist"
// residual descriptions.
//
// Scope note (#548): the pre-#548 baseline lived in `.designs/541/design-doc.md` (the #541 doc).
// The #548 outline does not authorize editing #541-doc content, and that doc's "loopable residual"
// wording now contradicts the gated reality. So this test is re-pointed at the #548-owned
// classification artifact — the K13 scanner allowlist (teardown_scanner_enforce_test.go) — whose
// sling `--reset` entry Phase 2 re-classified to `gated`. The honesty pin now rides the artifact
// this PR actually owns and keeps honest.
const (
	slingHonestBaseline = "gated by scopedStopAllowed" // the sling --reset K13 entry must certify the #548 gate
)

// slingUngatedLies are the pre-#548 framings that are now FALSE (the reset stop IS gated). None may
// survive in the K13 scanner artifact.
var slingUngatedLies = []string{
	"own-worker",
	"NOT provenance-bound",
	"no callerDispatched",
	"loopable per-specialist",
}

// TestSlingResetResidualHonestlyLabeled pins that the teardown-authority classification artifact
// describes the `af sling --agent X --reset` stop HONESTLY. Post-#548 that means: the K13 scanner
// allowlist must certify the reset stop as gated by scopedStopAllowed and must NOT carry any of the
// pre-#548 ungated framings.
func TestSlingResetResidualHonestlyLabeled(t *testing.T) {
	root := findModuleRoot(t)
	scanner := func() string {
		b, err := os.ReadFile(filepath.Join(root, "internal/cmd/teardown_scanner_enforce_test.go"))
		if err != nil {
			t.Fatalf("read K13 scanner file: %v", err)
		}
		return string(b)
	}()

	for _, lie := range slingUngatedLies {
		if strings.Contains(scanner, lie) {
			t.Errorf("K13 scanner still carries the pre-#548 ungated framing %q for sling --reset; "+
				"the reset stop is now gated by scopedStopAllowed (#548 P5) — reconcile the classification", lie)
		}
	}

	// Protective (DO-NOT-CHANGE): the honest gated label for the sling --reset stop must survive.
	if !strings.Contains(scanner, slingHonestBaseline) {
		t.Errorf("K13 scanner lost the honest gated sling --reset label %q — it must be preserved (#548 P5)", slingHonestBaseline)
	}
}
