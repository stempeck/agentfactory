// Package transcript derives one turn's tool evidence from a Claude Code session transcript.
//
// It replaces the session-tail reconstruction the Stop gates used to perform in bash
// (hooks/fidelity-gate.sh:144-172, byte-identical in hooks/quality-gate.sh:85-113), which reversed
// the whole transcript, took five JSONL lines, and sliced tool calls and tool results in two
// independent passes. Turn scoping (AC-1), execution order (AC-2), call/result pairing (AC-3) and
// partial-vs-complete disclosure (AC-4) are properties of this derivation rather than accidents of
// that pipeline (design-doc.md:70, decision D-1).
//
// Two rules govern everything here:
//
//   - Degradation is DISCLOSED, never guessed (design-doc.md:108). Any state that could hide missing
//     evidence — a cap that dropped calls, a result that matched no call, a record the reader had to
//     skip — clears Turn.Complete, and an absent boundary yields no calls at all rather than a
//     best-effort file-wide dump.
//   - The derivation is total. It never returns an error and never panics, because it runs inside a
//     Stop hook that must never be blocked by gate infrastructure (ADR-007). Malformed, foreign and
//     bookkeeping records are skipped the way internal/statusline/tokens.go:105-119 skips them.
//
// Marker returns the frozen text for the disclosure states that HAVE one. A turn that is degraded
// while still showing calls — unmatched_results above zero, or a record the reader lost — has no
// frozen string of its own, so a consumer that renders only Marker() would hide it. Render
// Turn.Complete and Turn.UnmatchedResults alongside the marker, never instead of it.
package transcript

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
)

// Marker strings and defaults are frozen contract (design-doc.md:100-102): the gate prompt documents
// exactly these, and Phase 3's CLI re-states them, so a re-spelling here would silently break the
// format-contract test rather than fail loudly.
const (
	MarkerNoCalls     = "No tool calls were made in this turn."
	MarkerUnavailable = "[tool evidence unavailable this turn]"

	markerPartialFormat = "[showing first %d + last %d of %d calls; %d middle calls in this turn omitted — EVIDENCE IS PARTIAL]"
)

const (
	defaultMaxCalls  = 20
	defaultHeadCalls = 5
	defaultTruncate  = 300
)

// Options carries the derivation's knobs as plain values. They are NOT read from the environment:
// this is a library package, and TestNoEnvReadsInLibraryPackages enforces that boundary across all
// of internal/ (ADR-004). The hook and the CLI own env overrides.
type Options struct {
	// MaxCalls caps the evidence in TOOL CALLS, not in JSONL lines — the distinction the replaced
	// `head -5` got wrong, since a turn's calls and the lines carrying them are unrelated counts.
	MaxCalls int
	// HeadCalls is the leading half of the head+tail window. Keeping the first calls as well as the
	// most recent ones preserves sequencing proof ("Read X before editing it") through truncation,
	// so a judge can PASS affirmatively instead of abstaining (design-doc.md:101, cross-review L-1).
	HeadCalls int
	// InputTruncate and ResultTruncate bound each rendered field in characters, matching the
	// existing `.[0:300]` convention at hooks/fidelity-gate.sh:163.
	InputTruncate  int
	ResultTruncate int
}

func DefaultOptions() Options {
	return Options{
		MaxCalls:       defaultMaxCalls,
		HeadCalls:      defaultHeadCalls,
		InputTruncate:  defaultTruncate,
		ResultTruncate: defaultTruncate,
	}
}

func (o Options) normalised() Options {
	if o.MaxCalls <= 0 {
		o.MaxCalls = defaultMaxCalls
	}
	if o.HeadCalls <= 0 {
		o.HeadCalls = defaultHeadCalls
	}
	if o.InputTruncate <= 0 {
		o.InputTruncate = defaultTruncate
	}
	if o.ResultTruncate <= 0 {
		o.ResultTruncate = defaultTruncate
	}
	return o
}

// Evidence is the record shape pinned by design-doc.md:108. Text renders from it; the JSON field
// names are the contract Phase 3's CLI serialises.
type Evidence struct {
	Turn  TurnMeta `json:"turn"`
	Calls []Call   `json:"calls"`
}

