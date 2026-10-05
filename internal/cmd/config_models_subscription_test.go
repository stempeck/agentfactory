package cmd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// This file pins Phase 2 of issue #686 (PR #688)'s config_models.go half: the derived-mode
// upstream-auth stage and the /model/info routing cross-check `checkProfile` gains when a
// subscription handle is present (todos/fable-implement/decisions.md D1/D3/D5/D7/D8/D9/D11-D13;
// concern_tests.md rows 1-12).
//
// Two NEW production seams these tests reference do not exist yet — this is the RED half of
// Phase 5, not a mistake:
//   - `modelInfoProbe` (a package var beside httpProbe, config_models.go): probes /model/info and
//     returns {effective class id -> litellm_params.model}.
//   - `liveSmokeDeadline` (a const beside modelsProbeTimeout): the live-smoke deadline, decoupled
//     from modelsProbeTimeout and parity-matched with litellm.yaml's router_settings.timeout.
// Until GREEN adds them, this whole package fails to build (see todos/fable-implement/red_predictions.md).
//
// Scope decision recorded here (not in decisions.md, which is already CLOSED): the new derived-mode
// stage AND the /model/info routing cross-check both fire ONLY when the subscription handle file
// exists on disk (gateway==true AND handle present), not for every gateway==true profile. D12's
// literal "once per gateway==true profile" phrasing would otherwise make modelInfoProbe fire for
// every EXISTING gateway-profile test in this package (none of which stub it), which is a live
// network call under `go test` (ADR-018 violation) for a symmetric key-mode check no AC names by
// test name. This narrower gating keeps every pre-existing test hermetic and is flagged for GREEN's
// attention rather than silently assumed away.

// subscriptionCheckFixture seeds a gateway subscription profile (from subscriptionModels()) plus a
// sibling direct fable-alias profile (so the pre-existing fable-class coverage row resolves SERVED
// and never confounds a test aimed at one specific new axis), stubs httpProbe to report every id
// served, and stubs modelInfoProbe to report clean chatgpt/ routing for every id. A test overrides
// whichever single axis it targets after calling this.
func subscriptionCheckFixture(t *testing.T) (root string, cfg *config.ModelsConfig) {
	t.Helper()
	root = setupConfigFactory(t)
	cfg = subscriptionModels()
	cfg.Models["fable-5"] = map[string]string{"ANTHROPIC_MODEL": "claude-fable-5"}
	writeSecretFile(t, root, "secrets/codex-subscription.key", "sk-gateway-master-key")

	origProbe := httpProbe
	httpProbe = func(string, string) ([]string, error) {
		return []string{"gpt-5.6-sol", "claude-fable-5"}, nil
	}
	t.Cleanup(func() { httpProbe = origProbe })

	stubModelInfoProbeAllGood(t, "gpt-5.6-sol")
	return root, cfg
}

// stubModelInfoProbeAllGood stubs the modelInfoProbe seam (new this phase) to report every id in
// ids as routed through a chatgpt/ lane, isolating a test from the routing cross-check's own
// hard-fail path so it can pin a different axis (auth-stage verdicts, classification, fleet-scale).
func stubModelInfoProbeAllGood(t *testing.T, ids ...string) {
	t.Helper()
	orig := modelInfoProbe
	modelInfoProbe = func(baseURL, authToken string) (map[string]string, error) {
		routes := make(map[string]string, len(ids))
		for _, id := range ids {
			routes[id] = "chatgpt/" + id
		}
		return routes, nil
	}
	t.Cleanup(func() { modelInfoProbe = orig })
}

func healthySubscriptionState() gatewayAuthState {
	return gatewayAuthState{
		Mode:            gatewayAuthProfileName,
		State:           gatewayAuthStateOK,
		AccessExpiresAt: time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339),
	}
}

// --- row 1: classification untouched ---

func TestCheckClassifiesSubscriptionProfileAsGatewayWithHardAliasRows(t *testing.T) {
	root, cfg := subscriptionCheckFixture(t)
	writeValidModels(t, root, cfg)
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "PLANTED-ACCESS-TOKEN-VALUE")
	writeSubscriptionState(t, root, healthySubscriptionState())

	out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
	if err != nil {
		t.Fatalf("a healthy subscription profile must pass check; err=%v out=%q", err, out)
	}
	if strings.Contains(out, "direct endpoint") {
		t.Errorf("a subscription profile (file: secret) must still classify as a gateway at the unchanged :524 discriminator — the derived-mode stage must never soften the alias rows; out=%q", out)
	}
}

// --- row 2: upstream-auth stage hard-fail verdicts ---

