package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/stempeck/agentfactory-web/internal/config"
	"github.com/stempeck/agentfactory-web/internal/dispatch"
	"github.com/stempeck/agentfactory-web/internal/exec"
	"github.com/stempeck/agentfactory-web/internal/formschema"
	"github.com/stempeck/agentfactory-web/internal/readmodel"
)

// fakeMutator records mutating calls; never execs.
type fakeMutator struct {
	mu    sync.Mutex
	calls []string
	err   error
}

func (f *fakeMutator) record(s string) {
	f.mu.Lock()
	f.calls = append(f.calls, s)
	f.mu.Unlock()
}
func (f *fakeMutator) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func (f *fakeMutator) Up(ctx context.Context) (exec.Result, error) {
	f.record("up")
	return exec.Result{}, f.err
}
func (f *fakeMutator) DownFactory(ctx context.Context, reset bool) (exec.Result, error) {
	f.record("downFactory")
	return exec.Result{}, f.err
}
func (f *fakeMutator) DownAgent(ctx context.Context, name string, reset bool) (exec.Result, error) {
	f.record("downAgent:" + name)
	return exec.Result{}, f.err
}
func (f *fakeMutator) Sling(ctx context.Context, name, task string, vars map[string]string) (exec.Result, error) {
	f.record("sling:" + name)
	return exec.Result{}, f.err
}

// fakeAssembler returns canned views.
type fakeAssembler struct {
	views []readmodel.AgentView
	err   error
}

func (f fakeAssembler) Assemble(ctx context.Context) ([]readmodel.AgentView, error) {
	return f.views, f.err
}

var _ Mutator = (*fakeMutator)(nil)
var _ Assembler = fakeAssembler{}

// fakeRunner implements exec.Runner so a test can drive the REAL exec.Wrapper hermetically.
// It records the full argv of every call and can return a canned Result per verb (e.g. the
// `af formula show --json` payload the form/sling key-validation path needs).
type fakeRunner struct {
	mu    sync.Mutex
	verbs []string
	args  [][]string
	resp  map[string]exec.Result // canned stdout keyed by VERB; default {Stdout:"[]"} when absent
	// respByName is an optional per-formula-NAME table for the `formula` verb (argv =
	// ["show", <name>, "--json"], so the name is args[1]). When set it takes precedence over
	// resp["formula"], letting a two-agent test return DISTINCT schemas per resolved formula
	// (resp alone is keyed by VERB, so it cannot — Gap 4). nil ⇒ unchanged verb-keyed behaviour.
	respByName map[string]exec.Result
}

func (f *fakeRunner) Run(ctx context.Context, verb string, args ...string) (exec.Result, error) {
	return f.RunStdin(ctx, nil, verb, args...)
}

func (f *fakeRunner) RunStdin(ctx context.Context, stdin []byte, verb string, args ...string) (exec.Result, error) {
	f.mu.Lock()
	f.verbs = append(f.verbs, verb)
	f.args = append(f.args, append([]string(nil), args...))
	if verb == "formula" && f.respByName != nil && len(args) >= 2 {
		if r, ok := f.respByName[args[1]]; ok { // per-formula payload keyed by the resolved NAME
			f.mu.Unlock()
			return r, nil
		}
	}
	r, ok := f.resp[verb]
	f.mu.Unlock()
	if !ok {
		return exec.Result{Stdout: "[]"}, nil
	}
	return r, nil
}

// argsFor returns the argv recorded for the first call to the given verb.
func (f *fakeRunner) argsFor(verb string) ([]string, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, v := range f.verbs {
		if v == verb {
			return f.args[i], true
		}
	}
	return nil, false
}

// formulaNames returns, in call order, the resolved formula NAME (args[1]) of every
// `af formula show <name> --json` call. argsFor returns only the FIRST `formula` call, so the
// two-agent AC-1 test reads each per-agent resolution here. A wrong name reaching the form (e.g.
// the read-model's running formula, were the Phase-1 fix reverted) shows up as the wrong args[1].
func (f *fakeRunner) formulaNames() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var names []string
	for i, v := range f.verbs {
		if v == "formula" && len(f.args[i]) >= 2 {
			names = append(names, f.args[i][1])
		}
	}
	return names
}

var _ exec.Runner = (*fakeRunner)(nil)

