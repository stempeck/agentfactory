package cmd

import (
	"bytes"
	"os"
	"testing"

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
