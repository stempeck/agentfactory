package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// This file pins Phase 2 of issue #686 (PR #688)'s watchdog.go half: the two new mail-only
// needles in endpointFailureSignatures, and the attributeEndpointFailure guard that wraps
// detectErrorPattern's sole call site so a wedged subscription gateway is named as a credential
// failure instead of respawned toward RECOVERY HALTED (todos/fable-implement/decisions.md D2, D6,
// D13, D14; consensus.md R2; design-doc.md K5, cross-review H-1).
//
// ONE NEW production seam this file references does not exist yet — this is the RED half of
// Phase 5, not a mistake:
//   - `attributeEndpointFailure(output, root string) (detected bool, cause string, mailOnly bool)`
//     (watchdog.go, wraps the :1126 detectErrorPattern call site): re-keys a respawn-posture
//     litellm.* match to the credential cause, mail-only, when derived mode is subscription (the
//     E2 handle file exists) and the handle/state shows a credential cause.
// Until GREEN adds it, this file fails to build (see todos/fable-implement/red_predictions.md).
//
// The two new needle entries in endpointFailureSignatures (litellm.AuthenticationError,
// litellm.RateLimitError) are ALSO not yet in the table, so TestWatchdog_DetectsMailOnlyEndpointSignatures
// is also RED today even though it calls only the pre-existing, pure detectErrorPattern.

// writeSubscriptionHandleWithDeviceCode writes the E2 handle with an EXTRA
// device_code_requested_at field — raw JSON, not a gatewayAuthHandle{} struct, because that
// struct (gateway_auth.go:51-57, DO-NOT-CHANGE) has no field for it (consensus.md R2 / decisions.md
// D2: the attribution guard must do its own raw-JSON read local to watchdog.go). The field's
// presence, not its exact value/type, is what the design says the guard checks ("device_code_requested_at
// present" — design-doc.md K5/K1 rows), so any non-absent value pins the signal.
func writeSubscriptionHandleWithDeviceCode(t *testing.T, root string, expiresAt int64) {
	t.Helper()
	p := gatewayAuthHandlePath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir subscription handle dir: %v", err)
	}
	body := map[string]any{
		"access_token":             "tok",
		"refresh_token":            "codex-refresh-token",
		"id_token":                 "",
		"expires_at":               expiresAt,
		"account_id":               "acct-fixture",
		"device_code_requested_at": time.Now().Add(-2 * time.Minute).Unix(),
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal subscription handle fixture: %v", err)
	}
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatalf("write subscription handle fixture: %v", err)
	}
}

// --- new needles: TestWatchdog_DetectsEndpointSignatures (existing, watchdog_test.go) stays
// unmodified — every one of its cases must remain respawn-posture. These are the mail-only
// siblings, pane text copied from the SP-7 captures the design doc records literally
// (design-doc.md:347): "litellm.AuthenticationError: Timed out waiting for device authorization"
// and "litellm.AuthenticationError: ChatgptException - …" (capital C — the needle is deliberately
// CONTEXT-FREE per cross-review H-1 because the wheel's own exception text carries no reliable
// lowercase "chatgpt" substring), plus a litellm.RateLimitError capture.

func TestWatchdog_DetectsMailOnlyEndpointSignatures(t *testing.T) {
	cases := []struct {
		name      string
		output    string
		wantCause string
	}{
		{
			name:      "auth_device_code_timeout",
			output:    "litellm.AuthenticationError: Timed out waiting for device authorization",
			wantCause: "endpoint failure: upstream authentication rejected by the gateway",
		},
		{
			name:      "auth_upstream_401_capitalised_provider",
			output:    "litellm.AuthenticationError: ChatgptException - 401 Unauthorized from upstream",
			wantCause: "endpoint failure: upstream authentication rejected by the gateway",
		},
		{
			name:      "rate_limit",
			output:    "litellm.RateLimitError: RateLimitError: you exceeded your current plan quota",
			wantCause: "endpoint failure: upstream rate limit (plan window exhausted)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detected, cause, mailOnly := detectErrorPattern(tc.output)
			if !detected {
				t.Fatalf("expected a subscription-auth signature to be detected in %q", tc.output)
			}
			if cause != tc.wantCause {
				t.Errorf("cause = %q, want %q", cause, tc.wantCause)
			}
			if !mailOnly {
				t.Errorf("%q must carry the mail-only posture (no respawn) — this is an upstream-auth/quota failure, not a dead session", tc.output)
			}
		})
	}
}