func TestCheckUpstreamAuthStageVerdicts(t *testing.T) {
	setup := func(t *testing.T) string {
		root, cfg := subscriptionCheckFixture(t)
		writeValidModels(t, root, cfg)
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		return root
	}

	t.Run("missing", func(t *testing.T) {
		setup(t)
		// deliberately no state file written at all
		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err != nil {
			t.Fatalf("an ABSENT state record must WARN (unverified), never hard-fail — the hard-fail set is narrowed to {revoked, no-refresh-token} (F4/D1); err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "unverified") {
			t.Errorf("an absent state record must be reported as unverified; out=%q", out)
		}
	})

	t.Run("corrupt", func(t *testing.T) {
		root := setup(t)
		if err := os.MkdirAll(filepath.Dir(gatewayAuthStatePath(root)), 0o755); err != nil {
			t.Fatalf("mkdir state dir: %v", err)
		}
		if err := os.WriteFile(gatewayAuthStatePath(root), []byte("not json{{{"), 0o600); err != nil {
			t.Fatalf("write corrupt state: %v", err)
		}
		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err != nil {
			t.Fatalf("a corrupt/unparseable state record must WARN (unverified), never hard-fail — the hard-fail set is narrowed to {revoked, no-refresh-token} (F4/D1); err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "unverified") {
			t.Errorf("a corrupt state record must be reported as unverified; out=%q", out)
		}
	})

	t.Run("no_refresh_token", func(t *testing.T) {
		root := setup(t)
		st := healthySubscriptionState()
		st.State = gatewayAuthStateNoRefreshToken
		writeSubscriptionState(t, root, st)
		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err == nil {
			t.Fatalf("a no-refresh-token state must hard-fail; out=%q", out)
		}
	})

	t.Run("ok", func(t *testing.T) {
		root := setup(t)
		writeSubscriptionState(t, root, healthySubscriptionState())

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err != nil {
			t.Fatalf("a healthy ok state must NOT hard-fail the upstream-auth stage; err=%v out=%q", err, out)
		}

		// Protective (D4/claims.md #7): the state write is read-modify-write at 0600, never a
		// full-struct clobber and never writeModelCoverageRecord's 0o644 precedent.
		info, statErr := os.Stat(gatewayAuthStatePath(root))
		if statErr != nil {
			t.Fatalf("check must leave the state record in place after its own write: %v", statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Errorf("the state record must stay 0600 after check's own write (D4); got %04o", info.Mode().Perm())
		}
	})

	t.Run("past_exp", func(t *testing.T) {
		root := setup(t)
		// Overwrite the setup's FUTURE handle with a PAST one: after F1 the past-exp verdict is read
		// from the HANDLE, so this fixture's handle must itself be past or the F1 fix would judge it
		// current and break this subtest. The mirror is kept past too — a single-truth fixture.
		writeSubscriptionHandle(t, root, time.Now().Add(-1*time.Hour).Unix(), "codex-refresh-token", "tok")
		st := healthySubscriptionState()
		st.AccessExpiresAt = time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
		writeSubscriptionState(t, root, st)

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err == nil {
			t.Fatalf("a past-exp access token must hard-fail even though State=ok (D3/D5); out=%q", out)
		}
	})

	// revoked is not one of the 5 subtests the IMPLREADME names by name for this test function, but
	// D13 closed it as a required checkProfile hard-fail anyway: sling's E5 refusal list explicitly
	// includes `revoked`, and a revoked credential that `check` reports as passing while `sling`
	// simultaneously refuses to launch on it would directly contradict the phase's own stated goal
	// (the two surfaces must agree). D13's own flip condition is keyed to THIS test exercising
	// revoked, so it is pinned here rather than left as an unverified decision-log claim.
	t.Run("revoked", func(t *testing.T) {
		root := setup(t)
		st := healthySubscriptionState()
		st.State = gatewayAuthStateRevoked
		writeSubscriptionState(t, root, st)

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err == nil {
			t.Fatalf("a revoked state must hard-fail check, matching sling's E5 refusal list (D13); out=%q", out)
		}
	})
}

// --- row 3: past-exp reported distinctly ---

func TestCheckReportsExpiredForPastExpJWT(t *testing.T) {
	root, cfg := subscriptionCheckFixture(t)
	writeValidModels(t, root, cfg)
	writeSubscriptionHandle(t, root, time.Now().Add(-2*time.Hour).Unix(), "codex-refresh-token", "tok")
	// A healthy (fresh-mirror, state=ok) record on purpose: the "expired" verdict must be proven to
	// come from the HANDLE's own past exp, not the .runtime mirror (F1/D7). The vestigial mirror
	// past-exp line is dropped so the handle is the sole possible source of the expired verdict.
	writeSubscriptionState(t, root, healthySubscriptionState())

	out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
	if err == nil {
		t.Fatalf("a past-exp credential must hard-fail; out=%q", out)
	}
	if !strings.Contains(out, "expired") {
		t.Errorf("the past-exp reason must be reported distinctly as \"expired\" (not conflated with missing/corrupt/no-refresh-token wording); out=%q", out)
	}
}

// --- row 4/AC-2: routing cross-check hard fail ---

func TestCheckRoutingCrossCheckHardFailsOpenAILaneInSubscriptionMode(t *testing.T) {
	root, cfg := subscriptionCheckFixture(t)
	writeValidModels(t, root, cfg)
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
	writeSubscriptionState(t, root, healthySubscriptionState())

	// Override the "all good" routing stub subscriptionCheckFixture installed: this id now routes
	// through the WRONG (openai/) lane while in subscription mode.
	origInfo := modelInfoProbe
	modelInfoProbe = func(string, string) (map[string]string, error) {
		return map[string]string{"gpt-5.6-sol": "openai/gpt-5.6-sol"}, nil
	}
	t.Cleanup(func() { modelInfoProbe = origInfo })

	out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
	if err == nil {
		t.Fatalf("an openai/ lane on a subscription-mode profile must hard-fail (D8/A11); out=%q", out)
	}
	if !strings.Contains(out, "gpt-5.6-sol") {
		t.Errorf("the routing hard-fail must name the id; out=%q", out)
	}
	if !strings.Contains(out, "openai/") {
		t.Errorf("the routing hard-fail must name the wrong lane; out=%q", out)
	}
}

// --- row 5: routing-unavailable is loud, never silent, never hard on its own ---

func TestCheckRoutingNotVerifiedIsLoud(t *testing.T) {
	root, cfg := subscriptionCheckFixture(t)
	writeValidModels(t, root, cfg)
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
	writeSubscriptionState(t, root, healthySubscriptionState())

	origInfo := modelInfoProbe
	modelInfoProbe = func(string, string) (map[string]string, error) {
		return nil, errors.New("/model/info unreachable")
	}
	t.Cleanup(func() { modelInfoProbe = origInfo })

	out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
	if !strings.Contains(out, "routing not verified") {
		t.Errorf("an unavailable /model/info must print \"routing not verified\", never silently; out=%q", out)
	}
	if err != nil {
		t.Errorf("routing-not-verified alone must not hard-fail the check (D7: uniform soft treatment, never a definitive wrong answer); err=%v out=%q", err, out)
	}
}

// --- row 6: frame-lift invariant — a no-handle profile is a true no-op for the new stage ---

func TestCheckApiKeyProfileOutputByteIdentical(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
			"fable-5": {"ANTHROPIC_MODEL": "claude-fable-5"},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	// Deliberately NO subscription handle anywhere on disk — this profile is never "derived mode".

	orig := httpProbe
	httpProbe = func(string, string) ([]string, error) { return []string{"gpt-5.3-codex", "claude-fable-5"}, nil }
	t.Cleanup(func() { httpProbe = orig })

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err != nil {
		t.Fatalf("a passing api-key profile must still succeed; err=%v out=%q", err, out)
	}
	for _, marker := range []string{"subscription", "chatgpt", gatewayAuthProfileName, "routing not verified", "derived mode"} {
		if strings.Contains(strings.ToLower(out), strings.ToLower(marker)) {
			t.Errorf("a profile with no subscription handle on disk must be a true no-op for every Phase-2 marker; found %q in out=%q", marker, out)
		}
	}
}

// --- row 7/8: never print planted token material ---

func TestCheckNeverPrintsPlantedToken(t *testing.T) {
	root, cfg := subscriptionCheckFixture(t)
	writeValidModels(t, root, cfg)
	const plantedAccess = "PLANTED-ACCESS-TOKEN-MARKER-XYZ"
	const plantedRefresh = "PLANTED-REFRESH-TOKEN-MARKER-XYZ"
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), plantedRefresh, plantedAccess)
	writeSubscriptionState(t, root, healthySubscriptionState())

	out, _ := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
	if strings.Contains(out, plantedAccess) {
		t.Errorf("check must NEVER print the handle's access token; out=%q", out)
	}
	if strings.Contains(out, plantedRefresh) {
		t.Errorf("check must NEVER print the handle's refresh token; out=%q", out)
	}
}

func TestShowNeverPrintsPlantedGatewayToken(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, subscriptionModels())
	const plantedAccess = "PLANTED-SHOW-ACCESS-TOKEN-XYZ"
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "refresh", plantedAccess)
	writeSubscriptionState(t, root, healthySubscriptionState())

	out, err := runModelsCmd(t, runConfigModelsShow)
	if err != nil {
		t.Fatalf("show: %v", err)
	}
	if strings.Contains(out, plantedAccess) {
		t.Errorf("show must never leak gateway-handle material — the redaction boundary must not have widened; out=%q", out)
	}
}

