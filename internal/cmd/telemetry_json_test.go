package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/telemetry"
)

// The JSON surface exists because the human one reports a LAYERED truth: printTelemetryStatus
// stops at the first bad layer, so "off but installed" and "off but never installed" print the
// same three words, and the backend is only ever contacted in the fully-healthy case. An
// operator staring at an empty dashboard cannot tell "no data yet" from "no data can ever
// arrive". These tests pin the opposite property — every axis answered every time — which is
// why several of them assert states the human formatter is contractually forbidden to produce.

// enableTelemetryJSON turns on --json for one test and guarantees it is off again afterwards.
//
// resetReportFlags is called FIRST, not last: its immediate block restores every flag to its
// default, which would undo the --json we are about to set. Its t.Cleanup is what actually
// clears the flag at test end. The second, explicit cleanup here is deliberate redundancy —
// telemetryCmd is a package-level singleton, this file sorts before every other telemetry test
// file, and a leaked --json would silently reroute their assertions through the JSON path.
func enableTelemetryJSON(t *testing.T) {
	t.Helper()
	resetReportFlags(t)
	if err := telemetryCmd.Flags().Set("json", "true"); err != nil {
		t.Fatalf("set --json: %v", err)
	}
	t.Cleanup(func() {
		if err := telemetryCmd.Flags().Set("json", "false"); err != nil {
			t.Fatalf("restore --json: %v", err)
		}
	})
}

type jsonSignal struct {
	Label   string `json:"label"`
	OK      bool   `json:"ok"`
	Status  int    `json:"status"`
	Summary string `json:"summary"`
}

type jsonStateDTO struct {
	V         int    `json:"v"`
	State     string `json:"state"`
	Installed struct {
		Present     bool   `json:"present"`
		Valid       bool   `json:"valid"`
		Endpoint    string `json:"endpoint"`
		HeaderCount int    `json:"header_count"`
	} `json:"installed"`
	Recording struct {
		Enabled bool `json:"enabled"`
	} `json:"recording"`
	Backend struct {
		Probed  bool         `json:"probed"`
		Signals []jsonSignal `json:"signals"`
	} `json:"backend"`
	UnprobedCause string `json:"unprobed_cause"`
}

type jsonReportDTO struct {
	V     int    `json:"v"`
	State string `json:"state"`
	Rows  []struct {
		Agent      string      `json:"agent"`
		Step       string      `json:"step"`
		Status     string      `json:"status"`
		DurationMS json.Number `json:"duration_ms"`
		Started    string      `json:"started"`
		Model      string      `json:"model"`
		VerbMS     json.Number `json:"verb_ms"`
		InstanceID string      `json:"instance_id"`
	} `json:"rows"`
	Stats struct {
		Malformed         int `json:"malformed"`
		Dropped           int `json:"dropped"`
		DroppedUnexported int `json:"dropped_unexported"`
	} `json:"stats"`
}

// runTelemetryJSON drives the real dispatcher rather than calling a producer directly, so the
// flag wiring is exercised too, and returns the command's error instead of failing on it —
// every caller here is asserting that it is nil.
func runTelemetryJSON(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var err error
	out := captureStdout(t, func() { err = runTelemetry(telemetryCmd, args) })
	return out, err
}

func decodeState(t *testing.T, out string) jsonStateDTO {
	t.Helper()
	var dto jsonStateDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	return dto
}

func decodeReport(t *testing.T, out string) jsonReportDTO {
	t.Helper()
	var dto jsonReportDTO
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &dto); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	return dto
}

// seedTelemetrySecret writes the credential file a file: header reference resolves to.
func seedTelemetrySecret(t *testing.T, root string) {
	t.Helper()
	dir := filepath.Join(root, ".agentfactory", "secrets")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "telemetry.auth"), []byte("Basic dGVzdA=="), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}
}

func seedTelemetryGate(t *testing.T, root string) {
	t.Helper()
	if err := os.WriteFile(telemetryGateFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write gate file: %v", err)
	}
}

const configuredEndpointJSON = `{
  "endpoint": "http://127.0.0.1:5080/api/default",
  "otlp_http_path_traces": "/v1/traces",
  "headers": {"Authorization": "file:.agentfactory/secrets/telemetry.auth"},
  "protocol": "http/json",
  "export_timeout_ms": 500
}`

