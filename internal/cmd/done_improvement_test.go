package cmd

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/memstore"
)

// --- #483 Phase 3: HOOK-1 / MSG-1 / DEL-1 test matrix (TEST-3) ---
//
// All fire-path tests call sendWorkDoneAndCleanup directly (subprocess-free) and
// rely on the isTestBinary() no-op seams so no mail/tmux is touched. The OFF-path
// golden is the mechanical interlock protecting the completion path.

// captureOutErr swaps BOTH os.Stdout and os.Stderr to pipes, each drained by its
// own goroutine (deadlock-safe), and returns (stdout, stderr).
func captureOutErr(t *testing.T, fn func()) (string, string) {
	t.Helper()
	origOut, origErr := os.Stdout, os.Stderr
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe out: %v", err)
	}
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("pipe err: %v", err)
	}
	os.Stdout, os.Stderr = wOut, wErr
	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() { b, _ := io.ReadAll(rOut); outCh <- string(b) }()
	go func() { b, _ := io.ReadAll(rErr); errCh <- string(b) }()
	fn()
	_ = wOut.Close()
	_ = wErr.Close()
	os.Stdout, os.Stderr = origOut, origErr
	return <-outCh, <-errCh
}

// testImprovementMarker mirrors the on-disk schema WITHOUT depending on the
// production type, so the golden/matrix tests describe the contract independently.
type testImprovementMarker struct {
	InstanceID          string `json:"instance_id"`
	Formula             string `json:"formula"`
	FormulaPath         string `json:"formula_path"`
	Caller              string `json:"caller"`
	TerminateOnComplete bool   `json:"terminate_on_complete"`
	FormulaSHA256       string `json:"formula_sha256"`
	FiredAt             string `json:"fired_at"`
}

const widgetFormulaTOML = "name = \"widget\"\n[[steps]]\nid = \"s1\"\n"

// setupImprovementFiringFactory builds a factory with the factory toggle ON, an
// agent "alpha" with continuous_improvement true, and a real widget formula file.
func setupImprovementFiringFactory(t *testing.T) string {
	t.Helper()
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	if err := os.WriteFile(improvementHookFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write factory toggle: %v", err)
	}
	fdir := config.FormulasDir(root)
	if err := os.MkdirAll(fdir, 0o755); err != nil {
		t.Fatalf("mkdir formulas: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fdir, "widget.formula.toml"), []byte(widgetFormulaTOML), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	return root
}

// seedCompletedFormula seeds an epic titled `title` with one closed step and
// returns the instance (epic) ID, so sendWorkDoneAndCleanup sees an all-complete
// molecule.
func seedCompletedFormula(t *testing.T, mem issuestore.Store, title string) string {
	t.Helper()
	ctx := t.Context()
	epic, err := mem.Create(ctx, issuestore.CreateParams{
		Title:  title,
		Type:   issuestore.TypeEpic,
		Labels: []string{"formula-instance"},
	})
	if err != nil {
		t.Fatalf("seed epic: %v", err)
	}
	step, err := mem.Create(ctx, issuestore.CreateParams{
		Title:    "Step 1",
		Parent:   epic.ID,
		Type:     issuestore.TypeTask,
		Labels:   []string{"formula-step"},
		Assignee: "AF_ACTOR",
	})
	if err != nil {
		t.Fatalf("seed step: %v", err)
	}
	if err := mem.Close(ctx, step.ID, ""); err != nil {
		t.Fatalf("close step: %v", err)
	}
	return epic.ID
}

func readMarker(t *testing.T, path string) testImprovementMarker {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read marker %s: %v", path, err)
	}
	var m testImprovementMarker
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("marker is not valid JSON (%v): %s", err, string(data))
	}
	return m
}

func skipFilePath(root, agent string) string {
	return filepath.Join(resolveAgentDir(root, agent), ".runtime", "improvement_skipped")
}

// --- Row E: (on,on) final ⇒ FIRE (non-dispatched) ---

