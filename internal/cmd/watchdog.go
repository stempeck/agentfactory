package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
	"github.com/stempeck/agentfactory/internal/worktree"
)

var (
	watchdogInterval       int
	watchdogSilenceTimeout int
)

var watchdogCmd = &cobra.Command{
	Use:   "watchdog",
	Short: "Monitor agent sessions for failures and auto-recover",
	Long: `Watchdog is a long-lived polling loop that supervises agent sessions. It runs
two surfaces side by side on every tick, each with its OWN scope (issue #596
Decision 4):

  pane monitoring    captures each in-scope pane and reacts to error patterns,
                     silence timeouts and Claude crashes. Its scope is exactly
                     startup.json.watchdog_agents. An empty or all-unknown list
                     makes this surface INERT — no pane is captured, no silence
                     nudge is sent, and no keystrokes reach any pane, which is
                     what bounds the kill/respawn blast radius issue #408 was
                     opened about.

  occupancy recovery reads each live agent's context-occupancy snapshot and
                     recycles agents whose context is exhausted. Its scope is
                     every agent in agents.json that has a live session under
                     this factory root, minus startup.json recovery.exclude.

Occupancy recovery is configured by the startup.json 'recovery' block, read
fresh on every tick (defaults in parentheses):

  enabled                     master switch (true)
  context_threshold_pct       occupancy at or above which a recycle becomes
                              possible (85)
  context_advisory_pct        soft threshold; mails one CONTEXT ADVISORY per
                              session so work is externalized while the session
                              can still act on the instruction (70)
  confirm_ticks               consecutive high readings required before a
                              context_exhaustion recycle fires (2)
  staleness_secs              age past which a snapshot reads stale (180)
  dark_grace_secs             age past which a channel reads dark; dark AFTER a
                              high reading fires dark_at_high_occupancy (600)
  post_recovery_progress_secs window in which a recycled agent must show
                              step-anchored progress, or the recycle counts as
                              failed and the breaker advances (900)
  progress_backstop_secs      an open step with no progress and no occupancy
                              growth this long fires progress_backstop (7200)
  no_step_escalation_secs     a live session holding no step whose channel stays
                              quiet this long escalates (3600)
  max_attempts                recycles within attempt_window_secs before the
                              breaker latches halted (3)
  attempt_window_secs         the re-stall window (1800)
  rate_cap_max                absolute recycle cap per rate_cap_window_secs,
                              independent of the attempt window (6)
  rate_cap_window_secs        the rate-cap window (86400)
  exclude                     agent names this surface never recycles ([])

Occupancy growth is never read as progress: a degraded backend answers every
turn with a useless token-consuming reply, so only a step-anchored signal
confirms a recovery. A latched breaker is cleared only by an operator running
'af recovery reset <agent>'. See the "Context exhaustion recovery" section of
USING_RECOVERY.md.

The process therefore starts on every configuration: watchdog_agents narrows
what the watchdog DOES, never whether it RUNS. That matters because the failure
this design exists to prevent was an exhausted agent sitting outside the
configured scope with nothing watching it.

On detection it writes .runtime/last_error, mails the supervisor, and respawns
the session — except for signatures marked mail-only, which report a failed
model request rather than a dead session and so escalate WITHOUT respawning.
Each completed tick touches .runtime/watchdog_heartbeat so the
supervisor's own absence is observable between af up runs. Use --interval to set
the polling frequency. A circuit breaker stops respawning after consecutive
failures and escalates to the supervisor for manual intervention.`,
	RunE: runWatchdog,
}

func init() {
	watchdogCmd.Flags().IntVar(&watchdogInterval, "interval", 30, "Polling interval in seconds")
	watchdogCmd.Flags().IntVar(&watchdogSilenceTimeout, "silence-timeout", 300, "Seconds of no output change before triggering recovery")
	rootCmd.AddCommand(watchdogCmd)
}

type watchdogAgentState struct {
	lastHash     string
	silenceCount int
}

var watchdogMaxConsecutiveFailures = 3

// watchdogNudgeFn nudges a silent agent. It is copy-mode-resilient (Issue #412
// Fix B, K-WATCH defense-in-depth) WITHOUT any change here: SendKeys ->
// SendKeysDebounced calls (*Tmux).exitCopyMode, which drops a pane latched in
// copy-mode back to live view before the "continue" reaches it. We intentionally
// do NOT cancel copy-mode in pollAgents directly — exitCopyMode is unexported and
// the watchdogTmux interface exposes no send/cancel method, so a literal cancel
// here would require a new exported tmux API for no added protection (the
// transitive coverage above already closes the C-CRIT-2 autonomy trap).
var watchdogNudgeFn = func(sessionID string) error {
	tx := tmux.NewTmux()
	return tx.SendKeys(sessionID, "your job is to `af prime`, execute, `af done`, are you doing your job?") // Updated verbiage because "continue" could result in agents guessing about what to do next.
}

// watchdogTmux is the subset of *tmux.Tmux that pollAgents needs to inspect a
// session. It exists so the poll loop can be driven with a fake in tests.
type watchdogTmux interface {
	HasSession(name string) (bool, error)
	IsClaudeRunning(session string) bool
	CapturePane(session string, lines int) (string, error)
	CapturePaneJoined(session string, lines int) (string, error)
}

// newWatchdogTmux is the seam tests override to inject a fake tmux client.
var newWatchdogTmux = func() watchdogTmux { return tmux.NewTmux() }

func handleSilenceNudge(sessionID, agentName string, agentStates map[string]*watchdogAgentState, failures map[string]int) {
	_ = watchdogNudgeFn(sessionID)
	if s, ok := agentStates[agentName]; ok {
		s.silenceCount = 0
	}
}

func checkpointBeforeKill(agentDir, reason string) {
	cp, err := checkpoint.Capture(agentDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: checkpoint capture failed: %v\n", err)
		return
	}
	cp.WithNotes(fmt.Sprintf("WATCHDOG: %s", reason))
	if writeErr := checkpoint.Write(agentDir, cp); writeErr != nil {
		fmt.Fprintf(os.Stderr, "watchdog: checkpoint write failed: %v\n", writeErr)
	}
}

// apiRequestMarker anchors the two generic connection-error needles below to a model
// API call. "connection refused" / "connection timed out" are Go's bare transport-error
// text — an agent running go test, curl, or ssh against a closed port prints them for
// reasons unrelated to its model gateway, so matching the bare substring would respawn a
// working agent on a false positive. Requiring the Anthropic messages path to co-occur
// scopes the match to a genuine gateway failure (Claude Code posts every model request to
// <base_url>/v1/messages), the same way the LiteLLM needles are scoped by the litellm.
// error-class prefix.
const apiRequestMarker = "/v1/messages"

