package cmd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// #622 Phase 4 / HIGH-4. These tests pin the two directions of the same fact: an improvement
// session on a factory whose telemetry gate is off has nothing measured to reason about, and
// today the operator learns that only after the agent-hour is spent. The warning moves the
// loudness UPSTREAM of the wasted session; the verdict remedy is the downstream half, for the
// session that already ran.

const telemetryRemedy = "af telemetry on"

// TestImprovementOn_TelemetryGateOff_Warns covers HIGH-4's first half.
//
// The --agent row is the load-bearing one. runImprovement returns from the --agent branch
// (improvement.go:346-348), so a warning emitted anywhere after that branch passes the factory
// row and silently misses the exact form the design calls out. Only a site between the argument
// switch and that branch covers both, and only this row proves it.
func TestImprovementOn_TelemetryGateOff_Warns(t *testing.T) {
	for _, tt := range []struct {
		name       string
		gateOn     bool
		arg        string
		agent      string
		wantRemedy bool
	}{
		{"factory form, gate off", false, "on", "", true},
		{"factory form, gate on", true, "on", "", false},
		{"agent form, gate off", false, "on", "alpha", true},
		{"agent form, gate on", true, "on", "alpha", false},
		{"off is never warned", false, "off", "", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": false})
			t.Chdir(root)
			if tt.gateOn {
				if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
					t.Fatalf("write telemetry gate: %v", err)
				}
			}
			if tt.agent != "" {
				setAgentFlag(t, tt.agent)
			}

			stdout, stderr := captureOutErr(t, func() {
				if err := runImprovement(improvementCmd, []string{tt.arg}); err != nil {
					t.Fatalf("runImprovement %q: %v", tt.arg, err)
				}
			})

			if got := strings.Contains(stderr, telemetryRemedy); got != tt.wantRemedy {
				t.Errorf("stderr carries %q = %v, want %v\nstdout:\n%s\nstderr:\n%s",
					telemetryRemedy, got, tt.wantRemedy, stdout, stderr)
			}
			if tt.wantRemedy && !strings.Contains(strings.ToLower(stderr), "warning") {
				t.Errorf("the advisory must read as a warning:\n%s", stderr)
			}
			// Warnings go to stderr in this package without exception (up.go:671-673,
			// improvement.go:561); stdout stays the surface a script parses.
			if strings.Contains(stdout, telemetryRemedy) {
				t.Errorf("the remedy reached stdout; warnings belong on stderr:\n%s", stdout)
			}

			// The warning is ADDITIVE: it must never replace the work the verb was asked to do.
			if tt.agent != "" {
				cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
				if err != nil {
					t.Fatalf("reload agents.json: %v", err)
				}
				if !cfg.Agents[tt.agent].ContinuousImprovement {
					t.Error("the --agent write did not happen; the warning must not short-circuit it")
				}
			} else {
				data, err := os.ReadFile(improvementHookFile(root))
				if err != nil || strings.TrimSpace(string(data)) != tt.arg {
					t.Errorf("hook file = %q (err %v), want %q", string(data), err, tt.arg)
				}
			}
		})
	}
}

// seedContextlessStepPair appends a start/end pair carrying NO context data whatsoever — not even
// the echoed bound, the shape done.go leaves only when LoadStartupConfig itself errors. Rows exist
// and every context pointer is nil, so an implementation that only asks len(rows) == 0 fails here.
func seedContextlessStepPair(t *testing.T, root, agent, instance string) {
	t.Helper()
	seedStepPair(t, root, agent, instance, false, nil)
}

// seedContextRichStepPair appends a start/end pair carrying occupancy and consumption figures —
// the measured shape, in which the verdict must stay silent about telemetry.
func seedContextRichStepPair(t *testing.T, root, agent, instance string) {
	t.Helper()
	seedStepPair(t, root, agent, instance, true, nil)
}

// seedBoundOnlyStepPair appends the shape a gate-ON factory with no statusline occupancy actually
// writes: done.go:206 echoes the configured bound onto every close whether or not anything was
// observed. seedContextlessStepPair's all-zero close is the rarer shape — done.go only leaves the
// bound at 0 when LoadStartupConfig itself errors — so this row, not that one, is the "telemetry
// on / occupancy absent" case the USING table names.
func seedBoundOnlyStepPair(t *testing.T, root, agent, instance string) {
	t.Helper()
	seedStepPair(t, root, agent, instance, false, func(_, end *telemetry.StepEvent) {
		end.CtxBoundTokens = 200000
	})
}

