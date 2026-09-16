package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/statusline"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// af dispatch-admit is #672's pre-act sub-agent-dispatch capacity gate: the PreToolUse sub-agent-tool
// hook (Task|Agent) that REFUSES a launch when admitting it would push a shared backend pool past its
// declared capacity. It
// is the first hook in the tree that emits a blocking permissionDecision:"deny" — its two siblings
// (containment.go PreToolUse, subagent_observer.go PostToolUse) deliberately only ever emit
// additionalContext and exit 0, because ADR-007 made "hooks never block" a rule. This one blocks under
// the ADR-007 2026-08-31 amendment, which grants exactly ONE enumerated exemption — this gate —
// bounded by five conditions: arithmetic only, fail-open on any input error, structurally inert where
// no capacity fact is declared, every refusal a recorded intervention, scope = new sub-agent dispatch.
//
// Everything the observer cannot do, this can, and only this: it fires BEFORE the launch, so its lever
// is the launch itself rather than the next one. The operand is the one no existing site computes — a
// CROSS-SESSION sum of live contexts on the same backend versus that backend's declared pool, not the
// launcher's own window (that is the observer's operand, and it is too local to see an oversubscribed
// pool). The observer stays; this is added in front of it, not in its place.
var dispatchAdmitCmd = &cobra.Command{
	Use:   "dispatch-admit",
	Short: "Refuse a sub-agent launch that would oversubscribe a shared backend pool (PreToolUse hook).",
	Long: `Dispatch-admit intercepts Claude Code's PreToolUse hook on the sub-agent tool (named
Agent on current Claude Code, Task on older builds). When the tokenomics dispatch mechanism is
on and the launching agent's backend declares a shared pool (an operator-set AF_BACKEND_POOL_TOKENS
on a profile that also declares an ANTHROPIC_BASE_URL), it sums the live contexts on that backend, holds a reservation for
the launch under consideration, and emits permissionDecision:"deny" with the arithmetic when
admitting it would breach the pool or leave less than the child-footprint floor
(AF_BACKEND_CHILD_FLOOR_TOKENS, default 50k) free — counselling af handoff when the launcher's own
session alone leaves no room, otherwise to launch one at a time. A profile that sets
AF_DISABLE_PARALLEL_SUBAGENTS=1 is instead a hard semaphore of one: a second sub-agent is refused
while a sibling still runs. Every refusal is recorded as an intervention. On any
resolution error, or on a cloud profile that declares no capacity fact, it admits (fails open)
and stays silent. It is inert by construction where there is no pool to divide.`,
	RunE: runDispatchAdmitCmd,
}

func init() {
	rootCmd.AddCommand(dispatchAdmitCmd)
}

// dispatchReservationSafetyTTL is the CRASH BACKSTOP for a reservation marker — the outer bound past
// which an admitted-but-never-retired launch is swept even though no completion signal ever arrived.
// The PRIMARY retirement path is the SubagentStop hook (af dispatch-retire), which retires one marker
// per stop event (#669 THREAD-1); this TTL catches a child that dies without signalling. It is the
// recovery progress-backstop order (2h), NOT the old 5-minute window: a 5-minute sweep retired a
// still-running child's marker and reopened the [BAD-1] oversubscription. Because a live child keeps
// its marker until it actually finishes, an in-process sub-agent that writes no occupancy snapshot
// still contributes its reservation to the sum for its whole lifetime, not just five minutes.
//
// For the CAP SLOT this backstop is liveness-conditioned as of #673 (Gap 5): a slot older than the TTL
// whose child is demonstrably still writing is retained, because the event this TTL was written to
// backstop turned out not to mean what it says — see dispatch_release.go. Evidence absent or
// unmeasurable still reclaims exactly as before, so a crashed child can never wedge the cap.
const dispatchReservationSafetyTTL = 2 * time.Hour

// subagentQuietReleaseSecs is how long a stopped child's sidechain must have been SILENT before the cap
// slot it holds may be released (#673 D-4). It is the threshold the whole evidence ladder compares
// against; dispatch_release.go is where it is applied.
//
// The derivation is ">= 1.5x the longest silence a live sub-agent was actually measured to take". A
// live 62.8-minute child was observed going quiet for 757.6s mid-run (.analysis/673/rootcause_concern_11.md,
// 329 records, p50 gap 2.1s), so any window under ~15 min lies toward EARLY release — the #673
// direction. 1.5 x 757.6s = 1136s, rounded up to the 20-minute recovery-staleness idiom. The rejected
// candidate was 900s, which left only 19% headroom above the measured gap, the same too-thin margin
// that made the old 5-minute reservation TTL an oversubscription bug.
//
// It bounds both directions: a vanished child wedges the slot for at most this window rather than the
// 2h backstop, and a live-but-silent child is exposed only beyond it rather than from ~7 minutes in.
// It is a structural constant, never a learned scalar (the house pattern below) — raise it if Phase 5's
// measured gap distribution falsifies the 757.6s maximum, never tune it down without re-measuring.
//
// It is deliberately NOT the operator-set CLAUDE_ASYNC_AGENT_STALL_TIMEOUT_MS (a Claude Code CLI env
// var, supplied via a models.json profile). That one gates a STILL-RUNNING agent that has gone silent;
// this one gates a child that has already emitted SubagentStop and whose sidechain has since fallen
// quiet — orthogonal events, so matching the two would be wrong, not merely unnecessary. A child still
// writing its sidechain always retains the slot (liveness-conditioned, dispatch_release.go), so this
// window only ever exposes a genuinely finished-and-silent holder, never masks a live one.
const subagentQuietReleaseSecs = 1200 * time.Second

// dispatchReservationNumer/Denom is the reservation scalar k = 3/5 (#672 D9a). The reservation the
// gate holds for the launch under consideration is k·(ceiling − Σmeasured): held against the REMAINING
// headroom rather than a fixed pool share so the algebra admits EXACTLY the first child and refuses the
// second for ANY launcher occupancy S in [0, ceiling) — 1st load 0.4S+0.6C ≤ C, 2nd load (ledger ×2)
// 1.2C−0.2S > C. Any k in (½,1) gives that admit-1/refuse-2 shape; 3/5 sits centrally. It is a
// structural tuning scalar, never a learned appetite — shrink it toward ½ if acceptance shows it
// over-refusing the first child.
const (
	dispatchReservationNumer = 3
	dispatchReservationDenom = 5
)

// reasonChildFloorHandoff is the BackendVerdict.Reason the verb stamps on a child-floor refusal that the
// LAUNCHER'S OWN occupancy caused: when the launcher alone already leaves less than a child's footprint
// free, serializing further launches cannot open room, so the deny counsels `af handoff` instead of the
// sibling-contention serialize text. Every other refusal — headroom, or a floor breach the SIBLINGS
// caused, or one where the launcher's own reading did not resolve — carries an empty Reason and gets the
// serialize counsel. It is a plain string so BackendVerdict stays ==-comparable (#669 F1/BAD-4).
const reasonChildFloorHandoff = "childfloor-launcher"

