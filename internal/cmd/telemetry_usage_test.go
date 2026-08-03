package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// This file is the first non-integration httptest user in internal/cmd. The established hermetic
// idiom here is package-var seam substitution, and it stays the primary tool — but the
// connect-refused and hang-until-deadline cases need a real listener to be real, so a server
// appears too. Every server is t.Cleanup(srv.Close)'d and none binds a fixed port.
//
// Ordering note: this file sorts after every other file that touches the shared telemetryCmd
// singleton (json, lifecycle, status_probe, test), so it inherits whatever flag state they leave.
// Every flag-reading test here calls resetReportFlags(t) first.

// usageFixture builds a factory root whose telemetry.json points at the given endpoint and whose
// credential is a file: reference, exactly as quickstart.sh writes it.
type usageFixture struct {
	root   string
	secret string
}

func newUsageFixture(t *testing.T, endpoint string) usageFixture {
	t.Helper()
	root := setupTestFactoryForFidelity(t)

	secretDir := filepath.Join(root, ".agentfactory", "secrets")
	if err := os.MkdirAll(secretDir, 0o700); err != nil {
		t.Fatalf("mkdir secrets: %v", err)
	}
	const secret = "Basic dGVzdC1jcmVkZW50aWFsLXRoYXQtbXVzdC1uZXZlci1iZS1wcmludGVk"
	if err := os.WriteFile(filepath.Join(secretDir, "telemetry.auth"), []byte(secret+"\n"), 0o600); err != nil {
		t.Fatalf("write secret: %v", err)
	}

	cfg := fmt.Sprintf(`{
  "endpoint": %q,
  "otlp_http_path_traces": "/v1/traces",
  "headers": { "Authorization": "file:.agentfactory/secrets/telemetry.auth" },
  "protocol": "http/json",
  "export_timeout_ms": 500
}`, endpoint)
	if err := os.WriteFile(filepath.Join(root, ".agentfactory", "telemetry.json"), []byte(cfg), 0o644); err != nil {
		t.Fatalf("write telemetry.json: %v", err)
	}
	return usageFixture{root: root, secret: secret}
}

// recordedRequest is what the fake backend saw. Bodies are captured so a test can prove a value did
// NOT reach the wire — an assertion no error-return check can make.
type recordedRequest struct {
	Method string
	Path   string
	Query  string
	Body   string
	Auth   string
}

type recorder struct {
	mu   sync.Mutex
	reqs []recordedRequest
}

func (r *recorder) add(req recordedRequest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reqs = append(r.reqs, req)
}

func (r *recorder) all() []recordedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]recordedRequest, len(r.reqs))
	copy(out, r.reqs)
	return out
}

// fakeBackend answers both query families. handler decides the response so each matrix row can pick
// its own status and body.
func fakeBackend(t *testing.T, handler func(w http.ResponseWriter, r *http.Request)) (string, *recorder) {
	t.Helper()
	rec := &recorder{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// io.ReadAll, not a single Read: an http body Read returns what is available, not what was
		// asked for, and the tokens query body is several KB of SQL. A short read here truncates
		// the record mid-JSON and every body assertion built on it becomes a parse error.
		body := make([]byte, 0)
		if r.Body != nil {
			body, _ = io.ReadAll(r.Body)
		}
		rec.add(recordedRequest{
			Method: r.Method, Path: r.URL.Path, Query: r.URL.RawQuery,
			Body: string(body), Auth: r.Header.Get("Authorization"),
		})
		handler(w, r)
	}))
	t.Cleanup(srv.Close)
	return srv.URL + "/api/default", rec
}

// okBackendSchema answers the tokens schema pre-flight with every column the shipped
// view's billable_requests CTE needs — a backend this fixture calls "healthy" must
// also pass the pre-flight step 3 added, or it no longer represents a coherent
// all-succeeds scenario now that queryTokens is gated behind it. Superset of the six
// required columns, matching the shape real captures carry (extra fields present).
const okBackendSchema = `{"name":"default","schema":[` +
	`{"name":"af_formula_instance","type":"Utf8"},` +
	`{"name":"af_agent","type":"Utf8"},` +
	`{"name":"_timestamp","type":"Int64"},` +
	`{"name":"input_tokens","type":"Int64"},` +
	`{"name":"output_tokens","type":"Int64"},` +
	`{"name":"af_overhead","type":"Utf8"}]}`

func okBackend(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if strings.Contains(r.URL.Path, telemetrySchemaPath) {
		_, _ = w.Write([]byte(okBackendSchema))
		return
	}
	if strings.Contains(r.URL.Path, "/prometheus/") {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		return
	}
	_, _ = w.Write([]byte(`{"took":3,"hits":[],"total":0,"from":0,"size":50}`))
}

func usageDTO(t *testing.T, root, agent, instance string) telemetryUsageJSON {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	return telemetryUsageDTO(ctx, root, agent, instance, time.Now().UTC())
}

