// Package server is the loopback HTTP server for the web module.
//
// It binds 127.0.0.1:0 (ephemeral, stdlib net/http + Go 1.22 ServeMux), answers a uniform
// {ok, message, data} JSON envelope on every response, and exposes ONLY the allowlisted verbs
// through the exec wrapper — there is no generic command passthrough.
//
// Two security gates on every state-changing request:
//   - CSRF: an Origin/Host allowlist (loopback only) is enforced on every POST/PUT.
//   - Auth: a session token (crypto/rand mint, crypto/subtle constant-time verify) is
//     MANDATORY whenever the effective bind is not pure loopback — pure loopback matches the
//     established same-machine trust model (any local process running as the same user can
//     connect), but the moment an operator widens the bind beyond loopback that trust model no
//     longer holds, so a token is required. On pure loopback the token is optional but the
//     Origin check still applies on POST.
//
// Destructive ops (--reset) require confirm:true in the request body, refused otherwise — a
// server-side guard, not merely a client affordance.
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
	"strconv"
	"strings"
	"unicode/utf8"

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
	// terminator) plus the remaining user-providable fields as vars (#440).
	Sling(ctx context.Context, name, task string, vars map[string]string) (exec.Result, error)
}

// Assembler is the read surface (the read-model). readmodel.ReadModel satisfies it.
type Assembler interface {
	Assemble(ctx context.Context) ([]readmodel.AgentView, error)
}

// FormReader yields the user-providable form schema for a formula by name.
// formschema.Reader satisfies it.
type FormReader interface {
	Read(ctx context.Context, formula string) (formschema.Schema, error)
}

// DispatchReader yields the read-only dispatch status view. dispatch.Reader satisfies it.
type DispatchReader interface {
	Status(ctx context.Context) (dispatch.View, error)
}

// SettingsService is the curated config read + the af-routed write. config.Service satisfies
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

// Prototypes is the static/prototype server: it enumerates the servable prototype dirs and
// serves their on-disk assets, traversal-contained. proto.Server satisfies it.
type Prototypes interface {
	List() []proto.Prototype
	http.Handler // ServeHTTP serves GET /proto/{id}/{asset...}
}

// Feedback is the gate-aware feedback writer. It writes feedback-form.md ONLY when the
// owning agent is verified parked at the matching design-feedback-{N} gate. feedback.Writer
// satisfies it. OpenFrom is the pure (no-exec) predicate used to annotate GET /api/prototypes from a
// single already-fetched view set.
type Feedback interface {
	Submit(ctx context.Context, id string, in feedback.Input) (feedback.Result, error)
	OpenFrom(views []readmodel.AgentView, id string) bool
}

// Tailer is the honest per-agent session-snapshot reader (#500). readmodel.TmuxCapture
// satisfies it via Tail. It is probe-first and never reports a false live; an absent session is
// an honest zero TailView, not an error. The detail handler passes tail output through WITHOUT
// logging it (sessions print secrets).
type Tailer interface {
	Tail(ctx context.Context, name string, lines int) (readmodel.TailView, error)
}

