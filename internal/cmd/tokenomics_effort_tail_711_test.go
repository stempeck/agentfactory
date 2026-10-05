package cmd

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// af prime keeps StepID on its effort/reduce_effort record because the tokenomics interventions tail
// is the one reader that prints it. A filter on effort records there (as the turn verb has) would
// leave that retention with no reader at all.
func TestTokenomicsTailShowsTheStepOfAnEffortReduction(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	t.Chdir(root)
	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
		TS: "2026-09-24T09:00:00Z", Agent: "manager", Formula: "offpath", InstanceID: "af-711-1",
		StepID: "s-X", Mechanism: string(tokenomics.MechanismEffort), Action: telemetry.ActionReduceEffort,
		Objective: telemetry.ObjectiveEfficiency, EffortLevel: "medium",
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}

	enableTokenomicsJSON(t)
	out, err := runTokenomicsArgs(t)
	if err != nil {
		t.Fatalf("tokenomics --json: %v", err)
	}
	var dto tokenomicsStatusJSON
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}

	events := dto.RecentInterventions.Events
	if len(events) != 1 || !strings.HasSuffix(events[0], "effort: reduce_effort [step s-X]") {
		t.Errorf("interventions tail = %q, want one line ending \"effort: reduce_effort [step s-X]\"", events)
	}
}
