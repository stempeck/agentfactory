package tokenomics

import (
	"math"
	"testing"
)

// The timestamp every fold in this file is stamped with. It is a constant rather than a clock read
// because that is exactly the property the aggregation must have: #668 K6 folds samples, it does
// not know what time it is.
const foldedAt = "2026-08-30T09:15:00.000Z"

// scalar returns a pointer to a recorded figure. Every K4 scalar is optional at the record level,
// and the distinction the pointer preserves — measured as zero versus never measured at all — is
// the one the whole fold turns on.
func scalar(v int64) *int64 { return &v }

// peaks builds one sample per recorded peak occupancy against a single key: the shape a real
// formula step produces run after run, and the shape most of the assertions below need.
func peaks(k DigestKey, values ...int64) []StepSample {
	out := make([]StepSample, 0, len(values))
	for _, v := range values {
		out = append(out, StepSample{Key: k, PeakCtxTokens: scalar(v), Sessions: 1})
	}
	return out
}

// marginals builds one sample per [start, peak] pair against a single key. Unlike peaks it also
// sets CtxTokensStart, so the marginal appetite (peak − start) is defined for the sample rather
// than absent: the shape the additive predicate's fix reads, where the appetite is the step's
// MARGINAL growth and not its absolute peak.
func marginals(k DigestKey, pairs ...[2]int64) []StepSample {
	out := make([]StepSample, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, StepSample{Key: k, CtxTokensStart: scalar(p[0]), PeakCtxTokens: scalar(p[1]), Sessions: 1})
	}
	return out
}

