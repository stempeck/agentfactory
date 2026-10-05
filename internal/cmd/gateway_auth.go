package cmd

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/fsutil"
)

// K1 of issue #686 (PR #688), Phase 1: `af gateway auth import|status`. Bridges an authenticated
// Codex CLI ChatGPT-subscription login ($CODEX_HOME/auth.json, E1) into the gateway's own flat
// credential handle (.agentfactory/secrets/chatgpt/auth.json, E2) that LiteLLM's Authenticator
// reads directly — af never talks OAuth itself, it only translates and audits (data.md Option D1;
// D5/D7/D8 rejected any af-owned refresh or write-back into ~/.codex).
//
// `import` is operator-only (writes credential material that would otherwise ride into every
// spawned agent's launch line); `status` is read-only and ungated, mirroring `af config models
// show`'s read/write asymmetry.

// codexTokens is E1's "tokens" sub-object (storage.rs:L36-L66; token_data.rs).
type codexTokens struct {
	IDToken      string `json:"id_token"`
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	AccountID    string `json:"account_id"`
}

// codexAuthDotJSON is $CODEX_HOME/auth.json's shape (E1, data.md §1). Tokens is a pointer so an
// API-key login (OPENAI_API_KEY set, no "tokens" key at all) decodes to a nil Tokens rather than
// a zero-valued struct — the on-disk signal that distinguishes an API-key login from a ChatGPT
// one (decisions.md D3 is the separate case of a PRESENT-but-partial tokens object).
type codexAuthDotJSON struct {
	AuthMode string       `json:"auth_mode"`
	Tokens   *codexTokens `json:"tokens"`
}

// gatewayAuthHandle is E2, LiteLLM's own flat auth-file schema (authenticator.py:L85-L100).
// ExpiresAt is pre-populated at import time — a raw Unix integer decoded from access_token's JWT
// exp claim, never id_token's, and never RFC3339 (investigation_report.md's cross-verified finding
// against the installed LiteLLM wheel: `_is_token_expired` does `float(expires_at) - 60` against
// `time.time()` and never re-derives it once present) — so LiteLLM's own read-path writes never
// fire.
type gatewayAuthHandle struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token"`
	ExpiresAt    int64  `json:"expires_at,omitempty"`
	AccountID    string `json:"account_id,omitempty"`
}

// translateCodexAuth is the pure E1->E2 translation, separated from all I/O so the JWT-source
// finding is unit-testable without touching disk. Never called with a nil Tokens —
// runGatewayAuthImport refuses that case (an API-key login) before reaching here.
func translateCodexAuth(nested codexAuthDotJSON) (gatewayAuthHandle, error) {
	if nested.Tokens == nil {
		return gatewayAuthHandle{}, errors.New("translateCodexAuth: tokens is nil (API-key login); the caller must refuse before translating")
	}
	t := nested.Tokens
	flat := gatewayAuthHandle{
		AccessToken:  t.AccessToken,
		RefreshToken: t.RefreshToken,
		IDToken:      t.IDToken,
		AccountID:    t.AccountID,
	}

	if claims := decodeJWTClaims(t.AccessToken); claims != nil {
		if exp, ok := claims["exp"].(float64); ok {
			flat.ExpiresAt = int64(exp)
		}
	}

	if flat.AccountID == "" {
		// account_id absent from the source: fall back to JWT-derivation, id_token first then
		// access_token — LiteLLM's own priority (authenticator.py:L132-L141).
		flat.AccountID = chatgptAccountIDFromClaims(decodeJWTClaims(t.IDToken))
		if flat.AccountID == "" {
			flat.AccountID = chatgptAccountIDFromClaims(decodeJWTClaims(t.AccessToken))
		}
	}

	return flat, nil
}

// isChatGPTAuthMode reports whether a Codex auth.json auth_mode names a ChatGPT-subscription
// session. A genuine `codex login` emits "chatgpt" or "chatgptAuthTokens" (login.rs); matched
// case-insensitively (F33/D8).
func isChatGPTAuthMode(mode string) bool {
	switch strings.ToLower(mode) {
	case "chatgpt", "chatgptauthtokens":
		return true
	}
	return false
}

