package session

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/tmux"
)

var (
	ErrAlreadyRunning = errors.New("agent session already running")
	ErrNotRunning     = errors.New("agent session not running")
	ErrNotProvisioned = errors.New("agent workspace not provisioned (run af install <role>)")
	ErrWorktreeNotSet = errors.New("session: Start called before SetWorktree with a non-empty path")
)

const (
	envBaseURL   = "ANTHROPIC_BASE_URL"
	envAuthToken = "ANTHROPIC_AUTH_TOKEN"

	// Git identity env (issue #371 AC-2): exported only when no ambient identity
	// resolves, so they never clobber a present one (C-4). GIT_AUTHOR_*/
	// GIT_COMMITTER_* override config unconditionally, hence the presence-gate.
	envGitAuthorName     = "GIT_AUTHOR_NAME"
	envGitAuthorEmail    = "GIT_AUTHOR_EMAIL"
	envGitCommitterName  = "GIT_COMMITTER_NAME"
	envGitCommitterEmail = "GIT_COMMITTER_EMAIL"

	// Trailer activation env (issue #371 AC-4/AC-5): GIT_CONFIG_* sets
	// core.hooksPath to the af-managed githooks dir for this session (writing
	// nothing to .git/), and AF_COAUTHOR_* hand the prepare-commit-msg hook the
	// co-author value from the C-3 constants (one source of truth, no shell literal).
	envGitConfigCount  = "GIT_CONFIG_COUNT"
	envGitConfigKey0   = "GIT_CONFIG_KEY_0"
	envGitConfigValue0 = "GIT_CONFIG_VALUE_0"
	envCoauthorName    = "AF_COAUTHOR_NAME"
	envCoauthorEmail   = "AF_COAUTHOR_EMAIL"
)

var checkAvailableMemoryFunc = checkAvailableMemory

func checkAvailableMemory() (uint64, error) {
	switch runtime.GOOS {
	case "linux":
		return readLinuxMemAvailableMB()
	case "darwin":
		return readDarwinMemAvailableMB()
	default:
		return 0, fmt.Errorf("unsupported platform for memory check: %s", runtime.GOOS)
	}
}

func readLinuxMemAvailableMB() (uint64, error) {
	f, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0, fmt.Errorf("reading /proc/meminfo: %w", err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemAvailable:") {
			fields := strings.Fields(line)
			if len(fields) < 2 {
				return 0, fmt.Errorf("unexpected MemAvailable format: %s", line)
			}
			kb, err := strconv.ParseUint(fields[1], 10, 64)
			if err != nil {
				return 0, fmt.Errorf("parsing MemAvailable: %w", err)
			}
			return kb / 1024, nil
		}
	}
	return 0, fmt.Errorf("MemAvailable not found in /proc/meminfo")
}

func readDarwinMemAvailableMB() (uint64, error) {
	out, err := exec.Command("vm_stat").Output()
	if err != nil {
		return 0, fmt.Errorf("running vm_stat: %w", err)
	}

	var pageSize uint64 = 4096
	var freePages, inactivePages uint64

	for _, line := range strings.Split(string(out), "\n") {
		if strings.Contains(line, "page size of") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "size" && i+2 < len(parts) {
					if ps, err := strconv.ParseUint(parts[i+2], 10, 64); err == nil {
						pageSize = ps
					}
					break
				}
			}
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		val := strings.TrimSuffix(fields[len(fields)-1], ".")
		n, _ := strconv.ParseUint(val, 10, 64)
		if strings.HasPrefix(line, "Pages free:") {
			freePages = n
		} else if strings.HasPrefix(line, "Pages inactive:") {
			inactivePages = n
		}
	}

	return (freePages + inactivePages) * pageSize / (1024 * 1024), nil
}

