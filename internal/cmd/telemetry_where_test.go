package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTelemetryWhereBuilder_RejectsInvalidFilters is the injection pin. T-7 chose
// validation-before-build over escaping precisely because escaping is a promise and validation is a
// mechanism: a rejected value cannot reach a request body because no body is ever constructed.
//
// Asserting err != nil would NOT prove that. A builder that rejected a value AFTER splicing it into
// a string would pass such a test while still having built the string. So the assertions below are
// about the returned clause as well as the error, and the recording arm in
// TestTelemetryUsage_RejectedFiltersNeverReachTheWire proves the request itself never happens.
func TestTelemetryWhereBuilder_RejectsInvalidFilters(t *testing.T) {
	hostile := []struct {
		name, agent, instance string
	}{
		{"agent closes the quote", "x' OR '1'='1", ""},
		{"agent terminates the statement", "x'; DROP TABLE logs;--", ""},
		{"agent comments out the rest", "x'--", ""},
		{"agent uses a backslash escape", `x\' OR 1=1`, ""},
		{"agent contains a bare quote", "o'brien", ""},
		{"agent contains a double quote", `x" OR "1"="1`, ""},
		{"agent contains a semicolon", "x;y", ""},
		{"agent contains whitespace", "x y", ""},
		{"agent contains a newline", "x\ny", ""},
		{"agent contains a NUL", "x\x00y", ""},
		{"agent contains a tab", "x\ty", ""},
		{"agent starts with a digit", "1abc", ""},
		{"agent starts with a hyphen", "-abc", ""},
		{"agent is a path traversal", "../../../../etc/passwd", ""},
		{"agent contains a percent", "x%y", ""},
		{"agent contains parentheses", "x()", ""},
		{"agent is over-long", strings.Repeat("a", 65), ""},

		{"instance closes the quote", "", "af-1' OR '1'='1"},
		{"instance terminates the statement", "", "af-1'; DROP TABLE logs;--"},
		{"instance contains a bare quote", "", "af-1'"},
		{"instance contains a double quote", "", `af-1"`},
		{"instance contains a semicolon", "", "af-1;x"},
		{"instance contains whitespace", "", "af 1"},
		{"instance contains a newline", "", "af-1\n"},
		{"instance contains a NUL", "", "af-1\x00"},
		{"instance starts with a digit", "", "1af"},
		{"instance starts with a hyphen", "", "-af"},
		{"instance is a path traversal", "", "../../etc/passwd"},
		{"instance contains a slash", "", "af/1"},
		{"instance contains a dot", "", "af.1"},
		{"instance is over-long", "", strings.Repeat("a", 65)},

		{"both hostile", "x'--", "y'--"},
	}

	for _, tc := range hostile {
		t.Run(tc.name, func(t *testing.T) {
			where, err := buildTelemetryWhere(tc.agent, tc.instance)
			if err == nil {
				t.Fatalf("buildTelemetryWhere(%q, %q) returned no error; a value that is not "+
					"provably an identifier must never become part of a query", tc.agent, tc.instance)
			}
			// The clause must be empty, not merely unused. A builder that constructs and then
			// discards is one refactor away from constructing and then sending.
			if where.Traces != "" || where.Logs != "" {
				t.Errorf("rejected input still produced clauses: traces=%q logs=%q\n"+
					"validation happens BEFORE the build, so a rejected value must leave nothing "+
					"built at all (conflicts.md T-7)", where.Traces, where.Logs)
			}
			// The hostile value must not survive anywhere in the error either — an error string is
			// a place values get echoed into logs.
			for _, bad := range []string{"DROP TABLE", "OR '1'='1"} {
				if strings.Contains(err.Error(), bad) {
					t.Errorf("error text echoes the hostile payload %q: %v", bad, err)
				}
			}
		})
	}
}

