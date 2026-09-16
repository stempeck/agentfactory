package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// writeSemanticsFormula writes an arbitrary formula body to the STORE path the improvement loop
// edits, and returns its absolute path. writeFormulaFile next door always writes the same
// single-step body, which cannot express the thing this file measures — a gate appearing or
// disappearing, a step id being renamed, a directive being deleted.
func writeSemanticsFormula(t *testing.T, root, name, body string) string {
	t.Helper()
	dir := config.FormulasDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas: %v", err)
	}
	path := filepath.Join(dir, name+".formula.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	return path
}

// The step prose below is deliberately distinctive. The semantics lint reports COUNTS and the waste
// note reports step IDS; neither may put a title or a description on a peer's mailbox, and a
// substring nobody would write by accident is what makes that assertable.
const (
	semanticsProseA = "ZZPROSEALPHA"
	semanticsProseB = "ZZPROSEBRAVO"
)

func semanticsFormulaBefore() string {
	return fmt.Sprintf(`formula = "fx"

[[steps]]
id = "s1"
title = "Collect %s"
description = "Read it top to bottom, then capture docs/architecture/adr.md Verbatim."

[[steps]]
id = "s2"
title = "Gate A %s"
description = "Byte-for-byte check against docs/architecture/adr.md."
needs = ["s1"]

[[steps]]
id = "s3"
title = "Gate B review"
description = "RE-COPY the table from reports/summary.md."
needs = ["s2"]
`, semanticsProseA, semanticsProseB)
}

// TestImprovementInstructionCarriesEfficiencyClause is #678 K10's first half.
//
// The negative subtest is the one that proves anything. Every existing assertion on this
// instruction is an unanchored strings.Contains (done_improvement_test.go:130-211), so an
// implementation that appended the clause UNCONDITIONALLY would leave the whole suite green while
// violating the design's central promise — umbrella off ⇒ byte-identical to what shipped.
func TestImprovementInstructionCarriesEfficiencyClause(t *testing.T) {
	const clause = "This factory is optimising token efficiency"

	t.Run("umbrella on appends the clause", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		writeFormulaFile(t, root, "fx", true)

		instruction, _, ok := improvementInstruction(root, "Formula: fx", true)
		if !ok {
			t.Fatal("expected improvementInstruction to resolve")
		}
		if !strings.Contains(instruction, clause) {
			t.Errorf("umbrella on: instruction carries no efficiency clause:\n%s", instruction)
		}
		// #483's own wiring must survive the append, and the completion verb must still be named
		// exactly once: two would make the agent run the whole teardown twice.
		if got := strings.Count(instruction, "af improvement complete"); got != 1 {
			t.Errorf("af improvement complete named %d times, want 1:\n%s", got, instruction)
		}
		if strings.Contains(instruction, "%!") {
			t.Errorf("instruction carries an unsubstituted verb:\n%s", instruction)
		}
	})

	t.Run("umbrella off is byte-identical to the bare template", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		writeFormulaFile(t, root, "fx", true)

		instruction, f, ok := improvementInstruction(root, "Formula: fx", false)
		if !ok {
			t.Fatal("expected improvementInstruction to resolve")
		}
		want := fmt.Sprintf(improvementInstructionTemplate, f.Name, f.AbsPath, f.Name, f.Name)
		if instruction != want {
			t.Errorf("umbrella off must leave the instruction byte-identical to the bare template.\n"+
				"got  (%d bytes):\n%s\nwant (%d bytes):\n%s",
				len(instruction), instruction, len(want), want)
		}
	})

	t.Run("the clause is a compile-time constant", func(t *testing.T) {
		// A formatting verb is the ONLY way task-derived text could reach this string. There is
		// none, so there is none possible — which is the trust boundary the design says the
		// template's must keep.
		if strings.Contains(improvementEfficiencyClause, "%") {
			t.Errorf("the efficiency clause carries a formatting verb, reopening the template's "+
				"trust boundary:\n%s", improvementEfficiencyClause)
		}
	})
}

