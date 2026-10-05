package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- issue af-d0bff338 (PR #694) Phase 3 (K9 per-mode yaml seeding, K13 auth-mode record wiring) --
//
// These tests pin _ensure_mode_yaml (AC-2), the retirement of the .codex-subscription.example
// fork (AC-1), the per-mode seed/legacy-migration behavior (T-5), and the K13
// _litellm_auth_mode_write wiring into setup_litellm()'s mode-resolution ladder (decisions.md D3).
// Written RED — see todos/fable-implement/red_predictions.md for the predicted failure per test.

// TestQuickstartExampleOrphanForkRetired pins AC-1: the .codex-subscription.example save-aside
// fork is fully retired; the literal string must not occur anywhere in quickstart.sh.
func TestQuickstartExampleOrphanForkRetired(t *testing.T) {
	content := quickstartScriptContent(t)
	if n := strings.Count(content, "codex-subscription.example"); n != 0 {
		t.Errorf("quickstart.sh still contains %d occurrence(s) of %q (AC-1 requires 0)", n, "codex-subscription.example")
	}
}

// TestQuickstartHasEnsureModeYamlHelper pins AC-2: the per-mode seeding helper exists.
func TestQuickstartHasEnsureModeYamlHelper(t *testing.T) {
	content := quickstartScriptContent(t)
	if !strings.Contains(content, "_ensure_mode_yaml") {
		t.Fatal("quickstart.sh has no _ensure_mode_yaml helper yet (AC-2)")
	}
}

// ensureModeYamlHarness extracts _ensure_mode_yaml and its logging dependencies from the REAL
// quickstart.sh so the actual gate/migration logic runs against real files in t.TempDir() — never
// a pasted copy (the same asymmetry TestExistingFactoryConfigIsMigrated's own comment warns
// against, quickstart_provisioning_shape_test.go:133-135).
func ensureModeYamlHarness(t *testing.T, content string) string {
	t.Helper()
	names := []string{"log_info", "log_warn", "log_success", "log_error", "_ensure_mode_yaml"}
	var b strings.Builder
	b.WriteString("set -euo pipefail\n")
	b.WriteString("RED='' GREEN='' YELLOW='' BLUE='' NC=''\n")
	for _, n := range names {
		fn := extractShellFunction(content, n)
		if fn == "" {
			t.Fatalf("could not extract %s() from quickstart.sh — _ensure_mode_yaml (AC-2) does not exist yet", n)
		}
		b.WriteString(fn)
		b.WriteString("\n")
	}
	return b.String()
}

// realApiKeySeedHeredoc reconstructs the literal `cat > "<target>" << 'EOF' ... EOF` heredoc using
// the LIVE heredoc #1 body (litellmSeedBlock) — never a hand-copied fixture, so an edit to the
// api-key seed changes this test's fixture too, same content TestQuickstartApiKeySeedIsByteIdentical
// already pins by golden SHA.
func realApiKeySeedHeredoc(t *testing.T, root, target string) string {
	t.Helper()
	body := litellmSeedBlock(t, setupLitellmSource(t, root))
	return `cat > "` + target + `" << 'EOF'` + body + "\nEOF\n"
}

// realSubscriptionBodyAssignment carves the LIVE `subscription_yaml_body="$(cat << 'SUBEOF' ...
// )"` assignment verbatim out of setup_litellm()'s real source, using the same boundary markers
// subscriptionSeedBlock relies on (`<< 'SUBEOF'` / `\nSUBEOF`), so every sub-test below stays
// byte-linked to the live heredoc #2 body instead of a hand-authored copy.
func realSubscriptionBodyAssignment(t *testing.T, setup string) string {
	t.Helper()
	const openMarker = `subscription_yaml_body="$(cat << 'SUBEOF'`
	i := strings.Index(setup, openMarker)
	if i < 0 {
		t.Fatal("could not locate the subscription_yaml_body assignment in setup_litellm()")
	}
	rest := setup[i:]
	const closeMarker = "\nSUBEOF\n)\""
	j := strings.Index(rest, closeMarker)
	if j < 0 {
		t.Fatal("could not locate the end of the subscription_yaml_body assignment")
	}
	return rest[:j+len(closeMarker)]
}

// apiKeySeedFileContent runs the LIVE heredoc #1 (realApiKeySeedHeredoc) standalone, once, to
// obtain byte-exact reference file content. apiKeyHeredocGoldenSHA (quickstart_litellm_auth_shape_test.go)
// hashes litellmSeedBlock's EXTRACTED text, whose documented boundary keeps the heredoc's opening
// line-terminator newline and drops its closing one — a representation chosen for mutation
// sensitivity in TestQuickstartApiKeySeedIsByteIdentical, not for byte-equality with what bash's
// heredoc actually writes to disk (no leading newline, one trailing newline). Comparing against
// this helper's real, executed output — rather than reusing that golden hash — is what stays
// byte-linked to the live heredoc body without inheriting a mismatched extraction boundary.
func apiKeySeedFileContent(t *testing.T, root string) []byte {
	t.Helper()
	const target = ".agentfactory/litellm.yaml"
	script := "set -euo pipefail\n" + realApiKeySeedHeredoc(t, root, target) + "cat " + shellQuote(target) + "\n"
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("bash", "-c", "cd "+shellQuote(dir)+"\n"+script).Output()
	if err != nil {
		t.Fatalf("computing reference api-key seed content: %v", err)
	}
	return out
}

// subscriptionSeedFileContent runs the LIVE subscription_yaml_body assignment plus the real
// script's own write idiom (`printf '%s\n' "$subscription_yaml_body"`) standalone, once, to obtain
// byte-exact reference content — sidesteps hand-simulating bash's command-substitution
// trailing-newline-stripping semantics in Go.
func subscriptionSeedFileContent(t *testing.T, root string) []byte {
	t.Helper()
	assign := realSubscriptionBodyAssignment(t, setupLitellmSource(t, root))
	script := "set -euo pipefail\n" + assign + "\nprintf '%s\\n' \"$subscription_yaml_body\"\n"
	out, err := exec.Command("bash", "-c", script).Output()
	if err != nil {
		t.Fatalf("computing reference subscription seed content: %v", err)
	}
	return out
}

// writeFixture runs a bash snippet (e.g. realApiKeySeedHeredoc's output) inside dir, for seeding
// pre-existing files before a gate test runs.
func writeFixture(t *testing.T, dir, snippet string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "set -euo pipefail\ncd " + shellQuote(dir) + "\n" + snippet
	if out, err := exec.Command("bash", "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("seeding fixture: %v\n%s", err, out)
	}
}

// TestQuickstartModeFilesSelectedByLaunchLine is T-5 (CHANGE item 5, AC-3): the real
// _ensure_mode_yaml gate/migration logic, exercised against real files, proves which mode seeds
// which file, the legacy save-aside migration, and same-mode-edit non-touch (content + mtime).
func TestQuickstartModeFilesSelectedByLaunchLine(t *testing.T) {
	root := findModuleRoot(t)

	const apiTarget = ".agentfactory/litellm.yaml"
	const subTarget = ".agentfactory/litellm.codex-subscription.yaml"

	runGate := func(t *testing.T, dir, mode, writeSnippet string) string {
		t.Helper()
		content := quickstartScriptContent(t)
		harness := ensureModeYamlHarness(t, content)
		script := harness +
			"cd " + shellQuote(dir) + "\n" +
			"mkdir -p .agentfactory\n" +
			"if _ensure_mode_yaml " + shellQuote(mode) + "; then\n" +
			writeSnippet +
			"  echo GATE_SAID_WRITE\n" +
			"else\n" +
			"  echo GATE_SAID_SKIP\n" +
			"fi\n"
		out, err := exec.Command("bash", "-c", script).CombinedOutput()
		if err != nil {
			if _, ok := err.(*exec.ExitError); !ok {
				t.Fatalf("running _ensure_mode_yaml: %v\n%s", err, out)
			}
		}
		return string(out)
	}

	t.Run("api-key seeds only litellm.yaml when absent", func(t *testing.T) {
		dir := t.TempDir()
		writeSnippet := realApiKeySeedHeredoc(t, root, apiTarget)
		out := runGate(t, dir, "api-key", writeSnippet)
		if !strings.Contains(out, "GATE_SAID_WRITE") {
			t.Fatalf("_ensure_mode_yaml api-key did not gate a write for an absent file:\n%s", out)
		}
		data, err := os.ReadFile(filepath.Join(dir, apiTarget))
		if err != nil {
			t.Fatalf("litellm.yaml was not written: %v", err)
		}
		if want := apiKeySeedFileContent(t, root); string(data) != string(want) {
			t.Errorf("written litellm.yaml content mismatch:\ngot:  %q\nwant: %q", data, want)
		}
		if _, err := os.Stat(filepath.Join(dir, subTarget)); !os.IsNotExist(err) {
			t.Error("an api-key run must never create litellm.codex-subscription.yaml")
		}
		matches, _ := filepath.Glob(filepath.Join(dir, ".agentfactory", "litellm.yaml.*.saved"))
		if len(matches) != 0 {
			t.Errorf("unexpected .saved sibling(s) on a fresh seed: %v", matches)
		}
	})

	t.Run("subscription seeds only litellm.codex-subscription.yaml, litellm.yaml untouched", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, dir, realApiKeySeedHeredoc(t, root, apiTarget))
		before, err := os.ReadFile(filepath.Join(dir, apiTarget))
		if err != nil {
			t.Fatal(err)
		}
		oldTime := time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(dir, apiTarget), oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		assign := realSubscriptionBodyAssignment(t, setupLitellmSource(t, root))
		writeSnippet := assign + "\nprintf '%s\\n' \"$subscription_yaml_body\" > " + shellQuote(subTarget) + "\n"
		out := runGate(t, dir, "codex-subscription", writeSnippet)
		if !strings.Contains(out, "GATE_SAID_WRITE") {
			t.Fatalf("_ensure_mode_yaml codex-subscription did not gate a write for an absent target:\n%s", out)
		}

		got, err := os.ReadFile(filepath.Join(dir, subTarget))
		if err != nil {
			t.Fatalf("litellm.codex-subscription.yaml was not written: %v", err)
		}
		want := subscriptionSeedFileContent(t, root)
		if string(got) != string(want) {
			t.Errorf("subscription seed content mismatch:\ngot:  %q\nwant: %q", got, want)
		}

		after, err := os.ReadFile(filepath.Join(dir, apiTarget))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Error("a subscription run rewrote litellm.yaml's content")
		}
		info, err := os.Stat(filepath.Join(dir, apiTarget))
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(oldTime) {
			t.Errorf("a subscription run touched litellm.yaml's mtime: got %v, want %v", info.ModTime(), oldTime)
		}
		if _, err := os.Stat(filepath.Join(dir, apiTarget+".codex-subscription.example")); !os.IsNotExist(err) {
			t.Error("the retired .example fork produced a file")
		}
	})

	t.Run("legacy chatgpt-only litellm.yaml under api-key mode is saved aside and reseeded", func(t *testing.T) {
		dir := t.TempDir()
		legacy := subscriptionSeedFileContent(t, root) // chatgpt-only, no api_key — genuine legacy shape
		if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, apiTarget), legacy, 0o644); err != nil {
			t.Fatal(err)
		}

		writeSnippet := realApiKeySeedHeredoc(t, root, apiTarget)
		out := runGate(t, dir, "api-key", writeSnippet)
		if !strings.Contains(out, "GATE_SAID_WRITE") {
			t.Fatalf("_ensure_mode_yaml api-key did not gate a reseed for a legacy chatgpt-only file:\n%s", out)
		}

		matches, err := filepath.Glob(filepath.Join(dir, ".agentfactory", "litellm.yaml.*.saved"))
		if err != nil {
			t.Fatal(err)
		}
		if len(matches) != 1 {
			t.Fatalf("got %d .saved sibling(s), want exactly 1: %v", len(matches), matches)
		}
		savedData, err := os.ReadFile(matches[0])
		if err != nil {
			t.Fatal(err)
		}
		if string(savedData) != string(legacy) {
			t.Error("the .saved sibling does not carry the original legacy content byte-for-byte")
		}

		reseeded, err := os.ReadFile(filepath.Join(dir, apiTarget))
		if err != nil {
			t.Fatal(err)
		}
		if want := apiKeySeedFileContent(t, root); string(reseeded) != string(want) {
			t.Errorf("litellm.yaml was not reseeded from heredoc #1:\ngot:  %q\nwant: %q", reseeded, want)
		}

		savedBase := filepath.Base(matches[0])
		if !strings.Contains(out, savedBase) {
			t.Errorf("warning output does not name the actual .saved filename %q:\n%s", savedBase, out)
		}
	})

	t.Run("same-mode operator edit under api-key mode is left untouched", func(t *testing.T) {
		dir := t.TempDir()
		writeFixture(t, dir, realApiKeySeedHeredoc(t, root, apiTarget))
		before, err := os.ReadFile(filepath.Join(dir, apiTarget))
		if err != nil {
			t.Fatal(err)
		}
		oldTime := time.Date(2001, 6, 15, 12, 0, 0, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(dir, apiTarget), oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		writeSnippet := realApiKeySeedHeredoc(t, root, apiTarget)
		out := runGate(t, dir, "api-key", writeSnippet)
		if !strings.Contains(out, "GATE_SAID_SKIP") {
			t.Fatalf("_ensure_mode_yaml api-key rewrote an existing openai/-laned litellm.yaml:\n%s", out)
		}

		after, err := os.ReadFile(filepath.Join(dir, apiTarget))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(before) {
			t.Error("an operator-edited litellm.yaml was rewritten")
		}
		info, err := os.Stat(filepath.Join(dir, apiTarget))
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(oldTime) {
			t.Errorf("an operator-edited litellm.yaml's mtime changed: got %v, want %v", info.ModTime(), oldTime)
		}
		matches, _ := filepath.Glob(filepath.Join(dir, ".agentfactory", "litellm.yaml.*.saved"))
		if len(matches) != 0 {
			t.Errorf("a same-mode edit must not create a .saved sibling: %v", matches)
		}
	})

	t.Run("same-mode operator edit under subscription mode is left untouched", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
			t.Fatal(err)
		}
		pre := subscriptionSeedFileContent(t, root)
		if err := os.WriteFile(filepath.Join(dir, subTarget), pre, 0o644); err != nil {
			t.Fatal(err)
		}
		oldTime := time.Date(2002, 3, 3, 3, 3, 3, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(dir, subTarget), oldTime, oldTime); err != nil {
			t.Fatal(err)
		}

		assign := realSubscriptionBodyAssignment(t, setupLitellmSource(t, root))
		writeSnippet := assign + "\nprintf '%s\\n' \"$subscription_yaml_body\" > " + shellQuote(subTarget) + "\n"
		out := runGate(t, dir, "codex-subscription", writeSnippet)
		if !strings.Contains(out, "GATE_SAID_SKIP") {
			t.Fatalf("_ensure_mode_yaml codex-subscription rewrote an existing litellm.codex-subscription.yaml:\n%s", out)
		}

		after, err := os.ReadFile(filepath.Join(dir, subTarget))
		if err != nil {
			t.Fatal(err)
		}
		if string(after) != string(pre) {
			t.Error("an operator-edited litellm.codex-subscription.yaml was rewritten")
		}
		info, err := os.Stat(filepath.Join(dir, subTarget))
		if err != nil {
			t.Fatal(err)
		}
		if !info.ModTime().Equal(oldTime) {
			t.Errorf("an operator-edited litellm.codex-subscription.yaml's mtime changed: got %v, want %v", info.ModTime(), oldTime)
		}
	})
}

