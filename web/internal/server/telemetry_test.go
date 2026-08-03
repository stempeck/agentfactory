package server

// #580 Phase 3 — the console's telemetry read surface.
//
// These tests live in their own file, not in server_test.go, deliberately: acceptance criterion #1
// counts lines in server_test.go matching `, true},$` and requires the count to stay at 2 (today,
// exactly the two write-tier route rows). A table-driven test added there with any row ending in
// that shape would false-fail a correct implementation. Keeping this file separate removes the
// coupling rather than relying on a row-shape convention being remembered.

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory-web/internal/telemetryview"
)

// fakeTelemetry is a hermetic Telemetry seam double. Each method returns the canned bytes or error;
// nothing is spawned.
type fakeTelemetry struct {
	raw json.RawMessage
	err error

	gotAgent    string
	gotInstance string
	calls       int
}

func (f *fakeTelemetry) Status(ctx context.Context) (json.RawMessage, error) {
	f.calls++
	return f.raw, f.err
}

func (f *fakeTelemetry) Report(ctx context.Context, agent, instance string) (json.RawMessage, error) {
	f.calls++
	f.gotAgent, f.gotInstance = agent, instance
	return f.raw, f.err
}

func (f *fakeTelemetry) Usage(ctx context.Context, agent, instance string) (json.RawMessage, error) {
	f.calls++
	f.gotAgent, f.gotInstance = agent, instance
	return f.raw, f.err
}

var _ Telemetry = (*fakeTelemetry)(nil)

// telemetryRoutes is the route set under test, with the query-bearing form for the two filtered
// routes. Written as a slice of structs with STRING fields only — no trailing bool — so no line here
// can ever match acceptance criterion #1's `, true},$` probe even if this file were merged into
// server_test.go later.
var telemetryRoutes = []struct {
	name string
	path string
}{
	{name: "status", path: "/api/telemetry"},
	{name: "report", path: "/api/telemetry/report"},
	{name: "usage", path: "/api/telemetry/usage"},
}

func telemetryServer(t *testing.T, seam Telemetry) *Server {
	t.Helper()
	if seam == nil {
		return New(&fakeMutator{}, fakeAssembler{}, nil, WithToken(wtoken))
	}
	return New(&fakeMutator{}, fakeAssembler{}, nil, WithToken(wtoken), WithTelemetry(seam))
}

// TestTelemetryRoutes_NotInstalledRendersDesignedResponse — acceptance criterion #3.
//
// Every OTHER Convention-A handler in this file 500s on a nil seam (handleAgentDetail at
// server.go:733-736 is the nearest precedent). Telemetry must not: an unwired reader is a state the
// operator needs to SEE, and a 500 renders it as a fault of the console rather than a fact about the
// factory.
//
// The ≠404 leg is load-bearing beyond its own assertion: it is the only mechanical proof anywhere in
// the phase that the three routes are registered UNCONDITIONALLY (Convention A). Acceptance
// criterion #2's grep reads 0 both before and after the change, so it cannot carry that proof — a
// Convention-B route would simply be absent from the mux and 404 here.
func TestTelemetryRoutes_NotInstalledRendersDesignedResponse(t *testing.T) {
	for _, rt := range telemetryRoutes {
		t.Run(rt.name, func(t *testing.T) {
			s := telemetryServer(t, nil)
			rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path, nil))

			if rec.Code == http.StatusNotFound {
				t.Fatalf("%s: 404 — the route is not registered; Convention A requires unconditional registration", rt.path)
			}
			if rec.Code == http.StatusInternalServerError {
				t.Fatalf("%s: 500 — a nil seam must render a designed response, not an error wall", rt.path)
			}
			if rec.Code != http.StatusOK {
				t.Fatalf("%s: code = %d, want 200; body=%s", rt.path, rec.Code, rec.Body.String())
			}

			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s: Cache-Control = %q, want no-store (the not-installed state must not be cached either)", rt.path, got)
			}

			var env Envelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("%s: body is not an Envelope: %v", rt.path, err)
			}
			if !env.OK {
				t.Errorf("%s: ok = false — OK describes the RELAY, and the relay succeeded in rendering a designed state", rt.path)
			}
			if env.Data == nil {
				t.Fatalf("%s: data is absent; the designed DTO is the whole point of the 200", rt.path)
			}

			// The payload must be this route's OWN DTO family with a complete key set, carrying a
			// visibly not-ok state. Re-decode Data generically so the assertion is about the wire
			// form the browser receives.
			data, err := json.Marshal(env.Data)
			if err != nil {
				t.Fatalf("%s: re-encode data: %v", rt.path, err)
			}
			var m map[string]interface{}
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatalf("%s: data is not an object: %v", rt.path, err)
			}

			if _, ok := m["v"]; !ok {
				t.Errorf("%s: payload has no schema version", rt.path)
			}
			state, _ := m["state"].(string)
			if state == "" {
				t.Fatalf("%s: payload has no state", rt.path)
			}

			switch rt.name {
			case "status":
				for _, k := range []string{"installed", "recording", "backend", "unprobed_cause"} {
					if _, ok := m[k]; !ok {
						t.Errorf("%s: status payload is missing key %q — degradation is a value difference, never a shape difference", rt.path, k)
					}
				}
				if state == "ok" {
					t.Error("status: state ok — nothing was measured, so health must not be claimed")
				}
			case "report":
				for _, k := range []string{"rows", "stats"} {
					if _, ok := m[k]; !ok {
						t.Errorf("%s: report payload is missing key %q", rt.path, k)
					}
				}
				// report's enum has no "not installed" value, and state:"ok" + rows:[] is
				// byte-identical to a real healthy-empty factory — the conflation AC-5 names.
				if state == "ok" {
					t.Error("report: state ok with no reader wired is indistinguishable from a healthy factory with no data yet")
				}
			case "usage":
				for _, k := range []string{"endpoint", "window", "filters", "tokens", "metrics", "cause"} {
					if _, ok := m[k]; !ok {
						t.Errorf("%s: usage payload is missing key %q", rt.path, k)
					}
				}
				if state != "not_installed" {
					t.Errorf("usage: state = %q, want not_installed (the enum has the exact value for this)", state)
				}
			}

			// Arrays are never null, in any state — a consumer iterates them without a guard.
			if strings.Contains(string(data), ":null") {
				t.Errorf("%s: payload carries a null where an array is promised: %s", rt.path, data)
			}
		})
	}
}