// TestWatchdog_NewNeedlesSitBelowInvalidModelNameAboveGenericLitellm is a PROTECTIVE assertion
// (one per DO-NOT-CHANGE item, fable-implement Phase 5) for the first-match-wins order gotcha the
// IMPLREADME calls non-negotiable: the two new needles must sit strictly between the #598
// "Invalid model name" entry and the generic litellm.InternalServerError entry, or attribution
// flips on a pane carrying more than one substring.
func TestWatchdog_NewNeedlesSitBelowInvalidModelNameAboveGenericLitellm(t *testing.T) {
	invalidModelIdx, authIdx, rateLimitIdx, genericIdx := -1, -1, -1, -1
	for i, sig := range endpointFailureSignatures {
		switch sig.needle {
		case "Invalid model name":
			invalidModelIdx = i
		case "litellm.AuthenticationError":
			authIdx = i
		case "litellm.RateLimitError":
			rateLimitIdx = i
		case "litellm.InternalServerError":
			genericIdx = i
		}
	}
	if invalidModelIdx == -1 || authIdx == -1 || rateLimitIdx == -1 || genericIdx == -1 {
		t.Fatalf("expected all four needles present; got indices invalid_model=%d auth=%d rate_limit=%d generic=%d",
			invalidModelIdx, authIdx, rateLimitIdx, genericIdx)
	}
	if !(invalidModelIdx < authIdx && authIdx < genericIdx) {
		t.Errorf("litellm.AuthenticationError must sit strictly between Invalid model name (%d) and litellm.InternalServerError (%d); got %d",
			invalidModelIdx, genericIdx, authIdx)
	}
	if !(invalidModelIdx < rateLimitIdx && rateLimitIdx < genericIdx) {
		t.Errorf("litellm.RateLimitError must sit strictly between Invalid model name (%d) and litellm.InternalServerError (%d); got %d",
			invalidModelIdx, genericIdx, rateLimitIdx)
	}
}

// --- attribution guard ---

func healthySubscriptionWatchdogState() gatewayAuthState {
	return gatewayAuthState{
		Mode:            gatewayAuthProfileName,
		State:           gatewayAuthStateOK,
		AccessExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}
}

