package tokenomics

import (
	"fmt"
	"strings"
)

// AdvisoryTokenBudget is the terseness bound ux.md:69 fixes: each advisory stays within roughly
// 150 tokens. It is a budget rather than a style note because an advisory is INJECTED into an
// agent's context — text spent counselling an agent about its context is text taken from the
// context it was counselled about, and an advisory that consumed the headroom it warned about
// would be self-defeating.
const AdvisoryTokenBudget = 150

// AdvisoryInputs are the only values a template may interpolate, and every one of them is a
// number. That is the rule that makes the budget above enforceable: with a string substitution,
// agent- or config-controlled text would enter an injected surface and no static length bound
// would survive it. There is deliberately no formula name here, and no step description.
type AdvisoryInputs struct {
	WindowTokens   int64
	FreeTokens     int64
	AppetiteTokens int64
	ProjectedPct   float64
	PriorRuns      int

	// The two learned generation figures #678 K7's counsel rests on. They are here rather than on a
	// second inputs type because this struct's subject is what a template may interpolate, not which
	// objective assembled it — and a second numeric-only inputs type would mean a second length bound,
	// a second substitution rule and a second golden. Both are zero on every capacity trigger, which
	// assembles none of them: only the efficiency template names them.
	RepeatReads     int64
	GeneratedTokens int64
}

// EfficiencyAdvisory is the operand set the efficiency thrift template reads, mapped from a plan's
// learned figures. It lives here, beside the template, for the reason args below does: the verbs and
// the operands stay in one place, so a verb layer assembling this by hand cannot pair the re-read
// count with the token count in the wrong order.
func EfficiencyAdvisory(in EfficiencyInputs) AdvisoryInputs {
	return AdvisoryInputs{
		RepeatReads:     in.MedianRepeatReads,
		GeneratedTokens: in.MedianOutTokens,
		PriorRuns:       in.PriorRuns,
	}
}

// AdvisoryTemplate is one counsel's fixed text. The text is a compile-time constant — there
// is no template loading, no operator-supplied wording and no per-formula variant, so the whole
// registry is reviewable by reading this file.
type AdvisoryTemplate struct {
	Mechanism Mechanism
	Text      string

	// key overrides the registry identity, and it exists because #678's efficiency counsel is thrift
	// FAMILY counsel under a mechanism vocabulary that is closed and has no efficiency member
	// (policy.go:67-71). Two entries under one Mechanism would make the second unreachable through
	// RenderAdvisory and would break the golden's bijection, so the registry is keyed on Key() rather
	// than on Mechanism and the two thrift entries are distinct keys of the same mechanism. Empty on
	// every entry whose key IS its mechanism, so the common case declares nothing.
	key string

	// args is what keeps the verbs and the operands in one place. A template edited without its
	// argument list renders %!d(MISSING), which the length test fails on rather than ships.
	args func(AdvisoryInputs) []any
}

// AdvisoryKeyEfficiencyThrift is the composite key the efficiency thrift is registered and LEDGERED
// under. Declared once, here, because the registry and the prime surface's per-step dedup ledger must
// spell it identically: two spellings would let the same counsel fire twice per step, or let the
// capacity thrift suppress the efficiency one — the two facts a composite key exists to keep apart.
const AdvisoryKeyEfficiencyThrift = "thrift|efficiency"

// Key is the registry identity: the mechanism's own name unless an entry declares otherwise.
func (t AdvisoryTemplate) Key() string {
	if t.key != "" {
		return t.key
	}
	return string(t.Mechanism)
}

