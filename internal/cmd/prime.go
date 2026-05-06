package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/lock"
	"github.com/stempeck/agentfactory/internal/templates"
)

var primeHookMode bool

var primeCmd = &cobra.Command{
	Use:   "prime",
	Short: "Inject agent identity context",
	Long:  "Output session metadata, role template, and startup directive for the current agent.",
	RunE:  runPrime,
}

func init() {
	primeCmd.Flags().BoolVar(&primeHookMode, "hook", false, "Read session ID from stdin JSON (Claude Code SessionStart hook)")
	primeCmd.Flags().Bool("formula", false, "Deprecated: formula context is now automatic")
	primeCmd.Flags().MarkHidden("formula")
	rootCmd.AddCommand(primeCmd)
}

func runPrime(cmd *cobra.Command, args []string) error {
	cwd, err := getWd()
	if err != nil {
		return err
	}

	// 1. Find factory root
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}

	// 2. Check if cwd is factory root — fan out to all agents
	rel, err := filepath.Rel(factoryRoot, cwd)
	if err != nil {
		return fmt.Errorf("detecting role: %w", err)
	}
	if rel == "." {
		// --hook mode makes no sense from factory root (it's per-agent)
		if primeHookMode {
			return fmt.Errorf("cannot use --hook from factory root (hook mode is per-agent)")
		}
		return runPrimeAll(cmd.Context(), cmd.OutOrStdout(), factoryRoot)
	}

	// 3. Handle --hook mode (single-agent path)
	if primeHookMode {
		sessionID := readHookSessionIDFromStdin()
		if sessionID != "" {
			persistSessionID(cwd, sessionID)
		}
	}

	// 4. Detect role
	role, _, err := detectRole(cwd, factoryRoot)
	if err != nil {
		return err
	}

	return primeAgent(cmd.Context(), cmd.OutOrStdout(), factoryRoot, role, cwd)
}

// runPrimeAll primes all provisioned agents when run from the factory root.
func runPrimeAll(ctx context.Context, out io.Writer, factoryRoot string) error {
	agentsPath := config.AgentsConfigPath(factoryRoot)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	primed := 0
	for name := range agentsCfg.Agents {
		agentDir := config.AgentDir(factoryRoot, name)
		if _, err := os.Stat(agentDir); os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "%s: skipped (not provisioned, run af install %s)\n", name, name)
			continue
		}
		if err := primeAgent(ctx, out, factoryRoot, name, agentDir); err != nil {
			fmt.Fprintf(os.Stderr, "%s: prime failed: %v\n", name, err)
			continue
		}
		primed++
	}

	if primed == 0 {
		return fmt.Errorf("no provisioned agents found (run af install <role>)")
	}
	return nil
}

// primeAgent outputs session metadata, role template, and startup directive for a single agent.
// workDir is the agent's working directory — may be a worktree agent dir or factory agent dir.
func primeAgent(ctx context.Context, out io.Writer, factoryRoot, role, workDir string) error {
	agentsPath := config.AgentsConfigPath(factoryRoot)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	agentEntry, ok := agentsCfg.Agents[role]
	if !ok {
		return fmt.Errorf("agent %q not found in agents.json", role)
	}

	// Acquire identity lock
	sessionID := getSessionID(workDir, role)
	acquireIdentityLock(workDir, sessionID)

	// Output session metadata
	fmt.Fprintf(out, "[AGENT FACTORY] role:%s pid:%d session:%s\n", role, os.Getpid(), sessionID)

	// Render role template — try agent-specific template first, fall back to type default
	tmpl := templates.New()
	templateRole := role
	if !tmpl.HasRole(templateRole) {
		if agentEntry.Formula != "" {
			fmt.Fprintf(os.Stderr, "WARNING: agent %q is formula-generated but its template is not embedded in the binary. Agent will function via workspace CLAUDE.md but af prime will inject a generic template.\n", role)
		}
		templateRole = agentEntry.Type
		if templateRole == "interactive" {
			templateRole = "manager"
		} else if templateRole == "autonomous" {
			templateRole = "supervisor"
		}
	}

	// Use localRoot for RootDir so worktree agents get their worktree root
	rootDir := factoryRoot
	if lr, err := config.FindLocalRoot(workDir); err == nil {
		rootDir = lr
	}

	data := templates.RoleData{
		Role:        role,
		Description: agentEntry.Description,
		RootDir:     rootDir,
		WorkDir:     workDir,
	}
	output, err := tmpl.RenderRole(templateRole, data)
	if err != nil {
		return fmt.Errorf("rendering role template: %w", err)
	}
	fmt.Fprint(out, output)

	// Output worktree context if applicable
	outputWorktreeContext(out, workDir)

	// Output startup directive
	outputStartupDirective(out, agentEntry.Type)

	// Inject formula workflow context if active (self-guarding -- no-op when no formula)
	beadsDir := filepath.Join(factoryRoot, ".beads")
	outputFormulaContext(ctx, out, workDir, beadsDir)
	outputCheckpointContext(out, workDir)

	// Write checkpoint for crash recovery (skip during hook mode -- session just starting)
	if !primeHookMode {
		writeFormulaCheckpoint(ctx, workDir, beadsDir)
	}

	// Append pending mail (best-effort)
	runMailCheckInject(out)

	return nil
}