// TestTelemetryWhereBuilder_AcceptsRealValues is the non-vacuity control. Without it the whole
// negative suite above passes against a builder that rejects everything, which would be a rejection
// mechanism that also rejects the feature.
func TestTelemetryWhereBuilder_AcceptsRealValues(t *testing.T) {
	// Every value here was read out of a live backend capture
	// (internal/telemetry/testdata/openobserve-v0.91.3/), not invented.
	real := []struct{ agent, instance string }{
		{"soldesign-engineer", "af-4e894132"},
		{"fable-implement", "af-64a0dc2b"},
		{"", "af-0003257a"},
		{"solver", ""},
		{"", ""},
		{"manager", "af-c1c60861"},
		{"a", "af-6f5ca64f"},
		{"agent_with_underscores", "wt-f0ab79"},
		{strings.Repeat("a", 64), ""},
	}

	for _, tc := range real {
		where, err := buildTelemetryWhere(tc.agent, tc.instance)
		if err != nil {
			t.Errorf("buildTelemetryWhere(%q, %q) rejected a real value: %v", tc.agent, tc.instance, err)
			continue
		}
		if tc.agent == "" && tc.instance == "" {
			if where.Traces != "" || where.Logs != "" {
				t.Errorf("no filters set but clauses were built: traces=%q logs=%q", where.Traces, where.Logs)
			}
			continue
		}
		if where.Traces == "" || where.Logs == "" {
			t.Errorf("buildTelemetryWhere(%q, %q) built an empty clause for one side: traces=%q logs=%q\n"+
				"both sides of the join must be filtered or one agent's events attribute to another "+
				"agent's steps", tc.agent, tc.instance, where.Traces, where.Logs)
		}
	}
}

// TestTelemetryWhereBuilder_UsesBothIngestSpellings pins the rewrite rule that is invisible in the
// Go code and load-bearing in the SQL: trace resource attributes are prefixed with service_ on
// ingest, OTLP logs are not. Verified against the live stream schemas, not just the view's
// assumptions block.
func TestTelemetryWhereBuilder_UsesBothIngestSpellings(t *testing.T) {
	where, err := buildTelemetryWhere("solver", "af-4e894132")
	if err != nil {
		t.Fatalf("buildTelemetryWhere: %v", err)
	}

	for _, want := range []string{"service_af_agent", "service_af_formula_instance"} {
		if !strings.Contains(where.Traces, want) {
			t.Errorf("traces clause %q lacks %q — the traces stream carries the service_-prefixed "+
				"spelling and only that one", where.Traces, want)
		}
	}
	for _, unwanted := range []string{"service_af_agent", "service_af_formula_instance"} {
		if strings.Contains(where.Logs, unwanted) {
			t.Errorf("logs clause %q uses the prefixed spelling %q; OTLP logs do NOT get the "+
				"service_ prefix, so this predicate can only ever match nothing",
				where.Logs, unwanted)
		}
	}
	for _, want := range []string{"af_agent", "af_formula_instance"} {
		if !strings.Contains(where.Logs, want) {
			t.Errorf("logs clause %q lacks %q", where.Logs, want)
		}
	}
}

// TestTelemetryWhere_ParenthesisesExistingDisjunction is the trap no existing test can catch.
//
// The billable_requests predicate is a bare disjunction: af_overhead IS NULL, OR af_overhead equals
// the empty string. Appending a conjunct to that binds by SQL precedence to the RIGHT DISJUNCT ONLY,
// so the filter silently stops applying to every row where af_overhead IS NULL — which is most of
// them — and overhead rows are re-admitted into a per-step total that claims to exclude them.
//
// views_contract_test.go cannot see this: it reads the authored JSON, not the string this builder
// produces at runtime. There is no error, no failing test, and no symptom except a step that looks
// more expensive than the work it did — the exact defect the overhead rule exists to prevent.
func TestTelemetryWhere_ParenthesisesExistingDisjunction(t *testing.T) {
	sql, err := telemetryTokensSQL("solver", "af-4e894132")
	if err != nil {
		t.Fatalf("telemetryTokensSQL: %v", err)
	}

	const disjunction = "af_overhead IS NULL OR af_overhead = ''"
	if !strings.Contains(sql, disjunction) {
		t.Fatalf("the overhead predicate is missing from the built SQL entirely; the anchor this "+
			"builder splices against has moved:\n%s", sql)
	}
	if !strings.Contains(sql, "("+disjunction+")") {
		t.Errorf("the overhead disjunction is not parenthesised in the built SQL.\n"+
			"Found:    %s\n"+
			"Required: (%s) AND ...\n"+
			"Without the brackets the appended filter binds to the right-hand disjunct only, the "+
			"filter stops applying to the af_overhead IS NULL rows, and quality-gate spend is "+
			"counted back into the step totals. No test but this one can see it.",
			disjunction, disjunction)
	}

	// Each injected conjunct is itself bracketed, so a future second filter cannot re-create the
	// same precedence bug one level down.
	for _, want := range []string{"(af_agent = 'solver')", "(af_formula_instance = 'af-4e894132')"} {
		if !strings.Contains(sql, want) {
			t.Errorf("built SQL lacks the bracketed conjunct %s:\n%s", want, sql)
		}
	}
}

