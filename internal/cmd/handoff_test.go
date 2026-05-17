package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandoff_NotInsideTmux(t *testing.T) {
	t.Setenv("TMUX", "")
	t.Setenv("TMUX_PANE", "")

	dir := setupTestFactoryForDone(t, "manager")
	err := runHandoffCore(t.Context(), filepath.Join(dir, ".agentfactory", "agents", "manager"), "test", "msg", false, false, false)
	if err == nil {
		t.Fatal("expected error when not inside tmux")
	}
	if !strings.Contains(err.Error(), "tmux") {
		t.Errorf("error should mention tmux, got: %v", err)
	}
}

func TestHandoff_NoTmuxPane(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "")

	dir := setupTestFactoryForDone(t, "manager")
	err := runHandoffCore(t.Context(), filepath.Join(dir, ".agentfactory", "agents", "manager"), "test", "msg", false, false, false)
	if err == nil {
		t.Fatal("expected error when TMUX_PANE not set")
	}
	if !strings.Contains(err.Error(), "TMUX_PANE") {
		t.Errorf("error should mention TMUX_PANE, got: %v", err)
	}
}

func TestHandoff_DryRun(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	err := runHandoffCore(t.Context(), workDir, "HANDOFF: test", "Test message", false, false, true)
	if err != nil {
		t.Fatalf("dry-run should not error, got: %v", err)
	}

	// Verify no checkpoint was written
	cpPath := filepath.Join(workDir, ".agent-checkpoint.json")
	if _, err := os.Stat(cpPath); !os.IsNotExist(err) {
		t.Error("dry-run should not write checkpoint")
	}
}

func TestHandoff_WritesCheckpoint(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	// runHandoffCore will write checkpoint then fail at tmux/mail commands — that's expected.
	// We just verify the checkpoint was written.
	_ = runHandoffCore(t.Context(), workDir, "Test handoff subject", "Test message", false, false, false)

	cpPath := filepath.Join(workDir, ".agent-checkpoint.json")
	if _, err := os.Stat(cpPath); err != nil {
		t.Fatalf("checkpoint should exist after handoff: %v", err)
	}

	data, err := os.ReadFile(cpPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Test handoff subject") {
		t.Error("checkpoint should contain the handoff subject in Notes")
	}
}

func TestHandoff_PreservesRuntimeFiles(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	writeRuntimeFile(t, workDir, "hooked_formula", "bd-instance-1")
	writeRuntimeFile(t, workDir, "formula_caller", "supervisor")

	// Run handoff — will fail at tmux/mail, but runtime files must survive
	_ = runHandoffCore(t.Context(), workDir, "test", "msg", false, false, false)

	if _, err := os.Stat(filepath.Join(workDir, ".runtime", "hooked_formula")); err != nil {
		t.Error("hooked_formula should be preserved after handoff")
	}
	if _, err := os.Stat(filepath.Join(workDir, ".runtime", "formula_caller")); err != nil {
		t.Error("formula_caller should be preserved after handoff")
	}
}

func TestHandoff_MailSkipUnderTest(t *testing.T) {
	err := sendHandoffMail("manager", "HANDOFF: test", "body")
	if err != nil {
		t.Errorf("sendHandoffMail should return nil under go test, got: %v", err)
	}
}

func TestHandoff_FlagRegistration(t *testing.T) {
	flags := []string{"subject", "message", "collect", "dry-run", "idle"}
	for _, name := range flags {
		if handoffCmd.Flags().Lookup(name) == nil {
			t.Errorf("handoff command should have --%s flag", name)
		}
	}
}

func TestHandoff_IdleIncrementsCounter(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	writeRuntimeFile(t, workDir, "idle_cycles", "2")

	// Run handoff with idle=true — will fail at tmux/mail, but .runtime/idle_cycles should be incremented
	_ = runHandoffCore(t.Context(), workDir, "test", "msg", false, true, false)

	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "idle_cycles"))
	if err != nil {
		t.Fatalf("idle_cycles should exist after idle handoff: %v", err)
	}
	if strings.TrimSpace(string(data)) != "3" {
		t.Errorf("idle_cycles should be incremented to 3, got: %q", strings.TrimSpace(string(data)))
	}
}

func TestHandoff_IdleFirstCycleCreatesCounter(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	// No idle_cycles file exists initially
	_ = runHandoffCore(t.Context(), workDir, "test", "msg", false, true, false)

	data, err := os.ReadFile(filepath.Join(workDir, ".runtime", "idle_cycles"))
	if err != nil {
		t.Fatalf("idle_cycles should be created on first idle handoff: %v", err)
	}
	if strings.TrimSpace(string(data)) != "1" {
		t.Errorf("idle_cycles should be 1 on first idle cycle, got: %q", strings.TrimSpace(string(data)))
	}
}

func TestHandoff_NonIdleResetsCounter(t *testing.T) {
	t.Setenv("TMUX", "/tmp/tmux-501/default,12345,0")
	t.Setenv("TMUX_PANE", "%0")

	dir := setupTestFactoryForDone(t, "manager")
	workDir := filepath.Join(dir, ".agentfactory", "agents", "manager")

	writeRuntimeFile(t, workDir, "idle_cycles", "5")

	// Run handoff without idle flag — counter should be reset
	_ = runHandoffCore(t.Context(), workDir, "test", "msg", false, false, false)

	_, err := os.Stat(filepath.Join(workDir, ".runtime", "idle_cycles"))
	if err == nil {
		t.Error("idle_cycles should be removed after productive (non-idle) handoff")
	}
}

func TestHandoff_IdleDelayComputation(t *testing.T) {
	tests := []struct {
		cycles   int
		expected int // seconds
	}{
		{1, 60},
		{2, 120},
		{5, 300},
		{10, 600},
		{30, 1800},
		{31, 1800}, // capped
		{100, 1800},
	}

	for _, tt := range tests {
		delay := idleBackoffSeconds(tt.cycles)
		if delay != tt.expected {
			t.Errorf("idleBackoffSeconds(%d) = %d, want %d", tt.cycles, delay, tt.expected)
		}
	}
}
