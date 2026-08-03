package telemetryview

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// repoRoot walks up from the package dir (the test's CWD) to the directory that holds quickstart.sh.
//
// Copied from web/internal/entrypoint/guard_test.go:24-42 rather than shared: it is a _test.go
// helper in another package, and the web module cannot import test helpers across packages. The
// walk is how a web-module test reaches the ROOT module's committed fixtures — Go's module system
// gates imports, not os.ReadFile.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 8; i++ {
		if _, err := os.Stat(filepath.Join(dir, "quickstart.sh")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	t.Fatalf("could not locate quickstart.sh by walking up from %q", dir)
	return ""
}

// sharedGolden reads one file from the ROOT module's cross-module DTO corpus.
func sharedGolden(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join(repoRoot(t), "internal", "telemetry", "testdata", "telemetry-dto", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("reading shared golden %s: %v", name, err)
	}
	return b
}

// localFixture reads one file from this package's own testdata (the Usage fixtures).
func localFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("reading local fixture %s: %v", name, err)
	}
	return b
}

// assertRoundTripsSemantically decodes golden into v, re-encodes v, and compares the two as decoded
// generic values. The goldens are INDENTED and the CLI emits COMPACT, so a byte comparison would
// compare whitespace; and a re-encode that drops or adds a key shows up here as a key-set
// difference, which is the drift this corpus exists to catch.
func assertRoundTripsSemantically(t *testing.T, golden []byte, v interface{}) {
	t.Helper()
	if err := json.Unmarshal(golden, v); err != nil {
		t.Fatalf("decode: %v", err)
	}
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	var want, got interface{}
	if err := json.Unmarshal(golden, &want); err != nil {
		t.Fatalf("decode golden as generic: %v", err)
	}
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("decode re-encoded as generic: %v", err)
	}
	if !reflect.DeepEqual(want, got) {
		t.Errorf("mirror is not faithful to the golden\n golden: %s\n mirror: %s", golden, out)
	}
}

// The eight full-State-DTO status goldens. The corpus holds nine status-*.json files; the ninth,
// status-error-infra.json, is the {v,state,error} envelope — a different shape family — and is
// asserted separately by TestTelemetryView_ErrorEnvelopeIsItsOwnFamily.
//
// Named explicitly rather than globbed so a golden DELETED upstream fails this test instead of
// silently shrinking its coverage.
var statusGoldens = []string{
	"status-absent-off-unprobed.json",
	"status-absent-on-unprobed.json",
	"status-invalid-on-unprobed.json",
	"status-valid-noendpoint-on-unprobed.json",
	"status-valid-endpoint-off-probed-ok.json",
	"status-valid-endpoint-on-probed-mixed.json",
	"status-valid-endpoint-on-unprobed-credential.json",
	"status-healthy-all-green.json",
}

var reportGoldens = []string{
	"report-empty.json",
	"report-rows.json",
	"report-corrupt.json",
}

// The Usage goldens in the SHARED corpus, as distinct from this package's local usage fixtures.
//
// The distinction is the point of Gap 4. The local fixtures are authored in this lane by the same
// hand as the mirror, so they cannot detect root-side drift; these are authored root-side and
// asserted against live emitter output there, so decoding them here is what makes the contract
// bind at both ends. A rename in internal/cmd now fails the root lane at its key-set diff and
// fails this test at the mirror.
var usageGoldens = []string{
	"usage-ok.json",
}

func TestTelemetryView_StatusGoldensDecode(t *testing.T) {
	for _, name := range statusGoldens {
		t.Run(name, func(t *testing.T) {
			golden := sharedGolden(t, name)
			var dto StateDTO
			assertRoundTripsSemantically(t, golden, &dto)

			// Rule 2 of the corpus README: signals is an array, never null — a consumer iterates it
			// without a guard.
			if dto.Backend.Signals == nil {
				t.Error("Backend.Signals decoded as nil; the corpus guarantees an array")
			}
			// Rule 4: the enum is closed at three values.
			switch dto.State {
			case StateOK, StateDegraded, StateError:
			default:
				t.Errorf("state = %q, outside the closed enum {ok,degraded,error}", dto.State)
			}
		})
	}
}

