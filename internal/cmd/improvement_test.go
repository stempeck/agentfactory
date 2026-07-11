package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/config"
)

// setupTestFactoryForImprovement creates a minimal factory layout (factory.json,
// plus an optional agents.json) so config.FindFactoryRoot and the agent enumeration
// both succeed. agents maps an agent name to its continuous_improvement flag; pass
// nil to omit agents.json entirely (the fresh-factory AC-1 case). Mirrors
// setupTestFactoryForFidelity (fidelity_test.go), which writes factory.json ONLY.
func setupTestFactoryForImprovement(t *testing.T, agents map[string]bool) string {
	t.Helper()
	dir := t.TempDir()
	afDir := filepath.Join(dir, ".agentfactory")
	if err := os.MkdirAll(afDir, 0o755); err != nil {
		t.Fatalf("mkdir .agentfactory: %v", err)
	}
	if err := os.WriteFile(
		filepath.Join(afDir, "factory.json"),
		[]byte(`{"type":"factory","version":1}`+"\n"),
		0o644,
	); err != nil {
		t.Fatalf("write factory.json: %v", err)
	}
	if agents != nil {
		cfg := &config.AgentConfig{Agents: map[string]config.AgentEntry{}}
		for name, ci := range agents {
			cfg.Agents[name] = config.AgentEntry{
				Type:                  "autonomous",
				Description:           name + " agent",
				ContinuousImprovement: ci,
			}
		}
		if err := config.SaveAgentConfig(config.AgentsConfigPath(dir), cfg); err != nil {
			t.Fatalf("save agents.json: %v", err)
		}
	}
	return dir
}

// setAgentFlag sets the shared improvementCmd --agent flag and returns a reset func.
// The flag lives on a package-global cobra.Command, so a leaked value bleeds into
// later tests (precedent: dispatch_status_json_test.go flag handling).
func setAgentFlag(t *testing.T, name string) {
	t.Helper()
	if err := improvementCmd.Flags().Set("agent", name); err != nil {
		t.Fatalf("set --agent: %v", err)
	}
	t.Cleanup(func() { _ = improvementCmd.Flags().Set("agent", "") })
}

// --- ST-1: improvementEnabled AND truth table + error paths (AC-4) ---

func TestImprovementEnabled_TruthTable(t *testing.T) {
	for _, factory := range []bool{false, true} {
		for _, agent := range []bool{false, true} {
			factory, agent := factory, agent
			t.Run("", func(t *testing.T) {
				root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": agent})
				if factory {
					if err := os.WriteFile(improvementHookFile(root), []byte("on\n"), 0o644); err != nil {
						t.Fatal(err)
					}
				}
				want := factory && agent
				if got := improvementEnabled(root, "alpha"); got != want {
					t.Errorf("improvementEnabled(factory=%v, agent=%v) = %v, want %v", factory, agent, got, want)
				}
			})
		}
	}
}

