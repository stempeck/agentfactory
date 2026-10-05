package config

import (
	"fmt"
	"maps"
	"path"
	"path/filepath"
	"slices"
)

// IntegrationBinding is one integration an agent session binds: the consumed snapshot it reads and what it
// contributes to the launch line.
type IntegrationBinding struct {
	Name, Scope, SnapshotDir, ContentSHA256, HookFailMode string
	Required                                              bool
	PluginDirs                                            []string
	Env                                                   []EnvVar
	ClaudePlugins                                         []string
}

// IntegrationDeclaredPaths is the declared set hashed and staged besides the manifest (D14, D47): the run
// scripts and the [claude] plugin dirs. Install and every re-hash share this one definition, so a snapshot
// that verified at install cannot read as drifted at launch.
func IntegrationDeclaredPaths(m *IntegrationManifest) []string {
	var declared []string
	if m.Install != nil {
		declared = append(declared, m.Install.Run)
	}
	if m.Check != nil {
		declared = append(declared, m.Check.Run)
	}
	if m.Service != nil {
		declared = append(declared, m.Service.Run)
	}
	if m.Claude != nil {
		declared = append(declared, m.Claude.Plugins...)
	}
	for i, p := range declared {
		declared[i] = path.Clean(p)
	}
	slices.Sort(declared)
	return slices.Compact(declared)
}

// ResolveIntegrationBindings computes the integrations a session binds from recorded consent alone: every
// factory-scope integration, every required name, and every optional name whose snapshot is intact. It reads
// only the consumed snapshots under IntegrationsDir (never store/plugins, ADR-025 §1) and runs no
// subprocess; a candidate that is missing, outside IntegrationsDir or drifted is dropped with a report, never
// bound from other content. envKeyUniverse is every [env] key any installed integration may export, so the
// launch line can clear the keys of integrations this session does not bind.
func ResolveIntegrationBindings(root string, manifest *PluginsConfig, required, optional []string) (bound []IntegrationBinding, envKeyUniverse []string, reports []string, err error) {
	if manifest == nil {
		return nil, nil, nil, fmt.Errorf("resolving integration bindings: no plugins config")
	}
	envKeyUniverse = IntegrationEnvKeyUniverse(manifest)
	isRequired := map[string]bool{}
	candidates := map[string]bool{}
	for _, name := range FactoryScopeIntegrations(manifest) {
		candidates[name] = true
	}
	for _, name := range required {
		candidates[name] = true
		isRequired[name] = true
	}
	for _, name := range optional {
		candidates[name] = true
	}

	for _, name := range slices.Sorted(maps.Keys(candidates)) {
		e, ok := manifest.Plugins[name]
		if !ok || e.Integration == nil {
			reports = append(reports, fmt.Sprintf("integration %q not bound: not installed", name))
			continue
		}
		b, err := BindIntegration(root, name, e.Integration)
		if err != nil {
			reports = append(reports, err.Error())
			continue
		}
		b.Required = isRequired[name]
		for _, kv := range b.Env {
			envKeyUniverse = append(envKeyUniverse, kv.Key)
		}
		bound = append(bound, b)
	}
	slices.Sort(envKeyUniverse)
	return bound, slices.Compact(envKeyUniverse), reports, nil
}

