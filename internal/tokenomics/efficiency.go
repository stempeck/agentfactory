package tokenomics

// Objective is why a run was configured the way it was, and it is the leg that makes the digest's
// two arms readable (#678 K2). The effort level a run used cannot say it on its own: a run at
// "medium" on a host whose default is medium is a control run, and a run at "medium" because the
// efficiency actuator chose it is a treatment run, and a baseline that folded them together would be
// comparing the experiment against itself.
//
// The vocabulary is declared here rather than imported from the record layer because the import edge
// runs telemetry → tokenomics: this package must never know what a record looks like. The record
// layer maps its own spelling onto these.
type Objective string

const (
	ObjectiveCapacity   Objective = "capacity"
	ObjectiveEfficiency Objective = "efficiency"
)

// Why an efficiency plan warrants no reduction. Empty when one is warranted, for the reason
// predicate.go gives about its own conclusions: a reason string on a plan that fires would invite a
// caller to branch on prose.
const (
	ReasonNoGenerationBaseline = "no generation baseline for this step"
	ReasonNoGain               = "thinking share below efficiency_thinking_share_pct"
	ReasonQualityGuard         = "the reduced arm spent less but tripped more gates"
)

// EfficiencyInputs is the arithmetic the plan rests on, carried so a read surface can show its
// working without re-deriving it and reaching a different answer.
//
// Every field is a plain scalar so EfficiencyPlan stays comparable — the purity test asserts two
// calls are identical with ==, and one map or slice in here would quietly turn that into a pointer
// comparison. All fields are zero on a plan that returned before any arithmetic ran.
type EfficiencyInputs struct {
	PriorRuns   int
	ReducedRuns int

	MedianOutTokens      int64
	MedianThinkTokens    int64
	MedianSubagentTokens int64
	MedianRepeatReads    int64

	// ThinkingSharePct is exact integer arithmetic — MedianThinkTokens*100/MedianOutTokens — and not a
	// rounded float. A float here would make the threshold comparison depend on a rounding rule, and
	// two surfaces rounding differently would disagree about whether the same step fires.
	ThinkingSharePct int
	SessionsPerStep  int
}

// EfficiencyPlan is what a step's learned history warrants. It is advice, not an action: nothing in
// this package fires, and the verb layer decides what to do with each field.
type EfficiencyPlan struct {
	// EffortLevel is the level to launch the next run of this step at, and empty means no reduction is
	// warranted — the host's own default stands. It is empty on every plan that carries a Reason.
	EffortLevel string

	// CleanStart asks for a relaunch at the step boundary, because the step historically spanned more
	// than one main session and carrying the previous step's transcript into it is the cost this
	// issue exists to remove. ThriftCounsel asks the prime surface to render the thrift template.
	CleanStart    bool
	ThriftCounsel bool

	Inputs EfficiencyInputs
	Reason string
}

// Efficiency reports what a step's learned generation history warrants, and it is the frame-lift the
// whole of #678 turns on (AC-2).
//
// Read the parameter list: an aggregate, whether that aggregate was found, and the resolved policy.
// There is no context-size reading, no live-fullness reading, and no pool operand — and that ABSENCE
// is the acceptance criterion, not an omission from it. The effort actuator this replaces was keyed
// on scarcity (free < projected), so on a roomy cloud profile it was arithmetically inert and token
// efficiency was conditional on running out of room. A roomy profile cannot switch this predicate
// off, because the function never learns how much room there is.
//
// The trust floor reads a.GenerationRuns and never a.Runs. They differ exactly where it matters: a
// key with a hundred recorded runs whose history predates the generation legs has measured no share
// at all, and trusting it on its occupancy history would be reading one measurement as evidence for
// another.
//
// The order of the guards is the order of the questions. Whether the operator switched the surface
// off comes before whether there is data, which comes before whether the data is trusted, which
// comes before what the data says — so a caller rendering Reason gets the FIRST thing that stopped
// the plan rather than the last.
func Efficiency(a Aggregate, found bool, p Policy) EfficiencyPlan {
	if !p.EfficiencyOn {
		return EfficiencyPlan{Reason: ReasonMechanismOff}
	}
	if !found {
		return EfficiencyPlan{Reason: ReasonNoLearnedData}
	}
	if a.GenerationRuns < p.LearnedMinRuns {
		return EfficiencyPlan{Reason: ReasonBelowMinRuns}
	}
	// A zero median is UNMEASURED, never a step that generated nothing — the same rule the band judge
	// applies at band.go:132. Reading it as zero would make the share below either a division by zero
	// or a confident 0 %, and both would answer for a step nobody has measured.
	if a.MedianOutTokens <= 0 || a.MedianThinkTokens <= 0 {
		return EfficiencyPlan{Reason: ReasonNoGenerationBaseline}
	}

	plan := EfficiencyPlan{
		CleanStart:    a.SessionsPerStep > 1,
		ThriftCounsel: a.MedianRepeatReads >= int64(p.EfficiencyRepeatReadFloor),
		Inputs: EfficiencyInputs{
			PriorRuns:            a.GenerationRuns,
			ReducedRuns:          a.ReducedRuns,
			MedianOutTokens:      a.MedianOutTokens,
			MedianThinkTokens:    a.MedianThinkTokens,
			MedianSubagentTokens: a.MedianSubagentTokens,
			MedianRepeatReads:    a.MedianRepeatReads,
			ThinkingSharePct:     int(a.MedianThinkTokens * 100 / a.MedianOutTokens),
			SessionsPerStep:      a.SessionsPerStep,
		},
	}

	// The counsel above is warranted by history alone and survives both declines below: a step that
	// spans two sessions still wants a clean boundary, and a step that re-reads the same files still
	// wants the thrift template, whether or not its generation is worth reducing.
	if plan.Inputs.ThinkingSharePct < p.EfficiencyThinkingSharePct {
		plan.Reason = ReasonNoGain
		return plan
	}
	if !reductionStillPays(a, p) {
		plan.Reason = ReasonQualityGuard
		return plan
	}
	plan.EffortLevel = p.EfficiencyEffortLevel
	return plan
}

// reductionStillPays asks whether the reduced arm has earned the right to continue.
//
// Below the learned floor the arm is still on TRIAL and is admitted on no evidence at all, because
// there is no other way for evidence to accumulate: a guard that demanded proof before allowing the
// first reduced run would never allow one, and the experiment could never start.
//
// Past the floor it must show its work. Fewer output tokens is the gain, and no more tripped quality
// gates is the price — a run that spent less while failing more bought nothing, and continuing to
// pay for it is worse than never having started. Both comparisons run against the baseline arm, which
// AggregateSamples keeps disjoint from this one.
//
// A reduced median of zero is unmeasured rather than free, so it is not evidence of a gain. Reading
// it as one would make every arm that recorded no output look like a total saving.
func reductionStillPays(a Aggregate, p Policy) bool {
	if a.ReducedRuns < p.LearnedMinRuns {
		return true
	}
	if a.ReducedMedianOutTokens <= 0 {
		return false
	}
	return a.ReducedMedianOutTokens < a.MedianOutTokens && a.ReducedMedianGateFlags <= a.MedianGateFlags
}