// chatgptAccountIDFromClaims extracts a JWT's chatgpt_account_id, reading the namespaced
// "https://api.openai.com/auth" claim first (where LiteLLM and a real Codex id_token carry it —
// authenticator.py:L132-141) and falling back to a flat top-level claim (F6). Returns "" when
// neither is a non-empty string.
func chatgptAccountIDFromClaims(claims map[string]any) string {
	if claims == nil {
		return ""
	}
	if ns, ok := claims["https://api.openai.com/auth"].(map[string]any); ok {
		if acct, ok := ns["chatgpt_account_id"].(string); ok && acct != "" {
			return acct
		}
	}
	if acct, ok := claims["chatgpt_account_id"].(string); ok && acct != "" {
		return acct
	}
	return ""
}

// decodeJWTClaims mirrors authenticator.py's own tolerance (`_decode_jwt_claims` catches every
// exception and returns {} — decisions.md D4): a malformed or undecodable JWT degrades to no
// claims rather than an error, the same place the actual consumer degrades to. Never verifies a
// signature — this design is a read-only translator, not an auth boundary of its own.
func decodeJWTClaims(token string) map[string]any {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return map[string]any{}
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return map[string]any{}
	}
	var claims map[string]any
	if err := json.Unmarshal(payload, &claims); err != nil {
		return map[string]any{}
	}
	return claims
}

// gatewayAuthProfileName is the fixed literal decisions.md D1 settled on: every design artifact
// that names a subscription-mode profile (data.md, api.md, design-doc.md, the E4 worked example)
// uses this identical literal, and `import` has no --profile flag to derive a different one from.
const gatewayAuthProfileName = "codex-subscription"

// gatewayAuthHandlePath is E2's path: 0700 dir, 0600 file (DO-NOT-CHANGE — must not copy
// writeModelCoverageRecord's 0o644).
func gatewayAuthHandlePath(root string) string {
	return filepath.Join(config.ConfigDir(root), "secrets", "chatgpt", "auth.json")
}

// gatewayHandleNonEmpty is the single subscription-handle presence predicate (F17/D10): a handle
// counts as present only when it is a non-empty regular file. Every reader — the mode-resolution
// ladder (install.go), the E2 install preflight, `status`, and the derived-mode check
// (subscriptionHandlePresent) — routes through this one definition so a 0-byte handle can never
// count as present on one surface and absent on another.
func gatewayHandleNonEmpty(root string) bool {
	info, err := os.Stat(gatewayAuthHandlePath(root))
	return err == nil && !info.IsDir() && info.Size() > 0
}

// gatewayAuthModeRecordPath is K1's record location (issue #693 Phase 1, design-doc.md:115).
func gatewayAuthModeRecordPath(root string) string {
	return filepath.Join(config.ConfigDir(root), "litellm-auth-mode")
}

// litellmAuthModeLanePrefixRe matches a lane-prefixed backend model id in litellm.yaml's
// model_list (the same "openai/" / "chatgpt/" textual idiom parseLitellmSeedEntries pins in
// quickstart_provisioning_shape_test.go:288 — that helper is test-only, so production code
// reimplements the scan here rather than depending on a _test.go symbol).
var litellmAuthModeLanePrefixRe = regexp.MustCompile(`^\s+model:\s*([^\s#]+)`)

