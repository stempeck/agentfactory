package telemetry

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// The exporter is reached only through package-level seams, so this suite opens no socket.
// Critically it substitutes the INNER transport seam rather than Export itself: replacing
// Export would put the status-code decision inside the fake, and the mapping from "the backend
// accepted it" to "the cursor may move" would then be tested nowhere. With the transport
// substituted, a real response object flows through the real status check into the real cursor
// logic.

func fakeResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}

func stubTransport(t *testing.T, fn func(*http.Request) (*http.Response, error)) {
	t.Helper()
	orig := httpDo
	httpDo = fn
	t.Cleanup(func() { httpDo = orig })
}

func testConfig() config.TelemetryConfig {
	// Built as a literal, so it does NOT pass through the loader's normalisation — the timeout
	// has to be set explicitly or it would mean "no timeout" rather than the documented default.
	return config.TelemetryConfig{
		Endpoint:           "http://127.0.0.1:5080",
		OTLPHTTPPathTraces: "/api/default/v1/traces",
		Protocol:           "http/json",
		ExportTimeoutMS:    500,
		Headers:            map[string]string{"Authorization": "Basic c2VjcmV0LXRva2Vu"},
	}
}

func cursorBytes(t *testing.T, dir, agent string) ([]byte, bool) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, ".cursor-"+agent))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false
	}
	if err != nil {
		t.Fatalf("reading cursor: %v", err)
	}
	return raw, true
}