func TestImprovementEnabled_ErrorPathsFalse(t *testing.T) {
	// Factory on but agent absent from agents.json ⇒ false.
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	if err := os.WriteFile(improvementHookFile(root), []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if improvementEnabled(root, "ghost") {
		t.Error("improvementEnabled for unknown agent should be false")
	}

	// Factory on but no agents.json at all ⇒ false (load error).
	bare := t.TempDir()
	if err := os.MkdirAll(filepath.Join(bare, ".agentfactory"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(improvementHookFile(bare), []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if improvementEnabled(bare, "alpha") {
		t.Error("improvementEnabled with no agents.json should be false")
	}

	// Factory file absent ⇒ false even if the agent flag is on.
	if improvementEnabled(root, "alpha") == false {
		// sanity: with factory on + agent on it IS true; remove the file and re-check
	}
	if err := os.Remove(improvementHookFile(root)); err != nil {
		t.Fatal(err)
	}
	if improvementEnabled(root, "alpha") {
		t.Error("improvementEnabled with absent factory file should be false")
	}
}

// --- CLI-1: status (AC-1) ---

func TestImprovement_StatusDefaultOff(t *testing.T) {
	// Fresh factory: no .improvement-hook AND no agents.json — the first line must
	// still be the stable "improvement hook: off" (status swallows the load error).
	dir := setupTestFactoryForImprovement(t, nil)
	t.Chdir(dir)

	out := captureStdout(t, func() {
		if err := runImprovement(improvementCmd, nil); err != nil {
			t.Fatalf("runImprovement: %v", err)
		}
	})
	first := strings.SplitN(out, "\n", 2)[0]
	if first != "improvement hook: off" {
		t.Errorf("first line = %q, want %q", first, "improvement hook: off")
	}
}

func TestImprovement_StatusOn(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	t.Chdir(dir)
	if err := os.WriteFile(improvementHookFile(dir), []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	out := captureStdout(t, func() {
		if err := runImprovement(improvementCmd, nil); err != nil {
			t.Fatalf("runImprovement: %v", err)
		}
	})
	first := strings.SplitN(out, "\n", 2)[0]
	if first != "improvement hook: on" {
		t.Errorf("first line = %q, want %q", first, "improvement hook: on")
	}
}

// --- CLI-1: factory toggle (AC-2) ---

func TestImprovement_FactoryTurnOn(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, nil)
	t.Chdir(dir)

	_ = captureStdout(t, func() {
		if err := runImprovement(improvementCmd, []string{"on"}); err != nil {
			t.Fatalf("runImprovement on: %v", err)
		}
	})
	data, err := os.ReadFile(improvementHookFile(dir))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("hook file = %q, want %q", string(data), "on\n")
	}
}

func TestImprovement_FactoryTurnOff(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, nil)
	t.Chdir(dir)

	_ = captureStdout(t, func() {
		if err := runImprovement(improvementCmd, []string{"off"}); err != nil {
			t.Fatalf("runImprovement off: %v", err)
		}
	})
	data, err := os.ReadFile(improvementHookFile(dir))
	if err != nil {
		t.Fatalf("read hook file: %v", err)
	}
	if string(data) != "off\n" {
		t.Errorf("hook file = %q, want %q", string(data), "off\n")
	}
}

func TestImprovement_BadArg(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, nil)
	t.Chdir(dir)

	err := runImprovement(improvementCmd, []string{"weird"})
	if err == nil {
		t.Fatal("expected error for bad arg, got nil")
	}
	if !strings.Contains(err.Error(), "usage: af improvement [on|off]") {
		t.Errorf("error %q does not contain expected usage message", err.Error())
	}
}

// --- CLI-1: per-agent writer + validation (AC-3) ---

func TestImprovement_AgentWriterRoundTrip(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, map[string]bool{"design-plan-impl": false})
	t.Chdir(dir)
	setAgentFlag(t, "design-plan-impl")

	_ = captureStdout(t, func() {
		if err := runImprovement(improvementCmd, []string{"on"}); err != nil {
			t.Fatalf("runImprovement on --agent: %v", err)
		}
	})

	cfg, err := config.LoadAgentConfig(config.AgentsConfigPath(dir))
	if err != nil {
		t.Fatalf("reload agents.json: %v", err)
	}
	if !cfg.Agents["design-plan-impl"].ContinuousImprovement {
		t.Error("continuous_improvement not set true after writer")
	}

	// And back off.
	_ = captureStdout(t, func() {
		if err := runImprovement(improvementCmd, []string{"off"}); err != nil {
			t.Fatalf("runImprovement off --agent: %v", err)
		}
	})
	cfg, _ = config.LoadAgentConfig(config.AgentsConfigPath(dir))
	if cfg.Agents["design-plan-impl"].ContinuousImprovement {
		t.Error("continuous_improvement not cleared after off")
	}
}

func TestImprovement_AgentWriterUnknownAgent(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, map[string]bool{"real-agent": false})
	t.Chdir(dir)
	setAgentFlag(t, "no-such-agent")

	err := runImprovement(improvementCmd, []string{"on"})
	if err == nil {
		t.Fatal("expected error for unknown agent, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q does not contain %q", err.Error(), "not found")
	}
	// agents.json must be unchanged (real-agent still false, no orphan entry).
	cfg, _ := config.LoadAgentConfig(config.AgentsConfigPath(dir))
	if _, ok := cfg.Agents["no-such-agent"]; ok {
		t.Error("orphan agent entry was written")
	}
	if cfg.Agents["real-agent"].ContinuousImprovement {
		t.Error("unrelated agent flag was mutated")
	}
}

