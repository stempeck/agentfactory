package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestHealthz_OK — the web module's rendezvous liveness probe (web/internal/rendezvous) GETs
// /healthz and reuses a running server only on 200. It returns 200 {ok:true}.
func TestHealthz_OK(t *testing.T) {
	s := New(&fakeMutator{}, fakeAssembler{}, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", rec.Code)
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("/healthz response not JSON: %v", err)
	}
	if env["ok"] != true {
		t.Errorf("/healthz ok = %v, want true", env["ok"])
	}
}

// TestHealthz_CarriesRoot — K5/Gap 7: /healthz surfaces the resolved factory root so an operator (or
// an automated probe) can see WHICH factory the console resolved to. The root is nested under data
// (Envelope.Data is json:"data,omitempty"), so the bare {ok:true} liveness contract is preserved.
func TestHealthz_CarriesRoot(t *testing.T) {
	const root = "/some/root"
	s := New(&fakeMutator{}, fakeAssembler{}, nil, WithRoot(root))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200", rec.Code)
	}
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("/healthz response not JSON: %v", err)
	}
	if env["ok"] != true {
		t.Errorf("/healthz ok = %v, want true", env["ok"])
	}
	data, ok := env["data"].(map[string]any)
	if !ok {
		t.Fatalf("/healthz data = %v, want a JSON object carrying the served root", env["data"])
	}
	if data["root"] != root {
		t.Errorf("/healthz data.root = %v, want %q", data["root"], root)
	}
}

// TestHealthz_OpenWhenNotLoopback — liveness must NOT require the session token even when the bind
// is non-loopback (where the API mandates auth), so the loopback rendezvous probe is never blocked.
// Mirrors mcpstore's unauthenticated /health.
func TestHealthz_OpenWhenNotLoopback(t *testing.T) {
	s := New(&fakeMutator{}, fakeAssembler{}, nil, WithBind("0.0.0.0:8080"), WithToken("secret"))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("/healthz status = %d, want 200 (must stay open even when not loopback)", rec.Code)
	}
}