func TestTelemetryView_ReportGoldensDecode(t *testing.T) {
	for _, name := range reportGoldens {
		t.Run(name, func(t *testing.T) {
			golden := sharedGolden(t, name)
			var dto ReportDTO
			assertRoundTripsSemantically(t, golden, &dto)

			if dto.Rows == nil {
				t.Error("Rows decoded as nil; the corpus guarantees an array")
			}
			switch dto.State {
			case StateOK, StateDegraded, StateError:
			default:
				t.Errorf("state = %q, outside the closed enum", dto.State)
			}
		})
	}
}

// TestTelemetryView_ErrorEnvelopeIsItsOwnFamily pins the finding that the twelve goldens are THREE
// shape families, not two: status-error-infra.json is the {v,state,error} envelope, not a State DTO.
//
// Folding the envelope into StateDTO the way web/internal/dispatch folds its own would add an
// "error" key to all eight other status payloads on re-encode — and because ,omitempty is banned
// family-wide, that difference cannot be hidden.
func TestTelemetryView_ErrorEnvelopeIsItsOwnFamily(t *testing.T) {
	golden := sharedGolden(t, "status-error-infra.json")

	var keys map[string]json.RawMessage
	if err := json.Unmarshal(golden, &keys); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(keys) != 3 {
		t.Fatalf("envelope has %d keys %v, want exactly 3 {v,state,error}", len(keys), keys)
	}
	for _, k := range []string{"v", "state", "error"} {
		if _, ok := keys[k]; !ok {
			t.Errorf("envelope missing key %q", k)
		}
	}

	var env ErrorDTO
	assertRoundTripsSemantically(t, golden, &env)
	if env.State != StateError {
		t.Errorf("state = %q, want %q", env.State, StateError)
	}
	if env.Error == "" {
		t.Error("Error is empty; the envelope exists to carry the reason")
	}

	// The self-negative: StateDTO must NOT round-trip this golden, because it would invent the
	// installed/recording/backend keys. If this ever passes, the two families have been folded.
	var wrong StateDTO
	if err := json.Unmarshal(golden, &wrong); err != nil {
		t.Fatalf("decode into StateDTO: %v", err)
	}
	out, err := json.Marshal(&wrong)
	if err != nil {
		t.Fatalf("re-encode: %v", err)
	}
	var a, b interface{}
	_ = json.Unmarshal(golden, &a)
	_ = json.Unmarshal(out, &b)
	if reflect.DeepEqual(a, b) {
		t.Error("StateDTO round-trips the error envelope — the two shape families have been folded")
	}
}

// TestTelemetryView_UsageGoldensDecode is the web half of the cross-lane Usage contract.
//
// The sibling test below reads this package's LOCAL fixtures, which pin the mirror's parsing but
// are written in this lane and therefore cannot notice the producer changing. This one reads the
// shared corpus, whose usage-ok.json the root lane asserts against live emitter output — so the
// two lanes are now bound to the same bytes, which is what Six-Sigma Gap 4 asked for and what the
// surviving json-tag rename proved was missing.
func TestTelemetryView_UsageGoldensDecode(t *testing.T) {
	for _, name := range usageGoldens {
		t.Run(name, func(t *testing.T) {
			golden := sharedGolden(t, name)
			var dto UsageDTO
			assertRoundTripsSemantically(t, golden, &dto)

			if dto.Tokens.Rows == nil || dto.Metrics.Rows == nil {
				t.Error("rows decoded as nil; the corpus guarantees an array in every state, so a " +
					"consumer iterates without a guard")
			}
			switch dto.State {
			case UsageOK, UsageNotInstalled, UsageBackendDown, UsageCredentialRejected, UsageQueryFailed:
			default:
				t.Errorf("state = %q, outside the closed usage enum", dto.State)
			}

			// The identity labels the live backend returns on these series must not survive into a
			// payload rendered in a browser. The allowlist is enforced by the producer; this asserts
			// the golden itself never teaches the mirror to expect one.
			body := string(golden)
			for _, needle := range []string{"user_email", "user_id", "session_id", "organization_id",
				"Authorization", "Basic ", "telemetry.auth"} {
				if strings.Contains(body, needle) {
					t.Errorf("shared golden %s contains %q", name, needle)
				}
			}
		})
	}
}

