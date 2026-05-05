package cmd

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/formula"
	"github.com/stempeck/agentfactory/internal/issuestore/mcpstore"
)

func setupFactoryDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(configDir, "agents"), 0755); err != nil {
		t.Fatal(err)
	}

	configs := map[string]string{
		"factory.json":   `{"type":"factory","version":1,"name":"agentfactory"}`,
		"agents.json":    `{"agents":{"manager":{"type":"interactive","description":"Interactive agent"},"supervisor":{"type":"autonomous","description":"Autonomous agent"}}}`,
		"messaging.json": `{"groups":{"all":["manager","supervisor"]}}`,
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	return dir
}

func runInstallInDir(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	origDir, _ := os.Getwd()
	defer os.Chdir(origDir)
	os.Chdir(dir)

	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&out)
	rootCmd.SetArgs(append([]string{"install"}, args...))

	err := rootCmd.Execute()
	return out.String(), err
}

func TestInstallRole_CreatesWorkspace(t *testing.T) {
	dir := setupFactoryDir(t)

	output, err := runInstallInDir(t, dir, "manager")
	if err != nil {
		t.Fatalf("install manager failed: %v\nOutput: %s", err, output)
	}

	roleDir := filepath.Join(dir, ".agentfactory", "agents", "manager")
	if _, err := os.Stat(roleDir); err != nil {
		t.Fatalf("manager directory not created: %v", err)
	}

	claudePath := filepath.Join(roleDir, "CLAUDE.md")
	if _, err := os.Stat(claudePath); err != nil {
		t.Fatalf("CLAUDE.md not created: %v", err)
	}

	settingsPath := filepath.Join(roleDir, ".claude", "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
}

func TestInstallRole_InteractiveSettings(t *testing.T) {
	dir := setupFactoryDir(t)

	if _, err := runInstallInDir(t, dir, "manager"); err != nil {
		t.Fatalf("install manager failed: %v", err)
	}

	settingsPath := filepath.Join(dir, ".agentfactory", "agents", "manager", ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing settings.json: %v", err)
	}

	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})
	entry := sessionStart[0].(map[string]interface{})
	hooksList := entry["hooks"].([]interface{})
	hook := hooksList[0].(map[string]interface{})
	cmd := hook["command"].(string)

	if cmd == "" {
		t.Fatal("SessionStart hook command is empty")
	}
	if strings.Contains(cmd, "af mail check") {
		t.Error("interactive SessionStart should NOT contain 'af mail check'")
	}
	if !strings.Contains(cmd, "af prime --hook") {
		t.Error("interactive SessionStart should contain 'af prime --hook'")
	}
}