// TestWatchdog_AttributesWedgedGatewayToCredential is AC-named (IMPLREADME "Full design-contract
// verification tests" / design-doc.md AC-6). It pins attributeEndpointFailure, the guard that
// wraps detectErrorPattern at watchdog.go:1126 so a credential-caused wedge is named as an auth
// failure (mail-only, no respawn) instead of respawning the agent toward RECOVERY HALTED.
func TestWatchdog_AttributesWedgedGatewayToCredential(t *testing.T) {
	t.Run("timeout_with_device_code_attempted", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeSubscriptionHandleWithDeviceCode(t, root, time.Now().Add(24*time.Hour).Unix())
		writeSubscriptionState(t, root, healthySubscriptionWatchdogState())

		detected, cause, mailOnly := attributeEndpointFailure("litellm.Timeout: request exceeded the deadline", root)
		if !detected {
			t.Fatal("expected detection")
		}
		if !mailOnly {
			t.Errorf("a wedge with device_code_requested_at present must be re-keyed to mail-only (no respawn); got mailOnly=false, cause=%q", cause)
		}
		if cause != "endpoint failure: upstream authentication rejected by the gateway" {
			t.Errorf("expected the re-keyed cause to be the auth cause; got %q", cause)
		}
	})

	t.Run("timeout_with_past_exp", func(t *testing.T) {
		root := setupConfigFactory(t)
		// Past exp on the RAW HANDLE itself (not the state record's mirror, and no state record
		// is written at all) — decisions.md D2's rationale: the guard needs the CURRENT on-disk
		// truth, not a value only as fresh as the last operator-triggered `af config models check`.
		writeSubscriptionHandle(t, root, time.Now().Add(-2*time.Hour).Unix(), "codex-refresh-token", "tok")

		detected, cause, mailOnly := attributeEndpointFailure("litellm.Timeout: request exceeded the deadline", root)
		if !detected {
			t.Fatal("expected detection")
		}
		if !mailOnly {
			t.Errorf("a wedge with a past-exp handle must be re-keyed to mail-only (no respawn); got mailOnly=false, cause=%q", cause)
		}
		if cause != "endpoint failure: upstream authentication rejected by the gateway" {
			t.Errorf("expected the re-keyed cause to be the auth cause; got %q", cause)
		}
	})

	t.Run("connection_error_with_revoked_record", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		st := healthySubscriptionWatchdogState()
		st.State = gatewayAuthStateRevoked
		writeSubscriptionState(t, root, st)

		detected, cause, mailOnly := attributeEndpointFailure("litellm.APIConnectionError: could not reach the endpoint", root)
		if !detected {
			t.Fatal("expected detection")
		}
		if !mailOnly {
			t.Errorf("a wedge with state=revoked must be re-keyed to mail-only (no respawn); got mailOnly=false, cause=%q", cause)
		}
		if cause != "endpoint failure: upstream authentication rejected by the gateway" {
			t.Errorf("expected the re-keyed cause to be the auth cause; got %q", cause)
		}
	})

	t.Run("timeout_with_healthy_handle_keeps_respawn", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		writeSubscriptionState(t, root, healthySubscriptionWatchdogState())

		detected, cause, mailOnly := attributeEndpointFailure("litellm.Timeout: request exceeded the deadline", root)
		if !detected {
			t.Fatal("expected detection")
		}
		if mailOnly {
			t.Errorf("a HEALTHY subscription handle must NOT be re-keyed — respawn is still correct for a genuinely dead session; got mailOnly=true, cause=%q", cause)
		}
		if cause != "endpoint failure: LiteLLM proxy timeout" {
			t.Errorf("cause must stay the original untouched litellm.Timeout cause; got %q", cause)
		}
	})

	t.Run("key_mode_unchanged", func(t *testing.T) {
		root := setupConfigFactory(t)
		// No subscription handle anywhere on disk at all — key mode, the frame-lift invariant.

		detected, cause, mailOnly := attributeEndpointFailure("litellm.InternalServerError: llm provider raised an error", root)
		if !detected {
			t.Fatal("expected detection")
		}
		if mailOnly {
			t.Errorf("a key-mode profile (no subscription handle on disk) must never be re-keyed; got mailOnly=true, cause=%q", cause)
		}
		if cause != "endpoint failure: LiteLLM proxy internal server error" {
			t.Errorf("cause must stay the original untouched litellm.InternalServerError cause; got %q", cause)
		}
	})

	// Protective (V37): litellm.ServiceUnavailableError is explicitly NOT in the re-key-eligible
	// set (Timeout/APIConnectionError/InternalServerError only) — even with every credential
	// signal present, it must pass through unchanged. Generalizing the guard to every litellm.*
	// class was the named real boundary risk.
	t.Run("service_unavailable_never_rekeys", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeSubscriptionHandleWithDeviceCode(t, root, time.Now().Add(-2*time.Hour).Unix())
		st := healthySubscriptionWatchdogState()
		st.State = gatewayAuthStateRevoked
		writeSubscriptionState(t, root, st)

		detected, cause, mailOnly := attributeEndpointFailure("litellm.ServiceUnavailableError: upstream is down", root)
		if !detected {
			t.Fatal("expected detection")
		}
		if mailOnly {
			t.Errorf("litellm.ServiceUnavailableError must NEVER be re-keyed, even with every credential signal present; got mailOnly=true, cause=%q", cause)
		}
		if cause != "endpoint failure: LiteLLM proxy service unavailable" {
			t.Errorf("cause must stay the original untouched litellm.ServiceUnavailableError cause; got %q", cause)
		}
	})
}

// --- mode-keyed remedy line (PR #688 decisions.md D3; supersedes the earlier cause-string-only D14) ---

