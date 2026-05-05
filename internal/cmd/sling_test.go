package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// installMemStore replaces newIssueStore with a memstore factory for the
// lifetime of the test. Returns the shared memstore so tests can inspect
// the beads that instantiateFormulaWorkflow creates. The override is
// reverted via t.Cleanup.
func installMemStore(t *testing.T) *memstore.Store {
	t.Helper()
	store := memstore.New()
	orig := newIssueStore
	newIssueStore = func(wd, beadsDir, actor string) (issuestore.Store, error) {
		return store, nil
	}
	t.Cleanup(func() { newIssueStore = orig })
	return store
}

// installNoopLaunchSession replaces launchAgentSession with a no-op for the
// lifetime of the test. The real implementation blocks in
// tmux.WaitForCommand waiting for claude to write a readiness sentinel; on
// CI (and any env without claude on PATH) that never arrives and the test
// suite hits its global 4m timeout. Dispatch-path tests that exercise the
// full pipeline through instantiateFormulaWorkflow and beyond install this
// no-op so launchAgentSession returns nil instead of hanging. Tests that
// need to verify the "already running" pre-flight or explicitly want
// launch to be skipped via slingNoLaunch do NOT need this helper.
func installNoopLaunchSession(t *testing.T) {
	t.Helper()
	orig := launchAgentSession
	launchAgentSession = func(*cobra.Command, string, string, string, string) error {
		return nil
	}
	t.Cleanup(func() { launchAgentSession = orig })
}

// installFailingIssueStore replaces newIssueStore with a factory that always
// returns an error. Used by tests that want to exercise the pre-store failure
// path (e.g., the --reset cleanup-before-error ordering) without depending on
// whether a live backend is available. In integration mode the real
// mcpstore would lazy-start a server and let Create succeed, masking the
// ordering bug these tests are meant to pin.
func installFailingIssueStore(t *testing.T) {
	t.Helper()
	orig := newIssueStore
	newIssueStore = func(wd, beadsDir, actor string) (issuestore.Store, error) {
		return nil, errors.New("issuestore disabled for test")
	}
	t.Cleanup(func() { newIssueStore = orig })
}

// killStaleTmuxSession kills a tmux session that may have leaked from a prior
// go test process (e.g., a crash mid-test or a real Claude launch that
// outlived its t.Cleanup). Without this, the pre-flight "already running"
// guard in dispatchToSpecialist fires before the test can do anything. Safe
// to call when tmux is absent or the session does not exist.
func killStaleTmuxSession(t *testing.T, name string) {
	t.Helper()
	_ = exec.Command("tmux", "kill-session", "-t", name).Run()
}

func TestParseCLIVars(t *testing.T) {
	tests := []struct {
		name    string
		input   []string
		want    map[string]string
		wantErr bool
	}{
		{
			name:  "empty",
			input: nil,
			want:  map[string]string{},
		},
		{
			name:  "single var",
			input: []string{"key=val"},
			want:  map[string]string{"key": "val"},
		},
		{
			name:  "multiple vars",
			input: []string{"a=1", "b=2", "c=three"},
			want:  map[string]string{"a": "1", "b": "2", "c": "three"},
		},
		{
			name:  "value with equals",
			input: []string{"expr=a=b"},
			want:  map[string]string{"expr": "a=b"},
		},
		{
			name:    "missing equals",
			input:   []string{"noequals"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := parseCLIVars(tt.input)
			if (err != nil) != tt.wantErr {
				t.Fatalf("parseCLIVars() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil {
				return
			}
			if len(got) != len(tt.want) {
				t.Fatalf("parseCLIVars() len = %d, want %d", len(got), len(tt.want))
			}
			for k, v := range tt.want {
				if got[k] != v {
					t.Errorf("parseCLIVars()[%q] = %q, want %q", k, got[k], v)
				}
			}
		})
	}
}

func TestExpandStepVars(t *testing.T) {
	// Import the formula package via the expandStepVars function
	// which operates on formula.Formula types
	// This test verifies the integration between CLI var parsing
	// and formula variable expansion.

	vars, err := parseCLIVars([]string{"name=Alice", "count=5"})
	if err != nil {
		t.Fatal(err)
	}
	if vars["name"] != "Alice" {
		t.Errorf("expected name=Alice, got %q", vars["name"])
	}
	if vars["count"] != "5" {
		t.Errorf("expected count=5, got %q", vars["count"])
	}
}

func TestDetectAgentName(t *testing.T) {
	t.Setenv("AF_ROLE", "")
	// resolveAgentName requires a loadable agents.json to validate a
	// path-derived name (GitHub issue #89). Build a real factory fixture.
	root, managerDir := setupFactoryFixture(t, "manager")
	nestedDir := filepath.Join(managerDir, "src", "lib")
	if err := os.MkdirAll(nestedDir, 0o755); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name    string
		wd      string
		want    string
		wantErr bool
	}{
		{
			name: "direct child",
			wd:   managerDir,
			want: "manager",
		},
		{
			name: "nested path",
			wd:   nestedDir,
			want: "manager",
		},
		{
			name:    "same as root",
			wd:      root,
			want:    "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := detectAgentName(tt.wd, root)
			if (err != nil) != tt.wantErr {
				t.Fatalf("detectAgentName() error = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Errorf("detectAgentName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestDetectAgentName_WorktreeAgent verifies that detectAgentName resolves
// the agent name correctly when cwd is inside a worktree. This requires real
// temp dirs because the fix calls FindLocalRoot(wd) internally.
func TestDetectAgentName_WorktreeAgent(t *testing.T) {
	factoryRoot := t.TempDir()

	// Set up factory config
	afDir := filepath.Join(factoryRoot, ".agentfactory")
	os.MkdirAll(afDir, 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0o644)
	// agents.json with "solver" so resolveAgentName can validate membership.
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"solver":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	// Set up worktree structure
	wtRoot := filepath.Join(afDir, "worktrees", "wt-test")
	wtAfDir := filepath.Join(wtRoot, ".agentfactory")
	wtAgentDir := filepath.Join(wtAfDir, "agents", "solver")
	os.MkdirAll(wtAgentDir, 0o755)
	os.WriteFile(filepath.Join(wtAfDir, ".factory-root"), []byte(factoryRoot), 0o644)

	// detectAgentName is called with factoryRoot — the current code fails
	// because the relative path yields parts[1]=="worktrees". After the fix,
	// detectAgentName computes localRoot internally and tries it first.
	got, err := detectAgentName(wtAgentDir, factoryRoot)
	if err != nil {
		t.Fatalf("detectAgentName from worktree agent dir: %v", err)
	}
	if got != "solver" {
		t.Errorf("detectAgentName = %q, want %q", got, "solver")
	}
}

// TestDetectAgentName_WrongButNoError_HonorsAF_ROLE pins the fix for GitHub
// issue #88 at the detectAgentName boundary. A cwd at a typo directory
// (exists on disk, not in agents.json) would otherwise silently flow into
// done.go's WORK_DONE mail recipient and sling.go's formula resolve context.
func TestDetectAgentName_WrongButNoError_HonorsAF_ROLE(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "solver")

	got, err := detectAgentName(typoDir, factoryRoot)
	if err != nil {
		t.Fatalf("detectAgentName with AF_ROLE fallback: %v", err)
	}
	if got != "solver" {
		t.Errorf("detectAgentName = %q, want %q (AF_ROLE overrides wrong path-derived name)", got, "solver")
	}
}

// TestDetectAgentName_WrongButNoError_NoAF_ROLE_Errors verifies that without
// AF_ROLE, detectAgentName errors rather than silently returning the wrong
// name. This closes the done.go:322 / sling.go:326 silent-acceptance defect.
func TestDetectAgentName_WrongButNoError_NoAF_ROLE_Errors(t *testing.T) {
	factoryRoot, _ := setupFactoryFixture(t, "solver")

	typoDir := filepath.Join(factoryRoot, ".agentfactory", "agents", "typo")
	if err := os.MkdirAll(typoDir, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_ROLE", "")

	got, err := detectAgentName(typoDir, factoryRoot)
	if err == nil {
		t.Fatalf("detectAgentName should error for unknown agent, got %q", got)
	}
	if got == "typo" {
		t.Errorf("detectAgentName must not return wrong path-derived name %q silently", got)
	}
}

func TestStepInfo(t *testing.T) {
	// stepInfo needs a real formula.Formula, but since it's tested via
	// integration, we just verify the function signature compiles and the
	// fallback case works.
	// Direct unit testing of stepInfo with formula types is covered by
	// the formula package's own tests.
}

func TestValidateDispatchArgs_Errors(t *testing.T) {
	tests := []struct {
		name    string
		formula string
		agent   string
		args    []string
		wantErr string
	}{
		{
			name:    "neither formula nor agent",
			formula: "",
			agent:   "",
			args:    nil,
			wantErr: "--formula is required",
		},
		{
			name:    "agent without task",
			formula: "",
			agent:   "ultraimplement",
			args:    nil,
			wantErr: "task description required",
		},
		{
			name:    "agent with empty task",
			formula: "",
			agent:   "ultraimplement",
			args:    []string{""},
			wantErr: "task description required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSlingArgs(tt.formula, tt.agent, tt.args)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err.Error(), tt.wantErr)
			}
		})
	}
}

func TestValidateDispatchArgs_ValidCases(t *testing.T) {
	tests := []struct {
		name    string
		formula string
		agent   string
		args    []string
	}{
		{
			name:    "formula provided",
			formula: "my-formula",
			agent:   "",
			args:    nil,
		},
		{
			name:    "agent with task",
			formula: "",
			agent:   "ultraimplement",
			args:    []string{"implement issue #42"},
		},
		{
			name:    "both formula and agent",
			formula: "my-formula",
			agent:   "ultraimplement",
			args:    nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateSlingArgs(tt.formula, tt.agent, tt.args)
			if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}

func TestPersistFormulaCaller_NoOverwrite(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "specialist")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	// First write: dispatcher identity
	persistFormulaCaller(agentDir, "manager")

	data, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "formula_caller"))
	if err != nil {
		t.Fatalf("reading formula_caller: %v", err)
	}
	if string(data) != "manager" {
		t.Errorf("formula_caller = %q, want %q", string(data), "manager")
	}

	// Second write (re-dispatch): should NOT overwrite
	persistFormulaCaller(agentDir, "specialist")

	data, err = os.ReadFile(filepath.Join(agentDir, ".runtime", "formula_caller"))
	if err != nil {
		t.Fatalf("reading formula_caller after second write: %v", err)
	}
	if string(data) != "manager" {
		t.Errorf("formula_caller should not be overwritten, got %q, want %q", string(data), "manager")
	}
}

func TestResolveSpecialistAgent(t *testing.T) {
	// Set up a temp factory with agents.json
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	os.MkdirAll(filepath.Join(configDir, "agents"), 0o755)

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"ultraimplement": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "factoryworker",
			},
			"manager": map[string]interface{}{
				"type":        "interactive",
				"description": "Not a specialist",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(configDir, "agents.json"), data, 0o644)

	// Test: specialist agent resolves successfully
	entry, err := resolveSpecialistAgent(dir, "ultraimplement")
	if err != nil {
		t.Fatalf("resolveSpecialistAgent(ultraimplement): %v", err)
	}
	if entry.Formula != "factoryworker" {
		t.Errorf("formula = %q, want %q", entry.Formula, "factoryworker")
	}

	// Test: non-specialist errors
	_, err = resolveSpecialistAgent(dir, "manager")
	if err == nil {
		t.Fatal("expected error for non-specialist agent")
	}
	if !strings.Contains(err.Error(), "not a specialist") {
		t.Errorf("error = %q, want it to contain 'not a specialist'", err.Error())
	}

	// Test: unknown agent errors
	_, err = resolveSpecialistAgent(dir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown agent")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error = %q, want it to contain 'not found'", err.Error())
	}
}

