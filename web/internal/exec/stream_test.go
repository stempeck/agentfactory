package exec

import (
	"bytes"
	"context"
	osexec "os/exec"
	"testing"
)

// install joins the allowlist (Phase 1d) so the real runner permits the verb — Wrapper.GenerateAgents
// is its sole, fixed-argv caller.
func TestValidateVerb_AllowsInstall(t *testing.T) {
	if err := ValidateVerb("install"); err != nil {
		t.Fatalf("install must be allowlisted: %v", err)
	}
}

// GenerateAgents emits EXACTLY the fixed argv `install --agents` — no caller args, ever. This is the
// no-passthrough / exec-safety pin (design Decision 4 / AC-10): the sole `install` caller cannot smuggle
// any other install subcommand or role argument (af install --agents itself rejects extras). Up-shaped
// (factory-wide mutation), so it is a plain Run with no stdin.
func TestWrapper_GenerateAgents_ArgvExact(t *testing.T) {
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	if _, err := w.GenerateAgents(context.Background()); err != nil {
		t.Fatalf("GenerateAgents: %v", err)
	}
	c := fr.lastCall()
	if c.Verb != "install" {
		t.Fatalf("verb = %q, want install", c.Verb)
	}
	if len(c.Args) != 1 || c.Args[0] != "--agents" {
		t.Fatalf("args = %v, want [--agents] exactly (no caller args)", c.Args)
	}
	if c.Stdin != nil {
		t.Fatalf("GenerateAgents is a plain Run — it must not pipe stdin, got %q", c.Stdin)
	}
}

// FormulaValidate pipes the formula TOML to the already-allowlisted `formula` verb via RunStdin
// (mirrors ConfigSet). It is a READ — no lock, no pre-flight — and needs NO allowlist entry of its own.
func TestWrapper_FormulaValidate_Argv(t *testing.T) {
	fr := newFakeRunner()
	w := NewWrapper(fr, "")
	text := []byte("formula = \"x\"\ntype = \"expansion\"\n[[template]]\nid=\"a\"\n")
	if _, err := w.FormulaValidate(context.Background(), text); err != nil {
		t.Fatalf("FormulaValidate: %v", err)
	}
	c := fr.lastCall()
	if c.Verb != "formula" {
		t.Fatalf("verb = %q, want formula", c.Verb)
	}
	want := []string{"validate", "--json"}
	if len(c.Args) != 2 || c.Args[0] != want[0] || c.Args[1] != want[1] {
		t.Fatalf("args = %v, want %v", c.Args, want)
	}
	if string(c.Stdin) != string(text) {
		t.Fatalf("stdin = %q, want %q (the TOML must round-trip to the child's stdin)", c.Stdin, text)
	}
}

// RunStream joins the Runner seam (extend-don't-mutate) and delivers the child's stdout to onChunk as
// bytes arrive. Driven against the recording fake: the canned chunks reach onChunk in order AND the call
// (verb + argv) is recorded, so a caller can assert both delivery and the invocation.
func TestRunStream_FeedsChunksAndRecordsCall(t *testing.T) {
	fr := newFakeRunner()
	fr.chunks = [][]byte{[]byte("regenerating "), []byte("agents...\n"), []byte("done\n")}

	var got []byte
	onChunk := func(b []byte) { got = append(got, b...) }
	if _, err := fr.RunStream(context.Background(), onChunk, "install", "--agents"); err != nil {
		t.Fatalf("RunStream: %v", err)
	}
	if want := "regenerating agents...\ndone\n"; string(got) != want {
		t.Fatalf("onChunk received %q, want %q", got, want)
	}
	c := fr.lastCall()
	if c.Verb != "install" || len(c.Args) != 1 || c.Args[0] != "--agents" {
		t.Fatalf("RunStream must record the call; got verb=%q args=%v", c.Verb, c.Args)
	}
}

// The REAL ExecRunner.RunStream pins the spawned child's cmd.Dir to the factory root (mirrors
// TestRun_AllVerbs_CarryCmdDir for the buffered path). The execCommand seam is captured and pointed at a
// guaranteed-absent binary so Start() fails at LookPath WITHOUT forking — the assertion reads .Dir with
// no real process spawned. Proves the net-new streaming path still honors the #432 cmd.Dir pin.
func TestExecRunner_RunStream_PinsCmdDir(t *testing.T) {
	factoryRoot := t.TempDir()
	er := NewExecRunner(factoryRoot)
	var captured *osCmd
	captureCmdDir(er, &captured)

	_, err := er.RunStream(context.Background(), func([]byte) {}, "agents", "list", "--json")
	if err == nil {
		t.Fatalf("RunStream against a nonexistent binary must return an error")
	}
	if captured == nil {
		t.Fatalf("execCommand seam was never reached for a valid verb")
	}
	if captured.Dir != factoryRoot {
		t.Fatalf("cmd.Dir = %q, want factory root %q (the streaming path must pin cmd.Dir too)", captured.Dir, factoryRoot)
	}
}

// The REAL ExecRunner.RunStream streams a genuine child's stdout through onChunk. Uses the execCommand
// seam to run a NON-shell producer (printf on PATH — the source-lint forbids sh/bash, not printf) that
// writes known bytes, so the pipe + Read-loop path is exercised end-to-end and the bytes actually arrive.
func TestExecRunner_RunStream_RealChildStreams(t *testing.T) {
	if _, err := osexec.LookPath("printf"); err != nil {
		t.Skip("printf not on PATH; the fake-driven RunStream test carries the contract")
	}
	er := NewExecRunner("")
	er.execCommand = func(ctx context.Context, name string, args ...string) *osCmd {
		return osexec.CommandContext(ctx, "printf", "hello-stream")
	}
	var buf bytes.Buffer
	res, err := er.RunStream(context.Background(), func(b []byte) { buf.Write(b) }, "agents")
	if err != nil {
		t.Fatalf("RunStream real child: %v", err)
	}
	if buf.String() != "hello-stream" {
		t.Fatalf("streamed stdout = %q, want %q", buf.String(), "hello-stream")
	}
	if res.ExitCode != 0 {
		t.Fatalf("exit code = %d, want 0", res.ExitCode)
	}
}
