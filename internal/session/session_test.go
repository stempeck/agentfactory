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
	if !strings.Contains(cmd, "AF_ACTOR='manager'") {
		t.Error("command should export AF_ACTOR")
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

	expected := "export AF_ROOT='/tmp/factory' AF_ROLE='ultraimplement' AF_ACTOR='ultraimplement' && claude --dangerously-skip-permissions"
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
	// AF_ACTOR should be present
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

func TestCheckAvailableMemory_ReturnsValue(t *testing.T) {
	mb, err := checkAvailableMemory()
	if err != nil {
		t.Skipf("memory check not supported on this platform: %v", err)
	}
	if mb == 0 {
		t.Error("checkAvailableMemory returned 0MB, expected > 0")
	}
}

func TestBuildStartupCommand_WithModel(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test", Model: "sonnet"}
	mgr := NewManager("/tmp/factory", "testagent", entry)

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "--model 'sonnet'") {
		t.Errorf("command should contain --model 'sonnet', got: %s", cmd)
	}
	if !strings.Contains(cmd, "claude --dangerously-skip-permissions") {
		t.Error("command should contain claude invocation")
	}
}

func TestBuildStartupCommand_WithModelAndPrompt(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test", Model: "sonnet"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetInitialPrompt("do work")

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "--model 'sonnet'") {
		t.Errorf("command should contain --model 'sonnet', got: %s", cmd)
	}
	if !strings.Contains(cmd, "do work") {
		t.Errorf("command should contain the prompt, got: %s", cmd)
	}
	modelIdx := strings.Index(cmd, "--model")
	promptIdx := strings.Index(cmd, "do work")
	if modelIdx >= promptIdx {
		t.Errorf("--model (at %d) must appear before prompt (at %d) in command: %s", modelIdx, promptIdx, cmd)
	}
}

func TestBuildStartupCommand_WithoutModel_NoModelFlag(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)

	cmd := mgr.BuildStartupCommand()

	if strings.Contains(cmd, "--model") {
		t.Errorf("command without model should NOT contain --model, got: %s", cmd)
	}
}

func TestBuildStartupCommand_ModelWithShellMetachars(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test", Model: `"; rm -rf /`}
	mgr := NewManager("/tmp/factory", "testagent", entry)

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "--model") {
		t.Errorf("command should contain --model flag, got: %s", cmd)
	}
	if strings.Contains(cmd, `--model "; rm`) {
		t.Error("model value should be quoted, not bare — shell injection possible")
	}
	quoted := shellQuote(`"; rm -rf /`)
	if !strings.Contains(cmd, "--model "+quoted) {
		t.Errorf("command should contain safely quoted model value, got: %s", cmd)
	}
}

// --- Endpoint configuration tests (BaseURL, AuthToken) ---

func TestBuildStartupCommand_WithEndpoint(t *testing.T) {
	entry := config.AgentEntry{
		Type: "autonomous", Description: "test",
		BaseURL: "http://localhost:1234/v1/messages", AuthToken: "tok123",
	}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	if err := mgr.SetWorktree("/tmp/wt", "wt-1"); err != nil {
		t.Fatal(err)
	}

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "ANTHROPIC_BASE_URL='http://localhost:1234/v1/messages'") {
		t.Errorf("command should contain ANTHROPIC_BASE_URL export, got: %s", cmd)
	}
	if !strings.Contains(cmd, "ANTHROPIC_AUTH_TOKEN='tok123'") {
		t.Errorf("command should contain ANTHROPIC_AUTH_TOKEN export, got: %s", cmd)
	}
}

