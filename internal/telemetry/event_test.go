package telemetry

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// pinnedWireLiterals is the on-disk value of every wire constant, spelled out separately from the
// constants themselves so that renaming an identifier cannot move the bytes that reach a file or
// a backend.
var pinnedWireLiterals = map[string]string{
	"EventInstanceStart":    "instance_start",
	"EventStepStart":        "step_start",
	"EventStepEnd":          "step_end",
	"EventInstanceEnd":      "instance_end",
	"EventSessionStart":     "session_start",
	"EventIntervention":     "intervention",
	"StatusClosed":          "closed",
	"StatusSkipped":         "skipped",
	"StatusGateWaiting":     "gate-waiting",
	"ModelSourceOverride":   "override",
	"ModelSourceModelsJSON": "models_json",
	"ModelSourceUnknown":    "unknown",
	// #668 Phase 5 / #672: what a firing mechanism DID. A closed FIVE-member vocabulary, pinned for
	// the same reason the event kinds are — a reader joins on these literals. ActionRefuse (#672 AC-3)
	// is the pre-act dispatch-capacity refusal; ActionObserve (#672 AC-8) is the armed gate's
	// visible fail-open admission when an enforcement input would not resolve. The vocabulary grew by
	// two over #668's three and is still closed.
	"ActionAdvise":       "advise",
	"ActionHandoff":      "handoff",
	"ActionReduceEffort": "reduce_effort",
	"ActionRefuse":       "refuse",
	"ActionObserve":      "observe",
	// #678 K1: three closed vocabularies the new record fields draw from. Pinned for the reason
	// every literal above is — the efficiency predicate joins on "why was this unmeasured", the
	// experiment groups on which objective fired, and both are string comparisons against these
	// bytes rather than against the identifiers.
	"ReasonTranscriptMissing": "transcript_missing",
	"ReasonSessionMismatch":   "session_mismatch",
	"ReasonNoRecordsInWindow": "no_records_in_window",
	"ObjectiveCapacity":       "capacity",
	"ObjectiveEfficiency":     "efficiency",
	"TokenomicsStateOn":       "on",
	"TokenomicsStateOff":      "off",
}

// declaredWireConstants returns the names of the exported untyped string constants declared in
// the named file, which is the set whose literal values are part of the on-disk contract.
// SchemaVersion and TimestampLayout are excluded: the first is not a string, and the second is a
// format the records are written WITH rather than a value written INTO one.
func declaredWireConstants(t *testing.T, file string) []string {
	t.Helper()
	parsed, err := parser.ParseFile(token.NewFileSet(), file, nil, 0)
	if err != nil {
		t.Fatalf("parsing %s: %v", file, err)
	}
	var names []string
	for _, decl := range parsed.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, ident := range vs.Names {
				if i >= len(vs.Values) {
					continue
				}
				lit, ok := vs.Values[i].(*ast.BasicLit)
				if !ok || lit.Kind != token.STRING || ident.Name == "TimestampLayout" {
					continue
				}
				names = append(names, ident.Name)
			}
		}
	}
	return names
}

