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

func launchLineFor(t *testing.T, root string, entry config.AgentEntry, c *session.LaunchContributions) string {
	t.Helper()
	mgr := session.NewManager(root, admAgent, entry)
	mgr.SetLaunchContributions(c)
	line, err := mgr.BuildStartupCommand()
	if err != nil {
		t.Fatalf("BuildStartupCommand: %v", err)
	}
	return line
}

func unsetSegment(line string) []string {
	for _, seg := range strings.Split(line, " && ") {
		if rest, ok := strings.CutPrefix(seg, "unset "); ok {
			return strings.Fields(rest)
		}
	}
	return nil
}

// With integrations installed, a launch that binds none must clear AF_INTEGRATION_HOOK_FAIL_MODES, or a
// respawn inherits the fail modes of snapshots it no longer loads (D73).
func TestComposeIntegrations_HookFailModesKeyClearedWhenNothingBinds(t *testing.T) {
	a := admFactory(t)
	admRecordHealthy(t, a, "acme-int", nil)
	admWriteFormula(t, a, "plain", nil, nil, nil)
	entry := config.AgentEntry{Type: "autonomous", Formula: "plain"}

	var c session.LaunchContributions
	reports, _, _ := composeIntegrations(a.root, a.agentDir, entry, &c)
	if len(c.PluginDirs) != 0 || c.HookFailModes != "" {
		t.Fatalf("fixture: a formula-scope integration the formula does not declare must not bind; got dirs %q modes %q (reports %q)", c.PluginDirs, c.HookFailModes, reports)
	}
	if !slices.Contains(c.IntegrationKeyUniverse, config.EnvIntegrationHookFailModes) {
		t.Errorf("IntegrationKeyUniverse = %q, want it to carry %s while integrations are installed", c.IntegrationKeyUniverse, config.EnvIntegrationHookFailModes)
	}
	if !slices.Contains(unsetSegment(launchLineFor(t, a.root, entry, &c)), config.EnvIntegrationHookFailModes) {
		t.Errorf("the launch line does not unset %s", config.EnvIntegrationHookFailModes)
	}
}

// With no integration installed the composer leaves the launch fields untouched, so the line stays
// byte-identical (IR C17).
func TestComposeIntegrations_NothingInstalledLeavesContributionsEmpty(t *testing.T) {
	a := admFactory(t)
	admWriteFormula(t, a, "plain", nil, nil, nil)
	var c session.LaunchContributions
	reports, dropped, mails := composeIntegrations(a.root, a.agentDir, config.AgentEntry{Type: "autonomous", Formula: "plain"}, &c)
	if len(reports)+len(dropped)+len(mails) != 0 {
		t.Errorf("reports %q dropped %q mails %d, want none", reports, dropped, len(mails))
	}
	if len(c.IntegrationKeyUniverse) != 0 || len(c.IntegrationEnv) != 0 || len(c.PluginDirs) != 0 || c.HookFailModes != "" {
		t.Errorf("contributions = %+v, want the integration fields empty", c)
	}
}

