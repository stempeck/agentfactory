package telemetry

// SchemaVersion is stamped on every record. Evolution is additive and readers ignore fields
// they do not know, so a newer af can write a key an older one has never heard of without
// breaking it — which is why nothing in this package turns on strict field checking.
const SchemaVersion = 1

// Event kinds. The literal values are part of the on-disk contract and are asserted
// separately from the identifier names, because renaming a constant must never be able to
// move the bytes that reach a file or a backend.
const (
	EventInstanceStart = "instance_start"
	EventStepStart     = "step_start"
	EventStepEnd       = "step_end"
	EventInstanceEnd   = "instance_end"
	EventSessionStart  = "session_start"
)

// Status values, carried only on a step_end record.
const (
	StatusClosed      = "closed"
	StatusSkipped     = "skipped"
	StatusGateWaiting = "gate-waiting"
)

// ModelSource records how the model on a record was decided, so a report can distinguish a
// deliberate per-run override from the configured default and from not knowing at all.
const (
	ModelSourceOverride   = "override"
	ModelSourceModelsJSON = "models_json"
	ModelSourceUnknown    = "unknown"
)

// TimestampLayout is millisecond precision on purpose. Step attribution is a half-open window
// join over discrete events, and second precision cannot separate an event that arrived just
// before a step closed from one that arrived just after — the boundary rule would then be
// decided by rounding. The repository's other timestamp writers use second precision because
// they are read by humans; this one is read by a join.
const TimestampLayout = "2006-01-02T15:04:05.000Z"

// StepEvent is one line of an agent's record log, and it is the privacy boundary of the whole
// feature. There is no redaction stage between here and disk, and none between disk and a
// backend, so whatever this struct can hold is whatever eventually ships. Every field is a
// scalar identifier, label, or measurement.
//
// What it deliberately cannot carry: the free-form text an operator supplies when dispatching
// work. That text is embedded in bead records and can contain whole issue bodies. Step titles
// are formula metadata and are safe; the accompanying prose is not, and there is no field for
// it. A future field of map or slice type would reopen this, which is why the schema is
// asserted closed by type as well as by name.
type StepEvent struct {
	V     int    `json:"v"`
	Event string `json:"event"`
	TS    string `json:"ts"`

	Agent      string `json:"agent"`
	WorktreeID string `json:"worktree_id"`

	Formula    string `json:"formula"`
	InstanceID string `json:"instance_id"`

	StepID    string `json:"step_id"`
	StepSeq   int    `json:"step_seq"`
	StepTitle string `json:"step_title"`

	SessionID string `json:"session_id"`

	Model       string `json:"model"`
	ModelSource string `json:"model_source"`

	// Verb and VerbMS answer the second half of the operator's question — not just which step
	// is slow, but which af command is. VerbMS is that invocation's own wall clock, measured
	// from command entry to the moment the record is written.
	Verb   string `json:"verb"`
	VerbMS int    `json:"verb_ms"`

	// Present only on a step_end record. Omitted rather than zero-valued elsewhere, so a
	// reader can tell "this step took no time" from "this record is not about a step ending".
	DurationMS int    `json:"duration_ms,omitempty"`
	Status     string `json:"status,omitempty"`
}
