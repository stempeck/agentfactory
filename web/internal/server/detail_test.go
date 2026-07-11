package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/exec"
	"github.com/stempeck/agentfactory-web/internal/readmodel"
)

// ============================================================================
// #500 — GET /api/agents/{name}/detail  +  POST /api/agents/{name}/mail
// (Shared fakes/helpers — fakeMutator, fakeAssembler, fakeRunner, loopbackPOST,
//  writeAgentsJSON — live in server_test.go, same package.)
// ============================================================================

// fakeTailer returns a canned TailView and records the (name, lines) of each call. It never execs.
type fakeTailer struct {
	view      readmodel.TailView
	err       error
	calls     int
	lastName  string
	lastLines int
}

func (f *fakeTailer) Tail(ctx context.Context, name string, lines int) (readmodel.TailView, error) {
	f.calls++
	f.lastName, f.lastLines = name, lines
	return f.view, f.err
}

// fakeMailSender records the (name, subject, body) of each MailSend and returns a canned result.
type fakeMailSender struct {
	res     exec.Result
	err     error
	calls   int
	name    string
	subject string
	body    string
}

func (f *fakeMailSender) MailSend(ctx context.Context, name, subject, body string) (exec.Result, error) {
	f.calls++
	f.name, f.subject, f.body = name, subject, body
	return f.res, f.err
}

// fakeFormula is a hermetic FormulaResolver double for the detail route's declared_formula field.
type fakeFormula struct {
	formula string
	found   bool
	err     error
}

func (f fakeFormula) AgentFormula(ctx context.Context, name string) (string, bool, error) {
	return f.formula, f.found, f.err
}

var _ Tailer = (*fakeTailer)(nil)
var _ MailSender = (*fakeMailSender)(nil)
var _ FormulaResolver = fakeFormula{}

// backslash is a single '\' char, built from its rune so no literal escape sequence appears in
// this source file (keeps the control-char JSON fixtures free of an escape the tooling mangles).
var backslash = string(rune(0x5c))

// AC-1 — the detail payload carries all twelve AgentView keys, the RUNNING vs DECLARED formula as
// DISTINCT fields (#455), and tail.{live,output,captured_at,lines}; plus Cache-Control: no-store.
func TestHandleAgentDetail_Fields(t *testing.T) {
	views := []readmodel.AgentView{{
		Name: "alpha", Type: "worker", Formula: "running-formula", Running: true,
		Status: "working", StepID: "s1", StepTitle: "Do the thing", StepState: "ready",
		Inputs: map[string]string{"issue": "42"},
	}}
	tl := &fakeTailer{view: readmodel.TailView{Live: true, Output: "pane text", Lines: 120}}
	s := New(&fakeMutator{}, fakeAssembler{views: views}, nil,
		WithTailer(tl),
		WithFormulaResolver(fakeFormula{formula: "declared-formula", found: true}))

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/alpha/detail", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("detail: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", got)
	}

	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Agent           map[string]any `json:"agent"`
			DeclaredFormula string         `json:"declared_formula"`
			Tail            map[string]any `json:"tail"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode detail: %v; body=%s", err, rec.Body.String())
	}
	if !env.OK {
		t.Fatalf("ok=false; body=%s", rec.Body.String())
	}
	for _, k := range []string{"name", "type", "formula", "running", "status", "step_id", "step_title", "step_state", "is_gate", "gate_id", "inputs", "assembled_at"} {
		if _, ok := env.Data.Agent[k]; !ok {
			t.Errorf("agent.%s missing from detail payload", k)
		}
	}
	if env.Data.Agent["formula"] != "running-formula" {
		t.Errorf("agent.formula = %v, want running-formula (the RUNNING formula)", env.Data.Agent["formula"])
	}
	if env.Data.DeclaredFormula != "declared-formula" {
		t.Errorf("declared_formula = %q, want declared-formula (agents.json), DISTINCT from agent.formula", env.Data.DeclaredFormula)
	}
	for _, k := range []string{"live", "output", "captured_at", "lines"} {
		if _, ok := env.Data.Tail[k]; !ok {
			t.Errorf("tail.%s missing", k)
		}
	}
	if tl.lastLines != 120 {
		t.Errorf("default line count = %d, want 120", tl.lastLines)
	}
}

// A gated agent surfaces gate_id / is_gate honestly.
func TestHandleAgentDetail_GateFixture(t *testing.T) {
	views := []readmodel.AgentView{{Name: "gamma", Running: true, Status: "gated", IsGate: true, GateID: "design-feedback-1"}}
	s := New(&fakeMutator{}, fakeAssembler{views: views}, nil, WithTailer(&fakeTailer{view: readmodel.TailView{Live: true, Lines: 120}}))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/gamma/detail", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("gate detail: code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"gate_id":"design-feedback-1"`) || !strings.Contains(body, `"is_gate":true`) {
		t.Errorf("gate fixture did not surface is_gate/gate_id: %s", body)
	}
}