func TestDigestAggregation(t *testing.T) {
	// This test fails against: an aggregation that computes a mean, where one pathological run sets
	// the expectation for every later run; an even-sample tie-break that averages or rounds, which
	// no rebuild could reproduce byte-equivalently unless every reader agreed on the rounding; a
	// fold whose answer depends on encounter order, for the same reason; a fold that treats an
	// unmeasured scalar as a zero, turning "nobody measured this" into "this cost nothing"; and one
	// that stamps its own clock instead of carrying the caller's timestamp.

	k := DigestKey{Formula: "design-v7", StepID: "P3", Model: "fable-5"}

	t.Run("the median ignores the pathological run a mean cannot", func(t *testing.T) {
		a := AggregateSamples(peaks(k, 40_000, 45_000, 50_000, 55_000, 373_416), foldedAt)

		if a.Runs != 5 {
			t.Fatalf("Runs = %d, want 5", a.Runs)
		}
		if a.MedianPeakCtxTokens != 50_000 {
			t.Errorf("MedianPeakCtxTokens = %d, want 50000", a.MedianPeakCtxTokens)
		}
		// 563416/5. Named so a fold that averaged could not pass on a set where the two agreed.
		if a.MedianPeakCtxTokens == 112_683 {
			t.Errorf("MedianPeakCtxTokens is the mean of the sample, not its median")
		}
		if a.MaxPeakCtxTokens != 373_416 {
			t.Errorf("MaxPeakCtxTokens = %d, want 373416 — the tail is carried, just not obeyed", a.MaxPeakCtxTokens)
		}
	})

	t.Run("an even sample takes the higher of the two middle values", func(t *testing.T) {
		a := AggregateSamples(peaks(k, 10_000, 20_000, 30_000, 40_000), foldedAt)

		if a.MedianPeakCtxTokens != 30_000 {
			t.Errorf("MedianPeakCtxTokens = %d, want 30000", a.MedianPeakCtxTokens)
		}
	})

	t.Run("the fold is order-independent", func(t *testing.T) {
		ascending := AggregateSamples(peaks(k, 10_000, 20_000, 30_000, 40_000, 50_000), foldedAt)
		shuffled := AggregateSamples(peaks(k, 30_000, 50_000, 10_000, 40_000, 20_000), foldedAt)

		if ascending != shuffled {
			t.Fatalf("the fold depends on encounter order: %+v vs %+v", ascending, shuffled)
		}
		if ascending.MedianPeakCtxTokens != 30_000 {
			t.Fatalf("MedianPeakCtxTokens = %d, want 30000; two empty aggregates would compare equal and prove nothing", ascending.MedianPeakCtxTokens)
		}
	})

	t.Run("an unmeasured figure is skipped, never folded in as a zero", func(t *testing.T) {
		a := AggregateSamples([]StepSample{
			{Key: k, Sessions: 1},
			{Key: k, PeakCtxTokens: scalar(90_000), Sessions: 1},
			{Key: k, Sessions: 1},
		}, foldedAt)

		if a.Runs != 3 {
			t.Errorf("Runs = %d, want 3 — every record is a run, measured or not", a.Runs)
		}
		if a.MedianPeakCtxTokens != 90_000 {
			t.Errorf("MedianPeakCtxTokens = %d, want 90000; a nil-as-zero fold reports 0", a.MedianPeakCtxTokens)
		}
	})

	t.Run("every K6 figure is folded from its own stored scalar", func(t *testing.T) {
		a := AggregateSamples([]StepSample{
			{Key: k, PeakCtxTokens: scalar(80_000), CumTokensDelta: scalar(30_000), DurationMS: scalar(1_000), Sessions: 1},
			{Key: k, PeakCtxTokens: scalar(90_000), CumTokensDelta: scalar(40_000), DurationMS: scalar(2_000), Sessions: 2},
			{Key: k, PeakCtxTokens: scalar(95_000), CumTokensDelta: scalar(90_000), DurationMS: scalar(3_000), Sessions: 1},
		}, foldedAt)

		if a.MedianCumTokensDelta != 40_000 {
			t.Errorf("MedianCumTokensDelta = %d, want 40000", a.MedianCumTokensDelta)
		}
		if a.MaxCumTokensDelta != 90_000 {
			t.Errorf("MaxCumTokensDelta = %d, want 90000", a.MaxCumTokensDelta)
		}
		if a.MedianDurationMS != 2_000 {
			t.Errorf("MedianDurationMS = %d, want 2000", a.MedianDurationMS)
		}
		if a.SessionsPerStep != 1 {
			t.Errorf("SessionsPerStep = %d, want 1 — the median run took one session", a.SessionsPerStep)
		}
	})

	t.Run("a key with no measurement at all reads as unknown, which is observation-only", func(t *testing.T) {
		d := NewDigest()
		d.Put(k, AggregateSamples([]StepSample{{Key: k, Sessions: 1}, {Key: k, Sessions: 1}}, foldedAt))

		app := d.AppetiteFor(k)
		if app.Known {
			t.Fatalf("appetite = %+v, want unknown", app)
		}
		got := Admit(Window{Tokens: 200_000, Source: "declared"}, Occupancy{Tokens: 10_000, Known: true}, app, onPolicy(10, 3))
		if got.Verdict != VerdictObserve || got.Reason != ReasonNoLearnedData {
			t.Errorf("Admit = %+v, want %s / %q", got, VerdictObserve, ReasonNoLearnedData)
		}
	})

	t.Run("thinking share is built from the stored scalars alone", func(t *testing.T) {
		// No transcript is reachable from this test, which is the point: a transcript expires while
		// the record summarising it persists, so the estimate must come from the stored figures.
		a := AggregateSamples([]StepSample{
			{Key: k, OutTokens: scalar(1_000), ThinkTokensEst: scalar(100), Sessions: 1},
			{Key: k, OutTokens: scalar(1_000), ThinkTokensEst: scalar(200), Sessions: 1},
			{Key: k, OutTokens: scalar(1_000), ThinkTokensEst: scalar(900), Sessions: 1},
			{Key: k, ThinkTokensEst: scalar(400), Sessions: 1},
			{Key: k, OutTokens: scalar(0), ThinkTokensEst: scalar(400), Sessions: 1},
		}, foldedAt)

		if a.ThinkingShare < 0.199 || a.ThinkingShare > 0.201 {
			t.Errorf("ThinkingShare = %v, want 0.2 — the mean of the same set is 0.4", a.ThinkingShare)
		}
		if a.Runs != 5 {
			t.Errorf("Runs = %d, want 5 — a run with no out_tokens is still a run", a.Runs)
		}
	})

	t.Run("the fold carries the caller's timestamp and holds no clock", func(t *testing.T) {
		const stamp = "2020-01-01T00:00:00.000Z"

		if a := AggregateSamples(peaks(k, 1_000), stamp); a.UpdatedAt != stamp {
			t.Errorf("UpdatedAt = %q, want %q", a.UpdatedAt, stamp)
		}
		d := BuildDigest(peaks(k, 1_000), stamp)
		a, ok := d.Lookup(k)
		if !ok {
			t.Fatalf("BuildDigest produced no entry for %+v; the assertion below would prove nothing", k)
		}
		if a.UpdatedAt != stamp {
			t.Errorf("BuildDigest UpdatedAt = %q, want %q", a.UpdatedAt, stamp)
		}
	})

	t.Run("a learned value stays inside the clamps before it can drive a decision", func(t *testing.T) {
		d := NewDigest()
		d.Put(k, AggregateSamples(peaks(k, 500_000), foldedAt))

		app := d.PeakAppetiteFor(k)
		if !app.Known {
			t.Fatalf("appetite = %+v, want known; an unknown appetite would clamp trivially", app)
		}
		if got := ClampAppetite(app.Tokens, 200_000); got != 200_000 {
			t.Errorf("ClampAppetite(%d, 200000) = %d, want 200000", app.Tokens, got)
		}
		if got := ClampAppetite(-1, 200_000); got != 0 {
			t.Errorf("ClampAppetite(-1, 200000) = %d, want 0", got)
		}
	})

	t.Run("every formula name accumulates its own history, including ones nobody special-cased", func(t *testing.T) {
		// The empty string, a unicode name and a near-miss of a shipped name. Each must reach its
		// own row: the formula name is a join key here, never a branch.
		names := []string{"", "設計-v7", "design-v7 ", "design-v7"}

		var samples []StepSample
		for i, n := range names {
			samples = append(samples, peaks(DigestKey{Formula: n, StepID: "P3", Model: "fable-5"}, int64(10_000*(i+1)))...)
		}
		d := BuildDigest(samples, foldedAt)

		if len(d.Entries) != len(names) {
			t.Fatalf("digest holds %d entries, want %d — two names collapsed onto one key", len(d.Entries), len(names))
		}
		if d.V != DigestVersion {
			t.Errorf("digest V = %d, want %d", d.V, DigestVersion)
		}
		for i, n := range names {
			a, ok := d.Lookup(DigestKey{Formula: n, StepID: "P3", Model: "fable-5"})
			if !ok {
				t.Fatalf("no entry for formula %q", n)
			}
			if want := int64(10_000 * (i + 1)); a.MedianPeakCtxTokens != want {
				t.Errorf("formula %q: MedianPeakCtxTokens = %d, want %d", n, a.MedianPeakCtxTokens, want)
			}
		}
	})

	// The merge is what makes the digest a standing cache rather than a view of the current log.
	// Every assertion below fails against a writer that simply overwrites, which is the shape that
	// hands back the rotation horizon the cache exists to cross.
	aged := DigestKey{Formula: "design-v7", StepID: "P1", Model: "fable-5"}

	t.Run("a row the records can no longer prove is carried forward", func(t *testing.T) {
		stored := BuildDigest(peaks(aged, 80_000, 90_000, 100_000), foldedAt)
		fresh := BuildDigest(peaks(k, 20_000), "2026-08-30T10:00:00.000Z")

		merged := MergeDigests(stored, fresh)
		if Coverage(merged) != 2 {
			t.Fatalf("merged digest holds %d rows, want 2 — the carried row and the fresh one", Coverage(merged))
		}
		carried, ok := merged.Lookup(aged)
		if !ok {
			t.Fatal("the stored row is gone; the cache forgot what its records no longer hold")
		}
		if carried.Runs != 3 || carried.MedianPeakCtxTokens != 90_000 {
			t.Errorf("carried = %+v, want the stored aggregate untouched", carried)
		}
	})

	t.Run("the derivation wins while the records still support it", func(t *testing.T) {
		// Steady state: every run the digest knows about is still on disk, so the fresh side has
		// at least as many runs and the merge is the identity. This is what keeps a rebuild
		// byte-equivalent to what the close wrote.
		stored := BuildDigest(peaks(k, 40_000, 50_000), foldedAt)
		fresh := BuildDigest(peaks(k, 40_000, 50_000, 60_000), "2026-08-30T10:00:00.000Z")

		got, ok := MergeDigests(stored, fresh).Lookup(k)
		if !ok {
			t.Fatal("the merged digest lost the key both sides hold")
		}
		want, _ := fresh.Lookup(k)
		if got != want {
			t.Errorf("merged = %+v, want the fresh aggregate %+v", got, want)
		}
	})

	t.Run("an equal run count goes to the fresh side so the stamp keeps moving", func(t *testing.T) {
		stored := BuildDigest(peaks(k, 40_000, 50_000), foldedAt)
		fresh := BuildDigest(peaks(k, 40_000, 50_000), "2026-08-30T10:00:00.000Z")

		got, _ := MergeDigests(stored, fresh).Lookup(k)
		if got.UpdatedAt != "2026-08-30T10:00:00.000Z" {
			t.Errorf("UpdatedAt = %q, want the fresh stamp — a cache that never restamps cannot be aged out", got.UpdatedAt)
		}
	})

	t.Run("a thinner derivation never overwrites the better-supported row", func(t *testing.T) {
		// What a partial read looks like: one agent's log unreadable, or a generation gone. The
		// thinner answer is not more correct for being newer, and taking it would let a transient
		// read failure quietly discard a hundred runs of history.
		stored := BuildDigest(peaks(k, 80_000, 90_000, 100_000), foldedAt)
		fresh := BuildDigest(peaks(k, 10_000), "2026-08-30T10:00:00.000Z")

		got, _ := MergeDigests(stored, fresh).Lookup(k)
		if got.Runs != 3 || got.MedianPeakCtxTokens != 90_000 {
			t.Errorf("merged = %+v, want the three-run aggregate kept", got)
		}
	})

	t.Run("merging onto nothing is the derivation itself", func(t *testing.T) {
		// The from-scratch rebuild: the operator deleted the file, so there is nothing to carry
		// and the merge must not invent anything.
		fresh := BuildDigest(peaks(k, 40_000, 50_000, 60_000), foldedAt)

		merged := MergeDigests(NewDigest(), fresh)
		if Coverage(merged) != Coverage(fresh) {
			t.Fatalf("merged holds %d rows, derived holds %d", Coverage(merged), Coverage(fresh))
		}
		got, _ := merged.Lookup(k)
		want, _ := fresh.Lookup(k)
		if got != want {
			t.Errorf("merged = %+v, want %+v", got, want)
		}
		if merged.V != DigestVersion {
			t.Errorf("merged V = %d, want %d", merged.V, DigestVersion)
		}
	})
}

