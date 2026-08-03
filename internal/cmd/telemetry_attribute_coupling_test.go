package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// The emitted attribute names and the queried column names are the same knowledge written twice,
// in two spellings, in two packages — and nothing connects them.
//
// af emits dotted attributes: `af.agent`, `af.formula_instance`, `af.worktree_id`, `af.step_id`.
// The backend rewrites them on ingest, and the rewrite differs per plane: a TRACE resource
// attribute gains a `service_` prefix and every dot becomes an underscore, so `af.agent` is
// queryable only as `service_af_agent`; an OTLP LOG attribute gets the underscore but no prefix,
// so the same attribute is `af_agent` on the native side of the join. Both spellings are written
// out as string literals at every query site.
//
// The failure that costs nothing to produce and is invisible to every existing test: rename
// `af.agent` on the emit side. buildTelemetryWhere still constructs a syntactically valid clause,
// the backend still answers HTTP 200, and the filter matches zero rows — reported as state "ok"
// with rows []. Both lanes stay green, because every test that mentions a query spelling asserts
// it against itself. Mechanically: of the five _test.go files that mention `af.agent`, none
// mentioned `af_agent`.
//
// This test is the coupling. It derives the expected query spelling FROM the emitted one, by
// applying the ingest rule, and fails if any query site disagrees.
//
// It deliberately does NOT introduce shared constants for the two sides to import. That would
// make a rename green by construction: the query would silently follow the emit side into a
// spelling the backend has never heard of, which is the very silent-empty this test exists to
// catch. The duplication is load-bearing; what was missing was something that checks it. The
// pattern is already in the tree — views_contract_test.go derives the queried `af_overhead` from
// the emitted `af.overhead` the same way.
//
// No production code changes for this test to pass. It is coverage that was absent, over
// behaviour that is correct today.

// readPackageSource reads one file of this package as text, for the assertions that must inspect a
// spelling written as a string literal rather than reachable through a call.
func readPackageSource(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "internal", "cmd", name))
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(data)
}

// logsSpelling is the ingest rewrite for an OTLP log attribute: dots become underscores.
func logsSpelling(emitted string) string { return strings.ReplaceAll(emitted, ".", "_") }

// tracesSpelling is the ingest rewrite for a trace RESOURCE attribute: the same underscore rule,
// plus the `service_` prefix the backend adds to resource-level attributes.
func tracesSpelling(emitted string) string { return "service_" + logsSpelling(emitted) }

// emittedResourceAttrs recovers the resource attributes af actually launches a session with, from
// the exported builder rather than from a list retyped here. Retyping them would reintroduce the
// duplication this file exists to close.
func emittedResourceAttrs(t *testing.T) map[string]string {
	t.Helper()

	env := telemetry.LaunchEnv(
		config.TelemetryConfig{Endpoint: "http://127.0.0.1:5080/api/default", Protocol: "http/protobuf"},
		telemetry.CorrelationKeys{
			FactoryID:       "f1",
			Agent:           "solver",
			WorktreeID:      "wt-abc123",
			FormulaInstance: "af-4e894132",
			ModelProfile:    "default",
		},
	)

	var raw string
	for _, kv := range env {
		if kv.Key == "OTEL_RESOURCE_ATTRIBUTES" {
			raw = kv.Value
		}
	}
	if raw == "" {
		t.Fatal("LaunchEnv emitted no OTEL_RESOURCE_ATTRIBUTES: the correlation attributes are what " +
			"every telemetry filter matches on, so their absence would make the whole filter axis " +
			"meaningless — and this test could not derive a single expected spelling")
	}

	attrs := map[string]string{}
	for _, part := range strings.Split(raw, ",") {
		if k, v, ok := strings.Cut(part, "="); ok {
			attrs[k] = v
		}
	}
	return attrs
}

