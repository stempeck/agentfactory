package cmd

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// --- PR #688 Phase 3 ("Bootstrap") pinning tests -----------------------------------------------
//
// These tests pin the --litellm-auth=<mode> mode-resolution ladder (issue #686 K6/K7). They are
// written RED, before setup_litellm()/parse_args() gain the new arms — see
// todos/fable-implement/red_predictions.md for the predicted failure per test.

// TestQuickstartParseArgsNamesBothAuthValuesAndAbortsOnUnknown pins AC-1/AC-3: parse_args() gains
// a `--litellm-auth=<mode>` arm that splits on `=`, enum-validates against {api-key,
// codex-subscription}, and aborts (exit 1, naming both legal values) rather than falling into the
// existing `*)` warn-and-ignore arm.
func TestQuickstartParseArgsNamesBothAuthValuesAndAbortsOnUnknown(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)
	parseArgs := extractShellFunction(content, "parse_args")
	if parseArgs == "" {
		t.Fatal("could not extract parse_args() from quickstart.sh")
	}
	if !strings.Contains(parseArgs, "--litellm-auth=") {
		t.Fatal("parse_args() has no --litellm-auth= arm yet")
	}

	// extractShellFunction only pulls function bodies — the color vars (log_error/log_warn/
	// log_info reference $RED/$YELLOW/$BLUE/$NC) and the CHECK_ONLY/WITH_LITELLM globals
	// parse_args() assigns into live at quickstart.sh's top level (lines ~45-49, 72-76), outside
	// any function, so they must be seeded here under set -u exactly like the real script seeds
	// them before parse_args ever runs.
	fns := "set -euo pipefail\n" +
		"RED='' GREEN='' YELLOW='' BLUE='' NC=''\n" +
		"CHECK_ONLY=false\nWITH_LITELLM=false\nLITELLM_AUTH_MODE=''\n" +
		extractShellFunction(content, "log_error") + "\n" +
		extractShellFunction(content, "log_warn") + "\n" +
		extractShellFunction(content, "log_info") + "\n" +
		parseArgs + "\n"

	run := func(args string) (int, string) {
		script := fns + "\nparse_args " + args + "\necho \"CHECK_ONLY=$CHECK_ONLY WITH_LITELLM=$WITH_LITELLM\"\n"
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		code := 0
		if err != nil {
			if ee, ok := err.(*exec.ExitError); ok {
				code = ee.ExitCode()
			} else {
				t.Fatalf("running parse_args: %v\n%s", err, out)
			}
		}
		return code, string(out)
	}

	for _, mode := range []string{"api-key", "codex-subscription"} {
		code, out := run("--litellm-auth=" + mode)
		if code != 0 {
			t.Errorf("--litellm-auth=%s exited %d, want 0\n%s", mode, code, out)
		}
	}

	code, out := run("--litellm-auth=bogus-value")
	if code != 1 {
		t.Fatalf("--litellm-auth=bogus-value exited %d, want 1 (abort on unknown value)\n%s", code, out)
	}
	if !strings.Contains(out, "api-key") || !strings.Contains(out, "codex-subscription") {
		t.Errorf("abort message must name BOTH legal values (api-key, codex-subscription); got: %s", out)
	}
	if strings.Contains(out, "ignoring") {
		t.Errorf("an unknown --litellm-auth value fell through to the *) warn-and-ignore arm instead of aborting: %s", out)
	}
}

// TestQuickstartRealScriptRefusesLitellmAuthWithoutLitellm pins the requires-relationship
// (decisions.md D10, corrected during blind review iteration 2): --litellm-auth REQUIRES
// --litellm. Unlike TestQuickstartParseArgsNamesBothAuthValuesAndAbortsOnUnknown, the refusal
// lives in main() (checked once parse_args returns, order-independent), not inside parse_args()
// itself, so this runs the REAL, unmodified quickstart.sh as a subprocess — the same independent-
// execution style used by the sideways check (todos/fable-implement/sideways.md) — rather than
// the function-extraction harness.
func TestQuickstartRealScriptRefusesLitellmAuthWithoutLitellm(t *testing.T) {
	root := findModuleRoot(t)
	scriptPath := filepath.Join(root, "quickstart.sh")

	out, err := exec.Command("bash", scriptPath, "--litellm-auth=codex-subscription").CombinedOutput()
	if err == nil {
		t.Fatalf("quickstart.sh --litellm-auth=codex-subscription (no --litellm) exited 0, want a refusal\n%s", out)
	}
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("running quickstart.sh: %v\n%s", err, out)
	}
	if ee.ExitCode() != 1 {
		t.Errorf("exit code = %d, want 1", ee.ExitCode())
	}
	if !strings.Contains(string(out), "litellm-auth") || !strings.Contains(string(out), "requires") {
		t.Errorf("output does not name --litellm-auth and 'requires': %s", out)
	}
	if strings.Contains(string(out), "Agentfactory Quickstart") {
		t.Error("the refusal must fire before the banner prints (immediately after parse_args, in main())")
	}
}

// apiKeyHeredocGoldenSHA is the SHA-256 (hex) of the api-key litellm.yaml seed heredoc #1 body, as
// returned by litellmSeedBlock. It guards INV-1/AC-8 byte-identity (see
// TestQuickstartApiKeySeedIsByteIdentical for the exact extraction boundary). Any edit to the
// api-key seed body changes this hash; update it only with an intentional, reviewed seed change.
const apiKeyHeredocGoldenSHA = "06d68d47315d401fbc1bb157a99688c39b1fbbb2be47d9d1ca99f5df20dfbb39"

// TestQuickstartApiKeySeedIsByteIdentical pins DO-NOT-CHANGE / the frame-lift invariant: heredoc
// #1 (the api-key seed) and the `.models.codex` jq injection must be byte-unchanged by this
// phase. This is a PROTECTIVE assertion — it is expected to PASS both before and after the
// change (see red_predictions.md).
//
// The api-key launch ENV-VAR DEREF EXPRESSIONS ("OPENAI_API_KEY=...LITELLM_MASTER_KEY=...") are
// pinned separately, inside gateway-relaunch.sh's api-key branch (TestGatewayRelaunchScriptShape
// and TestQuickstartGuardHasNoInlineLaunchLine) — not here. Decision D11 (decisions.md):
// asserting the FULL launch line (env-var derefs + trailing "litellm --config
// .agentfactory/litellm.yaml --port ") verbatim in setup_litellm()'s own text is mechanically
// incompatible with TestQuickstartGuardHasNoInlineLaunchLine's requirement that no occurrence of
// "litellm --config .agentfactory/litellm.yaml --port" remain anywhere in that same text — the
// former string is a superstring of the latter, so no implementation can satisfy both as
// originally written. The CHANGE item ("no inline litellm --config line remains anywhere") is the
// more specific, unambiguous directive; this test is narrowed to the two pieces that both remain
// literally unchanged AND coexist with that requirement.
func TestQuickstartApiKeySeedIsByteIdentical(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	wantSeedStart := `cat > ".agentfactory/litellm.yaml" << 'EOF'`
	if !strings.Contains(setup, wantSeedStart) {
		t.Fatalf("api-key seed heredoc open line changed; want to find %q verbatim", wantSeedStart)
	}

	wantJQGuard := `if ! jq -e '.models.codex' .agentfactory/models.json >/dev/null 2>&1; then`
	if !strings.Contains(setup, wantJQGuard) {
		t.Errorf("codex jq injection guard changed; want to find %q verbatim", wantJQGuard)
	}

	// F21 / INV-1 (concern_tests.md §2, red_predictions.md row 12): the two substring assertions
	// above let mutation B8a (edit any byte INSIDE the heredoc body) survive. Pin the FULL heredoc
	// #1 body by SHA-256 so any single-byte change to the api-key seed trips this test.
	//
	// Extraction boundary (deterministic, so the golden is reproducible): litellmSeedBlock returns
	// the bytes of heredoc #1 STRICTLY BETWEEN its open line
	// `cat > ".agentfactory/litellm.yaml" << 'EOF'` (exclusive) and the terminating `\nEOF`
	// (exclusive) — i.e. the body begins with the newline immediately after `'EOF'`
	// (quickstart.sh:902) and ends just before the `\nEOF` terminator (quickstart.sh:947). The
	// open line is guarded verbatim by wantSeedStart above and the terminator is a fixed token, so
	// hashing the body captures every remaining seed byte.
	body := litellmSeedBlock(t, setup)
	sum := sha256.Sum256([]byte(body))
	got := hex.EncodeToString(sum[:])
	if got != apiKeyHeredocGoldenSHA {
		t.Errorf("api-key heredoc #1 body sha256 = %s, want golden %s (INV-1 byte-identity / AC-8).\n"+
			"If this is the FIRST run after the golden was introduced, set apiKeyHeredocGoldenSHA = %q; "+
			"otherwise the api-key seed body changed and INV-1 is violated — revert the seed edit.",
			got, apiKeyHeredocGoldenSHA, got)
	}
}