func TestDone_ImprovementHook_FinalOnOn_Fires(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")
	// A held identity lock whose release must be DEFERRED on fire.
	writeRuntimeFile(t, cwd, "agent.lock", "held")

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	var mailedCaller string
	origMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error {
		mailedCaller = caller
		return nil
	}
	defer func() { sendWorkDoneMail = origMail }()

	var stdout string
	stdout, _ = captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	// Marker written at the resolved agent dir, valid 7-field JSON.
	markerPath := improvementPendingFile(root, "alpha")
	m := readMarker(t, markerPath)
	if m.InstanceID != instanceID {
		t.Errorf("marker.instance_id = %q, want %q", m.InstanceID, instanceID)
	}
	if m.Formula != "widget" {
		t.Errorf("marker.formula = %q, want %q (prefix stripped)", m.Formula, "widget")
	}
	if m.FormulaPath != ".agentfactory/store/formulas/widget.formula.toml" {
		t.Errorf("marker.formula_path = %q, want the stripped relative path", m.FormulaPath)
	}
	if strings.Contains(m.FormulaPath, "Formula: ") {
		t.Errorf("marker.formula_path still carries the 'Formula: ' prefix: %q", m.FormulaPath)
	}
	if m.Caller != "supervisor" {
		t.Errorf("marker.caller = %q, want %q", m.Caller, "supervisor")
	}
	if m.TerminateOnComplete != false {
		t.Errorf("marker.terminate_on_complete = %v, want false (original shouldTerminate for a non-dispatched session)", m.TerminateOnComplete)
	}
	wantSHA := fmt.Sprintf("%x", sha256.Sum256([]byte(widgetFormulaTOML)))
	if m.FormulaSHA256 != wantSHA {
		t.Errorf("marker.formula_sha256 = %q, want %q", m.FormulaSHA256, wantSHA)
	}
	if _, err := time.Parse(time.RFC3339, m.FiredAt); err != nil {
		t.Errorf("marker.fired_at = %q is not RFC3339: %v", m.FiredAt, err)
	}

	// Stdout instruction block: STRIPPED path + the completion command; NOT the raw title.
	if !strings.Contains(stdout, ".agentfactory/store/formulas/widget.formula.toml") {
		t.Errorf("stdout missing the stripped formula path:\n%s", stdout)
	}
	if !strings.Contains(stdout, "af improvement complete") {
		t.Errorf("stdout missing the completion command:\n%s", stdout)
	}
	if strings.Contains(stdout, "Formula: widget") {
		t.Errorf("stdout instruction leaked the raw 'Formula: ' title:\n%s", stdout)
	}

	// WORK_DONE mail still attempted.
	if mailedCaller != "supervisor" {
		t.Errorf("WORK_DONE mail caller = %q, want %q (the hook must not suppress it)", mailedCaller, "supervisor")
	}

	// Identity lock release suppressed (deferred to `af improvement complete`).
	if _, err := os.Stat(filepath.Join(cwd, ".runtime", "agent.lock")); err != nil {
		t.Errorf("identity lock should NOT be released on fire (deferred): %v", err)
	}
}

// --- Dispatched fire variant: terminate_on_complete=true, teardown suppressed ---

func TestDone_ImprovementHook_Dispatched_DefersTerminate(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")
	writeRuntimeFile(t, cwd, "dispatched", "1")
	writeRuntimeFile(t, cwd, "worktree_id", "wt-improve")
	writeRuntimeFile(t, cwd, "worktree_owner", "true")

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	origMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error { return nil }
	defer func() { sendWorkDoneMail = origMail }()

	_, stderr := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	m := readMarker(t, improvementPendingFile(root, "alpha"))
	if !m.TerminateOnComplete {
		t.Errorf("marker.terminate_on_complete = false, want true (dispatched + mail ok ⇒ original shouldTerminate true)")
	}
	// finishDispatchedSession must be suppressed: no worktree teardown activity.
	if strings.Contains(stderr, "worktree RemoveAgent") || strings.Contains(stderr, "worktree cleanup") {
		t.Errorf("teardown should be suppressed on fire, but stderr shows worktree activity:\n%s", stderr)
	}
}

// --- OFF-path byte-identity golden (the mechanical interlock) ---

func TestDone_OffPathIdentical_Golden(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	// Bare factory: NO .improvement-hook ⇒ improvementFactoryEnabled == false.
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")
	writeRuntimeFile(t, cwd, "hooked_formula", "x")
	writeRuntimeFile(t, cwd, "worktree_id", "wt-off")
	writeRuntimeFile(t, cwd, "worktree_owner", "true")

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	origMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error { return nil }
	defer func() { sendWorkDoneMail = origMail }()

	stdout, stderr := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	if stdout != "✓ All formula steps complete. WORK_DONE mailed.\n" {
		t.Errorf("OFF-path stdout changed:\n%q", stdout)
	}
	if stderr != "" {
		t.Errorf("OFF-path stderr must be empty, got:\n%q", stderr)
	}
	// No improvement artifacts anywhere on the OFF path.
	if _, err := os.Stat(improvementPendingFile(root, "alpha")); err == nil {
		t.Error("OFF path must NOT write improvement_pending")
	}
	if _, err := os.Stat(skipFilePath(root, "alpha")); err == nil {
		t.Error("OFF path must NOT write improvement_skipped (factory toggle off ⇒ nothing recorded)")
	}
	// Lock released, carrier files cleaned, worktree files preserved — unchanged behavior.
	if _, err := os.Stat(filepath.Join(cwd, ".runtime", "formula_caller")); err == nil {
		t.Error("formula_caller should have been cleaned")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".runtime", "worktree_id")); err != nil {
		t.Error("worktree_id must be preserved")
	}
}

