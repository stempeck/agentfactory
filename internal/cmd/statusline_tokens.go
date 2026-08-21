package cmd

import (
	"errors"
	"hash/fnv"
	"io"
	"os"

	"github.com/stempeck/agentfactory/internal/statusline"
)

// maxTranscriptRead bounds ONE render's transcript read, so per-render I/O is a constant no matter
// how large the transcript grows (scale.md S3.1). A burst larger than this catches up over the
// following writes: the counter is cumulative, so a capped read undercounts temporarily and never
// in the wrong direction. It is deliberately STRICTLY GREATER than statusline.maxTranscriptLineBytes
// (4 MB): the oversize consume-and-skip fires only when a record EXCEEDS that line cap, so the read
// window must deliver a full line-cap-sized record whole. A window equal to (or below) the line cap
// makes the LimitReader hit EOF mid-record on a 1–4 MB line, so the offset never advances and the
// counter freezes permanently (statusline_tokens_test T8). The throttle bounds this read to the
// ≥10s write window, so the larger cap is still a per-render constant, not a per-tick cost.
const maxTranscriptRead = 5 << 20

var errTranscriptNotRegular = errors.New("statusline: transcript is not a regular file")

// transcriptAccumulator binds this render's transcript path to the token counter. An empty path —
// the payload field is absent, or upstream did not send it — yields a nil accumulator, which means
// WriteSnapshotWith does no transcript work at all: not a stat, not an open. That is what keeps the
// hot-path budget free for the payloads that carry no transcript.
func transcriptAccumulator(path string) statusline.TokenAccumulator {
	if path == "" {
		return nil
	}
	return func(prev statusline.TranscriptCursor) statusline.TranscriptCursor {
		cur, err := readTranscriptDelta(path, prev)
		if err != nil {
			// Silent on the render path (security.md E2.1 / C-3): a transcript that is missing,
			// deleted mid-session, or pointed at the wrong directory by an upstream bug must degrade
			// to "no new tokens", never blank the pane. The counter is preserved, never reset.
			return prev
		}
		return cur
	}
}

// readTranscriptDelta reads the new bytes of a session transcript and returns the advanced cursor.
// This is the whole filesystem seam for the token counter: opening, stat-ing, clamping, seeking and
// capping happen HERE in package cmd, which already owns every file interaction on the render path,
// while the counting arithmetic stays in the library where it is directly testable (conflicts.md
// D1×D2). The library never sees a path.
//
// transcript_path is a semi-trusted payload field, so the posture mirrors sanitizeSessionID's: the
// file is opened read-only and never created or modified, a non-regular file (device, FIFO,
// directory) is refused outright, and the read is capped — a hostile or corrupt file yields capped
// bytes that parse as zero records and are skipped. Only numbers cross back.
func readTranscriptDelta(path string, prev statusline.TranscriptCursor) (statusline.TranscriptCursor, error) {
	f, err := os.Open(path)
	if err != nil {
		return prev, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return prev, err
	}
	if !fi.Mode().IsRegular() {
		return prev, errTranscriptNotRegular
	}

	size := fi.Size()
	cur := prev.ResetIfTruncated(size)

	// Generation check (rotation reset). A byte offset is meaningful only within ONE generation of an
	// append-only file. ResetIfTruncated only catches a SHRINK; a transcript replaced or rotated with
	// a same-size-or-larger file keeps Offset<=size, so the offset is stale but the size check cannot
	// see it — seeking to it would permanently skip every record before it. A head-content fingerprint
	// does see it: on a normal append the head bytes are unchanged, on a replacement they differ.
	// Computed BEFORE the stat-first short-circuit below, because a same-size replacement leaves
	// Offset==size and would otherwise short-circuit with zero reads and never notice the new file.
	// Only numbers cross back into the cursor (security.md E2.1); the fingerprint is one int64.
	headSig, err := hashTranscriptHead(f, size)
	if err != nil {
		return prev, err
	}
	if cur.HeadSig != 0 && headSig != cur.HeadSig {
		cur = statusline.TranscriptCursor{CumTokens: cur.CumTokens} // replaced ⇒ re-derive from zero, carry the counter (L-R3)
	}
	cur.HeadSig = headSig

	if cur.Offset >= size {
		return cur, nil // nothing appended since the last read: no tail re-read (the head sample is O(1))
	}
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return prev, err
	}
	next := statusline.ScanUsage(io.LimitReader(f, maxTranscriptRead), cur)

	// Deadlock guard. A read that filled the WHOLE window (at least maxTranscriptRead bytes of file lie
	// beyond the offset) yet advanced the offset by nothing is stuck: either one record larger than the
	// window (its terminator never reached — F-B) or a run of records all sharing one message.id that
	// fills the window, so ScanUsage's trailing-run holdback collapses to offset 0 and never reaches the
	// next message.id that would release it (F-H). Force the offset past this window so the next read
	// resumes further on. The skipped records are NOT counted here; a message's max-per-id total is
	// re-counted once its run's tail lands in a window alongside a different id, so the counter only
	// undercounts temporarily and never over-counts (AC-2). The legitimate in-flight hold is untouched:
	// there the window reaches TRUE EOF (offset+cap >= size), so this guard stays silent.
	if next.Offset == cur.Offset && cur.Offset+maxTranscriptRead < size {
		next.Offset = cur.Offset + maxTranscriptRead
	}
	return next, nil
}

// headSampleBytes bounds the head sample hashTranscriptHead fingerprints. A transcript's first record
// carries a session id / summary line unique to that file, so a small prefix disambiguates a
// replacement from an append; a false "same prefix" is harmless (identical pre-offset content means no
// undercount). Fixed and small so the generation check is O(1) per read.
const headSampleBytes = 4096

// hashTranscriptHead returns an FNV-64 fingerprint of the transcript's first headSampleBytes bytes as
// a plain int64 (0 ⇒ empty file / unknown, which suppresses a spurious reset). It seeks the file to 0;
// callers that read the delta re-seek to the cursor offset afterwards.
func hashTranscriptHead(f *os.File, size int64) (int64, error) {
	if size == 0 {
		return 0, nil
	}
	n := int64(headSampleBytes)
	if size < n {
		n = size
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, err
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(f, buf); err != nil {
		return 0, err
	}
	h := fnv.New64a()
	_, _ = h.Write(buf)
	sig := int64(h.Sum64())
	if sig == 0 {
		sig = 1 // reserve 0 for "unknown" so a real head never suppresses its own generation check
	}
	return sig, nil
}