// gatewayAuthMode is K1 (issue #693 Phase 1): the record-over-inference resolver for the
// LiteLLM gateway's auth mode. It is a distinct, additive resolver in this phase — it does
// NOT call, delegate to, or get called by the existing resolveLitellmAuthMode ladder
// (install.go:924); that reconciliation is Phase 2a's K5 job (decisions.md D1). Two parallel
// ladders coexisting for this one phase is spec-sanctioned scaffolding, not the silently-
// duplicated resolver the K1 GOTCHA-DECISION warns against, because it is named and dated here
// with a forward pointer to the phase that unifies them.
//
// Record present: content must be exactly "api-key" or "codex-subscription" after trimming one
// optional trailing "\n" (and a defensive "\r"); anything else — including a present-but-empty
// file — is invalid content and errors naming the file (decisions.md D4; a zero-byte record is
// treated as a torn write, never as "absent").
//
// Record absent (os.IsNotExist): write-once migration through 5 tiers, first match wins,
// immediate return (decisions.md D3): (1) an existing gateway-relaunch.sh's baked
// LITELLM_AUTH_MODE= line; (2) litellm.yaml's model_list lane prefix; (3) exactly one non-empty
// credential handle; (4) both handles and no other signal — the single surviving refusal naming
// --litellm-auth; (5) neither — default api-key. Migration never writes the record itself
// (quickstart's setup_litellm, K13, is the only writer in the tree); it reports migrated=true so
// callers can log "mode inferred (no record yet)".
func gatewayAuthMode(root string) (mode string, migrated bool, err error) {
	recPath := gatewayAuthModeRecordPath(root)
	data, statErr := os.ReadFile(recPath)
	if statErr == nil {
		content := strings.TrimSuffix(strings.TrimSuffix(string(data), "\n"), "\r")
		if content == "api-key" || content == "codex-subscription" {
			return content, false, nil
		}
		return "", false, fmt.Errorf("gateway auth mode record %s has invalid content (want %q or %q, one line, trailing newline tolerated)",
			recPath, "api-key", "codex-subscription")
	}
	if !os.IsNotExist(statErr) {
		return "", false, fmt.Errorf("reading gateway auth mode record %s: %w", recPath, statErr)
	}

	// Tier 1: an existing gateway-relaunch.sh's baked LITELLM_AUTH_MODE= line.
	if relaunch, relErr := os.ReadFile(filepath.Join(config.ConfigDir(root), "gateway-relaunch.sh")); relErr == nil {
		if m := regexp.MustCompile(`LITELLM_AUTH_MODE=\"?([a-zA-Z0-9-]+)\"?`).FindSubmatch(relaunch); m != nil {
			switch string(m[1]) {
			case "api-key", "codex-subscription":
				return string(m[1]), true, nil
			}
		}
	}

	// Tier 2: litellm.yaml's model_list lane prefix (openai/ only ⇒ api-key, chatgpt/ only ⇒
	// codex-subscription).
	if yaml, yamlErr := os.ReadFile(filepath.Join(config.ConfigDir(root), "litellm.yaml")); yamlErr == nil {
		sawOpenAI, sawChatGPT := false, false
		for _, line := range strings.Split(string(yaml), "\n") {
			if m := litellmAuthModeLanePrefixRe.FindStringSubmatch(line); m != nil {
				switch {
				case strings.HasPrefix(m[1], "openai/"):
					sawOpenAI = true
				case strings.HasPrefix(m[1], "chatgpt/"):
					sawChatGPT = true
				}
			}
		}
		switch {
		case sawOpenAI && !sawChatGPT:
			return "api-key", true, nil
		case sawChatGPT && !sawOpenAI:
			return "codex-subscription", true, nil
		}
	}

	// Tiers 3/4/5: credential-handle presence.
	hasKey := apiKeySecretPresent(root)
	hasSubscription := gatewayHandleNonEmpty(root)
	switch {
	case hasKey && hasSubscription:
		return "", false, fmt.Errorf("both an OpenAI API key and a ChatGPT-subscription handle exist with no recorded gateway auth mode; pass --litellm-auth=<api-key|codex-subscription> to choose one")
	case hasSubscription:
		return "codex-subscription", true, nil
	case hasKey:
		return "api-key", true, nil
	default:
		return "api-key", true, nil
	}
}

