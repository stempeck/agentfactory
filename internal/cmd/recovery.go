package cmd

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/claude"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/statusline"
)

// The #596 recovery decision layer: K4 evaluator, K5 durable breaker, K7 executor, K8 verified
// escalation, K14 advisory, K18 step-anchored progress detection, K21 kill-residue hygiene and
// K23 no-step terminal state — plus the three side effects the recycle funnel invokes (K6 event
// log, K19 recycle-fence, K20 re-provisioning).
//
// Two placement decisions carry this design, and both are easy to get wrong by writing the
// obvious thing:
//
//   - C-1: the K6 write and the K19 fence-arm live in the FUNNEL (respawnSession), not in this
//     file's executor. Four of the five recycle classes — crash, error_pattern, compact_handoff,
//     self_handoff — never pass through recoverExhausted at all, so anchoring the audit trail at
//     the executor would have left them unlogged and unfenced while appearing to satisfy AC-6.
//     They live here rather than in helpers.go only because helpers.go is pinned by line number
//     (see recordRecycle).
//
//   - C-2: occupancy growth is NEVER evidence of progress. A degraded backend answers every turn
//     with a useless token-consuming reply, so a wedged agent's occupancy climbs steadily while
//     nothing advances. If growth rewound the breaker, the breaker would be permanently disarmed
//     by exactly the failure it exists to catch. Only a step-anchored signal confirms a recovery.
//
// pollOccupancy is wired into watchdogTick (watchdog.go), which calls it through the
// pollOccupancyFn seam BEFORE pollAgents and then withholds the agents it recycled from that
// tick's pane scope. It is nonetheless still built to be driven directly: every input arrives as
// a parameter, so the whole verdict matrix is exercisable without a running watchdog.

// Recycle-class triggers (K6). The literal values are part of the recovery_log.jsonl contract and
// are asserted separately from the identifier names — renaming a constant must never move the
// bytes that reach the log (the internal/telemetry/event.go:8-10 rule).
const (
	triggerContextExhaustion   = "context_exhaustion"
	triggerDarkAtHighOccupancy = "dark_at_high_occupancy"
	triggerProgressBackstop    = "progress_backstop"
	triggerCrash               = "crash"
	triggerErrorPattern        = "error_pattern"
	triggerCompactHandoff      = "compact_handoff"
	triggerSelfHandoff         = "self_handoff"
	// triggerStepBoundaryHandoff is the #622 C5 cooperative boundary: a step closed at or above
	// step_context.handoff_pct and recycled itself before the next step started. It is a DISTINCT
	// class from self_handoff on purpose — the whole point of the boundary is that it fired
	// instead of the forceful ladder, and a report that cannot tell the two apart cannot show that
	// the cooperative half is working.
	triggerStepBoundaryHandoff = "step_boundary_handoff"

	// triggerUnknown is what an unset RespawnOptions.Trigger records. A future caller that adds a
	// recycle path and forgets to name its class must produce a visibly UNCLASSIFIED line rather
	// than one with an empty trigger — an empty string reads like a decoder fault, which would
	// send an investigator after the log format instead of after the missing call site.
	triggerUnknown = "unknown"
)

// Recycle outcomes. The design enumerates the trigger set (design-doc.md:76) but never the outcome
// set, and the value reaches the same on-disk contract; the funnel has exactly two exits, so these
// are they.
const (
	outcomeRespawned     = "respawned"
	outcomeRespawnFailed = "respawn_failed"
)

// Halt causes. design-doc.md:142 requires operator-facing messages to be built from enumerated
// cause constants rather than free text, so the two latching causes are named.
const (
	haltReasonMaxAttempts = "max_attempts"
	haltReasonRateCap     = "rate_cap"
	haltReasonCorrupt     = "unreadable_breaker_state"
)

// Breaker verdicts as they reach an operator (K10-cli, K11). Like the trigger and ChannelState
// literals these values are an on-wire contract — they are what `af agents list --json` and
// `af dispatch status --json` say — so they are asserted separately from the identifier names.
const (
	recoveryStatusNone       = "none"
	recoveryStatusRecovering = "recovering"
	recoveryStatusHalted     = "halted"
)

// recoveryStatus renders a breaker as the three-value operator-facing verdict.
//
// The "recovering" predicate is an IMPLEMENTER DECISION, not a design requirement: the design and
// the outline name the three values but never define when a non-halted agent is recovering. The
// two signals chosen are the ones that mean "a recovery is in flight and unconfirmed" — an open
// K18 post-recovery confirmation (PendingStepID, cleared at confirmPostRecovery) and a non-zero
// in-window attempt count (Attempts, rewound to 0 ONLY on a confirmed recovery or window expiry).
// Attempts therefore persists across a FAILED recovery, which is exactly the case an operator
// needs to see before it latches.
func recoveryStatus(st recoveryState) string {
	switch {
	case st.Halted:
		return recoveryStatusHalted
	case st.PendingStepID != "" || st.Attempts > 0:
		return recoveryStatusRecovering
	default:
		return recoveryStatusNone
	}
}

// --- K13: the operator reset verb ---------------------------------------------------------------
//
// haltRecovery has named `af recovery reset <agent>` in both halt-escalation subjects since Phase
// 2, so this closes a dangling reference in shipped output, not a nice-to-have. Keep the spelling
// identical to those strings.

var recoveryCmd = &cobra.Command{
	Use:   "recovery",
	Short: "Context-exhaustion recovery commands",
}

var recoveryResetCmd = &cobra.Command{
	Use:   "reset <agent>",
	Short: "Clear an agent's recovery breaker so the watchdog may recycle it again",
	Long: `Clear the durable recovery breaker for one agent.

The breaker latches when recovery has failed repeatedly (or hit the recycle rate
cap) and it never expires on its own — a self-clearing breaker is a breaker that
never stops anything. This is the supported way to clear it after investigating,
and it is the command the RECOVERY HALTED escalation names.

It mutates breaker state ONLY. It does not kill or start sessions, does not touch
worktrees, and does not close beads: after resetting, relaunch with 'af up <agent>'
if the agent is not running.`,
	Args: cobra.ExactArgs(1),
	RunE: runRecoveryReset,
}

func init() {
	recoveryCmd.AddCommand(recoveryResetCmd)
	rootCmd.AddCommand(recoveryCmd)
}

// recoveryResetRefusal is deliberately NOT the K1 teardown refusal. Reusing that body would ship a
// message claiming this command "stops the whole factory ... would kill YOU", which is false for a
// verb that removes one JSON file — and would add a fifth surface to a set three tests enumerate
// as four (authority.go:25-32, authority_test.go:13-21). security.md D5-B is explicit that the
// reset introduces no new teardown surface. Like the K1 text it never names the detection
// mechanism: ux.md L36-39 forbids handing the agent a bypass recipe.
const recoveryResetRefusal = `recovery reset refused: agent context (af recovery reset)
The breaker latched because recovery already failed repeatedly for this agent, and
it stays latched until a human has looked at why. Clearing it is an operator action.
Do NOT retry and do NOT clear it another way. If you believe the breaker should be
cleared, tell your operator (af mail send manager -s "recovery reset request" -m "...")
and continue with your remaining work.`

// runRecoveryReset clears one agent's durable breaker and prints what it did.
//
// It REMOVES the file rather than loading, clearing and saving it. That is not a style choice: an
// undecodable breaker loads as {Halted: true, HaltReason: unreadable_breaker_state, corrupt: true}
// and saveRecoveryState then refuses to overwrite it (the bytes are the only evidence of what went
// wrong). A load-clear-save reset would therefore fail on exactly the latch most in need of a
// reset. The same reasoning governs resetAgentState's clear (reset.go).
func runRecoveryReset(cmd *cobra.Command, args []string) error {
	if callerAuthority() != AuthorityOperator {
		return errors.New(recoveryResetRefusal)
	}
	agent := args[0]
	// The name becomes a path component, so it is validated before any path is composed —
	// the same guard saveRecoveryState and appendRecoveryLog apply for the same reason.
	if err := config.ValidateAgentName(agent); err != nil {
		return fmt.Errorf("refusing to reset breaker state for an invalid agent name: %w", err)
	}

	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	// Read before removing so the printed action can name what was actually cleared. This read
	// cannot fail: an absent breaker is the zero value and an unreadable one is the corrupt latch.
	st := loadRecoveryState(root, agent)
	path := recoveryStatePath(root, agent)
	if err := os.Remove(path); err != nil {
		if !os.IsNotExist(err) {
			return fmt.Errorf("clearing breaker state for %s: %w", agent, err)
		}
		fmt.Fprintf(cmd.OutOrStdout(), "recovery reset %s: no breaker state to clear\n", agent)
		return nil
	}

	if st.Halted {
		fmt.Fprintf(cmd.OutOrStdout(), "recovery reset %s: cleared halted breaker (%s); recovery re-armed\n",
			agent, st.HaltReason)
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "recovery reset %s: cleared breaker state (attempts=%d); recovery re-armed\n",
			agent, st.Attempts)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "recovery reset %s: breaker state only — this does not relaunch the agent (use 'af up %s')\n",
		agent, agent)
	return nil
}

