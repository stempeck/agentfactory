//go:build integration

package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
	"github.com/stempeck/agentfactory/internal/memory"
	"github.com/stempeck/agentfactory/internal/templates"
)

// findRepoRoot walks up from THIS source file's directory (not the process cwd)
// to the module root. Deriving it from runtime.Caller keeps it correct for callers
// that run after a t.Chdir — notably a subtest whose parent chdir'd into a temp
// factory, where the parent's cwd is still in effect (its restore runs only after
// the subtest returns).
func findRepoRoot(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate the test source file")
	}
	dir := filepath.Dir(thisFile)
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not find repo root (no go.mod)")
		}
		dir = parent
	}
}

// requirePython3WithServerDeps skips the test unless python3 is on PATH AND
// can import the Python MCP server's runtime deps (aiohttp, sqlalchemy). The
// two-step probe catches hosts that have python3 but no venv-installed server
// deps, where bare LookPath would let the test run and fail at a confusing
// ModuleNotFoundError during server start.
func requirePython3WithServerDeps(t *testing.T) {
	t.Helper()
	// Route the missing-dep decision through realStoreGateDecision (the pure,
	// unit-tested predicate in realstore_gate_test.go). afRequireRealStore is the
	// CI signal captured in TestMain before NeutralizeAFEnv wiped it. Under the
	// gating integration lane (AF_REQUIRE_REAL_STORE=1) a missing dep HARD-FAILS,
	// so this real-store gate — incl. the sole #458 Gap-1 catcher
	// TestAgentsListIntegration_OwnInstancePerAgent — can never silently no-op →
	// green under CI Python/venv drift. Locally (signal unset) it stays a friendly
	// skip, matching internal/issuestore/mcpstore/mcpstore_test.go. t.Skipf and
	// t.Fatalf share func(string, ...any), so one selector covers both probes.
	fail := t.Skipf
	if realStoreGateDecision(afRequireRealStore, true) == realStoreFatal {
		fail = t.Fatalf
	}
	if _, err := exec.LookPath("python3"); err != nil {
		fail("python3 not available")
	}
	if out, err := exec.Command("python3", "-c", "import aiohttp, sqlalchemy").CombinedOutput(); err != nil {
		fail("python3 missing server deps (aiohttp/sqlalchemy): %s", out)
	}
}

// ensurePySymlink symlinks the repo's py/ package into factoryRoot so the
// Python MCP server subprocess (launched with cmd.Dir=factoryRoot as
// `python3 -m py.issuestore.server`) can import py.issuestore. Mirrors the
// pattern in internal/issuestore/mcpstore/mcpstore_test.go.
func ensurePySymlink(t *testing.T, factoryRoot string) {
	t.Helper()
	target := filepath.Join(findRepoRoot(t), "py")
	link := filepath.Join(factoryRoot, "py")
	if err := os.Symlink(target, link); err != nil && !os.IsExist(err) {
		t.Fatalf("symlink py/ into %s: %v", factoryRoot, err)
	}
}

// terminateMCPServer reads factoryRoot/.runtime/mcp_server.json and SIGTERMs
// the recorded PID. Best-effort — swallows all errors. Intended for t.Cleanup
// to avoid orphaning the Python subprocess after t.TempDir is removed.
func terminateMCPServer(factoryRoot string) {
	epFile := filepath.Join(factoryRoot, ".runtime", "mcp_server.json")
	data, err := os.ReadFile(epFile)
	if err != nil {
		return
	}
	var info struct {
		PID int `json:"pid"`
	}
	if err := json.Unmarshal(data, &info); err != nil || info.PID <= 0 {
		return
	}
	_ = syscall.Kill(info.PID, syscall.SIGTERM)
}

func buildAF(t *testing.T) string {
	t.Helper()
	return buildAFInto(t, t.TempDir())
}

// buildAFInto lets a caller choose where the binary lands. t.TempDir() is where the noexec trap lives,
// and a caller whose subject is the hook's own PATH resolution has to plant af where the hook looks.
func buildAFInto(t *testing.T, dir string) string {
	t.Helper()
	return buildAFIntoContext(context.Background(), t, dir)
}

// buildAFIntoContext puts the compile itself under the caller's deadline. A test whose contract is a
// hard wall-clock bound cannot leave the slowest step in the middle of it unbounded.
func buildAFIntoContext(ctx context.Context, t *testing.T, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, "af")
	cmd := exec.CommandContext(ctx, "go", "build", "-o", binary, "./cmd/af")
	cmd.Dir = findRepoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building af: %s\n%s", err, out)
	}
	return binary
}

// plantAFUnderHome builds af where a provisioned hook command's own PATH export will find it —
// $HOME/go/bin:$HOME/.local/bin:$HOME/bin — and then proves the planted file RUNS there.
//
// The exec proof is what keeps either caller from passing by accident. A binary that cannot exec
// produces exactly the output a healthy run produces for both of them: an empty stdout for the
// per-role SessionStart proof, and a silent admit for the live deny probe. Neither would notice.
func plantAFUnderHome(ctx context.Context, t *testing.T, afBin string) {
	t.Helper()
	if built := buildAFIntoContext(ctx, t, filepath.Dir(afBin)); built != afBin {
		t.Fatalf("af was built at %s, not the %s the hook's PATH export resolves", built, afBin)
	}
	if _, err := exec.CommandContext(ctx, afBin, "root").Output(); err != nil {
		// `af root` outside a factory exits non-zero; all that matters here is that the file RAN.
		if _, isExit := err.(*exec.ExitError); !isExit {
			t.Fatalf("the freshly built af at %s cannot be executed: %v", afBin, err)
		}
	}
}

// assertNotNestedFactory fails unless root is the outermost factory on its path.
//
// tryExecCapableDir's last candidate is the repo tree, which is itself a factory, and a factory
// nested inside another resolves to the OUTER root — so every af a caller then runs would read the
// real factory's config, agents, mail and memory instead of the disposable ones it just staged, and
// would keep exiting 0 while doing it.
func assertNotNestedFactory(t *testing.T, root string) {
	t.Helper()
	if enclosing, err := config.FindFactoryRoot(filepath.Dir(root)); err == nil && enclosing != "" {
		t.Fatalf("the disposable factory at %s sits inside another factory at %s; every af below "+
			"would resolve the wrong root", root, enclosing)
	}
}

