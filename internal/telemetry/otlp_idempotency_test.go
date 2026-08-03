package telemetry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"testing"
)

// goldenIDs pins the identity scheme to literals computed OUTSIDE this package, with the
// shell's own sha256 over the documented key strings. That provenance is what makes the
// assertion non-circular: a value read back from the code under test would agree with any
// scheme, including one that varies per process.
//
//	traceId = sha256("af.telemetry.trace.v1|" + instance_id)[:16]
//	spanId  = sha256("af.telemetry.span.v1|"  + instance_id + "|" + step_id)[:8]
var goldenIDs = []struct {
	instance, step string
	traceID        string
	spanID         string
}{
	{"af-329-instance", "phase2-analysis", "cb63a113a0fd11a0bdbc9cefcfd53efd", "8099eea5735d01c6"},
	{"af-329-instance", "phase3-synthesis", "cb63a113a0fd11a0bdbc9cefcfd53efd", "8e8a39fc6d2b80cd"},
	{"syn-instance-0001", "step-one", "895d8a7044b979a493c788700b705fb9", "07f1f47efc188cc4"},
}

type spanView struct {
	TraceID string `json:"traceId"`
	SpanID  string `json:"spanId"`
	Name    string `json:"name"`
}

func spansOf(t *testing.T, payload []byte) []spanView {
	t.Helper()
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []spanView `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	var out []spanView
	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			out = append(out, ss.Spans...)
		}
	}
	return out
}

func stepEnd(instance, step string) StepEvent {
	return StepEvent{
		V: SchemaVersion, Event: EventStepEnd, TS: "2026-07-22T18:31:10.500Z",
		Agent: "design-v7", WorktreeID: "wt-03478d", Formula: "design-v7",
		InstanceID: instance, StepID: step, StepSeq: 7, StepTitle: "Phase " + step,
		SessionID: "sess-1", Model: "fable-5", ModelSource: ModelSourceModelsJSON,
		Verb: "done", VerbMS: 77, DurationMS: 6388, Status: StatusClosed,
	}
}

func TestExportIdempotentSpanIDs(t *testing.T) {
	// This test fails against: ids seeded from a per-process value (the golden literals);
	// ids that fold in a timestamp or duration (the input-closure subtests); a naive
	// concatenation with no separator (the ambiguity subtest); and attributes emitted by
	// ranging a Go map, which makes the payload differ between two runs of the same input.

	t.Run("re-encoding the same records is byte-identical", func(t *testing.T) {
		evs := []StepEvent{stepEnd("af-329-instance", "phase2-analysis"), stepEnd("af-329-instance", "phase3-synthesis")}
		first, err := EncodeOTLPTraces(evs)
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		for i := 0; i < 8; i++ {
			again, err := EncodeOTLPTraces(evs)
			if err != nil {
				t.Fatalf("EncodeOTLPTraces: %v", err)
			}
			if !bytes.Equal(first, again) {
				t.Fatalf("encode %d differs from the first:\n%s\n%s", i, first, again)
			}
		}
	})

	t.Run("emitted ids match the pinned golden literals", func(t *testing.T) {
		for _, g := range goldenIDs {
			payload, err := EncodeOTLPTraces([]StepEvent{stepEnd(g.instance, g.step)})
			if err != nil {
				t.Fatalf("EncodeOTLPTraces: %v", err)
			}
			spans := spansOf(t, payload)
			if len(spans) != 1 {
				t.Fatalf("got %d spans for one record, want 1", len(spans))
			}
			if spans[0].TraceID != g.traceID {
				t.Errorf("traceId for instance %q = %s, want %s", g.instance, spans[0].TraceID, g.traceID)
			}
			if spans[0].SpanID != g.spanID {
				t.Errorf("spanId for (%q,%q) = %s, want %s", g.instance, g.step, spans[0].SpanID, g.spanID)
			}
		}
	})

	t.Run("the trace id depends on the instance alone", func(t *testing.T) {
		payload, err := EncodeOTLPTraces([]StepEvent{
			stepEnd("shared-instance", "step-a"),
			stepEnd("shared-instance", "step-b"),
		})
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		spans := spansOf(t, payload)
		if len(spans) != 2 {
			t.Fatalf("got %d spans, want 2", len(spans))
		}
		if spans[0].TraceID != spans[1].TraceID {
			t.Errorf("two steps of one instance have different trace ids (%s, %s); the waterfall "+
				"groups by trace id, so they would render as unrelated traces", spans[0].TraceID, spans[1].TraceID)
		}
		if spans[0].SpanID == spans[1].SpanID {
			t.Errorf("two different steps share span id %s", spans[0].SpanID)
		}
	})

	t.Run("the span id depends on the instance and step alone", func(t *testing.T) {
		base := stepEnd("i", "s")
		variant := base
		variant.TS = "2027-01-01T00:00:00.000Z"
		variant.DurationMS = 999999
		variant.VerbMS = 1
		variant.Model = "some-other-model"
		variant.ModelSource = ModelSourceOverride
		variant.Status = StatusGateWaiting
		variant.SessionID = "a-different-session"
		variant.StepSeq = 42
		variant.StepTitle = "a different title"

		a, err := EncodeOTLPTraces([]StepEvent{base})
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		b, err := EncodeOTLPTraces([]StepEvent{variant})
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		sa, sb := spansOf(t, a), spansOf(t, b)
		if sa[0].SpanID != sb[0].SpanID {
			t.Errorf("span id changed when only non-key fields changed (%s vs %s); a retry after a "+
				"failed export would then create a duplicate span rather than replacing the first",
				sa[0].SpanID, sb[0].SpanID)
		}
		if sa[0].TraceID != sb[0].TraceID {
			t.Errorf("trace id changed when only non-key fields changed (%s vs %s)", sa[0].TraceID, sb[0].TraceID)
		}
	})

	t.Run("composite key boundaries are unambiguous", func(t *testing.T) {
		a, err := EncodeOTLPTraces([]StepEvent{stepEnd("ab", "c")})
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		b, err := EncodeOTLPTraces([]StepEvent{stepEnd("a", "bc")})
		if err != nil {
			t.Fatalf("EncodeOTLPTraces: %v", err)
		}
		// A plain concatenation of the two keys passes every other assertion here and fails
		// only this one, which is why it is stated separately.
		if spansOf(t, a)[0].SpanID == spansOf(t, b)[0].SpanID {
			t.Error(`(instance "ab", step "c") and (instance "a", step "bc") collide; the hash input ` +
				`concatenates the keys with no separator`)
		}
	})

	t.Run("distinct keys do not collide after truncation", func(t *testing.T) {
		traces := map[string]string{}
		spans := map[string]string{}
		for i := 0; i < 200; i++ {
			inst := fmt.Sprintf("instance-%03d", i)
			payload, err := EncodeOTLPTraces([]StepEvent{stepEnd(inst, fmt.Sprintf("step-%03d", i))})
			if err != nil {
				t.Fatalf("EncodeOTLPTraces: %v", err)
			}
			s := spansOf(t, payload)[0]
			if prev, dup := traces[s.TraceID]; dup {
				t.Errorf("trace id %s collides between %s and %s", s.TraceID, prev, inst)
			}
			traces[s.TraceID] = inst
			if prev, dup := spans[s.SpanID]; dup {
				t.Errorf("span id %s collides between %s and %s", s.SpanID, prev, inst)
			}
			spans[s.SpanID] = inst
		}
	})

	t.Run("input order does not change any span's identity", func(t *testing.T) {
		fwd := []StepEvent{stepEnd("i", "s1"), stepEnd("i", "s2"), stepEnd("i", "s3")}
		rev := []StepEvent{fwd[2], fwd[1], fwd[0]}
		idOf := func(evs []StepEvent) map[string]string {
			payload, err := EncodeOTLPTraces(evs)
			if err != nil {
				t.Fatalf("EncodeOTLPTraces: %v", err)
			}
			out := map[string]string{}
			for _, s := range spansOf(t, payload) {
				out[s.Name] = s.TraceID + ":" + s.SpanID
			}
			return out
		}
		a, b := idOf(fwd), idOf(rev)
		if len(a) != 3 {
			t.Fatalf("got %d distinct span names, want 3", len(a))
		}
		for name, id := range a {
			if b[name] != id {
				t.Errorf("span %q has id %s in one order and %s in the other", name, id, b[name])
			}
		}
	})

	t.Run("an empty record set encodes to nothing rather than a malformed envelope", func(t *testing.T) {
		payload, err := EncodeOTLPTraces(nil)
		if err == nil && len(spansOf(t, payload)) != 0 {
			t.Error("an empty input produced spans")
		}
	})
}
