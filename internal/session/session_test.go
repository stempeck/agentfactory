package session

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func TestNewManager(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/factory", "manager", entry)

	if mgr.SessionID() != "af-manager" {
		t.Errorf("SessionID = %q, want %q", mgr.SessionID(), "af-manager")
	}
}

func TestBuildStartupCommand_Interactive(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/factory", "manager", entry)

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Error("interactive command should contain --dangerously-skip-permissions")
	}
	if !strings.Contains(cmd, "claude") {
		t.Error("command should contain 'claude'")
	}
	if !strings.Contains(cmd, "AF_ROOT='/tmp/factory'") {
		t.Error("command should export AF_ROOT")
	}
	if !strings.Contains(cmd, "AF_ROLE='manager'") {
		t.Error("command should export AF_ROLE")
	}
	if !strings.Contains(cmd, "BD_ACTOR='manager'") {
		t.Error("command should export BD_ACTOR")
	}
	if !strings.Contains(cmd, "BEADS_DIR='/tmp/factory/.beads'") {
		t.Error("command should export BEADS_DIR")
	}
}

func TestBuildStartupCommand_Autonomous(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "supervisor", entry)

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Error("autonomous command should contain --dangerously-skip-permissions")
	}
}

func TestBuildNudge_WithDirective(t *testing.T) {
	entry := config.AgentEntry{
		Type:        "interactive",
		Description: "test",
		Directive:   "Read your memory and docs, and prove it.",
	}
	mgr := NewManager("/tmp/factory", "manager", entry)

	nudge := mgr.BuildNudge()

	if !strings.Contains(nudge, "Run `af prime` to check mail and begin work.") {
		t.Error("nudge should contain the base startup instruction")
	}
	if !strings.Contains(nudge, "Read your memory and docs, and prove it.") {
		t.Error("nudge should contain the custom directive")
	}
}

func TestBuildNudge_WithoutDirective(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/factory", "manager", entry)

	nudge := mgr.BuildNudge()

	if nudge != "Run `af prime` to check mail and begin work." {
		t.Errorf("nudge without directive = %q, want base instruction only", nudge)
	}
}

func TestStartNotProvisioned(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	// Use a directory that definitely doesn't exist
	mgr := NewManager("/tmp/af-test-nonexistent-factory-12345", "af-test-not-provisioned", entry)
	// Satisfy the ErrWorktreeNotSet precondition so the test reaches the
	// ErrNotProvisioned check; the worktree path is intentionally the same
	// nonexistent directory so workDir stays non-provisioned.
	if err := mgr.SetWorktree("/tmp/af-test-nonexistent-factory-12345/.worktrees/wt-test", "wt-test"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}

	err := mgr.Start()
	if err == nil {
		t.Fatal("expected error for non-provisioned workspace")
	}
	if !strings.Contains(err.Error(), "not provisioned") {
		t.Errorf("expected ErrNotProvisioned, got: %v", err)
	}
}

func TestStopNotRunning(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/factory", "af-test-not-running-agent", entry)

	err := mgr.Stop()
	if err == nil {
		t.Fatal("expected error for non-running session")
	}
	if !strings.Contains(err.Error(), "not running") {
		t.Errorf("expected ErrNotRunning, got: %v", err)
	}
}

func TestIsRunningNoSession(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/factory", "af-test-no-session-agent", entry)

	running, err := mgr.IsRunning()
	if err != nil {
		t.Fatalf("IsRunning: %v", err)
	}
	if running {
		t.Fatal("expected IsRunning to return false for non-existent session")
	}
}

func TestBuildStartupCommand_NoAPIKey(t *testing.T) {
	types := []string{"interactive", "autonomous"}
	for _, agentType := range types {
		t.Run(agentType, func(t *testing.T) {
			entry := config.AgentEntry{Type: agentType, Description: "test"}
			mgr := NewManager("/tmp/factory", "testagent", entry)
			cmd := mgr.BuildStartupCommand()

			if strings.Contains(cmd, "ANTHROPIC_API_KEY") {
				t.Errorf("startup command must not contain ANTHROPIC_API_KEY; Claude authenticates via CLI auth, got: %s", cmd)
			}
		})
	}
}