// reasonSequentialOnly is the BackendVerdict.Reason the verb stamps on a HARD-CAP refusal: the backend
// declares AF_DISABLE_PARALLEL_SUBAGENTS and a sub-agent is already running, so this launch is refused
// with no pool arithmetic — the cap is a semaphore of one, not a token calculation. Its deny sentence
// therefore omits the pool figures the other refusals carry (#672 hard cap).
const reasonSequentialOnly = "sequential-only"

// dispatchRefusalReasons is the CLOSED vocabulary a refusal may carry, declared once so the two
// surfaces that render it — dispatchDenyReason for the agent being refused, dispatchRefusalRelay for
// the observer counselling the next launch — can be checked against the same list rather than against
// each other's memory.
//
// It exists because "closed" was otherwise a claim in a comment. A fourth reason added to the consts
// above and not here, or here and not to both renderers, is the silent degradation this whole change
// is about: the deny would still fire, the relay would fall through to "" and the counsel channel for
// that class of refusal would go quiet with nothing failing.
// TestDispatchRefusalVocabularyIsClosed reads the consts out of this file's source and fails if this
// list does not name every one of them.
var dispatchRefusalReasons = []string{"", reasonChildFloorHandoff, reasonSequentialOnly}

// sequentialSlotName is the single fixed-name reservation file the hard cap claims (#672). Unlike the
// arithmetic path's pid+nanotime markers, the cap needs exactly ONE well-known file so an O_EXCL create
// is an atomic "claim the only slot": the first launch creates it and admits, a concurrent second gets
// EEXIST and is refused — race-safe even when three Agent calls arrive in a single message.
//
// The SubagentStop retire hook does NOT remove it. That event fires while a background child is still
// running (measured: ~7 minutes into a ~2 hour child), so as of #673 retire only writes a
// sequential.stop PROPOSAL beside it and the next claim disposes of the slot once the evidence ladder
// agrees the child went quiet. dispatchReservationSafetyTTL still reclaims a slot whose child died
// without signalling. See dispatch_release.go for the state machine.
const sequentialSlotName = "sequential.slot"

// dispatchLastRefusalName is the fixed-name breadcrumb the gate overwrites on every refusal, and the
// only thing the PostToolUse observer reads before counselling (#673 item 1, AC-1). One verdict, one
// computer: the gate decides and writes it down, the observer relays what is written. A fixed name is
// deliberate — the observer wants the LATEST refusal, so an overwrite is the whole datum and a
// directory of per-refusal files would only invite a second reader to aggregate them into a second
// verdict.
const dispatchLastRefusalName = "dispatch_admit_last_refusal.json"

// dispatchLastRefusalVersion is stamped by the WRITER, never supplied by a caller — the same rule
// sequentialStopVersion follows (dispatch_release.go:64-66), writeModelCoverageRecord follows
// (config_models.go:828), and recoveryStateVersion follows. A reader that meets an unrecognised
// version treats the file as absent: for THIS record that means silence, which is the safe direction
// because false counsel about capacity is worse than no counsel.
const dispatchLastRefusalVersion = 1

// dispatchAdmitPayload is the subset of the PreToolUse hook JSON this command reads. Like the observer
// it never reads tool_input: the tool is Task, whose input is a whole prompt, and this gate's
// arithmetic is content-blind by construction (the ADR-007 amendment's condition 1). ToolUseID is a
// top-level sibling of tool_name, not a tool_input field, so reading it keeps that content-blindness. It
// is recorded beside the slot stamp at claim so this child's completion record can free the slot. An
// absent id decodes to "" and the release falls back to the quiet timer.
type dispatchAdmitPayload struct {
	ToolName  string `json:"tool_name"`
	Cwd       string `json:"cwd"`
	ToolUseID string `json:"tool_use_id"`
}

func runDispatchAdmitCmd(cmd *cobra.Command, _ []string) error {
	p, ok := readDispatchAdmitPayloadFromStdin()
	if !ok {
		return nil
	}
	if p.Cwd == "" {
		if wd, err := getWd(); err == nil {
			p.Cwd = wd
		}
	}
	return runDispatchAdmitCore(cmd.Context(), cmd.OutOrStdout(), p, time.Now())
}

func readDispatchAdmitPayloadFromStdin() (dispatchAdmitPayload, bool) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return dispatchAdmitPayload{}, false
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return dispatchAdmitPayload{}, false
	}
	var p dispatchAdmitPayload
	if err := json.NewDecoder(os.Stdin).Decode(&p); err != nil {
		return dispatchAdmitPayload{}, false
	}
	return p, true
}

