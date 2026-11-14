package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func seedUnitLintLine(profile, key string) string {
	return `warning: models.json: profile "` + profile + `" has a plaintext ` + key +
		` — use a "file:" reference or confirm this is an intentional placeholder`
}

func seedUnitDecodeMap(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("not a JSON object: %v\n%s", err, data)
	}
	return m
}

// seedUnitStartupSeed omits "tokenomics" and most "recovery" knobs on purpose: a typed
// StartupConfig round-trip would re-emit them as zero values, so only a key-level
// rewrite keeps the decoded map equal to the input's.
func seedUnitStartupSeed(telemetryField string) []byte {
	return seedUnitStartupSeedWith(`"improvement":"off",`, telemetryField)
}

func seedUnitStartupSeedWith(improvementField, telemetryField string) []byte {
	return []byte(`{"agents":["manager"],"quality":"default","fidelity":"on",` +
		improvementField + telemetryField +
		`"start_dispatch":true,"watchdog_agents":["manager"],"recovery":{"enabled":true,"context_threshold_pct":85},"step_context":{"bound_tokens":256000,"handoff_pct":75}}`)
}

func seedUnitWriteImprovementHook(t *testing.T, factoryRoot, state string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(improvementHookFile(factoryRoot)), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(improvementHookFile(factoryRoot), []byte(state+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

// TestSanitizeSeed_StrictDecodeRejectsUnknownKey pins that the RAW seed bytes are
// strict-decoded, so an unknown key is refused instead of being silently re-encoded away.
func TestSanitizeSeed_StrictDecodeRejectsUnknownKey(t *testing.T) {
	cases := []struct {
		label string
		name  string
		data  string
	}{
		{"dispatch_top_level", "dispatch.json", `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[],"interval_seconds":300,"retry_after_seconds":1800,"bogus_field":true}`},
		{"factory_top_level", "factory.json", `{"type":"factory","version":1,"name":"x","bogus_field":true}`},
		{"models_top_level", "models.json", `{"default":"default","models":{"default":{}},"agents":{},"bogus_field":true}`},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			out, _, err := sanitizeSeed(c.name, []byte(c.data), t.TempDir())
			if err == nil {
				t.Fatalf("sanitizeSeed(%s) accepted an unknown key and re-encoded it away (output %s); want an error naming it", c.name, out)
			}
			for _, want := range []string{c.name, `unknown field "bogus_field"`} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

// TestSanitizeSeed_DispatchCronsDropped pins that both dispatcher trigger sections
// are neutralised (repos -> [], crons dropped) and every other key survives unchanged.
func TestSanitizeSeed_DispatchCronsDropped(t *testing.T) {
	in := []byte(`{"repos":["some/repo"],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"agent":"x"}],"interval_seconds":300,"retry_after_seconds":1800,"remove_trigger_after_dispatch":false,"workflows":[{"label":"pipeline","phases":["bug"]}],"crons":[{"name":"vault-audit","agent":"x","every":"4h"}]}`)

	out, _, err := sanitizeSeed("dispatch.json", in, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := seedUnitDecodeMap(t, out)
	if crons, present := got["crons"]; present {
		t.Errorf("crons survived the seed (%#v); a seeded dispatch.json must carry no crons trigger", crons)
	}
	delete(got, "crons")

	want := seedUnitDecodeMap(t, in)
	delete(want, "crons")
	want["repos"] = []any{}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("seeded dispatch.json (crons aside) = %#v\nwant the input with only repos cleared: %#v", got, want)
	}
}

// TestSanitizeSeed_TelemetryOnBecomesDefaultWithoutTelemetryJSON pins that, with
// no telemetry.json, a seeded "telemetry":"on" becomes "default" and nothing else changes.
func TestSanitizeSeed_TelemetryOnBecomesDefaultWithoutTelemetryJSON(t *testing.T) {
	in := seedUnitStartupSeed(`"telemetry":"on",`)

	out, _, err := sanitizeSeed("startup.json", in, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := seedUnitDecodeMap(t, out)
	if got["telemetry"] != "default" {
		t.Errorf("telemetry = %#v, want \"default\" (no telemetry.json under the factory root)", got["telemetry"])
	}
	delete(got, "telemetry")
	want := seedUnitDecodeMap(t, in)
	delete(want, "telemetry")
	if !reflect.DeepEqual(got, want) {
		t.Errorf("seeded startup.json (telemetry aside) = %#v\nwant every other key preserved: %#v", got, want)
	}
}

// TestSanitizeSeed_StartupPassesThroughByteIdentical pins that rewrite's boundary: every case
// other than exactly "on" with telemetry.json absent returns the input bytes untouched.
func TestSanitizeSeed_StartupPassesThroughByteIdentical(t *testing.T) {
	cases := []struct {
		label         string
		field         string
		telemetryJSON bool
	}{
		{"telemetry_json_present", `"telemetry":"on",`, true},
		{"telemetry_off", `"telemetry":"off",`, false},
		{"telemetry_default", `"telemetry":"default",`, false},
		{"telemetry_absent", ``, false},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			factoryRoot := t.TempDir()
			if c.telemetryJSON {
				cfgDir := filepath.Join(factoryRoot, ".agentfactory")
				if err := os.MkdirAll(cfgDir, 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(cfgDir, "telemetry.json"), []byte(`{"endpoint":"http://localhost:4318"}`+"\n"), 0644); err != nil {
					t.Fatal(err)
				}
			}
			in := seedUnitStartupSeed(c.field)
			out, _, err := sanitizeSeed("startup.json", in, factoryRoot)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(out, in) {
				t.Errorf("startup.json mutated:\n got %s\nwant %s (byte-identical pass-through)", out, in)
			}
		})
	}
}

const seedUnitImprovementRecord = `improvement "on" -> "default" (.improvement-hook not on)`

// A seeded "improvement":"on" would write .improvement-hook at the first af up and arm the
// factory half of the improvement toggle, which `af install --init` must never do.
func TestSanitizeSeed_ImprovementOnBecomesDefaultUnlessFactoryHookOn(t *testing.T) {
	for _, hook := range []string{"", "off"} {
		t.Run("hook="+hook, func(t *testing.T) {
			factoryRoot := t.TempDir()
			if hook != "" {
				seedUnitWriteImprovementHook(t, factoryRoot, hook)
			}
			in := seedUnitStartupSeedWith(`"improvement":"on",`, `"telemetry":"off",`)

			out, records, err := sanitizeSeed("startup.json", in, factoryRoot)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := seedUnitDecodeMap(t, out)
			if got["improvement"] != "default" {
				t.Errorf("improvement = %#v, want \"default\"", got["improvement"])
			}
			delete(got, "improvement")
			want := seedUnitDecodeMap(t, in)
			delete(want, "improvement")
			if !reflect.DeepEqual(got, want) {
				t.Errorf("seeded startup.json (improvement aside) = %#v\nwant every other key preserved: %#v", got, want)
			}
			if !reflect.DeepEqual(records, []string{seedUnitImprovementRecord}) {
				t.Errorf("records = %q, want [%q]", records, seedUnitImprovementRecord)
			}
		})
	}
}

func TestSanitizeSeed_ImprovementRelaxBoundaryByteIdentical(t *testing.T) {
	cases := []struct {
		label string
		field string
		hook  string
	}{
		{"factory_hook_on", `"improvement":"on",`, "on"},
		{"improvement_off", `"improvement":"off",`, ""},
		{"improvement_default", `"improvement":"default",`, ""},
		{"improvement_absent", ``, ""},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			factoryRoot := t.TempDir()
			if c.hook != "" {
				seedUnitWriteImprovementHook(t, factoryRoot, c.hook)
			}
			in := seedUnitStartupSeedWith(c.field, `"telemetry":"off",`)
			out, records, err := sanitizeSeed("startup.json", in, factoryRoot)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !bytes.Equal(out, in) || len(records) != 0 {
				t.Errorf("startup.json mutated (records %q):\n got %s\nwant %s (byte-identical pass-through)", records, out, in)
			}
		})
	}
}

func TestSanitizeSeed_TelemetryAndImprovementBothRelaxed(t *testing.T) {
	in := seedUnitStartupSeedWith(`"improvement":"on",`, `"telemetry":"on",`)
	out, records, err := sanitizeSeed("startup.json", in, t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := seedUnitDecodeMap(t, out)
	if got["telemetry"] != "default" || got["improvement"] != "default" {
		t.Errorf("telemetry = %#v, improvement = %#v; want both \"default\"", got["telemetry"], got["improvement"])
	}
	want := []string{`telemetry "on" -> "default" (no telemetry.json)`, seedUnitImprovementRecord}
	if !reflect.DeepEqual(records, want) {
		t.Errorf("records = %q, want %q", records, want)
	}
}

// TestSeedValidation_LoaderRulesRefuseRegardlessOfAgentsFreshness pins that the
// loader's full models validator runs at seed time as a hard error, even when agents.json is
// pre-existing (agentsAreFresh=false selects warn-only severity for agent-ref checks only).
func TestSeedValidation_LoaderRulesRefuseRegardlessOfAgentsFreshness(t *testing.T) {
	cases := []struct {
		label string
		data  string
		want  []string
	}{
		{"orphan_agent_pin",
			`{"default":"default","models":{"default":{}},"agents":{"manager":"no-such-profile"}}`,
			[]string{"models.json", `"manager"`, `"no-such-profile"`, "undefined model"}},
		{"orphan_default",
			`{"default":"ghost-profile","models":{"other":{}},"agents":{}}`,
			[]string{"models.json", `"ghost-profile"`, "undefined model"}},
		{"non_empty_anthropic_api_key",
			`{"default":"default","models":{"default":{},"leaky":{"ANTHROPIC_API_KEY":"plain-api-key-123"}},"agents":{}}`,
			[]string{"models.json", `"leaky"`, "ANTHROPIC_API_KEY"}},
		{"sk_token_without_base_url",
			`{"default":"default","models":{"default":{},"leaky":{"ANTHROPIC_AUTH_TOKEN":"sk-live-abc123token"}},"agents":{}}`,
			[]string{"models.json", `"leaky"`, "ANTHROPIC_AUTH_TOKEN"}},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			var buf bytes.Buffer
			err := validateSeed("models.json", []byte(c.data), twoAgentRoster(), false, &buf)
			if err == nil {
				t.Fatalf("validateSeed accepted a models.json the loader rejects (warnings: %q); want a hard error even with agentsAreFresh=false", buf.String())
			}
			for _, want := range c.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

var seedUnitLoaderRejectedStartups = []struct {
	label, data, want string
}{
	{"fidelity_strict", `{"agents":["manager"],"quality":"default","fidelity":"strict","improvement":"off","telemetry":"off","watchdog_agents":["manager"]}`,
		`startup fidelity must be "on", "off", or "default", got "strict"`},
	{"quality_high", `{"agents":["manager"],"quality":"high","fidelity":"on","improvement":"off","telemetry":"off","watchdog_agents":["manager"]}`,
		`startup quality must be "on", "off", or "default", got "high"`},
	{"improvement_bogus", `{"agents":["manager"],"improvement":"maybe","watchdog_agents":["manager"]}`,
		`startup improvement must be "on", "off", or "default", got "maybe"`},
	{"advisory_at_threshold", `{"agents":["manager"],"watchdog_agents":["manager"],"recovery":{"context_threshold_pct":80,"context_advisory_pct":80}}`,
		`context_advisory_pct (80) must be below context_threshold_pct (80)`},
}

// A startup.json LoadStartupConfig rejects breaks every af up whatever agents.json holds, so
// unlike an agent-ref finding it is refused even beside a pre-existing agents.json.
func TestSeedValidation_StartupLoaderRulesRefuseRegardlessOfAgentsFreshness(t *testing.T) {
	for _, c := range seedUnitLoaderRejectedStartups {
		for _, fresh := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s/agentsAreFresh=%v", c.label, fresh), func(t *testing.T) {
				var warn bytes.Buffer
				err := validateSeed("startup.json", []byte(c.data), twoAgentRoster(), fresh, &warn)
				if err == nil {
					t.Fatalf("validateSeed accepted a startup.json the loader rejects (warnings: %q)", warn.String())
				}
				for _, want := range []string{"startup.json", c.want} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q missing %q", err.Error(), want)
					}
				}
			})
		}
	}
}