// removeStaleGatewayCoverageRecord clears the model-coverage verdict recorded under every
// gateway-endpoint profile (a file: ANTHROPIC_AUTH_TOKEN) on a mode switch (decisions.md D1/D6,
// F32), plus the seeded subscription profile name itself, so a stale api-key-era coverage record
// never survives under a profile that now authenticates a ChatGPT subscription. Targeting every
// gateway-endpoint profile rather than only the hard-wired seed name means an operator running the
// gateway under any profile name is covered. Each name keeps the filepath.Base traversal guard
// (config_models.go precedent). A missing/unreadable models.json is best-effort — the seed name is
// still cleared.
func removeStaleGatewayCoverageRecord(root string) error {
	names := map[string]struct{}{gatewayAuthProfileName: {}}
	if cfg, err := config.LoadModelsConfig(root); err == nil && cfg != nil {
		for name, profile := range cfg.Models {
			if strings.HasPrefix(profile[authTokenKey], secretPrefix) {
				names[name] = struct{}{}
			}
		}
	}
	for name := range names {
		if name != filepath.Base(name) || name == "." || name == ".." {
			continue
		}
		if err := os.Remove(modelCoveragePath(root, name)); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// gatewayAuthStateVersion / the state enum are E4 (data.md §4, Option D9).
const gatewayAuthStateVersion = 1

const (
	gatewayAuthStateOK             = "ok"
	gatewayAuthStateMissing        = "missing"
	gatewayAuthStateCorrupt        = "corrupt"
	gatewayAuthStateNoRefreshToken = "no-refresh-token"
	gatewayAuthStateRevoked        = "revoked" // written by a future refresh-ahead caller, decisions.md D5
	gatewayAuthStateUnverified     = "unverified"
)

// gatewayAuthState is E4, versioned and fail-closed like modelCoverageRecord. Never stores a
// token, a JWT, an email, or a hash of secret bytes (data.md §4: "a hash of a refresh token is an
// oracle").
// LastVerifiedAt and DeviceCodeAttempted are part of E3's documented schema but have no writer
// yet in Phase 1: `import` stamps ImportedAt (the moment it wrote the handle), not
// LastVerifiedAt (the moment the state was last *confirmed* against reality — a `status`/`check`
// concern, neither of which writes this record yet); DeviceCodeAttempted mirrors the handle's own
// `device_code_requested_at`, which only LiteLLM's Authenticator ever sets, never `import`. Both
// fields are declared now (zero-valued, `omitempty`) so the on-disk schema matches what
// IMPLREADME_PHASE1.md documents Phase 1 as implementing and Phase 2's reader is not surprised by
// their absence; a later phase's `status`/`check`/refresh-ahead writer populates them for real.
type gatewayAuthState struct {
	SchemaVersion       int    `json:"v"`
	Mode                string `json:"mode"`
	AuthDir             string `json:"auth_dir"`
	ImportedAt          string `json:"imported_at"`
	Source              string `json:"source"`
	SourceMtime         string `json:"source_mtime,omitempty"`
	LastVerifiedAt      string `json:"last_verified_at,omitempty"`
	State               string `json:"state"`
	AccessExpiresAt     string `json:"access_expires_at,omitempty"`
	RefreshTokenPresent bool   `json:"refresh_token_present"`
	AccountIDPresent    bool   `json:"account_id_present"`
	DeviceCodeAttempted bool   `json:"device_code_attempted,omitempty"`
}

func gatewayAuthStatePath(root string) string {
	return filepath.Join(root, ".runtime", "gateway_auth", gatewayAuthProfileName+".json")
}

// writeGatewayAuthState stamps the schema version and writes atomically at 0600 (DO-NOT-CHANGE —
// must not copy writeModelCoverageRecord's 0o644; claims.md #7).
func writeGatewayAuthState(root string, st gatewayAuthState) error {
	dir := filepath.Dir(gatewayAuthStatePath(root))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	st.SchemaVersion = gatewayAuthStateVersion
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("marshaling gateway auth state: %w", err)
	}
	data = append(data, '\n')
	return fsutil.WriteFileAtomic(gatewayAuthStatePath(root), data, 0o600)
}

// readGatewayAuthState is the E4 fail-closed reader, modeled on readModelCoverageRecord but with
// a finer taxonomy (investigation_report.md): missing (ENOENT) and corrupt (present but
// unparseable — a real condition from LiteLLM's own non-atomic `open(path,"w")` rewrite, not
// hypothetical) are distinguished rather than collapsed into one boolean, because an operator
// needs to know which remedy applies.
func readGatewayAuthState(root string) (gatewayAuthState, string) {
	data, err := os.ReadFile(gatewayAuthStatePath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return gatewayAuthState{}, gatewayAuthStateMissing
		}
		return gatewayAuthState{}, gatewayAuthStateUnverified
	}
	var st gatewayAuthState
	if err := json.Unmarshal(data, &st); err != nil {
		return gatewayAuthState{}, gatewayAuthStateCorrupt
	}
	if st.SchemaVersion != gatewayAuthStateVersion {
		return gatewayAuthState{}, gatewayAuthStateCorrupt
	}
	return st, st.State
}

// gatewayAuthImportRefusal is decisions.md D9: fresh wording (NOT recoveryResetRefusal or
// teardownRefusalFormat verbatim, per IMPLREADME_PHASE1.md's explicit prohibition), same
// four-part shape as its siblings (name the surface, state why it's operator-only, forbid retry,
// redirect to the operator) but scoped to `import`'s own blast radius — it writes one file under
// secrets/chatgpt/, not a factory-wide teardown — anchored on api.md:157's E7 ("operator action;
// run from a host shell"). Never names the detection mechanism (ux.md L36-39).
const gatewayAuthImportRefusal = `gateway auth import refused: agent context (af gateway auth import)
This command writes the gateway's OAuth credential handle under
.agentfactory/secrets/chatgpt/ — material every spawned agent's launch line would then be
exposed to on its own host. Importing gateway credentials is an operator action: run it
from a host shell.
Do NOT retry and do NOT look for another way to import it. If you believe an import is
genuinely needed, tell your operator (af mail send manager -s "gateway auth import request" -m "...")
and continue with your remaining work.`

var gatewayCmd = &cobra.Command{
	Use:   "gateway",
	Short: "Gateway credential and routing commands",
}

