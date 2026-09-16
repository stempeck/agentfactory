package telemetry

import (
	"path/filepath"
	"strings"

	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// digestSubdir holds the learned-data cache, one file per formula. It sits UNDER the records
// directory rather than beside it because the cache is derived from those records: deleting the
// telemetry directory has to take the cache with it, or a rebuild would be reconciling a digest
// against records that no longer exist.
const (
	digestSubdir  = "digest"
	digestFileExt = ".json"
)

// LearnedDigestDir and LearnedDigestPath compose the cache's location from an injected telemetry
// directory, the way stepsPath and cursorPath already compose the record log's and the export
// cursor's. They are NOT in internal/config/paths.go, and the distinction is not incidental: that
// file is the inventory of config DOCUMENTS, each of which is one file with one exposure
// disposition (#620), and a per-formula derived artifact is neither. INV-12 is satisfied the same
// way the record log satisfies it — nothing here names .agentfactory, so a relocation of the
// config tree moves this with it.
//
// They are exported because the two writers live in the verb layer: one at af done's step close
// and one behind af telemetry rebuild. Both compose the path here rather than independently, so a
// drift between their spellings — a cache written where nothing reads it, i.e. a factory that
// never learns while reporting that it has — is not expressible.
func LearnedDigestDir(telemetryDir string) string {
	return filepath.Join(telemetryDir, digestSubdir)
}

// LearnedDigestPath joins; it does not validate. Whether a formula name can be a path segment is
// safeDigestSegment's question, and keeping the refusal there keeps ONE refusal shared by both
// writers rather than two that can disagree.
func LearnedDigestPath(telemetryDir, formula string) string {
	return filepath.Join(LearnedDigestDir(telemetryDir), formula+digestFileExt)
}

// RebuildStats is what one rebuild saw. It embeds ReadStats because a rebuild is a read, and adds
// the one count only a MULTI-agent read can produce: an agent whose log could not be read at all.
//
// That count is carried rather than raised. The digest is a cache built from every agent's history,
// and one unreadable log — a permission the operator has yet to fix — must not cost every other
// agent's learning. It cannot corrupt the result either: a partial read yields FEWER runs, and
// tokenomics.MergeDigests keeps the better-supported row, so the missing agent's absence leaves the
// standing figures alone rather than overwriting them with a thinner sample.
type RebuildStats struct {
	ReadStats
	UnreadableAgents int
}

// RebuildLearnedDigests reconstructs the learned-data cache from the raw record store (#668 K6).
//
// This is the ONLY path by which an aggregate is computed. The af done hook and the operator's
// rebuild verb both come through it, differing only in scope, and they have to: a median cannot be
// maintained incrementally, so a hook that folded one closing record into a loaded digest would
// drift away from anything a rebuild produced, and a cache that disagrees with the records it was
// built from is worse than no cache. Re-deriving at close is a real cost, paid deliberately.
//
// agents is the roster to walk, and walking all of it is what makes the cache cross-agent: the
// same formula step is closed by different agents on different runs, and every one of those runs
// belongs in the same row.
//
// An empty formula means every formula in the store — the operator verb's scope. Naming one
// narrows the walk to it, which is the hook's. Either way the result is keyed by formula name,
// because that is the granularity of a digest FILE, and both callers write those files from the
// same values.
//
// updatedAt is stamped onto every aggregate by the caller. Neither this package nor
// internal/tokenomics holds a clock here, and that is what lets a rebuild reproduce the file it
// replaced rather than merely resemble it.
//
// The record store is the truth and this is the cache. Malformed lines are counted and returned
// rather than raised, because one corrupt line at the tail of a log — the shape a crash mid-write
// leaves behind — must not cost the caller every record before it.
//
// What this returns is a derivation from the records that SURVIVE, which is not the whole cache: a
// step whose records have rotated out is absent here and present on disk. Reconciling the two is
// tokenomics.MergeDigests' job, and keeping that job out of this function is what lets the same
// derivation serve both the standing cache and the byte-equivalence a rebuild is checked against.
func RebuildLearnedDigests(telemetryDir string, agents []string, formula, updatedAt string) (map[string]tokenomics.Digest, RebuildStats) {
	var stats RebuildStats
	byFormula := map[string][]tokenomics.StepSample{}

	for _, agent := range agents {
		records, agentStats, err := ReadEvents(telemetryDir, Filter{Agent: agent})
		if err != nil {
			stats.UnreadableAgents++
			continue
		}
		stats.Malformed += agentStats.Malformed
		stats.Dropped += agentStats.Dropped
		stats.DroppedUnexported += agentStats.DroppedUnexported

		for _, sample := range samplesFrom(records) {
			name := sample.Key.Formula
			if !SafeDigestSegment(name) {
				continue
			}
			if formula != "" && name != formula {
				continue
			}
			byFormula[name] = append(byFormula[name], sample)
		}
	}

	out := make(map[string]tokenomics.Digest, len(byFormula))
	for name, samples := range byFormula {
		out[name] = tokenomics.BuildDigest(samples, updatedAt)
	}
	return out, stats
}

// SafeDigestSegment answers whether a formula name can be a filename.
//
// The name comes off a record, and the record's name comes off a bead title that nothing
// validates. A digest path is composed from it, so without this check a name carrying path
// separators would place the cache anywhere the process can write — the same exposure AppendEvent
// refuses for the agent name, for the same reason.
//
// The refusal lives here rather than at either writer so that the af done hook and the rebuild
// verb inherit ONE rule. Two copies could disagree, and a formula that one writer skipped while
// the other wrote it would break the byte-equivalence the cache's correctness rests on. It is
// exported for that reason and not for reuse: the close path asks it the same question in order to
// SAY that it declined, because a bulk rebuild skipping one name among many is noise while one
// step closing and learning nothing is the whole of what that close had to learn.
func SafeDigestSegment(name string) bool {
	if name == "" || name == "." || name == ".." {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

// stepRun identifies one run of one step WITHIN one instance. The instance is part of it because
// the per-instance bead id it pairs with is minted fresh on every instantiation of a formula, so
// the pairing is a correct within-instance session count and never a cross-instance join.
type stepRun struct {
	instance string
	step     string
}

// samplesFrom reduces one agent's records to one sample per closed step.
//
// Only a step_end record is a run. An opening record carries occupancy figures of its own, and
// folding it in would count one step as two runs while mixing an opening level into a peak.
//
// The digest key's StepID leg is the record's StepLabel — the formula's own stable step id — not
// its StepID, which is the per-instance minted bead id that differs on every run. Keying on the
// bead id would file each run of a step under its own single-run row, and no later run of the same
// formula-step could ever join it. A step_end with no StepLabel is SKIPPED rather than bucketed
// under the empty string or fallen back to its bead id: an old record written before the label
// existed contributes nothing rather than either collapsing every unlabelled step onto one bogus
// key or silently re-creating the cold-arm bug the label exists to fix.
//
// Two of the sample's legs are NOT on the closing record and are joined from other kinds (#678 K2).
// Which program a run executed is on its instance_start, and which objective chose its effort level
// is on the intervention the actuator wrote. Neither can be moved onto step_end: these records are
// append-only, so the join is the only way to read a leg a closing record was written without.
func samplesFrom(records []StepEvent) []tokenomics.StepSample {
	sessions := sessionsPerStep(records)
	programs := programPerInstance(records)
	objectives := objectivePerSession(records)

	var out []tokenomics.StepSample
	for _, r := range records {
		if r.Event != EventStepEnd || r.StepLabel == "" {
			continue
		}
		run := stepRun{r.InstanceID, r.StepID}
		program := programs[r.InstanceID]
		out = append(out, tokenomics.StepSample{
			Key:               tokenomics.DigestKey{Formula: r.Formula, StepID: r.StepLabel, Model: r.Model},
			PeakCtxTokens:     r.PeakCtxTokens,
			CtxTokensStart:    r.CtxTokensStart,
			CumTokensDelta:    r.CumTokensDelta,
			DurationMS:        durationOf(r),
			OutTokens:         r.OutTokens,
			ThinkTokensEst:    r.ThinkTokensEst,
			ThinkTokens:       r.ThinkTokens,
			SubagentTokens:    r.SubagentTokens,
			SubagentLaunches:  r.SubagentLaunches,
			RepeatReads:       r.RepeatReads,
			GateFlags:         r.GateFlags,
			EffortLevel:       r.EffortLevel,
			Objective:         objectives[r.SessionID],
			FormulaDigest:     program.digest,
			InstanceStartedAt: program.startedAt,
			Sessions:          sessions[run],
		})
	}
	return out
}

// programRun is what one instance_start says about the program its instance ran: the hash of the
// formula file and when the run began.
type programRun struct {
	digest    string
	startedAt string
}

// programPerInstance indexes the opening records so each closed step can name the program it ran
// under. FormulaDigest is written only on instance_start (and re-hashed on instance_end), and the
// OPENING record is the one this join wants: it is the program the run actually executed, where the
// closing hash would be the file as it stands after any edit made while the run was in flight.
//
// An instance with no opening record — rotated away, or written before the digest existed — yields
// the zero programRun, and the aggregation reads that empty digest as "unknown" rather than as a
// program of its own.
func programPerInstance(records []StepEvent) map[string]programRun {
	out := map[string]programRun{}
	for _, r := range records {
		if r.Event != EventInstanceStart {
			continue
		}
		out[r.InstanceID] = programRun{digest: r.FormulaDigest, startedAt: r.TS}
	}
	return out
}

// objectivePerSession indexes the intervention records so each closed step can name why its effort
// level was what it was. The arm is a property of the SESSION, not of a per-step join: a session
// runs reduced or it does not, and every step it closes belongs to whichever arm the session ran in
// (#679 F1/T1). Keying on the step instead is the cold-arm bug it fixes — a reduce_effort record
// carries the session it acted on, not the step that later closed, so a step-keyed join stamps the
// baseline arm onto a reduced session's steps and the efficiency arm onto none of them.
//
// Only an Action=ActionReduceEffort record marks a session reduced: it is the sole
// attestation that the effort level actually changed. An ADVISORY (Action=ActionAdvise, e.g. thrift
// or interview counsel) keeps its own objective on the record but does NOT decide the arm — counsel
// the agent could ignore is not the actuator reducing the level.
//
// A record with an EMPTY SessionID is unjoinable and marks nothing: with no session to
// attribute the reduction to, its steps stay in the baseline arm rather than being credited to a
// session the record cannot name.
//
// A session can carry several reduce_effort records, and the efficiency objective WINS over any
// other: a session whose effort level was chosen by the efficiency actuator is a treatment session
// whatever else also fired on it — a capacity handoff later does not put it back in the control arm.
func objectivePerSession(records []StepEvent) map[string]tokenomics.Objective {
	out := map[string]tokenomics.Objective{}
	for _, r := range records {
		if r.Event != EventIntervention || r.Objective == "" {
			continue
		}
		if r.Action != ActionReduceEffort || r.SessionID == "" {
			continue
		}
		if out[r.SessionID] == tokenomics.ObjectiveEfficiency {
			continue
		}
		out[r.SessionID] = tokenomics.Objective(r.Objective)
	}
	return out
}

// durationOf lifts the one non-pointer figure onto the pointer discipline the rest of the sample
// uses. A duration of zero is omitted on the wire, so an unmeasured step and a zero-length one are
// already the same bytes; reporting the absence honestly keeps it out of the median rather than
// dragging one down.
func durationOf(r StepEvent) *int64 {
	if r.DurationMS <= 0 {
		return nil
	}
	ms := int64(r.DurationMS)
	return &ms
}

// sessionsPerStep counts how many sessions each run of each step spanned — H-R5's multiplier, and
// on its own the signal that a step does not fit its window in one pass.
//
// It cannot be read off the closing record. The count is a property of the SPAN, and the records
// that prove a step crossed three sessions are the session_start records BETWEEN its two ends,
// which carry no step id at all. DeriveWindows already establishes those ends for the backend
// attribution join, so the count is every distinct session id the instance recorded inside one.
//
// The bounds are compared as strings, the way DeriveWindows compares them: the record timestamp
// layout is fixed-width and lexically ordered, and parsing here would add a failure mode to a
// count that has an honest answer without one.
//
// Records are indexed by instance first so the scan is bounded by one instance's own records
// rather than by the whole log, which is on the close path.
// A session is only counted if it ANNOUNCED itself, and #678 K1 is what made that necessary. The
// quality-gate grader launches `claude -p` from the agent's own working directory, which fires the
// agent's SessionStart hook, which used to take the agent's session identity — so a graded step
// accumulated one extra "session" per graded turn and the steps that were graded hardest looked like
// the steps that recycled most. The pane guard in af prime stops FUTURE identity theft at the
// source; the read side here does NOT undo the past, because a grader session_start already in the
// window is still an EventSessionStart and still counts as announced (#679 F13). A log written before
// the pane guard keeps reporting its inflated count until those records rotate away; the honest fix
// was to stop new ones being written, not to teach the read side to guess which past session_start a
// grader authored.
//
// Announcing is EITHER of two things, and the union is load-bearing rather than a hedge. A
// session_start inside the window is the direct evidence. But a step's OPENING session usually
// announced itself before the step began — the window starts at the step's own start, not at the
// session's — so requiring session_start alone would drop the session that ran most of the step and
// silently report one fewer. Appearing on a step boundary FOR THIS STEP is the second form of
// evidence, and it is evidence a grader can never manufacture: af prime and af done write those
// records, a grader session runs neither.
func sessionsPerStep(records []StepEvent) map[stepRun]int {
	byInstance := map[string][]StepEvent{}
	for _, r := range records {
		if r.SessionID == "" {
			continue
		}
		byInstance[r.InstanceID] = append(byInstance[r.InstanceID], r)
	}

	out := map[stepRun]int{}
	for _, w := range DeriveWindows(records) {
		seen := map[string]bool{}
		for _, r := range byInstance[w.InstanceID] {
			if r.TS < w.Start || r.TS > w.End {
				continue
			}
			announced := r.Event == EventSessionStart ||
				((r.Event == EventStepStart || r.Event == EventStepEnd) && r.StepID == w.StepID)
			if !announced {
				continue
			}
			seen[r.SessionID] = true
		}
		out[stepRun{w.InstanceID, w.StepID}] = len(seen)
	}
	return out
}

// StepRunKey identifies one run of one step within an instance on the read side. Its StepID is the
// per-instance bead id (not the StepLabel the learned digest folds under), because that is what the
// report and band builders already index their rows by.
type StepRunKey struct {
	InstanceID string
	StepID     string
}

// SessionSpans exposes the per-run session count to the read surfaces (#679 F7). The learned side
// already folds this same count into SessionsPerStep; a per-step read surface reports it per run so
// K10 can rank steps by sessions without re-deriving the announce-union rule sessionsPerStep guards.
func SessionSpans(records []StepEvent) map[StepRunKey]int {
	src := sessionsPerStep(records)
	out := make(map[StepRunKey]int, len(src))
	for k, v := range src {
		out[StepRunKey{InstanceID: k.instance, StepID: k.step}] = v
	}
	return out
}
