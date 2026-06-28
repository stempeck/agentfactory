package cmd

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// These commands give external (non-Go) consumers a write path for the curated
// config files, symmetric with the `af … --json` read commands: the consumer
// sends a JSON config on stdin and af-core validates + writes it atomically. This
// keeps such consumers off the internal config struct layout (they never import
// internal/config), closing the H-1 coupling on the write side. They are
// registered under the EXISTING `config` parent command (config.go), alongside
// `config build-host`.

var configDispatchCmd = &cobra.Command{
	Use:   "dispatch",
	Short: "Manage dispatch configuration (dispatch.json)",
}

var configDispatchSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Replace dispatch.json from a JSON document on stdin",
	Long: `Read a complete DispatchConfig as JSON on stdin, validate it (struct-level
plus a cross-file check that every referenced agent exists in agents.json), and
write it atomically to dispatch.json. On any validation failure the command exits
non-zero and leaves the existing file untouched.`,
	RunE: runConfigDispatchSet,
}

var configStartupCmd = &cobra.Command{
	Use:   "startup",
	Short: "Manage startup configuration (startup.json)",
}

var configStartupSetCmd = &cobra.Command{
	Use:   "set",
	Short: "Replace startup.json from a JSON document on stdin",
	Long: `Read a complete StartupConfig as JSON on stdin, validate it, and write it
atomically to startup.json. On validation failure the command exits non-zero and
leaves the existing file untouched.`,
	RunE: runConfigStartupSet,
}

func init() {
	// No --json flag: these commands only ever read JSON from stdin, so a --json flag
	// would be a dead control (never consulted) that misrepresents the CLI contract.
	configDispatchCmd.AddCommand(configDispatchSetCmd)
	configCmd.AddCommand(configDispatchCmd)

	configStartupCmd.AddCommand(configStartupSetCmd)
	configCmd.AddCommand(configStartupCmd)
}

// decodeJSONStdin decodes a single JSON document from the command's input (stdin
// in production; overridable via cmd.SetIn in tests). Empty input is an error.
func decodeJSONStdin(cmd *cobra.Command, v any) error {
	if err := json.NewDecoder(cmd.InOrStdin()).Decode(v); err != nil {
		return fmt.Errorf("parsing JSON from stdin: %w", err)
	}
	return nil
}

func runConfigDispatchSet(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	var cfg config.DispatchConfig
	if err := decodeJSONStdin(cmd, &cfg); err != nil {
		return err
	}

	// Cross-file validation (L-1): every referenced agent must exist in
	// agents.json. Runs BEFORE the write, so a dangling reference never touches
	// the file.
	agents, err := config.LoadAgentConfig(config.AgentsConfigPath(root))
	if err != nil {
		return fmt.Errorf("loading agents.json for cross-file validation: %w", err)
	}
	if err := config.ValidateDispatchConfig(&cfg, agents); err != nil {
		return err
	}

	// SaveDispatchConfig re-runs struct validation, then writes atomically.
	if err := config.SaveDispatchConfig(config.DispatchConfigPath(root), &cfg); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Dispatch configuration saved.")
	return nil
}

func runConfigStartupSet(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	var cfg config.StartupConfig
	if err := decodeJSONStdin(cmd, &cfg); err != nil {
		return err
	}

	if err := config.SaveStartupConfig(config.StartupConfigPath(root), &cfg); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Startup configuration saved.")
	return nil
}
