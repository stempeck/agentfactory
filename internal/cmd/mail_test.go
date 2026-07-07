package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/mail"
)

// setupWorktreeFixture creates a realistic worktree filesystem layout:
//
//	factoryRoot/.agentfactory/factory.json
//	factoryRoot/.agentfactory/agents.json  (contains agentName)
//	factoryRoot/.agentfactory/worktrees/wt-test/.agentfactory/.factory-root -> factoryRoot
//	factoryRoot/.agentfactory/worktrees/wt-test/.agentfactory/agents/<agentName>/
//
// Returns (factoryRoot, worktreeAgentDir).
func setupWorktreeFixture(t *testing.T, agentName string) (string, string) {
	t.Helper()
	factoryRoot := t.TempDir()

	// Factory-root config
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"`+agentName+`":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	// Worktree structure
	wtRoot := filepath.Join(afDir, "worktrees", "wt-test")
	wtAfDir := filepath.Join(wtRoot, ".agentfactory")
	wtAgentDir := filepath.Join(wtAfDir, "agents", agentName)
	os.MkdirAll(wtAgentDir, 0o755)

	// .factory-root redirect so FindFactoryRoot resolves to factoryRoot
	os.WriteFile(filepath.Join(wtAfDir, ".factory-root"), []byte(factoryRoot), 0o644)

	return factoryRoot, wtAgentDir
}

// setupFactoryFixture creates a standard (non-worktree) agent filesystem layout:
//
//	factoryRoot/.agentfactory/factory.json
//	factoryRoot/.agentfactory/agents.json  (contains agentName)
//	factoryRoot/.agentfactory/agents/<agentName>/
//
// Returns (factoryRoot, agentDir).
func setupFactoryFixture(t *testing.T, agentName string) (string, string) {
	t.Helper()
	factoryRoot := t.TempDir()

	afDir := filepath.Join(factoryRoot, ".agentfactory")
	agentDir := filepath.Join(afDir, "agents", agentName)
	os.MkdirAll(agentDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"`+agentName+`":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	return factoryRoot, agentDir
}

func TestDetectSender_WorktreeAgent(t *testing.T) {
	_, wtAgentDir := setupWorktreeFixture(t, "solver")

	got, err := detectSender(wtAgentDir)
	if err != nil {
		t.Fatalf("detectSender from worktree agent dir: %v", err)
	}
	if got != "solver" {
		t.Errorf("detectSender = %q, want %q", got, "solver")
	}
}

func TestDetectSender_FactoryAgent_NoRegression(t *testing.T) {
	_, agentDir := setupFactoryFixture(t, "manager")

	got, err := detectSender(agentDir)
	if err != nil {
		t.Fatalf("detectSender from factory agent dir: %v", err)
	}
	if got != "manager" {
		t.Errorf("detectSender = %q, want %q", got, "manager")
	}
}

// TestDetectSender_WrongButNoError_HonorsAF_ROLE pins the fix for GitHub
// issue #88 at the detectSender boundary. Pre-fix, a cwd at a typo directory
// raised "agent not found in agents.json" even when AF_ROLE was set correctly
// by session.Manager — because the membership check at the wrapper fired
// before the AND-gate could ever consult AF_ROLE.
func TestDetectSender_WrongButNoError_HonorsAF_ROLE(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "solver")

	got, err := detectSender(typoDir)
	if err != nil {
		t.Fatalf("detectSender with AF_ROLE fallback: %v", err)
	}
	if got != "solver" {
		t.Errorf("detectSender = %q, want %q (AF_ROLE overrides wrong path-derived name)", got, "solver")
	}
}

// TestDetectSender_WrongButNoError_NoAF_ROLE_Errors verifies that without
// AF_ROLE, detectSender errors clearly naming the membership failure.
func TestDetectSender_WrongButNoError_NoAF_ROLE_Errors(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "")

	got, err := detectSender(typoDir)
	if err == nil {
		t.Fatalf("detectSender should error for unknown agent, got %q", got)
	}
	if got == "typo" {
		t.Errorf("detectSender must not return wrong path-derived name %q silently", got)
	}
}

// setupMailSendFixture creates a two-member factory (alice, bob) including
// the messaging.json that mail.NewRouter hard-requires. The one-member
// helpers above predate cobra-level send coverage and write neither.
func setupMailSendFixture(t *testing.T) string {
	t.Helper()
	factoryRoot := t.TempDir()

	afDir := filepath.Join(factoryRoot, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"alice":{"type":"autonomous","description":"test agent"},"bob":{"type":"autonomous","description":"test agent"}}}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "messaging.json"), []byte(`{"groups":{}}`), 0o644)

	return factoryRoot
}

// resetMailSendFlags restores every `mail send` flag to its default and
// clears Changed. Flag state persists across rootCmd.Execute() calls (see
// the warning in install_test.go), so every Execute in this file must be
// followed by a reset or values leak into sibling tests.
func resetMailSendFlags(t *testing.T) {
	t.Helper()
	sendCmd, _, err := rootCmd.Find([]string{"mail", "send"})
	if err != nil {
		t.Fatalf("finding mail send command: %v", err)
	}
	for _, name := range []string{"from", "subject", "message", "priority", "reply-to"} {
		f := sendCmd.Flags().Lookup(name)
		if f == nil {
			continue
		}
		if err := f.Value.Set(f.DefValue); err != nil {
			t.Fatalf("resetting --%s: %v", name, err)
		}
		f.Changed = false
	}
}

