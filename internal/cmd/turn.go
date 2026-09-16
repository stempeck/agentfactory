package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
	"github.com/stempeck/agentfactory/internal/tokenomics"
	"github.com/stempeck/agentfactory/internal/transcript"
)

// The judge-facing format is FROZEN CONTRACT (K2, design-doc.md:100-102, api.md:43-54): the gate
// prompt Phase 6 ships documents exactly these strings, and its format-contract test asserts the
// prompt and this renderer agree. Two consequences bind everything below:
//
//   - The three marker strings are NEVER spelled here. MarkerNoCalls and MarkerUnavailable are
//     referenced as constants and the truncation marker comes from Evidence.Marker(), whose format
//     string is deliberately unexported (evidence.go:42). A second, correct-today copy of any of
//     them would pass this phase's acceptance criteria and break Phase 6 instead of failing loudly.
//   - The defaults come from transcript.DefaultOptions() rather than from re-declared literals, so
//     20 / 5 / 300 / 300 has exactly one definition (evidence.go:45-49).
const (
	evidenceHeader = "Tool activity for THIS TURN ONLY (chronological, oldest first; results paired to calls):"

	// turnIncompleteFormat is NOT one of the frozen markers — it is the disclosure the package's own
	// doc comment requires of this consumer (evidence.go:20-23): "A turn that is degraded while
	// still showing calls — unmatched_results above zero, or a record the reader lost — has no
	// frozen string of its own, so a consumer that renders only Marker() would hide it. Render
	// Turn.Complete and Turn.UnmatchedResults alongside the marker, never instead of it."
	//
	// It is emitted unconditionally whenever the turn is incomplete, including when Marker() already
	// reported a cap, because those two disclose different things: the marker says calls were
	// dropped, this says the evidence itself may not be sound. It reuses the "EVIDENCE IS PARTIAL"
	// fragment design-doc.md:101 pins so the judge's partial-evidence rule keys on one phrase.
	turnIncompleteFormat = "[turn evidence incomplete (complete=%t, unmatched_results=%d) — EVIDENCE IS PARTIAL]"

	// defaultTurnMaxBytes is the 16KiB output bound pinned at design-doc.md:100. The design fixes
	// the value and leaves the trimming algorithm to this command; see fitTurnEvidence.
	defaultTurnMaxBytes = 16 << 10

	formatText = "text"
	formatJSON = "json"
)

var turnCmd = &cobra.Command{
	Use:   "turn",
	Short: "Turn-scoped inspection commands",
}

var evidenceCmd = &cobra.Command{
	Use:   "evidence",
	Short: "Print this turn's tool-call evidence for a gate judge",
	Long: fmt.Sprintf(`Derive the tool activity of the turn that is ending from a Claude Code session
transcript and render it as a block a judge can read.

Intended for scripting and hook consumption: the fidelity-gate and quality-gate
Stop hooks call this command and embed its text output in the judge prompt, so
both gates present the same evidence derived the same way.

Evidence is scoped to the triggering turn alone, emitted oldest-first in the
order the calls executed, with each result paired to the call that produced it
by tool_use_id. Sub-agent (sidechain) activity is excluded. Degradation is
disclosed rather than guessed: a turn whose evidence hit the cap, whose results
could not all be paired, or whose records could not all be read says so in the
output.

A missing, unreadable or malformed transcript is not an error. The command still
exits 0 and prints %q, so a Stop hook is never
blocked by gate infrastructure (ADR-007). Only a usage error — an unknown
--format — exits non-zero, which callers treat as evidence being unavailable.`,
		transcript.MarkerUnavailable),
	RunE: runTurnEvidence,
}

// fidelityInterventionHeading is the line the fidelity gate prints above this verb's output, and it
// is declared HERE in Go while being spelled in bash, in two places (hooks/fidelity-gate.sh and
// internal/cmd/install_hooks/fidelity-gate.sh). The duplication is deliberate and pinned:
// TestFidelityGatePromptExcusesHarnessActions greps both scripts for this exact constant, so a
// reworded heading fails loudly here instead of quietly detaching the judge's instruction — which
// names this section — from the section itself.
const fidelityInterventionHeading = "System interventions this turn:"

