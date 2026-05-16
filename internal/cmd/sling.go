package cmd

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/checkpoint"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/templates"
	"github.com/stempeck/agentfactory/internal/tmux"
	"github.com/stempeck/agentfactory/internal/worktree"
)

var (
	slingFormulaName string
	slingVars        []string
	slingAgent       string
	slingNoLaunch    bool
	slingReset       bool
	slingCaller      string
	slingPersistent  bool
)

// InstantiateParams contains parameters for formula instantiation.
// Decoupled from package globals so both runFormulaInstantiation()
// and dispatchToSpecialist() can provide values.
type InstantiateParams struct {
	Ctx             context.Context
	FormulaName     string
	CLIVars         []string
	AgentName       string
	Root            string
	WorkDir         string
	TaskDescription string
	CallerIdentity  string
}

var slingCmd = &cobra.Command{
	Use:   "sling [task]",
	Short: "Instantiate a formula or dispatch a task to a specialist agent",
	Long: `Sling parses a formula TOML file, creates step beads with DAG
dependencies via bd, and launches an agent in a tmux session.

Specialist dispatch mode: when --agent names a specialist (an agent with a
formula field in agents.json), the agent's formula is instantiated with the
task injected as a variable and embedded in the bead description.

Formula succession: if a prior formula is still active in the target workspace,
sling errors and directs you to --reset. See USING_AGENTFACTORY.md "Formula
Succession" for details.

Examples:
  af sling --formula my-workflow --var issue=bd-42 --agent manager
  af sling --agent ultraimplement "implement issue #42"`,
	Args: cobra.MaximumNArgs(1),
	RunE: runSling,
}

func init() {
	slingCmd.Flags().StringVar(&slingFormulaName, "formula", "", "Formula name to instantiate")
	slingCmd.Flags().StringSliceVar(&slingVars, "var", nil, "Variable in key=val format (repeatable)")
	slingCmd.Flags().StringVar(&slingAgent, "agent", "", "Agent to dispatch to or launch")
	slingCmd.Flags().BoolVar(&slingNoLaunch, "no-launch", false, "Create beads only, skip tmux session launch")
	slingCmd.Flags().BoolVar(&slingReset, "reset", false, "Stop target agent, clean stale runtime state, and re-dispatch. Use when a prior formula was abandoned or you want a clean start.")
	slingCmd.Flags().StringVar(&slingCaller, "caller", "", "Explicit caller identity for WORK_DONE mail (used by dispatch)")
	slingCmd.Flags().MarkHidden("caller")
	slingCmd.Flags().BoolVar(&slingPersistent, "persistent", false, "Keep session alive after formula completion (do not auto-terminate). !IMPORTANT! ONLY used in formulas instructions, not ad-hoc specialist dispatch. Use with caution: the session will not auto-terminate on formula completion.")
	rootCmd.AddCommand(slingCmd)
}

func runSling(cmd *cobra.Command, args []string) error {
	if err := validateSlingArgs(slingFormulaName, slingAgent, args); err != nil {
		return err
	}

	wd, err := getWd()
	if err != nil {
		return err
	}

	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	// Specialist dispatch: --agent without --formula
	if slingFormulaName == "" && slingAgent != "" {
		return dispatchToSpecialist(cmd, root, wd, slingAgent, args[0])
	}

	// Formula instantiation path (existing behavior)
	return runFormulaInstantiation(cmd, root, wd, args)
}

// validateSlingArgs checks that the flag/arg combination is valid.
func validateSlingArgs(formulaName, agent string, args []string) error {
	if formulaName == "" && agent == "" {
		return fmt.Errorf("--formula is required unless --agent is provided with a task")
	}
	if formulaName == "" && agent != "" {
		// Specialist dispatch requires a task string
		if len(args) == 0 || strings.TrimSpace(args[0]) == "" {
			return fmt.Errorf("task description required: af sling --agent %s \"<task>\"", agent)
		}
	}
	return nil
}