// tmuxClient is the exact union of the 13 *tmux.Tmux methods that Manager.Start()
// and Manager.Stop() call. Typing Manager.tmux to this interface is the seam that
// lets tests inject a fake; the compile assertion below guarantees the real
// client still satisfies it.
type tmuxClient interface {
	HasSession(name string) (bool, error)
	IsClaudeRunning(session string) bool
	KillSession(name string) error
	NewSession(name, workDir string) error
	SetEnvironment(session, key, value string) error
	SetOption(session, name, value string) error
	ShowOption(session, name string) (string, error)
	WaitForShellReady(session string, timeout time.Duration) error
	SendKeysDelayed(session, keys string, delayMs int) error
	WaitForCommand(session string, excludeCommands []string, timeout time.Duration) error
	AcceptBypassPermissionsWarning(session string) error
	NudgeSession(session, message string) error
	SendKeysRaw(session, keys string) error
}

// Compile-time check: the real *tmux.Tmux must satisfy tmuxClient (R-4 discipline).
var _ tmuxClient = (*tmux.Tmux)(nil)

// newManagerTmux is the seam tests override to inject a fake tmux client into
// NewManager. Production default returns the real *tmux.Tmux.
var newManagerTmux = func() tmuxClient { return tmux.NewTmux() }

// Manager handles agent session lifecycle operations.
type Manager struct {
	factoryRoot   string
	agentName     string
	agentEntry    config.AgentEntry
	tmux          tmuxClient
	initialPrompt string
	worktreePath  string
	worktreeID    string
	buildHost     *config.BuildHostConfig

	// Git identity to export when no ambient identity resolves (issue #371 AC-2).
	// Empty ⇒ not exported (presence-gate / C-4); set via SetGitIdentity.
	gitAuthorName  string
	gitAuthorEmail string

	// Trailer activation (issue #371 AC-4/AC-5): when gitHooksDir is non-empty the
	// session sets core.hooksPath to it and passes the co-author value to the hook.
	gitHooksDir   string
	coauthorName  string
	coauthorEmail string
}

// SetGitIdentity configures the default git author/committer identity exported
// into the agent session (issue #371 AC-2). The caller (cmd layer) presence-gates
// this: it is invoked only when no ambient identity resolves, so the exports
// never override a real identity (C-4). Empty values leave the exports off.
func (m *Manager) SetGitIdentity(name, email string) {
	m.gitAuthorName = name
	m.gitAuthorEmail = email
}

// SetGitTrailer activates the centralized Co-authored-by trailer for this session
// (issue #371 AC-4/AC-5): hooksDir becomes core.hooksPath (via GIT_CONFIG_*), and
// the co-author name/email are handed to the prepare-commit-msg hook via env. An
// empty hooksDir leaves the trailer channel off.
func (m *Manager) SetGitTrailer(hooksDir, coauthorName, coauthorEmail string) {
	m.gitHooksDir = hooksDir
	m.coauthorName = coauthorName
	m.coauthorEmail = coauthorEmail
}

// NewManager creates a Manager for the given agent.
func NewManager(factoryRoot, agentName string, entry config.AgentEntry) *Manager {
	return &Manager{
		factoryRoot: factoryRoot,
		agentName:   agentName,
		agentEntry:  entry,
		tmux:        newManagerTmux(),
	}
}

// SetInitialPrompt sets a task prompt that will be passed as Claude's first
// user message via CLI argument. When set, the startup nudge is suppressed.
func (m *Manager) SetInitialPrompt(prompt string) {
	m.initialPrompt = prompt
}

// SetWorktree configures the manager to use a worktree-based working directory.
// When set, workDir returns the agent dir inside the worktree, and AF_WORKTREE /
// AF_WORKTREE_ID environment variables are exported in the tmux session.
// Returns an error if path is empty.
func (m *Manager) SetWorktree(path, id string) error {
	if path == "" {
		return fmt.Errorf("SetWorktree: path must not be empty")
	}
	m.worktreePath = path
	m.worktreeID = id
	return nil
}

// SetBuildHost configures optional build-host settings. When set, AF_BUILD_*
// environment variables are exported into the tmux session.
func (m *Manager) SetBuildHost(cfg *config.BuildHostConfig) {
	m.buildHost = cfg
}

// SessionID returns the tmux session name for this agent.
func (m *Manager) SessionID() string {
	return SessionName(m.agentName)
}

// workDir returns the agent's workspace directory.
// When a worktree is configured, returns the agent dir inside the worktree.
func (m *Manager) workDir() string {
	if m.worktreePath != "" {
		return config.AgentDir(m.worktreePath, m.agentName)
	}
	return config.AgentDir(m.factoryRoot, m.agentName)
}

