package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
	"github.com/stempeck/agentfactory/internal/worktree"
)

// integrationPinFile is the per-instance pin of the bound integration set, beside hooked_formula.
const integrationPinFile = "integration_bindings"

const integrationPinVersion = 1

type integrationPin struct {
	V        int                     `json:"v"`
	Formula  string                  `json:"formula"`
	Bindings []integrationPinBinding `json:"bindings"`
	Skipped  []integrationPinSkipped `json:"skipped"`
}

type integrationPinBinding struct {
	Name          string   `json:"name"`
	SnapshotDir   string   `json:"snapshot_dir"`
	ContentSHA256 string   `json:"content_sha256"`
	EnvKeys       []string `json:"env_keys"`
	Required      bool     `json:"required"`
	ClaudePlugins []string `json:"claude_plugins"`
}

type integrationPinSkipped struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

func integrationPinPath(agentDir string) string {
	return filepath.Join(agentDir, ".runtime", integrationPinFile)
}

func newIntegrationPin(formulaName string, bound []config.IntegrationBinding, skipped []integrationPinSkipped) *integrationPin {
	pin := &integrationPin{V: integrationPinVersion, Formula: formulaName, Bindings: []integrationPinBinding{}, Skipped: []integrationPinSkipped{}}
	for _, b := range bound {
		keys := []string{}
		for _, kv := range b.Env {
			keys = append(keys, kv.Key)
		}
		plugins := b.ClaudePlugins
		if plugins == nil {
			plugins = []string{}
		}
		pin.Bindings = append(pin.Bindings, integrationPinBinding{
			Name:          b.Name,
			SnapshotDir:   b.SnapshotDir,
			ContentSHA256: b.ContentSHA256,
			EnvKeys:       keys,
			Required:      b.Required,
			ClaudePlugins: plugins,
		})
	}
	pin.Skipped = append(pin.Skipped, skipped...)
	return pin
}

// readIntegrationPin reports found=false with a nil error when the agent has no pin; a pin that is present
// but unreadable, malformed or of an unknown version is an error so the caller can report it (D29).
func readIntegrationPin(agentDir string) (*integrationPin, bool, error) {
	b, err := os.ReadFile(integrationPinPath(agentDir))
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, fmt.Errorf("reading integration pin: %w", err)
	}
	var pin integrationPin
	if err := json.Unmarshal(b, &pin); err != nil {
		return nil, true, fmt.Errorf("integration pin %s is malformed: %w", integrationPinPath(agentDir), err)
	}
	if pin.V != integrationPinVersion {
		return nil, true, fmt.Errorf("integration pin %s has unknown version %d", integrationPinPath(agentDir), pin.V)
	}
	return &pin, true, nil
}

func writeIntegrationPin(agentDir string, pin *integrationPin) error {
	b, err := json.Marshal(pin)
	if err != nil {
		return err
	}
	p := integrationPinPath(agentDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return fmt.Errorf("writing integration pin: %w", err)
	}
	if err := fsutil.WriteFileAtomic(p, append(b, '\n'), 0o644); err != nil {
		return fmt.Errorf("writing integration pin %s: %w", p, err)
	}
	return nil
}

func clearIntegrationPin(agentDir string) error {
	if err := os.Remove(integrationPinPath(agentDir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("clearing integration pin: %w", err)
	}
	return nil
}

// pinAdmittedIntegrations pins the bound set of admission's integrations, factory-scope ones included. A pin
// turns the guard on, so it is written only when that set binds something or admission skipped something and
// is otherwise cleared, not even a stale one left behind (D39). An unreadable plugins.json refuses a pin that
// would silently lack the factory-scope set, unless there is nothing to pin: every unpinned launch reports the
// error. A new pin is a new instance, so its guard denials are announced afresh (D32).
func pinAdmittedIntegrations(root, agentDir, formulaName string, admitted []config.IntegrationBinding, reports []string) error {
	skipped := skippedFromReports(reports)
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		if len(admitted) == 0 && len(skipped) == 0 {
			return clearIntegrationPin(agentDir)
		}
		return fmt.Errorf("writing integration pin: %w", err)
	}
	var required, optional []string
	for _, b := range admitted {
		if b.Required {
			required = append(required, b.Name)
		} else {
			optional = append(optional, b.Name)
		}
	}
	bound, candidates, err := integrationBoundSet(root, cfg, nil, required, optional)
	if err != nil {
		return fmt.Errorf("writing integration pin: %w", err)
	}
	if len(bound) == 0 && len(skipped) == 0 {
		return clearIntegrationPin(agentDir)
	}
	for _, name := range candidates {
		in := cfg.Plugins[name].Integration
		if in == nil || slices.ContainsFunc(bound, func(b config.IntegrationBinding) bool { return b.Name == name }) {
			continue
		}
		bound = append(bound, config.IntegrationBinding{
			Name:          name,
			Scope:         in.Scope,
			SnapshotDir:   in.SnapshotDir,
			ContentSHA256: in.ContentSHA256,
			ClaudePlugins: in.ClaudePlugins,
			Required:      slices.Contains(required, name),
		})
	}
	if err := writeIntegrationPin(agentDir, newIntegrationPin(formulaName, bound, skipped)); err != nil {
		return err
	}
	clearIntegrationReport(root, "unknown", integrationReportGuardDenied)
	for _, b := range bound {
		clearIntegrationReport(root, b.Name, integrationReportGuardDenied)
	}
	return nil
}