func TestPersistFormulaCaller_StaleClearAndRewrite(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "specialist")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	// Simulate previous dispatch: write caller A
	persistFormulaCaller(agentDir, "old-manager")

	// Simulate new dispatch: remove stale file, then write caller B
	os.Remove(filepath.Join(agentDir, ".runtime", "formula_caller"))
	persistFormulaCaller(agentDir, "new-manager")

	data, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "formula_caller"))
	if err != nil {
		t.Fatalf("reading formula_caller: %v", err)
	}
	if string(data) != "new-manager" {
		t.Errorf("formula_caller = %q, want %q after stale clear and rewrite", string(data), "new-manager")
	}
}

func TestValidateDispatchArgs_WhitespaceOnlyTask(t *testing.T) {
	err := validateSlingArgs("", "ultraimplement", []string{"   "})
	if err == nil {
		t.Fatal("expected error for whitespace-only task")
	}
	if !strings.Contains(err.Error(), "task description required") {
		t.Errorf("error = %q, want it to contain 'task description required'", err.Error())
	}
}

func TestResolveSpecialistAgent_ConfigLoadFailure(t *testing.T) {
	_, err := resolveSpecialistAgent("/nonexistent-factory-root-12345", "someagent")
	if err == nil {
		t.Fatal("expected error for non-existent config")
	}
	if !strings.Contains(err.Error(), "loading agents config") {
		t.Errorf("error = %q, want it to contain 'loading agents config'", err.Error())
	}
}

func TestEnsureCallerIdentity_WithRole(t *testing.T) {
	dir := t.TempDir()
	root := dir
	agentDir := filepath.Join(dir, ".agentfactory", "agents", "target-agent")
	callerDir := filepath.Join(dir, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(agentDir, 0o755)
	os.MkdirAll(callerDir, 0o755)

	// detectRole needs .agentfactory/agents.json to resolve the caller
	configDir := filepath.Join(dir, ".agentfactory")
	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"caller-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test caller",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(configDir, "agents.json"), data, 0o644)

	var buf bytes.Buffer
	role := ensureCallerIdentity(callerDir, root, agentDir, &buf)

	if role != "caller-agent" {
		t.Errorf("ensureCallerIdentity() = %q, want 'caller-agent'", role)
	}
	if buf.Len() != 0 {
		t.Errorf("should not emit warning when caller is detected, got: %s", buf.String())
	}
}

func TestEnsureCallerIdentity_FromFactoryRoot(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, ".agentfactory", "agents", "target-agent")
	os.MkdirAll(agentDir, 0o755)

	var buf bytes.Buffer
	// callerWd == root → detectRole returns empty
	role := ensureCallerIdentity(dir, dir, agentDir, &buf)

	if role != "@cli" {
		t.Errorf("ensureCallerIdentity() = %q, want '@cli'", role)
	}
	if !strings.Contains(buf.String(), "warning") {
		t.Errorf("should emit warning when dispatching from factory root, got: %q", buf.String())
	}

	// Verify @cli was persisted
	data, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "formula_caller"))
	if err != nil {
		t.Fatalf("formula_caller not created: %v", err)
	}
	if string(data) != "@cli" {
		t.Errorf("formula_caller = %q, want '@cli'", string(data))
	}
}

