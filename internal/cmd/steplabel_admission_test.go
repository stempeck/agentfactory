package cmd

import (
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// TestLearnedAppetiteJoinsAcrossInstancesByLabel is B1-b, the mission's headline pin (thread T2): two
// prior instances of the same formula-step must accumulate into ONE learned appetite the next run
// can join. Each instance mints its own per-instance bead-id StepID, so a digest keyed on the bead
// id splits the two runs into two single-run rows, and a third run — carrying yet another bead id —
// joins neither. Keying the write on the STABLE StepLabel is what lets the appetite accumulate and
// be found by a later run of the same formula-step.
func TestLearnedAppetiteJoinsAcrossInstancesByLabel(t *testing.T) {
	const (
		formula   = "offpath"
		stepLabel = "phase-2"
		model     = "lmstudio"
	)

	root := setupTestFactoryForPrime(t)
	dir := config.TelemetryDir(root)

	// Two runs of the same formula-step: same Formula and StepLabel, different InstanceID and
	// different per-instance minted StepID — the shape two instantiations of one formula produce.
	for _, r := range []telemetry.StepEvent{
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-08-31T09:00:00.000Z", Agent: "manager", Formula: formula,
			InstanceID: "af-inst-1", StepID: "af-inst-1-s2", StepLabel: stepLabel, StepSeq: 2,
			Model: model, Status: telemetry.StatusClosed, CtxTokensStart: i64p(10_000), PeakCtxTokens: i64p(40_000),
		},
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-08-31T09:05:00.000Z", Agent: "manager", Formula: formula,
			InstanceID: "af-inst-2", StepID: "af-inst-2-s2", StepLabel: stepLabel, StepSeq: 2,
			Model: model, Status: telemetry.StatusClosed, CtxTokensStart: i64p(10_000), PeakCtxTokens: i64p(50_000),
		},
	} {
		if err := telemetry.AppendEvent(dir, r); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	// The cache the way af done writes it: re-derived from the record store for the named formula.
	if _, err := writeLearnedDigests(root, formula, "2026-08-31T09:05:00.000Z"); err != nil {
		t.Fatalf("writeLearnedDigests: %v", err)
	}

	// The next run asks admission what THIS formula-step has historically cost, by its stable label.
	// The marginal appetite is the one the additive decision reads; the two prior runs recorded a
	// start and a peak, so it is known and rests on both runs.
	learned := learnedFor(root, formula, stepLabel, model)
	if !learned.found {
		t.Fatalf("no learned aggregate for %s/%s at all — the digest lookup missed the key the two "+
			"runs were filed under", formula, stepLabel)
	}
	app := learned.marginal
	if !app.Known {
		t.Fatalf("learned appetite for %s/%s is unknown — the two runs were filed under their "+
			"per-instance bead ids, so nothing accumulated under the stable label and the next run "+
			"joins neither", formula, stepLabel)
	}
	if app.Runs != 2 {
		t.Errorf("appetite Runs = %d, want 2 — both prior runs of the same formula-step accumulate "+
			"under the StepLabel key", app.Runs)
	}
}
