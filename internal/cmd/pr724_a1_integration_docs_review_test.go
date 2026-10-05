package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const (
	p724a1ADR007 = "docs/architecture/adrs/ADR-007-hooks-never-block.md"
	p724a1ADR017 = "docs/architecture/adrs/ADR-017-no-customer-repo-mutations.md"
)

func p724a1ReadDoc(t *testing.T, rel string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(findModuleRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(raw)
}

// Docs wrap prose at arbitrary columns, so phrase checks compare whitespace-normalised text.
func p724a1Flat(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func p724a1GuardAmendment(t *testing.T) string {
	t.Helper()
	doc := p724a1ReadDoc(t, p724a1ADR007)
	start := strings.Index(doc, "## Amendment (2026-09-29)")
	if start < 0 {
		t.Fatalf("%s has no `## Amendment (2026-09-29)` heading", p724a1ADR007)
	}
	end := strings.Index(doc[start:], "## Corpus links")
	if end < 0 {
		t.Fatalf("%s has no `## Corpus links` heading after the 2026-09-29 amendment", p724a1ADR007)
	}
	return p724a1Flat(doc[start : start+end])
}

func TestPR724_T1_ADR007GuardAmendmentPlainAndNumberFree(t *testing.T) {
	amendment := p724a1GuardAmendment(t)
	if strings.Contains(amendment, "can raise a runtime prompt that survives") {
		t.Error("the 2026-09-29 amendment still explains itself with the runtime-prompt sentence the operator could not understand")
	}
	unpublished := regexp.MustCompile(`#695|design 695|\.designs/695/|\.analysis/695/|IMPLREADME_PHASE`)
	if refs := unpublished.FindAllString(amendment, -1); len(refs) > 0 {
		t.Errorf("the 2026-09-29 amendment still cites an issue number or a path the published repo lacks: %q", refs)
	}
}

func TestPR724_T2_ADR007GuardAmendmentDropsNoRefusalClaim(t *testing.T) {
	if strings.Contains(p724a1GuardAmendment(t), "No tool call is refused") {
		t.Error("the 2026-09-29 amendment still says no tool call is refused, yet a denied permission request refuses the tool call that asked")
	}
}

func TestPR724_T1_KeepADR007BroadRuleAndPriorException(t *testing.T) {
	doc := p724a1ReadDoc(t, p724a1ADR007)
	if !strings.Contains(doc, "## Amendment (2026-08-31)") {
		t.Errorf("%s lost its `## Amendment (2026-08-31)` heading", p724a1ADR007)
	}
	if !strings.Contains(p724a1Flat(doc), "it remains broad") {
		t.Errorf("%s no longer says the broad rule \"remains broad\"", p724a1ADR007)
	}
	amendment := p724a1GuardAmendment(t)
	for _, keep := range []string{
		"The broad rule, and the 2026-08-31 exception, stand exactly as before.",
		"**Status:** Proposed (awaiting operator ruling R1)",
	} {
		if !strings.Contains(amendment, keep) {
			t.Errorf("the 2026-09-29 amendment no longer says %q", keep)
		}
	}
}

func TestPR724_T3_ADR017AmendmentNamesNoIntegration(t *testing.T) {
	doc := p724a1ReadDoc(t, p724a1ADR017)
	if strings.Contains(strings.ToLower(doc), "defenseclaw") {
		t.Errorf("%s names a specific integration (defenseclaw); its rule must hold for any integration", p724a1ADR017)
	}
	unpublished := regexp.MustCompile(`#695|\.designs/695/|\.analysis/695/`)
	if refs := unpublished.FindAllString(p724a1Flat(doc), -1); len(refs) > 0 {
		t.Errorf("%s still cites an issue number or a path the published repo lacks: %q", p724a1ADR017, refs)
	}
}

func TestPR724_T3_KeepADR017OriginalDecisionAndAmendmentHeading(t *testing.T) {
	doc := p724a1ReadDoc(t, p724a1ADR017)
	if !strings.Contains(p724a1Flat(doc), "af infrastructure commands must not delete customer data") {
		t.Errorf("%s no longer states that af infrastructure commands must not delete customer data", p724a1ADR017)
	}
	// ADR-025 cites this amendment by its heading date.
	if !strings.Contains(doc, "## Amendment (2026-09-28)") {
		t.Errorf("%s lost its `## Amendment (2026-09-28)` heading", p724a1ADR017)
	}
}

func TestPR724_T4_UsingAgentfactoryDocNoIssueNumbersOrRestartParagraph(t *testing.T) {
	doc := p724a1Flat(p724a1ReadDoc(t, "USING_AGENTFACTORY.md"))
	if refs := regexp.MustCompile(`(#|issue |PR |design )[0-9]{3,}`).FindAllString(doc, -1); len(refs) > 0 {
		t.Errorf("USING_AGENTFACTORY.md cites issue or PR numbers the published repo does not share: %q", refs)
	}
	if strings.Contains(doc, "Restart agents once after upgrading") {
		t.Error("USING_AGENTFACTORY.md still carries the paragraph telling operators to restart agents to shed stale tmux configuration keys")
	}
}

func TestPR724_T4_KeepReprovisionAndTroubleshootingSections(t *testing.T) {
	doc := p724a1ReadDoc(t, "USING_AGENTFACTORY.md")
	for _, keep := range []string{
		"Re-provision after upgrading the binary",
		"### Which endpoint/model/telemetry is this agent running with?",
	} {
		if !strings.Contains(doc, keep) {
			t.Errorf("USING_AGENTFACTORY.md lost %q", keep)
		}
	}
}

func TestPR724_T4_PublishedDocsNoIntegrationIssueReferences(t *testing.T) {
	unpublished := regexp.MustCompile(`#695|design 695|\.designs/695/|\.analysis/695/`)
	for _, rel := range []string{
		"USING_PLUGINS.md",
		"docs/architecture/adrs/ADR-025-plugin-repositories-trust-boundary.md",
		"docs/architecture/subsystems/embedded-assets.md",
		"internal/cmd/integrations/README.md",
	} {
		t.Run(filepath.Base(rel), func(t *testing.T) {
			if refs := unpublished.FindAllString(p724a1Flat(p724a1ReadDoc(t, rel)), -1); len(refs) > 0 {
				t.Errorf("%s cites an issue or design number the published repo does not share: %q", rel, refs)
			}
		})
	}
}