// runDispatchAdmitCore is the testable core, and it returns nil on EVERY path. Every resolution below
// can fail on a healthy host — an agent outside a factory, a config that will not load, a session with
// no snapshot — and each is a reason to ADMIT (fail open, ADR-007 amendment condition 2), never to
// fail a hook. A refusal is emitted on exactly one path: a positive no-fit against a declared pool.
func runDispatchAdmitCore(ctx context.Context, out io.Writer, p dispatchAdmitPayload, now time.Time) error {
	if !isSubagentTool(p.ToolName) || p.Cwd == "" {
		return nil
	}
	factoryRoot, err := resolveInvokerRoot(p.Cwd)
	if err != nil {
		return nil
	}
	launcher, err := resolveAgentName(p.Cwd, factoryRoot)
	if err != nil || launcher == "" {
		return nil
	}
	startupCfg, err := config.LoadStartupConfig(factoryRoot)
	if err != nil {
		return nil
	}
	policy := tokenomics.ResolvePolicy(
		tokenomicsFactoryEnabled(factoryRoot) && startupCfg.Tokenomics.Enabled != "off",
		startupCfg.Tokenomics).WithContextThreshold(startupCfg.Recovery.ContextThresholdPct)
	// Asked before the expensive gather, the observer's cost idiom: this hook fires on every Task in
	// every factory, and a factory that has never armed the dispatch mechanism should not pay a
	// models.json load and a whole-roster occupancy sweep to be told nothing. WithinBackendCapacity
	// asks again — it owes its own callers the answer — and the duplication is the point.
	if !policy.On(tokenomics.MechanismDispatch) {
		return nil
	}

	modelsCfg, err := config.LoadModelsConfig(factoryRoot)
	if err != nil || modelsCfg == nil {
		// AC-8, the visible fail-open. The gate is ARMED — the dispatch mechanism is on — but an
		// enforcement input will not resolve: the model registry that names each backend is
		// unreadable, so the launcher's pool cannot be identified. Admit (fail open) AND record the
		// admission, so a broken gate reproduces today's behavior WITHOUT being silent. Everything
		// before this point is either not-a-factory (no root/agent/config to record against) or the
		// mechanism being off (not armed); everything after is a resolved capacity fact or its
		// AC-6-inert absence, which is a decision rather than an error.
		observeFailOpen(ctx, factoryRoot, p.Cwd, launcher, now)
		return nil
	}

	// The launcher's backend. resolveRecordModel honours its per-launch model override, so the pool
	// this launch is judged against is the pool it is actually launching onto.
	launcherModel, _ := resolveRecordModel(factoryRoot, p.Cwd, launcher, "")
	launcherKey := config.NormalizedEndpoint(modelsCfg.Models[launcherModel])
	if launcherKey == "" {
		// No shared backend to pool against — a cloud profile declares no ANTHROPIC_BASE_URL. This is
		// the isEndpointProfile half of AC-6's cloud inertness (D2/D9c), realized by exclusion rather
		// than by a model-name heuristic: inert by construction, admit, no record, no arithmetic.
		return nil
	}

	poolTokens, declared := config.BackendPoolTokens(modelsCfg.Models[launcherModel])
	if !declared {
		// A backend that declares no operator-set pool fact carries no pool to divide — AC-6, and inert
		// BY CONSTRUCTION (the declared pool fact, not a threshold, a classifier, or the per-request
		// window). Short-circuited here, before the roster sweep, so the cloud common path stays as cheap
		// as "no behavior change" requires; WithinBackendCapacity re-checks the same declared source and
		// is the authority. The per-request window (CLAUDE_CODE_MAX_CONTEXT_TOKENS / ResolveWindow) is
		// left untouched — this gate now reads ONLY the pool fact for its capacity operand.
		return nil
	}
	pool := tokenomics.Window{Tokens: poolTokens, Source: config.WindowSourceDeclared}
	// The child-footprint floor rides beside the pool on the same profile. Unlike the pool it is never
	// absent — an undeclared floor defaults to ~50k rather than going inert — so once a pool is declared
	// the floor always has a footprint to compare against (#669 F1/BAD-4).
	childFloor := config.BackendChildFloorTokens(modelsCfg.Models[launcherModel])

	live, launcherOwn, launcherResolved := sumBackendOccupancy(factoryRoot, modelsCfg, startupCfg, launcher, launcherModel, launcherKey, now)

	// The reservation for the launch under consideration, scaled by the in-message ledger so a second
	// call in the same message sees the first admission counted (D1/D9a/D9b). Held against the
	// remaining headroom: reservation = k·(ceiling − Σmeasured)·(1 + recently-admitted-unmeasured).
	summedMeasured := int64(0)
	for _, occ := range live {
		summedMeasured += tokenomics.ClampAppetite(occ.Tokens, pool.Tokens)
	}
	// The ceiling the reservation is sized against must be the SAME breaker-clamped ceiling the verdict
	// (WithinBackendCapacity) judges Σ against — not the raw margin-only ceiling. When the breaker
	// clamps below the margin (margin 10 → raw 90, clamped 85), the raw ceiling oversizes the
	// reservation and refuses a launch the verdict would admit (#669 F2). policy.ContextThresholdPct is
	// already threaded in at :158 via WithContextThreshold.
	ceilingTokens := pool.Tokens * int64(tokenomics.EffectiveAdmissionCeilingPct(policy.AdmissionMarginPct, policy.ContextThresholdPct)) / 100
	ledgerDir := reservationDir(p.Cwd, launcherKey)
	ledgerCount := countLiveReservations(ledgerDir, dispatchReservationSafetyTTL, now)
	if reservation := reservationTokens(ceilingTokens, summedMeasured, ledgerCount); reservation > 0 {
		live = append(live, tokenomics.Occupancy{Tokens: reservation, Known: true})
	}

	verdict := tokenomics.WithinBackendCapacity(pool, live, policy)
	switch verdict.Verdict {
	case tokenomics.VerdictNoFit:
		refuseLaunch(ctx, out, factoryRoot, p.Cwd, launcher, launcherKey, verdict, now)
	case tokenomics.VerdictAdmit:
		// The headroom predicate admits, but a child-footprint FLOOR still guards the first child near the
		// ceiling — where reservationTokens shrinks toward zero and would let through a launch that leaves
		// no room for the next (#669 F1/BAD-4). It is a SEPARATE predicate over the MEASURED sum
		// (pool − Σmeasured < childFloor), deliberately kept out of reservationTokens (which stays pure)
		// and read against summedMeasured, not the reservation-inclusive verdict sum.
		if pool.Tokens-summedMeasured < childFloor {
			refuseLaunch(ctx, out, factoryRoot, p.Cwd, launcher, launcherKey,
				childFloorVerdict(pool.Tokens, summedMeasured, launcherOwn, launcherResolved, childFloor, policy), now)
			break
		}
		// #672 HARD CAP, composed LAST (#669 F1). An operator's AF_DISABLE_PARALLEL_SUBAGENTS on this
		// backend is a semaphore of one, not an arithmetic question — but it is evaluated only AFTER
		// headroom and the floor admit, because claimSubagentSlot is the ONLY side-effecting (O_EXCL)
		// predicate: ordering it last keeps every earlier refusal side-effect-free and lets the
		// informative arithmetic reasons (which carry pool figures) win over the figure-less semaphore
		// reason. A held slot refuses with the sequential-only counsel.
		if config.ParallelSubagentsDisabled(modelsCfg.Models[launcherModel]) {
			if claimSubagentSlot(ledgerDir, dispatchReservationSafetyTTL, now) {
				// On a successful claim the slot IS the reservation and the sole marker on the cap path:
				// do NOT also write a pid-nanotime marker, which would double-count the one child and let
				// retire remove the wrong marker, wedging the slot to its 2h TTL. Record the PreToolUse
				// tool_use_id beside the slot stamp so this child's completion record can free the slot
				// ahead of the quiet timer. An empty id writes nothing, so the release falls back to the
				// timer rather than to a sidecar that can never match.
				recordSlotClaimToolUse(ledgerDir, p.ToolUseID, now)
			} else {
				refuseLaunch(ctx, out, factoryRoot, p.Cwd, launcher, launcherKey,
					tokenomics.BackendVerdict{Verdict: tokenomics.VerdictNoFit, Reason: reasonSequentialOnly, PoolTokens: pool.Tokens}, now)
			}
			break
		}
		// Record the admission in the ledger so a sibling launched moments later, before this child
		// has a reading of its own, is counted against the pool it just joined.
		writeReservationMarker(ledgerDir, now)
	}
	return nil
}

