package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
	"github.com/stempeck/agentfactory/internal/issuestore"
)

func setupTestFactoryForPrime(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(configDir, "agents"), 0o755); err != nil {
		t.Fatal(err)
	}

	factory := map[string]interface{}{
		"type":    "factory",
		"version": 1,
		"name":    "test-factory",
	}
	agents := map[string]interface{}{
		"agents": map[string]interface{}{
			"manager": map[string]string{
				"type":        "interactive",
				"description": "Factory coordinator",
			},
			"supervisor": map[string]string{
				"type":        "autonomous",
				"description": "Autonomous worker",
			},
		},
	}

	writeTestJSON(t, filepath.Join(configDir, "factory.json"), factory)
	writeTestJSON(t, filepath.Join(configDir, "agents.json"), agents)

	// Create agent directories
	os.MkdirAll(filepath.Join(dir, ".agentfactory", "agents", "manager"), 0o755)
	os.MkdirAll(filepath.Join(dir, ".agentfactory", "agents", "supervisor"), 0o755)

	return dir
}

func writeTestJSON(t *testing.T, path string, v interface{}) {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDetectRole_ValidAgent(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	cwd := filepath.Join(root, ".agentfactory", "agents", "manager")

	role, entry, err := detectRole(cwd, root)
	if err != nil {
		t.Fatalf("detectRole failed: %v", err)
	}
	if role != "manager" {
		t.Errorf("expected role manager, got %s", role)
	}
	if entry.Type != "interactive" {
		t.Errorf("expected type interactive, got %s", entry.Type)
	}
	if entry.Description != "Factory coordinator" {
		t.Errorf("expected description 'Factory coordinator', got %s", entry.Description)
	}
}

func TestDetectRole_FactoryRoot(t *testing.T) {
	t.Setenv("AF_ROLE", "")
	root := setupTestFactoryForPrime(t)

	_, _, err := detectRole(root, root)
	if err == nil {
		t.Fatal("detectRole should return error when cwd is factory root")
	}
}

func TestDetectRole_UnknownAgent(t *testing.T) {
	t.Setenv("AF_ROLE", "")
	root := setupTestFactoryForPrime(t)
	cwd := filepath.Join(root, ".agentfactory", "agents", "nonexistent")
	os.MkdirAll(cwd, 0o755)

	_, _, err := detectRole(cwd, root)
	if err == nil {
		t.Fatal("detectRole should return error for unknown agent")
	}
}

func TestDetectRole_WrongButNoError_HonorsAF_ROLE(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	cwd := filepath.Join(root, ".agentfactory", "agents", "typo")
	os.MkdirAll(cwd, 0o755)

	t.Setenv("AF_ROLE", "manager")

	role, entry, err := detectRole(cwd, root)
	if err != nil {
		t.Fatalf("detectRole with AF_ROLE fallback: %v", err)
	}
	if role != "manager" {
		t.Errorf("role = %q, want %q (AF_ROLE must override wrong path-derived name)", role, "manager")
	}
	if entry == nil || entry.Type != "interactive" {
		t.Errorf("entry = %+v, want manager interactive entry", entry)
	}
}

func TestDetectRole_NestedDir(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	cwd := filepath.Join(root, ".agentfactory", "agents", "manager", "subdir", "deep")
	os.MkdirAll(cwd, 0o755)

	role, _, err := detectRole(cwd, root)
	if err != nil {
		t.Fatalf("detectRole should work from nested dir: %v", err)
	}
	if role != "manager" {
		t.Errorf("expected manager, got %s", role)
	}
}

func TestReadHookSessionID_ValidJSON(t *testing.T) {
	input := strings.NewReader(`{"session_id":"abc-123","source":"startup"}`)
	id := readHookSessionID(input)
	if id != "abc-123" {
		t.Errorf("expected abc-123, got %s", id)
	}
}

func TestReadHookSessionID_EmptyInput(t *testing.T) {
	input := strings.NewReader("")
	id := readHookSessionID(input)
	if id != "" {
		t.Errorf("expected empty string, got %s", id)
	}
}

func TestReadHookSessionID_InvalidJSON(t *testing.T) {
	input := strings.NewReader("not json at all")
	id := readHookSessionID(input)
	if id != "" {
		t.Errorf("expected empty string for invalid JSON, got %s", id)
	}
}

func TestPersistSessionID(t *testing.T) {
	dir := t.TempDir()
	persistSessionID(dir, "test-session-42")

	path := filepath.Join(dir, ".runtime", "session_id")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("session_id file not created: %v", err)
	}
	if string(data) != "test-session-42" {
		t.Errorf("expected test-session-42, got %s", string(data))
	}
}