// TestTelemetryUsage_FailureMatrix is the AC #2 pin.
//
// It deliberately does NOT assert that five cases produce five distinct states: under the mapping
// the design prescribes (transport error -> backend_down; 401/403 -> credential_rejected; other
// non-2xx -> query_failed; 2xx -> ok) connect-refused and hang-until-deadline are BOTH transport
// errors and both mean backend_down. Asserting injectivity would force an invented sixth state and
// break AC #8. What matters — and what is asserted — is that each case maps to its SPECIFIED state
// and that the states which must stay distinguishable actually are.
func TestTelemetryUsage_FailureMatrix(t *testing.T) {
	resetReportFlags(t)

	cases := []struct {
		name      string
		handler   func(w http.ResponseWriter, r *http.Request)
		endpoint  string // when set, overrides the fake backend
		refuse    bool
		hang      bool
		wantState string
	}{
		{
			name:      "2xx is ok",
			handler:   okBackend,
			wantState: telemetryUsageStateOK,
		},
		{
			name: "401 is credential_rejected",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"code":401,"message":"Unauthorized Access"}`))
			},
			wantState: telemetryUsageStateCredentialRejected,
		},
		{
			name: "403 is credential_rejected",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
			},
			wantState: telemetryUsageStateCredentialRejected,
		},
		{
			name: "404 with an EMPTY body is query_failed",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			wantState: telemetryUsageStateQueryFailed,
		},
		{
			name: "404 with a PLAIN TEXT body is query_failed",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
				_, _ = w.Write([]byte("Organization not found"))
			},
			wantState: telemetryUsageStateQueryFailed,
		},
		{
			name: "400 with the backend's own error envelope is query_failed",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(`{"code":20004,"message":"Search field not found","error_detail":"","trace_id":"x"}`))
			},
			wantState: telemetryUsageStateQueryFailed,
		},
		{
			name: "500 is query_failed",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
			},
			wantState: telemetryUsageStateQueryFailed,
		},
		{
			name:      "connect refused is backend_down",
			refuse:    true,
			wantState: telemetryUsageStateBackendDown,
		},
		{
			name:      "hang until the deadline is backend_down",
			hang:      true,
			wantState: telemetryUsageStateBackendDown,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var endpoint string

			switch {
			case tc.refuse:
				// A server that is started to reserve a port and then closed: the address is
				// well-formed and nothing is listening. No fixed port, no flake window worth
				// naming, and it fails in microseconds rather than on a network timeout.
				srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				endpoint = srv.URL + "/api/default"
				srv.Close()
			case tc.hang:
				release := make(chan struct{})
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					select {
					case <-release:
					case <-r.Context().Done():
					}
				}))
				// Cleanups run LIFO, so registering Close FIRST means it runs LAST: the release
				// happens, every blocked handler returns, and only then does Close wait on
				// outstanding requests. The order matters because httptest.Server.Close blocks
				// until every in-flight request completes — register these the other way round and
				// Close waits on a handler that nothing has released, deadlocking the whole
				// package into the 10-minute test-binary panic.
				t.Cleanup(srv.Close)
				t.Cleanup(func() { close(release) })
				endpoint = srv.URL + "/api/default"
			default:
				endpoint, _ = fakeBackend(t, tc.handler)
			}

			fx := newUsageFixture(t, endpoint)

			// The deadline is composed, not weakened. The production floor (2s) and ceiling (10s)
			// run unchanged inside; a short PARENT deadline wins because context.WithTimeout takes
			// the earlier of the two. The hang case therefore costs milliseconds, not 2 seconds,
			// and the floor code path is still the one under test.
			parent := context.Background()
			if tc.hang {
				var cancel context.CancelFunc
				parent, cancel = context.WithTimeout(parent, 120*time.Millisecond)
				defer cancel()
			}

			started := time.Now()
			dto := telemetryUsageDTO(parent, fx.root, "", "", time.Now().UTC())
			elapsed := time.Since(started)

			if dto.State != tc.wantState {
				t.Errorf("state = %q, want %q\ntokens=%q metrics=%q",
					dto.State, tc.wantState, dto.Tokens.State, dto.Metrics.State)
			}
			if tc.hang && elapsed > 2*time.Second {
				t.Errorf("the hang case took %v; the parent deadline must win over the 2s floor, "+
					"or this test pays the production timeout on every run", elapsed)
			}

			// Exit 0 in every state is the whole contract: a consumer branches on .state, never on
			// the exit code.
			if err := emitTelemetryJSONDocument(dto); err != nil {
				t.Errorf("emitting the %s payload returned %v, want nil — every usage state exits 0",
					tc.wantState, err)
			}
		})
	}

	// The three states that must never collapse into one another, because each routes an operator
	// to a different recovery: a rejected credential is not a dead backend, and neither is a
	// backend that answered and refused the query.
	distinct := map[string]bool{
		telemetryUsageStateCredentialRejected: true,
		telemetryUsageStateBackendDown:        true,
		telemetryUsageStateQueryFailed:        true,
		telemetryUsageStateOK:                 true,
	}
	if len(distinct) != 4 {
		t.Fatal("the four post-request states are not distinct constants")
	}
}

// TestTelemetryUsage_NotInstalledIsItsOwnState covers the leaf no request can produce, so it is not
// reachable from the matrix above.
func TestTelemetryUsage_NotInstalledIsItsOwnState(t *testing.T) {
	resetReportFlags(t)

	t.Run("no telemetry.json at all", func(t *testing.T) {
		root := setupTestFactoryForFidelity(t)
		dto := usageDTO(t, root, "", "")
		if dto.State != telemetryUsageStateNotInstalled {
			t.Errorf("state = %q, want %q for a factory with no telemetry.json", dto.State, telemetryUsageStateNotInstalled)
		}
	})

	t.Run("present but no endpoint", func(t *testing.T) {
		root := setupTestFactoryForFidelity(t)
		if err := os.WriteFile(filepath.Join(root, ".agentfactory", "telemetry.json"),
			[]byte(`{"protocol":"http/json"}`), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		dto := usageDTO(t, root, "", "")
		if dto.State != telemetryUsageStateNotInstalled {
			t.Errorf("state = %q, want %q for a config with no endpoint — there is nowhere to query",
				dto.State, telemetryUsageStateNotInstalled)
		}
	})
}

// TestTelemetryUsage_GateOffStillQueries is H3, and it is the reason this verb exists in the shape
// it does. Recording-off is a fact about the future; it says nothing about the data the backend
// already holds. A gate check here would hide historical data behind a switch that never wrote it —
// the state-hiding defect one level down from the one this issue is about.
func TestTelemetryUsage_GateOffStillQueries(t *testing.T) {
	resetReportFlags(t)

	endpoint, rec := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)

	// The gate file is absent, which IS off — it is deliberately never seeded.
	if _, err := os.Stat(telemetryGateFile(fx.root)); !os.IsNotExist(err) {
		t.Fatalf("precondition: gate file should be absent, stat err = %v", err)
	}
	if telemetryFactoryEnabled(fx.root) {
		t.Fatal("precondition: telemetry must read as off")
	}

	dto := usageDTO(t, fx.root, "", "")

	if got := len(rec.all()); got == 0 {
		t.Fatal("the backend was never contacted with the gate off.\n" +
			"The gate governs RECORDING, not reading: af telemetry report already stays readable " +
			"after 'af telemetry off', and usage must too, or switching telemetry off becomes a " +
			"loss of data rather than a loss of further visibility.")
	}
	if dto.State != telemetryUsageStateOK {
		t.Errorf("state = %q with the gate off, want %q — the gate must not degrade the result",
			dto.State, telemetryUsageStateOK)
	}

	// The same run with the gate ON must be indistinguishable, which is what makes the gate
	// provably absent from this path rather than merely appearing to be.
	if err := os.WriteFile(telemetryGateFile(fx.root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write gate: %v", err)
	}
	onDTO := usageDTO(t, fx.root, "", "")
	if onDTO.State != dto.State {
		t.Errorf("state differs by gate: off=%q on=%q; the gate is not an input to this path",
			dto.State, onDTO.State)
	}

	// And no reachable state value is the gate's.
	blob, _ := json.Marshal(onDTO)
	if strings.Contains(string(blob), "recording_off") {
		t.Errorf("the payload carries recording_off:\n%s", blob)
	}
}

// TestTelemetryUsage_StateEnumIsClosed replaces AC #8's grep, which does not discriminate: against
// an absent file it prints a warning and exits 2 with empty stdout, which some harnesses read as
// success. This asserts the property the grep was reaching for.
func TestTelemetryUsage_StateEnumIsClosed(t *testing.T) {
	resetReportFlags(t)

	allowed := map[string]bool{
		telemetryUsageStateOK:                 true,
		telemetryUsageStateNotInstalled:       true,
		telemetryUsageStateBackendDown:        true,
		telemetryUsageStateCredentialRejected: true,
		telemetryUsageStateQueryFailed:        true,
	}
	if allowed["recording_off"] {
		t.Fatal("recording_off is a usage state; design-doc.md:140 excludes it deliberately")
	}

	endpoint, _ := fakeBackend(t, okBackend)
	roots := []string{
		newUsageFixture(t, endpoint).root,
		setupTestFactoryForFidelity(t),
	}
	for _, root := range roots {
		dto := usageDTO(t, root, "", "")
		if !allowed[dto.State] {
			t.Errorf("state %q is outside the closed enum", dto.State)
		}
		for _, half := range []string{dto.Tokens.State, dto.Metrics.State} {
			if half != "" && !allowed[half] {
				t.Errorf("per-query state %q is outside the closed enum", half)
			}
		}
	}
}

// TestTelemetryUsage_NoSecretInOutput is AC #9. It scans the emitted payload for the credential
// itself, for the header NAME, for the secret file's path, and for endpoint userinfo.
//
// The userinfo arm is here because the sibling status DTO already fails it: telemetry_json.go:150
// copies cfg.Endpoint verbatim and config validation checks only scheme and host, so
// http://user:pass@host round-trips a password into the payload. That is pre-existing and out of
// scope to fix, but a new DTO must not inherit it.
func TestTelemetryUsage_NoSecretInOutput(t *testing.T) {
	resetReportFlags(t)

	handlers := map[string]func(w http.ResponseWriter, r *http.Request){
		"ok": okBackend,
		"401": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":401,"message":"Unauthorized Access"}`))
		},
		"echoes the credential back": func(w http.ResponseWriter, r *http.Request) {
			// A backend that quotes the token it rejected is common, and this error text reaches
			// terminals and logs.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprintf(w, `{"code":1,"message":"rejected credential %s"}`, r.Header.Get("Authorization"))
		},
		"500 with a huge body": func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(strings.Repeat("x", 200000)))
		},
	}

	for name, h := range handlers {
		t.Run(name, func(t *testing.T) {
			endpoint, _ := fakeBackend(t, h)
			fx := newUsageFixture(t, endpoint)
			dto := usageDTO(t, fx.root, "", "")
			assertNoSecret(t, dto, fx)
		})
	}

	t.Run("deref failure names no header", func(t *testing.T) {
		endpoint, _ := fakeBackend(t, okBackend)
		fx := newUsageFixture(t, endpoint)
		// Make the credential unreadable: the resulting error from derefTelemetryHeaders names the
		// header KEY, and passing it through verbatim would print "Authorization".
		if err := os.Remove(filepath.Join(fx.root, ".agentfactory", "secrets", "telemetry.auth")); err != nil {
			t.Fatalf("remove secret: %v", err)
		}
		dto := usageDTO(t, fx.root, "", "")
		assertNoSecret(t, dto, fx)
	})

	t.Run("endpoint userinfo never reaches the payload", func(t *testing.T) {
		endpoint, _ := fakeBackend(t, okBackend)
		withUser := strings.Replace(endpoint, "http://", "http://leakuser:leakpassword@", 1)
		fx := newUsageFixture(t, withUser)
		dto := usageDTO(t, fx.root, "", "")

		blob, err := json.Marshal(dto)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		for _, bad := range []string{"leakpassword", "leakuser"} {
			if strings.Contains(string(blob), bad) {
				t.Errorf("the payload carries endpoint userinfo %q:\n%s\n"+
					"An endpoint is display-safe only after its userinfo is stripped; copying "+
					"cfg.Endpoint verbatim publishes a password.", bad, blob)
			}
		}
	})
}

func assertNoSecret(t *testing.T, dto telemetryUsageJSON, fx usageFixture) {
	t.Helper()
	blob, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	out := string(blob)

	forbidden := map[string]string{
		fx.secret:               "the credential itself",
		"Basic ":                "an Authorization header value",
		"Authorization":         "the header NAME (this surface reports counts, never identity)",
		"telemetry.auth":        "the secret file's name",
		".agentfactory/secrets": "the secret directory path",
		"file:":                 "an unresolved secret reference",
		"dGVzdC1jcmVkZW50aWFs":  "the base64 credential body",
	}
	for needle, why := range forbidden {
		if strings.Contains(out, needle) {
			t.Errorf("payload contains %q (%s):\n%s", needle, why, out)
		}
	}
}

// TestTelemetryUsage_RejectedFiltersNeverReachTheWire is the arm that makes AC #6 mean what it says.
// The builder's unit tests prove it returns an error; only a recording backend proves no request
// was ever made with the hostile value in it.
func TestTelemetryUsage_RejectedFiltersNeverReachTheWire(t *testing.T) {
	resetReportFlags(t)

	endpoint, rec := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)

	const hostile = "x'; DROP TABLE logs;--"
	dto := usageDTO(t, fx.root, hostile, "")

	for _, req := range rec.all() {
		if strings.Contains(req.Body, "DROP TABLE") || strings.Contains(req.Query, "DROP") {
			t.Errorf("a rejected filter reached the wire:\n  path=%s\n  query=%s\n  body=%s",
				req.Path, req.Query, req.Body)
		}
	}
	if len(rec.all()) != 0 {
		t.Errorf("%d request(s) were sent despite an invalid filter; validation happens BEFORE the "+
			"build, so nothing should have been constructed or sent", len(rec.all()))
	}
	if dto.State == telemetryUsageStateOK {
		t.Error("state = ok after an invalid filter; a refused query is not a successful one")
	}

	// Non-vacuity control: the same path with a VALID filter must reach the wire, or the assertion
	// above is satisfied by a function that never sends anything at all.
	rec2 := &recorder{}
	endpoint2, rec2 := fakeBackend(t, okBackend)
	fx2 := newUsageFixture(t, endpoint2)
	if dto2 := usageDTO(t, fx2.root, "solver", "af-4e894132"); dto2.State != telemetryUsageStateOK {
		t.Errorf("control arm: state = %q with a valid filter, want ok", dto2.State)
	}
	sent := rec2.all()
	if len(sent) == 0 {
		t.Fatal("control arm sent nothing; the negative assertion above proves nothing")
	}
	var sawFilter bool
	for _, req := range sent {
		if strings.Contains(req.Body, "solver") && strings.Contains(req.Body, "af-4e894132") {
			sawFilter = true
		}
	}
	if !sawFilter {
		t.Error("control arm: the validated filter never appeared in any request body")
	}
}

