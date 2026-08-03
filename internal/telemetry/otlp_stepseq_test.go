package telemetry

import (
	"encoding/json"
	"sort"
	"testing"
)

// stepSeqAttrOf decodes the emitted payload far enough to inspect the af.step_seq attribute
// as raw JSON, deliberately WITHOUT modelling it as a string or an integer. Decoding into a
// typed field would make the test agree with whichever shape the encoder happens to produce,
// which is the failure mode this whole phase exists to avoid.
func stepSeqAttrOf(t *testing.T, payload []byte) map[string]json.RawMessage {
	t.Helper()
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					Attributes []struct {
						Key   string                     `json:"key"`
						Value map[string]json.RawMessage `json:"value"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}
	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				for _, a := range sp.Attributes {
					if a.Key == "af.step_seq" {
						return a.Value
					}
				}
			}
		}
	}
	t.Fatal("no af.step_seq attribute found in the emitted payload")
	return nil
}

// TestStepSeqIsOrderableNumerically pins the defect the shipped waterfall renders: step_seq
// is emitted as an OTLP stringValue, so every view that orders on it orders lexicographically
// and step 10 sorts before step 2. Any formula with ten or more steps renders out of order.
//
// The assertion is on the WIRE TYPE rather than on a sorted list of Go values, because the
// ordering is performed by the backend over what we emit — a test that sorted in Go would
// pass while the shipped dashboards stayed wrong.
func TestStepSeqIsOrderableNumerically(t *testing.T) {
	payload, err := EncodeOTLPTraces([]StepEvent{ev("a", "i1", "s10", 10)})
	if err != nil {
		t.Fatalf("EncodeOTLPTraces: %v", err)
	}
	val := stepSeqAttrOf(t, payload)

	if _, isString := val["stringValue"]; isString {
		if _, isInt := val["intValue"]; !isInt {
			t.Errorf("af.step_seq is emitted as a stringValue: the views ORDER BY it, so "+
				"lexicographic order puts step 10 before step 2. OTLP has intValue; got %v", val)
		}
	}
	raw, ok := val["intValue"]
	if !ok {
		t.Fatalf("af.step_seq carries no intValue member; got %v", val)
	}

	// OTLP/JSON encodes 64-bit integers as decimal STRINGS (proto3 JSON mapping), which the
	// pinned fixture set records at testdata/otlp-schema.json under decimal_string_fields.
	// So the correct emission is {"intValue":"10"} — a bare JSON number would be a spec
	// violation of exactly the kind the reject fixtures already cover for timestamps.
	var asDecimalString string
	if err := json.Unmarshal(raw, &asDecimalString); err != nil {
		t.Errorf("intValue is not a JSON decimal string (%s): proto3 JSON maps int64 to a "+
			"string, and testdata/otlp-schema.json lists intValue under decimal_string_fields", raw)
	} else if asDecimalString != "10" {
		t.Errorf("intValue = %q, want \"10\"", asDecimalString)
	}
}

// TestStepSeqZeroIsStillEmitted guards the omitempty trap that a naive integer conversion
// walks into. Records that carry no step — instance_start, instance_end, and any step_end
// whose start record was lost — have StepSeq 0. If the integer field is emitted with
// omitempty on the wrong member, those spans silently lose the attribute entirely, which is
// a new data-loss bug introduced by the fix for an ordering bug.
func TestStepSeqZeroIsStillEmitted(t *testing.T) {
	payload, err := EncodeOTLPTraces([]StepEvent{ev("a", "i1", "s0", 0)})
	if err != nil {
		t.Fatalf("EncodeOTLPTraces: %v", err)
	}
	val := stepSeqAttrOf(t, payload)
	raw, ok := val["intValue"]
	if !ok {
		t.Fatalf("a step_seq of 0 emitted no intValue member; got %v", val)
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil || s != "0" {
		t.Errorf("intValue for step_seq 0 = %s, want \"0\"", raw)
	}
}

// TestAttributeValueIsExactlyOneOneofMember is the assertion that nothing in the existing
// conformance suite makes, and its absence is what would let a naive S1 fix ship a spec
// violation with the whole suite green.
//
// AnyValue is a proto3 `oneof`: exactly one member may be set. otlpAnyValue.StringValue
// carries no omitempty, so adding an IntValue field beside it emits BOTH keys —
// {"stringValue":"","intValue":"7"} — and validateOTLP allowlists both names without any
// exclusivity rule. A receiver taking the first member, or the empty one, would then read
// af.step_seq as the empty string: worse than the defect being fixed.
func TestAttributeValueIsExactlyOneOneofMember(t *testing.T) {
	payload, err := EncodeOTLPTraces([]StepEvent{ev("a", "i1", "s7", 7)})
	if err != nil {
		t.Fatalf("EncodeOTLPTraces: %v", err)
	}

	var doc struct {
		ResourceSpans []struct {
			Resource struct {
				Attributes []struct {
					Key   string                     `json:"key"`
					Value map[string]json.RawMessage `json:"value"`
				} `json:"attributes"`
			} `json:"resource"`
			ScopeSpans []struct {
				Spans []struct {
					Attributes []struct {
						Key   string                     `json:"key"`
						Value map[string]json.RawMessage `json:"value"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("decoding payload: %v", err)
	}

	check := func(key string, val map[string]json.RawMessage) {
		members := make([]string, 0, len(val))
		for k := range val {
			members = append(members, k)
		}
		sort.Strings(members)
		if len(members) != 1 {
			t.Errorf("attribute %q sets %d AnyValue members %v; a proto3 oneof permits exactly "+
				"one. Emitting an empty stringValue alongside a real intValue lets a receiver "+
				"read the empty one", key, len(members), members)
		}
	}

	for _, rs := range doc.ResourceSpans {
		for _, a := range rs.Resource.Attributes {
			check(a.Key, a.Value)
		}
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				for _, a := range sp.Attributes {
					check(a.Key, a.Value)
				}
			}
		}
	}
}