// statuslineSentinel marks a rendered statusline line so checkSilence can drop it before
// hashing. It is the single definition shared by the stripper (here) and the emitter (K6),
// a one-way K7→K6 edge that keeps the marker from ever existing in two copies
// (dependencies.md:77).
//
// Two runes, both zero-width and both Unicode class Cf: sanitize drops only unicode.IsControl
// runes, which is false above U+00FF, so the pair survives the render path the same way the
// mid-needle U+200B defuse does (internal/cmd/statusline.go:222-224). It must be a DISTINCT
// two-rune sequence rather than a second lone U+200B: defuseNeedle splices a single U+200B
// after the first byte of an ASCII needle, so a defused needle can never contain this pair and
// the two mechanisms cannot cross-trigger each other. Spelled with escapes because the literal
// runes are invisible: a raw pair would be unreviewable in a diff and indistinguishable from an
// accidental paste.
//
// EMISSION CONSTRAINT for K6 — verified against tmux 3.4, both capture modes: tmux treats a
// wcwidth==0 rune as combining and appends it to the PRECEDING grid cell. At column 0 there is
// no preceding cell and tmux DISCARDS the rune outright. The sentinel must therefore be emitted
// after at least one visible character, never line-leading, or the strip below can never match.
// A Phase-5a survival probe that emits at column 0 would fail for this reason rather than
// because the Claude display hop ate it, and would retire I1.1 on a false negative.
const statuslineSentinel = "\u200b\u2060"

// endpointFailureSignatures enumerates the substrings a down or misbehaving per-agent
// endpoint (issue #508) surfaces into the pane. Matching is by explicit substring; when
// context is non-empty it must ALSO be present, which scopes an otherwise-generic needle
// to model-gateway output. The 5xx entries carry the full HTTP reason phrase — never a
// bare digit or a loose "HTTP" — so a 4xx/200 line cannot trip them (the watchdog
// negatives pin 400/200). The LiteLLM entries use specific error-class prefixes so a
// benign secrets path like "file:.agentfactory/secrets/litellm.key" cannot false-positive.
// The returned cause is human-readable and flows verbatim into the operator escalation
// mail so a gateway outage reads as "endpoint failure" rather than a generic respawn.
//
// mailOnly carries the RESPONSE, which until #598 was a property of the agent's type alone
// (shouldAutoRecover) and so was identical for every needle. The invalid-model refusal breaks
// that symmetry: it reports the death of a SUB-agent, not of the session — the incident session
// kept working and shipped its review — so respawning would destroy in-flight work to announce a
// failure that has already finished happening (six_sigma_gaps.md Gap 11). The flag travels with
// the match instead of being re-derived downstream because deriving a class by matching on prose
// is the drift recoverAgent's contract forbids (#596 C-1), and here it would be concretely wrong:
// the unsupported_api_for_model cause below already ends in the invalid-model entry's own
// "(model not served on this endpoint)" wording.
//
// The invalid-model needle is a FRAGMENT scoped by a context needle rather than the incident's
// full phrase "Invalid model name passed in model=...", because detection reads the un-joined
// CapturePane (physical rows, the sole call site below) and the live capture of this failure
// wrapped mid-phrase — the full phrase straddles the break and would never fire on a real pane,
// giving a test-green, production-inert defense layer (design-doc.md Decision 10).
//
// Order is behavioral: detectErrorPattern returns the FIRST match, so a capture carrying several
// needles resolves to whichever appears earliest HERE — not to the most severe, and not to the
// safest. The invalid-model entry sits below the transport and 5xx needles deliberately: a pane
// showing both a gateway outage and one model refusal is a sick endpoint, which is a respawn, and
// only the LiteLLM class prefixes below are less specific than it. An entry's position is
// therefore a decision, not formatting.
var endpointFailureSignatures = []struct {
	needle, context, cause string
	mailOnly               bool
}{
	{"502 Bad Gateway", "", "endpoint failure: HTTP 502 Bad Gateway", false},
	{"503 Service Unavailable", "", "endpoint failure: HTTP 503 Service Unavailable", false},
	{"504 Gateway Timeout", "", "endpoint failure: HTTP 504 Gateway Timeout", false},
	{"connection refused", apiRequestMarker, "endpoint failure: connection refused (endpoint unreachable)", false},
	{"connection timed out", apiRequestMarker, "endpoint failure: connection timed out (endpoint unreachable)", false},
	{"unsupported_api_for_model", "", "endpoint failure: unsupported_api_for_model (model not served on this endpoint)", false},
	{"Invalid model name", "model=", "endpoint failure: model not served on this endpoint", true},
	{"litellm.InternalServerError", "", "endpoint failure: LiteLLM proxy internal server error", false},
	{"litellm.ServiceUnavailableError", "", "endpoint failure: LiteLLM proxy service unavailable", false},
	{"litellm.APIConnectionError", "", "endpoint failure: LiteLLM proxy connection error", false},
	{"litellm.Timeout", "", "endpoint failure: LiteLLM proxy timeout", false},
}

// detectErrorPattern returns the matched failure's cause AND its posture. The posture is returned
// here rather than looked up again at the recovery site because the cause is operator-facing prose
// the table deliberately lets overlap between entries — matching on it downstream would silently
// re-classify a signature whose wording merely resembles another's.
func detectErrorPattern(output string) (detected bool, cause string, mailOnly bool) {
	if strings.Contains(output, "Invalid signature in thinking block") {
		return true, "Invalid signature in thinking block", false
	}
	for _, sig := range endpointFailureSignatures {
		if strings.Contains(output, sig.needle) && (sig.context == "" || strings.Contains(output, sig.context)) {
			return true, sig.cause, sig.mailOnly
		}
	}
	return false, "", false
}

// watchdogFailureMail formats the operator escalation mail for a detected session
// failure. It is a pure formatter (no send) so the cause threading — including the
// endpoint-failure signatures — is directly unit-testable; recoverAgent feeds its
// output to sendHandoffMail (a no-op under `go test`).
//
// The mail-only body STATES that no respawn happened rather than merely dropping the claim: for
// that posture the mail is the ENTIRE response, so the operator who reads it and assumes the
// factory already acted is the failure mode this branch exists to prevent (the same reason
// escalateDarkChannel spells out what was not done).
func watchdogFailureMail(agentName, pattern string, mailOnly bool) (subject, body string) {
	subject = fmt.Sprintf("WATCHDOG: %s session failure detected: %s", agentName, pattern)
	if mailOnly {
		body = fmt.Sprintf("Watchdog detected failure in agent %s: %s. The session was NOT respawned — "+
			"this class reports a failed model request rather than a dead session, and the agent may still "+
			"be doing useful work. Investigate the endpoint's model coverage, then recycle the agent "+
			"manually if it is stuck.", agentName, pattern)
		return subject, body
	}
	body = fmt.Sprintf("Watchdog detected failure in agent %s: %s. Session will be respawned.", agentName, pattern)
	return subject, body
}