// TestTelemetryUsage_BothQueriesAlwaysRun pins the no-short-circuit rule. On the development host
// that motivated it, the tokens query fails 400/20004 while the metrics query succeeds — a
// first-failure return would ship a verb that returns nothing at all on the author's own factory.
func TestTelemetryUsage_BothQueriesAlwaysRun(t *testing.T) {
	resetReportFlags(t)

	tokensFails := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, telemetrySchemaPath) {
			// The pre-flight itself must pass so the _search call this test exists to
			// observe actually fires — a schema-fetch failure would (correctly, per
			// decisions.md D3) skip _search entirely, which is a different scenario.
			_, _ = w.Write([]byte(okBackendSchema))
			return
		}
		if strings.Contains(r.URL.Path, "/prometheus/") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":20004,"message":"Search field not found","error_detail":"","trace_id":"x"}`))
	}

	endpoint, rec := fakeBackend(t, tokensFails)
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	var sawSearch, sawPromQL bool
	for _, req := range rec.all() {
		if strings.HasSuffix(req.Path, "/_search") {
			sawSearch = true
		}
		if strings.Contains(req.Path, "/prometheus/api/v1/query") {
			sawPromQL = true
		}
	}
	if !sawSearch {
		t.Error("the tokens _search request was never sent")
	}
	if !sawPromQL {
		t.Error("the metrics PromQL request was never sent after the tokens query failed.\n" +
			"Both halves are issued unconditionally: an operator debugging an empty dashboard " +
			"needs to know WHICH half is broken, and the first failure is often the less " +
			"informative half.")
	}
	if dto.Tokens.State != telemetryUsageStateQueryFailed {
		t.Errorf("tokens state = %q, want %q", dto.Tokens.State, telemetryUsageStateQueryFailed)
	}
	if dto.Metrics.State != telemetryUsageStateOK {
		t.Errorf("metrics state = %q, want %q", dto.Metrics.State, telemetryUsageStateOK)
	}
	if dto.State != telemetryUsageStateQueryFailed {
		t.Errorf("overall state = %q, want %q — a half-dark surface must never summarise as ok",
			dto.State, telemetryUsageStateQueryFailed)
	}
}

// TestTelemetryUsage_QueryURLsPreserveOrgSegment pins C-12 by property rather than by shared code.
// The configured endpoint carries /api/default, and an earlier shape that rebuilt scheme://host:port
// made every request 404 silently. Asserting the prefix catches any reconstruction, which is
// strictly stronger than sharing a join helper.
func TestTelemetryUsage_QueryURLsPreserveOrgSegment(t *testing.T) {
	resetReportFlags(t)

	endpoint, rec := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)
	_ = usageDTO(t, fx.root, "", "")

	reqs := rec.all()
	if len(reqs) < 2 {
		t.Fatalf("expected both queries, saw %d requests", len(reqs))
	}
	for _, req := range reqs {
		if !strings.HasPrefix(req.Path, "/api/default/") {
			t.Errorf("request path %q does not sit under the configured org segment /api/default.\n"+
				"Query URLs are built by joining onto the configured base, never by rebuilding "+
				"scheme://host:port — dropping the segment 404s every request silently.", req.Path)
		}
	}
}

// TestTelemetryUsage_SendsTheCredential is the counterpart to NoSecretInOutput: the credential must
// reach the BACKEND even though it never reaches the payload. Without this, an implementation that
// simply never sends it would pass every leak test.
func TestTelemetryUsage_SendsTheCredential(t *testing.T) {
	resetReportFlags(t)

	endpoint, rec := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)
	_ = usageDTO(t, fx.root, "", "")

	for _, req := range rec.all() {
		if req.Auth != fx.secret {
			t.Errorf("request to %s carried Authorization %q, want the dereferenced credential.\n"+
				"An unresolved file: reference or an empty header means the deref never happened.",
				req.Path, req.Auth)
		}
	}
}

// TestTelemetryUsage_UnknownVerbFallbackListsUsage is the regression Gotcha 7 says cannot be caught
// by the existing assertions: both of them use strings.Contains(err, "usage"), and the fallback
// string starts with "usage:", so they pass whether or not the verb is listed. Equality catches it.
func TestTelemetryUsage_UnknownVerbFallbackListsUsage(t *testing.T) {
	resetReportFlags(t)
	root := setupTestFactoryForFidelity(t)
	t.Chdir(root)

	var err error
	_ = captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"definitely-not-a-verb"}) })
	if err == nil {
		t.Fatal("an unknown verb must still error")
	}

	const want = "usage: af telemetry [on|off|status|report|usage]"
	if err.Error() != want {
		t.Errorf("fallback = %q,\n    want %q\n"+
			"Asserted by equality on purpose: the existing Contains(err, \"usage\") checks match "+
			"the leading \"usage:\" and cannot tell whether the verb is listed.", err.Error(), want)
	}
}

// TestTelemetryUsage_HelpListsUsage is AC #1's tightened companion. AC #1's own first check greps
// the runtime output for "usage", which matches the probe label "token usage" on a host with a live
// backend and matches nothing at all on a host with telemetry off — it never measures the verb.
func TestTelemetryUsage_HelpListsUsage(t *testing.T) {
	if !strings.Contains(telemetryCmd.Long, "af telemetry usage") {
		t.Errorf("the Long help does not contain the literal %q:\n%s", "af telemetry usage", telemetryCmd.Long)
	}
	if !strings.Contains(telemetryCmd.Use, "usage") {
		t.Errorf("Use = %q, want it to list the usage verb", telemetryCmd.Use)
	}
}