// TestQuickstartSubscriptionSeedHasNoFallbacks pins the CHANGE item: heredoc #2 (the subscription
// seed) must never carry `api_key` or `fallbacks`.
func TestQuickstartSubscriptionSeedHasNoFallbacks(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	seed := subscriptionSeedBlock(t, setup)

	if strings.Contains(seed, "api_key") {
		t.Error("the subscription seed heredoc must not carry an api_key entry")
	}
	if strings.Contains(seed, "fallbacks") {
		t.Error("the subscription seed heredoc must not carry a fallbacks block")
	}
	if !strings.Contains(seed, "drop_params: true") {
		t.Error(`the subscription seed heredoc must carry "drop_params: true"`)
	}
}

// TestQuickstartSubscriptionBranchSkipsKeyLadder pins the mode-gating requirement: when mode
// resolves to codex-subscription, the api-key key ladder (file→env→TTY→refuse) must not execute.
// Scoped to the subscription branch specifically (mirroring
// TestQuickstartSubscriptionLaunchLineExportsNoOpenAIKey's branch-scoping technique) — a version
// that ran the key ladder unconditionally alongside merely referencing the handle path elsewhere
// would fail this, unlike a bare whole-function substring check.
func TestQuickstartSubscriptionBranchSkipsKeyLadder(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	const branchIf = `    if [ "$LITELLM_AUTH_MODE" = "api-key" ]; then`
	i := strings.Index(setup, branchIf)
	if i < 0 {
		t.Fatal("setup_litellm() has no api-key/subscription mode-resolution if yet")
	}
	rest := setup[i:]
	elseMarker := "\n    else\n"
	j := strings.Index(rest, elseMarker)
	if j < 0 {
		t.Fatal("could not find the subscription (else) branch of the mode-resolution if")
	}
	rest = rest[j+len(elseMarker):]
	k := strings.Index(rest, "\n    fi\n")
	if k < 0 {
		t.Fatal("could not find the end of the subscription branch")
	}
	branch := rest[:k]

	if !strings.Contains(branch, `af gateway auth import`) {
		t.Fatal("the subscription branch does not call `af gateway auth import` — the actual " +
			"ChatGPT-subscription handle acquisition (E2)")
	}
	for _, forbidden := range []string{`-t 0`, `read -rsp`, `OPENAI_API_KEY`} {
		if strings.Contains(branch, forbidden) {
			t.Errorf("the subscription branch must not run the api-key key ladder, but contains %q", forbidden)
		}
	}
}

// TestQuickstartSubscriptionLaunchLineExportsNoOpenAIKey pins the DO-NOT-CHANGE analog: in
// subscription mode, OPENAI_API_KEY must never be exported/persisted; CHATGPT_TOKEN_DIR is
// exported instead. It reads the gateway-relaunch.sh WRITER's source inside quickstart.sh (a
// shipped, always-present file) rather than the runtime-written artifact itself — the latter
// only exists after a live bootstrap, so a test gated on it would t.Skip() on every normal
// checkout/CI run and could never catch a regression.
func TestQuickstartSubscriptionLaunchLineExportsNoOpenAIKey(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	const branchStart = `elif [ "$LITELLM_AUTH_MODE" = "codex-subscription" ]; then`
	i := strings.Index(setup, branchStart)
	if i < 0 {
		t.Fatal("gateway-relaunch.sh writer has no codex-subscription launch branch yet")
	}
	rest := setup[i:]
	// The unindented "\nfi\n" closes the elif itself; the branch also contains a
	// nested, indented "    fi" (its own handle-missing guard) that must not be
	// mistaken for the outer close.
	j := strings.Index(rest, "\nfi\n")
	if j < 0 {
		t.Fatal("could not find the end of the codex-subscription launch branch")
	}
	branch := rest[:j]

	if !strings.Contains(branch, "CHATGPT_TOKEN_DIR") {
		t.Error("the codex-subscription launch branch does not export CHATGPT_TOKEN_DIR")
	}
	if strings.Contains(branch, "OPENAI_API_KEY") {
		t.Error("the codex-subscription launch branch must never export OPENAI_API_KEY")
	}
}

// TestQuickstartWritesExampleYamlWhenFileExists (retired issue af-d0bff338 / PR #694 Phase 3,
// CHANGE item 5): pinned the .codex-subscription.example save-aside fork, which this phase retires
// outright — the subscription branch no longer forks on litellm.yaml's existence at all; it seeds
// a dedicated target (litellm.codex-subscription.yaml) unconditionally-if-absent via
// _ensure_mode_yaml. There is no successor if/else pairing to check because the construct this test
// verified is retired, not relocated. Superseded in full by
// TestQuickstartModeFilesSelectedByLaunchLine (T-5, quickstart_mode_yaml_test.go) — see
// concern_tests.md §2 and consumers.md row 19 for the investigation that established this.

// TestQuickstartGuardHasNoInlineLaunchLine pins the CHANGE item: neither the tmux first-bring-up
// launch nor the login-shell relaunch guard may contain an inline `litellm --config …` command —
// both must call .agentfactory/gateway-relaunch.sh instead.
func TestQuickstartGuardHasNoInlineLaunchLine(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	inlineLaunch := `litellm --config .agentfactory/litellm.yaml --port`
	if strings.Contains(setup, inlineLaunch) {
		t.Error("setup_litellm() still contains an inline `litellm --config …` launch line; " +
			"both the tmux launch and the login-shell relaunch guard must instead call gateway-relaunch.sh")
	}
	if !strings.Contains(setup, "gateway-relaunch.sh") {
		t.Error("setup_litellm() never references gateway-relaunch.sh")
	}
}

