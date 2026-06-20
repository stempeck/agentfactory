package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// writeAFFile writes root/.agentfactory/<name> with the given body.
func writeAFFile(t *testing.T, root, name, body string) {
	t.Helper()
	afDir := filepath.Join(root, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(afDir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// agentTouched reports whether runUp processed the named agent — detected via any of
// the per-agent lines runUp emits (worktree-created, not-provisioned skip, or a
// "NAME: ..." per-agent message). An agent absent from the resolved start set emits
// none of these.
func agentTouched(out, name string) bool {
	return strings.Contains(out, "for "+name+"\n") ||
		strings.Contains(out, "af install "+name) ||
		strings.Contains(out, name+": ")
}

// watchdogSendOp returns the recorded SendKeysDelayed op that launches `af watchdog`.
func watchdogSendOp(ops []string) string {
	for _, op := range ops {
		if strings.HasPrefix(op, "SendKeysDelayed ") && strings.Contains(op, "af watchdog") {
			return op
		}
	}
	return ""
}

// C-4 + N4 (issue #408 Phase 3): no startup.json ⇒ all agents start, no dispatcher
// starts, and both gate files are left untouched. With no watchdog_agents the scope
// is empty, so the watchdog launch is SKIPPED with a notice + breadcrumb — never a
// silent bare "watch all".
func TestRunUp_NoStartupConfig_AllStart_WatchdogSkipped(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"alpha":{"type":"autonomous","description":"a"},"bravo":{"type":"autonomous","description":"b"}}}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)
	out := buf.String()

	if !agentTouched(out, "alpha") || !agentTouched(out, "bravo") {
		t.Errorf("no startup.json must start ALL agents; out=%q", out)
	}
	// N4: an empty watchdog scope SKIPS the launch — no session, a one-line notice,
	// and the namespaced breadcrumb (never a silent bare "watch all").
	if send := watchdogSendOp(fake.ops); send != "" {
		t.Errorf("an empty watchdog scope must SKIP the launch (no send op); got %q", send)
	}
	if !strings.Contains(out, "watchdog: not started") {
		t.Errorf("an empty scope must print the skip notice; out=%q", out)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".runtime", "watchdog_last_error")); statErr != nil {
		t.Errorf("the skip path must write the namespaced breadcrumb watchdog_last_error: %v", statErr)
	}
	if opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("no startup.json ⇒ no dispatcher should start; ops=%v", fake.ops)
	}
	if _, err := os.Stat(filepath.Join(root, ".agentfactory", ".quality-gate")); !os.IsNotExist(err) {
		t.Errorf(".quality-gate must be untouched (C-4); stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".agentfactory", ".fidelity-gate")); !os.IsNotExist(err) {
		t.Errorf(".fidelity-gate must be untouched (C-4); stat err=%v", err)
	}
}

// SC7 core: a configured subset + quality gate + dispatch + watchdog scope all apply
// on the blanket `af up` path.
func TestRunUp_ConfiguredSubset_GateDispatchScope(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"manager":{"type":"autonomous","description":"m"},"supervisor":{"type":"autonomous","description":"s"},"extra":{"type":"autonomous","description":"e"}}}`)
	writeAFFile(t, root, "startup.json",
		`{"agents":["manager","supervisor"],"quality":"on","start_dispatch":true,"watchdog_agents":["manager"]}`)
	writeAFFile(t, root, "dispatch.json",
		`{"repos":["t/r"],"trigger_label":"agentic","mappings":[{"label":"x","agent":"manager"}],"interval_seconds":300}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)
	out := buf.String()

	if !agentTouched(out, "manager") || !agentTouched(out, "supervisor") {
		t.Errorf("configured subset must start manager AND supervisor; out=%q", out)
	}
	if agentTouched(out, "extra") {
		t.Errorf("agent 'extra' is not in the startup set and must NOT start; out=%q", out)
	}
	gate, err := os.ReadFile(filepath.Join(root, ".agentfactory", ".quality-gate"))
	if err != nil {
		t.Fatalf("quality:on must write .quality-gate: %v", err)
	}
	if string(gate) != "on\n" {
		t.Errorf(".quality-gate = %q, want %q", string(gate), "on\n")
	}
	if !opRecorded(fake.ops, "NewSession "+dispatchSessionName) {
		t.Errorf("start_dispatch:true must launch the dispatcher; ops=%v", fake.ops)
	}
	send := watchdogSendOp(fake.ops)
	if send == "" {
		t.Fatalf("a known watchdog scope must LAUNCH the watchdog; ops=%v", fake.ops)
	}
	if strings.Contains(send, "--agents") {
		t.Errorf("the launch must be a bare `af watchdog` (self-scopes from startup.json); got %q", send)
	}
}