// MailSender is the operator-mail write surface (#500). exec.Wrapper satisfies it via MailSend,
// which pins the sender to `operator`, fixes the subcommand to `send`, and bypasses the
// dispatched-marker pre-flight (mail's primary recipients ARE dispatched agents).
type MailSender interface {
	MailSend(ctx context.Context, name, subject, body string) (exec.Result, error)
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
	form     FormReader      // form-schema reader (nil ⇒ the /form and /sling routes 500)
	formula  FormulaResolver // #455 static-config (agents.json) formula resolver (nil ⇒ /form and /sling routes 500)
	dispatch DispatchReader  // dispatch reader (nil ⇒ GET /api/dispatch 500)
	settings SettingsService // settings read/write (nil ⇒ /api/settings routes 500)
	proto    Prototypes      // prototype server (nil ⇒ the /api/prototypes and /proto routes are unregistered)
	feedback Feedback        // feedback writer (nil ⇒ the feedback route is unregistered)
	tailer   Tailer          // #500 session-snapshot reader (nil ⇒ the agent-detail route 500s)
	mailer   MailSender      // #500 operator-mail sender (nil ⇒ the agent-mail route 500s)
	static   http.Handler

	root string // the resolved factory root this console serves; surfaced via GET /healthz so a
	// wrong-but-valid root is visible, not silent

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

// WithFormReader wires the form-schema reader used by the /form and /sling routes.
func WithFormReader(fr FormReader) Option {
	return func(s *Server) { s.form = fr }
}

// WithDispatchReader wires the dispatch reader used by GET /api/dispatch.
func WithDispatchReader(dr DispatchReader) Option {
	return func(s *Server) { s.dispatch = dr }
}

// WithSettings wires the settings service used by GET /api/settings and PUT /api/settings/{file}.
func WithSettings(ss SettingsService) Option {
	return func(s *Server) { s.settings = ss }
}

// WithFormulaResolver wires the static-config (agents.json) formula resolver used by the
// /form and /sling routes (nil ⇒ those routes 500 — never a read-model fallback). #455.
func WithFormulaResolver(fr FormulaResolver) Option {
	return func(s *Server) { s.formula = fr }
}

// WithPrototypes wires the prototype server used by GET /api/prototypes and GET /proto/{id}/...
func WithPrototypes(p Prototypes) Option {
	return func(s *Server) { s.proto = p }
}

// WithFeedback wires the gate-aware feedback writer used by POST /api/prototypes/{id}/feedback.
func WithFeedback(f Feedback) Option {
	return func(s *Server) { s.feedback = f }
}

// WithTailer wires the #500 session-snapshot reader used by the agent-detail route.
func WithTailer(t Tailer) Option {
	return func(s *Server) { s.tailer = t }
}

// WithMailer wires the #500 operator-mail sender used by the agent-mail route.
func WithMailer(m MailSender) Option {
	return func(s *Server) { s.mailer = m }
}

// WithRoot records the resolved factory root this console serves. It is surfaced via GET /healthz
// so an operator — or an automated probe — can see WHICH factory the console resolved to, making
// a wrong-but-valid root visible rather than silent.
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
	s.mux.HandleFunc("GET /api/agents/{name}/detail", s.handleAgentDetail)
	s.mux.HandleFunc("POST /api/agents/{name}/mail", s.handleAgentMail)
	s.mux.HandleFunc("GET /api/dispatch", s.handleDispatch)
	s.mux.HandleFunc("GET /api/settings", s.handleSettings)
	s.mux.HandleFunc("PUT /api/settings/{file}", s.handleSettingsWrite)
	if s.proto != nil {
		// Enumeration (read) + traversal-contained on-disk static serving. The static subtree
		// is mounted under StripPrefix so the proto handler receives "{id}/{asset}".
		s.mux.HandleFunc("GET /api/prototypes", s.handlePrototypes)
		s.mux.Handle("GET /proto/", http.StripPrefix("/proto/", s.proto))
	}
	if s.feedback != nil {
		// state-changing, gate-verified feedback write.
		s.mux.HandleFunc("POST /api/prototypes/{id}/feedback", s.handleFeedback)
	}
	if s.static != nil {
		// The CSP lands on the served HTML document ONLY — never on the API JSON responses
		// (write() stays header-clean). The /proto/ mount is deliberately NOT wrapped: prototype
		// HTML is operator-authored local content already neutralised by the iframe `sandbox`
		// attribute — a recorded exclusion, not an oversight.
		s.mux.Handle("/", withDocumentCSP(s.static))
	}
}

// cspPolicy is the restrictive document Content-Security-Policy. The 'unsafe-inline' in
// style-src is LOAD-BEARING: index.html carries inline style="" attributes. The Google-Fonts
// @import in variables.css is intentionally left to fall back to the declared offline stacks
// rather than allowlisting a font CDN, matching variables.css's own offline-fallback comment.
const cspPolicy = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'"

