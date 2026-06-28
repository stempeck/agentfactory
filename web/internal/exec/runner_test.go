package exec

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// compile-time proof the fake satisfies the seam (mirrors af-core seam_test.go).
var _ Runner = (*fakeRunner)(nil)

// call records one invocation of the fake Runner.
type call struct {
	Verb  string
	Args  []string
	Stdin []byte // the payload piped to the child (nil for plain Run calls)
}

// fakeRunner is a hermetic, recording Runner double. It NEVER shells out, sleeps,
// or touches the filesystem. Modeled on af-core internal/cmd/hermetic_test.go's fakeTmux.
type fakeRunner struct {
	mu    sync.Mutex
	calls []call
	resp  map[string]Result // canned stdout keyed by verb
	err   map[string]error  // canned error keyed by verb

	// serialization-window hooks (used only by TestSling_SerializedPerAgent).
	entered chan string   // receives the verb when a call enters Run
	block   chan struct{} // Run blocks until this is closed
}

func newFakeRunner() *fakeRunner {
	return &fakeRunner{resp: map[string]Result{}, err: map[string]error{}}
}

func (f *fakeRunner) Run(ctx context.Context, verb string, args ...string) (Result, error) {
	return f.RunStdin(ctx, nil, verb, args...)
}

// RunStdin records the call (including any piped stdin payload) and returns the canned result for
// the verb. Both seam methods funnel through here so a test can assert argv AND stdin uniformly.
func (f *fakeRunner) RunStdin(ctx context.Context, stdin []byte, verb string, args ...string) (Result, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{Verb: verb, Args: append([]string(nil), args...), Stdin: append([]byte(nil), stdin...)})
	entered, block := f.entered, f.block
	r, e := f.resp[verb], f.err[verb]
	f.mu.Unlock()

	if entered != nil {
		entered <- verb
	}
	if block != nil {
		<-block
	}
	return r, e
}

// lastCall returns the most recently recorded call (for argv/stdin assertions).
func (f *fakeRunner) lastCall() call {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return call{}
	}
	return f.calls[len(f.calls)-1]
}

func (f *fakeRunner) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

func (f *fakeRunner) lastArgs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.calls) == 0 {
		return nil
	}
	return f.calls[len(f.calls)-1].Args
}

