package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// capPoolTokens is the pool every cap fixture DECLARES, on both build tags: 262144 rather than the
// default 200000 because a pool that leaves less than defaultBackendChildFloorTokens free refuses on
// the floor predicate, which runs before the cap and reports a different reason. Assertions about a
// record's pool_tokens keep spelling the number — an expectation read from the same constant the
// fixture was written from proves nothing.
const capPoolTokens = 262144

// Untagged so the integration-tagged live probe and the default-suite dispatch tests share one
// filter instead of the four byte-identical copies the probe would otherwise have made.
func dispatchInterventionRecords(t *testing.T, root, agent string) []telemetry.StepEvent {
	t.Helper()
	recs, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	var out []telemetry.StepEvent
	for _, r := range recs {
		if r.Event == telemetry.EventIntervention && r.Mechanism == string(tokenomics.MechanismDispatch) {
			out = append(out, r)
		}
	}
	return out
}

func dispatchRefusals(t *testing.T, root, agent string) []telemetry.StepEvent {
	t.Helper()
	var out []telemetry.StepEvent
	for _, r := range dispatchInterventionRecords(t, root, agent) {
		if r.Action == telemetry.ActionRefuse {
			out = append(out, r)
		}
	}
	return out
}

// assertSequentialOnlyRefusal pins what a cap refusal is NOT: token arithmetic. The cap is a
// semaphore, so a summed occupancy on the record means the floor or headroom predicate produced this
// refusal and the deny under test is not the one the caller thinks it read.
func assertSequentialOnlyRefusal(t *testing.T, rec telemetry.StepEvent) {
	t.Helper()
	if rec.SummedTokens != nil {
		t.Errorf("the sequential-only refusal recorded summed_occupancy_tokens = %d; the cap is a "+
			"semaphore, not token arithmetic", *rec.SummedTokens)
	}
}

// assertRefusalBreadcrumb reads the file #673 item 1's PostToolUse observer relays. Shared across
// build tags because the live probe and the fixture replay assert the same terminus for the same
// reason, and this phase's own headline change was deleting duplicated helpers.
func assertRefusalBreadcrumb(t *testing.T, workDir string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(workDir, ".runtime", dispatchLastRefusalName))
	if err != nil {
		t.Fatalf("the refusal left no breadcrumb for the observer to relay: %v", err)
	}
	var breadcrumb struct {
		V      int    `json:"v"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal(raw, &breadcrumb); err != nil {
		t.Fatalf("breadcrumb does not decode: %v", err)
	}
	if breadcrumb.V != dispatchLastRefusalVersion || breadcrumb.Reason != reasonSequentialOnly {
		t.Errorf("breadcrumb = %+v, want v%d/%s", breadcrumb, dispatchLastRefusalVersion, reasonSequentialOnly)
	}
}