// interventionEffects say WHAT the harness told the agent, one fixed clause per mechanism.
//
// Without them the section reads `- dispatch: advise`, and the grader's instruction — excuse
// "anything the listed intervention accounts for" — is unbounded from the judge's side: a haiku
// grader handed a broad excuse and an uninformative label will excuse more than was excused, which
// would weaken the gate rather than correct its one known false positive. The judge cannot excuse a
// behaviour it was never told about.
//
// Keyed on the CLOSED vocabulary and written out here, so no free text and no agent- or
// operator-supplied string can reach the grader's prompt. A mechanism with no clause renders the
// bare label, which is the pre-Phase-5 behaviour and is why the map is not exhaustive by force:
// escalate moves work to a different backend rather than counselling it, and inventing an excuse for
// it would be licensing something no mechanism asks for.
//
// Interview gained a clause with #678 K8, and it is the clause the grader needs most, because what
// this mechanism does is WITHHOLD output. A turn primed with the identity block omitted, or with the
// checkpoint block superseded by a brief, is a turn the harness gave less context to — and a grader
// that judged it against a first turn's priming would fault the agent for a reduction the harness
// applied.
//
// Effort's clause moved with the actuator. The level is no longer chosen from the headroom left: it
// comes from what prior runs of this step generated, so the reduction is in force from a session's
// first turn on a window with no pressure at all, and a clause naming headroom would tell the grader
// something the arithmetic no longer says.
var interventionEffects = map[string]string{
	string(tokenomics.MechanismBudget):    "the harness told this session its next step would not fit and to hand off or narrow scope",
	string(tokenomics.MechanismThrift):    "the harness told this session to read narrowly and not re-read what is already in context",
	string(tokenomics.MechanismDispatch):  "the harness told this session to launch sub-agents one at a time and wait for each to return",
	string(tokenomics.MechanismEffort):    "the harness started this session at reduced reasoning effort for this step",
	string(tokenomics.MechanismInterview): "the harness withheld priming this session had already received, so this turn was given less context than a first turn would be",
}

var interventionsCmd = &cobra.Command{
	Use:   "interventions",
	Short: "Print the harness interventions that fired during this turn",
	Long: `List the token-economics mechanisms that acted on this agent since a turn
boundary, one per line, naming the mechanism and what it did.

Intended for scripting and hook consumption: the fidelity-gate Stop hook calls
this command and splices its output into the judge prompt, so a grader can tell
an agent that deviated from its step contract apart from one the harness told to
wait, to work at reduced effort, or to hand off (#668 K15).

The source is af's own append-only record log, never the session transcript --
which is what makes this a different command from ` + "`af turn evidence`" + ` rather than a
flag on it.

Nothing is printed when the turn had no interventions, when --since names no
parsable boundary, or when the record log cannot be read. All of those exit 0:
this runs inside a Stop hook that must never be blocked by gate infrastructure
(ADR-007), and an empty section leaves the judge prompt byte-identical to what it
would have been.`,
	RunE: runTurnInterventionsCmd,
}

func init() {
	evidenceCmd.Flags().String("transcript", "", "Path to the Claude Code session transcript (JSONL)")
	evidenceCmd.Flags().String("format", formatText, "Output format: text (judge-facing block) or json (evidence record)")
	evidenceCmd.Flags().Int("max-calls", transcript.DefaultOptions().MaxCalls,
		"Maximum tool calls to show, as a head+tail window over the turn")
	evidenceCmd.Flags().Int("max-bytes", defaultTurnMaxBytes, "Maximum size of the rendered output in bytes (0 or less: unbounded)")
	turnCmd.AddCommand(evidenceCmd)

	interventionsCmd.Flags().String("since", "", "Turn boundary timestamp; records at or after it belong to this turn")
	interventionsCmd.Flags().String("agent", "", "Agent whose record log to read (default: $AF_ROLE, else the working directory's agent)")
	turnCmd.AddCommand(interventionsCmd)

	rootCmd.AddCommand(turnCmd)
}