// recoveryLogRotateLines is the K6 size guard. It is a var, not a const, for the reason
// telemetry's rotation caps are (store.go:28-37): a rotation contract that can only be exercised
// by writing ten thousand lines is a contract nobody tests.
var recoveryLogRotateLines = 10000

// recoveryIdentityReadWindow is deliberately far larger than any staleness knob. The reads it
// governs want a datum's IDENTITY (which session wrote it), never its freshness, and
// ObservedReading refuses every stamp when the thresholds are zero (observation.go:224-226) — so
// a small window here would silently return "no observation" and disarm the fence.
const recoveryIdentityReadWindow = 365 * 24 * time.Hour

// occupancyGrowthEpsilon is the smallest occupancy change counted as growth. The channel reports a
// float percentage, so an exactly-equal comparison would treat encoding noise as progress — which
// under C-2 is the one direction that must never be generous.
const occupancyGrowthEpsilon = 0.5

// There is no .runtime path constructor in internal/config, deliberately: that package owns
// .agentfactory/* paths only (config_models.go:298-301 states this). These four mirror
// modelFitnessPath (config_models.go:315-317), the closest structural precedent — factory-root
// .runtime/<subdir>/<key>.json.
//
// Factory root, never the agent worktree (L-1): a worktree-resident halted latch evaporates with
// the worktree, so a breaker that had stopped an agent would silently re-arm on teardown and the
// agent would start recycling again with nobody told.
func recoveryStateDir(root string) string { return filepath.Join(root, ".runtime", "recovery") }

func recoveryStatePath(root, agent string) string {
	return filepath.Join(recoveryStateDir(root), agent+".json")
}

func recoveryLogPath(root string) string {
	return filepath.Join(root, ".runtime", "recovery_log.jsonl")
}

func recoveryUndeliveredPath(root string) string {
	return filepath.Join(root, ".runtime", "recovery_halt_undelivered")
}

// recoveryStamp is the one timestamp spelling this layer writes: RFC3339 in UTC, second
// precision. It matches the snapshot writer's written_at and the reader's parse
// (observation.go:408), so K19's newer-than comparison never crosses a format boundary.
func recoveryStamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }

func parseRecoveryStamp(s string) (time.Time, bool) {
	if s == "" {
		return time.Time{}, false
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

// --- K6: the recovery event log -------------------------------------------------------------

// recoveryLogEntry is one line of .runtime/recovery_log.jsonl. Every durable record in this tree
// carries a version (telemetry.StepEvent.V, the snapshot's schema:2) and observation.go:47-57
// documents what an unversioned on-disk format costs later, so this one does too even though the
// design does not ask for it.
type recoveryLogEntry struct {
	V            int     `json:"v"`
	At           string  `json:"at"`
	Agent        string  `json:"agent"`
	Trigger      string  `json:"trigger"`
	ObservedPct  float64 `json:"observed_pct"`
	ThresholdPct int     `json:"threshold_pct"`
	SessionID    string  `json:"session_id"`
	InstanceID   string  `json:"instance_id"`
	ResumedStep  string  `json:"resumed_step"`
	Attempt      int     `json:"attempt"`
	Outcome      string  `json:"outcome"`
}

const recoveryLogVersion = 1

// recycleDetail carries the occupancy-specific K6 fields the funnel cannot derive for itself. Its
// zero value is correct for every class except the three occupancy ones, which is why the funnel
// can log a crash or a self-handoff without knowing anything about occupancy.
type recycleDetail struct {
	ObservedPct  float64
	ThresholdPct int
	SessionID    string
	InstanceID   string
	ResumedStep  string
	Attempt      int
}

// appendRecoveryLog writes one event to the factory-root log and rotates if it has outgrown its
// cap. It returns its error so the (single) caller can decide; that caller swallows it, because a
// recycle must never fail on a log write.
//
// The file handling follows the tree's existing append idiom (authority.go:274,
// telemetry/store.go:130) rather than inventing one — the IMPLREADME's Gotcha 6 claims no in-tree
// precedent exists, but four append writers do, one of them a complete JSONL store.
func appendRecoveryLog(root string, entry recoveryLogEntry) error {
	// The path does not embed the agent name, but the LINE does, and the same name reaches
	// recoveryStatePath, which does. Validating here keeps one rule for both and matches the
	// telemetry store's guard (store.go:119-121): an agent name carrying path separators must
	// never be able to steer a write.
	if err := config.ValidateAgentName(entry.Agent); err != nil {
		return fmt.Errorf("refusing to log a recycle for an invalid agent name: %w", err)
	}
	path := recoveryLogPath(root)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("creating recovery log dir: %w", err)
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encoding recovery event: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("opening recovery log: %w", err)
	}
	// One Fprintf, newline included: three separate af processes (the watchdog daemon, an
	// agent-invoked af handoff, a hook-invoked af compact-handoff) append to this file, and a
	// single write under O_APPEND is atomic with respect to the offset. Building the line with
	// several writes would let two processes interleave halves of two records.
	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		f.Close()
		return fmt.Errorf("appending recovery event: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing recovery log: %w", err)
	}
	return rotateRecoveryLog(path)
}

// rotateRecoveryLog keeps one generation. It counts LINES rather than bytes because that is the
// cap the plan states; the read is affordable because recycles are rare events, not a hot path,
// and a byte approximation of a line cap would be a third number nobody could reason about.
//
// The count-then-rename is a check-then-act race across processes. It is left unlocked
// deliberately: the loss it can cause is at most one forensic line at a rotation boundary, while a
// lock file would add a failure mode to the path that recycles a wedged agent.
func rotateRecoveryLog(path string) error {
	lines, err := countRecoveryLogLines(path)
	if err != nil || lines <= recoveryLogRotateLines {
		return err
	}
	return os.Rename(path, path+".1")
}

func countRecoveryLogLines(path string) (int, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, fmt.Errorf("sizing recovery log: %w", err)
	}
	defer f.Close()

	count := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		count++
	}
	// A line too long for the scanner is a corrupt record, not a reason to refuse to rotate:
	// reporting zero here would let a poisoned log grow without bound.
	if err := sc.Err(); err != nil {
		return count, nil
	}
	return count, nil
}

// --- K5: the durable breaker ----------------------------------------------------------------

// recoveryState is the per-agent durable latch at the FACTORY root. The seven fields the design
// names (design-doc.md:75) come first; the rest are the state the fence, the rate cap, the
// advisory and the two progress detectors need to survive a watchdog restart, which is the whole
// point of the file existing.
//
// escalation_sent and recipient_session_live are SEPARATE booleans, not one "delivered" flag
// (H-3): `supervisor` is a roster member whose 2,443 lifetime messages were all purged unread, so
// a send into a live-session-less mailbox is not delivery and must not be recorded as such.
type recoveryState struct {
	V                    int    `json:"v"`
	Attempts             int    `json:"attempts"`
	WindowStart          string `json:"window_start"`
	LastRecoveryAt       string `json:"last_recovery_at"`
	Halted               bool   `json:"halted"`
	HaltReason           string `json:"halt_reason"`
	EscalationSent       bool   `json:"escalation_sent"`
	RecipientSessionLive bool   `json:"recipient_session_live"`

	// K19 fence: which session was recycled, and when. The id is the SANITIZED spelling — see
	// latestObservedSessionID.
	LastRecoverySessionID string `json:"last_recovery_session_id"`
	LastTrigger           string `json:"last_trigger"`

	// The absolute cause-tagged rate cap (C-2). Separate from Attempts because it counts only
	// occupancy-class recycles and no progress signal may ever rewind it.
	RateCapCount       int    `json:"rate_cap_count"`
	RateCapWindowStart string `json:"rate_cap_window_start"`

	// K14: the session the advisory was already sent for. Anchored to the session id and not to
	// a counter so a recycled agent becomes eligible again (L-2).
	AdvisorySessionID string `json:"advisory_session_id"`

	// K18(a): what a post-recovery confirmation is waiting on.
	PendingStepID     string `json:"pending_step_id"`
	PendingStepMarker string `json:"pending_step_marker"`
	PendingDeadline   string `json:"pending_deadline"`

	// K18(b): the backstop's view of when this agent last actually moved.
	LastProgressAt     string  `json:"last_progress_at"`
	LastProgressMarker string  `json:"last_progress_marker"`
	LastProgressPct    float64 `json:"last_progress_pct"`

	// K23: the no-step escalation is raised once, not once per tick.
	NoStepEscalatedAt string `json:"no_step_escalated_at"`

	// ChannelQuietSince is when this agent's channel was first seen unhealthy in the current
	// episode, cleared the moment it reads fresh again. It exists because Age() is only available
	// when a datum exists: an agent whose snapshots were deleted, or which never rendered at all,
	// reads none/malformed with NO age — and that is precisely the suppressed-channel case AC-5
	// cares most about. Without an independent clock those agents would wait forever for a
	// measurement that can never arrive.
	ChannelQuietSince string `json:"channel_quiet_since"`
	// DarkEscalatedAt latches the K4 dark-channel escalation to once per episode, not once per
	// tick. Cleared with ChannelQuietSince when the channel recovers, so a genuinely new outage is
	// reported again.
	DarkEscalatedAt string `json:"dark_escalated_at"`

	// corrupt is not persisted. It marks a state that could not be decoded, so the executor can
	// refuse without overwriting the evidence.
	corrupt bool
}

