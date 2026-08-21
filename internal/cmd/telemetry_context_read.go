package cmd

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// This file is #622 Phase 3's read half: the one derivation of what a step's context window did,
// and the first production reader of the recovery funnel log.
//
// It exists for the same reason step_context.go (C4's write half) does. The table renderer and the
// --json producer are deliberately separate — one owes an operator display strings, the other owes
// a consumer comparable numbers (telemetry_json.go:317-324) — but they must never disagree about
// WHETHER a step went over its bound. Two spellings of that arithmetic would drift, and the
// drifting half would be the machine-readable one the improvement loop acts on. So the facts are
// derived once here and rendered twice there.
//
// No verdict is ever stored on a record (event.go:67-69): a figure cannot lie and a verdict
// computed under one version of the rules can. Everything below is therefore recomputed on every
// read, from the raw figures alone.

// reportReadContext is what a report needs beyond the step records themselves. It is assembled
// once per report — before the per-agent fan-out — because both of its members are factory-wide:
// re-reading the funnel log per agent would multiply a file read by the roster size and could
// observe the file mid-rotation, which is how an agent's recycle would appear in one row and not
// in the next.
type reportReadContext struct {
	// recoveries is every funnel entry across both retained generations, oldest-first.
	recoveries []recoveryLogEntry
	// stalenessSecs is the factory's own definition of a stale occupancy reading, borrowed from
	// the recovery ladder rather than invented here so the report and the watchdog cannot
	// disagree about what "stale" means. Zero means the factory could not tell us — in which case
	// no reading is marked stale, because "we cannot judge freshness" is not "this is fresh".
	stalenessSecs int
}

// newReportReadContext resolves both members from the SAME factory root the report already holds.
//
// That is not a convenience: recoveryLogPath(root) and config.TelemetryDir(root) hang off one
// root, resolved once by runTelemetry through resolveInvokerRoot (telemetry.go:91), which follows
// the .factory-root redirect out of a worktree. A second resolution rule here could read a
// worktree-local .runtime while the records came from the outer factory, and the join would
// silently never match.
//
// Neither member can fail the report. An unreadable funnel log costs the INTERRUPTED annotation;
// an unreadable startup config costs the staleness marker. Both degrade to a missing annotation on
// a row that still renders, which is the direction telemetry_json.go:26-28 requires of this whole
// surface.
func newReportReadContext(factoryRoot string) reportReadContext {
	rc := reportReadContext{recoveries: readRecoveryLog(factoryRoot)}
	if cfg, err := config.LoadStartupConfig(factoryRoot); err == nil {
		rc.stalenessSecs = cfg.Recovery.StalenessSecs
	}
	return rc
}

// stepPair is the matched record pair behind one report row.
//
// Retaining the START RECORD — not merely its row index, as both pairing loops did before #622 —
// is what makes the HIGH-3 session gate possible at read time. The gate needs three things off the
// start (SessionID, CtxTokensUsed, CumTokens) and the pre-#622 map[key]int kept none of them.
//
// The recorded cum_tokens_delta on the close cannot stand in for the derivation. done.go:261-275
// already applies the same gate at write time, so a nil there is FOUR-WAY ambiguous — the start's
// counter was absent, the close's was, the session changed, or a negative was suppressed. The
// report must tell "nobody measured this" from "this cannot be attributed", so it re-derives.
type stepPair struct {
	start *telemetry.StepEvent
	end   *telemetry.StepEvent
}

// stepContextFacts is one step's context story as the report knows it: raw figures, the two
// read-time verdicts, and the markers that qualify them.
//
// Every figure is a POINTER, and that is the whole point of the issue. A step nobody measured and
// a step that consumed nothing must not collapse into the same 0 — a zero occupancy reads as an
// empty window, the exact opposite of the conclusion an unmeasured overrun should produce
// (gaps.md GAP-19 states the same rule for the occupancy channel).
//
// consumptionState carries the distinction pointers alone cannot: a nil cum_tokens_delta has two
// different meanings and they must never be swapped. Across a KNOWN session boundary the number
// exists but belongs to nobody — UNATTRIBUTABLE. With no recorded start, no counter at either end,
// or no session id to compare, there is no number at all — UNMEASURABLE. The improvement loop
// treats the first as a worst-class signal and the second as an instruction to switch measurement
// on, which is why an unknown session must land in the second: it is the weaker claim, and the only
// one the data supports.
type stepContextFacts struct {
	ctxTokensStart *int64
	ctxTokensEnd   *int64
	ctxTokensTotal *int64
	ctxUsedPct     *float64
	cumTokensDelta *int64
	ctxBoundTokens *int64

	overOccupancy    *bool
	overConsumption  *bool
	consumptionState string

	compacted  *bool
	stale      *bool
	boundDrift *bool

	interruptedTrigger string
	interruptedPct     *float64
}

