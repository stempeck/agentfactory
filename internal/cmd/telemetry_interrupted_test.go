package cmd

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// These tests pin #622 C10: the report's OPEN rows joined against the factory-root funnel log.
//
// The behaviour they protect is survivorship. Before this join a step whose session was recycled
// mid-step stayed "open" forever and then dropped out of the improvement loop's evidence entirely,
// so the loop only ever saw the steps that survived — the highest-impact gap in the design.

func setupInterruptedReport(t *testing.T) string {
	t.Helper()
	root := setupTestFactoryForPrime(t)
	t.Chdir(root)
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
	resetReportFlags(t)
	return root
}

// openStepAt seeds a step_start with no matching step_end — the only row shape C10 acts on.
func openStepAt(t *testing.T, root string, openedAt time.Time, title string) {
	t.Helper()
	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
		TS: openedAt.UTC().Format(telemetry.TimestampLayout), Agent: "manager", Formula: "offpath",
		InstanceID: "i1", StepID: "s-open", StepSeq: 1, StepTitle: title,
		SessionID: "sess-a", Verb: "prime", VerbMS: 9,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
}

func renderReport(t *testing.T) string {
	t.Helper()
	return captureStdout(t, func() {
		if err := runTelemetry(telemetryCmd, []string{"report"}); err != nil {
			t.Fatalf("af telemetry report: %v", err)
		}
	})
}

func TestInterruptedRowsJoinTheRecoveryLog(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Interrupted step")

	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(2 * time.Minute)),
		Agent: "manager", Trigger: triggerContextExhaustion,
		ObservedPct: 93.5, ThresholdPct: 85,
		SessionID: "sess-a", InstanceID: "i1", ResumedStep: "s-open",
		Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	line := lineContaining(t, renderReport(t), "Interrupted step")
	if !strings.Contains(line, "INTERRUPTED") {
		t.Errorf("an open step whose session was recycled is not marked INTERRUPTED:\n%s", line)
	}
	if !strings.Contains(line, triggerContextExhaustion) {
		t.Errorf("the row does not name the trigger that recycled it:\n%s", line)
	}
	if !strings.Contains(line, "93.5") {
		t.Errorf("the row does not carry the last-known occupancy:\n%s", line)
	}
}

// The non-vacuity arm. Without it an implementation that marks EVERY open row INTERRUPTED passes.
func TestRecoveryLogJoinLeavesUnmatchedRowsOpen(t *testing.T) {
	root := setupInterruptedReport(t)
	openStepAt(t, root, time.Now().UTC().Add(-10*time.Minute), "Still running")

	line := lineContaining(t, renderReport(t), "Still running")
	if strings.Contains(line, "INTERRUPTED") {
		t.Errorf("an open step with no funnel entry was marked INTERRUPTED:\n%s", line)
	}
	if !strings.Contains(line, "open") {
		t.Errorf("an open step with no funnel entry no longer renders as open:\n%s", line)
	}
}

// An entry stamped BEFORE the step opened describes a previous recycle, not this step's.
func TestRecoveryLogJoinIgnoresEntriesBeforeTheStepOpened(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Opened after the recycle")

	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(-5 * time.Minute)),
		Agent: "manager", Trigger: triggerContextExhaustion, ObservedPct: 91,
		SessionID: "sess-old", InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	line := lineContaining(t, renderReport(t), "Opened after the recycle")
	if strings.Contains(line, "INTERRUPTED") {
		t.Errorf("a funnel entry predating the step_start must not interrupt it:\n%s", line)
	}
}

// The two logs use DIFFERENT timestamp grammars — telemetry.TimestampLayout carries milliseconds
// and a literal Z, recoveryStamp is RFC3339 at second precision. At the same instant
// "…:04.000Z" sorts BEFORE "…:04Z" ('.' < 'Z'), so a lexical comparison inverts this answer and a
// parse with the wrong layout finds nothing at all. Both failures look like "no recycle happened".
func TestRecoveryLogJoinComparesAcrossTimestampGrammars(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Date(2026, 7, 22, 18, 31, 4, 0, time.UTC)
	openStepAt(t, root, openedAt, "Grammar crossing")

	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Second)),
		Agent: "manager", Trigger: triggerProgressBackstop, ObservedPct: 88,
		SessionID: "sess-a", InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	line := lineContaining(t, renderReport(t), "Grammar crossing")
	if !strings.Contains(line, "INTERRUPTED") {
		t.Errorf("an entry one second after the step opened was not matched:\n%s", line)
	}
}

