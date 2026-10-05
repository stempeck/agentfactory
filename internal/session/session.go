package session

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
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

	ErrLaunchContributionsMissing = errors.New("session: Start/BuildStartupCommand called before SetLaunchContributions")
)

const (
	envBaseURL   = "ANTHROPIC_BASE_URL"
	envAuthToken = "ANTHROPIC_AUTH_TOKEN"

	// envAPIKey names the key the profile-key-universe hygiene must never clear (issue
	// #602). It is a legal profile key, so it appears in the union the cmd layer computes;
	// the carve-out that spares it lives in universeCarveOutVars. Named here rather than
	// inline so the exclusion reads the same as the redirect family's.
	envAPIKey = "ANTHROPIC_API_KEY"

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

	// envOTelHeaders is the telemetry channel's secret-bearing key (issue #329). Its
	// value is a header list (Name=value,…) whose value may be a file: ref, so it takes
	// the same inline deref ANTHROPIC_AUTH_TOKEN uses.
	envOTelHeaders = "OTEL_EXPORTER_OTLP_HEADERS"
)

// redirectFamilyVars enumerates the endpoint/model redirect env the launch chokepoint
// owns. The launch line's hygiene pass (issue #508) clears any of these NOT in
// the effective set so a profile switch on a reused session leaves no stale redirect
// var. The names and their order are owned by config.RedirectFamilyEnvVars (#695 Lift B,
// which also records why ANTHROPIC_API_KEY is excluded); this is a private copy so nothing
// here can reorder config's list.
var redirectFamilyVars = slices.Clone(config.RedirectFamilyEnvVars)

// telemetryFamilyVars enumerates the exact seven OTel launch-env vars this chokepoint owns
// (issue #329) — the fixed set telemetry.LaunchEnv builds. Like redirectFamilyVars they are
// iterated by EXCLUSION in the launch line's KEY='' hygiene loop: any of these NOT emitted by
// this launch is cleared, so a telemetry-off relaunch of a reused/respawned session leaves no
// stale OTel var behind. This family is kept SEPARATE from
// redirectFamilyVars and from the model-env `effective` bookkeeping so the two orthogonal
// channels never clear each other's vars. There is deliberately no content-capture gate here:
// the five content-capture log switches are never in this set and must never be added (the
// design's Privacy Posture; AC greps this file for their name-prefix and requires zero hits).
var telemetryFamilyVars = []string{
	"CLAUDE_CODE_ENABLE_TELEMETRY",
	"OTEL_METRICS_EXPORTER",
	"OTEL_LOGS_EXPORTER",
	"OTEL_EXPORTER_OTLP_PROTOCOL",
	"OTEL_EXPORTER_OTLP_ENDPOINT",
	envOTelHeaders,
	"OTEL_RESOURCE_ATTRIBUTES",
}

// afGatewayUpstreamAuthVars are the five gateway upstream-auth env names (issue #686 K2) —
// reserved for the factory-managed `.agentfactory/secrets/chatgpt/auth.json` handle, never a
// profile key (config.afGatewayUpstreamKeys denylists them at the config boundary; this list
// must stay byte-identical to that one). Cleared unconditionally on every launch line, exactly
// like redirectFamilyVars/telemetryFamilyVars, but kept in a separate, fourth family: unlike
// those two this family has no corresponding "carry" path — nothing in `effective` ever sets
// one of these keys, so this family is pure hygiene (clear-only), never export.
var afGatewayUpstreamAuthVars = []string{
	"OPENAI_API_KEY",
	"CHATGPT_TOKEN_DIR",
	"CHATGPT_AUTH_FILE",
	"CHATGPT_API_BASE",
	"CODEX_HOME",
}

