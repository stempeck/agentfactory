//go:build !integration

package cmd

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stempeck/agentfactory/internal/lock"
)

func setupGateLockTestEnv(t *testing.T) string {
	t.Helper()
	workDir := t.TempDir()

	afDir := filepath.Join(workDir, ".agentfactory")
	os.MkdirAll(afDir, 0755)
	os.WriteFile(filepath.Join(afDir, "factory.json"), []byte(`{}`), 0644)

	os.WriteFile(filepath.Join(afDir, ".fidelity-gate"), []byte("on\n"), 0644)
	os.WriteFile(filepath.Join(afDir, ".quality-gate"), []byte("on\n"), 0644)

	os.MkdirAll(filepath.Join(workDir, ".runtime"), 0755)

	return workDir
}

func runHookWithEnv(t *testing.T, scriptPath, workDir string, inputJSON []byte) (string, int) {
	t.Helper()
	cmd := exec.Command("bash", scriptPath)
	cmd.Stdin = bytes.NewReader(inputJSON)
	cmd.Dir = workDir
	cmd.Env = append(os.Environ(),
		"AF_ROOT="+workDir,
		"PATH="+os.Getenv("PATH"),
	)
	out, err := cmd.CombinedOutput()
	exitCode := 0
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			exitCode = exitErr.ExitCode()
		}
	}
	return string(out), exitCode
}

func TestGateLockFile_StaleRecovery(t *testing.T) {
	repoRoot := findRepoRoot(t)
	inputJSON := []byte(`{"stop_hook_active": false, "last_assistant_message": "test message for lock verification"}`)

	hooks := []struct {
		name     string
		script   string
		lockFile string
		debugLog string
	}{
		{"fidelity", "fidelity-gate.sh", "fidelity-gate.lock", "fidelity_debug.log"},
		{"quality", "quality-gate.sh", "quality-gate.lock", "quality_debug.log"},
	}

	for _, hook := range hooks {
		t.Run(hook.name, func(t *testing.T) {
			workDir := setupGateLockTestEnv(t)

			lockPath := filepath.Join(workDir, ".runtime", hook.lockFile)
			os.WriteFile(lockPath, []byte(`{"pid": 99999999}`), 0644)

			scriptPath := filepath.Join(repoRoot, "hooks", hook.script)
			out, exitCode := runHookWithEnv(t, scriptPath, workDir, inputJSON)

			if exitCode != 0 {
				t.Fatalf("expected exit 0, got %d; output: %s", exitCode, out)
			}
			if !strings.Contains(out, `{"ok": true}`) {
				t.Fatalf("expected {\"ok\": true} in output, got: %s", out)
			}

			debugLogPath := filepath.Join(workDir, ".runtime", hook.debugLog)
			debugContent, err := os.ReadFile(debugLogPath)
			if err != nil {
				t.Fatalf("failed to read debug log %s: %v", hook.debugLog, err)
			}

			if !strings.Contains(string(debugContent), "EXIT4b: stale_lock_recovered") {
				t.Errorf("debug log should contain EXIT4b: stale_lock_recovered, got: %s", debugContent)
			}
			if strings.Contains(string(debugContent), "EXIT4a: lock_contention") {
				t.Errorf("debug log should NOT contain EXIT4a: lock_contention, got: %s", debugContent)
			}
		})
	}
}

func TestGateLockFile_LivePIDBlocks(t *testing.T) {
	repoRoot := findRepoRoot(t)
	inputJSON := []byte(`{"stop_hook_active": false, "last_assistant_message": "test message for lock verification"}`)

	hooks := []struct {
		name     string
		script   string
		lockFile string
		debugLog string
	}{
		{"fidelity", "fidelity-gate.sh", "fidelity-gate.lock", "fidelity_debug.log"},
		{"quality", "quality-gate.sh", "quality-gate.lock", "quality_debug.log"},
	}

	for _, hook := range hooks {
		t.Run(hook.name, func(t *testing.T) {
			workDir := setupGateLockTestEnv(t)

			lockPath := filepath.Join(workDir, ".runtime", hook.lockFile)
			lockData := fmt.Sprintf(`{"pid": %d}`, os.Getpid())
			os.WriteFile(lockPath, []byte(lockData), 0644)

			scriptPath := filepath.Join(repoRoot, "hooks", hook.script)
			out, exitCode := runHookWithEnv(t, scriptPath, workDir, inputJSON)

			if exitCode != 0 {
				t.Fatalf("expected exit 0, got %d; output: %s", exitCode, out)
			}
			if !strings.Contains(out, `{"ok": true}`) {
				t.Fatalf("expected {\"ok\": true} in output, got: %s", out)
			}

			debugLogPath := filepath.Join(workDir, ".runtime", hook.debugLog)
			debugContent, err := os.ReadFile(debugLogPath)
			if err != nil {
				t.Fatalf("failed to read debug log %s: %v", hook.debugLog, err)
			}

			if !strings.Contains(string(debugContent), "EXIT4a: lock_contention") {
				t.Errorf("debug log should contain EXIT4a: lock_contention, got: %s", debugContent)
			}
			if strings.Contains(string(debugContent), "EXIT4b: stale_lock_recovered") {
				t.Errorf("debug log should NOT contain EXIT4b: stale_lock_recovered, got: %s", debugContent)
			}

			afterLockData, err := os.ReadFile(lockPath)
			if err != nil {
				t.Fatalf("lock file should still exist after contention, got error: %v", err)
			}
			if string(afterLockData) != lockData {
				t.Errorf("lock file should be unchanged; expected %s, got %s", lockData, afterLockData)
			}
		})
	}
}