// detectRole determines the agent role from cwd relative to factory root.
// Delegates to resolveAgentName (helpers.go) for three-tier resolution, then
// loads the AgentEntry from agents.json.
func detectRole(cwd, factoryRoot string) (string, *config.AgentEntry, error) {
	agentName, err := resolveAgentName(cwd, factoryRoot)
	if err != nil {
		return "", nil, fmt.Errorf("detecting role: %w", err)
	}

	agentsPath := config.AgentsConfigPath(factoryRoot)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return "", nil, fmt.Errorf("detecting role: %w", err)
	}

	entry, ok := agentsCfg.Agents[agentName]
	if !ok {
		return "", nil, fmt.Errorf("agent %q not found in agents.json", agentName)
	}

	return agentName, &entry, nil
}

// outputWorktreeContext prints worktree information if the agent is running in a worktree.
// Reads .runtime/worktree_id from workDir; if absent, the agent is not in a worktree.
func outputWorktreeContext(out io.Writer, workDir string) {
	wtIDPath := filepath.Join(workDir, ".runtime", "worktree_id")
	wtIDData, err := os.ReadFile(wtIDPath)
	if err != nil {
		return // not in a worktree
	}
	wtID := strings.TrimSpace(string(wtIDData))
	if wtID == "" {
		return
	}

	// Get current branch
	branch := getCurrentGitBranch(workDir)

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "## Worktree Isolation")
	fmt.Fprintln(out, "")
	if branch != "" {
		fmt.Fprintf(out, "[WORKTREE] branch: %s | id: %s | root: %s\n", branch, wtID, workDir)
	} else {
		fmt.Fprintf(out, "[WORKTREE] id: %s | root: %s\n", wtID, workDir)
	}
	fmt.Fprintln(out, "")
}

// readHookSessionID parses a session ID from a JSON reader.
func readHookSessionID(r io.Reader) string {
	var payload struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(r).Decode(&payload); err != nil {
		return ""
	}
	return payload.SessionID
}

// readHookSessionIDFromStdin reads the session ID from stdin, guarding against terminal input.
func readHookSessionIDFromStdin() string {
	stat, err := os.Stdin.Stat()
	if err != nil {
		return ""
	}
	// Only read if stdin is a pipe (not a terminal)
	if (stat.Mode() & os.ModeCharDevice) != 0 {
		return ""
	}
	return readHookSessionID(os.Stdin)
}

// persistSessionID writes the session ID to <dir>/.runtime/session_id.
func persistSessionID(dir, sessionID string) {
	runtimeDir := filepath.Join(dir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "session_id"), []byte(sessionID), 0o644)
}