// stubProbe installs a probe seam that records whether it ran. Every test that configures an
// endpoint must call it, or the test opens a real socket.
func stubProbe(t *testing.T, results []telemetry.ProbeResult) *int {
	t.Helper()
	calls := 0
	orig := telemetry.Probe
	telemetry.Probe = func(cfg config.TelemetryConfig) []telemetry.ProbeResult {
		calls++
		// A probe handed an unresolved reference reports an authentication failure for what is
		// really a configuration problem — and ProbeResult.Summary would then carry the header
		// NAME into the payload, which is the one thing this surface may never emit.
		if got := cfg.Headers["Authorization"]; strings.HasPrefix(got, "file:") {
			t.Errorf("the JSON path handed the probe an unresolved secret reference: %q", got)
		}
		return results
	}
	t.Cleanup(func() { telemetry.Probe = orig })
	return &calls
}

func assertNoSecretMaterial(t *testing.T, out string) {
	t.Helper()
	for _, banned := range []string{"Authorization", "Basic dGVzdA==", "telemetry.auth"} {
		if strings.Contains(out, banned) {
			t.Errorf("output contains %q; this surface reports how many credentials are "+
				"configured, never which ones or what they hold:\n%s", banned, out)
		}
	}
}

func TestTelemetryStatusJSON_AllAxesUnconditional(t *testing.T) {
	healthy := []telemetry.ProbeResult{
		{Label: "step timings", URL: "http://127.0.0.1:5080/api/default/v1/traces", Status: 200},
		{Label: "token usage", URL: "http://127.0.0.1:5080/api/default/v1/logs", Status: 404},
		{Label: "session metrics", URL: "http://127.0.0.1:5080/api/default/v1/metrics", Status: 200},
	}

	cases := []struct {
		name        string
		gateOn      bool
		configJSON  string // empty means no telemetry.json at all
		secret      bool
		probe       []telemetry.ProbeResult
		wantProbed  bool
		wantSignals int
		wantPresent bool
		wantValid   bool
		wantCause   string
	}{
		{name: "gate off, no config"},
		{name: "gate on, no config", gateOn: true},
		{
			name:       "gate off, valid config with endpoint — the probe still runs",
			configJSON: configuredEndpointJSON, secret: true, probe: healthy,
			wantProbed: true, wantSignals: 3, wantPresent: true, wantValid: true,
		},
		{
			name: "gate on, valid config with endpoint", gateOn: true,
			configJSON: configuredEndpointJSON, secret: true, probe: healthy,
			wantProbed: true, wantSignals: 3, wantPresent: true, wantValid: true,
		},
		{
			name: "gate on, config present with no endpoint", gateOn: true,
			configJSON: `{}`, wantPresent: true, wantValid: true,
		},
		{
			name: "gate on, config invalid scheme", gateOn: true,
			configJSON: `{"endpoint":"ftp://nope"}`, wantPresent: true,
		},
		{
			name: "gate on, config malformed json", gateOn: true,
			configJSON: `{ not json`, wantPresent: true,
		},
		{
			name:       "gate off, config invalid scheme",
			configJSON: `{"endpoint":"ftp://nope"}`, wantPresent: true,
		},
		{
			name: "gate on, endpoint configured but the credential is unreadable", gateOn: true,
			configJSON: configuredEndpointJSON, secret: false,
			wantPresent: true, wantValid: true,
			wantCause: "a configured credential could not be read from its secret file",
		},
		{
			name: "gate on, probe returns zero signals", gateOn: true,
			configJSON: configuredEndpointJSON, secret: true,
			probe:      []telemetry.ProbeResult{},
			wantProbed: true, wantSignals: 0, wantPresent: true, wantValid: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := setupTestFactoryForFidelity(t)
			t.Chdir(root)
			enableTelemetryJSON(t)

			if tc.gateOn {
				seedTelemetryGate(t, root)
			}
			if tc.configJSON != "" {
				writeTelemetryJSON(t, root, tc.configJSON)
			}
			if tc.secret {
				seedTelemetrySecret(t, root)
			}
			var calls *int
			if tc.configJSON == configuredEndpointJSON {
				calls = stubProbe(t, tc.probe)
			}

			out, err := runTelemetryJSON(t, "status")
			if err != nil {
				t.Fatalf("status --json must exit 0 in every state, got: %v", err)
			}

			// No axis may be absent, whatever the state.
			var keys map[string]json.RawMessage
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &keys); err != nil {
				t.Fatalf("unmarshal %q: %v", out, err)
			}
			for _, k := range []string{"v", "state", "installed", "recording", "backend", "unprobed_cause"} {
				if _, ok := keys[k]; !ok {
					t.Errorf("missing key %q — every axis is answered in every state, or the "+
						"surface hides exactly what it exists to reveal; got %q", k, out)
				}
			}

			dto := decodeState(t, out)
			if dto.V != 1 {
				t.Errorf("v = %d, want 1", dto.V)
			}
			if dto.Recording.Enabled != tc.gateOn {
				t.Errorf("recording.enabled = %v, want %v", dto.Recording.Enabled, tc.gateOn)
			}
			if dto.Installed.Present != tc.wantPresent {
				t.Errorf("installed.present = %v, want %v", dto.Installed.Present, tc.wantPresent)
			}
			if dto.Installed.Valid != tc.wantValid {
				t.Errorf("installed.valid = %v, want %v", dto.Installed.Valid, tc.wantValid)
			}
			if dto.Backend.Probed != tc.wantProbed {
				t.Errorf("backend.probed = %v, want %v", dto.Backend.Probed, tc.wantProbed)
			}
			if len(dto.Backend.Signals) != tc.wantSignals {
				t.Errorf("len(backend.signals) = %d, want %d", len(dto.Backend.Signals), tc.wantSignals)
			}
			if dto.UnprobedCause != tc.wantCause {
				t.Errorf("unprobed_cause = %q, want %q", dto.UnprobedCause, tc.wantCause)
			}
			if tc.wantProbed && (calls == nil || *calls == 0) {
				t.Error("the probe seam was never called, so backend.probed is asserting a value " +
					"nothing computed")
			}
			// A nil slice marshals to null, which a consumer cannot iterate.
			if raw := string(keys["backend"]); strings.Contains(raw, `"signals":null`) {
				t.Errorf("backend.signals serialised as null rather than []: %s", raw)
			}
			assertNoSecretMaterial(t, out)
		})
	}
}

