package cmd

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"text/template"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/session"
	"github.com/stempeck/agentfactory/internal/templates"
)

func TestGenerateAgentTemplate_Workflow(t *testing.T) {
	f := &formula.Formula{
		Name:        "test-workflow",
		Description: "Test workflow description",
		Type:        formula.TypeWorkflow,
		Steps: []formula.Step{
			{ID: "step1", Title: "First Step", Description: "Do the first thing"},
			{ID: "step2", Title: "Second Step", Description: "Do the second thing", Needs: []string{"step1"}},
		},
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	// AC: Generated agent includes formula description in Behavioral Discipline
	if !strings.Contains(content, "Test workflow description") {
		t.Error("generated agent does not include formula description")
	}

	// AC: Generated agent references af commands
	if !strings.Contains(content, "af prime") {
		t.Error("generated agent does not reference af prime")
	}
	if !strings.Contains(content, "af done") {
		t.Error("generated agent does not reference af done")
	}

	// AC: Uses agentfactory terminology (no molecule/wisp)
	if strings.Contains(content, "molecule") {
		t.Error("generated agent contains gastown term 'molecule'")
	}
	if strings.Contains(content, "wisp") {
		t.Error("generated agent contains gastown term 'wisp'")
	}

	// Contains step listing
	if !strings.Contains(content, "First Step") {
		t.Error("generated agent does not list step titles")
	}
	if !strings.Contains(content, "Second Step") {
		t.Error("generated agent does not list step titles")
	}

	// Contains formula name
	if !strings.Contains(content, "test-workflow") {
		t.Error("generated agent does not contain formula name")
	}

	// Template structure
	if !strings.Contains(content, "# Agent Identity: {{ .Role }}") {
		t.Error("missing Agent Identity template heading")
	}
	if !strings.Contains(content, "## Operational Knowledge") {
		t.Error("missing Operational Knowledge section")
	}
	if !strings.Contains(content, "## Behavioral Discipline") {
		t.Error("missing Behavioral Discipline section")
	}
}

func TestGenerateAgentTemplate_WorkflowWithGates(t *testing.T) {
	f := &formula.Formula{
		Name: "gated-workflow",
		Type: formula.TypeWorkflow,
		Steps: []formula.Step{
			{ID: "step1", Title: "Do Work", Description: "Work"},
			{ID: "step2", Title: "Wait for Review", Description: "Gate step", Gate: &formula.Gate{
				Type: "approval", ID: "gate-1",
			}},
		},
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	// Structural gate gets GATE marker in table
	if !strings.Contains(content, "| GATE |") {
		t.Error("generated agent does not mark structural gate steps")
	}
	if !strings.Contains(content, "af done --phase-complete --gate") {
		t.Error("generated agent does not include gate execution instructions")
	}
}

func TestGenerateAgentTemplate_Convoy(t *testing.T) {
	f := &formula.Formula{
		Name:        "test-convoy",
		Description: "Parallel analysis",
		Type:        formula.TypeConvoy,
		Legs: []formula.Leg{
			{ID: "leg1", Title: "Leg One", Focus: "Performance"},
			{ID: "leg2", Title: "Leg Two", Focus: "Security"},
		},
		Synthesis: &formula.Synthesis{
			Title:       "Combine Results",
			Description: "Merge all leg outputs",
			DependsOn:   []string{"leg1", "leg2"},
		},
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	if !strings.Contains(content, "Leg One") {
		t.Error("generated agent does not list leg titles")
	}
	if !strings.Contains(content, "Combine Results") {
		t.Error("generated agent does not include synthesis")
	}
	if !strings.Contains(content, "parallel") {
		t.Error("generated agent does not mention parallel execution for convoy")
	}
}

func TestGenerateAgentTemplate_Expansion(t *testing.T) {
	f := &formula.Formula{
		Name: "test-expansion",
		Type: formula.TypeExpansion,
		Template: []formula.Template{
			{ID: "t1", Title: "Template One", Description: "First template step"},
			{ID: "t2", Title: "Template Two", Description: "Second template step"},
		},
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	if !strings.Contains(content, "Template One") {
		t.Error("generated agent does not list template titles")
	}
	// Templates section now uses a table format
	if !strings.Contains(content, "| Template |") {
		t.Error("generated agent does not include template table")
	}
}

func TestGenerateAgentTemplate_Aspect(t *testing.T) {
	f := &formula.Formula{
		Name: "test-aspect",
		Type: formula.TypeAspect,
		Aspects: []formula.Aspect{
			{ID: "a1", Title: "Aspect One", Focus: "Correctness"},
			{ID: "a2", Title: "Aspect Two", Focus: "Style"},
		},
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	if !strings.Contains(content, "Aspect One") {
		t.Error("generated agent does not list aspect titles")
	}
	if !strings.Contains(content, "parallel") {
		t.Error("generated agent does not mention parallel for aspects")
	}
}

func TestGenerateAgentTemplate_Variables(t *testing.T) {
	f := &formula.Formula{
		Name: "var-workflow",
		Type: formula.TypeWorkflow,
		Vars: map[string]formula.Var{
			"feature": {Description: "The feature to implement", Required: true},
		},
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	if !strings.Contains(content, "feature") {
		t.Error("generated agent does not list variable 'feature'")
	}
	// Required is now in table format: "yes" not "(required)"
	if !strings.Contains(content, "| yes |") {
		t.Error("generated agent does not mark required variables in table")
	}
}

func TestGenerateAgentTemplate_NoGastownTerminology(t *testing.T) {
	// Verify across all formula types that no gastown terminology leaks
	formulas := []*formula.Formula{
		{Name: "w", Type: formula.TypeWorkflow, Steps: []formula.Step{{ID: "s", Title: "S"}}},
		{Name: "c", Type: formula.TypeConvoy, Legs: []formula.Leg{{ID: "l", Title: "L"}}},
		{Name: "e", Type: formula.TypeExpansion, Template: []formula.Template{{ID: "t", Title: "T"}}},
		{Name: "a", Type: formula.TypeAspect, Aspects: []formula.Aspect{{ID: "a", Title: "A"}}},
	}

	forbidden := []string{"molecule", "wisp", "bond"}
	for _, f := range formulas {
		content := generateAgentTemplate(f, f.Name, "autonomous")
		for _, term := range forbidden {
			if strings.Contains(strings.ToLower(content), term) {
				t.Errorf("formula type %s: generated agent contains forbidden term %q", f.Type, term)
			}
		}
	}
}

func TestGenerateAgentTemplate_ThreeLayerFormat(t *testing.T) {
	f := &formula.Formula{
		Name:        "investigate",
		Description: "Investigate a codebase question or bug with structured analysis",
		Type:        formula.TypeWorkflow,
		Version:     1,
		Steps: []formula.Step{
			{ID: "orient", Title: "Orient and scope", Description: "Orient yourself"},
			{ID: "verify", Title: "Verify findings against symptoms", Description: "Verify", Needs: []string{"orient"}},
			{ID: "report", Title: "Write up findings", Description: "Report", Needs: []string{"verify"}},
		},
		Vars: map[string]formula.Var{
			"issue": {Description: "What to investigate (bug description, question, or issue reference)", Required: true, Source: "cli"},
		},
	}

	content := generateAgentTemplate(f, "investigate", "autonomous")

	// Template sections must exist
	for _, section := range []string{"# Agent Identity: {{ .Role }}", "## Operational Knowledge", "## Behavioral Discipline"} {
		if !strings.Contains(content, section) {
			t.Errorf("missing section %q in generated output", section)
		}
	}

	// HTML comment header
	if !strings.Contains(content, "<!-- Generated by af formula agent-gen from investigate v1 -->") {
		t.Error("missing HTML comment header")
	}

	// Formula-specific sling command
	if !strings.Contains(content, "af sling --formula investigate") {
		t.Error("missing formula-specific sling command")
	}

	// Structure table with step count
	if !strings.Contains(content, "| # | Step | Gate |") {
		t.Error("missing structure table header")
	}
	if !strings.Contains(content, "Orient and scope") {
		t.Error("missing step title in structure table")
	}

	// Variables table with Source column
	if !strings.Contains(content, "| Variable | Required | Source | Description |") {
		t.Error("missing variables table header")
	}
	if !strings.Contains(content, "| issue | yes | cli |") {
		t.Error("missing variable row with required/source")
	}

	// Behavioral Discipline contains description verbatim
	if !strings.Contains(content, "## Behavioral Discipline") {
		t.Error("missing Behavioral Discipline section")
	}

	// No gate section for gateless formula
	if strings.Contains(content, "### Gate Steps") {
		t.Error("gate section should not appear for gateless formula")
	}

	// Available Commands should NOT include gate command for gateless formula
	if strings.Contains(content, "af done --phase-complete --gate") {
		t.Error("gate command should not appear for gateless formula")
	}
}

func TestGenerateAgentTemplate_DualGateDetection(t *testing.T) {
	f := &formula.Formula{
		Name:        "gated-formula",
		Description: "Test dual gate detection",
		Type:        formula.TypeWorkflow,
		Version:     1,
		Steps: []formula.Step{
			{ID: "step1", Title: "Do Work", Description: "Work"},
			{ID: "step2", Title: "GATE 0: Verify consensus", Description: "Gate by title"},
			{ID: "step3", Title: "Structural gate step", Description: "Gate by struct", Gate: &formula.Gate{
				Type: "approval", ID: "gate-1",
			}},
		},
	}

	content := generateAgentTemplate(f, "gated-formula", "autonomous")

	// Title-heuristic gate gets GATE*
	if !strings.Contains(content, "GATE*") {
		t.Error("title-heuristic gate should show GATE* marker")
	}

	// Structural gate gets GATE (without asterisk)
	// Need to check for "GATE" without asterisk on the structural gate row
	if !strings.Contains(content, "| GATE |") {
		t.Error("structural gate should show GATE marker (without asterisk)")
	}

	// Gate protocol section must appear
	if !strings.Contains(content, "### Gate Steps") {
		t.Error("gate protocol section missing")
	}

	// Gate command in Available Commands
	if !strings.Contains(content, "af done --phase-complete --gate") {
		t.Error("gate command missing from Available Commands")
	}
}

func TestGenerateAgentTemplate_NameOverride(t *testing.T) {
	f := &formula.Formula{
		Name:        "investigate",
		Description: "Test",
		Type:        formula.TypeWorkflow,
		Version:     1,
	}

	content := generateAgentTemplate(f, "my-custom-agent", "autonomous")

	// Template source uses {{ .Role }}, not a hardcoded name
	if !strings.Contains(content, "# Agent Identity: {{ .Role }}") {
		t.Error("template should use {{ .Role }} in heading, not hardcoded name")
	}

	// Rendering with the override name produces the correct heading
	data := templates.RoleData{
		Role:        "my-custom-agent",
		Description: "Test",
		RootDir:     "/tmp",
		WorkDir:     "/tmp/my-custom-agent",
	}
	rendered, err := renderTemplateString(content, data)
	if err != nil {
		t.Fatalf("rendering template: %v", err)
	}
	if !strings.Contains(rendered, "# Agent Identity: my-custom-agent") {
		t.Error("rendered output should contain agent name override")
	}
}

func TestFormulaAgentGen_WriteToFile(t *testing.T) {
	// Set up a temporary factory with a formula file
	tmpDir := t.TempDir()

	// Create formula dir and file
	formulaDir := config.FormulasDir(tmpDir)
	if err := os.MkdirAll(formulaDir, 0755); err != nil {
		t.Fatal(err)
	}

	formulaContent := `formula = "test-workflow"
description = "Test workflow"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "First Step"
description = "Do the first thing"
`
	if err := os.WriteFile(filepath.Join(formulaDir, "test-workflow.formula.toml"), []byte(formulaContent), 0644); err != nil {
		t.Fatal(err)
	}

	// Parse the formula file directly (FindFormulaFile requires factory root
	// discovery which is not relevant to what we're testing here)
	formulaPath := filepath.Join(formulaDir, "test-workflow.formula.toml")
	f, err := formula.ParseFile(formulaPath)
	if err != nil {
		t.Fatalf("parsing formula: %v", err)
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	// Write to a file
	outFile := filepath.Join(tmpDir, "output.md")
	if err := os.WriteFile(outFile, []byte(content), 0644); err != nil {
		t.Fatalf("writing output file: %v", err)
	}

	// Verify file was written and has expected content
	data, err := os.ReadFile(outFile)
	if err != nil {
		t.Fatalf("reading output file: %v", err)
	}

	output := string(data)
	if !strings.Contains(output, "af prime") {
		t.Error("output file does not reference af prime")
	}
	if !strings.Contains(output, "Test workflow") {
		t.Error("output file does not contain formula description")
	}
	if !strings.Contains(output, "First Step") {
		t.Error("output file does not contain step title")
	}
}

// setupFormulaFactory creates a temp factory with .agentfactory/factory.json,
// .agentfactory/agents.json (with manager+supervisor), and a formula TOML file.
func setupFormulaFactory(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(configDir, "agents"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create template directory for template file writes
	tmplDir := filepath.Join(dir, "internal", "templates", "roles")
	if err := os.MkdirAll(tmplDir, 0755); err != nil {
		t.Fatal(err)
	}

	// Create go.mod so resolveAFSource treats this as a valid AF source tree
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/stempeck/agentfactory\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}

	configs := map[string]string{
		"factory.json": `{"type":"factory","version":1,"name":"agentfactory"}`,
		// manual-fixture is a synthetic manual agent for tests that must exercise
		// the manual-agent guard (no "formula" field) without colliding with a
		// production-reserved session name (af-manager, af-supervisor) that may
		// be live in an operator's running factory. See gh-59.
		"agents.json":  `{"agents":{"manager":{"type":"interactive","description":"Interactive agent"},"supervisor":{"type":"autonomous","description":"Autonomous agent"},"manual-fixture":{"type":"autonomous","description":"Synthetic manual agent for test assertions"}}}`,
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Create formula
	formulaDir := config.FormulasDir(dir)
	if err := os.MkdirAll(formulaDir, 0755); err != nil {
		t.Fatal(err)
	}
	formulaContent := `formula = "investigate"
description = "Investigate a codebase question"
type = "workflow"
version = 1

[[steps]]
id = "orient"
title = "Orient and scope"
description = "Orient yourself"

[[steps]]
id = "verify"
title = "Verify findings"
description = "Verify"
needs = ["orient"]

[[steps]]
id = "report"
title = "Write up findings"
description = "Report"
needs = ["verify"]
`
	if err := os.WriteFile(filepath.Join(formulaDir, "investigate.formula.toml"), []byte(formulaContent), 0644); err != nil {
		t.Fatal(err)
	}

	return dir
}

func runFormulaAgentGenInDir(t *testing.T, dir string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	os.Chdir(dir)

	// Reset flags to defaults before each test run
	agentGenOutput = false
	agentGenName = ""
	agentGenType = "autonomous"
	agentGenDelete = false
	agentGenAFSrc = ""
	agentGenBuild = false
	compiledSourceRoot = dir

	var outBuf, errBuf bytes.Buffer
	rootCmd.SetOut(&outBuf)
	rootCmd.SetErr(&errBuf)
	rootCmd.SetArgs(append([]string{"formula", "agent-gen"}, args...))

	err = rootCmd.Execute()
	return outBuf.String(), errBuf.String(), err
}

func TestFormulaAgentGen_FullProvisioning(t *testing.T) {
	dir := setupFormulaFactory(t)

	stdout, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	// AC 7: stdout must be empty
	if stdout != "" {
		t.Errorf("expected empty stdout, got: %q", stdout)
	}

	// AC 1: agents.json entry exists with formula field
	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	agentsStr := string(agentsData)
	if !strings.Contains(agentsStr, `"investigate"`) {
		t.Error("agents.json does not contain investigate entry")
	}
	if !strings.Contains(agentsStr, `"formula"`) {
		t.Error("agents.json entry missing formula field")
	}

	// AC 1: workspace directory exists
	if _, err := os.Stat(filepath.Join(dir, ".agentfactory", "agents", "investigate")); err != nil {
		t.Fatalf("workspace directory not created: %v", err)
	}

	// AC 1: CLAUDE.md exists with content
	claudeData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents", "investigate", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not created: %v", err)
	}
	if !strings.Contains(string(claudeData), "# Agent Identity: investigate") {
		t.Error("CLAUDE.md missing rendered Agent Identity heading")
	}

	// AC 1: settings.json exists
	if _, err := os.Stat(filepath.Join(dir, ".agentfactory", "agents", "investigate", ".claude", "settings.json")); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}

	// AC 7: stderr has 6 checkmark lines (formula, template, agent entry, workspace, CLAUDE.md, settings.json)
	checkmarks := strings.Count(stderr, "✓")
	if checkmarks != 6 {
		t.Errorf("expected 6 checkmark lines in stderr, got %d\nstderr: %s", checkmarks, stderr)
	}

	// AC 5: manager and supervisor preserved
	if !strings.Contains(agentsStr, `"manager"`) {
		t.Error("manager entry lost from agents.json")
	}
	if !strings.Contains(agentsStr, `"supervisor"`) {
		t.Error("supervisor entry lost from agents.json")
	}
}

func TestFormulaAgentGen_DryRun(t *testing.T) {
	dir := setupFormulaFactory(t)

	stdout, _, err := runFormulaAgentGenInDir(t, dir, "investigate", "-o")
	if err != nil {
		t.Fatalf("dry-run failed: %v", err)
	}

	// AC 2: stdout contains CLAUDE.md content
	if !strings.Contains(stdout, "<!-- Generated by af formula agent-gen") {
		t.Error("dry-run stdout missing CLAUDE.md header")
	}

	// AC 2: no files created
	if _, err := os.Stat(filepath.Join(dir, ".agentfactory", "agents", "investigate")); err == nil {
		t.Error("dry-run should not create workspace directory")
	}
}

func TestFormulaAgentGen_Idempotent(t *testing.T) {
	dir := setupFormulaFactory(t)

	// First run
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}

	// AC 3: second run succeeds
	stdout, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("idempotent re-run failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	// Should show "updated" language
	if !strings.Contains(stderr, "updated") {
		t.Errorf("idempotent re-run should use 'updated' language, got: %s", stderr)
	}
}

func TestFormulaAgentGen_NonFormulaAgentRejected(t *testing.T) {
	dir := setupFormulaFactory(t)

	// AC 4: --name supervisor should fail (supervisor has no formula field)
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate", "--name", "supervisor")
	if err == nil {
		t.Fatal("expected error when targeting non-formula agent, got nil")
	}
	if !strings.Contains(err.Error(), "not created by agent-gen") {
		t.Errorf("error should mention 'not created by agent-gen', got: %s", err.Error())
	}
}

func TestFormulaAgentGen_NameOverrideWorkspace(t *testing.T) {
	dir := setupFormulaFactory(t)

	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate", "--name", "detective")
	if err != nil {
		t.Fatalf("agent-gen with --name failed: %v", err)
	}

	// Workspace should be named "detective", not "investigate"
	if _, err := os.Stat(filepath.Join(dir, ".agentfactory", "agents", "detective")); err != nil {
		t.Fatal("workspace should use --name override for directory name")
	}

	// agents.json entry key should be "detective"
	data, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"detective"`) {
		t.Error("agents.json should have entry keyed by --name override")
	}

	// But formula field should be "investigate" (f.Name, not agentName)
	if !strings.Contains(string(data), `"investigate"`) {
		t.Error("formula field should use formula name, not --name override")
	}
}

func TestHumanSize(t *testing.T) {
	tests := []struct {
		bytes int
		want  string
	}{
		{0, "0 B"},
		{500, "500 B"},
		{1024, "1.0 KB"},
		{2150, "2.1 KB"},
		{12700, "12.4 KB"},
	}
	for _, tt := range tests {
		got := humanSize(tt.bytes)
		if got != tt.want {
			t.Errorf("humanSize(%d) = %q, want %q", tt.bytes, got, tt.want)
		}
	}
}

// --- Phase 4 tests ---

func TestGenerateAgentTemplate_SlingCommand(t *testing.T) {
	f := &formula.Formula{
		Name: "my-formula",
		Type: formula.TypeWorkflow,
		Vars: map[string]formula.Var{
			"target":  {Description: "Target to analyze", Required: true, Source: "cli"},
			"verbose": {Description: "Enable verbose mode", Required: false, Source: "env"},
		},
		Inputs: map[string]formula.Input{
			"request": {Description: "The request to process", Required: true},
		},
	}

	content := generateAgentTemplate(f, "custom-agent", "autonomous")

	// Sling command uses formula name, not agent name
	if !strings.Contains(content, "af sling --formula my-formula") {
		t.Error("sling command should use formula name, not agent name")
	}
	if strings.Contains(content, "af sling --formula custom-agent") {
		t.Error("sling command should not use agent name")
	}

	// CLI-sourced var present with --var flag
	if !strings.Contains(content, "--var target=") {
		t.Error("missing --var target= in sling command")
	}

	// Env-sourced var must NOT appear as --var flag in sling command
	if strings.Contains(content, "--var verbose=") {
		t.Error("env-sourced var 'verbose' should not appear as --var flag in sling command")
	}

	// Input (always CLI-sourced) present with --var flag
	if !strings.Contains(content, "--var request=") {
		t.Error("missing --var request= in sling command for required input")
	}
}

func TestGenerateAgentTemplate_DescriptionVerbatim(t *testing.T) {
	multiLineDesc := `## Overview

This is a multi-line description with:
- Bullet point one
- Bullet point two

### Code Example
` + "```bash\necho hello\n```" + `

Final paragraph with **bold** and _italic_ text.`

	f := &formula.Formula{
		Name:        "desc-test",
		Description: multiLineDesc,
		Type:        formula.TypeWorkflow,
		Version:     1,
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	// Verify description appears verbatim under Behavioral Discipline
	if !strings.Contains(content, "## Behavioral Discipline") {
		t.Fatal("missing Behavioral Discipline section")
	}

	// The description should appear verbatim
	if !strings.Contains(content, multiLineDesc) {
		t.Error("description does not appear verbatim in output")
	}

	// Verify specific substrings
	for _, sub := range []string{
		"## Overview",
		"- Bullet point one",
		"### Code Example",
		"echo hello",
		"**bold** and _italic_",
	} {
		if !strings.Contains(content, sub) {
			t.Errorf("missing description substring: %q", sub)
		}
	}
}

func TestGenerateAgentTemplate_StepsLineAlwaysIncludesGateCount(t *testing.T) {
	gateless := &formula.Formula{
		Name: "simple",
		Type: formula.TypeWorkflow,
		Steps: []formula.Step{
			{ID: "s1", Title: "Step One"},
			{ID: "s2", Title: "Step Two", Needs: []string{"s1"}},
		},
	}
	content := generateAgentTemplate(gateless, gateless.Name, "autonomous")
	if !strings.Contains(content, "- **Steps**: 2 (0 gates)") {
		t.Errorf("gateless formula should include '(0 gates)' in Steps line, got content containing: %q",
			extractLine(content, "**Steps**"))
	}

	gated := &formula.Formula{
		Name: "gated",
		Type: formula.TypeWorkflow,
		Steps: []formula.Step{
			{ID: "s1", Title: "Do Work"},
			{ID: "s2", Title: "Review", Gate: &formula.Gate{Type: "approval", ID: "g1"}, Needs: []string{"s1"}},
		},
	}
	content = generateAgentTemplate(gated, gated.Name, "autonomous")
	if !strings.Contains(content, "- **Steps**: 2 (1 gates)") {
		t.Errorf("gated formula should include '(1 gates)' in Steps line, got content containing: %q",
			extractLine(content, "**Steps**"))
	}
}

func extractLine(content, marker string) string {
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, marker) {
			return line
		}
	}
	return "(not found)"
}

func TestGenerateAgentTemplate_NoGatesNoGateProtocol(t *testing.T) {
	f := &formula.Formula{
		Name: "simple-workflow",
		Type: formula.TypeWorkflow,
		Steps: []formula.Step{
			{ID: "step1", Title: "Analyze code", Description: "Analyze"},
			{ID: "step2", Title: "Write report", Description: "Report", Needs: []string{"step1"}},
		},
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	if strings.Contains(content, "### Gate Steps") {
		t.Error("gate protocol section should not appear for gateless formula")
	}
	if strings.Contains(content, "af done --phase-complete --gate") {
		t.Error("gate command should not appear for gateless formula")
	}
}

func TestProvisioningPipeline_TypeInteractive(t *testing.T) {
	dir := setupFormulaFactory(t)

	_, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate", "--type", "interactive")
	if err != nil {
		t.Fatalf("agent-gen --type interactive failed: %v\nstderr: %s", err, stderr)
	}

	// Read settings.json and verify it's the interactive variant
	settingsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents", "investigate", ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	settingsStr := string(settingsData)
	// The autonomous template includes "af prime --hook && af mail check --inject"
	// in SessionStart, while the interactive template has just "af prime --hook".
	if strings.Contains(settingsStr, "af prime --hook && af mail check --inject") {
		t.Error("interactive settings.json should not contain 'af prime --hook && af mail check --inject' in SessionStart (that's the autonomous template)")
	}

	// Verify it's valid JSON
	if !strings.Contains(settingsStr, "hooks") {
		t.Error("settings.json should contain hooks configuration")
	}
}

func TestProvisioningPipeline_CreatesAllArtifacts(t *testing.T) {
	dir := setupFormulaFactory(t)

	_, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen failed: %v\nstderr: %s", err, stderr)
	}

	// agents.json has entry with correct fields
	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	agentsStr := string(agentsData)

	// After SaveAgentConfig, JSON is pretty-printed
	if !strings.Contains(agentsStr, `"investigate"`) {
		t.Error("agents.json missing investigate entry")
	}
	if !strings.Contains(agentsStr, `"autonomous"`) {
		t.Error("agents.json investigate entry missing type")
	}
	if !strings.Contains(agentsStr, `"formula"`) {
		t.Error("agents.json investigate entry missing formula field")
	}
	if !strings.Contains(agentsStr, `"directive"`) {
		t.Error("agents.json investigate entry missing directive field")
	}

	// Workspace directory exists
	wsDir := filepath.Join(dir, ".agentfactory", "agents", "investigate")
	info, err := os.Stat(wsDir)
	if err != nil {
		t.Fatalf("workspace dir not created: %v", err)
	}
	if !info.IsDir() {
		t.Error("workspace path is not a directory")
	}

	// CLAUDE.md exists with 3-layer structure and formula-specific content
	claudeData, err := os.ReadFile(filepath.Join(wsDir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("CLAUDE.md not created: %v", err)
	}
	claudeStr := string(claudeData)
	for _, section := range []string{"# Agent Identity: investigate", "## Operational Knowledge", "## Behavioral Discipline"} {
		if !strings.Contains(claudeStr, section) {
			t.Errorf("CLAUDE.md missing section %q", section)
		}
	}
	if !strings.Contains(claudeStr, "Investigate a codebase question") {
		t.Error("CLAUDE.md missing formula description")
	}

	// settings.json exists and is valid JSON
	settingsData, err := os.ReadFile(filepath.Join(wsDir, ".claude", "settings.json"))
	if err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
	if !strings.Contains(string(settingsData), "{") {
		t.Error("settings.json does not appear to be valid JSON")
	}

	// Manager and supervisor preserved
	if !strings.Contains(agentsStr, `"manager"`) {
		t.Error("manager entry lost from agents.json")
	}
	if !strings.Contains(agentsStr, `"supervisor"`) {
		t.Error("supervisor entry lost from agents.json")
	}
}

func TestProvisioningPipeline_Idempotent(t *testing.T) {
	dir := setupFormulaFactory(t)

	// First run
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("first run failed: %v", err)
	}

	firstClaude, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents", "investigate", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md after first run: %v", err)
	}

	// Second run
	_, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("second run failed: %v\nstderr: %s", err, stderr)
	}

	// agents.json has exactly ONE investigate entry (not duplicated)
	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	count := strings.Count(string(agentsData), `"investigate"`)
	// "investigate" appears twice: once as the key and once as the formula value
	if count != 2 {
		t.Errorf("agents.json should have exactly 2 occurrences of 'investigate' (key + formula value), got %d", count)
	}

	// CLAUDE.md content identical to first run
	secondClaude, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents", "investigate", "CLAUDE.md"))
	if err != nil {
		t.Fatalf("reading CLAUDE.md after second run: %v", err)
	}
	if string(firstClaude) != string(secondClaude) {
		t.Error("CLAUDE.md content differs between first and second run")
	}

	// Stderr contains "updated" language
	if !strings.Contains(stderr, "updated") {
		t.Errorf("second run stderr should contain 'updated', got: %s", stderr)
	}

	// Manager and supervisor still present
	agentsStr := string(agentsData)
	if !strings.Contains(agentsStr, `"manager"`) {
		t.Error("manager entry lost after idempotent re-run")
	}
	if !strings.Contains(agentsStr, `"supervisor"`) {
		t.Error("supervisor entry lost after idempotent re-run")
	}
}

func TestProvisioningPipeline_DescriptionFirstSentence(t *testing.T) {
	cases := []struct {
		name        string
		description string
		want        string
	}{
		{
			name:        "sentence then paragraphs",
			description: "PR merge processor patrol loop.\n\nThe Patrol is the Engineer in the engine room. You process agent branches.",
			want:        "PR merge processor patrol loop.",
		},
		{
			name:        "heading before sentence",
			description: "## Overview\n\nPR merge processor patrol loop.\n\nMore details here.",
			want:        "## Overview\n\nPR merge processor patrol loop.",
		},
		{
			name:        "single sentence no trailing content",
			description: "Investigate a codebase question.",
			want:        "Investigate a codebase question.",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()

			configDir := filepath.Join(dir, ".agentfactory")
			os.MkdirAll(filepath.Join(configDir, "agents"), 0755)
			os.MkdirAll(filepath.Join(dir, "internal", "templates", "roles"), 0755)

			os.WriteFile(filepath.Join(configDir, "factory.json"), []byte(`{"type":"factory","version":1,"name":"agentfactory"}`), 0644)
			os.WriteFile(filepath.Join(configDir, "agents.json"), []byte(`{"agents":{"manager":{"type":"interactive","description":"Interactive agent"}}}`), 0644)

			formulaDir := config.FormulasDir(dir)
			os.MkdirAll(formulaDir, 0755)

			// Use raw string quoting to get the multi-line description into TOML
			formulaContent := "formula = \"patrol\"\ndescription = \"\"\"\n" + tc.description + "\"\"\"\ntype = \"workflow\"\nversion = 1\n\n[[steps]]\nid = \"check\"\ntitle = \"Check\"\ndescription = \"Check inbox\"\n"
			os.WriteFile(filepath.Join(formulaDir, "patrol.formula.toml"), []byte(formulaContent), 0644)

			_, stderr, err := runFormulaAgentGenInDir(t, dir, "patrol")
			if err != nil {
				t.Fatalf("agent-gen failed: %v\nstderr: %s", err, stderr)
			}

			agentsData, err := os.ReadFile(filepath.Join(configDir, "agents.json"))
			if err != nil {
				t.Fatalf("reading agents.json: %v", err)
			}

			var cfg config.AgentConfig
			if err := json.Unmarshal(agentsData, &cfg); err != nil {
				t.Fatalf("parsing agents.json: %v", err)
			}

			entry, ok := cfg.Agents["patrol"]
			if !ok {
				t.Fatal("patrol entry not found in agents.json")
			}

			if entry.Description != tc.want {
				t.Errorf("agents.json description mismatch\ngot:  %q\nwant: %q", entry.Description, tc.want)
			}
		})
	}
}

// --- Phase 5 tests ---

func TestGenerateAgentTemplate_ValidGoTemplate(t *testing.T) {
	f := &formula.Formula{
		Name:        "investigate",
		Description: "Investigate a codebase question",
		Type:        formula.TypeWorkflow,
		Version:     1,
		Steps: []formula.Step{
			{ID: "orient", Title: "Orient and scope", Description: "Orient yourself"},
			{ID: "verify", Title: "Verify findings", Description: "Verify", Needs: []string{"orient"}},
		},
	}

	content := generateAgentTemplate(f, "investigate", "autonomous")

	// Must parse as valid Go template
	_, err := template.New("test").Parse(content)
	if err != nil {
		t.Fatalf("generated template is not valid Go template: %v", err)
	}
}

func TestGenerateAgentTemplate_RendersCorrectly(t *testing.T) {
	f := &formula.Formula{
		Name:        "investigate",
		Description: "Investigate a codebase question",
		Type:        formula.TypeWorkflow,
		Version:     1,
		Steps: []formula.Step{
			{ID: "orient", Title: "Orient and scope", Description: "Orient"},
			{ID: "verify", Title: "Verify findings", Description: "Verify", Needs: []string{"orient"}},
		},
		Vars: map[string]formula.Var{
			"issue": {Description: "What to investigate", Required: true, Source: "cli"},
		},
	}

	content := generateAgentTemplate(f, "investigate", "autonomous")

	data := templates.RoleData{
		Role:        "investigate",
		Description: "Investigate a codebase question",
		RootDir:     "/tmp/factory",
		WorkDir:     "/tmp/factory/investigate",
	}
	rendered, err := renderTemplateString(content, data)
	if err != nil {
		t.Fatalf("rendering template: %v", err)
	}

	// Resolved template variables
	if !strings.Contains(rendered, "# Agent Identity: investigate") {
		t.Error("rendered output missing resolved Agent Identity heading")
	}
	if !strings.Contains(rendered, "You are **investigate**, Investigate a codebase question.") {
		t.Error("rendered output missing resolved identity sentence")
	}
	if !strings.Contains(rendered, "/tmp/factory") {
		t.Error("rendered output missing resolved RootDir")
	}
	if !strings.Contains(rendered, "/tmp/factory/investigate") {
		t.Error("rendered output missing resolved WorkDir")
	}

	// No unresolved template variables
	if strings.Contains(rendered, "{{ .") {
		t.Error("rendered output still contains unresolved template variables")
	}

	// Formula-specific content survives rendering
	if !strings.Contains(rendered, "af sling --formula investigate") {
		t.Error("rendered output missing formula-specific sling command")
	}
	if !strings.Contains(rendered, "Orient and scope") {
		t.Error("rendered output missing formula step title")
	}
}

func TestGenerateAgentTemplate_StandardSections(t *testing.T) {
	f := &formula.Formula{
		Name:        "test",
		Description: "Test formula",
		Type:        formula.TypeWorkflow,
		Version:     1,
	}

	content := generateAgentTemplate(f, "test", "autonomous")

	for _, section := range []string{
		"# Agent Identity: {{ .Role }}",
		"## Workspace",
		"## Operational Knowledge",
		"## Behavioral Discipline",
		"## Mail Protocol",
		"## Startup Protocol",
		"## Constraints",
	} {
		if !strings.Contains(content, section) {
			t.Errorf("template missing standard section %q", section)
		}
	}

	// Autonomous declaration
	if !strings.Contains(content, "You are an autonomous agent that acts independently") {
		t.Error("template missing autonomous agent declaration")
	}

	// Template variables present
	for _, v := range []string{"{{ .Role }}", "{{ .Description }}", "{{ .RootDir }}", "{{ .WorkDir }}"} {
		if !strings.Contains(content, v) {
			t.Errorf("template missing template variable %s", v)
		}
	}
}

func TestGenerateAgentTemplate_DescriptionEscaping(t *testing.T) {
	f := &formula.Formula{
		Name:        "test-escape",
		Description: "Use bd show {{issue}} to view details",
		Type:        formula.TypeWorkflow,
		Version:     1,
	}

	content := generateAgentTemplate(f, "test-escape", "autonomous")

	// Must parse as valid Go template despite {{ in description
	_, err := template.New("test").Parse(content)
	if err != nil {
		t.Fatalf("template with escaped description failed to parse: %v", err)
	}

	// Rendering should produce the original {{issue}} literal
	data := templates.RoleData{
		Role:        "test-escape",
		Description: "Test",
		RootDir:     "/tmp",
		WorkDir:     "/tmp/test",
	}
	rendered, err := renderTemplateString(content, data)
	if err != nil {
		t.Fatalf("rendering template with escaped description: %v", err)
	}
	if !strings.Contains(rendered, "{{issue}}") {
		t.Error("rendered output should contain literal {{issue}} from description")
	}
}

func TestProvisioningPipeline_CreatesTemplateFile(t *testing.T) {
	dir := setupFormulaFactory(t)

	_, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen failed: %v\nstderr: %s", err, stderr)
	}

	// Template file must be created
	tmplPath := filepath.Join(dir, "internal", "templates", "roles", "investigate.md.tmpl")
	tmplData, err := os.ReadFile(tmplPath)
	if err != nil {
		t.Fatalf("template file not created: %v", err)
	}

	tmplStr := string(tmplData)

	// Template must be valid Go template
	_, err = template.New("test").Parse(tmplStr)
	if err != nil {
		t.Fatalf("created template file is not valid Go template: %v", err)
	}

	// Template must contain standard template variables
	if !strings.Contains(tmplStr, "{{ .Role }}") {
		t.Error("template file missing {{ .Role }}")
	}

	// Stderr must mention template written
	if !strings.Contains(stderr, "Role template written") {
		t.Error("stderr missing template-written confirmation")
	}

	// Stderr must remind about make build
	if !strings.Contains(stderr, "make build") {
		t.Error("stderr missing make build reminder")
	}
}

func TestEscapeTmplDelimiters(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check string // substring that must appear in rendered output
	}{
		{"no delimiters", "hello world", "hello world"},
		{"single braces", "a {b} c", "a {b} c"},
		{"double braces", "bd show {{issue}}", "{{issue}}"},
		{"multiple occurrences", "{{a}} and {{b}}", "{{a}} and {{b}}"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			escaped := escapeTmplDelimiters(tt.input)

			// Must parse as valid template
			tmpl, err := template.New("test").Parse(escaped)
			if err != nil {
				t.Fatalf("escaped string failed to parse as template: %v", err)
			}

			// Must render to produce the original content
			var buf bytes.Buffer
			if err := tmpl.Execute(&buf, nil); err != nil {
				t.Fatalf("executing escaped template: %v", err)
			}
			if !strings.Contains(buf.String(), tt.check) {
				t.Errorf("rendered output %q does not contain %q", buf.String(), tt.check)
			}
		})
	}
}

// --- Delete tests ---

func TestFormulaAgentGen_DeleteSuccess(t *testing.T) {
	dir := setupFormulaFactory(t)

	// Create agent first
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen create failed: %v", err)
	}

	// Verify artifacts exist before delete
	agentsPath := filepath.Join(dir, ".agentfactory", "agents.json")
	tmplPath := filepath.Join(dir, "internal", "templates", "roles", "investigate.md.tmpl")
	wsDir := filepath.Join(dir, ".agentfactory", "agents", "investigate")

	if _, err := os.Stat(tmplPath); err != nil {
		t.Fatalf("template should exist before delete: %v", err)
	}
	if _, err := os.Stat(wsDir); err != nil {
		t.Fatalf("workspace should exist before delete: %v", err)
	}

	// Delete
	stdout, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate", "--delete")
	if err != nil {
		t.Fatalf("agent-gen --delete failed: %v\nstdout: %s\nstderr: %s", err, stdout, stderr)
	}

	// AC1: Config entry removed
	agentsData, err := os.ReadFile(agentsPath)
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	if strings.Contains(string(agentsData), `"investigate"`) {
		t.Error("agents.json still contains investigate entry after delete")
	}

	// AC1: Template removed
	if _, err := os.Stat(tmplPath); !os.IsNotExist(err) {
		t.Error("template file should be deleted")
	}

	// AC1: Workspace removed
	if _, err := os.Stat(wsDir); !os.IsNotExist(err) {
		t.Error("workspace directory should be deleted")
	}

	// AC5: Stdout empty
	if stdout != "" {
		t.Errorf("expected empty stdout, got: %q", stdout)
	}

	// AC5: Stderr has checkmark lines
	if !strings.Contains(stderr, "✓") {
		t.Error("stderr should contain checkmark lines")
	}

	// AC6: Make build reminder
	if !strings.Contains(stderr, "make build") {
		t.Error("stderr should remind about make build")
	}
}

func TestFormulaAgentGen_DeletePreservesOtherAgents(t *testing.T) {
	dir := setupFormulaFactory(t)

	// Create agent
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen create failed: %v", err)
	}

	// Delete
	_, _, err = runFormulaAgentGenInDir(t, dir, "investigate", "--delete")
	if err != nil {
		t.Fatalf("agent-gen --delete failed: %v", err)
	}

	// Manager and supervisor preserved
	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	agentsStr := string(agentsData)
	if !strings.Contains(agentsStr, `"manager"`) {
		t.Error("manager entry lost from agents.json after delete")
	}
	if !strings.Contains(agentsStr, `"supervisor"`) {
		t.Error("supervisor entry lost from agents.json after delete")
	}
}

func TestFormulaAgentGen_DeleteRefusesManualAgent(t *testing.T) {
	dir := setupFormulaFactory(t)

	// gh-59: Target a synthetic manual agent whose session name
	// ("af-manual-fixture") cannot collide with any production-reserved session
	// that may be live in the operator's factory. Defensive cleanup mirrors the
	// sibling TestFormulaAgentGen_DeleteRefusesLiveSession pattern to harden
	// against pathological environments where an af-manual-fixture session
	// somehow exists.
	if _, err := exec.LookPath("tmux"); err == nil {
		exec.Command("tmux", "kill-session", "-t", "af-manual-fixture").Run()
		t.Cleanup(func() {
			exec.Command("tmux", "kill-session", "-t", "af-manual-fixture").Run()
		})
	}

	// AC2: Attempt to delete manual agent "manual-fixture" (seeded by
	// setupFormulaFactory without a formula field).
	_, _, err := runFormulaAgentGenInDir(t, dir, "manual-fixture", "--delete")
	if err == nil {
		t.Fatal("expected error when deleting manual agent")
	}
	if !strings.Contains(err.Error(), "not created by agent-gen") {
		t.Errorf("error should mention 'not created by agent-gen', got: %s", err.Error())
	}
}

func TestFormulaAgentGen_DeleteNonExistentAgent(t *testing.T) {
	dir := setupFormulaFactory(t)

	// AC7: Delete agent not in config
	_, _, err := runFormulaAgentGenInDir(t, dir, "nonexistent", "--delete")
	if err == nil {
		t.Fatal("expected error when deleting nonexistent agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should mention 'not found', got: %s", err.Error())
	}
}

func TestFormulaAgentGen_DeletePartialState_NoWorkspace(t *testing.T) {
	dir := setupFormulaFactory(t)

	// Create agent, then manually remove workspace
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen create failed: %v", err)
	}
	os.RemoveAll(filepath.Join(dir, ".agentfactory", "agents", "investigate"))

	// AC7: Delete should still succeed
	_, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate", "--delete")
	if err != nil {
		t.Fatalf("agent-gen --delete with missing workspace failed: %v\nstderr: %s", err, stderr)
	}

	// Config entry should be removed
	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	if strings.Contains(string(agentsData), `"investigate"`) {
		t.Error("agents.json still contains investigate entry")
	}

	// Template should be removed
	tmplPath := filepath.Join(dir, "internal", "templates", "roles", "investigate.md.tmpl")
	if _, err := os.Stat(tmplPath); !os.IsNotExist(err) {
		t.Error("template file should be deleted")
	}
}

func TestFormulaAgentGen_DeletePartialState_NoTemplate(t *testing.T) {
	dir := setupFormulaFactory(t)

	// Create agent, then manually remove template
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen create failed: %v", err)
	}
	os.Remove(filepath.Join(dir, "internal", "templates", "roles", "investigate.md.tmpl"))

	// AC7: Delete should still succeed
	_, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate", "--delete")
	if err != nil {
		t.Fatalf("agent-gen --delete with missing template failed: %v\nstderr: %s", err, stderr)
	}

	// Config entry should be removed
	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	if strings.Contains(string(agentsData), `"investigate"`) {
		t.Error("agents.json still contains investigate entry")
	}

	// Workspace should be removed
	if _, err := os.Stat(filepath.Join(dir, ".agentfactory", "agents", "investigate")); !os.IsNotExist(err) {
		t.Error("workspace directory should be deleted")
	}
}

func TestFormulaAgentGen_DeleteRefusesLiveSession(t *testing.T) {
	// AC3: requires tmux binary
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	dir := setupFormulaFactory(t)

	// Create agent
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen create failed: %v", err)
	}

	// Start a tmux session matching the agent's session name
	sessionID := session.SessionName("investigate") // "af-investigate"
	startCmd := exec.Command("tmux", "new-session", "-d", "-s", sessionID)
	if err := startCmd.Run(); err != nil {
		t.Fatalf("creating tmux session: %v", err)
	}
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionID).Run()
	})

	// Attempt delete — should refuse
	_, _, err = runFormulaAgentGenInDir(t, dir, "investigate", "--delete")
	if err == nil {
		t.Fatal("expected error when deleting agent with live session")
	}
	if !strings.Contains(err.Error(), "live tmux session") {
		t.Errorf("error should mention 'live tmux session', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "af down investigate") {
		t.Errorf("error should mention 'af down investigate', got: %s", err.Error())
	}

	// Verify artifacts are NOT removed
	agentsData, err := os.ReadFile(filepath.Join(dir, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("reading agents.json: %v", err)
	}
	if !strings.Contains(string(agentsData), `"investigate"`) {
		t.Error("investigate entry should still be in agents.json after refused delete")
	}
}

func TestFormulaAgentGen_DeleteWarnsOnDirtyWorkspace(t *testing.T) {
	// AC4: requires git
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}

	dir := setupFormulaFactory(t)

	// Initialize git repo so git status works
	gitInit := exec.Command("git", "init")
	gitInit.Dir = dir
	if err := gitInit.Run(); err != nil {
		t.Fatalf("git init: %v", err)
	}
	gitAdd := exec.Command("git", "add", "-A")
	gitAdd.Dir = dir
	if err := gitAdd.Run(); err != nil {
		t.Fatalf("git add: %v", err)
	}
	gitCommit := exec.Command("git", "commit", "-m", "init", "--no-gpg-sign")
	gitCommit.Dir = dir
	gitCommit.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	if err := gitCommit.Run(); err != nil {
		t.Fatalf("git commit: %v", err)
	}

	// Create agent
	_, _, err := runFormulaAgentGenInDir(t, dir, "investigate")
	if err != nil {
		t.Fatalf("agent-gen create failed: %v", err)
	}

	// Add and commit the agent artifacts, then make a dirty change
	gitAdd2 := exec.Command("git", "add", "-A")
	gitAdd2.Dir = dir
	gitAdd2.Run()
	gitCommit2 := exec.Command("git", "commit", "-m", "add agent", "--no-gpg-sign")
	gitCommit2.Dir = dir
	gitCommit2.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@test",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@test",
	)
	gitCommit2.Run()

	// Create uncommitted change in workspace
	dirtyFile := filepath.Join(dir, ".agentfactory", "agents", "investigate", "dirty.txt")
	os.WriteFile(dirtyFile, []byte("uncommitted"), 0644)

	// Delete should succeed but warn
	_, stderr, err := runFormulaAgentGenInDir(t, dir, "investigate", "--delete")
	if err != nil {
		t.Fatalf("agent-gen --delete with dirty workspace failed: %v\nstderr: %s", err, stderr)
	}

	// AC4: stderr should contain warning about uncommitted changes
	if !strings.Contains(stderr, "uncommitted changes") {
		t.Errorf("stderr should warn about uncommitted changes, got: %s", stderr)
	}
}

func TestFormulaAgentGen_DeleteDoesNotTouchFactoryRootTemplates(t *testing.T) {
	factoryRoot, afSourceRoot := setupFormulaFactoryWithAFSource(t)

	// Re-add go.mod to factory root so it looks like an AF checkout
	os.WriteFile(filepath.Join(factoryRoot, "go.mod"),
		[]byte("module github.com/stempeck/agentfactory\n\ngo 1.24\n"), 0644)

	// Pre-populate template at factory root (simulates git-tracked template)
	factoryTmplDir := filepath.Join(factoryRoot, "internal", "templates", "roles")
	os.MkdirAll(factoryTmplDir, 0755)
	factoryTmpl := filepath.Join(factoryTmplDir, "investigate.md.tmpl")
	os.WriteFile(factoryTmpl, []byte("factory root template"), 0644)

	// Create agent with --af-src pointing to separate AF source
	_, _, err := runFormulaAgentGenInDir(t, factoryRoot, "investigate", "--af-src", afSourceRoot)
	if err != nil {
		t.Fatalf("agent-gen create failed: %v", err)
	}

	// Delete with --af-src
	_, _, err = runFormulaAgentGenInDir(t, factoryRoot, "investigate", "--delete", "--af-src", afSourceRoot)
	if err != nil {
		t.Fatalf("agent-gen --delete failed: %v", err)
	}

	// Template at AF source should be deleted
	if _, err := os.Stat(filepath.Join(afSourceRoot, "internal", "templates", "roles", "investigate.md.tmpl")); !os.IsNotExist(err) {
		t.Error("template at AF source should be deleted")
	}

	// Template at factory root must NOT be deleted
	if _, err := os.Stat(factoryTmpl); err != nil {
		t.Errorf("template at factory root must not be deleted: %v", err)
	}
}

func TestGenerateAgentTemplate_HandoffBlock(t *testing.T) {
	f := &formula.Formula{
		Name: "test-workflow",
		Type: formula.TypeWorkflow,
		Steps: []formula.Step{
			{ID: "step1", Title: "First Step", Description: "Do the first thing"},
		},
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	// AC: Handoff instruction present
	if !strings.Contains(content, "af handoff") {
		t.Error("generated agent does not include af handoff instruction")
	}
	if !strings.Contains(content, "Then cycle to a clean session:") {
		t.Error("generated agent missing handoff section header")
	}

	// AC: Three-block ordering: sling < handoff < drive-loop
	slingIdx := strings.Index(content, "af sling --formula")
	handoffIdx := strings.Index(content, "af handoff")
	driveIdx := strings.Index(content, "Then drive the workflow:")
	if slingIdx < 0 || handoffIdx < 0 || driveIdx < 0 {
		t.Fatal("one or more How You Work blocks missing entirely")
	}
	if slingIdx >= handoffIdx {
		t.Error("sling block should appear before handoff block")
	}
	if handoffIdx >= driveIdx {
		t.Error("handoff block should appear before drive-loop block")
	}

	// AC: Handoff command is inside a fenced code block
	fenceOpen := strings.LastIndex(content[:handoffIdx], "```\n")
	fenceClose := strings.Index(content[handoffIdx:], "```\n")
	if fenceOpen < 0 || fenceClose < 0 {
		t.Error("af handoff is not inside a fenced code block")
	}
}

// --- Phase 1 tests: renderVar + collectRenderVars + generator fixes ---

func TestCollectRenderVars_InputsAndVars(t *testing.T) {
	f := &formula.Formula{
		Name: "mixed-formula",
		Type: formula.TypeWorkflow,
		Inputs: map[string]formula.Input{
			"issue_uri": {Description: "GitHub issue URI", Required: true},
			"optional_flag": {Description: "An optional flag", Required: false},
		},
		Vars: map[string]formula.Var{
			"hook_val":  {Description: "From hooked bead", Required: true, Source: "hook_bead"},
			"cli_var":   {Description: "A CLI variable", Required: true, Source: "cli"},
			"env_var":   {Description: "Environment variable", Required: false, Source: "env"},
			"empty_src": {Description: "Empty source var", Required: false},
		},
	}

	rvs, err := collectRenderVars(f)
	if err != nil {
		t.Fatalf("collectRenderVars returned error: %v", err)
	}

	// Should have 6 entries total (2 inputs + 4 vars)
	if len(rvs) != 6 {
		t.Errorf("expected 6 renderVars, got %d", len(rvs))
	}

	// Verify inputs are marked as such
	byName := make(map[string]renderVar)
	for _, rv := range rvs {
		byName[rv.Name] = rv
	}

	// Input entries
	if rv, ok := byName["issue_uri"]; !ok {
		t.Error("missing renderVar for input 'issue_uri'")
	} else {
		if !rv.IsInput {
			t.Error("issue_uri should have IsInput=true")
		}
		if rv.Source != "cli" {
			t.Errorf("issue_uri source should be 'cli', got %q", rv.Source)
		}
		if !rv.Required {
			t.Error("issue_uri should be Required")
		}
	}

	// Non-CLI var should NOT be marked as input
	if rv, ok := byName["hook_val"]; !ok {
		t.Error("missing renderVar for var 'hook_val'")
	} else {
		if rv.IsInput {
			t.Error("hook_val should have IsInput=false")
		}
		if rv.Source != "hook_bead" {
			t.Errorf("hook_val source should be 'hook_bead', got %q", rv.Source)
		}
	}

	// Sort order: required inputs first, then optional inputs, then cli vars, then non-cli vars
	// Required inputs: issue_uri
	// Optional inputs: optional_flag
	// CLI vars (source=cli or source=""): cli_var, empty_src
	// Non-CLI vars: env_var, hook_val
	if len(rvs) >= 2 {
		if rvs[0].Name != "issue_uri" {
			t.Errorf("first entry should be required input 'issue_uri', got %q", rvs[0].Name)
		}
		if rvs[1].Name != "optional_flag" {
			t.Errorf("second entry should be optional input 'optional_flag', got %q", rvs[1].Name)
		}
	}
}

func TestCollectRenderVars_Collision(t *testing.T) {
	f := &formula.Formula{
		Name: "collision-formula",
		Type: formula.TypeWorkflow,
		Inputs: map[string]formula.Input{
			"issue": {Description: "The issue", Required: true},
		},
		Vars: map[string]formula.Var{
			"issue": {Description: "Also the issue", Required: true, Source: "cli"},
		},
	}

	_, err := collectRenderVars(f)
	if err == nil {
		t.Fatal("expected collision error, got nil")
	}
	if !strings.Contains(err.Error(), "collides") {
		t.Errorf("error should mention collision, got: %v", err)
	}
}

func TestGenerateOperationalPlaybook_InputsOnly(t *testing.T) {
	f := &formula.Formula{
		Name: "inputs-only",
		Type: formula.TypeWorkflow,
		Inputs: map[string]formula.Input{
			"problem": {Description: "Problem to solve", Required: true},
			"context": {Description: "Additional context", Required: false},
		},
	}

	content := generateOperationalPlaybook(f)

	// Required input should appear as --var flag
	if !strings.Contains(content, "--var problem=") {
		t.Error("required input 'problem' should appear as --var flag in sling command")
	}

	// Optional input should NOT appear as --var flag (there are required entries)
	if strings.Contains(content, "--var context=") {
		t.Error("optional input 'context' should not appear as --var flag when required entries exist")
	}
}

func TestGenerateOperationalPlaybook_NonCLIVarsExcluded(t *testing.T) {
	f := &formula.Formula{
		Name: "non-cli-formula",
		Type: formula.TypeWorkflow,
		Vars: map[string]formula.Var{
			"issue":   {Description: "Issue reference", Required: true, Source: "cli"},
			"env_val": {Description: "Env value", Required: false, Source: "env"},
			"lit_val": {Description: "Literal value", Required: false, Source: "literal"},
		},
	}

	content := generateOperationalPlaybook(f)

	// CLI-sourced var should appear as --var flag; non-CLI vars should not
	if !strings.Contains(content, "--var issue=") {
		t.Error("cli-sourced var 'issue' should appear as --var flag")
	}
	if strings.Contains(content, "--var env_val=") {
		t.Error("env-sourced var 'env_val' should NOT appear as --var flag")
	}
	if strings.Contains(content, "--var lit_val=") {
		t.Error("literal-sourced var 'lit_val' should NOT appear as --var flag")
	}
}

func TestGenerateOperationalPlaybook_OptionalOnlyCommented(t *testing.T) {
	f := &formula.Formula{
		Name: "optional-only",
		Type: formula.TypeWorkflow,
		Inputs: map[string]formula.Input{
			"feature_area": {Description: "Feature area to break down", Required: false},
		},
	}

	content := generateOperationalPlaybook(f)

	// Base sling command should have no --var flags
	if strings.Contains(content, " --var feature_area=") && !strings.Contains(content, "# Optional:") {
		t.Error("optional-only formula should not have --var flags in base command")
	}

	// Should have commented optional lines
	if !strings.Contains(content, "# Optional: --var feature_area=") {
		t.Error("optional-only formula should have commented '# Optional: --var' lines")
	}
}

func TestGenerateVariablesTable_InputsVisible(t *testing.T) {
	f := &formula.Formula{
		Name: "inputs-table",
		Type: formula.TypeWorkflow,
		Inputs: map[string]formula.Input{
			"problem": {Description: "Problem description", Required: true},
		},
	}

	content := generateVariablesTable(f)

	// Should NOT be empty (inputs-only formula should produce a table)
	if content == "" {
		t.Fatal("generateVariablesTable should return non-empty for formula with inputs")
	}

	// Should contain the input
	if !strings.Contains(content, "problem") {
		t.Error("variables table should contain input 'problem'")
	}
	if !strings.Contains(content, "| yes |") {
		t.Error("variables table should show 'yes' for required input")
	}
	if !strings.Contains(content, "| cli |") {
		t.Error("variables table should show 'cli' as source for inputs")
	}
}

func TestGenerateVariablesTable_RequiredUnless(t *testing.T) {
	f := &formula.Formula{
		Name: "conditional-formula",
		Type: formula.TypeWorkflow,
		Inputs: map[string]formula.Input{
			"target": {Description: "Target name", Required: false, RequiredUnless: []string{"other_input"}},
		},
	}

	content := generateVariablesTable(f)

	if !strings.Contains(content, "conditional") {
		t.Error("variables table should show 'conditional' for RequiredUnless input")
	}
}

func TestVarPlaceholder_RenderVar(t *testing.T) {
	rv := renderVar{
		Name:        "issue_uri",
		Description: "GitHub issue URI, including the full URL",
	}

	result := varPlaceholder(rv)
	if result != "github-issue-uri" {
		t.Errorf("varPlaceholder should truncate at comma, got %q", result)
	}
}


func TestGenerateAgentTemplate_InputsOnlyNoVars(t *testing.T) {
	formulaPath := filepath.Join("install_formulas", "design.formula.toml")
	f, err := formula.ParseFile(formulaPath)
	if err != nil {
		t.Fatalf("parsing design formula: %v", err)
	}

	content := generateAgentTemplate(f, f.Name, "autonomous")

	// Required input should appear as --var flag in sling command
	if !strings.Contains(content, "--var problem=") {
		t.Error("missing --var problem= in sling command for required input")
	}

	// Variables section should exist despite 0 vars
	if !strings.Contains(content, "### Variables") {
		t.Error("Variables section should exist even when formula has 0 vars (inputs are shown)")
	}

	// Variables table should contain a row for problem
	if !strings.Contains(content, "| problem |") {
		t.Error("missing problem row in variables table")
	}
}

func TestGenerateAgentTemplate_OptionalOnlyFormula(t *testing.T) {
	f := &formula.Formula{
		Name: "optional-only",
		Type: formula.TypeWorkflow,
		Steps: []formula.Step{
			{ID: "s1", Title: "Do stuff"},
		},
		Inputs: map[string]formula.Input{
			"debug": {Description: "Enable debug mode", Required: false},
			"limit": {Description: "Max items to process", Required: false, Default: "100"},
		},
	}

	content := generateAgentTemplate(f, "optional-agent", "autonomous")

	// Sling command line should NOT contain --var flags (no required entries)
	lines := strings.Split(content, "\n")
	for _, line := range lines {
		if strings.Contains(line, "af sling --formula optional-only") && strings.Contains(line, " --var ") {
			t.Error("optional-only formula should not have --var flags on sling command line")
		}
	}

	// Should have commented optional lines in the output
	if !strings.Contains(content, "# Optional: --var debug=") {
		t.Error("missing commented '# Optional: --var debug=' line for optional input")
	}
	if !strings.Contains(content, "# Optional: --var limit=") {
		t.Error("missing commented '# Optional: --var limit=' line for optional input")
	}
}

// --- AF Source Resolution tests ---

func setupFormulaFactoryWithAFSource(t *testing.T) (factoryRoot, afSourceRoot string) {
	t.Helper()
	factoryRoot = setupFormulaFactory(t)

	// Remove go.mod from factory root if it exists (force resolution away from fallback)
	os.Remove(filepath.Join(factoryRoot, "go.mod"))

	// Create separate AF source tree
	afSourceRoot = t.TempDir()
	if err := os.WriteFile(filepath.Join(afSourceRoot, "go.mod"), []byte("module github.com/stempeck/agentfactory\n\ngo 1.24\n"), 0644); err != nil {
		t.Fatal(err)
	}
	afTmplDir := filepath.Join(afSourceRoot, "internal", "templates", "roles")
	if err := os.MkdirAll(afTmplDir, 0755); err != nil {
		t.Fatal(err)
	}
	return factoryRoot, afSourceRoot
}

func TestValidateAFSource(t *testing.T) {
	t.Run("valid", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/stempeck/agentfactory\n"), 0644)
		if !validateAFSource(dir) {
			t.Error("validateAFSource should return true for dir with agentfactory go.mod")
		}
	})
	t.Run("missing_gomod", func(t *testing.T) {
		dir := t.TempDir()
		if validateAFSource(dir) {
			t.Error("validateAFSource should return false for dir without go.mod")
		}
	})
	t.Run("wrong_module", func(t *testing.T) {
		dir := t.TempDir()
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/other/project\n"), 0644)
		if validateAFSource(dir) {
			t.Error("validateAFSource should return false for go.mod without agentfactory")
		}
	})
}

func TestResolveAFSource(t *testing.T) {
	// Create valid AF source dirs for each tier
	makeValidAFDir := func(t *testing.T) string {
		t.Helper()
		dir := t.TempDir()
		// Resolve symlinks so expected paths match EvalSymlinks in resolveAFSource
		dir, _ = filepath.EvalSymlinks(dir)
		os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module github.com/stempeck/agentfactory\n"), 0644)
		return dir
	}

	factoryRoot := t.TempDir() // no go.mod — will be fallback only

	t.Run("flag_wins", func(t *testing.T) {
		flagDir := makeValidAFDir(t)
		envDir := makeValidAFDir(t)
		srcDir := makeValidAFDir(t)

		agentGenAFSrc = flagDir
		compiledSourceRoot = srcDir
		t.Setenv("AF_SOURCE_ROOT", envDir)
		t.Cleanup(func() {
			agentGenAFSrc = ""
			compiledSourceRoot = ""
		})

		resolved, fb := resolveAFSource(factoryRoot)
		if resolved != flagDir {
			t.Errorf("expected flag dir %q, got %q", flagDir, resolved)
		}
		if fb {
			t.Error("should not be fallback when flag is valid")
		}
	})

	t.Run("env_wins_when_no_flag", func(t *testing.T) {
		envDir := makeValidAFDir(t)
		srcDir := makeValidAFDir(t)

		agentGenAFSrc = ""
		compiledSourceRoot = srcDir
		t.Setenv("AF_SOURCE_ROOT", envDir)
		t.Cleanup(func() { compiledSourceRoot = "" })

		resolved, fb := resolveAFSource(factoryRoot)
		if resolved != envDir {
			t.Errorf("expected env dir %q, got %q", envDir, resolved)
		}
		if fb {
			t.Error("should not be fallback when env is valid")
		}
	})

	t.Run("sourceRoot_wins_when_no_flag_or_env", func(t *testing.T) {
		srcDir := makeValidAFDir(t)

		agentGenAFSrc = ""
		compiledSourceRoot = srcDir
		t.Setenv("AF_SOURCE_ROOT", "")
		t.Cleanup(func() { compiledSourceRoot = "" })

		resolved, fb := resolveAFSource(factoryRoot)
		if resolved != srcDir {
			t.Errorf("expected sourceRoot dir %q, got %q", srcDir, resolved)
		}
		if fb {
			t.Error("should not be fallback when sourceRoot is valid")
		}
	})

	t.Run("fallback_to_factory_root", func(t *testing.T) {
		agentGenAFSrc = ""
		compiledSourceRoot = ""
		t.Setenv("AF_SOURCE_ROOT", "")

		resolved, fb := resolveAFSource(factoryRoot)
		if resolved != factoryRoot {
			t.Errorf("expected factory root %q, got %q", factoryRoot, resolved)
		}
		if !fb {
			t.Error("should be fallback when all candidates empty")
		}
	})

	t.Run("invalid_flag_skipped", func(t *testing.T) {
		invalidDir := t.TempDir() // no go.mod
		envDir := makeValidAFDir(t)

		agentGenAFSrc = invalidDir
		compiledSourceRoot = ""
		t.Setenv("AF_SOURCE_ROOT", envDir)
		t.Cleanup(func() { agentGenAFSrc = "" })

		resolved, fb := resolveAFSource(factoryRoot)
		if resolved != envDir {
			t.Errorf("expected env dir %q after invalid flag skip, got %q", envDir, resolved)
		}
		if fb {
			t.Error("should not be fallback when env is valid")
		}
	})
}

func TestAgentGenWritesToAFSource(t *testing.T) {
	factoryRoot, afSourceRoot := setupFormulaFactoryWithAFSource(t)

	// Use AF_SOURCE_ROOT env to test resolution (compiledSourceRoot is reset by runFormulaAgentGenInDir)
	t.Setenv("AF_SOURCE_ROOT", afSourceRoot)

	_, stderr, err := runFormulaAgentGenInDir(t, factoryRoot, "investigate")
	if err != nil {
		t.Fatalf("agent-gen failed: %v\nstderr: %s", err, stderr)
	}

	// Template should be in AF source tree
	tmplPath := filepath.Join(afSourceRoot, "internal", "templates", "roles", "investigate.md.tmpl")
	if _, err := os.Stat(tmplPath); err != nil {
		t.Fatalf("template not written to AF source tree: %v", err)
	}

	// Template should NOT be in factory root
	factoryTmplPath := filepath.Join(factoryRoot, "internal", "templates", "roles", "investigate.md.tmpl")
	if _, err := os.Stat(factoryTmplPath); !os.IsNotExist(err) {
		t.Error("template should NOT be written to factory root when AF source is resolved")
	}

	// CLAUDE.md should still be in factory root
	claudePath := filepath.Join(factoryRoot, ".agentfactory", "agents", "investigate", "CLAUDE.md")
	if _, err := os.Stat(claudePath); err != nil {
		t.Fatalf("CLAUDE.md should be in factory root: %v", err)
	}

	// agents.json should still be in factory root
	agentsData, err := os.ReadFile(filepath.Join(factoryRoot, ".agentfactory", "agents.json"))
	if err != nil {
		t.Fatalf("agents.json should be in factory root: %v", err)
	}
	if !strings.Contains(string(agentsData), `"investigate"`) {
		t.Error("agents.json should contain investigate entry")
	}

	// Stderr should mention the AF source path
	if !strings.Contains(stderr, afSourceRoot) {
		t.Errorf("stderr should mention AF source path %q, got: %s", afSourceRoot, stderr)
	}
}

func TestAgentGenAFSrcFlag(t *testing.T) {
	factoryRoot, afSourceRoot := setupFormulaFactoryWithAFSource(t)

	_, stderr, err := runFormulaAgentGenInDir(t, factoryRoot, "investigate", "--af-src", afSourceRoot)
	if err != nil {
		t.Fatalf("agent-gen with --af-src failed: %v\nstderr: %s", err, stderr)
	}

	// Template should be in the --af-src path
	tmplPath := filepath.Join(afSourceRoot, "internal", "templates", "roles", "investigate.md.tmpl")
	if _, err := os.Stat(tmplPath); err != nil {
		t.Fatalf("template not written to --af-src path: %v", err)
	}

	// Template should NOT be in factory root
	factoryTmplPath := filepath.Join(factoryRoot, "internal", "templates", "roles", "investigate.md.tmpl")
	if _, err := os.Stat(factoryTmplPath); !os.IsNotExist(err) {
		t.Error("template should NOT be in factory root when --af-src is used")
	}
}

func TestAgentGenBuildFlag(t *testing.T) {
	factoryRoot, afSourceRoot := setupFormulaFactoryWithAFSource(t)

	// Create a fake Makefile that creates a marker file instead of actually building
	markerPath := filepath.Join(afSourceRoot, "build-marker")
	makefileContent := fmt.Sprintf("install:\n\ttouch %s\n", markerPath)
	os.WriteFile(filepath.Join(afSourceRoot, "Makefile"), []byte(makefileContent), 0644)

	_, stderr, err := runFormulaAgentGenInDir(t, factoryRoot, "investigate", "--af-src", afSourceRoot, "--build")
	if err != nil {
		t.Fatalf("agent-gen with --build failed: %v\nstderr: %s", err, stderr)
	}

	// Verify build was attempted (marker file exists)
	if _, err := os.Stat(markerPath); err != nil {
		t.Error("--build should have executed make install (marker file not found)")
	}

	// Stderr should mention binary rebuilt
	if !strings.Contains(stderr, "Binary rebuilt") {
		t.Errorf("stderr should mention 'Binary rebuilt', got: %s", stderr)
	}
}

func TestAgentGenBuildFlag_FailureIsWarning(t *testing.T) {
	factoryRoot, afSourceRoot := setupFormulaFactoryWithAFSource(t)

	// Create a Makefile that fails
	os.WriteFile(filepath.Join(afSourceRoot, "Makefile"), []byte("install:\n\tfalse\n"), 0644)

	_, stderr, err := runFormulaAgentGenInDir(t, factoryRoot, "investigate", "--af-src", afSourceRoot, "--build")
	if err != nil {
		t.Fatalf("agent-gen should not fail when --build fails: %v\nstderr: %s", err, stderr)
	}

	// Stderr should contain WARNING
	if !strings.Contains(stderr, "WARNING") {
		t.Errorf("stderr should contain WARNING about build failure, got: %s", stderr)
	}

	// Template should still be written despite build failure
	tmplPath := filepath.Join(afSourceRoot, "internal", "templates", "roles", "investigate.md.tmpl")
	if _, err := os.Stat(tmplPath); err != nil {
		t.Error("template should still be written when --build fails")
	}
}

func TestAgentGenFallbackWarning(t *testing.T) {
	factoryRoot := setupFormulaFactory(t)

	// Remove go.mod from factory root to ensure it's not a valid AF source
	os.Remove(filepath.Join(factoryRoot, "go.mod"))

	// Clear all resolution sources
	agentGenAFSrc = ""
	compiledSourceRoot = ""
	t.Setenv("AF_SOURCE_ROOT", "")

	_, stderr, err := runFormulaAgentGenInDir(t, factoryRoot, "investigate")
	if err != nil {
		t.Fatalf("agent-gen failed: %v\nstderr: %s", err, stderr)
	}

	// Stderr should contain WARNING about skipping template write
	if !strings.Contains(stderr, "WARNING") {
		t.Errorf("stderr should contain WARNING about fallback, got: %s", stderr)
	}
	if !strings.Contains(stderr, "skipping template write") {
		t.Errorf("stderr should mention skipping template write, got: %s", stderr)
	}

	// Template should NOT be written when AF source is unavailable
	tmplPath := filepath.Join(factoryRoot, "internal", "templates", "roles", "investigate.md.tmpl")
	if _, err := os.Stat(tmplPath); err == nil {
		t.Error("template should NOT be written when AF source tree is unavailable")
	}
}

