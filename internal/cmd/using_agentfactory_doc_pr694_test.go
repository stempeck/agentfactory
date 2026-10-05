package cmd

import (
	"strings"
	"testing"
)

// litellmAuthParagraph returns the blank-line-delimited paragraph of USING_AGENTFACTORY.md that
// documents the `--litellm-auth` bootstrap flag. Scoping to that one paragraph keeps the guard off
// the doc's other one-command-flow prose and makes each assertion answerable to the flag's own text.
func litellmAuthParagraph(t *testing.T, content string) string {
	t.Helper()
	for _, para := range strings.Split(content, "\n\n") {
		if strings.Contains(para, "--litellm-auth") {
			return para
		}
	}
	t.Fatal("USING_AGENTFACTORY.md has no paragraph mentioning `--litellm-auth`; the bootstrap-options anchor moved")
	return ""
}

// TestUsingAgentfactoryDoc_LitellmAuthFlow pins PR #694 BODY-8: the `--litellm-auth` paragraph must
// describe the consent-gated one-command flow (bootstrap installs codex and runs
// `codex login --device-auth` automatically) and must stop pointing the reader at USING_LITELLM.md's
// removed "adoption steps". GREP-RED at head (the paragraph still says "adoption steps" and mentions
// neither consent nor device login); GREEN once the paragraph is rewritten.
func TestUsingAgentfactoryDoc_LitellmAuthFlow(t *testing.T) {
	para := litellmAuthParagraph(t, readUsingAgentfactoryDoc(t))

	if strings.Contains(para, "adoption steps") {
		t.Error("the --litellm-auth paragraph still points at USING_LITELLM.md's removed " +
			"\"adoption steps\"; bootstrap now installs codex and logs in automatically via the " +
			"consent-gated one-command flow (PR #694 BODY-8)")
	}
	if !strings.Contains(para, "consent") {
		t.Error("the --litellm-auth paragraph never mentions consent; the codex-subscription bootstrap " +
			"is consent-gated and the doc must say so (PR #694 BODY-8)")
	}
	if !strings.Contains(para, "codex login --device-auth") && !strings.Contains(para, "one-command") {
		t.Error("the --litellm-auth paragraph describes neither `codex login --device-auth` nor the " +
			"one-command flow bootstrap now runs to authenticate the subscription (PR #694 BODY-8)")
	}
}