// stripStatuslineLines drops every line carrying the statusline sentinel. It is what makes the
// silence hash invariant to statusline content — wall-time ticks, cross-session daily movement,
// and any element added later — so the watchdog, not the config package's default element list,
// owns the liveness invariant.
//
// Matching is by Contains rather than by prefix: the sentinel cannot be emitted at column 0
// (tmux discards zero-width runes there — see statuslineSentinel), so its position within the
// line is K6's choice, not something this strip should constrain.
func stripStatuslineLines(output string) string {
	if !strings.Contains(output, statuslineSentinel) {
		return output
	}
	lines := strings.Split(output, "\n")
	kept := lines[:0]
	for _, line := range lines {
		if !strings.Contains(line, statuslineSentinel) {
			kept = append(kept, line)
		}
	}
	return strings.Join(kept, "\n")
}

// volatileRenderMasks is the I1.2 fallback: instead of trusting a zero-width sentinel to survive the
// render path, it rewrites the volatile element renders themselves to fixed placeholders before
// hashing. It is ARMED — checkSilence masks AFTER stripping (mask-after-strip), so a statusline whose
// sentinel was eaten by Claude Code's display hop still normalizes to a stable hash.
//
// Armed rather than dormant because the one thing I1.1 (the sentinel strip) depends on cannot be
// verified in an autonomous factory: whether the zero-width sentinel survives Claude Code's own
// statusline display hop. The Phase-5a probe that would answer it is a MANUAL, live-attached procedure
// (ADR-018 — CI cannot observe the live factory), so the hop stays permanently UNVERIFIED here. Rather
// than trust an unverifiable assumption, this belt-and-suspenders closes the watchdog-masking hazard
// whether or not the sentinel survives (PR #601 BODY-1/F-A). A maintainer who runs the live probe and
// confirms the sentinel survives may revert to strip-only (I1.1).
//
// The trade-off: masks match anywhere in the pane, so genuine agent output that happens to look like a
// daily figure is normalized too. Harmless for silence — it can only make the watchdog MORE willing to
// nudge (a question, circuit-broken, interactive-exempt), never less, so it can never hide a hang.
//
// The patterns accept BOTH the shipped render format AND its predecessor, so a future reformat cannot
// silently disarm the mask. Two places that matters:
//
//   - elapsed renders `T <H>h <MM>m` / `T <N>m` (formatDuration, internal/statusline/render.go — the
//     PR #601 T6 screenshot-parity format); the legacy Go-duration forms ("45s", "2m5s", "1h6m6s") are
//     also accepted so a pane captured before the reformat is still masked.
//   - cost renders "$ 12.34" and "D $ 80.64" (a space after "$"), so "$" accepts an optional following
//     space; the doc's spaceless "$21.95" is accepted too.
//
// Token counts also accept "G" so a very long session cannot slip the mask. maskVolatileRenders' guard
// test (TestMaskVolatileRenders_CatchesShippedFormats) asserts every pattern against both format
// families, keeping this set honest as the renderer evolves.
//
// Order matters twice: the daily mask runs before the session mask so the "D $" form is consumed by its
// own pattern rather than half-matched as a bare session figure, and within elapsed the longer
// alternatives come first (Go's regexp is leftmost-FIRST, not leftmost-longest, so `T \d+m` placed
// ahead of `T \d+m\d+s` would truncate the match and leave the seconds behind).
var volatileRenderMasks = []struct {
	name string
	re   *regexp.Regexp
}{
	{"elapsed", regexp.MustCompile(`T \d+h\d+m\d+s|T \d+h \d+m|T \d+m\d+s|T \d+m|T \d+s`)},
	{"daily", regexp.MustCompile(`D ~?\$ ?[\d.]+( · [\d.]+[kMG]? tok)?`)},
	{"session", regexp.MustCompile(`~?\$ ?[\d.]+( · [\d.]+[kMG]? tok)?`)},
	{"bar", regexp.MustCompile(`[█░]+ \d+% \([\d.]+[kMG]?/[\d.]+[kMG]?\)`)},
}

// maskVolatileRenders replaces every volatile element render with a per-element placeholder.
// Armed via checkSilence (mask-after-strip): see volatileRenderMasks.
func maskVolatileRenders(output string) string {
	for _, m := range volatileRenderMasks {
		output = m.re.ReplaceAllString(output, "<"+m.name+">")
	}
	return output
}

func checkSilence(agentName, output string, state map[string]*watchdogAgentState, threshold int) bool {
	// Mask AFTER strip (I1.1 then I1.2): the strip removes sentinel-marked statusline lines; the mask
	// normalizes any volatile render that reached the pane UN-sentineled (the display hop ate the
	// sentinel). Belt-and-suspenders so silence detection is correct whether or not the sentinel
	// survives — the Phase-5a live probe that would confirm it cannot run here (PR #601 BODY-1/F-A).
	h := sha256.Sum256([]byte(strings.TrimSpace(maskVolatileRenders(stripStatuslineLines(output)))))
	hash := hex.EncodeToString(h[:])

	s, ok := state[agentName]
	if !ok {
		s = &watchdogAgentState{}
		state[agentName] = s
	}

	if s.lastHash == "" {
		s.lastHash = hash
		s.silenceCount = 1
		return false
	}

	if hash == s.lastHash {
		s.silenceCount++
		return s.silenceCount >= threshold
	}

	s.lastHash = hash
	s.silenceCount = 0
	return false
}

func shouldRespawn(failures map[string]int, agentName string, maxFailures int) bool {
	return failures[agentName] < maxFailures
}

func resetCircuitBreaker(failures map[string]int, agentName string) {
	failures[agentName] = 0
}

func shouldAutoRecover(agentType string) bool {
	return agentType != "interactive"
}

// checkCircuitBreaker gates the respawn paths only: after the mail-only posture was split out
// (BODY-1/F5) its two remaining callers — the crash branch and the non-mail-only error-pattern
// branch — both attempt a respawn, so its "consecutive recoveries" wording is now accurate: every
// failure it counts is one where a respawn was attempted.
func checkCircuitBreaker(failures map[string]int, name string) bool {
	if shouldRespawn(failures, name, watchdogMaxConsecutiveFailures) {
		return false
	}
	if failures[name] == watchdogMaxConsecutiveFailures {
		_ = sendHandoffMail(escalationTarget,
			fmt.Sprintf("WATCHDOG CIRCUIT BREAKER: %s failed %d consecutive recoveries. Manual intervention required.",
				name, watchdogMaxConsecutiveFailures),
			fmt.Sprintf("Agent %s has failed %d consecutive recovery attempts. The watchdog has stopped auto-recovery. Please investigate manually.",
				name, watchdogMaxConsecutiveFailures))
		failures[name]++
	}
	return true
}

