package cmd

// Cross-module behavioral contract for the Web Console "Sling" page (Issue #440, Phase 4).
//
// The web console (web/go.mod, module github.com/stempeck/agentfactory-web) talks to
// af-core ONLY through the `af` CLI/JSON contract and is structurally forbidden from
// importing internal/... (Go's internal seal + the separate module; enforced by
// web/internal/server/extractability_test.go). The web side can therefore assert only
// the argv *shape* it emits — `--agent <name> --reset [--var k=v …] -- "<task>"`. Whether
// af-core actually *accepts* that argv and binds the right field for each agent shape can
// only be proven on the ROOT side. That is what this file does: it feeds the web's
// positional-task shape through the real af-core sling binder (instantiateFormulaWorkflow)
// in a hermetic, in-process memstore and asserts the EFFECTIVE bound field per shape.
//
// Three af-core binding mechanisms are in play (read-only context, verified):
//   - Mechanism 1 — assignment bead (the LOAD-BEARING binder), sling.go:432-444: when the
//     positional task is set and cliVars["issue"] is empty, af creates a Type=Task,
//     Labels=["assignment"] bead with Description==<task> and sets cliVars["issue"]=bead.ID.
//     This fires for EVERY bare-task dispatch (it does not depend on len(f.Inputs)).
//   - Mechanism 2 — input bridge (a workflow optimization), sling.go:473-484: for a workflow
//     with inputs, the SINGLE unsatisfied required input receives the literal positional task.
//   - Universal resolution, formula/vars.go:64-70: a var resolves from cliVars[name] when set,
//     BEFORE its declared source — so the assignment-bead ID wins regardless of source
//     (including design-v7's hook_bead source).
//
// The contract asserted here is the EFFECTIVE bound field (the rendered step bead), never
// equality with findUnsatisfiedRequiredInputs (which scans inputs only and returns [] for the
// vars-only `issue` shapes — asserting against it would re-encode the C1 trap and "prove"
// Primary==""). For the assignment-bead shapes the step renders a memstore bead ID (mem-N);
// for the input-bridge shape the step renders the verbatim task text — a decisive discriminator.
//
// This file MUST be in the DEFAULT (untagged) build so `make test` (= `go test ./...`, no
// integration tag) runs it — do NOT add //go:build integration. It is hermetic: it never runs
// a live `af sling`/`--reset` and never mutates this working tree (C-1 safety).

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// webTask is a representative web-console task: free text with shell-looking and
// hash characters, exactly the kind the positional-after-`--` shape must carry intact.
const webTask = "implement the widget; fix #38"

// ---- synthetic formula shapes (mirror the real roster's bind-determining structure) ----

// issueVarTOML mirrors the rootcause-all shape: a required user-providable `issue` VAR
// (source=cli), no inputs. The task binds `issue` via the assignment bead (Mechanism 1).
func issueVarTOML(name string) string {
	return `formula = "` + name + `"
type = "workflow"
version = 1

[vars.issue]
description = "Issue ID assigned to this agent"
required = true
source = "cli"

[[steps]]
id = "s1"
title = "analyze {{issue}}"
description = "work on {{issue}}"
`
}

// hookBeadIssueTOML mirrors the design-v7 shape: a required `issue` VAR whose source is the
// hidden `hook_bead`, no inputs. With no hooked-formula file present, the assignment-bead path
// fires and the universal CLI override (vars.go:64-70) resolves `issue` to the bead ID despite
// the hook_bead source.
func hookBeadIssueTOML(name string) string {
	return `formula = "` + name + `"
type = "workflow"
version = 1

[vars.issue]
description = "Design request assigned to this agent"
required = true
source = "hook_bead"

[[steps]]
id = "s1"
title = "design {{issue}}"
description = "design for {{issue}}"
`
}

// inputBridgeTOML mirrors the rapid-soldesign-plan shape: one required input with NO default
// (issue_uri) plus a sibling required input WITH a default (analyst_name). The single
// unsatisfied required input receives the literal task (Mechanism 2); the defaulted input keeps
// its default.
func inputBridgeTOML(name string) string {
	return `formula = "` + name + `"
type = "workflow"
version = 1

[inputs.issue_uri]
description = "GitHub issue URL to use as the design problem input"
type = "string"
required = true

[inputs.analyst_name]
description = "Agent name for the analyst role"
type = "string"
required = true
default = "rootcause-all"

[[steps]]
id = "s1"
title = "parse {{issue_uri}}"
description = "design from {{issue_uri}} using {{analyst_name}}"
`
}

