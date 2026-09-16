package cmd

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// stepIDLabelPrefix is the label sling.go mints a formula-step bead with (sling.go), pairing the
// bead's per-instance id with the formula's own STABLE step id. It is the single source of truth
// both the writer and every reader spell the label under, so a write-side and read-side spelling
// cannot drift — which is the exact failure class the learned digest's cross-instance key exists to
// fix, one layer down.
const stepIDLabelPrefix = "step-id:"

// stepLabelOf recovers a step bead's stable formula step id from its labels. Empty when the bead
// carries none: the digest write and the admission reads both treat an empty label as "no stable
// key to join on" rather than substituting the per-instance bead id, the silent fallback decisions.md
// D1 forbids.
func stepLabelOf(iss issuestore.Issue) string {
	for _, l := range iss.Labels {
		if s, ok := strings.CutPrefix(l, stepIDLabelPrefix); ok {
			return s
		}
	}
	return ""
}

// This file is #668 K7's assembly half: the one place the verb layer turns factory state into the
// three operands tokenomics.Admit takes. af prime asks at a step's open and af done at its close,
// and two spellings of "what does this step need and what is left" would drift — the same reason
// step_context.go exists for the occupancy half, which this file consumes rather than re-derives.
//
// Nothing here decides anything. Admit is the predicate and shouldBoundaryHandoff is the boundary's
// single owner (D7); this assembles inputs and reports what came back.

// admission is one assembled verdict plus the operands that produced it.
//
// freshFits is not decoration and it is not the predicate's business. A no-fit verdict says the
// step will not fit BESIDE what this session is already carrying; it does not say a recycle would
// help. A step whose learned appetite exceeds the whole window no-fits in every session there will
// ever be, and handing off for it buys a respawn and changes nothing. So the second call asks the
// question the first cannot: would this step fit a session that had just started?
// policy rides along for #668 K9, whose advisories are keyed on a mechanism OTHER than budget. It is
// the same resolved value the verdict above was computed under, returned rather than re-resolved:
// two spellings of the umbrella conjunction (tokenomics.go:318-322 is the one precedent) is exactly
// the drift a caller would introduce by asking again, and an advisory that fired under a different
// policy than the verdict printed beside it describes a factory that does not exist.
// stepLabel and efficiency are #678 K5/K6's half of the same assembly, and they ride here for the
// reason policy does. The plan is derived from the SAME digest read the appetites came from, so a
// boundary that hands off for an efficiency reason and an economics block printed beside it cannot
// describe two different cache generations. stepLabel is the key that plan was resolved under: the
// boundary's cause line names it, and the per-instance bead id every other rollup uses means nothing
// across runs of the same formula.
type admission struct {
	decision  tokenomics.Decision
	freshFits bool
	window    tokenomics.Window
	occupancy tokenomics.Occupancy
	appetite  tokenomics.Appetite
	policy    tokenomics.Policy

	stepLabel  string
	efficiency tokenomics.EfficiencyPlan
}

// admits is K7's fail-OPEN spelling, and it is deliberately the inverse of this layer's usual
// grain. step_context.go:28-29 states the rule the occupancy channel is built on — absence must
// never be able to arm an action — and every reading-shaped decision in this package follows it.
// Admission runs the other way: only a positive no-fit refuses, and observe (mechanism off, no
// window, no reading, no learned data, too few runs) admits. The asymmetry is the point. An
// occupancy channel that fails closed declines to ACT on nothing; an admission check that failed
// closed would BLOCK work on nothing, which is the failure mode of a capacity policy that has just
// been switched on in a factory that has learned nothing yet.
func (a admission) admits() bool { return a.decision.Verdict != tokenomics.VerdictNoFit }

// handoffHelps is the operand the step boundary takes: this step does not fit here, AND it would
// fit a fresh session. Both halves, because a handoff that changes nothing is a respawn spent on
// a step that will overrun either way.
func (a admission) handoffHelps() bool {
	return a.decision.Verdict == tokenomics.VerdictNoFit && a.freshFits
}

// profileWindow resolves the window for ONE already-resolved profile.
//
// It takes the model NAME rather than the agent, and that is the whole point. An earlier spelling
// took the agent and re-derived the profile from cfg.Agents/cfg.Default — a second copy of #480's
// precedence chain that had already lost the .runtime/model_override leg resolveRecordModel honours
// (telemetry_record.go:113-131). Under `af sling --model X` that copy fed the window of one profile
// into an arithmetic whose appetite was keyed on another. The chain has exactly one implementation
// now, and the window and the appetite cannot describe two different profiles.
//
// resolveTokenomicsWindow (tokenomics.go:378) answers for the DEFAULT profile with no host reading,
// which is right for a factory-wide status surface and wrong here: a decision about this agent's
// session has both a resolved profile and a live host-reported total, and answering it from the
// default profile would predict one backend's capacity from another's.
func profileWindow(factoryRoot, model string, hostReported int64) tokenomics.Window {
	cfg, err := config.LoadModelsConfig(factoryRoot)
	if err != nil || cfg == nil {
		return tokenomics.ResolveWindow(nil, hostReported)
	}
	return tokenomics.ResolveWindow(cfg.Models[model], hostReported)
}

