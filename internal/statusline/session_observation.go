package statusline

import (
	"os"
	"path/filepath"
	"time"
)

// SessionObservation classifies ONE session's occupancy channel, keyed on the session id rather
// than on the agent (#622 C3). It is the read a step boundary must use.
//
// Why not ReadObservations. That reader is agent-keyed and resolves an agent's several session
// files by newest-written_at-wins (observation.go:347-349), which is right for a fleet sweep and
// inverted at a step boundary: after a session recycle the DEAD session's last snapshot still
// reads fresh for up to Staleness while the respawned session has not yet rendered its first one.
// A boundary decision keyed on the agent would therefore fire off a previous session's occupancy,
// spend the recovery rate cap, and land on a RECOVERY HALTED — an operator action taken on a
// measurement that was never about this session (cross-review CRIT-1).
//
// Why not readSnapshot. That is the WRITER's read-back path (daily.go:372): no attribution, no
// schema floor, no clamping. It cannot produce an Observation, so it cannot honour the invariant
// that no channel without a trustworthy datum ever reads healthy. This function decodes through
// readObservationFile — the same validating decoder ReadObservations uses — and classifies through
// ObservedReading, which is what makes its entry in the NoDatumFreeConstructorsExist allowlist
// truthful rather than a waiver.
//
// G3: the id is sanitized HERE. The caller's <agentDir>/.runtime/session_id holds the raw id and
// sanitizeSessionID is unexported, so a caller building the path itself would have to restate the
// filename grammar — and the two spellings would drift apart while every test kept passing, which
// is the issue #563 class of defect. Observation.SessionID() returns the sanitized stem for the
// same reason: it is the spelling a caller can actually compare against.
//
// G4(a): the roster arrives in opts and is never bypassed. A boundary caller knows its own name
// and passes a one-element roster, exactly as recovery.go:610 and dispatch.go:1152 already do, so
// a nil or empty KnownAgents yields no datum rather than "trust everything". Note the consequence
// that follows from the writer being unvalidated (daily.go:108-113): a snapshot whose agent field
// is empty — written honestly by a render that could not resolve its own role — is unattributable
// even when the session id matches. The file is ours and does not say so provably, so it is not
// evidence.
//
// G4(b): the cap enforced on this path is the READER's maxObservationBytes (64 KB), inherited by
// going through readObservationFile. daily.go's 4 KB cap bounds the writer's read-back of a file
// it just wrote, a different boundary, and TestReadObservations_SizeCapIs64KBNotWriterCap exists
// to stop the two being collapsed into one number.
//
// Thresholds are the caller's. ObservedReading is fail-closed and classifies every datum dark when
// either Staleness or DarkAfter is unset (observation.go:224-226), so a caller that forgot to
// configure this reader gets a visibly dark channel rather than a silently healthy one.
func SessionObservation(dir, sessionID string, opts ReadOptions, now time.Time) ChannelReading {
	sid := sanitizeSessionID(sessionID)
	if sid == "" {
		return NoReading()
	}
	path := filepath.Join(dir, sid+".json")

	// The regular-file check lives in ReadObservations' directory loop (observation.go:328), not
	// in readObservationFile, so a direct-path read has to repeat it. A size cap bounds how MUCH a
	// hostile file can make us read but not how LONG: opening a FIFO read-only blocks until a
	// writer appears, and this directory is agent-writable, so one `mkfifo <sid>.json` would wedge
	// every step boundary for that session. Lstat rather than Stat, so a symlink is refused rather
	// than followed.
	//
	// Stat-then-open is a check of one inode and a read of whatever the name points at by the time
	// readObservationFile opens it, and closing that window would mean opening non-blocking and
	// fstat-ing the descriptor ourselves — which means not going through the validating decoder,
	// the one thing G4 requires. The window is worth more than the alternative: losing it costs the
	// caller a decode of a file it should have refused, and losing the decoder costs every caller
	// the attribution and schema floor that make a reading trustworthy at all.
	fi, err := os.Lstat(path)
	if err != nil {
		// Absent is none, not malformed: this session has simply never rendered, or the factory
		// statusline gate is off. Splitting that from "something is here and we cannot trust it"
		// is the reason for the stat — readObservationFile collapses both into the same
		// unattributable result, which is all a directory sweep needs and less than a
		// session-keyed caller needs.
		return NoReading()
	}
	if !fi.Mode().IsRegular() {
		return MalformedReading()
	}

	obs, _, ok := readObservationFile(path, sid, opts)
	if !ok {
		// A file exists for this session and produced no usable datum: oversized, unparseable,
		// pre-occupancy schema, a partial occupancy block, or an agent name that is ungrammatical
		// or off the roster. Every one of those is malformed rather than none — something wrote
		// here and it could not be trusted, which is a different fact from nothing being here, and
		// the difference is what a caller needs to tell "not measured yet" from "measuring is
		// broken". This is a deliberate divergence from ReadObservations, which reaches the same
		// code and skips the file (observation.go:340-342) because a directory sweep cannot charge
		// an unattributable file to anybody.
		return MalformedReading()
	}
	return ObservedReading(obs, opts, now)
}
