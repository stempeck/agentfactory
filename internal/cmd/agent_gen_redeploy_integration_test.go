//go:build integration

package cmd

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// extractRegenLoopBlock isolates agent-gen-all.sh's per-formula regenerate loop
// (the marker pair mirrors extractSyncBlock's pattern in
// formula_sync_integration_test.go, which deliberately excludes this exact block —
// issue #527's fix lives inside it, so this test extracts precisely what that one
// omits).
func extractRegenLoopBlock(t *testing.T, repoRoot string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(repoRoot, "agent-gen-all.sh"))
	if err != nil {
		t.Fatalf("reading agent-gen-all.sh: %v", err)
	}
	body := string(data)

	const startMarker = "# --- Regenerate each formula"
	const endMarker = "# --- Rebuild af"

	startIdx := strings.Index(body, startMarker)
	if startIdx == -1 {
		t.Fatal("agent-gen-all.sh missing regen-loop start marker")
	}
	endIdx := strings.Index(body[startIdx:], endMarker)
	if endIdx == -1 {
		t.Fatal("agent-gen-all.sh missing regen-loop end marker")
	}

	block := body[startIdx : startIdx+endIdx]
	return "#!/usr/bin/env bash\nset -euo pipefail\n" + block
}

// runRegenLoopScript runs the extracted regen-loop block against a real `af`
// binary on PATH, with FORMULA_DIR/AF_SRC pointing at a self-contained temp
// factory (never the real repo — both are absolute paths inside t.TempDir()).
func runRegenLoopScript(t *testing.T, script, afBinDir, formulaDir, afSrc string) string {
	t.Helper()
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"PATH="+afBinDir+":"+os.Getenv("PATH"),
		"FORMULA_DIR="+formulaDir,
		"AF_SRC="+afSrc,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("regen loop script failed: %v\nOutput:\n%s", err, out)
	}
	return string(out)
}

