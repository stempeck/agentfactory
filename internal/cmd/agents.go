package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/statusline"
)

// resolvedVarsLabel is the generic discriminating label on the dedicated bead
// that carries an agent's resolved formula variables (written at sling time by
// instantiateFormulaWorkflow). It is shared by the writer (sling.go) and the
// reader (here) so the two never drift.
const resolvedVarsLabel = "resolved-vars"

// resolvedVarsInstanceLabel returns the unique label that keys a resolved_vars
// carrier bead to its formula instance. The carrier is keyed by THIS label rather
// than by Parent: instanceID deliberately — making it a formula-instance child
// would entangle it in the formula's step DAG, inflating Ready.TotalSteps (and so
// af prime's "Step X of N" on every slung formula) and risking `af step current`
// reporting the carrier as the active step. A label-keyed, non-child bead is
// fully queryable yet leaves the DAG pristine (GAP-1's intent: a dedicated,
// instance-keyed, queryable metadata bead — see persistResolvedVars in sling.go).
func resolvedVarsInstanceLabel(instanceID string) string {
	return resolvedVarsLabel + "-of:" + instanceID
}

var agentsCmd = &cobra.Command{
	Use:   "agents",
	Short: "Agent inspection commands",
}

var agentsListCmd = &cobra.Command{
	Use:   "list",
	Short: "List configured agents with live status as JSON",
	Long: `List every configured agent with its live status as a single JSON array.

Each element carries the agent's name, type, declared formula, whether its tmux
session is running, an honest status (a running session with no active formula is
"idle", never "working"), the current formula step, and the inputs it was slung
with. Intended for scripting and machine consumption — the field set is a
versioned contract pinned by TestAgentsList_JSON_SchemaSnapshot.

Like 'af step current --json', this command always exits 0; infrastructure
failures are encoded as {"state":"error","error":"..."} so callers branch on the
output shape rather than the exit code.`,
	RunE: runAgentsList,
}

func init() {
	agentsListCmd.Flags().Bool("json", true, "Emit JSON output (currently the only supported format)")
	agentsCmd.AddCommand(agentsListCmd)
	rootCmd.AddCommand(agentsCmd)
}

// agentListItem is the JSON shape emitted per agent by `af agents list --json`.
// The field names are a versioned public contract pinned by
// TestAgentsList_JSON_SchemaSnapshot — do not rename without a coordinated
// rollout. gate_id intentionally OMITS omitempty (matching the step.go:62
// precedent) so the key set stays stable for jq/snapshot consumers; is_gate uses
// omitempty (a non-gate row emits one fewer key), so consumers must check
// gate_id != "" as well as is_gate == true.
type agentListItem struct {
	Name      string            `json:"name"`
	Type      string            `json:"type"`
	Formula   string            `json:"formula"`
	Running   bool              `json:"running"`
	Status    string            `json:"status"`
	StepID    string            `json:"step_id"`
	StepTitle string            `json:"step_title"`
	StepState string            `json:"step_state"`
	IsGate    bool              `json:"is_gate,omitempty"`
	GateID    string            `json:"gate_id"`
	Inputs    map[string]string `json:"inputs"`
	// ForeignRoot is true when a live session's baked AF_ROOT resolves to a factory
	// DIFFERENT from the querying factory (K9b, #519). Like gate_id it deliberately
	// OMITS omitempty so the key set stays stable for jq/snapshot consumers.
	ForeignRoot bool `json:"foreign_root"`
	// Occupancy and recovery visibility (K10-cli, #596). Like gate_id and foreign_root
	// these deliberately OMIT omitempty so the key set stays stable for jq/snapshot
	// consumers — and because a missing key here would read as "nothing to worry
	// about", which is the exact masking this phase exists to remove.
	//
	// ContextPct is -1 when there is no datum. It is NOT 0: the reader returns
	// (0, false) for a channel with nothing in it, and a 0 that reads as "empty
	// context" is the 0%-reads-healthy defect internal/statusline exists to prevent.
	// ContextState carries the reader's five-literal ChannelState verbatim
	// (fresh|stale|dark|none|malformed) — malformed is never collapsed into none.
	// Recovery is the K5 breaker's verdict: none|recovering|halted. None of the three
	// feeds deriveAgentStatus: the honesty enum keeps its three inputs.
	ContextPct   int    `json:"context_pct"`
	ContextState string `json:"context_state"`
	Recovery     string `json:"recovery"`
}