// minimalworker: one required input `task`, one hidden deferred var `orchestrator`. The
// user-providable field set is exactly {task}.
const minimalworkerFormulaJSON = `{"name":"minimalworker","description":"d","type":"workflow","inputs":[{"name":"task","description":"the task","type":"string","required":true,"required_unless":null,"default":"","source":""}],"vars":[{"name":"orchestrator","description":"o","type":"","required":false,"required_unless":null,"default":"","source":"deferred"}]}`

// multi: two user-providable fields — required input `task` + cli var `k`.
const multiFormulaJSON = `{"name":"multi","description":"d","type":"workflow","inputs":[{"name":"task","description":"the task","type":"string","required":true,"required_unless":null,"default":"","source":""}],"vars":[{"name":"k","description":"k","type":"","required":false,"required_unless":null,"default":"","source":"cli"}]}`

func loopbackPOST(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Origin", "http://127.0.0.1:0")
	return r
}

// loopbackPUT is the PUT analogue of loopbackPOST: PUT is state-changing, so it must carry a
// loopback Origin to pass the CSRF gate.
func loopbackPUT(path, body string) *http.Request {
	r := httptest.NewRequest(http.MethodPut, path, strings.NewReader(body))
	r.Header.Set("Origin", "http://127.0.0.1:0")
	return r
}

// fakeSettings is a hermetic SettingsService double: it returns a canned curated read and records
// the (file, payload) of every write without ever touching disk or spawning af.
type fakeSettings struct {
	view      config.Settings
	readErr   error
	writeErr  error
	writeRes  exec.Result // lets a test simulate af's exit code (non-zero ⇒ validation rejection)
	lastFile  string
	lastBytes []byte
}

func (f *fakeSettings) Read(ctx context.Context) (config.Settings, error) {
	return f.view, f.readErr
}
func (f *fakeSettings) Write(ctx context.Context, file string, payload []byte) (exec.Result, error) {
	f.lastFile = file
	f.lastBytes = append([]byte(nil), payload...)
	return f.writeRes, f.writeErr
}

var _ SettingsService = (*fakeSettings)(nil)

// AC5 — Server binds loopback on an ephemeral port.
func TestServer_BindsLoopback(t *testing.T) {
	s := New(&fakeMutator{}, fakeAssembler{}, nil)
	ln, err := s.Listen()
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })

	addr, ok := ln.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("addr is not TCP: %v", ln.Addr())
	}
	if !addr.IP.IsLoopback() {
		t.Fatalf("bind IP %s is not loopback", addr.IP)
	}
	if addr.IP.String() == "0.0.0.0" {
		t.Fatalf("must never bind 0.0.0.0")
	}
	if addr.Port == 0 {
		t.Fatalf("ephemeral port was not assigned")
	}
}

// AC2 — Auth required when not loopback; loopback Origin still enforced on POST.
func TestAuthRequiredWhenNotLoopback(t *testing.T) {
	const tok = "0123456789abcdef0123456789abcdef"

	// --- non-loopback bind: token mandatory ---
	exposed := New(&fakeMutator{}, fakeAssembler{}, nil, WithBind("0.0.0.0:8080"), WithToken(tok))

	// no token → 401
	rec := httptest.NewRecorder()
	exposed.Handler().ServeHTTP(rec, loopbackPOST("/api/factory/up", ""))
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-loopback, no token: code = %d, want 401", rec.Code)
	}

	// wrong token of equal length → 401 (exercises constant-time compare, not mere presence)
	rec = httptest.NewRecorder()
	req := loopbackPOST("/api/factory/up", "")
	req.Header.Set("Authorization", "Bearer "+strings.Repeat("f", len(tok)))
	exposed.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("non-loopback, wrong token: code = %d, want 401", rec.Code)
	}

	// valid token → not 401 (the verb path runs)
	rec = httptest.NewRecorder()
	req = loopbackPOST("/api/factory/up", "")
	req.Header.Set("Authorization", "Bearer "+tok)
	exposed.Handler().ServeHTTP(rec, req)
	if rec.Code == http.StatusUnauthorized {
		t.Fatalf("non-loopback, valid token should be accepted, got 401")
	}

	// --- loopback bind: token optional, but Origin still enforced on POST ---
	local := New(&fakeMutator{}, fakeAssembler{}, nil, WithBind("127.0.0.1:0"))

	// bad Origin → 403 even though token is optional
	rec = httptest.NewRecorder()
	bad := httptest.NewRequest(http.MethodPost, "/api/factory/up", nil)
	bad.Header.Set("Origin", "http://evil.example.com")
	local.Handler().ServeHTTP(rec, bad)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("loopback, bad Origin: code = %d, want 403", rec.Code)
	}

	// good loopback Origin, no token → allowed
	rec = httptest.NewRecorder()
	local.Handler().ServeHTTP(rec, loopbackPOST("/api/factory/up", ""))
	if rec.Code == http.StatusUnauthorized || rec.Code == http.StatusForbidden {
		t.Fatalf("loopback, good Origin, no token should be allowed, got %d", rec.Code)
	}
}

