package config

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// writeStartupRoot creates a temp factory root with a startup.json containing data.
func writeStartupRoot(t *testing.T, data string) string {
	t.Helper()
	dir := t.TempDir()
	configDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(configDir, "startup.json"), []byte(data), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	return dir
}

// Case 1: absent file => defaults, no error (the C-4 divergence, highest-value test).
func TestLoadStartupConfig_AbsentFileDefaults(t *testing.T) {
	dir := t.TempDir() // no startup.json

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("expected nil error for absent file, got %v", err)
	}
	if errors.Is(err, ErrNotFound) {
		t.Fatalf("absent file must NOT return ErrNotFound (C-4 invariant)")
	}
	if cfg == nil {
		t.Fatal("expected non-nil cfg for absent file")
	}
	if cfg.Agents != nil {
		t.Errorf("Agents = %#v, want nil", cfg.Agents)
	}
	if cfg.WatchdogAgents != nil {
		t.Errorf("WatchdogAgents = %#v, want nil", cfg.WatchdogAgents)
	}
	if cfg.Quality != "default" {
		t.Errorf("Quality = %q, want \"default\"", cfg.Quality)
	}
	if cfg.Fidelity != "default" {
		t.Errorf("Fidelity = %q, want \"default\"", cfg.Fidelity)
	}
	if cfg.StartDispatch {
		t.Errorf("StartDispatch = true, want false")
	}
}

// Case 2: bad gate value => ErrInvalidType.
func TestLoadStartupConfig_BadGateValue(t *testing.T) {
	dir := writeStartupRoot(t, `{"quality":"of"}`)

	_, err := LoadStartupConfig(dir)
	if !errors.Is(err, ErrInvalidType) {
		t.Fatalf("expected ErrInvalidType, got %v", err)
	}
}

// Case 3: valid full file round-trips all five fields.
func TestLoadStartupConfig_FullRoundTrip(t *testing.T) {
	dir := writeStartupRoot(t, `{"agents":["manager","supervisor"],"quality":"on","fidelity":"off","start_dispatch":true,"watchdog_agents":["manager"]}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := []string{"manager", "supervisor"}; !reflect.DeepEqual(cfg.Agents, want) {
		t.Errorf("Agents = %#v, want %#v", cfg.Agents, want)
	}
	if cfg.Quality != "on" {
		t.Errorf("Quality = %q, want \"on\"", cfg.Quality)
	}
	if cfg.Fidelity != "off" {
		t.Errorf("Fidelity = %q, want \"off\"", cfg.Fidelity)
	}
	if !cfg.StartDispatch {
		t.Errorf("StartDispatch = false, want true")
	}
	if want := []string{"manager"}; !reflect.DeepEqual(cfg.WatchdogAgents, want) {
		t.Errorf("WatchdogAgents = %#v, want %#v", cfg.WatchdogAgents, want)
	}
}

// Case 4: agents: [] (present-empty) => non-nil empty slice (distinct from absent).
func TestLoadStartupConfig_EmptyAgentsSlice(t *testing.T) {
	dir := writeStartupRoot(t, `{"agents":[]}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents == nil {
		t.Fatal("Agents = nil, want non-nil empty slice")
	}
	if len(cfg.Agents) != 0 {
		t.Errorf("len(Agents) = %d, want 0", len(cfg.Agents))
	}
}

// Case 5: empty {} file => same defaults as absent-field.
func TestLoadStartupConfig_EmptyObjectDefaults(t *testing.T) {
	dir := writeStartupRoot(t, `{}`)

	cfg, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Quality != "default" {
		t.Errorf("Quality = %q, want \"default\"", cfg.Quality)
	}
	if cfg.Fidelity != "default" {
		t.Errorf("Fidelity = %q, want \"default\"", cfg.Fidelity)
	}
}

// Case 6: malformed JSON => non-nil error.
func TestLoadStartupConfig_MalformedJSON(t *testing.T) {
	dir := writeStartupRoot(t, `{not valid json}`)

	if _, err := LoadStartupConfig(dir); err == nil {
		t.Fatal("expected error for malformed JSON, got nil")
	}
}

// Case 7: the install scaffold must parse correctly with its opinionated defaults.
func TestLoadStartupConfig_ScaffoldLoads(t *testing.T) {
	scaffoldDir := writeStartupRoot(t, `{"agents":["manager"],"quality":"default","fidelity":"default","start_dispatch":true,"watchdog_agents":["mergepatrol"]}`)

	cfg, err := LoadStartupConfig(scaffoldDir)
	if err != nil {
		t.Fatalf("scaffold load error: %v", err)
	}
	if !reflect.DeepEqual(cfg.Agents, []string{"manager"}) {
		t.Errorf("agents = %v, want [manager]", cfg.Agents)
	}
	if !cfg.StartDispatch {
		t.Error("start_dispatch = false, want true")
	}
	if !reflect.DeepEqual(cfg.WatchdogAgents, []string{"mergepatrol"}) {
		t.Errorf("watchdog_agents = %v, want [mergepatrol]", cfg.WatchdogAgents)
	}
}