// A stopped agent is a VALUE (200 + tail.live=false + declared_formula), NEVER an error.
func TestHandleAgentDetail_StoppedAgent(t *testing.T) {
	views := []readmodel.AgentView{{Name: "beta", Running: false, Status: "stopped"}}
	s := New(&fakeMutator{}, fakeAssembler{views: views}, nil,
		WithTailer(&fakeTailer{view: readmodel.TailView{Live: false, Lines: 120}}),
		WithFormulaResolver(fakeFormula{formula: "beta-formula", found: true}))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/beta/detail", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("stopped detail: code=%d, want 200 (stopped is a value); body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"live":false`) {
		t.Errorf("tail.live should be false for a stopped agent: %s", body)
	}
	if !strings.Contains(body, `"declared_formula":"beta-formula"`) {
		t.Errorf("declared_formula should still surface for a stopped agent: %s", body)
	}
}

// An unknown agent is an honest 404 (read-model membership is the oracle).
func TestHandleAgentDetail_Unknown404(t *testing.T) {
	s := New(&fakeMutator{}, fakeAssembler{views: []readmodel.AgentView{{Name: "alpha"}}}, nil, WithTailer(&fakeTailer{}))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/ghost/detail", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown detail: code=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "agent ghost not found") {
		t.Errorf("want honest 404 copy 'agent ghost not found', got %s", rec.Body.String())
	}
}

// The ?lines= query is SILENTLY clamped to [1,500] (default 120) — never a 4xx (scale.md S3-1).
func TestHandleAgentDetail_LinesClampNever4xx(t *testing.T) {
	views := []readmodel.AgentView{{Name: "alpha", Running: true, Status: "working"}}
	cases := []struct {
		q    string
		want int
	}{
		{"", 120}, {"abc", 120}, {"50", 50}, {"0", 1}, {"-5", 1}, {"9999", 500}, {"500", 500}, {"1", 1},
	}
	for _, c := range cases {
		tl := &fakeTailer{view: readmodel.TailView{Live: true}}
		s := New(&fakeMutator{}, fakeAssembler{views: views}, nil, WithTailer(tl))
		url := "/api/agents/alpha/detail"
		if c.q != "" {
			url += "?lines=" + c.q
		}
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, url, nil))
		if rec.Code != http.StatusOK {
			t.Errorf("lines=%q: code=%d, want 200 (clamp is never a 4xx)", c.q, rec.Code)
		}
		if tl.lastLines != c.want {
			t.Errorf("lines=%q: clamped to %d, want %d", c.q, tl.lastLines, c.want)
		}
	}
}

// nil Tailer / nil MailSender ⇒ clean 500 (nil-seam parity with /dispatch, /settings).
func TestAgentDetailAndMail_NilDeps500(t *testing.T) {
	s := New(&fakeMutator{}, fakeAssembler{views: []readmodel.AgentView{{Name: "alpha", Running: true}}}, nil)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/alpha/detail", nil))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "tailer not configured") {
		t.Fatalf("detail nil tailer: code=%d body=%s, want 500 'tailer not configured'", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/mail", `{"subject":"s","body":"b"}`))
	if rec.Code != http.StatusInternalServerError || !strings.Contains(rec.Body.String(), "mail sender not configured") {
		t.Fatalf("mail nil mailer: code=%d body=%s, want 500 'mail sender not configured'", rec.Code, rec.Body.String())
	}
}

// AC-4 — a valid mail POST queues with the honest async copy and threads (name, subject, body).
func TestHandleAgentMail_Success(t *testing.T) {
	ms := &fakeMailSender{}
	s := New(&fakeMutator{}, fakeAssembler{views: []readmodel.AgentView{{Name: "alpha", Running: true}}}, nil, WithMailer(ms))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/mail", `{"subject":"hello","body":"world"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("mail: code=%d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "Mail queued for alpha — delivery is asynchronous") {
		t.Errorf("want honest async copy, got %s", rec.Body.String())
	}
	if ms.calls != 1 || ms.name != "alpha" || ms.subject != "hello" || ms.body != "world" {
		t.Errorf("MailSend args = (calls=%d, %q,%q,%q), want (1, alpha,hello,world)", ms.calls, ms.name, ms.subject, ms.body)
	}
}

// Empty subject / empty body / control chars ⇒ direct 400 with the friendly copy; MailSend not
// called. The control-char bodies are VALID JSON (a  escape, assembled from the backslash
// rune) whose decoded value carries a real control char, so the handler's content validation — not
// the JSON decoder — is what rejects them.
func TestHandleAgentMail_ContentValidation400(t *testing.T) {
	views := []readmodel.AgentView{{Name: "alpha", Running: true}}
	ctrlSubj := `{"subject":"a` + backslash + `u0001b","body":"b"}`
	ctrlBody := `{"subject":"s","body":"a` + backslash + `u0001b"}`
	cases := []struct{ name, body, wantMsg string }{
		{"empty subject", `{"subject":"   ","body":"b"}`, "subject is required"},
		{"empty body", `{"subject":"s","body":""}`, "message body is required"},
		{"control-char subject", ctrlSubj, "control"},
		{"control-char body", ctrlBody, "control"},
	}
	for _, c := range cases {
		ms := &fakeMailSender{}
		s := New(&fakeMutator{}, fakeAssembler{views: views}, nil, WithMailer(ms))
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/mail", c.body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: code=%d, want 400; body=%s", c.name, rec.Code, rec.Body.String())
		}
		if !strings.Contains(rec.Body.String(), c.wantMsg) {
			t.Errorf("%s: want message containing %q, got %s", c.name, c.wantMsg, rec.Body.String())
		}
		if ms.calls != 0 {
			t.Errorf("%s: MailSend must NOT be called on a 400 (got %d)", c.name, ms.calls)
		}
	}
}

