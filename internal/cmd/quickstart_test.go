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

// TestQuickstartConfigureFactoryDiscovery locks in tech-stack-agnostic repo
// discovery: configure_factory must find the cloned target repo by its .git
// directory alone, never by go.mod, so non-Go customer repos are selected
// (issue af-8b4ee574 / GitHub #336). The install_af AF-source go.mod check is
// separate and must be preserved.
func TestQuickstartConfigureFactoryDiscovery(t *testing.T) {
	root := findModuleRoot(t)
	data, err := os.ReadFile(filepath.Join(root, "quickstart.sh"))
	if err != nil {
		t.Fatalf("reading quickstart.sh: %v", err)
	}
	content := string(data)

	configureFactory := extractShellFunction(content, "configure_factory")
	if configureFactory == "" {
		t.Fatal("could not extract configure_factory() function body")
	}

	installAf := extractShellFunction(content, "install_af")
	if installAf == "" {
		t.Fatal("could not extract install_af() function body")
	}

	// Scenario: configure_factory discovery is stack-agnostic
	t.Run("configure_factory_discovery_is_stack_agnostic", func(t *testing.T) {
		if strings.Contains(configureFactory, "go.mod") {
			t.Error("configure_factory() must not reference go.mod: discovery must be tech-stack-agnostic (no go.mod in the loop, comment, or error message)")
		}
	})

	// Scenario: configure_factory keeps the .git filter
	t.Run("configure_factory_keeps_git_filter", func(t *testing.T) {
		if !strings.Contains(configureFactory, `[ -d "$d/.git" ]`) {
			t.Error("configure_factory() must keep the [ -d \"$d/.git\" ] filter so non-git scratch dirs (e.g. aftmp) are excluded")
		}
	})

	// Scenario: configure_factory error message no longer names go.mod
	t.Run("configure_factory_error_no_longer_names_go_mod", func(t *testing.T) {
		if !strings.Contains(configureFactory, "No git repository") {
			t.Error("configure_factory() discovery-failure error must say 'No git repository'")
		}
		if strings.Contains(configureFactory, "go.mod") {
			t.Error("configure_factory() error message must not name go.mod")
		}
	})

	// Scenario: install_af still verifies the agentfactory source go.mod
	t.Run("install_af_preserves_source_go_mod_check", func(t *testing.T) {
		if !strings.Contains(installAf, "$SCRIPT_DIR/go.mod") {
			t.Error("install_af() must keep its $SCRIPT_DIR/go.mod AF-source check")
		}
		if !strings.Contains(installAf, "agentfactory") {
			t.Error("install_af() must keep grepping the source go.mod for agentfactory")
		}
	})
}