// childFloorVerdict builds the NoFit verdict for a child-footprint floor breach. It carries the same
// arithmetic the headroom refusal records (pool + the MEASURED sum, not the reservation-inclusive one)
// so both deny paths record an identical shape. It stamps reasonChildFloorHandoff only when the
// launcher's OWN resolved occupancy is itself enough to leave less than childFloor free — the case where
// serializing cannot help and `af handoff` is the only counsel that clears it. A sibling-caused breach,
// or a launcher reading that did not resolve, keeps the empty Reason and so gets the serialize text (the
// milder, fail-open direction).
func childFloorVerdict(poolTokens, summedMeasured, launcherOwn int64, launcherResolved bool, childFloor int64, policy tokenomics.Policy) tokenomics.BackendVerdict {
	v := tokenomics.BackendVerdict{
		Verdict:      tokenomics.VerdictNoFit,
		PoolTokens:   poolTokens,
		SummedTokens: uint64(summedMeasured),
		ProjectedPct: float64(summedMeasured) / float64(poolTokens) * 100,
		HeadroomPct:  float64(tokenomics.EffectiveAdmissionCeilingPct(policy.AdmissionMarginPct, policy.ContextThresholdPct)),
	}
	if launcherResolved && poolTokens-tokenomics.ClampAppetite(launcherOwn, poolTokens) < childFloor {
		v.Reason = reasonChildFloorHandoff
	}
	return v
}

// sumBackendOccupancy gathers the live per-session occupancies sharing the launcher's backend. It is
// the impure half the pure predicate cannot own (policy.go:15-16: this package acts, tokenomics
// decides): a whole-roster occupancy sweep, partitioned by the normalized endpoint each agent's
// resolved profile points at, keeping only the sessions on the same backend as the launcher.
//
// A session whose reading did not resolve (dark, stale, no snapshot, unhealthy) is SKIPPED, never
// counted as zero — so the sum is an honest LOWER bound and a refusal only fires when the resolved
// sessions ALONE breach the pool (AC-8, the fail-open lower bound). The launcher's own session is one
// of the roster members and contributes its own reading like any other.
//
// It also surfaces the launcher's OWN measured occupancy separately (launcherOwn, launcherResolved): it
// is the datum the pure predicate cannot see (it receives an unlabeled slice) and the one the child-
// floor deny needs to tell a launcher-caused breach — where serializing cannot help and `af handoff` is
// the counsel — from sibling contention (#669 F1/BAD-4). launcherResolved is false when the launcher's
// own reading did not resolve, and the caller then falls back to the milder serialize text.
func sumBackendOccupancy(factoryRoot string, modelsCfg *config.ModelsConfig, startupCfg *config.StartupConfig,
	launcher, launcherModel, launcherKey string, now time.Time) (live []tokenomics.Occupancy, launcherOwn int64, launcherResolved bool) {

	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(factoryRoot))
	if err != nil || agentsCfg == nil {
		return nil, 0, false
	}
	roster := make(map[string]struct{}, len(agentsCfg.Agents))
	for name := range agentsCfg.Agents {
		roster[name] = struct{}{}
	}
	readings, _ := statusline.ReadObservations(
		config.StatuslineSessionsDir(factoryRoot),
		statusline.ReadOptions{
			KnownAgents: roster,
			Staleness:   time.Duration(startupCfg.Recovery.StalenessSecs) * time.Second,
			DarkAfter:   time.Duration(startupCfg.Recovery.DarkGraceSecs) * time.Second,
		},
		now)

	for agent, reading := range readings {
		if !reading.IsHealthy() {
			continue
		}
		obs, ok := reading.Observation()
		if !ok {
			continue
		}
		model := launcherModel
		if agent != launcher {
			// Other agents resolve through models.json alone; a per-launch override marker is the
			// launcher's own and rare, and missing it only skips or mis-groups one session — the
			// fail-open lower-bound direction, never a spurious refusal.
			model = modelsCfg.Agents[agent]
			if model == "" {
				model = modelsCfg.Default
			}
		}
		if config.NormalizedEndpoint(modelsCfg.Models[model]) != launcherKey {
			continue // a different backend, or none: neither summed against this pool nor refused by it
		}
		tokens := obs.TokensUsed()
		if agent == launcher {
			launcherOwn = tokens
			launcherResolved = true
		}
		live = append(live, tokenomics.Occupancy{Tokens: tokens, Known: true})
	}
	return live, launcherOwn, launcherResolved
}

// reservationTokens is k·(ceiling − Σmeasured)·(1 + ledger), clamped at zero. Zero when the backend is
// already at or over its ceiling — in which case the measured sum alone already refuses, and no
// reservation is needed to push it over.
func reservationTokens(ceilingTokens, summedMeasured int64, ledgerCount int) int64 {
	headroom := ceilingTokens - summedMeasured
	if headroom <= 0 {
		return 0
	}
	return headroom * dispatchReservationNumer / dispatchReservationDenom * int64(1+ledgerCount)
}

// refuseLaunch emits the blocking deny and records the refusal. Both, and in that order: the deny is
// what stops THIS launch (the agent-facing half), and the record is what makes the refusal retrievable
// afterward with the arithmetic that justified it (ADR-007 amendment condition 4, AC-3). The record
// rides recordEnforcement, NOT recordIntervention, so it survives the telemetry toggle being off —
// which is the default, and a refusal an operator cannot retrieve on run #1 is the exact silence
// corollary 3 forbids.
//
// It never arms the K17 fan-out latch: that latch is the observer's episode discriminator, and a
// second armer would break its single-armer invariant. A refusal is its own record, not a fan-out
// episode.
func refuseLaunch(ctx context.Context, out io.Writer, factoryRoot, workDir, launcher, backendKey string,
	verdict tokenomics.BackendVerdict, now time.Time) {

	emitDispatchDeny(out, dispatchDenyReason(backendKey, verdict))

	ctx = withVerbTelemetry(ctx, verbTelemetry{
		verb: "dispatch-admit", agent: launcher, start: now,
		enabled: telemetryFactoryEnabled(factoryRoot),
	})
	instanceID := readHookedFormulaID(workDir)
	stepID, _, _ := strings.Cut(readStepPrimed(workDir), ":")
	pool := verdict.PoolTokens
	recordEnforcement(ctx, factoryRoot, workDir, launcher, instanceID, func(ev *telemetry.StepEvent) {
		ev.Formula = instanceFormulaName(ctx, workDir, instanceID)
		ev.StepID = stepID
		ev.Mechanism = string(tokenomics.MechanismDispatch)
		ev.Action = telemetry.ActionRefuse
		ev.PoolTokens = &pool
		// The sequential-only cap refusal is a semaphore, not token arithmetic — leaving SummedTokens
		// nil records "not token-measured" rather than the misleading ptr-to-0 "measured zero" the JSON
		// distinguishes (#669 C1). The headroom and child-floor legs carry the real measured sum.
		if verdict.Reason != reasonSequentialOnly {
			summed := int64(verdict.SummedTokens)
			ev.SummedTokens = &summed
		}
	})

	// Last, and outside both of the above: the deny text and the enforcement record are the contract,
	// and the breadcrumb is a courtesy to the observer. Writing it here rather than inside
	// emitDispatchDeny or dispatchDenyReason keeps those two on exactly the bytes they emitted before
	// (U-1a), and writing it after recordEnforcement means a breadcrumb only ever exists for a refusal
	// that was also recorded.
	writeLastRefusal(workDir, backendKey, verdict, now)
}