func TestFormulaUsesBeadSources(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]formula.Var
		want bool
	}{
		{
			name: "no bead sources",
			vars: map[string]formula.Var{
				"x": {Source: "cli"},
				"y": {Source: "env"},
			},
			want: false,
		},
		{
			name: "has hook_bead",
			vars: map[string]formula.Var{
				"issue": {Source: "hook_bead"},
			},
			want: true,
		},
		{
			name: "has bead_title",
			vars: map[string]formula.Var{
				"title": {Source: "bead_title"},
			},
			want: true,
		},
		{
			name: "has bead_description",
			vars: map[string]formula.Var{
				"desc": {Source: "bead_description"},
			},
			want: true,
		},
		{
			name: "mixed sources with bead",
			vars: map[string]formula.Var{
				"x":     {Source: "cli"},
				"issue": {Source: "hook_bead"},
			},
			want: true,
		},
		{
			name: "empty vars",
			vars: map[string]formula.Var{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formulaUsesBeadSources(tt.vars)
			if got != tt.want {
				t.Errorf("formulaUsesBeadSources() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestInstantiateFormulaWorkflow_ParamsAndSignature(t *testing.T) {
	// This test verifies that:
	// 1. InstantiateParams struct exists with the required fields
	// 2. instantiateFormulaWorkflow exists with the correct signature
	// 3. The function does not read package globals

	// Construct params — this verifies the struct exists with the right fields
	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "nonexistent-formula",
		CLIVars:     []string{"key=val"},
		AgentName:   "test-agent",
		Root:        "/tmp/fake-root",
		WorkDir:     "/tmp/fake-root/.agentfactory/agents/test-agent",
	}

	var buf bytes.Buffer

	// Call the function — it should fail at FindFormulaFile (formula not found)
	// because we gave it a non-existent root. This proves the function exists
	// and the pre-formula-discovery wiring works.
	instanceID, stepIDs, agentName, err := instantiateFormulaWorkflow(params, &buf)

	// We expect an error from FindFormulaFile
	if err == nil {
		t.Fatal("expected error from instantiateFormulaWorkflow with fake paths")
	}
	if !strings.Contains(err.Error(), "finding formula") {
		t.Errorf("expected 'finding formula' error, got: %v", err)
	}

	// On error, return values should be zero
	if instanceID != "" {
		t.Errorf("instanceID should be empty on error, got %q", instanceID)
	}
	if stepIDs != nil {
		t.Errorf("stepIDs should be nil on error, got %v", stepIDs)
	}
	// agentName may or may not be resolved before the error — it's set from params
	_ = agentName
}

func TestInstantiateFormulaWorkflow_NoGlobals(t *testing.T) {
	// Verify the function uses params, not globals.
	// Set globals to known values, pass DIFFERENT values via params.
	// If the function reads globals, the error message will reference the global value.

	origFormula := slingFormulaName
	origVars := slingVars
	origAgent := slingAgent
	defer func() {
		slingFormulaName = origFormula
		slingVars = origVars
		slingAgent = origAgent
	}()

	slingFormulaName = "global-formula-name"
	slingVars = []string{"global=true"}
	slingAgent = "global-agent"

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "params-formula-name",
		CLIVars:     []string{"params=true"},
		AgentName:   "params-agent",
		Root:        "/tmp/fake-root",
		WorkDir:     "/tmp/fake-root/.agentfactory/agents/params-agent",
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)

	if err == nil {
		t.Fatal("expected error")
	}

	// The error should reference "params-formula-name" (from params), not "global-formula-name" (from global)
	errMsg := err.Error()
	if strings.Contains(errMsg, "global-formula-name") {
		t.Error("instantiateFormulaWorkflow is reading slingFormulaName global instead of params.FormulaName")
	}
}

// createTestFormulaFactory sets up a temp factory with .agentfactory/factory.json
// and a minimal formula TOML, returning (root, agentDir).
func createTestFormulaFactory(t *testing.T, formulaName, agentName string) (string, string) {
	t.Helper()
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	os.MkdirAll(filepath.Join(afDir, "agents"), 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"name":"test"}`), 0o644)
	// agents.json so resolveAgentName can validate the path-derived name
	// (GitHub issue #89 — membership gate treats unloadable config as failure).
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"`+agentName+`":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	formulaDir := filepath.Join(root, ".beads", "formulas")
	os.MkdirAll(formulaDir, 0o755)
	toml := `
formula = "` + formulaName + `"
type = "workflow"
version = 1
[[steps]]
id = "step1"
title = "Step 1"
`
	os.WriteFile(filepath.Join(formulaDir, formulaName+".formula.toml"), []byte(toml), 0o644)

	agentDir := filepath.Join(afDir, "agents", agentName)
	os.MkdirAll(agentDir, 0o755)
	return root, agentDir
}

func TestInstantiateFormulaWorkflow_AgentNameResolution(t *testing.T) {
	// When AgentName is provided, it should be returned as-is.
	// Uses a real formula so the function gets past FindFormulaFile/ParseFile.
	root, agentDir := createTestFormulaFactory(t, "test-formula", "explicit-agent")

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-formula",
		AgentName:   "explicit-agent",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	// Will fail at store.Create (expected) but agentName is resolved before that
	_, _, agentName, _ := instantiateFormulaWorkflow(params, &buf)

	if agentName != "explicit-agent" {
		t.Errorf("agentName = %q, want 'explicit-agent'", agentName)
	}
}

func TestInstantiateFormulaWorkflow_AgentNameFallback(t *testing.T) {
	// When AgentName is empty, it should be inferred from WorkDir relative to Root.
	root, agentDir := createTestFormulaFactory(t, "test-formula", "inferred-agent")

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-formula",
		AgentName:   "", // empty — should be inferred
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, _, agentName, _ := instantiateFormulaWorkflow(params, &buf)

	if agentName != "inferred-agent" {
		t.Errorf("agentName = %q, want 'inferred-agent'", agentName)
	}
}

func TestDispatchToSpecialist_UsesFormulaInstantiation(t *testing.T) {
	killStaleTmuxSession(t, "af-specialist-agent")
	installMemStore(t)
	installNoopLaunchSession(t)
	// Set up factory with specialist agent + matching formula TOML
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	// Clean up any tmux session that may be created during the test
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	err := dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "implement issue #42")

	hookedPath := filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "hooked_formula")

	if err != nil {
		// If the function errored, it should be from the formula instantiation path
		// (e.g., store.Create failure), not from tmux setup.
		errMsg := err.Error()
		if !strings.Contains(errMsg, "formula") && !strings.Contains(errMsg, "bead") {
			t.Errorf("expected error from formula instantiation path, got: %s", errMsg)
		}
		return
	}

	// Function succeeded — .runtime/hooked_formula MUST exist, proving
	// instantiateFormulaWorkflow ran (it calls persistFormulaInstanceID).
	// Before the fix: function uses SetInitialPrompt, never calls
	// instantiateFormulaWorkflow, so hooked_formula is never written.
	if _, statErr := os.Stat(hookedPath); statErr != nil {
		t.Error(".runtime/hooked_formula should exist after specialist dispatch (proves formula instantiation ran)")
	}
}

func TestDispatchToSpecialist_CallerIdentityPreserved(t *testing.T) {
	// Verify stale caller removal and fresh caller identity persist through
	// the rewritten dispatch path.
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	installNoopLaunchSession(t)
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
			"dispatcher-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Dispatcher",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	agentDir := filepath.Join(root, ".agentfactory", "agents", "specialist-agent")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)

	// Write stale caller from a previous dispatch
	os.WriteFile(filepath.Join(runtimeDir, "formula_caller"), []byte("old-caller"), 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "dispatcher-agent")
	os.MkdirAll(callerWd, 0o755)

	// Call will error at store.Create or tmux, but caller identity should already be set
	_ = dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "some task")

	callerData, err := os.ReadFile(filepath.Join(runtimeDir, "formula_caller"))
	if err != nil {
		t.Fatalf("formula_caller should exist: %v", err)
	}
	if string(callerData) == "old-caller" {
		t.Error("stale formula_caller was not replaced")
	}
	if string(callerData) != "dispatcher-agent" {
		t.Errorf("formula_caller = %q, want 'dispatcher-agent'", string(callerData))
	}
}

func TestDispatchToSpecialist_TaskInBeadDescription(t *testing.T) {
	// Verify that TaskDescription appends to the formula's description
	// inside instantiateFormulaWorkflow before the parent bead is created.
	store := installMemStore(t)
	root, agentDir := createTestFormulaFactory(t, "test-formula", "test-agent")

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     "test-formula",
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		TaskDescription: "implement issue #42",
	}

	var buf bytes.Buffer
	instanceID, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	parent, err := store.Get(t.Context(), instanceID)
	if err != nil {
		t.Fatalf("store.Get(parent): %v", err)
	}
	if !strings.Contains(parent.Description, "Dispatch task: implement issue #42") {
		t.Errorf("parent description missing dispatch task, got: %q", parent.Description)
	}
}

func TestDispatchToSpecialist_TaskDescriptionEmpty(t *testing.T) {
	// Verify that when TaskDescription is empty (formula path),
	// the description is NOT modified.
	store := installMemStore(t)
	root, agentDir := createTestFormulaFactory(t, "test-formula", "test-agent")

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-formula",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	instanceID, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	parent, err := store.Get(t.Context(), instanceID)
	if err != nil {
		t.Fatalf("store.Get(parent): %v", err)
	}
	if strings.Contains(parent.Description, "Dispatch task:") {
		t.Errorf("formula path should not append dispatch task, got: %q", parent.Description)
	}
}

// createTestFormulaFactoryWithTOML sets up a temp factory with a custom formula TOML,
// returning (root, agentDir).
func createTestFormulaFactoryWithTOML(t *testing.T, formulaName, agentName, toml string) (string, string) {
	t.Helper()
	root := t.TempDir()
	afDir := filepath.Join(root, ".agentfactory")
	os.MkdirAll(filepath.Join(afDir, "agents"), 0o755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{"name":"test"}`), 0o644)
	// agents.json so resolveAgentName can validate the path-derived name
	// (GitHub issue #89 — membership gate treats unloadable config as failure).
	os.WriteFile(filepath.Join(afDir, "agents.json"),
		[]byte(`{"agents":{"`+agentName+`":{"type":"autonomous","description":"test agent"}}}`), 0o644)

	formulaDir := filepath.Join(root, ".beads", "formulas")
	os.MkdirAll(formulaDir, 0o755)
	os.WriteFile(filepath.Join(formulaDir, formulaName+".formula.toml"), []byte(toml), 0o644)

	agentDir := filepath.Join(afDir, "agents", agentName)
	os.MkdirAll(agentDir, 0o755)
	return root, agentDir
}

func TestInstantiateFormulaWorkflow_InputsMerged(t *testing.T) {
	// Formula with [inputs] section — verify input vars expand in step descriptions.
	store := installMemStore(t)
	toml := `
formula = "test-inputs"
type = "workflow"
version = 1

[inputs.issue]
description = "Issue ID"
type = "string"
required = false
default = "bd-99"

[[steps]]
id = "step1"
title = "Fix {{issue}}"
description = "Working on {{issue}}"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-inputs", "test-agent", toml)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-inputs",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
		CLIVars:     []string{"issue=bd-42"},
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	// The CLI var issue=bd-42 should have been resolved and expanded into the step.
	stepBeadID, ok := stepIDs["step1"]
	if !ok {
		t.Fatal("stepIDs missing step1")
	}
	step, err := store.Get(t.Context(), stepBeadID)
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Title, "bd-42") {
		t.Errorf("step title should contain bd-42, got: %q", step.Title)
	}
	if !strings.Contains(step.Description, "bd-42") {
		t.Errorf("step description should contain bd-42, got: %q", step.Description)
	}
}

func TestInstantiateFormulaWorkflow_OrchestratorInjected(t *testing.T) {
	// Verify {{orchestrator}} expands to caller identity in step descriptions.
	store := installMemStore(t)
	toml := `
formula = "test-orchestrator"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Dispatched by {{orchestrator}}"
description = "Orchestrator is {{orchestrator}}"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-orchestrator", "test-agent", toml)

	params := InstantiateParams{
		Ctx:            t.Context(),
		FormulaName:    "test-orchestrator",
		AgentName:      "test-agent",
		Root:           root,
		WorkDir:        agentDir,
		CallerIdentity: "manager-agent",
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	stepBeadID, ok := stepIDs["step1"]
	if !ok {
		t.Fatal("stepIDs missing step1")
	}
	step, err := store.Get(t.Context(), stepBeadID)
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Title, "manager-agent") {
		t.Errorf("step title should contain 'manager-agent', got: %q", step.Title)
	}
	if !strings.Contains(step.Description, "manager-agent") {
		t.Errorf("step description should contain 'manager-agent', got: %q", step.Description)
	}
}

func TestInstantiateFormulaWorkflow_OrchestratorExpandsDirect(t *testing.T) {
	// Directly verify {{orchestrator}} expands by testing expandStepVars
	// with orchestrator in the vars map.
	f := &formula.Formula{
		Steps: []formula.Step{
			{ID: "s1", Title: "Run by {{orchestrator}}", Description: "Caller: {{orchestrator}}"},
		},
	}
	vars := map[string]string{"orchestrator": "supervisor"}
	expandStepVars(f, vars)

	if f.Steps[0].Title != "Run by supervisor" {
		t.Errorf("title = %q, want 'Run by supervisor'", f.Steps[0].Title)
	}
	if f.Steps[0].Description != "Caller: supervisor" {
		t.Errorf("description = %q, want 'Caller: supervisor'", f.Steps[0].Description)
	}
}