// dispatchToSpecialist instantiates the specialist agent's formula and launches
// the agent session. The task is injected as a synthetic CLI variable and
// embedded in the formula instance bead description. Session launch uses
// launchAgentSession (no initial prompt), so the startup nudge fires and
// af prime delivers formula context.
func dispatchToSpecialist(cmd *cobra.Command, root, callerWd, agentName, task string) error {
	// 1. Validate the agent is a specialist
	entry, err := resolveSpecialistAgent(root, agentName)
	if err != nil {
		return err
	}

	agentDir := config.AgentDir(root, agentName)

	// Pre-flight: check if agent is already running BEFORE creating beads
	mgr := session.NewManager(root, agentName, entry)
	if slingReset {
		// Reset target agent — stop session and clean stale runtime files
		if err := mgr.Stop(); err != nil && err != session.ErrNotRunning {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to stop %s: %v\n", agentName, err)
		}
		// Clean up agent's worktree if one exists
		if meta, wtErr := worktree.FindByOwner(root, agentName); wtErr == nil && meta != nil {
			if rmErr := worktree.Remove(root, meta); rmErr != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to remove worktree for %s: %v\n", agentName, rmErr)
			} else {
				fmt.Fprintf(cmd.ErrOrStderr(), "cleaned up worktree %s for %s\n", meta.ID, agentName)
			}
		}
		for _, f := range []string{
			filepath.Join(agentDir, ".runtime", "session_id"),
			filepath.Join(agentDir, ".runtime", "hooked_formula"),
			filepath.Join(agentDir, ".runtime", "dispatched"),
			filepath.Join(agentDir, ".agent-checkpoint.json"),
		} {
			if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to remove %s: %v\n", f, err)
			}
		}
	} else if !slingNoLaunch {
		// Not resetting — check if agent is running and fail early
		sessionID := session.SessionName(agentName)
		t := tmux.NewTmux()
		if running, _ := t.HasSession(sessionID); running {
			return fmt.Errorf("%s is already running; use --reset to stop and re-dispatch, "+
				"or af down %s first", agentName, agentName)
		}
	}

	// Load factory config for MaxWorktrees cap
	factoryCfg, fcErr := config.LoadFactoryConfig(config.FactoryConfigPath(root))
	if fcErr != nil {
		return fmt.Errorf("loading factory config: %w", fcErr)
	}

	// Worktree resolution — unified start-time check (issue #86).
	envWT := os.Getenv("AF_WORKTREE")
	envWTID := os.Getenv("AF_WORKTREE_ID")
	creator, _ := resolveAgentName(callerWd, root)

	// Issue #188: non-specialist callers (orchestrators like manager/supervisor)
	// dispatch independent work — each agent needs its own worktree. Only
	// specialists (formula-bearing agents) do collaborative dispatch where
	// worktree sharing is correct.
	if creator != "" {
		if callerEntry, err := resolveCallerAgent(root, creator); err == nil && callerEntry.Formula == "" {
			envWT = ""
			envWTID = ""
			creator = ""
		}
	}

	worktreePath, worktreeID, wtCreated, wtErr := worktree.ResolveOrCreate(root, agentName, creator, envWT, envWTID, worktree.CreateOpts{MaxWorktrees: factoryCfg.MaxWorktrees})
	if wtErr != nil {
		return fmt.Errorf("worktree creation failed for %s: %w", agentName, wtErr)
	}
	if worktreePath != "" {
		wtAgentDir, setupErr := worktree.SetupAgent(root, worktreePath, agentName, wtCreated)
		if setupErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree SetupAgent: %v\n", setupErr)
		} else {
			agentDir = wtAgentDir
		}
		if wtCreated {
			fmt.Fprintf(cmd.OutOrStdout(), "Created worktree %s for %s\n", worktreeID, agentName)
		}
	}

	// 2. Persist caller identity for WORK_DONE mail routing.
	// Remove any stale caller file from a previous dispatch, then write.
	// This MUST happen BEFORE instantiateFormulaWorkflow because
	// persistFormulaCaller has no-overwrite semantics.
	os.Remove(filepath.Join(agentDir, ".runtime", "formula_caller"))
	os.Remove(filepath.Join(agentDir, ".runtime", "hooked_formula"))
	var callerIdentity string
	if slingCaller != "" {
		callerIdentity = slingCaller
		persistFormulaCaller(agentDir, callerIdentity)
	} else {
		callerIdentity = ensureCallerIdentity(callerWd, root, agentDir, cmd.ErrOrStderr())
	}
	if !slingPersistent {
		writeDispatchedMarker(agentDir, callerIdentity)
	}

	if !templates.New().HasRole(agentName) {
		fmt.Fprintf(cmd.ErrOrStderr(), "WARNING: agent %q template not embedded in binary. Agent will function via workspace CLAUDE.md.\n", agentName)
	}

	// 3. Instantiate the agent's formula with the task as a synthetic CLI var
	params := InstantiateParams{
		Ctx:             cmd.Context(),
		FormulaName:     entry.Formula,
		CLIVars:         append(slingVars, fmt.Sprintf("task=%s", task)),
		AgentName:       agentName,
		Root:            root,
		WorkDir:         agentDir,
		TaskDescription: task,
		CallerIdentity:  callerIdentity,
	}

	if _, _, _, err := instantiateFormulaWorkflow(params, cmd.OutOrStdout()); err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Dispatched to %s: %s\n", session.SessionName(agentName), task)

	// 4. Launch the agent session (unless --no-launch)
	if slingNoLaunch {
		return nil
	}

	return launchAgentSession(cmd, root, agentName, worktreePath, worktreeID)
}

