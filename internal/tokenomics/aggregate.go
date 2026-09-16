package tokenomics

import (
	"cmp"
	"slices"
)

// StepSample is one recorded run of one key, reduced to the scalars the cache aggregates.
//
// It is a tokenomics type rather than a telemetry one because the import edge runs
// telemetry → tokenomics: the store knows what a digest is, and this package must never know what
// a record looks like. The record layer maps its own fields onto this shape.
//
// The pointers are the record's pointers, kept. A run that recorded no peak and a run that peaked
// at nothing are different facts, and folding an absence in as a zero would turn "nobody measured
// this" into "this cost nothing" — the one error direction a capacity cache must never take.
type StepSample struct {
	Key DigestKey

	PeakCtxTokens  *int64
	CtxTokensStart *int64
	CumTokensDelta *int64
	DurationMS     *int64
	OutTokens      *int64
	ThinkTokensEst *int64

	// The generation legs (#678 K2). ThinkTokens is the host's EXACT count and is a different field
	// from ThinkTokensEst rather than a better value for it: the estimate's bias is known and its
	// divisor is pinned, so replacing it in place would move every historical share silently. A run
	// on a host that cannot report thinking carries nil here and the estimate there, and the two
	// answer different questions for the rest of time.
	ThinkTokens      *int64
	SubagentTokens   *int64
	SubagentLaunches *int64
	RepeatReads      *int64
	GateFlags        *int64

	// Sessions is how many sessions this one run of the step spanned. Below 1 means the run
	// reported no session count at all, which is an absence rather than a run that used none.
	Sessions int

	// EffortLevel is the arm this run RAN under and Objective is why. The pair is needed because the
	// level alone cannot say it: a run at "medium" on a host whose default is medium is a control
	// run, and one at "medium" because the efficiency actuator selected it is a treatment run, and
	// folding those two together would compare the experiment against itself.
	EffortLevel string
	Objective   Objective

	// FormulaDigest is the hash of the program this run executed, and InstanceStartedAt is when that
	// program started. Both come from the run's opening record rather than its closing one, because
	// that is the only record either lives on.
	//
	// The stamp is carried as the opaque string the record wrote and is compared lexically, never
	// parsed: the layout is fixed-width and lexically ordered, and this package holds no clock (#519)
	// — a parse here would add a failure mode to an ordering that has an honest answer without one.
	FormulaDigest     string
	InstanceStartedAt string
}

// median returns the HIGHER of the two middle samples on an even-sized set, and the middle one on
// an odd-sized set — sorted[n/2] under both parities.
//
// One rule rather than two, and no rounding decision to make: the mean of the middle pair would
// need one, and any rounding a rebuild did not reproduce byte for byte would make the cache
// disagree with itself. Choosing the higher side is also the direction H-R5 argues for —
// over-prediction costs one cheap planned handoff, under-prediction re-creates the pathology this
// issue exists to fix.
//
// An empty set yields the zero value. The aggregate's fields are plain scalars, because it must
// stay comparable, so zero is the only spelling of "nothing was measured" the type has and every
// consumer already reads it that way.
func median[T cmp.Ordered](values []T) T {
	var zero T
	if len(values) == 0 {
		return zero
	}
	sorted := slices.Clone(values)
	slices.Sort(sorted)
	return sorted[len(sorted)/2]
}

func maxOf[T cmp.Ordered](values []T) T {
	var zero T
	if len(values) == 0 {
		return zero
	}
	return slices.Max(values)
}

// minOf has no caller in this package yet. It lands here with maxOf because the read surface that
// judges a measured run against its learned baseline (#678 K9) compares against the minimum of the
// before-arm, and a lone min written at that call site would be the second spelling of a fold this
// file already owns.
func minOf[T cmp.Ordered](values []T) T {
	var zero T
	if len(values) == 0 {
		return zero
	}
	return slices.Min(values)
}

func appendRecorded(dst []int64, v *int64) []int64 {
	if v == nil {
		return dst
	}
	return append(dst, *v)
}

// currentDigest reports the formula digest of the most recently STARTED instance among samples that
// carry one, and "" when none does.
//
// The stamps are compared as strings. They are fixed-width and lexically ordered, so the comparison
// is the same one a parse would make, and this package holds no clock to parse them against (#519).
//
// A sample that carries a digest but no stamp cannot win, which collapses the whole key back to ""
// rather than crowning an unordered candidate. That is the conservative direction: "" means the
// partition below keeps every run it has.
func currentDigest(samples []StepSample) string {
	var latest, current string
	for _, s := range samples {
		if s.FormulaDigest == "" || s.InstanceStartedAt <= latest {
			continue
		}
		latest, current = s.InstanceStartedAt, s.FormulaDigest
	}
	return current
}

