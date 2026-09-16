package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// #678 K9, the disk-and-render third of the Measurement Protocol. tokenomics.ProtocolCompare owns
// the verdict and telemetry.MeasureRun owns the fold; this file is the only part that knows where
// records live, which runs the operator nominated, and how to lay ten of them out.
//
// Record-only, deliberately: it reads through telemetry.ReadEvents and never opens a transcript or
// a formula file. A protocol that had to re-read the inputs to judge the outputs could not be run
// after the fact, which is the one thing a measurement protocol has to be able to do.
//
// No field below is omitempty and every absent figure is a pointer rendered as an explicit null —
// the report family's rule (telemetry_json.go:43-45). A comparison whose key set moved with the
// state of the factory could not be parsed by one consumer.

const (
	compareSurfaceA = "a"
	compareSurfaceB = "b"

	compareArmBefore = "before"
	compareArmAfter  = "after"
)

// The check vocabulary. Every one of these is a reason a comparison can be VOID, and they are named
// rather than folded into one boolean because "void" with no named check is exactly the
// unfalsifiable refusal the verdict exists to replace.
const (
	compareCheckDistinctRuns        = "distinct_runs"
	compareCheckArmSize             = "arm_size"
	compareCheckFormula             = "formula_matches"
	compareCheckRunCompletion       = "run_completion"
	compareCheckStepsClosedConstant = "steps_closed_constant"
	compareCheckMeasuredCoverage    = "measured_steps_cover_closed"
	compareCheckAFCommit            = "af_commit_constant"
	compareCheckModel               = "model_constant"
	compareCheckHostVersion         = "host_version_constant"
	compareCheckCheckoutCommit      = "checkout_commit_constant"
	compareCheckInputDigest         = "input_digest_constant"
	compareCheckInputDigestVerified = "input_digest_verified"
	compareCheckBaseCommit          = "base_commit_matches_checkout"
	compareCheckFormulaDigest       = "formula_digest_constant"
	compareCheckTokenomicsState     = "tokenomics_state_per_arm"
	compareCheckNestingComparable   = "nesting_comparable"
)

type compareRunJSON struct {
	InstanceID string `json:"instance_id"`
	Arm        string `json:"arm"`
	Agent      string `json:"agent"`
	Started    string `json:"started"`
	Model      string `json:"model"`

	AFCommit       string `json:"af_commit"`
	HostVersion    string `json:"host_version"`
	CheckoutCommit string `json:"checkout_commit"`
	BaseCommit     string `json:"base_commit"`

	FormulaDigestStart string `json:"formula_digest_start"`
	FormulaDigestEnd   string `json:"formula_digest_end"`
	TokenomicsState    string `json:"tokenomics_state"`
	// InputDigest is read from the record's sling_digest. The two spellings are deliberate: the
	// design and the --input-digest flag both call this quantity the input digest, and the RECORD
	// field is named for the surface that writes it because the closed-schema content-name ban
	// rejects "input" as a substring (event.go:367-378).
	InputDigest string `json:"input_digest"`

	Completed     bool     `json:"completed"`
	StepsClosed   int      `json:"steps_closed"`
	MeasuredSteps int      `json:"measured_steps"`
	Interrupted   int      `json:"interrupted"`
	Sessions      int      `json:"sessions"`
	EffortLevels  []string `json:"effort_levels"`

	OutTokens      *int64 `json:"out_tokens"`
	SubagentTokens *int64 `json:"subagent_tokens"`
	Metric         int64  `json:"metric"`

	ThinkTokens    *int64 `json:"think_tokens"`
	ThinkTokensEst *int64 `json:"think_tokens_est"`

	SubagentLaunches    *int64 `json:"subagent_launches"`
	WorkflowLaunches    *int64 `json:"workflow_launches"`
	NestedLaunches      *int64 `json:"nested_launches"`
	RepeatReads         *int64 `json:"repeat_reads"`
	GateFlags           *int64 `json:"gate_flags"`
	InTokens            *int64 `json:"in_tokens"`
	CacheReadTokens     *int64 `json:"cache_read_tokens"`
	CacheCreationTokens *int64 `json:"cache_creation_tokens"`

	// TrustedKeys and LearnedMinRuns ride every row so a dark after-arm is visible in the matrix
	// itself. An arm that changed nothing because the factory had learned nothing looks exactly
	// like an arm whose intervention did not work, and only these two columns separate them.
	TrustedKeys    int `json:"trusted_keys"`
	LearnedMinRuns int `json:"learned_min_runs"`

	ExcludedReason string `json:"excluded_reason"`
}

// compareCheckJSON is one protocol check. Applies separates "this check ran and passed" from "this
// check had nothing to judge" — an unsupplied attestation reported as a pass would be the report
// asserting something nobody attested.
type compareCheckJSON struct {
	Name    string `json:"name"`
	Applies bool   `json:"applies"`
	Passed  bool   `json:"passed"`
	Detail  string `json:"detail"`
}

