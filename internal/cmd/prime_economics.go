package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// This file is #668 K16: what af prime costs the session it primes, and the one thing prime can
// usefully say about capacity before the step body is rendered.
//
// Everything here reads local files only. af prime is a SessionStart hook — TestPrimeNoNetworkIO
// pins that it puts no round trip in front of a session — so nothing in this path may reach the
// telemetry backend, and nothing may block.

// primeCostWriter counts what actually reached the agent.
//
// Measured rather than estimated from the template, because the template is the smaller half: the
// role prose, the formula header, the step description, the checkpoint block and the pending mail
// are all assembled at run time from state, and a figure derived from anything else would be a
// figure about a different session.
type primeCostWriter struct {
	w io.Writer
	n int64
}

func (p *primeCostWriter) Write(b []byte) (int, error) {
	n, err := p.w.Write(b)
	p.n += int64(n)
	return n, err
}

// primeCostRecord accumulates one SESSION's priming cost. A session is primed many times — once at
// SessionStart and again after every af done — and the question K16 exists to answer is what the
// whole session spent on being told where it was, not what one invocation spent.
//
// It is a sidecar under the telemetry dir rather than a record in the log, and that is a stated
// limitation rather than a preference: StepEvent is a closed schema with no field this figure
// belongs in, and internal/telemetry/event.go is read-only in this phase. SessionID is carried so
// the figure joins to the records that DO ship.
type primeCostRecord struct {
	SessionID string `json:"session_id"`
	Agent     string `json:"agent"`
	Primes    int    `json:"primes"`
	Bytes     int64  `json:"bytes"`
	TokensEst int64  `json:"tokens_est"`
	UpdatedAt string `json:"updated_at"`
}

func primeCostPath(factoryRoot, agent string) string {
	return filepath.Join(config.TelemetryDir(factoryRoot), "prime_cost", agent+".json")
}

// estimateTokens converts bytes to tokens at the four-bytes-per-token rule of thumb. It is an
// ESTIMATE and named one: prime output is markdown prose and paths, which tokenize better than
// four bytes per token, so the figure runs high. A tokenizer would be exact and would also be a
// new dependency (ADR-013), for a number whose job is to show an operator a trend.
func estimateTokens(bytes int64) int64 { return (bytes + 3) / 4 }

// recordPrimeCost accumulates this invocation into the session's running total.
//
// Gated on the telemetry gate, and best-effort past it: a hook may not fail because a measurement
// could not be filed (ADR-007). A session id change resets the counter rather than appending, so
// the file stays one small record per agent instead of growing without bound.
func recordPrimeCost(ctx context.Context, factoryRoot, agent, sessionID string, bytes int64, now time.Time) {
	if !verbTelemetryFrom(ctx).enabled || factoryRoot == "" || agent == "" {
		return
	}
	if err := config.ValidateAgentName(agent); err != nil {
		return
	}

	path := primeCostPath(factoryRoot, agent)
	var rec primeCostRecord
	if data, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(data, &rec); err != nil || rec.SessionID != sessionID {
			rec = primeCostRecord{}
		}
	}
	rec.SessionID = sessionID
	rec.Agent = agent
	rec.Primes++
	rec.Bytes += bytes
	rec.TokensEst = estimateTokens(rec.Bytes)
	rec.UpdatedAt = now.UTC().Format(telemetry.TimestampLayout)

	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(path, append(data, '\n'), 0o644)
}

// primeCountRecord is how many times THIS session has been primed, and it is a second counter beside
// the sidecar above rather than a reader of it (#678 K8b).
//
// The sidecar cannot answer the question. recordPrimeCost early-returns on the telemetry gate, which is
// default-off and never seeded, so its Primes field is zero on every factory that has not opted into
// measurement — and a re-prime reduction that only worked with telemetry on would be a token saving
// nobody receives. This counter is therefore un-gated, and it is deliberately NOT under the telemetry
// dir: it is session state belonging to the agent's own .runtime/, beside session_id and
// transcript_path, which is what makes it survive a telemetry reset and stay out of the record log.
//
// Reduced is the once-per-session latch. A session is primed many times and each of the later ones is
// reduced, but the reduction is ONE decision about this session — recording it per prime would report
// one mechanism's single firing as a dozen, which is the same argument advisoryLedger makes per step.
type primeCountRecord struct {
	SessionID string `json:"session_id"`
	Primes    int    `json:"primes"`
	Reduced   bool   `json:"reduced"`
}

