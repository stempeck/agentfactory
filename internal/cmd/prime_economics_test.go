package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// runPrimeCapturing drives af prime through its RunE entry point — where the per-invocation clock
// and the single telemetry gate read are established — and returns what the agent would have seen.
// runPrimeInFixture discards its buffer, and every assertion here is about the output.
func runPrimeCapturing(t *testing.T) string {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	if err := runPrime(cmd, nil); err != nil {
		t.Fatalf("af prime: %v", err)
	}
	return out.String()
}

// primedFixture is one agent hooked to a two-step formula with a live occupancy snapshot: the
// ordinary state af prime runs in.
func primedFixture(t *testing.T, occupancyPct float64) (lifecycleFixture, issuestore.Issue, issuestore.Issue) {
	t.Helper()
	fx := newLifecycleFixture(t)
	now := boundaryTestNow()
	epic, step := seedTwoStepBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", occupancyPct, 1000, now.Add(-10*time.Second), now)
	return fx, epic, step
}

// plantBrief writes the checkpoint a recycling session would have left behind, through the
// production interview so the test cannot assert against a brief shape nothing produces.
func plantBrief(t *testing.T, fx lifecycleFixture) *checkpoint.Checkpoint {
	t.Helper()
	dirtyTree(t, fx, 3)
	if err := captureCheckpointWithFormula(t.Context(), fx.workDir, "HANDOFF: step context boundary", nil); err != nil {
		t.Fatalf("captureCheckpointWithFormula: %v", err)
	}
	cp, err := checkpoint.Read(fx.workDir)
	if err != nil || cp == nil {
		t.Fatalf("checkpoint.Read: %v (cp=%v)", err, cp)
	}
	if !cp.HasResumeBrief() {
		t.Fatal("fixture: the interview left no brief, so slimming has nothing to arm on")
	}
	return cp
}

// The step contract, which is re-emitted unconditionally. design-doc.md:228 grades slimming it away
// a Medium risk — an agent that resumes without its instructions has nothing to resume.
var stepContractMarkers = []string{"### Current Step Instructions", "First", "### Work Loop", "af done"}

func assertStepContract(t *testing.T, out string) {
	t.Helper()
	for _, marker := range stepContractMarkers {
		if !strings.Contains(out, marker) {
			t.Errorf("prime output is missing the step contract marker %q", marker)
		}
	}
}

// TestPrimeSlimming covers #668 K16: a session resuming the SAME step it was already primed for,
// carrying a brief that says where the work is, does not need the checkpoint block to repeat what
// the brief and the formula header already said.
func TestPrimeSlimming(t *testing.T) {
	t.Run("a same-step resume drops the superseded sections and keeps the contract", func(t *testing.T) {
		fx, _, _ := primedFixture(t, 40)

		first := runPrimeCapturing(t)
		assertStepContract(t, first)

		cp := plantBrief(t, fx)
		slimmed := runPrimeCapturing(t)

		// The same checkpoint with the interview removed, so the brief is the ONLY difference
		// between the two primes compared below. Comparing against the first prime instead would
		// compare a session that had no checkpoint at all with one that has.
		noBrief := *cp
		noBrief.ResumeArtifacts, noBrief.ResumeVerified, noBrief.ResumeNextAction = nil, "", ""
		if err := checkpoint.Write(fx.workDir, &noBrief); err != nil {
			t.Fatal(err)
		}
		full := runPrimeCapturing(t)

		assertStepContract(t, slimmed)
		if !strings.Contains(slimmed, "**Next action:**") {
			t.Error("the resumed prime does not surface the brief it slimmed on")
		}
		for _, superseded := range []string{"**Modified files:**", "**Working on:**"} {
			if strings.Contains(slimmed, superseded) {
				t.Errorf("a same-step resume with a brief still re-emits %q", superseded)
			}
			if !strings.Contains(full, superseded) {
				t.Errorf("the unslimmed control is missing %q, so the assertion above proves nothing", superseded)
			}
		}
		if len(slimmed) >= len(full) {
			t.Errorf("the slimmed prime is not smaller: %d bytes vs %d", len(slimmed), len(full))
		}
	})

	t.Run("a first prime of a step re-emits everything", func(t *testing.T) {
		// The different-step control. A session starting a step it has never been primed for is
		// resuming nothing, and a brief about the previous step supersedes none of its output.
		fx, epic, _ := primedFixture(t, 40)
		dirtyTree(t, fx, 3)
		cp, err := checkpoint.Capture(fx.workDir)
		if err != nil {
			t.Fatal(err)
		}
		cp.WithFormula(epic.ID, "some-other-step", "Some other step").
			WithResumeBrief([]string{"a.go"}, "1 of 2 formula steps closed", "some-other-step",
				"continue step some-other-step")
		if err := checkpoint.Write(fx.workDir, cp); err != nil {
			t.Fatal(err)
		}

		out := runPrimeCapturing(t)

		assertStepContract(t, out)
		if !strings.Contains(out, "**Modified files:**") {
			t.Error("a first prime slimmed its checkpoint block; slimming is for a SAME-step resume only")
		}
	})

	t.Run("a same-step resume without a brief re-emits everything", func(t *testing.T) {
		// The fail-closed control: absence must never arm an action (step_context.go:28-29).
		fx, epic, step := primedFixture(t, 40)
		runPrimeCapturing(t)

		dirtyTree(t, fx, 3)
		cp, err := checkpoint.Capture(fx.workDir)
		if err != nil {
			t.Fatal(err)
		}
		// WithFormula naming the SAME step, and no brief. Capture alone leaves CurrentStep empty,
		// which makes the same-step conjunct false and this control never reach the conjunct it is
		// named for — it would stay green with `&& cp.HasResumeBrief()` deleted, testing nothing.
		cp.WithFormula(epic.ID, step.ID, step.Title).WithNotes("recycled")
		if err := checkpoint.Write(fx.workDir, cp); err != nil {
			t.Fatal(err)
		}
		if cp.HasResumeBrief() {
			t.Fatal("fixture: this control must carry NO brief")
		}

		out := runPrimeCapturing(t)

		assertStepContract(t, out)
		if !strings.Contains(out, "**Modified files:**") {
			t.Error("a resume with NO brief was slimmed; the brief is what supersedes those sections")
		}
	})
}

