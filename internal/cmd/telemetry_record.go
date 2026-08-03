package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

const (
	// exportBatchSize bounds one af done's export. The verb runs on the lifecycle hot path, so
	// a factory that has been offline for a week must not turn its next close into an
	// unbounded upload; the backlog drains over the closes that follow.
	exportBatchSize = 200

	// exportDrainRounds bounds the manual full drain behind `af telemetry report --export`.
	// Each round strictly advances the export cursor or stops, so this is a backstop against a
	// cursor that cannot move rather than an expected limit.
	exportDrainRounds = 100
)

// verbTelemetry is what a lifecycle verb knows about itself the moment it starts: which verb it
// is, when it began, whether the factory gate is on, and — for the frames that cannot resolve
// it themselves — which agent it is acting for.
//
// It travels in the context rather than in parameters because the functions that own the
// emission sites carry between fifteen and thirty-six test call sites each. Threading a new
// argument through them would put this feature behind a mechanical rewrite of tests that have
// nothing to do with telemetry. The zero value means "no clock, gate off", which is exactly
// what a test calling one of those functions directly should observe.
type verbTelemetry struct {
	verb    string
	agent   string
	start   time.Time
	enabled bool
}

type verbTelemetryKey struct{}

func withVerbTelemetry(ctx context.Context, vt verbTelemetry) context.Context {
	if ctx == nil {
		// cobra's Command.Context() is a bare field read, not a Background() default, so a
		// command built directly in a test hands us nil — and WithValue would panic on it.
		ctx = context.Background()
	}
	return context.WithValue(ctx, verbTelemetryKey{}, vt)
}

func verbTelemetryFrom(ctx context.Context) verbTelemetry {
	if ctx == nil {
		return verbTelemetry{}
	}
	vt, _ := ctx.Value(verbTelemetryKey{}).(verbTelemetry)
	return vt
}

// elapsedMS is how long this verb had been running when the record was assembled — not how long
// the whole invocation took. An append-only log cannot know its own future, so a step_end's
// verb_ms covers af done up to the close and excludes the gate handling, mail and teardown that
// follow it. That is the useful half anyway: it is the work that stood between the agent asking
// to close a step and the step being closed.
//
// A zero start means nobody captured one, which reads as 0 rather than as the fifty-odd years
// since the zero instant.
func (v verbTelemetry) elapsedMS() int {
	if v.start.IsZero() {
		return 0
	}
	ms := int(time.Since(v.start) / time.Millisecond)
	if ms < 0 {
		return 0
	}
	return ms
}

// telemetryTimestamp stamps a record at millisecond precision. The surrounding lifecycle code
// stamps RFC3339 because humans read those files; this one is read by a half-open window join,
// which at second precision cannot separate an event just before a step closed from one just
// after.
func telemetryTimestamp() string {
	return time.Now().UTC().Format(telemetry.TimestampLayout)
}

// telemetryFormulaName strips the prefix the formula-instance bead title carries, so a record
// written by af sling (which holds the parsed formula's own name) and one written by af prime
// or af done (which read the bead title) name the same formula with the same string.
func telemetryFormulaName(name string) string {
	return strings.TrimPrefix(name, "Formula: ")
}