// seedRecycledStepPair appends a pair whose two records name DIFFERENT sessions and carry no
// figures. deriveConsumption reads that as "unattributable" (telemetry_context_read.go:196-197)
// with all six raw pointers still nil — evidence that lives outside them.
func seedRecycledStepPair(t *testing.T, root, agent, instance string) {
	t.Helper()
	seedStepPair(t, root, agent, instance, false, func(_, end *telemetry.StepEvent) {
		end.SessionID = "sess-b"
	})
}

// seedOverBoundStepPair appends a measured pair whose close sits above its own bound: 120000
// occupied against a 100000 budget, so over_occupancy derives true rather than null.
func seedOverBoundStepPair(t *testing.T, root, agent, instance string) {
	t.Helper()
	seedStepPair(t, root, agent, instance, true, func(_, end *telemetry.StepEvent) {
		end.CtxBoundTokens = 100000
	})
}

func seedStepPair(t *testing.T, root, agent, instance string, withContext bool, mutate func(start, end *telemetry.StepEvent)) {
	t.Helper()
	start := telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
		TS: "2026-08-15T10:00:00.000Z", Agent: agent, Formula: "fx",
		InstanceID: instance, StepID: "s1", StepTitle: "Step one", SessionID: "sess-a",
		Verb: "prime", VerbMS: 11,
	}
	end := telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
		TS: "2026-08-15T10:10:00.000Z", Agent: agent, Formula: "fx",
		InstanceID: instance, StepID: "s1", StepTitle: "Step one", SessionID: "sess-a",
		Verb: "done", VerbMS: 22, DurationMS: 600000, Status: telemetry.StatusClosed,
	}
	if withContext {
		// The write side records occupancy all-or-none (step_context.go), so the observation
		// stamp rides with the figures.
		start.CtxTokensUsed, start.CtxTokensTotal, start.CumTokens = i64p(40000), i64p(200000), i64p(9000)
		start.CtxUsedPct, start.CtxObservedAt = f64p(20), "2026-08-15T09:59:58.000Z"
		end.CtxTokensUsed, end.CtxTokensTotal, end.CumTokens = i64p(120000), i64p(200000), i64p(31000)
		end.CtxUsedPct, end.CtxObservedAt = f64p(60), "2026-08-15T10:09:58.000Z"
		end.CtxTokensStart, end.CtxBoundTokens = i64p(40000), 200000
	}
	if mutate != nil {
		mutate(&start, &end)
	}
	for _, ev := range []telemetry.StepEvent{start, end} {
		if err := telemetry.AppendEvent(config.TelemetryDir(root), ev); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
}

// assertRemedy pins the remedy to the BODY and out of the SUBJECT. Asserting on subject+body
// concatenated would let an implementation that appended the remedy to the subject — where the
// four-part label is prefix-matched (improvement_test.go:635) — pass every case below.
func assertRemedy(t *testing.T, want bool, why, subject, body string) {
	t.Helper()
	if got := strings.Contains(body, telemetryRemedy); got != want {
		t.Errorf("%s: body carries %q = %v, want %v\nsubject: %s\nbody: %s",
			why, telemetryRemedy, got, want, subject, body)
	}
	if strings.Contains(subject, telemetryRemedy) {
		t.Errorf("the remedy belongs in the body; the subject is a fixed label:\n%s", subject)
	}
}

func stageImprovementCompletion(t *testing.T, root, agent, instance string) string {
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
		FiredAt:       time.Now().UTC().Format(time.RFC3339),
	}
	if err := writeImprovementMarker(root, agent, m); err != nil {
		t.Fatalf("writeImprovementMarker: %v", err)
	}
	return config.AgentDir(root, agent)
}

