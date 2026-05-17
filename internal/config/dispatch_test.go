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
	if cfg.Mappings[0].Label != "" {
		t.Errorf("mapping Label = %q, want empty (migrated to Labels)", cfg.Mappings[0].Label)
	}
	if len(cfg.Mappings[0].Labels) != 1 || cfg.Mappings[0].Labels[0] != "bug-triage" {
		t.Errorf("mapping Labels = %v, want [bug-triage]", cfg.Mappings[0].Labels)
	}
	if cfg.Mappings[0].Agent != "debugger" {
		t.Errorf("mapping Agent = %q, want %q", cfg.Mappings[0].Agent, "debugger")
	}
	if cfg.Mappings[0].Source != "issue" {
		t.Errorf("mapping Source = %q, want %q (default)", cfg.Mappings[0].Source, "issue")
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

func TestLoadDispatchConfig_LabelsArray(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"labels": ["bug", "triage"], "agent": "debugger"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(cfg.Mappings[0].Labels) != 2 {
		t.Fatalf("expected 2 labels, got %d", len(cfg.Mappings[0].Labels))
	}
	if cfg.Mappings[0].Labels[0] != "bug" || cfg.Mappings[0].Labels[1] != "triage" {
		t.Errorf("labels = %v, want [bug triage]", cfg.Mappings[0].Labels)
	}
	if cfg.Mappings[0].Label != "" {
		t.Errorf("label = %q, want empty (new-style uses labels)", cfg.Mappings[0].Label)
	}
	if cfg.Mappings[0].Source != "issue" {
		t.Errorf("source = %q, want %q (default)", cfg.Mappings[0].Source, "issue")
	}
}

func TestLoadDispatchConfig_LabelMigration(t *testing.T) {
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
	if cfg.Mappings[0].Label != "" {
		t.Errorf("label = %q, want empty (migrated to labels)", cfg.Mappings[0].Label)
	}
	if len(cfg.Mappings[0].Labels) != 1 || cfg.Mappings[0].Labels[0] != "bug" {
		t.Errorf("labels = %v, want [bug]", cfg.Mappings[0].Labels)
	}
	if cfg.Mappings[0].Source != "issue" {
		t.Errorf("source = %q, want %q (default)", cfg.Mappings[0].Source, "issue")
	}
}

func TestLoadDispatchConfig_BothLabelAndLabels(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"label": "bug", "labels": ["triage"], "agent": "debugger"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for ambiguous label+labels")
	}
	if !errors.Is(err, ErrMissingField) {
		t.Errorf("expected ErrMissingField, got: %v", err)
	}
}

func TestLoadDispatchConfig_SourceValidation(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"labels": ["bug"], "agent": "debugger", "source": "invalid"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := LoadDispatchConfig(dir)
	if err == nil {
		t.Fatal("expected error for invalid source value")
	}
	if !errors.Is(err, ErrInvalidType) {
		t.Errorf("expected ErrInvalidType, got: %v", err)
	}
}

func TestLoadDispatchConfig_SourceDefault(t *testing.T) {
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	data := `{
		"repos": ["owner/repo"],
		"trigger_label": "agentic",
		"mappings": [{"labels": ["bug"], "agent": "debugger"}]
	}`
	if err := os.WriteFile(filepath.Join(configDir, "dispatch.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	cfg, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Mappings[0].Source != "issue" {
		t.Errorf("source = %q, want %q (default)", cfg.Mappings[0].Source, "issue")
	}
}