// --- Row A: intermediate (non-final) af done ⇒ no fire ---

func TestDone_ImprovementHook_Intermediate_NoFire(t *testing.T) {
	t.Setenv("AF_ACTOR", "alpha")
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	ctx := t.Context()

	mem := memstore.NewWithActor("alpha")
	epic, err := mem.Create(ctx, issuestore.CreateParams{Title: "Formula: widget", Type: issuestore.TypeEpic, Labels: []string{"formula-instance"}, Assignee: "alpha"})
	if err != nil {
		t.Fatal(err)
	}
	s1, _ := mem.Create(ctx, issuestore.CreateParams{Title: "Step 1", Parent: epic.ID, Type: issuestore.TypeTask, Labels: []string{"formula-step"}, Assignee: "alpha"})
	s2, _ := mem.Create(ctx, issuestore.CreateParams{Title: "Step 2", Parent: epic.ID, Type: issuestore.TypeTask, Labels: []string{"formula-step"}, Assignee: "alpha"})
	if err := mem.DepAdd(ctx, s2.ID, s1.ID); err != nil {
		t.Fatal(err)
	}

	origStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origStore }()

	writeRuntimeFile(t, cwd, "hooked_formula", epic.ID)
	writeRuntimeFile(t, cwd, "step_primed", s1.ID)

	stdout, _ := captureOutErr(t, func() {
		if err := runDoneCore(ctx, cwd, false, ""); err != nil {
			t.Fatalf("runDoneCore: %v", err)
		}
	})

	if !strings.Contains(stdout, "Next step") {
		t.Errorf("intermediate done should print the next step:\n%s", stdout)
	}
	if strings.Contains(stdout, "IMPROVEMENT HOOK") {
		t.Errorf("intermediate done must not emit the instruction:\n%s", stdout)
	}
	if _, err := os.Stat(improvementPendingFile(root, "alpha")); err == nil {
		t.Error("intermediate done must not write a marker")
	}
}

// --- Row B: gate branch (--phase-complete) ⇒ no fire ---

func TestDone_ImprovementHook_GateBranch_NoFire(t *testing.T) {
	t.Setenv("AF_ACTOR", "alpha")
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	ctx := t.Context()

	mem := memstore.NewWithActor("alpha")
	epic, _ := mem.Create(ctx, issuestore.CreateParams{Title: "Formula: widget", Type: issuestore.TypeEpic, Labels: []string{"formula-instance"}, Assignee: "alpha"})
	s1, _ := mem.Create(ctx, issuestore.CreateParams{Title: "Step 1", Parent: epic.ID, Type: issuestore.TypeTask, Labels: []string{"formula-step"}, Assignee: "alpha"})
	gate, _ := mem.Create(ctx, issuestore.CreateParams{Title: "Gate", Parent: epic.ID, Type: issuestore.TypeTask, Labels: []string{"gate"}, Assignee: "alpha"})

	origStore := newIssueStore
	newIssueStore = func(_, _ string) (issuestore.Store, error) { return mem, nil }
	defer func() { newIssueStore = origStore }()

	writeRuntimeFile(t, cwd, "hooked_formula", epic.ID)
	writeRuntimeFile(t, cwd, "step_primed", s1.ID)

	stdout, _ := captureOutErr(t, func() {
		if err := runDoneCore(ctx, cwd, true, gate.ID); err != nil {
			t.Fatalf("runDoneCore (gate): %v", err)
		}
	})

	if !strings.Contains(stdout, "Phase complete") {
		t.Errorf("gate branch should print 'Phase complete':\n%s", stdout)
	}
	if strings.Contains(stdout, "IMPROVEMENT HOOK") {
		t.Errorf("gate branch must not emit the instruction:\n%s", stdout)
	}
	if _, err := os.Stat(improvementPendingFile(root, "alpha")); err == nil {
		t.Error("gate branch must not write a marker")
	}
}

// --- Row C: velocity-guard-blocked ⇒ no fire ---

