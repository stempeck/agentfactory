package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
	"github.com/stempeck/agentfactory/internal/worktree"
)

var (
	watchdogInterval       int
	watchdogSilenceTimeout int
)

var watchdogCmd = &cobra.Command{
	Use:   "watchdog",
	Short: "Monitor agent sessions for failures and auto-recover",
	Long: `Watchdog is a long-lived polling loop that monitors agent tmux sessions
for error patterns, silence timeouts, and Claude crashes. On detection it
writes .runtime/last_error, mails the supervisor, and respawns the session.

Scope comes solely from startup.json.watchdog_agents — the explicit, bounded set
of agents to monitor. The watchdog refuses to start when none is configured
(issue #408); it never monitors all agents. Use --interval to set the polling
frequency. A circuit breaker stops respawning after consecutive failures and
escalates to the supervisor for manual intervention.`,
	RunE: runWatchdog,
}

func init() {
	watchdogCmd.Flags().IntVar(&watchdogInterval, "interval", 30, "Polling interval in seconds")
	watchdogCmd.Flags().IntVar(&watchdogSilenceTimeout, "silence-timeout", 300, "Seconds of no output change before triggering recovery")
	rootCmd.AddCommand(watchdogCmd)
}

type watchdogAgentState struct {
	lastHash     string
	silenceCount int
}

var watchdogMaxConsecutiveFailures = 3

// watchdogNudgeFn nudges a silent agent. It is copy-mode-resilient (Issue #412
// Fix B, K-WATCH defense-in-depth) WITHOUT any change here: SendKeys ->
// SendKeysDebounced calls (*Tmux).exitCopyMode, which drops a pane latched in
// copy-mode back to live view before the "continue" reaches it. We intentionally
// do NOT cancel copy-mode in pollAgents directly — exitCopyMode is unexported and
// the watchdogTmux interface exposes no send/cancel method, so a literal cancel
// here would require a new exported tmux API for no added protection (the
// transitive coverage above already closes the C-CRIT-2 autonomy trap).
var watchdogNudgeFn = func(sessionID string) error {
	tx := tmux.NewTmux()
	return tx.SendKeys(sessionID, "continue")
}

// watchdogTmux is the subset of *tmux.Tmux that pollAgents needs to inspect a
// session. It exists so the poll loop can be driven with a fake in tests.
type watchdogTmux interface {
	HasSession(name string) (bool, error)
	IsClaudeRunning(session string) bool
	CapturePane(session string, lines int) (string, error)
}

// newWatchdogTmux is the seam tests override to inject a fake tmux client.
var newWatchdogTmux = func() watchdogTmux { return tmux.NewTmux() }

func handleSilenceNudge(sessionID, agentName string, agentStates map[string]*watchdogAgentState, failures map[string]int) {
	_ = watchdogNudgeFn(sessionID)
	if s, ok := agentStates[agentName]; ok {
		s.silenceCount = 0
	}
}

func checkpointBeforeKill(agentDir, reason string) {
	cp, err := checkpoint.Capture(agentDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: checkpoint capture failed: %v\n", err)
		return
	}
	cp.WithNotes(fmt.Sprintf("WATCHDOG: %s", reason))
	if writeErr := checkpoint.Write(agentDir, cp); writeErr != nil {
		fmt.Fprintf(os.Stderr, "watchdog: checkpoint write failed: %v\n", writeErr)
	}
}

func detectErrorPattern(output string) (bool, string) {
	if strings.Contains(output, "Invalid signature in thinking block") {
		return true, "Invalid signature in thinking block"
	}
	return false, ""
}

func checkSilence(agentName, output string, state map[string]*watchdogAgentState, threshold int) bool {
	h := sha256.Sum256([]byte(strings.TrimSpace(output)))
	hash := hex.EncodeToString(h[:])

	s, ok := state[agentName]
	if !ok {
		s = &watchdogAgentState{}
		state[agentName] = s
	}

	if s.lastHash == "" {
		s.lastHash = hash
		s.silenceCount = 1
		return false
	}

	if hash == s.lastHash {
		s.silenceCount++
		return s.silenceCount >= threshold
	}

	s.lastHash = hash
	s.silenceCount = 0
	return false
}