// runAgentsList is the RunE for `af agents list`. It enumerates agents.json and,
// for each agent, composes a live status row from tmux liveness + the shared
// issue store (the agent's active formula-instance bead, its current step, and
// the resolved_vars carrier). Always returns nil — infrastructure errors are
// reflected in an error envelope so callers branch on the output, not the exit
// code (mirrors runStepCurrent, step.go:90).
func runAgentsList(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	cwd, err := getWd()
	if err != nil {
		return emitAgentsError(cmd, err)
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		// A factory-root mismatch must not break the agents-list JSON-array contract:
		// downgrade it to a stderr warning and proceed on the cwd-resolved root. A
		// not-found error still goes into the error envelope (nil RunE) as before.
		if r, downgraded := downgradeRootMismatch(err); downgraded {
			root = r
		} else {
			return emitAgentsError(cmd, err)
		}
	}
	agentsCfg, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return emitAgentsError(cmd, err)
	}

	actor := os.Getenv("AF_ACTOR")
	// Build the store on the already-resolved (and possibly downgraded) root via
	// newIssueStoreAt — NOT newIssueStore(cwd), which would re-run resolveInvokerRoot
	// on the same cwd and re-raise the very mismatch just downgraded above, dropping
	// this read-only verb into its error envelope (issue #519 review follow-up).
	store, err := newIssueStoreAt(root, actor)
	if err != nil {
		return emitAgentsError(cmd, err)
	}
	tmux := newCmdTmux()

	names := make([]string, 0, len(agentsCfg.Agents))
	for name := range agentsCfg.Agents {
		names = append(names, name)
	}
	sort.Strings(names)

	// The occupancy sweep wants its roster before the row loop, but ForeignRoot — the one
	// exclusion axis an ADR-004-clean reader cannot observe for itself, so applying it is
	// explicitly the caller's job — is a per-row derivation. Resolve liveness and
	// foreign-rootness once here and let the loop consume the memoized values; the tmux call
	// count per row is unchanged.
	facts := make(map[string]sessionFacts, len(names))
	scope := make(map[string]struct{}, len(names))
	for _, name := range names {
		sessionName := session.SessionName(name)
		running, _ := tmux.HasSession(sessionName)
		f := sessionFacts{running: running}
		if running {
			f.foreignRoot = sessionForeignRoot(tmux, sessionName, root)
		}
		facts[name] = f
		// Coverage mirrors pollOccupancy's (recovery.go): agents.json ∩ live sessions, minus
		// foreign-root sessions. An agent with no session has no context to occupy, and
		// another factory's leftover snapshot is not this factory's occupancy.
		if f.running && !f.foreignRoot {
			scope[name] = struct{}{}
		}
	}
	readings := readAgentOccupancy(root, scope)

	items := make([]agentListItem, 0, len(names))
	for _, name := range names {
		entry := agentsCfg.Agents[name]
		f := facts[name]
		item := agentListItem{
			Name:        name,
			Type:        entry.Type,
			Formula:     entry.Formula,
			Running:     f.running,
			StepState:   "no_formula",
			Inputs:      map[string]string{},
			ForeignRoot: f.foreignRoot,
			// Unknown until a reading says otherwise. A row outside the sweep must reach
			// StateNone explicitly: the zero ChannelReading normalises to malformed (so a
			// value nobody produced can never pass for health), which would be a different
			// and wrong claim here.
			ContextPct:   -1,
			ContextState: string(statusline.StateNone),
			Recovery:     recoveryStatus(loadRecoveryState(root, name)),
		}
		if reading, ok := readings[name]; ok {
			item.ContextState = string(reading.State())
			// The bool is the whole point: a false with a 0 is "no reading", not "empty
			// context", and -1 is how this contract encodes that.
			if pct, has := reading.UsedPct(); has {
				item.ContextPct = int(math.Round(pct))
			}
		}
		populateAgentStep(ctx, store, name, &item)
		item.Status = deriveAgentStatus(f.running, item.StepState, item.IsGate)
		items = append(items, item)
	}

	return emitAgents(cmd, items)
}