func TestCrossProfileIndependent(t *testing.T) {
	// This test fails against an aggregation that drops the model leg from the join key: one
	// backend's history would then answer for another's, which is the failure the leg exists to
	// prevent. A local 30B profile and a frontier profile have appetites that differ by more than
	// the margin any policy could absorb, so the two figures are set far apart — an implementation
	// that returned either row for both keys cannot pass by coincidence.

	fable := DigestKey{Formula: "design-v7", StepID: "P3", Model: "fable-5"}
	local := DigestKey{Formula: "design-v7", StepID: "P3", Model: "lmstudio/qwen3-coder-30b"}

	samples := append(peaks(fable, 40_000, 45_000, 50_000), peaks(local, 150_000, 160_000, 170_000)...)
	d := BuildDigest(samples, foldedAt)

	if len(d.Entries) != 2 {
		t.Fatalf("digest holds %d entries, want 2 — the model leg was dropped", len(d.Entries))
	}

	fableApp, localApp := d.PeakAppetiteFor(fable), d.PeakAppetiteFor(local)
	if !fableApp.Known || !localApp.Known {
		t.Fatalf("appetites must both be known: fable=%+v local=%+v", fableApp, localApp)
	}
	if fableApp.Tokens != 45_000 {
		t.Errorf("fable-5 appetite = %d, want 45000", fableApp.Tokens)
	}
	if localApp.Tokens != 160_000 {
		t.Errorf("lmstudio appetite = %d, want 160000", localApp.Tokens)
	}
	if fableApp.Tokens == localApp.Tokens {
		t.Fatal("both profiles report the same appetite; the independence assertion proves nothing")
	}

	t.Run("the step leg is independent too", func(t *testing.T) {
		other := DigestKey{Formula: "design-v7", StepID: "P4", Model: "fable-5"}
		d := BuildDigest(append(peaks(fable, 45_000), peaks(other, 120_000)...), foldedAt)

		got := d.PeakAppetiteFor(other)
		if !got.Known || got.Tokens != 120_000 {
			t.Errorf("P4 appetite = %+v, want 120000 known", got)
		}
	})
}

