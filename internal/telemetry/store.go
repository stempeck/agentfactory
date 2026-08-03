package telemetry

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
)

const (
	stepsSubdir    = "steps"
	rotatedSuffix  = ".1"
	cursorPrefix   = ".cursor-"
	recordFileExt  = ".jsonl"
	maxRecordBytes = 1 << 20
)

// rotateAtBytes is the size at which the live log is rotated when doing so is safe.
// hardCapBytes is the ceiling past which rotation happens even when it is not safe, because
// refusing forever would turn a stalled exporter into unbounded disk growth. Both are vars so
// the rotation contract can be exercised without writing tens of megabytes; neither is
// configurable, because an operator has no way to reason about the right value and the export
// cursor, not the cap, is what actually protects the data.
var (
	rotateAtBytes int64 = 10 << 20
	hardCapBytes  int64 = 40 << 20
)

// ErrNoRecords reports that there is nothing a trace export can carry: either no records at
// all, or none that describes a step ending. It is not a failure — a factory whose steps are
// all already exported, and one whose current step is still running, both land here.
var ErrNoRecords = errors.New("telemetry: no exportable step records")

// Filter narrows a read. The zero value reads everything for the named agent, which is what a
// local report wants; the exporter adds Unexported and Limit to get a bounded drain.
type Filter struct {
	Agent      string
	InstanceID string
	Unexported bool
	Limit      int
}

// ReadStats carries the two counts that must never be silently zero. Malformed is per-read:
// lines that could not be parsed and were skipped. Dropped is cumulative and durable: records
// that left the readable set because a hard ceiling forced a rotation. Both exist so that a
// hole in the data is reportable rather than invisible.
type ReadStats struct {
	Malformed int
	Dropped   int

	// DroppedUnexported is the subset of Dropped that no backend ever received. Dropped
	// records that the cursor had already passed are retention working as designed; these
	// are the ones that represent real loss.
	DroppedUnexported int
}

// StoreStats is what a status surface reads: the counts plus how far behind the exporter is.
type StoreStats struct {
	ReadStats
	Backlog int
}

// cursorState is the export bookmark plus the loss counters, written as one atomic value.
//
// The bookmark is the DIGEST of the last exported record, never a byte offset or a record
// index. Rotation renames the live file out from under any positional bookmark and silently
// re-bases it to the start, which would offer already-exported records again or — far worse,
// depending on which direction the code rounds — skip records that were never sent. A digest
// is invariant under rotation because it is a property of the record, not of where it sits.
type cursorState struct {
	LastExportedDigest string `json:"last_exported_digest"`
	ExportedTotal      int64  `json:"exported_total"`
	DroppedTotal       int64  `json:"dropped_total"`
	DroppedUnexported  int64  `json:"dropped_unexported_total"`
	UpdatedAt          string `json:"updated_at"`
}

func stepsPath(telemetryDir, agent string) string {
	return filepath.Join(telemetryDir, stepsSubdir, agent+recordFileExt)
}

func cursorPath(telemetryDir, agent string) string {
	return filepath.Join(telemetryDir, cursorPrefix+agent)
}

// recordDigest identifies a record by its canonical serialized form rather than by its raw
// line, so reformatting whitespace in a log cannot desynchronise a cursor.
func recordDigest(ev StepEvent) string {
	raw, err := json.Marshal(ev)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:16])
}

// AppendEvent writes one record to the agent's log at the factory root and rotates if the log
// has outgrown its cap.
//
// The file handling copies the repository's existing append idiom, with one deliberate
// divergence: the existing sites swallow every error because nothing ever reads those files.
// This log IS read back — by the exporter, by a local report — so a write that failed and
// said nothing would be indistinguishable from a step that never ran. The error is returned.
// Callers on a hook path are free to ignore it, and that decision belongs to them.
func AppendEvent(telemetryDir string, ev StepEvent) error {
	// The record path is built from a field of the record. Without this check an agent name
	// carrying path separators would place the log anywhere the process can write, including
	// outside the factory root entirely.
	if err := config.ValidateAgentName(ev.Agent); err != nil {
		return fmt.Errorf("telemetry: refusing to write a record for an invalid agent name: %w", err)
	}
	path := stepsPath(telemetryDir, ev.Agent)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("telemetry: creating records dir: %w", err)
	}
	line, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("telemetry: encoding record: %w", err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("telemetry: opening records file: %w", err)
	}
	if _, err := fmt.Fprintf(f, "%s\n", line); err != nil {
		f.Close()
		return fmt.Errorf("telemetry: appending record: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("telemetry: closing records file: %w", err)
	}
	return maybeRotate(telemetryDir, ev.Agent)
}

