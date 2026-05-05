package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadDispatchConfig_Valid(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"label": "bug-triage", "agent": "debugger"}],
		"interval_seconds": 600,
		"retry_after_seconds": 3600
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Repos) != 1 || cfg.Repos[0] != "owner/repo" {
		t.Errorf("repos = %v, want [owner/repo]", cfg.Repos)
	}
	if cfg.TriggerLabel != "agentic" {
		t.Errorf("trigger_label = %q, want %q", cfg.TriggerLabel, "agentic")
	}
	if len(cfg.Mappings) != 1 {
		t.Fatalf("expected 1 mapping, got %d", len(cfg.Mappings))
	}
	if cfg.Mappings[0].Label != "bug-triage" || cfg.Mappings[0].Agent != "debugger" {
		t.Errorf("mapping = %+v, want {bug-triage debugger}", cfg.Mappings[0])
	}
	if cfg.IntervalSecs != 600 {
		t.Errorf("interval_seconds = %d, want 600", cfg.IntervalSecs)
	}
	if cfg.RetryAfterSecs != 3600 {
		t.Errorf("retry_after_seconds = %d, want 3600", cfg.RetryAfterSecs)
	}
	if cfg.NotifyOnComplete != "manager" {
		t.Errorf("notify_on_complete = %q, want %q (default)", cfg.NotifyOnComplete, "manager")
	}
}

func TestLoadDispatchConfig_FileNotFound(t *testing.T) {
	_, err := LoadDispatchConfig("/nonexistent/root")
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound, got: %v", err)
	}
}

func TestLoadDispatchConfig_MissingRepos(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": [],
		"trigger_label": "agentic",
		"mappings": [{"label": "bug", "agent": "debugger"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for empty repos")
	}
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("expected ErrMissingField, got: %v", err)
	}
}

func TestLoadDispatchConfig_MissingTriggerLabel(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "",
		"mappings": [{"label": "bug", "agent": "debugger"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for empty trigger_label")
	}
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("expected ErrMissingField, got: %v", err)
	}
}

func TestLoadDispatchConfig_EmptyMappingLabel(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"label": "", "agent": "debugger"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for empty mapping label")
	}
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("expected ErrMissingField, got: %v", err)
	}
}

func TestLoadDispatchConfig_EmptyMappingAgent(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"label": "bug", "agent": ""}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for empty mapping agent")
	}
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("expected ErrMissingField, got: %v", err)
	}
}

func TestLoadDispatchConfig_EmptyMappings(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": []
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for empty mappings")
	}
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("expected ErrMissingField, got: %v", err)
	}
}

func TestLoadDispatchConfig_DefaultsIntervalAndRetry(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"label": "bug", "agent": "debugger"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.IntervalSecs != 300 {
		t.Errorf("interval_seconds = %d, want 300 (default)", cfg.IntervalSecs)
	}
	if cfg.RetryAfterSecs != 1800 {
		t.Errorf("retry_after_seconds = %d, want 1800 (default)", cfg.RetryAfterSecs)
	}
}

func TestLoadDispatchConfig_DefaultsNotifyOnComplete(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"label": "bug", "agent": "debugger"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.NotifyOnComplete != "manager" {
		t.Errorf("notify_on_complete = %q, want %q (default)", cfg.NotifyOnComplete, "manager")
	}
}

func TestLoadDispatchConfig_ExplicitNotifyOnComplete(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"label": "bug", "agent": "debugger"}],
		"notify_on_complete": "supervisor"
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.NotifyOnComplete != "supervisor" {
		t.Errorf("notify_on_complete = %q, want %q", cfg.NotifyOnComplete, "supervisor")
	}
}

func TestLoadDispatchConfig_MalformedJSON(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(`{not valid json}`), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}