// readRuntimeSessionID reads the session id af prime persisted, or "" when there is none.
// Deliberately not getSessionID: that one fabricates a "<role>-<pid>" value when the file is
// absent, which would stamp the pid of whichever af process is writing onto a field meant to
// identify the agent's own session.
func readRuntimeSessionID(workDir string) string {
	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "session_id"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// resolveRecordModel answers which model a record carries and how that was decided, following
// the launch resolver's precedence: an explicit flag, then the per-launch marker, then
// models.json. Knowing WHICH of those decided it is the point — a report that cannot separate a
// deliberate one-off override from the configured default cannot support the "change the
// per-agent model and nothing else" loop this feature exists to serve.
func resolveRecordModel(factoryRoot, workDir, agent, cliModel string) (string, string) {
	if cliModel != "" {
		return cliModel, telemetry.ModelSourceOverride
	}
	if marker := readModelOverride(workDir); marker != "" {
		return marker, telemetry.ModelSourceOverride
	}
	cfg, err := config.LoadModelsConfig(factoryRoot)
	if err != nil || cfg == nil {
		return "", telemetry.ModelSourceUnknown
	}
	if name := cfg.Agents[agent]; name != "" {
		return name, telemetry.ModelSourceModelsJSON
	}
	if cfg.Default != "" {
		return cfg.Default, telemetry.ModelSourceModelsJSON
	}
	return "", telemetry.ModelSourceUnknown
}

// telemetryFactoryID names this factory. Several factories can share a host and a backend, and
// without it their data mingles into one indistinguishable stream. factory.json's name is the
// operator's own word for the factory; the directory name is the honest fallback when there
// isn't one, and is never fabricated from anything else.
func telemetryFactoryID(factoryRoot string) string {
	if cfg, err := config.LoadFactoryConfig(config.FactoryConfigPath(factoryRoot)); err == nil && cfg.Name != "" {
		return cfg.Name
	}
	return filepath.Base(factoryRoot)
}

// telemetryIdentity derives the identity that af's own records and a launched session's native
// signals must share for the two to ever join, together with how the model was decided.
//
// This is the single derivation. Every record goes through it, so the correlation keys a record
// carries and the ones a session is launched with cannot drift apart into two implementations
// of the same idea.
//
// instanceID is passed in rather than always read from disk because af sling emits its first
// record BEFORE it persists .runtime/hooked_formula: reading the marker there would report the
// previous run's instance. An empty argument means "no in-process value", and only then does
// the marker answer.
func telemetryIdentity(factoryRoot, workDir, agent, instanceID, cliModel string) (telemetry.CorrelationKeys, string) {
	if instanceID == "" {
		instanceID = readHookedFormulaID(workDir)
	}
	model, modelSource := resolveRecordModel(factoryRoot, workDir, agent, cliModel)
	return telemetry.CorrelationKeys{
		FactoryID:       telemetryFactoryID(factoryRoot),
		Agent:           agent,
		WorktreeID:      readWorktreeID(workDir),
		FormulaInstance: instanceID,
		ModelProfile:    model,
	}, modelSource
}

// telemetryRecordFor assembles the fields every record carries regardless of kind. Each call
// site sets its own event kind and the fields only it can know, so the kind stays visible at
// the site where the decision to record is made.
func telemetryRecordFor(ctx context.Context, factoryRoot, workDir, agent, instanceID, cliModel string) telemetry.StepEvent {
	vt := verbTelemetryFrom(ctx)
	keys, modelSource := telemetryIdentity(factoryRoot, workDir, agent, instanceID, cliModel)
	return telemetry.StepEvent{
		V:           telemetry.SchemaVersion,
		TS:          telemetryTimestamp(),
		Agent:       keys.Agent,
		WorktreeID:  keys.WorktreeID,
		InstanceID:  keys.FormulaInstance,
		SessionID:   readRuntimeSessionID(workDir),
		Model:       keys.ModelProfile,
		ModelSource: modelSource,
		Verb:        vt.verb,
		VerbMS:      vt.elapsedMS(),
	}
}

// appendTelemetryRecord writes one record and refuses to let that failure reach the verb.
//
// The store returns its errors rather than swallowing them, deliberately, and leaves this
// decision to the caller. On a lifecycle verb the decision is settled: observability never
// blocks work. But the error is not dropped either — a write that failed silently is
// indistinguishable from a step that never ran.
func appendTelemetryRecord(factoryRoot string, ev telemetry.StepEvent) {
	if err := telemetry.AppendEvent(config.TelemetryDir(factoryRoot), ev); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record %s telemetry: %v\n", ev.Event, err)
	}
}

