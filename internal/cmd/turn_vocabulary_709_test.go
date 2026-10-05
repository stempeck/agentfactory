//go:build !integration

package cmd

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// Issue #709 fault 3 and the pins of the launch-line lift. Unlike effort_lifecycle_709_test.go this
// file names symbols the lift introduces, so it cannot compile on the base tree.

func standingEffortLine(level string) string {
	return "- effort: reduce_effort — " + effectEffortReduced + " (effort_level=" + level + ")\n"
}

func turnInterventionsWith(t *testing.T, root, agent, since string, standing launchEffort) string {
	t.Helper()
	var out bytes.Buffer
	if err := runTurnInterventionsCore(&out, root, agent, since, standing); err != nil {
		t.Fatalf("runTurnInterventionsCore returned an error; the verb exits 0 on every outcome: %v", err)
	}
	return out.String()
}

// TestNonReductionEffortRecordsAreNotWordedAsReductions is fault 3: every effort record was rendered
// with the reduction clause, so an `observe` or a `handoff` naming the NEXT session's level told the
// grader the graded session had been reduced.
func TestNonReductionEffortRecordsAreNotWordedAsReductions(t *testing.T) {
	const agent = "manager"
	root := setupTestFactoryForDone(t, agent)
	agentDir := config.AgentDir(root, agent)
	for _, r := range []struct {
		mechanism tokenomics.Mechanism
		action    string
		level     string
	}{
		{tokenomics.MechanismEffort, telemetry.ActionObserve, ""},
		{tokenomics.MechanismEffort, telemetry.ActionHandoff, "low"},
		{tokenomics.MechanismInterview, telemetry.ActionObserve, ""},
		{tokenomics.MechanismInterview, telemetry.ActionHandoff, ""},
		{tokenomics.MechanismDispatch, telemetry.ActionObserve, ""},
		{tokenomics.MechanismBudget, telemetry.ActionHandoff, ""},
		{tokenomics.MechanismEffort, telemetry.ActionReduceEffort, "low"},
	} {
		if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventIntervention, TS: turnInWindow, Agent: agent,
			Verb: "done", Formula: "offpath", StepID: "bd-709-step-1",
			Mechanism: string(r.mechanism), Action: r.action, EffortLevel: r.level,
		}); err != nil {
			t.Fatalf("AppendEvent %s/%s: %v", r.mechanism, r.action, err)
		}
	}

	plantLaunchEffort(t, "", "", "", "")
	if got := turnInterventionsFrom(t, root, agentDir, agent, turnBoundary); got != "" {
		t.Errorf("records that do not describe the graded session were rendered to its grader:\n%q", got)
	}

	plantLaunchEffort(t, "medium", string(tokenomics.ObjectiveEfficiency), "step-1", "offpath")
	got := turnInterventionsFrom(t, root, agentDir, agent, turnBoundary)
	if want := standingEffortLine("medium"); got != want {
		t.Errorf("with the session attested at medium:\ngot  %q\nwant %q — one standing line carrying the "+
			"launch line's level, never a record's", got, want)
	}
}

// vocabularyFromSource reads a closed vocabulary's string constants out of the source file that
// declares it. A hand-written list cannot fail on the constant someone forgot to add to it.
func vocabularyFromSource(t *testing.T, rel, prefix string) []string {
	t.Helper()
	path := filepath.Join(findModuleRoot(t), rel)
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, 0)
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	var values []string
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs := spec.(*ast.ValueSpec)
			for i, name := range vs.Names {
				if !strings.HasPrefix(name.Name, prefix) || i >= len(vs.Values) {
					continue
				}
				if lit, ok := vs.Values[i].(*ast.BasicLit); ok && lit.Kind == token.STRING {
					v, _ := strconv.Unquote(lit.Value)
					values = append(values, v)
				}
			}
		}
	}
	sort.Strings(values)
	return values
}