func TestImprovement_AgentWriterBadName(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, map[string]bool{"real-agent": false})
	t.Chdir(dir)
	setAgentFlag(t, "../evil")

	err := runImprovement(improvementCmd, []string{"on"})
	if err == nil {
		t.Fatal("expected error for bad agent name, got nil")
	}
	// ValidateAgentName runs BEFORE the membership check, so the message is the
	// name-validation one ("invalid agent name"), not "not found".
	if !strings.Contains(err.Error(), "invalid agent name") {
		t.Errorf("error %q should be a ValidateAgentName rejection", err.Error())
	}
}

// --- CLI-1: rich status effective table + pending list (AC-4) ---

func TestImprovement_StatusEffectiveTableAndPending(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true, "beta": false})
	t.Chdir(dir)
	if err := os.WriteFile(improvementHookFile(dir), []byte("on\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// alpha has a pending marker.
	if err := writeImprovementPending(dir, "alpha", "2026-07-06T19:03:51Z"); err != nil {
		t.Fatalf("write pending: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runImprovement(improvementCmd, nil); err != nil {
			t.Fatalf("runImprovement: %v", err)
		}
	})

	if !strings.Contains(out, "continuous-improvement") || !strings.Contains(out, "effective") {
		t.Errorf("status %q missing table header", out)
	}
	// alpha: factory on AND agent on ⇒ fires.
	if !improvementStatusRowSays(out, "alpha", "fires") {
		t.Errorf("status %q: alpha should be 'fires'", out)
	}
	// beta: factory on AND agent off ⇒ skipped.
	if !improvementStatusRowSays(out, "beta", "skipped") {
		t.Errorf("status %q: beta should be 'skipped'", out)
	}
	if !strings.Contains(out, "pending improvement sessions:") {
		t.Errorf("status %q missing pending header", out)
	}
	if !strings.Contains(out, "alpha (fired 2026-07-06T19:03:51Z)") {
		t.Errorf("status %q missing pending alpha row", out)
	}
}

// improvementStatusRowSays reports whether some output line mentions both agent and want.
func improvementStatusRowSays(out, agent, want string) bool {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, agent) && strings.Contains(line, want) {
			return true
		}
	}
	return false
}

// --- ST-1: pending + skip marker helpers ---

func TestImprovementPendingMarker_WriteRead(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})

	if _, ok := readImprovementPending(dir, "alpha"); ok {
		t.Fatal("no marker should exist yet")
	}
	if err := writeImprovementPending(dir, "alpha", "2026-07-07T00:00:00Z"); err != nil {
		t.Fatalf("write pending: %v", err)
	}
	got, ok := readImprovementPending(dir, "alpha")
	if !ok {
		t.Fatal("marker should exist after write")
	}
	if got != "2026-07-07T00:00:00Z" {
		t.Errorf("marker content = %q, want the fired_at value", got)
	}
}

func TestRecordImprovementSkip(t *testing.T) {
	dir := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	if err := recordImprovementSkip(dir, "alpha", "factory hook off"); err != nil {
		t.Fatalf("recordImprovementSkip: %v", err)
	}
	skipFile := filepath.Join(resolveAgentDir(dir, "alpha"), ".runtime", "improvement_skipped")
	data, err := os.ReadFile(skipFile)
	if err != nil {
		t.Fatalf("read skip file: %v", err)
	}
	if !strings.Contains(string(data), "factory hook off") {
		t.Errorf("skip file = %q, want the reason", string(data))
	}
}

// --- CFG-3: applyGate improvement case (AC-6) ---