// learnedStep is everything ONE digest read answers about a (formula, step, model) key: the two
// appetites the capacity predicate needs, and the aggregate the efficiency predicate takes.
//
// found is carried rather than inferred from a zero aggregate, because Efficiency takes the pair and
// the two halves are different answers. A key that is absent has no learned data; a key that is
// present and empty measured nothing. Collapsing them would report one step's missing history as
// another step's measured zero.
type learnedStep struct {
	marginal  tokenomics.Appetite
	peak      tokenomics.Appetite
	aggregate tokenomics.Aggregate
	found     bool
}

// learnedFor answers what this (formula, step, model) has historically cost, split into the
// two appetites #668's two Admit calls need — resolved from ONE digest read, because af done and af
// prime are hot verbs and a second LoadDigest for the second appetite would double the off-path cost.
// #678 K5 added the aggregate leg to the same read for the same reason.
//
// The marginal is the step's GROWTH (peak − start), and it is what the additive decision reads: that
// decision adds the appetite to the current occupancy, which already carries the step's baseline, so
// adding the absolute peak would count the baseline twice (thread T1). The peak is the whole
// single-pass footprint, and freshFits reads it: a session that starts empty pays baseline + marginal
// = the absolute peak, so the "would it fit a fresh session" question is asked against the peak.
//
// The digest path is composed from telemetry.LearnedDigestPath, the same spelling af done's writer
// uses, so a lookup cannot miss a cache that was written — the drift the exported helper's own doc
// exists to prevent. A digest that exists and cannot be decoded yields unknown appetites rather than
// an error: the caller has nothing to do with one, and an unknown appetite is exactly what a corrupt
// cache honestly amounts to at a decision point.
//
// The key's StepID leg is the formula's STABLE step-id label, resolved by the caller from the step
// bead (stepLabelOf), because that is what samplesFrom files aggregates under. Bead ids are minted
// per formula instance, so keying the lookup on one would find nothing a later run wrote; keying on
// the label lets a step's history accumulate across runs of the same formula. An empty label is
// unknown, never a fallback to the bead id.
func learnedFor(factoryRoot, formula, stepLabel, model string) learnedStep {
	if formula == "" || stepLabel == "" || !telemetry.SafeDigestSegment(formula) {
		return learnedStep{}
	}
	d, err := tokenomics.LoadDigest(telemetry.LearnedDigestPath(config.TelemetryDir(factoryRoot), formula))
	if err != nil {
		return learnedStep{}
	}
	key := tokenomics.DigestKey{Formula: formula, StepID: stepLabel, Model: model}
	a, found := d.Lookup(key)
	return learnedStep{
		marginal:  d.AppetiteFor(key),
		peak:      d.PeakAppetiteFor(key),
		aggregate: a,
		found:     found,
	}
}

// admissionMechanisms are the mechanisms whose firing depends on the WINDOW operands this file
// assembles — budget for K7/K8's verdict, and thrift and dispatch, which read the same window,
// occupancy and appetite. Interview and escalate are absent because neither consults them.
//
// Effort left this list with #678 K5. It is no longer a window-pressure mechanism at all: the level
// it applies comes from the step's learned generation history, and the switch that decides whether
// that history is consulted is Policy.EfficiencyOn, which the guard in stepAdmission now names
// beside this list. An effort-on/efficiency-off factory reads none of the operands below, so leaving
// it here would arm the assembly for a mechanism that cannot fire from it.
var admissionMechanisms = []tokenomics.Mechanism{
	tokenomics.MechanismBudget,
	tokenomics.MechanismThrift,
	tokenomics.MechanismDispatch,
}

func policyArmsAny(p tokenomics.Policy, ms []tokenomics.Mechanism) bool {
	for _, m := range ms {
		if p.On(m) {
			return true
		}
	}
	return false
}

