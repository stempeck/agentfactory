package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// TestBandReport is #668 K10's second half. "Within baselines" was the design's phrase for the
// thing Phase 7 has to conclude, and until this exists it is an opinion: somebody eyeballs a median
// against a run and says whether it looks right. The verdict is computed here, from the digest the
// factory learned, against tolerances stated as named constants — and recomputed on every read,
// never stored, for deriveStepContext's reason (telemetry_context_read.go:24-26).
func TestBandReport(t *testing.T) {
	const formula = "offpath"

	// seedBand writes one learned digest and one closed step, and returns the factory root. The
	// digest is written through the SHIPPED writer path so the fixture cannot describe a file the
	// factory would never produce.
	seedBand := func(t *testing.T, agg tokenomics.Aggregate, end telemetry.StepEvent) string {
		t.Helper()
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedTelemetryGate(t, root)

		dir := config.TelemetryDir(root)
		if agg.Runs > 0 {
			d := tokenomics.NewDigest()
			d.Put(tokenomics.DigestKey{Formula: formula, StepID: end.StepLabel, Model: end.Model}, agg)
			if err := os.MkdirAll(telemetry.LearnedDigestDir(dir), 0o755); err != nil {
				t.Fatalf("mkdir digest dir: %v", err)
			}
			if err := tokenomics.SaveDigest(telemetry.LearnedDigestPath(dir, formula), d); err != nil {
				t.Fatalf("SaveDigest: %v", err)
			}
		}
		if err := telemetry.AppendEvent(dir, end); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		return root
	}

	closedStep := func(peak *int64) telemetry.StepEvent {
		return telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-08-31T09:15:06.388Z", Agent: "manager", Formula: formula,
			InstanceID: "af-668-1", StepID: "s-2", StepLabel: "s-2", StepSeq: 2, StepTitle: "Phase 2 — implement",
			Model: "lmstudio", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "done", VerbMS: 12,
			DurationMS: 6388, Status: telemetry.StatusClosed, SessionID: "sess-a",
			PeakCtxTokens: peak,
		}
	}

	// A median of 100,000 with the shipped ±20% gives a band of 80,000..120,000.
	baseline := tokenomics.Aggregate{
		Runs:                4,
		MedianPeakCtxTokens: 100_000,
		UpdatedAt:           "2026-08-31T09:00:00Z",
	}

	bandOf := func(t *testing.T, root string) bandReportJSON {
		t.Helper()
		enableTelemetryJSON(t)
		out, err := runTelemetryJSON(t, "band")
		if err != nil {
			t.Fatalf("band --json: %v", err)
		}
		var dto bandReportJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		return dto
	}

	figureOf := func(t *testing.T, dto bandReportJSON, name string) bandFigureJSON {
		t.Helper()
		if len(dto.Rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1", len(dto.Rows))
		}
		for _, f := range dto.Rows[0].Figures {
			if f.Name == name {
				return f
			}
		}
		t.Fatalf("no %q figure on the row; the band judges it and a report that omits it cannot be "+
			"joined on", name)
		return bandFigureJSON{}
	}

	t.Run("a run inside its learned band is within baselines", func(t *testing.T) {
		root := seedBand(t, baseline, closedStep(i64p(105_000)))

		dto := bandOf(t, root)
		f := figureOf(t, dto, tokenomics.BandFigurePeakCtxTokens)
		if f.Verdict != tokenomics.BandWithin {
			t.Errorf("peak_ctx_tokens verdict = %q, want %q for 105000 inside 80000..120000",
				f.Verdict, tokenomics.BandWithin)
		}
		if dto.Rows[0].Verdict != tokenomics.BandWithin {
			t.Errorf("row verdict = %q, want %q", dto.Rows[0].Verdict, tokenomics.BandWithin)
		}
	})

	t.Run("a run outside its learned band is outside baselines, with the band stated", func(t *testing.T) {
		root := seedBand(t, baseline, closedStep(i64p(151_000)))

		dto := bandOf(t, root)
		f := figureOf(t, dto, tokenomics.BandFigurePeakCtxTokens)
		if f.Verdict != tokenomics.BandOutside {
			t.Errorf("peak_ctx_tokens verdict = %q, want %q for 151000 against 80000..120000",
				f.Verdict, tokenomics.BandOutside)
		}
		// The arithmetic travels with the answer. A verdict whose median and tolerance are not
		// reported is the unfalsifiable claim this deliverable replaces.
		if f.Median != 100_000 || f.TolerancePct != tokenomics.BandPeakTolerancePct {
			t.Errorf("figure reports median %d ±%d%%, want 100000 ±%d%%",
				f.Median, f.TolerancePct, tokenomics.BandPeakTolerancePct)
		}
		if f.Low != 80_000 || f.High != 120_000 {
			t.Errorf("band = %d..%d, want 80000..120000", f.Low, f.High)
		}
	})

	t.Run("a step with no learned history is not judged", func(t *testing.T) {
		root := seedBand(t, tokenomics.Aggregate{}, closedStep(i64p(105_000)))

		dto := bandOf(t, root)
		f := figureOf(t, dto, tokenomics.BandFigurePeakCtxTokens)
		if f.Verdict != tokenomics.BandNoBaseline {
			t.Errorf("verdict = %q, want %q; with nothing learned there is no band, and reporting a "+
				"pass or a failure would both be inventions", f.Verdict, tokenomics.BandNoBaseline)
		}
		if dto.Formulas != 0 || dto.Aggregates != 0 {
			t.Errorf("formulas = %d, aggregates = %d with no digest written, want 0 and 0",
				dto.Formulas, dto.Aggregates)
		}
	})

	t.Run("a learned step with no observed figure is unmeasurable, not no_baseline", func(t *testing.T) {
		root := seedBand(t, baseline, closedStep(nil))

		dto := bandOf(t, root)
		f := figureOf(t, dto, tokenomics.BandFigurePeakCtxTokens)
		if f.Verdict != tokenomics.BandUnmeasurable {
			t.Errorf("verdict = %q, want %q — a baseline exists and the run recorded nothing to "+
				"compare, which is a different fact from having no baseline",
				f.Verdict, tokenomics.BandUnmeasurable)
		}
		if f.Observed != nil {
			t.Errorf("observed = %d, want null; a step nobody measured must not read as one that "+
				"peaked at nothing", *f.Observed)
		}
	})

	t.Run("the verdict is recomputed on every read and never stored", func(t *testing.T) {
		root := seedBand(t, baseline, closedStep(i64p(105_000)))
		_ = bandOf(t, root)

		records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: "manager"})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		raw, err := json.Marshal(records)
		if err != nil {
			t.Fatalf("marshal records: %v", err)
		}
		for _, verdict := range []string{
			tokenomics.BandWithin, tokenomics.BandOutside,
			tokenomics.BandNoBaseline, tokenomics.BandUnmeasurable,
		} {
			if strings.Contains(string(raw), verdict) {
				t.Errorf("a record carries the band verdict %q. A figure cannot lie and a verdict "+
					"computed under one version of the rules can — and this one is judged against a "+
					"median that MOVES as the digest learns", verdict)
			}
		}
	})

	t.Run("escalated-step counts are per instance and honest about their zero", func(t *testing.T) {
		root := seedBand(t, baseline, closedStep(i64p(105_000)))

		t.Run("no record", func(t *testing.T) {
			dto := bandOf(t, root)
			if len(dto.Escalations) != 0 {
				t.Fatalf("escalations = %v with no intervention record", dto.Escalations)
			}
			if strings.TrimSpace(dto.EscalationsZeroBecause) == "" {
				t.Error("the report reads 0 escalated steps with no reason, which is indistinguishable " +
					"from a mechanism that ran and found nothing; escalate defaults off and K14 is deferred")
			}
		})

		t.Run("records across two instances", func(t *testing.T) {
			for _, ev := range []telemetry.StepEvent{
				{
					V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
					TS: "2026-08-31T09:20:00.000Z", Agent: "manager", Formula: formula,
					InstanceID: "af-668-1", StepID: "s-3",
					Mechanism: string(tokenomics.MechanismEscalate), Action: telemetry.ActionHandoff,
				},
				{
					V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
					TS: "2026-08-31T09:21:00.000Z", Agent: "manager", Formula: formula,
					InstanceID: "af-668-1", StepID: "s-4",
					Mechanism: string(tokenomics.MechanismEscalate), Action: telemetry.ActionHandoff,
				},
				{
					V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
					TS: "2026-08-31T09:22:00.000Z", Agent: "manager", Formula: formula,
					InstanceID: "af-668-2", StepID: "s-5",
					Mechanism: string(tokenomics.MechanismEscalate), Action: telemetry.ActionHandoff,
				},
				// A different mechanism, so the count is proven to be a filter rather than a total.
				{
					V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
					TS: "2026-08-31T09:23:00.000Z", Agent: "manager", Formula: formula,
					InstanceID: "af-668-2", StepID: "s-6",
					Mechanism: string(tokenomics.MechanismBudget), Action: telemetry.ActionAdvise,
				},
				// s-4 again. The field is Steps and both surfaces render "escalated steps", so a step
				// that escalated twice counts once — counting records would answer a different
				// question from the one the report prints.
				{
					V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
					TS: "2026-08-31T09:24:00.000Z", Agent: "manager", Formula: formula,
					InstanceID: "af-668-1", StepID: "s-4",
					Mechanism: string(tokenomics.MechanismEscalate), Action: telemetry.ActionHandoff,
				},
			} {
				if err := telemetry.AppendEvent(config.TelemetryDir(root), ev); err != nil {
					t.Fatalf("AppendEvent: %v", err)
				}
			}

			dto := bandOf(t, root)
			want := map[string]int{"af-668-1": 2, "af-668-2": 1}
			if len(dto.Escalations) != len(want) {
				t.Fatalf("escalations = %v, want one entry per instance %v", dto.Escalations, want)
			}
			for _, e := range dto.Escalations {
				if want[e.InstanceID] != e.Steps {
					t.Errorf("instance %s escalated %d steps, want %d", e.InstanceID, e.Steps, want[e.InstanceID])
				}
			}
			if strings.TrimSpace(dto.EscalationsZeroBecause) != "" {
				t.Errorf("the report explains a zero it does not have: %q", dto.EscalationsZeroBecause)
			}
		})
	})

	t.Run("a digest that outlived its raw records is still counted", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		seedTelemetryGate(t, root)

		// The digest alone: no step records at all, which is the state past the rotation horizon.
		// Nothing else in the tree enumerates this directory, so if the reader composed a path per
		// known formula instead of listing it, this file would be invisible and the flywheel property
		// MergeDigests exists for would be unobservable.
		dir := config.TelemetryDir(root)
		d := tokenomics.NewDigest()
		d.Put(tokenomics.DigestKey{Formula: formula, StepID: "s-2", Model: "lmstudio"}, baseline)
		if err := os.MkdirAll(telemetry.LearnedDigestDir(dir), 0o755); err != nil {
			t.Fatalf("mkdir digest dir: %v", err)
		}
		if err := tokenomics.SaveDigest(telemetry.LearnedDigestPath(dir, formula), d); err != nil {
			t.Fatalf("SaveDigest: %v", err)
		}

		dto := bandOf(t, root)
		if dto.Formulas != 1 || dto.Aggregates != 1 {
			t.Errorf("formulas = %d, aggregates = %d, want 1 and 1 — the learned side is enumerated "+
				"from the digest DIRECTORY so a cache that outlived its records still counts",
				dto.Formulas, dto.Aggregates)
		}
		if len(dto.Rows) != 0 {
			t.Errorf("len(rows) = %d with no step records, want 0", len(dto.Rows))
		}
	})

	t.Run("only a closed step is a run", func(t *testing.T) {
		// An opening record carries occupancy figures of its own. Judging one against a peak median
		// would compare a level to a maximum — and every such row would read "outside baselines" on a
		// perfectly ordinary run, from a record the band has no business reading.
		root := seedBand(t, baseline, closedStep(i64p(105_000)))
		start := closedStep(i64p(400_000))
		start.Event = telemetry.EventStepStart
		start.TS = "2026-08-31T09:15:00.000Z"
		start.Status = ""
		if err := telemetry.AppendEvent(config.TelemetryDir(root), start); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		dto := bandOf(t, root)
		if len(dto.Rows) != 1 {
			t.Fatalf("len(rows) = %d, want 1; the step_start record is not a run", len(dto.Rows))
		}
		if got := *figureOf(t, dto, tokenomics.BandFigurePeakCtxTokens).Observed; got != 105_000 {
			t.Errorf("observed = %d, want 105000 — the row judged is the closed step's, not the "+
				"opening record's 400000", got)
		}
	})

	t.Run("a duration nobody recorded is not a duration of zero", func(t *testing.T) {
		// The learned side omits a zero duration when it folds (rebuild.go's durationOf), so the
		// observed side has to read it the same way. A step whose duration was never recorded would
		// otherwise arrive as 0, land outside every median it never entered, and drag the row's
		// worst-first verdict to outside_baselines on a figure nobody measured.
		agg := baseline
		agg.MedianDurationMS = 600_000
		end := closedStep(i64p(105_000))
		end.DurationMS = 0
		root := seedBand(t, agg, end)

		dto := bandOf(t, root)
		f := figureOf(t, dto, tokenomics.BandFigureDurationMS)
		if f.Observed != nil {
			t.Errorf("observed = %d, want null for an unrecorded duration", *f.Observed)
		}
		if f.Verdict != tokenomics.BandUnmeasurable {
			t.Errorf("duration verdict = %q, want %q", f.Verdict, tokenomics.BandUnmeasurable)
		}
		if dto.Rows[0].Verdict != tokenomics.BandWithin {
			t.Errorf("row verdict = %q, want %q; the peak was measured and fits, and an unmeasured "+
				"figure must not out-vote it", dto.Rows[0].Verdict, tokenomics.BandWithin)
		}
	})

	t.Run("the operator's configured min_runs reaches the verdict", func(t *testing.T) {
		// min_runs is an advertised field, and its own comment says a report that did not state its
		// clamp would be two reports under one name. The shipped default is 2 and the fallback is 1,
		// so a fixture at either cannot tell a resolved clamp from a hardcoded one: 5 can.
		root := seedBand(t, baseline, closedStep(i64p(105_000)))
		armAdvisoryPolicy(t, root, 10, 5, nil)

		dto := bandOf(t, root)
		if dto.MinRuns != 5 {
			t.Fatalf("min_runs = %d, want 5 from startup.json's learned_min_runs", dto.MinRuns)
		}
		f := figureOf(t, dto, tokenomics.BandFigurePeakCtxTokens)
		if f.Verdict != tokenomics.BandNoBaseline {
			t.Errorf("verdict = %q, want %q: the aggregate rests on 4 runs and the operator asked for "+
				"5, so there is not yet a baseline they would trust", f.Verdict, tokenomics.BandNoBaseline)
		}
	})

	t.Run("a stray json file does not become a learned formula", func(t *testing.T) {
		// The digest enumerator recovers the formula from the filename and checks it by re-composing
		// the writer's own path. Without that check any .json dropped in the directory inflates the
		// coverage figure — on this verb AND on af tokenomics status, which reuses the enumerator.
		root := seedBand(t, baseline, closedStep(i64p(105_000)))
		strayDir := telemetry.LearnedDigestDir(config.TelemetryDir(root))
		for _, name := range []string{"notes.txt", "README.md"} {
			if err := os.WriteFile(filepath.Join(strayDir, name), []byte("{}"), 0o644); err != nil {
				t.Fatalf("write stray file: %v", err)
			}
		}

		dto := bandOf(t, root)
		if dto.Formulas != 1 {
			t.Errorf("formulas = %d, want 1; two files in the digest directory are not digests this "+
				"factory wrote, and counting them reports learning that never happened", dto.Formulas)
		}
		if dto.State != telemetryStateOK {
			t.Errorf("state = %q, want %q; a file the writer would never have produced is not a "+
				"corrupt digest", dto.State, telemetryStateOK)
		}
	})

	t.Run("a narrowed scan does not make a claim about the whole factory", func(t *testing.T) {
		root := seedBand(t, baseline, closedStep(i64p(105_000)))
		if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventIntervention,
			TS: "2026-08-31T09:16:00Z", Agent: "manager", Formula: formula,
			InstanceID: "af-668-1", StepID: "s-2",
			Mechanism: string(tokenomics.MechanismEscalate), Action: telemetry.ActionAdvise,
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}

		// After enableTelemetryJSON, not before: it calls resetReportFlags, which clears --instance.
		enableTelemetryJSON(t)
		if err := telemetryCmd.Flags().Set("instance", "af-668-nothing-here"); err != nil {
			t.Fatalf("set --instance: %v", err)
		}
		out, err := runTelemetryJSON(t, "band")
		if err != nil {
			t.Fatalf("band --json: %v", err)
		}
		var dto bandReportJSON
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}

		if len(dto.Escalations) != 0 {
			t.Fatalf("escalations = %v under a filter that matches nothing", dto.Escalations)
		}
		if dto.EscalationsZeroBecause == bandEscalationsZeroBecause {
			t.Error("a filtered scan printed the factory-wide reason. The factory DOES hold an " +
				"escalate record; this scan simply did not open it, and explaining the empty list " +
				"with the deferred-mechanism story answers a question the caller did not ask")
		}
		if dto.EscalationsZeroBecause == "" {
			t.Error("the empty list says nothing about why it is empty")
		}
	})

	t.Run("the human rendering states the band it judged against", func(t *testing.T) {
		seedBand(t, baseline, closedStep(i64p(151_000)))
		resetReportFlags(t)

		out := captureStdout(t, func() {
			if err := runTelemetry(telemetryCmd, []string{"band"}); err != nil {
				t.Fatalf("band: %v", err)
			}
		})
		for _, want := range []string{tokenomics.BandOutside, "100000", "80000..120000"} {
			if !strings.Contains(out, want) {
				t.Errorf("the human band report does not show %q:\n%s", want, out)
			}
		}
		if strings.Contains(out, `"v":1`) {
			t.Errorf("the human path emitted JSON; --json must default to false:\n%s", out)
		}
	})

	// #678 K9. The direction leg is a SEPARATE answer from the verdict, and it is the whole point
	// of the band under an efficiency objective: `outside_baselines` alone reads identically for a
	// run that got cheaper and one that got more expensive, which is the difference the objective
	// exists to see. Both surfaces are pinned because the verdict word is unchanged on each, so a
	// direction that never reached them would leave every other band assertion green.
	t.Run("the direction leg says which side of the band a run fell on", func(t *testing.T) {
		for _, tc := range []struct {
			name      string
			peak      int64
			direction string
			arrow     string
			verdict   string
		}{
			{"a cheaper run is below", 60_000, tokenomics.BandDirectionBelow, "↓", tokenomics.BandOutside},
			{"a costlier run is above", 151_000, tokenomics.BandDirectionAbove, "↑", tokenomics.BandOutside},
			{"a run inside the band is within", 100_000, tokenomics.BandDirectionWithin, "", tokenomics.BandWithin},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root := seedBand(t, baseline, closedStep(i64p(tc.peak)))

				figure := bandFigureNamed(t, bandOf(t, root), tokenomics.BandFigurePeakCtxTokens)
				if figure.Verdict != tc.verdict {
					t.Fatalf("verdict = %q, want %q", figure.Verdict, tc.verdict)
				}
				if figure.Direction != tc.direction {
					t.Errorf("direction = %q, want %q — the verdict word is the same on both sides "+
						"of the band, so without this leg a cheaper run and a costlier one are one "+
						"answer", figure.Direction, tc.direction)
				}

				resetReportFlags(t)
				t.Chdir(root)
				out := captureStdout(t, func() {
					if err := runTelemetry(telemetryCmd, []string{"band"}); err != nil {
						t.Fatalf("band: %v", err)
					}
				})
				if !strings.Contains(out, tc.verdict) {
					t.Errorf("the human band report dropped the verdict word %q:\n%s", tc.verdict, out)
				}
				if tc.arrow == "" {
					return
				}
				want := tc.arrow + " " + tc.direction
				if !strings.Contains(out, want) {
					t.Errorf("the human band report does not render %q:\n%s", want, out)
				}
			})
		}
	})
}

func bandFigureNamed(t *testing.T, dto bandReportJSON, name string) bandFigureJSON {
	t.Helper()
	for _, row := range dto.Rows {
		for _, f := range row.Figures {
			if f.Name == name {
				return f
			}
		}
	}
	t.Fatalf("no %q figure in %+v", name, dto.Rows)
	return bandFigureJSON{}
}