func TestGetSessionID_Exists(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "session_id"), []byte("persisted-id"), 0o644)

	id := getSessionID(dir, "manager")
	if id != "persisted-id" {
		t.Errorf("expected persisted-id, got %s", id)
	}
}

func TestGetSessionID_Missing(t *testing.T) {
	dir := t.TempDir()
	id := getSessionID(dir, "manager")
	if id == "" {
		t.Fatal("getSessionID should return fallback, not empty")
	}
	if !strings.HasPrefix(id, "manager-") {
		t.Errorf("fallback should start with 'manager-', got %s", id)
	}
}

func TestOutputStartupDirective_Interactive(t *testing.T) {
	var buf strings.Builder
	outputStartupDirective(&buf, "interactive")
	output := buf.String()
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "check mail") && !strings.Contains(lower, "mail") {
		t.Error("interactive directive should mention mail")
	}
}

func TestOutputStartupDirective_Autonomous(t *testing.T) {
	var buf strings.Builder
	outputStartupDirective(&buf, "autonomous")
	output := buf.String()
	lower := strings.ToLower(output)
	if !strings.Contains(lower, "autonomous") && !strings.Contains(lower, "hooked") {
		t.Error("autonomous directive should mention autonomous or hooked work")
	}
}

func TestOutputStartupDirective_NoCustomDirective(t *testing.T) {
	// Custom directives are now delivered via the startup nudge (session.Manager.Start),
	// not via outputStartupDirective. Verify the output contains only the standard steps.
	var buf strings.Builder
	outputStartupDirective(&buf, "interactive")
	output := buf.String()
	if strings.Contains(output, "Read memory") {
		t.Error("outputStartupDirective should not contain custom directives")
	}
	if !strings.Contains(output, "Startup Directive") {
		t.Error("output should contain the Startup Directive header")
	}
}