// deriveStepContext is #622 C6, and it is pure: same records in, same facts out, no clock, no
// filesystem, no environment. Everything the report says about context is decided here.
func deriveStepContext(pair stepPair, stalenessSecs int) stepContextFacts {
	f := stepContextFacts{consumptionState: consumptionUnmeasurable}

	// The window at the moment the step opened, preferring the step_start record itself and
	// falling back to the echo done.go writes onto the close (done.go:205). Both record the same
	// fact; the echo survives a rotation that discarded the start, so preferring the record and
	// accepting the echo strictly increases what the report can say without ever mixing meanings.
	if pair.start != nil && pair.start.CtxTokensUsed != nil {
		f.ctxTokensStart = pair.start.CtxTokensUsed
	}
	if pair.end == nil {
		// An open step. Nothing about its close exists yet, and inventing an end would put a
		// fabricated duration where nothing could later distinguish it from a measurement
		// (window.go:45-48). The C10 join below is the only thing that may add to this row.
		return f
	}
	end := pair.end
	if f.ctxTokensStart == nil {
		f.ctxTokensStart = end.CtxTokensStart
	}
	f.ctxTokensEnd = end.CtxTokensUsed
	f.ctxTokensTotal = end.CtxTokensTotal
	f.ctxUsedPct = end.CtxUsedPct

	// A bound of 0 is "no bound was recorded", not "the budget was zero". Phase 2 assigns
	// int64(stepCtx.BoundTokens) unconditionally (done.go:206) and stepCtx's zero value IS the
	// config-unreadable case, so a plain `> 0` comparison against it would rule every measured
	// step over its bound.
	if end.CtxBoundTokens > 0 {
		bound := end.CtxBoundTokens
		f.ctxBoundTokens = &bound
	}

	if f.ctxTokensStart != nil && f.ctxTokensEnd != nil {
		compacted := *f.ctxTokensEnd < *f.ctxTokensStart
		f.compacted = &compacted
	}
	if f.ctxBoundTokens != nil && f.ctxTokensTotal != nil {
		drift := *f.ctxBoundTokens > *f.ctxTokensTotal
		f.boundDrift = &drift
	}
	if f.ctxBoundTokens != nil && f.ctxTokensEnd != nil {
		over := *f.ctxTokensEnd > *f.ctxBoundTokens
		f.overOccupancy = &over
	}
	f.stale = deriveStaleness(*end, stalenessSecs)

	f.cumTokensDelta, f.consumptionState = deriveConsumption(pair)
	if f.cumTokensDelta != nil && f.ctxBoundTokens != nil {
		over := *f.cumTokensDelta > *f.ctxBoundTokens
		f.overConsumption = &over
	}
	return f
}

// deriveConsumption is the HIGH-3 session gate, restated on the read side.
//
// cum_tokens counts ONE session's lifetime. Across a mid-step recycle the new session's counter
// starts near zero, so the subtraction yields a large negative — a number that looks like a
// measurement, in the figure the improvement loop leans on hardest. The only honest answers are a
// delta or a named refusal, never a zero.
//
// The refusals are two, and they are not interchangeable. UNATTRIBUTABLE means the step ran across
// a session boundary: consumption happened and cannot be assigned. UNMEASURABLE means there is
// nothing to subtract — no start record was found (the pre-existing close-without-start state,
// telemetry.go's step_end branch), a session id was missing, or a counter was.
//
// Which side an ABSENT session id falls on is the subtle half. It is unmeasurable, not
// unattributable: telemetry_record.go:100-106 returns "" for the id on any read error, so an empty
// one says the session is UNKNOWN and says nothing whatever about a recycle. Calling that a
// mismatch would print "recycled mid-step" over a step that may never have been recycled — the
// absent-collapsing-into-a-claim error this issue exists to prevent, merely pointing the other way.
// Only two ids that are both present and different establish a boundary.
func deriveConsumption(pair stepPair) (*int64, string) {
	if pair.start == nil || pair.end == nil {
		return nil, consumptionUnmeasurable
	}
	start, end := pair.start, pair.end
	if start.SessionID == "" || end.SessionID == "" {
		return nil, consumptionUnmeasurable
	}
	if start.SessionID != end.SessionID {
		return nil, consumptionUnattributable
	}
	if start.CumTokens == nil || end.CumTokens == nil {
		return nil, consumptionUnmeasurable
	}
	delta := *end.CumTokens - *start.CumTokens
	if delta < 0 {
		// One session id and a falling lifetime counter is not a consumption figure, it is a
		// contradiction — the same one done.go:269-273 refuses to record. There is no number here
		// to attribute, so it is unmeasurable rather than unattributable.
		return nil, consumptionUnmeasurable
	}
	return &delta, consumptionMeasured
}