// TestImprovementMarkerRecordsTokenomicsState pins the marker field to the SAME resolver the
// instance_start record uses. A site that read only the toggle file would report a posture an
// operator had switched off in startup.json.
func TestImprovementMarkerRecordsTokenomicsState(t *testing.T) {
	cases := []struct {
		name    string
		gate    bool
		startup string
		want    string
	}{
		{"both halves on", true, `{"tokenomics":{"enabled":"on"}}`, telemetry.TokenomicsStateOn},
		{"toggle off", false, `{"tokenomics":{"enabled":"on"}}`, telemetry.TokenomicsStateOff},
		{"startup disables it", true, `{"tokenomics":{"enabled":"off"}}`, telemetry.TokenomicsStateOff},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("AF_ROLE", "alpha")
			root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
			writeFormulaFile(t, root, "fx", true)
			if tc.gate {
				if err := os.WriteFile(tokenomicsGateFile(root), []byte("on\n"), 0o644); err != nil {
					t.Fatalf("write tokenomics gate: %v", err)
				}
			}
			if err := os.WriteFile(config.StartupConfigPath(root), []byte(tc.startup+"\n"), 0o644); err != nil {
				t.Fatalf("write startup.json: %v", err)
			}

			fired, agent, _, reason := evaluateImprovementFire(root, root, "inst-1", "manager",
				"Formula: fx", false, tokenomicsState(root) == telemetry.TokenomicsStateOn)
			if !fired {
				t.Fatalf("expected fire, got reason=%q", reason)
			}
			m, err := readImprovementMarkerFile(improvementPendingFile(root, agent))
			if err != nil {
				t.Fatalf("read marker: %v", err)
			}
			if m.TokenomicsState != tc.want {
				t.Errorf("marker tokenomics_state = %q, want %q", m.TokenomicsState, tc.want)
			}
		})
	}
}

// fireThenEdit drives the REAL fire path so the pre-edit fingerprint is captured by production
// code, then overwrites the store formula the way a self-edit would. Returns the agent dir the
// completion verb runs against.
func fireThenEdit(t *testing.T, root, instance, before, after string) string {
	t.Helper()
	t.Setenv("AF_ROLE", "alpha")
	writeSemanticsFormula(t, root, "fx", before)

	fired, agent, _, reason := evaluateImprovementFire(root, root, instance, "manager",
		"Formula: fx", false, false)
	if !fired {
		t.Fatalf("expected fire, got reason=%q", reason)
	}
	if after != "" {
		writeSemanticsFormula(t, root, "fx", after)
	}
	return config.AgentDir(root, agent)
}

// TestImprovementCompleteSemanticsLint is #678 K10's guarantee that a self-edit never silently
// changes what a formula DECLARES (design-doc.md:210).
//
// It compares against a fingerprint captured at FIRE time, which is the only honest baseline: the
// store copy IS the edit target, so by completion the "before" has been overwritten, and
// install_formulas/ is the promotion source rather than the pre-edit body (improvement.go:34-39).
func TestImprovementCompleteSemanticsLint(t *testing.T) {
	t.Run("a dropped gate is reported as a count delta", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		after := strings.Replace(semanticsFormulaBefore(), `title = "Gate B review"`, `title = "Review"`, 1)
		agentDir := fireThenEdit(t, root, "i-gate", semanticsFormulaBefore(), after)
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		if !strings.Contains(*body, "gate steps 2 -> 1") {
			t.Errorf("the verdict does not report the dropped gate:\n%s", *body)
		}
		if !strings.Contains(*body, "CHANGED") {
			t.Errorf("a dropped gate must read as CHANGED:\n%s", *body)
		}
		// The subject is a fixed four-part label an asserter prefix-matches
		// (improvement_test.go:635). Everything this phase adds belongs in the body.
		if !strings.HasPrefix(*subject, "IMPROVEMENT: fx — ") {
			t.Errorf("subject shape changed: %q", *subject)
		}
		for _, prose := range []string{semanticsProseA, semanticsProseB, "Verbatim", "RE-COPY"} {
			if strings.Contains(*subject+"\n"+*body, prose) {
				t.Errorf("formula prose %q reached the outcome mail; the lint reports COUNTS:\n%s",
					prose, *body)
			}
		}
	})

	t.Run("a prose-only edit reads as preserved", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		after := strings.Replace(semanticsFormulaBefore(), `title = "Collect `, `title = "Gather `, 1)
		agentDir := fireThenEdit(t, root, "i-prose", semanticsFormulaBefore(), after)
		_, _, _, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		if !strings.Contains(*body, "Semantics lint: preserved") {
			t.Errorf("a prose-only edit must read as preserved:\n%s", *body)
		}
	})

	t.Run("a rename at equal cardinality is still caught", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		after := strings.Replace(semanticsFormulaBefore(), `id = "s3"`, `id = "s9"`, 1)
		agentDir := fireThenEdit(t, root, "i-rename", semanticsFormulaBefore(), after)
		_, _, _, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		if !strings.Contains(*body, "step ids CHANGED") {
			t.Errorf("a rename that leaves the count at 3 must still be caught; a count alone "+
				"cannot see it:\n%s", *body)
		}
	})

	t.Run("a deleted directive and artifact path are counted", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		after := strings.Replace(semanticsFormulaBefore(),
			"RE-COPY the table from reports/summary.md.", "Summarise the table.", 1)
		agentDir := fireThenEdit(t, root, "i-directive", semanticsFormulaBefore(), after)
		_, _, _, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		if !strings.Contains(*body, "directives 4 -> 3") {
			t.Errorf("the deleted protected directive is not counted:\n%s", *body)
		}
		if !strings.Contains(*body, "artifact paths 2 -> 1") {
			t.Errorf("the dropped artifact path is not counted:\n%s", *body)
		}
	})

	t.Run("a marker with no baseline stays silent and still completes", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		// stageImprovementCompletion writes the pre-#678 marker shape: no fingerprint at all,
		// which is exactly what a marker left by an older binary looks like.
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-nobaseline")
		teardowns, _, _, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		if strings.Contains(*body, "Semantics lint") {
			t.Errorf("with no baseline the lint must invent nothing:\n%s", *body)
		}
		if *teardowns != 0 {
			t.Errorf("terminate_on_complete was false; teardown ran %d times", *teardowns)
		}
	})
}