func TestPrimeAgent_SingleAgent(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	var buf strings.Builder

	agentDir := filepath.Join(root, ".agentfactory", "agents", "manager")
	err := primeAgent(t.Context(), &buf, root, "manager", agentDir)
	if err != nil {
		t.Fatalf("primeAgent failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "role:manager") {
		t.Error("output should contain role:manager")
	}
	if !strings.Contains(output, "Startup Directive") {
		t.Error("output should contain Startup Directive")
	}
}

func TestPrimeAgent_UnknownAgent(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	var buf strings.Builder

	agentDir := filepath.Join(root, ".agentfactory", "agents", "nonexistent")
	err := primeAgent(t.Context(), &buf, root, "nonexistent", agentDir)
	if err == nil {
		t.Fatal("primeAgent should fail for unknown agent")
	}
}

func TestRunPrimeAll_AllProvisioned(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	var buf strings.Builder

	err := runPrimeAll(t.Context(), &buf, root)
	if err != nil {
		t.Fatalf("runPrimeAll failed: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "role:manager") {
		t.Error("output should contain role:manager")
	}
	if !strings.Contains(output, "role:supervisor") {
		t.Error("output should contain role:supervisor")
	}
}

func TestRunPrimeAll_SkipsUnprovisioned(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	// Remove supervisor directory to simulate unprovisioned agent
	os.RemoveAll(filepath.Join(root, ".agentfactory", "agents", "supervisor"))

	var buf strings.Builder
	err := runPrimeAll(t.Context(), &buf, root)
	if err != nil {
		t.Fatalf("runPrimeAll should succeed with partial provisioning: %v", err)
	}

	output := buf.String()
	if !strings.Contains(output, "role:manager") {
		t.Error("output should contain role:manager")
	}
	if strings.Contains(output, "role:supervisor") {
		t.Error("output should NOT contain role:supervisor (not provisioned)")
	}
}

func TestRunPrimeAll_ZeroProvisioned(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	// Remove both agent directories
	os.RemoveAll(filepath.Join(root, ".agentfactory", "agents", "manager"))
	os.RemoveAll(filepath.Join(root, ".agentfactory", "agents", "supervisor"))

	var buf strings.Builder
	err := runPrimeAll(t.Context(), &buf, root)
	if err == nil {
		t.Fatal("runPrimeAll should fail when no agents are provisioned")
	}
	if !strings.Contains(err.Error(), "no provisioned agents") {
		t.Errorf("error should mention 'no provisioned agents', got: %v", err)
	}
}

func TestRunPrimeAll_IncludesFormulaContextForActiveFormulas(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	// Give manager a hooked formula; supervisor has none
	runtimeDir := filepath.Join(root, ".agentfactory", "agents", "manager", ".runtime")
	os.MkdirAll(runtimeDir, 0o755)
	os.WriteFile(filepath.Join(runtimeDir, "hooked_formula"), []byte("bd-mgr-formula"), 0o644)
	os.MkdirAll(config.StoreDir(root), 0o755)

	var buf strings.Builder
	err := runPrimeAll(t.Context(), &buf, root)
	if err != nil {
		t.Fatalf("runPrimeAll failed: %v", err)
	}

	output := buf.String()
	// Both agents should be primed
	if !strings.Contains(output, "role:manager") {
		t.Error("output should contain role:manager")
	}
	if !strings.Contains(output, "role:supervisor") {
		t.Error("output should contain role:supervisor")
	}
	// Formula context should appear for manager (who has hooked_formula)
	if !strings.Contains(output, "Formula Workflow") {
		t.Error("output should contain Formula Workflow for agent with active formula")
	}
	if !strings.Contains(output, "bd-mgr-formula") {
		t.Error("output should contain the manager's formula instance ID")
	}
	// Formula Workflow should appear exactly once (only manager has a formula)
	if count := strings.Count(output, "## Formula Workflow"); count != 1 {
		t.Errorf("expected Formula Workflow exactly once, got %d", count)
	}
}

// --- Fork bomb guard tests (issue #5 fix, commit 392717f) ---

// Scenario: isTestBinary detects Go test binary under go test
func TestIsTestBinary_DetectsGoTestBinary(t *testing.T) {
	// When running under go test, os.Executable() returns a binary ending in .test
	got := isTestBinary()
	if !got {
		t.Error("isTestBinary() should return true when running under go test")
	}
}

// Scenario: isTestBinary allows real af binary
func TestIsTestBinary_AllowsRealBinary(t *testing.T) {
	// The suffix check is on filepath.Base(os.Executable()). We can't change
	// os.Executable() in a test, but we can verify the suffix logic directly.
	// These are the paths that should NOT be detected as test binaries:
	testCases := []struct {
		name string
		ok   bool // true = ends with .test
	}{
		{"cmd.test", true},
		{"internal.test", true},
		{"/tmp/go-build123/cmd.test", true},
		{"af", false},
		{"/usr/local/bin/af", false},
		{"af-factory", false},
		{"test", false},      // "test" without dot prefix is not a Go test binary
		{"my.testing", false}, // not .test suffix
	}
	for _, tc := range testCases {
		got := strings.HasSuffix(filepath.Base(tc.name), ".test")
		if got != tc.ok {
			t.Errorf("suffix check for %q: got %v, want %v", tc.name, got, tc.ok)
		}
	}
}

// Scenario: runMailCheckInject is no-op under go test
func TestRunMailCheckInject_NoOpUnderGoTest(t *testing.T) {
	var buf strings.Builder
	// Under go test, isTestBinary() returns true, so this should be a no-op
	runMailCheckInject(&buf)
	if buf.Len() != 0 {
		t.Errorf("runMailCheckInject() should produce no output under go test, got %d bytes", buf.Len())
	}
}

// Scenario: sendWorkDoneMail is no-op under go test
func TestSendWorkDoneMail_NoOpUnderGoTest(t *testing.T) {
	// Under go test, isTestBinary() returns true, so this should return nil immediately
	err := sendWorkDoneMail("witness", "test-instance", "test-formula", 3)
	if err != nil {
		t.Errorf("sendWorkDoneMail() should return nil under go test, got: %v", err)
	}
}

func TestPrime_DiscriminatesBlockedFromAllComplete(t *testing.T) {
	root := setupTestFactoryForPrime(t)
	os.MkdirAll(config.StoreDir(root), 0o755)
	mem := installMemStore(t)

	ctx := t.Context()
	agentName := "manager"
	workDir := filepath.Join(root, ".agentfactory", "agents", agentName)

	epic, err := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeEpic,
		Title:    "Test Formula",
		Assignee: agentName,
	})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}

	child, err := mem.Create(ctx, issuestore.CreateParams{
		Type:     issuestore.TypeTask,
		Parent:   epic.ID,
		Title:    "Blocked step",
		Assignee: agentName,
	})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	blocker, err := mem.Create(ctx, issuestore.CreateParams{
		Type:  issuestore.TypeTask,
		Title: "External blocker",
	})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := mem.DepAdd(ctx, child.ID, blocker.ID); err != nil {
		t.Fatalf("DepAdd: %v", err)
	}

	writeRuntimeFile(t, workDir, "hooked_formula", epic.ID)

	var buf strings.Builder
	outputFormulaContext(ctx, &buf, workDir)
	output := buf.String()

	if !strings.Contains(output, "blocked") {
		t.Errorf("expected output to contain 'blocked' when all steps are blocked, got:\n%s", output)
	}
	if strings.Contains(output, "all_complete") {
		t.Errorf("output should NOT contain 'all_complete' when open children exist, got:\n%s", output)
	}
}