func TestSeedValidation_StartupSeedVerdictMatchesLoader(t *testing.T) {
	docs := map[string]string{
		"valid": `{"agents":["manager"],"quality":"default","fidelity":"on","improvement":"off","telemetry":"off","watchdog_agents":["manager"]}`,
	}
	for _, c := range seedUnitLoaderRejectedStartups {
		docs[c.label] = c.data
	}
	for label, doc := range docs {
		t.Run(label, func(t *testing.T) {
			seedErr := validateSeed("startup.json", []byte(doc), twoAgentRoster(), true, io.Discard)
			root := t.TempDir()
			if err := os.MkdirAll(config.ConfigDir(root), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(config.ConfigDir(root), "startup.json"), []byte(doc), 0644); err != nil {
				t.Fatal(err)
			}
			_, loadErr := config.LoadStartupConfig(root)
			if (seedErr == nil) != (loadErr == nil) {
				t.Errorf("seed verdict %v disagrees with the loader's %v", seedErr, loadErr)
			}
		})
	}
}

// TestSeedValidation_NonPortableDefaultRefused pins that a default naming a profile
// the pin-drop predicate calls non-portable is refused, never cleared or re-pointed.
func TestSeedValidation_NonPortableDefaultRefused(t *testing.T) {
	cases := []struct {
		label   string
		profile string
		data    string
	}{
		{"loopback_gateway_file_token", "codex",
			`{"default":"codex","models":{"codex":{"ANTHROPIC_BASE_URL":"http://localhost:4000","ANTHROPIC_AUTH_TOKEN":"file:.agentfactory/secrets/litellm.key","ANTHROPIC_MODEL":"gpt-5.6-sol"}},"agents":{}}`},
		{"non_loopback_base_url_no_file_token", "remote",
			`{"default":"remote","models":{"remote":{"ANTHROPIC_BASE_URL":"https://gateway.example.com","ANTHROPIC_AUTH_TOKEN":"remote-placeholder","ANTHROPIC_MODEL":"m"}},"agents":{}}`},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			var buf bytes.Buffer
			err := validateSeed("models.json", []byte(c.data), twoAgentRoster(), false, &buf)
			if err == nil {
				t.Fatalf("validateSeed accepted non-portable default %q (warnings: %q); want a refusal", c.profile, buf.String())
			}
			want := `default "` + c.profile + `" names a non-portable profile (gateway endpoint or file: secret reference) — set models.json "default" to a profile with no gateway endpoint or secret reference in the source clone`
			for _, w := range []string{"models.json", want} {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error %q missing %q", err.Error(), w)
				}
			}
		})
	}
}