// Mail to an unknown agent is a 404; MailSend never fires.
func TestHandleAgentMail_Unknown404(t *testing.T) {
	ms := &fakeMailSender{}
	s := New(&fakeMutator{}, fakeAssembler{views: []readmodel.AgentView{{Name: "alpha", Running: true}}}, nil, WithMailer(ms))
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/ghost/mail", `{"subject":"s","body":"b"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown mail: code=%d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if ms.calls != 0 {
		t.Errorf("MailSend must not fire for an unknown agent (got %d)", ms.calls)
	}
}

// A DISPATCHED agent is STILL mailable — mail bypasses mutate()'s dispatched-marker pre-flight
// (its primary recipients ARE dispatched agents). Driven through the REAL exec.Wrapper.
func TestHandleAgentMail_DispatchedAgentStillMailable(t *testing.T) {
	root := t.TempDir()
	writeAgentsJSON(t, root, `{"agents":{"alpha":{"formula":"minimalworker"}}}`)
	markerDir := filepath.Join(root, ".agentfactory", "agents", "alpha", ".runtime")
	if err := os.MkdirAll(markerDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(markerDir, "dispatched"), []byte("1"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := &fakeRunner{}
	w := exec.NewWrapper(fr, root) // non-empty root ⇒ the dispatched pre-flight is ACTIVE for mutate()
	s := New(w, fakeAssembler{views: []readmodel.AgentView{{Name: "alpha", Running: true}}}, nil, WithMailer(w))

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/mail", `{"subject":"s","body":"b"}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("dispatched-agent mail: code=%d, want 200 (mail bypasses the pre-flight); body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := fr.argsFor("mail"); !ok {
		t.Errorf("the mail verb never reached the Runner (was it wrongly refused?); verbs=%v", fr.verbs)
	}
}

// TestValidateMailContent unit-tests the friendly-copy validator directly (control chars supplied
// via string(rune(1)) so no JSON/escape layer is involved).
func TestValidateMailContent(t *testing.T) {
	ctrl := rune(1)
	if msg, ok := validateMailContent("   ", "b"); ok || msg != "subject is required" {
		t.Errorf("empty subject: msg=%q ok=%v", msg, ok)
	}
	if msg, ok := validateMailContent("s", ""); ok || msg != "message body is required" {
		t.Errorf("empty body: msg=%q ok=%v", msg, ok)
	}
	if _, ok := validateMailContent("a"+string(ctrl)+"b", "b"); ok {
		t.Error("control-char subject must be rejected")
	}
	if _, ok := validateMailContent("s", "a"+string(ctrl)+"b"); ok {
		t.Error("control-char body must be rejected")
	}
	if _, ok := validateMailContent(strings.Repeat("x", 201), "b"); ok {
		t.Error("subject > 200 runes must be rejected")
	}
	if _, ok := validateMailContent("s", strings.Repeat("x", 10001)); ok {
		t.Error("body > 10000 runes must be rejected")
	}
	if _, ok := validateMailContent("s", "line1\nline2\twith tab"); !ok {
		t.Error("a multi-line body with newline+tab must be ACCEPTED")
	}
}

// Guard parity: detail GET needs a token off-loopback; mail POST enforces the Origin CSRF allowlist.
func TestAgentDetailMail_Guarded(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef"
	views := []readmodel.AgentView{{Name: "alpha", Running: true}}

	exposed := New(&fakeMutator{}, fakeAssembler{views: views}, nil,
		WithBind("0.0.0.0:8080"), WithToken(tok), WithTailer(&fakeTailer{view: readmodel.TailView{Live: true}}), WithMailer(&fakeMailSender{}))
	rec := httptest.NewRecorder()
	exposed.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/alpha/detail", nil))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("detail off-loopback, no token: code=%d, want 401", rec.Code)
	}
	rec = httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/agents/alpha/detail", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	exposed.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("detail with a valid token should be accepted, got 401")
	}

	local := New(&fakeMutator{}, fakeAssembler{views: views}, nil, WithTailer(&fakeTailer{}), WithMailer(&fakeMailSender{}))
	rec = httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "/api/agents/alpha/mail", strings.NewReader(`{"subject":"s","body":"b"}`))
	bad.Header.Set("Origin", "http://evil.example.com")
	local.Handler().ServeHTTP(rec, bad)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("mail bad Origin: code=%d, want 403", rec.Code)
	}
}