// TestWatchdog_SubscriptionRemedyNamesCodexLogin is AC-named (design-doc.md AC-6(ii); IMPLREADME
// "the subscription mail body contains `codex login`, never `af recovery reset`"). PR #688 D3
// (decisions.md) makes the remedy MODE-DERIVED (subscription ⇒ codex login; api-key ⇒ check
// openai.key) and renames the rate-limit cause to a mode-neutral spelling, so the remedy can no
// longer be read off the cause string alone. This test therefore drives the REAL escalation path
// (pollAgents → attributeEndpointFailure → recoverAgent → watchdogFailureMail) in SUBSCRIPTION mode
// — a subscription handle on disk is what makes the derived mode subscription — and asserts on the
// emitted mail rather than on a formatter signature the fix is free to reshape (mechanism-agnostic).
func TestWatchdog_SubscriptionRemedyNamesCodexLogin(t *testing.T) {
	t.Run("auth_cause", func(t *testing.T) {
		root := t.TempDir()
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		mail := driveWatchdogPoll(t, root,
			"litellm.AuthenticationError: ChatgptException - 401 Unauthorized from upstream",
			map[string]int{})

		if len(mail.sent) != 1 {
			t.Fatalf("expected exactly one subscription-mode escalation mail, got %d", len(mail.sent))
		}
		if !strings.Contains(mail.sent[0].body, "codex login") {
			t.Errorf("a subscription-mode auth failure mail must name the codex login remedy (E5); got %q", mail.sent[0].body)
		}
		if strings.Contains(mail.sent[0].body, "af recovery reset") {
			t.Errorf("a subscription-mode failure mail must never suggest af recovery reset; got %q", mail.sent[0].body)
		}
	})

	t.Run("rate_limit_cause", func(t *testing.T) {
		root := t.TempDir()
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		mail := driveWatchdogPoll(t, root,
			"litellm.RateLimitError: RateLimitError: you exceeded your current plan quota",
			map[string]int{})

		if len(mail.sent) != 1 {
			t.Fatalf("expected exactly one subscription-mode escalation mail, got %d", len(mail.sent))
		}
		// The subscription-mode remedy is still codex login ...
		if !strings.Contains(mail.sent[0].body, "codex login") {
			t.Errorf("a subscription-mode rate-limit failure mail must still name the codex login remedy; got %q", mail.sent[0].body)
		}
		// ... but the cause is mode-neutral now: the old "subscription plan window exhausted" literal
		// must no longer appear anywhere in the escalation (the subject carries the cause verbatim).
		if strings.Contains(mail.sent[0].subject+mail.sent[0].body, "subscription plan window exhausted") {
			t.Errorf("the rate-limit cause must be mode-neutral (no \"subscription plan window exhausted\"); got subject %q body %q",
				mail.sent[0].subject, mail.sent[0].body)
		}
	})
}

// TestWatchdog_KeyModeRemedyUnchangedByModeKeying is the D14 misrouting-risk protective test: the
// pre-existing #598 "Invalid model name" mail-only remedy must NOT pick up the new codex-login
// wording purely because watchdogFailureMail now recognizes SOME cause strings as subscription-mode
// — the dispatch must key on the specific new cause strings, never a broad "is this mail-only"
// heuristic.
func TestWatchdog_KeyModeRemedyUnchangedByModeKeying(t *testing.T) {
	_, body := watchdogFailureMail("worker_a", "endpoint failure: model not served on this endpoint", true, false)
	if strings.Contains(body, "codex login") {
		t.Errorf("the pre-existing #598 Invalid-model-name remedy must NOT be misrouted to the subscription (codex login) wording; got %q", body)
	}
}

// --- real call site (not attributeEndpointFailure in isolation) ---

