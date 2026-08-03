package telemetry

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"time"
)

// The types below are this package's private view of the OTLP trace request. They are
// unexported on purpose: permitting an in-module wire format was conditional on the encoder
// covering only the subset actually emitted, on the fixtures being written first, and on
// these types never leaking past this package. An exported OTLP type here would put the wire
// format into every caller's vocabulary and the third condition would be gone.
//
// Every tag below encodes a rule that the pinned fixtures enforce, and each one is a place
// where the obvious Go spelling is wrong:
//   - identifiers are hex strings, so they are typed string and NOT []byte, which this
//     language would base64-encode without comment;
//   - 64-bit times carry the string option, because proto3 JSON maps 64-bit integers to
//     decimal strings and a bare number is rejected;
//   - enums are plain ints, because only integer enum values are legal here — the name
//     strings that vanilla proto3 JSON permits are forbidden;
//   - keys are lowerCamelCase, because the original field names are not valid JSON keys.
type otlpTraceRequest struct {
	ResourceSpans []otlpResourceSpans `json:"resourceSpans"`
}

type otlpResourceSpans struct {
	Resource   otlpResource     `json:"resource"`
	ScopeSpans []otlpScopeSpans `json:"scopeSpans"`
}

type otlpResource struct {
	Attributes []otlpKeyValue `json:"attributes"`
}

type otlpScopeSpans struct {
	Scope otlpScope  `json:"scope"`
	Spans []otlpSpan `json:"spans"`
}

type otlpScope struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

type otlpSpan struct {
	TraceID           string         `json:"traceId"`
	SpanID            string         `json:"spanId"`
	Name              string         `json:"name"`
	Kind              int            `json:"kind"`
	StartTimeUnixNano uint64         `json:"startTimeUnixNano,string"`
	EndTimeUnixNano   uint64         `json:"endTimeUnixNano,string"`
	Attributes        []otlpKeyValue `json:"attributes"`
	Status            otlpStatus     `json:"status"`
}

type otlpStatus struct {
	Code int `json:"code"`
}

type otlpKeyValue struct {
	Key   string       `json:"key"`
	Value otlpAnyValue `json:"value"`
}

// otlpAnyValue is a proto3 `oneof`: exactly ONE member may be present on the wire. Both
// fields therefore carry omitempty, and the constructors below set exactly one of them.
//
// omitempty on StringValue is load-bearing rather than tidy. Without it, adding IntValue emits
// {"stringValue":"","intValue":"10"} — two members of the oneof — and a receiver is free to
// take the empty one. The conformance validator allowlists both key names and has no
// exclusivity rule, so that violation would pass the whole pinned suite. It is byte-neutral for
// existing output because attributeList already skips empty values.
//
// IntValue is a string because OTLP/JSON follows the proto3 JSON mapping, where 64-bit
// integers are encoded as decimal STRINGS — recorded in testdata/otlp-schema.json under
// decimal_string_fields. A bare JSON number here is the mistake the nanos-as-json-number
// reject fixture already covers for timestamps.
type otlpAnyValue struct {
	StringValue string `json:"stringValue,omitempty"`
	IntValue    string `json:"intValue,omitempty"`
}

const (
	spanKindInternal = 1
	statusCodeUnset  = 0
	statusCodeOK     = 1

	scopeName    = "agentfactory"
	scopeVersion = "1"
	serviceName  = "agentfactory"

	// Domain separation. Deriving both identifiers from the same hash input would make a
	// trace identifier and a span identifier collide whenever the step happened to be empty,
	// and the prefixes cost nothing.
	traceIDDomain = "af.telemetry.trace.v1|"
	spanIDDomain  = "af.telemetry.span.v1|"

	traceIDBytes = 16
	spanIDBytes  = 8
)

// TraceIDFor and SpanIDFor are the identity scheme, stated once.
//
// They are pure functions of the formula instance and the step, and of nothing else. That is
// what makes a retry idempotent: the exporter re-sends a batch after a failed attempt, and if
// the identifiers moved between attempts the backend would accumulate a duplicate span per
// retry instead of overwriting the original. Timestamps, durations, models and statuses are
// therefore excluded from the hash input even though they are part of the record.
//
// The separator is load-bearing. Concatenating the two keys with nothing between them makes
// ("ab","c") and ("a","bc") the same span, which is a silent merge rather than an error.
func TraceIDFor(instanceID string) string {
	sum := sha256.Sum256([]byte(traceIDDomain + instanceID))
	return hex.EncodeToString(sum[:traceIDBytes])
}

func SpanIDFor(instanceID, stepID string) string {
	sum := sha256.Sum256([]byte(spanIDDomain + instanceID + "|" + stepID))
	return hex.EncodeToString(sum[:spanIDBytes])
}

// EncodeOTLPTraces renders closed steps as an OTLP trace request.
//
// Only step_end records become spans; a step that has started but not finished has no
// duration and no end, and inventing one would put a guess into the backend that no later
// correction could distinguish from a measurement.
func EncodeOTLPTraces(events []StepEvent) ([]byte, error) {
	payload, _, err := encodeTraces(events)
	return payload, err
}