// runTurnInterventionsCmd resolves the two things the core cannot: which factory's log to read and
// whose. Both are resolved the way the gate script beside it resolves them — AF_ROOT and AF_ROLE
// first, the working directory second — so the command and its caller cannot disagree about which
// agent's turn is being graded.
//
// Every resolution failure returns nil with nothing printed, for runTurnEvidence's reason.
func runTurnInterventionsCmd(cmd *cobra.Command, _ []string) error {
	since, _ := cmd.Flags().GetString("since")
	agent, _ := cmd.Flags().GetString("agent")

	// resolveWatchdogRoot rather than a raw AF_ROOT read, and rather than a fifth resolver of this
	// package's own: it is already the "AF_ROOT first, cwd second" seam, and it NORMALISES the env
	// value through config.FindFactoryRoot before trusting it. That normalisation is the whole point
	// — AF_ROOT may itself carry a .factory-root redirect (helpers.go:464-466), and TelemetryDir of an
	// un-redirected path names a directory with no steps/<agent>.jsonl in it. The verb would then
	// print nothing forever and the only symptom would be a K15 section that never appears. The
	// writer never consults AF_ROOT at all, which is exactly why preferring the raw value here is
	// what would make the two disagree.
	root, err := resolveWatchdogRoot()
	if err != nil {
		return nil
	}
	if agent == "" {
		if agent = os.Getenv("AF_ROLE"); agent == "" {
			cwd, werr := getWd()
			if werr != nil {
				return nil
			}
			if agent, err = resolveAgentName(cwd, root); err != nil {
				return nil
			}
		}
	}
	return runTurnInterventionsCore(cmd.OutOrStdout(), root, agent, since)
}

// runTurnInterventionsCore renders the interventions recorded for agent at or after since.
//
// The window is half-open from `since` forward with no upper edge, and that asymmetry is right: the
// gate runs at the END of the turn it is grading, so "not yet recorded" and "belongs to the next
// turn" are the same empty set. A record stamped exactly at the boundary belongs to this turn — the
// boundary is the user message that STARTED it, so anything at that instant is a consequence of it.
//
// An unparsable `since` prints nothing rather than defaulting to the epoch. fidelity-gate.sh:197
// substitutes the literal "unknown" whenever the extractor found no boundary, and that string
// reaches this verb on every such turn; treating it as "no lower bound" would hand the grader every
// intervention the agent has ever received and excuse a turn that deviated for its own reasons.
//
// STATED RESIDUAL — the boundary is the last user record carrying no tool_result
// (transcript/evidence.go:259-260), so an advisory injected by the SessionStart prime hook lands
// BEFORE the first turn's boundary and is not reported to the grader for that turn. Every later
// step is unaffected: the work loop has the agent run af prime itself, whose record is a tool result
// and therefore after the boundary, and the K18 observer fires on PostToolUse, which is mid-turn by
// construction. The residual is left rather than papered over because the fix requires knowing that
// a boundary is a session's FIRST, which is not derivable from a timestamp — and its direction is
// the safe one: this under-reports, which costs the grader context it did not have before this
// phase, where the alternative would import a stale advisory into every later turn.
func runTurnInterventionsCore(out io.Writer, root, agent, since string) error {
	if root == "" || agent == "" {
		return nil
	}
	boundary, err := time.Parse(time.RFC3339, since)
	if err != nil {
		return nil
	}
	// A malformed line is skipped by the reader and costs only itself (ReadStats.Malformed), which is
	// the behaviour this verb wants: one corrupt record must not blind the grader to the ones beside
	// it. A hard read error is different and yields nothing, because a log that could not be opened
	// at all is not evidence that nothing fired.
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(root), telemetry.Filter{Agent: agent})
	if err != nil {
		return nil
	}

	var b strings.Builder
	armFiredEffort, inWindowEffort := false, false
	for _, r := range records {
		if r.Event != telemetry.EventIntervention || r.Mechanism == "" {
			continue
		}
		reducedEffort := r.Mechanism == string(tokenomics.MechanismEffort) && r.Action == telemetry.ActionReduceEffort
		// The effort arm having fired at all is proof it applied a reduction in THIS factory, which is
		// what separates a real treatment from an ambient host effort setting the env may carry into
		// any process. It is read across the WHOLE log, not just this window, because the record that
		// applied the reduction was stamped once at the relaunch boundary that falls before every turn
		// the reduction then excuses.
		if reducedEffort {
			armFiredEffort = true
		}
		ts, err := time.Parse(telemetry.TimestampLayout, r.TS)
		if err != nil || ts.Before(boundary) {
			continue
		}
		fmt.Fprintf(&b, "- %s: %s", r.Mechanism, r.Action)
		if effect := interventionEffects[r.Mechanism]; effect != "" {
			fmt.Fprintf(&b, " — %s", effect)
		}
		if r.EffortLevel != "" {
			fmt.Fprintf(&b, " (effort_level=%s)", r.EffortLevel)
		}
		if r.StepID != "" {
			fmt.Fprintf(&b, " [step %s]", r.StepID)
		}
		b.WriteByte('\n')
		if reducedEffort {
			inWindowEffort = true
		}
	}
	// The effort reduction is persistent session state, not a per-turn event, so
	// its single boundary-stamped record falls before this turn's window and the grader of a reduced
	// turn would never be told. When the arm has fired and the session-scoped current-effort surface
	// still reports a reduced level, surface it here even with no in-window record — never falling back
	// to the raw env alone, which every process inherits, so a factory that never reduced effort stays
	// silent. Only the persistent effort reduction is un-filtered this way; every other pre-boundary
	// record stays filtered, because those are turn-scoped events rather than standing state.
	if armFiredEffort && !inWindowEffort {
		if level := os.Getenv(config.EnvEffortLevel); level != "" {
			fmt.Fprintf(&b, "- %s: %s", tokenomics.MechanismEffort, telemetry.ActionReduceEffort)
			if effect := interventionEffects[string(tokenomics.MechanismEffort)]; effect != "" {
				fmt.Fprintf(&b, " — %s", effect)
			}
			fmt.Fprintf(&b, " (effort_level=%s)\n", level)
		}
	}
	_, _ = io.WriteString(out, b.String())
	return nil
}