// mailOnlyKey namespaces an agent's mail-only escalation counter inside the shared per-run failures
// map. The mail-only posture never respawns (recoverAgent returns before ClearHistory), so a
// persistent invalid-model pane must not spend the crash-respawn budget keyed under the bare name — or
// a later genuine crash would find the breaker already latched and never recycle (BODY-1/F5). Agent
// names match ^[a-zA-Z][a-zA-Z0-9_-]*$, so the NUL separator can never collide with a bare name; the
// key rides the failures map's own lifecycle (per watchdogTick, fresh per test), so decoupling the two
// budgets needs no new state container and no signature change to the frozen pollAgents/watchdogTick.
func mailOnlyKey(name string) string { return name + "\x00mail" }

// checkMailOnlyEscalation bounds the mail-only posture with its OWN namespaced counter, decoupled from
// the crash-respawn breaker. It returns true once the posture has escalated its bound, after emitting
// ONE final notice; unlike checkCircuitBreaker its wording never claims "consecutive recoveries" — this
// posture attempts none. It reuses watchdogMaxConsecutiveFailures so the cadence matches the crash path
// without an unexplained second constant (decision D6).
func checkMailOnlyEscalation(failures map[string]int, name string) bool {
	key := mailOnlyKey(name)
	if failures[key] < watchdogMaxConsecutiveFailures {
		return false
	}
	if failures[key] == watchdogMaxConsecutiveFailures {
		_ = sendHandoffMail(escalationTarget,
			fmt.Sprintf("WATCHDOG: %s repeated model-request failure — no longer re-escalating", name),
			fmt.Sprintf("Agent %s has reported the same unserved model request %d times. No recovery was attempted — "+
				"this posture leaves the session running — so the watchdog has stopped re-escalating it. Investigate the "+
				"endpoint's model coverage, then recycle the agent manually if it is stuck.",
				name, watchdogMaxConsecutiveFailures))
		failures[key]++
	}
	return true
}

// resetMailOnlyEscalation clears an agent's mail-only counter, called alongside resetCircuitBreaker on
// a clean tick so a recovered-then-refailed mail-only agent does not carry a stale count into a
// premature bound.
func resetMailOnlyEscalation(failures map[string]int, name string) {
	delete(failures, mailOnlyKey(name))
}

func writeLastError(agentDir, description string) error {
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), description)
	return os.WriteFile(filepath.Join(runtimeDir, "last_error"), []byte(content), 0o644)
}

// writeWatchdogLastError writes a timestamped breadcrumb to
// <root>/.runtime/watchdog_last_error — the watchdog process's own (factory-root)
// error record. It is DISTINCT from writeLastError, whose
// <agentDir>/.runtime/last_error is the per-agent recovery record used by
// recoverAgent; a factory-root last_error would be ambiguous with an agent's
// (issue #408 R2-L1). The `af up` pre-check (Phase 3) points at this same file so
// operators have a single place to look.
func writeWatchdogLastError(root, description string) error {
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), description)
	return os.WriteFile(filepath.Join(runtimeDir, "watchdog_last_error"), []byte(content), 0o644)
}

func resolveAgentDir(root, agentName string) string {
	meta, err := worktree.FindByAgent(root, agentName)
	if err == nil && meta != nil {
		return config.AgentDir(worktree.AbsWorktreePath(root, meta), agentName)
	}
	return config.AgentDir(root, agentName)
}

func resolveWorktreeMeta(root, agentName string) (*worktree.Meta, string) {
	meta, err := worktree.FindByAgent(root, agentName)
	if err == nil && meta != nil {
		return meta, worktree.AbsWorktreePath(root, meta)
	}
	return nil, ""
}

// recoverAgent recycles a failed agent. trigger names WHICH failure class this is — the free-text
// pattern is the operator-facing description and cannot serve: both callers produce prose, and the
// K6 log's trigger field is a closed enum. The two are kept separate rather than derived from each
// other because deriving a class by matching on prose is exactly the drift the enum exists to
// prevent (#596 C-1).
//
// mailOnly is the matched signature's posture (#598): true means the escalation mail IS the whole
// response and the session is left running. It is a parameter for the same reason trigger is — the
// caller holds the match, and the free-text pattern cannot serve as a stand-in for it.
func recoverAgent(root, agentName string, entry config.AgentEntry, pattern, trigger string, mailOnly bool) {
	agentDir := resolveAgentDir(root, agentName)
	checkpointBeforeKill(agentDir, pattern)
	if err := writeLastError(agentDir, pattern); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: %s: failed to write last_error: %v\n", agentName, err)
	}

	subject, body := watchdogFailureMail(agentName, pattern, mailOnly)
	_ = sendHandoffMail(escalationTarget, subject, body)

	// The breadcrumbs above still run for a mail-only class: the checkpoint and last_error are what
	// the operator reads when they arrive, and neither touches the live session.
	//
	// Returning here also skips the ClearHistory that respawnSession performs, so the matched text
	// stays in the pane and re-fires each tick until it scrolls out of the 50-row window: the
	// operator gets up to watchdogMaxConsecutiveFailures escalations and then a circuit-breaker
	// mail. That is the same shape the interactive/alert-only branch below already has, and it is
	// preferable to not counting the failure at all — an uncounted class would escalate forever.
	if mailOnly {
		fmt.Fprintf(os.Stderr, "watchdog: %s: %s: escalated by mail, session left running (no respawn)\n",
			agentName, pattern)
		return
	}

	if !shouldAutoRecover(entry.Type) {
		fmt.Fprintf(os.Stderr, "watchdog: %s: interactive agent, alert-only (no respawn)\n", agentName)
		return
	}

	meta, absWtPath := resolveWorktreeMeta(root, agentName)
	opts := RespawnOptions{
		FactoryRoot:  root,
		AgentName:    agentName,
		AgentEntry:   entry,
		PaneID:       session.SessionName(agentName),
		AgentWorkDir: agentDir,
		Trigger:      trigger,
	}
	if meta != nil {
		opts.WorktreePath = absWtPath
		opts.WorktreeID = meta.ID
	}
	if err := respawnSession(opts); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: %s: respawn failed: %v\n", agentName, err)
	} else {
		fmt.Fprintf(os.Stderr, "watchdog: %s: session respawned\n", agentName)
	}
}