// stepAdmission assembles and asks. The reading is passed IN rather than taken here, because both
// callers have already taken one for their own record and a second read would answer about a
// different instant than the record they are about to write.
func stepAdmission(factoryRoot, workDir, agent, formula, stepLabel string,
	reading statusline.ChannelReading, tcfg config.TokenomicsConfig, breakerThresholdPct int) admission {

	// The umbrella is a conjunction of two switches living in two places, and only this layer sees
	// both (tokenomics.go:318-322 is the one precedent). A site that read the toggle file alone
	// would fire mechanisms an operator had switched off in startup.json. Admit re-checks
	// MechanismBudget itself, so that check is deliberately not duplicated here.
	policy := resolvedPolicy(factoryRoot, tcfg).WithContextThreshold(breakerThresholdPct)

	// Admit short-circuits on the budget switch and returns this identical decision from any
	// operands, so nothing observable changes by asking first — but the operands below cost a
	// models.json load and a digest read, and af done and af prime are hot verbs whose off-path
	// budget this package states as one gate-file read (telemetry_lifecycle_test.go:241-243).
	// With tokenomics off, which is the default, that budget is now kept.
	//
	// The test is over every mechanism that READS these operands, not over budget alone. Budget-off-
	// thrift-on is a posture an operator can write, and skipping the assembly on budget's switch
	// would leave K9's advisories with no window, no occupancy and no appetite to trigger on — a
	// mechanism switched on that can never fire, which is a worse answer than one switched off.
	//
	// EfficiencyOn is named beside the list rather than added to it because it is not a Mechanism
	// (policy.go:67-72) and because what it arms is the digest read, not the window arithmetic. It is
	// the single switch every #678 actuator is downstream of: Efficiency returns an empty plan with
	// ReasonMechanismOff without it, so a factory that has it off reads no aggregate here no matter
	// which mechanisms are on.
	if !policyArmsAny(policy, admissionMechanisms) && !policy.EfficiencyOn {
		return admission{
			decision: tokenomics.Admit(tokenomics.Window{}, tokenomics.Occupancy{}, tokenomics.Appetite{}, policy),
			policy:   policy,
		}
	}

	var occ tokenomics.Occupancy
	var hostReported int64
	if reading.IsHealthy() {
		if obs, ok := reading.Observation(); ok {
			occ = tokenomics.Occupancy{Tokens: obs.TokensUsed(), Known: true}
			hostReported = obs.TokensTotal()
		}
	}

	model, _ := resolveRecordModel(factoryRoot, workDir, agent, "")
	learned := learnedFor(factoryRoot, formula, stepLabel, model)
	a := admission{
		window:    profileWindow(factoryRoot, model, hostReported),
		occupancy: occ,
		appetite:  learned.marginal,
		stepLabel: stepLabel,
		// #678 K5/K6. Asked from the aggregate the read above already returned, so the boundary and
		// the launch legs judge one cache generation. It takes no operand from the three lines above
		// it — that is the objective split (design C-4), and it is why this line answers the same way
		// on a 1M-token window as on a full one.
		efficiency: tokenomics.Efficiency(learned.aggregate, learned.found, policy),
	}
	a.policy = policy
	a.decision = tokenomics.Admit(a.window, a.occupancy, a.appetite, policy)
	if a.decision.Verdict == tokenomics.VerdictNoFit {
		// freshFits asks a DIFFERENT question than the additive decision above, and it takes a
		// different operand: the ABSOLUTE peak, not the marginal a.appetite the decision used. A fresh
		// session starts empty, so what it must hold is the step's whole single-pass footprint; routing
		// the marginal growth here would model an empty session as needing only the step's increment and
		// call a handoff "helpful" for a step that overruns a fresh window too (thread T1 item 5).
		//
		// An empty window that is KNOWN to be empty — the state a fresh session starts in. Known must be
		// true or Admit short-circuits to observe and freshFits would read false for every no-fit there
		// is, which would disarm the mechanism entirely.
		a.freshFits = tokenomics.Admit(a.window, tokenomics.Occupancy{Known: true}, learned.peak, policy).
			Verdict == tokenomics.VerdictAdmit
	}
	return a
}

// recordIntervention writes the one record a fired mechanism leaves behind (#668 K4, AC-4).
//
// It carries the identity keys — formula, instance, step, session, model — and Verb names the
// surface that fired. Phase 4 stopped there and stated the residual: two mechanisms firing on the
// same verb were not distinguishable from the record alone. Phase 5 closed it, so the caller's
// mutate is now expected to set ev.Mechanism and ev.Action, which the schema reserved from the
// start (event.go:17-21) and which K15's reader and D16's readout both join on. Nothing here
// enforces that — a mutate is free to write any subset — because the mechanisms are spread across
// six call sites and a central check would be a seventh place to keep in step. An unlabelled record
// is not invisible: it is still an intervention record, joined to its step; what it loses is K15's
// section, whose reader skips a firing it cannot name (turn.go:188).
//
// Gated on the ctx-carried reading rather than a fresh gate-file read: the off-path budget is one
// gate read per verb, and a second reader is a second place for the exact-match rule to drift.
func recordIntervention(ctx context.Context, factoryRoot, workDir, agent, instanceID string,
	mutate func(*telemetry.StepEvent)) {

	if !verbTelemetryFrom(ctx).enabled {
		return
	}
	writeInterventionRecord(ctx, factoryRoot, workDir, agent, instanceID, mutate)
}

// recordEnforcement writes an intervention record that is NOT gated by the telemetry toggle (#672
// AC-3, decisions.md D5). A forced boundary handoff and a sub-agent-dispatch capacity refusal are
// ENFORCEMENT acts, not measurements: the ADR-007 (2026-08-31) amendment condition 4 makes every
// such act "a recorded intervention retrievable through the standard read surfaces, carrying the
// arithmetic that justified it." Telemetry is a separate, default-off, never-seeded switch, so gating
// the record on it would leave run-#1 enforcement with zero retrievable proof it happened — exactly
// what AC-3 and corollary 3 ("silence never passes") forbid. Advisory and effort-arm records stay on
// recordIntervention above, because THEIR subject is the measurement posture.
func recordEnforcement(ctx context.Context, factoryRoot, workDir, agent, instanceID string,
	mutate func(*telemetry.StepEvent)) {
	writeInterventionRecord(ctx, factoryRoot, workDir, agent, instanceID, mutate)
}

