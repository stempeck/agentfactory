package statusline

import (
	"bufio"
	"encoding/json"
	"io"
)

const (
	// maxTranscriptLineBytes bounds one JSONL record. Real transcripts reach 2.4MB on a single line,
	// so this ceiling discards nothing observed. A line above it is discarded but its bytes are
	// still CONSUMED: a reader that re-attempts an over-long line never advances its byte offset, so
	// the cursor deadlocks and the counter freezes permanently (telemetry/store.go:389-405 solves
	// the same problem the same way).
	maxTranscriptLineBytes = 4 << 20
	transcriptReadBufBytes = 64 << 10
)

// TranscriptCursor is the token accumulator's persisted position in one session's transcript. It
// crosses the package boundary as plain numbers — no path, no transcript text (security.md E2.1).
type TranscriptCursor struct {
	// CumTokens is the figure the statusline displays. It never decreases, even when the transcript
	// is replaced by a shorter file and the reader re-derives from byte zero (L-R3).
	CumTokens int64
	// RederiveTokens is the total accrued since the current Offset epoch. It restarts at zero on an
	// offset reset, which is what lets CumTokens hold at max(stored, rederived-so-far) during
	// catch-up instead of either dropping or double-counting.
	RederiveTokens int64
	// Offset is the byte position in the transcript that the next read resumes from.
	Offset int64
	// HeadSig is a content fingerprint of the transcript's head bytes, stamped by the cmd-layer
	// reader (readTranscriptDelta). A byte offset is only meaningful within one generation of an
	// append-only file; when the file is replaced/rotated with a same-size-or-larger one the offset
	// is stale but ResetIfTruncated's size check cannot see it, so the reader compares HeadSig and
	// re-derives from zero on a mismatch. It crosses the package boundary as a plain number (E2.1);
	// the library only carries it, the cmd layer computes and acts on it.
	HeadSig int64
}

// TokenAccumulator turns the cursor persisted by the previous snapshot write into the current one.
// WriteSnapshotWith calls it ONLY when the throttle admits a real write, so transcript work is
// bound to the ≥10s write window rather than to the render rate (scale.md S3.4).
type TokenAccumulator func(prev TranscriptCursor) TranscriptCursor

// ResetIfTruncated re-bases a cursor whose offset no longer fits the file. A byte offset is only
// meaningful on an append-only file; a transcript that was replaced, rotated or truncated must be
// re-derived from the start. The counter is CARRIED, never rebuilt by subtraction (X6) — telemetry
// rejected a positional bookmark outright for this reason (store.go:73-79), and the clamp is the
// only thing that makes one safe here.
func (c TranscriptCursor) ResetIfTruncated(size int64) TranscriptCursor {
	if c.Offset >= 0 && c.Offset <= size {
		return c
	}
	return TranscriptCursor{CumTokens: c.CumTokens}
}