// dispatchLastRefusal is what the gate hands the observer: the verdict it already computed, not the
// inputs to compute one again. The observer substitutes these fields into a sentence and does no
// arithmetic on them, which is what makes "exactly one component computes the dispatch capacity
// verdict" (#673 AC-1) mechanical rather than aspirational — there is nothing here to divide.
//
// SummedTokens is a pointer for the same reason the intervention record's is (#669 C1): the
// sequential-only cap is a semaphore, and nil records "not token-measured" where a ptr-to-0 would
// claim the false "measured zero".
type dispatchLastRefusal struct {
	V            int       `json:"v"`
	TS           time.Time `json:"ts"`
	Backend      string    `json:"backend"`
	Reason       string    `json:"reason"`
	PoolTokens   int64     `json:"pool_tokens"`
	SummedTokens *int64    `json:"summed_tokens"`
}

// writeLastRefusal overwrites the fixed-name breadcrumb with the refusal just emitted. Every failure
// path is a bare return: a breadcrumb that does not land costs the observer its counsel and costs the
// operator nothing else, because the deny already stopped the launch and the enforcement record
// already captured the arithmetic. Counsel lost, never false counsel — and never a hook failure
// (ADR-007).
//
// It does NOT arm the K17 fan-out latch. The gate writes; the observer reads and arms. A second armer
// would break the latch's single-armer invariant, which is the whole reason the observer can treat one
// arming as one fan-out episode.
func writeLastRefusal(workDir, backendKey string, verdict tokenomics.BackendVerdict, now time.Time) {
	rec := dispatchLastRefusal{
		V:          dispatchLastRefusalVersion,
		TS:         now.UTC(),
		Backend:    backendKey,
		Reason:     verdict.Reason,
		PoolTokens: verdict.PoolTokens,
	}
	if verdict.Reason != reasonSequentialOnly {
		summed := int64(verdict.SummedTokens)
		rec.SummedTokens = &summed
	}
	data, err := json.MarshalIndent(&rec, "", "  ")
	if err != nil {
		return
	}
	path := filepath.Join(workDir, ".runtime", dispatchLastRefusalName)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(path, data, 0o644)
}

// readLastRefusal returns the recorded refusal, or ok=false for every way it might not be there:
// absent, empty, truncated, corrupt, or stamped with a version this binary does not speak.
//
// All five collapse to the same answer deliberately, and that answer is the OPPOSITE of
// loadAdvisoryLedger's. An unreadable advisory ledger costs one duplicate advisory, so it is read
// permissively; an unreadable refusal breadcrumb would cost a sentence telling an operator their
// backend is full when the gate never said so. Between "no counsel" and "counsel that may be wrong",
// a capacity verdict must choose no counsel.
func readLastRefusal(workDir string) (dispatchLastRefusal, bool) {
	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", dispatchLastRefusalName))
	if err != nil {
		return dispatchLastRefusal{}, false
	}
	var rec dispatchLastRefusal
	if err := json.Unmarshal(data, &rec); err != nil {
		return dispatchLastRefusal{}, false
	}
	if rec.V != dispatchLastRefusalVersion || rec.TS.IsZero() {
		return dispatchLastRefusal{}, false
	}
	return rec, true
}

// dispatchRefusalRelay is the observer's whole vocabulary: one sentence per recorded reason, built by
// substituting the breadcrumb's own integers. There is no `/`, no `*` and no float here, and that is
// the point — the observer restates a verdict rather than reaching one.
//
// It deliberately does NOT route through tokenomics.RenderAdvisory. That template formats
// `%.1f%% of a %d-token window` from AdvisoryInputs{ProjectedPct, WindowTokens}, and neither field is
// available honestly: a sequential-only refusal never computed a percentage (it is a semaphore), and
// WindowTokens is the per-request window, a deliberately distinct quantity from the shared pool
// (config/models.go:50-55). Deriving either would re-create the second computer this change deletes.
// MechanismDispatch's registry template survives for the PRIME advisory (D-7) alone.
//
// An unknown reason returns "" and the observer stays silent, so a reason this binary cannot restate
// costs counsel rather than producing a wrong one. That is the safe direction but a lossy one, which
// is why dispatchRefusalReasons and its test exist: silence must be the answer to a CORRUPT record,
// never to a reason someone added upstream and forgot to teach this switch.
func dispatchRefusalRelay(rec dispatchLastRefusal) string {
	switch rec.Reason {
	case reasonSequentialOnly:
		return fmt.Sprintf("dispatch capacity: the `af dispatch-admit` gate refused a sub-agent launch on backend %s "+
			"because it is configured for sequential sub-agents only (AF_DISABLE_PARALLEL_SUBAGENTS). Launch the "+
			"next sub-agent only after the current one has completed.", rec.Backend)
	case reasonChildFloorHandoff:
		if rec.SummedTokens == nil {
			return ""
		}
		return fmt.Sprintf("dispatch capacity: the `af dispatch-admit` gate refused a sub-agent launch on backend %s "+
			"at %d of %d pool tokens; your own session alone leaves less than a child's footprint free, so "+
			"serializing cannot open room. Hand off to a fresh session with `af handoff` before dispatching another.",
			rec.Backend, *rec.SummedTokens, rec.PoolTokens)
	case "":
		if rec.SummedTokens == nil {
			return ""
		}
		return fmt.Sprintf("dispatch capacity: the `af dispatch-admit` gate refused a sub-agent launch on backend %s "+
			"at %d of %d pool tokens. Launch the remaining sub-agents one at a time, waiting for each to make "+
			"progress before the next.", rec.Backend, *rec.SummedTokens, rec.PoolTokens)
	}
	return ""
}

// observeFailOpen writes AC-8's visible fail-open record: the armed gate ADMITTED a launch it could
// not judge because an enforcement input would not resolve. It emits NOTHING to the model — failing
// open means the launch proceeds untouched — and rides recordEnforcement (un-gated by the telemetry
// toggle) for the same reason a refusal does: a broken gate an operator cannot retrieve on run #1 is
// the silence corollary 3 forbids. It carries no pool/summed arithmetic because the error is exactly
// that the arithmetic could not be assembled; the record's value is that the gate fired-open at all,
// retrievable through the same intervention surface as a refusal.
func observeFailOpen(ctx context.Context, factoryRoot, workDir, launcher string, now time.Time) {
	ctx = withVerbTelemetry(ctx, verbTelemetry{
		verb: "dispatch-admit", agent: launcher, start: now,
		enabled: telemetryFactoryEnabled(factoryRoot),
	})
	instanceID := readHookedFormulaID(workDir)
	stepID, _, _ := strings.Cut(readStepPrimed(workDir), ":")
	recordEnforcement(ctx, factoryRoot, workDir, launcher, instanceID, func(ev *telemetry.StepEvent) {
		ev.Formula = instanceFormulaName(ctx, workDir, instanceID)
		ev.StepID = stepID
		ev.Mechanism = string(tokenomics.MechanismDispatch)
		ev.Action = telemetry.ActionObserve
	})
}

