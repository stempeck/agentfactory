package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

// Version information set via ldflags at build time.
var (
	Version   = "dev"
	Commit    = "unknown"
	BuildTime = "unknown"
)

var rootCmd = &cobra.Command{
	Use:     "af",
	Short:   "Agentfactory - Multi-agent workspace manager",
	Version: Version,
	Long: `Agentfactory (af) manages multi-agent workspaces called factories.

It coordinates agent spawning, work distribution, and communication
across distributed teams of AI agents working on shared codebases.`,
	SilenceErrors: true,
	SilenceUsage:  true,
}

// Execute runs the root command and returns an exit code.
func Execute() int {
	if err := rootCmd.Execute(); err != nil {
		var noMail errNoMail
		if errors.As(err, &noMail) {
			return 1
		}
		rootCmd.PrintErrln("Error:", err)
		return 1
	}
	return 0
}