// usageRecord is the tolerant per-line decode. It deliberately reads only the two flat scalars that
// make up the headline figure: `cache_creation` and `iterations[]` are exact roll-ups of the flat
// fields (verified 461/461 and 1359/1359 in the K2-V capture), so decoding them would invite a
// double count, and cache splits are excluded from "tok" by design (design-doc.md:420).
type usageRecord struct {
	Message struct {
		ID    string `json:"id"`
		Usage *struct {
			InputTokens  int64 `json:"input_tokens"`
			OutputTokens int64 `json:"output_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

// MessageUsage is one message's usage after its records have been reduced, and Absorb is the
// reduction RULE itself — exported so the one statement of it has two callers rather than two
// implementations that could drift.
//
// The rule is MAX per field, and every word of that is load-bearing. Claude Code writes one record
// per content block and stamps the whole message's usage on every one, so a sum over-counts by
// ~2.2x. First-wins under-counts instead: an in-flight record carries a partial output_tokens, and
// keeping it in place of the completed count lost 26M output tokens across the measured corpus.
//
// The cache fields exist for Occupancy, whose caller measures how full the window got rather than
// what it cost. Know which producer built the value before calling it: ScanUsage leaves both cache
// fields ZERO, because the statusline's figure is SPEND and cache splits are excluded from it by
// design (design-doc.md:420), so Occupancy on a ScanUsage-built value silently returns a
// spend-shaped number. internal/cmd's step-close reader fills all four and is the only caller
// entitled to Occupancy today. Widening usageRecord would move no shipped number — ScanUsage sums
// Spend() — and is the fix if a statusline consumer ever needs occupancy.
//
// ThinkingTokens (#678 K1) is a PART of OutputTokens, not a fifth leg beside it — the host reports it
// in usage.output_tokens_details as a breakdown of the same output count. That is why it is absent
// from both Occupancy and Spend: adding it there would charge every thinking token twice and move two
// figures the statusline has been displaying since #622. It rides here rather than in a parallel map
// because the MAX-per-message.id rule below is the reduction it needs, and one statement of that rule
// with three callers is the point of this type.
type MessageUsage struct {
	InputTokens         int64
	OutputTokens        int64
	CacheReadTokens     int64
	CacheCreationTokens int64
	ThinkingTokens      int64
}

func (m *MessageUsage) Absorb(o MessageUsage) {
	m.InputTokens = max(m.InputTokens, o.InputTokens)
	m.OutputTokens = max(m.OutputTokens, o.OutputTokens)
	m.CacheReadTokens = max(m.CacheReadTokens, o.CacheReadTokens)
	m.CacheCreationTokens = max(m.CacheCreationTokens, o.CacheCreationTokens)
	// A record without the details object contributes 0 and therefore never lowers the MAX. That is
	// the whole rule for this leg: on a real host 32% of message ids carry the object on some of their
	// lines and not others — the detail-less ones are streaming partials — so "absent on any line
	// means unmeasured" would discard a third of all messages. Absence is only meaningful for the
	// MESSAGE (no line carried it), and that question is asked by the caller, not here.
	m.ThinkingTokens = max(m.ThinkingTokens, o.ThinkingTokens)
}

// Occupancy is how much of the context window this message was carrying: everything the model read
// plus what it wrote. It is a different quantity from Spend and includes the cache splits, because
// a cache-read token occupies the window exactly as an uncached one does — it is only cheaper.
func (m MessageUsage) Occupancy() int64 {
	return m.InputTokens + m.CacheReadTokens + m.CacheCreationTokens + m.OutputTokens
}

// Spend is the headline figure: what this message cost, cache splits excluded by design.
func (m MessageUsage) Spend() int64 { return m.InputTokens + m.OutputTokens }

// ScanUsage accumulates API-reported token spend from a window of transcript bytes starting at
// prev.Offset, and returns the cursor to persist. It never fails: a malformed line, a foreign
// record type or an empty window all degrade to "nothing new", because one bad line must never
// blank a render (telemetry/store.go:parseRecordFile idiom).
//
// Two properties carry the whole design, and both come from measuring real transcripts:
//
// Claude Code writes ONE RECORD PER CONTENT BLOCK and stamps the whole message's usage on every
// one, so summing records over-counts by ~2.2×. Records are therefore reduced per message.id with
// MAX per field — not first-wins, which would keep an in-flight record's partial output_tokens and
// drop the completed count (measured: 26M output tokens lost across the corpus), and not a sum.
// requestId is not part of the key: it is absent entirely on gateway-model records, and no
// message.id was ever observed spanning two requestIds, so including it could only ever split a
// message in two and over-count.
//
// A message's record run spans 7.0s of wall clock on average against a 10s write window, so a read
// lands INSIDE a run most of the time. The offset is therefore held back to the start of the
// trailing run and that message is excluded from this window's total, so the next read sees the
// whole run. Advancing merely past the last complete line over-counts by 28.9% (measured over 249
// transcripts); this holdback measured 0.0000%. The in-flight message's tokens lag by one tick —
// a temporary undercount, never the wrong direction (scale.md S3.1).
func ScanUsage(r io.Reader, prev TranscriptCursor) TranscriptCursor {
	br := NewTranscriptReader(r)

	type observation struct {
		start   int64
		id      string
		in, out int64
	}
	var obs []observation
	var consumed int64

	for {
		line, size, ok := ReadTranscriptLine(br)
		if !ok {
			break
		}
		start := consumed
		consumed += size
		if line == nil {
			continue
		}
		var u usageRecord
		if json.Unmarshal(line, &u) != nil {
			continue
		}
		if u.Message.Usage == nil || u.Message.ID == "" {
			continue
		}
		obs = append(obs, observation{
			start: start,
			id:    u.Message.ID,
			in:    u.Message.Usage.InputTokens,
			out:   u.Message.Usage.OutputTokens,
		})
	}

	next := TranscriptCursor{CumTokens: prev.CumTokens, RederiveTokens: prev.RederiveTokens, HeadSig: prev.HeadSig}
	if len(obs) == 0 {
		next.Offset = prev.Offset + consumed
		return next
	}

	trailing := obs[len(obs)-1].id
	hold := obs[len(obs)-1].start
	for i := len(obs) - 1; i >= 0 && obs[i].id == trailing; i-- {
		hold = obs[i].start
	}

	perMessage := make(map[string]MessageUsage, len(obs))
	for _, o := range obs {
		if o.id == trailing {
			continue
		}
		m := perMessage[o.id]
		m.Absorb(MessageUsage{InputTokens: o.in, OutputTokens: o.out})
		perMessage[o.id] = m
	}
	var added int64
	for _, m := range perMessage {
		added += m.Spend()
	}

	next.RederiveTokens = prev.RederiveTokens + added
	if next.RederiveTokens > next.CumTokens {
		next.CumTokens = next.RederiveTokens
	}
	next.Offset = prev.Offset + hold
	return next
}

// NewTranscriptReader sizes the buffered reader ReadTranscriptLine consumes. Exported alongside it
// so the size is chosen once rather than at each call site: it does not change WHICH lines are
// judged over-long — maxTranscriptLineBytes alone decides that — only how many ReadSlice rounds an
// ordinary line costs, and a consumer picking its own number would be re-deciding a performance
// trade this package has already measured against real transcripts.
func NewTranscriptReader(r io.Reader) *bufio.Reader {
	return bufio.NewReaderSize(r, transcriptReadBufBytes)
}

// ReadTranscriptLine returns the next COMPLETE line, the bytes it occupied including its terminator,
// and whether one was available. An over-long line is reported with a nil payload and a non-zero
// size so the caller skips its content but still advances past it. A trailing line with no
// terminator is NOT consumed: the window was cut mid-record, and the next read must start at that
// record's first byte rather than inside it.
//
// Exported for #668's step-close reader, which needs the same skip-don't-stop semantics over the
// same files. bufio.Scanner is the wrong tool and quietly so: it ABANDONS the rest of the file on a
// token above its cap, so one 4MB tool result would truncate a step's figures and still report them
// as measured. This deadlock/truncation class has been re-fixed three times here (fb8c4345,
// 72061892, d0923047), which is why the second consumer shares this implementation rather than
// growing a fourth copy of it.
func ReadTranscriptLine(br *bufio.Reader) ([]byte, int64, bool) {
	var buf []byte
	var size int64
	oversize := false
	for {
		chunk, err := br.ReadSlice('\n')
		size += int64(len(chunk))
		if size > maxTranscriptLineBytes {
			oversize = true
			buf = nil
		}
		if err == bufio.ErrBufferFull {
			if !oversize {
				buf = append(buf, chunk...)
			}
			continue
		}
		if err != nil {
			return nil, 0, false
		}
		if oversize {
			return nil, size, true
		}
		return append(buf, chunk...), size, true
	}
}