// deriveStaleness answers how far the occupancy reading lags the record that carries it.
//
// Both stamps use telemetry.TimestampLayout — ctx_observed_at is written through it
// (step_context.go:82) — so this comparison never crosses a format boundary, unlike the funnel
// join below. A reading that cannot be dated is not marked fresh and is not marked stale: nil,
// because "we cannot tell" is a third answer and #596's three-state rule exists to keep it.
//
// This marker is deliberately unreachable for records THIS binary wrote, and that is not dead code.
// attachStepOccupancy already refuses to record a reading older than the same threshold
// (step_context.go:59-70), so under a stable config the write side filters what this would catch.
// It fires on the cases that outlive one config: a lowered threshold read against older records,
// clock skew between the snapshot writer and the recorder, or a foreign writer. ux.md U2 asks the
// reader to qualify a lagging figure, and a reader that trusts the writer to have done it cannot.
func deriveStaleness(end telemetry.StepEvent, stalenessSecs int) *bool {
	if stalenessSecs <= 0 || end.CtxObservedAt == "" {
		return nil
	}
	// Staleness QUALIFIES a figure, so with no figure there is nothing to qualify. The write side
	// records occupancy all-or-none (step_context.go:attachStepOccupancy), so this only guards a
	// record some other writer left half-filled — but "the reading is stale" said about a reading
	// that does not exist is worse than silence.
	if end.CtxTokensUsed == nil && end.CtxUsedPct == nil {
		return nil
	}
	observed, err := time.Parse(telemetry.TimestampLayout, end.CtxObservedAt)
	if err != nil {
		return nil
	}
	recorded, err := time.Parse(telemetry.TimestampLayout, end.TS)
	if err != nil {
		return nil
	}
	lag := recorded.Sub(observed)
	if lag < 0 {
		lag = -lag
	}
	stale := lag > time.Duration(stalenessSecs)*time.Second
	return &stale
}

// --- C10: the funnel log's first production reader ------------------------------------------

// maxRecoveryLogLineBytes matches the ceiling countRecoveryLogLines already scans with
// (recovery.go:373) and the record store's own maxRecordBytes (store.go:22-25). One line of this
// log is a dozen scalars; anything larger is corruption, not data.
const maxRecoveryLogLineBytes = 1 << 20

// readRecoveryLog returns every funnel entry across both retained generations, oldest-first.
//
// Two generations, in the rotated-then-live order telemetry.ReadEvents reads its own log in
// (store.go:284-295). Reading only the live file would lose every recycle older than the 10 000-line
// cap (rotateRecoveryLog, recovery.go:353-359) — which is precisely the history an improvement
// session reads when it asks what interrupted the run.
//
// Nothing here is fatal. A factory that has never recycled has no log at all, and that is data
// rather than a fault: if it were an error, every report on a healthy factory would fail.
func readRecoveryLog(root string) []recoveryLogEntry {
	path := recoveryLogPath(root)
	return append(parseRecoveryLogFile(path+".1"), parseRecoveryLogFile(path)...)
}

// parseRecoveryLogFile decodes one generation, skipping what it cannot read.
//
// It mirrors parseRecordFile (store.go:342-384) rather than calling it: that function is
// unexported, lives in package telemetry, and is hard-typed to StepEvent. The four-branch shape is
// copied deliberately — absent file is empty, a blank line is not corruption, an unparseable line
// is skipped, an over-long line is skipped — because one corrupt line at the tail is the shape a
// crash mid-write leaves behind and it must not hide the entries around it (store.go:274-276).
//
// SKIPPED means skipped, which is why this reads through a bufio.Reader and not the obvious
// Scanner. A Scanner STOPS at an over-long line (store.go:362-364 says so in as many words), and
// stopping here would discard every LATER entry — in an append-only log, the most recent recycles,
// which are exactly the ones the latest-wins join needs. countRecoveryLogLines (recovery.go:361-383)
// can afford a Scanner because an undercount only delays a rotation; losing the tail of this read
// would silently render an interrupted step as merely open.
//
// Unlike the record store this reader does NOT surface a malformed count. The report's stats key
// set is a pinned contract in five places, and the cost of silence differs: a lost record line
// costs a ROW, while a lost funnel line costs at most an annotation on a row that still renders as
// open. Widening a pinned shape to report that is not a trade this phase is authorised to make.
func parseRecoveryLogFile(path string) []recoveryLogEntry {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()

	var entries []recoveryLogEntry
	r := bufio.NewReaderSize(f, 64*1024)
	for {
		line, tooLong, rerr := readFunnelLine(r)
		if len(line) > 0 && !tooLong {
			if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
				var entry recoveryLogEntry
				if json.Unmarshal(trimmed, &entry) == nil {
					entries = append(entries, entry)
				}
			}
		}
		if rerr != nil {
			return entries
		}
	}
}