func TestInstantiateFormulaWorkflow_DeferredVarsSurvive(t *testing.T) {
	// Verify {{deferred_var}} remains literal in bead descriptions
	// (deferred vars are not in the resolved map, so they survive expansion).
	store := installMemStore(t)
	toml := `
formula = "test-deferred"
type = "workflow"
version = 1

[vars.myvar]
source = "cli"
description = "A normal var"

[vars.deferred_var]
source = "deferred"
description = "A deferred var"

[[steps]]
id = "step1"
title = "Normal: {{myvar}}, Deferred: {{deferred_var}}"
description = "Check deferred survival"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-deferred", "test-agent", toml)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-deferred",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
		CLIVars:     []string{"myvar=hello"},
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	stepBeadID, ok := stepIDs["step1"]
	if !ok {
		t.Fatal("stepIDs missing step1")
	}
	step, err := store.Get(t.Context(), stepBeadID)
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Title, "Normal: hello") {
		t.Errorf("step title should contain 'Normal: hello', got: %q", step.Title)
	}
	if !strings.Contains(step.Title, "{{deferred_var}}") {
		t.Errorf("step title should preserve {{deferred_var}} literal, got: %q", step.Title)
	}
}

func TestInstantiateFormulaWorkflow_ConvoySkipsMerge(t *testing.T) {
	// Convoy formula does NOT call MergeInputsToVars.
	// A convoy with inputs that would collide with vars should NOT error
	// (because merge is skipped for non-workflow types).
	toml := `
formula = "test-convoy"
type = "convoy"
version = 1

[inputs.issue]
description = "Issue ID"
type = "string"
required = false

[vars.issue]
source = "cli"
description = "Same name as input — would collide if merge ran"

[[legs]]
id = "leg1"
title = "Investigate"
description = "Investigate the issue"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-convoy", "test-agent", toml)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-convoy",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)

	// Should NOT get "merging inputs to vars" error (merge skipped for convoy)
	if err != nil && strings.Contains(err.Error(), "merging inputs to vars") {
		t.Errorf("convoy should skip MergeInputsToVars, got merge error: %v", err)
	}
	// It should fail at store.Create or topo sort, not at merge
}

func TestDispatchToSpecialist_CallerIdentityPassedThrough(t *testing.T) {
	// Verify CallerIdentity flows from dispatch to workflow via InstantiateParams.
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	killStaleTmuxSession(t, "af-specialist-agent")
	installNoopLaunchSession(t)
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
			"dispatcher-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Dispatcher",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "dispatcher-agent")
	os.MkdirAll(callerWd, 0o755)

	// dispatch will call ensureCallerIdentity which persists the caller
	_ = dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "some task")

	// Verify dispatcher-agent was persisted as caller identity
	callerData, err := os.ReadFile(filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "formula_caller"))
	if err != nil {
		t.Fatalf("formula_caller should exist: %v", err)
	}
	if string(callerData) != "dispatcher-agent" {
		t.Errorf("formula_caller = %q, want 'dispatcher-agent'", string(callerData))
	}
}

func TestResetFlag_Registered(t *testing.T) {
	f := slingCmd.Flags().Lookup("reset")
	if f == nil {
		t.Fatal("--reset flag should be registered on sling command")
	}
	if f.DefValue != "false" {
		t.Errorf("--reset default should be false, got %q", f.DefValue)
	}
	if !strings.Contains(f.Usage, "Stop target agent") {
		t.Errorf("--reset usage should mention 'Stop target agent', got %q", f.Usage)
	}
}

func TestSlingLongDescription_MentionsSuccession(t *testing.T) {
	if !strings.Contains(strings.ToLower(slingCmd.Long), "succession") {
		t.Errorf("slingCmd.Long should mention formula succession, got %q", slingCmd.Long)
	}
}

func TestDispatchToSpecialist_ResetCleansRuntimeFiles(t *testing.T) {
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	// Create stale runtime files that --reset should clean
	agentDir := filepath.Join(root, ".agentfactory", "agents", "specialist-agent")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "session_id"), []byte("stale-session"), 0o644)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("stale-formula"), 0o644)
	os.WriteFile(filepath.Join(agentDir, ".agent-checkpoint.json"), []byte("{}"), 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	// Set --reset global
	origReset := slingReset
	slingReset = true
	defer func() { slingReset = origReset }()

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	// Force newIssueStore to return an error so that dispatchToSpecialist
	// bails out of instantiateFormulaWorkflow BEFORE persistFormulaInstanceID
	// rewrites hooked_formula. This pins the ordering invariant (reset
	// cleanup MUST happen before later failures) independently of whether
	// a live backend is available. Without this, the test is a lie in
	// integration mode: mcpstore lazy-starts a server and Create succeeds.
	installFailingIssueStore(t)

	// dispatchToSpecialist will fail at newIssueStore, but reset cleanup
	// should happen BEFORE that.
	_ = dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "implement issue #38")

	// Verify stale files were cleaned
	if _, err := os.Stat(filepath.Join(runtimeDir, "session_id")); err == nil {
		t.Error(".runtime/session_id should be deleted by --reset")
	}
	if _, err := os.Stat(filepath.Join(runtimeDir, "hooked_formula")); err == nil {
		t.Error(".runtime/hooked_formula should be deleted by --reset")
	}
	if _, err := os.Stat(filepath.Join(agentDir, ".agent-checkpoint.json")); err == nil {
		t.Error(".agent-checkpoint.json should be deleted by --reset")
	}
}

func TestDispatchToSpecialist_RunningAgentWithoutResetErrors(t *testing.T) {
	// When target agent has a running tmux session and --reset is false,
	// dispatchToSpecialist should return an error BEFORE creating beads.
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	// Create a tmux session to simulate a running agent
	sessionName := "af-specialist-agent"
	createErr := exec.Command("tmux", "new-session", "-d", "-s", sessionName).Run()
	if createErr != nil {
		t.Skip("tmux not available, skipping integration test")
	}
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	// Set --reset to false
	origReset := slingReset
	origNoLaunch := slingNoLaunch
	slingReset = false
	slingNoLaunch = false
	defer func() {
		slingReset = origReset
		slingNoLaunch = origNoLaunch
	}()

	err := dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "some task")
	if err == nil {
		t.Fatal("expected error when dispatching to running agent without --reset")
	}
	if !strings.Contains(err.Error(), "already running") {
		t.Errorf("error should contain 'already running', got: %s", err.Error())
	}
	if !strings.Contains(err.Error(), "--reset") {
		t.Errorf("error should mention '--reset', got: %s", err.Error())
	}

	// Verify no bead was created (error fired BEFORE instantiation)
	hookedPath := filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "hooked_formula")
	if _, statErr := os.Stat(hookedPath); statErr == nil {
		t.Error("hooked_formula should NOT exist — error should fire before bead creation")
	}
}

func TestDispatchToSpecialist_ResetWithNonRunningAgent(t *testing.T) {
	// --reset should succeed even when agent is not running.
	// mgr.Stop() returns ErrNotRunning, which is explicitly ignored.
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	// Create stale runtime files
	agentDir := filepath.Join(root, ".agentfactory", "agents", "specialist-agent")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "session_id"), []byte("stale"), 0o644)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("stale"), 0o644)
	os.WriteFile(filepath.Join(agentDir, ".agent-checkpoint.json"), []byte("{}"), 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	origReset := slingReset
	slingReset = true
	defer func() { slingReset = origReset }()

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	// Same reasoning as TestDispatchToSpecialist_ResetCleansRuntimeFiles:
	// force an error at the store boundary so dispatch bails out BEFORE
	// persistFormulaInstanceID rewrites hooked_formula.
	installFailingIssueStore(t)

	// No tmux session exists — agent is NOT running. Reset should still proceed.
	err := dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "test task")

	// Should NOT return an error about "already running" or "not running"
	if err != nil && strings.Contains(err.Error(), "not running") {
		t.Errorf("--reset should not error when agent is not running, got: %s", err.Error())
	}

	// Stale files should still be cleaned even though agent wasn't running
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "session_id")); statErr == nil {
		t.Error(".runtime/session_id should be deleted by --reset even when agent not running")
	}
	if _, statErr := os.Stat(filepath.Join(runtimeDir, "hooked_formula")); statErr == nil {
		t.Error(".runtime/hooked_formula should be deleted by --reset even when agent not running")
	}
	if _, statErr := os.Stat(filepath.Join(agentDir, ".agent-checkpoint.json")); statErr == nil {
		t.Error(".agent-checkpoint.json should be deleted by --reset even when agent not running")
	}

	// stderr should NOT contain "failed to stop" warning (ErrNotRunning is expected, not a warning)
	if strings.Contains(buf.String(), "failed to stop") {
		t.Error("ErrNotRunning should not produce a warning")
	}
}

