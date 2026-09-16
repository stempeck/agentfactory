package cmd

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// These tests do not run in parallel, for boundary_handoff_test.go:20-21's reason: they reassign
// the same package vars.

// armTokenomics switches BOTH inputs of the umbrella on — the factory toggle file and the
// startup.json enum — because either alone leaves every mechanism resolved off (Gotcha 12). The
// margin and the run floor are stated rather than defaulted so a fixture's arithmetic is readable
// from the fixture.
func armTokenomics(t *testing.T, root string, marginPct, minRuns int) {
	t.Helper()
	if err := os.WriteFile(tokenomicsGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write tokenomics gate: %v", err)
	}
	body := fmt.Sprintf(
		`{"tokenomics":{"enabled":"on","budget":"on","admission_margin_pct":%d,"learned_min_runs":%d}}`,
		marginPct, minRuns)
	if err := os.WriteFile(config.StartupConfigPath(root), []byte(body), 0o644); err != nil {
		t.Fatalf("write startup.json: %v", err)
	}
	// Through the real loader, so a fixture whose config the production path would reject fails
	// here rather than reappearing as an unexplained inert mechanism three assertions later.
	cfg, err := config.LoadStartupConfig(root)
	if err != nil {
		t.Fatalf("the fixture's startup.json does not load: %v", err)
	}
	if cfg.StepContext.HandoffPct < 1 {
		t.Fatalf("fixture startup.json left handoff_pct unseated: %+v", cfg.StepContext)
	}
}

// seedAppetite writes a learned aggregate for one key through the production writer, so the test
// and af done compose the digest path the same way.
//
// It seeds the marginal EQUAL to the peak: the collapsed pre-fix world where the whole footprint is
// treated as the step's growth. That keeps every seedAppetite call site firing the additive decision
// on the same figure it drove before thread T1 split the two appetites — the split shows up only in
// seedMarginalAppetite, which sets them apart. A fixture that never sets a marginal would, after the
// fix, leave AppetiteFor unknown and every additive no-fit would silently degrade to observe.
func seedAppetite(t *testing.T, root, formula, stepID, model string, peak int64, runs int) {
	t.Helper()
	path := telemetry.LearnedDigestPath(config.TelemetryDir(root), formula)
	d, err := tokenomics.LoadDigest(path)
	if err != nil {
		t.Fatalf("LoadDigest: %v", err)
	}
	d.Put(tokenomics.DigestKey{Formula: formula, StepID: stepID, Model: model}, tokenomics.Aggregate{
		Runs: runs, MedianPeakCtxTokens: peak, MedianMarginalCtxTokens: peak, UpdatedAt: "2026-08-30T00:00:00.000Z",
	})
	if err := tokenomics.SaveDigest(path, d); err != nil {
		t.Fatalf("SaveDigest: %v", err)
	}
}

// seedMarginalAppetite is seedAppetite's split-appetite twin: it writes a distinct absolute peak
// AND marginal growth for one key, so a fixture can drive the additive decision (which reads the
// marginal) and freshFits (which reads the absolute peak) apart. The two figures collapse onto one
// only when peak == marginal, which is the pre-fix world seedAppetite still describes.
func seedMarginalAppetite(t *testing.T, root, formula, stepID, model string, peak, marginal int64, runs int) {
	t.Helper()
	path := telemetry.LearnedDigestPath(config.TelemetryDir(root), formula)
	d, err := tokenomics.LoadDigest(path)
	if err != nil {
		t.Fatalf("LoadDigest: %v", err)
	}
	d.Put(tokenomics.DigestKey{Formula: formula, StepID: stepID, Model: model}, tokenomics.Aggregate{
		Runs: runs, MedianPeakCtxTokens: peak, MedianMarginalCtxTokens: marginal,
		UpdatedAt: "2026-08-30T00:00:00.000Z",
	})
	if err := tokenomics.SaveDigest(path, d); err != nil {
		t.Fatalf("SaveDigest: %v", err)
	}
}

// nextStepID returns the id of the step armBoundaryFixture leaves BEHIND the primed one — the step
// whose appetite a close-time admission check is actually about.
func nextStepID(t *testing.T, fx lifecycleFixture, epicID, primedID string) string {
	t.Helper()
	items, err := fx.mem.List(t.Context(), issuestore.Filter{Parent: epicID})
	if err != nil {
		t.Fatalf("listing steps: %v", err)
	}
	for _, it := range items {
		if it.ID != primedID {
			return it.ID
		}
	}
	t.Fatal("the fixture has no second step; admission would have nothing to predict")
	return ""
}

// nextStepLabel is nextStepID's counterpart for the paths that go through production admission: a
// close-time check keys the learned lookup on the next step's STABLE label (stepLabelOf), not its
// per-instance bead id, so a fixture arming that lookup has to file the appetite under the same
// label af done will resolve from the bead.
func nextStepLabel(t *testing.T, fx lifecycleFixture, epicID, primedID string) string {
	t.Helper()
	items, err := fx.mem.List(t.Context(), issuestore.Filter{Parent: epicID})
	if err != nil {
		t.Fatalf("listing steps: %v", err)
	}
	for _, it := range items {
		if it.ID != primedID {
			return stepLabelOf(it)
		}
	}
	t.Fatal("the fixture has no second step; admission would have nothing to predict")
	return ""
}