// TestGatewayRelaunchScriptShape pins the CHANGE item: a NEW .agentfactory/gateway-relaunch.sh
// writer, modeled on the telemetry relaunch.sh writer, but mode 0700 (not 0755) since it may
// embed the ability to export secrets.
func TestGatewayRelaunchScriptShape(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	if !strings.Contains(setup, `.agentfactory/gateway-relaunch.sh`) {
		t.Fatal("setup_litellm() never writes .agentfactory/gateway-relaunch.sh")
	}
	if !strings.Contains(setup, "chmod 0700") {
		t.Error("the gateway-relaunch.sh writer must chmod the script 0700 (not 0755 — it may embed secret-exporting logic)")
	}
	// D11 (decisions.md): the api-key launch env-var deref expressions must survive the move into
	// gateway-relaunch.sh byte-identical — this is the DO-NOT-CHANGE half of TestQuickstartApiKeySeedIsByteIdentical's
	// original (now-narrowed) scope, relocated to where the expressions actually live post-move.
	wantKeyDeref := `OPENAI_API_KEY="$(cat .agentfactory/secrets/openai.key)"`
	if !strings.Contains(setup, wantKeyDeref) {
		t.Errorf("gateway-relaunch.sh does not carry the api-key deref expression %q verbatim", wantKeyDeref)
	}
	wantMasterKeyDeref := `LITELLM_MASTER_KEY="$(cat .agentfactory/secrets/litellm.key)"`
	if !strings.Contains(setup, wantMasterKeyDeref) {
		t.Errorf("gateway-relaunch.sh does not carry the master-key deref expression %q verbatim", wantMasterKeyDeref)
	}
	if !strings.Contains(setup, "umask 077") {
		t.Error("gateway-relaunch.sh's own body must set umask 077")
	}
	if !strings.Contains(setup, "CHATGPT_DEFAULT_INSTRUCTIONS") {
		t.Error("gateway-relaunch.sh must set a non-empty CHATGPT_DEFAULT_INSTRUCTIONS")
	}
	// D12 (decisions.md): the api-key branch's launch line is forced to `--port … --config …`
	// (not the historical `--config … --port …`) by a mechanical collision with
	// TestQuickstartGuardHasNoInlineLaunchLine's forbidden-substring check. Pin the resulting
	// exact argument order so a further accidental change (e.g. dropping --port) is caught here
	// instead of silently drifting unguarded.
	wantLaunchLine := `litellm --port '"$LITELLM_PORT"' --config .agentfactory/litellm.yaml`
	if !strings.Contains(setup, wantLaunchLine) {
		t.Errorf("gateway-relaunch.sh's api-key launch line does not match the pinned order %q verbatim", wantLaunchLine)
	}

	// F29 (concern_tests.md §3, red_predictions.md row 13): mutation B8c (delete any one of the five
	// `tmux set-environment -g -u <NAME>` unsets) survived because no assertion pinned them. Pin all
	// five so a dropped unset — which would leak a gateway upstream-auth var into the tmux global env
	// (INV-1 mode-invariance) — trips. These are the canonical five gateway upstream-auth vars
	// (quickstart.sh:1111-1115). PASSES at head (all five present); RED is proven by the mutation.
	for _, k := range []string{"OPENAI_API_KEY", "CHATGPT_TOKEN_DIR", "CHATGPT_AUTH_FILE", "CHATGPT_API_BASE", "CODEX_HOME"} {
		wantUnset := "tmux set-environment -g -u " + k
		if !strings.Contains(setup, wantUnset) {
			t.Errorf("gateway-relaunch.sh must clear %s from the tmux global env; missing the line %q", k, wantUnset)
		}
	}

	// K10 (PR #694 Phase 4, design-doc.md:124/175): the writer gains a `--identity` mode printing
	// the seven-field identity JSON, and writes launch.json (identity fields + launched_at +
	// pane_pid) under .runtime/gateway/ after a successful tmux new-session.
	const identityBranch = `"$1" = "--identity"`
	idIdx := strings.Index(setup, identityBranch)
	if idIdx < 0 {
		t.Fatal("gateway-relaunch.sh writer never branches on `--identity` (K10-b not implemented)")
	}
	// Isolate the --identity branch's own text so the "no secret material" check below is scoped
	// to it specifically, not the whole writer body (branch-isolation-by-slicing idiom, matching
	// TestQuickstartSubscriptionBranchSkipsKeyLadder).
	branchRest := setup[idIdx:]
	branchEnd := strings.Index(branchRest, "\nfi\n")
	if branchEnd < 0 {
		t.Fatal("could not find the end (`fi` on its own line) of the --identity branch")
	}
	identityBlock := branchRest[:branchEnd]

	for _, field := range []string{`"v"`, `"mode"`, `"config_sha256"`, `"litellm_version"`, `"credential_ref"`, `"port"`, `"factory_root"`} {
		if !strings.Contains(identityBlock, field) {
			t.Errorf("the --identity branch does not print the field %s; block:\n%s", field, identityBlock)
		}
	}
	if strings.Contains(identityBlock, "cat ") && (strings.Contains(identityBlock, "secrets/openai.key") || strings.Contains(identityBlock, "secrets/litellm.key")) {
		t.Errorf("the --identity branch appears to dereference a secret file directly — it must print "+
			"references only, never secret content. Block:\n%s", identityBlock)
	}

	if !strings.Contains(setup, "gateway/launch.json") {
		t.Error("gateway-relaunch.sh writer never references .runtime/gateway/launch.json (K10-c not implemented)")
	}
	if !strings.Contains(setup, "pane_pid") {
		t.Error("gateway-relaunch.sh writer never captures pane_pid (K10-c: launch.json must include the pane pid via `tmux list-panes … -F '#{pane_pid}'`)")
	}
	if !strings.Contains(setup, "launched_at") {
		t.Error("gateway-relaunch.sh writer never stamps launched_at (K10-c)")
	}
	if !strings.Contains(setup, "umask 077") {
		t.Error("launch.json write must happen under `umask 077` so it lands 0600 (K10-c)")
	}
}

// TestQuickstartSmokeIsCheckLive pins the CHANGE item: the curl-based readiness poll is replaced
// by `af config models check "$profile" --live`, with $profile parametrized to codex or
// codex-subscription.
func TestQuickstartSmokeIsCheckLive(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	if !strings.Contains(setup, `models check "$profile" --live`) {
		t.Error(`setup_litellm() does not call ` + "`af config models check \"$profile\" --live`" +
			` with a parametrized profile variable`)
	}
	if strings.Contains(setup, `af config models check codex`+"\n") || strings.Contains(setup, `af config models check codex `) {
		t.Error("the old hard-coded, non-live `af config models check codex` smoke is still present")
	}
}

// TestQuickstartEnforcesLitellmPin pins the CHANGE item: litellm is reinstalled when
// `litellm --version` differs from $LITELLM_VERSION, not only when the binary is absent.
func TestQuickstartEnforcesLitellmPin(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	setup := extractShellFunction(string(data), "setup_litellm")
	if setup == "" {
		t.Fatal("could not extract setup_litellm()")
	}
	// Today's guard only fires on absence: `if ! command_exists litellm; then`. The pin-enforcement
	// requirement is that the SAME guard also fires on a version mismatch, e.g.
	// `if ! command_exists litellm || [[ "$(litellm --version ...)" != *"$LITELLM_VERSION"* ]]; then`.
	// Asserting only "litellm --version" appears somewhere is too weak — that substring already
	// exists today in an unrelated log_success message and would make this test a false pass.
	installGuard := `if ! command_exists litellm`
	i := strings.Index(setup, installGuard)
	if i < 0 {
		t.Fatal("could not locate the litellm install guard in setup_litellm()")
	}
	guardLine := setup[i:]
	if nl := strings.Index(guardLine, "\n"); nl >= 0 {
		guardLine = guardLine[:nl]
	}
	if !strings.Contains(guardLine, "LITELLM_VERSION") || !strings.Contains(guardLine, "litellm --version") {
		t.Errorf("the litellm install guard condition still only checks absence, not version mismatch "+
			"against $LITELLM_VERSION; guard line: %q", guardLine)
	}
}

// TestQuickstartSubscriptionSeedRoutesEveryLaneToChatgptResponses pins the CHANGE item: every
// model_list lane in the subscription seed routes to a chatgpt/responses/<registry id> backend.
func TestQuickstartSubscriptionSeedRoutesEveryLaneToChatgptResponses(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)
	seed := subscriptionSeedBlock(t, setup)
	entries := parseLitellmSeedEntries(t, seed)

	for _, e := range entries {
		if !strings.HasPrefix(e.backend, "chatgpt/responses/") {
			t.Errorf("subscription seed entry %q routes to %q, want a chatgpt/responses/<registry id> backend",
				e.name, e.backend)
		}
	}
}

// TestQuickstartShowHelpMentionsLitellmAuthFlag pins the protective CHANGE item: show_help()
// gains a line documenting the new --litellm-auth flag.
func TestQuickstartShowHelpMentionsLitellmAuthFlag(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	help := extractShellFunction(string(data), "show_help")
	if help == "" {
		t.Fatal("could not extract show_help() from quickstart.sh")
	}
	if !strings.Contains(help, "--litellm-auth") {
		t.Error("show_help() does not document the --litellm-auth flag")
	}
}

