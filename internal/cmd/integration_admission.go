package cmd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
)

const (
	refusalIntegrationMissing        = "integration-missing"
	refusalIntegrationDrifted        = "integration-drifted"
	refusalIntegrationCheckFailed    = "integration-check-failed"
	refusalIntegrationServiceDown    = "integration-service-down"
	refusalIntegrationSnapshotBad    = "integration-snapshot-invalid"
	refusalNamespacedSkillUnresolved = "namespaced-skill-unresolved"
)

// slingRefusedMarker prefixes the one stdout line a refused instantiation prints, which the dispatcher parses.
const slingRefusedMarker = "AF_SLING_REFUSED"

const integrationSkippedPrefix = "INTEGRATION_SKIPPED "

type integrationRefusal struct {
	Class       string
	Integration string
	Detail      string
}

func (r *integrationRefusal) Error() string {
	return fmt.Sprintf("%s: %s", r.Class, r.Detail)
}

func formulaDeclaresIntegrations(f *formula.Formula) bool {
	return len(f.Integrations) > 0 || len(f.IntegrationsOptional) > 0 || slices.ContainsFunc(f.Skills, isNamespacedSkill)
}

func isNamespacedSkill(s string) bool { return strings.Contains(s, ":") }

// admitFormulaIntegrations decides, before any bead exists, whether the formula's declared integrations let
// it run. A required name must be recorded, intact, pass its [check] and have its [service] session up;
// an optional name that fails any of these is skipped with an INTEGRATION_SKIPPED report instead.
func admitFormulaIntegrations(ctx context.Context, cmd *cobra.Command, root string, f *formula.Formula) (bound []config.IntegrationBinding, reports []string, err error) {
	if !formulaDeclaresIntegrations(f) {
		return nil, nil, nil
	}
	if ctx == nil && cmd != nil {
		ctx = cmd.Context()
	}
	if ctx == nil {
		ctx = context.Background()
	}
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return nil, nil, fmt.Errorf("loading plugins config: %w", err)
	}
	if refusal := missingRequiredIntegrations(cfg, f); refusal != nil {
		return nil, nil, refusal
	}

	skippedPlugins := map[string]string{}
	for _, name := range f.Integrations {
		b, rs, refusal := admitIntegration(ctx, root, name, cfg.Plugins[name].Integration)
		reports = append(reports, rs...)
		if refusal != nil {
			return nil, reports, refusal
		}
		b.Required = true
		bound = append(bound, b)
	}
	for _, name := range f.IntegrationsOptional {
		e, ok := cfg.Plugins[name]
		if !ok || e.Integration == nil {
			reports = append(reports, skipOptionalIntegration(root, name, "not installed")...)
			continue
		}
		b, rs, refusal := admitIntegration(ctx, root, name, e.Integration)
		reports = append(reports, rs...)
		if refusal != nil {
			reports = append(reports, skipOptionalIntegration(root, name, refusal.Detail)...)
			for _, p := range e.Integration.ClaudePlugins {
				skippedPlugins[p] = name
			}
			continue
		}
		clearIntegrationReport(root, name, integrationReportSkipped)
		bound = append(bound, b)
	}

	for _, skill := range f.Skills {
		if !isNamespacedSkill(skill) || namespacedSkillResolves(skill, bound) {
			continue
		}
		ns, _, _ := strings.Cut(skill, ":")
		if name, ok := skippedPlugins[ns]; ok {
			reports = append(reports, fmt.Sprintf("namespaced skill %q unresolved: its integration %s was skipped", skill, name))
			continue
		}
		return nil, reports, &integrationRefusal{
			Class:  refusalNamespacedSkillUnresolved,
			Detail: fmt.Sprintf("skill %q is not provided by a bound plugin of an integration formula %q declares", skill, f.Name),
		}
	}
	return bound, reports, nil
}

// skipOptionalIntegration returns the INTEGRATION_SKIPPED report, mailed once until the integration binds.
func skipOptionalIntegration(root, name, reason string) []string {
	report := integrationSkippedPrefix + name + ": " + reason
	if err := reportIntegration(root, name, integrationReportSkipped, integrationSkippedPrefix+name, report); err != nil {
		return []string{report, err.Error()}
	}
	return []string{report}
}

