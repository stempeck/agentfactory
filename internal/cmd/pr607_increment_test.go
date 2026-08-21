package cmd

// Pinning tests for the PR #607 increment (unresolved review threads T5/T6/T7 + body-findings
// F1/F3/F5 and the C5/BODY-3 comment rewords). Each test RED-pins one acceptance criterion against
// the code at PR head cdb272f3 and goes GREEN only after the Phase-6 implementation. The gateway
// control sub-test is protective: it passes NOW and must keep passing, proving the direct-endpoint
// softening never leaks onto the gateway path (#598 preservation).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// TestWatchdog_MailOnlyPostureDoesNotStarveCrashRespawn pins BODY-1/F5: the mail-only posture shares
// the crash-respawn `failures` map, so a persistent invalid-model pane latches the shared breaker and
// a subsequent genuine crash is never respawned. Multi-tick over ONE persistent failures/states pair
// (pollOnceWithPane's fresh-maps helper cannot express the accumulation the bug is about).
func TestWatchdog_MailOnlyPostureDoesNotStarveCrashRespawn(t *testing.T) {
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"test-worker":{"type":"autonomous","description":"w"}}}`)
	writeTestJSON(t, filepath.Join(root, ".agentfactory", "factory.json"),
		map[string]any{"type": "factory", "version": 1, "name": "f"})
	mail := (&mailRecorder{}).install(t)

	// One mutable fake returned on every tick: configurableWatchdogTmux reads its fields at call
	// time, so mutating .output/.claudeRunning between ticks swaps what the next tick sees.
	tx := &configurableWatchdogTmux{claudeRunning: true, output: wrappedInvalidModelPane}
	origTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return tx }
	t.Cleanup(func() { newWatchdogTmux = origTmux })

	failures := map[string]int{}
	states := map[string]*watchdogAgentState{}
	scope := map[string]struct{}{"test-worker": {}}
	tick := func() { pollAgents(&cobra.Command{}, root, scope, states, failures, 2) }

	const mailOnlyTicks = 10
	for i := 0; i < mailOnlyTicks; i++ {
		tick()
	}
	mailOnlyMails := len(mail.sent)

	// (a) Bounded escalation — a guard against a naive "just exclude mail-only from the counter" fix
	//     that would escalate once per tick forever. Passes on current code (bounded at 4) and after.
	if mailOnlyMails > watchdogMaxConsecutiveFailures+1 {
		t.Errorf("mail-only escalation is not bounded: %d mails over %d ticks (want <= %d)",
			mailOnlyMails, mailOnlyTicks, watchdogMaxConsecutiveFailures+1)
	}
	if mailOnlyMails >= mailOnlyTicks {
		t.Errorf("one escalation per tick (%d/%d): the mail-only posture has no bound", mailOnlyMails, mailOnlyTicks)
	}

	// (c) Wording — a zero-recovery posture must never claim "consecutive recoveries". RED on current
	//     code: the shared breaker mail fires at tick 4 with exactly that wording.
	for _, m := range mail.sent {
		if strings.Contains(strings.ToLower(m.subject+m.body), "consecutive recover") {
			t.Errorf("a mail-only escalation claims \"consecutive recover...\" though zero recoveries were "+
				"attempted for this posture; subject=%q body=%q", m.subject, m.body)
		}
	}

	// No mail-only tick may have respawned anything.
	if got := len(readRecoveryLogLines(t, root)); got != 0 {
		t.Fatalf("the mail-only posture must never respawn: got %d recovery-log line(s)", got)
	}

	// (b) A SUBSEQUENT GENUINE CRASH STILL RESPAWNS — the bug-proof, RED on current code (the latched
	//     shared breaker makes the crash branch `continue` without respawning).
	tx.claudeRunning = false
	tx.output = ""
	tick()

	if got := len(readRecoveryLogLines(t, root)); got != 1 {
		t.Fatalf("after a mail-only posture latched the shared breaker, a genuine crash was NOT respawned "+
			"(got %d recovery-log line(s), want 1) — the mail-only path consumed the crash-respawn budget (BODY-1/F5)", got)
	}
}

// TestConfigModelsCheck_DirectEndpoint_ClaudeAliasesDoNotHardFail pins T7/F3: a DIRECT endpoint (one
// whose ANTHROPIC_AUTH_TOKEN is a literal, not a file: secret — the D3 discriminator) must not
// hard-fail on un-served claude-* alias rows, and its remedy must point at the operator's own server
// rather than litellm.yaml. The gateway control proves the fix does not soften the gateway path.
func TestConfigModelsCheck_DirectEndpoint_ClaudeAliasesDoNotHardFail(t *testing.T) {
	t.Run("direct_endpoint_softens_alias_rows_and_branches_the_remedy", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeValidModels(t, root, &config.ModelsConfig{
			Models: map[string]map[string]string{
				// A literal auth token marks this a DIRECT (non-gateway) endpoint under the D3
				// discriminator — it is the shape the shipped lmstudio profile carries.
				"lmstudio": {
					"ANTHROPIC_MODEL":      "qwen/qwen3-coder-next",
					"ANTHROPIC_BASE_URL":   "http://host.docker.internal:11434",
					"ANTHROPIC_AUTH_TOKEN": "lmstudio",
				},
				// Direct profiles name claude-* ids, so directProfileClaudeIDs is non-empty and the
				// alias rows are actually computed against lmstudio (else the test is vacuous).
				"opus-5":  {"ANTHROPIC_MODEL": "claude-opus-5"},
				"fable-5": {"ANTHROPIC_MODEL": "claude-fable-5"},
			},
		})
		// Only the profile's own model is served; NO claude-* alias is.
		stubModelsProbe(t, []string{"qwen/qwen3-coder-next"}, nil)

		out, err := runModelsCmd(t, runConfigModelsCheck, "lmstudio")
		if err != nil {
			t.Fatalf("a DIRECT endpoint must NOT hard-fail on un-served claude-* alias rows (F3); err=%v out=%q", err, out)
		}
		if strings.Contains(out, "litellm.yaml") {
			t.Errorf("a direct endpoint must NOT be told to edit litellm.yaml — a dead end for a non-LiteLLM server (F3); out=%s", out)
		}
		if !strings.Contains(out, "own server") {
			t.Errorf("a direct endpoint's alias remedy must point at the operator's own server; out=%s", out)
		}
	})

	t.Run("gateway_control_still_hard_fails_on_missing_alias", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeValidModels(t, root, &config.ModelsConfig{
			Models: map[string]map[string]string{
				// A file: auth token marks this a GATEWAY under the D3 discriminator.
				"codex": {
					"ANTHROPIC_MODEL":      "gpt-5.6-terra",
					"ANTHROPIC_BASE_URL":   "http://localhost:4000",
					"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
				},
				"opus-5":  {"ANTHROPIC_MODEL": "claude-opus-5"},
				"fable-5": {"ANTHROPIC_MODEL": "claude-fable-5"},
			},
		})
		writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
		stubModelsProbe(t, []string{"gpt-5.6-terra"}, nil) // no claude-* alias served

		out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
		if err == nil {
			t.Fatalf("a GATEWAY (file: auth token) missing its claude-* aliases must STILL hard-fail — "+
				"the fix must not soften the gateway path (#598); out=%q", out)
		}
		if !strings.Contains(out, "litellm.yaml") {
			t.Errorf("the gateway remedy must still point at litellm.yaml (aliasRemedy unchanged); out=%s", out)
		}
	})
}

// TestCheckedInLitellm_HaikuAliasMatchesModelsJson pins T6/F1: the checked-in litellm.yaml haiku alias
// must route to the lane models.json declares for the HAIKU class (gpt-5.6-luna), not the main lane.
// wantLane is derived from models.json so the alias stays pinned to the source of truth if re-laned.
func TestCheckedInLitellm_HaikuAliasMatchesModelsJson(t *testing.T) {
	moduleRoot := findModuleRoot(t)

	yamlBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", "litellm.yaml"))
	if err != nil {
		t.Fatalf("read checked-in .agentfactory/litellm.yaml: %v", err)
	}
	regBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", "models.json"))
	if err != nil {
		t.Fatalf("read checked-in .agentfactory/models.json: %v", err)
	}
	var reg config.ModelsConfig
	if err := json.Unmarshal(regBytes, &reg); err != nil {
		t.Fatalf("parse checked-in .agentfactory/models.json: %v", err)
	}

	wantLane := reg.Models["codex"]["ANTHROPIC_DEFAULT_HAIKU_MODEL"]
	if wantLane == "" {
		t.Fatal("models.json codex profile names no ANTHROPIC_DEFAULT_HAIKU_MODEL to match the alias against")
	}

	var haikuBackend string
	for _, e := range parseLitellmSeedEntries(t, string(yamlBytes)) {
		if e.name == "claude-haiku-4-5" {
			haikuBackend = e.backend
		}
	}
	if haikuBackend == "" {
		t.Fatal("no claude-haiku-4-5 alias in checked-in litellm.yaml")
	}
	lane := haikuBackend[strings.LastIndex(haikuBackend, "/")+1:] // "openai/gpt-5.6-luna" -> "gpt-5.6-luna"
	if lane != wantLane {
		t.Errorf("claude-haiku-4-5 aliases to lane %q; models.json routes the HAIKU class to %q — "+
			"the checked-in alias must match the source of truth (T6/F1)", lane, wantLane)
	}
}

// TestPr607_AliasBlockCommentsDoNotContradictHaikuMapping pins C5/T5 and BODY-3/F2: the alias-block
// comments must stop forbidding the (correct) haiku→background/small mapping. Asserting the specific
// contradictory phrase is ABSENT is robust to whatever concise rewording Phase 6 chooses.
func TestPr607_AliasBlockCommentsDoNotContradictHaikuMapping(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	for _, tc := range []struct{ file, badPhrase, thread string }{
		{filepath.Join(".agentfactory", "litellm.yaml"), "never a smaller lane", "C5/T5"},
		{"quickstart.sh", "never the small one", "BODY-3/F2"},
	} {
		data, err := os.ReadFile(filepath.Join(moduleRoot, tc.file))
		if err != nil {
			t.Fatalf("read %s: %v", tc.file, err)
		}
		if strings.Contains(string(data), tc.badPhrase) {
			t.Errorf("%s alias-block comment still says %q, contradicting the correct haiku→background/small "+
				"mapping (%s)", tc.file, tc.badPhrase, tc.thread)
		}
	}
}
