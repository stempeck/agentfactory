package readmodel

import (
	"context"
	"encoding/json"
	"errors"
	osexec "os/exec"
	"sync"
	"testing"
	"time"
)

// ---- #500 Phase 1: TmuxCapture test double ----
//
// Injection strategy (IMPLREADME Gotcha 1): the execCommand seam records the name+args
// PARAMETERS it is asked to construct (option (a) — recording the parameters directly, so
// argv[0] is never lost to the captureCmdDir argv[0]-replacement caveat), and the private
// runCmd seam (option (c)) returns canned stdout/stderr/err as pure Go. No process is ever
// spawned and nothing here shells out — the server lint raw-scans this file for shell
// literals, so canned output can never come from a shell.

// cannedRun is one canned runCmd result, consumed in call order.
type cannedRun struct {
	stdout string
	stderr string
	err    error
}

// errExit stands in for a non-zero tmux exit.
var errExit = errors.New("exit status 1")

// captureFake wires a TmuxCapture whose exec layer is fully faked: argv construction is
// recorded; execution returns the canned results in order.
type captureFake struct {
	mu    sync.Mutex
	argvs [][]string // element 0 is the program name; the rest are the args
	runs  []cannedRun
	n     int
}

func newCaptureFake(t *testing.T, runs ...cannedRun) (*TmuxCapture, *captureFake) {
	t.Helper()
	fx := &captureFake{runs: runs}
	tc := NewTmuxCapture()
	tc.execCommand = func(ctx context.Context, name string, args ...string) *osexec.Cmd {
		fx.mu.Lock()
		fx.argvs = append(fx.argvs, append([]string{name}, args...))
		fx.mu.Unlock()
		return osexec.CommandContext(ctx, name, args...) // constructed only — the faked runCmd never runs it
	}
	tc.runCmd = func(cmd *osexec.Cmd) (string, string, error) {
		fx.mu.Lock()
		defer fx.mu.Unlock()
		if fx.n >= len(fx.runs) {
			t.Fatalf("unexpected exec #%d: only %d canned results wired", fx.n+1, len(fx.runs))
		}
		r := fx.runs[fx.n]
		fx.n++
		return r.stdout, r.stderr, r.err
	}
	return tc, fx
}

func (f *captureFake) calls() [][]string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([][]string, len(f.argvs))
	copy(out, f.argvs)
	return out
}

func assertArgv(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s argv = %v, want %v", label, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s argv[%d] = %q, want %q (full: %v)", label, i, got[i], want[i], got)
		}
	}
}

// The capture argv is byte-pinned to the exact-match target form. tmux PREFIX-matches a bare
// `-t af-<name>` (verified on tmux 3.4 — verification-report §K): with only af-design-v7
// running, `capture-pane -t af-design` exits 0 and silently captures the WRONG agent's
// terminal. Only the '=af-<name>:' form is safe; this pins those bytes against BOTH a live
// and an absent session fixture, and forbids the bare form and -e everywhere.
func TestTmuxCapture_ArgvExactMatch(t *testing.T) {
	// Live fixture: the probe lists the session, then the capture runs.
	tc, fx := newCaptureFake(t,
		cannedRun{stdout: "af-designer\naf-other\n"},
		cannedRun{stdout: "pane text\n"},
	)
	if _, err := tc.Tail(context.Background(), "designer", 120); err != nil {
		t.Fatalf("Tail(live): %v", err)
	}
	calls := fx.calls()
	if len(calls) != 2 {
		t.Fatalf("expected probe + capture (2 execs), got %d: %v", len(calls), calls)
	}
	assertArgv(t, "probe", calls[0], []string{"tmux", "list-sessions", "-F", "#{session_name}"})
	assertArgv(t, "capture", calls[1], []string{"tmux", "capture-pane", "-p", "-t", "=af-designer:", "-S", "-120"})

	// Absent fixture: the probe misses; no capture argv is ever constructed.
	tc2, fx2 := newCaptureFake(t, cannedRun{stdout: "af-other\n"})
	if _, err := tc2.Tail(context.Background(), "designer", 120); err != nil {
		t.Fatalf("Tail(absent): %v", err)
	}

	for _, argv := range append(fx.calls(), fx2.calls()...) {
		for i := 0; i+1 < len(argv); i++ {
			if argv[i] == "-t" && argv[i+1] == "af-designer" {
				t.Fatalf("bare -t af-designer target constructed (prefix-match hazard): %v", argv)
			}
		}
		for _, a := range argv {
			if a == "-e" {
				t.Fatalf("capture must be plain text — never -e (data.md D2-B): %v", argv)
			}
		}
	}
}

