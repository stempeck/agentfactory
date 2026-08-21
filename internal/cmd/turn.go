package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/spf13/cobra"
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

func init() {
	evidenceCmd.Flags().String("transcript", "", "Path to the Claude Code session transcript (JSONL)")
	evidenceCmd.Flags().String("format", formatText, "Output format: text (judge-facing block) or json (evidence record)")
	evidenceCmd.Flags().Int("max-calls", transcript.DefaultOptions().MaxCalls,
		"Maximum tool calls to show, as a head+tail window over the turn")
	evidenceCmd.Flags().Int("max-bytes", defaultTurnMaxBytes, "Maximum size of the rendered output in bytes (0 or less: unbounded)")
	turnCmd.AddCommand(evidenceCmd)
	rootCmd.AddCommand(turnCmd)
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