func TestApplyGate_ImprovementDirectWriteUsesRoot(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	if err := applyGate(root, formulaDir, "improvement", "on"); err != nil {
		t.Fatalf("applyGate improvement on: %v", err)
	}
	data, err := os.ReadFile(improvementHookFile(root))
	if err != nil {
		t.Fatalf("read improvement hook under root: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("improvement hook = %q, want %q", string(data), "on\n")
	}
}

func TestApplyGate_ImprovementNoOpOnSentinels(t *testing.T) {
	root, formulaDir := newGateRoot(t)
	for _, state := range []string{"", "default"} {
		if err := applyGate(root, formulaDir, "improvement", state); err != nil {
			t.Fatalf("applyGate improvement %q: %v", state, err)
		}
	}
	if _, err := os.Stat(improvementHookFile(root)); err == nil {
		t.Error("improvement hook file written for sentinel state")
	}
}

// --- Phase 4: CLI-2 / OBS-1 completion verb (AC-1, AC-2, AC-3) ---

// writeFormulaFile writes a formula.toml under the factory's FormulasDir. valid=true
// writes a minimal workflow (parses + validates); valid=false writes non-TOML garbage
// so formula.ParseFile returns an error (the fail-open validation case). Returns the
// absolute path the completion verb reconstructs against the factory root.
func writeFormulaFile(t *testing.T, root, name string, valid bool) string {
	t.Helper()
	dir := config.FormulasDir(root)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir formulas: %v", err)
	}
	path := filepath.Join(dir, name+".formula.toml")
	content := "formula = \"" + name + "\"\n\n[[steps]]\nid = \"s1\"\n"
	if !valid {
		content = "this is not valid toml {{{ ]]] === \n"
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write formula: %v", err)
	}
	return path
}

// stubTeardownAndMail replaces the teardown and outcome-mail seams with recorders,
// auto-restored via t.Cleanup. Returns a pointer to the teardown counter and the
// captured (recipient, subject, body).
func stubTeardownAndMail(t *testing.T) (teardowns *int, recipient, subject, body *string) {
	t.Helper()
	n := 0
	var rc, sub, bod string
	origFinish := finishDispatchedSessionFn
	finishDispatchedSessionFn = func(cwd, fr string) { n++ }
	origMail := sendImprovementOutcomeMail
	sendImprovementOutcomeMail = func(r, s, b string) error { rc, sub, bod = r, s, b; return nil }
	t.Cleanup(func() {
		finishDispatchedSessionFn = origFinish
		sendImprovementOutcomeMail = origMail
	})
	return &n, &rc, &sub, &bod
}

func TestImprovementComplete_MissingMarker(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	err := runImprovementCompleteCore(agentDir, root, false)
	if err == nil || !strings.Contains(err.Error(), "no pending improvement (missing .runtime/improvement_pending)") {
		t.Fatalf("want missing-marker error idiom, got %v", err)
	}
}

func TestImprovementComplete_ConsumeOnceTerminates(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	writeFormulaFile(t, root, "fx", true)
	teardowns, _, _, _ := stubTeardownAndMail(t)

	m := improvementMarker{Formula: "fx", Caller: "manager", TerminateOnComplete: true, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}

	if err := runImprovementCompleteCore(agentDir, root, false); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	if *teardowns != 1 {
		t.Fatalf("teardowns after first = %d, want 1", *teardowns)
	}
	pending := filepath.Join(agentDir, ".runtime", "improvement_pending")
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("pending marker must be consumed (renamed away), stat err=%v", err)
	}
	if _, err := os.Stat(pending + ".consumed"); err != nil {
		t.Errorf(".consumed marker must exist after consume: %v", err)
	}

	// Second run (the watchdog-vs-agent race loser): no marker → error, NO second teardown.
	err := runImprovementCompleteCore(agentDir, root, false)
	if err == nil || !strings.Contains(err.Error(), "no pending improvement") {
		t.Fatalf("second complete want missing-marker error, got %v", err)
	}
	if *teardowns != 1 {
		t.Fatalf("atomic consume must prevent double teardown: teardowns = %d, want 1", *teardowns)
	}
}

func TestImprovementComplete_NoTerminateLeavesSessionRunning(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	writeFormulaFile(t, root, "fx", true)
	teardowns, _, _, _ := stubTeardownAndMail(t)

	// A lock file present → assert release.
	lockPath := filepath.Join(agentDir, ".runtime", "agent.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(lockPath, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := improvementMarker{Formula: "fx", Caller: "manager", TerminateOnComplete: false, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, root, false); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if *teardowns != 0 {
		t.Errorf("terminate_on_complete=false must NOT tear down, got %d", *teardowns)
	}
	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Errorf("identity lock must be released, stat err=%v", err)
	}
}

func TestImprovementComplete_ValidationFailOpen(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	writeFormulaFile(t, root, "fx", false) // broken TOML
	teardowns, _, subject, body := stubTeardownAndMail(t)

	m := improvementMarker{Formula: "fx", Caller: "manager", TerminateOnComplete: true, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, root, false); err != nil {
		t.Fatalf("broken formula must fail open (exit 0), got err %v", err)
	}
	if !strings.Contains(*subject+*body, "validation FAILED") {
		t.Errorf("verdict must say 'validation FAILED', got subject=%q body=%q", *subject, *body)
	}
	if *teardowns != 1 {
		t.Errorf("fail-open must still tear down, got %d", *teardowns)
	}
}