// WorkDir returns the agent's workspace directory for testing.
func (m *Manager) WorkDir() string {
	return m.workDir()
}

// Start creates the tmux session and launches Claude.
func (m *Manager) Start() error {
	sessionID := m.SessionID()

	if m.worktreePath == "" {
		return ErrWorktreeNotSet
	}

	// Zombie detection: if session exists, check health
	running, _ := m.tmux.HasSession(sessionID)
	if running {
		if m.tmux.IsClaudeRunning(sessionID) {
			return ErrAlreadyRunning
		}
		// Zombie — tmux alive but Claude dead. Kill and recreate.
		if err := m.tmux.KillSession(sessionID); err != nil {
			return fmt.Errorf("killing zombie session: %w", err)
		}
	}

	// Verify workspace exists
	workDir := m.workDir()
	if _, err := os.Stat(workDir); os.IsNotExist(err) {
		return fmt.Errorf("%w: %s", ErrNotProvisioned, workDir)
	}

	// Create tmux session
	if err := m.tmux.NewSession(sessionID, workDir); err != nil {
		return fmt.Errorf("creating tmux session: %w", err)
	}

	if (m.agentEntry.BaseURL != "") != (m.agentEntry.AuthToken != "") {
		set, unset := "base_url", "auth_token"
		if m.agentEntry.AuthToken != "" {
			set, unset = "auth_token", "base_url"
		}
		fmt.Fprintf(os.Stderr, "warning: agent %s has %s but not %s — local endpoints typically require both\n",
			m.agentName, set, unset)
	}

	// Set environment variables (best-effort)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ROOT", m.factoryRoot)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ROLE", m.agentName)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ACTOR", m.agentName)
	if m.worktreePath != "" {
		_ = m.tmux.SetEnvironment(sessionID, "AF_WORKTREE", m.worktreePath)
		_ = m.tmux.SetEnvironment(sessionID, "AF_WORKTREE_ID", m.worktreeID)
	}
	if m.agentEntry.Model != "" {
		_ = m.tmux.SetEnvironment(sessionID, "ANTHROPIC_MODEL", m.agentEntry.Model)
	}
	if m.agentEntry.BaseURL != "" {
		if err := m.tmux.SetEnvironment(sessionID, envBaseURL, m.agentEntry.BaseURL); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to set %s for %s: %v\n", envBaseURL, sessionID, err)
		}
	}
	if m.agentEntry.AuthToken != "" {
		if err := m.tmux.SetEnvironment(sessionID, envAuthToken, m.agentEntry.AuthToken); err != nil {
			fmt.Fprintf(os.Stderr, "warning: failed to set %s for %s: %v\n", envAuthToken, sessionID, err)
		}
	}
	// Git identity fallback (best-effort; presence-gated — issue #371 AC-2/C-4).
	if m.gitAuthorName != "" && m.gitAuthorEmail != "" {
		_ = m.tmux.SetEnvironment(sessionID, envGitAuthorName, m.gitAuthorName)
		_ = m.tmux.SetEnvironment(sessionID, envGitAuthorEmail, m.gitAuthorEmail)
		_ = m.tmux.SetEnvironment(sessionID, envGitCommitterName, m.gitAuthorName)
		_ = m.tmux.SetEnvironment(sessionID, envGitCommitterEmail, m.gitAuthorEmail)
	}
	// Trailer activation (best-effort — issue #371 AC-4/AC-5).
	if m.gitHooksDir != "" {
		_ = m.tmux.SetEnvironment(sessionID, envGitConfigCount, "1")
		_ = m.tmux.SetEnvironment(sessionID, envGitConfigKey0, "core.hooksPath")
		_ = m.tmux.SetEnvironment(sessionID, envGitConfigValue0, m.gitHooksDir)
		if m.coauthorName != "" && m.coauthorEmail != "" {
			_ = m.tmux.SetEnvironment(sessionID, envCoauthorName, m.coauthorName)
			_ = m.tmux.SetEnvironment(sessionID, envCoauthorEmail, m.coauthorEmail)
		}
	}
	if m.buildHost != nil {
		_ = m.tmux.SetEnvironment(sessionID, "AF_BUILD_MODE", m.buildHost.Mode)
		if m.buildHost.Host != "" {
			_ = m.tmux.SetEnvironment(sessionID, "AF_BUILD_HOST", m.buildHost.Host)
		}
		if m.buildHost.User != "" {
			_ = m.tmux.SetEnvironment(sessionID, "AF_BUILD_USER", m.buildHost.User)
		}
		if m.buildHost.MountPath != "" {
			_ = m.tmux.SetEnvironment(sessionID, "AF_HOST_MOUNT", m.buildHost.MountPath)
		}
	}

	// Enable mouse so the wheel scrolls Claude's conversation viewport instead of
	// being translated to arrow keys by the outer terminal's alternate-scroll
	// (Issue #412, Fix A). Session-scoped — agent sessions only, never promoted to
	// global. Best-effort: a failed apply must never abort session creation.
	_ = m.tmux.SetOption(sessionID, "mouse", "on")
	// Best-effort read-back: a silent apply failure would leave the wheel scrolling
	// broken with no signal (Issue #412 Gap 7). Surface a single stderr warning if
	// the option did not take, mirroring the warning idiom used above. A read error
	// is itself swallowed (warn-or-stay-silent) — this must never abort Start().
	if v, err := m.tmux.ShowOption(sessionID, "mouse"); err == nil && v != "on" {
		fmt.Fprintf(os.Stderr, "warning: mouse option did not take for %s (got %q, want \"on\") — wheel scrollback may not work\n", sessionID, v)
	}

	// Wait for shell to be ready
	if err := m.tmux.WaitForShellReady(sessionID, 5*time.Second); err != nil {
		_ = m.tmux.KillSession(sessionID)
		return fmt.Errorf("waiting for shell: %w", err)
	}

	// Pre-flight memory check before launching Claude
	availMB, memErr := checkAvailableMemoryFunc()
	if memErr == nil && availMB < 512 {
		_ = m.tmux.KillSession(sessionID)
		return fmt.Errorf("insufficient memory to launch Claude: %dMB available, 512MB required", availMB)
	}

	// Build startup command with inline exports
	startupCmd := m.buildStartupCommand()

	// Send startup command after brief delay
	if err := m.tmux.SendKeysDelayed(sessionID, startupCmd, 200); err != nil {
		_ = m.tmux.KillSession(sessionID)
		return fmt.Errorf("starting Claude agent: %w", err)
	}

	// Wait for Claude to start (non-fatal)
	_ = m.tmux.WaitForCommand(sessionID, tmux.SupportedShells(), tmux.ClaudeStartTimeout())

	// Accept bypass permissions warning for all agents
	_ = m.tmux.AcceptBypassPermissionsWarning(sessionID)

	// Startup nudge (non-fatal). Skipped when an initial prompt is set
	// because the task is already delivered as a CLI argument.
	if nudge := m.buildNudge(); nudge != "" {
		_ = m.tmux.NudgeSession(sessionID, nudge)
	}

	return nil
}

