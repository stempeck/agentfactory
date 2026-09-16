package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// #668 K10, the disk half. internal/tokenomics/band.go decides; this file is the only part that
// knows where a digest lives, what a record looks like, and how to render an answer.
//
// No field below is omitempty, anywhere, and every absent figure is a pointer rendered as an
// explicit null — the report family's rule (telemetry_json.go:43-45), for the report family's
// reason: a band report whose keys came and went with the state of the factory could not be parsed
// by one consumer.

const (
	bandDefaultMinRuns = 1

	// The reason the escalation list reads empty on every honest factory today. It is carried WITH
	// the zero rather than instead of it, the idiom tokenomics status uses for the same problem: a
	// bare 0 beside "escalated steps" is indistinguishable from a mechanism that ran and found
	// nothing, and this one cannot run at all.
	bandEscalationsZeroBecause = "the escalate mechanism is off in this release and defaults off " +
		"even under an enabled umbrella, so nothing writes an escalate intervention record; this " +
		"count reads those records, and there is no other derivation"
)

type bandFigureJSON struct {
	Name string `json:"name"`
	// Observed is null when the run recorded no such figure, which is why the verdict beside it can
	// be "unmeasurable" while a baseline exists.
	Observed     *int64 `json:"observed"`
	Median       int64  `json:"median"`
	TolerancePct int    `json:"tolerance_pct"`
	Low          int64  `json:"low"`
	High         int64  `json:"high"`
	Verdict      string `json:"verdict"`
	// Direction is which side of the band the run fell, and it is the empty string wherever there
	// was no band to fall outside of. Empty rather than omitted, this file's rule: absence is a
	// difference in value, never in the key set.
	Direction string `json:"direction"`
}

type bandRowJSON struct {
	Agent      string `json:"agent"`
	Formula    string `json:"formula"`
	Step       string `json:"step"`
	Model      string `json:"model"`
	InstanceID string `json:"instance_id"`
	// Runs is how many recorded runs the median rests on, so a reader can see a verdict withheld for
	// thin evidence rather than guess at it.
	Runs int `json:"runs"`
	// #679 F7: the observed run's own two authoring-waste signals, carried beside the judged figures
	// so K10 (improve-agent Phase 1.5b) can rank steps by them from this surface as design-doc.md:209
	// intends. These are the observed run's values, not the learned medians the figures compare
	// against. Pointer, never omitempty: null is "this run recorded none", not zero.
	RepeatReads *int64           `json:"repeat_reads"`
	Sessions    *int64           `json:"sessions"`
	Verdict     string           `json:"verdict"`
	Figures     []bandFigureJSON `json:"figures"`
}

type bandEscalationJSON struct {
	InstanceID string `json:"instance_id"`
	Steps      int    `json:"steps"`
}

type bandReportJSON struct {
	V     int    `json:"v"`
	State string `json:"state"`
	// MinRuns is the resolved Policy.LearnedMinRuns the verdicts were computed under. A band report
	// that did not say which clamp it applied would be two different reports under one name.
	MinRuns int `json:"min_runs"`
	// Formulas and Aggregates describe the learned side: how many digest FILES were enumerated and
	// how many rows they hold between them. They are counted from the directory, so a digest whose
	// raw records have rotated away is still counted — which is the property that makes the cache
	// worth keeping.
	Formulas    int                  `json:"formulas"`
	Aggregates  int                  `json:"aggregates"`
	Rows        []bandRowJSON        `json:"rows"`
	Escalations []bandEscalationJSON `json:"escalations"`
	// EscalationsZeroBecause is non-empty exactly when the list above is empty.
	EscalationsZeroBecause string                 `json:"escalations_zero_because"`
	Stats                  telemetryReadStatsJSON `json:"stats"`
}