// TestTelemetryStatusJSON_ProbesRunWhenGateOff is the whole correction in one assertion. The
// human formatter returns after printing "telemetry: off" without reading the config, let alone
// contacting anything; a JSON path that inherited that short-circuit could never answer the
// backend axis in precisely the dark states this surface exists to name.
func TestTelemetryStatusJSON_ProbesRunWhenGateOff(t *testing.T) {
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	enableTelemetryJSON(t)

	// Deliberately NO gate file. That single absence is the test.
	seedTelemetrySecret(t, root)
	writeTelemetryJSON(t, root, configuredEndpointJSON)

	results := []telemetry.ProbeResult{
		{Label: "step timings", URL: "http://127.0.0.1:5080/api/default/v1/traces", Status: 200},
		{Label: "token usage", URL: "http://127.0.0.1:5080/api/default/v1/logs", Status: 404},
	}
	calls := stubProbe(t, results)

	out, err := runTelemetryJSON(t, "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	if *calls == 0 {
		t.Fatal("status --json never called telemetry.Probe with the gate off: the JSON path is " +
			"reusing the human formatter's gate-off short-circuit, which is exactly the " +
			"state-hiding this surface exists to eliminate")
	}

	dto := decodeState(t, out)
	if dto.Recording.Enabled {
		t.Error("recording.enabled = true, want false — no gate file was written")
	}
	if !dto.Backend.Probed {
		t.Error("backend.probed = false; the backend axis must be answered regardless of the gate")
	}
	if len(dto.Backend.Signals) != len(results) {
		t.Fatalf("len(signals) = %d, want %d", len(dto.Backend.Signals), len(results))
	}

	// Built by calling Summary() rather than by re-typing its output, so a wording change in the
	// probe cannot make the console and the terminal describe the same backend differently.
	for i, want := range results {
		if got := dto.Backend.Signals[i].Summary; got != want.Summary() {
			t.Errorf("signals[%d].summary = %q, want %q", i, got, want.Summary())
		}
		if got := dto.Backend.Signals[i].OK; got != want.OK() {
			t.Errorf("signals[%d].ok = %v, want %v", i, got, want.OK())
		}
	}
	assertNoSecretMaterial(t, out)
}