func missingRequiredIntegrations(cfg *config.PluginsConfig, f *formula.Formula) *integrationRefusal {
	var missing []string
	for _, name := range f.Integrations {
		if e, ok := cfg.Plugins[name]; !ok || e.Integration == nil {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	noun := "integrations"
	if len(missing) == 1 {
		noun = "integration"
	}
	return &integrationRefusal{
		Class:       refusalIntegrationMissing,
		Integration: missing[0],
		Detail: fmt.Sprintf("%d %s required by formula %q not installed: %s — run af plugin list, then af plugin install %s",
			len(missing), noun, f.Name, strings.Join(missing, ", "), strings.Join(missing, " ")),
	}
}

// admitIntegration runs the three post-record checks on one integration in the spec's order: snapshot
// intact, [check] ok, [service] up. It is side-effecting: it may run [check] and start the service.
func admitIntegration(ctx context.Context, root, name string, in *config.PluginIntegration) (config.IntegrationBinding, []string, *integrationRefusal) {
	b, err := config.BindIntegration(root, name, in)
	if err != nil {
		return b, nil, bindRefusal(name, err)
	}
	if refusal := admitIntegrationCheck(ctx, root, name, in); refusal != nil {
		return b, nil, refusal
	}
	if in.Service == "" {
		return b, nil, nil
	}
	reports := ensureIntegrationService(ctx, root, name, in)
	up, err := newCmdTmux().HasSession(in.Service)
	if err != nil || !up {
		detail := fmt.Sprintf("integration %q service session %s is not running after the ensure", name, in.Service)
		if err != nil {
			detail = fmt.Sprintf("integration %q service session %s cannot be probed: %v", name, in.Service, err)
		}
		return b, reports, &integrationRefusal{Class: refusalIntegrationServiceDown, Integration: name, Detail: detail}
	}
	return b, reports, nil
}

func bindRefusal(name string, err error) *integrationRefusal {
	class := refusalIntegrationSnapshotBad
	var be *config.IntegrationBindError
	if errors.As(err, &be) && be.Drifted {
		class = refusalIntegrationDrifted
	}
	return &integrationRefusal{Class: class, Integration: name, Detail: err.Error()}
}

// admitIntegrationCheck reuses a check record only when it is ok, fresh and for the consented content (D1);
// otherwise it runs [check], once more on failure (D28), and refuses on the second failure.
func admitIntegrationCheck(ctx context.Context, root, name string, in *config.PluginIntegration) *integrationRefusal {
	if checkRecordReusable(root, name, in) {
		return nil
	}
	var rec *integrationCheckRecord
	for range 2 {
		rec = runIntegrationCheck(ctx, root, name, in)
		if rec == nil || rec.State == integrationCheckOK {
			return nil
		}
	}
	return &integrationRefusal{
		Class:       refusalIntegrationCheckFailed,
		Integration: name,
		Detail:      fmt.Sprintf("integration %q check failed at %s: %s", name, rec.At, rec.Output),
	}
}

func checkRecordReusable(root, name string, in *config.PluginIntegration) bool {
	rec := freshCheckRecord(root, name, in)
	return rec != nil && rec.State == integrationCheckOK
}

// freshCheckRecord returns the check record only when it was taken of the consented content and is still
// inside the manifest's fresh_for.
func freshCheckRecord(root, name string, in *config.PluginIntegration) *integrationCheckRecord {
	rec, err := readIntegrationCheckRecord(root, name)
	if err != nil || rec == nil || rec.ContentSHA256 != in.ContentSHA256 {
		return nil
	}
	snap, ok := config.IntegrationSnapshotPath(root, name, in.ContentSHA256, in.SnapshotDir)
	if !ok {
		return nil
	}
	m, err := config.LoadIntegrationManifestNamed(snap, name)
	if err != nil || m.Check == nil {
		return nil
	}
	freshFor, err := config.ParseIntegrationDuration(m.Check.FreshFor)
	if err != nil || freshFor == 0 {
		return nil
	}
	at, err := time.Parse(time.RFC3339, rec.At)
	if err != nil || time.Since(at) >= freshFor {
		return nil
	}
	return rec
}

// namespacedSkillResolves accepts <ns>:<skill> only from a bound plugin dir whose plugin.json names ns, and
// only when ns is a plugin the integration's consent recorded.
func namespacedSkillResolves(skill string, bound []config.IntegrationBinding) bool {
	ns, name, _ := strings.Cut(skill, ":")
	for _, b := range bound {
		if !slices.Contains(b.ClaudePlugins, ns) {
			continue
		}
		for _, dir := range b.PluginDirs {
			if pluginName, err := checkClaudePluginJSON(filepath.Base(dir), dir); err != nil || pluginName != ns {
				continue
			}
			if _, err := os.Stat(filepath.Join(dir, "skills", name, "SKILL.md")); err == nil {
				return true
			}
		}
	}
	return false
}

// skippedFromReports recovers admission's skipped optionals, with reasons, for the pin.
func skippedFromReports(reports []string) []integrationPinSkipped {
	var skipped []integrationPinSkipped
	for _, r := range reports {
		rest, ok := strings.CutPrefix(r, integrationSkippedPrefix)
		if !ok {
			continue
		}
		if name, reason, ok := strings.Cut(rest, ": "); ok {
			skipped = append(skipped, integrationPinSkipped{Name: name, Reason: reason})
		}
	}
	return skipped
}

// precheckFormulaIntegrations is the read-only half of admission the dispatcher runs before it touches an
// agent: it runs no [check], starts no service and writes nothing. It refuses only what admission is bound to
// refuse from the state already on disk; whatever admission could still repair is left to it.
func precheckFormulaIntegrations(root string, f *formula.Formula) error {
	if len(f.Integrations) == 0 && !slices.ContainsFunc(f.Skills, isNamespacedSkill) {
		return nil
	}
	cfg, err := config.LoadPluginsConfig(config.PluginsConfigPath(root))
	if err != nil {
		return fmt.Errorf("loading plugins config: %w", err)
	}
	if refusal := missingRequiredIntegrations(cfg, f); refusal != nil {
		return refusal
	}
	var bound []config.IntegrationBinding
	for _, name := range f.Integrations {
		in := cfg.Plugins[name].Integration
		b, err := config.BindIntegration(root, name, in)
		if err != nil {
			return bindRefusal(name, err)
		}
		if refusal := precheckIntegrationCheck(root, name, in); refusal != nil {
			return refusal
		}
		if refusal := precheckIntegrationService(root, name, in); refusal != nil {
			return refusal
		}
		bound = append(bound, b)
	}
	return precheckNamespacedSkills(cfg, f, bound)
}

// precheckIntegrationCheck refuses on a fresh failing record of the consented content. Admission would re-run
// that [check], so the refusal names the verb that re-runs it; a stale or absent record is left to admission.
func precheckIntegrationCheck(root, name string, in *config.PluginIntegration) *integrationRefusal {
	rec := freshCheckRecord(root, name, in)
	if rec == nil || rec.State == integrationCheckOK {
		return nil
	}
	return &integrationRefusal{
		Class:       refusalIntegrationCheckFailed,
		Integration: name,
		Detail:      fmt.Sprintf("integration %q check failed at %s: %s — fix it, then run af plugin check %s", name, rec.At, rec.Output, name),
	}
}

// precheckIntegrationService refuses a required service whose session is down while ensureIntegrationService
// would not relaunch it yet, which is exactly when admission's post-ensure probe fails.
func precheckIntegrationService(root, name string, in *config.PluginIntegration) *integrationRefusal {
	if in.Service == "" {
		return nil
	}
	if up, err := newCmdTmux().HasSession(in.Service); err != nil || up {
		return nil
	}
	st, err := readIntegrationServiceState(root, name)
	if err != nil || !integrationServiceRelaunchDeferred(st, integrationNowFn()) {
		return nil
	}
	return &integrationRefusal{
		Class:       refusalIntegrationServiceDown,
		Integration: name,
		Detail:      fmt.Sprintf("integration %q service session %s is not running and is inside its relaunch backoff — inspect it with af plugin check %s", name, in.Service, name),
	}
}

// precheckNamespacedSkills refuses a namespaced skill no required binding provides and no installed optional
// integration could: admission would refuse it too. A skill an optional might provide is left to admission.
func precheckNamespacedSkills(cfg *config.PluginsConfig, f *formula.Formula, bound []config.IntegrationBinding) error {
	for _, skill := range f.Skills {
		if !isNamespacedSkill(skill) || namespacedSkillResolves(skill, bound) {
			continue
		}
		ns, _, _ := strings.Cut(skill, ":")
		if slices.ContainsFunc(f.IntegrationsOptional, func(name string) bool {
			e, ok := cfg.Plugins[name]
			return ok && e.Integration != nil && slices.Contains(e.Integration.ClaudePlugins, ns)
		}) {
			continue
		}
		return &integrationRefusal{
			Class:  refusalNamespacedSkillUnresolved,
			Detail: fmt.Sprintf("skill %q is not provided by a bound plugin of an integration formula %q declares", skill, f.Name),
		}
	}
	return nil
}

// precheckNamedFormula runs precheckFormulaIntegrations on the formula an agent would instantiate. A formula
// that cannot be found or parsed is left to instantiateFormulaWorkflow, which reports that failure itself.
func precheckNamedFormula(root, formulaName string) error {
	path, err := formula.FindFormulaFile(formulaName, root)
	if err != nil {
		return nil
	}
	f, err := formula.ParseFile(path)
	if err != nil {
		return nil
	}
	return precheckFormulaIntegrations(root, f)
}

// dispatchRefusalItem names what a dispatch was for: the task, or the formula of a taskless (bare) sling.
func dispatchRefusalItem(task, formulaName string) string {
	if task == "" {
		return "bare:" + formulaName
	}
	return task
}

// reportSlingRefusal prints the marker line the dispatcher parses from sling's stdout (D8) and mails the
// refusal to the caller the SKILL_MISSING mail it replaces would reach, once per (agent, item, class) (D9), so a dispatcher re-slinging the same item each backoff
// window does not re-announce it.
func reportSlingRefusal(out, errOut io.Writer, root, caller, agent, item string, r *integrationRefusal) {
	fmt.Fprintf(out, "%s class=%s integration=%s\n", slingRefusedMarker, r.Class, r.Integration)
	if caller == "" || caller == "@cli" {
		return
	}
	subject := fmt.Sprintf("INTEGRATION_REFUSED %s: %s", r.Class, agent)
	body := fmt.Sprintf("Dispatch of %s to %s was refused before anything was touched: %s", item, agent, r.Error())
	if err := reportIntegrationTo(root, caller, slingRefusalReportName(agent, item), r.Class, subject, body); err != nil {
		fmt.Fprintf(errOut, "warning: %v\n", err)
	}
}

// dispatchCaller is the recipient ensureCallerIdentity would persist, resolved without writing anything: the
// pre-check refuses before the dispatch has touched the agent.
func dispatchCaller(callerWd, root string) string {
	if slingCaller != "" {
		return slingCaller
	}
	if role, _, _ := detectRole(callerWd, root); role != "" {
		return role
	}
	return fallbackCaller
}

// clearSlingRefusalReports re-arms every refusal class of (agent, item) once a sling of it succeeds.
func clearSlingRefusalReports(root, agent, item string) {
	matches, _ := filepath.Glob(integrationReportMarker(root, slingRefusalReportName(agent, item), "*"))
	for _, m := range matches {
		if err := os.Remove(m); err != nil && !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(os.Stderr, "warning: clearing refusal report marker: %v\n", err)
		}
	}
}

// slingRefusalReportName keys the dedup marker; the item is hashed because a URL is not a file name.
func slingRefusalReportName(agent, item string) string {
	sum := sha256.Sum256([]byte(agent + "\x00" + item))
	return filepath.Join("dispatch", agent+"-"+hex.EncodeToString(sum[:8]))
}
