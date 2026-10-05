package cmd

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// Blind review iteration 1 pins: a declared path may live under .upstream/ (data.md L71, the AWS
// example), so it can only be checked after the fetch; and the bytes [install] run executes must
// be the bytes that were checked.

const intBUpstreamPluginName = "acme-up-plugin"

// intBUpstreamPluginRepo builds a local upstream repo carrying plugins/up (a [claude] plugin dir),
// optionally with a toolchain-shadowing bin/git, and routes intBUpstreamCredURL to it.
func intBUpstreamPluginRepo(t *testing.T, e *intBEnv, shadowGit bool) string {
	t.Helper()
	repo := filepath.Join(e.base, "upstream-plugin-src")
	files := map[string]intBFile{
		"plugins/up/.claude-plugin/plugin.json": {`{"name":"` + intBUpstreamPluginName + `","version":"0.1.0"}` + "\n", 0o644},
		"plugins/up/skills/up/SKILL.md":         {"---\nname: up\ndescription: upstream skill\n---\nbody\n", 0o644},
	}
	if shadowGit {
		files["plugins/up/bin/git"] = intBFile{"#!/bin/sh\necho not-git\n", 0o755}
	}
	intBWriteTree(t, repo, files)
	intBGit(t, repo, "init", "-q")
	intBGit(t, repo, "add", "-A")
	intBGit(t, repo, "commit", "-q", "-m", "upstream")
	commit := intBGit(t, repo, "rev-parse", "HEAD")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "url.file://"+repo+".insteadOf")
	t.Setenv("GIT_CONFIG_VALUE_0", intBUpstreamCredURL)
	return commit
}

func intBUpstreamDeclaredSource(e *intBEnv, commit string) map[string]intBFile {
	files := intBSource(e, intBName, intBUpstreamManifest(commit))
	m := files[config.IntegrationManifestFile]
	m.body = strings.Replace(m.body, `plugins = ["claude-plugin"]`, `plugins = ["claude-plugin", ".upstream/plugins/up"]`, 1)
	files[config.IntegrationManifestFile] = m
	return files
}

func TestPluginInstall_IntegrationUpstreamDeclaredPlugin(t *testing.T) {
	t.Run("first_install_seals_the_upstream_plugin_dir", func(t *testing.T) {
		e := intBFactory(t)
		commit := intBUpstreamPluginRepo(t, e, false)
		dir := intBAcquire(t, e, intBName, intBUpstreamDeclaredSource(e, commit))
		if !strings.Contains(intBMustLoadManifest(t, dir).Claude.Plugins[1], ".upstream/") {
			t.Fatal("fixture: the manifest must declare a [claude] dir under .upstream/")
		}

		out, err := runPlugin(t, "install", nil, intBName)
		if err != nil {
			t.Fatalf("a declared [claude] dir under .upstream/ must install on the first try (it exists only after the fetch); got: %v\n%s", err, out)
		}
		in := intBLoadEntry(t, e.root, intBName).Integration
		if in == nil {
			t.Fatal("no integration recorded")
		}
		if !slices.Contains(in.ClaudePlugins, intBUpstreamPluginName) {
			t.Errorf("claude_plugins = %v, want it to include the upstream plugin %q", in.ClaudePlugins, intBUpstreamPluginName)
		}
		const rel = ".upstream/plugins/up/.claude-plugin/plugin.json"
		if _, ok := in.Files[rel]; !ok {
			t.Errorf("files has no %s: the upstream plugin dir was not hashed", rel)
		}
		if !intBExists(filepath.Join(e.root, in.SnapshotDir, filepath.FromSlash(rel))) {
			t.Errorf("the snapshot does not carry %s", rel)
		}
		intBWantSeamsUntouched(t, e)
	})

	t.Run("upstream_dir_content_rules_run_before_install_run", func(t *testing.T) {
		e := intBFactory(t)
		commit := intBUpstreamPluginRepo(t, e, true)
		intBAcquire(t, e, intBName, intBUpstreamDeclaredSource(e, commit))
		pluginsBefore := intBSeedPluginsJSON(t, e)

		out, err := runPlugin(t, "install", nil, intBName)
		if err == nil {
			t.Fatalf("an upstream [claude] dir shipping bin/git must be refused; output:\n%s", out)
		}
		if !strings.Contains(err.Error(), "bin/git would shadow the af toolchain") {
			t.Errorf("want the toolchain-shadow refusal for the upstream dir; got: %v", err)
		}
		if intBExists(e.ext) {
			t.Error("[install] run executed although the fetched upstream content breaks the content rules")
		}
		intBWantNoStoreWrite(t, e, intBName, pluginsBefore)
	})
}

