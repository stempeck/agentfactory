package cmd

import (
	"context"
	"encoding/json"
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
	"github.com/stempeck/agentfactory/internal/mail"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/templates"
	"github.com/stempeck/agentfactory/internal/tmux"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// fallbackCaller is the routable default recipient synthesized when no
// dispatching agent can be resolved. Must be a real, seeded mailbox so that
// {{orchestrator}} step signals and WORK_DONE mail are deliverable (unlike the
// former "@cli" sentinel, which the mail router rejects as an unknown @group).
const fallbackCaller = "manager"

var (
	slingFormulaName string
	slingVars        []string
	slingAgent       string
	slingNoLaunch    bool
	slingReset       bool
	slingCaller      string
	slingPersistent  bool
	slingModel       string
	slingSkipFitness bool
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
	// StringArrayVar (not StringSliceVar): each --var is taken verbatim as one element,
	// so a comma in a value (e.g. --var pr_uri=a,b) is preserved instead of being
	// CSV-split into a malformed second var. --var is repeatable for multiple variables.
	slingCmd.Flags().StringArrayVar(&slingVars, "var", nil, "Variable in key=val format (repeatable)")
	slingCmd.Flags().StringVar(&slingAgent, "agent", "", "Agent to dispatch to or launch")
	slingCmd.Flags().BoolVar(&slingNoLaunch, "no-launch", false, "Create beads only, skip tmux session launch")
	slingCmd.Flags().BoolVar(&slingReset, "reset", false, "Force-reset target agent: close beads, remove worktree (even if dirty), clean all runtime state")
	slingCmd.Flags().StringVar(&slingCaller, "caller", "", "Explicit caller identity for WORK_DONE mail (used by dispatch)")
	slingCmd.Flags().MarkHidden("caller")
	slingCmd.Flags().BoolVar(&slingPersistent, "persistent", false, "Keep session alive after formula completion (do not auto-terminate). !IMPORTANT! ONLY used in formulas instructions, not ad-hoc specialist dispatch. Use with caution: the session will not auto-terminate on formula completion.")
	slingCmd.Flags().StringVar(&slingModel, "model", "", "Per-agent model profile (or raw model id) from models.json — overrides the per-agent default for this launch")
	slingCmd.Flags().BoolVar(&slingSkipFitness, "skip-fitness", false, "Launch a non-loopback model profile without a fitness attestation (loud override; see `af config models attest`)")
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

	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "factory: %s\n", root)

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
		if err := mgr.Stop(); err != nil && err != session.ErrNotRunning {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: failed to stop %s: %v\n", agentName, err)
		}
		if err := resetAgentState(cmd.Context(), cmd.OutOrStdout(), root, agentName, config.CloseReasonResetSling); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: reset incomplete for %s: %v\n", agentName, err)
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

	worktreePath, worktreeID, outcome, wtErr := worktree.ResolveOrCreate(root, agentName, creator, envWT, envWTID, worktree.CreateOpts{MaxWorktrees: factoryCfg.MaxWorktrees})
	if wtErr != nil {
		return fmt.Errorf("worktree creation failed for %s: %w", agentName, wtErr)
	}
	if worktreePath != "" {
		wtAgentDir, setupErr := worktree.SetupAgent(root, worktreePath, agentName, outcome.IsCreated())
		if setupErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree SetupAgent: %v\n", setupErr)
		} else {
			agentDir = wtAgentDir
		}
		if outcome.IsCreated() {
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
		if callerIdentity != "" && callerIdentity != "@cli" {
			if store, storeErr := storeForMail(root); storeErr == nil {
				if router, routerErr := mail.NewRouter(root, store); routerErr == nil {
					msg := mail.NewMessage(agentName, callerIdentity, "SKILL_MISSING: "+agentName, fmt.Sprintf("Dispatch to %s failed: %v", agentName, err))
					_ = router.Send(cmd.Context(), msg)
				}
			}
		}
		return err
	}

	fmt.Fprintf(cmd.OutOrStdout(), "Dispatched to %s: %s\n", session.SessionName(agentName), task)

	// 4. Launch the agent session (unless --no-launch)
	if slingNoLaunch {
		return nil
	}

	return launchAgentSession(cmd, root, agentName, worktreePath, worktreeID, slingModel, slingSkipFitness)
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
		if slingAgent != "" {
			store, err := newIssueStore(root, slingAgent)
			if err != nil {
				fmt.Fprintf(cmd.ErrOrStderr(), "%s: warning: cannot initialize store for bead cleanup: %v\n", slingAgent, err)
			} else {
				closedCount := closeAgentBeads(cmd.Context(), store, slingAgent, config.CloseReasonResetFormulaSling)
				if closedCount > 0 {
					fmt.Fprintf(cmd.OutOrStdout(), "  %s: closed %d formula beads\n", slingAgent, closedCount)
				}
			}
		}
		os.RemoveAll(filepath.Join(wd, ".runtime"))
		checkpoint.Remove(wd)
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
	worktreePath, worktreeID, outcome, wtErr := worktree.ResolveOrCreate(root, agentName, creator, envWT, envWTID, worktree.CreateOpts{MaxWorktrees: fmFactoryCfg.MaxWorktrees})
	if wtErr != nil {
		return fmt.Errorf("worktree creation failed for %s: %w", agentName, wtErr)
	}
	if worktreePath != "" {
		if _, setupErr := worktree.SetupAgent(root, worktreePath, agentName, outcome.IsCreated()); setupErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "warning: worktree SetupAgent: %v\n", setupErr)
			return fmt.Errorf("worktree setup failed for %s: %w", agentName, setupErr)
		}
		if outcome.IsCreated() {
			fmt.Fprintf(cmd.OutOrStdout(), "Created worktree %s for %s\n", worktreeID, agentName)
		}
	}

	if err := launchAgentSession(cmd, root, agentName, worktreePath, worktreeID, slingModel, slingSkipFitness); err != nil {
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

	// 1. Find formula file — pass the already-validated root (thread 7a); never cwd.
	formulaPath, err := formula.FindFormulaFile(params.FormulaName, params.Root)
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

	// 2.2 Validate declared skills are available (AC-1, AC-2 from GH#241).
	skillsDir := filepath.Join(params.Root, ".claude", "skills")
	if err := f.ValidateSkills(skillsDir); err != nil {
		return "", nil, "", fmt.Errorf("validating formula skills: %w", err)
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
			callerIdentity = fallbackCaller
			if _, ok := agentsCfg.Agents[fallbackCaller]; !ok {
				fmt.Fprintf(w, "warning: fallback caller %q is not present in agents.json; WORK_DONE/orchestrator mail may be undeliverable (run `af install --init` to re-seed it)\n", fallbackCaller)
			}
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

	// 3.4. Inject detected default branch (after resolve, before expand) — A1 ambient token.
	// {{default_branch}} is NOT a declared formula var, so ResolveVars never applied a
	// --var override to it; honor the override by reading cliVars directly.
	if v, ok := cliVars["default_branch"]; ok {
		resolvedVars["default_branch"] = v // operator override wins (A4)
	} else if db := detectDefaultBranch(params.WorkDir); db != "" {
		resolvedVars["default_branch"] = db
		fmt.Fprintf(w, "Default branch: %s\n", db) // K9 success echo (U2)
	} else {
		// K9 fail-loud: visible warning, do NOT silently bake "main".
		fmt.Fprintf(w, "warning: could not detect repository default branch (origin/HEAD unset, no GitHub remote); re-run with --var default_branch=<name>\n")
		resolvedVars["default_branch"] = "main" // last-resort sentinel, surfaced by the warning above
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
	instanceID, stepIDs, err := instantiateFormula(ctx, store, f, sortedIDs, agentName, params.Root)
	if err != nil {
		return "", nil, agentName, err
	}

	// 6.1. Persist the resolved variables on a dedicated metadata child bead so
	// an external read model (`af agents list --json`) can surface "what inputs
	// was this agent given" (AC-1(ii) / H-2). This is strictly additive and
	// FAIL-OPEN: it runs on the AC-8 hot path (every interactive AND dispatched
	// run reaches here), so persisting inputs — a read-model convenience — must
	// never break instantiation. Any failure is logged and swallowed (H-P2).
	persistResolvedVars(ctx, store, instanceID, agentName, resolvedVars, w)

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
// persistResolvedVars writes the resolved formula variables as JSON onto a
// dedicated metadata bead so machine consumers of `af agents list --json` can
// report the inputs an agent was slung with (H-2). The carrier:
//   - is keyed to the instance by the unique label resolvedVarsInstanceLabel
//     (NOT by Parent: instanceID). Keying via a label rather than parentage keeps
//     the carrier out of the formula's step DAG: a child bead would inflate
//     Ready.TotalSteps (corrupting af prime's "Step X of N" on every run) and
//     could be reported as the active step by `af step current`. This preserves
//     GAP-1's intent — a dedicated, instance-keyed, queryable metadata bead — and
//     mirrors the existing orphan, labeled "assignment" bead above;
//   - holds the map in Description, NOT Notes — Notes is a whole-field overwrite
//     target (`af bead update`, GAP-1) and would be silently clobbered;
//   - is closed immediately so it never lingers in open-work listings.
//
// It is entirely FAIL-OPEN (H-P2): marshal failure, store.Create failure, a
// store.Close failure, or a panic are caught, logged to w, and swallowed — the
// caller's instantiation result is identical to the pre-feature behavior. This
// is mandatory because the function runs for every interactive `af sling` AND
// every autonomous `af dispatch` auto-sling (the AC-8 success path).
func persistResolvedVars(ctx context.Context, store issuestore.Store, instanceID, assignee string, vars map[string]string, w io.Writer) {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(w, "warning: resolved_vars persistence panicked (ignored): %v\n", r)
		}
	}()

	data, err := json.Marshal(vars)
	if err != nil {
		fmt.Fprintf(w, "warning: could not marshal resolved_vars (ignored): %v\n", err)
		return
	}

	carrier, err := store.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeTask,
		Title:       "resolved-vars",
		Description: string(data),
		Assignee:    assignee,
		Labels:      []string{resolvedVarsLabel, resolvedVarsInstanceLabel(instanceID)},
	})
	if err != nil {
		fmt.Fprintf(w, "warning: could not persist resolved_vars (ignored): %v\n", err)
		return
	}

	if err := store.Close(ctx, carrier.ID, "resolved_vars metadata carrier"); err != nil {
		// Non-fatal: an open carrier is still readable (agents.go reads with
		// IncludeClosed) and, being label-keyed not Parent-keyed, never pollutes
		// the formula DAG even while open.
		fmt.Fprintf(w, "warning: could not close resolved_vars carrier (ignored): %v\n", err)
	}
}