// TestSeedValidation_PortableDefaultAccepted is the inverse of that refusal: a default with no
// gateway endpoint and no secret reference is accepted silently.
func TestSeedValidation_PortableDefaultAccepted(t *testing.T) {
	data := `{"default":"default","models":{"default":{"ANTHROPIC_MODEL":"claude-opus-4-8"}},"agents":{}}`
	var buf bytes.Buffer
	if err := validateSeed("models.json", []byte(data), twoAgentRoster(), false, &buf); err != nil {
		t.Fatalf("portable default refused: %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("unexpected warning for a portable default: %q", buf.String())
	}
}

// TestSeedValidation_PlaintextAPIKeyWarnsNamingProfileAndKey pins the plaintext-key warning. GEMINI_API_KEY,
// not OPENAI_API_KEY: main's validator hard-rejects the latter, so it would not survive the merge.
func TestSeedValidation_PlaintextAPIKeyWarnsNamingProfileAndKey(t *testing.T) {
	const secret = "AIzaPlaintextGeminiValue"
	data := `{"default":"default","models":{"default":{},"gem":{"GEMINI_API_KEY":"` + secret + `"}},"agents":{}}`
	var buf bytes.Buffer
	if err := validateSeed("models.json", []byte(data), twoAgentRoster(), true, &buf); err != nil {
		t.Fatalf("secret lint is warn-only; a plaintext GEMINI_API_KEY must not refuse the seed: %v", err)
	}
	want := seedUnitLintLine("gem", "GEMINI_API_KEY")
	if !strings.Contains(buf.String(), want) {
		t.Errorf("warnings %q do not carry %q", buf.String(), want)
	}
	if strings.Contains(buf.String(), secret) {
		t.Errorf("warning echoes the secret value: %q", buf.String())
	}
}

// TestSeedValidation_LintCoversEveryAPIKeySortedByProfileThenKey pins the lint's coverage
// (ANTHROPIC_AUTH_TOKEN plus every *_API_KEY) and its deterministic profile-then-key order.
func TestSeedValidation_LintCoversEveryAPIKeySortedByProfileThenKey(t *testing.T) {
	data := `{"default":"default","models":{"default":{},` +
		`"beta":{"MISTRAL_API_KEY":"plain-m","GEMINI_API_KEY":"plain-g"},` +
		`"alpha":{"GEMINI_API_KEY":"plain-ga","ANTHROPIC_AUTH_TOKEN":"plain-ta"}},"agents":{}}`
	var buf bytes.Buffer
	if err := validateSeed("models.json", []byte(data), twoAgentRoster(), true, &buf); err != nil {
		t.Fatalf("secret lint is warn-only: %v", err)
	}
	got := buf.String()
	wantOrder := []string{
		seedUnitLintLine("alpha", "ANTHROPIC_AUTH_TOKEN"),
		seedUnitLintLine("alpha", "GEMINI_API_KEY"),
		seedUnitLintLine("beta", "GEMINI_API_KEY"),
		seedUnitLintLine("beta", "MISTRAL_API_KEY"),
	}
	last := -1
	for _, line := range wantOrder {
		idx := strings.Index(got, line)
		if idx < 0 {
			t.Errorf("missing warning %q", line)
			continue
		}
		if idx < last {
			t.Errorf("warning %q out of profile-then-key sorted order in %q", line, got)
		}
		last = idx
	}
	if n := strings.Count(got, "warning:"); n != len(wantOrder) {
		t.Errorf("got %d warning lines, want %d:\n%s", n, len(wantOrder), got)
	}
}

// TestSeedValidation_EmptyOrFileAPIKeySilent pins the lint's carve-outs on the widened key set.
func TestSeedValidation_EmptyOrFileAPIKeySilent(t *testing.T) {
	cases := []struct {
		label string
		data  string
	}{
		{"empty_anthropic_api_key", `{"default":"default","models":{"default":{"ANTHROPIC_API_KEY":""}},"agents":{}}`},
		{"empty_gemini_api_key", `{"default":"default","models":{"default":{},"gem":{"GEMINI_API_KEY":""}},"agents":{}}`},
		{"file_ref_gemini_api_key", `{"default":"default","models":{"default":{},"gem":{"GEMINI_API_KEY":"file:.agentfactory/secrets/gemini.key"}},"agents":{}}`},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			var buf bytes.Buffer
			if err := validateSeed("models.json", []byte(c.data), twoAgentRoster(), true, &buf); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if buf.Len() != 0 {
				t.Errorf("unexpected warning: %q", buf.String())
			}
		})
	}
}