// resolveWatchdogRoot determines the factory root for the watchdog (#309 W2).
// It consults AF_ROOT first — exported into the watchdog session by `af up` —
// so the watchdog survives a deleted working directory (e.g. a worktree removed
// out from under it). It falls back to the cwd only when AF_ROOT is unset or no
// longer points at a valid factory. Reading AF_ROOT in internal/cmd is permitted
// by ADR-004 (the env-hermeticity ban exempts internal/cmd).
func resolveWatchdogRoot() (string, error) {
	if afRoot := os.Getenv("AF_ROOT"); afRoot != "" {
		if root, err := config.FindFactoryRoot(afRoot); err == nil {
			return root, nil
		}
	}
	wd, err := getWd()
	if err != nil {
		return "", err
	}
	return config.FindFactoryRoot(wd)
}

func runWatchdog(cmd *cobra.Command, args []string) error {
	root, err := resolveWatchdogRoot()
	if err != nil {
		return err
	}

	// The watchdog is the single authority for its PANE scope: it self-reads
	// startup.json.watchdog_agents (NOT the CLI flags). A non-nil error here now means
	// only that startup.json itself is unreadable or invalid — a broken factory. A
	// narrow or empty scope is no longer an error (#596 Decision 4); see
	// resolveWatchdogScope.
	ws, err := resolveWatchdogScope(root)
	if err != nil {
		if werr := writeWatchdogLastError(root, err.Error()); werr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "watchdog: failed to write breadcrumb: %v\n", werr)
		}
		return err
	}
	if ws.paneMisconfig {
		// Names that do not exist in agents.json are an operator error, not a
		// configuration choice, so this half keeps the loud stderr line AND the durable
		// breadcrumb the old refusal left. The empty-scope case deliberately gets
		// neither: under the revision it is a supported way to run.
		fmt.Fprintf(cmd.ErrOrStderr(), "watchdog: pane monitoring inert — %s\n", ws.paneInertReason)
		_ = writeWatchdogLastError(root, "watchdog: pane monitoring inert — "+ws.paneInertReason)
	}
	if ws.membershipNote != "" {
		// Transient-read guard (N-2): membership could not be validated, so the
		// scope launches unvalidated. Surface it both ways so "monitoring nothing
		// because of a flaky read" stays discoverable.
		fmt.Fprintln(cmd.ErrOrStderr(), ws.membershipNote)
		_ = writeWatchdogLastError(root, ws.membershipNote)
	}
	// Per-name typos stay non-fatal: warn, but keep monitoring the known names.
	for _, name := range ws.unknown {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"watchdog: startup.json watchdog_agents names unknown agent %q — it will NOT be monitored\n", name)
	}
	scope := ws.agents

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	interval := time.Duration(watchdogInterval) * time.Second
	silenceThreshold := watchdogSilenceTimeout / watchdogInterval
	if silenceThreshold < 1 {
		silenceThreshold = 1
	}

	agentStates := make(map[string]*watchdogAgentState)
	failures := make(map[string]int)

	fmt.Fprintf(cmd.OutOrStdout(), "watchdog: started (interval=%ds, silence-timeout=%ds)\n",
		watchdogInterval, watchdogSilenceTimeout)
	// BOTH scopes, always (#596 Decision 4). The two surfaces are scoped
	// independently, so printing only one would let an operator read "watchdog
	// started" and infer coverage the process does not have — which is the reporting
	// half of the incident this design addresses.
	fmt.Fprintf(cmd.OutOrStdout(), "watchdog: pane scope: %s\n", describePaneScope(ws))
	fmt.Fprintf(cmd.OutOrStdout(), "watchdog: recovery scope: %s\n", describeRecoveryScope(ws.recovery))

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(cmd.OutOrStdout(), "watchdog: shutting down\n")
			return nil
		case <-ticker.C:
			watchdogTick(cmd, root, scope, agentStates, failures, silenceThreshold)
		}
	}
}

// telemetryBackendGuardInFlight bounds the periodic telemetry-backend liveness
// check (fable-implement Step 1, R4) to at most one attempt in flight at a time,
// across however many ticks fire while a prior attempt is still running — a 90s
// cold start must not stack three 30s-spaced spawns (decisions.md D4). There is no
// existing sync/atomic idiom elsewhere in this package to copy; this is the first.
var telemetryBackendGuardInFlight atomic.Bool

// watchdogTick is one tick's work: the occupancy sweep, then the existing agent poll,
// plus the telemetry-backend liveness guard — all fired BESIDE each other and never
// folded into pollAgents, which stays agent-scoped (DO-NOT-CHANGE, decisions.md,
// consumers.md). pollAgents' silence surface has exactly one sanctioned change since
// Phase 2: the #600 sentinel-strip of statusline lines from the hash input, pinned by
// the checkSilence tests in watchdog_test.go.
//
// Its 6-parameter signature is FROZEN and must stay that way. watchdog_test.go calls it
// with exactly these arguments, and the C-6 silence tests pin the silence semantics. So
// pollOccupancy's two inputs are loaded here from the root the tick already carries,
// using the same per-tick read idiom pollAgents itself uses, rather than threaded in as
// parameters.
//
// Ordering is load-bearing, not stylistic:
//
//  1. pollOccupancy runs FIRST so this tick's recycle verdicts exist before the pane
//     surface looks at the same agents.
//  2. It runs SYNCHRONOUSLY. The async treatment below is reserved for the telemetry
//     probe, which is async only because it can take ~12s; occupancy reads are
//     milliseconds and must order with this tick's breaker updates.
//  3. The heartbeat is written LAST, so its presence means "a complete tick finished"
//     rather than "a tick started" — a tick wedged in pollAgents must not look healthy.
func watchdogTick(cmd *cobra.Command, root string, scope map[string]struct{}, agentStates map[string]*watchdogAgentState, failures map[string]int, silenceThreshold int) {
	now := time.Now()

	recoveryCfg := watchdogRecoveryConfig(cmd, root)
	noteRecoveredAgents(pollOccupancyFn(root, watchdogAgentsSnapshot(root), recoveryCfg, now), recoveryCfg, now)

	pollAgents(cmd, root, paneScopeExcludingRecovered(scope, now), agentStates, failures, silenceThreshold)
	triggerTelemetryBackendGuard(cmd, root)

	if err := writeWatchdogHeartbeat(root, now); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "watchdog: heartbeat write failed: %v\n", err)
	}
}

