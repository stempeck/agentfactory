package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/telemetry"
)

// primeWithHookPayload fires `af prime --hook` with an arbitrary payload on stdin and WITHOUT
// touching TMUX_PANE, so a caller can stage either side of the pane guard. primeWithHookSession is
// the agent-shaped convenience over the same path; this one exists because the grader's defining
// property is the absence of the variable that helper sets.
func primeWithHookPayload(t *testing.T, payload string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	if _, err := w.WriteString(payload); err != nil {
		t.Fatalf("write hook payload: %v", err)
	}
	w.Close()

	orig := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = orig; r.Close() }()

	if err := runPrimeInFixture(t); err != nil {
		t.Fatalf("af prime --hook: %v", err)
	}
}

func readRuntime(t *testing.T, workDir, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", name))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// TestPrimeHookIgnoresGraderSessions is #678 K1's measurement-integrity guard, and the bug it pins
// was found in a live factory rather than imagined.
//
// The quality gates evaluate a turn by running `claude -p --model haiku` from the agent's OWN
// working directory (hooks/fidelity-gate.sh:270-278) under `env -i HOME PATH` plus a short OTel
// allowlist. That is a real Claude Code session, so it fires the SessionStart hook, so it ran
// `af prime --hook` in the agent's directory — and af wrote the GRADER's session id into
// .runtime/session_id. Everything downstream then measured the wrong session: the step's generation
// figures are session-guarded (telemetry_generation.go) and went nil, and the grader's session
// counted as one more session the step had crossed. The steps that were graded hardest scored
// worst, which is exactly backwards.
//
// TMUX_PANE presence is the discriminator, and the assertions below are written around WHY it works
// rather than around the variable: the agent's Claude Code runs inside a tmux pane and passes the
// variable to its hooks, and `env -i` cannot pass down what it does not carry.
func TestPrimeHookIgnoresGraderSessions(t *testing.T) {
	setHookMode := func(t *testing.T) {
		t.Helper()
		orig := primeHookMode
		primeHookMode = true
		t.Cleanup(func() { primeHookMode = orig })
	}

	t.Run("a grader session claims neither the identity nor a record", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		epic, _ := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		setHookMode(t)

		// The agent claims its identity first, from inside its pane. Without this the test could
		// pass by writing nothing at all, and the failure it exists to catch is an OVERWRITE.
		primeWithHookSession(t, "sess-agent")
		if got := readRuntime(t, fx.workDir, "session_id"); got != "sess-agent" {
			t.Fatalf("fixture: the agent's own hook did not claim the identity, got %q", got)
		}
		before := countEvents(t, fx.root, fx.agent, telemetry.EventSessionStart)

		// Now the grader: same directory, same hook, no pane. Set empty rather than unset because
		// the guard reads os.Getenv, which cannot tell the two apart, and t.Setenv restores whatever
		// the runner's own environment had.
		t.Setenv("TMUX_PANE", "")
		primeWithHookPayload(t, `{"session_id":"sess-grader","transcript_path":"/tmp/grader.jsonl"}`)

		if got := readRuntime(t, fx.workDir, "session_id"); got != "sess-agent" {
			t.Errorf("session_id = %q, want %q; a grader session took the agent's identity and every "+
				"generation figure for the step is now session-guarded against the wrong session", got, "sess-agent")
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventSessionStart); got != before {
			t.Errorf("session_start count = %d, want %d; a grader session was counted as one the step "+
				"crossed, which inflates SessionsPerStep for exactly the steps that were graded", got, before)
		}
		// Contains and not equality: the marker is <sessionID>\t<path>, so an equality check against
		// the bare path would pass even if the grader's answer had been written.
		if got := readRuntime(t, fx.workDir, "transcript_path"); strings.Contains(got, "/tmp/grader.jsonl") {
			t.Errorf("transcript_path = %q; the grader's transcript path was persisted, and af done "+
				"would derive this step's generation figures from a haiku grading run rather than "+
				"from the agent's own work", got)
		}
	})

	t.Run("the agent's own hook in its own pane still claims both", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		gateOn(t, fx.root)
		epic, _ := seedFormulaBeads(t, fx)
		writeRuntimeFile(t, fx.workDir, "hooked_formula", epic.ID)
		setHookMode(t)

		t.Setenv("TMUX_PANE", "%7")
		primeWithHookPayload(t, `{"session_id":"sess-real","transcript_path":"/tmp/real.jsonl"}`)

		if got := readRuntime(t, fx.workDir, "session_id"); got != "sess-real" {
			t.Errorf("session_id = %q, want %q; the guard is refusing the agent's own session, which "+
				"would leave every run unmeasured rather than mismeasured", got, "sess-real")
		}
		// The marker carries the session it belongs to, so a later session that reports no
		// transcript_path falls back to the derivation instead of inheriting this one.
		if got, want := readRuntime(t, fx.workDir, "transcript_path"), "sess-real\t/tmp/real.jsonl"; got != want {
			t.Errorf("transcript_path = %q, want %q", got, want)
		}
		if got := countEvents(t, fx.root, fx.agent, telemetry.EventSessionStart); got != 1 {
			t.Errorf("session_start count = %d, want 1", got)
		}
	})

	t.Run("the refusal is loud", func(t *testing.T) {
		// Declining a session silently is the failure mode this whole test is about, one level up: an
		// operator running claude directly in an agent workspace is legitimately declined too, and
		// has no way to learn why unless the guard says so. done.go:487-494 takes the same trade and
		// warns for the same reason.
		t.Setenv("TMUX_PANE", "")
		stderr := captureStderr(t, func() {
			if hookRunsInAgentPane() {
				t.Error("the guard admitted a hook firing with no pane")
			}
		})
		if !strings.Contains(stderr, "tmux pane") {
			t.Errorf("the declined claim printed %q, which does not name the reason", stderr)
		}
	})
}