func shouldRespawn(failures map[string]int, agentName string, maxFailures int) bool {
	return failures[agentName] < maxFailures
}

func resetCircuitBreaker(failures map[string]int, agentName string) {
	failures[agentName] = 0
}

func shouldAutoRecover(agentType string) bool {
	return agentType != "interactive"
}

func checkCircuitBreaker(failures map[string]int, name string) bool {
	if shouldRespawn(failures, name, watchdogMaxConsecutiveFailures) {
		return false
	}
	if failures[name] == watchdogMaxConsecutiveFailures {
		_ = sendHandoffMail(escalationTarget,
			fmt.Sprintf("WATCHDOG CIRCUIT BREAKER: %s failed %d consecutive recoveries. Manual intervention required.",
				name, watchdogMaxConsecutiveFailures),
			fmt.Sprintf("Agent %s has failed %d consecutive recovery attempts. The watchdog has stopped auto-recovery. Please investigate manually.",
				name, watchdogMaxConsecutiveFailures))
		failures[name]++
	}
	return true
}

func writeLastError(agentDir, description string) error {
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), description)
	return os.WriteFile(filepath.Join(runtimeDir, "last_error"), []byte(content), 0o644)
}

// writeWatchdogLastError writes a timestamped breadcrumb to
// <root>/.runtime/watchdog_last_error — the watchdog process's own (factory-root)
// error record. It is DISTINCT from writeLastError, whose
// <agentDir>/.runtime/last_error is the per-agent recovery record used by
// recoverAgent; a factory-root last_error would be ambiguous with an agent's
// (issue #408 R2-L1). The `af up` pre-check (Phase 3) points at this same file so
// operators have a single place to look.
func writeWatchdogLastError(root, description string) error {
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return err
	}
	content := fmt.Sprintf("%s %s\n", time.Now().Format(time.RFC3339), description)
	return os.WriteFile(filepath.Join(runtimeDir, "watchdog_last_error"), []byte(content), 0o644)
}

func resolveAgentDir(root, agentName string) string {
	meta, err := worktree.FindByAgent(root, agentName)
	if err == nil && meta != nil {
		return config.AgentDir(worktree.AbsWorktreePath(root, meta), agentName)
	}
	return config.AgentDir(root, agentName)
}

func resolveWorktreeMeta(root, agentName string) (*worktree.Meta, string) {
	meta, err := worktree.FindByAgent(root, agentName)
	if err == nil && meta != nil {
		return meta, worktree.AbsWorktreePath(root, meta)
	}
	return nil, ""
}

func recoverAgent(root, agentName string, entry config.AgentEntry, pattern string) {
	agentDir := resolveAgentDir(root, agentName)
	checkpointBeforeKill(agentDir, pattern)
	if err := writeLastError(agentDir, pattern); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: %s: failed to write last_error: %v\n", agentName, err)
	}

	_ = sendHandoffMail(escalationTarget,
		fmt.Sprintf("WATCHDOG: %s session failure detected: %s", agentName, pattern),
		fmt.Sprintf("Watchdog detected failure in agent %s: %s. Session will be respawned.", agentName, pattern))

	if !shouldAutoRecover(entry.Type) {
		fmt.Fprintf(os.Stderr, "watchdog: %s: interactive agent, alert-only (no respawn)\n", agentName)
		return
	}

	meta, absWtPath := resolveWorktreeMeta(root, agentName)
	opts := RespawnOptions{
		FactoryRoot: root,
		AgentName:   agentName,
		AgentEntry:  entry,
		PaneID:      session.SessionName(agentName),
	}
	if meta != nil {
		opts.WorktreePath = absWtPath
		opts.WorktreeID = meta.ID
	}
	if err := respawnSession(opts); err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: %s: respawn failed: %v\n", agentName, err)
	} else {
		fmt.Fprintf(os.Stderr, "watchdog: %s: session respawned\n", agentName)
	}
}

