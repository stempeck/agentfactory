//go:build !integration

package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// primeHookCapturing drives `af prime --hook` the way Claude Code's SessionStart hook does — payload
// on stdin, TMUX_PANE set so the pane guard admits the session — and returns raw stdout. It composes
// primeWithHookSession's stdin staging with runPrimeCapturing's buffer, because the assertions here
// are about the bytes the harness receives and neither existing helper returns them.
func primeHookCapturing(t *testing.T, payload string) string {
	t.Helper()
	t.Setenv("TMUX_PANE", "%0")

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

	primeHookMode = true
	defer func() { primeHookMode = false }()

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{}) // warnings belong on stderr and must not pollute the envelope
	if err := runPrime(cmd, nil); err != nil {
		t.Fatalf("af prime --hook: %v", err)
	}
	return out.String()
}

// primeSurfaceFixture is one provisioned agent with a real embedded role template and no formula:
// the state a session opens in.
func primeSurfaceFixture(t *testing.T) {
	t.Helper()
	_, agentDir := setupFactoryFixture(t, "manager")
	installMemStore(t)
	t.Chdir(agentDir)
	t.Setenv("AF_ROLE", "")
}

// TestPrimeHook_EmitsNoIdentity is #675 K1. At SessionStart the harness has already loaded the
// agent's CLAUDE.md — the same text the role template renders — so re-sending it is the largest
// redundant block on a surface the harness truncates. The header, the worktree block and the startup
// directive stay: no other carrier delivers them.
func TestPrimeHook_EmitsNoIdentity(t *testing.T) {
	primeSurfaceFixture(t)

	stdout := primeHookCapturing(t, `{"session_id":"sess-hook","source":"startup"}`)
	block := decodeAdditionalContext(t, stdout)

	if !strings.Contains(block, "[AGENT FACTORY] role:") {
		t.Errorf("hook output must still name the session, got:\n%s", block)
	}
	if strings.Contains(block, "# Agent Identity:") {
		t.Errorf("hook mode must not re-send the role template's identity heading, got:\n%s", block)
	}
	for _, heading := range []string{"## Workspace", "## Available Commands", "## Mail Protocol"} {
		if strings.Contains(block, heading) {
			t.Errorf("hook mode must not re-send the role template section %q, got:\n%s", heading, block)
		}
	}
	if !strings.Contains(block, "## Startup Directive") {
		t.Errorf("the startup directive has no other carrier and must survive hook mode, got:\n%s", block)
	}
	if n := strings.Count(strings.TrimSpace(stdout), "\n"); n != 0 {
		t.Errorf("hook mode must emit exactly ONE JSON object, stdout holds %d lines:\n%s", n+1, stdout)
	}
}

// TestPrimePlain_RendersIdentity is OD-1's keep-guarantee: a tool-result prime is the agent ASKING
// who it is, and it still gets the whole template, unenveloped.
func TestPrimePlain_RendersIdentity(t *testing.T) {
	primeSurfaceFixture(t)

	out := runPrimeCapturing(t)

	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("plain prime must not wrap its output in a hook envelope, got:\n%s", out)
	}
	for _, want := range []string{"[AGENT FACTORY] role:", "# Agent Identity:", "## Workspace", "## Startup Directive"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain prime is missing %q, got:\n%s", want, out)
		}
	}
}

// t1t12_armedSurfaceFixture is primeSurfaceFixture with the tokenomics umbrella armed, so the
// interview mechanism resolves ON and the re-prime slimming decision is live. It returns the factory
// root and the agent working dir so a test can read the session-keyed prime counter directly. The
// margin/floor are the same readable values the other tokenomics fixtures state.
func t1t12_armedSurfaceFixture(t *testing.T) (string, string) {
	t.Helper()
	root, agentDir := setupFactoryFixture(t, "manager")
	installMemStore(t)
	t.Chdir(agentDir)
	t.Setenv("AF_ROLE", "")
	armTokenomics(t, root, 10, 1)
	return root, agentDir
}

