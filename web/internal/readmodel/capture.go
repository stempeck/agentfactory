package readmodel

import (
	"bytes"
	"context"
	osexec "os/exec"
	"strconv"
	"strings"
	"time"
)

// TailView is the honest per-agent session snapshot. Live comes from the
// membership probe — NEVER from capture success (a prefix-matching capture of the wrong
// session would otherwise report a false live). Output is plain text ("" when !Live);
// CapturedAt is the server clock at capture (zero value when !Live); Lines echoes the
// requested line count on every path. The snake_case time.Time tag follows the assembled_at
// precedent (readmodel.go:75) — RFC3339 default marshaling is what the frontend's
// receipt-anchored age logic (Phase 3) expects.
type TailView struct {
	Live       bool      `json:"live"`
	Output     string    `json:"output"`
	CapturedAt time.Time `json:"captured_at"`
	Lines      int       `json:"lines"`
}

// TmuxCapture is the web-tier pane-snapshot reader, a sibling of TmuxLiveness: the same
// injectable execCommand seam (liveness.go:14-21) plus a private runCmd seam so tests can
// simulate exec outcomes as pure Go. The runCmd seam exists because the server lint raw-scans
// this package's test files for shell literals — canned tmux output can never come from a
// shell, and a run-func fake needs no process at all.
type TmuxCapture struct {
	execCommand func(ctx context.Context, name string, args ...string) *osexec.Cmd
	runCmd      func(cmd *osexec.Cmd) (stdout, stderr string, err error)
	now         func() time.Time
}

// NewTmuxCapture returns a TmuxCapture backed by the real tmux binary on PATH.
func NewTmuxCapture() *TmuxCapture {
	return &TmuxCapture{execCommand: osexec.CommandContext, runCmd: runBuffered, now: time.Now}
}

// runBuffered executes cmd with buffered stdout/stderr (the liveness.go:27-35 shape).
func runBuffered(cmd *osexec.Cmd) (string, string, error) {
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// Tail returns the last lines of the agent's pane as an honest TailView.
//
// It is probe-first: session membership is established by an EXACT string compare against
// `list-sessions` output before any capture, and the capture itself uses the exact-match
// target form — never a bare `-t af-<name>`, which tmux PREFIX-matches (verified on tmux 3.4:
// with only af-design-v7 running, `capture-pane -t af-design` exits 0 and silently captures
// the wrong agent's terminal — a cross-agent information leak). The `=` exact idiom mirrors
// the root module's HasSession (internal/tmux/tmux.go:249-255) and the #201 GC fix; the
// root's own CapturePane (tmux.go:493-498) still uses the bare form and is deliberately NOT
// copied. name is expected already validated/trimmed (the Phase-2 handler validates recipient
// names), so the probe's sessionName(name) and the capture target agree.
//
// Absent session and no-server both yield the honest zero view (Live=false, nil error) — the
// module's no-server-is-not-an-error posture (liveness.go:11-13). A capture-time "can't find"
// stderr (reachable only in the probe→capture race window) classifies as the same honest
// absence; any OTHER probe/capture failure is a real error — genuine errors are never
// silently swallowed as absence. lines is used as given: clamping it (default 120, 1-500)
// is the caller's responsibility, not this function's.
func (t *TmuxCapture) Tail(ctx context.Context, name string, lines int) (TailView, error) {
	absent := TailView{Lines: lines}

	probe := t.execCommand(ctx, "tmux", "list-sessions", "-F", "#{session_name}")
	out, stderr, err := t.runCmd(probe)
	if err != nil {
		if isNoServer(stderr) {
			return absent, nil
		}
		return TailView{}, err
	}
	session := sessionName(name) // the package's single session-name helper, kept consistent with the probe below
	present := false
	for _, s := range splitSessions(out) {
		if strings.TrimSpace(s) == session {
			present = true
			break
		}
	}
	if !present {
		return absent, nil
	}

	// '=' pins the exact session name; the ':' suffix pins the target-pane form (the
	// colon-less '=name' fails with "can't find pane" on tmux 3.4, verified). Plain text
	// only — never -e, which would let tmux interpret escape sequences in captured output.
	capture := t.execCommand(ctx, "tmux", "capture-pane", "-p", "-t", "=af-"+name+":", "-S", "-"+strconv.Itoa(lines))
	text, cstderr, err := t.runCmd(capture)
	if err != nil {
		if strings.Contains(strings.ToLower(cstderr), "can't find") {
			return absent, nil
		}
		return TailView{}, err
	}
	return TailView{Live: true, Output: text, CapturedAt: t.now(), Lines: lines}, nil
}
