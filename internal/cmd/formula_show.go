package cmd

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/formula"
)

var formulaShowCmd = &cobra.Command{
	Use:   "show <name>",
	Short: "Print a formula's inputs and vars as JSON",
	Long: `Print a formula's declared inputs and vars as a single JSON document.

Reads the formula's typed [inputs.*] and [vars.*] blocks directly (NOT the merged
Vars, which drop an input's type and required_unless), so the conditional-required
logic and field typing the sling form needs are preserved. Intended for machine
consumption — the field set is a versioned contract pinned by
TestFormulaShow_JSON_SchemaSnapshot.

Like the other --json read commands, this always exits 0; a missing or unparsable
formula is encoded as {"state":"error","error":"..."} so callers branch on the
output shape rather than the exit code.`,
	Args: cobra.ExactArgs(1),
	RunE: runFormulaShow,
}

func init() {
	formulaShowCmd.Flags().Bool("json", true, "Emit JSON output (currently the only supported format)")
	formulaCmd.AddCommand(formulaShowCmd)
}

// formulaField is the per-field JSON shape for one declared input or var. The
// key set is a versioned contract pinned by TestFormulaShow_JSON_SchemaSnapshot;
// none of the keys use omitempty so the shape is stable across inputs (which
// carry type/required_unless) and vars (which do not — those emit as ""/null/[]
// by design, see formula.Var).
type formulaField struct {
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Type           string   `json:"type"`
	Required       bool     `json:"required"`
	RequiredUnless []string `json:"required_unless"`
	Default        string   `json:"default"`
	Source         string   `json:"source"`
}

// formulaShowOutput is the success shape of `af formula show <name> --json`.
type formulaShowOutput struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Type        string         `json:"type"`
	Inputs      []formulaField `json:"inputs"`
	Vars        []formulaField `json:"vars"`
}

// runFormulaShow is the RunE for `af formula show`. Always returns nil — a
// not-found / parse failure is reflected in an error envelope (mirrors
// runStepCurrent, step.go:90).
func runFormulaShow(cmd *cobra.Command, args []string) error {
	name := args[0]

	cwd, err := getWd()
	if err != nil {
		return emitFormulaError(err)
	}
	path, err := formula.FindFormulaFile(name, cwd)
	if err != nil {
		return emitFormulaError(err)
	}
	f, err := formula.ParseFile(path)
	if err != nil {
		return emitFormulaError(err)
	}

	out := formulaShowOutput{
		Name:        f.Name,
		Description: f.Description,
		Type:        string(f.Type),
		Inputs:      make([]formulaField, 0, len(f.Inputs)),
		Vars:        make([]formulaField, 0, len(f.Vars)),
	}

	// Maps have no defined order; sort by key for deterministic, snapshot-stable
	// output.
	for _, k := range sortedKeys(f.Inputs) {
		in := f.Inputs[k]
		out.Inputs = append(out.Inputs, formulaField{
			Name:           k,
			Description:    in.Description,
			Type:           in.Type,
			Required:       in.Required,
			RequiredUnless: in.RequiredUnless,
			Default:        in.Default,
			// [inputs.*] carry no source; the merged var would default it, but
			// reading raw preserves the input/var distinction the form needs.
			Source: "",
		})
	}
	for _, k := range sortedVarKeys(f.Vars) {
		v := f.Vars[k]
		out.Vars = append(out.Vars, formulaField{
			Name:        k,
			Description: v.Description,
			// formula.Var has no Type / RequiredUnless — emit zero values so the
			// field shape stays uniform with inputs.
			Type:           "",
			Required:       v.Required,
			RequiredUnless: nil,
			Default:        v.Default,
			Source:         v.Source,
		})
	}

	data, err := json.Marshal(out)
	if err != nil {
		return emitFormulaError(err)
	}
	fmt.Println(string(data))
	return nil
}

// sortedKeys returns the keys of an inputs map in deterministic order.
func sortedKeys(m map[string]formula.Input) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// sortedVarKeys returns the keys of a vars map in deterministic order.
func sortedVarKeys(m map[string]formula.Var) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// emitFormulaError prints the error envelope and returns nil (exit 0).
func emitFormulaError(e error) error {
	data, err := json.Marshal(stepErrorOutput{State: "error", Error: e.Error()})
	if err != nil {
		fmt.Println(`{"state":"error","error":"json marshal failed"}`)
		return nil
	}
	fmt.Println(string(data))
	return nil
}