func TestQuickdockerNoAPIKey(t *testing.T) {
	// Find project root (two dirs up from internal/session/)
	projectRoot := filepath.Join("..", "..")
	scripts := []string{"quickdocker.sh", "test-container-bootstrap.sh"}

	for _, script := range scripts {
		t.Run(script, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join(projectRoot, script))
			if err != nil {
				t.Skipf("cannot read %s: %v", script, err)
			}
			if strings.Contains(string(data), "ANTHROPIC_API_KEY") {
				t.Errorf("%s must not reference ANTHROPIC_API_KEY; Claude authenticates via CLI auth, not API key", script)
			}
		})
	}
}

func TestBuildStartupCommand_WithInitialPrompt(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "ultraimplement", entry)
	mgr.SetInitialPrompt("implement issue #42")

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Error("command should contain --dangerously-skip-permissions")
	}
	if !strings.Contains(cmd, "implement issue #42") {
		t.Errorf("command should contain the task prompt, got: %s", cmd)
	}
	// Prompt must come after the claude command, not before
	claudeIdx := strings.Index(cmd, "claude")
	promptIdx := strings.Index(cmd, "implement issue #42")
	if promptIdx < claudeIdx {
		t.Error("prompt must come after claude command")
	}
}

func TestBuildStartupCommand_WithoutInitialPrompt_Unchanged(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "ultraimplement", entry)

	cmd := mgr.BuildStartupCommand()

	expected := "export AF_ROOT='/tmp/factory' AF_ROLE='ultraimplement' BD_ACTOR='ultraimplement' BEADS_DIR='/tmp/factory/.beads' && claude --dangerously-skip-permissions"
	if cmd != expected {
		t.Errorf("command without prompt should be unchanged.\ngot:  %s\nwant: %s", cmd, expected)
	}
}

func TestBuildStartupCommand_PromptWithQuotes(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "ultraimplement", entry)
	mgr.SetInitialPrompt(`implement "issue #42" with 'special' chars`)

	cmd := mgr.BuildStartupCommand()

	// The prompt should be shell-safe (not break the command)
	if !strings.Contains(cmd, "claude") {
		t.Error("command should still contain claude")
	}
	if !strings.Contains(cmd, "--dangerously-skip-permissions") {
		t.Error("command should still contain --dangerously-skip-permissions")
	}
	// Should contain the prompt text (possibly escaped)
	if !strings.Contains(cmd, "implement") {
		t.Error("command should contain the prompt content")
	}
}

func TestBuildNudge_SkippedWithInitialPrompt(t *testing.T) {
	entry := config.AgentEntry{
		Type:        "autonomous",
		Description: "test",
		Directive:   "Run af prime to load formula context.",
	}
	mgr := NewManager("/tmp/factory", "ultraimplement", entry)
	mgr.SetInitialPrompt("implement issue #42")

	nudge := mgr.BuildNudge()

	// When an initial prompt is set, nudge should be empty (task is delivered via CLI arg)
	if nudge != "" {
		t.Errorf("nudge should be empty when initial prompt is set, got: %q", nudge)
	}
}