// --- row 9: frame-lift invariant — attest is unchanged for a subscription profile ---

func TestAttestSemanticsUnchangedForSubscriptionProfile(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, subscriptionModels())
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "refresh", "tok")
	st := healthySubscriptionState()
	st.State = gatewayAuthStateRevoked
	writeSubscriptionState(t, root, st)

	out, err := runModelsCmd(t, runConfigModelsAttest, gatewayAuthProfileName)
	if err != nil {
		t.Fatalf("attest must succeed regardless of the handle's own state (issue #598 D8: attest never reads the handle); err=%v out=%q", err, out)
	}
	attPath := filepath.Join(root, ".runtime", "model_fitness", gatewayAuthProfileName+".json")
	if _, statErr := os.Stat(attPath); statErr != nil {
		t.Fatalf("attest must write %s even for a revoked subscription profile: %v", attPath, statErr)
	}
}

// --- row 10: fleet-scale advisory ---

func TestCheckFleetScaleAdvisoryWhenSubscriptionProfileIsDefaultOrDispatchMapped(t *testing.T) {
	setup := func(t *testing.T) (string, *config.ModelsConfig) {
		root, cfg := subscriptionCheckFixture(t)
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "refresh", "tok")
		writeSubscriptionState(t, root, healthySubscriptionState())
		return root, cfg
	}

	t.Run("default profile gets the advisory", func(t *testing.T) {
		root, cfg := setup(t)
		cfg.Default = gatewayAuthProfileName
		writeValidModels(t, root, cfg)

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err != nil {
			t.Fatalf("the fleet-scale advisory must never change the exit code (D19: advisory only); err=%v out=%q", err, out)
		}
		if !strings.Contains(out, gatewayAuthProfileName) ||
			!(strings.Contains(strings.ToLower(out), "quota") || strings.Contains(strings.ToLower(out), "fleet")) {
			t.Errorf("check must print a fleet-scale advisory line naming the default subscription profile; out=%q", out)
		}
	})

	t.Run("dispatch-mapped profile gets the advisory", func(t *testing.T) {
		root, cfg := setup(t)
		writeValidModels(t, root, cfg)
		disp := fmt.Sprintf(`{"repos":["o/r"],"trigger_label":"agentic","mappings":[{"labels":["needs-triage"],"agent":"debugger","model":%q}],"notify_on_complete":"manager"}`, gatewayAuthProfileName)
		if err := os.WriteFile(config.DispatchConfigPath(root), []byte(disp), 0o644); err != nil {
			t.Fatalf("seed dispatch.json: %v", err)
		}

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err != nil {
			t.Fatalf("the fleet-scale advisory must never change the exit code (D19); err=%v out=%q", err, out)
		}
		if !strings.Contains(out, gatewayAuthProfileName) ||
			!(strings.Contains(strings.ToLower(out), "quota") || strings.Contains(strings.ToLower(out), "fleet")) {
			t.Errorf("check must print a fleet-scale advisory line naming the dispatch-mapped subscription profile; out=%q", out)
		}
	})

	t.Run("neither default nor dispatch-mapped gets no advisory", func(t *testing.T) {
		root, cfg := setup(t)
		writeValidModels(t, root, cfg)

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err != nil {
			t.Fatalf("unexpected failure: %v out=%q", err, out)
		}
		if strings.Contains(strings.ToLower(out), "fleet-scale") {
			t.Errorf("a subscription profile that is neither default nor dispatch-mapped must get no advisory (D11: per-profile, not blanket); out=%q", out)
		}
	})
}