// TestTelemetryUsage_MatchesCapturedBackendShapes reads the committed captures rather than a
// hand-authored idea of what the backend returns. Three of these shapes were not what the design
// assumed, which is exactly why they are captures.
func TestTelemetryUsage_MatchesCapturedBackendShapes(t *testing.T) {
	resetReportFlags(t)

	dir := filepath.Join(findRepoRootForUsage(t), "internal", "telemetry", "testdata", "openobserve-v0.91.3")

	read := func(name string) []byte {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("reading fixture %s: %v", name, err)
		}
		return b
	}

	cases := []struct {
		fixture   string
		status    int
		promQL    bool
		wantState string
	}{
		{"tokens_search_response_ok.json", http.StatusOK, false, telemetryUsageStateOK},
		{"tokens_search_response_query_failed.json", http.StatusBadRequest, false, telemetryUsageStateQueryFailed},
		{"search_response_401.json", http.StatusUnauthorized, false, telemetryUsageStateCredentialRejected},
		{"metrics_promql_response.json", http.StatusOK, true, telemetryUsageStateOK},
		{"metrics_promql_response_error.json", http.StatusBadRequest, true, telemetryUsageStateQueryFailed},
	}

	for _, tc := range cases {
		t.Run(tc.fixture, func(t *testing.T) {
			body := read(tc.fixture)
			endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				if strings.Contains(r.URL.Path, telemetrySchemaPath) {
					// Pre-flight must pass so the tokens _search call under test actually
					// fires — a fixture that only shapes the _search/PromQL response must
					// not accidentally get short-circuited by an unrelated schema failure.
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write([]byte(okBackendSchema))
					return
				}
				isPromQL := strings.Contains(r.URL.Path, "/prometheus/")
				if isPromQL != tc.promQL {
					// The half this fixture is not about answers healthily, so the assertion below
					// is about the half under test.
					w.Header().Set("Content-Type", "application/json")
					if isPromQL {
						_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
					} else {
						_, _ = w.Write([]byte(`{"took":1,"hits":[],"total":0,"from":0,"size":50}`))
					}
					return
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = w.Write(body)
			})

			fx := newUsageFixture(t, endpoint)
			dto := usageDTO(t, fx.root, "", "")

			got := dto.Tokens.State
			if tc.promQL {
				got = dto.Metrics.State
			}
			if got != tc.wantState {
				t.Errorf("%s -> state %q, want %q", tc.fixture, got, tc.wantState)
			}
			assertNoSecret(t, dto, fx)
		})
	}

	// The success fixture carries real rows; projecting them must produce the shipped view's
	// columns, not a re-shaped guess.
	t.Run("rows project the shipped view's columns", func(t *testing.T) {
		endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			if strings.Contains(r.URL.Path, telemetrySchemaPath) {
				_, _ = w.Write([]byte(okBackendSchema))
				return
			}
			if strings.Contains(r.URL.Path, "/prometheus/") {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"took":1,"total":1,"from":0,"size":50,"hits":[
              {"formula_run":"af-4e894132","agent":"solver","model":"opus-5","step_bucket":"step-1",
               "step_order":1,"requests":7,"input_tokens":100,"output_tokens":30,"total_tokens":130}]}`))
		})
		fx := newUsageFixture(t, endpoint)
		dto := usageDTO(t, fx.root, "", "")

		if len(dto.Tokens.Rows) != 1 {
			t.Fatalf("got %d rows, want 1", len(dto.Tokens.Rows))
		}
		r := dto.Tokens.Rows[0]
		if r.FormulaRun != "af-4e894132" || r.Agent != "solver" || r.Model != "opus-5" ||
			r.StepBucket != "step-1" || r.StepOrder != 1 || r.Requests != 7 ||
			r.InputTokens != 100 || r.OutputTokens != 30 || r.TotalTokens != 130 {
			t.Errorf("row = %+v, does not match the shipped view's output columns", r)
		}
	})
}

// TestTelemetryUsage_PayloadShapeIsStable pins the two envelope conventions the JSON family already
// holds: a version on every payload, and no omitempty anywhere — degradation is a difference in
// VALUE, never in key set, or a schema snapshot pins nothing.
func TestTelemetryUsage_PayloadShapeIsStable(t *testing.T) {
	resetReportFlags(t)

	endpoint, _ := fakeBackend(t, okBackend)
	healthy := usageDTO(t, newUsageFixture(t, endpoint).root, "", "")
	degraded := usageDTO(t, setupTestFactoryForFidelity(t), "", "")

	keys := func(v telemetryUsageJSON) map[string]bool {
		blob, err := json.Marshal(v)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		var m map[string]json.RawMessage
		if err := json.Unmarshal(blob, &m); err != nil {
			t.Fatalf("unmarshal: %v", err)
		}
		out := map[string]bool{}
		for k := range m {
			out[k] = true
		}
		return out
	}

	hk, dk := keys(healthy), keys(degraded)
	if len(hk) != len(dk) {
		t.Errorf("key set differs by state: healthy=%v degraded=%v\n"+
			"No field is omitempty in this family; a shape that varies with the state makes a "+
			"schema pin meaningless.", sortedSet(hk), sortedSet(dk))
	}
	for k := range hk {
		if !dk[k] {
			t.Errorf("key %q present when healthy and absent when degraded", k)
		}
	}
	if healthy.V != degraded.V || healthy.V == 0 {
		t.Errorf("v = %d / %d, want the same non-zero schema version on every payload", healthy.V, degraded.V)
	}

	// Empty must marshal as [] and not null, or a consumer cannot iterate it.
	blob, _ := json.Marshal(degraded)
	if strings.Contains(string(blob), `"rows":null`) {
		t.Errorf("a nil row slice marshalled to null; empty is a real answer and must look like one:\n%s", blob)
	}
}

func sortedSet(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// findRepoRootForUsage walks up to the module root so fixture paths do not depend on where the test
// binary was invoked from.
func findRepoRootForUsage(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, "go.mod")); statErr == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find the module root")
		}
		dir = parent
	}
}

// TestTelemetryUsage_WindowStartIsAcceptableToTheBackend guards a defect that only a real backend
// could reveal, and that therefore only a written-down rule can keep out.
//
// The window is all-time by design. The obvious encoding of "all time" is start_time 0 — and this
// backend rejects that outright with 400 "[file_list] invalid time range", because it reads zero as
// "unset". Every fake in this file happily accepts a zero start, so the whole suite passed while
// the real command returned query_failed against a healthy backend. That is the shape of bug the
// sideways check exists to find and the shape a unit test cannot.
func TestTelemetryUsage_WindowStartIsAcceptableToTheBackend(t *testing.T) {
	resetReportFlags(t)

	endpoint, rec := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if dto.Window.StartUS <= 0 {
		t.Errorf("window.start_us = %d; the backend reads a zero start_time as unset and answers "+
			"400 invalid time range. Use the smallest POSITIVE microsecond instead — it is "+
			"indistinguishable from all-time for any record this system can hold.", dto.Window.StartUS)
	}
	if dto.Window.EndUS <= dto.Window.StartUS {
		t.Errorf("window is not ordered: start=%d end=%d", dto.Window.StartUS, dto.Window.EndUS)
	}

	// And the value actually sent must match the value reported, or the echoed window is decoration.
	var sawSearch bool
	for _, req := range rec.all() {
		if !strings.HasSuffix(req.Path, telemetrySearchPath) {
			continue
		}
		sawSearch = true
		var body struct {
			Query struct {
				StartTime int64 `json:"start_time"`
				EndTime   int64 `json:"end_time"`
			} `json:"query"`
		}
		if err := json.Unmarshal([]byte(req.Body), &body); err != nil {
			t.Fatalf("the _search body is not JSON: %v\n%s", err, req.Body)
		}
		if body.Query.StartTime <= 0 {
			t.Errorf("the _search request carried start_time=%d, which this backend rejects", body.Query.StartTime)
		}
		if body.Query.StartTime != dto.Window.StartUS || body.Query.EndTime != dto.Window.EndUS {
			t.Errorf("the reported window (%d..%d) is not the window queried (%d..%d)",
				dto.Window.StartUS, dto.Window.EndUS, body.Query.StartTime, body.Query.EndTime)
		}
	}
	if !sawSearch {
		t.Fatal("no _search request was recorded; this test proves nothing")
	}
}

// TestTelemetryUsage_AlwaysExitsZeroEvenBeforeDispatch closes the last hole in "usage always exits
// 0; branch on .state".
//
// Two failures happen BEFORE the verb switch is reached — getwd failing, and the factory root not
// resolving — and both originally returned a bare error, which cobra turns into exit 1 with prose
// on stderr. The flagged path was already covered because --json was read first; the unflagged one
// was not, and usage has no human rendering to fall back to. A consumer that got exit 1 and an
// "Error:" line there could not tell a broken invocation from an empty result, which is the
// distinction this whole surface exists to preserve.
//
// Found by running the real binary in a directory the invoker seam refuses — not by any unit test,
// because every fixture here hands the producer a root that already resolved.
func TestTelemetryUsage_AlwaysExitsZeroEvenBeforeDispatch(t *testing.T) {
	resetReportFlags(t)

	// A directory with no factory marker anywhere above it: resolveInvokerRoot cannot answer.
	t.Chdir(t.TempDir())

	var err error
	out := captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"usage"}) })

	if err != nil {
		t.Errorf("runTelemetry(usage) returned %v; usage must exit 0 in every state, including the "+
			"ones that fail before the verb is dispatched", err)
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(strings.TrimSpace(out)), &payload); jsonErr != nil {
		t.Fatalf("usage emitted something that is not JSON (%v):\n%s", jsonErr, out)
	}
	if payload["state"] == nil || payload["state"] == "" {
		t.Errorf("payload carries no state for a consumer to branch on:\n%s", out)
	}
	if payload["v"] == nil {
		t.Errorf("payload carries no schema version:\n%s", out)
	}

	// The human verbs keep their non-zero exits — this change must not widen to them.
	resetReportFlags(t)
	if statusErr := runTelemetry(telemetryCmd, []string{"status"}); statusErr == nil {
		t.Error("human status must still error when the factory root cannot be resolved; " +
			"the machine-readable exemption is scoped to usage and --json")
	}
}

// TestTelemetryUsage_RejectedFilterIsNeverEchoed closes a hole that the wire-level assertions could
// not see: a value can be kept out of every request and still be handed straight back to the caller
// in the response payload.
//
// The DTO reports the filters it applied, and echoing an unvalidated one there gives a refused
// injection payload a second delivery route — into a browser that renders this JSON, and into every
// log that stores it. The value is reported only after validation accepts it.
func TestTelemetryUsage_RejectedFilterIsNeverEchoed(t *testing.T) {
	resetReportFlags(t)

	endpoint, _ := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)

	hostile := []string{
		"x'; DROP TABLE logs;--",
		`<script>alert(1)</script>`,
		"x\x1b[31mred",
		strings.Repeat("a", 200),
	}
	for _, bad := range hostile {
		t.Run("agent", func(t *testing.T) {
			blob, err := json.Marshal(usageDTO(t, fx.root, bad, ""))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(blob), bad) {
				t.Errorf("the payload echoes the rejected --agent value verbatim:\n%s", blob)
			}
		})
		t.Run("instance", func(t *testing.T) {
			blob, err := json.Marshal(usageDTO(t, fx.root, "", bad))
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(blob), bad) {
				t.Errorf("the payload echoes the rejected --instance value verbatim:\n%s", blob)
			}
		})
	}

	// Non-vacuity: an ACCEPTED filter must still be reported, or "never echo" is satisfied by never
	// reporting anything.
	dto := usageDTO(t, fx.root, "solver", "af-4e894132")
	if dto.Filters.Agent != "solver" || dto.Filters.Instance != "af-4e894132" {
		t.Errorf("filters = %+v, want the accepted values reported back", dto.Filters)
	}
}

// TestTelemetryUsage_ClosedEnumHoldsAtTheCommandBoundary is the command-level twin of
// TestTelemetryUsage_StateEnumIsClosed, which calls the producer directly and therefore cannot see
// what the verb actually prints.
//
// The pre-dispatch failures originally emitted the generic error envelope — state "error", a
// different key set — which put a sixth value outside the closed enum on exactly the payloads a
// consumer is least equipped to interpret.
func TestTelemetryUsage_ClosedEnumHoldsAtTheCommandBoundary(t *testing.T) {
	resetReportFlags(t)

	allowed := map[string]bool{
		telemetryUsageStateOK:                 true,
		telemetryUsageStateNotInstalled:       true,
		telemetryUsageStateBackendDown:        true,
		telemetryUsageStateCredentialRejected: true,
		telemetryUsageStateQueryFailed:        true,
	}

	keysOfPayload := func(out string) map[string]bool {
		t.Helper()
		var m map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &m); err != nil {
			t.Fatalf("not JSON (%v):\n%s", err, out)
		}
		got := map[string]bool{}
		for k := range m {
			got[k] = true
		}
		return got
	}

	// A healthy run, for the reference key set.
	endpoint, _ := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)
	t.Chdir(fx.root)
	healthyOut := captureStdout(t, func() {
		if err := runTelemetry(telemetryCmd, []string{"usage"}); err != nil {
			t.Fatalf("healthy usage: %v", err)
		}
	})
	healthyKeys := keysOfPayload(healthyOut)

	// The pre-dispatch failure: a directory with no factory above it.
	resetReportFlags(t)
	t.Chdir(t.TempDir())
	var err error
	brokenOut := captureStdout(t, func() { err = runTelemetry(telemetryCmd, []string{"usage"}) })
	if err != nil {
		t.Errorf("usage returned %v; it must exit 0 in every state", err)
	}

	var broken map[string]any
	if unmarshalErr := json.Unmarshal([]byte(strings.TrimSpace(brokenOut)), &broken); unmarshalErr != nil {
		t.Fatalf("not JSON (%v):\n%s", unmarshalErr, brokenOut)
	}
	state, _ := broken["state"].(string)
	if !allowed[state] {
		t.Errorf("state %q is outside the closed enum; the pre-dispatch payload must be a Usage DTO, "+
			"not the generic error envelope", state)
	}

	brokenKeys := keysOfPayload(brokenOut)
	if len(brokenKeys) != len(healthyKeys) {
		t.Errorf("key set varies with state: healthy=%v degraded=%v\n"+
			"No field in this family is omitempty; a shape that changes with the state makes a "+
			"schema pin meaningless.", sortedSet(healthyKeys), sortedSet(brokenKeys))
	}
	for k := range healthyKeys {
		if !brokenKeys[k] {
			t.Errorf("key %q present when healthy, absent in the pre-dispatch payload", k)
		}
	}
}

// TestAssertSpliceable_FiresOnAWidenedValidator makes the backstop reachable rather than decorative.
//
// The previous form compared two expressions over the BUILT clause, which the builder itself had
// just filled with quote delimiters — a tautology that could never fire. A guard that provably
// cannot fire, sitting under a comment explaining when it will, is worse than no guard, because it
// is read as protection. This asserts the real one against the inputs.
func TestAssertSpliceable_FiresOnAWidenedValidator(t *testing.T) {
	dangerous := []string{`a'--`, `a"b`, `a\b`, "a;b", "a\x00b", "a\nb", "a`b"}
	for _, v := range dangerous {
		if err := assertSpliceable(v); err == nil {
			t.Errorf("assertSpliceable(%q) = nil; if a validator is ever widened to admit this, the "+
				"splice must refuse rather than quietly produce a query", v)
		}
		if err := assertSpliceable("safe", v); err == nil {
			t.Errorf("assertSpliceable(safe, %q) = nil", v)
		}
	}
	for _, v := range []string{"solver", "af-4e894132", "wt-f0ab79", "a_b-c", ""} {
		if err := assertSpliceable(v); err != nil {
			t.Errorf("assertSpliceable(%q) = %v, want nil", v, err)
		}
	}
}

// TestTelemetryUsage_MetricsReportTheInstantTheyQueried pins the honesty fix for the metrics half.
// The window belongs to the tokens query; PromQL instant queries answer at one moment, and the
// payload has to say which moment rather than let the window imply a range it never asked for.
func TestTelemetryUsage_MetricsReportTheInstantTheyQueried(t *testing.T) {
	resetReportFlags(t)

	endpoint, rec := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)

	now := time.Now().UTC()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	dto := telemetryUsageDTO(ctx, fx.root, "", "", now)

	if dto.Metrics.AtUS != now.UnixMicro() {
		t.Errorf("metrics.at_us = %d, want %d", dto.Metrics.AtUS, now.UnixMicro())
	}

	var sawPromQL bool
	for _, req := range rec.all() {
		// Excludes the existence-probe's series requests (fable-implement Step 4):
		// those are a bounded existence question, not an instant PromQL query, and
		// carry no "time" param by design — pinned separately by
		// TestTelemetryUsage_SilentMetricExistenceProbe_InstantReadSemanticsUnchanged.
		if !strings.Contains(req.Path, "/prometheus/") || strings.Contains(req.Path, telemetrySeriesPath) {
			continue
		}
		sawPromQL = true
		values, err := url.ParseQuery(req.Query)
		if err != nil {
			t.Fatalf("parsing %q: %v", req.Query, err)
		}
		got := values.Get("time")
		if got == "" {
			t.Error("the PromQL request carries no time parameter, so it is evaluated at whenever it " +
				"happens to arrive — which is not the instant the payload reports")
			continue
		}
		if want := strconv.FormatInt(now.Unix(), 10); got != want {
			t.Errorf("PromQL time = %q, want %q (the instant reported as metrics.at_us)", got, want)
		}
	}
	if !sawPromQL {
		t.Fatal("no PromQL request recorded; this test proves nothing")
	}
}