// armNoFitFixture puts a two-step formula in the more-steps position with occupancy BELOW the
// handoff threshold and a learned appetite for the next step that cannot fit beside it. Occupancy
// below the threshold is the whole point: it is the cell where the #622 predicate alone refuses and
// only admission can fire, so a fixture at 88% would prove nothing about K7.
func armNoFitFixture(t *testing.T, fx lifecycleFixture, occupancyPct float64) issuestore.Issue {
	t.Helper()
	now := boundaryTestNow()
	epic, step := seedTwoStepBeads(t, fx)
	writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
	writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)
	writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", occupancyPct, 1000, now.Add(-10*time.Second), now)

	armTokenomics(t, fx.root, 10, 1)
	model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
	// 150000 against a 200000 window with ~120000 already occupied projects to 135% — no fit — and
	// against an empty window it projects to 75%, inside the 90% headroom, so a fresh session WOULD
	// take it. Both halves matter: the handoff is only worth taking when it actually helps.
	seedAppetite(t, fx.root, "offpath", nextStepLabel(t, fx, epic.ID, step.ID), model, 150000, 3)
	return step
}

// TestBoundaryRefusesGateClose is AC-1's must-pass: the gate-close exemption is UNCONDITIONAL. A
// no-fit admission verdict supplies an operand to the boundary decision; it does not get a recycle
// path of its own, and the first conjunct of the predicate outranks it (D7).
func TestBoundaryRefusesGateClose(t *testing.T) {
	t.Run("the predicate refuses whatever admission says", func(t *testing.T) {
		now := boundaryTestNow()
		root := t.TempDir()
		fresh := plantSessionSnapshot(t, root, "manager", "sessa", 95, 1000, now.Add(-10*time.Second), now)
		cfg := config.StepContextConfig{BoundTokens: 200000, HandoffPct: 75}

		// Non-vacuity first: an operand that is not really a no-fit would make the refusal below
		// prove nothing.
		d := tokenomics.Admit(
			tokenomics.Window{Tokens: 200000, Source: "declared"},
			tokenomics.Occupancy{Tokens: 180000, Known: true},
			tokenomics.Appetite{Tokens: 60000, Runs: 5, Known: true},
			tokenomics.ResolvePolicy(true, config.TokenomicsConfig{
				Enabled: "on", Budget: "on", AdmissionMarginPct: 10, LearnedMinRuns: 2,
			}),
		)
		if d.Verdict != tokenomics.VerdictNoFit {
			t.Fatalf("fixture verdict = %q, want %q", d.Verdict, tokenomics.VerdictNoFit)
		}

		// Both pressure operands true: #678 K6 adds a second one, and "unconditional" has to mean the
		// gate-close conjunct outranks EVERY operand downstream of it, not just the one that existed
		// when the rule was written.
		if shouldBoundaryHandoff(fresh, cfg, true, true, true, true) {
			t.Error("a gate close fired the boundary under a no-fit verdict; the exemption is unconditional (HIGH-2)")
		}
		if !shouldBoundaryHandoff(fresh, cfg, false, true, true, false) {
			t.Error("the same cell off a gate close did not fire; the refusal above proves nothing")
		}
		// And the efficiency operand alone, so the refusal above is not resting on the capacity one.
		if !shouldBoundaryHandoff(fresh, cfg, false, true, false, true) {
			t.Error("an efficiency relaunch off a gate close did not fire; #678 K6's operand is not wired")
		}
	})

	t.Run("af done --phase-complete is inert with admission armed", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		armNoFitFixture(t, fx, 60)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, true, "gate-1"); err != nil {
			t.Fatalf("af done --phase-complete: %v", err)
		}
		if rec.calls != 0 {
			t.Errorf("boundary fired on a gate close with admission armed (%d calls) — HIGH-2", rec.calls)
		}
		// HIGH-2 excludes the handoff, not the measurement.
		end := lastStepEnd(t, fx.root, fx.agent)
		if end.Status != telemetry.StatusGateWaiting {
			t.Errorf("step_end status = %q, want %q", end.Status, telemetry.StatusGateWaiting)
		}
		if end.CtxUsedPct == nil {
			t.Error("a gate close recorded no occupancy")
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 0 {
			t.Errorf("a refused mechanism wrote %d intervention records, want 0", got)
		}
	})

	// Without this the whole test passes against an admission wiring that never fires at all.
	t.Run("non-vacuity control: the same fixture off a gate close hands off", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		armNoFitFixture(t, fx, 60)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
		if rec.calls != 1 {
			t.Fatalf("boundary executed %d times below the handoff threshold, want 1: "+
				"admission is the predictive input that fires this cell", rec.calls)
		}

		// The funnel line an operator greps has to survive the same reading the printed message
		// does. observed 60 against threshold 75 with nothing else on the record says "this
		// boundary fired below its own bound", which is a bug report about the mechanism rather
		// than a description of it.
		d := rec.opts.TriggerDetail
		if d.ProjectedPct <= float64(d.ThresholdPct) {
			t.Errorf("funnel projected_pct = %.0f against threshold %d — an admission-driven recycle "+
				"must record what it was taken against, not only what was measured",
				d.ProjectedPct, d.ThresholdPct)
		}
		if d.ObservedPct >= float64(d.ThresholdPct) {
			t.Errorf("funnel observed_pct = %.0f is not below threshold %d; this fixture no longer "+
				"exercises the cell where only a projection can fire", d.ObservedPct, d.ThresholdPct)
		}
	})
}

