//go:build integration

package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/issuestore"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

func findRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getting cwd: %v", err)
	}
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
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	if out, err := exec.Command("python3", "-c", "import aiohttp, sqlalchemy").CombinedOutput(); err != nil {
		t.Skipf("python3 missing server deps (aiohttp/sqlalchemy): %s", out)
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
	binary := filepath.Join(t.TempDir(), "af")
	cmd := exec.Command("go", "build", "-o", binary, "./cmd/af")
	cmd.Dir = findRepoRoot(t)
	cmd.Env = append(os.Environ(), "CGO_ENABLED=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("building af: %s\n%s", err, out)
	}
	return binary
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

	// git init — needed for worktree and for the .beads subdirectory to live
	// inside a repo. The MCP server does not require git.
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
	formulaDir := filepath.Join(workspace, ".beads", "formulas")
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
		".beads",
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
	qgPath := filepath.Join(workspace, "hooks", "quality-gate.sh")
	info, err := os.Stat(qgPath)
	if err != nil {
		t.Fatalf("quality-gate.sh should exist: %v", err)
	}
	if info.Mode()&0111 == 0 {
		t.Fatal("quality-gate.sh should be executable")
	}

	// 10. Verify fidelity gate hook
	fgPath := filepath.Join(workspace, "hooks", "fidelity-gate.sh")
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
// a tabwriter table with a header row "ID  FROM  SUBJECT  PRIORITY  TIME".
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