// resolveSpecialistAgent loads agents.json and validates that the named agent
// is a specialist (has a formula field).
func resolveSpecialistAgent(root, agentName string) (config.AgentEntry, error) {
	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return config.AgentEntry{}, fmt.Errorf("loading agents config: %w", err)
	}

	entry, ok := agentsCfg.Agents[agentName]
	if !ok {
		return config.AgentEntry{}, fmt.Errorf("agent %q not found in agents.json", agentName)
	}

	if entry.Formula == "" {
		return config.AgentEntry{}, fmt.Errorf("agent %q is not a specialist (no formula field in agents.json)", agentName)
	}

	return entry, nil
}

// resolveCallerAgent loads agents.json and returns the entry for the named agent.
func resolveCallerAgent(root, agentName string) (config.AgentEntry, error) {
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return config.AgentEntry{}, err
	}
	entry, ok := agentsCfg.Agents[agentName]
	if !ok {
		return config.AgentEntry{}, fmt.Errorf("agent %q not found", agentName)
	}
	return entry, nil
}

// runFormulaInstantiation is the original formula-based sling path.
// It constructs InstantiateParams from package globals and delegates
// to instantiateFormulaWorkflow. The launch decision stays here.
func runFormulaInstantiation(cmd *cobra.Command, root, wd string, args []string) error {
	if slingReset {
		cleanupPriorRuntime(wd)
		os.Remove(filepath.Join(wd, ".runtime", "hooked_formula"))
	}

	params := InstantiateParams{
		Ctx:         cmd.Context(),
		FormulaName: slingFormulaName,
		CLIVars:     slingVars,
		AgentName:   slingAgent,
		Root:        root,
		WorkDir:     wd,
	}

	_, _, agentName, err := instantiateFormulaWorkflow(params, cmd.OutOrStdout())
	if err != nil {
		return err
	}

	// Launch agent session (unless --no-launch)
	if slingNoLaunch {
		return nil
	}

	if agentName == "" {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: no agent specified and could not detect role, skipping session launch\n")
		return nil
	}

	fmFactoryCfg, fmFcErr := config.LoadFactoryConfig(config.FactoryConfigPath(root))
	if fmFcErr != nil {
		return fmt.Errorf("loading factory config: %w", fmFcErr)
	}

	envWT := os.Getenv("AF_WORKTREE")
	envWTID := os.Getenv("AF_WORKTREE_ID")
	creator, _ := resolveAgentName(wd, root)
	worktreePath, worktreeID, wtCreated, wtErr := worktree.ResolveOrCreate(root, agentName, creator, envWT, envWTID, worktree.CreateOpts{MaxWorktrees: fmFactoryCfg.MaxWorktrees})
	if wtErr != nil {
		return fmt.Errorf("worktree creation failed for %s: %w", agentName, wtErr)
	}
	if worktreePath != "" {
		if _, setupErr := worktree.SetupAgent(root, worktreePath, agentName, wtCreated); setupErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree SetupAgent: %v\n", setupErr)
			return fmt.Errorf("worktree setup failed for %s: %w", agentName, setupErr)
		}
		if wtCreated {
			fmt.Fprintf(cmd.OutOrStdout(), "Created worktree %s for %s\n", worktreeID, agentName)
		}
	}

	if err := launchAgentSession(cmd, root, agentName, worktreePath, worktreeID); err != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "warning: session launch failed: %v\n", err)
		// Non-fatal — beads were created successfully
	}

	return nil
}

