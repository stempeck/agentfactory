package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/session"
)

// Phase 3 (#541) command-gate tests. The Phase 2 authority core is proven in
// authority_test.go; these assert the K5/K11, K6, and K7 gates wired at the three
// teardown-class command entries, plus the K16 in-band guidance echo. Every agent-context
// row re-introduces the classifier signal with t.Setenv("AF_ROLE", …) (the internal/cmd
// TestMain scrubs the ambient value) and keeps signal 2 quiet with t.Setenv("TMUX", "").

// assertTeardownRefused asserts err carries the K1 refusal for the given surface. Uses the
// stable, machine-greppable first line so it holds for both the direct-call paths (runDown /
// runDispatchStop) and the cobra-Execute path (af install --agents).
func assertTeardownRefused(t *testing.T, err error, surface string) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected teardown refusal for surface %q, got nil", surface)
	}
	want := "teardown refused: agent context (" + surface + ")"
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain the K1 refusal %q", err.Error(), want)
	}
}

// setupDownGateFactory writes a factory root with a mixed roster (an interactive manager, a
// formula-backed autonomous specialist, and a second formula-backed sibling), chdir's into
// it, and installs the shared hermetic tmux/store fake. Mirrors setupDownFactory but with the
// richer roster the K11 carve-out tests need.
func setupDownGateFactory(t *testing.T) (string, *fakeTmux) {
	t.Helper()
	root := t.TempDir()
	writeAFFile(t, root, "factory.json", `{"type":"factory","version":1,"name":"test"}`)
	writeAFFile(t, root, "agents.json", `{"agents":{
  "manager":{"type":"interactive","description":"m"},
  "solver":{"type":"autonomous","description":"s","formula":"factoryworker"},
  "sibling":{"type":"autonomous","description":"sib","formula":"factoryworker"}}}`)
	t.Chdir(root)
	fake, _ := setupHermeticSessions(t)
	return root, fake
}

// --- K5 + K11: runDown gate (AC-1, AC-3) ---

// TestDown_AgentContext_RefusesBareAllReset_ZeroOps pins AC-1: bare / --all / no-target --reset
// in agent context refuse with the K1 message and record ZERO KillSession ops (the roster loop
// is never entered) and no preResetScan warning. Roster sessions are marked present so a
// NON-refused loop WOULD record a KillSession — making the zero-op assertion non-vacuous.
// (Scoped --reset with a tier-granted target is allowed post-#553; see
// TestDown_ManagerContext_ScopedResetAllowed.)
func TestDown_AgentContext_RefusesBareAllReset_ZeroOps(t *testing.T) {
	_, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	for _, name := range []string{"manager", "solver", "sibling"} {
		fake.present[session.SessionName(name)] = true
	}
	fake.present[session.DispatchSessionName()] = true
	fake.present[session.WatchdogSessionName()] = true

	// This is the only downAll=true test in the default suite. The K5 gate refuses above the
	// orphan sweep (down.go), so the sweep is never reached — but install the runPkill recorder
	// anyway so the zero-host-kill posture is STRUCTURAL, not merely a consequence of the K5/K10
	// gates staying correct: the recorder guarantees a refused --all can never shell out to a real
	// host-process sweep (outline File-2 "orphan-sweep seam zero-exec on refused --all"; IMPLREADME
	// AC-7 forbids any default-suite test reaching a real host-process sweep).
	pkillCalls := installPkillRecorder(t)

	cases := []struct {
		name        string
		all, reset  bool
		wantSurface string
	}{
		{"bare", false, false, "af down"},
		{"all", true, false, "af down"},
		{"reset", false, true, "af down --reset"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			downAll, downReset = tc.all, tc.reset
			t.Cleanup(func() { downAll, downReset = false, false })
			fake.ops = nil

			cmd := &cobra.Command{}
			var out, errb bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errb)

			err := runDown(cmd, nil)
			assertTeardownRefused(t, err, tc.wantSurface)
			if opRecorded(fake.ops, "KillSession") {
				t.Errorf("refused %s must record zero KillSession; ops=%v", tc.name, fake.ops)
			}
			if len(*pkillCalls) != 0 {
				t.Errorf("refused %s must reach zero runPkill orphan-sweep calls; the gate forecloses the sweep, so no real host-process sweep may run; calls=%v", tc.name, *pkillCalls)
			}
			if strings.Contains(errb.String(), "WARNING: --reset") {
				t.Errorf("refused %s must print no preResetScan warning; stderr=%q", tc.name, errb.String())
			}
		})
	}
}

// TestDown_AgentContext_AllowsSelf pins the K11 self carve-out: af down <self> is not
// refused and stops the caller's own session.
func TestDown_AgentContext_AllowsSelf(t *testing.T) {
	_, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("manager")] = true

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := runDown(cmd, []string{"manager"})
	if err != nil && strings.Contains(err.Error(), "teardown refused") {
		t.Fatalf("af down <self> must not refuse; got %v", err)
	}
	if !opRecorded(fake.ops, "KillSession "+session.SessionName("manager")) {
		t.Errorf("af down <self> must stop the caller's own session; ops=%v", fake.ops)
	}
}

