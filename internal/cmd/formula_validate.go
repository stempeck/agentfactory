package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/formula"
)

var formulaValidateCmd = &cobra.Command{
	Use:   "validate",
	Short: "Validate formula TOML from stdin and print the composed verdict as JSON",
	Long: `Read a formula.toml document on stdin and print the engine-of-record's composed
verdict as a single JSON document: {"ok":bool,"findings":[{"lamp","message"}]}.

The verdict is the af core's own Validate() plus TopologicalSort() run as separable
stages, so each failure is keyed to one of four lamps (parse, ids, needs, cycles) —
the same classification the shipped browser engine uses. This is the write-boundary
gate the web console consults before saving a formula.

Like the other --json read commands this ALWAYS exits 0; a rejecting verdict is
{"ok":false,...} in the payload, never a non-zero exit — so a caller distinguishes a
validation rejection from a process failure by the body, not the exit code.`,
	Args: cobra.NoArgs,
	RunE: runFormulaValidate,
}

func init() {
	formulaValidateCmd.Flags().Bool("json", true, "Emit JSON output (currently the only supported format)")
	formulaCmd.AddCommand(formulaValidateCmd)
}

// validateFinding is one composed-verdict failure keyed to a lamp. The lamp vocabulary
// (parse/ids/needs/cycles) matches the shipped browser engine (toml-engine.js validate).
type validateFinding struct {
	Lamp    string `json:"lamp"`
	Message string `json:"message"`
}

// validateOutput is the composed verdict. Findings is always a non-nil slice so the success
// case marshals as {"ok":true,"findings":[]} (an empty ARRAY, never null).
type validateOutput struct {
	OK       bool              `json:"ok"`
	Findings []validateFinding `json:"findings"`
}

// runFormulaValidate is the RunE for `af formula validate`. It reads TOML from stdin and runs
// toml.Decode -> formula.InferType -> Validate() -> TopologicalSort() as SEPARABLE stages (NOT
// formula.Parse, which folds decode+InferType+Validate into one wrapped message and runs Validate
// twice — IMPLREADME Gotcha 1). The inference is the engine's own exported method, so a fifth formula
// type cannot be inferred by Parse and silently missed by this verb. Always returns nil (exit 0); a
// rejecting verdict is encoded in the payload (mirrors runFormulaShow).
func runFormulaValidate(cmd *cobra.Command, _ []string) error {
	data, err := io.ReadAll(cmd.InOrStdin())
	if err != nil {
		return emitValidate(false, []validateFinding{{Lamp: "parse", Message: err.Error()}})
	}

	var f formula.Formula
	if _, err := toml.Decode(string(data), &f); err != nil {
		return emitValidate(false, []validateFinding{{Lamp: "parse", Message: err.Error()}})
	}
	f.InferType()

	if err := f.Validate(); err != nil {
		return emitValidate(false, []validateFinding{{Lamp: classifyLamp(err.Error()), Message: err.Error()}})
	}
	if _, err := f.TopologicalSort(); err != nil {
		return emitValidate(false, []validateFinding{{Lamp: classifyLamp(err.Error()), Message: err.Error()}})
	}

	return emitValidate(true, []validateFinding{})
}

// classifyLamp keys a single flat Validate()/TopologicalSort() error string to one of the four lamps.
// The buckets mirror the shipped browser engine's classifier (toml-engine.js validate): dependency
// cycles, unknown-dependency references, id-shape/arity problems, and everything else (schema/name/
// type/source/collision/skill) as `parse`. Order matters — cycles and needs are checked before ids so
// their more-specific substrings win.
func classifyLamp(msg string) string {
	switch {
	case strings.Contains(msg, "cycle"):
		return "cycles"
	case strings.Contains(msg, "needs unknown"), strings.Contains(msg, "references unknown"):
		return "needs"
	case strings.Contains(msg, "missing required id field"),
		strings.Contains(msg, "requires at least one"),
		strings.Contains(msg, "duplicate") && strings.Contains(msg, "id:"):
		return "ids"
	default:
		return "parse"
	}
}

// emitValidate prints the verdict envelope and returns nil (exit 0).
func emitValidate(ok bool, findings []validateFinding) error {
	data, err := json.Marshal(validateOutput{OK: ok, Findings: findings})
	if err != nil {
		fmt.Println(`{"ok":false,"findings":[{"lamp":"parse","message":"json marshal failed"}]}`)
		return nil
	}
	fmt.Println(string(data))
	return nil
}