// pollOccupancyFn is the seam the tick calls through, mirroring ensureTelemetryBackendFn.
// It exists so a test can assert the tick's WIRING — that the sweep is dispatched, with
// this root, once per tick — without standing up snapshots, sessions and a store just to
// observe a side effect three layers down.
var pollOccupancyFn = pollOccupancy

// watchdogAgentsSnapshot reads agents.json for the occupancy sweep.
//
// This is deliberately a SECOND read of the same file pollAgents reads on the same tick.
// Handing pollAgents a shared copy would mean changing its signature, and the C-6 silence
// tests pin pollAgents' frozen surface — the occupancy sweep must not reshape it. The cost
// is one extra read of a small JSON file per 30s tick; the benefit is that the silence
// surface stays exactly what those tests pin.
//
// A read failure returns nil, which pollOccupancy fail-closes on. It is deliberately
// silent: pollAgents warns about the very same failure on the very same tick, and a
// second line would double every such warning in the log.
func watchdogAgentsSnapshot(root string) *config.AgentConfig {
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return nil
	}
	return agentsCfg
}

// watchdogRecoveryConfig reads the recovery block fresh each tick, rather than snapshotting
// it at startup beside the pane scope. Recovery is the surface that KILLS things, so an
// operator who sets recovery.enabled=false, or adds an agent to recovery.exclude, must be
// able to stop it inside one tick without restarting a long-lived process.
//
// An unreadable or invalid startup.json fail-closes to a disabled block, not to a
// zero-value one: a zero value reads as enabled (Enabled is a tri-state pointer, and nil
// means "absent ⇒ on") and would then be rejected one layer down by recoveryConfigUsable,
// printing its own refusal every tick on top of ours.
func watchdogRecoveryConfig(cmd *cobra.Command, root string) config.RecoveryConfig {
	startupCfg, err := config.LoadStartupConfig(root)
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"watchdog: occupancy recovery disabled this tick — startup.json unreadable: %v\n", err)
		disabled := false
		return config.RecoveryConfig{Enabled: &disabled}
	}
	return startupCfg.Recovery
}

// recoveredAgentUntil holds, per agent, the instant until which the PANE surface must
// leave that agent alone because the occupancy surface is mid-way through recovering it.
//
// It is a package var for the same reason recoveryTracks is: watchdogTick's signature is
// frozen (see above), so per-tick state has nowhere else to live. pollOccupancy and
// pollAgents are both plain sequential loops on the tick goroutine — the one goroutine
// this package spawns never touches this map — so no mutex is warranted.
var recoveredAgentUntil = map[string]time.Time{}

// resetRecoveredAgents clears the suppression state. Production never calls it, but a test
// binary runs every case in one process, so without it a case that recovers an agent leaves
// that agent suppressed for the next case, which would then pass or fail for a reason
// unrelated to what it asserts. Mirrors resetRecoveryTracks.
func resetRecoveredAgents() { recoveredAgentUntil = map[string]time.Time{} }

// noteRecoveredAgents arms the suppression window for agents this tick actually acted on.
//
// The predicate is the whole point. pollOccupancy returns one decision per EVALUATED
// agent — every live in-scope agent, every tick, whatever the outcome — so keying on mere
// presence in the slice would suppress the pane surface for the entire fleet forever and
// silently delete the silence-nudge behaviour the C-6 tests protect. Only a verdict that
// reached for the session counts: evaluateAgent clears fire when the recycle was fenced,
// when the agent is interactive, or when the breaker has halted, so a fire that survives to
// the return means a recycle was attempted. executed is folded in for readability; it
// implies fire.
//
// A halted agent is deliberately NOT suppressed: its breaker has latched, recovery will
// not act on it again, and the silence nudge is then the only remaining signal it gets.
func noteRecoveredAgents(decisions []recoveryDecision, cfg config.RecoveryConfig, now time.Time) {
	for _, d := range decisions {
		if !d.verdict.fire && !d.executed {
			continue
		}
		recoveredAgentUntil[d.agent] = now.Add(recoveryPaneGrace(cfg))
	}
}

// recoveryPaneGrace is how long a recycled agent stays out of the pane scope.
//
// It reuses post_recovery_progress_secs rather than introducing a knob because that IS the
// window being described: it is exactly how long K18 gives a recycled agent to show
// progress before the durable breaker counts the recovery as failed. For that whole window
// recovery owns the agent and is itself judging the outcome, so a second surface acting on
// the same agent would be racing the judge. When the window closes, the agent returns to
// the pane scope automatically — whether recovery confirmed, or the breaker latched.
func recoveryPaneGrace(cfg config.RecoveryConfig) time.Duration {
	secs := cfg.PostRecoveryProgressSecs
	if secs <= 0 {
		// A literal built outside the validator can state 0; falling back to the tick
		// interval keeps the suppression to this tick alone rather than disabling it.
		secs = watchdogInterval
	}
	return time.Duration(secs) * time.Second
}

// paneScopeExcludingRecovered is the K9 nudge-suppression PRE-CHECK: a new path beside the
// silence path, not a change to it. K9 touches none of checkSilence, handleSilenceNudge or
// pollAgents; what changes is the scope VALUE this tick hands pollAgents.
//
// Filtering the scope — rather than just skipping the nudge — is what actually closes the
// hazard. respawnSession recycles an agent with RespawnPane, so the tmux SESSION survives:
// a same-tick pollAgents pass would find HasSession true and IsClaudeRunning false and take
// the crash branch, double-recycling the agent, incrementing its circuit breaker, and
// writing a second recovery-log line mis-tagged as a crash. ClearHistory also changes the
// pane hash, so checkSilence would have returned false anyway — a nudge-only suppression
// would have protected against nothing while leaving the real hazard open.
//
// When nothing is suppressed it returns the caller's own map, unmodified and unallocated,
// so the silence path receives byte-identical inputs to its Phase-2 behaviour. That
// identity is what TestWatchdog_PaneScopeIsUntouchedWithoutRecoveryVerdict asserts.
func paneScopeExcludingRecovered(scope map[string]struct{}, now time.Time) map[string]struct{} {
	suppressed := 0
	for agent, until := range recoveredAgentUntil {
		if !now.Before(until) {
			delete(recoveredAgentUntil, agent)
			continue
		}
		if _, inScope := scope[agent]; inScope {
			suppressed++
		}
	}
	if suppressed == 0 {
		return scope
	}

	filtered := make(map[string]struct{}, len(scope))
	for agent := range scope {
		if _, recovering := recoveredAgentUntil[agent]; recovering {
			continue
		}
		filtered[agent] = struct{}{}
	}
	return filtered
}