// ---- hermetic harness ----

// instantiateWebArgvShape drives the af-core binder exactly as a web-console dispatch would:
// the positional task is delivered as InstantiateParams.TaskDescription (the in-process
// equivalent of the argv `… -- "<task>"`), with NO `issue` CLI var pre-set. It returns the
// inspectable memstore and the stepID->beadID map so a test can read back the rendered field.
func instantiateWebArgvShape(t *testing.T, formulaName, toml, task string) (*memstore.Store, map[string]string) {
	t.Helper()
	store := installMemStore(t)
	root, agentDir := createTestFormulaFactoryWithTOML(t, formulaName, "test-agent", toml)

	params := InstantiateParams{
		Ctx:             t.Context(),
		FormulaName:     formulaName,
		AgentName:       "test-agent",
		Root:            root,
		WorkDir:         agentDir,
		TaskDescription: task,
	}

	var buf bytes.Buffer
	_, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow(%s): %v\nbinder output:\n%s", formulaName, err, buf.String())
	}
	return store, stepIDs
}

// renderedStep returns the interpolated step bead for stepID.
func renderedStep(t *testing.T, store *memstore.Store, stepIDs map[string]string, stepID string) issuestore.Issue {
	t.Helper()
	beadID, ok := stepIDs[stepID]
	if !ok {
		t.Fatalf("stepIDs missing %q (got %v)", stepID, stepIDs)
	}
	step, err := store.Get(t.Context(), beadID)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", stepID, err)
	}
	return step
}

// assignmentBead returns the single "assignment"-labelled bead Mechanism 1 creates, failing
// if the count is not exactly one. The IncludeAllAgents/IncludeClosed filter mirrors the
// defensive shape used by resolved_vars_test.go.
func assignmentBead(t *testing.T, store *memstore.Store) issuestore.Issue {
	t.Helper()
	beads, err := store.List(t.Context(), issuestore.Filter{
		Labels:           []string{"assignment"},
		IncludeAllAgents: true,
		IncludeClosed:    true,
	})
	if err != nil {
		t.Fatalf("list assignment beads: %v", err)
	}
	if len(beads) != 1 {
		t.Fatalf("want exactly 1 assignment bead, got %d: %+v", len(beads), beads)
	}
	return beads[0]
}

// ---- per-shape effective-bind contract tests ----

// TestSlingWebArgvContract_RootcauseAllShapeBindsIssue pins the rootcause-all shape: the task
// binds `issue` via an assignment bead, and the step renders that bead's ID (NOT the task text).
func TestSlingWebArgvContract_RootcauseAllShapeBindsIssue(t *testing.T) {
	store, stepIDs := instantiateWebArgvShape(t, "wac-issue-cli", issueVarTOML("wac-issue-cli"), webTask)

	bead := assignmentBead(t, store)
	if bead.Description != webTask {
		t.Errorf("assignment bead Description = %q, want the verbatim task %q", bead.Description, webTask)
	}

	step := renderedStep(t, store, stepIDs, "s1")
	// {{issue}} must resolve to the assignment-bead ID (a mem-N id), proving Mechanism 1 bound it.
	if !strings.Contains(step.Description, bead.ID) {
		t.Errorf("step {{issue}} should render assignment-bead id %q, got %q", bead.ID, step.Description)
	}
	// It must NOT be the literal task text — that would be the buggy "task passed as the value"
	// shape this contract exists to forbid.
	if strings.Contains(step.Description, webTask) {
		t.Errorf("step {{issue}} leaked the literal task %q instead of the bead id; got %q", webTask, step.Description)
	}
}

// TestSlingWebArgvContract_RapidSoldesignShapeBindsIssueUri pins the rapid-soldesign-plan shape:
// the single unsatisfied required input (issue_uri) receives the verbatim task via the input
// bridge, while the defaulted sibling input keeps its default.
func TestSlingWebArgvContract_RapidSoldesignShapeBindsIssueUri(t *testing.T) {
	store, stepIDs := instantiateWebArgvShape(t, "wac-input-bridge", inputBridgeTOML("wac-input-bridge"), webTask)

	step := renderedStep(t, store, stepIDs, "s1")
	// issue_uri binds the LITERAL task text (Mechanism 2, input bridge).
	if !strings.Contains(step.Description, webTask) {
		t.Errorf("step {{issue_uri}} should render the verbatim task %q, got %q", webTask, step.Description)
	}
	// The defaulted input keeps its default and is NOT bound to the task.
	if !strings.Contains(step.Description, "rootcause-all") {
		t.Errorf("step {{analyst_name}} should keep its default %q, got %q", "rootcause-all", step.Description)
	}
	// Mechanism 1 still creates an assignment bead (its guard ignores len(f.Inputs)); the
	// EFFECTIVE bind for this shape is the input bridge, so the bead's id must NOT be what
	// issue_uri rendered. (We assert the effective field, never the bead's absence.)
	bead := assignmentBead(t, store)
	if strings.Contains(step.Description, bead.ID) {
		t.Errorf("input-bridge shape must render the task into {{issue_uri}}, not the assignment-bead id %q; got %q", bead.ID, step.Description)
	}
}