// TestTelemetryUsage_TotalBudgetIsBounded pins the ceiling as a property of the VERB, not of each
// request. Five sequential requests each holding their own 2s-to-10s envelope let a dead backend
// hold the command for the sum of them; the comment claimed a ceiling that the code did not impose.
//
// The production-shape arm below uses context.Background() on purpose. An earlier version passed a
// short parent deadline of its own, which meant the test bounded the test rather than the code: it
// still passed with the per-verb budget deleted. This one costs the real 2s floor once, and that is
// the price of an assertion that can actually fail.
func TestTelemetryUsage_TotalBudgetIsBounded(t *testing.T) {
	resetReportFlags(t)

	newDeadBackend := func(t *testing.T) string {
		t.Helper()
		release := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			select {
			case <-release:
			case <-r.Context().Done():
			}
		}))
		// Registered first so it runs LAST (cleanups are LIFO): the release unblocks every handler
		// before Close waits on outstanding requests.
		t.Cleanup(srv.Close)
		t.Cleanup(func() { close(release) })
		return srv.URL + "/api/default"
	}

	t.Run("no caller deadline: the configured envelope bounds the whole verb", func(t *testing.T) {
		fx := newUsageFixture(t, newDeadBackend(t))
		cfg, err := config.LoadTelemetryConfig(fx.root)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		envelope := usageTimeout(*cfg)

		started := time.Now()
		dto := telemetryUsageDTO(context.Background(), fx.root, "", "", time.Now().UTC())
		elapsed := time.Since(started)

		// Five requests share one envelope, so the total is bounded by it plus slack. Without the
		// per-verb budget this is five envelopes, which this bound rejects.
		if limit := envelope + 2*time.Second; elapsed > limit {
			t.Errorf("the verb took %v against an unresponsive backend; the configured envelope is "+
				"%v, so the bound is %v.\nA per-request budget multiplies by the number of queries, "+
				"which is not a ceiling.", elapsed, envelope, limit)
		}
		if dto.State != telemetryUsageStateBackendDown {
			t.Errorf("state = %q, want %q", dto.State, telemetryUsageStateBackendDown)
		}
	})

	t.Run("a caller deadline still wins", func(t *testing.T) {
		fx := newUsageFixture(t, newDeadBackend(t))
		const budget = 300 * time.Millisecond
		ctx, cancel := context.WithTimeout(context.Background(), budget)
		defer cancel()

		started := time.Now()
		dto := telemetryUsageDTO(ctx, fx.root, "", "", time.Now().UTC())
		elapsed := time.Since(started)

		if elapsed > 3*budget {
			t.Errorf("took %v with a %v caller deadline; the 2s floor must never become a minimum "+
				"wait for a caller who asked for less", elapsed, budget)
		}
		if dto.State != telemetryUsageStateBackendDown {
			t.Errorf("state = %q, want %q", dto.State, telemetryUsageStateBackendDown)
		}
	})
}

// TestTelemetryUsage_ReportsTruncation is T-6's rule made mechanical: a cap or a partial answer that
// the payload does not admit to is truncation presented as totality.
func TestTelemetryUsage_ReportsTruncation(t *testing.T) {
	resetReportFlags(t)

	cases := []struct {
		name          string
		body          string
		wantTruncated bool
		wantTotal     int
	}{
		{
			name:          "backend says the answer is partial",
			body:          `{"took":1,"total":1,"is_partial":true,"hits":[{"agent":"a"}]}`,
			wantTruncated: true,
			wantTotal:     1,
		},
		{
			name:          "backend matched more than it returned",
			body:          `{"took":1,"total":9000,"is_partial":false,"hits":[{"agent":"a"}]}`,
			wantTruncated: true,
			wantTotal:     9000,
		},
		{
			name:          "complete answer",
			body:          `{"took":1,"total":1,"is_partial":false,"hits":[{"agent":"a"}]}`,
			wantTruncated: false,
			wantTotal:     1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, telemetrySchemaPath) {
					_, _ = w.Write([]byte(okBackendSchema))
					return
				}
				if strings.Contains(r.URL.Path, "/prometheus/") {
					_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
					return
				}
				_, _ = w.Write([]byte(tc.body))
			})
			fx := newUsageFixture(t, endpoint)
			dto := usageDTO(t, fx.root, "", "")

			if dto.Tokens.Truncated != tc.wantTruncated {
				t.Errorf("tokens.truncated = %v, want %v — an answer the operator cannot tell is "+
					"partial is worse than no answer", dto.Tokens.Truncated, tc.wantTruncated)
			}
			if dto.Tokens.Total != tc.wantTotal {
				t.Errorf("tokens.total = %d, want %d", dto.Tokens.Total, tc.wantTotal)
			}
		})
	}
}

// TestTelemetryUsage_CredentialStraddlingTheBodyBoundIsNotLeaked is the arm the original leak test
// was missing. Every hostile handler there put the credential early in a short body, so redaction
// always ran on a string that contained the whole value.
//
// The defect it could not see: the failure body was truncated to the display bound BEFORE redaction,
// so a credential that began just before the cut had its tail removed, no longer matched the whole
// value the redactor looks for, and its prefix was published in `detail`.
func TestTelemetryUsage_CredentialStraddlingTheBodyBoundIsNotLeaked(t *testing.T) {
	resetReportFlags(t)

	for _, pad := range []int{490, 492, 500, 505, 511, 512, 520} {
		t.Run(fmt.Sprintf("credential begins at byte %d", pad), func(t *testing.T) {
			endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				_, _ = fmt.Fprint(w, strings.Repeat("p", pad)+r.Header.Get("Authorization")+" trailing")
			})
			fx := newUsageFixture(t, endpoint)
			dto := usageDTO(t, fx.root, "", "")

			assertNoSecret(t, dto, fx)

			// A prefix is a leak too. Anything from the credential longer than the shortest
			// coincidence worth caring about must not survive into the payload.
			// Scanned on the body-derived part only. The HTTP status line is not secret-derived
			// and legitimately shares short substrings with any credential ("Bad Request" contains
			// "Ba"), so including it would assert about the wrong string.
			for _, detail := range []string{dto.Tokens.Detail, dto.Metrics.Detail} {
				if detail == "" {
					continue
				}
				body := detailBody(t, detail)
				for n := len(fx.secret); n >= 1; n-- {
					if strings.Contains(body, fx.secret[:n]) {
						t.Fatalf("a %d-character prefix of the credential survived into detail:\n%s", n, body)
					}
				}
			}
		})
	}
}