// TestBoundaryAdmission is the admission axis of AC-1's matrix.
//
// The composition is stated here because the plan states the rule and not the arithmetic: a no-fit
// verdict is OR'd into the TERMINAL comparison only. Every one of the predicate's four
// short-circuit refusals — gate close, no work following, an unconfigured threshold, an unhealthy
// channel — outranks it. So admission can RAISE a handoff that occupancy alone would not, and can
// never resurrect one the predicate has already refused for a reason of its own.
func TestBoundaryAdmission(t *testing.T) {
	now := boundaryTestNow()
	cfg := config.StepContextConfig{BoundTokens: 200000, HandoffPct: 75}

	reading := func(state string, pct float64) statusline.ChannelReading {
		root := t.TempDir()
		switch state {
		case "fresh":
			return plantSessionSnapshot(t, root, "manager", "sessa", pct, 1000, now.Add(-10*time.Second), now)
		case "stale":
			return plantSessionSnapshot(t, root, "manager", "sessa", pct, 1000, now.Add(-5*time.Minute), now)
		case "dark":
			return plantSessionSnapshot(t, root, "manager", "sessa", pct, 1000, now.Add(-30*time.Minute), now)
		case "none":
			return readSessionReading(t, root, "manager", "sessa", now)
		case "malformed":
			// A snapshot file that exists and cannot be parsed. It is a distinct row from "none"
			// because the two arrive by different routes — nothing written yet versus a truncated
			// or half-flushed write — and a reader that surfaced the second as anything other than
			// "no datum" would let corrupt bytes arm a handoff.
			dir := config.StatuslineSessionsDir(root)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "sessa.json"), []byte("{not json"), 0o644); err != nil {
				t.Fatal(err)
			}
			return readSessionReading(t, root, "manager", "sessa", now)
		}
		t.Fatalf("unknown state %q", state)
		return statusline.NoReading()
	}

	for _, tc := range []struct {
		name        string
		state       string
		pct         float64
		noFit       bool
		gateClose   bool
		workFollows bool
		want        bool
	}{
		{"below threshold, no-fit fires", "fresh", 60, true, false, true, true},
		{"below threshold, fits, inert", "fresh", 60, false, false, true, false},
		{"above threshold, fits, still fires", "fresh", 88, false, false, true, true},
		{"above threshold, no-fit, fires once", "fresh", 88, true, false, true, true},
		{"gate close outranks no-fit", "fresh", 88, true, true, true, false},
		{"nothing follows outranks no-fit", "fresh", 88, true, false, false, false},
		{"a stale channel outranks no-fit", "stale", 95, true, false, true, false},
		{"a dark channel outranks no-fit", "dark", 95, true, false, true, false},
		{"an absent channel outranks no-fit", "none", 0, true, false, true, false},
		{"a malformed snapshot outranks no-fit", "malformed", 0, true, false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The efficiency operand is false throughout: this matrix is the CAPACITY operand's, and a
			// second true would make every "fires" row pass for the wrong reason.
			got := shouldBoundaryHandoff(reading(tc.state, tc.pct), cfg, tc.gateClose, tc.workFollows, tc.noFit, false)
			if got != tc.want {
				t.Errorf("shouldBoundaryHandoff = %v, want %v", got, tc.want)
			}
		})
	}

	t.Run("an unconfigured threshold outranks no-fit", func(t *testing.T) {
		if shouldBoundaryHandoff(reading("fresh", 99), config.StepContextConfig{}, false, true, true, true) {
			t.Error("a zero handoff_pct fired under a no-fit verdict; an unconfigured factory must stay inert")
		}
	})

	// The assembly half: the operand the predicate is handed has to come out of real factory state,
	// or the matrix above is a matrix over a bool nothing produces.
	t.Run("the operand is assembled from the factory", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		epic, step := seedTwoStepBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 60, 1000, now.Add(-10*time.Second), now)
		armTokenomics(t, fx.root, 10, 1)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		next := nextStepID(t, fx, epic.ID, step.ID)
		seedAppetite(t, fx.root, "offpath", next, model, 150000, 3)

		startupCfg, err := config.LoadStartupConfig(fx.root)
		if err != nil {
			t.Fatalf("LoadStartupConfig: %v", err)
		}
		r := stepContextReading(fx.root, fx.workDir, fx.agent, startupCfg.Recovery, now)
		a := stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", next, r, startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct)

		if a.decision.Verdict != tokenomics.VerdictNoFit {
			t.Fatalf("verdict = %q (%s), want %q", a.decision.Verdict, a.decision.Reason, tokenomics.VerdictNoFit)
		}
		if !a.handoffHelps() {
			t.Error("the step does not fit here and DOES fit a fresh window, so a handoff helps")
		}
		if a.decision.ProjectedPct <= 100 {
			t.Errorf("projected = %.1f%%, want the arithmetic to show its working", a.decision.ProjectedPct)
		}
	})

	t.Run("a cold factory observes rather than refuses", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 60, 1000, now.Add(-10*time.Second), now)
		armTokenomics(t, fx.root, 10, 1)

		startupCfg, err := config.LoadStartupConfig(fx.root)
		if err != nil {
			t.Fatalf("LoadStartupConfig: %v", err)
		}
		r := stepContextReading(fx.root, fx.workDir, fx.agent, startupCfg.Recovery, now)
		a := stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", "no-such-step", r, startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct)

		if a.decision.Verdict != tokenomics.VerdictObserve || a.decision.Reason != tokenomics.ReasonNoLearnedData {
			t.Errorf("cold verdict = %q/%q, want observe/%q", a.decision.Verdict, a.decision.Reason, tokenomics.ReasonNoLearnedData)
		}
		if a.handoffHelps() {
			t.Error("a factory that has learned nothing recycled a session on it")
		}
		if !a.admits() {
			t.Error("observe must ADMIT: absence is not a refusal (K7 fail-open)")
		}
	})

	t.Run("the umbrella's config leg vetoes the mechanism", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		epic, step := seedTwoStepBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 60, 1000, now.Add(-10*time.Second), now)
		armTokenomics(t, fx.root, 10, 1)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		next := nextStepID(t, fx, epic.ID, step.ID)
		seedAppetite(t, fx.root, "offpath", next, model, 150000, 3)

		startupCfg, err := config.LoadStartupConfig(fx.root)
		if err != nil {
			t.Fatalf("LoadStartupConfig: %v", err)
		}
		tcfg := startupCfg.Tokenomics
		tcfg.Enabled = "off"
		r := stepContextReading(fx.root, fx.workDir, fx.agent, startupCfg.Recovery, now)
		a := stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", next, r, tcfg, startupCfg.Recovery.ContextThresholdPct)

		if a.decision.Reason != tokenomics.ReasonMechanismOff {
			t.Errorf("reason = %q, want %q — the enum vetoes the toggle file", a.decision.Reason, tokenomics.ReasonMechanismOff)
		}
		if a.handoffHelps() {
			t.Error("a mechanism the operator switched off recycled a session")
		}
	})

	// The window and the appetite are two halves of one arithmetic and must describe ONE profile.
	// They stopped doing so once the window was resolved from cfg.Agents/cfg.Default while the
	// appetite was keyed on resolveRecordModel's answer: `af sling --model X` writes a marker that
	// only the second of those two reads, so a step's cost was being weighed against a window
	// belonging to a backend the session was not running on.
	t.Run("a --model launch weighs the step against the profile it is actually running", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		models := `{"default":"small","models":{
			"small":{"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"},
			"big":{"CLAUDE_CODE_MAX_CONTEXT_TOKENS":"1000000"}}}`
		if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
			t.Fatal(err)
		}
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 60, 1000, now.Add(-10*time.Second), now)
		seedAppetite(t, fx.root, "offpath", "s-1", "big", 150000, 3)

		startupCfg, err := config.LoadStartupConfig(fx.root)
		if err != nil {
			t.Fatalf("LoadStartupConfig: %v", err)
		}
		r := stepContextReading(fx.root, fx.workDir, fx.agent, startupCfg.Recovery, now)

		// Control: with no marker the agent runs the default profile, whose 200000-token window is
		// what the small-profile appetite would have to fit in — and it has no learned data at all
		// under that key, so the mechanism is inert. This is the state the marker changes.
		if a := stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", "s-1", r, startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct); a.window.Tokens != 200000 {
			t.Fatalf("no marker: window = %d, want the default profile's 200000", a.window.Tokens)
		}

		writeModelOverride(fx.workDir, "big")
		a := stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", "s-1", r, startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct)

		if a.appetite.Tokens != 150000 {
			t.Fatalf("the appetite was not keyed on the launched profile (tokens = %d); the rest of "+
				"this assertion proves nothing", a.appetite.Tokens)
		}
		if a.window.Tokens != 1000000 {
			t.Errorf("window = %d, want 1000000 — the marker names the profile this session runs, and "+
				"the window must come from the same profile the appetite was keyed on", a.window.Tokens)
		}
		if !a.admits() {
			t.Error("150000 of a 1000000-token window at 60% of 200000 occupancy fits; the mechanism " +
				"refused a step its real backend has room for")
		}
	})

	t.Run("a nil occupancy reading admits", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		startupCfg, err := config.LoadStartupConfig(fx.root)
		if err != nil {
			t.Fatalf("LoadStartupConfig: %v", err)
		}
		a := stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", "s-1", statusline.NoReading(), startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct)

		if !a.admits() {
			t.Error("a nil reading refused admission; K7 fails OPEN, which is the inverse of this file's grain and deliberate")
		}
		if a.decision.Reason != tokenomics.ReasonNoOccupancy {
			t.Errorf("reason = %q, want %q", a.decision.Reason, tokenomics.ReasonNoOccupancy)
		}
	})

	// --- the final-step call site ----------------------------------------------------------------
	//
	// The plan wires admission at BOTH boundary call sites. The final-step one is the branch that
	// hands the window to an improvement session, and the interesting property is not that it fires
	// — nothing there can make it fire today — but WHAT it asks about. A site that reached for the
	// nearest available step id would key on the step that has just closed, and predict the cost of
	// work the session has already paid for.

	t.Run("the final-step boundary asks admission about no step, never about the one just closed", func(t *testing.T) {
		now := boundaryTestNow()
		t.Setenv("AF_ROLE", "alpha")
		root := setupImprovementFiringFactory(t)
		armTokenomics(t, root, 10, 1)
		writeRuntimeFile(t, root, "formula_caller", "supervisor")
		writeRuntimeFile(t, root, "session_id", "sess.imp\n")
		// 40% of the window, well under the 75% default: the cell where the #622 predicate alone
		// refuses, so a handoff here could only have come from admission.
		plantSessionSnapshot(t, root, "alpha", "sessimp", 40, 100, now.Add(-10*time.Second), now)

		mem := memstore.New()
		instanceID := seedCompletedFormula(t, mem, "Formula: widget")
		closedStepID := onlyChild(t, mem, instanceID)

		// The appetite filed under the step that just CLOSED, sized so that finding it is FATAL and
		// the assertion below cannot pass by arithmetic. plantSessionSnapshot's window is 200000, so
		// 40% is 80000 used and the 10% margin leaves 180000 of headroom: a site keyed on this step
		// projects 80000+150000 = 115% of the window, no-fits, and — since 150000 alone fits a fresh
		// session at 75% — handoffHelps, fires, and turns this subtest red. A modest number here
		// (900, say) would project 40.45%, admit, and let the very mistake this exists to exclude
		// pass it. Same value armNoFitFixture uses, for the same reason.
		model, _ := resolveRecordModel(root, root, "alpha", "")
		seedAppetite(t, root, "widget", closedStepID, model, 150000, 5)

		tmuxPaneEnv(t)
		rec := (&boundaryRecorder{}).install(t)
		captureStdout(t, func() {
			if err := sendWorkDoneAndCleanup(t.Context(), mem, root, root, instanceID, false); err != nil {
				t.Fatalf("final af done: %v", err)
			}
		})

		if rec.calls != 0 {
			t.Errorf("the final-step boundary fired at 40%% occupancy (%d calls) — the only appetite "+
				"on disk belongs to the step that just closed, and history already paid for is not a "+
				"prediction about what comes next", rec.calls)
		}
		// Belt and braces, and worth saying so: this seam drives sendWorkDoneAndCleanup directly
		// rather than through runDoneCore, so recordIntervention would early-return on the ctx
		// telemetry flag whatever the mechanism decided. The load-bearing assertion is rec.calls
		// above; AC-4's record is proved in TestInterventionRecordJoinsToStep, which goes through
		// the real verb.
		if n := countEvents(t, root, "alpha", telemetry.EventIntervention); n != 0 {
			t.Errorf("intervention records = %d, want 0", n)
		}
	})

	t.Run("control: the same fixture still hands off on occupancy alone", func(t *testing.T) {
		// Without this the subtest above passes for a fixture that could never fire at all, which
		// would make "it did not fire" evidence of nothing.
		now := boundaryTestNow()
		t.Setenv("AF_ROLE", "alpha")
		root := setupImprovementFiringFactory(t)
		armTokenomics(t, root, 10, 1)
		writeRuntimeFile(t, root, "formula_caller", "supervisor")
		writeRuntimeFile(t, root, "session_id", "sess.imp\n")
		plantSessionSnapshot(t, root, "alpha", "sessimp", 92, 100, now.Add(-10*time.Second), now)

		mem := memstore.New()
		instanceID := seedCompletedFormula(t, mem, "Formula: widget")

		tmuxPaneEnv(t)
		rec := (&boundaryRecorder{}).install(t)
		captureStdout(t, func() {
			if err := sendWorkDoneAndCleanup(t.Context(), mem, root, root, instanceID, false); err != nil {
				t.Fatalf("final af done: %v", err)
			}
		})
		if rec.calls != 1 {
			t.Fatalf("the final-step boundary executed %d times at 92%% occupancy, want 1", rec.calls)
		}
	})

	t.Run("both boundary call sites pass an assembled operand, never a literal", func(t *testing.T) {
		// Structural, and it is the only honest way to pin this clause: the two sites agree on
		// today's ANSWER (the final-step key is empty, so admission observes and the operand is
		// false either way), and a behavioural test therefore cannot tell an assembled verdict from
		// a hardcoded false. What separates them is what happens when the cache underneath changes,
		// which is exactly what a drift guard is for. Same technique, and same reason, as
		// interview_test.go's "all three recycle legs reach the interview".
		//
		// One level of indirection defeats it — `noFit := false; shouldBoundaryHandoff(…, noFit)`
		// reads as an Ident that is not a literal — so do not over-trust it. It catches the way this
		// actually regresses, which is someone deleting an operand they think is dead. The sites
		// count is the D7 pin: a third DECIDING caller of this predicate is a second owner of the
		// recycle.
		//
		// efficiencyCausedBoundary is the one caller that is not deciding, and it is walked under the
		// opposite rule rather than skipped. It asks the counterfactual — would this boundary have
		// fired WITHOUT the efficiency operand — to decide who the recycle is billed to, so a literal
		// false is its entire contract and an assembled operand there would silently make the answer
		// "efficiency caused every handoff it coincided with". Exempting it without pinning that would
		// leave a hole shaped exactly like the guard.
		const counterfactual = "efficiencyCausedBoundary"
		fset := token.NewFileSet()
		parsed, err := parser.ParseFile(fset, "done.go", nil, 0)
		if err != nil {
			t.Fatalf("parse done.go: %v", err)
		}
		deciding, counterfactuals := 0, 0
		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				id, ok := call.Fun.(*ast.Ident)
				if !ok || id.Name != "shouldBoundaryHandoff" {
					return true
				}
				if len(call.Args) != 6 {
					t.Errorf("shouldBoundaryHandoff called with %d args at %s, want 6",
						len(call.Args), fset.Position(call.Pos()))
					return true
				}
				lit := func(arg int) (string, bool) {
					id, ok := call.Args[arg].(*ast.Ident)
					if ok && (id.Name == "true" || id.Name == "false") {
						return id.Name, true
					}
					return "", false
				}
				if fn.Name.Name == counterfactual {
					counterfactuals++
					if got, isLit := lit(5); !isLit || got != "false" {
						t.Errorf("%s passes %s as the efficiency operand at %s, want the literal false; "+
							"the counterfactual must remove the operand it is measuring the effect of",
							counterfactual, types.ExprString(call.Args[5]), fset.Position(call.Pos()))
					}
					if _, isLit := lit(4); isLit {
						t.Errorf("%s hardcodes the admission operand at %s; it must reproduce the real "+
							"decision in every respect except the one it is removing",
							counterfactual, fset.Position(call.Pos()))
					}
					return true
				}
				deciding++
				// BOTH pressure operands, by index and by name. #678 K6 added the second one, and the
				// failure this guard exists to catch applies to it identically: an efficiency operand
				// hardcoded false is an actuator that is wired, recorded and permanently inert.
				for _, operand := range []struct {
					arg  int
					name string
				}{{4, "admission"}, {5, "efficiency"}} {
					if got, isLit := lit(operand.arg); isLit {
						t.Errorf("the %s operand at %s is the literal %q — this call site does not ask, "+
							"and will keep not asking after the thing it should be asking about changes",
							operand.name, fset.Position(call.Pos()), got)
					}
				}
				return true
			})
		}
		if deciding != 2 {
			t.Errorf("found %d DECIDING shouldBoundaryHandoff call sites in done.go, want 2 (more-steps "+
				"and final-step) — the plan wires admission at both", deciding)
		}
		if counterfactuals != 1 {
			t.Errorf("found %d shouldBoundaryHandoff calls inside %s, want exactly 1; the exemption "+
				"above is scoped to that one call and a second would ride in on it",
				counterfactuals, counterfactual)
		}
	})
}