// TestQuickstartShowHelpMentionsCodexInstallConsent pins the named CHANGE item ("show_help: add a
// line documenting AF_CODEX_INSTALL_CONSENT", intake.md) that Phase 3's Test Strategist found had
// zero coverage (concern_tests.md §7, decisions.md D7). The line already exists on HEAD
// (quickstart.sh:781-784) — this closes the coverage gap, it does not change behavior.
func TestQuickstartShowHelpMentionsCodexInstallConsent(t *testing.T) {
	content := quickstartScriptContent(t)
	help := extractShellFunction(content, "show_help")
	if help == "" {
		t.Fatal("could not extract show_help() from quickstart.sh")
	}
	if !strings.Contains(help, "AF_CODEX_INSTALL_CONSENT") {
		t.Error("show_help() does not document AF_CODEX_INSTALL_CONSENT")
	}
}

// TestQuickstartInstallsCodexCliOnlyWhenAbsent pins the protective CHANGE item, STRENGTHENED for
// Phase 2b (K7, issue af-6a25a83c, concern_tests.md §3.2): the codex CLI install must live inside
// a top-level `_ensure_codex_cli` function (not a bare inline guard, since that function's own
// internal branch structure would make a naive backward "nearest if" scan fragile/wrong post-K7),
// gated by BOTH an absence check and a consent gate (env var + interactive read), and its install
// line must be hardened (`sudo -n`, not bare `sudo `). Also pins the DO-NOT-CHANGE placement item:
// `_ensure_codex_cli` must be top-level, beside `install_playwright`, not nested inside
// `setup_litellm`.
func TestQuickstartInstallsCodexCliOnlyWhenAbsent(t *testing.T) {
	content := quickstartScriptContent(t)

	fn := extractShellFunction(content, "_ensure_codex_cli")
	if fn == "" {
		t.Fatal("could not extract _ensure_codex_cli() from quickstart.sh")
	}
	if !strings.Contains(fn, "command -v codex") && !strings.Contains(fn, "command_exists codex") {
		t.Errorf("_ensure_codex_cli() does not check absence of the codex binary; body:\n%s", fn)
	}
	if !strings.Contains(fn, "AF_CODEX_INSTALL_CONSENT") {
		t.Error("_ensure_codex_cli() does not reference AF_CODEX_INSTALL_CONSENT")
	}
	if !strings.Contains(fn, "read -r -p") {
		t.Error("_ensure_codex_cli() does not have an interactive `read -r -p` consent prompt")
	}
	if !strings.Contains(fn, "sudo -n npm install -g @openai/codex") {
		t.Errorf("_ensure_codex_cli() has no `sudo -n npm install -g @openai/codex` line (the sudo -n branch of the "+
			"install_playwright-modeled writable-vs-sudo probe); body:\n%s", fn)
	}
	if !strings.Contains(fn, "npm install -g @openai/codex") {
		t.Fatal("_ensure_codex_cli() does not install the codex CLI (`npm install -g @openai/codex`)")
	}

	setup := extractShellFunction(content, "setup_litellm")
	if setup != "" && strings.Contains(setup, "_ensure_codex_cli() {") {
		t.Error("_ensure_codex_cli is defined nested inside setup_litellm(); it must be top-level, beside install_playwright")
	}
}

// TestQuickstartCodexRerunDeadEndRemoved pins AC-2 (concern_tests.md §1): the old subscription-
// branch dead-end message ("...then rerun") must be fully removed once K7/K8 replace the inline
// install/login block with `_ensure_codex_cli`/`_ensure_codex_session`. Kept as an independent
// top-level test (not nested under T-1) so a T-1 failure never masks this one.
func TestQuickstartCodexRerunDeadEndRemoved(t *testing.T) {
	content := quickstartScriptContent(t)
	if n := strings.Count(content, "then rerun"); n != 0 {
		t.Errorf(`quickstart.sh still contains %d occurrence(s) of "then rerun" (AC-2 requires 0)`, n)
	}
}

// subscriptionSeedBlock returns the body of the subscription litellm.yaml seed. Post-F14 (PR #688)
// the body has a SINGLE source — one heredoc captured into a shell variable and written to BOTH the
// live litellm.yaml and the .codex-subscription.example path — so this helper locates that heredoc
// by its SUBEOF delimiter (kept distinct from the api-key seed's 'EOF' so the two never collide)
// rather than by a second `cat > ".agentfactory/litellm.yaml"` redirect, which the dedup removes.
func subscriptionSeedBlock(t *testing.T, setup string) string {
	t.Helper()
	const open = "<< 'SUBEOF'"
	i := strings.Index(setup, open)
	if i < 0 {
		t.Fatal("no subscription seed heredoc (distinct 'SUBEOF' delimiter) found in setup_litellm(); " +
			"the single-source subscription body does not exist yet")
	}
	rest := setup[i+len(open):]
	nl := strings.Index(rest, "\n")
	if nl < 0 {
		t.Fatal("malformed subscription seed heredoc open line")
	}
	body := rest[nl+1:]
	const term = "\nSUBEOF"
	j := strings.Index(body, term)
	if j < 0 {
		t.Fatal("unterminated subscription seed heredoc (looking for delimiter SUBEOF)")
	}
	return body[:j]
}

// TestQuickstartSubscriptionSeedSmallIdIsRegistryLuna pins F8 (concern_tests.md §1 F8,
// red_predictions.md row 18): the small/haiku backend id in the codex-subscription path must be the
// registry id `gpt-5.6-luna` — the id the tracked litellm.yaml/USING_LITELLM.md use — and the
// placeholder `gpt-5.6-sol-mini` must appear NOWHERE in quickstart.sh. RED at head: sol-mini is
// present (quickstart.sh:966,968,986,1001,1003,1021,1060) and luna is absent.
func TestQuickstartSubscriptionSeedSmallIdIsRegistryLuna(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	if strings.Contains(content, "gpt-5.6-sol-mini") {
		t.Error("quickstart.sh still references the placeholder small id `gpt-5.6-sol-mini`; every " +
			"subscription small/haiku lane must use the registry id `gpt-5.6-luna`")
	}
	// The subscription seed's small model lane (the gpt-5.6 small lane and the claude-haiku alias
	// both route to the small backend) must route to the luna registry id.
	if !strings.Contains(content, "chatgpt/responses/gpt-5.6-luna") {
		t.Error("the subscription seed's small lane does not route to `chatgpt/responses/gpt-5.6-luna`")
	}
	// The codex-subscription profile's haiku row (the jq seed at quickstart.sh:1060) must use luna.
	if !strings.Contains(content, `"ANTHROPIC_DEFAULT_HAIKU_MODEL": "gpt-5.6-luna"`) {
		t.Error(`the codex-subscription profile seed's ANTHROPIC_DEFAULT_HAIKU_MODEL row does not use "gpt-5.6-luna"`)
	}
}

// TestQuickstartLitellmAuthTextsAgree pins F13 (concern_tests.md §1 F13, red_predictions.md row 23):
// show_help()'s --litellm-auth line must agree with main()'s refusal (quickstart.sh:1626,
// `--litellm-auth requires --litellm`) — it must say the flag "requires" --litellm, never "implies"
// it — and USING_LITELLM.md must document the AF_LITELLM_AUTH env var. RED at head: show_help says
// "implies --litellm" (quickstart.sh:687) and USING_LITELLM.md never mentions AF_LITELLM_AUTH.
func TestQuickstartLitellmAuthTextsAgree(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	// Ground the required verb in the actual main() refusal so "same verb" is not a guess.
	const refusal = "--litellm-auth requires --litellm"
	if !strings.Contains(content, refusal) {
		t.Fatalf("main()'s refusal no longer reads %q — the verb this test aligns show_help against has moved", refusal)
	}

	help := extractShellFunction(content, "show_help")
	if help == "" {
		t.Fatal("could not extract show_help() from quickstart.sh")
	}
	if strings.Contains(help, "implies") {
		t.Error("show_help() says the --litellm-auth flag `implies` --litellm, contradicting main()'s " +
			"refusal `--litellm-auth requires --litellm` (quickstart.sh:1626); the help must use the same verb")
	}
	if !strings.Contains(help, "requires") {
		t.Error("show_help()'s --litellm-auth line does not use the refusal's `requires` verb")
	}

	t.Run("USING_LITELLM.md documents AF_LITELLM_AUTH", func(t *testing.T) {
		doc := readUsingLitellmDoc(t)
		if !strings.Contains(doc, "AF_LITELLM_AUTH") {
			t.Error("USING_LITELLM.md never mentions the AF_LITELLM_AUTH env var; the subscription-mode " +
				"docs must document it")
		}
	})
}

