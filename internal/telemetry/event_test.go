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
}

// contentBearingNames are concepts that must never name a field. The record is the privacy
// boundary and there is no downstream redaction step, so a field able to carry free-form text
// is a leak by construction rather than by accident. step_title and status must survive this
// list, which is why it matches whole concepts and not the substring "t".
var contentBearingNames = []string{
	"description", "desc", "vars", "task", "prompt", "body", "content", "text",
	"message", "msg", "args", "argv", "input", "output", "payload", "notes", "detail",
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
			switch f.Type.Kind() {
			case reflect.String, reflect.Int, reflect.Int64:
			default:
				t.Errorf("field %s is a %s; the schema admits only scalar strings and integers, "+
					"because a map, slice, pointer, interface or nested struct is a carrier for "+
					"arbitrary content", f.Name, f.Type.Kind())
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
		for _, k := range []string{"duration_ms", "status"} {
			if _, present := got[k]; present {
				t.Errorf("a %s record carries %q, which belongs only to %s", EventStepStart, k, EventStepEnd)
			}
		}
	})
}

func fullyPopulatedEvent() StepEvent {
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
	}
}
