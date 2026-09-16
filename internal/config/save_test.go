package config

import (
	"errors"
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

// The af config startup set write path (runConfigStartupSet) decodes stdin into a
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

// The af config startup set write path decodes stdin into a FRESH StartupConfig, so a document
// omitting "tokenomics" — i.e. every document in existence — reaches SaveStartupConfig with an
// all-zero block. This is the same write path TestSaveStartupConfig_MissingStepContextFillsDefaults
// pins for #622, and the same two-sided guard: the fill keeps the write path open, and the seed in
// defaultStartupConfig keeps the ABSENT-FILE path open, which never runs the validator at all.
func TestSaveStartupConfig_MissingTokenomicsFillsDefaults(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &StartupConfig{Quality: "on"} // caller-built literal, no Tokenomics
	if err := SaveStartupConfig(StartupConfigPath(dir), cfg); err != nil {
		t.Fatalf("a literal with no Tokenomics must save, got %v", err)
	}

	raw, err := os.ReadFile(StartupConfigPath(dir))
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !strings.Contains(string(raw), `"tokenomics"`) {
		t.Errorf("the written file must carry the filled block, got %s", raw)
	}

	loaded, err := LoadStartupConfig(dir)
	if err != nil {
		t.Fatalf("LoadStartupConfig: %v", err)
	}
	if want := defaultTokenomicsConfig(); loaded.Tokenomics != want {
		t.Errorf("Tokenomics = %+v, want %+v", loaded.Tokenomics, want)
	}

	// The seed enum is the umbrella, and it takes the same three values the four gate enums
	// beside it take. Nothing else in the block means anything until it is on.
	for _, v := range []string{"on", "off", "default"} {
		cfg := &StartupConfig{Tokenomics: TokenomicsConfig{Enabled: v}}
		if err := SaveStartupConfig(StartupConfigPath(dir), cfg); err != nil {
			t.Errorf("tokenomics.enabled = %q must be accepted, got %v", v, err)
		}
	}
}

// Every written tokenomics value rejects LOUDLY and names both the on-disk key and the value the
// operator wrote — the validateStepContextRelations posture, not the
// watchdog clamp. A misconfigured mechanism that silently corrected itself would be a policy the
// operator never chose and cannot see.
func TestSaveStartupConfig_WrittenTokenomicsRejectedLoudly(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	for _, tc := range []struct {
		name     string
		cfg      TokenomicsConfig
		wantKey  string
		wantText string
	}{
		{"the seed enum", TokenomicsConfig{Enabled: "sometimes"}, "tokenomics.enabled", "sometimes"},
		{"budget", TokenomicsConfig{Budget: "sometimes"}, "tokenomics.budget", "sometimes"},
		{"thrift", TokenomicsConfig{Thrift: "yes"}, "tokenomics.thrift", "yes"},
		{"dispatch", TokenomicsConfig{Dispatch: "maybe"}, "tokenomics.dispatch", "maybe"},
		{"interview", TokenomicsConfig{Interview: "1"}, "tokenomics.interview", "1"},
		{"effort", TokenomicsConfig{Effort: "high"}, "tokenomics.effort", "high"},
		{"escalate", TokenomicsConfig{Escalate: "ON"}, "tokenomics.escalate", "ON"},
		{"admission_margin_pct above the range", TokenomicsConfig{AdmissionMarginPct: 250}, "tokenomics.admission_margin_pct", "250"},
		{"admission_margin_pct below the range", TokenomicsConfig{AdmissionMarginPct: -1}, "tokenomics.admission_margin_pct", "-1"},
		{"learned_min_runs below the floor", TokenomicsConfig{LearnedMinRuns: -3}, "tokenomics.learned_min_runs", "-3"},
		{"efficiency", TokenomicsConfig{Efficiency: "sometimes"}, "tokenomics.efficiency", "sometimes"},
		{"efficiency_effort_level outside the host vocabulary", TokenomicsConfig{EfficiencyEffortLevel: "medium-ish"}, "tokenomics.efficiency_effort_level", "medium-ish"},
		{"efficiency_thinking_share_pct above the range", TokenomicsConfig{EfficiencyThinkingSharePct: 140}, "tokenomics.efficiency_thinking_share_pct", "140"},
		{"efficiency_thinking_share_pct below the range", TokenomicsConfig{EfficiencyThinkingSharePct: -1}, "tokenomics.efficiency_thinking_share_pct", "-1"},
		{"efficiency_repeat_read_floor below the floor", TokenomicsConfig{EfficiencyRepeatReadFloor: -2}, "tokenomics.efficiency_repeat_read_floor", "-2"},
		{"efficiency_max_relaunches below the floor", TokenomicsConfig{EfficiencyMaxRelaunches: -4}, "tokenomics.efficiency_max_relaunches", "-4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := SaveStartupConfig(StartupConfigPath(dir), &StartupConfig{Tokenomics: tc.cfg})
			if err == nil {
				t.Fatalf("SaveStartupConfig accepted %+v", tc.cfg)
			}
			if !errors.Is(err, ErrInvalidType) {
				t.Errorf("error %v should wrap ErrInvalidType", err)
			}
			for _, want := range []string{tc.wantKey, tc.wantText} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q must name %q so the operator can find what they wrote", err, want)
				}
			}
		})
	}
}

// The numeric knobs invert the enum ordering the same way step_context does: a zero is "absent on
// disk", so the relations run FIRST and skip it, and the fill then supplies the shipped value. If
// the fill ran first there would be no way left to tell an absent knob from a written 0, and an
// operator who wrote 0 deliberately would silently get 10.
func TestSaveStartupConfig_TokenomicsZeroIsAbsentNotWritten(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := &StartupConfig{Tokenomics: TokenomicsConfig{Budget: "on"}} // both numerics absent
	if err := SaveStartupConfig(StartupConfigPath(dir), cfg); err != nil {
		t.Fatalf("absent numerics must fill, not reject: %v", err)
	}
	want := defaultTokenomicsConfig()
	if cfg.Tokenomics.AdmissionMarginPct != want.AdmissionMarginPct {
		t.Errorf("admission_margin_pct = %d, want the shipped %d", cfg.Tokenomics.AdmissionMarginPct, want.AdmissionMarginPct)
	}
	if cfg.Tokenomics.LearnedMinRuns != want.LearnedMinRuns {
		t.Errorf("learned_min_runs = %d, want the shipped %d", cfg.Tokenomics.LearnedMinRuns, want.LearnedMinRuns)
	}
}

// Pins that the gate-enum loop still fires ahead of the tokenomics numeric relations, the same
// ordering property the recovery and step_context siblings pin. Only a document violating BOTH
// discriminates.
func TestSaveStartupConfig_GateEnumRejectedBeforeTokenomicsRelations(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	bad := &StartupConfig{Quality: "bogus", Tokenomics: TokenomicsConfig{AdmissionMarginPct: 250}}
	err := SaveStartupConfig(StartupConfigPath(dir), bad)
	if err == nil {
		t.Fatal("SaveStartupConfig accepted a config with both a bad gate and a bad relation")
	}
	if !strings.Contains(err.Error(), "quality") {
		t.Errorf("the gate-enum loop must fire ahead of the tokenomics relations; got %v", err)
	}
}