// advisoryRegistry holds a template for the mechanisms that COUNSEL and for no others. Interview
// and escalate are deterministic — they ask a question or move the work — and giving them advisory
// text would imply a persuasion step neither of them has.
var advisoryRegistry = []AdvisoryTemplate{
	{
		Mechanism: MechanismBudget,
		Text: "Context budget: this step has historically needed about %d tokens, and %d of a " +
			"%d-token window are free — projecting %.1f%% occupancy. Consider narrowing the scope " +
			"or handing off before you begin rather than part-way through.",
		args: func(in AdvisoryInputs) []any {
			return []any{in.AppetiteTokens, in.FreeTokens, in.WindowTokens, in.ProjectedPct}
		},
	},
	{
		Mechanism: MechanismThrift,
		Text: "Thrift: this step projects %.1f%% of a %d-token window. Prefer targeted reads over " +
			"whole-file reads, and do not re-read what is already in context.",
		args: func(in AdvisoryInputs) []any {
			return []any{in.ProjectedPct, in.WindowTokens}
		},
	},
	// K9 names this mechanism "serialization" and the closed vocabulary (policy.go) has no such
	// member, so serialization counsel is carried HERE, on dispatch — the member whose subject is
	// where sub-agent work goes. A seventh member was the alternative and was rejected: the
	// vocabulary is closed on purpose, and "serialize the fan-out" and "hand the work to a fresh
	// specialist" are the same decision seen from two ends, not two decisions.
	//
	// The wording therefore had to change. It used to counsel decomposition alone — a fresh window
	// starts at 0% — which is true but is not what the design asks for at this trigger. Waiting is
	// the load-bearing half: concurrent sub-agents all report back into THIS window, so the fan-out's
	// return traffic is what breaches the projection, not the launching.
	//
	// As of #673 item 1 its ONLY renderer is the prime advisory (D-7), which is appetite counsel and
	// does not arm the K17 latch. The PostToolUse observer used to render it too, and stopped: it now
	// relays the dispatch gate's recorded refusal, whose figures are a shared POOL and not the
	// per-request window this template names — deriving one from the other is the second capacity
	// verdict #673 deleted. Keep this template for prime; do not route the observer back through it.
	{
		Mechanism: MechanismDispatch,
		Text: "Serialization: this step projects %.1f%% of a %d-token window. Launch sub-agents one " +
			"at a time and wait for each to return before starting the next — a fresh specialist " +
			"begins at 0%% and spends its own window, but every concurrent one reports back into " +
			"this one.",
		args: func(in AdvisoryInputs) []any {
			return []any{in.ProjectedPct, in.WindowTokens}
		},
	},
	// #678 K7's fifth entry, and the only one in this registry that no band above can reach. Its
	// operands are a step's learned generation history, so it is triggered by the efficiency predicate
	// (efficiency.go, EfficiencyPlan.ThriftCounsel) rather than by counselWarranted below — which is
	// the whole frame lift: this counsel has no window in it and a roomy profile cannot switch it off.
	// It is registered here anyway, rather than declared beside its caller, so the ≤150-token budget,
	// the numeric-only substitution rule and the golden all cover it with no test change
	// (AdvisoryTemplates' doc below).
	{
		Mechanism: MechanismThrift,
		key:       AdvisoryKeyEfficiencyThrift,
		Text: "Efficiency: prior runs of this step re-read %d files and generated about %d tokens " +
			"across %d runs. Read each file once; work from what is already in context.",
		args: func(in AdvisoryInputs) []any {
			return []any{in.RepeatReads, in.GeneratedTokens, in.PriorRuns}
		},
	},
}

// counselMechanisms is the set #668 K9 names — serialization (carried on dispatch above) and
// thrift — in the canonical order Mechanisms() declares, so a prime that fires two of them prints
// them in the same order every time.
//
// Effort left this set at #678 K5. Its band asked whether the step needed more than was left, which
// made the counsel conditional on scarcity; the actuator now chooses a level from the step's learned
// generation baseline at the launch legs, where the level can actually be applied rather than merely
// suggested.
//
// Budget is excluded although the registry carries a template for it. The budget mechanism already
// has a voice at this surface: K7's economics block renders the same arithmetic in its own words at
// the same moment, and firing both would tell one session the same thing twice in one prime.
var counselMechanisms = []Mechanism{MechanismThrift, MechanismDispatch}

// AdvisoryTrigger is one mechanism's warrant to counsel, carrying the operands it will render.
type AdvisoryTrigger struct {
	Mechanism Mechanism
	Inputs    AdvisoryInputs
}

