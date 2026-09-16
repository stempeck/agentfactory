package cmd

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// This file is #673 RELEASE-MECH: the half of the sequential cap slot's lifecycle that decides when a
// release is JUSTIFIED. The claim half stays in dispatch_admit.go, where an O_EXCL create IS the fact
// it asserts; releasing has never had that property.
//
// The defect it closes: SubagentStop was treated as a verified completion event. On run wt-df5d8f a
// background child's SubagentStop fired ~7 minutes into a child that then ran ~2 hours, so the slot was
// freed mid-life and two children were concurrent for 1h20m+. The semaphore held per release-window,
// not per child lifetime. So the slot gains a verification state and SubagentStop is demoted to a
// proposal:
//
//	(absent) --O_EXCL claim--> CLAIMED
//	CLAIMED --SubagentStop writes sequential.stop--> STOP-PROPOSED
//	STOP-PROPOSED --admit: evidence says quiet >= subagentQuietReleaseSecs--> (absent) + new claim
//	STOP-PROPOSED --admit: evidence says activity--> STOP-PROPOSED (refuse: still "one running")
//	CLAIMED|STOP-PROPOSED --liveness-conditioned TTL | clearDispatchReservations--> (absent)  [backstops]
//
// Retire proposes; admit disposes. Disposal is LAZY — evaluated at the next claim attempt — so no
// daemon and no timer is introduced.
//
// Note the direction inversion this file introduces against its neighbour, because the two must coexist
// and a reader who harmonizes them reopens one defect or the other: on the CLAIM side every failure
// fails OPEN (admit), because a slot we cannot manage must never manufacture a false refusal. On the
// RELEASE side every "cannot tell" RETAINS (constraint C-4), because a release we cannot justify is how
// #673 happened. Claim-side errors admit; release-side ignorance holds.
//
// The house's PID-lock prior art (internal/lock, INV-7) is deliberately NOT used: platform sub-agents
// are in-process with no per-child PID (.analysis/673/rootcause_concern_11.md), which is precisely why
// the evidence is a transcript-mtime ladder rather than a liveness probe.
//
// Mixed-version rollout leaves one accepted residual, recorded rather than compensated for: an OLD
// dispatch-admit running against a NEW dispatch-retire counts the sequential.stop it does not recognise
// as an arithmetic marker, oversizing the next launch's reservation. The effect is over-refusal — the
// recoverable direction — and it clears at the next relaunch, when clearDispatchReservations wipes the
// ledger. Adding a compatibility shim for a window that closes on its own would cost more than the
// window does.

const (
	// sequentialStopName is the release PROPOSAL sidecar written beside sequential.slot. It lives INSIDE
	// the ledger dir so clearDispatchReservations' os.RemoveAll reaps it for free (#669 F5); a breadcrumb
	// placed outside the ledger would need its own reaper.
	sequentialStopName = "sequential.stop"

	// sequentialStatePrefix is what isCapStateFile matches. It is a PREFIX rather than the three literal
	// names because the state family is open-ended: sequential.slot, sequential.stop, the
	// sequential.slot.reclaim-<pid>-<nanotime> corpse, and the sequential.stop.<rand>.tmp that
	// fsutil.WriteFileAtomic materializes in this same directory for the duration of a write. A literal
	// list silently misses that last one. Arithmetic markers are always <pid>-<nanotime>
	// (writeReservationMarker), so the prefix can never swallow one.
	sequentialStatePrefix = "sequential."

	// sequentialStopVersion is stamped by the WRITER, never by a caller — the writeModelCoverageRecord
	// shape (config_models.go:828), the tree's only versioned-JSON precedent.
	sequentialStopVersion = 1

	// sequentialReclaimName is the O_EXCL mutex serializing slot reclaims (see acquireReclaimGuard). It
	// carries the sequential. prefix so both ledger sweeps are blind to it for free — a guard counted as
	// an arithmetic marker would be swept by the TTL reaper mid-reclaim.
	sequentialReclaimName = "sequential.reclaim"
)

