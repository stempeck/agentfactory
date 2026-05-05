package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

var rootSubCmd = &cobra.Command{
	Use:   "root",
	Short: "Print factory root path",
	RunE: func(cmd *cobra.Command, args []string) error {
		cwd, err := getWd()
		if err != nil {
			return err
		}
		root, err := config.FindLocalRoot(cwd)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), root)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(rootSubCmd)
}
