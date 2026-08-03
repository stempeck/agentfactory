package telemetry

import (
	"sort"
	"strings"

	"github.com/stempeck/agentfactory/internal/config"
)

// CorrelationKeys are the identity values stamped onto every signal a launched agent emits.
// They are the join between what the agent's own instrumentation reports and what this
// factory recorded about the step that was running.
//
// FactoryID matters more than it looks: several factories can share a host and a backend, and
// without it their data mingles into one indistinguishable stream.
type CorrelationKeys struct {
	FactoryID       string
	Agent           string
	WorktreeID      string
	FormulaInstance string
	ModelProfile    string
}

// LaunchEnv builds the environment a telemetry-enabled agent session is launched with. It
// constructs an environment; it never reads one. Whether to apply the result — whether
// telemetry is even switched on — is the caller's decision, made where the gate is known.
//
// The set is fixed at seven variables and is returned whole even when the configuration is
// empty. A caller clearing stale settings from a previous launch needs the complete family to
// clear; returning a short slice would leave some of it set from the last run.
//
// Nothing here turns on capture of prompts, responses, tool arguments, tool results or raw
// request bodies. Those switches default off upstream, they are not in this set, and this
// package must never be what enables one.
func LaunchEnv(cfg config.TelemetryConfig, keys CorrelationKeys) []config.EnvVar {
	return []config.EnvVar{
		{Key: "CLAUDE_CODE_ENABLE_TELEMETRY", Value: "1"},
		{Key: "OTEL_METRICS_EXPORTER", Value: "otlp"},
		{Key: "OTEL_LOGS_EXPORTER", Value: "otlp"},
		{Key: "OTEL_EXPORTER_OTLP_PROTOCOL", Value: cfg.Protocol},
		{Key: "OTEL_EXPORTER_OTLP_ENDPOINT", Value: cfg.Endpoint},
		{Key: "OTEL_EXPORTER_OTLP_HEADERS", Value: headerList(cfg.Headers)},
		{Key: "OTEL_RESOURCE_ATTRIBUTES", Value: resourceAttrString(cfg, keys)},
	}
}

// headerList renders the configured headers as the comma-separated list the exporter expects.
// A secret reference is passed through exactly as written: resolving it needs the factory
// root, which this package is not given, and the launch layer performs that step at the point
// where it can also keep the reference — rather than the secret — out of a session's
// environment listing.
func headerList(headers map[string]string) string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	parts := make([]string, 0, len(names))
	for _, name := range names {
		parts = append(parts, name+"="+headers[name])
	}
	return strings.Join(parts, ",")
}

// resourceAttrString emits the correlation keys first, in a fixed order, then any configured
// extras sorted by name. Sorting is not cosmetic: ranging a Go map directly would make the
// launched environment differ between two launches of the same configuration, and an
// environment that changes on its own is impossible to diff when something goes wrong.
func resourceAttrString(cfg config.TelemetryConfig, keys CorrelationKeys) string {
	parts := []string{
		"af.factory_id=" + keys.FactoryID,
		"af.agent=" + keys.Agent,
		"af.worktree_id=" + keys.WorktreeID,
		"af.formula_instance=" + keys.FormulaInstance,
		"af.model_profile=" + keys.ModelProfile,
	}
	extras := make([]string, 0, len(cfg.ResourceAttrsExtra))
	for name := range cfg.ResourceAttrsExtra {
		extras = append(extras, name)
	}
	sort.Strings(extras)
	for _, name := range extras {
		parts = append(parts, name+"="+cfg.ResourceAttrsExtra[name])
	}
	return strings.Join(parts, ",")
}