func TestDone_ImprovementHook_VelocityBlocked_NoFire(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")
	// Three unprimed closes trips the completion-velocity guard.
	now := time.Now().UTC()
	velocity := fmt.Sprintf(`{"closes":[{"step_id":"a","was_primed":false,"closed_at":%q},{"step_id":"b","was_primed":false,"closed_at":%q},{"step_id":"c","was_primed":false,"closed_at":%q}]}`,
		now.Format(time.RFC3339), now.Format(time.RFC3339), now.Format(time.RFC3339))
	writeRuntimeFile(t, cwd, "done_velocity", velocity)

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	_, _ = captureOutErr(t, func() {
		err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID)
		if err == nil {
			t.Error("velocity guard should have blocked completion")
		}
	})

	if _, err := os.Stat(improvementPendingFile(root, "alpha")); err == nil {
		t.Error("velocity-blocked completion must not write a marker")
	}
}

// --- Row D: callerless completion ⇒ no fire ---

func TestDone_ImprovementHook_Callerless_NoFire(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	// No formula_caller ⇒ caller == "".

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	stdout, _ := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	if strings.Contains(stdout, "IMPROVEMENT HOOK") {
		t.Errorf("callerless completion must not emit the instruction:\n%s", stdout)
	}
	if _, err := os.Stat(improvementPendingFile(root, "alpha")); err == nil {
		t.Error("callerless completion must not write a marker")
	}
}

// --- Row F: pre-existing marker ⇒ no second fire (idempotence) ---