// TestWatchdog_RunLoopReKeysWedgedSubscriptionGateway proves attributeEndpointFailure is wired into
// pollAgents' actual polling loop (watchdog.go's sole call site), not merely callable in isolation.
// TestWatchdog_AttributesWedgedGatewayToCredential above calls attributeEndpointFailure directly and
// would stay green even if the call site at :1226 still called the unwrapped detectErrorPattern — this
// test drives pollAgents itself with the phase's own motivating scenario ("a revoked subscription
// session that wedges as litellm.Timeout") and would have caught that gap.
func TestWatchdog_RunLoopReKeysWedgedSubscriptionGateway(t *testing.T) {
	root := t.TempDir()
	writeTestAgentsConfig(t, root, `{"agents":{"test-worker":{"type":"autonomous","description":"w"}}}`)
	writeTestJSON(t, filepath.Join(root, ".agentfactory", "factory.json"),
		map[string]any{"type": "factory", "version": 1, "name": "f"})
	writeSubscriptionHandleWithDeviceCode(t, root, time.Now().Add(24*time.Hour).Unix())
	writeSubscriptionState(t, root, healthySubscriptionWatchdogState())
	mail := (&mailRecorder{}).install(t)

	tx := &configurableWatchdogTmux{claudeRunning: true, output: "litellm.Timeout: request exceeded the deadline"}
	origTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return tx }
	t.Cleanup(func() { newWatchdogTmux = origTmux })

	failures := map[string]int{}
	states := map[string]*watchdogAgentState{}
	scope := map[string]struct{}{"test-worker": {}}
	pollAgents(&cobra.Command{}, root, scope, states, failures, 2)

	if got := len(readRecoveryLogLines(t, root)); got != 0 {
		t.Fatalf("a credential-caused wedge routed through the real polling loop must never respawn; got %d recovery-log line(s)", got)
	}
	if len(mail.sent) != 1 {
		t.Fatalf("expected exactly one mail-only escalation from the real polling loop, got %d", len(mail.sent))
	}
	if !strings.Contains(mail.sent[0].body, "codex login") {
		t.Errorf("the real polling loop's wedge escalation mail must name the codex login remedy; got body %q", mail.sent[0].body)
	}
}

// --- PR #688 F3/F11/F27 pinning tests (RED at head) ---

// driveWatchdogPoll runs one real pollAgents tick against a single "test-worker" agent whose pane
// shows output, capturing every escalation mail. It mirrors the setup of
// TestWatchdog_RunLoopReKeysWedgedSubscriptionGateway so the F3/F11 assertions ride the actual
// escalation path (attributeEndpointFailure → recoverAgent → watchdogFailureMail / the mail-only
// bound notice) and stay agnostic to the fix's mechanism. failures is passed in so a test can
// pre-seed a counter; whether a subscription handle is on disk is the caller's choice (that is what
// makes the derived mode subscription vs api-key). "test-worker" makes the pane af-test-worker,
// which the ADR-018 guard exempts — a bare "worker" would panic the test binary.
func driveWatchdogPoll(t *testing.T, root, output string, failures map[string]int) *mailRecorder {
	t.Helper()
	writeTestAgentsConfig(t, root, `{"agents":{"test-worker":{"type":"autonomous","description":"w"}}}`)
	writeTestJSON(t, filepath.Join(root, ".agentfactory", "factory.json"),
		map[string]any{"type": "factory", "version": 1, "name": "f"})
	mail := (&mailRecorder{}).install(t)

	tx := &configurableWatchdogTmux{claudeRunning: true, output: output}
	origTmux := newWatchdogTmux
	newWatchdogTmux = func() watchdogTmux { return tx }
	t.Cleanup(func() { newWatchdogTmux = origTmux })

	pollAgents(&cobra.Command{}, root,
		map[string]struct{}{"test-worker": {}},
		map[string]*watchdogAgentState{}, failures, 2)
	return mail
}

// TestWatchdog_ApiKeyAuthFailureNeverAdvisesCodexLogin pins F3 (decisions.md D3): in api-key mode
// (NO subscription handle on disk) a rotated/invalid key surfaces as litellm.AuthenticationError,
// and the escalation must name the api-key remedy (openai.key), never `codex login` — which only
// makes sense for a subscription session. RED at head: the litellm.AuthenticationError needle maps
// UNCONDITIONALLY to subscriptionAuthRejectedCause (watchdog.go:259), whose mail body advises
// `codex login` regardless of mode.
func TestWatchdog_ApiKeyAuthFailureNeverAdvisesCodexLogin(t *testing.T) {
	root := t.TempDir()
	// No subscription handle anywhere on disk ⇒ api-key mode.
	mail := driveWatchdogPoll(t, root,
		"litellm.AuthenticationError: ChatgptException - 401 Unauthorized from upstream",
		map[string]int{})

	if len(mail.sent) != 1 {
		t.Fatalf("expected exactly one api-key-mode escalation mail, got %d", len(mail.sent))
	}
	if strings.Contains(mail.sent[0].body, "codex login") {
		t.Errorf("an api-key-mode auth failure must NOT advise `codex login` (there is no subscription session); got %q", mail.sent[0].body)
	}
	if !strings.Contains(mail.sent[0].body, "openai.key") {
		t.Errorf("an api-key-mode auth failure must name the api-key remedy (openai.key); got %q", mail.sent[0].body)
	}
}

