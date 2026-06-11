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

// C-4: no startup.json ⇒ all agents start, a bare watchdog launches, no dispatcher
// starts, and both gate files are left untouched.
func TestRunUp_NoStartupConfig_AllStart_BareWatchdog(t *testing.T) {
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
	send := watchdogSendOp(fake.ops)
	if send == "" {
		t.Fatalf("a watchdog SendKeysDelayed op must be recorded; ops=%v", fake.ops)
	}
	if strings.Contains(send, "--agents") {
		t.Errorf("no scope ⇒ bare 'af watchdog' (no --agents); got %q", send)
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
	if !strings.Contains(send, "af watchdog --agents manager") {
		t.Errorf("watchdog_agents:[manager] must scope the launch; got %q", send)
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
	if send := watchdogSendOp(fake.ops); !strings.Contains(send, "supervisor") {
		t.Errorf("the known entry must still scope the watchdog; send=%q", send)
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
	if send := watchdogSendOp(fake.ops); !strings.Contains(send, "--agents manager") {
		t.Errorf("watchdog_agents:[manager] must scope the launch; send=%q", send)
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