func instantiateFormula(ctx context.Context, store issuestore.Store, f *formula.Formula, sortedIDs []string, slingAgent, root string) (string, map[string]string, error) {
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
// cannot be detected (e.g., dispatching from the factory root), it falls back to
// the routable fallbackCaller mailbox and emits a warning so WORK_DONE mail is
// delivered to a real recipient rather than silently lost.
func ensureCallerIdentity(callerWd, root, agentDir string, w io.Writer) string {
	callerRole, _, _ := detectRole(callerWd, root)
	if callerRole == "" {
		callerRole = fallbackCaller
		fmt.Fprintf(w, "warning: no resolvable dispatching agent; defaulting WORK_DONE/orchestrator recipient to %s (use --caller to set an explicit recipient)\n", fallbackCaller)
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
var launchAgentSession = func(cmd *cobra.Command, root, agentName, worktreePath, worktreeID, cliModel string, skipFitness bool) error {
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
	wireGitIdentity(mgr, root, worktreePath)

	// Resolve the per-agent model export set (issue #480) and fail fast BEFORE
	// mgr.Start() — an unknown profile or incomplete endpoint must never launch a
	// half-configured tmux session. The marker dir matches where a respawn
	// will read it (worktree agent dir when a worktree exists).
	agentDir := config.AgentDir(root, agentName)
	if worktreePath != "" {
		agentDir = config.AgentDir(worktreePath, agentName)
	}
	modelName, modelEnv, modelErr := resolveLaunchModelEnv(root, agentName, agentDir, cliModel, entry.Model, skipFitness, cmd.ErrOrStderr())
	if modelErr != nil {
		return modelErr
	}
	if len(modelEnv) > 0 {
		mgr.SetModelEnv(modelEnv)
	}
	// Persist ONLY an explicit per-launch --model override (precedence step 2); a
	// durable agents-map/agents.json default writes no marker (it resolves durably).
	if cliModel != "" && modelName != "" {
		writeModelOverride(agentDir, modelName)
	}

	if err := mgr.Start(); err != nil {
		if err == session.ErrAlreadyRunning {
			fmt.Fprintf(cmd.OutOrStdout(), "%s: already running\n", session.SessionName(agentName))
			return nil
		}
		return err
	}

	var parts []string
	displayModel := entry.Model
	if modelName != "" {
		displayModel = modelName
	}
	if displayModel != "" {
		parts = append(parts, "model: "+displayModel)
	}
	// Endpoint echo from the resolved set (names only, never auth_token), falling
	// back to the legacy field; empty when neither applies.
	endpoint := entry.BaseURL
	if u := modelEnvValue(modelEnv, "ANTHROPIC_BASE_URL"); u != "" {
		endpoint = u
	}
	if endpoint != "" {
		parts = append(parts, "endpoint: "+endpoint)
	}
	if len(parts) > 0 {
		fmt.Fprintf(cmd.OutOrStdout(), "Launched %s (%s)\n", session.SessionName(agentName), strings.Join(parts, ", "))
	} else {
		fmt.Fprintf(cmd.OutOrStdout(), "Launched %s\n", session.SessionName(agentName))
	}
	return nil
}

// resolveLaunchModelEnv resolves the per-agent model-env export set for a launch
// (issue #480). It loads models.json, reads the .runtime/model_override marker
// (precedence step 2), applies the unknown-profile / incomplete-endpoint fail-fast,
// and runs the pure config.ResolveModelEnv. It is the single shared resolver for
// every launch path (sling launch and respawn; af up in Phase 4) so fail-fast and
// precedence stay uniform.
//
// A non-empty cliModel makes the launch "profile-selecting": a broken models.json or
// a bad selection fails loud (a structured error the caller must return BEFORE
// mgr.Start()). A launch that selects no profile (cliModel == "", e.g. every respawn)
// treats a load/resolve error as non-fatal — it warns and falls through to the global
// default (empty set) so one bad models.json cannot brick default-model agents.
//
// The marker is read here in the cmd layer and passed INTO the pure resolver; the
// resolver never reads a file or the environment (ADR-004).
func resolveLaunchModelEnv(root, agentName, agentDir, cliModel, legacyModel string, skipFitness bool, warn io.Writer) (string, []config.EnvVar, error) {
	profileSelecting := cliModel != ""

	cfg, err := config.LoadModelsConfig(root)
	if err != nil {
		if profileSelecting {
			return "", nil, fmt.Errorf("cannot select model %q: %w", cliModel, err)
		}
		fmt.Fprintf(warn, "warning: ignoring models.json (%v); using the global default model\n", err)
		return "", nil, nil
	}

	// Unknown-profile fail-fast: an explicit --model naming something not in a populated
	// registry is a typo, not a raw-id passthrough — the pure resolver would silently
	// pass it through (models.go raw-id branch). A raw model id with NO registry stays a
	// passthrough (`--model claude-opus-4-8` works with no models.json).
	if profileSelecting && len(cfg.Models) > 0 {
		if _, ok := cfg.Models[cliModel]; !ok {
			return "", nil, fmt.Errorf("unknown model profile %q: not defined in models.json (defined: %s)", cliModel, knownProfiles(cfg))
		}
	}

	marker := readModelOverride(agentDir)
	name, env, ok, err := config.ResolveModelEnv(cfg, agentName, cliModel, marker, legacyModel)
	if err != nil {
		if profileSelecting {
			return "", nil, fmt.Errorf("model %q: %w", name, err)
		}
		fmt.Fprintf(warn, "warning: ignoring models.json model %q (%v); using the global default model\n", name, err)
		return "", nil, nil
	}
	if !ok {
		return "", nil, nil
	}

	// Launch preflight (issue #508): a resolved ANTHROPIC_AUTH_TOKEN carrying a
	// file: secret reference must point at a real, non-empty file BEFORE mgr.Start(). The
	// cmd layer may do this I/O (ADR-004 confines only the library). A selecting launch
	// fails fast naming profile + path; a respawn (cliModel "") warns naming the abandoned
	// model and falls through to the global default so one missing secret can never brick a
	// default-model agent (the 43052536 posture). The path resolves like Phase-2's emission
	// deref: relative to the factory root, absolute as-is.
	if tok := modelEnvValue(env, "ANTHROPIC_AUTH_TOKEN"); strings.HasPrefix(tok, "file:") {
		path := secretRefPath(root, tok)
		if info, statErr := os.Stat(path); statErr != nil || info.IsDir() || info.Size() == 0 {
			if profileSelecting {
				return "", nil, fmt.Errorf("model %q: secret reference ANTHROPIC_AUTH_TOKEN → %s: file not found", name, path)
			}
			fmt.Fprintf(warn, "warning: agent %s: model %q secret %s missing; falling back to %s\n", agentName, name, path, globalDefaultDesc(legacyModel))
			return "", nil, nil
		}
	}

	// Fitness attestation interlock (issue #508): a selecting launch of a profile
	// with a NON-loopback endpoint requires a recorded fitness attestation (af config models
	// attest) unless --skip-fitness is passed. Loopback profiles are exempt, a profile with
	// no endpoint (a plain model-id passthrough) is not gated at all, and a respawn
	// (non-selecting) never reaches the refuse branch — so a routine handoff can never brick.
	if endpoint := modelEnvValue(env, "ANTHROPIC_BASE_URL"); profileSelecting && endpoint != "" && !config.IsLoopbackEndpoint(endpoint) {
		if skipFitness {
			fmt.Fprintf(warn, "warning: --skip-fitness: launching model %q on non-loopback endpoint %s without a fitness attestation (operator override)\n", name, endpoint)
		} else if !hasFitnessAttestation(root, name) {
			return "", nil, fmt.Errorf("model %q targets a non-loopback endpoint with no fitness attestation; run `af config models attest %s` after verifying it, or pass --skip-fitness to override", name, name)
		}
	}

	return name, env, nil
}

// globalDefaultDesc names the fallback target for a non-selecting launch whose selected
// profile's secret is missing: the agent's legacy default model when it has one, else the
// implicit global default. Used to name both the abandoned and the fallback model in the
// respawn secret-missing warning.
func globalDefaultDesc(legacyModel string) string {
	if legacyModel != "" {
		return fmt.Sprintf("global default model %s", legacyModel)
	}
	return "global default model"
}

// knownProfiles returns the sorted, comma-joined profile names defined in cfg, so a
// fail-fast error can point the operator at the valid choices.
func knownProfiles(cfg *config.ModelsConfig) string {
	names := make([]string, 0, len(cfg.Models))
	for n := range cfg.Models {
		names = append(names, n)
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

// modelEnvValue returns the value of the first export with the given key, or "".
func modelEnvValue(env []config.EnvVar, key string) string {
	for _, e := range env {
		if e.Key == key {
			return e.Value
		}
	}
	return ""
}

// writeModelOverride persists the resolved per-launch model override to
// <agentDir>/.runtime/model_override (issue #480). Written unconditionally
// (mirrors writeDispatchedMarker) so a fresh --model overwrites a stale marker; the
// existing --reset .runtime wipe cleans it. Only the per-launch --model override is
// persisted here — never an agents.json/models.json.agents default (those resolve
// durably without a marker, so a one-off override never becomes sticky).
func writeModelOverride(agentDir, name string) {
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "model_override"), []byte(name), 0o644)
}

// readModelOverride reads the per-launch model override marker from
// <workDir>/.runtime/model_override (mirrors readHookedFormulaID). Returns "" when
// absent; the value is passed into the pure resolver as the precedence-step-2 marker.
func readModelOverride(workDir string) string {
	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "model_override"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
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

// writeDispatchedMarker writes the dispatch marker to <agentDir>/.runtime/dispatched.
// The content is the caller identity (dispatcher's agent name or the fallbackCaller default) for debugging.
// Only the file's existence matters functionally — Phase 2 reads it via isDispatchedSession().
// Each dispatch writes a fresh marker unconditionally (no no-overwrite semantics).
func writeDispatchedMarker(agentDir, callerIdentity string) {
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte(callerIdentity), 0o644)
}
