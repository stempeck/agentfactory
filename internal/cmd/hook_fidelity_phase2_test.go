//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFidelityGate_AfDoneTurnDetection(t *testing.T) {
	repoRoot := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"))
	if err != nil {
		t.Fatalf("read fidelity-gate.sh: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "IS_AF_DONE_TURN") {
		t.Error("fidelity-gate.sh must contain IS_AF_DONE_TURN variable for af done turn detection")
	}
	if !strings.Contains(content, "last_closed_step") {
		t.Error("fidelity-gate.sh must reference last_closed_step file for af done turn detection")
	}
	if !strings.Contains(content, "stat -c %Y") {
		t.Error("fidelity-gate.sh must use stat -c %%Y for file age calculation")
	}
}

func TestFidelityGate_StateCheckBypass(t *testing.T) {
	repoRoot := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"))
	if err != nil {
		t.Fatalf("read fidelity-gate.sh: %v", err)
	}
	content := string(data)

	stateCheckIdx := strings.Index(content, `.state // "error"`)
	if stateCheckIdx < 0 {
		t.Fatal("fidelity-gate.sh must contain .state check")
	}

	doneGuardBeforeState := strings.LastIndex(content[:stateCheckIdx], "IS_AF_DONE_TURN")
	if doneGuardBeforeState < 0 {
		t.Error("state check must be guarded by IS_AF_DONE_TURN to bypass on af done turns (last_closed_step has no .state field)")
	}
}

func TestFidelityGate_EvalCountNoResetGuard(t *testing.T) {
	repoRoot := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"))
	if err != nil {
		t.Fatalf("read fidelity-gate.sh: %v", err)
	}
	content := string(data)

	resetIdx := strings.Index(content, `echo 0 > "$EVAL_COUNT_FILE"`)
	if resetIdx < 0 {
		t.Fatal("fidelity-gate.sh must contain eval count reset logic")
	}

	doneGuardBeforeReset := strings.LastIndex(content[:resetIdx], "IS_AF_DONE_TURN")
	if doneGuardBeforeReset < 0 {
		t.Error("eval count reset must be guarded by IS_AF_DONE_TURN to prevent spurious reset on af done turns")
	}
}

func TestFidelityGate_VelocityUpdate(t *testing.T) {
	repoRoot := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"))
	if err != nil {
		t.Fatalf("read fidelity-gate.sh: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "done_velocity") {
		t.Error("fidelity-gate.sh must reference done_velocity file for last_eval_between update")
	}
	if !strings.Contains(content, "last_eval_between") {
		t.Error("fidelity-gate.sh must update last_eval_between timestamp in done_velocity")
	}
}

func TestFidelityGate_DebugLogExitPaths(t *testing.T) {
	repoRoot := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"))
	if err != nil {
		t.Fatalf("read fidelity-gate.sh: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "fidelity_debug.log") {
		t.Error("fidelity-gate.sh must reference fidelity_debug.log for debug logging")
	}

	exitLabels := []string{
		"EXIT1: no_factory_root",
		"EXIT2: gate_disabled",
		"EXIT3: recursion_guard",
		"EXIT4a: lock_contention",
		"EXIT4b: stale_lock_recovered",
		"EXIT5: no_message",
		"EXIT6: no_af_binary",
		"EXIT7: step_not_ready",
		"EXIT8: no_claude_binary",
		"EXIT9: normal_completion",
	}
	for _, label := range exitLabels {
		if !strings.Contains(content, label) {
			t.Errorf("fidelity-gate.sh must contain debug label %q", label)
		}
	}

	exit1Idx := strings.Index(content, "EXIT1: no_factory_root")
	if exit1Idx >= 0 {
		before := content[:exit1Idx]
		if !strings.Contains(before[strings.LastIndex(before, "\n"):], "[ -d \"$AGENT_RUNTIME\" ]") {
			lineStart := strings.LastIndex(before, "\n")
			line := content[lineStart : exit1Idx+len("EXIT1: no_factory_root")+50]
			if !strings.Contains(line, "[ -d \"$AGENT_RUNTIME\" ]") {
				t.Error("EXIT1 debug log must be guarded by [ -d \"$AGENT_RUNTIME\" ]")
			}
		}
	}
}

func TestFidelityPrompt_ArtifactCopyPrinciple(t *testing.T) {
	repoRoot := findRepoRoot(t)
	data, err := os.ReadFile(filepath.Join(repoRoot, "hooks", "fidelity-gate-prompt.txt"))
	if err != nil {
		t.Fatalf("read fidelity-gate-prompt.txt: %v", err)
	}
	content := string(data)

	if !strings.Contains(content, "ARTIFACT COPYING") {
		t.Error("fidelity-gate-prompt.txt must contain principle 7 'NO ARTIFACT COPYING'")
	}
	if !strings.Contains(content, "recycled") {
		t.Error("fidelity-gate-prompt.txt principle 7 must mention 'recycled' artifacts")
	}

	if !strings.Contains(content, "1. ON-CONTRACT WORK") {
		t.Error("fidelity-gate-prompt.txt must still contain principle 1")
	}
	if !strings.Contains(content, "6. Only flag material violations") {
		t.Error("fidelity-gate-prompt.txt must still contain principle 6")
	}
}