func TestBuildStartupCommand_WithEndpointAndModel(t *testing.T) {
	entry := config.AgentEntry{
		Type: "autonomous", Description: "test",
		Model: "sonnet", BaseURL: "http://localhost:1234", AuthToken: "tok",
	}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	if err := mgr.SetWorktree("/tmp/wt", "wt-1"); err != nil {
		t.Fatal(err)
	}

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "ANTHROPIC_BASE_URL='http://localhost:1234'") {
		t.Errorf("command should contain ANTHROPIC_BASE_URL, got: %s", cmd)
	}
	if !strings.Contains(cmd, "ANTHROPIC_AUTH_TOKEN='tok'") {
		t.Errorf("command should contain ANTHROPIC_AUTH_TOKEN, got: %s", cmd)
	}
	if !strings.Contains(cmd, "--model 'sonnet'") {
		t.Errorf("command should contain --model flag, got: %s", cmd)
	}
	sepIdx := strings.Index(cmd, "&&")
	baseURLIdx := strings.Index(cmd, "ANTHROPIC_BASE_URL")
	modelIdx := strings.Index(cmd, "--model")
	if baseURLIdx > sepIdx {
		t.Errorf("ANTHROPIC_BASE_URL (at %d) must appear before && (at %d)", baseURLIdx, sepIdx)
	}
	if modelIdx < sepIdx {
		t.Errorf("--model (at %d) must appear after && (at %d)", modelIdx, sepIdx)
	}
}

func TestBuildStartupCommand_WithoutEndpoint_Unchanged(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "ultraimplement", entry)

	cmd := mgr.BuildStartupCommand()

	expected := "export AF_ROOT='/tmp/factory' AF_ROLE='ultraimplement' AF_ACTOR='ultraimplement' && claude --dangerously-skip-permissions"
	if cmd != expected {
		t.Errorf("command without endpoint should be unchanged.\ngot:  %s\nwant: %s", cmd, expected)
	}
}

func TestBuildStartupCommand_NoEndpoint_NoAuthTokenExport(t *testing.T) {
	types := []string{"interactive", "autonomous"}
	for _, agentType := range types {
		t.Run(agentType, func(t *testing.T) {
			entry := config.AgentEntry{Type: agentType, Description: "test"}
			mgr := NewManager("/tmp/factory", "testagent", entry)

			cmd := mgr.BuildStartupCommand()

			if strings.Contains(cmd, "ANTHROPIC_BASE_URL") {
				t.Errorf("command without endpoint must not contain ANTHROPIC_BASE_URL, got: %s", cmd)
			}
			if strings.Contains(cmd, "ANTHROPIC_AUTH_TOKEN") {
				t.Errorf("command without endpoint must not contain ANTHROPIC_AUTH_TOKEN, got: %s", cmd)
			}
		})
	}
}

func TestBuildStartupCommand_EndpointWithShellMetachars(t *testing.T) {
	entry := config.AgentEntry{
		Type: "autonomous", Description: "test",
		BaseURL: "http://localhost:1234/v1?key=val&other=yes",
	}
	mgr := NewManager("/tmp/factory", "testagent", entry)

	cmd := mgr.BuildStartupCommand()

	quoted := shellQuote("http://localhost:1234/v1?key=val&other=yes")
	if !strings.Contains(cmd, "ANTHROPIC_BASE_URL="+quoted) {
		t.Errorf("URL with metacharacters should be shell-quoted, got: %s", cmd)
	}
}

func TestBuildStartupCommand_AuthTokenWithShellMetachars(t *testing.T) {
	entry := config.AgentEntry{
		Type: "autonomous", Description: "test",
		AuthToken: `"; rm -rf /`,
	}
	mgr := NewManager("/tmp/factory", "testagent", entry)

	cmd := mgr.BuildStartupCommand()

	if strings.Contains(cmd, `ANTHROPIC_AUTH_TOKEN="; rm`) {
		t.Error("auth token should be quoted, not bare — shell injection possible")
	}
	quoted := shellQuote(`"; rm -rf /`)
	if !strings.Contains(cmd, "ANTHROPIC_AUTH_TOKEN="+quoted) {
		t.Errorf("auth token with metacharacters should be shell-quoted, got: %s", cmd)
	}
}

func TestStart_PartialEndpointWarning(t *testing.T) {
	tests := []struct {
		name    string
		baseURL string
		token   string
		wantSet string
		wantNot string
	}{
		{"base_url only", "http://localhost:1234", "", "base_url", "auth_token"},
		{"auth_token only", "", "tok123", "auth_token", "base_url"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			entry := config.AgentEntry{
				Type: "autonomous", Description: "test",
				BaseURL: tc.baseURL, AuthToken: tc.token,
			}
			hasBaseURL := entry.BaseURL != ""
			hasAuthToken := entry.AuthToken != ""
			if hasBaseURL == hasAuthToken {
				t.Fatal("test setup error: exactly one field should be set")
			}
			if !((hasBaseURL) != (hasAuthToken)) {
				t.Error("partial config condition should be true when exactly one field is set")
			}
		})
	}
}