func TestTelemetryView_UsageMirrorMatchesFixture(t *testing.T) {
	for _, name := range []string{"usage-ok.json", "usage-not-installed.json", "usage-query-failed.json"} {
		t.Run(name, func(t *testing.T) {
			fixture := localFixture(t, name)
			var dto UsageDTO
			assertRoundTripsSemantically(t, fixture, &dto)

			if dto.Tokens.Rows == nil {
				t.Error("Tokens.Rows decoded as nil; must be an array in every state")
			}
			if dto.Metrics.Rows == nil {
				t.Error("Metrics.Rows decoded as nil; must be an array in every state")
			}
			// The usage enum is closed at FIVE values and is distinct from the status/report enum.
			switch dto.State {
			case UsageOK, UsageNotInstalled, UsageBackendDown, UsageCredentialRejected, UsageQueryFailed:
			default:
				t.Errorf("state = %q, outside the closed usage enum", dto.State)
			}
			// No credential material, in any state.
			body := string(fixture)
			for _, needle := range []string{"Authorization", "Basic ", "telemetry.auth"} {
				if strings.Contains(body, needle) {
					t.Errorf("fixture carries %q — the DTO reports a header COUNT, never an identity", needle)
				}
			}
		})
	}
}

// TestTelemetryView_UsageCountToleratesWireVariance pins that the mirror is at least as tolerant as
// the producer. The root declares usageCount with a custom UnmarshalJSON that accepts a quoted
// string or a float (internal/cmd/telemetry_usage.go:84-98) precisely because the backend's wire
// type "is not something to assume" — a plain int field turns a single 2.0 into an unmarshal error
// for the WHOLE response, which this surface would then report as a failure: a healthy backend
// rendered as a broken one by a JSON encoding detail.
//
// A mirror stricter than its producer would re-introduce that bug one layer up.
func TestTelemetryView_UsageCountToleratesWireVariance(t *testing.T) {
	cases := []struct {
		raw  string
		want int
	}{
		{`5`, 5},
		{`2.0`, 2},
		{`"7"`, 7},
		{`""`, 0},
		{`null`, 0},
	}
	for _, c := range cases {
		t.Run(c.raw, func(t *testing.T) {
			body := `{"formula_run":"r","agent":"a","model":"m","step_bucket":"b",` +
				`"step_order":1,"requests":` + c.raw + `,"input_tokens":0,"output_tokens":0,"total_tokens":0}`
			var row UsageRowDTO
			if err := json.Unmarshal([]byte(body), &row); err != nil {
				t.Fatalf("decode %s: %v", c.raw, err)
			}
			if int(row.Requests) != c.want {
				t.Errorf("requests = %d, want %d", int(row.Requests), c.want)
			}
		})
	}
}

// TestTelemetryView_MirrorsCarryNoOmitempty pins the root's family rule on the mirror: no field is
// omitempty, anywhere. A schema snapshot pins a SHAPE; if degradation removed keys the shape would
// vary with the state and the pin would mean nothing. Degradation is always a difference in VALUE.
//
// Checked by reflection over the struct tags rather than by grep, because a grep for "omitempty"
// also matches the comments that state this very rule.
func TestTelemetryView_MirrorsCarryNoOmitempty(t *testing.T) {
	types := []interface{}{
		StateDTO{}, SignalDTO{}, ReportDTO{}, ReportRowDTO{}, ReadStatsDTO{}, ErrorDTO{},
		UsageDTO{}, UsageWindowDTO{}, UsageFiltersDTO{}, UsageTokensDTO{}, UsageRowDTO{},
		UsageMetricsDTO{}, UsageMetricRowDTO{},
	}
	var walk func(t *testing.T, rt reflect.Type, path string)
	walk = func(t *testing.T, rt reflect.Type, path string) {
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if strings.Contains(f.Tag.Get("json"), "omitempty") {
				t.Errorf("%s.%s carries omitempty; the family forbids it", path, f.Name)
			}
			ft := f.Type
			for ft.Kind() == reflect.Ptr || ft.Kind() == reflect.Slice {
				ft = ft.Elem()
			}
			if ft.Kind() == reflect.Struct && ft != rt {
				walk(t, ft, path+"."+f.Name)
			}
		}
	}
	for _, v := range types {
		rt := reflect.TypeOf(v)
		walk(t, rt, rt.Name())
	}
}