// TestSlingWebArgvContract_DesignV7ShapeBindsIssueViaUniversalOverride pins the design-v7 shape:
// even though `issue` declares the hidden hook_bead source and no hooked-formula file exists, the
// task binds `issue` via the assignment bead and the universal CLI override (vars.go:64-70) — NOT
// via a hook file.
func TestSlingWebArgvContract_DesignV7ShapeBindsIssueViaUniversalOverride(t *testing.T) {
	store, stepIDs := instantiateWebArgvShape(t, "wac-issue-hookbead", hookBeadIssueTOML("wac-issue-hookbead"), webTask)

	bead := assignmentBead(t, store)
	if bead.Description != webTask {
		t.Errorf("assignment bead Description = %q, want the verbatim task %q", bead.Description, webTask)
	}

	step := renderedStep(t, store, stepIDs, "s1")
	// The universal override resolves issue to the assignment-bead id despite the hook_bead source.
	if !strings.Contains(step.Description, bead.ID) {
		t.Errorf("hook_bead-sourced {{issue}} should still resolve to assignment-bead id %q via the CLI override, got %q", bead.ID, step.Description)
	}
	// It must not have stayed an unresolved literal or leaked the raw task.
	if strings.Contains(step.Description, "{{issue}}") {
		t.Errorf("hook_bead-sourced {{issue}} stayed unresolved; got %q", step.Description)
	}
	if strings.Contains(step.Description, webTask) {
		t.Errorf("step {{issue}} leaked the literal task %q instead of the bead id; got %q", webTask, step.Description)
	}
}

// TestSlingWebArgvContract_DashPrefixedTaskReachesBoundFieldIntact proves a dash-prefixed task
// (the kind the web emits after the `--` terminator so af-core's cobra.MaximumNArgs(1) parse
// does not treat it as a flag) reaches the bound field byte-for-byte through the af-core binder.
func TestSlingWebArgvContract_DashPrefixedTaskReachesBoundFieldIntact(t *testing.T) {
	const dashTask = "-n drop tables"

	// Input-bridge shape: the literal task lands in {{issue_uri}}, so we can assert it verbatim.
	store, stepIDs := instantiateWebArgvShape(t, "wac-dash-bridge", inputBridgeTOML("wac-dash-bridge"), dashTask)
	step := renderedStep(t, store, stepIDs, "s1")
	if !strings.Contains(step.Description, dashTask) {
		t.Errorf("dash-prefixed task should reach {{issue_uri}} intact, got %q", step.Description)
	}

	// Assignment-bead shape: the dash task is preserved verbatim in the assignment bead Description.
	store2, _ := instantiateWebArgvShape(t, "wac-dash-issue", issueVarTOML("wac-dash-issue"), dashTask)
	bead := assignmentBead(t, store2)
	if bead.Description != dashTask {
		t.Errorf("assignment bead Description = %q, want the dash-prefixed task %q", bead.Description, dashTask)
	}
}

// TestSlingWebArgvContract_EmptyTaskRejected pins the AC-2 gate: specialist dispatch requires a
// non-empty positional task. validateSlingArgs is the af-core guard the web's argv must satisfy.
func TestSlingWebArgvContract_EmptyTaskRejected(t *testing.T) {
	for _, args := range [][]string{nil, {""}, {"   "}} {
		err := validateSlingArgs("", "rootcause-all", args)
		if err == nil {
			t.Errorf("validateSlingArgs with args=%q should reject an empty task", args)
			continue
		}
		if !strings.Contains(err.Error(), "task description required") {
			t.Errorf("validateSlingArgs error = %q, want it to mention 'task description required'", err.Error())
		}
	}
	// A non-empty task is accepted (the happy path the web always sends).
	if err := validateSlingArgs("", "rootcause-all", []string{webTask}); err != nil {
		t.Errorf("validateSlingArgs with a real task should succeed, got %v", err)
	}
}