func TestPluginInstall_IntegrationConsentChangedBeforeRun(t *testing.T) {
	e := intBFactory(t)
	dir := intBAcquire(t, e, intBName, intBSource(e, intBName, intBManifestOpts{}))

	u, err := buildCollisionUniverse(e.root)
	if err != nil {
		t.Fatal(err)
	}
	pi, err := enumeratePlugin(e.root, intBName, u)
	if err != nil {
		t.Fatal(err)
	}
	p := &integrationPlan{info: pi, manifest: pi.Manifest, declared: integrationDeclaredPaths(pi.Manifest)}
	if err := validateIntegrationBatch(e.root, []*integrationPlan{p}, u.manifest, false); err != nil {
		t.Fatalf("fixture must validate: %v", err)
	}

	marker := filepath.Join(e.base, "swapped-script-ran")
	if err := os.WriteFile(filepath.Join(dir, "af", "install.sh"), []byte("#!/bin/sh\ntouch '"+marker+"'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})

	_, err = installIntegration(context.Background(), cmd, e.root, p, false)
	if err == nil {
		t.Fatal("[install] run must be refused when the acquisition dir changed after validation")
	}
	if !strings.Contains(err.Error(), "af/install.sh") {
		t.Errorf("the refusal must name the changed file af/install.sh: %v", err)
	}
	if intBExists(marker) {
		t.Error("the changed install script ran")
	}
	if parent := filepath.Join(config.IntegrationsDir(e.root), intBName); intBExists(parent) {
		t.Errorf("nothing may be staged after a refused run, but %s exists", parent)
	}
}

// Spec pipeline order: step 2 prints the install set, step 3 fetches .upstream/. A pin the upstream
// does not carry fails the fetch, which must come after the set was shown.
func TestPluginInstall_IntegrationInstallSetPrintedBeforeUpstreamFetch(t *testing.T) {
	e := intBFactory(t)
	intBUpstreamRepo(t, e)
	intBAcquire(t, e, intBName, intBSource(e, intBName, intBUpstreamManifest(strings.Repeat("0", 40))))

	out, err := runPlugin(t, "install", nil, intBName)
	if err == nil || !strings.Contains(err.Error(), "fetching upstream") {
		t.Fatalf("want the upstream fetch to fail for a pin the upstream lacks; got: %v\n%s", err, out)
	}
	if !strings.Contains(out, "installing 1 plugin(s):") {
		t.Errorf("the install set must be printed (step 2) before the .upstream/ fetch (step 3); output:\n%s", out)
	}
}

// Spec: the content rules, the plugin.json name collision among them, run again against the staged
// copy. [install] run executes after validation, so it can still rename the plugin; the collision
// check covered only the validated names.
func TestPluginInstall_IntegrationStagedPluginNameChangedByInstallRun(t *testing.T) {
	e := intBFactory(t)
	files := intBSource(e, intBName, intBManifestOpts{})
	script := files["af/install.sh"]
	script.body += "printf '{\"name\":\"renamed-plugin\",\"version\":\"0.1.0\"}\\n' > claude-plugin/.claude-plugin/plugin.json\n"
	files["af/install.sh"] = script
	intBAcquire(t, e, intBName, files)
	pluginsBefore := intBSeedPluginsJSON(t, e)

	out, err := runPlugin(t, "install", nil, intBName)
	if err == nil {
		t.Fatalf("a plugin.json name changed by [install] run must be refused at staging; output:\n%s", out)
	}
	if !strings.Contains(err.Error(), "renamed-plugin") || !strings.Contains(err.Error(), intBName+"-plugin") {
		t.Errorf("the refusal must name the staged and the validated plugin names: %v", err)
	}
	intBWantNoStoreWrite(t, e, intBName, pluginsBefore)
}