const recoveryStateVersion = 1

// loadRecoveryState reads an agent's breaker. An ABSENT file is the zero value with no error —
// "no recovery has ever happened" is the normal state, the same posture as loadDispatchState.
//
// An UNREADABLE file is the deliberate inversion of the two templates this is modelled on
// (hasFitnessAttestation and loadDispatchState both fail OPEN, because absent-means-permissive is
// right for them). For a breaker the permissive direction is the dangerous one: a corrupt latch
// that read "not halted" would resume recycling an agent an operator had already been told to
// investigate. So a state that cannot be decoded reads HALTED.
func loadRecoveryState(root, agent string) recoveryState {
	data, err := os.ReadFile(recoveryStatePath(root, agent))
	if err != nil {
		if os.IsNotExist(err) {
			return recoveryState{V: recoveryStateVersion}
		}
		return recoveryState{V: recoveryStateVersion, Halted: true, HaltReason: haltReasonCorrupt, corrupt: true}
	}
	var st recoveryState
	if err := json.Unmarshal(data, &st); err != nil {
		return recoveryState{V: recoveryStateVersion, Halted: true, HaltReason: haltReasonCorrupt, corrupt: true}
	}
	return st
}

// saveRecoveryState writes the breaker atomically. INV-6: the latch must be on disk before
// anything acts on or surfaces it, and fsutil.WriteFileAtomic places its temp file in the target's
// own directory, so the rename never crosses a filesystem even through the .runtime symlink.
func saveRecoveryState(root, agent string, st recoveryState) error {
	if err := config.ValidateAgentName(agent); err != nil {
		return fmt.Errorf("refusing to write breaker state for an invalid agent name: %w", err)
	}
	if st.corrupt {
		// Never overwrite a state we could not read: the bytes are the only evidence of what
		// went wrong, and replacing them with a fresh latch would launder the corruption.
		return fmt.Errorf("refusing to overwrite unreadable breaker state for %s", agent)
	}
	st.V = recoveryStateVersion
	dir := recoveryStateDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating recovery state dir: %w", err)
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling breaker state: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(recoveryStatePath(root, agent), data, 0o644)
}

// noteRecoveryAttempt records one attempt against the sliding window and the absolute rate cap,
// and reports the halt cause if either latched.
//
// The two counters are deliberately different animals. Attempts is per-window and CAN be rewound,
// but only by K18's step-anchored confirmation. RateCapCount counts occupancy-class recycles only
// and is rewound by nothing at all — it is the class-independent floor, sized against
// threshold-refill time: refilling a context from empty to 85% takes hours, so a 30-minute attempt
// window never sees two attempts on its own and would never latch under a slow re-stall loop.
func noteRecoveryAttempt(st *recoveryState, trigger string, cfg config.RecoveryConfig, now time.Time) string {
	maxAttempts := cfg.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}
	window := time.Duration(cfg.AttemptWindowSecs) * time.Second
	start, ok := parseRecoveryStamp(st.WindowStart)
	if !ok || (window > 0 && now.Sub(start) > window) {
		st.WindowStart = recoveryStamp(now)
		st.Attempts = 0
	}
	st.Attempts++

	if isRateCappedTrigger(trigger) {
		capWindow := time.Duration(cfg.RateCapWindowSecs) * time.Second
		capStart, capOK := parseRecoveryStamp(st.RateCapWindowStart)
		if !capOK || (capWindow > 0 && now.Sub(capStart) > capWindow) {
			st.RateCapWindowStart = recoveryStamp(now)
			st.RateCapCount = 0
		}
		st.RateCapCount++
		if cfg.RateCapMax >= 1 && st.RateCapCount > cfg.RateCapMax {
			return haltReasonRateCap
		}
	}

	if st.Attempts > maxAttempts {
		return haltReasonMaxAttempts
	}
	return ""
}

// isRateCappedTrigger reports whether a recycle counts against the absolute cap. Only the classes
// this layer itself initiates do: a crash or an operator-invoked handoff is not evidence that
// automatic recovery is looping, and counting them would halt an agent for someone else's reason.
//
// triggerStepBoundaryHandoff (#622 C5) was checked against that rule and deliberately left OUT.
// It is agent-initiated cooperative hygiene — a step closing above its context bound and cycling
// itself clean — and it fires at most once per step by construction. Counting it would let a long
// formula of context-heavy steps exhaust the cap through correct behaviour and reach RECOVERY
// HALTED, an operator action, for doing exactly what the boundary exists to make it do.
func isRateCappedTrigger(trigger string) bool {
	switch trigger {
	case triggerContextExhaustion, triggerDarkAtHighOccupancy, triggerProgressBackstop:
		return true
	}
	return false
}

// --- K19: the recycle fence -----------------------------------------------------------------

// armRecycleFence records when this agent was recycled and which session died, so the evaluator
// can refuse to re-fire on the dead session's own final high snapshot (Gap 2).
//
// It is called from the funnel for EVERY class, not just occupancy: without that, an `af handoff`
// or a PreCompact recycle would leave the dead session's last high reading as the newest
// observation, and the next poll would recycle the freshly-started session on the strength of its
// predecessor's occupancy.
func armRecycleFence(root, agent, trigger, sessionID string, now time.Time) error {
	st := loadRecoveryState(root, agent)
	if st.corrupt {
		return fmt.Errorf("cannot arm the recycle fence for %s: breaker state is unreadable", agent)
	}
	st.LastRecoveryAt = recoveryStamp(now)
	st.LastRecoverySessionID = sessionID
	st.LastTrigger = trigger
	// A recycle starts a new session, so the advisory budget and the progress baseline both reset
	// (L-2). Leaving the advisory latch set would silence the new session's advisory entirely.
	st.AdvisorySessionID = ""
	st.LastProgressAt = recoveryStamp(now)
	st.LastProgressMarker = ""
	st.LastProgressPct = 0
	return saveRecoveryState(root, agent, st)
}

// recycleFenceBlocks reports whether the fence forbids acting on this reading.
//
// Re-firing requires an observation that is BOTH newer than the last recycle AND from a different
// session. Either half alone is insufficient: a newer stamp from the same session is the dying
// session still rendering into the SessionStart window, and a different session id with an older
// stamp is a leftover file from a previous life.
func recycleFenceBlocks(st recoveryState, r statusline.ChannelReading) bool {
	last, ok := parseRecoveryStamp(st.LastRecoveryAt)
	if !ok {
		return false // never recycled — there is nothing to fence against
	}
	obs, hasObs := r.Observation()
	if !hasObs {
		// No datum at all cannot be demonstrated to be newer than the recycle, and the fence's
		// job is to require that demonstration.
		return true
	}
	if !obs.WrittenAt().After(last) {
		return true
	}
	return obs.SessionID() == st.LastRecoverySessionID
}

// latestObservedSessionID returns the sanitized session id of the newest snapshot attributable to
// the agent, or "" when none exists.
//
// The fence compares what it stores against Observation.SessionID(), which is the sanitized
// FILENAME stem rather than the file's own session_id field (observation.go:419-420). The agent's
// <agentDir>/.runtime/session_id holds the RAW id and statusline's sanitizer is unexported — so
// reading the raw id here, or restating the grammar, would put two spellings of one identifier on
// the two sides of the comparison. The fence would then never match and every post-recovery
// snapshot would look like a new session: the exact issue-#563 drift class, with a silent failure
// mode. Taking the id from the observation makes both sides the same bytes by construction.
func latestObservedSessionID(root, agent string, now time.Time) string {
	if config.ValidateAgentName(agent) != nil {
		return ""
	}
	readings, _ := statusline.ReadObservations(
		config.StatuslineSessionsDir(root),
		statusline.ReadOptions{
			KnownAgents: map[string]struct{}{agent: {}},
			Staleness:   recoveryIdentityReadWindow,
			DarkAfter:   recoveryIdentityReadWindow,
		}, now)
	if obs, ok := readings[agent].Observation(); ok {
		return obs.SessionID()
	}
	return ""
}

