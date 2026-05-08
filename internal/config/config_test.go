package config

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// --- LoadAgentConfig tests ---

func TestLoadAgentConfig_ValidTwoAgents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	data := `{
		"agents": {
			"manager": {"type": "interactive", "description": "General Manager"},
			"supervisor": {"type": "autonomous", "description": "Floor Supervisor"}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Agents) != 2 {
		t.Fatalf("expected 2 agents, got %d", len(cfg.Agents))
	}
	if cfg.Agents["manager"].Type != "interactive" {
		t.Errorf("manager type = %q, want %q", cfg.Agents["manager"].Type, "interactive")
	}
	if cfg.Agents["supervisor"].Type != "autonomous" {
		t.Errorf("supervisor type = %q, want %q", cfg.Agents["supervisor"].Type, "autonomous")
	}
}

func TestLoadAgentConfig_InvalidType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	data := `{"agents": {"bad": {"type": "daemon", "description": "Invalid"}}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadAgentConfig(path)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
	if !errors.Is(err, ErrInvalidType) {
		t.Errorf("expected ErrInvalidType, got: %v", err)
	}
}

func TestLoadAgentConfig_EmptyDescription(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	data := `{"agents": {"mgr": {"type": "interactive", "description": ""}}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadAgentConfig(path)
	if err == nil {
		t.Fatal("expected error for empty description")
	}
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("expected ErrMissingField, got: %v", err)
	}
}

func TestLoadAgentConfig_FileNotFound(t *testing.T) {
	_, err := LoadAgentConfig("/nonexistent/agents.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestLoadAgentConfig_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	if err := os.WriteFile(path, []byte(`{not valid json}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadAgentConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// --- LoadMessagingConfig tests ---

func TestLoadMessagingConfig_ValidWithGroups(t *testing.T) {
	dir := t.TempDir()

	agents := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager":    {Type: "interactive", Description: "Manager"},
			"supervisor": {Type: "autonomous", Description: "Supervisor"},
		},
	}

	path := filepath.Join(dir, "messaging.json")
	data := `{"groups": {"supervisors": ["supervisor"]}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadMessagingConfig(path, agents)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(cfg.Groups))
	}
	members := cfg.Groups["supervisors"]
	if len(members) != 1 || members[0] != "supervisor" {
		t.Errorf("supervisors group = %v, want [supervisor]", members)
	}
}

func TestLoadMessagingConfig_GroupMemberNotInAgents(t *testing.T) {
	dir := t.TempDir()

	agents := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager": {Type: "interactive", Description: "Manager"},
		},
	}

	path := filepath.Join(dir, "messaging.json")
	data := `{"groups": {"workers": ["manager", "nonexistent"]}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadMessagingConfig(path, agents)
	if err == nil {
		t.Fatal("expected error for nonexistent agent in group")
	}
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("expected ErrMissingField, got: %v", err)
	}
}