// readFunnelLine returns one newline-terminated line under the funnel's own ceiling, delegating to
// the shared telemetry.ReadBoundedLine. The name is kept as a thin wrapper because the funnel keeps
// its OWN ceiling (maxRecoveryLogLineBytes, independent of the record store's) and because external
// comments reference this symbol by name.
func readFunnelLine(r *bufio.Reader) (line []byte, tooLong bool, err error) {
	return telemetry.ReadBoundedLine(r, maxRecoveryLogLineBytes)
}

// interruptedBy finds the recycle that ended an open step, or reports that none did.
//
// The join keys on AGENT and RECENCY, with the instance id as a narrowing filter rather than a
// requirement. Three details of the funnel's own writers force that shape:
//
//   - ResumedStep is unusable as a key. It is "" for every occupancy-less class
//     (watchdog.go:531-538, compact_handoff.go:111, handoff.go:127), so keying on it would drop
//     exactly the classes an operator most needs named.
//   - InstanceID is "" for those same four classes. A strict equality join would make crash,
//     error_pattern, compact_handoff and self_handoff permanently unjoinable, so an EMPTY instance
//     on the entry is treated as "this recycle did not record which formula it was in" and is
//     allowed to match. A non-empty one must agree.
//   - SessionID is NOT compared, and must not be. StepEvent.SessionID is the raw .runtime/session_id
//     (telemetry_record.go:100-106) while recoveryLogEntry.SessionID is the sanitized stem
//     (done.go:354-357). Comparing them is the #563 drift class: two spellings of one identifier on
//     the two sides of a comparison, and the match silently never happens.
//
// Recency is compared as TIME, never as text. The two logs use different grammars —
// telemetry.TimestampLayout carries milliseconds and a literal Z (event.go:39), recoveryStamp is
// RFC3339 at second precision (recovery.go:258) — so each side is parsed with its own layout. A
// lexical comparison inverts the answer within a shared second, because '.' sorts below 'Z'.
//
// The LATEST match wins: the design asks for the last-known occupancy, and a step recycled twice
// died in the state the second entry describes.
func interruptedBy(entries []recoveryLogEntry, agent, instance, startTS string) (recoveryLogEntry, bool) {
	opened, err := time.Parse(telemetry.TimestampLayout, startTS)
	if err != nil {
		// An undatable start cannot be joined against anything without guessing, and a guess here
		// would relabel a running step as lost.
		return recoveryLogEntry{}, false
	}
	var (
		best   recoveryLogEntry
		bestAt time.Time
		found  bool
	)
	for _, entry := range entries {
		if entry.Agent != agent {
			continue
		}
		if entry.InstanceID != "" && entry.InstanceID != instance {
			continue
		}
		at, ok := parseRecoveryStamp(entry.At)
		if !ok || at.Before(opened) {
			continue
		}
		if !found || !at.Before(bestAt) {
			best, bestAt, found = entry, at, true
		}
	}
	return best, found
}

// applyInterruption folds a matched recycle into the row's facts.
//
// ObservedPct 0 becomes UNKNOWN rather than 0%, and that is the same absent-is-not-zero rule the
// pointers above encode: four of the nine trigger classes record no occupancy at all, and a 0%
// would rank a crashed agent as the emptiest window in the run. The trigger is carried through
// VERBATIM, including a class this binary has never heard of — recovery.go:68-71 requires an
// unrecognised recycle path to read as visibly unclassified rather than as a decoder fault.
func applyInterruption(f *stepContextFacts, entry recoveryLogEntry) {
	f.interruptedTrigger = entry.Trigger
	if entry.ObservedPct > 0 {
		pct := entry.ObservedPct
		f.interruptedPct = &pct
	}
}