// --- row 11: live-smoke deadline is its own constant; the router timeout must not undercut it ---

func TestLiveSmokeUsesItsOwnDeadline(t *testing.T) {
	if liveSmokeDeadline == modelsProbeTimeout {
		t.Fatalf("the live-smoke deadline must be a DISTINCT constant from modelsProbeTimeout (K3: decoupled); got the same value %v", liveSmokeDeadline)
	}
	if modelsProbeTimeout != 5*time.Second {
		t.Errorf("modelsProbeTimeout (:46) must stay unmodified at 5s (DO-NOT-CHANGE); got %v", modelsProbeTimeout)
	}

	moduleRoot := findModuleRoot(t)
	yamlBytes, err := os.ReadFile(filepath.Join(moduleRoot, ".agentfactory", "litellm.yaml"))
	if err != nil {
		t.Fatalf("read checked-in .agentfactory/litellm.yaml: %v", err)
	}
	m := regexp.MustCompile(`(?m)^\s*timeout:\s*(\d+)`).FindStringSubmatch(string(yamlBytes))
	if m == nil {
		t.Fatal("could not find router_settings.timeout in the checked-in litellm.yaml — the parity anchor is gone")
	}
	wantSeconds, convErr := strconv.Atoi(m[1])
	if convErr != nil {
		t.Fatalf("parse router timeout %q: %v", m[1], convErr)
	}
	if time.Duration(wantSeconds)*time.Second < liveSmokeDeadline {
		t.Errorf("router_settings.timeout (%ds) is shorter than liveSmokeDeadline (%v); the gateway would cut the probe before its own budget", wantSeconds, liveSmokeDeadline)
	}
}

// --- row 12/AC-3: a --live timeout is a DISTINCT verdict, never NOT SERVED ---

