package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// errOnCreateMatchStore wraps an issuestore.Store and fails Create only when a
// predicate matches the params (e.g. the resolved_vars carrier). All other
// methods delegate to the inner store. Mirrors the errOn*Store pattern
// (done_test.go:22-43); used to exercise the fail-open carrier write.
type errOnCreateMatchStore struct {
	issuestore.Store
	failIf func(issuestore.CreateParams) bool
	err    error
}

func (s *errOnCreateMatchStore) Create(ctx context.Context, params issuestore.CreateParams) (issuestore.Issue, error) {
	if s.failIf != nil && s.failIf(params) {
		return issuestore.Issue{}, s.err
	}
	return s.Store.Create(ctx, params)
}

const resolvedVarsTOML = `
formula = "rv-test"
type = "workflow"
version = 1

[inputs.issue]
description = "Issue ID"
type = "string"
required = true

[[steps]]
id = "s1"
title = "do {{issue}}"
description = "work on {{issue}}"
`

// TestSling_PersistsResolvedVars verifies that after instantiation, a dedicated
// child bead (Parent = instance, labeled resolved-vars) holds the resolved --var
// map as JSON in its Description — and the read path surfaces it.
func TestSling_PersistsResolvedVars(t *testing.T) {
	mem := installMemStore(t)
	root, agentDir := createTestFormulaFactoryWithTOML(t, "rv-test", "rv-agent", resolvedVarsTOML)

	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "rv-test",
		AgentName:   "rv-agent",
		Root:        root,
		WorkDir:     agentDir,
		CLIVars:     []string{"issue=bd-42"},
	}
	var buf bytes.Buffer
	instanceID, _, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiateFormulaWorkflow: %v", err)
	}

	// The carrier bead exists, is keyed to the instance by label (NOT Parent —
	// so it stays out of the formula's step DAG), and stores the resolved --var
	// in Description (JSON).
	carriers, err := mem.List(t.Context(), issuestore.Filter{
		Labels:           []string{resolvedVarsInstanceLabel(instanceID)},
		IncludeAllAgents: true,
		IncludeClosed:    true,
	})
	if err != nil {
		t.Fatalf("list carriers: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("want exactly 1 resolved_vars carrier, got %d", len(carriers))
	}
	carrier := carriers[0]
	if carrier.Parent != "" {
		t.Errorf("carrier.Parent = %q, want empty (carrier must NOT be a formula-instance child)", carrier.Parent)
	}
	if carrier.Notes != "" {
		t.Errorf("carrier must NOT use Notes (GAP-1 clobber-prone); got Notes=%q", carrier.Notes)
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(carrier.Description), &got); err != nil {
		t.Fatalf("carrier Description is not the resolved_vars JSON map: %q (%v)", carrier.Description, err)
	}
	if got["issue"] != "bd-42" {
		t.Errorf("resolved_vars[issue] = %q, want %q (full map: %v)", got["issue"], "bd-42", got)
	}

	// The carrier is closed so it never lingers in open-work listings.
	if !carrier.Status.IsTerminal() {
		t.Errorf("carrier status = %q, want terminal (closed)", carrier.Status)
	}

	// The read path surfaces it.
	inputs := readResolvedVars(t.Context(), mem, instanceID, "")
	if inputs["issue"] != "bd-42" {
		t.Errorf("surfaced inputs[issue] = %q, want bd-42", inputs["issue"])
	}

	// The carrier is NOT among the instance's ready steps, and — crucially — does
	// NOT inflate Ready.TotalSteps (which counts children by Parent). The rv-test
	// formula has exactly one step, so TotalSteps must be 1, not 2. This is the
	// regression that keying by Parent would have caused in af prime's
	// "Step X of N" progress display on every slung formula.
	ready, err := mem.Ready(t.Context(), issuestore.Filter{MoleculeID: instanceID})
	if err != nil {
		t.Fatalf("Ready: %v", err)
	}
	for _, s := range ready.Steps {
		if s.ID == carrier.ID {
			t.Errorf("resolved_vars carrier leaked into the formula's ready steps")
		}
	}
	if ready.TotalSteps != 1 {
		t.Errorf("Ready.TotalSteps = %d, want 1 (carrier must not be counted as a formula step)", ready.TotalSteps)
	}
}