// TestPrimeCost covers K16's other half: what priming a session actually costs it, measured on the
// bytes that reached the agent rather than estimated from the template.
func TestPrimeCost(t *testing.T) {
	t.Run("the gate on records the session's prime cost", func(t *testing.T) {
		fx, _, _ := primedFixture(t, 40)
		gateOn(t, fx.root)

		out := runPrimeCapturing(t)

		rec := readPrimeCost(t, fx.root, fx.agent)
		if rec.Primes != 1 {
			t.Errorf("primes = %d, want 1", rec.Primes)
		}
		if rec.Bytes != int64(len(out)) {
			t.Errorf("bytes = %d, want %d — the figure must be what the agent actually received",
				rec.Bytes, len(out))
		}
		if rec.TokensEst <= 0 {
			t.Errorf("tokens_est = %d, want a positive estimate", rec.TokensEst)
		}
		if rec.SessionID == "" {
			t.Error("the record carries no session id, so it joins to nothing")
		}

		// Per session, so a second prime accumulates rather than starting over.
		second := runPrimeCapturing(t)
		rec2 := readPrimeCost(t, fx.root, fx.agent)
		if rec2.Primes != 2 {
			t.Errorf("primes = %d after two primes, want 2", rec2.Primes)
		}
		if rec2.Bytes != rec.Bytes+int64(len(second)) {
			t.Errorf("bytes = %d, want %d", rec2.Bytes, rec.Bytes+int64(len(second)))
		}
	})

	t.Run("the gate off records nothing", func(t *testing.T) {
		fx, _, _ := primedFixture(t, 40)

		runPrimeCapturing(t)

		if _, err := os.Stat(primeCostPath(fx.root, fx.agent)); !os.IsNotExist(err) {
			t.Errorf("the telemetry gate is off but a prime-cost record was written (stat err %v)", err)
		}
	})
}

func readPrimeCost(t *testing.T, root, agent string) primeCostRecord {
	t.Helper()
	data, err := os.ReadFile(primeCostPath(root, agent))
	if err != nil {
		t.Fatalf("reading prime cost: %v", err)
	}
	var rec primeCostRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("prime cost is not JSON: %v", err)
	}
	return rec
}

