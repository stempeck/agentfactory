package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/session"
)

// writeAgentsJSON drops <dir>/.agentfactory/agents.json with the given body.
func writeAgentsJSON(t *testing.T, dir, body string) {
	t.Helper()
	path := config.AgentsConfigPath(dir)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write agents.json: %v", err)
	}
}

// installFakeTmuxPresent overrides newCmdTmux so the named tmux sessions report
// present (running). Restored via t.Cleanup.
func installFakeTmuxPresent(t *testing.T, sessions ...string) *fakeTmux {
	t.Helper()
	fake := newFakeTmux()
	for _, s := range sessions {
		fake.present[s] = true
	}
	orig := newCmdTmux
	newCmdTmux = func() cmdTmux { return fake }
	t.Cleanup(func() { newCmdTmux = orig })
	return fake
}

// invokeAgentsList runs runAgentsList via agentsListCmd so cmd.Context() is wired.
func invokeAgentsList(t *testing.T) string {
	t.Helper()
	// agentsListCmd inherits rootCmd's writer, and helpers such as execMemoryOut leave rootCmd
	// pointed at their own buffer, which would swallow the output captured here.
	rootCmd.SetOut(nil)
	var runErr error
	out := captureStdout(t, func() {
		agentsListCmd.SetContext(t.Context())
		runErr = runAgentsList(agentsListCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("runAgentsList: %v", runErr)
	}
	return out
}

// TestAgentsList_ErrorState pins the "always exit 0, encode errors in state"
// contract: an infrastructure failure (no factory root) yields a
// {"state":"error",...} envelope with a nil RunE error, never a crash or nonzero
// exit — so callers branch on the output shape, not the exit code.
func TestAgentsList_ErrorState(t *testing.T) {
	t.Chdir(t.TempDir()) // no .agentfactory ⇒ FindFactoryRoot fails

	var runErr error
	out := captureStdout(t, func() {
		agentsListCmd.SetContext(t.Context())
		runErr = runAgentsList(agentsListCmd, nil)
	})
	if runErr != nil {
		t.Fatalf("runAgentsList must return nil (errors go in the envelope), got %v", runErr)
	}
	var env map[string]string
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &env); err != nil {
		t.Fatalf("output is not a JSON object: %q (%v)", out, err)
	}
	if env["state"] != "error" || env["error"] == "" {
		t.Errorf("want {state:error, error:<msg>}, got %q", out)
	}
}

// TestDeriveAgentStatus pins the honest-status mapping at the value level — most
// importantly that a running agent with no active formula is "idle", never
// "working" (AC-1's load-bearing semantic), and that a gate step reports "gated".
func TestDeriveAgentStatus(t *testing.T) {
	cases := []struct {
		running   bool
		stepState string
		isGate    bool
		want      string
	}{
		{false, "ready", false, "stopped"},
		{false, "no_formula", false, "stopped"},
		{true, "no_formula", false, "idle"}, // honesty: running session ≠ working
		{true, "all_complete", false, "idle"},
		{true, "ready", false, "working"},
		{true, "ready", true, "gated"},
		{true, "blocked", false, "blocked"},
	}
	for _, c := range cases {
		if got := deriveAgentStatus(c.running, c.stepState, c.isGate); got != c.want {
			t.Errorf("deriveAgentStatus(%v, %q, %v) = %q, want %q", c.running, c.stepState, c.isGate, got, c.want)
		}
	}
}

// TestAgentsList_HonestStatusAndInputs is the end-to-end VALUE guard for AC-1:
// a running agent with no active formula renders Idle (not Working), a running
// agent parked at a gate renders Gated with its current step, and the persisted
// resolved_vars are surfaced as inputs. A key-only snapshot would miss a
// regression in any of these values.
func TestAgentsList_HonestStatusAndInputs(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{`+
		`"gatedworker":{"type":"autonomous","description":"g","formula":"minimalworker"},`+
		`"idleworker":{"type":"autonomous","description":"i"}}}`)
	mem := installMemStore(t)
	installFakeTmuxPresent(t, session.SessionName("gatedworker"), session.SessionName("idleworker"))

	ctx := t.Context()
	inst, err := mem.Create(ctx, issuestore.CreateParams{
		Type: issuestore.TypeEpic, Title: "Formula: minimalworker",
		Labels: []string{"formula-instance"}, Assignee: "gatedworker",
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	if _, err := mem.Create(ctx, issuestore.CreateParams{
		Type: issuestore.TypeTask, Title: "Gate step",
		Description: "run af done --phase-complete --gate g",
		Parent:      inst.ID, Assignee: "gatedworker", Labels: []string{"formula-step"},
	}); err != nil {
		t.Fatalf("create step: %v", err)
	}
	if _, err := mem.Create(ctx, issuestore.CreateParams{
		Type: issuestore.TypeTask, Title: "resolved-vars",
		Description: `{"issue":"407"}`, Assignee: "gatedworker",
		Labels: []string{resolvedVarsLabel, resolvedVarsInstanceLabel(inst.ID)},
	}); err != nil {
		t.Fatalf("create carrier: %v", err)
	}

	out := invokeAgentsList(t)
	var items []agentListItem
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &items); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	byName := map[string]agentListItem{}
	for _, it := range items {
		byName[it.Name] = it
	}

	gated, ok := byName["gatedworker"]
	if !ok {
		t.Fatalf("gatedworker missing from output %q", out)
	}
	if !gated.Running || gated.Status != "gated" || !gated.IsGate {
		t.Errorf("gatedworker = {running:%v status:%q is_gate:%v}, want {true gated true}", gated.Running, gated.Status, gated.IsGate)
	}
	if gated.Inputs["issue"] != "407" {
		t.Errorf("gatedworker inputs = %v, want issue=407", gated.Inputs)
	}

	idle, ok := byName["idleworker"]
	if !ok {
		t.Fatalf("idleworker missing from output %q", out)
	}
	if !idle.Running || idle.Status != "idle" || idle.StepState != "no_formula" {
		t.Errorf("idleworker = {running:%v status:%q step_state:%q}, want {true idle no_formula} — a running session must NOT read as working", idle.Running, idle.Status, idle.StepState)
	}
}

// TestAgentsList_CrossActor_ReportsOwnReadyStep is the behavioral RED→GREEN guard
// for issue #458 Phase 2 (AC-1's step clause). `af agents list` runs in the
// operator/webui process, whose AF_ACTOR (the store actor) is frequently a
// DIFFERENT agent than the one being listed. Steps under a formula instance carry
// their own agent's Assignee (5f0eb29), and populateAgentStep's step query —
// store.Ready(Filter{MoleculeID: inst.ID}) — has no explicit Assignee, so without
// IncludeAllAgents:true the actor overlay (memstore.go:172; mcpstore listArgs)
// hides alice's step from operator "manager" → 0 ready steps → the row falls
// through to step_state "blocked" with an empty step_id (the exact #458 symptom:
// the Floor shows the wrong/empty step). With the flag, Ready is actor-independent
// and reports alice's OWN ready step. RED before the agents.go Ready fix, GREEN
// after. memstore honors the overlay only when built WITH an actor
// (installMemStoreWithActor) — which is why the empty-actor installMemStore tests
// above are green before AND after and cannot reproduce this. The formula column
// (primary List) still resolves via its explicit Assignee regardless of overlay,
// isolating this assertion to the step columns this phase fixes.
func TestAgentsList_CrossActor_ReportsOwnReadyStep(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{"alice":{"type":"autonomous","description":"a","formula":"minimalworker"}}}`)
	mem := installMemStoreWithActor(t, "manager") // operator actor != alice
	installFakeTmuxPresent(t, session.SessionName("alice"))

	ctx := t.Context()
	inst, err := mem.Create(ctx, issuestore.CreateParams{
		Type: issuestore.TypeEpic, Title: "Formula: minimalworker",
		Labels: []string{"formula-instance"}, Assignee: "alice",
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	step, err := mem.Create(ctx, issuestore.CreateParams{
		Type: issuestore.TypeTask, Title: "Build the thing",
		Parent: inst.ID, Assignee: "alice", Labels: []string{"formula-step"},
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}

	out := invokeAgentsList(t)
	var items []agentListItem
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &items); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	var alice agentListItem
	found := false
	for _, it := range items {
		if it.Name == "alice" {
			alice, found = it, true
		}
	}
	if !found {
		t.Fatalf("alice missing from output %q", out)
	}
	// Precondition: the instance (formula column) resolves via the primary List's
	// explicit Assignee regardless of the overlay — guards the fixture is wired right.
	if alice.Formula != "minimalworker" {
		t.Fatalf("precondition: alice.Formula = %q, want minimalworker (instance not resolved)", alice.Formula)
	}
	if alice.StepState != "ready" {
		t.Errorf("alice.StepState = %q, want \"ready\" — operator actor \"manager\" must NOT hide alice's own ready step", alice.StepState)
	}
	if alice.StepID != step.ID {
		t.Errorf("alice.StepID = %q, want %q (alice's own ready step)", alice.StepID, step.ID)
	}
	if alice.StepTitle != "Build the thing" {
		t.Errorf("alice.StepTitle = %q, want \"Build the thing\"", alice.StepTitle)
	}
}

// TestAgentsList_JSON_SchemaSnapshot pins the per-agent JSON key set for an
// agent that is running and parked at a GATE step with persisted inputs — the
// maximal shape (all keys present). A field added/removed/renamed fails CI,
// mirroring TestStepCurrent_SchemaSnapshot (step_test.go:441).
func TestAgentsList_JSON_SchemaSnapshot(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{"worker":{"type":"autonomous","description":"d","formula":"minimalworker"}}}`)
	mem := installMemStore(t)
	installFakeTmuxPresent(t, session.SessionName("worker"))

	ctx := t.Context()
	instance, err := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Formula: minimalworker",
		Labels:   []string{"formula-instance"},
		Assignee: "worker",
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	// A gate step (description keyword ⇒ isGateStep true) so is_gate is emitted.
	if _, err := mem.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeTask,
		Title:       "Gate step",
		Description: "run af done --phase-complete --gate g",
		Parent:      instance.ID,
		Assignee:    "worker",
		Labels:      []string{"formula-step"},
	}); err != nil {
		t.Fatalf("create step: %v", err)
	}
	// The resolved_vars carrier bead — keyed to the instance by label, NOT Parent
	// (so it never appears among the instance's steps).
	if _, err := mem.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeTask,
		Title:       "resolved-vars",
		Description: `{"issue":"407"}`,
		Assignee:    "worker",
		Labels:      []string{resolvedVarsLabel, resolvedVarsInstanceLabel(instance.ID)},
	}); err != nil {
		t.Fatalf("create resolved-vars: %v", err)
	}

	out := invokeAgentsList(t)

	var arr []map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &arr); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if len(arr) != 1 {
		t.Fatalf("want 1 agent row, got %d: %q", len(arr), out)
	}
	keys := arr[0]

	want := map[string]bool{
		"name":          true,
		"type":          true,
		"formula":       true,
		"running":       true,
		"status":        true,
		"step_id":       true,
		"step_title":    true,
		"step_state":    true,
		"is_gate":       true,
		"gate_id":       true,
		"inputs":        true,
		"foreign_root":  true,
		"context_pct":   true,
		"context_state": true,
		"recovery":      true,
	}
	if len(keys) != len(want) {
		t.Errorf("key count = %d (%v), want %d", len(keys), keysOf(keys), len(want))
	}
	for k := range want {
		if _, ok := keys[k]; !ok {
			t.Errorf("missing key %q in output %q", k, out)
		}
	}
	for k := range keys {
		if !want[k] {
			t.Errorf("unexpected key %q in output %q", k, out)
		}
	}
}

// TestAgentsList_ForeignRoot (K9b, #519 Phase 3) proves foreign_root is true for a
// live session whose baked AF_ROOT resolves to a DIFFERENT factory than the
// querying root, and false for a session whose AF_ROOT matches (best-effort — an
// unset AF_ROOT also reads false).
func TestAgentsList_ForeignRoot(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	writeAgentsJSON(t, dir, `{"agents":{`+
		`"foreignagent":{"type":"autonomous","description":"f"},`+
		`"homeagent":{"type":"autonomous","description":"h"}}}`)
	installMemStore(t)
	fake := installFakeTmuxPresent(t, session.SessionName("foreignagent"), session.SessionName("homeagent"))

	otherRoot := t.TempDir()
	fake.env[session.SessionName("foreignagent")] = map[string]string{"AF_ROOT": otherRoot}
	fake.env[session.SessionName("homeagent")] = map[string]string{"AF_ROOT": dir}

	out := invokeAgentsList(t)
	var items []agentListItem
	if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &items); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	byName := map[string]agentListItem{}
	for _, it := range items {
		byName[it.Name] = it
	}
	if !byName["foreignagent"].ForeignRoot {
		t.Errorf("foreignagent.foreign_root = false, want true (AF_ROOT %q != querying root %q)", otherRoot, dir)
	}
	if byName["homeagent"].ForeignRoot {
		t.Errorf("homeagent.foreign_root = true, want false (AF_ROOT matches querying root)")
	}
}

// agwWindowsDir is the record directory spelled out, so a fixture never depends on the helper
// under test to find where it plants.
func agwWindowsDir(root string) string {
	return filepath.Join(root, ".agentfactory", "statusline", "windows")
}

func agwWriteJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestAgentsList_JSON_WindowFields pins the one documented exception to the stable key set: the
// two window keys appear only while this agent's own figure of its current launch disagrees with
// its declaration, and never on the strength of another agent's figure on the same identity.
func TestAgentsList_JSON_WindowFields(t *testing.T) {
	const (
		profile  = "fable-5-2"
		modelID  = "claude-fable-5-2"
		version  = "2.1.308"
		declared = int64(1000000)
	)
	now := time.Now().UTC().Truncate(time.Second)
	launched := now.Add(-time.Hour)
	key := version + "|1000000|" + modelID

	fact := map[string]any{
		"profile": profile, "model_id": modelID, "declared": declared,
		"harness_version": version, "launched_at": launched,
	}
	entry := func(agents map[string]any, applied int64) map[string]any {
		return map[string]any{
			"model_id": modelID, "harness_version": version, "declared": declared,
			"applied": applied, "agents": agents, "first_seen": launched, "last_seen": now,
			"sessions": len(agents), "source": "session",
		}
	}
	figure := func(applied int64) map[string]any {
		return map[string]any{"applied": applied, "launched_at": launched, "last_seen": now}
	}

	tests := []struct {
		name        string
		plantFact   bool
		identities  map[string]any
		wantPresent bool
	}{
		{
			name:        "this agent's divergent figure shows both keys",
			plantFact:   true,
			identities:  map[string]any{key: entry(map[string]any{"worker": figure(200000)}, 200000)},
			wantPresent: true,
		},
		{
			name:       "no launch fact shows neither",
			identities: map[string]any{key: entry(map[string]any{"worker": figure(200000)}, 200000)},
		},
		{
			name:       "no entry for its identity shows neither",
			plantFact:  true,
			identities: map[string]any{"2.1.322|1000000|" + modelID: entry(map[string]any{"worker": figure(200000)}, 200000)},
		},
		{
			name:       "equal figures show neither",
			plantFact:  true,
			identities: map[string]any{key: entry(map[string]any{"worker": figure(declared)}, declared)},
		},
		{
			name:      "only another agent's divergent figure shows neither",
			plantFact: true,
			identities: map[string]any{key: entry(map[string]any{
				"worker": figure(declared),
				"other":  figure(200000),
			}, 200000)},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dir := setupTestFactoryForStep(t)
			t.Chdir(dir)
			writeAgentsJSON(t, dir, `{"agents":{"worker":{"type":"autonomous","description":"d"},"other":{"type":"autonomous","description":"d"}}}`)
			installMemStore(t)
			installFakeTmuxPresent(t, session.SessionName("worker"), session.SessionName("other"))
			if tc.plantFact {
				agwWriteJSON(t, filepath.Join(agwWindowsDir(dir), "launched", "worker.json"), fact)
			}
			agwWriteJSON(t, filepath.Join(agwWindowsDir(dir), profile+".json"), map[string]any{
				"profile": profile, "identities": tc.identities,
			})

			out := invokeAgentsList(t)
			var arr []map[string]json.RawMessage
			if err := json.Unmarshal([]byte(strings.TrimSpace(out)), &arr); err != nil {
				t.Fatalf("unmarshal %q: %v", out, err)
			}
			var row map[string]json.RawMessage
			for _, r := range arr {
				if string(r["name"]) == `"worker"` {
					row = r
				}
			}
			if row == nil {
				t.Fatalf("no worker row in %q", out)
			}
			gotDeclared, hasDeclared := row["window_declared"]
			gotApplied, hasApplied := row["window_applied"]
			if !tc.wantPresent {
				if hasDeclared || hasApplied {
					t.Errorf("window keys must be absent, got window_declared=%s window_applied=%s", gotDeclared, gotApplied)
				}
				return
			}
			if string(gotDeclared) != "1000000" {
				t.Errorf("window_declared = %q, want 1000000 (row %q)", gotDeclared, out)
			}
			if string(gotApplied) != "200000" {
				t.Errorf("window_applied = %q, want this agent's own figure 200000 (row %q)", gotApplied, out)
			}
		})
	}
}
