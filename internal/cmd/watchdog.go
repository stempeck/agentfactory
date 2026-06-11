package cmd

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
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
	watchdogAgent          string
	watchdogAgents         []string
	watchdogSilenceTimeout int
)

var watchdogCmd = &cobra.Command{
	Use:   "watchdog",
	Short: "Monitor agent sessions for failures and auto-recover",
	Long: `Watchdog is a long-lived polling loop that monitors agent tmux sessions
for error patterns, silence timeouts, and Claude crashes. On detection it
writes .runtime/last_error, mails the supervisor, and respawns the session.

Use --interval to set the polling frequency and --agent to monitor a single
agent instead of all. A circuit breaker stops respawning after consecutive
failures and escalates to the supervisor for manual intervention.`,
	RunE: runWatchdog,
}

func init() {
	watchdogCmd.Flags().IntVar(&watchdogInterval, "interval", 30, "Polling interval in seconds")
	watchdogCmd.Flags().StringVar(&watchdogAgent, "agent", "", "Monitor a specific agent (default: all)")
	watchdogCmd.Flags().StringSliceVar(&watchdogAgents, "agents", nil, "Monitor a set of agents (CSV/repeatable; default: all)")
	watchdogCmd.Flags().IntVar(&watchdogSilenceTimeout, "silence-timeout", 300, "Seconds of no output change before triggering recovery")
	rootCmd.AddCommand(watchdogCmd)
}

type watchdogAgentState struct {
	lastHash     string
	silenceCount int
}

var watchdogMaxConsecutiveFailures = 3

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

func resolveAgentDir(root, agentName string) string {
	meta, err := worktree.FindByAgent(root, agentName)
	if err == nil && meta != nil {
		return config.AgentDir(filepath.Join(root, meta.Path), agentName)
	}
	return config.AgentDir(root, agentName)
}

func resolveWorktreeMeta(root, agentName string) (*worktree.Meta, string) {
	meta, err := worktree.FindByAgent(root, agentName)
	if err == nil && meta != nil {
		return meta, filepath.Join(root, meta.Path)
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

	ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	interval := time.Duration(watchdogInterval) * time.Second
	silenceThreshold := watchdogSilenceTimeout / watchdogInterval
	if silenceThreshold < 1 {
		silenceThreshold = 1
	}

	agentStates := make(map[string]*watchdogAgentState)
	failures := make(map[string]int)

	// Scope folds the legacy single --agent into the multi-agent --agents set so
	// `af watchdog --agent X` keeps working (C-4). A nil scope means "all".
	scope := buildWatchdogScope(watchdogAgents, watchdogAgent)

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

// buildWatchdogScope builds the monitoring scope set, folding the single --agent
// value into the --agents set. It returns nil ("all") when no scope is requested
// so the existing bare-`af watchdog` and `--agent X` behaviors are unchanged.
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
	if len(scope) == 0 {
		return nil
	}
	return scope
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
		// scope is nil ⇒ all (C-4); non-nil ⇒ only members.
		if scope != nil {
			if _, in := scope[name]; !in {
				continue
			}
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