// TestTelemetryHandlers_NoSecretInAnyState — acceptance criterion #6, the web half of AC-6.
//
// The scan is over the WHOLE serialized response body, not an enumeration of known fields: the usage
// payload carries backend-controlled strings in metrics.rows[].labels and in tokens.detail, and a
// field-by-field scan would not see them.
//
// It deliberately does NOT plant a credential and assert its absence. Redaction is entirely
// root-side; a correct relay relays what it is given, so a planted needle would fail correct code
// while testing the wrong module. Non-vacuity is proved instead by the canary subtest below, which
// shows the scanner detects a marker when one is present.
func TestTelemetryHandlers_NoSecretInAnyState(t *testing.T) {
	needles := []string{"Authorization", "Basic ", "X-Api-Key", "telemetry.auth", ".agentfactory/telemetry"}

	scan := func(t *testing.T, label, body string) {
		t.Helper()
		for _, n := range needles {
			if strings.Contains(body, n) {
				t.Errorf("%s: response body carries %q\nbody=%s", label, n, body)
			}
		}
	}

	// Every state the two disjoint enums can express, plus the relay failure and the nil seam.
	states := []struct {
		name string
		raw  string
		err  error
	}{
		{name: "status ok", raw: `{"v":1,"state":"ok","installed":{"present":true,"valid":true,"endpoint":"http://127.0.0.1:5080/api/default","header_count":1},"recording":{"enabled":true},"backend":{"probed":true,"signals":[{"label":"step timings","ok":true,"status":200,"summary":"step timings: reachable (HTTP 200)"}]},"unprobed_cause":""}`},
		{name: "status degraded", raw: `{"v":1,"state":"degraded","installed":{"present":true,"valid":true,"endpoint":"http://127.0.0.1:5080/api/default","header_count":1},"recording":{"enabled":true},"backend":{"probed":true,"signals":[{"label":"token usage","ok":false,"status":404,"summary":"token usage: reachable but the address is not served (HTTP 404)"}]},"unprobed_cause":""}`},
		{name: "status error envelope", raw: `{"v":1,"state":"error","error":"not in an agentfactory workspace (no .agentfactory/factory.json found)"}`},
		{name: "status credential unreadable", raw: `{"v":1,"state":"degraded","installed":{"present":true,"valid":true,"endpoint":"http://127.0.0.1:5080/api/default","header_count":1},"recording":{"enabled":true},"backend":{"probed":false,"signals":[]},"unprobed_cause":"a configured credential could not be read"}`},
		{name: "report ok", raw: `{"v":1,"state":"ok","rows":[],"stats":{"malformed":0,"dropped":0,"dropped_unexported":0}}`},
		{name: "report degraded", raw: `{"v":1,"state":"degraded","rows":[],"stats":{"malformed":2,"dropped":7,"dropped_unexported":3}}`},
		{name: "usage ok with backend-controlled labels", raw: `{"v":1,"state":"ok","endpoint":"http://127.0.0.1:5080/api/default","window":{"start_us":1,"end_us":2,"limit":1000},"filters":{"agent":"","instance":""},"tokens":{"state":"ok","rows":[],"total":0,"truncated":false,"detail":""},"metrics":{"state":"ok","rows":[{"metric":"claude_code_token_usage","agent":"solver","instance":"af-1234abcd","labels":{"model":"claude-opus-5","query_source":"main","terminal_type":"tmux","type":"input"},"value":"48213"}],"at_us":2,"detail":""},"cause":""}`},
		{name: "usage query_failed with backend error text", raw: `{"v":1,"state":"query_failed","endpoint":"http://127.0.0.1:5080/api/default","window":{"start_us":1,"end_us":2,"limit":1000},"filters":{"agent":"","instance":""},"tokens":{"state":"query_failed","rows":[],"total":0,"truncated":false,"detail":"400 Bad Request: Search field not found: no field named af_overhead (trace_id 019fa1f1ef777500)"},"metrics":{"state":"ok","rows":[],"at_us":2,"detail":""},"cause":""}`},
		{name: "usage credential_rejected", raw: `{"v":1,"state":"credential_rejected","endpoint":"http://127.0.0.1:5080/api/default","window":{"start_us":1,"end_us":2,"limit":1000},"filters":{"agent":"","instance":""},"tokens":{"state":"credential_rejected","rows":[],"total":0,"truncated":false,"detail":"401 Unauthorized"},"metrics":{"state":"credential_rejected","rows":[],"at_us":0,"detail":"401 Unauthorized"},"cause":"the configured credential was rejected"}`},
		{name: "usage backend_down", raw: `{"v":1,"state":"backend_down","endpoint":"http://127.0.0.1:5080/api/default","window":{"start_us":1,"end_us":2,"limit":1000},"filters":{"agent":"","instance":""},"tokens":{"state":"backend_down","rows":[],"total":0,"truncated":false,"detail":"the backend could not be reached"},"metrics":{"state":"backend_down","rows":[],"at_us":0,"detail":"the backend could not be reached"},"cause":""}`},
	}

	for _, st := range states {
		for _, rt := range telemetryRoutes {
			t.Run(st.name+" via "+rt.name, func(t *testing.T) {
				s := telemetryServer(t, &fakeTelemetry{raw: json.RawMessage(st.raw), err: st.err})
				rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path, nil))
				scan(t, st.name+" via "+rt.name, rec.Body.String())
			})
		}
	}

	t.Run("relay failure carries no child stderr", func(t *testing.T) {
		// ExecRunner.run embeds the child's stderr in its error text (runner.go:187), and root-side
		// credential failures name the header there. A handler that relays err.Error() — which the
		// handleDispatch precedent at server.go:507 does — would publish it to a browser.
		hostile := errors.New(`af telemetry: exit 1: failed to set header Authorization: Basic c2VjcmV0`)
		for _, rt := range telemetryRoutes {
			s := telemetryServer(t, &fakeTelemetry{err: hostile})
			rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path, nil))
			scan(t, "relay failure via "+rt.name, rec.Body.String())
			if strings.Contains(rec.Body.String(), "c2VjcmV0") {
				t.Errorf("%s: the child's stderr reached the response body", rt.path)
			}
		}
	})

	t.Run("nil seam carries no secret", func(t *testing.T) {
		for _, rt := range telemetryRoutes {
			s := telemetryServer(t, nil)
			rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path, nil))
			scan(t, "nil seam via "+rt.name, rec.Body.String())
		}
	})

	// Non-vacuity. The scanner must be able to FIND a marker; otherwise every assertion above is a
	// tautology over an empty search. A benign canary is used rather than a real credential,
	// because a real one planted in the payload would be relayed by a correct implementation.
	t.Run("canary: the scanner is not vacuous", func(t *testing.T) {
		control := `{"note":"Authorization"}`
		var found bool
		for _, n := range needles {
			if strings.Contains(control, n) {
				found = true
			}
		}
		if !found {
			t.Fatal("the scanner found nothing in a body that plainly contains a needle — every assertion in this test is vacuous")
		}
	})
}

