package telemetry

import (
	"net/http"
	"strings"
	"testing"
)

// TestProbeChecksBothPlanes is the assertion that makes the reachability layer worth having.
//
// The install-time check that already existed probed only the address the af binary posts to —
// the one that carried the organisation segment — and it passed while the agent sessions' own
// usage events, which is where the exact per-step token counts live, returned 404 on every
// request. A probe with the same blind spot would report a healthy factory that cannot answer
// the question it was built to answer.
func TestProbeChecksBothPlanes(t *testing.T) {
	var seen []string
	stubTransport(t, func(req *http.Request) (*http.Response, error) {
		seen = append(seen, req.URL.String())
		return fakeResponse(200, `{"partialSuccess":null}`), nil
	})

	cfg := testConfig()
	results := Probe(cfg)

	if len(results) < 2 {
		t.Fatalf("Probe checked %d addresses, want the af plane and the native plane at minimum", len(results))
	}
	for _, r := range results {
		if !r.OK() {
			t.Errorf("%s reported not-OK against a backend answering 200: %s", r.Label, r.Summary())
		}
	}

	joined := strings.Join(seen, " ")
	if !strings.Contains(joined, "/v1/traces") {
		t.Errorf("Probe never checked the step-timing address; probed: %v", seen)
	}
	if !strings.Contains(joined, "/v1/logs") {
		t.Errorf("Probe never checked the token-usage address, which is the one that was 404ing "+
			"and the one carrying the per-step token counts; probed: %v", seen)
	}
}

// TestProbeReportsAnUnservedAddress pins the verdict for the exact failure B2 describes: the
// backend is up and authenticating, and the address still is not served. "Unreachable" would be
// the wrong word for that, and an operator told only "HTTP 404" has to know the protocol to act.
func TestProbeReportsAnUnservedAddress(t *testing.T) {
	stubTransport(t, func(req *http.Request) (*http.Response, error) {
		if strings.Contains(req.URL.Path, "/v1/logs") {
			return fakeResponse(404, `not found`), nil
		}
		return fakeResponse(200, `{}`), nil
	})

	results := Probe(testConfig())
	var logs *ProbeResult
	for i := range results {
		if strings.Contains(results[i].URL, "/v1/logs") {
			logs = &results[i]
			break
		}
	}
	if logs == nil {
		t.Fatal("Probe did not check the token-usage address")
	}
	if logs.OK() {
		t.Error("a 404 was reported as OK: data posted to an unserved address is discarded")
	}
	if !strings.Contains(logs.Summary(), "not served") {
		t.Errorf("summary %q does not say the address is not served, which is the actionable "+
			"part — the backend is up, the credential works, and the data still goes nowhere",
			logs.Summary())
	}
}

// TestProbeNeverEchoesACredential holds the line the whole status surface holds: header values do
// not reach a terminal or a log, including through a diagnostic that has every reason to mention
// them.
func TestProbeNeverEchoesACredential(t *testing.T) {
	stubTransport(t, func(req *http.Request) (*http.Response, error) {
		// A backend that quotes the token it rejected is common, which is exactly the case that
		// would leak one through an error path.
		return fakeResponse(401, "rejected credential Basic c2VjcmV0LXRva2Vu"), nil
	})

	cfg := testConfig()
	for _, r := range Probe(cfg) {
		for _, v := range cfg.Headers {
			if strings.Contains(r.Summary(), v) {
				t.Errorf("probe summary echoed a header value: %q", r.Summary())
			}
			if r.Err != nil && strings.Contains(r.Err.Error(), v) {
				t.Errorf("probe error echoed a header value: %v", r.Err)
			}
		}
		if !strings.Contains(r.Summary(), "credential was rejected") {
			t.Errorf("a 401 should be reported as a rejected credential, got %q", r.Summary())
		}
	}
}

// TestProbeRefusesAnUnresolvedSecretReference keeps the two halves of the credential story
// consistent. An unresolved reference is the caller's bug; sending it would produce a 401 that
// reads like a backend problem and send an operator to the wrong place.
func TestProbeRefusesAnUnresolvedSecretReference(t *testing.T) {
	called := false
	stubTransport(t, func(req *http.Request) (*http.Response, error) {
		called = true
		return fakeResponse(200, `{}`), nil
	})

	cfg := testConfig()
	cfg.Headers = map[string]string{"Authorization": "file:.agentfactory/secrets/telemetry.auth"}

	for _, r := range Probe(cfg) {
		if r.Err == nil {
			t.Errorf("%s probed with an unresolved secret reference", r.Label)
		}
	}
	if called {
		t.Error("a request was sent carrying an unresolved secret reference as a credential")
	}
}

// TestProbeIsSilentWithoutAnEndpoint keeps the cheap paths cheap: no endpoint means the factory is
// on the local-report-only path, which is a supported end state and not something to probe.
func TestProbeIsSilentWithoutAnEndpoint(t *testing.T) {
	stubTransport(t, func(req *http.Request) (*http.Response, error) {
		t.Error("Probe made a request with no endpoint configured")
		return fakeResponse(200, `{}`), nil
	})
	cfg := testConfig()
	cfg.Endpoint = ""
	if got := Probe(cfg); got != nil {
		t.Errorf("Probe returned %d results with no endpoint configured, want none", len(got))
	}
}