type compareVerdictJSON struct {
	Verdict     string `json:"verdict"`
	MedianAfter int64  `json:"median_after"`
	MinBefore   int64  `json:"min_before"`
	// The three figures that keep a pass from reading as a discovery: how much the before arm
	// varied on its own, how large a reduction the bar therefore demands, and how often the bar
	// passes on noise alone.
	BeforeSpreadPct         int      `json:"before_spread_pct"`
	RequiredReductionPct    int      `json:"required_reduction_pct"`
	NullFalsePassFavourable int      `json:"null_false_pass_favourable"`
	NullFalsePassTotal      int      `json:"null_false_pass_total"`
	VoidBecause             []string `json:"void_because"`
}

type compareInterventionJSON struct {
	Arm       string `json:"arm"`
	Mechanism string `json:"mechanism"`
	Objective string `json:"objective"`
	Count     int    `json:"count"`
}

// compareArmAuditJSON is AC-6 made checkable: only token consumption may drop. An arm that got
// cheaper by finishing fewer steps, waiting at more gates or producing worse artifacts is not a
// cheaper arm, and the verdict alone cannot tell the difference.
type compareArmAuditJSON struct {
	Arm               string `json:"arm"`
	Runs              int    `json:"runs"`
	StatusClosed      int    `json:"status_closed"`
	StatusSkipped     int    `json:"status_skipped"`
	StatusGateWaiting int    `json:"status_gate_waiting"`
	Interrupted       int    `json:"interrupted"`
	GateFlags         int64  `json:"gate_flags"`
	// The fidelity legs are operator-attested rather than record-derived: artifact accuracy is not
	// a quantity any record carries, and inventing one would be worse than asking for it.
	FidelityRuns       int `json:"fidelity_runs"`
	FidelityVerified   int `json:"fidelity_verified"`
	FidelityInaccurate int `json:"fidelity_inaccurate"`
}

type compareAuditJSON struct {
	Arms          []compareArmAuditJSON     `json:"arms"`
	Interventions []compareInterventionJSON `json:"interventions"`
	// FidelityUnattributed names the --fidelity entries that reached no counted run — a typo'd id,
	// or the id of a run C-9 excluded. Reported rather than dropped: an operator who attested the
	// artifacts of a run that then left the arm has made a claim about evidence this report does
	// not use, and silently discarding it is the same "silently short" failure the exclusion list
	// exists to prevent.
	FidelityUnattributed []string `json:"fidelity_unattributed"`
}

type compareReportJSON struct {
	V              int                    `json:"v"`
	State          string                 `json:"state"`
	Formula        string                 `json:"formula"`
	Surface        string                 `json:"surface"`
	ArmSize        int                    `json:"arm_size"`
	LearnedMinRuns int                    `json:"learned_min_runs"`
	Runs           []compareRunJSON       `json:"runs"`
	Excluded       []compareRunJSON       `json:"excluded"`
	Checks         []compareCheckJSON     `json:"checks"`
	Verdict        compareVerdictJSON     `json:"verdict"`
	Audit          compareAuditJSON       `json:"audit"`
	Stats          telemetryReadStatsJSON `json:"stats"`
}

type compareRequest struct {
	formula      string
	surface      string
	before       []string
	after        []string
	verifyDigest string
	fidelity     map[string]compareFidelity
}

type compareFidelity struct {
	verified   int
	inaccurate int
}

type compareArm struct {
	name string
	runs []telemetry.RunMeasure
}

func compareRequestFrom(cmd *cobra.Command) (compareRequest, error) {
	req := compareRequest{fidelity: map[string]compareFidelity{}}
	req.formula, _ = cmd.Flags().GetString("formula")
	req.surface, _ = cmd.Flags().GetString("surface")
	req.verifyDigest, _ = cmd.Flags().GetString("verify-input-digest")
	before, _ := cmd.Flags().GetString("before")
	after, _ := cmd.Flags().GetString("after")
	fidelity, _ := cmd.Flags().GetString("fidelity")

	req.before, req.after = compareSplit(before), compareSplit(after)

	if req.formula == "" {
		return req, fmt.Errorf("--formula is required: a comparison is of one formula's runs against its own")
	}
	if req.surface != compareSurfaceA && req.surface != compareSurfaceB {
		return req, fmt.Errorf("--surface must be %q (the formula changed) or %q (the posture changed), got %q",
			compareSurfaceA, compareSurfaceB, req.surface)
	}
	if len(req.before) == 0 || len(req.after) == 0 {
		return req, fmt.Errorf("--before and --after each need the instance ids of one arm's runs")
	}
	for _, entry := range compareSplit(fidelity) {
		id, counts, ok := strings.Cut(entry, "=")
		verified, inaccurate, split := strings.Cut(counts, "/")
		if !ok || !split || id == "" {
			return req, fmt.Errorf("--fidelity entry %q: want <instance_id>=<verified>/<inaccurate>", entry)
		}
		v, verr := strconv.Atoi(verified)
		i, ierr := strconv.Atoi(inaccurate)
		if verr != nil || ierr != nil {
			return req, fmt.Errorf("--fidelity entry %q: both counts must be integers", entry)
		}
		// Rejected rather than last-wins: two attestations of one run are two different claims
		// about the same artifacts, and silently keeping the later one discards the disagreement
		// that is the interesting part.
		if _, dup := req.fidelity[id]; dup {
			return req, fmt.Errorf("--fidelity names %s twice; one run gets one attestation", id)
		}
		req.fidelity[id] = compareFidelity{verified: v, inaccurate: i}
	}
	return req, nil
}