// TestWatchdog_RateLimitCauseIsModeNeutral pins the F3 rate-limit-cause rename (decisions.md D3):
// the rate-limit cause must be mode-neutral, so a litellm.RateLimitError in api-key mode (no
// subscription handle) must not surface the word "subscription" in either the mail subject (which
// carries the cause verbatim) or the body. Driven end-to-end because the post-fix cause literal is
// the implementer's to spell. RED at head: subscriptionRateLimitCause is
// "endpoint failure: upstream rate limit (subscription plan window exhausted)" (watchdog.go:245)
// and, in api-key mode, its mail even advises the subscription `codex login` remedy.
func TestWatchdog_RateLimitCauseIsModeNeutral(t *testing.T) {
	root := t.TempDir()
	// No subscription handle ⇒ api-key mode; nothing here legitimately concerns a subscription.
	mail := driveWatchdogPoll(t, root,
		"litellm.RateLimitError: RateLimitError: you exceeded your current plan quota",
		map[string]int{})

	if len(mail.sent) == 0 {
		t.Fatal("expected the rate-limit failure to escalate at least one mail")
	}
	for i, m := range mail.sent {
		if strings.Contains(m.subject, "subscription") || strings.Contains(m.body, "subscription") {
			t.Errorf("mail[%d]: a rate-limit failure in api-key mode must carry a mode-neutral cause/remedy — no \"subscription\"; got subject %q body %q",
				i, m.subject, m.body)
		}
	}
}

// TestWatchdog_MailOnlyBoundNoticeNamesCause pins F11 (decisions.md D3): the FINAL mail-only bound
// notice must name the actual cause, not a fixed "model coverage" line, so an operator learns a
// credential/auth wedge is what stopped re-escalating. It pre-seeds the mail-only counter to the
// bound so this single poll emits exactly that final notice, then drives the real loop with an auth
// cause. RED at head: checkMailOnlyEscalation (watchdog.go:542-557) emits a cause-free notice worded
// around an "unserved model request" / "model coverage".
func TestWatchdog_MailOnlyBoundNoticeNamesCause(t *testing.T) {
	root := t.TempDir()
	// Seed the mail-only counter at the bound so this single poll emits the FINAL notice.
	failures := map[string]int{mailOnlyKey("test-worker"): watchdogMaxConsecutiveFailures}
	mail := driveWatchdogPoll(t, root,
		"litellm.AuthenticationError: ChatgptException - 401 Unauthorized from upstream",
		failures)

	if len(mail.sent) != 1 {
		t.Fatalf("expected exactly one final mail-only bound notice, got %d", len(mail.sent))
	}
	if strings.Contains(mail.sent[0].body, "model coverage") {
		t.Errorf("the final mail-only notice must name the actual (auth/credential) cause, not the fixed \"model coverage\" line; got %q", mail.sent[0].body)
	}
	if !strings.Contains(mail.sent[0].body, subscriptionAuthRejectedCause) {
		t.Errorf("the final mail-only notice must name the auth cause %q; got %q", subscriptionAuthRejectedCause, mail.sent[0].body)
	}
}