// onlyChild returns the id of the single child seedCompletedFormula left behind, closed status and
// all, so a test can key a digest entry on the step that just finished.
func onlyChild(t *testing.T, mem issuestore.Store, parent string) string {
	t.Helper()
	items, err := mem.List(t.Context(), issuestore.Filter{Parent: parent, IncludeClosed: true})
	if err != nil {
		t.Fatalf("listing children: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("fixture has %d children, want exactly 1", len(items))
	}
	return items[0].ID
}

// TestInterventionRecordJoinsToStep is AC-4: a fired mechanism leaves exactly ONE intervention
// record, and that record joins to the step it fired on. The join is asserted against the step_end
// record rather than field-by-field against a literal, because "the field is non-empty" passes for
// a record that joins to the wrong step.
func TestInterventionRecordJoinsToStep(t *testing.T) {
	t.Run("af done close-time admission", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		armNoFitFixture(t, fx, 60)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
		if rec.calls != 1 {
			t.Fatalf("the mechanism did not fire (%d handoffs); there is no intervention to join", rec.calls)
		}

		records, _, err := telemetry.ReadEvents(config.TelemetryDir(fx.root), telemetry.Filter{Agent: fx.agent})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		var interventions []telemetry.StepEvent
		for _, r := range records {
			if r.Event == telemetry.EventIntervention {
				interventions = append(interventions, r)
			}
		}
		if len(interventions) != 1 {
			t.Fatalf("one fired mechanism wrote %d intervention records, want exactly 1", len(interventions))
		}
		iv := interventions[0]
		end := lastStepEnd(t, fx.root, fx.agent)

		for _, k := range []struct{ name, got, want string }{
			{"formula", iv.Formula, end.Formula},
			{"instance_id", iv.InstanceID, end.InstanceID},
			{"step_id", iv.StepID, end.StepID},
			{"session_id", iv.SessionID, end.SessionID},
			{"model", iv.Model, end.Model},
		} {
			if k.got != k.want {
				t.Errorf("intervention %s = %q, step_end %s = %q — the record does not join to its step",
					k.name, k.got, k.name, k.want)
			}
		}
		if iv.StepSeq != end.StepSeq {
			t.Errorf("intervention step_seq = %d, step_end step_seq = %d", iv.StepSeq, end.StepSeq)
		}
		if iv.Verb != "done" {
			t.Errorf("verb = %q, want %q — the record must name the surface that fired it", iv.Verb, "done")
		}
		if _, perr := time.Parse(telemetry.TimestampLayout, iv.TS); perr != nil {
			t.Errorf("ts %q does not parse with the record layout: %v", iv.TS, perr)
		}
	})

	t.Run("no fire means no record", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		now := boundaryTestNow()
		epic, step := seedTwoStepBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 20, 1000, now.Add(-10*time.Second), now)
		armTokenomics(t, fx.root, 10, 1)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}
		if rec.calls != 0 {
			t.Fatalf("the boundary fired at 20%% occupancy with no learned data (%d calls)", rec.calls)
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 0 {
			t.Errorf("nothing fired but %d intervention records were written", got)
		}
	})

	t.Run("a forced boundary handoff records even with the telemetry gate off (AC-3)", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		armNoFitFixture(t, fx, 60)

		tmuxPaneEnv(t)
		(&mailRecorder{}).install(t)
		rec := (&boundaryRecorder{}).install(t)

		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done with the telemetry gate off: %v", err)
		}
		// The mechanism FIRES with the gate off — it is armed by statusline data and the learned
		// digest, neither of which the telemetry gate owns.
		if rec.calls != 1 {
			t.Errorf("the telemetry gate suppressed the mechanism itself (%d handoffs), not just its record", rec.calls)
		}
		// #672 AC-3 flips the pre-existing assertion here (which required the gate-off path to write
		// NOTHING): a forced boundary handoff is an ENFORCEMENT act, and enforcement records must
		// survive the telemetry toggle. Telemetry is a separate, default-off, never-seeded switch, so
		// gating the record on it would leave run-#1 enforcement with zero retrievable proof it
		// happened — precisely what AC-3 (and corollary 3, "silence never passes") forbid.
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventIntervention); got != 1 {
			t.Errorf("the telemetry gate is off but the forced boundary handoff wrote %d intervention records, want 1", got)
		}
	})
}

