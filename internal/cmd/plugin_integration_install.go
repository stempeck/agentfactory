package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// pluginInstallDispatch is pluginInstallCmd.RunE (D27): it classifies the batch without writing
// anything and routes an all-integration batch to runIntegrationInstall and anything else to the
// unchanged formula pipeline. Batches the two pipelines cannot share are refused here, before
// either one runs.
func pluginInstallDispatch(cmd *cobra.Command, args []string) error {
	integrationOnly, err := classifyInstallBatch(cmd, args)
	if err != nil {
		return err
	}
	if integrationOnly {
		return runIntegrationInstall(cmd, args)
	}
	return runPluginInstall(cmd, args)
}

// classifyInstallBatch reports whether every named plugin is an integration (af-integration.toml
// and zero top-level formulas). A name it cannot resolve is left to the formula pipeline, which
// owns those refusals.
func classifyInstallBatch(cmd *cobra.Command, args []string) (bool, error) {
	cwd, err := getWd()
	if err != nil {
		return false, err
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		return false, nil
	}
	var integrations, formulaPlugins []string
	for _, name := range args {
		if config.ValidateAgentName(name) != nil {
			return false, nil
		}
		dir := filepath.Join(config.PluginsDir(root), name)
		entries, err := os.ReadDir(dir)
		if err != nil {
			return false, nil
		}
		var formulas []string
		hasManifest := false
		for _, e := range entries {
			switch {
			case e.Name() == config.IntegrationManifestFile:
				hasManifest = true
			case strings.HasSuffix(e.Name(), ".formula.toml"):
				formulas = append(formulas, e.Name())
			}
		}
		switch {
		case hasManifest && len(formulas) > 0:
			return false, fmt.Errorf("plugin %q ships both %s and top-level formulas (%s); a plugin is an integration or a formula plugin, not both",
				name, config.IntegrationManifestFile, strings.Join(formulas, ", "))
		case hasManifest:
			integrations = append(integrations, name)
		default:
			formulaPlugins = append(formulaPlugins, name)
		}
	}
	if len(integrations) > 0 && len(formulaPlugins) > 0 {
		return false, fmt.Errorf("cannot install integrations (%s) and formula plugins (%s) in one command; install each kind separately",
			strings.Join(integrations, ", "), strings.Join(formulaPlugins, ", "))
	}
	if len(integrations) > 0 {
		return true, nil
	}

	if factoryWide, _ := cmd.Flags().GetBool("factory-wide"); factoryWide {
		return false, fmt.Errorf("--factory-wide applies only to scope = \"factory\" integrations; %s is a formula plugin", strings.Join(formulaPlugins, ", "))
	}
	// A corrupt record is left for the formula pipeline to report.
	if record, err := config.LoadPluginsConfig(config.PluginsConfigPath(root)); err == nil {
		for _, name := range formulaPlugins {
			if record.Plugins[name].Integration != nil {
				return false, fmt.Errorf("plugin %q is installed as an integration; run af plugin remove %s first", name, name)
			}
		}
	}
	return false, nil
}

// integrationToolchainNames are the bin/ names a [claude] plugin may not ship: its bin/ joins
// PATH ahead of the tools af and its agents run (the outline's list plus design S6's).
var integrationToolchainNames = []string{"af", "bash", "bd", "claude", "gh", "git", "jq", "python3", "sh", "tmux"}

// integrationPlan is one plugin of an integration batch, validated and ready to install.
type integrationPlan struct {
	info            pluginInfo
	manifest        *config.IntegrationManifest
	declared        []string
	claudePlugins   []string
	upstreamFetched bool
	consentFiles    map[string]string
}

// deferred reports whether rel lives in the [upstream] checkout the fetch has not produced yet
// (data.md L71 declares [claude] dirs there), so its checks wait for the post-fetch validation.
func (p *integrationPlan) deferred(rel string) bool {
	return p.manifest.Upstream != nil && !p.upstreamFetched &&
		(rel == config.IntegrationUpstreamDir || strings.HasPrefix(rel, config.IntegrationUpstreamDir+"/"))
}