func TestCursorAdvancesOnlyOn2xx(t *testing.T) {
	// This test fails against: an implementation that advances the cursor unconditionally
	// (the non-2xx rows compare the cursor file byte for byte); one that never advances it
	// (the 2xx rows are the positive control); one that advances past records it did not send
	// (the partial-drain row); and one that creates a cursor on failure, which would mark
	// never-sent records as delivered.

	rows := []struct {
		name        string
		status      int
		transportEr error
		wantAdvance bool
	}{
		{"200 OK", 200, nil, true},
		{"201 Created", 201, nil, true},
		{"202 Accepted", 202, nil, true},
		{"204 No Content", 204, nil, true},
		{"299 upper 2xx bound", 299, nil, true},
		{"100 Continue", 100, nil, false},
		{"302 Found", 302, nil, false},
		{"400 Bad Request", 400, nil, false},
		{"401 Unauthorized", 401, nil, false},
		{"403 Forbidden", 403, nil, false},
		{"404 Not Found", 404, nil, false},
		{"408 Request Timeout", 408, nil, false},
		{"413 Payload Too Large", 413, nil, false},
		{"429 Too Many Requests", 429, nil, false},
		{"500 Internal Server Error", 500, nil, false},
		{"502 Bad Gateway", 502, nil, false},
		{"503 Service Unavailable", 503, nil, false},
		{"504 Gateway Timeout", 504, nil, false},
		{"transport timeout", 0, os.ErrDeadlineExceeded, false},
	}

	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			dir := mkStore(t)
			for i := 1; i <= 3; i++ {
				if err := AppendEvent(dir, ev("a", "i1", "s"+string(rune('0'+i)), i)); err != nil {
					t.Fatalf("AppendEvent: %v", err)
				}
			}
			before, existedBefore := cursorBytes(t, dir, "a")

			stubTransport(t, func(*http.Request) (*http.Response, error) {
				if row.transportEr != nil {
					return nil, row.transportEr
				}
				return fakeResponse(row.status, `{}`), nil
			})

			res, err := Drain(dir, testConfig(), "a", 100)
			after, existsAfter := cursorBytes(t, dir, "a")

			if row.wantAdvance {
				if err != nil {
					t.Fatalf("Drain returned %v for a %d response", err, row.status)
				}
				if !res.Exported {
					t.Error("Drain reported nothing exported on a 2xx")
				}
				if res.Sent != 3 {
					t.Errorf("sent = %d, want 3", res.Sent)
				}
				if !existsAfter {
					t.Fatal("cursor was not written after a successful export")
				}
				pending, _, perr := ReadEvents(dir, Filter{Agent: "a", Unexported: true})
				if perr != nil {
					t.Fatalf("ReadEvents: %v", perr)
				}
				if len(pending) != 0 {
					t.Errorf("%d records still pending after a successful export of all of them", len(pending))
				}
				return
			}

			if err == nil {
				t.Fatalf("Drain returned nil error for %s; a non-2xx is not a success", row.name)
			}
			if res.Exported {
				t.Error("Drain reported an export on a failure")
			}
			// Byte comparison, not a parsed value: an implementation that rewrites the cursor
			// with the same logical contents is doing something it should not.
			if existedBefore != existsAfter {
				t.Errorf("cursor existence changed on failure (before=%v after=%v); a failed export "+
					"must not create a cursor, which would mark never-sent records as delivered",
					existedBefore, existsAfter)
			}
			if !bytes.Equal(before, after) {
				t.Errorf("cursor bytes changed on a %s", row.name)
			}
			pending, _, perr := ReadEvents(dir, Filter{Agent: "a", Unexported: true})
			if perr != nil {
				t.Fatalf("ReadEvents: %v", perr)
			}
			if len(pending) != 3 {
				t.Errorf("%d records pending after a failed export, want all 3 still offered", len(pending))
			}
		})
	}

	t.Run("a failed export offers a byte-identical payload on retry", func(t *testing.T) {
		dir := mkStore(t)
		for i := 1; i <= 3; i++ {
			if err := AppendEvent(dir, ev("a", "i1", "s"+string(rune('0'+i)), i)); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		var seen [][]byte
		stubTransport(t, func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seen = append(seen, body)
			return fakeResponse(503, "unavailable"), nil
		})
		for i := 0; i < 2; i++ {
			if _, err := Drain(dir, testConfig(), "a", 100); err == nil {
				t.Fatal("expected an error from a 503")
			}
		}
		if len(seen) != 2 {
			t.Fatalf("transport called %d times, want 2", len(seen))
		}
		// Deterministic ids are what make this redelivery idempotent at the backend; a payload
		// that varied between attempts would create duplicate spans on every retry.
		if !bytes.Equal(seen[0], seen[1]) {
			t.Error("retry payload differs from the first attempt")
		}
		// The reported payload must be the bytes that actually went out, not a plausible
		// re-render of them — a caller logging it for diagnosis would otherwise be reading
		// something the backend never saw.
		res, _ := Drain(dir, testConfig(), "a", 100)
		if !bytes.Equal(res.Payload, seen[0]) {
			t.Errorf("DrainResult.Payload is not the payload the transport received:\n got %s\nwant %s",
				res.Payload, seen[0])
		}
	})

	t.Run("a partial drain advances by exactly what it sent", func(t *testing.T) {
		dir := mkStore(t)
		for i := 1; i <= 8; i++ {
			if err := AppendEvent(dir, ev("a", "i1", "s"+string(rune('0'+i)), i)); err != nil {
				t.Fatalf("AppendEvent: %v", err)
			}
		}
		stubTransport(t, func(*http.Request) (*http.Response, error) {
			return fakeResponse(200, `{}`), nil
		})
		res, err := Drain(dir, testConfig(), "a", 3)
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if res.Sent != 3 {
			t.Fatalf("sent = %d, want the bounded 3", res.Sent)
		}
		pending, _, err := ReadEvents(dir, Filter{Agent: "a", Unexported: true})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(pending) != 5 {
			t.Fatalf("%d records pending after sending 3 of 8, want 5", len(pending))
		}
		if pending[0].StepID != "s4" {
			t.Errorf("first pending record is %q, want s4 — the cursor over- or under-advanced", pending[0].StepID)
		}
	})

	t.Run("a 2xx carrying a partial-success body still advances", func(t *testing.T) {
		// The spec has the server answer 200 with a partialSuccess body and the client MUST
		// NOT retry. Pinned explicitly because this is the one case where "2xx" and
		// "everything was accepted" diverge, and leaving it unpinned invites someone to
		// "fix" it into a resend loop or, worse, a non-advance that never converges.
		dir := mkStore(t)
		if err := AppendEvent(dir, ev("a", "i1", "s1", 1)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		stubTransport(t, func(*http.Request) (*http.Response, error) {
			return fakeResponse(200, `{"partialSuccess":{"rejectedSpans":"1","errorMessage":"nope"}}`), nil
		})
		if _, err := Drain(dir, testConfig(), "a", 100); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		pending, _, err := ReadEvents(dir, Filter{Agent: "a", Unexported: true})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("%d records still pending after a 200; the cursor rule is keyed on the status code", len(pending))
		}
	})

	t.Run("draining an empty backlog is a no-op that touches nothing", func(t *testing.T) {
		dir := mkStore(t)
		called := false
		stubTransport(t, func(*http.Request) (*http.Response, error) {
			called = true
			return fakeResponse(200, `{}`), nil
		})
		res, err := Drain(dir, testConfig(), "a", 100)
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if res.Sent != 0 || res.Exported {
			t.Errorf("result = %+v, want nothing sent", res)
		}
		if called {
			t.Error("an empty backlog still hit the network")
		}
		if _, exists := cursorBytes(t, dir, "a"); exists {
			t.Error("an empty drain created a cursor file")
		}
	})

	t.Run("one unrenderable record cannot block the records behind it", func(t *testing.T) {
		// A record can be perfectly valid JSON and still be impossible to render as a span —
		// a corrupt timestamp is enough. The drain is ordered and the cursor only moves on
		// success, so without a write-off such a record is a permanent blockage: every later
		// record queues behind it until the size ceiling starts discarding good data. That is
		// the same silent loss the rotation rules exist to prevent, arriving by a different
		// road.
		dir := mkStore(t)
		poison := ev("a", "i1", "poison", 1)
		poison.TS = "not-a-timestamp"
		if err := AppendEvent(dir, poison); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		stubTransport(t, func(*http.Request) (*http.Response, error) {
			return fakeResponse(200, `{}`), nil
		})

		// First drain: nothing renderable, so nothing is sent — but the blockage is cleared.
		if _, err := Drain(dir, testConfig(), "a", 100); err != nil {
			t.Fatalf("Drain over an unrenderable record: %v", err)
		}
		st, err := Stats(dir, "a")
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if st.DroppedUnexported == 0 {
			t.Error("a record that can never be exported was stepped over without being counted")
		}

		// A good record written afterwards must now reach the backend.
		if err := AppendEvent(dir, ev("a", "i1", "good", 2)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		res, err := Drain(dir, testConfig(), "a", 100)
		if err != nil {
			t.Fatalf("Drain: %v", err)
		}
		if res.Sent == 0 || !res.Exported {
			t.Fatalf("the record behind the unrenderable one never shipped: %+v", res)
		}
		pending, _, err := ReadEvents(dir, Filter{Agent: "a", Unexported: true})
		if err != nil {
			t.Fatalf("ReadEvents: %v", err)
		}
		if len(pending) != 0 {
			t.Errorf("%d records still pending after a successful drain", len(pending))
		}
	})

	t.Run("an unrenderable record travelling with good ones is counted, not hidden", func(t *testing.T) {
		dir := mkStore(t)
		poison := ev("a", "i1", "poison", 1)
		poison.TS = ""
		if err := AppendEvent(dir, poison); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		if err := AppendEvent(dir, ev("a", "i1", "good", 2)); err != nil {
			t.Fatalf("AppendEvent: %v", err)
		}
		stubTransport(t, func(*http.Request) (*http.Response, error) {
			return fakeResponse(200, `{}`), nil
		})
		if _, err := Drain(dir, testConfig(), "a", 100); err != nil {
			t.Fatalf("Drain: %v", err)
		}
		st, err := Stats(dir, "a")
		if err != nil {
			t.Fatalf("Stats: %v", err)
		}
		if st.DroppedUnexported != 1 {
			t.Errorf("droppedUnexported = %d, want 1; the batch shipped and the record that was "+
				"not in it must still be accounted for", st.DroppedUnexported)
		}
	})

	t.Run("the request targets the configured path and declares the required content type", func(t *testing.T) {
		var got *http.Request
		var body []byte
		stubTransport(t, func(req *http.Request) (*http.Response, error) {
			got = req
			body, _ = io.ReadAll(req.Body)
			return fakeResponse(200, `{}`), nil
		})
		cfg := testConfig()
		cfg.Endpoint = "http://127.0.0.1:5080/"
		if err := Export(cfg, []byte(`{"resourceSpans":[]}`)); err != nil {
			t.Fatalf("Export: %v", err)
		}
		if got == nil {
			t.Fatal("the transport seam was never reached")
		}
		if got.Method != http.MethodPost {
			t.Errorf("method = %s, want POST", got.Method)
		}
		if want := "http://127.0.0.1:5080/api/default/v1/traces"; got.URL.String() != want {
			t.Errorf("url = %s, want %s (a trailing slash on the endpoint must not double up)", got.URL, want)
		}
		if ct := got.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json — the spec states it as a MUST", ct)
		}
		if got.Header.Get("Authorization") == "" {
			t.Error("configured headers were not applied")
		}
		if !bytes.Equal(body, []byte(`{"resourceSpans":[]}`)) {
			t.Errorf("body = %s, want the payload verbatim", body)
		}
		if _, ok := got.Context().Deadline(); !ok {
			t.Error("the request carries no deadline; the export budget is a hard total timeout")
		}
	})

	t.Run("the traces path defaults when unset", func(t *testing.T) {
		var got *http.Request
		stubTransport(t, func(req *http.Request) (*http.Response, error) {
			got = req
			return fakeResponse(200, `{}`), nil
		})
		cfg := testConfig()
		cfg.OTLPHTTPPathTraces = ""
		if err := Export(cfg, []byte(`{}`)); err != nil {
			t.Fatalf("Export: %v", err)
		}
		if want := "http://127.0.0.1:5080/v1/traces"; got.URL.String() != want {
			t.Errorf("url = %s, want the spec default %s", got.URL, want)
		}
	})

	t.Run("the export budget bounds the whole exchange", func(t *testing.T) {
		cfg := testConfig()
		cfg.ExportTimeoutMS = 40
		start := time.Now()
		stubTransport(t, func(req *http.Request) (*http.Response, error) {
			// The fallback arm is what makes this test FAIL rather than HANG when the deadline
			// is removed: without it the context never closes, and the missing budget would
			// surface ten minutes later as a suite-wide timeout panic instead of as this
			// subtest's own message.
			select {
			case <-req.Context().Done():
				return nil, req.Context().Err()
			case <-time.After(2 * time.Second):
				return fakeResponse(200, `{}`), nil
			}
		})
		err := Export(cfg, []byte(`{}`))
		elapsed := time.Since(start)
		if err == nil {
			t.Fatal("a request that outlives the budget must fail; the request carried no deadline")
		}
		if elapsed > 2*time.Second {
			t.Errorf("export took %s; the configured budget was %dms", elapsed, cfg.ExportTimeoutMS)
		}
	})

	t.Run("no header value ever reaches an error string", func(t *testing.T) {
		const secret = "Basic c3VwZXItc2VjcmV0LXZhbHVl"
		cfg := testConfig()
		cfg.Headers = map[string]string{"Authorization": secret}
		stubTransport(t, func(*http.Request) (*http.Response, error) {
			return fakeResponse(401, "unauthorized: token "+secret+" rejected"), nil
		})
		err := Export(cfg, []byte(`{}`))
		if err == nil {
			t.Fatal("a 401 must be an error")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error echoed a header value: %v", err)
		}
	})

	t.Run("an undereferenced secret reference is refused, not sent", func(t *testing.T) {
		// This package cannot honour the containment rule that a secret path must resolve
		// inside the factory root, because it is handed no root. Sending the literal
		// reference as a credential would be both wrong and silent, so it is refused here and
		// dereferencing stays with the caller that owns the root.
		cfg := testConfig()
		cfg.Headers = map[string]string{"Authorization": "file:/etc/shadow"}
		called := false
		stubTransport(t, func(*http.Request) (*http.Response, error) {
			called = true
			return fakeResponse(200, `{}`), nil
		})
		err := Export(cfg, []byte(`{}`))
		if err == nil {
			t.Fatal("an undereferenced secret reference must be refused")
		}
		if called {
			t.Error("the literal secret reference was sent to the backend as a credential")
		}
		if strings.Contains(err.Error(), "/etc/shadow") {
			t.Errorf("the error echoed the secret path: %v", err)
		}
		if !strings.Contains(err.Error(), "Authorization") {
			t.Errorf("the error does not name the offending header key: %v", err)
		}
	})

	t.Run("an unconfigured endpoint is refused before any request is built", func(t *testing.T) {
		cfg := testConfig()
		cfg.Endpoint = ""
		called := false
		stubTransport(t, func(*http.Request) (*http.Response, error) {
			called = true
			return fakeResponse(200, `{}`), nil
		})
		if err := Export(cfg, []byte(`{}`)); err == nil {
			t.Fatal("an empty endpoint must be an error")
		}
		if called {
			t.Error("a request was attempted with no endpoint")
		}
	})
}
