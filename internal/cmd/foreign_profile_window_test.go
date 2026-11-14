package cmd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/tokenomics"
)

// A non-claude- gateway id is the one class where the declaration is the only correct operand
// (#754 AC-5). This file pins that class's windows on the resolver as it stood before the split
// and is never edited afterwards, so the split cannot move them unnoticed.
func TestForeignProfileWindowUnchanged(t *testing.T) {
	const models = `{"default":"sol","models":{
		"sol":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"1050000"},
		"sol-small":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"150000"},
		"sol-equal":{"ANTHROPIC_MODEL":"gpt-5.6-sol","CLAUDE_CODE_MAX_CONTEXT_TOKENS":"200000"},
		"sol-bare":{"ANTHROPIC_MODEL":"gpt-5.6-sol"}}}`
	now := boundaryTestNow()

	session := func(t *testing.T, profile string, plant bool) tokenomics.Window {
		t.Helper()
		fx := newLifecycleFixture(t)
		armTokenomics(t, fx.root, 10, 1)
		if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
			t.Fatal(err)
		}
		writeModelOverride(fx.workDir, profile)
		writeRuntimeFile(t, fx.workDir, "session_id", "sessa")
		if plant {
			plantSessionSnapshot(t, fx.root, fx.agent, "sessa", 60, 1000, now.Add(-10*time.Second), now)
		}
		startupCfg, err := config.LoadStartupConfig(fx.root)
		if err != nil {
			t.Fatalf("LoadStartupConfig: %v", err)
		}
		r := stepContextReading(fx.root, fx.workDir, fx.agent, startupCfg.Recovery, now)
		return stepAdmission(fx.root, fx.workDir, fx.agent, "offpath", "s-1", r,
			startupCfg.Tokenomics, startupCfg.Recovery.ContextThresholdPct).window
	}
	expect := func(t *testing.T, got tokenomics.Window, tokens int64, source string) {
		t.Helper()
		if got.Tokens != tokens || got.Source != source {
			t.Errorf("window = {%d, %q}, want {%d, %q}", got.Tokens, got.Source, tokens, source)
		}
	}

	t.Run("session declared over host", func(t *testing.T) {
		expect(t, session(t, "sol", true), 1050000, "declared")
	})
	t.Run("session declared under host", func(t *testing.T) {
		expect(t, session(t, "sol-small", true), 150000, "declared")
	})
	t.Run("session declared equals host", func(t *testing.T) {
		expect(t, session(t, "sol-equal", true), 200000, "declared")
	})
	t.Run("session nothing declared", func(t *testing.T) {
		expect(t, session(t, "sol-bare", true), 200000, "host")
	})
	t.Run("session no reading", func(t *testing.T) {
		expect(t, session(t, "sol", false), 1050000, "declared")
	})
	t.Run("prediction status window", func(t *testing.T) {
		fx := newLifecycleFixture(t)
		if err := os.WriteFile(config.ModelsConfigPath(fx.root), []byte(models), 0o644); err != nil {
			t.Fatal(err)
		}
		expect(t, resolveTokenomicsWindow(fx.root), 1050000, "declared")
	})
	t.Run("prediction pairing lint", func(t *testing.T) {
		profile := map[string]string{"ANTHROPIC_MODEL": "gpt-5.6-sol", "CLAUDE_CODE_AUTO_COMPACT_WINDOW": "220000"}
		warning, warns := config.PairingLintProfile("sol", profile)
		if !warns || !strings.Contains(warning, "200000") {
			t.Errorf("PairingLintProfile = (%q, %v), want a warning naming the host's 200000 assumption", warning, warns)
		}
		profile["CLAUDE_CODE_MAX_CONTEXT_TOKENS"] = "1050000"
		if warning, warns := config.PairingLintProfile("sol", profile); warns {
			t.Errorf("PairingLintProfile with a declared window warned: %q", warning)
		}
	})
}
