package cmd

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
)

// TestInstallHelpDescribesConfigSourceOrder pins `installCmd.Long` naming the starter-config
// source order (source clone via AF_SOURCE_ROOT, else the source tree af was built from) and the
// no-overwrite guarantee. Phase 5 (CFG-8, issue #654) added this prose; without this test the only "proof"
// is a one-shot grep against the whole file that already matches pre-existing comments and
// error strings (baseline 6, unrelated to Long) and would vanish once the phase's IMPLREADME
// is closed out — the same gap fidelity_help_test.go exists to close for `fidelityCmd.Long`.
func TestInstallHelpDescribesConfigSourceOrder(t *testing.T) {
	long := installCmd.Long

	if !strings.Contains(long, "AF_SOURCE_ROOT") {
		t.Errorf("installCmd.Long does not name %q:\n%s", "AF_SOURCE_ROOT", long)
	}
	if !strings.Contains(long, "never overwrit") { // covers "overwrite"/"overwritten"
		t.Errorf("installCmd.Long does not state the no-overwrite guarantee:\n%s", long)
	}

	// The help names the whole source chain, and says the clone channel reads the
	// working tree — "committed" is false, since uncommitted edits are seeded too. Whitespace is
	// collapsed because the help text is hard-wrapped.
	flat := strings.Join(strings.Fields(long), " ")
	var problems []string
	for _, want := range []string{"source tree this af was built from", "working tree"} {
		if !strings.Contains(flat, want) {
			problems = append(problems, fmt.Sprintf("does not say %q", want))
		}
	}
	if installHelpCommittedWord.MatchString(long) {
		problems = append(problems, `calls the seeded clone configs "committed" (the seed reads the working tree)`)
	}
	if len(problems) > 0 {
		t.Errorf("installCmd.Long %s:\n%s", strings.Join(problems, "; "), long)
	}
}

var installHelpCommittedWord = regexp.MustCompile(`(?i)\bcommitted\b`)

// TestInstallHelpNoInteractivePromptText is ADR-014 clause 4 (no interactive prompting text in
// command help) applied to `installCmd.Long`, mirroring fidelity_help_test.go's banned-list
// check. This must hold before and after Phase 5's addition — a protective assertion, not a
// RED-to-GREEN pin.
func TestInstallHelpNoInteractivePromptText(t *testing.T) {
	long := installCmd.Long
	for _, banned := range []string{"(y/N)", "[y/N]", "Proceed?", "Continue?"} {
		if strings.Contains(long, banned) {
			t.Errorf("installCmd.Long contains interactive prompt text %q (ADR-014 clause 4)", banned)
		}
	}
}
