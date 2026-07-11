// Package exec is the command-injection-safe af exec wrapper for the web module.
//
// Every af invocation is built as an argv array (program "af" + literal arguments) and run
// via os/exec — NEVER through a shell. This mirrors the canonical af-core idiom at
// internal/tmux/tmux.go:163-177 (exec.Command with a fixed program name + an argv slice, no
// shell interpolation). A field value such as "; rm -rf" is carried as ONE literal argv
// element and can never be interpreted as a command.
//
// All execution goes through the Runner interface so the server, read-model, and handlers
// depend on the seam — never on os/exec directly. Tests inject a hermetic fake so the suite
// can assert argv/serialization without ever spawning a real af (ADR-018). No unit or
// acceptance test issues a real down/sling/--reset against the live tree.
package exec

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sync"
)

// osCmd aliases os/exec.Cmd so the execCommand seam has a stable spelling.
type osCmd = osexec.Cmd

// Result is the outcome of one af invocation.
type Result struct {
	Stdout   string
	Stderr   string
	ExitCode int
}

// Runner executes `af <verb> <args...>` as an argv array. It is the single seam between the
// web module and the af binary; implementations must never interpret args through a shell.
//
// RunStdin is the stdin-capable sibling of Run: the same argv-array contract, but with a payload
// piped to the child process's standard input. It exists because `af config <file> set` reads the
// full config document on stdin (internal/cmd/config_set.go:60-65); Run has no stdin parameter, so
// extending the seam (rather than mutating Run's signature) keeps every existing Run caller intact.
//
// RunStream is the streaming sibling of Run: the same argv-array contract, but the child's stdout is
// delivered to onChunk as bytes arrive instead of being buffered and returned whole. It exists because
// `af install --agents` is a long regeneration whose progress must reach the operator live (#502 Phase
// 1d widens the seam; Phase 2's job runner consumes the stream). The seam is EXTENDED (not mutated): the
// returned Result carries the exit code (Stdout stays empty — the bytes went to onChunk), so a caller
// still learns the child's exit status the same way Run/RunStdin report it.
type Runner interface {
	Run(ctx context.Context, verb string, args ...string) (Result, error)
	RunStdin(ctx context.Context, stdin []byte, verb string, args ...string) (Result, error)
	RunStream(ctx context.Context, onChunk func([]byte), verb string, args ...string) (Result, error)
}

// Sentinel errors surfaced as friendly "agent busy" states by the server.
var (
	// ErrAgentBusy means another UI mutation for this agent is already in flight
	// (per-agent try-lock; closes UI-vs-itself races only).
	ErrAgentBusy = errors.New("agent busy: a mutation is already in progress")
	// ErrAgentOrchestrated means the agent's live session is driven by a dispatched /
	// orchestrated formula (.runtime/dispatched present); the UI refuses to collide with it.
	ErrAgentOrchestrated = errors.New("agent busy: managed by an orchestrator")
)

// afArgv builds the full argv for invoking af. argv[0] is always "af"; the verb and every
// argument are literal elements — the value is never split or shell-interpreted.
func afArgv(verb string, args ...string) []string {
	return append([]string{"af", verb}, args...)
}

// ExecRunner is the real, process-backed Runner. The execCommand field is an internal seam so
// even the real impl is unit-inspectable without spawning a process.
type ExecRunner struct {
	bin         string
	root        string // factory root; the spawned af child's cmd.Dir is pinned to this ("" inherits the console's cwd)
	execCommand func(ctx context.Context, name string, args ...string) *osCmd
}

// NewExecRunner returns a Runner that invokes the `af` binary on PATH, pinning the spawned af
// child's working directory to root. root is required — this is the SOLE constructor form,
// a compile-time interlock: a root-less production caller will not compile, so the #432
// bug cannot silently return via a forgotten root. Pass "" to opt into the prior cwd-inheriting
// behaviour (used by unit tests that reject the verb before any exec).
func NewExecRunner(root string) *ExecRunner {
	return &ExecRunner{
		bin:         "af",
		root:        root,
		execCommand: osexec.CommandContext,
	}
}