// --- the funnel's two entry points ----------------------------------------------------------

// provisionRecycleSettings is K20: an idempotent re-provision of the settings the relaunched
// session will read, run before the pane is replaced.
//
// EnsureSettings is normally delivered by worktree.SetupAgent on `af up` and both `af sling`
// launch paths; a pure respawn reaches none of them, so without this a session could relaunch
// indefinitely without the statusLine registration the whole occupancy channel depends on — the
// observer silently missing for the one agent that most needs observing.
//
// Best-effort: a provisioning failure must not stop a wedged agent from being recycled.
func provisionRecycleSettings(opts RespawnOptions) {
	if opts.FactoryRoot == "" {
		return
	}
	dir := respawnAgentDir(opts)
	if dir == "" {
		return
	}
	// RoleTypeFor needs an agents.json roster the funnel does not hold; this mirrors its own rule
	// (settings.go:29-32) from the entry the caller already passed.
	roleType := claude.Interactive
	if opts.AgentEntry.Type == "autonomous" {
		roleType = claude.Autonomous
	}
	if err := claude.EnsureSettings(dir, roleType); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: settings re-provision failed: %v\n", opts.AgentName, err)
	}
}

// recordRecycle is K6 + K19: the funnel's single call into this layer after the pane is replaced.
//
// It reads the clock itself rather than taking one, which is the one place this file departs from
// the repository's trailing-`now` idiom. helpers.go is pinned by line number by
// teardown_scanner_enforce_test.go (:76, :104, :108 — all above the funnel), so giving it the
// `time` import would shift those three lines and fail a conformance test that has nothing to do
// with recovery. recordRecycleAt is the seam tests drive with a fixed clock.
func recordRecycle(opts RespawnOptions, respawnErr error) {
	recordRecycleAt(opts, respawnErr, time.Now())
}

func recordRecycleAt(opts RespawnOptions, respawnErr error, now time.Time) {
	// A root is required: without one, filepath.Join would compose a RELATIVE .runtime path and
	// scatter recovery state into whatever directory the process happens to be in.
	if opts.FactoryRoot == "" || opts.AgentName == "" {
		return
	}

	trigger := opts.Trigger
	switch trigger {
	case triggerContextExhaustion, triggerDarkAtHighOccupancy, triggerProgressBackstop,
		triggerCrash, triggerErrorPattern, triggerCompactHandoff, triggerSelfHandoff,
		triggerStepBoundaryHandoff:
	default:
		trigger = triggerUnknown
	}

	outcome := outcomeRespawned
	if respawnErr != nil {
		outcome = outcomeRespawnFailed
	}

	sessionID := opts.TriggerDetail.SessionID
	if sessionID == "" {
		sessionID = latestObservedSessionID(opts.FactoryRoot, opts.AgentName, now)
	}

	entry := recoveryLogEntry{
		V:            recoveryLogVersion,
		At:           recoveryStamp(now),
		Agent:        opts.AgentName,
		Trigger:      trigger,
		ObservedPct:  opts.TriggerDetail.ObservedPct,
		ThresholdPct: opts.TriggerDetail.ThresholdPct,
		SessionID:    sessionID,
		InstanceID:   opts.TriggerDetail.InstanceID,
		ResumedStep:  opts.TriggerDetail.ResumedStep,
		Attempt:      opts.TriggerDetail.Attempt,
		Outcome:      outcome,
	}
	// Loud but never fatal, the writeTeardownRefusedArtifact posture (authority.go:249-256) with
	// containment.go:402's mandatory stderr: an unlogged recycle is bad, a recycle that ABORTED
	// because a log line could not be written is worse.
	if err := appendRecoveryLog(opts.FactoryRoot, entry); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: recovery log write failed: %v\n", opts.AgentName, err)
	}
	// A failed respawn replaced no pane, so there is no new session to protect: arming the fence on
	// the still-alive wedged session would make recycleFenceBlocks suppress every later re-fire, the
	// breaker would never reach max_attempts, and AC-7's halt+escalate would never engage. Arm only
	// on success; the K6 log write above stays unconditional (AC-6 logs every recycle class).
	if respawnErr == nil {
		if err := armRecycleFence(opts.FactoryRoot, opts.AgentName, trigger, sessionID, now); err != nil {
			fmt.Fprintf(os.Stderr, "recovery: %s: recycle fence arm failed: %v\n", opts.AgentName, err)
		}
	}
}

// --- K4: the evaluator ----------------------------------------------------------------------

// agentRecoveryTrack is the per-agent IN-MEMORY tick state: how many consecutive ticks have read
// high, and what the last reading was. It is deliberately not the durable breaker (design-doc.md:206
// keeps the two breakers' signals, state, caps and reset disjoint) — confirm-tick accounting is
// about the last few minutes and has no meaning across a watchdog restart.
type agentRecoveryTrack struct {
	consecutiveHigh int
	lastPct         float64
	hasLastPct      bool
	lastSessionID   string
}

// exhaustionVerdict is one agent's evaluation at one tick. fire and escalate are distinct
// outcomes, not two levels of one: AC-4 turns on never auto-recycling an agent whose occupancy is
// low, however dark its channel has gone.
type exhaustionVerdict struct {
	fire        bool
	trigger     string
	escalate    bool
	advise      bool
	observedPct float64
	hasPct      bool
	reason      string
}

// recoveryTracks holds the confirm-tick state between ticks. It is a package var because
// watchdogTick's signature is frozen — not by the DO-NOT-CHANGE comment above it, as this
// comment previously said, but by watchdog_test.go, which calls the tick with a literal six
// arguments at three sites and is required to stay byte-identical to the commit that pins the
// C-6 silence tests. So there is nowhere else for per-tick state to live; recoveredAgentUntil
// (watchdog.go) is a package var for the same reason. Tests reset it; pollAgents is a plain
// sequential loop and the only goroutine in this package never touches the recycle path, so no
// mutex is warranted.
var recoveryTracks = map[string]*agentRecoveryTrack{}

// resetRecoveryTracks clears the confirm-tick state. Production never calls it — the watchdog wants
// exactly the continuity this map provides — but a test binary runs every case in one process, so
// without it a "must not fire" case inherits the consecutiveHigh count an earlier "must fire" case
// left behind and fires on its first tick, passing or failing for a reason that has nothing to do
// with what it asserts.
func resetRecoveryTracks() { recoveryTracks = map[string]*agentRecoveryTrack{} }

func recoveryTrackFor(agent string) *agentRecoveryTrack {
	t, ok := recoveryTracks[agent]
	if !ok {
		t = &agentRecoveryTrack{}
		recoveryTracks[agent] = t
	}
	return t
}

// evaluateExhaustion decides what one reading means. It is pure: every I/O-dependent input arrives
// as a parameter, so the trigger rule can be exercised across the whole reading matrix without a
// factory on disk.
//
// It takes no `now`, unlike the design's proposed signature (design-doc.md:163). The reading was
// already classified against the poll's clock inside the reader and carries its own Age; a second
// clock here would be a second source of truth for one instant, and the two could disagree.
func evaluateExhaustion(r statusline.ChannelReading, track *agentRecoveryTrack, cfg config.RecoveryConfig) exhaustionVerdict {
	pct, hasPct := r.UsedPct()
	v := exhaustionVerdict{observedPct: pct, hasPct: hasPct}

	if !hasPct {
		// none / malformed: no trustworthy datum exists. Absence is not evidence of exhaustion,
		// and recycling on it would turn one statusline outage into a factory-wide recycle storm.
		track.consecutiveHigh = 0
		v.escalate = true
		v.reason = "no occupancy datum"
		return v
	}

	// The confirm_ticks debounce is per-session. A recycle replaces the session, and while a fresh
	// af prime normally starts low, a replacement whose first snapshot is already high must not
	// inherit the dead session's consecutiveHigh and fire on that single reading — the debounce would
	// be bypassed. Reset the counter on a session-id change, wiring lastSessionID (committed below).
	if obs, ok := r.Observation(); ok && obs.SessionID() != track.lastSessionID {
		track.consecutiveHigh = 0
	}

	// A stated 0 never survives config validation, but a hand-built literal never met the
	// validator (fillRecoveryDefaults is unexported), and confirm_ticks 0 would fire on the very
	// first high reading — removing the confirmation the knob exists to provide.
	confirm := cfg.ConfirmTicks
	if confirm < 1 {
		confirm = 1
	}
	threshold := float64(cfg.ContextThresholdPct)
	high := pct >= threshold

	if r.State() == statusline.StateDark {
		track.consecutiveHigh = 0
		// Dark AFTER a high reading is the wedged-at-high shape the incident produced: the channel
		// fell silent with the context nearly full. Dark at LOW occupancy answers a different
		// question — it says the channel died, not that the agent is exhausted — so it escalates
		// for visibility and never recycles.
		if high || (track.hasLastPct && track.lastPct >= threshold) {
			v.fire = true
			v.trigger = triggerDarkAtHighOccupancy
			v.reason = "channel dark after a high reading"
		} else {
			v.escalate = true
			v.reason = "channel dark at low occupancy"
		}
	} else if high {
		track.consecutiveHigh++
		if track.consecutiveHigh >= confirm {
			v.fire = true
			v.trigger = triggerContextExhaustion
			v.reason = "sustained high occupancy"
		}
	} else {
		track.consecutiveHigh = 0
		if pct >= float64(cfg.ContextAdvisoryPct) {
			v.advise = true
		}
	}

	track.lastPct = pct
	track.hasLastPct = true
	if obs, ok := r.Observation(); ok {
		track.lastSessionID = obs.SessionID()
	}
	return v
}