func TestLiveSmokeTimeoutIsNotNotServed(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
			"fable-5": {"ANTHROPIC_MODEL": "claude-fable-5"},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")

	origProbe := httpProbe
	httpProbe = func(string, string) ([]string, error) { return []string{"gpt-5.3-codex", "claude-fable-5"}, nil }
	t.Cleanup(func() { httpProbe = origProbe })

	origMsg := modelsMessagesDo
	modelsMessagesDo = func(req *http.Request) (*http.Response, error) {
		return nil, context.DeadlineExceeded
	}
	t.Cleanup(func() { modelsMessagesDo = origMsg })

	origLive := configModelsCheckLive
	configModelsCheckLive = true
	t.Cleanup(func() { configModelsCheckLive = origLive })

	out, _ := runModelsCmd(t, runConfigModelsCheck, "codex")
	sawLiveLine := false
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "--live") {
			continue
		}
		sawLiveLine = true
		if strings.Contains(line, "NOT SERVED") {
			t.Errorf("a --live smoke that TIMES OUT must report a DISTINCT verdict, never NOT SERVED; line=%q", line)
		}
		if !strings.Contains(strings.ToLower(line), "timed out") && !strings.Contains(strings.ToLower(line), "timeout") {
			t.Errorf("a --live smoke timeout must be reported using a distinguishable timeout wording; line=%q", line)
		}
	}
	if !sawLiveLine {
		t.Fatalf("expected at least one --live smoke line in the output; out=%q", out)
	}
}

// --- PR #688 Phase 3 adoption-path pin ---
//
// TestAdoptionPathFromKeyModeFixture_FailsUntilRoutedThenPasses: a factory adopting subscription
// mode carries a codex-subscription profile whose routing was never re-verified against the REAL
// chatgpt/responses/<registry id> lanes quickstart.sh's subscription seed advertises. This reads
// the seed from the actual quickstart.sh source (never a hand-written stand-in — see
// freshBootstrapRegistry's rationale in quickstart_provisioning_shape_test.go), so it is
// genuinely gated on Phase 3's real artifacts: with no subscription seed yet, extraction itself
// fails loud (this is the RED state today). Once the seed exists, the fixture first simulates a
// pre-adoption gateway reporting stale (openai/) routing — hard-fail — then the real chatgpt/
// routing the seed demands — pass.
func TestAdoptionPathFromKeyModeFixture_FailsUntilRoutedThenPasses(t *testing.T) {
	moduleRoot := findModuleRoot(t)
	setup := setupLitellmSource(t, moduleRoot)
	subEntries := parseLitellmSeedEntries(t, subscriptionSeedBlock(t, setup))
	if len(subEntries) == 0 {
		t.Fatal("the subscription seed advertises no model_name entries")
	}

	root, cfg := subscriptionCheckFixture(t)
	writeValidModels(t, root, cfg)
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
	writeSubscriptionState(t, root, healthySubscriptionState())

	// Adoption snapshot: the routing cross-check still reports the id via the OLD key-mode
	// assumption (an openai/ lane) — as if the factory redeployed with --litellm-auth=
	// codex-subscription but the gateway's litellm.yaml was never actually reseeded.
	origInfo := modelInfoProbe
	modelInfoProbe = func(string, string) (map[string]string, error) {
		return map[string]string{"gpt-5.6-sol": "openai/gpt-5.6-sol"}, nil
	}
	out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
	if err == nil {
		t.Fatalf("adoption fixture with stale (openai/) routing must hard-fail until the "+
			"subscription seed's chatgpt/ lanes are actually in place; out=%q", out)
	}
	if !strings.Contains(out, "openai/") {
		t.Errorf("failure must name the wrong lane so an operator can see what to fix; out=%q", out)
	}
	modelInfoProbe = origInfo

	// Routed: the gateway now reports the id via the REAL lane the subscription seed advertises
	// for its first entry (read from quickstart.sh above, never hand-written).
	modelInfoProbe = func(string, string) (map[string]string, error) {
		return map[string]string{"gpt-5.6-sol": subEntries[0].backend}, nil
	}
	t.Cleanup(func() { modelInfoProbe = origInfo })

	out, err = runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
	if err != nil {
		t.Fatalf("adoption fixture routed through the seed's real backend must pass; err=%v out=%q", err, out)
	}
}