func TestDispatchToSpecialist_NoLaunchSkipsSession(t *testing.T) {
	// Verify that --no-launch is now valid for specialist dispatch.
	// With --no-launch, formula instantiation runs but session launch is skipped.
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	// Set --no-launch global
	origNoLaunch := slingNoLaunch
	slingNoLaunch = true
	defer func() { slingNoLaunch = origNoLaunch }()

	err := dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "implement issue #42")

	// Should complete without trying to launch a session.
	// If bd is available, this succeeds. If not, it errors at store.Create.
	if err != nil {
		// Even if it errors, the error should be from formula path, not --no-launch rejection
		errMsg := err.Error()
		if strings.Contains(errMsg, "no-launch") {
			t.Errorf("--no-launch should be valid for specialist dispatch, got: %s", errMsg)
		}
	}

	// Verify no tmux session was created (--no-launch should skip session launch)
	output := buf.String()
	if strings.Contains(output, "Launched") {
		t.Error("session should not be launched when --no-launch is set")
	}
}

func TestDispatchToSpecialist_VarForwarding(t *testing.T) {
	// Verify that user-provided --var flags (slingVars) are forwarded to
	// InstantiateParams.CLIVars in the specialist dispatch path, not dropped.
	// The formula has a required cli-sourced {{issue}} var and a step whose
	// title interpolates it, so the only way the step bead ends up titled
	// "Do work on bd-42" is if slingVars made it through to cliVars and
	// through variable resolution.
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	store := installMemStore(t)
	installNoopLaunchSession(t)

	toml := `
formula = "var-forward-test"
type = "workflow"
version = 1

[vars.issue]
description = "Issue bead ID"
source = "cli"
required = true

[[steps]]
id = "step-1"
title = "Do work on {{issue}}"
description = "Working on {{issue}}"
`
	root, _ := createTestFormulaFactoryWithTOML(t, "var-forward-test", "var-fwd-agent", toml)

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"var-fwd-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test var forwarding",
				"formula":     "var-forward-test",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	// Save and restore global slingVars
	origVars := slingVars
	slingVars = []string{"issue=bd-42"}
	t.Cleanup(func() { slingVars = origVars })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	if err := dispatchToSpecialist(cmd, root, callerWd, "var-fwd-agent", "implement issue #42"); err != nil {
		t.Fatalf("dispatchToSpecialist: %v", err)
	}

	// Verify the step bead's title contains the resolved variable. If
	// slingVars was dropped, variable resolution would have errored with
	// "resolving variables" before reaching bead creation; if it was
	// forwarded but the substitution failed, the title would still contain
	// the literal "{{issue}}" placeholder.
	issues, err := store.List(t.Context(), issuestore.Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	var found bool
	for _, iss := range issues {
		if strings.Contains(iss.Title, "Do work on bd-42") {
			found = true
			break
		}
	}
	if !found {
		var titles []string
		for _, iss := range issues {
			titles = append(titles, iss.Title)
		}
		t.Errorf("no bead titled 'Do work on bd-42' — slingVars not forwarded or {{issue}} not resolved; got titles: %v", titles)
	}
}

func TestWriteDispatchedMarker(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "specialist")
	os.MkdirAll(agentDir, 0o755)

	writeDispatchedMarker(agentDir, "manager")

	data, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "dispatched"))
	if err != nil {
		t.Fatalf("reading dispatched marker: %v", err)
	}
	if string(data) != "manager" {
		t.Errorf("dispatched = %q, want %q", string(data), "manager")
	}
}

func TestWriteDispatchedMarker_Overwrites(t *testing.T) {
	dir := t.TempDir()
	agentDir := filepath.Join(dir, "specialist")
	os.MkdirAll(filepath.Join(agentDir, ".runtime"), 0o755)

	// First write
	writeDispatchedMarker(agentDir, "old-caller")

	// Second write: MUST overwrite (unlike persistFormulaCaller)
	writeDispatchedMarker(agentDir, "new-caller")

	data, err := os.ReadFile(filepath.Join(agentDir, ".runtime", "dispatched"))
	if err != nil {
		t.Fatalf("reading dispatched marker: %v", err)
	}
	if string(data) != "new-caller" {
		t.Errorf("dispatched = %q, want %q (should overwrite)", string(data), "new-caller")
	}
}

func TestDispatchToSpecialist_WritesDispatchedMarker(t *testing.T) {
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	installNoopLaunchSession(t)
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	// dispatchToSpecialist will fail at store.Create, but marker write happens BEFORE that
	_ = dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "implement issue #99")

	markerPath := filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "dispatched")
	if _, err := os.Stat(markerPath); err != nil {
		t.Error(".runtime/dispatched should exist after specialist dispatch (proves writeDispatchedMarker ran)")
	}
}

func TestDispatchToSpecialist_ResetCleansDispatchedMarker(t *testing.T) {
	installNoopLaunchSession(t)
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	// Create stale dispatched marker
	agentDir := filepath.Join(root, ".agentfactory", "agents", "specialist-agent")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("stale-dispatch"), 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	origReset := slingReset
	slingReset = true
	defer func() { slingReset = origReset }()

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	_ = dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "implement issue #40")

	// The stale marker should have been cleaned by --reset
	// (A NEW marker may have been written by the dispatch itself, but the stale one was cleaned first)
	// We verify the reset path cleaned it by checking the --reset block ran.
	// The simplest check: the dispatched file should either not exist (if dispatch failed before re-writing)
	// or contain the NEW caller identity (not "stale-dispatch").
	markerPath := filepath.Join(runtimeDir, "dispatched")
	data2, err := os.ReadFile(markerPath)
	if err == nil && string(data2) == "stale-dispatch" {
		t.Error(".runtime/dispatched still contains stale content — --reset should have cleaned it")
	}
}

func TestCallerFlag_Registered(t *testing.T) {
	f := slingCmd.Flags().Lookup("caller")
	if f == nil {
		t.Fatal("--caller flag should be registered on sling command")
	}
	if f.DefValue != "" {
		t.Errorf("--caller default should be empty string, got %q", f.DefValue)
	}
	if !f.Hidden {
		t.Error("--caller flag should be hidden")
	}
	if !strings.Contains(f.Usage, "caller identity") {
		t.Errorf("--caller usage should mention 'caller identity', got %q", f.Usage)
	}
}

func TestPersistentFlag_Registered(t *testing.T) {
	f := slingCmd.Flags().Lookup("persistent")
	if f == nil {
		t.Fatal("--persistent flag should be registered on sling command")
	}
	if f.DefValue != "false" {
		t.Errorf("--persistent default should be 'false', got %q", f.DefValue)
	}
}

func TestDispatchToSpecialist_PersistentFlag_SuppressesMarker(t *testing.T) {
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	installNoopLaunchSession(t)
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	origPersistent := slingPersistent
	slingPersistent = true
	defer func() { slingPersistent = origPersistent }()

	_ = dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "implement issue #144")

	markerPath := filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "dispatched")
	if _, err := os.Stat(markerPath); err == nil {
		t.Error(".runtime/dispatched should NOT exist when --persistent is set (marker suppression is the interlock)")
	}

	callerPath := filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "formula_caller")
	if _, err := os.Stat(callerPath); err != nil {
		t.Error(".runtime/formula_caller should still exist with --persistent (WORK_DONE mail needs it)")
	}
}

func TestDispatchToSpecialist_PersistentDefault_WritesMarker(t *testing.T) {
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	installNoopLaunchSession(t)
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	origPersistent := slingPersistent
	slingPersistent = false
	defer func() { slingPersistent = origPersistent }()

	_ = dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "implement issue #99")

	markerPath := filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "dispatched")
	if _, err := os.Stat(markerPath); err != nil {
		t.Error(".runtime/dispatched should exist after default dispatch (regression guard)")
	}
}

func TestDispatchToSpecialist_ExplicitCaller(t *testing.T) {
	// When slingCaller is set (as dispatch passes --caller), dispatchToSpecialist
	// should use it directly instead of falling back to ensureCallerIdentity.
	// Dispatching from factory root with slingCaller="manager" must produce
	// formula_caller="manager", NOT "@cli".
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	installNoopLaunchSession(t)
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	agentDir := filepath.Join(root, ".agentfactory", "agents", "specialist-agent")
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Set explicit caller (simulating --caller manager from dispatch)
	origCaller := slingCaller
	slingCaller = "manager"
	defer func() { slingCaller = origCaller }()

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	// Dispatch from factory root — without explicit caller this would produce @cli
	_ = dispatchToSpecialist(cmd, root, root, "specialist-agent", "implement issue #99")

	callerData, err := os.ReadFile(filepath.Join(runtimeDir, "formula_caller"))
	if err != nil {
		t.Fatalf("formula_caller should exist: %v", err)
	}
	if string(callerData) != "manager" {
		t.Errorf("formula_caller = %q, want 'manager'", string(callerData))
	}

	// Verify dispatched marker also uses the explicit caller
	dispatchedData, err := os.ReadFile(filepath.Join(runtimeDir, "dispatched"))
	if err != nil {
		t.Fatalf("dispatched marker should exist: %v", err)
	}
	if string(dispatchedData) != "manager" {
		t.Errorf("dispatched marker = %q, want 'manager'", string(dispatchedData))
	}
}

func TestDispatchToSpecialist_CallerFlagEmpty_FallsBack(t *testing.T) {
	// When slingCaller is empty (manual CLI usage, agent-to-agent dispatch),
	// ensureCallerIdentity should run as before and detect the caller from cwd.
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	installNoopLaunchSession(t)
	root, _ := createTestFormulaFactory(t, "test-specialist-formula", "specialist-agent")

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"specialist-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test specialist",
				"formula":     "test-specialist-formula",
			},
			"dispatcher-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Dispatcher",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Explicitly set slingCaller to empty to confirm fallback path
	origCaller := slingCaller
	slingCaller = ""
	defer func() { slingCaller = origCaller }()

	callerWd := filepath.Join(root, ".agentfactory", "agents", "dispatcher-agent")
	os.MkdirAll(callerWd, 0o755)

	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", "af-specialist-agent").Run()
	})

	_ = dispatchToSpecialist(cmd, root, callerWd, "specialist-agent", "some task")

	callerData, err := os.ReadFile(filepath.Join(root, ".agentfactory", "agents", "specialist-agent", ".runtime", "formula_caller"))
	if err != nil {
		t.Fatalf("formula_caller should exist: %v", err)
	}
	if string(callerData) != "dispatcher-agent" {
		t.Errorf("formula_caller = %q, want 'dispatcher-agent' (from ensureCallerIdentity fallback)", string(callerData))
	}
}