// TestDown_AgentContext_AllowsOwnDispatchedSpecialist pins AC-3 on the DISPATCHER tier: a
// SPECIALIST caller (solver, #548 re-attribution) may stop a formula-backed specialist it
// dispatched, authorized by provenance match (formula_caller[sibling]=="solver", read via the
// compat fallback in readDispatchProvenance). Caller != target is mandatory: a non-interactive
// specialist caller cannot reach the manager tier, so this exercises the provenance-match
// dispatcher tier rather than passing vacuously; making caller==target would collapse to the
// SELF carve-out. K9 lets the autonomous target through, so the session is actually killed.
func TestDown_AgentContext_AllowsOwnDispatchedSpecialist(t *testing.T) {
	root, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "solver")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("sibling")] = true
	writeRuntimeFile(t, config.AgentDir(root, "sibling"), "formula_caller", "solver")

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := runDown(cmd, []string{"sibling"})
	if err != nil && strings.Contains(err.Error(), "teardown refused") {
		t.Fatalf("af down <own-dispatched-specialist> must not refuse; got %v", err)
	}
	if !opRecorded(fake.ops, "KillSession "+session.SessionName("sibling")) {
		t.Errorf("af down <own-dispatched-specialist> must stop it; ops=%v", fake.ops)
	}
}

// TestDown_AgentContext_RefusesNonDispatchedSibling is the load-bearing AC-3 test: a formula-backed
// sibling the caller did NOT dispatch must refuse. K9 alone does not close this (it only backstops
// interactive targets), so without the scoped-stop gate the autonomous sibling would be killed —
// this test proves the gate closes that hole. The caller is a SPECIALIST identity (solver, #548
// re-attribution): a non-interactive caller cannot reach the manager tier, so this stays an AC-3
// refusal — an interactive manager stopping a non-dispatched worker is now the AC-2 ALLOW cell
// (TestDown_ManagerContext_AllowsNonDispatchedWorker), not a refusal.
func TestDown_AgentContext_RefusesNonDispatchedSibling(t *testing.T) {
	root, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "solver")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("sibling")] = true
	writeRuntimeFile(t, config.AgentDir(root, "sibling"), "formula_caller", "someoneelse")

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := runDown(cmd, []string{"sibling"})
	assertTeardownRefused(t, err, "af down")
	if opRecorded(fake.ops, "KillSession") {
		t.Errorf("refused sibling stop must record zero KillSession; ops=%v", fake.ops)
	}
}

// TestDown_AgentContext_RefusesManager pins the interactive-manager refusal via the K11
// formula axis (manager has no formula ⇒ isSpecialistTarget false), from a specialist caller.
func TestDown_AgentContext_RefusesManager(t *testing.T) {
	_, fake := setupDownGateFactory(t)
	t.Setenv("AF_ROLE", "solver")
	t.Setenv("TMUX", "")
	fake.present[session.SessionName("manager")] = true

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := runDown(cmd, []string{"manager"})
	assertTeardownRefused(t, err, "af down")
	if opRecorded(fake.ops, "KillSession") {
		t.Errorf("refused manager stop must record zero KillSession; ops=%v", fake.ops)
	}
}

// --- K6: runInstallAgents gate (AC-4, AC-7) ---

// installSeamHygiene resets the persistent install flags and source-resolution vars, saves
// and restores the two script seams, and returns an events recorder wired into both seams.
// Mirrors the save/override/Cleanup idiom in TestInstallAgentsDispatchesToSeam.
func installSeamHygiene(t *testing.T) *[]string {
	t.Helper()
	installAgentsFlag = false
	installNoBuildFlag = false
	origAFSrcFlag := agentGenAFSrc
	origCompiled := compiledSourceRoot
	agentGenAFSrc = ""
	compiledSourceRoot = ""
	origAgentGen := runAgentGenScript
	origQuickstart := runQuickstartScript
	t.Cleanup(func() {
		installAgentsFlag = false
		installNoBuildFlag = false
		agentGenAFSrc = origAFSrcFlag
		compiledSourceRoot = origCompiled
		runAgentGenScript = origAgentGen
		runQuickstartScript = origQuickstart
	})

	var events []string
	runAgentGenScript = func(cmd *cobra.Command, afSrc, projectDir string, noBuild bool) error {
		events = append(events, "agentgen")
		return nil
	}
	runQuickstartScript = func(cmd *cobra.Command, afSrc, projectDir string, extraArgs []string) error {
		events = append(events, "quickstart")
		return nil
	}
	return &events
}

// TestInstallAgents_AgentContext_RefusesBeforeSeam pins AC-4: af install --agents in agent
// context returns the K1 refusal BEFORE the runAgentGenScript seam (which shells the
// af down --all path) — proven by the seam recording zero calls. A valid AF_SOURCE_ROOT is
// supplied so Guards 1/2/3 pass and execution reaches the K6 site (which replaces Guard 5).
func TestInstallAgents_AgentContext_RefusesBeforeSeam(t *testing.T) {
	events := installSeamHygiene(t)
	afSrc := newAFSourceDir(t, []string{"agent-gen-all.sh", "quickstart.sh"}, nil)
	t.Setenv("AF_SOURCE_ROOT", afSrc)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")

	dir := setupFactoryDir(t)
	out, err := runInstallInDir(t, dir, "--agents")
	assertTeardownRefused(t, err, "af install --agents")
	if len(*events) != 0 {
		t.Errorf("K6 must refuse BEFORE the runAgentGenScript seam; events=%v out=%s", *events, out)
	}
}

