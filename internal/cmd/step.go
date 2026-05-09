package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/issuestore"
)

var stepCmd = &cobra.Command{
	Use:   "step",
	Short: "Formula step inspection commands",
}

var currentCmd = &cobra.Command{
	Use:   "current",
	Short: "Print the current formula step as JSON",
	Long: `Print the state of the currently-active formula step as a single-line
JSON document. Intended for scripting and hook consumption (the fidelity-gate
Stop hook parses this output with jq).`,
	RunE: runStepCurrent,
}

func init() {
	// --json is declared for future symmetry with a hypothetical --text
	// mode; today it is a no-op because JSON is always emitted.
	currentCmd.Flags().Bool("json", true, "Emit JSON output (currently the only supported format)")
	stepCmd.AddCommand(currentCmd)
	rootCmd.AddCommand(stepCmd)
}

// stepCurrentOutput is the JSON shape emitted by `af step current --json`
// for the ready-branch state. The field names are a public API contract
// with the fidelity-gate Phase 2 bash hook (parses via jq) — do not
// rename without a coordinated rollout.
//
// DEVIATION FROM SPEC (IMPLREADME_PHASE1.md Gotcha #13): the spec declares
// that "the 7-field stepCurrentOutput struct uses omitempty on ALL non-State
// fields." GateID here intentionally OMITS omitempty so it is always emitted
// in the ready branch, giving the Phase 2 jq consumer a stable 7-key shape
// (state, id, title, description, is_gate, gate_id, formula) even when the
// step has no structural blocker. This deviation is load-bearing for
// TestStepCurrent_SchemaSnapshot, which asserts exactly 7 keys: the spec's
// literal struct tags would emit 6 keys for a gate-by-description step
// (gate_id == "" → stripped by omitempty), contradicting AC1.8. The
// behavioral intent of Gotcha #13 — minimal shapes for no_formula/blocked/
// all_complete — is preserved via the stepStateOnly struct below, and the
// error state is preserved via stepErrorOutput.
//
// IsGate uses omitempty so a non-gate ready step emits 6 keys instead of 7
// — consumers that care about gate-ness must check gate_id != "" as well
// as checking is_gate == true.
type stepCurrentOutput struct {
	State       string `json:"state"`
	ID          string `json:"id,omitempty"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	IsGate      bool   `json:"is_gate,omitempty"`
	GateID      string `json:"gate_id"`
	Formula     string `json:"formula,omitempty"`
}

// stepStateOnly is the minimal envelope used for no_formula, blocked, and
// all_complete outputs, each of which emit exactly {"state":"..."} with no
// other keys. Keeping a separate struct (rather than relying on
// stepCurrentOutput + omitempty) lets GateID be unconditional in the ready
// branch while these minimal states stay compact — which the Phase 2
// bash hook depends on for its `jq -e '.state == "no_formula"'` branch.
type stepStateOnly struct {
	State string `json:"state"`
}

// stepErrorOutput is the two-field envelope used when infrastructure fails
// (getWd, FindFactoryRoot, newIssueStore, store.Ready). Kept separate from
// stepCurrentOutput so the intentional no-omitempty gate_id in the ready
// branch does not leak a spurious `"gate_id":""` key into error output.
// The downstream contract is `{"state":"error","error":"<msg>"}`.
type stepErrorOutput struct {
	State string `json:"state"`
	Error string `json:"error"`
}

// runStepCurrent is the RunE for `af step current`. It always emits a
// single JSON line on stdout and returns nil — infrastructure errors are
// reflected in the output's `state` field (state="error") so hook callers
// can branch on `.state` without also checking exit codes.
func runStepCurrent(cmd *cobra.Command, _ []string) error {
	ctx := cmd.Context()

	cwd, err := getWd()
	if err != nil {
		return emitError(err)
	}

	instanceID := readHookedFormulaID(cwd)
	if instanceID == "" {
		return emitMinimal("no_formula")
	}

	actor := os.Getenv("AF_ACTOR")
	store, err := newIssueStore(cwd, actor)
	if err != nil {
		return emitError(err)
	}

	result, err := store.Ready(ctx, issuestore.Filter{MoleculeID: instanceID})
	if err != nil {
		return emitError(err)
	}

	if len(result.Steps) == 0 {
		openChildren, err := store.List(ctx, issuestore.Filter{
			Parent:   instanceID,
			Statuses: []issuestore.Status{issuestore.StatusOpen},
		})
		if err != nil {
			return emitError(fmt.Errorf("listing open children: %w", err))
		}
		if len(openChildren) > 0 {
			return emitMinimal("blocked")
		}
		return emitMinimal("all_complete")
	}

	// Ready branch: result.Steps[0] is "next ready step" — matches the
	// R-API-2 convention shared with prime.go:375, done.go:105, and
	// handoff.go:138,177. For linear workflow formulas this is identical
	// to "current"; under hypothetical parallel branching it picks an
	// arbitrary ready step. Formulas are linear today, so no defensive
	// tie-breaking is required.
	step := result.Steps[0]

	// Ready DTO does not carry Description — re-fetch it via Get.
	// Mirrors prime.go:399-401.
	var description string
	if iss, err := store.Get(ctx, step.ID); err == nil {
		description = iss.Description
	}

	// Fetch formula name from instance bead title; fallback to instance ID
	// if the lookup fails or Title is empty. Mirrors prime.go:362-365.
	formulaName := instanceID
	if iss, err := store.Get(ctx, instanceID); err == nil && iss.Title != "" {
		formulaName = iss.Title
	}

	isGate := isGateStep(ctx, store, step.ID, description)
	gateID := firstOpenBlocker(ctx, store, step.ID)

	return emitState(stepCurrentOutput{
		State:       "ready",
		ID:          step.ID,
		Title:       step.Title,
		Description: description,
		IsGate:      isGate,
		GateID:      gateID,
		Formula:     formulaName,
	})
}

// firstOpenBlocker returns the ID of the first non-terminal blocker of the
// given step, or "" if the step has no open blockers (or the step itself
// is unreadable). Defensive: re-reads each blocker via store.Get because
// no adapter filters Issue.BlockedBy by terminal status — trusting
// len(iss.BlockedBy) > 0 would over-report
// gate steps whenever a blocker is already closed/done. Mirrors the
// semantics of stepHasOpenBlockers (prime.go:539-555); the two are
// siblings, not substitutes — firstOpenBlocker returns an ID (the most
// useful operator diagnostic), stepHasOpenBlockers returns a bool. If a
// blocker exists but cannot be fetched, returns that blocker's ID as a
// diagnostic rather than swallowing it.
func firstOpenBlocker(ctx context.Context, store issuestore.Store, stepID string) string {
	iss, err := store.Get(ctx, stepID)
	if err != nil {
		return ""
	}
	for _, ref := range iss.BlockedBy {
		blocker, err := store.Get(ctx, ref.ID)
		if err != nil {
			return ref.ID
		}
		if !blocker.Status.IsTerminal() {
			return blocker.ID
		}
	}
	return ""
}

// emitState marshals a full ready-branch output struct and prints it as a
// single JSON line on stdout. Always returns nil — failures are reflected
// in the output itself. If json.Marshal somehow fails (should be
// impossible with this struct — no channels, no funcs), a last-resort
// fixed-string fallback is emitted so the hook's jq parser still sees a
// valid state field.
//
// fmt.Println (not fmt.Print) appends the trailing newline that matches
// the bead.go:127 JSON output house pattern and keeps jq happy.
func emitState(out stepCurrentOutput) error {
	data, err := json.Marshal(out)
	if err != nil {
		fmt.Println(`{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Println(string(data))
	return nil
}

// emitMinimal prints a state-only JSON envelope: exactly
// `{"state":"<state>"}` followed by a newline. Used for no_formula,
// blocked, and all_complete where no other fields are meaningful.
func emitMinimal(state string) error {
	data, err := json.Marshal(stepStateOnly{State: state})
	if err != nil {
		fmt.Println(`{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Println(string(data))
	return nil
}

// emitError prints an error-state JSON envelope of the form
// `{"state":"error","error":"<msg>"}` followed by a newline. Uses the
// dedicated stepErrorOutput struct so the ready-branch struct's
// no-omitempty gate_id field does not bleed into the error shape.
func emitError(e error) error {
	data, err := json.Marshal(stepErrorOutput{State: "error", Error: e.Error()})
	if err != nil {
		fmt.Println(`{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Println(string(data))
	return nil
}
