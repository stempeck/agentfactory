package cmd

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
)

// This file pins Phase 1 of issue #686 (PR #688): `af gateway auth import|status`. The production
// file (gateway_auth.go) does not exist yet — every symbol referenced below (gatewayCmd, authCmd,
// gatewayAuthImportCmd, gatewayAuthStatusCmd, runGatewayAuthImport, runGatewayAuthStatus,
// translateCodexAuth, gatewayAuthHandle, codexAuthDotJSON, gatewayAuthState,
// gatewayAuthImportRefusal, gatewayTokenRefreshDo, the gatewayAuthState* constants) is undefined
// until Phase 6 writes it. That makes this whole package fail to COMPILE right now — the correct
// RED state for a brand-new file under TDD, recorded as such in red_predictions.md. Field/type
// names below are Phase 5's best-grounded guess at Phase 6's API surface, sourced from
// investigation_report.md's "Files to Modify" table (todos/fable-implement/investigation_report.md)
// and .designs/686/design-doc.md:175 (E3 shape) / codebase-snapshot.md:5688 (E2 shape); Phase 6 may
// rename without invalidating the behavior these tests pin.

// --- fixture helpers ---------------------------------------------------------------------------

// codexJWT builds a syntactically valid (unsigned) JWT carrying the given claims, matching what
// authenticator.py's _decode_jwt_claims tolerates (base64url payload, exceptions on the rest are
// swallowed — investigation_report.md's D4). Sufficient to pin exp-claim SOURCE (access_token vs
// id_token), not signature verification (this design never verifies Codex's signature — it is a
// read-only translator, not an auth boundary of its own).
func codexJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("marshaling JWT claims: %v", err)
	}
	body := base64.RawURLEncoding.EncodeToString(payload)
	return header + "." + body + ".sig"
}

// codexAuthFixture writes a nested Codex auth.json under a fresh $CODEX_HOME and returns that dir.
// accessExp and idExp are deliberately DIFFERENT so a test asserting E2's expires_at came from
// access_token (not id_token) has an unambiguous fixture (investigation_report.md's cross-verified
// JWT-source finding, authenticator.py:102-111,113-118,330-339).
func codexAuthFixture(t *testing.T, accessExp, idExp int64, accountID string) string {
	t.Helper()
	dir := t.TempDir()
	access := codexJWT(t, map[string]any{"exp": accessExp})
	id := codexJWT(t, map[string]any{"exp": idExp, "chatgpt_account_id": accountID})
	body := map[string]any{
		"auth_mode": "chatgpt",
		"tokens": map[string]any{
			"id_token":      id,
			"access_token":  access,
			"refresh_token": "codex-refresh-token-value",
			"account_id":    accountID,
		},
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling codex auth fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
		t.Fatalf("writing codex auth fixture: %v", err)
	}
	return dir
}

