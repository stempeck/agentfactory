package telemetry

import "sort"

// #678 K9, the record half of the Measurement Protocol. tokenomics.ProtocolCompare decides;
// this file reduces one run's records to the scalars it decides over.
//
// The split is not a style choice. This package already imports internal/tokenomics
// (rebuild.go), so a pure comparator that took StepEvents would be a compile-time cycle — and
// the cycle is pointing at the right design anyway: a function that decides whether a change
// worked must be reproducible from two slices of numbers, with no record schema behind it. The
// closest analogue in the tree is samplesFrom in rebuild.go, which reduces the same records to
// the learned digest; this is the same move for one run instead of many.

// ExcludedUnmeasuredDelegation is C-9, the one exclusion the protocol has.
//
// A run that launched a sub-agent or a workflow and recorded no sub-agent tokens has a metric that
// undercounts by an unknown amount — the delegated generation happened and simply was not seen. It
// is excluded rather than counted as zero, because counting it would drag its arm's figures down
// and bias the comparison toward a pass, which is the one direction a measurement protocol must
// never be biased in.
const ExcludedUnmeasuredDelegation = "delegated_without_subagent_tokens"

// InterventionCount is one (mechanism, objective) pair's firing count within a run. The objective
// leg is what separates the two arms of #678's experiment: the same mechanism fires for either
// reason, so a count without it cannot say whether efficiency ever acted.
type InterventionCount struct {
	Mechanism string
	Objective string
	Count     int
}

// RunMeasure is one run, folded. Everything on it is a scalar or a slice of scalars, so the
// comparator that consumes it never learns what a record looks like.
type RunMeasure struct {
	InstanceID string
	Formula    string
	Agent      string
	StartedAt  string
	Model      string

	// The provenance that has to be constant within an arm for a comparison to mean anything: what
	// binary, on what tree, under what posture, from what inputs.
	AFCommit        string
	HostVersion     string
	CheckoutCommit  string
	BaseCommit      string
	SlingDigest     string
	TokenomicsState string

	// Both ends of the formula's identity. One digest can only say what the run started from; the
	// pair is what turns a mid-run edit from an invisible event into two digests that differ.
	FormulaDigestStart string
	FormulaDigestEnd   string

	// Completed is the presence of an instance_end record, which is the only record-only evidence
	// that a run finished. It is written at exactly one site and only after the completion-velocity
	// guard passes, so a run it rejects has none — and an unfinished before-run left in an arm would
	// make min(before) unbeatable.
	Completed bool

	StepsClosed   int
	MeasuredSteps int
	Interrupted   int
	Sessions      int

	StatusClosed      int
	StatusSkipped     int
	StatusGateWaiting int

	// EffortLevels is sorted and deduped so two runs line up without the reader re-ordering them.
	EffortLevels []string

	// The pass metric and its two legs. Metric is Σ over closed steps of out + subagent tokens, and
	// nothing else is ever added to it.
	//
	// Every summed figure is a POINTER, and nil means no closed step recorded it — which is a
	// different fact from a run that recorded zero. A run whose steps all failed to measure
	// generation would otherwise report a Σ of 0 and read as the cheapest run in its arm.
	OutTokens      *int64
	SubagentTokens *int64
	Metric         int64

	// Diagnostics. ThinkTokens is the host's own count and ThinkTokensEst the derived estimate;
	// both are carried because a change in thinking is worth seeing, and NEITHER is summed into
	// Metric — a pass metric that included them would measure how hard the model thought rather
	// than what it produced.
	ThinkTokens    *int64
	ThinkTokensEst *int64

	InTokens            *int64
	CacheReadTokens     *int64
	CacheCreationTokens *int64

	SubagentLaunches       *int64
	WorkflowLaunches       *int64
	SubagentNestedLaunches *int64
	RepeatReads            *int64
	GateFlags              *int64

	Interventions []InterventionCount

	// ExcludedReason is empty for a run that counts. Named rather than dropped: an arm silently
	// short by one run is an arm nobody can audit.
	ExcludedReason string
}