// Removing the last integration must not silently unbind an instance that consented to it, so a pin af
// cannot read is reported with nothing installed exactly as with integrations installed. The launch fields
// stay untouched, so the line is still byte-identical (IR C17).
func TestComposeIntegrations_CorruptPinReportedWithNothingInstalled(t *testing.T) {
	corrupt := []struct {
		name  string
		write func(t *testing.T, agentDir string)
		want  string
	}{
		{"malformed", func(t *testing.T, agentDir string) {
			p := integrationPinPath(agentDir)
			if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte("{not json\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, "is malformed"},
		{"unknown_version", func(t *testing.T, agentDir string) {
			pinFixtureWrite(t, agentDir, integrationPin{V: 2, Formula: "plain"})
		}, "has unknown version 2"},
		// A directory at the pin path fails the read even as root, where chmod 000 would not.
		{"unreadable", func(t *testing.T, agentDir string) {
			if err := os.MkdirAll(integrationPinPath(agentDir), 0o755); err != nil {
				t.Fatal(err)
			}
		}, "reading integration pin"},
	}
	installed := []struct {
		name string
		seed func(t *testing.T, a *admEnv)
	}{
		{"no_plugins_json", func(*testing.T, *admEnv) {}},
		{"formula_plugin_only", func(t *testing.T, a *admEnv) { intBSeedPluginsJSON(t, a.intBEnv) }},
		{"last_integration_removed", func(t *testing.T, a *admEnv) {
			admRecordHealthy(t, a, "acme-int", nil)
			path := config.PluginsConfigPath(a.root)
			cfg, err := config.LoadPluginsConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			delete(cfg.Plugins, "acme-int")
			if err := config.SavePluginsConfig(path, cfg); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, in := range installed {
		for _, pc := range corrupt {
			t.Run(in.name+"/"+pc.name, func(t *testing.T) {
				a := admFactory(t)
				in.seed(t, a)
				admWriteFormula(t, a, "plain", nil, nil, nil)
				pc.write(t, a.agentDir)
				p := integrationPinPath(a.agentDir)
				before, _ := os.ReadFile(p)
				entry := config.AgentEntry{Type: "autonomous", Formula: "plain"}

				var c session.LaunchContributions
				reports, dropped, mails := composeIntegrations(a.root, a.agentDir, entry, &c)

				if len(reports) != 1 || !strings.Contains(reports[0], pc.want) || !strings.Contains(reports[0], p) ||
					!strings.HasSuffix(reports[0], "; binding factory-scope integrations only") {
					t.Errorf("the %s pin must be reported once, naming the pin, with no integration installed; reports = %q", pc.name, reports)
				}
				if len(dropped) != 0 || len(mails) != 0 {
					t.Errorf("dropped %q mails %+v, want none: a corrupt pin is a report, as it is with integrations installed", dropped, mails)
				}
				if len(c.IntegrationKeyUniverse) != 0 || len(c.IntegrationEnv) != 0 || len(c.PluginDirs) != 0 || c.HookFailModes != "" {
					t.Errorf("contributions = %+v, want the integration fields untouched with nothing installed", c)
				}
				if got, want := launchLineFor(t, a.root, entry, &c), launchLineFor(t, a.root, entry, &session.LaunchContributions{}); got != want {
					t.Errorf("the launch line moved with nothing installed:\n got:  %s\n want: %s", got, want)
				}
				after, _ := os.ReadFile(p)
				if _, err := os.Lstat(p); err != nil || string(after) != string(before) {
					t.Errorf("compose rewrote or removed the corrupt pin (lstat err %v)", err)
				}
			})
		}
	}
}

// A valid pin that binds nothing is not a corrupt one: with nothing installed it stays silent and leaves the
// launch fields empty, so the corrupt-pin report must key on the read error, not on the pin being present.
func TestComposeIntegrations_SkippedOnlyPinWithNothingInstalledLeavesContributionsEmpty(t *testing.T) {
	a := admFactory(t)
	admWriteFormula(t, a, "optfix", nil, []string{"defenseclaw"}, nil)
	pinFixtureWrite(t, a.agentDir, integrationPin{Formula: "optfix",
		Skipped: []integrationPinSkipped{{Name: "defenseclaw", Reason: "not installed"}}})
	var c session.LaunchContributions
	reports, dropped, mails := composeIntegrations(a.root, a.agentDir, config.AgentEntry{Type: "autonomous", Formula: "optfix"}, &c)
	if len(reports)+len(dropped)+len(mails) != 0 {
		t.Errorf("reports %q dropped %q mails %d, want none", reports, dropped, len(mails))
	}
	if len(c.IntegrationKeyUniverse) != 0 || len(c.IntegrationEnv) != 0 || len(c.PluginDirs) != 0 || c.HookFailModes != "" {
		t.Errorf("contributions = %+v, want the integration fields empty", c)
	}
}

// A snapshot installed before the reserved-key deny existed may still carry an af-owned key. The composer
// must neither export it nor put it in the unset universe, and must say which key it dropped (D73, IR C17).
func TestFillIntegrationContributions_DropsAFOwnedAndInvalidKeys(t *testing.T) {
	bound := []config.IntegrationBinding{{
		Name: "acme-int",
		Env: []config.EnvVar{
			{Key: "AF_ROLE", Value: "manager"},
			{Key: config.EnvIntegrationHookFailModes, Value: "acme-int=ignore"},
			{Key: "BAD KEY", Value: "x"},
			{Key: "ACME_TOKEN", Value: "t"},
		},
	}}
	var c session.LaunchContributions
	reports := fillIntegrationContributions(bound, []string{"ACME_TOKEN", "AF_ROLE"}, &c)

	var exported []string
	for _, ev := range c.IntegrationEnv {
		exported = append(exported, ev.Key)
	}
	if !slices.Equal(exported, []string{"ACME_TOKEN"}) {
		t.Errorf("exported integration keys = %q, want only ACME_TOKEN", exported)
	}
	if slices.Contains(c.IntegrationKeyUniverse, "AF_ROLE") {
		t.Errorf("IntegrationKeyUniverse = %q: an af identity key must never be unset by the integration universe", c.IntegrationKeyUniverse)
	}
	for _, key := range []string{"AF_ROLE", config.EnvIntegrationHookFailModes} {
		if !slices.ContainsFunc(reports, func(r string) bool { return strings.Contains(r, `"`+key+`"`) && strings.Contains(r, "not exported") }) {
			t.Errorf("no report names the dropped af-owned key %s; reports %q", key, reports)
		}
	}
	if !slices.Contains(reports, `integration env key "BAD KEY" is not a valid identifier; not exported`) {
		t.Errorf("the invalid key must be reported by name; reports %q", reports)
	}
}

// The model and telemetry families own their keys three ways (the model key universe, the resolved model
// env, the telemetry env); an integration naming any of them is dropped and reported, and its key never
// joins the integration unset universe, which would clear af's own value on the next launch.
func TestFillIntegrationContributions_DropsModelAndTelemetryOwnedKeys(t *testing.T) {
	owned := []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW", "ANTHROPIC_BASE_URL", "OTEL_RESOURCE_ATTRIBUTES"}
	bound := []config.IntegrationBinding{{
		Name: "acme-int",
		Env: []config.EnvVar{
			{Key: "CLAUDE_CODE_AUTO_COMPACT_WINDOW", Value: "evil"},
			{Key: "ANTHROPIC_BASE_URL", Value: "evil"},
			{Key: "OTEL_RESOURCE_ATTRIBUTES", Value: "evil"},
			{Key: "ACME_TOKEN", Value: "t"},
		},
	}}
	c := session.LaunchContributions{
		ModelKeyUniverse: []string{"CLAUDE_CODE_AUTO_COMPACT_WINDOW"},
		ModelEnv:         []config.EnvVar{{Key: "ANTHROPIC_BASE_URL", Value: "https://gw.example"}},
		TelemetryEnv:     []config.EnvVar{{Key: "OTEL_RESOURCE_ATTRIBUTES", Value: "service.name=af"}},
	}
	reports := fillIntegrationContributions(bound, append([]string{"ACME_TOKEN"}, owned...), &c)

	var exported []string
	for _, ev := range c.IntegrationEnv {
		exported = append(exported, ev.Key)
	}
	if !slices.Equal(exported, []string{"ACME_TOKEN"}) {
		t.Errorf("exported integration keys = %q, want only ACME_TOKEN", exported)
	}
	for _, key := range owned {
		if want := `integration acme-int: env key "` + key + `" belongs to the model or telemetry launch env; not exported`; !slices.Contains(reports, want) {
			t.Errorf("missing report %q; reports %q", want, reports)
		}
		if slices.Contains(c.IntegrationKeyUniverse, key) {
			t.Errorf("IntegrationKeyUniverse = %q: af's own %s must never be unset by the integration universe", c.IntegrationKeyUniverse, key)
		}
	}
}
