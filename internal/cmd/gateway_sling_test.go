package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// This file pins Phase 2 of issue #686 (PR #688)'s sling.go half: the selecting-launch E5 refusal
// inserted at the existing sling.go:1213 gap (between the file: secret preflight and the
// fitness-attestation interlock), gated on profileSelecting so a respawn never reaches it
// (todos/fable-implement/decisions.md D4, D9, D13; consumers.md sling.go rows).
//
// Fixtures reuse subscriptionModels/writeSubscriptionHandle/writeSubscriptionState from
// gateway_check_test.go (same package, same fixed subscription-profile literal).

// TestSlingRefusesSelectingLaunchOnRevokedSubscriptionRecord is AC-named: a selecting launch of the
// subscription profile must hard-refuse with E5 when the Phase-1 state record already shows
// `revoked` (decisions.md D13: checkProfile's and sling's hard-fail sets must agree on revoked
// regardless of who ever sets it — nothing produces that value yet in this phase).
func TestSlingRefusesSelectingLaunchOnRevokedSubscriptionRecord(t *testing.T) {
	resetModelCoverageWarnings()
	t.Cleanup(resetModelCoverageWarnings)
	root := setupTestFactoryForDone(t, "manager")
	cfg := subscriptionModels()
	writeValidModels(t, root, cfg)
	writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
	writeSubscriptionState(t, root, gatewayAuthState{
		Mode: gatewayAuthProfileName, State: gatewayAuthStateRevoked,
	})
	// Clears the PRE-EXISTING fitness-attestation interlock (sling.go:1219-1225) as a confound: without
	// it, a selecting launch of this non-loopback profile refuses on the attestation gap regardless of
	// whether the new E5 check exists, which would pin the wrong reason.
	writeAttestationFixture(t, root, gatewayAuthProfileName)

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), gatewayAuthProfileName, "", false, &warn)
	if err == nil {
		t.Fatalf("a selecting launch on a revoked subscription record must refuse; got name=%q env=%v", name, env)
	}
	if !strings.Contains(err.Error(), "E5") {
		t.Errorf("the refusal must be labeled E5; got: %v", err)
	}
	if !strings.Contains(err.Error(), gatewayAuthProfileName) {
		t.Errorf("the refusal must name the profile %q; got: %v", gatewayAuthProfileName, err)
	}
	if env != nil {
		t.Errorf("no export set must be produced on refusal; got: %v", env)
	}
}

// TestSlingWarnsWhenSubscriptionUnverified pins D9: `unverified` is a WARN, not a refuse — and it is
// triggered by the STATE FILE itself being unreadable (a non-ENOENT read error), independent of the
// handle's own health. The fixture makes the state path a directory rather than chmod-ing it, so the
// read failure is portable and does not depend on the test process not running as root
// (todos/fable-implement/consensus.md Test Strategist Q4 / decisions.md D9).
func TestSlingWarnsWhenSubscriptionUnverified(t *testing.T) {
	resetModelCoverageWarnings()
	t.Cleanup(resetModelCoverageWarnings)
	root := setupTestFactoryForDone(t, "manager")
	cfg := subscriptionModels()
	writeValidModels(t, root, cfg)
	writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
	writeAttestationFixture(t, root, gatewayAuthProfileName)

	statePath := gatewayAuthStatePath(root)
	if err := os.MkdirAll(statePath, 0o700); err != nil {
		t.Fatalf("mkdir state path as a directory (unverified fixture): %v", err)
	}

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), gatewayAuthProfileName, "", false, &warn)
	if err != nil {
		t.Fatalf("unverified must warn, never refuse; got err=%v", err)
	}
	if name != gatewayAuthProfileName || len(env) == 0 {
		t.Fatalf("fixture must resolve through the profile branch, else the assertion below is vacuous; got name=%q env=%v", name, env)
	}
	// "unverified" (not the broader "af config models check" substring): the PRE-EXISTING
	// model-coverage-report warning (sling.go:1262 modelCoverageWarning) ALSO recommends
	// `af config models check` whenever no coverage record exists on disk — which this fixture never
	// writes — so asserting on that shared phrase alone would pass today for the wrong reason.
	// "unverified" names the credential state and appears nowhere in the coverage-warning's text.
	if !strings.Contains(warn.String(), "unverified") {
		t.Errorf("the warning must name the unverified credential state; got %q", warn.String())
	}
}