func TestImprovementComplete_OutcomeChangedUnchanged(t *testing.T) {
	for _, tt := range []struct {
		name        string
		useRealSHA  bool
		wantVerdict string
	}{
		{"unchanged", true, "unchanged"},
		{"changed", false, "changed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
			agentDir := config.AgentDir(root, "alpha")
			absFormula := writeFormulaFile(t, root, "fx", true)
			realSHA, err := formulaSHA256(absFormula)
			if err != nil {
				t.Fatal(err)
			}
			recorded := "0000000000000000000000000000000000000000000000000000000000000000"
			if tt.useRealSHA {
				recorded = realSHA
			}
			_, recipient, subject, _ := stubTeardownAndMail(t)

			m := improvementMarker{Formula: "fx", Caller: "manager", TerminateOnComplete: false, FormulaSHA256: recorded, FiredAt: time.Now().UTC().Format(time.RFC3339)}
			if err := writeImprovementMarker(root, "alpha", m); err != nil {
				t.Fatal(err)
			}
			if err := runImprovementCompleteCore(agentDir, root, false); err != nil {
				t.Fatalf("complete: %v", err)
			}
			if !strings.Contains(*subject, "fx") {
				t.Errorf("outcome mail must name the formula, got %q", *subject)
			}
			if !strings.Contains(*subject, tt.wantVerdict) {
				t.Errorf("want verdict %q in subject, got %q", tt.wantVerdict, *subject)
			}
			if !strings.Contains(*subject, "validation passed") {
				t.Errorf("valid formula must report 'validation passed', got %q", *subject)
			}
			if *recipient != "manager" {
				t.Errorf("recipient = %q, want caller 'manager'", *recipient)
			}
		})
	}
}

func TestImprovementComplete_OutcomeSupervisorFallback(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	writeFormulaFile(t, root, "fx", true)
	_, recipient, _, _ := stubTeardownAndMail(t)

	m := improvementMarker{Formula: "fx", Caller: "", TerminateOnComplete: false, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, root, false); err != nil {
		t.Fatal(err)
	}
	if *recipient != escalationTarget {
		t.Errorf("empty caller must fall back to %q, got %q", escalationTarget, *recipient)
	}
}

func TestImprovementComplete_ReapRelabelsOutcomeMail(t *testing.T) {
	root := setupTestFactoryForImprovement(t, map[string]bool{"alpha": true})
	agentDir := config.AgentDir(root, "alpha")
	writeFormulaFile(t, root, "fx", true)
	_, _, subject, _ := stubTeardownAndMail(t)

	m := improvementMarker{Formula: "fx", Caller: "manager", TerminateOnComplete: true, FiredAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeImprovementMarker(root, "alpha", m); err != nil {
		t.Fatal(err)
	}
	if err := runImprovementCompleteCore(agentDir, root, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(*subject, "IMPROVEMENT_REAPED:") {
		t.Errorf("reap mode must relabel subject IMPROVEMENT_REAPED, got %q", *subject)
	}
}

func TestImprovementComplete_CommandWiring(t *testing.T) {
	sub, _, err := improvementCmd.Find([]string{"complete"})
	if err != nil || sub == nil || sub.Name() != "complete" {
		t.Fatalf("`complete` sub-verb must be registered under improvement: sub=%v err=%v", sub, err)
	}
	if sub.Flags().Lookup("reap") == nil {
		t.Error("complete must expose the --reap flag")
	}
	if sub.Flags().Lookup("dir") == nil {
		t.Error("complete must expose the --dir flag")
	}
}

func TestImprovementComplete_ReapRequiresDir(t *testing.T) {
	if err := improvementCompleteCmd.Flags().Set("reap", "true"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = improvementCompleteCmd.Flags().Set("reap", "false")
		_ = improvementCompleteCmd.Flags().Set("dir", "")
	})
	err := runImprovementComplete(improvementCompleteCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--reap requires --dir") {
		t.Fatalf("--reap without --dir must error, got %v", err)
	}
}