// dispatchDenyReason is the sentence the refused agent sees. Both branches carry the same arithmetic so
// the refusal can be argued with rather than merely obeyed; they differ only in the action counselled.
// A launcher-caused child-floor breach (reasonChildFloorHandoff) counsels `af handoff`, because when the
// launcher alone leaves less than a child's footprint free, serializing cannot open room. Every other
// refusal — headroom, or a sibling-caused floor breach — keeps the serialize counsel: launch the
// remaining sub-agents one at a time, letting the backend drain between them.
func dispatchDenyReason(backendKey string, v tokenomics.BackendVerdict) string {
	if v.Reason == reasonSequentialOnly {
		return fmt.Sprintf("dispatch: backend %s is configured for sequential sub-agents only "+
			"(AF_DISABLE_PARALLEL_SUBAGENTS) and one sub-agent is already running. Launch the next only "+
			"after the current one has completed — this backend cannot run sub-agents in parallel.", backendKey)
	}
	if v.Reason == reasonChildFloorHandoff {
		return fmt.Sprintf("dispatch capacity: backend %s is at %d of %d pool tokens (%.0f%% projected, %.0f%% ceiling); "+
			"your own session alone leaves less than a child's footprint free, so serializing the remaining "+
			"sub-agents cannot open room. Hand off to a fresh session with `af handoff` before dispatching another.",
			backendKey, v.SummedTokens, v.PoolTokens, v.ProjectedPct, v.HeadroomPct)
	}
	return fmt.Sprintf("dispatch capacity: backend %s is at %d of %d pool tokens (%.0f%% projected, %.0f%% ceiling); "+
		"launching another sub-agent now would oversubscribe the shared context pool and risk recycling a "+
		"sibling mid-turn. Launch the remaining sub-agents one at a time, waiting for each to make progress "+
		"before the next.", backendKey, v.SummedTokens, v.PoolTokens, v.ProjectedPct, v.HeadroomPct)
}

// emitDispatchDeny writes the PreToolUse deny decision. permissionDecision:"deny" with a reason is the
// proven blocking shape (decision-gating-verification.md); the command still exits 0, because the
// refusal lives in this JSON, not in a non-zero code that Claude Code would read as a hook malfunction.
func emitDispatchDeny(out io.Writer, reason string) {
	var payload struct {
		HookSpecificOutput struct {
			HookEventName            string `json:"hookEventName"`
			PermissionDecision       string `json:"permissionDecision"`
			PermissionDecisionReason string `json:"permissionDecisionReason"`
		} `json:"hookSpecificOutput"`
	}
	payload.HookSpecificOutput.HookEventName = "PreToolUse"
	payload.HookSpecificOutput.PermissionDecision = "deny"
	payload.HookSpecificOutput.PermissionDecisionReason = reason
	_ = json.NewEncoder(out).Encode(&payload)
}

// reservationDir is where the in-message ledger markers for one backend live: under the launcher's own
// .runtime, keyed by a hash of the normalized endpoint so two backends do not share a counter. The
// same orchestrator makes every launch in one message, so its own runtime dir is exactly the shared
// surface those rapid successive calls need — and a hash keeps a base URL out of a filesystem path.
func reservationDir(workDir, backendKey string) string {
	h := sha256.Sum256([]byte(backendKey))
	return filepath.Join(workDir, ".runtime", "dispatch_admit_reservations", fmt.Sprintf("%x", h[:8]))
}

// clearDispatchReservations removes a launcher's whole sub-agent reservation ledger — every backend's
// arithmetic markers and, above all, any held sequential.slot. It is called only where the session's
// children cannot outlive the clear: at formula completion (cleanupRuntimeArtifacts) and at relaunch
// (af up). A child whose session was torn down before it emitted SubagentStop leaves a slot no reaper
// clears before the 2h TTL, so the next session's FIRST launch is refused with a false "one sub-agent
// is already running" (#669 F5, scenario i). Best-effort: a missing tree is a nil RemoveAll.
func clearDispatchReservations(workDir string) {
	_ = os.RemoveAll(filepath.Join(workDir, ".runtime", "dispatch_admit_reservations"))
}