// TestRespawnKeepsProfileAndAddsNoLine is the architecture-invariant test IMPLREADME names for
// sling.go: a respawn (cliModel == "", profileSelecting == false) must never reach the new E5
// refusal branch at all, even when the on-disk record would hard-refuse a selecting launch — a
// routine handoff/compact must never brick on a credential problem it cannot fix mid-session
// (decisions.md D4; consumers.md helpers.go:172 row: the caller discards the resolver's error).
func TestRespawnKeepsProfileAndAddsNoLine(t *testing.T) {
	root := setupTestFactoryForDone(t, "manager")
	cfg := subscriptionModels()
	cfg.Agents = map[string]string{"manager": gatewayAuthProfileName}
	writeValidModels(t, root, cfg)
	writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
	writeSubscriptionState(t, root, gatewayAuthState{
		Mode: gatewayAuthProfileName, State: gatewayAuthStateRevoked,
	})

	var warn bytes.Buffer
	name, env, err := resolveRespawnModelEnv(root, "manager", config.AgentDir(root, "manager"), "", &warn)
	if err != nil {
		t.Fatalf("a respawn must never brick, even on a revoked subscription record; got err: %v", err)
	}
	if name != gatewayAuthProfileName || len(env) == 0 {
		t.Fatalf("fixture must resolve through the profile branch, else the assertion below is vacuous; got name=%q env=%v", name, env)
	}
	if warn.String() != "" {
		t.Errorf("a respawn must add NO line for the subscription credential audit; got %q", warn.String())
	}
}

// TestSlingLaunchJudgesExpiryFromHandle is the F1 sibling in sling (red_predictions.md row 3c;
// decisions.md D7): a selecting launch must judge access-token expiry from the LIVE handle's own
// `expires_at`, never from the `.runtime` mirror's `access_expires_at`. The fixture makes the two
// disagree — the handle is FRESH (now+24h) while the mirror is STALE (now-2h) — which is F1's exact
// failure scenario (a healthy gateway refused after LiteLLM refreshed the handle). State stays `ok`
// so the only refusal reachable is the expiry branch, and the attestation fixture clears the
// PRE-EXISTING fitness interlock (sling.go:1239) so the assertion pins the expiry reason, not the
// attestation gap. At head the audit reads the stale mirror (sling.go:1229) → past-exp → E5 refusal.
func TestSlingLaunchJudgesExpiryFromHandle(t *testing.T) {
	resetModelCoverageWarnings()
	t.Cleanup(resetModelCoverageWarnings)
	root := setupTestFactoryForDone(t, "manager")
	cfg := subscriptionModels()
	writeValidModels(t, root, cfg)
	writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
	writeSubscriptionState(t, root, gatewayAuthState{
		Mode:            gatewayAuthProfileName,
		State:           gatewayAuthStateOK,
		AccessExpiresAt: time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339),
	})
	writeAttestationFixture(t, root, gatewayAuthProfileName)

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), gatewayAuthProfileName, "", false, &warn)
	if err != nil {
		t.Fatalf("expiry must be judged from the fresh handle, not the stale mirror; a launch must NOT refuse; got err=%v (name=%q env=%v)", err, name, env)
	}
	if name != gatewayAuthProfileName || len(env) == 0 {
		t.Fatalf("fixture must resolve through the profile branch, else the assertion above is vacuous; got name=%q env=%v", name, env)
	}
}