// --- PR #688 F1: expiry is judged from the handle's own exp, not the .runtime mirror (D7) ---
//
// checkUpstreamAuthStage reads gatewayAuthPastExpiry(st.AccessExpiresAt) at config_models.go:701 —
// the STATE-RECORD mirror. F1/D7 requires the live handle's own exp to be the sole source of the
// expiry verdict; the mirror must NEVER mask a stale handle nor condemn a fresh one. Each subcase
// makes the handle and the mirror DISAGREE and asserts the handle wins.
func TestCheckJudgesExpiryFromHandleNotMirror(t *testing.T) {
	t.Run("handle_fresh_mirror_stale", func(t *testing.T) {
		root, cfg := subscriptionCheckFixture(t)
		writeValidModels(t, root, cfg)
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		st := healthySubscriptionState()
		st.AccessExpiresAt = time.Now().Add(-2 * time.Hour).UTC().Format(time.RFC3339) // stale mirror decoy
		writeSubscriptionState(t, root, st)

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err != nil {
			t.Fatalf("a FRESH handle must be judged current even when the .runtime mirror is stale — expiry is read from the handle, never the mirror (F1/D7); err=%v out=%q", err, out)
		}
		if !strings.Contains(out, "credential verified") {
			t.Errorf("a fresh handle must report the credential verified; out=%q", out)
		}
	})

	t.Run("handle_stale_mirror_fresh", func(t *testing.T) {
		root, cfg := subscriptionCheckFixture(t)
		writeValidModels(t, root, cfg)
		writeSubscriptionHandle(t, root, time.Now().Add(-2*time.Hour).Unix(), "codex-refresh-token", "tok")
		writeSubscriptionState(t, root, healthySubscriptionState()) // fresh mirror decoy (state=ok)

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err == nil {
			t.Fatalf("a STALE handle must hard-fail even when the .runtime mirror is fresh — a fresh mirror must never mask an expired handle (F1/D7); out=%q", out)
		}
		if !strings.Contains(out, "expired") {
			t.Errorf("a stale handle must be reported distinctly as expired; out=%q", out)
		}
	})
}

// --- PR #688 F9: the /model/info routing cross-check runs in KEY mode too (D2) ---
//
// At head the cross-check block is gated `gateway && subscriptionHandlePresent(root)`
// (config_models.go:594), so a gateway advertising a chatgpt/ lane with NO subscription handle is
// never cross-checked and passes silently. F9 wires the symmetric key-mode branch: a chatgpt/ lane
// with no handle is a HARD failure naming the id and the missing handle.
func TestCheckKeyModeChatgptLaneWithoutHandleHardFails(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
			"fable-5": {"ANTHROPIC_MODEL": "claude-fable-5"},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	// Deliberately NO subscription handle anywhere on disk — this is a KEY-mode gateway.

	origProbe := httpProbe
	httpProbe = func(string, string) ([]string, error) { return []string{"gpt-5.3-codex", "claude-fable-5"}, nil }
	t.Cleanup(func() { httpProbe = origProbe })

	// The gateway routes this id through a chatgpt/ lane — a subscription-only lane with no handle
	// to authenticate it: the exact key-mode defect F9's cross-check must catch.
	origInfo := modelInfoProbe
	modelInfoProbe = func(string, string) (map[string]string, error) {
		return map[string]string{"gpt-5.3-codex": "chatgpt/gpt-5.3-codex"}, nil
	}
	t.Cleanup(func() { modelInfoProbe = origInfo })

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err == nil {
		t.Fatalf("a chatgpt/ lane on a KEY-mode gateway with no subscription handle must hard-fail — the /model/info cross-check must run in key mode too (F9/D2); out=%q", out)
	}
	if !strings.Contains(out, "gpt-5.3-codex") {
		t.Errorf("the key-mode routing hard-fail must name the id; out=%q", out)
	}
	if !strings.Contains(strings.ToLower(out), "handle") {
		t.Errorf("the key-mode routing hard-fail must name the missing subscription handle; out=%q", out)
	}
}

// --- PR #688 F10: mode is derived from three artifacts, never bare handle presence (D2) ---
//
// At head the credential stage runs whenever `gateway && subscriptionHandlePresent(root)`
// (config_models.go:594), so a stray secrets/chatgpt/auth.json makes `check` run the subscription
// credential stage on an api-key gateway. F10/D2 derives mode from the profile's lanes/env: an
// openai/-routed gateway is api-key mode and the subscription credential stage must be skipped.
func TestCheckKeyModeGatewayWithStrayHandleSkipsCredentialStage(t *testing.T) {
	root := setupConfigFactory(t)
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex": {
				"ANTHROPIC_MODEL":      "gpt-5.3-codex",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
			},
			"fable-5": {"ANTHROPIC_MODEL": "claude-fable-5"},
		},
	})
	writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
	// A STRAY subscription handle sits on disk even though this is an api-key gateway.
	writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "refresh", "tok")
	// decisions.md D9: an explicit, persisted gatewayAuthMode record of "api-key" is what makes
	// this fixture resolve correctly once K16 keys the mode off the record — a bare stray handle
	// with no record would otherwise migrate-infer to "codex-subscription" (tier 3) and wrongly
	// flip this test's expected outcome. Planting the record is a no-op under today's code (the
	// record is not yet read here), so this assertion is protective: it must keep passing.
	if err := os.WriteFile(authModeRecordPath(root), []byte("api-key\n"), 0o644); err != nil {
		t.Fatalf("write gatewayAuthMode record: %v", err)
	}

	origProbe := httpProbe
	httpProbe = func(string, string) ([]string, error) { return []string{"gpt-5.3-codex", "claude-fable-5"}, nil }
	t.Cleanup(func() { httpProbe = origProbe })

	// The gateway routes this id through an openai/ lane — the signal that this is api-key mode,
	// not subscription, despite the stray handle.
	origInfo := modelInfoProbe
	modelInfoProbe = func(string, string) (map[string]string, error) {
		return map[string]string{"gpt-5.3-codex": "openai/gpt-5.3-codex"}, nil
	}
	t.Cleanup(func() { modelInfoProbe = origInfo })

	out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
	if err != nil {
		t.Fatalf("an api-key gateway with openai/ lanes must NOT hard-fail merely because a stray subscription handle sits on disk — mode is derived from lanes/env, not bare handle presence (F10/D2); err=%v out=%q", err, out)
	}
	if strings.Contains(strings.ToLower(out), "subscription credential") {
		t.Errorf("the subscription credential stage must be SKIPPED for an api-key gateway (mode derived from openai/ lanes, not the stray handle); out=%q", out)
	}
}