// claimSubagentSlot is the #672 hard cap's atomic gate: it returns true iff THIS launch may proceed as
// the single permitted sub-agent. The slot is one fixed-name file created with O_EXCL, so the
// check-and-claim is a single atomic filesystem operation — two launches racing in one message cannot
// both succeed, exactly the guarantee the read-then-write arithmetic ledger cannot give. A held slot
// older than ttl is a child that died without a SubagentStop; it is reclaimed. Every failure fails OPEN
// (returns true / admits): a slot we cannot manage must never manufacture a false refusal — the cap is a
// safety limit, not a correctness gate, and admitting-on-error only reproduces the pre-cap behavior.
func claimSubagentSlot(dir string, ttl time.Duration, now time.Time) bool {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return true
	}
	slot := filepath.Join(dir, sequentialSlotName)
	// The loop exists for ONE case: finding the slot VANISHED between the failed O_EXCL create and the
	// look that follows it. That is not an error, it is a reclaim in flight — the slot is absent for the
	// few syscalls a reclaim takes — and treating it as one would fail OPEN and admit alongside whoever
	// re-creates it. Looking again is the correct response, and the create at the top of the next turn
	// is how this launcher wins the slot outright if the reclaimer refused it.
	for range claimSlotRaceLooks {
		switch err := tryCreateSlot(slot, now); {
		case err == nil:
			return true // claimed the only slot
		case !os.IsExist(err):
			return true // some other FS error — fail open, never a false refusal
		}
		// EEXIST: a slot is held. Every fact this launch judges it by — its age AND which claim it is —
		// is taken from ONE open file, so the two cannot describe different claims. Reading them as two
		// separate path lookups let an mtime from the dead claim be paired with the content of the live
		// one that replaced it, and that pairing passes every identity check while being about nothing
		// that ever existed.
		//
		// An unopenable slot is either a reclaim in flight — the slot is absent for the few syscalls one
		// takes — or a dirent this process genuinely cannot manage. Those two are INDISTINGUISHABLE in a
		// single observation (a dangling symlink and a deleted file both report ENOENT, and probing with
		// Lstat afterwards just races the reclaimer's re-create), and they differ only in whether they
		// persist. So look again rather than guess. Guessing "unmanageable" fails open and admits
		// alongside whoever is mid-reclaim, which is the concurrency the cap exists to prevent; the
		// persistent case is settled after the loop, where it still fails open per #669 N1.
		//
		// This leg stays FIRST: consulting the evidence ladder ahead of it would let a "not releasable"
		// answer turn an input error into a refusal, silently reverting N1.
		info, held, ok := readHeldSlot(slot)
		if !ok {
			continue
		}
		// A held slot with a stop proposal whose evidence says the child finished is released HERE rather
		// than by the retire hook (#673): retire proposes, admit disposes, lazily at the next claim. Every
		// "cannot tell" leg inside slotReleasable retains, so this can only ever free a slot on positive
		// evidence of quiet.
		//
		// Each reclaim decision RETURNS its own outcome rather than falling through to the next. A failed
		// reclaim means another launcher already holds the slot, and re-deciding against an observation
		// taken before that happened would judge the winner's fresh slot by the dead one's evidence and
		// take it away.
		if rung := slotReleaseRungOf(dir, held, now); rung != rungRetain {
			reclaimed := reclaimSlot(slot, held, now)
			if reclaimed {
				// Record which rung freed the slot so a completion-format drift surfaces as the timer
				// firing rather than as a silent 20-minute wait. Written only on a WON reclaim so the
				// breadcrumb describes a release this launcher actually performed; best-effort, a lost
				// breadcrumb costs counsel, never a wrong release.
				writeSlotReleaseAudit(dir, rung, now)
			}
			return reclaimed
		}
		// The 2h crash backstop, now liveness-conditioned (Gap 5): a slot past the TTL whose child is
		// demonstrably STILL writing is retained, because the completion event this backstop was written
		// to catch turned out to fire mid-lifetime. Evidence absent or unmeasurable reclaims exactly as
		// before, so a crashed child still cannot wedge the cap (AC-D4-4).
		if now.Sub(info.ModTime()) > ttl && !slotEvidenceLive(dir, now) {
			return reclaimSlot(slot, held, now)
		}
		return false
	}
	// Every look found a slot that could not be opened. Persistence is now the answer the single
	// observation could not give: a dirent still standing there that still cannot be opened is #669 N1's
	// unmanageable slot and fails OPEN, exactly as the dangling-symlink pin requires. Anything else is
	// contention this launch lost, and refusing is both the safe direction and the likely truth.
	if _, _, ok := readHeldSlot(slot); !ok && !slotVanished(slot) {
		return true
	}
	return false
}

// claimSlotRaceLooks bounds the re-looks above. A reclaim holds the slot absent for a handful of
// syscalls, so two looks already cover it; the third is slack. Unbounded retrying would turn a hook
// that must return promptly into one that spins against a pathological neighbour.
const claimSlotRaceLooks = 1

