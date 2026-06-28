package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/session"
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
	statusCmd.Flags().Bool("json", false, "Emit dispatch status as JSON instead of human-readable text")
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
//
// The first four fields are the original, non-workflow correlation record. The
// trailing five (issue #378 K3) are additive, omitempty workflow-correlation
// fields that let the autonomous multi-phase layer (Phase 3) know WHICH formula
// instance a phase ran as and WHEN it was dispatched. Old 4-field state files
// unmarshal these as zero values — no migration — and an empty PhaseInstanceID
// means "not captured / safe to re-sling." Phase 3 writes and branches on these:
// runDispatch routes workflow-labeled items to handleWorkflowItem, slingPhase writes
// all five fields, and evaluatePhase branches on Phase + PhaseInstanceID.
type dispatchEntry struct {
	Agent        string    `json:"agent"`
	DispatchedAt time.Time `json:"dispatched_at"`
	ItemURL      string    `json:"item_url"`
	Source       string    `json:"source"`

	// Workflow correlation (issue #378, additive).
	Workflow          string    `json:"workflow,omitempty"`
	Phase             string    `json:"phase,omitempty"`
	PhaseInstanceID   string    `json:"phase_instance_id,omitempty"`
	PhaseDispatchedAt time.Time `json:"phase_dispatched_at,omitempty"`
	Attempts          int       `json:"attempts,omitempty"`
}

// partitionDispatchMappings splits a dispatch config's mappings into those whose agent
// is provisioned in agents.json (known, kept in order) and the distinct agent names
// referenced by mappings that are NOT (unknownAgents, in first-seen order). It backs the
// K6 dispatch-loop tolerance (skip-and-warn) WITHOUT relaxing the shared write-path
// validator (config.ValidateDispatchConfig), which the CLI/web config-set paths still use.
func partitionDispatchMappings(disp *config.DispatchConfig, agents *config.AgentConfig) (known []config.DispatchMapping, unknownAgents []string) {
	seen := make(map[string]bool)
	for _, m := range disp.Mappings {
		if _, ok := agents.Agents[m.Agent]; ok {
			known = append(known, m)
			continue
		}
		if !seen[m.Agent] {
			seen[m.Agent] = true
			unknownAgents = append(unknownAgents, m.Agent)
		}
	}
	return known, unknownAgents
}

// Dispatch config-validity states surfaced by `af dispatch status --json` and the `af up`
// pre-flight (issue #73 K8). They make a fresh/partial factory's dispatch readiness
// observable so a degraded loop (K6) is never silent (cross-review H2).
const (
	dispatchStateOK            = "ok"
	dispatchStateNotConfigured = "not_configured"
	dispatchStateUnprovisioned = "references_unprovisioned_agents"
	dispatchStateInvalid       = "invalid"
)

// dispatchConfigState classifies a dispatch config's readiness from its load result and a
// cross-file check against agents.json. "not_configured" covers the empty install default
// and an absent file (the dispatcher friendly-skips both); "references_unprovisioned_agents"
// is the K6 degraded path the loop tolerates; "invalid" is any other load/validation error.
func dispatchConfigState(disp *config.DispatchConfig, agents *config.AgentConfig, loadErr error) string {
	if loadErr != nil {
		if errors.Is(loadErr, config.ErrNotFound) || errors.Is(loadErr, config.ErrMissingField) {
			return dispatchStateNotConfigured
		}
		return dispatchStateInvalid
	}
	if _, unknown := partitionDispatchMappings(disp, agents); len(unknown) > 0 {
		return dispatchStateUnprovisioned
	}
	if err := config.ValidateDispatchConfig(disp, agents); err != nil {
		return dispatchStateInvalid
	}
	return dispatchStateOK
}

// loadDispatchConfigState loads the dispatch + agents configs at root and classifies the
// dispatch config-validity state for observability (K8). A missing/invalid agents.json is
// "invalid" — the dispatcher cannot cross-validate without it. Read-only and advisory; it
// never mutates state and never aborts.
func loadDispatchConfigState(root string) string {
	disp, loadErr := config.LoadDispatchConfig(root)
	if loadErr != nil {
		return dispatchConfigState(nil, nil, loadErr)
	}
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return dispatchStateInvalid
	}
	return dispatchConfigState(disp, agents, nil)
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

	// K6 (issue #73): the dispatch LOOP tolerates a partial/edited factory — drop
	// mappings whose agent is not provisioned (skip-and-warn) and dispatch the rest,
	// instead of hard-failing the whole cycle. The write path (af config dispatch set)
	// stays strict. K8 surfaces the degraded state at `af up` / `af dispatch status` so a
	// "running-but-dispatching-nothing" loop is never silent (cross-review H2).
	knownMappings, unknownAgents := partitionDispatchMappings(dispatchCfg, agentsCfg)
	for _, agent := range unknownAgents {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: skipping dispatch mappings for unprovisioned agent %q (not in agents.json)\n", agent)
	}
	dispatchCfg.Mappings = knownMappings
	if len(dispatchCfg.Mappings) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "skipping dispatch: no mapping references a provisioned agent")
		return nil
	}
	// The remaining cross-file rules (an explicitly set notify_on_complete, workflow phase
	// formulas) stay authoritative; config.ValidateDispatchConfig is still the shared single
	// source of truth. Only this loop caller relaxes hard-fail to skip-and-warn, so a
	// residual error degrades the cycle rather than aborting the polling loop.
	if err := config.ValidateDispatchConfig(dispatchCfg, agentsCfg); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: skipping dispatch: %v\n", err)
		return nil
	}

	// Check gh auth
	if err := checkGHAuth(); err != nil {
		return fmt.Errorf("GitHub CLI not authenticated: %w", err)
	}

	lk := lock.NewWithPath(filepath.Join(root, ".runtime", "dispatch-cycle.lock"))
	if err := lk.Acquire(fmt.Sprintf("pid-%d", os.Getpid())); err != nil {
		return fmt.Errorf("[%s] acquiring dispatch lock: %w", time.Now().UTC().Format("2006-01-02 15:04:05"), err)
	}
	defer lk.Release()

	stats := &dispatchCycleStats{start: time.Now().UTC()}
	defer func() {
		fmt.Fprintln(cmd.OutOrStdout(), stats.String())
	}()

	// Load dispatch state
	state := loadDispatchState(root)

	issueMappings, prMappings := groupMappingsBySource(dispatchCfg.Mappings)

	t := newCmdTmux()
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

		stats.queried += len(items)

		for i, item := range items {
			// Workflow pre-branch (issue #378 K8): a workflow-labeled item is fully
			// owned by handleWorkflowItem (its own skip/retry/record/label logic) and
			// MUST `continue` without ever touching the non-workflow retry-window or
			// record write below (HIGH-3). A non-workflow item (nil) falls through to
			// today's exact code, byte-for-byte unchanged (C-10).
			if wf := matchWorkflow(item, dispatchCfg.Workflows); wf != nil {
				handleWorkflowItem(cmd, root, t, &state, stats, dispatchCfg, repo, item, itemSources[i], wf)
				continue
			}

			agent := matchItemToAgent(item, itemMappings[i])
			if agent == "" {
				stats.skipped++
				continue
			}
			itemKey := fmt.Sprintf("%s#%d", repo, item.Number)

			sessionID := session.SessionName(agent)
			agentRunning, _ := t.HasSession(sessionID)

			if entry, ok := state.Dispatched[itemKey]; ok {
				if agentRunning {
					fmt.Fprintf(cmd.OutOrStdout(), "skip %s: agent %s is busy\n", itemKey, agent)
					stats.skipped++
					continue
				}
				retryAfter := time.Duration(dispatchCfg.RetryAfterSecs) * time.Second
				if time.Since(entry.DispatchedAt) < retryAfter {
					stats.skipped++
					continue
				}
				delete(state.Dispatched, itemKey)
				fmt.Fprintf(cmd.OutOrStdout(), "retry %s: agent %s idle, previous dispatch expired\n", itemKey, agent)
			}

			if agentRunning {
				fmt.Fprintf(cmd.OutOrStdout(), "skip %s: agent %s is busy\n", itemKey, agent)
				stats.skipped++
				continue
			}

			if dispatchDryRun {
				fmt.Fprintf(cmd.OutOrStdout(), "would dispatch %s to %s\n", itemKey, agent)
				stats.dispatched++
				continue
			}

			// Phase 2 adds no workflow branch here; the captured sling stdout is
			// the Phase-3 fallback source for instance-ID capture and is discarded
			// on the non-workflow path (C-10: observable behavior unchanged).
			if _, err := dispatchItem(root, agent, item.URL, dispatchCfg.NotifyOnComplete); err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "dispatch %s failed: %v\n", itemKey, err)
				stats.errors++
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
			stats.dispatched++
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