// TestInterventionEffectsCoverTheClosedVocabularies classifies every (mechanism, action) pair by what
// the renderer actually does with an in-window record of it, and compares against an explicit table.
func TestInterventionEffectsCoverTheClosedVocabularies(t *testing.T) {
	mechanisms := vocabularyFromSource(t, "internal/tokenomics/policy.go", "Mechanism")
	actions := vocabularyFromSource(t, "internal/telemetry/event.go", "Action")
	if len(mechanisms) != 6 || len(actions) != 5 {
		t.Fatalf("vocabularies are %d mechanisms × %d actions, want 6 × 5; a new constant needs a row "+
			"in this table before it can reach the grader: %v × %v", len(mechanisms), len(actions), mechanisms, actions)
	}

	clause := map[interventionKey]string{
		{string(tokenomics.MechanismBudget), telemetry.ActionAdvise}:    effectBudget,
		{string(tokenomics.MechanismThrift), telemetry.ActionAdvise}:    effectThrift,
		{string(tokenomics.MechanismDispatch), telemetry.ActionAdvise}:  effectDispatch,
		{string(tokenomics.MechanismDispatch), telemetry.ActionRefuse}:  effectDispatch,
		{string(tokenomics.MechanismEffort), telemetry.ActionAdvise}:    effectEffortAdvised,
		{string(tokenomics.MechanismInterview), telemetry.ActionAdvise}: effectInterview,
	}
	omitted := func(k interventionKey) bool {
		return k.Action == telemetry.ActionObserve || k.Action == telemetry.ActionHandoff ||
			(k.Mechanism == string(tokenomics.MechanismEffort) && k.Action == telemetry.ActionReduceEffort)
	}

	var nOmitted, nClause, nBare int
	for _, m := range mechanisms {
		for _, a := range actions {
			k := interventionKey{m, a}
			root := t.TempDir()
			plantIntervention(t, root, "agent-a", turnInWindow, tokenomics.Mechanism(m), a)
			got := turnInterventionsWith(t, root, "agent-a", turnBoundary, launchEffort{})

			label := "- " + m + ": " + a
			switch want, hasClause := clause[k]; {
			case omitted(k):
				nOmitted++
				if got != "" {
					t.Errorf("%s/%s does not describe the graded session and must not be rendered: %q", m, a, got)
				}
			case hasClause:
				nClause++
				if !strings.HasPrefix(got, label+" — "+want) {
					t.Errorf("%s/%s rendered %q, want its own clause %q", m, a, got, want)
				}
			default:
				nBare++
				if !strings.HasPrefix(got, label+" [step ") {
					t.Errorf("%s/%s rendered %q, want the bare label %q with no clause", m, a, got, label)
				}
			}
			if strings.Contains(got, effectEffortReduced) {
				t.Errorf("%s/%s rendered the standing-reduction clause, which only the launch attestation may carry: %q", m, a, got)
			}
		}
	}
	if nOmitted != 13 || nClause != 6 || nBare != 11 {
		t.Errorf("pairs: %d omitted, %d clause, %d bare; want 13, 6, 11", nOmitted, nClause, nBare)
	}
	if len(interventionEffects) != len(clause) {
		t.Errorf("interventionEffects has %d entries, want exactly the %d clause pairs", len(interventionEffects), len(clause))
	}
}

// TestWithEffortLevelExportsAttestationOnEveryPath: the running session's attestation is whatever its
// launch line said, so every path must say something — a path that exported nothing would leave a
// respawned pane carrying the previous session's attestation.
func TestWithEffortLevelExportsAttestationOnEveryPath(t *testing.T) {
	const nextStep = "step-2"
	attestation := []string{config.EnvEffortObjective, config.EnvEffortStepLabel, config.EnvEffortFormula}

	setup := func(t *testing.T, arm, seed bool) lifecycleFixture {
		t.Helper()
		fx, _, _ := primedFixture(t, roomyOccupancyPct)
		declareWindow(t, fx.root, roomyWindowTokens)
		if arm {
			armEfficiency(t, fx.root, nil)
		} else {
			armAdvisoryPolicy(t, fx.root, advisoryMarginPct, advisoryMinRuns, map[string]string{"effort": "off"})
		}
		hookFormulaName(t, fx.workDir, "offpath")
		if seed {
			model, _ := resolveRecordModel(fx.root, fx.workDir, fx.agent, "")
			seedEfficiency(t, fx.root, "offpath", nextStep, model, reducibleAggregate())
		}
		return fx
	}

	for _, tc := range []struct {
		name      string
		arm, seed bool
		declared  string
	}{
		{"arm off", false, true, "high"},
		{"nothing selected", true, false, "high"},
		{"selection equals the declared level", true, true, "low"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fx := setup(t, tc.arm, tc.seed)
			got := withEffortLevel(fx.root, fx.workDir, launchEnv(tc.declared), nextStep, "")
			for _, key := range attestation {
				if !modelEnvHasKey(got, key) || modelEnvValue(got, key) != "" {
					t.Errorf("%s must be exported empty on a non-selecting launch: %v", key, got)
				}
			}
			if lvl := effortLevelIn(got); lvl != tc.declared {
				t.Errorf("%s = %q, want the declared %q untouched (#707)", config.EnvEffortLevel, lvl, tc.declared)
			}
		})
	}

	t.Run("selecting", func(t *testing.T) {
		fx := setup(t, true, true)
		got := withEffortLevel(fx.root, fx.workDir, launchEnv(""), nextStep, "")
		for key, want := range map[string]string{
			config.EnvEffortLevel:     "medium",
			config.EnvEffortObjective: string(tokenomics.ObjectiveEfficiency),
			config.EnvEffortStepLabel: nextStep,
			// Resolved from last_closed_step inside withEffortLevel: the leg passed "".
			config.EnvEffortFormula: "offpath",
		} {
			if v := modelEnvValue(got, key); v != want {
				t.Errorf("%s = %q, want %q: %v", key, v, want, got)
			}
		}
		if _, err := os.Stat(filepath.Join(fx.workDir, ".runtime", "effort_level")); !os.IsNotExist(err) {
			t.Errorf("a selecting launch wrote .runtime/effort_level (stat err=%v)", err)
		}
	})
}

