package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

var dispatchDryRun bool
var dispatchStartInterval int

var dispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Run a single GitHub issue dispatch cycle",
	Long: `Dispatch queries GitHub for issues with the configured trigger label,
matches issue labels to agents, and dispatches work via af sling.

The dispatch cycle:
  1. Load .agentfactory/dispatch.json and .agentfactory/agents.json
  2. Check gh auth status
  3. Query each configured repo for open issues with the trigger label
  4. Match issue labels to agent mappings
  5. Skip issues already dispatched (within 24h TTL) or with busy agents
  6. Dispatch via af sling --agent <name> --reset <issue-url>
  7. Save dispatch state to .runtime/dispatch-state.json

Use --dry-run to preview what would be dispatched without acting.`,
	RunE: runDispatch,
}

func init() {
	dispatchCmd.Flags().BoolVar(&dispatchDryRun, "dry-run", false, "Preview what would be dispatched without acting")
	rootCmd.AddCommand(dispatchCmd)

	startCmd := &cobra.Command{
		Use:   "start",
		Short: "Start background dispatch polling",
		Long: `Start launches a tmux session that polls GitHub for issues at a
configurable interval. The session runs: af dispatch in a loop.

Use --interval to override the interval_seconds from dispatch.json.`,
		RunE: runDispatchStart,
	}
	startCmd.Flags().IntVar(&dispatchStartInterval, "interval", 0, "Override polling interval in seconds")
	dispatchCmd.AddCommand(startCmd)

	stopCmd := &cobra.Command{
		Use:   "stop",
		Short: "Stop background dispatch polling",
		Long:  "Stop kills the af-dispatch tmux session, ending background polling.",
		RunE:  runDispatchStop,
	}
	dispatchCmd.AddCommand(stopCmd)

	statusCmd := &cobra.Command{
		Use:   "status",
		Short: "Show dispatcher status and dispatch history",
		Long:  "Status shows whether the dispatcher is running and lists dispatched issues with agent status and age.",
		RunE:  runDispatchStatus,
	}
	dispatchCmd.AddCommand(statusCmd)
}

// ghIssue represents a GitHub issue from gh CLI JSON output.
type ghIssue struct {
	Number int       `json:"number"`
	Title  string    `json:"title"`
	URL    string    `json:"url"`
	Labels []ghLabel `json:"labels"`
}

// ghLabel represents a GitHub label.
type ghLabel struct {
	Name string `json:"name"`
}

// dispatchState tracks which issues have been dispatched to prevent double-dispatch.
type dispatchState struct {
	Dispatched map[string]dispatchEntry `json:"dispatched"`
}

// dispatchEntry records a single dispatch event.
type dispatchEntry struct {
	Agent        string    `json:"agent"`
	DispatchedAt time.Time `json:"dispatched_at"`
	IssueURL     string    `json:"issue_url"`
}