// writeWatchdogHeartbeat records that a tick completed, at <root>/.runtime/watchdog_heartbeat
// (K22). It is the observability counterpart to writeWatchdogLastError, whose shape it
// mirrors: same directory, same permissions, best-effort, factory-root-scoped so it is never
// confused with an agent's own .runtime files. Without it a dead watchdog and a healthy one
// are indistinguishable from outside the process, and the supervisor's absence is exactly
// the failure nobody is watching for.
//
// It takes root as a parameter and never resolves one — findroot_drift_test.go confines
// config.FindFactoryRoot to four named seams, and the tick already carries the root
// resolveWatchdogRoot validated at startup.
//
// The stamp is RFC3339Nano, not RFC3339: at second granularity two back-to-back ticks write
// the same bytes, and "the heartbeat advanced" stops being observable.
func writeWatchdogHeartbeat(root string, now time.Time) error {
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	content := now.UTC().Format(time.RFC3339Nano) + "\n"
	return os.WriteFile(filepath.Join(runtimeDir, "watchdog_heartbeat"), []byte(content), 0o644)
}

// triggerTelemetryBackendGuard fires ensureTelemetryBackendFn asynchronously so a
// down backend's worst-case probe+relaunch latency (~12s) never delays this tick's
// own pollAgents call or the next tick's readiness (DO-NOT-CHANGE: no ~12s latency
// added to a down-tick). CompareAndSwap makes "at most one in-flight attempt" a
// property a test can assert deterministically, rather than relying on the 10s
// script budget staying under the 30s tick interval by timing luck alone.
//
// Scope: that guarantee is PROCESS-LOCAL. telemetryBackendGuardInFlight is a
// package-level atomic, so it orders this process's ticks against each other and
// nothing else. On a first af up the two callers are sequential anyway (the
// cold-start guard at up.go:398 completes before launchWatchdog at :423), but af up
// is idempotent and routinely re-run against a factory whose watchdog is already
// ticking — two processes, one of them holding no knowledge of the other's attempt.
// What bounds that case is not this flag: it is relaunch.sh's own
// `tmux has-session -t telemetry || …` check-then-act (quickstart.sh:1105-1107) plus
// tmux's refusal to create a duplicate session name, so the worst outcome is a
// redundant probe and a refused second launch, never two backends.
//
// Note for the next test author: this goroutine writes to cmd.OutOrStdout() and
// cmd.ErrOrStderr() while the main loop writes to the same writers (pollAgents'
// warnings, the shutdown line). In production both are os.Stdout, so that is
// interleaving only — but a test that points cobra at a shared bytes.Buffer AND lets
// the real ensureTelemetryBackend run would have a genuine data race, and this
// package cannot detect it: `make test` pins CGO_ENABLED=0 (Makefile:59), so -race
// has never run over this code. Existing tests override ensureTelemetryBackendFn, so
// nothing reaches the writers today; keep it that way, or give the test its own
// writer.
func triggerTelemetryBackendGuard(cmd *cobra.Command, root string) {
	if !telemetryBackendGuardInFlight.CompareAndSwap(false, true) {
		return // an attempt is already running; this tick skips it, never queues it
	}
	go func() {
		defer telemetryBackendGuardInFlight.Store(false)
		ensureTelemetryBackendFn(context.Background(), cmd, root)
	}()
}

// buildWatchdogScope builds the monitoring scope set from a list of agent names,
// folding an optional single name into the set (blank names are dropped). An empty
// result is a non-nil EMPTY map meaning "no-scope" — monitor NOTHING (issue #408).
// A nil/empty scope is never "all"; pollAgents fail-closes on it.
func buildWatchdogScope(agents []string, single string) map[string]struct{} {
	scope := make(map[string]struct{})
	for _, a := range agents {
		if a = strings.TrimSpace(a); a != "" {
			scope[a] = struct{}{}
		}
	}
	if single != "" {
		scope[single] = struct{}{}
	}
	return scope
}

// watchdogScope is the resolved PANE scope plus the membership metadata the caller
// needs for non-fatal observability: per-name typo warnings (unknown), the
// transient-read note (membershipNote), and — when the pane surface came out empty —
// why (paneInertReason). recovery carries the occupancy surface's configuration, read
// from the same startup.json load rather than a second one.
//
// agents may legitimately be EMPTY. That is the #596 Decision 4 revision of issue #408:
// an empty pane scope is an inert pane surface, not a refusal to run.
type watchdogScope struct {
	agents          map[string]struct{}   // the pane set to monitor; empty ⇒ inert pane surface
	unknown         []string              // configured names absent from agents.json (typos)
	membershipNote  string                // non-empty when the all-unknown check was skipped (transient read)
	paneInertReason string                // non-empty when the pane surface resolved to empty
	paneMisconfig   bool                  // the pane surface is inert because names do NOT exist, not because none were configured
	recovery        config.RecoveryConfig // the occupancy surface's knobs (startup.json recovery block)
}