func compareSplit(csv string) []string {
	out := []string{}
	for _, part := range strings.Split(csv, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

// compareReportDTO reads the log once and derives everything. It stores nothing and stamps nothing.
func compareReportDTO(factoryRoot string, req compareRequest) (compareReportJSON, error) {
	out := compareReportJSON{
		V:        telemetry.SchemaVersion,
		State:    telemetryStateOK,
		Formula:  req.formula,
		Surface:  req.surface,
		ArmSize:  tokenomics.ProtocolArmSize,
		Runs:     []compareRunJSON{},
		Excluded: []compareRunJSON{},
		Checks:   []compareCheckJSON{},
		Audit:    compareAuditJSON{Arms: []compareArmAuditJSON{}, Interventions: []compareInterventionJSON{}},
	}
	out.LearnedMinRuns = bandMinRuns(factoryRoot)

	agents, err := telemetryReportAgents(factoryRoot, "")
	if err != nil {
		return out, err
	}

	// The whole roster, because an instance id says nothing about which agent's log holds it: a
	// dispatched run records under the agent that ran it, and ReadEvents refuses an empty agent
	// name. The same fan-out bandReportDTO and readTokenomicsInterventionTail already do.
	dir := config.TelemetryDir(factoryRoot)
	var records []telemetry.StepEvent
	var stats telemetry.ReadStats
	for _, agent := range agents {
		agentRecords, agentStats, readErr := telemetry.ReadEvents(dir, telemetry.Filter{Agent: agent})
		if readErr != nil {
			return out, fmt.Errorf("reading %s step records: %w", agent, readErr)
		}
		records = append(records, agentRecords...)
		stats.Malformed += agentStats.Malformed
		stats.Dropped += agentStats.Dropped
		stats.DroppedUnexported += agentStats.DroppedUnexported
	}

	digests, unreadableDigests := loadLearnedDigests(dir)
	trustedKeys := tokenomics.CoverageJoinEligible(digests[req.formula], out.LearnedMinRuns)

	// The nomination is deduped BEFORE anything is measured, and the id that was repeated is named.
	// One id nominated five times would otherwise become five identical rows: every constancy check
	// compares a set and would see one value, arm_size would count five, and a comparison of two
	// runs would print the 21-in-252 false-pass rate of ten. That figure is a statement about ten
	// INDEPENDENT runs, and nothing else in this file can tell that it is not describing them.
	nominated := map[string]string{}
	repeated := []string{}
	arms := []compareArm{{name: compareArmBefore}, {name: compareArmAfter}}
	for i, ids := range [][]string{req.before, req.after} {
		for _, id := range ids {
			if firstArm, seen := nominated[id]; seen {
				repeated = append(repeated, fmt.Sprintf("%s (nominated in the %s arm and again in the %s arm)",
					id, firstArm, arms[i].name))
				continue
			}
			nominated[id] = arms[i].name

			m := telemetry.MeasureRun(records, id)
			row := compareRunRow(m, arms[i].name, trustedKeys, out.LearnedMinRuns)
			if m.ExcludedReason != "" {
				out.Excluded = append(out.Excluded, row)
				continue
			}
			arms[i].runs = append(arms[i].runs, m)
			out.Runs = append(out.Runs, row)
		}
	}

	out.Checks = compareChecks(arms, req, repeated)
	out.Verdict = compareVerdict(arms, out.Checks)
	out.Audit = compareAudit(arms, req, nominated)

	out.Stats = telemetryReadStatsJSON{
		Malformed:         stats.Malformed,
		Dropped:           stats.Dropped,
		DroppedUnexported: stats.DroppedUnexported,
	}
	// A partial read never silently passes an arm: a comparison computed over a subset of the runs
	// an operator nominated is a different comparison from the one they asked for.
	if stats.Malformed > 0 || stats.Dropped > 0 || unreadableDigests > 0 {
		out.State = telemetryStateDegraded
	}
	return out, nil
}

func compareRunRow(m telemetry.RunMeasure, arm string, trustedKeys, minRuns int) compareRunJSON {
	return compareRunJSON{
		InstanceID: m.InstanceID, Arm: arm, Agent: m.Agent, Started: m.StartedAt, Model: m.Model,
		AFCommit: m.AFCommit, HostVersion: m.HostVersion,
		CheckoutCommit: m.CheckoutCommit, BaseCommit: m.BaseCommit,
		FormulaDigestStart: m.FormulaDigestStart, FormulaDigestEnd: m.FormulaDigestEnd,
		TokenomicsState: m.TokenomicsState, InputDigest: m.SlingDigest,
		Completed: m.Completed, StepsClosed: m.StepsClosed, MeasuredSteps: m.MeasuredSteps,
		Interrupted: m.Interrupted, Sessions: m.Sessions, EffortLevels: m.EffortLevels,
		OutTokens: m.OutTokens, SubagentTokens: m.SubagentTokens, Metric: m.Metric,
		ThinkTokens: m.ThinkTokens, ThinkTokensEst: m.ThinkTokensEst,
		SubagentLaunches: m.SubagentLaunches, WorkflowLaunches: m.WorkflowLaunches,
		NestedLaunches: m.SubagentNestedLaunches, RepeatReads: m.RepeatReads, GateFlags: m.GateFlags,
		InTokens: m.InTokens, CacheReadTokens: m.CacheReadTokens,
		CacheCreationTokens: m.CacheCreationTokens,
		TrustedKeys:         trustedKeys, LearnedMinRuns: minRuns,
		ExcludedReason: m.ExcludedReason,
	}
}

func compareChecks(arms []compareArm, req compareRequest, repeated []string) []compareCheckJSON {
	checks := []compareCheckJSON{
		compareDistinctCheck(repeated),
		compareArmSizeCheck(arms),
		compareFormulaCheck(arms, req.formula),
		compareCompletionCheck(arms),
		compareStepsClosedCheck(arms, req.surface),
		compareCoverageCheck(arms),
		compareConstantCheck(compareCheckAFCommit, arms, func(m telemetry.RunMeasure) string { return m.AFCommit }),
		compareConstantCheck(compareCheckModel, arms, func(m telemetry.RunMeasure) string { return m.Model }),
		compareConstantCheck(compareCheckHostVersion, arms, func(m telemetry.RunMeasure) string { return m.HostVersion }),
		compareConstantCheck(compareCheckCheckoutCommit, arms, func(m telemetry.RunMeasure) string { return m.CheckoutCommit }),
		compareConstantCheck(compareCheckInputDigest, arms, func(m telemetry.RunMeasure) string { return m.SlingDigest }),
		compareAttestationCheck(arms, req.verifyDigest),
		compareBaseCommitCheck(arms),
		compareDigestCheck(arms, req.surface),
		comparePostureCheck(arms, req.surface),
		compareNestingCheck(arms),
	}
	return checks
}

// compareDistinctCheck reports the ids the nomination repeated. It is a check rather than a silent
// dedupe because the two failures read identically once the duplicate is dropped — a five-id arm
// with one repeat and a genuine four-run arm both void on arm_size — and only this names which.
func compareDistinctCheck(repeated []string) compareCheckJSON {
	check := compareCheckJSON{Name: compareCheckDistinctRuns, Applies: true, Passed: len(repeated) == 0}
	if len(repeated) > 0 {
		check.Detail = "the same run was nominated more than once: " + strings.Join(repeated, "; ")
	}
	return check
}

// compareFormulaCheck holds the payload's own claim to the evidence. --formula is required, is
// echoed into the report, and decides which learned digest trusted_keys is read from — so a run of
// a DIFFERENT formula in an arm makes all three of those statements false at once, while every
// other check passes because that run is perfectly self-consistent.
//
// MeasureRun filters on instance id alone, deliberately: the id is the join key and a fold that
// also filtered on formula would silently return an empty run for a typo'd name instead of the
// mismatch this check reports.
func compareFormulaCheck(arms []compareArm, want string) compareCheckJSON {
	check := compareCheckJSON{Name: compareCheckFormula, Passed: true}
	var details []string
	recorded := false
	for _, arm := range arms {
		for _, r := range arm.runs {
			if r.Formula == "" {
				continue
			}
			recorded = true
			if r.Formula != want {
				check.Passed = false
				details = append(details, fmt.Sprintf("%s (%s) is a run of %q", r.InstanceID, arm.name, r.Formula))
			}
		}
	}
	if !recorded {
		check.Detail = "no run recorded a formula name, so the evidence cannot be tied to " +
			strconv.Quote(want)
		return check
	}
	check.Applies = true
	check.Detail = strings.Join(details, "; ")
	return check
}

func compareArmSizeCheck(arms []compareArm) compareCheckJSON {
	// Void and not fail on a short arm: an under-sized arm is an absence of evidence, and the
	// false-pass rate printed beside the verdict describes a 5 + 5 split and nothing else.
	check := compareCheckJSON{Name: compareCheckArmSize, Applies: true, Passed: true}
	var details []string
	for _, arm := range arms {
		if len(arm.runs) != tokenomics.ProtocolArmSize {
			check.Passed = false
			details = append(details, fmt.Sprintf("%s arm has %d measured runs, want %d",
				arm.name, len(arm.runs), tokenomics.ProtocolArmSize))
		}
	}
	check.Detail = strings.Join(details, "; ")
	return check
}

// compareCompletionCheck is the run-completion arm check, derived from records alone.
//
// instance_end is written at exactly one site (done.go) and only after the completion-velocity
// guard passes, so its presence IS the record-only evidence that a run finished. The formula's
// declared step count is deliberately not read: doing so would need the TOML, and max(step_seq) is
// computed as totalSteps-openCount+1, which makes it self-satisfying on exactly the crashed runs
// this check exists to catch.
func compareCompletionCheck(arms []compareArm) compareCheckJSON {
	check := compareCheckJSON{Name: compareCheckRunCompletion, Applies: true, Passed: true}
	var details []string
	for _, arm := range arms {
		for _, r := range arm.runs {
			if !r.Completed {
				check.Passed = false
				details = append(details, fmt.Sprintf("%s (%s) wrote no instance_end record", r.InstanceID, arm.name))
			}
		}
	}
	check.Detail = strings.Join(details, "; ")
	return check
}

// compareStepsClosedCheck is the second half of the same question: not "did it end" but "did it do
// the same amount of work". Constancy is the record-only form of the design's step-count check —
// no StepEvent carries a formula's declared step count, and every run of one formula that ran to
// completion closes the same number of steps. Under --surface b the same formula spans both arms,
// so the count must hold across them too.
func compareStepsClosedCheck(arms []compareArm, surface string) compareCheckJSON {
	check := compareConstantCheck(compareCheckStepsClosedConstant, arms,
		func(m telemetry.RunMeasure) string { return strconv.Itoa(m.StepsClosed) })
	if surface != compareSurfaceB || !check.Passed {
		return check
	}
	seen := map[int]bool{}
	for _, arm := range arms {
		for _, r := range arm.runs {
			seen[r.StepsClosed] = true
		}
	}
	if len(seen) > 1 {
		check.Passed = false
		check.Detail = "the two arms closed different numbers of steps for one formula"
	}
	return check
}

func compareCoverageCheck(arms []compareArm) compareCheckJSON {
	check := compareCheckJSON{Name: compareCheckMeasuredCoverage, Applies: true, Passed: true}
	var details []string
	for _, arm := range arms {
		for _, r := range arm.runs {
			if r.MeasuredSteps != r.StepsClosed {
				check.Passed = false
				details = append(details, fmt.Sprintf("%s (%s) measured %d of %d closed steps",
					r.InstanceID, arm.name, r.MeasuredSteps, r.StepsClosed))
			}
		}
	}
	check.Detail = strings.Join(details, "; ")
	return check
}

// compareConstantCheck asserts one field holds one value within each arm.
//
// A field NO run recorded does not apply, and that distinction is the whole difference between a
// check and a decoration: ten empty strings are one value, so a naive set comparison reports
// "constant" over an arm that stamped nothing. Several of these fields are routinely empty —
// sling_digest whenever --input-digest was omitted, af_commit on any binary built without the
// ldflags stamp — so the vacuous pass is the NORMAL case, not a corner of it, and a report claiming
// the input digest was constant across ten runs that carry no digest asserts something nobody
// recorded.
//
// A field SOME runs recorded still applies: the empty string is then one of the differing values
// and the check fails on it, which is right — a run that stamped nothing is not comparable to one
// that did.
func compareConstantCheck(name string, arms []compareArm, of func(telemetry.RunMeasure) string) compareCheckJSON {
	check := compareCheckJSON{Name: name, Passed: true}
	var details []string
	recorded := false
	for _, arm := range arms {
		seen := map[string]bool{}
		for _, r := range arm.runs {
			v := of(r)
			if v != "" {
				recorded = true
			}
			seen[v] = true
		}
		if len(seen) > 1 {
			check.Passed = false
			details = append(details, fmt.Sprintf("%s arm: %s", arm.name, strings.Join(compareSorted(seen), ", ")))
		}
	}
	if !recorded {
		return compareCheckJSON{
			Name:   name,
			Detail: "no run recorded this field, so its constancy is unasserted rather than confirmed",
		}
	}
	check.Applies = true
	check.Detail = strings.Join(details, "; ")
	return check
}

// compareAttestationCheck verifies the frozen-input digest the operator attested.
//
// Applies is false when no attestation was supplied, rather than reporting a vacuous pass: a report
// claiming the inputs were verified when nobody offered a digest to verify them against would be
// the report asserting something no one attested.
func compareAttestationCheck(arms []compareArm, want string) compareCheckJSON {
	check := compareCheckJSON{Name: compareCheckInputDigestVerified}
	if want == "" {
		check.Detail = "no --verify-input-digest supplied, so nothing was attested"
		return check
	}
	check.Applies, check.Passed = true, true
	var details []string
	for _, arm := range arms {
		for _, r := range arm.runs {
			if r.SlingDigest != want {
				check.Passed = false
				details = append(details, fmt.Sprintf("%s (%s) was slung with %q",
					r.InstanceID, arm.name, r.SlingDigest))
			}
		}
	}
	check.Detail = strings.Join(details, "; ")
	return check
}

// compareBaseCommitCheck asserts the rebase landed on the pin: every run's merge-base is constant
// within its arm AND equal to the commit the tree was checked out at. A run whose branch moved
// underneath it was measured against a different tree from the rest of its arm.
func compareBaseCommitCheck(arms []compareArm) compareCheckJSON {
	check := compareConstantCheck(compareCheckBaseCommit, arms,
		func(m telemetry.RunMeasure) string { return m.BaseCommit })
	var details []string
	if check.Detail != "" && check.Applies {
		details = append(details, check.Detail)
	}
	// The equality leg is skipped where either side is absent, for compareConstantCheck's reason:
	// "" == "" would report the rebase as landing on the pin over two commits nobody recorded,
	// which is the CRITICAL-1 assertion this check exists to make and the one it must not fake.
	compared := false
	for _, arm := range arms {
		for _, r := range arm.runs {
			if r.BaseCommit == "" || r.CheckoutCommit == "" {
				continue
			}
			compared = true
			if r.BaseCommit != r.CheckoutCommit {
				check.Passed = false
				details = append(details, fmt.Sprintf("%s (%s) rebased onto %q from a checkout at %q",
					r.InstanceID, arm.name, r.BaseCommit, r.CheckoutCommit))
			}
		}
	}
	if !check.Applies && !compared {
		return check
	}
	check.Applies = true
	check.Detail = strings.Join(details, "; ")
	return check
}

// compareDigestCheck is where the two surfaces differ, and it is the reason --surface exists.
//
// Surface b holds the formula fixed and moves the posture, so ONE digest must span all ten runs —
// both ends of every run, since a mid-run edit would otherwise pass unseen. Surface a moves the
// formula itself, so each arm must be internally constant and the two arms must DIFFER: one digest
// across both arms means the treatment never landed and the comparison measured nothing.
func compareDigestCheck(arms []compareArm, surface string) compareCheckJSON {
	check := compareCheckJSON{Name: compareCheckFormulaDigest, Passed: true}
	var details []string
	recorded := false

	armDigests := make([]map[string]bool, len(arms))
	for i, arm := range arms {
		seen := map[string]bool{}
		for _, r := range arm.runs {
			if r.FormulaDigestStart != "" || r.FormulaDigestEnd != "" {
				recorded = true
			}
			seen[r.FormulaDigestStart] = true
			seen[r.FormulaDigestEnd] = true
		}
		armDigests[i] = seen
		if len(seen) > 1 {
			check.Passed = false
			details = append(details, fmt.Sprintf("%s arm: %s", arm.name, strings.Join(compareSorted(seen), ", ")))
		}
	}

	switch surface {
	case compareSurfaceB:
		all := map[string]bool{}
		for _, seen := range armDigests {
			for digest := range seen {
				all[digest] = true
			}
		}
		if len(all) > 1 {
			check.Passed = false
			details = append(details, "surface b holds the formula fixed, but the ten runs span "+
				strconv.Itoa(len(all))+" digests")
		}
	case compareSurfaceA:
		if len(armDigests) == 2 && len(armDigests[0]) == 1 && len(armDigests[1]) == 1 &&
			compareSorted(armDigests[0])[0] == compareSorted(armDigests[1])[0] {
			check.Passed = false
			details = append(details, "surface a measures a formula change, but both arms ran the same digest")
		}
	}
	if !recorded {
		return compareCheckJSON{
			Name: compareCheckFormulaDigest,
			Detail: "no run recorded a formula digest, so neither the one-digest rule (surface b) " +
				"nor the distinct-digest rule (surface a) can be asserted",
		}
	}
	check.Applies = true
	check.Detail = strings.Join(details, "; ")
	return check
}

// comparePostureCheck applies to surface b alone: the posture IS the treatment there, so a before
// arm that recorded tokenomics on, or an after arm that recorded it off, ran the wrong experiment.
// Under surface a the formula is the treatment and the posture is held constant by the operator,
// which the af_commit and digest checks already cover.
func comparePostureCheck(arms []compareArm, surface string) compareCheckJSON {
	check := compareCheckJSON{Name: compareCheckTokenomicsState}
	if surface != compareSurfaceB {
		check.Detail = "surface a moves the formula, not the posture"
		return check
	}
	check.Applies, check.Passed = true, true
	want := map[string]string{
		compareArmBefore: telemetry.TokenomicsStateOff,
		compareArmAfter:  telemetry.TokenomicsStateOn,
	}
	var details []string
	for _, arm := range arms {
		for _, r := range arm.runs {
			if r.TokenomicsState != want[arm.name] {
				check.Passed = false
				details = append(details, fmt.Sprintf("%s (%s) recorded tokenomics %q, want %q",
					r.InstanceID, arm.name, r.TokenomicsState, want[arm.name]))
			}
		}
	}
	check.Detail = strings.Join(details, "; ")
	return check
}

// compareNestingCheck asks whether the two arms counted delegation the same way. An arm whose runs
// recorded nested launches compared against one whose runs never looked would read as a change in
// delegation depth that nobody made — the figure moved because the measurement did.
func compareNestingCheck(arms []compareArm) compareCheckJSON {
	check := compareCheckJSON{Name: compareCheckNestingComparable, Applies: true, Passed: true}
	observed := map[string]bool{}
	for _, arm := range arms {
		for _, r := range arm.runs {
			if r.SubagentNestedLaunches != nil {
				observed[arm.name] = true
			}
		}
	}
	if len(arms) == 2 && observed[arms[0].name] != observed[arms[1].name] {
		check.Passed = false
		check.Detail = "one arm counted nested launches and the other recorded none, so a change in " +
			"delegation depth cannot be told from a change in what was measured"
	}
	return check
}

func compareVerdict(arms []compareArm, checks []compareCheckJSON) compareVerdictJSON {
	before, after := compareMetrics(arms, compareArmBefore), compareMetrics(arms, compareArmAfter)

	verdict, medianAfter, minBefore := tokenomics.ProtocolCompare(before, after)
	favourable, total := tokenomics.ProtocolNullFalsePassOdds(len(before), len(after))
	out := compareVerdictJSON{
		Verdict:                 verdict,
		MedianAfter:             medianAfter,
		MinBefore:               minBefore,
		BeforeSpreadPct:         tokenomics.ProtocolSpreadPct(before),
		RequiredReductionPct:    tokenomics.ProtocolRequiredReductionPct(before),
		NullFalsePassFavourable: favourable,
		NullFalsePassTotal:      total,
		VoidBecause:             []string{},
	}
	// A failed check voids and never fails. Fail is a claim about the intervention; void is a claim
	// about the comparison, and a comparison whose arms were not held fixed says nothing about
	// either direction.
	for _, c := range checks {
		if c.Applies && !c.Passed {
			out.VoidBecause = append(out.VoidBecause, c.Name)
		}
	}
	if len(out.VoidBecause) > 0 {
		out.Verdict = tokenomics.ProtocolVoid
	}
	return out
}

func compareMetrics(arms []compareArm, name string) []int64 {
	for _, arm := range arms {
		if arm.name != name {
			continue
		}
		metrics := make([]int64, 0, len(arm.runs))
		for _, r := range arm.runs {
			metrics = append(metrics, r.Metric)
		}
		return metrics
	}
	return nil
}

func compareAudit(arms []compareArm, req compareRequest, nominated map[string]string) compareAuditJSON {
	audit := compareAuditJSON{
		Arms:                 make([]compareArmAuditJSON, 0, len(arms)),
		Interventions:        []compareInterventionJSON{},
		FidelityUnattributed: []string{},
	}
	attributed := map[string]bool{}
	for _, arm := range arms {
		row := compareArmAuditJSON{Arm: arm.name, Runs: len(arm.runs)}
		fired := map[telemetry.InterventionCount]int{}
		for _, r := range arm.runs {
			row.StatusClosed += r.StatusClosed
			row.StatusSkipped += r.StatusSkipped
			row.StatusGateWaiting += r.StatusGateWaiting
			row.Interrupted += r.Interrupted
			if r.GateFlags != nil {
				row.GateFlags += *r.GateFlags
			}
			if f, ok := req.fidelity[r.InstanceID]; ok {
				attributed[r.InstanceID] = true
				row.FidelityRuns++
				row.FidelityVerified += f.verified
				row.FidelityInaccurate += f.inaccurate
			}
			for _, i := range r.Interventions {
				fired[telemetry.InterventionCount{Mechanism: i.Mechanism, Objective: i.Objective}] += i.Count
			}
		}
		audit.Arms = append(audit.Arms, row)

		keys := make([]telemetry.InterventionCount, 0, len(fired))
		for key := range fired {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			if keys[i].Mechanism != keys[j].Mechanism {
				return keys[i].Mechanism < keys[j].Mechanism
			}
			return keys[i].Objective < keys[j].Objective
		})
		for _, key := range keys {
			audit.Interventions = append(audit.Interventions, compareInterventionJSON{
				Arm: arm.name, Mechanism: key.Mechanism, Objective: key.Objective, Count: fired[key],
			})
		}
	}

	for id := range req.fidelity {
		if attributed[id] {
			continue
		}
		reason := "names no nominated run"
		if arm, ok := nominated[id]; ok {
			reason = "was nominated in the " + arm + " arm but is not a counted run there"
		}
		audit.FidelityUnattributed = append(audit.FidelityUnattributed, id+" ("+reason+")")
	}
	sort.Strings(audit.FidelityUnattributed)
	return audit
}

func compareSorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// runTelemetryCompare is the human rendering. Like the report table and the band it is deliberately
// NOT gated on the telemetry switch: a comparison is a read over records that already exist, and
// the after arm's posture is the thing being measured rather than a precondition for reading it.
func runTelemetryCompare(cmd *cobra.Command, factoryRoot string) error {
	req, err := compareRequestFrom(cmd)
	if err != nil {
		return err
	}
	dto, err := compareReportDTO(factoryRoot, req)
	if err != nil {
		return err
	}

	fmt.Printf("compare: %s (surface %s, learned min runs %d, arm size %d)\n",
		dto.Formula, dto.Surface, dto.LearnedMinRuns, dto.ArmSize)

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ARM\tINSTANCE\tMETRIC\tOUT\tSUBAGENT\tTHINK\tTHINK_EST\tSTEPS\tMEASURED\tSESSIONS\tEFFORT\tTRUSTED")
	for _, r := range dto.Runs {
		fmt.Fprintf(w, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%d\t%d\t%d\t%s\t%d\n",
			r.Arm, r.InstanceID, r.Metric,
			bandObservedDisplay(r.OutTokens), bandObservedDisplay(r.SubagentTokens),
			bandObservedDisplay(r.ThinkTokens), bandObservedDisplay(r.ThinkTokensEst),
			r.StepsClosed, r.MeasuredSteps, r.Sessions,
			compareDisplayList(r.EffortLevels), r.TrustedKeys)
	}
	fmt.Fprintln(w, "\nARM\tINSTANCE\tSTARTED\tMODEL\tAF_COMMIT\tHOST\tCHECKOUT\tBASE\tDIGEST(start/end)\tSTATE\tINPUT_DIGEST\tDONE")
	for _, r := range dto.Runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s/%s\t%s\t%s\t%t\n",
			r.Arm, r.InstanceID, r.Started, r.Model, r.AFCommit, r.HostVersion,
			r.CheckoutCommit, r.BaseCommit, r.FormulaDigestStart, r.FormulaDigestEnd,
			r.TokenomicsState, r.InputDigest, r.Completed)
	}
	fmt.Fprintln(w, "\nARM\tINSTANCE\tLAUNCHES\tWORKFLOWS\tNESTED\tREPEAT_READS\tGATE_FLAGS\tIN\tCACHE_READ\tCACHE_CREATE")
	for _, r := range dto.Runs {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			r.Arm, r.InstanceID,
			bandObservedDisplay(r.SubagentLaunches), bandObservedDisplay(r.WorkflowLaunches),
			bandObservedDisplay(r.NestedLaunches), bandObservedDisplay(r.RepeatReads),
			bandObservedDisplay(r.GateFlags), bandObservedDisplay(r.InTokens),
			bandObservedDisplay(r.CacheReadTokens), bandObservedDisplay(r.CacheCreationTokens))
	}
	if err := w.Flush(); err != nil {
		return err
	}

	for _, r := range dto.Excluded {
		fmt.Printf("excluded: %s (%s) — %s\n", r.InstanceID, r.Arm, r.ExcludedReason)
	}

	fmt.Println("\nprotocol checks:")
	for _, c := range dto.Checks {
		fmt.Printf("  %-30s %s%s\n", c.Name, compareCheckDisplay(c), compareDetailDisplay(c.Detail))
	}

	v := dto.Verdict
	fmt.Printf("\nverdict: %s — median(after) %d, min(before) %d\n", v.Verdict, v.MedianAfter, v.MinBefore)
	if v.NullFalsePassTotal > 0 {
		fmt.Printf("  before-arm spread %d%%, the bar demands a %d%% reduction, and it passes on noise "+
			"alone in %d of %d splits (false-pass rate)\n",
			v.BeforeSpreadPct, v.RequiredReductionPct, v.NullFalsePassFavourable, v.NullFalsePassTotal)
	} else {
		fmt.Println("  the arms hold no counted runs, so the bar has no false-pass rate to state")
	}
	if len(v.VoidBecause) > 0 {
		fmt.Printf("  void because: %s\n", strings.Join(v.VoidBecause, ", "))
	}

	fmt.Println("\noutcome audit:")
	for _, arm := range dto.Audit.Arms {
		fmt.Printf("  %-7s runs %d  closed %d  skipped %d  gate-waiting %d  interrupted %d  "+
			"gate_flags %d  fidelity %d/%d over %d runs\n",
			arm.Arm, arm.Runs, arm.StatusClosed, arm.StatusSkipped, arm.StatusGateWaiting,
			arm.Interrupted, arm.GateFlags, arm.FidelityVerified, arm.FidelityInaccurate,
			arm.FidelityRuns)
	}
	if len(dto.Audit.Interventions) == 0 {
		fmt.Println("  interventions: none recorded in either arm")
	}
	for _, i := range dto.Audit.Interventions {
		fmt.Printf("  %-7s %s/%s fired %d\n", i.Arm, i.Mechanism, i.Objective, i.Count)
	}
	for _, entry := range dto.Audit.FidelityUnattributed {
		fmt.Printf("  attested but not counted: %s\n", entry)
	}
	if dto.Stats.Malformed > 0 {
		fmt.Printf("skipped %d unparseable record lines\n", dto.Stats.Malformed)
	}
	return nil
}

func compareCheckDisplay(c compareCheckJSON) string {
	switch {
	case !c.Applies:
		return "n/a"
	case c.Passed:
		return "ok"
	default:
		return "FAILED"
	}
}

func compareDetailDisplay(detail string) string {
	if detail == "" {
		return ""
	}
	return "  " + detail
}

func compareDisplayList(values []string) string {
	if len(values) == 0 {
		return "-"
	}
	return strings.Join(values, ",")
}

// emitTelemetryCompareJSON writes to os.Stdout directly, for emitTelemetryJSONDocument's measured
// reason (telemetry_json.go:504-510): the cobra seam resolves to the ROOT command's writer, which
// sibling tests in this package redirect and never restore.
func emitTelemetryCompareJSON(cmd *cobra.Command, factoryRoot string) error {
	req, err := compareRequestFrom(cmd)
	if err != nil {
		return emitTelemetryJSONError(err)
	}
	dto, err := compareReportDTO(factoryRoot, req)
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