// C-4 (highest risk, R-2): positional args ALWAYS win over startup.json; `af up
// manager` starts only manager regardless of a subset configured in startup.json.
func TestRunUp_PositionalArgsWin(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"manager":{"type":"autonomous","description":"m"},"supervisor":{"type":"autonomous","description":"s"}}}`)
	writeAFFile(t, root, "startup.json", `{"agents":["manager","supervisor"]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, []string{"manager"})
	out := buf.String()

	if !agentTouched(out, "manager") {
		t.Errorf("`af up manager` must start manager; out=%q", out)
	}
	if agentTouched(out, "supervisor") {
		t.Errorf("`af up manager` must NOT start supervisor despite startup.json; out=%q", out)
	}
}

// LOW-2: a present-but-empty agents list (Agents==[]) resolves to zero started
// agents AND prints the loud "0 configured agents started" notice.
func TestRunUp_EmptyAgents_LoudNotice(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeAFFile(t, root, "startup.json", `{"agents":[]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)
	out := buf.String()

	if !strings.Contains(out, "0 configured agents started") {
		t.Errorf("agents:[] must print the loud empty-set notice; out=%q", out)
	}
	if agentTouched(out, "alpha") {
		t.Errorf("agents:[] must start zero agents; out=%q", out)
	}
}

// SC11 (CRIT-1): a resolved start set larger than max_worktrees triggers the
// pre-flight warning BEFORE any worktree is created.
func TestRunUp_SubsetExceedsMaxWorktrees_Warns(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test","max_worktrees":1}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"alpha":{"type":"autonomous","description":"a"},"bravo":{"type":"autonomous","description":"b"}}}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)
	out := buf.String()

	warnIdx := strings.Index(out, "exceeds max_worktrees")
	if warnIdx < 0 {
		t.Fatalf("start set over the cap must emit the SC11 pre-flight warning; out=%q", out)
	}
	if createIdx := strings.Index(out, "Created worktree"); createIdx >= 0 && warnIdx > createIdx {
		t.Errorf("the SC11 warning must appear BEFORE any worktree is created; out=%q", out)
	}
}

// PR2-HIGH-1: omitting the centralized escalation target (supervisor) from a
// configured startup subset emits the omission warning EVEN WHEN supervisor is in no
// messaging.json group AND dispatch NotifyOnComplete == "manager" — i.e. the warning
// is driven by source (3), escalationTargets().
func TestRunUp_OmitsSupervisorEscalationTarget_Warns(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"manager":{"type":"autonomous","description":"m"},"supervisor":{"type":"autonomous","description":"s"}}}`)
	// supervisor in NO messaging group; dispatch NotifyOnComplete is the "manager"
	// default (no dispatch.json). Only escalationTargets() reaches supervisor.
	writeAFFile(t, root, "startup.json", `{"agents":["manager"]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)
	out := buf.String()

	if !strings.Contains(out, "supervisor") || !strings.Contains(out, "mail/notify target") {
		t.Errorf("omitting the escalation target supervisor must warn (driven by escalationTargets()); out=%q", out)
	}
}

// The fidelity active-formula guard must check the af-up-RESOLVED root, not the
// raw cwd. A formula hooked at the root must block fidelity:"off" even when
// `af up` is invoked from a subdirectory (wd != root).
func TestRunUp_FidelityOffFromSubdir_GuardChecksRoot(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeAFFile(t, root, "startup.json", `{"fidelity":"off"}`)

	// Active formula marker at the ROOT — where the guard must look.
	if err := os.MkdirAll(filepath.Join(root, ".runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".runtime", "hooked_formula"), []byte("bd-x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	sub := filepath.Join(root, "subdir")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(sub)

	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)
	out := buf.String()

	if !strings.Contains(out, "cannot disable fidelity gate") {
		t.Errorf("the active-formula guard must fire when af up runs from a subdir; out=%q", out)
	}
	if data, err := os.ReadFile(filepath.Join(root, ".agentfactory", ".fidelity-gate")); err == nil &&
		strings.TrimSpace(string(data)) == "off" {
		t.Error("fidelity gate silently disabled despite an active formula at the root — guard checked wd, not root")
	}
}

// Companion guard for the fix: with NO active formula, fidelity:"off" still
// applies and is echoed (the guard must not over-correct into always refusing).
func TestRunUp_FidelityOffNoActiveFormula_Applies(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeAFFile(t, root, "startup.json", `{"fidelity":"off"}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)
	out := buf.String()

	data, err := os.ReadFile(filepath.Join(root, ".agentfactory", ".fidelity-gate"))
	if err != nil {
		t.Fatalf("fidelity:off with no active formula must write the gate file: %v", err)
	}
	if string(data) != "off\n" {
		t.Errorf(".fidelity-gate = %q, want %q", string(data), "off\n")
	}
	if !strings.Contains(out, "fidelity gate: off") {
		t.Errorf("the per-action echo must report the fidelity gate state; out=%q", out)
	}
}

