package transcript

import (
	"bufio"
	"io"
)

const (
	// maxTranscriptLineBytes bounds one JSONL record. Real transcripts reach 2.4MB on a single line,
	// so this ceiling discards nothing observed. A line above it is discarded but its bytes are
	// still CONSUMED: a reader that re-attempts an over-long line never advances its byte offset, so
	// the cursor deadlocks and the counter freezes permanently.
	//
	// The value, the constant and readTranscriptLine below are COPIED from
	// internal/statusline/tokens.go:9-17,168-198 rather than imported: both are unexported there,
	// and importing statusline from here would invert the dependency (that package models tokens,
	// this one models tool evidence). The deadlock this cap prevents has been re-fixed three times
	// in this repo (fb8c4345, 72061892, d0923047), always because a byte window interacted badly
	// with the line cap — which is why this package streams the whole file and defines no read
	// window at all.
	//
	// Anchor drift, so the next reader does not re-derive it: design-doc.md:70 cites the cap as
	// tokens.go:168-198, but :168-172 is readTranscriptLine's doc comment and the constant is at
	// tokens.go:15.
	maxTranscriptLineBytes = 4 << 20
	transcriptReadBufBytes = 64 << 10
)

// readTranscriptLine returns the next COMPLETE line, the bytes it occupied including its terminator,
// and whether one was available. An over-long line is reported with a nil payload and a non-zero
// size so the caller skips its content but still advances past it. A trailing line with no
// terminator is NOT consumed: the window was cut mid-record, and the next read must start at that
// record's first byte rather than inside it.
func readTranscriptLine(br *bufio.Reader) ([]byte, int64, bool) {
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

// countingReader exists solely so the derivation can DETECT the unterminated trailing line that
// readTranscriptLine deliberately refuses to consume. That refusal is correct for the statusline's
// resumable cursor — the next tick re-reads the record — but this derivation is one-shot, so an
// undisclosed trailing record would be lost evidence reported as complete. Comparing bytes pulled
// from the source against bytes the line reader accounted for makes the loss visible without
// changing the copied function's semantics.
type countingReader struct {
	src io.Reader
	n   int64
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.src.Read(p)
	c.n += int64(n)
	return n, err
}