// TestTelemetryTokensSQL_UnfilteredIsByteIdenticalToTheAuthoredView is the anti-divergence pin.
// Gotcha 10's worry is a hand-copy that drifts from the authored source; with no filters there is
// nothing to splice, so the query sent must be the authored query exactly.
func TestTelemetryTokensSQL_UnfilteredIsByteIdenticalToTheAuthoredView(t *testing.T) {
	built, err := telemetryTokensSQL("", "")
	if err != nil {
		t.Fatalf("telemetryTokensSQL: %v", err)
	}
	authored, err := authoredTokensViewSQL()
	if err != nil {
		t.Fatalf("authoredTokensViewSQL: %v", err)
	}
	if built != authored {
		t.Errorf("the unfiltered query is not the authored view's query.\n"+
			"The join rules (overhead exclusion, half-open windows, tail attribution) are encoded "+
			"in that SQL and contract-tested against window.go. A divergent copy breaks the "+
			"contract those tests protect while leaving them green.\n--- built ---\n%s\n--- authored ---\n%s",
			built, authored)
	}
}

// TestValidateTelemetryInstance_ShapeRule documents the shape rule in the one place it is decided.
// The values are real: af_formula_instance is af-<8hex> (af-4e894132), which is why the web-side
// validVarKey rule (^[A-Za-z0-9_]+$) is too narrow — it rejects the hyphen every real id contains.
func TestValidateTelemetryInstance_ShapeRule(t *testing.T) {
	valid := []string{"af-4e894132", "af-0003257a", "wt-f0ab79", "a", "A1", "x_y-z", strings.Repeat("a", 64)}
	for _, v := range valid {
		if err := validateTelemetryInstance(v); err != nil {
			t.Errorf("validateTelemetryInstance(%q) = %v, want nil", v, err)
		}
	}

	invalid := []string{
		"", "1af", "-af", "_af", "af 1", "af.1", "af/1", "af'1", `af"1`, "af;1",
		"af\n1", "af\x001", "af%1", "af*1", strings.Repeat("a", 65),
	}
	for _, v := range invalid {
		if err := validateTelemetryInstance(v); err == nil {
			t.Errorf("validateTelemetryInstance(%q) = nil, want an error", v)
		}
	}
}

// --- fable-implement Step 3 (Root Cause B, immediate half): the logs-plane column
// extractor, hoisted ahead of the P6b fixture (R5) since its only input is the
// embedded SQL. Phase 5 (RED): tokensViewLogsPlaneColumns() is stubbed to return
// nothing — both tests below fail until Phase 6 implements the real extraction.

func TestTokensViewLogsPlaneColumns_ExtractsOnlyLogsPlaneRefs(t *testing.T) {
	got, err := tokensViewLogsPlaneColumns()
	if err != nil {
		t.Fatalf("tokensViewLogsPlaneColumns() error = %v, want nil", err)
	}

	want := []string{"af_formula_instance", "af_agent", "_timestamp", "input_tokens", "output_tokens", "af_overhead"}
	gotSet := make(map[string]bool, len(got))
	for _, c := range got {
		gotSet[c] = true
	}
	for _, w := range want {
		if !gotSet[w] {
			t.Errorf("tokensViewLogsPlaneColumns() = %v, missing logs-plane column %q (from the "+
				"billable_requests CTE's SELECT list or its af_overhead WHERE clause)", got, w)
		}
	}

	// Traces-plane columns (step_windows, FROM "traces"."default") must NEVER
	// appear — including one, service_af_agent, that differs from its logs-plane
	// counterpart (af_agent) only by the "service_" prefix, so a extractor that
	// scanned the WHOLE SQL rather than just the logs-plane CTE would silently
	// pass this check by accident unless it is checked for explicitly.
	forbidden := []string{"service_af_formula_instance", "service_af_agent", "af_step_id", "af_step_seq", "af_model", "start_time", "end_time", "service_name"}
	for _, f := range forbidden {
		if gotSet[f] {
			t.Errorf("tokensViewLogsPlaneColumns() = %v, must NOT include traces-plane column %q — "+
				"a logs-stream schema pre-flight checking for a traces-only column would report a "+
				"permanently-missing column the logs stream was never expected to carry", got, f)
		}
	}
}