func TestReadLaunchEffortIsZeroWithoutObjective(t *testing.T) {
	for _, tc := range []struct {
		name, level, objective string
		want                   launchEffort
	}{
		{"no objective", "medium", "", launchEffort{}},
		{"no level", "", "efficiency", launchEffort{}},
		{"level the host cannot read", "bogus", "efficiency", launchEffort{}},
		{"attested", "medium", "efficiency", launchEffort{Level: "medium", Objective: "efficiency", StepLabel: "step-1", Formula: "offpath"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plantLaunchEffort(t, tc.level, tc.objective, "step-1", "offpath")
			if got := readLaunchEffort(); got != tc.want {
				t.Errorf("readLaunchEffort() = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// TestNoProfileRespawnClearsAttestation: a launch that resolves no profile never reaches
// withEffortLevel, yet it may reuse a session a selecting launch attested.
func TestNoProfileRespawnClearsAttestation(t *testing.T) {
	attestation := []string{"AF_EFFORT_OBJECTIVE", "AF_EFFORT_STEP_LABEL", "AF_EFFORT_FORMULA"}
	breakModels := func(t *testing.T, root string) {
		t.Helper()
		if err := os.WriteFile(config.ModelsConfigPath(root), []byte("{not json"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Run("respawn", func(t *testing.T) {
		root := setupTestFactoryForDone(t, "manager")
		breakModels(t, root)
		line := respawnLaunchLine(t, root, config.AgentDir(root, "manager"))
		for _, key := range attestation {
			if !strings.Contains(line, " "+key+"=''") {
				t.Errorf("the no-profile respawn line does not clear %s: %s", key, line)
			}
		}
	})

	t.Run("start", func(t *testing.T) {
		const wtID = "wt-709noprof"
		root := setupTestFactoryForDone(t, "manager")
		breakModels(t, root)
		wtPath := filepath.Join(root, ".agentfactory", "worktrees", wtID)
		if err := os.MkdirAll(config.AgentDir(wtPath, "manager"), 0o755); err != nil {
			t.Fatal(err)
		}
		fake, _ := setupHermeticSessions(t)

		cmd, out := launchCmd(t)
		if err := launchAgentSession(cmd, root, "manager", wtPath, wtID, "", false); err != nil {
			t.Fatalf("af sling launch: %v\n%s", err, out)
		}
		assertSessionAttestsNothing(t, fake.ops)
	})
}

// TestStandingLineSurvivesUnknownBoundary: the gate substitutes "unknown" when it finds no boundary.
// That suppresses the per-turn records, but the standing reduction does not depend on the window.
func TestStandingLineSurvivesUnknownBoundary(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	agentDir := config.AgentDir(root, "manager")
	plantIntervention(t, root, "manager", turnInWindow, tokenomics.MechanismDispatch, telemetry.ActionAdvise)

	for _, since := range []string{"unknown", "", "not-a-timestamp"} {
		t.Run("since="+strconv.Quote(since), func(t *testing.T) {
			plantLaunchEffort(t, "", "", "", "")
			if got := turnInterventionsFrom(t, root, agentDir, "manager", since); got != "" {
				t.Errorf("no attestation and no boundary: got %q, want nothing", got)
			}

			plantLaunchEffort(t, "medium", string(tokenomics.ObjectiveEfficiency), "step-1", "offpath")
			if got, want := turnInterventionsFrom(t, root, agentDir, "manager", since), standingEffortLine("medium"); got != want {
				t.Errorf("attested session, no boundary:\ngot  %q\nwant %q", got, want)
			}
		})
	}
}