func runAF(t *testing.T, binary, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+dir)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("af %s: %s\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

func runAFMayFail(t *testing.T, binary, dir string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "HOME="+dir)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// setupTerminationTest creates a factory workspace with a 1-step formula, runs
// af sling --formula to instantiate it, and creates a tmux session. Returns the
// binary path, workspace root, agent directory, and tmux session name.
func setupTerminationTest(t *testing.T, agentName string) (binary, workspace, agentDir, sessionName string) {
	t.Helper()

	requirePython3WithServerDeps(t)
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not available")
	}

	binary = buildAF(t)
	workspace = t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	// git init — needed for worktree and for the store subdirectory
	// to live inside a repo. The MCP server does not require git.
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	runAF(t, binary, workspace, "install", "--init")

	// Add test agent to agents.json so mail routing works (detectSender
	// validates the sender against agents.json).
	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	agentsJSON := fmt.Sprintf(
		`{"agents":{"manager":{"type":"interactive","description":"manager"},"supervisor":{"type":"autonomous","description":"supervisor"},"%s":{"type":"autonomous","description":"test agent"}}}`,
		agentName,
	)
	if err := os.WriteFile(agentsPath, []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("writing agents.json: %v", err)
	}

	// Create 1-step formula TOML
	formulaDir := config.FormulasDir(workspace)
	formulaContent := "formula = \"test-terminate\"\ntype = \"workflow\"\nversion = 1\n\n[[steps]]\nid = \"step1\"\ntitle = \"Only step\"\n"
	if err := os.WriteFile(filepath.Join(formulaDir, "test-terminate.formula.toml"), []byte(formulaContent), 0o644); err != nil {
		t.Fatalf("writing formula: %v", err)
	}

	// Create agent directory
	agentDir = filepath.Join(workspace, ".agentfactory", "agents", agentName)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("creating agent dir: %v", err)
	}

	// Instantiate formula from agent dir (creates beads + hooked_formula + formula_caller)
	runAF(t, binary, agentDir, "sling", "--formula", "test-terminate", "--var", "issue=test", "--no-launch")

	// Create tmux session
	sessionName = "af-" + agentName
	cmd := exec.Command("tmux", "new-session", "-d", "-s", sessionName, "-c", agentDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("tmux new-session: %s\n%s", err, out)
	}
	t.Cleanup(func() {
		exec.Command("tmux", "kill-session", "-t", sessionName).Run()
	})

	return binary, workspace, agentDir, sessionName
}

func TestAutoTermination_DispatchedSession(t *testing.T) {
	binary, _, agentDir, sessionName := setupTerminationTest(t, "test-specialist")

	// Write dispatch marker and formula_caller (overwrite whatever sling wrote)
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("manager"), 0o644)
	os.Remove(filepath.Join(runtimeDir, "formula_caller"))
	os.WriteFile(filepath.Join(runtimeDir, "formula_caller"), []byte("manager"), 0o644)

	// Prime the step (production flow: af prime runs before af done)
	runAF(t, binary, agentDir, "prime")

	// Run af done — closes the single step, sends WORK_DONE, auto-terminates
	out, err := runAFMayFail(t, binary, agentDir, "done")
	if err != nil {
		t.Logf("af done output:\n%s", out)
		t.Fatalf("af done failed: %v", err)
	}

	// Verify: tmux session no longer exists
	if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err == nil {
		t.Fatal("tmux session should have been killed but is still alive")
	}

	// Verify: dispatched marker removed by cleanupRuntimeArtifacts
	if _, err := os.Stat(filepath.Join(runtimeDir, "dispatched")); !os.IsNotExist(err) {
		t.Fatal(".runtime/dispatched should have been removed")
	}

	// Verify: last_termination breadcrumb exists
	if _, err := os.Stat(filepath.Join(runtimeDir, "last_termination")); err != nil {
		t.Fatal(".runtime/last_termination should exist after auto-termination")
	}

	// Verify: output contains auto-termination message
	if !strings.Contains(out, "Auto-terminating dispatched session") {
		t.Fatalf("output should contain 'Auto-terminating dispatched session', got:\n%s", out)
	}
}

func TestNoAutoTermination_PersistentSession(t *testing.T) {
	binary, _, agentDir, sessionName := setupTerminationTest(t, "test-persistent")

	// DO NOT write .runtime/dispatched — this is the key difference

	// Prime the step (production flow: af prime runs before af done)
	runAF(t, binary, agentDir, "prime")

	// Run af done
	out, err := runAFMayFail(t, binary, agentDir, "done")
	if err != nil {
		t.Logf("af done output:\n%s", out)
		t.Fatalf("af done failed: %v", err)
	}

	// Verify: tmux session is STILL alive
	if err := exec.Command("tmux", "has-session", "-t", "="+sessionName).Run(); err != nil {
		t.Fatal("tmux session should still be alive for persistent session")
	}

	// Verify: no dispatched marker
	runtimeDir := filepath.Join(agentDir, ".runtime")
	if _, err := os.Stat(filepath.Join(runtimeDir, "dispatched")); !os.IsNotExist(err) {
		t.Fatal(".runtime/dispatched should not exist")
	}

	// Verify: no last_termination breadcrumb
	if _, err := os.Stat(filepath.Join(runtimeDir, "last_termination")); !os.IsNotExist(err) {
		t.Fatal(".runtime/last_termination should not exist for persistent session")
	}

	// Verify: output does NOT contain auto-termination message
	if strings.Contains(out, "Auto-terminating") {
		t.Fatalf("output should NOT contain 'Auto-terminating', got:\n%s", out)
	}
}