func TestTokensViewLogsPlaneColumns_NonEmpty(t *testing.T) {
	got, err := tokensViewLogsPlaneColumns()
	if err != nil {
		t.Fatalf("tokensViewLogsPlaneColumns() error = %v, want nil", err)
	}
	if len(got) == 0 {
		t.Fatal("tokensViewLogsPlaneColumns() returned zero columns — a parser regression that " +
			"silently returns nothing would make the step-3 pre-flight vacuously always pass " +
			"(never finding a missing column, however malformed the view)")
	}
}

// --- fable-implement Step 6b (Root Cause B, permanent half): evidence-completeness
// interlock — R6, deliberately NOT the strict-subset form (decisions.md D5,
// investigation_report.md): "columns ⊆ recorded-real schema" is unsatisfiable once
// the evidenced arm says three of the six columns are absent, so this asserts EVERY
// referenced column is present in the fixture OR in a recorded known-absent set with
// its own evidence — a column with no evidence either way is a CI failure, not a
// production 400.

// tokensKnownAbsentColumns is decisions.md D5's Go literal: co-located with the
// interlock test it feeds (not a second data file), each entry carrying an inline
// WHY-comment citing its evidence, matching this file's own dense-WHY-comment
// convention (telemetry_where.go:31-39's tracesPredicate/logsPredicate).
func tokensKnownAbsentColumns() map[string]string {
	return map[string]string{
		// internal/telemetry/testdata/recorded-real/logs_schema_response.json — a live
		// capture against this factory's own pinned OpenObserve backend
		// (2026-07-28, see recorded-real/README.md): 74 real fields on the logs
		// stream, none named input_tokens.
		"input_tokens": "recorded-real/logs_schema_response.json: absent from the 74-field live logs schema capture (2026-07-28)",
		// Same capture, same absence.
		"output_tokens": "recorded-real/logs_schema_response.json: absent from the 74-field live logs schema capture (2026-07-28)",
		// Same capture. This is the column the live 400 (tokens_search_response_query_failed.json)
		// names directly ("No field named af_overhead").
		"af_overhead": "recorded-real/logs_schema_response.json: absent from the 74-field live logs schema capture (2026-07-28); the live 400 in openobserve-v0.91.3/tokens_search_response_query_failed.json names this exact column",
	}
}

// recordedRealLogsSchemaFields reads the captured schema fixture and returns the set
// of field names OpenObserve's real logs stream carries.
func recordedRealLogsSchemaFields(t *testing.T) map[string]bool {
	t.Helper()
	path := filepath.Join(recordedRealFixtureDir(t), "logs_schema_response.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	var answer struct {
		Schema []struct {
			Name string `json:"name"`
		} `json:"schema"`
	}
	if err := json.Unmarshal(data, &answer); err != nil {
		t.Fatalf("parsing %s: %v", path, err)
	}
	fields := make(map[string]bool, len(answer.Schema))
	for _, f := range answer.Schema {
		fields[f.Name] = true
	}
	return fields
}

func TestTelemetryWhere_EvidenceCompletenessOfLogsPlaneColumns(t *testing.T) {
	required, err := tokensViewLogsPlaneColumns()
	if err != nil {
		t.Fatalf("tokensViewLogsPlaneColumns() error = %v", err)
	}
	present := recordedRealLogsSchemaFields(t)
	knownAbsent := tokensKnownAbsentColumns()

	for _, col := range required {
		if !columnEvidenced(col, present, knownAbsent) {
			t.Errorf("column %q referenced by the authored tokens view is neither present in the "+
				"recorded-real schema fixture NOR in tokensKnownAbsentColumns() with evidence — a "+
				"column with no recorded evidence either way must be a CI failure, not a production 400",
				col)
		}
	}
}