// TestSeedValidation_EndpointScopedPlaceholderRule pins that an
// auth token on a local endpoint (loopback or host.docker.internal) is non-secret unless it is
// sk- shaped; elsewhere every plaintext value warns, whatever its spelling.
func TestSeedValidation_EndpointScopedPlaceholderRule(t *testing.T) {
	cases := []struct {
		label    string
		data     string
		wantLine string
		secret   string
	}{
		{"a_renamed_placeholder_on_host_docker_internal_silent",
			`{"default":"default","models":{"default":{},"ollama":{"ANTHROPIC_BASE_URL":"http://host.docker.internal:11434","ANTHROPIC_AUTH_TOKEN":"ollama-local","ANTHROPIC_MODEL":"qwen/qwen3.8-27b"}},"agents":{}}`,
			"", ""},
		{"b_sk_token_on_loopback_warns",
			`{"default":"default","models":{"default":{},"codex":{"ANTHROPIC_BASE_URL":"http://localhost:4000","ANTHROPIC_AUTH_TOKEN":"sk-litellm-0123abcd","ANTHROPIC_MODEL":"gpt-5.6-sol"}},"agents":{}}`,
			seedUnitLintLine("codex", "ANTHROPIC_AUTH_TOKEN"), "sk-litellm-0123abcd"},
		{"c_lmstudio_on_remote_endpoint_warns",
			`{"default":"default","models":{"default":{},"remote":{"ANTHROPIC_BASE_URL":"https://lm.example.com","ANTHROPIC_AUTH_TOKEN":"lmstudio","ANTHROPIC_MODEL":"m"}},"agents":{}}`,
			seedUnitLintLine("remote", "ANTHROPIC_AUTH_TOKEN"), ""},
		{"d_endpointless_plaintext_warns",
			`{"default":"default","models":{"default":{},"plain":{"ANTHROPIC_AUTH_TOKEN":"plaintext-abc123"}},"agents":{}}`,
			seedUnitLintLine("plain", "ANTHROPIC_AUTH_TOKEN"), "plaintext-abc123"},
		{"e_provider_key_on_host_docker_internal_warns",
			`{"default":"default","models":{"default":{},"local":{"ANTHROPIC_BASE_URL":"http://host.docker.internal:11434","ANTHROPIC_AUTH_TOKEN":"file:.agentfactory/secrets/local.key","GEMINI_API_KEY":"gemini-local"}},"agents":{}}`,
			seedUnitLintLine("local", "GEMINI_API_KEY"), "gemini-local"},
		{"f_placeholder_on_localhost_silent",
			`{"default":"default","models":{"default":{},"lmstudio":{"ANTHROPIC_BASE_URL":"http://localhost:1234","ANTHROPIC_AUTH_TOKEN":"lm-studio","ANTHROPIC_MODEL":"m"}},"agents":{}}`,
			"", ""},
		{"g_placeholder_on_127_0_0_1_silent",
			`{"default":"default","models":{"default":{},"lmstudio":{"ANTHROPIC_BASE_URL":"http://127.0.0.1:1234","ANTHROPIC_AUTH_TOKEN":"lm-studio","ANTHROPIC_MODEL":"m"}},"agents":{}}`,
			"", ""},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			var buf bytes.Buffer
			if err := validateSeed("models.json", []byte(c.data), twoAgentRoster(), true, &buf); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := buf.String()
			if c.wantLine == "" {
				if got != "" {
					t.Errorf("unexpected warning(s) for a non-credential token on a local endpoint: %q", got)
				}
				return
			}
			if !strings.Contains(got, c.wantLine) {
				t.Errorf("warnings %q do not carry %q", got, c.wantLine)
			}
			if n := strings.Count(got, "warning:"); n != 1 {
				t.Errorf("got %d warning lines, want 1:\n%s", n, got)
			}
			if c.secret != "" && strings.Contains(got, c.secret) {
				t.Errorf("warning echoes the token value: %q", got)
			}
		})
	}
}