// TestQuickstartAuthModeWriteWiredAfterLadder pins K13 (CHANGE item 4, decisions.md D3): the
// mode-resolution ladder must call the existing _litellm_auth_mode_write helper exactly once, from
// inside setup_litellm(), positioned after the ladder resolves (the "LiteLLM upstream auth mode:
// ..." log line) and before the credential-acquisition branch that can exit 1. Also closes the
// "not redefined" gap concern_tests.md §3 flags: a duplicate definition with identical observable
// behavior would pass the existing K2 round-trip tests silently.
func TestQuickstartAuthModeWriteWiredAfterLadder(t *testing.T) {
	root := findModuleRoot(t)
	content := quickstartScriptContent(t)

	if n := strings.Count(content, `_litellm_auth_mode_write "`); n != 1 {
		t.Fatalf(`quickstart.sh calls _litellm_auth_mode_write "..." %d time(s), want exactly 1`, n)
	}
	if n := strings.Count(content, "_litellm_auth_mode_write() {"); n != 1 {
		t.Fatalf("_litellm_auth_mode_write is defined %d time(s), want exactly 1 (must not be redefined)", n)
	}
	if n := strings.Count(content, "_litellm_auth_mode_read() {"); n != 1 {
		t.Fatalf("_litellm_auth_mode_read is defined %d time(s), want exactly 1 (must not be redefined)", n)
	}

	setup := setupLitellmSource(t, root)
	if !strings.Contains(setup, `_litellm_auth_mode_write "`) {
		t.Fatal("the _litellm_auth_mode_write call site is not inside setup_litellm()")
	}

	const logMarker = `log_info "LiteLLM upstream auth mode:`
	const writeMarker = `_litellm_auth_mode_write "`
	const credBranchMarker = `if [ "$LITELLM_AUTH_MODE" = "api-key" ]`
	const importMarker = `af gateway auth import`

	idxLog := strings.Index(setup, logMarker)
	idxWrite := strings.Index(setup, writeMarker)
	idxCred := strings.Index(setup, credBranchMarker)
	idxImport := strings.Index(setup, importMarker)
	if idxLog < 0 {
		t.Fatal("setup_litellm() no longer logs the resolved auth mode")
	}
	if idxWrite < 0 {
		t.Fatal("setup_litellm() does not call _litellm_auth_mode_write")
	}
	if idxCred < 0 {
		t.Fatal("setup_litellm() no longer branches on the resolved auth mode for credential acquisition")
	}
	if idxImport < 0 {
		t.Fatal("setup_litellm() no longer imports the subscription handle (`af gateway auth import`)")
	}
	// T15/decisions.md D13: the record is stamped only AFTER the mode's prerequisites succeed — after
	// ladder resolution (log) AND after the subscription branch's `af gateway auth import`, so a
	// declined consent/login (which exits 1 mid-branch) never leaves a lying codex-subscription
	// record. The PR's original K13 position (between the log and the credential branch) is T15's bug.
	if idxLog >= idxWrite {
		t.Errorf("the mode-record write must follow ladder resolution: log@%d write@%d", idxLog, idxWrite)
	}
	if idxWrite <= idxImport {
		t.Errorf("T15: the mode record must be stamped only after the subscription prerequisites "+
			"(import@%d), not before the credential branch — writing earlier leaves a lying "+
			"codex-subscription record on a declined consent; write@%d credBranch@%d", idxImport, idxWrite, idxCred)
	}
}