// maybeRotate enforces the rule the whole storage design turns on: a rotation must not
// destroy a record the exporter has not yet sent.
//
// One generation is kept, so rotating means the existing <agent>.jsonl.1 is overwritten. If
// that file still holds unexported records, the rename would take them with it and nothing
// downstream would ever know they existed. So the rename happens only when the kept
// generation is fully exported; otherwise the live log is allowed to grow past its cap, which
// is the correct trade — disk is recoverable, a missing record is not.
//
// That cannot be unconditional, or a permanently unreachable backend would fill the disk. At
// hardCapBytes the rename proceeds regardless, and the number of records it destroys is
// counted BEFORE the rename and persisted, so the loss is reported rather than silent.
func maybeRotate(telemetryDir, agent string) error {
	live := stepsPath(telemetryDir, agent)
	info, err := os.Stat(live)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("telemetry: sizing records file: %w", err)
	}
	if info.Size() <= rotateAtBytes {
		return nil
	}

	kept := live + rotatedSuffix
	keptRecords, _, err := parseRecordFile(kept)
	if err != nil {
		return err
	}

	if len(keptRecords) > 0 {
		state, err := readCursor(telemetryDir, agent)
		if err != nil {
			return err
		}
		// Records the kept generation holds that the cursor HAS passed are discarded without
		// ceremony — that is retention working as intended, and counting it as loss would bury
		// the real losses in noise.
		if unexported := countUnexportedInKept(telemetryDir, agent, keptRecords, state); unexported > 0 {
			if info.Size() < hardCapBytes {
				// Refuse. The cap is a target, the cursor is the contract.
				return nil
			}
			state.DroppedTotal += int64(len(keptRecords))
			state.DroppedUnexported += int64(unexported)
			if err := writeCursor(telemetryDir, agent, state); err != nil {
				return err
			}
		}
	}

	if err := os.Rename(live, kept); err != nil {
		return fmt.Errorf("telemetry: rotating records file: %w", err)
	}
	return nil
}

// countUnexportedInKept answers the rotation question, which is NOT the question
// countUnexported answers. countUnexported is asked about the whole record span, where a
// bookmark it cannot find can only be older than everything present, so "treat these as
// unexported" is the safe reading. Rotation asks about the kept generation ALONE — a strict
// PREFIX of that span — and there the same absence has a second possible cause: the bookmark
// may be NEWER than everything here, sitting in the live log, which is precisely what a
// healthy exporter that is keeping up produces on every single append.
//
// Reading the prefix with the whole-span rule is what made rotateAtBytes dead in normal
// operation and booked a fully-delivered generation as permanent loss. So the span is
// reconstructed here and the bookmark located within it: an index at or past the end of the
// kept records means kept is fully exported. Ordering is what makes this sound — rotation
// renames live to kept and records only ever append, so every kept record precedes every live
// one.
//
// countUnexported itself is deliberately left untouched. Its other two callers pass the full
// span, and changing its shared semantics to suit this one caller would make ReadEvents return
// an empty pending set and Stats report a zero backlog — an exporter that has silently stopped,
// which is worse than the accounting bug being fixed here. Scoping the change to this function
// makes their behaviour unchanged by construction rather than by test.
func countUnexportedInKept(telemetryDir, agent string, keptRecords []StepEvent, state cursorState) int {
	if len(keptRecords) == 0 {
		return 0
	}
	if state.LastExportedDigest == "" {
		// Nothing has ever been exported, so nothing here can have been.
		return len(keptRecords)
	}
	// If the bookmark is inside the kept generation, the whole-span rule already gives the
	// right answer for this prefix.
	for i, r := range keptRecords {
		if recordDigest(r) == state.LastExportedDigest {
			return len(keptRecords) - (i + 1)
		}
	}
	// Not in kept. Look in live: finding it there means the exporter has passed every kept
	// record, so the kept generation is fully exported and rotation is safe.
	liveRecords, _, err := parseRecordFile(stepsPath(telemetryDir, agent))
	if err != nil {
		// Unreadable live log: fall back to the conservative whole-span answer rather than
		// authorise a rotation on a guess.
		return len(keptRecords)
	}
	for _, r := range liveRecords {
		if recordDigest(r) == state.LastExportedDigest {
			return 0
		}
	}
	// In neither generation: the bookmarked record was itself discarded by an earlier forced
	// rotation, so these are genuinely at risk. Same answer as the whole-span rule.
	return len(keptRecords)
}

