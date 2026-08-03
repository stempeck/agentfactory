package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// The shared golden corpus is asserted by both lanes — but only one of the two assertions the
// contract needs actually existed.
//
// Six-Sigma Gap 4 asks for "one checked-in golden fixture per contract, asserted by BOTH lanes:
// the root test asserts the emitter produces the fixture; the web test parses the fixture." The
// web half shipped. The root half did not: internal/telemetry/dto_fixtures_test.go reads the
// corpus to check the files are manifested, well-formed and secret-free, and never runs an
// emitter; the root emitter tests pin against INLINE key-set literals, not against the goldens.
//
// So the two halves never composed into a contract. A developer renaming a DTO field updates the
// inline snapshot in the same edit — that is the test that goes red — and the goldens go stale
// silently: the root lane is green because the emitter matches its own inline snapshot, the web
// lane is green because the mirror matches the stale goldens, and the console parses a shape the
// CLI no longer emits.
//
// This file is the missing half. It lives in package cmd because it has to: internal/cmd imports
// internal/telemetry, so the assertion cannot live in that package without an import cycle, and
// the DTO producers are unexported here.
//
// It compares map[string]json.RawMessage, NEVER two decodes of the same struct. Decoding both
// sides into one struct is the trap that makes this kind of test vacuous: a renamed json tag
// leaves the field zero-valued on BOTH sides, so the comparison passes while asserting nothing —
// the exact mutation this test exists to kill.

const goldenDir = "telemetry-dto"

// moduleRoot is resolved ONCE, before any test chdirs.
//
// These tests emit through the real command, which resolves the factory from the working
// directory, so they t.Chdir into a temp factory root first. findModuleRoot walks UP from the
// working directory looking for go.mod and finds none there — so a golden path computed after the
// chdir fails with "could not find go.mod", which reads like a broken assertion rather than what
// it is.
var moduleRootOnce struct {
	path string
}

func moduleRoot(t *testing.T) string {
	t.Helper()
	if moduleRootOnce.path == "" {
		moduleRootOnce.path = findModuleRoot(t)
	}
	return moduleRootOnce.path
}

func goldenPath(t *testing.T, name string) string {
	t.Helper()
	return filepath.Join(moduleRoot(t), "internal", "telemetry", "testdata", goldenDir, name)
}

// readGolden returns the golden as a key/raw-value map — the shape a contract is actually made of.
func readGolden(t *testing.T, name string) map[string]json.RawMessage {
	t.Helper()
	raw, err := os.ReadFile(goldenPath(t, name))
	if err != nil {
		t.Fatalf("read golden %s: %v", name, err)
	}
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("golden %s does not parse: %v", name, err)
	}
	return doc
}

// asKeyedJSON renders live emitter output the same way, so the two are comparable without either
// side passing through a Go type that could silently absorb a rename.
func asKeyedJSON(t *testing.T, emitted []byte) map[string]json.RawMessage {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal(emitted, &doc); err != nil {
		t.Fatalf("emitter output does not parse: %v\n%s", err, emitted)
	}
	return doc
}

// shapeOnly names, per golden, the dot-paths whose VALUE cannot be reproduced deterministically —
// a clock reading, an ephemeral port. Everything else is compared by value, at every depth.
//
// The distinction is not bookkeeping. The first draft of this file compared nested objects by KEY
// SET only, and the reviewer's own experiment walked straight through it: setting header_count to
// 99 in status-healthy-all-green.json left the root lane green, because the key was still there.
// A golden that pins names but not numbers cannot go stale in the way that actually happens.
//
// Each entry states why the value is unreproducible. A path listed here is still required to be
// present and of the same JSON type; only its value goes unchecked.
var shapeOnly = map[string]map[string]string{
	"usage-ok.json": {
		"endpoint":      "the test backend binds an ephemeral port, so host:port cannot be pinned",
		"window.end_us": "the window closes at 'now', which is a clock reading",
		"metrics.at_us": "the instant the metrics were evaluated at — also a clock reading",
	},
	"report-rows.json": {
		"rows[1].duration_ms": "an open step's duration is elapsed-so-far, so it grows between runs",
		"rows[1].started":     "the open step is seeded relative to now so it stays open",
	},
}