// TestPrimePlain_RendersIdentity_ArmOnAfterHookPrime is T1-a: OD-1's keep-guarantee STRENGTHENED. A
// plain (tool-result) prime is the agent asking who it is and must render the whole identity even
// when the interview arm is ON and a prior --hook prime already ran this session. Added as a sibling
// rather than folding this into TestPrimePlain_RendersIdentity, which is the arm-OFF baseline and is
// left untouched as its own protective assertion.
//
// RED at head: the hook prime bumps the session's prime count to 1 (identity withheld anyway by the
// hook gate), then this plain prime bumps it to 2, and 2 > 1 slims the identity — the OD-1 violation.
func TestPrimePlain_RendersIdentity_ArmOnAfterHookPrime(t *testing.T) {
	t1t12_armedSurfaceFixture(t)

	// A SessionStart hook prime first: it withholds identity by the hook gate but, at head, still
	// spends the session's one free identity render by counting itself.
	primeHookCapturing(t, `{"session_id":"sess-hook","source":"startup"}`)

	out := runPrimeCapturing(t)

	if strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("plain prime must not wrap its output in a hook envelope, got:\n%s", out)
	}
	for _, want := range []string{"[AGENT FACTORY] role:", "# Agent Identity:", "## Workspace", "## Startup Directive"} {
		if !strings.Contains(out, want) {
			t.Errorf("plain prime after a hook prime (arm on) is missing %q — the hook prime spent the identity render:\n%s", want, out)
		}
	}
}

// TestBumpPrimeCount_HookPrimeNotCounted is T1-c: the fix's mechanism directly. A --hook prime must
// not advance the session-keyed count that gates slimming, so a lone hook prime leaves the count at
// zero (nothing COUNTED has happened yet). RED at head, where the hook prime increments it to 1.
func TestBumpPrimeCount_HookPrimeNotCounted(t *testing.T) {
	_, agentDir := t1t12_armedSurfaceFixture(t)

	primeHookCapturing(t, `{"session_id":"sess-hook","source":"startup"}`)

	if got := loadPrimeCount(agentDir, "sess-hook").Primes; got != 0 {
		t.Errorf("a lone hook prime advanced the slim-gating count to %d; a hook prime must not be counted", got)
	}
}

// TestPrimeReprime_SlimsAfterFirstPlain is the protective (DO-NOT-CHANGE) companion to T1: the fix
// narrows the re-prime bug to plain re-primes but must NOT disable slimming. Two PLAIN primes in the
// same session with the arm on: the first renders full identity, the second slims it. This is the
// POST-FIX expectation and already holds at head (both primes are plain: count 1 then 2), so it is
// GREEN, not RED.
//
// NB: at head a slimmed re-prime withholds the worktree and startup blocks along with the identity
// heading (prime.go:306 gates all three on `primeHookMode || !slimIdentity`), so this asserts only
// that slimming FIRES — the identity heading disappears while the session-metadata header, which no
// slim ever drops, stays.
func TestPrimeReprime_SlimsAfterFirstPlain(t *testing.T) {
	t1t12_armedSurfaceFixture(t)

	first := runPrimeCapturing(t)
	if !strings.Contains(first, "# Agent Identity:") {
		t.Fatalf("the first plain prime withheld identity; slimming must fire only on a re-prime:\n%s", first)
	}

	second := runPrimeCapturing(t)
	if strings.Contains(second, "# Agent Identity:") {
		t.Errorf("a second plain prime in the same session (arm on) did not slim the identity heading:\n%s", second)
	}
	if !strings.Contains(second, "[AGENT FACTORY] role:") {
		t.Errorf("the slimmed re-prime dropped the session-metadata header, which no slim withholds:\n%s", second)
	}
}

// TestPrimeHook_OpenPipeStdinDoesNotBlock is T12-a: once the stdin guard is unified, the prime path
// gets the same non-block guarantee as mail. os.Stdin is a pipe read-end whose writer stays open and
// silent — the shape the harness reported hanging. RED at head: readHookPayloadFromStdin falls
// through the char-device guard to a blocking json.Decode.
func TestPrimeHook_OpenPipeStdinDoesNotBlock(t *testing.T) {
	primeSurfaceFixture(t)
	t.Setenv("TMUX_PANE", "%0")

	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	orig := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = orig; r.Close(); w.Close() })

	primeHookMode = true
	t.Cleanup(func() { primeHookMode = false })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	if !t1t12_runWithTimeout(t, 3*time.Second, func() { _ = runPrime(cmd, nil) }) {
		t.Fatal("runPrime --hook blocked on an open pipe stdin with no data")
	}
}

// TestPrimeHookHeader_UnderBudget bounds what prime costs a session that has no formula and no
// checkpoint. The budget is asserted on the ENCODED bytes because that is what the harness reads.
func TestPrimeHookHeader_UnderBudget(t *testing.T) {
	primeSurfaceFixture(t)

	const budget = 1024
	stdout := primeHookCapturing(t, `{"session_id":"sess-budget","source":"startup"}`)

	if len(stdout) == 0 {
		t.Fatal("hook mode emitted nothing; the header and startup directive are not optional")
	}
	if len(stdout) > budget {
		t.Errorf("hook-mode stdout is %d bytes, over the %d-byte budget:\n%s", len(stdout), budget, stdout)
	}
}