// Absent session ⇒ the honest zero view (Live false, empty Output, zero CapturedAt), nil error
// — the module's no-server-is-not-an-error posture (liveness.go:11-13). Lines echoes the
// requested count on every path (data.md D2-B: "the line count actually requested").
func TestTmuxCapture_AbsentSession_HonestValue(t *testing.T) {
	tc, _ := newCaptureFake(t, cannedRun{stdout: "af-other\n"})
	v, err := tc.Tail(context.Background(), "designer", 120)
	if err != nil {
		t.Fatalf("absent session must not be an error, got %v", err)
	}
	if v.Live || v.Output != "" || !v.CapturedAt.IsZero() {
		t.Fatalf("absent session must be the honest zero view, got %+v", v)
	}
	if v.Lines != 120 {
		t.Fatalf("Lines = %d, want the requested 120", v.Lines)
	}

	// No tmux server at all is the same honest absence, not an error (isNoServer precedent).
	tc2, _ := newCaptureFake(t, cannedRun{stderr: "no server running on /tmp/tmux-1000/default", err: errExit})
	v2, err := tc2.Tail(context.Background(), "designer", 120)
	if err != nil {
		t.Fatalf("no-server must not be an error, got %v", err)
	}
	if v2.Live {
		t.Fatalf("no-server must be Live=false, got %+v", v2)
	}
}

// Probe-first: when the membership probe misses, NO capture exec happens — the probe is the
// ONLY invocation. This keeps the prefix-match hazard structurally unreachable for absent
// sessions.
func TestTmuxCapture_ProbeFirst(t *testing.T) {
	tc, fx := newCaptureFake(t, cannedRun{stdout: "af-other\naf-third\n"})
	if _, err := tc.Tail(context.Background(), "designer", 50); err != nil {
		t.Fatalf("Tail: %v", err)
	}
	calls := fx.calls()
	if len(calls) != 1 {
		t.Fatalf("probe miss must yield exactly 1 exec (the probe), got %d: %v", len(calls), calls)
	}
	if calls[0][1] != "list-sessions" {
		t.Fatalf("the single exec must be the list-sessions probe, got %v", calls[0])
	}
}

// Capture-time stderr classification (design-doc L365 / Gotcha 4): the probe→capture race
// (session died in between) answers lowercase "can't find …" on tmux 3.4 and classifies as
// honest absence (nil error). Any OTHER failure — capture or probe — is a real error; the
// design-doc supersedes data.md's looser degrade-on-any-failure sketch, and the 8b434fc3
// honesty doctrine forbids silently degrading unexpected failures.
func TestTmuxCapture_StderrClassification(t *testing.T) {
	// can't find ⇒ absent, nil error.
	tc, _ := newCaptureFake(t,
		cannedRun{stdout: "af-designer\n"},
		cannedRun{stderr: "can't find session: af-designer", err: errExit},
	)
	v, err := tc.Tail(context.Background(), "designer", 120)
	if err != nil {
		t.Fatalf("the can't-find race must classify as absent, got err %v", err)
	}
	if v.Live {
		t.Fatalf("the can't-find race must be Live=false, got %+v", v)
	}

	// Any other capture stderr ⇒ error.
	tc2, _ := newCaptureFake(t,
		cannedRun{stdout: "af-designer\n"},
		cannedRun{stderr: "server exited unexpectedly", err: errExit},
	)
	if _, err := tc2.Tail(context.Background(), "designer", 120); err == nil {
		t.Fatalf("a non-can't-find capture failure must surface as an error")
	}

	// An unexpected PROBE failure (not no-server) is an error too — never a silent lie.
	tc3, _ := newCaptureFake(t, cannedRun{stderr: "permission denied", err: errExit})
	if _, err := tc3.Tail(context.Background(), "designer", 120); err == nil {
		t.Fatalf("an unexpected probe failure must surface as an error")
	}
}

// A successful capture returns honest content: stdout verbatim, the server clock stamp, and
// the requested line count.
func TestTmuxCapture_SuccessfulCapture_Content(t *testing.T) {
	tc, _ := newCaptureFake(t,
		cannedRun{stdout: "af-designer\n"},
		cannedRun{stdout: "line1\nline2"},
	)
	stamp := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	tc.now = func() time.Time { return stamp }
	v, err := tc.Tail(context.Background(), "designer", 42)
	if err != nil {
		t.Fatalf("Tail: %v", err)
	}
	if !v.Live {
		t.Fatalf("successful capture must be Live=true, got %+v", v)
	}
	if v.Output != "line1\nline2" {
		t.Fatalf("Output = %q, want the captured pane text verbatim", v.Output)
	}
	if !v.CapturedAt.Equal(stamp) {
		t.Fatalf("CapturedAt = %v, want the injected clock %v", v.CapturedAt, stamp)
	}
	if v.Lines != 42 {
		t.Fatalf("Lines = %d, want 42", v.Lines)
	}
}

// TailView marshals with the design-pinned snake_case keys (data.md D2-B; the assembled_at
// precedent, readmodel.go:75) — the JSON contract Phase 2's DetailView embeds.
func TestTmuxCapture_TailViewJSONShape(t *testing.T) {
	b, err := json.Marshal(TailView{
		Live:       true,
		Output:     "x",
		CapturedAt: time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC),
		Lines:      7,
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"live", "output", "captured_at", "lines"} {
		if _, ok := m[key]; !ok {
			t.Errorf("TailView JSON is missing the %q key (got %s)", key, b)
		}
	}
	if len(m) != 4 {
		t.Errorf("TailView JSON must have exactly the 4 design-pinned keys, got %s", b)
	}
}