// execMailSend drives `af mail send` through the real cobra root (the
// runInstallInDir pattern) with the mandatory flag reset afterwards.
func execMailSend(t *testing.T, args ...string) error {
	t.Helper()
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"mail", "send"}, args...))
	err := rootCmd.Execute()
	resetMailSendFlags(t)
	return err
}

// TestMailSend_FromFlag_SkipsDetectSender pins the --from contract: from the
// factory root (the one cwd where detectSender fails but factory-root
// discovery succeeds — the web tier's exact situation) an explicit member
// sender is used verbatim and auto-detection is never consulted.
func TestMailSend_FromFlag_SkipsDetectSender(t *testing.T) {
	factoryRoot := setupMailSendFixture(t)
	store := installMemStore(t)
	t.Chdir(factoryRoot)
	t.Setenv("AF_ROLE", "")

	if err := execMailSend(t, "bob", "-s", "s", "-m", "m", "--from", "alice"); err != nil {
		t.Fatalf("mail send --from alice from factory root: %v", err)
	}

	msgs, err := mail.NewMailbox("bob", store).List(context.Background())
	if err != nil {
		t.Fatalf("listing bob's mailbox: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("bob's mailbox has %d messages, want 1", len(msgs))
	}
	if msgs[0].From != "alice" {
		t.Errorf("stored From = %q, want %q", msgs[0].From, "alice")
	}
}

func TestMailSend_FromFlag_Validated(t *testing.T) {
	cases := []struct {
		name     string
		from     string
		wantErr  bool
		errPart  string
		wantFrom string
	}{
		{name: "shape reject", from: "bad name!", wantErr: true, errPart: `"bad name!"`},
		{name: "reserved reject", from: "dispatch", wantErr: true, errPart: `"dispatch"`},
		{name: "non-member reject", from: "ceo", wantErr: true, errPart: "not an agents.json member"},
		{name: "operator accept", from: "operator", wantFrom: "operator"},
		{name: "member accept", from: "alice", wantFrom: "alice"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			factoryRoot := setupMailSendFixture(t)
			store := installMemStore(t)
			t.Chdir(factoryRoot)
			t.Setenv("AF_ROLE", "")

			err := execMailSend(t, "bob", "-s", "s", "-m", "m", "--from", tc.from)

			msgs, listErr := mail.NewMailbox("bob", store).List(context.Background())
			if listErr != nil {
				t.Fatalf("listing bob's mailbox: %v", listErr)
			}
			if tc.wantErr {
				if err == nil {
					t.Fatalf("mail send --from %q should fail", tc.from)
				}
				if !strings.Contains(err.Error(), tc.errPart) {
					t.Errorf("error %q does not mention %s", err, tc.errPart)
				}
				if len(msgs) != 0 {
					t.Errorf("rejected send stored %d messages, want 0 (validation must precede Router.Send)", len(msgs))
				}
				return
			}
			if err != nil {
				t.Fatalf("mail send --from %q: %v", tc.from, err)
			}
			if len(msgs) != 1 {
				t.Fatalf("bob's mailbox has %d messages, want 1", len(msgs))
			}
			if msgs[0].From != tc.wantFrom {
				t.Errorf("stored From = %q, want %q", msgs[0].From, tc.wantFrom)
			}
		})
	}
}

// TestMailSend_NoFromFlag_UsesDetectSender pins the absent-flag regression
// contract: without --from, the send path resolves the sender exactly as
// today (getWd → detectSender), which hooks and Go-side subprocess callers
// depend on.
func TestMailSend_NoFromFlag_UsesDetectSender(t *testing.T) {
	factoryRoot := setupMailSendFixture(t)
	store := installMemStore(t)
	aliceDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "alice")
	if err := os.MkdirAll(aliceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(aliceDir)
	t.Setenv("AF_ROLE", "")

	if err := execMailSend(t, "bob", "-s", "s", "-m", "m"); err != nil {
		t.Fatalf("mail send without --from from alice's dir: %v", err)
	}

	msgs, err := mail.NewMailbox("bob", store).List(context.Background())
	if err != nil {
		t.Fatalf("listing bob's mailbox: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("bob's mailbox has %d messages, want 1", len(msgs))
	}
	if msgs[0].From != "alice" {
		t.Errorf("stored From = %q, want %q (detectSender path)", msgs[0].From, "alice")
	}
}

func TestDetectSender_WorktreeAgent_AF_ROLE_Fallback(t *testing.T) {
	// Set up a worktree where path-based detection works,
	// but also verify AF_ROLE is respected when paths fail.
	factoryRoot := t.TempDir()
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"solver":{"type":"autonomous","description":"test"}}}`), 0o644)

	// Create a worktree dir that has .factory-root but agent is NOT under
	// the standard .agentfactory/agents/ path — simulating a case where
	// path detection fails.
	wtRoot := filepath.Join(afDir, "worktrees", "wt-test")
	wtAfDir := filepath.Join(wtRoot, ".agentfactory")
	os.MkdirAll(wtAfDir, 0o755)
	os.WriteFile(filepath.Join(wtAfDir, ".factory-root"), []byte(factoryRoot), 0o644)

	// cwd is inside the worktree .agentfactory but NOT in agents/ subdir
	cwd := wtAfDir

	t.Setenv("AF_ROLE", "solver")

	got, err := detectSender(cwd)
	if err != nil {
		t.Fatalf("detectSender with AF_ROLE fallback: %v", err)
	}
	if got != "solver" {
		t.Errorf("detectSender = %q, want %q", got, "solver")
	}
}
