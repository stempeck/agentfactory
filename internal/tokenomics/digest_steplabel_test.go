package tokenomics

import "testing"

// TestCoverageJoinEligibleCountsOnlyTrustedKeys pins T6: `af tokenomics status` must count only the
// keys a learned read can actually join — those whose Runs has reached the trust floor — so a digest
// full of single-run keys no read will ever hit reads as structurally cold rather than as rising
// coverage. Coverage counts every aggregate regardless of Runs; CoverageJoinEligible must not.
func TestCoverageJoinEligibleCountsOnlyTrustedKeys(t *testing.T) {
	d := NewDigest()
	// Three distinct keys with mixed run counts: two below the trust floor, one above it.
	d.Put(DigestKey{Formula: "design-v7", StepID: "P1", Model: "fable-5"}, Aggregate{Runs: 1, MedianPeakCtxTokens: 40_000})
	d.Put(DigestKey{Formula: "design-v7", StepID: "P2", Model: "fable-5"}, Aggregate{Runs: 1, MedianPeakCtxTokens: 50_000})
	d.Put(DigestKey{Formula: "design-v7", StepID: "P3", Model: "fable-5"}, Aggregate{Runs: 3, MedianPeakCtxTokens: 60_000})

	// Only the Runs>=2 key is join-eligible, so a status read must report one, not three.
	if got := CoverageJoinEligible(d, 2); got != 1 {
		t.Errorf("CoverageJoinEligible(d, 2) = %d, want 1 — only the Runs>=2 key can be joined; single-run keys are structurally cold", got)
	}
	// Coverage stays the raw aggregate count: the two verbs answer different questions and must not
	// collapse onto one, or a cold digest full of single-run keys would read as rising coverage.
	if got := Coverage(d); got != 3 {
		t.Errorf("Coverage(d) = %d, want 3 — every aggregate is counted regardless of Runs", got)
	}
}
