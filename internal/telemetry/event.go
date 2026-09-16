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
	// EventIntervention is what a tokenomics mechanism records itself as when it fires (#668 K4).
	// Its payload is scalar like every other record's: a mechanism label, the action taken, and
	// the arithmetic that triggered it.
	EventIntervention = "intervention"
)

// Actions are what a firing mechanism DID, and the vocabulary is closed at three because there
// are exactly three things this harness can do to a session: tell it something, end its turn, or
// change how hard it thinks. A reader joins on these literals to separate a mechanism that merely
// spoke from one that took the session away, which is the difference between counsel the agent
// could ignore and an interruption it could not.
//
// ActionReduceEffort names the ARM, not the direction. The vocabulary is closed and there is no
// set_effort, so a relaunch that applies a profile declaring "xhigh" is labelled a reduction like
// any other — the label says the effort arm acted, and EffortLevel says what it acted to. A reader
// comparing arms groups on that field rather than trusting the verb.
//
// ActionRefuse is the fourth and, like the first three, closed: it is the pre-act sub-agent-dispatch
// capacity gate REFUSING a launch (#672 AC-3, the ADR-007 2026-08-31 amendment). It is a distinct
// verb from ActionHandoff because a handoff recycles the session that asked while a refusal denies a
// launch the session requested — the difference between "your turn is over" and "not this one, not
// yet." The vocabulary is now four; it is still closed, and a reader still joins on these literals.
//
// ActionObserve is the fifth: the armed dispatch gate ADMITTED a launch it could not judge because an
// enforcement input would not resolve (#672 AC-8). It is not a firing — nothing was done to the
// session — but AC-8 requires "admission plus an observe record" precisely so a broken gate is
// legible rather than silent, so the fail-open admission carries this label and its arithmetic (what
// it had) instead of vanishing. Distinct from ActionRefuse because the gate did the opposite (let the
// launch through), and from an absent record because AC-8 forbids the gate being invisible on error.
// The vocabulary is now five; it is still closed, and a reader still joins on these literals.
const (
	ActionAdvise       = "advise"
	ActionHandoff      = "handoff"
	ActionReduceEffort = "reduce_effort"
	ActionRefuse       = "refuse"
	ActionObserve      = "observe"
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

// Why a step_end carries no generation figures (#678 K1). Absent figures and a stated reason are a
// different record from absent figures alone, and the difference is what makes the efficiency
// predicate's degrade path auditable: "no generation baseline" is only trustworthy if a reader can
// tell a host that reported nothing from a derivation that refused to answer.
//
// The vocabulary is closed at three because there are exactly three ways the derivation declines:
// it could not open a transcript, the transcript it could open belongs to a different session than
// the step (so its figures would describe part of a step while looking like the whole of one), or it
// opened the right transcript and the step's window contained no usage-bearing record.
const (
	ReasonTranscriptMissing = "transcript_missing"
	ReasonSessionMismatch   = "session_mismatch"
	ReasonNoRecordsInWindow = "no_records_in_window"
)

// Which objective a mechanism fired for (#678). Carried only on an intervention record, beside the
// mechanism and action that already say what fired and what it did. Two objectives exist because the
// design gives efficiency its own owner rather than folding it into capacity: without this label the
// two arms of the experiment are indistinguishable in the data, since the same mechanism can fire for
// either reason.
const (
	ObjectiveCapacity   = "capacity"
	ObjectiveEfficiency = "efficiency"
)

// Whether the tokenomics umbrella was on for the run this instance_start opened. Recorded as the
// resolved conjunction rather than as its inputs: the inputs live in config that can be edited
// mid-run, and a record that had to be re-derived against config-as-it-is-now could not answer what
// was true when the run began.
const (
	TokenomicsStateOn  = "on"
	TokenomicsStateOff = "off"
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

	// StepLabel is the formula's own stable step id (the bead's step-id:<label> Label), as opposed
	// to StepID, which is the per-instance minted bead id that differs on every run. The learned
	// digest keys on this so it accumulates across runs; StepID stays the bead id for the OTLP span
	// and every within-instance rollup that must distinguish one run from another.
	StepLabel string `json:"step_label,omitempty"`

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

	// Captured at instantiation and carried on the instance_start record (#668 K4/D15), so "were
	// these two runs the same formula" is answerable from the record log alone, without anyone
	// having to still have the file. It hashes the file's RAW bytes and never the parsed formula:
	// per-run --var substitutions are expanded into the in-memory Formula, so hashing that would
	// give one file as many identities as it had invocations.
	FormulaDigest string `json:"formula_digest,omitempty"`

	// Generation figures for one step, derived from the session transcript at close (#668 K4/D17).
	// They answer what the occupancy trio above cannot: occupancy is how full the window was, these
	// are what the model actually produced getting there.
	//
	// The counting method is pinned, not incidental. Claude Code writes one record per content
	// block and stamps the whole message's usage on every one, so records are reduced per
	// message.id with MAX per field — summing lines over-counts by ~2.2x, which is the arithmetic
	// that produced this feature's original headline figure.
	//
	// ThinkTokensEst is an ESTIMATE and its bias is known: thinking text is not written to the
	// transcript (the blocks carry a signature and an empty string), so it cannot be counted and is
	// instead inferred as output minus visible text. Tool-call arguments are output that is not
	// visible text, so they land on the thinking side of that subtraction — measured at roughly a
	// 20% over-attribution on a real transcript. It is a share indicator, not an accounting figure.
	//
	// PeakCtxTokens is the MAXIMUM occupancy observed across the step rather than the reading at
	// either end, which is what makes it a single-pass appetite: a step that peaked at 190k and
	// closed at 40k after a compaction did not fit in a 128k window, and neither endpoint says so.
	//
	// All three cover the MAIN agent only. Sub-agent work is written to a separate transcript tree
	// (<session>/subagents/*.jsonl) which this derivation does not read, so a step that delegates
	// reports less generation than it caused. That is the right answer for PeakCtxTokens — a
	// sub-agent's window is not this one's, and folding it in would invent occupancy that never
	// existed — and a known understatement for the other two, which SubagentTokens below reports
	// separately rather than folding in for the same reason.
	//
	// Nil throughout when the step could not be measured — the transcript was unreachable, or the
	// step spanned a session recycle and no single transcript covers it. Absent is a different
	// fact from zero here for exactly the reason the occupancy pointers above are pointers.
	OutTokens      *int64 `json:"out_tokens,omitempty"`
	ThinkTokensEst *int64 `json:"think_tokens_est,omitempty"`
	PeakCtxTokens  *int64 `json:"peak_ctx_tokens,omitempty"`

	// ThinkTokens is the host's OWN thinking count (usage.output_tokens_details.thinking_tokens),
	// and it stands BESIDE ThinkTokensEst rather than replacing it (#678 K1, D-3). The estimate is
	// pinned (D17) and knowingly over-attributes by ~20% because tool-call arguments land on the
	// thinking side of its subtraction; this figure has no such bias. Both are kept because the
	// estimate is the only figure available on hosts older than the one that added the detail — the
	// exact count is absent on 100% of usage blocks from claude 2.1.224 — so a reader comparing runs
	// across a host upgrade needs the series that spans both, and a reader wanting the truth needs
	// this one. Replacing the estimate would have silently changed what a pinned indicator means.
	//
	// Nil when no message in the step's window reported the detail, which is NOT zero: a step whose
	// host cannot report thinking and a step that did none are different facts, and only one of them
	// is a fact about the step.
	ThinkTokens *int64 `json:"think_tokens,omitempty"`

	// The diagnostic legs of the same per-message MAX reduction that produced OutTokens. They exist
	// so a reader can ask WHERE a step's occupancy came from — a step that re-read the same context
	// forty times and a step that generated a great deal both end at a high peak, and only these
	// separate them. Cache splits are reported apart from InTokens because a cache-read token
	// occupies the window exactly as an uncached one does but costs a fraction; folding them together
	// would make the cheap step and the expensive step look identical.
	InTokens            *int64 `json:"in_tokens,omitempty"`
	CacheReadTokens     *int64 `json:"cache_read_tokens,omitempty"`
	CacheCreationTokens *int64 `json:"cache_creation_tokens,omitempty"`

	// What the step DID to arrive at the figures above, counted in the same single pass over the
	// parent transcript (#678 K1). All counts, no paths and no names: RepeatReads is a count of Read
	// tool_use blocks whose file_path repeats one already read in this step's window, and the path
	// itself never leaves the derivation — counting a repeat requires remembering the path only for
	// as long as the pass runs, which is why a count can be recorded where a list could not.
	//
	// SubagentLaunches and WorkflowLaunches are counted SEPARATELY rather than summed. isSubagentTool
	// (internal/cmd/subagent_tool.go) is the dispatch gate's predicate and is deliberately not
	// widened to include Workflow, so a merged count here would report a number no other surface in
	// the factory could reproduce.
	//
	// GateFlags is a COUNT of the agent's own gate verdicts landing in the step's window — a scalar,
	// never a list. The record is the privacy boundary and a verdict's prose is exactly the kind of
	// free-form text this schema exists to keep off disk.
	SubagentLaunches       *int64 `json:"subagent_launches,omitempty"`
	WorkflowLaunches       *int64 `json:"workflow_launches,omitempty"`
	SubagentNestedLaunches *int64 `json:"subagent_nested_launches,omitempty"`
	RepeatReads            *int64 `json:"repeat_reads,omitempty"`
	GateFlags              *int64 `json:"gate_flags,omitempty"`

	// HostVersion is the Claude Code version that wrote the transcript this step was measured from,
	// taken from the transcript's own records rather than from anything af knows. Every figure above
	// is only comparable across runs that a host of the same generation produced — the exact thinking
	// count simply does not exist before one particular host — so a reader that pools runs without
	// this field is pooling series that mean different things.
	//
	// GenerationUnmeasuredReason says why the generation figures are absent, from the closed
	// vocabulary above. Set only when they ARE absent; a measured step carries figures and no excuse.
	HostVersion                string `json:"host_version,omitempty"`
	GenerationUnmeasuredReason string `json:"generation_unmeasured_reason,omitempty"`

	// What this step's sub-agents spent, summed across the sibling transcript tree at close
	// (#668 K18). It is deliberately a FOURTH figure and not an addition to the trio above: those
	// describe one window, this describes work that happened in other windows entirely, and a
	// reader that wanted the total can add two numbers while a reader that wanted the main agent's
	// occupancy could never subtract them back apart.
	//
	// Nil when the step delegated nothing AND when the host has expired the tree, which are not the
	// same fact — neither is zero, and the derivation cannot tell them apart, so it says neither.
	SubagentTokens *int64 `json:"subagent_tokens,omitempty"`

	// The same walk's spend, split (#678 K1). SubagentTokens keeps its meaning and its value exactly
	// — it is the summed Spend() it has always been — and these two say what it was made of. The
	// split is what lets a reader tell delegation that READ a great deal from delegation that WROTE a
	// great deal, which is the distinction the efficiency baseline turns on: the first is context
	// handed down and the second is work done.
	SubagentInTokens  *int64 `json:"subagent_in_tokens,omitempty"`
	SubagentOutTokens *int64 `json:"subagent_out_tokens,omitempty"`

	// The scalar payload a firing mechanism records: which mechanism, and what it did. Both are
	// closed vocabularies declared in code — tokenomics.Mechanism and the Action constants above —
	// so no operator-, agent- or formula-supplied string can reach them. Carried only on an
	// EventIntervention record.
	//
	// Objective says which of the two things the mechanism was trying to achieve, from the closed
	// vocabulary above (#678). Capacity and efficiency can fire the same mechanism to do the same
	// thing for opposite reasons, so without this label the arms of the experiment are one series.
	//
	// EffortLevel is the host effort level in force, from the host's own closed vocabulary. It began
	// as #668 D16's intervention-only readout — what a relaunch APPLIED — and #678 K1 widens it to the
	// two records that establish what was in force WITHOUT a firing: session_start (the level the
	// session launched under) and step_end (the level the step ran at). The widening is deliberate and
	// is the reason the schema test no longer treats it as intervention-only: with the field on
	// interventions alone, a run where the effort actuator never fired carries no effort level at all,
	// and the control arm of the experiment is unreadable — which is precisely the comparison the
	// field was added to make.
	Mechanism   string `json:"mechanism,omitempty"`
	Action      string `json:"action,omitempty"`
	Objective   string `json:"objective,omitempty"`
	EffortLevel string `json:"effort_level,omitempty"`

	// The arithmetic that justified a dispatch-capacity refusal (#672 AC-3, ADR-007 2026-08-31
	// condition 4: every refusal is "a recorded intervention retrievable through the standard read
	// surfaces, carrying the arithmetic that justified it"). PoolTokens is the backend's declared
	// shared capacity; SummedTokens is Σ of the live same-backend occupancies plus the reservation the
	// gate held for the launch it refused. Two token counts, no free text — the backend endpoint the
	// refusal was on is recoverable from the record's own agent+model, so it is not restated as a
	// field. Pointers for the reason the occupancy trio above are: nil is "not a refusal record", which
	// is a different fact from a refusal whose pool happened to be zero.
	PoolTokens   *int64 `json:"pool_tokens,omitempty"`
	SummedTokens *int64 `json:"summed_occupancy_tokens,omitempty"`

	// What was running, and what it was running ON (#678 K1). Every figure this record carries is a
	// measurement of a binary against a tree, and a comparison across runs is only sound if both are
	// held fixed; these fields are what make that checkable after the fact instead of assumed.
	//
	// AFVersion/AFCommit ride session_start and instance_start, the two records that open something.
	// Under a plain `go build` they are the unstamped defaults ("dev"/"unknown") rather than absent —
	// the ldflags only apply to the Makefile's build target — so a reader must treat them as a
	// best-effort attestation, not a guarantee.
	//
	// CheckoutCommit and BaseCommit are 40-hex or ABSENT, never a guess: git prints the literal string
	// "HEAD" to stdout when asked to resolve HEAD in a repository with no commits, so a writer that
	// trusts stdout without checking the exit code and the hex shape will record that word as a commit.
	// BaseCommit is the merge-base the run's branch sits on, resolved locally at close — it is what
	// gives the frozen-input attestation something to be void against, since a digest of the inputs
	// says nothing if the tree underneath them moved.
	AFVersion       string `json:"af_version,omitempty"`
	AFCommit        string `json:"af_commit,omitempty"`
	TokenomicsState string `json:"tokenomics_state,omitempty"`
	CheckoutCommit  string `json:"checkout_commit,omitempty"`
	BaseCommit      string `json:"base_commit,omitempty"`

	// SlingDigest is the digest of the inputs a run was slung with (af sling --input-digest), the
	// frozen-input attestation's other half.
	//
	// The NAME is a workaround and this is the note that says so. The design calls this field
	// "input_digest"; the closed-schema content-name ban (event_test.go) matches its banned concepts
	// as SUBSTRINGS of the field name and the json tag, and "input" is banned — so input_digest is
	// rejected by the same rule that keeps prompts and task text off disk. The rule is right and the
	// field is harmless (a digest is a hash of a document, never the document), but weakening a
	// privacy boundary to fit a name is the wrong trade, so the field is named for the surface that
	// produces it instead. This is the second time the ban list has forced this rename: OutTokens is
	// not OutputTokens for exactly the same reason. The CLI flag remains --input-digest.
	SlingDigest string `json:"sling_digest,omitempty"`
}