// TestQuickstartSubscriptionYamlBodyDefinedOnce pins F14 (concern_tests.md §1 F14, red_predictions.md
// row 22): the ~30-line codex-subscription litellm.yaml body is duplicated across two heredocs today
// (the live-file write and the .codex-subscription.example write, quickstart.sh:961-1028). After the
// fix the body has ONE source (a variable written to both paths), so a distinctive subscription-body
// line must appear at most once. RED at head: the anchor appears twice.
func TestQuickstartSubscriptionYamlBodyDefinedOnce(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	// A line unique to the subscription seed body (absent from the api-key heredoc #1) and
	// unaffected by the F8 sol-mini->luna id swap, so this test is independent of F8.
	const anchor = "substitute the Codex/ChatGPT model id you want agents on"
	n := strings.Count(setup, anchor)
	if n == 0 {
		t.Fatalf("distinctive subscription-body anchor %q not found in setup_litellm(); the anchor this "+
			"test counts has moved or been reworded", anchor)
	}
	if n > 1 {
		t.Errorf("the subscription yaml body appears %d times (anchor %q); it must have a single source "+
			"written to both the live file and the .codex-subscription.example path (want <=1)", n, anchor)
	}
}

// TestQuickstartVerifiesSessionBeforeLoggingLaunched pins F18 (concern_tests.md §3 F18,
// red_predictions.md row 21): the first-bring-up runs "$relaunch_script" then logs "Launched
// LiteLLM ..." UNCONDITIONALLY (quickstart.sh:1119-1122), even though gateway-relaunch.sh exits 0
// without launching when a handle/key is missing. The log must be gated on a `tmux has-session -t
// litellm` recheck performed AFTER the relaunch invocation. RED at head: nothing sits between the
// invocation and the log.
func TestQuickstartVerifiesSessionBeforeLoggingLaunched(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	const launchedLog = "Launched LiteLLM"
	logIdx := strings.Index(setup, launchedLog)
	if logIdx < 0 {
		t.Fatalf("setup_litellm() no longer logs %q", launchedLog)
	}
	before := setup[:logIdx]

	const invocation = `"$relaunch_script"`
	invIdx := strings.LastIndex(before, invocation)
	if invIdx < 0 {
		t.Fatalf("could not find the gateway-relaunch.sh invocation (%s) before the %q log", invocation, launchedLog)
	}
	between := before[invIdx+len(invocation):]

	if !strings.Contains(between, "tmux has-session -t =litellm") {
		t.Errorf("the %q log is not gated on a `tmux has-session -t =litellm` recheck after running the "+
			"relaunch script — gateway-relaunch.sh exits 0 without launching when a handle/key is missing, "+
			"so the log fires even when nothing launched.\nBetween the relaunch invocation and the log: %q",
			launchedLog, between)
	}
}

// TestQuickstartParseArgsHandlesBareLitellmAuthForm pins F25 (concern_tests.md §1 F25,
// red_predictions.md row 19): today only the `--litellm-auth=<mode>` (equals) form is parsed
// (quickstart.sh:715); the SPACE form `--litellm-auth codex-subscription` falls through to the `*)`
// warn-and-ignore arm, silently leaving the mode empty. The bare form must EITHER be consumed
// (rc 0, mode set) OR abort (rc 1, naming the value) — never rc 0 with an empty mode / an "ignoring"
// warning. RED at head: rc 0, empty mode, "ignoring".
//
// Reuses the parse_args extraction harness of
// TestQuickstartParseArgsNamesBothAuthValuesAndAbortsOnUnknown: the CHECK_ONLY/WITH_LITELLM/
// LITELLM_AUTH_MODE globals and the color vars parse_args reads live at quickstart.sh's top level
// (outside any function), so they are seeded here under `set -u` exactly as the real script seeds
// them before parse_args runs.
func TestQuickstartParseArgsHandlesBareLitellmAuthForm(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)
	parseArgs := extractShellFunction(content, "parse_args")
	if parseArgs == "" {
		t.Fatal("could not extract parse_args() from quickstart.sh")
	}

	fns := "set -euo pipefail\n" +
		"RED='' GREEN='' YELLOW='' BLUE='' NC=''\n" +
		"CHECK_ONLY=false\nWITH_LITELLM=false\nLITELLM_AUTH_MODE=''\n" +
		extractShellFunction(content, "log_error") + "\n" +
		extractShellFunction(content, "log_warn") + "\n" +
		extractShellFunction(content, "log_info") + "\n" +
		parseArgs + "\n"

	script := fns + "\nparse_args --litellm-auth codex-subscription\n" +
		"echo \"LITELLM_AUTH_MODE=$LITELLM_AUTH_MODE\"\n"
	out, err := exec.Command("bash", "-c", script).CombinedOutput()
	code := 0
	if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else {
			t.Fatalf("running parse_args: %v\n%s", err, out)
		}
	}
	output := string(out)

	consumed := code == 0 && strings.Contains(output, "LITELLM_AUTH_MODE=codex-subscription")
	abortedNamingValue := code == 1 && strings.Contains(output, "codex-subscription")
	if !consumed && !abortedNamingValue {
		t.Errorf("bare `--litellm-auth codex-subscription` must be CONSUMED (rc 0, "+
			"LITELLM_AUTH_MODE=codex-subscription) or ABORTED (rc 1, naming the value); got rc=%d, output:\n%s",
			code, output)
	}
	if strings.Contains(output, "ignoring") {
		t.Errorf("bare `--litellm-auth codex-subscription` fell through to the *) warn-and-ignore arm "+
			"(only the --litellm-auth=* form is handled); output:\n%s", output)
	}
}

// TestQuickstartSubscriptionSeedCarriesThrottle pins F28 (concern_tests.md §1 F28, red_predictions.md
// row 20, decision D4=seed): the codex-subscription models.json jq seed (quickstart.sh:1055-1065)
// must carry AF_DISABLE_PARALLEL_SUBAGENTS — the shared-plan rate-limit throttle (INV-6/D13) — which
// appears nowhere in quickstart.sh today. RED at head: the seed object lacks the key.
func TestQuickstartSubscriptionSeedCarriesThrottle(t *testing.T) {
	root := findModuleRoot(t)
	setup := setupLitellmSource(t, root)

	// Isolate the codex-subscription jq seed OBJECT: from `.models["codex-subscription"] = {` (the
	// assignment — distinct from the `jq -e '.models["codex-subscription"]'` existence guard, which
	// carries no ` = {`) through the first `}'` that closes the jq object.
	const seedMarker = `.models["codex-subscription"] = {`
	i := strings.Index(setup, seedMarker)
	if i < 0 {
		t.Fatalf("could not locate the codex-subscription jq seed assignment %q in setup_litellm()", seedMarker)
	}
	rest := setup[i:]
	end := strings.Index(rest, "}'")
	if end < 0 {
		t.Fatal("could not find the end of the codex-subscription jq seed object (expected a closing `}'`)")
	}
	seedBlock := rest[:end]

	if !strings.Contains(seedBlock, "AF_DISABLE_PARALLEL_SUBAGENTS") {
		t.Errorf("the codex-subscription profile seed does not carry AF_DISABLE_PARALLEL_SUBAGENTS "+
			"(the shared-plan throttle, decisions D4/D13); seed object:\n%s", seedBlock)
	}
}

