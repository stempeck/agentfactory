// Package tokenomics is the pure decision core of the token-economics surface (#668 K5).
//
// It is pure in the internal/config sense (#519, internal/formula/discover.go:17-25): no clock, no
// environment reads, no cwd-to-root resolution, and the filesystem only through a path the caller
// injects. That is not a stylistic preference — it is what makes the whole decision matrix
// table-testable, and both of the design's hardest guarantees are claims about the table:
//
//   - Cloud-inertness (AC-4). The admission predicate divides by a resolved context window, so a
//     1M-class window cannot trip it. There is no profile classifier in this package and there
//     must not be one; inertness is arithmetic, not a special case.
//   - Generalization (AC-2). Nothing here takes formula CONTENT as input. A formula name is a join
//     key into the learned digest and never a branch, which is what lets a formula nobody
//     special-cased benefit with no author change.
//
// This package decides. It never acts: no mechanism fires from here, nothing is written except
// through SaveDigest, and the verb layer owns every side effect.
package tokenomics

import "github.com/stempeck/agentfactory/internal/config"

// Mechanism is one addressable token-economics behavior. The vocabulary is closed and matches the
// per-mechanism keys of config.TokenomicsConfig (api.md L47-62) minus the umbrella, because a
// mechanism that exists in one of those two places and not the other is a policy an operator can
// name but not switch.
type Mechanism string

const (
	MechanismBudget    Mechanism = "budget"
	MechanismThrift    Mechanism = "thrift"
	MechanismDispatch  Mechanism = "dispatch"
	MechanismInterview Mechanism = "interview"
	MechanismEffort    Mechanism = "effort"
	MechanismEscalate  Mechanism = "escalate"
)

// Mechanisms returns the vocabulary in its canonical order. Callers that render a list — the
// status payload, the human self-test — walk this rather than re-typing the names, for the reason
// config.tokenomicsEnums gives about its own single list: two copies drift, and the mechanism that
// drops out of one of them is invisible.
func Mechanisms() []Mechanism {
	return []Mechanism{
		MechanismBudget, MechanismThrift, MechanismDispatch,
		MechanismInterview, MechanismEffort, MechanismEscalate,
	}
}

// Policy is the resolved posture: every mechanism reduced to a bool, plus the two numeric operands
// the admission predicate reads. Resolution happens once, at the edge, so no decision site has to
// re-derive what "default" meant.
type Policy struct {
	on map[Mechanism]bool

	// AdmissionMarginPct is the headroom kept below the resolved window before a step is admitted,
	// and LearnedMinRuns is how many recorded runs an aggregate needs before its median is trusted
	// as a prediction. Both arrive clamped — see ResolvePolicy.
	AdmissionMarginPct int
	LearnedMinRuns     int

	// ContextThresholdPct is the exhaustion breaker's threshold (recovery.context_threshold_pct),
	// carried so a decision site can clamp its admission ceiling DOWN to the breaker — the AC-4
	// no-admit-then-die guarantee (D4). It lives in a DIFFERENT config struct than the tokenomics
	// block ResolvePolicy reads, so ResolvePolicy cannot set it; the decision site injects it with
	// WithContextThreshold. Zero means "not injected", which EffectiveAdmissionCeilingPct treats as
	// no clamp — the pre-clamp behavior, so a caller that never sets it is unchanged.
	ContextThresholdPct int

	// EfficiencyOn is the eighth switch and deliberately not a seventh Mechanism (#678 K3). Every
	// member of that vocabulary answers "does this step fit its window?"; efficiency answers "is
	// this step generating more than its work needs?", which is a question about learned history and
	// has no window in it. A caller walking Mechanisms() to render or resolve a capacity posture
	// must not pick this up, so it is a plain bool a caller has to name.
	EfficiencyOn bool

	// The four efficiency operands, clamped. EfficiencyEffortLevel is a host vocabulary value and is
	// the only one config validates by membership rather than by range, so it arrives already
	// checked and is carried rather than clamped.
	EfficiencyEffortLevel      string
	EfficiencyThinkingSharePct int
	EfficiencyRepeatReadFloor  int
	EfficiencyMaxRelaunches    int
}

func (p Policy) On(m Mechanism) bool { return p.on[m] }

// WithContextThreshold returns a copy carrying the exhaustion-breaker threshold. It is separate from
// ResolvePolicy because the breaker lives in recovery config, not the tokenomics block, so only the
// decision site (which loads startup config) can supply it. The `on` map is shared by reference and
// read-only after resolution, so the copy is safe.
func (p Policy) WithContextThreshold(pct int) Policy {
	p.ContextThresholdPct = pct
	return p
}

// Equal compares two resolved policies. Policy carries a map, so it is not comparable with ==, and
// a test that wants to assert "these two resolutions agree" needs this rather than reflect.DeepEqual
// — which would also report a nil map and an empty one as different, a distinction no caller has.
func (p Policy) Equal(other Policy) bool {
	if p.AdmissionMarginPct != other.AdmissionMarginPct || p.LearnedMinRuns != other.LearnedMinRuns ||
		p.ContextThresholdPct != other.ContextThresholdPct {
		return false
	}
	if p.EfficiencyOn != other.EfficiencyOn || p.EfficiencyEffortLevel != other.EfficiencyEffortLevel ||
		p.EfficiencyThinkingSharePct != other.EfficiencyThinkingSharePct ||
		p.EfficiencyRepeatReadFloor != other.EfficiencyRepeatReadFloor ||
		p.EfficiencyMaxRelaunches != other.EfficiencyMaxRelaunches {
		return false
	}
	for _, m := range Mechanisms() {
		if p.on[m] != other.on[m] {
			return false
		}
	}
	return true
}