// Run executes `af <verb> <args...>` as an argv array (mirrors internal/tmux/tmux.go:163-177).
// The verb is checked against the allowlist before any process is started. A non-zero exit is
// returned as an error with the ExitCode populated on Result; reads encode failure in their
// JSON payload (state:"error") and exit 0, so read callers branch on .state, not on this error.
func (e *ExecRunner) Run(ctx context.Context, verb string, args ...string) (Result, error) {
	return e.run(ctx, nil, verb, args...)
}

// RunStdin is Run with a stdin payload piped to the child. It is the write path used by
// `af config <file> set`, which reads the full config JSON on stdin. On a non-zero exit the
// returned error embeds the child's stderr — that is the friendly per-field validation message
// af-core prints when the config fails struct/cross-file validation.
func (e *ExecRunner) RunStdin(ctx context.Context, stdin []byte, verb string, args ...string) (Result, error) {
	return e.run(ctx, stdin, verb, args...)
}

// RunStream runs `af <verb> <args...>` and delivers the child's stdout to onChunk as bytes arrive.
// It cannot funnel through run(), which wires stdout to a bytes.Buffer and blocks on cmd.Run() until
// exit; a stream needs its own path: attach a pipe to the child's stdout, Start (not Run), pump the
// pipe into onChunk until EOF, THEN Wait for the exit status. Verb allowlisting and the #432 cmd.Dir
// pin are preserved exactly as in run(). The returned Result carries the ExitCode (Stdout stays empty —
// stdout went to onChunk); a non-zero exit is surfaced as an error embedding the child's stderr, the
// same mapping run() uses.
func (e *ExecRunner) RunStream(ctx context.Context, onChunk func([]byte), verb string, args ...string) (Result, error) {
	if err := ValidateVerb(verb); err != nil {
		return Result{}, err
	}
	argv := afArgv(verb, args...)
	cmd := e.execCommand(ctx, argv[0], argv[1:]...)
	if e.root != "" { // pin the af child's working directory to the resolved factory root (#432), same as run()
		cmd.Dir = e.root
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return Result{}, fmt.Errorf("af %s: stdout pipe: %w", verb, err)
	}
	if err := cmd.Start(); err != nil {
		return Result{}, fmt.Errorf("af %s: %w", verb, err)
	}

	// Raw fixed-size reads (not bufio.Scanner, which would split on lines and could stall a
	// progress stream that lacks a trailing newline). Each chunk is copied before onChunk sees it
	// because buf is reused on the next iteration.
	buf := make([]byte, 32*1024)
	for {
		n, rerr := stdout.Read(buf)
		if n > 0 && onChunk != nil {
			onChunk(append([]byte(nil), buf[:n]...))
		}
		if rerr != nil {
			break // io.EOF on a clean close; any read error ends the pump and Wait reports the real cause
		}
	}

	err = cmd.Wait()
	res := Result{Stderr: stderr.String()}
	if err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, fmt.Errorf("af %s: exit %d: %s", verb, res.ExitCode, stderr.String())
		}
		return res, fmt.Errorf("af %s: %w", verb, err)
	}
	return res, nil
}

// run is the shared core of Run/RunStdin. A nil stdin leaves the child's stdin unset (identical to
// the prior Run behaviour); a non-nil stdin is piped in as a single reader.
func (e *ExecRunner) run(ctx context.Context, stdin []byte, verb string, args ...string) (Result, error) {
	if err := ValidateVerb(verb); err != nil {
		return Result{}, err
	}
	argv := afArgv(verb, args...)
	cmd := e.execCommand(ctx, argv[0], argv[1:]...)
	if e.root != "" { // pin the af child's working directory to the resolved factory root, never via an
		// inherited AF_ROOT env var, which could be silently wrong or misattribute the sender
		cmd.Dir = e.root
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}

	err := cmd.Run()
	res := Result{Stdout: stdout.String(), Stderr: stderr.String()}
	if err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) {
			res.ExitCode = ee.ExitCode()
			return res, fmt.Errorf("af %s: exit %d: %s", verb, res.ExitCode, stderr.String())
		}
		return res, fmt.Errorf("af %s: %w", verb, err)
	}
	return res, nil
}