// telemetryStepSpan answers the two things a closing record cannot know from its own frame: how
// long the step ran, and where it sat in the formula.
//
// Neither has another source. The primed marker af prime leaves behind carries no timestamp,
// and the step sequence has exactly one derivation in this repository — in af prime, which is
// why it is read back from the record af prime wrote rather than derived a second time here.
// A missing start (telemetry switched on mid-formula) yields zeroes, which is honest: the
// encoder then produces a zero-length span rather than a fabricated duration.
//
// This reads the agent's log on the lifecycle path, which is a real cost — bounded by the log's
// rotation cap, and narrowed to one instance by the filter, but a parse nonetheless. It is paid
// deliberately: the alternative is a second derivation of step_seq that could disagree with af
// prime's, and two numbers that disagree are worse than one number that costs a file read.
func telemetryStepSpan(factoryRoot, agent, instanceID, stepID, endTS string) (seq, durationMS int) {
	records, _, err := telemetry.ReadEvents(config.TelemetryDir(factoryRoot),
		telemetry.Filter{Agent: agent, InstanceID: instanceID})
	if err != nil {
		return 0, 0
	}

	var start *telemetry.StepEvent
	for i := range records {
		r := &records[i]
		if r.Event != telemetry.EventStepStart || r.StepID != stepID {
			continue
		}
		// Earliest wins, matching the window join. A re-prime after a handoff records the
		// start again, and taking the later one would shorten the very step being measured.
		if start == nil || r.TS < start.TS {
			start = r
		}
	}
	if start == nil {
		return 0, 0
	}

	began, beganErr := time.Parse(telemetry.TimestampLayout, start.TS)
	ended, endedErr := time.Parse(telemetry.TimestampLayout, endTS)
	if beganErr != nil || endedErr != nil || ended.Before(began) {
		return start.StepSeq, 0
	}
	return start.StepSeq, int(ended.Sub(began) / time.Millisecond)
}

// drainTelemetryBounded exports what it can within one af done and never lets the outcome reach
// the verb. Three silences here are deliberate:
//
//   - No configured endpoint means the factory is running the local-report-only path, which is
//     a supported end state, not a misconfiguration. Exporting anyway would print a warning on
//     every single close.
//   - Nothing exportable is not a failure. Every non-final af done leaves a step open, and an
//     open step produces no span, so the drain has nothing to carry and says nothing.
//   - A real failure warns exactly once and the verb still succeeds. Delivery is at-least-once
//     by design: the cursor did not move, so the same records are offered again next time.
func drainTelemetryBounded(factoryRoot, agent string) {
	cfg, err := config.LoadTelemetryConfig(factoryRoot)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: telemetry config invalid, skipping export: %v\n", err)
		return
	}
	if cfg.Endpoint == "" {
		return
	}
	// The export path owns the dereference: telemetry.Export is handed no factory root and
	// refuses a reference it cannot vet, so a config carrying one would be rejected rather than
	// sent. Failures warn and return — the verb still succeeds, which is the same posture as
	// every other failure here.
	resolved, err := derefTelemetryHeaders(factoryRoot, *cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: telemetry export skipped: %v\n", err)
		return
	}
	if _, err := telemetry.Drain(config.TelemetryDir(factoryRoot), resolved, agent, exportBatchSize); err != nil {
		fmt.Fprintf(os.Stderr, "warning: telemetry export failed: %v\n", err)
	}
}

// drainTelemetryFully empties one agent's backlog, batch after batch. This runs only from the
// operator's explicit `af telemetry report --export`, never from a hook path, which is why it
// is allowed to take as long as the backlog needs.
func drainTelemetryFully(factoryRoot, agent string, cfg config.TelemetryConfig) (int, error) {
	sent := 0
	for round := 0; round < exportDrainRounds; round++ {
		result, err := telemetry.Drain(config.TelemetryDir(factoryRoot), cfg, agent, exportBatchSize)
		sent += result.Sent
		if err != nil {
			return sent, err
		}
		if result.Sent == 0 {
			return sent, nil
		}
	}
	return sent, nil
}