// TestTelemetryStatus_ErrorEnvelopeIsDataNot502 pins the second-most-likely defect in this phase.
//
// web/internal/dispatch — the structural template this package's reader was built from — converts
// state=="error" into a Go error (dispatch.go:96-98), which a handler then renders as a 502. For
// telemetry that would turn a committed, fixture-backed, DESIGNED payload into an error wall: the
// state:"error" envelope is how the CLI reports "not in an agentfactory workspace", which is a fact
// about the factory that the operator needs to read, not a failure of the relay.
//
// No acceptance criterion covers this.
func TestTelemetryStatus_ErrorEnvelopeIsDataNot502(t *testing.T) {
	raw := `{"v":1,"state":"error","error":"not in an agentfactory workspace (no .agentfactory/factory.json found)"}`
	s := telemetryServer(t, &fakeTelemetry{raw: json.RawMessage(raw)})

	rec := serve(s, httptest.NewRequest(http.MethodGet, "/api/telemetry", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("code = %d, want 200 — a state:\"error\" payload is DATA, not a transport failure; body=%s", rec.Code, rec.Body.String())
	}

	var env Envelope
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not an Envelope: %v", err)
	}
	if !env.OK {
		t.Error("ok = false; OK describes the relay, and the relay succeeded")
	}
	data, _ := json.Marshal(env.Data)
	if !strings.Contains(string(data), `"state":"error"`) {
		t.Errorf("the payload did not arrive intact: %s", data)
	}
}