// FactoryScopeIntegrations names, sorted, the installed integrations that bind without a formula declaring
// them.
func FactoryScopeIntegrations(manifest *PluginsConfig) []string {
	var names []string
	for name, e := range manifest.Plugins {
		if e.Integration != nil && (e.Integration.FactoryWide || e.Integration.Scope == IntegrationScopeFactory) {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

// IntegrationEnvKeyUniverse is every [env] key an installed integration recorded, so a launch can clear the
// keys of the integrations it does not bind without re-hashing their snapshots.
func IntegrationEnvKeyUniverse(manifest *PluginsConfig) []string {
	var keys []string
	for _, e := range manifest.Plugins {
		if e.Integration != nil {
			keys = append(keys, e.Integration.EnvKeys...)
		}
	}
	slices.Sort(keys)
	return slices.Compact(keys)
}

// IntegrationBindError says why a recorded integration cannot be bound. Drifted means the snapshot is
// present but its content no longer hashes to what was consented; any other failure leaves no snapshot
// af may bind.
type IntegrationBindError struct {
	Name    string
	Drifted bool
	Changed int
	Msg     string
}

func (e *IntegrationBindError) Error() string { return e.Msg }

// BindIntegration binds one recorded integration from its consumed snapshot after re-hashing it.
func BindIntegration(root, name string, in *PluginIntegration) (IntegrationBinding, error) {
	snap, m, err := VerifiedIntegrationSnapshot(root, name, in)
	if err != nil {
		return IntegrationBinding{}, err
	}

	b := IntegrationBinding{
		Name:          name,
		Scope:         in.Scope,
		SnapshotDir:   in.SnapshotDir,
		ContentSHA256: in.ContentSHA256,
		ClaudePlugins: in.ClaudePlugins,
	}
	if m.Claude != nil {
		b.HookFailMode = m.Claude.HookFailMode
		for _, p := range m.Claude.Plugins {
			b.PluginDirs = append(b.PluginDirs, filepath.Join(snap, filepath.FromSlash(path.Clean(p))))
		}
	}
	for _, k := range slices.Sorted(maps.Keys(m.Env)) {
		b.Env = append(b.Env, EnvVar{Key: k, Value: m.Env[k]})
	}
	return b, nil
}

// VerifiedIntegrationSnapshot re-hashes one recorded integration's consumed snapshot and returns it with the
// manifest that was hashed, so whatever af runs from it is the content that was consented.
func VerifiedIntegrationSnapshot(root, name string, in *PluginIntegration) (string, *IntegrationManifest, error) {
	snap, ok := IntegrationSnapshotPath(root, name, in.ContentSHA256, in.SnapshotDir)
	if !ok {
		return "", nil, &IntegrationBindError{Name: name, Msg: fmt.Sprintf("integration %q not bound: snapshot_dir %q is not its content-addressed snapshot under %s", name, in.SnapshotDir, IntegrationsDir(root))}
	}
	m, err := LoadIntegrationManifestNamed(snap, name)
	if err != nil {
		return "", nil, &IntegrationBindError{Name: name, Msg: fmt.Sprintf("integration %q not bound: snapshot missing or unreadable (%v; run af plugin verify %s)", name, err, name)}
	}
	sum, files, err := IntegrationContentHash(snap, IntegrationDeclaredPaths(m))
	if err != nil {
		return "", nil, &IntegrationBindError{Name: name, Msg: fmt.Sprintf("integration %q not bound: snapshot cannot be hashed (%v; run af plugin verify %s)", name, err, name)}
	}
	if sum != in.ContentSHA256 {
		// Without per-file hashes af cannot name what changed, only how much there is (D33).
		changed := len(files)
		if in.Files != nil {
			changed = changedIntegrationFiles(in.Files, files)
		}
		return "", nil, &IntegrationBindError{Name: name, Drifted: true, Changed: changed,
			Msg: fmt.Sprintf("integration %q not bound: content changed since consent (%d files; run af plugin verify %s)", name, changed, name)}
	}
	return snap, m, nil
}

// IntegrationSnapshotPath resolves a recorded root-relative snapshot_dir and accepts it only when it is the
// content-addressed IntegrationsDir/<name>/<sha> of that integration, so a forged or stale record cannot
// point a session at other content. The name and sha are checked first because the equality alone accepts a
// traversal name/sha pair that joins to the same place outside IntegrationsDir.
func IntegrationSnapshotPath(root, name, sha, snapshotDir string) (string, bool) {
	if ValidateAgentName(name) != nil || !artifactSHA256.MatchString(sha) || snapshotDir == "" || filepath.IsAbs(snapshotDir) {
		return "", false
	}
	want := filepath.Join(IntegrationsDir(root), name, sha)
	if filepath.Join(root, filepath.FromSlash(snapshotDir)) != want {
		return "", false
	}
	return want, true
}

func changedIntegrationFiles(recorded, current map[string]string) int {
	n := 0
	for rel, line := range current {
		if recorded[rel] != line {
			n++
		}
	}
	for rel := range recorded {
		if _, ok := current[rel]; !ok {
			n++
		}
	}
	return n
}