// crash, error_pattern, compact_handoff and self_handoff all write ObservedPct 0 AND an empty
// InstanceID. Both facts are load-bearing: 0 must render UNKNOWN rather than a healthy-looking
// 0%, and an empty instance must still join or that whole class is permanently invisible.
func TestInterruptedOccupancyIsUnknownForOccupancylessTriggers(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Crashed step")

	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "manager", Trigger: triggerCrash,
		Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	line := lineContaining(t, renderReport(t), "Crashed step")
	if !strings.Contains(line, "INTERRUPTED") {
		t.Errorf("an entry with an empty instance id must still join on agent and recency:\n%s", line)
	}
	if !strings.Contains(line, triggerCrash) {
		t.Errorf("the row does not name the crash trigger:\n%s", line)
	}
	if !strings.Contains(line, occupancyUnknown) {
		t.Errorf("an occupancy-less trigger class must render %s:\n%s", occupancyUnknown, line)
	}
	if strings.Contains(line, "0%") {
		t.Errorf("an unrecorded occupancy must never render as 0%%:\n%s", line)
	}
}

// The funnel rotates to a single .jsonl.1 generation. A reader of only the live file loses every
// recycle older than the cap, which is exactly the history the improvement loop reads.
func TestRecoveryLogJoinReadsBothGenerations(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Rotated evidence")

	entry := recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "manager", Trigger: triggerDarkAtHighOccupancy, ObservedPct: 87.25,
		SessionID: "sess-a", InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if err := os.MkdirAll(recoveryStateDir(root), 0o755); err != nil {
		t.Fatalf("mkdir .runtime/recovery: %v", err)
	}
	if err := os.WriteFile(recoveryLogPath(root)+".1", append(line, '\n'), 0o644); err != nil {
		t.Fatalf("seed rotated generation: %v", err)
	}

	row := lineContaining(t, renderReport(t), "Rotated evidence")
	if !strings.Contains(row, "INTERRUPTED") {
		t.Errorf("an entry living only in the rotated generation was not read:\n%s", row)
	}
	if !strings.Contains(row, "87.25") {
		t.Errorf("the rotated entry's occupancy was not carried:\n%s", row)
	}
}

// A factory that has never recycled has no funnel log at all. That is data, not a fault — and if
// it were a fault, every report test in this package would fail at once.
func TestRecoveryLogJoinToleratesAbsentLog(t *testing.T) {
	root := setupInterruptedReport(t)
	if _, err := os.Stat(recoveryLogPath(root)); !os.IsNotExist(err) {
		t.Fatalf("fixture root unexpectedly has a funnel log: %v", err)
	}
	openStepAt(t, root, time.Now().UTC().Add(-time.Minute), "No log at all")

	if line := lineContaining(t, renderReport(t), "No log at all"); !strings.Contains(line, "open") {
		t.Errorf("an absent funnel log must leave the row open:\n%s", line)
	}
}