// loadLearnedDigests enumerates the learned-digest DIRECTORY.
//
// Nothing else in the tree does. Both writers compose a path for one named formula
// (telemetry.LearnedDigestPath), because both already know which formula they are writing; a read
// surface knows none, so the directory listing is the only way to ask what the factory has learned.
//
// The formula name is recovered by trimming the extension and then CHECKED by re-composing the
// path through the writer's own helper. A name that does not round-trip is not a digest this
// factory wrote — a stray file, or one whose name the writer would have refused — and counting it
// would let an unrelated .json inflate the coverage figure.
//
// An unreadable directory is an empty answer, not an error: a factory that has closed no step has
// no digest directory at all, and that is a normal state rather than a failure of this verb.
func loadLearnedDigests(telemetryDir string) (map[string]tokenomics.Digest, int) {
	digests := map[string]tokenomics.Digest{}

	dir := telemetry.LearnedDigestDir(telemetryDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return digests, 0
	}

	unreadable := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		formula := name[:len(name)-len(filepath.Ext(name))]
		path := filepath.Join(dir, name)
		if formula == "" || telemetry.LearnedDigestPath(telemetryDir, formula) != path {
			continue
		}
		d, err := tokenomics.LoadDigest(path)
		if err != nil {
			unreadable++
			continue
		}
		digests[formula] = d
	}
	return digests, unreadable
}

// bandObservation lifts one closed step onto the shape the pure judge compares.
//
// The duration rule is rebuild.go's durationOf, restated rather than shared because that helper is
// unexported: a zero duration is omitted on the wire, so an unmeasured step and an instantaneous
// one are already the same bytes. The observed side has to read it the way the learned side folded
// it, or a step whose duration nobody recorded would be judged against a median it never entered.
//
// The key's StepID leg is the record's StepLabel — the stable formula step id samplesFrom folds the
// learned side under — not its per-instance StepID bead id. A run carries a fresh bead id every time,
// so keying the observation on it would miss the label-keyed baseline and report no_baseline on a
// step the factory has a median for. A record with no StepLabel keys on the empty string and simply
// finds no baseline, which is the honest "never seen" answer rather than a silent bead-id fallback.
func bandObservation(r telemetry.StepEvent) tokenomics.Observation {
	obs := tokenomics.Observation{
		Key:            tokenomics.DigestKey{Formula: r.Formula, StepID: r.StepLabel, Model: r.Model},
		PeakCtxTokens:  r.PeakCtxTokens,
		CumTokensDelta: r.CumTokensDelta,
		OutTokens:      r.OutTokens,
		SubagentTokens: r.SubagentTokens,
		ThinkTokens:    r.ThinkTokens,
	}
	if r.DurationMS > 0 {
		d := int64(r.DurationMS)
		obs.DurationMS = &d
	}
	return obs
}