func TestLoadMessagingConfig_EmptyGroups(t *testing.T) {
	dir := t.TempDir()

	agents := &AgentConfig{
		Agents: map[string]AgentEntry{},
	}

	path := filepath.Join(dir, "messaging.json")
	data := `{"groups": {}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadMessagingConfig(path, agents)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Groups) != 0 {
		t.Errorf("expected 0 groups, got %d", len(cfg.Groups))
	}
}

func TestLoadMessagingConfig_FileNotFound(t *testing.T) {
	agents := &AgentConfig{Agents: map[string]AgentEntry{}}
	_, err := LoadMessagingConfig("/nonexistent/messaging.json", agents)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

// --- LoadFactoryConfig tests ---

func TestLoadFactoryConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	data := `{"type": "factory", "version": 1, "name": "agentfactory"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadFactoryConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Type != "factory" {
		t.Errorf("type = %q, want %q", cfg.Type, "factory")
	}
	if cfg.Version != 1 {
		t.Errorf("version = %d, want 1", cfg.Version)
	}
	if cfg.Name != "agentfactory" {
		t.Errorf("name = %q, want %q", cfg.Name, "agentfactory")
	}
}

func TestLoadFactoryConfig_WrongType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	data := `{"type": "town", "version": 1, "name": "test"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadFactoryConfig(path)
	if err == nil {
		t.Fatal("expected error for wrong type")
	}
	if !errors.Is(err, ErrInvalidType) {
		t.Errorf("expected ErrInvalidType, got: %v", err)
	}
}

func TestLoadFactoryConfig_VersionZero(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	data := `{"type": "factory", "version": 0, "name": "test"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadFactoryConfig(path)
	if err == nil {
		t.Fatal("expected error for version 0")
	}
	if !errors.Is(err, ErrInvalidVersion) {
		t.Errorf("expected ErrInvalidVersion, got: %v", err)
	}
}

func TestLoadFactoryConfig_FutureVersion(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	data := `{"type": "factory", "version": 9999, "name": "test"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadFactoryConfig(path)
	if err == nil {
		t.Fatal("expected error for future version")
	}
	if !errors.Is(err, ErrInvalidVersion) {
		t.Errorf("expected ErrInvalidVersion, got: %v", err)
	}
}

func TestLoadFactoryConfig_FileNotFound(t *testing.T) {
	_, err := LoadFactoryConfig("/nonexistent/factory.json")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestLoadFactoryConfig_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	if err := os.WriteFile(path, []byte(`{bad json}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadFactoryConfig(path)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

// --- ValidateAgentName tests ---

func TestValidateAgentName_ValidNames(t *testing.T) {
	valid := []string{"analyst", "code-reviewer", "test_runner", "A1", "a", "myAgent123"}
	for _, name := range valid {
		if err := ValidateAgentName(name); err != nil {
			t.Errorf("ValidateAgentName(%q) = %v, want nil", name, err)
		}
	}
}

func TestValidateAgentName_Empty(t *testing.T) {
	err := ValidateAgentName("")
	if err == nil {
		t.Fatal("expected error for empty name")
	}
	if !strings.Contains(err.Error(), "cannot be empty") {
		t.Errorf("error = %q, want it to contain 'cannot be empty'", err.Error())
	}
}

func TestValidateAgentName_TooLong(t *testing.T) {
	name := strings.Repeat("a", 65)
	err := ValidateAgentName(name)
	if err == nil {
		t.Fatal("expected error for name > 64 chars")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("error = %q, want it to contain 'too long'", err.Error())
	}
}

func TestValidateAgentName_ExactlyMaxLength(t *testing.T) {
	name := "a" + strings.Repeat("b", 63) // 64 chars, starts with letter
	if err := ValidateAgentName(name); err != nil {
		t.Errorf("ValidateAgentName(64-char name) = %v, want nil", err)
	}
}

func TestValidateAgentName_InvalidChars(t *testing.T) {
	invalid := []string{
		"../../etc",    // path traversal
		"agent;rm",     // shell injection
		"agent name",   // spaces
		"123start",     // starts with digit
		".hidden",      // starts with dot
		"agent\ttab",   // tab
		"agent/slash",  // slash
		"",             // empty (covered separately but included for completeness)
	}
	for _, name := range invalid {
		if name == "" {
			continue // tested separately
		}
		err := ValidateAgentName(name)
		if err == nil {
			t.Errorf("ValidateAgentName(%q) = nil, want error", name)
		}
	}
}

// --- SaveAgentConfig tests ---

func TestSaveAgentConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")

	original := &AgentConfig{
		Agents: map[string]AgentEntry{
			"test-agent": {Type: "autonomous", Description: "Test agent", Formula: "specialist-v1"},
		},
	}

	if err := SaveAgentConfig(path, original); err != nil {
		t.Fatalf("SaveAgentConfig: %v", err)
	}

	loaded, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}

	if loaded.Agents["test-agent"].Type != "autonomous" {
		t.Errorf("type = %q, want %q", loaded.Agents["test-agent"].Type, "autonomous")
	}
	if loaded.Agents["test-agent"].Formula != "specialist-v1" {
		t.Errorf("formula = %q, want %q", loaded.Agents["test-agent"].Formula, "specialist-v1")
	}
}