// managerOwnedVars enumerates the env this Manager exports on its own authority — git
// identity, trailer activation, build host. config.validateModelProfile denylists only the
// AF_* identity and OTel keys, so an operator MAY legally name one of these in a profile,
// which would pull it into the profile-key universe. Inline, the universe's unset segment
// follows the export statement, so an uncarved key here would be exported and then wiped in
// the same command — a respawned agent would silently lose its git identity. That is PR
// #509 T1's auth-token clobber one class wider, so these are carved out rather than
// reordered around (reordering would move the first `&&` that several tests index on).
var managerOwnedVars = []string{
	envGitAuthorName,
	envGitAuthorEmail,
	envGitCommitterName,
	envGitCommitterEmail,
	envGitConfigCount,
	envGitConfigKey0,
	envGitConfigValue0,
	envCoauthorName,
	envCoauthorEmail,
	"AF_BUILD_MODE",
	"AF_BUILD_HOST",
	"AF_BUILD_USER",
	"AF_HOST_MOUNT",
}

// effortAttestationVars carry a selected effort level's attestation (#709). A reused session keeps
// the environment of the launch before it, so every launch that does not carry them clears them —
// otherwise a relaunch that selected nothing would go on attesting the previous session's reduction.
var effortAttestationVars = []string{config.EnvEffortObjective, config.EnvEffortStepLabel, config.EnvEffortFormula}

// universeCarveOutVars are the keys the profile-key-universe hygiene (issue #602) must never
// clear, even on a launch that does not carry them. Four groups, three reasons:
//
//   - redirectFamilyVars, telemetryFamilyVars and effortAttestationVars already own their keys
//     and clear them with the proven KEY='' idiom. Two idioms for two classes is deliberate;
//     letting the universe true-unset these would silently change the behavior #508, #329 and
//     #709 each pinned. (The attestation names are also denylisted from every profile, so they
//     never reach the universe; the carve-out is insurance, not the guard.)
//   - envAPIKey is a legal profile key so it lands in the union, but security.md I2 decides it
//     is never auto-cleared: a default-profile agent may authenticate via an ambient key.
//   - managerOwnedVars would otherwise be exported and immediately unset in one command.
//
// The carve-outs live in this package — the owner of the family lists and of the exports they
// protect — rather than in the cmd layer that computes the raw union (ADR-004).
var universeCarveOutVars = func() map[string]bool {
	out := map[string]bool{envAPIKey: true}
	for _, family := range [][]string{redirectFamilyVars, telemetryFamilyVars, effortAttestationVars, managerOwnedVars} {
		for _, key := range family {
			out[key] = true
		}
	}
	return out
}()

// shellCriticalVars are environment names the universe hygiene must never `unset`, even when a
// profile defines one that the current launch does not carry (issue #602 P1). Unlike
// universeCarveOutVars — keys the factory itself owns — these belong to the shell and loader the
// bare `claude` command runs under: `unset PATH` before `claude` leaves it unresolvable so a
// handoff/compact/watchdog respawn never relaunches, and the others would corrupt command lookup,
// home-dir resolution, or the dynamic loader the same way. A profile naming one is a
// misconfiguration, but the never-brick posture requires the cleanup to degrade to "leave it set"
// (recoverable) rather than emit a launch-breaking `unset`. This is a cleanup-side guard only: the
// write boundary does NOT reject these names (they are valid identifiers), so a dormant one never
// fails a registry load.
var shellCriticalVars = func() map[string]bool {
	out := map[string]bool{}
	for _, key := range config.ShellCriticalEnvVars {
		out[key] = true
	}
	return out
}()

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
	KillSession(name string) error //af:teardown:decl
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

// ambientCallerRole yields the AF_ROLE of the process invoking a lifecycle op — the
// caller's own agent identity, used by the K9 Manager.Stop interlock (#541). It is a seam,
// not an inline os.Getenv, because internal/session is a library package that must not read
// named env directly (env_hermetic_test.go, issue #98): ambient state enters at the cmd
// boundary and is injected here via SetAmbientCallerRole. The default returns "" — a pure
// library/test process carries no agent context, so the interlock treats it as operator/self
// and passes through.
var ambientCallerRole = func() string { return "" }

// SetAmbientCallerRole lets the cmd boundary inject the ambient AF_ROLE reader for the K9
// interlock (#541), keeping the named-env read in internal/cmd where it is permitted.
func SetAmbientCallerRole(fn func() string) { ambientCallerRole = fn }