// writeInterventionRecord is the un-gated body both paths share, so the intervention shape has one
// definition and the only difference between an enforcement act and a measurement is whether the
// telemetry gate is consulted before reaching here.
func writeInterventionRecord(ctx context.Context, factoryRoot, workDir, agent, instanceID string,
	mutate func(*telemetry.StepEvent)) {
	ev := telemetryRecordFor(ctx, factoryRoot, workDir, agent, instanceID, "")
	ev.Event = telemetry.EventIntervention
	// #678 K1. Every mechanism that fires today triggers on window pressure, which is the capacity
	// objective by definition — efficiency does not exist as an objective until K4 gives it a
	// predicate that cannot read a window. Set HERE, before mutate, rather than at each of the ten
	// firing sites: it is one fact about all of them, and a site that later fires for efficiency
	// says so in its own closure and overwrites this. These records are append-only, so a firing
	// that goes out unlabelled can never be told apart from a later one afterwards.
	ev.Objective = telemetry.ObjectiveCapacity
	if mutate != nil {
		mutate(&ev)
	}
	appendTelemetryRecord(factoryRoot, ev)
}

// resolvedPolicy is the two-place umbrella conjunction, spelled once. The umbrella has one input in
// startup.json and one in the factory toggle file the verb layer owns, and only this layer sees both
// (tokenomics.go:318-322 is the one precedent). Every site that resolves a POLICY goes through here so
// a factory cannot be off by one reader's spelling and on by another's; the one deliberate re-spelling
// is tokenomicsState (telemetry_attribution.go), which needs the same conjunction as the record's
// on/off string rather than a Policy.
func resolvedPolicy(factoryRoot string, tcfg config.TokenomicsConfig) tokenomics.Policy {
	return tokenomics.ResolvePolicy(tokenomicsFactoryEnabled(factoryRoot) && tcfg.Enabled != "off", tcfg)
}

// launchPolicy resolves the posture for a site that has no reading, no step and no operands — only
// the question. It loads the startup config itself because a launch leg is upstream of every place
// that already holds one.
//
// A config that will not load resolves to the zero Policy, whose `on` map is nil and whose On is a
// nil-safe read, so every mechanism reads off. That is fail-CLOSED and deliberately the opposite
// grain to admits(): the question here is whether to apply a TREATMENT, and applying one on the
// strength of a config nobody could read would put an experiment's arm outside the operator's
// control.
func launchPolicy(factoryRoot string) tokenomics.Policy {
	cfg, err := config.LoadStartupConfig(factoryRoot)
	if err != nil {
		return tokenomics.Policy{}
	}
	return resolvedPolicy(factoryRoot, cfg.Tokenomics)
}

// hookedFormulaName is the formula's human NAME resolved without a store, which is what makes it
// usable from a launch leg and from a SessionStart hook.
//
// Every id→name conversion elsewhere goes through store.Get(id).Title, which constructs an
// issuestore and in production spawns the Python MCP server and waits on it. The learned digest is
// keyed on the name, so a reader that had to build a store to find out which formula it was in could
// not ask at all on the paths that matter most.
//
// The cache it reads is af done's own last_closed_step record (done.go), and memoryScopeKey
// (memory.go:295) reads the same file for the same reason — this is the shared spelling, extracted
// rather than copied. Its limitation is stated there and holds here: the record does not exist until
// the first af done of a run, so the FIRST session of a formula instance resolves no name and reads
// no learned data. That is honest — a launch with nothing to join on gets no treatment — and it is
// not silently a different formula's history, which is the failure a fallback would produce.
func hookedFormulaName(workDir string) string {
	if readHookedFormulaID(workDir) == "" {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "last_closed_step"))
	if err != nil {
		return ""
	}
	var rec lastClosedStepRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return ""
	}
	return telemetryFormulaName(rec.Formula)
}