// runTurnEvidence is the RunE for `af turn evidence`. It returns nil for every transcript-side
// outcome — missing file, unreadable path, malformed records, no turn boundary — because
// Execute() (root.go:29-39) turns any non-nil error into exit 1, and this command runs inside a Stop
// hook that must never be blocked by gate infrastructure (ADR-007). The unavailable state is carried
// in the OUTPUT instead, exactly as `af step current` carries its failures in a state field
// (step.go:86-89).
//
// The single exception is a hard usage error, which api.md:39 keeps non-zero: a caller that asked
// for a format this command cannot produce gets an error rather than silently different bytes.
func runTurnEvidence(cmd *cobra.Command, _ []string) error {
	path, _ := cmd.Flags().GetString("transcript")
	format, _ := cmd.Flags().GetString("format")
	maxCalls, _ := cmd.Flags().GetInt("max-calls")
	maxBytes, _ := cmd.Flags().GetInt("max-bytes")

	if format != formatText && format != formatJSON {
		return fmt.Errorf("invalid --format %q: must be %q or %q", format, formatText, formatJSON)
	}

	// Options.normalised() already restores every non-positive knob to its default (evidence.go:77-91),
	// so a zero or negative --max-calls needs no clamping here.
	opts := transcript.DefaultOptions()
	opts.MaxCalls = maxCalls

	renderTurnEvidence(cmd.OutOrStdout(), transcript.DeriveFile(path, opts), format, maxBytes, opts.HeadCalls)
	return nil
}

// renderTurnEvidence writes the evidence in the requested format, bounded to maxBytes.
//
// Write errors are discarded the way step.go:201-236's fmt.Println calls discard theirs: stdout
// being gone is not a reason to fail a gate.
func renderTurnEvidence(out io.Writer, ev transcript.Evidence, format string, maxBytes, headCalls int) {
	if maxBytes > 0 {
		ev = fitTurnEvidence(ev, format, maxBytes, headCalls)
	}
	_, _ = out.Write(renderTurnEvidenceBytes(ev, format))
}