// One corrupt line at the tail is the shape a crash mid-write leaves behind. It must not make the
// surrounding entries unreadable — the rule internal/telemetry/store.go:274-276 already states for
// the record log.
func TestRecoveryLogJoinSkipsMalformedLines(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Corrupt neighbour")

	good, err := json.Marshal(recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "manager", Trigger: triggerStepBoundaryHandoff, ObservedPct: 76,
		SessionID: "sess-a", InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if err := os.MkdirAll(recoveryStateDir(root), 0o755); err != nil {
		t.Fatalf("mkdir .runtime/recovery: %v", err)
	}
	body := "{not json at all\n\n" + string(good) + "\n"
	if err := os.WriteFile(recoveryLogPath(root), []byte(body), 0o644); err != nil {
		t.Fatalf("seed funnel log: %v", err)
	}

	line := lineContaining(t, renderReport(t), "Corrupt neighbour")
	if !strings.Contains(line, "INTERRUPTED") {
		t.Errorf("a corrupt line must not hide the entries around it:\n%s", line)
	}
	if !strings.Contains(line, triggerStepBoundaryHandoff) {
		t.Errorf("the cooperative boundary trigger was not carried:\n%s", line)
	}
}

// An entry with an EMPTY instance id matches every open row of that agent, and this pins that as a
// decision rather than an accident. It follows from two constraints the design imposes at once:
// ResumedStep cannot be a join key (it is "" for the same classes), and crash / error_pattern /
// compact_handoff / self_handoff must still be joinable even though they record no instance. The
// cost is real and accepted — an older abandoned open row can inherit a later, unrelated recycle's
// trigger — because the alternative is that the four classes an operator most needs named are the
// four that can never be named at all.
func TestRecoveryLogJoinTreatsAnEmptyInstanceAsAWildcard(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-20 * time.Minute)

	for _, step := range []struct{ instance, id, title string }{
		{"i1", "s-a", "First instance step"},
		{"i2", "s-b", "Second instance step"},
	} {
		if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: openedAt.Format(telemetry.TimestampLayout), Agent: "manager", Formula: "offpath",
			InstanceID: step.instance, StepID: step.id, StepSeq: 1, StepTitle: step.title,
			SessionID: "sess-a", Verb: "prime", VerbMS: 9,
		}); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}
	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "manager", Trigger: triggerCrash, Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	out := renderReport(t)
	for _, title := range []string{"First instance step", "Second instance step"} {
		line := lineContaining(t, out, title)
		if !strings.Contains(line, statusInterrupted) {
			t.Errorf("an instance-less crash did not reach %q — the four occupancy-less trigger "+
				"classes would be permanently unjoinable:\n%s", title, line)
		}
		if !strings.Contains(line, triggerCrash) {
			t.Errorf("%q carries no trigger:\n%s", title, line)
		}
	}
}

// SKIPPED has to mean skipped, not stopped. An over-long line is the one corruption shape that
// tempts a reader into abandoning the rest of the file, and in an append-only log the rest is the
// RECENT entries — exactly what the latest-wins join needs. A reader that gave up here would render
// an interrupted step as merely open, which is indistinguishable from data never written.
func TestRecoveryLogJoinSurvivesAnOverlongLine(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "After the giant")

	good, err := json.Marshal(recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(2 * time.Minute)),
		Agent: "manager", Trigger: triggerContextExhaustion, ObservedPct: 88,
		SessionID: "sess-a", InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	})
	if err != nil {
		t.Fatalf("marshal entry: %v", err)
	}
	if err := os.MkdirAll(recoveryStateDir(root), 0o755); err != nil {
		t.Fatalf("mkdir .runtime/recovery: %v", err)
	}
	// Past maxRecoveryLogLineBytes, and past the reader's own buffer, so it is delivered in
	// several chunks — the case a single-shot read would never reach.
	giant := `{"v":1,"agent":"manager","note":"` + strings.Repeat("x", maxRecoveryLogLineBytes+4096) + `"}`
	body := giant + "\n" + string(good) + "\n"
	if err := os.WriteFile(recoveryLogPath(root), []byte(body), 0o644); err != nil {
		t.Fatalf("seed funnel log: %v", err)
	}

	line := lineContaining(t, renderReport(t), "After the giant")
	if !strings.Contains(line, statusInterrupted) {
		t.Errorf("the entry AFTER an over-long line was lost:\n%s", line)
	}
	if !strings.Contains(line, triggerContextExhaustion) {
		t.Errorf("row does not carry the trigger that followed the over-long line:\n%s", line)
	}
	if !strings.Contains(line, "88%") {
		t.Errorf("row does not carry the occupancy that followed the over-long line:\n%s", line)
	}
}