func TestAutoTermination_MailDeliveredBeforeKill(t *testing.T) {
	binary, workspace, agentDir, _ := setupTerminationTest(t, "test-mailcheck")

	// Write dispatch marker and formula_caller
	runtimeDir := filepath.Join(agentDir, ".runtime")
	os.WriteFile(filepath.Join(runtimeDir, "dispatched"), []byte("manager"), 0o644)
	os.Remove(filepath.Join(runtimeDir, "formula_caller"))
	os.WriteFile(filepath.Join(runtimeDir, "formula_caller"), []byte("manager"), 0o644)

	// Prime the step (production flow: af prime runs before af done)
	runAF(t, binary, agentDir, "prime")

	// Run af done
	out, err := runAFMayFail(t, binary, agentDir, "done")
	if err != nil {
		t.Logf("af done output:\n%s", out)
		t.Fatalf("af done failed: %v", err)
	}

	// Verify: WORK_DONE mail bead exists. WORK_DONE is a Title prefix on a
	// TypeTask bead (see done.go: `fmt.Sprintf("WORK_DONE: %s", instanceID)`),
	// not a Label or Type, so we scan List output for the Title substring.
	store, err := mcpstore.New(workspace, "")
	if err != nil {
		t.Fatalf("mcpstore.New for WORK_DONE check: %v", err)
	}
	issues, err := store.List(context.Background(), issuestore.Filter{
		IncludeAllAgents: true,
		IncludeClosed:    true,
	})
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	foundWorkDone := false
	for _, iss := range issues {
		if strings.Contains(iss.Title, "WORK_DONE") {
			foundWorkDone = true
			break
		}
	}
	if !foundWorkDone {
		t.Fatalf("expected WORK_DONE bead in %d issues, none found", len(issues))
	}

	// Verify: last_termination contains a valid RFC3339 timestamp
	termData, err := os.ReadFile(filepath.Join(runtimeDir, "last_termination"))
	if err != nil {
		t.Fatalf("reading last_termination: %v", err)
	}
	termStr := strings.TrimSpace(string(termData))
	// Format is "auto-terminated at <RFC3339>"
	if !strings.HasPrefix(termStr, "auto-terminated at ") {
		t.Fatalf("last_termination should start with 'auto-terminated at', got: %s", termStr)
	}
	timestamp := strings.TrimPrefix(termStr, "auto-terminated at ")
	if _, err := time.Parse(time.RFC3339, timestamp); err != nil {
		t.Fatalf("last_termination timestamp is not valid RFC3339: %s (err: %v)", timestamp, err)
	}

	// The fact that WORK_DONE bead exists AND last_termination exists proves
	// mail was sent before kill (sendWorkDoneMail runs before selfTerminate in
	// sendWorkDoneAndCleanup, and shouldAutoTerminate would have returned false
	// if mail had failed, preventing the kill entirely).
	_ = out
}