// emittedSpanAttrKeys recovers the per-span attribute keys from the exported encoder, by encoding
// one step event and reading the keys back out of the payload af would actually send.
func emittedSpanAttrKeys(t *testing.T) map[string]bool {
	t.Helper()

	payload, err := telemetry.EncodeOTLPTraces([]telemetry.StepEvent{
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-07-22T18:31:04.112Z", Agent: "solver", Formula: "demo",
			InstanceID: "af-4e894132", StepID: "s-1", StepSeq: 1, StepTitle: "Phase 1",
			Status: "closed", Model: "fable-5", Verb: "prime", VerbMS: 12,
		},
	})
	if err != nil {
		t.Fatalf("EncodeOTLPTraces: %v", err)
	}

	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []struct {
					Attributes []struct {
						Key string `json:"key"`
					} `json:"attributes"`
				} `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	if err := json.Unmarshal(payload, &doc); err != nil {
		t.Fatalf("unmarshal OTLP payload: %v", err)
	}

	keys := map[string]bool{}
	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			for _, sp := range ss.Spans {
				for _, a := range sp.Attributes {
					keys[a.Key] = true
				}
			}
		}
	}
	if len(keys) == 0 {
		t.Fatal("EncodeOTLPTraces produced a span with no attributes at all")
	}
	return keys
}

// TestTelemetryQuerySpellingsDeriveFromTheEmittedAttributes is the coupling test proper.
//
// Every query site below is checked against a spelling COMPUTED from the emit side. Rename
// `af.agent` in launchenv.go and this fails at each site that still names the old spelling —
// which is the whole point: the failure arrives at the moment of the rename, not months later as
// an empty table nobody can explain.
func TestTelemetryQuerySpellingsDeriveFromTheEmittedAttributes(t *testing.T) {
	resource := emittedResourceAttrs(t)
	span := emittedSpanAttrKeys(t)

	for _, emitted := range []string{"af.agent", "af.formula_instance"} {
		if _, ok := resource[emitted]; !ok {
			t.Fatalf("LaunchEnv no longer emits the resource attribute %q. Every filter in this "+
				"package is written against its ingest spelling, so a rename here silently empties "+
				"every filtered telemetry query while both test lanes stay green. If the rename is "+
				"deliberate, update the query sites below in the same change.", emitted)
		}
	}
	if !span["af.step_id"] {
		t.Fatal("EncodeOTLPTraces no longer emits the span attribute \"af.step_id\": the shipped " +
			"per-step tokens view groups on its ingest spelling af_step_id, so the step windows the " +
			"join depends on would vanish")
	}

	// --- The traces plane: resource attributes gain `service_`. ---
	where, err := buildTelemetryWhere("solver", "af-4e894132")
	if err != nil {
		t.Fatalf("buildTelemetryWhere: %v", err)
	}

	for _, emitted := range []string{"af.agent", "af.formula_instance"} {
		want := tracesSpelling(emitted)
		// Boundary-anchored: a suffixed rename (service_af_agentZZ) CONTAINS service_af_agent, so a
		// substring test passes the exact drift this is here to catch.
		if !regexp.MustCompile(regexp.QuoteMeta(want) + `\s*=`).MatchString(where.Traces) {
			t.Errorf("the traces clause does not filter on %q, the ingest spelling of the emitted "+
				"%q.\nclause: %s\nA trace RESOURCE attribute is prefixed on ingest; without this "+
				"spelling the clause is still valid SQL and still returns 200, and it matches zero "+
				"rows.", want, emitted, where.Traces)
		}
		// The logs spelling must NOT appear on the traces side: it is a real column name on the
		// other plane, so the mistake produces a valid query against the wrong plane's vocabulary.
		if regexp.MustCompile(`\(` + regexp.QuoteMeta(logsSpelling(emitted)) + ` `).MatchString(where.Traces) {
			t.Errorf("the traces clause filters on the LOGS spelling %q; on the traces plane that "+
				"column does not exist", logsSpelling(emitted))
		}
	}

	// --- The native logs plane: the same attributes, no prefix. ---
	for _, emitted := range []string{"af.agent", "af.formula_instance"} {
		want := logsSpelling(emitted)
		if !regexp.MustCompile(regexp.QuoteMeta(want) + `\s*=`).MatchString(where.Logs) {
			t.Errorf("the logs clause does not filter on %q, the ingest spelling of the emitted %q.\n"+
				"clause: %s\nOTLP log attributes are NOT prefixed, so the traces spelling would match "+
				"nothing here.", want, emitted, where.Logs)
		}
		if strings.Contains(where.Logs, tracesSpelling(emitted)) {
			t.Errorf("the logs clause filters on the traces spelling %q; logs carry no service_ prefix",
				tracesSpelling(emitted))
		}
	}

	// --- The metrics plane: the PromQL selector, and the labels read back off the answer. ---
	//
	// Asserted against the string the code BUILDS, not against the file's text. Reading the source
	// for "af_agent" couples nothing: the same token sits in a comment above telemetryUsageMetrics
	// and in the label allowlist, so the grep passes while the selector says af_agentZZ and every
	// filtered query matches zero series. That mutation survived the entire root suite before this
	// assertion existed.
	selector := promQLSelector("solver", "af-4e894132")
	for _, emitted := range []string{"af.agent", "af.formula_instance"} {
		want := logsSpelling(emitted) + `="`
		if !strings.Contains(selector, want) {
			t.Errorf("the PromQL selector %q does not filter on %q, the ingest spelling of the emitted "+
				"%q. A selector naming a label the backend does not carry is still valid PromQL and "+
				"still answers HTTP 200 — with an empty vector, reported as state \"ok\" and no rows.",
				selector, want, emitted)
		}
		// Boundary-anchored: a suffixed rename (af_agentZZ) contains af_agent, so a substring test
		// passes the exact drift this is here to catch.
		if !regexp.MustCompile(regexp.QuoteMeta(logsSpelling(emitted)) + `="`).MatchString(selector) {
			t.Errorf("the selector names %q only as a prefix of some longer label; the label must be "+
				"exactly the emitted attribute's ingest spelling", logsSpelling(emitted))
		}
	}
	// The traces spelling must not leak into the metrics plane, whose labels carry no prefix.
	for _, emitted := range []string{"af.agent", "af.formula_instance"} {
		if strings.Contains(selector, tracesSpelling(emitted)) {
			t.Errorf("the PromQL selector %q uses the traces spelling %q; metric labels carry no "+
				"service_ prefix", selector, tracesSpelling(emitted))
		}
	}

	// The READ side. A selector that matches nothing yields an empty vector — visible. A label read
	// back under a stale spelling yields rows whose agent and run are blank: attribution lost on
	// data that did arrive, which is worse because it looks like an answer.
	row := metricRowFromLabels(map[string]string{
		logsSpelling("af.agent"):            "solver",
		logsSpelling("af.formula_instance"): "af-4e894132",
		logsSpelling("af.worktree_id"):      "wt-4e8941",
		logsSpelling("af.model_profile"):    "default",
		"user_email":                        "someone@example.com",
	}, "claude_code_token_usage", "42")
	if row.Agent != "solver" {
		t.Errorf("a metrics row read back under the emitted attribute's ingest spelling has agent %q, "+
			"want \"solver\" — the read-back label name has drifted from the emitted one, so every "+
			"row arrives unattributed while the query still looks healthy", row.Agent)
	}
	if row.Instance != "af-4e894132" {
		t.Errorf("a metrics row has instance %q, want the emitted af.formula_instance's ingest "+
			"spelling to be read back", row.Instance)
	}
	for _, emitted := range []string{"af.worktree_id", "af.model_profile"} {
		if _, ok := row.Labels[logsSpelling(emitted)]; !ok {
			t.Errorf("the label allowlist does not carry %q, the ingest spelling of the emitted %q — "+
				"a series distinguished only by that label collapses into an indistinguishable "+
				"duplicate", logsSpelling(emitted), emitted)
		}
		if _, ok := resource[emitted]; !ok {
			t.Errorf("LaunchEnv no longer emits %q, but the allowlist still reads its ingest "+
				"spelling %q off every metrics row", emitted, logsSpelling(emitted))
		}
	}
	// The identity labels the live backend also returns must never be relayed.
	if _, leaked := row.Labels["user_email"]; leaked {
		t.Error("the label allowlist relayed user_email; this payload is rendered in a browser")
	}
}