func (p *integrationPlan) checkable(paths []string) []string {
	return slices.DeleteFunc(slices.Clone(paths), p.deferred)
}

// runIntegrationInstall is the integration-only install branch. It skips the formula pipeline's
// relink, source-tree guard, cwd check, rebuild (which reaches af down --all) and verify (B1):
// an integration adds no agent to the binary. The record is written last (step 10).
func runIntegrationInstall(cmd *cobra.Command, args []string) error {
	seen := map[string]bool{}
	for _, name := range args {
		if seen[name] {
			return fmt.Errorf("plugin %q is named more than once in this install; name each plugin once", name)
		}
		seen[name] = true
	}
	cwd, err := getWd()
	if err != nil {
		return err
	}
	if err := refuseWorktreeCwd(cwd, "af plugin install"); err != nil {
		return err
	}
	if err := requireOperator("plugin install"); err != nil {
		return err
	}
	root, err := resolveInvokerRoot(cwd)
	if err != nil {
		return err
	}
	factoryWide, _ := cmd.Flags().GetBool("factory-wide")

	u, err := buildCollisionUniverse(root)
	if err != nil {
		return err
	}
	record := u.manifest
	var plans []*integrationPlan
	for _, name := range args {
		pi, err := enumeratePlugin(root, name, u)
		if err != nil {
			return err
		}
		plans = append(plans, &integrationPlan{info: pi, manifest: pi.Manifest, declared: integrationDeclaredPaths(pi.Manifest)})
	}
	if err := validateIntegrationBatch(root, plans, record, factoryWide); err != nil {
		return err
	}
	printInstallSet(cmd, plansInfo(plans))
	fetched, err := fetchIntegrationUpstreams(cmd.Context(), plans)
	if err != nil {
		return err
	}
	if fetched {
		if err := validateIntegrationBatch(root, plans, record, factoryWide); err != nil {
			return err
		}
	}

	out := cmd.OutOrStdout()
	entries := map[string]config.PluginEntry{}
	for _, p := range plans {
		entry, err := installIntegration(cmd.Context(), cmd, root, p, factoryWide)
		if err != nil {
			return err
		}
		entries[p.info.Name] = entry
	}

	if record.Plugins == nil {
		record.Plugins = map[string]config.PluginEntry{}
	}
	maps.Copy(record.Plugins, entries)
	if err := config.SavePluginsConfig(config.PluginsConfigPath(root), record); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(entries)) {
		fmt.Fprintf(out, "installed integration %s -> %s\n", displaySafe(name), entries[name].Integration.SnapshotDir)
	}
	fmt.Fprintln(out, "a formula binds it by naming it in integrations or integrations_optional; a factory-wide integration reaches every agent session at its next launch")
	return nil
}

func plansInfo(plans []*integrationPlan) []pluginInfo {
	infos := make([]pluginInfo, 0, len(plans))
	for _, p := range plans {
		infos = append(infos, p.info)
	}
	return infos
}