// recoveryConfigUsable reports whether the knobs this layer reads are actually configured.
//
// Fail-closed: an unconfigured threshold would make every reading "high" and recycle the entire
// factory on the first tick. A validated config can never be in this state; a literal built in a
// test, or a future caller that forgets to load startup.json, can.
func recoveryConfigUsable(cfg config.RecoveryConfig) bool {
	return cfg.ContextThresholdPct >= 1 && cfg.ContextThresholdPct <= 99 &&
		cfg.StalenessSecs > 0 && cfg.DarkGraceSecs > 0
}

// --- the per-tick sweep ---------------------------------------------------------------------

// recoveryDecision is what one agent's evaluation produced. pollOccupancy returns these so Phase 3
// can order the tick's other responses around them (a recovered agent must not also be nudged) and
// so the trigger rule is assertable without reading files back.
type recoveryDecision struct {
	agent    string
	verdict  exhaustionVerdict
	executed bool
	halted   bool
	fenced   bool
	err      error
}

// pollOccupancy evaluates every in-scope agent's occupancy channel and acts on the verdicts.
//
// It takes root as a parameter and never resolves one: findroot_drift_test.go confines
// config.FindFactoryRoot to four named seams, and more importantly a function that resolved its own
// root would write recovery state into whatever factory the process happened to be standing in.
//
// watchdogTick calls this through the pollOccupancyFn seam, synchronously and BEFORE pollAgents:
// the pane surface must know which agents this tick already recycled, because respawnSession
// leaves the tmux session alive and pollAgents would otherwise read the respawn window as a crash.
func pollOccupancy(root string, agentsCfg *config.AgentConfig, cfg config.RecoveryConfig, now time.Time) []recoveryDecision {
	if agentsCfg == nil || !cfg.IsEnabled() {
		return nil
	}
	if !recoveryConfigUsable(cfg) {
		fmt.Fprintf(os.Stderr, "recovery: refusing to evaluate occupancy: recovery config is not usable "+
			"(context_threshold_pct=%d, staleness_secs=%d, dark_grace_secs=%d)\n",
			cfg.ContextThresholdPct, cfg.StalenessSecs, cfg.DarkGraceSecs)
		return nil
	}

	excluded := make(map[string]struct{}, len(cfg.Exclude))
	for _, name := range cfg.Exclude {
		excluded[name] = struct{}{}
	}

	tx := newCmdTmux()
	scope := map[string]struct{}{}
	names := make([]string, 0, len(agentsCfg.Agents))
	for name := range agentsCfg.Agents {
		if _, skip := excluded[name]; skip {
			continue
		}
		sessionName := session.SessionName(name)
		live, err := tx.HasSession(sessionName)
		if err != nil || !live {
			// Coverage is agents.json ∩ live sessions (Decision 4). An agent with no session has
			// no context to exhaust, and a probe fault is not evidence that it has.
			continue
		}
		// Excluding foreign-root sessions is the CALLER's job: ForeignRoot is derived from live
		// tmux state, which an ADR-004-clean library may not observe, so the reader has no way to
		// notice they should have been left out (observation.go:263-267).
		if sessionForeignRoot(tx, sessionName, root) {
			continue
		}
		scope[name] = struct{}{}
		names = append(names, name)
	}
	if len(names) == 0 {
		return nil
	}
	sort.Strings(names)

	readings, err := statusline.ReadObservations(
		config.StatuslineSessionsDir(root),
		statusline.ReadOptions{
			KnownAgents: scope,
			Staleness:   time.Duration(cfg.StalenessSecs) * time.Second,
			DarkAfter:   time.Duration(cfg.DarkGraceSecs) * time.Second,
		}, now)
	if err != nil {
		// The reader still returns a fully-populated all-none map on error, so the sweep proceeds
		// on honest "no datum" readings rather than skipping the tick entirely.
		fmt.Fprintf(os.Stderr, "recovery: reading occupancy snapshots: %v\n", err)
	}

	decisions := make([]recoveryDecision, 0, len(names))
	for _, name := range names {
		decisions = append(decisions, evaluateAgent(root, name, agentsCfg.Agents[name], readings[name], cfg, now))
	}
	return decisions
}

// evaluateAgent is one agent's whole per-tick decision, in the order the design requires: the
// durable halt first, then the fence, then the trigger rule, then the backstops.
func evaluateAgent(root, agent string, entry config.AgentEntry, reading statusline.ChannelReading, cfg config.RecoveryConfig, now time.Time) recoveryDecision {
	d := recoveryDecision{agent: agent}
	st := loadRecoveryState(root, agent)
	if st.Halted {
		d.halted = true
		d.verdict.reason = "breaker halted: " + st.HaltReason
		return d
	}

	agentDir := resolveAgentDir(root, agent)
	stepID, hasStep, stepKnown := recoveryOpenStep(agentDir)

	// Track the current quiet episode before anything reads it. A healthy channel ends the episode
	// and re-arms both the dark and no-step escalations, so a genuinely new outage is reported
	// again rather than being permanently silenced by the first one.
	noteChannelHealth(&st, reading, now)

	// K18(a): settle any outstanding post-recovery confirmation BEFORE deciding anything new. A
	// recovery that did not take must count against the breaker even if this tick would otherwise
	// fire again — that accounting is what makes the breaker able to latch at all.
	if confirmPostRecovery(root, agent, agentDir, &st, stepID, hasStep, stepKnown, cfg, now) {
		d.halted = st.Halted
		if st.Halted {
			d.verdict.reason = "breaker halted: " + st.HaltReason
			return d
		}
	}

	d.verdict = evaluateExhaustion(reading, recoveryTrackFor(agent), cfg)

	// K14: the soft-threshold advisory, at most once per session, sent while the session is still
	// capable of acting on it. It is NOT counted as activity anywhere — nothing in this layer
	// treats a mail send as evidence that an agent is alive (L-2).
	if d.verdict.advise {
		sendContextAdvisory(root, agent, reading, d.verdict.observedPct, &st)
	}

	// K18(b): the progress backstop, for the under-reporting case where occupancy never crosses
	// the threshold at all. Only meaningful for an agent that holds an open step — the class with
	// no step is K23's.
	if !d.verdict.fire && hasStep {
		if backstopFires(&st, reading, agentDir, cfg, now) {
			d.verdict.fire = true
			d.verdict.trigger = triggerProgressBackstop
			d.verdict.reason = "no step progress and no occupancy growth"
		}
	}

	// K4's dark-channel escalation. evaluateExhaustion distinguishes "dark at high occupancy"
	// (which fires) from "dark at low occupancy" and "no datum at all" (which do not) — the latter
	// two are visibility events, and without this they would be computed and then discarded, so a
	// suppressed channel would be silently invisible rather than merely un-recycled (AC-5).
	if d.verdict.escalate && !d.verdict.fire {
		escalateDarkChannel(root, agent, reading, &st, cfg, now)
	}

	// K23: a live session with NO open step whose channel has gone quiet has no backstop of its
	// own — the "open ready step" precondition never starts — so under payload under-reporting
	// nothing else would ever fire for it. It escalates, and never auto-recycles at low occupancy.
	// stepKnown gates it: an unreachable store must not be mistaken for a consultant with no epic.
	if stepKnown && !hasStep && !d.verdict.fire {
		escalateNoStep(root, agent, reading, &st, cfg, now)
	}

	if !d.verdict.fire {
		if err := saveRecoveryState(root, agent, st); err != nil {
			d.err = err
		}
		return d
	}

	// The fence is consulted before anything acts: the dead session's own last high snapshot must
	// not recycle the session that replaced it.
	if recycleFenceBlocks(st, reading) {
		d.fenced = true
		d.verdict.fire = false
		d.verdict.reason = "recycle fence: no newer observation from a different session"
		if err := saveRecoveryState(root, agent, st); err != nil {
			d.err = err
		}
		return d
	}

	// Interactive agents inherit the existing exemption (Decision 7): a human is present to act,
	// and every other automatic response in the factory already stops here.
	if !shouldAutoRecover(entry.Type) {
		fmt.Fprintf(os.Stderr, "recovery: %s: interactive agent, alert-only (no recycle)\n", agent)
		d.verdict.fire = false
		d.verdict.reason = "interactive agent, alert-only"
		if err := saveRecoveryState(root, agent, st); err != nil {
			d.err = err
		}
		return d
	}

	if err := saveRecoveryState(root, agent, st); err != nil {
		d.err = err
		return d
	}
	d.err = recoverExhausted(root, agent, entry, reading, d.verdict.trigger, cfg, now)
	d.executed = d.err == nil
	after := loadRecoveryState(root, agent)
	d.halted = after.Halted
	return d
}