// TestImprovementCompleteWasteNote is the third half of K10's verdict: which steps this run spent
// the most generating, and whether the self-edit went anywhere near them.
func TestImprovementCompleteWasteNote(t *testing.T) {
	t.Run("names the costliest steps and marks each touched or untouched", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		gateOn(t, root)
		seedGenerationStepEnd(t, root, "alpha", "i-waste", "s1", 900)
		seedGenerationStepEnd(t, root, "alpha", "i-waste", "s2", 5000)
		seedGenerationStepEnd(t, root, "alpha", "i-waste", "s3", 3000)

		after := strings.Replace(semanticsFormulaBefore(),
			"Byte-for-byte check against docs/architecture/adr.md.",
			"Byte-for-byte check against docs/architecture/adr.md. Then stop.", 1)
		agentDir := fireThenEdit(t, root, "i-waste", semanticsFormulaBefore(), after)
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		if !strings.Contains(*body, "s2 5000 touched") {
			t.Errorf("the costliest step must be named first and marked touched:\n%s", *body)
		}
		if !strings.Contains(*body, "s3 3000 untouched") {
			t.Errorf("an untouched step must be marked untouched:\n%s", *body)
		}
		if strings.Contains(*subject, "s2") {
			t.Errorf("the waste ranking belongs in the body; the subject is a fixed label: %q", *subject)
		}
		for _, prose := range []string{semanticsProseA, semanticsProseB} {
			if strings.Contains(*body, prose) {
				t.Errorf("only step IDS may be named, never titles or descriptions:\n%s", *body)
			}
		}
	})

	t.Run("no generation figures means no ranking", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		agentDir := fireThenEdit(t, root, "i-nofigures", semanticsFormulaBefore(), "")
		_, _, _, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		if strings.Contains(*body, "Waste ranking") {
			t.Errorf("with nothing measured the ranking must be silent:\n%s", *body)
		}
	})
}

// seedGenerationStepEnd writes one closed step carrying generation figures, which is what the waste
// ranking reads. StepLabel — the formula's own stable step id — is the join key; StepID is a
// per-instance bead id and would name nothing a fingerprint could be matched against.
func seedGenerationStepEnd(t *testing.T, root, agent, instance, label string, outTokens int64) {
	t.Helper()
	ev := telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
		TS: "2026-08-15T10:10:00.000Z", Agent: agent, Formula: "fx",
		InstanceID: instance, StepID: "bead-" + label, StepLabel: label,
		SessionID: "sess-a", Verb: "done", Status: telemetry.StatusClosed,
		OutTokens: i64p(outTokens),
	}
	if err := telemetry.AppendEvent(config.TelemetryDir(root), ev); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
}