// editItemLabels batches add + remove label mutations into ONE atomic `gh edit`
// call (decision W-8), mirroring removeTriggerLabel's issue/pr subcommand
// selection. Package-var seam (ADR-009) so the workflow tests swap it to a
// recorder; without the seam the whole workflow label state machine is untestable
// without a live gh. Label mutation stays config-bounded (D5-B1, C-7, ADR-017):
// callers pass ONLY the configured trigger_label and workflows[].phases labels,
// never a label sourced from agent input.
var editItemLabels = func(repo string, number int, source string, add, remove []string) error {
	subcmd := "issue"
	if source == "pr" {
		subcmd = "pr"
	}
	args := []string{subcmd, "edit", "--repo", repo, fmt.Sprintf("%d", number)}
	for _, l := range add {
		args = append(args, "--add-label", l)
	}
	for _, l := range remove {
		args = append(args, "--remove-label", l)
	}
	return exec.Command("gh", args...).Run() // one atomic gh edit (W-8)
}

// dispatchItem invokes af sling --agent <name> --reset [--caller <caller>] <itemURL>.
//
// It returns sling's captured stdout alongside the exit error. The operator still
// sees that stdout live (it is tee'd through os.Stdout via io.MultiWriter), so the
// non-workflow dispatch path is byte-for-byte unchanged in observable behavior
// (C-10): same argv, same stderr, same exit-code semantics. The returned buffer
// is the dir-agnostic fallback source for captureInstanceID (issue #378 K4); the
// non-workflow caller discards it.
//
// Package-var seam (issue #378 Phase 3): the workflow engine slings phases through
// this var so tests swap it to a recorder without spawning a real `af sling`
// subprocess. Promotion is behavior-preserving for production (same argv, same
// stderr, same exit semantics) — C-10 observable behavior unchanged.
var dispatchItem = func(root, agent, itemURL, caller string) (string, error) {
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
	var buf bytes.Buffer
	c.Stdout = io.MultiWriter(os.Stdout, &buf)
	c.Stderr = os.Stderr
	err = c.Run()
	return buf.String(), err
}

// ============================================================================
// Issue #378 Phase 2 — completion foundation. These are PURE predicate/capture
// helpers (no loop branch yet); Phase 3 wires them into runDispatch. Each is
// hermetically testable via the newIssueStore memstore seam and the ghLinkedPRs/
// ghPRStatus package-var seams below.
// ============================================================================

// parseInstanceIDFromStdout extracts the formula-instance epic ID from sling's
// "Formula %q instantiated: <id> (<n> steps)" line (sling.go:543). This is the
// dir-agnostic K4 fallback used when the worktree-aware hooked_formula read
// misses (e.g. the dispatcher cannot recompute the worktree agent dir). Returns
// "" when the marker is absent.
func parseInstanceIDFromStdout(stdout string) string {
	const marker = "instantiated: "
	for _, line := range strings.Split(stdout, "\n") {
		idx := strings.Index(line, marker)
		if idx < 0 {
			continue
		}
		rest := line[idx+len(marker):]
		// rest is "<id> (<n> steps)"; the ID is the token before the first space.
		if sp := strings.IndexByte(rest, ' '); sp >= 0 {
			rest = rest[:sp]
		}
		if rest = strings.TrimSpace(rest); rest != "" {
			return rest
		}
	}
	return ""
}

// captureInstanceID resolves the formula-instance epic ID created by the sling
// that dispatchItem just ran (issue #378 K4). It prefers the agent's
// .runtime/hooked_formula (read from agentDir — which under worktrees is the
// worktree agent dir sling reassigned to, sling.go:188), and falls back to
// parsing slingStdout when that file is absent.
//
// The captured ID is returned ONLY when it is FRESH (HIGH-3): the epic's
// CreatedAt must be at or after phaseDispatchedAt, the timestamp stamped on the
// correlation record at dispatch. A stale hooked_formula left by a prior sling,
// a not-found bead, or no ID at all all collapse to "" — the safe no-capture
// path that leaves PhaseInstanceID empty so the next cycle re-slings rather than
// keying completion to a previous, already-terminal instance (the #413 CRIT-2
// class of false-advance).
func captureInstanceID(ctx context.Context, store issuestore.Store, agentDir, slingStdout string, phaseDispatchedAt time.Time) string {
	id := readHookedFormulaID(agentDir)
	if id == "" {
		id = parseInstanceIDFromStdout(slingStdout)
	}
	if id == "" {
		return ""
	}
	iss, err := store.Get(ctx, id)
	if err != nil {
		return ""
	}
	if iss.CreatedAt.Before(phaseDispatchedAt) {
		return "" // stale: this epic predates the dispatch — not the instance we just created
	}
	return id
}

// instanceComplete reports whether a recorded formula-instance epic has GENUINELY
// completed (issue #378 K6): it is in a terminal lifecycle state AND was closed
// with the typed completion reason. Gating on the reason — never a bare
// IsTerminal() — is what stops a --reset (which also closes the epic) from being
// misread as completion. The epic is fetched by exact ID via Store.Get, which is
// actor-independent, so no IncludeAllAgents overlay is needed.
func instanceComplete(iss issuestore.Issue) bool {
	return iss.Status.IsTerminal() && iss.CloseReason == config.CloseReasonFormulaComplete
}

// linkedPRsGraphQL is the W-6 issue→PR linkage query, pinned by the Phase-3
// empirical spike (todos/ultra-implement/W6_SPIKE.md, verified against real linked
// pairs #438→[439], #435→[436], #430→[431], #378→[]). `closingIssuesReferences`
// is a field ON the PR; the reverse edge `closedByPullRequestsReferences` on the
// ISSUE is the GitHub-native "which PRs close this issue". `includeClosedPrs:true`
// makes it robust across open/closed/merged PR states (the dispatcher's live case
// is an OPEN issue + OPEN linked PR pre-merge, per HIGH-A).
const linkedPRsGraphQL = `query($owner:String!,$repo:String!,$number:Int!){
  repository(owner:$owner,name:$repo){
    issue(number:$number){
      closedByPullRequestsReferences(first:50, includeClosedPrs:true){
        nodes { number }
      }
    }
  }
}`