// AC1 — No shell-string exec: a malicious field value is ONE literal argv element.
func TestExec_NoShellString(t *testing.T) {
	got := afArgv("down", "; rm -rf")
	want := []string{"af", "down", "; rm -rf"}
	if len(got) != len(want) {
		t.Fatalf("afArgv length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("afArgv[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
	// And through the wrapper: the value reaches the Runner as one literal element.
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.DownAgent(context.Background(), "victim", false); err != nil {
		t.Fatalf("DownAgent: %v", err)
	}
	// argv tail must be exactly the agent name (no shell, no splitting).
	if a := fr.lastArgs(); len(a) != 1 || a[0] != "victim" {
		t.Fatalf("down args = %v, want [victim]", a)
	}
}

// AC6 — Injectable fake: the wrapper runs entirely against the fake; canned stdout flows back.
func TestRunner_InjectableFake(t *testing.T) {
	fr := newFakeRunner()
	fr.resp["agents"] = Result{Stdout: "[]", ExitCode: 0}
	w := NewWrapper(fr, "")

	out, err := w.AgentsListJSON(context.Background())
	if err != nil {
		t.Fatalf("AgentsListJSON: %v", err)
	}
	if out != "[]" {
		t.Fatalf("stdout = %q, want []", out)
	}
	if fr.callCount() != 1 {
		t.Fatalf("fake recorded %d calls, want 1 (handler must use the seam, not exec.Command)", fr.callCount())
	}
}

// AC3 — Per-agent serialization: two near-simultaneous mutations for the SAME agent;
// the second sees busy. Two DIFFERENT agents proceed concurrently.
func TestSling_SerializedPerAgent(t *testing.T) {
	fr := newFakeRunner()
	fr.entered = make(chan string, 1)
	fr.block = make(chan struct{})
	w := NewWrapper(fr, "")

	var wg sync.WaitGroup
	var firstErr, secondErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		_, firstErr = w.Sling(context.Background(), "alpha", "t", nil)
	}()

	// Wait until the first call is INSIDE Run (holding alpha's lock).
	<-fr.entered

	// Second mutation for the same agent must be rejected as busy (try-lock fails).
	_, secondErr = w.Sling(context.Background(), "alpha", "t", nil)
	if secondErr != ErrAgentBusy {
		t.Fatalf("second concurrent Sling(alpha) err = %v, want ErrAgentBusy", secondErr)
	}

	// A DIFFERENT agent is not blocked by alpha's lock (per-agent, not global).
	frBeta := newFakeRunner()
	wBeta := NewWrapper(frBeta, "")
	if _, err := wBeta.Sling(context.Background(), "beta", "t", nil); err != nil {
		t.Fatalf("Sling(beta) on its own wrapper should proceed, got %v", err)
	}

	close(fr.block) // release the first call
	wg.Wait()
	if firstErr != nil {
		t.Fatalf("first Sling(alpha) err = %v, want nil", firstErr)
	}
}

// AC3 corollary — different agents on the SAME wrapper run concurrently.
func TestMutate_DifferentAgentsConcurrent(t *testing.T) {
	fr := newFakeRunner()
	fr.entered = make(chan string, 2)
	fr.block = make(chan struct{})
	w := NewWrapper(fr, "")

	var wg sync.WaitGroup
	for _, name := range []string{"alpha", "beta"} {
		wg.Add(1)
		go func(n string) {
			defer wg.Done()
			_, _ = w.Sling(context.Background(), n, "t", nil)
		}(name)
	}
	// Both should enter Run concurrently (neither blocks the other).
	<-fr.entered
	<-fr.entered
	close(fr.block)
	wg.Wait()
	if fr.callCount() != 2 {
		t.Fatalf("expected 2 concurrent calls, got %d", fr.callCount())
	}
}

// Verb allowlist — the Runner refuses any verb outside the allowlist and never execs.
func TestRun_RejectsUnknownVerb(t *testing.T) {
	er := NewExecRunner("")
	execed := false
	er.execCommand = func(ctx context.Context, name string, args ...string) *osCmd {
		execed = true
		return nil
	}
	if _, err := er.Run(context.Background(), "rm", "-rf", "/"); err == nil {
		t.Fatalf("Run(rm) should be rejected by the verb allowlist")
	}
	if execed {
		t.Fatalf("an unknown verb must NEVER reach exec.Command")
	}
}

// Agent-name allowlist — injection-shaped names are refused at the wrapper, never exec'd.
func TestRun_RejectsBadAgentName(t *testing.T) {
	for _, bad := range []string{"a;rm", "../x", "agent name", "123start", "dispatch", ""} {
		fr := newFakeRunner()
		w := NewWrapper(fr, "")
		if _, err := w.DownAgent(context.Background(), bad, false); err == nil {
			t.Fatalf("DownAgent(%q) should be rejected", bad)
		}
		if fr.callCount() != 0 {
			t.Fatalf("DownAgent(%q) must not exec (recorded %d calls)", bad, fr.callCount())
		}
	}
}

// --var validation — bad keys/values rejected; an injection-shaped value passes as one literal arg.
func TestSling_VarValidation(t *testing.T) {
	// Bad key: not ^[A-Za-z0-9_]+$.
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.Sling(context.Background(), "alpha", "t", map[string]string{"bad-key": "v"}); err == nil {
		t.Fatalf("var key with a hyphen should be rejected")
	}
	if fr.callCount() != 0 {
		t.Fatalf("rejected var must not exec")
	}

	// Control char in value rejected.
	fr2 := newFakeRunner()
	w2 := NewWrapper(fr2, "")
	if _, err := w2.Sling(context.Background(), "alpha", "t", map[string]string{"k": "line1\nline2"}); err == nil {
		t.Fatalf("var value with a newline should be rejected")
	}

	// A shell-looking value is fine — it travels as one literal argv element.
	fr3 := newFakeRunner()
	w3 := NewWrapper(fr3, "")
	if _, err := w3.Sling(context.Background(), "alpha", "t", map[string]string{"k": "; rm -rf"}); err != nil {
		t.Fatalf("a literal shell-looking value should be accepted as one arg, got %v", err)
	}
	args := fr3.lastArgs()
	joined := ""
	for _, a := range args {
		if a == "k=; rm -rf" {
			joined = a
		}
	}
	if joined == "" {
		t.Fatalf("expected one literal var arg 'k=; rm -rf' in %v", args)
	}
}

// H-P3 — best-effort cross-actor pre-flight: an agent with .runtime/dispatched present is
// refused for sling/down/--reset. Bounded-TOCTOU mitigation only (no full-closure claim).
func TestRun_RefusesDispatchedAgent(t *testing.T) {
	root := t.TempDir()
	agentRuntime := filepath.Join(root, ".agentfactory", "agents", "alpha", ".runtime")
	if err := os.MkdirAll(agentRuntime, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(agentRuntime, "dispatched"), []byte("@cli"), 0o644); err != nil {
		t.Fatal(err)
	}

	fr := newFakeRunner()
	w := NewWrapper(fr, root)

	if _, err := w.DownAgent(context.Background(), "alpha", false); err != ErrAgentOrchestrated {
		t.Fatalf("DownAgent(orchestrated) err = %v, want ErrAgentOrchestrated", err)
	}
	if _, err := w.Sling(context.Background(), "alpha", "t", nil); err != ErrAgentOrchestrated {
		t.Fatalf("Sling(orchestrated) err = %v, want ErrAgentOrchestrated", err)
	}
	if fr.callCount() != 0 {
		t.Fatalf("orchestrated agent must not be exec'd (recorded %d calls)", fr.callCount())
	}

	// A NON-dispatched agent under the same root proceeds.
	if _, err := w.DownAgent(context.Background(), "beta", false); err != nil {
		t.Fatalf("DownAgent(beta, not dispatched) err = %v, want nil", err)
	}
}

// Destructive --reset is wired through the down verb only when reset==true.
func TestDownAgent_ResetFlag(t *testing.T) {
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.DownAgent(context.Background(), "alpha", true); err != nil {
		t.Fatalf("DownAgent reset: %v", err)
	}
	args := fr.lastArgs()
	hasReset := false
	for _, a := range args {
		if a == "--reset" {
			hasReset = true
		}
	}
	if !hasReset {
		t.Fatalf("reset=true must append --reset, got %v", args)
	}
}

// Sling always force-resets, emits one --var per non-task field (sorted), and carries the
// operator's task as the POSITIONAL argument after a `--` terminator (#440 K1/H1). The UI sling
// argv must be byte-identical to `af sling --agent <name> --reset --var k=v -- "<task>"`.
func TestSling_ResetAndVarArgv(t *testing.T) {
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.Sling(context.Background(), "alpha", "do it", map[string]string{"k": "v"}); err != nil {
		t.Fatalf("Sling: %v", err)
	}
	got := fr.lastArgs()
	// vars sorted by varArgs (only k here); the task is the positional after the `--` terminator.
	want := []string{"--agent", "alpha", "--reset", "--var", "k=v", "--", "do it"}
	if len(got) != len(want) {
		t.Fatalf("sling argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("sling argv[%d] = %q, want %q (full: %v)", i, got[i], want[i], got)
		}
	}
}

// H1 — a dash-prefixed task is carried as the positional AFTER the `--` terminator, never parsed as
// a flag. Under af-core's cobra.MaximumNArgs(1) + default pflag, an unterminated "-n …" task would
// be misparsed as a flag and re-trigger the `task description required` error (AC-2). The `--`
// terminator (one literal argv element before the task) makes it safe; the argv-array exec (no
// shell) makes `--` zero-risk.
func TestSling_DashPrefixedTaskAfterTerminator(t *testing.T) {
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.Sling(context.Background(), "alpha", "-n drop tables", nil); err != nil {
		t.Fatalf("Sling(dash task): %v", err)
	}
	got := fr.lastArgs()
	want := []string{"--agent", "alpha", "--reset", "--", "-n drop tables"}
	if len(got) != len(want) {
		t.Fatalf("argv = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("argv[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
	}
	// the `--` must be immediately before the task, and the task is exactly one element.
	if got[len(got)-2] != "--" || got[len(got)-1] != "-n drop tables" {
		t.Fatalf("task must be the single positional after `--`, got tail %v", got[len(got)-2:])
	}
}

// A shell-looking task stays ONE literal argv element after `--` — no shell, no splitting (C2).
func TestSling_ShellLookingTaskIsOneArg(t *testing.T) {
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.Sling(context.Background(), "alpha", "; rm -rf /", nil); err != nil {
		t.Fatalf("a shell-looking task should be accepted as one literal positional, got %v", err)
	}
	got := fr.lastArgs()
	if len(got) < 2 || got[len(got)-2] != "--" || got[len(got)-1] != "; rm -rf /" {
		t.Fatalf("shell-looking task must be one literal positional after `--`, got %v", got)
	}
}

// A task with a control char is rejected by validateTask BEFORE any exec (no process is spawned).
func TestSling_RejectsControlCharTask(t *testing.T) {
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.Sling(context.Background(), "alpha", "bad\ntask", nil); err == nil {
		t.Fatalf("a task with a newline must be rejected")
	}
	if fr.callCount() != 0 {
		t.Fatalf("a rejected task must never exec (recorded %d calls)", fr.callCount())
	}
}

// TestValidateTask — AC-3. The task is a free-text VALUE rule: reject control chars / newlines /
// 0x7f (the same rune predicate as validateVar's value loop), but ALLOW tab, commas, and a leading
// dash (the `--` terminator makes a dash-prefixed task safe). The `--var` key identifier regex
// (validVarKey) must NOT be applied — the task is free text, not an identifier.
func TestValidateTask(t *testing.T) {
	good := []string{
		"do the thing",
		"fix the bug, then verify", // comma allowed
		"-n drop tables",           // leading dash allowed (`--` terminator protects it)
		"--reset everything",       // double-dash prefix allowed
		"; rm -rf /",               // shell metacharacters are harmless (one literal argv element)
		"tab\there",                // tab (0x09) allowed
		"unicode café ☃ 日本語",       // arbitrary printable unicode allowed
		"",                         // empty has no control chars; arity is af-core's concern, not this value rule
	}
	for _, tk := range good {
		if err := validateTask(tk); err != nil {
			t.Errorf("validateTask(%q) = %v, want nil (must accept commas, leading dash, shell text)", tk, err)
		}
	}
	bad := []string{
		"line1\nline2",     // newline
		"carriage\rreturn", // carriage return
		"null\x00byte",     // C0 control
		"bell\x07now",      // C0 control
		"del\x7fhere",      // 0x7f DEL
	}
	for _, tk := range bad {
		if err := validateTask(tk); err == nil {
			t.Errorf("validateTask(%q) = nil, want error (control characters must be rejected)", tk)
		}
	}
}

// ValidateAgentName — the copied af-core rule (config.go:57,62-64,294-309).
func TestValidateAgentName(t *testing.T) {
	valid := []string{"a", "agent", "agent-1", "Agent_2", "x-y-z"}
	for _, v := range valid {
		if err := ValidateAgentName(v); err != nil {
			t.Errorf("ValidateAgentName(%q) = %v, want nil", v, err)
		}
	}
	invalid := []string{"", "agent;rm", "../../etc", "agent name", "123start", "dispatch"}
	for _, v := range invalid {
		if err := ValidateAgentName(v); err == nil {
			t.Errorf("ValidateAgentName(%q) = nil, want error", v)
		}
	}
	// length cap (>64)
	long := ""
	for i := 0; i < 65; i++ {
		long += "a"
	}
	if err := ValidateAgentName(long); err == nil {
		t.Errorf("ValidateAgentName(65 chars) = nil, want error")
	}
}

// DispatchStatusJSON is a plain read of `af dispatch status --json` (no lock, no pre-flight).
func TestDispatchStatusJSON_Argv(t *testing.T) {
	fr := newFakeRunner()
	fr.resp["dispatch"] = Result{Stdout: `{"dispatcher_running":true,"entries":[]}`}
	w := NewWrapper(fr, "")

	out, err := w.DispatchStatusJSON(context.Background())
	if err != nil {
		t.Fatalf("DispatchStatusJSON: %v", err)
	}
	if out != `{"dispatcher_running":true,"entries":[]}` {
		t.Fatalf("stdout = %q", out)
	}
	c := fr.lastCall()
	if c.Verb != "dispatch" {
		t.Fatalf("verb = %q, want dispatch", c.Verb)
	}
	want := []string{"status", "--json"}
	if len(c.Args) != 2 || c.Args[0] != want[0] || c.Args[1] != want[1] {
		t.Fatalf("argv = %v, want %v", c.Args, want)
	}
	if c.Stdin != nil {
		t.Fatalf("a read must not pipe stdin, got %q", c.Stdin)
	}
}

// ConfigSet pipes the full config payload to `af config <file> set` on stdin and validates the
// file allowlist (factory.json is read-only).
func TestConfigSet_RoutesStdinThroughAfConfigSet(t *testing.T) {
	payload := []byte(`{"repos":["o/r"],"trigger_label":"go","mappings":[{"labels":["bug"],"agent":"rootcause"}]}`)

	for _, file := range []string{"dispatch", "startup"} {
		fr := newFakeRunner()
		w := NewWrapper(fr, "")
		if _, err := w.ConfigSet(context.Background(), file, payload); err != nil {
			t.Fatalf("ConfigSet(%s): %v", file, err)
		}
		c := fr.lastCall()
		if c.Verb != "config" {
			t.Fatalf("verb = %q, want config", c.Verb)
		}
		want := []string{file, "set"}
		if len(c.Args) != 2 || c.Args[0] != want[0] || c.Args[1] != want[1] {
			t.Fatalf("argv = %v, want %v", c.Args, want)
		}
		if string(c.Stdin) != string(payload) {
			t.Fatalf("stdin = %q, want %q", c.Stdin, payload)
		}
	}

	// factory.json (and any other file) is rejected BEFORE exec — there is no `af config factory set`.
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.ConfigSet(context.Background(), "factory", payload); err == nil {
		t.Fatalf("ConfigSet(factory) must be rejected (read-only)")
	}
	if fr.callCount() != 0 {
		t.Fatalf("a non-writable file must never reach exec (recorded %d calls)", fr.callCount())
	}
}

// ConfigSet surfaces a non-zero `af` exit (the friendly per-field validation error) as an error.
func TestConfigSet_SurfacesValidationError(t *testing.T) {
	fr := newFakeRunner()
	fr.err["config"] = errFakeReject
	w := NewWrapper(fr, "")
	if _, err := w.ConfigSet(context.Background(), "dispatch", []byte(`{}`)); err == nil {
		t.Fatalf("ConfigSet must surface the af validation error")
	}
}

var errFakeReject = errFake("dispatch mapping references unknown agent \"ghost\"")

type errFake string

func (e errFake) Error() string { return string(e) }

// config is now on the allowlist (Phase 3) so the real runner permits the verb (it would otherwise
// fail closed before exec).
func TestValidateVerb_AllowsConfig(t *testing.T) {
	if err := ValidateVerb("config"); err != nil {
		t.Fatalf("config must be allowlisted: %v", err)
	}
}

// ---- #432 Phase 4: cmd.Dir regression coverage (K6 + K6′) ----
//
// These tests lock the Phase-2 fix (runner.go: `if e.root != "" { cmd.Dir = e.root }`) against
// silent regression. They reassign the internal execCommand seam to CAPTURE the constructed *osCmd,
// the same pattern as TestRun_RejectsUnknownVerb above — but for a VALID verb, so run() proceeds to
// set cmd.Dir and call cmd.Run(). The fake therefore returns a REAL *osCmd (not nil): run() sets its
// .Dir BEFORE cmd.Run(), and pointing the command at a guaranteed-nonexistent program makes cmd.Run()
// fail at LookPath WITHOUT forking — so the assertion reads .Dir with no real process spawned.

// nonexistentBinary is a guaranteed-absent program name. It must NEVER be "af": the real af IS on this
// host's PATH (e.g. ~/.local/bin/af), so naming the fake's command "af" would spawn a live af against
// the captured argv. An absent name makes cmd.Run() fail at LookPath instead of forking.
const nonexistentBinary = "af-cmddir-test-no-such-binary"

// captureCmdDir reassigns er.execCommand to record the *osCmd run() constructs (so the test can read
// the .Dir field the fix sets) without ever spawning a process.
func captureCmdDir(er *ExecRunner, captured **osCmd) {
	er.execCommand = func(ctx context.Context, name string, args ...string) *osCmd {
		c := osexec.CommandContext(ctx, nonexistentBinary, args...)
		*captured = c
		return c
	}
}

// K6 — the fix proper: with the process cwd in a NON-factory directory, NewExecRunner(factoryRoot)
// must pin the spawned af child's cmd.Dir to factoryRoot (the channel is cmd.Dir, NOT AF_ROOT), so the
// af child reads the intended factory instead of inheriting the console's cwd (#432).
func TestExecRunner_SetsCmdDir(t *testing.T) {
	t.Chdir(t.TempDir()) // process cwd is a non-factory dir: an inherited-cwd regression would read the wrong place

	factoryRoot := t.TempDir()
	er := NewExecRunner(factoryRoot)

	var captured *osCmd
	captureCmdDir(er, &captured)

	_, _ = er.Run(context.Background(), "agents", "list", "--json") // run() sets captured.Dir, then cmd.Run() fails at LookPath (no fork)

	if captured == nil {
		t.Fatalf("execCommand seam was never reached for a valid verb")
	}
	if captured.Dir != factoryRoot {
		t.Fatalf("cmd.Dir = %q, want factory root %q (the af child must run IN the resolved factory, not inherit the console cwd)", captured.Dir, factoryRoot)
	}
}

// K6 companion — NewExecRunner("") is the documented opt-out (used by unit tests that reject the verb
// before exec). It must leave cmd.Dir empty so the child inherits the caller's cwd.
func TestExecRunner_EmptyRoot_InheritsCwd(t *testing.T) {
	er := NewExecRunner("")

	var captured *osCmd
	captureCmdDir(er, &captured)

	_, _ = er.Run(context.Background(), "agents", "list", "--json")

	if captured == nil {
		t.Fatalf("execCommand seam was never reached for a valid verb")
	}
	if captured.Dir != "" {
		t.Fatalf("cmd.Dir = %q, want empty (NewExecRunner(%q) must inherit the caller's cwd)", captured.Dir, "")
	}
}

// K6′ — the shared-seam invariant: because cmd.Dir is set ONCE in the shared run core, EVERY
// allowlisted verb must carry it. This proves the fix is systemic (not just present for the one verb
// K6 tests) and closes the sibling-bug class where a newly added verb silently bypasses the pin. The
// mutating verbs (down/sling/config) are driven ONLY through the execCommand seam — never a live af —
// so the source-lint (server/lint_test.go: TestExec_NoLiveTreeMutation) stays green.
func TestRun_AllVerbs_CarryCmdDir(t *testing.T) {
	factoryRoot := t.TempDir()
	// The exact allowlist from validate.go (allowedVerbs). A new verb added there without being added
	// here is itself a prompt to re-confirm this invariant.
	verbs := []string{"up", "down", "sling", "agents", "formula", "dispatch", "step", "config"}
	for _, verb := range verbs {
		t.Run(verb, func(t *testing.T) {
			er := NewExecRunner(factoryRoot)
			var captured *osCmd
			captureCmdDir(er, &captured)

			_, _ = er.Run(context.Background(), verb)

			if captured == nil {
				t.Fatalf("verb %q never reached the execCommand seam", verb)
			}
			if captured.Dir == "" {
				t.Fatalf("verb %q: cmd.Dir is empty — the fix must pin cmd.Dir for EVERY allowlisted verb", verb)
			}
			if captured.Dir != factoryRoot {
				t.Fatalf("verb %q: cmd.Dir = %q, want %q", verb, captured.Dir, factoryRoot)
			}
		})
	}
}