// validateIntegrationBatch runs every refusal before the first write: scope consent, record
// conflicts, and each plugin's content rules and collisions.
func validateIntegrationBatch(root string, plans []*integrationPlan, record *config.PluginsConfig, factoryWide bool) error {
	var factory, formula []string
	for _, p := range plans {
		if p.manifest.Scope == config.IntegrationScopeFactory {
			factory = append(factory, p.info.Name)
		} else {
			formula = append(formula, p.info.Name)
		}
	}
	switch {
	case len(factory) > 0 && len(formula) > 0:
		return fmt.Errorf("cannot install %s (factory) and %s (formula) in one command; run: af plugin install %s --factory-wide; af plugin install %s",
			quotedList(factory), quotedList(formula), strings.Join(factory, " "), strings.Join(formula, " "))
	case len(factory) > 0 && !factoryWide:
		return fmt.Errorf("plugin %q declares scope = \"factory\"; binding a guard to every agent needs --factory-wide", factory[0])
	case len(formula) > 0 && factoryWide:
		return fmt.Errorf("--factory-wide applies only to scope = \"factory\" integrations; plugin %q is scope = \"formula\"", formula[0])
	}

	userScope, err := readClaudeUserScope()
	if err != nil {
		return fmt.Errorf("cannot check plugin names against the user scope: %w", err)
	}
	for _, p := range plans {
		if recorded := record.Plugins[p.info.Name]; len(recorded.Formulas) > 0 {
			var stems []string
			for key := range recorded.Formulas {
				stems = append(stems, strings.TrimSuffix(key, ".formula.toml"))
			}
			slices.Sort(stems)
			return fmt.Errorf("plugin %q was installed as a formula plugin (agents: %s) and now ships only %s; uninstall its formula agents first (af plugin remove does not uninstall formula plugins yet, #525)",
				p.info.Name, strings.Join(stems, ", "), config.IntegrationManifestFile)
		}
		declared := p.checkable(p.declared)
		if err := checkDeclaredContainment(p.info.Dir, declared); err != nil {
			return fmt.Errorf("plugin %q: %w", p.info.Name, err)
		}
		_, files, err := config.IntegrationContentHash(p.info.Dir, declared)
		if err != nil {
			return fmt.Errorf("plugin %q: %w", p.info.Name, err)
		}
		names, err := checkClaudePluginDirs(p.info.Dir, p.checkable(integrationClaudePluginPaths(p.manifest)))
		if err != nil {
			return fmt.Errorf("plugin %q: %w", p.info.Name, err)
		}
		p.claudePlugins, p.consentFiles = names, files
	}
	return checkIntegrationCollisions(plans, record, userScope)
}

func quotedList(names []string) string {
	q := make([]string, 0, len(names))
	for _, n := range names {
		q = append(q, fmt.Sprintf("%q", n))
	}
	return strings.Join(q, ", ")
}

// checkDeclaredContainment resolves every declared path's symlinks before the within-check
// (B9, H3-9): a lexically contained path can still point anywhere on the host.
func checkDeclaredContainment(dir string, declared []string) error {
	realDir, err := filepath.EvalSymlinks(dir)
	if err != nil {
		return err
	}
	for _, rel := range declared {
		real, err := filepath.EvalSymlinks(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return fmt.Errorf("declared path %q: %w", rel, err)
		}
		if r, err := filepath.Rel(realDir, real); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
			return fmt.Errorf("declared path %q resolves to %s, outside the plugin dir", rel, real)
		}
	}
	return nil
}