func TestShellQuote(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "simple string",
			input: "hello world",
			want:  "'hello world'",
		},
		{
			name:  "string with single quotes",
			input: "it's a test",
			want:  "'it'\\''s a test'",
		},
		{
			name:  "string with double quotes",
			input: `say "hello"`,
			want:  `'say "hello"'`,
		},
		{
			name:  "empty string",
			input: "",
			want:  "''",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellQuote(tt.input)
			if got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestShellQuote_ShellMetachars(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "backticks",
			input: "run `cmd`",
			want:  "'run `cmd`'",
		},
		{
			name:  "dollar expansion",
			input: "$(rm -rf /)",
			want:  "'$(rm -rf /)'",
		},
		{
			name:  "newlines",
			input: "line1\nline2",
			want:  "'line1\nline2'",
		},
		{
			name:  "semicolons and pipes",
			input: "cmd; rm -rf / | cat",
			want:  "'cmd; rm -rf / | cat'",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := shellQuote(tt.input)
			if got != tt.want {
				t.Errorf("shellQuote(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestBuildStartupCommand_PromptWithShellMetachars(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "ultraimplement", entry)
	mgr.SetInitialPrompt("fix $(echo hack) and `rm -rf /` issues; drop table")

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "claude --dangerously-skip-permissions") {
		t.Error("command should contain claude invocation")
	}
	// The dangerous strings should be safely quoted (inside single quotes)
	if !strings.Contains(cmd, "$(echo hack)") {
		t.Error("command should contain the prompt content")
	}
	// Verify the prompt is single-quoted (not bare)
	if strings.Contains(cmd, "claude --dangerously-skip-permissions $(") {
		t.Error("prompt should be quoted, not bare — shell expansion possible")
	}
}

func TestBuildStartupCommand_FactoryRootWithSpaces(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/my factory", "ultraimplement", entry)

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "AF_ROOT='/tmp/my factory'") {
		t.Errorf("factory root with spaces should be quoted, got: %s", cmd)
	}
	if !strings.Contains(cmd, "BEADS_DIR='/tmp/my factory/.beads'") {
		t.Errorf("beads dir with spaces should be quoted, got: %s", cmd)
	}
}

func TestBuildStartupCommand_AllAgentsGetPermissionsFlag(t *testing.T) {
	types := []string{"interactive", "autonomous"}
	for _, agentType := range types {
		t.Run(agentType, func(t *testing.T) {
			entry := config.AgentEntry{Type: agentType, Description: "test"}
			mgr := NewManager("/tmp/factory", "testagent", entry)
			cmd := mgr.BuildStartupCommand()

			if !strings.Contains(cmd, "--dangerously-skip-permissions") {
				t.Errorf("%s agent command should contain --dangerously-skip-permissions, got: %s", agentType, cmd)
			}
		})
	}
}

func TestSetWorktree_WorkDirOverride(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/factory", "researcher", entry)

	// Before SetWorktree, workDir should return factory agent dir
	cmdBefore := mgr.BuildStartupCommand()
	_ = cmdBefore // used below after we verify workDir indirectly

	// Set worktree
	if err := mgr.SetWorktree("/tmp/factory/.worktrees/wt-abc123", "wt-abc123"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}

	// After SetWorktree, workDir should return worktree agent dir
	// We test this indirectly through Start() which calls workDir(),
	// but more directly through BuildStartupCommand (which doesn't use workDir).
	// The real test is that workDir() returns the worktree path.
	// Since workDir() is unexported, we test via the exported WorkDir() accessor.
	got := mgr.WorkDir()
	want := "/tmp/factory/.worktrees/wt-abc123/.agentfactory/agents/researcher"
	if got != want {
		t.Errorf("WorkDir() after SetWorktree = %q, want %q", got, want)
	}
}

func TestSetWorktree_WorkDirDefault(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/factory", "researcher", entry)

	// Without SetWorktree, workDir should return factory agent dir
	got := mgr.WorkDir()
	want := "/tmp/factory/.agentfactory/agents/researcher"
	if got != want {
		t.Errorf("WorkDir() without SetWorktree = %q, want %q", got, want)
	}
}

func TestBuildStartupCommand_WithWorktree(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "researcher", entry)
	if err := mgr.SetWorktree("/tmp/factory/.worktrees/wt-abc123", "wt-abc123"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "AF_WORKTREE='/tmp/factory/.worktrees/wt-abc123'") {
		t.Errorf("command should export AF_WORKTREE, got: %s", cmd)
	}
	if !strings.Contains(cmd, "AF_WORKTREE_ID='wt-abc123'") {
		t.Errorf("command should export AF_WORKTREE_ID, got: %s", cmd)
	}
	// Base vars should still be present
	if !strings.Contains(cmd, "AF_ROOT='/tmp/factory'") {
		t.Errorf("command should still export AF_ROOT, got: %s", cmd)
	}
	if !strings.Contains(cmd, "AF_ROLE='researcher'") {
		t.Errorf("command should still export AF_ROLE, got: %s", cmd)
	}
}