func TestFragmentedBaseline(t *testing.T) {
	// H-R5. This test fails against: an appetite rule that reports unknown whenever a transcript
	// peak is missing, which would make the recycle-fragmented step — the measured pathology this
	// whole issue exists for — the one case the cache can never learn; a reconstruction built from
	// a single session's delta, which under-predicts by exactly the factor that caused the
	// fragmentation; and one that lets a reconstruction override a recorded peak.
	//
	// The error direction is deliberately asymmetric: over-prediction costs one cheap planned
	// handoff, under-prediction re-creates the pathology.

	k := DigestKey{Formula: "design-v7", StepID: "P3", Model: "fable-5"}

	// One recorded run. A delta is recorded only when the step opened and closed inside one
	// session, so the fragmented runs below carry a session count and no delta — which is the
	// fixture's whole point, and the reason a single-run fold cannot answer this question.
	run := func(sessions int, delta *int64) StepSample {
		return StepSample{Key: k, CumTokensDelta: delta, Sessions: sessions}
	}

	// The measured pathology: the median run of this step needs three sessions, and the only
	// per-session figures on record come from the runs that happened to fit in one.
	fragmentedHistory := func() []StepSample {
		return []StepSample{
			run(3, nil), run(3, nil), run(3, nil),
			run(1, scalar(60_000)), run(1, scalar(60_000)),
		}
	}

	t.Run("a fragmented baseline reconstructs sessions_per_step times the per-session delta", func(t *testing.T) {
		d := BuildDigest(fragmentedHistory(), foldedAt)

		a, ok := d.Lookup(k)
		if !ok {
			t.Fatalf("no entry for %+v", k)
		}
		if a.SessionsPerStep != 3 {
			t.Fatalf("SessionsPerStep = %d, want 3", a.SessionsPerStep)
		}
		if a.MedianCumTokensDelta != 60_000 {
			t.Fatalf("MedianCumTokensDelta = %d, want 60000 — folded only from the runs that recorded one", a.MedianCumTokensDelta)
		}
		if a.MedianPeakCtxTokens != 0 {
			t.Fatalf("MedianPeakCtxTokens = %d, want 0 — with a peak on record the reconstruction below is never reached", a.MedianPeakCtxTokens)
		}

		app := d.PeakAppetiteFor(k)
		if !app.Known {
			t.Fatalf("appetite = %+v, want known — a fragmented step must still yield an appetite", app)
		}
		if app.Tokens != 180_000 {
			t.Errorf("appetite = %d, want 180000 (3 x 60000)", app.Tokens)
		}
		if app.Tokens < a.MedianCumTokensDelta {
			t.Errorf("appetite %d is below one session's delta %d — under-prediction re-creates the pathology", app.Tokens, a.MedianCumTokensDelta)
		}
		if !app.Reconstructed {
			t.Error("appetite does not report itself as reconstructed; a later phase cannot tell an inference from a measurement")
		}
	})

	t.Run("the transcript-derived peak wins when it exists", func(t *testing.T) {
		samples := fragmentedHistory()
		for i := range samples {
			samples[i].PeakCtxTokens = scalar(120_000)
		}
		d := BuildDigest(samples, foldedAt)

		app := d.PeakAppetiteFor(k)
		if !app.Known || app.Tokens != 120_000 {
			t.Errorf("appetite = %+v, want 120000 known — the recorded peak, not the 180000 reconstruction", app)
		}
		if app.Reconstructed {
			t.Error("a recorded peak was reported as reconstructed")
		}
	})

	t.Run("a single-session step with no peak stays unknown", func(t *testing.T) {
		d := BuildDigest([]StepSample{
			run(1, scalar(55_000)), run(1, scalar(60_000)), run(1, scalar(70_000)),
		}, foldedAt)

		if app := d.PeakAppetiteFor(k); app.Known {
			t.Errorf("appetite = %+v, want unknown — one session that recorded no peak is an absence, not a synthesized number", app)
		}
	})

	t.Run("the session count travels with the answer as a no-fit signal", func(t *testing.T) {
		samples := []StepSample{run(2, nil), run(2, nil), run(2, nil)}
		for i := range samples {
			samples[i].PeakCtxTokens = scalar(90_000)
		}
		d := BuildDigest(samples, foldedAt)

		app := d.PeakAppetiteFor(k)
		if app.SessionsPerStep != 2 {
			t.Errorf("appetite SessionsPerStep = %d, want 2 — an open-time decision must read the session count without re-deriving it", app.SessionsPerStep)
		}
		if app.Runs != 3 {
			t.Errorf("appetite Runs = %d, want 3", app.Runs)
		}
	})

	t.Run("an unmeasured session count is not a fragmented baseline", func(t *testing.T) {
		d := BuildDigest([]StepSample{
			run(0, scalar(55_000)), run(0, scalar(60_000)), run(0, scalar(70_000)),
		}, foldedAt)

		if app := d.PeakAppetiteFor(k); app.Known {
			t.Errorf("appetite = %+v, want unknown — a record that never reported a session count cannot be multiplied by one", app)
		}
	})

	t.Run("runs that reported no session count do not drag the count down", func(t *testing.T) {
		// The mixed set is the case the guard exists for, and the only one that distinguishes it:
		// a step whose older runs predate the session-count derivation and whose newer ones report
		// three. Fold the absences in as zeroes and the median is 0, so H-R5 declines to
		// reconstruct on exactly the fragmented step it was written for.
		d := BuildDigest([]StepSample{
			run(0, scalar(60_000)), run(0, scalar(60_000)), run(3, nil),
		}, foldedAt)

		a, ok := d.Lookup(k)
		if !ok {
			t.Fatalf("no entry for %+v", k)
		}
		if a.Runs != 3 {
			t.Errorf("Runs = %d, want 3 — a run without a session count is still a run", a.Runs)
		}
		if a.SessionsPerStep != 3 {
			t.Fatalf("SessionsPerStep = %d, want 3 — folded only from the runs that reported one", a.SessionsPerStep)
		}
		if app := d.PeakAppetiteFor(k); !app.Known || app.Tokens != 180_000 {
			t.Errorf("appetite = %+v, want 180000 known — the reconstruction H-R5 exists to make", app)
		}
	})

	t.Run("a reconstruction that would overflow saturates rather than wrapping", func(t *testing.T) {
		// Both operands come off a file on disk, so nothing this process did bounds them. A wrapped
		// product lands negative, a negative appetite clamps to zero downstream, and "this step
		// needs nothing" admits everything — the exact inversion of H-R5's chosen error direction.
		d := BuildDigest([]StepSample{
			run(2, scalar(math.MaxInt64)), run(2, scalar(math.MaxInt64)), run(2, scalar(math.MaxInt64)),
		}, foldedAt)

		app := d.PeakAppetiteFor(k)
		if !app.Known {
			t.Fatalf("appetite = %+v, want known", app)
		}
		if app.Tokens != math.MaxInt64 {
			t.Errorf("appetite = %d, want %d — saturated, not wrapped", app.Tokens, int64(math.MaxInt64))
		}
		if ClampAppetite(app.Tokens, 200_000) != 200_000 {
			t.Error("the saturated appetite does not clamp to the window; a negative product would have clamped to zero instead")
		}
	})
}