// TestTelemetryJSON_ExitZeroInEveryState covers state combinations, not verbs: on/off/unknown
// keep their existing human behaviour and exit codes, because only status and report are relayed
// to a consumer that branches on .state.
func TestTelemetryJSON_ExitZeroInEveryState(t *testing.T) {
	t.Run("config invalid, where the human path exits non-zero", func(t *testing.T) {
		root := setupTestFactoryForFidelity(t)
		t.Chdir(root)
		enableTelemetryJSON(t)
		seedTelemetryGate(t, root)
		writeTelemetryJSON(t, root, `{"endpoint":"ftp://elsewhere/x","headers":{"Authorization":"file:a b;c"}}`)

		out, err := runTelemetryJSON(t, "status")
		if err != nil {
			t.Fatalf("want exit 0 with degradation in the payload, got error: %v", err)
		}
		dto := decodeState(t, out)
		if dto.Installed.Valid {
			t.Error("installed.valid = true for an unloadable config")
		}
		if !dto.Installed.Present {
			t.Error("installed.present = false, but the file exists — absent and invalid are " +
				"different states and must stay distinguishable")
		}
		// The loader's error names the offending header key, so it may not be echoed anywhere.
		assertNoSecretMaterial(t, out)
	})

	t.Run("no factory root at all", func(t *testing.T) {
		fx := buildNestedFactoryFixture(t)
		t.Chdir(fx.markerless)
		enableTelemetryJSON(t)

		out, err := runTelemetryJSON(t, "status")
		if err != nil {
			t.Fatalf("infra failure must still exit 0: %v", err)
		}
		var env struct {
			V     int    `json:"v"`
			State string `json:"state"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		if env.State != "error" {
			t.Errorf("state = %q, want %q", env.State, "error")
		}
		if !strings.Contains(env.Error, "agentfactory workspace") {
			t.Errorf("error = %q, want it to name the missing workspace; without this the case "+
				"passes even if the fixture stops reproducing the failure", env.Error)
		}
		if env.V != 1 {
			t.Errorf("v = %d, want 1 — a consumer version-checks before it branches", env.V)
		}
	})

	t.Run("report with no records", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		enableTelemetryJSON(t)

		out, err := runTelemetryJSON(t, "report")
		if err != nil {
			t.Fatalf("report --json: %v", err)
		}
		dto := decodeReport(t, out)
		if dto.State != "ok" {
			t.Errorf("state = %q, want %q — having no data yet is not a degraded state", dto.State, "ok")
		}
		if len(dto.Rows) != 0 {
			t.Errorf("len(rows) = %d, want 0", len(dto.Rows))
		}
		var keys map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &keys); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		if string(keys["rows"]) == "null" {
			t.Error("rows serialised as null rather than []; a consumer iterates it without a guard")
		}
	})

	t.Run("report with an invalid agent filter", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		enableTelemetryJSON(t)
		if err := telemetryCmd.Flags().Set("agent", "../../../../etc/passwd"); err != nil {
			t.Fatalf("set --agent: %v", err)
		}

		out, err := runTelemetryJSON(t, "report")
		if err != nil {
			t.Fatalf("a rejected filter is reported in the payload, not as an exit code: %v", err)
		}
		var env struct {
			State string `json:"state"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
			t.Fatalf("unmarshal %q: %v", out, err)
		}
		if env.State != "error" {
			t.Errorf("state = %q, want %q; the traversing name must still be refused", env.State, "error")
		}
	})
}

