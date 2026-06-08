package cmd

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompactHandoff_OutsideTmuxReturnsNil(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	dir := setupTestFactoryForDone(t, "manager")
	err := runCompactHandoffCore(t.Context(), filepath.Join(dir, ".agentfactory", "agents", "manager"), false)
	if err != nil {
		t.Fatalf("expected nil when not inside tmux, got: %v", err)
	}
}

func TestCompactHandoff_NoTmuxPaneReturnsNil(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "")

	dir := setupTestFactoryForDone(t, "manager")
	err := runCompactHandoffCore(t.Context(), filepath.Join(dir, ".agentfactory", "agents", "manager"), false)
	if err != nil {
		t.Fatalf("expected nil when TMUX_PANE is empty, got: %v", err)
	}
}

func TestCompactHandoff_InteractiveModeSkipsRespawn(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	err := runCompactHandoffCore(t.Context(), filepath.Join(dir, ".agentfactory", "agents", "manager"), true)
	if err != nil {
		t.Fatalf("expected nil in interactive mode, got: %v", err)
	}
}

func TestCompactHandoff_RateLimiterIncrements(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	counter := compactHandoffCounter{Count: 1, FirstAt: time.Now(), LastAt: time.Now()}
	data, _ := json.MarshalIndent(counter, "", "  ")
	if err := os.WriteFile(filepath.Join(runtimeDir, compactHandoffCountFile), data, 0o644); err != nil {
		t.Fatal(err)
	}

	checkCompactHandoffRate(dir, "test-agent")

	raw, err := os.ReadFile(filepath.Join(runtimeDir, compactHandoffCountFile))
	if err != nil {
		t.Fatal(err)
	}
	var result compactHandoffCounter
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 2 {
		t.Errorf("count should be incremented to 2, got %d", result.Count)
	}
}

func TestCompactHandoff_RateLimiterResetsAfterWindow(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	oldTime := time.Now().Add(-compactHandoffWindow - time.Minute)
	counter := compactHandoffCounter{Count: 3, FirstAt: oldTime, LastAt: oldTime}
	data, _ := json.MarshalIndent(counter, "", "  ")
	if err := os.WriteFile(filepath.Join(runtimeDir, compactHandoffCountFile), data, 0o644); err != nil {
		t.Fatal(err)
	}

	checkCompactHandoffRate(dir, "test-agent")

	raw, err := os.ReadFile(filepath.Join(runtimeDir, compactHandoffCountFile))
	if err != nil {
		t.Fatal(err)
	}
	var result compactHandoffCounter
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != 1 {
		t.Errorf("count should reset to 1 after window expires, got %d", result.Count)
	}
}

func TestCompactHandoff_RateLimiterEscalatesAboveThreshold(t *testing.T) {
	dir := t.TempDir()
	runtimeDir := filepath.Join(dir, ".runtime")
	if err := os.MkdirAll(runtimeDir, 0o755); err != nil {
		t.Fatal(err)
	}

	counter := compactHandoffCounter{Count: compactHandoffThreshold, FirstAt: time.Now(), LastAt: time.Now()}
	data, _ := json.MarshalIndent(counter, "", "  ")
	if err := os.WriteFile(filepath.Join(runtimeDir, compactHandoffCountFile), data, 0o644); err != nil {
		t.Fatal(err)
	}

	checkCompactHandoffRate(dir, "test-agent")

	raw, err := os.ReadFile(filepath.Join(runtimeDir, compactHandoffCountFile))
	if err != nil {
		t.Fatal(err)
	}
	var result compactHandoffCounter
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	if result.Count != compactHandoffThreshold+1 {
		t.Errorf("count should be %d, got %d", compactHandoffThreshold+1, result.Count)
	}
}

func TestCompactHandoff_NoFactoryRootReturnsNil(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := t.TempDir()
	err := runCompactHandoffCore(t.Context(), dir, false)
	if err != nil {
		t.Fatalf("compact-handoff should return nil when factory root not found (ADR-007), got: %v", err)
	}
}

func TestCompactHandoff_NoAgentDetectionReturnsNil(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")
	t.Setenv("AF_ROLE", "")

	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, ".agentfactory"), 0o755)
	os.WriteFile(filepath.Join(dir, ".agentfactory", "factory.json"), []byte(`{"name":"test"}`), 0o644)

	err := runCompactHandoffCore(t.Context(), dir, false)
	if err != nil {
		t.Fatalf("compact-handoff should return nil when agent detection fails (ADR-007), got: %v", err)
	}
}

func TestCompactHandoff_FlagRegistration(t *testing.T) {
	if compactHandoffCmd.Flags().Lookup("interactive") == nil {
		t.Error("compact-handoff command should have --interactive flag")
	}
}

func TestCompactHandoff_WritesCheckpoint(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	_ = runCompactHandoffCore(t.Context(), workDir, false)

	cpPath := filepath.Join(workDir, ".agent-checkpoint.json")
	if _, err := os.Stat(cpPath); err != nil {
		t.Fatalf("checkpoint should exist after compact-handoff: %v", err)
	}

	data, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "COMPACTION") {
		t.Error("checkpoint should contain COMPACTION marker")
	}
}