// TestImprovementSessionMeasured is Gap 7(i): the improvement loop's OWN spend is measured, so the
// experiment can price the thing it uses to run the experiment.
func TestImprovementSessionMeasured(t *testing.T) {
	t.Run("gate on writes one step_end labelled improvement", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		gateOn(t, root)

		const sessionID = "sess-improve"
		firedAt := time.Now().Add(-time.Hour).UTC()
		agentDir := stageMeasuredImprovement(t, root, "alpha", "i-self", sessionID, firedAt)
		seedTranscript(t, agentDir, sessionID, transcriptLine(
			firedAt.Add(30*time.Minute).Format(telemetry.TimestampLayout),
			"msg_A", "text", strings.Repeat("z", 40), 1000, 100, 10, 1, 60))
		stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}

		rec := onlyImprovementRecord(t, root, "alpha")
		if rec.Event != telemetry.EventStepEnd {
			t.Errorf("event = %q, want %q", rec.Event, telemetry.EventStepEnd)
		}
		if rec.Formula != "fx" || rec.InstanceID != "i-self" {
			t.Errorf("record does not carry the run's identity: formula=%q instance=%q",
				rec.Formula, rec.InstanceID)
		}
		if rec.OutTokens == nil || *rec.OutTokens != 100 {
			t.Errorf("out_tokens = %v, want 100 — the derivation was never wired", rec.OutTokens)
		}
		if rec.ThinkTokens == nil || *rec.ThinkTokens != 60 {
			t.Errorf("think_tokens = %v, want 60", rec.ThinkTokens)
		}
		// The learned digest accumulates per-STEP medians a later run is judged against. An
		// improvement session is not a formula step; folding it in would move every median by a
		// figure no step produced.
		digest := telemetry.LearnedDigestPath(config.TelemetryDir(root), "fx")
		if _, err := os.Stat(digest); err == nil {
			t.Errorf("the improvement record updated the learned digest at %s", digest)
		}
	})

	t.Run("gate off writes nothing and still completes", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})

		const sessionID = "sess-quiet"
		agentDir := stageMeasuredImprovement(t, root, "alpha", "i-quiet", sessionID,
			time.Now().Add(-time.Hour).UTC())
		stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		records, _, err := telemetry.ReadEvents(config.TelemetryDir(root),
			telemetry.Filter{Agent: "alpha"})
		if err == nil && len(records) != 0 {
			t.Errorf("gate off must record nothing; got %d records", len(records))
		}
	})

	t.Run("the record is not evidence about itself", func(t *testing.T) {
		t.Setenv(claudeConfigDirEnv, t.TempDir())
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		gateOn(t, root)
		seedContextRichStepPair(t, root, "alpha", "i-selfref")

		agentDir := stageMeasuredImprovement(t, root, "alpha", "i-selfref", "sess-x",
			time.Now().Add(-time.Hour).UTC())
		_, _, _, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		// improvementContextNote reads this run's own report in-process. A self-measurement record
		// appended before the body was composed would be counted as evidence about itself.
		if !strings.Contains(*body, "Context review: 1 step in this run's report") {
			t.Errorf("the context review counted the verb's own record:\n%s", *body)
		}
	})
}

// stageMeasuredImprovement stages a completion whose fired_at is an hour in the past, so a seeded
// transcript line can fall inside the [fired_at, now) window the generation derivation joins on. A
// marker stamped "now" leaves a window microseconds wide that nothing could land in.
func stageMeasuredImprovement(t *testing.T, root, agent, instance, sessionID string, firedAt time.Time) string {
	t.Helper()
	absFormula := writeFormulaFile(t, root, "fx", true)
	sum, err := formulaSHA256(absFormula)
	if err != nil {
		t.Fatalf("formulaSHA256: %v", err)
	}
	m := improvementMarker{
		InstanceID:    instance,
		Formula:       "fx",
		FormulaPath:   absFormula,
		Caller:        "manager",
		FormulaSHA256: sum,
		FiredAt:       firedAt.Format(time.RFC3339),
	}
	if err := writeImprovementMarker(root, agent, m); err != nil {
		t.Fatalf("writeImprovementMarker: %v", err)
	}
	agentDir := config.AgentDir(root, agent)
	writeRuntimeFile(t, agentDir, "session_id", sessionID)
	return agentDir
}

func onlyImprovementRecord(t *testing.T, root, agent string) telemetry.StepEvent {
	t.Helper()
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		t.Fatalf("ReadEvents: %v", err)
	}
	var found []telemetry.StepEvent
	for _, r := range records {
		if r.StepLabel == improvementStepLabel {
			found = append(found, r)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one record with step_label %q, got %d (of %d records)",
			improvementStepLabel, len(found), len(records))
	}
	return found[0]
}