// --- K18: step-anchored progress ------------------------------------------------------------

// recoveryOpenStep reports the agent's currently-open step.
//
// It is a package var so the breaker and backstop paths can be driven with a stated step without
// standing up a store (ADR-009 §Scope: the production body blocks on the external MCP store
// process, and an in-memory substitute is stable). Named for the operation, not the dependency.
// The third return value separates "this agent holds no step" from "the step state could not be
// determined". Collapsing them — which the obvious two-value signature forces — makes a store
// outage indistinguishable from a closed step, and confirmPostRecovery reads a closed step as a
// CONFIRMED recovery. A degraded store would therefore rewind the breaker on exactly the runs where
// nothing can be verified, which is the C-2 inversion this whole layer exists to prevent.
var recoveryOpenStep = func(agentDir string) (stepID string, hasStep bool, known bool) {
	instanceID := readHookedFormulaID(agentDir)
	if instanceID == "" {
		// No hooked formula is a genuine answer, not a failure: the agent closed its epic and holds
		// no step. This is the K23 class.
		return "", false, true
	}
	store, err := newIssueStore(agentDir, os.Getenv("AF_ACTOR"))
	if err != nil {
		return "", false, false
	}
	result, err := store.Ready(context.Background(), issuestore.Filter{MoleculeID: instanceID})
	if err != nil {
		return "", false, false
	}
	if len(result.Steps) == 0 {
		return "", false, true
	}
	return result.Steps[0].ID, true, true
}

// readStepPrimed returns the raw <agentDir>/.runtime/step_primed marker, the file writeStepPrimed
// maintains (prime.go:323-336). Its CONTENT is what matters here, not its meaning: any change to
// it means prime ran for a step it had not primed before, which is a step-anchored advance.
func readStepPrimed(agentDir string) string {
	data, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "step_primed"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// confirmPostRecovery settles an outstanding post-recovery confirmation, returning whether it
// settled one.
//
// This is where C-2 lives. A step-holding agent confirms its recovery ONLY by closing the step it
// held or by priming a different one. Occupancy growth is not consulted at all: under the degraded
// backend the design is defending against, every useless turn consumes tokens, so occupancy climbs
// on exactly the runs where nothing is happening. Accepting growth as confirmation would rewind
// the breaker on every cycle of the attested re-stall loop and AC-7 would never engage.
func confirmPostRecovery(root, agent, agentDir string, st *recoveryState, stepID string, hasStep, stepKnown bool, cfg config.RecoveryConfig, now time.Time) bool {
	deadline, ok := parseRecoveryStamp(st.PendingDeadline)
	if !ok || now.Before(deadline) {
		return false
	}

	// A step_primed advance is readable without the store, so it is checked first: it is positive
	// evidence of progress and stays valid even when the step state is unknown.
	marker := readStepPrimed(agentDir)
	stepAdvanced := marker != "" && marker != st.PendingStepMarker

	if !stepAdvanced && !stepKnown {
		// The store could not answer. Leaving the window OPEN is the only safe direction: closing
		// it as confirmed would rewind the breaker on an infrastructure fault, and closing it as
		// failed would charge the agent for one. The absolute rate cap is the floor that still
		// bounds a loop while the store is down — it is rewound by nothing, which is exactly why
		// the design made it class-independent.
		fmt.Fprintf(os.Stderr, "recovery: %s: post-recovery confirmation deferred: step state unavailable\n", agent)
		return false
	}

	stepClosed := !hasStep || (st.PendingStepID != "" && stepID != st.PendingStepID)
	confirmed := stepClosed || stepAdvanced

	st.PendingStepID = ""
	st.PendingStepMarker = ""
	st.PendingDeadline = ""

	if confirmed {
		// The recovery took. Rewind the sliding window — but never the rate cap, which no
		// progress signal may touch.
		st.Attempts = 0
		st.WindowStart = ""
		st.LastProgressAt = recoveryStamp(now)
		st.LastProgressMarker = marker
		if err := saveRecoveryState(root, agent, *st); err != nil {
			fmt.Fprintf(os.Stderr, "recovery: %s: breaker write failed: %v\n", agent, err)
		}
		return true
	}

	// A failed recovery costs exactly ONE attempt, and that attempt was already charged by the
	// executor when it fired. Counting again here would make every failed cycle cost two, halting
	// at max_attempts/2 recycles and printing an attempt total nobody can reconcile with the log.
	// What this branch does is decline to rewind — the count simply stands.
	fmt.Fprintf(os.Stderr, "recovery: %s: recovery did not take (no step-anchored progress within the window); "+
		"breaker attempts stand at %d\n", agent, st.Attempts)
	if err := saveRecoveryState(root, agent, *st); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: breaker write failed: %v\n", agent, err)
	}
	return true
}

// backstopFires reports whether the progress backstop has tripped: an open step that has not moved
// and occupancy that has not grown, for progress_backstop_secs.
//
// Both halves are required. Occupancy growth without step progress is the C-2 case and must not
// count as progress — but it IS evidence the session is still consuming turns, which is a
// different situation from a frozen host, and the backstop is aimed at the frozen one. Its window
// is two hours by default, an order above any plausible step.
func backstopFires(st *recoveryState, r statusline.ChannelReading, agentDir string, cfg config.RecoveryConfig, now time.Time) bool {
	window := time.Duration(cfg.ProgressBackstopSecs) * time.Second
	if window <= 0 {
		return false
	}
	marker := readStepPrimed(agentDir)
	pct, hasPct := r.UsedPct()

	moved := marker != st.LastProgressMarker || (hasPct && pct > st.LastProgressPct+occupancyGrowthEpsilon)
	last, ok := parseRecoveryStamp(st.LastProgressAt)
	if moved || !ok {
		st.LastProgressAt = recoveryStamp(now)
		st.LastProgressMarker = marker
		if hasPct {
			st.LastProgressPct = pct
		}
		return false
	}
	return now.Sub(last) >= window
}

// noteChannelHealth opens and closes the current quiet episode. A fresh reading ends one and
// re-arms both escalations; anything else starts one if none is open.
//
// The episode clock is what makes the none/malformed cases actionable. Age() exists only when a
// datum does, so an agent whose snapshots were deleted — the deliberate-suppression case — has no
// age to compare a window against, and every age-gated escalation would skip it forever. That is
// the precise shape of "a dark channel must not read as a healthy agent" (AC-5).
func noteChannelHealth(st *recoveryState, r statusline.ChannelReading, now time.Time) {
	if r.IsHealthy() {
		st.ChannelQuietSince = ""
		st.DarkEscalatedAt = ""
		st.NoStepEscalatedAt = ""
		return
	}
	if st.ChannelQuietSince == "" {
		st.ChannelQuietSince = recoveryStamp(now)
	}
}

// channelQuietFor reports how long this channel has been unhealthy, preferring the datum's own age
// and falling back to the episode clock when there is no datum to age.
func channelQuietFor(st recoveryState, r statusline.ChannelReading, now time.Time) (time.Duration, bool) {
	if age, ok := r.Age(); ok {
		return age, true
	}
	since, ok := parseRecoveryStamp(st.ChannelQuietSince)
	if !ok {
		return 0, false
	}
	quiet := now.Sub(since)
	if quiet < 0 {
		quiet = 0
	}
	return quiet, true
}

// escalateDarkChannel raises the K4 visibility escalation for a channel that has gone quiet without
// meeting the recycle bar — dark at LOW occupancy, or carrying no datum at all.
//
// It never recycles: AC-4 forbids acting on an agent whose occupancy is low however dark its
// channel, because "the channel died" and "the agent is exhausted" are different claims. The
// operator is told; nothing is killed. Raised once per episode, after the same dark grace the
// reader used to classify it.
func escalateDarkChannel(root, agent string, r statusline.ChannelReading, st *recoveryState, cfg config.RecoveryConfig, now time.Time) {
	if st.DarkEscalatedAt != "" {
		return
	}
	quiet, ok := channelQuietFor(*st, r, now)
	if !ok {
		return
	}
	grace := time.Duration(cfg.DarkGraceSecs) * time.Second
	if grace <= 0 || quiet < grace {
		return
	}
	st.DarkEscalatedAt = recoveryStamp(now)
	subject := fmt.Sprintf("RECOVERY: occupancy channel dark for %s (no snapshot for %ds); not auto-recycled (low last reading)",
		agent, int(quiet.Seconds()))
	body := fmt.Sprintf("Agent %s's occupancy channel has read %s for %ds. Its last trustworthy reading was below the "+
		"recovery threshold, so the factory has NOT recycled it — a dark channel is not evidence of exhaustion. "+
		"Check whether the session is still rendering its statusline.", agent, r.State(), int(quiet.Seconds()))
	escalateRecovery(root, escalationTarget, subject, body, st)
}

