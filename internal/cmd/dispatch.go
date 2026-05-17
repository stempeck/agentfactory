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
	"github.com/stempeck/agentfactory/internal/lock"
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

// ghItem represents a GitHub issue or PR from gh CLI JSON output.
type ghItem struct {
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
	ItemURL      string    `json:"item_url"`
	Source       string    `json:"source"`
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

	lk := lock.NewWithPath(filepath.Join(root, ".runtime", "dispatch-cycle.lock"))
	if err := lk.Acquire(fmt.Sprintf("pid-%d", os.Getpid())); err != nil {
		return fmt.Errorf("acquiring dispatch lock: %w", err)
	}
	defer lk.Release()

	// Load dispatch state
	state := loadDispatchState(root)

	issueMappings, prMappings := groupMappingsBySource(dispatchCfg.Mappings)

	t := tmux.NewTmux()
	for _, repo := range dispatchCfg.Repos {
		var items []ghItem
		var itemMappings [][]config.DispatchMapping
		var itemSources []string

		if len(issueMappings) > 0 {
			issues, err := queryGitHubIssues(repo, dispatchCfg.TriggerLabel)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to query issues for %s: %v\n", repo, err)
			} else {
				if len(issues) == 50 {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: query returned 50 results (limit reached) for %s — some items may be missed\n", repo)
				}
				for range issues {
					itemMappings = append(itemMappings, issueMappings)
					itemSources = append(itemSources, "issue")
				}
				items = append(items, issues...)
			}
		}

		if len(prMappings) > 0 {
			prs, err := queryGitHubPRs(repo, dispatchCfg.TriggerLabel)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to query PRs for %s: %v\n", repo, err)
			} else {
				if len(prs) == 50 {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: query returned 50 results (limit reached) for %s — some items may be missed\n", repo)
				}
				for range prs {
					itemMappings = append(itemMappings, prMappings)
					itemSources = append(itemSources, "pr")
				}
				items = append(items, prs...)
			}
		}

		for i, item := range items {
			agent := matchItemToAgent(item, itemMappings[i])
			if agent == "" {
				continue
			}
			itemKey := fmt.Sprintf("%s#%d", repo, item.Number)

			sessionID := session.SessionName(agent)
			agentRunning, _ := t.HasSession(sessionID)

			if entry, ok := state.Dispatched[itemKey]; ok {
				if agentRunning {
					fmt.Fprintf(cmd.OutOrStdout(), "skip %s: agent %s is busy\n", itemKey, agent)
					continue
				}
				retryAfter := time.Duration(dispatchCfg.RetryAfterSecs) * time.Second
				if time.Since(entry.DispatchedAt) < retryAfter {
					continue
				}
				delete(state.Dispatched, itemKey)
				fmt.Fprintf(cmd.OutOrStdout(), "retry %s: agent %s idle, previous dispatch expired\n", itemKey, agent)
			}

			if agentRunning {
				fmt.Fprintf(cmd.OutOrStdout(), "skip %s: agent %s is busy\n", itemKey, agent)
				continue
			}

			if dispatchDryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "would dispatch %s to %s\n", itemKey, agent)
				continue
			}

			if err := dispatchItem(root, agent, item.URL, dispatchCfg.NotifyOnComplete); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "dispatch %s failed: %v\n", itemKey, err)
				continue
			}

			state.Dispatched[itemKey] = dispatchEntry{
				Agent:        agent,
				DispatchedAt: time.Now().UTC(),
				ItemURL:      item.URL,
				Source:       itemSources[i],
			}
			if dispatchCfg.RemoveTriggerAfterDispatch {
				if err := removeTriggerLabel(repo, item.Number, dispatchCfg.TriggerLabel, itemSources[i]); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to remove trigger label from %s: %v\n", itemKey, err)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "dispatched %s to %s\n", itemKey, agent)
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
func queryGitHubIssues(repo, triggerLabel string) ([]ghItem, error) {
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
	var items []ghItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parsing gh output: %w", err)
	}
	return items, nil
}

// queryGitHubPRs queries GitHub for open PRs with the given label.
func queryGitHubPRs(repo, triggerLabel string) ([]ghItem, error) {
	out, err := exec.Command("gh", "pr", "list",
		"--repo", repo,
		"--label", triggerLabel,
		"--state", "open",
		"--json", "number,title,url,labels",
		"--limit", "50",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh pr list: %w", err)
	}
	var items []ghItem
	if err := json.Unmarshal(out, &items); err != nil {
		return nil, fmt.Errorf("parsing gh output: %w", err)
	}
	return items, nil
}

// matchItemToAgent returns the agent name for the first mapping whose labels
// are ALL present on the item (AND semantics), or empty string if no mapping matches.
func matchItemToAgent(item ghItem, mappings []config.DispatchMapping) string {
	itemLabels := make(map[string]bool, len(item.Labels))
	for _, l := range item.Labels {
		itemLabels[l.Name] = true
	}
	for _, m := range mappings {
		allMatch := true
		for _, label := range m.Labels {
			if !itemLabels[label] {
				allMatch = false
				break
			}
		}
		if allMatch && len(m.Labels) > 0 {
			return m.Agent
		}
	}
	return ""
}

// groupMappingsBySource splits mappings into issue and PR groups based on Source field.
func groupMappingsBySource(mappings []config.DispatchMapping) (issues, prs []config.DispatchMapping) {
	for _, m := range mappings {
		if m.Source == "pr" {
			prs = append(prs, m)
		} else {
			issues = append(issues, m)
		}
	}
	return issues, prs
}

// removeTriggerLabel removes the trigger label from a GitHub issue or PR.
func removeTriggerLabel(repo string, number int, label string, source string) error {
	subcmd := "issue"
	if source == "pr" {
		subcmd = "pr"
	}
	return exec.Command("gh", subcmd, "edit",
		"--repo", repo,
		fmt.Sprintf("%d", number),
		"--remove-label", label,
	).Run()
}

// dispatchItem invokes af sling --agent <name> --reset [--caller <caller>] <itemURL>.
func dispatchItem(root, agent, itemURL, caller string) error {
	afBin, err := os.Executable()
	if err != nil {
		afBin = "af"
	}
	args := []string{"sling", "--agent", agent, "--reset"}
	if caller != "" {
		args = append(args, "--caller", caller)
	}
	args = append(args, itemURL)
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
	fmt.Fprintln(w, "ISSUE\tSOURCE\tAGENT\tSTATUS\tDISPATCHED")
	for _, key := range keys {
		entry := entries[key]
		status := "completed"
		if agentState[entry.Agent] {
			status = "running"
		}
		age := time.Since(entry.DispatchedAt).Round(time.Minute)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s ago\n", key, entry.Source, entry.Agent, status, age)
	}
	w.Flush()

	return buf.String()
}