// allowlist is the closed field set the record schema is permitted to carry. It is spelled out
// here rather than derived from the struct, because a test that reads its expectation off the
// thing under test cannot detect the thing under test growing a field.
var allowlist = []string{
	"v", "event", "ts", "agent", "worktree_id", "formula", "instance_id",
	"step_id", "step_seq", "step_title", "step_label", "session_id", "model", "model_source",
	"verb", "verb_ms", "duration_ms", "status",
	// #622 C2: context occupancy and consumption. Measurements, not content — see the
	// "every field is a scalar" subtest for why a pointer to one is still a scalar on the wire.
	"ctx_used_pct", "ctx_tokens_used", "ctx_tokens_total", "ctx_observed_at", "cum_tokens",
	"ctx_tokens_start", "cum_tokens_delta", "ctx_bound_tokens",
	// #668 K4: formula content identity, and what a step generated getting to the occupancy
	// above. A digest is a hash of a document, not the document; the rest are token counts.
	"formula_digest", "out_tokens", "think_tokens_est", "peak_ctx_tokens",
	// #668 Phase 5: the scalar payload event.go:17-21 reserved for a firing mechanism. Three
	// closed-vocabulary labels and one token count — no free text can reach any of them, because
	// every value a writer may put there is a constant this package or internal/tokenomics declares.
	"mechanism", "action", "effort_level", "subagent_tokens",
	// #672 AC-3: the arithmetic a dispatch-capacity refusal carries. Two token counts, no free text.
	"pool_tokens", "summed_occupancy_tokens",
	// #678 K1: the measurement floor. The host's own thinking count beside the pinned estimate, the
	// diagnostic legs of the same reduction, and counts of what the step DID — every one a number.
	// repeat_reads and gate_flags are counts precisely because the things they count (file paths,
	// gate verdicts) are content this record must never carry.
	"think_tokens", "in_tokens", "cache_read_tokens", "cache_creation_tokens",
	"subagent_launches", "workflow_launches", "subagent_nested_launches", "repeat_reads", "gate_flags",
	"subagent_in_tokens", "subagent_out_tokens",
	// #678 K1: what was measured, and why it was not. Both labels, both from closed vocabularies —
	// host_version is the host's own version string, which is a version and not prose.
	"host_version", "generation_unmeasured_reason",
	// #678: which objective a firing served, so the two arms of the experiment are separable.
	"objective",
	// #678 K1: what ran, and what it ran on. Two build labels, one resolved state, two commit ids and
	// one digest. sling_digest is the design's "input_digest" renamed — the content-name subtest below
	// matches "input" as a substring and rejects that name; see the field's comment in event.go.
	"af_version", "af_commit", "tokenomics_state", "checkout_commit", "base_commit", "sling_digest",
}

// contentBearingNames are concepts that must never name a field. The record is the privacy
// boundary and there is no downstream redaction step, so a field able to carry free-form text
// is a leak by construction rather than by accident. step_title and status must survive this
// list, which is why it matches whole concepts and not the substring "t".
var contentBearingNames = []string{
	"description", "desc", "vars", "task", "prompt", "body", "content", "text",
	"message", "msg", "args", "argv", "input", "output", "payload", "notes", "detail",
}

// scalarSchemaViolation reports why a field type is outside the closed schema, or "" if it is
// inside it. Exactly ONE level of pointer is dereferenced, and only to reach the pointee's kind.
// #622 C2 needs *int64/*float64 so that "nobody measured this step" and "this step consumed
// nothing" stay different records — a plain int cannot express that, and a sentinel like -1 is a
// lie every reader would have to know about. A pointer to a scalar is still one scalar value on the
// wire, so the boundary is unchanged: what a pointer must not be allowed to smuggle in is a map,
// slice, interface or nested struct, and dereferencing that one level is what lets this keep
// rejecting those instead of stopping at reflect.Ptr and never looking past it.
func scalarSchemaViolation(t reflect.Type) string {
	kind := t.Kind()
	if kind == reflect.Ptr {
		if t.Elem().Kind() == reflect.Ptr {
			return "is a pointer to a pointer; the schema admits one level of indirection to one scalar, and nothing else"
		}
		kind = t.Elem().Kind()
	}
	switch kind {
	case reflect.String, reflect.Int, reflect.Int64, reflect.Float64:
		return ""
	default:
		return "is a " + t.Kind().String() + "; the schema admits only scalar strings, integers and " +
			"floats (or a single pointer to one), because a map, slice, interface or nested struct " +
			"is a carrier for arbitrary content"
	}
}

