package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// readUsingLitellmDoc reads the LiteLLM gateway runbook. Sibling of
// readUsingAgentfactoryDoc (dispatch_crons_doc_test.go).
func readUsingLitellmDoc(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "USING_LITELLM.md"))
	if err != nil {
		t.Fatalf("reading USING_LITELLM.md: %v", err)
	}
	return string(data)
}

// readQuickstartSh reads the bootstrap script that seeds the LiteLLM gateway, so the
// version-parity subtest can grep its LITELLM_VERSION value rather than trust a copy.
func readQuickstartSh(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(findModuleRoot(t), "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	return string(data)
}

// quickstartLitellmVersion matches the LITELLM_VERSION="X.Y.Z" assignment by VALUE, not by
// line number — the anchor drifted from quickstart.sh:47 (pre-#686) to :48 once
// LITELLM_AUTH_MODE was inserted above it, and a future insertion can move it again.
var quickstartLitellmVersion = regexp.MustCompile(`(?m)^LITELLM_VERSION="([^"]+)"`)

// usingLitellmVersionMention matches USING_LITELLM.md's `litellm[proxy]==X.Y.Z` version
// mention, likewise by value.
var usingLitellmVersionMention = regexp.MustCompile(`litellm\[proxy\]==([0-9]+\.[0-9]+\.[0-9]+)`)

// openaiKeyAbsentLine matches the retired verify-recipe line that asserts the OpenAI key file is
// absent. Coexistence of a stored key and a live subscription is normal (operator ruling 2), so the
// recipe must verify the active path via `status --json`, not by the key file's absence. Matched by
// value, tolerant of the column padding, so it never trips the accurate "stores no OpenAI API key"
// prose mention in the Setup section (USING_LITELLM.md:50, protected by P16).
var openaiKeyAbsentLine = regexp.MustCompile(`openai\.key\s+#\s*absent`)

// litellmDocSection returns the body of the ATX section whose heading line starts with
// headingPrefix, from that heading through the line before the next ATX heading. Fenced code blocks
// are tracked so a `#`-comment inside an example cannot end the section early. Sibling idiom of
// cronsDocSection (dispatch_crons_doc_test.go).
func litellmDocSection(t *testing.T, content, headingPrefix string) string {
	t.Helper()
	lines := strings.Split(content, "\n")
	start := -1
	for i, line := range lines {
		if strings.HasPrefix(line, headingPrefix) {
			start = i
			break
		}
	}
	if start < 0 {
		t.Fatalf("USING_LITELLM.md has no section headed %q", headingPrefix)
	}
	inFence := false
	for i := start + 1; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "```") {
			inFence = !inFence
			continue
		}
		if !inFence && isATXHeading(lines[i]) {
			return strings.Join(lines[start:i], "\n")
		}
	}
	return strings.Join(lines[start:], "\n")
}