// nextReadyStepLabel resolves the STABLE step-id label of the step a launching session will pick up.
//
// The launch legs need it and none of them has it: nextStepLabel is derived at exactly one place in
// this package (done.go's close-time branch), because only af done has just finished a step. A leg
// that guessed — the closing step's label, the first step of the formula — would key the digest read
// on work already paid for or on work that is already closed.
//
// It is the only store construction on this path, and it is gated TWICE, because a store is not a
// file read: newIssueStore discovers or spawns the Python MCP server and will wait up to 30s for it
// (mcpstore/lifecycle.go:64).
//
// The policy gate comes first and it is the load-bearing one. Go evaluates a call's arguments before
// the call, so a label resolved as an argument to withEffortLevel is resolved BEFORE withEffortLevel
// reads the policy — which would make every factory pay for this, including one with tokenomics off,
// which is the default. The respawn leg is where that matters most: it did no store I/O at all before
// #678, it runs under a context nothing can cancel, and a watchdog recycle is already the worst
// moment to add a 30-second wait.
//
// The hooked-formula gate is second and cheaper: a launch with no formula in flight has no next step
// to resolve. Past both gates the cost lands where the benefit is — af up and af sling already build a
// store on this same invocation.
func nextReadyStep(ctx context.Context, factoryRoot, agentDir string) (label, formula string) {
	if !launchPolicy(factoryRoot).On(tokenomics.MechanismEffort) {
		return "", ""
	}
	instanceID := readHookedFormulaID(agentDir)
	if instanceID == "" {
		return "", ""
	}
	store, err := newIssueStore(agentDir, os.Getenv("AF_ACTOR"))
	if err != nil {
		return "", ""
	}
	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: instanceID})
	if err != nil || len(result.Steps) == 0 {
		return "", ""
	}
	// #679 F3: the formula name comes from the store already open for the label, so a level is
	// selected on the FIRST session too. hookedFormulaName reads last_closed_step, which no af done
	// has written yet on session #1; the instance bead's title carries the name from sling. The file
	// read wins on later sessions; on the first, fall back to the instance title here rather than pay
	// a second MCP construction — this stays the only store built on the path.
	formula = hookedFormulaName(agentDir)
	if formula == "" {
		if inst, gerr := store.Get(ctx, instanceID); gerr == nil {
			formula = telemetryFormulaName(inst.Title)
		}
	}
	return stepLabelOf(result.Steps[0]), formula
}

func nextReadyStepLabel(ctx context.Context, factoryRoot, agentDir string) string {
	label, _ := nextReadyStep(ctx, factoryRoot, agentDir)
	return label
}

// capEffortLevel bounds a PLANNED level by what the resolved profile DECLARES (#678 K5). A plan may
// reduce effort and may never raise it: the declared level is the operator's statement about this
// profile, and an actuator that argued with it would be tuning a knob its owner had already set.
//
// Both unranked answers are refusals, and both are deliberate. A planned level with no rank — auto,
// or empty — reduces nothing, because auto is the HOST's default and the host does not publish where
// that default sits in the order (config.EffortRank). A DECLARED level with no rank imposes no
// ceiling, because "auto" declares no depth to stay under; the plan stands.
func capEffortLevel(planned, declared string) string {
	pr := config.EffortRank(planned)
	if pr < 0 {
		return ""
	}
	if dr := config.EffortRank(declared); dr >= 0 && dr < pr {
		return declared
	}
	return planned
}

// effortBreadcrumb is what a launch leg tells the session it is about to start about the level it was
// started at, and WHY (#678 K5).
//
// It exists because the two readers cannot see the launch. af prime records the session_start arm and
// af done compares the next step's plan against the level in force, and both run in a process the
// launcher replaced. The environment carries the level but not the objective, and the objective is
// the whole point: a run at "medium" on a host whose default is medium is a control run, and a run at
// "medium" because the efficiency actuator chose it is a treatment run (efficiency.go:3-7).
//
// StepLabel is the key the plan was resolved under, so af done can tell "the plan for the step I am
// about to open changed" from "a different step's plan was in force".
type effortBreadcrumb struct {
	Level     string `json:"level"`
	Objective string `json:"objective"`
	StepLabel string `json:"step_label"`
}

func effortBreadcrumbPath(agentDir string) string {
	return filepath.Join(agentDir, ".runtime", "effort_level")
}

// writeEffortBreadcrumb is best-effort past the write, for the reason every other .runtime/ writer on
// a launch path is: a session may not fail to start because a note about it could not be filed
// (ADR-007). The cost of losing it is one unlabelled session_start.
func writeEffortBreadcrumb(agentDir string, b effortBreadcrumb) {
	data, err := json.Marshal(b)
	if err != nil {
		return
	}
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(effortBreadcrumbPath(agentDir), append(data, '\n'), 0o644)
}

// clearEffortBreadcrumb is the other half of writing one, and it is not housekeeping.
//
// The breadcrumb is an ATTESTATION: af prime reads it and writes a reduce_effort record saying this
// session ran reduced. A launch that selects nothing applies nothing, so a breadcrumb left over from
// the previous launch would have the next session attest a treatment it never received — into an
// append-only log, where it cannot be corrected, and where Phase 7 counts it as a firing and credits
// the arm with a session that ran in the control (D-8/D-14).
//
// So every path out of withEffortLevel that does not select must come through here. The arm being off
// is one of those paths: the control group must leave no trace that reads as treatment.
func clearEffortBreadcrumb(agentDir string) {
	_ = os.Remove(effortBreadcrumbPath(agentDir))
}

// readEffortBreadcrumb returns the zero value for every failure — absent, unreadable, undecodable —
// and a zero Level is "no level in force", which is what an unlaunched arm honestly is. A level the
// host itself would not honour is dropped for the same reason launchEffortLevel drops one: a record
// naming it would attest an arm the session never ran in.
func readEffortBreadcrumb(agentDir string) effortBreadcrumb {
	data, err := os.ReadFile(effortBreadcrumbPath(agentDir))
	if err != nil {
		return effortBreadcrumb{}
	}
	var b effortBreadcrumb
	if err := json.Unmarshal(data, &b); err != nil || !config.IsEffortLevel(b.Level) {
		return effortBreadcrumb{}
	}
	return b
}

