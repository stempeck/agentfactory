package cmd

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)



// setupTestFactoryForStep creates a minimal factory layout sufficient for
// step.go's runStepCurrent: .agentfactory/factory.json at the tempdir root,
// plus .agentfactory/store/ so config.StoreDir(factoryRoot) resolves. Returns
// the tempdir path; the caller is expected to call installMemStore(t)
// separately (which creates the memstore AND installs the seam override).
func setupTestFactoryForStep(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	if err := os.MkdirAll(config.StoreDir(dir), 0o755); err != nil {
		t.Fatalf("mkdir store: %v", err)
	}
	return dir
}

// writeHookedFormula drops <dir>/.runtime/hooked_formula with the given id.
func writeHookedFormula(t *testing.T, dir, instanceID string) {
	t.Helper()
	runtimeDir := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatalf("mkdir .runtime: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(runtimeDir, "hooked_formula"),
		[]byte(instanceID),
		0o644,
	); err != nil {
		t.Fatalf("write hooked_formula: %v", err)
	}
}

// captureStdout redirects os.Stdout through an os.Pipe while fn runs and
// returns everything written. Canonical pattern from
// install_integration_test.go:147-192, reduced to a single-stream helper.
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	done := make(chan []byte, 1)
	go func() {
		b, _ := io.ReadAll(r)
		done <- b
	}()

	fn()

	_ = w.Close()
	os.Stdout = origStdout
	return string(<-done)
}

// invokeStepCurrent runs runStepCurrent via currentCmd so cmd.Context() is
// wired. Returns captured stdout and the RunE error.
func invokeStepCurrent(t *testing.T) (string, error) {
	t.Helper()
	var runErr error
	out := captureStdout(t, func() {
		currentCmd.SetContext(t.Context())
		runErr = runStepCurrent(currentCmd, nil)
	})
	return out, runErr
}

func TestStepCurrent_NoFormula(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	installMemStore(t)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}
	if out != `{"state":"no_formula"}`+"\n" {
		t.Errorf("got %q, want %q", out, `{"state":"no_formula"}`+"\n")
	}
}

func TestStepCurrent_ReadyStep_NonGate(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	mem := installMemStore(t)

	ctx := t.Context()
	instance, err := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeEpic,
		Title: "my-workflow",
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	step, err := mem.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeTask,
		Title:       "Write foo",
		Description: "do foo",
		Parent:      instance.ID,
		Assignee:    "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}
	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if got := parsed["state"]; got != "ready" {
		t.Errorf("state = %v, want ready", got)
	}
	if got := parsed["id"]; got != step.ID {
		t.Errorf("id = %v, want %s", got, step.ID)
	}
	if got := parsed["title"]; got != "Write foo" {
		t.Errorf("title = %v, want Write foo", got)
	}
	if got := parsed["description"]; got != "do foo" {
		t.Errorf("description = %v, want do foo", got)
	}
	// is_gate uses omitempty: absent when false.
	if _, present := parsed["is_gate"]; present {
		t.Errorf("is_gate should be absent (omitempty) for non-gate step, got %v", parsed["is_gate"])
	}
	if got := parsed["gate_id"]; got != "" {
		t.Errorf("gate_id = %v, want empty", got)
	}
	if got := parsed["formula"]; got != "my-workflow" {
		t.Errorf("formula = %v, want my-workflow", got)
	}
}

func TestStepCurrent_ReadyStep_GateByDescription(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	mem := installMemStore(t)

	ctx := t.Context()
	instance, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeEpic,
		Title: "wf",
	})
	_, err := mem.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeTask,
		Title:       "Phase complete",
		Description: "Complete all work, then run af done --phase-complete --gate g1",
		Parent:      instance.ID,
		Assignee:    "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}
	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if got, _ := parsed["is_gate"].(bool); !got {
		t.Errorf("is_gate = %v, want true", parsed["is_gate"])
	}
	if got := parsed["gate_id"]; got != "" {
		t.Errorf("gate_id = %v, want empty (description-keyword path, no structural blocker)", got)
	}
}

func TestStepCurrent_ReadyStep_GateByBlocker(t *testing.T) {
	// Unit-tests firstOpenBlocker directly. The full-flow "ready step with
	// an open structural blocker" state is unreachable against either
	// memstore or mcpstore because Ready excludes any step whose deps are
	// non-terminal (memstore.go:205-212, contract-pinned). Constructing that state via the
	// subcommand would route through the `blocked` branch instead. This
	// test therefore exercises firstOpenBlocker in isolation — the helper
	// is what the ready branch of runStepCurrent calls to derive gate_id,
	// and pinning its open-blocker path here is the strongest unit-level
	// guarantee we can give. The terminal-blocker path is pinned via the
	// full subcommand flow in TestStepCurrent_ReadyStep_TerminalBlocker.
	mem := installMemStore(t)
	ctx := t.Context()

	step, err := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "Gated step",
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}
	blocker, err := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "Open blocker",
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := mem.DepAdd(ctx, step.ID, blocker.ID); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}

	if got := firstOpenBlocker(ctx, mem, step.ID); got != blocker.ID {
		t.Errorf("firstOpenBlocker = %q, want %q", got, blocker.ID)
	}
	if !isGateStep(ctx, mem, step.ID, "") {
		t.Error("isGateStep should return true for step with open structural blocker")
	}
}

