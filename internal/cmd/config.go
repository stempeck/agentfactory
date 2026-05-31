package cmd

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

var (
	flagMode         string
	flagHost         string
	flagUser         string
	flagKey          string
	flagMountPath    string
	flagStatus       bool
	flagRemove       bool
	flagSkipSSHCheck bool
)

var sshCheckFunc = func(host, user, keyPath string) error {
	cmd := exec.Command("ssh",
		"-i", keyPath,
		"-o", "ConnectTimeout=5",
		"-o", "StrictHostKeyChecking=accept-new",
		user+"@"+host,
		"echo", "ok",
	)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var localCheckFunc = func() error {
	return exec.Command("xcodebuild", "-version").Run()
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage factory configuration",
}

var configBuildHostCmd = &cobra.Command{
	Use:   "build-host",
	Short: "Configure iOS build host",
	RunE:  runConfigBuildHost,
}

func init() {
	rootCmd.AddCommand(configCmd)
	configCmd.AddCommand(configBuildHostCmd)

	configBuildHostCmd.Flags().StringVar(&flagMode, "mode", "", "Build mode: local or ssh")
	configBuildHostCmd.Flags().StringVar(&flagHost, "host", "", "SSH host address")
	configBuildHostCmd.Flags().StringVar(&flagUser, "user", "", "SSH username")
	configBuildHostCmd.Flags().StringVar(&flagKey, "key", "", "Path to SSH private key")
	configBuildHostCmd.Flags().StringVar(&flagMountPath, "mount-path", "", "Remote mount path")
	configBuildHostCmd.Flags().BoolVar(&flagStatus, "status", false, "Show current build-host configuration")
	configBuildHostCmd.Flags().BoolVar(&flagRemove, "remove", false, "Remove build-host configuration")
	configBuildHostCmd.Flags().BoolVar(&flagSkipSSHCheck, "skip-ssh-check", false, "Skip SSH connectivity check")
}

func runConfigBuildHost(cmd *cobra.Command, args []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}

	root, err := config.FindFactoryRoot(wd)
	if err != nil {
		return err
	}

	bhPath := config.BuildHostConfigPath(root)

	if flagStatus {
		return configBuildHostStatus(cmd, bhPath)
	}

	if flagRemove {
		return configBuildHostRemove(cmd, bhPath)
	}

	if flagMode != "" {
		return configBuildHostSet(cmd, bhPath)
	}

	return cmd.Help()
}

func configBuildHostStatus(cmd *cobra.Command, bhPath string) error {
	cfg, err := config.LoadBuildHostConfig(bhPath)
	if err != nil {
		return err
	}
	if cfg == nil {
		fmt.Fprintln(cmd.OutOrStdout(), "No build-host configuration found.")
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "mode:       %s\n", cfg.Mode)
	if cfg.Host != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "host:       %s\n", cfg.Host)
	}
	if cfg.User != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "user:       %s\n", cfg.User)
	}
	if cfg.KeyPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "key_path:   %s\n", cfg.KeyPath)
	}
	if cfg.MountPath != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "mount_path: %s\n", cfg.MountPath)
	}
	return nil
}

func configBuildHostRemove(cmd *cobra.Command, bhPath string) error {
	err := os.Remove(bhPath)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(cmd.OutOrStdout(), "No build-host configuration to remove.")
			return nil
		}
		return fmt.Errorf("removing build-host config: %w", err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Build-host configuration removed.")
	return nil
}

func configBuildHostSet(cmd *cobra.Command, bhPath string) error {
	switch flagMode {
	case "local":
		if err := localCheckFunc(); err != nil {
			fmt.Fprintln(cmd.ErrOrStderr(), "Warning: xcodebuild not found or failed; writing config anyway.")
		}
		cfg := &config.BuildHostConfig{Mode: "local"}
		if err := config.SaveBuildHostConfig(bhPath, cfg); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Build-host configuration saved (mode: local).")
		return nil

	case "ssh":
		if flagHost == "" {
			return fmt.Errorf("--host is required for ssh mode")
		}
		if flagUser == "" {
			return fmt.Errorf("--user is required for ssh mode")
		}
		if flagKey == "" {
			return fmt.Errorf("--key is required for ssh mode")
		}
		info, err := os.Stat(flagKey)
		if err != nil {
			if os.IsNotExist(err) {
				return fmt.Errorf("key file not found: %s", flagKey)
			}
			return fmt.Errorf("checking key file: %w", err)
		}
		if info.Mode().Perm()&0077 != 0 {
			fmt.Fprintf(cmd.ErrOrStderr(), "Warning: key file %s has permissions %04o; recommended 0600.\n", flagKey, info.Mode().Perm())
		}
		if !flagSkipSSHCheck {
			if err := sshCheckFunc(flagHost, flagUser, flagKey); err != nil {
				return fmt.Errorf("SSH connectivity check failed: %w\nUse --skip-ssh-check to skip this check", err)
			}
		}
		cfg := &config.BuildHostConfig{
			Mode:      "ssh",
			Host:      flagHost,
			User:      flagUser,
			KeyPath:   flagKey,
			MountPath: flagMountPath,
		}
		if err := config.SaveBuildHostConfig(bhPath, cfg); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "Build-host configuration saved (mode: ssh).")
		return nil

	default:
		return fmt.Errorf("invalid mode %q: must be \"local\" or \"ssh\"", flagMode)
	}
}