// TestQuickstartLitellmPinValue pins F30 (concern_tests.md §3 F30, red_predictions.md row 14): the
// LITELLM_VERSION pin must be the exact reviewed value, asserted by absolute value rather than shape,
// so a bump is always a deliberate reviewed change. PASSES at head (quickstart.sh:48); RED is proven
// by mutation B8d (pin -> a different value). Complements the shape-only TestQuickstartEnforcesLitellmPin
// and the quickstart<->doc parity cross-check in TestUsingLitellmDocContract.
func TestQuickstartLitellmPinValue(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	const wantPin = `LITELLM_VERSION="1.93.0"`
	if !strings.Contains(string(data), wantPin) {
		t.Errorf("quickstart.sh must pin %s verbatim; not found — a version change must be a deliberate, "+
			"reviewed edit", wantPin)
	}
}

// --- Phase 2b (K7/K8, issue af-6a25a83c) pinning tests -----------------------------------------
//
// These pin _ensure_codex_cli's consent gate + hardened install, and _ensure_codex_session/
// _codex_session_valid's login-skip-when-valid behavior. Written RED (concern_tests.md §0: grep
// for _ensure_codex_cli/_codex_session_valid/_ensure_codex_session/CODEX_LOGIN_TIMEOUT in
// quickstart.sh and internal/cmd/*.go returns zero hits today) — see
// todos/fable-implement/red_predictions.md for the predicted failure per test.

// codexFnsHarness extracts _ensure_codex_cli, _ensure_codex_session, _codex_session_valid and
// their shared dependencies from the real quickstart.sh, plus the CODEX_LOGIN_TIMEOUT global these
// functions read (seeded here exactly as parse_args's harness seeds CHECK_ONLY/WITH_LITELLM/
// LITELLM_AUTH_MODE, since extractShellFunction only pulls function bodies, never top-level var
// assignments).
func codexFnsHarness(t *testing.T, content string) string {
	t.Helper()
	names := []string{"command_exists", "log_info", "log_success", "log_warn", "log_error",
		"log_step", "_ensure_codex_cli", "_ensure_codex_session", "_run_with_timeout", "_codex_session_valid"}
	var b strings.Builder
	b.WriteString("set -uo pipefail\n")
	b.WriteString("RED='' GREEN='' YELLOW='' BLUE='' NC=''\n")
	b.WriteString("CODEX_LOGIN_TIMEOUT=900\n")
	for _, n := range names {
		fn := extractShellFunction(content, n)
		if fn == "" {
			t.Fatalf("could not extract %s() from quickstart.sh", n)
		}
		b.WriteString(fn)
		b.WriteString("\n")
	}
	return b.String()
}

// writeCodexStub writes a 0o755 shell-script stub into dir/name, mirroring install_test.go:275-315's
// PATH-stub idiom.
func writeCodexStub(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatalf("writing %s stub: %v", name, err)
	}
}

// hermeticCodexBinDir creates a fresh t.TempDir() and symlinks the real `grep`/`sleep`/`tail`/
// `jq` into it — the coreutils the extracted functions call alongside `npm`/`sudo`/`codex` (`jq`
// is what the fixed `_codex_session_valid` reads `auth.json`'s `auth_mode`/`.tokens.refresh_token`
// with — decisions.md D1). The
// resulting dir is meant to be the SOLE PATH entry (see hermeticCodexEnv): a merely-prefixed real
// PATH would let a `codex`/`npm`/`sudo` binary that happens to already be installed on the host
// (observed in at least one sandboxed dev environment) leak through and silently short-circuit
// the very absence/decline paths T-2/T-3 exist to exercise — an exclusive PATH consisting only of
// this dir is what makes "codex is genuinely absent unless stubbed" an actual guarantee rather
// than a hope about the host's installed tooling.
func hermeticCodexBinDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tool := range []string{"grep", "sleep", "tail", "jq"} {
		real, err := exec.LookPath(tool)
		if err != nil {
			t.Fatalf("resolving real %s for the hermetic stub bin dir: %v", tool, err)
		}
		if err := os.Symlink(real, filepath.Join(dir, tool)); err != nil {
			t.Fatalf("symlinking %s into the hermetic stub bin dir: %v", tool, err)
		}
	}
	return dir
}

// hermeticCodexEnv builds a subprocess env whose PATH is EXCLUSIVELY the hermetic stub bin dir —
// glibc's getenv resolves the FIRST matching PATH= entry, so appending a PATH override after
// os.Environ() (which already carries a real PATH) would silently leave the real PATH in effect.
// Filtering out every existing PATH= entry before setting the stub-only override closes that gap.
func hermeticCodexEnv(bin string, extra ...string) []string {
	env := []string{"PATH=" + bin}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

// TestQuickstartCodexConsentDeclineInstallsNothing is T-2 (concern_tests.md §1.2): every decline
// path (stdin "n", empty stdin, EOF stdin, or an invalid AF_CODEX_INSTALL_CONSENT value) must exit
// 1, name the reason in the log output, and never touch npm or sudo. Checked against COMBINED
// output, not stderr alone: every log_* helper in quickstart.sh (including log_error) writes via
// plain `echo`, i.e. to stdout, matching the file's existing convention throughout — not a
// stderr/stdout split this phase introduces or changes.
func TestQuickstartCodexConsentDeclineInstallsNothing(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := codexFnsHarness(t, content)

	cases := []struct {
		name       string
		stdin      string
		env        string
		wantStderr string
	}{
		{"stdin-n", "n\n", "", "declined"},
		{"stdin-empty", "", "", "declined"},
		{"stdin-eof", "", "", "declined"},
		{"env-maybe", "n\n", "maybe", "AF_CODEX_INSTALL_CONSENT"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := hermeticCodexBinDir(t)
			npmLog := filepath.Join(t.TempDir(), "npm.log")
			sudoLog := filepath.Join(t.TempDir(), "sudo.log")
			writeCodexStub(t, bin, "npm", `printf '%s\n' "$*" >> "$NPM_LOG"
if [ "$1" = root ] && [ "$2" = -g ]; then
  exit 1
fi
exit 0
`)
			writeCodexStub(t, bin, "sudo", `printf '%s\n' "$*" >> "$SUDO_LOG"
exit 0
`)

			script := harness + "\n_ensure_codex_cli\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = hermeticCodexEnv(bin, "NPM_LOG="+npmLog, "SUDO_LOG="+sudoLog)
			if tc.env != "" {
				cmd.Env = append(cmd.Env, "AF_CODEX_INSTALL_CONSENT="+tc.env)
			}
			if tc.name == "stdin-eof" {
				cmd.Stdin = nil
			} else {
				cmd.Stdin = strings.NewReader(tc.stdin)
			}
			var errBuf bytes.Buffer
			cmd.Stderr = &errBuf
			err := cmd.Run()

			ee, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("_ensure_codex_cli did not exit with a non-zero ExitError: %v\nstderr:\n%s", err, errBuf.String())
			}
			if ee.ExitCode() != 1 {
				t.Errorf("exit code = %d, want 1", ee.ExitCode())
			}
			if !strings.Contains(errBuf.String(), tc.wantStderr) {
				t.Errorf("stderr does not contain %q; got: %s", tc.wantStderr, errBuf.String())
			}
			for _, log := range []string{npmLog, sudoLog} {
				if data, statErr := os.ReadFile(log); statErr == nil && len(data) > 0 {
					t.Errorf("%s is non-empty on a decline path; decline must short-circuit before any probe/install call. Contents: %s", log, data)
				}
			}
		})
	}

	// The TTY-interactive-decline message (finding F, decisions.md D4) has no verbatim spec
	// string to match (ux.md U3 never enumerates this exact path × TTY-present cell), so D4
	// keeps the current wording but requires it follow the file's own established convention:
	// 44/45 other log_error calls end with no terminal punctuation; this was the sole outlier.
	t.Run("TTY decline message has no trailing period (static)", func(t *testing.T) {
		fn := extractShellFunction(content, "_ensure_codex_cli")
		if fn == "" {
			t.Fatal("could not extract _ensure_codex_cli() from quickstart.sh")
		}
		re := regexp.MustCompile(`log_error "codex CLI install declined — subscription mode cannot run without the Codex CLI; nothing was installed(\.?)"`)
		m := re.FindStringSubmatch(fn)
		if m == nil {
			t.Fatalf("could not find the TTY-decline log_error literal in _ensure_codex_cli(); body:\n%s", fn)
		}
		if m[1] == "." {
			t.Error("TTY-decline message ends with a trailing period, inconsistent with the file's other 44 log_error calls (finding F2); drop it")
		}
	})
}