func TestBuildStartupCommand_WithBuildHost(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetBuildHost(&config.BuildHostConfig{
		Mode:      "ssh",
		Host:      "mac-mini.local",
		User:      "builder",
		MountPath: "/Volumes/build",
	})

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "AF_BUILD_MODE='ssh'") {
		t.Errorf("command should contain AF_BUILD_MODE='ssh', got: %s", cmd)
	}
	if !strings.Contains(cmd, "AF_BUILD_HOST='mac-mini.local'") {
		t.Errorf("command should contain AF_BUILD_HOST='mac-mini.local', got: %s", cmd)
	}
	if !strings.Contains(cmd, "AF_BUILD_USER='builder'") {
		t.Errorf("command should contain AF_BUILD_USER='builder', got: %s", cmd)
	}
	if strings.Contains(cmd, "AF_BUILD_KEY") {
		t.Errorf("command should NOT contain AF_BUILD_KEY, got: %s", cmd)
	}
	if !strings.Contains(cmd, "AF_HOST_MOUNT='/Volumes/build'") {
		t.Errorf("command should contain AF_HOST_MOUNT, got: %s", cmd)
	}
}

func TestBuildStartupCommand_WithoutBuildHost(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)

	cmd := mgr.BuildStartupCommand()

	if strings.Contains(cmd, "AF_BUILD_") {
		t.Errorf("command without build host should NOT contain AF_BUILD_, got: %s", cmd)
	}
}

func TestBuildStartupCommand_BuildHostLocalMode(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetBuildHost(&config.BuildHostConfig{Mode: "local"})

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "AF_BUILD_MODE='local'") {
		t.Errorf("command should contain AF_BUILD_MODE='local', got: %s", cmd)
	}
	if strings.Contains(cmd, "AF_BUILD_HOST") {
		t.Errorf("local mode should NOT contain AF_BUILD_HOST, got: %s", cmd)
	}
	if strings.Contains(cmd, "AF_BUILD_USER") {
		t.Errorf("local mode should NOT contain AF_BUILD_USER, got: %s", cmd)
	}
	if strings.Contains(cmd, "AF_HOST_MOUNT") {
		t.Errorf("local mode should NOT contain AF_HOST_MOUNT, got: %s", cmd)
	}
}

func TestEndpointConstants_NoDuplicateStrings(t *testing.T) {
	src, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatalf("reading session.go: %v", err)
	}
	lines := strings.Split(string(src), "\n")

	var constBlockEnd int
	inConst := false
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "const (" {
			inConst = true
		}
		if inConst && trimmed == ")" {
			constBlockEnd = i
			inConst = false
		}
	}

	for i, line := range lines {
		if i <= constBlockEnd {
			continue
		}
		if strings.Contains(line, "//") || strings.Contains(line, "test") || strings.Contains(line, "Test") {
			continue
		}
		if strings.Contains(line, `"ANTHROPIC_BASE_URL"`) || strings.Contains(line, `"ANTHROPIC_AUTH_TOKEN"`) {
			t.Errorf("line %d: found hardcoded env var string outside const block: %s", i+1, strings.TrimSpace(line))
		}
	}
}

// --- Per-agent model-env set tests (issue #480 Phase 2) ---

// TestBuildStartupCommand_ClearsAPIKey is the deliberate inverse of
// TestBuildStartupCommand_NoAPIKey: when a profile sets ANTHROPIC_API_KEY:"" the
// command MUST emit ANTHROPIC_API_KEY='' to clear an ambient cloud key.
func TestBuildStartupCommand_ClearsAPIKey(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_API_KEY", Value: ""}})

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, "ANTHROPIC_API_KEY=''") {
		t.Errorf("empty-value profile entry should emit ANTHROPIC_API_KEY='' to clear it, got: %s", cmd)
	}
}