// getSessionID reads a persisted session ID or returns a fallback.
func getSessionID(dir, role string) string {
	path := filepath.Join(dir, ".runtime", "session_id")
	data, err := os.ReadFile(path)
	if err == nil && len(data) > 0 {
		return string(data)
	}
	return fmt.Sprintf("%s-%d", role, os.Getpid())
}

// acquireIdentityLock attempts to acquire the identity lock. Warns on failure, does not fail hard.
func acquireIdentityLock(workDir, sessionID string) {
	l := lock.New(workDir)
	if err := l.Acquire(sessionID); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: identity lock: %v\n", err)
	}
}

// outputStartupDirective prints startup instructions based on agent type.
// Custom directives from agents.json are delivered via the startup nudge
// (session.Manager.Start), not here — tool output is not treated as a
// direct instruction by Claude.
func outputStartupDirective(w io.Writer, agentType string) {
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "## Startup Directive")
	fmt.Fprintln(w, "")
	switch agentType {
	case "autonomous":
		fmt.Fprintln(w, "1. Check mail for pending instructions (`af mail inbox`)")
		fmt.Fprintln(w, "2. Act on any hooked work or queued tasks")
		fmt.Fprintln(w, "3. Begin autonomous execution")
	default: // interactive
		fmt.Fprintln(w, "1. Check mail for pending instructions (`af mail inbox`)")
		fmt.Fprintln(w, "2. Act on any instructions or requests found in mail")
		fmt.Fprintln(w, "3. If no actionable mail, await user input")
	}
	// Custom directive is delivered via the startup nudge (session.Manager.Start),
	// not here — tool output is not treated as a direct instruction by Claude.
	fmt.Fprintln(w, "")
}

// isTestBinary reports whether the current process is a Go test binary.
// Go test binaries are named "<pkg>.test" (e.g., "cmd.test"). Spawning
// subprocess commands via os.Executable() from a test binary causes infinite
// recursion because Cobra routes the args back through the same command tree.
func isTestBinary() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	return strings.HasSuffix(filepath.Base(exe), ".test")
}

