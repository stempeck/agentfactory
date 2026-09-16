//go:build !integration

package cmd

import (
	"bytes"
	"fmt"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// TestEffortExcuseVisibleInRelaunchedTurnWindow is S1 (threads T4/T5): the effort-reduction excuse
// must reach the fidelity grader on the turns it is actually excusing. The reduce_effort record is
// written ONCE at the boundary that relaunches the session; the reduced level then persists for
// every turn of that session, but the per-turn window drops any record stamped before the boundary,
// so the grader of a reduced turn is never told the turn was reduced.
//
// The pin is on runTurnInterventionsCore — the exact call the gate makes (hooks/fidelity-gate.sh) —
// so it holds for either fix approach. Arrange follows the chosen fix: with the
// session-scoped current-effort surface set (env CLAUDE_CODE_EFFORT_LEVEL
// reduced) and NO in-window record, the effort clause is synthesised at grade time.
//
// The companion assertion is the effort-scoping guard: a dispatch advisory planted before the SAME
// boundary must STAY filtered. The fix un-filters the persistent effort reduction only, never every
// pre-boundary record — that would hand the grader firings from an earlier turn.
func TestEffortExcuseVisibleInRelaunchedTurnWindow(t *testing.T) {
	const (
		boundary       = "2026-08-09T10:00:00Z"
		beforeBoundary = "2026-08-09T09:59:59.000Z"
		agent          = "agent-a"
		level          = "low"
	)
	root := t.TempDir()

	// The session-scoped current-effort surface: the reduced level is in effect for THIS turn.
	t.Setenv(config.EnvEffortLevel, level)

	// The reduce_effort record is stamped at the prior boundary — the relaunch it rode — and so falls
	// before the window this turn is graded over. A dispatch advisory shares that pre-boundary instant
	// to prove the fix stays effort-scoped rather than un-filtering the whole earlier turn.
	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
		TS: beforeBoundary, Agent: agent, Verb: "done", Formula: "hook-e2e", StepID: "bd-k15-step-1",
		Mechanism: string(tokenomics.MechanismEffort), Action: telemetry.ActionReduceEffort, EffortLevel: level,
	}); err != nil {
		t.Fatalf("AppendEvent effort: %v", err)
	}
	plantIntervention(t, root, agent, beforeBoundary, tokenomics.MechanismDispatch, telemetry.ActionAdvise)

	var out bytes.Buffer
	if err := runTurnInterventionsCore(&out, root, agent, boundary); err != nil {
		t.Fatalf("runTurnInterventionsCore: %v", err)
	}
	got := out.String()

	if !strings.Contains(got, string(tokenomics.MechanismEffort)) ||
		!strings.Contains(got, telemetry.ActionReduceEffort) {
		t.Fatalf("the reduced turn's grader was not told the effort was reduced; the only reduce_effort "+
			"record is stamped at the prior boundary and the per-turn window drops it, so the excuse is "+
			"invisible on every turn it excuses:\n%q", got)
	}
	if effect := interventionEffects[string(tokenomics.MechanismEffort)]; !strings.Contains(got, effect) {
		t.Errorf("the effort clause omits the closed-vocabulary effect the grader needs in order to "+
			"excuse it:\n%q", got)
	}
	if want := fmt.Sprintf("(effort_level=%s)", level); !strings.Contains(got, want) {
		t.Errorf("the effort clause does not name the level in effect %q:\n%q", want, got)
	}
	// The scoping guard: the persistent effort reduction surfaces, the earlier turn's dispatch does not.
	if strings.Contains(got, string(tokenomics.MechanismDispatch)) {
		t.Errorf("a dispatch advisory from before the boundary reached this turn's grader; the fix must "+
			"un-filter the persistent effort reduction only, not every pre-boundary record:\n%q", got)
	}
}
