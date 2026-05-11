package cmd

import (
	"bytes"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
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
	formulasDir := config.FormulasDir(t.TempDir())
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

func TestWriteAgentsMd_CreatesNewFile(t *testing.T) {
	dir := setupFactoryDir(t)

	if err := writeAgentsMd(dir); err != nil {
		t.Fatalf("writeAgentsMd: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if err != nil {
		t.Fatalf("reading AGENTS.md: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "## BEGIN AgentFactory Agents") {
		t.Error("missing begin marker")
	}
	if !strings.Contains(content, "## END AgentFactory Agents") {
		t.Error("missing end marker")
	}
	if !strings.Contains(content, "| `manager` |") {
		t.Error("missing manager agent row")
	}
	if !strings.Contains(content, "| `supervisor` |") {
		t.Error("missing supervisor agent row")
	}
	if !strings.Contains(content, "af sling --agent") {
		t.Error("missing dispatch syntax")
	}
}

func TestWriteAgentsMd_BlockReplace(t *testing.T) {
	dir := setupFactoryDir(t)

	prelude := "# My Project Notes\n\nSome existing content.\n\n"
	agentsMdPath := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(agentsMdPath, []byte(prelude), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeAgentsMd(dir); err != nil {
		t.Fatalf("first writeAgentsMd: %v", err)
	}

	data, err := os.ReadFile(agentsMdPath)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	if !strings.HasPrefix(content, prelude) {
		t.Error("existing content was not preserved")
	}
	if strings.Count(content, agentsMdBegin) != 1 {
		t.Error("expected exactly one begin marker")
	}

	// Modify agents.json — add an agent, re-run, verify block updated
	agentsCfg := filepath.Join(dir, ".agentfactory", "agents.json")
	if err := os.WriteFile(agentsCfg, []byte(`{"agents":{"manager":{"type":"interactive","description":"Interactive agent"},"supervisor":{"type":"autonomous","description":"Autonomous agent"},"worker":{"type":"autonomous","description":"Worker agent"}}}`), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeAgentsMd(dir); err != nil {
		t.Fatalf("second writeAgentsMd: %v", err)
	}

	data, err = os.ReadFile(agentsMdPath)
	if err != nil {
		t.Fatal(err)
	}
	content = string(data)

	if !strings.HasPrefix(content, prelude) {
		t.Error("existing content lost after block replace")
	}
	if !strings.Contains(content, "| `worker` |") {
		t.Error("new worker agent not in updated block")
	}
	if strings.Count(content, agentsMdBegin) != 1 {
		t.Error("duplicate begin markers after replace")
	}
}

func TestAgentDescriptionLine(t *testing.T) {
	tests := []struct {
		name string
		desc string
		want string
	}{
		{
			name: "simple",
			desc: "Interactive agent for human-supervised work",
			want: "Interactive agent for human-supervised work",
		},
		{
			name: "strips_overview_joins_lines",
			desc: "## Overview\nStructured design exploration via\nparallel specialized analysts.",
			want: "Structured design exploration via parallel specialized analysts.",
		},
		{
			name: "truncates_at_128",
			desc: strings.Repeat("x", 150),
			want: strings.Repeat("x", 125) + "...",
		},
		{
			name: "skips_blank_lines_joins_remainder",
			desc: "\n\n## Overview\n\nFirst line.\nSecond line.",
			want: "First line. Second line.",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := agentDescriptionLine(tt.desc)
			if got != tt.want {
				t.Errorf("agentDescriptionLine(%q) = %q, want %q", tt.desc, got, tt.want)
			}
		})
	}
}

func TestInstallRoleFallbackWarning(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stderr = w

	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(filepath.Join(configDir, "agents"), 0755); err != nil {
		t.Fatal(err)
	}
	configs := map[string]string{
		"factory.json":   `{"type":"factory","version":1,"name":"test"}`,
		"agents.json":    `{"agents":{"phantom-agent":{"type":"autonomous","description":"Formula agent without template","formula":"phantom-formula"}}}`,
		"messaging.json": `{"groups":{}}`,
	}
	for name, content := range configs {
		if err := os.WriteFile(filepath.Join(configDir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	_, installErr := runInstallInDir(t, dir, "phantom-agent")

	w.Close()
	var stderrBuf bytes.Buffer
	stderrBuf.ReadFrom(r)
	os.Stderr = origStderr

	if installErr != nil {
		t.Fatalf("install should succeed with fallback: %v", installErr)
	}
	if !strings.Contains(stderrBuf.String(), "WARNING") {
		t.Errorf("expected WARNING on stderr for formula agent without embedded template, got stderr: %q", stderrBuf.String())
	}
}

func TestSkillsFS_ContainsAllFiles(t *testing.T) {
	expected := map[string]bool{
		"install_skills/documentation-update/SKILL.md":  false,
		"install_skills/formula-create/SKILL.md":        false,
		"install_skills/formula-create/skillmd-mode.md": false,
		"install_skills/github-issue/SKILL.md":          false,
		"install_skills/rapid-implement/SKILL.md":       false,
	}

	err := fs.WalkDir(skillsFS, "install_skills", func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if _, ok := expected[path]; ok {
			expected[path] = true
		} else {
			t.Errorf("unexpected file in skillsFS: %s", path)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walking skillsFS: %v", err)
	}

	for path, found := range expected {
		if !found {
			t.Errorf("expected file missing from skillsFS: %s", path)
		}
	}
}

func TestWriteSkills_SkipWhenEqual(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := writeSkills(skillsDir); err != nil {
		t.Fatalf("first writeSkills: %v", err)
	}

	samplePath := filepath.Join(skillsDir, "documentation-update", "SKILL.md")
	info1, err := os.Stat(samplePath)
	if err != nil {
		t.Fatalf("stat after first write: %v", err)
	}
	mtime1 := info1.ModTime()

	time.Sleep(50 * time.Millisecond)

	if err := writeSkills(skillsDir); err != nil {
		t.Fatalf("second writeSkills: %v", err)
	}

	info2, err := os.Stat(samplePath)
	if err != nil {
		t.Fatalf("stat after second write: %v", err)
	}
	if !info2.ModTime().Equal(mtime1) {
		t.Errorf("skill file was rewritten despite identical content: mtime changed from %v to %v",
			mtime1, info2.ModTime())
	}

	if err := os.WriteFile(samplePath, []byte("mutated content"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeSkills(skillsDir); err != nil {
		t.Fatalf("third writeSkills (after mutation): %v", err)
	}

	got, err := os.ReadFile(samplePath)
	if err != nil {
		t.Fatal(err)
	}
	embedded, err := skillsFS.ReadFile("install_skills/documentation-update/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, embedded) {
		t.Errorf("mutated skill was not restored to embedded content")
	}
}

func TestWriteSkills_PreservesCustomerSkills(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), ".claude", "skills")

	customerDir := filepath.Join(skillsDir, "my-custom-skill")
	if err := os.MkdirAll(customerDir, 0755); err != nil {
		t.Fatal(err)
	}
	customerFile := filepath.Join(customerDir, "SKILL.md")
	customerContent := []byte("# My Custom Skill\nCustomer-created content.\n")
	if err := os.WriteFile(customerFile, customerContent, 0644); err != nil {
		t.Fatal(err)
	}

	if err := writeSkills(skillsDir); err != nil {
		t.Fatalf("writeSkills: %v", err)
	}

	got, err := os.ReadFile(customerFile)
	if err != nil {
		t.Fatalf("customer skill deleted: %v", err)
	}
	if !bytes.Equal(got, customerContent) {
		t.Errorf("customer skill content modified: got %q, want %q", got, customerContent)
	}
}

func TestWriteSkills_RecursiveStructure(t *testing.T) {
	skillsDir := filepath.Join(t.TempDir(), ".claude", "skills")
	if err := os.MkdirAll(skillsDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := writeSkills(skillsDir); err != nil {
		t.Fatalf("writeSkills: %v", err)
	}

	expectedFiles := []string{
		filepath.Join(skillsDir, "formula-create", "SKILL.md"),
		filepath.Join(skillsDir, "formula-create", "skillmd-mode.md"),
		filepath.Join(skillsDir, "documentation-update", "SKILL.md"),
		filepath.Join(skillsDir, "github-issue", "SKILL.md"),
	}
	for _, path := range expectedFiles {
		if _, err := os.Stat(path); err != nil {
			t.Errorf("expected file missing after extraction: %s", path)
		}
	}

	fcDir := filepath.Join(skillsDir, "formula-create")
	entries, err := os.ReadDir(fcDir)
	if err != nil {
		t.Fatalf("reading formula-create dir: %v", err)
	}
	if len(entries) != 2 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Errorf("formula-create should have 2 files, got %d: %v", len(entries), names)
	}
}

func TestMigrateBeadsDir(t *testing.T) {
	t.Run("beads_exists_store_does_not", func(t *testing.T) {
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		os.MkdirAll(filepath.Join(beadsDir, "formulas"), 0755)
		os.WriteFile(filepath.Join(beadsDir, ".gitignore"), []byte("*.sqlite\n"), 0644)
		os.WriteFile(filepath.Join(beadsDir, "issues.sqlite"), []byte("fakedb"), 0644)
		os.WriteFile(filepath.Join(beadsDir, "formulas", "test.toml"), []byte("[formula]"), 0644)

		if err := migrateBeadsDir(root); err != nil {
			t.Fatalf("migrateBeadsDir: %v", err)
		}

		storeDir := filepath.Join(root, ".agentfactory", "store")
		if _, err := os.Stat(filepath.Join(storeDir, ".gitignore")); err != nil {
			t.Error(".gitignore not migrated")
		}
		if _, err := os.Stat(filepath.Join(storeDir, "issues.sqlite")); err != nil {
			t.Error("issues.sqlite not migrated")
		}
		if _, err := os.Stat(filepath.Join(storeDir, "formulas", "test.toml")); err != nil {
			t.Error("formulas/test.toml not migrated")
		}
		if _, err := os.Stat(filepath.Join(storeDir, ".migration-complete")); err != nil {
			t.Error(".migration-complete sentinel not written")
		}
		if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
			t.Error(".beads/ should have been deleted after migration")
		}
	})

	t.Run("both_exist_skip", func(t *testing.T) {
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		storeDir := filepath.Join(root, ".agentfactory", "store")
		os.MkdirAll(beadsDir, 0755)
		os.MkdirAll(storeDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "old.txt"), []byte("old"), 0644)

		if err := migrateBeadsDir(root); err != nil {
			t.Fatalf("migrateBeadsDir: %v", err)
		}

		if _, err := os.Stat(filepath.Join(storeDir, "old.txt")); !os.IsNotExist(err) {
			t.Error("file should not have been copied when both dirs exist")
		}
		if _, err := os.Stat(beadsDir); err != nil {
			t.Error(".beads/ should still exist when both dirs exist")
		}
	})

	t.Run("neither_exists_noop", func(t *testing.T) {
		root := t.TempDir()
		if err := migrateBeadsDir(root); err != nil {
			t.Fatalf("migrateBeadsDir: %v", err)
		}
	})

	t.Run("partial_migration_cleanup", func(t *testing.T) {
		root := t.TempDir()
		beadsDir := filepath.Join(root, ".beads")
		storeDir := filepath.Join(root, ".agentfactory", "store")
		os.MkdirAll(beadsDir, 0755)
		os.MkdirAll(storeDir, 0755)
		os.WriteFile(filepath.Join(beadsDir, "leftover.txt"), []byte("old"), 0644)
		os.WriteFile(filepath.Join(storeDir, ".migration-complete"), []byte("done"), 0644)

		if err := migrateBeadsDir(root); err != nil {
			t.Fatalf("migrateBeadsDir: %v", err)
		}

		if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
			t.Error(".beads/ should have been deleted (sentinel exists)")
		}
	})
}

func TestMigrateBeadsDir_Idempotent(t *testing.T) {
	root := t.TempDir()
	beadsDir := filepath.Join(root, ".beads")
	os.MkdirAll(beadsDir, 0755)
	os.WriteFile(filepath.Join(beadsDir, "test.txt"), []byte("data"), 0644)

	if err := migrateBeadsDir(root); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := migrateBeadsDir(root); err != nil {
		t.Fatalf("second migration: %v", err)
	}

	storeDir := filepath.Join(root, ".agentfactory", "store")
	data, err := os.ReadFile(filepath.Join(storeDir, "test.txt"))
	if err != nil {
		t.Fatal("test.txt not in store after idempotent migration")
	}
	if string(data) != "data" {
		t.Errorf("test.txt content = %q, want %q", string(data), "data")
	}
	if _, err := os.Stat(beadsDir); !os.IsNotExist(err) {
		t.Error(".beads/ should not exist after migration")
	}
}

func TestEnsureGitExclude_CreatesEntries(t *testing.T) {
	root := t.TempDir()
	gitInfoDir := filepath.Join(root, ".git", "info")
	if err := os.MkdirAll(gitInfoDir, 0755); err != nil {
		t.Fatal(err)
	}
	excludePath := filepath.Join(gitInfoDir, "exclude")
	if err := os.WriteFile(excludePath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	if err := ensureGitExclude(root); err != nil {
		t.Fatalf("ensureGitExclude: %v", err)
	}

	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("reading exclude: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "# agentfactory managed paths") {
		t.Error("missing sentinel comment")
	}
	for _, pattern := range []string{".agentfactory/", ".runtime/", "AGENTS.md", ".claude/"} {
		if !strings.Contains(content, pattern) {
			t.Errorf("missing pattern: %s", pattern)
		}
	}
}

func TestEnsureGitExclude_Idempotent(t *testing.T) {
	root := t.TempDir()
	gitInfoDir := filepath.Join(root, ".git", "info")
	if err := os.MkdirAll(gitInfoDir, 0755); err != nil {
		t.Fatal(err)
	}
	excludePath := filepath.Join(gitInfoDir, "exclude")
	if err := os.WriteFile(excludePath, nil, 0644); err != nil {
		t.Fatal(err)
	}

	if err := ensureGitExclude(root); err != nil {
		t.Fatalf("first ensureGitExclude: %v", err)
	}
	if err := ensureGitExclude(root); err != nil {
		t.Fatalf("second ensureGitExclude: %v", err)
	}

	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("reading exclude: %v", err)
	}
	content := string(data)

	if count := strings.Count(content, "# agentfactory managed paths"); count != 1 {
		t.Errorf("sentinel count = %d, want 1", count)
	}
	for _, pattern := range []string{".agentfactory/", ".runtime/", "AGENTS.md", ".claude/"} {
		if count := strings.Count(content, pattern); count != 1 {
			t.Errorf("pattern %q count = %d, want 1", pattern, count)
		}
	}
}

func TestEnsureGitExclude_PreservesExisting(t *testing.T) {
	root := t.TempDir()
	gitInfoDir := filepath.Join(root, ".git", "info")
	if err := os.MkdirAll(gitInfoDir, 0755); err != nil {
		t.Fatal(err)
	}
	excludePath := filepath.Join(gitInfoDir, "exclude")
	preExisting := "*.log\nbuild/\n"
	if err := os.WriteFile(excludePath, []byte(preExisting), 0644); err != nil {
		t.Fatal(err)
	}

	if err := ensureGitExclude(root); err != nil {
		t.Fatalf("ensureGitExclude: %v", err)
	}

	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("reading exclude: %v", err)
	}
	content := string(data)

	if !strings.HasPrefix(content, preExisting) {
		t.Error("pre-existing content not preserved at beginning of file")
	}
	if !strings.Contains(content, "# agentfactory managed paths") {
		t.Error("missing sentinel comment")
	}
	for _, pattern := range []string{".agentfactory/", ".runtime/", "AGENTS.md", ".claude/"} {
		if !strings.Contains(content, pattern) {
			t.Errorf("missing pattern: %s", pattern)
		}
	}
}

func TestEnsureGitExclude_MissingFile(t *testing.T) {
	root := t.TempDir()
	gitInfoDir := filepath.Join(root, ".git", "info")
	if err := os.MkdirAll(gitInfoDir, 0755); err != nil {
		t.Fatal(err)
	}

	if err := ensureGitExclude(root); err != nil {
		t.Fatalf("ensureGitExclude: %v", err)
	}

	excludePath := filepath.Join(gitInfoDir, "exclude")
	data, err := os.ReadFile(excludePath)
	if err != nil {
		t.Fatalf("exclude file not created: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "# agentfactory managed paths") {
		t.Error("missing sentinel comment")
	}
	for _, pattern := range []string{".agentfactory/", ".runtime/", "AGENTS.md", ".claude/"} {
		if !strings.Contains(content, pattern) {
			t.Errorf("missing pattern: %s", pattern)
		}
	}
}

