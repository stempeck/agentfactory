package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
)

const agenIntegrationsTOML = `formula = "needsint"
type = "workflow"
version = 1
description = "needs an integration"
integrations = ["absent"]
integrations_optional = ["maybe-there"]

[[steps]]
id = "step1"
title = "Step 1"
`

// Agent-gen runs inside af install --agents, so it must never admit: a factory without the integration still
// generates the agent (K7).
func TestInstallAgents_FormulaWithUninstalledIntegrationGenerates(t *testing.T) {
	dir := setupFormulaFactory(t)
	if err := os.WriteFile(filepath.Join(config.FormulasDir(dir), "needsint.formula.toml"), []byte(agenIntegrationsTOML), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(config.PluginsConfigPath(dir)); err == nil {
		t.Fatal("fixture unexpectedly has a plugins.json")
	}
	stdout, stderr, err := runFormulaAgentGenInDir(t, dir, "needsint")
	if err != nil {
		t.Fatalf("agent-gen of a formula declaring an uninstalled integration failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}
	if _, err := os.Stat(filepath.Join(config.AgentDir(dir, "needsint"), "CLAUDE.md")); err != nil {
		t.Errorf("CLAUDE.md not generated: %v", err)
	}
}

func TestGenerateAgentTemplate_IntegrationsSection(t *testing.T) {
	t.Run("rendered when declared", func(t *testing.T) {
		f, err := formula.Parse([]byte(agenIntegrationsTOML))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		content := generateAgentTemplate(f, f.Name, "autonomous")
		i := strings.Index(content, "### Integrations")
		if i < 0 {
			t.Fatalf("template has no ### Integrations section:\n%s", content)
		}
		section := content[i:]
		req, opt := strings.Index(section, "absent"), strings.Index(section, "maybe-there")
		if req < 0 || opt < 0 || req > opt {
			t.Errorf("### Integrations must list required (absent) then optional (maybe-there):\n%s", section)
		}
	})
	t.Run("absent when not declared", func(t *testing.T) {
		f, err := formula.Parse([]byte(validPluginFormula("plain")))
		if err != nil {
			t.Fatalf("parse: %v", err)
		}
		if content := generateAgentTemplate(f, f.Name, "autonomous"); strings.Contains(content, "### Integrations") {
			t.Errorf("a formula declaring no integration rendered ### Integrations")
		}
	})
}