// TestSeedValidation_ProviderKeyWarnsOnLocalEndpoint pins that a
// *_API_KEY is an upstream provider credential the gateway forwards, so a local endpoint does not
// make it a placeholder. Only ANTHROPIC_AUTH_TOKEN, the value the local server itself receives,
// earns the local-endpoint exemption; empty and file: provider keys stay silent everywhere.
func TestSeedValidation_ProviderKeyWarnsOnLocalEndpoint(t *testing.T) {
	const secret = "AIzaPlaintextProviderKey0123"
	gateway := func(gemini string) string {
		return `{"default":"default","models":{"default":{},"codex":{"ANTHROPIC_BASE_URL":"http://localhost:4000",` +
			`"ANTHROPIC_AUTH_TOKEN":"file:.agentfactory/secrets/litellm.key","GEMINI_API_KEY":"` + gemini + `",` +
			`"ANTHROPIC_MODEL":"gpt-5.6-sol"}},"agents":{}}`
	}

	t.Run("plaintext_provider_key_on_loopback_gateway_warns", func(t *testing.T) {
		var buf bytes.Buffer
		if err := validateSeed("models.json", []byte(gateway(secret)), twoAgentRoster(), true, &buf); err != nil {
			t.Fatalf("secret lint is warn-only; a plaintext provider key must not refuse the seed: %v", err)
		}
		got := buf.String()
		if want := seedUnitLintLine("codex", "GEMINI_API_KEY"); !strings.Contains(got, want) {
			t.Errorf("warnings %q do not carry %q", got, want)
		}
		if n := strings.Count(got, "warning:"); n != 1 {
			t.Errorf("got %d warning lines, want 1 (the file: auth token must stay silent):\n%s", n, got)
		}
		if strings.Contains(got, secret) {
			t.Errorf("warning echoes the provider key value: %q", got)
		}
	})

	for _, c := range []struct{ label, gemini string }{
		{"empty_provider_key_on_loopback_gateway_silent", ""},
		{"file_ref_provider_key_on_loopback_gateway_silent", "file:.agentfactory/secrets/gemini.key"},
	} {
		t.Run(c.label, func(t *testing.T) {
			var buf bytes.Buffer
			if err := validateSeed("models.json", []byte(gateway(c.gemini)), twoAgentRoster(), true, &buf); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if buf.Len() != 0 {
				t.Errorf("unexpected warning: %q", buf.String())
			}
		})
	}
}

