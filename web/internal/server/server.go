// Package server is the C1 loopback HTTP server for the web module.
//
// It binds 127.0.0.1:0 (ephemeral, stdlib net/http + Go 1.22 ServeMux), answers a uniform
// {ok, message, data} JSON envelope on every response, and exposes ONLY the Phase-1
// allowlisted verbs through the exec wrapper — there is no generic command passthrough (C-6).
//
// Two security gates on every state-changing request:
//   - CSRF: an Origin/Host allowlist (loopback only) is enforced on every POST/PUT.
//   - Auth: a session token (crypto/rand mint, crypto/subtle constant-time verify) is
//     MANDATORY whenever the effective bind is not pure loopback (design-doc CR-1, which
//     overrides security.md Decision 1's loopback-no-auth recommendation). On pure loopback
//     the token is optional but the Origin check still applies on POST.
//
// Destructive ops (--reset) require confirm:true in the request body, refused otherwise — a
// server-side guard, not merely a client affordance (Security Decision 5).
package server

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/stempeck/agentfactory-web/internal/config"
	"github.com/stempeck/agentfactory-web/internal/dispatch"
	"github.com/stempeck/agentfactory-web/internal/exec"
	"github.com/stempeck/agentfactory-web/internal/feedback"
	"github.com/stempeck/agentfactory-web/internal/formschema"
	"github.com/stempeck/agentfactory-web/internal/proto"
	"github.com/stempeck/agentfactory-web/internal/readmodel"
)

// Mutator is the mutating surface (the exec wrapper). exec.Wrapper satisfies it.
type Mutator interface {
	Up(ctx context.Context) (exec.Result, error)
	DownFactory(ctx context.Context, reset bool) (exec.Result, error)
	DownAgent(ctx context.Context, name string, reset bool) (exec.Result, error)
	// Sling carries the operator's task as the af-sling positional argument (after a `--`
	// terminator) plus the remaining user-providable fields as vars (#440 K1).
	Sling(ctx context.Context, name, task string, vars map[string]string) (exec.Result, error)
}

// Assembler is the read surface (the read-model). readmodel.ReadModel satisfies it.
type Assembler interface {
	Assemble(ctx context.Context) ([]readmodel.AgentView, error)
}

// FormReader yields the user-providable form schema for a formula by name (C4).
// formschema.Reader satisfies it.
type FormReader interface {
	Read(ctx context.Context, formula string) (formschema.Schema, error)
}

// DispatchReader yields the read-only dispatch status view (C3). dispatch.Reader satisfies it.
type DispatchReader interface {
	Status(ctx context.Context) (dispatch.View, error)
}

// SettingsService is the curated config read + the af-routed write (C5). config.Service satisfies
// it: Read returns the secret-stripped settings, Write routes the edited config through
// `af config <file> set` (atomic + cross-file validated inside af-core).
type SettingsService interface {
	Read(ctx context.Context) (config.Settings, error)
	Write(ctx context.Context, file string, payload []byte) (exec.Result, error)
}

// FormulaResolver answers "what formula is agent <name> configured to run" from static
// config (agents.json) ONLY — never from runtime/instance/bead state. config.Service
// satisfies it via AgentFormula. The /form and /sling routes resolve through it (#455),
// so the Sling form is built from the DECLARED formula, not the RUNNING one.
type FormulaResolver interface {
	AgentFormula(ctx context.Context, name string) (formula string, found bool, err error)
}

// Prototypes is the C7 static/prototype server: it enumerates the servable prototype dirs and
// serves their on-disk assets, traversal-contained. proto.Server satisfies it.
type Prototypes interface {
	List() []proto.Prototype
	http.Handler // ServeHTTP serves GET /proto/{id}/{asset...}
}

// Feedback is the C6 gate-aware feedback writer (H-5). It writes feedback-form.md ONLY when the
// owning agent is verified parked at the matching design-feedback-{N} gate. feedback.Writer
// satisfies it. OpenFrom is the pure (no-exec) predicate used to annotate GET /api/prototypes from a
// single already-fetched view set.
type Feedback interface {
	Submit(ctx context.Context, id string, in feedback.Input) (feedback.Result, error)
	OpenFrom(views []readmodel.AgentView, id string) bool
}

