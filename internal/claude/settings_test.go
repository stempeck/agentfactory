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
	if !strings.Contains(content, ".agentfactory/hooks/quality-gate.sh") {
		t.Error("autonomous settings.json Stop hook should use .agentfactory/hooks/ path for quality-gate.sh")
	}

	// Stop hooks must use ${AF_ROOT} for worktree-safe path resolution (C12)
	if strings.Contains(content, "$(af root)") {
		t.Error("autonomous settings.json Stop hook must use ${AF_ROOT}, not $(af root)")
	}
	if !strings.Contains(content, "${AF_ROOT}") {
		t.Error("autonomous settings.json Stop hook must reference ${AF_ROOT} for worktree compatibility")
	}

	if !strings.Contains(content, "af compact-handoff") {
		t.Error("autonomous settings.json PreCompact missing 'af compact-handoff'")
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
	if !strings.Contains(content, ".agentfactory/hooks/quality-gate.sh") {
		t.Error("interactive settings.json Stop hook should use .agentfactory/hooks/ path for quality-gate.sh")
	}

	// Stop hook must use ${AF_ROOT} for worktree-safe path resolution (C12)
	if strings.Contains(content, "$(af root)") {
		t.Error("interactive settings.json Stop hook must use ${AF_ROOT}, not $(af root)")
	}
	if !strings.Contains(content, "${AF_ROOT}") {
		t.Error("interactive settings.json Stop hook must reference ${AF_ROOT} for worktree compatibility")
	}

	preCompact := hooks["PreCompact"].([]interface{})
	preCompactEntry := preCompact[0].(map[string]interface{})
	preCompactHooksList := preCompactEntry["hooks"].([]interface{})
	preCompactHook := preCompactHooksList[0].(map[string]interface{})
	preCompactCmd := preCompactHook["command"].(string)
	if !strings.Contains(preCompactCmd, "af compact-handoff --interactive") {
		t.Errorf("interactive PreCompact command should contain 'af compact-handoff --interactive', got: %s", preCompactCmd)
	}
}

// TestEnsureSettings_DeniesAskUserQuestion proves the AskUserQuestion hard-disable
// (issue af-69d8bf24) propagates into every generated agent's .claude/settings.json,
// for BOTH role types.
//
//	Scenario: AskUserQuestion is denied fleet-wide
//	  Given an agent of any role type (interactive or autonomous)
//	  When EnsureSettings writes its .claude/settings.json
//	  Then the settings carry a permissions.deny entry for "AskUserQuestion"
//
// Motivation: in some environments the built-in AskUserQuestion tool auto-selects its
// DEFAULT option and times out as if the human answered, fabricating false approvals.
// Denying the tool by name blocks all of its uses, forcing agents to ask in plain text
// and wait for a real reply. EnsureSettings copies the embedded template verbatim, so a
// deny rule in the template is the propagation mechanism this test guards.
func TestEnsureSettings_DeniesAskUserQuestion(t *testing.T) {
	cases := []struct {
		name     string
		roleType RoleType
	}{
		{"Interactive", Interactive},
		{"Autonomous", Autonomous},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s) error: %v", tc.name, err)
			}

			settingsPath := filepath.Join(dir, ".claude", "settings.json")
			data, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}

			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			permsRaw, ok := parsed["permissions"]
			if !ok {
				t.Fatalf("%s settings.json missing top-level \"permissions\" block", tc.name)
			}
			perms, ok := permsRaw.(map[string]interface{})
			if !ok {
				t.Fatalf("%s settings.json \"permissions\" is not an object", tc.name)
			}
			denyRaw, ok := perms["deny"]
			if !ok {
				t.Fatalf("%s settings.json permissions missing \"deny\" array", tc.name)
			}
			deny, ok := denyRaw.([]interface{})
			if !ok {
				t.Fatalf("%s settings.json permissions.deny is not an array", tc.name)
			}

			found := false
			for _, entry := range deny {
				if s, ok := entry.(string); ok && s == "AskUserQuestion" {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s settings.json permissions.deny must contain \"AskUserQuestion\", got: %v", tc.name, deny)
			}
		})
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

// TestEnsureSettings_PreToolUseContainment is a STRUCTURAL presence check (Phase 3,
// issue #386): it proves BOTH templates carry the PreToolUse -> af containment-check
// hook after EnsureSettings, for every role type. It is deliberately SUBORDINATE and
// SEPARATE from the behavioral enforcement tests — presence-in-settings is necessary but
// is NOT the detection/correction test. The real interlock behavior is proven by the
// Phase 2 seam tests (af containment-check) and the Phase 4 e2e; do not let this
// structural check stand in for them.
func TestEnsureSettings_PreToolUseContainment(t *testing.T) {
	cases := []struct {
		name     string
		roleType RoleType
	}{
		{"Interactive", Interactive},
		{"Autonomous", Autonomous},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := EnsureSettings(dir, tc.roleType); err != nil {
				t.Fatalf("EnsureSettings(%s) error: %v", tc.name, err)
			}

			settingsPath := filepath.Join(dir, ".claude", "settings.json")
			data, err := os.ReadFile(settingsPath)
			if err != nil {
				t.Fatalf("reading settings.json: %v", err)
			}

			var parsed map[string]interface{}
			if err := json.Unmarshal(data, &parsed); err != nil {
				t.Fatalf("settings.json is not valid JSON: %v", err)
			}

			// Walk into hooks.PreToolUse[0].hooks[0].command (mirrors the SessionStart
			// idiom above). The PreToolUse lookup is comma-ok-guarded because its very
			// presence is what this test proves.
			hooks := parsed["hooks"].(map[string]interface{})
			preToolUseRaw, ok := hooks["PreToolUse"]
			if !ok {
				t.Fatalf("%s settings.json missing top-level hooks.PreToolUse entry", tc.name)
			}
			preToolUse := preToolUseRaw.([]interface{})
			if len(preToolUse) == 0 {
				t.Fatalf("%s PreToolUse array is empty", tc.name)
			}
			firstEntry := preToolUse[0].(map[string]interface{})

			// Matcher uniquely fingerprints this hook vs. the empty-matcher siblings (D-8:
			// scope to cwd-affecting tools).
			matcher, _ := firstEntry["matcher"].(string)
			if matcher != "Bash|Write|Edit" {
				t.Errorf("%s PreToolUse matcher = %q, want \"Bash|Write|Edit\"", tc.name, matcher)
			}

			hooksList := firstEntry["hooks"].([]interface{})
			if len(hooksList) == 0 {
				t.Fatalf("%s PreToolUse hooks array is empty", tc.name)
			}
			firstHook := hooksList[0].(map[string]interface{})
			cmd := firstHook["command"].(string)

			if !strings.Contains(cmd, "af containment-check") {
				t.Errorf("%s PreToolUse command should run 'af containment-check', got: %s", tc.name, cmd)
			}
			// Carries the same PATH-export prefix as every other direct-af hook. NOT
			// ${AF_ROOT} (that token belongs only to the bash-script Stop hooks).
			if !strings.Contains(cmd, `export PATH="$HOME/go/bin:`) {
				t.Errorf("%s PreToolUse command should carry the export PATH= prefix, got: %s", tc.name, cmd)
			}
		})
	}
}