func TestSaveAgentConfig_OmitsEmptyFormula(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")

	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager": {Type: "interactive", Description: "Manager"},
		},
	}

	if err := SaveAgentConfig(path, cfg); err != nil {
		t.Fatalf("SaveAgentConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	if strings.Contains(string(data), "formula") {
		t.Errorf("expected no 'formula' key in JSON output for empty formula, got: %s", data)
	}
}

func TestSaveAgentConfig_WritesValidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")

	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"a": {Type: "interactive", Description: "Agent A"},
			"b": {Type: "autonomous", Description: "Agent B", Formula: "gen-v2"},
		},
	}

	if err := SaveAgentConfig(path, cfg); err != nil {
		t.Fatalf("SaveAgentConfig: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}

	var check AgentConfig
	if err := json.Unmarshal(data, &check); err != nil {
		t.Fatalf("written file is not valid JSON: %v", err)
	}
}

// --- AddAgentEntry tests ---

func TestAddAgentEntry_NewToEmptyConfig(t *testing.T) {
	cfg := &AgentConfig{} // nil Agents map

	err := AddAgentEntry(cfg, "new-agent", AgentEntry{
		Type:        "autonomous",
		Description: "New agent",
		Formula:     "specialist-v1",
	})
	if err != nil {
		t.Fatalf("AddAgentEntry: %v", err)
	}

	if cfg.Agents == nil {
		t.Fatal("expected Agents map to be initialized")
	}
	if cfg.Agents["new-agent"].Formula != "specialist-v1" {
		t.Errorf("formula = %q, want %q", cfg.Agents["new-agent"].Formula, "specialist-v1")
	}
}

func TestAddAgentEntry_UpdateFormulaAgent(t *testing.T) {
	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"gen-agent": {Type: "autonomous", Description: "Gen agent", Formula: "v1"},
		},
	}

	err := AddAgentEntry(cfg, "gen-agent", AgentEntry{
		Type:        "autonomous",
		Description: "Gen agent updated",
		Formula:     "v2",
	})
	if err != nil {
		t.Fatalf("AddAgentEntry: %v", err)
	}
	if cfg.Agents["gen-agent"].Formula != "v2" {
		t.Errorf("formula = %q, want %q", cfg.Agents["gen-agent"].Formula, "v2")
	}
}

func TestAddAgentEntry_RefuseOverwriteManualAgent(t *testing.T) {
	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager": {Type: "interactive", Description: "Manager"},
		},
	}

	err := AddAgentEntry(cfg, "manager", AgentEntry{
		Type:        "autonomous",
		Description: "Overwritten",
		Formula:     "gen-v1",
	})
	if err == nil {
		t.Fatal("expected error when overwriting manual agent")
	}
	if !errors.Is(err, ErrAgentExists) {
		t.Errorf("expected ErrAgentExists, got: %v", err)
	}
	// Verify original entry unchanged
	if cfg.Agents["manager"].Type != "interactive" {
		t.Errorf("manager type changed to %q, should be unchanged", cfg.Agents["manager"].Type)
	}
}

// --- Formula field backward compatibility ---

func TestLoadAgentConfig_FormulaFieldLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	data := `{
		"agents": {
			"gen-agent": {"type": "autonomous", "description": "Generated", "formula": "specialist-v1"}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents["gen-agent"].Formula != "specialist-v1" {
		t.Errorf("formula = %q, want %q", cfg.Agents["gen-agent"].Formula, "specialist-v1")
	}
}

func TestLoadAgentConfig_NoFormulaFieldStillLoads(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	data := `{
		"agents": {
			"manager": {"type": "interactive", "description": "Manager"}
		}
	}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents["manager"].Formula != "" {
		t.Errorf("formula = %q, want empty string", cfg.Agents["manager"].Formula)
	}
}

// --- Phase 4 tests ---