// TestBoundaryHandoff_NoSpuriousRecycleOnCheapNextStep is thread T1's AC-3, the integration the fix
// exists for. handoffHelps() must be FALSE when the next step is cheap at the margin even though the
// session already sits at 45% occupancy: the additive decision reads the step's MARGINAL growth, so
// a step that only grows by ~5k does not no-fit beside a 45%-full session, and no recycle is spent
// on it. The double-counting predicate — reading the absolute peak instead — refuses it and burns a
// handoff the run does not need, the exact spurious recycle #668 measured.
func TestBoundaryHandoff_NoSpuriousRecycleOnCheapNextStep(t *testing.T) {
	now := boundaryTestNow()

	admit := func(t *testing.T, occPct float64, peak, marginal int64) admission {
		t.Helper()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1) // margin 10 ⇒ ceiling 90% of the 200000 window
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		plantSessionSnapshot(t, fx.root, fx.agent, "sessa", occPct, 1000, now.Add(-10*time.Second), now)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedMarginalAppetite(t, fx.root, "offpath", "next", model, peak, marginal, 3)

		startupCfg, err := config.LoadStartupConfig(fx.root)
		if err != nil {
			t.Fatalf("LoadStartupConfig: %v", err)
		}
		r := stepContextReading(fx.root, fx.workDir, fx.agent, startupCfg.Recovery, now)
		return stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", "next", r,
			startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct)
	}

	t.Run("a cheap next step at 45% occupancy does not recycle", func(t *testing.T) {
		// occ 90000 + marginal 5000 = 95000 of 200000 = 47.5% ⇒ admit ⇒ no handoff. The absolute peak
		// 108000 is what the double-count would add: 90000+108000 = 99% ⇒ a no-fit and a wasted recycle.
		a := admit(t, 45, 108_000, 5_000)
		if a.handoffHelps() {
			t.Errorf("handoffHelps() = true at 45%% occupancy on a step that grows by 5000 — the additive "+
				"decision double-counted the baseline (verdict %s, projected %.1f%%) and burned a recycle the "+
				"run did not need", a.decision.Verdict, a.decision.ProjectedPct)
		}
	})

	// Non-vacuity: a genuinely heavy next step at high occupancy still no-fits AND fits a fresh
	// session, so the mechanism DOES fire. Without this the "false" above would pass for a handoff
	// path that never fires at all.
	t.Run("a heavy next step at high occupancy still recycles", func(t *testing.T) {
		// occ 140000 + marginal 45000 = 185000 of 200000 = 92.5% ⇒ no-fit; the absolute peak 50000
		// fits a fresh session (25%), so a handoff genuinely helps.
		a := admit(t, 70, 50_000, 45_000)
		if !a.handoffHelps() {
			t.Errorf("handoffHelps() = false on a step that no-fits here (verdict %s, projected %.1f%%) yet "+
				"fits a fresh session — the marginal mechanism failed to fire where it should",
				a.decision.Verdict, a.decision.ProjectedPct)
		}
	})
}