// Manager handles agent session lifecycle operations.
type Manager struct {
	factoryRoot   string
	agentName     string
	agentEntry    config.AgentEntry
	tmux          tmuxClient
	initialPrompt string
	worktreePath  string
	worktreeID    string
	c             *LaunchContributions
}

// LaunchContributions is every config-derived value a launch line carries. The cmd layer composes
// it once per launch, so every launch path hands the emitter the same values.
type LaunchContributions struct {
	// ModelEnv is the resolved per-agent model-env export set (issue #480). Non-empty, it
	// supersedes the legacy entry Model/BaseURL/AuthToken emission (presence gate); empty values
	// are kept so a profile can clear an ambient var (e.g. ANTHROPIC_API_KEY='').
	ModelEnv []config.EnvVar
	// ModelKeyUniverse is every env key any models.json profile defines (issue #602). It emits
	// nothing; it only bounds what the hygiene pass may clear, outside ModelEnv's presence gate,
	// so a switch to no profile at all still clears. Order is kept as handed in (the cmd layer
	// sorts) so the launch line is deterministic.
	ModelKeyUniverse []string
	// TelemetryEnv is the OTel launch-env set (issue #329), nil when the gate is off. It must
	// never be merged into ModelEnv: a non-empty ModelEnv elides the legacy endpoint, so a
	// telemetry-only ModelEnv would silently drop a no-profile agent's endpoint.
	TelemetryEnv []config.EnvVar
	// GitAuthorName/GitAuthorEmail are empty unless no ambient identity resolves (issue #371
	// C-4), so the export never overrides a real identity. A non-empty GitHooksDir becomes
	// core.hooksPath and activates the co-author trailer (AC-4/AC-5).
	GitAuthorName, GitAuthorEmail            string
	GitHooksDir, CoauthorName, CoauthorEmail string
	BuildHost                                *config.BuildHostConfig // nil ⇒ no AF_BUILD_*

	PluginDirs             []string
	IntegrationEnv         []config.EnvVar
	IntegrationKeyUniverse []string
	HookFailModes          string
}

