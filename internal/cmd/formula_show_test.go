package cmd

import (
	"encoding/json"
	"strings"
	"testing"
)

const showFormulaTOML = `
formula = "showme"
description = "demo formula"
type = "workflow"
version = 1

[inputs.issue]
description = "Issue ID"
type = "string"
required = true

[inputs.url]
description = "PR url"
type = "string"
required = false
required_unless = ["issue"]
default = "x"

[vars.branch]
description = "branch var"
source = "cli"

[[steps]]
id = "step1"
title = "do {{issue}}"
description = "work on {{issue}}"
`

// invokeFormulaShow runs runFormulaShow via formulaShowCmd with the given args.
func invokeFormulaShow(t *testing.T, args ...string) string {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		formulaShowCmd.SetContext(t.Context())
		runErr = runFormulaShow(formulaShowCmd, args)
	})
	if runErr != nil {
		t.Fatalf("runFormulaShow: %v", runErr)
	}
	return out
}

func TestFormulaShow_JSON_SchemaSnapshot(t *testing.T) {
	root, _ := createTestFormulaFactoryWithTOML(t, "showme", "worker", showFormulaTOML)
	t.Chdir(root)

	out := invokeFormulaShow(t, "showme")

	// Top-level key set is frozen.
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &top); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	wantTop := map[string]bool{"name": true, "description": true, "type": true, "inputs": true, "vars": true}
	if len(top) != len(wantTop) {
		t.Errorf("top-level key count = %d (%v), want %d", len(top), keysOf(top), len(wantTop))
	}
	for k := range wantTop {
		if _, ok := top[k]; !ok {
			t.Errorf("missing top-level key %q in %q", k, out)
		}
	}
	for k := range top {
		if !wantTop[k] {
			t.Errorf("unexpected top-level key %q in %q", k, out)
		}
	}

	// Per-field key set is frozen (assert on the first input field).
	var fields []map[string]json.RawMessage
	if err := json.Unmarshal(top["inputs"], &fields); err != nil {
		t.Fatalf("unmarshal inputs: %v", err)
	}
	if len(fields) == 0 {
		t.Fatalf("no input fields emitted: %q", out)
	}
	wantField := map[string]bool{
		"name": true, "description": true, "type": true, "required": true,
		"required_unless": true, "default": true, "source": true,
	}
	if len(fields[0]) != len(wantField) {
		t.Errorf("field key count = %d (%v), want %d", len(fields[0]), keysOf(fields[0]), len(wantField))
	}
	for k := range wantField {
		if _, ok := fields[0][k]; !ok {
			t.Errorf("missing field key %q in %q", k, out)
		}
	}
	for k := range fields[0] {
		if !wantField[k] {
			t.Errorf("unexpected field key %q in %q", k, out)
		}
	}
}

// TestFormulaShow_PreservesTypeAndRequiredUnless is the value-level guard: it
// proves the command reads the typed inputs/vars (NOT the merged Vars, which drop
// type and required_unless). A regression to MergeInputsToVars-based reading
// would zero these and fail here even though the snapshot key set still passes.
func TestFormulaShow_PreservesTypeAndRequiredUnless(t *testing.T) {
	root, _ := createTestFormulaFactoryWithTOML(t, "showme", "worker", showFormulaTOML)
	t.Chdir(root)

	out := invokeFormulaShow(t, "showme")
	var parsed formulaShowOutput
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}

	var url *formulaField
	for i := range parsed.Inputs {
		if parsed.Inputs[i].Name == "url" {
			url = &parsed.Inputs[i]
		}
	}
	if url == nil {
		t.Fatalf("input %q missing from output %q", "url", out)
	}
	if url.Type != "string" {
		t.Errorf("url.type = %q, want %q (type dropped — MergeInputsToVars regression?)", url.Type, "string")
	}
	if len(url.RequiredUnless) != 1 || url.RequiredUnless[0] != "issue" {
		t.Errorf("url.required_unless = %v, want [issue] (required_unless dropped)", url.RequiredUnless)
	}

	if len(parsed.Vars) != 1 || parsed.Vars[0].Name != "branch" || parsed.Vars[0].Source != "cli" {
		t.Errorf("vars = %+v, want one var 'branch' with source 'cli'", parsed.Vars)
	}
}

func TestFormulaShow_MissingFormula_ErrorState(t *testing.T) {
	root, _ := createTestFormulaFactoryWithTOML(t, "showme", "worker", showFormulaTOML)
	t.Chdir(root)

	out := invokeFormulaShow(t, "does-not-exist")
	var env map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if env["state"] != "error" {
		t.Errorf("state = %q, want \"error\" for a missing formula (output %q)", env["state"], out)
	}
}