// TestAgentGenAllRegenLoop_PreservesOperatorFields pins issue #527, AC1/AC2/AC4:
// after an operator sets every AgentEntry operator-owned field (via the supported
// `af improvement on --agent` CLI for ContinuousImprovement, and direct
// agents.json state for the remaining four, which have no dedicated CLI setter)
// on TWO formula-backed agents, running agent-gen-all.sh's actual regenerate loop
// — the real subprocess sequence `af install --agents` invokes, not an in-process
// Go call — must leave every field intact on every agent the loop processes, and
// `af improvement`'s rendered status (the CLI AC1 names) must report the same
// per-agent state as before the loop ran.
//
// Pre-fix (loop still runs `--delete` before regen, agent-gen-all.sh:141-144):
// every field is expected to zero out — this test is expected to FAIL before the
// fix and PASS after it (Phase 5 RED / Phase 6 GREEN).
func TestAgentGenAllRegenLoop_PreservesOperatorFields(t *testing.T) {
	repoRoot := findRepoRoot(t)
	afBin := buildAF(t)
	afBinDir := filepath.Dir(afBin)

	dir := setupFormulaFactory(t) // ships the "investigate" formula + self-contained AF source (go.mod, internal/templates/roles/)
	formulaDir := config.FormulasDir(dir)

	// AC2 — "every formula-backed agent the redeploy loop processes," not just one.
	secondFormula := `formula = "surveyor"
description = "Survey a codebase area"
type = "workflow"
version = 1

[[steps]]
id = "scan"
title = "Scan"
description = "Scan"
`
	if err := os.WriteFile(filepath.Join(formulaDir, "surveyor.formula.toml"), []byte(secondFormula), 0644); err != nil {
		t.Fatalf("writing second formula: %v", err)
	}

	// First generation, via the real binary — mirrors what a prior `af install --agents`
	// run (or manual agent-gen) would have already produced before this redeploy.
	runAF(t, afBin, dir, "formula", "agent-gen", "investigate", "--af-src", dir)
	runAF(t, afBin, dir, "formula", "agent-gen", "surveyor", "--af-src", dir)

	// AC1 — set continuous_improvement through the supported CLI surface the
	// acceptance criterion names explicitly ("af improvement on --agent <name>").
	runAF(t, afBin, dir, "improvement", "on", "--agent", "investigate")

	// The remaining four operator fields have no dedicated CLI setter in this
	// codebase (confirmed in Phase 2's consumer sweep) — set them directly on
	// agents.json, exactly as TestFormulaAgentGen_PreservesOperatorFields does for
	// the in-place-regen case this test extends to the real redeploy sequence.
	agentsPath := filepath.Join(dir, ".agentfactory", "agents.json")
	cfg, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		t.Fatalf("loading agents.json: %v", err)
	}

	inv := cfg.Agents["investigate"]
	inv.Model = "claude-opus-4"
	inv.SparsePaths = []string{"internal/", "docs/"}
	inv.BaseURL = "http://localhost:1234/v1/messages"
	inv.AuthToken = "sk-operator-secret-investigate"
	cfg.Agents["investigate"] = inv // ContinuousImprovement already set true by the CLI call above

	srv := cfg.Agents["surveyor"]
	srv.Model = "claude-sonnet-5"
	srv.SparsePaths = []string{"web/"}
	srv.BaseURL = "http://localhost:5678/v1/messages"
	srv.AuthToken = "sk-operator-secret-surveyor"
	srv.ContinuousImprovement = true
	cfg.Agents["surveyor"] = srv

	if err := config.SaveAgentConfig(agentsPath, cfg); err != nil {
		t.Fatalf("saving operator fields: %v", err)
	}

	// Blind review (Phase 8) flagged that the pre-fix delete path did not merely
	// clear these 5 fields — it os.RemoveAll'd the entire agent workspace
	// directory, destroying checkpoint/session-recovery state and any other
	// accumulated files on every redeploy. Plant a marker file to prove this
	// larger blast radius is also closed, not just the fields the issue names.
	markerPath := filepath.Join(dir, ".agentfactory", "agents", "investigate", "todos", "marker.txt")
	if err := os.MkdirAll(filepath.Dir(markerPath), 0755); err != nil {
		t.Fatalf("creating marker dir: %v", err)
	}
	if err := os.WriteFile(markerPath, []byte("should survive redeploy"), 0644); err != nil {
		t.Fatalf("writing workspace marker: %v", err)
	}

	// The actual redeploy sequence: agent-gen-all.sh's real regen loop, run as a
	// real bash script against a real `af` binary — not an in-process Go call.
	script := extractRegenLoopBlock(t, repoRoot)
	runRegenLoopScript(t, script, afBinDir, formulaDir, dir)

	if _, err := os.Stat(markerPath); err != nil {
		t.Errorf("workspace marker file did not survive the redeploy loop (%v) — the loop is still wiping agent workspace directories, a larger regression than the 5 named agents.json fields", err)
	}

	reloaded, err := config.LoadAgentConfig(agentsPath)
	if err != nil {
		t.Fatalf("loading agents.json after redeploy: %v", err)
	}

	gotInv := reloaded.Agents["investigate"]
	if gotInv.Model != "claude-opus-4" {
		t.Errorf("investigate.Model = %q, want claude-opus-4 — lost across redeploy", gotInv.Model)
	}
	if len(gotInv.SparsePaths) != 2 || gotInv.SparsePaths[0] != "internal/" || gotInv.SparsePaths[1] != "docs/" {
		t.Errorf("investigate.SparsePaths = %v, want [internal/ docs/] — lost across redeploy", gotInv.SparsePaths)
	}
	if gotInv.BaseURL != "http://localhost:1234/v1/messages" {
		t.Errorf("investigate.BaseURL = %q, want it preserved — lost across redeploy", gotInv.BaseURL)
	}
	if gotInv.AuthToken != "sk-operator-secret-investigate" {
		t.Errorf("investigate.AuthToken = %q, want it preserved — lost across redeploy", gotInv.AuthToken)
	}
	if !gotInv.ContinuousImprovement {
		t.Error("investigate.ContinuousImprovement = false, want true — lost across redeploy")
	}

	gotSrv := reloaded.Agents["surveyor"]
	if gotSrv.Model != "claude-sonnet-5" {
		t.Errorf("surveyor.Model = %q, want claude-sonnet-5 — lost across redeploy", gotSrv.Model)
	}
	if len(gotSrv.SparsePaths) != 1 || gotSrv.SparsePaths[0] != "web/" {
		t.Errorf("surveyor.SparsePaths = %v, want [web/] — lost across redeploy", gotSrv.SparsePaths)
	}
	if gotSrv.BaseURL != "http://localhost:5678/v1/messages" {
		t.Errorf("surveyor.BaseURL = %q, want it preserved — lost across redeploy", gotSrv.BaseURL)
	}
	if gotSrv.AuthToken != "sk-operator-secret-surveyor" {
		t.Errorf("surveyor.AuthToken = %q, want it preserved — lost across redeploy", gotSrv.AuthToken)
	}
	if !gotSrv.ContinuousImprovement {
		t.Error("surveyor.ContinuousImprovement = false, want true — lost across redeploy")
	}

	// AC1, literally: "af improvement reports the same per-agent state ... as
	// before the redeploy" — read it through the real CLI, not just the struct.
	statusOut := runAF(t, afBin, dir, "improvement")
	investigateOnRow := regexp.MustCompile(`(?m)^\s*investigate\s+on\s+`)
	if !investigateOnRow.MatchString(statusOut) {
		t.Errorf("af improvement status does not show investigate as on after redeploy:\n%s", statusOut)
	}
}
