package cmd

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/session"
)

// composeIntegrations fills the integration launch fields from recorded consent. A pinned instance binds
// exactly its pinned snapshots (re-hashed against the pin, D34); without a pin the agent's registered formula
// is resolved without writing one (IR:L449). Nothing here refuses a launch: every problem is a report and the
// affected integration is dropped (D29). Its mails are returned, not sent, for the launch site to send once
// the launch has happened (IR:L588). With no integration installed it leaves c untouched, so the launch line
// stays byte-identical.
func composeIntegrations(root, agentDir string, entry config.AgentEntry, c *session.LaunchContributions) (reports, dropped []string, mails []integrationMail) {
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return []string{fmt.Sprintf("integrations not bound: %v", err)}, nil, nil
	}
	pin, pinned, pinErr := readIntegrationPin(agentDir)
	if pinErr != nil {
		reports = append(reports, pinErr.Error()+"; binding factory-scope integrations only")
	}
	// A pinned snapshot outlives its plugins.json record (the GC spares it), so removing the last
	// integration must not silently unbind an instance that consented to it.
	if !slices.ContainsFunc(slices.Collect(maps.Values(cfg.Plugins)), func(e config.PluginEntry) bool { return e.Integration != nil }) &&
		(pin == nil || len(pin.Bindings) == 0) {
		return reports, nil, nil
	}

	var required, optional []string
	if !pinned {
		required, optional, err = registeredFormulaIntegrations(root, entry)
		if err != nil {
			reports = append(reports, err.Error())
		}
	}
	bound, candidates, err := integrationBoundSet(root, cfg, pin, required, optional)
	if err != nil {
		reports = append(reports, err.Error())
	}
	factoryScope := map[string]bool{}
	for _, name := range config.FactoryScopeIntegrations(cfg) {
		factoryScope[name] = true
	}

	boundNames := map[string]bool{}
	for _, b := range bound {
		boundNames[b.Name] = true
		clearIntegrationReport(root, b.Name, integrationReportNotBound)
		clearIntegrationReport(root, b.Name, integrationReportSkipped)
	}
	for _, name := range candidates {
		if boundNames[name] {
			continue
		}
		dropped = append(dropped, name)
		report := notBoundReport(root, cfg, pin, name)
		subject, condition := "INTEGRATION_NOT_BOUND "+name, integrationReportNotBound
		if slices.Contains(optional, name) && !slices.Contains(required, name) && !factoryScope[name] {
			report = integrationSkippedPrefix + name + ": " + report
			subject, condition = integrationSkippedPrefix+name, integrationReportSkipped
		}
		reports = append(reports, report)
		mails = append(mails, integrationMail{name: name, condition: condition, subject: subject, body: report})
	}

	for _, b := range bound {
		if !factoryScope[b.Name] {
			continue
		}
		if report, degraded := factoryScopeCheckReport(root, b); report != "" {
			reports = append(reports, report)
			if degraded {
				mails = append(mails, integrationMail{name: b.Name, condition: integrationReportDegraded, subject: "INTEGRATION_DEGRADED " + b.Name, body: report})
			}
		}
	}

	reports = append(reports, fillIntegrationContributions(bound, config.IntegrationEnvKeyUniverse(cfg), c)...)
	return reports, dropped, mails
}

// factoryScopeCheckReport reads, never runs, the check record of a bound factory-scope integration: a failed
// check keeps it bound and is reported as degraded (DD:349). Only a record for the bound snapshot counts; one
// left by other content says nothing about it.
func factoryScopeCheckReport(root string, b config.IntegrationBinding) (report string, degraded bool) {
	rec, err := readIntegrationCheckRecord(root, b.Name)
	switch {
	case err != nil:
		return fmt.Sprintf("integration %s: %v", b.Name, err), false
	case rec == nil || rec.ContentSHA256 != b.ContentSHA256:
		return "", false
	case rec.State == integrationCheckOK:
		clearIntegrationReport(root, b.Name, integrationReportDegraded)
		return "", false
	}
	return fmt.Sprintf("INTEGRATION_DEGRADED %s: %s", b.Name, rec.Output), true
}

type integrationMail struct {
	name, condition, subject, body string
}

// pinnedIntegrationRecord is the consent record the pin captured. The per-file hashes come from plugins.json
// only while it still records that same snapshot, so a drift report can say how many files changed.
func pinnedIntegrationRecord(cfg *config.PluginsConfig, pb integrationPinBinding) *config.PluginIntegration {
	in := &config.PluginIntegration{SnapshotDir: pb.SnapshotDir, ContentSHA256: pb.ContentSHA256, ClaudePlugins: pb.ClaudePlugins}
	if e, ok := cfg.Plugins[pb.Name]; ok && e.Integration != nil && e.Integration.ContentSHA256 == pb.ContentSHA256 {
		in.Files = e.Integration.Files
		in.Scope = e.Integration.Scope
	}
	return in
}

