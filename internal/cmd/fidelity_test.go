package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFidelity_OffBlockedDuringFormula(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	writeRuntimeFile(t, dir, "hooked_formula", "bd-test-instance")

	err := runFidelity(fidelityCmd, []string{"off"})
	if err == nil {
		t.Fatal("expected error when hooked_formula exists, got nil")
	}
	if !strings.Contains(err.Error(), "cannot disable fidelity gate") {
		t.Errorf("error %q does not contain expected message", err.Error())
	}

	gateFile := filepath.Join(dir, ".agentfactory", ".fidelity-gate")
	data, err := os.ReadFile(gateFile)
	if err == nil && strings.TrimSpace(string(data)) == "off" {
		t.Error(".fidelity-gate was written to 'off' despite active formula — guard did not block")
	}
}

// setupTestFactoryForFidelity creates a minimal factory layout so
// config.FindFactoryRoot succeeds. Returns the tempdir path.
func setupTestFactoryForFidelity(t *testing.T) string {
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
	return dir
}

func TestFidelity_DefaultOff(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "fidelity gate: off") {
		t.Errorf("output %q does not contain %q", out, "fidelity gate: off")
	}
}

func TestFidelity_OnByDefaultAfterInstall(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	// Simulate what af install --init does: create .fidelity-gate with "on"
	if err := os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644); err != nil {
		t.Fatalf("write .fidelity-gate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q does not contain %q", out, "fidelity gate: on")
	}
}

func TestFidelity_TurnOn(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	_ = captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, []string{"on"}); err != nil {
			t.Fatalf("runFidelity on: %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"))
	if err != nil {
		t.Fatalf("read .fidelity-gate: %v", err)
	}
	if string(data) != "on\n" {
		t.Errorf("file contents = %q, want %q", string(data), "on\n")
	}
}

func TestFidelity_TurnOff(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	_ = captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, []string{"off"}); err != nil {
			t.Fatalf("runFidelity off: %v", err)
		}
	})

	data, err := os.ReadFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"))
	if err != nil {
		t.Fatalf("read .fidelity-gate: %v", err)
	}
	if string(data) != "off\n" {
		t.Errorf("file contents = %q, want %q", string(data), "off\n")
	}
}

func TestFidelity_StatusOnReport(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	if err := os.WriteFile(
		filepath.Join(dir, ".agentfactory", ".fidelity-gate"),
		[]byte("on\n"),
		0o644,
	); err != nil {
		t.Fatalf("pre-write .fidelity-gate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q does not contain %q", out, "fidelity gate: on")
	}
}

func TestFidelity_StatusOffReport(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	if err := os.WriteFile(
		filepath.Join(dir, ".agentfactory", ".fidelity-gate"),
		[]byte("off\n"),
		0o644,
	); err != nil {
		t.Fatalf("pre-write .fidelity-gate: %v", err)
	}

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "fidelity gate: off") {
		t.Errorf("output %q does not contain %q", out, "fidelity gate: off")
	}
}

func TestFidelity_BadArg(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	err := runFidelity(fidelityCmd, []string{"weird"})
	if err == nil {
		t.Fatal("expected error for bad arg, got nil")
	}
	if !strings.Contains(err.Error(), "usage") {
		t.Errorf("error %q does not contain %q", err.Error(), "usage")
	}
}

func TestFidelity_StatusWithStaleLock(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644)

	writeRuntimeFile(t, dir, "fidelity-gate.lock",
		`{"pid":99999999,"acquired_at":"2026-01-01T00:00:00Z","session_id":"dead-session"}`)

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if !strings.Contains(out, "WARNING") {
		t.Errorf("output %q should contain WARNING for stale lock", out)
	}
	if !strings.Contains(out, "99999999") {
		t.Errorf("output %q should contain the dead PID", out)
	}
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q should still show gate on", out)
	}
}

func TestFidelity_StatusOnCleanNoWarning(t *testing.T) {
	dir := setupTestFactoryForFidelity(t)
	t.Chdir(dir)

	os.WriteFile(filepath.Join(dir, ".agentfactory", ".fidelity-gate"), []byte("on\n"), 0o644)

	out := captureStdout(t, func() {
		if err := runFidelity(fidelityCmd, nil); err != nil {
			t.Fatalf("runFidelity: %v", err)
		}
	})
	if strings.Contains(out, "WARNING") {
		t.Errorf("output %q should NOT contain WARNING when no stale lock", out)
	}
	if !strings.Contains(out, "fidelity gate: on") {
		t.Errorf("output %q should contain clean on status", out)
	}
}