// TestTelemetryRelayFailureIsTransportError is the counterpart: when the relay itself fails, there
// is no measurement to report, and inventing one would be the silent fallback this whole feature
// exists to remove. It must be an Envelope transport error carrying a FIXED message.
func TestTelemetryRelayFailureIsTransportError(t *testing.T) {
	for _, rt := range telemetryRoutes {
		t.Run(rt.name, func(t *testing.T) {
			s := telemetryServer(t, &fakeTelemetry{err: errors.New("af telemetry: exit -1: ")})
			rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path, nil))

			if rec.Code == http.StatusOK {
				t.Fatalf("%s: code = 200 — a failed relay must not be rendered as a telemetry state", rt.path)
			}
			if rec.Code != http.StatusBadGateway {
				t.Errorf("%s: code = %d, want 502", rt.path, rec.Code)
			}
			var env Envelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("%s: body is not an Envelope: %v", rt.path, err)
			}
			if env.OK {
				t.Errorf("%s: ok = true on a transport failure", rt.path)
			}
			if env.Message == "" {
				t.Errorf("%s: no message; the operator needs to know the relay failed", rt.path)
			}
			if env.Data != nil {
				t.Errorf("%s: data is present on a transport failure — there is nothing to render", rt.path)
			}
			if got := rec.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("%s: Cache-Control = %q, want no-store on the failure path too", rt.path, got)
			}
		})
	}
}

// TestTelemetryFilterRejectionIsClientError pins the one place in this surface where an error wall
// is the honest answer: a malformed filter means nothing was measured, so there is no state to
// report. It also pins that the rejected value is never echoed — this message becomes a field of a
// JSON payload delivered to a browser.
func TestTelemetryFilterRejectionIsClientError(t *testing.T) {
	hostile := []struct {
		name  string
		query string
		echo  string
	}{
		{name: "agent with a space", query: "?agent=has+space", echo: "has space"},
		{name: "agent with a semicolon", query: "?agent=a%3Brm", echo: "a;rm"},
		{name: "agent script tag", query: "?agent=%3Cscript%3E", echo: "<script>"},
		{name: "instance leading digit", query: "?instance=1bad", echo: "1bad"},
		{name: "instance with a quote", query: "?instance=a%27b", echo: "a'b"},
		{name: "instance with a slash", query: "?instance=a%2Fb", echo: "a/b"},
	}

	for _, rt := range telemetryRoutes[1:] { // status takes no filters
		for _, h := range hostile {
			t.Run(rt.name+" "+h.name, func(t *testing.T) {
				seam := &fakeTelemetry{raw: json.RawMessage(`{"v":1,"state":"ok"}`)}
				s := telemetryServer(t, seam)
				rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path+h.query, nil))

				if rec.Code != http.StatusBadRequest {
					t.Fatalf("%s%s: code = %d, want 400; body=%s", rt.path, h.query, rec.Code, rec.Body.String())
				}
				if seam.calls != 0 {
					t.Errorf("%s%s: the seam was reached despite an invalid filter — validation must precede exec", rt.path, h.query)
				}
				body := rec.Body.String()
				if strings.Contains(body, h.echo) {
					t.Errorf("%s%s: the response echoes the rejected value %q: %s", rt.path, h.query, h.echo, body)
				}
			})
		}
	}
}