// TestUpStyleLaunchRefusesRevokedSubscription is the F2 (BODY-1) driver (red_predictions.md row 5;
// decisions.md F2 gate-swap note): the E5 subscription refusal must reach an `af up` / `af sling
// --agent` launch, which passes no --model, so it must gate on the launch flavor (reportCoverage),
// not on profile SELECTION (cliModel). It mirrors TestSlingRefusesSelectingLaunchOnRevokedSubscriptionRecord
// but NON-selecting: cfg.Agents maps the agent to the profile so cliModel "" still resolves it, the
// record is revoked, and the attestation fixture clears the fitness interlock so the refusal pins on
// E5. At head the audit is gated `profileSelecting := cliModel != ""` (sling.go:1146) → false →
// skipped → no refusal, env returned.
func TestUpStyleLaunchRefusesRevokedSubscription(t *testing.T) {
	resetModelCoverageWarnings()
	t.Cleanup(resetModelCoverageWarnings)
	root := setupTestFactoryForDone(t, "manager")
	cfg := subscriptionModels()
	cfg.Agents = map[string]string{"manager": gatewayAuthProfileName}
	writeValidModels(t, root, cfg)
	writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
	writeSubscriptionState(t, root, gatewayAuthState{
		Mode: gatewayAuthProfileName, State: gatewayAuthStateRevoked,
	})
	writeAttestationFixture(t, root, gatewayAuthProfileName)

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), "", "", false, &warn)
	if err == nil {
		t.Fatalf("an `af up`-style launch (cliModel empty) of a revoked subscription record must refuse; got name=%q env=%v", name, env)
	}
	if !strings.Contains(err.Error(), "E5") {
		t.Errorf("the refusal must be labeled E5; got: %v", err)
	}
	if env != nil {
		t.Errorf("no export set must be produced on refusal; got: %v", env)
	}
}

// TestUpStyleLaunchSkipsE5WhenModeAmbiguous is the F2/F10 consistency guard (blind-review iteration
// 1, decisions.md D16), HOLDS AS-IS under K17 (concern_tests.md item 7): the launch-path E5 audit
// derives subscription mode from gatewayAuthMode(root), and this fixture plants both an api-key
// secret and a subscription handle with NO persisted record, so gatewayAuthMode hits its tier-4
// both-present error (INV-2) — af cannot decide offline which credential the gateway uses. K17
// swallows that error as "not confirmed subscription mode" and skips the E5 audit rather than
// propagating it as a launch refusal (design-doc.md C-9: coexistence is informational, never a
// refusal), mirroring the check path's own coexistence posture. Same revoked-handle fixture as
// TestUpStyleLaunchRefusesRevokedSubscription plus a non-empty openai.key: the revoked state must
// NOT E5-refuse the launch. If K17 instead propagated gatewayAuthMode's tier-4 error as a hard
// refusal, this test would fail (err != nil).
func TestUpStyleLaunchSkipsE5WhenModeAmbiguous(t *testing.T) {
	resetModelCoverageWarnings()
	t.Cleanup(resetModelCoverageWarnings)
	root := setupTestFactoryForDone(t, "manager")
	cfg := subscriptionModels()
	cfg.Agents = map[string]string{"manager": gatewayAuthProfileName}
	writeValidModels(t, root, cfg)
	writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
	writeSubscriptionState(t, root, gatewayAuthState{
		Mode: gatewayAuthProfileName, State: gatewayAuthStateRevoked,
	})
	writeAttestationFixture(t, root, gatewayAuthProfileName)
	// The api-key half of the ambiguous pair: a non-empty openai.key at the ladder's own path
	// (config_models.go apiKeySecretPresent / install.go:935), NOT a models.json file: ref. Its
	// presence ⇒ mode ambiguous ⇒ the E5 audit must be skipped.
	keyPath := filepath.Join(config.ConfigDir(root), "secrets", "openai.key")
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		t.Fatalf("mkdir openai.key dir: %v", err)
	}
	if err := os.WriteFile(keyPath, []byte("sk-stray-api-key"), 0o600); err != nil {
		t.Fatalf("write openai.key: %v", err)
	}

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), "", "", false, &warn)
	if err != nil {
		t.Fatalf("an ambiguous mode (both handles present) must skip the E5 audit, never refuse; got err=%v", err)
	}
	if name != gatewayAuthProfileName || len(env) == 0 {
		t.Fatalf("the launch must resolve through the profile (else the no-refusal assertion is vacuous); got name=%q env=%v", name, env)
	}
}

