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

	// secretRefPrefix marks a file:<path> indirection for a secret-bearing env value
	// (issue #508). The canonical validator lives in internal/config as an
	// unexported symbol, so the launch chokepoint recognizes the prefix locally
	// rather than importing it.
	secretRefPrefix = "file:"
)

// redirectFamilyVars enumerates the endpoint/model redirect env the launch chokepoint
// owns. Start()'s session-env hygiene pass (issue #508) unsets any of these NOT in
// the effective set so a profile switch on a reused session leaves no stale redirect
// var. envBaseURL/envAuthToken use the consts (TestEndpointConstants_NoDuplicateStrings
// forbids their string literals outside the const block).
//
// ANTHROPIC_API_KEY is deliberately EXCLUDED: security.md I2 decides it is never
// auto-cleared — default-profile agents may legitimately authenticate via an ambient
// Anthropic key. Leaving it out of the hygiene family keeps API_KEY handling
// byte-identical to today's behavior (the zero-regression choice on this fleet-wide
// chokepoint). A profile that wants it cleared declares ANTHROPIC_API_KEY:"" explicitly,
// which lands in the effective set and emits the inline clear — so it is untouched here
// regardless.
var redirectFamilyVars = []string{
	envBaseURL,
	envAuthToken,
	"ANTHROPIC_MODEL",
	"ANTHROPIC_SMALL_FAST_MODEL",
	"ANTHROPIC_DEFAULT_OPUS_MODEL",
	"ANTHROPIC_DEFAULT_SONNET_MODEL",
	"ANTHROPIC_DEFAULT_HAIKU_MODEL",
	"CLAUDE_CODE_SUBAGENT_MODEL",
}

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