// TestDispatchSling_PersistsResolvedVars proves the autonomous dispatch path —
// which auto-slings via `af sling --agent <name> "<task>"` through
// dispatchToSpecialist → instantiateFormulaWorkflow — also persists the carrier
// (FR-2: the single write covers both interactive and dispatched runs).
func TestDispatchSling_PersistsResolvedVars(t *testing.T) {
	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	mem := installMemStore(t)
	installNoopLaunchSession(t)

	root, _ := createTestFormulaFactoryWithTOML(t, "rv-test", "rv-agent", resolvedVarsTOML)
	// dispatchToSpecialist requires the agent to carry a formula field — the
	// helper writes a formula-less entry, so overwrite agents.json with one.
	os.WriteFile(filepath.Join(root, ".agentfactory", "agents.json"),
		[]byte(`{"agents":{"rv-agent":{"type":"autonomous","description":"d","formula":"rv-test"}}}`), 0o644)

	origVars := slingVars
	slingVars = []string{"issue=bd-77"}
	t.Cleanup(func() { slingVars = origVars })

	cmd := &cobra.Command{}
	cmd.SetContext(t.Context())
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	callerWd := filepath.Join(root, ".agentfactory", "agents", "caller-agent")
	os.MkdirAll(callerWd, 0o755)

	if err := dispatchToSpecialist(cmd, root, callerWd, "rv-agent", "implement issue #77"); err != nil {
		t.Fatalf("dispatchToSpecialist: %v", err)
	}

	// Find the formula-instance, then its resolved_vars carrier.
	instances, err := mem.List(t.Context(), issuestore.Filter{
		Labels:           []string{"formula-instance"},
		IncludeAllAgents: true,
	})
	if err != nil || len(instances) == 0 {
		t.Fatalf("no formula-instance bead created (err=%v)", err)
	}
	carriers, err := mem.List(t.Context(), issuestore.Filter{
		Labels:           []string{resolvedVarsInstanceLabel(instances[0].ID)},
		IncludeAllAgents: true,
		IncludeClosed:    true,
	})
	if err != nil {
		t.Fatalf("list carriers: %v", err)
	}
	if len(carriers) != 1 {
		t.Fatalf("dispatch-slung instance must carry resolved_vars; got %d carriers", len(carriers))
	}
	var got map[string]string
	if err := json.Unmarshal([]byte(carriers[0].Description), &got); err != nil {
		t.Fatalf("carrier Description not JSON: %q", carriers[0].Description)
	}
	if got["issue"] != "bd-77" {
		t.Errorf("dispatched resolved_vars[issue] = %q, want bd-77 (map: %v)", got["issue"], got)
	}
}

// TestInstantiate_SucceedsWhenResolvedVarsWriteFails is the fail-open guard
// (H-P2): when the resolved_vars carrier write errors, instantiation still
// succeeds exactly as before (no AC-8 regression), the failure is logged, and no
// carrier is left behind.
func TestInstantiate_SucceedsWhenResolvedVarsWriteFails(t *testing.T) {
	mem := memstore.New()
	failing := &errOnCreateMatchStore{
		Store: mem,
		failIf: func(p issuestore.CreateParams) bool {
			for _, l := range p.Labels {
				if l == resolvedVarsLabel {
					return true
				}
			}
			return false
		},
		err: errors.New("injected carrier write failure"),
	}
	orig := newIssueStore
	newIssueStore = func(wd, actor string) (issuestore.Store, error) { return failing, nil }
	t.Cleanup(func() { newIssueStore = orig })

	root, agentDir := createTestFormulaFactoryWithTOML(t, "rv-test", "rv-agent", resolvedVarsTOML)
	params := InstantiateParams{
		Ctx:         t.Context(),
		FormulaName: "rv-test",
		AgentName:   "rv-agent",
		Root:        root,
		WorkDir:     agentDir,
		CLIVars:     []string{"issue=bd-9"},
	}
	var buf bytes.Buffer
	instanceID, stepIDs, _, err := instantiateFormulaWorkflow(params, &buf)
	if err != nil {
		t.Fatalf("instantiation must succeed even when the carrier write fails: %v", err)
	}
	if instanceID == "" || len(stepIDs) == 0 {
		t.Fatalf("instantiation produced no instance/steps (instance=%q steps=%d)", instanceID, len(stepIDs))
	}

	// No carrier was persisted (the write failed); the read path degrades cleanly.
	carriers, _ := mem.List(t.Context(), issuestore.Filter{
		Labels:           []string{resolvedVarsInstanceLabel(instanceID)},
		IncludeAllAgents: true,
		IncludeClosed:    true,
	})
	if len(carriers) != 0 {
		t.Errorf("expected no carrier after injected failure, got %d", len(carriers))
	}
	if inputs := readResolvedVars(t.Context(), mem, instanceID, ""); len(inputs) != 0 {
		t.Errorf("expected empty inputs after injected failure, got %v", inputs)
	}

	// The failure was surfaced, not silently dropped.
	if !strings.Contains(buf.String(), "could not persist resolved_vars") {
		t.Errorf("expected a fail-open warning in output, got: %q", buf.String())
	}
}
