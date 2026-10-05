package session

import (
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func integrationTestManager() *Manager {
	return newTestManager("/tmp/factory", "intagent", config.AgentEntry{Type: "autonomous", Description: "test"})
}

func indexOrFail(t *testing.T, line, sub string) int {
	t.Helper()
	i := strings.Index(line, sub)
	if i < 0 {
		t.Fatalf("launch line lacks %q:\n%s", sub, line)
	}
	return i
}

func TestBuildStartupCommand_PluginDirBetweenModelAndPrompt(t *testing.T) {
	m := integrationTestManager()
	m.c.ModelEnv = []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}}
	m.c.PluginDirs = []string{"/f/int/acme-int/abc/claude-plugin", "/f/int/with space/def/claude-plugin"}
	m.SetInitialPrompt("af prime")

	line := startupLine(t, m)

	model := indexOrFail(t, line, " --model 'claude-opus-4'")
	d1 := indexOrFail(t, line, " --plugin-dir '/f/int/acme-int/abc/claude-plugin'")
	d2 := indexOrFail(t, line, " --plugin-dir '/f/int/with space/def/claude-plugin'")
	prompt := indexOrFail(t, line, " 'af prime'")
	if !(model < d1 && d1 < d2 && d2 < prompt) {
		t.Errorf("want --model < --plugin-dir d1 < --plugin-dir d2 < prompt, got offsets %d %d %d %d:\n%s", model, d1, d2, prompt, line)
	}
	if got := strings.Count(line, "--plugin-dir"); got != 2 {
		t.Errorf("--plugin-dir count = %d, want 2 (one per dir):\n%s", got, line)
	}
	assertShellParses(t, line)
	assertShellParses(t, respawnPrefix+line)
}

func TestBuildStartupCommand_IntegrationEnvSurvivesUnset(t *testing.T) {
	m := integrationTestManager()
	m.c.ModelEnv = []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}}
	m.c.ModelKeyUniverse = []string{"ANTHROPIC_MODEL", "ACME_INT_A"}
	m.c.IntegrationEnv = []config.EnvVar{
		{Key: "ACME_INT_A", Value: "v"},
		{Key: "ACME_INT_B", Value: "file:secrets/x"},
	}
	m.c.IntegrationKeyUniverse = []string{"ACME_INT_A", "ACME_INT_B", "ACME_INT_OLD"}
	m.c.HookFailModes = "acme-int=open"

	line := startupLine(t, m)

	a := indexOrFail(t, line, " ACME_INT_A='v'")
	indexOrFail(t, line, ` ACME_INT_B="$(cat '/tmp/factory/secrets/x')"`)
	indexOrFail(t, line, " AF_INTEGRATION_HOOK_FAIL_MODES='acme-int=open'")
	for _, k := range []string{"ACME_INT_A", "ACME_INT_B", "AF_INTEGRATION_HOOK_FAIL_MODES"} {
		if hasUnsetToken(line, k) {
			t.Errorf("%s is exported by this launch but also unset right after:\n%s", k, line)
		}
	}
	if !hasUnsetToken(line, "ACME_INT_OLD") {
		t.Errorf("ACME_INT_OLD (installed, not bound) must be cleared by a true unset:\n%s", line)
	}
	if model := indexOrFail(t, line, "ANTHROPIC_MODEL="); a < model {
		t.Errorf("integration env must follow the model family (ACME_INT_A at %d, model at %d):\n%s", a, model, line)
	}
	if tel := indexOrFail(t, line, "OTEL_RESOURCE_ATTRIBUTES="); a < tel {
		t.Errorf("integration env must follow the telemetry family (ACME_INT_A at %d, telemetry at %d):\n%s", a, tel, line)
	}
	if amp := indexOrFail(t, line, " && "); a > amp {
		t.Errorf("integration env must be on the export statement, before the first &&:\n%s", line)
	}
	assertShellParses(t, line)

	t.Run("start_writes_no_integration_key_into_tmux", func(t *testing.T) {
		mgr, fake := startMouseAgent(t, nil)
		mgr.c.IntegrationEnv = []config.EnvVar{{Key: "ACME_INT_A", Value: "v"}}
		mgr.c.IntegrationKeyUniverse = []string{"ACME_INT_A"}
		mgr.c.HookFailModes = "acme-int=open"
		mgr.c.PluginDirs = []string{"/f/int/acme-int/abc/claude-plugin"}
		if err := mgr.Start(); err != nil {
			t.Fatalf("Start: %v", err)
		}
		for _, k := range []string{"ACME_INT_A", "AF_INTEGRATION_HOOK_FAIL_MODES"} {
			assertNoTmuxEnvKey(t, fake.ops, mgr.SessionID(), k)
		}
		sent := sentLine(t, fake.ops, mgr.SessionID())
		if !strings.Contains(sent, "ACME_INT_A=") || !strings.Contains(sent, "--plugin-dir") {
			t.Errorf("Start's launch line must carry the integration contributions:\n%s", sent)
		}
	})
}

