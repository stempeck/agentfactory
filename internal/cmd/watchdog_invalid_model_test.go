package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// wrappedInvalidModelPane reproduces the incident refusal as tmux PHYSICAL rows, not as the log
// line. detectErrorPattern scans the un-joined CapturePane in pollAgents, and the concern-5 live
// capture observed the gateway refusal wrapping mid-phrase (design-doc.md:183,
// analyst-review-design.md:136-138), so the logical line arrives split. A fixture that kept the
// phrase intact would green a needle that no real pane can ever produce — the incident's own
// silent-failure shape rebuilt inside its defense.
const wrappedInvalidModelPane = `> /gpt-fable-review 595
  spawning adversarial gap-hunter (fable class)
API Error: 400 {"error":{"message":"litellm.BadRequestError: Invalid model name
passed in model=claude-fable-5","type":null,"code":"400"}}
  sub-agent returned no findings
`

// pollOnceWithPane drives the REAL poll loop over one live autonomous agent whose pane shows
// output, and returns the escalation mails plus the K6 recycle records the run produced.
//
// The respawn is observed through recovery_log.jsonl rather than a mock because recoverAgent has
// no Tx seam: it builds RespawnOptions without one and respawnSession falls through to the real
// client. recordRecycle runs unconditionally inside respawnSession (helpers.go:204), so the line
// lands even though the guarded tmux hop is a no-op — there is no path by which respawnSession
// runs and leaves the log empty. That is what makes a zero here a statement about production code
// rather than about the fake.
//
// The agent is "test-worker" because its pane af-test-worker is the identity the ADR-018 tmux
// guard exempts; a bare name would panic the whole test binary (tmux.go:98-106, :746-748).
func pollOnceWithPane(t *testing.T, output string) (root string, mail *mailRecorder, recycles []recoveryLogEntry) {
	t.Helper()
	root = t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"test-worker":{"type":"autonomous","description":"w"}}}`)
	writeTestJSON(t, filepath.Join(root, ".agentfactory", "factory.json"),
		map[string]any{"type": "factory", "version": 1, "name": "f"})
	mail = (&mailRecorder{}).install(t)

	origTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux {
		return &configurableWatchdogTmux{claudeRunning: true, output: output}
	}
	t.Cleanup(func() { newWatchdogTmux = origTmux })

	pollAgents(&cobra.Command{}, root, map[string]struct{}{"test-worker": {}},
		map[string]*watchdogAgentState{}, map[string]int{}, 2)

	return root, mail, readRecoveryLogLines(t, root)
}

// TestWatchdog_InvalidModelMailsWithoutRespawn covers the endpoint refusal that shipped the PR #595
// review without its adversarial audit pass (#598). The watchdog had no needle for it, so the
// sub-agent's death was silent; and the response every needle shared — respawn — is the wrong one
// here, because the session that spawned the sub-agent was healthy and still working
// (six_sigma_gaps.md Gap 11).
//
// Both halves are load-bearing, and the second is why the control subtest exists: the
// unsupported_api_for_model cause already ENDS in "(model not served on this endpoint)", so a
// posture derived from the cause TEXT would flip that respawn-worthy
// signature to mail-only and no other test in the package would notice. Running both fixtures
// through identical wiring makes the 0 and the 1 a differential rather than a pair of assertions
// that could both be vacuously true.
func TestWatchdog_InvalidModelMailsWithoutRespawn(t *testing.T) {
	const invalidModelCause = "endpoint failure: model not served on this endpoint"

	t.Run("wrapped_refusal_is_detected_with_mail_only_posture", func(t *testing.T) {
		if strings.Contains(wrappedInvalidModelPane, "Invalid model name passed in") {
			t.Fatal("the fixture must stay WRAPPED across physical rows: an unwrapped phrase would " +
				"prove a needle that the un-joined CapturePane can never match on a real pane")
		}

		detected, cause, mailOnly := detectErrorPattern(wrappedInvalidModelPane)
		if !detected {
			t.Fatalf("the wrapped gateway refusal must be detected; got detected=false, cause=%q", cause)
		}
		if cause != invalidModelCause {
			t.Errorf("cause = %q, want %q", cause, invalidModelCause)
		}
		if !mailOnly {
			t.Error("the invalid-model signature must carry the mail-only posture: respawning a " +
				"healthy session over one sub-agent's refusal destroys in-flight work")
		}
	})

	t.Run("mails_the_operator_and_never_respawns", func(t *testing.T) {
		root, mail, recycles := pollOnceWithPane(t, wrappedInvalidModelPane)

		if len(recycles) != 0 {
			t.Fatalf("a mail-only signature must not recycle the session, got %d recycle record(s): %+v",
				len(recycles), recycles)
		}
		if len(mail.sent) != 1 {
			t.Fatalf("the operator must be escalated to exactly once, got %d mail(s)", len(mail.sent))
		}

		sent := mail.sent[0]
		if !strings.Contains(sent.subject, invalidModelCause) {
			t.Errorf("subject must name the cause; got %q", sent.subject)
		}
		if strings.Contains(sent.body, "Session will be respawned.") {
			t.Errorf("the mail-only body must not claim a respawn that never happens; got %q", sent.body)
		}
		if !strings.Contains(sent.body, "NOT respawned") {
			t.Errorf("the mail-only body must state that the session was left running, so the operator "+
				"knows the escalation is the whole of the response; got %q", sent.body)
		}

		// Skipping the respawn must not skip the breadcrumb: last_error is what the operator opens
		// after reading a mail that tells them to investigate rather than that the factory acted.
		lastError := filepath.Join(config.AgentDir(root, "test-worker"), ".runtime", "last_error")
		if _, err := os.Stat(lastError); err != nil {
			t.Errorf("the mail-only path must still record last_error: %v", err)
		}
	})

	t.Run("control_respawn_worthy_signature_still_recycles", func(t *testing.T) {
		_, mail, recycles := pollOnceWithPane(t, "litellm proxy rejected the call: unsupported_api_for_model\n")

		if len(recycles) != 1 {
			t.Fatalf("unsupported_api_for_model must still recycle the session, got %d recycle record(s) — "+
				"if this is 0, the mail-only posture leaked onto a signature whose cause merely CONTAINS "+
				"the invalid-model wording", len(recycles))
		}
		if len(mail.sent) != 1 {
			t.Fatalf("the operator must be escalated to exactly once, got %d mail(s)", len(mail.sent))
		}
		if !strings.Contains(mail.sent[0].body, "Session will be respawned.") {
			t.Errorf("the respawning posture's mail text must be unchanged; got %q", mail.sent[0].body)
		}
	})

	t.Run("benign_pane_content_is_not_detected", func(t *testing.T) {
		for _, benign := range []string{
			"HTTP/1.1 400 Bad Request",
			"HTTP/1.1 200 OK",
			"loaded model=claude-sonnet-5 from the local registry",
			"Invalid model name",
		} {
			if detected, cause, _ := detectErrorPattern(benign); detected {
				t.Errorf("benign pane content must not trip the watchdog: %q -> %q", benign, cause)
			}
		}
	})
}