// checkClaudePluginDirs applies the [claude] plugin-dir content rules (spec L546-555) and returns
// the plugin.json names, sorted.
func checkClaudePluginDirs(dir string, plugins []string) ([]string, error) {
	names := []string{}
	for _, rel := range plugins {
		pdir := filepath.Join(dir, filepath.FromSlash(rel))
		entries, err := os.ReadDir(pdir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if n := e.Name(); n == "CLAUDE.md" || strings.HasSuffix(n, ".formula.toml") || strings.HasSuffix(n, ".md.tmpl") {
				return nil, fmt.Errorf("%s/%s: a [claude] plugin dir may not carry CLAUDE.md, *.formula.toml or *.md.tmpl: they would direct agents outside the plugin channel", rel, n)
			}
		}
		bins, err := os.ReadDir(filepath.Join(pdir, "bin"))
		if err != nil && !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		for _, b := range bins {
			if slices.Contains(integrationToolchainNames, b.Name()) {
				return nil, fmt.Errorf("%s/bin/%s would shadow the af toolchain; rename it", rel, b.Name())
			}
		}
		if raw, err := os.ReadFile(filepath.Join(pdir, "settings.json")); err == nil {
			var settings map[string]json.RawMessage
			if err := json.Unmarshal(raw, &settings); err != nil {
				return nil, fmt.Errorf("%s/settings.json: %w", rel, err)
			}
			if _, ok := settings["agent"]; ok {
				return nil, fmt.Errorf(`%s/settings.json sets "agent": a plugin may not replace the session identity (INV-2)`, rel)
			}
		} else if !errors.Is(err, fs.ErrNotExist) {
			return nil, err
		}
		name, err := checkClaudePluginJSON(rel, pdir)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	slices.Sort(names)
	return names, nil
}

func checkClaudePluginJSON(rel, pdir string) (string, error) {
	jsonRel := rel + "/.claude-plugin/plugin.json"
	raw, err := os.ReadFile(filepath.Join(pdir, ".claude-plugin", "plugin.json"))
	if err != nil {
		return "", fmt.Errorf("%s: %w (a [claude] plugin dir needs a plugin.json naming the plugin)", jsonRel, err)
	}
	var pj struct {
		Name           string                     `json:"name"`
		DefaultEnabled *bool                      `json:"defaultEnabled"`
		Settings       map[string]json.RawMessage `json:"settings"`
		UserConfig     map[string]map[string]any  `json:"userConfig"`
	}
	if err := json.Unmarshal(raw, &pj); err != nil {
		return "", fmt.Errorf("%s: %w", jsonRel, err)
	}
	if pj.Name == "" {
		return "", fmt.Errorf(`%s: missing "name"`, jsonRel)
	}
	if _, ok := pj.Settings["agent"]; ok {
		return "", fmt.Errorf(`%s sets "agent" in settings: a plugin may not replace the session identity (INV-2)`, jsonRel)
	}
	pluginDirOK := pj.DefaultEnabled == nil || *pj.DefaultEnabled
	for _, opt := range pj.UserConfig {
		if _, ok := opt["default"]; !ok {
			pluginDirOK = false
		}
	}
	if !pluginDirOK {
		return "", fmt.Errorf("%s: defaultEnabled must be true and every userConfig option needs a default under --plugin-dir", jsonRel)
	}
	return pj.Name, nil
}

// checkIntegrationCollisions refuses an env key, service session or Claude plugin name that
// another installed integration, another plugin of the batch, or a user-scope plugin already
// owns. A re-install of the same name excludes its own record.
func checkIntegrationCollisions(plans []*integrationPlan, record *config.PluginsConfig, userScope claudeUserScope) error {
	batch := map[string]bool{}
	for _, p := range plans {
		batch[p.info.Name] = true
	}
	envOwners, sessionOwners, pluginOwners := map[string][]string{}, map[string][]string{}, map[string][]string{}
	for name, entry := range record.Plugins {
		in := entry.Integration
		if in == nil || batch[name] {
			continue
		}
		for _, k := range in.EnvKeys {
			envOwners[k] = append(envOwners[k], name)
		}
		if in.Service != "" {
			sessionOwners[in.Service] = append(sessionOwners[in.Service], name)
		}
		for _, cp := range in.ClaudePlugins {
			pluginOwners[cp] = append(pluginOwners[cp], name)
		}
	}
	userPlugins := map[string]bool{}
	for _, up := range userScope.Plugins {
		userPlugins[up.Name] = true
	}

	for _, p := range plans {
		name := p.info.Name
		for _, k := range slices.Sorted(maps.Keys(p.manifest.Env)) {
			if owners := envOwners[k]; len(owners) > 0 {
				slices.Sort(owners)
				return fmt.Errorf("plugin %q: [env] %s is already set by integration %s", name, k, quotedList(owners))
			}
			envOwners[k] = append(envOwners[k], name)
		}
		if svc := p.manifest.Service; svc != nil {
			if owners := sessionOwners[svc.Session]; len(owners) > 0 {
				slices.Sort(owners)
				return fmt.Errorf("plugin %q: [service] session %q is already used by integration %s", name, svc.Session, quotedList(owners))
			}
			sessionOwners[svc.Session] = append(sessionOwners[svc.Session], name)
		}
		for _, cp := range p.claudePlugins {
			if userPlugins[cp] {
				return fmt.Errorf("plugin %q: plugin name %q is already provided by a user-scope plugin; rename it", name, cp)
			}
			if owners := pluginOwners[cp]; len(owners) > 0 {
				slices.Sort(owners)
				return fmt.Errorf("plugin %q: plugin name %q is already provided by integration %s; rename it", name, cp, quotedList(owners))
			}
			pluginOwners[cp] = append(pluginOwners[cp], name)
		}
	}
	return nil
}

// installIntegration runs pipeline steps 3-9 for one plugin and returns its record entry.
func installIntegration(ctx context.Context, cmd *cobra.Command, root string, p *integrationPlan, factoryWide bool) (config.PluginEntry, error) {
	name, dir, m := p.info.Name, p.info.Dir, p.manifest
	out := cmd.OutOrStdout()
	if ctx == nil {
		ctx = context.Background()
	}
	installTimeout, err := integrationInstallTimeout(m)
	if err != nil {
		return config.PluginEntry{}, fmt.Errorf("plugin %q: %w", name, err)
	}
	if m.Upstream != nil && !p.upstreamFetched {
		return config.PluginEntry{}, fmt.Errorf("plugin %q: [upstream] was not fetched and validated before install", name)
	}

	hashes := map[string]string{}
	artifacts := []config.IntegrationArtifact{}
	externalWrites := []string{}
	if m.Install != nil {
		externalWrites = append(externalWrites, m.Install.ExternalWrites...)
		artifacts = append(artifacts, m.Install.Artifacts...)
		if integrationSharedAdoptable(m.Install) {
			fmt.Fprintf(out, "plugin %s: adopted the existing host install (shared = true and every external write already exists); [install] run skipped\n", displaySafe(name))
		} else {
			if err := requireConsentedBytes(p); err != nil {
				return config.PluginEntry{}, fmt.Errorf("plugin %q: %w", name, err)
			}
			if err := runIntegrationInstallScript(ctx, root, dir, m, installTimeout); err != nil {
				return config.PluginEntry{}, fmt.Errorf("plugin %q: %w", name, err)
			}
		}
		if hashes, err = verifyIntegrationExternalWrites(m.Install); err != nil {
			return config.PluginEntry{}, fmt.Errorf("plugin %q: %w", name, err)
		}
	}

	parent := filepath.Join(config.IntegrationsDir(root), name)
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return config.PluginEntry{}, fmt.Errorf("plugin %q: staging: %w", name, err)
	}
	stage, err := os.MkdirTemp(parent, ".tmp-")
	if err != nil {
		return config.PluginEntry{}, fmt.Errorf("plugin %q: staging: %w", name, err)
	}
	sum, files, err := stageIntegration(dir, stage, m, p.declared, p.claudePlugins)
	if err != nil {
		_ = os.RemoveAll(stage)
		return config.PluginEntry{}, fmt.Errorf("plugin %q: staging: %w", name, err)
	}
	snap := filepath.Join(parent, sum)
	if _, err := os.Lstat(snap); err == nil {
		// A <sha>/ only ever comes from a renamed, fully hashed stage (D37).
		if err := os.RemoveAll(stage); err != nil {
			return config.PluginEntry{}, fmt.Errorf("plugin %q: removing stage: %w", name, err)
		}
	} else {
		if err := os.Rename(stage, snap); err != nil {
			_ = os.RemoveAll(stage)
			return config.PluginEntry{}, fmt.Errorf("plugin %q: staging: %w", name, err)
		}
		if err := chmodTreeReadOnly(snap); err != nil {
			return config.PluginEntry{}, fmt.Errorf("plugin %q: sealing snapshot: %w", name, err)
		}
	}

	manifestBytes, err := os.ReadFile(filepath.Join(snap, config.IntegrationManifestFile))
	if err != nil {
		return config.PluginEntry{}, fmt.Errorf("plugin %q: %w", name, err)
	}
	relSnap, err := filepath.Rel(root, snap)
	if err != nil {
		return config.PluginEntry{}, err
	}
	now := time.Now().UTC().Format(time.RFC3339)
	in := &config.PluginIntegration{
		ManifestSHA256:      hashHex(manifestBytes),
		ContentSHA256:       sum,
		Files:               files,
		Scope:               m.Scope,
		FactoryWide:         factoryWide && m.Scope == config.IntegrationScopeFactory,
		ClaudePlugins:       p.claudePlugins,
		EnvKeys:             slices.Sorted(maps.Keys(m.Env)),
		ExternalWrites:      externalWrites,
		ExternalWriteHashes: hashes,
		Artifacts:           artifacts,
		Source:              "clone",
		SnapshotDir:         relSnap,
		StagedAt:            now,
	}
	if in.EnvKeys == nil {
		in.EnvKeys = []string{}
	}
	if p.info.Embedded {
		in.Source = acquireSourceEmbedded
	}
	if m.Upstream != nil {
		in.UpstreamRepo, in.UpstreamCommit = redactRemoteURL(m.Upstream.Repo), m.Upstream.Commit
	}
	if m.Service != nil {
		in.Service, in.ServiceProbe = m.Service.Session, m.Service.Probe
	}
	if m.Claude != nil {
		in.HookFailMode = m.Claude.HookFailMode
	}
	return config.PluginEntry{Source: p.info.Source, Commit: p.info.Commit, InstalledAt: now, Integration: in}, nil
}