// runMailCheckInject shells out to `af mail check --inject` and appends output.
func runMailCheckInject(w io.Writer) {
	if isTestBinary() {
		return // no-op under go test to prevent fork bomb
	}

	// Find the af binary — use current executable path
	afPath, err := os.Executable()
	if err != nil {
		// Fallback: try PATH
		afPath, err = exec.LookPath("af")
		if err != nil {
			return // best-effort
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, afPath, "mail", "check", "--inject")
	cmd.Env = os.Environ()
	output, err := cmd.Output()
	if err != nil {
		return // best-effort
	}
	if len(output) > 0 {
		w.Write(output)
	}
}

// readHookedFormulaID reads the formula instance bead ID from <workDir>/.runtime/hooked_formula.
// Returns empty string if file doesn't exist (no formula active).
func readHookedFormulaID(workDir string) string {
	path := filepath.Join(workDir, ".runtime", "hooked_formula")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

// outputFormulaContext injects formula workflow context into the prime output.
// Lazy-constructs the issue store via the newIssueStore seam only when a
// formula is hooked, so non-hooked prime paths don't need bd on PATH.
func outputFormulaContext(ctx context.Context, out io.Writer, workDir, beadsDir string) {
	instanceID := readHookedFormulaID(workDir)
	if instanceID == "" {
		return
	}

	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(workDir, beadsDir, actor)
	if err != nil {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "## Formula Workflow")
		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "**Formula:** %s\n", instanceID)
		fmt.Fprintln(out, "**Status:** unknown (step query failed)")
		fmt.Fprintf(out, "Error: %v\n", err)
		return
	}

	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: instanceID})
	if err != nil {
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "## Formula Workflow")
		fmt.Fprintln(out, "")
		fmt.Fprintf(out, "**Formula:** %s\n", instanceID)
		fmt.Fprintln(out, "**Status:** unknown (step query failed)")
		fmt.Fprintf(out, "Error: %v\n", err)
		return
	}
	totalSteps := result.TotalSteps
	if totalSteps == 0 {
		ts, tsErr := countAllChildren(ctx, store, instanceID)
		if tsErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not count children: %v\n", tsErr)
		}
		totalSteps = ts
	}
	if totalSteps == 0 {
		totalSteps = 1
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "## Formula Workflow")
	fmt.Fprintln(out, "")

	// Get formula name from instance bead title.
	formulaName := instanceID
	if iss, err := store.Get(ctx, instanceID); err == nil && iss.Title != "" {
		formulaName = iss.Title
	}
	fmt.Fprintf(out, "**Formula:** %s\n", formulaName)

	if len(result.Steps) == 0 {
		openChildren, err := store.List(ctx, issuestore.Filter{
			Parent:   instanceID,
			Statuses: []issuestore.Status{issuestore.StatusOpen},
		})
		if err != nil {
			fmt.Fprintf(out, "**Status:** error querying formula state: %v\n", err)
			return
		}
		if len(openChildren) > 0 {
			fmt.Fprintln(out, "**Status:** blocked")
			fmt.Fprintln(out, "")
			fmt.Fprintln(out, "Some formula steps remain but are not actionable.")
			return
		}
		fmt.Fprintln(out, "**Status:** all_complete")
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, "All formula steps are complete.")
		return
	}

	step := result.Steps[0]
	openChildren, err := store.List(ctx, issuestore.Filter{
		Parent:   instanceID,
		Statuses: []issuestore.Status{issuestore.StatusOpen},
	})
	if err != nil {
		fmt.Fprintf(out, "**Status:** error querying open steps: %v\n", err)
		return
	}
	openCount := len(openChildren)
	stepNum := totalSteps - openCount + 1
	if stepNum < 1 {
		stepNum = 1
	}

	fmt.Fprintf(out, "**Progress:** Step %d of %d: %s\n", stepNum, totalSteps, step.Title)
	fmt.Fprintln(out, "**Status:** ready")
	fmt.Fprintln(out, "")

	// Get step description from the store.
	var description string
	if iss, err := store.Get(ctx, step.ID); err == nil {
		description = iss.Description
	}
	outputCommandPreflight(out, description)

	if description != "" {
		fmt.Fprintln(out, "### Current Step Instructions")
		fmt.Fprintln(out, "")
		fmt.Fprintln(out, description)
		fmt.Fprintln(out, "")
		outputStandingDirective(out)
	}

	// Check if this is a gate step
	if isGateStep(ctx, store, step.ID, description) {
		fmt.Fprintln(out, "**WARNING: This is a GATE step.** Complete all work, then run:")
		fmt.Fprintf(out, "  af done --phase-complete --gate %s\n", step.ID)
		fmt.Fprintln(out, "Do NOT skip this gate. Quality gates protect the CEO's time.")
		fmt.Fprintln(out, "")
	}

	fmt.Fprintln(out, "### Work Loop")
	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "1. Complete the current step")
	fmt.Fprintln(out, "2. Run `af done` to close it and advance")
	fmt.Fprintln(out, "3. If gate step: run `af done --phase-complete --gate <gate-id>`")
}

// outputCheckpointContext injects checkpoint/resume context from a previous session.
func outputCheckpointContext(out io.Writer, workDir string) {
	cp, err := checkpoint.Read(workDir)
	if err != nil || cp == nil {
		return
	}
	if cp.IsStale(24 * time.Hour) {
		_ = checkpoint.Remove(workDir)
		return
	}

	fmt.Fprintln(out, "")
	fmt.Fprintln(out, "## Previous Session Checkpoint")
	fmt.Fprintf(out, "A previous session left a checkpoint %s ago.\n\n", cp.Age().Round(time.Minute))
	if cp.StepTitle != "" {
		fmt.Fprintf(out, "  **Working on:** %s\n", cp.StepTitle)
	}
	if cp.FormulaID != "" {
		fmt.Fprintf(out, "  **Formula:** %s\n", cp.FormulaID)
	}
	if cp.CurrentStep != "" {
		fmt.Fprintf(out, "  **Step:** %s\n", cp.CurrentStep)
	}
	if cp.Branch != "" {
		fmt.Fprintf(out, "  **Branch:** %s\n", cp.Branch)
		currentBranch := getCurrentGitBranch(workDir)
		if currentBranch != "" && cp.Branch != "HEAD" && currentBranch != cp.Branch {
			fmt.Fprintf(out, "  WARNING: Branch changed since checkpoint: was %s, now %s\n", cp.Branch, currentBranch)
		}
	}
	if len(cp.ModifiedFiles) > 0 {
		fmt.Fprintf(out, "  **Modified files:** %s\n", strings.Join(cp.ModifiedFiles, ", "))
		fmt.Fprintln(out, "  (These files were modified when the previous session checkpointed. Check if changes were committed or lost.)")
	}
	if cp.Notes != "" {
		fmt.Fprintf(out, "  **Notes:** %s\n", cp.Notes)
	}
	fmt.Fprintln(out, "")
}

