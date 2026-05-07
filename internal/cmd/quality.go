package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

var qualityCmd = &cobra.Command{
	Use:   "quality [on|off]",
	Short: "Toggle or show quality gate status",
	Long:  `Toggle the quality gate hook on or off, or show current status.`,
	Args:  cobra.MaximumNArgs(1),
	RunE:  runQuality,
}

func init() {
	rootCmd.AddCommand(qualityCmd)
}

func qualityGateFile(factoryRoot string) string {
	return filepath.Join(factoryRoot, ".agentfactory", ".quality-gate")
}

func runQuality(cmd *cobra.Command, args []string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	factoryRoot, err := config.FindFactoryRoot(cwd)
	if err != nil {
		return err
	}

	gateFile := qualityGateFile(factoryRoot)

	if len(args) == 0 {
		// Show status
		data, err := os.ReadFile(gateFile)
		if err != nil || strings.TrimSpace(string(data)) != "on" {
			fmt.Println("quality gate: off")
		} else {
			fmt.Println("quality gate: on")
		}
		return nil
	}

	switch args[0] {
	case "on":
		if err := os.WriteFile(gateFile, []byte("on\n"), 0644); err != nil {
			return fmt.Errorf("enabling quality gate: %w", err)
		}
		fmt.Println("quality gate: on")
	case "off":
		if err := os.WriteFile(gateFile, []byte("off\n"), 0644); err != nil {
			return fmt.Errorf("disabling quality gate: %w", err)
		}
		fmt.Println("quality gate: off")
	default:
		return fmt.Errorf("usage: af quality [on|off]")
	}

	return nil
}