// encodeTraces is the form the exporter uses, reporting how many closed steps could not be
// rendered at all.
//
// Such a record is skipped rather than failing the batch. Failing would be worse than it
// sounds: the drain is ordered and the cursor only advances on success, so one record that can
// never be rendered — a corrupt timestamp, say — would block every record behind it forever,
// and the backlog would grow until the size ceiling started discarding good data. The same
// judgement the reader applies to a line it cannot parse applies here, and for the same
// reason. The count is what keeps the skip from being silent.
func encodeTraces(events []StepEvent) ([]byte, int, error) {
	type groupKey struct{ agent, worktree, formula, instance string }

	order := []groupKey{}
	grouped := map[groupKey][]otlpSpan{}
	unrenderable := 0

	for _, ev := range events {
		if ev.Event != EventStepEnd {
			continue
		}
		span, err := encodeSpan(ev)
		if err != nil {
			unrenderable++
			continue
		}
		k := groupKey{ev.Agent, ev.WorktreeID, ev.Formula, ev.InstanceID}
		if _, seen := grouped[k]; !seen {
			order = append(order, k)
		}
		grouped[k] = append(grouped[k], span)
	}
	if len(order) == 0 {
		return nil, unrenderable, ErrNoRecords
	}

	req := otlpTraceRequest{ResourceSpans: make([]otlpResourceSpans, 0, len(order))}
	for _, k := range order {
		req.ResourceSpans = append(req.ResourceSpans, otlpResourceSpans{
			Resource: otlpResource{Attributes: resourceAttributes(k.agent, k.worktree, k.formula, k.instance)},
			ScopeSpans: []otlpScopeSpans{{
				Scope: otlpScope{Name: scopeName, Version: scopeVersion},
				Spans: grouped[k],
			}},
		})
	}
	payload, err := json.Marshal(req)
	return payload, unrenderable, err
}

func encodeSpan(ev StepEvent) (otlpSpan, error) {
	end, err := parseRecordTime(ev.TS)
	if err != nil {
		return otlpSpan{}, fmt.Errorf("telemetry: record timestamp %q: %w", ev.TS, err)
	}
	start := end.Add(-time.Duration(ev.DurationMS) * time.Millisecond)

	name := ev.StepTitle
	if name == "" {
		name = ev.StepID
	}

	code := statusCodeUnset
	if ev.Status == StatusClosed {
		code = statusCodeOK
	}

	return otlpSpan{
		TraceID:           TraceIDFor(ev.InstanceID),
		SpanID:            SpanIDFor(ev.InstanceID, ev.StepID),
		Name:              name,
		Kind:              spanKindInternal,
		StartTimeUnixNano: uint64(start.UnixNano()),
		EndTimeUnixNano:   uint64(end.UnixNano()),
		// af.step_seq is an INTEGER attribute, not a string one: the shipped waterfall and the
		// per-step token view both order on it, and a string sorts lexicographically, so step 10
		// arrived before step 2 for any formula with ten or more steps.
		//
		// af.verb_ms and af.schema stay strings deliberately. Nothing orders or aggregates on
		// them — af_schema is read as a presence probe — and no review comment asks for them, so
		// converting them here would be scope this delivery has not earned.
		Attributes: attributeListWithInts(map[string]string{
			"af.step_id":      ev.StepID,
			"af.step_title":   ev.StepTitle,
			"af.status":       ev.Status,
			"af.model":        ev.Model,
			"af.model_source": ev.ModelSource,
			"af.verb":         ev.Verb,
			"af.verb_ms":      strconv.Itoa(ev.VerbMS),
			"af.session_id":   ev.SessionID,
			"af.schema":       strconv.Itoa(ev.V),
		}, map[string]int{
			"af.step_seq": ev.StepSeq,
		}),
		Status: otlpStatus{Code: code},
	}, nil
}

func resourceAttributes(agent, worktree, formula, instance string) []otlpKeyValue {
	return attributeList(map[string]string{
		"service.name":        serviceName,
		"af.agent":            agent,
		"af.worktree_id":      worktree,
		"af.formula":          formula,
		"af.formula_instance": instance,
	})
}

// attributeList emits a sorted list rather than ranging the map directly. Go randomises map
// iteration, so the unsorted form would produce a different byte sequence on each call — and
// the idempotency the retry story depends on is asserted on the whole payload, not just on
// the identifiers. Empty values are omitted rather than emitted blank: an attribute present
// with no value reads as "measured as empty" instead of "not applicable here".
func attributeList(attrs map[string]string) []otlpKeyValue {
	return attributeListWithInts(attrs, nil)
}

// attributeListWithInts emits string and integer attributes as one ascending run of keys.
//
// The two maps are merged before sorting rather than appended after, because the sort order is
// the attribute list's only stable property: ranging a Go map directly would make two encodings
// of the same record differ, and an output that changes on its own cannot be diffed when
// something goes wrong.
//
// The empty-value skip applies to strings ONLY. An integer attribute is emitted even when it is
// zero, because zero is a value: instance_start and instance_end records carry no step, and a
// step_end whose start record was lost reports sequence 0. Dropping those would replace an
// ordering bug with a data-loss bug.
func attributeListWithInts(attrs map[string]string, ints map[string]int) []otlpKeyValue {
	keys := make([]string, 0, len(attrs)+len(ints))
	for k, v := range attrs {
		if v == "" {
			continue
		}
		keys = append(keys, k)
	}
	for k := range ints {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]otlpKeyValue, 0, len(keys))
	for _, k := range keys {
		if v, ok := ints[k]; ok {
			out = append(out, otlpKeyValue{Key: k, Value: otlpAnyValue{IntValue: strconv.Itoa(v)}})
			continue
		}
		out = append(out, otlpKeyValue{Key: k, Value: otlpAnyValue{StringValue: attrs[k]}})
	}
	return out
}

// parseRecordTime accepts the record layout first and the wider forms afterwards, so a record
// written by an older build with coarser precision still exports rather than being dropped.
func parseRecordTime(ts string) (time.Time, error) {
	if ts == "" {
		return time.Time{}, errors.New("empty timestamp")
	}
	for _, layout := range []string{TimestampLayout, time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("not a recognised timestamp")
}