// AggregateSamples folds every recorded run of ONE key into the aggregate the cache stores.
//
// Each figure is folded from its own sample set, and a run that recorded no figure is absent from
// that set rather than present as a zero. Runs therefore counts records while any given median may
// rest on fewer, which is the honest reporting: a key with six runs and one measured peak has a
// median peak, and it has it from one sample.
//
// The generation figures fold the BASELINE arm only, and the Reduced ones the efficiency arm (#678
// K2). Folding both together would compare the experiment against itself: the guard that asks
// whether a reduced-effort run spent fewer tokens has no question left to ask if the reduced runs
// are already inside the number it compares against. The occupancy figures above fold every sample
// whatever arm it ran under, because how much room a step needs is a fact about the step.
//
// updatedAt is a PARAMETER. This package holds no clock (#519), and the byte-equivalence a rebuild
// has to reproduce depends on that: two callers stamping their own time could never write the same
// file from the same records.
func AggregateSamples(samples []StepSample, updatedAt string) Aggregate {
	var (
		peaks     []int64
		marginals []int64
		deltas    []int64
		durations []int64
		sessions  []int
		shares    []float64

		generationRuns   int
		outs             []int64
		thinks           []int64
		subagentTokens   []int64
		subagentLaunches []int64
		repeatReads      []int64
		gateFlags        []int64

		reducedRuns      int
		reducedOuts      []int64
		reducedGateFlags []int64
	)
	for _, s := range samples {
		peaks = appendRecorded(peaks, s.PeakCtxTokens)
		// The marginal is the step's own growth, peak − start, and it needs BOTH pointers. A run that
		// recorded only one contributes no marginal sample rather than a figure resting on an assumed
		// zero. A non-positive marginal is measurement noise — peak and start are two provenances that
		// can drift — and is excluded at source so it cannot drag the median below the growth the step
		// really costs.
		if s.PeakCtxTokens != nil && s.CtxTokensStart != nil && *s.PeakCtxTokens > *s.CtxTokensStart {
			marginals = append(marginals, *s.PeakCtxTokens-*s.CtxTokensStart)
		}
		deltas = appendRecorded(deltas, s.CumTokensDelta)
		durations = appendRecorded(durations, s.DurationMS)
		if s.Sessions >= 1 {
			sessions = append(sessions, s.Sessions)
		}
		// A share needs both figures and a denominator. Out tokens at zero is not a run that
		// thought about nothing, it is a run whose generation was never counted.
		if s.OutTokens != nil && s.ThinkTokensEst != nil && *s.OutTokens > 0 {
			shares = append(shares, float64(*s.ThinkTokensEst)/float64(*s.OutTokens))
		}

		if s.Objective == ObjectiveCapacity {
			// A capacity-reduced run belongs to NEITHER arm (#679 F9). The reduced arm
			// feeds the guard that asks whether the EFFICIENCY actuator's reduction preserved quality,
			// so a run whose effort was cut for capacity pressure is not a sample of that question; and
			// folding it into the baseline would inflate the very number that guard trusts. It is
			// history — Runs above still counts it — but it enters no generation fold on either side.
			continue
		}
		if s.Objective == ObjectiveEfficiency {
			reducedRuns++
			reducedOuts = appendRecorded(reducedOuts, s.OutTokens)
			reducedGateFlags = appendRecorded(reducedGateFlags, s.GateFlags)
		} else {
			outs = appendRecorded(outs, s.OutTokens)
			thinks = appendRecorded(thinks, s.ThinkTokens)
			subagentTokens = appendRecorded(subagentTokens, s.SubagentTokens)
			subagentLaunches = appendRecorded(subagentLaunches, s.SubagentLaunches)
			repeatReads = appendRecorded(repeatReads, s.RepeatReads)
			gateFlags = appendRecorded(gateFlags, s.GateFlags)
			// GenerationRuns counts the runs that measured a SHARE, which needs both legs. It is the
			// trust floor the efficiency predicate reads instead of Runs, so a key whose whole history
			// predates the generation fields must count zero here however long that history is.
			if s.OutTokens != nil && s.ThinkTokens != nil {
				generationRuns++
			}
		}
	}

	return Aggregate{
		Runs:                    len(samples),
		MedianPeakCtxTokens:     median(peaks),
		MedianMarginalCtxTokens: median(marginals),
		MaxPeakCtxTokens:        maxOf(peaks),
		MedianCumTokensDelta:    median(deltas),
		MaxCumTokensDelta:       maxOf(deltas),
		MedianDurationMS:        median(durations),
		SessionsPerStep:         median(sessions),
		ThinkingShare:           median(shares),
		GenerationRuns:          generationRuns,
		MedianOutTokens:         median(outs),
		MedianThinkTokens:       median(thinks),
		MedianSubagentTokens:    median(subagentTokens),
		MedianSubagentLaunches:  median(subagentLaunches),
		MedianRepeatReads:       median(repeatReads),
		MedianGateFlags:         median(gateFlags),
		FormulaDigest:           currentDigest(samples),
		ReducedRuns:             reducedRuns,
		ReducedMedianOutTokens:  median(reducedOuts),
		ReducedMedianGateFlags:  median(reducedGateFlags),
		UpdatedAt:               updatedAt,
	}
}