// TestQuickstartCodexConsentYesInstallsWithSudoN is T-3 (concern_tests.md §1.2): a pre-granted
// AF_CODEX_INSTALL_CONSENT=yes (which bypasses the TTY prompt entirely — env precedes TTY) must
// exit 0 and install via `sudo -n npm install -g @openai/codex`.
//
// A live "y"/"Y" TTY-consent sub-case is NOT exercised here: `exec.Command`'s stdin is always a
// pipe in this harness (no pty library exists in this module — D8, concern_tests.md §0), so
// `[ -t 0 ]` is unconditionally false regardless of piped content, and any interactive-TTY
// sub-case would exercise the SAME no-terminal decline branch as T-2, never the TTY-read branch.
// The "only y/Y consents" DO-NOT-CHANGE item is instead pinned as a static source assertion below
// (mirroring T-4's CODEX_LOGIN_TIMEOUT static companion check) — the real-TTY interactive path is
// exercised by hand at the human demo gate, not by this unit test.
func TestQuickstartCodexConsentYesInstallsWithSudoN(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := codexFnsHarness(t, content)

	t.Run("env-yes", func(t *testing.T) {
		bin := hermeticCodexBinDir(t)
		npmLog := filepath.Join(t.TempDir(), "npm.log")
		sudoLog := filepath.Join(t.TempDir(), "sudo.log")
		writeCodexStub(t, bin, "npm", `printf '%s\n' "$*" >> "$NPM_LOG"
if [ "$1" = root ] && [ "$2" = -g ]; then
  exit 1
fi
exit 0
`)
		writeCodexStub(t, bin, "sudo", `printf '%s\n' "$*" >> "$SUDO_LOG"
exit 0
`)

		script := harness + "\n_ensure_codex_cli\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = hermeticCodexEnv(bin, "NPM_LOG="+npmLog, "SUDO_LOG="+sudoLog, "AF_CODEX_INSTALL_CONSENT=yes")
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		if err := cmd.Run(); err != nil {
			t.Fatalf("_ensure_codex_cli failed: %v\nstdout:\n%s\nstderr:\n%s", err, outBuf.String(), errBuf.String())
		}
		sudoData, err := os.ReadFile(sudoLog)
		if err != nil {
			t.Fatalf("reading sudo log: %v", err)
		}
		if !strings.Contains(string(sudoData), "-n npm install -g @openai/codex") {
			t.Errorf("sudo log does not contain a `-n npm install -g @openai/codex` call; got: %s", sudoData)
		}
	})

	// No-sudo-n-and-prefix-not-writable is G/G2 (concern_decisions.md, decisions.md D3): before
	// this phase's fix the refusal here was a paraphrase of ux.md U3 row 4, and the branch had
	// zero test coverage at all. Pins the verbatim spec text, an exit 1, and that neither npm
	// install nor a sudo install call ever fires on this branch.
	t.Run("no-sudo-n-and-prefix-not-writable", func(t *testing.T) {
		bin := hermeticCodexBinDir(t)
		npmLog := filepath.Join(t.TempDir(), "npm.log")
		sudoLog := filepath.Join(t.TempDir(), "sudo.log")
		writeCodexStub(t, bin, "npm", `printf '%s\n' "$*" >> "$NPM_LOG"
if [ "$1" = root ] && [ "$2" = -g ]; then
  exit 1
fi
exit 0
`)
		writeCodexStub(t, bin, "sudo", `printf '%s\n' "$*" >> "$SUDO_LOG"
if [ "$1" = -n ] && [ "$2" = true ]; then
  exit 1
fi
exit 0
`)

		script := harness + "\n_ensure_codex_cli\n"
		cmd := exec.Command("bash", "-c", script)
		cmd.Env = hermeticCodexEnv(bin, "NPM_LOG="+npmLog, "SUDO_LOG="+sudoLog, "AF_CODEX_INSTALL_CONSENT=yes")
		// Unlike the consent-decline log_error calls (:513, :517), this refusal has no `>&2`
		// redirect — log_error itself always echoes to stdout (:91-93); the decline paths are
		// the deliberate exception (AC-1 requires "stderr contains 'declined'"), not the rule.
		// Capture combined output to match that established convention.
		var outErrBuf bytes.Buffer
		cmd.Stdout = &outErrBuf
		cmd.Stderr = &outErrBuf
		err := cmd.Run()
		ee, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("_ensure_codex_cli did not exit with a non-zero ExitError: %v\noutput:\n%s", err, outErrBuf.String())
		}
		if ee.ExitCode() != 1 {
			t.Errorf("exit code = %d, want 1", ee.ExitCode())
		}
		const wantMsg = "cannot install the Codex CLI: the npm global prefix (/usr) is root-owned and passwordless sudo is unavailable in this container"
		if !strings.Contains(outErrBuf.String(), wantMsg) {
			t.Errorf("output does not contain the ux.md U3 verbatim refusal text %q; got: %s", wantMsg, outErrBuf.String())
		}
		sudoData, _ := os.ReadFile(sudoLog)
		if strings.Contains(string(sudoData), "npm install") {
			t.Errorf("sudo log shows an install call on the no-sudo-n branch; got: %s", sudoData)
		}
		npmData, _ := os.ReadFile(npmLog)
		if strings.Contains(string(npmData), "install -g") {
			t.Errorf("npm log shows an install call on the not-writable branch; got: %s", npmData)
		}
	})

	t.Run("TTY consent accepts only y or Y (static)", func(t *testing.T) {
		fn := extractShellFunction(content, "_ensure_codex_cli")
		if fn == "" {
			t.Fatal("could not extract _ensure_codex_cli() from quickstart.sh")
		}
		if !strings.Contains(fn, `!= "y"`) || !strings.Contains(fn, `!= "Y"`) {
			t.Errorf("_ensure_codex_cli()'s TTY consent read does not compare the answer against exactly "+
				"\"y\" and \"Y\"; body:\n%s", fn)
		}
	})
}