// TestImprovementCompleteVerdictNamesTelemetryRemedy covers HIGH-4's second half (G6): the
// completion verb obtains the report outcome for marker.InstanceID and carries the remedy into
// the verdict only when that report returned no context data.
//
// The negative subtest is the one that proves anything. Every pre-existing completion test runs
// on a factory with no telemetry directory, so a presence-only assertion would pass against an
// implementation that hard-codes the sentence unconditionally.
func TestImprovementCompleteVerdictNamesTelemetryRemedy(t *testing.T) {
	t.Run("no records at all", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-none")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, true, "an unmeasured run on a gate-off factory", *subject, *body)
	})

	t.Run("records carry context figures", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedContextRichStepPair(t, root, "alpha", "i-ctx")
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-ctx")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, false, "a measured run", *subject, *body)
		// The pre-existing verdict vocabulary must survive the addition.
		if !strings.Contains(*subject, "unchanged") || !strings.Contains(*body, "validation passed") {
			t.Errorf("existing verdict words lost\nsubject: %s\nbody: %s", *subject, *body)
		}
	})

	t.Run("rows exist but every context figure is nil", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedContextlessStepPair(t, root, "alpha", "i-blank")
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-blank")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, true, "rows without figures are not measurement", *subject, *body)
	})

	t.Run("a bound echoed from config keeps the wrong remedy unsaid", func(t *testing.T) {
		// Telemetry is ON here — the bound only reaches the record because the gate let done.go
		// write it. Naming `af telemetry on` would instruct the operator to enable what is already
		// enabled, and the spec fixes that one remedy string, so silence is the honest output. This
		// pins the trade-off as chosen rather than accidental: what is missing on this factory is
		// statusline occupancy, which this sentence cannot ask for.
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		// The comment above is only true when the gate is actually on — and in production a
		// bound-only record exists ONLY because done.go wrote the bound under a gate-on close.
		if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
			t.Fatalf("write telemetry gate: %v", err)
		}
		seedBoundOnlyStepPair(t, root, "alpha", "i-bound")
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-bound")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, false, "the gate is on for this run", *subject, *body)
		// G-1: a bound-only run measured NOTHING; the verdict must reach the gate-on silence branch,
		// never "Context review: … none flagged" — which is indistinguishable from a run that WAS
		// measured and cleared. Silence beats a false all-clear.
		if strings.Contains(*body, "Context review") {
			t.Errorf("a measured-nothing (bound-only) run must be silent, not report a context review\nbody: %s", *body)
		}
	})

	t.Run("a mid-step recycle is evidence, not absence", func(t *testing.T) {
		// Two present, different session ids: consumption_state is "unattributable" while every raw
		// figure stays nil. The skill's selection table ranks that the WORST class, so this run has
		// something to classify and the verdict must not say it had nothing.
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedRecycledStepPair(t, root, "alpha", "i-recycle")
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-recycle")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, false, "an unattributable step is context data", *subject, *body)
		if !strings.Contains(*body, "unattributable 1") {
			t.Errorf("the verdict must name the class it found\nbody: %s", *body)
		}
	})

	t.Run("an INTERRUPTED step is evidence, not absence", func(t *testing.T) {
		// The C10 join marks a start-only row INTERRUPTED from the funnel log alone
		// (telemetry_json.go:397-405); none of the six pointers is set on such a row. It is the
		// other signal that lives outside them, and the same worst class.
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		openedAt := time.Now().UTC().Add(-10 * time.Minute)
		if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: openedAt.Format(telemetry.TimestampLayout), Agent: "alpha", Formula: "fx",
			InstanceID: "i-int", StepID: "s1", StepTitle: "Step one", SessionID: "sess-a",
			Verb: "prime", VerbMS: 11,
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		if err := appendRecoveryLog(root, recoveryLogEntry{
			V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(2 * time.Minute)),
			Agent: "alpha", Trigger: triggerContextExhaustion, ObservedPct: 93.5, ThresholdPct: 85,
			SessionID: "sess-a", InstanceID: "i-int", ResumedStep: "s1",
			Attempt: 1, Outcome: outcomeRespawned,
		}); err != nil {
			t.Fatalf("appendRecoveryLog: %v", err)
		}
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-int")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, false, "a recycled-mid-step row is the loudest evidence there is", *subject, *body)
		if !strings.Contains(*body, statusInterrupted+" 1") {
			t.Errorf("the verdict must name the class it found\nbody: %s", *body)
		}
	})

	t.Run("the gate is on, so the remedy is never the advice", func(t *testing.T) {
		// Nothing was recorded for this run, but the gate the remedy names is already on — the
		// factory is armed and something else (statusline occupancy) is what is missing. Naming
		// `af telemetry on` here would be a confident instruction to enable what is enabled.
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
			t.Fatalf("write telemetry gate: %v", err)
		}
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-armed")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, false, "the gate this remedy names is already on", *subject, *body)
		if strings.Contains(*body, "Context review") {
			t.Errorf("there was nothing to review; the verdict must not claim one\nbody: %s", *body)
		}
	})

	t.Run("the verdict names the evidence it reviewed", func(t *testing.T) {
		// Manual-verification item 5: the outcome mail names the context evidence it acted on, or
		// states that there was none. This is the first branch — measured, nothing over its bound.
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedContextRichStepPair(t, root, "alpha", "i-named")
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-named")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, false, "a measured run", *subject, *body)
		if !strings.Contains(*body, "Context review: 1 step in this run's report, none flagged") {
			t.Errorf("the verdict does not name what it reviewed\nbody: %s", *body)
		}
	})

	t.Run("the verdict names an over-bound step by its class", func(t *testing.T) {
		// The other branch of the same checklist item, and the one that pins the nil-guard: a
		// derived verdict is a *bool, and an implementation that read null as false would report
		// this step — and every unmeasured one — as inside its budget.
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedOverBoundStepPair(t, root, "alpha", "i-over")
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-over")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, false, "a measured run", *subject, *body)
		if !strings.Contains(*body, "over_occupancy 1") {
			t.Errorf("the verdict does not name the class it flagged\nbody: %s", *body)
		}
	})

	t.Run("another instance's figures do not count as this run's", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedContextRichStepPair(t, root, "alpha", "i-other")
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-mine")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, true, "the report must be filtered to marker.InstanceID", *subject, *body)
	})

	t.Run("an empty instance id selects nothing, not everything", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedContextRichStepPair(t, root, "alpha", "i-other")
		agentDir := stageImprovementCompletion(t, root, "alpha", "")
		_, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("runImprovementCompleteCore: %v", err)
		}
		assertRemedy(t, true, "an empty instance filter is a no-op on the read path, so it is never evidence",
			*subject, *body)
	})

	t.Run("a report read error stays fail-open", func(t *testing.T) {
		// No agents.json: the report's agent enumeration errors. The completion verb is
		// fail-open toward teardown (improvement.go:481-485) and must stay so.
		root := setupTestFactoryForImprovement(t, nil)
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-err")
		teardowns, _, subject, body := stubTeardownAndMail(t)

		if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
			t.Fatalf("a report read error must not fail the completion verb: %v", err)
		}
		if *subject == "" {
			t.Error("no verdict was mailed")
		}
		assertRemedy(t, true, "an unreadable report is not evidence of measurement", *subject, *body)
		if *teardowns != 0 {
			t.Errorf("terminate_on_complete was false; teardowns = %d", *teardowns)
		}
	})

	t.Run("the remedy reaches the printed surface", func(t *testing.T) {
		// The mail goes to an agent's mailbox; without the print the operator never sees it
		// (the reason improvement.go:528-531 prints subject and body at all).
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		agentDir := stageImprovementCompletion(t, root, "alpha", "i-print")
		stubTeardownAndMail(t)

		out := captureStdout(t, func() {
			if err := runImprovementCompleteCore(agentDir, root, false, ""); err != nil {
				t.Fatalf("runImprovementCompleteCore: %v", err)
			}
		})
		if !strings.Contains(out, telemetryRemedy) {
			t.Errorf("the remedy never reached stdout:\n%s", out)
		}
	})
}

