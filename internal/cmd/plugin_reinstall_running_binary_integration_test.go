//go:build integration

package cmd

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stempeck/agentfactory/internal/config"
)

// PR #539 T1/T6: af plugin install run AS ~/.local/bin/af must survive make install's
// plain `cp` over its own inode (ETXTBSY without the relink).
func TestPluginInstallBehavioral_ReinstallsTheRunningBinary(t *testing.T) {
	requirePython3WithServerDeps(t)
	for _, tool := range []string{"git", "tar", "make"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s not available", tool)
		}
	}

	seedBinary := buildAF(t)
	workspace := t.TempDir()
	ensurePySymlink(t, workspace)
	t.Cleanup(func() { terminateMCPServer(workspace) })

	for _, args := range [][]string{
		{"init", "-q"},
		{"config", "user.email", "t@e2e.test"},
		{"config", "user.name", "E2E"},
	} {
		c := exec.Command("git", args...)
		c.Dir = workspace
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}
	runAF(t, seedBinary, workspace, "install", "--init")
	for _, args := range [][]string{{"add", "-A"}, {"commit", "-q", "-m", "init factory"}} {
		c := exec.Command("git", args...)
		c.Dir = workspace
		if out, err := c.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %s\n%s", strings.Join(args, " "), err, out)
		}
	}
	storeDir := config.FormulasDir(workspace)
	if entries, err := os.ReadDir(storeDir); err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".formula.toml") {
				_ = os.Remove(filepath.Join(storeDir, e.Name()))
			}
		}
	}

	afSrc := t.TempDir()
	behavioralArchiveInto(t, afSrc)
	behavioralTrimInstallFormulas(t, afSrc)
	behavioralWriteFile(t, filepath.Join(afSrc, "quickstart.sh"), "#!/usr/bin/env bash\nexit 0\n", 0o755)
	// Mirrors the real Makefile: a plain cp over ~/.local/bin/af.
	behavioralWriteFile(t, filepath.Join(afSrc, "Makefile"),
		"install:\n\tmkdir -p $(HOME)/.local/bin\n\tCGO_ENABLED=0 go build -o $(HOME)/.af-side-build ./cmd/af\n\tcp $(HOME)/.af-side-build $(HOME)/.local/bin/af\n", 0o644)

	binDir := filepath.Join(workspace, ".local", "bin")
	installedAF := filepath.Join(binDir, "af")
	seedBytes, err := os.ReadFile(seedBinary)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(installedAF, seedBytes, 0o755); err != nil {
		t.Fatal(err)
	}

	const pluginName, agentName = "acmeplugin", "acme-triage"
	writePluginFixture(t, workspace, pluginName, map[string]string{
		agentName + ".formula.toml": validPluginFormula(agentName),
	})

	env := append(os.Environ(),
		"HOME="+workspace,
		"AF_SOURCE_ROOT="+afSrc,
		"GOMODCACHE="+behavioralGoEnv(t, "GOMODCACHE"),
		"GOCACHE="+behavioralGoEnv(t, "GOCACHE"),
		"PATH="+binDir+":"+os.Getenv("PATH"),
	)

	installOut, err := behavioralRunAF(t, installedAF, workspace, env, "plugin", "install", pluginName)
	if err != nil {
		t.Fatalf("af plugin install run as %s failed (Text file busy => no relink before make install): %v\n%s", installedAF, err, installOut)
	}
	if !strings.Contains(installOut, "plugin install verified") {
		t.Fatalf("plugin install did not report verification:\n%s", installOut)
	}
	after, err := os.ReadFile(installedAF)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(after, seedBytes) {
		t.Error("the reinstall did not land: ~/.local/bin/af is still the seed binary")
	}
	leftovers, _ := filepath.Glob(filepath.Join(binDir, ".af-selfexec-*"))
	if len(leftovers) != 0 {
		t.Errorf("relink self-copy not cleaned up: %v", leftovers)
	}
}