var authCmd = &cobra.Command{
	Use:   "auth",
	Short: "Manage the gateway's upstream ChatGPT-subscription credential handle",
}

var gatewayAuthImportCmd = &cobra.Command{
	Use:   "import",
	Short: "Import an authenticated Codex CLI session into the gateway's credential handle",
	Long: `Read $CODEX_HOME/auth.json (default ~/.codex/auth.json), or --from <path>, and
translate a ChatGPT-subscription Codex login into the gateway's own flat credential handle
at .agentfactory/secrets/chatgpt/auth.json (0600, in a 0700 directory).

Refuses an API-key login (OPENAI_API_KEY set, no tokens), a keyring-backed login (leaves
no file to read — on-disk indistinguishable from never having logged in) or a non-ChatGPT
auth_mode: subscription mode needs an actual 'codex login' ChatGPT session.

Idempotent: re-running against an unchanged source is a no-op; a changed source replaces
the handle. Operator-only — refused in agent context.`,
	Args: cobra.NoArgs,
	RunE: runGatewayAuthImport,
}

var gatewayAuthStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report the gateway credential handle's state (never prints a token)",
	Long: `Offline audit of the imported gateway credential handle and its state record:
existence, permission, git-tracking, expiry and presence flags only. Never prints a
token value, a raw JWT, or any account identifier — read-only, ungated.`,
	Args: cobra.NoArgs,
	RunE: runGatewayAuthStatus,
}

func init() {
	gatewayAuthImportCmd.Flags().String("from", "", "path to a Codex auth.json (default: $CODEX_HOME/auth.json, or ~/.codex/auth.json)")
	gatewayAuthStatusCmd.Flags().Bool("json", false, "print machine-readable JSON")
	authCmd.AddCommand(gatewayAuthImportCmd)
	authCmd.AddCommand(gatewayAuthStatusCmd)
	gatewayCmd.AddCommand(authCmd)
	rootCmd.AddCommand(gatewayCmd)
}

// codexAuthSourcePath resolves the Codex auth.json path: --from wins, else $CODEX_HOME/auth.json,
// else ~/.codex/auth.json. The env read lives in this cmd-layer function rather than
// internal/config per ADR-004/ADR-013.
func codexAuthSourcePath(cmd *cobra.Command) (string, error) {
	if from, _ := cmd.Flags().GetString("from"); from != "" {
		return from, nil
	}
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "auth.json"), nil
	}
	userHome, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("af gateway auth import: resolving home directory: %w", err)
	}
	return filepath.Join(userHome, ".codex", "auth.json"), nil
}