// sequentialReclaimGuardTTL is how long a reclaim guard may stand before it is read as a crashed
// process rather than a working one. The guard covers a read, a rename, two removes and a create — a
// handful of syscalls, never the evidence ladder — so a minute is three orders of magnitude of slack.
// It exists only so a crash cannot wedge the cap; it is not a tuning knob for contention.
const sequentialReclaimGuardTTL = time.Minute

// quietForever is E2's verdict when the child's transcript no longer exists: an artifact that is gone
// cannot still be written to. It is a duration rather than a separate boolean so every leg of the
// ladder returns the same shape and the threshold comparison stays in one place.
const quietForever = time.Duration(math.MaxInt64)

// isCapStateFile reports whether a ledger entry belongs to the hard cap's slot machinery rather than
// being an arithmetic reservation marker. Both ledger sweeps — countLiveReservations on the admit side
// and retireOneReservation's FIFO selection on the retire side — read THIS predicate, for the reason
// isSubagentTool exists (subagent_tool.go:10): the defect that motivated that one (#669 BROKEN-0) was
// two sites drifting onto the same wrong literal, and here the drift is worse than a miscount. An
// unrecognised sequential.stop is not merely counted, it is deleted — by countLiveReservations' TTL
// sweep or by retire's FIFO pick — which destroys the proposal and silently restores #673.
//
// The prefix is deliberately wider than the three names it recognises, and NARROWING it to those names
// would be a regression, not a tidy-up: a crash mid-reclaim can leave a .reclaim-* corpse, a .stale-*
// corpse or WriteFileAtomic's sequential.stop.<rand>.tmp sibling behind, and every one of those must be
// invisible to both sweeps for the same reason the proposal is. The cost is that such litter is reaped
// only by clearDispatchReservations, which is the direction that cannot break the cap.
func isCapStateFile(name string) bool {
	return strings.HasPrefix(name, sequentialStatePrefix)
}

