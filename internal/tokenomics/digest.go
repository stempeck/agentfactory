package tokenomics

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// DigestVersion rides on every encoded digest. The digest is a rebuildable cache, so a reader that
// meets a shape it does not speak can discard rather than migrate — but only if it can TELL, which
// is what the version is for.
const DigestVersion = 1

// The key encoding. digestKeySeparator is a unit separator and digestKeyEscape an escape byte:
// neither occurs in a real formula name, step id or model name, so the escaping below is inert in
// practice. It is here anyway because without it the encoding is not injective — ("a<US>b", "c")
// and ("a", "b<US>c") would collapse onto one key — and a join key that two different triples can
// share is a cache that answers for the wrong step.
const (
	digestKeySeparator = "\x1f"
	digestKeyEscape    = "\x1b"
)

// DigestKey is the join key for learned data: which formula, which step, which model.
//
// The formula name appears HERE and nowhere else in this package. That placement is AC-2 stated in
// the type system — a key is looked up, never branched on — and it is what lets a formula nobody
// special-cased accumulate its own history with no author change.
//
// The model leg is not decoration: the same step against a different backend has a different
// appetite, and a key that dropped the leg would predict one backend's needs from another's.
type DigestKey struct {
	Formula string
	StepID  string
	Model   string
}

func (k DigestKey) String() string {
	return escapeKeyLeg(k.Formula) + digestKeySeparator +
		escapeKeyLeg(k.StepID) + digestKeySeparator +
		escapeKeyLeg(k.Model)
}

func escapeKeyLeg(leg string) string {
	leg = strings.ReplaceAll(leg, digestKeyEscape, digestKeyEscape+digestKeyEscape)
	return strings.ReplaceAll(leg, digestKeySeparator, digestKeyEscape+digestKeySeparator)
}

// Aggregate is what one key has historically cost. Medians rather than means because a single
// pathological run — a retry storm, a runaway read — would drag a mean into a prediction nothing
// resembles, and the max is carried alongside so a later phase can reason about the tail without
// letting it set the expectation.
//
// It is comparable, so a round-trip test can assert equality without reaching for reflect. That
// constraint is also why every K6 figure below is a plain scalar and none of them carries the
// sample set it came from: a median cannot be maintained incrementally, so the cache stores the
// answer and the store keeps the evidence.
//
// SessionsPerStep is the median run's session count, and it earns its place next to the token
// figures by being two things at once — the multiplier H-R5's reconstruction needs, and, above 1,
// a no-fit signal in its own right. ThinkingShare is a share indicator rather than an accounting
// figure, for the reason StepEvent.ThinkTokensEst records: the estimate's bias is known.
type Aggregate struct {
	Runs                    int     `json:"runs"`
	MedianPeakCtxTokens     int64   `json:"median_peak_ctx_tokens"`
	MedianMarginalCtxTokens int64   `json:"median_marginal_ctx_tokens,omitempty"`
	MaxPeakCtxTokens        int64   `json:"max_peak_ctx_tokens"`
	MedianCumTokensDelta    int64   `json:"median_cum_tokens_delta"`
	MaxCumTokensDelta       int64   `json:"max_cum_tokens_delta"`
	MedianDurationMS        int64   `json:"median_duration_ms"`
	SessionsPerStep         int     `json:"sessions_per_step"`
	ThinkingShare           float64 `json:"thinking_share"`

	// The generation baseline (#678 K2). Every figure above is an OCCUPANCY figure — how full the
	// window got — and on a roomy window they are all comfortable, which is why a cache built from
	// them alone cannot tell a step that generates enormously from one that generates nothing. These
	// are what a step COST to produce rather than how much room it took up, and they are what the
	// efficiency predicate decides on.
	//
	// GenerationRuns counts the runs that recorded BOTH out and exact think tokens, and the
	// efficiency trust floor is applied to it and never to Runs (cross-review HIGH-1). A key with
	// forty occupancy runs and no generation history is not a key whose generation is well
	// understood, and keying the floor on Runs would trust exactly that.
	//
	// Every figure is omitempty and DigestVersion is unchanged, so a cache written before this phase
	// decodes with zeroes and re-encodes byte-identically. A zero median reads as unmeasured, never
	// as a measured nothing — the same rule band.go applies to the occupancy medians.
	GenerationRuns         int    `json:"generation_runs,omitempty"`
	MedianOutTokens        int64  `json:"median_out_tokens,omitempty"`
	MedianThinkTokens      int64  `json:"median_think_tokens,omitempty"`
	MedianSubagentTokens   int64  `json:"median_subagent_tokens,omitempty"`
	MedianSubagentLaunches int64  `json:"median_subagent_launches,omitempty"`
	MedianRepeatReads      int64  `json:"median_repeat_reads,omitempty"`
	MedianGateFlags        int64  `json:"median_gate_flags,omitempty"`
	FormulaDigest          string `json:"formula_digest,omitempty"`

	// The treatment arm: the same two figures folded over the runs that actually ran reduced. They
	// are what lets the predicate ask whether the reduction paid, rather than assume it. Empty until
	// a step has run under an efficiency-selected level at least once, which is why the predicate
	// treats a thin arm as still on trial rather than as evidence against.
	ReducedRuns            int   `json:"reduced_runs,omitempty"`
	ReducedMedianOutTokens int64 `json:"reduced_median_out_tokens,omitempty"`
	ReducedMedianGateFlags int64 `json:"reduced_median_gate_flags,omitempty"`

	UpdatedAt string `json:"updated_at"`
}