// generation builds one sample per [out, think] pair against a single key, in the arm given. It is
// the shape the efficiency baseline reads: the occupancy helpers above set neither leg, so a fold
// that mixed the two figure families could not pass both.
func generation(k DigestKey, obj Objective, pairs ...[2]int64) []StepSample {
	out := make([]StepSample, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, StepSample{
			Key: k, Objective: obj, Sessions: 1,
			OutTokens: scalar(p[0]), ThinkTokens: scalar(p[1]),
		})
	}
	return out
}

// TestGenerationBaselineSplitsTheArms is the #678 K2 fold: the control arm and the efficiency arm
// are aggregated separately, so the guard that asks whether reduced effort actually paid has two
// numbers to compare rather than one number containing both.
func TestGenerationBaselineSplitsTheArms(t *testing.T) {
	k := DigestKey{Formula: "design-v7", StepID: "P3", Model: "fable-5"}

	t.Run("capacity-reduced runs enter neither arm; efficiency runs are the reduced arm", func(t *testing.T) {
		// #679 F9 reconcile: this used three ObjectiveCapacity samples as the baseline. A
		// capacity-reduced run had its effort cut for capacity pressure, not by the efficiency
		// actuator, so it is not a control the efficiency arm is measured against — folding it into
		// the baseline inflated the number the efficiency guard trusts. The three capacity samples
		// now count toward neither arm; only the two efficiency samples form the reduced arm, and the
		// baseline is empty.
		samples := append(
			generation(k, ObjectiveCapacity, [2]int64{1_000, 900}, [2]int64{1_000, 900}, [2]int64{1_000, 900}),
			generation(k, ObjectiveEfficiency, [2]int64{200, 10}, [2]int64{200, 10})...,
		)
		a := AggregateSamples(samples, foldedAt)

		if a.Runs != 5 {
			t.Fatalf("Runs = %d, want 5 — every run is history whatever arm it ran under", a.Runs)
		}
		if a.GenerationRuns != 0 {
			t.Errorf("GenerationRuns = %d, want 0 — capacity samples are not the baseline", a.GenerationRuns)
		}
		if a.MedianOutTokens != 0 || a.MedianThinkTokens != 0 {
			t.Errorf("baseline = %d out / %d think, want 0/0 — capacity samples enter no baseline median",
				a.MedianOutTokens, a.MedianThinkTokens)
		}
		if a.ReducedRuns != 2 || a.ReducedMedianOutTokens != 200 {
			t.Errorf("reduced arm = %d runs / %d out, want 2/200", a.ReducedRuns, a.ReducedMedianOutTokens)
		}
	})

	t.Run("a run that measured one leg does not count toward generation trust", func(t *testing.T) {
		// The upgrade case: out tokens have been recorded since #668, the exact thinking count only
		// since #678 K1. Every historical run looks like this one.
		half := []StepSample{{Key: k, Sessions: 1, OutTokens: scalar(1_000)}}
		a := AggregateSamples(half, foldedAt)

		if a.GenerationRuns != 0 {
			t.Errorf("GenerationRuns = %d, want 0 — a share needs both legs", a.GenerationRuns)
		}
		if a.MedianOutTokens != 1_000 {
			t.Errorf("MedianOutTokens = %d, want 1000 — each median folds from its OWN sample set", a.MedianOutTokens)
		}
	})

	t.Run("the occupancy figures fold every arm", func(t *testing.T) {
		// How much room a step needs is a fact about the step, not about the effort it ran at, so the
		// arm split must stop at the generation legs.
		samples := []StepSample{
			{Key: k, Sessions: 1, PeakCtxTokens: scalar(40_000), Objective: ObjectiveCapacity},
			{Key: k, Sessions: 1, PeakCtxTokens: scalar(50_000), Objective: ObjectiveEfficiency},
			{Key: k, Sessions: 1, PeakCtxTokens: scalar(60_000), Objective: ObjectiveEfficiency},
		}
		if a := AggregateSamples(samples, foldedAt); a.MedianPeakCtxTokens != 50_000 {
			t.Errorf("MedianPeakCtxTokens = %d, want 50000 over all three runs", a.MedianPeakCtxTokens)
		}
	})
}