// getCurrentGitBranch returns the current git branch for the given directory.
// Returns empty string on error or if HEAD is detached (literal "HEAD").
func getCurrentGitBranch(workDir string) string {
	cmd := exec.Command("git", "-C", workDir, "rev-parse", "--abbrev-ref", "HEAD")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(output))
}

// writeFormulaCheckpoint captures current formula state for crash recovery.
// Called during PreCompact (--formula without --hook). Lazy-constructs the
// store via the seam only when a formula is hooked.
func writeFormulaCheckpoint(ctx context.Context, workDir, beadsDir string) {
	instanceID := readHookedFormulaID(workDir)
	if instanceID == "" {
		return
	}
	cp, err := checkpoint.Capture(workDir)
	if err != nil {
		return // best-effort
	}
	actor := os.Getenv("BD_ACTOR")
	store, err := newIssueStore(workDir, beadsDir, actor)
	if err != nil {
		cp.WithFormula(instanceID, "", "")
		cp.WithHookedBead(instanceID)
		if cp.SessionID == "" {
			cp.SessionID = os.Getenv("CLAUDE_SESSION_ID")
		}
		_ = checkpoint.Write(workDir, cp) // best-effort
		return
	}
	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: instanceID})
	if err != nil || len(result.Steps) == 0 {
		cp.WithFormula(instanceID, "", "")
	} else {
		cp.WithFormula(instanceID, result.Steps[0].ID, result.Steps[0].Title)
	}
	cp.WithHookedBead(instanceID)
	if cp.SessionID == "" {
		cp.SessionID = os.Getenv("CLAUDE_SESSION_ID")
	}
	_ = checkpoint.Write(workDir, cp) // best-effort
}

// isGateStep detects whether a step is a gate step using dual detection:
// structural (open blockers) + heuristic (description keywords).
func isGateStep(ctx context.Context, store issuestore.Store, stepID, description string) bool {
	// Primary: structural check via bead metadata
	if stepHasOpenBlockers(ctx, store, stepID) {
		return true
	}
	// Fallback: description text heuristic
	lower := strings.ToLower(description)
	markers := []string{
		"af done --phase-complete",
		"this step has a gate",
		"gate enforces",
		"gate blocks closure",
	}
	for _, m := range markers {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}

// stepHasOpenBlockers checks if a step bead has any non-terminal blockers.
//
// Defensive rewrite (Phase 1 investigation finding H-C-A-2): no adapter
// filters Issue.BlockedBy by terminal status. IMPLREADME Gotcha #D6 is
// WRONG — trusting
// len(iss.BlockedBy) > 0 would over-report gate steps whenever a blocker
// is already closed/done. We therefore re-read each blocker via store.Get
// and use Status.IsTerminal() to match the old "status != closed"
// semantics.
func stepHasOpenBlockers(ctx context.Context, store issuestore.Store, stepID string) bool {
	iss, err := store.Get(ctx, stepID)
	if err != nil {
		return false
	}
	for _, ref := range iss.BlockedBy {
		blocker, err := store.Get(ctx, ref.ID)
		if err != nil {
			// Can't verify the blocker — treat as open (defensive).
			return true
		}
		if !blocker.Status.IsTerminal() {
			return true
		}
	}
	return false
}
