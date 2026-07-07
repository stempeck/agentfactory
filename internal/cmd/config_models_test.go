package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// runModelsCmd invokes a config-models subcommand's RunE in-process with positional
// args, capturing stdout+stderr into one buffer (mirrors runConfigSet, but threads
// args so `check`/`attest` can receive a profile name).
func runModelsCmd(t *testing.T, fn func(*cobra.Command, []string) error, args ...string) (string, error) {
	t.Helper()
	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	cmd.SetIn(strings.NewReader(""))
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	err := fn(cmd, args)
	return buf.String(), err
}

// --- AC-4: `show` redaction ---

func TestConfigModelsShow_RedactsLiteralToken(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			// literal token on a LOOPBACK endpoint (the only place the validator allows a
			// literal sk- token) — show must still redact it to ****.
			"loop": {
				"ANTHROPIC_MODEL":      "local",
				"ANTHROPIC_BASE_URL":   "http://localhost:1234",
				"ANTHROPIC_AUTH_TOKEN": "sk-SECRETLITERAL",
				"ANTHROPIC_API_KEY":    "",
			},
			// a file: reference must print verbatim.
			"remote": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/x.key",
			},
		},
	})

	out, err := runModelsCmd(t, runConfigModelsShow)
	if err != nil {
		t.Fatalf("runConfigModelsShow: %v (out=%q)", err, out)
	}
	if !strings.Contains(out, "****") {
		t.Errorf("show must redact a literal ANTHROPIC_AUTH_TOKEN to ****; out=%q", out)
	}
	if !strings.Contains(out, "file:secrets/x.key") {
		t.Errorf("show must print a file: reference verbatim; out=%q", out)
	}
	if strings.Contains(out, "sk-SECRETLITERAL") {
		t.Errorf("show must NEVER print the literal secret value (T-2); out=%q", out)
	}
}

// --- AC-4: `check` runs entirely through the httpProbe seam ---

func TestConfigModelsCheck_UsesHTTPProbeSeam(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")

	orig := httpProbe
	called := false
	httpProbe = func(baseURL, authToken string) ([]string, error) {
		called = true
		return []string{"gpt-5.3-codex"}, nil // model present
	}
	t.Cleanup(func() { httpProbe = orig })

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err != nil {
		t.Fatalf("check with a reachable, model-present gateway must succeed; err=%v out=%q", err, out)
	}
	if !called {
		t.Error("check must reach the gateway ONLY through the httpProbe seam (it was never called)")
	}
	if !strings.Contains(out, "transport-level only") {
		t.Errorf("check output must state it is transport-level only; out=%q", out)
	}
	if strings.Contains(out, "sk-real-value") {
		t.Errorf("check must NEVER print token material; out=%q", out)
	}
}

func TestConfigModelsCheck_ModelAbsent_Warns(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")

	orig := httpProbe
	httpProbe = func(baseURL, authToken string) ([]string, error) {
		return []string{"some-other-model"}, nil // the profile's model id is ABSENT
	}
	t.Cleanup(func() { httpProbe = orig })

	out, _ := runModelsCmd(t, runConfigModelsCheck, "codex")
	if !strings.Contains(out, "not in GET /v1/models response") {
		t.Errorf("a model id absent from the gateway must warn (not block); out=%q", out)
	}
}

func TestConfigModelsCheck_Unreachable_FailsNonZero(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")

	orig := httpProbe
	httpProbe = func(baseURL, authToken string) ([]string, error) {
		return nil, os.ErrDeadlineExceeded // unreachable
	}
	t.Cleanup(func() { httpProbe = orig })

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("an unreachable endpoint must fail non-zero; out=%q", out)
	}
	if !strings.Contains(err.Error()+out, "unreachable") {
		t.Errorf("the failure must state the endpoint is unreachable; err=%v out=%q", err, out)
	}
}

// TestConfigModels_HTTPProbeSeamExists backs the AC-4 grep gate
// (`grep -n 'var httpProbe' internal/cmd/config_models.go`).
func TestConfigModels_HTTPProbeSeamExists(t *testing.T) {
	src, err := os.ReadFile("config_models.go")
	if err != nil {
		t.Fatalf("read config_models.go: %v", err)
	}
	if !strings.Contains(string(src), "var httpProbe") {
		t.Error("config_models.go must declare the ADR-009 package-var seam `var httpProbe`")
	}
}

// --- AC-6: `attest` writes the attestation file ---

func TestConfigModelsAttest_WritesAttestationFile(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
		},
	})

	out, err := runModelsCmd(t, runConfigModelsAttest, "codex")
	if err != nil {
		t.Fatalf("attest must succeed for a defined profile; err=%v out=%q", err, out)
	}
	attDir := filepath.Join(root, ".runtime", "model_fitness")
	attPath := filepath.Join(attDir, "codex.json")
	data, err := os.ReadFile(attPath)
	if err != nil {
		t.Fatalf("attest must write %s; %v", attPath, err)
	}
	var att struct {
		Profile    string `json:"profile"`
		AttestedBy string `json:"attested_by"`
		AttestedAt string `json:"attested_at"`
	}
	if err := json.Unmarshal(data, &att); err != nil {
		t.Fatalf("attestation must be valid JSON: %v", err)
	}
	if att.Profile != "codex" {
		t.Errorf("attestation must name the profile; got %+v", att)
	}
	if att.AttestedAt == "" {
		t.Errorf("attestation must record when (attested_at); got %+v", att)
	}
	// Atomic write: no temp residue.
	for _, e := range mustReadDir(t, attDir) {
		if strings.HasSuffix(e, ".tmp") {
			t.Errorf("temp residue after atomic attestation write: %s", e)
		}
	}
}

func TestConfigModelsAttest_UndefinedProfile_Rejected(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{"codex": {"ANTHROPIC_MODEL": "gpt-5.3-codex"}},
	})
	_ = root

	_, err := runModelsCmd(t, runConfigModelsAttest, "ghost")
	if err == nil {
		t.Fatal("attest of an undefined profile must be rejected")
	}
	if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("rejection should name the undefined profile; got %v", err)
	}
}

// --- AC-2: subcommands registered ---

func TestConfigModels_ShowCheckAttest_Registered(t *testing.T) {
	findChild := func(parent *cobra.Command, name string) *cobra.Command {
		for _, c := range parent.Commands() {
			if c.Name() == name {
				return c
			}
		}
		return nil
	}
	for _, sub := range []string{"show", "check", "attest"} {
		if findChild(configModelsCmd, sub) == nil {
			t.Errorf("`config models %s` is not registered under configModelsCmd", sub)
		}
	}
}

// --- AC-5: --skip-fitness flag registered on both entrypoints ---

func TestSling_SkipFitnessFlag_Registered(t *testing.T) {
	if slingCmd.Flags().Lookup("skip-fitness") == nil {
		t.Error("af sling must register a --skip-fitness flag")
	}
}

func TestUp_SkipFitnessFlag_Registered(t *testing.T) {
	if upCmd.Flags().Lookup("skip-fitness") == nil {
		t.Error("af up must register a --skip-fitness flag")
	}
}
