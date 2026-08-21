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
//
// Since #622 some measurements are POINTERS to scalars, and that widening is deliberate. A step
// nobody measured and a step that consumed nothing must not produce the same record: with a plain
// int the two collapse into 0, and a caller reading the log would have to invent a rule for
// telling them apart. A pointer to a scalar is still exactly one number on the wire — it carries
// no more content than the int it points at — so what the boundary forbids is unchanged. What it
// still forbids: a map, a slice, an interface, a nested struct, or a pointer to any of those, all
// of which are carriers for arbitrary content. The closed-schema test dereferences exactly one
// level for this reason, and admits only the pointee kinds a measurement can be.
//
// Every added field is `omitempty`, and that is not a formatting choice either. It is what keeps
// "absent" absent rather than an explicit null, and it is what keeps recordDigest (store.go:98)
// byte-stable: that digest is the export cursor's bookmark, persisted by whichever binary wrote it
// last, so a record written before these fields existed has to re-marshal to the same bytes after
// an upgrade or the entire retained backlog re-exports once.
//
// What is still NOT here: any stored verdict. Whether a step went over its bound is derived at
// read time from these raw figures, because a figure cannot lie and a verdict computed under one
// version of the rules can.
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

	// Context occupancy at the moment this record was written: the window the agent was
	// carrying, how full it was, and the monotonic transcript counter behind it. Carried on a
	// step_start record and repeated on step_end with the values at close, so a step's window
	// can be read from either end. Nil throughout when nothing measured this step — absent is
	// a fact worth recording honestly, and it is not the same fact as zero.
	//
	// CtxObservedAt is the observing snapshot's own written_at, so a reader can see how far the
	// measurement lags the record instead of assuming they are simultaneous. It is the one
	// string here, and it is a fixed-format machine timestamp of the same class as TS.
	CtxUsedPct     *float64 `json:"ctx_used_pct,omitempty"`
	CtxTokensUsed  *int64   `json:"ctx_tokens_used,omitempty"`
	CtxTokensTotal *int64   `json:"ctx_tokens_total,omitempty"`
	CtxObservedAt  string   `json:"ctx_observed_at,omitempty"`
	CumTokens      *int64   `json:"cum_tokens,omitempty"`

	// Present only on a step_end record. Omitted rather than zero-valued elsewhere, so a
	// reader can tell "this step took no time" from "this record is not about a step ending".
	//
	// CtxTokensStart echoes the step_start figure and CumTokensDelta is the difference across
	// the step — the direct answer to "what did this step cost", immune to compaction because
	// cum_tokens never decreases. CtxBoundTokens is the bound that was IN EFFECT at close, so
	// history stays interpretable after an operator changes it.
	DurationMS     int    `json:"duration_ms,omitempty"`
	Status         string `json:"status,omitempty"`
	CtxTokensStart *int64 `json:"ctx_tokens_start,omitempty"`
	CumTokensDelta *int64 `json:"cum_tokens_delta,omitempty"`
	CtxBoundTokens int64  `json:"ctx_bound_tokens,omitempty"`
}