// "Last-known occupancy" (design-doc C10): when several entries follow the step's open, the most
// recent one is the state the step was actually in when it died.
func TestRecoveryLogJoinTakesTheLatestMatchingEntry(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Twice recycled")

	for _, e := range []recoveryLogEntry{
		{V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
			Agent: "manager", Trigger: triggerProgressBackstop, ObservedPct: 44.5,
			InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned},
		{V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(5 * time.Minute)),
			Agent: "manager", Trigger: triggerContextExhaustion, ObservedPct: 96.5,
			InstanceID: "i1", Attempt: 2, Outcome: outcomeRespawned},
	} {
		if err := appendRecoveryLog(root, e); err != nil {
			t.Fatalf("appendRecoveryLog: %v", err)
		}
	}

	line := lineContaining(t, renderReport(t), "Twice recycled")
	if !strings.Contains(line, "96.5") || !strings.Contains(line, triggerContextExhaustion) {
		t.Errorf("the row does not carry the LAST-known recycle:\n%s", line)
	}
	if strings.Contains(line, "44.5") {
		t.Errorf("the row carries a superseded recycle:\n%s", line)
	}
}

// A funnel entry belonging to another agent must never reach this agent's rows. The join's only
// wildcard is the instance id.
func TestRecoveryLogJoinNeverCrossesAgents(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Manager step")

	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "supervisor", Trigger: triggerContextExhaustion, ObservedPct: 92,
		InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	line := lineContaining(t, renderReport(t), "Manager step")
	if strings.Contains(line, "INTERRUPTED") {
		t.Errorf("another agent's recycle interrupted this agent's step:\n%s", line)
	}
}

// A closed step is not a candidate. prime.go:204's isNew guard means a resumed step emits no
// second step_start, so an interrupted-then-resumed step closes normally — and a join that acted
// on closed rows would relabel completed work as lost.
func TestRecoveryLogJoinLeavesClosedRowsAlone(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Closed after a recycle")
	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
		TS:    openedAt.Add(6 * time.Minute).UTC().Format(telemetry.TimestampLayout),
		Agent: "manager", Formula: "offpath", InstanceID: "i1", StepID: "s-open", StepSeq: 1,
		StepTitle: "Closed after a recycle", SessionID: "sess-b", Verb: "done", VerbMS: 11,
		DurationMS: 360000, Status: telemetry.StatusClosed,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "manager", Trigger: triggerContextExhaustion, ObservedPct: 92,
		InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	line := lineContaining(t, renderReport(t), "Closed after a recycle")
	if strings.Contains(line, "INTERRUPTED") {
		t.Errorf("a step that closed was rendered as interrupted:\n%s", line)
	}
}

func TestInterruptedRowsRenderInJSON(t *testing.T) {
	root := setupInterruptedReport(t)
	enableTelemetryJSON(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Interrupted step")

	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "manager", Trigger: triggerContextExhaustion, ObservedPct: 93.5,
		SessionID: "sess-a", InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}
	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(2 * time.Minute)),
		Agent: "supervisor", Trigger: triggerCrash, Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	out, err := runTelemetryJSON(t, "report")
	if err != nil {
		t.Fatalf("report --json: %v", err)
	}
	row := jsonReportRowByStep(t, out, "Interrupted step")

	if got := jsonString(t, row["status"]); got != statusInterrupted {
		t.Errorf("status = %q, want %q", got, statusInterrupted)
	}
	if got := jsonString(t, row["interrupted_trigger"]); got != triggerContextExhaustion {
		t.Errorf("interrupted_trigger = %q, want %q", got, triggerContextExhaustion)
	}
	if got := strings.TrimSpace(string(row["interrupted_observed_pct"])); got != "93.5" {
		t.Errorf("interrupted_observed_pct = %s, want 93.5", got)
	}

	// No duration is fabricated. The join explains WHY a row is open; it does not close it, and
	// window.go:45-48 forbids inventing the end that would be needed to. The step was opened ten
	// minutes ago and never closed, so this must still be elapsed-so-far — and every figure that
	// could only come from a step_end must still be null.
	elapsed, err := strconv.ParseInt(strings.TrimSpace(string(row["duration_ms"])), 10, 64)
	if err != nil {
		t.Fatalf("duration_ms is not an integer: %v", err)
	}
	if elapsed < 9*60*1000 || elapsed > 11*60*1000 {
		t.Errorf("duration_ms = %d, want elapsed-so-far near 600000; an interrupted row must not "+
			"acquire a recorded duration", elapsed)
	}
	for _, key := range []string{"ctx_tokens_end", "ctx_used_pct", "cum_tokens_delta", "ctx_bound_tokens"} {
		if got := strings.TrimSpace(string(row[key])); got != "null" {
			t.Errorf("%s = %s, want null — the join invented a step_end", key, got)
		}
	}
}