// tmuxClient is the exact union of the 14 *tmux.Tmux methods that Manager.Start()
// and Manager.Stop() call. Typing Manager.tmux to this interface is the seam that
// lets tests inject a fake; the compile assertion below guarantees the real
// client still satisfies it.
type tmuxClient interface {
	HasSession(name string) (bool, error)
	IsClaudeRunning(session string) bool
	KillSession(name string) error
	NewSession(name, workDir string) error
	SetEnvironment(session, key, value string) error
	UnsetEnvironment(session, key string) error
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

	// Resolved per-agent model-env export set (issue #480). When non-empty it is
	// emitted at the launch chokepoint in place of the legacy Model/BaseURL/
	// AuthToken fields (presence-gate); empty values are kept so a profile can
	// clear an ambient var (e.g. ANTHROPIC_API_KEY=''). Set via SetModelEnv.
	modelEnv []config.EnvVar

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

// SetModelEnv configures the resolved per-agent model-env export set (issue #480).
// The cmd layer (Phase 3) resolves it via config.ResolveModelEnv and hands it in
// after NewManager. A nil/empty set leaves the legacy Model/BaseURL/AuthToken
// emission path unchanged (presence-gate); a non-empty set is emitted at both
// launch sites and supersedes the legacy fields.
func (m *Manager) SetModelEnv(env []config.EnvVar) {
	m.modelEnv = env
}

// modelFromModelEnv returns the ANTHROPIC_MODEL value carried in the resolved set,
// or "" if the set does not define one (e.g. a base_url-only profile). The CLI
// --model flag and the ANTHROPIC_MODEL env are sourced from this single value so
// they never disagree. The key is scanned by name, not by position, because the
// resolver only places ANTHROPIC_MODEL first when the profile defines it.
func modelFromModelEnv(env []config.EnvVar) string {
	for _, ev := range env {
		if ev.Key == "ANTHROPIC_MODEL" {
			return ev.Value
		}
	}
	return ""
}

// modelEnvHasKey reports whether the resolved set already carries the given key. Used
// by both emission twins to decide whether a legacy endpoint must still be emitted: a
// model-only passthrough set (PR #482) carries no ANTHROPIC_BASE_URL, so the legacy
// endpoint must travel with it rather than be suppressed.
func modelEnvHasKey(env []config.EnvVar, key string) bool {
	for _, ev := range env {
		if ev.Key == key {
			return true
		}
	}
	return false
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

	// Issue #508: a legacy agents.json remote endpoint carrying a credential-
	// shaped literal auth_token is the most likely operator secret-leak mistake. Warn
	// LOUDLY but never fail (the 43052536 warn-only posture) so the operator moves the
	// key to a file: reference. Loopback endpoints are exempt (the seeded lmstudio
	// profile is legitimate) via the shared Phase-1 classifier. The sk- heuristic is
	// inlined to match Phase-1's looksLikeCredential shape without exporting it —
	// internal/config stays silent by convention (ADR-004); this warn layer is the
	// session boundary, mirroring the XOR-warn precedent above. base_url is already
	// URL-validated in validateAgentConfig (config.go); this adds no validation.
	if strings.HasPrefix(m.agentEntry.AuthToken, "sk-") && m.agentEntry.BaseURL != "" && !config.IsLoopbackEndpoint(m.agentEntry.BaseURL) {
		fmt.Fprintf(os.Stderr,
			"warning: agent %s has a credential-shaped auth_token on a non-loopback base_url %q in %s — "+
				"move the secret out of config: use a file: reference (auth_token: \"file:.agentfactory/secrets/%s.key\") instead of a literal token\n",
			m.agentName, m.agentEntry.BaseURL, config.AgentsConfigPath(m.factoryRoot), m.agentName)
	}

	// Set environment variables (best-effort)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ROOT", m.factoryRoot)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ROLE", m.agentName)
	_ = m.tmux.SetEnvironment(sessionID, "AF_ACTOR", m.agentName)
	if m.worktreePath != "" {
		_ = m.tmux.SetEnvironment(sessionID, "AF_WORKTREE", m.worktreePath)
		_ = m.tmux.SetEnvironment(sessionID, "AF_WORKTREE_ID", m.worktreeID)
	}
	// effective records the redirect-family keys this launch actually emits, so the
	// hygiene pass below can unset the rest (issue #508). It is populated in lockstep
	// with the SetEnvironment calls to guarantee it never diverges from what was emitted.
	effective := map[string]bool{}
	if len(m.modelEnv) > 0 {
		// Resolved model-env set supersedes the legacy fields (issue #480): emit the
		// whole set (empty values clear) and skip the legacy trio below so the
		// tmux env and the inline command never disagree.
		//
		// Deliberate twin asymmetry (issue #508): the tmux env carries a file:<path>
		// ANTHROPIC_AUTH_TOKEN as the raw placeholder VERBATIM — NOT the $(cat …) deref
		// buildStartupCommand emits inline, and NOT the resolved secret. tmux
		// set-environment does no shell evaluation, so a "$(cat …)" string would be
		// stored literally, and a resolved secret would be readable via
		// `tmux show-environment`. The file:→$(cat …) transform lives ONLY in
		// buildStartupCommand's inline loop.
		for _, ev := range m.modelEnv {
			_ = m.tmux.SetEnvironment(sessionID, ev.Key, ev.Value)
			effective[ev.Key] = true
		}
		// A model-only resolved set (a legacy agent whose Model is not a defined
		// profile, or no models.json at all) carries no endpoint. Keep the legacy
		// BaseURL/AuthToken travelling with it so a mixed-provider agent still reaches
		// its endpoint (PR #482: regression of #262).
		if !modelEnvHasKey(m.modelEnv, envBaseURL) {
			if m.agentEntry.BaseURL != "" {
				if err := m.tmux.SetEnvironment(sessionID, envBaseURL, m.agentEntry.BaseURL); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to set %s for %s: %v\n", envBaseURL, sessionID, err)
				}
				effective[envBaseURL] = true
			}
			if m.agentEntry.AuthToken != "" {
				if err := m.tmux.SetEnvironment(sessionID, envAuthToken, m.agentEntry.AuthToken); err != nil {
					fmt.Fprintf(os.Stderr, "warning: failed to set %s for %s: %v\n", envAuthToken, sessionID, err)
				}
				effective[envAuthToken] = true
			}
			// No endpoint travels at all after the legacy carry: blank any stale redirect
			// endpoint on the reused session, mirroring the inline KEY='' clear (issue
			// #508). Computed AFTER the carry so a legacy endpoint is never clobbered (PR
			// #482 regression class). The auth-token clear is further gated on an empty
			// auth_token: an auth_token-only config (base_url empty, token set just above)
			// must keep its token rather than lose it to a last-write-wins clear.
			if m.agentEntry.BaseURL == "" {
				_ = m.tmux.SetEnvironment(sessionID, envBaseURL, "")
				effective[envBaseURL] = true
				if m.agentEntry.AuthToken == "" {
					_ = m.tmux.SetEnvironment(sessionID, envAuthToken, "")
					effective[envAuthToken] = true
				}
			}
		}
	} else {
		if m.agentEntry.Model != "" {
			_ = m.tmux.SetEnvironment(sessionID, "ANTHROPIC_MODEL", m.agentEntry.Model)
			effective["ANTHROPIC_MODEL"] = true
		}
		if m.agentEntry.BaseURL != "" {
			if err := m.tmux.SetEnvironment(sessionID, envBaseURL, m.agentEntry.BaseURL); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to set %s for %s: %v\n", envBaseURL, sessionID, err)
			}
			effective[envBaseURL] = true
		}
		if m.agentEntry.AuthToken != "" {
			if err := m.tmux.SetEnvironment(sessionID, envAuthToken, m.agentEntry.AuthToken); err != nil {
				fmt.Fprintf(os.Stderr, "warning: failed to set %s for %s: %v\n", envAuthToken, sessionID, err)
			}
			effective[envAuthToken] = true
		}
	}
	// Session-env hygiene (issue #508): a respawn / profile switch reuses the
	// tmux session, so a redirect var set by a prior profile survives in the session env
	// (respawn-pane inherits it) unless we actively remove it. Unset every
	// redirect-family var NOT in the effective set emitted above, leaving a switched-away
	// endpoint with no stale value. No-op when nothing is stale.
	for _, key := range redirectFamilyVars {
		if !effective[key] {
			_ = m.tmux.UnsetEnvironment(sessionID, key)
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
	if len(m.modelEnv) > 0 {
		// Resolved model-env set supersedes the legacy fields (issue #480), in the
		// same slot the legacy exports occupied. Every value is single-quoted via
		// shellQuote (shell-injection inert); an empty value emits KEY='' to clear it.
		// effective records the redirect-family keys this launch actually emits so the
		// hygiene pass below can clear the rest — the inline twin of Start()'s pass.
		effective := map[string]bool{}
		for _, ev := range m.modelEnv {
			// A file:<path> ANTHROPIC_AUTH_TOKEN is dereferenced to "$(cat '<abs>')" so
			// the pane shell reads the secret at exec time — the value never lands on
			// the launch line or in scrollback (issue #508). Only the path passes
			// through shellQuote; the surrounding double-quotes and $(cat …) are
			// literal, because shellQuote would single-quote the whole token and
			// disable the command substitution. A relative path resolves against the
			// factory root so $(cat …) reads the right file whatever the pane's cwd.
			if ev.Key == envAuthToken && strings.HasPrefix(ev.Value, secretRefPrefix) {
				path := strings.TrimPrefix(ev.Value, secretRefPrefix)
				if !filepath.IsAbs(path) {
					path = filepath.Join(m.factoryRoot, path)
				}
				exports += fmt.Sprintf(" %s=\"$(cat %s)\"", ev.Key, shellQuote(path))
			} else {
				exports += fmt.Sprintf(" %s=%s", ev.Key, shellQuote(ev.Value))
			}
			effective[ev.Key] = true
		}
		// A model-only resolved set carries no endpoint; keep the legacy BaseURL/
		// AuthToken travelling with it (PR #482: regression of #262). Mirrors
		// the Start() tmux-env twin above.
		if !modelEnvHasKey(m.modelEnv, envBaseURL) {
			if m.agentEntry.BaseURL != "" {
				exports += fmt.Sprintf(" %s=%s", envBaseURL, shellQuote(m.agentEntry.BaseURL))
				effective[envBaseURL] = true
			}
			if m.agentEntry.AuthToken != "" {
				exports += fmt.Sprintf(" %s=%s", envAuthToken, shellQuote(m.agentEntry.AuthToken))
				effective[envAuthToken] = true
			}
		}
		// Redirect-var hygiene at parity with Start(): emit an explicit KEY='' for every
		// redirect-family var this launch does NOT carry, so a value a prior profile left
		// on a reused session survives no switch. This is the ONLY clear the respawn paths
		// (handoff / compact-handoff / watchdog recoverAgent all rebuild through here) ever
		// emit, so it must cover the whole family — not just base_url/auth_token (issue
		// #508). Computed on the EFFECTIVE env AFTER the legacy carry so a carried endpoint
		// is never clobbered (PR #482 regression class); an auth_token-only config keeps
		// its token because envAuthToken is in the effective set; ANTHROPIC_API_KEY is not
		// in this family, so it is never auto-cleared.
		for _, key := range redirectFamilyVars {
			if !effective[key] {
				exports += fmt.Sprintf(" %s=''", key)
			}
		}
	} else {
		if m.agentEntry.BaseURL != "" {
			exports += fmt.Sprintf(" %s=%s", envBaseURL, shellQuote(m.agentEntry.BaseURL))
		}
		if m.agentEntry.AuthToken != "" {
			exports += fmt.Sprintf(" %s=%s", envAuthToken, shellQuote(m.agentEntry.AuthToken))
		}
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
	if len(m.modelEnv) > 0 {
		// Single source of truth: the CLI flag mirrors the set's ANTHROPIC_MODEL
		// (issue #480). A set without a model key (base_url-only profile) omits
		// --model and lets the CLI fall back to its own default.
		if model := modelFromModelEnv(m.modelEnv); model != "" {
			claude += " --model " + shellQuote(model)
		}
	} else if m.agentEntry.Model != "" {
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