func countUnexported(records []StepEvent, state cursorState) int {
	if state.LastExportedDigest == "" {
		return len(records)
	}
	for i, r := range records {
		if recordDigest(r) == state.LastExportedDigest {
			return len(records) - (i + 1)
		}
	}
	// The bookmarked record is not in this file. Either it was already discarded — in which
	// case everything before it went with it and everything here is newer — or the file was
	// edited. Both point the same way: treat these as unexported. Re-sending a record is
	// harmless because span identities are deterministic; skipping one is not recoverable.
	return len(records)
}

// ReadEvents returns the agent's records oldest-first, spanning the rotated generation and the
// live log.
//
// A line that will not parse is skipped and counted, never fatal: one corrupt line at the tail
// of a log — the shape a crash mid-write leaves behind — must not make every earlier record
// unreadable. The counts come back with the records so a caller can surface them.
func ReadEvents(telemetryDir string, filter Filter) ([]StepEvent, ReadStats, error) {
	var stats ReadStats
	if filter.Agent == "" {
		return nil, stats, errors.New("telemetry: reading records requires an agent name")
	}

	live := stepsPath(telemetryDir, filter.Agent)
	records, malformed, err := parseRecordFile(live + rotatedSuffix)
	if err != nil {
		return nil, stats, err
	}
	stats.Malformed += malformed

	liveRecords, liveMalformed, err := parseRecordFile(live)
	if err != nil {
		return nil, stats, err
	}
	stats.Malformed += liveMalformed
	records = append(records, liveRecords...)

	state, err := readCursor(telemetryDir, filter.Agent)
	if err != nil {
		return nil, stats, err
	}
	stats.Dropped = int(state.DroppedTotal)
	stats.DroppedUnexported = int(state.DroppedUnexported)

	if filter.Unexported {
		records = records[len(records)-countUnexported(records, state):]
	}
	if filter.InstanceID != "" {
		kept := records[:0:0]
		for _, r := range records {
			if r.InstanceID == filter.InstanceID {
				kept = append(kept, r)
			}
		}
		records = kept
	}
	if filter.Limit > 0 && len(records) > filter.Limit {
		records = records[:filter.Limit]
	}
	return records, stats, nil
}

// Stats reports what a status surface needs: the durable loss counters and how many records
// are waiting to be exported.
func Stats(telemetryDir, agent string) (StoreStats, error) {
	var out StoreStats
	all, stats, err := ReadEvents(telemetryDir, Filter{Agent: agent})
	if err != nil {
		return out, err
	}
	state, err := readCursor(telemetryDir, agent)
	if err != nil {
		return out, err
	}
	out.ReadStats = stats
	out.Backlog = countUnexported(all, state)
	return out, nil
}

// parseRecordFile reads one JSONL file, returning the records it could parse and the count of
// lines it could not. An absent file is an empty result, not an error: an agent that has never
// recorded anything is the normal state, not a fault.
func parseRecordFile(path string) ([]StepEvent, int, error) {
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, 0, nil
		}
		return nil, 0, fmt.Errorf("telemetry: opening %s: %w", filepath.Base(path), err)
	}
	defer f.Close()

	var (
		records   []StepEvent
		malformed int
	)
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, tooLong, err := readRecordLine(r)
		if len(line) > 0 || tooLong {
			switch {
			case tooLong:
				// Counted, not swallowed. The obvious line reader in this language gives up
				// silently on an over-long line, and a reader that stops early looks exactly
				// like data that was never written.
				malformed++
			case len(bytes.TrimSpace(line)) == 0:
				// A blank line is not corruption.
			default:
				var ev StepEvent
				if json.Unmarshal(line, &ev) != nil {
					malformed++
					continue
				}
				records = append(records, ev)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return records, malformed, nil
			}
			return nil, 0, fmt.Errorf("telemetry: reading %s: %w", filepath.Base(path), err)
		}
	}
}