// CSRF Origin allowlist as a standalone control.
func TestPostRejectsBadOrigin(t *testing.T) {
	s := New(&fakeMutator{}, fakeAssembler{}, nil)
	for _, origin := range []string{"http://evil.example.com", "https://attacker.test", "http://10.0.0.5"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/factory/up", nil)
		req.Header.Set("Origin", origin)
		s.Handler().ServeHTTP(rec, req)
		if rec.Code != http.StatusForbidden {
			t.Errorf("Origin %q: code = %d, want 403", origin, rec.Code)
		}
	}
}

// Destructive ops require confirm:true server-side — the mutator is NEVER invoked otherwise.
func TestDownReset_RequiresConfirm(t *testing.T) {
	// agent reset without confirm
	fm := &fakeMutator{}
	s := New(fm, fakeAssembler{}, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/down", `{"reset":true,"confirm":false}`))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("agent reset w/o confirm: code = %d, want 422", rec.Code)
	}
	if fm.count() != 0 {
		t.Fatalf("reset w/o confirm must not invoke the mutator (got %d calls)", fm.count())
	}

	// factory reset without confirm
	fm2 := &fakeMutator{}
	s2 := New(fm2, fakeAssembler{}, nil)
	rec = httptest.NewRecorder()
	s2.Handler().ServeHTTP(rec, loopbackPOST("/api/factory/down", `{"reset":true,"confirm":false}`))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("factory reset w/o confirm: code = %d, want 422", rec.Code)
	}
	if fm2.count() != 0 {
		t.Fatalf("factory reset w/o confirm must not invoke the mutator (got %d calls)", fm2.count())
	}

	// agent reset WITH confirm → proceeds
	fm3 := &fakeMutator{}
	s3 := New(fm3, fakeAssembler{}, nil)
	rec = httptest.NewRecorder()
	s3.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/down", `{"reset":true,"confirm":true}`))
	if fm3.count() != 1 {
		t.Fatalf("reset WITH confirm should invoke the mutator once (got %d)", fm3.count())
	}
}

// Uniform {ok,message,data} envelope on success and error.
func TestEnvelope_UniformShape(t *testing.T) {
	// success read
	s := New(&fakeMutator{}, fakeAssembler{views: []readmodel.AgentView{{Name: "x", Status: "idle"}}}, nil)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	var env map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("agents response not JSON: %v", err)
	}
	if ok, present := env["ok"]; !present || ok != true {
		t.Fatalf("success envelope ok = %v (present=%v), want true", ok, present)
	}

	// error read
	s2 := New(&fakeMutator{}, fakeAssembler{err: context.DeadlineExceeded}, nil)
	rec = httptest.NewRecorder()
	s2.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents", nil))
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("error response not JSON: %v", err)
	}
	if env["ok"] != false {
		t.Fatalf("error envelope ok = %v, want false", env["ok"])
	}
	if _, present := env["message"]; !present {
		t.Fatalf("error envelope must carry a message")
	}
}

// Handlers run against the REAL exec.Wrapper backed by a fake Runner — proving the handler uses
// the injectable seam (never exec.Command directly) and the suite stays hermetic.
func TestServer_HandlerUsesFakeRunner(t *testing.T) {
	fr := &fakeRunner{}
	wrapper := exec.NewWrapper(fr, "")
	rm := readmodel.New(wrapper, stubLiveness{})
	s := New(wrapper, rm, nil)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/factory/up", ""))
	if rec.Code != http.StatusOK {
		t.Fatalf("up: code = %d, want 200", rec.Code)
	}
	fr.mu.Lock()
	defer fr.mu.Unlock()
	if len(fr.verbs) != 1 || fr.verbs[0] != "up" {
		t.Fatalf("expected the handler to reach the Runner with verb 'up', got %v", fr.verbs)
	}
}