// runGatewayAuthImport is `af gateway auth import`.
func runGatewayAuthImport(cmd *cobra.Command, _ []string) error {
	if callerAuthority() != AuthorityOperator {
		return errors.New(gatewayAuthImportRefusal)
	}

	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	authPath, err := codexAuthSourcePath(cmd)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(authPath)
	if err != nil {
		if os.IsNotExist(err) {
			// E2-shaped refusal (decisions.md D2), adapted to import's own call site: also
			// covers a keyring-backed login (D8), which is on-disk-indistinguishable from
			// "never ran codex login" — both are ENOENT on this exact path.
			return fmt.Errorf("af gateway auth import: no Codex CLI session found at %s — run 'codex login' (or 'codex login --device-auth' on a headless host — openai/codex cli/src/login.rs:L118), then re-run 'af gateway auth import'", authPath)
		}
		return fmt.Errorf("af gateway auth import: reading %s: %w", authPath, err)
	}

	var nested codexAuthDotJSON
	if err := json.Unmarshal(data, &nested); err != nil {
		return fmt.Errorf("af gateway auth import: %s is not valid JSON: %w", authPath, err)
	}
	if nested.Tokens == nil {
		return fmt.Errorf("af gateway auth import: the Codex CLI at %s is logged in with an API key, not a ChatGPT session; subscription mode needs 'codex login' (ChatGPT)", authPath)
	}
	// A genuine `codex login` emits auth_mode "chatgpt" or "chatgptAuthTokens" (login.rs), and Codex
	// treats an absent auth_mode with tokens present as ChatGPT (Option<AuthMode>). Only a NON-empty,
	// non-ChatGPT literal is refused — matched case-insensitively so a future capitalisation cannot
	// cause a wrong refusal (F33/D8).
	if nested.AuthMode != "" && !isChatGPTAuthMode(nested.AuthMode) {
		return fmt.Errorf("af gateway auth import: %s auth_mode is %q, not a ChatGPT session; subscription mode needs 'codex login' (ChatGPT)", authPath, nested.AuthMode)
	}

	flat, err := translateCodexAuth(nested)
	if err != nil {
		return fmt.Errorf("af gateway auth import: %w", err)
	}

	handlePath := gatewayAuthHandlePath(root)

	// F5/D6: a re-import whose on-disk handle is strictly newer than the source is a no-op — the
	// source has not changed since it was last translated, so re-writing would only churn the
	// handle's mtime and needlessly re-run the side-effects. sourceInfo is reused below for the
	// recorded source_mtime.
	sourceInfo, sourceStatErr := os.Stat(authPath)
	if handleInfo, statErr := os.Stat(handlePath); statErr == nil && sourceStatErr == nil && handleInfo.ModTime().After(sourceInfo.ModTime()) {
		fmt.Fprintf(cmd.OutOrStdout(), "af gateway auth import: %s is already newer than %s — nothing to import\n", handlePath, authPath)
		return nil
	}

	// F5/D6/BODY-2: the coverage record is cleared only on a real mode switch — the mode the factory
	// currently resolves to (gatewayAuthMode) differs from the mode the prior state record names.
	// Keying on !subscriptionHandlePresent stuck false forever once any handle existed, so a genuine
	// api-key→subscription switch with a handle already on disk was missed and the stale coverage
	// record survived. An unresolvable mode (e.g. both handles present, no record) is treated as no
	// switch — no churn. Sampled BEFORE the handle write below.
	prevState, _ := readGatewayAuthState(root)
	resolvedMode, _, resolvedModeErr := gatewayAuthMode(root)
	modeSwitch := resolvedModeErr == nil && resolvedMode != prevState.Mode

	secretsDir := filepath.Dir(handlePath)
	// os.MkdirAll alone is umask-subject and will not re-tighten a pre-existing looser
	// directory, so the perm is asserted explicitly with os.Chmod too.
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		return fmt.Errorf("af gateway auth import: creating %s: %w", secretsDir, err)
	}
	if err := os.Chmod(secretsDir, 0o700); err != nil {
		return fmt.Errorf("af gateway auth import: %w", err)
	}

	handleData, err := json.MarshalIndent(flat, "", "  ")
	if err != nil {
		return fmt.Errorf("af gateway auth import: marshaling handle: %w", err)
	}
	handleData = append(handleData, '\n')
	if err := fsutil.WriteFileAtomic(handlePath, handleData, 0o600); err != nil {
		return fmt.Errorf("af gateway auth import: writing %s: %w", handlePath, err)
	}

	// Fail-closed side-effect ordering (decisions.md D6): handle write, then (on a real mode switch
	// only) stale coverage-record removal, then the E4 state record last — a crash between any two
	// steps self-heals on the next import run, and the state record only ever describes a handle
	// that provably already exists on disk.
	if modeSwitch {
		if err := removeStaleGatewayCoverageRecord(root); err != nil {
			return fmt.Errorf("af gateway auth import: removing stale coverage record: %w", err)
		}
	}

	state := gatewayAuthStateOK
	if flat.RefreshToken == "" {
		state = gatewayAuthStateNoRefreshToken
	}
	accessExpiresAt := ""
	if flat.ExpiresAt != 0 {
		accessExpiresAt = time.Unix(flat.ExpiresAt, 0).UTC().Format(time.RFC3339)
	}
	sourceMtime := ""
	if sourceStatErr == nil {
		sourceMtime = sourceInfo.ModTime().UTC().Format(time.RFC3339)
	}
	st := gatewayAuthState{
		Mode:                gatewayAuthProfileName,
		AuthDir:             secretsDir,
		ImportedAt:          time.Now().UTC().Format(time.RFC3339),
		Source:              authPath,
		SourceMtime:         sourceMtime,
		State:               state,
		AccessExpiresAt:     accessExpiresAt,
		RefreshTokenPresent: flat.RefreshToken != "",
		AccountIDPresent:    flat.AccountID != "",
	}
	if err := writeGatewayAuthState(root, st); err != nil {
		return fmt.Errorf("af gateway auth import: writing state record: %w", err)
	}

	fmt.Fprintf(cmd.OutOrStdout(), "af gateway auth import: wrote %s (mode=%s, state=%s)\n", handlePath, st.Mode, st.State)
	return nil
}