// withDocumentCSP wraps the HTML document handler so the CSP header is set on document responses
// (set before the wrapped handler writes its status, so it sticks).
func withDocumentCSP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", cspPolicy)
		next.ServeHTTP(w, r)
	})
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
// It also carries the resolved factory root under data so an operator (or an automated
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
// `af dispatch status --json`. af-core already computes dispatcher + per-agent liveness, so
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
// read-only factory.json, and the secret-free agent roster. Per-agent secrets
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
// fed straight to `af config <file> set` on stdin: the web module never decodes it into a
// typed struct nor re-implements validation. af-core validates (struct + cross-file) and writes
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
// the form reader filters out the auto-sourced (identity-bearing) vars and orders required-first.
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
// the positional after a `--` terminator) through the exec wrapper. The body is the structured
// {task, vars} shape: `task` is the operator's primary text (value-validated in the wrapper, never
// key-checked), and every `vars` key is validated against the formula's user-providable field set
// first — an unknown vars key (e.g. an auto-sourced var) is refused with 400 and Sling is never invoked.
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
	// Reject any VARS key that is not a user-providable field of this formula. Written as a
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
	if s.form == nil {
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

// DetailView is the per-agent detail payload (#500). Agent embeds readmodel.AgentView VERBATIM
// (twelve snake_case keys) — never a fork (#455). DeclaredFormula is the agents.json formula,
// carried as a SEPARATE, distinctly-labelled field from Agent.Formula (the RUNNING formula). Tail
// is the honest session snapshot.
type DetailView struct {
	Agent           readmodel.AgentView `json:"agent"`
	DeclaredFormula string              `json:"declared_formula"`
	Tail            readmodel.TailView  `json:"tail"`
}

// handleAgentDetail (read) returns the honest per-agent detail projection: the read-model
// AgentView, the DECLARED (agents.json) formula, and a read-only tmux session snapshot. Membership
// is the read-model's decision (unknown ⇒ 404); the FormulaResolver only ANNOTATES declared_formula
// and never 404s (a race-window unresolved formula is carried honestly as ""). The tail output is
// passed through WITHOUT being logged anywhere (sessions print secrets), and the
// response is Cache-Control: no-store, since the snapshot is secret-bearing.
func (s *Server) handleAgentDetail(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, false) {
		return
	}
	if s.tailer == nil {
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "tailer not configured"})
		return
	}
	name := r.PathValue("name")
	views, err := s.reader.Assemble(r.Context())
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	agent, found := findAgentView(views, name)
	if !found {
		s.write(w, http.StatusNotFound, Envelope{OK: false, Message: "agent " + name + " not found"})
		return
	}

	// declared_formula annotates from static config; a nil resolver, an unknown agent, or an empty
	// formula all carry "" honestly (the 404 decision already belongs to read-model membership above).
	declared := ""
	if s.formula != nil {
		if f, ok, ferr := s.formula.AgentFormula(r.Context(), name); ferr == nil && ok {
			declared = f
		}
	}

	lines := clampLines(r.URL.Query().Get("lines"))
	tail, err := s.tailer.Tail(r.Context(), name, lines)
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}

	w.Header().Set("Cache-Control", "no-store")
	s.write(w, http.StatusOK, Envelope{OK: true, Data: DetailView{Agent: agent, DeclaredFormula: declared, Tail: tail}})
}

// handleAgentMail (state-changing) queues operator mail to an agent's mailbox. Flow:
// guard → nil-seam 500 → decode → name shape/membership 404 → content validation (direct 400 with
// the friendly copy) → MailSend (502 on failure) → 200 with the honest async copy. Content
// validation lives HERE (not only in the wrapper) because a fake MailSender never rejects, and
// writeMutation/isValidationErr would mis-map these messages to 502.
func (s *Server) handleAgentMail(w http.ResponseWriter, r *http.Request) {
	if !s.guard(w, r, true) {
		return
	}
	if s.mailer == nil {
		s.write(w, http.StatusInternalServerError, Envelope{OK: false, Message: "mail sender not configured"})
		return
	}
	name := r.PathValue("name")
	subject, body, ok := decodeMailBody(r)
	if !ok {
		s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: `invalid mail body: expected {"subject":"…","body":"…"}`})
		return
	}

	// Name shape + read-model membership both resolve to the same honest 404 (an invalid name can
	// never be a member). Trimming mirrors Wrapper.MailSend's trimAgent so the check agrees with exec.
	trimmed := strings.TrimRight(strings.TrimSpace(name), "/")
	if verr := exec.ValidateAgentName(trimmed); verr != nil {
		s.write(w, http.StatusNotFound, Envelope{OK: false, Message: "agent " + name + " not found"})
		return
	}
	views, err := s.reader.Assemble(r.Context())
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	if _, member := findAgentView(views, trimmed); !member {
		s.write(w, http.StatusNotFound, Envelope{OK: false, Message: "agent " + name + " not found"})
		return
	}

	if msg, valid := validateMailContent(subject, body); !valid {
		s.write(w, http.StatusBadRequest, Envelope{OK: false, Message: msg})
		return
	}

	res, err := s.mailer.MailSend(r.Context(), trimmed, subject, body)
	if err != nil {
		s.write(w, http.StatusBadGateway, Envelope{OK: false, Message: err.Error()})
		return
	}
	s.write(w, http.StatusOK, Envelope{OK: true, Message: "Mail queued for " + trimmed + " — delivery is asynchronous", Data: map[string]int{"exit_code": res.ExitCode}})
}

