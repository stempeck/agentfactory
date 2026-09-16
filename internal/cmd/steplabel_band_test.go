package cmd

import (
	"os"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// TestBandJudgesAcrossInstancesByLabel is B1-c (thread T3): the K10 baseline band must judge a fresh
// run against the history of the same formula-step, which is keyed on the stable StepLabel. A run
// carries a NEW per-instance bead-id StepID every time, so a band that observes on the bead id misses
// the label-keyed baseline and reports no_baseline on a step the factory has a median for.
func TestBandJudgesAcrossInstancesByLabel(t *testing.T) {
	const (
		formula   = "offpath"
		stepLabel = "phase-2"
		model     = "lmstudio"
	)

	root := setupTestFactoryForPrime(t)
	dir := config.TelemetryDir(root)

	// A learned baseline keyed on the STABLE label: median 100k over 4 runs, band 80k..120k.
	d := tokenomics.NewDigest()
	d.Put(tokenomics.DigestKey{Formula: formula, StepID: stepLabel, Model: model},
		tokenomics.Aggregate{Runs: 4, MedianPeakCtxTokens: 100_000, UpdatedAt: "2026-08-30T00:00:00.000Z"})
	if err := os.MkdirAll(telemetry.LearnedDigestDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir digest dir: %v", err)
	}
	if err := tokenomics.SaveDigest(telemetry.LearnedDigestPath(dir, formula), d); err != nil {
		t.Fatalf("SaveDigest: %v", err)
	}

	// A fresh run of the same formula-step: a NEW bead-id StepID, the same StepLabel, a peak inside
	// the learned band.
	if err := telemetry.AppendEvent(dir, telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
		TS: "2026-08-31T09:15:00.000Z", Agent: "manager", Formula: formula,
		InstanceID: "af-inst-9", StepID: "af-inst-9-s2", StepLabel: stepLabel, StepSeq: 2,
		Model: model, Status: telemetry.StatusClosed, PeakCtxTokens: i64p(105_000),
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	dto, err := bandReportDTO(root, "manager", "")
	if err != nil {
		t.Fatalf("bandReportDTO: %v", err)
	}
	if len(dto.Rows) != 1 {
		t.Fatalf("len(rows) = %d, want 1", len(dto.Rows))
	}
	row := dto.Rows[0]
	if row.Verdict == tokenomics.BandNoBaseline {
		t.Fatalf("row verdict = %q — the run was observed under its per-instance bead id, which misses "+
			"the label-keyed baseline; a step the factory has a median for reads as never seen", row.Verdict)
	}
	if row.Verdict != tokenomics.BandWithin {
		t.Errorf("row verdict = %q, want %q for 105000 inside 80000..120000", row.Verdict, tokenomics.BandWithin)
	}
	if row.Runs != 4 {
		t.Errorf("row Runs = %d, want 4 — the verdict must rest on the label-keyed baseline's runs", row.Runs)
	}
}