// resolveWatchdogScope is the watchdog's single authority for its PANE scope. It
// self-reads startup.json.watchdog_agents (NOT the CLI flags) under root, folds it
// through the Phase-1 buildWatchdogScope contract, and validates membership against
// agents.json.
//
// #596 Decision 4 — DELIBERATE REVISION of issue #408. This function previously
// returned a non-nil error, and therefore a non-zero exit, on an empty scope and on an
// all-unknown scope. Both refusals are gone. They are replaced by an INERT pane surface,
// because the refusal was solving the wrong half of the problem:
//
//   - What #408 actually bounded (.designs/408/security.md:13) was kill/respawn blast
//     radius — a watchdog scoped to "all" would "detect, checkpoint, kill, and respawn
//     any agent session it sees". That containment is preserved EXACTLY: pollAgents
//     fail-closes per agent on the scope map, so an empty map captures no pane, sends no
//     nudge, and respawns nobody. It is enforced by the frozen
//     TestWatchdog_PollSilenceRespectsAgentType.
//   - What the refusal ALSO did, unintentionally, was leave a factory whose startup.json
//     omits watchdog_agents with no supervising process at all — reproducing the very
//     incident #596 exists to prevent, in which the exhausted agent was outside the
//     configured scope and nothing was watching it.
//
// So the process now always starts, and the two surfaces are scoped independently.
// A genuinely unreadable startup.json is still a hard error: that is a broken factory,
// not a narrow configuration.
//
// Membership keys on agents.json, NOT on a live session: a configured-but-not-running
// agent is "known". A failed/partial agents.json read is NOT escalated to an all-unknown
// verdict (transient-read guard, N-2); the configured scope is used unvalidated with
// membershipNote set. The refuse/membership decision lives here in the cmd layer, never
// in internal/config (ADR-004). The agents.json read here is a read-once-at-start
// snapshot, separate from pollAgents' per-tick read (N-1).
func resolveWatchdogScope(root string) (watchdogScope, error) {
	startupCfg, err := config.LoadStartupConfig(root)
	if err != nil {
		return watchdogScope{}, err
	}

	// Source the scope from startup.json (not flags), reusing the Phase-1 contract
	// so blank entries drop and an empty result is the non-nil empty map.
	scope := buildWatchdogScope(startupCfg.WatchdogAgents, "")
	if len(scope) == 0 {
		return watchdogScope{
			agents:   scope,
			recovery: startupCfg.Recovery,
			paneInertReason: fmt.Sprintf("no watchdog_agents configured in %s",
				config.StartupConfigPath(root)),
		}, nil
	}

	agentsCfg, agErr := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if agErr != nil || agentsCfg == nil {
		// Transient/partial read (unreadable/absent agents.json): do NOT treat as
		// all-unknown. Prefer monitoring the configured (non-empty) scope over
		// narrowing to nothing on a flaky read. A successfully-parsed but EMPTY map is
		// NOT a transient read — it falls through to the all-unknown branch below
		// (#408/PR#410).
		return watchdogScope{
			agents:   scope,
			recovery: startupCfg.Recovery,
			membershipNote: "watchdog: could not validate scope membership — agents.json " +
				"unreadable; monitoring the configured scope unvalidated",
		}, nil
	}

	// Membership = a plain agents.json map lookup (the warnUnknownWatchdogAgents
	// idiom).
	var unknown []string
	known := 0
	for name := range scope {
		if _, ok := agentsCfg.Agents[name]; ok {
			known++
		} else {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if known == 0 {
		// Every configured name is unknown. The pane surface has nothing real to watch,
		// so it goes inert — but unlike the empty case this is an operator error, not a
		// supported configuration, and paneMisconfig keeps it loud and breadcrumbed.
		// The returned set is empty rather than the configured-but-nonexistent names:
		// pollAgents intersects against agents.json, so the two are behaviourally
		// identical, and an empty map states the outcome instead of implying monitoring.
		return watchdogScope{
			agents:          map[string]struct{}{},
			unknown:         unknown,
			recovery:        startupCfg.Recovery,
			paneMisconfig:   true,
			paneInertReason: fmt.Sprintf("none of watchdog_agents {%s} exist in agents.json", strings.Join(unknown, ", ")),
		}, nil
	}

	return watchdogScope{agents: scope, unknown: unknown, recovery: startupCfg.Recovery}, nil
}

// describePaneScope renders the pane surface for the startup line, naming the inert case
// and exactly what inertness means so an operator reading one line knows the blast radius.
func describePaneScope(ws watchdogScope) string {
	if len(ws.agents) == 0 {
		reason := ws.paneInertReason
		if reason == "" {
			reason = "empty scope"
		}
		return fmt.Sprintf("INERT (%s) — no pane captured, no silence nudges, no keystrokes sent", reason)
	}
	names := make([]string, 0, len(ws.agents))
	for name := range ws.agents {
		names = append(names, name)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// describeRecoveryScope renders the occupancy surface for the startup line. The scope
// itself is computed per tick inside pollOccupancy (agents.json ∩ live sessions under
// this root), so what is printable at startup is the RULE plus the operator's opt-outs.
func describeRecoveryScope(cfg config.RecoveryConfig) string {
	if !cfg.IsEnabled() {
		return "DISABLED (startup.json recovery.enabled=false)"
	}
	desc := "every live agent in agents.json"
	if len(cfg.Exclude) > 0 {
		excluded := append([]string(nil), cfg.Exclude...)
		sort.Strings(excluded)
		desc += fmt.Sprintf(", excluding {%s}", strings.Join(excluded, ", "))
	}
	return desc
}

func pollAgents(cmd *cobra.Command, root string, scope map[string]struct{}, agentStates map[string]*watchdogAgentState, failures map[string]int, silenceThreshold int) {
	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: failed to load agents config: %v\n", err)
		return
	}

	tx := newWatchdogTmux()

	for name, entry := range agentsCfg.Agents {
		// nil/empty scope monitors NOTHING — the watchdog must refuse before
		// reaching here (issue #408); a nil scope is never "all".
		if _, in := scope[name]; !in {
			continue
		}

		// A stale improvement_pending marker (#483) is a
		// zombie improvement session that never ran `af improvement complete` — it
		// holds a worktree slot indefinitely. Reap it before the HasSession check,
		// because the reap target may already be a dead session.
		if maybeReapImprovement(root, name) {
			continue
		}

		sessionID := session.SessionName(name)

		running, err := tx.HasSession(sessionID)
		if err != nil || !running {
			continue
		}

		if !tx.IsClaudeRunning(sessionID) {
			if checkCircuitBreaker(failures, name) {
				continue
			}
			failures[name]++
			recoverAgent(root, name, entry, "Claude process crashed", triggerCrash, false)
			continue
		}

		output, err := tx.CapturePane(sessionID, 50)
		if err != nil {
			continue
		}

		if detected, pattern, mailOnly := detectErrorPattern(output); detected {
			// The mail-only posture (recoverAgent leaves the session running) gets its OWN bounded
			// counter so it cannot consume the crash-respawn budget keyed under the bare name; a later
			// genuine crash then still finds failures[name] clear and recycles (BODY-1/F5).
			if mailOnly {
				if checkMailOnlyEscalation(failures, name) {
					continue
				}
				failures[mailOnlyKey(name)]++
				recoverAgent(root, name, entry, pattern, triggerErrorPattern, true)
				continue
			}
			if checkCircuitBreaker(failures, name) {
				continue
			}
			failures[name]++
			recoverAgent(root, name, entry, pattern, triggerErrorPattern, false)
			continue
		}

		// The silence path takes its OWN joined capture. detectErrorPattern above keeps the
		// plain physical-row view deliberately: -J would let a needle that today straddles a
		// wrap boundary start matching, and changing error detection while relocating the
		// silence invariant would put two behavior changes in one diff. (That wrapped-needle
		// blind spot is real and pre-existing — worth its own change, not this one.)
		joined, err := tx.CapturePaneJoined(sessionID, 50)
		if err != nil {
			continue
		}

		if checkSilence(name, joined, agentStates, silenceThreshold) {
			// Interactive agents are human-supervised: like the crash path
			// (recoverAgent), never act on them automatically — no nudge.
			if shouldAutoRecover(entry.Type) {
				handleSilenceNudge(sessionID, name, agentStates, failures)
			}
			continue
		}

		resetCircuitBreaker(failures, name)
		resetMailOnlyEscalation(failures, name)
	}
}