// ResolvePolicy reduces the operator's tri-state configuration to a posture.
//
// umbrellaOn is passed in rather than read from cfg.Enabled because the umbrella has two inputs
// that live in different places: the enum in startup.json and the factory toggle file the verb
// layer owns. Only the caller can see both, and a resolver that consulted just one of them would
// silently ignore whichever the operator actually used.
//
// The umbrella is absolute: with it off, an explicit "on" on a mechanism still resolves off. That
// asymmetry is the point of having an umbrella at all — an operator switching the surface off must
// not have to also visit six keys.
//
// "default" resolves per mechanism (design-doc.md:137): the five advisory mechanisms come on with
// the umbrella; escalate does not, because it moves work to a different backend and that is a
// decision an operator makes deliberately rather than inherits. An EMPTY value resolves as
// "default" for the same reason config fills an absent block with the shipped defaults: this block
// is absent from every startup.json in existence, and a zero value means "not written", never
// "chosen off".
func ResolvePolicy(umbrellaOn bool, cfg config.TokenomicsConfig) Policy {
	p := Policy{
		on:                 map[Mechanism]bool{},
		AdmissionMarginPct: ClampMarginPct(cfg.AdmissionMarginPct),
		LearnedMinRuns:     ClampMinRuns(cfg.LearnedMinRuns),
		// Efficiency resolves ON when the operator left it at "default", which is where it differs
		// from escalate: escalate moves work to another backend and is a deliberate choice, while
		// reducing a step's wasted generation changes no backend and costs nothing to inherit. The
		// umbrella still decides absolutely, exactly as it does for the six mechanisms.
		EfficiencyOn:               umbrellaOn && resolveMechanism(cfg.Efficiency, true),
		EfficiencyEffortLevel:      cfg.EfficiencyEffortLevel,
		EfficiencyThinkingSharePct: ClampSharePct(cfg.EfficiencyThinkingSharePct),
		EfficiencyRepeatReadFloor:  ClampNonNegative(cfg.EfficiencyRepeatReadFloor),
		EfficiencyMaxRelaunches:    ClampNonNegative(cfg.EfficiencyMaxRelaunches),
	}
	declared := map[Mechanism]string{
		MechanismBudget:    cfg.Budget,
		MechanismThrift:    cfg.Thrift,
		MechanismDispatch:  cfg.Dispatch,
		MechanismInterview: cfg.Interview,
		MechanismEffort:    cfg.Effort,
		MechanismEscalate:  cfg.Escalate,
	}
	for _, m := range Mechanisms() {
		p.on[m] = umbrellaOn && resolveMechanism(declared[m], defaultOn(m))
	}
	return p
}

func resolveMechanism(declared string, whenDefault bool) bool {
	switch declared {
	case "on":
		return true
	case "off":
		return false
	default:
		return whenDefault
	}
}

// defaultOn is the one place the escalate exception lives. Stated as a predicate rather than as a
// map literal so the exception reads as the sentence design-doc.md:137 writes.
func defaultOn(m Mechanism) bool {
	return m != MechanismEscalate
}

// ClampMarginPct bounds the headroom percentage to the range config validates. 100 is admitted
// deliberately: startup.go documents a margin of the entire window as a coherent way to say "admit
// nothing", and it is an operator's choice at EVERY window size rather than a large-window carve-out.
func ClampMarginPct(pct int) int {
	if pct < 0 {
		return 0
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// ClampMinRuns keeps the learned-data floor at one run or more. Zero would mean "trust a prediction
// derived from no observations", which is not a weaker policy but an incoherent one.
func ClampMinRuns(runs int) int {
	if runs < 1 {
		return 1
	}
	return runs
}

// ClampSharePct keeps the efficiency thinking-share threshold inside 1..100 (#678 K3).
//
// The floor is 1 rather than 0 for the reason config gives when it rejects a written zero: a
// threshold of nothing is met by every step that ever generated anything, so it does not express a
// weaker policy but the absence of one. A value above 100 is not a share of anything.
func ClampSharePct(pct int) int {
	if pct < 1 {
		return 1
	}
	if pct > 100 {
		return 100
	}
	return pct
}

// ClampNonNegative floors a counted efficiency operand at zero. Both knobs it guards — the
// repeat-read floor and the relaunch bound — are counts of things that happened, and a negative
// count would invert the comparison it feeds rather than merely widen it.
func ClampNonNegative(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ClampAppetite bounds a learned appetite to [0, window].
//
// It exists because Phase 4 feeds this predicate values derived from RECORDED TELEMETRY rather
// than from validated config: a negative median from a malformed record, or a peak recorded
// against a larger window than the one now resolved, must not be able to invert a decision. A
// zero window clamps everything to zero, which the predicate never divides by — ReasonNoWindow
// catches that case before any arithmetic runs.
func ClampAppetite(tokens, window int64) int64 {
	if tokens < 0 || window <= 0 {
		return 0
	}
	if tokens > window {
		return window
	}
	return tokens
}
