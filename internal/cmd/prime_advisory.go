package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// This file is #668 K9: the advisory half of the mechanism surface. Phase 2 shipped the templates,
// the 150-token budget and the renderer with no production caller; this is the caller.
//
// It emits and it records. Which counsel the arithmetic warrants is tokenomics.Advisories' answer
// (advisory.go), and re-deriving any of it here would put a second copy of the bands in the layer
// that is supposed to be doing as it is told.
//
// Like everything else af prime does, this reads local files only — TestPrimeNoNetworkIO pins that a
// SessionStart hook puts no round trip in front of a session.

// advisoryHeading is the fixed prefix ux.md:22-30 requires. Fixed because it is what an operator
// greps for and what the byte-identity control removes: with the mechanisms off, deleting this block
// from the output must leave the previous output exactly.
const advisoryHeading = "## Session Guidance"

// advisoryLedger is the once-per-(step, mechanism) memory, and it is keyed on the step because that
// is the granularity design-doc.md's K9 row fixes. A session is primed many times for one step —
// SessionStart, then again after every af done that did not advance — and a mechanism that counselled
// on each of them would spend its token budget repeating advice the agent already has.
//
// StepID rather than a set of steps: the ledger describes the step in progress and nothing else, so a
// step change resets it rather than accumulating. An agent that returns to a step it was counselled
// about two steps ago is a fresh episode and gets counselled again, which is the honest answer — the
// context it was counselled in is gone.
// Keys holds advisory KEYS, of which a bare mechanism name is one — the json tag stays "mechanisms"
// because that is what every ledger already on disk says, and a rename would silently reset the dedup
// for every step in flight. #678 K7 is why the distinction exists: two templates now share the thrift
// mechanism, so remembering "thrift fired" would let the capacity counsel suppress the efficiency one.
type advisoryLedger struct {
	StepID string   `json:"step_id"`
	Keys   []string `json:"mechanisms"`
}

func advisoryLedgerPath(workDir string) string {
	return filepath.Join(workDir, ".runtime", "tokenomics_advisories.json")
}

// loadAdvisoryLedger returns the ledger for stepID, which is an EMPTY ledger for any step other than
// the one on disk. A file that will not decode reads the same way: the failure mode of a forgotten
// firing is one extra advisory, and the failure mode of a remembered one that never happened is a
// mechanism that has silently stopped working.
func loadAdvisoryLedger(workDir, stepID string) advisoryLedger {
	fresh := advisoryLedger{StepID: stepID}
	data, err := os.ReadFile(advisoryLedgerPath(workDir))
	if err != nil {
		return fresh
	}
	var l advisoryLedger
	if err := json.Unmarshal(data, &l); err != nil || l.StepID != stepID {
		return fresh
	}
	return l
}

// saveAdvisoryLedger is best-effort past the write: a hook may not fail because a note could not be
// filed (ADR-007). The cost of losing it is a repeated advisory, which the budget above bounds.
func saveAdvisoryLedger(workDir string, l advisoryLedger) {
	data, err := json.Marshal(l)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(advisoryLedgerPath(workDir)), 0o755); err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(advisoryLedgerPath(workDir), append(data, '\n'), 0o644)
}

// advisoryEmission is one counsel about to be printed: which template says it, which ledger entry
// remembers it, and which objective it serves. These three used to be one value — the mechanism — and
// #678 K7 separated them, because the two thrift-family templates share a mechanism and answer to
// different objectives. The record layer needs the mechanism, the dedup needs the key, and the
// baseline read needs the objective, so all three travel together rather than being re-derived at
// each use.
type advisoryEmission struct {
	key       string
	mechanism tokenomics.Mechanism
	objective string
	inputs    tokenomics.AdvisoryInputs
}

