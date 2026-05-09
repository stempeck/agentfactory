//go:build integration

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// TestE2E_FreshInstall_SlingToCompletion drives: install → sling → done loop →
// completion cleanup. Sling runs via subprocess (real CLI bead creation path).
// Done runs via direct runDoneCore calls — subprocess af done requires a tmux
// session (isTestBinary returns false for compiled binaries, so sendWorkDoneMail
// and selfTerminate would fire). Matches TestDone_MultiStepFormula pattern.
func TestE2E_FreshInstall_SlingToCompletion(t *testing.T) {
	requirePython3WithServerDeps(t)

	binary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@e2e.test"},
		{"config", "user.name", "E2E Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	runAF(t, binary, workspace, "install", "--init")

	agentName := "test-e2e-agent"
	agentsPath := filepath.Join(workspace, ".agentfactory", "agents.json")
	agentsJSON := fmt.Sprintf(
		`{"agents":{"manager":{"type":"interactive","description":"manager"},"supervisor":{"type":"autonomous","description":"supervisor"},"%s":{"type":"autonomous","description":"e2e test agent"}}}`,
		agentName,
	)
	if err := os.WriteFile(agentsPath, []byte(agentsJSON), 0o644); err != nil {
		t.Fatalf("writing agents.json: %v", err)
	}

	formulaDir := config.FormulasDir(workspace)
	formulaContent := "formula = \"test-e2e\"\ntype = \"workflow\"\nversion = 1\n\n[[steps]]\nid = \"step1\"\ntitle = \"First step\"\n\n[[steps]]\nid = \"step2\"\ntitle = \"Second step\"\nneeds = [\"step1\"]\n"
	if err := os.WriteFile(filepath.Join(formulaDir, "test-e2e.formula.toml"), []byte(formulaContent), 0o644); err != nil {
		t.Fatalf("writing formula TOML: %v", err)
	}

	agentDir := filepath.Join(workspace, ".agentfactory", "agents", agentName)
	if err := os.MkdirAll(agentDir, 0o755); err != nil {
		t.Fatalf("creating agent dir: %v", err)
	}

	slingOut := runAF(t, binary, agentDir, "sling", "--formula", "test-e2e", "--agent", agentName, "--no-launch", "--var", "issue=test")

	if strings.Contains(slingOut, "doctor --fix") {
		t.Fatalf("sling output contains 'doctor --fix': %s", slingOut)
	}

	hookedPath := filepath.Join(agentDir, ".runtime", "hooked_formula")
	if _, err := os.Stat(hookedPath); err != nil {
		t.Fatalf("hooked_formula should exist after sling: %v", err)
	}

	ctx := t.Context()

	if err := runDoneCore(ctx, agentDir, false, ""); err != nil {
		t.Fatalf("first runDoneCore: %v", err)
	}
	if _, err := os.Stat(hookedPath); err != nil {
		t.Fatalf("hooked_formula should persist after step 1 — step 2 still open: %v", err)
	}

	if err := runDoneCore(ctx, agentDir, false, ""); err != nil {
		t.Fatalf("second runDoneCore: %v", err)
	}
	if _, err := os.Stat(hookedPath); !os.IsNotExist(err) {
		t.Fatalf("hooked_formula should be removed after all steps complete, stat err: %v", err)
	}
}