// gatewayAuthStatusJSON is the --json payload for `af gateway auth status`. Like the plain-text
// form, it carries state/path/presence flags only — never a token value.
//
// SelectedMode (issue #693 Phase 6 K18, decisions.md D8a) is the persisted/migration-inferred
// gatewayAuthMode(root) report — a read-only report, not a gate, so an unresolvable mode simply
// omits the field via omitempty rather than failing the command, matching every other field's own
// degrade-to-zero-value contract on missing/corrupt data. DeviceCodeRequestedAt (D8b) is
// RFC3339-normalized like AccessExpiresAt/ExpiresAt elsewhere on this struct.
type gatewayAuthStatusJSON struct {
	State                 string                     `json:"state"`
	Path                  string                     `json:"path"`
	HandlePresent         bool                       `json:"handle_present"`
	HandleValid           bool                       `json:"handle_valid"`
	Mode                  string                     `json:"mode,omitempty"`
	AccessExpiresAt       string                     `json:"access_expires_at,omitempty"`
	RefreshTokenPresent   bool                       `json:"refresh_token_present"`
	AccountIDPresent      bool                       `json:"account_id_present"`
	SelectedMode          string                     `json:"selected_mode,omitempty"`
	DeviceCodeRequested   bool                       `json:"device_code_requested"`
	DeviceCodeRequestedAt string                     `json:"device_code_requested_at,omitempty"`
	Launch                *gatewayLaunchIdentityJSON `json:"launch,omitempty"`
}

// gatewayLaunchIdentityJSON is the launch-identity object status --json surfaces from
// .runtime/gateway/launch.json (K18/Interface, decisions.md D16, T17): what the running gateway was
// actually launched with. It deliberately omits credential_ref — an opaque credential-version marker
// (openai.key mtime or the import's imported_at) that only the reconcile comparison consumes, not an
// operator-facing fact.
type gatewayLaunchIdentityJSON struct {
	Mode           string `json:"mode,omitempty"`
	ConfigSHA256   string `json:"config_sha256,omitempty"`
	LitellmVersion string `json:"litellm_version,omitempty"`
	PanePID        int    `json:"pane_pid,omitempty"`
}

