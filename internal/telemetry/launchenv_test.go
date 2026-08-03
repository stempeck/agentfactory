package telemetry

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func TestLaunchEnv(t *testing.T) {
	keys := CorrelationKeys{
		FactoryID:       "fact-01",
		Agent:           "design-v7",
		WorktreeID:      "wt-03478d",
		FormulaInstance: "af-329-instance",
		ModelProfile:    "fable-5",
	}

	t.Run("returns exactly the seven-variable set in order", func(t *testing.T) {
		got := LaunchEnv(testConfig(), keys)
		want := []string{
			"CLAUDE_CODE_ENABLE_TELEMETRY",
			"OTEL_METRICS_EXPORTER",
			"OTEL_LOGS_EXPORTER",
			"OTEL_EXPORTER_OTLP_PROTOCOL",
			"OTEL_EXPORTER_OTLP_ENDPOINT",
			"OTEL_EXPORTER_OTLP_HEADERS",
			"OTEL_RESOURCE_ATTRIBUTES",
		}
		if len(got) != len(want) {
			t.Fatalf("got %d variables, want exactly %d: %+v", len(got), len(want), got)
		}
		for i, w := range want {
			if got[i].Key != w {
				t.Errorf("variable %d is %q, want %q — the launch chokepoint emits these in order", i, got[i].Key, w)
			}
		}
		byKey := map[string]string{}
		for _, e := range got {
			byKey[e.Key] = e.Value
		}
		if byKey["CLAUDE_CODE_ENABLE_TELEMETRY"] != "1" {
			t.Errorf("enable flag = %q, want 1", byKey["CLAUDE_CODE_ENABLE_TELEMETRY"])
		}
		for _, k := range []string{"OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER"} {
			if byKey[k] != "otlp" {
				t.Errorf("%s = %q, want otlp", k, byKey[k])
			}
		}
		if byKey["OTEL_EXPORTER_OTLP_ENDPOINT"] != "http://127.0.0.1:5080" {
			t.Errorf("endpoint = %q", byKey["OTEL_EXPORTER_OTLP_ENDPOINT"])
		}
		if byKey["OTEL_EXPORTER_OTLP_PROTOCOL"] != "http/json" {
			t.Errorf("protocol = %q", byKey["OTEL_EXPORTER_OTLP_PROTOCOL"])
		}
	})

	t.Run("resource attributes carry every correlation key", func(t *testing.T) {
		got := LaunchEnv(testConfig(), keys)
		var attrs string
		for _, e := range got {
			if e.Key == "OTEL_RESOURCE_ATTRIBUTES" {
				attrs = e.Value
			}
		}
		for _, want := range []string{
			"af.factory_id=fact-01",
			"af.agent=design-v7",
			"af.worktree_id=wt-03478d",
			"af.formula_instance=af-329-instance",
			"af.model_profile=fable-5",
		} {
			if !strings.Contains(attrs, want) {
				t.Errorf("resource attributes %q missing %q; without it the backend cannot tell "+
					"this factory's data from another's on a shared host", attrs, want)
			}
		}
	})

	t.Run("extra resource attributes are appended deterministically", func(t *testing.T) {
		cfg := testConfig()
		cfg.ResourceAttrsExtra = map[string]string{"deployment.environment": "dev", "af.site": "local", "zz.last": "z"}
		first := LaunchEnv(cfg, keys)
		for i := 0; i < 8; i++ {
			again := LaunchEnv(cfg, keys)
			for j := range first {
				if first[j] != again[j] {
					t.Fatalf("variable %d differs between calls: %+v vs %+v — a Go map was ranged "+
						"without sorting, so the emitted environment is not reproducible", j, first[j], again[j])
				}
			}
		}
	})

	t.Run("a secret reference is carried verbatim and never dereferenced here", func(t *testing.T) {
		cfg := testConfig()
		cfg.Headers = map[string]string{"Authorization": "file:.agentfactory/secrets/telemetry.auth"}
		var headers string
		for _, e := range LaunchEnv(cfg, keys) {
			if e.Key == "OTEL_EXPORTER_OTLP_HEADERS" {
				headers = e.Value
			}
		}
		if !strings.Contains(headers, "file:.agentfactory/secrets/telemetry.auth") {
			t.Errorf("headers = %q, want the reference carried through unchanged; dereferencing "+
				"belongs to the launch layer that knows the factory root", headers)
		}
	})

	t.Run("no content-capture variable is ever emitted", func(t *testing.T) {
		cfg := testConfig()
		cfg.ResourceAttrsExtra = map[string]string{"x": "y"}
		for _, e := range LaunchEnv(cfg, keys) {
			// The five content gates default to off upstream, and this package must never be
			// the thing that turns one on. Asserted on the key AND the value, since a value
			// could smuggle one through the resource-attribute string.
			for _, banned := range []string{
				"USER_PROMPTS", "ASSISTANT_RESPONSES", "TOOL_DETAILS", "TOOL_CONTENT", "RAW_API_BODIES",
			} {
				if strings.Contains(e.Key, banned) || strings.Contains(e.Value, banned) {
					t.Errorf("variable %s=%s references a content-capture gate", e.Key, e.Value)
				}
			}
		}
	})

	t.Run("an empty config still yields a well-formed set", func(t *testing.T) {
		got := LaunchEnv(config.TelemetryConfig{}, CorrelationKeys{})
		if len(got) != 7 {
			t.Fatalf("got %d variables for an empty config, want 7; the caller decides whether to "+
				"emit them, and a short slice would silently unset only some of the family", len(got))
		}
		for _, e := range got {
			if e.Key == "" {
				t.Error("a variable with an empty key was emitted")
			}
		}
	})
}