// sessionFacts memoizes the two live-tmux derivations runAgentsList needs twice: once to build
// the occupancy sweep's roster, once to fill the row.
type sessionFacts struct {
	running     bool
	foreignRoot bool
}

// readAgentOccupancy classifies the occupancy channel for every agent in scope, in ONE sweep per
// invocation (mirroring the computePhaseCompletion precompute in dispatch.go).
//
// Best-effort in both directions, because `af agents list` is a cheap machine read whose error
// envelope is reserved for cwd/root/agents.json/store failures. A startup.json that will not load,
// or thresholds that are not configured, SKIP the sweep rather than handing the reader zero
// thresholds — zero thresholds classify every channel as dark, so the "safe" fallback would report
// a factory-wide outage that is not happening. A reader error is ignored deliberately: the reader
// still returns a fully-populated all-"none" map alongside it, so the rows stay honest.
//
// recovery.enabled and recovery.exclude are deliberately NOT consulted. They govern whether the
// engine ACTS on an agent; this is a pure observation surface, and hiding an excluded agent's
// occupancy would recreate the masking defect the phase exists to close.
func readAgentOccupancy(root string, scope map[string]struct{}) map[string]statusline.ChannelReading {
	if len(scope) == 0 {
		return nil
	}
	cfg, err := config.LoadStartupConfig(root)
	if err != nil {
		return nil
	}
	staleness := time.Duration(cfg.Recovery.StalenessSecs) * time.Second
	darkAfter := time.Duration(cfg.Recovery.DarkGraceSecs) * time.Second
	if staleness <= 0 || darkAfter <= 0 {
		return nil
	}
	readings, _ := statusline.ReadObservations(config.StatuslineSessionsDir(root), statusline.ReadOptions{
		KnownAgents: scope,
		Staleness:   staleness,
		DarkAfter:   darkAfter,
	}, time.Now())
	return readings
}

// sessionForeignRoot reports whether a live session's baked AF_ROOT resolves to a
// factory different from the querying root (K9b, #519). Best-effort: a getter error
// or an unset/empty AF_ROOT leaves it false — a read hiccup must never fail the
// listing (mirrors populateAgentStep's fail-open posture).
func sessionForeignRoot(tmux cmdTmux, sessionName, root string) bool {
	envRoot, err := tmux.GetEnvironment(sessionName, "AF_ROOT")
	if err != nil || envRoot == "" {
		return false
	}
	return !config.SameResolvedRoot(envRoot, root)
}

// populateAgentStep fills item's formula/step/inputs fields from the agent's
// active formula-instance bead in the shared store. It is read-only and
// best-effort: any store hiccup leaves item at its "no_formula" default rather
// than failing the whole listing.
func populateAgentStep(ctx context.Context, store issuestore.Store, name string, item *agentListItem) {
	instances, err := store.List(ctx, issuestore.Filter{
		Labels:   []string{"formula-instance"},
		Assignee: name,
	})
	if err != nil || len(instances) == 0 {
		return
	}
	// Most-recent active instance wins (an agent may have stale completed
	// instances; the loader already excludes terminal ones by default).
	inst := instances[0]
	for _, c := range instances[1:] {
		if c.CreatedAt.After(inst.CreatedAt) {
			inst = c
		}
	}

	// Prefer the running formula's name from the instance title ("Formula: <x>").
	if f := strings.TrimPrefix(inst.Title, "Formula: "); f != inst.Title && f != "" {
		item.Formula = f
	}

	// Inputs from the resolved_vars carrier bead (with a dispatch-task fallback).
	// The carrier is keyed by label, not Parent, so it never appears among the
	// instance's steps below.
	item.Inputs = readResolvedVars(ctx, store, inst.ID, inst.Description)

	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: inst.ID, IncludeAllAgents: true})
	if err != nil {
		return
	}

	if len(result.Steps) == 0 {
		// No ready step. Distinguish "blocked" (open children remain) from
		// "all_complete" (everything done).
		open, lerr := store.List(ctx, issuestore.Filter{
			Parent:           inst.ID,
			Statuses:         []issuestore.Status{issuestore.StatusOpen},
			IncludeAllAgents: true,
		})
		if lerr == nil && len(open) > 0 {
			item.StepState = "blocked"
		} else {
			item.StepState = "all_complete"
		}
		return
	}

	step := result.Steps[0]
	item.StepID = step.ID
	item.StepTitle = step.Title
	item.StepState = "ready"

	// Ready's DTO omits Description; re-fetch it for gate detection (mirrors
	// step.go:138).
	var desc string
	if iss, gerr := store.Get(ctx, step.ID); gerr == nil {
		desc = iss.Description
	}
	item.IsGate = isGateStep(ctx, store, step.ID, desc)
	item.GateID = firstOpenBlocker(ctx, store, step.ID)
}

