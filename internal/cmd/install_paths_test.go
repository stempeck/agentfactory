package cmd

import (
	"os"
	"strings"
	"testing"
)

func TestInstallInit_NoHardcodedHooksPath(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	content := string(data)
	if strings.Contains(content, `hooksDir := filepath.Join(cwd, "hooks")`) {
		t.Error("install.go still assigns hooksDir via hardcoded filepath.Join(cwd, \"hooks\") — should use config.HooksDir(cwd)")
	}
}

func TestInstallInit_NoHardcodedFidelityTogglePath(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	content := string(data)
	if strings.Contains(content, `filepath.Join(cwd, ".fidelity-gate")`) {
		t.Error("install.go still uses hardcoded filepath.Join(cwd, \".fidelity-gate\") — should use filepath.Join(configDir, \".fidelity-gate\")")
	}
}

func TestInstallInit_UsesConfigHooksDir(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, "config.HooksDir") {
		t.Error("install.go does not call config.HooksDir — hooks directory should use the centralized path helper")
	}
}

func TestInstallInit_HasAgentReProvisioning(t *testing.T) {
	data, err := os.ReadFile("install.go")
	if err != nil {
		t.Fatalf("read install.go: %v", err)
	}
	content := string(data)
	idx := strings.Index(content, "func runInstallInit")
	if idx < 0 {
		t.Fatal("runInstallInit function not found")
	}
	initBody := content[idx:]
	nextFunc := strings.Index(initBody[1:], "\nfunc ")
	if nextFunc > 0 {
		initBody = initBody[:nextFunc+1]
	}
	if !strings.Contains(initBody, "reprovisionAgentSettings") {
		t.Error("runInstallInit does not call reprovisionAgentSettings — agent settings must be re-provisioned during init")
	}
	if !strings.Contains(content, "func reprovisionAgentSettings") {
		t.Error("reprovisionAgentSettings function not found — agent settings re-provisioning must be implemented")
	}
	if !strings.Contains(content, "EnsureSettings") {
		t.Error("install.go does not call EnsureSettings anywhere — reprovisionAgentSettings must call it")
	}
}