func TestStepCurrent_ReadyStep_TerminalBlocker(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	mem := installMemStore(t)

	ctx := t.Context()
	instance, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeEpic,
		Title: "wf",
	})
	step, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeTask,
		Title:       "Step",
		Description: "plain description",
		Parent:      instance.ID,
		Assignee:    "AF_ACTOR",
	})
	blocker, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "Blocker",
	})
	if err := mem.DepAdd(ctx, step.ID, blocker.ID); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}
	if err := mem.Close(ctx, blocker.ID, ""); err != nil {
		t.Fatalf("Close blocker: %v", err)
	}
	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	// Terminal blocker: firstOpenBlocker returns "", isGateStep returns
	// false (since no open blockers and description lacks gate keywords).
	if _, present := parsed["is_gate"]; present {
		t.Errorf("is_gate should be absent (false via omitempty); got %v", parsed["is_gate"])
	}
	if got := parsed["gate_id"]; got != "" {
		t.Errorf("gate_id = %v, want empty for terminal blocker", got)
	}
}

func TestStepCurrent_AllComplete(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	mem := installMemStore(t)

	ctx := t.Context()
	instance, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeEpic,
		Title: "wf",
	})
	step, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Title:    "Step",
		Parent:   instance.ID,
		Assignee: "AF_ACTOR",
	})
	if err := mem.Close(ctx, step.ID, ""); err != nil {
		t.Fatalf("Close step: %v", err)
	}
	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}
	if out != `{"state":"all_complete"}`+"\n" {
		t.Errorf("got %q, want %q", out, `{"state":"all_complete"}`+"\n")
	}
}

func TestStepCurrent_Blocked(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	mem := installMemStore(t)

	ctx := t.Context()
	instance, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeEpic,
		Title: "wf",
	})
	step, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Title:    "Blocked step",
		Parent:   instance.ID,
		Assignee: "AF_ACTOR",
	})
	// External blocker (no parent → Ready does not return it when
	// filter.MoleculeID == instance.ID).
	blocker, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "External blocker",
	})
	if err := mem.DepAdd(ctx, step.ID, blocker.ID); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}
	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}
	if out != `{"state":"blocked"}`+"\n" {
		t.Errorf("got %q, want %q", out, `{"state":"blocked"}`+"\n")
	}
}

func TestStepCurrent_FormulaTitleFallback(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	mem := installMemStore(t)

	ctx := t.Context()
	// Instance with EMPTY title — fallback path.
	instance, _ := mem.Create(ctx, issuestore.CreateParams{
		Type: issuestore.TypeEpic,
		// Title intentionally empty
	})
	_, _ = mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Title:    "Step",
		Parent:   instance.ID,
		Assignee: "AF_ACTOR",
	})
	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if got := parsed["formula"]; got != instance.ID {
		t.Errorf("formula = %v, want fallback to instance.ID %q", got, instance.ID)
	}
}

func TestStepCurrent_DescriptionPassthrough(t *testing.T) {
	// C-DATA-2 / R-DATA-4: description content must pass through the JSON
	// layer untouched. Shell-command substitution must not be evaluated.
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	mem := installMemStore(t)

	canary := filepath.Join(t.TempDir(), "pwned-step-test-canary")
	payload := "$(touch " + canary + ")"

	ctx := t.Context()
	instance, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeEpic,
		Title: "wf",
	})
	_, _ = mem.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeTask,
		Title:       "Step",
		Description: payload,
		Parent:      instance.ID,
		Assignee:    "AF_ACTOR",
	})
	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}

	// 1. Literal passes through.
	if !strings.Contains(out, payload) {
		t.Errorf("output %q does not contain payload %q", out, payload)
	}

	// 2. Canary file NOT created — the shell command was not evaluated.
	if _, err := os.Stat(canary); !os.IsNotExist(err) {
		t.Errorf("canary file %q should not exist; os.Stat err = %v", canary, err)
	}

	// 3. JSON round-trips cleanly.
	var parsed stepCurrentOutput
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	if parsed.Description != payload {
		t.Errorf("round-trip description = %q, want %q", parsed.Description, payload)
	}
}