func TestDone_ImprovementHook_PreexistingMarker_NoSecondFire(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")

	// Pre-existing sentinel marker at the resolved path.
	markerPath := improvementPendingFile(root, "alpha")
	if err := os.MkdirAll(filepath.Dir(markerPath), 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := `{"fired_at":"SENTINEL"}` + "\n"
	if err := os.WriteFile(markerPath, []byte(sentinel), 0o644); err != nil {
		t.Fatal(err)
	}

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	origMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error { return nil }
	defer func() { sendWorkDoneMail = origMail }()

	stdout, _ := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	got, _ := os.ReadFile(markerPath)
	if string(got) != sentinel {
		t.Errorf("pre-existing marker must not be overwritten; got:\n%s", string(got))
	}
	if strings.Contains(stdout, "IMPROVEMENT HOOK") {
		t.Errorf("pre-existing marker must suppress the instruction:\n%s", stdout)
	}
}

// --- Fail-open: toggles-ON no-fire records a skip reason + completion proceeds ---

func TestImprovement_FailOpen_MissingFormula_SkipRecorded(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")

	mem := memstore.New()
	// Title references a formula whose file does NOT exist.
	instanceID := seedCompletedFormula(t, mem, "Formula: ghost")

	origMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error { return nil }
	defer func() { sendWorkDoneMail = origMail }()

	stdout, stderr := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	if _, err := os.Stat(improvementPendingFile(root, "alpha")); err == nil {
		t.Error("missing formula must not write a marker")
	}
	if _, err := os.Stat(skipFilePath(root, "alpha")); err != nil {
		t.Errorf("missing formula must record improvement_skipped: %v", err)
	}
	if strings.Contains(stdout, "IMPROVEMENT HOOK") {
		t.Errorf("no instruction should be emitted on a no-fire:\n%s", stdout)
	}
	// Completion still succeeds (fail-open).
	if !strings.Contains(stdout, "✓ All formula steps complete") {
		t.Errorf("completion path must proceed as off:\n%s", stdout)
	}
	if !strings.Contains(stderr, "improvement") {
		t.Errorf("a stderr warning should mention the improvement no-fire:\n%s", stderr)
	}
}

func TestImprovement_FailOpen_UnresolvableName_SkipRecorded(t *testing.T) {
	// AF_ROLE unset + cwd not under agents/ ⇒ detectAgentName errors.
	os.Unsetenv("AF_ROLE")
	root := setupImprovementFiringFactory(t)
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	origMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error { return nil }
	defer func() { sendWorkDoneMail = origMail }()

	stdout, _ := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	if strings.Contains(stdout, "IMPROVEMENT HOOK") {
		t.Errorf("unresolvable name must not fire:\n%s", stdout)
	}
	// A skip file is recorded (keyed on the empty/fallback agent dir).
	if _, err := os.Stat(skipFilePath(root, "")); err != nil {
		t.Errorf("unresolvable name must record improvement_skipped: %v", err)
	}
	if !strings.Contains(stdout, "✓ All formula steps complete") {
		t.Errorf("completion path must proceed as off:\n%s", stdout)
	}
}

// --- DEL-1: redundant delivery ordering + fail-soft ---

// seedFiringForDelivery wires the ON-path fixture and a captured WORK_DONE mail
// seam, returning the memstore + instance so a delivery test can drive the hook.
func seedFiringForDelivery(t *testing.T) (issuestore.Store, string, string) {
	t.Helper()
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	writeRuntimeFile(t, root, "formula_caller", "supervisor")
	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")
	origWD := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error { return nil }
	t.Cleanup(func() { sendWorkDoneMail = origWD })
	return mem, root, instanceID
}

func TestDone_ImprovementDelivery_MailBeforeNudge(t *testing.T) {
	mem, root, instanceID := seedFiringForDelivery(t)

	var order []string
	origMail, origNudge := sendImprovementMail, deliverImprovementNudge
	sendImprovementMail = func(agent, subject, instruction string) error { order = append(order, "mail"); return nil }
	deliverImprovementNudge = func(sessionName, message string) error { order = append(order, "nudge"); return nil }
	defer func() { sendImprovementMail = origMail; deliverImprovementNudge = origNudge }()

	_, _ = captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, root, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	if len(order) != 2 || order[0] != "mail" || order[1] != "nudge" {
		t.Errorf("delivery order = %v, want [mail nudge]", order)
	}
}

func TestDone_ImprovementDelivery_MailFails_NudgeStillFires(t *testing.T) {
	mem, root, instanceID := seedFiringForDelivery(t)

	nudged := false
	origMail, origNudge := sendImprovementMail, deliverImprovementNudge
	sendImprovementMail = func(agent, subject, instruction string) error { return fmt.Errorf("boom") }
	deliverImprovementNudge = func(sessionName, message string) error { nudged = true; return nil }
	defer func() { sendImprovementMail = origMail; deliverImprovementNudge = origNudge }()

	stdout, stderr := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, root, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	if !nudged {
		t.Error("nudge must still fire after a mail failure")
	}
	if !strings.Contains(stderr, "self-mail failed") {
		t.Errorf("stderr must warn on mail failure:\n%s", stderr)
	}
	if !strings.Contains(stdout, "af improvement complete") {
		t.Errorf("stdout instruction must still emit despite mail failure:\n%s", stdout)
	}
	// Completion is unaffected: the marker was still written.
	if _, err := os.Stat(improvementPendingFile(root, "alpha")); err != nil {
		t.Errorf("marker must persist despite a delivery failure: %v", err)
	}
}

func TestDone_ImprovementDelivery_NudgeFails_StdoutEmits(t *testing.T) {
	mem, root, instanceID := seedFiringForDelivery(t)

	origMail, origNudge := sendImprovementMail, deliverImprovementNudge
	sendImprovementMail = func(agent, subject, instruction string) error { return nil }
	deliverImprovementNudge = func(sessionName, message string) error { return fmt.Errorf("no session") }
	defer func() { sendImprovementMail = origMail; deliverImprovementNudge = origNudge }()

	stdout, stderr := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, root, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	if !strings.Contains(stderr, "nudge failed") {
		t.Errorf("stderr must warn on nudge failure:\n%s", stderr)
	}
	if !strings.Contains(stdout, "af improvement complete") {
		t.Errorf("stdout instruction must still emit despite nudge failure:\n%s", stdout)
	}
}

func TestImprovement_FailOpen_UnreadableAgentsJSON_SkipRecorded(t *testing.T) {
	t.Setenv("AF_ROLE", "alpha")
	root := setupImprovementFiringFactory(t)
	cwd := root
	writeRuntimeFile(t, cwd, "formula_caller", "supervisor")
	// Corrupt agents.json so LoadAgentConfig fails.
	if err := os.WriteFile(config.AgentsConfigPath(root), []byte("not json{"), 0o644); err != nil {
		t.Fatal(err)
	}

	mem := memstore.New()
	instanceID := seedCompletedFormula(t, mem, "Formula: widget")

	origMail := sendWorkDoneMail
	sendWorkDoneMail = func(caller, instanceID, formulaName string, stepCount int) error { return nil }
	defer func() { sendWorkDoneMail = origMail }()

	stdout, _ := captureOutErr(t, func() {
		if err := sendWorkDoneAndCleanup(t.Context(), mem, cwd, root, instanceID); err != nil {
			t.Fatalf("sendWorkDoneAndCleanup: %v", err)
		}
	})

	if strings.Contains(stdout, "IMPROVEMENT HOOK") {
		t.Errorf("unreadable agents.json must not fire:\n%s", stdout)
	}
	if _, err := os.Stat(skipFilePath(root, "alpha")); err != nil {
		t.Errorf("unreadable agents.json must record improvement_skipped: %v", err)
	}
	if !strings.Contains(stdout, "✓ All formula steps complete") {
		t.Errorf("completion path must proceed as off:\n%s", stdout)
	}
}