func TestSaveAgentConfig_AtomicNoTempFileRemains(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")

	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"test": {Type: "autonomous", Description: "Test agent"},
		},
	}

	if err := SaveAgentConfig(path, cfg); err != nil {
		t.Fatalf("SaveAgentConfig: %v", err)
	}

	tmpPath := filepath.Join(dir, ".agents.json.tmp")
	if _, err := os.Stat(tmpPath); !os.IsNotExist(err) {
		t.Errorf(".agents.json.tmp still exists after successful write")
	}
}

func TestSaveAgentConfig_PreservesExistingAgents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")

	// Save initial config with manager + supervisor (no formula)
	initial := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager":    {Type: "interactive", Description: "General Manager"},
			"supervisor": {Type: "autonomous", Description: "Floor Supervisor"},
		},
	}
	if err := SaveAgentConfig(path, initial); err != nil {
		t.Fatalf("SaveAgentConfig (initial): %v", err)
	}

	// Load, add a formula agent, save again
	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig: %v", err)
	}
	if err := AddAgentEntry(cfg, "investigate", AgentEntry{
		Type: "autonomous", Description: "Investigator", Formula: "investigate",
	}); err != nil {
		t.Fatalf("AddAgentEntry: %v", err)
	}
	if err := SaveAgentConfig(path, cfg); err != nil {
		t.Fatalf("SaveAgentConfig (with formula agent): %v", err)
	}

	// Reload and verify manager + supervisor are unchanged
	reloaded, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("LoadAgentConfig (reloaded): %v", err)
	}

	if reloaded.Agents["manager"].Type != "interactive" {
		t.Errorf("manager type = %q, want %q", reloaded.Agents["manager"].Type, "interactive")
	}
	if reloaded.Agents["manager"].Description != "General Manager" {
		t.Errorf("manager description = %q, want %q", reloaded.Agents["manager"].Description, "General Manager")
	}
	if reloaded.Agents["supervisor"].Type != "autonomous" {
		t.Errorf("supervisor type = %q, want %q", reloaded.Agents["supervisor"].Type, "autonomous")
	}
	if reloaded.Agents["supervisor"].Description != "Floor Supervisor" {
		t.Errorf("supervisor description = %q, want %q", reloaded.Agents["supervisor"].Description, "Floor Supervisor")
	}
	if reloaded.Agents["investigate"].Formula != "investigate" {
		t.Errorf("investigate formula = %q, want %q", reloaded.Agents["investigate"].Formula, "investigate")
	}
}

func TestValidateAgentName_AdditionalEdgeCases(t *testing.T) {
	// Invalid names
	invalid := []struct {
		name string
		desc string
	}{
		{`"; rm -rf /"`, "shell injection with semicolon and quotes"},
		{"foo bar", "space in name"},
		{"foo/bar", "slash in name"},
		{"@#$%", "only special chars"},
	}
	for _, tc := range invalid {
		err := ValidateAgentName(tc.name)
		if err == nil {
			t.Errorf("ValidateAgentName(%q) = nil, want error (%s)", tc.name, tc.desc)
		}
	}

	// Valid: real long name with hyphens
	if err := ValidateAgentName("factoryworker"); err != nil {
		t.Errorf("ValidateAgentName(%q) = %v, want nil", "factoryworker", err)
	}
}

func TestValidateAgentName_ReservedDispatch(t *testing.T) {
	err := ValidateAgentName("dispatch")
	if err == nil {
		t.Fatal("expected error for reserved name \"dispatch\"")
	}
	if !strings.Contains(err.Error(), "reserved") {
		t.Errorf("error = %q, want it to contain 'reserved'", err.Error())
	}
}

// --- RemoveAgentEntry tests ---

func TestRemoveAgentEntry_FormulaAgent(t *testing.T) {
	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager":     {Type: "interactive", Description: "Manager"},
			"investigate": {Type: "autonomous", Description: "Investigator", Formula: "investigate"},
		},
	}

	err := RemoveAgentEntry(cfg, "investigate")
	if err != nil {
		t.Fatalf("RemoveAgentEntry: %v", err)
	}
	if _, exists := cfg.Agents["investigate"]; exists {
		t.Error("investigate entry should be removed from config")
	}
}