func integrationInstallTimeout(m *config.IntegrationManifest) (time.Duration, error) {
	if m.Install != nil {
		return config.ParseIntegrationDuration(m.Install.Timeout)
	}
	return config.ParseIntegrationDuration(config.DefaultIntegrationInstallTimeout)
}

// fetchIntegrationUpstreams fetches every plan's [upstream] pin and reports whether any was
// fetched: the batch must then be validated again, now covering the .upstream/ declared paths.
func fetchIntegrationUpstreams(ctx context.Context, plans []*integrationPlan) (bool, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	fetched := false
	for _, p := range plans {
		if p.manifest.Upstream == nil {
			continue
		}
		timeout, err := integrationInstallTimeout(p.manifest)
		if err != nil {
			return false, fmt.Errorf("plugin %q: %w", p.info.Name, err)
		}
		if err := fetchIntegrationUpstream(ctx, filepath.Join(p.info.Dir, config.IntegrationUpstreamDir), p.manifest.Upstream, timeout); err != nil {
			return false, fmt.Errorf("plugin %q: %w", p.info.Name, err)
		}
		p.upstreamFetched, fetched = true, true
	}
	return fetched, nil
}

// requireConsentedBytes re-hashes the acquisition dir right before [install] run executes from
// it: a file changed since validation would run bytes nobody checked.
func requireConsentedBytes(p *integrationPlan) error {
	if p.consentFiles == nil {
		return errors.New("[install] run refused: the plugin was not validated before install")
	}
	_, files, err := config.IntegrationContentHash(p.info.Dir, p.declared)
	if err != nil {
		return err
	}
	var changed []string
	for rel, sum := range files {
		if p.consentFiles[rel] != sum {
			changed = append(changed, rel)
		}
	}
	for rel := range p.consentFiles {
		if _, ok := files[rel]; !ok {
			changed = append(changed, rel)
		}
	}
	if len(changed) > 0 {
		slices.Sort(changed)
		return fmt.Errorf("[install] run refused: %s changed after validation; nothing was run, staged or recorded. Re-run af plugin install", strings.Join(changed, ", "))
	}
	return nil
}

