package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/feedback"
	"github.com/stempeck/agentfactory-web/internal/proto"
	"github.com/stempeck/agentfactory-web/internal/readmodel"
)

// TestPrototypes_RoutesWired proves the C7/C6 routes are registered and reachable end-to-end through
// the real server (the package-level proto/feedback tests cover the units; this proves the wiring,
// the {ok,message,data} envelope, the feedback_open annotation, and the state-changing CSRF guard).
// fakeAssembler (server_test.go) satisfies BOTH the server's Assembler and feedback.AgentSource.
func TestPrototypes_RoutesWired(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".designs", "web-ui", "prototype-v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("INDEX_MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}

	// an owning agent verified parked at design-feedback-1 for web-ui.
	src := fakeAssembler{views: []readmodel.AgentView{{
		Name: "web-design", Formula: "web-design",
		StepState: "ready", IsGate: true, GateID: "design-feedback-1",
		Inputs: map[string]string{"output_dir": filepath.Join(root, ".designs", "web-ui")},
	}}}

	s := New(&fakeMutator{}, src, nil,
		WithPrototypes(proto.New(root)),
		WithFeedback(feedback.New(root, src)),
	)
	h := s.Handler()

	// GET /api/prototypes → enumerated, feedback_open annotated true.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/prototypes", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/prototypes code=%d body=%q", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool
		Data []proto.Prototype
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode prototypes: %v", err)
	}
	if !env.OK || len(env.Data) != 1 || env.Data[0].ID != "web-ui" || !env.Data[0].FeedbackOpen {
		t.Fatalf("prototypes envelope = %+v, want one web-ui with feedback_open=true", env)
	}

	// GET /proto/web-ui/index.html → static served rooted at the prototype dir.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/proto/web-ui/index.html", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "INDEX_MARKER") {
		t.Fatalf("GET /proto/web-ui/index.html code=%d body=%q", rec.Code, rec.Body.String())
	}

	// POST feedback (state-changing, loopback Origin) at the gate → writes the form, ok:true.
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, loopbackPOST("/api/prototypes/web-ui/feedback", `{"decision":"APPROVED","notes":"looks great"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("POST feedback code=%d body=%q", rec.Code, rec.Body.String())
	}
	var fb struct {
		OK      bool
		Message string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &fb); err != nil {
		t.Fatalf("decode feedback: %v", err)
	}
	if !fb.OK {
		t.Fatalf("feedback ok=false: %s", fb.Message)
	}
	if _, err := os.Stat(filepath.Join(dir, "feedback-form.md")); err != nil {
		t.Fatalf("feedback-form.md not written: %v", err)
	}

	// A cross-origin feedback POST is refused by the CSRF guard (state-changing).
	rec = httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "/api/prototypes/web-ui/feedback", strings.NewReader(`{}`))
	bad.Header.Set("Origin", "http://evil.example.com")
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-origin feedback POST code=%d, want 403 (CSRF)", rec.Code)
	}
}

// TestFeedback_OffGate_HTTPRefusal drives the OFF-GATE path end-to-end through the real handler
// (H-5): when no owning agent is parked at the matching gate, POST .../feedback returns 200 ok:false
// with the honest "not currently open" message and the pre-existing form is byte-for-byte unchanged.
func TestFeedback_OffGate_HTTPRefusal(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, ".designs", "web-ui", "prototype-v1")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("INDEX"), 0o644); err != nil {
		t.Fatal(err)
	}
	formPath := filepath.Join(dir, "feedback-form.md")
	const blank = "# Design Feedback - Iteration 1\n\n## Decision\n- Decision: ______\n\n## Notes\n[free-form feedback]\n"
	if err := os.WriteFile(formPath, []byte(blank), 0o644); err != nil {
		t.Fatal(err)
	}

	// owning agent present but NOT parked at a gate (working) → feedback must be refused.
	src := fakeAssembler{views: []readmodel.AgentView{{
		Name: "web-design", StepState: "running", IsGate: false, GateID: "",
		Inputs: map[string]string{"output_dir": filepath.Join(root, ".designs", "web-ui")},
	}}}
	s := New(&fakeMutator{}, src, nil, WithPrototypes(proto.New(root)), WithFeedback(feedback.New(root, src)))

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/prototypes/web-ui/feedback", `{"decision":"APPROVED","notes":"x"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("off-gate POST code=%d, want 200 (honest ok:false value)", rec.Code)
	}
	var env struct {
		OK      bool
		Message string
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if env.OK || !strings.Contains(strings.ToLower(env.Message), "not currently open") {
		t.Fatalf("off-gate envelope = %+v, want ok:false + 'not currently open'", env)
	}
	after, err := os.ReadFile(formPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != blank {
		t.Fatalf("off-gate HTTP write MUTATED feedback-form.md:\n got %q\nwant %q", string(after), blank)
	}

	// the same prototype is enumerated with feedback_open=false (panel disabled honestly).
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/prototypes", nil))
	var list struct {
		Data []proto.Prototype
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list.Data) != 1 || list.Data[0].FeedbackOpen {
		t.Fatalf("enumeration = %+v, want one web-ui with feedback_open=false", list.Data)
	}
}