type TurnMeta struct {
	// BoundaryUUID is empty exactly when no turn boundary was found, which is the one condition
	// under which no evidence at all may be reported.
	BoundaryUUID string `json:"boundary_uuid"`
	BoundaryTS   string `json:"boundary_ts"`
	// Complete is false whenever anything could be missing: calls dropped by the cap, a result that
	// matched no call, or a record the reader could not read. It stays TRUE for a turn that simply
	// used no tools — an empty turn is an authoritative answer, and conflating it with an
	// unavailable one would let a parse failure masquerade as "no tools were used".
	Complete          bool `json:"complete"`
	CallsTotal        int  `json:"calls_total"`
	CallsShown        int  `json:"calls_shown"`
	SidechainExcluded int  `json:"sidechain_excluded"`
	// UnmatchedResults is the self-check: a result whose tool_use_id names no call in this turn
	// means the boundary is probably wrong, so the count is surfaced rather than the result being
	// attached to whichever call happened to sit next to it.
	UnmatchedResults int `json:"unmatched_results"`
}

type Call struct {
	// Seq is the call's position in the WHOLE turn, so a head+tail window shows its own gap.
	Seq   int               `json:"seq"`
	Tool  string            `json:"tool"`
	Input map[string]string `json:"input"`
	// Result is nil for a call the turn made but whose result had not been written yet. Nothing is
	// invented to fill it.
	Result *Result `json:"result,omitempty"`
}

type Result struct {
	Content   string `json:"content"`
	IsError   bool   `json:"is_error"`
	Truncated bool   `json:"truncated"`
}

// Marker returns the frozen text for this evidence record's disclosure state, or "" when the
// evidence is complete and non-empty. The head/tail split is recovered from the shown calls' Seq
// values so the marker survives a JSON round trip through the CLI.
func (e Evidence) Marker() string {
	if e.Turn.BoundaryUUID == "" {
		return MarkerUnavailable
	}
	if e.Turn.CallsTotal == 0 {
		// A turn that used no tools is an authoritative answer; a turn whose calls the reader could
		// not READ is not, and both arrive here as a zero count. Without this clause a turn whose
		// single 5MB Write record exceeded the line cap renders as the positive claim "no tool calls
		// were made", and the judge blocks an agent for doing nothing — a wrong verdict manufactured
		// by gate infrastructure, which is the failure ADR-007 and design-doc.md:108 both forbid.
		if !e.Turn.Complete {
			return MarkerUnavailable
		}
		return MarkerNoCalls
	}
	if e.Turn.CallsShown >= e.Turn.CallsTotal {
		return ""
	}
	head := 0
	for i, c := range e.Calls {
		if c.Seq != i+1 {
			break
		}
		head = i + 1
	}
	return fmt.Sprintf(markerPartialFormat, head, e.Turn.CallsShown-head, e.Turn.CallsTotal,
		e.Turn.CallsTotal-e.Turn.CallsShown)
}

// DeriveFile derives the turn from a transcript path. An unreadable path is not an error: it is the
// unavailable state, which is what the gate must report when it cannot see the turn.
func DeriveFile(path string, opts Options) Evidence {
	f, err := os.Open(path)
	if err != nil {
		return Evidence{}
	}
	defer f.Close()
	return Derive(f, opts)
}

