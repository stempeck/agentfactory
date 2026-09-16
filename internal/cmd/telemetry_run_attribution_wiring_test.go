package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// attributionGit answers the two read-only derivations with fixed values, so a test can tell a
// field that was wired from a field that merely happens to be empty. The fixture's factory is a
// real git repo with no origin, where the honest answer to base_commit is "" — which is
// indistinguishable from never having asked.
const (
	fakeCheckoutSHA = "1111111111111111111111111111111111111111"
	fakeBaseSHA     = "2222222222222222222222222222222222222222"
)

func installAttributionGit(t *testing.T) {
	t.Helper()
	installFakeGit(t, func(_ string, args []string) string {
		switch args[0] {
		case "rev-parse":
			return fakeCheckoutSHA
		case "symbolic-ref":
			return "origin/main"
		case "merge-base":
			return fakeBaseSHA
		}
		return ""
	})
}

func findRecord(t *testing.T, fx lifecycleFixture, kind string) telemetry.StepEvent {
	t.Helper()
	for _, r := range recordsFor(t, fx) {
		if r.Event == kind {
			return r
		}
	}
	t.Fatalf("no %s record was written; the fixture never reached the site that writes it", kind)
	return telemetry.StepEvent{}
}

func formulaFileDigest(t *testing.T, root, formulaName string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(config.FormulasDir(root), formulaName+".formula.toml"))
	if err != nil {
		t.Fatalf("reading the formula back: %v", err)
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// TestRunAttributionIsWiredOntoTheRecords is the interlock the derivations cannot be (#678 K1).
//
// checkoutCommit, baseCommit, tokenomicsState, launchEffortLevel and gateFlagsInWindow are each
// pinned on their own, and every one of those pins stays green if the verbs simply never assign
// what they return. That failure is permanent in a way a normal regression is not: these records
// are append-only, so a run closed without its attribution can never be given it afterwards.
func TestRunAttributionIsWiredOntoTheRecords(t *testing.T) {
	t.Run("instance_start says what the run was made of", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		installAttributionGit(t)

		if err := slingOnce(t, fx); err != nil {
			t.Fatalf("af sling: %v", err)
		}

		start := findRecord(t, fx, telemetry.EventInstanceStart)
		if start.AFVersion != Version || start.AFCommit != Commit {
			t.Errorf("instance_start af_version/af_commit = %q/%q, want %q/%q — the binary that ran "+
				"is not derivable from anything else on the record", start.AFVersion, start.AFCommit, Version, Commit)
		}
		if start.CheckoutCommit != fakeCheckoutSHA {
			t.Errorf("checkout_commit = %q, want %q", start.CheckoutCommit, fakeCheckoutSHA)
		}
		if start.TokenomicsState != telemetry.TokenomicsStateOff {
			t.Errorf("tokenomics_state = %q, want %q for a factory with the umbrella switched off",
				start.TokenomicsState, telemetry.TokenomicsStateOff)
		}
		// effort_level belongs to a SESSION, and at instantiation no session exists to have one.
		if start.EffortLevel != "" {
			t.Errorf("instance_start carries effort_level = %q; the record kind that carries it is "+
				"session_start", start.EffortLevel)
		}
	})

	t.Run("step_end says what the step ran at and what it earned", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		t.Setenv(config.EnvEffortLevel, "low")
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)

		// The gates mail through `af mail send`, which builds a Router, which loads this. The
		// lifecycle fixture has no need of it and does not write one.
		if err := os.WriteFile(config.MessagingConfigPath(fx.root), []byte(`{"groups":{}}`), 0o644); err != nil {
			t.Fatalf("writing messaging.json: %v", err)
		}

		runLifecycleVerbsWithSession(t, fx, "sess-attribution", func() {
			sendGateVerdict(t, fx.agent, fx.agent, "STEP_FIDELITY")
			sendGateVerdict(t, fx.agent, fx.agent, "HANDOFF")
			// The window closes at the step_end record's own timestamp, which is stamped at
			// millisecond precision while the mail is stored at the clock's full resolution. Without
			// this pause the whole fixture — prime, send, close — completes inside one millisecond and
			// the verdict lands ON the closing edge, which a half-open window excludes. A real step
			// lasts minutes; only the fixture is this fast, and a slow machine only widens the gap.
			time.Sleep(5 * time.Millisecond)
		})

		end := lastStepEnd(t, fx.root, fx.agent)
		if end.EffortLevel != "low" {
			t.Errorf("step_end effort_level = %q, want %q — a step's cost is not comparable across "+
				"arms of the effort experiment without it", end.EffortLevel, "low")
		}
		if end.GateFlags == nil {
			t.Fatal("step_end carries no gate_flags: af done never asked the mail store, so every " +
				"step of this run looks equally correct to the quality guard")
		}
		if *end.GateFlags != 1 {
			t.Errorf("gate_flags = %d, want 1 — one fidelity verdict was filed inside the step's window "+
				"and one ordinary message was not", *end.GateFlags)
		}
	})

	t.Run("instance_end says what the work should be diffed against", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		installAttributionGit(t)

		epic, step := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		writeRuntimeFile(t, fx.workDir, "step_primed", step.ID)
		if err := runDoneCore(t.Context(), fx.workDir, false, ""); err != nil {
			t.Fatalf("af done: %v", err)
		}

		end := findRecord(t, fx, telemetry.EventInstanceEnd)
		if end.BaseCommit != fakeBaseSHA {
			t.Errorf("instance_end base_commit = %q, want %q — resolved at CLOSE because that is the "+
				"first moment the branch has stopped moving", end.BaseCommit, fakeBaseSHA)
		}
		if want := formulaFileDigest(t, fx.root, "offpath"); end.FormulaDigest != want {
			t.Errorf("instance_end formula_digest = %q, want the sha256 of the formula file %q — a run "+
				"whose instance_start rotated out of the retention window loses its identity otherwise",
				end.FormulaDigest, want)
		}
	})
}