// --- K23: the no-step terminal state ---------------------------------------------------------

// escalateNoStep raises the terminal-state escalation for a live session with no open step whose
// channel has gone quiet.
//
// This class is the post-formula persistent consultant: its `af done` closed the epic and deleted
// the hooked_formula pointer, so the K18 backstop's "open ready step" precondition never starts and
// nothing else in the design would ever fire for it. It escalates for a human, and never recycles
// — its occupancy is low, and AC-4 forbids recycling on low occupancy however dark the channel.
func escalateNoStep(root, agent string, r statusline.ChannelReading, st *recoveryState, cfg config.RecoveryConfig, now time.Time) {
	if cfg.NoStepEscalationSecs < 1 || st.NoStepEscalatedAt != "" {
		return
	}
	if r.IsHealthy() {
		return
	}
	// Age when a datum exists, the episode clock when none does. An agent that has NEVER rendered
	// reads none with no age at all, and gating on the age alone would exempt exactly the class
	// K23 was promoted to cover.
	age, ok := channelQuietFor(*st, r, now)
	if !ok {
		return
	}
	if age < time.Duration(cfg.NoStepEscalationSecs)*time.Second {
		return
	}
	st.NoStepEscalatedAt = recoveryStamp(now)
	subject := fmt.Sprintf("RECOVERY: occupancy channel %s for %s with no open step (no snapshot for %ds); not auto-recycled",
		r.State(), agent, int(age.Seconds()))
	body := fmt.Sprintf("Agent %s has a live session, no open step, and an occupancy channel that has been %s "+
		"for %ds. It cannot be resumed from beads and is not auto-recycled. Investigate the session directly.",
		agent, r.State(), int(age.Seconds()))
	escalateRecovery(root, escalationTarget, subject, body, st)
}

// --- K8: verified escalation ------------------------------------------------------------------

// escalateRecovery sends an operator escalation and records what actually happened to it.
//
// Existence is checked BEFORE the send and the outcome is checked AFTER (the done.go:252-255
// contract), and `escalation_sent` and `recipient_session_live` are recorded SEPARATELY. A send
// into a roster member's mailbox that has no live session is not delivery: `supervisor` is a member
// whose 2,443 lifetime messages were all purged unread. That case takes the undelivered branch —
// breadcrumb and stderr — while still recording that the send itself succeeded.
//
// Halting never depends on any of this. The latch is the operator's protection; delivery is only
// how they find out.
func escalateRecovery(root, recipient, subject, body string, st *recoveryState) {
	reachable, live := recoveryRecipientReachable(root, recipient)
	st.RecipientSessionLive = live

	if !reachable {
		st.EscalationSent = false
		noteUndeliveredEscalation(root, recipient,
			"recipient is absent from agents.json, or absent from both the live-session set and startup.json")
		return
	}
	if err := sendHandoffMail(recipient, subject, body); err != nil {
		st.EscalationSent = false
		noteUndeliveredEscalation(root, recipient, "send failed: "+err.Error())
		return
	}
	st.EscalationSent = true
	if !live {
		noteUndeliveredEscalation(root, recipient, "sent into a mailbox whose agent has no live session")
	}
}

// recoveryRecipientReachable implements the K8 rule: agents.json ∩ (live session ∨ startup.json).
//
// This is stricter than security.md D5-C's "present in agents.json", deliberately, and the design
// supersedes it: bare roster membership is proven-empty as a delivery guarantee. The startup.json
// half keeps a routine supervisor restart from false-failing the check.
func recoveryRecipientReachable(root, recipient string) (reachable, live bool) {
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return false, false
	}
	if _, ok := agentsCfg.Agents[recipient]; !ok {
		return false, false
	}
	if hasSession, hasErr := newCmdTmux().HasSession(session.SessionName(recipient)); hasErr == nil && hasSession {
		live = true
	}
	if live {
		return true, true
	}
	startupCfg, err := config.LoadStartupConfig(root)
	if err != nil {
		return false, false
	}
	for _, name := range startupCfg.Agents {
		if name == recipient {
			return true, false
		}
	}
	return false, false
}

// noteUndeliveredEscalation writes the K8 breadcrumb floor and the mandatory stderr line. It
// appends rather than truncates (the authority.go:252-256 rationale) because repeated
// undeliverable escalations are exactly the pattern an operator needs to see accumulate, and it
// swallows its errors because an escalation that could not be recorded must still not stop a halt.
func noteUndeliveredEscalation(root, recipient, detail string) {
	msg := fmt.Sprintf("RECOVERY HALTED (escalation undelivered to %q): %s", recipient, detail)
	fmt.Fprintf(os.Stderr, "recovery: %s\n", msg)
	if root == "" {
		return
	}
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return
	}
	f, err := os.OpenFile(recoveryUndeliveredPath(root), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "%s %s\n", time.Now().UTC().Format(time.RFC3339), msg)
}

// haltRecovery latches the breaker and raises the halt escalation exactly once.
//
// The latch is set BEFORE the escalation is attempted: AC-7 orders it that way, and the inverse
// order would let a mail failure leave an agent that has re-stalled three times still eligible for
// a fourth recycle. There is no auto-expiry — clearing it is an operator act.
func haltRecovery(root, agent string, st *recoveryState, reason string, cfg config.RecoveryConfig, now time.Time) {
	if st.Halted {
		return
	}
	st.Halted = true
	st.HaltReason = reason
	if err := saveRecoveryState(root, agent, *st); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: breaker halt write failed: %v\n", agent, err)
	}

	window := time.Duration(cfg.AttemptWindowSecs) * time.Second
	subject := fmt.Sprintf("RECOVERY HALTED: %s re-stalled %d× in %s; run 'af recovery reset %s' after investigating",
		agent, st.Attempts, window, agent)
	if reason == haltReasonRateCap {
		subject = fmt.Sprintf("RECOVERY HALTED: %s hit the recycle rate cap (%d in %s); run 'af recovery reset %s' after investigating",
			agent, st.RateCapCount, time.Duration(cfg.RateCapWindowSecs)*time.Second, agent)
	}
	body := fmt.Sprintf("Automatic context-exhaustion recovery for %s has been halted (cause: %s) at %s. "+
		"No further automatic recycles will be attempted for this agent until an operator clears the latch. "+
		"Investigate the session before clearing it.", agent, reason, recoveryStamp(now))
	escalateRecovery(root, escalationTarget, subject, body, st)
	if err := saveRecoveryState(root, agent, *st); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: breaker escalation write failed: %v\n", agent, err)
	}
}

// --- K14: the soft-threshold advisory ----------------------------------------------------------

// The advisory an agent gets at high occupancy is the last instruction it reads before it may be
// recycled, so it must name mechanisms that exist. It previously told agents to "write checkpoint
// notes, and mail summaries" — but checkpoints are written FOR the agent by the recovery path,
// never by it, and mail, the one writable survivor of worktree death, is rent-charged and
// ephemeral: a handoff channel, not a durable store to accrue learnings in. No store an agent could
// DURABLY write to was named in the sentence — the gap the memory vault fills (#515). Hoisted to
// package level so instruction_reality_test.go can read the shipped text as a literal instead of
// reconstructing it from a call site.
const (
	contextAdvisorySubject = "CONTEXT ADVISORY: occupancy %.0f%% — externalize durable state now " +
		"(commit, af memory add, mail)"

	contextAdvisoryBody = "Your session's context window is %.0f%% full. Externalize anything that must " +
		"survive a session recycle while you still have the headroom: commit your work, record durable " +
		"learnings with `af memory add -s \"<subject>\" -m \"<what you learned>\" --type gotcha`, and mail " +
		"summaries of anything held only in this conversation. Your notes are read back to you on the next " +
		"session by `af memory check --inject`; this conversation is not. Continue your current assignment " +
		"afterwards."
)

