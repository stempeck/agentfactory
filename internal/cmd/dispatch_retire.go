package cmd

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/fsutil"
)

// af dispatch-retire is #672's reservation-retirement leg (#669 THREAD-1): the SubagentStop hook that
// retires a sub-agent's hold on the ledger. It is the mirror of the marker WRITER af dispatch-admit —
// the two verbs bracket the same ledger, one holding a reservation at launch and this one giving it
// back. It is wired to SubagentStop rather than PostToolUse because a background Task's PostToolUse
// fires at dispatch-return (decision-gating-verification.md), which would drop a marker seconds after
// it was written.
//
// SubagentStop is NOT a verified completion event. #673 measured it firing ~7 minutes into a child that
// then ran ~2 hours, so the two ledger legs treat it differently:
//
//   - the arithmetic FIFO leg retires one marker per stop, unchanged. A marker is a soft accounting
//     hold, its early return only weakens the ledger toward admitting, and gating it on set-level quiet
//     would degrade pooled turnover to TTL-only as the common case (cross-review H-4). The residual
//     early-retirement exposure there is a named one (Gap 6), not an oversight.
//   - the sequential CAP slot is a safety semaphore, where an early return means two children run at
//     once. So this verb only PROPOSES its release, by writing sequential.stop beside the slot; the next
//     claim disposes of it once the evidence ladder agrees the child went quiet. See dispatch_release.go.
//
// Like every completion hook it exits 0 and emits nothing (ADR-007): a hook that blocked here would
// stall the parent session on a child's teardown.
var dispatchRetireCmd = &cobra.Command{
	Use:   "dispatch-retire",
	Short: "Retire a sub-agent's ledger hold on the SubagentStop event (dispatch-admit's mirror).",
	Long: `Dispatch-retire intercepts Claude Code's SubagentStop hook — the event that fires when a
sub-agent stops responding, which for a background child is not the same thing as finishing. For the
arithmetic ledger it removes exactly one dispatch-admit reservation marker (the oldest by mtime, FIFO)
for the launcher whose cwd the payload carries, so the child stops counting against its backend's shared
pool. For the sequential hard-cap slot it removes nothing: it writes a sequential.stop proposal beside
the slot, and the next launch releases the slot only once evidence shows the child's sidechain went
quiet. The SubagentStop payload names no backend, so it sweeps every backend subdir under the launcher's
ledger. It exits 0 and emits nothing; a lost retirement only weakens the ledger toward admitting, never
toward a false refusal.`,
	RunE: runDispatchRetireCmd,
}

func init() {
	rootCmd.AddCommand(dispatchRetireCmd)
}