// TestSlingE5RunsOnRecordSubscriptionSkipsOnRecordApiKey is K17 (IMPLREADME_PHASE6.md; design-doc.md
// L468/AC-13; decisions.md D1): the E5 audit must be RECORD-derived — gated on
// gatewayAuthMode(root) == "codex-subscription", not on subscriptionHandlePresent(root) &&
// !apiKeySecretPresent(root). RED at head: the old inference guard reads the wrong signal from
// disk (credential presence) rather than the persisted record, so both subtests fail for the
// predicted reason (see red_predictions.md).
func TestSlingE5RunsOnRecordSubscriptionSkipsOnRecordApiKey(t *testing.T) {
	t.Run("record_codex_subscription_stray_key_E5_runs", func(t *testing.T) {
		resetModelCoverageWarnings()
		t.Cleanup(resetModelCoverageWarnings)
		root := setupTestFactoryForDone(t, "manager")
		cfg := subscriptionModels()
		writeValidModels(t, root, cfg)
		writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
		writeSubscriptionState(t, root, gatewayAuthState{
			Mode: gatewayAuthProfileName, State: gatewayAuthStateRevoked,
		})
		writeAttestationFixture(t, root, gatewayAuthProfileName)
		// A stray api-key secret on disk must NOT suppress E5 once the record says subscription.
		keyPath := filepath.Join(config.ConfigDir(root), "secrets", "openai.key")
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
			t.Fatalf("mkdir openai.key dir: %v", err)
		}
		if err := os.WriteFile(keyPath, []byte("sk-stray-api-key"), 0o600); err != nil {
			t.Fatalf("write openai.key: %v", err)
		}
		if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("codex-subscription\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}

		var warn bytes.Buffer
		name, env, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), gatewayAuthProfileName, "", false, &warn)
		if err == nil {
			t.Fatalf("a persisted codex-subscription record must run E5 (and refuse on the revoked state) even though a stray openai.key exists on disk; got name=%q env=%v", name, env)
		}
		if !strings.Contains(err.Error(), "E5") {
			t.Errorf("the refusal must be labeled E5; got: %v", err)
		}
		if env != nil {
			t.Errorf("no export set must be produced on refusal; got: %v", env)
		}
	})

	t.Run("record_api_key_stray_handle_E5_skipped", func(t *testing.T) {
		resetModelCoverageWarnings()
		t.Cleanup(resetModelCoverageWarnings)
		root := setupTestFactoryForDone(t, "manager")
		cfg := subscriptionModels()
		writeValidModels(t, root, cfg)
		writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
		// A stray, REVOKED subscription handle on disk must NOT trigger E5 once the record says api-key.
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
		writeSubscriptionState(t, root, gatewayAuthState{
			Mode: gatewayAuthProfileName, State: gatewayAuthStateRevoked,
		})
		writeAttestationFixture(t, root, gatewayAuthProfileName)
		if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("api-key\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}

		var warn bytes.Buffer
		name, env, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), gatewayAuthProfileName, "", false, &warn)
		if err != nil {
			t.Fatalf("a persisted api-key record must skip E5 entirely, even though a stray, revoked subscription handle exists on disk; got err=%v", err)
		}
		if name != gatewayAuthProfileName || len(env) == 0 {
			t.Fatalf("fixture must resolve through the profile branch, else the assertion above is vacuous; got name=%q env=%v", name, env)
		}
	})
}

// TestSlingLaunchWarnsNotRefusesOnMissingState is the F4 sibling in sling (red_predictions.md row 4c;
// decisions.md D1 option [A]): an absent state record maps to `unverified` — a WARNING — never a
// hard-fail. The fixture writes the handle and a valid secret but deliberately NO state record, so
// readGatewayAuthState → `missing`; the attestation fixture clears the fitness interlock so any
// refusal could only be the E5 credential audit. At head `missing` ∈ subscriptionHardFailState
// (config_models.go:677) → E5 refusal; after narrowing the set to {revoked, no-refresh-token} it
// warns and the launch proceeds.
func TestSlingLaunchWarnsNotRefusesOnMissingState(t *testing.T) {
	resetModelCoverageWarnings()
	t.Cleanup(resetModelCoverageWarnings)
	root := setupTestFactoryForDone(t, "manager")
	cfg := subscriptionModels()
	writeValidModels(t, root, cfg)
	writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
	writeAttestationFixture(t, root, gatewayAuthProfileName)

	var warn bytes.Buffer
	name, env, err := resolveLaunchModelEnv(root, "manager", config.AgentDir(root, "manager"), gatewayAuthProfileName, "", false, &warn)
	if err != nil {
		t.Fatalf("a missing state record must warn (unverified), never E5-refuse a selecting launch; got err=%v (name=%q env=%v)", err, name, env)
	}
	if name != gatewayAuthProfileName || len(env) == 0 {
		t.Fatalf("fixture must resolve through the profile branch, else the assertion above is vacuous; got name=%q env=%v", name, env)
	}
}