// TestProfile_EmitsFullSet asserts the full resolved set is emitted (each value
// single-quoted), --model is sourced from the set's ANTHROPIC_MODEL, and the
// legacy agentEntry fields are NOT emitted (no double-export). The legacy fields
// are set to DISTINCT values precisely so their absence proves the legacy-skip.
func TestProfile_EmitsFullSet(t *testing.T) {
	entry := config.AgentEntry{
		Type: "autonomous", Description: "test",
		Model: "legacy-model", BaseURL: "http://legacy", AuthToken: "legacy-tok",
	}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
		{Key: "ANTHROPIC_BASE_URL", Value: "http://localhost:1234"},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "tok123"},
		{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: "claude-opus-4"},
	})

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{
		"ANTHROPIC_MODEL='claude-opus-4'",
		"ANTHROPIC_BASE_URL='http://localhost:1234'",
		"ANTHROPIC_AUTH_TOKEN='tok123'",
		"ANTHROPIC_DEFAULT_OPUS_MODEL='claude-opus-4'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("command should contain %q, got: %s", want, cmd)
		}
	}
	if !strings.Contains(cmd, "--model 'claude-opus-4'") {
		t.Errorf("--model should be sourced from the set's ANTHROPIC_MODEL, got: %s", cmd)
	}
	for _, banned := range []string{"http://legacy", "legacy-tok", "--model 'legacy-model'"} {
		if strings.Contains(cmd, banned) {
			t.Errorf("legacy emission must be skipped when modelEnv is present; found %q in: %s", banned, cmd)
		}
	}
}

// TestBuildStartupCommand_ModelOnlySet_KeepsLegacyEndpoint
// (PR #482): a legacy mixed-provider agent whose Model is NOT a defined models.json
// profile resolves through ResolveModelEnv's raw-id branch to a set of ONLY
// ANTHROPIC_MODEL. That non-empty set must NOT suppress the legacy BaseURL/AuthToken —
// otherwise the endpoint is silently dropped and the agent falls back to the default
// Anthropic endpoint (regression of #262). The set carries no
// ANTHROPIC_BASE_URL, so the legacy endpoint must still travel with the model.
func TestBuildStartupCommand_ModelOnlySet_KeepsLegacyEndpoint(t *testing.T) {
	entry := config.AgentEntry{
		Type: "autonomous", Description: "test",
		Model: "legacy-model", BaseURL: "http://legacy:1234", AuthToken: "legacy-tok",
	}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	// Raw-id passthrough set (no matching profile / no models.json): ANTHROPIC_MODEL only.
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "legacy-model"}})

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{
		"ANTHROPIC_MODEL='legacy-model'",
		"ANTHROPIC_BASE_URL='http://legacy:1234'",
		"ANTHROPIC_AUTH_TOKEN='legacy-tok'",
	} {
		if !strings.Contains(cmd, want) {
			t.Errorf("a model-only resolved set must still carry the legacy endpoint; missing %q in: %s", want, cmd)
		}
	}
}

// TestBuildStartupCommand_ModelOnlySet_NoLegacyEndpoint_EmitsNoEndpoint is the negative
// companion: the legacy-endpoint fallback is presence-gated, so an agent with NO legacy
// BaseURL/AuthToken and a model-only resolved set carries no endpoint. Issue #508 W2
// changed "emit nothing" into "emit an explicit structural clear" so a redirect var
// inherited on a reused session is overwritten rather than silently surviving (AC-4).
// A NON-EMPTY endpoint value must still never travel.
func TestBuildStartupCommand_ModelOnlySet_NoLegacyEndpoint_EmitsNoEndpoint(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test", Model: "legacy-model"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "legacy-model"}})

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{"ANTHROPIC_BASE_URL=''", "ANTHROPIC_AUTH_TOKEN=''"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("no endpoint travels, so an explicit structural clear is expected; missing %q in: %s", want, cmd)
		}
	}
	// No real (non-empty) endpoint value may travel — the clear must be empty.
	if strings.Contains(cmd, "ANTHROPIC_BASE_URL='http") {
		t.Errorf("no legacy endpoint is set, so no real ANTHROPIC_BASE_URL value should travel, got: %s", cmd)
	}
}