// instantiateFormulaWorkflow performs the full formula instantiation pipeline:
// formula discovery, parsing, variable resolution, bead creation, DAG wiring,
// and runtime file persistence. It does not read package globals.
// Callers must populate params.Ctx (both production call sites pass
// cmd.Context(); tests should pass t.Context()).
func instantiateFormulaWorkflow(params InstantiateParams, w io.Writer) (string, map[string]string, string, error) {
	ctx := params.Ctx

	// 1. Find formula file
	formulaPath, err := formula.FindFormulaFile(params.FormulaName, params.WorkDir)
	if err != nil {
		return "", nil, "", fmt.Errorf("finding formula: %w", err)
	}

	// 2. Parse and validate (Parse calls Validate internally)
	f, err := formula.ParseFile(formulaPath)
	if err != nil {
		return "", nil, "", fmt.Errorf("parsing formula %s: %w", formulaPath, err)
	}

	// 2.1 Validate declared agents against agents.json (AC-3 from GH#122).
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(params.Root))
	if err != nil {
		return "", nil, "", fmt.Errorf("loading agents config: %w", err)
	}
	if err := f.ValidateAgents(agentsCfg); err != nil {
		return "", nil, "", fmt.Errorf("validating formula agents: %w", err)
	}

	// 3. Resolve variables
	cliVars, err := parseCLIVars(params.CLIVars)
	if err != nil {
		return "", nil, "", err
	}

	// 3.1. Detect caller identity early (before variable resolution)
	// so {{orchestrator}} can be injected into resolvedVars for expandStepVars.
	callerIdentity := params.CallerIdentity
	if callerIdentity == "" {
		callerIdentity, _, _ = detectRole(params.WorkDir, params.Root)
		if callerIdentity == "" {
			callerIdentity = "@cli"
		}
	}

	agentName := params.AgentName
	if agentName == "" {
		agentName, _ = detectAgentName(params.WorkDir, params.Root)
	}
	if agentName == "" {
		return "", nil, "", fmt.Errorf("--agent is required: formula step Assignee cannot be resolved without it")
	}

	priorInstanceID := readHookedFormulaID(params.WorkDir)
	if priorInstanceID != "" {
		return "", nil, agentName, fmt.Errorf(
			"prior formula %s is still active; use --reset to clean runtime state and re-sling",
			priorInstanceID,
		)
	}

	actor := os.Getenv("AF_ACTOR")
	store, err := newIssueStore(params.WorkDir, actor)
	if err != nil {
		return "", nil, agentName, fmt.Errorf("initializing issue store: %w", err)
	}

	// Auto-create assignment bead for specialist dispatch when no issue provided
	if params.TaskDescription != "" && cliVars["issue"] == "" {
		iss, err := store.Create(ctx, issuestore.CreateParams{
			Type:        issuestore.TypeTask,
			Title:       assignmentTitle(params.TaskDescription),
			Description: params.TaskDescription,
			Labels:      []string{"assignment"},
		})
		if err != nil {
			return "", nil, agentName, fmt.Errorf("creating assignment bead: %w", err)
		}
		cliVars["issue"] = iss.ID
		fmt.Fprintf(w, "Created assignment bead: %s\n", iss.ID)
	}

	resolveCtx := formula.ResolveContext{CLIArgs: cliVars, EnvLookup: os.Getenv}

	if formulaUsesBeadSources(f.Vars) && agentName != "" {
		beadID := readHookedFormulaID(params.WorkDir)
		if beadID != "" {
			iss, err := store.Get(ctx, beadID)
			if err != nil {
				fmt.Fprintf(w, "warning: failed to resolve bead metadata for %s: %v\n", beadID, err)
			} else {
				resolveCtx.HookedBeadID = beadID
				resolveCtx.BeadTitle = iss.Title
				resolveCtx.BeadDescription = iss.Description
				fmt.Fprintf(w, "Resolved bead metadata from hooked bead %s\n", beadID)
			}
		}
	}

	// 3.2. Merge inputs to vars (workflow formulas only)
	mergedVars := f.Vars
	if f.Type == formula.TypeWorkflow {
		mergedVars, err = formula.MergeInputsToVars(f.Inputs, f.Vars)
		if err != nil {
			return "", nil, agentName, fmt.Errorf("merging inputs to vars: %w", err)
		}
	}

	// Bridge positional text to unsatisfied required inputs (specialist dispatch only).
	if params.TaskDescription != "" && f.Type == formula.TypeWorkflow && len(f.Inputs) > 0 {
		unsatisfied := findUnsatisfiedRequiredInputs(f.Inputs, cliVars)
		if len(unsatisfied) == 1 {
			cliVars[unsatisfied[0]] = params.TaskDescription
		} else if len(unsatisfied) > 1 {
			return "", nil, agentName, fmt.Errorf(
				"formula %q has %d required inputs not provided: %s; "+
					"provide all but one via --var flags, the remaining one "+
					"receives the positional text argument",
				f.Name, len(unsatisfied), strings.Join(unsatisfied, ", "))
		}
	}

	resolvedVars, err := formula.ResolveVars(mergedVars, resolveCtx)
	if err != nil {
		return "", nil, agentName, fmt.Errorf("resolving variables: %w", err)
	}

	// 3.3. Inject orchestrator into resolved vars (after resolve, before expand)
	if callerIdentity != "" {
		resolvedVars["orchestrator"] = callerIdentity
	}

	// 4. Topological sort for execution order
	sortedIDs, err := f.TopologicalSort()
	if err != nil {
		return "", nil, agentName, fmt.Errorf("sorting steps: %w", err)
	}

	// 5. Expand variables in step descriptions
	expandStepVars(f, resolvedVars)

	// 5.5. Embed dispatch task in parent bead description (specialist dispatch)
	if params.TaskDescription != "" {
		f.Description = f.Description + "\n\n---\nDispatch task: " + params.TaskDescription
	}

	// 6. Create beads
	instanceID, stepIDs, err := instantiateFormula(ctx, store, f, sortedIDs, agentName)
	if err != nil {
		return "", nil, agentName, err
	}

	// 7. Add DAG dependencies
	if err := addStepDependencies(ctx, store, f, stepIDs); err != nil {
		return instanceID, stepIDs, agentName, err
	}

	fmt.Fprintf(w, "Formula %q instantiated: %s (%d steps)\n", f.Name, instanceID, len(stepIDs))
	for _, id := range sortedIDs {
		beadID := stepIDs[id]
		fmt.Fprintf(w, "  %s → %s\n", id, beadID)
	}

	// 7.5. Persist formula instance ID for af prime
	if agentName != "" {
		persistFormulaInstanceID(params.WorkDir, instanceID)
	}

	// 7.6. Persist caller identity for af done WORK_DONE mail
	if agentName != "" {
		ensureCallerIdentity(params.WorkDir, params.Root, params.WorkDir, w)
	}

	return instanceID, stepIDs, agentName, nil
}