// TestTelemetryFilters_EmptyMeansUnfiltered pins that a cleared filter is an ordinary request.
// url.Values.Get cannot distinguish an absent parameter from a present-but-empty one, and both
// mirrored validators reject the empty string — so "validate unconditionally, then append" would
// reject the most common request the panel makes.
func TestTelemetryFilters_EmptyMeansUnfiltered(t *testing.T) {
	for _, q := range []string{"", "?agent=", "?instance=", "?agent=&instance="} {
		for _, rt := range telemetryRoutes[1:] {
			t.Run(rt.name+" "+q, func(t *testing.T) {
				seam := &fakeTelemetry{raw: json.RawMessage(`{"v":1,"state":"ok"}`)}
				s := telemetryServer(t, seam)
				rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path+q, nil))

				if rec.Code != http.StatusOK {
					t.Fatalf("%s%s: code = %d, want 200; body=%s", rt.path, q, rec.Code, rec.Body.String())
				}
				if seam.calls != 1 {
					t.Fatalf("%s%s: seam calls = %d, want 1", rt.path, q, seam.calls)
				}
				if seam.gotAgent != "" || seam.gotInstance != "" {
					t.Errorf("%s%s: filters = (%q,%q), want both empty", rt.path, q, seam.gotAgent, seam.gotInstance)
				}
			})
		}
	}
}

// TestTelemetryFilters_ValidReachTheSeam is the positive half: a well-formed filter must actually
// arrive, or the panel's filtering silently does nothing.
func TestTelemetryFilters_ValidReachTheSeam(t *testing.T) {
	for _, rt := range telemetryRoutes[1:] {
		t.Run(rt.name, func(t *testing.T) {
			seam := &fakeTelemetry{raw: json.RawMessage(`{"v":1,"state":"ok"}`)}
			s := telemetryServer(t, seam)
			rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path+"?agent=solver&instance=af-1234abcd", nil))

			if rec.Code != http.StatusOK {
				t.Fatalf("code = %d, want 200; body=%s", rec.Code, rec.Body.String())
			}
			if seam.gotAgent != "solver" || seam.gotInstance != "af-1234abcd" {
				t.Errorf("filters = (%q,%q), want (solver, af-1234abcd)", seam.gotAgent, seam.gotInstance)
			}
		})
	}
}

// TestTelemetryRoutes_AreReadTier pins the tier declaration at the handler level, complementing the
// route-table enumeration in server_test.go: on a non-loopback bind the token is mandatory for
// every route, and these three must behave like reads (no Origin requirement, no write gate).
func TestTelemetryRoutes_AreReadTier(t *testing.T) {
	for _, rt := range telemetryRoutes {
		t.Run(rt.name, func(t *testing.T) {
			seam := &fakeTelemetry{raw: json.RawMessage(`{"v":1,"state":"ok"}`)}
			s := telemetryServer(t, seam)

			// Loopback, no token: a read must not 401.
			rec := serve(s, httptest.NewRequest(http.MethodGet, rt.path, nil))
			if rec.Code == http.StatusUnauthorized {
				t.Errorf("%s: 401 on loopback without a token — this is a read route", rt.path)
			}
			// A read must never be refused for want of an Origin header.
			if rec.Code == http.StatusForbidden {
				t.Errorf("%s: 403 — the Origin gate belongs to state-changing requests only", rt.path)
			}
		})
	}
}