// TestImprovementContextEvidence_BoundOnlyIsNotMeasured pins G-1 at the mechanism: the echoed
// config bound (done.go:206 writes it on every gate-on close) is not a measurement, so a row that
// carries ONLY the bound must count as zero measured rows — regardless of the telemetry gate. This
// is the unconditional half; the mail-level silence is pinned in the subtest above.
func TestImprovementContextEvidence_BoundOnlyIsNotMeasured(t *testing.T) {
	now := time.Date(2026, 8, 15, 10, 20, 0, 0, time.UTC)

	t.Run("a bound-only row counts as zero measured rows", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedBoundOnlyStepPair(t, root, "alpha", "i-bo")
		rows, flagged := improvementContextEvidence(root, "i-bo", now)
		if rows != 0 {
			t.Errorf("bound-only row: rows = %d, want 0 (the echoed bound is not a measurement)", rows)
		}
		if len(flagged) != 0 {
			t.Errorf("bound-only row: flagged = %v, want none", flagged)
		}
	})

	t.Run("a genuinely-measured row still counts", func(t *testing.T) {
		root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
		seedContextRichStepPair(t, root, "alpha", "i-rich")
		rows, _ := improvementContextEvidence(root, "i-rich", now)
		if rows != 1 {
			t.Errorf("context-rich row: rows = %d, want 1 (occupancy WAS measured)", rows)
		}
	})
}
