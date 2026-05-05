//go:build integration

package cmd

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestInstallInit_CreatesDispatchJson exercises `af install --init` end-to-end,
// which spawns the Python MCP server via mcpstore.New. It is tagged
// `integration` so it does not run under `make test` (no python3 + server
// deps guaranteed). The rest of the install test suite stays in the unit tier
// and tests the filesystem/template paths that do not need the server.
func TestInstallInit_CreatesDispatchJson(t *testing.T) {
	requirePython3WithServerDeps(t)

	dir := t.TempDir()
	ensurePySymlink(t, dir)
	t.Cleanup(func() { terminateMCPServer(dir) })

	// git init — kept for repo-parity. mcpstore does not require git.
	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "test@test.test"},
		{"config", "user.name", "Test"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}

	output, err := runInstallInDir(t, dir, "--init")
	// Reset flag to avoid affecting subsequent tests (cobra doesn't reset bool flags)
	installInitFlag = false
	if err != nil {
		t.Fatalf("install --init failed: %v\nOutput: %s", err, output)
	}

	// Verify dispatch.json was created
	dispatchPath := filepath.Join(dir, ".agentfactory", "dispatch.json")
	data, err := os.ReadFile(dispatchPath)
	if err != nil {
		t.Fatalf("dispatch.json not created: %v", err)
	}

	// Verify valid JSON with expected structure
	var cfg map[string]interface{}
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("dispatch.json is not valid JSON: %v", err)
	}

	if cfg["trigger_label"] != "agentic" {
		t.Errorf("trigger_label should be 'agentic', got: %v", cfg["trigger_label"])
	}
	repos, ok := cfg["repos"].([]interface{})
	if !ok || len(repos) != 0 {
		t.Errorf("repos should be empty array, got: %v", cfg["repos"])
	}
	mappings, ok := cfg["mappings"].([]interface{})
	if !ok || len(mappings) != 0 {
		t.Errorf("mappings should be empty array, got: %v", cfg["mappings"])
	}
	if interval, ok := cfg["interval_seconds"].(float64); !ok || int(interval) != 300 {
		t.Errorf("interval_seconds should be 300, got: %v", cfg["interval_seconds"])
	}
	if retry, ok := cfg["retry_after_seconds"].(float64); !ok || int(retry) != 1800 {
		t.Errorf("retry_after_seconds should be 1800, got: %v", cfg["retry_after_seconds"])
	}
	if notify, ok := cfg["notify_on_complete"].(string); !ok || notify != "manager" {
		t.Errorf("notify_on_complete should be 'manager', got: %v", cfg["notify_on_complete"])
	}
}
