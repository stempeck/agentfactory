//go:build !integration

package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestGateHooks_NoContentionMail(t *testing.T) {
	repoRoot := findRepoRoot(t)
	hooks := []struct {
		path string
		name string
	}{
		{filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"), "fidelity-gate.sh"},
		{filepath.Join(repoRoot, "hooks", "quality-gate.sh"), "quality-gate.sh"},
	}
	for _, h := range hooks {
		data, err := os.ReadFile(h.path)
		if err != nil {
			t.Fatalf("read %s: %v", h.name, err)
		}
		content := string(data)

		if strings.Contains(content, "GATE_LOCK_CONTENTION") {
			t.Errorf("%s: must not contain GATE_LOCK_CONTENTION mail send — lock contention is expected and logged via EXIT4a", h.name)
		}

		if !strings.Contains(content, "EXIT4a: lock_contention") {
			t.Errorf("%s: must preserve EXIT4a debug log line for lock contention observability", h.name)
		}
	}
}

func TestGateHooks_ActionableMailPreserved(t *testing.T) {
	repoRoot := findRepoRoot(t)

	fidelity, err := os.ReadFile(filepath.Join(repoRoot, "hooks", "fidelity-gate.sh"))
	if err != nil {
		t.Fatalf("read fidelity-gate.sh: %v", err)
	}
	for _, subject := range []string{"STEP_FIDELITY", "FIDELITY_ESCALATION"} {
		if !strings.Contains(string(fidelity), subject) {
			t.Errorf("fidelity-gate.sh: must contain actionable mail subject %s", subject)
		}
	}

	quality, err := os.ReadFile(filepath.Join(repoRoot, "hooks", "quality-gate.sh"))
	if err != nil {
		t.Fatalf("read quality-gate.sh: %v", err)
	}
	if !strings.Contains(string(quality), "QUALITY_GATE") {
		t.Errorf("quality-gate.sh: must contain actionable mail subject QUALITY_GATE")
	}
}