// TestTelemetryView_NotInstalledDTOsAreWellFormed pins the payloads the server renders when the
// telemetry seam is nil. Each is its OWN route's DTO family with a complete key set and a visibly
// not-ok state — never an empty dataset presented as a healthy one.
func TestTelemetryView_NotInstalledDTOsAreWellFormed(t *testing.T) {
	t.Run("status", func(t *testing.T) {
		dto := NotInstalledStatus()
		if dto.V != SchemaVersion {
			t.Errorf("v = %d, want %d", dto.V, SchemaVersion)
		}
		if dto.State == StateOK {
			t.Error("state is ok; a console with no reader has measured nothing and must not claim health")
		}
		if dto.Installed.Present {
			t.Error("installed.present is true; nothing was read")
		}
		if dto.Backend.Probed {
			t.Error("backend.probed is true; no probe ran")
		}
		if dto.Backend.Signals == nil {
			t.Error("signals is nil; must serialize as []")
		}
		if dto.UnprobedCause == "" {
			t.Error("unprobed_cause is empty; the console's own fault must be named")
		}
		assertSerializesArraysNotNull(t, dto)
	})

	t.Run("report", func(t *testing.T) {
		dto := NotInstalledReport()
		if dto.V != SchemaVersion {
			t.Errorf("v = %d, want %d", dto.V, SchemaVersion)
		}
		// The load-bearing assertion of this whole phase's honesty rule: report's enum has no
		// "not installed" value, and state:"ok" + rows:[] is byte-identical to a real healthy-empty
		// factory — the exact conflation AC-5 names ("reporting an empty dataset as though it were
		// a healthy one").
		if dto.State == StateOK {
			t.Error("state is ok; that is indistinguishable from a healthy factory with no data yet")
		}
		if dto.State != StateDegraded {
			t.Errorf("state = %q, want %q (in-enum and visibly not-ok)", dto.State, StateDegraded)
		}
		if dto.Rows == nil {
			t.Error("rows is nil; must serialize as []")
		}
		assertSerializesArraysNotNull(t, dto)
	})

	t.Run("usage", func(t *testing.T) {
		dto := NotInstalledUsage()
		if dto.V != SchemaVersion {
			t.Errorf("v = %d, want %d", dto.V, SchemaVersion)
		}
		if dto.State != UsageNotInstalled {
			t.Errorf("state = %q, want %q", dto.State, UsageNotInstalled)
		}
		if dto.Tokens.State != UsageNotInstalled || dto.Metrics.State != UsageNotInstalled {
			t.Error("the two halves must carry the same not_installed verdict as the top level")
		}
		if dto.Tokens.Rows == nil || dto.Metrics.Rows == nil {
			t.Error("rows is nil; must serialize as []")
		}
		if dto.Cause == "" {
			t.Error("cause is empty; the console's own fault must be named")
		}
		if strings.Contains(dto.Cause, "Authorization") || strings.Contains(dto.Cause, "Basic ") {
			t.Error("cause names a credential")
		}
		assertSerializesArraysNotNull(t, dto)
	})
}

// assertSerializesArraysNotNull marshals v and fails if the wire form carries a JSON null where an
// array is promised. A nil Go slice marshals to null, which is exactly the trap the corpus rule
// exists to prevent, and it is invisible in the struct.
func assertSerializesArraysNotNull(t *testing.T, v interface{}) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), "null") {
		t.Errorf("wire form carries a null: %s", b)
	}
}

// ---- relay behaviour ----

// fakeReader is a hermetic Reader double: it never spawns a process.
type fakeReader struct {
	out string
	err error
}

func (f fakeReader) TelemetryStatus(ctx context.Context) (string, error) { return f.out, f.err }
func (f fakeReader) TelemetryReport(ctx context.Context, agent, instance string) (string, error) {
	return f.out, f.err
}
func (f fakeReader) TelemetryUsage(ctx context.Context, agent, instance string) (string, error) {
	return f.out, f.err
}

var _ Reader = fakeReader{}

// TestTelemetryView_RelayFailures pins the no-silent-fallback rule. Each row is a way the relay can
// fail where the tempting implementation produces a payload an operator cannot tell apart from a
// real telemetry state.
func TestTelemetryView_RelayFailures(t *testing.T) {
	cases := []struct {
		name string
		src  fakeReader
	}{
		{"spawn error with empty stdout", fakeReader{out: "", err: errors.New("af telemetry: exit -1: ")}},
		{"empty stdout, no error", fakeReader{out: ""}},
		{"whitespace only", fakeReader{out: "   \n\t "}},
		{"not JSON at all", fakeReader{out: "af: command not found"}},
		{"truncated JSON", fakeReader{out: `{"v":1,"state":"ok"`}},
		{"state outside the closed enum", fakeReader{out: `{"v":1,"state":"weather","installed":{},"recording":{},"backend":{"signals":[]},"unprobed_cause":""}`}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := New(c.src)
			raw, err := s.Status(context.Background())
			if err == nil {
				t.Fatalf("Status = %s, want an error — a relay failure must never decode into a payload", raw)
			}
			if raw != nil {
				t.Errorf("Status returned %s alongside an error; callers must get nothing to render", raw)
			}
		})
	}
}