// TestPrimeAdmission is K7's open-time half. It is advisory ONLY: prime is a SessionStart hook, and
// a hook that recycled the session it was invoked for would recycle it before it ever ran.
func TestPrimeAdmission(t *testing.T) {
	t.Run("a step that will not fit is said out loud and recorded, and nothing else", func(t *testing.T) {
		fx, _, step := primedFixture(t, 60)
		gateOn(t, fx.root)
		armTokenomics(t, fx.root, 10, 1)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, 150000, 3)

		out := runPrimeCapturing(t)

		if !strings.Contains(out, "Session Economics") {
			t.Error("a no-fit verdict at step open said nothing to the agent")
		}
		assertStepContract(t, out)
		// Counted by MECHANISM rather than by kind. Phase 5's advisories fire on the same verb and
		// write the same record kind, so a bare count would measure them too and this subtest would
		// go red for a reason that has nothing to do with K7. The precision is what the mechanism
		// label bought.
		if got := len(interventionsByMechanism(t, fx.root, fx.agent)[string(tokenomics.MechanismBudget)]); got != 1 {
			t.Errorf("budget intervention records = %d, want exactly 1 — one firing leaves one record", got)
		}
		if got := len(readRecoveryLogLines(t, fx.root)); got != 0 {
			t.Errorf("af prime recycled a session (%d funnel lines); the open-time check is advisory only", got)
		}

		// The advisory must NOT arm K17's latch. The latch suppresses every watchdog fire class,
		// context_exhaustion included, so arming it here would take the #596 exhaustion ladder away
		// from a session at the highest occupancy this mechanism knows how to find — and the text
		// printed just above tells the agent to keep working, which is not a wait.
		if interventionLatchHolds(loadRecoveryState(fx.root, fx.agent), boundaryTestNow()) {
			t.Error("the open-time advisory latched the watchdog off; it describes an agent that is " +
				"working, not one that is waiting by design")
		}
	})

	t.Run("the intervention record joins to the step it fired on", func(t *testing.T) {
		fx, epic, step := primedFixture(t, 60)
		gateOn(t, fx.root)
		armTokenomics(t, fx.root, 10, 1)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, 150000, 3)

		runPrimeCapturing(t)

		records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		var iv, start telemetry.StepEvent
		for _, r := range records {
			switch {
			case r.Event == telemetry.EventIntervention && r.Mechanism == string(tokenomics.MechanismBudget):
				iv = r
			case r.Event == telemetry.EventStepStart:
				start = r
			}
		}
		if start.StepID == "" {
			t.Fatal("no step_start record; there is nothing to join against")
		}
		for _, k := range []struct{ name, got, want string }{
			{"formula", iv.Formula, start.Formula},
			{"instance_id", iv.InstanceID, epic.ID},
			{"step_id", iv.StepID, start.StepID},
			{"session_id", iv.SessionID, start.SessionID},
			{"model", iv.Model, start.Model},
		} {
			if k.got != k.want {
				t.Errorf("intervention %s = %q, want %q", k.name, k.got, k.want)
			}
		}
		if iv.StepSeq != start.StepSeq {
			t.Errorf("intervention step_seq = %d, step_start step_seq = %d", iv.StepSeq, start.StepSeq)
		}
		if iv.Verb != "prime" {
			t.Errorf("verb = %q, want %q", iv.Verb, "prime")
		}
		// #678 K1. Budget triggers on window pressure, so it is a capacity act — and until K4 gives
		// efficiency a predicate that cannot read a window, every mechanism in the tree is. The label
		// has to be written AT the firing: these records are append-only, so a firing that goes out
		// unlabelled can never be told apart from a later efficiency one afterwards.
		if iv.Objective != telemetry.ObjectiveCapacity {
			t.Errorf("intervention objective = %q, want %q", iv.Objective, telemetry.ObjectiveCapacity)
		}
	})

	t.Run("a factory that has learned nothing says nothing", func(t *testing.T) {
		// The non-vacuity control on the other side: without it every assertion above passes against
		// an advisory that prints unconditionally.
		fx, _, _ := primedFixture(t, 60)
		gateOn(t, fx.root)
		armTokenomics(t, fx.root, 10, 1)

		out := runPrimeCapturing(t)

		if strings.Contains(out, "Session Economics") {
			t.Error("a cold factory refused admission; observe must ADMIT (K7 fails open)")
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 0 {
			t.Errorf("nothing fired but %d intervention records were written", got)
		}
		if interventionLatchHolds(loadRecoveryState(fx.root, fx.agent), boundaryTestNow()) {
			t.Error("a factory that has learned nothing latched the watchdog off")
		}
	})

	t.Run("no occupancy reading admits", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		epic, step := seedTwoStepBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		gateOn(t, fx.root)
		armTokenomics(t, fx.root, 10, 1)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, 150000, 3)

		out := runPrimeCapturing(t)

		if strings.Contains(out, "Session Economics") {
			t.Error("a session with no occupancy datum was refused; K7 fails OPEN on a nil reading")
		}
		assertStepContract(t, out)
	})
}

// TestPrimeEconomics_WordsAppetiteAsGrowth pins N3 (r3906601... "historically needed" mislabels the
// marginal): the figure the economics block prints is adm.appetite.Tokens, the MARGINAL growth
// (peak - start), not a footprint a step "needs". "needed" describes a footprint; the honest verb for
// growth is "grown by"/"added about". RED at head (the block says "historically needed").
func TestPrimeEconomics_WordsAppetiteAsGrowth(t *testing.T) {
	fx, _, step := primedFixture(t, 60)
	gateOn(t, fx.root)
	armTokenomics(t, fx.root, 10, 1)
	model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
	seedAppetite(t, fx.root, "offpath", stepLabelOf(step), model, 150000, 3)

	out := runPrimeCapturing(t)

	if !strings.Contains(out, "Session Economics") {
		t.Fatalf("the no-fit economics block did not render, so there is nothing to word-check:\n%s", out)
	}
	if strings.Contains(out, "historically needed") {
		t.Errorf("the economics block says the appetite was 'historically needed' — a footprint word for a "+
			"figure that is marginal growth (peak-start); it must read 'grown by'/'added about':\n%s", out)
	}
	if !strings.Contains(out, "grown by") && !strings.Contains(out, "added about") {
		t.Errorf("the economics block does not describe the appetite as growth ('grown by'/'added about'):\n%s", out)
	}
}