// withEffortLevel is the #678 K5 effort actuator: it CHOOSES the reasoning-effort level a launching
// session runs at, from the next step's learned generation history.
//
// The wrapper it replaces only ever DROPPED a level a profile had already declared, so a factory
// whose profiles declared nothing never ran at a chosen level at all — the arm existed and could not
// reach the launch. The level it now exports comes from tokenomics.Efficiency, a predicate that takes no window
// operand, so the reduction happens on a 1M-token cloud profile exactly as it does on a full one —
// which is AC-2. The band it replaces (`free < appetite`, deleted from advisory.go) could not: on a
// roomy window `free` is enormous, so token efficiency was conditional on running out of room.
//
// With the arm off it keeps the drop-when-off rule verbatim, and that rule is about the TREATMENT
// rather than the bookkeeping: design-doc.md:330 makes "the relaunch env carries the reduced-effort
// setting only when the policy arm is enabled" the criterion, because an experiment whose control
// group receives the treatment measures nothing. DROPPED rather than overwritten, because there is no
// neutral level to write — models.go accepts an empty value as the operator's deferral to the host's
// own default, which is what dropping already means, and the #602 universe clear that follows every
// one of these call sites turns the drop into a real unset on a reused pane.
//
// It wraps EVERY production SetModelEnv — the respawn leg, af sling and af up —
// because a session launched under a profile that declared the key would otherwise carry the
// treatment from its first turn and never be relaunched into the control.
// TestEffortArmWiredAtEveryModelEnvSite pins the set.
//
// Selecting nothing returns the env UNCHANGED rather than dropping the declared key: with the arm on,
// what a profile declares is in force until something warrants less, and a step with no learned
// history warrants nothing.
//
// formula is the launch leg's answer to "which formula is this?", resolved from a source that knows
// it on the FIRST session (nextReadyStep's instance-bead title). Empty falls back to hookedFormulaName
// inside selectEffortLevel, which is all a direct caller with a last_closed_step needs (#679 F3).
func withEffortLevel(factoryRoot, agentDir string, env []config.EnvVar, nextStepLabel, formula string) []config.EnvVar {
	declared, hasDeclared := declaredEffortLevel(env)
	policy := launchPolicy(factoryRoot)
	if !policy.On(tokenomics.MechanismEffort) {
		clearEffortBreadcrumb(agentDir)
		if !hasDeclared {
			return env
		}
		return withoutEffortLevel(env)
	}

	level, objective := selectEffortLevel(factoryRoot, agentDir, nextStepLabel, declared, formula, policy)
	if level == "" {
		clearEffortBreadcrumb(agentDir)
		return env
	}
	// #679 F4: a chosen level equal to what the profile already DECLARES reduced nothing — the
	// operator's profile set it, not the actuator (capEffortLevel returns `declared` when the profile
	// sits at or below the plan). Attesting an efficiency treatment there would file a control run
	// into the reduced arm. Suppress the breadcrumb and leave the already-declared env untouched. A
	// level over an UNDECLARED profile (declared == "") differs and is a real reduction that still
	// attests — the PROTECT case.
	if level == declared {
		clearEffortBreadcrumb(agentDir)
		return env
	}
	writeEffortBreadcrumb(agentDir, effortBreadcrumb{
		Level:     level,
		Objective: string(objective),
		StepLabel: nextStepLabel,
	})
	return withDeclaredEffortLevel(env, level)
}

// selectEffortLevel answers what the next step's history warrants, and under which objective.
//
// The efficiency question is asked FIRST and the capacity one only if it declined, which is what
// makes the second a last resort rather than a competing owner. They are recorded under different
// objectives because they are different claims: efficiency says this step generates more than its
// work needs, capacity says this step does not fit anywhere and there is no other lever left.
func selectEffortLevel(factoryRoot, agentDir, nextStepLabel, declared, formula string,
	policy tokenomics.Policy) (string, tokenomics.Objective) {

	agentName, err := resolveAgentName(agentDir, factoryRoot)
	if err != nil {
		return "", ""
	}
	// The single #480 precedence chain, not a second derivation: the digest is keyed on the model
	// spelling the RECORD writer files under, and a second chain here would eventually resolve a
	// profile the learned side never wrote (profileWindow's doc states the same rule for the window).
	model, _ := resolveRecordModel(factoryRoot, agentDir, agentName, "")
	// The leg resolves the formula name from the instance title so selection works on the first
	// session; a direct caller (or a leg that could not read the store) passes "" and we fall back to
	// last_closed_step, the only source there was before #679 F3.
	if formula == "" {
		formula = hookedFormulaName(agentDir)
	}
	learned := learnedFor(factoryRoot, formula, nextStepLabel, model)

	if level := capEffortLevel(tokenomics.Efficiency(learned.aggregate, learned.found, policy).EffortLevel, declared); level != "" {
		return level, tokenomics.ObjectiveEfficiency
	}
	if capacityLastResort(factoryRoot, model, learned, policy) {
		return capEffortLevel(policy.EfficiencyEffortLevel, declared), tokenomics.ObjectiveCapacity
	}
	return "", ""
}