func runDispatch(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	// Load configs
	dispatchCfg, err := config.LoadDispatchConfig(root)
	if err != nil {
		return fmt.Errorf("loading dispatch config: %w", err)
	}
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return fmt.Errorf("loading agent config: %w", err)
	}

	// Cross-validate: all mapping agents must exist in agents.json
	for _, m := range dispatchCfg.Mappings {
		if _, ok := agentsCfg.Agents[m.Agent]; !ok {
			return fmt.Errorf("dispatch mapping references unknown agent %q", m.Agent)
		}
	}
	if _, ok := agentsCfg.Agents[dispatchCfg.NotifyOnComplete]; !ok {
		return fmt.Errorf("notify_on_complete agent %q not found in agents.json", dispatchCfg.NotifyOnComplete)
	}

	// Check gh auth
	if err := checkGHAuth(); err != nil {
		return fmt.Errorf("GitHub CLI not authenticated: %w", err)
	}

	// Load dispatch state
	state := loadDispatchState(root)

	// For each repo, query open issues with trigger label
	t := tmux.NewTmux()
	for _, repo := range dispatchCfg.Repos {
		issues, err := queryGitHubIssues(repo, dispatchCfg.TriggerLabel)
		if err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to query %s: %v\n", repo, err)
			continue
		}

		for _, issue := range issues {
			agent := matchIssueToAgent(issue, dispatchCfg.Mappings)
			if agent == "" {
				continue
			}
			issueKey := fmt.Sprintf("%s#%d", repo, issue.Number)

			// Check if agent is running
			sessionID := session.SessionName(agent)
			agentRunning, _ := t.HasSession(sessionID)

			// Skip if already dispatched (unless agent is idle and retry window elapsed)
			if entry, ok := state.Dispatched[issueKey]; ok {
				if agentRunning {
					fmt.Fprintf(cmd.OutOrStdout(), "skip %s: agent %s is busy\n", issueKey, agent)
					continue
				}
				retryAfter := time.Duration(dispatchCfg.RetryAfterSecs) * time.Second
				if time.Since(entry.DispatchedAt) < retryAfter {
					continue
				}
				// Agent idle + retry window elapsed: remove stale entry, allow re-dispatch
				delete(state.Dispatched, issueKey)
				fmt.Fprintf(cmd.OutOrStdout(), "retry %s: agent %s idle, previous dispatch expired\n", issueKey, agent)
			}

			if agentRunning {
				fmt.Fprintf(cmd.OutOrStdout(), "skip %s: agent %s is busy\n", issueKey, agent)
				continue
			}

			if dispatchDryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "would dispatch %s to %s\n", issueKey, agent)
				continue
			}

			// Dispatch via af sling
			if err := dispatchIssue(root, agent, issue.URL, dispatchCfg.NotifyOnComplete); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "dispatch %s failed: %v\n", issueKey, err)
				continue
			}

			// Record in state
			state.Dispatched[issueKey] = dispatchEntry{
				Agent:        agent,
				DispatchedAt: time.Now().UTC(),
				IssueURL:     issue.URL,
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dispatched %s to %s\n", issueKey, agent)
		}
	}

	// Prune stale entries and save state
	pruneDispatchState(&state)
	if err := saveDispatchState(root, &state); err != nil {
		return fmt.Errorf("saving dispatch state: %w", err)
	}
	return nil
}

// checkGHAuth verifies the GitHub CLI is authenticated.
func checkGHAuth() error {
	return exec.Command("gh", "auth", "status").Run()
}

// queryGitHubIssues queries GitHub for open issues with the given label.
func queryGitHubIssues(repo, triggerLabel string) ([]ghIssue, error) {
	out, err := exec.Command("gh", "issue", "list",
		"--repo", repo,
		"--label", triggerLabel,
		"--state", "open",
		"--json", "number,title,url,labels",
		"--limit", "50",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh issue list: %w", err)
	}
	var issues []ghIssue
	if err := json.Unmarshal(out, &issues); err != nil {
		return nil, fmt.Errorf("parsing gh output: %w", err)
	}
	return issues, nil
}

// matchIssueToAgent returns the agent name for the first matching label,
// or empty string if no mapping matches.
func matchIssueToAgent(issue ghIssue, mappings []config.DispatchMapping) string {
	issueLabels := make(map[string]bool, len(issue.Labels))
	for _, l := range issue.Labels {
		issueLabels[l.Name] = true
	}
	for _, m := range mappings {
		if issueLabels[m.Label] {
			return m.Agent
		}
	}
	return ""
}

// dispatchIssue invokes af sling --agent <name> --reset [--caller <caller>] <issueURL>.
func dispatchIssue(root, agent, issueURL, caller string) error {
	afBin, err := os.Executable()
	if err != nil {
		afBin = "af"
	}
	args := []string{"sling", "--agent", agent, "--reset"}
	if caller != "" {
		args = append(args, "--caller", caller)
	}
	args = append(args, issueURL)
	c := exec.Command(afBin, args...)
	c.Dir = root
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

// loadDispatchState reads .runtime/dispatch-state.json.
// Returns an empty state with initialized map if the file doesn't exist.
func loadDispatchState(root string) dispatchState {
	path := filepath.Join(root, ".runtime", "dispatch-state.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return dispatchState{Dispatched: make(map[string]dispatchEntry)}
	}
	var state dispatchState
	if err := json.Unmarshal(data, &state); err != nil {
		return dispatchState{Dispatched: make(map[string]dispatchEntry)}
	}
	if state.Dispatched == nil {
		state.Dispatched = make(map[string]dispatchEntry)
	}
	return state
}

// saveDispatchState writes .runtime/dispatch-state.json atomically via temp file + rename.
func saveDispatchState(root string, state *dispatchState) error {
	runtimeDir := filepath.Join(root, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		return fmt.Errorf("creating .runtime directory: %w", err)
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling dispatch state: %w", err)
	}
	data = append(data, '\n')
	tmp := filepath.Join(runtimeDir, ".dispatch-state.json.tmp")
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("writing temp dispatch state: %w", err)
	}
	return os.Rename(tmp, filepath.Join(runtimeDir, "dispatch-state.json"))
}

