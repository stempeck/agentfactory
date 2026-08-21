package cmd

import (
	"strings"
	"testing"
)

// TestFidelityHelpDescribesStatus is the pin `evidenceCmd.Long` has had since Phase 3
// (turn_test.go:383-395) and `fidelityCmd.Long` never did. Phase 8's acceptance criteria checked
// these substrings with one-shot greps that vanish when the phase ends; the repo's answer to a
// doc claim with no standing guard is issue #600, narrated at statusline_doc_vocabulary_test.go:14-29.
func TestFidelityHelpDescribesStatus(t *testing.T) {
	long := fidelityCmd.Long

	// The read side must be discoverable from --help: `status` appears in Use: but described
	// nowhere until Phase 8. Each substring names a surface printFidelityStatus actually renders.
	for _, want := range []string{
		"fidelity_log.jsonl",               // the per-agent run record the table is read from
		"per-agent",                        // that the table is per agent, not factory-wide
		"fidelity_escalated_step",          // the latch — NOT a column of the run record (fidelity.go:387-389)
		".agentfactory/.fidelity-gate.log", // the provenance tail
	} {
		if !strings.Contains(long, want) {
			t.Errorf("fidelityCmd.Long does not name %q:\n%s", want, long)
		}
	}

	// Phase 5 shipped these and Phase 8 must not have regressed them.
	for _, want := range []string{"--agent", "override"} {
		if !strings.Contains(long, want) {
			t.Errorf("fidelityCmd.Long no longer names %q (Phase 5 surface):\n%s", want, long)
		}
	}

	// The Gotcha-2 honesty constraint, pinned so it cannot be dropped by a later tightening pass:
	// nothing reads .agentfactory/fidelity-overrides/, so the help must keep saying so. Asserted as
	// a required disclosure rather than a banned claim — a ban on "stop the gate firing" would
	// match the very sentence that makes the disclosure.
	if !strings.Contains(long, "no hook reads") {
		t.Errorf("fidelityCmd.Long no longer discloses that no hook reads the override directory; "+
			"see .designs/562/follow-ups.md, \"Gap found during Phase 8 investigation\":\n%s", long)
	}

	// ADR-014 clause 4: no interactive prompting text in command help.
	for _, banned := range []string{"(y/N)", "[y/N]", "Proceed?", "Continue?"} {
		if strings.Contains(long, banned) {
			t.Errorf("fidelityCmd.Long contains interactive prompt text %q (ADR-014 clause 4)", banned)
		}
	}
}