func TestRemoveAgentEntry_RefuseManualAgent(t *testing.T) {
	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager": {Type: "interactive", Description: "Manager"},
		},
	}

	err := RemoveAgentEntry(cfg, "manager")
	if err == nil {
		t.Fatal("expected error when removing manual agent")
	}
	if !errors.Is(err, ErrManualAgent) {
		t.Errorf("expected ErrManualAgent, got: %v", err)
	}
	// Verify original entry unchanged
	if _, exists := cfg.Agents["manager"]; !exists {
		t.Error("manager entry should still exist after refused removal")
	}
}

func TestRemoveAgentEntry_NonExistentAgent(t *testing.T) {
	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager": {Type: "interactive", Description: "Manager"},
		},
	}

	err := RemoveAgentEntry(cfg, "nonexistent")
	if err == nil {
		t.Fatal("expected error for nonexistent agent")
	}
	if !errors.Is(err, ErrAgentNotFound) {
		t.Errorf("expected ErrAgentNotFound, got: %v", err)
	}
}

func TestRemoveAgentEntry_PreservesOtherAgents(t *testing.T) {
	cfg := &AgentConfig{
		Agents: map[string]AgentEntry{
			"manager":     {Type: "interactive", Description: "Manager"},
			"supervisor":  {Type: "autonomous", Description: "Supervisor"},
			"investigate": {Type: "autonomous", Description: "Investigator", Formula: "investigate"},
		},
	}

	if err := RemoveAgentEntry(cfg, "investigate"); err != nil {
		t.Fatalf("RemoveAgentEntry: %v", err)
	}

	if len(cfg.Agents) != 2 {
		t.Fatalf("expected 2 agents after removal, got %d", len(cfg.Agents))
	}
	if cfg.Agents["manager"].Type != "interactive" {
		t.Errorf("manager type = %q, want %q", cfg.Agents["manager"].Type, "interactive")
	}
	if cfg.Agents["supervisor"].Type != "autonomous" {
		t.Errorf("supervisor type = %q, want %q", cfg.Agents["supervisor"].Type, "autonomous")
	}
}

func TestValidateAgentConfig_RejectsMetachars(t *testing.T) {
	badNames := []string{
		"agent'; rm -rf /",
		"$(whoami)",
		"`id`",
		"foo;bar",
		"../../etc",
	}

	for _, name := range badNames {
		cfg := &AgentConfig{
			Agents: map[string]AgentEntry{
				name: {Type: "autonomous", Description: "Evil agent"},
			},
		}
		err := validateAgentConfig(cfg)
		if err == nil {
			t.Errorf("validateAgentConfig accepted agent name %q, want error", name)
		}
	}
}

func TestLoadFactoryConfig_MaxWorktreesField(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	data := `{"type":"factory","version":1,"name":"test","max_worktrees":4}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadFactoryConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxWorktrees != 4 {
		t.Errorf("MaxWorktrees = %d, want 4", cfg.MaxWorktrees)
	}
}

func TestLoadFactoryConfig_MaxWorktreesMissing(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "factory.json")
	data := `{"type":"factory","version":1,"name":"test"}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadFactoryConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.MaxWorktrees != 0 {
		t.Errorf("MaxWorktrees = %d, want 0 (default)", cfg.MaxWorktrees)
	}
}

func TestLoadAgentConfig_SparsePaths(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "agents.json")
	data := `{"agents":{"solver":{"type":"autonomous","description":"Solver","sparse_paths":["src/","docs/"]}}}`
	if err := os.WriteFile(path, []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadAgentConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	entry := cfg.Agents["solver"]
	if len(entry.SparsePaths) != 2 {
		t.Fatalf("SparsePaths length = %d, want 2", len(entry.SparsePaths))
	}
	if entry.SparsePaths[0] != "src/" || entry.SparsePaths[1] != "docs/" {
		t.Errorf("SparsePaths = %v, want [src/ docs/]", entry.SparsePaths)
	}
}