// bandReportDTO computes the whole report. It reads the store and the digest directory and derives
// every verdict; it stores nothing and stamps nothing.
func bandReportDTO(factoryRoot, agentFilter, instanceFilter string) (bandReportJSON, error) {
	out := bandReportJSON{
		V:           telemetry.SchemaVersion,
		State:       telemetryStateOK,
		MinRuns:     bandMinRuns(factoryRoot),
		Rows:        []bandRowJSON{},
		Escalations: []bandEscalationJSON{},
	}

	agents, err := telemetryReportAgents(factoryRoot, agentFilter)
	if err != nil {
		return out, err
	}

	dir := config.TelemetryDir(factoryRoot)
	digests, unreadableDigests := loadLearnedDigests(dir)
	out.Formulas = len(digests)
	for _, d := range digests {
		out.Aggregates += tokenomics.Coverage(d)
	}

	escalatedSteps := map[string]map[string]bool{}
	var stats telemetry.ReadStats
	for _, agent := range agents {
		records, agentStats, readErr := telemetry.ReadEvents(dir,
			telemetry.Filter{Agent: agent, InstanceID: instanceFilter})
		if readErr != nil {
			return out, fmt.Errorf("reading %s step records: %w", agent, readErr)
		}
		stats.Malformed += agentStats.Malformed
		stats.Dropped += agentStats.Dropped
		stats.DroppedUnexported += agentStats.DroppedUnexported

		spans := telemetry.SessionSpans(records)
		for _, r := range records {
			// Grouped by instance_id and counted from the intervention records themselves, because
			// the record schema carries no escalation marker to join on. fidelity.go's `escalated`
			// field is the fidelity gate's own latch and is not this quantity.
			//
			// DISTINCT steps, not records: the field is named Steps and both surfaces render
			// "escalated steps", so a step that escalated twice must count once or the report answers
			// a different question from the one it prints.
			if r.Event == telemetry.EventIntervention {
				if r.Mechanism == string(tokenomics.MechanismEscalate) {
					if escalatedSteps[r.InstanceID] == nil {
						escalatedSteps[r.InstanceID] = map[string]bool{}
					}
					escalatedSteps[r.InstanceID][r.StepID] = true
				}
				continue
			}
			// Only a closed step is a run, samplesFrom's rule: an opening record carries occupancy
			// figures of its own, and judging one against a peak would compare a level to a maximum.
			if r.Event != telemetry.EventStepEnd || r.Formula == "" {
				continue
			}
			row := tokenomics.JudgeBand(digests[r.Formula], bandObservation(r), out.MinRuns)
			bandRow := bandRowJSON{
				Agent:       agent,
				Formula:     r.Formula,
				Step:        telemetryStepLabel(r),
				Model:       r.Model,
				InstanceID:  r.InstanceID,
				Runs:        row.Runs,
				RepeatReads: r.RepeatReads,
				Verdict:     row.Verdict,
				Figures:     bandFiguresJSON(row.Figures),
			}
			if n, ok := spans[telemetry.StepRunKey{InstanceID: r.InstanceID, StepID: r.StepID}]; ok {
				sessions := int64(n)
				bandRow.Sessions = &sessions
			}
			out.Rows = append(out.Rows, bandRow)
		}
	}

	instances := make([]string, 0, len(escalatedSteps))
	for id := range escalatedSteps {
		instances = append(instances, id)
	}
	sort.Strings(instances)
	for _, id := range instances {
		out.Escalations = append(out.Escalations, bandEscalationJSON{InstanceID: id, Steps: len(escalatedSteps[id])})
	}
	// The factory-wide reason is a claim about the whole tree, so it is only made when the whole tree
	// was scanned. Under a filter the empty list says nothing about the factory — another agent or
	// another instance may hold records this scan never opened — and printing the deferred-mechanism
	// explanation there would answer a question the caller did not ask.
	if len(out.Escalations) == 0 {
		out.EscalationsZeroBecause = bandEscalationsZeroBecause
		if agentFilter != "" || instanceFilter != "" {
			out.EscalationsZeroBecause = "no escalation record matched this filter; the scan was " +
				"narrowed, so this is not a statement about the rest of the factory"
		}
	}

	out.Stats = telemetryReadStatsJSON{
		Malformed:         stats.Malformed,
		Dropped:           stats.Dropped,
		DroppedUnexported: stats.DroppedUnexported,
	}
	// An unreadable digest file degrades the answer without emptying it: the rows it would have
	// judged fall back to no_baseline, which looks exactly like a step nobody has run before.
	if stats.Malformed > 0 || stats.Dropped > 0 || unreadableDigests > 0 {
		out.State = telemetryStateDegraded
	}
	return out, nil
}

func bandFiguresJSON(figures []tokenomics.BandFigure) []bandFigureJSON {
	out := make([]bandFigureJSON, 0, len(figures))
	for _, f := range figures {
		out = append(out, bandFigureJSON{
			Name:         f.Name,
			Observed:     f.Observed,
			Median:       f.Median,
			TolerancePct: f.TolerancePct,
			Low:          f.Low,
			High:         f.High,
			Verdict:      f.Verdict,
			Direction:    f.Direction,
		})
	}
	return out
}

// bandMinRuns resolves the clamp the verdicts are computed under. A factory whose startup.json does
// not load still gets a report: the band is a read over records that already exist, and the honest
// fallback is the loosest clamp — one run is a baseline of one, reported as such by Runs.
//
// The umbrella argument is a literal true, and it is not an assertion that the umbrella is on. This
// is a read over records already on disk, so it must answer the same on a factory that has since
// switched tokenomics off; ResolvePolicy takes LearnedMinRuns from ClampMinRuns independently of the
// umbrella (policy.go), and passing the real conjunction here would only make the verdicts depend on
// a switch that has nothing to do with what was recorded.
func bandMinRuns(factoryRoot string) int {
	startup, err := config.LoadStartupConfig(factoryRoot)
	if err != nil {
		return bandDefaultMinRuns
	}
	return tokenomics.ResolvePolicy(true, startup.Tokenomics).LearnedMinRuns
}