// TestTelemetryUsage_NumericColumnsToleratePlausibleEncodings guards a failure mode that only shows
// up against real data, which this query has never seen: the shipped tokens view does not execute
// against the reference backend, so nothing has ever exercised this decode with a live aggregate.
//
// A SQL sum() rendered as 2.0 rather than 2 would fail the unmarshal for the WHOLE response, and the
// surface would report a healthy backend as query_failed on a JSON encoding detail.
func TestTelemetryUsage_NumericColumnsToleratePlausibleEncodings(t *testing.T) {
	resetReportFlags(t)

	bodies := map[string]string{
		"integers": `{"total":1,"hits":[{"formula_run":"af-1","agent":"a","model":"m","step_bucket":"s",
                      "step_order":1,"requests":2,"input_tokens":100,"output_tokens":30,"total_tokens":130}]}`,
		"floats": `{"total":1,"hits":[{"formula_run":"af-1","agent":"a","model":"m","step_bucket":"s",
                    "step_order":1.0,"requests":2.0,"input_tokens":100.0,"output_tokens":30.0,"total_tokens":130.0}]}`,
		"exponent": `{"total":1,"hits":[{"formula_run":"af-1","agent":"a","model":"m","step_bucket":"s",
                      "step_order":1,"requests":2,"input_tokens":1e2,"output_tokens":30,"total_tokens":130}]}`,
		"quoted": `{"total":1,"hits":[{"formula_run":"af-1","agent":"a","model":"m","step_bucket":"s",
                    "step_order":"1","requests":"2","input_tokens":"100","output_tokens":"30","total_tokens":"130"}]}`,
		"null": `{"total":1,"hits":[{"formula_run":"af-1","agent":"a","model":"m","step_bucket":"s",
                  "step_order":1,"requests":2,"input_tokens":100,"output_tokens":30,"total_tokens":null}]}`,
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				if strings.Contains(r.URL.Path, telemetrySchemaPath) {
					_, _ = w.Write([]byte(okBackendSchema))
					return
				}
				if strings.Contains(r.URL.Path, "/prometheus/") {
					_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
					return
				}
				_, _ = w.Write([]byte(body))
			})
			fx := newUsageFixture(t, endpoint)
			dto := usageDTO(t, fx.root, "", "")

			if dto.Tokens.State != telemetryUsageStateOK {
				t.Fatalf("tokens state = %q (%s); a numeric encoding must not turn a healthy answer "+
					"into a failed query", dto.Tokens.State, dto.Tokens.Detail)
			}
			if len(dto.Tokens.Rows) != 1 {
				t.Fatalf("got %d rows, want 1", len(dto.Tokens.Rows))
			}
			row := dto.Tokens.Rows[0]
			if row.InputTokens != 100 || row.Requests != 2 {
				t.Errorf("row decoded as %+v; want input_tokens 100 and requests 2 regardless of encoding", row)
			}
			// And the emitted payload renders them as numbers, not as whatever came in.
			blob, err := json.Marshal(dto)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(blob), `"input_tokens":100`) {
				t.Errorf("input_tokens is not emitted as a bare number:\n%s", blob)
			}
		})
	}
}

// TestTelemetryUsage_MetricRowsCarryTheirDistinguishingLabels closes a real hole: the live backend
// answers claude_code_token_usage with eight series for one (agent, instance) pair, split by token
// type. Reporting only (metric, agent, instance) collapses them into eight rows that look like
// duplicates and cannot be told apart or summed correctly.
//
// The allowlist is asserted in both directions, because these series also carry user_email, user_id,
// session_id and organization_id, and this payload is rendered in a browser.
func TestTelemetryUsage_MetricRowsCarryTheirDistinguishingLabels(t *testing.T) {
	resetReportFlags(t)

	fixture, err := os.ReadFile(filepath.Join(findRepoRootForUsage(t),
		"internal", "telemetry", "testdata", "openobserve-v0.91.3", "metrics_promql_response.json"))
	if err != nil {
		t.Fatalf("reading the captured PromQL response: %v", err)
	}

	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/prometheus/") {
			_, _ = w.Write(fixture)
			return
		}
		_, _ = w.Write([]byte(`{"took":1,"total":0,"hits":[]}`))
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if len(dto.Metrics.Rows) == 0 {
		t.Fatal("the captured response produced no metric rows")
	}

	// Every row must be distinguishable from every other. Without labels the captured fixture
	// collapses several series onto one key, which is the defect.
	seen := map[string]int{}
	for _, r := range dto.Metrics.Rows {
		key := r.Metric + "|" + r.Agent + "|" + r.Instance
		for _, name := range telemetryUsageMetricLabels {
			key += "|" + name + "=" + r.Labels[name]
		}
		seen[key]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("%d rows share the key %q — series that differ only in value are indistinguishable "+
				"to a reader and cannot be attributed or summed", n, key)
		}
	}

	// And no identifying label may ride along.
	blob, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	for _, forbidden := range []string{
		"user_email", "user_id", "user_account_id", "user_account_uuid",
		"session_id", "organization_id", "prompt",
	} {
		if strings.Contains(string(blob), forbidden) {
			t.Errorf("the payload carries the identifying label %q; the label set is an allowlist, "+
				"not a copy of whatever the backend attached", forbidden)
		}
	}
}

// TestTelemetryUsage_RedactionSurvivesReEncoding covers the gap a byte-exact redactor leaves.
//
// A backend that quotes the token it rejected renders it into ITS OWN response format. JSON escaping
// turns / into \/ and " into \", and an error that echoes a URL percent-encodes. A redactor matching
// only the verbatim bytes sees none of those, so it publishes the credential while appearing to have
// removed it. The reference backend uses serde_json and never escapes a solidus, so no captured
// fixture can show this — it has to be constructed.
func TestTelemetryUsage_RedactionSurvivesReEncoding(t *testing.T) {
	secrets := []string{
		`Basic abc/def+ghi=jkl0123456`,
		`Basic a"b"c0123456789`,
		`Basic a\b\c0123456789`,
		`tok`,
		`xy`,
		`z`,
	}

	for _, secret := range secrets {
		t.Run(secret, func(t *testing.T) {
			// Every rendering a backend might choose.
			jsonEscaped := strings.Trim(jsonEncodedString(t, secret), `"`)
			slashEscaped := strings.ReplaceAll(secret, "/", `\/`)

			for _, echo := range []string{secret, jsonEscaped, slashEscaped,
				url.QueryEscape(secret), url.PathEscape(secret)} {

				body := detailBody(t, usageDetail("400 Bad Request",
					[]byte(`{"message":"rejected credential `+echo+`"}`),
					map[string]string{"Authorization": secret}))

				if strings.Contains(body, secret) {
					t.Errorf("detail carries the credential verbatim after the backend echoed it as %q:\n%s",
						echo, body)
				}
				if echo != secret && strings.Contains(body, echo) {
					t.Errorf("detail carries the %q-encoded credential:\n%s", echo, body)
				}
			}
		})
	}
}

// TestTelemetryUsage_ShortCredentialIsStillRedacted pins the asymmetry between header names and
// header values. A length floor on values would publish a short credential verbatim, and "no
// credential material in any output path, in any state" has no length qualifier in it.
func TestTelemetryUsage_ShortCredentialIsStillRedacted(t *testing.T) {
	for _, secret := range []string{"a", "ab", "abc", "abcd", "abcde", "abcdef", "abcdefg"} {
		body := detailBody(t, usageDetail("400 Bad Request",
			[]byte(`{"message":"rejected `+secret+`"}`),
			map[string]string{"Authorization": secret}))
		if outside := withoutRedactionMarkers(body); strings.Contains(outside, secret) {
			t.Errorf("a %d-byte credential survived into detail:\n%s", len(secret), body)
		}
	}

	// And a short PREFIX pulled to the tail by the truncation must not survive either.
	long := "Basic " + strings.Repeat("S", 40)
	for k := 1; k <= 12; k++ {
		raw := strings.Repeat("p", int(usageErrorBodyLimit)-k) + long
		body := detailBody(t, usageDetail("400 Bad Request", []byte(raw), map[string]string{"Authorization": long}))
		if strings.HasSuffix(body, long[:k]) {
			t.Errorf("a %d-character prefix of the credential survived at the tail:\n%s", k, body)
		}
	}
}

// TestTelemetryUsage_ShortHeaderNameIsRedacted covers the other half: a name like "token" or
// "apikey" is under the old floor and was published.
func TestTelemetryUsage_ShortHeaderNameIsRedacted(t *testing.T) {
	for _, name := range []string{"token", "apikey", "auth", "key"} {
		body := detailBody(t, usageDetail("401 Unauthorized",
			[]byte(`{"message":"header `+name+` was rejected"}`),
			map[string]string{name: "Basic 0123456789abcdef"}))
		if strings.Contains(body, name) {
			t.Errorf("the header name %q reached detail; this surface reports credential problems "+
				"without identifying the credential:\n%s", name, body)
		}
	}
}