// MeasureRun folds every record belonging to one instance into that run's figures.
//
// The whole log is passed in and filtered here rather than by the caller, because the filter IS
// part of the measurement: a fold that silently absorbed a neighbouring run's step would report a
// metric no operator could reproduce.
//
// Every FIGURE is order-independent: the sums, the counts, the session and effort sets and the
// intervention tally all fold associatively, so a run whose records were appended out of order —
// two agents closing steps concurrently — still reports the same numbers. The provenance STRINGS
// are not, and cannot be: HostVersion takes the first step_end that carries one, and the
// instance_start fields take the last such record. Both are well-defined and neither is a set, so
// a run that spanned a host upgrade reports one version rather than the drift. That drift is
// invisible to the arm-level constancy checks, and making it visible would mean folding provenance
// as a set — a wider change than this fold's callers need.
func MeasureRun(records []StepEvent, instanceID string) RunMeasure {
	m := RunMeasure{InstanceID: instanceID, EffortLevels: []string{}, Interventions: []InterventionCount{}}

	var out, subagent, think, thinkEst sum
	var in, cacheRead, cacheCreation sum
	var subagentLaunches, workflowLaunches, nestedLaunches, repeatReads, gateFlags sum

	effort := map[string]bool{}
	sessions := map[string]bool{}
	opened := map[string]bool{}
	closed := map[string]bool{}
	fired := map[InterventionCount]int{}

	var excludedDelegation bool

	for _, r := range records {
		if r.InstanceID != instanceID {
			continue
		}
		if r.SessionID != "" {
			sessions[r.SessionID] = true
		}
		if r.EffortLevel != "" {
			effort[r.EffortLevel] = true
		}
		if m.Formula == "" {
			m.Formula = r.Formula
		}
		if m.Agent == "" {
			m.Agent = r.Agent
		}

		switch r.Event {
		case EventInstanceStart:
			m.StartedAt, m.Model = r.TS, r.Model
			m.AFCommit, m.CheckoutCommit = r.AFCommit, r.CheckoutCommit
			m.SlingDigest, m.TokenomicsState = r.SlingDigest, r.TokenomicsState
			m.FormulaDigestStart = r.FormulaDigest
		case EventInstanceEnd:
			m.Completed = true
			m.FormulaDigestEnd, m.BaseCommit = r.FormulaDigest, r.BaseCommit
		case EventStepStart:
			opened[stepKey(r)] = true
		case EventIntervention:
			fired[InterventionCount{Mechanism: r.Mechanism, Objective: r.Objective}]++
		case EventStepEnd:
			closed[stepKey(r)] = true
			m.StepsClosed++
			if m.HostVersion == "" {
				m.HostVersion = r.HostVersion
			}
			switch r.Status {
			case StatusClosed:
				m.StatusClosed++
			case StatusSkipped:
				m.StatusSkipped++
			case StatusGateWaiting:
				m.StatusGateWaiting++
			}
			// A step is MEASURED when it recorded what the model generated. The shortfall against
			// StepsClosed is why both are reported: GenerationUnmeasuredReason names it per step,
			// and an arm whose steps mostly went unmeasured is not comparable to one whose did not.
			if r.OutTokens != nil {
				m.MeasuredSteps++
			}
			// C-9 is decided PER CLOSED STEP, not from run-wide folds (#679 F2/T2). A run is
			// excluded if ANY closed step delegated and recorded no sub-agent tokens; deciding it
			// from the run-wide `subagent.recorded` lets a sibling step that measured its own
			// delegation launder a step whose delegated spend was never seen. The key is
			// SubagentTokens == nil — a recorded 0 is a measured absence of spend, not the missing
			// measurement absence is.
			if launchCount(r.SubagentLaunches)+launchCount(r.WorkflowLaunches) >= 1 && r.SubagentTokens == nil {
				excludedDelegation = true
			}
			out.add(r.OutTokens)
			subagent.add(r.SubagentTokens)
			think.add(r.ThinkTokens)
			thinkEst.add(r.ThinkTokensEst)
			in.add(r.InTokens)
			cacheRead.add(r.CacheReadTokens)
			cacheCreation.add(r.CacheCreationTokens)
			subagentLaunches.add(r.SubagentLaunches)
			workflowLaunches.add(r.WorkflowLaunches)
			nestedLaunches.add(r.SubagentNestedLaunches)
			repeatReads.add(r.RepeatReads)
			gateFlags.add(r.GateFlags)
		}
	}

	m.OutTokens, m.SubagentTokens = out.value(), subagent.value()
	m.ThinkTokens, m.ThinkTokensEst = think.value(), thinkEst.value()
	m.InTokens, m.CacheReadTokens = in.value(), cacheRead.value()
	m.CacheCreationTokens = cacheCreation.value()
	m.SubagentLaunches, m.WorkflowLaunches = subagentLaunches.value(), workflowLaunches.value()
	m.SubagentNestedLaunches, m.RepeatReads = nestedLaunches.value(), repeatReads.value()
	m.GateFlags = gateFlags.value()

	m.Metric = out.total + subagent.total
	m.Sessions = len(sessions)
	for key := range opened {
		if !closed[key] {
			m.Interrupted++
		}
	}
	for level := range effort {
		m.EffortLevels = append(m.EffortLevels, level)
	}
	sort.Strings(m.EffortLevels)
	for key, count := range fired {
		key.Count = count
		m.Interventions = append(m.Interventions, key)
	}
	sort.Slice(m.Interventions, func(i, j int) bool {
		if m.Interventions[i].Mechanism != m.Interventions[j].Mechanism {
			return m.Interventions[i].Mechanism < m.Interventions[j].Mechanism
		}
		return m.Interventions[i].Objective < m.Interventions[j].Objective
	})

	if excludedDelegation {
		m.ExcludedReason = ExcludedUnmeasuredDelegation
	}
	return m
}

// launchCount reads a launch figure as its value, treating a nil pointer as zero launches. An absent
// count is zero launches, not the missing-measurement absence an absent token figure is.
func launchCount(v *int64) int64 {
	if v == nil {
		return 0
	}
	return *v
}

// stepKey identifies a step within one run. StepID is the per-instance bead id and is the right
// join key; StepLabel is the fallback for a record written before an id existed, and pairing an
// opening record with its close on nothing at all would count every run as interrupted.
func stepKey(r StepEvent) string {
	if r.StepID != "" {
		return r.StepID
	}
	return r.StepLabel
}

// sum accumulates one figure across a run's closed steps while remembering whether ANY step
// recorded it. The pair is the whole point: a total of zero from twelve steps that each recorded
// zero, and a total of zero from twelve steps that recorded nothing, are different runs.
type sum struct {
	total    int64
	recorded bool
}

func (s *sum) add(v *int64) {
	if v == nil {
		return
	}
	s.total += *v
	s.recorded = true
}

func (s sum) value() *int64 {
	if !s.recorded {
		return nil
	}
	total := s.total
	return &total
}