// readRecordLine returns one newline-terminated line, reporting rather than hiding a line that
// exceeds the record ceiling. The remainder of an over-long line is consumed so the read
// resumes cleanly at the next record instead of treating the tail as a fresh one.
func readRecordLine(r *bufio.Reader) (line []byte, tooLong bool, err error) {
	for {
		chunk, rerr := r.ReadSlice('\n')
		if len(line)+len(chunk) <= maxRecordBytes {
			line = append(line, chunk...)
		} else {
			tooLong = true
		}
		if rerr == bufio.ErrBufferFull {
			continue
		}
		if tooLong {
			line = nil
		}
		return line, tooLong, rerr
	}
}

func readCursor(telemetryDir, agent string) (cursorState, error) {
	var state cursorState
	raw, err := os.ReadFile(cursorPath(telemetryDir, agent))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state, nil
		}
		return state, fmt.Errorf("telemetry: reading export cursor: %w", err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		// A cursor that will not parse is treated as absent. That re-sends records rather
		// than skipping them, which deterministic span identities make harmless.
		return cursorState{}, nil
	}
	return state, nil
}

// writeCursor replaces the whole cursor value at once. This is the one place in the package
// where the atomic whole-file helper is the right tool — the record log must never be
// rewritten, but the cursor is a single small value whose readers must never observe a
// half-written state.
func writeCursor(telemetryDir, agent string, state cursorState) error {
	if err := os.MkdirAll(telemetryDir, 0o755); err != nil {
		return fmt.Errorf("telemetry: creating telemetry dir: %w", err)
	}
	state.UpdatedAt = time.Now().UTC().Format(TimestampLayout)
	raw, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("telemetry: encoding export cursor: %w", err)
	}
	if err := fsutil.WriteFileAtomic(cursorPath(telemetryDir, agent), append(raw, '\n'), 0o644); err != nil {
		return fmt.Errorf("telemetry: writing export cursor: %w", err)
	}
	return nil
}

// countUnexportable records closed steps that no export could ever carry — the batch they
// travelled in reached the backend, but they were not in it. They belong in the same loss
// count as a record a forced rotation destroyed, because from a reader's side the two are the
// same thing: data that exists locally and will never appear downstream.
func countUnexportable(telemetryDir, agent string, n int) error {
	if n <= 0 {
		return nil
	}
	state, err := readCursor(telemetryDir, agent)
	if err != nil {
		return err
	}
	state.DroppedTotal += int64(n)
	state.DroppedUnexported += int64(n)
	return writeCursor(telemetryDir, agent, state)
}

// writeOffUnexportable counts the loss and then steps the cursor over the whole batch, which
// is only ever called when nothing in that batch could be rendered. Without it a single
// unrenderable record is a permanent blockage: the drain is ordered, so every later record
// queues behind it until the size ceiling begins discarding them.
func writeOffUnexportable(telemetryDir, agent string, batch []StepEvent, unrenderable int) error {
	if err := countUnexportable(telemetryDir, agent, unrenderable); err != nil {
		return err
	}
	return advanceCursor(telemetryDir, agent, batch)
}

// advanceCursor bookmarks the last record of a successfully delivered batch. It is called only
// after the backend has confirmed receipt; every other outcome leaves the bookmark untouched,
// which is what makes a failed export a retry rather than a loss.
func advanceCursor(telemetryDir, agent string, exported []StepEvent) error {
	if len(exported) == 0 {
		return nil
	}
	state, err := readCursor(telemetryDir, agent)
	if err != nil {
		return err
	}
	state.LastExportedDigest = recordDigest(exported[len(exported)-1])
	state.ExportedTotal += int64(len(exported))
	return writeCursor(telemetryDir, agent, state)
}