func TestRunFormulaInstantiation_PassesWorktreeToLaunch(t *testing.T) {
	installMemStore(t)

	var capturedWTPath, capturedWTID string
	orig := launchAgentSession
	launchAgentSession = func(_ *cobra.Command, _, _, wtPath, wtID string) error {
		capturedWTPath = wtPath
		capturedWTID = wtID
		return nil
	}
	t.Cleanup(func() { launchAgentSession = orig })

	root, _ := createTestFormulaFactory(t, "test-wt-formula", "test-wt-agent")

	fakeWT := filepath.Join(root, "worktrees", "wt-test")
	os.MkdirAll(fakeWT, 0o755)
	t.Setenv("AF_WORKTREE", fakeWT)
	t.Setenv("AF_WORKTREE_ID", "wt-test123")

	origFormula := slingFormulaName
	origAgent := slingAgent
	origVars := slingVars
	origNoLaunch := slingNoLaunch
	slingFormulaName = "test-wt-formula"
	slingAgent = "test-wt-agent"
	slingVars = nil
	slingNoLaunch = false
	t.Cleanup(func() {
		slingFormulaName = origFormula
		slingAgent = origAgent
		slingVars = origVars
		slingNoLaunch = origNoLaunch
	})

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runFormulaInstantiation(cmd, root, root, nil)
	if err != nil {
		t.Fatalf("runFormulaInstantiation failed: %v", err)
	}

	if capturedWTPath == "" {
		t.Error("worktreePath passed to launchAgentSession is empty; want non-empty (ResolveOrCreate should resolve from AF_WORKTREE env)")
	}
	if capturedWTID == "" {
		t.Error("worktreeID passed to launchAgentSession is empty; want non-empty (ResolveOrCreate should resolve from AF_WORKTREE_ID env)")
	}
}