// An open row whose start cannot be dated is never relabelled INTERRUPTED. Joining it would mean
// guessing which side of the recycle it fell on, and a guess here reports a running step as lost.
func TestRecoveryLogJoinSkipsAnUndatableStart(t *testing.T) {
	root := setupInterruptedReport(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)

	if err := telemetry.AppendEvent(config.TelemetryDir(root), telemetry.StepEvent{
		V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
		TS: "27 July, half past four", Agent: "manager", Formula: "offpath",
		InstanceID: "i1", StepID: "s-undatable", StepSeq: 1, StepTitle: "Undatable start",
		SessionID: "sess-a", Verb: "prime", VerbMS: 9,
	}); err != nil {
		t.Fatalf("AppendEvent: %v", err)
	}
	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "manager", Trigger: triggerContextExhaustion, ObservedPct: 91,
		SessionID: "sess-a", InstanceID: "i1", Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	line := lineContaining(t, renderReport(t), "Undatable start")
	if strings.Contains(line, statusInterrupted) {
		t.Errorf("a row whose start could not be dated was joined anyway:\n%s", line)
	}
	if !strings.Contains(line, "open") {
		t.Errorf("row is not open:\n%s", line)
	}
	if strings.Contains(line, triggerContextExhaustion) {
		t.Errorf("row carries a trigger it could not have been matched to:\n%s", line)
	}
}

// The JSON twin of the UNKNOWN rule: an occupancy-less class must emit null, never 0. A consumer
// that read 0 would rank a crashed step as the emptiest window in the run.
func TestInterruptedJSONOccupancyIsNullNotZero(t *testing.T) {
	root := setupInterruptedReport(t)
	enableTelemetryJSON(t)
	openedAt := time.Now().UTC().Add(-10 * time.Minute)
	openStepAt(t, root, openedAt, "Crashed step")

	if err := appendRecoveryLog(root, recoveryLogEntry{
		V: recoveryLogVersion, At: recoveryStamp(openedAt.Add(time.Minute)),
		Agent: "manager", Trigger: triggerCrash, Attempt: 1, Outcome: outcomeRespawned,
	}); err != nil {
		t.Fatalf("appendRecoveryLog: %v", err)
	}

	out, err := runTelemetryJSON(t, "report")
	if err != nil {
		t.Fatalf("report --json: %v", err)
	}
	row := jsonReportRowByStep(t, out, "Crashed step")
	if got := strings.TrimSpace(string(row["interrupted_observed_pct"])); got != "null" {
		t.Errorf("interrupted_observed_pct = %s, want null", got)
	}
	if got := jsonString(t, row["interrupted_trigger"]); got != triggerCrash {
		t.Errorf("interrupted_trigger = %q, want %q", got, triggerCrash)
	}
}

// jsonReportRowByStep decodes the report payload generically and returns the row whose step label
// matches. Generic rather than typed on purpose: these tests assert the spelling of ABSENCE
// (`null` vs `0`), which a typed decode into a non-pointer field would erase before the assertion.
func jsonReportRowByStep(t *testing.T, out, step string) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		Rows []map[string]json.RawMessage `json:"rows"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &doc); err != nil {
		t.Fatalf("unmarshal report payload: %v\n%s", err, out)
	}
	for _, row := range doc.Rows {
		if jsonString(t, row["step"]) == step {
			return row
		}
	}
	t.Fatalf("no row for step %q in:\n%s", step, out)
	return nil
}

func jsonString(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatalf("decode %s as a string: %v", raw, err)
	}
	return s
}