// assertGoldenMatchesEmitter is the contract assertion: the golden and live emitter output must be
// the same document — key for key and value for value — except at the declared unreproducible paths.
func assertGoldenMatchesEmitter(t *testing.T, name string, golden, emitted map[string]json.RawMessage) {
	t.Helper()

	var want, got any
	mustDecode(t, golden, &want)
	mustDecode(t, emitted, &got)

	compareJSON(t, name, "", want, got)
}

func mustDecode(t *testing.T, doc map[string]json.RawMessage, into *any) {
	t.Helper()
	raw, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if err := json.Unmarshal(raw, into); err != nil {
		t.Fatalf("decode: %v", err)
	}
}

// compareJSON walks both documents together, reporting the PATH of every disagreement so a failure
// says what drifted rather than dumping two payloads and leaving the reader to diff them.
func compareJSON(t *testing.T, name, path string, want, got any) {
	t.Helper()

	if reason, ok := shapeOnly[name][path]; ok {
		if got == nil {
			t.Errorf("%s: %s is compared by shape only (%s) but the emitter produced null — an "+
				"unreproducible value still has to be produced", name, path, reason)
			return
		}
		if fmt.Sprintf("%T", want) != fmt.Sprintf("%T", got) {
			t.Errorf("%s: %s changed JSON type: golden %T, emitter %T", name, path, want, got)
		}
		return
	}

	switch w := want.(type) {
	case map[string]any:
		g, ok := got.(map[string]any)
		if !ok {
			t.Errorf("%s: %s is an object in the golden and %T in the emitter output", name, pathOr(path), got)
			return
		}
		for k, wv := range w {
			gv, present := g[k]
			if !present {
				t.Errorf("%s: %s is in the golden but NOT emitted. A renamed or dropped field shows "+
					"up here; the web module's mirror parses these goldens, so a shape the CLI no "+
					"longer emits is a console reading a payload that no longer exists.",
					name, join(path, k))
				continue
			}
			compareJSON(t, name, join(path, k), wv, gv)
		}
		for k := range g {
			if _, present := w[k]; !present {
				t.Errorf("%s: %s is emitted but absent from the golden. Every key is part of the "+
					"shape both lanes agreed on; an unannounced addition is how a mirror falls behind.",
					name, join(path, k))
			}
		}
	case []any:
		g, ok := got.([]any)
		if !ok {
			t.Errorf("%s: %s is an array in the golden and %T in the emitter output", name, pathOr(path), got)
			return
		}
		if len(w) != len(g) {
			t.Errorf("%s: %s has %d entries in the golden and %d emitted. Row counts are part of the "+
				"contract: a golden pinning two rows against an emitter producing one has stopped "+
				"describing its producer.", name, pathOr(path), len(w), len(g))
			return
		}
		for i := range w {
			compareJSON(t, name, fmt.Sprintf("%s[%d]", path, i), w[i], g[i])
		}
	default:
		if !reflect.DeepEqual(want, got) {
			t.Errorf("%s: %s = %v, want %v. The golden is the committed contract read by BOTH lanes; "+
				"if the emitter is right, the golden and its README entry move in the same change.",
				name, pathOr(path), got, want)
		}
	}
}

func join(path, key string) string {
	if path == "" {
		return key
	}
	return path + "." + key
}

func pathOr(path string) string {
	if path == "" {
		return "(document root)"
	}
	return path
}