// Digest is the standing learned-data cache. UpdatedAt is a string rather than a time.Time because
// this package holds no clock (#519): the writer stamps it, and everything here only carries it.
type Digest struct {
	V       int                  `json:"v"`
	Entries map[string]Aggregate `json:"entries"`
}

// NewDigest returns an empty digest whose Entries map is initialised, so an encoded empty digest
// marshals "entries" as {} rather than null and a consumer can range over it unconditionally.
func NewDigest() Digest {
	return Digest{V: DigestVersion, Entries: map[string]Aggregate{}}
}

func (d Digest) Lookup(k DigestKey) (Aggregate, bool) {
	a, ok := d.Entries[k.String()]
	return a, ok
}

func (d Digest) Put(k DigestKey, a Aggregate) {
	d.Entries[k.String()] = a
}

// AppetiteFor is the digest's job in the ADDITIVE decision path: answer how much this key's step has
// historically GROWN — the marginal (peak − start) median — with the sample size attached so the
// predicate can apply its own trust floor rather than have one applied for it here.
//
// The additive predicate adds this to the CURRENT occupancy, and that occupancy already carries the
// step's baseline. Returning the absolute peak here — which includes that baseline — would count the
// baseline a second time, the double-count thread T1 exists to remove. freshFits, whose session
// starts empty, needs the absolute peak instead and reads PeakAppetiteFor for it.
//
// A missing marginal is UNKNOWN, not a fall back to the absolute peak. An old on-disk digest written
// before the marginal was folded, or a step that always fragments and so records no single-pass peak
// to subtract a start from, degrades to occupancy-only rather than silently reintroducing the exact
// double-count on precisely the cache entries no one inspects. Zero would be worse still — a
// prediction that the step grows by nothing, which admits every step ever observed. Runs and
// SessionsPerStep travel regardless of the arm, so a step observed repeatedly-not-to-fit still
// reports as what it is rather than as a number invented for it.
func (d Digest) AppetiteFor(k DigestKey) Appetite {
	a, ok := d.Lookup(k)
	if !ok {
		return Appetite{}
	}
	app := Appetite{Runs: a.Runs, SessionsPerStep: a.SessionsPerStep}
	if a.MedianMarginalCtxTokens > 0 {
		app.Tokens, app.Known = a.MedianMarginalCtxTokens, true
	}
	return app
}

// PeakAppetiteFor answers the ABSOLUTE-peak appetite: the whole single-pass footprint a step needs
// in a session that starts empty. freshFits reads it (tokenomics_admission.go), because a fresh
// session's real cost is baseline + marginal = the absolute peak; the recycle question "would this
// step fit a session that had just started" is asked against the peak, never the marginal growth
// AppetiteFor returns for the additive decision.
//
// A recorded aggregate with no peak is reported as UNKNOWN, not as zero. Zero would be a prediction
// that the step needs nothing, which would admit every step that has ever been observed — the
// failure mode of treating an absence as a measurement.
//
// The peak is the transcript-derived single-pass appetite and wins whenever it exists. H-R5 is the
// second rule, and it exists because the steps with no peak on record are not a random sample:
// they are precisely the steps that spanned a session recycle, so no single transcript covers
// them. Where only fragmented per-session figures survive, the appetite is reconstructed as the
// session count times the median per-session delta — deliberately the over-predicting direction,
// because over-prediction costs one cheap planned handoff and under-prediction re-creates the
// pathology.
//
// What the reconstruction can and cannot reach is worth stating plainly, because the boundary is
// not where it first appears. The recorder refuses to attribute a delta across a session boundary,
// so a run that fragmented contributes no per-session figure at all. The multiplier therefore comes
// from the step's fragmented runs and the per-session cost from its unfragmented ones: a step that
// usually needs a recycle but has occasionally fitted in one pass gets a reconstructed appetite,
// and a step that has NEVER once fitted stays unknown, because nothing in the store says what one
// of its sessions costs. That is not silence about it. Runs and SessionsPerStep are filled in
// regardless of which arm answered, so a step that always fragments still travels as what it is —
// observed, repeatedly, not to fit — which is a true statement about it rather than a number
// invented for it.
//
// A single-session step with no peak stays unknown. Multiplying one session's delta by one would
// dress the same absence up as a measurement without adding anything to it.
func (d Digest) PeakAppetiteFor(k DigestKey) Appetite {
	a, ok := d.Lookup(k)
	if !ok {
		return Appetite{}
	}
	app := Appetite{Runs: a.Runs, SessionsPerStep: a.SessionsPerStep}
	switch {
	case a.MedianPeakCtxTokens > 0:
		app.Tokens, app.Known = a.MedianPeakCtxTokens, true
	case a.SessionsPerStep > 1 && a.MedianCumTokensDelta > 0:
		app.Tokens, app.Known, app.Reconstructed = reconstructAppetite(a.SessionsPerStep, a.MedianCumTokensDelta), true, true
	}
	return app
}