// TestQuickstartCodexLoginFollowsInstallAndSkipsWhenValid is T-4 (concern_tests.md §1.2,
// decisions.md D1/D2): with codex present, an invalid/absent session must trigger `codex login
// --device-auth` (never under sudo); a valid session must run the status check but skip login
// entirely. Also pins the DO-NOT-CHANGE negative: only `codex login --device-auth` is ever
// invoked — never a bare `codex login`, never `--with-api-key`.
//
// The stub `codex login status` prints the REAL CLI's documented output shape — the single
// hardcoded string "Logged in using ChatGPT" (verification-report.md #70/#107,
// codebase-snapshot.md:1031) — never the `auth_mode:`/`refresh_token:` key:value lines the old,
// self-confirming stub fabricated (investigation_report.md headline finding). Session-validity
// state instead lives in a hermetic `$CODEX_HOME/auth.json` fixture, matching design-doc.md D7 /
// data.md D-D1's predicate: CLI exit code + status text, AND `auth_mode`/`.tokens.refresh_token`
// read from `auth.json` via `jq`.
func TestQuickstartCodexLoginFollowsInstallAndSkipsWhenValid(t *testing.T) {
	content := quickstartScriptContent(t)
	harness := codexFnsHarness(t, content)

	writeAuthJSON := func(t *testing.T, codexHome, authMode, refreshToken string) {
		t.Helper()
		if authMode == "" && refreshToken == "" {
			return // no fixture at all — models "never logged in"
		}
		body := `{"auth_mode":"` + authMode + `","tokens":`
		if refreshToken == "" {
			body += "null}"
		} else {
			body += `{"refresh_token":"` + refreshToken + `"}}`
		}
		if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(body), 0o600); err != nil {
			t.Fatalf("writing auth.json fixture: %v", err)
		}
	}

	cases := []struct {
		name         string
		statusExit   string
		statusText   string
		authMode     string
		refreshToken string
		wantLogin    bool
	}{
		{"invalid-session", "1", "", "", "", true},
		{"valid-session", "0", "Logged in using ChatGPT", "chatgpt", "rt-abc123", false},
		{
			// The exact bug class this predicate exists to catch: the CLI reports a logged-in
			// session, but auth.json says the login is an API key, not a ChatGPT subscription
			// (no tokens object). A predicate that trusts CLI text alone would wrongly call
			// this valid and skip login.
			"cli-says-in-but-auth-json-is-api-key", "0", "Logged in using ChatGPT", "apikey", "", true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			bin := hermeticCodexBinDir(t)
			sudoLog := filepath.Join(t.TempDir(), "sudo.log")
			codexLog := filepath.Join(t.TempDir(), "codex.log")
			codexHome := t.TempDir()
			writeAuthJSON(t, codexHome, tc.authMode, tc.refreshToken)
			writeCodexStub(t, bin, "sudo", `printf '%s\n' "$*" >> "$SUDO_LOG"
exit 0
`)
			writeCodexStub(t, bin, "codex", `printf '%s\n' "$*" >> "$CODEX_LOG"
if [ "$1" = login ] && [ "$2" = status ]; then
  [ -n "$CODEX_STATUS_TEXT" ] && printf '%s\n' "$CODEX_STATUS_TEXT"
  exit "${CODEX_STATUS_EXIT:-0}"
fi
if [ "$1" = login ] && [ "$2" = --device-auth ]; then
  exit 0
fi
exit 0
`)

			script := harness + "\n_ensure_codex_session\n"
			cmd := exec.Command("bash", "-c", script)
			cmd.Env = hermeticCodexEnv(bin, "SUDO_LOG="+sudoLog, "CODEX_LOG="+codexLog,
				"CODEX_HOME="+codexHome, "CODEX_STATUS_EXIT="+tc.statusExit,
				"CODEX_STATUS_TEXT="+tc.statusText)
			var outBuf, errBuf bytes.Buffer
			cmd.Stdout = &outBuf
			cmd.Stderr = &errBuf
			// The invalid-session sub-case's stub never transitions to valid after a login
			// attempt (a static exit-code stub cannot model that), so _ensure_codex_session is
			// expected to still report failure there — only the valid-session sub-case must
			// succeed. What both sub-cases pin is which codex/sudo invocations happened, not the
			// function's own exit code.
			err := cmd.Run()
			if !tc.wantLogin && err != nil {
				t.Fatalf("_ensure_codex_session failed on an already-valid session: %v\nstdout:\n%s\nstderr:\n%s", err, outBuf.String(), errBuf.String())
			}

			codexData, err := os.ReadFile(codexLog)
			if err != nil {
				t.Fatalf("reading codex log: %v", err)
			}
			codexCalls := string(codexData)
			if !strings.Contains(codexCalls, "login status") {
				t.Errorf("codex log does not show the `login status` check ran; got: %s", codexCalls)
			}
			gotLogin := strings.Contains(codexCalls, "login --device-auth")
			if gotLogin != tc.wantLogin {
				t.Errorf("`codex login --device-auth` invoked = %v, want %v; codex log: %s", gotLogin, tc.wantLogin, codexCalls)
			}

			sudoData, err := os.ReadFile(sudoLog)
			if err != nil && !os.IsNotExist(err) {
				t.Fatalf("reading sudo log: %v", err)
			}
			if strings.Contains(string(sudoData), "codex") || strings.Contains(string(sudoData), "login") {
				t.Errorf("sudo log shows codex/login was invoked under sudo — login must never run under sudo; got: %s", sudoData)
			}
		})
	}

	t.Run("never bare login or --with-api-key", func(t *testing.T) {
		fn := extractShellFunction(content, "_ensure_codex_session")
		if fn == "" {
			t.Fatal("could not extract _ensure_codex_session() from quickstart.sh")
		}
		if strings.Contains(fn, "codex login\n") || strings.Contains(fn, `codex login "`) {
			t.Error("_ensure_codex_session() contains a bare `codex login` invocation; only `codex login --device-auth` may ever be invoked")
		}
		if strings.Contains(fn, "--with-api-key") {
			t.Error("_ensure_codex_session() contains `--with-api-key`; only `codex login --device-auth` may ever be invoked")
		}
		if !strings.Contains(fn, "CODEX_LOGIN_TIMEOUT") {
			t.Error("_ensure_codex_session() never references CODEX_LOGIN_TIMEOUT to bound the login wait")
		}
	})
}

// bashConsentLiteral extracts the literal text of _ensure_codex_cli's `read -r -p "..."` consent
// prompt, tolerating either quoting style (the exact style is not fixed by spec).
func bashConsentLiteral(t *testing.T, fnBody string) string {
	t.Helper()
	re := regexp.MustCompile(`read -r -p ("([^"]*)"|'([^']*)')`)
	m := re.FindStringSubmatch(fnBody)
	if m == nil {
		t.Fatal("_ensure_codex_cli has no `read -r -p \"...\"` consent prompt")
	}
	if m[2] != "" {
		return m[2]
	}
	return m[3]
}

// goConsentLiteral extracts install.go's promptCodexInstallConsent consent literal, scoped to that
// function's own body first so a later, unrelated fmt.Fprint(errW, "...") call elsewhere in
// install.go can never be silently matched instead.
func goConsentLiteral(t *testing.T, installGoSrc string) string {
	t.Helper()
	const marker = "var promptCodexInstallConsent = func("
	i := strings.Index(installGoSrc, marker)
	if i < 0 {
		t.Fatal("promptCodexInstallConsent not found in install.go")
	}
	body := installGoSrc[i:]
	re := regexp.MustCompile(`fmt\.Fprint\(errW, "([^"]*)"\)`)
	m := re.FindStringSubmatch(body)
	if m == nil {
		t.Fatal("promptCodexInstallConsent has no fmt.Fprint(errW, \"...\") consent literal")
	}
	return m[1]
}

// TestCodexConsentTextsAgree is T-8 (AC-3, concern_tests.md §2): the consent prompt text shown by
// quickstart.sh's `_ensure_codex_cli` (direct invocation path) and install.go's
// promptCodexInstallConsent (via `af install`) must be byte-identical, since both gate the same
// user-visible decision.
func TestCodexConsentTextsAgree(t *testing.T) {
	root := findModuleRoot(t)
	quickstartSrc := quickstartScriptContent(t)
	installGoData, err := os.ReadFile(filepath.Join(root, "internal", "cmd", "install.go"))
	if err != nil {
		t.Fatalf("reading install.go: %v", err)
	}
	installGoSrc := string(installGoData)

	const sanitySubstr = "This will INSTALL the codex cli"
	if !strings.Contains(quickstartSrc, sanitySubstr) {
		t.Fatalf("quickstart.sh does not contain the sanity substring %q anywhere", sanitySubstr)
	}
	if !strings.Contains(installGoSrc, sanitySubstr) {
		t.Fatalf("install.go does not contain the sanity substring %q anywhere", sanitySubstr)
	}

	bashFn := extractShellFunction(quickstartSrc, "_ensure_codex_cli")
	if bashFn == "" {
		t.Fatal("could not extract _ensure_codex_cli() from quickstart.sh")
	}
	bashLit := bashConsentLiteral(t, bashFn)
	goLit := goConsentLiteral(t, installGoSrc)
	if bashLit != goLit {
		t.Errorf("consent literal mismatch:\n  quickstart.sh: %q (len %d)\n  install.go:    %q (len %d)",
			bashLit, len(bashLit), goLit, len(goLit))
	}
}
