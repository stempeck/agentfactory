package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
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

// mailSendFlagNames is the reset list resetMailSendFlags walks. It is a named
// var, not a literal inside the loop, so TestResetMailSendFlags_CoversEverySendFlag
// can prove it stayed complete: the helper skips names it cannot find, so a flag
// added to `mail send` and forgotten here leaks its value into sibling tests
// without any compile error.
var mailSendFlagNames = []string{"from", "subject", "message", "priority", "reply-to", "report-delivery"}

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
	for _, name := range mailSendFlagNames {
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
	_, err := execMailSendOut(t, args...)
	return err
}

// execMailSendOut is execMailSend plus the captured output, for the tests that
// assert on what the send actually printed.
func execMailSendOut(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	rootCmd.SetOut(&buf)
	rootCmd.SetErr(&buf)
	rootCmd.SetArgs(append([]string{"mail", "send"}, args...))
	err := rootCmd.Execute()
	resetMailSendFlags(t)
	return buf.String(), err
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

// TestResetMailSendFlags_CoversEverySendFlag is the interlock over Gotcha 6:
// resetMailSendFlags looks names up and silently skips the ones it cannot find,
// so an omission is invisible until an unrelated test starts failing for a
// reason that has nothing to do with it.
func TestResetMailSendFlags_CoversEverySendFlag(t *testing.T) {
	sendCmd, _, err := rootCmd.Find([]string{"mail", "send"})
	if err != nil {
		t.Fatalf("finding mail send command: %v", err)
	}
	if len(mailSendFlagNames) == 0 {
		t.Fatal("mailSendFlagNames is empty, so this scan would pass vacuously")
	}

	registered := map[string]bool{}
	sendCmd.Flags().VisitAll(func(f *pflag.Flag) {
		// cobra injects --help into the local set the first time the command
		// runs, so whether it is present depends on test order. It carries no
		// state worth resetting.
		if f.Name == "help" {
			return
		}
		registered[f.Name] = true
		if !slices.Contains(mailSendFlagNames, f.Name) {
			t.Errorf("flag --%s is registered on `mail send` but missing from mailSendFlagNames — "+
				"its value will leak into sibling tests", f.Name)
		}
	})
	for _, name := range mailSendFlagNames {
		if !registered[name] {
			t.Errorf("mailSendFlagNames lists --%s, which `mail send` does not register", name)
		}
	}
}

// TestMailSend_ReportDeliveryFlag_Registered pins the surviving delivery-report
// flag behaviourally rather than by grep: it exists, is an opt-in boolean, and is
// not required. (--no-wake was removed in PR #608; see TestMailSend_NoWakeFlag_Removed.)
func TestMailSend_ReportDeliveryFlag_Registered(t *testing.T) {
	sendCmd, _, err := rootCmd.Find([]string{"mail", "send"})
	if err != nil {
		t.Fatalf("finding mail send command: %v", err)
	}
	for _, name := range []string{"report-delivery"} {
		f := sendCmd.Flags().Lookup(name)
		if f == nil {
			t.Fatalf("flag --%s is not registered on `mail send`", name)
		}
		if f.Value.Type() != "bool" {
			t.Errorf("--%s has type %q, want bool", name, f.Value.Type())
		}
		if f.DefValue != "false" {
			t.Errorf("--%s defaults to %q, want \"false\" (the flags are opt-in)", name, f.DefValue)
		}
		if f.Usage == "" {
			t.Errorf("--%s has no usage string", name)
		}
		if ann := f.Annotations[cobra.BashCompOneRequiredFlag]; len(ann) > 0 && ann[0] == "true" {
			t.Errorf("--%s is marked required; it must stay optional", name)
		}
	}
}

// TestMailSend_NoWakeFlag_Removed pins the owner directive on PR #608: the `--no-wake`
// flag and its wake-suppression are gone, so `mail send` must not register the flag.
func TestMailSend_NoWakeFlag_Removed(t *testing.T) {
	sendCmd, _, err := rootCmd.Find([]string{"mail", "send"})
	if err != nil {
		t.Fatalf("finding mail send command: %v", err)
	}
	if f := sendCmd.Flags().Lookup("no-wake"); f != nil {
		t.Errorf("--no-wake is still registered on `mail send`; it must be removed (PR #608): mail must always wake")
	}
}

// TestMailSend_DefaultOutput_ByteIdentical is the phase's "no default behaviour
// change" requirement made mechanical. Nothing else in the repo pins this line,
// so without this test a regression in it would ship green.
func TestMailSend_DefaultOutput_ByteIdentical(t *testing.T) {
	factoryRoot := setupMailSendFixture(t)
	installMemStore(t)
	t.Chdir(factoryRoot)
	t.Setenv("AF_ROLE", "")

	out, err := execMailSendOut(t, "bob", "-s", "Subj", "-m", "Body", "--from", "alice")
	if err != nil {
		t.Fatalf("mail send: %v", err)
	}
	if out != "Sent to bob: Subj\n" {
		t.Errorf("stdout = %q, want %q", out, "Sent to bob: Subj\n")
	}
}

// TestMailSend_ReportDelivery_FiledForAbsentRecipient is cross-review C-1 made
// observable at the CLI: bob has no session, so the send must report that it
// filed the mail rather than claiming anyone was told. Reachable in the default
// build precisely because the tmux guard guarantees the absent branch.
func TestMailSend_ReportDelivery_FiledForAbsentRecipient(t *testing.T) {
	factoryRoot := setupMailSendFixture(t)
	store := installMemStore(t)
	t.Chdir(factoryRoot)
	t.Setenv("AF_ROLE", "")

	out, err := execMailSendOut(t, "bob", "-s", "Subj", "-m", "Body", "--from", "alice", "--report-delivery")
	if err != nil {
		t.Fatalf("mail send --report-delivery: %v", err)
	}
	if !strings.Contains(out, "Filed for bob: Subj") {
		t.Errorf("stdout = %q, want it to report the mail as filed", out)
	}
	for _, unwanted := range []string{"Sent to", "Notified"} {
		if strings.Contains(out, unwanted) {
			t.Errorf("stdout = %q, must not contain %q for an absent recipient", out, unwanted)
		}
	}

	msgs, err := mail.NewMailbox("bob", store).List(context.Background())
	if err != nil {
		t.Fatalf("listing bob's mailbox: %v", err)
	}
	if len(msgs) != 1 {
		t.Fatalf("bob's mailbox has %d messages, want 1", len(msgs))
	}
}

// TestDeliveryLine covers both report variants. The notified variant is
// unreachable through the cobra path in the default build (the tmux guard
// answers every liveness probe false), so the rendering is a pure function and
// is asserted as one; internal/mail's seam tests prove the Delivery values it
// is handed are real.
func TestDeliveryLine(t *testing.T) {
	cases := []struct {
		name string
		d    mail.Delivery
		want string
	}{
		{
			name: "notified",
			d:    mail.Delivery{Filed: true, Notified: true, Reason: "notified"},
			want: "Notified bob: Subj\n",
		},
		{
			name: "filed with reason",
			d:    mail.Delivery{Filed: true, Reason: "no session"},
			want: "Filed for bob: Subj (no session)\n",
		},
		{
			// A group that resolves to nobody but the sender files nothing, so
			// saying "filed" would claim a bead that does not exist.
			name: "nothing filed",
			d:    mail.Delivery{Reason: "no recipients"},
			want: "Not delivered to bob: Subj (no recipients)\n",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := deliveryLine("bob", "Subj", tc.d); got != tc.want {
				t.Errorf("deliveryLine = %q, want %q", got, tc.want)
			}
		})
	}
}