// resolveWatchdogRoot determines the factory root for the watchdog (#309 W2).
// It consults AF_ROOT first — exported into the watchdog session by `af up` —
// so the watchdog survives a deleted working directory (e.g. a worktree removed
// out from under it). It falls back to the cwd only when AF_ROOT is unset or no
// longer points at a valid factory. Reading AF_ROOT in internal/cmd is permitted
// by ADR-004 (the env-hermeticity ban exempts internal/cmd).
func resolveWatchdogRoot() (string, error) {
	if afRoot := os.Getenv("AF_ROOT"); afRoot != "" {
		if root, err := config.FindFactoryRoot(afRoot); err == nil {
			return root, nil
		}
	}
	wd, err := getWd()
	if err != nil {
		return "", err
	}
	return config.FindFactoryRoot(wd)
}

func runWatchdog(cmd *cobra.Command, args []string) error {
	root, err := resolveWatchdogRoot()
	if err != nil {
		return err
	}

	// The watchdog is the single authority for its scope (issue #408 Phase 2): it
	// self-reads startup.json.watchdog_agents (NOT the CLI flags) and refuses to
	// start on an empty or all-unknown scope, leaving a durable breadcrumb before
	// refusing so the misconfiguration is loud and discoverable.
	ws, err := resolveWatchdogScope(root)
	if err != nil {
		if werr := writeWatchdogLastError(root, err.Error()); werr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "watchdog: failed to write breadcrumb: %v\n", werr)
		}
		return err
	}
	if ws.membershipNote != "" {
		// Transient-read guard (N-2): membership could not be validated, so the
		// scope launches unvalidated. Surface it both ways so "monitoring nothing
		// because of a flaky read" stays discoverable.
		fmt.Fprintln(cmd.ErrOrStderr(), ws.membershipNote)
		_ = writeWatchdogLastError(root, ws.membershipNote)
	}
	// Per-name typos stay non-fatal: warn, but keep monitoring the known names.
	for _, name := range ws.unknown {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"watchdog: startup.json watchdog_agents names unknown agent %q — it will NOT be monitored\n", name)
	}
	scope := ws.agents

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	interval := time.Duration(watchdogInterval) * time.Second
	silenceThreshold := watchdogSilenceTimeout / watchdogInterval
	if silenceThreshold < 1 {
		silenceThreshold = 1
	}

	agentStates := make(map[string]*watchdogAgentState)
	failures := make(map[string]int)

	fmt.Fprintf(cmd.OutOrStdout(), "watchdog: started (interval=%ds, silence-timeout=%ds)\n",
		watchdogInterval, watchdogSilenceTimeout)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(cmd.OutOrStdout(), "watchdog: shutting down\n")
			return nil
		case <-ticker.C:
			pollAgents(cmd, root, scope, agentStates, failures, silenceThreshold)
		}
	}
}

// buildWatchdogScope builds the monitoring scope set from a list of agent names,
// folding an optional single name into the set (blank names are dropped). An empty
// result is a non-nil EMPTY map meaning "no-scope" — monitor NOTHING (issue #408).
// A nil/empty scope is never "all"; pollAgents fail-closes on it.
func buildWatchdogScope(agents []string, single string) map[string]struct{} {
	scope := make(map[string]struct{})
	for _, a := range agents {
		if a = strings.TrimSpace(a); a != "" {
			scope[a] = struct{}{}
		}
	}
	if single != "" {
		scope[single] = struct{}{}
	}
	return scope
}

// watchdogScope is the resolved monitoring scope plus the membership metadata the
// caller needs for non-fatal observability: per-name typo warnings (unknown) and
// the transient-read note (membershipNote). When resolveWatchdogScope returns a
// nil error, agents is the non-empty set to monitor.
type watchdogScope struct {
	agents         map[string]struct{} // the set to monitor (non-empty on success)
	unknown        []string            // configured names absent from agents.json (typos)
	membershipNote string              // non-empty when the all-unknown check was skipped (transient read)
}