func renderTurnEvidenceBytes(ev transcript.Evidence, format string) []byte {
	if format == formatJSON {
		// A turn with no calls has a nil Calls slice, which marshals to `null` rather than to the
		// array design-doc.md:108 pins. That difference is not cosmetic for the consumer this
		// command exists to serve: `jq '.calls[]'` over null fails with "Cannot iterate over null"
		// and exits 5, so an empty turn would look to a hook like a broken command rather than like
		// an authoritative "no tools were used". The shape is normalised here, at the seam that owns
		// the JSON contract, rather than in internal/transcript (Phase 2 owns that package).
		if ev.Calls == nil {
			ev.Calls = []transcript.Call{}
		}
		data, err := json.Marshal(ev)
		if err != nil {
			// Unreachable with this struct — no channels, no funcs — but a gate that printed
			// nothing would look like a turn that used no tools, so fall back to the marker that
			// says the opposite.
			return []byte(transcript.MarkerUnavailable + "\n")
		}
		return append(data, '\n')
	}
	return []byte(renderTurnEvidenceText(ev))
}

func renderTurnEvidenceText(ev transcript.Evidence) string {
	marker := ev.Marker()

	// The two states that replace the block rather than annotate it: no turn boundary was found, or
	// the turn is over and used no tools. Both are single, self-contained sentences (api.md:51-54).
	if ev.Turn.BoundaryUUID == "" || ev.Turn.CallsTotal == 0 {
		return marker + "\n"
	}

	var b strings.Builder
	b.WriteString(evidenceHeader)
	b.WriteByte('\n')
	if marker != "" {
		b.WriteString(marker)
		b.WriteByte('\n')
	}
	if !ev.Turn.Complete {
		fmt.Fprintf(&b, turnIncompleteFormat+"\n", ev.Turn.Complete, ev.Turn.UnmatchedResults)
	}
	for _, c := range ev.Calls {
		// Seq is the call's position in the WHOLE turn, so a head+tail window shows its own gap
		// (evidence.go:120-121) — numbering the lines 1..N instead would hide the omission the
		// marker just declared.
		fmt.Fprintf(&b, "%d. %s(%s)\n", c.Seq, c.Tool, renderTurnCallInput(c.Input))
		b.WriteString(renderTurnCallResult(c.Result))
	}
	return b.String()
}

// renderTurnCallInput renders a call's arguments in sorted key order. Call.Input is a map, and Go
// randomises map iteration, so unsorted output would differ between two runs over the same
// transcript — which would make the evidence unreproducible and every golden flaky.
func renderTurnCallInput(input map[string]string) string {
	if len(input) == 0 {
		return ""
	}
	keys := make([]string, 0, len(input))
	for k := range input {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%q", k, input[k]))
	}
	return strings.Join(parts, ", ")
}

// renderTurnCallResult renders the result beneath the call it belongs to, or says plainly that no
// result was recorded. A call whose result had not been written yet has Result == nil and nothing is
// invented to fill it (evidence.go:125-126); rendering it as an empty success would tell the judge
// the call completed.
func renderTurnCallResult(r *transcript.Result) string {
	if r == nil {
		return "   -> (no result recorded in this turn)\n"
	}
	status := "ok"
	if r.IsError {
		status = "error"
	}
	line := fmt.Sprintf("   -> result[%s]: %q", status, r.Content)
	if r.Truncated {
		// The cut length is read back off the content rather than threaded down from Options:
		// renderValue truncates to exactly `limit` runes (evidence.go:467-471), so the rendered
		// content IS the limit and the two can never disagree.
		line += fmt.Sprintf(" (truncated at %d chars)", utf8.RuneCountInString(r.Content))
	}
	return line + "\n"
}