func TestAssignmentTitle(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"short string", "fix the login bug", "fix the login bug"},
		{"exactly 80 chars", strings.Repeat("a", 80), strings.Repeat("a", 80)},
		{"81 chars truncates", strings.Repeat("b", 81), strings.Repeat("b", 77) + "..."},
		{"multi-line uses first line", "first line\nsecond line\nthird line", "first line"},
		{"empty string", "", ""},
		{"long first line of multi-line", strings.Repeat("c", 100) + "\nsecond", strings.Repeat("c", 77) + "..."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := assignmentTitle(tt.input)
			if got != tt.want {
				t.Errorf("assignmentTitle(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestInstantiateFormulaWorkflow_AutoCreateAssignmentBead(t *testing.T) {
	store := installMemStore(t)
	installNoopLaunchSession(t)
	toml := `
formula = "test-auto-assign"
type = "workflow"
version = 1

[vars.issue]
source = "cli"
required = true

[[steps]]
id = "step1"
title = "Work on {{issue}}"
description = "Fixing {{issue}}"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-auto-assign", "test-agent", toml)

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     "test-auto-assign",
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		TaskDescription: "implement feature X",
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	// Verify an assignment bead was created.
	issues, err := store.List(t.Context(), issuestore.Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	var found bool
	for _, iss := range issues {
		for _, lbl := range iss.Labels {
			if lbl == "assignment" {
				found = true
				if iss.Title != "implement feature X" {
					t.Errorf("assignment bead title = %q, want %q", iss.Title, "implement feature X")
				}
				if iss.Type != issuestore.TypeTask {
					t.Errorf("assignment bead type = %q, want %q", iss.Type, issuestore.TypeTask)
				}
			}
		}
	}
	if !found {
		t.Error("no assignment bead found in store")
	}
}

func TestInstantiateFormulaWorkflow_AutoCreateSkippedWhenIssueProvided(t *testing.T) {
	store := installMemStore(t)
	installNoopLaunchSession(t)
	toml := `
formula = "test-skip-assign"
type = "workflow"
version = 1

[vars.issue]
source = "cli"
required = true

[[steps]]
id = "step1"
title = "Work on {{issue}}"
description = "Fixing {{issue}}"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-skip-assign", "test-agent", toml)

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     "test-skip-assign",
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		CLIVars:         []string{"issue=bd-42"},
		TaskDescription: "implement feature X",
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	// No assignment bead should have been created.
	issues, err := store.List(t.Context(), issuestore.Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	for _, iss := range issues {
		for _, lbl := range iss.Labels {
			if lbl == "assignment" {
				t.Error("assignment bead should NOT be created when --var issue= is provided")
			}
		}
	}
}

func TestDispatchToSpecialist_AutoCreateAssignmentBead(t *testing.T) {
	// When dispatchToSpecialist is called WITHOUT --var issue= in slingVars,
	// the auto-creation path should fire, creating an assignment bead with
	// the "assignment" label derived from the task string.
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	store := installMemStore(t)
	installNoopLaunchSession(t)

	toml := `
formula = "auto-create-test"
type = "workflow"
version = 1

[vars.issue]
description = "Issue bead ID"
source = "cli"
required = true

[[steps]]
id = "step-1"
title = "Work on {{issue}}"
description = "Implementing {{issue}}"
`
	root, _ := createTestFormulaFactoryWithTOML(t, "auto-create-test", "auto-agent", toml)

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"auto-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test auto-creation",
				"formula":     "auto-create-test",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	// Do NOT set slingVars with issue= — this triggers auto-creation
	origVars := slingVars
	slingVars = []string{}
	t.Cleanup(func() { slingVars = origVars })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	if err := dispatchToSpecialist(cmd, root, callerWd, "auto-agent", "implement the widget feature"); err != nil {
		t.Fatalf("dispatchToSpecialist: %v", err)
	}

	// Verify an assignment bead was created in the store.
	issues, err := store.List(t.Context(), issuestore.Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	var assignmentID string
	for _, iss := range issues {
		for _, lbl := range iss.Labels {
			if lbl == "assignment" {
				assignmentID = iss.ID
				if iss.Title != "implement the widget feature" {
					t.Errorf("assignment bead title = %q, want %q", iss.Title, "implement the widget feature")
				}
				if iss.Type != issuestore.TypeTask {
					t.Errorf("assignment bead type = %q, want %q", iss.Type, issuestore.TypeTask)
				}
				if !strings.Contains(iss.Description, "implement the widget feature") {
					t.Errorf("assignment bead description = %q, want it to contain full task text", iss.Description)
				}
			}
		}
	}
	if assignmentID == "" {
		t.Fatal("no assignment bead found in store — auto-creation did not fire through dispatchToSpecialist")
	}

	// Verify .runtime/hooked_formula exists and contains a different ID than
	// the assignment bead (it holds the formula instance bead, not the assignment).
	agentDir := filepath.Join(root, ".agentfactory", "agents", "auto-agent")
	hookedPath := filepath.Join(agentDir, ".runtime", "hooked_formula")
	hookedData, err := os.ReadFile(hookedPath)
	if err != nil {
		t.Fatalf("hooked_formula not written: %v", err)
	}
	hookedID := strings.TrimSpace(string(hookedData))
	if hookedID == "" {
		t.Error("hooked_formula file is empty")
	}
	if hookedID == assignmentID {
		t.Errorf("hooked_formula ID %q should differ from assignment bead ID", hookedID)
	}
}

func TestDispatchToSpecialist_CallerSuppliedBeadUsed(t *testing.T) {
	// When dispatchToSpecialist is called WITH --var issue=<id> in slingVars,
	// auto-creation should be skipped and the caller-supplied bead used.
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	store := installMemStore(t)
	installNoopLaunchSession(t)

	toml := `
formula = "caller-supplied-test"
type = "workflow"
version = 1

[vars.issue]
description = "Issue bead ID"
source = "cli"
required = true

[[steps]]
id = "step-1"
title = "Work on {{issue}}"
description = "Implementing {{issue}}"
`
	root, _ := createTestFormulaFactoryWithTOML(t, "caller-supplied-test", "supplied-agent", toml)

	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"supplied-agent": map[string]interface{}{
				"type":        "autonomous",
				"description": "Test caller-supplied bead",
				"formula":     "caller-supplied-test",
			},
		},
	}
	data, _ := json.Marshal(agents)
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"), data, 0o644)

	// Pre-create a bead to supply via --var issue=
	preCreated, err := store.Create(t.Context(), issuestore.CreateParams{
		Title: "pre-existing issue",
		Type:  issuestore.TypeTask,
	})
	if err != nil {
		t.Fatalf("store.Create: %v", err)
	}

	// Set slingVars with the pre-created bead ID — should skip auto-creation
	origVars := slingVars
	slingVars = []string{"issue=" + preCreated.ID}
	t.Cleanup(func() { slingVars = origVars })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	if err := dispatchToSpecialist(cmd, root, callerWd, "supplied-agent", "some task"); err != nil {
		t.Fatalf("dispatchToSpecialist: %v", err)
	}

	// Verify NO assignment bead was created (auto-creation skipped).
	issues, err := store.List(t.Context(), issuestore.Filter{})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	for _, iss := range issues {
		for _, lbl := range iss.Labels {
			if lbl == "assignment" {
				t.Error("assignment bead should NOT be created when --var issue= is provided via slingVars")
			}
		}
	}

	// Verify the step bead resolved with the pre-created bead ID.
	var foundStep bool
	for _, iss := range issues {
		if strings.Contains(iss.Title, preCreated.ID) {
			foundStep = true
			break
		}
	}
	if !foundStep {
		var titles []string
		for _, iss := range issues {
			titles = append(titles, iss.Title)
		}
		t.Errorf("no step bead references pre-created ID %q; got titles: %v", preCreated.ID, titles)
	}
}

// TestSling_FormulaWithBogusAgent_Rejected pins Phase 3 wire-up (AC-3): a
// formula declaring an agent that is not in agents.json must be rejected by
// instantiateFormulaWorkflow before bead creation. Validation at sling.go:314-321
// runs before newIssueStore at L346, so this test needs no store/launch stubs.
func TestSling_FormulaWithBogusAgent_Rejected(t *testing.T) {
	root, agentDir := createTestFormulaFactory(t, "test", "manager")

	// Overwrite the default formula TOML with one declaring agent = "ghost".
	// createTestFormulaFactory seeded agents.json with only "manager", so
	// "ghost" is not a known agent and ValidateAgents must reject it.
	bogusTOML := `
formula = "test"
type = "workflow"
version = 1
agent = "ghost"

[[steps]]
id = "step1"
title = "Step 1"
`
	formulaPath := filepath.Join(root, ".beads", "formulas", "test.formula.toml")
	if err := os.WriteFile(formulaPath, []byte(bogusTOML), 0o644); err != nil {
		t.Fatalf("overwriting bogus formula: %v", err)
	}

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test",
		AgentName:   "manager",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err == nil {
		t.Fatal("expected error from bogus-agent formula, got nil")
	}
	msg := err.Error()
	if !strings.Contains(msg, `unknown agent "ghost"`) {
		t.Errorf("err = %q, want it to contain %q", msg, `unknown agent "ghost"`)
	}
	if !strings.Contains(msg, "validating formula agents") {
		t.Errorf("err = %q, want it to contain %q", msg, "validating formula agents")
	}
}

// TestInstantiateFormula_PerStepAssignee pins the per-step `agent` branch of
// Formula.AgentFor — the highest-precedence branch. The step bead's Assignee
// must equal the per-step `agent` declaration.
func TestInstantiateFormula_PerStepAssignee(t *testing.T) {
	store := installMemStore(t)
	toml := `
formula = "test-per-step-agent"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Step 1"
agent = "alpha"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-per-step-agent", "alpha", toml)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-per-step-agent",
		AgentName:   "alpha",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	stepBeadID, ok := stepIDs["step1"]
	if !ok {
		t.Fatal("stepIDs missing step1")
	}
	step, err := store.Get(t.Context(), stepBeadID)
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if step.Assignee != "alpha" {
		t.Errorf("step.Assignee = %q, want %q", step.Assignee, "alpha")
	}
}

// TestInstantiateFormula_FormulaLevelFallback pins the formula-level fallback
// branch of Formula.AgentFor — applies to every step when no per-step `agent`
// is declared. Both step beads must carry the formula-level agent.
func TestInstantiateFormula_FormulaLevelFallback(t *testing.T) {
	store := installMemStore(t)
	toml := `
formula = "test-formula-agent"
type = "workflow"
version = 1
agent = "beta"

[[steps]]
id = "step1"
title = "Step 1"

[[steps]]
id = "step2"
title = "Step 2"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-formula-agent", "beta", toml)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-formula-agent",
		AgentName:   "beta",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	for _, id := range []string{"step1", "step2"} {
		stepBeadID, ok := stepIDs[id]
		if !ok {
			t.Fatalf("stepIDs missing %s", id)
		}
		step, err := store.Get(t.Context(), stepBeadID)
		if err != nil {
			t.Fatalf("store.Get(%s): %v", id, err)
		}
		if step.Assignee != "beta" {
			t.Errorf("%s: step.Assignee = %q, want %q", id, step.Assignee, "beta")
		}
	}
}

// TestInstantiateFormula_FallsBackToSlingAgentWhenUndeclared pins the Phase 1
// data-plane invariant (parent_id = '' OR assignee != ''): when no agent is
// declared at any formula level, the step bead's Assignee falls back to the
// CLI-resolved slingAgent via assigneeForStep. Empty Assignee on
// parent-scoped beads is now unrepresentable; the fallback is what makes
// that true for formulas like `scenario` that declare no agent.
func TestInstantiateFormula_FallsBackToSlingAgentWhenUndeclared(t *testing.T) {
	store := installMemStore(t)
	toml := `
formula = "test-no-agent"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Step 1"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-no-agent", "runner", toml)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-no-agent",
		AgentName:   "runner",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	stepBeadID, ok := stepIDs["step1"]
	if !ok {
		t.Fatal("stepIDs missing step1")
	}
	step, err := store.Get(t.Context(), stepBeadID)
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if step.Assignee != "runner" {
		t.Errorf("step.Assignee = %q, want %q (slingAgent fallback)", step.Assignee, "runner")
	}
}

// TestSling_RequiresNonEmptyAgent pins UX-2: when the --agent flag is empty
// AND detectAgentName's workspace fallback also returns "", the workflow
// must error before any bead is created. The error message must mention the
// --agent flag so the user knows how to fix it.
func TestSling_RequiresNonEmptyAgent(t *testing.T) {
	installMemStore(t)
	toml := `
formula = "test-no-agent-no-cli"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Step 1"
`
	// createTestFormulaFactoryWithTOML writes an agents.json containing the
	// named agent and creates its workspace dir. For this test we want
	// detectAgentName to fail — invoke from the factory root (outside any
	// agent workspace) so detectCreatingAgent/detectAgentName returns "".
	root, _ := createTestFormulaFactoryWithTOML(t, "test-no-agent-no-cli", "runner", toml)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-no-agent-no-cli",
		AgentName:   "", // UX-2: explicit CLI empty
		Root:        root,
		WorkDir:     root, // not inside an agent workspace
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err == nil {
		t.Fatal("expected error when --agent is empty and workspace detection fails, got nil")
	}
	if !strings.Contains(err.Error(), "--agent is required") {
		t.Errorf("error %q should mention --agent is required", err.Error())
	}
}

// TestSling_PopulatesStepAssigneeAndEpicAssignee pins the core Phase 1
// invariant from the producer side: EPIC and step beads carry non-empty
// Assignee after sling instantiation. Formula declares no agent; the
// --agent flag provides the fallback used by both the EPIC CreateParams
// (directly) and the step CreateParams (via assigneeForStep).
func TestSling_PopulatesStepAssigneeAndEpicAssignee(t *testing.T) {
	store := installMemStore(t)
	toml := `
formula = "test-populate-assignee"
type = "workflow"
version = 1

[[steps]]
id = "step1"
title = "Step 1"

[[steps]]
id = "step2"
title = "Step 2"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-populate-assignee", "builder", toml)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-populate-assignee",
		AgentName:   "builder",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	instanceID, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	// EPIC's Assignee must equal slingAgent.
	epic, err := store.Get(t.Context(), instanceID)
	if err != nil {
		t.Fatalf("store.Get(epic): %v", err)
	}
	if epic.Assignee != "builder" {
		t.Errorf("EPIC Assignee = %q, want %q", epic.Assignee, "builder")
	}

	// Every step's Assignee must equal slingAgent (formula declares no agent
	// so assigneeForStep falls through to slingAgent).
	for _, id := range []string{"step1", "step2"} {
		stepBeadID, ok := stepIDs[id]
		if !ok {
			t.Fatalf("stepIDs missing %s", id)
		}
		step, err := store.Get(t.Context(), stepBeadID)
		if err != nil {
			t.Fatalf("store.Get(%s): %v", id, err)
		}
		if step.Assignee != "builder" {
			t.Errorf("%s.Assignee = %q, want %q", id, step.Assignee, "builder")
		}
	}
}

func TestSling_AbandonedPrior_ErrorsWithResetHint(t *testing.T) {
	installMemStore(t)
	installNoopLaunchSession(t)

	root, agentDir := createTestFormulaFactory(t, "test-formula", "test-agent")

	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("stale-instance-id"), 0o644)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-formula",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err == nil {
		t.Fatal("expected error when prior formula is active, got nil")
	}
	if !strings.Contains(err.Error(), "use --reset") {
		t.Errorf("error should mention 'use --reset', got: %v", err)
	}
}

func TestSling_AbandonedPrior_ResetCleansAndSucceeds(t *testing.T) {
	installMemStore(t)
	installNoopLaunchSession(t)

	root, agentDir := createTestFormulaFactory(t, "test-formula", "test-agent")

	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("stale-instance-id"), 0o644)
	os.WriteFile(filepath.Join(runtimeDir, "formula_caller"), []byte("old-caller"), 0o644)
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("old-dispatch"), 0o644)

	origReset := slingReset
	slingReset = true
	t.Cleanup(func() { slingReset = origReset })

	origFormulaName := slingFormulaName
	slingFormulaName = "test-formula"
	t.Cleanup(func() { slingFormulaName = origFormulaName })

	origAgent := slingAgent
	slingAgent = "test-agent"
	t.Cleanup(func() { slingAgent = origAgent })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runFormulaInstantiation(cmd, root, agentDir, nil)
	if err != nil {
		t.Fatalf("runFormulaInstantiation with --reset should succeed, got: %v", err)
	}

	if _, err := os.Stat(filepath.Join(runtimeDir, "dispatched")); err == nil {
		t.Error("dispatched should be cleaned by --reset")
	}
	if data, err := os.ReadFile(filepath.Join(runtimeDir, "formula_caller")); err == nil {
		if strings.TrimSpace(string(data)) == "old-caller" {
			t.Error("formula_caller should have been cleaned and re-written by --reset, still contains old value")
		}
	}
	if data, err := os.ReadFile(filepath.Join(runtimeDir, "hooked_formula")); err != nil {
		t.Error("hooked_formula should exist after successful re-sling")
	} else if strings.TrimSpace(string(data)) == "stale-instance-id" {
		t.Error("hooked_formula should contain new instance ID, still contains stale value")
	}
}