// failingNewSessionTmux wraps the hermetic fake and fails NewSession for the
// dispatcher session only, driving launchDispatchSession's
// "creating tmux session: %w" path while agent starts and the watchdog launch
// stay healthy on the embedded fake.
type failingNewSessionTmux struct {
	*fakeTmux
}

func (f *failingNewSessionTmux) NewSession(name, workDir string) error {
	f.fakeTmux.record(fmt.Sprintf("NewSession %s %s", name, workDir))
	if name == dispatchSessionName {
		return fmt.Errorf("tmux server gone")
	}
	return nil
}

// PR #355 thread 1 (up.go:264): a real dispatcher launch failure must warn on
// stderr and flip allOK — like every other best-effort failure in runUp — not
// vanish into a discarded return with exit 0.
func TestRunUp_DispatchLaunchFailure_WarnsAndFailsExit(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeAFFile(t, root, "startup.json", `{"start_dispatch":true}`)
	writeAFFile(t, root, "dispatch.json",
		`{"repos":["t/r"],"trigger_label":"agentic","mappings":[{"label":"x","agent":"alpha"}],"interval_seconds":300}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	inner, _ := setupHermeticSessions(t)
	// Re-point only the cmd-layer seam at the failing wrapper: the session-side
	// seam keeps the healthy inner fake, and NewSession fails selectively for
	// dispatchSessionName, isolating the dispatch path. No t.Parallel (seam
	// reassignment).
	failing := &failingNewSessionTmux{fakeTmux: inner}
	orig := newCmdTmux
	newCmdTmux = func() cmdTmux { return failing }
	t.Cleanup(func() { newCmdTmux = orig })

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, nil)
	out := buf.String()

	if !strings.Contains(out, "warning") || !strings.Contains(out, "dispatch") {
		t.Errorf("a failed dispatcher launch must warn on stderr like every other best-effort failure; out=%q", out)
	}
	if err == nil || !strings.Contains(err.Error(), "some agents failed to start") {
		t.Errorf("a failed dispatcher launch must flip allOK (aggregate error), got err=%v", err)
	}
}

// PR #355 thread 2 (up.go:268): each watchdog_agents entry missing from
// agents.json must warn — not error — at af up time; pollAgents silently skips
// unknown names, so a typo would otherwise shrink monitoring coverage with no
// signal anywhere. Valid entries must still scope the watchdog launch.
func TestRunUp_WatchdogAgentsUnknownEntry_Warns(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"supervisor":{"type":"autonomous","description":"s"}}}`)
	// "supervsor" is the canonical typo from the review thread.
	writeAFFile(t, root, "startup.json", `{"watchdog_agents":["supervsor","supervisor"]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, nil)
	out := buf.String()

	if !strings.Contains(out, "supervsor") || !strings.Contains(out, "warning") {
		t.Errorf("an unknown watchdog_agents entry must warn at af up time; out=%q", out)
	}
	// Warn-only: the membership warning must NOT flip allOK.
	if err != nil {
		t.Errorf("unknown watchdog_agents entry must be warn-only, got err=%v", err)
	}
	send := watchdogSendOp(fake.ops)
	if send == "" {
		t.Fatalf("a scope with >=1 known name must still LAUNCH the watchdog; ops=%v", fake.ops)
	}
	if strings.Contains(send, "--agents") {
		t.Errorf("the launch must be a bare `af watchdog` (no --agents); got %q", send)
	}
}

// Companion negative for thread 2: all-known watchdog_agents entries stay
// quiet — no membership warning fires for valid names.
func TestRunUp_WatchdogAgentsAllKnown_NoWarning(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"manager":{"type":"autonomous","description":"m"}}}`)
	writeAFFile(t, root, "startup.json", `{"watchdog_agents":["manager"]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)
	out := buf.String()

	if strings.Contains(out, "watchdog_agents") {
		t.Errorf("all-known watchdog_agents must produce no membership warning; out=%q", out)
	}
	send := watchdogSendOp(fake.ops)
	if send == "" {
		t.Fatalf("an all-known watchdog scope must LAUNCH the watchdog; ops=%v", fake.ops)
	}
	if strings.Contains(send, "--agents") {
		t.Errorf("the launch must be a bare `af watchdog` (no --agents); send=%q", send)
	}
}

// N4 (issue #408 Phase 3): a non-empty but ALL-UNKNOWN watchdog scope is the early,
// observable echo of the watchdog's own refusal — `af up` SKIPS the launch (no
// session), names the misconfiguration in a one-line notice, writes the namespaced
// breadcrumb, and never aborts (best-effort, W1).
func TestRunUp_WatchdogAgentsAllUnknown_Skipped(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"supervisor":{"type":"autonomous","description":"s"}}}`)
	writeAFFile(t, root, "startup.json", `{"watchdog_agents":["ghost"]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, nil)
	out := buf.String()

	if send := watchdogSendOp(fake.ops); send != "" {
		t.Errorf("an all-unknown watchdog scope must SKIP the launch (no send op); got %q", send)
	}
	if !strings.Contains(out, "watchdog: not started") || !strings.Contains(out, "ghost") {
		t.Errorf("the skip notice must name the all-unknown misconfiguration; out=%q", out)
	}
	if _, statErr := os.Stat(filepath.Join(root, ".runtime", "watchdog_last_error")); statErr != nil {
		t.Errorf("the all-unknown skip path must write the namespaced breadcrumb: %v", statErr)
	}
	// Best-effort: a watchdog-scope gap must NOT abort af up (the configured agent
	// still started cleanly).
	if err != nil {
		t.Errorf("an all-unknown watchdog scope must be best-effort (no abort); got %v", err)
	}
}

// AC-5 (issue #408 Phase 3): a fresh scaffold (manager + supervisor configured,
// watchdog_agents = both, both present in the default agents.json) brings up a
// scoped, FUNCTIONAL watchdog — a bare `af watchdog` is launched (the watchdog
// self-scopes from startup.json, Phase 2), with NO "unknown agent" warning and NO
// N4 skip breadcrumb. Mirrors the reconciled install scaffold seed (install.go N6).
func TestRunUp_FreshScaffold_WatchdogLaunchesScoped(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	// The default agents.json seeded by `af install --init` (install.go:109).
	writeAFFile(t, root, "agents.json",
		`{"agents":{"manager":{"type":"interactive","description":"m"},"supervisor":{"type":"autonomous","description":"s"}}}`)
	// The reconciled scaffold seed (install.go:113, N6): both names are real agents.
	writeAFFile(t, root, "startup.json",
		`{"agents":["manager","supervisor"],"quality":"default","fidelity":"default","start_dispatch":true,"watchdog_agents":["manager","supervisor"]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, nil)
	out := buf.String()

	// Functional launch: a bare `af watchdog` send op is recorded (NOT skipped).
	send := watchdogSendOp(fake.ops)
	if send == "" {
		t.Fatalf("a fresh scaffold must LAUNCH the watchdog (not skip); ops=%v out=%q", fake.ops, out)
	}
	if strings.Contains(send, "--agents") {
		t.Errorf("the launch must be a bare `af watchdog` (self-scopes from startup.json); got %q", send)
	}
	// No membership warning: both seeded names exist in agents.json.
	if strings.Contains(out, "unknown agent") {
		t.Errorf("a fresh scaffold names only real agents — no unknown-agent warning expected; out=%q", out)
	}
	// No N4 skip breadcrumb: the launch proceeded, so the skip path did not run.
	if _, statErr := os.Stat(filepath.Join(root, ".runtime", "watchdog_last_error")); statErr == nil {
		t.Errorf("a functional launch must NOT write the N4 skip breadcrumb watchdog_last_error")
	}
	// Best-effort: a clean fresh-scaffold af up must not error.
	if err != nil {
		t.Errorf("a clean fresh-scaffold af up must not error; got %v", err)
	}
}

// W1 (issue #408 Phase 4 / AC-3, AC-6): a watchdog scope gap is a MONITORING gap,
// not a START failure. An empty watchdog_agents scope must SKIP the launch and
// write the durable refusal breadcrumb, yet must NOT flip allOK or change af up's
// exit code — even though af up is otherwise clean. The empty-scope exit-code half
// of W1 is unpinned by the P1 test (TestRunUp_NoStartupConfig_AllStart_WatchdogSkipped
// discards runUp's error at :68); this names and asserts it directly, complementing
// the all-unknown variant (TestRunUp_WatchdogAgentsAllUnknown_Skipped) which uses a
// non-empty all-unknown scope.
func TestRunUp_WatchdogRefusal_DoesNotAbortUp(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	// Present startup.json with one real configured agent but NO watchdog_agents —
	// an EMPTY watchdog scope (the refusal case; complements the all-unknown variant).
	writeAFFile(t, root, "startup.json", `{"agents":["alpha"]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	err := runUp(cmd, nil)
	out := buf.String()

	// The configured agent still started cleanly despite the watchdog scope gap.
	if !agentTouched(out, "alpha") {
		t.Errorf("the configured agent must start despite the watchdog scope gap; out=%q", out)
	}
	// W1: the watchdog gap must NOT abort af up — runUp returns nil (no aggregate
	// "some agents failed to start"; allOK was not flipped).
	if err != nil {
		t.Errorf("a watchdog scope gap must be best-effort (no abort / clean exit code); got %v", err)
	}
	// The launch was SKIPPED (an empty scope is never a silent bare "watch all").
	if send := watchdogSendOp(fake.ops); send != "" {
		t.Errorf("an empty watchdog scope must SKIP the launch (no send op); got %q", send)
	}
	// The refusal stays observable: the no-abort must not silence the durable signal.
	// R2-L1: the NAMESPACED breadcrumb <root>/.runtime/watchdog_last_error, NOT a
	// per-agent last_error.
	if _, statErr := os.Stat(filepath.Join(root, ".runtime", "watchdog_last_error")); statErr != nil {
		t.Errorf("the refusal path must write the namespaced breadcrumb watchdog_last_error: %v", statErr)
	}
}

// P2 (issue #408 Phase 4 / AC-1): the positional `af up <names>` path must never
// launch an unscoped "watch all" watchdog. Phase 3 N5 moved the assignment
// `watchdogScope = startupCfg.WatchdogAgents` OUT of the `if blanket` block
// (up.go:343), so the positional path self-scopes from startup.json too. This pins
// the launch-op layer: on the positional path the watchdog still LAUNCHES (the scope
// is known/functional) as a BARE `af watchdog` (self-scopes from startup.json) — it
// does NOT widen to "watch all" and does NOT append a positional/config name as
// scope. (TestRunUp_PositionalArgsWin covers agent selection; this covers the
// watchdog launch op.)
func TestRunUp_PositionalArgs_WatchdogSelfScopes(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"manager":{"type":"autonomous","description":"m"},"supervisor":{"type":"autonomous","description":"s"}}}`)
	// A KNOWN watchdog scope: both names are real agents (a functional watchdog).
	writeAFFile(t, root, "startup.json",
		`{"agents":["manager","supervisor"],"watchdog_agents":["manager","supervisor"]}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	fake, _ := setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	// Positional path: `af up manager` selects only manager, but the watchdog still
	// self-scopes from startup.json (Phase 3 N5).
	err := runUp(cmd, []string{"manager"})
	out := buf.String()

	if !agentTouched(out, "manager") {
		t.Errorf("`af up manager` must start manager; out=%q", out)
	}
	if err != nil {
		t.Errorf("a clean positional af up must not error; got %v", err)
	}
	// Not skipped incorrectly: the known scope must LAUNCH the watchdog.
	send := watchdogSendOp(fake.ops)
	if send == "" {
		t.Fatalf("a known watchdog scope must LAUNCH on the positional path (not skip); ops=%v out=%q", fake.ops, out)
	}
	// Not widened: a bare `af watchdog` (self-scopes from startup.json) — no --agents
	// flag (it no longer exists) and no positional/config name appended as scope.
	if strings.Contains(send, "--agents") {
		t.Errorf("the positional-path launch must be a bare `af watchdog` (self-scopes from startup.json); got %q", send)
	}
	if strings.Contains(send, "af watchdog manager") || strings.Contains(send, "af watchdog supervisor") {
		t.Errorf("the positional-path watchdog must not append a name as scope (no widening to watch-all); got %q", send)
	}
}

// Sanity: escalationTargets() is the single source of truth and includes supervisor.
func TestEscalationTargets_IncludesSupervisor(t *testing.T) {
	got := escalationTargets()
	if len(got) == 0 || got[0] != "supervisor" {
		t.Errorf("escalationTargets() = %v, want [supervisor]", got)
	}
	if escalationTarget != "supervisor" {
		t.Errorf("escalationTarget = %q, want supervisor", escalationTarget)
	}
}