// parseCLIVars parses --var key=val flags into a map.
func parseCLIVars(vars []string) (map[string]string, error) {
	result := make(map[string]string, len(vars))
	for _, v := range vars {
		parts := strings.SplitN(v, "=", 2)
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid --var format %q: expected key=val", v)
		}
		result[parts[0]] = parts[1]
	}
	return result, nil
}

// expandStepVars applies variable substitution to all step/leg/template descriptions.
func expandStepVars(f *formula.Formula, vars map[string]string) {
	for i := range f.Steps {
		f.Steps[i].Description = formula.ExpandTemplateVars(f.Steps[i].Description, vars)
		f.Steps[i].Title = formula.ExpandTemplateVars(f.Steps[i].Title, vars)
	}
	for i := range f.Legs {
		f.Legs[i].Description = formula.ExpandTemplateVars(f.Legs[i].Description, vars)
		f.Legs[i].Title = formula.ExpandTemplateVars(f.Legs[i].Title, vars)
	}
	for i := range f.Template {
		f.Template[i].Description = formula.ExpandTemplateVars(f.Template[i].Description, vars)
		f.Template[i].Title = formula.ExpandTemplateVars(f.Template[i].Title, vars)
	}
}

// instantiateFormula creates the parent bead and step beads via the issue store.
// Returns the instance bead ID and a map of step ID → bead ID.
func instantiateFormula(ctx context.Context, store issuestore.Store, f *formula.Formula, sortedIDs []string, slingAgent string) (string, map[string]string, error) {
	// Create parent formula instance bead
	parent, err := store.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeEpic,
		Title:       fmt.Sprintf("Formula: %s", f.Name),
		Description: f.Description,
		Labels:      []string{"formula-instance"},
		Assignee:    slingAgent,
	})
	if err != nil {
		return "", nil, fmt.Errorf("creating formula instance bead: %w", err)
	}
	instanceID := parent.ID

	// Create step beads as children
	stepIDs := make(map[string]string, len(sortedIDs))
	for _, stepID := range sortedIDs {
		title, desc := stepInfo(f, stepID)

		step, err := store.Create(ctx, issuestore.CreateParams{
			Type:        issuestore.TypeTask,
			Parent:      instanceID,
			Title:       fmt.Sprintf("Step: %s", title),
			Description: desc,
			Assignee:    assigneeForStep(f, stepID, slingAgent),
			Labels:      []string{"formula-step", fmt.Sprintf("step-id:%s", stepID)},
		})
		if err != nil {
			return instanceID, stepIDs, fmt.Errorf("creating step bead for %q: %w", stepID, err)
		}
		stepIDs[stepID] = step.ID
	}

	return instanceID, stepIDs, nil
}

