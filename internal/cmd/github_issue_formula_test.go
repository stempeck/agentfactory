package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/formula"
)

// github_issue_formula_test.go — af-40ad72f3 / GitHub issue #491.
//
// .claude/skills/github-issue/SKILL.md is mandatory-by-protocol (the manager persona
// names it as the required issue-filing path) but was interactive-only: no formula or
// roster agent could be dispatched to run it unattended. This test proves the
// skillmd-mode conversion (internal/cmd/install_formulas/github-issue.formula.toml)
// exists, preserves the required workflow shape, and — the acceptance-critical part —
// redefines the skill's two "ask the user" points (Phase 3's per-symptom gap row, and
// the adjacent-finding scoping decision) so an unattended agent never stalls waiting
// for a human reply, while still surfacing the gap/decision visibly in its output.

const githubIssueFormulaPath = "install_formulas/github-issue.formula.toml"

func loadGithubIssueFormula(t *testing.T) *formula.Formula {
	t.Helper()
	data, err := os.ReadFile(githubIssueFormulaPath)
	if err != nil {
		t.Fatalf("read %s: %v — run /formula-create against .claude/skills/github-issue/SKILL.md", githubIssueFormulaPath, err)
	}
	f, err := formula.Parse(data)
	if err != nil {
		t.Fatalf("parse %s: %v", githubIssueFormulaPath, err)
	}
	return f
}

// TestGithubIssueFormulaValid proves the formula parses, validates (DAG has no
// cycles, no dangling `needs`), and declares itself as the workflow type every other
// work-execution formula in this repo uses.
func TestGithubIssueFormulaValid(t *testing.T) {
	f := loadGithubIssueFormula(t)

	if f.Name != "github-issue" {
		t.Errorf("formula name = %q, want %q", f.Name, "github-issue")
	}
	if f.Type != formula.TypeWorkflow {
		t.Errorf("formula type = %q, want %q", f.Type, formula.TypeWorkflow)
	}
	if err := f.Validate(); err != nil {
		t.Errorf("formula.Validate(): %v", err)
	}
	if _, err := f.TopologicalSort(); err != nil {
		t.Errorf("TopologicalSort(): %v (needs chain broken or cyclic)", err)
	}
}

// TestGithubIssueFormulaInvariantSteps proves all 10 mandatory invariant steps from
// the skillmd-mode conversion process (Section 10.2 of .claude/skills/formula-create/
// skillmd-mode.md) are present — omitting any of these is a structural regression
// every work-execution formula in this repo must avoid.
func TestGithubIssueFormulaInvariantSteps(t *testing.T) {
	f := loadGithubIssueFormula(t)

	want := []string{
		"load-context", "branch-setup", "validate-contract", "preflight-tests",
		"self-review", "run-tests", "self-verify", "cleanup-workspace",
		"prepare-for-review", "submit-and-exit",
	}
	have := map[string]bool{}
	for _, s := range f.Steps {
		have[s.ID] = true
	}
	for _, id := range want {
		if !have[id] {
			t.Errorf("missing invariant step %q", id)
		}
	}
}

// TestGithubIssueFormulaNeedsChainUnbroken proves every step except load-context
// declares a `needs` predecessor, per skillmd-mode.md Section 10.2 ("The needs chain
// must be unbroken").
func TestGithubIssueFormulaNeedsChainUnbroken(t *testing.T) {
	f := loadGithubIssueFormula(t)

	for _, s := range f.Steps {
		if s.ID == "load-context" {
			continue
		}
		if len(s.Needs) == 0 {
			t.Errorf("step %q has no `needs` — chain is broken", s.ID)
		}
	}
}

// TestGithubIssueFormulaIssueVarCLISource proves [vars.issue] uses source = "cli",
// matching every other shipped work formula (issue #98 / TestSpecialistFormulasIssueSourceCLI
// convention) rather than the legacy hook_bead source.
func TestGithubIssueFormulaIssueVarCLISource(t *testing.T) {
	f := loadGithubIssueFormula(t)

	v, ok := f.Vars["issue"]
	if !ok {
		t.Fatal("[vars.issue] not declared")
	}
	if v.Source != "cli" {
		t.Errorf("[vars.issue].source = %q, want %q", v.Source, "cli")
	}
	if !v.Required {
		t.Error("[vars.issue] must be required")
	}
}

// TestGithubIssueFormulaDomainPhasesPresent proves every phase of the original
// SKILL.md's 7-phase process (Understand -> Investigate -> Reconcile -> Draft ->
// Validate -> Post -> Bead/Dispatch) is represented as a formula step, so the
// conversion didn't silently drop a phase.
func TestGithubIssueFormulaDomainPhasesPresent(t *testing.T) {
	f := loadGithubIssueFormula(t)

	var allDescriptions strings.Builder
	ids := map[string]bool{}
	for _, s := range f.Steps {
		ids[s.ID] = true
		allDescriptions.WriteString(s.Title)
		allDescriptions.WriteString("\n")
		allDescriptions.WriteString(s.Description)
		allDescriptions.WriteString("\n")
	}

	phaseCount := 0
	for id := range ids {
		if strings.HasPrefix(id, "phase-") {
			phaseCount++
		}
	}
	if phaseCount < 6 {
		t.Errorf("found %d phase-* steps, want at least 6 (one per SKILL.md phase, phase 7 may be merged as conditional)", phaseCount)
	}

	body := allDescriptions.String()
	for _, must := range []string{"gh api", "Validate the Draft"} {
		if !strings.Contains(body, must) {
			t.Errorf("no step mentions %q — a domain phase looks missing or content wasn't preserved", must)
		}
	}
}