// slotReleaseProposal is the sequential.stop sidecar: a claim that the child holding the slot MAY have
// finished, plus the evidence hints needed to check.
//
// SlotStamp is the anti-replay join and is an OPAQUE byte-echo of sequential.slot's content — never
// parsed. A proposal releases only the slot whose content it echoes, so a previous child's stop cannot
// free the current child's slot. Keeping it opaque is also what makes the join survive a mixed-version
// rollout, where an older binary's slot holds a bare RFC3339Nano rather than today's pid+nanotime shape.
//
// SessionID and TranscriptPath are evidence HINTS, never trust anchors (design-doc.md:87): they steer
// which rung of the ladder can run, and every one of them failing only means "cannot tell" — retain.
type slotReleaseProposal struct {
	V              int    `json:"v"`
	SlotStamp      string `json:"slot_stamp"`
	StopAt         string `json:"stop_at"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

// proposeSlotRelease records that a stop event arrived for the child holding slotPath. It is what
// replaced the os.Remove that #673 is about.
//
// The slot file itself is NEVER rewritten: its mtime is the authority dispatchReservationSafetyTTL
// reads, and refreshing it here would extend the crash backstop every time a child stopped — the
// rejected "A-b" shape. Every failure leg is a silent no-op that leaves the slot held, which is the
// fail-safe direction: an unwritten proposal costs one child's turnaround, a wrongly-written one costs
// the guarantee.
func proposeSlotRelease(slotPath, sessionID, transcriptPath string, now time.Time) {
	stamp, err := os.ReadFile(slotPath)
	if err != nil {
		return
	}
	data, err := json.MarshalIndent(slotReleaseProposal{
		V:              sequentialStopVersion,
		SlotStamp:      string(stamp),
		StopAt:         now.UTC().Format(time.RFC3339Nano),
		SessionID:      sessionID,
		TranscriptPath: transcriptPath,
	}, "", "  ")
	if err != nil {
		return
	}
	// INV-6: a file whose PRESENCE triggers a downstream effect — here admit's disposal — must be
	// written atomically. WriteFileAtomic stages a sibling sequential.stop.<rand>.tmp inside this dir;
	// isCapStateFile's prefix shape is what keeps both sweeps blind to it.
	_ = fsutil.WriteFileAtomic(filepath.Join(filepath.Dir(slotPath), sequentialStopName), data, 0o644)
}

// readSlotProposal reads the sidecar. Absent, unreadable, corrupt, an unrecognised version, or missing
// its join are all the SAME answer — "nothing was proposed that this process can act on" — which is
// readModelCoverageRecord's shape (config_models.go:805) pointed in the retain direction.
func readSlotProposal(dir string) (slotReleaseProposal, bool) {
	raw, err := os.ReadFile(filepath.Join(dir, sequentialStopName))
	if err != nil {
		return slotReleaseProposal{}, false
	}
	var p slotReleaseProposal
	if err := json.Unmarshal(raw, &p); err != nil {
		return slotReleaseProposal{}, false
	}
	if p.V != sequentialStopVersion || p.SlotStamp == "" {
		return slotReleaseProposal{}, false
	}
	return p, true
}

// slotReleasable reports whether the slot in dir may be released: iff the proposal beside it echoes
// held — the content of the claim its caller observed — AND the evidence ladder agrees the child
// finished. Every other answer retains.
//
// held is passed in rather than re-read here so that the whole decision rests on ONE observation of one
// claim. Re-reading would let this function join a proposal against a claim the caller never saw, and
// the caller would then act on a verdict about a slot that is no longer there.
//
// This is consulted on the admit path AFTER claimSubagentSlot's fail-open leg, never before — see the
// ordering note there (#669 N1).
func slotReleasable(dir, held string, now time.Time) bool {
	return slotReleaseRungOf(dir, held, now) != rungRetain
}

// slotReleaseRungOf returns which rung of the ladder justifies releasing dir's slot, or rungRetain.
// slotReleasable stays the bare predicate its callers use; the reclaim site reads the rung to write the
// audit breadcrumb, and returning it rather than writing the audit here keeps this function pure, which
// its tests depend on.
func slotReleaseRungOf(dir, held string, now time.Time) slotReleaseRung {
	prop, ok := readSlotProposal(dir)
	if !ok {
		return rungRetain
	}
	if prop.SlotStamp != held {
		// A proposal describing a DIFFERENT claim: a stale stop from a previous child must never free
		// the current child's slot.
		return rungRetain
	}
	// The completion-record rung sits below the stamp guard and above the quiet compare: a matching record
	// in the parent transcript frees a finished child now instead of after the timer. Every failure inside
	// it falls through unchanged to E1/E2/E3.
	if slotCompletionRecorded(dir, held, prop.TranscriptPath, now) {
		return rungCompletion
	}
	if quiet, measured := subagentQuietEvidence(workDirFromReservationDir(dir), prop.SessionID, prop.TranscriptPath, now); measured {
		if quiet >= subagentQuietReleaseSecs {
			return rungQuiet
		}
		return rungRetain
	}
	// E3 (degraded) — no evidence path the ladder could measure, so fall back to how long the proposal
	// itself has been dwelling. This is a timer, and a timer owning release is exactly what [BAD-1] was
	// (a 5-minute sweep retiring a live child). It is acceptable here only because it is LAST in the
	// ladder, sized from a measured silence distribution rather than an assumed launch cadence, and
	// biased toward over-refusing. If it ever migrates ahead of E1/E2, [BAD-1] returns under a new name.
	stopAt, err := time.Parse(time.RFC3339Nano, prop.StopAt)
	if err != nil {
		return rungRetain
	}
	if sinceNotBefore(now, stopAt) >= subagentQuietReleaseSecs {
		return rungDwell
	}
	return rungRetain
}

// slotEvidenceLive reports whether the child holding dir's slot is DEMONSTRABLY still working. It is
// the liveness condition on dispatchReservationSafetyTTL (Gap 5), and its default is the opposite of
// slotReleasable's on purpose: "cannot tell" here means NOT live, so the 2h crash backstop reclaims
// exactly as it did before this change and AC-D4-4's no-wedge direction is preserved. Only positive
// evidence of recent writes buys a child time past the backstop.
//
// Inverting either default is silent: making this one retain wedges the cap forever on a crashed child,
// and making slotReleasable release reopens #673.
func slotEvidenceLive(dir string, now time.Time) bool {
	var sessionID, transcriptPath string
	if prop, ok := readSlotProposal(dir); ok {
		sessionID, transcriptPath = prop.SessionID, prop.TranscriptPath
	}
	quiet, measured := subagentQuietEvidence(workDirFromReservationDir(dir), sessionID, transcriptPath, now)
	return measured && quiet < subagentQuietReleaseSecs
}

// subagentQuietEvidence is the ADR-009 seam fronting the whole E0-E3 evidence ladder, and the single
// point AC-D4-5's lifecycle test drives to walk a slot from claimed through running to released.
//
// It is a var rather than a plain function — a stretch of ADR-009's "shells out to an external binary"
// scope that earns itself the way the ADR asks (L56: not a license for arbitrary globals). What sits
// behind it is a PLATFORM-OWNED, version-drifting artifact tree: the host's own sub-agent transcripts,
// whose LAYOUT is the host's to change. A test can build that tree — TestSubagentQuietEvidence_LadderOrder
// does exactly that, in-process via CLAUDE_CONFIG_DIR, and that test is what pins the rungs. What a test
// cannot do is drive a slot's whole lifecycle THROUGH it without also asserting the host's layout, which
// would make every timeline test a hostage to a shape we do not own. The seam is that separation.
// Tests MUST restore the original via t.Cleanup — the suite is one binary and a leaked seam silently
// rewrites every later dispatch verdict.
//
// The ladder, in order, with every failure meaning "cannot tell" (false) rather than "quiet":
//
//	E0 — background_tasks[] "no live subagent entry". NAMED BUT DARK, deliberately not wired here.
//	     The field has zero observations anywhere in this tree; it is documented-only. Promoting a
//	     documented-only platform claim to a correctness authority is precisely the epistemic error
//	     that produced #673, so it stays behind PAYLOAD-CAPTURE's observation gate.
//	E1 — preferred, alias-proof: the launcher session's whole subagents dir. SET-LEVEL, so a
//	     SubagentStop belonging to a different, shorter child cannot fool it.
//	E2 — fallback: the single recorded transcript_path.
//	E3 — degraded proposal-age dwell, which lives in slotReleasable rather than here because it needs
//	     no platform artifact at all.
//
// All access HERE is stat/glob only — no evidence file the E0-E3 ladder consults is ever OPENED. The one
// bounded content read lives outside this seam, in slotReleaseRungOf's completion-record rung: a
// regular-file-gated tail read of the parent transcript, ranked ahead of this quiet compare. Stat/glob-only
// here keeps this cheap enough to run on every claim.
var subagentQuietEvidence = func(workDir, sessionID, transcriptPath string, now time.Time) (time.Duration, bool) {
	if quiet, ok := subagentSidechainQuiet(workDir, sessionID, now); ok {
		return quiet, true
	}
	return subagentTranscriptQuiet(transcriptPath, now)
}

// subagentSidechainQuiet is E1: how long the QUIETEST-recent moment of a session's whole sub-agent
// sidechain was, measured as the newest agent-*.jsonl mtime under it.
//
// Zero matches is "cannot tell", NOT "quiet". The distinction is load-bearing: filepath.Glob returns
// (nil, nil) for a directory that does not exist, so treating an empty match set as silence would make
// this rung trivially true whenever the path derivation is wrong — an under-refuse, the same unsafe
// direction as the defect this file closes.
//
// agent-*.jsonl rather than *, for subagentSpend's reason (subagent_occupancy.go:64): the host writes
// .meta.json sidecars into this same directory.
func subagentSidechainQuiet(workDir, sessionID string, now time.Time) (time.Duration, bool) {
	dir := sessionSubagentDir(workDir, sessionID)
	if dir == "" {
		return 0, false
	}
	paths, err := filepath.Glob(filepath.Join(dir, "agent-*.jsonl"))
	if err != nil || len(paths) == 0 {
		return 0, false
	}
	var newest time.Time
	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().After(newest) {
			newest = info.ModTime()
		}
	}
	if newest.IsZero() {
		return 0, false
	}
	return sinceNotBefore(now, newest), true
}

// subagentTranscriptQuiet is E2: the single-transcript fallback for when no session dir is derivable.
// It stats the PARENT session transcript (the stop payload's transcript_path), NOT the child's
// agent_transcript_path sidechain. A background child keeps writing that sidechain after SubagentStop
// fires, so the parent transcript is a strictly weaker liveness signal — which is why E1 (the child
// sidechain, keyed off session_id) outranks it and E2 is reached only when session_id is absent. This
// is ground-truthed by the fixture-replay assertion in dispatch_deny_fixture_replay_test.go
// ("transcript_path is the parent session transcript, not the child's").
//
// The vanished-file leg is the ONE place in this file that reports in the RELEASE direction on an
// absence (Risk R-2). It is sound because the path is the host's own, taken verbatim from the stop
// payload rather than derived by us — so "gone" means the host removed it, not that we looked in the
// wrong place. Measured retention is ~30 days, some 2000x the release threshold, so a live child's
// transcript vanishing underneath it is not a case that occurs.
//
// A non-regular path is refused outright, statusline_tokens.go:51's posture for this same semi-trusted
// field. transcript_path is a hint, and a directory's or FIFO's mtime is not a measurement of a child:
// an idle directory reads as arbitrarily quiet, and this rung answers in the RELEASE direction and
// outranks the E3 dwell, so a wrong answer here spends the guarantee rather than a turnaround.
func subagentTranscriptQuiet(transcriptPath string, now time.Time) (time.Duration, bool) {
	if transcriptPath == "" {
		return 0, false
	}
	info, err := os.Stat(transcriptPath)
	if os.IsNotExist(err) {
		return quietForever, true
	}
	if err != nil || !info.Mode().IsRegular() {
		return 0, false
	}
	return sinceNotBefore(now, info.ModTime()), true
}

// sinceNotBefore is now-then clamped at zero. A negative elapsed time means the clock moved backwards
// or a host wrote a future mtime; reporting it verbatim would compare below every threshold and read as
// "very recently active", which is the retain direction on both consult paths — but clamping keeps the
// two call sites reasoning about one monotonic quantity instead of a signed one.
func sinceNotBefore(now, then time.Time) time.Duration {
	if d := now.Sub(then); d > 0 {
		return d
	}
	return 0
}

// workDirFromReservationDir inverts reservationDir (dispatch_admit.go:473):
// <workDir>/.runtime/dispatch_admit_reservations/<hash> -> <workDir>.
//
// It exists so the layout coupling has ONE home and can be round-trip tested against reservationDir,
// rather than being spelled inline at a call site where a silent off-by-one-level would degrade every
// child to E3 forever — a failure that looks like nothing at all, because over-refusing is invisible
// until someone measures turnaround.
//
// The alternative — widening claimSubagentSlot to carry workDir — was rejected: it is pinned by nine
// call sites, and the information is already present in the argument it does take.
func workDirFromReservationDir(dir string) string {
	return filepath.Dir(filepath.Dir(filepath.Dir(dir)))
}