// fitTurnEvidence bounds the RENDERED output to maxBytes by narrowing the head+tail window until it
// fits, then lets Evidence.Marker() re-declare the narrower window.
//
// Three alternatives were rejected. Reading only the first maxBytes of the transcript would make
// this a byte window over the input, which internal/transcript deliberately does not have: a byte
// window interacting with the line cap has caused three separate deadlock regressions in this repo
// (reader.go:14-20). Truncating the rendered TEXT would produce a JSON document no parser accepts,
// and would need a marker string outside the frozen set. Re-deriving with a smaller --max-calls
// would re-stream the whole transcript per attempt.
//
// Narrowing the evidence instead is what Marker() is built for: it recovers the head/tail split from
// the shown calls' Seq values "so the marker survives a JSON round trip through the CLI"
// (evidence.go:135-138). The dropped calls are therefore disclosed through the existing frozen
// string, with correct numbers, and json stays a valid document.
func fitTurnEvidence(ev transcript.Evidence, format string, maxBytes, headCalls int) transcript.Evidence {
	if len(ev.Calls) == 0 || len(renderTurnEvidenceBytes(ev, format)) <= maxBytes {
		return ev
	}

	// Rendered size grows with the number of calls kept, so the largest window that fits is found by
	// bisection. It is not STRICTLY monotone — the marker's own digits widen as the omitted count
	// grows — so the search can settle one call below the true optimum. It can never settle above
	// it: `best` is only ever assigned a window that was measured to fit.
	lo, hi, best := 0, len(ev.Calls)-1, 0
	for lo <= hi {
		mid := (lo + hi) / 2
		if len(renderTurnEvidenceBytes(narrowTurnEvidence(ev, mid, headCalls), format)) <= maxBytes {
			best = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	// If even zero calls overflows the budget, the header and its disclosure are what remains: the
	// evidence is bounded as far as it can be without emitting a block that lies about its own scope.
	return narrowTurnEvidence(ev, best, headCalls)
}

// narrowTurnEvidence keeps `keep` calls as a head+tail window and records the loss. CallsTotal is
// left alone — the turn still made that many calls, and rewriting it is how a partial view would
// come to describe itself as whole.
func narrowTurnEvidence(ev transcript.Evidence, keep, headCalls int) transcript.Evidence {
	if keep >= len(ev.Calls) {
		return ev
	}
	if keep < 0 {
		keep = 0
	}
	// The input is usually ALREADY a head+tail window, so its middle is a gap, not calls. Slicing it
	// as if it were contiguous takes head calls into the new tail, and Evidence.Marker() — which
	// recovers the split by walking Seq — then announces a "last N" the turn never ended with. A
	// marker describing a window other than the one on screen is the failure this block exists to
	// prevent, so the new split is bounded by the old one.
	inHead, inTail := turnWindowSplit(ev.Calls)

	// Mirrors the derivation's own guard (evidence.go:340-342): a head that fills the window leaves
	// no room for the most recent call, and recency is half of what the judge reads the block for.
	head := headCalls
	if head > keep-1 {
		head = keep / 2
	}
	if head > inHead {
		head = inHead
	}
	// Clamped from below as well as above: a negative head would slice past the end of the tail and
	// panic, and a panicking gate command exits non-zero — the one outcome ADR-007 forbids. No
	// caller can reach it today (runTurnEvidence always passes DefaultOptions().HeadCalls), but
	// renderTurnEvidence is package-visible and Phase 6 may grow a second caller.
	if head < 0 {
		head = 0
	}
	if tail := keep - head; tail > inTail {
		head += tail - inTail
	}

	kept := make([]transcript.Call, 0, keep)
	kept = append(kept, ev.Calls[:head]...)
	kept = append(kept, ev.Calls[len(ev.Calls)-(keep-head):]...)

	ev.Calls = kept
	ev.Turn.CallsShown = keep
	ev.Turn.Complete = false
	return ev
}

// turnWindowSplit reports how many of a window's calls are the turn's first ones and how many are
// its most recent ones, read off Seq the same way Evidence.Marker() does. For a window that is the
// whole turn the two overlap and both cover it — every call is available to either side.
func turnWindowSplit(calls []transcript.Call) (head, tail int) {
	if len(calls) == 0 {
		return 0, 0
	}
	for i, c := range calls {
		if c.Seq != i+1 {
			break
		}
		head = i + 1
	}
	tail = 1
	for i := len(calls) - 1; i > 0; i-- {
		if calls[i-1].Seq != calls[i].Seq-1 {
			break
		}
		tail++
	}
	return head, tail
}