func primeCountPath(workDir string) string {
	return filepath.Join(workDir, ".runtime", "prime_count")
}

// loadPrimeCount returns the record for sessionID, and a FRESH record for any other session or a file
// that will not decode. A session change resets rather than accumulates, because the question is what
// this session has already received and a new session has received nothing — the same key-scoped reset
// loadAdvisoryLedger applies to its step.
func loadPrimeCount(workDir, sessionID string) primeCountRecord {
	fresh := primeCountRecord{SessionID: sessionID}
	data, err := os.ReadFile(primeCountPath(workDir))
	if err != nil {
		return fresh
	}
	var rec primeCountRecord
	if err := json.Unmarshal(data, &rec); err != nil || rec.SessionID != sessionID {
		return fresh
	}
	return rec
}

func savePrimeCount(workDir string, rec primeCountRecord) {
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(primeCountPath(workDir)), 0o755); err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(primeCountPath(workDir), append(data, '\n'), 0o644)
}

// bumpPrimeCount counts this prime and returns the new total, so 1 is "this is the first prime of this
// session" and anything above it is "this session has been primed before".
//
// A failed write returns the count anyway. That is fail-open toward SENDING context: the next prime
// re-reads the old count, decides the session has been primed one fewer time than it has, and emits
// the identity block again. One repeated block is the honest cost of an unwritable .runtime/; a session
// silently missing its identity because a counter could not be read is not.
func bumpPrimeCount(workDir, sessionID string) int {
	if workDir == "" || sessionID == "" {
		return 0
	}
	rec := loadPrimeCount(workDir, sessionID)
	rec.Primes++
	savePrimeCount(workDir, rec)
	return rec.Primes
}

// markPrimeReduced latches the reduction for this session and reports whether this call was the first.
// Called only when a reduction actually applied, so a session that was never reduced never writes one.
func markPrimeReduced(workDir, sessionID string) bool {
	if workDir == "" || sessionID == "" {
		return false
	}
	rec := loadPrimeCount(workDir, sessionID)
	if rec.Reduced {
		return false
	}
	rec.Reduced = true
	savePrimeCount(workDir, rec)
	return true
}

