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

	// An invalid gate enum is rejected before any write. The struct also carries a
	// zero-value Recovery, which the fill makes valid, so this proves only that a bad
	// gate still reaches the enum loop — NOT the check ordering. That is
	// TestSaveStartupConfig_GateEnumRejectedBeforeRecoveryRelations below.
	bad := &StartupConfig{Quality: "bogus"}
	err = SaveStartupConfig(StartupConfigPath(dir), bad)
	if err == nil {
		t.Error("SaveStartupConfig accepted an invalid quality enum")
	} else if !strings.Contains(err.Error(), "quality") {
		t.Errorf("a bad quality enum must fail on the QUALITY gate; got %v", err)
	}
}

// Pins the check ordering: the gate-enum loop must fire AHEAD of the K3 relation
// checks. A zero-value Recovery cannot pin this — fillRecoveryDefaults makes that
// block valid before the relations ever run, so both orderings would return the
// quality error. Only a document that violates BOTH discriminates, and getting it
// backwards tells the operator about the wrong field.
func TestSaveStartupConfig_GateEnumRejectedBeforeRecoveryRelations(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	bad := &StartupConfig{Quality: "bogus", Recovery: RecoveryConfig{ContextThresholdPct: 100}}
	err := SaveStartupConfig(StartupConfigPath(dir), bad)
	if err == nil {
		t.Fatal("SaveStartupConfig accepted a config with both a bad gate and a bad relation")
	}
	if !strings.Contains(err.Error(), "quality") {
		t.Errorf("the gate-enum loop must fire ahead of the recovery relations; got %v", err)
	}
}

// The af config startup set write path (config_set.go:134-141) decodes stdin into a
// FRESH StartupConfig, so a document omitting "recovery" — i.e. every document any
// operator has today — reaches SaveStartupConfig with an all-zero block. Only the
// validate-fill keeps that write path open.
func TestSaveStartupConfig_MissingRecoveryFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &StartupConfig{Quality: "on"} // caller-built literal, no Recovery
	if err := SaveStartupConfig(StartupConfigPath(dir), cfg); err != nil {
		t.Fatalf("a literal with no Recovery must save, got %v", err)
	}

	loaded, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("LoadStartupConfig: %v", err)
	}
	if loaded.Recovery.ContextThresholdPct != 85 {
		t.Errorf("Recovery.context_threshold_pct = %d, want the 85 default", loaded.Recovery.ContextThresholdPct)
	}
	if !loaded.Recovery.IsEnabled() {
		t.Error("a written-then-reloaded config must have recovery enabled by default")
	}
}

// The af config startup set write path decodes stdin into a FRESH StartupConfig, so a document
// omitting "step_context" — i.e. every document any operator has today — reaches SaveStartupConfig
// with an all-zero block. Only the validate-fill keeps that write path open.
//
// It also pins the accepted residual of the plain-int design (#622 C1): validateStartupConfig fills
// in place and SaveStartupConfig marshals the MUTATED struct, so the derived handoff_pct is
// materialised onto disk as an explicit key. The recovery block has behaved this way since K3, so
// the two are consistent; the consequence is recorded here rather than discovered later.
func TestSaveStartupConfig_MissingStepContextFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &StartupConfig{Quality: "on"} // caller-built literal, no StepContext
	if err := SaveStartupConfig(StartupConfigPath(dir), cfg); err != nil {
		t.Fatalf("a literal with no StepContext must save, got %v", err)
	}

	raw, err := os.ReadFile(StartupConfigPath(dir))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), `"step_context"`) {
		t.Errorf("the written file must carry the filled block, got %s", raw)
	}

	loaded, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("LoadStartupConfig: %v", err)
	}
	if loaded.StepContext.BoundTokens != 200000 || loaded.StepContext.HandoffPct != 75 {
		t.Errorf("StepContext = %+v, want {200000 75}", loaded.StepContext)
	}
}

// Pins that the gate-enum loop still fires ahead of the step_context ladder, the same ordering
// property TestSaveStartupConfig_GateEnumRejectedBeforeRecoveryRelations pins for K3. Only a
// document violating BOTH discriminates, and getting it backwards tells the operator about the
// wrong field.
func TestSaveStartupConfig_GateEnumRejectedBeforeStepContextRelations(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	bad := &StartupConfig{Quality: "bogus", StepContext: StepContextConfig{HandoffPct: 90}}
	err := SaveStartupConfig(StartupConfigPath(dir), bad)
	if err == nil {
		t.Fatal("SaveStartupConfig accepted a config with both a bad gate and a bad relation")
	}
	if !strings.Contains(err.Error(), "quality") {
		t.Errorf("the gate-enum loop must fire ahead of the step_context relations; got %v", err)
	}
}
