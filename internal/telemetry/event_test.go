package telemetry

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// allowlist is the closed field set the record schema is permitted to carry. It is spelled out
// here rather than derived from the struct, because a test that reads its expectation off the
// thing under test cannot detect the thing under test growing a field.
var allowlist = []string{
	"v", "event", "ts", "agent", "worktree_id", "formula", "instance_id",
	"step_id", "step_seq", "step_title", "session_id", "model", "model_source",
	"verb", "verb_ms", "duration_ms", "status",
	// #622 C2: context occupancy and consumption. Measurements, not content — see the
	// "every field is a scalar" subtest for why a pointer to one is still a scalar on the wire.
	"ctx_used_pct", "ctx_tokens_used", "ctx_tokens_total", "ctx_observed_at", "cum_tokens",
	"ctx_tokens_start", "cum_tokens_delta", "ctx_bound_tokens",
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
			"StatusClosed":          StatusClosed,
			"StatusSkipped":         StatusSkipped,
			"StatusGateWaiting":     StatusGateWaiting,
			"ModelSourceOverride":   ModelSourceOverride,
			"ModelSourceModelsJSON": ModelSourceModelsJSON,
			"ModelSourceUnknown":    ModelSourceUnknown,
		} {
			if want := map[string]string{
				"EventInstanceStart":    "instance_start",
				"EventStepStart":        "step_start",
				"EventStepEnd":          "step_end",
				"EventInstanceEnd":      "instance_end",
				"EventSessionStart":     "session_start",
				"StatusClosed":          "closed",
				"StatusSkipped":         "skipped",
				"StatusGateWaiting":     "gate-waiting",
				"ModelSourceOverride":   "override",
				"ModelSourceModelsJSON": "models_json",
				"ModelSourceUnknown":    "unknown",
			}[name]; got != want {
				t.Errorf("%s = %q, want %q", name, got, want)
			}
		}
		if SchemaVersion != 1 {
			t.Errorf("SchemaVersion = %d, want 1", SchemaVersion)
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
		for _, k := range []string{"duration_ms", "status", "ctx_tokens_start", "cum_tokens_delta", "ctx_bound_tokens"} {
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
		} {
			if _, present := got[k]; present {
				t.Errorf("an unmeasured record carries %q; absent and zero must stay distinguishable", k)
			}
		}
	})
}

func fullyPopulatedEvent() StepEvent {
	// Every field must be set to something serialisable, including the omitempty ones: the
	// "serialized key set is exactly the allowlist" subtest errors in BOTH directions, so a field
	// left nil here reports as an allowlisted key absent from the record.
	usedPct := 37.5
	tokensUsed, tokensTotal := int64(75000), int64(200000)
	cumTokens, ctxTokensStart, cumTokensDelta := int64(412000), int64(21000), int64(54000)
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
	}
}