// readResolvedVars returns the inputs map to surface for a formula instance. It
// reads the dedicated resolved_vars carrier bead by its instance-specific label
// (including closed, since the writer closes the carrier). A missing or malformed
// carrier degrades cleanly to the dispatch-task string parsed from the instance
// description, never an error.
func readResolvedVars(ctx context.Context, store issuestore.Store, instanceID, instanceDesc string) map[string]string {
	carriers, err := store.List(ctx, issuestore.Filter{
		Labels:           []string{resolvedVarsInstanceLabel(instanceID)},
		IncludeAllAgents: true,
		IncludeClosed:    true,
	})
	if err == nil {
		for _, c := range carriers {
			var m map[string]string
			if json.Unmarshal([]byte(c.Description), &m) == nil && m != nil {
				return m
			}
			break // carrier present but unparsable → clean fallback
		}
	}
	return dispatchTaskFallback(instanceDesc)
}

// dispatchTaskFallback extracts the embedded "Dispatch task: <text>" suffix that
// sling.go appends to a specialist-dispatch instance bead's description, exposing
// it under a "task" key so it is clearly labeled as the fallback (not a resolved
// --var). Returns an empty (non-nil) map when no task is embedded.
func dispatchTaskFallback(instanceDesc string) map[string]string {
	const marker = "Dispatch task: "
	if i := strings.LastIndex(instanceDesc, marker); i >= 0 {
		if task := strings.TrimSpace(instanceDesc[i+len(marker):]); task != "" {
			return map[string]string{"task": task}
		}
	}
	return map[string]string{}
}

// deriveAgentStatus maps (running, step state, is-gate) to an honest agent
// status. A running session with no active formula is "idle" — never "working" —
// which is the load-bearing honesty rule (a tmux session existing does not mean
// the agent is doing formula work). A running agent parked at a gate step reports
// "gated" rather than "working".
func deriveAgentStatus(running bool, stepState string, isGate bool) string {
	if !running {
		return "stopped"
	}
	switch stepState {
	case "ready":
		if isGate {
			return "gated"
		}
		return "working"
	case "blocked":
		return "blocked"
	case "all_complete", "no_formula":
		return "idle"
	default:
		return "idle"
	}
}

// emitAgents marshals the agent list as a single JSON line and writes it through the
// cobra output seam (cmd.OutOrStdout()), so a consumer that captures output via
// cmd.SetOut receives it. Always returns nil (failures fall back to an error envelope),
// matching the step.go emit* discipline.
func emitAgents(cmd *cobra.Command, items []agentListItem) error {
	data, err := json.Marshal(items)
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), `{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}

// emitAgentsError writes the error envelope `{"state":"error","error":"<msg>"}` through
// the cobra output seam and returns nil so the exit code stays 0 — callers branch on the
// output shape (an array on success, an object with state:"error" on failure).
func emitAgentsError(cmd *cobra.Command, e error) error {
	data, err := json.Marshal(stepErrorOutput{State: "error", Error: e.Error()})
	if err != nil {
		fmt.Fprintln(cmd.OutOrStdout(), `{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Fprintln(cmd.OutOrStdout(), string(data))
	return nil
}