func TestStepCurrent_SchemaSnapshot(t *testing.T) {
	// AC1.8: pin the JSON key set for a ready GATE step (is_gate == true
	// via description keyword). Because IsGate uses omitempty, a non-gate
	// ready step only emits 6 keys; seeding a gate step via the
	// description-keyword path ensures all 7 schema keys are present.
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)
	mem := installMemStore(t)

	ctx := t.Context()
	instance, _ := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeEpic,
		Title: "wf",
	})
	_, _ = mem.Create(ctx, issuestore.CreateParams{
		Type:        issuestore.TypeTask,
		Title:       "Gate step",
		Description: "run af done --phase-complete --gate g",
		Parent:      instance.ID,
		Assignee:    "AF_ACTOR",
	})
	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent: %v", err)
	}

	var keys map[string]json.RawMessage
	if err := json.Unmarshal([]byte(strings.TrimRight(out, "\n")), &keys); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}

	want := map[string]bool{
		"state":       true,
		"id":          true,
		"title":       true,
		"description": true,
		"is_gate":     true,
		"gate_id":     true,
		"formula":     true,
	}
	if len(keys) != len(want) {
		t.Errorf("key count = %d (%v), want %d (%v)", len(keys), keysOf(keys), len(want), keysOf(wantSet(want)))
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

// TestEmitError_Shape pins the error-state JSON shape at
// {"state":"error","error":"<msg>"}. This test is intentionally NOT in
// the AC1.10 canonical 11-test list (note the TestEmitError_ prefix,
// not TestStepCurrent_) because the spec does not prescribe error-shape
// coverage — the blind review (todos/ultra-implement/blind_review.md)
// flagged that the stepCurrentOutput GateID no-omitempty deviation
// could leak a spurious `"gate_id":""` key into error output if
// emitError were routed through emitState. step.go now uses a dedicated
// stepErrorOutput struct; this test pins the contract directly by
// calling emitError with a sentinel error and asserting the exact
// 2-key JSON shape.
func TestEmitError_Shape(t *testing.T) {
	out := captureStdout(t, func() {
		_ = emitError(errors.New("boom"))
	})
	trimmed := strings.TrimRight(out, "\n")
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal([]byte(trimmed), &parsed); err != nil {
		t.Fatalf("unmarshal %q: %v", out, err)
	}
	want := map[string]bool{"state": true, "error": true}
	if len(parsed) != len(want) {
		t.Errorf("key count = %d, want %d; got keys %v", len(parsed), len(want), keysOf(parsed))
	}
	for k := range parsed {
		if !want[k] {
			t.Errorf("unexpected key %q — error-state must not leak gate_id, id, title, etc.", k)
		}
	}
	var state, errMsg string
	if err := json.Unmarshal(parsed["state"], &state); err != nil {
		t.Errorf("unmarshal state: %v", err)
	}
	if err := json.Unmarshal(parsed["error"], &errMsg); err != nil {
		t.Errorf("unmarshal error: %v", err)
	}
	if state != "error" {
		t.Errorf("state = %q, want %q", state, "error")
	}
	if errMsg != "boom" {
		t.Errorf("error = %q, want %q", errMsg, "boom")
	}
}

func keysOf[T any](m map[string]T) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func wantSet(m map[string]bool) map[string]struct{} {
	out := make(map[string]struct{}, len(m))
	for k := range m {
		out[k] = struct{}{}
	}
	return out
}

func TestStepCurrent_ListError_EmitsErrorNotAllComplete(t *testing.T) {
	dir := setupTestFactoryForStep(t)
	t.Chdir(dir)

	mem := memstore.New()
	ctx := t.Context()

	instance, err := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeEpic,
		Title: "wf",
	})
	if err != nil {
		t.Fatalf("create instance: %v", err)
	}
	step, err := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Title:    "Blocked step",
		Parent:   instance.ID,
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("create step: %v", err)
	}
	blocker, err := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "External blocker",
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := mem.DepAdd(ctx, step.ID, blocker.ID); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}

	failStore := &errOnListStore{
		Store:   mem,
		listErr: errors.New("transient MCP server failure"),
	}
	origNewIssueStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return failStore, nil }
	defer func() { newIssueStore = origNewIssueStore }()

	writeHookedFormula(t, dir, instance.ID)

	out, err := invokeStepCurrent(t)
	if err != nil {
		t.Fatalf("runStepCurrent returned unexpected error: %v", err)
	}
	if strings.Contains(out, "all_complete") {
		t.Errorf("store.List error must NOT produce all_complete; got: %s", out)
	}
	if !strings.Contains(out, `"state":"error"`) {
		t.Errorf("store.List error should produce error state JSON; got: %s", out)
	}
	if !strings.Contains(out, "listing open children") {
		t.Errorf("error message should contain 'listing open children'; got: %s", out)
	}
}