// ghLinkedPRs returns the PR numbers linked to (i.e. that close) the given issue,
// via the pinned W-6 GraphQL query (linkedPRsGraphQL). Package-var seam (ADR-009,
// mirroring ghPRStatus) so tests inject linked-PR sets without a real gh.
//
// The linkage is AGENT-AUTHORED and NOT universally present: it is populated only
// when the producing PR body carries a GitHub closing keyword (`Resolves #N` /
// `Closes #N` / `Fixes #N`). factoryworker/tdd-implement/rapid-implement/
// ultra-implement emit one; rapid-soldesign-plan/soldesign-plan put `(#N)` only in
// the PR TITLE (a cross-reference, NOT a closing keyword) → resolve to 0 PRs. The
// caller treats 0 or >1 linked PRs as a documented, detectable stall, never an
// advance.
var ghLinkedPRs = func(repo string, issueNumber int) ([]int, error) {
	owner, name, ok := strings.Cut(repo, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("ghLinkedPRs: repo %q is not in owner/name form", repo)
	}
	out, err := exec.Command("gh", "api", "graphql",
		"-f", "query="+linkedPRsGraphQL,
		"-F", "owner="+owner,
		"-F", "repo="+name,
		"-F", fmt.Sprintf("number=%d", issueNumber),
	).Output()
	if err != nil {
		return nil, fmt.Errorf("gh api graphql (linked PRs for issue %d): %w", issueNumber, err)
	}
	prs, err := parseLinkedPRs(out)
	if err != nil {
		return nil, fmt.Errorf("parsing gh api graphql linked PRs for issue %d: %w", issueNumber, err)
	}
	return prs, nil
}