// runTelemetryBand is the human rendering. Like the report table it is deliberately NOT gated on
// the telemetry switch: records already on disk stay readable after recording is switched off.
func runTelemetryBand(cmd *cobra.Command, factoryRoot string) error {
	agentFilter, _ := cmd.Flags().GetString("agent")
	instanceFilter, _ := cmd.Flags().GetString("instance")

	dto, err := bandReportDTO(factoryRoot, agentFilter, instanceFilter)
	if err != nil {
		return err
	}

	fmt.Printf("learned baselines: %d aggregates across %d formula digests (min runs: %d)\n",
		dto.Aggregates, dto.Formulas, dto.MinRuns)
	if len(dto.Rows) == 0 {
		fmt.Println("no closed steps to judge")
	}
	for _, row := range dto.Rows {
		fmt.Printf("%s  %s / %s  [%s]  %s (runs: %d)\n",
			row.Agent, row.Formula, row.Step, row.Model, row.Verdict, row.Runs)
		for _, f := range row.Figures {
			fmt.Printf("  %-18s %-18s %s\n", f.Name, bandObservedDisplay(f.Observed), bandFigureDisplay(f))
		}
	}

	if len(dto.Escalations) == 0 {
		fmt.Printf("escalated steps: 0 (%s)\n", dto.EscalationsZeroBecause)
	} else {
		fmt.Println("escalated steps:")
		for _, e := range dto.Escalations {
			fmt.Printf("  %s: %d\n", e.InstanceID, e.Steps)
		}
	}
	if dto.Stats.Malformed > 0 {
		fmt.Printf("skipped %d unparseable record lines\n", dto.Stats.Malformed)
	}
	return nil
}

// The dash is the table's spelling of absence and never crosses to the JSON surface, where the same
// nil is an explicit null (telemetry_json.go:405-410).
func bandObservedDisplay(v *int64) string {
	if v == nil {
		return "-"
	}
	return fmt.Sprintf("%d", *v)
}

// bandFigureDisplay prints the arithmetic beside the answer. A verdict with no band under it is the
// unfalsifiable claim this whole deliverable replaces.
//
// The direction is APPENDED to the line the occupancy story already prints, never folded into the
// verdict word: on a generation figure "outside baselines" alone reads the same for a run that
// halved its output and one that doubled it, and the arrow is the only thing that separates them.
func bandFigureDisplay(f bandFigureJSON) string {
	if f.Verdict == tokenomics.BandNoBaseline {
		return tokenomics.BandNoBaseline
	}
	line := fmt.Sprintf("%s (median %d ±%d%% ⇒ %d..%d)", f.Verdict, f.Median, f.TolerancePct, f.Low, f.High)
	switch f.Direction {
	case tokenomics.BandDirectionBelow:
		return line + " ↓ " + tokenomics.BandDirectionBelow
	case tokenomics.BandDirectionAbove:
		return line + " ↑ " + tokenomics.BandDirectionAbove
	}
	return line
}

// emitTelemetryBandJSON writes to os.Stdout directly, for emitTelemetryJSONDocument's measured
// reason (telemetry_json.go:504-510): the cobra seam resolves to the ROOT command's writer, which
// sibling tests in this package redirect and never restore.
func emitTelemetryBandJSON(cmd *cobra.Command, factoryRoot string) error {
	agentFilter, _ := cmd.Flags().GetString("agent")
	instanceFilter, _ := cmd.Flags().GetString("instance")

	dto, err := bandReportDTO(factoryRoot, agentFilter, instanceFilter)
	if err != nil {
		return emitTelemetryJSONError(err)
	}
	data, marshalErr := json.Marshal(dto)
	if marshalErr != nil {
		return emitTelemetryJSONError(marshalErr)
	}
	fmt.Println(string(data))
	return nil
}