// TestQuickstartLadderConsultsEnvAndRecordAbovePresence pins PR #694 BODY-1 (decisions.md D17): the
// bash mode ladder in setup_litellm must consult the AF_LITELLM_AUTH env override and the recorded
// mode (_litellm_auth_mode_read) BEFORE the handle-presence migration and its both-handles refusal —
// parity with install.go's resolveLitellmAuthMode (whose tiers are behaviorally pinned by the Go
// TestGatewayAuthMode_Tier* / install-forwarding tests). RED at head: the ladder was flag>presence
// with an unconditional both-handles refusal — AF_LITELLM_AUTH absent from the script and
// _litellm_auth_mode_read defined with zero callers.
func TestQuickstartLadderConsultsEnvAndRecordAbovePresence(t *testing.T) {
	setup := setupLitellmSource(t, findModuleRoot(t))

	idxEnv := strings.Index(setup, "AF_LITELLM_AUTH")
	idxRecord := strings.Index(setup, "_litellm_auth_mode_read")
	idxRefusal := strings.Index(setup, "both an OpenAI API key")

	if idxRefusal < 0 {
		t.Fatal("the both-handles refusal is gone from setup_litellm; the ladder anchor moved (BODY-1)")
	}
	if idxEnv < 0 {
		t.Error("the setup_litellm mode ladder never reads AF_LITELLM_AUTH; the env-override tier is missing (BODY-1/D17)")
	} else if idxEnv >= idxRefusal {
		t.Errorf("AF_LITELLM_AUTH must be consulted before the both-handles refusal (BODY-1/D17): env@%d refusal@%d", idxEnv, idxRefusal)
	}
	if idxRecord < 0 {
		t.Error("the setup_litellm mode ladder never consults the recorded mode via _litellm_auth_mode_read; the record tier is missing (BODY-1/D17)")
	} else if idxRecord >= idxRefusal {
		t.Errorf("the recorded mode must be consulted before the both-handles refusal (BODY-1/D17): record@%d refusal@%d", idxRecord, idxRefusal)
	}
}