// TestTelemetryUsage_PromQLFailureInsideA200CarriesItsCause covers the arm where the transport
// succeeded and the query did not. The PromQL family reports failure inside HTTP 200, so nothing
// about the status code reveals it, and reporting query_failed with a blank cause is the blank this
// surface exists to remove.
func TestTelemetryUsage_PromQLFailureInsideA200CarriesItsCause(t *testing.T) {
	resetReportFlags(t)

	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/prometheus/") {
			// HTTP 200 with an error envelope — exactly what the captured error fixture shows,
			// minus the status code.
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"Execution error: unexpected left brace"}`))
			return
		}
		_, _ = w.Write([]byte(`{"took":1,"total":0,"hits":[]}`))
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if dto.Metrics.State != telemetryUsageStateQueryFailed {
		t.Errorf("metrics state = %q, want %q — a PromQL error inside a 200 is still a failed query",
			dto.Metrics.State, telemetryUsageStateQueryFailed)
	}
	if dto.Metrics.Detail == "" {
		t.Error("metrics detail is blank for a failure the backend explained; the cause must be " +
			"carried, or an operator is told only that something went wrong")
	}
	if !strings.Contains(dto.Metrics.Detail, "bad_data") {
		t.Errorf("metrics detail = %q, want it to carry the backend's own error class", dto.Metrics.Detail)
	}
	assertNoSecret(t, dto, fx)
}

func jsonEncodedString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal %q: %v", s, err)
	}
	return string(b)
}

// detailBody strips the HTTP status prefix usageDetail prepends. That prefix comes from net/http,
// never from the backend body or the credential, so a leak assertion that included it would fail on
// coincidences like "Bad Request" sharing "Ba" with "Basic ...".
func detailBody(t *testing.T, detail string) string {
	t.Helper()
	if _, body, found := strings.Cut(detail, ": "); found {
		return body
	}
	return ""
}

// withoutRedactionMarkers removes this package's own replacement tokens before a leak assertion
// reads what is left.
//
// It exists for a degenerate case that is nonetheless worth stating: a one-character credential
// cannot be distinguished from the marker that replaces it, because "[redacted]" is itself made of
// characters. The property that actually matters is that no secret material survives OUTSIDE a
// marker, and that is what this lets the assertion say.
func withoutRedactionMarkers(s string) string {
	for _, marker := range []string{"[redacted]", "[header]", "[truncated]"} {
		s = strings.ReplaceAll(s, marker, "")
	}
	return s
}

// ---------------------------------------------------------------------------
// The filter axis — a metric name that no longer matches is not an idle factory.
// ---------------------------------------------------------------------------

// AC-5 made three axes honest: installed, recording, reachable. A fourth way to read nothing has
// no equivalent. The four metric names this file queries are Claude Code's, and Claude Code
// versions independently of this repo; nothing pins them and no test anywhere references them
// against a live source. When a rename lands, every query still succeeds — HTTP 200, status
// "success", an EMPTY vector — so worstUsageState over four "ok"s reports ok, Rows is empty, and
// the panel prints "No session metrics in this window.": indistinguishable from a factory that
// simply did no work, with recording on, the backend healthy, and all five dark states clean.
//
// The distinction rides `detail`, the field the metrics half already uses to carry its failure
// classes. It cannot ride `state`: that enum is closed (design-doc.md:140), and a sixth value
// would be ranked by severity's zero value — the same rank as ok — so the DTO would summarise a
// silent blackout as healthy, while the web mirror rejected the unknown state into a relay
// failure that blanks all three panes.
func TestTelemetryUsage_AllMetricsEmptyIsDistinguishableFromIdle(t *testing.T) {
	resetReportFlags(t)

	// okBackend answers every PromQL query with status "success" and an empty vector — exactly the
	// shape a renamed metric produces, and exactly what was observed against the real binary.
	endpoint, _ := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if dto.Metrics.State != telemetryUsageStateOK {
		t.Fatalf("metrics state = %q, want %q — every query succeeded, so the query did not fail",
			dto.Metrics.State, telemetryUsageStateOK)
	}
	if len(dto.Metrics.Rows) != 0 {
		t.Fatalf("len(metrics rows) = %d, want 0 — this fixture answers every metric with an empty vector", len(dto.Metrics.Rows))
	}
	if dto.Metrics.Detail == "" {
		t.Error("metrics detail is blank when every session metric was queried and none returned a " +
			"series. state=ok with rows=[] and no detail is the exact shape an idle factory produces, " +
			"so a consumer cannot tell 'nothing happened' from 'the names this factory asks for no " +
			"longer exist'. AC-5 made installed/recording/reachable honest; this is the filter axis, " +
			"and it needs the same treatment")
	}
	assertNoSecret(t, dto, fx)
}

// The other half of the pin, and the one that keeps it from being a rubber stamp: a factory whose
// metrics DID return series must carry no emptiness signal at all. Without this, "always set
// detail" passes the assertion above while telling every healthy operator their metric names are
// broken.
func TestTelemetryUsage_PopulatedMetricsCarryNoEmptinessSignal(t *testing.T) {
	resetReportFlags(t)

	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/prometheus/") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{"__name__":"claude_code_token_usage","af_agent":"solver"},"value":[1,"42"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"took":3,"hits":[],"total":0,"from":0,"size":50}`))
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if dto.Metrics.State != telemetryUsageStateOK {
		t.Fatalf("metrics state = %q, want %q", dto.Metrics.State, telemetryUsageStateOK)
	}
	if len(dto.Metrics.Rows) == 0 {
		t.Fatal("len(metrics rows) = 0, want > 0 — this fixture answers every metric with a series")
	}
	if dto.Metrics.Detail != "" {
		t.Errorf("metrics detail = %q, want empty — every metric returned a series, so there is "+
			"nothing to disclose. A signal that fires on a healthy factory teaches operators to "+
			"ignore it, which is how the honest state stops being read at all", dto.Metrics.Detail)
	}
	assertNoSecret(t, dto, fx)
}

// A metric rename does not have to be total. Claude Code renaming ONE of the four leaves the other
// three answering, so Rows is non-empty and every guard keyed on "no rows" is blind to it — while
// the pane silently drops a whole category of measurement. The signal therefore records WHICH
// metrics answered, not merely whether anything did.
func TestTelemetryUsage_PartiallyEmptyMetricsAreRecorded(t *testing.T) {
	resetReportFlags(t)

	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/prometheus/") {
			// Only the cost series still exists; the other three names no longer match anything.
			if strings.Contains(r.URL.RawQuery, "claude_code_cost_usage") {
				_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
					`{"metric":{"__name__":"claude_code_cost_usage","af_agent":"solver"},"value":[1,"0.12"]}]}}`))
				return
			}
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"took":3,"hits":[],"total":0,"from":0,"size":50}`))
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if dto.Metrics.State != telemetryUsageStateOK {
		t.Fatalf("metrics state = %q, want %q — three empty vectors are three successful queries",
			dto.Metrics.State, telemetryUsageStateOK)
	}
	if len(dto.Metrics.Rows) == 0 {
		t.Fatal("len(metrics rows) = 0, want > 0 — one metric still answers in this fixture")
	}
	if dto.Metrics.Detail == "" {
		t.Error("metrics detail is blank when three of four session metrics returned no series. " +
			"Rows is non-empty, so every check keyed on an empty row set passes and the partial " +
			"blackout is invisible: the pane renders the one surviving series as though it were the " +
			"whole picture")
	}
	if !strings.Contains(dto.Metrics.Detail, "claude_code_token_usage") {
		t.Errorf("metrics detail = %q, want it to NAME the metrics that returned nothing — "+
			"'some metrics were empty' does not tell an operator which category of measurement "+
			"disappeared, and naming them is what makes a rename diagnosable", dto.Metrics.Detail)
	}
	assertNoSecret(t, dto, fx)
}

// The guard that keeps the new signal from erasing a better one. queryMetrics already populates
// detail with the first failing metric's error class; assigning the emptiness sentence
// unconditionally at the end of the function would overwrite a specific backend diagnosis with a
// generic one — a silent fallback that hides the more useful cause exactly when it matters.
func TestTelemetryUsage_EmptinessSignalNeverOverwritesAFailureCause(t *testing.T) {
	resetReportFlags(t)

	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/prometheus/") {
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"Execution error: unexpected left brace"}`))
			return
		}
		_, _ = w.Write([]byte(`{"took":1,"total":0,"hits":[]}`))
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if dto.Metrics.State != telemetryUsageStateQueryFailed {
		t.Fatalf("metrics state = %q, want %q", dto.Metrics.State, telemetryUsageStateQueryFailed)
	}
	if !strings.Contains(dto.Metrics.Detail, "bad_data") {
		t.Errorf("metrics detail = %q, want the backend's own error class preserved — a failed "+
			"query also produces zero rows, and letting the emptiness sentence win there replaces "+
			"a diagnosis the backend supplied with a guess about metric names", dto.Metrics.Detail)
	}
}

// ---------------------------------------------------------------------------
// fable-implement Step 3 (Root Cause B, immediate half): tokens schema pre-flight.
// Phase 5 (RED): telemetry_usage.go has NOT been modified yet — queryTokens still
// runs unconditionally, so every test below fails for the predicted reason (the
// pre-flight branch does not exist), never for an unrelated one.
// ---------------------------------------------------------------------------

// schemaBackend answers the schema pre-flight with a fixed field list on top of
// okBackend's existing tokens/metrics handling, so a test can control exactly
// which columns "exist" without touching the other two query families.
//
// The response shape below — {"schema":[{"name":...,"type":...}]} — is the REAL
// OpenObserve v0.91.3 shape, captured live against a real backend
// (internal/telemetry/testdata/recorded-real/logs_schema_response.json). An earlier
// draft of this fixture guessed a flatter {"fields":[...]} shape; corrected to match
// the capture, not the other way around, per this codebase's own "captures rather
// than hand-authored fixtures" idiom (openobserve-v0.91.3/README.md).
func schemaBackend(t *testing.T, fields []string) (string, *recorder) {
	t.Helper()
	entries := make([]string, len(fields))
	for i, f := range fields {
		entries[i] = `{"name":"` + f + `","type":"Utf8"}`
	}
	body := `{"name":"default","schema":[` + strings.Join(entries, ",") + `]}`
	return fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, telemetrySchemaPath) {
			_, _ = w.Write([]byte(body))
			return
		}
		okBackend(w, r)
	})
}