// factoryLockKey is the per-"agent" serialization key for factory-wide up/down.
const factoryLockKey = "@factory"

// Wrapper is the high-level surface the server depends on. It validates every input
// against the allowlists, serializes mutations per agent (UI-vs-itself), and performs a
// best-effort cross-actor pre-flight before any mutating exec.
//
// Concurrency note: the per-agent mutex closes only UI-vs-itself races. Cross-actor
// races (UI vs CLI operator vs the autonomous orchestrator) are NOT eliminable here — af's
// underlying locks are advisory/PID-TOCTOU (internal/lock/lock.go:52). The dispatched-marker
// pre-flight is a bounded-TOCTOU mitigation, not a guarantee; the residual is an accepted risk.
type Wrapper struct {
	runner Runner
	root   string // factory root; "" disables the on-disk dispatched-marker pre-flight

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewWrapper builds a Wrapper over the given Runner. root is the factory root used for the
// .runtime/dispatched pre-flight; pass "" to disable the on-disk check (e.g. in pure unit tests).
func NewWrapper(r Runner, root string) *Wrapper {
	return &Wrapper{runner: r, root: root, locks: map[string]*sync.Mutex{}}
}

// agentLock returns the per-key mutex, creating it on first use.
func (w *Wrapper) agentLock(key string) *sync.Mutex {
	w.mu.Lock()
	defer w.mu.Unlock()
	l := w.locks[key]
	if l == nil {
		l = &sync.Mutex{}
		w.locks[key] = l
	}
	return l
}

// isDispatched reports whether <root>/.agentfactory/agents/<name>/.runtime/dispatched exists.
// This mirrors isDispatchedSession (internal/cmd/done.go:470-474) over the path from
// internal/config/paths.go:15. Only existence is contractual — never read the file's content.
func (w *Wrapper) isDispatched(name string) bool {
	if w.root == "" {
		return false
	}
	marker := filepath.Join(w.root, ".agentfactory", "agents", name, ".runtime", "dispatched")
	_, err := os.Stat(marker)
	return err == nil
}

// mutate runs a mutating verb under the per-key lock with the dispatched-marker pre-flight.
// lockKey serializes UI-vs-itself; preflightAgent (if non-empty) is the agent whose
// .runtime/dispatched marker gates the call. The lock is HELD across the exec so a second
// near-simultaneous caller for the same key sees ErrAgentBusy (try-lock semantics).
func (w *Wrapper) mutate(ctx context.Context, lockKey, preflightAgent, verb string, args ...string) (Result, error) {
	l := w.agentLock(lockKey)
	if !l.TryLock() {
		return Result{}, ErrAgentBusy
	}
	defer l.Unlock()

	// Best-effort cross-actor pre-flight, performed immediately before the exec to minimize
	// the TOCTOU window. Refuse to collide with an orchestrated/dispatched run.
	if preflightAgent != "" && w.isDispatched(preflightAgent) {
		return Result{}, ErrAgentOrchestrated
	}
	return w.runner.Run(ctx, verb, args...)
}

// Up starts the factory: `af up` (no args/flags — the startup set is governed by
// .agentfactory/startup.json).
func (w *Wrapper) Up(ctx context.Context) (Result, error) {
	return w.mutate(ctx, factoryLockKey, "", "up")
}

// GenerateAgents regenerates and reinstalls all formula-derived agents: `af install --agents`. The argv
// is FIXED — exactly ["install","--agents"], no caller args, ever — so the sole `install` caller cannot
// smuggle any other install subcommand or role argument (`af install --agents` itself rejects extras),
// preserving the no-passthrough / exec-safety doctrine (design Decision 4 / AC-10). It is a factory-wide
// mutation, so it takes the same posture as Up: the @factory lock (UI-vs-itself), no per-agent
// pre-flight. (Phase 2's job runner drives the live progress stream via Runner.RunStream; this method is
// the buffered, argv-pinned entry point the seam exposes.)
func (w *Wrapper) GenerateAgents(ctx context.Context) (Result, error) {
	return w.mutate(ctx, factoryLockKey, "", "install", "--agents")
}

// DownFactory stops the whole factory: `af down [--reset]`.
func (w *Wrapper) DownFactory(ctx context.Context, reset bool) (Result, error) {
	var args []string
	if reset {
		args = append(args, "--reset")
	}
	return w.mutate(ctx, factoryLockKey, "", "down", args...)
}

// DownAgent stops a single agent: `af down <name> [--reset]`.
func (w *Wrapper) DownAgent(ctx context.Context, name string, reset bool) (Result, error) {
	name = trimAgent(name)
	if err := ValidateAgentName(name); err != nil {
		return Result{}, err
	}
	args := []string{name}
	if reset {
		args = append(args, "--reset")
	}
	return w.mutate(ctx, name, name, "down", args...)
}

// Sling dispatches a task to an agent: `af sling --agent <name> --reset [--var k=v ...]`. Sling
// is fire-and-forget (af detaches a tmux session and returns in ~5-6s); the lock is held for the
// duration of that call, so a second UI sling for the same agent within the window sees busy.
//
// --reset is ALWAYS appended on the UI sling path. Re-slinging an already-provisioned (even
// idle) agent otherwise errors on Formula Succession — af refuses with "prior formula still
// active; use --reset" (internal/cmd/sling.go) — so the UI path force-resets runtime state to
// keep a UI-slung run byte-identical to the hand-slung `af sling --agent <name> --reset …`
// argv the operator would type. --var travels as ONE literal argv element per field
// (varArgs), never a comma-joined StringSliceVar value.
//
// task is the operator's primary text. af-core's `af sling [task]` takes it as the POSITIONAL
// argument (Use: "sling [task]"; Args: cobra.MaximumNArgs(1)), so it is emitted AFTER a `--`
// terminator: `[--agent <name> --reset --var k=v … -- <task>]`. The `--` is mandatory (#440) —
// with default pflag a task beginning with `-`/`--` would otherwise be misparsed as a flag and
// re-trigger af's `task description required` error. The task is value-validated (validateTask:
// reject control chars; allow commas and a leading dash) but never key-checked — it is the
// positional, not a --var.
func (w *Wrapper) Sling(ctx context.Context, name, task string, vars map[string]string) (Result, error) {
	name = trimAgent(name)
	if err := ValidateAgentName(name); err != nil {
		return Result{}, err
	}
	if err := validateTask(task); err != nil {
		return Result{}, err
	}
	vargs, err := varArgs(vars)
	if err != nil {
		return Result{}, err
	}
	args := append([]string{"--agent", name, "--reset"}, vargs...)
	args = append(args, "--", task) // `--` terminator then the positional task
	return w.mutate(ctx, name, name, "sling", args...)
}

// AgentsListJSON returns the raw stdout of `af agents list --json` (a read; no lock, no
// pre-flight). The command always exits 0 and encodes failure as {"state":"error",...}; the
// read-model branches on that .state shape, not on the process exit code.
func (w *Wrapper) AgentsListJSON(ctx context.Context) (string, error) {
	res, err := w.runner.Run(ctx, "agents", "list", "--json")
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

// FormulaShowJSON returns the raw stdout of `af formula show <formula> --json` (a read; no lock,
// no pre-flight). Like AgentsListJSON the command always exits 0 and encodes failure as
// {"state":"error",...}; the form-schema reader branches on that .state shape, not on the
// process exit code. The "formula" verb is already on the allowlist (validate.go).
func (w *Wrapper) FormulaShowJSON(ctx context.Context, formula string) (string, error) {
	res, err := w.runner.Run(ctx, "formula", "show", formula, "--json")
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

// FormulaValidate pipes a formula.toml document to `af formula validate --json`, which returns the
// engine-of-record's composed verdict ({ok, findings:[{lamp,message}]}, always exit 0). It is a READ —
// no lock, no pre-flight (like FormulaShowJSON) — and it reuses the ALREADY-allowlisted `formula` verb
// via RunStdin (mirroring ConfigSet), so it needs no allowlist entry of its own. The write handler that
// consumes it turns a rejecting verdict into a 422; the always-0 exit keeps that distinguishable from a
// process failure by the body, not the exit code.
func (w *Wrapper) FormulaValidate(ctx context.Context, text []byte) (Result, error) {
	return w.runner.RunStdin(ctx, text, "formula", "validate", "--json")
}

// DispatchStatusJSON returns the raw stdout of `af dispatch status --json` (a read; no lock, no
// pre-flight). af-core already computes dispatcher + per-agent tmux liveness inside the payload, so
// the web module gets liveness for free from this one read. Shape: {dispatcher_running:bool,
// entries:[{issue,agent,agent_running,item_url,source,dispatched_at}]} (internal/cmd/dispatch.go:573-587).
// Like the other reads, the command always exits 0 and encodes failure as {"state":"error",...};
// the dispatch reader branches on that .state shape, not on the process exit code. The "dispatch"
// verb is already on the allowlist (validate.go).
func (w *Wrapper) DispatchStatusJSON(ctx context.Context) (string, error) {
	res, err := w.runner.Run(ctx, "dispatch", "status", "--json")
	if err != nil {
		return "", err
	}
	return res.Stdout, nil
}

// ConfigSet writes a curated config file by piping payload (a COMPLETE JSON config document) to the
// stdin of `af config <file> set`, where file ∈ {"dispatch","startup"}. af-core is the single
// canonical validator/writer: it validates (struct + cross-file ValidateDispatchConfig for dispatch)
// and writes atomically (temp+rename), exiting non-zero with a friendly stderr message on any
// validation failure — which RunStdin surfaces in the returned error. The web module never
// re-declares the config schema nor re-implements validation on the write side.
//
// file is checked against the {dispatch,startup} allowlist BEFORE exec (factory.json is read-only —
// there is no `af config factory set`), so a caller can never smuggle an arbitrary subcommand as the
// second argv element. This is a config write, not an agent session mutation: no per-agent lock and
// no .runtime/dispatched pre-flight.
func (w *Wrapper) ConfigSet(ctx context.Context, file string, payload []byte) (Result, error) {
	if file != "dispatch" && file != "startup" {
		return Result{}, fmt.Errorf("config file %q is not writable", file)
	}
	return w.runner.RunStdin(ctx, payload, "config", file, "set")
}

// mailFooter is appended to every web-sent body so recipients don't wait on a reply-blackhole:
// the wrapper-pinned sender below is not a monitored mailbox.
const mailFooter = "\n\n(sent from the web console; replies to 'operator' are not monitored)"

// MailSend sends operator mail to an agent's mailbox: `af mail send <name> --subject=<s>
// --message=<body+footer>` with the constant sender `operator`. Recipient name (trim, then the
// copied af-core shape rule — the wrapper convention), subject, and body are all validated
// BEFORE any exec; the sender is a wrapper constant, never caller input (root af enforces
// members ∪ operator on it — internal/cmd/mail.go). Single-token `=` flag forms so a
// dash-leading value can never re-parse as a flag, and the subcommand is fixed to
// `send` — a caller can never smuggle a different mail subcommand.
//
// MailSend deliberately calls runner.Run DIRECTLY — no mutate(), no per-agent lock, no
// .runtime/dispatched pre-flight, the same direct-Run exception ConfigSet above makes. Mail's
// primary recipients ARE dispatched agents: the pre-flight would refuse exactly the mail this
// feature exists to send. Mail targets the agent's MAILBOX, not its running session, so there
// is no session mutation to serialize or protect.
func (w *Wrapper) MailSend(ctx context.Context, name, subject, body string) (Result, error) {
	name = trimAgent(name)
	if err := ValidateAgentName(name); err != nil {
		return Result{}, err
	}
	if err := validateMailSubject(subject); err != nil {
		return Result{}, err
	}
	if err := validateMailBody(body); err != nil {
		return Result{}, err
	}
	return w.runner.Run(ctx, "mail", "send", name, "--subject="+subject, "--message="+body+mailFooter, "--from=operator")
}