// TestTelemetryView_ErrorStateIsDataNotError is the counterpart: a CLI that SUCCEEDS and reports
// state:"error" in its payload is reporting a factory condition, not a relay failure. Returning a Go
// error here — which is exactly what the web/internal/dispatch template does at dispatch.go:96-98 —
// would turn a committed, fixture-backed designed payload into an HTTP error wall.
func TestTelemetryView_ErrorStateIsDataNotError(t *testing.T) {
	golden := sharedGolden(t, "status-error-infra.json")
	s := New(fakeReader{out: string(golden)})

	raw, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("state:\"error\" must relay as DATA, got a relay error: %v", err)
	}
	var got map[string]interface{}
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatalf("relayed bytes are not JSON: %v", uerr)
	}
	if got["state"] != "error" {
		t.Errorf("state = %v, want error — the payload must arrive unchanged", got["state"])
	}
}

// TestTelemetryView_RelaysOriginalBytes pins that the service does not reshape. It decodes to BRANCH
// and returns the CLI's own bytes, so a field the mirror does not know about still reaches the
// browser (additive evolution) while the mirror stays load-bearing for the branch.
func TestTelemetryView_RelaysOriginalBytes(t *testing.T) {
	// A valid payload carrying a field no mirror struct declares.
	src := `{"v":1,"state":"ok","installed":{"present":true,"valid":true,"endpoint":"http://x","header_count":1},` +
		`"recording":{"enabled":true},"backend":{"probed":true,"signals":[]},"unprobed_cause":"",` +
		`"future_field":{"added":"in a later phase"}}`
	s := New(fakeReader{out: src})

	raw, err := s.Status(context.Background())
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	var got map[string]interface{}
	if uerr := json.Unmarshal(raw, &got); uerr != nil {
		t.Fatalf("decode: %v", uerr)
	}
	if _, ok := got["future_field"]; !ok {
		t.Error("future_field was dropped — the service re-encoded a mirror instead of relaying the CLI's bytes")
	}
}

// TestTelemetryView_ReportAndUsageRelay covers the other two routes' happy paths and their own
// closed-enum checks, so the branch logic is not pinned for status alone.
func TestTelemetryView_ReportAndUsageRelay(t *testing.T) {
	t.Run("report ok", func(t *testing.T) {
		s := New(fakeReader{out: string(sharedGolden(t, "report-rows.json"))})
		if _, err := s.Report(context.Background(), "", ""); err != nil {
			t.Fatalf("Report: %v", err)
		}
	})
	t.Run("report rejects a usage-enum value", func(t *testing.T) {
		// not_installed is legal for usage and NOT legal for report — the two enums are disjoint
		// beyond "ok", and a mirror that shared one enum would accept this.
		s := New(fakeReader{out: `{"v":1,"state":"not_installed","rows":[],"stats":{"malformed":0,"dropped":0,"dropped_unexported":0}}`})
		if _, err := s.Report(context.Background(), "", ""); err == nil {
			t.Error("report accepted a usage-only state value; the enums must not be shared")
		}
	})
	t.Run("usage ok", func(t *testing.T) {
		s := New(fakeReader{out: string(localFixture(t, "usage-ok.json"))})
		if _, err := s.Usage(context.Background(), "", ""); err != nil {
			t.Fatalf("Usage: %v", err)
		}
	})
	t.Run("usage rejects a report-enum value", func(t *testing.T) {
		s := New(fakeReader{out: `{"v":1,"state":"degraded","endpoint":"","window":{},"filters":{},` +
			`"tokens":{"state":"ok","rows":[],"total":0,"truncated":false,"detail":""},` +
			`"metrics":{"state":"ok","rows":[],"at_us":0,"detail":""},"cause":""}`})
		if _, err := s.Usage(context.Background(), "", ""); err == nil {
			t.Error("usage accepted a report-only state value; the enums must not be shared")
		}
	})
}