// capacityLastResort is the one capacity trigger that survives K5's deletion, and it is a capacity
// FACT rather than the `free < appetite` heuristic it replaces: this step's learned PEAK does not fit
// a session that has just started, so it fits nowhere on this profile and no handoff can help.
//
// That is handoffHelps' negation on the no-fit side (`:76-78`) — no-fit AND not freshFits — asked at a
// leg that has no live reading. One call answers both halves: a peak that overruns an EMPTY window
// overruns every occupancy there will ever be. Known must be true or Admit short-circuits to observe.
//
// It consults no pool. declaredPool() decides whether a BACKEND's aggregate capacity is knowable, and
// keying a per-session effort reduction on it would make the treatment appear on a local profile and
// vanish on a cloud one for a reason that has nothing to do with either step. The window this divides
// by comes from the resolved profile with no host reading, which is all a launch has.
func capacityLastResort(factoryRoot, model string, learned learnedStep, policy tokenomics.Policy) bool {
	if !learned.found {
		return false
	}
	window := profileWindow(factoryRoot, model, 0)
	return tokenomics.Admit(window, tokenomics.Occupancy{Known: true}, learned.peak, policy).
		Verdict == tokenomics.VerdictNoFit
}

// recordObjective maps a breadcrumb's objective onto the record vocabulary, and returns "" for
// anything it does not recognise. The two vocabularies spell the same two words, and this is where the
// import edge is honoured rather than assumed: internal/telemetry must never learn what a decision
// looks like, so nothing but a value this switch names may reach a record.
//
// "" means "this breadcrumb names no objective I can attest", and its caller writes NO RECORD at all
// rather than an unlabelled one. That is the conservative half: an unrecognised objective means the
// breadcrumb was written by a binary whose vocabulary this one does not share, so what the session
// actually ran in is unknown — and a record is a claim about the arm a session ran in, which is worse
// wrong than missing.
func recordObjective(objective string) string {
	switch objective {
	case string(tokenomics.ObjectiveEfficiency):
		return telemetry.ObjectiveEfficiency
	case string(tokenomics.ObjectiveCapacity):
		return telemetry.ObjectiveCapacity
	}
	return ""
}

func declaredEffortLevel(env []config.EnvVar) (string, bool) {
	for _, kv := range env {
		if kv.Key == config.EnvEffortLevel {
			return kv.Value, true
		}
	}
	return "", false
}

func withoutEffortLevel(env []config.EnvVar) []config.EnvVar {
	kept := make([]config.EnvVar, 0, len(env))
	for _, kv := range env {
		if kv.Key != config.EnvEffortLevel {
			kept = append(kept, kv)
		}
	}
	return kept
}

// efficiencyRelaunchLedger bounds how many times ONE formula instance may be recycled for an
// efficiency reason (#678 K6). Without it the boundary is a loop: the plan warrants a level the
// session is not running at, the relaunch starts a session at that level — and if anything on the way
// loses the level, the next boundary warrants the same relaunch again, forever, one respawn per step
// close.
//
// Keyed on the instance so a mismatch RESETS rather than accumulates, the same rule
// advisoryLedger states about its step key: the count describes this run of this formula and nothing
// else, and a factory that has run twenty formulas must not arrive at its twenty-first already spent.
type efficiencyRelaunchLedger struct {
	InstanceID string `json:"instance_id"`
	Count      int    `json:"count"`
}

func efficiencyRelaunchPath(workDir string) string {
	return filepath.Join(workDir, ".runtime", "efficiency_relaunches")
}

// loadEfficiencyRelaunches reads the count for instanceID, which is ZERO for any other instance and
// for a file that will not decode. Zero is the fail-open answer here, and it is the right way round:
// the failure mode of a forgotten relaunch is one extra recycle, bounded by the cap on the next pass,
// while the failure mode of a remembered one that never happened is an actuator that has silently
// stopped working — which is the asymmetry loadAdvisoryLedger argues for its own ledger.
func loadEfficiencyRelaunches(workDir, instanceID string) int {
	data, err := os.ReadFile(efficiencyRelaunchPath(workDir))
	if err != nil {
		return 0
	}
	var l efficiencyRelaunchLedger
	if err := json.Unmarshal(data, &l); err != nil || l.InstanceID != instanceID {
		return 0
	}
	return l.Count
}

func bumpEfficiencyRelaunches(workDir, instanceID string) {
	l := efficiencyRelaunchLedger{InstanceID: instanceID, Count: loadEfficiencyRelaunches(workDir, instanceID) + 1}
	data, err := json.Marshal(l)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Join(workDir, ".runtime"), 0o755); err != nil {
		return
	}
	_ = os.WriteFile(efficiencyRelaunchPath(workDir), append(data, '\n'), 0o644)
}