// The CSP lands on the HTML document ONLY (never on API JSON); the detail route carries no-store.
func TestSecurityHeaders_CSPOnDocumentNotAPI(t *testing.T) {
	doc := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("<!doctype html><title>x</title>"))
	})
	views := []readmodel.AgentView{{Name: "alpha", Running: true}}
	s := New(&fakeMutator{}, fakeAssembler{views: views}, doc, WithTailer(&fakeTailer{view: readmodel.TailView{Live: true}}))

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	csp := rec.Header().Get("Content-Security-Policy")
	if csp == "" {
		t.Error("document GET / must carry a Content-Security-Policy")
	} else if !strings.Contains(csp, "default-src 'self'") || !strings.Contains(csp, "'unsafe-inline'") {
		t.Errorf("unexpected CSP policy: %q", csp)
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if got := rec.Header().Get("Content-Security-Policy"); got != "" {
		t.Errorf("API JSON must NOT carry a CSP, got %q", got)
	}

	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/alpha/detail", nil))
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("detail Cache-Control = %q, want no-store", got)
	}

	// #534 — the transplanted formula-editor subtree carries its OWN document policy: the approved
	// screens execute inline scripts (containment recorded on editorCSPPolicy; bytes CI-pinned by
	// web/internal/web/reconstruct_test.go). The shell document above must NOT get script inline.
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/formula-editor/screens/roster.html", nil))
	editorCSP := rec.Header().Get("Content-Security-Policy")
	if !strings.Contains(editorCSP, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("formula-editor documents must allow the approved inline scripts, got %q", editorCSP)
	}
	if strings.Contains(csp, "script-src 'self' 'unsafe-inline'") {
		t.Errorf("the SHELL document must keep the strict script-src 'self' policy, got %q", csp)
	}
}