type stubLiveness struct{}

func (stubLiveness) Sessions(ctx context.Context) ([]string, error) { return nil, nil }

// writeAgentsJSON writes agentsJSON to <root>/.agentfactory/agents.json, creating the
// .agentfactory dir first (os.WriteFile does not create parents). The config package's mustWrite
// is not importable from package server, so the server test package has its own local helper.
func writeAgentsJSON(t *testing.T, root, agentsJSON string) {
	t.Helper()
	dir := filepath.Join(root, ".agentfactory") // matches config.dotDir
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "agents.json"), []byte(agentsJSON), 0o644); err != nil {
		t.Fatal(err)
	}
}

// slingServer builds a server whose sling/form path resolves each agent's DECLARED formula from a
// temp <root>/.agentfactory/agents.json through the real FormulaResolver seam (config.Service),
// and runs `af formula show`/`af sling` against the REAL exec.Wrapper backed by a fakeRunner — so
// the assertion proves the FINAL argv. agentsJSON maps each agent name to its formula; formulaJSON
// is the verb-keyed `af formula show --json` payload (override per-name via fr.respByName when a
// test needs two agents to return distinct schemas). views seed the read-model ONLY for tests that
// inject a bogus runtime state (AC-2/AC-3) — the form path no longer reads AgentView.Formula (#455).
func slingServer(t *testing.T, agentsJSON, formulaJSON string, views ...readmodel.AgentView) (*Server, *fakeRunner) {
	t.Helper()
	root := t.TempDir()
	writeAgentsJSON(t, root, agentsJSON)
	fr := &fakeRunner{resp: map[string]exec.Result{"formula": {Stdout: formulaJSON}}}
	w := exec.NewWrapper(fr, "")
	cfg := config.New(root, w) // *config.Service implements server.FormulaResolver via AgentFormula
	s := New(w, fakeAssembler{views: views}, nil,
		WithFormReader(formschema.New(w)),
		WithFormulaResolver(cfg))
	return s, fr
}