// TestDigestPartitionByFormulaDigest is the other half of #678 K2: a step's baseline describes the
// program the step runs NOW. Editing the formula changes what the step costs, and a baseline that
// keeps averaging in runs of the previous text predicts for a program that no longer exists.
func TestDigestPartitionByFormulaDigest(t *testing.T) {
	k := DigestKey{Formula: "design-v7", StepID: "P3", Model: "fable-5"}

	stamped := func(digest, startedAt string, peak int64) StepSample {
		return StepSample{
			Key: k, Sessions: 1, PeakCtxTokens: scalar(peak),
			FormulaDigest: digest, InstanceStartedAt: startedAt,
		}
	}

	t.Run("the superseded program's runs leave the baseline", func(t *testing.T) {
		d := BuildDigest([]StepSample{
			stamped("old", "2026-08-01T00:00:00.000Z", 200_000),
			stamped("old", "2026-08-02T00:00:00.000Z", 200_000),
			stamped("old", "2026-08-03T00:00:00.000Z", 200_000),
			stamped("new", "2026-08-04T00:00:00.000Z", 40_000),
			stamped("new", "2026-08-05T00:00:00.000Z", 50_000),
		}, foldedAt)

		a, ok := d.Lookup(k)
		if !ok {
			t.Fatal("no entry for the key; the partition dropped it entirely")
		}
		if a.Runs != 2 || a.MedianPeakCtxTokens != 50_000 {
			t.Errorf("aggregate = %d runs / %d median, want 2/50000 — the three runs of the old "+
				"program outnumber the new ones, so a fold that kept them reports 200000",
				a.Runs, a.MedianPeakCtxTokens)
		}
		if a.FormulaDigest != "new" {
			t.Errorf("FormulaDigest = %q, want %q — the row must name the program it describes", a.FormulaDigest, "new")
		}
	})

	t.Run("the winner is the most recently STARTED program, not the most frequent", func(t *testing.T) {
		d := BuildDigest([]StepSample{
			stamped("new", "2026-08-05T00:00:00.000Z", 40_000),
			stamped("old", "2026-08-01T00:00:00.000Z", 200_000),
			stamped("old", "2026-08-02T00:00:00.000Z", 200_000),
		}, foldedAt)

		if a, _ := d.Lookup(k); a.Runs != 1 || a.FormulaDigest != "new" {
			t.Errorf("aggregate = %d runs of %q, want 1 run of %q — a majority of the old program is "+
				"still the old program", a.Runs, a.FormulaDigest, "new")
		}
	})

	t.Run("runs with no recorded digest are kept whatever the winner is", func(t *testing.T) {
		// The upgrade case, and the reason the wildcard is load-bearing: formula_digest is joined from
		// a record kind that has carried it only recently, so on every existing factory the great
		// majority of a key's history has none. Reading absent as "its own program" would discard that
		// history the first time one run carried a digest.
		d := BuildDigest([]StepSample{
			stamped("", "", 40_000),
			stamped("", "", 50_000),
			stamped("", "", 60_000),
			stamped("new", "2026-08-05T00:00:00.000Z", 55_000),
		}, foldedAt)

		if a, _ := d.Lookup(k); a.Runs != 4 {
			t.Errorf("Runs = %d, want 4 — three runs of learned history were discarded", a.Runs)
		}
	})

	t.Run("a key with no digests anywhere is untouched", func(t *testing.T) {
		if a, _ := BuildDigest(peaks(k, 40_000, 50_000, 60_000), foldedAt).Lookup(k); a.Runs != 3 {
			t.Errorf("Runs = %d, want 3 — the partition must be inert on a log that records no digest", a.Runs)
		}
	})

	t.Run("a different program replaces the stored row however thin it is", func(t *testing.T) {
		stored := BuildDigest([]StepSample{
			stamped("old", "2026-08-01T00:00:00.000Z", 200_000),
			stamped("old", "2026-08-02T00:00:00.000Z", 200_000),
			stamped("old", "2026-08-03T00:00:00.000Z", 200_000),
		}, foldedAt)
		fresh := BuildDigest([]StepSample{stamped("new", "2026-08-05T00:00:00.000Z", 40_000)}, foldedAt)

		got, _ := MergeDigests(stored, fresh).Lookup(k)
		if got.FormulaDigest != "new" || got.Runs != 1 {
			t.Errorf("merged = %d runs of %q, want 1 run of %q — more-runs-wins may only decide "+
				"between two rows describing the SAME program", got.Runs, got.FormulaDigest, "new")
		}
	})

	t.Run("an unknown digest on either side is not evidence of a different program", func(t *testing.T) {
		// The first rebuild after upgrade: the stored row was written before the leg existed. Reading
		// its empty digest as "differs from everything" would discard the whole cache at once.
		stored := BuildDigest(peaks(k, 80_000, 90_000, 100_000), foldedAt)
		fresh := BuildDigest([]StepSample{stamped("new", "2026-08-05T00:00:00.000Z", 40_000)}, foldedAt)

		if got, _ := MergeDigests(stored, fresh).Lookup(k); got.Runs != 3 {
			t.Errorf("merged Runs = %d, want the carried 3", got.Runs)
		}
	})
}