// findAgentView returns the AgentView whose Name matches name (exact), and whether it was found.
// It is the read-model membership oracle shared by the detail and mail handlers.
func findAgentView(views []readmodel.AgentView, name string) (readmodel.AgentView, bool) {
	for _, v := range views {
		if v.Name == name {
			return v, true
		}
	}
	return readmodel.AgentView{}, false
}

// clampLines maps the ?lines= query to a snapshot line count. It NEVER errors:
// absent/non-numeric ⇒ the default 120; out-of-range ⇒ clamped to [1,500]. The clamped value is
// what the handler passes to Tail and what the response's tail.lines reports.
func clampLines(raw string) int {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 120
	}
	if n < 1 {
		return 1
	}
	if n > 500 {
		return 500
	}
	return n
}

// mailBody is the composer POST shape.
type mailBody struct {
	Subject string `json:"subject"`
	Body    string `json:"body"`
}

// decodeMailBody decodes {subject, body}. A missing/empty body decodes to empty strings (the
// content validation then rejects them as required); a malformed body ⇒ ok=false for a 400.
func decodeMailBody(r *http.Request) (subject, body string, ok bool) {
	if r.Body == nil {
		return "", "", true
	}
	var b mailBody
	if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
		if errors.Is(err, io.EOF) {
			return "", "", true
		}
		return "", "", false
	}
	return b.Subject, b.Body, true
}

// validateMailContent applies friendly error copy and the ≤200/≤10000 rune caps at the handler
// level. It is at least as strict as Wrapper.MailSend's
// pre-exec validation, so anything it accepts the wrapper also accepts (no content path can slip to
// a 502). Subject: single-line, no C0 controls except tab. Body: multi-line (newline + tab allowed).
func validateMailContent(subject, body string) (msg string, valid bool) {
	if strings.TrimSpace(subject) == "" {
		return "subject is required", false
	}
	if utf8.RuneCountInString(subject) > 200 {
		return "subject too long (max 200 characters)", false
	}
	for _, r := range subject {
		if r == '\n' || r == '\r' || (r < 0x20 && r != '\t') || r == 0x7f {
			return "subject cannot contain control characters", false
		}
	}
	if strings.TrimSpace(body) == "" {
		return "message body is required", false
	}
	if utf8.RuneCountInString(body) > 10000 {
		return "message body too long (max 10000 characters)", false
	}
	for _, r := range body {
		if r == '\r' || (r < 0x20 && r != '\t' && r != '\n') || r == 0x7f {
			return "message body cannot contain control characters other than newline and tab", false
		}
	}
	return "", true
}

// slingBody is the structured POST body of a sling request: the operator's positional task plus the
// remaining user-providable fields as vars. `task` binds to the formula's effective field
// (Schema.Primary) as the af-sling positional; each `vars` entry travels as one --var (#440).
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

// handlePrototypes (read) returns the enumerated, servable prototype dirs. Each entry is
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
// agent is verified parked at the matching design-feedback-{N} gate. An off-gate (or
// no-such-agent) submission returns ok:false with the honest "feedback not currently open" message
// and writes nothing; a transport failure reading gate state is a 502. The form is the SOLE
// AUTHORITY that releases the gate — there is no alternate feedback channel.
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
