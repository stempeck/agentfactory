package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

func TestRoleTypeFor_Interactive(t *testing.T) {
	agents := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"manager": {Type: "interactive", Description: "test manager"},
		},
	}
	got := RoleTypeFor("manager", agents)
	if got != Interactive {
		t.Errorf("RoleTypeFor(manager) = %d, want Interactive (%d)", got, Interactive)
	}
}

func TestRoleTypeFor_Autonomous(t *testing.T) {
	agents := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{
			"supervisor": {Type: "autonomous", Description: "test supervisor"},
		},
	}
	got := RoleTypeFor("supervisor", agents)
	if got != Autonomous {
		t.Errorf("RoleTypeFor(supervisor) = %d, want Autonomous (%d)", got, Autonomous)
	}
}

func TestRoleTypeFor_Default(t *testing.T) {
	agents := &config.AgentConfig{
		Agents: map[string]config.AgentEntry{},
	}
	got := RoleTypeFor("unknown", agents)
	if got != Interactive {
		t.Errorf("RoleTypeFor(unknown) = %d, want Interactive (%d) as default", got, Interactive)
	}
}

func TestEnsureSettings_Autonomous(t *testing.T) {
	dir := t.TempDir()

	err := EnsureSettings(dir, Autonomous)
	if err != nil {
		t.Fatalf("EnsureSettings(Autonomous) error: %v", err)
	}

	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	content := string(data)

	// Autonomous SessionStart MUST have both prime AND mail check
	if !strings.Contains(content, "af prime --hook && af mail check --inject") {
		t.Error("autonomous settings.json SessionStart missing 'af prime --hook && af mail check --inject'")
	}

	// Stop hook must reference quality-gate.sh
	if !strings.Contains(content, "quality-gate.sh") {
		t.Error("autonomous settings.json missing quality-gate.sh in Stop hook")
	}

	// Stop hooks must use ${AF_ROOT} for worktree-safe path resolution (C12)
	if strings.Contains(content, "$(af root)") {
		t.Error("autonomous settings.json Stop hook must use ${AF_ROOT}, not $(af root)")
	}
	if !strings.Contains(content, "${AF_ROOT}") {
		t.Error("autonomous settings.json Stop hook must reference ${AF_ROOT} for worktree compatibility")
	}
}

func TestEnsureSettings_Interactive(t *testing.T) {
	dir := t.TempDir()

	err := EnsureSettings(dir, Interactive)
	if err != nil {
		t.Fatalf("EnsureSettings(Interactive) error: %v", err)
	}

	settingsPath := filepath.Join(dir, ".claude", "settings.json")
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("reading settings.json: %v", err)
	}

	// Verify it's valid JSON
	var parsed map[string]interface{}
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("settings.json is not valid JSON: %v", err)
	}

	content := string(data)

	// Interactive SessionStart should have prime but NOT mail check
	if !strings.Contains(content, "af prime --hook") {
		t.Error("interactive settings.json SessionStart missing 'af prime --hook'")
	}

	// Parse and check SessionStart hook command specifically
	hooks := parsed["hooks"].(map[string]interface{})
	sessionStart := hooks["SessionStart"].([]interface{})
	firstEntry := sessionStart[0].(map[string]interface{})
	hooksList := firstEntry["hooks"].([]interface{})
	firstHook := hooksList[0].(map[string]interface{})
	cmd := firstHook["command"].(string)
	if strings.Contains(cmd, "af mail check") {
		t.Error("interactive SessionStart should NOT contain 'af mail check --inject'")
	}

	// Stop hook must reference quality-gate.sh
	if !strings.Contains(content, "quality-gate.sh") {
		t.Error("interactive settings.json missing quality-gate.sh in Stop hook")
	}

	// Stop hook must use ${AF_ROOT} for worktree-safe path resolution (C12)
	if strings.Contains(content, "$(af root)") {
		t.Error("interactive settings.json Stop hook must use ${AF_ROOT}, not $(af root)")
	}
	if !strings.Contains(content, "${AF_ROOT}") {
		t.Error("interactive settings.json Stop hook must reference ${AF_ROOT} for worktree compatibility")
	}
}

func TestEnsureSettings_CreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	claudeDir := filepath.Join(dir, ".claude")

	// .claude/ should not exist before
	if _, err := os.Stat(claudeDir); !os.IsNotExist(err) {
		t.Fatal(".claude/ already exists before EnsureSettings")
	}

	err := EnsureSettings(dir, Interactive)
	if err != nil {
		t.Fatalf("EnsureSettings error: %v", err)
	}

	// .claude/ should exist after
	if _, err := os.Stat(claudeDir); err != nil {
		t.Fatalf(".claude/ not created: %v", err)
	}

	// settings.json should exist
	settingsPath := filepath.Join(claudeDir, "settings.json")
	if _, err := os.Stat(settingsPath); err != nil {
		t.Fatalf("settings.json not created: %v", err)
	}
}