// TestFreshFits_UsesAbsolutePeak is thread T1's AC-4 and the protective guard on item 5: after the
// additive decision is moved onto the marginal appetite, freshFits must KEEP projecting a fresh
// session on the ABSOLUTE peak. A fresh session's real footprint is baseline + marginal = the
// absolute peak; collapsing freshFits onto the marginal would model an empty session as needing only
// the step's growth and declare a recycle "helps" when the step overruns a fresh window too.
func TestFreshFits_UsesAbsolutePeak(t *testing.T) {
	now := boundaryTestNow()
	fx := newLifecycleFixture(t)
	armTokenomics(t, fx.root, 10, 1) // margin 10 ⇒ ceiling 90% of the 200000 window
	writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
	// 89% occupancy: high enough that even the small marginal no-fits, so the freshFits leg is reached.
	plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 89, 1000, now.Add(-10*time.Second), now)
	model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
	// A large absolute peak (95% of the window, no-fits even from empty) with a small marginal (5000).
	seedMarginalAppetite(t, fx.root, "offpath", "next", model, 190_000, 5_000, 3)

	startupCfg, err := config.LoadStartupConfig(fx.root)
	if err != nil {
		t.Fatalf("LoadStartupConfig: %v", err)
	}
	r := stepContextReading(fx.root, fx.workDir, fx.agent, startupCfg.Recovery, now)
	a := stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", "next", r,
		startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct)

	// The freshFits leg is only computed on a no-fit decision; assert the decision first so a
	// vacuous admit cannot let the freshFits assertion pass without exercising the seam.
	if a.decision.Verdict != tokenomics.VerdictNoFit {
		t.Fatalf("decision = %s (%.1f%%), want no-fit so the freshFits leg is reached",
			a.decision.Verdict, a.decision.ProjectedPct)
	}
	if a.freshFits {
		t.Error("freshFits = true — a 190000-token step (95% of a fresh window) cannot fit a fresh session; " +
			"freshFits collapsed onto the marginal (5000) instead of the absolute peak")
	}
}