func TestTelemetryStatusJSON_SchemaSnapshot(t *testing.T) {
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	enableTelemetryJSON(t)

	// The fully-populated state is the only one in which every nested object is non-degenerate.
	seedTelemetryGate(t, root)
	seedTelemetrySecret(t, root)
	writeTelemetryJSON(t, root, configuredEndpointJSON)
	stubProbe(t, []telemetry.ProbeResult{
		{Label: "step timings", URL: "http://127.0.0.1:5080/api/default/v1/traces", Status: 200},
	})

	out, err := runTelemetryJSON(t, "status")
	if err != nil {
		t.Fatalf("status --json: %v", err)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &keys); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	assertKeySet(t, keys, map[string]bool{
		"v": true, "state": true, "installed": true,
		"recording": true, "backend": true, "unprobed_cause": true,
	}, out)

	// The privacy contract lives in the nested objects: a top-level-only snapshot would not
	// notice a header_names key appearing inside installed.
	assertNestedKeySet(t, keys["installed"], map[string]bool{
		"present": true, "valid": true, "endpoint": true, "header_count": true,
	}, "installed")
	assertNestedKeySet(t, keys["recording"], map[string]bool{"enabled": true}, "recording")
	assertNestedKeySet(t, keys["backend"], map[string]bool{"probed": true, "signals": true}, "backend")

	var signals []map[string]json.RawMessage
	if err := json.Unmarshal(keys["backend"], &struct {
		Signals *[]map[string]json.RawMessage `json:"signals"`
	}{Signals: &signals}); err != nil {
		t.Fatalf("unmarshal signals: %v", err)
	}
	if len(signals) != 1 {
		t.Fatalf("len(signals) = %d, want 1", len(signals))
	}
	assertNestedKeySetMap(t, signals[0], map[string]bool{
		"label": true, "ok": true, "status": true, "summary": true,
	}, "signals[0]")

	dto := decodeState(t, out)
	if dto.Installed.HeaderCount != 1 {
		t.Errorf("installed.header_count = %d, want 1", dto.Installed.HeaderCount)
	}
	if dto.Installed.Endpoint != "http://127.0.0.1:5080/api/default" {
		t.Errorf("installed.endpoint = %q", dto.Installed.Endpoint)
	}
	assertNoSecretMaterial(t, out)
}

func TestTelemetryReportJSON_SchemaSnapshot(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	t.Chdir(root)
	enableTelemetryJSON(t)
	seedTelemetryGate(t, root)

	openedAt := time.Now().UTC().Add(-90 * time.Second).Format(telemetry.TimestampLayout)
	for _, ev := range []telemetry.StepEvent{
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: "2026-07-22T18:31:04.112Z", Agent: "manager", Formula: "offpath",
			InstanceID: "i1", StepID: "s-closed", StepSeq: 1, StepTitle: "Phase 1",
			Model: "fable-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "prime", VerbMS: 12,
		},
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
			TS: "2026-07-22T18:31:10.500Z", Agent: "manager", Formula: "offpath",
			InstanceID: "i1", StepID: "s-closed", StepSeq: 1, StepTitle: "Phase 1",
			Model: "fable-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "done", VerbMS: 77,
			DurationMS: 6388, Status: telemetry.StatusClosed,
		},
		{
			V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
			TS: openedAt, Agent: "manager", Formula: "offpath",
			InstanceID: "i1", StepID: "s-open", StepSeq: 2, StepTitle: "Phase 2",
			Model: "fable-5", ModelSource: telemetry.ModelSourceModelsJSON, Verb: "prime", VerbMS: 9,
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

	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &keys); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	assertKeySet(t, keys, map[string]bool{
		"v": true, "state": true, "rows": true, "stats": true,
	}, out)
	assertNestedKeySet(t, keys["stats"], map[string]bool{
		"malformed": true, "dropped": true, "dropped_unexported": true,
	}, "stats")

	var rows []map[string]json.RawMessage
	if err := json.Unmarshal(keys["rows"], &rows); err != nil {
		t.Fatalf("unmarshal rows: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("len(rows) = %d, want 2", len(rows))
	}
	for i, row := range rows {
		assertNestedKeySetMap(t, row, map[string]bool{
			"agent": true, "step": true, "status": true, "duration_ms": true,
			"started": true, "model": true, "verb_ms": true, "instance_id": true,
		}, "rows["+string(rune('0'+i))+"]")
	}

	// json.Number refuses a JSON string, which is the exact failure an implementation that
	// marshalled the human row type would produce: every one of its fields is a display string.
	dto := decodeReport(t, out)
	var closed, open int
	for _, r := range dto.Rows {
		switch r.Status {
		case "closed":
			closed++
			if r.DurationMS.String() != "6388" {
				t.Errorf("closed row duration_ms = %s, want 6388", r.DurationMS)
			}
			if r.InstanceID != "i1" {
				t.Errorf("closed row instance_id = %q, want %q", r.InstanceID, "i1")
			}
		case "open":
			open++
			n, convErr := r.DurationMS.Int64()
			if convErr != nil {
				t.Errorf("open row duration_ms is not an integer: %v", convErr)
			}
			if n <= 0 {
				t.Errorf("open row duration_ms = %d, want elapsed-so-far > 0", n)
			}
		}
	}
	if closed != 1 || open != 1 {
		t.Errorf("got %d closed and %d open rows, want 1 of each", closed, open)
	}
}