// TestUsingLitellmDocContract pins USING_LITELLM.md to the shipped --litellm-auth
// subscription mode (issue #686 Phase 5): the retired "subscription cannot be used"
// claim must stay gone, both auth modes must be documented, and the LITELLM_VERSION
// mention must never drift from quickstart.sh's installed version.
func TestUsingLitellmDocContract(t *testing.T) {
	content := readUsingLitellmDoc(t)

	t.Run("the subscription-cannot-be-used claim is retired", func(t *testing.T) {
		if strings.Contains(content, "subscription cannot be used") {
			t.Error("USING_LITELLM.md still claims a ChatGPT subscription cannot be used; " +
				"the two-mode Setup must retire this sentence (issue #686)")
		}
	})

	t.Run("the litellm-auth flag is documented", func(t *testing.T) {
		if !strings.Contains(content, "litellm-auth") {
			t.Error("USING_LITELLM.md never mentions litellm-auth; the two-mode Setup must " +
				"document the --litellm-auth flag")
		}
	})

	t.Run("af gateway auth import is documented", func(t *testing.T) {
		if !strings.Contains(content, "af gateway auth import") {
			t.Error("USING_LITELLM.md never mentions `af gateway auth import`; the " +
				"subscription-mode adoption steps must reference it")
		}
	})

	t.Run("both --litellm-auth values are documented", func(t *testing.T) {
		for _, value := range []string{"api-key", "codex-subscription"} {
			if !strings.Contains(content, value) {
				t.Errorf("USING_LITELLM.md does not mention --litellm-auth value %q", value)
			}
		}
	})

	t.Run("the infeasible unprivileged codex install is retired", func(t *testing.T) {
		if strings.Contains(content, "npm install -g @openai/codex` (once), then") {
			t.Error("USING_LITELLM.md still instructs an unprivileged, manual " +
				"`npm install -g @openai/codex` as a required operator step (PR #694 Phase 4: " +
				"bootstrap now installs it automatically, consent-gated and sudo-respecting)")
		}
	})

	t.Run("the host-relay is not framed as the first-time path", func(t *testing.T) {
		if strings.Contains(content, "no second login") {
			t.Error("USING_LITELLM.md still frames the docker-cp/--codex-auth host-relay as " +
				"the first-time subscription-mode answer (the retired \"no second login\" " +
				"framing); bootstrap now handles install+login automatically in-container")
		}
	})

	t.Run("the consent prompt text is documented", func(t *testing.T) {
		if !strings.Contains(content, "This will INSTALL the codex") ||
			!strings.Contains(content, "are you sure? y/N") {
			t.Error("USING_LITELLM.md does not document the codex-install consent prompt " +
				"bootstrap actually shows the operator")
		}
	})

	t.Run("codex login --device-auth is documented", func(t *testing.T) {
		if !strings.Contains(content, "codex login --device-auth") {
			t.Error("USING_LITELLM.md does not mention `codex login --device-auth`, the " +
				"authentication mechanism bootstrap runs")
		}
	})

	t.Run("no stale bare kill-session -t litellm form remains", func(t *testing.T) {
		if strings.Contains(content, "kill-session -t litellm") &&
			!strings.Contains(content, "kill-session -t =litellm") {
			t.Error("USING_LITELLM.md mentions `kill-session -t litellm` without the exact-match " +
				"`=litellm` form quickstart.sh now uses everywhere")
		}
		if regexp.MustCompile(`kill-session -t litellm\b`).MatchString(content) {
			t.Error("USING_LITELLM.md still documents a manual `tmux kill-session -t litellm` " +
				"step; the gateway now reconciles (restarts) itself on a config/credential/build " +
				"identity mismatch")
		}
	})

	t.Run("a changed credential is no longer promised to change the launch identity", func(t *testing.T) {
		normalized := strings.Join(strings.Fields(content), " ")
		if strings.Contains(normalized, "changed credential all change the recorded launch identity") {
			t.Error("USING_LITELLM.md still promises a \"changed credential\" changes the recorded launch " +
				"identity; the identity keys on the static credential PATH, not on re-import time, so a " +
				"re-imported credential is invisible to the reconcile compare (PR #694 T12)")
		}
	})

	t.Run("the verify recipe drops the openai.key-absence check and verifies via selected_mode", func(t *testing.T) {
		section := litellmDocSection(t, content, "### Verifying it end-to-end")
		if openaiKeyAbsentLine.MatchString(section) {
			t.Error("the verify recipe still asserts `.agentfactory/secrets/openai.key` is absent; a stored " +
				"key coexisting with a live subscription is normal, so the recipe must confirm the active " +
				"path via `af gateway auth status --json` selected_mode instead (PR #694 BODY-3)")
		}
		if !strings.Contains(section, "selected_mode") {
			t.Error("the verify recipe never references `selected_mode`; it must confirm the resolved gateway " +
				"mode via `af gateway auth status --json` selected_mode (PR #694 BODY-3)")
		}
	})

	t.Run("LITELLM_VERSION stays in parity with quickstart.sh", func(t *testing.T) {
		qs := readQuickstartSh(t)
		qsMatch := quickstartLitellmVersion.FindStringSubmatch(qs)
		if qsMatch == nil {
			t.Fatal("quickstart.sh declares no LITELLM_VERSION=\"...\" line; the parity " +
				"anchor this test greps for has moved or been renamed")
		}
		docMatch := usingLitellmVersionMention.FindStringSubmatch(content)
		if docMatch == nil {
			t.Fatal("USING_LITELLM.md never mentions litellm[proxy]==X.Y.Z; the parity " +
				"anchor this test greps for has moved or been reworded")
		}
		if qsMatch[1] != docMatch[1] {
			t.Errorf("LITELLM_VERSION parity broken: quickstart.sh declares %q, "+
				"USING_LITELLM.md says %q", qsMatch[1], docMatch[1])
		}
	})
}