// TestSeedDispatchInert_RefusesAnArmingCandidate pins that the
// seam re-parses its own sanitized dispatch.json through the loader and refuses to write one the
// dispatcher would act on, so a trigger section the sanitizer does not know about fails at seed
// time instead of polling a foreign repo.
func TestSeedDispatchInert_RefusesAnArmingCandidate(t *testing.T) {
	cases := []struct {
		label     string
		data      string
		refuse    bool
		wantExtra string
	}{
		{"repos_trigger_and_mapping_arms", `{"repos":["some/repo"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"x"}]}`, true, ""},
		{"crons_only_arms", `{"repos":[],"crons":[{"name":"vault-audit","agent":"x","every":"4h"}]}`, true, ""},
		{"sanitized_shape_is_inert", `{"repos":[],"trigger_label":"agentic","notify_on_complete":"manager","mappings":[{"labels":["bug"],"agent":"x"}],"interval_seconds":300,"retry_after_seconds":1800}`, false, ""},
		{"arming_mapping_invalid", `{"repos":["some/repo"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"x","source":"bogus"}]}`, true, "invalid config"},
		{"arming_cron_invalid", `{"repos":[],"crons":[{"name":"c","agent":"x","every":"bogus"}]}`, true, "invalid config"},
	}
	for _, c := range cases {
		t.Run(c.label, func(t *testing.T) {
			err := checkSeedDispatchInert("dispatch.json", []byte(c.data))
			if !c.refuse {
				if err != nil {
					t.Fatalf("an inert dispatch.json was refused: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("a dispatch.json the dispatcher would act on was accepted: %s", c.data)
			}
			for _, want := range []string{"dispatch.json", "arm the dispatcher", c.wantExtra} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err.Error(), want)
				}
			}
		})
	}
}