func TestBuildStartupCommand_WithoutWorktree_NoWorktreeVars(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "researcher", entry)

	cmd := mgr.BuildStartupCommand()

	if strings.Contains(cmd, "AF_WORKTREE") {
		t.Errorf("command without worktree should NOT contain AF_WORKTREE, got: %s", cmd)
	}
	if strings.Contains(cmd, "AF_WORKTREE_ID") {
		t.Errorf("command without worktree should NOT contain AF_WORKTREE_ID, got: %s", cmd)
	}
}

func TestBuildStartupCommand_WorktreePathWithSpaces(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/my factory", "researcher", entry)
	if err := mgr.SetWorktree("/tmp/my factory/.worktrees/wt-abc123", "wt-abc123"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "AF_WORKTREE='/tmp/my factory/.worktrees/wt-abc123'") {
		t.Errorf("worktree path with spaces should be quoted, got: %s", cmd)
	}
}

func TestStartAndStop(t *testing.T) {
	if os.Getenv("AF_INTEGRATION_TEST") == "" {
		t.Skip("set AF_INTEGRATION_TEST=1 to run integration tests")
	}

	// Create a temp workspace with a worktree-style agent dir so the Phase 3.5
	// ErrWorktreeNotSet guard is satisfied and workDir() still resolves to a
	// provisioned directory.
	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")
	agentDir := filepath.Join(wtPath, ".agentfactory", "agents", "testagent")
	if err := os.MkdirAll(agentDir, 0755); err != nil {
		t.Fatalf("creating agent dir: %v", err)
	}

	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager(tmpDir, "testagent", entry)
	if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
		t.Fatalf("SetWorktree: %v", err)
	}

	// Start — should create session (Claude won't actually launch in test, but session will exist)
	// Note: This will timeout on WaitForCommand since Claude isn't installed in test env.
	// The important thing is the session gets created.
	_ = mgr.Start()

	// Check running
	running, _ := mgr.IsRunning()
	if !running {
		t.Skip("session did not start — tmux may not be available")
	}

	// Stop
	if err := mgr.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	running, _ = mgr.IsRunning()
	if running {
		t.Fatal("session still running after Stop")
	}
}

func TestStart_ErrorsWithoutWorktree(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/af-test-no-worktree-factory", "af-test-no-worktree-agent", entry)

	err := mgr.Start()
	if err == nil {
		t.Fatal("expected error when Start is called before SetWorktree")
	}
	if !errors.Is(err, ErrWorktreeNotSet) {
		t.Errorf("expected ErrWorktreeNotSet, got: %v", err)
	}

	running, _ := mgr.IsRunning()
	if running {
		t.Error("no tmux session should be created when Start fails on precondition")
	}
}

func TestSetWorktree_RejectsEmptyPath(t *testing.T) {
	entry := config.AgentEntry{Type: "interactive", Description: "test"}
	mgr := NewManager("/tmp/factory", "agent", entry)

	err := mgr.SetWorktree("", "any-id")
	if err == nil {
		t.Fatal("expected error when SetWorktree is called with empty path")
	}
	if !strings.Contains(err.Error(), "path must not be empty") {
		t.Errorf("expected error containing %q, got: %v", "path must not be empty", err)
	}

	// Confirm worktreePath was not mutated: WorkDir() should still return the
	// factory-root-scoped agent dir, not a worktree-scoped one.
	got := mgr.WorkDir()
	want := "/tmp/factory/.agentfactory/agents/agent"
	if got != want {
		t.Errorf("WorkDir() after rejected SetWorktree = %q, want %q", got, want)
	}
}