// Advisories answers which counsel this arithmetic warrants. It is the K9 half of the decision core
// and it decides nothing else: no text, no ordering beyond the vocabulary's own, no side effect. The
// verb layer renders and records.
//
// The three bands are disjoint where it matters and deliberately not where it does not:
//
//   - THRIFT triggers on occupancy alone, at or above the operator's ceiling. It needs no learned
//     appetite because it is not a claim about this step — a session already at the ceiling should
//     read narrowly whatever it is about to do, and demanding history first would silence the one
//     mechanism that still applies in a factory that has learned nothing.
//   - DISPATCH (serialization) triggers when it fits, but only just: appetite <= free < 2*appetite.
//
// Thrift may accompany dispatch, because "you are nearly full" and "this step is large" are two
// different facts and a session can be in both.
//
// This function answers for the CAPACITY objective alone, and the registry's fifth template is
// deliberately outside its reach: efficiency counsel is keyed on learned generation history, and a
// band here could only key it on a window (#678 C-4).
//
// Everything below is guarded on a resolved window and a KNOWN occupancy. An unmeasured session
// yields no counsel at all: step_context.go's rule is that absence must never arm an action, and an
// advisory injected into a context on the strength of a missing reading is exactly that.
func Advisories(w Window, occ Occupancy, app Appetite, p Policy) []AdvisoryTrigger {
	if w.Tokens <= 0 || !occ.Known {
		return nil
	}
	used := ClampAppetite(occ.Tokens, w.Tokens)
	free := w.Tokens - used

	// The trust rule is the predicate's, restated rather than relaxed: an aggregate below the
	// operator's run floor is a number, not a prediction, and counsel keyed on one would be the
	// mechanism speaking with more confidence than its evidence.
	trusted := app.Known && app.Tokens > 0 && app.Runs >= p.LearnedMinRuns
	in := AdvisoryInputs{WindowTokens: w.Tokens, FreeTokens: free}
	if trusted {
		in.AppetiteTokens = app.Tokens
		in.PriorRuns = app.Runs
	}
	in.ProjectedPct = float64(uint64(used)+uint64(ClampAppetite(in.AppetiteTokens, w.Tokens))) /
		float64(w.Tokens) * 100

	var out []AdvisoryTrigger
	for _, m := range counselMechanisms {
		if p.On(m) && counselWarranted(m, used, free, in.AppetiteTokens, w.Tokens, p, trusted) {
			out = append(out, AdvisoryTrigger{Mechanism: m, Inputs: in})
		}
	}
	return out
}

func counselWarranted(m Mechanism, used, free, appetite, window int64, p Policy, trusted bool) bool {
	switch m {
	case MechanismThrift:
		return atLeastPct(uint64(used), window, 100-ClampMarginPct(p.AdmissionMarginPct))
	case MechanismDispatch:
		// free-appetite rather than 2*appetite: the subtraction cannot overflow under the guard that
		// precedes it, and the doubling can for an appetite an operator's window declaration allows.
		return trusted && appetite <= free && free-appetite < appetite
	}
	return false
}

// AdvisoryTemplates returns the registry. Callers that need to walk every template — the length
// bound, the substitution rule — walk this rather than a re-typed list, so a template added later
// is covered with no test change.
func AdvisoryTemplates() []AdvisoryTemplate {
	out := make([]AdvisoryTemplate, len(advisoryRegistry))
	copy(out, advisoryRegistry)
	return out
}

// RenderAdvisory interpolates one mechanism's template. The false return is the closed-vocabulary
// half: a mechanism with no template says so, rather than returning an empty string a caller would
// inject blindly and an operator would later find as a blank line in a transcript.
//
// A mechanism's own name is its entry's key, so this reaches the capacity thrift and cannot reach the
// efficiency one — which is the property that makes two thrift-family templates safe. An earlier
// spelling matched on Mechanism and returned the FIRST hit, which would have made the second entry
// unreachable by accident rather than by rule.
func RenderAdvisory(m Mechanism, in AdvisoryInputs) (string, bool) {
	return RenderAdvisoryKey(string(m), in)
}

// RenderAdvisoryKey interpolates the template registered under one key. It is what a caller whose
// counsel is not addressed by a bare mechanism name uses — today the efficiency thrift, under
// AdvisoryKeyEfficiencyThrift.
func RenderAdvisoryKey(key string, in AdvisoryInputs) (string, bool) {
	for _, tpl := range advisoryRegistry {
		if tpl.Key() == key {
			return fmt.Sprintf(tpl.Text, tpl.args(in)...), true
		}
	}
	return "", false
}

// EstimateTokens is the measuring stick for the budget above, and it is deliberately the crude
// four-characters-per-token heuristic rather than a real tokenizer.
//
// A real tokenizer would be a dependency, a model-specific answer, and a moving target — and the
// bound it would be measuring is itself written as "≤ ~150 tokens". What the budget needs is a
// stable, conservative estimate that fails a template someone has let grow into a paragraph, and
// this is that. It rounds UP, so a bound that holds here holds on any tokenizer that averages more
// than four characters per token.
func EstimateTokens(s string) int {
	if strings.TrimSpace(s) == "" {
		return 0
	}
	return (len(s) + 3) / 4
}