func TestBuildStartupCommand_InvalidIntegrationKeyDropped(t *testing.T) {
	m := integrationTestManager()
	m.c.IntegrationEnv = []config.EnvVar{{Key: "FIX-TURE", Value: "x"}, {Key: "GOOD", Value: "y"}}
	m.c.IntegrationKeyUniverse = []string{"FIX-TURE", "GOOD"}

	line := startupLine(t, m)

	if strings.Contains(line, "FIX-TURE") {
		t.Errorf("an invalid key must be neither exported nor unset:\n%s", line)
	}
	indexOrFail(t, line, " GOOD='y'")
	assertShellParses(t, line)
}

// An integration exporting a key the model or telemetry family already emitted would follow it on the
// same export statement and win, redirecting that traffic; each way af records a key as its own must
// make the integration's duplicate disappear.
func TestBuildStartupCommand_ModelAndTelemetryEnvWinOverIntegrationEnv(t *testing.T) {
	tests := []struct {
		name  string
		entry config.AgentEntry
		model []config.EnvVar
		telem []config.EnvVar
		key   string
		afSet string
	}{
		{
			name:  "model_env_key",
			model: []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}, {Key: "CLAUDE_CODE_AUTO_COMPACT_WINDOW", Value: "200000"}},
			key:   "CLAUDE_CODE_AUTO_COMPACT_WINDOW",
			afSet: " CLAUDE_CODE_AUTO_COMPACT_WINDOW='200000'",
		},
		{
			name:  "telemetry_env_key",
			telem: []config.EnvVar{{Key: "OTEL_RESOURCE_ATTRIBUTES", Value: "service.name=af"}},
			key:   "OTEL_RESOURCE_ATTRIBUTES",
			afSet: " OTEL_RESOURCE_ATTRIBUTES='service.name=af'",
		},
		{
			name:  "legacy_base_url",
			entry: config.AgentEntry{Type: "autonomous", Description: "test", BaseURL: "https://gw.example"},
			model: []config.EnvVar{{Key: "ANTHROPIC_MODEL", Value: "claude-opus-4"}},
			key:   "ANTHROPIC_BASE_URL",
			afSet: " ANTHROPIC_BASE_URL='https://gw.example'",
		},
		{
			name:  "telemetry_headers_file_ref",
			telem: []config.EnvVar{{Key: "OTEL_EXPORTER_OTLP_HEADERS", Value: "Authorization=file:secrets/otel"}},
			key:   "OTEL_EXPORTER_OTLP_HEADERS",
			afSet: "$(cat '/tmp/factory/secrets/otel')",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := integrationTestManager()
			if tt.entry.BaseURL != "" {
				m = newTestManager("/tmp/factory", "intagent", tt.entry)
			}
			m.c.ModelEnv = tt.model
			m.c.TelemetryEnv = tt.telem
			m.c.IntegrationEnv = []config.EnvVar{{Key: tt.key, Value: "evil"}, {Key: "ACME_INT_A", Value: "v"}}
			m.c.IntegrationKeyUniverse = []string{tt.key, "ACME_INT_A"}

			line := startupLine(t, m)

			indexOrFail(t, line, " ACME_INT_A='v'")
			indexOrFail(t, line, tt.afSet)
			if strings.Contains(line, tt.key+"='evil'") {
				t.Errorf("an integration must not override af's %s:\n%s", tt.key, line)
			}
			assertShellParses(t, line)
		})
	}
}

func TestBuildStartupCommand_NoIntegrationsIsByteIdentical(t *testing.T) {
	bare := startupLine(t, integrationTestManager())

	m := integrationTestManager()
	m.c.PluginDirs = []string{}
	m.c.IntegrationEnv = []config.EnvVar{}
	m.c.IntegrationKeyUniverse = []string{}
	m.c.HookFailModes = ""
	if got := startupLine(t, m); got != bare {
		t.Errorf("zero integration contributions changed the line.\ngot:  %s\nwant: %s", got, bare)
	}
	if strings.Contains(bare, "AF_INTEGRATION_HOOK_FAIL_MODES") || strings.Contains(bare, "--plugin-dir") {
		t.Errorf("a launch with nothing bound must mention no integration surface:\n%s", bare)
	}
}