func TestTelemetryUsage_PreFlightSkipsDoomedQueryOnMissingColumns(t *testing.T) {
	resetReportFlags(t)

	// A real logs-stream schema, per the live capture: none of the token columns
	// this view needs are present.
	endpoint, rec := schemaBackend(t, []string{"af_agent", "af_formula_instance", "_timestamp", "service_name"})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if dto.Tokens.State != telemetryUsageStateQueryFailed {
		t.Errorf("tokens state = %q, want %q — required columns are missing from the schema",
			dto.Tokens.State, telemetryUsageStateQueryFailed)
	}
	if dto.Tokens.Detail == "" {
		t.Error("tokens detail is blank when the pre-flight found missing columns — the gap must be " +
			"stated in the surface's own words, not left silent")
	}
	for _, req := range rec.all() {
		if req.Path == telemetrySearchPath {
			t.Errorf("the _search POST reached the wire despite a missing-column pre-flight failure: %+v", req)
		}
	}
}

func TestTelemetryUsage_PreFlightDetailNeverLeaksBackendJargon(t *testing.T) {
	resetReportFlags(t)

	endpoint, _ := schemaBackend(t, []string{"af_agent", "_timestamp"})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	blob, err := json.Marshal(dto)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(blob), "Search field not found") {
		t.Errorf("payload leaks the backend's raw jargon string:\n%s", blob)
	}
}

func TestTelemetryUsage_PreFlightRespectsTheSharedBudget(t *testing.T) {
	resetReportFlags(t)
	// A hung schema endpoint must not let the pre-flight exceed the one shared
	// per-verb budget (telemetry_usage.go:321) — it is one more sequential request
	// under the same envelope, not a separate unbounded call.
	// Cleanup order matters: t.Cleanup runs LIFO, and httptest.Server.Close() blocks
	// until every in-flight handler returns. Registering Close before the
	// hang-closer makes Close run LAST, after the handler has already unblocked.
	hang := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, telemetrySchemaPath) {
			<-hang
			return
		}
		okBackend(w, r)
	}))
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(hang) })
	fx := newUsageFixture(t, srv.URL+"/api/default")

	start := time.Now()
	dto := usageDTO(t, fx.root, "", "")
	if elapsed := time.Since(start); elapsed > 12*time.Second {
		t.Errorf("usageDTO took %s against a hung schema endpoint, want it bounded by the shared "+
			"per-verb budget (2-10s), not left to hang indefinitely", elapsed)
	}
	if dto.Tokens.State == telemetryUsageStateOK {
		t.Error("tokens state = ok against a hung schema pre-flight, want a transport failure state")
	}
}

func TestTelemetryUsage_PreFlightSchemaCheckFailureDegradesGracefully(t *testing.T) {
	resetReportFlags(t)
	// The schema fetch itself failing (backend down before it can answer) must
	// classify through the EXISTING transport rule, not a bespoke one — the
	// spec's own comment: "Transport verdicts pass through unchanged" (decisions.md D3).
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	srv.Close()
	fx := newUsageFixture(t, srv.URL+"/api/default")

	dto := usageDTO(t, fx.root, "", "")
	if dto.Tokens.State != telemetryUsageStateBackendDown {
		t.Errorf("tokens state = %q, want %q — a transport failure of the schema fetch must reuse "+
			"the existing backend_down classification, not a bespoke pre-flight-specific one",
			dto.Tokens.State, telemetryUsageStateBackendDown)
	}
}

func TestTelemetryUsage_PreFlightReusesQueryFailedNoSixthState(t *testing.T) {
	resetReportFlags(t)
	endpoint, _ := schemaBackend(t, []string{"af_agent"})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	closed := map[string]bool{
		telemetryUsageStateOK: true, telemetryUsageStateNotInstalled: true,
		telemetryUsageStateBackendDown: true, telemetryUsageStateCredentialRejected: true,
		telemetryUsageStateQueryFailed: true,
	}
	if !closed[dto.Tokens.State] {
		t.Errorf("tokens state = %q, not a member of the closed 5-state enum — the pre-flight must "+
			"never introduce a sixth state", dto.Tokens.State)
	}
}

// ---------------------------------------------------------------------------
// fable-implement Step 4 (contributing gap #5): silent-metric existence probe.
// Phase 5 (RED): queryMetrics has NOT been modified yet — the flat
// "queried and returned no series: <names>" sentence (telemetry_usage.go:709) is
// still produced unconditionally, so every differentiated-sentence assertion below
// fails for the predicted reason.
// ---------------------------------------------------------------------------

func TestTelemetryUsage_SilentMetricExistenceProbe_HistoryExistsIdleSentence(t *testing.T) {
	resetReportFlags(t)
	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, telemetrySeriesPath):
			// History exists: the series endpoint answers with at least one entry.
			_, _ = w.Write([]byte(`{"status":"success","data":[{"__name__":"claude_code_token_usage"}]}`))
		case strings.Contains(r.URL.Path, "/prometheus/"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"took":3,"hits":[],"total":0,"from":0,"size":50}`))
		}
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if !strings.Contains(dto.Metrics.Detail, "idle") || !strings.Contains(dto.Metrics.Detail, "history exists") {
		t.Errorf("metrics detail = %q, want it to say the metric is idle with history — the series "+
			"endpoint proved it has existed before", dto.Metrics.Detail)
	}
}

func TestTelemetryUsage_SilentMetricExistenceProbe_NeverRecordedSentence(t *testing.T) {
	resetReportFlags(t)
	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, telemetrySeriesPath):
			_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
		case strings.Contains(r.URL.Path, "/prometheus/"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"took":3,"hits":[],"total":0,"from":0,"size":50}`))
		}
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	if !strings.Contains(dto.Metrics.Detail, "never") && !strings.Contains(dto.Metrics.Detail, "names have moved") {
		t.Errorf("metrics detail = %q, want it to say no series has ever existed / the names may have "+
			"moved — the series endpoint proved none has ever been recorded", dto.Metrics.Detail)
	}
}

func TestTelemetryUsage_SilentMetricExistenceProbe_ProbeFailureKeepsHedgedSentenceVerbatim(t *testing.T) {
	resetReportFlags(t)
	endpoint, _ := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, telemetrySeriesPath):
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"status":"error"}`))
		case strings.Contains(r.URL.Path, "/prometheus/"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"took":3,"hits":[],"total":0,"from":0,"size":50}`))
		}
	})
	fx := newUsageFixture(t, endpoint)
	dto := usageDTO(t, fx.root, "", "")

	want := "queried and returned no series: " + strings.Join(telemetryUsageMetrics, ", ")
	if dto.Metrics.Detail != want {
		t.Errorf("metrics detail = %q, want the today's hedged sentence verbatim (%q) when the "+
			"existence probe itself fails — this is the explicit no-regression requirement",
			dto.Metrics.Detail, want)
	}
}

func TestTelemetryUsage_SilentMetricExistenceProbe_BoundedToFourGETs(t *testing.T) {
	resetReportFlags(t)
	endpoint, rec := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.Contains(r.URL.Path, telemetrySeriesPath):
			_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
		case strings.Contains(r.URL.Path, "/prometheus/"):
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
		default:
			_, _ = w.Write([]byte(`{"took":3,"hits":[],"total":0,"from":0,"size":50}`))
		}
	})
	fx := newUsageFixture(t, endpoint)
	_ = usageDTO(t, fx.root, "", "")

	var probes int
	for _, req := range rec.all() {
		if strings.Contains(req.Path, telemetrySeriesPath) {
			probes++
		}
	}
	if probes == 0 {
		t.Fatal("zero existence-probe requests recorded — the probe never fired")
	}
	if probes > len(telemetryUsageMetrics) {
		t.Errorf("existence probe issued %d requests, want at most %d (len(telemetryUsageMetrics))",
			probes, len(telemetryUsageMetrics))
	}
}

func TestTelemetryUsage_SilentMetricExistenceProbe_NeverFiresOnHealthyPane(t *testing.T) {
	resetReportFlags(t)
	endpoint, rec := fakeBackend(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "/prometheus/") {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[` +
				`{"metric":{"__name__":"claude_code_token_usage","af_agent":"solver"},"value":[1,"42"]}]}}`))
			return
		}
		_, _ = w.Write([]byte(`{"took":3,"hits":[],"total":0,"from":0,"size":50}`))
	})
	fx := newUsageFixture(t, endpoint)
	_ = usageDTO(t, fx.root, "", "")

	for _, req := range rec.all() {
		if strings.Contains(req.Path, telemetrySeriesPath) {
			t.Errorf("existence probe fired against a healthy (non-silent) metrics pane: %+v", req)
		}
	}
}

func TestTelemetryUsage_SilentMetricExistenceProbe_InstantReadSemanticsUnchanged(t *testing.T) {
	resetReportFlags(t)
	endpoint, rec := fakeBackend(t, okBackend)
	fx := newUsageFixture(t, endpoint)
	before := time.Now().Unix()
	_ = usageDTO(t, fx.root, "", "")
	after := time.Now().Unix()

	for _, req := range rec.all() {
		if !strings.Contains(req.Path, "/prometheus/") || strings.Contains(req.Path, telemetrySeriesPath) {
			continue
		}
		q, err := url.ParseQuery(req.Query)
		if err != nil {
			t.Fatalf("parse query %q: %v", req.Query, err)
		}
		ts, err := strconv.ParseInt(q.Get("time"), 10, 64)
		if err != nil {
			t.Fatalf("primary query's time param %q did not parse: %v", q.Get("time"), err)
		}
		if ts < before || ts > after {
			t.Errorf("primary query time=%d outside [%d,%d] — instant-read semantics must be unchanged by the existence probe", ts, before, after)
		}
	}
}