func TestSling_NoPrior_SucceedsWithoutReset(t *testing.T) {
	installMemStore(t)
	installNoopLaunchSession(t)

	root, agentDir := createTestFormulaFactory(t, "test-formula", "test-agent")

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "test-formula",
		AgentName:   "test-agent",
		Root:        root,
		WorkDir:     agentDir,
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow should succeed when no prior formula exists, got: %v", err)
	}
}

// ---------------------------------------------------------------------------
// findUnsatisfiedRequiredInputs unit tests
// ---------------------------------------------------------------------------

func TestFindUnsatisfiedRequiredInputs_SingleRequired(t *testing.T) {
	inputs := map[string]formula.Input{
		"issue_uri": {Required: true},
	}
	got := findUnsatisfiedRequiredInputs(inputs, map[string]string{})
	if len(got) != 1 || got[0] != "issue_uri" {
		t.Fatalf("expected [issue_uri], got %v", got)
	}
}

func TestFindUnsatisfiedRequiredInputs_AlreadySatisfied(t *testing.T) {
	inputs := map[string]formula.Input{
		"issue_uri": {Required: true},
	}
	got := findUnsatisfiedRequiredInputs(inputs, map[string]string{"issue_uri": "val"})
	if len(got) != 0 {
		t.Fatalf("expected [], got %v", got)
	}
}

func TestFindUnsatisfiedRequiredInputs_HasDefault(t *testing.T) {
	inputs := map[string]formula.Input{
		"issue_uri": {Required: true, Default: "default-val"},
	}
	got := findUnsatisfiedRequiredInputs(inputs, map[string]string{})
	if len(got) != 0 {
		t.Fatalf("expected [], got %v", got)
	}
}

func TestFindUnsatisfiedRequiredInputs_NotRequired(t *testing.T) {
	inputs := map[string]formula.Input{
		"issue_uri": {Required: false},
	}
	got := findUnsatisfiedRequiredInputs(inputs, map[string]string{})
	if len(got) != 0 {
		t.Fatalf("expected [], got %v", got)
	}
}

func TestFindUnsatisfiedRequiredInputs_RequiredUnlessPresent(t *testing.T) {
	inputs := map[string]formula.Input{
		"primary": {Required: true, RequiredUnless: []string{"fallback"}},
	}
	got := findUnsatisfiedRequiredInputs(inputs, map[string]string{"fallback": "val"})
	if len(got) != 0 {
		t.Fatalf("expected [] (excused by RequiredUnless), got %v", got)
	}
}

func TestFindUnsatisfiedRequiredInputs_RequiredUnlessAbsent(t *testing.T) {
	inputs := map[string]formula.Input{
		"primary": {Required: true, RequiredUnless: []string{"fallback"}},
	}
	got := findUnsatisfiedRequiredInputs(inputs, map[string]string{})
	if len(got) != 1 || got[0] != "primary" {
		t.Fatalf("expected [primary], got %v", got)
	}
}

func TestFindUnsatisfiedRequiredInputs_SortedOutput(t *testing.T) {
	inputs := map[string]formula.Input{
		"zebra": {Required: true},
		"alpha": {Required: true},
		"mid":   {Required: true},
	}
	got := findUnsatisfiedRequiredInputs(inputs, map[string]string{})
	if len(got) != 3 || got[0] != "alpha" || got[1] != "mid" || got[2] != "zebra" {
		t.Fatalf("expected [alpha mid zebra], got %v", got)
	}
}

func TestFindUnsatisfiedRequiredInputs_MixedSatisfaction(t *testing.T) {
	inputs := map[string]formula.Input{
		"outline_path": {Required: true},
		"target_agent": {Required: true},
	}
	got := findUnsatisfiedRequiredInputs(inputs, map[string]string{"outline_path": "/path"})
	if len(got) != 1 || got[0] != "target_agent" {
		t.Fatalf("expected [target_agent], got %v", got)
	}
}

// ---------------------------------------------------------------------------
// Bridge logic integration tests
// ---------------------------------------------------------------------------

func TestInstantiateFormulaWorkflow_InputBridge_SingleUnsatisfied(t *testing.T) {
	store := installMemStore(t)
	toml := `
formula = "test-bridge"
type = "workflow"
version = 1

[inputs.issue_uri]
description = "Issue URI"
type = "string"
required = true

[[steps]]
id = "step1"
title = "Process {{issue_uri}}"
description = "Working on {{issue_uri}}"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-bridge", "test-agent", toml)

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     "test-bridge",
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		TaskDescription: "https://github.com/example/123",
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	stepBeadID, ok := stepIDs["step1"]
	if !ok {
		t.Fatal("stepIDs missing step1")
	}
	step, err := store.Get(t.Context(), stepBeadID)
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Title, "https://github.com/example/123") {
		t.Errorf("step title should contain bridged value, got: %q", step.Title)
	}
}

func TestInstantiateFormulaWorkflow_InputBridge_MultipleUnsatisfied_Error(t *testing.T) {
	installMemStore(t)
	toml := `
formula = "test-bridge-multi"
type = "workflow"
version = 1

[inputs.outline_path]
description = "Path to outline"
type = "string"
required = true

[inputs.target_agent]
description = "Target agent name"
type = "string"
required = true

[[steps]]
id = "step1"
title = "Process"
description = "Working"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-bridge-multi", "test-agent", toml)

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     "test-bridge-multi",
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		TaskDescription: "some text",
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err == nil {
		t.Fatal("expected error for multiple unsatisfied inputs, got nil")
	}
	if !strings.Contains(err.Error(), "outline_path") || !strings.Contains(err.Error(), "target_agent") {
		t.Errorf("error should name both unsatisfied inputs, got: %v", err)
	}
}

func TestInstantiateFormulaWorkflow_InputBridge_PartialVar(t *testing.T) {
	store := installMemStore(t)
	toml := `
formula = "test-bridge-partial"
type = "workflow"
version = 1

[inputs.outline_path]
description = "Path to outline"
type = "string"
required = true

[inputs.target_agent]
description = "Target agent name"
type = "string"
required = true

[[steps]]
id = "step1"
title = "{{target_agent}} processes {{outline_path}}"
description = "Working"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-bridge-partial", "test-agent", toml)

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     "test-bridge-partial",
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		CLIVars:         []string{"outline_path=/path/to/outline.md"},
		TaskDescription: "deacon",
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	step, err := store.Get(t.Context(), stepIDs["step1"])
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Title, "deacon") {
		t.Errorf("step title should contain bridged 'deacon', got: %q", step.Title)
	}
	if !strings.Contains(step.Title, "/path/to/outline.md") {
		t.Errorf("step title should contain explicit var, got: %q", step.Title)
	}
}

func TestInstantiateFormulaWorkflow_InputBridge_AllSatisfied_NoOp(t *testing.T) {
	store := installMemStore(t)
	toml := `
formula = "test-bridge-noop"
type = "workflow"
version = 1

[inputs.issue_uri]
description = "Issue URI"
type = "string"
required = true

[[steps]]
id = "step1"
title = "Process {{issue_uri}}"
description = "Working"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-bridge-noop", "test-agent", toml)

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     "test-bridge-noop",
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		CLIVars:         []string{"issue_uri=https://example.com/456"},
		TaskDescription: "should-not-override",
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	step, err := store.Get(t.Context(), stepIDs["step1"])
	if err != nil {
		t.Fatalf("store.Get(step1): %v", err)
	}
	if !strings.Contains(step.Title, "https://example.com/456") {
		t.Errorf("step title should contain explicit var value, got: %q", step.Title)
	}
	if strings.Contains(step.Title, "should-not-override") {
		t.Errorf("bridge should NOT override explicit CLIVar, got: %q", step.Title)
	}
}

func TestInstantiateFormulaWorkflow_ConvoyDoesNotBridge(t *testing.T) {
	installMemStore(t)
	toml := `
formula = "test-convoy-bridge"
type = "convoy"
version = 1

[inputs.problem]
description = "Problem to solve"
type = "string"
required = true

[inputs.severity]
description = "Severity level"
type = "string"
required = true

[[legs]]
id = "leg1"
title = "Investigate"
description = "Working on it"
`
	root, agentDir := createTestFormulaFactoryWithTOML(t, "test-convoy-bridge", "test-agent", toml)

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     "test-convoy-bridge",
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		TaskDescription: "BRIDGE-SENTINEL-SHOULD-NOT-APPEAR",
	}

	var buf bytes.Buffer
	_, _, _, err := instantiateFormulaWorkflow(params, &buf)

	if err != nil && strings.Contains(err.Error(), "required inputs not provided") {
		t.Errorf("convoy should NOT trigger input bridge, got bridge error: %v", err)
	}
}