// TestCapacityReducedLeavesNeitherArm pins that a capacity-reduced sample (Objective ==
// ObjectiveCapacity) belongs in NEITHER arm of the efficiency comparison: it is not the control the
// reduced arm is measured against, and it is not itself a reduced run — its effort was cut for CAPACITY
// reasons, so folding it into the baseline would inflate the very trust floor the efficiency guard
// reads. It is the inversion of TestGenerationBaselineSplitsTheArms: the same three capacity samples
// must count toward nothing — no baseline runs or medians, and no reduced arm either.
func TestCapacityReducedLeavesNeitherArm(t *testing.T) {
	k := DigestKey{Formula: "design-v7", StepID: "P3", Model: "fable-5"}

	samples := generation(k, ObjectiveCapacity,
		[2]int64{1_000, 900}, [2]int64{1_000, 900}, [2]int64{1_000, 900})
	a := AggregateSamples(samples, foldedAt)

	if a.GenerationRuns != 0 {
		t.Errorf("GenerationRuns = %d, want 0 — a capacity-reduced run is excluded from the baseline "+
			"trust floor entirely", a.GenerationRuns)
	}
	if a.MedianOutTokens != 0 || a.MedianThinkTokens != 0 {
		t.Errorf("baseline = %d out / %d think, want 0/0 — capacity samples must not enter the baseline "+
			"medians", a.MedianOutTokens, a.MedianThinkTokens)
	}
	if a.ReducedRuns != 0 || a.ReducedMedianOutTokens != 0 {
		t.Errorf("reduced arm = %d runs / %d out, want 0/0 — capacity is not the efficiency reduced arm "+
			"either; it belongs to neither arm", a.ReducedRuns, a.ReducedMedianOutTokens)
	}
}
