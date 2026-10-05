package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

var pluginAcquireCmd = &cobra.Command{
	Use:   "acquire <name>",
	Short: "Copy an integration embedded in af into store/plugins/<name>/ (inert; operator-only)",
	Args:  cobra.ExactArgs(1),
	RunE:  runPluginAcquire,
}

// acquireEmbeddedFS returns the tree `af plugin acquire` copies from, rooted so that
// <name>/af-integration.toml is a path in it. A seam because the reference integrations land in
// Phase 4, so tests supply their own tree.
var acquireEmbeddedFS = func() fs.FS {
	sub, err := fs.Sub(embeddedIntegrations, "integrations")
	if err != nil {
		panic(err) // fs.Sub fails only on an invalid path literal
	}
	return sub
}

// acquireMarkerFile records an embedded acquisition's provenance (D7). plugins.json has one
// writer (install), and an embedded copy has no git metadata to read provenance from.
const acquireMarkerFile = ".af-acquire.json"

type acquireMarker struct {
	Source string `json:"source"`
	Commit string `json:"commit"`
}

const acquireSourceEmbedded = "embedded"

func runPluginAcquire(cmd *cobra.Command, args []string) error {
	if err := requireOperator("plugin acquire"); err != nil {
		return err
	}
	name := args[0]
	if err := config.ValidateAgentName(name); err != nil {
		return fmt.Errorf("integration name %q is not a valid agent name (must match [a-zA-Z][a-zA-Z0-9_-]*)", name)
	}
	cwd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}

	fsys := acquireEmbeddedFS()
	if _, err := fs.Stat(fsys, path.Join(name, config.IntegrationManifestFile)); err != nil {
		return fmt.Errorf("af embeds no integration named %q", name)
	}
	dir := filepath.Join(config.PluginsDir(root), name)
	if _, err := os.Lstat(dir); err == nil {
		return fmt.Errorf("%s already exists; acquire never overwrites an acquisition (remove it first to re-acquire)", dir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	if err := copyEmbeddedIntegration(fsys, name, dir); err != nil {
		_ = os.RemoveAll(dir)
		return fmt.Errorf("acquiring %q: %w", name, err)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "acquired %s into %s (inert: review it, then run af plugin install %s)\n", name, dir, name)
	return nil
}

// copyEmbeddedIntegration writes every file under name/ to dir. embed.FS keeps no mode bits, so
// the exec rule (N12) and a #! first line decide each file's mode from the copied manifest.
func copyEmbeddedIntegration(fsys fs.FS, name, dir string) error {
	var rels []string
	shebang := map[string]bool{}
	err := fs.WalkDir(fsys, name, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(fsys, p)
		if err != nil {
			return err
		}
		rel := p[len(name)+1:]
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		rels = append(rels, rel)
		shebang[rel] = bytes.HasPrefix(b, []byte("#!"))
		return os.WriteFile(dst, b, 0o644)
	})
	if err != nil {
		return err
	}
	m, err := config.LoadIntegrationManifestNamed(dir, name)
	if err != nil {
		return err
	}
	for _, rel := range rels {
		mode := os.FileMode(0o644)
		if integrationExecPath(m, rel) || shebang[rel] {
			mode = 0o755
		}
		if err := os.Chmod(filepath.Join(dir, filepath.FromSlash(rel)), mode); err != nil {
			return err
		}
	}
	marker, err := json.Marshal(acquireMarker{Source: acquireSourceEmbedded, Commit: Version})
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, acquireMarkerFile), append(marker, '\n'), 0o644)
}

// integrationExecPath is the exec rule (N12): a declared run path, or a file directly under
// bin/ at the top level or in a declared [claude] plugin dir.
func integrationExecPath(m *config.IntegrationManifest, rel string) bool {
	rel = path.Clean(rel)
	if slices.Contains(integrationRunPaths(m), rel) {
		return true
	}
	parent := path.Dir(rel)
	if path.Base(parent) != "bin" {
		return false
	}
	owner := path.Dir(parent)
	if owner == "." {
		return true
	}
	return m.Claude != nil && slices.Contains(cleanPaths(m.Claude.Plugins), owner)
}

// integrationRunPaths lists the manifest's declared run scripts.
func integrationRunPaths(m *config.IntegrationManifest) []string {
	var runs []string
	if m.Install != nil {
		runs = append(runs, m.Install.Run)
	}
	if m.Check != nil {
		runs = append(runs, m.Check.Run)
	}
	if m.Service != nil {
		runs = append(runs, m.Service.Run)
	}
	return cleanPaths(runs)
}

func cleanPaths(ps []string) []string {
	out := make([]string, 0, len(ps))
	for _, p := range ps {
		out = append(out, path.Clean(p))
	}
	return out
}

// readAcquireMarker reads acquire's provenance marker; ok is false when there is none.
func readAcquireMarker(dir string) (acquireMarker, bool) {
	b, err := os.ReadFile(filepath.Join(dir, acquireMarkerFile))
	if err != nil {
		return acquireMarker{}, false
	}
	var m acquireMarker
	if json.Unmarshal(b, &m) != nil || m.Source != acquireSourceEmbedded {
		return acquireMarker{}, false
	}
	return m, true
}