// TestTelemetryStatusGoldenReproducesTheEmitter closes the reviewer's second experiment: corrupt
// a State golden and the ROOT lane must go red. Before this test it did not — only the web lane
// noticed, and only for a parse-level corruption.
func TestTelemetryStatusGoldenReproducesTheEmitter(t *testing.T) {
	golden := readGolden(t, "status-healthy-all-green.json") // before the chdir below
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	enableTelemetryJSON(t)

	seedTelemetryGate(t, root)
	seedTelemetrySecret(t, root)
	writeTelemetryJSON(t, root, configuredEndpointJSON)
	// All three signals, because the golden documents a fully healthy factory and the comparison
	// below is by VALUE: a one-signal stub would disagree on the array length, which is itself part
	// of the contract.
	stubProbe(t, []telemetry.ProbeResult{
		{Label: "step timings", URL: "http://127.0.0.1:5080/api/default/v1/traces", Status: 200},
		{Label: "token usage", URL: "http://127.0.0.1:5080/api/default/v1/logs", Status: 200},
		{Label: "session metrics", URL: "http://127.0.0.1:5080/api/default/v1/metrics", Status: 200},
	})

	out, err := runTelemetryJSON(t, "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	assertGoldenMatchesEmitter(t, "status-healthy-all-green.json", golden, asKeyedJSON(t, []byte(out)))
}

// TestTelemetryUsageGoldenReproducesTheEmitter closes the reviewer's FIRST experiment — the one
// that survived: renaming a json tag in telemetry_usage.go left both lanes green, because no
// Usage golden existed anywhere in the repository for an emitter to be compared against.
func TestTelemetryUsageGoldenReproducesTheEmitter(t *testing.T) {
	resetReportFlags(t)

	// A POPULATED answer on both halves, so the golden pins the row shapes too — an all-empty
	// fixture would compare two empty arrays and pin nothing about what a row looks like, which is
	// exactly the vacuity this file exists to avoid.
	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, telemetrySchemaPath) {
			_, _ = w.Write([]byte(okBackendSchema))
			return
		}
		if strings.Contains(r.URL.Path, "/prometheus/") {
			// Every metric answers, each with its own value: this golden pins a HEALTHY query, so no
			// metric may be silent — a silent one would populate metrics.detail and the golden would
			// then document a partially-blind factory under the name "ok".
			//
			// The identity labels are deliberately present in the ANSWER. They are what the live
			// backend returns, and the golden proves the allowlist drops them before the payload
			// reaches a browser.
			value := map[string]string{
				"claude_code_token_usage":       "48210",
				"claude_code_cost_usage":        "0.42",
				"claude_code_session_count":     "3",
				"claude_code_active_time_total": "1897",
			}
			name := "claude_code_token_usage"
			for m := range value {
				if strings.Contains(r.URL.RawQuery, m) {
					name = m
				}
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{` +
				`"__name__":"` + name + `","af_agent":"solver","af_formula_instance":"af-4e894132",` +
				`"type":"input","model":"claude-fable-5","af_worktree_id":"wt-4e8941","af_model_profile":"default",` +
				`"user_email":"someone@example.com","session_id":"s-1"},"value":[1,"` + value[name] + `"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"took":3,"total":1,"from":0,"size":1000,"hits":[{` +
			`"formula_run":"af-4e894132","agent":"solver","model":"claude-fable-5",` +
			`"step_bucket":"s-implement","step_order":3,"requests":12,` +
			`"input_tokens":48210,"output_tokens":6104,"total_tokens":54314}]}`))
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	emitted, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal usage DTO: %v", err)
	}

	assertGoldenMatchesEmitter(t, "usage-ok.json",
		readGolden(t, "usage-ok.json"), asKeyedJSON(t, emitted))
}

// TestTelemetryReportGoldenReproducesTheEmitter covers the third family. The reviewer's
// experiments named State and Usage; the corpus pins Report too, and a golden nobody asserts is a
// golden that can go stale.
func TestTelemetryReportGoldenReproducesTheEmitter(t *testing.T) {
	golden := readGolden(t, "report-rows.json") // before the chdir below
	root := setupTestFactoryForPrime(t)
	t.Chdir(root)
	enableTelemetryJSON(t)
	seedTelemetryGate(t, root)

	// Seeded to reproduce report-rows.json exactly: one closed step and one open one, same agent,
	// same instance, same titles, same durations. The open row's start is relative to now so it
	// stays open — the two values that makes unreproducible are declared in shapeOnly.
	openedAt := time.Now().UTC().Add(-45 * time.Second).Format(telemetry.TimestampLayout)
	for _, ev := range []telemetry.StepEvent{
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: "2026-07-27T09:15:00.000Z", Agent: "manager", Formula: "offpath",
			InstanceID: "af-580-1", StepID: "s-2", StepSeq: 2, StepTitle: "Phase 2 — implement",
			Model: "claude-opus-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "prime", VerbMS: 12,
		},
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-07-27T09:15:06.388Z", Agent: "manager", Formula: "offpath",
			InstanceID: "af-580-1", StepID: "s-2", StepSeq: 2, StepTitle: "Phase 2 — implement",
			Model: "claude-opus-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "done", VerbMS: 12,
			DurationMS: 6388, Status: telemetry.StatusClosed,
		},
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: openedAt, Agent: "manager", Formula: "offpath",
			InstanceID: "af-580-1", StepID: "s-3", StepSeq: 3, StepTitle: "Phase 3 — review",
			ModelSource: telemetry.ModelSourceModelsJSON, Verb: "prime", VerbMS: 8,
		},
	} {
		if err := telemetry.AppendEvent(config.TelemetryDir(root), ev); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
	}

	out, err := runTelemetryJSON(t, "report")
	if err != nil {
		t.Fatalf("report --json: %v", err)
	}

	assertGoldenMatchesEmitter(t, "report-rows.json", golden, asKeyedJSON(t, []byte(out)))
}

// TestTelemetryGoldenCoverageIsComplete keeps the two tests above from silently covering less
// than the corpus holds. A golden nobody asserts is a golden that can go stale — which is the
// whole defect class this file addresses, one level up.
func TestTelemetryGoldenCoverageIsComplete(t *testing.T) {
	dir := filepath.Join(moduleRoot(t), "internal", "telemetry", "testdata", goldenDir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	// Families a test in this file compares against live emitter output. Each name here is asserted
	// by one of the tests above.
	asserted := map[string]bool{
		"status-healthy-all-green.json": true,
		"report-rows.json":              true,
		"usage-ok.json":                 true,
	}

	// Families are DISCOVERED from the corpus, never listed here.
	//
	// A hardcoded family list makes this test say something it does not check: add a golden for a
	// fourth DTO family and a fixed list simply never looks at it, so the corpus grows an
	// emitter-unpinned family while the test that exists to prevent exactly that reports success.
	// The corpus README states the rule this enforces, and a rule enforced by a fixed list is the
	// same false-assurance defect the mirror-header correction in this change is about.
	families := map[string][]string{}
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) != ".json" {
			continue
		}
		family, _, _ := strings.Cut(e.Name(), "-")
		families[family] = append(families[family], e.Name())
	}
	if len(families) == 0 {
		t.Fatalf("no goldens in %s", dir)
	}

	for family, members := range families {
		covered := false
		for _, m := range members {
			if asserted[m] {
				covered = true
			}
		}
		if !covered {
			t.Errorf("the %q family has %d golden(s) — %v — and no test compares any of them against "+
				"live emitter output. The corpus pins a shape that nothing produces, which reads as "+
				"coverage and is not: a rename in that family leaves every lane green while the "+
				"console parses a payload the CLI stopped emitting. Add an emitter comparison to "+
				"telemetry_goldens_test.go in the same change that adds the golden.",
				family, len(members), members)
		}
	}
}
