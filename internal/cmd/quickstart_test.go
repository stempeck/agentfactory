package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func extractShellFunction(content, funcName string) string {
	marker := funcName + "() {"
	start := strings.Index(content, marker)
	if start == -1 {
		return ""
	}
	depth := 0
	inBody := false
	for i := start; i < len(content); i++ {
		if content[i] == '{' {
			depth++
			inBody = true
		} else if content[i] == '}' {
			depth--
			if inBody && depth == 0 {
				return content[start : i+1]
			}
		}
	}
	return content[start:]
}

func TestQuickstartSupplyChainInvariants(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	installClaude := extractShellFunction(content, "install_claude")
	if installClaude == "" {
		t.Fatal("could not extract install_claude() function body")
	}

	configureShell := extractShellFunction(content, "configure_shell")
	if configureShell == "" {
		t.Fatal("could not extract configure_shell() function body")
	}

	t.Run("uses_official_installer", func(t *testing.T) {
		re := regexp.MustCompile(`curl.*https://claude\.ai/install\.sh.*\|.*bash`)
		if !re.MatchString(installClaude) {
			t.Error("install_claude() must use official curl|bash installer as primary method")
		}
	})

	t.Run("has_npm_fallback", func(t *testing.T) {
		if !strings.Contains(installClaude, "npm install -g @anthropic-ai/claude-code") {
			t.Error("install_claude() must have npm global install as fallback")
		}
	})

	t.Run("has_sudo_npm_sub_fallback", func(t *testing.T) {
		re := regexp.MustCompile(`sudo\s+npm\s+install\s+-g`)
		if !re.MatchString(installClaude) {
			t.Error("install_claude() must have sudo npm as sub-fallback")
		}
	})

	t.Run("no_version_pinning", func(t *testing.T) {
		if strings.Contains(installClaude, "claude-code@") {
			t.Error("install_claude() must not pin claude-code version")
		}
	})

	t.Run("no_npm_global_prefix", func(t *testing.T) {
		if strings.Contains(installClaude, "npm-global") || strings.Contains(installClaude, "NPM_PREFIX") {
			t.Error("install_claude() must not use user-local npm prefix")
		}
	})

	t.Run("path_uses_local_bin", func(t *testing.T) {
		if !strings.Contains(installClaude, ".local/bin") {
			t.Error("install_claude() must add $HOME/.local/bin to PATH")
		}
	})

	t.Run("conditional_guard_preserved", func(t *testing.T) {
		if !strings.Contains(installClaude, "command_exists claude") {
			t.Error("install_claude() missing conditional guard (command_exists claude)")
		}
	})

	t.Run("configure_shell_path_no_npm_global", func(t *testing.T) {
		if strings.Contains(configureShell, "npm-global") {
			t.Error("configure_shell() PATH must not contain npm-global")
		}
	})

	t.Run("configure_shell_path_has_local_bin", func(t *testing.T) {
		if !strings.Contains(configureShell, ".local/bin") {
			t.Error("configure_shell() PATH must include .local/bin")
		}
	})

	t.Run("pip_require_hashes", func(t *testing.T) {
		if !strings.Contains(content, "--require-hashes") {
			t.Error("quickstart.sh missing --require-hashes in pip install")
		}
	})
}