// serveForm GETs /api/agents/<name>/form, asserts 200, and returns the response body. The form
// route is a GET, so no loopback Origin is required.
func serveForm(t *testing.T, s *Server, name string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/"+name+"/form", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("form %s: code = %d, want 200; body=%s", name, rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

// AC-2/AC-4 — a form submit threads the operator's task to the af-sling POSITIONAL argument (after
// a `--` terminator), never as a --var. minimalworker's only user-providable field IS the task, so
// the emitted argv carries no --var at all: `af sling --agent alpha --reset -- "do the thing"`.
func TestSling_ArgvPerVar(t *testing.T) {
	s, fr := slingServer(t, `{"agents":{"alpha":{"formula":"minimalworker"}}}`, minimalworkerFormulaJSON)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/sling", `{"task":"do the thing","vars":{}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("sling: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	args, ok := fr.argsFor("sling")
	if !ok {
		t.Fatalf("expected the handler to reach the Runner with verb 'sling', got verbs=%v", fr.verbs)
	}
	want := []string{"--agent", "alpha", "--reset", "--", "do the thing"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("sling argv = %v, want %v", args, want)
	}
	// the task is the positional after `--`, NOT a --var.
	if containsArg(args, "--var") {
		t.Errorf("the task must not be emitted as a --var: %v", args)
	}
	if !containsArg(args, "--reset") {
		t.Errorf("argv missing --reset: %v", args)
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != "do the thing" {
		t.Errorf("task must be the single positional after `--`, got tail %v", args[len(args)-2:])
	}
}

// Multiple submitted fields: each non-task var gets its own --var (sorted, never comma-joined), and
// the task is still the positional after `--`: `af sling --agent alpha --reset --var k=v -- "do it"`.
func TestSling_MultipleVarsSortedNotJoined(t *testing.T) {
	s, fr := slingServer(t, `{"agents":{"alpha":{"formula":"multi"}}}`, multiFormulaJSON)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/sling", `{"task":"do it","vars":{"k":"v"}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("sling: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	args, _ := fr.argsFor("sling")
	want := []string{"--agent", "alpha", "--reset", "--var", "k=v", "--", "do it"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("sling argv = %v, want %v (sorted vars, task positional after --)", args, want)
	}
	// the only --var value must not be comma-joined (the StringSliceVar footgun guard).
	for i, a := range args {
		if a == "--var" && i+1 < len(args) && strings.Contains(args[i+1], ",") {
			t.Errorf("comma-joined --var value %q — the StringSliceVar footgun", args[i+1])
		}
	}
}

// INV-2 — the sling handler rejects any VARS key that is not a user-providable field of the agent's
// formula (here an auto-sourced `orchestrator`): 400, and Sling is NEVER invoked. The task is the
// positional and is excluded from this key check.
func TestSling_RejectsUnknownKey(t *testing.T) {
	s, fr := slingServer(t, `{"agents":{"alpha":{"formula":"minimalworker"}}}`, minimalworkerFormulaJSON)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/sling", `{"task":"do it","vars":{"orchestrator":"impersonated"}}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown vars key: code = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := fr.argsFor("sling"); ok {
		t.Fatalf("INV-2 violated: Sling ran with an unknown (auto-sourced) vars key")
	}
}

// AC-4 — the task is the POSITIONAL, not a key-checked field. A task whose text equals an
// auto-sourced (hidden) field name — which as a VARS key would be rejected 400 by INV-2 — must
// still thread through untouched as the positional after `--`. This proves the task is excluded
// from the schema.FieldNames() key check.
func TestSling_TaskNotKeyChecked(t *testing.T) {
	s, fr := slingServer(t, `{"agents":{"alpha":{"formula":"minimalworker"}}}`, minimalworkerFormulaJSON)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/alpha/sling", `{"task":"orchestrator","vars":{}}`))
	if rec.Code != http.StatusOK {
		t.Fatalf("task must not be key-checked: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	args, ok := fr.argsFor("sling")
	if !ok {
		t.Fatalf("Sling should have run (task is not key-checked)")
	}
	if args[len(args)-2] != "--" || args[len(args)-1] != "orchestrator" {
		t.Fatalf("task must thread to the positional after `--`, got %v", args)
	}
}

// The form handler resolves the agent's formula and returns the user-providable schema; the
// deferred var never leaks into the form.
func TestForm_HandlerReturnsSchema(t *testing.T) {
	s, _ := slingServer(t, `{"agents":{"alpha":{"formula":"minimalworker"}}}`, minimalworkerFormulaJSON)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/alpha/form", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("form: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Name    string `json:"name"`
			Primary string `json:"primary"`
			Fields  []struct {
				Name     string `json:"name"`
				Required bool   `json:"required"`
			} `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &env); err != nil {
		t.Fatalf("form response not JSON: %v", err)
	}
	if !env.OK {
		t.Fatalf("form envelope ok=false")
	}
	// Schema.Primary is serialized in the form response (Phase 3 consumes it). minimalworker's only
	// required input is `task`, so the effective bind target is "task" (input-bridge mechanism).
	if env.Data.Primary != "task" {
		t.Errorf("form response primary = %q, want \"task\" (Schema.Primary must serialize)", env.Data.Primary)
	}
	var hasTask, leakedOrch bool
	for _, f := range env.Data.Fields {
		switch f.Name {
		case "task":
			hasTask = true
			if !f.Required {
				t.Errorf("'task' should be Required in the form schema")
			}
		case "orchestrator":
			leakedOrch = true
		}
	}
	if !hasTask {
		t.Errorf("form schema missing 'task'")
	}
	if leakedOrch {
		t.Errorf("INV-2: form schema leaked 'orchestrator' (source=deferred)")
	}
}

// A sling to an unknown agent (absent from agents.json) is a 404, and Sling never runs.
func TestSling_UnknownAgent_NotFound(t *testing.T) {
	// agents.json OMITS ghost ⇒ Option-B resolver returns found=false ⇒ 404 (no read-model lookup).
	s, fr := slingServer(t, `{"agents":{}}`, minimalworkerFormulaJSON)

	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPOST("/api/agents/ghost/sling", `{"task":"x"}`))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unknown agent: code = %d, want 404; body=%s", rec.Code, rec.Body.String())
	}
	if _, ok := fr.argsFor("sling"); ok {
		t.Fatalf("Sling must not run for an unknown agent")
	}
}

// AC-5 / AC-1 — per-agent resolution. Two agents with DISTINCT declared formulas resolve to two
// distinct, source-discriminated schemas: GET /api/agents/alpha/form runs `af formula show
// minimalworker` and GET /api/agents/beta/form runs `af formula show multi`. The NAME on the argv
// (args[1]) is asserted — a response-only check would be vacuous since fakeRunner.resp is keyed by
// VERB (Gap 4). Source-discriminating seed: each agent's READ-MODEL (running) formula is the OTHER
// agent's declared formula, so reverting the Phase-1 fix (read-model resolution) would resolve the
// WRONG-but-PRESENT name — failing this test for the right reason (wrong source), not empty→422.
func TestForm_PerAgentFormulaResolution(t *testing.T) {
	const agentsJSON = `{"agents":{"alpha":{"formula":"minimalworker"},"beta":{"formula":"multi"}}}`
	s, fr := slingServer(t, agentsJSON, minimalworkerFormulaJSON,
		readmodel.AgentView{Name: "alpha", Formula: "multi", Running: true},        // running ≠ declared (minimalworker)
		readmodel.AgentView{Name: "beta", Formula: "minimalworker", Running: true}) // running ≠ declared (multi)
	// Distinct per-formula payloads so the two forms return DIFFERENT schemas (resp is verb-keyed
	// and cannot distinguish; respByName branches on the resolved NAME at args[1]).
	fr.respByName = map[string]exec.Result{
		"minimalworker": {Stdout: minimalworkerFormulaJSON},
		"multi":         {Stdout: multiFormulaJSON},
	}

	bodyAlpha := serveForm(t, s, "alpha")
	bodyBeta := serveForm(t, s, "beta")

	// NAME assertion: each agent's DECLARED formula reached the `af formula show` argv, in order.
	names := fr.formulaNames()
	want := []string{"minimalworker", "multi"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("resolved formula names = %v, want %v (per-agent DECLARED resolution, not the running formula)", names, want)
	}
	// The two schemas are source-discriminated: multi exposes the extra cli var `k`; minimalworker
	// does not. (Belt-and-suspenders over the byte inequality, in case both names ever collide.)
	if bodyAlpha == bodyBeta {
		t.Fatalf("alpha and beta forms must differ (distinct formulas); both =\n%s", bodyAlpha)
	}
	if !strings.Contains(bodyBeta, `"k"`) {
		t.Errorf("beta (multi) form should expose field k: %s", bodyBeta)
	}
	if strings.Contains(bodyAlpha, `"k"`) {
		t.Errorf("alpha (minimalworker) form must NOT expose field k: %s", bodyAlpha)
	}
}

// AC-5 / AC-2 (+ AC-4) — the form ignores the read-model's most-recent RUNNING formula. agents.json
// declares alpha→minimalworker; the read-model carries a BOGUS unrelated running value
// (test-dispatch). The form must resolve the DECLARED formula: `af formula show minimalworker`, never
// `af formula show test-dispatch`. This is the source-discriminating RED-check anchor — reverting the
// Phase-1 fix makes args[1] become "test-dispatch" and this test goes red for the right reason. The
// same test re-asserts AC-4 (INV-2): the deferred var `orchestrator` never leaks into the schema.
func TestForm_IgnoresRuntimeFormula(t *testing.T) {
	const agentsJSON = `{"agents":{"alpha":{"formula":"minimalworker"}}}`
	s, fr := slingServer(t, agentsJSON, minimalworkerFormulaJSON,
		readmodel.AgentView{Name: "alpha", Formula: "test-dispatch", Running: true}) // bogus most-recent value

	body := serveForm(t, s, "alpha")

	// NAME on the argv carries the DECLARED formula; the bogus runtime value never reaches it.
	args, ok := fr.argsFor("formula")
	if !ok {
		t.Fatalf("expected an `af formula show` call; verbs=%v", fr.verbs)
	}
	if len(args) < 2 || args[1] != "minimalworker" {
		t.Fatalf("resolved formula argv = %v, want args[1]==\"minimalworker\" (the bogus test-dispatch must be ignored)", args)
	}
	if containsArg(args, "test-dispatch") {
		t.Fatalf("INV: the bogus runtime formula test-dispatch reached the formula argv: %v", args)
	}

	// AC-4 (INV-2): the resolved schema exposes `task` and HIDES the deferred var `orchestrator`.
	var env struct {
		OK   bool `json:"ok"`
		Data struct {
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &env); err != nil {
		t.Fatalf("form response not JSON: %v", err)
	}
	if !env.OK {
		t.Fatalf("form envelope ok=false: %s", body)
	}
	var hasTask, leakedOrch bool
	for _, f := range env.Data.Fields {
		switch f.Name {
		case "task":
			hasTask = true
		case "orchestrator":
			leakedOrch = true
		}
	}
	if !hasTask {
		t.Errorf("resolved schema missing 'task': %s", body)
	}
	if leakedOrch {
		t.Errorf("AC-4/INV-2: deferred var 'orchestrator' leaked into the schema: %s", body)
	}
}

// AC-5 / AC-3 — runtime-state immunity. The SAME declared formula resolved under two DIFFERENT
// read-model runtime states yields a BYTE-IDENTICAL form body (the form is a pure function of the
// declared formula, never of the running one).
func TestForm_ByteIdenticalAcrossRuntimeStates(t *testing.T) {
	const agentsJSON = `{"agents":{"alpha":{"formula":"minimalworker"}}}`
	s1, _ := slingServer(t, agentsJSON, minimalworkerFormulaJSON,
		readmodel.AgentView{Name: "alpha", Formula: "test-dispatch", Running: true})
	s2, _ := slingServer(t, agentsJSON, minimalworkerFormulaJSON,
		readmodel.AgentView{Name: "alpha", Formula: "somethingelse", Running: false})

	if b1, b2 := serveForm(t, s1, "alpha"), serveForm(t, s2, "alpha"); b1 != b2 {
		t.Fatalf("form body must be byte-identical across runtime states:\n s1=%s\n s2=%s", b1, b2)
	}
}

func containsArg(args []string, want string) bool {
	for _, a := range args {
		if a == want {
			return true
		}
	}
	return false
}

// dispatchServer builds a server whose /api/dispatch route runs against the REAL dispatch.Reader
// backed by a fakeRunner returning the given `af dispatch status --json` payload — so the assertion
// proves the FINAL argv and that the view reflects the contract.
func dispatchServer(statusJSON string) (*Server, *fakeRunner) {
	fr := &fakeRunner{resp: map[string]exec.Result{"dispatch": {Stdout: statusJSON}}}
	w := exec.NewWrapper(fr, "")
	s := New(&fakeMutator{}, fakeAssembler{}, nil, WithDispatchReader(dispatch.New(w)))
	return s, fr
}

// AC-1 — GET /api/dispatch reflects `af dispatch status --json`: the table shows every entry and
// the dispatcher-running flag, and updates on a re-fetch.
func TestDispatchView_MatchesJSON(t *testing.T) {
	const payload1 = `{"dispatcher_running":true,"entries":[` +
		`{"issue":"o/r#407","agent":"soldesign-plan","agent_running":true,"item_url":"https://x/407","source":"issue","dispatched_at":"2026-06-20T00:00:00Z"},` +
		`{"issue":"o/r#392","agent":"rootcause","agent_running":false,"item_url":"https://x/392","source":"issue","dispatched_at":"2026-06-20T01:00:00Z"}` +
		`]}`
	s, fr := dispatchServer(payload1)

	type resp struct {
		OK   bool          `json:"ok"`
		Data dispatch.View `json:"data"`
	}
	get := func() resp {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/dispatch", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET /api/dispatch: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
		}
		var out resp
		if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("dispatch response not JSON: %v", err)
		}
		return out
	}

	out := get()
	if !out.OK || !out.Data.DispatcherRunning {
		t.Fatalf("expected ok + dispatcher_running true, got %+v", out)
	}
	if len(out.Data.Entries) != 2 {
		t.Fatalf("entries = %d, want 2", len(out.Data.Entries))
	}
	e0 := out.Data.Entries[0]
	if e0.Issue != "o/r#407" || e0.Source != "issue" || e0.Agent != "soldesign-plan" || !e0.AgentRunning {
		t.Fatalf("entry[0] mismatch: %+v", e0)
	}
	if out.Data.Entries[1].AgentRunning {
		t.Fatalf("entry[1] agent_running should be false")
	}

	// The handler reached the seam with EXACTLY `af dispatch status --json`.
	args, ok := fr.argsFor("dispatch")
	if !ok {
		t.Fatalf("handler did not reach the Runner with verb 'dispatch'; verbs=%v", fr.verbs)
	}
	if want := []string{"status", "--json"}; !reflect.DeepEqual(args, want) {
		t.Fatalf("dispatch argv = %v, want %v", args, want)
	}

	// Updates on re-fetch: a different upstream payload yields a different view.
	fr.mu.Lock()
	fr.resp["dispatch"] = exec.Result{Stdout: `{"dispatcher_running":false,"entries":[]}`}
	fr.mu.Unlock()
	out2 := get()
	if out2.Data.DispatcherRunning {
		t.Fatalf("re-fetch should reflect dispatcher_running=false")
	}
	if len(out2.Data.Entries) != 0 {
		t.Fatalf("re-fetch entries = %d, want 0", len(out2.Data.Entries))
	}
}

// The dispatch read failing upstream (the {"state":"error"} envelope) surfaces as a 502 error envelope.
func TestDispatchView_ErrorEnvelope(t *testing.T) {
	s, _ := dispatchServer(`{"state":"error","error":"state file unreadable"}`)
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/dispatch", nil))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("error envelope: code = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}
}

// GET /api/settings returns the curated read; PUT /api/settings/{file} routes the RAW body to the
// settings service; factory.json is read-only (no write path).
func TestSettings_HandlerRoutes(t *testing.T) {
	fs := &fakeSettings{view: config.Settings{
		Dispatch: config.Dispatch{Repos: []string{"o/r"}, TriggerLabel: "go", Mappings: []config.DispatchMapping{{Labels: []string{"bug"}, Agent: "rootcause"}}},
		Startup:  config.Startup{Quality: "default", Fidelity: "default"},
		Factory:  config.Factory{Type: "factory", Name: "demo", Version: 1},
		Agents:   []config.AgentSummary{{Name: "rootcause", Type: "specialist"}},
	}}
	s := New(&fakeMutator{}, fakeAssembler{}, nil, WithSettings(fs))

	// GET read
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /api/settings: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "auth_token") || strings.Contains(rec.Body.String(), "base_url") {
		t.Fatalf("settings response must never contain a secret field: %s", rec.Body.String())
	}

	// PUT dispatch — the RAW body is handed straight to Write (no in-UI typed decode).
	body := `{"repos":["o/r"],"trigger_label":"go","mappings":[{"labels":["bug"],"agent":"rootcause"}]}`
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPUT("/api/settings/dispatch", body))
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT /api/settings/dispatch: code = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if fs.lastFile != "dispatch" {
		t.Fatalf("Write file = %q, want dispatch", fs.lastFile)
	}
	if string(fs.lastBytes) != body {
		t.Fatalf("Write payload = %q, want the raw body %q", fs.lastBytes, body)
	}

	// af RAN and rejected the config (non-zero child exit) → 422 with the friendly message surfaced.
	fs.writeErr = errors.New(`dispatch mapping references unknown agent "ghost"`)
	fs.writeRes = exec.Result{ExitCode: 1}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPUT("/api/settings/dispatch", body))
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("rejected write: code = %d, want 422; body=%s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "unknown agent") {
		t.Fatalf("friendly validation message not surfaced: %s", rec.Body.String())
	}

	// af could not RUN at all (zero exit code, error set) → 502 (infrastructure failure, not validation).
	fs.writeErr = errors.New(`af config: exec: "af": executable file not found in $PATH`)
	fs.writeRes = exec.Result{}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, loopbackPUT("/api/settings/dispatch", body))
	if rec.Code != http.StatusBadGateway {
		t.Fatalf("infra failure: code = %d, want 502; body=%s", rec.Code, rec.Body.String())
	}

	// factory.json is read-only: ErrNotWritable → 400, and Write was not asked to persist it.
	fs.writeErr = nil
	fs2 := &fakeSettings{writeErr: config.ErrNotWritable}
	s2 := New(&fakeMutator{}, fakeAssembler{}, nil, WithSettings(fs2))
	rec = httptest.NewRecorder()
	s2.Handler().ServeHTTP(rec, loopbackPUT("/api/settings/factory", `{}`))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT factory: code = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
}

// The new routes 500 cleanly when their dependency is not wired (nil-guard parity with /form).
func TestDispatchAndSettings_NilDeps500(t *testing.T) {
	s := New(&fakeMutator{}, fakeAssembler{}, nil) // no dispatch reader, no settings service
	for _, path := range []string{"/api/dispatch", "/api/settings"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != http.StatusInternalServerError {
			t.Fatalf("GET %s with nil dep: code = %d, want 500", path, rec.Code)
		}
	}
}
