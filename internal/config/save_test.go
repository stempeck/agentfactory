package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// assertNoTempResidue fails if any *.tmp file (the WriteFileAtomic scratch file)
// remains in dir after a successful atomic write.
func assertNoTempResidue(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir %s: %v", dir, err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("temp residue left after atomic write: %s", e.Name())
		}
	}
}

func TestSaveDispatchConfig_AtomicCrossFileValidated(t *testing.T) {
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	agents := &AgentConfig{Agents: map[string]AgentEntry{
		"debugger": {Type: "autonomous", Description: "d"},
		"manager":  {Type: "interactive", Description: "m"},
	}}

	// 1. A mapping referencing a non-existent agent is rejected by the cross-file
	//    validator — and SaveDispatchConfig is never reached, so the file is not
	//    created/corrupted.
	bad := &DispatchConfig{
		Repos:        []string{"owner/repo"},
		TriggerLabel: "agentic",
		Mappings:     []DispatchMapping{{Labels: []string{"bug"}, Agent: "ghost"}},
	}
	if err := ValidateDispatchConfig(bad, agents, nil); err == nil {
		t.Fatal("ValidateDispatchConfig accepted a mapping to an unknown agent")
	} else if !strings.Contains(err.Error(), "ghost") {
		t.Errorf("error %q should name the unknown agent %q", err.Error(), "ghost")
	}
	if _, err := os.Stat(DispatchConfigPath(dir)); !os.IsNotExist(err) {
		t.Errorf("dispatch.json must not exist after a rejected validation")
	}

	// 1b. NotifyOnComplete is also cross-checked.
	badNotify := &DispatchConfig{
		Repos:            []string{"owner/repo"},
		TriggerLabel:     "agentic",
		Mappings:         []DispatchMapping{{Labels: []string{"bug"}, Agent: "debugger"}},
		NotifyOnComplete: "ghost",
	}
	if err := ValidateDispatchConfig(badNotify, agents, nil); err == nil {
		t.Error("ValidateDispatchConfig accepted an unknown notify_on_complete agent")
	}

	// 2. A valid edit passes cross-file validation and persists atomically.
	good := &DispatchConfig{
		Repos:            []string{"owner/repo"},
		TriggerLabel:     "agentic",
		Mappings:         []DispatchMapping{{Labels: []string{"bug"}, Agent: "debugger"}},
		NotifyOnComplete: "manager",
		IntervalSecs:     600,
		RetryAfterSecs:   3600,
	}
	if err := ValidateDispatchConfig(good, agents, nil); err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	if err := SaveDispatchConfig(DispatchConfigPath(dir), good); err != nil {
		t.Fatalf("SaveDispatchConfig: %v", err)
	}
	assertNoTempResidue(t, afDir)

	// 3. It reloads identically.
	loaded, err := LoadDispatchConfig(dir)
	if err != nil {
		t.Fatalf("LoadDispatchConfig after save: %v", err)
	}
	if !reflect.DeepEqual(loaded.Repos, good.Repos) ||
		loaded.TriggerLabel != good.TriggerLabel ||
		loaded.NotifyOnComplete != good.NotifyOnComplete ||
		len(loaded.Mappings) != 1 || loaded.Mappings[0].Agent != "debugger" {
		t.Errorf("round-trip mismatch: loaded=%+v", loaded)
	}
}

func TestSaveStartupConfig_Atomic(t *testing.T) {
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &StartupConfig{
		Agents:         []string{"manager"},
		Quality:        "on",
		Fidelity:       "default",
		StartDispatch:  true,
		WatchdogAgents: []string{"manager"},
	}
	// Writes even though the file does not pre-exist (absent-file invariant lives
	// in Load, not Save).
	if err := SaveStartupConfig(StartupConfigPath(dir), cfg); err != nil {
		t.Fatalf("SaveStartupConfig: %v", err)
	}
	assertNoTempResidue(t, afDir)

	loaded, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("LoadStartupConfig: %v", err)
	}
	if loaded.Quality != "on" || !loaded.StartDispatch ||
		!reflect.DeepEqual(loaded.WatchdogAgents, []string{"manager"}) {
		t.Errorf("round-trip mismatch: loaded=%+v", loaded)
	}

	// An invalid gate enum is rejected before any write.
	bad := &StartupConfig{Quality: "bogus"}
	if err := SaveStartupConfig(StartupConfigPath(dir), bad); err == nil {
		t.Error("SaveStartupConfig accepted an invalid quality enum")
	}
}