func TestE2EWorkflow(t *testing.T) {
	requirePython3WithServerDeps(t)

	binary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	// git init — the MCP server does not require git; left for factory parity.
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	// 1. af install --init
	runAF(t, binary, workspace, "install", "--init")

	// 2. Verify init artifacts
	for _, path := range []string{
		".agentfactory/store",
		".agentfactory/factory.json",
		".agentfactory/agents.json",
		".agentfactory/messaging.json",
	} {
		if _, err := os.Stat(filepath.Join(workspace, path)); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}

	// 3. af install manager and supervisor
	runAF(t, binary, workspace, "install", "manager")
	runAF(t, binary, workspace, "install", "supervisor")

	// 4. Verify agent artifacts
	for _, path := range []string{
		".agentfactory/agents/manager/CLAUDE.md",
		".agentfactory/agents/manager/.claude/settings.json",
		".agentfactory/agents/supervisor/CLAUDE.md",
		".agentfactory/agents/supervisor/.claude/settings.json",
	} {
		if _, err := os.Stat(filepath.Join(workspace, path)); err != nil {
			t.Fatalf("expected %s to exist: %v", path, err)
		}
	}

	// 5. af prime from manager/
	managerDir := filepath.Join(workspace, ".agentfactory", "agents", "manager")
	primeOut := runAF(t, binary, managerDir, "prime")
	if !strings.Contains(primeOut, "[AGENT FACTORY]") {
		t.Fatalf("prime output should contain [AGENT FACTORY], got:\n%s", primeOut)
	}

	// 6. af root from manager/
	rootOut := runAF(t, binary, managerDir, "root")
	if !strings.Contains(strings.TrimSpace(rootOut), workspace) {
		t.Fatalf("root output should contain workspace path %s, got: %s", workspace, rootOut)
	}

	// 7. af mail send from manager/ to supervisor
	runAF(t, binary, managerDir, "mail", "send", "supervisor", "-s", "e2e-test", "-m", "hello")

	// 8. af mail inbox from supervisor/
	supervisorDir := filepath.Join(workspace, ".agentfactory", "agents", "supervisor")
	inboxOut := runAF(t, binary, supervisorDir, "mail", "inbox")
	if !strings.Contains(inboxOut, "e2e-test") {
		t.Fatalf("inbox should contain e2e-test message, got:\n%s", inboxOut)
	}

	// 9. Verify quality gate hook
	qgPath := filepath.Join(workspace, ".agentfactory", "hooks", "quality-gate.sh")
	info, err := os.Stat(qgPath)
	if err != nil {
		t.Fatalf("quality-gate.sh should exist: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatal("quality-gate.sh should be executable")
	}

	// 10. Verify fidelity gate hook
	fgPath := filepath.Join(workspace, ".agentfactory", "hooks", "fidelity-gate.sh")
	fgInfo, err := os.Stat(fgPath)
	if err != nil {
		t.Fatalf("fidelity-gate.sh should exist: %v", err)
	}
	if fgInfo.Mode()&0111 == 0 {
		t.Fatal("fidelity-gate.sh should be executable")
	}
}

// TestMailRoundTrip covers AC-6: mail send → inbox → read → reply → delete →
// check across manager and supervisor agents, end-to-end through the af CLI
// against the mcpstore-backed Python MCP server.
func TestMailRoundTrip(t *testing.T) {
	requirePython3WithServerDeps(t)

	binary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@mail.test"},
		{"config", "user.name", "Mail Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	runAF(t, binary, workspace, "install", "--init")
	runAF(t, binary, workspace, "install", "manager")
	runAF(t, binary, workspace, "install", "supervisor")

	managerDir := filepath.Join(workspace, ".agentfactory", "agents", "manager")
	supervisorDir := filepath.Join(workspace, ".agentfactory", "agents", "supervisor")

	// 1. Empty inbox: af mail check returns non-zero when no mail (errNoMail).
	if out, err := runAFMayFail(t, binary, supervisorDir, "mail", "check"); err == nil {
		t.Fatalf("expected `af mail check` to exit non-zero with empty inbox, got nil error\noutput:\n%s", out)
	}

	// 2. Send from manager → supervisor.
	runAF(t, binary, managerDir, "mail", "send", "supervisor",
		"-s", "round-trip-subject", "-m", "round-trip-body")

	// 3. Inbox on supervisor lists the message; first column is the mail ID.
	inboxOut := runAF(t, binary, supervisorDir, "mail", "inbox")
	if !strings.Contains(inboxOut, "round-trip-subject") {
		t.Fatalf("inbox should list round-trip-subject, got:\n%s", inboxOut)
	}
	mailID := parseFirstMailID(t, inboxOut)

	// 4. Check returns zero (there IS mail).
	runAF(t, binary, supervisorDir, "mail", "check")

	// 5. Read returns full body.
	readOut := runAF(t, binary, supervisorDir, "mail", "read", mailID)
	if !strings.Contains(readOut, "round-trip-body") {
		t.Fatalf("read should contain body, got:\n%s", readOut)
	}

	// 6. Reply from supervisor back to manager.
	runAF(t, binary, supervisorDir, "mail", "reply", mailID, "-m", "reply-body")

	// 7. Manager inbox shows the reply (subject is "Re: round-trip-subject").
	mgrInbox := runAF(t, binary, managerDir, "mail", "inbox")
	if !strings.Contains(mgrInbox, "Re: round-trip-subject") {
		t.Fatalf("manager inbox should contain reply, got:\n%s", mgrInbox)
	}

	// 8. Delete the original from supervisor's inbox.
	runAF(t, binary, supervisorDir, "mail", "delete", mailID)

	// 9. Supervisor inbox no longer lists the deleted mail. Assert on the
	// captured mailID rather than the subject string — the supervisor inbox
	// may also contain the supervisor-authored reply ("Re: round-trip-subject"),
	// which would otherwise fool a substring match on "round-trip-subject".
	inboxAfterDelete, _ := runAFMayFail(t, binary, supervisorDir, "mail", "inbox")
	if strings.Contains(inboxAfterDelete, mailID) {
		t.Fatalf("deleted mail %s should be gone from inbox, got:\n%s", mailID, inboxAfterDelete)
	}
}

// parseFirstMailID extracts the first data-row's ID column from `af mail
// inbox` output. The output format (see internal/cmd/mail.go:runMailInbox) is
// a tabwriter table with a header row "ID  FROM  SUBJECT  PRIORITY  TIME  DELIVERED".
// Only field 0 is read, so a column appended at the right edge cannot break this.
func parseFirstMailID(t *testing.T, inboxOut string) string {
	t.Helper()
	for _, line := range strings.Split(inboxOut, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		if fields[0] == "ID" {
			continue // header row
		}
		return fields[0]
	}
	t.Fatalf("no mail ID found in inbox output:\n%s", inboxOut)
	return ""
}

// --- #675 K9: the out-of-process SessionStart proof (constraint C-8) ---
//
// Everything below drives a BUILT af through the commands a real agent's provisioned
// .claude/settings.json actually carries. The in-process TestMailCheckInject_* / TestMemoryCheckInject_*
// families call the render functions directly, in one process, and so cannot observe the four places
// this surface can silently fail in production: the provisioned command string, the envelope
// encoding, the mail append, and the hook's own PATH resolution.

const (
	// The pane id every hook child is given. ADR-018: claimedSession wants a non-empty TMUX_PANE,
	// not a live tmux. It is set EXPLICITLY rather than inherited because NeutralizeAFEnv wipes only
	// the AF_/CLAUDE_ prefixes, so an ambient pane survives for an agent running the suite from
	// inside one and is absent in CI — the dedup assertions below would then hold on a developer
	// host and fail on the runner, for a reason that reads like a dedup bug.
	sessionStartPane = "%0"
	// internal/templates/roles/ and .agentfactory/agents.json both hold 43 today, so this floor is
	// exact rather than slack. It is not a claim about how many agents a factory may have — it is
	// the tripwire for an enumerator that has quietly stopped seeing the embedded templates, which
	// would otherwise turn this whole sweep into a loop over nothing.
	sessionStartRosterFloor = 43

	primeHookSegment          = "af prime --hook"
	mailHookSegment           = "af mail check --inject"
	hookEventUserPromptSubmit = "UserPromptSubmit"

	// The one line that says a mail block was delivered (mail.go:652), and the anchor every one of
	// the 43 role templates opens its identity with. Counting the first across every entry is AC-4;
	// finding the second anywhere is an AC-2 failure.
	mailInjectionHeader   = "Mail delivered to "
	memoryInjectionHeader = "Memory from your own past runs"
	identityAnchor        = "# Agent Identity:"
	primeSessionHeader    = "[AGENT FACTORY]"
	// Both injecting writers spell their budget-dropped remainder this way (mail.go:660,
	// memory.go:987).
	injectOverflowPrefix = "…and "
)

// sessionStartFx is a disposable factory whose af is planted where the PROVISIONED hook command's
// own PATH export looks for it.
type sessionStartFx struct {
	base  string
	home  string
	root  string
	afBin string
	env   []string
}

// newSessionStartFactory brings up that factory and installs every named role.
//
// The binary is planted under the sandbox HOME rather than merely prepended to PATH because the
// provisioned command PREPENDS $HOME/go/bin:$HOME/.local/bin:$HOME/bin to whatever the test passes
// — so the assertion this test can make is not "our PATH entry happened to win" but "the hook's own
// prefix resolved our binary". Makefile:39-41 puts a real af at ~/.local/bin on any host that has
// run `make install`, and CI's regen and integration jobs share one self-hosted runner HOME, so the
// alternative is a green run that measured somebody else's af.
func newSessionStartFactory(ctx context.Context, t *testing.T, roles []string) sessionStartFx {
	t.Helper()

	// Fatal, not a skip: a binary that cannot exec produces an empty stdout, and every assertion
	// below is satisfied by an empty stdout except the positive block checks — which is exactly the
	// vacuity Gap 18 exists to prevent, so the degraded path must not be reachable.
	base, err := tryExecCapableDir(t, "af-test-sessionstart")
	if err != nil {
		t.Fatalf("no exec-capable filesystem for the SessionStart proof: %v — the built af could not "+
			"run and the hooks would silently resolve the ambient one", err)
	}

	fx := sessionStartFx{
		base:  base,
		home:  filepath.Join(base, "home"),
		root:  filepath.Join(base, "factory"),
		afBin: filepath.Join(base, "home", ".local", "bin", "af"),
	}
	for _, dir := range []string{filepath.Dir(fx.afBin), fx.root, filepath.Join(base, "transcripts")} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	assertNotNestedFactory(t, fx.root)

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@sessionstart.test"},
		{"config", "user.name", "SessionStart Test"},
	} {
		cmd := exec.CommandContext(ctx, "git", args...)
		cmd.Dir = fx.root
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	ensurePySymlink(t, fx.root)
	t.Cleanup(func() { terminateMCPServer(fx.root) })

	plantAFUnderHome(ctx, t, fx.afBin)

	fx.env = []string{
		"HOME=" + fx.home,
		"PATH=" + filepath.Dir(fx.afBin) + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMUX_PANE=" + sessionStartPane,
	}

	runAF(t, fx.afBin, fx.root, "install", "--init")

	// af install <role> fails if the role is absent from agents.json (install.go:595-598), and
	// --init scaffolds only manager and supervisor — so the whole roster is written before the
	// install loop. Role type selects which settings template lands; SessionStart is byte-identical
	// across both, and keeping manager interactive exercises each one at least once.
	type agentEntry struct {
		Type        string `json:"type"`
		Description string `json:"description"`
	}
	agents := map[string]agentEntry{}
	for _, role := range roles {
		kind := "autonomous"
		if role == "manager" {
			kind = "interactive"
		}
		agents[role] = agentEntry{Type: kind, Description: role + " (SessionStart proof)"}
	}
	agentsJSON, err := json.Marshal(struct {
		Agents map[string]agentEntry `json:"agents"`
	}{agents})
	if err != nil {
		t.Fatalf("marshalling agents.json: %v", err)
	}
	if err := os.WriteFile(config.AgentsConfigPath(fx.root), agentsJSON, 0o644); err != nil {
		t.Fatalf("writing agents.json: %v", err)
	}
	for _, role := range roles {
		runAF(t, fx.afBin, fx.root, "install", role)
	}

	// The stale-binary hazard, made loud. Run the provisioned prefix itself and ask it which af it
	// found: everything after this line reads the output of a hook that resolved af on its own.
	resolved := strings.TrimSpace(runSessionStartHook(ctx, t, fx, fx.root, sessionStartHookPrefix+"command -v af", ""))
	if resolved != fx.afBin {
		t.Fatalf("the provisioned PATH export resolves af to %s, not the %s this test built; every "+
			"assertion below would be about the wrong binary", resolved, fx.afBin)
	}
	return fx
}

// runSessionStartHook runs one provisioned hook command the way the harness runs it: bash -c on the
// command string VERBATIM, cwd at the agent dir, the payload on stdin.
//
// stdout and stderr are captured separately. prime warns on stderr when TMUX_PANE is missing
// (prime.go:511-513), and a combined capture would corrupt both the JSON decode and the encoded
// length that is this test's whole subject.
func runSessionStartHook(ctx context.Context, t *testing.T, fx sessionStartFx, workDir, command, payload string) string {
	t.Helper()
	cmd := exec.CommandContext(ctx, "bash", "-c", command)
	cmd.Dir = workDir
	// Never leave stdin at the inherited /dev/null: readHookPayloadFromCmd returns the ZERO payload
	// for a char device (hook_payload.go:42-51), which empties session_id, which makes
	// claimedSession false — mail would then emit and record nothing, and the UserPromptSubmit
	// assertion would fail looking exactly like a dedup bug.
	cmd.Stdin = strings.NewReader(payload)
	cmd.Env = append(os.Environ(), fx.env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		// A caller's deadline surfaces here as `signal: killed`, not as a testing timeout, because
		// the context belongs to this exec rather than to the test binary.
		t.Fatalf("hook %q in %s: %v (a `signal: killed` here is the caller's context deadline)\n"+
			"stderr:\n%s", command, workDir, err, stderr.String())
	}
	// A hook's stderr is not shown to the session, so a warning that starts firing on every launch
	// is invisible from inside Claude Code and free to go unnoticed for as long as it likes. prime
	// has two on nil-error paths (prime.go:632, :740) that this fixture is expected to keep quiet.
	if stderr.Len() != 0 {
		t.Errorf("hook %q wrote to stderr:\n%s", command, stderr.String())
	}
	return stdout.String()
}

// sessionStartPayload spells the harness's stdin object as WIRE TEXT rather than marshalling
// internal/cmd's own hookPayload: a renamed json tag has to go red here, and a test that encoded
// with the very struct the hook decodes with would stay green through exactly that rename.
func sessionStartPayload(sessionID, transcript, event string) string {
	// source is a SessionStart field and is what mailSourceResets keys on; a UserPromptSubmit
	// carries none.
	source := ""
	if event == hookEventSessionStart {
		source = `"source":"startup",`
	}
	return fmt.Sprintf(`{"session_id":%q,"transcript_path":%q,%s"hook_event_name":%q}`,
		sessionID, transcript, source, event)
}

// maxExcerptBody fills an injector's per-item excerpt exactly, behind a marker the assertions can
// find. Exactly at the budget and not over: above it the excerpt truncates, and a truncated body
// makes "the seeded body arrived whole" unmeasurable.
func maxExcerptBody(t *testing.T, marker string, chars int) string {
	t.Helper()
	if len(marker) > chars {
		t.Fatalf("marker %q is longer than the %d-character excerpt budget", marker, chars)
	}
	return marker + strings.Repeat("x", chars-len(marker))
}

// mailIDsIn names every message a rendered mail block actually carried; renderMailEntry opens each
// with "- [<id>] From: …" (mail.go:629).
//
// An id set, rather than a search for a particular body, is what lets the paging assertion below say
// "the next call delivered one this call did not" without also asserting WHICH message the budget
// deferred. selectMailForInjection walks the store's order and stops at the first entry that does
// not fit (mail.go:608-621), so the deferred message is the one at the far end of that walk — not
// necessarily the one most recently sent.
func mailIDsIn(block string) map[string]bool {
	ids := map[string]bool{}
	for _, line := range strings.Split(block, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "- [") {
			continue
		}
		if end := strings.Index(line, "]"); end > len("- [") {
			ids[line[len("- ["):end]] = true
		}
	}
	return ids
}

// sessionStartWriterOf names which of the three #675 K3 writers an entry is, from the command
// itself rather than from its position. The ORDER is pinned by assertProvisionedSessionStart
// (install_test.go:107) and re-asserting it here would be a second copy of the same claim; what
// this test needs is to know which block to expect back from which entry.
func sessionStartWriterOf(command string) string {
	switch {
	case strings.HasSuffix(command, primeHookSegment):
		return "prime"
	case strings.HasSuffix(command, mailHookSegment):
		return "mail"
	case strings.HasSuffix(command, memoryHookSegment):
		return "memory"
	}
	return ""
}

// TestSessionStartOutputFitsInlineLimit_EveryRole is #675 K9, and it is constraint C-8's whole
// point: the size assertion made OUT OF PROCESS. For every shipped role it provisions the role,
// seeds mail and memory at their budget maxima, runs each SessionStart entry AS PROVISIONED with a
// fake TMUX_PANE and a real stdin payload, and asserts len(stdout) < 10000 — the ENCODED envelope,
// because the harness judges raw stdout characters (analyst concern 15: all 2,066 observed
// truncations were raw-stdout-sized) — with the decoded additionalContext length reported as the
// secondary figure.
//
// The vacuity guards are load-bearing rather than ceremonial (Gap 18). Every --inject failure path
// returns nil and ZERO output, and the per-writer budgets cap each block well under 10,000, so a
// silently broken verb sails under the cap: what makes the size assertion mean anything is the
// positive proof that both blocks, and the seeded bodies inside them, are actually there.
//
//	Scenario: A fresh session of any shipped role receives mail and memory, and identity never
//	  Given a role provisioned with three independent SessionStart entries
//	  And one message per K slot and one note per K slot, each at its excerpt maximum
//	  When each entry runs as provisioned, then the first UserPromptSubmit runs
//	  Then each entry is exactly one SessionStart-labelled JSON object under 10000 characters
//	  And both blocks and their seeded bodies are present with nothing deferred
//	  And "# Agent Identity:" appears nowhere
//	  And the first UserPromptSubmit emits ZERO BYTES, for exactly one mail header in total
func TestSessionStartOutputFitsInlineLimit_EveryRole(t *testing.T) {
	requirePython3WithServerDeps(t)

	// The embedded roster, not a glob of the checkout: what ships is the binary's embed.FS, and a
	// sweep that means "every shipped role" has to enumerate the thing that shipped.
	roles := templates.Roles()
	if len(roles) == 0 {
		t.Fatal("the embedded roster enumerated zero roles; this sweep would pass having proven nothing")
	}
	if len(roles) < sessionStartRosterFloor {
		t.Fatalf("the embedded roster holds %d roles, want at least %d", len(roles), sessionStartRosterFloor)
	}

	// A hang detector, not a budget: the whole sweep measures ~15s against Makefile:99's 4m
	// per-package timeout, which the rest of this package's integration tests also draw on.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	fx := newSessionStartFactory(ctx, t, roles)
	managerDir := filepath.Join(fx.root, ".agentfactory", "agents", "manager")

	maxEncoded, maxDecoded, maxRole := map[string]int{}, map[string]int{}, map[string]string{}

	for _, role := range roles {
		agentDir := filepath.Join(fx.root, ".agentfactory", "agents", role)
		settingsPath := filepath.Join(agentDir, ".claude", "settings.json")

		// One message per K slot and one note per K slot, each exactly at its excerpt maximum — the
		// state the budgets were sized for. K messages, not K+1: injectMail records only the ids it
		// SERVED (mail.go:558-562), so a budget-deferred message is delivered on the NEXT call,
		// which is the first UserPromptSubmit this test then asserts is silent. That paging path is
		// covered by its own subtest below rather than by weakening this one.
		//
		// The fit is deliberately tight, and the arithmetic is worth stating because it is the same
		// for all 43 roles: ids are fixed-width (`af-` + 8 hex), so each rendered entry is 74 bytes
		// of header + 603 of indented body + 1 blank = 678, and 678 × 6 = 4,068 against
		// mailInjectTotalBytes 4,096. That 28-byte margin IS the tripwire mailInjectK's own doc
		// (mail.go:506-510) describes — anything adding ≥5 bytes per entry reds the no-overflow
		// assertion below for every role at once, which is the intended signal, not a fixture bug.
		mailBody := maxExcerptBody(t, "k9-mail-"+role+" ", mailInjectExcerptChars)
		for i := range mailInjectK {
			runAF(t, fx.afBin, managerDir, "mail", "send", role, "-s", fmt.Sprintf("S%d", i), "-m", mailBody)
		}
		noteBody := maxExcerptBody(t, "k9-note-"+role+" ", memory.DefaultExcerptChars)
		for i := range memory.DefaultK {
			seedNote(t, fx.root, role, memory.Note{
				ID:   fmt.Sprintf("k9-note-%d", i),
				Type: memory.TypeGotcha,
				Body: noteBody,
			})
		}

		sessionID := "sess-" + role
		transcript := filepath.Join(fx.base, "transcripts", role+".jsonl")

		entries := provisionedHookCommands(t, settingsPath, hookEventSessionStart)
		mailHeaders, seen := 0, map[string]bool{}
		for _, command := range entries {
			writer := sessionStartWriterOf(command)
			if writer == "" {
				t.Fatalf("%s: SessionStart entry %q is none of the three #675 K3 writers", role, command)
			}
			seen[writer] = true

			stdout := runSessionStartHook(ctx, t, fx, agentDir, command, sessionStartPayload(sessionID, transcript, hookEventSessionStart))
			if stdout == "" {
				t.Errorf("%s %s entry emitted nothing; every --inject failure path returns zero bytes, "+
					"so this is the fixture or the verb failing silently", role, writer)
				continue
			}
			// json.Encoder appends exactly one newline per value, so a second object would show up
			// as an interior one.
			if n := strings.Count(strings.TrimSpace(stdout), "\n"); n != 0 {
				t.Errorf("%s %s entry emitted %d JSON objects, want exactly one:\n%s", role, writer, n+1, stdout)
			}
			if got := hookEventOf(t, stdout); got != hookEventSessionStart {
				t.Errorf("%s %s entry declared hookEventName %q, want %q", role, writer, got, hookEventSessionStart)
			}
			block := decodeAdditionalContext(t, stdout)

			if len(stdout) >= 10000 {
				t.Errorf("%s %s entry: encoded stdout is %d characters, at or over the harness's "+
					"observed ~10000-character cap (decoded block %d)", role, writer, len(stdout), len(block))
			}
			if len(stdout) > maxEncoded[writer] {
				maxEncoded[writer], maxDecoded[writer], maxRole[writer] = len(stdout), len(block), role
			}
			if strings.Contains(stdout, identityAnchor) {
				t.Errorf("%s %s entry carried %q into the session; the SessionStart hook withholds "+
					"identity unconditionally (#675 K1, AC-2)", role, writer, identityAnchor)
			}
			mailHeaders += strings.Count(block, mailInjectionHeader)

			switch writer {
			case "prime":
				if !strings.Contains(block, primeSessionHeader) {
					t.Errorf("%s prime entry carries no %q line:\n%s", role, primeSessionHeader, block)
				}
			case "mail":
				if !strings.Contains(block, mailInjectionHeader+role) {
					t.Errorf("%s mail entry carries no mail block:\n%s", role, block)
				}
				if !strings.Contains(block, mailBody) {
					t.Errorf("%s mail entry carries no seeded body; the block arrived without what it is for", role)
				}
				if strings.Contains(block, injectOverflowPrefix) {
					t.Errorf("%s mail entry deferred a message: the %d-message fixture no longer fits "+
						"mailInjectTotalBytes, and the zero-byte UserPromptSubmit assertion below "+
						"depends on nothing being deferred:\n%s", role, mailInjectK, block)
				}
			case "memory":
				if !strings.Contains(block, memoryInjectionHeader) {
					t.Errorf("%s memory entry carries no memory block:\n%s", role, block)
				}
				if !strings.Contains(block, noteBody) {
					t.Errorf("%s memory entry carries no seeded note body", role)
				}
				if strings.Contains(block, injectOverflowPrefix) {
					t.Errorf("%s memory entry deferred a note: the %d-note fixture no longer fits "+
						"memory.DefaultTotalBytes:\n%s", role, memory.DefaultK, block)
				}
			}
		}
		for _, writer := range []string{"prime", "mail", "memory"} {
			if !seen[writer] {
				t.Fatalf("%s: SessionStart provisions no %s entry", role, writer)
			}
		}

		// AC-4 spans SessionStart AND the first prompt (Gap 13): a SessionStart-only count would
		// miss the first-prompt re-delivery the deleted shell-out used to produce.
		ups := provisionedHookCommands(t, settingsPath, hookEventUserPromptSubmit)
		if len(ups) != 1 {
			t.Fatalf("%s: UserPromptSubmit carries %d commands, want 1", role, len(ups))
		}
		upsOut := runSessionStartHook(ctx, t, fx, agentDir, ups[0], sessionStartPayload(sessionID, transcript, hookEventUserPromptSubmit))
		if upsOut != "" {
			// The zero case is zero BYTES for all three writers, not an envelope around an empty
			// block (hook_context.go:31-33).
			t.Errorf("%s: the first UserPromptSubmit emitted %d bytes:\n%s", role, len(upsOut), upsOut)
			mailHeaders += strings.Count(decodeAdditionalContext(t, upsOut), mailInjectionHeader)
		}
		if mailHeaders != 1 {
			t.Errorf("%s: %d mail headers across the three SessionStart entries and the first "+
				"UserPromptSubmit, want exactly 1 (AC-4)", role, mailHeaders)
		}
	}

	for _, writer := range []string{"prime", "mail", "memory"} {
		t.Logf("%s entry ceiling over %d roles: encoded %d characters (%s), decoded block %d",
			writer, len(roles), maxEncoded[writer], maxRole[writer], maxDecoded[writer])
	}

	// The K+1 case the sweep deliberately does not carry. Seeding K+1 and asserting a silent first
	// prompt are mutually exclusive by design — recordDelivered stamps only what entered context
	// (mail_delivered.go:79-81) — so the deferred message's fate is asserted here instead of being
	// dropped.
	t.Run("BudgetDeferredMessagePagesOntoTheNextCall", func(t *testing.T) {
		const role = "supervisor"
		agentDir := filepath.Join(fx.root, ".agentfactory", "agents", role)
		settingsPath := filepath.Join(agentDir, ".claude", "settings.json")
		managerDir := filepath.Join(fx.root, ".agentfactory", "agents", "manager")

		// The sweep left K open messages for this role and closed none — injection never calls
		// MarkRead or Close (C-13). One more makes K+1.
		runAF(t, fx.afBin, managerDir, "mail", "send", role,
			"-s", "S6", "-m", maxExcerptBody(t, "k9-overflow ", mailInjectExcerptChars))

		var mailEntry string
		for _, command := range provisionedHookCommands(t, settingsPath, hookEventSessionStart) {
			if sessionStartWriterOf(command) == "mail" {
				mailEntry = command
			}
		}
		sessionID, transcript := "sess-overflow", filepath.Join(fx.base, "transcripts", "overflow.jsonl")

		block := decodeAdditionalContext(t, runSessionStartHook(ctx, t, fx, agentDir, mailEntry,
			sessionStartPayload(sessionID, transcript, hookEventSessionStart)))
		if !strings.Contains(block, injectOverflowPrefix) {
			t.Fatalf("%d messages at max excerpt did not overflow the budget, so this subtest is "+
				"asserting nothing:\n%s", mailInjectK+1, block)
		}
		served := mailIDsIn(block)
		if len(served) != mailInjectK {
			t.Fatalf("SessionStart served %d of the %d messages, want the K cap of %d:\n%s",
				len(served), mailInjectK+1, mailInjectK, block)
		}

		next := runSessionStartHook(ctx, t, fx, agentDir, mailEntry,
			sessionStartPayload(sessionID, transcript, hookEventUserPromptSubmit))
		if next == "" {
			t.Fatal("the deferred message was never delivered; recording an id the budget skipped " +
				"would lose the message outright")
		}
		if got := hookEventOf(t, next); got != hookEventUserPromptSubmit {
			t.Errorf("the paged delivery declared hookEventName %q, want %q", got, hookEventUserPromptSubmit)
		}
		paged := mailIDsIn(decodeAdditionalContext(t, next))
		if len(paged) == 0 {
			t.Fatalf("the next call's block names no message at all:\n%s", next)
		}
		for id := range paged {
			if served[id] {
				t.Errorf("the next call re-delivered %s, which SessionStart had already served; the "+
					"deferred message is still outstanding:\n%s", id, next)
			}
		}
	})
}

// TestMailCheckInject_HandoffSelfMailReachesSuccessor is AC-3 clause (iv) and the reason the
// delivered-state is keyed per session rather than globally: a successor session's context does not
// contain what its predecessor was shown, so mail the predecessor never acted on has to arrive
// again. It lives in the integration lane because the HANDOFF path's own producer cannot be
// exercised in process — sendHandoffMail returns early under isTestBinary() (handoff.go:144) — and
// because what is being proven is that the SHIPPED hook entry delivers it.
//
//	Scenario: An unacted HANDOFF self-mail reaches the successor whole
//	  Given a self-addressed message carrying the HANDOFF: subject prefix
//	  And a predecessor session that was shown it and did not `af mail delete` it
//	  When the provisioned SessionStart mail entry runs under a fresh successor session_id
//	  Then the successor receives the still-open body whole and untruncated
//	  And the predecessor's own session still sees nothing new
func TestMailCheckInject_HandoffSelfMailReachesSuccessor(t *testing.T) {
	requirePython3WithServerDeps(t)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const role = "supervisor"
	fx := newSessionStartFactory(ctx, t, []string{"manager", role})
	agentDir := filepath.Join(fx.root, ".agentfactory", "agents", role)

	// A HANDOFF self-mail is exactly this: sender == recipient, subject prefixed HANDOFF:
	// (handoff.go:38, done.go:577). There is no label and no store-level marker. It is seeded
	// through `af mail send` because the handoff verb itself is a no-op inside a test binary.
	const handoffSubject = "HANDOFF: Session cycling"
	const handoffBody = "Context cycling. Run af prime for current step."
	runAF(t, fx.afBin, agentDir, "mail", "send", role, "-s", handoffSubject, "-m", handoffBody)
	id := parseFirstMailID(t, runAF(t, fx.afBin, agentDir, "mail", "inbox"))

	settingsPath := filepath.Join(agentDir, ".claude", "settings.json")
	var mailEntry string
	for _, command := range provisionedHookCommands(t, settingsPath, hookEventSessionStart) {
		if sessionStartWriterOf(command) == "mail" {
			mailEntry = command
		}
	}
	if mailEntry == "" {
		t.Fatalf("%s provisions no SessionStart mail entry", role)
	}
	transcript := filepath.Join(fx.base, "transcripts", role+".jsonl")

	predecessor := decodeAdditionalContext(t, runSessionStartHook(ctx, t, fx, agentDir, mailEntry,
		sessionStartPayload("sess-predecessor", transcript, hookEventSessionStart)))
	if !strings.Contains(predecessor, handoffSubject) {
		t.Fatalf("the predecessor session was never shown the handoff mail:\n%s", predecessor)
	}

	successor := decodeAdditionalContext(t, runSessionStartHook(ctx, t, fx, agentDir, mailEntry,
		sessionStartPayload("sess-successor", transcript, hookEventSessionStart)))
	for _, want := range []string{"[" + id + "]", "From: " + role, "Subject: " + handoffSubject, handoffBody} {
		if !strings.Contains(successor, want) {
			t.Errorf("the successor session's mail block is missing %q:\n%s", want, successor)
		}
	}
	if strings.Contains(successor, "truncated") {
		t.Errorf("the handoff body arrived truncated, so \"delivered whole\" is not what this measured:\n%s", successor)
	}

	// Per successor, never a global reset: the predecessor's own session has already had it.
	if again := runSessionStartHook(ctx, t, fx, agentDir, mailEntry,
		sessionStartPayload("sess-predecessor", transcript, hookEventSessionStart)); again != "" {
		t.Errorf("the predecessor session re-received %d bytes after the successor ran:\n%s", len(again), again)
	}
}
