package cmd

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

var pluginRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Uninstall an integration: drop its record and snapshots (operator-only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runPluginRemove,
}

// runPluginRemove is `af plugin remove <name>` (operator-only, integrations only). It deletes
// none of the integration's external writes (ADR-017) and never stops its service (D26, D58):
// both are host state af did not create and cannot prove unshared.
func runPluginRemove(cmd *cobra.Command, args []string) error {
	if err := requireOperator("plugin remove"); err != nil {
		return err
	}
	name := args[0]
	cwd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}
	path := config.PluginsConfigPath(root)
	cfg, err := config.LoadPluginsConfig(path)
	if err != nil {
		return err
	}
	entry, ok := cfg.Plugins[name]
	if !ok {
		return fmt.Errorf("plugin %q is not installed (nothing recorded in %s)", name, path)
	}
	if entry.Integration == nil {
		return fmt.Errorf("plugin %q is a formula plugin; af plugin remove uninstalls integrations only", name)
	}
	in := entry.Integration

	// The record goes first: an interrupted remove then leaves unreferenced snapshots, never a
	// record naming a snapshot that is gone.
	delete(cfg.Plugins, name)
	if err := config.SavePluginsConfig(path, cfg); err != nil {
		return err
	}
	snapshots := filepath.Join(config.IntegrationsDir(root), name)
	spared, err := removeUnpinnedSnapshots(root, name, snapshots)
	if err != nil {
		return fmt.Errorf("removed %q from %s, but its snapshots remain at %s: %w", name, path, snapshots, err)
	}
	for _, p := range []string{integrationCheckRecordPath(root, name), integrationServiceStatePath(root, name)} {
		if err := os.Remove(p); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("removed %q, but its runtime record %s remains: %w", name, p, err)
		}
	}

	out := cmd.OutOrStdout()
	fmt.Fprintf(out, "removed integration %s (record and snapshots)\n", displaySafe(name))
	for _, sha := range spared {
		fmt.Fprintf(out, "kept snapshot %s: a formula instance's pin still binds it; delete %s once that instance ends\n",
			displaySafe(sha), displaySafe(filepath.Join(snapshots, sha)))
	}
	if len(in.ExternalWrites) > 0 {
		fmt.Fprintln(out, "external writes left in place (remove deletes none; delete them yourself if nothing else uses them):")
		for _, w := range in.ExternalWrites {
			fmt.Fprintf(out, "  %s\n", displaySafe(w))
		}
	}
	if in.Service != "" {
		live, herr := newCmdTmux().HasSession(in.Service)
		switch {
		case herr != nil:
			fmt.Fprintf(out, "could not tell whether service session %s is running (%v); if it is, stop it with: tmux kill-session -t %s\n",
				displaySafe(in.Service), herr, shellQuote(in.Service))
		case live:
			fmt.Fprintf(out, "service session %s keeps running; stop it with: tmux kill-session -t %s\n",
				displaySafe(in.Service), shellQuote(in.Service))
		}
	}
	return nil
}

// removeUnpinnedSnapshots removes every snapshot of name that no pin binds, and the whole parent when none
// is pinned. It returns the spared snapshot hashes.
func removeUnpinnedSnapshots(root, name, parent string) ([]string, error) {
	pinned, err := pinnedIntegrationSnapshots(root, name)
	if err != nil {
		return nil, err
	}
	if len(pinned) == 0 {
		return nil, removeReadOnlyTree(parent)
	}
	entries, err := os.ReadDir(parent)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	var spared []string
	for _, e := range entries {
		if pinned[e.Name()] {
			spared = append(spared, e.Name())
			continue
		}
		if err := removeReadOnlyTree(filepath.Join(parent, e.Name())); err != nil {
			return spared, err
		}
	}
	return spared, nil
}

// removeReadOnlyTree restores u+w on a chmod -R a-w snapshot tree so RemoveAll can unlink its
// entries (H3-14).
func removeReadOnlyTree(dir string) error {
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chmod(p, info.Mode().Perm()|0o700)
	})
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return os.RemoveAll(dir)
}
