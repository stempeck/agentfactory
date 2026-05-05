package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

var fidelityCmd = &cobra.Command{
	Use:   "fidelity [on|off]",
	Short: "Toggle or show fidelity gate status",
	Long: `Toggle the fidelity gate hook on or off, or show current status.

The fidelity gate is a runtime grader that evaluates each agent turn against
the current formula step's contract via a Haiku model. It fires after every
Stop event when a formula is hooked; on by default (af install --init creates
.fidelity-gate with "on"). Mirrors af quality on the file-toggle side —
enabling writes "on\n", disabling writes "off\n" to .fidelity-gate.`,
	Args: cobra.MaximumNArgs(1),
	RunE: runFidelity,
}

func init() {
	rootCmd.AddCommand(fidelityCmd)
}

func fidelityGateFile(factoryRoot string) string {
	return filepath.Join(factoryRoot, ".fidelity-gate")
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
			fmt.Println("fidelity gate: on")
		}
		return nil
	}

	switch args[0] {
	case "on":
		if err := os.WriteFile(gateFile, []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("enabling fidelity gate: %w", err)
		}
		fmt.Println("fidelity gate: on")
	case "off":
		if err := os.WriteFile(gateFile, []byte("off\n"), 0644); err != nil {
			return fmt.Errorf("disabling fidelity gate: %w", err)
		}
		fmt.Println("fidelity gate: off")
	default:
		return fmt.Errorf("usage: af fidelity [on|off]")
	}

	return nil
}
