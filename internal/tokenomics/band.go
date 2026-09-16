package tokenomics

import "math"

// #668 K10 — "within baselines" as a computed verdict rather than an opinion.
//
// The model is deriveStepContext (internal/cmd/telemetry_context_read.go:24-26): every verdict is
// recomputed on every read, from the raw figures alone, and none is ever written onto a record. A
// band verdict is the strongest case for that rule in the whole feature, because the thing being
// compared against MOVES — the median is a cache that learns — so a verdict stored beside a run
// would go on asserting a comparison nobody can reproduce, against a band that no longer exists.
//
// Pure in this package's sense (#519): no clock, no environment, no filesystem. The caller brings
// the digest and the observations, which is what makes the whole matrix table-testable.

// The band verdict vocabulary. It is a SEPARATE field from consumption_state and shares only the
// word "unmeasurable" with it, which means the same thing on both: there was nothing to compare.
// The two pass/fail spellings are band-specific on purpose — "over occupancy" is a judgement
// against a configured bound, and this is a judgement against a learned history.
const (
	BandWithin       = "within_baselines"
	BandOutside      = "outside_baselines"
	BandNoBaseline   = "no_baseline"
	BandUnmeasurable = "unmeasurable"
)

// The tolerances, stated as named constants and DIFFERENT per figure, because the three quantities
// do not vary alike. Peak occupancy is the tightest: it is the figure the admission predicate
// divides with, so a run that drifts on it changes what the harness will admit next time. A
// cumulative delta absorbs one extra tool result and moves more. Duration is the loosest by a wide
// margin — it is dominated by backend latency, which is a property of the host on the day, not of
// the step — and a duration band tight enough to be interesting would fire on every busy afternoon.
const (
	BandPeakTolerancePct     = 20
	BandDeltaTolerancePct    = 25
	BandDurationTolerancePct = 50
)

// The figure names, which are join keys for a consumer and therefore part of the contract. They
// match the record field names they are observed from rather than inventing report-side spellings.
const (
	BandFigurePeakCtxTokens  = "peak_ctx_tokens"
	BandFigureCumTokensDelta = "cum_tokens_delta"
	BandFigureDurationMS     = "duration_ms"

	BandFigureOutTokens      = "out_tokens"
	BandFigureSubagentTokens = "subagent_tokens"
	BandFigureThinkTokens    = "think_tokens"
)

// The direction vocabulary (#678 K9), a SEPARATE leg from the verdict and not a widening of it.
//
// The verdict answers "is this run normal", and the four words that answer it are load-bearing on
// two surfaces already. Efficiency asks a question the verdict cannot carry: a run that generated
// half the baseline's tokens and a run that generated double are both outside_baselines, byte for
// byte, so a win reads exactly like a regression. Direction is the only leg that separates them,
// and it is stated as its own field rather than as two new verdict words so that every consumer of
// the existing vocabulary keeps working unchanged.
//
// The empty string is the fourth member and means "no direction": a figure with no band under it
// has nothing to be above or below, and a direction there would be unfalsifiable.
const (
	BandDirectionBelow  = "below"
	BandDirectionWithin = "within"
	BandDirectionAbove  = "above"
)

// Observation is one measured run of one key, in the shape the band compares.
//
// The pointers are the record's pointers, kept, for StepSample's reason: a run that recorded no
// peak and a run that peaked at nothing are different facts, and a nil here yields "unmeasurable"
// where a zero would yield a confident "outside baselines" against a median nobody missed.
type Observation struct {
	Key DigestKey

	PeakCtxTokens  *int64
	CumTokensDelta *int64
	DurationMS     *int64

	OutTokens      *int64
	SubagentTokens *int64
	ThinkTokens    *int64
}

// BandFigure is one figure's verdict together with everything the verdict rested on. The band is
// carried, not just the answer: "outside baselines" with no median and no tolerance beside it is a
// number an operator has to take on trust, and the whole point of computing this rather than
// asserting it is that the arithmetic is visible.
type BandFigure struct {
	Name         string
	Observed     *int64
	Median       int64
	TolerancePct int
	Low          int64
	High         int64
	Verdict      string
	Direction    string
}

// BandRow is one (formula, step, model) key's verdict across every figure.
type BandRow struct {
	Key     DigestKey
	Runs    int
	Figures []BandFigure
	Verdict string
}

