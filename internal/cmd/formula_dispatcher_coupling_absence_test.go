package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// formula_dispatcher_coupling_absence_test.go — PR #473 review threads T1–T7.
//
// A formula runs in an arbitrary checkout and cannot open the af source tree, so a
// formula comment that tells the agent to keep its inlined GraphQL query
// "BYTE-FOR-BYTE" in lockstep with `dispatch.go`'s `linkedPRsGraphQL` is an
// unfollowable cross-file maintenance contract — the reviewer's "internal systems
// treasure hunt." The inlined query itself is fine and stays; only the
// dispatcher-internals coupling commentary is the defect.
//
// This lint is the mechanical interlock that keeps it gone: it fails CI if any
// source formula re-introduces a reference that couples the self-contained formula
// to dispatcher internals. It bans precise high-signal phrases, never the bare word
// "dispatcher" — legitimate uses ("Mail dispatcher NOTHING_TO_DO", "af dispatch
// status", "direct-dispatch") must stay clean.

// dispatcherCouplingPatterns is the dispatcher-internals-coupling class. Each phrase
// appears today ONLY in the coupling commentary of the three pr_uri formulas and is
// removed by the T1–T7 fix.
var dispatcherCouplingPatterns = []*regexp.Regexp{
	// a reference to the dispatcher Go source file by name
	regexp.MustCompile(`dispatch\.go`),
	// the "mirror it exactly" maintenance contract
	regexp.MustCompile(`BYTE-FOR-BYTE`),
	regexp.MustCompile(`in lockstep`),
	// the dispatcher's internal query symbol
	regexp.MustCompile(`linkedPRsGraphQL`),
	// the prose that justifies the inlined query by dispatcher parity
	regexp.MustCompile(`dispatcher resolve the SAME`),
}

// checkDispatcherCouplingRefs returns one human-readable violation per line of
// content that couples a formula to dispatcher internals. A nil result means clean.
// Pulled out as a free function so the self-negative test can prove the check bites
// without touching real formulas.
func checkDispatcherCouplingRefs(content string) []string {
	var violations []string
	for i, line := range strings.Split(content, "\n") {
		for _, re := range dispatcherCouplingPatterns {
			if m := re.FindString(line); m != "" {
				violations = append(violations,
					fmt.Sprintf("line %d: dispatcher-internals coupling %q in %q", i+1, m, strings.TrimSpace(line)))
				break // one violation per line is enough to fail
			}
		}
	}
	return violations
}

// TestFormulaDispatcherCouplingAbsence fails if any source formula references
// dispatcher internals. It is RED until the T1–T7 fix lands and GREEN after; its
// forward value is blocking a future formula that re-introduces the coupling.
func TestFormulaDispatcherCouplingAbsence(t *testing.T) {
	const sourceDir = "install_formulas"
	for _, name := range listFormulas(t, sourceDir) {
		data, err := os.ReadFile(filepath.Join(sourceDir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		for _, v := range checkDispatcherCouplingRefs(string(data)) {
			t.Errorf("%s: %s — a formula is self-contained; inline what the agent needs and describe it neutrally, do not reference dispatch.go internals (PR #473 T1-T7)", name, v)
		}
	}
}

// TestDispatcherCouplingLintSelfNegative proves the lint is NOT vacuous: every
// coupling fixture MUST be flagged, and every legitimate dispatch/dispatcher use
// MUST NOT be. If this regresses, a green TestFormulaDispatcherCouplingAbsence is
// worthless.
func TestDispatcherCouplingLintSelfNegative(t *testing.T) {
	mustFlag := []string{
		"  # Mirror dispatch.go's linkedPRsGraphQL (dispatch.go:571-579) BYTE-FOR-BYTE: same",
		"dispatcher runs so direct-dispatch and the dispatcher resolve the SAME PR (AC-4):",
		"  # the two query literals in lockstep; do NOT add a one-sided draft filter.",
		"see dispatch.go for the canonical query",
		"keep it BYTE-FOR-BYTE identical to the dispatcher's copy",
		"# NO isDraft filter — so direct-dispatch and the dispatcher resolve the SAME PR (AC-4). Keep",
	}
	for _, s := range mustFlag {
		if v := checkDispatcherCouplingRefs(s); len(v) == 0 {
			t.Errorf("self-negative bite failed: lint did NOT flag %q (the check is vacuous)", s)
		}
	}

	// Legitimate dispatch/dispatcher prose that MUST stay clean, or the lint
	// false-fails on today's tree once the coupling is removed.
	mustNotFlag := []string{
		"Mail dispatcher NOTHING_TO_DO, close remaining steps with that reason, complete formula",
		"af dispatch status --json",
		"resolve it to its single linked PR via the closing-keyword relationship",
		"direct-dispatch and the workflow must agree on the resolved PR",
		"COMP-CLOSING-KEYWORD: what the dispatcher's ghLinkedPRs reads",
		"Query the issue's closing-keyword reverse edge so the issue URL resolves to one PR",
		"uses the closedByPullRequestsReferences reverse edge, includeClosedPrs:true, nodes { number }",
	}
	for _, s := range mustNotFlag {
		if v := checkDispatcherCouplingRefs(s); len(v) > 0 {
			t.Errorf("false-positive: lint should not flag legitimate prose %q, got %v", s, v)
		}
	}
}
