package cmd

import (
	"bytes"
	"testing"

	"github.com/spf13/cobra"
)

// K9 (cross-review C2): the documented `af up manager` is POSITIONAL, but the
// dispatcher auto-start was gated to the blanket `af up` path only — so the
// "just tag an issue, never visit the manager" promise broke on the documented
// setup path. Hoisting StartDispatch out of the blanket gate makes the positional
// path also auto-start the dispatcher (idempotent). This pins it.
func TestRunUp_PositionalArg_AutoStartsDispatcher(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"manager":{"type":"interactive","description":"m"}}}`)
	writeAFFile(t, root, "startup.json", `{"start_dispatch":true}`)
	writeAFFile(t, root, "dispatch.json",
		`{"repos":["t/r"],"trigger_label":"agentic","mappings":[{"label":"x","agent":"manager"}],"interval_seconds":300}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, []string{"manager"})

	if !opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("positional `af up manager` must auto-start the dispatcher (K9); ops=%v out=%q", fake.ops, buf.String())
	}
}

// K9: the positional auto-start is idempotent — a second `af up manager` with the
// dispatcher already running is a benign no-op (no new session), not an error.
func TestRunUp_PositionalArg_DispatcherAlreadyRunning_NoOp(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"manager":{"type":"interactive","description":"m"}}}`)
	writeAFFile(t, root, "startup.json", `{"start_dispatch":true}`)
	writeAFFile(t, root, "dispatch.json",
		`{"repos":["t/r"],"trigger_label":"agentic","mappings":[{"label":"x","agent":"manager"}],"interval_seconds":300}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)
	fake.present[dispatchSessionName] = true // already running

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, []string{"manager"})

	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("an already-running dispatcher must NOT be re-created on the positional path; ops=%v", fake.ops)
	}
}