// pruneDispatchState removes entries older than 24 hours.
func pruneDispatchState(state *dispatchState) {
	cutoff := time.Now().Add(-24 * time.Hour)
	for key, entry := range state.Dispatched {
		if entry.DispatchedAt.Before(cutoff) {
			delete(state.Dispatched, key)
		}
	}
}

const dispatchSessionName = "af-dispatch"

func runDispatchStart(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	t := tmux.NewTmux()
	if running, _ := t.HasSession(dispatchSessionName); running {
		return fmt.Errorf("dispatcher is already running (session: %s)", dispatchSessionName)
	}

	dispatchCfg, err := config.LoadDispatchConfig(root)
	if err != nil {
		return fmt.Errorf("loading dispatch config: %w", err)
	}

	interval := resolveDispatchInterval(dispatchStartInterval, dispatchCfg.IntervalSecs)

	// Ensure .runtime/ exists so tee -a .runtime/dispatch.log works on first run
	if err := os.MkdirAll(filepath.Join(root, ".runtime"), 0o755); err != nil {
		return fmt.Errorf("creating .runtime directory: %w", err)
	}

	afBin, err := os.Executable()
	if err != nil {
		afBin = "af"
	}

	loopCmd := buildDispatchLoopCmd(afBin, interval)

	if err := t.NewSession(dispatchSessionName, root); err != nil {
		return fmt.Errorf("creating tmux session: %w", err)
	}

	if err := t.SendKeys(dispatchSessionName, loopCmd); err != nil {
		return fmt.Errorf("sending loop command: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Dispatcher started (session: %s, interval: %ds)\n", dispatchSessionName, interval)
	return nil
}

func runDispatchStop(cmd *cobra.Command, args []string) error {
	t := tmux.NewTmux()
	if running, _ := t.HasSession(dispatchSessionName); !running {
		return fmt.Errorf("dispatcher is not running")
	}

	if err := t.KillSession(dispatchSessionName); err != nil {
		return fmt.Errorf("killing tmux session: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Dispatcher stopped\n")
	return nil
}

func runDispatchStatus(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	t := tmux.NewTmux()
	running, _ := t.HasSession(dispatchSessionName)

	state := loadDispatchState(root)

	// Build agent session state map
	agentState := make(map[string]bool)
	for _, entry := range state.Dispatched {
		if _, checked := agentState[entry.Agent]; !checked {
			agentRunning, _ := t.HasSession(session.SessionName(entry.Agent))
			agentState[entry.Agent] = agentRunning
		}
	}

	out := formatDispatchStatus(running, state.Dispatched, agentState)
	fmt.Fprint(cmd.OutOrStdout(), out)
	return nil
}

// buildDispatchLoopCmd constructs the shell loop command for the dispatcher tmux session.
func buildDispatchLoopCmd(afBin string, interval int) string {
	return fmt.Sprintf("while true; do %s dispatch 2>&1 | tee -a .runtime/dispatch.log; sleep %d; done", afBin, interval)
}

// resolveDispatchInterval returns the flag value if non-zero, otherwise the config value.
func resolveDispatchInterval(flagValue, configValue int) int {
	if flagValue > 0 {
		return flagValue
	}
	return configValue
}

// formatDispatchStatus formats the dispatcher status and dispatch history as a string.
func formatDispatchStatus(running bool, entries map[string]dispatchEntry, agentState map[string]bool) string {
	var buf bytes.Buffer

	if running {
		fmt.Fprintln(&buf, "Dispatcher: RUNNING")
	} else {
		fmt.Fprintln(&buf, "Dispatcher: STOPPED")
	}

	if len(entries) == 0 {
		fmt.Fprintln(&buf, "No dispatched issues.")
		return buf.String()
	}

	// Sort keys for stable output
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	fmt.Fprintln(&buf)
	w := tabwriter.NewWriter(&buf, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "ISSUE\tAGENT\tSTATUS\tDISPATCHED")
	for _, key := range keys {
		entry := entries[key]
		status := "completed"
		if agentState[entry.Agent] {
			status = "running"
		}
		age := time.Since(entry.DispatchedAt).Round(time.Minute)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s ago\n", key, entry.Agent, status, age)
	}
	w.Flush()

	return buf.String()
}
