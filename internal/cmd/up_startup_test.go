package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
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

// C-4 (issue #408 Phase 3): no startup.json ⇒ all agents start, no dispatcher starts, and
// both gate files are left untouched.
//
// The watchdog half is REVISED by #596 Phase 3. This test previously asserted that an empty
// watchdog_agents SKIPPED the launch entirely, with a notice and a durable breadcrumb. That
// is exactly the behaviour Decision 4 removed: "no startup.json" is the factory's own
// default state, and skipping the launch there left the default factory with no recovery
// process running at all — reproducing the incident in which an exhausted agent sat outside
// the configured scope with nothing watching it. The watchdog now LAUNCHES in recovery-only
// mode. The containment #408 asked for is unchanged and is asserted where it lives, on the
// pane scope (TestWatchdog_EmptyScopeYieldsInertPaneSurface); the launch is still a bare
// `af watchdog`, never a widened "watch all".
func TestRunUp_NoStartupConfig_AllStart_WatchdogLaunchesRecoveryOnly(t *testing.T) {
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
	// #596 Decision 4: the launch PROCEEDS on an empty scope, as a bare `af watchdog`.
	send := watchdogSendOp(fake.ops)
	if send == "" {
		t.Errorf("an empty watchdog scope must still LAUNCH the watchdog (recovery-only mode); ops=%v out=%q", fake.ops, out)
	}
	if strings.Contains(send, "--agents") {
		t.Errorf("the launch must stay a bare `af watchdog` (no widening to watch-all); got %q", send)
	}
	if !strings.Contains(out, "recovery-only mode") {
		t.Errorf("an empty scope must announce that the pane surface is inert and why; out=%q", out)
	}
	// No breadcrumb: an omitted watchdog_agents is a supported configuration, not an
	// error. Writing an error record for the default state would train operators to
	// ignore the one file that names real failures.
	if _, statErr := os.Stat(filepath.Join(root, ".runtime", "watchdog_last_error")); statErr == nil {
		t.Error("an empty scope is a supported configuration and must NOT write the error breadcrumb")
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
	if _, err := os.Stat(filepath.Join(root, ".agentfactory", ".telemetry-gate")); !os.IsNotExist(err) {
		t.Errorf(".telemetry-gate must be untouched (C-4); stat err=%v", err)
	}
}

// The telemetry gate reaches the filesystem only if startup.json's "telemetry" key is
// carried all the way through: a StartupConfig field, applyGate's case, AND an af up
// call site. A grep for the string in up.go passes even when the call sits outside the
// blanket branch, so this is the assertion that proves the wiring is live.
func TestRunUp_TelemetryGate_Applies(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"manager":{"type":"autonomous","description":"m"}}}`)
	writeAFFile(t, root, "startup.json", `{"agents":["manager"],"telemetry":"on"}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)

	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)

	gate, err := os.ReadFile(filepath.Join(root, ".agentfactory", ".telemetry-gate"))
	if err != nil {
		t.Fatalf("telemetry:on in startup.json must write .telemetry-gate: %v", err)
	}
	if string(gate) != "on\n" {
		t.Errorf(".telemetry-gate = %q, want %q", string(gate), "on\n")
	}
	if out := buf.String(); !strings.Contains(out, "telemetry gate: on") {
		t.Errorf("af up must echo the applied telemetry state; out=%q", out)
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

// A non-empty but ALL-UNKNOWN watchdog scope, REVISED by #596 Phase 3: `af up` no longer
// skips the launch, but — unlike the empty-scope case above — this one stays loud and keeps
// the durable breadcrumb. Names configured that do not exist in agents.json are an operator
// error, not a configuration choice, and collapsing the two would make a typo in
// watchdog_agents indistinguishable from deliberately running pane-inert.
func TestRunUp_WatchdogAgentsAllUnknown_LaunchesRecoveryOnlyButStaysLoud(t *testing.T) {
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

	if send := watchdogSendOp(fake.ops); send == "" {
		t.Errorf("an all-unknown scope must still LAUNCH the watchdog (recovery-only mode); ops=%v out=%q", fake.ops, out)
	}
	if !strings.Contains(out, "recovery-only mode") || !strings.Contains(out, "ghost") {
		t.Errorf("the notice must name the all-unknown misconfiguration; out=%q", out)
	}
	// Unlike the empty-scope case, this one IS an error and keeps its durable record.
	if _, statErr := os.Stat(filepath.Join(root, ".runtime", "watchdog_last_error")); statErr != nil {
		t.Errorf("a name absent from agents.json is a real misconfiguration and must still write the namespaced breadcrumb: %v", statErr)
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

// W1 (issue #408 Phase 4 / AC-3, AC-6): a watchdog scope gap is a MONITORING gap, not a
// START failure — it must not flip allOK or change af up's exit code, even though af up is
// otherwise clean. That half is untouched by #596 Phase 3 and is the reason this test
// survives.
//
// REVISED: the scope gap used to mean "skip the launch and write the refusal breadcrumb".
// Under Decision 4 an empty scope launches in recovery-only mode and writes no breadcrumb,
// so those two assertions are inverted here. The name changed with them — "Refusal" names a
// contract that no longer exists. The exit-code assertion is what this test is FOR, and it
// is unchanged: the sibling P1 test discards runUp's error, so this is the only place the
// empty-scope exit code is pinned.
func TestRunUp_WatchdogScopeGap_DoesNotAbortUp(t *testing.T) {
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
	// The launch PROCEEDS (#596 Decision 4) — and still as a bare `af watchdog`, so the
	// removed refusal did not become a silent widening to "watch all".
	send := watchdogSendOp(fake.ops)
	if send == "" {
		t.Errorf("an empty watchdog scope must still launch the watchdog; ops=%v out=%q", fake.ops, out)
	}
	if strings.Contains(send, "--agents") {
		t.Errorf("the launch must stay a bare `af watchdog`; got %q", send)
	}
	// No breadcrumb: an omitted watchdog_agents is a supported configuration. The gap is
	// still observable — it is announced on the launch notice — but it is not an error.
	if _, statErr := os.Stat(filepath.Join(root, ".runtime", "watchdog_last_error")); statErr == nil {
		t.Error("an empty scope must not write the error breadcrumb — it is a configuration, not a failure")
	}
	if !strings.Contains(out, "recovery-only mode") {
		t.Errorf("the scope gap must stay observable on the launch notice; out=%q", out)
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

// provisionAgentSettings writes a factory-root agent dir with the given .claude/settings.json,
// simulating an agent provisioned at some point in the past — the state the K20 pre-check
// exists to inspect.
func provisionAgentSettings(t *testing.T, root, agent, settings string) {
	t.Helper()
	claudeDir := filepath.Join(root, ".agentfactory", "agents", agent, ".claude")
	if err := os.MkdirAll(claudeDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claudeDir, "settings.json"), []byte(settings), 0o644); err != nil {
		t.Fatal(err)
	}
}

// K20 (#596): an agent whose settings.json predates the statusLine key writes no occupancy
// snapshot, so the reader honestly reports "none" and recovery correctly declines to act on
// absent evidence — silently. `af up` is the one guaranteed operator touchpoint, so it must
// say so before launching the watchdog that will appear to be doing nothing.
//
// The remediation verb is the load-bearing part. `af install --init` reprovisions
// factory-root agent dirs only; what actually delivers the settings template to a live agent
// is worktree.SetupAgent, reached from `af up` and both `af sling` paths. Naming the wrong
// verb would send an operator to a command that silently does nothing for them.
func TestRunUp_MissingStatusLineWiring_WarnsWithUpRemediation(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json",
		`{"agents":{"alpha":{"type":"autonomous","description":"a"},"stale":{"type":"autonomous","description":"s"}}}`)
	// Only alpha starts, so `stale` never passes through SetupAgent on this run — exactly
	// the agent class the pre-check is for.
	writeAFFile(t, root, "startup.json", `{"agents":["alpha"]}`)
	writeAFFile(t, root, ".statusline-gate", "on\n")
	provisionAgentSettings(t, root, "stale", `{"model":"opus"}`) // provisioned, no statusLine key

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

	if !strings.Contains(out, "stale") || !strings.Contains(out, "statusLine") {
		t.Errorf("af up must name the agent whose occupancy recovery cannot observe; out=%q", out)
	}
	if !strings.Contains(out, "af up") || !strings.Contains(out, "af sling") {
		t.Errorf("the remediation must name the verbs that actually deliver the settings template; out=%q", out)
	}
	// The corrected verb (design-doc.md:383). `af install --init` is mentioned only to say
	// what it does NOT cover, so the per-agent form must never appear.
	if strings.Contains(out, "af install stale") {
		t.Errorf("the remediation must not send the operator to `af install <agent>`; out=%q", out)
	}
	// The warning must not route through writeWatchdogLastError: that breadcrumb is the
	// watchdog's own error record, and a provisioning notice there would make a functional
	// launch indistinguishable from a failed one.
	if _, statErr := os.Stat(filepath.Join(root, ".runtime", "watchdog_last_error")); statErr == nil {
		t.Error("the K20 warning must not write the watchdog error breadcrumb")
	}
	// Two literals other af up tests assert the ABSENCE of, checked against the K20 lines
	// themselves rather than the whole run: the recovery-only launch notice legitimately
	// names watchdog_agents, and it only prints on factories whose scope is empty — which
	// is never true of the tests that forbid the literal.
	for _, line := range strings.Split(out, "\n") {
		if !strings.Contains(line, "cannot observe") && !strings.Contains(line, "remediation:") {
			continue
		}
		for _, forbidden := range []string{"unknown agent", "watchdog_agents"} {
			if strings.Contains(line, forbidden) {
				t.Errorf("the K20 warning must not contain %q — other af up tests assert its absence; line=%q", forbidden, line)
			}
		}
		// agentTouched's three shapes: several af up tests assert an agent was NOT
		// processed, and a warning naming agents in those shapes would break them.
		if strings.Contains(line, "stale: ") || strings.Contains(line, "af install stale") || strings.HasSuffix(line, "for stale") {
			t.Errorf("the K20 warning must not name an agent in an agentTouched shape; line=%q", line)
		}
	}
}

// The negative companion. Without it the test above passes for an implementation that warns
// unconditionally, which would be worse than silence — an always-on warning is ignored.
func TestRunUp_StatusLineWiringPresent_NoWarning(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeAFFile(t, root, "startup.json", `{"agents":["alpha"]}`)
	writeAFFile(t, root, ".statusline-gate", "on\n")
	provisionAgentSettings(t, root, "alpha", `{"statusLine":{"type":"command","command":"af statusline render"}}`)

	t.Setenv("AF_WORKTREE", "")
	t.Setenv("AF_WORKTREE_ID", "")
	t.Chdir(root)
	setupHermeticSessions(t)

	cmd := &cobra.Command{}
	var buf bytes.Buffer
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)

	_ = runUp(cmd, nil)

	if out := buf.String(); strings.Contains(out, "cannot observe") {
		t.Errorf("a correctly-wired factory must produce no provisioning warning; out=%q", out)
	}
}

// The factory-wide gate is the OTHER way every occupancy snapshot stops being written, and
// it short-circuits the render path before any per-agent setting matters — so a per-agent
// check alone would report "all clear" on a factory where recovery is blind for everyone.
func TestRunUp_StatuslineGateOff_WarnsRecoveryIsBlind(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeAFFile(t, root, "startup.json", `{"agents":["alpha"]}`)
	// .statusline-gate deliberately absent ⇒ off.

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

	if !strings.Contains(out, "statusline gate is off") {
		t.Errorf("an off statusline gate blinds recovery factory-wide and must be reported; out=%q", out)
	}
	if !strings.Contains(out, "af statusline on") {
		t.Errorf("the gate warning must name its own remediation; out=%q", out)
	}
}

// HIGH-1 / #622 G5, asserted where the damage would land. An operator who legitimately tightened
// recovery.context_threshold_pct BEFORE upgrading has no step_context block on disk, and the
// shipped handoff default of 75 does not fit under their threshold. If the new ladder rejected that
// DERIVED value the way it rejects a written one, LoadStartupConfig would hard-error here — and
// up.go:112 wraps and returns that error, blocking ALL agent launch for the whole factory.
//
// The start set is deliberately empty: this test is about reaching past the config load, and an
// empty set keeps it off the worktree-creation path (and therefore off that path's disk-space
// floor) while the watchdog launch, which is downstream of up.go:112, still proves progress. Per
// ADR-018 nothing is launched for real — setupHermeticSessions swaps the tmux seams.
func TestUp_TightenedRecoveryAbsentStepContextStartupConfigLoads(t *testing.T) {
	root := t.TempDir()
	initTestGitRepo(t, root)
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{"alpha":{"type":"autonomous","description":"a"}}}`)
	writeAFFile(t, root, "startup.json",
		`{"agents":[],"recovery":{"context_threshold_pct":70,"context_advisory_pct":60}}`)

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

	if err != nil && strings.Contains(err.Error(), "loading startup config") {
		t.Fatalf("a tightened recovery ladder with no step_context block bricked af up: %v", err)
	}
	if strings.Contains(out, "loading startup config") {
		t.Errorf("af up surfaced a startup config load failure; out=%q", out)
	}
	if watchdogSendOp(fake.ops) == "" {
		t.Errorf("af up did not reach the watchdog launch, which is downstream of the config load; ops=%v out=%q", fake.ops, out)
	}

	cfg, cfgErr := config.LoadStartupConfig(root)
	if cfgErr != nil {
		t.Fatalf("LoadStartupConfig: %v", cfgErr)
	}
	if cfg.StepContext.HandoffPct != 69 {
		t.Errorf("effective handoff_pct = %d, want 69 = min(75, threshold-1)", cfg.StepContext.HandoffPct)
	}
	if warning, ok := config.StepContextLint(cfg); !ok {
		t.Error("the clamped ladder must leave a warning for the cmd layer to surface")
	} else if !strings.Contains(warning, "handoff_pct") {
		t.Errorf("warning must name the on-disk key, got %q", warning)
	}
}