// TestBreadcrumbOnlyWhenReduced pins that withEffortLevel writes the objective=efficiency reduce_effort
// breadcrumb only when a reduction genuinely happened. capEffortLevel returns the profile's DECLARED
// level when the profile declares a shallower ceiling than the plan chose, so a profile declaring `low`
// under a `medium` plan exports `low` — which equals what it declared, so nothing was reduced — and must
// write NO attestation; an undeclared profile whose exported level differs from its declared one DID
// receive a reduction and must still write one. The breadcrumb is the attestation af prime reads, so a
// stale one records a treatment the session never received.
//
// These tests do not run in parallel, for efficiency_actuator_test.go:28's reason.
func TestBreadcrumbOnlyWhenReduced(t *testing.T) {
	const nextStep = "step-2"

	setup := func(t *testing.T) lifecycleFixture {
		t.Helper()
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		armEfficiency(t, fx.root, nil)
		hookFormulaName(t, fx.workDir, "offpath")
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedEfficiency(t, fx.root, "offpath", nextStep, model, reducibleAggregate())
		return fx
	}

	// A profile declaring `low` under a `medium` plan exports `low` — capped to the declared level, so
	// nothing was reduced. No treatment happened, so no attestation may be written.
	t.Run("a launch whose export equals the profile's declared level writes no breadcrumb", func(t *testing.T) {
		fx := setup(t)

		got := withEffortLevel(fx.root, fx.workDir, launchEnv("low"), nextStep, "")

		if lvl := effortLevelIn(got); lvl != "low" {
			t.Fatalf("the export is %q, want the declared low; the breadcrumb assertion below only "+
				"means anything when the exported level equals the declared one", lvl)
		}
		if crumb := readEffortBreadcrumb(fx.workDir); crumb != (effortBreadcrumb{}) {
			t.Errorf("breadcrumb = %+v after a launch whose export (low) equals the profile's declared "+
				"level; nothing was reduced, so af prime must have no reduce_effort attestation to read — "+
				"a stale attestation records a treatment the session never received", crumb)
		}
	})

	// PROTECT. An undeclared profile whose exported level (medium) genuinely differs from the declared
	// one ("") DID receive a reduction, so the attestation must still be written. Mirrors
	// efficiency_actuator_test.go:193-212.
	t.Run("an undeclared profile whose export differs still writes the breadcrumb", func(t *testing.T) {
		fx := setup(t)

		if lvl := effortLevelIn(withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "")); lvl != "medium" {
			t.Fatalf("the export is %q, want medium; without a real reduction the attestation below "+
				"proves nothing", lvl)
		}
		crumb := readEffortBreadcrumb(fx.workDir)
		if crumb.Level != "medium" || crumb.Objective != string(tokenomics.ObjectiveEfficiency) {
			t.Errorf("breadcrumb = %+v, want level=medium objective=efficiency — the exported level "+
				"differs from the undeclared profile, so a reduction genuinely happened and af prime "+
				"reads its attestation from here", crumb)
		}
		if crumb.StepLabel != nextStep {
			t.Errorf("breadcrumb step_label = %q, want %q", crumb.StepLabel, nextStep)
		}
	})
}