// dispatchRetirePayload is the subset of the SubagentStop hook JSON this command reads. It reads no
// backend/model field because the event carries none — retirement sweeps every backend subdir instead.
//
// SessionID and TranscriptPath are #673's evidence HINTS, never trust anchors: they are copied into the
// sequential.stop proposal so the release ladder knows where to look for signs the child is still
// working. Both absent simply degrades the ladder to its proposal-age rung, which over-refuses; nothing
// downstream trusts either field to be present or correct.
type dispatchRetirePayload struct {
	Cwd            string `json:"cwd"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

func runDispatchRetireCmd(cmd *cobra.Command, _ []string) error {
	p, raw, ok := readDispatchRetirePayloadFromStdin()
	if !ok {
		return nil
	}
	if p.Cwd == "" {
		if wd, err := getWd(); err == nil {
			p.Cwd = wd
		}
	}
	if p.Cwd == "" {
		return nil
	}
	captureDispatchStopPayload(p.Cwd, raw)
	retireOneReservation(p, "", time.Now())
	return nil
}

// readDispatchRetirePayloadFromStdin returns the decoded payload AND the raw bytes it decoded from.
// The raw copy is what PAYLOAD-CAPTURE observes: the whole point of that instrument is the fields this
// struct does NOT model, so re-encoding the struct would capture only what we already believed.
//
// The decode is STREAMING and unbounded, and the size limit sits on the capture alone. Bounding the
// decode instead — a LimitReader feeding one Unmarshal — makes an oversized payload truncate into a
// parse error, and this verb's answer to a parse error is to retire NOTHING: no marker removed, no
// sequential.stop written, the cap slot held for its full 2h backstop. last_assistant_message is
// unbounded prose riding on the same object, so that is a reachable input, and an observation
// instrument that can cost a retirement is worse than no instrument at all. The bound may degrade
// the capture; it may never degrade the retirement.
func readDispatchRetirePayloadFromStdin() (dispatchRetirePayload, []byte, bool) {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return dispatchRetirePayload{}, nil, false
	}
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return dispatchRetirePayload{}, nil, false
	}
	var capture boundedCapture
	dec := json.NewDecoder(io.TeeReader(os.Stdin, &capture))
	var p dispatchRetirePayload
	if err := dec.Decode(&p); err != nil {
		return dispatchRetirePayload{}, nil, false
	}
	return p, capture.decoded(dec.InputOffset()), true
}

// boundedCapture keeps the bytes a json.Decoder consumed, up to a cap, so the raw payload can be
// observed without the decoder being answerable to the cap. Past the limit it drops what it holds
// rather than keeping a prefix: half a payload is not an observation, and holding a growing buffer is
// the resource question the limit exists to answer.
type boundedCapture struct {
	buf      []byte
	overflow bool
}

func (c *boundedCapture) Write(p []byte) (int, error) {
	if !c.overflow {
		if len(c.buf)+len(p) > dispatchStopPayloadReadLimit {
			c.overflow = true
			c.buf = nil
		} else {
			c.buf = append(c.buf, p...)
		}
	}
	return len(p), nil
}

// decoded returns just the bytes of the value the decoder actually consumed. A Decoder reads ahead, so
// the tee also holds whatever followed — trailing NDJSON, say — and handing that to the capture's own
// Unmarshal would fail on input the retirement itself accepted.
func (c *boundedCapture) decoded(offset int64) []byte {
	if c.overflow || offset < 0 || offset > int64(len(c.buf)) {
		return nil
	}
	return c.buf[:offset]
}

// retireOneReservation gives back one sub-agent's hold on the ledger. THREAD-1's done-when (b): one
// stop event retires one marker (FIFO per launcher backend). When backendKey names a backend it retires
// from that backend's subdir; when it is "" (the SubagentStop payload identifies no backend, per pin #2)
// it sweeps every backend subdir under the launcher's ledger and acts on the single globally-oldest
// marker. mtime is the authority the admit sweep (countLiveReservations) already trusts, so the two
// reapers agree on which marker is oldest.
//
// The two legs differ as of #673, and the difference is the whole point: an arithmetic marker is
// REMOVED, while the cap slot is only PROPOSED for release. See the command doc above for why.
//
// Every path is a fail-safe no-op: a missing dir, an empty ledger, or a delete race returns silently. A
// lost retirement only weakens the ledger toward admitting — the recoverable false-refusal direction.
func retireOneReservation(p dispatchRetirePayload, backendKey string, now time.Time) {
	workDir := p.Cwd
	var dirs []string
	if backendKey != "" {
		dirs = []string{reservationDir(workDir, backendKey)}
	} else {
		root := filepath.Join(workDir, ".runtime", "dispatch_admit_reservations")
		entries, err := os.ReadDir(root)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() {
				dirs = append(dirs, filepath.Join(root, e.Name()))
			}
		}
	}

	var oldestPath, slotPath string
	var oldestMod time.Time
	for _, dir := range dirs {
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, e := range entries {
			if e.IsDir() {
				continue
			}
			// isCapStateFile rather than a bare sequentialSlotName test: this loop selects a DELETION
			// target, so an unrecognised sequential.stop proposal (or a transient .reclaim-* corpse, or
			// WriteFileAtomic's staging file) would not merely be miscategorised — it would be picked as
			// the oldest marker and destroyed, silently restoring #673. The admit sweep reads the same
			// predicate; the two must agree or the cap breaks in a way no single site looks wrong.
			if isCapStateFile(e.Name()) {
				if e.Name() == sequentialSlotName {
					slotPath = filepath.Join(dir, e.Name())
				}
				continue
			}
			info, err := e.Info()
			if err != nil {
				continue
			}
			if oldestPath == "" || info.ModTime().Before(oldestMod) {
				oldestPath = filepath.Join(dir, e.Name())
				oldestMod = info.ModTime()
			}
		}
	}
	// A child that held the sequential cap slot must resolve THAT first (#669 F5): a leaked older
	// arithmetic marker would otherwise steal this retirement under pure mtime FIFO and leave the slot
	// with no proposal at all, wedged to its 2h TTL and blocking the next sequential launch. Absent a
	// slot the oldest marker retires exactly as before, so the two reapers still agree on which marker
	// is oldest for the non-slot case.
	//
	// Where that target is the slot, #673 inverts what happens to it: PROPOSE, never remove. The slot
	// file is left exactly as it is — untouched mtime and all, since that mtime is the TTL's authority.
	//
	// One accepted consequence of the retain: at HEAD the first stop deleted the slot, so later stops fell
	// through to the FIFO leg, whereas now the slot legitimately persists for the child's whole lifetime
	// and preempts every retirement for that launcher. An arithmetic marker in a SIBLING backend dir under
	// the same launcher therefore waits for the TTL reaper instead of a stop event. That is the
	// over-refuse direction, it needs one launcher holding two backends' ledgers at once, and the marker
	// is a soft accounting hold — so it is accepted here rather than worked around by retiring both.
	if slotPath != "" {
		proposeSlotRelease(slotPath, p.SessionID, p.TranscriptPath, now)
		return
	}
	if oldestPath != "" {
		_ = os.Remove(oldestPath)
	}
}

const (
	// dispatchStopPayloadReadLimit bounds what the CAPTURE retains, not what the decoder may read. The
	// hook payload is a small identity/path object plus one free-text field; a copy of anything beyond
	// this is not an observation worth holding in memory. See readDispatchRetirePayloadFromStdin for why
	// the retirement is deliberately not subject to it.
	dispatchStopPayloadReadLimit = 1 << 20

	// dispatchStopPayloadMaxBytes bounds what is WRITTEN. Past it the capture is dropped rather than
	// truncated: half a JSON object is not an observation, and the instrument must never be the reason
	// a hook did something surprising.
	dispatchStopPayloadMaxBytes = 64 << 10

	// dispatchStopPayloadFreeText is the one field stripped before capture — an unbounded assistant
	// message whose evidence value here is zero. Everything PAYLOAD-CAPTURE exists to observe is in the
	// identity, path and background_tasks fields.
	dispatchStopPayloadFreeText = "last_assistant_message"
)

// captureDispatchStopPayload writes the latest SubagentStop payload to a fixed-name overwrite file so
// the platform's actual hook contract becomes a greppable runtime fact (#673 PAYLOAD-CAPTURE).
//
// This exists because #673 was an EPISTEMIC failure, not a coding one: the claim "SubagentStop means
// the child finished" was adopted from documentation and never observed, and the two fields the release
// ladder now leans on (session_id, transcript_path) plus the one it deliberately does NOT use
// (background_tasks[]) carry that same documented-only status. Nothing may promote background_tasks to
// a live evidence rung until this instrument has shown what the host actually sends. It is also the
// standing tripwire that turns any future early-fire into a file you can look at rather than a
// transcript-forensics project.
//
// It lives OUTSIDE the reservation ledger deliberately: it is an observation of the platform, not slot
// state, and it must survive the clearDispatchReservations sweeps that reset a session's holds.
//
// Every failure is a silent no-op. This is a hook that must exit 0 and emit nothing (ADR-007), and an
// observation instrument that could fail a retirement would be worse than no instrument.
func captureDispatchStopPayload(workDir string, raw []byte) {
	var payload map[string]any
	if err := json.Unmarshal(raw, &payload); err != nil {
		return
	}
	delete(payload, dispatchStopPayloadFreeText)
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil || len(data) > dispatchStopPayloadMaxBytes {
		return
	}
	dir := filepath.Join(workDir, ".runtime")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return
	}
	_ = fsutil.WriteFileAtomic(filepath.Join(dir, "dispatch_stop_payload.json"), data, 0o644)
}
