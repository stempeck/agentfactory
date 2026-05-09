package session

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
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

// Manager handles agent session lifecycle operations.
type Manager struct {
	factoryRoot    string
	agentName      string
	agentEntry     config.AgentEntry
	tmux           *tmux.Tmux
	initialPrompt  string
	worktreePath   string
	worktreeID     string
}

// NewManager creates a Manager for the given agent.
func NewManager(factoryRoot, agentName string, entry config.AgentEntry) *Manager {
	return &Manager{
		factoryRoot: factoryRoot,
		agentName:   agentName,
		agentEntry:  entry,
		tmux:        tmux.NewTmux(),
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

	// Set environment variables (best-effort)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ROOT", m.factoryRoot)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ROLE", m.agentName)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ACTOR", m.agentName)
	if m.worktreePath != "" {
		_ = m.tmux.SetEnvironment(sessionID, "AF_WORKTREE", m.worktreePath)
		_ = m.tmux.SetEnvironment(sessionID, "AF_WORKTREE_ID", m.worktreeID)
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

	claude := "claude --dangerously-skip-permissions"
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