// buildStartupCommand constructs the claude launch command with inline exports.
// When an initial prompt is set, it is appended as a positional argument to
// claude, making it the first user message.
func (m *Manager) buildStartupCommand() string {
	exports := fmt.Sprintf("export AF_ROOT=%s AF_ROLE=%s AF_ACTOR=%s",
		shellQuote(m.factoryRoot), shellQuote(m.agentName), shellQuote(m.agentName))
	if m.worktreePath != "" {
		exports += fmt.Sprintf(" AF_WORKTREE=%s AF_WORKTREE_ID=%s",
			shellQuote(m.worktreePath), shellQuote(m.worktreeID))
	}
	if m.agentEntry.BaseURL != "" {
		exports += fmt.Sprintf(" %s=%s", envBaseURL, shellQuote(m.agentEntry.BaseURL))
	}
	if m.agentEntry.AuthToken != "" {
		exports += fmt.Sprintf(" %s=%s", envAuthToken, shellQuote(m.agentEntry.AuthToken))
	}
	// Git identity fallback (presence-gated — only when no ambient identity resolved).
	if m.gitAuthorName != "" && m.gitAuthorEmail != "" {
		exports += fmt.Sprintf(" %s=%s %s=%s %s=%s %s=%s",
			envGitAuthorName, shellQuote(m.gitAuthorName),
			envGitAuthorEmail, shellQuote(m.gitAuthorEmail),
			envGitCommitterName, shellQuote(m.gitAuthorName),
			envGitCommitterEmail, shellQuote(m.gitAuthorEmail))
	}
	// Trailer activation: redirect git hook lookup to the af-managed githooks dir
	// (via core.hooksPath, ADR-017-clean) and hand the hook the co-author value.
	if m.gitHooksDir != "" {
		exports += fmt.Sprintf(" %s=1 %s=%s %s=%s",
			envGitConfigCount,
			envGitConfigKey0, shellQuote("core.hooksPath"),
			envGitConfigValue0, shellQuote(m.gitHooksDir))
		if m.coauthorName != "" && m.coauthorEmail != "" {
			exports += fmt.Sprintf(" %s=%s %s=%s",
				envCoauthorName, shellQuote(m.coauthorName),
				envCoauthorEmail, shellQuote(m.coauthorEmail))
		}
	}
	if m.buildHost != nil {
		exports += fmt.Sprintf(" AF_BUILD_MODE=%s", shellQuote(m.buildHost.Mode))
		if m.buildHost.Host != "" {
			exports += fmt.Sprintf(" AF_BUILD_HOST=%s", shellQuote(m.buildHost.Host))
		}
		if m.buildHost.User != "" {
			exports += fmt.Sprintf(" AF_BUILD_USER=%s", shellQuote(m.buildHost.User))
		}
		if m.buildHost.MountPath != "" {
			exports += fmt.Sprintf(" AF_HOST_MOUNT=%s", shellQuote(m.buildHost.MountPath))
		}
	}

	claude := "claude --dangerously-skip-permissions"
	if m.agentEntry.Model != "" {
		claude += " --model " + shellQuote(m.agentEntry.Model)
	}
	if m.initialPrompt != "" {
		claude += " " + shellQuote(m.initialPrompt)
	}

	return fmt.Sprintf("%s && %s", exports, claude)
}