// resolveWatchdogScope is the watchdog's single authority for its scope (issue
// #408 Phase 2). It self-reads startup.json.watchdog_agents (NOT the CLI flags)
// under root, folds it through the Phase-1 buildWatchdogScope contract, and
// validates membership against agents.json. It returns a non-nil error — so the
// caller (a cobra RunE) exits non-zero — when the watchdog must refuse to start:
//
//   - the resolved scope is empty (absent file, omitted field, [], or all-blank), or
//   - the scope is non-empty but EVERY name is unknown vs agents.json (R2-H1).
//
// Membership keys on agents.json, NOT on a live session: a configured-but-not-
// running agent is "known". A failed/partial agents.json read is NOT escalated to
// an all-unknown refusal (transient-read guard, N-2); the configured scope launches
// unvalidated with membershipNote set. The refuse/membership decision lives here in
// the cmd layer, never in internal/config (ADR-004). The agents.json read here is a
// read-once-at-start snapshot, separate from pollAgents' per-tick read (N-1).
func resolveWatchdogScope(root string) (watchdogScope, error) {
	startupCfg, err := config.LoadStartupConfig(root)
	if err != nil {
		return watchdogScope{}, err
	}

	// Source the scope from startup.json (not flags), reusing the Phase-1 contract
	// so blank entries drop and an empty result is the non-nil empty map.
	scope := buildWatchdogScope(startupCfg.WatchdogAgents, "")
	if len(scope) == 0 {
		return watchdogScope{}, fmt.Errorf(
			"watchdog: refusing to start — no watchdog_agents configured. "+
				"The watchdog only monitors an explicit, bounded set of agents (issue #408). "+
				"Set \"watchdog_agents\" in %s or it will not start.",
			config.StartupConfigPath(root))
	}

	agentsCfg, agErr := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if agErr != nil || agentsCfg == nil {
		// Transient/partial read (unreadable/absent agents.json): do NOT treat as
		// all-unknown. Prefer launching on the configured (non-empty) scope over
		// refusing on a flaky read. A successfully-parsed but EMPTY map is NOT a
		// transient read — it falls through to the all-unknown refusal below (#408/PR#410).
		return watchdogScope{
			agents: scope,
			membershipNote: "watchdog: could not validate scope membership — agents.json " +
				"unreadable; launching on configured scope unvalidated",
		}, nil
	}

	// Membership = a plain agents.json map lookup (the warnUnknownWatchdogAgents
	// idiom). Refuse only when EVERY configured name is unknown.
	var unknown []string
	known := 0
	for name := range scope {
		if _, ok := agentsCfg.Agents[name]; ok {
			known++
		} else {
			unknown = append(unknown, name)
		}
	}
	sort.Strings(unknown)
	if known == 0 {
		return watchdogScope{}, fmt.Errorf(
			"watchdog: refusing to start — none of watchdog_agents {%s} exist in agents.json; "+
				"nothing to monitor (issue #408). Fix the names or set a valid watchdog_agents.",
			strings.Join(unknown, ", "))
	}

	return watchdogScope{agents: scope, unknown: unknown}, nil
}

func pollAgents(cmd *cobra.Command, root string, scope map[string]struct{}, agentStates map[string]*watchdogAgentState, failures map[string]int, silenceThreshold int) {
	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "watchdog: failed to load agents config: %v\n", err)
		return
	}

	tx := newWatchdogTmux()

	for name, entry := range agentsCfg.Agents {
		// nil/empty scope monitors NOTHING — the watchdog must refuse before
		// reaching here (issue #408); a nil scope is never "all".
		if _, in := scope[name]; !in {
			continue
		}

		sessionID := session.SessionName(name)

		running, err := tx.HasSession(sessionID)
		if err != nil || !running {
			continue
		}

		if !tx.IsClaudeRunning(sessionID) {
			if checkCircuitBreaker(failures, name) {
				continue
			}
			failures[name]++
			recoverAgent(root, name, entry, "Claude process crashed")
			continue
		}

		output, err := tx.CapturePane(sessionID, 50)
		if err != nil {
			continue
		}

		if detected, pattern := detectErrorPattern(output); detected {
			if checkCircuitBreaker(failures, name) {
				continue
			}
			failures[name]++
			recoverAgent(root, name, entry, pattern)
			continue
		}

		if checkSilence(name, output, agentStates, silenceThreshold) {
			// Interactive agents are human-supervised: like the crash path
			// (recoverAgent), never act on them automatically — no nudge.
			if shouldAutoRecover(entry.Type) {
				handleSilenceNudge(sessionID, name, agentStates, failures)
			}
			continue
		}

		resetCircuitBreaker(failures, name)
	}
}