// Derive reads a whole transcript and returns the evidence for its final turn.
//
// The turn is every record after the last plain non-sidechain type=="user" record — "plain" meaning
// its message.content carries no tool_result block, because within a turn every user record is a
// tool_result carrier and a turn opens when a prompt is submitted (data.md:21). The scan is a single
// forward pass: the transcript is append-ordered, so encounter order IS execution order (AC-2), and
// the accumulator simply restarts each time a new boundary appears rather than buffering the file.
func Derive(r io.Reader, opts Options) Evidence {
	opts = opts.normalised()

	counted := &countingReader{src: r}
	br := bufio.NewReaderSize(counted, transcriptReadBufBytes)

	var (
		boundaryUUID string
		boundaryTS   string
		calls        []Call
		callIDs      []string
		callSeen     = map[string]bool{}
		results      = map[string]Result{}
		unjoinable   int
		sidechain    int
		consumed     int64
		lineLost     bool
		blockDropped bool
	)

	for {
		line, size, ok := readTranscriptLine(br)
		if !ok {
			break
		}
		consumed += size
		if line == nil {
			// The record's bytes were consumed but its content is gone, so its TYPE is unknowable —
			// it may have been the prompt that opens this turn. Recorded globally rather than
			// per-turn for that reason.
			lineLost = true
			continue
		}

		// A blank line is the one unparseable shape that is not lost evidence — it never carried a
		// record — so it must not reach the disclosure below and manufacture a false degradation.
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}

		var rec record
		if json.Unmarshal(line, &rec) != nil {
			// The same loss by a different cause: a line that will not parse has an unknowable type
			// too, so it may equally have been this turn's opening prompt — and a missed boundary
			// re-imports the PREVIOUS turn's calls, which is precisely the AC-1 over-report this
			// package exists to remove. Foreign and bookkeeping records are NOT this case: they parse
			// cleanly into a zero value and fall through below, so ordinary traffic still costs
			// nothing. Skipping is mandatory (ADR-007); skipping SILENTLY is not.
			lineLost = true
			continue
		}

		// A spawned sub-agent's thread interleaved into the same file is not this agent's work.
		// Filtering here — BEFORE the boundary test — is what stops a sub-agent's own prompt from
		// truncating the dispatching turn. Real sidechains live in sibling files that
		// transcript_path does not reference, so this guard is defense in depth against a future
		// format change rather than the primary containment.
		if rec.IsSidechain {
			if boundaryUUID != "" {
				sidechain++
			}
			continue
		}

		blocks, decoded := contentBlocks(rec.Message)
		if !decoded {
			blockDropped = true
		}

		// A user record with no message, or with bare-string content, carries no tool_result block
		// and is therefore plain. That is the fail-safe reading: a boundary set too LATE only
		// under-reports, while one set too EARLY re-imports the previous turn's calls — the AC-1
		// violation this package exists to remove. A record with no uuid cannot be a boundary
		// because the evidence record would then be unable to say which turn it describes.
		if rec.Type == "user" && rec.UUID != "" && !carriesToolResult(blocks) {
			boundaryUUID, boundaryTS = rec.UUID, rec.Timestamp
			calls, callIDs = nil, nil
			callSeen = map[string]bool{}
			results = map[string]Result{}
			unjoinable, sidechain = 0, 0
			// A loss recorded BEFORE this boundary is now provably pre-boundary, so it says nothing
			// about this turn. Left sticky, one oversize line early in a session would mark every
			// later turn degraded for the rest of that session — and since Marker() now consults
			// Complete, a genuinely tool-free turn would render "unavailable" forever.
			//
			// blockDropped carries THIS record's own decode result rather than clearing: if the
			// boundary record's content array failed to decode, we cannot actually know it was
			// plain, so it may be a spurious boundary and that doubt must survive the reset.
			lineLost, blockDropped = false, !decoded
			continue
		}
		if boundaryUUID == "" {
			continue
		}

		for _, b := range blocks {
			switch b.Type {
			case "tool_use":
				// Block identity is the block's OWN id, never (message.id, index): Claude Code
				// writes one record per content block and stamps the whole message's id on each, so
				// a four-call message appears as four records whose blocks all sit at content[0]
				// (verification-report.md rows 61,63). Keying on the index would collapse them into
				// one call. Keying on the block id also gives the multi-block tolerance Gap 4 asks
				// for: the same call emitted twice, in either shape, is one call.
				if b.ID != "" {
					if callSeen[b.ID] {
						continue
					}
					callSeen[b.ID] = true
				}
				calls = append(calls, Call{
					Seq:   len(calls) + 1,
					Tool:  b.Name,
					Input: renderInput(b.Input, opts.InputTruncate),
				})
				callIDs = append(callIDs, b.ID)
			case "tool_result":
				if b.ToolUseID == "" {
					unjoinable++
					continue
				}
				if _, dup := results[b.ToolUseID]; dup {
					continue
				}
				content, truncated := renderValue(b.Content, opts.ResultTruncate)
				results[b.ToolUseID] = Result{
					Content:   content,
					IsError:   b.IsError != nil && *b.IsError,
					Truncated: truncated,
				}
			}
		}
	}

	if boundaryUUID == "" {
		return Evidence{}
	}

	// AC-3: the join is on tool_use_id. Anything left over is counted, not attached — the replaced
	// pipeline paired by position and so reported whichever result happened to be adjacent.
	matched := 0
	for i, id := range callIDs {
		if id == "" {
			continue
		}
		if res, ok := results[id]; ok {
			calls[i].Result = &res
			matched++
		}
	}
	unmatched := len(results) - matched + unjoinable

	total := len(calls)
	shown := calls
	if total > opts.MaxCalls {
		head := opts.HeadCalls
		if head > opts.MaxCalls-1 {
			head = opts.MaxCalls / 2
		}
		tail := opts.MaxCalls - head
		shown = append(append(make([]Call, 0, opts.MaxCalls), calls[:head]...), calls[total-tail:]...)
	}

	// A trailing record with no newline was cut mid-write and deliberately left unconsumed by the
	// line reader; it is still evidence this turn cannot show.
	degraded := lineLost || blockDropped || counted.n > consumed

	return Evidence{
		Turn: TurnMeta{
			BoundaryUUID:      boundaryUUID,
			BoundaryTS:        boundaryTS,
			Complete:          len(shown) == total && unmatched == 0 && !degraded,
			CallsTotal:        total,
			CallsShown:        len(shown),
			SidechainExcluded: sidechain,
			UnmatchedResults:  unmatched,
		},
		Calls: shown,
	}
}

