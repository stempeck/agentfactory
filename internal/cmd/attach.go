package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/tmux"
)

var attachCmd = &cobra.Command{
	Use:   "attach <agent>",
	Short: "Attach to an agent's tmux session",
	Args:  cobra.ExactArgs(1),
	RunE:  runAttach,
}

func init() {
	rootCmd.AddCommand(attachCmd)
}

func runAttach(cmd *cobra.Command, args []string) error {
	agentName := args[0]

	wd, err := getWd()
	if err != nil {
		return err
	}

	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	// Validate agent exists
	agentsPath := config.AgentsConfigPath(root)
	agentsCfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		return err
	}

	if _, ok := agentsCfg.Agents[agentName]; !ok {
		return fmt.Errorf("unknown agent: %s", agentName)
	}

	// Check session is running
	t := tmux.NewTmux()
	sessionID := session.SessionName(agentName)

	running, err := t.HasSession(sessionID)
	if err != nil {
		return fmt.Errorf("checking session: %w", err)
	}
	if !running {
		return fmt.Errorf("session %s is not running (run 'af up %s' first)", sessionID, agentName)
	}

	// If inside tmux, use switch-client; otherwise, attach directly
	if os.Getenv("TMUX") != "" {
		return t.SwitchClient(sessionID)
	}
	return t.AttachSession(sessionID)
}