// notBoundReport re-derives why name did not bind, for the report and the manager's mail.
func notBoundReport(root string, cfg *config.PluginsConfig, pin *integrationPin, name string) string {
	if pin != nil {
		for _, pb := range pin.Bindings {
			if pb.Name == name {
				if _, err := config.BindIntegration(root, name, pinnedIntegrationRecord(cfg, pb)); err != nil {
					return err.Error()
				}
			}
		}
	}
	e, ok := cfg.Plugins[name]
	if !ok || e.Integration == nil {
		return fmt.Sprintf("integration %q not bound: not installed", name)
	}
	if _, err := config.BindIntegration(root, name, e.Integration); err != nil {
		return err.Error()
	}
	return fmt.Sprintf("integration %q not bound", name)
}

func registeredFormulaIntegrations(root string, entry config.AgentEntry) (required, optional []string, err error) {
	if entry.Formula == "" {
		return nil, nil, nil
	}
	path, err := formula.FindFormulaFile(entry.Formula, root)
	if err != nil {
		return nil, nil, fmt.Errorf("integrations of formula %q not bound: %w", entry.Formula, err)
	}
	f, err := formula.ParseFile(path)
	if err != nil {
		return nil, nil, fmt.Errorf("integrations of formula %q not bound: %w", entry.Formula, err)
	}
	return f.Integrations, f.IntegrationsOptional, nil
}

// fillIntegrationContributions writes the bound set into the launch fields. A key that is not a shell
// identifier, that another bound integration already exports, or that af, the model or the telemetry
// families own is dropped with a report: an integration must not be able to redirect model traffic (D35),
// spoof identity or override af's own launch exports. AF_INTEGRATION_HOOK_FAIL_MODES joins the universe so
// a launch that binds nothing clears the value an earlier launch left in the pane (IR C17); it is only
// reached with integrations installed.
func fillIntegrationContributions(bound []config.IntegrationBinding, universe []string, c *session.LaunchContributions) (reports []string) {
	owned := map[string]bool{}
	for _, k := range c.ModelKeyUniverse {
		owned[k] = true
	}
	for _, ev := range slices.Concat(c.ModelEnv, c.TelemetryEnv) {
		owned[ev.Key] = true
	}
	exported := map[string]string{}
	var modes []string
	for _, b := range bound {
		c.PluginDirs = append(c.PluginDirs, b.PluginDirs...)
		for _, ev := range b.Env {
			switch {
			case !config.IsValidEnvKeyName(ev.Key):
				reports = append(reports, fmt.Sprintf("integration env key %q is not a valid identifier; not exported", ev.Key))
			case config.IsAFIdentityKey(ev.Key) || config.IsAFLaunchKey(ev.Key):
				reports = append(reports, fmt.Sprintf("integration %s: env key %q is reserved for af; not exported", b.Name, ev.Key))
			case owned[ev.Key]:
				reports = append(reports, fmt.Sprintf("integration %s: env key %q belongs to the model or telemetry launch env; not exported", b.Name, ev.Key))
			case exported[ev.Key] != "":
				reports = append(reports, fmt.Sprintf("integration %s: env key %q is already exported by integration %s; not exported", b.Name, ev.Key, exported[ev.Key]))
			default:
				exported[ev.Key] = b.Name
				c.IntegrationEnv = append(c.IntegrationEnv, ev)
			}
		}
		if b.HookFailMode != "" {
			modes = append(modes, b.Name+"="+b.HookFailMode)
		}
	}
	for _, k := range universe {
		if config.IsValidEnvKeyName(k) && !owned[k] && !config.IsAFIdentityKey(k) && !config.IsAFLaunchKey(k) {
			c.IntegrationKeyUniverse = append(c.IntegrationKeyUniverse, k)
		}
	}
	for _, ev := range c.IntegrationEnv {
		if !slices.Contains(c.IntegrationKeyUniverse, ev.Key) {
			c.IntegrationKeyUniverse = append(c.IntegrationKeyUniverse, ev.Key)
		}
	}
	c.IntegrationKeyUniverse = append(c.IntegrationKeyUniverse, config.EnvIntegrationHookFailModes)
	slices.Sort(c.IntegrationKeyUniverse)
	c.HookFailModes = strings.Join(modes, ",")
	return reports
}