// TestTelemetryNotInstalledPayloadsComeFromTheViewPackage keeps the server's nil-seam rendering and
// the view package's designed DTOs from drifting apart: the handler must serve exactly the payload
// telemetryview declares, not a second hand-rolled copy that could diverge silently.
func TestTelemetryNotInstalledPayloadsComeFromTheViewPackage(t *testing.T) {
	cases := []struct {
		path string
		want interface{}
	}{
		{path: "/api/telemetry", want: telemetryview.NotInstalledStatus()},
		{path: "/api/telemetry/report", want: telemetryview.NotInstalledReport()},
		{path: "/api/telemetry/usage", want: telemetryview.NotInstalledUsage()},
	}
	for _, c := range cases {
		t.Run(c.path, func(t *testing.T) {
			s := telemetryServer(t, nil)
			rec := serve(s, httptest.NewRequest(http.MethodGet, c.path, nil))

			var env Envelope
			if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
				t.Fatalf("body is not an Envelope: %v", err)
			}
			got, err := json.Marshal(env.Data)
			if err != nil {
				t.Fatalf("re-encode: %v", err)
			}
			want, err := json.Marshal(c.want)
			if err != nil {
				t.Fatalf("marshal want: %v", err)
			}
			var a, b interface{}
			if err := json.Unmarshal(got, &a); err != nil {
				t.Fatalf("decode got: %v", err)
			}
			if err := json.Unmarshal(want, &b); err != nil {
				t.Fatalf("decode want: %v", err)
			}
			if !jsonEqual(a, b) {
				t.Errorf("nil-seam payload diverges from telemetryview's designed DTO\n got: %s\nwant: %s", got, want)
			}
		})
	}
}

func jsonEqual(a, b interface{}) bool {
	x, err1 := json.Marshal(a)
	y, err2 := json.Marshal(b)
	return err1 == nil && err2 == nil && string(x) == string(y)
}

// TestTelemetryBudgets_CoverTheBackingCLIWorstCase closes the one gap the behavioural tests cannot:
// every other assertion in this file passes with the budgets set to a millisecond.
//
// The design rule is that each handler's deadline MUST be at least its backing CLI's worst case,
// because a budget below it converts slow DEGRADATION into a transport error — and degradation is
// the entire product here. That rule constrains the NUMBERS, and a number is not observable from a
// hermetic handler test: the fake seam returns instantly, so a 1ms budget is indistinguishable from
// a 35s one.
//
// The arithmetic is re-derived here from the root's own constants rather than restated, so this
// fails if either side moves:
//
//	status: the CLI probes its signals SEQUENTIALLY, each bounded by a 10s ceiling
//	        (internal/telemetry/probe.go probeTimeout), one probe per address in
//	        {traces, logs, metrics} => 3 x 10s = 30s.
//	usage:  the CLI holds ONE budget for the whole verb, ceilinged at 10s
//	        (internal/cmd/telemetry_usage.go usageTimeout), and every request it makes derives from
//	        that context, so the total cannot exceed it => 10s.
//	report: local file scan, no CLI-side ceiling; the design names 5s.
func TestTelemetryBudgets_CoverTheBackingCLIWorstCase(t *testing.T) {
	const (
		probeCeiling = 10 * time.Second // probe.go probeTimeout ceiling
		signalCount  = 3                // traces + logs + metrics, probed in sequence
		usageCeiling = 10 * time.Second // telemetry_usage.go usageTimeout ceiling, whole-verb
	)

	cases := []struct {
		name   string
		budget time.Duration
		floor  time.Duration
		why    string
	}{
		{
			name:   "status",
			budget: telemetryStatusBudget,
			floor:  probeCeiling * signalCount,
			why:    "probe ceiling x signal count, probed sequentially",
		},
		{
			name:   "usage",
			budget: telemetryUsageBudget,
			floor:  usageCeiling,
			why:    "one whole-verb query ceiling",
		},
		{
			name:   "report",
			budget: telemetryReportBudget,
			floor:  5 * time.Second,
			why:    "the budget the design names for a linear local scan",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if c.budget < c.floor {
				t.Errorf("%s budget is %v, below its backing CLI's worst case of %v (%s); "+
					"a budget under the floor turns slow degradation into a transport error on exactly "+
					"the paths where the degraded verdict is the thing worth having",
					c.name, c.budget, c.floor, c.why)
			}
		})
	}

	// The three budgets must also stay DISTINCT per-handler values. A refactor that collapses them
	// to one shared constant would satisfy every floor above while discarding the reason they differ.
	if telemetryStatusBudget == telemetryReportBudget || telemetryStatusBudget == telemetryUsageBudget {
		t.Error("the status budget is no longer distinct; each handler's deadline is derived from its own backing command")
	}
	if telemetryReportBudget == telemetryUsageBudget {
		t.Error("the report and usage budgets are no longer distinct")
	}
}