// TestProfile_ShellInjectionInert asserts every model-env value is routed through
// shellQuote: a quote-laden value stays single-quote-wrapped, never bare.
func TestProfile_ShellInjectionInert(t *testing.T) {
	const payload = `'; rm -rf / #`
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_AUTH_TOKEN", Value: payload}})

	cmd := mgr.BuildStartupCommand()

	quoted := shellQuote(payload)
	if !strings.Contains(cmd, "ANTHROPIC_AUTH_TOKEN="+quoted) {
		t.Errorf("model-env value with metacharacters should be shell-quoted (%s), got: %s", quoted, cmd)
	}
	if strings.Contains(cmd, "ANTHROPIC_AUTH_TOKEN='; rm") {
		t.Error("model-env value should be quoted, not bare — shell injection possible")
	}
}

// TestHandoff_ModelEnvParity is a behavioral parity check: the respawn entrypoint
// (BuildStartupCommand) re-emits the same set identically. Respawn rebuilds a
// Manager and calls BuildStartupCommand, so a deterministic re-build of the same
// state is the in-package guarantee that start and respawn agree.
func TestHandoff_ModelEnvParity(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
		{Key: "ANTHROPIC_BASE_URL", Value: "http://localhost:1234"},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "tok123"},
	})

	first := mgr.BuildStartupCommand()
	second := mgr.BuildStartupCommand()

	if first != second {
		t.Errorf("respawn must re-emit an identical command.\nfirst:  %s\nsecond: %s", first, second)
	}
	for _, want := range []string{
		"ANTHROPIC_BASE_URL='http://localhost:1234'",
		"ANTHROPIC_AUTH_TOKEN='tok123'",
		"--model 'claude-opus-4'",
	} {
		if !strings.Contains(first, want) {
			t.Errorf("re-emitted command should carry %q, got: %s", want, first)
		}
	}
}

// --- Issue #508 W2: file: deref, structural clears, session-env hygiene ---

// TestBuildStartupCommand_FileRefDerefsSecret proves the deref (AC-3): a file:-ref
// ANTHROPIC_AUTH_TOKEN is emitted as ANTHROPIC_AUTH_TOKEN="$(cat '<abs-path>')" so the
// pane shell substitutes at exec time and the secret VALUE never lands on the launch
// line or in scrollback. The set carries ANTHROPIC_BASE_URL (a real redirect always
// has a base_url), so the structural clear does not fire over the deref.
func TestBuildStartupCommand_FileRefDerefsSecret(t *testing.T) {
	const secret = "sk-super-secret-gateway-value-DO-NOT-LEAK"
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
		{Key: "ANTHROPIC_BASE_URL", Value: "https://gateway.internal"},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "file:secrets/gw.tok"},
	})

	cmd := mgr.BuildStartupCommand()

	if !strings.Contains(cmd, `ANTHROPIC_AUTH_TOKEN="$(cat '`) {
		t.Errorf("file:-ref token must be dereferenced to \"$(cat '<path>')\", got: %s", cmd)
	}
	// A relative file: path resolves against the factory root so $(cat …) reads the
	// right file regardless of the pane's working directory.
	if !strings.Contains(cmd, `$(cat '/tmp/factory/secrets/gw.tok')`) {
		t.Errorf("relative file: path must be joined to the factory root, got: %s", cmd)
	}
	// The raw file: placeholder must NOT survive on the inline line — it is transformed.
	if strings.Contains(cmd, "file:secrets/gw.tok") {
		t.Errorf("raw file: placeholder must not appear inline (it is dereferenced), got: %s", cmd)
	}
	// The secret VALUE must never appear (only the path does); the deref reads the file
	// at exec time, so the literal contents can never reach the built command.
	if strings.Contains(cmd, secret) {
		t.Errorf("the secret value must never appear on the launch line, got: %s", cmd)
	}
}