// TestTelemetryQuerySpellingCouplingSelfNegative proves the coupling above can fail.
//
// A derivation test that only ever ran against a correct tree would be indistinguishable from one
// that asserts nothing — which is exactly the property that let the original gap survive review.
func TestTelemetryQuerySpellingCouplingSelfNegative(t *testing.T) {
	// The rewrite rules themselves, applied to a renamed attribute.
	if got := logsSpelling("af.agentz"); got == logsSpelling("af.agent") {
		t.Fatal("logsSpelling maps two different emitted attributes to one column name")
	}
	if got, want := tracesSpelling("af.agent"), "service_af_agent"; got != want {
		t.Fatalf("tracesSpelling(%q) = %q, want %q", "af.agent", got, want)
	}
	if got, want := logsSpelling("af.agent"), "af_agent"; got != want {
		t.Fatalf("logsSpelling(%q) = %q, want %q", "af.agent", got, want)
	}

	// And the assertion shape: a clause built for one agent must not satisfy a spelling derived
	// from a DIFFERENT emitted attribute. If it did, the checks above would pass on any rename.
	where, err := buildTelemetryWhere("solver", "")
	if err != nil {
		t.Fatalf("buildTelemetryWhere: %v", err)
	}
	if strings.Contains(where.Traces, tracesSpelling("af.agentz")) {
		t.Error("the traces clause matched a spelling derived from an attribute af does not emit; " +
			"the coupling assertions would then pass against a renamed emit side")
	}
}