// assigneeForStep resolves the Assignee for a step bead. Prefers the
// formula's declared agent (step → leg → template → formula); falls back to
// the CLI-resolved slingAgent. Panics if both are empty — UX-2 in
// instantiateFormulaWorkflow ensures slingAgent is non-empty before this
// runs, so the panic is a defense-in-depth assertion.
func assigneeForStep(f *formula.Formula, stepID, slingAgent string) string {
	if a := f.AgentFor(stepID); a != "" {
		return a
	}
	if slingAgent == "" {
		panic("assigneeForStep: slingAgent empty — UX-2 CLI validation missed?")
	}
	return slingAgent
}

// stepInfo returns the title and description for a step/leg/template/aspect by ID.
func stepInfo(f *formula.Formula, id string) (string, string) {
	if s := f.GetStep(id); s != nil {
		return s.Title, s.Description
	}
	if l := f.GetLeg(id); l != nil {
		return l.Title, l.Description
	}
	if t := f.GetTemplate(id); t != nil {
		return t.Title, t.Description
	}
	if a := f.GetAspect(id); a != nil {
		return a.Title, a.Description
	}
	return id, ""
}

// addStepDependencies registers the DAG edges for each step's needs via the issue store.
func addStepDependencies(ctx context.Context, store issuestore.Store, f *formula.Formula, stepIDs map[string]string) error {
	allIDs := f.GetAllIDs()
	for _, id := range allIDs {
		deps := f.GetDependencies(id)
		beadID, ok := stepIDs[id]
		if !ok {
			continue
		}
		for _, depID := range deps {
			depBeadID, ok := stepIDs[depID]
			if !ok {
				return fmt.Errorf("dependency %q for step %q has no bead ID", depID, id)
			}
			// beadID depends on depBeadID
			if err := store.DepAdd(ctx, beadID, depBeadID); err != nil {
				return fmt.Errorf("adding dependency %s→%s: %w", id, depID, err)
			}
		}
	}
	return nil
}