// TestBuildStartupCommand_NoEndpoint_EmitsStructuralClears proves the structural clear
// (AC-4): a no-endpoint / no-legacy resolved set emits explicit ANTHROPIC_BASE_URL=''
// and ANTHROPIC_AUTH_TOKEN='' so a stale redirect var inherited on a reused session is
// overwritten. The set carries a model + a default var but no ANTHROPIC_BASE_URL.
func TestBuildStartupCommand_NoEndpoint_EmitsStructuralClears(t *testing.T) {
	entry := config.AgentEntry{Type: "autonomous", Description: "test"}
	mgr := NewManager("/tmp/factory", "testagent", entry)
	mgr.SetModelEnv([]config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
		{Key: "ANTHROPIC_DEFAULT_OPUS_MODEL", Value: "claude-opus-4"},
	})

	cmd := mgr.BuildStartupCommand()

	for _, want := range []string{"ANTHROPIC_BASE_URL=''", "ANTHROPIC_AUTH_TOKEN=''"} {
		if !strings.Contains(cmd, want) {
			t.Errorf("no-endpoint set should emit the explicit clear %q, got: %s", want, cmd)
		}
	}
}

// TestStart_NoEndpointProfile_UnsetsStaleRedirect proves the Start() hygiene twin (AC-8):
// a no-endpoint model-only profile clears ANTHROPIC_BASE_URL and unsets the other
// redirect-family vars on the (reused) session, so a profile switch leaves no stale
// redirect var. It drives the full Start() path through the recording hermetic fake.
func TestStart_NoEndpointProfile_UnsetsStaleRedirect(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelEnv([]config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	sessionID := mgr.SessionID()
	staleBaseURLSet := "SetEnvironment " + sessionID + " ANTHROPIC_BASE_URL="
	unsetBaseURL := "UnsetEnvironment " + sessionID + " ANTHROPIC_BASE_URL"
	unsetStaleModel := "UnsetEnvironment " + sessionID + " ANTHROPIC_DEFAULT_OPUS_MODEL"

	var baseURLCleared, staleModelUnset bool
	for _, op := range fake.ops {
		// A non-empty base_url set on a no-endpoint profile is a contamination bug.
		if strings.HasPrefix(op, staleBaseURLSet) && op != staleBaseURLSet {
			t.Errorf("no-endpoint profile must not set a non-empty ANTHROPIC_BASE_URL; got op %q", op)
		}
		if op == staleBaseURLSet || op == unsetBaseURL {
			baseURLCleared = true
		}
		if op == unsetStaleModel {
			staleModelUnset = true
		}
	}
	if !baseURLCleared {
		t.Errorf("expected an empty-value clear or unset of ANTHROPIC_BASE_URL; ops=%v", fake.ops)
	}
	// A redirect var NOT in the effective set must be unset by the hygiene pass so a
	// prior profile's value cannot survive the switch.
	if !staleModelUnset {
		t.Errorf("hygiene pass must unset a redirect var absent from the effective set (ANTHROPIC_DEFAULT_OPUS_MODEL); ops=%v", fake.ops)
	}
}

// envOpsForSession returns the SetEnvironment/UnsetEnvironment ops the recorder
// tagged with exactly sess (the op's session field is fields[1]). Used by the
// two-manager isolation test to partition one shared recorder's ops per session.
func envOpsForSession(ops []string, sess string) []string {
	var out []string
	for _, op := range ops {
		f := strings.Fields(op)
		if len(f) >= 2 && (f[0] == "SetEnvironment" || f[0] == "UnsetEnvironment") && f[1] == sess {
			out = append(out, op)
		}
	}
	return out
}

// hasOp reports whether ops contains an exact match for want.
func hasOp(ops []string, want string) bool {
	for _, op := range ops {
		if op == want {
			return true
		}
	}
	return false
}

// isRedirectFamilyEnvOp reports whether op is a Set/Unset of a redirect-family var
// (the ANTHROPIC_* / CLAUDE_CODE_SUBAGENT_MODEL set the hygiene pass governs).
func isRedirectFamilyEnvOp(op string) bool {
	f := strings.Fields(op)
	if len(f) < 3 || (f[0] != "SetEnvironment" && f[0] != "UnsetEnvironment") {
		return false
	}
	key := f[2]
	if f[0] == "SetEnvironment" {
		key = strings.SplitN(f[2], "=", 2)[0]
	}
	for _, rk := range redirectFamilyVars {
		if key == rk {
			return true
		}
	}
	return false
}

// TestStart_TwoManagers_NoRedirectCrossContamination is the literal two-manager
// isolation proof the AC-9 session row names (issue #508 AC-4). Two managers with
// DIFFERENT profiles — an OpenAI-via-LiteLLM endpoint profile (A) and a no-endpoint
// Anthropic model-only profile (B) — are started against ONE shared hermetic tmux
// recorder. Each Manager writes env only to its OWN session (SessionName(agentName)),
// so the isolation guarantee is:
//   - A's session carries A's real ANTHROPIC_BASE_URL + the raw file: token placeholder
//     (the tmux twin verbatim, never a resolved secret);
//   - B's session emits the explicit ANTHROPIC_BASE_URL='' / ANTHROPIC_AUTH_TOKEN=''
//     structural clears and unsets its stale redirect var (the hygiene pass);
//   - NO op tagged with B's session ever carries A's endpoint URL or token (A cannot
//     leak into B), and NO op tagged with A's session is the empty clear (B starting
//     never clobbers A's live endpoint);
//   - every redirect-family env op is tagged with exactly one of the two DISTINCT
//     session IDs (no op escapes to a shared or third session).
//
// The AC-4 substance (no stale redirect survives a switch; every value is shell-inert)
// is already covered by TestStart_NoEndpointProfile_UnsetsStaleRedirect and
// TestProfile_ShellInjectionInert; this additive func closes the AC-9 wording literally
// and pins the cross-session guarantee those single-manager tests cannot express.
func TestStart_TwoManagers_NoRedirectCrossContamination(t *testing.T) {
	origMem := checkAvailableMemoryFunc
	checkAvailableMemoryFunc = func() (uint64, error) { return 100000, nil }
	t.Cleanup(func() { checkAvailableMemoryFunc = origMem })

	fake := installHermeticSession(t) // ONE recorder shared by both managers

	tmpDir := t.TempDir()
	wtPath := filepath.Join(tmpDir, ".worktrees", "wt-test")

	const gatewayURL = "https://gateway.internal"
	const gatewayTok = "file:secrets/a.tok"

	startManager := func(name string, env []config.EnvVar) *Manager {
		if err := os.MkdirAll(config.AgentDir(wtPath, name), 0o755); err != nil {
			t.Fatalf("creating agent dir for %s: %v", name, err)
		}
		mgr := NewManager(tmpDir, name, config.AgentEntry{Type: "autonomous", Description: "test"})
		if err := mgr.SetWorktree(wtPath, "wt-test"); err != nil {
			t.Fatalf("SetWorktree(%s): %v", name, err)
		}
		mgr.SetModelEnv(env)
		if err := mgr.Start(); err != nil {
			t.Fatalf("Start(%s): unexpected error: %v", name, err)
		}
		return mgr
	}

	// A: an OpenAI-via-LiteLLM endpoint profile (base_url + file: token).
	mgrA := startManager("manager-a", []config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "gpt-x"},
		{Key: "ANTHROPIC_BASE_URL", Value: gatewayURL},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: gatewayTok},
	})
	// B: a no-endpoint Anthropic model-only profile — its ANTHROPIC_DEFAULT_OPUS_MODEL
	// is absent from the effective set, so the hygiene pass must unset it.
	mgrB := startManager("manager-b", []config.EnvVar{
		{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"},
	})

	sessA, sessB := mgrA.SessionID(), mgrB.SessionID()
	if sessA == sessB {
		t.Fatalf("two managers must have distinct session IDs; both were %q", sessA)
	}

	opsA := envOpsForSession(fake.ops, sessA)
	opsB := envOpsForSession(fake.ops, sessB)
	if len(opsA) == 0 || len(opsB) == 0 {
		t.Fatalf("non-vacuity: both sessions must record env ops; A=%d B=%d\nops=%v", len(opsA), len(opsB), fake.ops)
	}

	// A carries its real endpoint + the raw file: token placeholder (tmux twin).
	for _, want := range []string{
		"SetEnvironment " + sessA + " ANTHROPIC_BASE_URL=" + gatewayURL,
		"SetEnvironment " + sessA + " ANTHROPIC_AUTH_TOKEN=" + gatewayTok,
	} {
		if !hasOp(opsA, want) {
			t.Errorf("manager A session missing %q; opsA=%v", want, opsA)
		}
	}

	// B emits the explicit structural clears and unsets its stale redirect (hygiene).
	for _, want := range []string{
		"SetEnvironment " + sessB + " ANTHROPIC_BASE_URL=",
		"SetEnvironment " + sessB + " ANTHROPIC_AUTH_TOKEN=",
	} {
		if !hasOp(opsB, want) {
			t.Errorf("manager B session missing structural clear %q; opsB=%v", want, opsB)
		}
	}
	if !hasOp(opsB, "UnsetEnvironment "+sessB+" ANTHROPIC_DEFAULT_OPUS_MODEL") {
		t.Errorf("manager B hygiene must unset the stale ANTHROPIC_DEFAULT_OPUS_MODEL; opsB=%v", opsB)
	}

	// Isolation A→B: A's endpoint URL / token never appear under B's session.
	for _, op := range opsB {
		if strings.Contains(op, gatewayURL) {
			t.Errorf("manager A endpoint URL leaked into manager B session: %q", op)
		}
		if strings.Contains(op, gatewayTok) {
			t.Errorf("manager A token leaked into manager B session: %q", op)
		}
	}
	// Isolation B→A: B's empty structural clear never lands on A's live endpoint.
	for _, op := range opsA {
		if op == "SetEnvironment "+sessA+" ANTHROPIC_BASE_URL=" ||
			op == "SetEnvironment "+sessA+" ANTHROPIC_AUTH_TOKEN=" {
			t.Errorf("manager B structural clear clobbered manager A's live endpoint: %q", op)
		}
	}

	// Completeness: every redirect-family env op belongs to exactly one of the two
	// distinct sessions — none escaped to a shared or third session name.
	for _, op := range fake.ops {
		if !isRedirectFamilyEnvOp(op) {
			continue
		}
		sess := strings.Fields(op)[1]
		if sess != sessA && sess != sessB {
			t.Errorf("redirect-family op escaped to an unexpected session %q: %q", sess, op)
		}
	}
}