func TestStepEventFieldAllowlist(t *testing.T) {
	// This test fails against: a struct that grows a map or slice field (the exact hole
	// through which formula vars, step descriptions and task text arrive — and one that
	// passes every name-based check); a field tagged with a content-bearing name; a field
	// hidden from the marshaller with a dash tag but still holding content in memory; and an
	// event-kind constant whose literal string value drifts from what downstream phases grep
	// for.

	t.Run("the serialized key set is exactly the allowlist", func(t *testing.T) {
		// Marshal-then-inspect rather than reflect-over-declared-fields: this asserts the
		// bytes that actually reach disk, so it also survives a custom marshaller, an
		// embedded struct, or an inlined map that a declaration walk would miss.
		raw, err := json.Marshal(fullyPopulatedEvent())
		if err != nil {
			t.Fatalf("marshalling StepEvent: %v", err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decoding marshalled StepEvent: %v", err)
		}
		want := map[string]bool{}
		for _, k := range allowlist {
			want[k] = true
		}
		for k := range got {
			if !want[k] {
				t.Errorf("serialized record carries key %q, which is not on the allowlist", k)
			}
		}
		for k := range want {
			if _, ok := got[k]; !ok {
				t.Errorf("allowlisted key %q is absent from the serialized record", k)
			}
		}
	})

	t.Run("every field is a scalar", func(t *testing.T) {
		rt := reflect.TypeOf(StepEvent{})
		if rt.NumField() != len(allowlist) {
			t.Errorf("StepEvent declares %d fields, allowlist has %d", rt.NumField(), len(allowlist))
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				t.Errorf("field %s is unexported; an unexported field never reaches the JSON output "+
					"but still holds whatever was put in it", f.Name)
			}
			if reason := scalarSchemaViolation(f.Type); reason != "" {
				t.Errorf("field %s %s", f.Name, reason)
			}
		}
	})

	t.Run("the scalar rule still rejects every carrier type", func(t *testing.T) {
		// The rule above got LOOSER in #622 C2 (it now dereferences a pointer), so the half that
		// matters is what it still refuses. Asserting that against synthetic types is what keeps
		// the loosening honest: a rewrite that admitted reflect.Ptr wholesale, or that stopped at
		// the pointer without inspecting the pointee, would pass the scan over StepEvent — every
		// real field is fine — and go red only here.
		type nested struct{ Free string }
		for _, tc := range []struct {
			what string
			typ  reflect.Type
		}{
			{"map", reflect.TypeOf(map[string]string(nil))},
			{"slice", reflect.TypeOf([]string(nil))},
			{"interface", reflect.TypeOf((*any)(nil)).Elem()},
			{"struct", reflect.TypeOf(nested{})},
			{"pointer to map", reflect.TypeOf((*map[string]string)(nil))},
			{"pointer to slice", reflect.TypeOf((*[]string)(nil))},
			{"pointer to struct", reflect.TypeOf((*nested)(nil))},
			{"pointer to pointer", reflect.TypeOf((**int64)(nil))},
			{"array", reflect.TypeOf([2]string{})},
			{"json.RawMessage", reflect.TypeOf(json.RawMessage(nil))},
		} {
			if scalarSchemaViolation(tc.typ) == "" {
				t.Errorf("a %s field would be admitted by the schema rule; it is a carrier for "+
					"arbitrary content and must not be", tc.what)
			}
		}
		for _, tc := range []struct {
			what string
			typ  reflect.Type
		}{
			{"string", reflect.TypeOf("")},
			{"int", reflect.TypeOf(0)},
			{"int64", reflect.TypeOf(int64(0))},
			{"pointer to int64", reflect.TypeOf((*int64)(nil))},
			{"pointer to float64", reflect.TypeOf((*float64)(nil))},
		} {
			if reason := scalarSchemaViolation(tc.typ); reason != "" {
				t.Errorf("a %s field must be admitted, got %s", tc.what, reason)
			}
		}
	})

	t.Run("every field has an allowlisted json tag and no escape hatch", func(t *testing.T) {
		want := map[string]bool{}
		for _, k := range allowlist {
			want[k] = true
		}
		rt := reflect.TypeOf(StepEvent{})
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag, ok := f.Tag.Lookup("json")
			if !ok || tag == "" {
				t.Errorf("field %s has no json tag; its key would default to the Go name", f.Name)
				continue
			}
			name := strings.Split(tag, ",")[0]
			if name == "-" {
				t.Errorf("field %s is tagged to be skipped; a field excluded from the output but "+
					"present on the struct is outside the allowlist the schema is supposed to be", f.Name)
				continue
			}
			if !want[name] {
				t.Errorf("field %s has json tag %q, which is not on the allowlist", f.Name, name)
			}
		}
	})

	t.Run("no field name names a content-bearing concept", func(t *testing.T) {
		rt := reflect.TypeOf(StepEvent{})
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := strings.Split(f.Tag.Get("json"), ",")[0]
			for _, banned := range contentBearingNames {
				if strings.Contains(strings.ToLower(f.Name), banned) {
					t.Errorf("field %s names %q", f.Name, banned)
				}
				if strings.Contains(strings.ToLower(tag), banned) {
					t.Errorf("json tag %q names %q", tag, banned)
				}
			}
		}
	})

	t.Run("event kinds and status values carry their literal wire strings", func(t *testing.T) {
		// Downstream phases assert against these literals, and the design names them
		// verbatim. Pinning the values here is what keeps the wire contract stable when the
		// constants are renamed.
		for name, got := range map[string]string{
			"EventInstanceStart":    EventInstanceStart,
			"EventStepStart":        EventStepStart,
			"EventStepEnd":          EventStepEnd,
			"EventInstanceEnd":      EventInstanceEnd,
			"EventSessionStart":     EventSessionStart,
			"EventIntervention":     EventIntervention,
			"StatusClosed":          StatusClosed,
			"StatusSkipped":         StatusSkipped,
			"StatusGateWaiting":     StatusGateWaiting,
			"ModelSourceOverride":   ModelSourceOverride,
			"ModelSourceModelsJSON": ModelSourceModelsJSON,
			"ModelSourceUnknown":    ModelSourceUnknown,
			"ActionAdvise":          ActionAdvise,
			"ActionHandoff":         ActionHandoff,
			"ActionReduceEffort":    ActionReduceEffort,
		} {
			if want := pinnedWireLiterals[name]; got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		if SchemaVersion != 1 {
			t.Errorf("SchemaVersion = %d, want 1", SchemaVersion)
		}
	})

	t.Run("every wire constant declared in event.go is pinned", func(t *testing.T) {
		// The map above only checks what it lists, so until #668 a new event kind could be
		// declared and shipped with nobody asserting its literal — the one site in this file
		// with no interlock behind it. Reading the declarations out of the source closes that:
		// adding a constant without pinning it now fails here rather than in whatever later
		// phase greps for the value.
		declared := declaredWireConstants(t, "event.go")
		if len(declared) == 0 {
			t.Fatal("parsed no wire constants out of event.go; the source walk is broken, not the schema")
		}
		for _, name := range declared {
			if _, pinned := pinnedWireLiterals[name]; !pinned {
				t.Errorf("constant %s is declared in event.go but its wire literal is not pinned; "+
					"the literal value is the on-disk contract and a rename must not be able to move it", name)
			}
		}
		for name := range pinnedWireLiterals {
			if !slices.Contains(declared, name) {
				t.Errorf("wire literal %q is pinned but no such constant is declared in event.go", name)
			}
		}
	})

	t.Run("a non-step_end record omits the step_end-only fields", func(t *testing.T) {
		raw, err := json.Marshal(StepEvent{
			V: SchemaVersion, Event: EventStepStart, TS: "2026-07-22T18:31:04.112Z",
			Agent: "design-v7", InstanceID: "i", StepID: "s", Verb: "prime",
		})
		if err != nil {
			t.Fatalf("marshalling StepEvent: %v", err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		// ctx_tokens_start, cum_tokens_delta and ctx_bound_tokens join duration_ms and status
		// here (#622 G14): all three are derived AT CLOSE, so their presence on a step_start
		// record would mean a reader could not tell an echo from a measurement. This subtest is
		// also what pins the omitempty decision — without the tag, a nil pointer serialises as an
		// explicit null and a zero int64 as 0, and "present" is what is tested.
		// #668 K4 adds three more figures derived at close, under the same rule, and Phase 5 adds
		// subagent_tokens — derived from a transcript tree that only exists once the step's
		// delegations have finished.
		// #678 K1 adds the rest of that same close-time derivation: the exact thinking count and the
		// diagnostic legs beside it, the counts of what the step did, the split sub-agent spend, and
		// the two labels that say what measured the step and why it could not be measured. Every one
		// is read out of a transcript window that does not exist until the window has closed, so a
		// step_start carrying any of them would again be an echo indistinguishable from a measurement.
		for _, k := range []string{
			"duration_ms", "status", "ctx_tokens_start", "cum_tokens_delta", "ctx_bound_tokens",
			"out_tokens", "think_tokens_est", "peak_ctx_tokens", "subagent_tokens",
			"think_tokens", "in_tokens", "cache_read_tokens", "cache_creation_tokens",
			"subagent_launches", "workflow_launches", "subagent_nested_launches", "repeat_reads",
			"gate_flags", "subagent_in_tokens", "subagent_out_tokens",
			"host_version", "generation_unmeasured_reason",
		} {
			if _, present := got[k]; present {
				t.Errorf("a %s record carries %q, which belongs only to %s", EventStepStart, k, EventStepEnd)
			}
		}
	})

	t.Run("an unmeasured record omits every context field rather than zeroing it", func(t *testing.T) {
		// The whole point of the pointer types (#622 C2): a step nobody measured and a step that
		// consumed nothing must not look alike on disk. Without omitempty every one of these keys
		// would appear as an explicit null, and a reader would have to guess which nulls meant
		// "absent" — the distinction the pointers exist to preserve, lost in the encoder.
		//
		// This also protects the export cursor: recordDigest (store.go:98) SHA-256s the
		// re-marshalled struct against a bookmark the PREVIOUS binary persisted, so a record
		// written before these fields existed must still marshal to the same bytes after upgrade.
		raw, err := json.Marshal(StepEvent{
			V: SchemaVersion, Event: EventStepEnd, TS: "2026-07-22T18:31:04.112Z",
			Agent: "design-v7", InstanceID: "i", StepID: "s", Verb: "done",
			DurationMS: 10, Status: StatusClosed,
		})
		if err != nil {
			t.Fatalf("marshalling StepEvent: %v", err)
		}
		var got map[string]json.RawMessage
		if err := json.Unmarshal(raw, &got); err != nil {
			t.Fatalf("decoding: %v", err)
		}
		for _, k := range []string{
			"ctx_used_pct", "ctx_tokens_used", "ctx_tokens_total", "ctx_observed_at", "cum_tokens",
			"ctx_tokens_start", "cum_tokens_delta", "ctx_bound_tokens",
			// #668 K4: a step whose transcript was unreachable, or that spanned a session
			// recycle, measured no generation either — and "unreachable" is not "produced zero".
			"out_tokens", "think_tokens_est", "peak_ctx_tokens",
			// #668 Phase 5: and a step that delegated nothing is not a step whose sub-agent
			// transcripts the host has since expired.
			"subagent_tokens",
			// #678 K1: the same distinction, one layer finer. A host that cannot report thinking and
			// a step that did none must not collapse into 0; nor must a step that launched no
			// sub-agents and a step whose launches could not be counted; nor a step that re-read
			// nothing and a step whose transcript was never opened. Every one of these is a pointer
			// for that reason, and omitempty is what stops the encoder undoing it.
			"think_tokens", "in_tokens", "cache_read_tokens", "cache_creation_tokens",
			"subagent_launches", "workflow_launches", "subagent_nested_launches", "repeat_reads",
			"gate_flags", "subagent_in_tokens", "subagent_out_tokens",
		} {
			if _, present := got[k]; present {
				t.Errorf("an unmeasured record carries %q; absent and zero must stay distinguishable", k)
			}
		}
	})

	t.Run("an instance record carries the attribution scalars and a step record does not", func(t *testing.T) {
		// #678 K1. The attribution family answers "what binary, on what tree, with what inputs", and
		// that question is settled ONCE per run — at instantiation for the checkout and the inputs, at
		// close for the base commit the branch ended up on. Echoing them onto every step_end would
		// multiply one fact by the step count and invite a reader to believe a step whose value
		// differed had observed something, when all it could ever be is a re-read of the same state.
		marshalKeys := func(ev StepEvent) map[string]json.RawMessage {
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshalling StepEvent: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			return got
		}
		base := StepEvent{
			V: SchemaVersion, TS: "2026-07-22T18:31:04.112Z",
			Agent: "design-v7", InstanceID: "i", StepID: "s",
		}

		// The presence half first: without it every absence assertion below is satisfiable by fields
		// that never serialise at all.
		instanceStart := base
		instanceStart.Event, instanceStart.Verb = EventInstanceStart, "sling"
		instanceStart.AFVersion, instanceStart.AFCommit = "0.9.3", "unknown"
		instanceStart.TokenomicsState = TokenomicsStateOn
		instanceStart.CheckoutCommit = "ee49c6a276f653ab12bbcbd2bd78b4aed1d2f75a"
		instanceStart.SlingDigest = "9f2b7c4e1a8d6035b7e2c9418f0a3d5671c8b24e0f9a6d3712b5c8e4a17d0369"
		instanceStartKeys := marshalKeys(instanceStart)
		for _, k := range []string{"af_version", "af_commit", "tokenomics_state", "checkout_commit", "sling_digest"} {
			if _, present := instanceStartKeys[k]; !present {
				t.Errorf("an %s record does not carry %q; the run cannot say what it ran on", EventInstanceStart, k)
			}
		}
		// base_commit is the one attribution field instantiation cannot know: the branch has not
		// finished moving yet, so it is resolved at close and belongs to instance_end.
		if _, present := instanceStartKeys["base_commit"]; present {
			t.Errorf("an %s record carries %q, which is only resolvable once the run has finished",
				EventInstanceStart, "base_commit")
		}

		instanceEnd := base
		instanceEnd.Event, instanceEnd.Verb = EventInstanceEnd, "done"
		instanceEnd.BaseCommit = "d19491982467a37acee661c2d85b15dcb77efc9e"
		instanceEnd.FormulaDigest = "0000000000000000000000000000000000000000000000000000000000000000"
		instanceEndKeys := marshalKeys(instanceEnd)
		for _, k := range []string{"base_commit", "formula_digest"} {
			if _, present := instanceEndKeys[k]; !present {
				t.Errorf("an %s record does not carry %q", EventInstanceEnd, k)
			}
		}

		stepEnd := base
		stepEnd.Event, stepEnd.Verb, stepEnd.Status = EventStepEnd, "done", StatusClosed
		stepEndKeys := marshalKeys(stepEnd)
		for _, k := range []string{
			"af_version", "af_commit", "tokenomics_state", "checkout_commit", "base_commit", "sling_digest",
		} {
			if _, present := stepEndKeys[k]; present {
				t.Errorf("a %s record carries %q, which is settled once per run and belongs to the "+
					"instance records; repeating it per step invents a per-step observation", EventStepEnd, k)
			}
		}
	})

	t.Run("a non-intervention record omits the intervention-only fields", func(t *testing.T) {
		// #668 K4 landed the intervention KIND with an empty payload and this subtest asserted the
		// emptiness. Phase 5 lands the payload event.go:17-21 reserved for it, so the subtest now
		// asserts the shape that replaces the emptiness: the firing payload is intervention-only in
		// BOTH directions — a populated intervention record carries it, and no other kind may.
		//
		// #678 K1 REMOVES effort_level from that set and adds objective in its place, and the swap is
		// the point rather than an accident of it. mechanism, action and objective describe an EVENT:
		// something fired, did a thing, for a reason. A step that closed is not a mechanism that
		// fired, and a reader counting firings by counting those keys must keep counting correctly.
		// effort_level describes a STATE — the level in force — and a state is exactly as true of a
		// session that launched and a step that ran as of a relaunch that changed it. Held to the old
		// rule, a run in which the effort actuator never fired carries no effort level anywhere, and
		// the control arm of the experiment #668 D16 added the field for cannot be read at all. So the
		// both-directions assertion survives, narrowed to the three keys it was really about.
		//
		// The other two directions are unchanged: formula_digest is captured at instantiation and
		// belongs to instance_start, while the generation scalars are derived at close and belong
		// to step_end. No kind may leak into another.
		marshalKeys := func(ev StepEvent) map[string]json.RawMessage {
			raw, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshalling StepEvent: %v", err)
			}
			var got map[string]json.RawMessage
			if err := json.Unmarshal(raw, &got); err != nil {
				t.Fatalf("decoding: %v", err)
			}
			return got
		}
		base := StepEvent{
			V: SchemaVersion, TS: "2026-07-22T18:31:04.112Z",
			Agent: "design-v7", InstanceID: "i", StepID: "s",
		}
		interventionOnly := []string{"mechanism", "action", "objective"}

		// A mechanism that fired without an effort level still writes no effort_level key: the
		// omitempty half, and what keeps the export cursor's digest stable for every record written
		// before these fields existed.
		minimal := base
		minimal.Event, minimal.Verb = EventIntervention, "done"
		for k := range marshalKeys(minimal) {
			if !slices.Contains(alwaysPresentKeys, k) {
				t.Errorf("a minimal %s record carries %q; a mechanism payload must be omitted when "+
					"the mechanism did not set it", EventIntervention, k)
			}
		}

		// The presence half. Without it every absence assertion below is satisfiable by fields that
		// never serialise at all.
		fired := base
		fired.Event, fired.Verb = EventIntervention, "done"
		fired.Mechanism, fired.Action = "effort", ActionReduceEffort
		fired.Objective, fired.EffortLevel = ObjectiveEfficiency, "low"
		firedKeys := marshalKeys(fired)
		for _, k := range append([]string{"effort_level"}, interventionOnly...) {
			if _, present := firedKeys[k]; !present {
				t.Errorf("a firing %s record does not carry %q; the record cannot say what fired",
					EventIntervention, k)
			}
		}

		instanceStart := base
		instanceStart.Event, instanceStart.Verb = EventInstanceStart, "sling"
		instanceStart.FormulaDigest = "0000000000000000000000000000000000000000000000000000000000000000"
		// effort_level joins the absence list here explicitly rather than through interventionOnly:
		// #678 K1 puts the state on session_start and step_end, and instance_start is neither. An
		// instantiation happens before any session exists to have a level.
		for _, k := range append([]string{
			"out_tokens", "think_tokens_est", "peak_ctx_tokens", "subagent_tokens", "effort_level",
		}, interventionOnly...) {
			if _, present := marshalKeys(instanceStart)[k]; present {
				t.Errorf("an %s record carries %q, which no instantiation can have produced", EventInstanceStart, k)
			}
		}

		outTokens := int64(27078)
		stepEnd := base
		stepEnd.Event, stepEnd.Verb, stepEnd.Status = EventStepEnd, "done", StatusClosed
		stepEnd.OutTokens = &outTokens
		stepEnd.EffortLevel = "high"
		stepEndKeys := marshalKeys(stepEnd)
		if _, present := stepEndKeys["formula_digest"]; present {
			t.Errorf("a %s record carries %q, which is captured once at instantiation and belongs "+
				"to %s", EventStepEnd, "formula_digest", EventInstanceStart)
		}
		for _, k := range interventionOnly {
			if _, present := stepEndKeys[k]; present {
				t.Errorf("a %s record carries %q; a step that closed is not a mechanism that fired, "+
					"and a reader counting firings would count every close", EventStepEnd, k)
			}
		}
		// The other half of the #678 K1 swap, asserted positively so that "effort_level left the
		// intervention-only set" cannot be satisfied by the field quietly never serialising anywhere.
		if _, present := stepEndKeys["effort_level"]; !present {
			t.Errorf("a %s record does not carry %q; the level a step RAN at is the experiment's "+
				"control arm, and a run where no mechanism fired would otherwise report no level at all",
				EventStepEnd, "effort_level")
		}
		sessionStart := base
		sessionStart.Event, sessionStart.Verb = EventSessionStart, "prime"
		sessionStart.EffortLevel = "high"
		if _, present := marshalKeys(sessionStart)["effort_level"]; !present {
			t.Errorf("a %s record does not carry %q; the level a session LAUNCHED under is what a "+
				"step's level is compared against", EventSessionStart, "effort_level")
		}
	})
}

// alwaysPresentKeys are the keys every record carries whatever its kind, because their fields are
// the record's identity rather than a measurement and so are not omitempty.
var alwaysPresentKeys = []string{
	"v", "event", "ts", "agent", "worktree_id", "formula", "instance_id",
	"step_id", "step_seq", "step_title", "session_id", "model", "model_source",
	"verb", "verb_ms",
}

func fullyPopulatedEvent() StepEvent {
	// Every field must be set to something serialisable, including the omitempty ones: the
	// "serialized key set is exactly the allowlist" subtest errors in BOTH directions, so a field
	// left nil here reports as an allowlisted key absent from the record.
	usedPct := 37.5
	tokensUsed, tokensTotal := int64(75000), int64(200000)
	cumTokens, ctxTokensStart, cumTokensDelta := int64(412000), int64(21000), int64(54000)
	outTokens, thinkTokensEst, peakCtxTokens := int64(27078), int64(25442), int64(116228)
	subagentTokens := int64(41190)
	poolTokens, summedTokens := int64(262144), int64(233472)
	thinkTokens, inTokens := int64(21106), int64(9440)
	cacheReadTokens, cacheCreationTokens := int64(1048576), int64(31000)
	subagentLaunches, workflowLaunches, subagentNestedLaunches := int64(4), int64(1), int64(2)
	repeatReads, gateFlags := int64(3), int64(2)
	subagentInTokens, subagentOutTokens := int64(30110), int64(11080)
	return StepEvent{
		V:           SchemaVersion,
		Event:       EventStepEnd,
		TS:          "2026-07-22T18:31:04.112Z",
		Agent:       "design-v7",
		WorktreeID:  "wt-03478d",
		Formula:     "design-v7",
		InstanceID:  "af-329-instance",
		StepID:      "phase2-analysis",
		StepSeq:     7,
		StepTitle:   "Phase 2: analysis",
		StepLabel:   "phase-2",
		SessionID:   "5f2c1d90-0000-4000-8000-000000000001",
		Model:       "fable-5",
		ModelSource: ModelSourceModelsJSON,
		Verb:        "done",
		VerbMS:      42,
		DurationMS:  6388,
		Status:      StatusClosed,

		CtxUsedPct:     &usedPct,
		CtxTokensUsed:  &tokensUsed,
		CtxTokensTotal: &tokensTotal,
		CtxObservedAt:  "2026-07-22T18:31:02Z",
		CumTokens:      &cumTokens,
		CtxTokensStart: &ctxTokensStart,
		CumTokensDelta: &cumTokensDelta,
		CtxBoundTokens: 200000,

		FormulaDigest:  "3f6c8b1a9d2e4705c8143b6f0a9e2d5741b8c30f6a2d94e17c05b83fa1d62e40",
		OutTokens:      &outTokens,
		ThinkTokensEst: &thinkTokensEst,
		PeakCtxTokens:  &peakCtxTokens,

		ThinkTokens:         &thinkTokens,
		InTokens:            &inTokens,
		CacheReadTokens:     &cacheReadTokens,
		CacheCreationTokens: &cacheCreationTokens,

		SubagentLaunches:       &subagentLaunches,
		WorkflowLaunches:       &workflowLaunches,
		SubagentNestedLaunches: &subagentNestedLaunches,
		RepeatReads:            &repeatReads,
		GateFlags:              &gateFlags,

		HostVersion:                "2.1.258",
		GenerationUnmeasuredReason: ReasonNoRecordsInWindow,

		Mechanism:      "effort",
		Action:         ActionReduceEffort,
		Objective:      ObjectiveEfficiency,
		EffortLevel:    "low",
		SubagentTokens: &subagentTokens,

		SubagentInTokens:  &subagentInTokens,
		SubagentOutTokens: &subagentOutTokens,

		PoolTokens:   &poolTokens,
		SummedTokens: &summedTokens,

		AFVersion:       "0.9.3",
		AFCommit:        "ee49c6a276f653ab12bbcbd2bd78b4aed1d2f75a",
		TokenomicsState: TokenomicsStateOn,
		CheckoutCommit:  "ee49c6a276f653ab12bbcbd2bd78b4aed1d2f75a",
		BaseCommit:      "d19491982467a37acee661c2d85b15dcb77efc9e",
		SlingDigest:     "9f2b7c4e1a8d6035b7e2c9418f0a3d5671c8b24e0f9a6d3712b5c8e4a17d0369",
	}
}