// efficiencyRelaunch is K6's operand for the step boundary: whether the next step's learned plan
// warrants a clean session, which mechanism owns the reason, and whether the bound refused it.
//
// mechanism is what the record is filed under, and the two cases are genuinely different claims —
// effort for a level change (the next step should run shallower than this session is running) and
// interview for a clean start (the step historically spanned more than one session, so it should not
// inherit this one's transcript). Neither is ever filed under budget: a budget record means the window
// would not fit, and an efficiency relaunch fires with the window nearly empty.
//
// atCap is carried separately from warranted because the two mean opposite things to the reader. A
// zero value is "history warranted nothing"; atCap is "history warranted a relaunch and the bound
// refused it", which is a fact about the bound and is what the observe record exists to say.
type efficiencyRelaunch struct {
	mechanism tokenomics.Mechanism
	level     string
	warranted bool
	atCap     bool
}

// boundaryEfficiencyRelaunch assembles that operand from the plan stepAdmission already resolved.
//
// The level comparison is against the breadcrumb the LAUNCH leg wrote rather than against this
// process's own environment, because af done inherits the pane's env and would compare the plan to
// itself on any path where the launcher exported nothing. A recycle that changes nothing is a respawn
// spent for nothing — handoffHelps' argument, applied to the efficiency arm — and reducesEffort is
// where that argument is actually made.
//
// The interview switch gates the clean start, which is the third of the three readers #678 K8 gives
// that switch. Both legs are additionally downstream of Policy.EfficiencyOn, because a plan resolved
// with efficiency off carries no level and no clean start at all.
func boundaryEfficiencyRelaunch(workDir, instanceID string, adm admission) efficiencyRelaunch {
	plan := adm.efficiency
	levelChanges := adm.policy.On(tokenomics.MechanismEffort) &&
		reducesEffort(readEffortBreadcrumb(workDir), plan.EffortLevel, adm.stepLabel)
	cleanStart := plan.CleanStart && adm.policy.On(tokenomics.MechanismInterview)
	if !levelChanges && !cleanStart {
		return efficiencyRelaunch{}
	}

	r := efficiencyRelaunch{mechanism: tokenomics.MechanismInterview, warranted: true}
	if levelChanges {
		// The level change wins the naming when both hold, because it is the more specific fact and
		// the only one of the two that has a level to record.
		r.mechanism = tokenomics.MechanismEffort
		r.level = plan.EffortLevel
	}
	if loadEfficiencyRelaunches(workDir, instanceID) >= adm.policy.EfficiencyMaxRelaunches {
		r.warranted = false
		r.atCap = true
	}
	return r
}

// reducesEffort answers the only question a level-driven relaunch can act on: is this session running
// at MORE reasoning effort than the next step's plan asks for? Three shapes answer no, and each one
// closes a loop that would otherwise recycle a session at every boundary until the cap burned out —
// six full re-primes per formula instance, spent by the mechanism that exists to save them.
//
//   - No planned level, or one with no rank. Nothing to move toward.
//   - An ABSENT breadcrumb. The launch leg writes one whenever it selects, so no breadcrumb means no
//     selection happened — the arm is off, or the step has no learned history, or the leg resolved an
//     empty model env and was skipped. A leg that did not run cannot be made to run by recycling into
//     it again, and treating absence as "the level differs" is exactly how that loop starts.
//   - A level at or BELOW the plan's. The launch leg CAPS its selection by what the profile declares
//     (capEffortLevel), so a profile declaring `low` under a `medium` plan applies `low` and will
//     apply `low` again on the next launch. Comparing the uncapped plan against the applied level
//     would warrant a relaunch forever for a difference no relaunch can close. Below the plan is also
//     not a problem worth solving: the operator asked for less depth and got it.
//
// The step label is the fourth refusal and the reason the breadcrumb carries one. A breadcrumb
// resolved for the step ABOUT TO OPEN belongs to a session that was launched targeting this very step,
// so it is already running the level this plan asked for and there is nothing to correct.
func reducesEffort(crumb effortBreadcrumb, planned, nextStepLabel string) bool {
	if crumb.Level == "" || crumb.StepLabel == nextStepLabel {
		return false
	}
	pr := config.EffortRank(planned)
	return pr >= 0 && config.EffortRank(crumb.Level) > pr
}

// withDeclaredEffortLevel replaces the key IN PLACE when the profile declared one and appends
// otherwise. In place, because the export set is ORDERED (orderedEnv) and a profile's own position
// for the key is part of what the operator wrote; moving it would make two launches of the same
// profile emit different commands.
func withDeclaredEffortLevel(env []config.EnvVar, level string) []config.EnvVar {
	out := make([]config.EnvVar, 0, len(env)+1)
	replaced := false
	for _, kv := range env {
		if kv.Key == config.EnvEffortLevel {
			kv.Value = level
			replaced = true
		}
		out = append(out, kv)
	}
	if !replaced {
		out = append(out, config.EnvVar{Key: config.EnvEffortLevel, Value: level})
	}
	return out
}
