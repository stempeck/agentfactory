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
// handed the session's launch attestation the way the verb reads it. With that attestation naming a
// reduced level and NO in-window record, the effort clause is synthesised at grade time.
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

	// The session's launch attestation: the reduced level is in effect for THIS turn.
	plantLaunchEffort(t, level, string(tokenomics.ObjectiveEfficiency), "", "")

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
	if err := runTurnInterventionsCore(&out, root, agent, boundary, readLaunchEffort()); err != nil {
		t.Fatalf("runTurnInterventionsCore: %v", err)
	}
	got := out.String()

	if !strings.Contains(got, string(tokenomics.MechanismEffort)) ||
		!strings.Contains(got, telemetry.ActionReduceEffort) {
		t.Fatalf("the reduced turn's grader was not told the effort was reduced; the only reduce_effort "+
			"record is stamped at the prior boundary and the per-turn window drops it, so the excuse is "+
			"invisible on every turn it excuses:\n%q", got)
	}
	if !strings.Contains(got, effectEffortReduced) {
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

// TestTurnInterventionsNamesLaunchedLevel: the grader is told the level the session's own launch
// line exported, taken from a real selecting launch. The decoy is the log: a reduce_effort record
// naming a different level is history, not the running session's state, and must not be what the
// clause names.
func TestTurnInterventionsNamesLaunchedLevel(t *testing.T) {
	const nextStep = "step-2"
	fx, _, _ := primedFixture(t, roomyOccupancyPct)
	declareWindow(t, fx.root, roomyWindowTokens)
	armEfficiency(t, fx.root, nil)
	hookFormulaName(t, fx.workDir, "offpath")
	model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
	seedEfficiency(t, fx.root, "offpath", nextStep, model, reducibleAggregate())

	env := withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "offpath")
	level := modelEnvValue(env, config.EnvEffortLevel)
	if level == "" || level == "low" {
		t.Fatalf("the launch exported level %q; the test needs a reduced level distinct from the decoy", level)
	}
	inheritLaunch(t, env)
	plantEffortRecord(t, fx.root, fx.agent, turnBeforeBoundary, "low")

	got := turnInterventionsFrom(t, fx.root, fx.workDir, fx.agent, turnBoundary)

	if !strings.Contains(got, "(effort_level="+level+")") {
		t.Errorf("the session was launched at %q and the grader was not told so:\n%q", level, got)
	}
	if strings.Contains(got, "effort_level=low") {
		t.Errorf("the clause names the log record's level, not the level the launch applied (%q):\n%q", level, got)
	}
	if n := strings.Count(got, string(tokenomics.MechanismEffort)+": "); n != 1 {
		t.Errorf("%d effort lines, want 1:\n%q", n, got)
	}
}

// TestTurnInterventionsIgnoresAttestationWithUnknownObjective: af prime records a reduction only for
// an attestation whose objective it recognises, and the grader clause makes the same claim, so it
// takes the same attestation.
func TestTurnInterventionsIgnoresAttestationWithUnknownObjective(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	agentDir := config.AgentDir(root, "manager")
	plantLaunchEffort(t, "medium", "bogus", "", "")
	plantEffortRecord(t, root, "manager", turnBeforeBoundary, "medium")

	if got := turnInterventionsFrom(t, root, agentDir, "manager", turnBoundary); got != "" {
		t.Errorf("an attestation with an objective recordObjective does not recognise produced a clause:\n%q", got)
	}
}

// TestTurnInterventionsReportsBreadcrumbWithoutHistoricalRecord: af prime writes the reduce_effort
// record only when telemetry is on, so the log is not evidence the session was reduced — the
// attestation its launch exported is.
func TestTurnInterventionsReportsBreadcrumbWithoutHistoricalRecord(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	agentDir := config.AgentDir(root, "manager")
	plantLaunchEffort(t, "medium", string(tokenomics.ObjectiveEfficiency), "", "")

	if got := turnInterventionsFrom(t, root, agentDir, "manager", turnBoundary); !strings.Contains(got, "(effort_level=medium)") {
		t.Errorf("the session's launch attests a reduction to medium and the grader was not told:\n%q", got)
	}
}

// TestTurnInterventionsReportsInWindowEffortOnce: an in-window reduce_effort record is the log's copy
// of what the launch line attests, so it is never rendered — with no attestation it says nothing, and
// with one the grader gets the single standing line at the LAUNCH's level, not the record's.
func TestTurnInterventionsReportsInWindowEffortOnce(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	agentDir := config.AgentDir(root, "manager")
	plantEffortRecord(t, root, "manager", turnInWindow, "low")

	plantLaunchEffort(t, "", "", "", "")
	if got := turnInterventionsFrom(t, root, agentDir, "manager", turnBoundary); got != "" {
		t.Errorf("an in-window reduce_effort record with no attestation was rendered:\n%q", got)
	}

	plantLaunchEffort(t, "medium", string(tokenomics.ObjectiveEfficiency), "step-1", "offpath")
	got := turnInterventionsFrom(t, root, agentDir, "manager", turnBoundary)
	if want := standingEffortLine("medium"); got != want {
		t.Errorf("got  %q\nwant %q — exactly one effort line, at the launch's level", got, want)
	}
}