// shellQuote wraps a string in POSIX single quotes, escaping embedded
// single quotes with the '\'' idiom.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}

// Stop gracefully terminates the agent session.
func (m *Manager) Stop() error {
	sessionID := m.SessionID()

	running, err := m.tmux.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return ErrNotRunning
	}

	// Graceful: send Ctrl-C first
	_ = m.tmux.SendKeysRaw(sessionID, "C-c")
	time.Sleep(100 * time.Millisecond)

	// Kill the session
	if err := m.tmux.KillSession(sessionID); err != nil {
		return fmt.Errorf("killing session: %w", err)
	}

	// Release lock (best-effort)
	_ = lock.New(m.workDir()).Release()
	_ = lock.NewWithPath(filepath.Join(m.workDir(), ".runtime", "fidelity-gate.lock")).Release()
	_ = lock.NewWithPath(filepath.Join(m.workDir(), ".runtime", "quality-gate.lock")).Release()

	return nil
}

// IsRunning checks if the agent session is active.
func (m *Manager) IsRunning() (bool, error) {
	return m.tmux.HasSession(m.SessionID())
}

// BuildStartupCommand returns the startup command for testing.
func (m *Manager) BuildStartupCommand() string {
	return m.buildStartupCommand()
}

// buildNudge constructs the startup nudge message.
// Returns empty string when an initial prompt is set (task delivered via CLI arg).
// Appends the agent's custom directive (from agents.json) if set.
func (m *Manager) buildNudge() string {
	if m.initialPrompt != "" {
		return ""
	}
	nudge := "Run `af prime` to check mail and begin work."
	if m.agentEntry.Directive != "" {
		nudge += " " + m.agentEntry.Directive
	}
	return nudge
}

// BuildNudge returns the startup nudge message for testing.
func (m *Manager) BuildNudge() string {
	return m.buildNudge()
}