// Envelope is the uniform response shape on every endpoint.
type Envelope struct {
	OK      bool        `json:"ok"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
}

// Server holds the routing + security configuration.
type Server struct {
	mut      Mutator
	reader   Assembler
	form     FormReader      // C4 form-schema reader (nil ⇒ the /form and /sling routes 500)
	formula  FormulaResolver // #455 static-config (agents.json) formula resolver (nil ⇒ /form and /sling routes 500)
	dispatch DispatchReader  // C3 dispatch reader (nil ⇒ GET /api/dispatch 500)
	settings SettingsService // C5 settings read/write (nil ⇒ /api/settings routes 500)
	proto    Prototypes      // C7 prototype server (nil ⇒ the /api/prototypes and /proto routes are unregistered)
	feedback Feedback        // C6 feedback writer (nil ⇒ the feedback route is unregistered)
	static   http.Handler

	root string // K5: the resolved factory root this console serves; surfaced via GET /healthz (Gap 7)

	bindAddr string
	loopback bool   // true ⇒ token optional (Origin still enforced on POST)
	token    string // session token; required when !loopback

	mux *http.ServeMux
}

// Option configures a Server.
type Option func(*Server)

// WithBind sets the bind address and recomputes the loopback flag from its host.
func WithBind(addr string) Option {
	return func(s *Server) {
		s.bindAddr = addr
		s.loopback = hostIsLoopback(addr)
	}
}

// WithToken pins the session token (tests). Production mints one with crypto/rand.
func WithToken(tok string) Option {
	return func(s *Server) { s.token = tok }
}

// WithFormReader wires the C4 form-schema reader used by the /form and /sling routes.
func WithFormReader(fr FormReader) Option {
	return func(s *Server) { s.form = fr }
}

// WithDispatchReader wires the C3 dispatch reader used by GET /api/dispatch.
func WithDispatchReader(dr DispatchReader) Option {
	return func(s *Server) { s.dispatch = dr }
}

// WithSettings wires the C5 settings service used by GET /api/settings and PUT /api/settings/{file}.
func WithSettings(ss SettingsService) Option {
	return func(s *Server) { s.settings = ss }
}

// WithFormulaResolver wires the static-config (agents.json) formula resolver used by the
// /form and /sling routes (nil ⇒ those routes 500 — never a read-model fallback). #455.
func WithFormulaResolver(fr FormulaResolver) Option {
	return func(s *Server) { s.formula = fr }
}

// WithPrototypes wires the C7 prototype server used by GET /api/prototypes and GET /proto/{id}/...
func WithPrototypes(p Prototypes) Option {
	return func(s *Server) { s.proto = p }
}

// WithFeedback wires the C6 gate-aware feedback writer used by POST /api/prototypes/{id}/feedback.
func WithFeedback(f Feedback) Option {
	return func(s *Server) { s.feedback = f }
}

// WithRoot records the resolved factory root this console serves. It is surfaced via GET /healthz
// (K5 / Gap 7) so an operator — or an automated probe — can see WHICH factory the console resolved
// to, making a wrong-but-valid root visible rather than silent.
func WithRoot(root string) Option {
	return func(s *Server) { s.root = root }
}

// New builds a Server. mut/reader are the exec wrapper + read-model (or fakes in tests);
// static is the embedded Floor view handler (may be nil in API-only tests).
func New(mut Mutator, reader Assembler, static http.Handler, opts ...Option) *Server {
	s := &Server{
		mut:      mut,
		reader:   reader,
		static:   static,
		bindAddr: "127.0.0.1:0",
		loopback: true,
		token:    mintToken(),
	}
	for _, o := range opts {
		o(s)
	}
	s.routes()
	return s
}

// Token returns the session token (printed at startup so an operator can authenticate when
// the surface is reached over a non-loopback forward).
func (s *Server) Token() string { return s.token }

// Handler exposes the mux for httptest-based tests.
func (s *Server) Handler() http.Handler { return s.mux }

func (s *Server) routes() {
	s.mux = http.NewServeMux()
	s.mux.HandleFunc("GET /healthz", s.handleHealthz)
	s.mux.HandleFunc("GET /api/agents", s.handleAgents)
	s.mux.HandleFunc("GET /api/agents/{name}/form", s.handleAgentForm)
	s.mux.HandleFunc("POST /api/factory/up", s.handleFactoryUp)
	s.mux.HandleFunc("POST /api/factory/down", s.handleFactoryDown)
	s.mux.HandleFunc("POST /api/agents/{name}/down", s.handleAgentDown)
	s.mux.HandleFunc("POST /api/agents/{name}/sling", s.handleAgentSling)
	s.mux.HandleFunc("GET /api/dispatch", s.handleDispatch)
	s.mux.HandleFunc("GET /api/settings", s.handleSettings)
	s.mux.HandleFunc("PUT /api/settings/{file}", s.handleSettingsWrite)
	if s.proto != nil {
		// C7: enumeration (read) + traversal-contained on-disk static serving. The static subtree
		// is mounted under StripPrefix so the proto handler receives "{id}/{asset}".
		s.mux.HandleFunc("GET /api/prototypes", s.handlePrototypes)
		s.mux.Handle("GET /proto/", http.StripPrefix("/proto/", s.proto))
	}
	if s.feedback != nil {
		// C6: state-changing, gate-verified feedback write (H-5).
		s.mux.HandleFunc("POST /api/prototypes/{id}/feedback", s.handleFeedback)
	}
	if s.static != nil {
		s.mux.Handle("/", s.static)
	}
}

// Listen binds the configured loopback address (127.0.0.1:0 by default), starts serving in a
// goroutine, and returns the listener so callers can read the ephemeral Addr().
func (s *Server) Listen() (net.Listener, error) {
	ln, err := net.Listen("tcp", s.bindAddr)
	if err != nil {
		return nil, err
	}
	srv := &http.Server{Handler: s.mux}
	go func() { _ = srv.Serve(ln) }()
	return ln, nil
}

// ---- security gates ----

// guard enforces auth (always when !loopback) and, for state-changing requests, the CSRF
// Origin allowlist. It writes the rejection envelope and returns false when the request is denied.
func (s *Server) guard(w http.ResponseWriter, r *http.Request, stateChanging bool) bool {
	if !s.authOK(r) {
		s.write(w, http.StatusUnauthorized, Envelope{OK: false, Message: "authentication required"})
		return false
	}
	if stateChanging && !originOK(r) {
		s.write(w, http.StatusForbidden, Envelope{OK: false, Message: "forbidden origin"})
		return false
	}
	return true
}

// authOK is satisfied automatically on pure loopback (token optional). When the effective bind
// is not loopback, a valid session token is mandatory (verified in constant time).
func (s *Server) authOK(r *http.Request) bool {
	if s.loopback {
		return true
	}
	tok := bearerToken(r)
	if tok == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(tok), []byte(s.token)) == 1
}

// originOK is the loopback CSRF allowlist: the request's Origin (or, if absent, its Host) must
// resolve to a loopback hostname. A cross-site attacker's Origin (their own domain) is rejected.
func originOK(r *http.Request) bool {
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		if err != nil {
			return false
		}
		return hostnameIsLoopback(u.Hostname())
	}
	// No Origin header (e.g. a non-browser client): fall back to the Host header.
	return hostIsLoopback(r.Host)
}

func bearerToken(r *http.Request) string {
	if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
	}
	return r.Header.Get("X-AF-Token")
}

func hostIsLoopback(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	return hostnameIsLoopback(host)
}

func hostnameIsLoopback(host string) bool {
	switch host {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// ---- handlers ----

// handleHealthz is the unauthenticated loopback liveness probe used by the web module's rendezvous
// (web/internal/rendezvous) to decide whether an already-running server may be reused instead of
// starting a duplicate. It is deliberately NOT behind guard: it reveals nothing sensitive and must
// answer even when the bind is non-loopback so the loopback probe never blocks on auth — mirroring
// mcpstore's unauthenticated /health (lifecycle.go:268-277).
//
// K5/Gap 7: it also carries the resolved factory root under data so an operator (or an automated
// check) can see WHICH factory the console resolved to. The root sits under Data (json:"data,omitempty")
// so the bare {ok:true} liveness contract is preserved — rendezvous.healthCheck checks ONLY the 200
// status, never the body, so surfacing the root cannot break the liveness probe.
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	s.write(w, http.StatusOK, Envelope{OK: true, Data: map[string]string{"root": s.root}})
}

func (s *Server) handleAgents(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, false) {
		return
	}
	views, err := s.reader.Assemble(r.Context())
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	s.write(w, http.StatusOK, Envelope{OK: true, Data: views})
}

// handleDispatch (read) returns the dispatcher's running state and the dispatched issues/PRs from
// `af dispatch status --json` (AC-4). af-core already computes dispatcher + per-agent liveness, so
// the view surfaces those directly. A read failure (e.g. the {"state":"error"} envelope) is a 502.
func (s *Server) handleDispatch(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, false) {
		return
	}
	if s.dispatch == nil {
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "dispatch reader not configured"})
		return
	}
	view, err := s.dispatch.Status(r.Context())
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	s.write(w, http.StatusOK, Envelope{OK: true, Data: view})
}

// handleSettings (read) returns the curated settings: editable dispatch.json/startup.json, the
// read-only factory.json, and the secret-free agent roster (AC-6). Per-agent secrets
// (Model/BaseURL/AuthToken) are stripped by construction in the config package, never here.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, false) {
		return
	}
	if s.settings == nil {
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "settings service not configured"})
		return
	}
	view, err := s.settings.Read(r.Context())
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	s.write(w, http.StatusOK, Envelope{OK: true, Data: view})
}

// handleSettingsWrite (state-changing) persists an edited config file. {file} ∈ {dispatch,startup}
// (factory.json is read-only). The raw request body IS the complete edited config document — it is
// fed straight to `af config <file> set` on stdin (H-P1: the web module never decodes it into a
// typed struct nor re-implements validation). af-core validates (struct + cross-file) and writes
// atomically; on a non-zero exit its friendly per-field message is surfaced as the validation error.
func (s *Server) handleSettingsWrite(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, true) {
		return
	}
	if s.settings == nil {
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "settings service not configured"})
		return
	}
	file := r.PathValue("file")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: "could not read request body"})
		return
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: "empty settings body: send the complete edited config as JSON"})
		return
	}
	res, err := s.settings.Write(r.Context(), file, body)
	switch {
	case err == nil:
		s.write(w, http.StatusOK, Envelope{OK: true, Message: "settings saved", Data: map[string]int{"exit_code": res.ExitCode}})
	case errors.Is(err, config.ErrNotWritable):
		// A read-only / unknown file is a client error.
		s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: err.Error()})
	case res.ExitCode != 0:
		// af ran and rejected the config (struct/cross-file validation): a non-zero child exit. The
		// friendly per-field message af printed to stderr is embedded in err. 422 = did not validate.
		s.write(w, http.StatusUnprocessableEntity, Envelope{OK: false, Message: err.Error()})
	default:
		// The af command could not run at all (e.g. not on PATH) — an upstream/infrastructure
		// failure, not a validation failure. Still surface the message so the operator isn't blind.
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
	}
}

func (s *Server) handleFactoryUp(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, true) {
		return
	}
	res, err := s.mut.Up(r.Context())
	s.writeMutation(w, "factory started", res, err)
}

func (s *Server) handleFactoryDown(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, true) {
		return
	}
	body := decodeDownBody(r)
	if body.Reset && !body.Confirm {
		s.write(w, http.StatusUnprocessableEntity, Envelope{OK: false, Message: "reset requires confirm:true"})
		return
	}
	res, err := s.mut.DownFactory(r.Context(), body.Reset)
	s.writeMutation(w, "factory shutting down", res, err)
}

func (s *Server) handleAgentDown(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, true) {
		return
	}
	name := r.PathValue("name")
	body := decodeDownBody(r)
	if body.Reset && !body.Confirm {
		s.write(w, http.StatusUnprocessableEntity, Envelope{OK: false, Message: "reset requires confirm:true"})
		return
	}
	res, err := s.mut.DownAgent(r.Context(), name, body.Reset)
	s.writeMutation(w, "agent stopping", res, err)
}

type downBody struct {
	Reset   bool `json:"reset"`
	Confirm bool `json:"confirm"`
}

func decodeDownBody(r *http.Request) downBody {
	var b downBody
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&b) // empty/invalid body ⇒ zero value (no reset)
	}
	return b
}

// handleAgentForm (read) returns the user-providable form schema for an idle agent's configured
// formula. It resolves {name} → formula via the agent's DECLARED agents.json config (#455), then
// the C4 reader filters out the auto-sourced (identity-bearing) vars (INV-2) and orders required-first.
func (s *Server) handleAgentForm(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, false) {
		return
	}
	name := r.PathValue("name")
	formula, ok := s.resolveFormula(r.Context(), w, name)
	if !ok {
		return
	}
	schema, err := s.form.Read(r.Context(), formula)
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	s.write(w, http.StatusOK, Envelope{OK: true, Data: schema})
}

// handleAgentSling (state-changing) dispatches a task to an agent, emitting the byte-identical
// `af sling --agent <name> --reset --var k=v … -- <task>` argv (one --var per field, the task as
// the positional after a `--` terminator) through C2. The body is the structured {task, vars}
// shape: `task` is the operator's primary text (value-validated in the wrapper, never key-checked),
// and every `vars` key is validated against the formula's user-providable field set first — an
// unknown vars key (e.g. an auto-sourced var) is refused with 400 and Sling is never invoked (INV-2).
func (s *Server) handleAgentSling(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, true) {
		return
	}
	name := r.PathValue("name")
	task, vars, ok := decodeSlingBody(r)
	if !ok {
		s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: `invalid sling body: expected {"task":"…","vars":{field:value}} with string vars values`})
		return
	}
	formula, ok := s.resolveFormula(r.Context(), w, name)
	if !ok {
		return
	}
	schema, err := s.form.Read(r.Context(), formula)
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	// INV-2: reject any VARS key that is not a user-providable field of this formula. Written as a
	// direct 400 (writeMutation's isValidationErr would not recognise this message and would
	// mis-map it to 502). The task is the positional argument — value-validated by validateTask in
	// the wrapper, NEVER key-checked here (it does not appear in schema.FieldNames()).
	allowed := schema.FieldNames()
	for k := range vars {
		if !allowed[k] {
			s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: "field " + k + " is not a user-providable variable of formula " + formula})
			return
		}
	}
	res, err := s.mut.Sling(r.Context(), name, task, vars)
	// Honest success copy: sling is fire-and-forget — the agent appears on the Floor shortly.
	s.writeMutation(w, "sling dispatched — the agent will appear on the Floor shortly", res, err)
}

// resolveFormula finds the agent's DECLARED formula from static config (agents.json) via the
// FormulaResolver seam (#455). It must NEVER read the live-status read-model (s.reader): the
// read-model's AgentView.Formula is the RUNNING (bead-derived) formula, so resolving the Sling
// form from it leaked an unrelated agent's most-recent formula. It writes the appropriate error
// envelope (500 if no form reader / no resolver is configured, 502 on a read failure, 404 if the
// agent is unknown, 422 if it has no configured formula) and returns ok=false in those cases.
func (s *Server) resolveFormula(ctx context.Context, w http.ResponseWriter, name string) (string, bool) {
	if s.form == nil { // keep — unchanged
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "form reader not configured"})
		return "", false
	}
	if s.formula == nil { // #455: parity with handleSettings; MUST NOT fall back to the read-model (re-introduces the bug)
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "formula resolver not configured"})
		return "", false
	}
	formula, found, err := s.formula.AgentFormula(ctx, name) // declared agents.json formula; replaces the former read-model lookup + .Formula loop
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()}) // 502 — dynamic, same shape as before
		return "", false
	}
	if !found {
		s.write(w, http.StatusNotFound, Envelope{OK: false, Message: "agent " + name + " not found"}) // 404
		return "", false
	}
	if formula == "" {
		s.write(w, http.StatusUnprocessableEntity, Envelope{OK: false, Message: "agent " + name + " has no configured formula"}) // 422
		return "", false
	}
	return formula, true
}

// slingBody is the structured POST body of a sling request: the operator's positional task plus the
// remaining user-providable fields as vars. `task` binds to the formula's effective field
// (Schema.Primary) as the af-sling positional; each `vars` entry travels as one --var (#440 K1).
type slingBody struct {
	Task string            `json:"task"`
	Vars map[string]string `json:"vars"`
}

// decodeSlingBody decodes the {"task":"…","vars":{…}} JSON body of a sling request. A missing/empty
// body decodes to an empty task and no vars. A malformed body (not an object, or a vars value that
// is not a string) returns ok=false so the handler can answer 400.
func decodeSlingBody(r *http.Request) (task string, vars map[string]string, ok bool) {
	if r.Body == nil {
		return "", map[string]string{}, true
	}
	var body slingBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		if errors.Is(err, io.EOF) { // empty body ⇒ empty task, no vars
			return "", map[string]string{}, true
		}
		return "", nil, false
	}
	if body.Vars == nil { // {"task":"…"} with no vars key ⇒ empty map, not nil
		body.Vars = map[string]string{}
	}
	return body.Task, body.Vars, true
}

// handlePrototypes (read) returns the enumerated, servable prototype dirs (AC-5). Each entry is
// annotated with feedback_open — whether the owning agent is verified parked at the matching gate —
// so the UI can disable the feedback panel honestly when feedback is not currently open. Enumeration
// is graceful: no .designs/ yet ⇒ an empty list, never a 502.
func (s *Server) handlePrototypes(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, false) {
		return
	}
	if s.proto == nil {
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "prototype server not configured"})
		return
	}
	list := s.proto.List()
	if s.feedback != nil {
		// One gate-state read, reused across every prototype (no N+1 `af agents list --json`). A read
		// failure degrades honestly to "not open" for all rather than failing the whole enumeration.
		views, _ := s.reader.Assemble(r.Context())
		for i := range list {
			list[i].FeedbackOpen = s.feedback.OpenFrom(views, list[i].ID)
		}
	}
	s.write(w, http.StatusOK, Envelope{OK: true, Data: list})
}

// handleFeedback (state-changing) writes the prototype's feedback-form.md — but ONLY when the owning
// agent is verified parked at the matching design-feedback-{N} gate (C6 / H-5). An off-gate (or
// no-such-agent) submission returns ok:false with the honest "feedback not currently open" message
// and writes nothing; a transport failure reading gate state is a 502. The form is the SOLE
// AUTHORITY that releases the gate (data.md Decision 3) — there is no alternate feedback channel.
func (s *Server) handleFeedback(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, true) {
		return
	}
	if s.feedback == nil {
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "feedback writer not configured"})
		return
	}
	id := r.PathValue("id")
	in, ok := decodeFeedback(r)
	if !ok {
		s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: "invalid feedback body: expected {decision, checks, notes}"})
		return
	}
	res, err := s.feedback.Submit(r.Context(), id, in)
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	// An off-gate refusal / empty submission is an honest VALUE (not an error): ok:false + message,
	// HTTP 200 — mirroring how the read-model surfaces honest states rather than transport failures.
	s.write(w, http.StatusOK, Envelope{OK: res.OK, Message: res.Message})
}

// decodeFeedback parses the feedback side-panel body {decision, checks, notes}. A missing/empty body
// decodes to a zero Input (which Submit treats as empty and refuses). A malformed body ⇒ ok=false.
func decodeFeedback(r *http.Request) (feedback.Input, bool) {
	var b struct {
		Decision string   `json:"decision"`
		Checks   []string `json:"checks"`
		Notes    string   `json:"notes"`
	}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil && !errors.Is(err, io.EOF) {
			return feedback.Input{}, false
		}
	}
	return feedback.Input{Decision: b.Decision, Checks: b.Checks, Notes: b.Notes}, true
}

// ---- response helpers ----

func (s *Server) write(w http.ResponseWriter, status int, env Envelope) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(env)
}

// writeMutation maps an exec outcome to the envelope: busy/orchestrated ⇒ 409, validation ⇒
// 400, other exec failure ⇒ 502, success ⇒ 200.
func (s *Server) writeMutation(w http.ResponseWriter, okMsg string, res exec.Result, err error) {
	switch {
	case err == nil:
		s.write(w, http.StatusOK, Envelope{OK: true, Message: okMsg, Data: map[string]int{"exit_code": res.ExitCode}})
	case errors.Is(err, exec.ErrAgentBusy), errors.Is(err, exec.ErrAgentOrchestrated):
		s.write(w, http.StatusConflict, Envelope{OK: false, Message: err.Error()})
	default:
		// Validation failures (bad agent name, bad --var) and exec errors both land here; a bad
		// name is a client error (400), everything else an upstream failure (502).
		if isValidationErr(err) {
			s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: err.Error()})
			return
		}
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
	}
}

func isValidationErr(err error) bool {
	m := err.Error()
	return strings.Contains(m, "agent name") || strings.Contains(m, "--var") || strings.Contains(m, "not allowed")
}

func mintToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand failure is catastrophic; fall back to an unusable token so auth fails closed.
		return ""
	}
	return hex.EncodeToString(b)
}