// TestInstallAgents_OperatorContext_RegenAllowed pins AC-7 (REGEN SAFETY): an operator (no
// AF_ROLE, no af-identity $TMUX) still runs the teardown-bearing regeneration — agent-gen
// then quickstart. A false positive here would break make check-regen / the regen CI job.
func TestInstallAgents_OperatorContext_RegenAllowed(t *testing.T) {
	events := installSeamHygiene(t)
	afSrc := newAFSourceDir(t, []string{"agent-gen-all.sh", "quickstart.sh"}, nil)
	t.Setenv("AF_SOURCE_ROOT", afSrc)
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")

	dir := setupFactoryDir(t)
	out, err := runInstallInDir(t, dir, "--agents")
	if err != nil {
		t.Fatalf("operator install --agents must succeed (regen safety); err=%v out=%s", err, out)
	}
	if len(*events) != 2 || (*events)[0] != "agentgen" || (*events)[1] != "quickstart" {
		t.Errorf("operator regen must invoke agent-gen then quickstart; events=%v", *events)
	}
}

// --- K7: runDispatchStop gate (AC-5) ---

// TestDispatchStop_AgentContext_Refuses pins AC-5: af dispatch stop refuses in agent context
// before touching tmux (zero KillSession), even with a live dispatcher marked present.
func TestDispatchStop_AgentContext_Refuses(t *testing.T) {
	fake := installFakeTmuxPresent(t, dispatchSessionName)
	t.Setenv("AF_ROLE", "manager")
	t.Setenv("TMUX", "")

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := runDispatchStop(cmd, nil)
	assertTeardownRefused(t, err, "af dispatch stop")
	if opRecorded(fake.ops, "KillSession") {
		t.Errorf("refused dispatch stop must record zero KillSession; ops=%v", fake.ops)
	}
}

// TestDispatchStop_OperatorContext_Unchanged pins AC-5's operator half: with no dispatcher
// running, the operator path is byte-for-byte the prior behavior ("dispatcher is not running").
func TestDispatchStop_OperatorContext_Unchanged(t *testing.T) {
	installFakeTmuxPresent(t) // no dispatcher present by default
	t.Setenv("AF_ROLE", "")
	t.Setenv("TMUX", "")

	cmd := &cobra.Command{}
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	err := runDispatchStop(cmd, nil)
	if err == nil || !strings.Contains(err.Error(), "not running") {
		t.Fatalf("operator dispatch stop with no dispatcher must error 'not running'; got %v", err)
	}
}

// locateRepoFile walks up from this test file's own directory (via runtime.Caller, so it is
// independent of the test's working directory) until it finds name at a directory level, and
// returns the absolute path. Used to read the repo-root agent-gen-all.sh script.
func locateRepoFile(t *testing.T, name string) string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	dir := filepath.Dir(self)
	for {
		candidate := filepath.Join(dir, name)
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("%s not found walking up from %s", name, filepath.Dir(self))
		}
		dir = parent
	}
}

// --- K16: in-band script guidance (AC-6) ---

// TestAgentGenAll_K16GuidanceEcho pins AC-6: a guidance echo appears on a line ABOVE the
// af down --all command, and that command's text stays byte-identical (K5 forecloses the
// teardown; K16 only closes the 2>/dev/null delivery gap).
func TestAgentGenAll_K16GuidanceEcho(t *testing.T) {
	scriptPath := locateRepoFile(t, "agent-gen-all.sh")
	data, err := os.ReadFile(scriptPath)
	if err != nil {
		t.Fatalf("read agent-gen-all.sh: %v", err)
	}
	lines := strings.Split(string(data), "\n")

	teardown := "af down --all 2>/dev/null || true"
	teardownIdx := -1
	for i, l := range lines {
		if l == teardown {
			teardownIdx = i
			break
		}
	}
	if teardownIdx < 0 {
		t.Fatalf("the byte-identical teardown command %q is missing from agent-gen-all.sh", teardown)
	}
	// A guidance echo must precede the teardown command.
	sawEchoAbove := false
	for i := 0; i < teardownIdx; i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "echo ") {
			sawEchoAbove = true
		}
	}
	if !sawEchoAbove {
		t.Errorf("K16: no guidance echo found on a line above %q", teardown)
	}
	// The K16 echo must sit immediately above the teardown command (its dedicated line).
	if teardownIdx == 0 || !strings.HasPrefix(strings.TrimSpace(lines[teardownIdx-1]), "echo ") {
		t.Errorf("K16: the line directly above the teardown command must be the guidance echo; got %q", lines[teardownIdx-1])
	}
}