// TestCheckBothCredentialsStillRunsRoutingCrossCheck is T-11 (design-doc.md AC-11/AC-13(iv);
// decisions.md D9, D4): once the gatewayAuthMode record is the source of truth, both an api-key
// secret and a subscription handle present simultaneously is no longer an ambiguity that skips
// the cross-check entirely — the persisted record decides which direction runs, and per K16 it
// always runs. RED at head: checkProfile's ambiguity branch (:617-618) still fires whenever both
// credentials are present, regardless of the record, so it never reaches the routing cross-check
// and always prints "ambiguous".
func TestCheckBothCredentialsStillRunsRoutingCrossCheck(t *testing.T) {
	setup := func(t *testing.T) string {
		root, cfg := subscriptionCheckFixture(t)
		writeValidModels(t, root, cfg)
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "refresh", "tok")
		keyPath := filepath.Join(config.ConfigDir(root), "secrets", "openai.key")
		if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
			t.Fatalf("mkdir openai.key dir: %v", err)
		}
		if err := os.WriteFile(keyPath, []byte("sk-api-key"), 0o600); err != nil {
			t.Fatalf("write openai.key: %v", err)
		}
		if err := os.WriteFile(authModeRecordPath(root), []byte("codex-subscription\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}
		writeSubscriptionState(t, root, healthySubscriptionState())
		return root
	}

	t.Run("openai_lane_hard_fails", func(t *testing.T) {
		setup(t)
		origInfo := modelInfoProbe
		modelInfoProbe = func(string, string) (map[string]string, error) {
			return map[string]string{"gpt-5.6-sol": "openai/gpt-5.6-sol"}, nil
		}
		t.Cleanup(func() { modelInfoProbe = origInfo })

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err == nil {
			t.Fatalf("a persisted codex-subscription record with observed openai/ routing must hard-fail the routing cross-check even though an api-key secret also exists on disk (T-11/AC-11); out=%q", out)
		}
		if !strings.Contains(out, "gpt-5.6-sol") || !strings.Contains(out, "openai/") {
			t.Errorf("the hard-fail must name the id and the wrong lane; out=%q", out)
		}
		if strings.Contains(strings.ToLower(out), "ambiguous") {
			t.Errorf("both credentials present must never print \"ambiguous\" once the record decides (K16); out=%q", out)
		}
	})

	t.Run("chatgpt_lane_passes_with_informational_line", func(t *testing.T) {
		setup(t)
		// subscriptionCheckFixture's default modelInfoProbe stub already reports chatgpt/ routing
		// for gpt-5.6-sol (stubModelInfoProbeAllGood), so no override is needed here.

		out, err := runModelsCmd(t, runConfigModelsCheck, gatewayAuthProfileName)
		if err != nil {
			t.Fatalf("a persisted codex-subscription record with observed chatgpt/ routing must pass — the routing cross-check direction always runs but agrees with the record (T-11/AC-11); err=%v out=%q", err, out)
		}
		if strings.Contains(strings.ToLower(out), "ambiguous") {
			t.Errorf("both credentials present must never print \"ambiguous\" once the record decides (K16); out=%q", out)
		}
		informational := regexp.MustCompile(`(?i)(api.?key.*subscription handle|subscription handle.*api.?key)`)
		if !informational.MatchString(out) {
			t.Errorf("both credentials present must print an informational line naming both credentials (K16; exact wording is a GREEN decision, decisions.md D4); out=%q", out)
		}
		for _, forbidden := range []string{"cannot decide", "remove one"} {
			if strings.Contains(strings.ToLower(out), forbidden) {
				t.Errorf("the informational line must not carry the deleted ambiguity branch's refusal-toned phrasing %q; out=%q", forbidden, out)
			}
		}
	})

	// boundary_migrated_stray_handle_still_skips_credential_stage is a named, intentional pin of
	// the HEADLINE FINDING's resolution (concern_tests.md item 1.3): deliberately redundant with
	// TestCheckKeyModeGatewayWithStrayHandleSkipsCredentialStage's exact fixture and assertions —
	// its only purpose is to make the migrated-vs-persisted distinction show up as a named pin
	// rather than relying on an unrelated-looking pre-existing test to catch a regression by
	// accident.
	t.Run("boundary_migrated_stray_handle_still_skips_credential_stage", func(t *testing.T) {
		root := setupConfigFactory(t)
		writeValidModels(t, root, &config.ModelsConfig{
			Models: map[string]map[string]string{
				"codex": {
					"ANTHROPIC_MODEL":      "gpt-5.3-codex",
					"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
					"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex.key",
				},
				"fable-5": {"ANTHROPIC_MODEL": "claude-fable-5"},
			},
		})
		writeSecretFile(t, root, "secrets/codex.key", "sk-real-value")
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "refresh", "tok")
		if err := os.WriteFile(authModeRecordPath(root), []byte("api-key\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}

		origProbe := httpProbe
		httpProbe = func(string, string) ([]string, error) { return []string{"gpt-5.3-codex", "claude-fable-5"}, nil }
		t.Cleanup(func() { httpProbe = origProbe })

		origInfo := modelInfoProbe
		modelInfoProbe = func(string, string) (map[string]string, error) {
			return map[string]string{"gpt-5.3-codex": "openai/gpt-5.3-codex"}, nil
		}
		t.Cleanup(func() { modelInfoProbe = origInfo })

		out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
		if err != nil {
			t.Fatalf("an api-key gateway with openai/ lanes must NOT hard-fail merely because a stray subscription handle sits on disk — mode is derived from lanes/env, not bare handle presence (F10/D2); err=%v out=%q", err, out)
		}
		if strings.Contains(strings.ToLower(out), "subscription credential") {
			t.Errorf("the subscription credential stage must be SKIPPED for an api-key gateway (mode derived from openai/ lanes, not the stray handle); out=%q", out)
		}
	})

	// boundary_stray_handle_never_suppresses_key_mode_routing_cross_check pins AC-11(iv) verbatim
	// ("the routing cross-check ... is never silently skipped merely because the prior mode's
	// credential exists"): a persisted api-key record plus a stray subscription handle plus a
	// gateway that wrongly serves a chatgpt/ lane must still hard-fail the key-mode routing
	// cross-check — the stray handle is exactly "the prior mode's credential" this clause names,
	// and must never make keyModeRoutingCrossCheck return early before inspecting a single route.
	t.Run("boundary_stray_handle_never_suppresses_key_mode_routing_cross_check", func(t *testing.T) {
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
		// A STRAY subscription handle — "the prior mode's credential" — sits on disk even though
		// the persisted record says api-key.
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "refresh", "tok")
		if err := os.WriteFile(authModeRecordPath(root), []byte("api-key\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}

		origProbe := httpProbe
		httpProbe = func(string, string) ([]string, error) { return []string{"gpt-5.3-codex"}, nil }
		t.Cleanup(func() { httpProbe = origProbe })

		// The gateway is misconfigured: it routes this api-key-mode id through a chatgpt/ lane.
		origInfo := modelInfoProbe
		modelInfoProbe = func(string, string) (map[string]string, error) {
			return map[string]string{"gpt-5.3-codex": "chatgpt/gpt-5.3-codex"}, nil
		}
		t.Cleanup(func() { modelInfoProbe = origInfo })

		out, err := runModelsCmd(t, runConfigModelsCheck, "codex")
		if err == nil {
			t.Fatalf("a chatgpt/ lane observed under a persisted api-key record must hard-fail the routing cross-check even though a stray subscription handle sits on disk (AC-11(iv)); out=%q", out)
		}
		if !strings.Contains(out, "gpt-5.3-codex") || !strings.Contains(out, "chatgpt/") {
			t.Errorf("the hard-fail must name the id and the wrong lane; out=%q", out)
		}
	})
}

// --- PR #688 F17: one shared non-empty handle predicate (D10) ---
//
// subscriptionHandlePresent is Stat + !info.IsDir() with no Size check (config_models.go:649), so a
// 0-byte handle counts as present — diverging from the mode-resolution ladder's `fi.Size() > 0`
// (install.go:939) and status's non-empty read. An empty handle must NOT count as present.
func TestSubscriptionHandlePresentRejectsEmptyFile(t *testing.T) {
	root := setupConfigFactory(t)
	p := gatewayAuthHandlePath(root)
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatalf("mkdir subscription handle dir: %v", err)
	}
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatalf("write empty handle: %v", err)
	}
	if subscriptionHandlePresent(root) {
		t.Errorf("a 0-byte subscription handle must NOT count as present — the presence predicate must require a non-empty file (F17/D10); path=%q", p)
	}
}