// record is the tolerant per-line decode. Every field it names is present on 862/862 message-bearing
// records in the live 2.1.224 census (verification-report.md:115); bookkeeping records carrying none
// of them decode to a zero value and fall through harmlessly.
type record struct {
	Type        string   `json:"type"`
	UUID        string   `json:"uuid"`
	Timestamp   string   `json:"timestamp"`
	IsSidechain bool     `json:"isSidechain"`
	Message     *message `json:"message"`
}

// message deliberately does not decode message.id. Claude Code stamps one message's id on every
// record it spans, so the id identifies the message and not the block — using it as a dedup key
// would collapse a four-call message into one call. Block identity does that job instead.
type message struct {
	Content json.RawMessage `json:"content"`
}

// block covers both shapes in one struct because a content array mixes them: tool_use blocks are
// {type,id,name,input,caller} and tool_result blocks are {tool_use_id,type,content} plus an is_error
// that is absent on 13 of 218 observed results — hence the pointer, so "absent" and "false" stay
// distinguishable rather than both reading as success.
type block struct {
	Type      string          `json:"type"`
	ID        string          `json:"id"`
	Name      string          `json:"name"`
	Input     json.RawMessage `json:"input"`
	ToolUseID string          `json:"tool_use_id"`
	Content   json.RawMessage `json:"content"`
	IsError   *bool           `json:"is_error"`
}

// contentBlocks decodes message.content, which is polymorphic: an array of blocks on the records
// this package cares about, a bare string on a typed prompt, absent on a bookkeeping record. Only
// the array shape yields blocks; the other shapes are ordinary traffic and yield none.
//
// The second return value is false only for an array that FAILED to decode — a block whose scalar
// came back wrongly typed. That is lost evidence rather than ordinary traffic: the record's blocks
// vanish, and a user record whose tool_result block vanished with them becomes a spurious turn
// boundary. The caller cannot repair either, so it discloses them instead.
func contentBlocks(msg *message) ([]block, bool) {
	if msg == nil {
		return nil, true
	}
	trimmed := bytes.TrimLeft(msg.Content, " \t\r\n")
	if len(trimmed) == 0 || trimmed[0] != '[' {
		return nil, true
	}
	var blocks []block
	if json.Unmarshal(trimmed, &blocks) != nil {
		return nil, false
	}
	return blocks, true
}

func carriesToolResult(blocks []block) bool {
	for _, b := range blocks {
		if b.Type == "tool_result" {
			return true
		}
	}
	return false
}

// renderInput truncates PER FIELD, so one pasted blob cannot crowd out the arguments that show what
// the call actually did.
func renderInput(raw json.RawMessage, limit int) map[string]string {
	if len(raw) == 0 {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || len(fields) == 0 {
		return nil
	}
	out := make(map[string]string, len(fields))
	for k, v := range fields {
		s, _ := renderValue(v, limit)
		out[k] = s
	}
	return out
}

// renderValue flattens one JSON value to the text the judge reads. tool_result content is a string
// on some calls and an array of blocks on others, so a string-typed field would fail to decode and
// silently drop the result — which would then resurface as an inflated unmatched_results count.
func renderValue(raw json.RawMessage, limit int) (string, bool) {
	if len(raw) == 0 {
		return "", false
	}
	var s string
	if json.Unmarshal(raw, &s) != nil {
		var buf bytes.Buffer
		if json.Compact(&buf, raw) != nil {
			s = string(raw)
		} else {
			s = buf.String()
		}
	}
	if len(s) <= limit {
		return s, false
	}
	runes := []rune(s)
	if len(runes) <= limit {
		return s, false
	}
	return string(runes[:limit]), true
}