// sendContextAdvisory delivers one CONTEXT ADVISORY per session, while the session still has the
// headroom to act on it.
//
// It is class-safe: it never instructs `af done`, because a persistent consultant closing its epic
// on a context warning would be a far worse outcome than the exhaustion. It is anchored to the
// session id, not to a per-lifetime or per-tick counter, so a recycled agent's new session becomes
// eligible again and a chatty tick loop cannot spam the mailbox.
func sendContextAdvisory(root, agent string, r statusline.ChannelReading, pct float64, st *recoveryState) {
	obs, ok := r.Observation()
	if !ok {
		return
	}
	sessionID := obs.SessionID()
	if sessionID == "" || st.AdvisorySessionID == sessionID {
		return
	}
	st.AdvisorySessionID = sessionID
	if err := sendHandoffMail(agent,
		fmt.Sprintf(contextAdvisorySubject, pct),
		fmt.Sprintf(contextAdvisoryBody, pct),
	); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: advisory mail failed: %v\n", agent, err)
	}
	_ = root
}

// --- K7: the executor ---------------------------------------------------------------------------

// recoverExhausted recycles one exhausted agent: checkpoint, hygiene, breadcrumb, class-safe mail,
// then the funnel.
//
// It is deliberately THIN. respawnSession re-resolves the full model precedence chain (#480) and
// the telemetry env family (#329) on every respawn; an executor that built its own respawn would
// silently drop both, and the resume contract is beads, so there is no state for this function to
// write beyond its own breadcrumbs.
func recoverExhausted(root, agent string, entry config.AgentEntry, reading statusline.ChannelReading, trigger string, cfg config.RecoveryConfig, now time.Time) error {
	st := loadRecoveryState(root, agent)
	if st.corrupt {
		return fmt.Errorf("refusing to recycle %s: breaker state is unreadable", agent)
	}
	if st.Halted {
		return fmt.Errorf("refusing to recycle %s: breaker halted (%s)", agent, st.HaltReason)
	}

	if reason := noteRecoveryAttempt(&st, trigger, cfg, now); reason != "" {
		haltRecovery(root, agent, &st, reason, cfg, now)
		return fmt.Errorf("recovery halted for %s: %s", agent, reason)
	}

	agentDir := resolveAgentDir(root, agent)
	meta, absWtPath := resolveWorktreeMeta(root, agent)
	repoDir := root
	if absWtPath != "" {
		repoDir = absWtPath
	}

	pct, hasPct := reading.UsedPct()
	// stepKnown is deliberately discarded here: the executor is past the decision point and only
	// needs a step id to name in the mail and the log. An unknown step state simply means there is
	// nothing to arm the K18(a) confirmation against, which the hasStep guard below already covers.
	stepID, hasStep, _ := recoveryOpenStep(agentDir)
	reason := recoveryReason(trigger, pct, hasPct, cfg)

	checkpointBeforeKill(agentDir, reason)
	clearKillResidue(agentDir, repoDir, now)
	if err := writeLastError(agentDir, reason); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: failed to write last_error: %v\n", agent, err)
	}

	// Class-safe mail to the agent itself: this is the recycled session's primary wake-up context,
	// and it instructs nothing a persistent consultant is forbidden to do.
	subject := recoveryMailSubject(trigger, pct, hasPct, cfg, stepID, hasStep)
	body := fmt.Sprintf("Your session was recycled by the factory (%s). Your assignment and open work survive in "+
		"beads; run af prime to pick them up. Anything that lived only in the previous conversation is gone.", reason)
	if err := sendHandoffMail(agent, subject, body); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: recovery mail failed: %v\n", agent, err)
	}

	// K18(a): arm the post-recovery confirmation for a step-holder. Absence of a step-anchored
	// signal inside the window is a FAILED recovery, and the next tick counts it as one.
	if hasStep {
		st.PendingStepID = stepID
		st.PendingStepMarker = readStepPrimed(agentDir)
		st.PendingDeadline = recoveryStamp(now.Add(time.Duration(cfg.PostRecoveryProgressSecs) * time.Second))
	}
	if err := saveRecoveryState(root, agent, st); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: %s: breaker write failed: %v\n", agent, err)
	}

	sessionID := ""
	if obs, ok := reading.Observation(); ok {
		sessionID = obs.SessionID()
	}
	opts := RespawnOptions{
		FactoryRoot:  root,
		AgentName:    agent,
		AgentEntry:   entry,
		PaneID:       session.SessionName(agent),
		AgentWorkDir: agentDir,
		Trigger:      trigger,
		TriggerDetail: recycleDetail{
			ObservedPct:  pct,
			ThresholdPct: cfg.ContextThresholdPct,
			SessionID:    sessionID,
			InstanceID:   readHookedFormulaID(agentDir),
			ResumedStep:  stepID,
			Attempt:      st.Attempts,
		},
	}
	if meta != nil {
		opts.WorktreePath = absWtPath
		opts.WorktreeID = meta.ID
	}
	return doRespawn(opts)
}

// doRespawn is the executor's route into the funnel. RespawnOptions.Tx cannot serve here: the
// executor constructs its own options, so a test driving recoverExhausted has nowhere to inject a
// fake and would drive real tmux — which the ADR-018 guard turns into a panic on any af- pane.
// Production assigns the real funnel, so the C-1 anchoring is unaffected.
var doRespawn = respawnSession

// recoveryReason renders the cause from enumerated constants and clamped numerics only — never
// from snapshot text, which an agent with write access to the snapshot directory could author.
func recoveryReason(trigger string, pct float64, hasPct bool, cfg config.RecoveryConfig) string {
	switch trigger {
	case triggerContextExhaustion:
		return fmt.Sprintf("context exhaustion (%.0f%% >= %d%%)", pct, cfg.ContextThresholdPct)
	case triggerDarkAtHighOccupancy:
		if hasPct {
			return fmt.Sprintf("occupancy channel dark after a high reading (%.0f%%)", pct)
		}
		return "occupancy channel dark after a high reading"
	case triggerProgressBackstop:
		return "progress backstop: no step progress and no occupancy growth"
	}
	return trigger
}

func recoveryMailSubject(trigger string, pct float64, hasPct bool, cfg config.RecoveryConfig, stepID string, hasStep bool) string {
	resuming := "resuming your open work"
	if hasStep {
		resuming = "resuming " + stepID
	}
	if trigger == triggerContextExhaustion && hasPct {
		return fmt.Sprintf("RECOVERY: context exhaustion (%.0f%% >= %d%%) — session recycled, %s",
			pct, cfg.ContextThresholdPct, resuming)
	}
	return fmt.Sprintf("RECOVERY: %s — session recycled, %s", recoveryReason(trigger, pct, hasPct, cfg), resuming)
}

// --- K21: kill-residue hygiene ------------------------------------------------------------------

// clearKillResidue removes the locks a killed session leaves behind, so the relaunched one is not
// blocked by its predecessor's corpse.
//
// Both removals are fenced by the recovery timestamp: residue NEWER than the recovery belongs to
// something still running, and deleting a live process's index.lock would corrupt a concurrent git
// operation rather than unblock anything.
func clearKillResidue(agentDir, repoDir string, now time.Time) {
	if lockPath := gitIndexLockPath(repoDir); lockPath != "" {
		removeStaleResidue(lockPath, now)
	}
	// The identity lock is warn-only on acquisition (prime.go:362-367), so a stale one does not
	// block a relaunch outright — but it makes the new session's identity read report the dead
	// session's, which is worse than a refusal because nothing complains.
	removeStaleResidue(filepath.Join(agentDir, ".runtime", "agent.lock"), now)
}

func removeStaleResidue(path string, now time.Time) {
	info, err := os.Stat(path)
	if err != nil || info.ModTime().After(now) {
		return
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "recovery: failed to clear residue %s: %v\n", path, err)
	}
}

// gitIndexLockPath resolves the index.lock of the repository at repoDir, handling the worktree
// case where .git is a FILE pointing at the real git dir. Agents work in worktrees by default, so
// treating .git as always-a-directory would make this hygiene a no-op exactly where it is needed.
func gitIndexLockPath(repoDir string) string {
	if repoDir == "" {
		return ""
	}
	gitPath := filepath.Join(repoDir, ".git")
	info, err := os.Stat(gitPath)
	if err != nil {
		return ""
	}
	if info.IsDir() {
		return filepath.Join(gitPath, "index.lock")
	}
	data, err := os.ReadFile(gitPath)
	if err != nil {
		return ""
	}
	gitDir := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(data)), "gitdir:"))
	if gitDir == "" {
		return ""
	}
	if !filepath.IsAbs(gitDir) {
		gitDir = filepath.Join(repoDir, gitDir)
	}
	return filepath.Join(gitDir, "index.lock")
}

// clearIdentityLockFor releases an agent's identity lock through the package that owns its naming,
// rather than composing the path a second time. Used by the executor's hygiene path when the lock
// must go regardless of age.
func clearIdentityLockFor(agentDir string) {
	if err := lock.New(agentDir).Release(); err != nil {
		fmt.Fprintf(os.Stderr, "recovery: failed to release identity lock in %s: %v\n", agentDir, err)
	}
}