// fetchIntegrationUpstream checks the pinned upstream commit out into up (data.md D1), reusing a
// checkout already at the pin (D18). Every printed URL is redacted: the manifest's may carry a token.
func fetchIntegrationUpstream(ctx context.Context, up string, pin *config.IntegrationUpstream, timeout time.Duration) error {
	redacted := redactRemoteURL(pin.Repo)
	if isOwnGitToplevel(up) && gitOut(up, "rev-parse", "HEAD") == pin.Commit {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var sshCommand string
	git := func(dir string, args ...string) error {
		c := exec.CommandContext(ctx, "git", args...)
		c.Dir = dir
		for _, kv := range os.Environ() {
			if !slices.ContainsFunc(gitOutScrubbedEnv, func(k string) bool { return strings.HasPrefix(kv, k+"=") }) {
				c.Env = append(c.Env, kv)
			}
		}
		c.Env = append(c.Env, "GIT_TERMINAL_PROMPT=0")
		if sshCommand != "" {
			c.Env = append(c.Env, "GIT_SSH_COMMAND="+sshCommand)
		}
		c.Stdin = nil
		c.WaitDelay = integrationWaitDelay
		b, err := c.CombinedOutput()
		if err != nil {
			tail := strings.ReplaceAll(integrationTail(string(b), integrationOutputLines), pin.Repo, redacted)
			return fmt.Errorf("fetching upstream %s at %s: git %s: %v\n%s", redacted, pin.Commit, args[0], err, redactIntegrationOutput(tail, nil))
		}
		return nil
	}
	if err := os.RemoveAll(up); err != nil {
		return err
	}
	if err := os.MkdirAll(up, 0o755); err != nil {
		return err
	}
	if err := git(up, "init", "-q"); err != nil {
		return err
	}
	// Resolved inside the new repository so core.sshCommand is read exactly as the fetch reads it.
	sshCommand = batchModeSSHCommand(up)
	if err := git(up, "fetch", "-q", "--depth", "1", pin.Repo, pin.Commit); err != nil {
		return err
	}
	if err := git(up, "checkout", "-q", "--detach", "FETCH_HEAD"); err != nil {
		return err
	}
	if head := gitOut(up, "rev-parse", "HEAD"); head != pin.Commit {
		return fmt.Errorf("upstream %s: pinned commit %s checks out as %s; pin the commit sha itself", redacted, pin.Commit, head)
	}
	return nil
}

// batchModeSSHCommand is the ssh command git would run in dir, in git's own precedence, with BatchMode
// added. ssh asks for passwords and unknown host keys on the terminal, not stdin, so neither a closed
// stdin nor GIT_TERMINAL_PROMPT=0 stops it; the operator's own program and options are kept.
func batchModeSSHCommand(dir string) string {
	base := os.Getenv("GIT_SSH_COMMAND")
	if base == "" {
		base = gitOut(dir, "config", "--get", "core.sshCommand")
	}
	if base == "" && os.Getenv("GIT_SSH") != "" {
		base = shellQuote(os.Getenv("GIT_SSH"))
	}
	if base == "" {
		base = "ssh"
	}
	return base + " -o BatchMode=yes"
}

// integrationSharedAdoptable reports whether a shared = true install is already on this host:
// every declared regular-file external write exists (D4: an empty set never adopts).
func integrationSharedAdoptable(in *config.IntegrationInstall) bool {
	if !in.Shared {
		return false
	}
	files := 0
	for _, w := range in.ExternalWrites {
		if strings.HasSuffix(w, "/") {
			continue
		}
		files++
		info, err := os.Stat(expandHomePath(w))
		if err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	return files > 0
}

func runIntegrationInstallScript(ctx context.Context, root, dir string, m *config.IntegrationManifest, timeout time.Duration) error {
	// A secret staged only inside the future snapshot cannot be read yet, and so cannot leak yet.
	secrets, _ := integrationSecretValues(root, m)
	res := runIntegrationScript(ctx, dir, m.Install.Run, timeout)
	if res.exit == 0 {
		return nil
	}
	var why string
	switch {
	case res.timedOut:
		why = fmt.Sprintf("timed out after %s", timeout)
	case res.startErr != nil:
		why = fmt.Sprintf("could not start: %v", res.startErr)
	default:
		why = fmt.Sprintf("exited %d", res.exit)
	}
	return fmt.Errorf("[install] run %s %s; nothing was staged or recorded. Last %d lines:\n%s",
		m.Install.Run, why, integrationOutputLines, redactIntegrationOutput(integrationTail(res.output, integrationOutputLines), secrets))
}

// verifyIntegrationExternalWrites checks every declared external write exists after [install],
// hashes the regular files, and requires every declared artifact's sha256 to match one of them
// (D3: every, fail-closed).
func verifyIntegrationExternalWrites(in *config.IntegrationInstall) (map[string]string, error) {
	hashes := map[string]string{}
	for _, w := range in.ExternalWrites {
		p := expandHomePath(w)
		info, err := os.Stat(p)
		if err != nil {
			return nil, fmt.Errorf("[install] declared external write %s was not written: %w", w, err)
		}
		if strings.HasSuffix(w, "/") {
			if !info.IsDir() {
				return nil, fmt.Errorf("[install] declared external write %s is not a directory", w)
			}
			continue
		}
		if !info.Mode().IsRegular() {
			return nil, fmt.Errorf("[install] declared external write %s is not a regular file", w)
		}
		sum, err := config.FileSHA256(p)
		if err != nil {
			return nil, err
		}
		hashes[w] = sum
	}
	for _, a := range in.Artifacts {
		if !slices.Contains(slices.Collect(maps.Values(hashes)), a.SHA256) {
			return nil, fmt.Errorf("[install] artifact %s (sha256 %s) matches no declared external write", a.URL, a.SHA256)
		}
	}
	return hashes, nil
}

// expandHomePath resolves a ~/-relative external write against HOME.
func expandHomePath(w string) string {
	rest, ok := strings.CutPrefix(w, "~/")
	if !ok {
		return w
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return w
	}
	return filepath.Join(home, rest)
}

// stageIntegration copies the consent set (manifest + declared paths, D47) into stage keeping each
// file's exec bit, normalised as the content hash does, with the exec rule (D6) as a floor, re-runs
// the content rules on the copy (TOCTOU), and hashes it. The staged plugin.json names must equal
// the validated ones: only those passed the collision check.
func stageIntegration(dir, stage string, m *config.IntegrationManifest, declared, claudePlugins []string) (string, map[string]string, error) {
	for _, root := range append([]string{config.IntegrationManifestFile}, declared...) {
		src := filepath.Join(dir, filepath.FromSlash(root))
		err := filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			relOS, _ := filepath.Rel(dir, p)
			rel := filepath.ToSlash(relOS)
			dst := filepath.Join(stage, relOS)
			switch {
			case d.IsDir():
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return os.MkdirAll(dst, 0o755)
			case !d.Type().IsRegular():
				return fmt.Errorf("%s is not a regular file", rel)
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				return err
			}
			mode := os.FileMode(0o644)
			if info.Mode().Perm()&0o111 != 0 || integrationExecPath(m, rel) {
				mode = 0o755
			}
			if err := os.WriteFile(dst, b, mode); err != nil {
				return err
			}
			return os.Chmod(dst, mode)
		})
		if err != nil {
			return "", nil, err
		}
	}
	names, err := checkClaudePluginDirs(stage, integrationClaudePluginPaths(m))
	if err != nil {
		return "", nil, err
	}
	if !slices.Equal(names, claudePlugins) {
		return "", nil, fmt.Errorf("[claude] plugin names changed after validation: staged %s, validated %s; re-run af plugin install", quotedList(names), quotedList(claudePlugins))
	}
	return config.IntegrationContentHash(stage, declared)
}

// chmodTreeReadOnly seals a snapshot (chmod -R a-w): the consented copy is not edited in place.
func chmodTreeReadOnly(root string) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			dirs = append(dirs, p)
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		return os.Chmod(p, info.Mode().Perm()&^0o222)
	})
	if err != nil {
		return err
	}
	// Deepest first: a parent sealed before its children would refuse their chmod.
	for i := len(dirs) - 1; i >= 0; i-- {
		if err := os.Chmod(dirs[i], 0o555); err != nil {
			return err
		}
	}
	return nil
}

func integrationDeclaredPaths(m *config.IntegrationManifest) []string {
	return config.IntegrationDeclaredPaths(m)
}

func integrationClaudePluginPaths(m *config.IntegrationManifest) []string {
	if m.Claude == nil {
		return nil
	}
	return cleanPaths(m.Claude.Plugins)
}