// readHeldSlot takes one atomic observation of a held slot: its metadata and its content, from a single
// open file descriptor, so both describe the same inode however much the path churns around them.
func readHeldSlot(slot string) (os.FileInfo, string, bool) {
	f, err := os.Open(slot)
	if err != nil {
		return nil, "", false
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil {
		return nil, "", false
	}
	held, err := io.ReadAll(f)
	if err != nil {
		return nil, "", false
	}
	return info, string(held), true
}

// slotVanished distinguishes "the dirent is GONE" from "the dirent is there but cannot be stat'd".
// os.Stat follows symlinks, so a DANGLING symlink and a deleted file both report ENOENT through it, and
// those two must not be treated alike: the first is #669 N1's fail-open case, the second is a reclaim
// race that fail-open would turn into a double admit. os.Lstat does not follow, so it tells them apart.
func slotVanished(slot string) bool {
	_, err := os.Lstat(slot)
	return os.IsNotExist(err)
}

// reclaimSlot takes a held slot away from whoever left it and re-claims it for this caller, returning
// whether that succeeded. held is the slot content the caller's decision was made about.
//
// The rename is the ARBITER, the improvement.go:552 pattern: there is no cross-process lock here
// (lock.Acquire is advisory and TOCTOU-prone), so of two racing claimers only the one whose os.Rename
// returned nil moved the file. The previous shape was remove-then-recreate, where both racers could
// observe the gap between the remove and the O_EXCL create and BOTH admit, defeating the semaphore
// precisely when contention made it matter (Gap 7 / VR-20). The corpse name is unique per process so two
// racers never collide on it either.
//
// The rename alone does NOT make this safe, and stopping there is how the double admit that this
// function was measured producing got written: it arbitrates over a PATH, not over the claim the caller
// judged. Consulting the evidence ladder takes long enough for another launcher to complete a whole
// reclaim underneath this one, and this rename would then move away a slot that launcher's child had
// just legitimately claimed — the semaphore of one admitting two, in exactly the contended moment it
// exists for. So the guard and the content re-read below are load-bearing, not belt-and-braces: under
// the guard, with no other reclaim in flight, the only thing that can create a slot is the O_EXCL fast
// path, which fires only while the slot is ABSENT — and the re-read has just proven it present and
// unchanged. Verifying AFTER the rename instead would mean renaming the mistake back, and the slot is
// absent for the whole of that repair, which is a window a third launcher claims straight through.
//
// Failing to take the guard REFUSES, the safe direction: another launcher is mid-reclaim and one of us
// is about to hold the slot legitimately.
//
// The winner removes the corpse and any stop proposal before re-claiming: a proposal echoing the dead
// slot's stamp can never match the new one, so leaving it behind would only accumulate orphans in a
// ledger whose contents the cap tests read exactly.
func reclaimSlot(slot, held string, now time.Time) bool {
	release, ok := acquireReclaimGuard(filepath.Dir(slot), now)
	if !ok {
		return false
	}
	defer release()
	if got, err := os.ReadFile(slot); err != nil || string(got) != held {
		return false
	}
	corpse := slot + ".reclaim-" + uniqueLedgerStamp(now)
	if err := os.Rename(slot, corpse); err != nil {
		return false
	}
	_ = os.Remove(corpse)
	_ = os.Remove(filepath.Join(filepath.Dir(slot), sequentialStopName))
	// The claim sidecar carries the PREVIOUS child's tool_use_id; drop it here beside the stop proposal so
	// a stale id can never survive to match a later child's completion record. Defence in depth over the
	// SlotStamp==held join, which already rejects a mismatched sidecar.
	removeSlotClaim(filepath.Dir(slot))
	return tryCreateSlot(slot, now) == nil
}

// acquireReclaimGuard serializes reclaims of one ledger's slot, returning a release func and whether it
// was taken. It is the same O_EXCL claim the slot itself uses, one level up: the primitive is already
// trusted here for exactly this, and reaching for internal/lock instead would be worse — it is advisory,
// PID-keyed, and these launchers have no per-child PID to key on.
//
// The guard is held across a handful of syscalls, never across the evidence ladder, so a guard older
// than the window below is a process that died mid-reclaim rather than one still working. Stealing it
// costs nothing to get wrong in the retain direction and everything to omit: an unreapable guard would
// wedge the cap past the 2h backstop, which is the wedge AC-D4-4 forbids.
//
// The release is conditioned on the guard still holding THIS caller's bytes. A guard stolen out from
// under us belongs to someone else by then, and removing it would drop that reclaim's cover.
func acquireReclaimGuard(dir string, now time.Time) (func(), bool) {
	guard := filepath.Join(dir, sequentialReclaimName)
	mine, err := createStateFile(guard, now)
	if err != nil {
		if !os.IsExist(err) {
			return nil, false
		}
		var ok bool
		if mine, ok = stealReclaimGuard(guard, now); !ok {
			return nil, false
		}
	}
	return func() { releaseReclaimGuard(guard, mine) }, true
}

// stealReclaimGuard takes over a guard left behind by a process that died mid-reclaim, returning the
// content it wrote for the new one.
//
// A guard that EXISTS but cannot be read is stealable outright, and that is the whole point of reading
// it through readHeldSlot rather than stat'ing it. os.Stat follows symlinks, so #669 N1's dangling
// symlink — one level up from the slot, where the same trick already applies — makes staleness
// unjudgeable; treating unjudgeable as fresh makes the guard forever-held, refuses every reclaim, and
// wedges the cap past the 2h backstop this guard exists to keep reachable. That is exactly the failure
// its own docstring promises not to cause, so the unreadable case resolves toward stealing.
//
// The rename is the arbiter, as in reclaimSlot, and the corpse's content is checked against the guard
// this caller actually judged stale. A mismatch means a fresher guard was moved — someone re-created it
// between the read and the rename — so this caller REFUSES rather than proceeding. It does not restore
// what it moved: putting it back is itself a race against whoever creates the next guard, and the guard
// is defence in depth over reclaimSlot's content-conditioned rename, which arbitrates on its own.
func stealReclaimGuard(guard string, now time.Time) (string, bool) {
	info, held, readable := readHeldSlot(guard)
	if readable && sinceNotBefore(now, info.ModTime()) <= sequentialReclaimGuardTTL {
		return "", false
	}
	corpse := guard + ".stale-" + uniqueLedgerStamp(now)
	if err := os.Rename(guard, corpse); err != nil {
		return "", false
	}
	got, err := os.ReadFile(corpse)
	_ = os.Remove(corpse)
	if readable && (err != nil || string(got) != held) {
		return "", false
	}
	content, createErr := createStateFile(guard, now)
	if createErr != nil {
		return "", false
	}
	return content, true
}

func releaseReclaimGuard(guard, mine string) {
	if got, err := os.ReadFile(guard); err != nil || string(got) != mine {
		return
	}
	_ = os.Remove(guard)
}

// tryCreateSlot attempts the atomic O_EXCL create and returns the raw error so the caller can tell EEXIST
// (a held slot) from any other filesystem failure (which fails open).
//
// The content is unique per claim, not a bare timestamp: sequential.stop echoes it as the slot_stamp
// anti-replay join, and a join is only sound if two claims cannot produce identical bytes. A bare
// RFC3339Nano could, whenever two claims are driven from the same clock reading — which every test
// that reuses one `now` does, and which the reclaim path does by construction.
func tryCreateSlot(path string, now time.Time) error {
	_, err := createStateFile(path, now)
	return err
}

// createStateFile is tryCreateSlot for callers that must later prove the file is still THEIRS: it hands
// back the bytes it wrote. The reclaim guard needs that to release only its own guard, and a caller that
// re-read the file to learn its own content would be reading whatever replaced it.
func createStateFile(path string, now time.Time) (string, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	content := uniqueLedgerStamp(now) + " " + now.UTC().Format(time.RFC3339Nano) + "\n"
	_, _ = f.WriteString(content)
	if err := f.Close(); err != nil {
		return "", err
	}
	return content, nil
}

// uniqueLedgerStamp is the ledger's pid+nanotime uniqueness idiom, shared by the arithmetic marker
// NAME, the cap slot's CONTENT and the reclaim corpse's suffix. It is one function rather than three
// spellings because the stamp and the corpse name drifting apart is the kind of divergence that only
// shows up as a stale file nobody can explain.
//
// The trailing sequence is what makes "unique" true rather than merely likely. pid+nanotime alone
// distinguishes stamps only when the two callers read DIFFERENT clocks, and now is a parameter here,
// not a fresh reading: reclaimSlot re-claims with the very now its caller was already handed, so a
// reclaim can reproduce the outgoing claim's bytes exactly. sequential.stop's slot_stamp is an
// equality join against that content, so two identical claims make a stale proposal match a live slot —
// #673's release-without-evidence through a side door.
func uniqueLedgerStamp(now time.Time) string {
	return strconv.Itoa(os.Getpid()) + "-" + strconv.FormatInt(now.UnixNano(), 10) +
		"-" + strconv.FormatUint(ledgerStampSeq.Add(1), 10)
}

var ledgerStampSeq atomic.Uint64

// countLiveReservations returns how many recently-admitted-but-unmeasured sibling launches this backend
// carries, sweeping expired markers as it goes. A marker older than the TTL describes a child that by
// now has a reading of its own (which the measured sum already counts), so keeping it would double-
// count; deleting it on read keeps the ledger self-cleaning without a separate reaper.
func countLiveReservations(dir string, ttl time.Duration, now time.Time) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	live := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		// Cap state is a semaphore of one, not an in-message arithmetic reservation: on the cap path it
		// is the SOLE marker (decision #1) and claimSubagentSlot owns its lifecycle. Counting it here
		// would double-count the one held child and, worse, oversize the next launch's reservation so an
		// arithmetic NoFit pre-empts the informative sequential-only refusal (#669 C1/F1). Skipping is
		// also what keeps the sweep below from DELETING a sequential.stop proposal once it ages past the
		// TTL, which would silently restore #673 — this loop is a reaper, not just a counter.
		if isCapStateFile(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) > ttl {
			_ = os.Remove(filepath.Join(dir, e.Name()))
			continue
		}
		live++
	}
	return live
}

// writeReservationMarker records one admission in the ledger. The name is pid+nanotime so concurrent
// or back-to-back admits never collide on one file; the content is the timestamp for a human reading
// the dir, though the mtime is what countLiveReservations trusts. A write failure is swallowed: a lost
// marker only weakens the ledger toward admitting (the fail-open direction), never toward a false
// refusal.
func writeReservationMarker(dir string, now time.Time) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	name := uniqueLedgerStamp(now)
	_ = os.WriteFile(filepath.Join(dir, name), []byte(now.UTC().Format(time.RFC3339Nano)+"\n"), 0o644)
}