// BuildDigest folds many keys' samples into one digest.
//
// Grouping is by the WHOLE DigestKey, through a map, and that is the design rather than an
// implementation detail: a key is looked up, never compared leg by leg, so a formula nobody
// special-cased accumulates its own history with no author change.
//
// Within a key, the fold is narrowed to the runs of the program that key is running NOW (#678 K2):
// editing a step's prompt changes what the step costs, and a baseline that keeps averaging in runs of
// the previous text answers for a program that no longer exists.
//
// Map iteration order does not reach the output. Each key's fold reads only its own samples, and
// the encoder sorts keys, so the bytes are the same whatever order the range produced.
func BuildDigest(samples []StepSample, updatedAt string) Digest {
	groups := map[DigestKey][]StepSample{}
	for _, s := range samples {
		groups[s.Key] = append(groups[s.Key], s)
	}

	d := NewDigest()
	for k, group := range groups {
		d.Put(k, AggregateSamples(currentProgram(group), updatedAt))
	}
	return d
}

// currentProgram drops the runs of a program this key has moved on from.
//
// A sample with NO digest is kept whatever the winner is, and that wildcard is load-bearing rather
// than lenient: the field is joined from a record kind that has only recently carried it, so on every
// factory in existence the overwhelming majority of a key's history predates it. Treating an absent
// digest as its own program would discard that history the first time one run carried a digest —
// silently regressing every learned baseline to a single run, and contradicting the carry-forward
// property the merge below exists to provide.
func currentProgram(samples []StepSample) []StepSample {
	current := currentDigest(samples)
	if current == "" {
		return samples
	}
	kept := make([]StepSample, 0, len(samples))
	for _, s := range samples {
		if s.FormulaDigest == "" || s.FormulaDigest == current {
			kept = append(kept, s)
		}
	}
	return kept
}

// MergeDigests folds a freshly derived digest onto the one already on disk, and is what makes the
// cache outlive the records it was built from.
//
// The store keeps ONE rotated generation, so a step's raw records eventually age out and a
// derivation from what survives has never heard of them. Re-deriving alone would therefore hand
// back the horizon problem the standing digest exists to fix: learned appetite would silently
// vanish at rotation and the step would regress to cold start with a full cache on disk. Carrying
// the stored row forward is the flywheel property.
//
// A key present in only one side is taken from that side; the stored-only case IS the carry-forward.
// Where both hold the key, the one resting on more runs wins, and a tie goes to the fresh side so
// the stamp keeps moving. Runs decides because it is the sample count each figure summarises, and
// the better-supported estimate is the one to keep.
//
// Runs only decides between two rows describing the SAME program (#678 K2). Where the two name
// different formula digests the fresh side wins outright however thin it is, because the stored row
// is not a better-supported estimate of this program — it is a well-supported estimate of a
// different one, and the step was just edited.
//
// The two are never blended field by field. Aggregate carries medians and no sample set — it must
// stay comparable — so there is no arithmetic that combines two of them into the aggregate of their
// union. A field-wise mix would pair one sample set's median peak with another's median duration and
// describe a run that never happened, which is worse than either input.
//
// Nothing here regresses a rebuild's reproducibility: while a formula's records survive intact the
// derived side has every run the stored side does, so it wins every key and the merge is the
// identity. Byte-equivalence and carry-forward diverge only past the rotation horizon, which is why
// the fixture that pins byte-equivalence is built from unrotated records.
func MergeDigests(stored, derived Digest) Digest {
	out := NewDigest()
	for k, a := range stored.Entries {
		out.Entries[k] = a
	}
	for k, fresh := range derived.Entries {
		if held, ok := out.Entries[k]; ok && !differentProgram(held, fresh) && held.Runs > fresh.Runs {
			continue
		}
		out.Entries[k] = fresh
	}
	return out
}

// differentProgram reports whether two rows for one key demonstrably describe different programs.
//
// An unknown digest on either side is NOT such a demonstration. Every row written before the digest
// leg existed carries "", and reading that as "differs from everything" would make the merge discard
// the entire stored cache on the first rebuild after upgrade — the carry-forward failure, arrived at
// from the other direction.
func differentProgram(held, fresh Aggregate) bool {
	return held.FormulaDigest != "" && fresh.FormulaDigest != "" && held.FormulaDigest != fresh.FormulaDigest
}