// TestFirstSessionSelectsLevel pins that the level actuator selects a reduction on the FIRST session,
// before any af done has written .runtime/last_closed_step. withEffortLevel/selectEffortLevel take the
// formula name plumbed from a leg that knows it — nextReadyStep reads the instance-bead title, present
// from sling — and fall back to last_closed_step only when no leg supplies one. So a roomy first session
// on a profile with a reducible history is reduced at launch, not left at the host default until the
// first af done.
//
// These tests do not run in parallel, for efficiency_actuator_test.go:28's reason.
func TestFirstSessionSelectsLevel(t *testing.T) {
	const nextStep = "step-1"
	const formula = "offpath"

	// A first session: everything a launch leg holds EXCEPT last_closed_step. primedFixture seeds the
	// instance bead titled "Formula: offpath" and writes hooked_formula, but never hookFormulaName, so
	// hookedFormulaName resolves "" exactly as it does before the first af done.
	setup := func(t *testing.T) lifecycleFixture {
		t.Helper()
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		armEfficiency(t, fx.root, nil)
		model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
		seedEfficiency(t, fx.root, formula, nextStep, model, reducibleAggregate())
		if hookedFormulaName(fx.workDir) != "" {
			t.Fatal("the fixture wrote a last_closed_step; this no longer exercises the first session")
		}
		return fx
	}

	// The leg resolves the formula name from the instance title (as nextReadyStep does) and passes it in,
	// so the level is selected on session #1 without waiting for the first af done to write last_closed_step.
	t.Run("a first-session launch with the formula plumbed selects the level", func(t *testing.T) {
		fx := setup(t)

		got := withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, formula)

		if lvl := effortLevelIn(got); lvl != "medium" {
			t.Errorf("%s = %q on the first session, want medium — the step's learned history warrants a "+
				"reduction and the actuator must not wait for the first af done to apply it",
				config.EnvEffortLevel, lvl)
		}
		crumb := readEffortBreadcrumb(fx.workDir)
		if crumb.Level != "medium" || crumb.Objective != string(tokenomics.ObjectiveEfficiency) {
			t.Errorf("breadcrumb = %+v, want level=medium objective=efficiency — af prime reads the "+
				"first session's treatment from here and nowhere else", crumb)
		}
	})

	// The mechanism the leg relies on: nextReadyStep resolves BOTH the next step and the formula name
	// from the instance-bead title on the first session, with no last_closed_step present.
	t.Run("nextReadyStep resolves the formula from the instance title on the first session", func(t *testing.T) {
		fx := setup(t)

		label, resolved := nextReadyStep(t.Context(), fx.root, fx.workDir)

		if label != nextStep {
			t.Errorf("nextReadyStep label = %q, want %q", label, nextStep)
		}
		if resolved != formula {
			t.Errorf("nextReadyStep formula = %q, want %q — the instance bead titled %q is the only "+
				"source of the name before the first af done", resolved, formula, "Formula: "+formula)
		}
	})

	// Absence is not evidence. With no formula plumbed AND no last_closed_step, the actuator selects
	// nothing — it must never invent a formula to reduce against.
	t.Run("no plumbed formula and no last_closed_step selects nothing", func(t *testing.T) {
		fx := setup(t)

		got := withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "")

		if lvl := effortLevelIn(got); lvl != "" {
			t.Errorf("%s = %q with neither a plumbed formula nor a last_closed_step, want empty — the "+
				"actuator reduced against a formula it had no source for", config.EnvEffortLevel, lvl)
		}
		if crumb := readEffortBreadcrumb(fx.workDir); crumb != (effortBreadcrumb{}) {
			t.Errorf("a breadcrumb was written for a selection that could not have happened: %+v", crumb)
		}
	})
}