// persistFormulaInstanceID writes the formula instance bead ID to
// <agentDir>/.runtime/hooked_formula so af prime can find it.
func persistFormulaInstanceID(agentDir, instanceID string) {
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte(instanceID), 0o644)
}

// assignmentTitle derives a bead title from a task string.
// Uses the first line, truncated to 80 characters.
func assignmentTitle(task string) string {
	line, _, _ := strings.Cut(task, "\n")
	if len(line) > 80 {
		return line[:77] + "..."
	}
	return line
}

// ensureCallerIdentity resolves the caller role and persists it. If the caller
// cannot be detected (e.g., dispatching from the factory root), it writes a
// synthetic "@cli" value and emits a warning so WORK_DONE mail is not silently lost.
func ensureCallerIdentity(callerWd, root, agentDir string, w io.Writer) string {
	callerRole, _, _ := detectRole(callerWd, root)
	if callerRole == "" {
		callerRole = "@cli"
		fmt.Fprintf(w, "warning: dispatching from outside an agent directory; WORK_DONE mail will be sent to @cli (use --caller to set an explicit recipient)\n")
	}
	persistFormulaCaller(agentDir, callerRole)
	return callerRole
}

// persistFormulaCaller writes the caller identity to <agentDir>/.runtime/formula_caller
// so that af done knows who to mail WORK_DONE to.
// If the file already exists, it is NOT overwritten — the original dispatcher
// identity is preserved across formula re-instantiation and step transitions.
//
// H-4 / D15 atomic-write ordering invariant: this function is called from
// dispatchToSpecialist (sling.go L156/L158) BEFORE the formula bead is created
// via instantiateFormulaWorkflow. If the process crashes between writing the
// caller file and creating the bead, the next dispatch can proceed without a
// stale bead hanging around — and conversely, af done will never observe a
// formula bead without a corresponding caller file. Pinned by
// done_test.go::TestDone_NoCallerFile_NoMail (the converse case: a missing
// caller file means no dispatcher is waiting, so WORK_DONE mail is skipped).
//
// Note: instantiateFormulaWorkflow (the non-dispatch path) also calls
// ensureCallerIdentity AFTER bead creation. That ordering is acceptable
// because non-dispatch sessions are not waited on by a parent — H-4 only
// constrains the dispatch path (peer-review Gap #3).
func persistFormulaCaller(agentDir, caller string) {
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	callerPath := filepath.Join(runtimeDir, "formula_caller")
	if _, err := os.Stat(callerPath); err == nil {
		return // preserve existing caller identity
	}
	os.WriteFile(callerPath, []byte(caller), 0o644)
}