// columnEvidenced is the interlock's core predicate, factored out so
// TestTelemetryWhere_EvidenceCompletenessRejectsAnUnclassifiedColumn can prove it
// actually rejects something rather than only asserting map non-membership.
func columnEvidenced(col string, present map[string]bool, knownAbsent map[string]string) bool {
	if present[col] {
		return true
	}
	evidence, ok := knownAbsent[col]
	return ok && strings.TrimSpace(evidence) != ""
}

// TestTelemetryWhere_RecordedRealFixtureIsNonEmptyAndParses guards against an empty
// placeholder vacuously satisfying the interlock above.
func TestTelemetryWhere_RecordedRealFixtureIsNonEmptyAndParses(t *testing.T) {
	fields := recordedRealLogsSchemaFields(t)
	if len(fields) == 0 {
		t.Fatal("recorded-real/logs_schema_response.json parses but carries zero fields — an empty " +
			"capture would make the evidence-completeness interlock vacuously pass")
	}
}

// TestTelemetryWhere_KnownAbsentColumnsCarryEvidence protects decisions.md D5's own
// requirement: every known-absent entry must carry a non-empty evidence string, not
// a bare column name.
func TestTelemetryWhere_KnownAbsentColumnsCarryEvidence(t *testing.T) {
	for col, evidence := range tokensKnownAbsentColumns() {
		if strings.TrimSpace(evidence) == "" {
			t.Errorf("tokensKnownAbsentColumns()[%q] carries no evidence — R6 requires evidence, "+
				"not a bare assertion of absence", col)
		}
	}
}

// TestTelemetryWhere_EvidenceCompletenessRejectsAnUnclassifiedColumn is the
// non-vacuity control: a synthetic column in neither the fixture nor the
// known-absent set must fail the interlock, proving it can actually fail.
func TestTelemetryWhere_EvidenceCompletenessRejectsAnUnclassifiedColumn(t *testing.T) {
	present := recordedRealLogsSchemaFields(t)
	knownAbsent := tokensKnownAbsentColumns()

	const unclassified = "af_this_column_was_never_evidenced_either_way"
	if present[unclassified] {
		t.Fatalf("test fixture bug: %q unexpectedly present in the live capture", unclassified)
	}
	if _, ok := knownAbsent[unclassified]; ok {
		t.Fatalf("test fixture bug: %q unexpectedly present in tokensKnownAbsentColumns()", unclassified)
	}
	if columnEvidenced(unclassified, present, knownAbsent) {
		t.Fatal("columnEvidenced() returned true for a column present in neither the fixture nor " +
			"the known-absent set — the interlock predicate itself is broken and would let an " +
			"unevidenced column ship")
	}
}

// recordedRealFixtureDir resolves internal/telemetry/testdata/recorded-real/
// relative to this package's directory, mirroring the existing
// openobserve-v0.91.3/ testdata path convention.
//
// It hard-fails on a missing fixture rather than letting its callers skip. The fixture is
// COMMITTED, so its absence is a repository defect — a fixture reorganisation that moved or
// pruned it — not a local condition some developers legitimately hit. Skipping green there
// would delete the step-6b interlock silently: all three tests that reach this path (through
// recordedRealLogsSchemaFields, including the non-vacuity control) would skip, the package
// would report ok, and a new column in the view SQL could then ship with no recorded
// evidence and reach production as a 400 — exactly Root Cause B, which this interlock is the
// permanent half of.
//
// Same reasoning as the AF_REQUIRE_REAL_STORE gate (.github/workflows/test.yml:106, issue
// #458: "a green skip here would say nothing about the real store"), but taken as t.Fatal
// rather than an env signal: that env arms only the `integration` job, and these tests carry
// no build tag, so they run in `unit` where it would never be set.
func recordedRealFixtureDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join("..", "telemetry", "testdata", "recorded-real")
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("the committed recorded-real fixture is missing (%s): %v\n"+
			"This interlock binds the shipped tokens view's columns to a real captured backend "+
			"schema. Without the fixture there is nothing to bind to, and a skip would report "+
			"that absence as success. Restore it, or delete the interlock deliberately.", dir, err)
	}
	return dir
}