// integrationBoundSet is the one rule for what a session binds, shared by the pin writer and the composer: a
// pin binds exactly the snapshots it names; without one, the formula's integrations bind together with every
// factory-scope one. A factory-scope integration reaches every session, so the pin must name it (IR:L407,
// L436). candidates are the names that should bind, because one that fails to bind at pin time is still
// pinned so that every launch reports it (D29).
func integrationBoundSet(root string, cfg *config.PluginsConfig, pin *integrationPin, required, optional []string) ([]config.IntegrationBinding, []string, error) {
	if pin == nil {
		bound, _, _, err := config.ResolveIntegrationBindings(root, cfg, required, optional)
		candidates := slices.Concat(required, optional, config.FactoryScopeIntegrations(cfg))
		slices.Sort(candidates)
		return bound, slices.Compact(candidates), err
	}
	var bound []config.IntegrationBinding
	var candidates []string
	for _, pb := range pin.Bindings {
		candidates = append(candidates, pb.Name)
		if b, err := config.BindIntegration(root, pb.Name, pinnedIntegrationRecord(cfg, pb)); err == nil {
			b.Required = pb.Required
			bound = append(bound, b)
		}
	}
	slices.Sort(candidates)
	return bound, slices.Compact(candidates), nil
}

// pinnedIntegrationSnapshots returns the snapshot hashes of integration name that some agent's pin still
// binds, in the root checkout or any worktree, so garbage collection cannot pull a snapshot from under a
// running formula instance. Unreadable worktree metadata is an error, not a skip: a pin behind it may still
// bind, and deleting the snapshot it names cannot be undone.
func pinnedIntegrationSnapshots(root, name string) (map[string]bool, error) {
	pins, err := integrationPinPaths(root)
	if err != nil {
		return nil, err
	}
	pinned := map[string]bool{}
	for _, p := range pins {
		pin, found, err := readIntegrationPin(filepath.Dir(filepath.Dir(p)))
		if !found || err != nil {
			continue
		}
		for _, b := range pin.Bindings {
			if b.Name == name {
				pinned[b.ContentSHA256] = true
			}
		}
	}
	return pinned, nil
}

// integrationPinPaths lists every path an agent's pin may sit at, each once; a path may name no pin. It
// returns all or nothing, so no caller mistakes a partial list for every pin.
func integrationPinPaths(root string) ([]string, error) {
	var paths []string
	seen := map[string]bool{}
	add := func(p string) {
		if !seen[p] {
			seen[p] = true
			paths = append(paths, p)
		}
	}
	for _, pattern := range []string{
		filepath.Join(config.AgentDir(root, "*"), ".runtime", integrationPinFile),
		filepath.Join(config.AgentDir(filepath.Join(root, ".agentfactory", "worktrees", "*"), "*"), ".runtime", integrationPinFile),
	} {
		matches, _ := filepath.Glob(pattern)
		for _, m := range matches {
			add(m)
		}
	}
	// The in-tree glob still finds a worktree whose metadata is lost; only its metadata finds a relocated one.
	// Its agents are read with ReadDir because Glob drops I/O errors, which would hide their pins.
	metas, err := worktree.ListMetas(root)
	if err != nil {
		return nil, err
	}
	for _, meta := range metas {
		dir := config.AgentsDir(worktree.AbsWorktreePath(root, meta))
		entries, err := os.ReadDir(dir)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("worktree %s: reading agents at %s: %w", meta.ID, dir, err)
		}
		for _, e := range entries {
			add(integrationPinPath(filepath.Join(dir, e.Name())))
		}
	}
	return paths, nil
}

// pinnedIntegrationNames returns the name of every integration some agent's pin binds. An unreadable pin
// names none: the composer binds only factory-scope integrations for it and reports why at its launch.
func pinnedIntegrationNames(root string) (map[string]bool, error) {
	pins, err := integrationPinPaths(root)
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	for _, p := range pins {
		pin, found, err := readIntegrationPin(filepath.Dir(filepath.Dir(p)))
		if !found || err != nil {
			continue
		}
		for _, b := range pin.Bindings {
			names[b.Name] = true
		}
	}
	return names, nil
}

// outputIntegrationLines prints one line per integration the agent's pin names: its binding state now, and
// every optional integration admission skipped. Without a pin it prints nothing.
func outputIntegrationLines(out io.Writer, root, agentDir string) {
	pin, found, err := readIntegrationPin(agentDir)
	if !found {
		return
	}
	if err != nil {
		fmt.Fprintf(out, "integrations: %v\n", err)
		return
	}
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		fmt.Fprintf(out, "integrations: %v\n", err)
		return
	}
	for _, pb := range pin.Bindings {
		fmt.Fprintf(out, "integration %s: %s\n", pb.Name, pinnedIntegrationState(root, cfg, pb))
	}
	for _, sk := range pin.Skipped {
		fmt.Fprintf(out, "integration %s: skipped (optional): %s\n", sk.Name, sk.Reason)
	}
}

func pinnedIntegrationState(root string, cfg *config.PluginsConfig, pb integrationPinBinding) string {
	if _, err := config.BindIntegration(root, pb.Name, pinnedIntegrationRecord(cfg, pb)); err != nil {
		var be *config.IntegrationBindError
		if errors.As(err, &be) && be.Drifted {
			return fmt.Sprintf("not bound: content changed (%d files); run af plugin verify %s", be.Changed, pb.Name)
		}
		return "not bound: snapshot missing or invalid"
	}
	rec, err := readIntegrationCheckRecord(root, pb.Name)
	switch {
	case err != nil:
		return fmt.Sprintf("bound; check record unreadable: %v", err)
	case rec == nil || (rec.ContentSHA256 != "" && rec.ContentSHA256 != pb.ContentSHA256):
		// A record for other content describes a later install, not the snapshot this session runs (D70).
		// A legacy record without a hash cannot be attributed and is shown rather than hidden.
		return "ok"
	case rec.State != integrationCheckOK:
		return fmt.Sprintf("check failed at %s", rec.At)
	}
	return "ok"
}