func TestTelemetryReportJSON_CorruptRecordsReportStats(t *testing.T) {
	// Two unparseable lines and one blank; a blank line is deliberately not counted as loss.
	const corrupt = "{ not json\n\n{\"v\":1,\"event\":\n"

	seedCorrupt := func(t *testing.T, root string, withCursor bool) {
		t.Helper()
		dir := config.TelemetryDir(root)
		if err := os.MkdirAll(filepath.Join(dir, "steps"), 0o755); err != nil {
			t.Fatalf("mkdir steps: %v", err)
		}
		if err := os.WriteFile(filepath.Join(dir, "steps", "manager.jsonl"), []byte(corrupt), 0o644); err != nil {
			t.Fatalf("write corrupt log: %v", err)
		}
		if withCursor {
			cursor := `{"last_exported_digest":"","exported_total":0,` +
				`"dropped_total":7,"dropped_unexported_total":3,` +
				`"updated_at":"2026-07-27T00:00:00.000Z"}`
			if err := os.WriteFile(filepath.Join(dir, ".cursor-manager"), []byte(cursor), 0o644); err != nil {
				t.Fatalf("write cursor: %v", err)
			}
		}
	}

	t.Run("every line corrupt is not an empty success", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		enableTelemetryJSON(t)
		seedCorrupt(t, root, false)

		out, err := runTelemetryJSON(t, "report")
		if err != nil {
			t.Fatalf("report --json: %v", err)
		}
		dto := decodeReport(t, out)
		if dto.Stats.Malformed != 2 {
			t.Errorf("stats.malformed = %d, want 2", dto.Stats.Malformed)
		}
		if len(dto.Rows) != 0 {
			t.Errorf("len(rows) = %d, want 0", len(dto.Rows))
		}
		if dto.State == "ok" {
			t.Error(`state = "ok" for a log whose every line is unreadable; "no records yet" is ` +
				"exactly the wrong thing to tell an operator whose records exist but cannot be read")
		}
		if strings.Contains(out, "No step records yet") {
			t.Error("the JSON payload carries the human formatter's prose")
		}
	})

	t.Run("drops and malformed lines are counted separately", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		enableTelemetryJSON(t)
		seedCorrupt(t, root, true)

		out, err := runTelemetryJSON(t, "report")
		if err != nil {
			t.Fatalf("report --json: %v", err)
		}
		dto := decodeReport(t, out)
		if dto.Stats.Malformed != 2 {
			t.Errorf("stats.malformed = %d, want 2", dto.Stats.Malformed)
		}
		if dto.Stats.Dropped != 7 {
			t.Errorf("stats.dropped = %d, want 7 — a malformed line is not a drop", dto.Stats.Dropped)
		}
		if dto.Stats.DroppedUnexported != 3 {
			t.Errorf("stats.dropped_unexported = %d, want 3", dto.Stats.DroppedUnexported)
		}
	})

	t.Run("loss is reported alongside data, not only instead of it", func(t *testing.T) {
		root := setupTestFactoryForPrime(t)
		t.Chdir(root)
		enableTelemetryJSON(t)
		seedCorrupt(t, root, false)

		// Appended after the corrupt bytes, so one good row and the loss coexist.
		for _, ev := range []telemetry.StepEvent{
			{
				V: telemetry.SchemaVersion, Event: telemetry.EventStepStart,
				TS: "2026-07-22T18:31:04.112Z", Agent: "manager", InstanceID: "i1",
				StepID: "s1", StepTitle: "Phase 1", Verb: "prime", VerbMS: 3,
			},
			{
				V: telemetry.SchemaVersion, Event: telemetry.EventStepEnd,
				TS: "2026-07-22T18:31:10.500Z", Agent: "manager", InstanceID: "i1",
				StepID: "s1", StepTitle: "Phase 1", Verb: "done", VerbMS: 4,
				DurationMS: 6388, Status: telemetry.StatusClosed,
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
		dto := decodeReport(t, out)
		if len(dto.Rows) != 1 {
			t.Errorf("len(rows) = %d, want 1", len(dto.Rows))
		}
		if dto.Stats.Malformed != 2 {
			t.Errorf("stats.malformed = %d, want 2 alongside the readable row", dto.Stats.Malformed)
		}
	})
}

// TestResetReportFlags_CoversEveryRegisteredFlag stops the reset helper from silently falling
// behind the flag set. telemetryCmd is a package-level singleton and this file sorts ahead of
// every other telemetry test file, so an uncleared flag reroutes their assertions.
func TestResetReportFlags_CoversEveryRegisteredFlag(t *testing.T) {
	dirty := map[string]string{"instance": "i", "agent": "a", "export": "true", "json": "true"}
	for name, value := range dirty {
		if err := telemetryCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("dirty --%s: %v", name, err)
		}
	}

	resetReportFlags(t)

	telemetryCmd.Flags().VisitAll(func(f *pflag.Flag) {
		if f.Value.String() != f.DefValue {
			t.Errorf("resetReportFlags leaves --%s = %q, want its default %q; every flag "+
				"registered on the shared command must be restored or it leaks into the next test",
				f.Name, f.Value.String(), f.DefValue)
		}
	})
}