// detectAgentName tries to detect the current agent name from the working directory.
// Delegates to resolveAgentName (helpers.go) for three-tier resolution.
func detectAgentName(wd, root string) (string, error) {
	return resolveAgentName(wd, root)
}

// launchAgentSession starts the agent in a tmux session.
//
// Declared as a package-level var so tests can substitute a no-op
// implementation (see installNoopLaunchSession in sling_test.go). The
// real path blocks in tmux.WaitForCommand until claude writes a readiness
// sentinel; on CI where claude is absent, that never happens and the
// test suite hits its global timeout. The seam mirrors newIssueStore in
// helpers.go and lets dispatch-path tests exercise the full pipeline
// without depending on tmux + claude being present on PATH.
var launchAgentSession = func(cmd *cobra.Command, root, agentName, worktreePath, worktreeID string) error {
	t := tmux.NewTmux()
	if !t.IsAvailable() {
		return fmt.Errorf("tmux is not installed or not available")
	}

	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	entry, ok := agentsCfg.Agents[agentName]
	if !ok {
		return fmt.Errorf("agent %q not found in agents.json", agentName)
	}

	mgr := session.NewManager(root, agentName, entry)
	if worktreePath != "" {
		if err := mgr.SetWorktree(worktreePath, worktreeID); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: SetWorktree for %s: %v\n", agentName, err)
			return err
		}
	}
	if err := mgr.Start(); err != nil {
		if err == session.ErrAlreadyRunning {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: already running\n", session.SessionName(agentName))
			return nil
		}
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Launched %s\n", session.SessionName(agentName))
	return nil
}

// formulaUsesBeadSources returns true if any variable in the formula uses a bead-based source.
func formulaUsesBeadSources(vars map[string]formula.Var) bool {
	for _, v := range vars {
		switch v.Source {
		case "hook_bead", "bead_title", "bead_description":
			return true
		}
	}
	return false
}

// findUnsatisfiedRequiredInputs returns the names of required inputs that have no
// default and are not yet present in cliVars. RequiredUnless is evaluated against
// cliVars only, not ENV-resolved variables — this is a known design boundary.
func findUnsatisfiedRequiredInputs(inputs map[string]formula.Input, cliVars map[string]string) []string {
	var unsatisfied []string
	for name, inp := range inputs {
		if !inp.Required || inp.Default != "" {
			continue
		}
		if _, ok := cliVars[name]; ok {
			continue
		}
		skip := false
		for _, unless := range inp.RequiredUnless {
			if _, ok := cliVars[unless]; ok {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		unsatisfied = append(unsatisfied, name)
	}
	sort.Strings(unsatisfied)
	return unsatisfied
}

func cleanupPriorRuntime(workDir string) {
	runtimeDir := filepath.Join(workDir, ".runtime")
	os.Remove(filepath.Join(runtimeDir, "formula_caller"))
	os.Remove(filepath.Join(runtimeDir, "dispatched"))
	resetCheckpointFormulaID(workDir)
}

func resetCheckpointFormulaID(workDir string) {
	cp, err := checkpoint.Read(workDir)
	if err != nil || cp == nil {
		return
	}
	cp.FormulaID = ""
	_ = checkpoint.Write(workDir, cp)
}

// writeDispatchedMarker writes the dispatch marker to <agentDir>/.runtime/dispatched.
// The content is the caller identity (dispatcher's agent name or "@cli") for debugging.
// Only the file's existence matters functionally — Phase 2 reads it via isDispatchedSession().
// Each dispatch writes a fresh marker unconditionally (no no-overwrite semantics).
func writeDispatchedMarker(agentDir, callerIdentity string) {
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte(callerIdentity), 0o644)
}