// gatewayFactoryRoot scaffolds the minimal factory root config_models_test.go/authority_test.go's
// own helpers already require (agents.json + factory.json), reused here rather than duplicated —
// `resolveInvokerRoot`/`callerAuthority`-gated commands need this shape to resolve a root at all.
func gatewayFactoryRoot(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	writeAgentsJSON(t, root, `{"agents":{"manager":{"type":"interactive","description":"c"}}}`)
	if err := os.WriteFile(config.FactoryConfigPath(root), []byte(`{}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

// --- E2 translation --------------------------------------------------------------------------

// TestGatewayAuthImport_NestedCodexToFlat pins the cross-verified, non-obvious finding: expires_at
// is decoded from access_token's JWT exp (never id_token's) and written as a raw Unix int (never
// RFC3339) — investigation_report.md "Agreed Fix Approach" §, verified against the installed
// LiteLLM wheel (authenticator.py:102-111,113-118,330-339).
func TestGatewayAuthImport_NestedCodexToFlat(t *testing.T) {
	const accessExp, idExp = 1900000000, 1800000000 // deliberately different
	nested := codexAuthDotJSON{
		AuthMode: "chatgpt",
		Tokens: &codexTokens{
			IDToken:      codexJWT(t, map[string]any{"exp": idExp}),
			AccessToken:  codexJWT(t, map[string]any{"exp": accessExp}),
			RefreshToken: "codex-refresh-token-value",
			AccountID:    "acct-123",
		},
	}
	flat, err := translateCodexAuth(nested)
	if err != nil {
		t.Fatalf("translateCodexAuth: unexpected error: %v", err)
	}
	if flat.ExpiresAt != accessExp {
		t.Errorf("ExpiresAt = %d, want %d (access_token's exp, never id_token's %d)", flat.ExpiresAt, accessExp, idExp)
	}
	if flat.AccountID != "acct-123" {
		t.Errorf("AccountID = %q, want %q (prefers tokens.account_id)", flat.AccountID, "acct-123")
	}
	if flat.RefreshToken != "codex-refresh-token-value" {
		t.Errorf("RefreshToken not carried through: %q", flat.RefreshToken)
	}
}

// TestGatewayAuthImport_PrepopulatesExpiresAtAndAccountID pins the AC-5 rationale: LiteLLM's own
// read-path writes never fire because af pre-populates both fields on write — never zero, never
// absent, when the source tokens are well-formed. The `namespaced_path` subcase is the F6 RED
// driver: real Codex/LiteLLM id_tokens nest chatgpt_account_id under the
// "https://api.openai.com/auth" namespace claim (authenticator.py:132-141), and head reads the
// FLAT key only (gateway_auth.go:84,90), so the JWT-derived fallback misses. `flat_path_still_honoured`
// is protective — the flat fallback must survive the namespaced-first fix.
func TestGatewayAuthImport_PrepopulatesExpiresAtAndAccountID(t *testing.T) {
	t.Run("namespaced_path", func(t *testing.T) {
		nested := codexAuthDotJSON{
			AuthMode: "chatgpt",
			Tokens: &codexTokens{
				IDToken: codexJWT(t, map[string]any{
					"exp": 1900000000,
					"https://api.openai.com/auth": map[string]any{
						"chatgpt_account_id": "acct-fallback",
					},
				}),
				AccessToken:  codexJWT(t, map[string]any{"exp": 1900000000}),
				RefreshToken: "rt",
				// AccountID intentionally absent: must fall back to JWT-derivation from the
				// id_token's namespaced claim (investigation_report.md row 74).
			},
		}
		flat, err := translateCodexAuth(nested)
		if err != nil {
			t.Fatalf("translateCodexAuth: unexpected error: %v", err)
		}
		if flat.ExpiresAt == 0 {
			t.Error("ExpiresAt must be pre-populated, not zero")
		}
		if flat.AccountID != "acct-fallback" {
			t.Errorf("AccountID = %q, want JWT-derived fallback %q from the namespaced claim", flat.AccountID, "acct-fallback")
		}
	})

	t.Run("flat_path_still_honoured", func(t *testing.T) {
		nested := codexAuthDotJSON{
			AuthMode: "chatgpt",
			Tokens: &codexTokens{
				IDToken:      codexJWT(t, map[string]any{"exp": 1900000000, "chatgpt_account_id": "acct-flat"}),
				AccessToken:  codexJWT(t, map[string]any{"exp": 1900000000}),
				RefreshToken: "rt",
			},
		}
		flat, err := translateCodexAuth(nested)
		if err != nil {
			t.Fatalf("translateCodexAuth: unexpected error: %v", err)
		}
		if flat.AccountID != "acct-flat" {
			t.Errorf("AccountID = %q, want flat-claim fallback %q (the flat path must survive the namespaced-first fix)", flat.AccountID, "acct-flat")
		}
	})
}

// TestGatewayAuthImport_ApiKeyLoginRefused pins E3 (api.md:157): a Codex file holding an API key
// (OPENAI_API_KEY set, tokens absent) is refused, not silently treated as a subscription session —
// the exact hazard six_sigma_gaps.md Gap 17 names (billing a platform key under the subscription
// label).
func TestGatewayAuthImport_ApiKeyLoginRefused(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	codexHome := t.TempDir()
	t.Setenv("CODEX_HOME", codexHome)
	body := `{"OPENAI_API_KEY":"sk-live-xxxx"}`
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	err := runGatewayAuthImport(cmd, nil)
	if err == nil {
		t.Fatal("import must refuse an API-key-mode Codex file")
	}
	if !strings.Contains(err.Error(), "API key") && !strings.Contains(err.Error(), "api key") {
		t.Errorf("refusal must name the API-key condition (E3); got: %v", err)
	}
}

// TestGatewayAuthImport_KeyringLoginRefused pins decisions.md D8: a keyring-backed Codex login
// leaves NO auth.json file at all (verified: dependencies.md:76 "(no file)"; codebase-snapshot.md
// storage.rs's FileAuthStorage is the only backend that writes auth.json). It is therefore
// on-disk-indistinguishable from "never ran codex login" — this test asserts the SAME refusal path
// as the missing-entirely case, not a synthetic keyring marker unsupported by the real Codex CLI.
func TestGatewayAuthImport_KeyringLoginRefused(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	codexHome := t.TempDir() // no auth.json written — the keyring-mode shape
	t.Setenv("CODEX_HOME", codexHome)

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	err := runGatewayAuthImport(cmd, nil)
	if err == nil {
		t.Fatal("import must refuse when $CODEX_HOME/auth.json does not exist (keyring-only or never logged in)")
	}
	if !strings.Contains(err.Error(), "codex login") {
		t.Errorf("refusal must name the remedy 'codex login' (D2/E2-shaped); got: %v", err)
	}
}

// TestGatewayAuthImport_Idempotent pins the design's idempotency contract: an unchanged source
// re-import is a no-op (byte-identical handle), a changed source replaces it.
func TestGatewayAuthImport_Idempotent(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	codexHome := codexAuthFixture(t, 1900000000, 1800000000, "acct-1")
	t.Setenv("CODEX_HOME", codexHome)

	run := func() []byte {
		cmd := &cobra.Command{}
		cmd.SetOut(new(strings.Builder))
		if err := runGatewayAuthImport(cmd, nil); err != nil {
			t.Fatalf("runGatewayAuthImport: %v", err)
		}
		data, err := os.ReadFile(gatewayAuthHandlePath(root))
		if err != nil {
			t.Fatalf("reading written handle: %v", err)
		}
		return data
	}

	first := run()
	second := run()
	if string(first) != string(second) {
		t.Error("re-importing an unchanged source must produce a byte-identical handle")
	}

	// Changed source: new account id, must replace.
	newHome := codexAuthFixture(t, 1900000001, 1800000001, "acct-2")
	t.Setenv("CODEX_HOME", newHome)
	third := run()
	if string(third) == string(second) {
		t.Error("re-importing a changed source must replace the handle, not keep the stale one")
	}
}

// TestGatewayAuthImport_HonoursCodexHome pins that import reads $CODEX_HOME (default
// ~/.codex/auth.json), never a hard-coded path.
func TestGatewayAuthImport_HonoursCodexHome(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	codexHome := codexAuthFixture(t, 1900000000, 1800000000, "acct-honours")
	t.Setenv("CODEX_HOME", codexHome)

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	if err := runGatewayAuthImport(cmd, nil); err != nil {
		t.Fatalf("runGatewayAuthImport: %v", err)
	}
	data, err := os.ReadFile(gatewayAuthHandlePath(root))
	if err != nil {
		t.Fatalf("expected a handle written from $CODEX_HOME's auth.json: %v", err)
	}
	if !strings.Contains(string(data), "acct-honours") {
		t.Errorf("handle does not reflect the $CODEX_HOME fixture: %s", data)
	}
}

// TestGatewayAuthImport_WritesAtomically0600 pins the DO-NOT-CHANGE writer-perm clause
// (investigation_report.md row 82; claims.md #7): 0600 file in a 0700 dir, no leftover temp file —
// must NOT copy writeModelCoverageRecord's 0o644.
func TestGatewayAuthImport_WritesAtomically0600(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	codexHome := codexAuthFixture(t, 1900000000, 1800000000, "acct-perm")
	t.Setenv("CODEX_HOME", codexHome)

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	if err := runGatewayAuthImport(cmd, nil); err != nil {
		t.Fatalf("runGatewayAuthImport: %v", err)
	}

	handlePath := gatewayAuthHandlePath(root)
	info, err := os.Stat(handlePath)
	if err != nil {
		t.Fatalf("stat handle: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("handle perm = %o, want 0600 (must NOT copy writeModelCoverageRecord's 0o644)", info.Mode().Perm())
	}
	dirInfo, err := os.Stat(filepath.Dir(handlePath))
	if err != nil {
		t.Fatalf("stat secrets dir: %v", err)
	}
	if dirInfo.Mode().Perm() != 0o700 {
		t.Errorf("secrets dir perm = %o, want 0700", dirInfo.Mode().Perm())
	}

	entries, err := os.ReadDir(filepath.Dir(handlePath))
	if err != nil {
		t.Fatalf("reading secrets dir: %v", err)
	}
	for _, e := range entries {
		if strings.Contains(e.Name(), ".tmp") {
			t.Errorf("stray temp file left behind: %s", e.Name())
		}
	}
}

// TestGatewayAuthImportRefusedInAgentContext pins operator-only refusal on `import` specifically
// (NOT `status`, which stays read-only/ungated — investigation_report.md row 70).
func TestGatewayAuthImportRefusedInAgentContext(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "manager") // signal 1 => Agent
	installFakeTmuxPresent(t)
	t.Setenv("TMUX", "")
	codexHome := codexAuthFixture(t, 1900000000, 1800000000, "acct-agent")
	t.Setenv("CODEX_HOME", codexHome)

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	err := runGatewayAuthImport(cmd, nil)
	if err == nil {
		t.Fatal("import must refuse in agent context")
	}
	if err.Error() != gatewayAuthImportRefusal {
		t.Errorf("refusal text = %q, want the bespoke gatewayAuthImportRefusal constant (decisions.md D9 — NOT teardownRefusalFormat/recoveryResetRefusal verbatim)", err.Error())
	}
	if _, statErr := os.Stat(gatewayAuthHandlePath(root)); !os.IsNotExist(statErr) {
		t.Error("agent-context refusal must write no handle")
	}
}

// TestGatewayAuthImportRemovesCoverageRecordOnModeSwitch pins decisions.md D1/D6: on a REAL mode
// switch — no subscription handle existed before this import, so the profile is transitioning from
// api-key to subscription — the stale api-key-era coverage verdict recorded under the profile name
// is cleared. Re-scoped from an unchanged re-import to an actual switch (D6): removal is justified
// by "no prior handle", not by rewriting every import. The keep-on-no-switch half lives in
// TestGatewayAuthImport_KeepsCoverageRecordWhenModeUnchanged.
func TestGatewayAuthImportRemovesCoverageRecordOnModeSwitch(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	codexHome := codexAuthFixture(t, 1900000000, 1800000000, "acct-switch")
	t.Setenv("CODEX_HOME", codexHome)

	// No handle exists yet — this import establishes subscription mode (the api-key -> subscription
	// switch), which is the only condition under which the stale coverage verdict is cleared (D6).
	if subscriptionHandlePresent(root) {
		t.Fatal("precondition: no subscription handle must exist before the switching import")
	}

	stalePath := filepath.Join(root, ".runtime", "model_coverage", "codex-subscription.json")
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePath, []byte(`{"v":1,"profile":"codex-subscription"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	if err := runGatewayAuthImport(cmd, nil); err != nil {
		t.Fatalf("runGatewayAuthImport: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error("import must remove the stale codex-subscription coverage record on a real mode switch")
	}
}

// --- status / state record --------------------------------------------------------------------

// TestGatewayAuthStatusNeverPrintsToken pins design-doc.md:175's hard constraint: `status` prints
// state/path/mode/exp/presence flags ONLY — never a token value, never a raw JWT, never PII.
func TestGatewayAuthStatusNeverPrintsToken(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	codexHome := codexAuthFixture(t, 1900000000, 1800000000, "acct-secret-marker")
	t.Setenv("CODEX_HOME", codexHome)

	importCmd := &cobra.Command{}
	importCmd.SetOut(new(strings.Builder))
	if err := runGatewayAuthImport(importCmd, nil); err != nil {
		t.Fatalf("runGatewayAuthImport: %v", err)
	}

	var out strings.Builder
	statusCmd := &cobra.Command{}
	statusCmd.SetOut(&out)
	if err := runGatewayAuthStatus(statusCmd, nil); err != nil {
		t.Fatalf("runGatewayAuthStatus: %v", err)
	}
	rendered := out.String()
	if strings.Contains(rendered, "codex-refresh-token-value") {
		t.Error("status output must never contain the refresh token value")
	}
	if strings.Contains(rendered, "acct-secret-marker") {
		t.Error("status output must never print the account id (PII per design-doc.md:175)")
	}
}

// TestGatewayAuthStatus_ReportsTornFileAsCorrupt pins the finer fail-closed taxonomy
// (investigation_report.md row 81): a present-but-unparseable state record reads as `corrupt`, a
// real condition from LiteLLM's own non-atomic `open(path,"w")` rewrite — not hypothetical.
func TestGatewayAuthStatus_ReportsTornFileAsCorrupt(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")

	statePath := filepath.Join(root, ".runtime", "gateway_auth", "codex-subscription.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(statePath, []byte(`{"v":1,"mode":"codex-subscri`), 0o600); err != nil { // torn write
		t.Fatal(err)
	}

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runGatewayAuthStatus(cmd, nil); err != nil {
		t.Fatalf("status must fail closed (report corrupt), not error out: %v", err)
	}
	if !strings.Contains(out.String(), gatewayAuthStateCorrupt) {
		t.Errorf("status must report state=%q for a torn file; got: %s", gatewayAuthStateCorrupt, out.String())
	}
}

// --- registration ------------------------------------------------------------------------------

// TestGatewayCmd_RegisteredOnRootCmd pins the three-level cobra registration
// (investigation_report.md row 69): gatewayCmd -> authCmd -> import|status, gatewayCmd on rootCmd.
func TestGatewayCmd_RegisteredOnRootCmd(t *testing.T) {
	var found *cobra.Command
	for _, c := range rootCmd.Commands() {
		if c.Name() == "gateway" {
			found = c
			break
		}
	}
	if found == nil {
		t.Fatal("gatewayCmd must be registered on rootCmd")
	}
	var authFound *cobra.Command
	for _, c := range found.Commands() {
		if c.Name() == "auth" {
			authFound = c
			break
		}
	}
	if authFound == nil {
		t.Fatal("authCmd must be registered on gatewayCmd")
	}
	names := map[string]bool{}
	for _, c := range authFound.Commands() {
		names[c.Name()] = true
	}
	if !names["import"] || !names["status"] {
		t.Errorf("authCmd must register both import and status subcommands; got %v", names)
	}
}

// --- refresh-ahead seam --------------------------------------------------------------------------

// TestGatewayTokenRefreshDoSeamPresentButDormant pins decisions.md D5 AND, per F7, mutation-proves
// the seam body's live branches instead of swallowing its return. Previously the test only checked
// `!= nil` and t.Logf'd any error, so mutation B6e (the seam returns an error on entry) survived.
// The three subcases below exercise the read / unmarshal / expiry-window branches
// (gateway_auth.go:495-514) so B6e trips the valid_fresh_handle case. The watchdog.go invariant
// (present but dormant; watchdogTick's 6-parameter signature stays FROZEN) is kept verbatim.
func TestGatewayTokenRefreshDoSeamPresentButDormant(t *testing.T) {
	if gatewayTokenRefreshDo == nil {
		t.Fatal("gatewayTokenRefreshDo must be defined as a var seam (ADR-009), even though uncalled this phase")
	}

	t.Run("no_handle", func(t *testing.T) {
		if err := gatewayTokenRefreshDo(t.TempDir(), time.Now()); err != nil {
			t.Errorf("no handle to refresh must be a nil result (nothing to do); got %v", err)
		}
	})

	t.Run("valid_fresh_handle", func(t *testing.T) {
		root := t.TempDir()
		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		if err := gatewayTokenRefreshDo(root, time.Now()); err != nil {
			t.Errorf("a well-formed fresh handle must refresh to nil (well outside the refresh window); got %v — this subcase catches mutation B6e (seam returns error on entry)", err)
		}
	})

	t.Run("corrupt_handle", func(t *testing.T) {
		root := t.TempDir()
		p := gatewayAuthHandlePath(root)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatalf("mkdir handle dir: %v", err)
		}
		if err := os.WriteFile(p, []byte("{not json"), 0o600); err != nil {
			t.Fatalf("write corrupt handle: %v", err)
		}
		if err := gatewayTokenRefreshDo(root, time.Now()); err == nil {
			t.Error("an unparseable handle must surface a non-nil error (the unmarshal branch is live, not swallowed)")
		}
	})

	src, err := os.ReadFile("watchdog.go")
	if err != nil {
		t.Fatalf("reading watchdog.go: %v", err)
	}
	if strings.Contains(string(src), "gatewayTokenRefreshDo") {
		t.Error("watchdog.go must NOT reference gatewayTokenRefreshDo this phase (D5: present but dormant; watchdogTick's signature is frozen)")
	}
}

// --- F33: accept the auth_mode literals a genuine `codex login` actually emits -----------------

// writeCodexAuthMode writes a nested Codex auth.json under a fresh $CODEX_HOME with a caller-chosen
// auth_mode (omitted entirely when mode == "") and well-formed tokens, returning that dir. It
// parallels codexAuthFixture but lets an F33 case exercise the exact auth_mode literal a real Codex
// CLI emits (chatgpt / chatgptAuthTokens / absent) — the fixed helper cannot vary it.
func writeCodexAuthMode(t *testing.T, mode string) string {
	t.Helper()
	dir := t.TempDir()
	body := map[string]any{
		"tokens": map[string]any{
			"id_token":      codexJWT(t, map[string]any{"exp": int64(1800000000), "chatgpt_account_id": "acct-variant"}),
			"access_token":  codexJWT(t, map[string]any{"exp": int64(1900000000)}),
			"refresh_token": "codex-refresh-token-value",
			"account_id":    "acct-variant",
		},
	}
	if mode != "" {
		body["auth_mode"] = mode
	}
	data, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshaling codex auth mode fixture: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), data, 0o600); err != nil {
		t.Fatalf("writing codex auth mode fixture: %v", err)
	}
	return dir
}

func importSucceedsForCodexMode(t *testing.T, mode string) {
	t.Helper()
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	t.Setenv("CODEX_HOME", writeCodexAuthMode(t, mode))

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	if err := runGatewayAuthImport(cmd, nil); err != nil {
		t.Fatalf("import must accept auth_mode=%q (a genuine codex login file); err=%v", mode, err)
	}
	if _, err := os.Stat(gatewayAuthHandlePath(root)); err != nil {
		t.Errorf("a handle must be written for auth_mode=%q: %v", mode, err)
	}
}

// TestGatewayAuthImport_AcceptsChatgptLowercase pins F33/D8: Codex emits lowercase `chatgpt`, which
// head refuses (gateway_auth.go:345 exact-matches "ChatGPT"). RED at head, GREEN after.
func TestGatewayAuthImport_AcceptsChatgptLowercase(t *testing.T) {
	importSucceedsForCodexMode(t, "chatgpt")
}

// TestGatewayAuthImport_AcceptsChatgptAuthTokens pins F33/D8: the `chatgptAuthTokens` variant is
// also a subscription session. RED at head.
func TestGatewayAuthImport_AcceptsChatgptAuthTokens(t *testing.T) {
	importSucceedsForCodexMode(t, "chatgptAuthTokens")
}

// TestGatewayAuthImport_AcceptsAbsentAuthModeWithTokens pins F33/D8: an absent auth_mode with tokens
// present is Codex's own ChatGPT inference (Option<AuthMode>). At head "" != "ChatGPT" refuses. RED.
func TestGatewayAuthImport_AcceptsAbsentAuthModeWithTokens(t *testing.T) {
	importSucceedsForCodexMode(t, "")
}

// TestGatewayAuthImport_AcceptsChatgptCaseInsensitive is PROTECTIVE: the canonical mixed-case
// "ChatGPT" must stay accepted while the fixture becomes lowercase (backward-tolerance). Passes at
// head (exact match) and after (case-insensitive).
func TestGatewayAuthImport_AcceptsChatgptCaseInsensitive(t *testing.T) {
	importSucceedsForCodexMode(t, "ChatGPT")
}

// --- F5: import idempotency (D6) ----------------------------------------------------------------

// TestGatewayAuthImport_NoOpsWhenHandleNewerThanSource pins F5/D6: when the on-disk handle is
// strictly newer than the source, a re-import is a no-op and the handle's mtime must not move. At
// head the handle is rewritten unconditionally (gateway_auth.go:370) so its mtime changes. RED.
func TestGatewayAuthImport_NoOpsWhenHandleNewerThanSource(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	t.Setenv("CODEX_HOME", codexAuthFixture(t, 1900000000, 1800000000, "acct-noop"))

	runImport := func() {
		cmd := &cobra.Command{}
		cmd.SetOut(new(strings.Builder))
		if err := runGatewayAuthImport(cmd, nil); err != nil {
			t.Fatalf("runGatewayAuthImport: %v", err)
		}
	}

	runImport()

	handlePath := gatewayAuthHandlePath(root)
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(handlePath, future, future); err != nil {
		t.Fatalf("chtimes handle newer than source: %v", err)
	}
	before, err := os.Stat(handlePath)
	if err != nil {
		t.Fatalf("stat handle: %v", err)
	}

	runImport()

	after, err := os.Stat(handlePath)
	if err != nil {
		t.Fatalf("stat handle after re-import: %v", err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Errorf("re-import with a handle newer than the source must be a no-op (handle mtime unchanged); before=%v after=%v", before.ModTime(), after.ModTime())
	}
}

// TestGatewayAuthImport_KeepsCoverageRecordWhenModeUnchanged pins F5/D6: the coverage record is
// removed only on a real mode switch (no prior handle), so an unchanged subscription re-import
// (prior handle present) must KEEP it. At head removeStaleGatewayCoverageRecord runs on every
// import (gateway_auth.go:378). RED.
func TestGatewayAuthImport_KeepsCoverageRecordWhenModeUnchanged(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	codexHome := codexAuthFixture(t, 1900000000, 1800000000, "acct-keep")
	t.Setenv("CODEX_HOME", codexHome)
	sourcePath := filepath.Join(codexHome, "auth.json")

	runImport := func() {
		cmd := &cobra.Command{}
		cmd.SetOut(new(strings.Builder))
		if err := runGatewayAuthImport(cmd, nil); err != nil {
			t.Fatalf("runGatewayAuthImport: %v", err)
		}
	}

	// First import establishes subscription mode (creates the handle). The coverage record is seeded
	// AFTER, so the second import runs with a prior handle present — mode UNCHANGED.
	runImport()

	coveragePath := modelCoveragePath(root, gatewayAuthProfileName)
	if err := os.MkdirAll(filepath.Dir(coveragePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(coveragePath, []byte(`{"v":1,"profile":"codex-subscription"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	// Force the second import to actually run its side-effects (source newer than the handle) so this
	// pins "mode unchanged keeps coverage", not merely the newer-handle no-op skip.
	future := time.Now().Add(time.Hour)
	if err := os.Chtimes(sourcePath, future, future); err != nil {
		t.Fatalf("chtimes source: %v", err)
	}

	runImport()

	if _, err := os.Stat(coveragePath); err != nil {
		t.Errorf("re-import with mode unchanged (a prior handle existed) must KEEP the coverage record; stat err %v", err)
	}
}

// --- F32: mode-switch removal targets every gateway-endpoint profile ----------------------------

// TestGatewayAuthImportRemovesCoverageRecordForAnyGatewayProfile pins F32: the coverage-record
// removal must cover every gateway-endpoint profile, not only the hard-wired "codex-subscription"
// literal (gateway_auth.go:138). A gateway profile named "codex-sub" keeps its stale record at head. RED.
func TestGatewayAuthImportRemovesCoverageRecordForAnyGatewayProfile(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")
	t.Setenv("CODEX_HOME", codexAuthFixture(t, 1900000000, 1800000000, "acct-anyprofile"))

	// A gateway-endpoint profile (file: secret => gateway, not direct-endpoint) whose name is NOT the
	// literal the head removal is hard-wired to.
	writeSecretFile(t, root, "secrets/codex-sub.key", "sk-gateway-master-key")
	writeValidModels(t, root, &config.ModelsConfig{
		Models: map[string]map[string]string{
			"codex-sub": {
				"ANTHROPIC_MODEL":      "gpt-5.6-sol",
				"ANTHROPIC_BASE_URL":   "https://gw.example:4000",
				"ANTHROPIC_AUTH_TOKEN": "file:secrets/codex-sub.key",
			},
		},
	})

	stalePath := modelCoveragePath(root, "codex-sub")
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stalePath, []byte(`{"v":1,"profile":"codex-sub"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetOut(new(strings.Builder))
	if err := runGatewayAuthImport(cmd, nil); err != nil {
		t.Fatalf("runGatewayAuthImport: %v", err)
	}
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Error(`import must remove the stale coverage record for EVERY gateway-endpoint profile, not only the hard-wired "codex-subscription" name`)
	}
}

// --- F12 / F1: status reads the handle, not just the .runtime mirror ----------------------------

// TestGatewayAuthStatusReportsDeviceCodeRequested pins F12: when the handle carries
// device_code_requested_at, status must name the device-code login attempt. Head never reads that
// field (gateway_auth.go:454-485). RED.
func TestGatewayAuthStatusReportsDeviceCodeRequested(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")

	writeSubscriptionHandleWithDeviceCode(t, root, time.Now().Add(24*time.Hour).Unix())
	writeSubscriptionState(t, root, healthySubscriptionState())

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runGatewayAuthStatus(cmd, nil); err != nil {
		t.Fatalf("runGatewayAuthStatus: %v", err)
	}
	rendered := strings.ToLower(out.String())
	if !strings.Contains(rendered, "device_code") && !strings.Contains(rendered, "attempted") {
		t.Errorf("status must name the device-code login attempt when device_code_requested_at is present; got: %s", out.String())
	}
}

// TestGatewayAuthStatusExpFromHandle pins F1's hidden status consumer: the `exp:` line must derive
// from the handle's own expires_at, never the stale .runtime mirror. Head prints st.AccessExpiresAt
// (the mirror, gateway_auth.go:482). RED. The RFC3339 expectation is grounded in import's own
// formatting of that field (gateway_auth.go:388).
func TestGatewayAuthStatusExpFromHandle(t *testing.T) {
	root := gatewayFactoryRoot(t)
	t.Chdir(root)
	t.Setenv("AF_ROLE", "")

	handleExp := time.Date(2033, 1, 2, 3, 4, 5, 0, time.UTC).Unix()
	handleExpRFC := time.Unix(handleExp, 0).UTC().Format(time.RFC3339)
	const staleMirror = "2001-01-01T00:00:00Z" // different year => unambiguous substring test

	writeSubscriptionHandle(t, root, handleExp, "codex-refresh-token", "tok")
	writeSubscriptionState(t, root, gatewayAuthState{
		Mode:                gatewayAuthProfileName,
		State:               gatewayAuthStateOK,
		AccessExpiresAt:     staleMirror,
		RefreshTokenPresent: true,
	})

	var out strings.Builder
	cmd := &cobra.Command{}
	cmd.SetOut(&out)
	if err := runGatewayAuthStatus(cmd, nil); err != nil {
		t.Fatalf("runGatewayAuthStatus: %v", err)
	}
	rendered := out.String()
	if strings.Contains(rendered, staleMirror) {
		t.Errorf("status exp must derive from the handle, never the stale .runtime mirror %q; got: %s", staleMirror, rendered)
	}
	if !strings.Contains(rendered, handleExpRFC) {
		t.Errorf("status exp must reflect the handle's expires_at (%s); got: %s", handleExpRFC, rendered)
	}
}

// --- T-VAR (D15): the on-disk schema-version wire key stays "v" ----------------------------------

// TestGatewayAuthStateWireKeyStaysV is PROTECTIVE: D15 renames the Go field V -> SchemaVersion for
// readability but MUST keep json:"v". This guards the on-disk wire key against a botched rename.
// The struct literal uses the CURRENT field name V (gateway_auth.go:172); the rename updates it.
func TestGatewayAuthStateWireKeyStaysV(t *testing.T) {
	st := gatewayAuthState{SchemaVersion: 1, Mode: gatewayAuthProfileName, State: gatewayAuthStateOK}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatalf("marshal gatewayAuthState: %v", err)
	}
	s := string(data)
	if !strings.Contains(s, `"v":`) {
		t.Errorf(`state record wire key must stay "v"; got: %s`, s)
	}
	if strings.Contains(s, `"schemaVersion"`) || strings.Contains(s, `"version"`) {
		t.Errorf("state record must NOT expose a renamed wire key (schemaVersion/version); got: %s", s)
	}

	var back gatewayAuthState
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("round-trip unmarshal: %v", err)
	}
	reData, err := json.Marshal(back)
	if err != nil {
		t.Fatalf("round-trip re-marshal: %v", err)
	}
	if string(reData) != s {
		t.Errorf("gatewayAuthState must round-trip byte-identically; before=%s after=%s", s, reData)
	}
}

// TestGatewayAuthStatusJSON_SelectedModeAndDeviceCode is K18 (concern_tests.md item 4): `status
// --json` must gain a selected_mode field and a device-code-requested signal, and must never leak
// a token or PII in the marshaled payload. Uses the package-level gatewayAuthStatusCmd (same
// pattern as tokenomicsCmd/telemetryCmd) since it already registers --json in init().
func TestGatewayAuthStatusJSON_SelectedModeAndDeviceCode(t *testing.T) {
	t.Run("record_present_with_device_code", func(t *testing.T) {
		root := gatewayFactoryRoot(t)
		t.Chdir(root)
		t.Setenv("AF_ROLE", "")

		p := gatewayAuthHandlePath(root)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		body := map[string]any{
			"access_token":             "PLANTED-JSON-ACCESS-MARKER",
			"refresh_token":            "PLANTED-JSON-REFRESH-MARKER",
			"id_token":                 "",
			"expires_at":               time.Now().Add(24 * time.Hour).Unix(),
			"account_id":               "acct-fixture",
			"device_code_requested_at": time.Now().Add(-2 * time.Minute).Unix(),
		}
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, data, 0o600); err != nil {
			t.Fatal(err)
		}
		writeSubscriptionState(t, root, healthySubscriptionState())
		if err := os.WriteFile(gatewayAuthModeRecordPath(root), []byte("codex-subscription\n"), 0o644); err != nil {
			t.Fatalf("write gatewayAuthMode record: %v", err)
		}

		if err := gatewayAuthStatusCmd.Flags().Set("json", "true"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := gatewayAuthStatusCmd.Flags().Set("json", "false"); err != nil {
				t.Fatal(err)
			}
		})
		var out strings.Builder
		gatewayAuthStatusCmd.SetOut(&out)
		if err := runGatewayAuthStatus(gatewayAuthStatusCmd, nil); err != nil {
			t.Fatalf("runGatewayAuthStatus: %v", err)
		}

		var rec struct {
			SelectedMode          string `json:"selected_mode"`
			DeviceCodeRequested   bool   `json:"device_code_requested"`
			DeviceCodeRequestedAt any    `json:"device_code_requested_at"`
		}
		if err := json.Unmarshal([]byte(out.String()), &rec); err != nil {
			t.Fatalf("unmarshal status json: %v; out=%q", err, out.String())
		}
		if rec.SelectedMode != "codex-subscription" {
			t.Errorf("selected_mode must reflect the persisted record; got %q", rec.SelectedMode)
		}
		if !rec.DeviceCodeRequested {
			t.Errorf("device_code_requested must be true when the handle carries device_code_requested_at; out=%q", out.String())
		}

		s := out.String()
		for _, marker := range []string{"PLANTED-JSON-ACCESS-MARKER", "PLANTED-JSON-REFRESH-MARKER", "acct-fixture"} {
			if strings.Contains(s, marker) {
				t.Errorf("status --json must never leak a token or PII; found %q in %q", marker, s)
			}
		}
	})

	t.Run("no_device_code_no_record_falls_through_migration", func(t *testing.T) {
		root := gatewayFactoryRoot(t)
		t.Chdir(root)
		t.Setenv("AF_ROLE", "")

		writeSubscriptionHandle(t, root, time.Now().Add(24*time.Hour).Unix(), "codex-refresh-token", "tok")
		writeSubscriptionState(t, root, healthySubscriptionState())

		if err := gatewayAuthStatusCmd.Flags().Set("json", "true"); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := gatewayAuthStatusCmd.Flags().Set("json", "false"); err != nil {
				t.Fatal(err)
			}
		})
		var out strings.Builder
		gatewayAuthStatusCmd.SetOut(&out)
		if err := runGatewayAuthStatus(gatewayAuthStatusCmd, nil); err != nil {
			t.Fatalf("runGatewayAuthStatus: %v", err)
		}

		var rec struct {
			SelectedMode        string `json:"selected_mode"`
			DeviceCodeRequested bool   `json:"device_code_requested"`
		}
		if err := json.Unmarshal([]byte(out.String()), &rec); err != nil {
			t.Fatalf("unmarshal status json: %v; out=%q", err, out.String())
		}
		if rec.DeviceCodeRequested {
			t.Errorf("device_code_requested must be false when the handle carries no device_code_requested_at; out=%q", out.String())
		}
		if rec.SelectedMode != "codex-subscription" {
			t.Errorf("selected_mode must still report the migration-inferred mode (a read-only report, not a gate); got %q", rec.SelectedMode)
		}
	})
}