// Both arm refusals mean the sanitized output escaped the not-configured verdict: the clone is
// not at fault, this binary's sanitizer is, so the clone remedy would send the operator after
// the wrong file.
func TestWithCloneRemedy_ArmingRefusalNamesSanitizerGap(t *testing.T) {
	const afSrc = "/clone"
	for label, data := range map[string]string{
		"still_arms":               `{"repos":["r"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"x"}]}`,
		"arms_with_invalid_config": `{"repos":["some/repo"],"trigger_label":"agentic","mappings":[{"labels":["bug"],"agent":"x","source":"bogus"}]}`,
	} {
		t.Run(label, func(t *testing.T) {
			inert := checkSeedDispatchInert("dispatch.json", []byte(data))
			if inert == nil {
				t.Fatal("checkSeedDispatchInert accepted an arming dispatch.json")
			}
			msg := withCloneRemedy(fmt.Errorf("sanitizing dispatch.json: %w", inert), "dispatch.json", afSrc, false).Error()
			for _, notWant := range []string{seamFixRemedy(afSrc, "dispatch.json"), seamRestoreRemedy(afSrc, "dispatch.json")} {
				if strings.Contains(msg, notWant) {
					t.Errorf("arm refusal carries the clone remedy %q:\n%s", notWant, msg)
				}
			}
			for _, want := range []string{"seed sanitizer", "report it", seamCloneFile(afSrc, "dispatch.json")} {
				if !strings.Contains(msg, want) {
					t.Errorf("arm refusal %q missing %q", msg, want)
				}
			}
		})
	}
}

