package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/lock"
)

var fidelityCmd = &cobra.Command{
	Use:   "fidelity [on|off]",
	Short: "Toggle or show fidelity gate status",
	Long: `Toggle the fidelity gate hook on or off, or show current status.

The fidelity gate is a runtime grader that evaluates each agent turn against
the current formula step's contract via a Haiku model. It fires after every
Stop event when a formula is hooked; on by default (af install --init creates
.agentfactory/.fidelity-gate with "on"). Mirrors af quality on the file-toggle side —
enabling writes "on\n", disabling writes "off\n" to .agentfactory/.fidelity-gate.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFidelity,
}

func init() {
	rootCmd.AddCommand(fidelityCmd)
}

func fidelityGateFile(factoryRoot string) string {
	return filepath.Join(factoryRoot, ".agentfactory", ".fidelity-gate")
}

func runFidelity(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}

	gateFile := fidelityGateFile(factoryRoot)

	if len(args) == 0 {
		// Show status
		data, err := os.ReadFile(gateFile)
		if err != nil || strings.TrimSpace(string(data)) != "on" {
			fmt.Println("fidelity gate: off")
		} else {
			lockPath := filepath.Join(cwd, ".runtime", "fidelity-gate.lock")
			if info, err := lock.NewWithPath(lockPath).Read(); err == nil && info.IsStale() {
				fmt.Printf("fidelity gate: on (WARNING: stale lock at .runtime/fidelity-gate.lock, PID %d dead)\n", info.PID)
			} else {
				fmt.Println("fidelity gate: on")
			}
		}
		return nil
	}

	switch args[0] {
	case "on":
		if err := applyFidelityGate(factoryRoot, cwd, "on"); err != nil {
			return err
		}
		fmt.Println("fidelity gate: on")
	case "off":
		if err := applyFidelityGate(factoryRoot, cwd, "off"); err != nil {
			return err
		}
		fmt.Println("fidelity gate: off")
	default:
		return fmt.Errorf("usage: af fidelity [on|off]")
	}

	return nil
}

// applyFidelityGate writes the fidelity gate, honoring the active-formula guard
// for "off". root locates the gate file (.agentfactory/.fidelity-gate);
// formulaDir is where .runtime/hooked_formula is checked (cwd for the CLI today,
// the af-up-resolved root for the Phase-3 startup path). Write errors are wrapped
// with the same messages runFidelity used historically; the guard refusal is
// returned verbatim so CLI behavior is unchanged.
func applyFidelityGate(root, formulaDir, state string) error {
	gateFile := fidelityGateFile(root)
	switch state {
	case "on":
		if err := os.WriteFile(gateFile, []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("enabling fidelity gate: %w", err)
		}
		return nil
	case "off":
		hookedFormula := filepath.Join(formulaDir, ".runtime", "hooked_formula")
		if _, err := os.Stat(hookedFormula); err == nil {
			return fmt.Errorf("cannot disable fidelity gate while a formula is active (found .runtime/hooked_formula)")
		}
		if err := os.WriteFile(gateFile, []byte("off\n"), 0644); err != nil {
			return fmt.Errorf("disabling fidelity gate: %w", err)
		}
		return nil
	}
	return fmt.Errorf("usage: af fidelity [on|off]")
}

// applyGate applies a quality/fidelity gate state using the af-up-resolved root
// (R-7: never re-derived from cwd). "default"/"" is a no-op (C-4 invariant).
// quality has no active-formula guard and is a direct write; fidelity routes
// through applyFidelityGate so the guard is honored. Its only caller is Phase 3's
// runUp (the SC8 mechanism).
func applyGate(root, formulaDir, gate, state string) error {
	if state == "" || state == "default" {
		return nil
	}
	switch gate {
	case "quality":
		return os.WriteFile(qualityGateFile(root), []byte(state+"\n"), 0644)
	case "fidelity":
		return applyFidelityGate(root, formulaDir, state)
	}
	return nil
}