// TestGithubIssueFormulaAskUserPointsNonStalling is the acceptance-critical test for
// GitHub issue #491's second AC: "Investigation gaps that would have required asking
// a user are handled without stalling and are visible in the output."
//
// The original SKILL.md blocks on the user in exactly two places (both in its Phase
// 3): a per-symptom "gap" row in the reconciliation table ("ask the user"), and the
// adjacent-finding scoping decision ("Scoping a finding out requires asking the user
// first"). An unattended dispatched agent cannot wait for a human reply, so the
// formula step covering Phase 3 must NOT contain either blocking instruction, and
// must instead replace it with a non-blocking escalation (mail, not a wait) plus an
// explicit, visible record of the open question / scope decision in the drafted
// artifact.
func TestGithubIssueFormulaAskUserPointsNonStalling(t *testing.T) {
	f := loadGithubIssueFormula(t)

	step := f.GetStep("phase-3-reconcile-findings")
	if step == nil {
		t.Fatal("step \"phase-3-reconcile-findings\" not found")
	}
	body := step.Description

	forbidden := []string{"ask the user", "asking the user"}
	for _, phrase := range forbidden {
		if regexp.MustCompile(`(?i)` + regexp.QuoteMeta(phrase)).MatchString(body) {
			t.Errorf("phase-3-reconcile-findings still contains blocking instruction %q — must be replaced with non-stalling escalation", phrase)
		}
	}

	requiredMarkers := map[string]string{
		"af mail send supervisor": "non-blocking escalation path (fire-and-forget mail, not a wait)",
		"Open Questions":          "visible record of unresolved symptom gaps in the drafted artifact",
		"Scope Decisions":         "visible record of adjacent-finding include/exclude decisions",
	}
	for marker, reason := range requiredMarkers {
		if !strings.Contains(body, marker) {
			t.Errorf("phase-3-reconcile-findings missing %q (%s)", marker, reason)
		}
	}

	if !regexp.MustCompile(`(?i)do not (wait|block|stall)`).MatchString(body) {
		t.Error(`phase-3-reconcile-findings must explicitly instruct the agent not to wait/block/stall on a reply`)
	}
}

// TestGithubIssueFormulaMandatoryDirective proves the formula description ends with
// the verbatim MANDATORY execution directive every skillmd-mode-derived formula
// carries (skillmd-mode.md Section 10.7 item 6).
func TestGithubIssueFormulaMandatoryDirective(t *testing.T) {
	f := loadGithubIssueFormula(t)

	const marker = "## !IMPORTANT - MANDATORY Exact Step Execution"
	if !strings.Contains(f.Description, marker) {
		t.Errorf("formula description missing mandatory execution directive %q", marker)
	}
	if !strings.Contains(f.Description, "TERMINATE YOU") {
		t.Error("formula description missing the fidelity-gate termination clause")
	}
}

// TestGithubIssueFormulaSyncedToStore is a targeted early signal for the generic
// TestFormulaDriftSourceVsInstalled check: the new formula must be copied to the
// on-disk store as well as the embedded source, or `af sling --formula github-issue`
// fails to find it in a fresh factory.
func TestGithubIssueFormulaSyncedToStore(t *testing.T) {
	const mirrorPath = "../../.agentfactory/store/formulas/github-issue.formula.toml"
	if _, err := os.Stat(filepath.Clean(mirrorPath)); err != nil {
		t.Errorf("mirror not found at %s: %v — run `make sync-formulas`", mirrorPath, err)
	}
}

// TestGithubIssueTemplateNotDrifted is the github-issue-specific, non-skipped
// counterpart to TestFormulaTemplateDrift (which stays globally skipped for the
// general "brand new formula has no committed template yet" chicken-and-egg case
// documented on that test). Once a template IS committed for a formula — which
// af-40ad72f3 / GitHub issue #491 requires so `make check-regen` (CI) has something
// to actually compare against on a fresh checkout — this test proves it wasn't
// hand-edited out of sync with what `af formula agent-gen` would produce from the
// current formula source.
func TestGithubIssueTemplateNotDrifted(t *testing.T) {
	f := loadGithubIssueFormula(t)

	const tmplPath = "../templates/roles/github-issue.md.tmpl"
	committed, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatalf("read committed template %s: %v — run `af formula agent-gen github-issue` and commit the result", tmplPath, err)
	}

	regenerated := generateAgentTemplate(f, f.Name, "autonomous")
	if string(committed) != regenerated {
		t.Errorf("template drift: %s does not match regenerated output from %s — rerun `af formula agent-gen github-issue` and commit the refreshed template", tmplPath, githubIssueFormulaPath)
	}
}