// On the no-source channel nothing came from a clone, so no clone remedy applies.
func TestWithCloneRemedy_NoSourceChannelLeavesErrorUntouched(t *testing.T) {
	for label, base := range map[string]error{
		"absent":  fmt.Errorf("resolving messaging.json: %w", fs.ErrNotExist),
		"invalid": errors.New("validating messaging.json: bad"),
	} {
		t.Run(label, func(t *testing.T) {
			if got := withCloneRemedy(base, "messaging.json", "/factory/root", true); got != base {
				t.Errorf("no-source channel error rewritten: %q", got)
			}
			if got := withCloneRemedy(base, "messaging.json", "/clone", false); got == base {
				t.Errorf("clone channel error got no remedy: %q", got)
			}
		})
	}
}

// seedConfigNames is a closed list: a name nobody gave an arm must fail, never pass through
// unsanitized and unvalidated.
func TestSeedArms_UnlistedNameFailsClosed(t *testing.T) {
	const name = "notes.json"
	if _, _, err := sanitizeSeed(name, []byte(`{}`), t.TempDir()); err == nil || !strings.Contains(err.Error(), name) {
		t.Errorf("sanitizeSeed(%s) = %v, want an error naming it", name, err)
	}
	if _, err := strictDecodeSeed(name, []byte(`{}`)); err == nil || !strings.Contains(err.Error(), name) {
		t.Errorf("strictDecodeSeed(%s) = %v, want an error naming it", name, err)
	}
	if err := validateSeed(name, []byte(`{}`), twoAgentRoster(), true, io.Discard); err == nil || !strings.Contains(err.Error(), name) {
		t.Errorf("validateSeed(%s) = %v, want an error naming it", name, err)
	}
}

func TestSeedArms_EveryManifestNameHasAnExplicitArm(t *testing.T) {
	agentsJSON, err := marshalSanitized(builtinAgentsConfig())
	if err != nil {
		t.Fatal(err)
	}
	content := starterSrcCloneConfigs(map[string]string{"agents.json": string(agentsJSON)})
	for _, name := range seedConfigNames {
		out, _, err := sanitizeSeed(name, []byte(content[name]), t.TempDir())
		if err != nil {
			t.Errorf("sanitizeSeed(%s): %v", name, err)
			continue
		}
		if _, err := strictDecodeSeed(name, out); err != nil {
			t.Errorf("strictDecodeSeed(%s): %v", name, err)
		}
	}
}

// The two predicates answer different questions about the same endpoint: whether a pin to it
// can launch in another factory (no), and whether its token can be a real secret (no).
func TestSeedPredicates_HostDockerInternalPinDroppedTokenPlaceholder(t *testing.T) {
	const endpoint = "http://host.docker.internal:11434"
	if !nonPortableProfile(map[string]string{baseURLKey: endpoint, authTokenKey: "lmstudio"}) {
		t.Error("a host.docker.internal profile is reported portable; a pin to it would be kept")
	}
	if !localPlaceholderSecret("lmstudio", endpoint) {
		t.Error("a host.docker.internal token is reported as a possible secret; the tracked lmstudio profile would warn on every bootstrap")
	}
}
