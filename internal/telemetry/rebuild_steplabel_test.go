package telemetry

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// TestDigestAccumulatesAcrossInstancesByStepLabel pins the learned digest's join key onto the
// STABLE formula step id (StepLabel), not the per-instance bead id (StepID). Two runs of the same
// formula-step mint different bead ids, so keying the digest's StepID leg on the bead id splits
// their history into two single-run rows that no cross-run lookup can ever hit. Keying that leg on
// the stable StepLabel is what lets the two runs accumulate into one row.
func TestDigestAccumulatesAcrossInstancesByStepLabel(t *testing.T) {
	const stepLabel = "phase-2"

	root := t.TempDir()
	dir := config.TelemetryDir(root)
	roster := []string{"architect"}

	// Two runs of the same formula-step: same Formula and StepLabel, different InstanceID and
	// different (per-instance minted) StepID — the shape two instantiations of one formula produce.
	firstRun := oneRun("architect", "inst-x1", "sess-x1", 1, 40_000, 30_000)
	for i := range firstRun {
		firstRun[i].StepID, firstRun[i].StepLabel = "af-x-1", stepLabel
	}
	secondRun := oneRun("architect", "inst-y1", "sess-y1", 5, 50_000, 35_000)
	for i := range secondRun {
		secondRun[i].StepID, secondRun[i].StepLabel = "af-y-1", stepLabel
	}
	seedStore(t, dir, append(firstRun, secondRun...)...)

	digests, stats := RebuildLearnedDigests(dir, roster, rebuildFormula, rebuiltAt)
	if stats.Malformed != 0 {
		t.Fatalf("Malformed = %d on a clean store, want 0", stats.Malformed)
	}

	digest := digests[rebuildFormula]

	// The stable-label key both runs must accumulate under.
	labelKey := tokenomics.DigestKey{Formula: rebuildFormula, StepID: stepLabel, Model: rebuildModel}

	if n := tokenomics.Coverage(digest); n != 1 {
		t.Errorf("digest holds %d entries, want exactly 1 — two runs keyed on the per-instance StepID split into one single-run row each instead of accumulating under the stable StepLabel", n)
	}

	agg, ok := digest.Lookup(labelKey)
	if !ok {
		t.Fatalf("no entry for %+v — the aggregate keys its StepID leg on the per-instance bead id, so neither run lands on the stable-label key and every cross-run lookup misses", labelKey)
	}
	if agg.Runs != 2 {
		t.Errorf("Runs = %d, want 2 — both runs of the same formula-step accumulate under the StepLabel key", agg.Runs)
	}
}