// parseLinkedPRs extracts the PR numbers from the W-6 GraphQL response (the
// .data.repository.issue.closedByPullRequestsReferences.nodes[].number path). Pure
// so a unit test pins the exact spike response shape without a live gh.
func parseLinkedPRs(out []byte) ([]int, error) {
	var resp struct {
		Data struct {
			Repository struct {
				Issue struct {
					ClosedByPullRequestsReferences struct {
						Nodes []struct {
							Number int `json:"number"`
						} `json:"nodes"`
					} `json:"closedByPullRequestsReferences"`
				} `json:"issue"`
			} `json:"repository"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return nil, err
	}
	nodes := resp.Data.Repository.Issue.ClosedByPullRequestsReferences.Nodes
	prs := make([]int, 0, len(nodes))
	for _, n := range nodes {
		prs = append(prs, n.Number)
	}
	return prs, nil
}

// hasLinkedPR is the K6b issue→pr handoff artifact predicate: a handoff is
// complete only when EXACTLY ONE PR is linked. Zero PRs means the downstream
// artifact has not been produced; more than one is ambiguous — both are
// detectable stalls (handled in Phase 3), never completion. A gh error is
// treated as "not complete."
func hasLinkedPR(repo string, issueNumber int) bool {
	prs, err := ghLinkedPRs(repo, issueNumber)
	if err != nil {
		return false
	}
	return len(prs) == 1
}

// prStatus is the agent-independent, landed-ready state of a PR, derived from the
// three gh fields the K6b source:pr terminal gate needs. It deliberately carries
// NO "merged" field: gating terminal completion on merge is circular — the PR's
// "Resolves #N" auto-closes the tracking issue on merge, dropping it out of the
// --state open query before the next cycle can observe completion (HIGH-A). The
// human merge happens AFTER the pipeline finishes.
type prStatus struct {
	Mergeable   bool // mergeStateStatus == CLEAN
	Approved    bool // reviewDecision == APPROVED
	ChecksGreen bool // every statusCheckRollup entry passing
}

// ghPRStatus reads a PR's mergeable/approved/checks-green state via one
// agent-independent gh call. Package-var seam so tests inject without a real gh.
var ghPRStatus = func(repo string, prNumber int) (prStatus, error) {
	out, err := exec.Command("gh", "pr", "view", fmt.Sprintf("%d", prNumber),
		"--repo", repo,
		"--json", "mergeStateStatus,reviewDecision,statusCheckRollup",
	).Output()
	if err != nil {
		return prStatus{}, fmt.Errorf("gh pr view %d: %w", prNumber, err)
	}
	var resp struct {
		MergeStateStatus  string `json:"mergeStateStatus"`
		ReviewDecision    string `json:"reviewDecision"`
		StatusCheckRollup []struct {
			State      string `json:"state"`
			Conclusion string `json:"conclusion"`
		} `json:"statusCheckRollup"`
	}
	if err := json.Unmarshal(out, &resp); err != nil {
		return prStatus{}, fmt.Errorf("parsing gh pr view %d: %w", prNumber, err)
	}
	green := true
	for _, c := range resp.StatusCheckRollup {
		if !checkRollupGreen(c.State, c.Conclusion) {
			green = false
			break
		}
	}
	return prStatus{
		Mergeable:   resp.MergeStateStatus == "CLEAN",
		Approved:    resp.ReviewDecision == "APPROVED",
		ChecksGreen: green,
	}, nil
}

// checkRollupGreen reports whether one statusCheckRollup entry is passing.
// GitHub returns two node shapes: CheckRun carries a conclusion
// (SUCCESS/NEUTRAL/SKIPPED are passing terminal values; anything else, including
// an empty conclusion on an in-progress run, is not green); StatusContext carries
// a state, where only SUCCESS is green.
func checkRollupGreen(state, conclusion string) bool {
	if conclusion != "" {
		switch conclusion {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			return true
		default:
			return false
		}
	}
	return state == "SUCCESS"
}

// prArtifactComplete is the K6b source:pr terminal-phase artifact predicate: the
// PR must be mergeable AND approved AND checks-green (HIGH-A) — NOT merged. A gh
// error is treated as "not complete."
func prArtifactComplete(repo string, prNumber int) bool {
	st, err := ghPRStatus(repo, prNumber)
	if err != nil {
		return false
	}
	return st.Mergeable && st.Approved && st.ChecksGreen
}

// ============================================================================
// Issue #378 Phase 3 — the running workflow engine. A pre-branch in runDispatch's
// per-item loop hands every workflow-labeled item to handleWorkflowItem, which
// drives a small state machine over the item's GitHub labels (bootstrap → advance
// → terminal) gating each advance on the Phase-2 completion predicates. The
// non-workflow path falls through unchanged (C-10). Every side effect routes
// through a package-var seam (editItemLabels, dispatchItem, ghLinkedPRs,
// ghPRStatus, sendWorkflowCompleteMail, newIssueStore, the injected cmdTmux) so the
// branch is hermetically unit-testable.
// ============================================================================

// dispatchStoreActor is the actor the dispatcher opens the issue store as. The
// completion read is Store.Get by exact recorded ID, which is actor-independent
// (no IncludeAllAgents overlay needed — see instanceComplete), so the value only
// names the reader.
const dispatchStoreActor = "dispatch"

// maxWorkflowAttempts bounds re-slinging of a single phase (W-7). On exceed the
// engine stops re-slinging and surfaces a distinctly-named detectable stall rather
// than looping forever.
const maxWorkflowAttempts = 5

// matchWorkflow returns the workflow whose label is present on the item (exact
// match, C-9), or nil. A nil result means the item is NOT a workflow item and
// falls through to today's unchanged non-workflow dispatch path (C-10).
func matchWorkflow(item ghItem, workflows []config.Workflow) *config.Workflow {
	if len(workflows) == 0 {
		return nil
	}
	present := make(map[string]bool, len(item.Labels))
	for _, l := range item.Labels {
		present[l.Name] = true
	}
	for i := range workflows {
		if present[workflows[i].Label] {
			return &workflows[i]
		}
	}
	return nil
}

// workflowCursor intersects the item's label names with the workflow's ordered
// phases and returns the current cursor (K7): the single in-flight phase label;
// "" when no phase label is present yet (bootstrap); and ambiguous=true when MORE
// THAN ONE phase label is present (LOW-1) — the caller emits a detectable stall,
// never "pick the latest". The bootstrap "" and the ambiguous signal are returned
// distinctly so the caller can tell "no phase yet" from "two phases at once".
func workflowCursor(item ghItem, phases []string) (phase string, ambiguous bool) {
	present := make(map[string]bool, len(item.Labels))
	for _, l := range item.Labels {
		present[l.Name] = true
	}
	found := ""
	count := 0
	for _, p := range phases {
		if present[p] {
			count++
			found = p
		}
	}
	switch count {
	case 0:
		return "", false
	case 1:
		return found, false
	default:
		return "", true
	}
}

// phaseMapping resolves a phase's backing mapping by DIRECT single-label lookup
// (HIGH-B): the mapping whose label set is exactly {phaseLabel}. This makes
// first-match-wins shadowing impossible by construction — the workflow engine must
// NEVER route the item's full live label set through matchItemToAgent, where a
// mapping keyed on the bare workflow/trigger label could shadow the phase mapping.
// Mirrors the unexported config.phaseResolvesAlone, replicated here so Phase 3
// touches only this file (the Phase-1 CRITICAL-2 validator guarantees the mapping
// exists for a loaded config; the cmd-layer lookup is the belt-and-suspenders
// runtime guard).
func phaseMapping(mappings []config.DispatchMapping, phaseLabel string) (config.DispatchMapping, bool) {
	for _, m := range mappings {
		if len(m.Labels) == 1 && m.Labels[0] == phaseLabel {
			return m, true
		}
	}
	return config.DispatchMapping{}, false
}

// phaseIndex returns phase's position in phases, or -1.
func phaseIndex(phases []string, phase string) int {
	for i, p := range phases {
		if p == phase {
			return i
		}
	}
	return -1
}

// nextPhaseLabel returns the phase after phase, or "" when phase is the last (or
// absent) — "" signals the terminal branch.
func nextPhaseLabel(phases []string, phase string) string {
	idx := phaseIndex(phases, phase)
	if idx < 0 || idx+1 >= len(phases) {
		return ""
	}
	return phases[idx+1]
}

// resolveLinkedPR returns the PR a pr-source phase operates on, the number of
// candidate PRs matched (so the caller distinguishes 0/1/>1), and any gh error. If
// the item is itself a PR it is the PR; if it is an issue (cross-source) the
// GitHub-native linked PR set is resolved via W-6 (ghLinkedPRs).
func resolveLinkedPR(repo string, item ghItem, itemSource string) (pr int, count int, err error) {
	if itemSource == "pr" {
		return item.Number, 1, nil
	}
	prs, err := ghLinkedPRs(repo, item.Number)
	if err != nil {
		return 0, 0, err
	}
	if len(prs) != 1 {
		return 0, len(prs), nil
	}
	return prs[0], 1, nil
}

// phaseOutcome is what the current in-flight phase's state implies (K6 + K6b).
type phaseOutcome int

const (
	phaseIncomplete phaseOutcome = iota // formula not genuinely done ⇒ re-sling / skip
	phaseWait                           // formula done, artifact pending (terminal PR not green, or transient gh) ⇒ defer, no re-sling
	phaseStall                          // formula done, artifact definitively wrong (0/>1 linked PR) ⇒ detectable stall
	phaseAdvance                        // fully complete ⇒ advance or terminal
)

// evaluatePhase gates an advance on the Phase-2 predicates (K6: IsTerminal +
// CloseReasonFormulaComplete on the SPECIFIC recorded instance; K6b: the
// agent-independent artifact predicate where one is definable). An empty/missing
// recorded instance ⇒ phaseIncomplete (re-sling the current phase — the
// lost-record self-heal — never advance).
func evaluatePhase(ctx context.Context, store issuestore.Store, repo string, item ghItem, itemSource string, entry dispatchEntry, mappings []config.DispatchMapping, wf *config.Workflow, phase string, m config.DispatchMapping) (phaseOutcome, string) {
	if entry.Phase != phase {
		// Staleness guard (#413 CRIT-1): the recorded entry must belong to the phase the
		// live GitHub cursor names. After a crash between advance()'s label swap and the
		// end-of-cycle save, the cursor reads the NEXT phase while the record still holds
		// the PREVIOUS, genuinely-complete instance — trusting PhaseInstanceID here would
		// advance the freshly-labeled phase un-run (a silent skip). A zero/lost entry
		// (Phase == "") also lands here ⇒ re-sling the current phase (the lost-record
		// self-heal), identical to the empty-PhaseInstanceID case below.
		return phaseIncomplete, ""
	}
	if entry.PhaseInstanceID == "" {
		return phaseIncomplete, "" // no recorded instance ⇒ re-sling, never advance
	}
	iss, err := store.Get(ctx, entry.PhaseInstanceID)
	if err != nil {
		return phaseIncomplete, "" // instance unresolvable ⇒ re-sling
	}
	if !instanceComplete(iss) {
		return phaseIncomplete, "" // formula not at its final af done with the completion reason
	}
	// K6 satisfied. Now the K6b agent-independent artifact gate.
	idx := phaseIndex(wf.Phases, phase)
	isLast := idx == len(wf.Phases)-1
	if isLast {
		// Terminal source:pr gates on mergeable+approved+green (HIGH-A), NOT merged.
		// A terminal issue-source phase has no downstream artifact: IsTerminal +
		// provenance IS the definition of completion for it (named residual, W-9).
		if m.Source == "pr" {
			prNum, count, err := resolveLinkedPR(repo, item, itemSource)
			if err != nil {
				return phaseWait, "" // transient gh error ⇒ retry next cycle
			}
			if count != 1 {
				return phaseStall, fmt.Sprintf("terminal pr phase %q resolved %d linked PRs (want exactly 1)", phase, count)
			}
			if !prArtifactComplete(repo, prNum) {
				return phaseWait, "" // PR not yet mergeable+approved+green ⇒ wait, do not re-sling a done formula
			}
		}
		return phaseAdvance, ""
	}
	// Non-terminal: an issue→pr handoff requires EXACTLY ONE linked PR to exist
	// (W-6 / K6b). A completed issue-source formula that produced 0 or >1 linked
	// PRs is a documented detectable stall (e.g. a producer that emits only a
	// title cross-reference, not a closing keyword), never a silent advance.
	if next, ok := phaseMapping(mappings, wf.Phases[idx+1]); ok && m.Source == "issue" && next.Source == "pr" {
		prs, err := ghLinkedPRs(repo, item.Number)
		if err != nil {
			return phaseWait, "" // transient gh error ⇒ retry next cycle
		}
		if len(prs) != 1 {
			return phaseStall, fmt.Sprintf("issue→pr handoff at phase %q resolved %d linked PRs (want exactly 1)", phase, len(prs))
		}
	}
	return phaseAdvance, ""
}

// sendWorkflowCompleteMail fires the single workflow-complete notification on
// terminal advance (LOW-3), DISTINCT from each phase's per-instance WORK_DONE
// (done.go sendWorkDoneMail). Package-var seam mirroring sendWorkDoneMail:
// isTestBinary() short-circuits under `go test`, os.Executable() with an `af` PATH
// fallback, one `af mail send <recipient> -s <subject> -m <body>` subprocess.
var sendWorkflowCompleteMail = func(recipient, workflow, itemKey string) error {
	if isTestBinary() {
		return nil
	}
	afPath, err := os.Executable()
	if err != nil {
		afPath, _ = exec.LookPath("af")
	}
	if afPath == "" {
		return fmt.Errorf("cannot find af binary")
	}
	subject := fmt.Sprintf("WORKFLOW_COMPLETE: %s", workflow)
	body := fmt.Sprintf("Workflow %q completed all phases for %s.", workflow, itemKey)
	c := exec.Command(afPath, "mail", "send", recipient, "-s", subject, "-m", body)
	c.Env = os.Environ()
	var stderr bytes.Buffer
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if stderr.Len() > 0 {
			return fmt.Errorf("mail send to %s failed: %w\nsubprocess stderr: %s", recipient, err, strings.TrimSpace(stderr.String()))
		}
		return fmt.Errorf("mail send to %s: %w", recipient, err)
	}
	return nil
}

// workflowCtx bundles the per-item dependencies of the K8 state machine so the
// branch helpers stay readable. It is built once per item by handleWorkflowItem.
type workflowCtx struct {
	cmd         *cobra.Command
	root        string
	t           cmdTmux
	ctx         context.Context
	store       issuestore.Store
	state       *dispatchState
	stats       *dispatchCycleStats
	dispatchCfg *config.DispatchConfig
	repo        string
	item        ghItem
	source      string
	wf          *config.Workflow
	itemKey     string
}

// handleWorkflowItem drives the K8 state machine for one workflow-labeled item. It
// is the pre-branch the loop calls (then `continue`s), so a workflow item FULLY
// owns its own skip/retry/record/label logic and NEVER touches the non-workflow
// retry-window or record write (HIGH-3).
func handleWorkflowItem(cmd *cobra.Command, root string, t cmdTmux, state *dispatchState, stats *dispatchCycleStats, dispatchCfg *config.DispatchConfig, repo string, item ghItem, source string, wf *config.Workflow) {
	w := &workflowCtx{
		cmd: cmd, root: root, t: t, ctx: context.Background(),
		state: state, stats: stats, dispatchCfg: dispatchCfg,
		repo: repo, item: item, source: source, wf: wf,
		itemKey: fmt.Sprintf("%s#%d", repo, item.Number),
	}
	w.run()
}

// stall surfaces a distinctly-named, detectable stall (never a silent skip) and
// counts it as an error so it stands apart from busy-defers. Phase 4 (K9) lifts
// these into `af dispatch status`; Phase 3 makes them observable on stderr.
func (w *workflowCtx) stall(format string, args ...any) {
	fmt.Fprintf(w.cmd.ErrOrStderr(), "stall %s: "+format+"\n", append([]any{w.itemKey}, args...)...)
	w.stats.errors++
}

func (w *workflowCtx) agentBusy(agent string) bool {
	running, _ := w.t.HasSession(session.SessionName(agent))
	return running
}

func (w *workflowCtx) skipBusy(agent string) {
	fmt.Fprintf(w.cmd.OutOrStdout(), "skip %s: workflow agent %s is busy\n", w.itemKey, agent)
	w.stats.skipped++
}

func (w *workflowCtx) run() {
	phase, ambiguous := workflowCursor(w.item, w.wf.Phases)
	if ambiguous {
		w.stall("workflow %q has an ambiguous cursor (multiple phase labels present)", w.wf.Label)
		return
	}

	store, err := newIssueStore(w.root, dispatchStoreActor)
	if err != nil {
		w.stall("workflow %q cannot open issue store: %v", w.wf.Label, err)
		return
	}
	w.store = store

	if phase == "" {
		w.bootstrap()
		return
	}

	m, ok := phaseMapping(w.dispatchCfg.Mappings, phase)
	if !ok {
		// CRITICAL-2 runtime guard (belt-and-suspenders for the Phase-1 validator).
		w.stall("workflow %q phase %q resolves to no agent", w.wf.Label, phase)
		return
	}

	entry := w.state.Dispatched[w.itemKey] // zero value if absent ⇒ lost-record self-heal
	outcome, reason := evaluatePhase(w.ctx, w.store, w.repo, w.item, w.source, entry, w.dispatchCfg.Mappings, w.wf, phase, m)
	switch outcome {
	case phaseStall:
		w.stall("workflow %q %s", w.wf.Label, reason)
	case phaseWait:
		w.stats.skipped++ // complete formula, artifact pending ⇒ defer (no re-sling, no advance)
	case phaseAdvance:
		if nextPhaseLabel(w.wf.Phases, phase) == "" {
			w.terminal(phase)
		} else {
			w.advance(phase)
		}
	case phaseIncomplete:
		w.resling(phase, m.Agent, entry)
	}
}

// bootstrap: no phase label yet ⇒ add phases[0] (keep agentic + the workflow
// label, C-8), then sling phases[0]'s agent if available (AC-3).
func (w *workflowCtx) bootstrap() {
	first := w.wf.Phases[0]
	m, ok := phaseMapping(w.dispatchCfg.Mappings, first)
	if !ok {
		w.stall("workflow %q phase %q resolves to no agent", w.wf.Label, first)
		return
	}
	if dispatchDryRun {
		fmt.Fprintf(w.cmd.OutOrStdout(), "would bootstrap %s: workflow %q add %q, sling %s\n", w.itemKey, w.wf.Label, first, m.Agent)
		w.stats.dispatched++
		return
	}
	// Add the first phase label; remove nothing — agentic + the workflow label
	// persist to the end of the pipeline (C-8). The label is the cursor, so it is
	// set even if the agent is momentarily busy (the next cycle re-slings via it).
	if err := editItemLabels(w.repo, w.item.Number, w.source, []string{first}, nil); err != nil {
		w.stall("workflow %q bootstrap label edit failed: %v", w.wf.Label, err)
		return
	}
	if w.agentBusy(m.Agent) {
		fmt.Fprintf(w.cmd.OutOrStdout(), "bootstrap %s: workflow %q added %q; agent %s busy, deferring sling\n", w.itemKey, w.wf.Label, first, m.Agent)
		w.stats.skipped++
		return
	}
	if err := w.slingPhase(first, m.Agent, 0); err != nil {
		fmt.Fprintf(w.cmd.ErrOrStderr(), "dispatch %s failed: %v\n", w.itemKey, err)
		w.stats.errors++
		return
	}
	fmt.Fprintf(w.cmd.OutOrStdout(), "bootstrap %s: workflow %q phase %q slung to %s\n", w.itemKey, w.wf.Label, first, m.Agent)
	w.stats.dispatched++
}

// advance: current phase complete ⇒ remove current + add next in ONE edit (W-8),
// then sling next's agent if available (busy ⇒ defer, non-blocking). The old
// phase's record is dropped (PhaseInstanceID cleared / Attempts reset by the fresh
// sling record).
func (w *workflowCtx) advance(phase string) {
	next := nextPhaseLabel(w.wf.Phases, phase)
	m, ok := phaseMapping(w.dispatchCfg.Mappings, next)
	if !ok {
		w.stall("workflow %q next phase %q resolves to no agent", w.wf.Label, next)
		return
	}
	if dispatchDryRun {
		fmt.Fprintf(w.cmd.OutOrStdout(), "would advance %s: workflow %q %q→%q\n", w.itemKey, w.wf.Label, phase, next)
		w.stats.dispatched++
		return
	}
	// Crash-safety (#413 CRIT-1, ORDER MATTERS): make the stale-record deletion durable
	// BEFORE the irreversible label swap. editItemLabels moves the GitHub cursor to the
	// next phase; a crash after it but before the end-of-cycle save (line ~285) would
	// leave evaluatePhase reading the NEW cursor while state still holds the OLD,
	// complete instance — a silent phase skip (paired with evaluatePhase's entry.Phase
	// guard). The old phase record is stale post-advance, so delete + persist, then edit.
	delete(w.state.Dispatched, w.itemKey)
	if err := saveDispatchState(w.root, w.state); err != nil {
		w.stall("workflow %q advance state save failed: %v", w.wf.Label, err)
		return
	}
	if err := editItemLabels(w.repo, w.item.Number, w.source, []string{next}, []string{phase}); err != nil {
		w.stall("workflow %q advance label edit failed: %v", w.wf.Label, err)
		return
	}
	if w.agentBusy(m.Agent) {
		fmt.Fprintf(w.cmd.OutOrStdout(), "advance %s: workflow %q now %q; agent %s busy, deferring sling\n", w.itemKey, w.wf.Label, next, m.Agent)
		w.stats.skipped++
		return
	}
	if err := w.slingPhase(next, m.Agent, 0); err != nil {
		fmt.Fprintf(w.cmd.ErrOrStderr(), "dispatch %s failed: %v\n", w.itemKey, err)
		w.stats.errors++
		return
	}
	fmt.Fprintf(w.cmd.OutOrStdout(), "advance %s: workflow %q %q→%q slung to %s\n", w.itemKey, w.wf.Label, phase, next, m.Agent)
	w.stats.dispatched++
}

// terminal: last phase complete ⇒ remove the last phase label AND agentic in ONE
// edit (C-8), drop the correlation record, and fire ONE workflow-complete notify
// (LOW-3, distinct from per-phase WORK_DONE). The workflow label is left in place;
// the item drops out of the trigger-label query because agentic is gone.
func (w *workflowCtx) terminal(phase string) {
	if dispatchDryRun {
		fmt.Fprintf(w.cmd.OutOrStdout(), "would complete %s: workflow %q remove %q + %q\n", w.itemKey, w.wf.Label, phase, w.dispatchCfg.TriggerLabel)
		w.stats.dispatched++
		return
	}
	// Crash-safety (#413 CRIT-1, ORDER MATTERS): make the record deletion durable BEFORE
	// the irreversible label edit. editItemLabels removes the agentic trigger label, so a
	// crash after it but before the end-of-cycle save (line ~285) would strand a
	// Workflow!="" cursor that pruneDispatchState exempts from the 24h prune — an orphan
	// with no cleanup path. Mirror the advance() ordering: delete + persist, then edit.
	delete(w.state.Dispatched, w.itemKey)
	if err := saveDispatchState(w.root, w.state); err != nil {
		w.stall("workflow %q terminal state save failed: %v", w.wf.Label, err)
		return
	}
	if err := editItemLabels(w.repo, w.item.Number, w.source, nil, []string{phase, w.dispatchCfg.TriggerLabel}); err != nil {
		w.stall("workflow %q terminal label edit failed: %v", w.wf.Label, err)
		return
	}
	if err := sendWorkflowCompleteMail(w.dispatchCfg.NotifyOnComplete, w.wf.Label, w.itemKey); err != nil {
		fmt.Fprintf(w.cmd.ErrOrStderr(), "warning: workflow-complete mail for %s failed: %v\n", w.itemKey, err)
	}
	fmt.Fprintf(w.cmd.OutOrStdout(), "complete %s: workflow %q finished (removed %q + %q)\n", w.itemKey, w.wf.Label, phase, w.dispatchCfg.TriggerLabel)
	w.stats.dispatched++
}

// resling: current phase NOT complete and agent idle ⇒ clear the record and
// re-sling the SAME phase with Attempts++ (#413 CRIT-2: completion keys to the NEW
// instance, never the previous one). Bounded by the retry window AND the attempt
// ceiling (W-7); on exceed, a distinctly-named detectable stall.
func (w *workflowCtx) resling(phase, agent string, entry dispatchEntry) {
	if w.agentBusy(agent) {
		w.skipBusy(agent) // non-blocking (C-13/AC-5)
		return
	}
	// Time-gate like the non-workflow retry window, measured from this phase's
	// dispatch. A lost record (empty PhaseInstanceID) skips the gate and re-slings
	// immediately (self-heal).
	if entry.PhaseInstanceID != "" {
		retryAfter := time.Duration(w.dispatchCfg.RetryAfterSecs) * time.Second
		if time.Since(entry.PhaseDispatchedAt) < retryAfter {
			w.stats.skipped++
			return
		}
	}
	if entry.Attempts >= maxWorkflowAttempts {
		w.stall("workflow %q phase %q exceeded the re-sling ceiling (%d attempts)", w.wf.Label, phase, entry.Attempts)
		return
	}
	if dispatchDryRun {
		fmt.Fprintf(w.cmd.OutOrStdout(), "would re-sling %s: workflow %q phase %q (attempt %d)\n", w.itemKey, w.wf.Label, phase, entry.Attempts+1)
		w.stats.dispatched++
		return
	}
	delete(w.state.Dispatched, w.itemKey)
	if err := w.slingPhase(phase, agent, entry.Attempts+1); err != nil {
		// Restore the correlation record with this attempt COUNTED. slingPhase only writes
		// a record on success, so without this the failed sling leaves no entry and next
		// cycle reads Attempts == 0 — the 0→delete→fail→0 loop that never lets the
		// maxWorkflowAttempts (W-7) ceiling fire. Preserve the prior PhaseInstanceID /
		// PhaseDispatchedAt so evaluatePhase still re-slings (never advances) and the
		// retry-window gate stays anchored to the original dispatch.
		entry.Attempts++
		w.state.Dispatched[w.itemKey] = entry
		fmt.Fprintf(w.cmd.ErrOrStderr(), "dispatch %s failed: %v\n", w.itemKey, err)
		w.stats.errors++
		return
	}
	fmt.Fprintf(w.cmd.OutOrStdout(), "re-sling %s: workflow %q phase %q (attempt %d) slung to %s\n", w.itemKey, w.wf.Label, phase, entry.Attempts+1, agent)
	w.stats.dispatched++
}

// slingPhase slings phase's agent, captures the FRESH instance ID (stdout fallback
// — the dispatcher cannot compute the worktree dir sling reassigns to, so
// captureInstanceID's stdout parse is the real path), and writes the correlation
// record. dispatchedAt is stamped BEFORE the sling so the just-created epic passes
// captureInstanceID's freshness gate (HIGH-3).
func (w *workflowCtx) slingPhase(phase, agent string, attempts int) error {
	dispatchedAt := time.Now().UTC()
	stdout, err := dispatchItem(w.root, agent, w.item.URL, w.dispatchCfg.NotifyOnComplete)
	if err != nil {
		return err
	}
	id := captureInstanceID(w.ctx, w.store, config.AgentDir(w.root, agent), stdout, dispatchedAt)
	w.state.Dispatched[w.itemKey] = dispatchEntry{
		Agent:             agent,
		DispatchedAt:      dispatchedAt,
		ItemURL:           w.item.URL,
		Source:            w.source,
		Workflow:          w.wf.Label,
		Phase:             phase,
		PhaseInstanceID:   id,
		PhaseDispatchedAt: dispatchedAt,
		Attempts:          attempts,
	}
	return nil
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

// pruneDispatchState removes non-workflow entries older than 24 hours. Workflow
// correlation records (Workflow != "") are KEPT regardless of age while their
// pipeline is active (issue #378 Gap 6): a long pipeline must not lose its
// instance-ID pointer to the 24h cutoff. If such a record is ever lost anyway, the
// live GitHub phase label self-heals to a re-sling of the current phase (never an
// advance) — the engine never infers "must be done" from a missing record. The
// terminal branch deletes the record when the pipeline finishes, so kept records
// reflect only still-active pipelines. Non-workflow entries prune EXACTLY as
// before (C-10).
func pruneDispatchState(state *dispatchState) {
	cutoff := time.Now().Add(-24 * time.Hour)
	for key, entry := range state.Dispatched {
		if entry.Workflow != "" {
			continue // active pipeline: label-as-cursor is the backstop, never prune mid-flight
		}
		if entry.DispatchedAt.Before(cutoff) {
			delete(state.Dispatched, key)
		}
	}
}

// dispatchCycleStats tracks per-cycle dispatch outcomes for the summary line.
type dispatchCycleStats struct {
	start      time.Time
	queried    int
	dispatched int
	skipped    int
	errors     int
}

func (s *dispatchCycleStats) String() string {
	return fmt.Sprintf("[%s] dispatch cycle complete: queried=%d dispatched=%d skipped=%d errors=%d elapsed=%s",
		s.start.Format("2006-01-02 15:04:05"),
		s.queried, s.dispatched, s.skipped, s.errors,
		time.Since(s.start).Round(time.Millisecond))
}

// dispatchSessionName is sourced from the single naming authority so the literal
// lives in exactly one place (session.SessionName). In production it is
// "af-dispatch", matching the config.reservedNames reservation.
var dispatchSessionName = session.DispatchSessionName()

func runDispatchStart(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	t := newCmdTmux()
	if running, _ := t.HasSession(dispatchSessionName); running {
		return fmt.Errorf("dispatcher is already running (session: %s)", dispatchSessionName)
	}

	dispatchCfg, err := config.LoadDispatchConfig(root)
	if err != nil {
		return fmt.Errorf("loading dispatch config: %w", err)
	}

	interval := resolveDispatchInterval(dispatchStartInterval, dispatchCfg.IntervalSecs)
	return launchDispatchSession(cmd, root, t, interval)
}

// launchDispatchSession creates the dispatcher tmux session and sends the loop
// command — the shared launch core for the strict CLI path (runDispatchStart)
// and the lenient af-up path (startDispatch). All tmux side-effects route
// through the injected t so tests stay hermetic.
func launchDispatchSession(cmd *cobra.Command, root string, t cmdTmux, interval int) error {
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

// startDispatch starts the dispatcher if not already running, for the af-up
// startup path. An already-running dispatcher is a benign no-op (distinct from
// runDispatchStart's strict CLI error). An absent dispatch.json or the empty
// install default skips with a friendly message; any other config error
// (unreadable file, malformed JSON, invalid values) is surfaced as a warning.
// All config outcomes return nil, but a real launch failure from
// launchDispatchSession (.runtime creation, tmux new-session, send-keys) IS
// returned: the af-up call site folds it into allOK (warn + non-zero exit)
// rather than aborting, so a dispatcher failure still never aborts af up
// mid-flight. The caller passes its own injected cmdTmux.
func startDispatch(cmd *cobra.Command, root string, t cmdTmux) error {
	if running, _ := t.HasSession(dispatchSessionName); running {
		fmt.Fprintf(cmd.OutOrStdout(), "Dispatcher already running (session: %s)\n", dispatchSessionName)
		return nil
	}

	cfg, err := config.LoadDispatchConfig(root)
	if errors.Is(err, config.ErrNotFound) || errors.Is(err, config.ErrMissingField) {
		// Absent, or present but not yet filled in (the install default).
		fmt.Fprintf(cmd.OutOrStdout(), "skipping dispatch (dispatch.json not configured)\n")
		return nil
	}
	if err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: skipping dispatch: %v\n", err)
		return nil
	}

	interval := resolveDispatchInterval(dispatchStartInterval, cfg.IntervalSecs)
	return launchDispatchSession(cmd, root, t, interval)
}

func runDispatchStop(cmd *cobra.Command, args []string) error {
	t := newCmdTmux()
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
	// Read --json first so the early infra-failure paths can honor the documented
	// "always exit 0, branch on .state" contract (the human path keeps its non-zero exit).
	jsonOut, _ := cmd.Flags().GetBool("json")
	wd, err := getWd()
	if err != nil {
		if jsonOut {
			return emitDispatchStatusError(cmd, err)
		}
		return err
	}
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		if jsonOut {
			return emitDispatchStatusError(cmd, err)
		}
		return err
	}

	t := newCmdTmux()
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

	// Real per-phase completion (issue #378 K9): read the recorded instance epic through
	// the store seam and apply the Phase-2 instanceComplete() predicate (terminal AND
	// CloseReasonFormulaComplete) — NOT session absence. Keyed by the dispatch-state map
	// key (<repo>#<n>), which both renderers already iterate. Opened ONCE here (mirroring
	// the agentState precompute) so the renderers stay pure/testable. A store-open or
	// per-instance read failure degrades gracefully to "no completion info" — the status
	// command must stay a cheap, offline-friendly read and never abort on store trouble.
	phaseComplete := computePhaseCompletion(cmd.Context(), root, state.Dispatched)

	if jsonOut {
		// K8: surface the dispatch config-validity state so a fresh/partial factory's
		// readiness ("not configured" vs "references unprovisioned agents" vs "ok") is
		// observable to the web reader and operators (read-only, never aborts).
		configState := loadDispatchConfigState(root)
		return emitDispatchStatusJSON(cmd, running, configState, state.Dispatched, agentState, phaseComplete)
	}

	out := formatDispatchStatus(running, state.Dispatched, agentState, phaseComplete)
	fmt.Fprint(cmd.OutOrStdout(), out)
	return nil
}

// computePhaseCompletion opens the issue store once via the newIssueStore seam (as the
// "dispatch" actor) and returns a map, keyed by the dispatch-state map key, reporting
// whether each workflow entry's recorded instance epic has GENUINELY completed
// (instanceComplete: terminal AND closed with CloseReasonFormulaComplete). Entries with
// no recorded PhaseInstanceID are skipped. Any store error degrades to an empty/partial
// map rather than failing the status command — completion just reads as "not yet" /
// drift, exactly as the design intends (stalls surface, never masquerade as completion).
func computePhaseCompletion(ctx context.Context, root string, entries map[string]dispatchEntry) map[string]bool {
	phaseComplete := make(map[string]bool)
	store, err := newIssueStore(root, dispatchStoreActor)
	if err != nil {
		return phaseComplete
	}
	for key, entry := range entries {
		if entry.PhaseInstanceID == "" {
			continue
		}
		iss, err := store.Get(ctx, entry.PhaseInstanceID)
		if err != nil {
			continue
		}
		phaseComplete[key] = instanceComplete(iss)
	}
	return phaseComplete
}

// dispatchStatusEntry is the per-dispatch JSON shape emitted by
// `af dispatch status --json`. The field set is a versioned contract pinned by
// TestDispatchStatus_JSON_SchemaSnapshot. "issue" is the dispatch key (the
// issue/PR identifier under which the item is recorded); "agent_running"
// reflects live tmux liveness of the assigned agent.
type dispatchStatusEntry struct {
	Issue        string    `json:"issue"`
	Agent        string    `json:"agent"`
	AgentRunning bool      `json:"agent_running"`
	ItemURL      string    `json:"item_url"`
	Source       string    `json:"source"`
	DispatchedAt time.Time `json:"dispatched_at"`

	// Workflow observability (issue #378 K9, additive — populated only for
	// workflow-dispatched entries; omitempty preserves the pinned 6-key non-workflow
	// contract). Workflow/Phase are read straight off the recorded dispatchEntry;
	// PhaseComplete is the REAL completion signal — instanceComplete() on the recorded
	// PhaseInstanceID read through the store seam — NOT tmux session absence.
	Workflow      string `json:"workflow,omitempty"`
	Phase         string `json:"phase,omitempty"`
	PhaseComplete bool   `json:"phase_complete,omitempty"`
}

// dispatchStatusJSON is the top-level success shape of
// `af dispatch status --json`.
type dispatchStatusJSON struct {
	DispatcherRunning bool                  `json:"dispatcher_running"`
	ConfigState       string                `json:"config_state"`
	Entries           []dispatchStatusEntry `json:"entries"`
}

// emitDispatchStatusError writes a {"state":"error",...} envelope through the cobra
// output seam and returns nil, preserving the `af dispatch status --json` "always exit 0,
// branch on .state" contract that the web reader depends on. Mirrors emitAgentsError /
// emitFormulaError so a consumer never sees a non-zero exit with an empty stdout.
func emitDispatchStatusError(cmd *cobra.Command, e error) error {
	data, err := json.Marshal(stepErrorOutput{State: "error", Error: e.Error()})
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), `{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

// emitDispatchStatusJSON marshals the dispatcher state as JSON to stdout. Entries
// are sorted by issue key for deterministic, snapshot-stable output.
func emitDispatchStatusJSON(cmd *cobra.Command, running bool, configState string, entries map[string]dispatchEntry, agentState map[string]bool, phaseComplete map[string]bool) error {
	keys := make([]string, 0, len(entries))
	for k := range entries {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := dispatchStatusJSON{
		DispatcherRunning: running,
		ConfigState:       configState,
		Entries:           make([]dispatchStatusEntry, 0, len(entries)),
	}
	for _, k := range keys {
		e := entries[k]
		out.Entries = append(out.Entries, dispatchStatusEntry{
			Issue:        k,
			Agent:        e.Agent,
			AgentRunning: agentState[e.Agent],
			ItemURL:      e.ItemURL,
			Source:       e.Source,
			DispatchedAt: e.DispatchedAt,
			// Workflow/Phase come straight off the record; PhaseComplete is the real
			// instanceComplete() signal (omitempty keeps the 6-key non-workflow contract).
			Workflow:      e.Workflow,
			Phase:         e.Phase,
			PhaseComplete: phaseComplete[k],
		})
	}

	data, err := json.Marshal(out)
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), `{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

// buildDispatchLoopCmd constructs the shell loop command for the dispatcher tmux session.
func buildDispatchLoopCmd(afBin string, interval int) string {
	return fmt.Sprintf(
		`trap 'echo "[$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)] dispatch loop exiting (signal)" | tee -a .runtime/dispatch.log; exit 1' TERM INT HUP; `+
			`while true; do `+
			`echo "[$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)] dispatch cycle starting" >> .runtime/dispatch.log; `+
			`%s dispatch 2>&1 | tee -a .runtime/dispatch.log; `+
			`rc=$?; `+
			`if [ $rc -ne 0 ]; then echo "[$(date -u +%%Y-%%m-%%dT%%H:%%M:%%SZ)] dispatch exited with code $rc" >> .runtime/dispatch.log; fi; `+
			`sleep %d; `+
			`done`,
		afBin, interval)
}

// resolveDispatchInterval returns the flag value if non-zero, otherwise the config value.
func resolveDispatchInterval(flagValue, configValue int) int {
	if flagValue > 0 {
		return flagValue
	}
	return configValue
}

// formatDispatchStatus formats the dispatcher status and dispatch history as a string.
//
// STATUS is no longer inferred from tmux session absence (issue #378 K9). The agent's
// tmux liveness is now an AVAILABILITY axis only (running / idle); genuine phase
// completion is read from phaseComplete (instanceComplete() on the recorded instance
// epic), keyed by the dispatch-state map key. Declared-vs-actual drift is surfaced for
// workflow entries whose agent is gone but whose instance has NOT genuinely completed —
// the dispatcher will re-sling them, so a stall can never masquerade as completion (D5-D).
func formatDispatchStatus(running bool, entries map[string]dispatchEntry, agentState map[string]bool, phaseComplete map[string]bool) string {
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
	fmt.Fprintln(w, "ISSUE\tSOURCE\tAGENT\tWORKFLOW\tPHASE\tSTATUS\tDISPATCHED")
	for _, key := range keys {
		entry := entries[key]

		// Availability axis (tmux), decoupled from completion.
		avail := "idle"
		if agentState[entry.Agent] {
			avail = "running"
		}

		// STATUS: real completion comes from the store (phaseComplete), never tmux
		// absence. For a workflow entry whose agent is gone but whose phase has not
		// genuinely completed, surface the drift — the dispatcher will re-sling it.
		status := avail
		switch {
		case phaseComplete[key]:
			status = "completed"
		case entry.Workflow != "" && entry.PhaseInstanceID == "" && !agentState[entry.Agent]:
			status = "idle (drift: no instance)"
		case entry.Workflow != "" && !agentState[entry.Agent]:
			status = "idle (will re-sling)"
		}

		workflow := entry.Workflow
		if workflow == "" {
			workflow = "-"
		}
		phase := entry.Phase
		if phase == "" {
			phase = "-"
		}

		age := time.Since(entry.DispatchedAt).Round(time.Minute)
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s ago\n", key, entry.Source, entry.Agent, workflow, phase, status, age)
	}
	w.Flush()

	return buf.String()
}