// SetLaunchContributions hands the Manager its composed contributions. Without it Start and
// BuildStartupCommand refuse, so a launch path that skips the composer launches nothing.
func (m *Manager) SetLaunchContributions(c *LaunchContributions) {
	m.c = c
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
// by the emitter to decide whether a legacy endpoint must still be emitted: a
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

// staleUniverseKeys returns the profile-key-universe keys this launch does NOT carry and is
// allowed to clear: everything in the universe minus what effective records as emitted, minus
// the carve-outs. Order follows the universe as handed in (sorted by
// the cmd layer), which is what keeps the emitted unset segment deterministic across runs.
func (m *Manager) staleUniverseKeys(effective map[string]bool) []string {
	var stale []string
	seen := map[string]bool{}
	for _, key := range slices.Concat(m.c.ModelKeyUniverse, m.c.IntegrationKeyUniverse) {
		if effective[key] || universeCarveOutVars[key] || seen[key] {
			continue
		}
		seen[key] = true
		// A profile key rides into `unset K1 K2 …` unquoted (issue #602 P1/F1), so a name that is
		// not a safe shell identifier (a space or shell metacharacter) or a shell/loader-critical
		// name (PATH …) must never reach the emitted segment. IsValidEnvKeyName is
		// the same predicate the write boundary rejects by, so the two cannot disagree.
		if shellCriticalVars[key] || !config.IsValidEnvKeyName(key) {
			continue
		}
		stale = append(stale, key)
	}
	return stale
}

// StaleTmuxEnvKeys returns every key an older af wrote into the tmux SESSION env, which a respawn
// reuses and so still inherits: the families this package owns, the effort level and the
// profile-key universe. The launch line's own exports override anything inherited, so the set
// ignores what this launch carries. envAPIKey is never auto-cleared (security.md I2), and
// shell-critical or non-identifier universe names are dropped as in staleUniverseKeys. Integration
// keys are absent because no af ever wrote one to tmux.
func (m *Manager) StaleTmuxEnvKeys() []string {
	var keys []string
	seen := map[string]bool{}
	families := slices.Concat(redirectFamilyVars, telemetryFamilyVars, afGatewayUpstreamAuthVars, effortAttestationVars, managerOwnedVars, []string{config.EnvEffortLevel})
	for _, key := range slices.Concat(families, m.c.ModelKeyUniverse) {
		if seen[key] || key == envAPIKey || shellCriticalVars[key] || !config.IsValidEnvKeyName(key) {
			continue
		}
		seen[key] = true
		keys = append(keys, key)
	}
	return keys
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
	if m.c == nil {
		return ErrLaunchContributionsMissing
	}

	present, live := m.probe()
	if live {
		return ErrAlreadyRunning
	}
	if present {
		// Zombie — tmux alive but Claude dead. Kill and recreate.
		if err := m.tmux.KillSession(sessionID); err != nil { //af:teardown:restorative
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
		_ = m.tmux.KillSession(sessionID) //af:teardown:restorative
		return fmt.Errorf("waiting for shell: %w", err)
	}

	// Pre-flight memory check before launching Claude
	availMB, memErr := checkAvailableMemoryFunc()
	if memErr == nil && availMB < 512 {
		_ = m.tmux.KillSession(sessionID) //af:teardown:restorative
		return fmt.Errorf("insufficient memory to launch Claude: %dMB available, 512MB required", availMB)
	}

	// Build startup command with inline exports
	startupCmd := m.buildStartupCommand()

	// Send startup command after brief delay
	if err := m.tmux.SendKeysDelayed(sessionID, startupCmd, 200); err != nil {
		_ = m.tmux.KillSession(sessionID) //af:teardown:restorative
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
	// effective records the keys this launch actually emits, so the hygiene passes can clear the
	// rest. Function-scoped because the universe pass below runs OUTSIDE the model-env gate, on
	// the no-profile path where that gate never opens, and must still see what was emitted.
	effective := map[string]bool{}
	if len(m.c.ModelEnv) > 0 {
		// Resolved model-env set supersedes the legacy fields (issue #480), in the
		// same slot the legacy exports occupied. Every value is single-quoted via
		// shellQuote (shell-injection inert); an empty value emits KEY='' to clear it.
		for _, ev := range m.c.ModelEnv {
			// A file:<path> ANTHROPIC_AUTH_TOKEN is dereferenced to "$(cat '<abs>')" so
			// the pane shell reads the secret at exec time — the value never lands on
			// the launch line or in scrollback (issue #508). Only the path passes
			// through shellQuote; the surrounding double-quotes and $(cat …) are
			// literal, because shellQuote would single-quote the whole token and
			// disable the command substitution. A relative path resolves against the
			// factory root so $(cat …) reads the right file whatever the pane's cwd.
			if ev.Key == envAuthToken && strings.HasPrefix(ev.Value, secretRefPrefix) {
				exports += " " + m.derefFileRefInline(ev.Key, "", strings.TrimPrefix(ev.Value, secretRefPrefix))
			} else {
				exports += fmt.Sprintf(" %s=%s", ev.Key, shellQuote(ev.Value))
			}
			effective[ev.Key] = true
		}
		// A model-only resolved set carries no endpoint; keep the legacy BaseURL/
		// AuthToken travelling with it (PR #482: regression of #262).
		if !modelEnvHasKey(m.c.ModelEnv, envBaseURL) {
			if m.agentEntry.BaseURL != "" {
				exports += fmt.Sprintf(" %s=%s", envBaseURL, shellQuote(m.agentEntry.BaseURL))
				effective[envBaseURL] = true
			}
			if m.agentEntry.AuthToken != "" {
				exports += fmt.Sprintf(" %s=%s", envAuthToken, shellQuote(m.agentEntry.AuthToken))
				effective[envAuthToken] = true
			}
		}
		// Redirect-var hygiene: emit an explicit KEY='' for every redirect-family var this
		// launch does NOT carry, so a value a prior profile left on a reused session survives
		// no switch. This is the ONLY clear any launch emits, so it must cover the whole family —
		// not just base_url/auth_token (issue #508). Computed on the EFFECTIVE env AFTER the
		// legacy carry so a carried endpoint
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
	// Telemetry env (issue #329), independent of the model-env gate above. A file: ref inside
	// OTEL_EXPORTER_OTLP_HEADERS (shape Name=file:<path>) is dereferenced to
	// `Name='"$(cat '<abs>')"` so the pane shell reads the secret at exec time. telemEffective is
	// keyed only on the telemetry family. The KEY='' hygiene loop below is the ONLY clear any
	// launch emits, so it must cover the whole telemetry family — that is what makes a
	// telemetry-off relaunch drop a prior run's OTel vars.
	telemEffective := map[string]bool{}
	for _, ev := range m.c.TelemetryEnv {
		if ev.Key == envOTelHeaders {
			if i := strings.Index(ev.Value, secretRefPrefix); i >= 0 {
				exports += " " + m.derefFileRefInline(ev.Key, ev.Value[:i], ev.Value[i+len(secretRefPrefix):])
				telemEffective[ev.Key] = true
				continue
			}
		}
		exports += fmt.Sprintf(" %s=%s", ev.Key, shellQuote(ev.Value))
		telemEffective[ev.Key] = true
	}
	for _, key := range telemetryFamilyVars {
		if !telemEffective[key] {
			exports += fmt.Sprintf(" %s=''", key)
		}
	}
	// Gateway upstream-auth hygiene (issue #686 K2), independent of the model-env gate above.
	// Unconditional because this
	// family has no legitimate carry path, so the clear must not depend on `effective` being
	// correct.
	for _, key := range afGatewayUpstreamAuthVars {
		exports += fmt.Sprintf(" %s=''", key)
	}
	for _, key := range effortAttestationVars {
		if !effective[key] {
			exports += fmt.Sprintf(" %s=''", key)
		}
	}
	// Git identity fallback (presence-gated — only when no ambient identity resolved).
	if m.c.GitAuthorName != "" && m.c.GitAuthorEmail != "" {
		exports += fmt.Sprintf(" %s=%s %s=%s %s=%s %s=%s",
			envGitAuthorName, shellQuote(m.c.GitAuthorName),
			envGitAuthorEmail, shellQuote(m.c.GitAuthorEmail),
			envGitCommitterName, shellQuote(m.c.GitAuthorName),
			envGitCommitterEmail, shellQuote(m.c.GitAuthorEmail))
	}
	// Trailer activation: redirect git hook lookup to the af-managed githooks dir
	// (via core.hooksPath, ADR-017-clean) and hand the hook the co-author value.
	if m.c.GitHooksDir != "" {
		exports += fmt.Sprintf(" %s=1 %s=%s %s=%s",
			envGitConfigCount,
			envGitConfigKey0, shellQuote("core.hooksPath"),
			envGitConfigValue0, shellQuote(m.c.GitHooksDir))
		if m.c.CoauthorName != "" && m.c.CoauthorEmail != "" {
			exports += fmt.Sprintf(" %s=%s %s=%s",
				envCoauthorName, shellQuote(m.c.CoauthorName),
				envCoauthorEmail, shellQuote(m.c.CoauthorEmail))
		}
	}
	if m.c.BuildHost != nil {
		exports += fmt.Sprintf(" AF_BUILD_MODE=%s", shellQuote(m.c.BuildHost.Mode))
		if m.c.BuildHost.Host != "" {
			exports += fmt.Sprintf(" AF_BUILD_HOST=%s", shellQuote(m.c.BuildHost.Host))
		}
		if m.c.BuildHost.User != "" {
			exports += fmt.Sprintf(" AF_BUILD_USER=%s", shellQuote(m.c.BuildHost.User))
		}
		if m.c.BuildHost.MountPath != "" {
			exports += fmt.Sprintf(" AF_HOST_MOUNT=%s", shellQuote(m.c.BuildHost.MountPath))
		}
	}
	// Integration env (design K9) follows every af-owned family, and a key one of them already
	// emitted is skipped so an integration can never redirect model or telemetry traffic. An af
	// launch key is skipped by membership even when af emitted none, because most of them sit
	// behind a presence gate and an integration must not fill the gap; it is not recorded in
	// effective, which would cancel its stale-key unset. An invalid key is dropped outright — not
	// exported and not unset — because it would otherwise ride the launch line unquoted; the
	// composer reports both.
	for _, ev := range m.c.IntegrationEnv {
		if effective[ev.Key] || telemEffective[ev.Key] || config.IsAFLaunchKey(ev.Key) || !config.IsValidEnvKeyName(ev.Key) {
			continue
		}
		if strings.HasPrefix(ev.Value, secretRefPrefix) {
			exports += " " + m.derefFileRefInline(ev.Key, "", strings.TrimPrefix(ev.Value, secretRefPrefix))
		} else {
			exports += fmt.Sprintf(" %s=%s", ev.Key, shellQuote(ev.Value))
		}
		effective[ev.Key] = true
	}
	if m.c.HookFailModes != "" {
		exports += fmt.Sprintf(" AF_INTEGRATION_HOOK_FAIL_MODES=%s", shellQuote(m.c.HookFailModes))
		effective["AF_INTEGRATION_HOOK_FAIL_MODES"] = true
	}

	claude := "claude --dangerously-skip-permissions"
	if len(m.c.ModelEnv) > 0 {
		// Single source of truth: the CLI flag mirrors the set's ANTHROPIC_MODEL
		// (issue #480). A set without a model key (base_url-only profile) omits
		// --model and lets the CLI fall back to its own default.
		if model := modelFromModelEnv(m.c.ModelEnv); model != "" {
			claude += " --model " + shellQuote(model)
		}
	} else if m.agentEntry.Model != "" {
		claude += " --model " + shellQuote(m.agentEntry.Model)
	}
	// Outside the claude literal above so a launch that binds nothing stays byte-identical.
	for _, dir := range m.c.PluginDirs {
		claude += " --plugin-dir " + shellQuote(dir)
	}
	if m.initialPrompt != "" {
		claude += " " + shellQuote(m.initialPrompt)
	}

	// Profile-key-universe hygiene (issue #602), the ONLY clear any launch emits for this class.
	// It clears by a TRUE `unset` rather than the families' KEY='': the host's handling of an
	// empty value for these keys is unverified, whereas unset makes "absent" byte-identical to
	// "never launched with the key". `unset` cannot ride the export statement — `export A=1
	// unset B` parses, but exports a variable literally named `unset` — so it takes its own
	// command segment, and the launch line grows from two segments to three.
	//
	// The segment is emitted ONLY when something is actually stale. That is what holds the
	// zero-delta contract: with nothing to clear this reduces to exports + " && " + claude,
	// byte-identical to the single Sprintf it replaced, so a factory that never defines such a
	// key sees no change at all. `&&` rather than `;` keeps the short-circuit chain the
	// `sleep N && ` respawn prefix relies on; `unset` returns 0 for names that are not set, so
	// it never breaks that chain.
	segments := []string{exports}
	if clears := m.staleUniverseKeys(effective); len(clears) > 0 {
		segments = append(segments, "unset "+strings.Join(clears, " "))
	}
	segments = append(segments, claude)
	return strings.Join(segments, " && ")
}

// derefFileRefInline renders one inline `KEY=<deref>` export for a file: secret reference:
// the pane shell reads the secret via $(cat …) at exec time, so it never lands on the launch
// line or in scrollback (issue #508). A relative path resolves against the factory root so the
// read is correct whatever the pane's cwd. prefix is the literal text preserved ahead of the
// deref: empty for a whole-value ref (ANTHROPIC_AUTH_TOKEN → `file:<path>`), or a `Name=` header
// segment for OTEL_EXPORTER_OTLP_HEADERS → `Name=file:<path>` (issue #329). Only the path passes
// through shellQuote; the surrounding double-quotes and $(cat …) are literal, because
// single-quoting the whole token would disable the command substitution. Adjacent shell quoting
// (`'Name='"$(cat '<abs>')"`) concatenates the preserved prefix and the secret at exec time.
func (m *Manager) derefFileRefInline(key, prefix, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.factoryRoot, path)
	}
	if prefix == "" {
		return fmt.Sprintf("%s=\"$(cat %s)\"", key, shellQuote(path))
	}
	return fmt.Sprintf("%s=%s\"$(cat %s)\"", key, shellQuote(prefix), shellQuote(path))
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

	// K9 interlock (#541): in agent context, refuse to stop a NON-SELF interactive-type
	// target (the control plane, e.g. the manager) before any disruptive action, so an
	// agent cannot kill the interactive manager. It uses ONLY the primitive AF_ROLE signal
	// (via the ambientCallerRole seam) and agentEntry.Type — NOT cmd's callerAuthority
	// classifier — because internal/session must not import internal/cmd (cmd imports
	// session; the reverse would cycle — conflicts.md E3). It is a primitive-level backstop
	// for a future ungated caller, not primary enforcement (that is the Phase 3 command
	// gates); self-stop and specialist/autonomous redispatch pass through untouched.
	//
	// The AF_ROLE read is deliberately NOT an inline os.Getenv here: internal/session is a
	// library package that must not read named env directly (env_hermetic_test.go, issue
	// #98). Per that invariant the ambient value enters at the cmd boundary and is injected
	// via SetAmbientCallerRole; the default returns "" so a pure library/test process
	// classifies as operator/self and passes through unchanged.
	if role := ambientCallerRole(); role != "" && role != m.agentName && m.agentEntry.Type == "interactive" {
		return fmt.Errorf("refusing to stop interactive agent %q from agent context %q: factory control-plane teardown is an operator action", m.agentName, role)
	}

	// Graceful: send Ctrl-C first
	_ = m.tmux.SendKeysRaw(sessionID, "C-c")
	time.Sleep(100 * time.Millisecond)

	// Kill the session
	if err := m.tmux.KillSession(sessionID); err != nil { //af:teardown:gated
		return fmt.Errorf("killing session: %w", err)
	}

	// Release lock (best-effort)
	_ = lock.New(m.workDir()).Release()
	_ = lock.NewWithPath(filepath.Join(m.workDir(), ".runtime", "fidelity-gate.lock")).Release()
	_ = lock.NewWithPath(filepath.Join(m.workDir(), ".runtime", "quality-gate.lock")).Release()

	return nil
}

// IsRunning reports only whether the agent's tmux session exists (HasSession), which
// is true of a zombie session too.
//
// Deprecated: use Live to ask whether the agent is running.
func (m *Manager) IsRunning() (bool, error) {
	return m.tmux.HasSession(m.SessionID())
}

func (m *Manager) probe() (present, live bool) {
	id := m.SessionID()
	present, _ = m.tmux.HasSession(id)
	return present, present && m.tmux.IsClaudeRunning(id)
}

// Live reports whether Claude is running in the agent's session. Unlike IsRunning, a zombie
// session (tmux alive, Claude dead) is not live: Start kills and relaunches it.
func (m *Manager) Live() bool {
	_, live := m.probe()
	return live
}

// BuildStartupCommand returns the launch line a respawn hands to RespawnPane.
func (m *Manager) BuildStartupCommand() (string, error) {
	if m.c == nil {
		return "", ErrLaunchContributionsMissing
	}
	return m.buildStartupCommand(), nil
}

// buildNudge constructs the startup nudge message.
// Returns empty string when an initial prompt is set (task delivered via CLI arg).
// Appends the agent's custom directive (from agents.json) if set.
func (m *Manager) buildNudge() string {
	if m.initialPrompt != "" {
		return ""
	}
	// "check mail" left with #675: the SessionStart hooks deliver mail on their own, so prime is no
	// longer where an agent learns it has any. The `af prime` instruction itself stays — it is what
	// loads identity and formula context.
	nudge := "Run `af prime` to load your context and begin work."
	if m.agentEntry.Directive != "" {
		nudge += " " + m.agentEntry.Directive
	}
	return nudge
}

// BuildNudge returns the startup nudge message for testing.
func (m *Manager) BuildNudge() string {
	return m.buildNudge()
}
