package cmd

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// recordFactoryIntegration consumes a service-less factory-scope integration into root the way install
// records one: a read-only snapshot named by its content hash, plus a v2 plugins.json entry. Its [env] key is
// deliberately unrelated to its hyphenated name, so any key derived from the name is af's own.
func recordFactoryIntegration(t *testing.T, root, name, envKey, envVal string) string {
	t.Helper()
	parent := filepath.Join(config.IntegrationsDir(root), name)
	stage := filepath.Join(parent, ".tmp-fixture")
	manifest := "name = \"" + name + "\"\ndescription = \"factory fixture\"\nscope = \"factory\"\n" +
		"\n[install]\nrun = \"af/install.sh\"\ntimeout = \"30s\"\nexternal_writes = [\"" + filepath.Join(root, "ext", name) + "\"]\n" +
		"\n[claude]\nplugins = [\"claude-plugin\"]\n" +
		"\n[env]\n" + envKey + " = \"" + envVal + "\"\n"
	intBWriteTree(t, stage, map[string]intBFile{
		config.IntegrationManifestFile:               {manifest, 0o644},
		"af/install.sh":                              {"#!/bin/sh\nexit 0\n", 0o755},
		"claude-plugin/.claude-plugin/plugin.json":   {`{"name":"` + name + `-plugin","version":"0.1.0"}` + "\n", 0o644},
		"claude-plugin/skills/" + name + "/SKILL.md": {"---\nname: " + name + "\ndescription: fixture\n---\nbody\n", 0o644},
	})
	sum, hashed, err := config.IntegrationContentHash(stage, intBDeclared)
	if err != nil {
		t.Fatalf("hash fixture: %v", err)
	}
	snap := filepath.Join(parent, sum)
	if err := os.Rename(stage, snap); err != nil {
		t.Fatal(err)
	}
	if err := intBChmodTree(snap, false); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = intBChmodTree(snap, true) })
	rel, _ := filepath.Rel(root, snap)
	intBSaveEntry(t, root, name, config.PluginEntry{
		Source: "https://example.invalid/" + name + ".git", Commit: intBFakeCommit, InstalledAt: "2026-09-28T00:00:00Z",
		Integration: &config.PluginIntegration{
			ManifestSHA256: intBSHA([]byte(manifest)),
			ContentSHA256:  sum,
			Files:          hashed,
			Scope:          "factory",
			ClaudePlugins:  []string{name + "-plugin"},
			EnvKeys:        []string{envKey},
			HookFailMode:   "open",
			ExternalWrites: []string{filepath.Join(root, "ext", name)},
			Artifacts:      []config.IntegrationArtifact{},
			Source:         "clone",
			SnapshotDir:    filepath.ToSlash(rel),
			StagedAt:       "2026-09-28T00:00:00Z",
		},
	})
	return snap
}

func TestLaunchContributionsParity_Integrations(t *testing.T) {
	fx := newK14Fixture(t)
	snap := recordFactoryIntegration(t, fx.root, "fix-ture", "FIXTURE_TOKEN", "t")
	pluginDir := filepath.Join(snap, "claude-plugin")
	fake, _ := setupHermeticSessions(t)

	u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, false)
	k14AssertParity(t, u.line, s.line, r.line)

	for name, l := range map[string]k14Launch{"up": u, "sling": s, "respawn": r} {
		if l.line == "" {
			t.Errorf("%s: nothing launched (err=%v)\n%s", name, l.err, l.output)
			continue
		}
		run := fx.exec(t, l.line)
		var dirs []string
		prompt := -1
		for i, a := range run.args {
			if a == "--plugin-dir" && i+1 < len(run.args) {
				dirs = append(dirs, run.args[i+1])
			}
			if a == "af prime" {
				prompt = i
			}
		}
		if len(dirs) != 1 || dirs[0] != pluginDir {
			t.Errorf("%s: --plugin-dir args = %q, want exactly [%s]", name, dirs, pluginDir)
		}
		if prompt >= 0 && len(dirs) > 0 && slices.Index(run.args, "--plugin-dir") > prompt {
			t.Errorf("%s: --plugin-dir must precede the initial prompt; args=%q", name, run.args)
		}
		if run.env["FIXTURE_TOKEN"] != "t" {
			t.Errorf("%s: the launched claude saw FIXTURE_TOKEN=%q, want %q", name, run.env["FIXTURE_TOKEN"], "t")
		}
		if !strings.Contains(run.env["AF_INTEGRATION_HOOK_FAIL_MODES"], "fix-ture=") {
			t.Errorf("%s: AF_INTEGRATION_HOOK_FAIL_MODES=%q, want it to carry fix-ture=<mode>", name, run.env["AF_INTEGRATION_HOOK_FAIL_MODES"])
		}
		for k := range run.env {
			if strings.Contains(k, "FIX_TURE") || strings.Contains(k, "FIX-TURE") {
				t.Errorf("%s: env key %q is derived from the integration name (C1)", name, k)
			}
		}
		if strings.Contains(l.line, "FIX-TURE") || strings.Contains(l.line, "FIX_TURE") {
			t.Errorf("%s: the line names a key derived from the integration name:\n%s", name, l.line)
		}
	}

	for name, l := range map[string]k14Launch{"up": u, "sling": s} {
		set, _ := k14SessionOps(l.ops, session.SessionName("manager"))
		for _, kv := range set {
			if strings.HasPrefix(kv, "FIXTURE_TOKEN=") || strings.HasPrefix(kv, "AF_INTEGRATION_") {
				t.Errorf("%s: Start wrote an integration key into the tmux env (%q); it belongs on the line only", name, kv)
			}
		}
	}
}

// Launch stderr is where an operator learns why an instance's pin binds nothing, so a pin af cannot read must
// warn on up, sling and respawn alike after the last integration is gone — without moving the line.
func TestLaunchContributionsParity_CorruptPinWithNothingInstalledWarnsOnEveryPath(t *testing.T) {
	fx := newK14Fixture(t)
	fake, _ := setupHermeticSessions(t)
	p := integrationPinPath(fx.agentDir)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("{not json\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	u, s, r := fx.up(t, fake), fx.sling(t, fake, ""), fx.respawn(t, false)

	if want := strings.ReplaceAll(k14GoldenLine, "<ROOT>", fx.root); u.line != want {
		t.Errorf("a corrupt pin with nothing installed moved af up's launch line:\n got:  %s\n want: %s", u.line, want)
	}
	k14AssertParity(t, u.line, s.line, r.line)
	for name, l := range map[string]k14Launch{"up": u, "sling": s, "respawn": r} {
		if !strings.Contains(l.output, "warning: manager: integration pin "+p+" is malformed") {
			t.Errorf("%s: the corrupt pin was not reported on stderr (err=%v):\n%s", name, l.err, l.output)
		}
	}
}