// outputEconomicsContext is K7's open-time half, and it is ADVISORY. af prime is the SessionStart
// hook; a hook that recycled the session it was invoked for would recycle it before it ever ran, so
// this says what it found and leaves the acting to the step boundary, which is the single owner of
// the recycle decision (D7).
//
// It prints NOTHING on an admit, and that is the point of a cost mechanism: a block that rendered
// on every prime would spend tokens on every prime to say that tokens are fine.
//
// It does not arm K17's intervention latch, and the omission is deliberate. The latch suppresses
// EVERY watchdog fire class, context_exhaustion included, so whatever arms it is asserting that the
// agent is waiting by design. Neither branch below describes a wait: one tells the agent to work the
// step normally, the other tells it to expect a mid-step recycle — and arming there would spend the
// #596 exhaustion ladder to protect a session in the very state that ladder exists for, at the very
// occupancy that makes it likeliest to be needed. design-doc.md scopes the latch to deferral and
// serialized sub-agent phases (K9/K18), which are Phase 5 mechanisms; this phase delivers the half
// the watchdog honours and the primitive that arms it, which is what AC-3 fabricates a latch to
// verify.
//
// One side effect remains: the intervention record, AC-4's evidence that a mechanism fired. It is
// unconditional here because the advisory is unconditional — every render of this block is one
// firing and leaves one record.
//
// The assembled admission is RETURNED so #668 K9's advisories can be keyed on the same operands
// (prime_advisory.go). Returning it rather than letting that block assemble its own is the same
// argument prime.go:208-211 makes about the occupancy reading underneath it: two assemblies a few
// microseconds apart can straddle a snapshot write, and counsel that disagreed with the economics
// block printed directly above it would be worse than either alone. The zero admission a refusal
// returns resolves every mechanism off, so an early return here silences K9 too — correctly, since
// both refusals are "there is no step, or no thresholds to judge it against".
func outputEconomicsContext(ctx context.Context, out io.Writer, factoryRoot, role, workDir string,
	primed *primedStep, reading statusline.ChannelReading, cfg *config.StartupConfig, now time.Time) admission {

	if primed == nil || cfg == nil {
		return admission{}
	}
	adm := stepAdmission(factoryRoot, workDir, role, telemetryFormulaName(primed.formula),
		primed.stepLabel, reading, cfg.Tokenomics, cfg.Recovery.ContextThresholdPct)
	if adm.admits() {
		return adm
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "## Session Economics")
	fmt.Fprintln(out, "")
	fmt.Fprintf(out, "This step has historically grown by about %d tokens. This session is carrying %d of a %d-token window, "+
		"which projects to %.0f%% against a %.0f%% ceiling.\n",
		adm.appetite.Tokens, adm.occupancy.Tokens, adm.window.Tokens,
		adm.decision.ProjectedPct, adm.decision.HeadroomPct)
	if adm.freshFits {
		fmt.Fprintln(out, "It would fit a session that had just started. Work the step as normally as you can; "+
			"`af done` will hand off to a fresh session at the boundary rather than let the step overrun.")
	} else {
		fmt.Fprintln(out, "It would not fit a fresh session either, so a handoff would not help. "+
			"Work in the smallest increments you can and expect to be recycled mid-step.")
		// #678 K5's capacity last resort, said out loud. The launch leg has already applied the reduced
		// level for exactly this case — a step whose learned peak fits no session on this profile — and
		// an agent that is not told why its reasoning depth is capped will read the cap as a defect.
		//
		// Two conjuncts, because they answer two different questions and neither implies the other.
		//
		// The arm is the operator's switch, honoured at the READ site rather than trusted to have been
		// honoured at the write site — a level applied before the switch was thrown leaves a breadcrumb
		// behind it, and a control-group session must not be told it was treated.
		//
		// The breadcrumb is whether a level was applied to THIS session, which the arm is no evidence
		// of. The launch leg needs a resolvable formula to read the step's learned peak, and the first
		// session of an instance has none — nothing has closed a step yet — so it launches at the host
		// default and would otherwise still be told its effort was reduced. Its step label is what makes
		// the attestation about the step being primed rather than a neighbouring one.
		crumb := readEffortBreadcrumb(workDir)
		if adm.policy.On(tokenomics.MechanismEffort) && crumb.Level != "" && crumb.StepLabel == primed.stepLabel {
			fmt.Fprintln(out, "Reasoning effort is reduced for a step this size, because no session on this profile can hold it. "+
				"Match the depth of the work to the headroom that is actually left.")
			recordIntervention(ctx, factoryRoot, workDir, role, primed.instanceID, func(ev *telemetry.StepEvent) {
				ev.Formula = telemetryFormulaName(primed.formula)
				ev.StepID = primed.stepID
				ev.StepSeq = primed.stepSeq
				ev.StepTitle = primed.stepTitle
				ev.Mechanism = string(tokenomics.MechanismEffort)
				ev.Action = telemetry.ActionAdvise
				// Capacity, and it is worth saying explicitly even though writeInterventionRecord
				// pre-stamps it: this is the one effort record in the tree that is NOT an efficiency act,
				// and a reader that found it under the efficiency objective would count a step nothing
				// can hold as evidence that reducing generation pays.
				ev.Objective = telemetry.ObjectiveCapacity
				attachStepOccupancy(ev, reading, factoryRoot, now)
			})
		}
	}
	fmt.Fprintln(out, "")

	recordIntervention(ctx, factoryRoot, workDir, role, primed.instanceID, func(ev *telemetry.StepEvent) {
		ev.Formula = telemetryFormulaName(primed.formula)
		ev.StepID = primed.stepID
		ev.StepSeq = primed.stepSeq
		ev.StepTitle = primed.stepTitle
		// Named now that the schema can carry it (#668 Phase 5). Advise rather than handoff: this
		// block says what it found and leaves the recycle to the boundary, so what the session
		// received was counsel it could act on or not. The handoff that MAY follow is af done's
		// record to write, and conflating the two would report one decision as two.
		ev.Mechanism = string(tokenomics.MechanismBudget)
		ev.Action = telemetry.ActionAdvise
		attachStepOccupancy(ev, reading, factoryRoot, now)
	})
	return adm
}