func TestGateLockFile_Cleanup(t *testing.T) {
	repoRoot := findRepoRoot(t)
	inputJSON := []byte(`{"stop_hook_active": false, "last_assistant_message": "test message for lock verification"}`)

	hooks := []struct {
		name     string
		script   string
		lockFile string
	}{
		{"fidelity", "fidelity-gate.sh", "fidelity-gate.lock"},
		{"quality", "quality-gate.sh", "quality-gate.lock"},
	}

	for _, hook := range hooks {
		t.Run(hook.name, func(t *testing.T) {
			workDir := setupGateLockTestEnv(t)

			lockPath := filepath.Join(workDir, ".runtime", hook.lockFile)

			scriptPath := filepath.Join(repoRoot, "hooks", hook.script)
			_, exitCode := runHookWithEnv(t, scriptPath, workDir, inputJSON)

			if exitCode != 0 {
				t.Fatalf("expected exit 0, got %d", exitCode)
			}

			if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
				t.Errorf("lock file %s should not exist after script exits (trap should remove it)", hook.lockFile)
			}
		})
	}
}

func TestGateLockFile_GoParseability(t *testing.T) {
	lockPath := filepath.Join(t.TempDir(), "gate.lock")
	os.WriteFile(lockPath, []byte(`{"pid": 12345}`), 0644)

	info, err := lock.NewWithPath(lockPath).Read()
	if err != nil {
		t.Fatalf("lock.Read() failed on shell-format JSON: %v", err)
	}

	if info.PID != 12345 {
		t.Errorf("PID = %d, want 12345", info.PID)
	}
	if info.AcquiredAt != (time.Time{}) {
		t.Errorf("AcquiredAt = %v, want zero time.Time", info.AcquiredAt)
	}
	if info.SessionID != "" {
		t.Errorf("SessionID = %q, want empty string", info.SessionID)
	}
	if info.Hostname != "" {
		t.Errorf("Hostname = %q, want empty string", info.Hostname)
	}
}

func TestGateHookMailSubjects(t *testing.T) {
	root := findRepoRoot(t)

	fidelityData, err := os.ReadFile(filepath.Join(root, "hooks", "fidelity-gate.sh"))
	if err != nil {
		t.Fatalf("read fidelity-gate.sh: %v", err)
	}
	qualityData, err := os.ReadFile(filepath.Join(root, "hooks", "quality-gate.sh"))
	if err != nil {
		t.Fatalf("read quality-gate.sh: %v", err)
	}

	fidelity := string(fidelityData)
	quality := string(qualityData)

	if strings.Contains(fidelity, "GATE_LOCK_CONTENTION") {
		t.Error("fidelity-gate.sh still contains GATE_LOCK_CONTENTION mail send")
	}
	if strings.Contains(quality, "GATE_LOCK_CONTENTION") {
		t.Error("quality-gate.sh still contains GATE_LOCK_CONTENTION mail send")
	}

	if !strings.Contains(fidelity, "STEP_FIDELITY") {
		t.Error("fidelity-gate.sh missing STEP_FIDELITY mail send")
	}
	if !strings.Contains(fidelity, "FIDELITY_ESCALATION") {
		t.Error("fidelity-gate.sh missing FIDELITY_ESCALATION mail send")
	}
	if !strings.Contains(quality, "QUALITY_GATE") {
		t.Error("quality-gate.sh missing QUALITY_GATE mail send")
	}
}