// reconstructAppetite is H-R5's multiplication, saturating rather than wrapping.
//
// Both operands come off a file on disk, so neither is bounded by anything this process did. A
// product that overflowed would land NEGATIVE, and a negative appetite clamps to zero downstream —
// "this step needs nothing", which admits everything. Saturating keeps the error on the
// over-predicting side, which is the side H-R5 chose.
//
// sessions is above 1 and perSession above 0 at the only call site, so the division is total.
func reconstructAppetite(sessions int, perSession int64) int64 {
	if perSession > math.MaxInt64/int64(sessions) {
		return math.MaxInt64
	}
	return int64(sessions) * perSession
}

// Coverage is how many keys the digest can answer for. Both the rebuild verb and `af tokenomics
// status` report it, and a zero from either is a measured zero: status pairs it with a reason
// naming the cold start, because a bare zero would read the same as a factory that had learned
// nothing and one that had never been asked.
func Coverage(d Digest) int { return len(d.Entries) }

// CoverageJoinEligible counts only the keys a learned read can actually join — those whose Runs has
// reached the trust floor minRuns. `af tokenomics status` uses it (not Coverage) so a digest full of
// single-run keys no read will ever hit reports as structurally cold rather than as rising coverage.
func CoverageJoinEligible(d Digest, minRuns int) int {
	n := 0
	for _, a := range d.Entries {
		if a.Runs >= minRuns {
			n++
		}
	}
	return n
}

// EncodeDigest produces the on-disk bytes. The output is deterministic for a given digest —
// encoding/json sorts map keys — which matters because this is a cache rewritten on a hot path: an
// encoder whose bytes varied between identical inputs would make every rebuild look like a change.
func EncodeDigest(d Digest) ([]byte, error) {
	if d.Entries == nil {
		d.Entries = map[string]Aggregate{}
	}
	d.V = DigestVersion
	return json.Marshal(d)
}

func DecodeDigest(data []byte) (Digest, error) {
	var d Digest
	if err := json.Unmarshal(data, &d); err != nil {
		return Digest{}, fmt.Errorf("parsing tokenomics digest: %w", err)
	}
	if d.V != DigestVersion {
		return Digest{}, fmt.Errorf("tokenomics digest is version %d, this binary speaks %d", d.V, DigestVersion)
	}
	if d.Entries == nil {
		d.Entries = map[string]Aggregate{}
	}
	return d, nil
}

// LoadDigest reads a digest from an INJECTED path. This package resolves no factory root and knows
// no filename convention; the verb layer, which already owns cwd-to-root resolution, supplies both.
//
// An absent file is the cold-start state every factory begins in, so it is an empty digest and a
// nil error. A file that exists and cannot be read is NOT: a cache that has been corrupted must say
// so rather than masquerade as a cold start, because the two lead to opposite operator actions.
func LoadDigest(path string) (Digest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return NewDigest(), nil
		}
		return Digest{}, fmt.Errorf("reading tokenomics digest: %w", err)
	}
	return DecodeDigest(data)
}

// SaveDigest writes the digest atomically. The digest is read by a later step of the same run that
// writes it, so a reader must never see a half-written file — the guarantee fsutil.WriteFileAtomic
// exists to give.
func SaveDigest(path string, d Digest) error {
	data, err := EncodeDigest(d)
	if err != nil {
		return fmt.Errorf("encoding tokenomics digest: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating tokenomics digest dir: %w", err)
	}
	return fsutil.WriteFileAtomic(path, data, 0o644)
}