// outputAdvisoryContext renders the counsel this step's arithmetic warrants and records each
// emission as an intervention.
//
// The admission is passed IN rather than assembled here for the reason prime.go:208-211 gives about
// the occupancy reading it was assembled from: af prime is a hot verb, stepAdmission costs a
// models.json load and a digest read, and an advisory computed from a second assembly could disagree
// with the economics block printed immediately above it.
//
// Two objectives feed this one block (#678 C-4). Capacity counsel comes from tokenomics.Advisories,
// which needs a resolved window and a known occupancy and yields nothing without them. Efficiency
// counsel comes from the step's learned generation history and has no window operand at all, so it
// fires on a roomy profile and on an unmeasured session — the two cases where the whole capacity half
// is arithmetically silent. They print under one heading because an agent reads one context, not one
// per objective.
func outputAdvisoryContext(ctx context.Context, out io.Writer, factoryRoot, agent, workDir string,
	primed *primedStep, adm admission, reading statusline.ChannelReading, now time.Time) {

	if primed == nil || workDir == "" {
		return
	}
	var pending []advisoryEmission
	for _, tr := range tokenomics.Advisories(adm.window, adm.occupancy, adm.appetite, adm.policy) {
		pending = append(pending, advisoryEmission{
			key:       string(tr.Mechanism),
			mechanism: tr.Mechanism,
			objective: telemetry.ObjectiveCapacity,
			inputs:    tr.Inputs,
		})
	}
	// Gated on the plan alone and NOT additionally on policy.On(MechanismThrift): the efficiency arm has
	// its own switch, and requiring a capacity mechanism to be armed too would make token efficiency
	// conditional on the capacity configuration — the exact coupling this issue removes. The thrift arm
	// still governs every window-keyed thrift trigger above.
	if adm.efficiency.ThriftCounsel {
		pending = append(pending, advisoryEmission{
			key:       tokenomics.AdvisoryKeyEfficiencyThrift,
			mechanism: tokenomics.MechanismThrift,
			objective: telemetry.ObjectiveEfficiency,
			inputs:    tokenomics.EfficiencyAdvisory(adm.efficiency.Inputs),
		})
	}
	if len(pending) == 0 {
		return
	}

	ledger := loadAdvisoryLedger(workDir, primed.stepID)
	var fired []advisoryEmission
	var texts []string
	for _, em := range pending {
		if slices.Contains(ledger.Keys, em.key) {
			continue
		}
		text, ok := tokenomics.RenderAdvisoryKey(em.key, em.inputs)
		if !ok {
			// A key the trigger set names and the registry has no text for. Skipping is the
			// only honest answer: an empty advisory injected into a context is a blank line the
			// agent has to read, and recording it would claim counsel that was never given.
			continue
		}
		fired = append(fired, em)
		texts = append(texts, text)
	}
	if len(fired) == 0 {
		return
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, advisoryHeading)
	for _, text := range texts {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, text)
	}
	fmt.Fprintln(out, "")

	for _, em := range fired {
		ledger.Keys = append(ledger.Keys, em.key)
		recordIntervention(ctx, factoryRoot, workDir, agent, primed.instanceID, func(ev *telemetry.StepEvent) {
			ev.Formula = telemetryFormulaName(primed.formula)
			ev.StepID = primed.stepID
			ev.StepSeq = primed.stepSeq
			ev.StepTitle = primed.stepTitle
			ev.Mechanism = string(em.mechanism)
			ev.Action = telemetry.ActionAdvise
			ev.Objective = em.objective
			attachStepOccupancy(ev, reading, factoryRoot, now)
		})
	}
	saveAdvisoryLedger(workDir, ledger)
}

// K9 DOES NOT ARM K17's intervention latch, and the reasoning is worth stating because Phase 4 named
// K9 as an intended armer (recovery.go's residual, since rewritten).
//
// The latch suppresses every watchdog fire class, context_exhaustion included, so arming it asserts
// that an agent is quiet BY DESIGN. design-doc.md's K17 row scopes it to a serialized sub-agent phase
// IN PROGRESS. This surface runs at a step's OPEN: the serialization advisory tells an agent to
// launch sub-agents one at a time, and the agent has not launched one and may never. Arming here
// would blind the #596 exhaustion ladder for fifteen minutes on the strength of advice, at the
// occupancy that makes the ladder likeliest to be needed — which is prime_economics.go's argument,
// applied to K9's own counsel.
//
// The observer (subagent_observer.go) arms it instead, on evidence: a Task has completed, so a
// fan-out is demonstrably under way and the quiet that follows is the wait this advisory asked for.
// The window that leaves uncovered is the FIRST sub-agent's own run, which is exactly the exposure
// every factory had before this phase.