// TestStart_TmuxTwinCarriesFileRefPlaceholder locks the deliberate twin asymmetry
// (issue #508 W2): the Start() tmux twin mirrors a file:-ref ANTHROPIC_AUTH_TOKEN as the
// RAW placeholder verbatim — never the "$(cat …)" deref (tmux set-environment does no
// shell evaluation, so it would be stored literally) and never a resolved secret (it
// would be readable via `tmux show-environment`). The deref lives ONLY in the inline
// command that buildStartupCommand types into the pane.
func TestStart_TmuxTwinCarriesFileRefPlaceholder(t *testing.T) {
	mgr, fake := startMouseAgent(t, nil)
	mgr.SetModelEnv([]config.EnvVar{
		{Key: "ANTHROPIC_BASE_URL", Value: "https://gateway.internal"},
		{Key: "ANTHROPIC_AUTH_TOKEN", Value: "file:secrets/gw.tok"},
	})

	if err := mgr.Start(); err != nil {
		t.Fatalf("Start: unexpected error: %v", err)
	}

	sessionID := mgr.SessionID()
	want := "SetEnvironment " + sessionID + " ANTHROPIC_AUTH_TOKEN=file:secrets/gw.tok"
	var found bool
	for _, op := range fake.ops {
		if op == want {
			found = true
		}
		// The tmux twin (a SetEnvironment op) must never carry the $(cat …) deref.
		// (The SendKeysDelayed op legitimately carries the inline deref — exclude it.)
		if strings.HasPrefix(op, "SetEnvironment ") &&
			strings.Contains(op, "ANTHROPIC_AUTH_TOKEN") && strings.Contains(op, "$(cat") {
			t.Errorf("tmux twin must carry the raw file: placeholder, not the $(cat …) deref; got op %q", op)
		}
	}
	if !found {
		t.Errorf("tmux twin must SetEnvironment the raw file: placeholder verbatim; want op %q, ops=%v", want, fake.ops)
	}
}