// JudgeBand compares one observed run against what the digest has learned for its key.
//
// minRuns is the caller's Policy.LearnedMinRuns. An aggregate resting on fewer runs than that is
// reported as no_baseline rather than judged, which is the same clamp the admission predicate
// applies to the same median — a band drawn around a single sample would make the first run of a
// step define correctness for every run after it.
func JudgeBand(d Digest, obs Observation, minRuns int) BandRow {
	row := BandRow{Key: obs.Key}

	a, found := d.Lookup(obs.Key)
	row.Runs = a.Runs
	trusted := found && a.Runs > 0 && a.Runs >= minRuns

	for _, f := range []struct {
		name     string
		observed *int64
		median   int64
		tolPct   int
	}{
		{BandFigurePeakCtxTokens, obs.PeakCtxTokens, a.MedianPeakCtxTokens, BandPeakTolerancePct},
		{BandFigureCumTokensDelta, obs.CumTokensDelta, a.MedianCumTokensDelta, BandDeltaTolerancePct},
		{BandFigureDurationMS, obs.DurationMS, a.MedianDurationMS, BandDurationTolerancePct},
		// The generation figures share the delta tolerance rather than getting a fourth constant:
		// they are token deltas over one step, which is the quantity BandDeltaTolerancePct was
		// chosen for, and a tolerance invented per figure would be an opinion with no history
		// behind it. They are appended rather than interleaved so the occupancy figures keep their
		// rendered positions on both surfaces.
		{BandFigureOutTokens, obs.OutTokens, a.MedianOutTokens, BandDeltaTolerancePct},
		{BandFigureSubagentTokens, obs.SubagentTokens, a.MedianSubagentTokens, BandDeltaTolerancePct},
		{BandFigureThinkTokens, obs.ThinkTokens, a.MedianThinkTokens, BandDeltaTolerancePct},
	} {
		row.Figures = append(row.Figures, judgeBandFigure(f.name, f.observed, f.median, f.tolPct, trusted))
	}
	row.Verdict = rollUpBand(row.Figures)
	return row
}

// judgeBandFigure is the whole arithmetic, in integers.
//
// Integer arithmetic and not float, deliberately: two readers of the same digest must draw the same
// edge, and a band edge that depended on floating-point rounding would put a run inside on one
// machine and outside on another — the failure a computed verdict exists to remove.
//
// A median of zero is no_baseline rather than a band of [0,0]. The aggregate's figures are plain
// scalars, so zero is the only spelling it has for "nothing was measured" (aggregate.go:40-42), and
// judging a real observation against it would report every measured run as outside a band that was
// never drawn.
func judgeBandFigure(name string, observed *int64, median int64, tolPct int, trusted bool) BandFigure {
	f := BandFigure{
		Name:         name,
		Observed:     observed,
		TolerancePct: tolPct,
		Verdict:      BandNoBaseline,
	}
	// The median is attached only once it is trusted. A no_baseline figure carrying the aggregate's
	// median beside a zero band would hand a consumer a number the verdict next to it says is not
	// usable — and the human path already suppresses it, so the two surfaces would disagree about
	// what the same row reports.
	if !trusted || median <= 0 {
		return f
	}
	f.Median = median

	// Multiply-then-divide, because divide-first loses the whole band on a small median: 50 tokens at
	// 20% would yield a delta of 0 and a band of [50,50], reporting every neighbouring run as outside.
	// Both steps saturate, in reconstructAppetite's shape (digest.go:145-157) and for its reason: the
	// median comes off a file on disk, so nothing this process did bounds it, and a wrapped product
	// or a wrapped sum lands NEGATIVE — Low above High, every observation "outside", from the
	// quietest possible input. Saturating keeps the band wide, which errs toward calling a strange
	// run normal rather than toward calling every normal run strange.
	delta := median / 100 * int64(tolPct)
	if median <= math.MaxInt64/100 {
		delta = median * int64(tolPct) / 100
	}
	f.Low = median - delta
	f.High = math.MaxInt64
	if delta <= math.MaxInt64-median {
		f.High = median + delta
	}

	if observed == nil {
		f.Verdict = BandUnmeasurable
		return f
	}
	if *observed < f.Low {
		f.Verdict, f.Direction = BandOutside, BandDirectionBelow
		return f
	}
	if *observed > f.High {
		f.Verdict, f.Direction = BandOutside, BandDirectionAbove
		return f
	}
	f.Verdict, f.Direction = BandWithin, BandDirectionWithin
	return f
}

// rollUpBand reduces a row's figures to one verdict, worst-first.
//
// One figure outside its band makes the row outside: a step that held its peak and doubled its
// consumption has drifted, and a roll-up that averaged the two would report it as fine. Below that
// the order is "judged beats unjudged", so a row with one real comparison is reported as compared.
func rollUpBand(figures []BandFigure) string {
	var outside, within, unmeasurable int
	for _, f := range figures {
		switch f.Verdict {
		case BandOutside:
			outside++
		case BandWithin:
			within++
		case BandUnmeasurable:
			unmeasurable++
		}
	}
	switch {
	case outside > 0:
		return BandOutside
	case within > 0:
		return BandWithin
	case unmeasurable > 0:
		return BandUnmeasurable
	default:
		return BandNoBaseline
	}
}