// runGatewayAuthStatus is `af gateway auth status`. Offline audit only, ungated (read-only).
func runGatewayAuthStatus(cmd *cobra.Command, _ []string) error {
	wd, err := getWd()
	if err != nil {
		return err
	}
	root, err := resolveInvokerRoot(wd)
	if err != nil {
		return err
	}

	handlePath := gatewayAuthHandlePath(root)
	handlePresent := false
	handleValid := false
	handleExp := ""
	deviceCodeAttempted := false
	deviceCodeRequestedAt := ""
	if gatewayHandleNonEmpty(root) {
		handlePresent = true
		if info, statErr := os.Stat(handlePath); statErr == nil && info.Mode().Perm()&0o077 != 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "warning: handle %s is group/other-accessible (mode %04o); tighten to 0600\n", handlePath, info.Mode().Perm())
		}
		if tracked := runGitDetect(root, "git", "ls-files", "--error-unmatch", handlePath); tracked != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "warning: handle %s is tracked by git; move it out of version control\n", handlePath)
		}
		if data, readErr := os.ReadFile(handlePath); readErr == nil {
			var h gatewayAuthHandle
			if handleValid = json.Unmarshal(data, &h) == nil; handleValid && h.ExpiresAt != 0 {
				// F1/D7: expiry is the handle's own fact — LiteLLM refreshes it in place — never the
				// .runtime state-record mirror.
				handleExp = time.Unix(h.ExpiresAt, 0).UTC().Format(time.RFC3339)
			}
			// F12: the handle's device_code_requested_at is set only by LiteLLM's device-code login
			// path, never by import; status surfaces the attempt (the field is not on
			// gatewayAuthHandle, which is DO-NOT-CHANGE, so it is read raw). K18/D8b widens this same
			// raw-read to also surface the RFC3339-normalized timestamp on --json, reusing the
			// already-computed bool rather than a second file read.
			var raw struct {
				DeviceCodeRequestedAt json.RawMessage `json:"device_code_requested_at"`
			}
			if json.Unmarshal(data, &raw) == nil && len(raw.DeviceCodeRequestedAt) > 0 && string(raw.DeviceCodeRequestedAt) != "null" {
				deviceCodeAttempted = true
				// K18/D8b/T19: LiteLLM writes device_code_requested_at with time.time() — a JSON float —
				// so decode into float64 and truncate to whole seconds; an int64 target rejects the
				// fractional literal and silently drops the field.
				var epoch float64
				if json.Unmarshal(raw.DeviceCodeRequestedAt, &epoch) == nil && epoch != 0 {
					deviceCodeRequestedAt = time.Unix(int64(epoch), 0).UTC().Format(time.RFC3339)
				}
			}
		}
	}

	st, state := readGatewayAuthState(root)
	selectedMode, _, modeErr := gatewayAuthMode(root)
	if modeErr != nil {
		selectedMode = ""
	}

	// K18/D16/T17: surface the recorded launch identity (mode, config sha, litellm version, pane pid —
	// never the reconcile-only credential_ref) when .runtime/gateway/launch.json exists, so an operator
	// can see what the running gateway was launched with. A missing or corrupt artifact tolerates to
	// omission, the same degrade-to-zero-value contract every other field on this struct keeps.
	var launchIdentity *gatewayLaunchIdentityJSON
	if data, readErr := os.ReadFile(filepath.Join(root, ".runtime", "gateway", "launch.json")); readErr == nil {
		var lj struct {
			Mode           string `json:"mode"`
			ConfigSHA256   string `json:"config_sha256"`
			LitellmVersion string `json:"litellm_version"`
			PanePID        int    `json:"pane_pid"`
		}
		if json.Unmarshal(data, &lj) == nil {
			launchIdentity = &gatewayLaunchIdentityJSON{
				Mode:           lj.Mode,
				ConfigSHA256:   lj.ConfigSHA256,
				LitellmVersion: lj.LitellmVersion,
				PanePID:        lj.PanePID,
			}
		}
	}

	if jsonOut, _ := cmd.Flags().GetBool("json"); jsonOut {
		data, err := json.Marshal(gatewayAuthStatusJSON{
			State:                 state,
			Path:                  handlePath,
			HandlePresent:         handlePresent,
			HandleValid:           handleValid,
			Mode:                  st.Mode,
			AccessExpiresAt:       handleExp,
			RefreshTokenPresent:   st.RefreshTokenPresent,
			AccountIDPresent:      st.AccountIDPresent,
			SelectedMode:          selectedMode,
			DeviceCodeRequested:   deviceCodeAttempted,
			DeviceCodeRequestedAt: deviceCodeRequestedAt,
			Launch:                launchIdentity,
		})
		if err != nil {
			return fmt.Errorf("af gateway auth status: marshaling json: %w", err)
		}
		fmt.Fprintln(cmd.OutOrStdout(), string(data))
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "state: %s\n", state)
	fmt.Fprintf(cmd.OutOrStdout(), "path: %s\n", handlePath)
	fmt.Fprintf(cmd.OutOrStdout(), "handle_present: %t\n", handlePresent)
	fmt.Fprintf(cmd.OutOrStdout(), "handle_valid: %t\n", handleValid)
	if deviceCodeAttempted {
		fmt.Fprintln(cmd.OutOrStdout(), "device_code_requested: attempted")
	}
	if state == gatewayAuthStateMissing || state == gatewayAuthStateCorrupt || state == gatewayAuthStateUnverified {
		return nil
	}
	fmt.Fprintf(cmd.OutOrStdout(), "mode: %s\n", st.Mode)
	fmt.Fprintf(cmd.OutOrStdout(), "exp: %s\n", handleExp)
	fmt.Fprintf(cmd.OutOrStdout(), "refresh_token_present: %t\n", st.RefreshTokenPresent)
	fmt.Fprintf(cmd.OutOrStdout(), "account_id_present: %t\n", st.AccountIDPresent)
	return nil
}

// gatewayTokenRefreshDo is the ADR-009 refresh-ahead seam (decisions.md D5): defined and directly
// unit-tested here, but deliberately NOT called from watchdogTick's body this phase. The spec's
// own wiring condition ("shipped iff SP-1 shows invalidation, else recorded and left dormant" —
// design-doc.md:120,321) is unmet without a Phase-0 spikes.md transcript, which this worktree does
// not carry; a future phase wires it into watchdogTick once that verdict is confirmed. The actual
// refresh HTTP call (POST grant_type=refresh_token) is out of this phase's scope for the same
// reason — the seam exists and is callable, nothing yet drives it live.
var gatewayTokenRefreshDo = func(root string, now time.Time) error {
	data, err := os.ReadFile(gatewayAuthHandlePath(root))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var handle gatewayAuthHandle
	if err := json.Unmarshal(data, &handle); err != nil {
		return err
	}
	if handle.ExpiresAt == 0 {
		return nil
	}
	if time.Unix(handle.ExpiresAt, 0).Sub(now) >= 15*time.Minute {
		return nil
	}
	return nil
}