func TestInstallRole_AutonomousSettings(t *testing.T) {
	dir := setupFactoryDir(t)

	if _, err := runInstallInDir(t, dir, "supervisor"); err != nil {
		t.Fatalf("install supervisor failed: %v", err)
	}

	settingsPath := filepath.Join(dir, ".agentfactory", "agents", "supervisor", ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	var settings map[string]interface{}
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("parsing settings.json: %v", err)
	}

	hooks := settings["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})
	entry := sessionStart[0].(map[string]interface{})
	hooksList := entry["hooks"].([]interface{})
	hook := hooksList[0].(map[string]interface{})
	cmd := hook["command"].(string)

	if !strings.Contains(cmd, "af prime --hook && af mail check --inject") {
		t.Errorf("autonomous SessionStart should contain 'af prime --hook && af mail check --inject', got: %s", cmd)
	}
}

func TestInstallRole_UnknownRole(t *testing.T) {
	dir := setupFactoryDir(t)

	_, err := runInstallInDir(t, dir, "nonexistent")
	if err == nil {
		t.Fatal("expected error for unknown role, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error should contain 'not found', got: %s", err.Error())
	}
}

func TestInstallRole_Idempotent(t *testing.T) {
	dir := setupFactoryDir(t)

	if _, err := runInstallInDir(t, dir, "manager"); err != nil {
		t.Fatalf("first install failed: %v", err)
	}

	if _, err := runInstallInDir(t, dir, "manager"); err != nil {
		t.Fatalf("second install failed: %v", err)
	}
}

func TestInstallRole_StopHookHasQualityGate(t *testing.T) {
	dir := setupFactoryDir(t)

	if _, err := runInstallInDir(t, dir, "manager"); err != nil {
		t.Fatalf("install manager failed: %v", err)
	}

	settingsPath := filepath.Join(dir, ".agentfactory", "agents", "manager", ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	if !strings.Contains(string(data), "quality-gate.sh") {
		t.Error("settings.json Stop hook should reference quality-gate.sh")
	}
}

// TestNoDirectBdInFormulas verifies that no formula TOML file instructs agents
// to call bd directly. All bd operations should use af commands.
func TestNoDirectBdInFormulas(t *testing.T) {
	// Catch-all: any "bd <word>" pattern, not a deny-list of known operations.
	// This catches bd show, bd create, bd agent state, bd anything-new.
	bdPattern := regexp.MustCompile(`\bbd\s+[a-z]`)

	entries, err := formulasFS.ReadDir("install_formulas")
	if err != nil {
		t.Fatalf("reading install_formulas: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := formulasFS.ReadFile("install_formulas/" + e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		lines := strings.Split(string(data), "\n")
		for i, line := range lines {
			trimmed := strings.TrimSpace(line)
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			if bdPattern.MatchString(trimmed) {
				t.Errorf("%s:%d: direct bd command found: %s", e.Name(), i+1, trimmed)
			}
		}
	}
}

func TestCheckPythonMCPDeps(t *testing.T) {
	t.Cleanup(func() {
		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot("")
	})

	t.Run("resolve_fails", func(t *testing.T) {
		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot("")
		dir := t.TempDir()

		var buf bytes.Buffer
		err := checkPythonMCPDeps(dir, &buf)
		if err == nil {
			t.Fatal("expected error when py/ not found, got nil")
		}
		if !strings.Contains(err.Error(), "py/ package not found") {
			t.Errorf("error should mention 'py/ package not found', got: %v", err)
		}
		if !strings.Contains(err.Error(), "AF_SOURCE_ROOT") {
			t.Errorf("error should mention AF_SOURCE_ROOT remediation, got: %v", err)
		}
	})

	t.Run("module_import_fails", func(t *testing.T) {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("python3 not available")
		}

		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot("")

		dir := t.TempDir()
		pyDir := filepath.Join(dir, "py")
		if err := os.MkdirAll(pyDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pyDir, "__init__.py"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		var buf bytes.Buffer
		err := checkPythonMCPDeps(dir, &buf)
		if err == nil {
			t.Fatal("expected error when py.issuestore.server not importable, got nil")
		}
		if !strings.Contains(err.Error(), "py.issuestore.server is not importable") {
			t.Errorf("error should mention 'py.issuestore.server is not importable', got: %v", err)
		}
	})

	t.Run("pip_deps_missing", func(t *testing.T) {
		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot("")

		binDir := t.TempDir()
		mockScript := filepath.Join(binDir, "python3")
		script := "#!/bin/sh\nif echo \"$2\" | grep -q aiohttp; then\n  echo \"ModuleNotFoundError: No module named 'aiohttp'\" >&2\n  exit 1\nfi\nexit 0\n"
		if err := os.WriteFile(mockScript, []byte(script), 0o755); err != nil {
			t.Fatal(err)
		}

		dir := t.TempDir()
		pyDir := filepath.Join(dir, "py")
		if err := os.MkdirAll(filepath.Join(pyDir, "issuestore"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pyDir, "__init__.py"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pyDir, "issuestore", "__init__.py"), nil, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(pyDir, "issuestore", "server.py"), nil, 0o644); err != nil {
			t.Fatal(err)
		}

		origPath := os.Getenv("PATH")
		t.Setenv("PATH", binDir+":"+origPath)

		var buf bytes.Buffer
		err := checkPythonMCPDeps(dir, &buf)
		if err == nil {
			t.Fatal("expected error when pip deps missing, got nil")
		}
		if !strings.Contains(err.Error(), "Missing Python dependencies") {
			t.Errorf("error should mention 'Missing Python dependencies', got: %v", err)
		}
		if !strings.Contains(err.Error(), "pip install -r py/requirements.txt") {
			t.Errorf("error should mention 'pip install -r py/requirements.txt', got: %v", err)
		}
	})

	t.Run("happy_path", func(t *testing.T) {
		if _, err := exec.LookPath("python3"); err != nil {
			t.Skip("python3 not available")
		}
		if _, err := exec.Command("python3", "-c", "import aiohttp, sqlalchemy").CombinedOutput(); err != nil {
			t.Skip("python3 missing server deps (aiohttp/sqlalchemy)")
		}

		mcpstore.SetSourceRoot("")
		mcpstore.SetEnvSourceRoot("")

		dir := t.TempDir()
		repoRoot := findRepoRoot(t)
		target := filepath.Join(repoRoot, "py")
		link := filepath.Join(dir, "py")
		if err := os.Symlink(target, link); err != nil {
			t.Fatalf("symlink py/: %v", err)
		}

		var buf bytes.Buffer
		err := checkPythonMCPDeps(dir, &buf)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.Contains(buf.String(), "Python MCP dependencies verified") {
			t.Errorf("output should contain 'Python MCP dependencies verified', got: %q", buf.String())
		}
	})
}

// TestAllEmbeddedFormulasParse verifies that every embedded formula TOML file
// parses without error. Catches TOML syntax errors from mechanical replacements.
func TestAllEmbeddedFormulasParse(t *testing.T) {
	entries, err := formulasFS.ReadDir("install_formulas")
	if err != nil {
		t.Fatalf("reading install_formulas: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".formula.toml") {
			continue
		}
		data, err := formulasFS.ReadFile("install_formulas/" + e.Name())
		if err != nil {
			t.Fatalf("reading %s: %v", e.Name(), err)
		}
		if _, err := formula.Parse(data); err != nil {
			t.Errorf("%s: parse error: %v", e.Name(), err)
		}
	}
}

// TestInstallInitFormulas_SkipWhenEqual calls writeFormulas directly instead of
// runInstallInDir(t, dir, "--init") because runInstallInit requires Python 3.12
// and the MCP server (checkPython312, mcpstore.New), making it unsuitable for
// the unit test tier. The integration path is covered by
// TestInstallInit_CreatesDispatchJson in install_integration_test.go.
func TestInstallInitFormulas_SkipWhenEqual(t *testing.T) {
	formulasDir := filepath.Join(t.TempDir(), ".beads", "formulas")
	if err := os.MkdirAll(formulasDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := writeFormulas(formulasDir); err != nil {
		t.Fatalf("first writeFormulas: %v", err)
	}

	entries, err := formulasFS.ReadDir("install_formulas")
	if err != nil {
		t.Fatal(err)
	}
	var sampleName string
	for _, e := range entries {
		if !e.IsDir() {
			sampleName = e.Name()
			break
		}
	}
	if sampleName == "" {
		t.Fatal("no formula files found in embedded FS")
	}

	samplePath := filepath.Join(formulasDir, sampleName)
	info1, err := os.Stat(samplePath)
	if err != nil {
		t.Fatalf("stat after first write: %v", err)
	}
	mtime1 := info1.ModTime()

	time.Sleep(50 * time.Millisecond)

	if err := writeFormulas(formulasDir); err != nil {
		t.Fatalf("second writeFormulas: %v", err)
	}

	info2, err := os.Stat(samplePath)
	if err != nil {
		t.Fatalf("stat after second write: %v", err)
	}
	if !info2.ModTime().Equal(mtime1) {
		t.Errorf("formula %s was rewritten despite identical content: mtime changed from %v to %v",
			sampleName, mtime1, info2.ModTime())
	}

	if err := os.WriteFile(samplePath, []byte("mutated content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeFormulas(formulasDir); err != nil {
		t.Fatalf("third writeFormulas (after mutation): %v", err)
	}

	got, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := formulasFS.ReadFile(filepath.Join("install_formulas", sampleName))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, embedded) {
		t.Errorf("mutated formula %s was not restored to embedded content", sampleName)
	}
}