// TestTelemetryHumanPathUnchangedByDefaultFlag is the direct pin for the promise that the
// existing surface is untouched: registering a flag must not change what happens without it.
func TestTelemetryHumanPathUnchangedByDefaultFlag(t *testing.T) {
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)
	resetReportFlags(t)

	out := captureStdout(t, func() {
		if err := runTelemetry(telemetryCmd, []string{"status"}); err != nil {
			t.Fatalf("human status: %v", err)
		}
	})
	if !strings.Contains(out, "telemetry: off") {
		t.Errorf("stdout = %q, want the human line %q", out, "telemetry: off")
	}
	if strings.Contains(out, `"v":1`) {
		t.Errorf("the human path emitted JSON; --json must default to false:\n%s", out)
	}
}

func assertKeySet(t *testing.T, keys map[string]json.RawMessage, want map[string]bool, out string) {
	t.Helper()
	if len(keys) != len(want) {
		t.Errorf("key count = %d (%v), want %d (%v)", len(keys), keysOf(keys), len(want), keysOf(wantSet(want)))
	}
	for k := range want {
		if _, ok := keys[k]; !ok {
			t.Errorf("missing key %q in output %q", k, out)
		}
	}
	for k := range keys {
		if !want[k] {
			t.Errorf("unexpected key %q in output %q", k, out)
		}
	}
}

func assertNestedKeySet(t *testing.T, raw json.RawMessage, want map[string]bool, label string) {
	t.Helper()
	var got map[string]json.RawMessage
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("unmarshal %s: %v", label, err)
	}
	assertNestedKeySetMap(t, got, want, label)
}

func assertNestedKeySetMap(t *testing.T, got map[string]json.RawMessage, want map[string]bool, label string) {
	t.Helper()
	if len(got) != len(want) {
		t.Errorf("%s key count = %d (%v), want %d (%v)", label, len(got), keysOf(got), len(want), keysOf(wantSet(want)))
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("%s missing key %q", label, k)
		}
	}
	for k := range got {
		if !want[k] {
			t.Errorf("%s has unexpected key %q", label, k)
		}
	}
}