// TestWatchdog_MissingStateNeverRekeysWedgedGateway pins F27 (decisions.md D1): a wedged gateway
// with a FRESH subscription handle but NO Phase-1 state record on disk must NOT be re-keyed to the
// credential cause — a missing state record is "unverified", not a hard credential failure, so the
// timeout stays a respawn-posture LiteLLM timeout. Mirrors
// TestWatchdog_AttributesWedgedGatewayToCredential/timeout_with_healthy_handle_keeps_respawn but
// omits the state record. RED at head: subscriptionHardFailState includes "missing"
// (config_models.go:677), so subscriptionCredentialSignalPresent re-keys it to mail-only auth.
func TestWatchdog_MissingStateNeverRekeysWedgedGateway(t *testing.T) {
	root := setupConfigFactory(t)
	// Fresh handle present, but NO state record written at all — the F27 scenario.
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")

	detected, cause, mailOnly := attributeEndpointFailure("litellm.Timeout: request exceeded the deadline", root)
	if !detected {
		t.Fatal("expected detection")
	}
	if mailOnly {
		t.Errorf("a wedge with a fresh handle and no state record must NOT be re-keyed (missing state is unverified, not a credential hard-fail); got mailOnly=true, cause=%q", cause)
	}
	if cause != "endpoint failure: LiteLLM proxy timeout" {
		t.Errorf("cause must stay the original untouched litellm.Timeout cause; got %q", cause)
	}
}

// TestWatchdogAttributionAndRemedyDeriveFromRecordNotHandlePresence is K18 (design-doc.md L468;
// concern_tests.md item 3): attributeEndpointFailure's credential-signal gate and recoverAgent's
// subscriptionMode argument to watchdogFailureMail must derive from gatewayAuthMode(root) ==
// "codex-subscription", not from raw handle/key presence on disk. Each subtest plants a record
// that DISAGREES with what bare-presence inference would conclude, so a still-inference-based
// implementation cannot accidentally pass.
func TestWatchdogAttributionAndRemedyDeriveFromRecordNotHandlePresence(t *testing.T) {
	t.Run("record_api_key_stray_handle_uses_api_key_remedy", func(t *testing.T) {
		root := t.TempDir()
		writeSubscriptionHandleWithDeviceCode(t, root, time.Now().Add(24*time.Hour).Unix())
		if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("api-key\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}

		mail := driveWatchdogPoll(t, root,
			"litellm.AuthenticationError: ChatgptException - 401 Unauthorized from upstream",
			map[string]int{})

		if len(mail.sent) != 1 {
			t.Fatalf("expected exactly one escalation mail, got %d", len(mail.sent))
		}
		if !strings.Contains(mail.sent[0].body, ".agentfactory/secrets/openai.key") {
			t.Errorf("a persisted api-key record must produce the api-key remedy even with a stray subscription handle on disk; got %q", mail.sent[0].body)
		}
		if strings.Contains(mail.sent[0].body, "codex login") {
			t.Errorf("a persisted api-key record must never advise codex login; got %q", mail.sent[0].body)
		}
	})

	t.Run("record_codex_subscription_stray_key_uses_subscription_remedy", func(t *testing.T) {
		root := t.TempDir()
		writeSecretFile(t, root, "secrets/openai.key", "sk-stray-api-key")
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("codex-subscription\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}

		mail := driveWatchdogPoll(t, root,
			"litellm.AuthenticationError: ChatgptException - 401 Unauthorized from upstream",
			map[string]int{})

		if len(mail.sent) != 1 {
			t.Fatalf("expected exactly one escalation mail, got %d", len(mail.sent))
		}
		if !strings.Contains(mail.sent[0].body, "codex login") || !strings.Contains(mail.sent[0].body, "af gateway auth import") {
			t.Errorf("a persisted codex-subscription record must produce the subscription remedy even with a stray openai.key on disk; got %q", mail.sent[0].body)
		}
		if strings.Contains(mail.sent[0].body, ".agentfactory/secrets/openai.key") {
			t.Errorf("a persisted codex-subscription record must never advise the api-key remedy; got %q", mail.sent[0].body)
		}
	})

	t.Run("record_api_key_stray_handle_with_device_code_never_rekeys", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeSubscriptionHandleWithDeviceCode(t, root, time.Now().Add(24*time.Hour).Unix())
		if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("api-key\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}

		detected, cause, mailOnly := attributeEndpointFailure("litellm.Timeout: request exceeded the deadline", root)
		if !detected {
			t.Fatal("expected detection")
		}
		if mailOnly {
			t.Errorf("a persisted api-key record must never be re-keyed to mail-only, even with a stray device-code-bearing subscription handle on disk; got mailOnly=true, cause=%q", cause)
		}
		if cause != "endpoint failure: LiteLLM proxy timeout" {
			t.Errorf("cause must stay the original untouched litellm.Timeout cause; got %q", cause)
		}
	})
}
